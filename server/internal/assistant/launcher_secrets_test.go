package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// fakeLauncherSecrets is a provider extension for exv:// references, with what extmgr keeps of launcher keys: values
// by reference, every resolution that reached the provider noted, a kept value answering until it is revoked.
type fakeLauncherSecrets struct {
	mu       sync.Mutex
	values   map[string]string
	resolved []string // reference|launcher|machine
	forgot   []string
	// keeps: keep values as extmgr does (off: every use asks the provider). kept is what is kept, by
	// reference|launcher|machine; away: references whose provider cannot be asked now (not signed in).
	keeps bool
	kept  map[string]string
	away  map[string]bool
}

func keptName(ref, launcher, machine string) string { return ref + "|" + launcher + "|" + machine }

// resolve is the provider's answer for ref, as extmgr's resolveSecret takes it (f.mu held): a revoking failure drops
// what is kept of ref.
func (f *fakeLauncherSecrets) resolve(ref, launcher, machine string) (string, error) {
	f.resolved = append(f.resolved, keptName(ref, launcher, machine))
	if f.away[ref] {
		return "", extmgr.NewSecretError("not_signed_in", i18n.Args{"provider": "Example Vault"})
	}
	if v, ok := f.values[ref]; ok {
		return v, nil
	}
	for k := range f.kept {
		if strings.HasPrefix(k, ref+"|") {
			delete(f.kept, k)
		}
	}
	return "", extmgr.NewSecretError("not_found", i18n.Args{"provider": "Example Vault"})
}

func (f *fakeLauncherSecrets) keep(ref, launcher, machine, v string) {
	if f.kept == nil {
		f.kept = map[string]string{}
	}
	for k := range f.kept {
		if strings.HasPrefix(k, ref+"|"+launcher+"|") {
			f.kept[k] = v
		}
	}
	f.kept[keptName(ref, launcher, machine)] = v
}

func (f *fakeLauncherSecrets) LauncherSecret(_ context.Context, ref, launcher, machine string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.kept[keptName(ref, launcher, machine)]
	if ok && f.keeps {
		return v, nil
	}
	fresh, err := f.resolve(ref, launcher, machine)
	switch {
	case err == nil:
		f.keep(ref, launcher, machine, fresh)
	case ok && f.keeps && f.away[ref]:
		return v, nil
	}
	return fresh, err
}

func (f *fakeLauncherSecrets) RefreshLauncherSecret(_ context.Context, ref, launcher, machine string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, err := f.resolve(ref, launcher, machine)
	if err == nil {
		f.keep(ref, launcher, machine, v)
	}
	return err
}

func (f *fakeLauncherSecrets) ForgetLauncherSecrets(launcher string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, launcher)
	for k := range f.kept {
		if strings.Split(k, "|")[1] == launcher {
			delete(f.kept, k)
		}
	}
}

func (f *fakeLauncherSecrets) RetainLauncherSecrets(_ context.Context, keep func(launcher, machine, reference string) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.kept {
		p := strings.Split(k, "|")
		if !keep(p[1], p[2], p[0]) {
			delete(f.kept, k)
			f.forgot = append(f.forgot, k)
		}
	}
}

// keptNames are the uses kept, sorted.
func (f *fakeLauncherSecrets) keptNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Collect(maps.Keys(f.kept))
	slices.Sort(out)
	return out
}

func (f *fakeLauncherSecrets) SecretProviderOf(ref string) (extension.SecretProvider, bool) {
	if !strings.HasPrefix(ref, "exv://") {
		return extension.SecretProvider{}, false
	}
	return extension.SecretProvider{Extension: "example-vault", Contribution: "vault", Name: "Example Vault"}, true
}

func (f *fakeLauncherSecrets) SecretReference(_ context.Context, ref string) *extmgr.SecretReferenceView {
	return &extmgr.SecretReferenceView{Provider: "example-vault", ProviderName: "Example Vault", Reference: ref, Title: "Item › " + ref[strings.LastIndex(ref, "/")+1:]}
}

func (f *fakeLauncherSecrets) noted() (resolved, forgot []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resolved), slices.Clone(f.forgot)
}

const (
	refMainValue = "sk-test-from-vault-main"
	refMacValue  = "sk-test-from-vault-mac"
)

// A provider's key, and one computer's key for it, saved as references: checked on save, resolved for the computer
// that pulls it, shown as the reference and what it names, never handed to a node from before the catalog.
func TestLauncherKeyReferences(t *testing.T) {
	s, _ := scopeService(t)
	vault := &fakeLauncherSecrets{keeps: true, values: map[string]string{"exv://Example/item/ok": refMainValue, "exv://Example/item/mac": refMacValue}}
	s.refs = vault
	addProvider(t, s, `{"name":"anchor","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-anchor-000"}`)

	words := func(key string, args i18n.Args) string { return i18n.T(i18n.Default, key, args) }
	for body, want := range map[string]string{
		`{"name":"gw","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-x","api_key_reference":"exv://Example/item/ok"}`: words("assistant-models_launchers.err_key_value_or_reference", nil),
		`{"name":"gw","base_url":"https://gw.example.com","model":"m1","api_key_reference":"nop://Example/item/ok"}`:                       words("hub-extension_secrets.no_provider", i18n.Args{"scheme": "nop://"}),
		`{"name":"gw","base_url":"https://gw.example.com","model":"m1","api_key_reference":"exv://Example/item/missing"}`:                  words("hub-extension_secrets.not_found", i18n.Args{"provider": "Example Vault"}),
		`{"name":"gw","base_url":"https://gw.example.com","model":"m1","api_key_reference":"https://example.com/key"}`:                     words("hub-extension_secrets.not_reference", nil),
	} {
		if r := request(s, "POST", "/api/v1/launchers", body); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), want) {
			t.Fatalf("%s: want 400 with %q, got %d %s", body, want, r.Code, r.Body)
		}
	}

	v := addProvider(t, s, `{"name":"gw","base_url":"https://gw.example.com","model":"m1","api_key_reference":"exv://Example/item/ok","own_service":"use"}`)
	if !v.HasKey || v.KeyReference == nil || v.KeyReference.Reference != "exv://Example/item/ok" || v.KeyReference.ProviderName != "Example Vault" || v.KeyReference.Title != "Item › ok" {
		t.Fatalf("the view says where the key comes from: %+v %+v", v, v.KeyReference)
	}
	if resolved, _ := vault.noted(); !slices.Contains(resolved, "exv://Example/item/ok|gw|") {
		t.Fatalf("checked on save: %v", resolved)
	}

	if l, err := pull(t, s, "mac", "gw", nil); err != nil || l.APIKey != refMainValue {
		t.Fatalf("mac pulls the value: %q %v", l.APIKey, err)
	}
	if resolved, _ := vault.noted(); !slices.Contains(resolved, "exv://Example/item/ok|gw|mac") {
		t.Fatalf("resolved for the computer that pulled it: %v", resolved)
	}

	// One computer's key as a reference; both maps for one computer are refused.
	if r := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m1","machine_keys":{"mac":"k"},"machine_key_references":{"mac":"exv://Example/item/mac"}}`); r.Code != http.StatusBadRequest {
		t.Fatalf("a key and a reference for mac: %d %s", r.Code, r.Body)
	}
	r := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m1","machine_key_references":{"mac":"exv://Example/item/mac"}}`)
	if r.Code != 200 {
		t.Fatalf("mac's key as a reference: %d %s", r.Code, r.Body)
	}
	if body := r.Body.String(); strings.Contains(body, "sk-test-from-vault") || !strings.Contains(body, `"key_machines":["mac"]`) ||
		!strings.Contains(body, `"machine_key_references":{"mac":{"provider":"example-vault"`) || !strings.Contains(body, `"key_reference":{`) {
		t.Fatalf("view: %s", body)
	}
	// Saved again with the same reference for the provider: what is kept of it stays; mac's own is new and asked.
	if kept := vault.keptNames(); !slices.Equal(kept, []string{"exv://Example/item/mac|gw|mac", "exv://Example/item/ok|gw|"}) {
		t.Fatalf("kept after mac's own key: %v", kept)
	}
	if l, _ := pull(t, s, "mac", "gw", nil); l.APIKey != refMacValue {
		t.Fatalf("mac's own: %q", l.APIKey)
	}
	if l, _ := pull(t, s, "server", "gw", nil); l.APIKey != refMainValue {
		t.Fatalf("server's: %q", l.APIKey)
	}
	for _, path := range []string{"/api/v1/launchers", "/api/v1/launchers/gw"} {
		if r := request(s, "GET", path, ""); strings.Contains(r.Body.String(), "sk-test-from-vault") || !strings.Contains(r.Body.String(), "exv://Example/item/mac") {
			t.Fatalf("%s: never a value, always the reference: %s", path, r.Body)
		}
	}

	// Kept sealed as the reference, never the value.
	stored, err := s.readStoredLaunchers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := s.readMachineKeys(context.Background())
	raw, _ := json.Marshal(map[string]any{"stored": stored, "keys": keys})
	if strings.Contains(string(raw), "sk-test-from-vault") || strings.Contains(string(raw), "exv://") {
		t.Fatalf("stored in the clear: %s", raw)
	}
	i := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == "gw" })
	if ref, ok := s.keyReference(stored[i].KeySealed); !ok || ref != "exv://Example/item/ok" {
		t.Fatalf("sealed reference: %q %v", ref, ok)
	}
	if m, err := s.openStoredLauncher(stored[i]); err != nil || m.APIKey != "" {
		t.Fatalf("the stored provider opens without the reference in place of a key: %q %v", m.APIKey, err)
	}

	// A node from before the catalog is not handed it; one that pulls is.
	catalog, legacy, _ := s.nodeLaunchersFor(context.Background(), stored, keys, "mac")
	if !slices.ContainsFunc(catalog, func(l proto.ManagedLauncher) bool { return l.Name == "gw" }) ||
		slices.ContainsFunc(legacy, func(l proto.ManagedLauncher) bool { return l.Name == "gw" }) {
		t.Fatalf("catalog %v legacy %v", catalog, legacy)
	}

	// The provider extension's details list both.
	refs := s.launcherReferences(context.Background())
	if len(refs) != 2 || refs[0] != (extmgr.LauncherReference{Launcher: "gw", LauncherName: "gw", Reference: "exv://Example/item/ok"}) ||
		refs[1] != (extmgr.LauncherReference{Launcher: "gw", LauncherName: "gw", Machine: "mac", Reference: "exv://Example/item/mac"}) {
		t.Fatalf("launcher references: %+v", refs)
	}

	// The reference stops resolving: what is kept answers until a refresh hears it is gone, then the pull refuses
	// with the provider's words.
	vault.mu.Lock()
	delete(vault.values, "exv://Example/item/ok")
	vault.mu.Unlock()
	if l, err := pull(t, s, "server", "gw", nil); err != nil || l.APIKey != refMainValue {
		t.Fatalf("kept, the provider not asked: %q %v", l.APIKey, err)
	}
	if r := request(s, "POST", "/api/v1/launchers/gw/refresh-key", ""); r.Code != http.StatusConflict ||
		!strings.Contains(r.Body.String(), `"secret_code":"not_found"`) || !strings.Contains(r.Body.String(), `"kept":false`) {
		t.Fatalf("refresh of a reference gone: %d %s", r.Code, r.Body)
	}
	if _, err := pull(t, s, "server", "gw", nil); codeOf(err) != proto.CodeLauncherKeyUnresolved || !strings.Contains(err.Error(), words("hub-extension_secrets.not_found", i18n.Args{"provider": "Example Vault"})) {
		t.Fatalf("unresolved: %v", err)
	}

	// A key given replaces the reference.
	if r := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-plain-0000"}`); r.Code != 200 || strings.Contains(r.Body.String(), `"key_reference"`) {
		t.Fatalf("a plain key again: %d %s", r.Code, r.Body)
	}
	if l, _ := pull(t, s, "server", "gw", nil); l.APIKey != "sk-test-plain-0000" {
		t.Fatalf("plain: %q", l.APIKey)
	}

	if r := request(s, "DELETE", "/api/v1/launchers/gw", ""); r.Code != 200 {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if _, forgot := vault.noted(); !slices.Contains(forgot, "gw") {
		t.Fatalf("removed: its kept keys dropped: %v", forgot)
	}
	if kept := vault.keptNames(); len(kept) != 0 {
		t.Fatalf("removed, still kept: %v", kept)
	}
	if refs := s.launcherReferences(context.Background()); len(refs) != 0 {
		t.Fatalf("a removed provider's references: %+v", refs)
	}
}

// The manager's own calls with a provider whose key is a reference resolve it for no computer.
func TestLauncherKeyReferenceForTheManager(t *testing.T) {
	s, _ := scopeService(t)
	vault := &fakeLauncherSecrets{values: map[string]string{"exv://Example/item/ok": refMainValue}}
	s.refs = vault
	addProvider(t, s, `{"kind":"api","api":"messages","name":"direct","base_url":"https://gw.example.com","model":"m1","api_key_reference":"exv://Example/item/ok"}`)
	c, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.direct", Model: "m1"})
	if err != nil || c.APIKey != refMainValue {
		t.Fatalf("the manager's call: %q %v", c.APIKey, err)
	}
	if resolved, _ := vault.noted(); resolved[len(resolved)-1] != "exv://Example/item/ok|direct|" {
		t.Fatalf("resolved for the manager: %v", resolved)
	}
	s.refs = nil // no extensions in this process: the reference does not open as a key
	if _, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.direct", Model: "m1"}); err == nil || !strings.Contains(err.Error(), "exv://") {
		t.Fatalf("no provider: %v", err)
	}
}

// What is kept of a launcher's key that is a reference (extmgr secret_kept.go) outlives every edit that leaves the key
// as it is: the scope, the model, the proxy, the name, the same reference sent back. Nobody is asked then (the vault
// may be locked). A refresh asks again: a provider that cannot answer now leaves it, a rotated secret replaces it. A
// key changed to another reference is asked for, and the old one is not kept any more.
func TestLauncherKeyKeptAcrossEdits(t *testing.T) {
	s, _ := scopeService(t)
	const ref, ref2 = "exv://Example/item/ok", "exv://Example/item/next"
	const rotated, next = "sk-test-from-vault-rotated", "sk-test-from-vault-next"
	vault := &fakeLauncherSecrets{keeps: true, values: map[string]string{ref: refMainValue, ref2: next}}
	s.refs = vault
	addProvider(t, s, `{"name":"gw","base_url":"https://gw.example.com","model":"m1","api_key_reference":"`+ref+`"}`)
	asked := func() int { r, _ := vault.noted(); return len(r) }
	if n := asked(); n != 1 {
		t.Fatalf("checked once on save: %d", n)
	}
	if l, err := pull(t, s, "mac", "gw", nil); err != nil || l.APIKey != refMainValue {
		t.Fatalf("mac pulls: %q %v", l.APIKey, err)
	}
	before := vault.keptNames()

	for _, body := range []string{
		`{"base_url":"https://gw.example.com","model":"m2","api_key_reference":"` + ref + `"}`,
		`{"base_url":"https://gw.example.com","model":"m2","share":"selected","scope_machines":["mac"]}`,
		`{"base_url":"https://gw.example.com","model":"m2","proxy":"http://127.0.0.1:7890","display_name":"Gateway"}`,
	} {
		if r := request(s, "PUT", "/api/v1/launchers/gw", body); r.Code != 200 {
			t.Fatalf("%s: %d %s", body, r.Code, r.Body)
		}
	}
	if n := asked(); n != 2 {
		t.Fatalf("an edit that leaves the key asked the provider: %d", n)
	}
	if kept := vault.keptNames(); !slices.Equal(kept, before) {
		t.Fatalf("an edit that leaves the key dropped what is kept: %v, was %v", kept, before)
	}

	// The vault is locked: every use is answered from what is kept, and a refresh says why it could not, keeping it.
	vault.mu.Lock()
	vault.away = map[string]bool{ref: true}
	vault.mu.Unlock()
	if l, err := pull(t, s, "mac", "gw", nil); err != nil || l.APIKey != refMainValue {
		t.Fatalf("locked, kept answers: %q %v", l.APIKey, err)
	}
	r := request(s, "POST", "/api/v1/launchers/gw/refresh-key", "")
	if r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), `"secret_code":"not_signed_in"`) ||
		!strings.Contains(r.Body.String(), `"kept":true`) || !strings.Contains(r.Body.String(), `"code":"launcher_key_unresolved"`) {
		t.Fatalf("refresh while locked: %d %s", r.Code, r.Body)
	}
	if l, err := pull(t, s, "mac", "gw", nil); err != nil || l.APIKey != refMainValue {
		t.Fatalf("after a refresh that could not: %q %v", l.APIKey, err)
	}

	// Unlocked and rotated: the refresh takes the new value, for every computer's use of it.
	vault.mu.Lock()
	vault.away, vault.values[ref] = nil, rotated
	vault.mu.Unlock()
	r = request(s, "POST", "/api/v1/launchers/gw/refresh-key", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"refreshed":[{}]`) || strings.Contains(r.Body.String(), "sk-test-from-vault") {
		t.Fatalf("refresh: %d %s", r.Code, r.Body)
	}
	if l, _ := pull(t, s, "mac", "gw", nil); l.APIKey != rotated {
		t.Fatalf("mac after the refresh: %q", l.APIKey)
	}

	// Another reference: asked for now, and the old one is not kept.
	n := asked()
	if r := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m2","api_key_reference":"`+ref2+`"}`); r.Code != 200 {
		t.Fatalf("another reference: %d %s", r.Code, r.Body)
	}
	if resolved, _ := vault.noted(); len(resolved) != n+1 || resolved[n] != keptName(ref2, "gw", "") {
		t.Fatalf("the new reference is checked: %v", resolved[n:])
	}
	if kept := vault.keptNames(); !slices.Equal(kept, []string{keptName(ref2, "gw", "")}) {
		t.Fatalf("the old reference still kept: %v", kept)
	}
	if l, _ := pull(t, s, "mac", "gw", nil); l.APIKey != next {
		t.Fatalf("mac pulls the new one: %q", l.APIKey)
	}

	// A plain key: nothing to refresh.
	if r := request(s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"m2","api_key":"sk-test-plain-0000"}`); r.Code != 200 {
		t.Fatalf("plain: %d %s", r.Code, r.Body)
	}
	if kept := vault.keptNames(); len(kept) != 0 {
		t.Fatalf("a plain key, a reference still kept: %v", kept)
	}
	if r := request(s, "POST", "/api/v1/launchers/gw/refresh-key", ""); r.Code != http.StatusBadRequest {
		t.Fatalf("refresh of a plain key: %d %s", r.Code, r.Body)
	}
	if r := request(s, "POST", "/api/v1/launchers/nope/refresh-key", ""); r.Code != http.StatusNotFound {
		t.Fatalf("refresh of no launcher: %d %s", r.Code, r.Body)
	}
}
