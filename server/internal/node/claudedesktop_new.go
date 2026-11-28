package node

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// A new session in Claude's desktop app, started from the phone (docs/integrations/claude-integration.md "New sessions
// in the desktop app", #5480). The app takes no session with a first message from outside: its new-session links only fill
// its composer, for the person at the computer to send. It does import a session the Claude Code CLI wrote, at
// claude://resume?session=<the transcript's id> (what the CLI's /desktop hands over with): the transcript becomes one of
// the app's sessions, in its sidebar under the session's folder, and the app runs it from then on. So a start with the
// launcher claudeDesktopLauncher:
//
//  1. writes the session with the CLI and no model request: `claude -p --session-id <new id> "/rename <title>"` runs a
//     local command only, so it needs no sign-in, and the transcript holds the folder and the title the app shows (the
//     app gives an imported session none of its own: the title is the first message's, or the start's name hint when
//     the message goes after it, as the apps send it);
//  2. stamps the start's permission mode on the transcript's user lines (desktopStartMode, stampDesktopMode), the
//     mode the app imports the session in;
//  3. asks the app to import it (in the background; the app comes to the front all the same) and waits for its index to
//     list the session (claudedesktop.go);
//  4. sends the first message as to any session of the app (send → sendThroughDesktopApp): the app starts the
//     session's claude with its peer inbox, and the message goes in. The start answers only after delivery succeeds.
//
// The app runs the session with its provider. Requested model and effort overrides are queued for Persona's Claude
// Code plugin; with neither requested, the app's defaults apply.

// claudeDesktopLauncher is the launcher a computer whose desktop app keeps Claude Code sessions offers (launchers.list),
// and claudeDesktopSource its Source.
const (
	claudeDesktopLauncher = "claude-desktop"
	claudeDesktopSource   = "claude_desktop"
)

// desktopCheckMsg explains why Settings › Models' Test has no standalone command for this launcher.
const desktopCheckMsg = "这个启动器在 Claude 桌面版里新开会话，用的是桌面版自己的设置，没有可以单独测试的命令。"

// desktopScratchName is the folder, beside the folders of the app's sessions, that a start with no folder runs in.
const desktopScratchName = "临时会话"

var (
	// desktopSeedWait bounds the CLI writing the session; desktopImportWait the app importing it; desktopFirstSendWait
	// the first message being delivered before the start is acknowledged.
	desktopSeedWait      = 30 * time.Second
	desktopImportWait    = 15 * time.Second
	desktopFirstSendWait = 2 * time.Minute

	// seedDesktopSession writes session id in dir with the CLI at claude, named title (a variable: tests write their own).
	seedDesktopSession = func(ctx context.Context, claude, dir, id, title string) error {
		ctx, cancel := context.WithTimeout(ctx, desktopSeedWait)
		defer cancel()
		program, prefix, err := desktopClaudeProgram(claude)
		if err != nil {
			return err
		}
		argv := append(prefix, "-p", "--session-id", id, "/rename "+title)
		cmd := exec.CommandContext(ctx, program, argv...)
		cmd.Dir = dir
		cmd.Env = desktopSeedEnv(os.Environ())
		prepareDesktopChild(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(lastLines(string(out), 3)))
		}
		return nil
	}
)

// desktopSeedEnv is env without what makes a claude another's: a running Claude Code's variables and the assistant's
// (agentenv.go), and a config directory of its own, so the transcript is where the app looks (~/.claude).
func desktopSeedEnv(env []string) []string {
	drop := map[string]bool{"CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true, "ELECTRON_RUN_AS_NODE": true}
	for _, k := range assistantOnlyEnvNames() {
		drop[k] = true
	}
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		return drop[k]
	})
}

// desktopKeepsSessions: the desktop app on this computer keeps Claude Code sessions (its index directory is there), so
// it can take one. claudeDesktopDirs knows the native platform's data directories.
func (n *Node) desktopKeepsSessions() bool {
	if !claudeDesktopOpens {
		return false
	}
	for _, dir := range claudeDesktopDirs(n.home) {
		if st, err := os.Stat(filepath.Join(dir, "claude-code-sessions")); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// withDesktopLauncher is list with the desktop app's launcher after it, on a computer whose app keeps sessions and
// where the CLI that writes them is found; added reports whether it was. A launcher of the login shell's with its
// name is left as it is: that one runs.
func (n *Node) withDesktopLauncher(list []proto.Launcher) (out []proto.Launcher, added bool) {
	if n.sessionScope != nil || !n.desktopKeepsSessions() || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == claudeDesktopLauncher }) {
		return list, false
	}
	if _, err := desktopClaudeExecutable(n.home); err != nil {
		return list, false
	}
	return append(slices.Clone(list), proto.Launcher{Name: claudeDesktopLauncher, Source: claudeDesktopSource, Agent: "claude", DisplayName: "Claude Desktop"}), true
}

// isDesktopLauncher: name is the desktop app's launcher, not a login shell's of that name.
func isDesktopLauncher(name string) bool {
	return name == claudeDesktopLauncher && !shellLauncherNames()[name]
}

// errDesktopLauncherInTerminal refuses the desktop app's launcher where a terminal would run it; errCodexAppLauncherInTerminal
// the Codex App's (codexapp_new.go).
var (
	errDesktopLauncherInTerminal  = errors.New("claude-desktop 只在 Claude 桌面版里新开会话，不能在终端里运行；换一个启动器。")
	errCodexAppLauncherInTerminal = errors.New("codex-app 只在 Codex App 里新开对话，不能在终端里运行；换一个启动器。")
)

// terminalLauncher is the launcher a start in a terminal runs (control.go start): an app's (the desktop app's, the Codex
// App's) runs nowhere but in its app. A resume naming one (New Agent's launcher, which the hub sends for a session
// nothing says the launcher of) runs plain claude or codex; a new start with one is refused (session.start sends it to
// its app before this; Run Agent Here and a work session would type it into a shell).
func terminalLauncher(launcher, resume string) (string, error) {
	switch {
	case isDesktopLauncher(launcher) && resume == "":
		return "", errDesktopLauncherInTerminal
	case isDesktopLauncher(launcher):
		return "claude", nil
	case isCodexAppLauncher(launcher) && resume == "":
		return "", errCodexAppLauncherInTerminal
	case isCodexAppLauncher(launcher):
		return "codex", nil
	}
	return launcher, nil
}

// startInDesktopApp starts a new session in the desktop app for a start with its launcher; handled false for any
// other start. A resume only retries setup of an existing desktop session, without creating a second transcript.
// nameHint names a new session when the start carries no first message (the apps send it after the start).
func (n *Node) startInDesktopApp(ctx context.Context, p runtimeStartParams, nameHint string) (s *proto.Session, err error, handled bool) {
	if !isDesktopLauncher(p.Launcher) {
		return nil, nil, false
	}
	if p.Resume != "" {
		s, err = n.resumeDesktopSession(ctx, p)
		return s, err, true
	}
	s, err = n.newDesktopSession(ctx, p, nameHint)
	return s, err, true
}

func (n *Node) newDesktopSession(ctx context.Context, p runtimeStartParams, nameHint string) (*proto.Session, error) {
	if n.sessionScope != nil {
		return nil, refusal("desktop_scope_unavailable", "Desktop sessions are unavailable in a scoped node.", nil)
	}
	if err := n.checkClaudeDesktopProject(p.DesktopProjectID, p.Cwd); err != nil {
		return nil, err
	}
	if !n.desktopKeepsSessions() {
		return nil, errors.New("这台电脑上没有 Claude 桌面版的 Code 会话，不能在桌面版里新开会话，这次没有发送。")
	}
	if p.PermissionMode == "plan" {
		// The app runs the session in its own mode, and a plan waiting for approval there could not be answered here.
		return nil, readonlyStartUnsupported()
	}
	if strings.HasPrefix(strings.TrimSpace(p.Prompt), "/") {
		return nil, refusal(peerCommand, peerCommandMsg, nil) // the first message goes to the inbox, which runs no commands
	}
	model := strings.TrimSpace(p.Model)
	if model != "" && !proto.ValidModelID(model) {
		return nil, fmt.Errorf("invalid model %q", p.Model)
	}
	if err := validModes("", p.Effort); err != nil {
		return nil, err
	}
	claude, err := desktopClaudeExecutable(n.home)
	if err != nil {
		return nil, fmt.Errorf("没找到 Claude Code（claude 命令），不能在桌面版里新开会话：%v", err)
	}
	dir, err := n.desktopStartDir(p.Cwd)
	if err != nil {
		return nil, err
	}
	id, title := newUUID(), desktopTitle(cmp.Or(strings.TrimSpace(p.Prompt), nameHint), dir)
	s := &proto.Session{SessionID: id, Cwd: dir, Title: title, Agent: "claude", Status: proto.StatusIdle,
		Model: model, Effort: p.Effort, StartedByPersona: true, SourceKind: proto.SourceClaudeDesktop, UpdatedAt: time.Now().UTC()}
	backend := backendLaunchFrom(ctx)
	if backend != nil {
		if err := backend.beforeDispatch(id); err != nil {
			return nil, err
		}
	}
	seedErr := seedDesktopSession(ctx, claude, dir, id, title)
	path := n.transcriptPath(id)
	if seedErr != nil && path == "" {
		return nil, fmt.Errorf("Claude Code 没能写出新会话：%v", seedErr)
	}
	n.state.put(id, sessionState{Launcher: claudeDesktopLauncher, Cwd: dir, StartedAt: s.UpdatedAt})
	if backend != nil {
		if err := backend.executor.recordLaunchSession(id); err != nil {
			return nil, err
		}
	}
	if path == "" {
		return s, desktopCreatedError(s, fmt.Errorf("Claude Code 没有写出会话 %s", id), false)
	}
	if err := stampDesktopMode(path, desktopStartMode(p.PermissionMode)); err != nil {
		// The session still imports, in the app's own choice of mode (Accept edits where settings say bypass).
		log.Printf("new desktop session %s: permission mode not written: %v", id, err)
	}
	if model != "" {
		n.noteModel(id, model, "")
	}
	if p.Effort != "" {
		n.noteModes(id, "", p.Effort)
	}
	if model != "" || p.Effort != "" {
		n.queueCCPluginSettings(id, model, p.Effort)
	}
	if seedErr != nil {
		// Keep the requested settings even when recovery sends directly to the retained session.
		return s, desktopCreatedError(s, seedErr, false)
	}
	if err := n.ensureDesktopImport(ctx, id); err != nil {
		return s, desktopCreatedError(s, err, false)
	}
	log.Printf("new desktop session %s in %s: imported by the app", id, dir)
	if strings.TrimSpace(p.Prompt) != "" {
		if err := n.firstDesktopMessage(ctx, id, p.Prompt); err != nil {
			return s, desktopCreatedError(s, err, true)
		}
	}
	go n.notifySessions()
	return s, nil
}

// ensureDesktopImport retries only the native import for this exact transcript. It never creates a transcript or turn.
func (n *Node) ensureDesktopImport(ctx context.Context, id string) error {
	if n.sessionScope != nil || !validID(id) {
		return refusal("desktop_scope_unavailable", "This node cannot import the desktop session.", nil)
	}
	index := readClaudeDesktopIndex(n.home)
	if !index.named[id] && (index.deleted[id] || index.deletedFromEntry[id]) {
		return refusal("desktop_session_unavailable", "The desktop session was deleted; create a new session in the desktop app.", nil)
	}
	if visible, known := index.visible[id]; known && !visible {
		return refusal("desktop_session_unavailable", "The desktop session was archived or replaced; open its current session in the desktop app.", nil)
	}
	if len(index.earlier[id]) > 0 {
		return refusal("desktop_session_unavailable", "The desktop session was replaced; open its current session in the desktop app.", nil)
	}
	if index.about[id].local != "" {
		return nil
	}
	if err := openDesktopLink(ctx, "claude://resume?session="+id); err != nil {
		return refusal(desktopOpenFailed, desktopOpenTimeoutMsg, nil)
	}
	if !n.awaitDesktopImport(ctx, id) {
		log.Printf("desktop session %s: the app did not import it within %s", id, desktopImportWait)
		return refusal(desktopOpenFailed, desktopOpenTimeoutMsg, nil)
	}
	return nil
}

// resumeDesktopSession recovers an interrupted native creation, including a blank session whose setup needs retrying.
func (n *Node) resumeDesktopSession(ctx context.Context, p runtimeStartParams) (*proto.Session, error) {
	if n.sessionScope != nil || !validID(p.Resume) {
		return nil, refusal("desktop_scope_unavailable", "This node cannot resume the desktop session.", nil)
	}
	if err := claudeDesktopProjectSelectionError(p.DesktopProjectID); err != nil {
		return nil, err
	}
	if p.PermissionMode == "plan" {
		return nil, readonlyStartUnsupported()
	}
	model := strings.TrimSpace(p.Model)
	if model != "" && !proto.ValidModelID(model) {
		return nil, fmt.Errorf("invalid model %q", p.Model)
	}
	if err := validModes("", p.Effort); err != nil {
		return nil, err
	}
	if err := n.state.loaded(ctx); err != nil {
		return nil, err
	}
	state, known := n.state.get(p.Resume)
	about := readClaudeDesktopIndex(n.home).about[p.Resume]
	if (!known || state.Launcher != claudeDesktopLauncher) && about.local == "" {
		return nil, refusal("desktop_session_unknown", "The requested session is not a known Claude desktop session.", nil)
	}
	s := &proto.Session{SessionID: p.Resume, Cwd: cmp.Or(about.cwd, state.Cwd), Title: about.title, Agent: "claude",
		Status: proto.StatusIdle, Model: model, Effort: p.Effort, StartedByPersona: known, SourceKind: proto.SourceClaudeDesktop, UpdatedAt: time.Now().UTC()}
	if err := n.checkClaudeDesktopProject(p.DesktopProjectID, s.Cwd); err != nil {
		return nil, err
	}
	if known && state.Launcher == claudeDesktopLauncher && about.local == "" {
		// A failed seed can leave its transcript before newDesktopSession stamps the requested import mode.
		if path := n.transcriptPath(p.Resume); path != "" {
			if err := stampDesktopMode(path, desktopStartMode(p.PermissionMode)); err != nil {
				log.Printf("resume desktop session %s: permission mode not written: %v", p.Resume, err)
			}
		}
	}
	if model != "" {
		n.noteModel(p.Resume, model, "")
	}
	if p.Effort != "" {
		n.noteModes(p.Resume, "", p.Effort)
	}
	if model != "" || p.Effort != "" {
		n.queueCCPluginSettings(p.Resume, model, p.Effort)
	}
	if err := n.ensureDesktopImport(ctx, p.Resume); err != nil {
		return s, desktopCreatedError(s, err, false)
	}
	if strings.TrimSpace(p.Prompt) != "" {
		if err := n.firstDesktopMessage(ctx, p.Resume, p.Prompt); err != nil {
			return s, desktopCreatedError(s, err, true)
		}
	}
	return s, nil
}

// The verified resume protocol imports a transcript by id; it has no verified native environment selector.
// Refuse explicit environment selections before any seed, import, or send instead of silently changing projects.
func claudeDesktopProjectSelectionError(projectID string) error {
	if projectID == "" || strings.HasPrefix(projectID, "claude-project:") {
		return nil
	}
	if root, ok := strings.CutPrefix(projectID, projectSpace); ok && filepath.IsAbs(root) {
		return nil
	}
	if strings.HasPrefix(projectID, "claude-environment:") {
		return refusal("claude_desktop_project_unsupported", "Claude Desktop cannot guarantee that this import uses the selected named environment. Clear the project selection to continue with a directory-based desktop session.", nil)
	}
	return refusal("claude_desktop_project_unsupported", "The selected project is not a supported Claude Desktop directory project.", nil)
}

func (n *Node) checkClaudeDesktopProject(projectID, cwd string) error {
	if err := claudeDesktopProjectSelectionError(projectID); err != nil || projectID == "" {
		return err
	}
	root, legacy := strings.CutPrefix(projectID, "claude-project:")
	if !legacy {
		root = strings.TrimPrefix(projectID, projectSpace)
	}
	cwd = proto.ExpandHome(cwd, n.home)
	if !filepath.IsAbs(root) || !filepath.IsAbs(cwd) ||
		!pathWithin(folderKey(realPath(filepath.Clean(cwd))), folderKey(realPath(filepath.Clean(root)))) {
		return refusal("claude_desktop_project_mismatch", "The session directory is outside the selected Claude Desktop directory project.", nil)
	}
	return nil
}

// awaitDesktopImport waits for the app's index to list session id.
func (n *Node) awaitDesktopImport(ctx context.Context, id string) bool {
	deadline := time.Now().Add(desktopImportWait)
	for {
		if readClaudeDesktopIndex(n.home).about[id].local != "" {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// desktopStartMode is the permission mode a new session of the app starts in for a start asking mode (#9350). The app
// imports a transcript in the mode of its last user line that names one, else in the settings' defaultMode, and always
// lowers bypassPermissions to acceptEdits for an imported session (measured in Claude.app 5.11492.5); auto it keeps,
// unless device policy withholds auto mode. So no mode, and bypassPermissions, which the app would not keep, start in
// auto, the mode closest to it the app runs an imported session in; default, acceptEdits and auto are kept as asked.
func desktopStartMode(mode string) string {
	switch mode {
	case "default", "acceptEdits", "auto":
		return mode
	}
	return "auto"
}

// stampDesktopMode writes mode as the permissionMode of the user lines of the transcript at path that name none (the
// seed's: the CLI writes no mode on a local command's lines, whatever --permission-mode says), so the app imports the
// session in it. Every other line is kept byte for byte, and the file is replaced whole.
func stampDesktopMode(path, mode string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	field := []byte(`{"permissionMode":` + strconv.Quote(mode) + `,`)
	lines := bytes.SplitAfter(b, []byte("\n"))
	stamped := 0
	for i, line := range lines {
		body := bytes.TrimRight(line, "\r\n")
		var row struct {
			Type           string  `json:"type"`
			PermissionMode *string `json:"permissionMode"`
		}
		if len(body) < 2 || body[0] != '{' || json.Unmarshal(body, &row) != nil || row.Type != "user" || row.PermissionMode != nil {
			continue
		}
		next := append(append(slices.Clone(field), body[1:]...), line[len(body):]...)
		if !json.Valid(bytes.TrimRight(next, "\r\n")) {
			continue
		}
		lines[i] = next
		stamped++
	}
	if stamped == 0 {
		return errors.New("the transcript has no user line to carry it")
	}
	return writeFileAtomic(path, bytes.Join(lines, nil))
}

// firstDesktopMessage reports delivery before the creation request is acknowledged.
func (n *Node) firstDesktopMessage(ctx context.Context, id, text string) error {
	ctx, cancel := context.WithTimeout(ctx, desktopFirstSendWait)
	defer cancel()
	if err := n.send(ctx, id, text, nil, sendOpts{}); err != nil {
		log.Printf("new desktop session %s: the first message did not go in: %v", id, err)
		return err
	}
	log.Printf("new desktop session %s: the first message went in", id)
	return nil
}

// desktopStartDir is where a new session of the app runs: dir (~ expanded), made when it is not there yet but the folder
// it goes in is (New Agent's new folder); with none, the scratch folder beside the app's sessions (desktopScratchDir),
// made when it is not there yet.
func (n *Node) desktopStartDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		scratch := n.desktopScratchDir()
		if err := os.MkdirAll(scratch, 0o755); err != nil {
			return "", fmt.Errorf("建不了临时会话的文件夹 %s：%v", scratch, err)
		}
		return scratch, nil
	}
	dir = filepath.Clean(proto.ExpandHome(dir, n.home))
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("directory %s does not exist: an absolute path is required", dir)
	}
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return "", fmt.Errorf("%s is not a directory", dir)
		}
		return dir, nil
	}
	switch err := os.Mkdir(dir, 0o755); {
	case errors.Is(err, fs.ErrNotExist): // the folder it goes in is not there either
		return "", fmt.Errorf("directory %s does not exist", dir)
	case err != nil && !errors.Is(err, fs.ErrExist):
		return "", fmt.Errorf("建不了文件夹 %s：%v", dir, err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return dir, nil
}

// desktopScratchDir is the scratch folder (desktopScratchName) in the folder that holds most of the app's sessions'
// folders, the app's projects; with none, in ~/Documents (home without it). The sessions started there are one group
// in the app's sidebar.
func (n *Node) desktopScratchDir() string {
	count := map[string]int{}
	for _, a := range readClaudeDesktopIndex(n.home).about {
		if a.cwd == "" {
			continue
		}
		count[filepath.Dir(a.cwd)]++ // the scratch folder's own sessions count for the folder it is in
	}
	best := ""
	for dir, c := range count {
		if filepath.Dir(dir) == dir {
			continue
		}
		if best == "" || c > count[best] || c == count[best] && dir < best {
			best = dir
		}
	}
	if best == "" {
		best = n.home
		if st, err := os.Stat(filepath.Join(n.home, "Documents")); err == nil && st.IsDir() {
			best = filepath.Join(n.home, "Documents")
		}
	}
	return filepath.Join(best, desktopScratchName)
}

// desktopTitleMax is how many characters of the first message name the session in the app's sidebar.
const desktopTitleMax = 40

// desktopTitle names a new session of the app: the first line of its first message, spaces folded, cut at
// desktopTitleMax characters; with no message, its folder's name.
func desktopTitle(prompt, dir string) string {
	line := ""
	for _, l := range strings.Split(prompt, "\n") {
		if l = strings.Join(strings.FieldsFunc(l, unicode.IsSpace), " "); l != "" {
			line = l
			break
		}
	}
	if line == "" {
		return filepath.Base(dir)
	}
	if utf8.RuneCountInString(line) > desktopTitleMax {
		line = strings.TrimSpace(string([]rune(line)[:desktopTitleMax])) + "…"
	}
	return line
}
