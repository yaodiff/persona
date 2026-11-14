package clipshare

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// tinyPNG is a 2×2 PNG.
var tinyPNG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}()

// fakeOsascript stands in for osascript: a file is the pasteboard ("<kind>\n<content>"). Setting writes the kind it
// was given and stdin; reading prints the file.
func fakeOsascript(t *testing.T) (board string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	board = filepath.Join(dir, "board")
	script := filepath.Join(dir, "osascript")
	body := "#!/bin/sh\n" +
		"for last; do :; done\n" +
		"case \"$last\" in\n" +
		"  text|image/png|image/jpeg) k=$last; [ \"$k\" = text ] || k=image; { printf '%s\\n' \"$k\"; cat; } > '" + board + "'; echo ok ;;\n" +
		"  *) if [ -f '" + board + "' ]; then cat '" + board + "'; else echo empty; fi ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath, oldOS := osascriptPath, installOS
	osascriptPath, installOS = script, "darwin"
	t.Cleanup(func() { osascriptPath, installOS = oldPath, oldOS })
	return board
}

func getClip(t *testing.T) proto.ClipboardContent {
	t.Helper()
	out, err := Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return out.(proto.ClipboardContent)
}

// Text and images go both ways on a Mac, through osascript's stdin and stdout.
func TestMacClipShareRoundTrip(t *testing.T) {
	board := fakeOsascript(t)
	if c := getClip(t); c.Kind != proto.ClipboardKindEmpty || c.Text != nil || c.Image != nil {
		t.Fatalf("empty: %+v", c)
	}
	if _, err := Set(context.Background(), json.RawMessage(`{"text":"从手机来的 · ok\nline 2"}`)); err != nil {
		t.Fatal(err)
	}
	if c := getClip(t); c.Kind != proto.ClipboardKindText || c.Text == nil || *c.Text != "从手机来的 · ok\nline 2" {
		t.Fatalf("text: %+v", c)
	}
	params, _ := json.Marshal(proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/png", Data: tinyPNG}})
	if _, err := Set(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	c := getClip(t)
	if c.Kind != proto.ClipboardKindImage || c.Image == nil || c.Image.Mime != "image/png" || string(c.Image.Data) != string(tinyPNG) {
		t.Fatalf("image: %+v", c)
	}
	// What a password manager marks as concealed comes back without its content.
	_ = os.WriteFile(board, []byte("concealed\n"), 0o600)
	if c := getClip(t); c.Kind != proto.ClipboardKindConcealed || c.Text != nil || c.Image != nil {
		t.Fatalf("concealed: %+v", c)
	}
}

// What clipboard.set takes: text or an image whose bytes match its type, within the limits.
func TestClipShareSetChecks(t *testing.T) {
	fakeOsascript(t)
	big := strings.Repeat("x", proto.ClipboardTextMost+1)
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}
	for _, tc := range []struct {
		name string
		in   proto.ClipboardContent
		code string
	}{
		{"nothing", proto.ClipboardContent{}, ""},
		{"both", proto.ClipboardContent{Text: new(string), Image: &proto.ClipboardImage{Mime: "image/png", Data: tinyPNG}}, ""},
		{"long text", proto.ClipboardContent{Text: &big}, proto.CodeTooLarge},
		{"big image", proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/png", Data: append(append([]byte{}, tinyPNG...), make([]byte, proto.ClipboardImageMost)...)}}, proto.CodeTooLarge},
		{"not an image", proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/png", Data: []byte("hello")}}, proto.CodeClipboardBadImage},
		{"wrong type", proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/png", Data: jpeg}}, proto.CodeClipboardBadImage},
		{"gif", proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/gif", Data: []byte("GIF89a")}}, proto.CodeClipboardBadImage},
	} {
		params, _ := json.Marshal(tc.in)
		_, err := Set(context.Background(), params)
		if err == nil || rpcCode(err) != tc.code {
			t.Errorf("%s: %v (code %q, want %q)", tc.name, err, rpcCode(err), tc.code)
		}
	}
	params, _ := json.Marshal(proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/jpg", Data: jpeg}})
	if _, err := Set(context.Background(), params); err != nil {
		t.Fatalf("a JPEG called image/jpg: %v", err)
	}
}

// An error says what failed, never what was on the clipboard.
func TestClipShareErrorsLeaveContentOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "osascript")
	_ = os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'execution error' >&2\nexit 1\n"), 0o755)
	oldPath, oldOS := osascriptPath, installOS
	osascriptPath, installOS = script, "darwin"
	t.Cleanup(func() { osascriptPath, installOS = oldPath, oldOS })
	_, err := Set(context.Background(), json.RawMessage(`{"text":"secret-value-123"}`))
	if rpcCode(err) != proto.CodeClipboardFailed || strings.Contains(err.Error(), "secret-value-123") {
		t.Fatalf("%v", err)
	}
}

// node.info says whether this OS has a clipboard the node reaches.
func TestClipShareSupport(t *testing.T) {
	old := installOS
	t.Cleanup(func() { installOS = old })
	installOS = "darwin"
	if s := Support(); !s.Supported {
		t.Fatalf("mac: %+v", s)
	}
	installOS = "plan9"
	if s := Support(); s.Supported || s.Reason != proto.ClipboardReasonOS {
		t.Fatalf("plan9: %+v", s)
	}
	_, err := Get(context.Background())
	if rpcCode(err) != proto.CodeClipboardUnsupported {
		t.Fatalf("get on plan9: %v", err)
	}
}

// The real pasteboard of the Mac running the test (PERSONA_CLIPBOARD_LIVE=1): text with characters outside ASCII and
// an image go on and come back; what was there before is put back.
func TestMacClipShareLive(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("PERSONA_CLIPBOARD_LIVE") != "1" {
		t.Skip("PERSONA_CLIPBOARD_LIVE=1 on a Mac")
	}
	before := getClip(t)
	t.Cleanup(func() {
		if before.Text != nil {
			_, _ = Set(context.Background(), mustJSON(proto.ClipboardContent{Text: before.Text}))
		} else if before.Image != nil {
			_, _ = Set(context.Background(), mustJSON(proto.ClipboardContent{Image: before.Image}))
		} else {
			_ = exec.Command(osascriptPath, "-l", "JavaScript", "-e", "ObjC.import('AppKit'); $.NSPasteboard.generalPasteboard.clearContents; ''").Run()
		}
	})
	text := "从手机来的 · ok ✓ 日本語"
	if _, err := Set(context.Background(), mustJSON(proto.ClipboardContent{Text: &text})); err != nil {
		t.Fatal(err)
	}
	if c := getClip(t); c.Kind != proto.ClipboardKindText || *c.Text != text {
		t.Fatalf("text: %+v", c)
	}
	if _, err := Set(context.Background(), mustJSON(proto.ClipboardContent{Image: &proto.ClipboardImage{Mime: "image/png", Data: tinyPNG}})); err != nil {
		t.Fatal(err)
	}
	if c := getClip(t); c.Kind != proto.ClipboardKindImage || sniffImage(c.Image.Data) != "image/png" {
		t.Fatalf("image: kind %s", c.Kind)
	}
}
