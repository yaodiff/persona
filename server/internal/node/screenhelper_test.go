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

	"github.com/nwverse/persona/server/internal/streams"
)

// fakeHelperBundle makes a minimal Persona Screen.app in dir: its executable and Info.plist, as Persona Link installs.
func fakeHelperBundle(t *testing.T, dir, bundleID, code string) string {
	t.Helper()
	app := filepath.Join(dir, screenHelperName)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "PersonaScreen"), []byte(code), 0o755); err != nil {
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

// fakeHelper stands in for LaunchServices starting Persona Screen: it connects to the socket it was given and says
// `say` as its first line, then `then`. It checks the socket is the node's alone.
func fakeHelper(t *testing.T, say func(token string) string, then string) func(context.Context, string, ...string) error {
	return func(_ context.Context, _ string, args ...string) error {
		var sock, token string
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "--socket":
				sock = args[i+1]
			case "--token":
				token = args[i+1]
			}
		}
		if st, err := os.Stat(sock); err != nil {
			t.Errorf("socket %s: %v", sock, err)
		} else if st.Mode().Perm() != 0o600 {
			t.Errorf("socket mode %v, want 0600", st.Mode().Perm())
		}
		if st, err := os.Stat(filepath.Dir(sock)); err != nil {
			t.Errorf("socket directory: %v", err)
		} else if st.Mode().Perm() != 0o700 {
			t.Errorf("socket directory mode %v, want 0700", st.Mode().Perm())
		}
		go func() {
			c, err := net.Dial("unix", sock)
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, say(token)+"\n"+then)
			// Hold the connection: the node owns it now.
		}()
		return nil
	}
}

func swap[T any](t *testing.T, v *T, to T) {
	t.Helper()
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

func TestParseScreenPreflight(t *testing.T) {
	for out, want := range map[string]string{
		"screen_recording: granted\n": "granted",
		"screen_recording: missing":   "missing",
		"  screen_recording:granted":  "granted",
		"screen_recording: maybe":     "unknown",
		"":                            "", // not written yet
		"something else\n":            "",
	} {
		if got := parseScreenPreflight(out); got != want {
			t.Errorf("parseScreenPreflight(%q) = %q, want %q", out, got, want)
		}
	}
}

func TestDialScreenHelperChecksTheTokenAndKeepsTheStream(t *testing.T) {
	swap(t, &screenLaunch, fakeHelper(t, func(token string) string { return token }, "HELLO"))
	c, err := dialScreenHelper(context.Background(), "/Applications/Persona Link.app/Contents/Helpers/Persona Screen.app")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "HELLO" {
		t.Fatalf("the stream after the token = %q, %v: no byte may be taken with the token line", got, err)
	}
}

func TestDialScreenHelperRefusesAWrongToken(t *testing.T) {
	swap(t, &screenLaunch, fakeHelper(t, func(string) string { return "not-the-token" }, ""))
	if c, err := dialScreenHelper(context.Background(), "helper"); err == nil {
		c.Close()
		t.Fatal("a connection with the wrong token was taken")
	}
}

func TestDialScreenHelperGivesUpWhenTheHelperNeverConnects(t *testing.T) {
	swap(t, &screenHelperWait, 200*time.Millisecond)
	swap(t, &screenLaunch, func(context.Context, string, ...string) error { return nil })
	start := time.Now()
	if _, err := dialScreenHelper(context.Background(), "helper"); err == nil {
		t.Fatal("no helper connected, yet it returned a connection")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("waited %v for a helper that never came", waited)
	}
}

// installedHelper makes the helper Persona Link installs, granted, with no node signing setting.
func installedHelper(t *testing.T, permission string) string {
	app := fakeHelperBundle(t, t.TempDir(), "com.example.persona.link.screen", "v1")
	swap(t, &screenHelperSource, func() string { return app })
	swap(t, &screenSigner, func(string) (helperSigner, error) { return nil, nil })
	swap(t, &screenHelpers, &helperResolver{})
	swap(t, &screenPermissions, &permissionCache{ttl: time.Minute})
	swap(t, &screenPreflight, func(context.Context, string) string { return permission })
	return app
}

func TestStartScreenHelperJoinsTheStreamAndEndsWithScreenSharing(t *testing.T) {
	installedHelper(t, "granted")
	swap(t, &screenLaunch, fakeHelper(t, func(token string) string { return token }, ""))
	streamClosed := make(chan struct{})
	n := &Node{screens: newScreenLogins(), streams: streams.New(func(event string, _ any) error {
		if event == streams.EventClose {
			close(streamClosed)
		}
		return nil
	})}
	// The paired screen-sharing stream, signed in.
	rfb, peer := net.Pipe()
	defer peer.Close()
	tee := n.screens.observe("K", rfb)
	n.screens.mu.Lock()
	n.screens.byID["K"].result = rfbLoginSignedIn
	n.screens.mu.Unlock()

	var released atomic.Int32
	if _, err := n.startScreenHelper(context.Background(), "v1", "K", false, false, func() { released.Add(1) }); err != nil {
		t.Fatal(err)
	}
	n.screens.mu.Lock()
	bound := len(n.screens.byID["K"].video)
	n.screens.mu.Unlock()
	if bound != 1 {
		t.Fatalf("video streams bound to the pair = %d, want 1", bound)
	}

	_ = tee.Close() // screen sharing ends: the video ends with it, and its slot is released once
	select {
	case <-streamClosed: // The stream pump has closed the helper connection again.
	case <-time.After(2 * time.Second):
		t.Fatal("the screen-sharing stream ended without closing the video stream")
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("slot released %d times, want 1", got)
	}
}

func TestStartScreenHelperRefusesWithoutAHelperOrPermission(t *testing.T) {
	n := &Node{screens: newScreenLogins(), streams: streams.New(func(string, any) error { return nil })}
	n.screens.byID["K"] = &screenPair{result: rfbLoginSignedIn, open: true}

	installedHelper(t, "missing")
	if _, err := n.startScreenHelper(context.Background(), "v1", "K", false, false, func() {}); dialErrCode(t, err) != "video_unavailable" {
		t.Fatalf("no Screen Recording permission: %v", err)
	}

	swap(t, &screenHelperSource, func() string { return "" })
	swap(t, &screenHelpers, &helperResolver{})
	if _, err := n.startScreenHelper(context.Background(), "v1", "K", false, false, func() {}); dialErrCode(t, err) != "video_unavailable" {
		t.Fatalf("no helper installed: %v", err)
	}
}

func TestAPairThatClosedBindsNoVideo(t *testing.T) {
	s := newScreenLogins()
	if s.attach("gone", io.NopCloser(strings.NewReader(""))) {
		t.Fatal("bound video to a pair with no screen-sharing stream")
	}
	s.byID["unsigned"] = &screenPair{result: rfbLoginFailed, open: true}
	if s.attach("unsigned", io.NopCloser(strings.NewReader(""))) {
		t.Fatal("bound video to a pair that did not sign in")
	}
}

// screen_video is in the statuses now that the official build decodes video (#3954), from what the helper says; the
// switch still gates it, so a node can be built without it.
func TestScreenVideoIsAdvertised(t *testing.T) {
	if !advertiseScreenVideo {
		t.Fatal("advertiseScreenVideo is off: the official TestFlight build decodes video since #3954 slices 3b and 3c")
	}
	installedHelper(t, "granted")
	ctx := context.Background()
	v := screenVideoStatus(ctx)
	if v == nil || v.Version != screenVideoVersion || strings.Join(v.Codecs, ",") != "hevc,h264" || v.Permission != "granted" || v.Asked {
		t.Fatalf("status, helper granted = %+v", v)
	}
	if v := screenVideoAdvertised(ctx, false); v != nil {
		t.Fatalf("switched off = %+v, want none", v)
	}

	swap(t, &screenHelperSource, func() string { return "" })
	swap(t, &screenHelpers, &helperResolver{})
	if v := screenVideoAdvertised(ctx, true); v != nil {
		t.Fatalf("no helper = %+v, want none", v)
	}
}

// askingFor sets up the ask (#3954): the helper installed with this permission, the record in a temporary bin, and
// launches counted. It returns the node, a pair signed in on it, and the launches so far.
func askingFor(t *testing.T, permission string) (*Node, func() [][]string) {
	t.Helper()
	installedHelper(t, permission)
	bin := t.TempDir()
	swap(t, &screenCopyDir, func() string { return bin })
	swap(t, &screenAsks, &askRecord{file: func() string { return filepath.Join(bin, "Persona Screen.asked") }})
	swap(t, &screenAskPoll, 5*time.Millisecond)
	var mu sync.Mutex
	var launched [][]string
	swap(t, &screenLaunch, func(_ context.Context, helper string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		launched = append(launched, append([]string{filepath.Base(helper)}, args...))
		return nil
	})
	n := &Node{screens: newScreenLogins()}
	rfb, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = rfb.Close() })
	_ = n.screens.observe("K", rfb)
	n.screens.mu.Lock()
	n.screens.byID["K"].result = rfbLoginSignedIn
	n.screens.mu.Unlock()
	return n, func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), launched...)
	}
}

func ask(n *Node, pair string) {
	done := make(chan struct{})
	n.askScreenPermission(pair, func() { close(done) })
	<-done
}

// The first time an app that plays the video signs in without the helper's grant, the helper asks macOS, once per
// helper version; the statuses then say it asked, so the app can point to System Settings.
func TestTheHelperAsksForScreenRecordingOncePerVersion(t *testing.T) {
	n, launched := askingFor(t, "missing")
	ctx := context.Background()
	if v := screenVideoStatus(ctx); v == nil || v.Permission != "missing" || v.Asked {
		t.Fatalf("before asking = %+v, want missing, not asked", v)
	}
	ask(n, "K")
	if got := launched(); len(got) != 1 || strings.Join(got[0], " ") != screenHelperName+" --request-permission" {
		t.Fatalf("launched %v, want the helper once with --request-permission", got)
	}
	if v := screenVideoStatus(ctx); v == nil || !v.Asked {
		t.Fatalf("after asking = %+v, want asked", v)
	}
	ask(n, "K")
	if got := launched(); len(got) != 1 {
		t.Fatalf("asked again for the same helper version: %v", got)
	}

	// A new helper version (Persona Link updated it): its grant is a new one, so it may ask once more.
	app := fakeHelperBundle(t, t.TempDir(), "com.example.persona.link.screen", "v2")
	swap(t, &screenHelperSource, func() string { return app })
	if v := screenVideoStatus(ctx); v == nil || v.Asked {
		t.Fatalf("new version before asking = %+v, want not asked", v)
	}
	ask(n, "K")
	if got := launched(); len(got) != 2 {
		t.Fatalf("launched %v, want a second ask for the new version", got)
	}
}

// It asks only for a stream that signed in, and never when the grant is there.
func TestTheHelperAsksOnlyAfterASignInAndWithoutTheGrant(t *testing.T) {
	n, launched := askingFor(t, "missing")
	swap(t, &screenAskWait, 30*time.Millisecond)
	ask(n, "nobody") // no such stream: gives up
	n.screens.mu.Lock()
	n.screens.byID["K"].result = rfbLoginFailed
	n.screens.mu.Unlock()
	ask(n, "K") // the login failed
	if got := launched(); len(got) != 0 {
		t.Fatalf("asked without a sign-in: %v", got)
	}
	if v := screenVideoStatus(context.Background()); v == nil || v.Asked {
		t.Fatalf("status = %+v, want not asked", v)
	}

	n, launched = askingFor(t, "granted")
	ask(n, "K")
	if got := launched(); len(got) != 0 {
		t.Fatalf("asked with the grant there: %v", got)
	}
}

func TestScreenPermissionIsAskedOncePerMinute(t *testing.T) {
	var asked atomic.Int32
	c := &permissionCache{ttl: time.Minute}
	swap(t, &screenPreflight, func(context.Context, string) string { asked.Add(1); return "granted" })
	for range 5 {
		if c.get(context.Background(), "h") != "granted" {
			t.Fatal("wrong answer")
		}
	}
	if asked.Load() != 1 {
		t.Fatalf("asked the helper %d times in a minute, want 1", asked.Load())
	}
	c.get(context.Background(), "another helper")
	if asked.Load() != 2 {
		t.Fatal("another helper's answer was taken from the cache")
	}
}

// fakeSigner signs by writing a marker into the bundle, as codesign's signature does.
type fakeSigner struct {
	signs atomic.Int32
	fail  error
}

func (f *fakeSigner) Sign(_ context.Context, path string) error {
	f.signs.Add(1)
	if f.fail != nil {
		return f.fail
	}
	return os.WriteFile(filepath.Join(path, "Contents", "signed"), []byte("yes"), 0o644)
}

func (f *fakeSigner) Meets(_ context.Context, path string) bool {
	_, err := os.Stat(filepath.Join(path, "Contents", "signed"))
	return err == nil
}

// With this Mac's node signing setting, the node runs its own signed copy, signs it again only when Persona Link
// installs another helper, and runs the installed one when signing fails.
func TestTheSignedHelperCopy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("copies with ditto and reads the bundle id with plutil, both macOS")
	}
	src := t.TempDir()
	app := fakeHelperBundle(t, src, "com.example.persona.link.screen", "v1")
	copies := t.TempDir()
	signer := &fakeSigner{}
	var signedAs string
	swap(t, &screenHelperSource, func() string { return app })
	swap(t, &screenCopyDir, func() string { return copies })
	swap(t, &screenSigner, func(id string) (helperSigner, error) { signedAs = id; return signer, nil })
	r := &helperResolver{}
	ctx := context.Background()
	logf := func(string, ...any) {}

	want := filepath.Join(copies, screenHelperName)
	if got := r.helper(ctx, logf); got != want {
		t.Fatalf("helper = %q, want the signed copy %q", got, want)
	}
	if signedAs != "com.example.persona.link.screen" {
		t.Fatalf("signed under %q, want the helper's own bundle id", signedAs)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "signed")); err == nil {
		t.Fatal("the installed helper was signed in place: Persona Link's seal would break")
	}
	r2 := &helperResolver{} // a node restart: the copy is still good
	if got := r2.helper(ctx, logf); got != want || signer.signs.Load() != 1 {
		t.Fatalf("after a restart: %q, signed %d times; want the same copy, signed once", got, signer.signs.Load())
	}

	// Persona Link installs another helper: signed again.
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "PersonaScreen"), []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := r.helper(ctx, logf); got != want || signer.signs.Load() != 2 {
		t.Fatalf("after an update: %q, signed %d times; want the copy, signed again", got, signer.signs.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(want, "Contents", "MacOS", "PersonaScreen")); string(b) != "v2" {
		t.Fatalf("the copy runs %q, want the new helper", b)
	}

	// Signing fails (keychain locked, certificate expired): the installed helper runs, the node goes on.
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "PersonaScreen"), []byte("v3"), 0o755); err != nil {
		t.Fatal(err)
	}
	signer.fail = errors.New("errSecInternalComponent")
	if got := r.helper(ctx, logf); got != app {
		t.Fatalf("signing failed: helper = %q, want the installed %q", got, app)
	}
	if _, err := os.Stat(filepath.Join(copies, "."+screenHelperName+".new")); err == nil {
		t.Fatal("a failed copy was left behind")
	}
}
