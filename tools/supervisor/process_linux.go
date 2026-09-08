//go:build linux

// Package supervisor owns isolated development workers, never provider operations.
package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Worker runs inside the supervisor's unreaped process-group anchor. Status is
// reported through FD3; FD4 keeps the anchor alive until the group is stopped.
func Worker(arguments []string) int {
	group, err := syscall.Getpgid(os.Getpid())
	if err != nil || group != os.Getpid() { return 2 }
	if len(arguments) == 0 {
		return 2
	}
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	status := os.NewFile(3, "worker-status")
	hold := os.NewFile(4, "worker-hold")
	if status == nil || hold == nil {
		return 2
	}
	defer status.Close()
	defer hold.Close()
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	// EOF means the supervisor has gone away. Keep the anchor alive while
	// monitoring this pipe, so its group identity cannot be reused.
	go func() {
		_, _ = io.Copy(io.Discard, hold)
		_ = syscall.Kill(-os.Getpid(),syscall.SIGKILL)
	}()
	cmd := exec.Command(arguments[0], arguments[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintln(status, "start-error")
		select {}
	}
	err = cmd.Wait()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if state, ok := exit.Sys().(syscall.WaitStatus); ok && state.Signaled() {
				code = -int(state.Signal())
			} else {
				code = exit.ExitCode()
			}
		} else {
			code = 127
		}
	}
	if _, err := fmt.Fprintln(status, code); err != nil {
		return 2
	}
	select {}
}

type Options struct {
	Environment                      []string
	CWD                              string
	Deadline                         time.Duration
	Directory, Phase, Barrier, Stdin string
	Stdout, Stderr io.Writer
}
type outcome struct {
	code int
	err  error
}
type worker struct {
	command *exec.Cmd
	status  *os.File
	hold    *os.File
	done    chan outcome
	result  *outcome
}
type supervisor struct {
	prefix     []string
	afterStart func(int)
}

func Supervise(ctx context.Context, commands [][]string, options Options) ([]int, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return (supervisor{prefix: []string{executable, "__worker"}}).run(ctx, commands, options)
}

func output(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		state := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || state.Nlink != 1 || state.Uid != uint32(os.Getuid()) {
			err = fmt.Errorf("unsafe worker output")
		}
	}
	if err == nil {
		err = f.Truncate(0)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func stop(workers []*worker) {
	for _, w := range workers {
		_ = syscall.Kill(-w.command.Process.Pid, syscall.SIGTERM)
	}
	if len(workers) > 0 {
		time.Sleep(200 * time.Millisecond)
	}
	for _, w := range workers {
		_ = syscall.Kill(-w.command.Process.Pid, syscall.SIGKILL)
	}
	for _, w := range workers {
		_ = w.command.Wait()
		w.status.Close()
		w.hold.Close()
		if w.result == nil {
			<-w.done
		}
	}
}

func (s supervisor) run(ctx context.Context, commands [][]string, options Options) ([]int, error) {
	if len(commands) == 0 || len(s.prefix) == 0 || options.Deadline <= 0 {
		return nil, fmt.Errorf("invalid supervisor arguments")
	}
	if options.Directory != "" {
		switch options.Phase {
		case "create", "submit", "import", "recovery":
		default:
			return nil, fmt.Errorf("invalid race phase")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, options.Deadline)
	defer cancel()
	var workers []*worker
	var files []*os.File
	defer func() {
		stop(workers)
		for _, f := range files {
			f.Close()
		}
	}()
	for index, command := range commands {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		if len(command) == 0 {
			return nil, fmt.Errorf("empty worker command")
		}
		cmd := exec.Command(s.prefix[0], append(append([]string{}, s.prefix[1:]...), command...)...)
		cmd.Dir = options.CWD
		cmd.Env = append(append([]string{}, options.Environment...), "GIT_DASH_WORKER="+strconv.Itoa(index+1))
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if options.Stdout != nil { cmd.Stdout = options.Stdout }
		if options.Stderr != nil { cmd.Stderr = options.Stderr }
		cmd.WaitDelay = time.Second
		if options.Directory != "" {
			out, err := output(filepath.Join(options.Directory, fmt.Sprintf("%s-%d.out", options.Phase, index+1)))
			if err != nil {
				return nil, err
			}
			files = append(files, out)
			e, err := output(filepath.Join(options.Directory, fmt.Sprintf("%s-%d.err", options.Phase, index+1)))
			if err != nil {
				return nil, err
			}
			files = append(files, e)
			cmd.Stdout = out
			cmd.Stderr = e
		}
		if options.Stdin != "" {
			input, err := os.Open(options.Stdin)
			if err != nil {
				return nil, err
			}
			files = append(files, input)
			cmd.Stdin = input
		}
		statusR, statusW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		holdR, holdW, err := os.Pipe()
		if err != nil {
			statusR.Close()
			statusW.Close()
			return nil, err
		}
		cmd.ExtraFiles = []*os.File{statusW, holdR}
		err = cmd.Start()
		statusW.Close()
		holdR.Close()
		if err != nil {
			statusR.Close()
			holdW.Close()
			return nil, err
		}
		w := &worker{command: cmd, status: statusR, hold: holdW, done: make(chan outcome, 1)}
		workers = append(workers, w)
		go func() {
			reader := bufio.NewReader(io.LimitReader(w.status, 32))
			line, err := reader.ReadString('\n')
			code := 0
			if err == nil {
				code, err = strconv.Atoi(strings.TrimSuffix(line, "\n"))
			}
			w.done <- outcome{code, err}
		}()
		if s.afterStart != nil {
			s.afterStart(cmd.Process.Pid)
		}
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
	}
	collect := func() int {
		count := 0
		for _, w := range workers {
			if w.result == nil {
				select {
				case result := <-w.done:
					w.result = &result
				default:
				}
			}
			if w.result != nil {
				count++
			}
		}
		return count
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	if options.Barrier != "" {
		for {
			if err := context.Cause(ctx); err != nil {
				return nil, fmt.Errorf("readiness deadline or cancellation: %w", err)
			}
			if collect() > 0 {
				return nil, fmt.Errorf("worker exited before readiness")
			}
			ready := true
			for i := range workers {
				info, err := os.Stat(filepath.Join(options.Barrier, fmt.Sprintf("ready-%d", i+1)))
				if err != nil || !info.Mode().IsRegular() {
					ready = false
					break
				}
			}
			if ready {
				if err := os.WriteFile(filepath.Join(options.Barrier, "go"), []byte("go\n"), 0600); err != nil {
					return nil, err
				}
				break
			}
			select {
			case <-ctx.Done():
			case <-ticker.C:
			}
		}
	}
	for collect() != len(workers) {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("execution deadline or cancellation: %w", context.Cause(ctx))
		case <-ticker.C:
		}
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	statuses := make([]int, len(workers))
	for i, w := range workers {
		if w.result.err != nil {
			return nil, w.result.err
		}
		statuses[i] = w.result.code
		if options.Directory != "" {
			f, err := output(filepath.Join(options.Directory, fmt.Sprintf("%s-%d.status", options.Phase, i+1)))
			if err != nil {
				return nil, err
			}
			_, err = fmt.Fprintf(f, "%d\n", statuses[i])
			f.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return statuses, nil
}
