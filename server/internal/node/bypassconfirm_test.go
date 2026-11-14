package node

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// bypassConfirm is Claude Code 2.1.322's Bypass Permissions confirmation as a fresh 160×50 pane shows it: the cursor
// on "No, exit", blank rows under it.
func bypassConfirm(t *testing.T) string {
	return screenFixture(t, "claude-2.1.322-bypass-confirm-160.txt")
}

// withFirstAfterDialog shortens how long a first message held back by a dialog waits, and how often it looks.
func withFirstAfterDialog(t *testing.T, wait, poll time.Duration) {
	w, p := firstAfterDialogWait, firstAfterDialogPoll
	firstAfterDialogWait, firstAfterDialogPoll = wait, poll
	t.Cleanup(func() { firstAfterDialogWait, firstAfterDialogPoll = w, p })
}

// Only Claude Code's Bypass Permissions confirmation gets keys, and they choose "Yes, I accept" wherever the cursor
// is. A permission, the trust dialog (whose other choice is "No, exit" too), a picker or a bare "Yes, I accept"
// without the warning get none.
func TestBypassConfirmKeys(t *testing.T) {
	if got := bypassConfirmKeys(bypassConfirm(t)); !slices.Equal(got, []string{"down", "enter"}) {
		t.Errorf("cursor on No: %v", got)
	}
	onYes := strings.Replace(strings.Replace(bypassConfirm(t), "❯ No, exit", "  No, exit", 1), "    Yes, I accept", "  ❯ Yes, I accept", 1)
	if got := bypassConfirmKeys(onYes); !slices.Equal(got, []string{"enter"}) {
		t.Errorf("cursor on Yes: %v", got)
	}
	for _, name := range []string{"claude-2.1.312-trust-162.txt", "claude-2.1.316-danger-placeholder-100.txt",
		"claude-2.1.312-model-picker-162.txt", "claude-2.1.312-empty-162.txt"} {
		if got := bypassConfirmKeys(screenFixture(t, name)); got != nil {
			t.Errorf("%s: %v", name, got)
		}
	}
	if got := bypassConfirmKeys("Do you want to proceed?\n❯ No, exit\n  Yes, I accept\nEnter to confirm · Esc to cancel"); got != nil {
		t.Errorf("no warning: %v", got)
	}
}

// startInBypassPane makes herdr open pane w1:p2 for a start into workspace w1, with a claude whose screen is first
// the Bypass Permissions confirmation and, after any keys, its empty input box.
func startInBypassPane(t *testing.T, f *fakeHerdr) {
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.screenSteps("w1:p2", isKeys(), bypassConfirm(t), screenFixture(t, "claude-2.1.312-empty-162.txt"))
}

// A start Persona asked for bypass itself (the user chose Allow all) meets Claude Code's Bypass Permissions
// confirmation: the node chooses "Yes, I accept" (down, enter), waits for the input box, and the first message goes in.
// Before, the start stopped at the confirmation, which no card could answer, and the task never began.
func TestBypassStartAcceptsTheConfirmationAndSendsTheFirstMessage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		flags  string
	}{
		{"bypass", map[string]any{"permission_mode": proto.ModeBypass}, " --permission-mode bypassPermissions"},
		// A plan that goes on in bypass starts with --allow-dangerously-skip-permissions, which asks the same.
		{"plan then bypass", map[string]any{"permission_mode": "plan", "plan_then": proto.ModeBypass},
			" --permission-mode plan --allow-dangerously-skip-permissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "proj"))
			startInBypassPane(t, f)
			promptLands(t, f, home, cwd)
			params := map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build"}
			for k, v := range tc.params {
				params[k] = v
			}
			if _, err := request(t, n, "session.start", params); err != nil {
				t.Fatal(err)
			}
			n.firstWait.Wait()
			id := startedID(f)
			typed := call("pane.send_input", map[string]any{"pane_id": "w1:p2", "keys": []string{"Enter"},
				"text": startCmd("claude", "--session-id "+id+tc.flags, personaDir(home))})
			prompt := call("agent.prompt", map[string]any{"target": "w1:p2", "text": "fix the build"})
			assertTrace(t, f.trace("pane.send_input", "pane.send_keys", "agent.prompt"), []string{
				typed,
				sendKeys("w1:p2", "down", "enter"), // "Yes, I accept"
				prompt,
			})
			if p, ok := n.lostFirst.get(id); ok {
				t.Errorf("first message marked lost: %s", p.why)
			}
		})
	}
}

// A confirmation that stays after "Yes, I accept" was chosen bypassConfirmTries times ends the start with what to do,
// not a wait for the timeout.
func TestBypassConfirmationThatStaysIsReported(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusBlocked)
	f.setScreen("w1:p2", bypassConfirm(t))
	_, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1",
		"prompt": "fix the build", "permission_mode": proto.ModeBypass})
	if err == nil || !strings.Contains(err.Error(), "stayed on screen") {
		t.Fatalf("err %v", err)
	}
	if got := len(f.trace("pane.send_keys")); got != bypassConfirmTries {
		t.Errorf("chose Yes %d times", got)
	}
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Errorf("first message typed: %v", got)
	}
}

// A start that did not ask for bypass (no mode chosen: the launcher's own flag shows the confirmation) never gets it
// accepted: nothing is pressed, and the start says what to do (#3294).
func TestStartWithoutBypassLeavesTheConfirmationToThePerson(t *testing.T) {
	for _, mode := range []string{"", "default", "plan"} {
		t.Run("mode="+mode, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "proj"))
			startInBypassPane(t, f)
			params := map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build"}
			if mode != "" {
				params["permission_mode"] = mode
			}
			_, err := request(t, n, "session.start", params)
			if err == nil || !strings.Contains(err.Error(), "Bypass Permissions confirmation") {
				t.Fatalf("err %v", err)
			}
			if got := f.trace("pane.send_keys", "agent.prompt"); len(got) != 0 {
				t.Errorf("pressed or typed: %v", got)
			}
		})
	}
}

// A bypass start whose claude comes up blocked on any other dialog (here a permission Claude asks even in bypass)
// presses nothing: the dialog is the user's, answered from its screen card. The first message is held, the session
// listed as needing the user; once the dialog is answered and the box is back, the message is typed and goes in.
func TestFirstMessageHeldByADialogGoesInOnceItIsAnswered(t *testing.T) {
	withFirstAfterDialog(t, 10*time.Second, 20*time.Millisecond)
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusBlocked)
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.316-danger-placeholder-100.txt"))
	promptLands(t, f, home, cwd)
	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1",
		"prompt": "fix the build", "permission_mode": proto.ModeBypass}); err != nil {
		t.Fatal(err)
	}
	id := startedID(f)
	if p, ok := n.lostFirst.get(id); !ok || p.why != lostDialogHeld {
		t.Fatalf("lost mark %+v %v", p, ok)
	}
	heldPolls(t, f, 5) // the dialog stays a while: nothing pressed, nothing typed
	if got := f.trace("pane.send_keys", "agent.prompt"); len(got) != 0 {
		t.Fatalf("pressed or typed while the dialog was open: %v", got)
	}

	// Answered from the card (or at the computer): the box is back.
	f.setScreens("w1:p2", screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	startHerdr(f, "w1:p2", proto.StatusIdle)
	n.dialogWait.Wait()
	n.firstWait.Wait()
	assertTrace(t, f.trace("pane.send_keys", "agent.prompt"), []string{
		call("agent.prompt", map[string]any{"target": "w1:p2", "text": "fix the build"}),
	})
	if p, ok := n.lostFirst.get(id); ok {
		t.Errorf("still marked lost: %s", p.why)
	}
}

// A message that reaches the session while its first one waits for a dialog (sent from the phone once the dialog was
// answered, or typed at the computer) wins: the held first message is not typed as well.
func TestHeldFirstMessageGivesWayToAnotherMessage(t *testing.T) {
	withFirstAfterDialog(t, 10*time.Second, 20*time.Millisecond)
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusBlocked)
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.312-model-picker-162.txt"))
	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1",
		"prompt": "fix the build"}); err != nil {
		t.Fatal(err)
	}
	heldPolls(t, f, 2)
	writeUserLine(t, home, cwd, startedID(f), "something else")
	f.setScreens("w1:p2", screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	startHerdr(f, "w1:p2", proto.StatusIdle)
	n.dialogWait.Wait()
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Errorf("held first message typed after another one: %v", got)
	}
}

// A send that held the typing lock while the held first message waited for it, and left its own text in the box
// (its delivery not confirmed, so the transcript has no new message), keeps the held message out: the box is looked at
// again under the lock, and the first message is not pasted onto that draft and sent with it.
func TestHeldFirstMessageRechecksTheBoxUnderTheTypingLock(t *testing.T) {
	withFirstAfterDialog(t, 10*time.Second, 20*time.Millisecond)
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusBlocked)
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.312-model-picker-162.txt"))
	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1",
		"prompt": "fix the build"}); err != nil {
		t.Fatal(err)
	}
	id := startedID(f)

	// Another send takes the session's typing lock; then the dialog is answered and the box shows empty.
	unlock := n.typing.lock(id)
	reads := len(f.trace("pane.read"))
	f.setScreens("w1:p2", screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	startHerdr(f, "w1:p2", proto.StatusIdle)
	for deadline := time.Now().Add(5 * time.Second); len(f.trace("pane.read")) == reads; runtime.Gosched() {
		if time.Now().After(deadline) {
			unlock()
			t.Fatal("the held first message never looked at the box")
		}
	}
	// The other send leaves its text in the box and lets go.
	f.setScreens("w1:p2", screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi"))
	unlock()
	reads = len(f.trace("pane.read"))
	for deadline := time.Now().Add(5 * time.Second); len(f.trace("pane.read")) < reads+3; runtime.Gosched() {
		if time.Now().After(deadline) {
			t.Fatal("the held first message stopped looking at the box")
		}
	}
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Fatalf("held first message typed onto another draft: %v", got)
	}
	writeUserLine(t, home, cwd, id, "the other message") // it goes in after all: the held one gives way
	n.dialogWait.Wait()
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Errorf("held first message typed after another one: %v", got)
	}
}

// heldPolls waits until herdr has been asked calls more things since now: the held first message looks at the pane
// every firstAfterDialogPoll, so this lets a few of its polls go by without a fixed sleep.
func heldPolls(t *testing.T, f *fakeHerdr, calls int) {
	t.Helper()
	from := len(f.calls())
	for deadline := time.Now().Add(5 * time.Second); len(f.calls()) < from+calls; runtime.Gosched() {
		if time.Now().After(deadline) {
			t.Fatal("the held first message stopped looking at the pane")
		}
	}
}
