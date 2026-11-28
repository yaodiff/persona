package node

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// A Claude session already open in another client — Claude's desktop app, a terminal Persona does not drive — is not
// resumed from the phone: a second `claude --resume` would write the same transcript as the one running. Without a
// terminal host Persona cannot see those as live (they list as not running), so the check is on the session files
// every running claude writes (sessions/<pid>.json in its config directory).

// Refusals of a start or resume, with nothing started or typed (the app shows the reason): a running claude has the
// session open; the machine's engine (herdr) is not running or not installed.
const (
	openElsewhere = "open_elsewhere"
	engineMissing = "engine_missing"
)

// claudeOpenElsewhere is the running claude that has session id open, outside any pane Persona drives (callers ask
// for sessions with no live agent), and where it runs (whereOpen).
func (n *Node) claudeOpenElsewhere(id string) (pid int, where string, ok bool) {
	if id == "" {
		return 0, "", false
	}
	for _, c := range n.liveClaudes() {
		if c.SessionID == id {
			return c.pid, whereOpen(c.Entrypoint), true
		}
	}
	return 0, "", false
}

// claudeProc is a running claude's sessions/<pid>.json: the session it has open and how it runs it.
type claudeProc struct {
	pid        int
	SessionID  string `json:"sessionId"`
	Entrypoint string `json:"entrypoint"`
	Cwd        string `json:"cwd"`
	Name       string `json:"name"`
	// NameSource says where Name came from: "derived" is the one Claude Code makes up itself, its directory's base
	// name and two hex digits ("home-a8"); "user", "peer", "collision", "auto" and "hook" are names something chose.
	NameSource string `json:"nameSource"`
	// Kind is "interactive" for a claude in a terminal, "bg" for a background session Claude's daemon runs; Status is
	// busy, idle, shell (idle with background commands running) or waiting (claudebg.go).
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// StatusUpdatedAt is when Status was last written, in Unix milliseconds (0 from a Claude Code that writes none).
	StatusUpdatedAt int64 `json:"statusUpdatedAt"`
	// ParkedJobID is set while an interactive claude has sent its own session to the background (claudebg.go).
	ParkedJobID string `json:"parkedJobId"`
	JobID       string `json:"jobId"` // a background session's job (claudebg.go)
	Spare       bool   `json:"spare"` // a warm background process no session has claimed yet
	StartedAt   int64  `json:"startedAt"`
	// ProcStart is when that process started, as Claude Code writes it (on Linux the starttime of /proc/<pid>/stat): a
	// file left by a claude that exited names a pid that may belong to another program by now.
	ProcStart string `json:"procStart"`
	// The claude's peer inbox (claudepeer.go): where it listens and the protocol it speaks.
	MessagingSocketPath string `json:"messagingSocketPath"`
	PeerProtocol        int    `json:"peerProtocol"`
	// Tmux is the pane Claude Code says it runs in ("orc:@11.%11", its session:window.pane), "" for one that is not in
	// tmux: the approval in such a pane can be answered from the app (#2562).
	Tmux string `json:"tmux"`
	dir  string // the sessions directory the file is in (its key file is next to it)
}

// literalGlobPath quotes every glob metacharacter in a literal directory. Windows does not support backslash escaping
// in filepath patterns, so use character classes there. The caller appends the active *.json suffix separately.
func literalGlobPath(path string) string {
	return literalGlobPathForOS(path, runtime.GOOS)
}

func literalGlobPathForOS(path, goos string) string {
	if goos == "windows" {
		return windowsGlobEscapes.Replace(path)
	}
	return globEscapes.Replace(path)
}

// Made once: the session list escapes every config directory on each rebuild, and a Replacer builds its table anew
// each time it is made (#2826's allocation bound).
var (
	windowsGlobEscapes = strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]")
	globEscapes        = strings.NewReplacer(`\`, `\\`, "[", `\[`, "*", `\*`, "?", `\?`)
)

// liveClaudes reads the sessions/<pid>.json of every claude still running, in every config directory, once per pid.
// The list calls it on every rebuild (once a second): a file is read again only once it changed (claudefiles.go).
func (n *Node) liveClaudes() []claudeProc {
	var out []claudeProc
	seen := map[int]bool{}
	var dirs []string
	for _, dir := range n.existingClaudeDirs() {
		dirs = append(dirs, filepath.Join(dir, "sessions"))
	}
	listed := map[string]bool{}
	for _, sessions := range dirs {
		files, _ := filepath.Glob(filepath.Join(literalGlobPath(sessions), "*.json"))
		for _, f := range files {
			p, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".json"))
			if err != nil || p <= 0 || seen[p] {
				continue
			}
			st, err := os.Stat(f)
			if err != nil || st.Size() > claudeFileMaxBytes {
				continue
			}
			listed[f] = true
			c, ok := n.cfiles.parse(f, st)
			if !ok {
				continue
			}
			c.pid, c.dir = p, sessions
			if !sameProcess(p, claudeFileStart(c.ProcStart)) {
				continue // a file left by a claude that exited (its pid may have been reused)
			}
			seen[p] = true
			out = append(out, c)
		}
	}
	n.cfiles.keep(dirs, listed)
	return out
}

// Where a claude outside Persona's panes has a session open: the open_elsewhere refusal's `where`, which the hub says
// in the app's language (apierr openElsewhereKeys, rpc-errors.open_elsewhere_*), and its words here.
const (
	whereDesktop    = "desktop"
	whereTerminal   = "terminal"
	whereProgram    = "program"
	whereOther      = "other"
	whereAgentsList = "agents_list" // a parked session's pane on Claude Code's agents list (claudebg.go)
)

var whereNames = map[string]string{
	whereDesktop:  "Claude 桌面版",
	whereTerminal: "电脑上的一个终端",
	whereProgram:  "电脑上的另一个程序",
	whereOther:    "电脑上的另一个地方",
}

// whereOpen is where a claude runs, from its entrypoint. The desktop app writes "claude-desktop" and, in some builds,
// a suffix ("claude-desktop-3p"); all of them are the desktop app.
func whereOpen(entrypoint string) string {
	switch {
	case desktopEntrypoint(entrypoint):
		return whereDesktop
	case entrypoint == "cli":
		return whereTerminal
	case strings.HasPrefix(entrypoint, "sdk"):
		return whereProgram
	default:
		return whereOther
	}
}

// desktopEntrypoint: the claude runs in Claude's desktop app.
func desktopEntrypoint(entrypoint string) bool {
	return strings.HasPrefix(entrypoint, "claude-desktop")
}

// openElsewhereRefusal is the refusal for a resume of a session open in another client, nil when none has it open.
// Its words name no device: the send may have come from the phone, the web or a computer.
func (n *Node) openElsewhereRefusal(id string) error {
	pid, where, ok := n.claudeOpenElsewhere(id)
	if !ok {
		return nil
	}
	w := whereNames[where]
	if w != "" && w[0] < 0x80 {
		w = " " + w // "in the Claude desktop app" in Chinese: a space between Chinese and a Latin word
	}
	return refusal(openElsewhere, "这个会话正在"+w+"里开着，Persona 不会再起一个 Claude 同时写它：在"+w+
		"里接着用，或者先在那边关掉它再发。这次没有发送。", map[string]any{"pid": pid, "where": where})
}
