//go:build linux

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWorkerHelper(t *testing.T) {
	a := os.Args
	for len(a) > 0 && a[0] != "--" {
		a = a[1:]
	}
	if len(a) > 0 {
		os.Exit(Worker(a[1:]))
	}
}
func testSupervisor(t *testing.T) supervisor {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return supervisor{prefix: []string{exe, "-test.run=^TestWorkerHelper$", "--"}}
}
func workers(t *testing.T, script string, barrier bool, duration time.Duration) ([]int, error, string) {
	t.Helper()
	dir := t.TempDir()
	barrierPath := ""
	if barrier {
		barrierPath = filepath.Join(dir, "barrier")
		if err := os.Mkdir(barrierPath, 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := []string{"/bin/sh", "-c", script}
	started := time.Now()
	statuses, err := testSupervisor(t).run(context.Background(), [][]string{command, command}, Options{Environment: []string{"PATH=/usr/bin:/bin"}, CWD: dir, Directory: dir, Phase: "create", Barrier: barrierPath, Deadline: duration})
	if time.Since(started) > duration+2*time.Second {
		t.Fatal("supervisor exceeded cleanup deadline")
	}
	return statuses, err, dir
}

func TestStatusesAndBarrier(t *testing.T) {
	statuses, err, dir := workers(t, `exit "$((GIT_DASH_WORKER - 1))"`, false, time.Second)
	if err != nil || fmt.Sprint(statuses) != "[0 1]" {
		t.Fatalf("statuses %v %v", statuses, err)
	}
	data, e := os.ReadFile(filepath.Join(dir, "create-2.status"))
	if e != nil || string(data) != "1\n" {
		t.Fatal("lost expected loser status")
	}
	statuses, err, _ = workers(t, `touch "barrier/ready-$GIT_DASH_WORKER"; while [ ! -f barrier/go ]; do sleep 0.01; done`, true, time.Second)
	if err != nil || fmt.Sprint(statuses) != "[0 0]" {
		t.Fatalf("barrier %v %v", statuses, err)
	}
	statuses, err, _ = workers(t, `kill -TERM $$`, false, time.Second)
	if err != nil || fmt.Sprint(statuses) != "[-15 -15]" {
		t.Fatalf("signal status %v %v", statuses, err)
	}
}

func TestReadinessAndCompletionFailures(t *testing.T) {
	_, err, dir := workers(t, `exit 7`, true, time.Second)
	if err == nil || !strings.Contains(err.Error(), "before readiness") {
		t.Fatalf("early exit: %v", err)
	}
	if _, e := os.Stat(filepath.Join(dir, "barrier/go")); !os.IsNotExist(e) {
		t.Fatal("released barrier after early exit")
	}
	for _, barrier := range []bool{true, false} {
		_, err, _ := workers(t, `sleep 60`, barrier, 150*time.Millisecond)
		if err == nil {
			t.Fatal("stalled workers succeeded")
		}
		want := "execution deadline"
		if barrier {
			want = "readiness deadline"
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("wrong deadline: %v", err)
		}
	}
}

func assertStopped(t *testing.T, pid int) {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	tail := string(data)[strings.LastIndex(string(data), ")")+2:]
	if !strings.HasPrefix(tail, "Z ") {
		t.Fatalf("worker %d survived: %s", pid, tail)
	}
}
func childPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid fixture PID: %v", err)
	}
	return pid
}

func TestExitedLeadersStillLoseDescendants(t *testing.T) {
	_, err, dir := workers(t, `sleep 60 & echo $! > "child-$GIT_DASH_WORKER"; exit 0`, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		assertStopped(t, childPID(t, filepath.Join(dir, fmt.Sprintf("child-%d", i))))
	}
}

func TestCancellationDuringSpawn(t *testing.T) {
	for _, termination := range []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM} {
		t.Run(termination.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			expected := fmt.Errorf("signal %d", termination)
			var owned []int
			s := testSupervisor(t)
			s.afterStart = func(pid int) { owned = append(owned, pid); cancel(expected) }
			_, err := s.run(ctx, [][]string{{"/bin/sleep", "60"}, {"/bin/sleep", "60"}}, Options{Environment: []string{"PATH=/usr/bin:/bin"}, CWD: t.TempDir(), Deadline: time.Second})
			if !errors.Is(err, expected) || len(owned) != 1 {
				t.Fatalf("cancellation registration: %v %v", owned, err)
			}
			assertStopped(t, owned[0])
		})
	}
}

func TestStdinAndMissingExecutable(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, []byte("request"), 0600); err != nil {
		t.Fatal(err)
	}
	s := testSupervisor(t)
	statuses, err := s.run(context.Background(), [][]string{{"/bin/cat"}}, Options{Environment: []string{"PATH=/usr/bin:/bin"}, CWD: dir, Directory: dir, Phase: "submit", Stdin: input, Deadline: time.Second})
	if err != nil || len(statuses) != 1 || statuses[0] != 0 {
		t.Fatalf("stdin: %v %v", statuses, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "submit-1.out"))
	if err != nil || string(data) != "request" {
		t.Fatal("stdin bytes lost")
	}
	if _, err := s.run(context.Background(), [][]string{{"/missing-fixture-executable"}}, Options{Environment: []string{"PATH=/usr/bin:/bin"}, CWD: dir, Deadline: time.Second}); err == nil {
		t.Fatal("failed spawn reported as successful race")
	}
}

func TestSupervisorDeathStopsWorkerGroup(t *testing.T) {
	self,err := os.Executable()
	if err != nil { t.Fatal(err) }
	dir := t.TempDir()
	statusR,statusW,err := os.Pipe()
	if err != nil { t.Fatal(err) }
	defer statusR.Close()
	holdR,holdW,err := os.Pipe()
	if err != nil { t.Fatal(err) }
	cmd := exec.Command(self,"-test.run=^TestWorkerHelper$","--","/bin/sh","-c",`sleep 30 & echo $! > child; wait`)
	cmd.Dir=dir
	cmd.Env=[]string{"PATH=/usr/bin:/bin"}
	cmd.SysProcAttr=&syscall.SysProcAttr{Setsid:true}
	cmd.ExtraFiles=[]*os.File{statusW,holdR}
	if err := cmd.Start(); err != nil { t.Fatal(err) }
	statusW.Close(); holdR.Close()
	done:=make(chan error,1)
	go func(){ done<-cmd.Wait() }()
	reaped:=false
	defer func() { holdW.Close(); if !reaped { _=cmd.Process.Kill(); <-done } }()
	until:=time.Now().Add(3*time.Second)
	for { if _,err:=os.Stat(filepath.Join(dir,"child")); err==nil { break }; if time.Now().After(until) { t.Fatal("worker did not start") }; time.Sleep(5*time.Millisecond) }
	// Closing the owning end models abrupt supervisor death, without a TERM pass.
	if err:=holdW.Close(); err!=nil { t.Fatal(err) }
	select { case <-done: reaped=true; case <-time.After(3*time.Second): t.Fatal("orphan anchor survived") }
	assertStopped(t,childPID(t,filepath.Join(dir,"child")))
}
