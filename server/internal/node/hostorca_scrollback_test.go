package node

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Scrolling up on an Orca terminal (#17048): what the client shows above the screen comes from Orca's output tail and,
// beyond the part one read sends, its log by cursor where that agrees; where Orca's kept output begins is said.

func orcaLines(from, to int, f string) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf(f, i))
	}
	return out
}

// screenBefore pages a session through session.screen.before.
func orcaBefore(t *testing.T, n *Node, anchor []string, lines, loaded int) map[string]any {
	t.Helper()
	res, err := request(t, n, "session.screen.before", map[string]any{"session_id": sidA, "anchor": anchor, "lines": lines,
		"loaded": loaded, "format": "ansi"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A short shell: the output above the screen comes page by page, and its first line is where the output began
// (nothing dropped: no "older output isn't kept"). Blank lines the screen read leaves out are kept above it.
func TestOrcaScrollbackPagesTheTailAboveTheScreen(t *testing.T) {
	n, o, _ := orcaSession(t)
	tail := append(orcaLines(1, 40, "out %d"), "", "out 41", "out 42", "$ ")
	o.SetOutput("term_1", tail, false, tail[:len(tail)-1], len(tail)-1)
	o.SetScreen("term_1", strings.Join(append(orcaLines(31, 40, "out %d"), "out 41", "out 42", "$"), "\n")+"\n")

	got := orcaBefore(t, n, []string{"out 31", "out 32"}, 20, 13)
	if got["text"] != strings.Join(orcaLines(11, 30, "out %d"), "\n") || got["has_more"] != true || got["dropped"] != false {
		t.Fatalf("first page %v", got)
	}
	got = orcaBefore(t, n, []string{"out 11", "out 12"}, 20, 33)
	if got["text"] != strings.Join(orcaLines(1, 10, "out %d"), "\n") || got["has_more"] != false || got["dropped"] != false ||
		got["at_limit"] != false {
		t.Fatalf("top %v", got)
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("invalid requests: %v", inv)
	}

	// "recent" (an app polling the screen with its scrollback): the output above, then the screen as read.
	res, err := request(t, n, "session.screen", map[string]any{"session_id": sidA, "source": "recent", "lines": 60})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(append(append(orcaLines(1, 30, "out %d"), orcaLines(31, 42, "out %d")...), "$"), "\n") + "\n"
	if res["text"] != want {
		t.Fatalf("recent = %q", res["text"])
	}
	// A screen check of the node's own reads the screen alone, as before.
	if text, err := n.readPane(t.Context(), "orca:tab1:leaf1", "recent", 60); err != nil || strings.Contains(text, "out 1\n") {
		t.Fatalf("the host's recent read is the screen: %q, %v", text, err)
	}
}

// A long build log: one tail read sends 32 KiB, and the log by cursor (plain output, the same lines) reaches further
// back, to the oldest line Orca keeps; reaching it says older output isn't kept.
func TestOrcaScrollbackReachesBackThroughTheLog(t *testing.T) {
	n, o, _ := orcaSession(t)
	pad := strings.Repeat("x", 60)
	log := orcaLines(501, 2500, "line %d "+pad)
	o.SetOutput("term_1", append(slices.Clone(log), "$ "), true, log, 2500)
	o.SetScreen("term_1", strings.Join(append(orcaLines(2481, 2500, "line %d "+pad), "$"), "\n")+"\n")

	loaded := 21
	anchor := []string{fmt.Sprintf("line 2481 %s", pad)}
	var pages int
	for {
		got := orcaBefore(t, n, anchor, 500, loaded)
		pages++
		rows := strings.Split(str(got["text"]), "\n")
		loaded += len(rows)
		anchor = rows[:1]
		if got["has_more"] == false {
			if rows[0] != "line 501 "+pad || got["dropped"] != true || got["at_limit"] != true {
				t.Fatalf("top %q… %v", rows[0], got["dropped"])
			}
			break
		}
		if got["dropped"] != false || pages > 10 {
			t.Fatalf("page %d: %v", pages, got["dropped"])
		}
	}
	if pages != 4 || loaded != 2001 {
		t.Errorf("%d pages, %d lines", pages, loaded)
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("invalid requests: %v", inv)
	}
}

// An agent's live rows redrawn in place: its log repeats them and does not agree with the tail, so only the tail is
// shown, and its top says older output isn't kept. An Orca without cursor reads the same.
func TestOrcaScrollbackLeavesARedrawnLogOut(t *testing.T) {
	for _, noCursor := range []bool{false, true} {
		n, o, _ := orcaSession(t)
		pad := strings.Repeat("y", 200)
		var log, tail []string
		for i := range 400 {
			log = append(log, fmt.Sprintf("said %d %s", i, pad), fmt.Sprintf("spinner %d", i))
			tail = append(tail, fmt.Sprintf("said %d %s", i, pad))
		}
		tail = append(tail, "spinner 399", "> ")
		if noCursor {
			o.SetOldOutput("term_1", tail)
		} else {
			o.SetOutput("term_1", tail, false, log, len(log))
		}
		o.SetScreen("term_1", fmt.Sprintf("said 399 %s\nspinner 399\n>\n", pad))
		got := orcaBefore(t, n, []string{"said 399 " + pad}, 500, 3)
		rows := strings.Split(str(got["text"]), "\n")
		if len(rows) >= 400 || strings.Contains(str(got["text"]), "spinner 1\n") || got["has_more"] != false || got["dropped"] != true {
			t.Fatalf("noCursor %v: %d rows, %v", noCursor, len(rows), got)
		}
	}
}

// A program on the alternate screen: Orca answers the tail read with the screen, nothing is above it, and the reads
// are as before.
func TestOrcaScrollbackOnTheAlternateScreen(t *testing.T) {
	n, o, _ := orcaSession(t)
	o.SetScreen("term_1", "a\nb\n")
	got := orcaBefore(t, n, []string{"a"}, 200, 2)
	if got["text"] != "" || got["has_more"] != false || got["dropped"] != nil {
		t.Fatalf("alternate screen %v", got)
	}
	res, err := request(t, n, "session.screen", map[string]any{"session_id": sidA, "source": "recent", "lines": 60, "format": "ansi"})
	if err != nil || res["text"] != "a\r\nb\r\n" {
		t.Fatalf("recent = %v, %v", res, err)
	}
}

// A watched Orca screen may be read four times a second (refresh_ms); a herdr one gets no hint.
func TestOrcaScreenRefreshHint(t *testing.T) {
	n, o, _ := orcaSession(t)
	o.SetScreen("term_1", "a\n")
	res, err := request(t, n, "session.screen", map[string]any{"session_id": sidA})
	if err != nil || res["refresh_ms"] != float64(250) {
		t.Fatalf("orca screen = %v, %v", res, err)
	}
	hn, f, _ := herdrNode(t)
	seedAgents(t, hn, f, claudeAgent(sidA, "idle", "w1:p1", "w1:t1", "w1", "/tmp/s", ""))
	f.setScreen("w1:p1", "a\n")
	res, err = request(t, hn, "session.screen", map[string]any{"session_id": sidA})
	if err != nil || res["refresh_ms"] != nil {
		t.Fatalf("herdr screen = %v, %v", res, err)
	}
}

// Where the screen begins in the output: blank lines passed over, a prompt read without its typed text, the latest of
// equal places; no place at all keeps all the output above the screen.
func TestAboveScreen(t *testing.T) {
	for _, c := range []struct {
		name            string
		out, shown, all []string
	}{
		{"blank lines", []string{"a", "b", "", "c", "$ ls"}, []string{"b", "c", "$"}, []string{"a", "b", "c", "$"}},
		{"repeats", []string{"x", "y", "x", "y", "z"}, []string{"x", "y", "z"}, []string{"x", "y", "x", "y", "z"}},
		{"latest", []string{"p", "q", "p", "q"}, []string{"p", "q"}, []string{"p", "q", "p", "q"}},
		{"no place", []string{"a", "b"}, []string{"c", "d"}, []string{"a", "b", "c", "d"}},
		{"empty screen", []string{"a", "b"}, nil, []string{"a", "b"}},
	} {
		if got := aboveScreen(c.out, c.shown); !slices.Equal(got, c.all) {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}
