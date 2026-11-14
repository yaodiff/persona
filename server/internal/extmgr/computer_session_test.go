package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// What a label found ended means (#7590): Cua Driver says the same for a session that idled out and one the user
// stopped on the Mac's screen, so the manager's own record of its calls tells them apart.
func TestCuaSessionBookTellsIdleFromStop(t *testing.T) {
	now := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour) // a book running long enough to know
	cases := []struct {
		name  string
		setup func(b *cuaSessionBook)
		own   bool
		want  cuaVerdict
	}{
		{"used a minute ago: ended on purpose", func(b *cuaSessionBook) { b.used("mac", "persona-c1", now.Add(-time.Minute)) }, false, cuaStopped},
		{"used just under the idle time ago", func(b *cuaSessionBook) { b.used("mac", "persona-c1", now.Add(-cuaIdleTTL+time.Second)) }, false, cuaStopped},
		{"used the idle time ago: it idled out", func(b *cuaSessionBook) { b.used("mac", "persona-c1", now.Add(-cuaIdleTTL)) }, false, cuaRestart},
		{"used an hour ago", func(b *cuaSessionBook) { b.used("mac", "persona-c1", now.Add(-time.Hour)) }, false, cuaRestart},
		{"never seen, by a book running for an hour", func(*cuaSessionBook) {}, false, cuaRestart},
		{"never seen, by a book a minute old: cannot tell, so the user's", func(b *cuaSessionBook) { b.start = now.Add(-time.Minute) }, false, cuaStopped},
		{"never seen by a new book, the owner spoke since it began", func(b *cuaSessionBook) {
			b.start = now.Add(-time.Minute)
			b.ownerSpoke("persona-c1", now.Add(-30*time.Second))
		}, false, cuaRestart},
		{"the manager's own label, used a second ago", func(b *cuaSessionBook) { b.used("mac", "persona-apps", now.Add(-time.Second)) }, true, cuaRestart},
		{"another Mac's use of the label says nothing of this one's", func(b *cuaSessionBook) { b.used("mini", "persona-c1", now.Add(-time.Minute)) }, false, cuaRestart},
	}
	for _, c := range cases {
		b := &cuaSessionBook{start: old}
		c.setup(b)
		label := "persona-c1"
		if c.own {
			label = "persona-apps"
		}
		if got := b.ended("mac", label, c.own, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Stopped by the user: told once, and again on every call until the owner speaks in that conversation; then the next
// call starts it again. A word before the stop, or another conversation's, is not a go-ahead.
func TestCuaSessionBookStopWaitsForTheOwner(t *testing.T) {
	now := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)
	b := &cuaSessionBook{start: now.Add(-time.Hour)}
	b.ownerSpoke("persona-c1", now.Add(-2*time.Minute)) // what started the work
	b.used("mac", "persona-c1", now.Add(-time.Minute))
	if got := b.ended("mac", "persona-c1", false, now); got != cuaStopped {
		t.Fatalf("stopped while in use: %v", got)
	}
	// The model retries in the same turn, even after the idle time: still the user's stop.
	if got := b.ended("mac", "persona-c1", false, now.Add(10*time.Minute)); got != cuaStopped {
		t.Fatalf("retried without the owner's word: %v", got)
	}
	b.ownerSpoke("persona-other", now.Add(11*time.Minute))
	if got := b.ended("mac", "persona-c1", false, now.Add(12*time.Minute)); got != cuaStopped {
		t.Fatalf("another conversation's word: %v", got)
	}
	b.ownerSpoke("persona-c1", now.Add(13*time.Minute))
	if got := b.ended("mac", "persona-c1", false, now.Add(14*time.Minute)); got != cuaRestart {
		t.Fatalf("the owner spoke since: %v", got)
	}
	b.used("mac", "persona-c1", now.Add(14*time.Minute))
	// Used again and then stopped again: a new stop, and the earlier word does not cover it.
	if got := b.ended("mac", "persona-c1", false, now.Add(15*time.Minute)); got != cuaStopped {
		t.Fatalf("stopped again: %v", got)
	}
}

func TestCuaSessionBookForgetsOldLabels(t *testing.T) {
	now := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)
	b := &cuaSessionBook{start: now.Add(-48 * time.Hour)}
	b.used("mac", "persona-old", now.Add(-25*time.Hour))
	b.ownerSpoke("persona-old", now.Add(-25*time.Hour))
	b.used("mac", "persona-new", now)
	if _, ok := b.labels[cuaKey("mac", "persona-old")]; ok || len(b.labels) != 1 || len(b.spoke) != 0 {
		t.Fatalf("kept past a day: %v %v", b.labels, b.spoke)
	}
}

// Through the tools (#7590): an idled-out session is started again and the call goes through; the user's stop is told
// to the model with a sentence for the user, nothing runs and nothing is started again until the owner speaks; the
// manager's own label is started again whatever happened.
func TestComputerToolsAfterTheSessionEnded(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	_, seen := computerMac(t, h, "mac")
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.cuaSessions.start = time.Now().Add(-time.Hour)
	end := func(label string) {
		fakeNoLockMu.Lock()
		if fakeEnded["mac"] == nil {
			fakeEnded["mac"] = map[string]bool{}
		}
		fakeEnded["mac"][label] = true
		fakeNoLockMu.Unlock()
	}
	t.Cleanup(func() { fakeNoLockMu.Lock(); delete(fakeEnded, "mac"); fakeNoLockMu.Unlock() })
	const conv = "conv1234-5678"
	label := cuaConversationLabel(conv)
	state := json.RawMessage(`{"machine": "mac", "app": "Calculator"}`)
	toolsSince := func(from int) (tools []string) {
		_, calls := seen()
		for _, c := range calls[from:] {
			tools = append(tools, c.Tool+"@"+c.Session)
		}
		return tools
	}

	// Idled out: never used by this manager, which has run for an hour.
	end(label)
	if res := h.callComputerTool(ctx, conv, "computer_state", state); res.IsError || !strings.Contains(res.Content[0].Text, "AXButton") {
		t.Fatalf("an idled-out session is started again and read: %+v", res)
	}
	// (computer_state looks at the app's processes first: computer_window.go.)
	if got := strings.Join(toolsSince(0), " "); got != "list_windows@"+label+" start_session@"+label+" list_windows@"+label+" run_script@"+label+
		" get_window_state@"+label {
		t.Fatalf("start_session under the same label, then the call again: %s", got)
	}

	// The user pressed Stop on the Mac a moment after that read.
	end(label)
	_, before := seen()
	res := h.callComputerTool(ctx, conv, "computer_state", state)
	say := i18n.T(h.userLang(), "hub-computer.session_stopped", i18n.Args{"machine": "mac"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "stopped on its screen") || !strings.Contains(res.Content[0].Text, say) ||
		!strings.Contains(res.Content[0].Text, "Do not call computer_state or computer_run again in this turn") {
		t.Fatalf("the model is told it was the user's stop, with a sentence for the user: %+v", res)
	}
	steps := json.RawMessage(`{"machine": "mac", "app": "Calculator", "steps": [{"click": {"role": "button", "name": "7"}}]}`)
	if res := h.callComputerTool(ctx, conv, "computer_run", steps); !res.IsError || !strings.Contains(res.Content[0].Text, "stopped on its screen") {
		t.Fatalf("a retry in the same turn is told the same: %+v", res)
	}
	// Each call is still tried (Cua Driver refuses it while the session stays ended; one started again some other
	// way would answer), but nothing starts it again behind the user's stop.
	for _, c := range toolsSince(len(before)) {
		if c == "start_session@"+label {
			t.Fatalf("started again behind the user's stop: %v", toolsSince(len(before)))
		}
	}
	fakeNoLockMu.Lock()
	stillEnded := fakeEnded["mac"][label]
	fakeNoLockMu.Unlock()
	if !stillEnded {
		t.Fatal("the session the user stopped was started again")
	}

	// The owner answers to go on: the next call starts it again.
	h.OwnerSpoke(conv)
	_, before = seen()
	if res := h.callComputerTool(ctx, conv, "computer_run", steps); res.IsError {
		t.Fatalf("after the owner's word: %+v", res)
	}
	// Started again on the run's first call (its look at the app's processes, computer_window.go), then run.
	if got := toolsSince(len(before)); len(got) < 3 || got[1] != "start_session@"+label || got[len(got)-1] != "run_steps@"+label ||
		strings.Count(strings.Join(got, " "), "start_session") != 1 {
		t.Fatalf("started again, then run: %v", got)
	}

	// The manager's own label (its look at the apps for the protected-app check) ended a second after its last use:
	// no one stops it from the screen, so it is started again rather than refusing every run as unreadable.
	end("persona-apps")
	_, before = seen()
	if res := h.callComputerTool(ctx, conv, "computer_run", steps); res.IsError {
		t.Fatalf("the manager's own label ended: %+v", res)
	}
	if got := strings.Join(toolsSince(len(before)), " "); !strings.Contains(got, "start_session@persona-apps") || !strings.Contains(got, "run_steps@"+label) {
		t.Fatalf("its own label started again: %s", got)
	}
}

// The extensions' manager is what the assistant tells of the owner's words (assistant ownerHeard).
var _ interface{ OwnerSpoke(string) } = (*Manager)(nil)
