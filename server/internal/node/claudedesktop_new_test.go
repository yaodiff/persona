package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeDesktopApp stands in for Claude's desktop app and the CLI that writes a new session: seeds records what the CLI
// was asked to write (and writes it, in home's ~/.claude), imports writes the app's index entry for a session it is
// asked to import, and a session it is asked to open gets a claude with inbox.
type fakeDesktopApp struct {
	t     *testing.T
	home  string
	index string // the app data directory its entries go in

	mu       sync.Mutex
	seeds    []fakeSeed
	links    []string
	modes    map[string]string // the mode each session was imported in, read as the app reads it
	inboxes  map[string]*fakeInbox
	noImport bool
}

type fakeSeed struct{ claude, dir, id, title string }

func newFakeDesktopApp(t *testing.T, home string) *fakeDesktopApp {
	t.Helper()
	_, third := useDesktopDirs(t)
	if err := os.MkdirAll(filepath.Join(third, "claude-code-sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &fakeDesktopApp{t: t, home: home, index: third, inboxes: map[string]*fakeInbox{}, modes: map[string]string{}}
	oldSeed, oldOpen := seedDesktopSession, openDesktopLink
	seedDesktopSession = func(_ context.Context, claude, dir, id, title string) error {
		a.mu.Lock()
		a.seeds = append(a.seeds, fakeSeed{claude, dir, id, title})
		a.mu.Unlock()
		writeTranscript(t, home, dir, id, time.Now(),
			fmt.Sprintf(`{"type":"custom-title","customTitle":%q,"sessionId":%q}`, title, id),
			fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":"2026-11-12T09:00:00Z","cwd":%q,"sessionId":%q,"message":{"role":"user","content":"<command-name>/rename</command-name>"}}`, dir, id))
		return nil
	}
	openDesktopLink = func(_ context.Context, link string) error {
		a.mu.Lock()
		a.links = append(a.links, link)
		noImport := a.noImport
		a.mu.Unlock()
		switch {
		case strings.HasPrefix(link, "claude://resume?session="):
			id := strings.TrimPrefix(link, "claude://resume?session=")
			mode := importedDesktopMode(t, findTranscript(t, home, id))
			a.mu.Lock()
			a.modes[id] = mode
			a.mu.Unlock()
			if !noImport {
				writeDesktopEntry(t, a.index, id, id, false, nil, fmt.Sprintf(`,"cwd":%q`, a.dirOf(id)))
			}
		case strings.HasPrefix(link, "claude://claude.ai/epitaxy/local_"):
			id := strings.TrimPrefix(link, "claude://claude.ai/epitaxy/local_")
			inbox := newFakeInbox(t, findTranscript(t, home, id))
			a.mu.Lock()
			a.inboxes[id] = inbox
			a.mu.Unlock()
			go func() {
				time.Sleep(200 * time.Millisecond) // sleep-ok: the app takes a moment to start the session's claude
				writeOutsideClaude(t, home, id, "claude-desktop-3p", "idle", inbox, nil)
			}()
		}
		return nil
	}
	t.Cleanup(func() { seedDesktopSession, openDesktopLink = oldSeed, oldOpen })
	return a
}

func (a *fakeDesktopApp) dirOf(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.seeds {
		if s.id == id {
			return s.dir
		}
	}
	return ""
}

func (a *fakeDesktopApp) importedMode(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.modes[id]
}

// importedDesktopMode is the mode the app imports the transcript at path in: the permissionMode of its last user line
// that names one ("" for none: the app's settings decide, and it lowers bypassPermissions to acceptEdits).
func importedDesktopMode(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := ""
	for _, line := range strings.Split(string(b), "\n") {
		var row struct {
			Type           string `json:"type"`
			PermissionMode string `json:"permissionMode"`
		}
		if json.Unmarshal([]byte(line), &row) == nil && row.Type == "user" && row.PermissionMode != "" {
			mode = row.PermissionMode
		}
	}
	return mode
}

func (a *fakeDesktopApp) seeded() []fakeSeed {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]fakeSeed(nil), a.seeds...)
}

func (a *fakeDesktopApp) opened() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.links...)
}

func (a *fakeDesktopApp) inbox(id string) *fakeInbox {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inboxes[id]
}

func findTranscript(t *testing.T, home, id string) string {
	t.Helper()
	found, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", id+".jsonl"))
	if len(found) != 1 {
		t.Fatalf("transcript of %s: %v", id, found)
	}
	return found[0]
}

// fakeClaudeOnPath puts an executable claude on PATH (what the launcher needs to be offered).
func fakeClaudeOnPath(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	p := filepath.Join(bin, "claude")
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return p
}

func startDesktop(t *testing.T, n *Node, params map[string]any) (proto.Session, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	res, err := n.handle(context.Background(), "session.start", b)
	if err != nil {
		return proto.Session{}, err
	}
	out, _ := json.Marshal(res)
	var r struct {
		Session proto.Session `json:"session"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return r.Session, nil
}

// New Agent with the desktop app's launcher starts the session in the app: the CLI writes it with a local command only
// (named after the first message), the app imports it at its resume link, and the first message goes to the inbox of
// the claude the app starts for it. No terminal is asked for anything.
func TestNewAgentWithTheDesktopLauncherStartsTheSessionInTheApp(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	app := newFakeDesktopApp(t, home)
	claude := fakeClaudeOnPath(t)
	shellLaunchers(t, "claude")
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reset()
	dir := filepath.Join(home, "projects", "report")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := n.handle(context.Background(), "launchers.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"name":"claude-desktop","base_url":"","model":"","source":"claude_desktop","agent":"claude"`) {
		t.Fatalf("launchers: %s", b)
	}

	const prompt = "  整理  本周的\n周报"
	s, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": "claude-desktop", "prompt": prompt, "async": true})
	if err != nil {
		t.Fatal(err)
	}
	firstMessageReceived := func() bool {
		in := app.inbox(s.SessionID)
		return in != nil && len(in.users()) == 1
	}
	// The inbox records the user before send finishes confirming delivery. Drain that send before fixture cleanup
	// restores peerConfirmWait or closes its background-created inbox, even when an assertion below fails.
	defer func() {
		waitFor(t, "the first message in the session's inbox", firstMessageReceived)
		waitFor(t, "the desktop session's first send to finish", func() bool {
			unlock, ok := n.typing.tryLock(s.SessionID)
			if ok {
				unlock()
			}
			return ok
		})
	}()
	if !validID(s.SessionID) || s.Cwd != dir || s.Title != "整理 本周的" || s.SourceKind != proto.SourceClaudeDesktop || s.Launcher != "" {
		t.Fatalf("session: %+v", s)
	}
	seeds := app.seeded()
	if len(seeds) != 1 || seeds[0] != (fakeSeed{claude, dir, s.SessionID, "整理 本周的"}) {
		t.Fatalf("seeded: %+v (claude %s)", seeds, claude)
	}
	if links := app.opened(); len(links) == 0 || links[0] != "claude://resume?session="+s.SessionID {
		t.Fatalf("links: %v", links)
	}
	waitFor(t, "the first message in the session's inbox", firstMessageReceived)
	users := app.inbox(s.SessionID).users()
	if content, _ := users[0]["message"].(map[string]any)["content"].(string); !strings.Contains(content, "整理  本周的\n周报") {
		t.Fatalf("first message: %v", users[0])
	}
	for _, c := range f.trace() {
		if strings.Contains(c, "workspace") || strings.Contains(c, "tab.create") || strings.Contains(c, "pane.") {
			t.Fatalf("herdr was asked to start or type something: %s", c)
		}
	}
}

// A folder that is not there yet, in one that is, is made (New Agent's new folder), and the session runs in it.
func TestADesktopStartMakesANewFolder(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	dir := filepath.Join(t.TempDir(), "新项目")
	s, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": "claude-desktop"})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || s.Cwd != dir || len(app.seeded()) != 1 || app.seeded()[0].dir != dir {
		t.Fatalf("folder %v, session %+v, seeded %v", err, s, app.seeded())
	}
}

// The apps start with no first message and send it after (async): the session is named after the start's name hint,
// and the message, sent as any message, goes to the inbox of the claude the app starts for it.
func TestADesktopStartNamedAfterItsHintTakesTheFirstMessageAfter(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t, "claude")
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	dir := t.TempDir()
	s, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": "claude-desktop", "async": true, "name_hint": "周报\n整理"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "周报" || len(app.seeded()) != 1 || app.seeded()[0].title != "周报" {
		t.Fatalf("session %+v, seeded %v", s, app.seeded())
	}
	if links := app.opened(); len(links) != 1 {
		t.Fatalf("a message was sent with no first message: %v", links)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	params, _ := json.Marshal(map[string]any{"session_id": s.SessionID, "text": "周报\n整理", "client_msg_id": newUUID()})
	if _, err := n.handle(ctx, "session.send", params); err != nil {
		t.Fatalf("first message: %v", err)
	}
	if in := app.inbox(s.SessionID); in == nil || len(in.users()) != 1 {
		t.Fatal("the first message is not in the session's inbox")
	}
}

// A computer with no terminal engine starts one in the app too: the launcher is not a terminal's.
func TestTheDesktopLauncherNeedsNoTerminalEngine(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	dir := t.TempDir()
	s, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": "claude-desktop", "task_instructions": "Keep the task scope."})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(app.seeded()) != 1 || s.Title != filepath.Base(dir) {
		t.Fatalf("seeded %v, session %+v", app.seeded(), s)
	}
	if links := app.opened(); len(links) != 1 { // no first message: nothing more to open
		t.Fatalf("links: %v", links)
	}
}

// The launcher is offered where the app keeps Claude Code sessions and the CLI that writes them is found; a login
// shell's launcher of its name runs instead, as for a runtime's.
func TestTheDesktopLauncherIsOfferedWhereTheAppKeepsSessions(t *testing.T) {
	n, home := testNode(t)
	first, _ := useDesktopDirs(t)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	if _, added := n.withDesktopLauncher(nil); added {
		t.Fatal("offered with no session index")
	}
	if err := os.MkdirAll(filepath.Join(first, "claude-code-sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, added := n.withDesktopLauncher([]proto.Launcher{{Name: "claude"}})
	if !added || len(list) != 2 || list[1] != (proto.Launcher{Name: "claude-desktop", Source: "claude_desktop", Agent: "claude", DisplayName: "Claude Desktop"}) {
		t.Fatalf("list %+v", list)
	}
	if !n.knownLauncher(context.Background(), "claude-desktop") {
		t.Fatal("not known")
	}
	models, err := n.launcherModels(context.Background(), "claude-desktop", "", false)
	if err != nil || models.Agent != "claude" || len(models.Models) != 0 || models.Locked != "" {
		t.Fatalf("models %+v, %v", models, err)
	}
	if c := n.launcherCheck(context.Background(), "claude-desktop"); c.Error != desktopCheckMsg {
		t.Fatalf("check: %+v", c)
	}

	// No claude where the node looks: not offered (a machine-wide one, outside the test's home, skips this part).
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", home)
	if _, err := desktopClaudeCLI(); err == nil {
		t.Log("a claude is installed machine-wide: the no-claude case is not checked here")
	} else if _, added := n.withDesktopLauncher(nil); added {
		t.Fatal("offered without a claude")
	}

	fakeClaudeOnPath(t)
	shellLaunchers(t, "claude-desktop")
	if _, added := n.withDesktopLauncher([]proto.Launcher{{Name: "claude-desktop"}}); added || isDesktopLauncher("claude-desktop") {
		t.Fatal("the login shell's claude-desktop is not the app's")
	}
}

// With no folder, the session runs in the scratch folder beside most of the app's sessions' folders (made the first
// time), so the app's sidebar shows them as one group; the scratch folder's own sessions count for the folder it is in.
func TestADesktopStartWithNoFolderRunsInTheScratchFolder(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	projects, other := filepath.Join(home, "Code"), filepath.Join(home, "Elsewhere")
	// Code holds a project and the scratch folder, Elsewhere one project: Code is where the projects are.
	for i, cwd := range []string{filepath.Join(projects, "a"), filepath.Join(other, "c"), filepath.Join(projects, desktopScratchName)} {
		id := strings.Replace("ffffffff-0000-4000-8000-00000000000x", "x", string(rune('1'+i)), 1)
		writeDesktopEntry(t, app.index, id, id, false, nil, fmt.Sprintf(`,"cwd":%q`, cwd))
	}
	if got, want := n.desktopScratchDir(), filepath.Join(projects, desktopScratchName); got != want {
		t.Fatalf("scratch %s, want %s", got, want)
	}
	s, err := startDesktop(t, n, map[string]any{"cwd": "", "launcher": "claude-desktop"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(projects, desktopScratchName); s.Cwd != want {
		t.Fatalf("cwd %s, want %s", s.Cwd, want)
	}
	if st, err := os.Stat(s.Cwd); err != nil || !st.IsDir() {
		t.Fatalf("scratch folder: %v", err)
	}

	// No session in the app yet: in Documents, or the home folder without one.
	n2, home2 := testNode(t)
	useDesktopDirs(t)
	if got := n2.desktopScratchDir(); got != filepath.Join(home2, desktopScratchName) {
		t.Fatalf("scratch with no Documents: %s", got)
	}
	if err := os.MkdirAll(filepath.Join(home2, "Documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := n2.desktopScratchDir(); got != filepath.Join(home2, "Documents", desktopScratchName) {
		t.Fatalf("scratch: %s", got)
	}
}

// What the app could not run as asked is refused before anything is written: a read-only plan (the app runs its own
// mode, and its approval would wait on the computer), a command (the inbox runs none), a folder that is not there, a
// secret hand-off (no terminal to start through). An import that does not happen in time is the app not opening it.
func TestADesktopStartRefusesWhatTheAppCannotRun(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove("relative-new") }) // where a relative folder would be made: the test's own
	for _, c := range []struct {
		params map[string]any
		code   string
		says   string
	}{
		{map[string]any{"cwd": dir, "launcher": "claude-desktop", "permission_mode": "plan", "prompt": "look"}, "readonly_unsupported", ""},
		{map[string]any{"cwd": dir, "launcher": "claude-desktop", "prompt": " /compact"}, peerCommand, ""},
		{map[string]any{"cwd": filepath.Join(dir, "missing", "new"), "launcher": "claude-desktop", "prompt": "hi"}, "", "does not exist"},
		{map[string]any{"cwd": "relative-new", "launcher": "claude-desktop", "prompt": "hi"}, "", "does not exist"},
		{map[string]any{"cwd": file, "launcher": "claude-desktop", "prompt": "hi"}, "", "is not a directory"},
		{map[string]any{"cwd": dir, "launcher": "claude-desktop", "prompt": "hi", "secret_ticket": strings.Repeat("a", 64)}, secretEnvAgentCode, ""},
	} {
		_, err := startDesktop(t, n, c.params)
		var re *rpc.Error
		if err == nil || c.code != "" && (!errors.As(err, &re) || re.Code != c.code) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%v: %v, want %q %q", c.params, err, c.code, c.says)
		}
	}
	if len(app.seeded()) != 0 || len(app.opened()) != 0 {
		t.Fatalf("seeded %v, opened %v", app.seeded(), app.opened())
	}

	app.mu.Lock()
	app.noImport = true
	app.mu.Unlock()
	old := desktopImportWait
	desktopImportWait = 300 * time.Millisecond
	t.Cleanup(func() { desktopImportWait = old })
	_, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": "claude-desktop", "prompt": "hi"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "desktop_start_incomplete" || !strings.Contains(string(re.Data), `"cause_code":"`+desktopOpenFailed+`"`) {
		t.Fatalf("not imported: %v", err)
	}
	if links := app.opened(); len(links) != 1 { // nothing sent to a session the app does not have
		t.Fatalf("links: %v", links)
	}
}

// The CLI writes the new session with a local command only, in its folder, without what would make it another
// claude's (a running Claude Code's variables, the assistant's) or put it where the app does not look.
func TestTheDesktopSeedRunsALocalCommandOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the native Windows seed is covered by TestWindowsDesktopSeedArguments")
	}
	bin, out := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	claude := filepath.Join(bin, "claude")
	script := "#!/bin/sh\n{ pwd; for a in \"$@\"; do echo \"arg:$a\"; done; env; } > " + out + "\n"
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_ENTRYPOINT": "claude-desktop-3p", "CLAUDE_CONFIG_DIR": "/elsewhere",
		"PERSONA_SESSION_ID": "x", "KEEP_ME": "yes"} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	const id = "abcdef01-0000-4000-8000-000000000001"
	if err := seedDesktopSession(context.Background(), claude, dir, id, "周报 整理"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	real, _ := filepath.EvalSymlinks(dir)
	lines := strings.Split(got, "\n")
	if lines[0] != dir && lines[0] != real {
		t.Errorf("ran in %q", lines[0])
	}
	if want := "arg:-p\narg:--session-id\narg:" + id + "\narg:/rename 周报 整理\n"; !strings.Contains(got, want) {
		t.Errorf("args: %s", got)
	}
	for _, gone := range []string{"CLAUDECODE=", "CLAUDE_CODE_ENTRYPOINT=", "CLAUDE_CONFIG_DIR=", "PERSONA_SESSION_ID="} {
		if strings.Contains(got, "\n"+gone) {
			t.Errorf("%s still set", gone)
		}
	}
	if !strings.Contains(got, "\nKEEP_ME=yes") {
		t.Error("the rest of the environment is gone")
	}

	if err := os.WriteFile(claude, []byte("#!/bin/sh\necho 'Not logged in' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := seedDesktopSession(context.Background(), claude, dir, id, "x"); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("a failed run: %v", err)
	}
}

// The app lowers bypassPermissions to acceptEdits for a session it imports and keeps auto (#9350), so a start with no
// mode, or bypassPermissions, is imported in auto; default, acceptEdits and auto are imported as asked. The mode is on
// the transcript before the app is asked to import it.
func TestADesktopStartIsImportedInAutoMode(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	dir := t.TempDir()
	for _, c := range []struct{ asked, want string }{
		{"", "auto"},
		{"bypassPermissions", "auto"},
		{"auto", "auto"},
		{"acceptEdits", "acceptEdits"},
		{"default", "default"},
	} {
		params := map[string]any{"cwd": dir, "launcher": "claude-desktop"}
		if c.asked != "" {
			params["permission_mode"] = c.asked
		}
		s, err := startDesktop(t, n, params)
		if err != nil {
			t.Fatalf("%q: %v", c.asked, err)
		}
		if got := app.importedMode(s.SessionID); got != c.want {
			t.Errorf("asked %q: imported in %q, want %q", c.asked, got, c.want)
		}
	}
}

// stampDesktopMode puts the mode on the user lines that name none and keeps every other byte of the transcript.
func TestStampDesktopModeKeepsTheRestOfTheTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	before := `{"type":"custom-title","customTitle":"周报 <整理> & co"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"<command-name>/rename</command-name>"}}` + "\n" +
		`{"type":"user","permissionMode":"acceptEdits","message":{"role":"user","content":"kept"}}` + "\r\n" +
		`not json` + "\n" +
		`{"type":"system","content":"<local-command-stdout>x</local-command-stdout>"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"last, no newline"}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := stampDesktopMode(path, "auto"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	want := strings.Replace(strings.Replace(before,
		`{"type":"user","message":{"role":"user","content":"<command`, `{"permissionMode":"auto","type":"user","message":{"role":"user","content":"<command`, 1),
		`{"type":"user","message":{"role":"user","content":"last`, `{"permissionMode":"auto","type":"user","message":{"role":"user","content":"last`, 1)
	if string(b) != want {
		t.Fatalf("stamped:\n%s\nwant:\n%s", b, want)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != beforeStat.Mode().Perm() {
		t.Errorf("mode %v, want original %v", st.Mode().Perm(), beforeStat.Mode().Perm())
	}

	if err := os.WriteFile(path, []byte(`{"type":"custom-title","customTitle":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stampDesktopMode(path, "auto"); err == nil {
		t.Fatal("a transcript with no user line was stamped")
	}
}

func TestDesktopTitleIsTheFirstLineOfTheFirstMessage(t *testing.T) {
	long := strings.Repeat("长", desktopTitleMax+5)
	for _, c := range []struct{ prompt, want string }{
		{"  fix   the\tlogin page \nand more", "fix the login page"},
		{"\n\n  第二行开始\n", "第二行开始"},
		{long, strings.Repeat("长", desktopTitleMax) + "…"},
		{"   \n ", "report"},
	} {
		if got := desktopTitle(c.prompt, "/src/report"); got != c.want {
			t.Errorf("%q: %q, want %q", c.prompt, got, c.want)
		}
	}
}

// The desktop app's launcher runs nowhere but in the app: not in a pane (Run Agent Here), not as the launcher a running
// session switches to, not for a new start in a terminal; a resume naming it runs plain claude.
func TestTheDesktopLauncherNeverRunsInATerminal(t *testing.T) {
	n, home := testNode(t)
	first, _ := useDesktopDirs(t)
	if err := os.MkdirAll(filepath.Join(first, "claude-code-sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeClaudeOnPath(t)
	shellLaunchers(t, "claude")
	if _, err := n.runAgentInPane(context.Background(), "w1:p1", "claude-desktop"); !errors.Is(err, errDesktopLauncherInTerminal) {
		t.Errorf("Run Agent Here: %v", err)
	}
	var re *rpc.Error
	if _, _, err := n.checkSwitchLauncher(context.Background(), sidA, "claude-desktop", ""); !errors.As(err, &re) || re.Code != proto.CodeSwitchOtherAgent {
		t.Errorf("switch: %v", err)
	}
	if _, err := n.start(context.Background(), home, "claude-desktop", "", "", "", "", "", "", "", ""); !errors.Is(err, errDesktopLauncherInTerminal) {
		t.Errorf("new start in a terminal: %v", err)
	}
	for _, c := range []struct{ launcher, resume, want string }{
		{"claude-desktop", sidA, "claude"}, {"glm", sidA, "glm"}, {"glm", "", "glm"},
	} {
		if got, err := terminalLauncher(c.launcher, c.resume); err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
	shellLaunchers(t, "claude", "claude-desktop") // the login shell's of that name is an ordinary launcher
	if got, err := terminalLauncher("claude-desktop", ""); err != nil || got != "claude-desktop" {
		t.Errorf("shell's claude-desktop: %q %v", got, err)
	}
}
