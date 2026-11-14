package node

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeInputBundle makes a minimal Persona Input.app in dir: its executable and Info.plist, as Persona Link installs.
func fakeInputBundle(t *testing.T, dir, bundleID, code string) string {
	t.Helper()
	app := filepath.Join(dir, inputHelperName)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", inputHelperExec), []byte(code), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + bundleID + `</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	return app
}

// inputWorld is a Mac with Persona Input installed whose permission is `permission`: what was launched (asks), how
// often the helper was started, and the helpers' ends of their sockets.
type inputWorld struct {
	inputs   *screenInputs
	app      string
	mu       sync.Mutex
	launched [][]string
	starts   atomic.Int32
	helpers  []net.Conn
	says     bool // what a started helper says: granted (true) or missing
	signed   atomic.Bool
}

func newInputWorld(t *testing.T, permission string) *inputWorld {
	t.Helper()
	w := &inputWorld{says: true}
	w.signed.Store(true)
	bin := t.TempDir()
	w.app = fakeInputBundle(t, t.TempDir(), "com.example.persona.link.input", "v1")
	swap(t, &screenCopyDir, func() string { return bin })
	swap(t, &inputHelperSource, func() string { w.mu.Lock(); defer w.mu.Unlock(); return w.app })
	swap(t, &inputHelpers, &inputResolver{})
	swap(t, &inputAsks, &askRecord{file: func() string { return filepath.Join(bin, "Persona Input.asked") }})
	swap(t, &screenSigner, func(string) (helperSigner, error) { return nil, nil })
	swap(t, &inputPermissions, &inputPermissionCache{ttl: time.Minute})
	swap(t, &inputPreflight, func(context.Context, string) string { return permission })
	swap(t, &screenLaunch, func(_ context.Context, helper string, args ...string) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.launched = append(w.launched, append([]string{filepath.Base(helper)}, args...))
		return nil
	})
	swap(t, &inputStart, func(context.Context, string) (net.Conn, bool, error) {
		w.starts.Add(1)
		node, helper := net.Pipe()
		w.mu.Lock()
		w.helpers = append(w.helpers, helper)
		says := w.says
		w.mu.Unlock()
		if !says {
			_ = node.Close()
			return nil, false, nil
		}
		return node, true, nil
	})
	w.inputs = &screenInputs{logf: t.Logf, signed: func(string) bool { return w.signed.Load() }}
	return w
}

// count is how many helpers run now.
func (s *screenInputs) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (w *inputWorld) asks() [][]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]string(nil), w.launched...)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

// With the permission, hello starts the helper once; the stream's records reach it as they came, in order; the
// stream's end ends it and frees its place.
func TestHelloStartsPersonaInputAndRecordsReachIt(t *testing.T) {
	w := newInputWorld(t, "granted")
	s := &inputSession{inputs: w.inputs, pair: "K"}
	first := record(inputKindScroll, 1, 0, -3, 0)
	if s.scroll(first) {
		t.Fatal("a record before hello went to no helper and must fall back")
	}
	s.hello()
	eventually(t, "the helper takes records", func() bool { return s.scroll(first) })
	s.hello() // again: nothing more
	second := record(inputKindScroll, 2, 0, -4.5, 1)
	if !s.scroll(second) {
		t.Fatal("the second record was not taken")
	}
	w.mu.Lock()
	helper := w.helpers[0]
	w.mu.Unlock()
	got := make([]byte, 2*inputRecordSize)
	if _, err := io.ReadFull(helper, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(first)+string(second) {
		t.Fatalf("the helper got %x", got)
	}
	if n := w.starts.Load(); n != 1 {
		t.Fatalf("started %d times, want once", n)
	}
	s.close()
	if _, err := helper.Read(make([]byte, 1)); err == nil {
		t.Fatal("the helper's socket is still open after the stream ended")
	}
	if s.scroll(second) {
		t.Fatal("a record after the end was taken")
	}
	if w.inputs.count() != 0 {
		t.Fatalf("%d helpers still counted as running", w.inputs.count())
	}
	if len(w.asks()) != 0 {
		t.Fatalf("asked macOS with the permission there: %v", w.asks())
	}
}

// Without the permission, hello asks macOS once for this helper version (the window shows on the Mac; only the user
// answers it), records fall back to notches, and the statuses say it was asked. A new helper version asks once more.
func TestWithoutThePermissionHelloAsksOncePerVersion(t *testing.T) {
	w := newInputWorld(t, "missing")
	ctx := context.Background()
	if st := screenInputStatus(ctx); st == nil || st.Version != screenInputVersion || st.Permission != "missing" || st.Asked {
		t.Fatalf("before asking: %+v", st)
	}
	s := &inputSession{inputs: w.inputs, pair: "K"}
	s.hello()
	eventually(t, "macOS asked", func() bool { return len(w.asks()) == 1 })
	if got := strings.Join(w.asks()[0], " "); got != inputHelperName+" --request-permission" {
		t.Fatalf("launched %q", got)
	}
	if s.scroll(record(inputKindScroll, 2, 0, 1, 0)) {
		t.Fatal("a record was taken without the permission")
	}
	if st := screenInputStatus(ctx); st == nil || !st.Asked {
		t.Fatalf("after asking: %+v", st)
	}
	other := &inputSession{inputs: w.inputs, pair: "L"}
	other.hello()
	time.Sleep(50 * time.Millisecond)
	if len(w.asks()) != 1 || w.starts.Load() != 0 {
		t.Fatalf("asked %d times and started %d: want one ask, no helper", len(w.asks()), w.starts.Load())
	}

	app := fakeInputBundle(t, t.TempDir(), "com.example.persona.link.input", "v2") // Persona Link updated it
	w.mu.Lock()
	w.app = app
	w.mu.Unlock()
	third := &inputSession{inputs: w.inputs, pair: "M"}
	third.hello()
	eventually(t, "the new version asked once more", func() bool { return len(w.asks()) == 2 })
}

// Nothing before the stream signed in to Screen Sharing; a helper that says it may not post is let go, and the next
// status asks it again; never more than inputHelpersMost helpers at once.
func TestPersonaInputOnlyForASignedInStreamAndWithinItsCount(t *testing.T) {
	w := newInputWorld(t, "granted")
	w.signed.Store(false)
	s := &inputSession{inputs: w.inputs, pair: "K"}
	s.hello()
	time.Sleep(50 * time.Millisecond)
	if w.starts.Load() != 0 || len(w.asks()) != 0 {
		t.Fatal("a stream that hadn't signed in started or asked for something")
	}

	w.signed.Store(true)
	w.mu.Lock()
	w.says = false
	w.mu.Unlock()
	var preflights atomic.Int32
	swap(t, &inputPreflight, func(context.Context, string) string { preflights.Add(1); return "granted" })
	s = &inputSession{inputs: w.inputs, pair: "K"}
	s.hello()
	eventually(t, "the helper started", func() bool { return w.starts.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	if s.scroll(record(inputKindScroll, 2, 0, 1, 0)) || w.inputs.count() != 0 {
		t.Fatalf("a helper that said missing took a record, or still counts (%d)", w.inputs.count())
	}
	before := preflights.Load()
	_ = screenInputStatus(context.Background())
	if preflights.Load() != before+1 {
		t.Fatal("the cached answer was kept after the helper said otherwise")
	}

	w.mu.Lock()
	w.says = true
	w.mu.Unlock()
	var sessions []*inputSession
	for _, pair := range []string{"A", "B", "C"} {
		s := &inputSession{inputs: w.inputs, pair: pair}
		s.hello()
		sessions = append(sessions, s)
	}
	eventually(t, "two helpers", func() bool { return w.starts.Load() == 3 })
	time.Sleep(50 * time.Millisecond)
	if n := w.starts.Load(); n != 3 || w.inputs.count() != inputHelpersMost {
		t.Fatalf("started %d (one earlier), %d running; want two more, %d running", n, w.inputs.count(), inputHelpersMost)
	}
	for _, s := range sessions {
		s.close()
	}
	if w.inputs.count() != 0 {
		t.Fatalf("%d still running", w.inputs.count())
	}
}

func TestParseInputPreflight(t *testing.T) {
	for out, want := range map[string]string{
		"": "", "post_event: granted\n": "granted", "post_event: missing": "missing", "noise\npost_event: what\n": "unknown",
	} {
		if got := parseInputPreflight(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
	swap(t, &inputHelperSource, func() string { return "" })
	swap(t, &inputHelpers, &inputResolver{})
	if st := screenInputStatus(context.Background()); st != nil {
		t.Fatalf("no helper, yet a status: %+v", st)
	}
}

// With this Mac's node signing setting the node runs its own signed copy, made again when Persona Link installs
// another helper, never signing the installed one in place.
func TestTheSignedInputCopy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("copies with ditto and reads the bundle id with plutil, both macOS")
	}
	app := fakeInputBundle(t, t.TempDir(), "com.example.persona.link.input", "v1")
	copies := t.TempDir()
	signer := &fakeSigner{}
	var signedAs string
	swap(t, &inputHelperSource, func() string { return app })
	swap(t, &screenCopyDir, func() string { return copies })
	swap(t, &screenSigner, func(id string) (helperSigner, error) { signedAs = id; return signer, nil })
	r := &inputResolver{}
	ctx := context.Background()
	logf := func(string, ...any) {}
	want := filepath.Join(copies, inputHelperName)
	if got, _ := r.current(ctx, logf); got != want || signedAs != "com.example.persona.link.input" {
		t.Fatalf("helper %q signed as %q; want the signed copy under the helper's own id", got, signedAs)
	}
	if got, _ := (&inputResolver{}).current(ctx, logf); got != want || signer.signs.Load() != 1 {
		t.Fatalf("after a restart: %q, signed %d times", got, signer.signs.Load())
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", inputHelperExec), []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.current(ctx, logf); got != want || signer.signs.Load() != 2 {
		t.Fatalf("after an update: %q, signed %d times", got, signer.signs.Load())
	}
	signer.fail = errors.New("errSecInternalComponent")
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", inputHelperExec), []byte("v3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.current(ctx, logf); got != app {
		t.Fatalf("signing failed: %q, want the installed helper", got)
	}
}

// wrap reads only paired streams; the record path end to end: a signed-in paired stream, hello, then a scroll that
// reaches the helper and never Screen Sharing.
func TestAPairedStreamsScrollsReachTheHelper(t *testing.T) {
	w := newInputWorld(t, "granted")
	plain, _ := net.Pipe()
	if got := w.inputs.wrap("", plain); got != plain {
		t.Fatal("an unpaired stream was wrapped")
	}
	s := &screenSide{}
	c := w.inputs.wrap("K", s).(*rfbInputConn)
	signIn(t, c, s, rfbSecAppleDH, 128)
	_, _ = c.Write(record(inputKindHello, 0, 0, 0, 0))
	eventually(t, "the helper started", func() bool { return w.inputs.count() == 1 })
	rec := record(inputKindScroll, 1, 0, -7, 0)
	_, _ = c.Write(rec)
	w.mu.Lock()
	helper := w.helpers[0]
	w.mu.Unlock()
	got := make([]byte, inputRecordSize)
	if _, err := io.ReadFull(helper, got); err != nil || string(got) != string(rec) {
		t.Fatalf("the helper got %x, %v", got, err)
	}
	if len(s.got()) != 0 {
		t.Fatalf("Screen Sharing got %x", s.got())
	}
	_ = c.Close()
	eventually(t, "the helper let go", func() bool { return w.inputs.count() == 0 })
}
