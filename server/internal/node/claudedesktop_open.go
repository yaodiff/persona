package node

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A session of Claude's desktop app that nothing runs goes back to the app, not into a terminal. The app opens a session
// at its own link, claude://claude.ai/epitaxy/<the session's local id> (the index's sessionId, claudedesktop.go; the one
// app keeps both data directories, Claude and Claude-3p). Opened in the background, the app starts that session's
// claude, with its peer inbox, without coming to the front or changing the session its window shows: about 3 seconds on
// a Mac, measured 2026-11-11. The message then goes to the inbox as to any session the app runs (claudebg.go
// sendOutside). The session stays the app's: its sidebar and window show the turn, and no second claude writes the
// transcript. Before, the node resumed it with `claude --resume` in a terminal tab, so the session ran outside the app,
// and was refused on a machine without a terminal engine.
//
// The index's sessions and sessions created for the app that await import, only when no claude has the session open: one
// that does (a terminal's claude without an inbox, a program's) is refused as before, never given a second claude.

// desktopOpenWait bounds the wait for the app to start the session's claude and its inbox; desktopInboxGrace is how
// long a claude seen without an inbox is given to name one before it is taken for one that has none.
var (
	desktopOpenWait   = 30 * time.Second
	desktopInboxGrace = 3 * time.Second
)

const (
	desktopOpenFailed     = "desktop_open_failed"
	desktopOpenTimeoutMsg = "电脑上的 Claude 桌面版没有及时打开这个会话（它可能还在启动，或电脑在锁屏）：在电脑上打开它再发。这次没有发送。"
)

// sendThroughDesktopApp sends text to session id through the desktop app when its index or persisted launcher owns it
// and nothing runs it: pending imports retain the same transcript, then the app opens its inbox. Desktop failures are
// handled refusals, so a retry never starts a terminal claude against the app's transcript.
func (n *Node) sendThroughDesktopApp(ctx context.Context, id, text string, images []proto.Image) (handled bool, err error) {
	// A scoped node's home may still be the user's real home. Its own offline
	// sessions continue through the regular route; never inspect or open the
	// user's desktop sessions while deciding that route.
	if n.sessionScope != nil || !claudeDesktopOpens {
		return false, nil
	}
	local := readClaudeDesktopIndex(n.home).about[id].local
	if local == "" {
		if n.state == nil {
			return false, nil
		}
		if err := n.state.loaded(ctx); err != nil {
			return true, err
		}
		if state, ok := n.state.get(id); !ok || state.Launcher != claudeDesktopLauncher {
			return false, nil
		}
	}
	if r := n.openElsewhereRefusal(id); r != nil {
		return true, r
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		// The inbox runs no commands, and a command resumed in a terminal would take the session out of the app.
		return true, refusal(peerCommand, peerCommandMsg, nil)
	}
	if local == "" {
		if err := n.ensureDesktopImport(ctx, id); err != nil {
			return true, err
		}
		local = readClaudeDesktopIndex(n.home).about[id].local
		if local == "" {
			return true, refusal(desktopOpenFailed, desktopOpenTimeoutMsg, nil)
		}
	}
	if err := openDesktopLink(ctx, "claude://claude.ai/epitaxy/"+local); err != nil {
		log.Printf("send %s: could not ask the desktop app to open it: %v", id, err)
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, refusal(desktopOpenFailed, desktopOpenTimeoutMsg, nil)
	}
	deadline := time.Now().Add(desktopOpenWait)
	var noInbox time.Time
	for {
		// The app may go on in a new transcript as it opens the session: the one its entry names now.
		cur := n.desktopTranscript(local, id)
		if c, ok := n.outsideClaude(cur); ok {
			if p, has := peerOf(c); has {
				if peerReachable(p) && peerInboxExists(p.socket) {
					if cur != id {
						log.Printf("send %s: the desktop app went on in %s", id, cur)
						n.transferDesktopSettings(id, cur)
					}
					return n.sendOpened(ctx, cur, text, images)
				}
			} else if noInbox.IsZero() {
				noInbox = time.Now()
			} else if time.Since(noInbox) >= desktopInboxGrace {
				return n.sendOpened(ctx, cur, text, images) // an older Claude Code: refused below, left open in the app
			}
		}
		if time.Now().After(deadline) {
			log.Printf("send %s: the desktop app did not start it within %s", id, desktopOpenWait)
			return true, refusal(desktopOpenFailed, desktopOpenTimeoutMsg, nil)
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// sendOpened sends to the claude the app started for session id; the refusal says why when there is none to send to.
func (n *Node) sendOpened(ctx context.Context, id, text string, images []proto.Image) (bool, error) {
	log.Printf("send %s: opened in the desktop app", id)
	if handled, err := n.sendOutside(ctx, id, text, images); handled {
		return true, err
	}
	if _, ok := n.outsideClaude(id); !ok {
		return true, refusal(peerUnreachable, peerUnreachMsg, nil) // it was there, and is gone
	}
	return true, refusal(peerUnsupported, peerNoInboxMsg, nil)
}

// wentOnKey marks a send that already went on from an earlier transcript (sendWentOn): it goes on once at most.
type wentOnKey struct{}

// desktopWentOn is the transcript the desktop app's session runs now when that session went on from transcript id and
// is unarchived (#14394); "" for any other id (a session's current transcript, one no entry names, an archived
// session's), for one a claude has open (a terminal's that took it up: refused as open elsewhere, as before), for a
// send that already went on, and where sendThroughDesktopApp does not look (a scoped node, an app that cannot be
// opened).
func (n *Node) desktopWentOn(ctx context.Context, id string) string {
	if n.sessionScope != nil || !claudeDesktopOpens || !validID(id) || ctx.Value(wentOnKey{}) != nil {
		return ""
	}
	for _, s := range readClaudeDesktopIndex(n.home).earlier[id] {
		if !s.archived {
			if _, _, open := n.claudeOpenElsewhere(id); open {
				return ""
			}
			return s.transcript
		}
	}
	return ""
}

// sendWentOn sends the message for transcript id, which its desktop session went on from, as one to the transcript
// the session runs now (cur): the inbox of its claude, or the app opens it (sendThroughDesktopApp), never a claude
// --resume of id in a terminal beside the app's session. The settings queued for id go with it, as when the app goes on
// in a new transcript as it opens a session. Sent as cur's, it takes cur's turn (inputTurn) inside id's, and the host
// id last ran in (withResumeHost) is not cur's.
func (n *Node) sendWentOn(ctx context.Context, id, cur, text string, images []proto.Image, opts sendOpts) error {
	log.Printf("send %s: its desktop session went on in %s", id, cur)
	n.transferDesktopSettings(id, cur)
	ctx = context.WithValue(context.WithValue(ctx, startHostKey{}, ""), wentOnKey{}, id)
	return n.send(ctx, cur, text, images, opts)
}

// desktopTranscript is the transcript the app's session local runs now, by its entry; id when no entry names it.
func (n *Node) desktopTranscript(local, id string) string {
	for t, a := range readClaudeDesktopIndex(n.home).about {
		if a.local == local {
			return t
		}
	}
	return id
}

func (n *Node) transferDesktopSettings(oldID, newID string) {
	if oldID == "" || newID == "" || oldID == newID {
		return
	}
	n.ccPlugins.moveQueuedSettings(oldID, newID)
	n.mu.Lock()
	if m, ok := n.modes[oldID]; ok {
		cur := n.modes[newID]
		if m.Model != nil {
			cur.Model = m.Model
		}
		if m.Effort != "" {
			cur.Effort, cur.EffortAt = m.Effort, m.EffortAt
		}
		n.modes[newID] = cur
	}
	n.mu.Unlock()
}
