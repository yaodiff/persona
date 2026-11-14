package clipshare

// Sharing this computer's clipboard with the phone (#3064, docs/operations/clipboard.md): clipboard.get reads it,
// clipboard.set puts text or an image on it. The node runs in the user's login session (a LaunchAgent in gui/<uid>
// on a Mac, a scheduled task in the interactive session on Windows), so the clipboard it reaches is the user's own.
// Whether the user allowed it for this computer is the manager's to check; the node only says what it can do.
//
// Content never goes into a log, an error message or a file: it moves through pipes (macOS, Linux) or memory
// (Windows) only. Items a password manager marks as concealed or transient are reported as "concealed", without them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// clipBackend is one OS's way to the clipboard. set gets text (mime "") or an image's bytes and its mime type.
type clipBackend interface {
	support() proto.ClipboardSupport
	get(ctx context.Context) (proto.ClipboardContent, error)
	set(ctx context.Context, mime string, data []byte) error
}

// clipTimeout bounds one read or write: a clipboard owner that does not answer (X11's selection owner, a busy
// Windows clipboard) must not hold the request.
const clipTimeout = 15 * time.Second

// installOS is the OS the backend is chosen for (tests set it).
var installOS = runtime.GOOS

// clipboardBackend is the backend for installOS (tests set it).
var clipboardBackend = func() clipBackend {
	switch installOS {
	case "darwin":
		return macClip{}
	case "windows":
		return winClip{}
	case "linux":
		return newLinuxClip()
	}
	return unsupportedClip{reason: proto.ClipboardReasonOS}
}

// Support is node.info's clipboard.
func Support() *proto.ClipboardSupport {
	s := clipboardBackend().support()
	return &s
}

func clipUnsupported(reason string) error {
	return &rpc.Error{Code: proto.CodeClipboardUnsupported, Message: reason}
}

// clipFailed is a read or write that failed: what failed, never the content.
func clipFailed(what string, err error) error {
	msg := what
	if err != nil {
		msg += ": " + err.Error()
	}
	return &rpc.Error{Code: proto.CodeClipboardFailed, Message: msg}
}

func clipTooLarge(what string, n int) error {
	return &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("%s is too large (%d bytes)", what, n)}
}

// Get is clipboard.get: {} → proto.ClipboardContent.
func Get(ctx context.Context) (any, error) {
	b := clipboardBackend()
	if s := b.support(); !s.Supported {
		return nil, clipUnsupported(s.Reason)
	}
	ctx, cancel := context.WithTimeout(ctx, clipTimeout)
	defer cancel()
	c, err := b.get(ctx)
	if err != nil {
		return nil, err
	}
	return checkClipContent(c)
}

// checkClipContent holds what a backend read to the limits, and drops content a kind says is not there.
func checkClipContent(c proto.ClipboardContent) (proto.ClipboardContent, error) {
	switch c.Kind {
	case proto.ClipboardKindText:
		if c.Text == nil {
			return proto.ClipboardContent{Kind: proto.ClipboardKindEmpty}, nil
		}
		if len(*c.Text) > proto.ClipboardTextMost {
			return proto.ClipboardContent{}, clipTooLarge("the text", len(*c.Text))
		}
		if !utf8.ValidString(*c.Text) {
			t := strings.ToValidUTF8(*c.Text, "�")
			c.Text = &t
		}
		return proto.ClipboardContent{Kind: c.Kind, Text: c.Text}, nil
	case proto.ClipboardKindImage:
		if c.Image == nil || len(c.Image.Data) == 0 {
			return proto.ClipboardContent{Kind: proto.ClipboardKindEmpty}, nil
		}
		if len(c.Image.Data) > proto.ClipboardImageMost {
			return proto.ClipboardContent{}, clipTooLarge("the image", len(c.Image.Data))
		}
		return proto.ClipboardContent{Kind: c.Kind, Image: c.Image}, nil
	case proto.ClipboardKindConcealed:
		return proto.ClipboardContent{Kind: c.Kind}, nil
	}
	return proto.ClipboardContent{Kind: proto.ClipboardKindEmpty}, nil
}

// Set is clipboard.set: {"text": …} or {"image": {"mime", "data"}} → {}.
func Set(ctx context.Context, params json.RawMessage) (any, error) {
	var in proto.ClipboardContent
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, &rpc.Error{Message: "bad params: " + err.Error()}
	}
	mime, data, err := clipPayload(in)
	if err != nil {
		return nil, err
	}
	b := clipboardBackend()
	if s := b.support(); !s.Supported {
		return nil, clipUnsupported(s.Reason)
	}
	ctx, cancel := context.WithTimeout(ctx, clipTimeout)
	defer cancel()
	if err := b.set(ctx, mime, data); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// clipPayload checks clipboard.set's params: text or an image (PNG or JPEG whose bytes say so), within the limits.
func clipPayload(in proto.ClipboardContent) (mime string, data []byte, err error) {
	switch {
	case in.Text != nil && in.Image != nil:
		return "", nil, &rpc.Error{Message: "text or image, not both"}
	case in.Text != nil:
		if len(*in.Text) > proto.ClipboardTextMost {
			return "", nil, clipTooLarge("the text", len(*in.Text))
		}
		return "", []byte(*in.Text), nil
	case in.Image != nil:
		if len(in.Image.Data) > proto.ClipboardImageMost {
			return "", nil, clipTooLarge("the image", len(in.Image.Data))
		}
		mime := strings.ToLower(strings.TrimSpace(in.Image.Mime))
		if mime == "image/jpg" {
			mime = "image/jpeg"
		}
		if got := sniffImage(in.Image.Data); got == "" || got != mime {
			return "", nil, &rpc.Error{Code: proto.CodeClipboardBadImage, Message: "the image is not a PNG or JPEG as its mime type says"}
		}
		return mime, in.Image.Data, nil
	}
	return "", nil, &rpc.Error{Message: "text or image is required"}
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// sniffImage is the mime type the bytes start like: image/png, image/jpeg or "".
func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, pngMagic):
		return "image/png"
	case len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	}
	return ""
}

// unsupportedClip is a clipboard the node cannot reach, and why.
type unsupportedClip struct{ reason string }

func (u unsupportedClip) support() proto.ClipboardSupport {
	return proto.ClipboardSupport{Reason: u.reason}
}
func (u unsupportedClip) get(context.Context) (proto.ClipboardContent, error) {
	return proto.ClipboardContent{}, clipUnsupported(u.reason)
}
func (u unsupportedClip) set(context.Context, string, []byte) error { return clipUnsupported(u.reason) }

// clipOutMost is the most a helper may print: the largest content plus a line saying what it is.
const clipOutMost = proto.ClipboardImageMost + 1024

// runClipTool runs a helper with stdin (nil: none) and returns what it printed, at most clipOutMost bytes (beyond
// that, errTooMuch). Its stderr is kept for the error, never stdout (the content).
func runClipTool(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b, rerr := io.ReadAll(io.LimitReader(out, clipOutMost+1))
	if len(b) > clipOutMost {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errTooMuch
	}
	werr := cmd.Wait()
	if werr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return b, fmt.Errorf("%w: %s", werr, msg)
		}
		return b, werr
	}
	return b, rerr
}

var errTooMuch = errors.New("the clipboard holds more than can be sent")

// limitedWriter keeps the first n bytes written to it and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// macClip is the Mac's general pasteboard through osascript's JavaScript for Automation (JXA). pbcopy and pbpaste
// cannot do this: they see only plain text, cannot see the pasteboard's types (so not the password managers'
// concealed marker), and read and write in the locale's encoding (a LaunchAgent without LANG turns Chinese into
// MacRoman). JXA reaches NSPasteboard itself, ships with every macOS, and needs no cgo or helper binary to be built
// and signed for the node. The content goes through its stdin and stdout, never its arguments or a file.
type macClip struct{}

// osascriptPath is the osascript to run (tests give a fake).
var osascriptPath = "/usr/bin/osascript"

func (macClip) support() proto.ClipboardSupport { return proto.ClipboardSupport{Supported: true} }

// macClipGet prints a line with the kind (text, image, empty, concealed) and then the content: UTF-8 text or PNG.
// Text comes first: a spreadsheet's cells and Finder's files also carry a picture of themselves, and the text is what
// was meant; an image alone (a screenshot, "Copy Image") is taken as PNG, TIFF turned into PNG.
const macClipGet = `ObjC.import('AppKit');
function run(argv) {
  var out = $.NSFileHandle.fileHandleWithStandardOutput;
  function emit(kind, data) {
    out.writeData($(kind + '\n').dataUsingEncoding($.NSUTF8StringEncoding));
    if (data) out.writeData(data);
  }
  var pb = $.NSPasteboard.generalPasteboard;
  var types = ObjC.deepUnwrap(pb.types) || [];
  if (types.indexOf('org.nspasteboard.ConcealedType') >= 0 || types.indexOf('org.nspasteboard.TransientType') >= 0) { emit('concealed'); return; }
  var s = pb.stringForType('public.utf8-plain-text');
  if (!s.isNil()) { emit('text', s.dataUsingEncoding($.NSUTF8StringEncoding)); return; }
  var png = pb.dataForType('public.png');
  if (png.isNil()) {
    var tiff = pb.dataForType('public.tiff');
    if (!tiff.isNil()) {
      var rep = $.NSBitmapImageRep.imageRepWithData(tiff);
      if (!rep.isNil()) png = rep.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $.NSDictionary.dictionary);
    }
  }
  if (!png.isNil()) { emit('image', png); return; }
  emit('empty');
}`

// macClipSet reads the content from stdin; argv[0] is "text", "image/png" or "image/jpeg". An image goes on as PNG
// and TIFF (apps that read only one of them). Prints ok, bad (unreadable content) or fail.
const macClipSet = `ObjC.import('AppKit');
function run(argv) {
  var kind = argv[0];
  var data = $.NSFileHandle.fileHandleWithStandardInput.readDataToEndOfFile;
  var pb = $.NSPasteboard.generalPasteboard;
  var ok;
  if (kind === 'text') {
    var s = $.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding);
    if (s.isNil()) return 'bad';
    pb.clearContents;
    ok = pb.setStringForType(s, 'public.utf8-plain-text');
  } else {
    var rep = $.NSBitmapImageRep.imageRepWithData(data);
    if (rep.isNil()) return 'bad';
    var png = kind === 'image/png' ? data : rep.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $.NSDictionary.dictionary);
    pb.clearContents;
    ok = pb.setDataForType(png, 'public.png') && pb.setDataForType(rep.TIFFRepresentation, 'public.tiff');
  }
  return ok ? 'ok' : 'fail';
}`

func (macClip) get(ctx context.Context) (proto.ClipboardContent, error) {
	out, err := runClipTool(ctx, nil, osascriptPath, "-l", "JavaScript", "-e", macClipGet)
	if errors.Is(err, errTooMuch) {
		return proto.ClipboardContent{}, clipTooLarge("the clipboard", clipOutMost)
	}
	if err != nil {
		return proto.ClipboardContent{}, clipFailed("read the clipboard", err)
	}
	kind, data, ok := bytes.Cut(out, []byte("\n"))
	if !ok {
		return proto.ClipboardContent{}, clipFailed("read the clipboard", errors.New("no answer"))
	}
	switch string(kind) {
	case proto.ClipboardKindText:
		t := string(data)
		return proto.ClipboardContent{Kind: proto.ClipboardKindText, Text: &t}, nil
	case proto.ClipboardKindImage:
		return proto.ClipboardContent{Kind: proto.ClipboardKindImage, Image: &proto.ClipboardImage{Mime: "image/png", Data: data}}, nil
	case proto.ClipboardKindConcealed, proto.ClipboardKindEmpty:
		return proto.ClipboardContent{Kind: string(kind)}, nil
	}
	return proto.ClipboardContent{}, clipFailed("read the clipboard", errors.New("unexpected answer"))
}

func (macClip) set(ctx context.Context, mime string, data []byte) error {
	kind := mime
	if kind == "" {
		kind = "text"
	}
	out, err := runClipTool(ctx, data, osascriptPath, "-l", "JavaScript", "-e", macClipSet, kind)
	if err != nil {
		return clipFailed("write the clipboard", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "ok":
		return nil
	case "bad":
		return &rpc.Error{Code: proto.CodeClipboardBadImage, Message: "the content could not be read"}
	}
	return clipFailed("write the clipboard", nil)
}
