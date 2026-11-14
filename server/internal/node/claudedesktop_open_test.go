//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const desktopLocal = "eeeeeeee-0000-4000-8000-000000000001" // the app's own id of sidA's session: local_<this>

// recordDesktopLinks replaces the opener: each link it is asked to open is sent on the channel returned, then `then`
// runs (the app starting a claude, say) and fail is returned.
func recordDesktopLinks(t *testing.T, fail error, then func()) <-chan string {
	t.Helper()
	links := make(chan string, 4)
	old := openDesktopLink
	openDesktopLink = func(_ context.Context, link string) error {
		links <- link
		if then != nil {
			go then()
		}
		return fail
	}
	t.Cleanup(func() { openDesktopLink = old })
	return links
}

// sendWithin sends text to id on another goroutine; the result comes on the channel.
func sendWithin(ctx context.Context, n *Node, id, text string) <-chan error {
	done := make(chan error, 1)
	b, _ := json.Marshal(map[string]any{"session_id": id, "text": text, "client_msg_id": newUUID()})
	go func() {
		_, err := n.handle(ctx, "session.send", b)
		done <- err
	}()
	return done
}

func waitSend(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("the send did not end")
		return nil
	}
}

func herdrAsked(f *fakeHerdr) string {
	for _, c := range f.trace() {
		if strings.Contains(c, "workspace") || strings.Contains(c, "tab.create") || strings.Contains(c, "pane.") {
			return c
		}
	}
	return ""
}

func shortDesktopWaits(t *testing.T, open, grace time.Duration) {
	t.Helper()
	oldOpen, oldGrace := desktopOpenWait, desktopInboxGrace
	desktopOpenWait, desktopInboxGrace = open, grace
	t.Cleanup(func() { desktopOpenWait, desktopInboxGrace = oldOpen, oldGrace })
}

// A session of the desktop app's sidebar that nothing runs is opened in the app, in the background, at its own link;
// the message goes to the inbox of the claude the app starts for it. Nothing is resumed or typed in a terminal. The
// third-party build's directory is the same app's.
func TestADesktopSessionNothingRunsIsOpenedInTheApp(t *testing.T) {
	for _, third := range []bool{false, true} {
		n, f, home := herdrNode(t)
		noPaneEnv(t)
		shortPeerWait(t, 3*time.Second)
		first, thirdDir := useDesktopDirs(t)
		dir := first
		if third {
			dir = thirdDir
		}
		p := bgTranscript(t, home, home, sidA, "plan")
		writeDesktopEntry(t, dir, desktopLocal, sidA, false, nil, "")
		inbox := newFakeInbox(t, p)
		started := make(chan struct{})
		links := recordDesktopLinks(t, nil, func() { time.Sleep(300 * time.Millisecond); close(started) }) // sleep-ok: the desktop app takes a moment to open the link
		f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
		f.reset()
		done := sendWithin(context.Background(), n, sidA, "go on with the plan")
		select {
		case <-started: // the app starts the session's claude
		case err := <-done:
			t.Fatalf("sent without opening the app: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("the app was not asked to open the session")
		}
		writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "idle", inbox, nil)
		if err := waitSend(t, done); err != nil {
			t.Fatalf("send: %v", err)
		}
		if link := <-links; link != "claude://claude.ai/epitaxy/local_"+desktopLocal {
			t.Fatalf("opened %q", link)
		}
		users := inbox.users()
		if len(users) != 1 {
			t.Fatalf("the inbox read %d messages", len(users))
		}
		if content, _ := users[0]["message"].(map[string]any)["content"].(string); !strings.Contains(content, "go on with the plan") {
			t.Errorf("message: %v", users[0])
		}
		if c := herdrAsked(f); c != "" {
			t.Fatalf("herdr was asked to start or type something: %s", c)
		}
		// Running now, it is sent to as any session the app runs: the app is not asked again.
		if err := waitSend(t, sendWithin(context.Background(), n, sidA, "and then?")); err != nil {
			t.Fatalf("second send: %v", err)
		}
		if len(links) != 0 || len(inbox.users()) != 2 {
			t.Fatalf("second send: %d links, %d messages", len(links), len(inbox.users()))
		}
	}
}

// The app goes on in a new transcript as it opens the session: the message goes to the claude of the one the entry
// names now.
func TestADesktopSessionThatGoesOnInANewTranscriptIsFollowed(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	first, _ := useDesktopDirs(t)
	bgTranscript(t, home, home, sidA, "plan")
	pb := bgTranscript(t, home, home, sidB, "plan")
	writeDesktopEntry(t, first, desktopLocal, sidA, false, nil, "")
	inbox := newFakeInbox(t, pb)
	moved := make(chan struct{})
	recordDesktopLinks(t, nil, func() { time.Sleep(200 * time.Millisecond); close(moved) }) // sleep-ok: the desktop app takes a moment to open the link
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	done := sendWithin(context.Background(), n, sidA, "go on")
	<-moved
	time.Sleep(20 * time.Millisecond) // sleep-ok: the entry's file must look changed (a newer mtime) for the index to read it again
	writeDesktopEntry(t, first, desktopLocal, sidB, false, []string{sidA}, `,"title":"plan, again"`)
	writeOutsideClaude(t, home, sidB, "claude-desktop-3p", "idle", inbox, nil)
	if err := waitSend(t, done); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(inbox.users()) != 1 {
		t.Fatalf("the new transcript's inbox read %d messages", len(inbox.users()))
	}
}

// The app's claude can name its inbox a moment after it starts: it is given that moment, not taken for an older one.
func TestADesktopClaudeThatNamesItsInboxLateIsWaitedFor(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	first, _ := useDesktopDirs(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	writeDesktopEntry(t, first, desktopLocal, sidA, false, nil, "")
	inbox := newFakeInbox(t, p)
	opened := make(chan struct{}, 1)
	recordDesktopLinks(t, nil, func() { opened <- struct{}{} })
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	done := sendWithin(context.Background(), n, sidA, "go on")
	<-opened
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", nil, nil) // starting: no inbox yet
	time.Sleep(1200 * time.Millisecond)                                      // sleep-ok: the app's claude stays busy without an inbox for longer than one poll
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "idle", inbox, nil)
	if err := waitSend(t, done); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(inbox.users()) != 1 {
		t.Fatalf("the inbox read %d messages", len(inbox.users()))
	}
}

// Another claude has the session open (a terminal's without an inbox, a program's): refused as before, the app not
// asked to start a second one.
func TestADesktopSessionAnotherClaudeHasIsNotOpened(t *testing.T) {
	for _, entrypoint := range []string{"cli", "sdk-ts"} {
		t.Run(entrypoint, func(t *testing.T) {
			n, f, home := herdrNode(t)
			noPaneEnv(t)
			first, _ := useDesktopDirs(t)
			bgTranscript(t, home, home, sidA, "plan")
			writeDesktopEntry(t, first, desktopLocal, sidA, false, nil, "")
			writeOutsideClaude(t, home, sidA, entrypoint, "idle", nil, nil)
			links := recordDesktopLinks(t, nil, nil)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			err := waitSend(t, sendWithin(context.Background(), n, sidA, "go on"))
			if refusalCode(err) != openElsewhere {
				t.Fatalf("send: %v", err)
			}
			if len(links) != 0 {
				t.Fatalf("the app was asked to open it: %s", <-links)
			}
		})
	}
}

// Refusals, with nothing sent anywhere: the app does not start it in time; a claude without an inbox (an older Claude
// Code) after its grace; a slash command, which the inbox runs no more than the terminal should take the session.
func TestADesktopSessionRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, text, code string
		start            bool // the app starts a claude without an inbox
		links            int
	}{
		{"not opened in time", "still there?", desktopOpenFailed, false, 1},
		{"no inbox", "still there?", peerUnsupported, true, 1},
		{"a command", "/compact", peerCommand, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			noPaneEnv(t)
			first, _ := useDesktopDirs(t)
			bgTranscript(t, home, home, sidA, "plan")
			writeDesktopEntry(t, first, desktopLocal, sidA, false, nil, "")
			shortDesktopWaits(t, 2*time.Second, 300*time.Millisecond)
			opened := make(chan struct{}, 1)
			links := recordDesktopLinks(t, nil, func() { opened <- struct{}{} })
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			f.reset()
			done := sendWithin(context.Background(), n, sidA, tc.text)
			if tc.start {
				<-opened
				writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "idle", nil, nil)
			}
			if err := waitSend(t, done); refusalCode(err) != tc.code {
				t.Fatalf("send: %v", err)
			}
			if len(links) != tc.links {
				t.Fatalf("%d links opened", len(links))
			}
			if c := herdrAsked(f); c != "" {
				t.Fatalf("herdr was asked to start or type something: %s", c)
			}
		})
	}
}

// The old way still serves what is not the sidebar's (an archived session, an entry whose sessionId is not the app's
// form) and a link that cannot be opened at all (nothing was started): the session is resumed in herdr as before.
func TestTheOldWayServesTheRest(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		archived    bool
		fail        error
		links       int
	}{
		{"archived", "", true, nil, 0},
		{"not the app's id", `,"sessionId":"../../etc"`, false, nil, 0},
		{"no opener", "", false, errors.New("no handler"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			noPaneEnv(t)
			first, _ := useDesktopDirs(t)
			bgTranscript(t, home, home, sidA, "plan")
			writeDesktopEntry(t, first, desktopLocal, sidA, tc.archived, nil, tc.extra)
			links := recordDesktopLinks(t, tc.fail, nil)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			f.reset()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // the resume itself is not this test's
			defer cancel()
			waitSend(t, sendWithin(ctx, n, sidA, "go on"))
			if len(links) != tc.links {
				t.Fatalf("%d links opened", len(links))
			}
			if herdrAsked(f) == "" {
				t.Fatalf("not resumed the old way: %v", f.trace())
			}
		})
	}
}

// Where the app does not open its links in the background (Windows: claudeDesktopOpens false), its index is read for
// the list alone: a send to a session of its sidebar opens nothing and is handled as before the index was read (a
// stopped agent's refusal), and the app's launcher is not offered.
func TestWithoutABackgroundOpenTheIndexIsForTheListAlone(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	first, _ := useDesktopDirs(t)
	claudeDesktopOpens = false
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	bgTranscript(t, home, home, sidA, "plan")
	writeDesktopEntry(t, first, desktopLocal, sidA, false, nil, "")
	if readClaudeDesktopIndex(home).about[sidA].local == "" {
		t.Fatal("the index was not read")
	}
	links := recordDesktopLinks(t, nil, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reset()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := waitSend(t, sendWithin(ctx, n, sidA, "go on"))
	if len(links) != 0 {
		t.Fatalf("%d links opened", len(links))
	}
	if code, _ := refusalOf(t, err); code != agentStopped {
		t.Fatalf("send: %v", err)
	}
	if _, added := n.withDesktopLauncher(nil); added {
		t.Fatal("the app's launcher offered")
	}
}

// The index keeps an unarchived session's local id, only in the app's own form (it goes into a link).
func TestTheIndexKeepsTheAppsOwnID(t *testing.T) {
	first, third := useDesktopDirs(t)
	home := t.TempDir()
	writeDesktopEntry(t, first, desktopLocal, sidA, false, nil, "")
	writeDesktopEntry(t, third, "ffffffff-0000-4000-8000-000000000002", sidB, false, nil, `,"sessionId":"local_x y"`)
	x := readClaudeDesktopIndex(home)
	if got := x.about[sidA].local; got != "local_"+desktopLocal {
		t.Errorf("local id %q", got)
	}
	if got := x.about[sidB].local; got != "" {
		t.Errorf("a malformed id kept: %q", got)
	}
}
