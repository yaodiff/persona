package node

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// Permission mode, effort and speed changes on an Orca terminal (#17042), against the fake runtime. The screens are
// what Orca 1.4.277's terminal.read gave for Claude Code 2.1.331 on an isolated runtime: the box's prompt row comes back
// as its bare ❯ and the box's text as the read's draft, the same for a typed draft and for Claude's grey suggestion.

var orcaRule = strings.Repeat("─", 120)

// orcaClaudeScreen is a resting Claude Code 2.1.331 on Orca 1.4.277: its last turn, the box (its prompt row as Orca
// leaves it, the bare ❯) and the footer.
func orcaClaudeScreen(footer string) string {
	return " ▐▛███▛█   Claude Code v2.1.331\n▝▜██████▀  Opus 5.5 · API Usage Billing\n ▝▝   ▝▝   /work/repo\n" +
		"❯ again\n● Hello there, all done.\n✻ Churned for 0s · done 9:16 AM\n" + orcaRule + "\n❯\n" + orcaRule + "\n" + footer + "\n"
}

const (
	orcaManualFooter = "  ⏸ manual mode on · ? for shortcuts"
	orcaEffortPicker = "❯ /effort\n" + "────────────────────────────────────────\n" + "  Effort\n" +
		"                             Faster                             Smarter\n" +
		"                             ──────────▲───────────────────────────────      Ultracode  off\n" +
		"                             low     medium     high     xhigh      max      Tab to toggle\n" +
		"  ←/→ to adjust · s to use this session only · Enter to save · Esc to cancel\n"
	orcaEffortDone = "❯ /effort\n  ⎿  Set effort level to medium (this session only): Balanced approach with standard implementation and testing\n"
)

// orcaWritten is what went into term_1 so far, one string per write.
func orcaWritten(o *orcarttest.Fake) []string {
	var out []string
	for _, w := range o.Writes() {
		if w.Handle == "term_1" && !w.Close {
			out = append(out, w.Text)
		}
	}
	return out
}

// orcaRead answers terminal.read with text and Orca's draft.
func orcaRead(text, draft string) map[string]any {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	t := map[string]any{"handle": "term_1", "status": "running", "source": "screen", "tail": lines}
	if draft != "" {
		t["draft"] = draft
	}
	return map[string]any{"terminal": t}
}

// orcaEffortScreens answers terminal.read like Claude Code: the box (with draft) until /effort and Enter went in, the
// picker until the session-only key, then the result over an empty box.
func orcaEffortScreens(o *orcarttest.Fake, draft string) {
	o.Handle("terminal.read", func(map[string]any) any {
		w := orcaWritten(o)
		opened := -1
		for i := 1; i < len(w); i++ {
			if w[i-1] == "/effort" && w[i] == "\r" {
				opened = i
				break
			}
		}
		switch {
		case opened < 0:
			return orcaRead(orcaClaudeScreen(orcaManualFooter), draft)
		case strings.Contains(strings.Join(w[opened:], "|"), "|s"):
			return orcaRead(orcaEffortDone+orcaRule+"\n❯\n"+orcaRule+"\n"+orcaManualFooter+"\n", "")
		}
		return orcaRead(orcaEffortPicker, "")
	})
}

// An effort change types /effort into an empty box, moves the slider and confirms for this session only ("s"); a box
// whose text Orca hands back as a draft refuses it with nothing typed. Without colors that is also Claude's grey
// suggestion (Orca's draft does not say which text is grey), and the refusal says so; the new-session tip, which Orca
// leaves in the row, is an empty box.
func TestOrcaEffortChange(t *testing.T) {
	n, o, _ := orcaSession(t)
	orcaEffortScreens(o, "")
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "effort": "medium"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/effort", "\r"}
	for _, k := range effortPickerKeys("medium") {
		b, _ := orcaKeyBytes(k)
		want = append(want, b)
	}
	want = append(want, "s")
	if got := orcaWritten(o); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("typed %q, want %q", got, want)
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}

	for name, draft := range map[string]string{"typed draft": "fix the flaky test", "Claude's suggestion": "run the tests"} {
		t.Run(name, func(t *testing.T) {
			n, o, _ := orcaSession(t)
			orcaEffortScreens(o, draft)
			_, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "effort": "high"})
			code, data := refusalOf(t, err)
			if code != screen.InputDraft || data["draft"] != draft || !strings.Contains(err.Error(), draft) {
				t.Fatalf("refusal %q %v: %v", code, data, err)
			}
			if w := orcaWritten(o); len(w) != 0 {
				t.Fatalf("typed into a box with text: %q", w)
			}
		})
	}

	// An Orca older than the draft field draws the box's text in the row: the same refusal.
	n, o, _ = orcaSession(t)
	o.SetScreen("term_1", strings.Replace(orcaClaudeScreen(orcaManualFooter), "\n❯\n", "\n❯ fix the flaky test\n", 1))
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "effort": "high"}); refusalCode(err) != screen.InputDraft {
		t.Fatalf("older Orca: %v", err)
	}
	// Claude's new-session tip stays in the row (Orca knows it is a placeholder): the box is empty.
	n, o, _ = orcaSession(t)
	orcaEffortScreens(o, "")
	tip := strings.Replace(orcaClaudeScreen(orcaManualFooter), "\n❯\n", "\n❯ Try \"fix typecheck errors\"\n", 1)
	if text, draft, err := n.promptDraft(context.Background(), "orca:tab1:leaf1"); err != nil || draft || text != "" {
		t.Fatalf("empty box: %q %v %v", text, draft, err)
	}
	o.Handle("terminal.read", func(map[string]any) any { return orcaRead(tip, "") })
	if _, draft, err := n.promptDraft(context.Background(), "orca:tab1:leaf1"); err != nil || draft {
		t.Fatalf("tip read as a draft: %v %v", draft, err)
	}
}

// A permission-mode change presses shift+tab until the footer shows the mode: no text goes in, so a draft (or Claude's
// suggestion) in the box does not stop it.
func TestOrcaPermissionModeChange(t *testing.T) {
	n, o, _ := orcaSession(t)
	footers := []string{orcaManualFooter, "  ⏵⏵ accept edits on (shift+tab to cycle)", "  ⏸ plan mode on (shift+tab to cycle)",
		"  ⏵⏵ auto mode on (shift+tab to cycle)"}
	o.Handle("terminal.read", func(map[string]any) any {
		presses := 0
		for _, w := range orcaWritten(o) {
			if w == "\x1b[Z" {
				presses++
			}
		}
		return orcaRead(orcaClaudeScreen(footers[presses%len(footers)]), "run the tests")
	})
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "permission_mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	if w := orcaWritten(o); strings.Join(w, "|") != "\x1b[Z|\x1b[Z" {
		t.Fatalf("pressed %q, want shift+tab twice", w)
	}
	if mode, _ := n.modesOf(sidA); mode != "plan" {
		t.Errorf("mode noted %q", mode)
	}
}

// A speed change on an Orca terminal submits /fast through Orca (a paste and a carriage return) and waits for Claude's
// own record of it; a box with text refuses it with nothing typed, naming the text.
func TestOrcaClaudeTerminalSpeed(t *testing.T) {
	n, _, o, procs, home := orcaNode(t)
	path := writeTranscript(t, home, orcaCwd, sidA, time.Now(), `{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","version":"2.1.331","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"Done"}],"usage":{"speed":"standard"}}}`)
	writeClaudePID(t, home, 4242, sidA)
	o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", "claude"))
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x",
		orcarttest.Agent("tab1:leaf1", "claude", "done")))
	procs.set("tab1:leaf1", orcaProc{pid: 4242, agent: "claude"})
	o.SetBoxScreen("term_1", orcaClaudeScreen(orcaManualFooter), "")
	row := byID(listSessions(t, n))[sidA]
	if row.PaneID != "orca:tab1:leaf1" || row.ServiceTier != "default" || strings.Join(speedIDs(row.ServiceTiers), ",") != "default,priority" {
		t.Fatalf("Orca row speeds: %q %q %v", row.PaneID, row.ServiceTier, row.ServiceTiers)
	}
	o.Handle("terminal.send", func(p map[string]any) any {
		w := orcaWritten(o)
		if len(w) >= 2 && w[len(w)-1] == "\r" && strings.Contains(w[len(w)-2], "/fast on") {
			appendLine(t, path, fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"<local-command-stdout>Fast mode ON · $8/$40 per Mtok</local-command-stdout>"}}`,
				time.Now().UTC().Format(time.RFC3339Nano)))
		}
		return map[string]any{"send": map[string]any{"handle": "term_1", "accepted": true, "bytesWritten": 1}}
	})
	o.Reset()
	if err := n.setSessionSpeed(context.Background(), sidA, "priority"); err != nil {
		t.Fatal(err)
	}
	if w := orcaWritten(o); len(w) != 2 || w[0] != pasteStart+"/fast on"+pasteEnd || w[1] != "\r" {
		t.Fatalf("typed %q", w)
	}
	if row := byID(listSessions(t, n))[sidA]; row.ServiceTier != "priority" {
		t.Fatalf("speed shown %q", row.ServiceTier)
	}

	o.SetBoxScreen("term_1", orcaClaudeScreen(orcaManualFooter), "run the tests")
	o.Reset()
	err := n.setSessionSpeed(context.Background(), sidA, "default")
	if code, data := refusalOf(t, err); code != screen.InputDraft || data["draft"] != "run the tests" {
		t.Fatalf("speed with text in the box: %q %v", code, data)
	}
	if w := orcaWritten(o); len(w) != 0 {
		t.Fatalf("typed into a box with text: %q", w)
	}
}

// #2553 on Orca: a dialog Orca does not report (it calls the agent done) holds a resting Claude; the node reads its
// plain screen and lists it as needing the user, the dialog's first line its reason, and done again once it is gone.
func TestOrcaDialogOnARestingClaudeNeedsYou(t *testing.T) {
	old := paneDialogEvery
	paneDialogEvery = 0
	t.Cleanup(func() { paneDialogEvery = old })
	n, o, _ := orcaSession(t)
	o.SetScreen("term_1", screenFixture(t, "claude-2.1.309-auto-mode-notice-120.txt"))
	waitRow := func(status string) proto.Session {
		t.Helper()
		var s proto.Session
		waitFor(t, "the row "+status, func() bool { s = paneRow(t, n, sidA); return s.Status == status })
		return s
	}
	s := waitRow(proto.StatusBlocked)
	if want := "We're changing auto mode to no longer charge for classifier requests in Claude Code."; s.AttentionReason != want {
		t.Fatalf("reason %q", s.AttentionReason)
	}
	o.SetBoxScreen("term_1", orcaClaudeScreen(orcaManualFooter), "")
	if s := waitRow(proto.StatusDone); s.AttentionReason != "" {
		t.Fatalf("reason left %q", s.AttentionReason)
	}
}
