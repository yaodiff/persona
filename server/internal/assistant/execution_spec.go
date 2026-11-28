package assistant

import (
	"cmp"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The four-dimension execution model (docs/assistant/execution-spec.md): Runtime × Channel × Model × Effort, judged
// against three declarations the hub intersects at the time of asking: what each runtime declares it speaks
// (builtinRuntimeCaps, overridable through assistant.execution), what each channel declares it serves (a managed
// channel's own declaration, a managed launcher's provider, a computer's native sign-in or shell launcher), and what
// each computer is ready for (the launchers the hub last saw there, and the managed channels' device scopes). Nothing
// here names a channel, a gateway or a model: those come from the declarations.
//
// This file is pure: execDecl is built by execDecl (execution_decl.go) and judged here.

// builtinRuntimeCaps are the built-in adapters' own declarations: the protocols each runtime speaks to a gateway, how
// it takes an effort, the efforts it takes and what a running session can switch for its next turn. A runtime that is
// not here (a plugin's) declares itself in assistant.execution; until it does it runs only on its own sign-in or
// shell launchers.
func builtinRuntimeCaps() []proto.RuntimeCapability {
	return []proto.RuntimeCapability{
		{Runtime: "claude", Protocols: []proto.WireProtocol{proto.ProtocolAnthropicMessages}, EffortVia: proto.EffortCLIFlag,
			Efforts: slices.Clone(proto.EffortLevels), LiveSwitch: []string{"model", "effort"}, Managed: true},
		{Runtime: "omp", Protocols: []proto.WireProtocol{proto.ProtocolAnthropicMessages}, EffortVia: proto.EffortNone, Managed: true},
		{Runtime: "codex", Protocols: []proto.WireProtocol{proto.ProtocolOpenAIResponses}, EffortVia: proto.EffortCLIFlag,
			Efforts: slices.Clone(proto.CodexEffortLevels), LiveSwitch: []string{"model", "effort"}, Managed: true},
		{Runtime: "agy", Protocols: []proto.WireProtocol{proto.ProtocolGeminiNative}, EffortVia: proto.EffortACPConfig,
			LiveSwitch: []string{"model"}, Managed: true},
		{Runtime: "pi", Protocols: []proto.WireProtocol{proto.ProtocolAnthropicMessages, proto.ProtocolOpenAIResponses}},
	}
}

// mergeRuntimeCaps is base with each of over replacing the declaration of its runtime (or added), by runtime name.
func mergeRuntimeCaps(base, over []proto.RuntimeCapability) map[string]proto.RuntimeCapability {
	out := map[string]proto.RuntimeCapability{}
	for _, list := range [][]proto.RuntimeCapability{base, over} {
		for _, c := range list {
			if c.Runtime = strings.TrimSpace(c.Runtime); c.Runtime != "" {
				out[c.Runtime] = c
			}
		}
	}
	return out
}

// execAlias is a launcher as the alias of one (runtime, channel) pair, with its default model and effort.
type execAlias struct {
	name string
	spec proto.ExecutionSpec
}

// execDecl is the three declarations, and the launcher aliases that project them onto launchers.
type execDecl struct {
	runtimes map[string]proto.RuntimeCapability
	channels map[string]proto.ChannelSupply
	machines map[string]proto.MachineReadiness
	// aliases are sorted by name, one per name.
	aliases []execAlias
}

// alias is the launcher called name as an alias; false when there is none.
func (d execDecl) alias(name string) (execAlias, bool) {
	i := slices.IndexFunc(d.aliases, func(a execAlias) bool { return a.name == name })
	if i < 0 {
		return execAlias{}, false
	}
	return d.aliases[i], true
}

// launcherFor is the launcher alias that runs runtime on channel, the first by name; "" when none does.
func (d execDecl) launcherFor(runtime, channel string) string {
	for _, a := range d.aliases {
		if a.spec.Runtime == runtime && a.spec.Channel == channel {
			return a.name
		}
	}
	return ""
}

// specOfLauncher is the four dimensions a launcher stands for, with model and effort as given (empty: the alias's
// defaults are not filled in, so a pick keeps following the launcher's own default); false for an unknown launcher.
func (d execDecl) specOfLauncher(launcher, model, effort string) (proto.ExecutionSpec, bool) {
	a, ok := d.alias(launcher)
	if !ok {
		return proto.ExecutionSpec{}, false
	}
	return proto.ExecutionSpec{Runtime: a.spec.Runtime, Channel: a.spec.Channel, Model: model, Effort: effort}, true
}

// execReason is one reason of a verdict, worded later in the request's language.
type execReason struct {
	code proto.CombinationReasonCode
	dim  string
	args i18n.Args
}

// execVerdict is a judgement before it is worded.
type execVerdict struct {
	spec         proto.ExecutionSpec
	tier         proto.EdgeTier
	protocol     proto.WireProtocol
	wireModel    string
	wireEffort   string
	launcher     string
	reasons      []execReason
	alternatives []proto.ExecutionSpec
}

func (v *execVerdict) add(code proto.CombinationReasonCode, dim string, args i18n.Args) {
	v.reasons = append(v.reasons, execReason{code: code, dim: dim, args: args})
}

// blocked: some reason blocks it.
func (v execVerdict) blocked() bool {
	return slices.ContainsFunc(v.reasons, func(r execReason) bool { return r.code.Blocks() })
}

// level is the judgement.
func (v execVerdict) level() proto.CombinationLevel {
	switch {
	case v.blocked():
		return proto.CombinationBlocked
	case len(v.reasons) > 0:
		return proto.CombinationNotice
	}
	return proto.CombinationAllowed
}

// recommended: it belongs in the everyday recommendations (not blocked, not an experimental bridge).
func (v execVerdict) recommended() bool {
	return !v.blocked() && v.tier != proto.EdgeBridgedExperimental
}

// rank orders runnable verdicts, best first: silent before noticed, then by edge tier.
func (v execVerdict) rank() int {
	r := v.tier.Rank()
	if v.level() == proto.CombinationNotice {
		r += 10
	}
	return r
}

// err is the first blocking reason as an error worded in the caller's language; nil when it runs.
func (v execVerdict) err() error {
	for _, r := range v.reasons {
		if r.code.Blocks() {
			return i18n.Err("assistant-execution.reason_"+string(r.code), r.args)
		}
	}
	return nil
}

// wire is the verdict worded in lang, blocking reasons first.
func (v execVerdict) wire(lang i18n.Lang) proto.CombinationVerdict {
	out := proto.CombinationVerdict{Spec: v.spec, Level: v.level(), Tier: v.tier, Protocol: v.protocol, WireModel: v.wireModel,
		WireEffort: v.wireEffort, Launcher: v.launcher, Recommended: v.recommended(), Alternatives: v.alternatives}
	reasons := slices.Clone(v.reasons)
	slices.SortStableFunc(reasons, func(a, b execReason) int {
		return cmp.Compare(blockOrder(a.code), blockOrder(b.code))
	})
	for _, r := range reasons {
		out.Reasons = append(out.Reasons, proto.CombinationReason{Code: r.code, Dimension: r.dim,
			Text: i18n.T(lang, "assistant-execution.reason_"+string(r.code), r.args)})
	}
	return out
}

func blockOrder(c proto.CombinationReasonCode) int {
	if c.Blocks() {
		return 0
	}
	return 1
}

// splitEffortSuffix reads id as "<base>-<effort>" when its last part is a thinking level (none aside).
func splitEffortSuffix(id string) (base, effort string, ok bool) {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 {
		return "", "", false
	}
	e := id[i+1:]
	if e == "none" || !slices.Contains(proto.EffortWords, e) {
		return "", "", false
	}
	return id[:i], e, true
}

// channelModels is a provider's model list by canonical id: a listed id that is another listed id with a thinking
// level appended ("m-high" beside "m") is that model at that effort, not a model of its own, and the upstream then
// takes the effort as the suffix over protocol. Ids are folded only when their base is listed too, so nothing is
// guessed from a name alone.
func channelModels(models []proto.ProviderModel, protocol proto.WireProtocol) []proto.ChannelModel {
	listed := map[string]bool{}
	for _, m := range models {
		listed[m.ID] = true
	}
	var out []proto.ChannelModel
	suffixed := map[string][]string{}
	for _, m := range models {
		if base, effort, ok := splitEffortSuffix(m.ID); ok && listed[base] {
			suffixed[base] = append(suffixed[base], effort)
			continue
		}
		efforts := m.Efforts
		if len(m.AllEfforts) > 0 {
			efforts = m.AllEfforts
		}
		out = append(out, proto.ChannelModel{Canonical: m.ID, Name: m.Name, Efforts: slices.Clone(efforts),
			DefaultEffort: m.DefaultEffort, ContextWindow: m.ContextWindow, MaxOutputTokens: m.MaxOutputTokens})
	}
	for i := range out {
		extra, ok := suffixed[out[i].Canonical]
		if !ok {
			continue
		}
		out[i].Efforts = proto.ProjectEfforts(append(out[i].Efforts, extra...), proto.EffortWords)
		if protocol != "" {
			out[i].Edges = []proto.ChannelEdge{{Protocol: protocol, Tier: proto.EdgeNative, EffortVia: proto.EffortModelSuffix}}
		}
	}
	return out
}

// findModel is the model of ch that id names, and the effort id carries as a suffix ("" when none); listed is false
// when ch lists no such model (or no models at all).
func findModel(ch proto.ChannelSupply, id string) (m proto.ChannelModel, suffixEffort string, listed bool) {
	if id == "" {
		return proto.ChannelModel{}, "", false
	}
	if m, ok := ch.Model(id); ok {
		return m, "", true
	}
	if base, effort, ok := splitEffortSuffix(id); ok {
		if m, ok := ch.Model(base); ok {
			return m, effort, true
		}
	}
	return proto.ChannelModel{}, "", false
}

// edgesOf are the protocols ch serves m over: the model's own declaration, else every endpoint of the channel, as
// its only protocol (native) or one of several (a stable bridge, unless the channel says otherwise per model).
func edgesOf(ch proto.ChannelSupply, m proto.ChannelModel) []proto.ChannelEdge {
	if len(m.Edges) > 0 {
		return m.Edges
	}
	tier := proto.EdgeBridgedStable
	if len(ch.Endpoints) == 1 {
		tier = proto.EdgeNative
	}
	out := make([]proto.ChannelEdge, 0, len(ch.Endpoints))
	for _, p := range ch.Endpoints {
		out = append(out, proto.ChannelEdge{Protocol: p, Tier: tier})
	}
	return out
}

// bestEdge is the best edge of m on ch that rc speaks: the best tier, then the runtime's preferred protocol.
func bestEdge(rc proto.RuntimeCapability, ch proto.ChannelSupply, m proto.ChannelModel) (proto.ChannelEdge, bool) {
	var best proto.ChannelEdge
	found := false
	for _, e := range edgesOf(ch, m) {
		if !rc.Speaks(e.Protocol) {
			continue
		}
		if !found || e.Tier.Rank() < best.Tier.Rank() ||
			e.Tier.Rank() == best.Tier.Rank() && slices.Index(rc.Protocols, e.Protocol) < slices.Index(rc.Protocols, best.Protocol) {
			best, found = e, true
		}
	}
	return best, found
}

// clampEffort is effort as one of supported takes it: itself when listed (or when nothing is listed), else the
// strongest listed level below it, else the weakest listed one.
func clampEffort(effort string, supported []string) string {
	if len(supported) == 0 || slices.Contains(supported, effort) {
		return effort
	}
	at := slices.Index(proto.EffortWords, effort)
	best, bestAt := "", -1
	for _, s := range supported {
		if i := slices.Index(proto.EffortWords, s); i >= 0 && i <= at && i > bestAt {
			best, bestAt = s, i
		}
	}
	if best != "" {
		return best
	}
	return supported[0]
}

func protocolList(ps []proto.WireProtocol) string {
	if len(ps) == 0 {
		return "-"
	}
	words := make([]string, len(ps))
	for i, p := range ps {
		words[i] = string(p)
	}
	return strings.Join(words, ", ")
}

// judge is the verdict of exactly in (every dimension it names; an empty model is the channel's default, an empty
// effort the model's) on machine ("" judges no readiness).
func (d execDecl) judge(in proto.ExecutionSpec, machine string) execVerdict {
	s := in.Trimmed()
	v := execVerdict{spec: s}
	rc, ok := d.runtimes[s.Runtime]
	if !ok {
		v.add(proto.ReasonRuntimeUnknown, "runtime", i18n.Args{"runtime": s.Runtime})
		return v
	}
	ch, ok := d.channels[s.Channel]
	if !ok {
		v.add(proto.ReasonChannelUnknown, "channel", i18n.Args{"channel": s.Channel})
		return v
	}
	label := cmp.Or(ch.DisplayName, ch.Name)
	m, suffixEffort, listed := findModel(ch, s.Model)
	if listed {
		v.spec.Model = m.Canonical
		if v.spec.Effort == "" {
			v.spec.Effort = suffixEffort
		}
	}
	if s.Model != "" && !listed && len(ch.Models) > 0 {
		v.add(proto.ReasonModelNotOffered, "model", i18n.Args{"channel": label, "model": s.Model})
		return v
	}
	var edge proto.ChannelEdge
	if ch.Kind == proto.ChannelManaged {
		e, ok := bestEdge(rc, ch, m)
		if !ok {
			offers := ch.Endpoints
			if listed && len(m.Edges) > 0 {
				offers = nil
				for _, x := range m.Edges {
					offers = append(offers, x.Protocol)
				}
			}
			v.add(proto.ReasonNoCommonProtocol, "channel", i18n.Args{"runtime": s.Runtime, "channel": label,
				"needs": protocolList(rc.Protocols), "offers": protocolList(offers)})
			return v
		}
		edge, v.tier, v.protocol = e, e.Tier, e.Protocol
		if s.Model != "" && len(ch.Models) == 0 {
			v.add(proto.ReasonModelUnlisted, "model", i18n.Args{"channel": label, "model": s.Model})
		}
	} else {
		if !slices.Contains(ch.Runtimes, s.Runtime) {
			v.add(proto.ReasonNoCommonProtocol, "channel", i18n.Args{"runtime": s.Runtime, "channel": label,
				"needs": protocolList(rc.Protocols), "offers": strings.Join(ch.Runtimes, ", ")})
			return v
		}
		v.tier = proto.EdgeNative
	}
	if v.tier == proto.EdgeBridgedExperimental {
		v.add(proto.ReasonBridgedExperimental, "channel", i18n.Args{"channel": label})
	}
	for _, w := range edge.Warnings {
		v.add(proto.ReasonBridgeWarning, "channel", i18n.Args{"warning": i18n.Verbatim(w)})
	}
	if e := v.spec.Effort; e != "" && e != "auto" {
		supported := rc.Efforts
		if listed && len(m.Efforts) > 0 {
			supported = m.Efforts
		}
		switch {
		case rc.EffortVia == proto.EffortNone:
			v.add(proto.ReasonEffortIgnored, "effort", i18n.Args{"runtime": s.Runtime, "effort": e})
		default:
			v.wireEffort = clampEffort(e, supported)
			if v.wireEffort != e {
				v.add(proto.ReasonEffortClamped, "effort", i18n.Args{"model": cmp.Or(v.spec.Model, label), "effort": e,
					"clamped": v.wireEffort})
			}
		}
	}
	v.wireModel = v.spec.Model
	switch {
	case edge.WireModel != "":
		v.wireModel = edge.WireModel
	case edge.EffortVia == proto.EffortModelSuffix && v.wireEffort != "" && v.spec.Model != "":
		v.wireModel = v.spec.Model + "-" + v.wireEffort
	}
	if v.launcher = d.launcherFor(s.Runtime, s.Channel); v.launcher == "" {
		v.add(proto.ReasonNoLauncher, "runtime", i18n.Args{"runtime": s.Runtime, "channel": label})
		return v
	}
	if machine != "" {
		d.judgeMachine(&v, ch, label, machine)
	}
	return v
}

// judgeMachine adds what machine's readiness says of v.
func (d execDecl) judgeMachine(v *execVerdict, ch proto.ChannelSupply, label, machine string) {
	mr, ok := d.machines[machine]
	if !ok {
		v.add(proto.ReasonMachineUnknown, "machine", i18n.Args{"machine": machine})
		return
	}
	name := cmp.Or(mr.Name, machine)
	if !mr.Online {
		v.add(proto.ReasonMachineOffline, "machine", i18n.Args{"machine": name})
	}
	if ch.Kind == proto.ChannelManaged && !slices.Contains(mr.Channels, ch.Name) {
		v.add(proto.ReasonChannelOutOfScope, "channel", i18n.Args{"channel": label, "machine": name})
	}
	if !mr.Known {
		v.add(proto.ReasonReadinessUnknown, "machine", i18n.Args{"machine": name})
		return
	}
	if !slices.Contains(mr.Runtimes, v.spec.Runtime) {
		v.add(proto.ReasonRuntimeNotReady, "runtime", i18n.Args{"runtime": v.spec.Runtime, "machine": name})
		return
	}
	if ch.Kind != proto.ChannelManaged && !slices.Contains(mr.Channels, ch.Name) {
		v.add(proto.ReasonChannelNotReady, "channel", i18n.Args{"runtime": v.spec.Runtime, "machine": name})
	}
}

// resolve judges in with its empty runtime or channel filled as the hub would run it: of the launcher aliases that
// agree with what in names, the best one that runs (silent before noticed, the better edge, then by name). With every
// dimension named, or nothing that runs, it is in's own verdict (or the first candidate's), with alternatives when
// blocked.
func (d execDecl) resolve(in proto.ExecutionSpec, machine string) execVerdict {
	s := in.Trimmed()
	if s.Runtime == "" || s.Channel == "" {
		var best, first *execVerdict
		for _, a := range d.aliases {
			if s.Runtime != "" && a.spec.Runtime != s.Runtime || s.Channel != "" && a.spec.Channel != s.Channel {
				continue
			}
			c := s
			c.Runtime, c.Channel = a.spec.Runtime, a.spec.Channel
			v := d.judge(c, machine)
			if first == nil {
				first = &v
			}
			if !v.blocked() && (best == nil || v.rank() < best.rank()) {
				best = &v
			}
		}
		if best != nil {
			return *best
		}
		if first != nil {
			s = first.spec
		}
	}
	v := d.judge(s, machine)
	if v.blocked() {
		v.alternatives = d.alternatives(v.spec, machine)
	}
	return v
}

// maxAlternatives bounds a blocked verdict's suggestions.
const maxAlternatives = 3

// alternatives are up to maxAlternatives combinations that run the same model and effort instead of s: other launcher
// aliases' (runtime, channel), the ones sharing a dimension with s first, then the better verdict.
func (d execDecl) alternatives(s proto.ExecutionSpec, machine string) []proto.ExecutionSpec {
	type alt struct {
		spec proto.ExecutionSpec
		near int
		rank int
	}
	var alts []alt
	for _, a := range d.aliases {
		if a.spec.Runtime == s.Runtime && a.spec.Channel == s.Channel {
			continue
		}
		c := s
		c.Runtime, c.Channel = a.spec.Runtime, a.spec.Channel
		if slices.ContainsFunc(alts, func(x alt) bool { return x.spec.Runtime == c.Runtime && x.spec.Channel == c.Channel }) {
			continue
		}
		v := d.judge(c, machine)
		if v.blocked() {
			continue
		}
		near := 1
		if a.spec.Runtime == s.Runtime || a.spec.Channel == s.Channel {
			near = 0
		}
		alts = append(alts, alt{spec: v.spec, near: near, rank: v.rank()})
	}
	slices.SortStableFunc(alts, func(a, b alt) int {
		return cmp.Or(cmp.Compare(a.near, b.near), cmp.Compare(a.rank, b.rank))
	})
	var out []proto.ExecutionSpec
	for _, a := range alts {
		if len(out) == maxAlternatives {
			break
		}
		out = append(out, a.spec)
	}
	return out
}
