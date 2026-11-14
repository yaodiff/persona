package node

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestFileCopyOperationReportsCompletion(t *testing.T) {
	n, home := testNode(t)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "abcd")
	if err := os.Mkdir(filepath.Join(home, "dest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := makeCall(t, n, "file.copy", map[string]any{"operation_id": "op-copy-1", "paths": []string{"~/proj/a.txt"}, "dir": "~/dest"}); err != nil {
		t.Fatal(err)
	}
	raw, err := n.handle(context.Background(), "file.operation", json.RawMessage(`{"operation_id":"op-copy-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(raw)
	var op proto.FileOperation
	if err := json.Unmarshal(b, &op); err != nil {
		t.Fatal(err)
	}
	if op.ID != "op-copy-1" || op.Kind != "copy" || op.State != "done" || op.FilesDone != 1 || op.FilesTotal != 1 || op.BytesDone != 4 || op.BytesTotal != 4 {
		t.Fatalf("operation = %+v", op)
	}
}

func TestFileCopyOperationCancelRemovesPartialFile(t *testing.T) {
	f := newUploadRPCFixture(t)
	f.peer.Cancels = map[string]bool{"file.copy": true}
	home := f.n.home
	data := bytes.Repeat([]byte("x"), 4<<20)
	if err := os.WriteFile(filepath.Join(home, "a.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "dest"), 0o755); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	fileOperationAfterWrite = func() {
		startedOnce.Do(func() { close(started) })
		<-release
	}
	t.Cleanup(func() { fileOperationAfterWrite = nil; unblock() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.peer.Call(ctx, "file.copy", map[string]any{"operation_id": "op-copy-cancel", "paths": []string{"~/a.txt"}, "dir": "~/dest"}, nil)
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("copy ended before its first write: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not reach its first write")
	}
	var op proto.FileOperation
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer statusCancel()
	if err := f.peer.Call(statusCtx, "file.operation", map[string]any{"operation_id": "op-copy-cancel"}, &op); err != nil {
		t.Fatal(err)
	}
	if op.State != proto.FileOperationRunning || op.BytesDone == 0 || op.BytesTotal != int64(len(data)) || op.FilesTotal != 1 {
		t.Fatalf("running operation = %+v", op)
	}
	cancel()
	unblock()
	if err := awaitUpload(t, done); err == nil {
		t.Fatal("cancelled copy succeeded")
	}
	waitFor(t, "cancelled file operation", func() bool {
		return f.peer.Call(statusCtx, "file.operation", map[string]any{"operation_id": "op-copy-cancel"}, &op) == nil && op.State == proto.FileOperationCancelled
	})
	if op.State != proto.FileOperationCancelled {
		t.Fatalf("settled operation = %+v", op)
	}
	if _, err := os.Stat(filepath.Join(home, "dest", "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial output remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "a.txt")); err != nil {
		t.Fatalf("source was changed: %v", err)
	}
}

func TestFileCopyOperationCancelRestoresReplacedFile(t *testing.T) {
	f := newUploadRPCFixture(t)
	f.peer.Cancels = map[string]bool{"file.copy": true}
	home := f.n.home
	data := bytes.Repeat([]byte("x"), 4<<20)
	if err := os.WriteFile(filepath.Join(home, "a.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "dest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "dest", "a.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	fileOperationAfterWrite = func() {
		startedOnce.Do(func() { close(started) })
		<-release
	}
	t.Cleanup(func() { fileOperationAfterWrite = nil; unblock() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.peer.Call(ctx, "file.copy", map[string]any{"operation_id": "op-copy-replace-cancel", "paths": []string{"~/a.txt"}, "dir": "~/dest", "conflict": "replace"}, nil)
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("copy ended before its first write: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not reach its first write")
	}
	cancel()
	unblock()
	if err := awaitUpload(t, done); err == nil {
		t.Fatal("cancelled replacement succeeded")
	}
	var op proto.FileOperation
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer statusCancel()
	waitFor(t, "cancelled replacement operation", func() bool {
		return f.peer.Call(statusCtx, "file.operation", map[string]any{"operation_id": "op-copy-replace-cancel"}, &op) == nil && op.State == proto.FileOperationCancelled
	})
	got, err := os.ReadFile(filepath.Join(home, "dest", "a.txt"))
	if err != nil {
		t.Fatalf("replaced destination was not restored: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("replaced destination was not restored: got %d bytes, want the 8 original bytes", len(got))
	}
}

func TestFileZipOperationReportsCompletion(t *testing.T) {
	n, home := testNode(t)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "abcd")
	if _, err := makeCall(t, n, "file.zip", map[string]any{"operation_id": "op-zip-1", "paths": []string{"~/proj/a.txt"}, "dir": "~/proj", "name": "out"}); err != nil {
		t.Fatal(err)
	}
	raw, err := n.handle(context.Background(), "file.operation", json.RawMessage(`{"operation_id":"op-zip-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(raw)
	var op proto.FileOperation
	if err := json.Unmarshal(b, &op); err != nil {
		t.Fatal(err)
	}
	if op.ID != "op-zip-1" || op.Kind != "zip" || op.State != "done" || op.FilesDone != 1 || op.FilesTotal != 1 || op.BytesDone != 4 || op.BytesTotal != 4 {
		t.Fatalf("operation = %+v", op)
	}
}

func TestFileZipOperationCancelRemovesPartialFile(t *testing.T) {
	f := newUploadRPCFixture(t)
	f.peer.Cancels = map[string]bool{"file.zip": true}
	home := f.n.home
	data := bytes.Repeat([]byte("z"), 4<<20)
	if err := os.WriteFile(filepath.Join(home, "a.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	fileOperationAfterWrite = func() {
		startedOnce.Do(func() { close(started) })
		<-release
	}
	t.Cleanup(func() { fileOperationAfterWrite = nil; unblock() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.peer.Call(ctx, "file.zip", map[string]any{"operation_id": "op-zip-cancel", "paths": []string{"~/a.txt"}, "dir": "~", "name": "out.zip"}, nil)
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("zip ended before its first write: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("zip did not reach its first write")
	}
	var op proto.FileOperation
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer statusCancel()
	if err := f.peer.Call(statusCtx, "file.operation", map[string]any{"operation_id": "op-zip-cancel"}, &op); err != nil {
		t.Fatal(err)
	}
	if op.State != proto.FileOperationRunning || op.BytesDone == 0 || op.BytesTotal != int64(len(data)) || op.FilesTotal != 1 {
		t.Fatalf("running operation = %+v", op)
	}
	cancel()
	unblock()
	if err := awaitUpload(t, done); err == nil {
		t.Fatal("cancelled zip succeeded")
	}
	waitFor(t, "cancelled zip operation", func() bool {
		return f.peer.Call(statusCtx, "file.operation", map[string]any{"operation_id": "op-zip-cancel"}, &op) == nil && op.State == proto.FileOperationCancelled
	})
	if _, err := os.Stat(filepath.Join(home, "out.zip")); !os.IsNotExist(err) {
		t.Fatalf("partial zip remains: %v", err)
	}
	parts, err := filepath.Glob(filepath.Join(home, ".persona-zip-*"))
	if err != nil || len(parts) != 0 {
		t.Fatalf("temporary zip files remain: %v, %v", parts, err)
	}
}
