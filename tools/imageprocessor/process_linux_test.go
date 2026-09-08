//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"github.com/ochairo/image-viewport.nvim/tools/supervisor"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__worker" { os.Exit(supervisor.Worker(os.Args[2:])) }
	if len(os.Args) > 1 && os.Args[1] == "__anchor" {
		os.Exit(Anchor(os.Args[2:]))
	}
	if len(os.Args) == 3 && os.Args[1] == "__supervisor-fixture" {
		input, err := memfd()
		if err != nil {
			os.Exit(98)
		}
		defer input.Close()
		self, err := os.Executable()
		if err != nil {
			os.Exit(98)
		}
		_, _, _ = superviseSandbox(context.Background(), self, []string{"/bin/sh", "-c", `printf '%s %s' "$PPID" "$$" > "$1"; sleep 30 & wait`, "fixture", os.Args[2]}, input, nil)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSupervisorDeathStopsAnchor(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := exec.Command(self, "__supervisor-fixture", pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var pids []string
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		data, _ := os.ReadFile(pidFile)
		pids = strings.Fields(string(data))
		if len(pids) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pids) != 2 {
		t.Fatal("supervised child did not start")
	}
	anchor, err := strconv.Atoi(pids[0])
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-anchor, syscall.SIGKILL)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	until = time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		stopped := true
		for _, pid := range pids {
			data, err := os.ReadFile("/proc/" + pid + "/stat")
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
			if len(fields) == 0 || fields[0] != "Z" {
				stopped = false
			}
		}
		if stopped {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("launcher death left the anchor or child executable")
}

func TestSandboxSupervisor(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"success", "failure", "timeout", "descendant", "process-limit"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			pidFile := filepath.Join(root, "pid")
			input, err := memfd()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			ctx := context.Background()
			expected := 0
			program := `printf done`
			switch kind {
			case "failure":
				program = `exit 17`
				expected = 17
			case "timeout":
				program = `sleep 30`
				expected = 75
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			case "descendant":
				program = `sleep 30 & printf '%s' "$!" > "$1"; exit 0`
			case "process-limit":
				program = `i=0; while [ "$i" -lt 40 ]; do sleep 30 & i=$((i+1)); done; wait`
				expected = 75
			}
			started := time.Now()
			code, out, err := superviseSandbox(ctx, self, []string{"/bin/sh", "-c", program, "fixture", pidFile}, input, nil)
			if code != expected || err != nil && kind != "timeout" {
				t.Fatalf("supervisor: %d %q %v", code, out, err)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("cleanup exceeded deadline")
			}
			if kind == "success" && out != "done" {
				t.Fatal(out)
			}
			if kind == "descendant" {
				data, err := os.ReadFile(pidFile)
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(data))
				if err != nil {
					t.Fatal(err)
				}
				stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if os.IsNotExist(err) {
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
				if len(fields) == 0 || fields[0] != "Z" {
					t.Fatal("descendant remains executable")
				}
			}
		})
	}
}

func TestRuntimeFingerprint(t *testing.T) {
	for _, defect := range []string{"none", "mode", "link", "hardlink", "extra", "missing"} {
		t.Run(defect, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"image-launch", "image-worker", "policy.xml", "source.sha256"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			candidate := filepath.Join(root, "image-worker")
			switch defect {
			case "mode":
				check(os.Chmod(candidate, 0777))
			case "link":
				check(os.Remove(candidate))
				check(os.Symlink("image-launch", candidate))
			case "hardlink":
				check(os.Link(candidate, filepath.Join(t.TempDir(), "alias")))
			case "extra":
				check(os.WriteFile(filepath.Join(root, "extra"), nil, 0600))
			case "missing":
				check(os.Remove(candidate))
			}
			parent, err := os.Open(root)
			check(err)
			defer parent.Close()
			digest, err := fingerprintAt(parent)
			if (err == nil) != (defect == "none") {
				t.Fatalf("fingerprint: %s %v", digest, err)
			}
			if defect == "none" {
				check(os.WriteFile(filepath.Join(root, "policy.xml"), []byte("changed policy"), 0600))
				changed, err := fingerprintAt(parent)
				check(err)
				if digest == changed {
					t.Fatal("policy change retained runtime identity")
				}
			}
		})
	}
}
