package devtool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func EditorTests(ctx context.Context, root string, upstream bool) error {
	nvim, err := tool(os.Getenv("NVIM"), "nvim")
	if err != nil { return err }
	private, env, err := isolated("image-viewport-tests-")
	if err != nil { return err }
	defer os.RemoveAll(private)
	env = append(env, "IMAGE_VIEWPORT_TEST_ROOT="+root)
	scripts := []string{"setup.lua", "setup_failure.lua"}
	if upstream {
		dependencies := os.Getenv("IMAGE_VIEWPORT_DEPENDENCIES")
		if dependencies == "" { return fmt.Errorf("set IMAGE_VIEWPORT_DEPENDENCIES to acquired locked dependency objects") }
		source, err := filepath.Abs(filepath.Join(dependencies, "image.nvim"))
		if err != nil { return err }
		source, err = filepath.EvalSymlinks(source)
		if err != nil { return err }
		entries, err := ReadLock(lockPath(root))
		if err != nil { return err }
		entry, ok := entries["image.nvim"]
		if !ok { return fmt.Errorf("image.nvim is missing from lock") }
		repository := filepath.Join(private, "objects")
		if _, err := git(ctx, private, env, nil, 1<<20, "init", "--bare", "--quiet", "--template=", repository); err != nil { return err }
		if _, err := git(ctx, repository, env, nil, 1<<20, "-c", "protocol.file.allow=always", "-c", "safe.directory="+source, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", source, entry.Commit); err != nil { return err }
		if err := Materialize(ctx, repository, entry.Commit, env); err != nil { return err }
		env = append(env, "IMAGE_VIEWPORT_UPSTREAM="+filepath.Join(repository, "checkout"))
		scripts = []string{"upstream.lua"}
	}
	for _, script := range scripts {
		out, err := command(ctx, private, env, nil, 4<<20, nvim, "--clean", "--headless", "-u", "NONE", "-i", "NONE", "--noplugin", "-l", filepath.Join(root, "tests", script))
		if err != nil { return err }
		fmt.Print(string(out))
	}
	return nil
}
