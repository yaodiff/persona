//go:build windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// TestSupervisorHelper is not a test: the tests below run this binary as the programs the supervisor looks after.
func TestSupervisorHelper(t *testing.T) {
	switch os.Getenv("SUPERVISE_TEST_ROLE") {
	case "exit": // a program that ends at once, leaving a mark
		f, _ := os.OpenFile(os.Getenv("SUPERVISE_TEST_FILE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		os.Exit(3)
	case "restart": // a node that asks to be started again at once, as after an update
		f, _ := os.OpenFile(os.Getenv("SUPERVISE_TEST_FILE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		os.Exit(nodeproto.RestartExitCode)
	case "supervisor": // killWithMe, then a child that would run for a minute; its pid on stdout
		if err := killWithMe(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		child := exec.Command(os.Args[0], "-test.run=TestSupervisorHelper")
		child.Env = append(os.Environ(), "SUPERVISE_TEST_ROLE=sleep")
		if err := child.Start(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestKeepRunningStartsItAgain(t *testing.T) {
	marks := filepath.Join(t.TempDir(), "marks")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var logged strings.Builder
	go func() {
		defer close(done)
		keepRunning(ctx, log.New(&logged, "", 0), "helper", 50*time.Millisecond, func() *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=TestSupervisorHelper")
			cmd.Env = append(os.Environ(), "SUPERVISE_TEST_ROLE=exit", "SUPERVISE_TEST_FILE="+marks)
			return cmd
		})
	}()
	waitStarts(t, marks, 3, logged.String)
	cancel()
	<-done
	if !strings.Contains(logged.String(), "helper ended after") || !strings.Contains(logged.String(), "exit status 3") {
		t.Fatalf("log:\n%s", logged.String())
	}
}

// A node that installed an update asks with its exit code to be started again at once, not after the restart delay;
// one that keeps asking right after it starts waits the delay like any other exit.
func TestKeepRunningRestartsAtOnceWhenAsked(t *testing.T) {
	marks := filepath.Join(t.TempDir(), "marks")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var logged lockedBuilder
	go func() {
		defer close(done)
		keepRunning(ctx, log.New(&logged, "", 0), "node", time.Minute, func() *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=TestSupervisorHelper")
			cmd.Env = append(os.Environ(), "SUPERVISE_TEST_ROLE=restart", "SUPERVISE_TEST_FILE="+marks)
			return cmd
		})
	}()
	waitStarts(t, marks, 2, logged.String)
	time.Sleep(2 * time.Second) // sleep-ok: long enough for a third start, were the delay skipped again
	if n := countStarts(marks); n != 2 {
		t.Fatalf("a node asking again right after it started was restarted at once (%d starts); log:\n%s", n, logged.String())
	}
	cancel()
	<-done
	if !strings.Contains(logged.String(), "asking to start again: starting it now") || !strings.Contains(logged.String(), "starting it again in 1m0s") {
		t.Fatalf("log:\n%s", logged.String())
	}
}

// countStarts is how many times a helper program has started: each start appends its pid as a line to marks.
func countStarts(marks string) int {
	b, _ := os.ReadFile(marks)
	return strings.Count(string(b), "\n")
}

// waitStarts polls until the helper program has started at least n times, or fails the test after 20 seconds.
func waitStarts(t *testing.T, marks string, n int, logged func() string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for countStarts(marks) < n {
		if time.Now().After(deadline) {
			t.Fatalf("started %d times, want %d; log:\n%s", countStarts(marks), n, logged())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// lockedBuilder is a strings.Builder the supervisor's logger may write while the test reads it.
type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// persona-link starts once the node has made its socket anew; a leftover socket file from before does not count.
func TestWaitListening(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "node-attach.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(sock, old, old); err != nil { // left by a node that ran an hour ago
		t.Fatal(err)
	}
	began := time.Now()
	if waitListening(sock, began, 300*time.Millisecond) {
		t.Fatal("a leftover socket file counted as the new node listening")
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.Remove(sock)
		_ = os.WriteFile(sock, nil, 0o600) // the node removes it and listens anew
	}()
	start := time.Now()
	if !waitListening(sock, began, 5*time.Second) {
		t.Fatal("the new socket was not seen")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("waited %v for a socket made after 200 ms", waited)
	}
	if waitListening(filepath.Join(t.TempDir(), "none.sock"), began, 200*time.Millisecond) {
		t.Fatal("no socket at all counted")
	}
}

// What the job is for: when the supervisor ends, however it ends (the task ended, a crash), what it started ends too.
func TestWhatTheSupervisorStartsEndsWithIt(t *testing.T) {
	sup := exec.Command(os.Args[0], "-test.run=TestSupervisorHelper")
	sup.Env = append(os.Environ(), "SUPERVISE_TEST_ROLE=supervisor")
	out, err := sup.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(); err != nil {
		t.Fatal(err)
	}
	defer sup.Process.Kill()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("supervisor said %q", line)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if ev, _ := windows.WaitForSingleObject(child, 0); ev != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("the child is not running")
	}
	if err := sup.Process.Kill(); err != nil { // TerminateProcess: no chance to clean up
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, out)
	_ = sup.Wait()
	if ev, _ := windows.WaitForSingleObject(child, 10_000); ev != windows.WAIT_OBJECT_0 {
		t.Fatal("the child outlived the supervisor")
	}
}
