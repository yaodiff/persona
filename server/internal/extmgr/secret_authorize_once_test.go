package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// Authorized once (docs/extensions/secret-providers.md): a launcher's key, an extension's secret and a reference's
// words that a provider gave once answer from then on without asking it again, so a provider that would have the user
// authorize it (1Password's op on a computer after 10 idle minutes, 12 hours or the app locking) is never asked by an
// everyday use: not by more launches, not by more starts of the extension, not by a restart of the manager. A value
// the provider may have changed (it stored a secret) is asked for quietly, so it never asks the user, and the kept one
// answers while it cannot be asked.
func TestAuthorizedOnceNeverAsksAgain(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "calls.jsonl")
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+secretProviderPackage(t, "example-vault", "exv", out)+`"}`)
	if c, body := call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); c != 200 {
		t.Fatalf("install: %d %v", c, body)
	}
	waitLong(t, "the provider running", func() bool {
		_, hp, err := h.extensions.secretProvider("exv://Example/item/ok")
		return err == nil && hp != nil
	})
	const ref = "exv://Example/item/ok"
	asked := func(method string) (n int, quiet int) {
		raw, _ := os.ReadFile(out)
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, `"method":"`+method+`"`) {
				n++
				if strings.Contains(line, `"quiet":true`) {
					quiet++
				}
			}
		}
		return n, quiet
	}
	lock := func(on bool) {
		t.Helper()
		if on {
			if err := os.WriteFile(out+".locked", nil, 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			_ = os.Remove(out + ".locked")
		}
	}
	restart := func() { // a new manager process: nothing in memory, the sealed copies in the database
		h.extensions.kept.mu.Lock()
		h.extensions.kept.kept, h.extensions.kept.loaded = nil, false
		h.extensions.kept.mu.Unlock()
		h.extensions.words.mu.Lock()
		h.extensions.words.kept, h.extensions.words.loaded = nil, false
		h.extensions.words.mu.Unlock()
	}
	launcher := func(what string) {
		t.Helper()
		if v, err := h.LauncherSecret(ctx, ref, "agy-gateway", ""); err != nil || v != secretValueForTest {
			t.Fatalf("%s: the launcher's key %q %v", what, v, err)
		}
	}
	extSecret := func(what string) {
		t.Helper()
		if v, err := h.extensions.extensionSecret(ctx, "example-user", ref); err != nil || v != secretValueForTest {
			t.Fatalf("%s: the extension's secret %q %v", what, v, err)
		}
	}
	words := func(what string) {
		t.Helper()
		if d, err := h.DescribeSecret(ctx, ref); err != nil || d.Title != "Item › ok" || d.Detail != "vault Example" {
			t.Fatalf("%s: the reference's words %+v %v", what, d, err)
		}
	}

	// First use, the user authorizes the provider once.
	launcher("first")
	extSecret("first")
	words("first")
	resolves, _ := asked("resolve")
	describes, _ := asked("describe")
	if resolves != 2 || describes != 1 {
		t.Fatalf("the first uses ask once each: %d resolves, %d describes", resolves, describes)
	}

	// The provider would ask the user again now: every everyday use answers without asking it.
	lock(true)
	for i := range 5 {
		launcher(fmt.Sprintf("launch %d", i))
		extSecret(fmt.Sprintf("start %d", i))
		words(fmt.Sprintf("page %d", i))
	}
	restart()
	launcher("after a restart")
	extSecret("after a restart")
	words("after a restart")
	// An upgrade, a restart or a new configuration of the provider keeps them (only removing or switching it off
	// revokes them).
	for _, kind := range []string{"upgraded", "config", "enabled", "rolled_back"} {
		h.extensions.keptProviderChanged(ctx, extension.Change{ID: "example-vault", Kind: kind})
		h.extensions.wordsProviderChanged(ctx, extension.Change{ID: "example-vault", Kind: kind})
		launcher(kind)
		extSecret(kind)
		words(kind)
	}
	if n, _ := asked("resolve"); n != resolves {
		t.Fatalf("asked the provider for a kept value: %d resolves, want %d", n, resolves)
	}
	if n, _ := asked("describe"); n != describes {
		t.Fatalf("asked the provider for kept words: %d describes, want %d", n, describes)
	}

	// The provider stored a secret: the values may have changed. Asked again quietly (it must not ask the user),
	// and while it cannot answer the kept ones still do.
	h.extensions.keptProviderStored(ctx, "example-vault")
	h.extensions.staleWords(ctx, "example-vault")
	launcher("stale, locked")
	extSecret("stale, locked")
	words("stale, locked")
	n, quiet := asked("resolve")
	if n != resolves+2 || quiet != 2 {
		t.Fatalf("a stale value is asked for quietly: %d resolves (%d quiet), want %d (2 quiet)", n, quiet, resolves+2)
	}
	if n, _ := asked("describe"); n != describes+1 {
		t.Fatalf("stale words are asked for once: %d", n)
	}
	// Unlocked again, the quiet ask succeeds and the values are fresh (not stale) from then on.
	lock(false)
	launcher("stale, unlocked")
	extSecret("stale, unlocked")
	lock(true)
	before, _ := asked("resolve")
	launcher("fresh, locked")
	extSecret("fresh, locked")
	if n, _ := asked("resolve"); n != before {
		t.Fatalf("a refreshed value asks again: %d → %d", before, n)
	}

	// Sealed, never in the clear: not the value, not the reference, not the words.
	for _, key := range []string{launcherKeptKey, describedKey} {
		var raw json.RawMessage
		if ok, err := h.store.Setting(ctx, key, &raw); !ok || err != nil {
			t.Fatalf("%s: nothing kept: %v %v", key, ok, err)
		}
		if s := string(raw); strings.Contains(s, secretValueForTest) || strings.Contains(s, "exv://") || strings.Contains(s, "Item ›") {
			t.Fatalf("%s kept in the clear: %s", key, s)
		}
	}

	// The extension no longer names the reference: what was kept of it goes; the launcher's stays.
	h.extensions.retainExtensionSecrets(ctx, "example-user", map[string]string{"key": "exv://Example/item/other"})
	if _, _, _, ok := h.extensions.kept.get(keptKey{reference: ref, kind: "extension", id: "example-user"}); ok {
		t.Fatal("an extension's secret it no longer names is kept")
	}
	if _, _, _, ok := h.extensions.kept.get(keptKey{reference: ref, kind: "launcher", id: "agy-gateway"}); !ok {
		t.Fatal("retaining an extension's secrets dropped a launcher's")
	}
	// The extension removed: its kept secrets go with it.
	extSecretUnlocked := func() {
		lock(false)
		defer lock(true)
		extSecret("again")
	}
	extSecretUnlocked()
	h.extensions.keptProviderChanged(ctx, extension.Change{ID: "example-user", Kind: "removed"})
	if _, _, _, ok := h.extensions.kept.get(keptKey{reference: ref, kind: "extension", id: "example-user"}); ok {
		t.Fatal("a removed extension's secret is kept")
	}
	// The provider removed: nothing of it stays, values or words.
	h.extensions.keptProviderChanged(ctx, extension.Change{ID: "example-vault", Kind: "removed"})
	h.extensions.wordsProviderChanged(ctx, extension.Change{ID: "example-vault", Kind: "removed"})
	if _, _, _, ok := h.extensions.kept.get(keptKey{reference: ref, kind: "launcher", id: "agy-gateway"}); ok {
		t.Fatal("a removed provider's value is kept")
	}
	h.extensions.words.mu.Lock()
	_, kept := h.extensions.words.kept[ref]
	h.extensions.words.mu.Unlock()
	if kept {
		t.Fatal("a removed provider's words are kept")
	}
}

// A provider that could not give a reference's words the first time (it would have asked the user) is not taken at
// its fallback: nothing is kept, the next describe asks again.
func TestWordsNotKeptWhenTheProviderCannotAnswer(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "calls.jsonl")
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+secretProviderPackage(t, "example-vault", "exv", out)+`"}`)
	if c, body := call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); c != 200 {
		t.Fatalf("install: %d %v", c, body)
	}
	waitLong(t, "the provider running", func() bool {
		_, hp, err := h.extensions.secretProvider("exv://Example/item/ok")
		return err == nil && hp != nil
	})
	_ = os.WriteFile(out+".locked", nil, 0o600)
	if _, err := h.DescribeSecret(ctx, "exv://Example/item/ok"); err == nil {
		t.Fatal("described while the provider cannot answer")
	}
	_ = os.Remove(out + ".locked")
	if d, err := h.DescribeSecret(ctx, "exv://Example/item/ok"); err != nil || d.Title != "Item › ok" {
		t.Fatalf("described once it can: %+v %v", d, err)
	}
}
