package devtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ochairo/image-viewport.nvim/tools/safefs"
)

type Dependency struct {
	Repository string `json:"repository"`
	Commit string `json:"commit"`
}

func ReadLock(path string) (map[string]Dependency, error) {
	data, err := safefs.Read(filepath.Dir(path), filepath.Base(path), 1<<20)
	if err != nil { return nil, err }
	var entries map[string]Dependency
	if json.Unmarshal(data, &entries) != nil || len(entries) == 0 || len(entries) > 16 { return nil, fmt.Errorf("invalid dependency lock") }
	for name, entry := range entries {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(name) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(entry.Commit) || !regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(entry.Repository) { return nil, fmt.Errorf("invalid locked dependency") }
	}
	return entries, nil
}

func git(ctx context.Context, cwd string, env []string, input []byte, limit int, arguments ...string) ([]byte, error) {
	args := []string{"/usr/bin/git", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "fetch.fsckObjects=true"}
	return command(ctx, cwd, env, input, limit, append(args, arguments...)...)
}

func Materialize(ctx context.Context, repository, commit string, env []string) error {
	listing, err := git(ctx, repository, env, nil, 2<<20, "ls-tree", "-rz", "--full-tree", commit)
	if err != nil { return err }
	records := strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00")
	if len(listing) == 0 { records = nil }
	if len(records) > 10000 { return fmt.Errorf("dependency tree exceeds bound") }
	output, err := safefs.NewDestination(filepath.Join(repository, "checkout"))
	if err != nil { return err }
	defer output.Close()
	var total int64
	for _, record := range records {
		header, name, found := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !found || len(fields) != 3 || !utf8.ValidString(name) || !filepath.IsLocal(name) || filepath.Clean(name) != name || strings.IndexFunc(name, unicode.IsControl) >= 0 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(fields[2]) { return fmt.Errorf("unsupported dependency tree entry") }
		for _, part := range strings.Split(name, "/") { if part == "." || part == ".." || part == ".git" { return fmt.Errorf("unsafe dependency path") } }
		sizeData, err := git(ctx, repository, env, nil, 128, "cat-file", "-s", fields[2])
		if err != nil { return err }
		size, err := strconv.ParseInt(strings.TrimSpace(string(sizeData)), 10, 64)
		total += size
		if err != nil || size < 0 || size > 16<<20 || total > 64<<20 { return fmt.Errorf("dependency content exceeds bound") }
		data, err := git(ctx, repository, env, nil, 16<<20, "cat-file", "blob", fields[2])
		if err != nil { return err }
		if int64(len(data)) != size { return fmt.Errorf("dependency object changed") }
		if err := output.MkdirAll(filepath.Dir(name), 0700); err != nil { return err }
		mode := os.FileMode(0644)
		if fields[0] == "100755" { mode = 0755 }
		f, err := output.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil { return err }
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil { return err }; if closeErr != nil { return closeErr }
	}
	return nil
}

func lockPath(root string) string {
	path := filepath.Join(root, "tests/dependencies.json")
	if _, err := os.Stat(path); os.IsNotExist(err) { path = filepath.Join(root, "dependencies.json") }
	return path
}

func Fetch(ctx context.Context, root, destination string) error {
	entries, err := ReadLock(lockPath(root))
	if err != nil { return err }
	destination, err = filepath.Abs(destination)
	if err != nil { return err }
	output, err := safefs.NewDestination(destination)
	if err != nil { return err }
	defer output.Close()
	private, env, err := isolated("plugin-fetch-")
	if err != nil { return err }
	defer os.RemoveAll(private)
	env = append(env, "GIT_ALLOW_PROTOCOL=https")
	if _, err := os.Stat("/tools/etc/ssl/certs/ca-bundle.crt"); err == nil { env = append(env, "GIT_SSL_CAINFO=/tools/etc/ssl/certs/ca-bundle.crt") }
	var names []string
	for name := range entries { names = append(names, name) }
	sort.Strings(names)
	for _, name := range names {
		if err := output.Validate(); err != nil { return err }
		entry := entries[name]
		repository := filepath.Join(destination, name)
		if _, err := git(ctx, destination, env, nil, 1<<20, "init", "--bare", "--quiet", "--template=", repository); err != nil { return err }
		if _, err := git(ctx, repository, env, nil, 1<<20, "fetch", "--quiet", "--depth=1", "--no-tags", "--no-recurse-submodules", entry.Repository, entry.Commit); err != nil { return err }
		actual, err := git(ctx, repository, env, nil, 128, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
		if err != nil { return err }
		if strings.TrimSpace(string(actual)) != entry.Commit { return fmt.Errorf("fetched commit differs from lock") }
		if err := Materialize(ctx, repository, entry.Commit, env); err != nil { return err }
	}
	if err := output.Validate(); err != nil { return err }
	fmt.Println(destination)
	return nil
}
