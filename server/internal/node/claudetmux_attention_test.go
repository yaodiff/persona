package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/tmux"
)

// A claude the user started in tmux that waits on a permission prompt is answered from the phone: its pane's screen is
// read and the keys go into it (#2562). Its row names the dialog the pane shows, never the computer-only sentence a
// claude outside any pane gets (#5965).
func TestTmuxClaudeWaitingOnADialogIsNotComputerOnly(t *testing.T) {
	old := paneDialogEvery
	paneDialogEvery = 0
	t.Cleanup(func() { paneDialogEvery = old })

	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	oldEnv := paneEnvOfPID
	paneEnvOfPID = func(int) map[string]string { return nil }
	t.Cleanup(func() { paneEnvOfPID = oldEnv })

	dialog := screenFixture(t, "claude-2.1.316-danger-rm-120.txt")
	want := screen.DialogHead(screen.Plain(dialog))
	if want == "" {
		t.Fatal("the fixture shows no dialog")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "screen"), []byte(dialog), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n[ \"$1\" = capture-pane ] && cat " + filepath.Join(dir, "screen") + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	n.hosts.Set(append(n.hosts.All(), &tmuxHost{c: tmux.Client{Bin: bin}})...)

	p := bgTranscript(t, home, home, sidA, "remove the build")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "cli", "waiting", inbox, map[string]any{"tmux": "orc:@11.%11"})

	var s proto.Session
	waitFor(t, "the dialog named", func() bool { s = byID(listSessions(t, n))[sidA]; return s.AttentionReason == want })
	if s.PaneID != "tmux:%11" || s.Capabilities == nil || !s.Capabilities.Terminal {
		t.Fatalf("not driven through its pane: %+v", s)
	}
	if s.Status != proto.StatusBlocked || s.AttentionReason != want {
		t.Fatalf("attention %q, want the dialog %q (status %s)", s.AttentionReason, want, s.Status)
	}

	// A resting claude whose pane shows a dialog its status file does not report is named the same way, and the row
	// returns to rest once the dialog is gone.
	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, map[string]any{"tmux": "orc:@11.%11"})
	wait := func(ok func(proto.Session) bool) proto.Session {
		t.Helper()
		var row proto.Session
		waitFor(t, "the row", func() bool { row = byID(listSessions(t, n))[sidA]; return ok(row) })
		return row
	}
	if s = wait(func(s proto.Session) bool { return s.AttentionReason == want }); s.Status != proto.StatusBlocked || s.AttentionReason != want {
		t.Fatalf("resting: attention %q, want the dialog %q (status %s)", s.AttentionReason, want, s.Status)
	}
	if err := os.WriteFile(filepath.Join(dir, "screen"), []byte(screenFixture(t, "claude-2.1.312-empty-162.txt")), 0o600); err != nil {
		t.Fatal(err)
	}
	if s = wait(func(s proto.Session) bool { return s.AttentionReason == "" }); s.Status == proto.StatusBlocked || s.AttentionReason != "" {
		t.Fatalf("dialog gone: attention %q (status %s)", s.AttentionReason, s.Status)
	}

	// A dialog the pane does not show (not read yet, or one the reader does not recognize) leaves the sentence the row
	// had: never an empty reason on a blocked row.
	writeOutsideClaude(t, home, sidA, "cli", "waiting", inbox, map[string]any{"tmux": "orc:@11.%11"})
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); <-tick.C {
		if s = byID(listSessions(t, n))[sidA]; s.Status != proto.StatusBlocked || s.AttentionReason != proto.AttentionClaudeComputer {
			t.Fatalf("no dialog on the pane: attention %q (status %s)", s.AttentionReason, s.Status)
		}
	}
}
