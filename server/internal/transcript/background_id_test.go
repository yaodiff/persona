package transcript

import (
	"testing"
	"time"
)

func TestLaunchedBackgroundID(t *testing.T) {
	for _, c := range []struct{ result, want string }{
		{"Command running in background with ID: b85polnoj. Output is being written to: /tmp/tasks/b85polnoj.output.", "b85polnoj"},
		{"Command running in background with ID: bash_12", "bash_12"},
		{"Async agent launched successfully.\nagentId: a1b2c3 (use it to check on the agent)", "a1b2c3"},
		{"Workflow launched in background. Task ID: w5k2nd1ap\nSummary: build", "w5k2nd1ap"},
		{"Monitor started (task bqke1prjm, expires in 30m unless stopped)", "bqke1prjm"},
		{"Command timed out and was moved to the background (ID: bmv1). Output is being written to: /tmp/x", "bmv1"},
		{"Command was manually backgrounded by user with ID: bmk9rnfdz. Output is being written to: /tmp/y", "bmk9rnfdz"},
		{"<task_id>bx1</task_id><status>running</status>", ""},
		{"Exit code 0", ""},
	} {
		if got := LaunchedBackgroundID(c.result); got != c.want {
			t.Errorf("%q: %q, want %q", c.result, got, c.want)
		}
	}
}

// Claude Code starts a line with a moved command's notice, alone or after the output so far; output that quotes one
// (grep hits, a numbered Read, a test table) launched nothing and must not leave a task running (#9091).
func TestMovedToBackgroundOnlyAtALineStart(t *testing.T) {
	for _, c := range []struct{ name, result, want string }{
		{"whole result", "Command did not complete within its 120s timeout and was moved to the background (ID: b1). Output is being written to: /tmp/tasks/b1.output", "b1"},
		{"after output", "building…\nok  pkg 1.2s\nCommand did not complete within its 120s timeout and was moved to the background (ID: b2). Output is being written to: /tmp/tasks/b2.output", "b2"},
		{"cwd line after", "Command did not complete within its 600s timeout and was moved to the background (ID: b3). Output is being written to: /tmp/tasks/b3.output\nSession cwd remains /home/me/project", "b3"},
		{"by hand after output", "step 1\nCommand was manually backgrounded by user with ID: b4. Output is being written to: /tmp/tasks/b4.output", "b4"},
		{"grep hit", "internal/transcript/transcript_test.go:645:\t\t`{\"content\":\"Command was manually backgrounded by user with ID: bmk9rnfdz. Output is being written to: /tmp/x\"}`", ""},
		{"numbered Read", "    12\u2192\t\t{\"Command timed out and was moved to the background (ID: bmv1). Output is being written to: /tmp/x\", \"bmv1\"},", ""},
		{"test table", "\t{\"timed out\", \"Command did not complete within its 30s timeout and was moved to the background (ID: b1).\", false},", ""},
		{"log line", "08:42:11 RES Command did not complete within its 600s timeout and was moved to the background (ID: b2h25b10v).", ""},
	} {
		if got := LaunchedBackgroundID(c.result); got != c.want {
			t.Errorf("%s: launched %q, want %q", c.name, got, c.want)
		}
		meta := &Meta{}
		meta.backgroundStarted("tu1", c.result, false, time.Unix(0, 0))
		want := 0
		if c.want != "" {
			want = 1
		}
		if len(meta.Background) != want || want == 1 && meta.Background[0].ID != c.want {
			t.Errorf("%s: background %+v, want %q", c.name, meta.Background, c.want)
		}
	}
}

// Only a foreground command Claude Code's timeout moved is marked: the node judges that kind stalled sooner (#16418).
// One started with run_in_background or backgrounded by hand is the agent's or the user's choice, and is not.
func TestMovedFromForegroundMarksOnlyTheTimeoutMove(t *testing.T) {
	ts := time.Unix(0, 0)
	moved := &Meta{}
	ParseLine(1, []byte(`{"type":"assistant","uuid":"a1","timestamp":"2026-11-26T08:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"cmp a.json | head -2"}}]}}`), moved)
	moved.backgroundStarted("tu1", "Command did not complete within its 120s timeout and was moved to the background (ID: b1). Output is being written to: /tmp/tasks/b1.output", false, ts)
	if len(moved.Background) != 1 || !moved.Background[0].MovedFromForeground {
		t.Fatalf("timeout move: %+v", moved.Background)
	}
	manual := &Meta{}
	manual.backgroundStarted("tu2", "Command was manually backgrounded by user with ID: b2. Output is being written to: /tmp/tasks/b2.output", false, ts)
	launched := &Meta{}
	launched.trackBackground("tu3", "Bash", []byte(`{"command":"sleep 600","run_in_background":true}`), ts)
	launched.backgroundStarted("tu3", "Command running in background with ID: b3. Output is being written to: /tmp/tasks/b3.output", false, ts)
	for name, m := range map[string]*Meta{"by hand": manual, "run_in_background": launched} {
		if len(m.Background) != 1 || m.Background[0].MovedFromForeground {
			t.Errorf("%s: %+v", name, m.Background)
		}
	}
}
