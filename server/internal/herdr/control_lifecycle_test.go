package herdr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func openTestFIFO(t *testing.T, name string) (string, *os.File) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("mkfifo", p).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo %s: %v (%s)", p, err, out)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return p, f
}

func waitFIFO(t *testing.T, f *os.File, label string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		var buf [1]byte
		_, err := f.Read(buf[:])
		done <- err
	}()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait %s: %v", label, err)
		}
	case <-timer.C:
		_ = f.Close()
		t.Fatalf("timed out waiting for %s", label)
	}
}

type errWriteCloser struct {
	writeErr error
	written  chan []byte
	closed   chan struct{}
	closeCnt atomic.Int32
}

func (w *errWriteCloser) Write(p []byte) (int, error) {
	if w.written != nil {
		cp := append([]byte(nil), p...)
		select {
		case w.written <- cp:
		default:
		}
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func (w *errWriteCloser) Close() error {
	if w.closeCnt.Add(1) == 1 && w.closed != nil {
		close(w.closed)
	}
	return nil
}

// CloseAt must still close the control session even if writeLoop already exited on an stdin write error;
// otherwise CloseAt queues the resize + nil sentinel into an unread buffered channel and leaks the child process.
func TestControlCloseAtClosesWhenWriteLoopExited(t *testing.T) {
	in := &errWriteCloser{
		writeErr: errors.New("broken pipe"),
		written:  make(chan []byte, 4),
		closed:   make(chan struct{}),
	}
	s := &Control{
		in:        in,
		writes:    make(chan []byte, 1024),
		writeDone: make(chan struct{}),
		done:      make(chan struct{}),
		cols:      80,
		rows:      24,
	}
	defer close(s.done)

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		s.writeLoop()
	}()

	if err := s.Input("hello"); err != nil {
		t.Fatalf("initial Input: %v", err)
	}
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("writeLoop did not exit after write error")
	}

	s.CloseAt(120, 40)

	select {
	case <-in.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseAt leaked session: stdin was never closed after writeLoop exited")
	}

	if err := s.Input("after-write-error"); err == nil {
		t.Fatal("Input after writeLoop exit unexpectedly succeeded")
	}
}

// StartControl must never deliver a frame to fn after returning an error due to context cancellation.
func TestStartControlCancelBeforeFrameNeverCallsFn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in herdr is a shell script")
	}
	dir := t.TempDir()
	startedPath, startedFIFO := openTestFIFO(t, "started.fifo")
	donePath, doneFIFO := openTestFIFO(t, "done.fifo")
	bin := filepath.Join(dir, "herdr")
	// Signal startup, block until stdin closes (from abortStart -> s.Close()), then emit a frame on stdout.
	script := "#!/bin/sh\n" +
		"printf x > " + startedPath + "\n" +
		"cat >/dev/null\n" +
		"printf '{\"type\":\"terminal.frame\",\"seq\":1,\"full\":true,\"width\":80,\"height\":24,\"bytes\":\"aGk=\"}\\n'\n" +
		"printf x > " + donePath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		var buf [1]byte
		if _, err := startedFIFO.Read(buf[:]); err == nil {
			cancel()
		}
	}()

	var called atomic.Int32
	c := &Client{Bin: bin}
	s, err := c.StartControl(ctx, "w1:p1", 80, 24, false, func(Frame) {
		called.Add(1)
	})
	if err == nil || s != nil {
		if s != nil {
			s.Close()
		}
		t.Fatalf("StartControl with canceled ctx = (%v, %v), want (nil, error)", s, err)
	}

	waitFIFO(t, doneFIFO, "stand-in herdr exit")
	if n := called.Load(); n != 0 {
		t.Fatalf("fn was called %d times after StartControl returned error", n)
	}
}

// When ctx cancels while fn is running the first frame, StartControl must wait for fn to finish before
// returning an error and must not invoke fn for subsequent frames.
func TestStartControlCancelDuringFirstFrameWaitsAndSuppressesLater(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in herdr is a shell script")
	}
	dir := t.TempDir()
	stdinClosedPath, stdinClosedFIFO := openTestFIFO(t, "stdin-closed.fifo")
	donePath, doneFIFO := openTestFIFO(t, "done.fifo")
	bin := filepath.Join(dir, "herdr")
	// Emit frame 1 immediately, wait for stdin EOF (from abortStart -> s.Close()), signal FIFO, emit frame 2.
	script := "#!/bin/sh\n" +
		"printf '{\"type\":\"terminal.frame\",\"seq\":1,\"full\":true,\"width\":80,\"height\":24,\"bytes\":\"aGk=\"}\\n'\n" +
		"cat >/dev/null\n" +
		"printf x > " + stdinClosedPath + "\n" +
		"printf '{\"type\":\"terminal.frame\",\"seq\":2,\"full\":false,\"width\":80,\"height\":24,\"bytes\":\"aGk=\"}\\n'\n" +
		"printf x > " + donePath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var returned atomic.Bool
	var seq1AfterReturn atomic.Bool
	var seq2Calls atomic.Int32
	var plainCount int

	c := &Client{Bin: bin}
	s, err := c.StartControl(ctx, "w1:p1", 80, 24, false, func(f Frame) {
		if f.Seq == 1 {
			cancel()
			// Wait until abortStart closes stdin (proving StartControl took the cancel path).
			waitFIFO(t, stdinClosedFIFO, "stdin close during first frame")
			if returned.Load() {
				seq1AfterReturn.Store(true)
			}
			plainCount++
			return
		}
		seq2Calls.Add(1)
		plainCount++
	})
	returned.Store(true)
	gotCount := plainCount
	if err == nil || s != nil {
		if s != nil {
			s.Close()
		}
		t.Fatalf("StartControl = (%v, %v), want (nil, canceled error)", s, err)
	}
	if seq1AfterReturn.Load() {
		t.Fatal("StartControl returned error while first-frame fn was still running")
	}
	if gotCount != 1 {
		t.Fatalf("plainCount = %d, want 1", gotCount)
	}
	waitFIFO(t, doneFIFO, "stand-in herdr frame-2 exit")
	if n := seq2Calls.Load(); n != 0 {
		t.Fatalf("subsequent frame delivered %d times after StartControl returned error", n)
	}
}

// When `herdr session list --json` returns an empty list (no default session entry yet), starting the
// default herdr server creates <configDir>/herdr.sock outside sessions/, which must invalidate listMemo.
func TestSessionListEmptyMemoInvalidatesWhenDefaultServerStarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in herdr is a shell script")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	t.Setenv("XDG_CONFIG_HOME", cfg)
	config := filepath.Join(cfg, "herdr")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	defSock := filepath.Join(config, "herdr.sock")
	execs := filepath.Join(dir, "execs")
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\n" +
		"echo x >> " + execs + "\n" +
		"if [ -e " + defSock + " ]; then\n" +
		"  printf '{\"sessions\":[{\"default\":true,\"name\":\"default\",\"running\":true,\"session_dir\":\"" + config + "\",\"socket_path\":\"" + defSock + "\"}]}\\n'\n" +
		"else\n" +
		"  printf '{\"sessions\":[]}\\n'\n" +
		"fi\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	c := &Client{Bin: bin}
	for range 3 {
		got, err := c.ListSessions(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("initial ListSessions = %+v, %v", got, err)
		}
	}
	b, _ := os.ReadFile(execs)
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Fatalf("expected 1 herdr invocation while empty, got %d", n)
	}

	// Default server starts and creates <configDir>/herdr.sock.
	if err := os.WriteFile(defSock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions after default server start: %v", err)
	}
	if len(got) != 1 || !got[0].Default || !got[0].Running {
		t.Fatalf("ListSessions returned stale memo %+v, want running default session", got)
	}
}
