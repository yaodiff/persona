package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// keygen and sign-index (offline) sign a private market's index; market add --key and market key list / add / remove
// manage the keys the hub trusts for it.
func TestExtensionMarketKeyCommands(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	h, _, _ := marketHub(t)
	gh := &fakeGitHub{files: map[string][]byte{}}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: h.routes()}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PERSONA_LISTEN", "")
	b, _ := json.Marshal(map[string]any{"listen": ln.Addr().String(), "token": h.cfg.Token})
	if err := os.WriteFile(filepath.Join(home, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := ExtensionCommand(context.Background(), args, strings.NewReader(""), &out, &out)
		return out.String(), err
	}

	// A publisher's keys and a signed index, all offline.
	work := t.TempDir()
	keyFile, newKeyFile := filepath.Join(work, "market.pem"), filepath.Join(work, "market-new.pem")
	out, err := run("keygen", "--out", keyFile)
	if err != nil {
		t.Fatalf("keygen: %v %s", err, out)
	}
	pub := strings.Fields(out)[0]
	if _, err := extension.ParseSigningKey(pub); err != nil {
		t.Fatalf("keygen printed %q", out)
	}
	if info, err := os.Stat(keyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key file: %v %v", info, err)
	}
	if b, _ := os.ReadFile(keyFile); strings.Contains(out, strings.TrimSpace(string(b))) {
		t.Fatal("keygen printed the private key")
	}
	if _, err := run("keygen", "--out", keyFile); err == nil {
		t.Fatal("keygen overwrote a key")
	}
	newOut, err := run("keygen", "--out="+newKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	newPub := strings.Fields(newOut)[0]
	index := filepath.Join(work, extension.IndexFile)
	if err := os.WriteFile(index, marketIndex(t, "Private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run("sign-index", index, "--key", keyFile); err != nil || !strings.Contains(out, extension.MarketKeyID(pub)) {
		t.Fatalf("sign-index: %v %s", err, out)
	}
	signed, _ := os.ReadFile(index)
	if f, err := extension.ParseCatalogKeys(signed, []string{pub}); err != nil || f.SignedBy != pub {
		t.Fatalf("the signed index does not verify: %v", err)
	}
	if _, err := run("sign-index", index); err == nil {
		t.Fatal("sign-index without a key")
	}
	gh.mu.Lock()
	gh.files[extension.IndexFile] = signed
	gh.mu.Unlock()

	// The hub: a wrong key refuses the market, the right one adds it signed.
	id := extension.MarketID(extension.MarketSpec{Kind: "github", Owner: "example", Repo: "plugins"})
	if _, err := run("market", "add", "github:example/plugins", "--key="+newPub); err == nil {
		t.Fatal("added with a key that did not sign the index")
	}
	if out, err := run("market", "add", "github:example/plugins", "--key="+pub); err != nil {
		t.Fatalf("add: %v %s", err, out)
	}
	if out, _ := run("market", "list"); !strings.Contains(out, "signed "+extension.MarketKeyID(pub)) {
		t.Fatalf("list: %s", out)
	}
	if out, err := run("market", "key", "list", id); err != nil || !strings.Contains(out, extension.MarketKeyID(pub)+"  "+pub) {
		t.Fatalf("key list: %v %s", err, out)
	}
	// Rotation from the command line: add the new key, re-sign with both, remove the old.
	if out, err := run("market", "key", "add", id, newPub); err != nil || !strings.Contains(out, "2 trusted keys") {
		t.Fatalf("key add: %v %s", err, out)
	}
	waitMarketsRead(t, h)
	if out, err := run("sign-index", index, "--key", keyFile, "--key", newKeyFile); err != nil || strings.Count(out, "signed by") != 2 {
		t.Fatalf("re-sign: %v %s", err, out)
	}
	signed, _ = os.ReadFile(index)
	gh.mu.Lock()
	gh.files[extension.IndexFile] = signed
	gh.mu.Unlock()
	if out, err := run("market", "refresh", id); err != nil {
		t.Fatalf("refresh: %v %s", err, out)
	}
	if out, err := run("market", "key", "remove", id, extension.MarketKeyID(pub)); err != nil || !strings.Contains(out, "1 trusted key") || !strings.Contains(out, "signed "+extension.MarketKeyID(newPub)) {
		t.Fatalf("key remove: %v %s", err, out)
	}
	waitMarketsRead(t, h)
	if _, err := run("market", "key", "remove", id, extension.MarketKeyID(pub)); err == nil {
		t.Fatal("removed a key that is not there")
	}
	if _, err := run("market", "key", "add", id, "ed25519:nope"); err == nil {
		t.Fatal("added a malformed key")
	}
}

// --key may repeat and goes with --token; --token and --token-from still do not go together.
func TestExtensionMarketKeyArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"market", "add", "github:o/r", "--key=ed25519:a", "--key=ed25519:b", "--token"}, true},
		{[]string{"market", "add", "github:o/r", "--token", "--token-from=x"}, false},
		{[]string{"market", "add", "github:o/r", "--key="}, false},
		{[]string{"market", "add", "github:o/r", "--keys=a"}, false},
		{[]string{"market", "key", "list", "m"}, true},
		{[]string{"market", "key", "add", "m", "ed25519:a"}, true},
		{[]string{"market", "key", "add", "m", "ed25519:a", "extra"}, false},
		{[]string{"market", "key"}, false},
	} {
		if _, err := checkExtensionArgs(c.args); (err == nil) != c.ok {
			t.Errorf("%v: %v", c.args, err)
		}
	}
}
