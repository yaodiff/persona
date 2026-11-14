package rpc

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// A call whose ctx ends while it waits for the writer (a write ahead of it stuck) says it was never written, and is
// still the ctx's error; one written and left unanswered is not "not written".
func TestCallSaysWhenItWasNeverWritten(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	p := New("h", w)
	stuck := make(chan error, 1)
	go func() { stuck <- p.Call(context.Background(), "file.upload", nil, nil) }() // nobody reads: its write blocks
	deadline := time.Now().Add(5 * time.Second)
	for len(p.wsem) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first call never took the writer")
		}
		<-time.After(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.Call(ctx, "ping", nil, nil)
	if !errors.Is(err, ErrNotWritten) || !errors.Is(err, context.DeadlineExceeded) || err.Error() != context.DeadlineExceeded.Error() {
		t.Fatalf("ping behind a stuck write: %v", err)
	}

	go func() { _, _ = io.Copy(io.Discard, r) }() // the line goes out now, and no answer comes
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	err = p.Call(ctx2, "ping", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotWritten) {
		t.Fatalf("written ping left unanswered: %v", err)
	}
	p.Close(errors.New("done"))
	<-stuck
}

// A ping that waited behind a large write but did reach the wire is write-delayed, not "not written". Its eventual
// reply timeout must retain that distinction for the connection watchdog.
func TestCallSaysWhenItWaitedForWriter(t *testing.T) {
	w := newStuckWriter()
	p := New("h", w)
	uploadDone := make(chan error, 1)
	go func() { uploadDone <- p.Call(context.Background(), "file.upload.chunk", nil, nil) }()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(w.release) }) }
	t.Cleanup(func() {
		release()
		p.Close(errors.New("done"))
		select {
		case <-uploadDone:
		case <-time.After(time.Second):
			t.Error("upload did not stop")
		}
	})
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("the upload never took the writer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	pingDone := make(chan error, 1)
	go func() { pingDone <- p.Call(ctx, "ping", nil, nil) }()
	deadline := time.Now().Add(time.Second)
	for p.writeWaiters.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("ping never queued behind the upload")
		}
		<-time.After(time.Millisecond)
	}
	release() // the upload frees the writer; the ping is written and then runs out of time for a pong
	err := <-pingDone
	if !errors.Is(err, ErrWriteDelayed) || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotWritten) {
		t.Fatalf("ping after the delayed write: %v", err)
	}
	if remaining, ok := WriteDelayRemaining(err); !ok || remaining <= 0 || remaining > 100*time.Millisecond {
		t.Fatalf("remaining response budget = %v, %v; want (0, 100ms]", remaining, ok)
	}
	if !strings.Contains(w.String(), `"method":"ping"`) {
		t.Fatalf("ping never reached the wire: %q", w.String())
	}
}
