//go:build !windows

package node

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A message to a transcript a session of the desktop app went on from goes on in the session (#14394): sent as one to
// the transcript it runs now, to its claude's inbox when it runs, else the app opens the session and the message goes to
// the claude it starts. Nothing is resumed in a terminal: a claude --resume of the old transcript would run beside the
// app's session. Before, a stale copy of the entry sent it either way, and with none the old transcript was resumed.
func TestAMessageToAnEarlierDesktopTranscriptGoesOnInTheSession(t *testing.T) {
	for _, running := range []bool{true, false} {
		n, f, home := herdrNode(t)
		noPaneEnv(t)
		shortPeerWait(t, 3*time.Second)
		first, third := useDesktopDirs(t)
		bgTranscript(t, home, home, sidA, "plan")
		now := bgTranscript(t, home, home, sidB, "plan")
		writeDesktopEntry(t, first, desktopLocal, sidB, false, []string{sidA}, "")
		// a copy from before the session went on, under another account: it still names the old transcript
		writeDesktopCopy(t, third, "account2", "org", desktopLocal, sidA, false, nil, "", 0)
		inbox := newFakeInbox(t, now)
		start := func() { writeOutsideClaude(t, home, sidB, "claude-desktop", "idle", inbox, nil) }
		if running {
			start()
		}
		links := recordDesktopLinks(t, nil, start)
		f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
		f.reset()
		if err := waitSend(t, sendWithin(context.Background(), n, sidA, "go on with the plan")); err != nil {
			t.Fatalf("running %v: send: %v", running, err)
		}
		users := inbox.users()
		if len(users) != 1 {
			t.Fatalf("running %v: the session's inbox read %d messages", running, len(users))
		}
		if content, _ := users[0]["message"].(map[string]any)["content"].(string); !strings.Contains(content, "go on with the plan") {
			t.Errorf("running %v: message %v", running, users[0])
		}
		if running && len(links) != 0 {
			t.Errorf("the app was asked to open a session its claude runs: %s", <-links)
		}
		if !running {
			if link := <-links; link != "claude://claude.ai/epitaxy/local_"+desktopLocal {
				t.Errorf("opened %q", link)
			}
		}
		if c := herdrAsked(f); c != "" {
			t.Fatalf("running %v: herdr was asked to start or type something: %s", running, c)
		}
	}
}
