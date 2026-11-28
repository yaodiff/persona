package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/preview"
)

// previewPage: a button that renames the page, a field that does too as it is typed in, and a link elsewhere.
const previewPage = `<!doctype html><html><head><title>Hi</title><style>
body { margin: 0; background: #00f } #go { position: absolute; left: 0; top: 0; width: 100px; height: 50px }
#in { position: absolute; left: 0; top: 100px; width: 200px; height: 40px }
#ext { position: absolute; left: 0; top: 200px; width: 200px; height: 40px; display: block }
#next { position: absolute; left: 0; top: 300px; width: 200px; height: 40px; display: block }
</style></head><body>
<button id="go" onclick="document.title='clicked'">Go</button>
<input id="in" oninput="document.title='v:'+this.value">
<a id="ext" href="https://example.com/away">elsewhere</a>
<a id="next" href="/second">second</a>
</body></html>`

// liveTab opens a preview tab of the browser on this computer, at url, and returns the web's end of its stream.
func liveTab(t *testing.T, url string) (*bufio.Reader, net.Conn) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := findBrowser(); err != nil {
		t.Skipf("no browser here: %v", err)
	}
	prof := t.TempDir()
	was := previewProfileDir
	previewProfileDir = func() (string, error) { return prof, nil }
	t.Cleanup(func() { previewProfileDir = was })
	h := &previewHost{logf: log.Printf}
	t.Cleanup(h.close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b, err := h.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := b.newTab(ctx, nil, previewViewport{w: 400, h: 400, dpr: 1}.clamp())
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := net.Pipe()
	tab.conn = mine
	go func() {
		defer h.release()
		tab.run(url)
	}()
	t.Cleanup(func() { theirs.Close() })
	return bufio.NewReader(theirs), theirs
}

type liveMsg struct {
	kind byte
	json map[string]any
	bin  []byte
}

// next reads messages until want says one is it, acknowledging pictures as the web does.
func next(t *testing.T, r *bufio.Reader, w net.Conn, what string, want func(liveMsg) bool) liveMsg {
	t.Helper()
	if err := w.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer w.SetReadDeadline(time.Time{})
	for {
		k, p, err := preview.ReadFrame(r)
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		m := liveMsg{kind: k, bin: p}
		if k == preview.Text {
			_ = json.Unmarshal(p, &m.json)
		} else if len(p) > 0 && p[0] == previewFrameMsg {
			say(t, w, map[string]any{"t": "ack", "seq": binary.BigEndian.Uint32(p[1:])})
		}
		if want(m) {
			return m
		}
	}
}

func say(t *testing.T, w net.Conn, v any) {
	b, _ := json.Marshal(v)
	if err := preview.WriteFrame(w, preview.Text, b); err != nil {
		t.Errorf("write: %v", err)
	}
}

func isText(typ string, more func(map[string]any) bool) func(liveMsg) bool {
	return func(m liveMsg) bool {
		return m.kind == preview.Text && m.json["t"] == typ && (more == nil || more(m.json))
	}
}

func click(t *testing.T, w net.Conn, x, y float64) {
	say(t, w, map[string]any{"t": "mouse", "type": "move", "x": x, "y": y})
	say(t, w, map[string]any{"t": "mouse", "type": "down", "x": x, "y": y, "button": "left", "buttons": 1, "clicks": 1})
	say(t, w, map[string]any{"t": "mouse", "type": "up", "x": x, "y": y, "button": "left", "clicks": 1})
}

func TestPreviewDrivesARealBrowser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/away" {
			http.Redirect(w, r, "https://example.com/redirected", http.StatusFound)
			return
		}
		if r.URL.Path == "/second" {
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>two</p>`)
			return
		}
		fmt.Fprint(w, previewPage)
	}))
	defer srv.Close()
	home := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/"
	r, w := liveTab(t, home)

	next(t, r, w, "ready", isText("ready", nil))
	// Chrome reports the document title and paints the page independently.
	var titled, painted bool
	next(t, r, w, "the page title and its picture", func(m liveMsg) bool {
		if m.kind == preview.Text && m.json["t"] == "page" && m.json["url"] == home && m.json["title"] == "Hi" {
			titled = true
		}
		if m.kind == preview.Binary && len(m.bin) > 9 && m.bin[0] == previewFrameMsg && binary.BigEndian.Uint16(m.bin[5:]) == 400 {
			frame, err := jpeg.Decode(bytes.NewReader(m.bin[9:]))
			if err != nil {
				t.Fatal(err)
			}
			red, green, blue, _ := frame.At(350, 350).RGBA()
			painted = painted || (blue > 0xc000 && red < 0x4000 && green < 0x4000)
		}
		return titled && painted
	})

	click(t, w, 50, 25)
	next(t, r, w, "the click", isText("page", func(m map[string]any) bool { return m["title"] == "clicked" }))

	click(t, w, 100, 120)
	say(t, w, map[string]any{"t": "key", "type": "down", "key": "a", "code": "KeyA", "key_code": 65, "text": "a"})
	say(t, w, map[string]any{"t": "key", "type": "up", "key": "a", "code": "KeyA", "key_code": 65})
	say(t, w, map[string]any{"t": "text", "text": "好"})
	next(t, r, w, "the typing", isText("page", func(m map[string]any) bool { return m["title"] == "v:a好" }))

	// a link elsewhere is not followed: the web is told, the page stays
	click(t, w, 50, 220)
	next(t, r, w, "the link elsewhere", isText("external", func(m map[string]any) bool { return m["url"] == "https://example.com/away" }))
	say(t, w, map[string]any{"t": "nav", "url": "/away"}) // a local address that redirects elsewhere
	next(t, r, w, "the redirect elsewhere", isText("external", func(m map[string]any) bool { return m["url"] == "https://example.com/redirected" }))
	say(t, w, map[string]any{"t": "nav", "url": "http://192.0.2.10:8080/"})
	next(t, r, w, "the address elsewhere", isText("external", func(m map[string]any) bool { return m["url"] == "http://192.0.2.10:8080/" }))

	say(t, w, map[string]any{"t": "shot", "id": 7})
	s := next(t, r, w, "the screenshot", func(m liveMsg) bool { return m.kind == preview.Binary && m.bin[0] == previewShotMsg })
	if binary.BigEndian.Uint32(s.bin[1:]) != 7 || !bytes.HasPrefix(s.bin[5:], []byte("\x89PNG")) {
		t.Fatalf("screenshot: % x…", s.bin[:12])
	}

	say(t, w, map[string]any{"t": "pick", "on": true})
	click(t, w, 50, 25)
	p := next(t, r, w, "the picked element", isText("picked", nil))
	if p.json["selector"] != "button#go" || p.json["tag"] != "button" || p.json["text"] != "Go" || p.json["url"] != home || p.json["title"] != "v:a好" {
		t.Fatalf("picked: %v", p.json)
	}

	// a link on this computer is followed, and back comes back
	click(t, w, 50, 320)
	next(t, r, w, "the second page", isText("page", func(m map[string]any) bool { return m["title"] == "Second" && m["back"] == true }))
	say(t, w, map[string]any{"t": "back"})
	next(t, r, w, "back", isText("page", func(m map[string]any) bool { return m["url"] == home && m["forward"] == true }))

	say(t, w, map[string]any{"t": "size", "w": 300, "h": 500, "dpr": 2, "mobile": true})
	f := next(t, r, w, "a phone-sized picture", func(m liveMsg) bool {
		return m.kind == preview.Binary && m.bin[0] == previewFrameMsg && binary.BigEndian.Uint16(m.bin[5:]) == 300
	})
	if binary.BigEndian.Uint16(f.bin[7:]) != 500 {
		t.Fatalf("phone-sized picture is %d high", binary.BigEndian.Uint16(f.bin[7:]))
	}
}

func decodeFrame(t *testing.T, bin []byte) (w, h uint16, img image.Image, format string) {
	t.Helper()
	if len(bin) <= 9 || bin[0] != previewFrameMsg {
		t.Fatalf("not a frame: len=%d", len(bin))
	}
	w = binary.BigEndian.Uint16(bin[5:])
	h = binary.BigEndian.Uint16(bin[7:])
	var err error
	img, format, err = image.Decode(bytes.NewReader(bin[9:]))
	if err != nil {
		t.Fatalf("decode frame (%dx%d header): %v", w, h, err)
	}
	return w, h, img, format
}

func nextDeadline(t *testing.T, r *bufio.Reader, w net.Conn, limit time.Duration, what string, want func(liveMsg) bool) liveMsg {
	t.Helper()
	if err := w.SetReadDeadline(time.Now().Add(limit)); err != nil {
		t.Fatal(err)
	}
	defer w.SetReadDeadline(time.Time{})
	for {
		k, p, err := preview.ReadFrame(r)
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		m := liveMsg{kind: k, bin: p}
		if k == preview.Text {
			_ = json.Unmarshal(p, &m.json)
		} else if len(p) > 0 && p[0] == previewFrameMsg {
			say(t, w, map[string]any{"t": "ack", "seq": binary.BigEndian.Uint32(p[1:])})
		}
		if want(m) {
			return m
		}
	}
}

func TestPreviewDPRAndStillFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>DPR</title><style>
body { margin: 0; background: #00f; color: #fff; font: 16px sans-serif; }
#box { position: absolute; left: 10px; top: 10px; width: 120px; height: 40px; border: 1px solid #ff0; }
</style></head><body><button id="box" onclick="this.style.background='#0a0'">Hi</button></body></html>`)
	}))
	defer srv.Close()
	home := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/"
	r, w := liveTab(t, home)

	next(t, r, w, "ready", isText("ready", nil))
	// Wait until the page itself has painted (skipping any transitional about:blank frame) and verify
	// initial dpr=1 viewport (400x400) decodes to 400x400 (w x h).
	var titled, painted bool
	var f1 liveMsg
	next(t, r, w, "dpr=1 painted page", func(m liveMsg) bool {
		if m.kind == preview.Text && m.json["t"] == "page" && m.json["title"] == "DPR" {
			titled = true
		}
		if m.kind == preview.Binary && len(m.bin) > 9 && m.bin[0] == previewFrameMsg &&
			binary.BigEndian.Uint16(m.bin[5:]) == 400 && binary.BigEndian.Uint16(m.bin[7:]) == 400 {
			_, _, img, _ := decodeFrame(t, m.bin)
			if img.Bounds().Dx() >= 351 && img.Bounds().Dy() >= 351 {
				red, green, blue, _ := img.At(350, 350).RGBA()
				if blue > 0xc000 && red < 0x4000 && green < 0x4000 {
					painted = true
					f1 = m
				}
			}
		}
		return titled && painted
	})
	cw, ch, img1, _ := decodeFrame(t, f1.bin)
	if cw != 400 || ch != 400 || img1.Bounds().Dx() != 400 || img1.Bounds().Dy() != 400 {
		t.Fatalf("dpr=1 frame header=%dx%d pixels=%dx%d, want 400x400 / 400x400", cw, ch, img1.Bounds().Dx(), img1.Bounds().Dy())
	}

	// Switch to dpr=2 (300x500 CSS): once Chrome finishes resizing from 400x400 (ignoring any transitional
	// old-aspect-ratio frame), the screencast frame must decode to 600x1000 (2w x 2h) with CSS header 300x500.
	say(t, w, map[string]any{"t": "size", "w": 300, "h": 500, "dpr": 2, "mobile": false})
	f2 := nextDeadline(t, r, w, 10*time.Second, "dpr=2 frame", func(m liveMsg) bool {
		if m.kind != preview.Binary || len(m.bin) <= 9 || m.bin[0] != previewFrameMsg {
			return false
		}
		if binary.BigEndian.Uint16(m.bin[5:]) != 300 || binary.BigEndian.Uint16(m.bin[7:]) != 500 {
			return false
		}
		_, _, img, _ := decodeFrame(t, m.bin)
		dx, dy := img.Bounds().Dx(), img.Bounds().Dy()
		// Skip a transitional frame still carrying the previous 400x400 aspect ratio.
		return dx*500 == dy*300
	})
	cw, ch, img2, fmt2 := decodeFrame(t, f2.bin)
	if cw != 300 || ch != 500 {
		t.Fatalf("dpr=2 header=%dx%d, want 300x500", cw, ch)
	}
	if img2.Bounds().Dx() != 600 || img2.Bounds().Dy() != 1000 {
		t.Fatalf("dpr=2 decoded pixels=%dx%d (%s), want 600x1000", img2.Bounds().Dx(), img2.Bounds().Dy(), fmt2)
	}

	// Enable PNG still frames and trigger a paint; once still (~150 ms), node must send a PNG f-frame at 2w x 2h.
	say(t, w, map[string]any{"t": "features", "png": true, "select": true})
	click(t, w, 50, 25)
	still2 := nextDeadline(t, r, w, 5*time.Second, "dpr=2 PNG still frame", func(m liveMsg) bool {
		return m.kind == preview.Binary && len(m.bin) > 9 && m.bin[0] == previewFrameMsg &&
			binary.BigEndian.Uint16(m.bin[5:]) == 300 && bytes.HasPrefix(m.bin[9:], []byte("\x89PNG\r\n\x1a\n"))
	})
	cw, ch, png2, fmtP2 := decodeFrame(t, still2.bin)
	if fmtP2 != "png" || cw != 300 || ch != 500 || png2.Bounds().Dx() != 600 || png2.Bounds().Dy() != 1000 {
		t.Fatalf("dpr=2 still frame format=%s header=%dx%d pixels=%dx%d, want png 300x500 / 600x1000",
			fmtP2, cw, ch, png2.Bounds().Dx(), png2.Bounds().Dy())
	}

	// Back to dpr=1 (360x280 CSS): both screencast and PNG still frame must be 360x280.
	say(t, w, map[string]any{"t": "size", "w": 360, "h": 280, "dpr": 1, "mobile": false})
	still1 := nextDeadline(t, r, w, 5*time.Second, "dpr=1 PNG still frame", func(m liveMsg) bool {
		return m.kind == preview.Binary && len(m.bin) > 9 && m.bin[0] == previewFrameMsg &&
			binary.BigEndian.Uint16(m.bin[5:]) == 360 && bytes.HasPrefix(m.bin[9:], []byte("\x89PNG\r\n\x1a\n"))
	})
	cw, ch, png1, fmtP1 := decodeFrame(t, still1.bin)
	if fmtP1 != "png" || cw != 360 || ch != 280 || png1.Bounds().Dx() != 360 || png1.Bounds().Dy() != 280 {
		t.Fatalf("dpr=1 still frame format=%s header=%dx%d pixels=%dx%d, want png 360x280 / 360x280",
			fmtP1, cw, ch, png1.Bounds().Dx(), png1.Bounds().Dy())
	}
}

const previewSelectPage = `<!doctype html><html><head><title>SelectInit</title><style>
* { box-sizing: border-box; }
body { margin: 0; background: #fff; }
#before { position: absolute; left: 10px; top: 10px; width: 140px; height: 30px; }
#fruit  { position: absolute; left: 10px; top: 60px; width: 200px; height: 36px; }
#after  { position: absolute; left: 10px; top: 120px; width: 140px; height: 36px; }
#multi  { position: absolute; left: 10px; top: 180px; width: 140px; height: 60px; }
#listbox { position: absolute; left: 170px; top: 180px; width: 140px; height: 60px; }
</style></head><body>
<input id="before" value="pre">
<select id="fruit">
  <optgroup label="Citrus">
    <option value="apple">  Apple   Pie  </option>
    <option value="orange" disabled>Orange</option>
    <option value="hidden1" hidden>HiddenOne</option>
    <option value="hidden2" style="display:none">HiddenTwo</option>
  </optgroup>
  <optgroup label="Berry" disabled>
    <option value="strawberry">Strawberry</option>
  </optgroup>
  <optgroup label="HiddenGroup" hidden>
    <option value="hiddengroupopt">HiddenGroupOpt</option>
  </optgroup>
  <option value="banana">Banana</option>
</select>
<button id="after">After</button>
<select id="multi" multiple size="3"><option>M1</option><option>M2</option></select>
<select id="listbox" size="3"><option>L1</option><option>L2</option></select>
<script>
window.__events = [];
window.__afterClicks = 0;
const sel = document.getElementById('fruit');
const sync = (tag) => {
  const active = document.activeElement ? document.activeElement.id : '';
  const leak = typeof window.__personaPreviewSelect;
  document.title = tag + '|val=' + sel.value + '|ev=' + window.__events.join(',') + '|act=' + active + '|after=' + window.__afterClicks + '|leak=' + leak;
};
sel.addEventListener('focus', () => sync('focus'));
sel.addEventListener('input', (e) => { window.__events.push('in:' + (e.bubbles ? 'b' : 'nb')); sync('input'); });
sel.addEventListener('change', (e) => { window.__events.push('ch:' + (e.bubbles ? 'b' : 'nb')); sync('change'); });
document.getElementById('after').addEventListener('click', () => { window.__afterClicks++; sync('after'); });
document.getElementById('multi').addEventListener('focus', () => sync('multi'));
document.getElementById('listbox').addEventListener('focus', () => sync('listbox'));
sync('ready');
</script>
</body></html>`

func TestPreviewSelectBridge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, previewSelectPage)
	}))
	defer srv.Close()
	home := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/"
	r, w := liveTab(t, home)

	next(t, r, w, "ready", isText("ready", nil))
	next(t, r, w, "initial page title", isText("page", func(m map[string]any) bool {
		return strings.HasPrefix(fmt.Sprint(m["title"]), "ready|")
	}))

	// 1. Before features {select: true}, clicking <select> must NOT emit select_open.
	click(t, w, 100, 78)
	next(t, r, w, "focus without features", func(m liveMsg) bool {
		if m.kind == preview.Text && m.json["t"] == "select_open" {
			t.Fatalf("received select_open before features {select: true}: %v", m.json)
		}
		return m.kind == preview.Text && m.json["t"] == "page" && strings.HasPrefix(fmt.Sprint(m.json["title"]), "focus|")
	})
	// Dismiss any native popup or focus by clicking #before.
	click(t, w, 50, 25)
	click(t, w, 50, 25)

	// 2. Enable features {select: true, png: true} (applies to current document and future documents).
	say(t, w, map[string]any{"t": "features", "select": true, "png": true})

	// 3. Click <select id="fruit"> -> must emit select_open with exact options, selected, rect.
	click(t, w, 100, 78)
	open1 := nextDeadline(t, r, w, 5*time.Second, "select_open on click", isText("select_open", nil))
	id1 := uint32(open1.json["id"].(float64))
	if id1 == 0 {
		t.Fatalf("select_open id=%d, want > 0", id1)
	}
	if selIdx := int(open1.json["selected"].(float64)); selIdx != 0 {
		t.Fatalf("select_open selected=%d, want 0", selIdx)
	}
	rect, _ := open1.json["rect"].(map[string]any)
	if rect["x"].(float64) < 8 || rect["x"].(float64) > 12 || rect["y"].(float64) < 58 || rect["y"].(float64) > 62 ||
		rect["w"].(float64) < 198 || rect["w"].(float64) > 202 || rect["h"].(float64) < 34 || rect["h"].(float64) > 38 {
		t.Fatalf("select_open rect=%v, want ~{10 60 200 36}", rect)
	}
	wantOpts := []any{
		map[string]any{"index": float64(0), "label": "Apple Pie", "group": "Citrus"},
		map[string]any{"index": float64(1), "label": "Orange", "disabled": true, "group": "Citrus"},
		map[string]any{"index": float64(4), "label": "Strawberry", "disabled": true, "group": "Berry"},
		map[string]any{"index": float64(6), "label": "Banana"},
	}
	if !reflect.DeepEqual(open1.json["options"], wantOpts) {
		t.Fatalf("select_open options:\ngot:  %#v\nwant: %#v", open1.json["options"], wantOpts)
	}

	// Verify native popup did NOT open: dismiss via select_close and click #after once; click must land on #after immediately.
	say(t, w, map[string]any{"t": "select_close", "id": id1})
	click(t, w, 50, 138)
	after1 := nextDeadline(t, r, w, 5*time.Second, "#after click immediately after select_close", isText("page", func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["title"]), "|after=1|")
	}))
	if !strings.Contains(fmt.Sprint(after1.json["title"]), "|leak=undefined") {
		t.Fatalf("select bridge leaked into page main world: %v", after1.json["title"])
	}

	// 4. Open again, send stale/disabled/out-of-bounds choices (all ignored), then choose Banana (index 6).
	click(t, w, 100, 78)
	open2 := nextDeadline(t, r, w, 5*time.Second, "second select_open", isText("select_open", nil))
	id2 := uint32(open2.json["id"].(float64))
	if id2 <= id1 {
		t.Fatalf("select_open id must increase: id2=%d <= id1=%d", id2, id1)
	}
	say(t, w, map[string]any{"t": "select_choose", "id": id1, "index": 6})   // stale id
	say(t, w, map[string]any{"t": "select_choose", "id": id2, "index": 1})   // disabled option
	say(t, w, map[string]any{"t": "select_choose", "id": id2, "index": 4})   // disabled optgroup
	say(t, w, map[string]any{"t": "select_choose", "id": id2, "index": 999}) // out of bounds
	say(t, w, map[string]any{"t": "select_choose", "id": id2, "index": 6})   // valid: Banana
	chosen := nextDeadline(t, r, w, 5*time.Second, "select_choose change", isText("page", func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["title"]), "val=banana|ev=in:b,ch:b|")
	}))
	if !strings.Contains(fmt.Sprint(chosen.json["title"]), "|act=fruit|") {
		t.Fatalf("focus did not stay on select after select_choose: %v", chosen.json["title"])
	}

	// 5. Keyboard: Tab from #before to #fruit, open with Space, choose SAME item (index 6) -> no extra input/change.
	click(t, w, 50, 25)
	say(t, w, map[string]any{"t": "key", "type": "down", "key": "Tab", "code": "Tab", "key_code": 9})
	say(t, w, map[string]any{"t": "key", "type": "up", "key": "Tab", "code": "Tab", "key_code": 9})
	nextDeadline(t, r, w, 5*time.Second, "focus via Tab", isText("page", func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["title"]), "|act=fruit|")
	}))
	say(t, w, map[string]any{"t": "key", "type": "down", "key": " ", "code": "Space", "key_code": 32, "text": " "})
	say(t, w, map[string]any{"t": "key", "type": "up", "key": " ", "code": "Space", "key_code": 32})
	open3 := nextDeadline(t, r, w, 5*time.Second, "select_open on Space", isText("select_open", nil))
	id3 := uint32(open3.json["id"].(float64))
	if id3 <= id2 || int(open3.json["selected"].(float64)) != 6 {
		t.Fatalf("Space open: id=%d selected=%v", id3, open3.json["selected"])
	}
	say(t, w, map[string]any{"t": "select_choose", "id": id3, "index": 6})
	click(t, w, 50, 138)
	sameCheck := nextDeadline(t, r, w, 5*time.Second, "#after click after same-option choose", isText("page", func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["title"]), "|after=2|")
	}))
	if !strings.Contains(fmt.Sprint(sameCheck.json["title"]), "val=banana|ev=in:b,ch:b|") {
		t.Fatalf("choosing same option fired unexpected events: %v", sameCheck.json["title"])
	}

	// 6. Keyboard Alt+ArrowDown opens select, and blurring withdraws it with select_close.
	click(t, w, 50, 25)
	say(t, w, map[string]any{"t": "key", "type": "down", "key": "Tab", "code": "Tab", "key_code": 9})
	say(t, w, map[string]any{"t": "key", "type": "up", "key": "Tab", "code": "Tab", "key_code": 9})
	nextDeadline(t, r, w, 5*time.Second, "focus via Tab 2", isText("page", func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["title"]), "|act=fruit|")
	}))
	say(t, w, map[string]any{"t": "key", "type": "down", "key": "ArrowDown", "code": "ArrowDown", "key_code": 40, "mods": 1})
	say(t, w, map[string]any{"t": "key", "type": "up", "key": "ArrowDown", "code": "ArrowDown", "key_code": 40, "mods": 1})
	open4 := nextDeadline(t, r, w, 5*time.Second, "select_open on Alt+ArrowDown", isText("select_open", nil))
	id4 := uint32(open4.json["id"].(float64))
	if id4 <= id3 {
		t.Fatalf("Alt+ArrowDown open id=%d <= %d", id4, id3)
	}
	// Clicking #after blurs #fruit -> node must emit select_close {id: id4} AND #after receives the click.
	click(t, w, 50, 138)
	var closed4, after3 bool
	nextDeadline(t, r, w, 5*time.Second, "select_close on blur and #after click", func(m liveMsg) bool {
		if m.kind == preview.Text && m.json["t"] == "select_close" && uint32(m.json["id"].(float64)) == id4 {
			closed4 = true
		}
		if m.kind == preview.Text && m.json["t"] == "page" && strings.Contains(fmt.Sprint(m.json["title"]), "|after=3|") {
			after3 = true
		}
		return closed4 && after3
	})

	// 7. multiple and size>1 selects must NOT be intercepted.
	click(t, w, 50, 200)  // #multi
	click(t, w, 200, 200) // #listbox
	nextDeadline(t, r, w, 5*time.Second, "listbox focus without select_open", func(m liveMsg) bool {
		if m.kind == preview.Text && m.json["t"] == "select_open" {
			t.Fatalf("multiple/listbox select was intercepted: %v", m.json)
		}
		return m.kind == preview.Text && m.json["t"] == "page" && strings.HasPrefix(fmt.Sprint(m.json["title"]), "listbox|")
	})

	// 8. Phone mode (emulated touch): reload keeps bridge via addScriptToEvaluateOnNewDocument, tap opens,
	// select_choose changes value, subsequent tap hits #after directly, and resize/navigation withdraw open lists.
	say(t, w, map[string]any{"t": "size", "w": 390, "h": 600, "dpr": 2, "mobile": true})
	nextDeadline(t, r, w, 10*time.Second, "phone reload ready", isText("page", func(m map[string]any) bool {
		return strings.HasPrefix(fmt.Sprint(m["title"]), "ready|")
	}))
	click(t, w, 100, 78)
	open5 := nextDeadline(t, r, w, 5*time.Second, "select_open in phone mode", isText("select_open", nil))
	id5 := uint32(open5.json["id"].(float64))
	if id5 <= id4 {
		t.Fatalf("phone open id=%d <= %d", id5, id4)
	}
	say(t, w, map[string]any{"t": "select_choose", "id": id5, "index": 6})
	nextDeadline(t, r, w, 5*time.Second, "phone select_choose change", isText("page", func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["title"]), "val=banana|ev=in:b,ch:b|")
	}))
	click(t, w, 50, 138)
	nextDeadline(t, r, w, 5*time.Second, "phone #after tap lands immediately", isText("page", func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["title"]), "|after=1|")
	}))

	// Resize withdraws an open select with select_close.
	click(t, w, 100, 78)
	open6 := nextDeadline(t, r, w, 5*time.Second, "select_open before resize", isText("select_open", nil))
	id6 := uint32(open6.json["id"].(float64))
	if id6 <= id5 {
		t.Fatalf("open6 id=%d <= %d", id6, id5)
	}
	say(t, w, map[string]any{"t": "size", "w": 380, "h": 600, "dpr": 2, "mobile": true})
	nextDeadline(t, r, w, 5*time.Second, "select_close on size change", isText("select_close", func(m map[string]any) bool {
		return uint32(m["id"].(float64)) == id6
	}))

	// Navigation withdraws an open select with select_close.
	click(t, w, 100, 78)
	open7 := nextDeadline(t, r, w, 5*time.Second, "select_open before nav", isText("select_open", nil))
	id7 := uint32(open7.json["id"].(float64))
	if id7 <= id6 {
		t.Fatalf("open7 id=%d <= %d", id7, id6)
	}
	say(t, w, map[string]any{"t": "nav", "url": "/other"})
	nextDeadline(t, r, w, 5*time.Second, "select_close on navigation", isText("select_close", func(m map[string]any) bool {
		return uint32(m["id"].(float64)) == id7
	}))
}
