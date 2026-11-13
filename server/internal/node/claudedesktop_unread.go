package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// A turn of a session in Claude's desktop app that ended while nobody looked at it is Unread: its row says done (not
// idle) until it is seen, so the phone shows Unread and the hub's finished push follows its usual rules, as for the
// Codex App's chats (codex_desktop.go) and a Claude in Tern (agent_status.go). #6477, docs/integrations/claude-integration.md
// ("Unread when a turn ends unseen").
//
// The app keeps no read state on disk. It takes a person to be attending a session when its window is focused, the
// session is shown and the person did something in the last five minutes; and its index records when it last brought a
// session into view (lastFocusedAt) and when a turn of it last began or ended (lastTurnEdgeAt). A session shown the
// whole time is not brought into view again when its turn ends, so the index alone would call the session someone is
// watching unread. So a resting session's last turn is seen when, after it ended:
//   - the app brought the session into view (lastFocusedAt), or
//   - the node found someone attending it: the app in front, the person active within claudeAttendIdle, and the
//     session among those the app brought into view last (within claudeShownTogether of the latest: a split view shows
//     two at once). The node keeps when (desktopSeenStore), across its restarts.
//
// Seen in an app (the phone, the web) is the hub's, as for any session.

// claudeDesktopSeen is what the index says of a session: when it was last brought into view and when a turn of it last
// began or ended (ms, 0 unknown).
type claudeDesktopSeen struct {
	focused, turnEdge int64
}

const (
	claudeDesktopBundle = "com.anthropic.claudefordesktop"
	// claudeAttendIdle: someone who did nothing for this long is away (the app's own rule for attending a session).
	claudeAttendIdle = 5 * time.Minute
	// claudeShownTogether: sessions brought into view within this of the latest are shown with it (a split view).
	claudeShownTogether = 5 * time.Second
)

// shown: the app shows session id now, as far as its index tells: it is among those it brought into view last.
func (x claudeDesktopIndex) shown(id string) bool {
	e, ok := x.seen[id]
	return ok && e.focused > 0 && e.focused >= x.lastShown-claudeShownTogether.Milliseconds()
}

// desktopTurnUnread: the last turn of desktop session id, which rests now, ended while nobody saw it. Someone attending
// it now sees it: that is kept, and it is not unread.
func (n *Node) desktopTurnUnread(id string, x claudeDesktopIndex) bool {
	e, ok := x.seen[id]
	if !ok || e.turnEdge <= max(e.focused, n.desktopSeen.at(n.dir, id)) { // no turn yet: 0
		return false
	}
	if x.shown(id) && claudeAppAttended() {
		n.desktopSeen.note(n.dir, id, time.Now().UnixMilli(), x)
		return false
	}
	return true
}

// desktopSeenStore is when the node found someone attending a desktop session (ms, by transcript), kept in
// <state dir>/desktop-seen.json so a restart does not make a watched session's last turn unread.
type desktopSeenStore struct {
	mu     sync.Mutex
	loaded bool
	seenAt map[string]int64
}

const desktopSeenFile = "desktop-seen.json"

func (s *desktopSeenStore) load(dir string) {
	if s.loaded {
		return
	}
	s.loaded, s.seenAt = true, map[string]int64{}
	if dir == "" {
		return
	}
	if b, err := os.ReadFile(filepath.Join(dir, desktopSeenFile)); err == nil {
		_ = json.Unmarshal(b, &s.seenAt)
	}
}

func (s *desktopSeenStore) at(dir, id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load(dir)
	return s.seenAt[id]
}

// note keeps that id was seen at; it keeps only what still matters (a session of x whose last turn edge is after it
// was last brought into view).
func (s *desktopSeenStore) note(dir, id string, at int64, x claudeDesktopIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load(dir)
	s.seenAt[id] = at
	for k := range s.seenAt {
		if e, ok := x.seen[k]; !ok || e.turnEdge <= e.focused {
			delete(s.seenAt, k)
		}
	}
	if dir == "" {
		return
	}
	b, _ := json.Marshal(s.seenAt)
	tmp := filepath.Join(dir, "."+desktopSeenFile+".tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, desktopSeenFile))
	}
}

// claudeAppAttended: someone is at the computer with Claude's desktop app in front (claudedesktop_attend_darwin.go;
// elsewhere true: the index is read on macOS only, so nothing is unread there anyway). It cannot tell (a command
// failed): true, so no turn is made unread, and nothing pushed, on a guess. A variable: tests set it.
var claudeAppAttended = claudeAppAttendedNow

var (
	bundleIDLine = lazyre.New(`"CFBundleIdentifier"\s*=\s*"([^"]*)"`)
	hidIdleLine  = lazyre.New(`"HIDIdleTime"\s*=\s*(\d+)`)
)

// frontBundleID is the bundle id in `lsappinfo info -only bundleid` output; "" none.
func frontBundleID(out string) string {
	if m := bundleIDLine.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// hidIdle is how long the person did nothing, from `ioreg -c IOHIDSystem` output (HIDIdleTime, in nanoseconds).
func hidIdle(out string) (time.Duration, bool) {
	m := hidIdleLine.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	ns, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(ns), true
}
