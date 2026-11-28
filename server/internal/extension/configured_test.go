package extension

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/i18n"
)

// configuredManifest is the example with base_url (format uri) reachable from its settings, api_key shown by its end
// and the settings checked by the program (#5906).
func configuredManifest(t *testing.T, network ...any) *Manifest {
	t.Helper()
	m, err := Parse(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": network}
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key", "hint": "last4"}}
		m["settings_check"] = true
		delete(m["configuration"].(map[string]any)["properties"].(map[string]any)["base_url"].(map[string]any), "default")
	}))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A config: entry names a string field of format uri with no default; anything else, and a hint the hub does not know,
// fails.
func TestConfiguredNetworkManifest(t *testing.T) {
	m := configuredManifest(t, "notes.example.com", "config:base_url", "config:base_url:http")
	if cs := m.Permissions.ConfiguredNetworks(); len(cs) != 2 || cs[0] != (ConfiguredNetwork{Field: "base_url"}) || !cs[1].HTTP {
		t.Fatalf("%+v", cs)
	}
	if !m.SettingsCheck || m.Secrets[0].Hint != HintLast4 {
		t.Fatalf("%+v", m)
	}
	for _, bad := range []struct {
		name   string
		change func(m map[string]any)
	}{
		{"no such field", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:nope"}} }},
		{"not a uri", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:folders"}} }},
		{"two colons", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:base_url:ftp"}} }},
		{"a default", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:base_url"}} }},
		{"unknown hint", func(m map[string]any) {
			m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key", "hint": "all"}}
		}},
	} {
		if _, err := Parse(with(t, bad.change)); err == nil {
			t.Errorf("%s: parsed", bad.name)
		}
	}
}

// The address a config: entry lets through is the field's value now: https by default (443), http only when the
// manifest says :http, and never one with user info, another scheme or no host.
func TestConfiguredTarget(t *testing.T) {
	https, http := ConfiguredNetwork{Field: "base_url"}, ConfiguredNetwork{Field: "base_url", HTTP: true}
	cfg := func(v string) json.RawMessage { b, _ := json.Marshal(map[string]string{"base_url": v}); return b }
	for _, c := range []struct {
		c    ConfiguredNetwork
		url  string
		want egress.Target
	}{
		{https, "https://Gateway.Example.com/v1", egress.Target{Host: "gateway.example.com", Port: "443"}},
		{https, "https://gateway.example.com:8443", egress.Target{Host: "gateway.example.com", Port: "8443"}},
		{https, "http://gateway.example.com", egress.Target{}},
		{https, "http://192.0.2.10:8080", egress.Target{}},
		{https, "http://127.0.0.1:8080", egress.Target{Host: "127.0.0.1", Port: "8080", HTTP: true}},
		{https, "http://localhost:8080/v1", egress.Target{Host: "localhost", Port: "8080", HTTP: true}},
		{https, "http://[::1]:8080", egress.Target{Host: "::1", Port: "8080", HTTP: true}},
		{https, "http://127.0.0.1", egress.Target{Host: "127.0.0.1", Port: "80", HTTP: true}},
		{https, "http://localhost", egress.Target{Host: "localhost", Port: "80", HTTP: true}},
		{https, "http://[::1]", egress.Target{Host: "::1", Port: "80", HTTP: true}},
		{http, "http://gateway.example.com", egress.Target{Host: "gateway.example.com", Port: "80", HTTP: true}},
		{http, "https://gateway.example.com", egress.Target{Host: "gateway.example.com", Port: "443"}},
		{https, "https://me:pw@gateway.example.com", egress.Target{}},
		{https, "ftp://gateway.example.com", egress.Target{}},
		{https, "gateway.example.com", egress.Target{}},
		{https, "", egress.Target{}},
		{https, "https://[2001:db8::1]:9443", egress.Target{Host: "2001:db8::1", Port: "9443"}},
	} {
		if got, _ := ConfiguredTarget(c.c, cfg(c.url)); got != c.want {
			t.Errorf("%+v %q: %+v, want %+v", c.c, c.url, got, c.want)
		}
	}
}

// A WebSocket address lets through what its HTTP one would (#16264): wss as https (443 by default), ws as plain http
// (80 by default), which needs :http in the manifest or a loopback host; user info, no host or another scheme still
// let nothing through. A voice upstream (wss://<host>/v1/live) is such an address.
func TestConfiguredTargetTakesWebSocketSchemes(t *testing.T) {
	https, http := ConfiguredNetwork{Field: "url"}, ConfiguredNetwork{Field: "url", HTTP: true}
	cfg := func(v string) json.RawMessage { b, _ := json.Marshal(map[string]string{"url": v}); return b }
	for _, c := range []struct {
		c    ConfiguredNetwork
		url  string
		want egress.Target
	}{
		{https, "wss://Live.Example.com/v1/live", egress.Target{Host: "live.example.com", Port: "443"}},
		{https, "WSS://live.example.com:9443/v1/live", egress.Target{Host: "live.example.com", Port: "9443"}},
		{http, "wss://live.example.com/v1/live", egress.Target{Host: "live.example.com", Port: "443"}},
		{https, "ws://live.example.com/v1/live", egress.Target{}},
		{https, "ws://192.0.2.10:8080/v1/live", egress.Target{}},
		{https, "ws://127.0.0.1:8080/v1/live", egress.Target{Host: "127.0.0.1", Port: "8080", HTTP: true}},
		{https, "ws://[::1]/v1/live", egress.Target{Host: "::1", Port: "80", HTTP: true}},
		{http, "ws://live.example.com/v1/live", egress.Target{Host: "live.example.com", Port: "80", HTTP: true}},
		{http, "ws://live.example.com:8080/v1/live", egress.Target{Host: "live.example.com", Port: "8080", HTTP: true}},
		{http, "wss://me:pw@live.example.com/v1/live", egress.Target{}},
		{http, "wss:///v1/live", egress.Target{}},
		{http, "wsx://live.example.com/v1/live", egress.Target{}},
	} {
		if got, _ := ConfiguredTarget(c.c, cfg(c.url)); got != c.want {
			t.Errorf("%+v %q: %+v, want %+v", c.c, c.url, got, c.want)
		}
	}
	// The manifest's entry as a package names it: the address reaches the host, whichever scheme it is written in.
	m := &Manifest{Permissions: Permissions{Network: []string{"config:url:http"}}}
	if got := ConfiguredTargets(m, cfg("wss://live.example.com/v1/live")); len(got) != 1 || got[0] != (egress.Target{Host: "live.example.com", Port: "443"}) {
		t.Fatalf("targets %+v", got)
	}
}

func TestSecretHint(t *testing.T) {
	last4 := SecretSpec{Name: "k", Hint: HintLast4}
	if got := SecretHint(last4, "ag-demo-key-0000-7f3a"); got != "7f3a" {
		t.Fatal(got)
	}
	if got := SecretHint(last4, "short-key"); got != "" {
		t.Fatalf("a short key shows nothing of itself: %q", got)
	}
	if got := SecretHint(SecretSpec{Name: "k"}, "ag-demo-key-0000-7f3a"); got != "" {
		t.Fatalf("a secret that did not ask for it: %q", got)
	}
}

// The consent names the address in the settings now (未填 before there is one) and says http when it is allowed; an
// upgrade that adds a config: entry adds a line to agree to.
func TestExplainConfiguredAddress(t *testing.T) {
	m := configuredManifest(t, "notes.example.com", "config:base_url")
	detail := func(lines []PermissionLine) string {
		for _, l := range lines {
			if l.Key == "network_config" {
				return l.Detail
			}
		}
		return ""
	}
	if d := detail(ExplainIn(i18n.ZhHans, m)); !strings.Contains(d, "当前：未填") || !strings.Contains(d, "只用 https") {
		t.Fatalf("before install: %q", d)
	}
	cfg := json.RawMessage(`{"base_url":"https://gateway.example.com"}`)
	if d := detail(ExplainWith(i18n.ZhHans, m, cfg)); !strings.Contains(d, "当前：gateway.example.com:443") {
		t.Fatalf("installed: %q", d)
	}
	for _, l := range ExplainIn(i18n.ZhHans, m) {
		if l.Key == "network" && strings.Contains(l.Detail, "config:") {
			t.Fatalf("the host line lists a field: %q", l.Detail)
		}
	}
	if d := detail(ExplainIn(i18n.ZhHans, configuredManifest(t, "config:base_url:http"))); !strings.Contains(d, "http") || strings.Contains(d, "只用 https") {
		t.Fatalf("http allowed: %q", d)
	}
	old := configuredManifest(t, "notes.example.com")
	if d := detail(AddedIn(i18n.ZhHans, old, m)); d == "" {
		t.Fatal("an upgrade adding config:base_url asks again")
	}
	if d := detail(AddedIn(i18n.ZhHans, m, m)); d != "" {
		t.Fatalf("nothing added: %q", d)
	}
}

// networkConfigDetail is the detail of the first "config:" line of lines ("" when there is none).
func networkConfigDetail(lines []PermissionLine) string {
	for _, l := range lines {
		if l.Key == "network_config" {
			return l.Detail
		}
	}
	return ""
}

// writeConfiguredPackage is the example at version with network as its permissions.network and base_url, a uri with
// no default, that a config: entry may name.
func writeConfiguredPackage(t *testing.T, version string, network ...any) string {
	t.Helper()
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["version"] = version
		m["permissions"] = map[string]any{"network": network}
		delete(m["configuration"].(map[string]any)["properties"].(map[string]any)["base_url"].(map[string]any), "default")
	})
	return pkg
}

// installConfigured installs the example at 1.2.0 with network, then saves base_url.
func installConfigured(t *testing.T, r *Registry, baseURL string, network ...any) {
	t.Helper()
	ctx := context.Background()
	pv, err := r.Preview(ctx, "dir:"+writeConfiguredPackage(t, "1.2.0", network...), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"`+baseURL+`"}`), "app"); err != nil {
		t.Fatal(err)
	}
}

// An upgrade's preview names the address the installed extension's settings hold for a config: entry, in every
// language it is shown in, as its settings page does; a fresh install of the same manifest has none yet (#8570).
func TestUpgradePreviewNamesSavedAddress(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installConfigured(t, r, "https://gateway.example.com:8443/v1", "config:base_url")
	next := writeConfiguredPackage(t, "1.3.0", "config:base_url")
	up, err := r.Preview(ctx, "dir:"+next, "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if !up.Upgrade {
		t.Fatalf("not an upgrade: %+v", up)
	}
	for _, c := range []struct {
		name  string
		lines []PermissionLine
		want  string
	}{
		{"staged", up.Permissions, "当前：gateway.example.com:8443"},
		{"zh-Hans", up.In(i18n.ZhHans).Permissions, "当前：gateway.example.com:8443"},
		{"en", up.In(i18n.En).Permissions, "now: gateway.example.com:8443"},
	} {
		if d := networkConfigDetail(c.lines); !strings.Contains(d, c.want) {
			t.Errorf("upgrade, %s: %q, want %q", c.name, d, c.want)
		}
	}

	fresh, _, _ := newTestRegistry(t)
	pv, err := fresh.Preview(ctx, "dir:"+next, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		lines []PermissionLine
		want  string
	}{
		{"staged", pv.Permissions, "当前：未填"},
		{"zh-Hans", pv.In(i18n.ZhHans).Permissions, "当前：未填"},
		{"en", pv.In(i18n.En).Permissions, "now: not set"},
	} {
		if d := networkConfigDetail(c.lines); !strings.Contains(d, c.want) {
			t.Errorf("fresh install, %s: %q, want %q", c.name, d, c.want)
		}
	}
}

// An upgrade that adds a config: entry for a field that already holds an address lists that address among the
// permissions that grew (#8570).
func TestUpgradeAddingConfigEntryShowsSavedValue(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installConfigured(t, r, "https://gateway.example.com:8443", "notes.example.com")
	up, err := r.Preview(ctx, "dir:"+writeConfiguredPackage(t, "1.3.0", "notes.example.com", "config:base_url"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if !up.MorePermissions {
		t.Fatal("adding config:base_url asks again")
	}
	for _, c := range []struct {
		name  string
		lines []PermissionLine
		want  string
	}{
		{"staged", up.AddedPermissions, "当前：gateway.example.com:8443"},
		{"zh-Hans", up.In(i18n.ZhHans).AddedPermissions, "当前：gateway.example.com:8443"},
		{"en", up.In(i18n.En).AddedPermissions, "now: gateway.example.com:8443"},
	} {
		if d := networkConfigDetail(c.lines); !strings.Contains(d, c.want) {
			t.Errorf("%s: %q, want %q", c.name, d, c.want)
		}
	}

	// What the user saved, not the old version's default: the new one has none for the field, so after the upgrade
	// one never filled in reaches nothing.
	defaulted, _, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, nil) // base_url defaults to https://notes.example.com
	pv, err := defaulted.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaulted.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	up, err = defaulted.Preview(ctx, "dir:"+writeConfiguredPackage(t, "1.3.0", "notes.example.com", "config:base_url"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if d := networkConfigDetail(up.In(i18n.En).AddedPermissions); !strings.Contains(d, "now: not set") {
		t.Fatalf("never filled in: %q", d)
	}
}

// settings_check: a change of a secret or the settings is checked by the extension with the values as they would be
// after it; a refusal saves nothing (no secret row, no audit line); one without settings_check is never asked.
func TestSettingsCheckBeforeSaving(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": []any{"config:base_url"}}
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key", "hint": "last4"}}
		m["settings_check"] = true
		delete(m["configuration"].(map[string]any)["properties"].(map[string]any)["base_url"].(map[string]any), "default")
	})
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	var asked []string
	refuse := errors.New("网关不认这把 Key")
	r.SettingsCheck = func(_ context.Context, p *Extension, config json.RawMessage, secrets map[string]string) error {
		asked = append(asked, string(config)+" "+secrets["api_key"])
		if strings.Contains(secrets["api_key"], "bad") || strings.Contains(string(config), "wrong") {
			return refuse
		}
		return nil
	}
	audits := len(store.audit)
	if err := r.SetSecret(ctx, "example-notes", "api_key", "bad-key-1234567890", "app"); !errors.Is(err, refuse) {
		t.Fatalf("refused key: %v", err)
	}
	if set := r.SecretsSet(ctx, r.Get("example-notes")); set["api_key"] || len(store.audit) != audits {
		t.Fatalf("a refused key was kept (%v) or audited (%v)", set, store.audit[audits:])
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "good-key-1234567890", "app"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://wrong.example.com"}`), "app"); !errors.Is(err, refuse) {
		t.Fatalf("refused settings: %v", err)
	}
	if strings.Contains(string(r.Config(r.Get("example-notes"))), "wrong") {
		t.Fatal("refused settings were saved")
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://gateway.example.com"}`), "app"); err != nil {
		t.Fatal(err)
	}
	// The settings were checked with the key kept, the key with the settings kept.
	if len(asked) != 4 || !strings.HasSuffix(asked[3], " good-key-1234567890") || !strings.Contains(asked[3], "gateway.example.com") {
		t.Fatalf("asked %q", asked)
	}
	for _, a := range store.audit {
		if strings.Contains(a, "key-1234567890") {
			t.Fatal("a value in the audit:", a)
		}
	}

	// An extension without settings_check is not asked.
	asked = nil
	r2, _, _ := newTestRegistry(t)
	pkg2 := t.TempDir()
	writePackage(t, pkg2, nil)
	pv, _ = r2.Preview(ctx, "dir:"+pkg2, "")
	if _, err := r2.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	r2.SettingsCheck = r.SettingsCheck
	_ = r2.SetSecret(ctx, "example-notes", "api_key", "bad-key-1234567890", "app")
	if len(asked) != 0 {
		t.Fatalf("asked an extension without settings_check: %q", asked)
	}
}

// A change saved while another was being checked is refused (settings_moved): it was checked against the settings
// before it.
func TestSettingsCheckSettingsMoved(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key"}}
		m["settings_check"] = true
	})
	pv, _ := r.Preview(ctx, "dir:"+pkg, "")
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	moved := false
	r.SettingsCheck = func(ctx context.Context, p *Extension, config json.RawMessage, secrets map[string]string) error {
		if !moved && secrets["api_key"] == "key-checked-slowly" {
			moved = true
			live := r.Get(p.Record.ID)
			r.mu.Lock()
			live.Record.Config = json.RawMessage(`{"base_url":"https://other.example.com"}`) // saved by someone else meanwhile
			r.mu.Unlock()
		}
		return nil
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "key-checked-slowly", "app"); !errors.Is(err, ErrSettingsMoved) {
		t.Fatalf("%v", err)
	}
}

// sandboxManifest is a manifest whose settings hold loopback addresses: base_url, an entry list's address, and an
// unrelated key that must survive.
func sandboxManifest(t *testing.T, defaultURL string) *Manifest {
	t.Helper()
	m, err := Parse(with(t, func(m map[string]any) {
		base := map[string]any{"type": "string", "title": "Base", "format": "uri"}
		if defaultURL != "" {
			base["default"] = defaultURL
		}
		m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{
			"base_url": base,
			"accounts": map[string]any{"type": "array", "title": "Accounts", "maxItems": 4, "x-secret": map[string]any{"name": "account_key", "title": "Key"}, "items": map[string]any{"type": "object", "properties": map[string]any{
				"name":    map[string]any{"type": "string", "title": "Name"},
				"address": map[string]any{"type": "string", "title": "Address", "format": "uri"},
			}}},
			"other": map[string]any{"type": "string", "title": "Other", "format": "uri"},
		}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Permitted loopback addresses in the settings are written as LocalHost for the sandbox, including a schema default and
// an entry list's address; unrelated keys and unpermitted addresses stay as they were (#13540).
func TestSandboxConfig(t *testing.T) {
	loopback := egress.Rule{Hosts: []string{"127.0.0.1"}}
	m := sandboxManifest(t, "http://127.0.0.1:8080/v1")
	call := func(config string, rule egress.Rule) map[string]any {
		t.Helper()
		var got map[string]any
		raw := SandboxConfig(m, json.RawMessage(config), rule)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return got
	}
	// A schema default is filled in and rewritten when the user set nothing.
	if got := call(`{"other":"https://notes.example.com"}`, loopback); got["base_url"] != "http://host.persona.internal:8080/v1" || got["other"] != "https://notes.example.com" {
		t.Fatalf("default: %v", got)
	}
	// An explicit loopback URI.
	got := call(`{"base_url":"http://localhost:8080/v1","other":"https://notes.example.com"}`, loopback)
	if got["base_url"] != "http://host.persona.internal:8080/v1" || got["other"] != "https://notes.example.com" {
		t.Fatalf("explicit: %v", got)
	}
	// An entry list's address, with unrelated keys kept.
	got = call(`{"accounts":[{"_id":"entry_one","name":"One","address":"http://127.0.0.1:9000/v1"},{"_id":"entry_two","name":"Two","address":"https://other.example.com"}],"keep":true}`, loopback)
	entries, _ := got["accounts"].([]any)
	first, _ := entries[0].(map[string]any)
	second, _ := entries[1].(map[string]any)
	if first["address"] != "http://host.persona.internal:9000/v1" || first["name"] != "One" ||
		second["address"] != "https://other.example.com" || got["keep"] != true {
		t.Fatalf("entries: %v", got)
	}
	// IPv6 loopback.
	if got := call(`{"base_url":"http://[::1]:8080/v1"}`, loopback); got["base_url"] != "http://host.persona.internal:8080/v1" {
		t.Fatalf("ipv6: %v", got)
	}
	// A "config:" target on the port lets it through; on another port it does not.
	target := egress.Rule{Targets: []egress.Target{{Host: "127.0.0.1", Port: "8080", HTTP: true}}}
	if got := call(`{"base_url":"http://127.0.0.1:8080/v1"}`, target); got["base_url"] != "http://host.persona.internal:8080/v1" {
		t.Fatalf("target: %v", got)
	}
	other := egress.Rule{Targets: []egress.Target{{Host: "127.0.0.1", Port: "9000", HTTP: true}}}
	if got := call(`{"base_url":"http://127.0.0.1:8080/v1"}`, other); got["base_url"] != "http://127.0.0.1:8080/v1" {
		t.Fatalf("another port: %v", got)
	}
	// http needs a target's HTTP allowance; https does not.
	httpsOnly := egress.Rule{Targets: []egress.Target{{Host: "127.0.0.1", Port: "8080"}}}
	if got := call(`{"base_url":"http://127.0.0.1:8080/v1"}`, httpsOnly); got["base_url"] != "http://127.0.0.1:8080/v1" {
		t.Fatalf("http without the allowance: %v", got)
	}
	if got := call(`{"base_url":"https://127.0.0.1:8080/v1"}`, httpsOnly); got["base_url"] != "https://host.persona.internal:8080/v1" {
		t.Fatalf("https: %v", got)
	}
	// An owner-allowed loopback host.
	owner := egress.Rule{Owner: []egress.Target{{Host: "127.0.0.1"}}}
	if got := call(`{"base_url":"http://127.0.0.1:8123/v1"}`, owner); got["base_url"] != "http://host.persona.internal:8123/v1" {
		t.Fatalf("owner: %v", got)
	}
	// Not permitted: unchanged, and the same bytes come back.
	raw := json.RawMessage(`{"base_url":"http://127.0.0.1:8080/v1"}`)
	plain := egress.Rule{Hosts: []string{"notes.example.com"}}
	if got := SandboxConfig(m, raw, plain); string(got) != string(raw) {
		t.Fatalf("unpermitted: %s", got)
	}
	// User info stays in front of the rewritten host.
	if got := call(`{"base_url":"http://me:pw@127.0.0.1:8080/v1"}`, loopback); got["base_url"] != "http://me:pw@host.persona.internal:8080/v1" {
		t.Fatalf("user info: %v", got)
	}
	// Not a sandbox URL: an https host, another scheme.
	for _, v := range []string{"https://notes.example.com", "ftp://127.0.0.1:8080"} {
		cfg, _ := json.Marshal(map[string]string{"base_url": v})
		if got := string(SandboxConfig(m, cfg, loopback)); got != string(cfg) {
			t.Errorf("%q: %s", v, got)
		}
	}
}

func TestHostLoopbackURL(t *testing.T) {
	for raw, want := range map[string]string{
		"http://host.persona.internal:8080/v1":    "http://127.0.0.1:8080/v1",
		"https://HOST.PERSONA.INTERNAL/v1":        "https://127.0.0.1/v1",
		"ws://host.persona.internal:9000/sock":    "ws://127.0.0.1:9000/sock",
		"http://notes.example.com:8080":           "http://notes.example.com:8080",
		"http://me:pw@host.persona.internal:8080": "http://me:pw@127.0.0.1:8080",
		"ftp://host.persona.internal:8080":        "ftp://host.persona.internal:8080",
		"":                                        "",
	} {
		if got := HostLoopbackURL(raw); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}
