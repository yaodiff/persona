package node

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

const cxUpdate0169 = "  Update available · 0.169.0 → 0.170.0\n\n› 1. Update now\n  2. Skip\n  3. Skip until next version\n\n  enter continue · esc skip\n"

func TestCodexUpdateOfferUsesEscapeOnly(t *testing.T) {
	for _, offer := range []string{cxUpdateOffer, cxUpdate0169, strings.Replace(cxUpdate0169, "› 1.", "  1.", 1)} {
		if keys := codexSkipUpdateKeys(offer); !slices.Equal(keys, []string{"escape"}) {
			t.Fatalf("update offer keys %v", keys)
		}
	}
	for _, other := range []string{
		cxTrust0153, cxReady,
		strings.Replace(cxUpdate0169, "3. Skip until next version", "3. Delete history", 1),
		strings.Replace(cxUpdate0169, "Update available", "Other setup", 1),
		cxUpdate0169 + "\n› Ask Codex to do anything\n",
	} {
		if keys := codexSkipUpdateKeys(other); keys != nil {
			t.Fatalf("keys %v for a different dialog or quoted offer", keys)
		}
	}
}

func TestCodexFirstPromptWithPasteChips(t *testing.T) {
	prompt := "context body\nmiddle line\nfinal tail"
	for _, tc := range []struct {
		draft string
		want  bool
	}{
		{prompt, true},
		{"[Pasted Content 35 chars]", true},
		{"[Pasted Content 13 chars]", false}, // the tail has not arrived yet
		{"[Pasted Content 13 chars]middle line\nfinal tail", true},
		{"[Pasted Content 11 chars]middle line\nfinal tail", false},
		{"[Pasted Content 13 chars]middle line[Pasted Content 1 chars]final tail", true},
		{"context body", false},
		{"[Pasted Content 0 chars]", false},
		{"[Pasted Content 999999999999999999999 chars]", false},
		{"[Pasted Content 500 chars]", false},
		{"[Pasted Content 13 chars]somebody else's draft", false},
		{"[Pasted Content 13 chars]final tail middle line", false},
		{"final tail[Pasted Content 13 chars]context body", false},
	} {
		if got := codexFirstPromptShown(tc.draft, prompt); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.draft, got, tc.want)
		}
	}
	if !codexFirstPromptShown("[Pasted Content 5 chars]m[Pasted Content 5 chars] #2tail", "abcdemfghijtail") {
		t.Fatal("equal-sized chips with Codex's disambiguation suffix were rejected")
	}
	if !codexFirstPromptShown("[Pasted Content 4 chars]", "a\r\nb\r\n") {
		t.Fatal("paste counts must use Codex's normalized line endings")
	}
}

func TestCodexFirstPasteAcrossBlankLine(t *testing.T) {
	n, f, _ := codexNode(t, "idle")
	f.setScreen(cxPane, cxReady)
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_text" {
			f.setScreen(cxPane, "› First paragraph.\n\n  Second paragraph.\n\n  GPT-6.2-Sol default · /work/project\n"+
				"                            ⚠ 1 warning · f2 to view\n")
		}
	})
	if err := n.typeCodexFirst(t.Context(), cxPane, "First paragraph.\n\nSecond paragraph."); err != nil {
		t.Fatal(err)
	}
	if keys := f.trace("pane.send_keys"); len(keys) != 1 || !strings.Contains(keys[0], `"enter"`) {
		t.Fatalf("first paste was not submitted once: %v", keys)
	}
}

func TestCodexFirstPasteWaitsForItsVisibleTail(t *testing.T) {
	n, f, _ := codexNode(t, "idle")
	prefix, tail := strings.Repeat("Context 中文\n", 120), "the final instruction"
	prompt := prefix + tail
	chip := fmt.Sprintf("[Pasted Content %d chars]", len([]rune(prefix)))
	f.setScreen(cxPane, cxReady)
	reads, submittedWithTail := 0, false
	var mu sync.Mutex
	f.after(func(c herdrCall) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case c.Method == "pane.send_text":
			f.setScreen(cxPane, "› "+chip+"\n\n  gpt-6 xhigh · /tmp/proj\n")
		case c.Method == "pane.read":
			reads++
			if reads == 5 {
				f.setScreen(cxPane, "› "+chip+tail+"\n\n  gpt-6 xhigh · /tmp/proj\n")
			}
		case isKeys("enter")(c):
			submittedWithTail = reads > 5
		}
	})
	if err := n.typeCodexFirst(context.Background(), cxPane, prompt); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !submittedWithTail || len(f.trace("pane.send_text")) != 1 || len(f.trace("pane.send_keys")) != 1 {
		t.Fatalf("submitted while the tail was in flight: reads=%d keys=%v", reads, f.trace("pane.send_keys"))
	}
}

// Even if an older CLI displays an update offer, native-argv startup only
// skips the offer. It never pastes or presses Enter to deliver the prompt.
func TestCodexNativeStartupSkipsUpdateAndConfirmsCompletePrompt(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			n, f := stalledCodexNode(t)
			withCodexWaits(t)
			codexStartWait = time.Second
			home := filepath.Dir(n.dir)
			cwd := filepath.Join(home, "proj")
			prompt := "Context 中文 🧪\n\nAll lines, quotes \" and $HOME stay literal.\nFinal line."
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start") {
					f.setScreen("w1:p2", cxUpdate0169)
				}
				if isKeys("escape")(c) {
					first := prompt
					if partial {
						first = "Context 中文"
					}
					writeWaitRollout(t, home, cwd, startThread, time.Now(), first)
					f.setScreen("w1:p2", cxReady)
				}
			})
			session, err := n.startCodex(context.Background(), n.defaultHost().(host.Launcher), cwd, "codex", []string{"codex"}, modelExecution{}, prompt, "w1", "", "")
			if partial {
				if err == nil || len(f.trace("tab.close", "pane.close")) == 0 {
					t.Fatalf("partial first message succeeded or left its pane: %+v %v", session, err)
				}
			} else if err != nil || session.SessionID != startThread {
				t.Fatalf("native start not confirmed: %+v %v", session, err)
			}
			if len(f.trace("pane.send_text", "agent.prompt")) != 0 {
				t.Fatal("native first prompt went through terminal input")
			}
			for _, c := range f.calls() {
				if c.Method == "pane.send_keys" && !isKeys("escape")(c) {
					t.Fatalf("startup sent %v instead of Escape", keysOf(c))
				}
			}
		})
	}
}

// Waiting rows still protect a user's draft and never submit into a modal.
func TestCodexWaitingInputUpdateDoesNotOverwriteDraftOrLoop(t *testing.T) {
	for _, next := range []string{cxUpdate0169, "› somebody else's draft\n\n  gpt-6 xhigh · /tmp/proj\n"} {
		t.Run(next[:min(12, len(next))], func(t *testing.T) {
			n, f, _ := codexNode(t, "idle")
			withCodexWaits(t)
			f.screenSteps(cxPane, func(c herdrCall) bool { return c.Method == "pane.send_text" || isKeys("escape")(c) }, cxReady, cxUpdate0169, next)
			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancel()
			err := n.typeCodexFirst(ctx, cxPane, "heading\nbody")
			if err == nil || len(f.trace("pane.send_text")) != 1 || len(f.trace("pane.send_keys")) != 1 {
				t.Fatalf("waiting input overwrote/submitted/looped: %v", err)
			}
		})
	}
}
