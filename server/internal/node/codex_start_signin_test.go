package node

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Codex's sign-in screen, which it shows in place of its input box when it has no login on the computer: the layout
// with the device-code choice, and an older one that offered an API key instead.
const (
	codexSignIn = "  Welcome to Codex, OpenAI's command-line coding agent\n\n" +
		"  Sign in with ChatGPT to use Codex as part of your paid plan\n  or connect an API key for usage-based billing\n\n" +
		"› 1. Sign in with ChatGPT\n     Usage included with paid plans\n" +
		"  2. Sign in with Device Code\n     Sign in from another device with a one-time code\n" +
		"  3. Sign in another way\n\n  Press enter to continue\n"
	codexSignInOlder = "  Sign in with ChatGPT to use Codex as part of your paid plan\n\n" +
		"> 1. Sign in with ChatGPT\n     Usage included with paid plans\n" +
		"  2. Provide your own API key\n     Pay for what you use\n\n  Press Enter to continue\n"
)

// A start that meets Codex's sign-in screen ends at once, with or without a first message: no wait outlasts it. Its
// refusal is coded codex_not_logged_in (the launcher's sign-in, never agent_start_stalled), carries the screen, answers
// nothing on it, and closes the new tab as any failed start does.
func TestCodexStartOnSignInScreenFailsAtOnce(t *testing.T) {
	for name, tc := range map[string]struct{ screen, prompt string }{
		"with a message":           {codexSignIn, "do the task"},
		"older layout":             {codexSignInOlder, "do the task"},
		"without a message":        {codexSignIn, ""},
		"older layout, no message": {codexSignInOlder, ""},
	} {
		t.Run(name, func(t *testing.T) {
			n, f := stalledCodexNode(t)
			codexStartWait = time.Minute
			tallPane(f, 51) // drawn from the top of a tall pane, as Codex draws its first screens
			f.setScreen("w1:p2", tc.screen)
			began := time.Now()
			_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": tc.prompt})
			if took := time.Since(began); took > 15*time.Second {
				t.Errorf("took %s: waited on a screen no wait outlasts", took)
			}
			if code, _ := refusalOf(t, err); code != proto.CodeCodexNotLoggedIn || !strings.Contains(err.Error(), "codex login") ||
				!strings.Contains(err.Error(), "Sign in with ChatGPT") {
				t.Fatalf("refusal %q: %v", code, err)
			}
			if keys := f.trace("pane.send_keys"); len(keys) != 0 {
				t.Errorf("answered the sign-in screen: %v", keys)
			}
			if closed := f.trace("tab.close", "pane.close"); len(closed) == 0 {
				t.Error("left the failed start's tab open")
			}
			if len(n.codexAbandoned) != 0 {
				t.Error("a closed tab remains pending")
			}
		})
	}
}

// A sign-in start whose tab could not be closed is still the launcher's sign-in, not a stalled start: the cleanup's words
// follow, and the next start must close the tab first, as after any failed cleanup.
func TestCodexSignInCleanupFailureKeepsItsCode(t *testing.T) {
	n, f := stalledCodexNode(t)
	codexStartWait = time.Minute
	f.setScreen("w1:p2", codexSignIn)
	f.reply("tab.close", herdrFail{Code: "unavailable", Message: "terminal unavailable"})
	_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
	if code, _ := refusalOf(t, err); code != proto.CodeCodexNotLoggedIn || !strings.Contains(err.Error(), "terminal unavailable") {
		t.Fatalf("refusal %q: %v", code, err)
	}
	if len(n.codexAbandoned) != 1 {
		t.Fatalf("pending cleanups %d, want 1", len(n.codexAbandoned))
	}
}

// codexSignInScreen knows the sign-in screen by its "Sign in with …" choice under a cursor, and no other screen.
func TestCodexSignInScreen(t *testing.T) {
	dialogs := codexTrustDialogs(t)
	for name, tc := range map[string]struct {
		screen string
		want   bool
	}{
		"sign-in":           {codexSignIn, true},
		"older sign-in":     {codexSignInOlder, true},
		"cursor further on": {strings.Replace(strings.Replace(codexSignIn, "› 1.", "  1.", 1), "  3. Sign", "› 3. Sign", 1), true},
		"no cursor drawn":   {strings.Replace(codexSignIn, "› 1.", "  1.", 1), false},
		"trust dialog":      {dialogs["0.167"], false},
		"update prompt":     {"  ✨ Update available! 0.166.0 -> 0.167.0\n\n› 1. Update now\n  2. Skip\n  3. Skip until next version\n\n  Press enter to continue\n", false},
		"another dialog":    {"  Pick a color theme\n\n› 1. Dark\n  2. Light\n\n  Press enter to continue\n", false},
		"message quoting it": {"› 1. Sign in with ChatGPT\n  2. Sign in with Device Code\n\n• Those are Codex's sign-in choices.\n\n" +
			"› Ask Codex to do anything\n\n  gpt-6 default · ~/proj\n", false},
		"composer": {"› Ask Codex to do anything\n\n  gpt-6 default · ~/proj\n", false},
	} {
		if got := codexSignInScreen(tc.screen); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
