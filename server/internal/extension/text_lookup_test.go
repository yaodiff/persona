package extension

import (
	"strings"
	"testing"
)

// The text_lookup point (docs/extensions/extensions.md "Looking up a word"): an id, an optional name and languages.
// Only "en" is accepted; omitting languages means ["en"]. It needs the program to speak the protocol.
func TestTextLookupPoint(t *testing.T) {
	rpc := func(items []any) []byte {
		return with(t, func(m map[string]any) {
			m["runtime"] = map[string]any{"command": "bin/x", "rpc": "stdio"}
			m["contributes"] = map[string]any{"text_lookup": items}
		})
	}
	m, err := Parse(rpc([]any{
		map[string]any{"id": "dict"},
		map[string]any{"id": "en_us", "name": "English", "languages": []any{"en"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Items("text_lookup"); strings.Join(got, ",") != "dict,en_us" {
		t.Fatalf("items %v", got)
	}
	p := Points["text_lookup"]
	if p.Version != 1 || p.Namespace != "text_lookup" || !p.NeedsRPC || p.NeedsHTTP {
		t.Fatalf("%+v", p)
	}
	if PointVersions()["text_lookup"] != 1 {
		t.Fatal("the point's version goes to the programs")
	}

	for name, items := range map[string][]any{
		"an empty list":     {},
		"a bad id":          {map[string]any{"id": "Bad ID"}},
		"an id too long":    {map[string]any{"id": strings.Repeat("a", 65)}},
		"no id":             {map[string]any{"name": "x"}},
		"an id twice":       {map[string]any{"id": "dict"}, map[string]any{"id": "dict"}},
		"another field":     {map[string]any{"id": "dict", "extra": true}},
		"a blank name":      {map[string]any{"id": "dict", "name": "  "}},
		"a name not a str":  {map[string]any{"id": "dict", "name": 3}},
		"another language":  {map[string]any{"id": "dict", "languages": []any{"ja"}}},
		"two languages":     {map[string]any{"id": "dict", "languages": []any{"en", "en"}}},
		"an empty language": {map[string]any{"id": "dict", "languages": []any{}}},
	} {
		if _, err := Parse(rpc(items)); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	// It needs the program to speak the protocol: the manager calls text_lookup.* on it.
	if _, err := Parse(with(t, func(m map[string]any) {
		m["runtime"] = map[string]any{"command": "bin/x", "rpc": "none"}
		m["contributes"] = map[string]any{"text_lookup": []any{map[string]any{"id": "dict"}}}
	})); err == nil || !strings.Contains(err.Error(), "text_lookup") {
		t.Fatalf("text_lookup without rpc: %v", err)
	}
}
