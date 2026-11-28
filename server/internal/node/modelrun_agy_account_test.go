package node

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The warm model.run worker follows a new agy sign-in kept in the macOS login keychain, and is not reused after agy
// answered with an account error (#17860).

func warmWorkerPID(t *testing.T, fx *fakeAgyWarmFixture) int {
	t.Helper()
	pool := fx.node.agyPool()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.worker == nil {
		return 0
	}
	return pool.worker.pid
}

func TestModelRunAgyWarmFollowsKeychainSignIn(t *testing.T) {
	fx := setupFakeAgyWarm(t, nil)
	stamp := "keychain:20261128100000Z:20261128100000Z"
	prev := agyKeychainStampFn
	agyKeychainStampFn = func() string { return stamp }
	t.Cleanup(func() { agyKeychainStampFn = prev })

	if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "before"}); err != nil {
		t.Fatal(err)
	}
	first := warmWorkerPID(t, fx)
	if first == 0 {
		t.Fatal("expected a warm worker after the first request")
	}
	if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "same sign-in"}); err != nil {
		t.Fatal(err)
	}
	if got := warmWorkerPID(t, fx); got != first {
		t.Fatalf("same sign-in: expected the warm worker %d to be reused, got %d", first, got)
	}

	stamp = "keychain:20261128100000Z:20261128105340Z" // the user signed in again: only the keychain item changed
	if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "after"}); err != nil {
		t.Fatal(err)
	}
	if got := warmWorkerPID(t, fx); got == first || got == 0 {
		t.Fatalf("expected a new warm worker after the keychain sign-in changed, still %d", got)
	}
}

func TestModelRunAgyWarmDropsWorkerAfterAccountError(t *testing.T) {
	fx := setupFakeAgyWarm(t, nil)
	got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "TRIGGER_QUOTA_LINE"})
	if err != nil {
		t.Fatal(err)
	}
	if ans, ok := got.(proto.ModelRunAnswer); !ok || !strings.Contains(ans.Text, "Individual quota reached") {
		t.Fatalf("expected agy's error line to be answered as before, got %#v", got)
	}
	if got := warmWorkerPID(t, fx); got != 0 {
		t.Fatalf("a worker that answered with a quota error must not be kept for the next request, kept %d", got)
	}
	if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "next"}); err != nil {
		t.Fatal(err)
	}
	if warmWorkerPID(t, fx) == 0 {
		t.Fatal("expected a fresh warm worker for the next request")
	}
}

func TestAgyAccountTroubleLine(t *testing.T) {
	for _, text := range []string{
		"⚠ RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 118h.",
		"\n⚠ UNAUTHENTICATED (code 401): Please sign in.\n",
		"⚠ FAILED_PRECONDITION (code 400): The quota for this project is used up.",
	} {
		if !agyAccountTrouble(text) {
			t.Errorf("expected an account error: %q", text)
		}
	}
	for _, text := range []string{
		"",
		"Here is the answer about quota planning.",
		"⚠ FAILED_PRECONDITION (code 400): The request is not supported here.",
		"Done.\n⚠ RESOURCE_EXHAUSTED (code 429): Slow down.",
	} {
		if agyAccountTrouble(text) {
			t.Errorf("not an account error: %q", text)
		}
	}
}

func TestParseAgyKeychainStamp(t *testing.T) {
	out := []byte(`keychain: "/Users/me/Library/Keychains/login.keychain-db"
class: "genp"
attributes:
    "acct"<blob>="antigravity"
    "cdat"<timedate>=0x32303236313131323136333930345A00  "20261112163904Z\000"
    "mdat"<timedate>=0x32303236313132383130353334305A00  "20261128105340Z\000"
    "svce"<blob>="gemini"
`)
	if got, want := parseAgyKeychainStamp(out), "keychain:20261112163904Z:20261128105340Z"; got != want {
		t.Fatalf("parseAgyKeychainStamp = %q, want %q", got, want)
	}
	if got := parseAgyKeychainStamp([]byte("class: \"genp\"\n")); got != "" {
		t.Fatalf("no dates: want empty, got %q", got)
	}
}
