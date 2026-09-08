package devtool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ochairo/image-viewport.nvim/tools/safefs"
)

type Diagnostic struct {
	URI string
	Message string
	Code string
	Line int
	Column int
}

func Diagnostics(report string) ([]Diagnostic, error) {
	data, err := safefs.Read(filepath.Dir(report), filepath.Base(report), 16<<20)
	if err != nil { return nil, fmt.Errorf("LuaLS report missing or invalid: %w", err) }
	if strings.TrimSpace(string(data)) == "[]" { return nil, nil }
	var collection map[string]json.RawMessage
	if err := json.Unmarshal(data, &collection); err != nil || collection == nil { return nil, fmt.Errorf("LuaLS report must map source URIs to diagnostics") }
	var result []Diagnostic
	for uri, raw := range collection {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil || entries == nil { return nil, fmt.Errorf("malformed LuaLS diagnostic collection") }
		for _, entry := range entries {
			var message string
			if entry == nil || len(entry["message"]) == 0 || string(entry["message"]) == "null" || json.Unmarshal(entry["message"], &message) != nil { return nil, fmt.Errorf("malformed LuaLS diagnostic") }
			d := Diagnostic{URI: uri, Message: message, Code: "diagnostic"}
			if code := entry["code"]; len(code) > 0 {
				if json.Unmarshal(code, &d.Code) != nil { d.Code = string(code) }
			}
			if region, exists := entry["range"]; exists {
				var fields map[string]json.RawMessage
				if json.Unmarshal(region, &fields) != nil || fields == nil { return nil, fmt.Errorf("malformed LuaLS range") }
				if start, exists := fields["start"]; exists {
					var rawPosition map[string]json.RawMessage
					if json.Unmarshal(start, &rawPosition) != nil || rawPosition == nil { return nil, fmt.Errorf("malformed LuaLS position") }
					for _, raw := range rawPosition { if string(raw) == "null" { return nil, fmt.Errorf("malformed LuaLS position") } }
					var position map[string]int
					if json.Unmarshal(start, &position) != nil || position == nil { return nil, fmt.Errorf("malformed LuaLS position") }
					for _, n := range position { if n < 0 { return nil, fmt.Errorf("negative LuaLS position") } }
					d.Line, d.Column = position["line"], position["character"]
				}
			}
			result = append(result, d)
		}
	}
	return result, nil
}

func Describe(entries []Diagnostic, root string) string {
	var lines []string
	for i, d := range entries {
		if i == 100 { lines = append(lines, fmt.Sprintf("%d additional diagnostics omitted", len(entries)-100)); break }
		u, err := url.Parse(d.URI)
		var relative string
		if err == nil && u.Scheme == "file" && (u.Host == "" || u.Host == "localhost") {
			resolved := u.Path
			if canonical, e := filepath.EvalSymlinks(resolved); e == nil { resolved = canonical }
			relative, err = filepath.Rel(root, resolved)
		} else { err = fmt.Errorf("external source") }
		if err != nil || !filepath.IsLocal(relative) { lines = append(lines, "external Lua library: diagnostic (details withheld)"); continue }
		text := fmt.Sprintf("%s:%d:%d: %s: %s", relative, d.Line+1, d.Column+1, d.Code, strings.ReplaceAll(d.Message, root, "."))
		text = strings.Map(func(r rune) rune { if unicode.IsPrint(r) { return r }; return ' ' }, text)
		runes := []rune(text)
		if len(runes) > 600 { text = string(runes[:600]) }
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

func Typecheck(ctx context.Context, root string) error {
	server, err := tool(os.Getenv("LUA_LS"), "lua-language-server")
	if err != nil { return err }
	nvim, err := tool(os.Getenv("NVIM"), "nvim")
	if err != nil { return err }
	private, env, err := isolated("plugin-luals-")
	if err != nil { return err }
	defer os.RemoveAll(private)
	data, err := command(ctx, private, env, nil, 1<<20, nvim, "--clean", "--headless", "-u", "NONE", "-i", "NONE", "--noplugin", "-c", "lua io.write(vim.env.VIMRUNTIME)", "-c", "qa!")
	if err != nil { return err }
	runtime := string(data)
	info, err := os.Stat(filepath.Join(runtime, "lua/vim"))
	if !filepath.IsAbs(runtime) || err != nil || !info.IsDir() { return fmt.Errorf("Neovim annotation library unavailable") }
	settingsData, err := os.ReadFile(filepath.Join(root, ".luarc.json"))
	if err != nil { return err }
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(settingsData, &settings); err != nil { return err }
	settings["workspace.library"], err = json.Marshal([]string{filepath.Join(runtime, "lua")})
	if err != nil { return err }
	settingsData, err = json.Marshal(settings)
	if err != nil { return err }
	config := filepath.Join(private, "luarc.json")
	if err := os.WriteFile(config, settingsData, 0600); err != nil { return err }
	check := func(workspace, phase string) ([]Diagnostic, error, error) {
		dir := filepath.Join(private, phase)
		if err := os.Mkdir(dir, 0700); err != nil { return nil, err, err }
		report := filepath.Join(dir, "check.json")
		_, status := command(ctx, workspace, env, nil, 1<<20, server, "--check", workspace, "--checklevel=Warning", "--check_format=json", "--check_out_path", report, "--configpath", config, "--logpath", filepath.Join(dir, "log"), "--metapath", filepath.Join(dir, "meta"))
		entries, err := Diagnostics(report)
		return entries, status, err
	}
	canary := filepath.Join(private, "canary")
	if err := os.Mkdir(canary, 0700); err != nil { return err }
	if err := os.WriteFile(filepath.Join(canary, "bad.lua"), []byte("---@param value integer\n---@return integer\nlocal function increment(value) return value + 1 end\nincrement('wrong')\n"), 0600); err != nil { return err }
	entries, _, err := check(canary, "canary-report")
	if err != nil { return err }
	found := false
	for _, d := range entries { found = found || d.Code == "param-type-mismatch" }
	if !found { return fmt.Errorf("LuaLS failed to diagnose typed-call canary") }
	entries, status, err := check(root, "source-report")
	if err != nil { return err }
	if status != nil || len(entries) > 0 { return fmt.Errorf("LuaLS failed (%v):\n%s", status, Describe(entries, root)) }
	fmt.Println("LuaLS runtime typecheck passed")
	return nil
}
