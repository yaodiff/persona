package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// agentSource answers file.pack, file.chunk (pinned to revision r1) and file.pack.done in memory, as node/transfer_pack.go.
type agentSource struct {
	mu      sync.Mutex
	pack    proto.FilePack
	data    []byte
	old     bool  // predates file.pack
	packErr error // file.pack refuses with it
	done    []string
	chunks  int
	onChunk func(n int) // before the n-th data chunk is answered (outside mu)
}

func newAgentSource(kind, name string, data []byte) *agentSource {
	p := proto.FilePack{Kind: kind, Path: "/home/me/proj/" + name, Name: name, Source: "~/proj/" + name, Size: int64(len(data)),
		Revision: "r1", SHA256: hexSHA(data)}
	if kind == proto.FilePackDir {
		p.Path, p.Temp, p.Files, p.Bytes, p.Skipped, p.SkippedNames = "/tmp/persona-transfers/p-1/"+name+".zip", true, 3, 300, 1, []string{".env"}
	}
	return &agentSource{pack: p, data: data}
}

func (s *agentSource) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	s.mu.Lock()
	switch {
	case s.old && strings.HasPrefix(method, "file.pack"):
		s.mu.Unlock()
		return nil, errors.New("unknown method " + method)
	case method == proto.MethodFilePack:
		defer s.mu.Unlock()
		if s.packErr != nil {
			return nil, s.packErr
		}
		return s.pack, nil
	case method == proto.MethodFilePackDone:
		defer s.mu.Unlock()
		var p struct{ Path string }
		_ = json.Unmarshal(params, &p)
		s.done = append(s.done, p.Path)
		return map[string]any{"ok": true}, nil
	case method == "file.chunk":
		var p proto.FileChunkRequest
		_ = json.Unmarshal(params, &p)
		n := s.chunks
		s.chunks++
		hook := s.onChunk
		s.mu.Unlock()
		if hook != nil {
			hook(n)
		}
		if p.Path != s.pack.Path || p.Revision != "r1" {
			return nil, &rpc.Error{Code: proto.CodeFileChanged, Message: "changed"}
		}
		return proto.FileChunk{Path: p.Path, Size: int64(len(s.data)), Revision: "r1", Offset: p.Offset,
			Data: append([]byte(nil), s.data[p.Offset:p.Offset+int64(p.Length)]...)}, nil
	}
	s.mu.Unlock()
	return nil, errors.New("unknown method " + method)
}

func (s *agentSource) packsDone() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.done...)
}

// agentTarget answers file.upload.* as node/upload.go does for dest=path / inbox with sha256 and unpack.
type agentTarget struct {
	mu      sync.Mutex
	begins  []map[string]any
	data    bytes.Buffer
	want    string
	name    string
	unpack  bool
	ended   bool
	aborted bool
	noSHA   bool  // a node from before sha256: does not echo it
	refuse  error // begin refuses with it
}

func (f *agentTarget) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p struct {
		Name   string `json:"name"`
		Dest   string `json:"dest"`
		SHA256 string `json:"sha256"`
		Unpack bool   `json:"unpack"`
		Data   []byte `json:"data"`
	}
	_ = json.Unmarshal(params, &p)
	switch method {
	case "file.upload.begin":
		var all map[string]any
		_ = json.Unmarshal(params, &all)
		f.begins = append(f.begins, all)
		if f.refuse != nil {
			return nil, f.refuse
		}
		f.want, f.name, f.unpack, f.ended = p.SHA256, p.Name, p.Unpack, false
		f.data.Reset()
		res := map[string]any{"upload_id": "u1", "dest": p.Dest}
		if p.SHA256 != "" && !f.noSHA {
			res["sha256"] = true
		}
		if p.Unpack {
			res["unpack"] = true
		}
		return res, nil
	case "file.upload.chunk":
		f.data.Write(p.Data)
		return map[string]any{"received": f.data.Len()}, nil
	case "file.upload.end":
		sum := hexSHA(f.data.Bytes())
		if f.want != "" && sum != f.want {
			return nil, &rpc.Error{Code: proto.CodeChecksumMismatch, Message: "mismatch"}
		}
		f.ended = true
		out := proto.UploadedFile{Path: "/home/you/in/" + f.name, Name: f.name, Size: int64(f.data.Len())}
		if !f.noSHA {
			out.SHA256 = sum
		}
		if f.unpack {
			out.Files = 3
		}
		return out, nil
	case "file.upload.abort":
		f.aborted = true
		return map[string]any{"ok": true}, nil
	}
	return nil, errors.New("unknown method " + method)
}

func (f *agentTarget) state() (begins []map[string]any, data []byte, ended, aborted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.begins...), append([]byte(nil), f.data.Bytes()...), f.ended, f.aborted
}

// auditStore is memStore with the audit table.
type auditStore struct {
	*memStore
	mu   sync.Mutex
	rows []db.FileTransferAudit
}

func (s *auditStore) AddFileTransferAudit(_ context.Context, a db.FileTransferAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, a)
	return nil
}

func (s *auditStore) FileTransferAudits(_ context.Context, machine string, limit int) ([]db.FileTransferAudit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.FileTransferAudit
	for i := len(s.rows) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if r := s.rows[i]; machine == "" || r.FromMachine == machine || r.ToMachine == machine {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *auditStore) audit() []db.FileTransferAudit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.FileTransferAudit(nil), s.rows...)
}

// agentTransferDevices is a manager with computers a (src) and b (dst, of trust bTrust), and the audit it keeps.
func agentTransferDevices(t *testing.T, src *agentSource, dst *agentTarget, bTrust ...string) (*Devices, *auditStore, *Machine, *Machine, func()) {
	t.Helper()
	d := newTestDevices(t)
	st := &auditStore{memStore: d.opts.Store.(*memStore)}
	d.opts.Store = st
	a, _ := fakeMachine(t, d, "a", "Laptop", src.handle)
	cfg := MachineConfig{ID: "b", Name: "Desktop", Kind: "ssh", InboxDir: "~/Downloads/Persona"}
	if len(bTrust) > 0 {
		cfg.Trust = bTrust[0]
	}
	b, cutB := fakeMachineWith(t, d, cfg, dst.handle)
	return d, st, a, b, cutB
}

func transferCode(err error) string {
	var te *AgentTransferError
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

// A file goes from the source's node to the target's a chunk at a time, pinned to the revision file.pack gave, with
// the SHA-256 file.pack gave checked as it passes and by the target before the file gets its name. It goes to the path
// asked (made when missing), without overwrite unless asked, and leaves an audit row. The per-machine switch for a
// person's transfers does not apply.
func TestAgentTransferSendsAFile(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), proto.FileDownloadChunkBytes/16*2+100) // two chunks and a bit
	src, dst := newAgentSource(proto.FilePackFile, "report.pdf", data), &agentTarget{}
	d, st, _, _, _ := agentTransferDevices(t, src, dst)
	res, err := d.AgentTransfer(context.Background(), AgentTransfer{From: "a", To: "b", Path: "~/proj/report.pdf", Dest: "~/in/",
		CallerMachine: "a", CallerSession: "s1", Direction: "send"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultPath != "/home/you/in/report.pdf" || res.SHA256 != hexSHA(data) || res.Size != int64(len(data)) || res.Kind != proto.FilePackFile {
		t.Fatalf("result %+v", res)
	}
	begins, got, ended, aborted := dst.state()
	if !bytes.Equal(got, data) || !ended || aborted {
		t.Fatalf("target got %d bytes, ended %v, aborted %v", len(got), ended, aborted)
	}
	b := begins[0]
	if b["dest"] != proto.UploadDestPath || b["path"] != "~/in/" || b["make_dirs"] != true || b["sha256"] != hexSHA(data) ||
		b["conflict"] != nil || b["unpack"] != nil {
		t.Fatalf("begin %v", b)
	}
	rows := st.audit()
	if len(rows) != 1 {
		t.Fatalf("audit %+v", rows)
	}
	r := rows[0]
	if r.State != proto.TransferDone || r.FromMachine != "a" || r.ToMachine != "b" || r.FromPath != "~/proj/report.pdf" || r.ToPath != "~/in/" ||
		r.ResultPath != res.ResultPath || r.SHA256 != res.SHA256 || r.Bytes != int64(len(data)) || r.CallerSession != "s1" ||
		r.CallerKind != "agent" || r.Direction != "send" || r.ID != res.ID {
		t.Fatalf("audit row %+v", r)
	}

	// overwrite asks the target to replace; no dest is the receive folder, numbered and never replaced.
	dst2 := &agentTarget{}
	d2, _, _, _, _ := agentTransferDevices(t, newAgentSource(proto.FilePackFile, "a.txt", []byte("hi")), dst2)
	if _, err := d2.AgentTransfer(context.Background(), AgentTransfer{From: "a", To: "b", Path: "/x/a.txt", Dest: "/home/you/a.txt", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.AgentTransfer(context.Background(), AgentTransfer{From: "a", To: "b", Path: "/x/a.txt", Overwrite: true, Assistant: true}); err != nil {
		t.Fatal(err)
	}
	begins, _, _, _ = dst2.state()
	if begins[0]["conflict"] != proto.UploadConflictReplace {
		t.Fatalf("overwrite: %v", begins[0])
	}
	if begins[1]["dest"] != proto.UploadDestInbox || begins[1]["dir"] != "~/Downloads/Persona" || begins[1]["conflict"] != nil {
		t.Fatalf("inbox: %v", begins[1])
	}
}

// A folder goes as its zip and is unpacked there; what file.pack left out is counted, and the zip on the source is
// removed afterwards, also when the transfer fails.
func TestAgentTransferSendsAFolder(t *testing.T) {
	zip := bytes.Repeat([]byte("z"), 5000)
	src, dst := newAgentSource(proto.FilePackDir, "proj", zip), &agentTarget{}
	d, st, _, _, _ := agentTransferDevices(t, src, dst)
	res, err := d.AgentTransfer(context.Background(), AgentTransfer{From: "a", To: "b", Path: "~/proj", Direction: "fetch", Assistant: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != proto.FilePackDir || res.Files != 3 || res.Skipped != 1 || res.SkippedNames[0] != ".env" || res.ResultPath != "/home/you/in/proj" {
		t.Fatalf("result %+v", res)
	}
	begins, _, _, _ := dst.state()
	if begins[0]["unpack"] != true {
		t.Fatalf("begin %v", begins[0])
	}
	if done := src.packsDone(); len(done) != 1 || done[0] != src.pack.Path {
		t.Fatalf("zip removed: %v", done)
	}
	if r := st.audit()[0]; r.Kind != proto.FilePackDir || r.Files != 3 || r.Skipped != 1 || r.CallerKind != "assistant" || r.Direction != "fetch" {
		t.Fatalf("audit %+v", r)
	}

	dst.refuse = &rpc.Error{Code: proto.CodeExists, Message: "proj already exists"}
	_, err = d.AgentTransfer(context.Background(), AgentTransfer{From: "a", To: "b", Path: "~/proj", Dest: "~/work"})
	if transferCode(err) != proto.CodeExists || !strings.Contains(err.Error(), "Desktop") {
		t.Fatalf("taken: %v", err)
	}
	if done := src.packsDone(); len(done) != 2 {
		t.Fatalf("the zip of a failed transfer stays: %v", done)
	}
}

// What cannot be done is refused with a code and words naming the computer, nothing is left on the target, and each
// refusal of two known computers leaves an audit row.
func TestAgentTransferRefusals(t *testing.T) {
	data := []byte("data")
	cases := []struct {
		name  string
		trust string // b's
		setup func(src *agentSource, dst *agentTarget, d *Devices, a, b *Machine, cutB func())
		t     AgentTransfer
		code  string
		says  string
		audit bool
		kind  string
	}{
		{name: "unknown computer", t: AgentTransfer{From: "a", To: "nope", Path: "/x"}, code: "unknown_machine", says: "nope"},
		{name: "offline target", setup: func(_ *agentSource, _ *agentTarget, _ *Devices, _, b *Machine, cutB func()) {
			cutB()
			eventually(t, 5*time.Second, "b offline", func() bool { return !b.isOnline() })
		}, code: "offline", says: "Desktop is offline", audit: true},
		{name: "work computer", trust: "work", code: "work", says: "Desktop is a work computer", audit: true},
		{name: "old source", setup: func(src *agentSource, _ *agentTarget, _ *Devices, _, _ *Machine, _ func()) { src.old = true },
			code: proto.CodeNodeOutdated, says: "Laptop", audit: true},
		{name: "unknown pack kind", setup: func(src *agentSource, _ *agentTarget, _ *Devices, _, _ *Machine, _ func()) {
			src.pack.Kind = "unknown"
		}, code: "invalid", says: "form this manager does not know", audit: true},
		{name: "old target", setup: func(_ *agentSource, dst *agentTarget, _ *Devices, _, _ *Machine, _ func()) { dst.noSHA = true },
			code: proto.CodeNodeOutdated, says: "Desktop", audit: true, kind: proto.FilePackFile},
		{name: "credential on the source", setup: func(src *agentSource, _ *agentTarget, _ *Devices, _, _ *Machine, _ func()) {
			src.packErr = &rpc.Error{Code: proto.CodeTransferSecret, Message: "~/.env looks like a credential"}
		}, code: proto.CodeTransferSecret, says: "Laptop: ~/.env looks like a credential", audit: true},
		{name: "credential by name (an older guard)", setup: func(src *agentSource, _ *agentTarget, _ *Devices, _, _ *Machine, _ func()) {
			src.pack.Name, src.pack.Path = "hub.json", "/home/me/backup/hub.json"
		}, code: proto.CodeTransferSecret, audit: true, kind: proto.FilePackFile},
		{name: "too large", setup: func(src *agentSource, _ *agentTarget, _ *Devices, _, _ *Machine, _ func()) {
			src.pack.Size = proto.MaxAgentTransferBytes + 1
		}, code: proto.CodeTooLarge, audit: true, kind: proto.FilePackFile},
		{name: "outside the target's home", setup: func(_ *agentSource, dst *agentTarget, _ *Devices, _, _ *Machine, _ func()) {
			dst.refuse = &rpc.Error{Code: proto.CodeReadOnly, Message: "/etc is read-only from the phone (only folders in the home are writable)"}
		}, code: proto.CodeReadOnly, says: "Desktop: /etc", audit: true, kind: proto.FilePackFile},
		{name: "source bytes differ from its hash", setup: func(src *agentSource, _ *agentTarget, _ *Devices, _, _ *Machine, _ func()) {
			src.pack.SHA256 = hexSHA([]byte("something else"))
		}, code: proto.CodeChecksumMismatch, audit: true, kind: proto.FilePackFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, dst := newAgentSource(proto.FilePackFile, "a.txt", data), &agentTarget{}
			d, st, a, b, cutB := agentTransferDevices(t, src, dst, c.trust)
			if c.setup != nil {
				c.setup(src, dst, d, a, b, cutB)
			}
			tr := c.t
			if tr.From == "" {
				tr = AgentTransfer{From: "a", To: "b", Path: "~/proj/a.txt", Dest: "/etc/"}
			}
			_, err := d.AgentTransfer(context.Background(), tr)
			if err == nil || transferCode(err) != c.code || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err %v (code %q), want code %q saying %q", err, transferCode(err), c.code, c.says)
			}
			_, _, ended, _ := dst.state()
			if ended {
				t.Fatal("the target kept it")
			}
			rows := st.audit()
			if !c.audit {
				if len(rows) != 0 {
					t.Fatalf("audit %+v", rows)
				}
				return
			}
			if len(rows) != 1 || rows[0].State != proto.TransferFailed || rows[0].Code != c.code || rows[0].Error == "" {
				t.Fatalf("audit %+v", rows)
			}
			if rows[0].Kind != c.kind {
				t.Fatalf("audit kind %q, want %q", rows[0].Kind, c.kind)
			}
		})
	}
}

// A caller that goes away mid-way stops the transfer, and the target's upload is aborted (no part file stays).
func TestAgentTransferStopsWithItsCaller(t *testing.T) {
	data := bytes.Repeat([]byte("x"), proto.FileDownloadChunkBytes*3)
	src, dst := newAgentSource(proto.FilePackFile, "big.bin", data), &agentTarget{}
	d, st, _, _, _ := agentTransferDevices(t, src, dst)
	ctx, cancel := context.WithCancel(context.Background())
	src.onChunk = func(n int) {
		if n == 1 {
			cancel()
		}
	}
	_, err := d.AgentTransfer(ctx, AgentTransfer{From: "a", To: "b", Path: "~/big.bin"})
	if transferCode(err) != "cancelled" {
		t.Fatalf("err %v", err)
	}
	eventually(t, 5*time.Second, "aborted", func() bool { _, _, _, aborted := dst.state(); return aborted })
	if rows := st.audit(); len(rows) != 1 || rows[0].Code != "cancelled" {
		t.Fatalf("audit %+v", rows)
	}
	if n := d.runningTransfers(); n != 0 {
		t.Fatalf("%d running", n)
	}
	// The slot it held is free again.
	for i := 0; i < maxRunningTransfers; i++ {
		if !d.transfers.reserveSlot() {
			t.Fatalf("slot %d not free", i)
		}
	}
}

// The audit is read by a signed-in device only: newest first, one machine's when asked.
func TestAgentTransferAuditRoute(t *testing.T) {
	src, dst := newAgentSource(proto.FilePackFile, "a.txt", []byte("hi")), &agentTarget{}
	d, _, _, _, _ := agentTransferDevices(t, src, dst)
	for _, p := range []string{"~/one.txt", "~/two.txt"} {
		if _, err := d.AgentTransfer(context.Background(), AgentTransfer{From: "a", To: "b", Path: p, Direction: "send"}); err != nil {
			t.Fatal(err)
		}
	}
	mux := d.Handler(http.NotFoundHandler())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/files/transfer-audit?machine=b&limit=5", nil))
	var out struct {
		Transfers []db.FileTransferAudit `json:"transfers"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Transfers) != 2 || out.Transfers[0].FromPath != "~/two.txt" {
		t.Fatalf("GET %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest("GET", "/api/v1/files/transfer-audit", nil)
	req.Header.Set(assistantwire.RoleHeader, "answer")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an answer-only device read the audit: %d", rec.Code)
	}
}
