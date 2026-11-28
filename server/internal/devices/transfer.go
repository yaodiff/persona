package devices

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Computer to computer transfers (docs/protocol/file-transfer.md): the manager reads a file from one machine with
// file.chunk, pinned to the revision it saw first, and writes it into another machine's receive folder with
// file.upload.* (dest=inbox), a chunk at a time. Nothing touches a disk on the way, and at most transferWindow chunks
// are in memory per transfer: the next one is read while the last one is written.
//
// The manager that carries both computers runs it (it holds their connections; the hub only relays the apps' requests,
// devicewire.TransferRoutes, and the events). The transfers live in its memory only: a restart or redeploy forgets
// them, the target's node removes the part file once no chunk comes for a while (node/upload.go uploadIdle), GET
// answers 404, and the client sends it again.

const (
	// transferWindow is how many chunks read from the source may wait for the target: one, so a transfer holds at
	// most three (one being read, one waiting, one being written).
	transferWindow = 1
	// transferStep bounds each call to a machine; a node that does not answer in time fails the transfer.
	transferStep = time.Minute
	// transferProgressEvery is how often a running transfer is pushed to the apps; its state changes always are.
	transferProgressEvery = time.Second
	// transferKeep is how long a finished transfer can still be read with GET.
	transferKeep = time.Hour
	// maxRunningTransfers bounds the transfers running at once in the manager (429 beyond).
	maxRunningTransfers = 8
)

type transfers struct {
	mu          sync.Mutex
	byID        map[string]*transfer
	slots       chan struct{} // capacity reserved before the machines' RPCs, not only once the transfer is listed (#3599)
	afterLaunch func(*transfer)
}

type transfer struct {
	mu        sync.Mutex
	st        proto.FileTransfer
	cancel    context.CancelFunc
	cancelled bool // DELETE asked for it (a failure that follows is reported as cancelled)
	pushedAt  time.Time
	done      chan struct{} // closed when it is over (tests)
}

// transferError is a refusal before a transfer starts, with the HTTP status and code it answers.
type transferError struct {
	status int
	code   string
	key    string // the message, translated for the client (i18n hub-upload / hub-download)
	args   i18n.Args
	extra  map[string]any
}

func (e *transferError) Error() string { return i18n.T(i18n.En, e.key, e.args) }

func newTransferID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (t *transfer) snapshot() proto.FileTransfer {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

// reserveSlot takes one of the running-transfer slots without waiting. Reserving before the first machine RPC is
// what keeps a burst of starts from all passing runningTransfers before any of them is listed.
func (t *transfers) reserveSlot() bool {
	t.mu.Lock()
	if t.slots == nil {
		t.slots = make(chan struct{}, maxRunningTransfers)
	}
	slots := t.slots
	t.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (t *transfers) releaseSlot() { <-t.slots }

// startTransfer checks both ends and opens the upload on the target, then sends the bytes in the background. It
// answers the transfer as it starts; refusals before that are *transferError or the machines' rpc errors.
func (d *Devices) startTransfer(ctx context.Context, from, to *Machine, src proto.FileTransferSource) (proto.FileTransfer, error) {
	if !d.transfers.reserveSlot() {
		return proto.FileTransfer{}, &transferError{status: http.StatusTooManyRequests, code: "busy",
			key: "hub-upload.transfer_busy", args: i18n.Args{"n": maxRunningTransfers}}
	}
	reserved := true
	defer func() {
		if reserved {
			d.transfers.releaseSlot()
		}
	}()
	step := func(m *Machine, method string, params, out any) error {
		c, cancel := context.WithTimeout(ctx, transferStep)
		defer cancel()
		return m.call(c, method, params, out)
	}
	// The source: what file it is now, and its revision, which every chunk is then asked at.
	var info proto.FileChunk
	if err := step(from, "file.chunk", proto.FileChunkRequest{Path: src.Path, Cwd: src.Cwd}, &info); err != nil {
		if unknownMethod(err) {
			return proto.FileTransfer{}, &transferError{status: http.StatusConflict, code: proto.CodeNodeOutdated,
				key: "hub-download.outdated_file", extra: map[string]any{"machine": from.cfg.ID}}
		}
		return proto.FileTransfer{}, err
	}
	if info.Size < 0 || info.Revision == "" {
		return proto.FileTransfer{}, &transferError{status: http.StatusBadGateway, key: "hub-upload.transfer_invalid"}
	}
	if info.Size > proto.MaxInboxUploadBytes {
		return proto.FileTransfer{}, &transferError{status: http.StatusRequestEntityTooLarge, code: proto.CodeTooLarge,
			key: "hub-upload.too_large", args: i18n.Args{"mb": proto.MaxInboxUploadBytes >> 20},
			extra: map[string]any{"limit": proto.MaxInboxUploadBytes}}
	}
	// The target: its receive folder, as hub.json names it.
	var begin struct {
		UploadID string `json:"upload_id"`
		Dest     string `json:"dest"`
	}
	params := map[string]any{"name": info.Name, "size": info.Size, "dest": proto.UploadDestInbox, "dir": to.cfg.InboxDir}
	if err := step(to, "file.upload.begin", params, &begin); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == "" && (strings.HasPrefix(re.Message, "unknown method") || strings.HasPrefix(re.Message, "unknown upload destination")) {
			return proto.FileTransfer{}, outdated(to)
		}
		return proto.FileTransfer{}, err
	}
	if begin.Dest != proto.UploadDestInbox { // a node from before dest=inbox opened it in its temp directory
		abortUpload(to, begin.UploadID)
		return proto.FileTransfer{}, outdated(to)
	}

	now := time.Now()
	run, cancel := context.WithCancel(d.transferParent())
	t := &transfer{cancel: cancel, done: make(chan struct{}), st: proto.FileTransfer{
		ID: newTransferID(), State: proto.TransferRunning, From: from.cfg.ID, To: to.cfg.ID, Name: info.Name,
		Total: info.Size, StartedAt: now, UpdatedAt: now,
	}}
	d.transfers.mu.Lock()
	d.pruneTransfersLocked(now)
	if d.transfers.byID == nil {
		d.transfers.byID = map[string]*transfer{}
	}
	d.transfers.byID[t.st.ID] = t
	d.transfers.mu.Unlock()
	d.logf("transfer %s: %s → %s, %d bytes", t.st.ID, from.cfg.ID, to.cfg.ID, info.Size)
	d.pushTransfer(t, true)
	initial := t.snapshot()
	reserved = false // runTransfer releases it when the transfer ends
	go d.runTransfer(run, t, from, to, proto.FileChunkRequest{Path: info.Path, Cwd: src.Cwd, Revision: info.Revision}, begin.UploadID)
	if d.transfers.afterLaunch != nil {
		d.transfers.afterLaunch(t)
	}
	return initial, nil
}

// transferParent is what every transfer is a child of: the manager's own context, cancelled when it stops.
func (d *Devices) transferParent() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx != nil {
		return d.ctx
	}
	return context.Background()
}

// outdated: the target's node does not know dest=inbox.
func outdated(m *Machine) error {
	return &transferError{status: http.StatusConflict, code: proto.CodeNodeOutdated,
		key: "hub-upload.outdated", args: i18n.Args{"name": m.cfg.Name}, extra: map[string]any{"machine": m.cfg.ID}}
}

func unknownMethod(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == "" && strings.HasPrefix(re.Message, "unknown method")
}

// abortUpload drops an upload on the target; not the transfer's context, which is what is cancelled when it matters.
func abortUpload(m *Machine, id string) {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.call(c, "file.upload.abort", map[string]any{"upload_id": id}, nil)
}

type transferChunk struct {
	data []byte
	err  error
}

// runTransfer sends the bytes (streamChunks), then names the file on the target. Any failure, on either side, or a
// cancel, aborts the upload on the target so no part file stays.
func (d *Devices) runTransfer(ctx context.Context, t *transfer, from, to *Machine, req proto.FileChunkRequest, uploadID string) {
	defer d.transfers.releaseSlot()
	defer close(t.done)
	defer t.cancel()
	// The state is told first (a target that went away may hold the abort for its whole timeout); the transfer is over
	// (done, what DELETE waits for) once the abort has been tried.
	fail := func(err error) {
		t.cancel()
		d.finishTransfer(t, err, "")
		abortUpload(to, uploadID)
	}
	// A switch turned off mid-way stops the transfer at the next chunk (the switches are re-read every 10 s).
	switches := func() error {
		for _, m := range []*Machine{from, to} {
			if !d.fileTransferOn(m.cfg.ID) {
				return onMachine(m, &rpc.Error{Code: proto.CodeFileTransferDisabled, Message: "file transfer was turned off"})
			}
		}
		return nil
	}
	progress := func(off int64) {
		t.mu.Lock()
		t.st.Bytes, t.st.UpdatedAt = off, time.Now()
		t.mu.Unlock()
		d.pushTransfer(t, false)
	}
	if err := streamChunks(ctx, from, to, req, uploadID, t.st.Total, nil, switches, progress); err != nil {
		fail(err)
		return
	}
	var out proto.UploadedFile
	if err := callStep(ctx, to, "file.upload.end", map[string]any{"upload_id": uploadID}, &out); err != nil {
		fail(onMachine(to, err))
		return
	}
	d.finishTransfer(t, nil, out.Path)
}

// streamChunks copies total bytes of the file req names on from (file.chunk, every chunk at req's revision, checked
// against it) into upload uploadID on to (file.upload.chunk): a reader asks the source for the next chunk while the
// last one is written, one chunk waiting between them (transferWindow). h, when set, hashes the bytes in order; check,
// when set, may stop it before each chunk is written; progress hears the bytes written so far. The error names the
// machine it came from (onMachine), or is ctx's. The upload is left as it is: the caller ends or aborts it.
func streamChunks(ctx context.Context, from, to *Machine, req proto.FileChunkRequest, uploadID string, total int64,
	h hash.Hash, check func() error, progress func(off int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	chunks := make(chan transferChunk, transferWindow)
	readerDone := make(chan struct{})
	defer func() {
		cancel()
		<-readerDone
	}()
	go func() {
		defer close(readerDone)
		defer close(chunks)
		for off := int64(0); off < total; {
			q := req
			q.Offset, q.Length = off, int(min(int64(proto.FileDownloadChunkBytes), total-off))
			var c proto.FileChunk
			err := callStep(ctx, from, "file.chunk", q, &c)
			if err == nil && (c.Offset != q.Offset || c.Size != total || c.Revision != q.Revision || len(c.Data) != q.Length) {
				err = &rpc.Error{Code: proto.CodeFileChanged, Message: "the file changed while it was being sent"}
			}
			select {
			case chunks <- transferChunk{data: c.Data, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
			off += int64(len(c.Data))
		}
	}()
	var off int64
	for off < total {
		var c transferChunk
		var ok bool
		select {
		case c, ok = <-chunks:
		case <-ctx.Done():
			return ctx.Err()
		}
		if !ok { // the reader stopped for the context
			return ctx.Err()
		}
		if c.err != nil {
			return onMachine(from, c.err)
		}
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if err := callStep(ctx, to, "file.upload.chunk", map[string]any{"upload_id": uploadID, "offset": off, "data": c.data}, nil); err != nil {
			return onMachine(to, err)
		}
		if h != nil {
			h.Write(c.data)
		}
		off += int64(len(c.data))
		if progress != nil {
			progress(off)
		}
	}
	return nil
}

func callStep(ctx context.Context, m *Machine, method string, params, out any) error {
	c, cancel := context.WithTimeout(ctx, transferStep)
	defer cancel()
	return m.call(c, method, params, out)
}

// sideErr says which machine an error came from, keeping its code.
type sideErr struct {
	machine string
	err     error
}

func (e *sideErr) Error() string { return e.machine + ": " + e.err.Error() }
func (e *sideErr) Unwrap() error { return e.err }

func onMachine(m *Machine, err error) error { return &sideErr{machine: m.cfg.Name, err: err} }

// finishTransfer records how a transfer ended and tells the apps.
func (d *Devices) finishTransfer(t *transfer, err error, path string) {
	t.mu.Lock()
	t.st.UpdatedAt = time.Now()
	switch {
	case err == nil:
		t.st.State, t.st.ResultPath = proto.TransferDone, path
	case t.cancelled:
		t.st.State = proto.TransferCancelled
	default:
		t.st.State, t.st.Error = proto.TransferFailed, err.Error()
		var re *rpc.Error
		if errors.As(err, &re) {
			t.st.Code = re.Code
		}
	}
	st := t.st
	t.mu.Unlock()
	if st.Error != "" {
		d.logf("transfer %s: %s (%d of %d bytes): %s", st.ID, st.State, st.Bytes, st.Total, st.Error)
	} else {
		d.logf("transfer %s: %s (%d of %d bytes)", st.ID, st.State, st.Bytes, st.Total)
	}
	d.pushTransfer(t, true)
}

// getTransfer is a transfer as it is now; false when this manager does not know it (never, forgotten, or restarted).
func (d *Devices) getTransfer(id string) (proto.FileTransfer, bool) {
	d.pruneTransfers(time.Now())
	d.transfers.mu.Lock()
	t := d.transfers.byID[id]
	d.transfers.mu.Unlock()
	if t == nil {
		return proto.FileTransfer{}, false
	}
	return t.snapshot(), true
}

// cancelTransfer stops a running transfer; one that is over stays as it ended. It answers the transfer once it has
// stopped (the target's part file is gone by then), or as it is when that takes longer than wait.
func (d *Devices) cancelTransfer(id string, wait time.Duration) (proto.FileTransfer, bool) {
	d.transfers.mu.Lock()
	t := d.transfers.byID[id]
	d.transfers.mu.Unlock()
	if t == nil {
		return proto.FileTransfer{}, false
	}
	t.mu.Lock()
	running := t.st.State == proto.TransferRunning
	if running {
		t.cancelled = true
	}
	t.mu.Unlock()
	if running {
		t.cancel()
		select {
		case <-t.done:
		case <-time.After(wait):
		}
	}
	return t.snapshot(), true
}

func (d *Devices) runningTransfers() int {
	d.pruneTransfers(time.Now())
	d.transfers.mu.Lock()
	defer d.transfers.mu.Unlock()
	n := 0
	for _, t := range d.transfers.byID {
		t.mu.Lock()
		if t.st.State == proto.TransferRunning {
			n++
		}
		t.mu.Unlock()
	}
	return n
}

// pruneTransfers forgets the transfers that ended more than transferKeep ago.
func (d *Devices) pruneTransfers(now time.Time) {
	d.transfers.mu.Lock()
	defer d.transfers.mu.Unlock()
	d.pruneTransfersLocked(now)
}

func (d *Devices) pruneTransfersLocked(now time.Time) {
	for id, t := range d.transfers.byID {
		t.mu.Lock()
		old := t.st.State != proto.TransferRunning && now.Sub(t.st.UpdatedAt) > transferKeep
		t.mu.Unlock()
		if old {
			delete(d.transfers.byID, id)
		}
	}
}

// pushTransfer tells the apps how a transfer stands: always when its state changes (changed), otherwise at most once
// a second. The hub passes it on only to the full devices that declared file_transfer, outside the event log (as
// extension_operation): an app that comes back asks GET.
func (d *Devices) pushTransfer(t *transfer, changed bool) {
	now := time.Now()
	t.mu.Lock()
	if !changed && now.Sub(t.pushedAt) < transferProgressEvery {
		t.mu.Unlock()
		return
	}
	t.pushedAt = now
	st := t.st
	t.mu.Unlock()
	d.broadcast(map[string]any{"type": "file_transfer", "transfer": st}) // proto.FileTransferEvent
}

// broadcast sends the apps a message through the hub (ws.broadcast); the hub decides who gets it by its type
// (hub/bus_broadcast.go managerMessageTypes).
func (d *Devices) broadcast(msg map[string]any) {
	raw, err := json.Marshal(msg)
	if err != nil || d.opts.Hub == nil {
		return
	}
	if err := d.opts.Hub.Notify(assistantwire.EventBroadcast, assistantwire.Broadcast{Message: raw}); err != nil {
		d.logf("broadcast %v: %v", msg["type"], err)
	}
}
