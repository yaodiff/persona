package proto

import (
	"slices"
	"testing"
)

func TestNormalizeEngineChoice(t *testing.T) {
	for _, c := range []struct {
		hosts     []string
		def       string
		wantHosts []string
		wantDef   string
		bad       bool
	}{
		{hosts: []string{"orca", "herdr", "orca"}, wantHosts: []string{"herdr", "orca"}, wantDef: "herdr"}, // canonical order, once each
		{hosts: []string{"orca", "herdr"}, def: "orca", wantHosts: []string{"herdr", "orca"}, wantDef: "orca"},
		{hosts: []string{"paseo"}, wantHosts: []string{"paseo"}, wantDef: "paseo"},
		{hosts: []string{"paseo", "tern", "herdr"}, def: "tern", wantHosts: []string{"herdr", "tern", "paseo"}, wantDef: "tern"}, // Tern after Orca, before Paseo
		{hosts: []string{"paseo", "rex", "tern"}, def: "rex", wantHosts: []string{"tern", "rex", "paseo"}, wantDef: "rex"},       // Rex after Tern, before Paseo
		{hosts: []string{"cindy"}, wantHosts: []string{"cindy"}, wantDef: "cindy"},
		{hosts: []string{"cindy", "herdr"}, def: "cindy", wantHosts: []string{"herdr", "cindy"}, wantDef: "cindy"},
		{hosts: []string{"cindy", "tern"}, def: "tern", wantHosts: []string{"tern", "cindy"}, wantDef: "tern"}, // Tern before Cindy
		{hosts: []string{"t3code", "cindy"}, wantHosts: []string{"cindy", "t3code"}, wantDef: "cindy"},         // Cindy before T3 Code
		{hosts: nil, wantHosts: []string{}, wantDef: ""},                                                       // the empty set: no default
		{hosts: []string{}, def: "herdr", bad: true},
		{hosts: []string{"herdr"}, def: "orca", bad: true}, // default not enabled
		{hosts: []string{"tmux"}, bad: true},
		{hosts: []string{"herdr-both"}, bad: true}, // a set, not a member
	} {
		got, err := NormalizeEngineChoice(c.hosts, c.def)
		if c.bad {
			if err == nil {
				t.Errorf("%v/%q: no error, got %v", c.hosts, c.def, got)
			}
			continue
		}
		if err != nil || got.Hosts == nil || !slices.Equal(got.Hosts, c.wantHosts) || got.Default != c.wantDef {
			t.Errorf("%v/%q = %v, %v; want %v/%q", c.hosts, c.def, got, err, c.wantHosts, c.wantDef)
		}
	}
}

// Every single engine maps to a set and back to itself; sets map to the closest single engine.
func TestLegacyEngineRoundTrip(t *testing.T) {
	for engine := range EngineNames {
		c := EngineHostsOf(engine)
		if got := LegacyEngine(c); got != engine {
			t.Errorf("%s → %v → %s", engine, c, got)
		}
		if !LegacyExpresses(c) {
			t.Errorf("%s: its own set is not expressible", engine)
		}
	}
	both := EngineHostsOf(EngineHerdrBoth)
	if !slices.Equal(both.Hosts, []string{"herdr", "herdr-core"}) || both.Default != "herdr" {
		t.Errorf("herdr-both = %v", both)
	}
	if got := EngineHostsOf("tmux"); !slices.Equal(got.Hosts, []string{"herdr"}) {
		t.Errorf("unknown engine = %v", got)
	}
	for _, c := range []struct {
		hosts     []string
		def       string
		legacy    string
		expresses bool
	}{
		{[]string{"herdr", "orca"}, "herdr", "herdr", false},
		{[]string{"herdr", "orca"}, "orca", "orca", false},
		{[]string{"herdr", "herdr-core"}, "herdr-core", "herdr-both", false}, // herdr-both defaults to herdr
		{[]string{}, "", "herdr", false},
	} {
		n, err := NormalizeEngineChoice(c.hosts, c.def)
		if err != nil {
			t.Fatal(err)
		}
		if got := LegacyEngine(n); got != c.legacy || LegacyExpresses(n) != c.expresses {
			t.Errorf("%v/%s: legacy %s expresses %v", c.hosts, c.def, got, LegacyExpresses(n))
		}
	}
}

// An older client's single engine on a stored set (#17052): it only moves the default of a set no single engine stands
// for when the set already holds it; outside the set, and on sets a single engine stands for, it replaces as before.
func TestLegacyEngineOnto(t *testing.T) {
	set := func(def string, hosts ...string) EngineChoice {
		c, err := NormalizeEngineChoice(hosts, def)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	web := set("orca", "herdr", "orca", "paseo")
	for _, c := range []struct {
		name   string
		stored EngineChoice
		engine string
		want   EngineChoice
	}{
		{"in the set: default only", web, "herdr", set("herdr", "herdr", "orca", "paseo")},
		{"the default again: unchanged", web, "orca", web},
		{"outside the set: replaces", web, "herdr-core", set("herdr-core", "herdr-core")},
		{"herdr-both inside a larger set: default herdr", set("orca", "herdr", "herdr-core", "orca"), "herdr-both", set("herdr", "herdr", "herdr-core", "orca")},
		{"herdr-both half outside: replaces", web, "herdr-both", set("herdr", "herdr", "herdr-core")},
		{"a host alone: replaces", set("orca", "orca"), "herdr", set("herdr", "herdr")},
		{"herdr-both narrowed: replaces", set("herdr", "herdr", "herdr-core"), "herdr", set("herdr", "herdr")},
		{"herdr with herdr-core by default: default only", set("herdr-core", "herdr", "herdr-core"), "herdr", set("herdr", "herdr", "herdr-core")},
		{"the empty set: replaces", set(""), "herdr", set("herdr", "herdr")},
	} {
		got := LegacyEngineOnto(c.stored, c.engine)
		if !SameEngineChoice(got, c.want) {
			t.Errorf("%s: %v + %s = %v, want %v", c.name, c.stored, c.engine, got, c.want)
		}
	}
	// The stored set is not shared with the result.
	got := LegacyEngineOnto(web, "paseo")
	got.Hosts[0] = "changed"
	if web.Hosts[0] != "herdr" {
		t.Errorf("stored set changed: %v", web)
	}
}

func TestSuggestEngineHosts(t *testing.T) {
	found := []Engine{
		{ID: "herdr", Available: false}, {ID: "herdr-core", Available: true}, {ID: "herdr-both", Available: false},
		{ID: "orca", Available: true}, {ID: "paseo", Available: false}, {ID: "future", Available: true},
	}
	if got := SuggestEngineHosts(found); !slices.Equal(got.Hosts, []string{"herdr-core", "orca"}) || got.Default != "herdr-core" {
		t.Errorf("suggested %v", got)
	}
	found[0].Available = true
	if got := SuggestEngineHosts(found); got.Default != "herdr" {
		t.Errorf("herdr found: default %q", got.Default)
	}
	for _, none := range [][]Engine{nil, {{ID: "orca"}}} {
		if got := SuggestEngineHosts(none); !slices.Equal(got.Hosts, []string{"herdr"}) || got.Default != "herdr" {
			t.Errorf("nothing found %v: %v", none, got)
		}
	}
}
