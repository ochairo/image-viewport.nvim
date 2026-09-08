//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func processParent(record []byte) (int, error) {
	closing := strings.LastIndexByte(string(record), ')')
	if closing < 2 {
		return 0, fmt.Errorf("process stat has no command boundary")
	}
	fields := strings.Fields(string(record[closing+1:]))
	if len(fields) < 2 || len(fields[0]) != 1 {
		return 0, fmt.Errorf("process stat is incomplete")
	}
	return strconv.Atoi(fields[1])
}

func processTree(root int) (int, int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0, err
	}
	type process struct{ parent, rss int }
	processes := map[int]process{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if os.IsNotExist(err) || os.IsPermission(err) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		parent, err := processParent(stat)
		if err != nil {
			return 0, 0, err
		}
		status, err := os.ReadFile("/proc/" + entry.Name() + "/status")
		if os.IsNotExist(err) || os.IsPermission(err) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		rss := 0
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) != 3 || fields[2] != "kB" {
					return 0, 0, fmt.Errorf("invalid process RSS")
				}
				rss, err = strconv.Atoi(fields[1])
				if err != nil || rss < 0 {
					return 0, 0, fmt.Errorf("invalid process RSS")
				}
			}
		}
		processes[pid] = process{parent, rss}
	}
	selected := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for pid, p := range processes {
			if !selected[pid] && selected[p.parent] {
				selected[pid] = true
				changed = true
			}
		}
	}
	rss := 0
	for pid := range selected {
		rss += processes[pid].rss
	}
	return len(selected), rss, nil
}

// Anchor holds the process-group leader unreaped until supervisor cleanup.
// Only fixed launcher-generated commands reach this internal entry point.
func Anchor(args []string) int {
	if len(args) == 0 {
		return 64
	}
	// Linux parent-death signals follow the creating thread. Keep that thread
	// alive for the anchor lifetime, including the fork/exec startup window.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	input, output := os.NewFile(3, "input"), os.NewFile(4, "output")
	status, hold := os.NewFile(5, "status"), os.NewFile(6, "hold")
	defer input.Close()
	defer output.Close()
	defer status.Close()
	defer hold.Close()
	for fd := 3; fd <= 6; fd++ {
		syscall.CloseOnExec(fd)
	}
	// The supervisor owns the sole write end. Observe its disappearance while
	// the command is running, including SIGKILL and crashes. Control descriptors
	// never reach the command, so descendants cannot postpone this EOF.
	go func() {
		_, _ = io.Copy(io.Discard, hold)
		_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
		os.Exit(70)
	}()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Env = []string{}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{input, output}
	code := 70
	if err := cmd.Run(); err == nil {
		code = 0
	} else if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
		if code < 0 {
			code = 70
		}
	}
	if _, err := fmt.Fprintln(status, code); err != nil {
		return 70
	}
	select {}
}

func sandboxArguments(runtime string, output bool, workerArguments []string) []string {
	args := []string{
		"/usr/bin/prlimit", "--as=1610612736:1610612736", "--cpu=10:10", "--fsize=67108864:67108864", "--nofile=128:128", "--",
		"/usr/bin/bwrap", "--die-with-parent", "--new-session", "--unshare-all", "--unshare-user", "--disable-userns", "--clearenv",
	}
	for _, pair := range decoderEnvironment {
		key, value, _ := strings.Cut(pair, "=")
		args = append(args, "--setenv", key, value)
	}
	args = append(args,
		"--size", "134217728", "--tmpfs", "/tmp", "--dir", "/tmp/home", "--dir", "/work", "--dir", "/input", "--dir", "/output", "--dir", "/policy",
		"--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib",
		"--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache", "--ro-bind", "/etc/fonts", "/etc/fonts",
		// Fontconfig can build a user cache under the private HOME on a fresh guest.
		"--ro-bind-try", "/var/cache/fontconfig", "/var/cache/fontconfig",
		"--ro-bind", runtime, "/runtime", "--ro-bind", runtime+"/policy.xml", "/policy/policy.xml", "--ro-bind-data", "3", "/input/source",
	)
	if output {
		args = append(args, "--bind-fd", "4", "/output/frame")
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--chdir", "/work", "/runtime/image-worker")
	return append(args, workerArguments...)
}

func runSandbox(ctx context.Context, runtime string, input, output *os.File, arguments []string) (int, string, error) {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return 70, "", err
	}
	self, err := os.Executable()
	if err != nil {
		return 70, "", err
	}
	return superviseSandbox(ctx, self, sandboxArguments(runtime, output != nil, arguments), input, output)
}

func superviseSandbox(ctx context.Context, self string, args []string, input, output *os.File) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return 75, "", err
	}
	if output == nil {
		var err error
		output, err = os.OpenFile("/dev/null", os.O_RDWR, 0)
		if err != nil {
			return 70, "", err
		}
		defer output.Close()
	}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		return 70, "", err
	}
	defer statusR.Close()
	defer statusW.Close()
	holdR, holdW, err := os.Pipe()
	if err != nil {
		return 70, "", err
	}
	defer holdR.Close()
	defer holdW.Close()
	cmd := exec.Command(self, append([]string{"__anchor"}, args...)...)
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{input, output, statusW, holdR}
	stdout, stderr := &boundedBuffer{limit: 64 << 10}, &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return 70, "", err
	}
	statusW.Close()
	holdR.Close()
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(statusR, 32)).ReadString('\n')
		code := 70
		if err == nil {
			code, err = strconv.Atoi(strings.TrimSpace(line))
		}
		done <- result{code, err}
	}()
	received := false
	cleanup := func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		time.Sleep(250 * time.Millisecond)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		statusR.Close()
		holdW.Close()
		if !received {
			<-done
		}
	}
	// stdout/stderr are read only after Wait joins the pipe-copy goroutines.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-done:
			received = true
			cleanup()
			if err := ctx.Err(); err != nil {
				return 75, "", err
			}
			return result.code, stdout.String(), result.err
		case <-ctx.Done():
			cleanup()
			return 75, "", ctx.Err()
		case <-ticker.C:
			count, rss, err := processTree(cmd.Process.Pid)
			if err != nil {
				cleanup()
				return 70, "", err
			}
			if count > 32 || rss > 1024*1024 {
				cleanup()
				return 75, "", nil
			}
		}
	}
}
