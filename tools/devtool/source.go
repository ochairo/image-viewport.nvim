package devtool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var annotation = regexp.MustCompile(`^\s*---@`)
var escapeType = regexp.MustCompile(`\b(any|unknown|function)\b|\btable\b`)
var signature = regexp.MustCompile(`^function\s+[\w.:]+\(([^)]*)\)`)
var suppression = regexp.MustCompile(`^\s*---@diagnostic\s+disable`)

func AnnotationErrors(text string, required bool) []string {
	var result []string
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if annotation.MatchString(line) {
			clean := regexp.MustCompile(`\btable\s*<`).ReplaceAllString(line, "map<")
			if escapeType.MatchString(clean) || suppression.MatchString(line) { result = append(result, fmt.Sprintf("line %d: annotation escape hatch", i+1)) }
		}
		match := signature.FindStringSubmatch(line)
		if !required || match == nil { continue }
		start := i
		for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "---") { start-- }
		notes := strings.Join(lines[start:i], "\n")
		for _, param := range strings.Split(match[1], ",") {
			param = strings.TrimSpace(param)
			if param != "" && !regexp.MustCompile(`---@param\s+`+regexp.QuoteMeta(param)+`\??\s+\S+`).MatchString(notes) { result = append(result, fmt.Sprintf("line %d: missing parameter type for %s", i+1, param)) }
		}
		if !regexp.MustCompile(`---@return\s+\S+`).MatchString(notes) { result = append(result, fmt.Sprintf("line %d: missing return type", i+1)) }
	}
	return result
}

func SourceCheck(ctx context.Context, root string) error {
	var contract struct { Modules []string `json:"annotated_modules"` }
	data, err := os.ReadFile(filepath.Join(root, "api-contracts.json"))
	if err != nil { return err }
	if err := json.Unmarshal(data, &contract); err != nil { return err }
	if len(contract.Modules) == 0 { return fmt.Errorf("missing annotated module contract") }
	required := map[string]bool{}
	for _, name := range contract.Modules { required[name] = true }
	seen := map[string]bool{}
	var failures []string
	for _, directory := range []string{"lua", "plugin", "tests", "scripts", "runtime", "integrations", "tools"} {
		base := filepath.Join(root, directory)
		if _, err := os.Stat(base); os.IsNotExist(err) { continue }
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil { return err }
			if entry.IsDir() {
				if strings.HasPrefix(entry.Name(), "image-processor-") || entry.Name() == "__pycache__" { return filepath.SkipDir }
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil { return err }
			if rel == "runtime/current" { return nil } // Build selector is not source.
			if !entry.Type().IsRegular() { return fmt.Errorf("nonregular source: %s", rel) }
			if filepath.Ext(path) == ".py" { return fmt.Errorf("repository-owned Python is forbidden: %s", rel) }
			if filepath.Ext(path) == ".go" { return nil } // Go compiler/vet own this syntax.
			data, err := os.ReadFile(path)
			if err != nil { return err }
			text := string(data)
			if strings.HasPrefix(text, "#!/bin/sh") {
				if _, err := command(ctx, root, os.Environ(), nil, 1<<20, "/bin/sh", "-n", path); err != nil { return err }
			}
			if filepath.Ext(path) == ".lua" {
				seen[rel] = true
				for _, failure := range AnnotationErrors(text, required[rel]) { failures = append(failures, rel+": "+failure) }
				if strings.HasPrefix(rel, "lua/") && regexp.MustCompile(`require\(["']config\.`).MatchString(text) { failures = append(failures, rel+": personal configuration dependency") }
			}
			return nil
		})
		if err != nil { return err }
	}
	for name := range required { if !seen[name] { failures = append(failures, "missing annotated module: "+name) } }
	if len(failures) > 0 { return fmt.Errorf("%s", strings.Join(failures, "\n")) }
	fmt.Println("Shell syntax and annotation policy passed; semantic types require LuaLS")
	return nil
}
