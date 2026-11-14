package node

import (
	"context"
	"log"
	"os"
	"os/exec"
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
// Only the index's sessions, on macOS (claudeDesktopOpens), and only when no claude has the session open: one
// that does (a terminal's claude without an inbox, a program's) is refused as before, never given a second claude.

// desktopOpenWait bounds the wait for the app to start the session's claude and its inbox; desktopInboxGrace is how
// long a claude seen without an inbox is given to name one before it is taken for one that has none.
var (
	desktopOpenWait   = 30 * time.Second
	desktopInboxGrace = 3 * time.Second
)

// openDesktopLink opens link with its handler, in the background (a variable: tests record the link).
var openDesktopLink = func(ctx context.Context, link string) error {
	return exec.CommandContext(ctx, "/usr/bin/open", "-g", link).Run()
}

const (
	desktopOpenFailed     = "desktop_open_failed"
	desktopOpenTimeoutMsg = "电脑上的 Claude 桌面版没有及时打开这个会话（它可能还在启动，或电脑在锁屏）：在电脑上打开它再发。这次没有发送。"
)

// sendThroughDesktopApp sends text to session id through the desktop app when the app's sidebar lists it and nothing
// runs it: the app opens it, then the message goes to its inbox. handled false for any other session, and when the link
// cannot be opened at all (nothing was started: the caller goes on as before), or not in the background here
// (claudeDesktopOpens).
func (n *Node) sendThroughDesktopApp(ctx context.Context, id, text string, images []proto.Image) (handled bool, err error) {
	if !claudeDesktopOpens {
		return false, nil
	}
	local := readClaudeDesktopIndex(n.home).about[id].local
	if local == "" {
		return false, nil
	}
	if r := n.openElsewhereRefusal(id); r != nil {
		return true, r
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		// The inbox runs no commands, and a command resumed in a terminal would take the session out of the app.
		return true, refusal(peerCommand, peerCommandMsg, nil)
	}
	if err := openDesktopLink(ctx, "claude://claude.ai/epitaxy/"+local); err != nil {
		log.Printf("send %s: could not ask the desktop app to open it (%v): resuming it as before", id, err)
		return false, nil
	}
	deadline := time.Now().Add(desktopOpenWait)
	var noInbox time.Time
	for {
		// The app may go on in a new transcript as it opens the session: the one its entry names now.
		cur := n.desktopTranscript(local, id)
		if c, ok := n.outsideClaude(cur); ok {
			if p, has := peerOf(c); has {
				if _, err := os.Stat(p.socket); err == nil {
					if cur != id {
						log.Printf("send %s: the desktop app went on in %s", id, cur)
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

// desktopTranscript is the transcript the app's session local runs now, by its entry; id when no entry names it.
func (n *Node) desktopTranscript(local, id string) string {
	for t, a := range readClaudeDesktopIndex(n.home).about {
		if a.local == local {
			return t
		}
	}
	return id
}
