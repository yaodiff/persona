package rpc

import (
	"context"
	"errors"
	"io"
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
