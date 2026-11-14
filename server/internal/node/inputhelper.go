package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/nwverse/persona/server/internal/proto"
)

// Persona Input, the live screen's pixel scrolling helper (#7775; docs/extensions/computer-use.md "Pixel scrolling").
// Persona Link installs it beside Persona Screen (Contents/Helpers/Persona Input.app). The node starts it through
// LaunchServices for a paired stream whose phone said hello, so macOS holds its Accessibility permission (posting
// events) to the helper's own bundle, gives it a private 0600 Unix socket and a token, and hands it the stream's scroll
// records. It is apart from Persona Screen's code here as on the Mac: nothing in screenhelper.go changes for it.
// With this Mac's node signing setting the node runs its own signed copy (~/.persona/bin/Persona Input.app), as for
// Persona Screen.

// advertiseScreenInput says whether the computer and browser statuses carry screen_input. A phone sends scroll records
// only to a node that does (the node takes them out of the RFB stream; one before this never sees them).
const advertiseScreenInput = true

// screenInputVersion is the record's version this node reads (PersonaInputKit's ScrollRecord.version).
const screenInputVersion = 1

const (
	inputHelperName = "Persona Input.app"
	inputHelperExec = "PersonaInput"
	// inputHelpersMost is how many helpers run at once (one a live screen; as many as video streams).
	inputHelpersMost = 2
)

// inputHelperSource is the helper Persona Link installed, "" when no Persona Link here has one. A variable for tests.
var inputHelperSource = func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, _ := os.UserHomeDir()
	for _, app := range []string{"/Applications/Persona Link.app", filepath.Join(home, "Applications", "Persona Link.app")} {
		p := filepath.Join(app, "Contents", "Helpers", inputHelperName)
		if st, err := os.Stat(filepath.Join(p, "Contents", "MacOS", inputHelperExec)); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// inputHelpers resolves which helper to run, once per change of the installed one.
var inputHelpers = &inputResolver{}

type inputResolver struct {
	mu     sync.Mutex
	source string
	stamp  string
	path   string
}

// current is the helper to run (the signed copy when this Mac signs, else the installed one; "" without one) and the
// installed helper's version (its stamp).
func (r *inputResolver) current(ctx context.Context, logf func(string, ...any)) (string, string) {
	source := inputHelperSource()
	if source == "" {
		return "", ""
	}
	stamp, err := inputStamp(source)
	if err != nil {
		return source, ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.source == source && r.stamp == stamp && r.path != "" {
		return r.path, r.stamp
	}
	r.source, r.stamp, r.path = source, stamp, source
	bundleID := plistString(ctx, filepath.Join(source, "Contents", "Info.plist"), "CFBundleIdentifier")
	signer, err := screenSigner(bundleID)
	if err != nil {
		logf("input helper: node signing setting: %v", err)
	}
	if signer == nil || bundleID == "" {
		return r.path, r.stamp
	}
	if p, err := signedInputCopy(ctx, signer, source, stamp); err != nil {
		logf("input helper: signing a copy: %v; running the installed helper", err)
	} else {
		r.path = p
	}
	return r.path, r.stamp
}

// inputStamp is a hash of the helper bundle's executable and Info.plist: what changes when Persona Link installs another.
func inputStamp(bundle string) (string, error) {
	h := sha256.New()
	for _, f := range []string{filepath.Join("Contents", "MacOS", inputHelperExec), filepath.Join("Contents", "Info.plist")} {
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

// signedInputCopy is the copy beside the node's other helpers signed with signer, made again when the installed one
// changed (its stamp kept beside it, never inside, so the seal stays whole).
func signedInputCopy(ctx context.Context, signer helperSigner, source, stamp string) (string, error) {
	dir := screenCopyDir()
	dst := filepath.Join(dir, inputHelperName)
	stampFile := dst + ".source"
	if b, err := os.ReadFile(stampFile); err == nil && strings.TrimSpace(string(b)) == stamp && signer.Meets(ctx, dst) {
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	staged := filepath.Join(dir, "."+inputHelperName+".new")
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
	return dst, os.WriteFile(stampFile, []byte(stamp+"\n"), 0o644)
}

// inputPreflight asks the helper whether it may post events, without asking macOS (--preflight): "granted", "missing"
// or "unknown". Through LaunchServices, so the answer is about the helper's own permission. A variable for tests.
var inputPreflight = func(ctx context.Context, helper string) string {
	dir, err := os.MkdirTemp("", "persona-input-")
	if err != nil {
		return "unknown"
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "preflight")
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		return "unknown"
	}
	if err := exec.CommandContext(ctx, "/usr/bin/open", "-g", "-n", "--stdout", out, helper, "--args", "--preflight").Run(); err != nil {
		return "unknown"
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		b, _ := os.ReadFile(out)
		if v := parseInputPreflight(string(b)); v != "" {
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

// parseInputPreflight is the helper's --preflight answer ("post_event: granted|missing"), "" before it wrote one.
func parseInputPreflight(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "post_event:"); ok {
			switch strings.TrimSpace(v) {
			case "granted", "missing":
				return strings.TrimSpace(v)
			}
			return "unknown"
		}
	}
	return ""
}

// inputPermissions keeps the helper's answer for a minute, as for Persona Screen; forget drops it (the helper said
// otherwise when it started).
var inputPermissions = &inputPermissionCache{ttl: time.Minute}

type inputPermissionCache struct {
	mu     sync.Mutex
	ttl    time.Duration
	helper string
	at     time.Time
	value  string
}

func (c *inputPermissionCache) get(ctx context.Context, helper string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.helper == helper && time.Since(c.at) < c.ttl && c.value != "" {
		return c.value
	}
	c.helper, c.at, c.value = helper, time.Now(), inputPreflight(ctx, helper)
	return c.value
}

func (c *inputPermissionCache) forget() {
	c.mu.Lock()
	c.value = ""
	c.mu.Unlock()
}

// inputAsks records which helper version macOS was asked about: the first time a phone that scrolls by pixels says hello
// while the helper may not post events, the node starts it with --request-permission, once per version. macOS shows its
// window on the Mac's own screen, which that phone is showing at that moment; only the user answers it (nothing in
// Persona does, #4336).
var inputAsks = &askRecord{file: func() string { return filepath.Join(screenCopyDir(), "Persona Input.asked") }}

// screenInputStatus is screen_input for the computer and browser statuses: nil while advertiseScreenInput is off, on a
// computer that isn't a Mac, or without the helper.
func screenInputStatus(ctx context.Context) *proto.ScreenInput {
	if !advertiseScreenInput {
		return nil
	}
	helper, stamp := inputHelpers.current(ctx, func(string, ...any) {})
	if helper == "" {
		return nil
	}
	s := &proto.ScreenInput{Version: screenInputVersion, Permission: inputPermissions.get(ctx, helper)}
	if s.Permission == "missing" {
		s.Asked = inputAsks.asked(stamp)
	}
	return s
}

// screenInputs runs the helpers for the paired streams: one session a stream, at most inputHelpersMost helpers.
type screenInputs struct {
	logf    func(string, ...any)
	signed  func(pair string) bool // the pair's RFB stream signed in (screenLogins.signedIn)
	mu      sync.Mutex
	running int
}

// wrap reads a paired 5900 connection for scroll records (rfbInputConn), its session starting the helper on hello.
func (s *screenInputs) wrap(pair string, c net.Conn) net.Conn {
	if pair == "" {
		return c
	}
	return newRFBInputConn(c, &inputSession{inputs: s, pair: pair})
}

func (s *screenInputs) take() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running >= inputHelpersMost {
		return false
	}
	s.running++
	return true
}

func (s *screenInputs) release() {
	s.mu.Lock()
	if s.running > 0 {
		s.running--
	}
	s.mu.Unlock()
}

// inputStart starts the helper on its socket and returns it once it said the token and whether it may post events.
// A variable for tests.
var inputStart = func(ctx context.Context, helper string) (net.Conn, bool, error) {
	c, err := dialScreenHelper(ctx, helper) // the same socket, token and wait as Persona Screen's
	if err != nil {
		return nil, false, err
	}
	_ = c.SetReadDeadline(time.Now().Add(screenHelperWait))
	line, err := readTokenLine(c)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return nil, false, fmt.Errorf("the helper's second line: %w", err)
	}
	return c, line == "granted", nil
}

// inputSession is one stream's: its helper once started, records to it through a queue (the relay never waits on it).
type inputSession struct {
	inputs *screenInputs
	pair   string

	mu      sync.Mutex
	started bool // hello came and something was done about it
	queue   chan []byte
	conn    net.Conn
	closed  bool
}

// inputQueue is how many records may wait for the helper: a few frames. More, and the helper is stuck: notches.
const inputQueue = 32

func (s *inputSession) hello() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.start()
}

// start is hello's work: the helper started if it may post events, else macOS asked once for this helper version.
func (s *inputSession) start() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	logf := s.inputs.logf
	if !s.inputs.signed(s.pair) {
		return
	}
	helper, stamp := inputHelpers.current(ctx, logf)
	if helper == "" {
		return
	}
	switch inputPermissions.get(ctx, helper) {
	case "granted":
	case "missing":
		if inputAsks.claim(stamp) {
			if err := screenLaunch(ctx, helper, "--request-permission"); err != nil {
				logf("input helper: asking macOS for Accessibility: %v", err)
			}
		}
		return
	default:
		return
	}
	if !s.inputs.take() {
		return
	}
	conn, granted, err := inputStart(ctx, helper)
	if err != nil || !granted {
		s.inputs.release()
		if err != nil {
			logf("input helper: %v", err)
		} else {
			inputPermissions.forget() // it says it may not: the next status asks again
		}
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		s.inputs.release()
		return
	}
	s.conn, s.queue = conn, make(chan []byte, inputQueue)
	queue := s.queue
	s.mu.Unlock()
	go s.send(conn, queue)
}

// send writes the queued records to the helper until the queue closes or the helper goes.
func (s *inputSession) send(conn net.Conn, queue chan []byte) {
	for raw := range queue {
		if _, err := conn.Write(raw); err != nil {
			s.close()
			return
		}
	}
}

func (s *inputSession) scroll(raw []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queue == nil || s.closed {
		return false
	}
	select {
	case s.queue <- append([]byte(nil), raw...):
		return true
	default:
		return false
	}
}

func (s *inputSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conn, queue := s.conn, s.queue
	s.conn, s.queue = nil, nil
	s.mu.Unlock()
	if queue != nil {
		close(queue)
	}
	if conn != nil {
		_ = conn.Close() // the helper ends with its socket
		s.inputs.release()
	}
}
