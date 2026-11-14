package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodesign"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The live-screen video helper, Persona Screen (#3954; docs/extensions/computer-use.md "Live screen video (#3954)").
// Persona Link installs it in its app bundle (Contents/Helpers/Persona Screen.app). The node starts it through
// LaunchServices, so macOS holds its Screen Recording to the helper's own bundle and not to the node, gives it a
// private 0600 Unix socket and a token, and joins the socket to screen.dial's stream once the helper has connected and
// said the token.
//
// With this Mac's node signing setting (nodesign, docs/operations/mac-node-signing.md) the node runs its own copy of the
// helper, ~/.persona/bin/Persona Screen.app, signed with that certificate under the helper's bundle id: its Screen
// Recording grant then survives updates of the helper. Without the setting it runs the helper where Persona Link put
// it; Persona Link builds it the same way each time, so the grant lasts until the helper itself changes.

// advertiseScreenVideo says whether the computer and browser statuses carry screen_video, and whether the node asks
// macOS for the helper's grant (askScreenPermission). On since the official TestFlight build decodes the video (#3954
// slices 3b and 3c); apps before it don't offer the picture switcher. screen.dial works either way.
const advertiseScreenVideo = true

// screenVideoVersion is the wire protocol version this node's helper speaks (PersonaScreenKit's protocolVersion).
const screenVideoVersion = 1

const screenHelperName = "Persona Screen.app"

// screenHelperSource is the helper Persona Link installed, "" when no Persona Link here has one. A variable for tests.
var screenHelperSource = func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, _ := os.UserHomeDir()
	for _, app := range []string{"/Applications/Persona Link.app", filepath.Join(home, "Applications", "Persona Link.app")} {
		p := filepath.Join(app, "Contents", "Helpers", screenHelperName)
		if st, err := os.Stat(filepath.Join(p, "Contents", "MacOS", "PersonaScreen")); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// helperSigner is what the copy needs of a nodesign.Signer.
type helperSigner interface {
	Sign(ctx context.Context, path string) error
	Meets(ctx context.Context, path string) bool
}

// screenSigner is this Mac's node signing setting for the helper (nil without one), signing under bundleID. A
// variable for tests.
var screenSigner = func(bundleID string) (helperSigner, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	c, err := nodesign.Load(nodesign.Path())
	if err != nil || c.Identity == "" {
		return nil, err
	}
	// The certificate the node signs with, under the helper's own bundle id: the node's identifier is the node's.
	s := nodesign.New(nodesign.Config{Identity: c.Identity, Identifier: bundleID}, "")
	if s == nil {
		return nil, nil // a nil *Signer in the interface would not read as nil
	}
	return s, nil
}

// screenCopyDir is where the node keeps its signed copy of the helper (~/.persona/bin). A variable for tests.
var screenCopyDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.DataDir(home), "bin")
}

// screenHelpers resolves which helper to run, once per change of the installed one.
var screenHelpers = &helperResolver{}

type helperResolver struct {
	mu     sync.Mutex
	source string // the installed helper this was resolved for
	stamp  string // its executable's and Info.plist's hash
	path   string // the helper to run
}

// helper is the bundle to start: the signed copy when this Mac signs, else the installed one; "" without a helper.
// Signing happens at most once per version of the installed helper, and a failure falls back to the installed one.
func (r *helperResolver) helper(ctx context.Context, logf func(string, ...any)) string {
	source := screenHelperSource()
	if source == "" {
		return ""
	}
	stamp, err := bundleStamp(source)
	if err != nil {
		return source
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.source == source && r.stamp == stamp && r.path != "" {
		return r.path
	}
	r.source, r.stamp, r.path = source, stamp, source
	bundleID := plistString(ctx, filepath.Join(source, "Contents", "Info.plist"), "CFBundleIdentifier")
	signer, err := screenSigner(bundleID)
	if err != nil {
		logf("screen helper: node signing setting: %v", err)
	}
	if signer == nil || bundleID == "" {
		return source
	}
	if p, err := signedHelperCopy(ctx, signer, source, stamp); err != nil {
		logf("screen helper: signing a copy: %v; running the installed helper", err)
	} else {
		r.path = p
	}
	return r.path
}

// signedHelperCopy is the copy in screenCopyDir signed with signer, made again when the installed helper changed (its
// stamp is kept beside the copy, never inside it, so the seal stays whole). A copy is signed and checked before it
// replaces the one there.
func signedHelperCopy(ctx context.Context, signer helperSigner, source, stamp string) (string, error) {
	dir := screenCopyDir()
	dst := filepath.Join(dir, screenHelperName)
	stampFile := dst + ".source"
	if b, err := os.ReadFile(stampFile); err == nil && strings.TrimSpace(string(b)) == stamp && signer.Meets(ctx, dst) {
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	staged := filepath.Join(dir, "."+screenHelperName+".new")
	_ = os.RemoveAll(staged)
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", source, staged).CombinedOutput(); err != nil {
		return "", fmt.Errorf("copy: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := signer.Sign(ctx, staged); err != nil {
		_ = os.RemoveAll(staged)
		return "", err
	}
	_ = os.RemoveAll(dst)
	if err := os.Rename(staged, dst); err != nil {
		_ = os.RemoveAll(staged)
		return "", err
	}
	if err := os.WriteFile(stampFile, []byte(stamp+"\n"), 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// bundleStamp is a hash of a helper bundle's executable and Info.plist: what changes when Persona Link installs
// another helper.
func bundleStamp(bundle string) (string, error) {
	h := sha256.New()
	for _, f := range []string{filepath.Join("Contents", "MacOS", "PersonaScreen"), filepath.Join("Contents", "Info.plist")} {
		file, err := os.Open(filepath.Join(bundle, f))
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, file)
		_ = file.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// screenLaunch starts the helper bundle through LaunchServices with args, without bringing it forward. A variable for
// tests.
var screenLaunch = func(ctx context.Context, helper string, args ...string) error {
	out, err := exec.CommandContext(ctx, "/usr/bin/open", append([]string{"-g", "-n", helper, "--args"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("open: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// screenPreflight asks the helper whether Screen Recording is granted, without asking macOS (--preflight): "granted",
// "missing", or "unknown". It runs through LaunchServices too, so the answer is about the helper's own grant; it
// answers in a file, read until it holds the line. A variable for tests.
var screenPreflight = func(ctx context.Context, helper string) string {
	dir, err := os.MkdirTemp("", "persona-screen-")
	if err != nil {
		return "unknown"
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "preflight")
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		return "unknown"
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/open", "-g", "-n", "--stdout", out, helper, "--args", "--preflight")
	if err := cmd.Run(); err != nil {
		return "unknown"
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(out)
		if v := parseScreenPreflight(string(b)); v != "" {
			return v
		}
		select {
		case <-ctx.Done():
			return "unknown"
		case <-time.After(50 * time.Millisecond):
		}
	}
	return "unknown"
}

// parseScreenPreflight is the helper's --preflight answer ("screen_recording: granted|missing"), "" before it has written one.
func parseScreenPreflight(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "screen_recording:"); ok {
			switch strings.TrimSpace(v) {
			case "granted":
				return "granted"
			case "missing":
				return "missing"
			}
			return "unknown"
		}
	}
	return ""
}

// screenPermissions keeps the helper's answer for a minute: the statuses are asked often, and a change in System
// Settings shows within that.
var screenPermissions = &permissionCache{ttl: time.Minute}

type permissionCache struct {
	mu     sync.Mutex
	ttl    time.Duration
	helper string
	at     time.Time
	value  string
}

func (c *permissionCache) get(ctx context.Context, helper string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.helper == helper && time.Since(c.at) < c.ttl && c.value != "" {
		return c.value
	}
	c.helper, c.at, c.value = helper, time.Now(), screenPreflight(ctx, helper)
	return c.value
}

// current is the helper to run, as helper says, and the installed helper's version (its stamp; "" when unreadable).
func (r *helperResolver) current(ctx context.Context, logf func(string, ...any)) (string, string) {
	path := r.helper(ctx, logf)
	r.mu.Lock()
	defer r.mu.Unlock()
	if path == "" {
		return "", ""
	}
	return path, r.stamp
}

// screenAsks records which helper version macOS was asked about (#3954). The first time an app that plays the video
// signs in to the live screen while the helper has no Screen Recording grant, the node starts the helper with
// --request-permission, once per helper version. macOS shows its alert on the Mac's own screen, which that app is
// showing over VNC at that moment: the user answers it there or at the Mac, and nothing in Persona does (computer-use
// can't target it, #4336). Once asked, Persona Screen is in System Settings' list; the statuses say so (asked). The
// version is kept beside the node's helper copy, so a restart doesn't ask again.
var screenAsks = &askRecord{file: func() string { return filepath.Join(screenCopyDir(), "Persona Screen.asked") }}

type askRecord struct {
	mu   sync.Mutex
	file func() string
}

// asked says whether macOS was asked for this helper version.
func (a *askRecord) asked(stamp string) bool {
	if stamp == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := os.ReadFile(a.file())
	return err == nil && strings.TrimSpace(string(b)) == stamp
}

// claim records this helper version as asked, and says whether the caller is the one to ask: false when it was asked
// already, or when the record can't be written (no asking that can't be remembered).
func (a *askRecord) claim(stamp string) bool {
	if stamp == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.file()
	if b, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(b)) == stamp {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return false
	}
	return os.WriteFile(f, []byte(stamp+"\n"), 0o600) == nil
}

// screenAskWait is how long askScreenPermission waits for the screen-sharing stream to sign in, and screenAskPoll how
// often it looks. Variables for tests.
var (
	screenAskWait = 30 * time.Second
	screenAskPoll = 250 * time.Millisecond
)

// askScreenPermission is the ask above, for the 5900 stream carrying pair (only an app that plays the video sends one):
// once that stream signs in, if the helper has no grant and macOS wasn't asked for this version, the helper asks. It
// returns at once; the waiting and asking happen on their own goroutine, and done (tests) runs when that ends.
func (n *Node) askScreenPermission(pair string, done func()) {
	if !advertiseScreenVideo || pair == "" {
		if done != nil {
			done()
		}
		return
	}
	logf := func(string, ...any) {}
	if n.logger != nil {
		logf = n.logger.Printf
	}
	go func() {
		if done != nil {
			defer done()
		}
		deadline := time.Now().Add(screenAskWait)
		for !n.screens.signedIn(pair) {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(screenAskPoll)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		helper, stamp := screenHelpers.current(ctx, logf)
		if helper == "" || screenPermissions.get(ctx, helper) != "missing" || !screenAsks.claim(stamp) {
			return
		}
		if err := screenLaunch(ctx, helper, "--request-permission"); err != nil {
			logf("screen helper: asking macOS for Screen Recording: %v", err)
		}
	}()
}

// screenVideoStatus is screen_video for the computer and browser statuses: nil while advertiseScreenVideo is off, on
// a computer that isn't a Mac, or without the helper.
func screenVideoStatus(ctx context.Context) *proto.ScreenVideo {
	return screenVideoAdvertised(ctx, advertiseScreenVideo)
}

func screenVideoAdvertised(ctx context.Context, advertise bool) *proto.ScreenVideo {
	if !advertise {
		return nil
	}
	helper, stamp := screenHelpers.current(ctx, func(string, ...any) {})
	return screenVideoReport(ctx, helper, stamp)
}

// screenVideoReport is what a Mac with this helper (of version stamp) can do for the video channel, nil without one.
func screenVideoReport(ctx context.Context, helper, stamp string) *proto.ScreenVideo {
	if helper == "" {
		return nil
	}
	v := &proto.ScreenVideo{Version: screenVideoVersion, Codecs: []string{"hevc", "h264"}, Permission: screenPermissions.get(ctx, helper)}
	if v.Permission == "missing" {
		v.Asked = screenAsks.asked(stamp)
	}
	return v
}

// screenHelperWait bounds the helper's start: launched, connected and token said. A variable for tests.
var screenHelperWait = 10 * time.Second

// dialScreenHelper starts the helper on a private 0600 Unix socket and returns its connection once the helper has
// connected and said the token. The socket file goes as soon as one connection is in.
func dialScreenHelper(ctx context.Context, helper string) (net.Conn, error) {
	dir, err := os.MkdirTemp("", "persona-screen-") // the user's own temporary directory, 0700
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	if err := os.Chmod(sock, 0o600); err != nil {
		return nil, err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw[:])
	if err := screenLaunch(ctx, helper, "--socket", sock, "--token", token); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(screenHelperWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = l.(*net.UnixListener).SetDeadline(deadline)
	c, err := l.Accept()
	if err != nil {
		return nil, fmt.Errorf("the helper did not connect: %w", err)
	}
	_ = c.SetReadDeadline(deadline)
	// The first line is the token. Read a byte at a time: nothing after the line may be taken from the stream.
	got, err := readTokenLine(c)
	if err != nil || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		_ = c.Close()
		if err == nil {
			err = errors.New("wrong token")
		}
		return nil, fmt.Errorf("the helper's first line: %w", err)
	}
	_ = c.SetReadDeadline(time.Time{})
	return c, nil
}

// readTokenLine reads the first line unbuffered: a buffered reader would take the stream's first bytes with it.
func readTokenLine(r io.Reader) (string, error) {
	var b strings.Builder
	one := make([]byte, 1)
	for b.Len() <= 256 {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return b.String(), nil
		}
		b.WriteByte(one[0])
	}
	return "", errors.New("too long")
}

// startScreenHelper is screenStart on a Mac: the helper started for this stream and joined to it. The stream ends when
// the helper does, when the phone or hub closes it, or when the paired screen-sharing stream closes.
func (n *Node) startScreenHelper(ctx context.Context, id, pair string, keepAwake, link bool, done func()) (bool, error) {
	logf := func(string, ...any) {}
	if n.logger != nil {
		logf = n.logger.Printf
	}
	helper := screenHelpers.helper(ctx, logf)
	if helper == "" {
		return false, &rpc.Error{Code: "video_unavailable", Message: "Persona Screen isn't installed on this Mac (it comes with Persona Link)"}
	}
	if screenPermissions.get(ctx, helper) == "missing" {
		return false, &rpc.Error{Code: "video_unavailable", Message: "Persona Screen has no Screen Recording permission on this Mac"}
	}
	conn, err := dialScreenHelper(ctx, helper)
	if err != nil {
		logf("screen helper: %v", err)
		return false, &rpc.Error{Code: "video_unavailable", Message: "Persona Screen did not start: " + err.Error()}
	}
	if err := n.streams.Open(id); err != nil {
		_ = conn.Close()
		return false, err // screenDial releases the slot
	}
	// From here closing c releases the slot (done), so no error is returned: screenDial would release it again.
	var c net.Conn = &helperConn{Conn: conn, done: done}
	if keepAwake {
		c = &watchedConn{Conn: c, done: awake.watch()}
	}
	if !n.screens.attach(pair, c) { // the screen-sharing stream closed while the helper started
		_ = c.Close()
		n.streams.Abort(id, errors.New("the screen-sharing stream closed"))
		return false, nil
	}
	if link {
		// The video joins the pair's screen session as a channel (#6398); the session closes c when it ends.
		if s := n.links.Get(pair); s != nil {
			if relay, ok := s.Video(c); ok {
				n.streams.Start(id, relay)
				return true, nil
			}
		}
	}
	n.streams.Start(id, c)
	return false, nil
}

// helperConn is the helper's connection: closing it (whoever does) releases the stream's slot once.
type helperConn struct {
	net.Conn
	done func()
	once sync.Once
}

func (c *helperConn) Close() error {
	c.once.Do(func() {
		if c.done != nil {
			c.done()
		}
	})
	return c.Conn.Close()
}
