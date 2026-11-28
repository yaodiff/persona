package proto

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Engine hosts (#2576, docs/protocol/protocol.md "Engines", "Host sets"): a machine enables a set of terminal hosts side by side — any of
// herdr, herdr-core, orca and paseo, or none — and names one of them its default, where sessions started from the
// phone go unless the start says otherwise. The single engines before it (EngineNames) are sets of one; herdr-both
// is {herdr, herdr-core} with herdr the default. The hub keeps both forms: the set for nodes that take one
// (NodeInfo.EngineSets), the single engine for older nodes and for a rollback to an older hub.

// EngineHostIDs are the hosts a set may hold, in their canonical order: a set is kept and sent in this order, and a
// set without a default takes the first of it that it holds (classic herdr, the one with every capability, first).
var EngineHostIDs = []string{EngineHerdr, EngineHerdrCore, EngineOrca, EngineTern, EngineRex, EnginePaseo, EngineCindy, EngineT3Code}

// EngineChoice is a machine's enabled hosts and its default host ("" exactly when Hosts is empty).
type EngineChoice struct {
	Hosts   []string `json:"hosts"`
	Default string   `json:"default"`
}

// IsEngineHost reports whether id may be in a set (herdr-both is a set itself, not a member).
func IsEngineHost(id string) bool { return slices.Contains(EngineHostIDs, id) }

// EngineHostsOf is the set a single engine stands for: herdr-both is herdr and herdr-core with herdr the default,
// any other engine a set of itself; an unknown one is herdr, as a machine never set.
func EngineHostsOf(engine string) EngineChoice {
	switch {
	case engine == EngineHerdrBoth:
		return EngineChoice{Hosts: []string{EngineHerdr, EngineHerdrCore}, Default: EngineHerdr}
	case IsEngineHost(engine):
		return EngineChoice{Hosts: []string{engine}, Default: engine}
	}
	return EngineChoice{Hosts: []string{EngineHerdr}, Default: EngineHerdr}
}

// NormalizeEngineChoice checks a set and puts it in canonical form: every host known, each once, in EngineHostIDs'
// order (never nil); the default one of them, the first of them when not given, and "" for the empty set.
func NormalizeEngineChoice(hosts []string, def string) (EngineChoice, error) {
	seen := map[string]bool{}
	for _, h := range hosts {
		if !IsEngineHost(h) {
			return EngineChoice{}, fmt.Errorf("unknown engine host %q (herdr, herdr-core, orca, tern, rex, paseo, cindy, t3code)", h)
		}
		seen[h] = true
	}
	out := EngineChoice{Hosts: []string{}}
	for _, h := range EngineHostIDs {
		if seen[h] {
			out.Hosts = append(out.Hosts, h)
		}
	}
	switch {
	case len(out.Hosts) == 0:
		if def != "" {
			return EngineChoice{}, fmt.Errorf("default host %q: no host is enabled", def)
		}
	case def == "":
		out.Default = out.Hosts[0]
	case seen[def]:
		out.Default = def
	default:
		return EngineChoice{}, fmt.Errorf("default host %q is not one of the enabled hosts", def)
	}
	return out, nil
}

// LegacyEngine is the single engine closest to a set, for nodes and hubs that know only one: the set's own engine
// when it is one (a host alone, herdr with herdr-core), else its default; herdr for the empty set (an old node
// cannot run without a host and ran herdr before any choice).
func LegacyEngine(c EngineChoice) string {
	switch {
	case len(c.Hosts) == 1:
		return c.Hosts[0]
	case slices.Equal(c.Hosts, []string{EngineHerdr, EngineHerdrCore}):
		return EngineHerdrBoth
	case c.Default != "":
		return c.Default
	}
	return EngineHerdr
}

// LegacyEngineOnto is the set a single engine from an older client ({"engine": X}) leaves on a machine whose set is
// stored: when no single engine stands for the stored set (herdr, Orca and Paseo built on the web, say) and the set
// already holds every host of X, X only becomes its default — a phone that can name one engine must not drop the
// others with a Save (#17052). Otherwise X's own set, as before: an engine outside the set, and any set a single
// engine stands for (a host alone, herdr with herdr-core), which the older client could show and so replaces.
func LegacyEngineOnto(stored EngineChoice, engine string) EngineChoice {
	want := EngineHostsOf(engine)
	if LegacyExpresses(stored) {
		return want
	}
	for _, h := range want.Hosts {
		if !slices.Contains(stored.Hosts, h) {
			return want
		}
	}
	return EngineChoice{Hosts: slices.Clone(stored.Hosts), Default: want.Default}
}

// SameEngineChoice reports whether two sets are the same hosts with the same default.
func SameEngineChoice(a, b EngineChoice) bool {
	return a.Default == b.Default && slices.Equal(a.Hosts, b.Hosts)
}

// LegacyExpresses reports whether a node that knows only single engines runs the set as chosen: LegacyEngine of it
// stands for exactly it.
func LegacyExpresses(c EngineChoice) bool { return SameEngineChoice(EngineHostsOf(LegacyEngine(c)), c) }

// SuggestEngineHosts is the set offered to a newly paired machine: every host the node found available, herdr when
// it found none (as before sets, and herdr can be installed from the phone).
func SuggestEngineHosts(found []Engine) EngineChoice {
	var hosts []string
	for _, e := range found {
		if e.Available && IsEngineHost(e.ID) {
			hosts = append(hosts, e.ID)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{EngineHerdr}
	}
	c, _ := NormalizeEngineChoice(hosts, "") // only known hosts: cannot fail
	return c
}

// StartHost is the host a session.start's params name ("" : none), for the hub's check that the node takes it.
func StartHost(params any) string {
	raw, _ := json.Marshal(params)
	var f struct {
		Host string `json:"host"`
	}
	_ = json.Unmarshal(raw, &f)
	return f.Host
}
