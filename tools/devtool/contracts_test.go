package devtool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"strconv"
	"fmt"
	"github.com/ochairo/image-viewport.nvim/tools/supervisor"

	"github.com/ochairo/image-viewport.nvim/tools/safefs"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil { t.Fatal(err) }
}

func TestAnnotationContracts(t *testing.T) {
	typed := "---@param opts Options\n---@return boolean\nfunction M.open(opts)\nend"
	if len(AnnotationErrors(typed, true)) != 0 { t.Fatal("typed signature rejected") }
	for _, text := range []string{"function M.open(opts)\nend", strings.Replace(typed, "opts Options", "other Options", 1), "---@param x any", "---@param x table", "---@return function", "---@return unknown", "---@diagnostic disable: no-unknown"} {
		if len(AnnotationErrors(text, true)) == 0 { t.Fatalf("accepted untyped contract: %s", text) }
	}
	if len(AnnotationErrors("---@param x table<string, integer>", false)) != 0 { t.Fatal("generic map rejected") }
}

func TestReportsFailClosed(t *testing.T) {
	report := filepath.Join(t.TempDir(), "check.json")
	if _, err := Diagnostics(report); err == nil { t.Fatal("missing report accepted") }
	for _, data := range []string{"", "null", `"ok"`, `{"file":{}}`, `{"file":[{}]}`, `{"file":[{"message":null}]}`, `{"file":[{"message":"x","range":null}]}`, `{"file":[{"message":"x","range":{"start":{"line":-1}}}]}`, `{"file":[{"message":"x","range":{"start":{"line":1.5}}}]}`} {
		write(t, report, data)
		if _, err := Diagnostics(report); err == nil { t.Fatalf("malformed report accepted: %s", data) }
	}
	for _, data := range []string{"{}", "[]", `{"file":[]}`} {
		write(t, report, data)
		entries, err := Diagnostics(report)
		if err != nil || len(entries) != 0 { t.Fatalf("empty report: %v %v", entries, err) }
	}
	write(t, report, `{"file:///fixture.lua":[{"message":"mismatch","code":"x"}]}`)
	entries, err := Diagnostics(report)
	if err != nil || len(entries) != 1 { t.Fatalf("diagnostic lost: %v", err) }
	alias := filepath.Join(filepath.Dir(report), "alias")
	if err := os.Symlink(report, alias); err != nil { t.Fatal(err) }
	if _, err := Diagnostics(alias); err == nil { t.Fatal("linked report accepted") }
	if err := os.Truncate(report, 16<<20+1); err != nil { t.Fatal(err) }
	if _, err := Diagnostics(report); err == nil { t.Fatal("oversized report accepted") }
}

func TestDiagnosticDisclosureBounds(t *testing.T) {
	root := t.TempDir()
	d := Diagnostic{URI: "file://"+root+"/lua/example.lua", Message: "type mismatch\n"+strings.Repeat("x", 1000), Code: "param-type-mismatch", Line: 2, Column: 3}
	entries := make([]Diagnostic, 102)
	for i := range entries { entries[i] = d }
	text := Describe(entries, root)
	if !strings.HasPrefix(text, "lua/example.lua:3:4: param-type-mismatch:") || strings.Contains(text, root) || len(strings.Split(text, "\n")) != 101 { t.Fatal("diagnostic disclosure changed") }
	for _, line := range strings.Split(text, "\n") { if len([]rune(line)) > 600 { t.Fatal("unbounded diagnostic") } }
	d.URI = "file:///outside/private.lua"
	if Describe([]Diagnostic{d}, root) != "external Lua library: diagnostic (details withheld)" { t.Fatal("external location disclosed") }
	if _, err := Executable("definitely-missing-plugin-checker"); err == nil { t.Fatal("missing tool accepted") }
}

func TestLockedDependencies(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lock.json")
	good := `{"plugin":{"repository":"https://github.com/owner/plugin","commit":"`+strings.Repeat("a",40)+`"}}`
	write(t, path, good)
	if _, err := ReadLock(path); err != nil { t.Fatal(err) }
	for _, data := range []string{`[]`, `{}`, strings.Replace(good, `"plugin":`, `"../escape":`, 1), strings.ReplaceAll(good, strings.Repeat("a", 40), "main"), strings.ReplaceAll(good, "https://github.com/owner/plugin", "file:///tmp/source"), strings.ReplaceAll(good, "https://github.com/owner/plugin", "https://user:placeholder@github.com/owner/plugin")} {
		write(t, path, data)
		if _, err := ReadLock(path); err == nil { t.Fatal("invalid lock accepted") }
	}
	if err := os.Mkdir(filepath.Join(root,"existing"), 0700); err != nil { t.Fatal(err) }
	write(t, filepath.Join(root,"existing/foreign"), "keep")
	if _, err := safefs.NewDestination(filepath.Join(root,"existing")); err == nil { t.Fatal("existing output adopted") }
	data, err := os.ReadFile(filepath.Join(root,"existing/foreign"))
	if err != nil || string(data) != "keep" { t.Fatal("foreign data changed") }
	if err := os.Symlink(root, filepath.Join(root,"alias")); err != nil { t.Fatal(err) }
	if _, err := safefs.NewDestination(filepath.Join(root,"alias/new")); err == nil { t.Fatal("symlink parent accepted") }
}

func TestMaterializeRegularBlobsOnly(t *testing.T) {
	for _, mode := range []string{"100644", "100755", "120000"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			private, env, err := isolated("dependency-contract-")
			if err != nil { t.Fatal(err) }
			defer os.RemoveAll(private)
			repository := filepath.Join(private,"objects")
			if _, err := git(ctx, private, env, nil, 1<<20, "init", "--bare", "--quiet", "--template=", repository); err != nil { t.Fatal(err) }
			blob, err := git(ctx, repository, env, []byte("fixture\n"), 128, "hash-object", "-w", "--stdin")
			if err != nil { t.Fatal(err) }
			tree, err := git(ctx, repository, env, []byte(mode+" blob "+strings.TrimSpace(string(blob))+"\tsource.lua\n"), 128, "mktree")
			if err != nil { t.Fatal(err) }
			err = Materialize(ctx, repository, strings.TrimSpace(string(tree)), env)
			if mode == "120000" { if err == nil { t.Fatal("symlink blob accepted") }; return }
			if err != nil { t.Fatal(err) }
			data, err := os.ReadFile(filepath.Join(repository,"checkout/source.lua"))
			if err != nil || string(data) != "fixture\n" { t.Fatal("wrong blob content") }
		})
	}
}

func TestAcquisitionRejectsBeforeNetwork(t *testing.T) {
	root := t.TempDir()
	data, err := json.Marshal(map[string]Dependency{"plugin": {Repository:"https://github.com/owner/plugin", Commit:strings.Repeat("a",40)}})
	if err != nil { t.Fatal(err) }
	write(t, filepath.Join(root,"dependencies.json"), string(data))
	if err := Fetch(context.Background(), root, root); err == nil { t.Fatal("existing fetch destination accepted") }
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__worker" { os.Exit(supervisor.Worker(os.Args[2:])) }
	os.Exit(m.Run())
}

func TestCommandStopsDescendants(t *testing.T) {
	for _, mode := range []string{"timeout","cancel","overflow","completed-parent"} {
		t.Run(mode,func(t *testing.T) {
			private := t.TempDir()
			ctx,cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "timeout" { var timeout context.CancelFunc; ctx,timeout = context.WithTimeout(ctx,200*time.Millisecond); defer timeout() }
			script := `sleep 30 & echo $! > child; `
			switch mode {
			case "overflow": script += `while :; do printf 'xxxxxxxxxxxxxxxx'; done`
			case "completed-parent": script += `exit 0`
			default: script += `wait`
			}
			if mode == "cancel" {
				done := make(chan struct{})
				defer func() { cancel(); <-done }()
				go func() { defer close(done); for { if _,err := os.Stat(filepath.Join(private,"child")); err == nil { cancel(); return }; select { case <-ctx.Done(): return; case <-time.After(5*time.Millisecond): } } }()
			}
			_,err := command(ctx,private,[]string{"PATH=/usr/bin:/bin"},nil,128,"/bin/sh","-c",script)
			if (err == nil) != (mode == "completed-parent") { t.Fatalf("command result %v",err) }
			data,err := os.ReadFile(filepath.Join(private,"child"))
			if err != nil { t.Fatal(err) }
			pid,err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil { t.Fatal(err) }
			stat,err := os.ReadFile(fmt.Sprintf("/proc/%d/stat",pid))
			if os.IsNotExist(err) { return }
			if err != nil { t.Fatal(err) }
			fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat),')')+1:]))
			if len(fields) == 0 || fields[0] != "Z" { t.Fatal("descendant remains executable") }
		})
	}
}
