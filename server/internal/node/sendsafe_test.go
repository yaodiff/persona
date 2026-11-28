package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

func screenFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "screen", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakePane is a herdr socket serving one pane: reads return screens[state], send_keys moves to the next state.
type fakePane struct {
	mu      sync.Mutex
	screens []string
	state   int
	keys    [][]string
	// clearAfter: after this many reads the pane moves to the next screen by itself (the TUI catching up).
	clearAfter   int
	reads        int
	readFailures int
	// cutOver / plainCutOver: a colored / plain read of more lines than this gets herdr 0.9.4's cut answer (the start
	// of it, no line end). With either set, a read is the screen's last `lines` rows, as herdr keeps the newest.
	cutOver, plainCutOver int
	// moved: what colored reads show instead of the screen (it changed between two reads).
	moved string
	// readLines: the lines each read asked for, colored ones as negative numbers.
	readLines []int
}

// bottom is text's last n rows, blank ones included, as herdr's grid read gives them.
func bottom(text string, n int) string {
	rows := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if n > 0 && n < len(rows) {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

func (f *fakePane) serve(t *testing.T) *herdr.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "hp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	ln, err := herdr.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var req struct {
				ID     string         `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			_ = json.Unmarshal(line, &req)
			f.mu.Lock()
			var result any = map[string]any{}
			switch req.Method {
			case "pane.read":
				if f.readFailures > 0 {
					f.readFailures--
					f.mu.Unlock()
					c.Close()
					continue
				}
				lines, _ := req.Params["lines"].(float64)
				ansi := req.Params["format"] == "ansi"
				f.readLines = append(f.readLines, map[bool]int{true: -int(lines), false: int(lines)}[ansi])
				text := f.screens[f.state]
				if f.cutOver > 0 || f.plainCutOver > 0 {
					if ansi && f.moved != "" {
						text = f.moved
					}
					if !ansi {
						text = screen.Plain(text) // a plain read has no colors
					}
					text = bottom(text, int(lines))
					if !ansi {
						text += "\n" // herdr ends a plain read's every row, the last one too; a colored read's last row has none
					}
					if limit := map[bool]int{true: f.cutOver, false: f.plainCutOver}[ansi]; limit > 0 && int(lines) > limit {
						b, _ := json.Marshal(map[string]any{"id": req.ID, "result": map[string]any{"read": map[string]any{"text": text}}})
						f.mu.Unlock()
						_, _ = c.Write(b[:len(b)/2])
						c.Close()
						continue
					}
				}
				result = map[string]any{"read": map[string]any{"text": text}}
				if f.reads++; f.clearAfter > 0 && f.reads >= f.clearAfter && f.state+1 < len(f.screens) {
					f.state++
				}
			case "pane.send_keys":
				var keys []string
				for _, k := range req.Params["keys"].([]any) {
					keys = append(keys, k.(string))
				}
				f.keys = append(f.keys, keys)
				if f.state+1 < len(f.screens) {
					f.state++
				}
			}
			f.mu.Unlock()
			b, _ := json.Marshal(map[string]any{"id": req.ID, "result": result})
			_, _ = c.Write(append(b, '\n'))
			c.Close()
		}
	}()
	return &herdr.Client{Socket: sock}
}

func refusalOf(t *testing.T, err error) (string, map[string]any) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) {
		t.Fatalf("not a refusal: %v", err)
	}
	data := map[string]any{}
	_ = json.Unmarshal(re.Data, &data)
	return re.Code, data
}

func TestCheckPaneRefusesDraftAndClearsWhenAsked(t *testing.T) {
	typed := screenFixture(t, "claude-2.1.312-typed-162.ansi")
	f := &fakePane{screens: []string{typed, screenFixture(t, "claude-2.1.312-cleared-162.ansi")}}
	n := &Node{hosts: herdrHosts(f.serve(t))}
	ctx := context.Background()

	code, data := refusalOf(t, n.checkPane(ctx, "w1:p1", "claude", ""))
	if code != "input_draft" || data["delivered"] != "none" || data["clearable"] != "fix the flaky test" {
		t.Fatalf("refusal %s %v", code, data)
	}
	// The user saw another draft: nothing is cleared.
	if code, _ := refusalOf(t, n.checkPane(ctx, "w1:p1", "claude", "something else")); code != "input_draft" || len(f.keys) != 0 {
		t.Fatalf("cleared a draft the user did not see: %s %v", code, f.keys)
	}
	if err := n.checkPane(ctx, "w1:p1", "claude", "fix the flaky test"); err != nil {
		t.Fatalf("after clearing: %v", err)
	}
	if len(f.keys) != 1 || f.keys[0][0] != "ctrl+k" || f.keys[0][1] != "ctrl+u" {
		t.Fatalf("keys %v", f.keys)
	}
}

// Two quick sends: the second found the first still in the box (typed, Enter not yet taken) and was refused as a
// draft, with an offer to clear it — the user's own first message. Now the sends to a session take turns and a box
// showing what was just typed is waited out.
func TestCheckPaneWaitsOutWhatWasJustTyped(t *testing.T) {
	typed := screenFixture(t, "claude-2.1.312-typed-162.ansi") // the box shows "fix the flaky test"
	f := &fakePane{screens: []string{typed, screenFixture(t, "claude-2.1.312-empty-162.ansi")}, clearAfter: 3}
	n := &Node{hosts: herdrHosts(f.serve(t))}
	n.typing.typed("w1:p1", "fix the flaky test\nand the second line of the message")
	if err := n.checkPane(context.Background(), "w1:p1", "claude", ""); err != nil {
		t.Fatalf("our own message taken for a draft: %v", err)
	}
	// Someone else's draft in another pane is still a draft.
	g := &fakePane{screens: []string{typed}}
	m := &Node{hosts: herdrHosts(g.serve(t))}
	m.typing.typed("w1:p2", "fix the flaky test")
	if code, _ := refusalOf(t, m.checkPane(context.Background(), "w1:p1", "claude", "")); code != "input_draft" {
		t.Fatalf("a draft in another pane: %s", code)
	}
	if !n.typing.ours("w1:p1", "[Pasted text #1 +500 lines] ") || n.typing.ours("w1:p9", "fix") {
		t.Fatal("paste placeholder / unknown pane")
	}
}

func TestSendsToOneSessionTakeTurns(t *testing.T) {
	var l typingLocks
	unlock := l.lock("s1")
	other := l.lock("s2") // another session does not wait
	other()
	got := make(chan struct{})
	go func() { defer l.lock("s1")(); close(got) }()
	select {
	case <-got:
		t.Fatal("a second send to the session did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("the second send never ran")
	}
}

func TestCheckPaneRefusesDialog(t *testing.T) {
	for _, name := range []string{"claude-2.1.312-trust-162.ansi", "claude-2.1.312-model-picker-162.ansi"} {
		f := &fakePane{screens: []string{screenFixture(t, name)}}
		n := &Node{hosts: herdrHosts(f.serve(t))}
		if code, _ := refusalOf(t, n.checkPane(context.Background(), "w1:p1", "claude", "")); code != "dialog_open" {
			t.Fatalf("%s: %s", name, code)
		}
	}
	f := &fakePane{screens: []string{screenFixture(t, "claude-2.1.312-empty-162.ansi")}}
	if err := (&Node{hosts: herdrHosts(f.serve(t))}).checkPane(context.Background(), "w1:p1", "claude", ""); err != nil {
		t.Fatalf("empty box: %v", err)
	}
	codex := &fakePane{screens: []string{"Allow command?\n\n› 1. Yes, proceed\n  2. No, and tell Codex what to do differently\n"}}
	if code, _ := refusalOf(t, (&Node{hosts: herdrHosts(codex.serve(t))}).checkPane(context.Background(), "w1:p1", "codex", "")); code != "dialog_open" {
		t.Fatal(code)
	}
}

func TestSentLogOnce(t *testing.T) {
	var l sentLog
	var typed atomic.Int32
	deliver := func() error { typed.Add(1); return nil }
	if err := l.once("m1", deliver); err != nil || l.once("m1", deliver) != nil || typed.Load() != 1 {
		t.Fatalf("a delivered message is typed once, got %d", typed.Load())
	}
	// Refused: nothing typed, so the retry runs.
	refuse := func() error { return refusal("input_draft", "x", nil) }
	_ = l.once("m2", refuse)
	if err := l.once("m2", deliver); err != nil || typed.Load() != 2 {
		t.Fatalf("retry after a refusal: %v %d", err, typed.Load())
	}
	// Failed while typing: it may be there, the retry reports the same instead of typing again.
	_ = l.once("m3", func() error { return uncertain(errors.New("socket closed")) })
	if err := l.once("m3", deliver); err == nil || typed.Load() != 2 {
		t.Fatalf("retry after an uncertain send: %v %d", err, typed.Load())
	}
	// A retry while the first is still typing waits for it.
	release := make(chan struct{})
	go func() { _ = l.once("m4", func() error { <-release; typed.Add(1); return nil }) }()
	time.Sleep(20 * time.Millisecond)
	done := make(chan error)
	go func() { done <- l.once("m4", deliver) }()
	close(release)
	if err := <-done; err != nil || typed.Load() != 3 {
		t.Fatalf("concurrent retry: %v %d", err, typed.Load())
	}
	// No id: every call sends.
	_ = l.once("", deliver)
	_ = l.once("", deliver)
	if typed.Load() != 5 {
		t.Fatal(typed.Load())
	}
}

func TestDialogKeysCheckRevision(t *testing.T) {
	trust := screenFixture(t, "claude-2.1.312-trust-162.txt")
	f := &fakePane{screens: []string{trust}}
	n := &Node{hosts: herdrHosts(f.serve(t)), agents: map[string]host.Agent{"s1": {PaneID: "w1:p1"}}}
	ctx := context.Background()
	rev := screenDialogRev(t, trust)
	keys, pre, err := n.dialogKeys(ctx, "s1", rev, "2")
	if err != nil || strings.Join(keys, ",") != "down,enter" || pre != nil {
		t.Fatalf("keys %v %v %v", keys, pre, err)
	}
	// The cursor moved on the computer: same dialog, keys from where it is now.
	moved := strings.Replace(strings.Replace(trust, " ❯ No, exit", "   No, exit", 1), "   Yes, I trust this folder", " ❯ Yes, I trust this folder", 1)
	f.screens[0] = moved
	if keys, _, err := n.dialogKeys(ctx, "s1", rev, "2"); err != nil || strings.Join(keys, ",") != "enter" {
		t.Fatalf("after the cursor moved: %v %v", keys, err)
	}
	// Answered at the computer: the screen moved on, nothing is pressed.
	f.screens[0] = screenFixture(t, "claude-2.1.312-empty-162.txt")
	if _, _, err := n.dialogKeys(ctx, "s1", rev, "2"); err == nil {
		t.Fatal("answered into a changed screen")
	} else if code, _ := refusalOf(t, err); code != "prompt_changed" {
		t.Fatal(code)
	}
}

// #6318: a pane full of CJK text whose 40 plain rows herdr cannot send whole (8 KB). The card and its answer both read
// the most bottom rows that fit, so they see the same dialog, agree on its revision, and the answer goes in; a screen
// that moved on is still refused.
func TestDialogCardAndAnswerOnACutPane(t *testing.T) {
	questions := screenFixture(t, "claude-2.1.318-questions-wrapped-100.txt")
	cjk := strings.Repeat("一行很长的中文输出，说明这次发布要注意的事\n", 40)
	f := &fakePane{screens: []string{cjk + questions}, plainCutOver: 30}
	n := &Node{hosts: herdrHosts(f.serve(t)), agents: map[string]host.Agent{"s1": {PaneID: "w1:p1"}}}
	ctx := context.Background()
	text, _, err := n.screen(ctx, "s1", screen.DialogLines, "", false) // what session.screen gives the hub for its card
	if err != nil {
		t.Fatalf("the card's read: %v (reads %v)", err, f.readLines)
	}
	rev := dialogRev(text, screen.DialogLines, "", "")
	if d := screen.ParseDialog(text); rev == "" || d == nil || len(d.Options) != 4 || !strings.Contains(d.Question, "你希望先切流量") {
		t.Fatalf("the card's dialog from the bottom rows: rev %q, %+v", rev, d)
	}
	if keys, _, err := n.dialogKeys(ctx, "s1", rev, "2"); err != nil || strings.Join(keys, ",") != "2" {
		t.Fatalf("the answer: %v %v (reads %v)", keys, err, f.readLines)
	}
	// Fewer rows fit when the answer comes (a row grew by a few bytes): the dialog is still shown whole, so it is the
	// same dialog with the same revision.
	f.plainCutOver = 24
	if keys, _, err := n.dialogKeys(ctx, "s1", rev, "1"); err != nil || strings.Join(keys, ",") != "1" {
		t.Fatalf("the answer with fewer rows: %v %v (reads %v)", keys, err, f.readLines)
	}
	f.screens[0] = cjk + screenFixture(t, "claude-2.1.312-empty-162.txt")
	if _, _, err := n.dialogKeys(ctx, "s1", rev, "2"); err == nil {
		t.Fatal("answered into a screen that moved on")
	} else if code, _ := refusalOf(t, err); code != screen.PromptChanged {
		t.Fatal(code)
	}
	// A dialog taller than every window that fits (a permission whose title and command are above its last 12 rows):
	// no card is made from part of it, and an answer presses nothing, since another command could end the same way.
	danger := screenFixture(t, "claude-2.1.316-danger-rm-120.txt")
	g := &fakePane{screens: []string{cjk + danger}, plainCutOver: 12}
	m := &Node{hosts: herdrHosts(g.serve(t)), agents: map[string]host.Agent{"s1": {PaneID: "w1:p1"}}}
	if text, _, err := m.screen(ctx, "s1", screen.DialogLines, "", false); err == nil {
		t.Fatalf("a card's read of a cut permission: %q", text)
	}
	keys, _, err := m.dialogKeys(ctx, "s1", dialogRev(danger, screen.DialogLines, "", ""), "1")
	if code, _ := refusalOf(t, err); code != notDelivered || len(keys) > 0 || len(g.keys) > 0 || !strings.Contains(err.Error(), "在电脑上回答") {
		t.Fatalf("answered a cut permission: %v %v, keys pressed %v", keys, err, g.keys)
	}
	// A review page too long to read after the last answer: not submitted unseen, and the answer says so.
	review := strings.Replace(screenFixture(t, "claude-2.1.316-multiselect-review-100.txt"), "\nReady to submit",
		strings.Repeat("\n ● 另一道题\n   → 一个回答", 4)+"\n\nReady to submit", 1) // taller than the 12 rows that fit
	r := &fakePane{screens: []string{cjk + review}, plainCutOver: 12}
	if err := (&Node{hosts: herdrHosts(r.serve(t))}).submitReview(ctx, "w1:p1"); err == nil || !strings.Contains(err.Error(), "Submit answers") || len(r.keys) > 0 {
		t.Fatalf("a cut review page: %v, keys %v", err, r.keys)
	}
}

// #5538: the card is made from the screen at one width and answered after the pane was resized (a phone or the web
// viewing it): the same dialog, so the answer goes in. A dialog that really changed is still refused, with both
// revisions in the refusal. An older hub's card carries the old formula, which the node still takes.
func TestDialogKeysAcrossWidths(t *testing.T) {
	wide := screenFixture(t, "claude-2.1.318-questions-wrapped-100.txt")
	f := &fakePane{screens: []string{wide}}
	var logs bytes.Buffer
	n := &Node{hosts: herdrHosts(f.serve(t)), agents: map[string]host.Agent{"s1": {PaneID: "w1:p1"}}, logger: log.New(&logs, "", 0)}
	ctx := context.Background()
	rev := dialogRev(wide, screen.DialogLines, "", "") // what session.screen gives the hub for its card
	if rev == "" || rev != screenDialogRev(t, wide) {
		t.Fatalf("session.screen's dialog_rev %q", rev)
	}
	for _, other := range [][3]any{{screen.DialogLines, "visible", "ansi"}, {60, "", ""}, {screen.DialogLines, "recent", ""}} {
		if r := dialogRev(wide, other[0].(int), other[1].(string), other[2].(string)); r != "" {
			t.Errorf("dialog_rev for a read dialogKeys does not make (%v): %q", other, r)
		}
	}

	f.screens[0] = screenFixture(t, "claude-2.1.318-questions-wrapped-72.txt")
	if keys, _, err := n.dialogKeys(ctx, "s1", rev, "2"); err != nil || strings.Join(keys, ",") != "2" {
		t.Fatalf("resized: %v %v", keys, err)
	}

	f.screens[0] = wide
	legacy := screen.ParseDialog(wide).LegacyRev
	if keys, _, err := n.dialogKeys(ctx, "s1", legacy, "1"); err != nil || strings.Join(keys, ",") != "1" {
		t.Fatalf("an older hub's card: %v %v", keys, err)
	}

	f.screens[0] = screenFixture(t, "claude-2.1.318-questions-wrapped-changed-100.txt")
	_, _, err := n.dialogKeys(ctx, "s1", rev, "2")
	code, data := refusalOf(t, err)
	seen := screenDialogRev(t, f.screens[0])
	if code != screen.PromptChanged || data["rev"] != rev || data["seen_rev"] != seen || data["seen_options"] != float64(4) ||
		!strings.Contains(data["seen_question"].(string), "回归") {
		t.Fatalf("changed: %s %v", code, data)
	}
	// The node's log says it too, with the pane the answer read and the agent's (the card's) pane.
	if l := logs.String(); !strings.Contains(l, "prompt_changed: the card's dialog "+rev+", "+seen+" in pane w1:p1 (the agent's pane: w1:p1)") {
		t.Fatalf("log %q", l)
	}
	if len(f.keys) != 0 {
		t.Fatalf("keys pressed: %v", f.keys)
	}
	f.screens[0] = screenFixture(t, "claude-2.1.312-empty-162.txt")
	if _, _, err := n.dialogKeys(ctx, "s1", rev, "2"); err == nil {
		t.Fatal("answered with no dialog on screen")
	} else if code, data := refusalOf(t, err); code != screen.PromptChanged || data["rev"] != rev || data["seen_rev"] != nil {
		t.Fatalf("gone: %s %v", code, data)
	}
}

// #5265: Claude Code 2.1.322's safety check with its countdown. The card is made at one width; the answer comes a few
// seconds later (the countdown moved) and after the phone's terminal view narrowed the pane (the countdown wrapped):
// the same dialog, so the key goes in. Another command at the same place is refused, nothing pressed.
func TestDialogKeysWhileTheCountdownRuns(t *testing.T) {
	wide := screenFixture(t, "claude-2.1.322-danger-shell-120.txt")
	f := &fakePane{screens: []string{wide}}
	n := &Node{hosts: herdrHosts(f.serve(t)), agents: map[string]host.Agent{"s1": {PaneID: "w1:p1"}}}
	ctx := context.Background()
	rev := dialogRev(wide, screen.DialogLines, "", "") // the card's
	for _, name := range []string{"later-120", "57", "later-57", "40"} {
		f.screens[0] = screenFixture(t, "claude-2.1.322-danger-shell-"+name+".txt")
		if keys, _, err := n.dialogKeys(ctx, "s1", rev, "1"); err != nil || strings.Join(keys, ",") != "1" {
			t.Fatalf("%s: %v %v", name, keys, err)
		}
	}
	narrow := screenFixture(t, "claude-2.1.322-danger-shell-later-57.txt")
	at := strings.LastIndex(narrow, "epsilon_name") // the command in the dialog's box
	f.screens[0] = narrow[:at] + "omega_name" + narrow[at+len("epsilon_name"):]
	if _, _, err := n.dialogKeys(ctx, "s1", rev, "1"); err == nil {
		t.Fatal("answered another command")
	} else if code, _ := refusalOf(t, err); code != screen.PromptChanged {
		t.Fatal(code)
	}
	if len(f.keys) != 0 {
		t.Fatalf("keys pressed: %v", f.keys)
	}
}

func screenDialogRev(t *testing.T, s string) string {
	t.Helper()
	d := screen.ParseDialog(s)
	if d == nil {
		t.Fatal("no dialog")
	}
	return d.Rev
}

// A send asked to close a notice first presses nothing on a screen read only by its bottom rows: what looks like a
// notice there may be a dialog whose head is above them (#6308).
func TestDismissNoticeNeedsTheWholeScreen(t *testing.T) {
	notice := screenFixture(t, "claude-2.1.309-auto-mode-notice-120.txt")
	f := &fakePane{screens: []string{strings.Repeat("一行很长的中文输出\r\n", 40) + notice}, cutOver: 10, plainCutOver: 24}
	err := (&Node{hosts: herdrHosts(f.serve(t))}).checkPaneFor(context.Background(), "w1:p1", "claude", sendOpts{DismissNotice: true})
	if code, data := refusalOf(t, err); code != "dialog_open" || data["notice"] == true || len(f.keys) != 0 {
		t.Fatalf("%s %v, keys %v", code, data, f.keys)
	}
}

// A prompt left in the input box (seen once with an image: `[Image #1]` and the text, the agent done) gets one Enter;
// a box that empties, or shows someone else's text, gets none. No transcript to watch: one look.
func TestEnsureSubmittedPressesEnterOnceForAStuckPrompt(t *testing.T) {
	withSubmitGrace(t, 100*time.Millisecond)
	typed := screenFixture(t, "claude-2.1.312-typed-162.ansi") // the box shows "fix the flaky test"
	empty := screenFixture(t, "claude-2.1.312-empty-162.ansi")
	ctx := context.Background()

	stuck := &fakePane{screens: []string{typed, empty}}
	n := &Node{hosts: herdrHosts(stuck.serve(t))}
	n.ensureSubmitted(ctx, "w1:p1", "fix the flaky test\n\n<attached_images>\n/tmp/a.jpg\n</attached_images>", time.Now(), nil)
	if len(stuck.keys) != 1 || stuck.keys[0][0] != "enter" {
		t.Fatalf("stuck prompt: keys %v", stuck.keys)
	}

	// The paste lands late: right after the prompt the box still looks empty, then holds the prompt (the 11-01 miss).
	late := &fakePane{screens: []string{empty, typed}}
	late.state = 1
	l := &Node{hosts: herdrHosts(late.serve(t))}
	l.ensureSubmitted(ctx, "w1:p1", "fix the flaky test", time.Now(), nil)
	if len(late.keys) != 1 || late.keys[0][0] != "enter" {
		t.Fatalf("late paste: keys %v", late.keys)
	}

	taken := &fakePane{screens: []string{empty}} // Claude took it by the time the grace ends
	m := &Node{hosts: herdrHosts(taken.serve(t))}
	m.ensureSubmitted(ctx, "w1:p1", "fix the flaky test", time.Now(), nil)
	if len(taken.keys) != 0 {
		t.Fatalf("pressed enter on a prompt Claude took: %v", taken.keys)
	}

	other := &fakePane{screens: []string{typed}}
	o := &Node{hosts: herdrHosts(other.serve(t))}
	o.ensureSubmitted(ctx, "w1:p1", "something else entirely", time.Now(), nil)
	if len(other.keys) != 0 {
		t.Fatalf("pressed enter on someone else's draft: %v", other.keys)
	}

	// A pane herdr cannot send whole even without colors: its bottom rows still show the stuck prompt (#6308).
	cut := &fakePane{screens: []string{strings.Repeat("一行很长的中文输出\r\n", 40) + typed}, cutOver: 10, plainCutOver: 24}
	c := &Node{hosts: herdrHosts(cut.serve(t))}
	c.ensureSubmitted(ctx, "w1:p1", "fix the flaky test", time.Now(), nil)
	if len(cut.keys) != 1 || cut.keys[0][0] != "enter" {
		t.Fatalf("stuck prompt on a cut pane: keys %v (reads %v)", cut.keys, cut.readLines)
	}
}

func TestStillPromptedIgnoresChips(t *testing.T) {
	for _, c := range []struct {
		box, prompted string
		want          bool
	}{
		{"[Image #1]What two colors <attached_images> </attached_images>", "What two colors are in this picture?\n\n<attached_images>\n/x.jpg\n</attached_images>", true},
		{"[Pasted text #1 +40 lines]", "a long paste", false},
		{"fix the flaky test", "fix the flaky test", true},
		{"[Image #1] fix the flaky test", "\n\nfix the flaky test\n\n<attached_images>\n/x.jpg\n</attached_images>", true}, // its first words, not a blank line
		{"[Image #1]", "<attached_images>\n/x.jpg\n</attached_images>", false},                                             // only a picture: nothing to tell by
		{"fix", "fix the flaky test", false},
		{"", "anything", false},
	} {
		if got := stillPrompted(c.box, c.prompted); got != c.want {
			t.Errorf("stillPrompted(%q, %q) = %v", c.box, c.prompted, got)
		}
	}
}

// A prompt of pictures only has no words to tell it by, but its box shows the path: line of each photo the send just
// saved (keepImagePaths), which nobody else types: that box is the prompt's. A chip alone, another photo's path, or
// words in front of the block are not.
func TestPendingPromptKnowsPicturesOnlyByTheirPaths(t *testing.T) {
	prompted := keepImagePaths("\n\n<attached_images>\n/u/2026-11-17/ab12cd34.png\n</attached_images>")
	for _, c := range []struct {
		box  string
		want bool
	}{
		{"[Image #11]<attached_images>\n  path:/u/2026-11-17/ab12cd34.png\n  </attached_images>", true},
		{"[Image #1]", false}, // a chip alone: anyone's paste
		{"[Image #1]<attached_images>\n  path:/u/2026-11-17/ef56ab78.png\n  </attached_images>", false},
		{"look at this [Image #1]<attached_images>\n  path:/u/2026-11-17/ab12cd34.png\n  </attached_images>", false},
	} {
		if got := pendingPrompt(c.box, prompted); got != c.want {
			t.Errorf("pendingPrompt(%q, %q) = %v", c.box, prompted, got)
		}
	}
}

// herdr 0.9.4 loses an answer over 8 KB, so a 60-line colored read of a wide pane with a status bar never arrived and
// the check went on without colors, where Claude's grey suggestion is a draft: every send to it was refused, and Clear
// and Send failed again (#6039). The screen is now read without colors and its bottom rows with them (spliceRead): the
// suggestion is told apart, a typed draft is still one, dialogs are judged on the whole screen. When even the plain
// screen is cut (#6308), its bottom rows are judged when they show the box or a dialog (bottomRead).
func TestCheckPaneReadsTheBoxDespiteCutAnswers(t *testing.T) {
	ctx := context.Background()
	check := func(f *fakePane, clear string) error {
		t.Helper()
		return (&Node{hosts: herdrHosts(f.serve(t))}).checkPane(ctx, "w1:p1", "claude", clear)
	}
	suggestion := screenFixture(t, "claude-2.1.312-suggestion-171.ansi")
	f := &fakePane{screens: []string{suggestion}, cutOver: 20}
	if err := check(f, ""); err != nil {
		t.Fatalf("a suggestion refused when the big read was cut: %v (reads %v)", err, f.readLines)
	}
	if len(f.readLines) < 3 || f.readLines[0] != -60 || f.readLines[1] != 60 || f.readLines[2] != -20 {
		t.Fatalf("reads %v", f.readLines)
	}
	// A typed draft is still one.
	typed := screenFixture(t, "claude-2.1.312-typed-162.ansi")
	if code, data := refusalOf(t, check(&fakePane{screens: []string{typed}, cutOver: 20}, "")); code != "input_draft" || data["clearable"] != "fix the flaky test" {
		t.Fatalf("typed: %s %v", code, data)
	}
	// The box above the colored rows (a menu of ten rows under it): its rows have no colors, so it is a draft.
	rule := strings.Repeat("─", 30)
	menu := rule + "\r\n❯ /re\r\n" + rule + strings.Repeat("\r\n  /review   review a pull request", 12)
	if code, _ := refusalOf(t, check(&fakePane{screens: []string{menu}, cutOver: 10}, "")); code != "input_draft" {
		t.Fatalf("a box above the colored rows: %s", code)
	}
	// The screen moved between the two reads: no colors at all, the text counts as a draft.
	if code, _ := refusalOf(t, check(&fakePane{screens: []string{suggestion}, cutOver: 20, moved: typed}, "")); code != "input_draft" {
		t.Fatalf("moved: %s", code)
	}
	// A dialog is judged on the whole screen: the rewind list's cursor row is above the colored rows, yet it is no
	// notice that Enter closes.
	list := screenFixture(t, "claude-2.1.318-rewind-list-middle-119.txt")
	if code, data := refusalOf(t, check(&fakePane{screens: []string{list}, cutOver: 10}, "")); code != "dialog_open" || data["notice"] == true {
		t.Fatalf("rewind list: %s %v", code, data)
	}
	// The plain read is cut too (a wide pane full of CJK text): the bottom rows that fit are judged when they show the
	// box, which Claude draws only while no dialog has the keyboard (bottomRead). An empty box with its suggestion
	// takes the message; a typed draft and the box over a menu are still drafts.
	cjk := strings.Repeat("一行很长的中文输出\r\n", 40)
	h := &fakePane{screens: []string{cjk + suggestion}, cutOver: 20, plainCutOver: 30}
	if err := check(h, ""); err != nil {
		t.Fatalf("plain cut, the box in the bottom rows: %v (reads %v)", err, h.readLines)
	}
	if code, data := refusalOf(t, check(&fakePane{screens: []string{cjk + typed}, cutOver: 20, plainCutOver: 30}, "")); code != "input_draft" || data["clearable"] != "fix the flaky test" {
		t.Fatalf("plain cut, typed: %s %v", code, data)
	}
	if code, _ := refusalOf(t, check(&fakePane{screens: []string{menu}, cutOver: 10, plainCutOver: 30}, "")); code != "input_draft" {
		t.Fatalf("plain cut, the box over a menu: %s", code)
	}
	// A dialog in the bottom rows refuses as one.
	if code, _ := refusalOf(t, check(&fakePane{screens: []string{cjk + list}, cutOver: 10, plainCutOver: 30}, "")); code != "dialog_open" {
		t.Fatalf("plain cut, a dialog: %s", code)
	}
	// No box and no dialog in any bottom rows that fit (a box above a long menu): nothing is known, nothing typed.
	longMenu := rule + "\r\n❯ /re\r\n" + rule + strings.Repeat("\r\n  /review   review a pull request", 40)
	if code, _ := refusalOf(t, check(&fakePane{screens: []string{longMenu}, cutOver: 10, plainCutOver: 16}, "")); code != notDelivered {
		t.Fatalf("plain cut, no box in the rows that fit: %s", code)
	}
	// The screen changes after the read that showed the box (a long menu pushes it out): the newer read the splice
	// makes lines up and shows neither, so nothing is typed.
	pushed := &fakePane{screens: []string{cjk + suggestion, longMenu}, clearAfter: 1, cutOver: 10, plainCutOver: 30}
	if code, _ := refusalOf(t, check(pushed, "")); code != notDelivered {
		t.Fatalf("the box pushed out after the first read: %s (reads %v)", code, pushed.readLines)
	}
	// No colored rows fit at all: a fresh plain read of the bottom rows is judged, without colors (an empty box takes
	// the message; any text in it would be a draft).
	empty := screenFixture(t, "claude-2.1.312-empty-162.ansi")
	plainOnly := &fakePane{screens: []string{cjk + empty}, cutOver: 2, plainCutOver: 30}
	if err := check(plainOnly, ""); err != nil {
		t.Fatalf("plain rows only, an empty box: %v (reads %v)", err, plainOnly.readLines)
	}
	if code, _ := refusalOf(t, check(&fakePane{screens: []string{cjk + suggestion}, cutOver: 2, plainCutOver: 30}, "")); code != "input_draft" {
		t.Fatalf("plain rows only, a suggestion without colors: %s", code)
	}
	// Only 12 plain rows fit: the colored rows spliced on are no more than those (a plain read's last line end is no
	// row), so the suggestion is still told from a draft.
	small := &fakePane{screens: []string{cjk + suggestion}, cutOver: 10, plainCutOver: 12}
	if err := check(small, ""); err != nil {
		t.Fatalf("12 rows: %v (reads %v)", err, small.readLines)
	}
	// A dialog seen only in the bottom rows is never a notice that Enter closes: its head may be above them.
	notice := screenFixture(t, "claude-2.1.309-auto-mode-notice-120.txt")
	if code, data := refusalOf(t, check(&fakePane{screens: []string{notice}, cutOver: 10}, "")); code != "dialog_open" || data["notice"] != true {
		t.Fatalf("the notice read whole: %s %v", code, data)
	}
	if code, data := refusalOf(t, check(&fakePane{screens: []string{cjk + notice}, cutOver: 10, plainCutOver: 24}, "")); code != "dialog_open" || data["notice"] == true {
		t.Fatalf("the notice in the bottom rows: %s %v", code, data)
	}
	// A short session in a tall pane: the box, then many blank rows. The colored rows reach past them to the box.
	tall := strings.Replace(typed, "\r\n", "\r\n", -1) + strings.Repeat("\r\n", 25)
	if code, data := refusalOf(t, check(&fakePane{screens: []string{tall}, cutOver: 40}, "")); code != "input_draft" || data["clearable"] != "fix the flaky test" {
		t.Fatalf("blank rows under a typed box: %s %v", code, data)
	}
	tallSuggestion := suggestion + strings.Repeat("\r\n", 25)
	if err := check(&fakePane{screens: []string{tallSuggestion}, cutOver: 50}, ""); err != nil {
		t.Fatalf("blank rows under a suggestion: %v", err)
	}
	// An idle Claude's empty box: the colored row has a no-break space after the ❯ that the plain one has not.
	idle := screenFixture(t, "claude-2.1.318-rewind-idle-before-119.ansi")
	g := &fakePane{screens: []string{idle}, cutOver: 20}
	if err := check(g, ""); err != nil {
		t.Fatalf("an empty box: %v (reads %v)", err, g.readLines)
	}
	if len(g.readLines) != 3 {
		t.Fatalf("the splice did not match the first time: reads %v", g.readLines)
	}
}
