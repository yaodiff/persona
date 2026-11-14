package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A settled operation stays briefly so a client polling around the long POST's return can read the final state.
const fileOperationKeep = 10 * time.Minute

type fileOperationEntry struct {
	op      proto.FileOperation
	updated time.Time
}

type fileOperations struct {
	mu      sync.Mutex
	entries map[string]*fileOperationEntry
}

type fileOperationReporter struct {
	ops *fileOperations
	id  string
}

func (o *fileOperations) start(id, kind string) (*fileOperationReporter, error) {
	if len(id) == 0 || len(id) > 128 {
		return nil, fmt.Errorf("operation_id must have 1 to 128 bytes")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	for key, entry := range o.entries {
		if entry.op.State != proto.FileOperationRunning && now.Sub(entry.updated) > fileOperationKeep {
			delete(o.entries, key)
		}
	}
	if o.entries == nil {
		o.entries = map[string]*fileOperationEntry{}
	}
	if _, exists := o.entries[id]; exists {
		return nil, &rpc.Error{Code: proto.CodeExists, Message: "operation id is already in use"}
	}
	o.entries[id] = &fileOperationEntry{op: proto.FileOperation{ID: id, Kind: kind, State: proto.FileOperationRunning}, updated: now}
	return &fileOperationReporter{ops: o, id: id}, nil
}

func (o *fileOperations) get(id string) (proto.FileOperation, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[id]
	if !ok {
		return proto.FileOperation{}, false
	}
	return entry.op, true
}

func (r *fileOperationReporter) update(fn func(*proto.FileOperation)) {
	if r == nil {
		return
	}
	r.ops.mu.Lock()
	defer r.ops.mu.Unlock()
	if entry := r.ops.entries[r.id]; entry != nil {
		fn(&entry.op)
		entry.updated = time.Now()
	}
}

func (r *fileOperationReporter) totals(files int, bytes int64) {
	r.update(func(op *proto.FileOperation) { op.FilesTotal, op.BytesTotal = files, bytes })
}

func (r *fileOperationReporter) bytes(n int64) {
	r.update(func(op *proto.FileOperation) { op.BytesDone += n })
}

func (r *fileOperationReporter) fileDone() {
	r.update(func(op *proto.FileOperation) { op.FilesDone++ })
}

func (r *fileOperationReporter) finish(err error) {
	r.update(func(op *proto.FileOperation) {
		switch {
		case err == nil:
			op.State = proto.FileOperationDone
		case errors.Is(err, context.Canceled):
			op.State = proto.FileOperationCancelled
		default:
			op.State = proto.FileOperationFailed
		}
	})
}

func (n *Node) runFileOperation(id, kind string, run func(*fileOperationReporter) (any, error)) (out any, err error) {
	reporter, err := n.fileOps.start(id, kind)
	if err != nil {
		return nil, err
	}
	defer func() { reporter.finish(err) }()
	return run(reporter)
}

// fileOperationAfterWrite lets a test hold a real file write while it cancels the calling RPC.
var fileOperationAfterWrite func()

type fileProgressWriter struct {
	ctx    context.Context
	w      io.Writer
	report *fileOperationReporter
}

func (w fileProgressWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.w.Write(p)
	if n > 0 {
		w.report.bytes(int64(n))
		if fileOperationAfterWrite != nil {
			fileOperationAfterWrite()
		}
	}
	return n, err
}

func copyFileContents(ctx context.Context, dst io.Writer, src io.Reader, report *fileOperationReporter) (int64, error) {
	return io.Copy(fileProgressWriter{ctx: ctx, w: dst, report: report}, src)
}
