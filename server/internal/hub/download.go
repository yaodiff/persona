package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (h *Hub) downloadFile(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	p := proto.FileChunkRequest{Path: q.Get("path"), Cwd: q.Get("cwd")}
	if strings.TrimSpace(p.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	get := func(p proto.FileChunkRequest) (*proto.FileChunk, error) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		var out proto.FileChunk
		if err := m.call(ctx, "file.chunk", p, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	info, err := get(p)
	if err != nil {
		if h.nodeOutdated(w, r, err, "hub-download.outdated_file", nil) {
			return
		}
		callErr(w, err)
		return
	}
	if info.Size < 0 || info.Revision == "" {
		writeErr(w, http.StatusBadGateway, errors.New("invalid file metadata"))
		return
	}
	p.Path, p.Revision = info.Path, info.Revision
	contentType, mediaType := downloadMIME(info.Mime)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", fmt.Sprintf("%q", info.Revision))
	disposition := "attachment"
	if q.Get("inline") == "1" && inlineMime(mediaType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", contentDisposition(disposition, info.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The hub serves the web app on this origin: a file opened by itself (an SVG, an HTML page saved as .png) must
	// not run as the app. Chrome will not show a PDF in a sandbox, and a PDF runs no page script anyway.
	if contentType != "application/pdf" {
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	reader := &remoteFileReader{get: get, request: p, size: info.Size,
		back: func(pause time.Duration, until time.Time) bool { return awaitMachine(r.Context(), m, pause, until) }}
	http.ServeContent(w, r, info.Name, info.Modified, reader)
	if failed := reader.failed.Load(); failed != nil {
		// ServeContent stops at the read that failed and returns as if it were done, with the head (200 or 206 and its
		// Content-Length) already out. Over HTTP/2 the stream would then end cleanly, short of that length, and a
		// client that does not count the bytes takes the cut file for the whole one (#10855). Aborting resets the
		// stream (HTTP/2) or drops the connection without ending the body (HTTP/1.1): every client sees it failed.
		if r.Context().Err() == nil { // not the client's own cancel
			h.logf("[%s] download from %s aborted at byte %d of %d: %v", w.Header().Get("X-Request-Id"), m.cfg.ID,
				failed.at, info.Size, failed.err)
		}
		panic(http.ErrAbortHandler)
	}
}

// A download waits this long for the machine to come back when its node's connection drops mid-file (#10855),
// counted from the read that failed; then it is aborted. Persona Link redials a second after a drop, doubling, and a
// full reconnect is normally back in a few seconds: 30 s covers its first redials, stays well inside the two minutes
// the iOS app waits on a quiet download, and does not hold a client long for a machine that is gone. Each read still
// has its own minute. Tests shorten them.
var (
	downloadRetryWindow = 30 * time.Second
	downloadRetryPoll   = 250 * time.Millisecond // how often a waiting download looks whether the machine is back
	downloadRetryMax    = 2 * time.Second        // the longest pause between two tries of the same read
)

// awaitMachine waits pause, then until m is reachable again (looking every downloadRetryPoll): false once until has
// passed or the request ended first.
func awaitMachine(ctx context.Context, m *Machine, pause time.Duration, until time.Time) bool {
	t := time.NewTimer(pause)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		if !time.Now().Before(until) {
			return false
		}
		if m.reachable() {
			return true
		}
		t.Reset(downloadRetryPoll)
	}
}

// nodeAway: a read failed because the machine's node, or the manager that carries it, was not there: the connection
// dropped with the call out (rpc.ErrClosed, the manager's node_closed), or the call found it offline. Asked again
// once it is back, the node reads the same revision or says the file changed (file_changed), which is never retried.
func nodeAway(err error) bool {
	return errors.Is(err, rpc.ErrClosed) || errors.Is(err, errOffline) || errors.Is(err, errManagerOffline)
}

// downloadMIME parses a node-supplied MIME type into a safe Content-Type header value and its canonical lowercase
// type/subtype. Malformed, empty, or slashless values fall back to application/octet-stream (with an empty mediaType
// so the response stays an attachment and sandboxed). application/pdf is emitted without parameters because it is the
// only type served without Content-Security-Policy: sandbox.
func downloadMIME(raw string) (contentType, mediaType string) {
	mt, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return "application/octet-stream", ""
	}
	typ, sub, ok := strings.Cut(mt, "/")
	if !ok || typ == "" || sub == "" {
		return "application/octet-stream", ""
	}
	if mt == "application/pdf" {
		return "application/pdf", "application/pdf"
	}
	if formatted := mime.FormatMediaType(mt, params); formatted != "" {
		return formatted, mt
	}
	return mt, mt
}

func contentDisposition(disposition, filename string) string {
	if cd := mime.FormatMediaType(disposition, map[string]string{"filename": filename}); cd != "" {
		return cd
	}
	return disposition
}

// inlineMime: what a browser may show in place (?inline=1: <img>, <video>, <audio>, a PDF in a frame); anything
// else is always downloaded. Expects a canonical type/subtype from downloadMIME. HTML is previewed from file.read
// text in a sandboxed frame (docs/protocol/protocol.md "File preview").
func inlineMime(mt string) bool {
	return strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "audio/") ||
		mt == "application/pdf"
}

// safeThumbMime allows only raster image formats for thumbnails: nodes always rasterize thumbnails (even for SVGs),
// so rejecting image/svg+xml and malformed MIME values prevents active content on the hub origin.
func safeThumbMime(raw string) (string, bool) {
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}
	switch mt {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "image/avif":
		return mt, true
	default:
		return "", false
	}
}

// fileThumb is a file's thumbnail (node file.thumb): 304 for the ETag the client has, 404 no_thumbnail when the
// machine cannot draw it.
func (h *Hub) fileThumb(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	p := proto.FileThumbRequest{Path: q.Get("path"), Cwd: q.Get("cwd"), Size: atoi(q.Get("size")),
		ETag: strings.Trim(r.Header.Get("If-None-Match"), `"`)}
	if strings.TrimSpace(p.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out proto.FileThumb
	if err := m.call(ctx, "file.thumb", p, &out); err != nil {
		var re *rpc.Error
		switch {
		case h.nodeOutdated(w, r, err, "hub-download.outdated_thumbnail", nil):
		case errors.As(err, &re) && re.Code == proto.CodeNoThumbnail:
			writeCode(w, http.StatusNotFound, proto.CodeNoThumbnail, re.Message, nil)
		default:
			callErr(w, err)
		}
		return
	}
	if out.NotModified && p.ETag != "" {
		w.Header().Set("ETag", fmt.Sprintf("%q", out.ETag))
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	thumbMIME, ok := safeThumbMime(out.Mime)
	if !ok {
		writeErr(w, http.StatusBadGateway, errors.New("invalid thumbnail"))
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("%q", out.ETag))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Type", thumbMIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	_, _ = w.Write(out.Data)
}

type remoteFileReader struct {
	get func(proto.FileChunkRequest) (*proto.FileChunk, error)
	// back waits, at least pause, for the machine to be reachable again, until at most; nil: a failed read is final.
	back    func(pause time.Duration, until time.Time) bool
	request proto.FileChunkRequest
	size    int64
	pos     int64
	buf     []byte
	// failed is the read that failed, for downloadFile to abort the response. Atomic: for several ranges ServeContent
	// reads in a goroutine of its own, which may still run when it returns after a failed write.
	failed atomic.Pointer[readFailure]
}

// readFailure is where a download's read failed (the offset in the file) and why.
type readFailure struct {
	at  int64
	err error
}

func (f *remoteFileReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if f.pos >= f.size {
		return 0, io.EOF
	}
	if len(f.buf) == 0 {
		q := f.request
		q.Offset, q.Length = f.pos, int(min(int64(proto.FileDownloadChunkBytes), f.size-f.pos))
		chunk, err := f.chunk(q)
		if err == nil && (chunk.Offset != q.Offset || chunk.Size != f.size || chunk.Revision != q.Revision || len(chunk.Data) != q.Length) {
			err = errors.New("file chunk does not match the download")
		}
		if err != nil {
			f.failed.Store(&readFailure{at: q.Offset, err: err})
			return 0, err
		}
		f.buf = chunk.Data
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	f.pos += int64(n)
	return n, nil
}

// chunk reads q, again at the same offset while the machine's node is away (nodeAway) and comes back within
// downloadRetryWindow of the first failure, pausing longer each time up to downloadRetryMax. A reconnect does not
// change what is read: the request still names the revision the download started with.
func (f *remoteFileReader) chunk(q proto.FileChunkRequest) (*proto.FileChunk, error) {
	var until time.Time
	pause := downloadRetryPoll
	for {
		c, err := f.get(q)
		if err == nil || f.back == nil || !nodeAway(err) {
			return c, err
		}
		if until.IsZero() {
			until = time.Now().Add(downloadRetryWindow)
		}
		if !f.back(pause, until) {
			return nil, err
		}
		pause = min(2*pause, downloadRetryMax)
	}
}

func (f *remoteFileReader) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = f.pos + offset
	case io.SeekEnd:
		target = f.size + offset
	default:
		return 0, errors.New("invalid seek origin")
	}
	if target < 0 || target > f.size {
		return 0, errors.New("seek outside the file")
	}
	if target != f.pos {
		f.pos, f.buf = target, nil
	}
	return f.pos, nil
}

// fileConvert asks the machine's LibreOffice for a PDF of a document (node file.convert): 404 no_converter when it
// has none, 409 node_outdated for an older node. The answer names the PDF on the machine, read with file/download.
func (h *Hub) fileConvert(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	p := proto.FileConvertRequest{Path: q.Get("path"), Cwd: q.Get("cwd"), To: q.Get("to")}
	if strings.TrimSpace(p.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	var out proto.FileConverted
	if err := m.call(ctx, "file.convert", p, &out); err != nil {
		var re *rpc.Error
		switch {
		case h.nodeOutdated(w, r, err, "hub-download.outdated_convert", nil):
		case errors.As(err, &re) && re.Code == proto.CodeNoConverter:
			writeCode(w, http.StatusNotFound, proto.CodeNoConverter, re.Message, nil)
		default:
			callErr(w, err)
		}
		return
	}
	writeJSON(w, out)
}
