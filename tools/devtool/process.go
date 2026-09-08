// Package devtool owns isolated development commands, never plugin runtime effects.
package devtool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"github.com/ochairo/image-viewport.nvim/tools/supervisor"
)

type boundedOutput struct {
	data []byte
	limit int
	err error
	cancel context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-len(b.data) { b.err = fmt.Errorf("tool output exceeds bound"); if b.cancel != nil { b.cancel() }; return 0, b.err }
	b.data = append(b.data, p...)
	return len(p), nil
}

func command(ctx context.Context, cwd string, env []string, input []byte, limit int, args ...string) ([]byte, error) {
	if len(args) == 0 { return nil, fmt.Errorf("empty tool command") }
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	private, err := os.MkdirTemp("", "plugin-command-")
	if err != nil { return nil, err }
	defer os.RemoveAll(private)
	stdin := ""
	if input != nil {
		stdin = filepath.Join(private,"stdin")
		if err := os.WriteFile(stdin,input,0600); err != nil { return nil, err }
	}
	out := &boundedOutput{limit:limit, cancel:cancel}
	diagnostic := &boundedOutput{limit:1<<20, cancel:cancel}
	statuses, err := supervisor.Supervise(ctx, [][]string{args}, supervisor.Options{CWD:cwd, Environment:env, Stdin:stdin, Deadline:300*time.Second, Stdout:out, Stderr:diagnostic})
	if err != nil { return nil, err }
	if out.err != nil { return nil, out.err }; if diagnostic.err != nil { return nil, diagnostic.err }
	if statuses[0] != 0 { return nil, fmt.Errorf("%s failed with status %d (private tool output withheld)", filepath.Base(args[0]),statuses[0]) }
	return out.data, nil
}

func Executable(name string) (string, error) {
	found, err := exec.LookPath(name)
	if err != nil { return "", fmt.Errorf("required executable unavailable: %s", name) }
	found, err = filepath.Abs(found)
	if err != nil { return "", err }
	return filepath.EvalSymlinks(found)
}

func tool(name, fallback string) (string, error) {
	if name == "" { name = fallback }
	return Executable(name)
}

func isolated(prefix string) (string, []string, error) {
	private, err := os.MkdirTemp("", prefix)
	if err != nil { return "", nil, err }
	env := []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "NVIM_LOG_FILE="+filepath.Join(private, "nvim.log")}
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMPDIR"} {
		path := filepath.Join(private, strings.ToLower(name))
		if err := os.Mkdir(path, 0700); err != nil { os.RemoveAll(private); return "", nil, err }
		env = append(env, name+"="+path)
	}
	return private, env, nil
}
