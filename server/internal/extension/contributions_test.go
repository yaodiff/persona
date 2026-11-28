package extension

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestContributionSwitches(t *testing.T) {
	ctx := context.Background()
	r, store, changes := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		c := m["contributes"].(map[string]any)
		c["providers"] = []any{
			map[string]any{"id": "notes-api", "name": "Notes API", "base_url": "https://api.notes.example.com"},
			map[string]any{"id": "notes-mini", "name": "Notes Mini", "base_url": "https://mini.notes.example.com"},
		}
		c["ui"] = []any{map[string]any{"id": "usage", "title": "用量", "placement": "extension_page"}}
	})
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	*changes = nil

	if err := r.SetContribution(ctx, "example-notes", "providers", "nope", false, "device:x"); err == nil {
		t.Fatal("no such contribution")
	}
	if err := r.SetContribution(ctx, "example-notes", "connectors", "examplenotes", false, "device:x"); !errors.Is(err, ErrSwitchedElsewhere) {
		t.Fatal("a connector's switch is the connector's:", err)
	}
	if err := r.SetContribution(ctx, "example-notes", "providers", "notes-mini", false, "device:x"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetContribution(ctx, "example-notes", "ui", "usage", false, "device:x"); err != nil {
		t.Fatal(err)
	}
	p := r.Get("example-notes")
	if ids := p.Manifest.Items("providers"); !slices.Equal(ids, []string{"notes-api"}) {
		t.Fatal("what takes effect:", ids)
	}
	if p.Manifest.Items("ui") != nil || p.Manifest.Contributes["ui"] != nil {
		t.Fatal("the only ui is off: the point is gone from what takes effect")
	}
	if ids := p.Manifest.Items("connectors"); !slices.Equal(ids, []string{"examplenotes"}) {
		t.Fatal("the connector stays:", ids)
	}
	if ids := p.Full.Items("providers"); len(ids) != 2 || p.Full.Items("ui") == nil {
		t.Fatal("the full manifest keeps all:", ids)
	}
	if got := store.rows["example-notes"].DisabledContributions; !slices.Equal(got, []string{"providers/notes-mini", "ui/usage"}) {
		t.Fatal("kept:", got)
	}
	if len(*changes) != 2 || (*changes)[0].Kind != "contributions" {
		t.Fatalf("%+v", *changes)
	}
	// The same again changes nothing.
	if err := r.SetContribution(ctx, "example-notes", "ui", "usage", false, "device:x"); err != nil || len(*changes) != 2 {
		t.Fatal(err, *changes)
	}

	// Read back from the store: the switches hold.
	r2 := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if ids := r2.Get("example-notes").Manifest.Items("providers"); !slices.Equal(ids, []string{"notes-api"}) {
		t.Fatal("after a restart:", ids)
	}

	// A UI ID belongs to its extension even when switched off; another extension has its own usage page.
	other := t.TempDir()
	writePackage(t, other, func(m map[string]any) {
		m["id"] = "other"
		m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "usage", "title": "x", "placement": "extension_page"}}}
	})
	if _, err := r.Preview(ctx, "dir:"+other, ""); err != nil {
		t.Fatal("other/usage is independent of example-notes/usage:", err)
	}
	// Other contribution points still reserve their IDs while switched off.
	writePackage(t, other, func(m map[string]any) {
		m["id"] = "other-provider"
		m["contributes"] = map[string]any{"providers": []any{map[string]any{"id": "notes-mini", "name": "Other", "base_url": "https://example.com"}}}
	})
	if _, err := r.Preview(ctx, "dir:"+other, ""); err == nil {
		t.Fatal("provider notes-mini is still example-notes's")
	}

	// Back on.
	if err := r.SetContribution(ctx, "example-notes", "ui", "usage", true, "device:x"); err != nil {
		t.Fatal(err)
	}
	if r.Get("example-notes").Manifest.Items("ui") == nil {
		t.Fatal("on again")
	}
	if title := ContributionTitle(r.Get("example-notes").Full, "providers", "notes-mini"); title != "Notes Mini" {
		t.Fatal(title)
	}
}

// Skills, assistant commands and MCP servers carry a display-only title; without one the switch keeps the id.
func TestContributionTitlesOfAssistantItems(t *testing.T) {
	m, err := Parse(with(t, func(m map[string]any) {
		m["contributes"] = map[string]any{
			"skills": []any{
				map[string]any{"id": "search", "title": "Search code", "dir": "skills/search"},
				map[string]any{"id": "plain", "dir": "skills/plain"},
			},
			"assistant": []any{map[string]any{
				"id": "search-command", "title": "Search command", "skills": []any{"skills/search"},
			}},
			"mcp_servers": []any{map[string]any{
				"id": "search-server", "title": "Search server", "command": "search-server",
			}},
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ point, id, want string }{
		{"skills", "search", "Search code"},
		{"skills", "plain", "plain"},
		{"assistant", "search-command", "Search command"},
		{"mcp_servers", "search-server", "Search server"},
	} {
		if got := ContributionTitle(m, c.point, c.id); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.point, c.id, got, c.want)
		}
	}
}

func TestWithoutKeepsTheManifest(t *testing.T) {
	full, err := Parse(with(t, func(map[string]any) {}))
	if err != nil {
		t.Fatal(err)
	}
	if full.Without(nil) != full || full.Without([]string{"providers/nope"}) != full {
		t.Fatal("nothing taken out: the same manifest")
	}
	if full.Without([]string{"connectors/examplenotes"}) != full {
		t.Fatal("a connector is not taken out here")
	}
}

func TestOffByDefault(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	pkg := t.TempDir()
	write := func(version string, uis ...string) {
		writePackage(t, pkg, func(m map[string]any) {
			m["version"] = version
			var items []any
			for _, id := range uis {
				items = append(items, map[string]any{"id": id, "title": id, "placement": "extension_page"})
			}
			m["contributes"].(map[string]any)["ui"] = items
			m["off_by_default"] = []any{"ui/page", "ui/later"}[:len(uis)-1]
		})
	}
	write("1.0.0", "usage", "page")
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Install(ctx, pv.ID, pv.Digest, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Record.DisabledContributions; !slices.Equal(got, []string{"ui/page"}) {
		t.Fatal("installed with the page off:", got)
	}
	if ids := p.Manifest.Items("ui"); !slices.Equal(ids, []string{"usage"}) || !p.Record.Enabled {
		t.Fatal("the extension is on, the rest of it too:", ids, p.Record.Enabled)
	}
	// The user turns the page on; an upgrade that adds another off-by-default item keeps that choice and switches
	// only the new one off.
	if err := r.SetContribution(ctx, "example-notes", "ui", "page", true, "device:x"); err != nil {
		t.Fatal(err)
	}
	write("1.1.0", "usage", "page", "later")
	pv, err = r.Preview(ctx, "dir:"+pkg, "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if p, err = r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	if got := store.rows["example-notes"].DisabledContributions; !slices.Equal(got, []string{"ui/later"}) {
		t.Fatal("after the upgrade:", got)
	}
	if ids := p.Manifest.Items("ui"); !slices.Equal(ids, []string{"usage", "page"}) {
		t.Fatal("what takes effect:", ids)
	}
}

func TestOffByDefaultChecked(t *testing.T) {
	for name, off := range map[string][]any{
		"not a contribution": {"ui/nope"},
		"no point":           {"usage"},
		"twice":              {"ui/usage", "ui/usage"},
		"a connector":        {"connectors/examplenotes"},
	} {
		raw := with(t, func(m map[string]any) {
			m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "usage", "title": "用量", "placement": "extension_page"}}
			m["off_by_default"] = off
		})
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), "off_by_default") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
