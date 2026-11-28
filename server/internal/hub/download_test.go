package hub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type downloadNode struct {
	mu        sync.Mutex
	data      []byte
	requests  []proto.FileChunkRequest
	old       bool
	bad       bool
	mime      string
	rawMime   bool
	name      string
	thumbMime string
	thumbs    []proto.FileThumbRequest
	// revision is the file's (r1 when empty): a chunk asked at another one is refused, as the node does.
	revision string
	// fail, when set, may fail a chunk read before it is answered (f.mu is held).
	fail func(p proto.FileChunkRequest) error

	m       *Machine // the hub's machine for this node
	connMu  sync.Mutex
	cutConn func()
}

// connect gives the hub's machine a new connection to this node (a full reconnect); drop cuts the one it has, as a
// lost relay would: a call out on it fails with rpc.ErrDropped, and the machine is offline until the next connect.
func (f *downloadNode) connect() {
	toNode, nodeIn := io.Pipe()
	toHub, hubIn := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", nodeIn), rpc.New("n", hubIn)
	nodePeer.OnRequest = f.handle
	ctx, cancel := context.WithCancel(context.Background())
	go hubPeer.Serve(ctx, toHub)
	go nodePeer.Serve(ctx, toNode)
	f.connMu.Lock()
	f.cutConn = func() { cancel(); toNode.Close(); toHub.Close(); hubPeer.Close(rpc.ErrClosed) }
	f.connMu.Unlock()
	f.m.mu.Lock()
	f.m.peer = hubPeer
	f.m.mu.Unlock()
}

func (f *downloadNode) drop() {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	if f.cutConn != nil {
		f.cutConn()
	}
}

func (f *downloadNode) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.old {
		return nil, errors.New("unknown method " + method)
	}
	if method == "file.convert" {
		var p proto.FileConvertRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if strings.HasSuffix(p.Path, ".key") {
			return nil, &rpc.Error{Code: proto.CodeNoConverter, Message: "LibreOffice is not installed on this machine"}
		}
		return proto.FileConverted{Path: "/cache/convert/ab.pdf", Size: 9, Mime: "application/pdf"}, nil
	}
	if method == "file.thumb" {
		var p proto.FileThumbRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		f.thumbs = append(f.thumbs, p)
		switch {
		case strings.HasSuffix(p.Path, ".bin"):
			return nil, &rpc.Error{Code: proto.CodeNoThumbnail, Message: "no thumbnail for x.bin"}
		case p.ETag == "r1-256":
			return proto.FileThumb{ETag: "r1-256", NotModified: true}, nil
		}
		thumbMime := f.thumbMime
		if thumbMime == "" {
			thumbMime = "image/png"
		}
		return proto.FileThumb{Mime: thumbMime, ETag: "r1-256", Data: []byte("png")}, nil
	}
	if method != "file.chunk" {
		return nil, errors.New("unexpected method")
	}
	var p proto.FileChunkRequest
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	f.requests = append(f.requests, p)
	if f.fail != nil {
		if err := f.fail(p); err != nil {
			return nil, err
		}
	}
	revision := cmp.Or(f.revision, "r1")
	if p.Revision != "" && p.Revision != revision {
		return nil, &rpc.Error{Code: proto.CodeFileChanged, Message: "file changed during download; reopen it to try again"}
	}
	mime := f.mime
	if !f.rawMime && mime == "" {
		mime = "video/mp4"
	}
	name := cmp.Or(f.name, "preview.mp4")
	out := proto.FileChunk{Path: "/work/preview.mp4", Name: name, Size: int64(len(f.data)), Mime: mime, Revision: revision, Modified: time.Unix(1700000000, 0), Offset: p.Offset}
	if p.Length > 0 {
		out.Data = f.data[p.Offset:min(int64(len(f.data)), p.Offset+int64(p.Length))]
		if f.bad {
			out.Revision = "r2"
		}
	}
	return out, nil
}

func downloadHub(t *testing.T, f *downloadNode) http.Handler {
	t.Helper()
	_, mux := downloadFixture(t, f)
	return mux
}

// downloadFixture is a hub with machine m1, connected to f, and the download routes alone.
func downloadFixture(t *testing.T, f *downloadNode) (*Hub, *http.ServeMux) {
	t.Helper()
	h := newTestHub(t)
	f.m = newMachine(h, MachineConfig{ID: "m1", Name: "box"})
	h.byID["m1"] = f.m
	f.connect()
	t.Cleanup(f.drop)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/machines/{mid}/file/download", h.withMachine(h.downloadFile))
	mux.HandleFunc("GET /api/v1/machines/{mid}/file/thumb", h.withMachine(h.fileThumb))
	mux.HandleFunc("GET /api/v1/machines/{mid}/file/convert", h.withMachine(h.fileConvert))
	return h, mux
}

func TestDownloadStreamsWholeFileAndRanges(t *testing.T) {
	data := bytes.Repeat([]byte{0, 255, 2, 3}, proto.FileDownloadChunkBytes+9)
	f := &downloadNode{data: data}
	mux := downloadHub(t, f)
	for _, test := range []struct {
		rangeHeader  string
		code         int
		want         []byte
		contentRange string
	}{
		{"", 200, data, ""}, {"bytes=0-1", 206, data[:2], "bytes 0-1/" + strconv.Itoa(len(data))},
		{"bytes=-17", 206, data[len(data)-17:], "bytes " + strconv.Itoa(len(data)-17) + "-" + strconv.Itoa(len(data)-1) + "/" + strconv.Itoa(len(data))},
		{"bytes=1048570-1048590", 206, data[1048570:1048591], "bytes 1048570-1048590/" + strconv.Itoa(len(data))},
	} {
		req := httptest.NewRequest("GET", "/api/v1/machines/m1/file/download?path=preview.mp4&cwd=/work", nil)
		if test.rangeHeader != "" {
			req.Header.Set("Range", test.rangeHeader)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != test.code || !bytes.Equal(rec.Body.Bytes(), test.want) || rec.Header().Get("Content-Range") != test.contentRange {
			t.Fatalf("range %q: status=%d size=%d headers=%v", test.rangeHeader, rec.Code, rec.Body.Len(), rec.Header())
		}
		if rec.Header().Get("Accept-Ranges") != "bytes" || rec.Header().Get("Content-Type") != "video/mp4" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatal(rec.Header())
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.requests {
		if p.Length > proto.FileDownloadChunkBytes {
			t.Fatal("unbounded read")
		}
		if p.Length > 0 && p.Revision != "r1" {
			t.Fatal("no revision")
		}
	}
}

func TestDownloadRefusesOldNodesAndBadChunks(t *testing.T) {
	for _, test := range []struct {
		f           *downloadNode
		path        string
		rangeHeader string
		code        int
	}{
		{&downloadNode{old: true}, "?path=x.mp4", "", 409}, {&downloadNode{}, "", "", 400},
		{&downloadNode{data: []byte{0, 1}}, "?path=x.mp4", "bytes=8-10", 416},
	} {
		req := httptest.NewRequest("GET", "/api/v1/machines/m1/file/download"+test.path, nil)
		req.Header.Set("Range", test.rangeHeader)
		rec := httptest.NewRecorder()
		downloadHub(t, test.f).ServeHTTP(rec, req)
		if rec.Code != test.code {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	// A chunk of another revision is not sent, and the response is aborted rather than ended (#10855).
	f := &downloadNode{data: []byte{0, 1, 2}, bad: true}
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Fatalf("not aborted: %v", v)
			}
		}()
		downloadHub(t, f).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/file/download?path=x.mp4", nil))
	}()
	if rec.Body.Len() != 0 {
		t.Fatal("changed chunk was sent")
	}
	if !strings.Contains(rec.Header().Get("Content-Length"), "3") {
		t.Fatal(rec.Header())
	}
}

func TestDownloadReaderCancellationAndSeek(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &remoteFileReader{size: 10, get: func(proto.FileChunkRequest) (*proto.FileChunk, error) { return nil, ctx.Err() }}
	if _, err := reader.Read(make([]byte, 3)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := reader.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek")
	}
	if _, err := reader.Seek(11, io.SeekStart); err == nil {
		t.Fatal("seek after EOF")
	}
	if _, err := reader.Seek(0, 99); err == nil {
		t.Fatal("invalid whence")
	}
}

func TestDownloadInlineOnlyForWhatABrowserShows(t *testing.T) {
	for _, test := range []struct {
		mime, query, disposition string
		sandbox                  bool
	}{
		{"video/mp4", "&inline=1", "inline;", true},
		{"audio/mpeg", "&inline=1", "inline;", true},
		{"application/pdf", "&inline=1", "inline;", false},
		{"image/svg+xml", "&inline=1", "inline;", true}, // shown, but never as the app's origin
		{"text/html", "&inline=1", "attachment;", true},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "&inline=1", "attachment;", true},
		{"video/mp4", "", "attachment;", true},
	} {
		rec := httptest.NewRecorder()
		downloadHub(t, &downloadNode{data: []byte{1, 2, 3}, mime: test.mime}).ServeHTTP(rec,
			httptest.NewRequest("GET", "/api/v1/machines/m1/file/download?path=x"+test.query, nil))
		h := rec.Header()
		if rec.Code != 200 || !strings.HasPrefix(h.Get("Content-Disposition"), test.disposition) ||
			(h.Get("Content-Security-Policy") == "sandbox") != test.sandbox || h.Get("Content-Type") != test.mime {
			t.Fatalf("%s%s: %d %v", test.mime, test.query, rec.Code, h)
		}
	}
}

func TestThumbnailCachesAndExplainsWhatItCannotDraw(t *testing.T) {
	f := &downloadNode{}
	mux := downloadHub(t, f)
	get := func(path, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/machines/m1/file/thumb?path="+path+"&cwd=/work&size=256", nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := get("a.png", "")
	if rec.Code != 200 || rec.Body.String() != "png" || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("ETag") != `"r1-256"` || !strings.Contains(rec.Header().Get("Cache-Control"), "private") {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if rec := get("a.png", `"r1-256"`); rec.Code != 304 || rec.Body.Len() != 0 ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("revalidated: %d %v", rec.Code, rec.Header())
	}
	if rec := get("x.bin", ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), proto.CodeNoThumbnail) {
		t.Fatalf("undrawable: %d %s", rec.Code, rec.Body)
	}
	if rec := get("", ""); rec.Code != 400 {
		t.Fatalf("no path: %d", rec.Code)
	}
	f.mu.Lock()
	if len(f.thumbs) != 3 || f.thumbs[0].Size != 256 || f.thumbs[0].Cwd != "/work" || f.thumbs[1].ETag != "r1-256" {
		t.Fatalf("%+v", f.thumbs)
	}
	f.mu.Unlock()
	rec = httptest.NewRecorder()
	downloadHub(t, &downloadNode{old: true}).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/file/thumb?path=a.png", nil))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), proto.CodeNodeOutdated) {
		t.Fatalf("old node: %d %s", rec.Code, rec.Body)
	}
}

func TestConvertNamesThePDFOrSaysWhatIsMissing(t *testing.T) {
	for _, test := range []struct {
		f      *downloadNode
		query  string
		code   int
		answer string
	}{
		{&downloadNode{}, "?path=old.doc&to=pdf", 200, "/cache/convert/ab.pdf"},
		{&downloadNode{}, "?path=deck.key", 404, proto.CodeNoConverter},
		{&downloadNode{old: true}, "?path=old.doc", 409, proto.CodeNodeOutdated},
		{&downloadNode{}, "", 400, "path is required"},
	} {
		rec := httptest.NewRecorder()
		downloadHub(t, test.f).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/file/convert"+test.query, nil))
		if rec.Code != test.code || !strings.Contains(rec.Body.String(), test.answer) {
			t.Fatalf("%s: %d %s", test.query, rec.Code, rec.Body)
		}
	}
}

// Previews and downloads do not depend on the file transfer switch (off for a machine that never turned it on, as
// here): the machine's file still streams.
func TestDownloadDoesNotNeedTheFileTransferSwitch(t *testing.T) {
	f := &downloadNode{data: []byte("0123456789")}
	rec := httptest.NewRecorder()
	downloadHub(t, f).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/file/download?path=preview.mp4", nil))
	if rec.Code != 200 || rec.Body.String() != "0123456789" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}

func TestDownloadHardensCraftedMIMEAndDisposition(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mime        string
		wantCT      string
		disposition string
		sandbox     bool
	}{
		{"comma-smuggled pdf", "application/pdf, text/html", "application/octet-stream", "attachment", true},
		{"comma-smuggled image", "image/png, text/html", "application/octet-stream", "attachment", true},
		{"pdf suffix mismatch", "application/pdf+xml", "application/pdf+xml", "attachment", true},
		{"pdf with params stripped", "APPLICATION/PDF; foo=bar", "application/pdf", "inline", false},
		{"empty mime", "", "application/octet-stream", "attachment", true},
		{"whitespace mime", "   ", "application/octet-stream", "attachment", true},
		{"slashless mime", "pdf", "application/octet-stream", "attachment", true},
		{"text with charset", "text/plain; charset=utf-8", "text/plain; charset=utf-8", "attachment", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			f := &downloadNode{data: []byte("<script>alert(1)</script>"), mime: tc.mime, rawMime: true}
			downloadHub(t, f).ServeHTTP(rec,
				httptest.NewRequest("GET", "/api/v1/machines/m1/file/download?path=x&inline=1", nil))
			h := rec.Header()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, headers = %v", rec.Code, h)
			}
			if got := h.Get("Content-Type"); got != tc.wantCT {
				t.Fatalf("Content-Type = %q, want %q", got, tc.wantCT)
			}
			if got := h.Get("Content-Disposition"); got != tc.disposition && !strings.HasPrefix(got, tc.disposition+";") {
				t.Fatalf("Content-Disposition = %q, want %q", got, tc.disposition)
			}
			if got := h.Get("Content-Security-Policy") == "sandbox"; got != tc.sandbox {
				t.Fatalf("sandbox = %v, want %v (CSP=%q)", got, tc.sandbox, h.Get("Content-Security-Policy"))
			}
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
	rec := httptest.NewRecorder()
	f := &downloadNode{data: []byte("hi"), mime: "text/html", rawMime: true, name: "evil\"\r\nX-Injected: 1.html"}
	downloadHub(t, f).ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/v1/machines/m1/file/download?path=x&inline=1", nil))
	cd := rec.Header().Get("Content-Disposition")
	if (cd != "attachment" && !strings.HasPrefix(cd, "attachment;")) || strings.ContainsAny(cd, "\r\n") {
		t.Fatalf("unsafe Content-Disposition = %q", cd)
	}
}

func TestThumbnailRejectsSVGAndMalformedMIMEAndSandboxesValidImages(t *testing.T) {
	for _, bad := range []string{"image/svg+xml", "image/png, text/html", "text/html", "image/x-icon", "not-a-mime"} {
		t.Run("reject_"+bad, func(t *testing.T) {
			rec := httptest.NewRecorder()
			downloadHub(t, &downloadNode{thumbMime: bad}).ServeHTTP(rec,
				httptest.NewRequest("GET", "/api/v1/machines/m1/file/thumb?path=a.svg&size=256", nil))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (headers=%v)", rec.Code, rec.Header())
			}
			if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "max-age") {
				t.Fatalf("502 response must not be cached: Cache-Control=%q", got)
			}
			if got := rec.Header().Get("ETag"); got != "" {
				t.Fatalf("502 response must not set ETag: %q", got)
			}
		})
	}
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"image/png", "image/png"},
		{"IMAGE/JPEG; quality=80", "image/jpeg"},
		{"image/webp", "image/webp"},
		{"image/gif", "image/gif"},
		{"image/avif", "image/avif"},
	} {
		t.Run("allow_"+tc.want, func(t *testing.T) {
			rec := httptest.NewRecorder()
			downloadHub(t, &downloadNode{thumbMime: tc.raw}).ServeHTTP(rec,
				httptest.NewRequest("GET", "/api/v1/machines/m1/file/thumb?path=a.png&size=256", nil))
			h := rec.Header()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := h.Get("Content-Type"); got != tc.want {
				t.Fatalf("Content-Type = %q, want %q", got, tc.want)
			}
			if got := h.Get("Content-Security-Policy"); got != "sandbox" {
				t.Fatalf("Content-Security-Policy = %q, want sandbox", got)
			}
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}
