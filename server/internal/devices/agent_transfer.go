package devices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Agents' transfers (file_transfer, #14683, docs/protocol/file-transfer.md "Agents and the assistant"): an Agent or an
// assistant conversation sends a file or a folder from its computer to another of the user's, or fetches one from
// there, in one call. It rides on the computer to computer transfer's loop (streamChunks): the manager holds both
// computers' node connections, so the bytes go from one node to the other through it, a chunk at a time, never
// through the hub and never onto a disk on the way.
//
//   - The source's node prepares what is sent (file.pack): a file as it is, a folder zipped into a temporary file,
//     each with its SHA-256; credentials are refused by the read guard and by name (identity.TransferSecretName).
//   - The target's node opens the upload where it goes (dest=path, or the receive folder) with that SHA-256 and, for a
//     folder, unpack; a taken name is refused unless the call said overwrite. It checks the hash before the file gets
//     its name, and the manager checks it too as the bytes pass.
//   - Every call that names two known computers leaves an audit row (file_transfer_audit), done or failed.
//
// It is the caller's call, waited for: the tool answers when the file is there (or why not). Unlike a person's
// transfer it is not listed for the apps and not pushed as file_transfer events.

const (
	// packStep bounds file.pack: hashing a file of the largest size, or zipping a folder of it.
	packStep = 15 * time.Minute
	// endStep bounds file.upload.end: for a folder, unpacking it.
	endStep = 15 * time.Minute
)

// AgentTransfer is one transfer an Agent or an assistant conversation asked for: Path on machine From goes to
// machine To, under Dest ("" the receive folder; else "~/…" or absolute, an existing folder taking it under its own
// name), replacing what is there only with Overwrite. The rest says who asked, for the audit.
type AgentTransfer struct {
	From, To  string
	Path      string
	Dest      string
	Overwrite bool

	CallerMachine string
	CallerSession string
	Assistant     bool
	Direction     string // send or fetch, as the tool was called
}

// AgentTransferResult is what a done transfer moved: a file or a folder (Kind), its name and path on the source, where
// it is on the target, the bytes streamed (a folder's zip) and their SHA-256, for a folder its files, their bytes and
// what was left out.
type AgentTransferResult struct {
	ID           string
	Kind         string
	Name         string
	Source       string
	ResultPath   string
	Size         int64
	SHA256       string
	Files        int
	Bytes        int64
	Skipped      int
	SkippedNames []string
	Elapsed      time.Duration
}

// AgentTransferError is a transfer that did not happen, with a code (proto.Code*, or offline, unknown_machine, busy,
// work) and what to tell the caller.
type AgentTransferError struct {
	Code    string
	Message string
	err     error
}

func (e *AgentTransferError) Error() string { return e.Message }
func (e *AgentTransferError) Unwrap() error { return e.err }

func transferRefused(code, format string, args ...any) error {
	return &AgentTransferError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// fileTransferAuditStore keeps the audit (db.DB); a store without it leaves only the log line.
type fileTransferAuditStore interface {
	AddFileTransferAudit(ctx context.Context, a db.FileTransferAudit) error
}

// AgentTransfer runs t and answers it once the target has it (or why not). ctx is the caller's: when it ends, the
// transfer stops and the target's part file is removed.
func (d *Devices) AgentTransfer(ctx context.Context, t AgentTransfer) (res AgentTransferResult, err error) {
	started := time.Now()
	res.ID = newTransferID()
	from, to := d.machineByID(t.From), d.machineByID(t.To)
	switch {
	case from == nil:
		return res, transferRefused("unknown_machine", "there is no computer %q", t.From)
	case to == nil:
		return res, transferRefused("unknown_machine", "there is no computer %q", t.To)
	}
	defer func() {
		res.Elapsed = time.Since(started)
		d.auditAgentTransfer(t, from, to, res, err)
	}()
	for _, m := range []*Machine{from, to} {
		switch {
		case m.cfg.Trust == "work":
			return res, transferRefused("work", "%s is a work computer: files are not moved to or from it", m.cfg.Name)
		case !m.isOnline():
			return res, transferRefused("offline", "%s is offline (not connected to Persona now); nothing was sent", m.cfg.Name)
		}
	}
	if !d.transfers.reserveSlot() {
		return res, transferRefused("busy", "%d transfers are running already; try again in a moment", maxRunningTransfers)
	}
	defer d.transfers.releaseSlot()

	var pack proto.FilePack
	if err := callWithin(ctx, from, proto.MethodFilePack, proto.FilePackRequest{Path: t.Path}, &pack, packStep); err != nil {
		if unknownMethod(err) {
			return res, nodeOutdated(from)
		}
		return res, sideRefusal(from, err)
	}
	if pack.Temp {
		defer func() { // the folder's zip on the source, whatever happened
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := from.call(c, proto.MethodFilePackDone, map[string]any{"path": pack.Path}, nil); err != nil {
				d.logf("agent transfer %s: the zip on %s stays until it is old: %v", res.ID, from.cfg.ID, err)
			}
		}()
	}
	res.Kind, res.Name, res.Source, res.Size, res.Files, res.Bytes = pack.Kind, pack.Name, pack.Source, pack.Size, pack.Files, pack.Bytes
	res.Skipped, res.SkippedNames = pack.Skipped, pack.SkippedNames
	if sum, err := hex.DecodeString(pack.SHA256); (pack.Kind != proto.FilePackFile && pack.Kind != proto.FilePackDir) ||
		pack.Size < 0 || pack.Revision == "" || pack.Path == "" || err != nil || len(sum) != sha256.Size {
		return res, transferRefused("invalid", "%s answered what it would send in a form this manager does not know", from.cfg.Name)
	}
	if pack.Kind == proto.FilePackFile && (identity.TransferSecretName(pack.Name) || identity.TransferSecretName(path.Base(pathSlash(pack.Path)))) {
		return res, transferRefused(proto.CodeTransferSecret, "%s looks like a credential (by its name) and is not sent between computers", pack.Source)
	}
	if pack.Size > proto.MaxAgentTransferBytes {
		return res, transferRefused(proto.CodeTooLarge, "%s is %d MB, over the %d MB a transfer takes", pack.Source, pack.Size>>20, proto.MaxAgentTransferBytes>>20)
	}

	params := map[string]any{"name": pack.Name, "size": pack.Size, "sha256": pack.SHA256}
	if strings.TrimSpace(t.Dest) == "" {
		params["dest"], params["dir"] = proto.UploadDestInbox, to.cfg.InboxDir // numbered when taken, never replaced
	} else {
		params["dest"], params["path"], params["make_dirs"] = proto.UploadDestPath, strings.TrimSpace(t.Dest), true
		if t.Overwrite {
			params["conflict"] = proto.UploadConflictReplace
		}
	}
	dir := pack.Kind == proto.FilePackDir
	if dir {
		params["unpack"] = true
	}
	var begin struct {
		UploadID string `json:"upload_id"`
		Dest     string `json:"dest"`
		SHA256   bool   `json:"sha256"`
		Unpack   bool   `json:"unpack"`
	}
	if err := callStep(ctx, to, "file.upload.begin", params, &begin); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == "" && (strings.HasPrefix(re.Message, "unknown method") || strings.HasPrefix(re.Message, "unknown upload destination")) {
			return res, nodeOutdated(to)
		}
		return res, sideRefusal(to, err)
	}
	// A node from before these would put the bytes elsewhere, keep ones that do not match, or keep a folder's zip.
	if begin.Dest != params["dest"] || !begin.SHA256 || dir && !begin.Unpack {
		abortUpload(to, begin.UploadID)
		return res, nodeOutdated(to)
	}
	h := sha256.New()
	req := proto.FileChunkRequest{Path: pack.Path, Revision: pack.Revision}
	if err := streamChunks(ctx, from, to, req, begin.UploadID, pack.Size, h, nil, nil); err != nil {
		abortUpload(to, begin.UploadID)
		if ctx.Err() != nil {
			return res, &AgentTransferError{Code: "cancelled", Message: "the call ended before the transfer did; nothing was kept", err: err}
		}
		return res, streamRefusal(err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != pack.SHA256 {
		abortUpload(to, begin.UploadID)
		return res, transferRefused(proto.CodeChecksumMismatch, "the bytes read from %s hash to %s, not to the %s it announced; nothing was kept", from.cfg.Name, got, pack.SHA256)
	}
	var out proto.UploadedFile
	if err := callWithin(ctx, to, "file.upload.end", map[string]any{"upload_id": begin.UploadID}, &out, endStep); err != nil {
		abortUpload(to, begin.UploadID)
		return res, sideRefusal(to, err)
	}
	if out.SHA256 != pack.SHA256 { // it said it would check, and then did not say what it wrote
		return res, transferRefused(proto.CodeChecksumMismatch, "%s did not confirm the SHA-256 of what it wrote (%q); check %s", to.cfg.Name, out.SHA256, out.Path)
	}
	res.ResultPath, res.SHA256 = out.Path, out.SHA256
	if dir {
		res.Skipped += out.Skipped
	}
	return res, nil
}

// pathSlash is p with a Windows source's separators as slashes, for its base name.
func pathSlash(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// callWithin is callStep with its own bound.
func callWithin(ctx context.Context, m *Machine, method string, params, out any, within time.Duration) error {
	c, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	return m.call(c, method, params, out)
}

func nodeOutdated(m *Machine) error {
	return transferRefused(proto.CodeNodeOutdated, "Persona on %s is too old for file transfers between computers: update it there", m.cfg.Name)
}

// sideRefusal is a machine's refusal or failure with its code, the message naming the machine.
func sideRefusal(m *Machine, err error) error {
	code := ""
	var re *rpc.Error
	if errors.As(err, &re) {
		code = re.Code
	}
	if errors.Is(err, errOffline) || errors.Is(err, rpc.ErrClosed) {
		code = "offline"
	}
	return &AgentTransferError{Code: code, Message: m.cfg.Name + ": " + err.Error(), err: err}
}

// streamRefusal is streamChunks' error (already naming its machine) with its code.
func streamRefusal(err error) error {
	code := ""
	var re *rpc.Error
	if errors.As(err, &re) {
		code = re.Code
	}
	if errors.Is(err, errOffline) || errors.Is(err, rpc.ErrClosed) {
		code = "offline"
	}
	return &AgentTransferError{Code: code, Message: err.Error(), err: err}
}

// auditAgentTransfer records how t went (file_transfer_audit), and logs it.
func (d *Devices) auditAgentTransfer(t AgentTransfer, from, to *Machine, res AgentTransferResult, err error) {
	row := db.FileTransferAudit{ID: res.ID, At: time.Now(), CallerMachine: t.CallerMachine, CallerSession: t.CallerSession,
		CallerKind: "agent", Direction: t.Direction, FromMachine: from.cfg.ID, FromPath: t.Path, ToMachine: to.cfg.ID,
		ToPath: strings.TrimSpace(t.Dest), ResultPath: res.ResultPath, Bytes: res.Size, Files: res.Files,
		Skipped: res.Skipped, SHA256: res.SHA256, State: proto.TransferDone, ElapsedMS: res.Elapsed.Milliseconds()}
	if t.Assistant {
		row.CallerKind = "assistant"
	}
	// A refused pack can report an unrecognized kind. Keep its audit with an empty kind.
	if res.Kind == proto.FilePackFile || res.Kind == proto.FilePackDir {
		row.Kind = res.Kind
	}
	if err != nil {
		row.State, row.Error = proto.TransferFailed, err.Error()
		var te *AgentTransferError
		if errors.As(err, &te) {
			row.Code = te.Code
		}
		d.logf("agent transfer %s: %s:%s → %s (%s, by %s/%s): %s", row.ID, row.FromMachine, row.FromPath, row.ToMachine,
			row.Direction, row.CallerMachine, row.CallerSession, row.Error)
	} else {
		d.logf("agent transfer %s: %s:%s → %s:%s, %d bytes, sha256 %s (%s, by %s/%s)", row.ID, row.FromMachine, row.FromPath,
			row.ToMachine, row.ResultPath, row.Bytes, row.SHA256, row.Direction, row.CallerMachine, row.CallerSession)
	}
	st, ok := d.opts.Store.(fileTransferAuditStore)
	if !ok {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.AddFileTransferAudit(c, row); err != nil {
		d.logf("agent transfer %s: the audit row was not written: %v", row.ID, err)
	}
}
