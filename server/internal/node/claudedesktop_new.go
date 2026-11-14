package node

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
//  2. asks the app to import it (in the background; the app comes to the front all the same) and waits for its index to
//     list the session (claudedesktop.go);
//  3. sends the first message as to any session of the app (send → sendThroughDesktopApp): the app starts the
//     session's claude with its peer inbox, and the message goes in. This runs after the answer, as an async start's
//     work does: the app may take seconds to start the session.
//
// The app runs the session with its own settings (provider, model, permission mode), as a session it opens itself.

// claudeDesktopLauncher is the launcher a computer whose desktop app keeps Claude Code sessions offers (launchers.list),
// and claudeDesktopSource its Source.
const (
	claudeDesktopLauncher = "claude-desktop"
	claudeDesktopSource   = "claude_desktop"
)

// desktopModelLocked says why New Agent's model does not apply to the launcher; desktopCheckMsg why Settings › Models'
// Test has nothing to run for it.
const (
	desktopModelLocked = "Claude 桌面版用它自己选的模型运行这个会话。"
	desktopCheckMsg    = "这个启动器在 Claude 桌面版里新开会话，用的是桌面版自己的设置，没有可以单独测试的命令。"
)

// desktopScratchName is the folder, beside the folders of the app's sessions, that a start with no folder runs in.
const desktopScratchName = "临时会话"

var (
	// desktopSeedWait bounds the CLI writing the session; desktopImportWait the app importing it; desktopFirstSendWait
	// the first message going in after the answer.
	desktopSeedWait      = 30 * time.Second
	desktopImportWait    = 15 * time.Second
	desktopFirstSendWait = 2 * time.Minute

	// seedDesktopSession writes session id in dir with the CLI at claude, named title (a variable: tests write their own).
	seedDesktopSession = func(ctx context.Context, claude, dir, id, title string) error {
		ctx, cancel := context.WithTimeout(ctx, desktopSeedWait)
		defer cancel()
		cmd := exec.CommandContext(ctx, claude, "-p", "--session-id", id, "/rename "+title)
		cmd.Dir = dir
		cmd.Env = desktopSeedEnv(os.Environ())
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
	drop := map[string]bool{"CLAUDE_CONFIG_DIR": true}
	for _, k := range assistantOnlyEnvNames() {
		drop[k] = true
	}
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return drop[k]
	})
}

// desktopKeepsSessions: the desktop app on this computer keeps Claude Code sessions (its index directory is there), so
// it can take one. Only where the app opens its links in the background (claudeDesktopOpens: macOS).
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
	if !n.desktopKeepsSessions() || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == claudeDesktopLauncher }) {
		return list, false
	}
	if _, err := claudeExecutable(); err != nil {
		return list, false
	}
	return append(slices.Clone(list), proto.Launcher{Name: claudeDesktopLauncher, Source: claudeDesktopSource, Agent: "claude"}), true
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
// other start (and a resume: a session of the app is continued by a message, claudedesktop_open.go). nameHint names it
// when the start carries no first message (the apps send it after the start).
func (n *Node) startInDesktopApp(ctx context.Context, p runtimeStartParams, nameHint string) (s *proto.Session, err error, handled bool) {
	if !isDesktopLauncher(p.Launcher) || p.Resume != "" {
		return nil, nil, false
	}
	s, err = n.newDesktopSession(ctx, p, nameHint)
	return s, err, true
}

func (n *Node) newDesktopSession(ctx context.Context, p runtimeStartParams, nameHint string) (*proto.Session, error) {
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
	claude, err := claudeExecutable()
	if err != nil {
		return nil, fmt.Errorf("没找到 Claude Code（claude 命令），不能在桌面版里新开会话：%v", err)
	}
	dir, err := n.desktopStartDir(p.Cwd)
	if err != nil {
		return nil, err
	}
	id, title := newUUID(), desktopTitle(cmp.Or(strings.TrimSpace(p.Prompt), nameHint), dir)
	if err := seedDesktopSession(ctx, claude, dir, id, title); err != nil {
		return nil, fmt.Errorf("Claude Code 没能写出新会话：%v", err)
	}
	if n.transcriptPath(id) == "" {
		return nil, fmt.Errorf("Claude Code 没有写出会话 %s", id)
	}
	if err := openDesktopLink(ctx, "claude://resume?session="+id); err != nil {
		return nil, refusal(desktopOpenFailed, desktopOpenTimeoutMsg, nil)
	}
	if !n.awaitDesktopImport(ctx, id) {
		log.Printf("new desktop session %s: the app did not import it within %s", id, desktopImportWait)
		return nil, refusal(desktopOpenFailed, desktopOpenTimeoutMsg, nil)
	}
	log.Printf("new desktop session %s in %s: imported by the app", id, dir)
	if strings.TrimSpace(p.Prompt) != "" {
		go n.firstDesktopMessage(id, p.Prompt)
	}
	go n.notifySessions()
	return &proto.Session{SessionID: id, Cwd: dir, Title: title, Agent: "claude", Status: proto.StatusIdle,
		StartedByPersona: true, SourceKind: proto.SourceClaudeDesktop, UpdatedAt: time.Now().UTC()}, nil
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

// firstDesktopMessage sends a new session's first message, as any message to a session of the app goes. A failure is
// logged: the session is there, in the app and the list, for the message to be sent again.
func (n *Node) firstDesktopMessage(id, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), desktopFirstSendWait)
	defer cancel()
	if err := n.send(ctx, id, text, nil, sendOpts{}); err != nil {
		log.Printf("new desktop session %s: the first message did not go in: %v", id, err)
		return
	}
	log.Printf("new desktop session %s: the first message went in", id)
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
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return "", fmt.Errorf("%s is not a directory", dir)
		}
		return dir, nil
	}
	if !filepath.IsAbs(dir) { // made relative to the node's own folder otherwise
		return "", fmt.Errorf("directory %s does not exist", dir)
	}
	switch err := os.Mkdir(dir, 0o755); {
	case errors.Is(err, fs.ErrNotExist): // the folder it goes in is not there either
		return "", fmt.Errorf("directory %s does not exist", dir)
	case err != nil && !errors.Is(err, fs.ErrExist):
		return "", fmt.Errorf("建不了文件夹 %s：%v", dir, err)
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
		if dir == string(filepath.Separator) {
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
