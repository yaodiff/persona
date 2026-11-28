package node

import (
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Context usage of a session for the apps' agent rows and session header (#2430). The numbers come from the
// transcript tail readQuickMetaSince already parses: the latest reply's input tokens (transcript.Meta.ContextTokens)
// and, for Codex, the window its rollout records. Nothing here reads a file.

const (
	claudeWindow   = 200_000   // a Claude model's window without the 1M variant
	claudeWindow1M = 1_000_000 // the [1m] variants, and the models whose window is 1M without it
)

// contextWindowOf uses the transcript's window when available, then the known model or explicit [1m] variant.
// Claude transcripts can omit the variant: a reply over 200k is evidence that Claude used its 1M window.
func contextWindowOf(model string, recorded, used int) int {
	if recorded > 0 {
		return recorded
	}
	id := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(id, "[1m]") {
		return claudeWindow1M
	}
	if claude := claudeModelAfterRoute(id); claude != "" {
		if used > claudeWindow || claudeNative1M(claude) {
			return claudeWindow1M
		}
		return claudeWindow
	}
	id = id[strings.LastIndexByte(id, '/')+1:]
	if base := trimAgyEffortSuffix(id); base != "" {
		id = base
	}
	// agy 1.5.15 reports the active models' capacities in ACP usage_update.size. Older Gemini text
	// models also publish their input limits. Match whole IDs so custom and media variants stay unknown.
	switch id {
	case "gemini-4-neon", "gemini-4-carbon", "gemini-4-argon", "gemini-4-hydrogen",
		"gemini-pro-orchestrator", "gemini-pro-orchestrator-neon", "gemini-pro-orchestrator-argon",
		"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash",
		"gemini-3.1-pro-preview", "gemini-3-pro-preview", "gemini-3-flash-preview",
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite":
		return 1_048_576
	case "gpt-oss-120b":
		return 131_072
	}
	return 0
}

// lookupProviderModel finds model in l.Models, matching exact base IDs first, then "/" route suffixes from longest to
// shortest, and finally proto.SameLauncherModel (where the declared ID carries a vendor prefix and the transcript
// echoes the bare ID). Bracketed placeholders ("<synthetic>") match nothing.
func lookupProviderModel(l proto.ManagedLauncher, model string) (proto.ProviderModel, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" || isBracketedModel(trimmed) {
		return proto.ProviderModel{}, false
	}
	base := proto.TrimModelWindow(trimmed)
	if base == "" || isBracketedModel(base) {
		return proto.ProviderModel{}, false
	}
	for _, pm := range l.Models {
		if strings.EqualFold(base, proto.TrimModelWindow(pm.ID)) {
			return pm, true
		}
	}
	for i := 0; i < len(base); i++ {
		if base[i] != '/' {
			continue
		}
		suf := base[i+1:]
		if suf == "" {
			continue
		}
		for _, pm := range l.Models {
			if strings.EqualFold(suf, proto.TrimModelWindow(pm.ID)) {
				return pm, true
			}
		}
	}
	for _, pm := range l.Models {
		if proto.SameLauncherModel(trimmed, pm.ID) {
			return pm, true
		}
	}
	return proto.ProviderModel{}, false
}

// launcherContextWindow resolves the context window a managed launcher declares for a session's model, with the same
// precedence providerEnv uses for CLAUDE_CODE_MAX_CONTEXT_TOKENS: l.MaxContext when > 0, else the first matching
// Models[].ContextWindow > 0. Only a Claude model's 200k entry can stand for its 1M variant.
func launcherContextWindow(l proto.ManagedLauncher, used int, models ...string) int {
	if l.MaxContext > 0 {
		return l.MaxContext
	}
	for _, m := range models {
		if pm, ok := lookupProviderModel(l, m); ok && pm.ContextWindow > 0 {
			id := strings.ToLower(strings.TrimSpace(m))
			if pm.ContextWindow == claudeWindow && claudeModelAfterRoute(id) != "" && (used > claudeWindow || strings.Contains(id, oneMillionSuffix)) {
				return claudeWindow1M
			}
			return pm.ContextWindow
		}
	}
	return 0
}

// contextWindowWithLauncher resolves a session's context window: a window recorded in the transcript itself (Codex)
// comes first; otherwise a managed launcher's MaxContext or matching model ContextWindow supplies the window, keeping
// 1M promotion when l.MaxContext is unset and an explicit "[1m]" suffix or >200k observed usage exceeds a 200k catalog
// entry; when neither is declared, contextWindowOf decides.
func contextWindowWithLauncher(l *proto.ManagedLauncher, model string, recorded, used int, fallbackModels ...string) int {
	if recorded > 0 {
		return recorded
	}
	if l != nil {
		if w := launcherContextWindow(*l, used, append([]string{model}, fallbackModels...)...); w > 0 {
			return w
		}
	}
	return contextWindowOf(model, recorded, used)
}

// differentClaudeModels reports whether run and said name two distinct Claude models (neither is a dated spelling of
// the other), such as an overload fallback answering in place of the session's configured Claude model.
func differentClaudeModels(run, said string) bool {
	r := claudeModelAfterRoute(strings.ToLower(proto.TrimModelWindow(run)))
	s := claudeModelAfterRoute(strings.ToLower(proto.TrimModelWindow(said)))
	return r != "" && s != "" && !strings.HasPrefix(s, r) && !strings.HasPrefix(r, s)
}

// claudeModelAfterRoute is id or, for a gateway's routed name ("passthrough/claude-opus-5-5"), the Claude model after
// its last "/"; "" when neither names a Claude model. id is lower case.
func claudeModelAfterRoute(id string) string {
	if strings.HasPrefix(id, "claude-") {
		return id
	}
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		if tail := id[i+1:]; strings.HasPrefix(tail, "claude-") {
			return tail
		}
	}
	return ""
}

// claudeNative1M reports a Claude model whose window is 1M without the "[1m]" opt-in, as Claude Code's own model list
// has them (2.1.311, #5758): Opus from 4.7, Sonnet and Haiku from 5, every Fable and Mythos. Opus and Sonnet 4.6 run
// with 200k there unless "[1m]" is chosen, though the API (and the model catalog, models_catalog.json) allows them 1M.
// id is lower case, claude-<family>-<major>[-<minor>], perhaps with a date ("-20250929", or Vertex's "@20250929").
// Older ids that put the version first (claude-3-5-sonnet-…) are 200k.
func claudeNative1M(id string) bool {
	id, _, _ = strings.Cut(id, "@")
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	if len(parts) < 2 {
		return false
	}
	major, ok := modelVersionPart(parts[1])
	if !ok {
		return false
	}
	minor := 0
	if len(parts) > 2 {
		minor, _ = modelVersionPart(parts[2]) // a date, or no number, is no minor version
	}
	switch parts[0] {
	case "fable", "mythos":
		return true
	case "opus":
		return major > 4 || major == 4 && minor >= 7
	case "sonnet", "haiku":
		return major >= 5
	}
	return false
}

// modelVersionPart is one component of a model's version: one or two digits (a date's eight are not).
func modelVersionPart(s string) (int, bool) {
	if s == "" || len(s) > 2 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// setContext copies cached title provenance and cumulative output alongside the context fields.
// The context fields stay zero (and so off the wire) while the transcript has no reply yet.
func setContext(s *proto.Session, m quickMeta) {
	if m.codex && m.serviceTier != "" {
		s.ServiceTier = m.serviceTier
	}
	s.SetOutputTokens(m.outputTokens)
	s.ContextRestartRecommended = m.contextRestartRecommended
	s.TitleManual = s.TitleManual || m.titleManual
	if m.title != "" && s.Title == m.title || m.title == "" && m.titleSource == "prompt" {
		// A terminal's tool-only fallback still has known transcript provenance
		// once the complete scan proves that no custom/AI title exists.
		s.TitleSource = proto.ParseTitleSource(m.titleSource)
	}
	if m.contextTokens <= 0 {
		s.SetContextBreakdown(nil)
		return
	}
	s.ContextTokens = proto.Tokens32(m.contextTokens)
	s.ContextWindow = proto.Tokens32(contextWindowOf(m.model, m.contextWindow, m.contextTokens))
	if m.contextStats.Native == nil {
		if existing := s.ContextBreakdown(); existing != nil && existing.Source == proto.ContextBreakdownExact {
			fillBreakdownDerived(s, existing, 0)
			return
		}
	}
	s.SetContextBreakdown(transcript.BuildContextBreakdown(m.contextStats, int(s.ContextTokens), int(s.ContextWindow), 0, m.contextFullWindow))
}

// Claude Desktop's meter uses the effective budget when its process sets an earlier compaction threshold.
func setClaudeProcessWindow(s *proto.Session, env map[string]string) {
	limit := smallerCapacity(contextLimit(env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]), contextLimit(env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]))
	if limit > 0 {
		s.ContextWindow = proto.Tokens32(smallerCapacity(int(s.ContextWindow), limit))
	}
}

// setRuntimeContext is setContext for a runtime's session whose conversation is a transcript here (Paseo). The model
// the runtime runs it as decides the window when it names the 1M variant: the transcript records the id without the
// suffix, and behind a gateway another id altogether, so such a session read as 200k, or as no window at all, until
// a reply passed 200k (#4496). A window the transcript records itself stays, and so does the transcript's word while
// the session answers as another model: a refusal's fallback, or a reply from a Claude model that is not the one the
// runtime names (an overload fallback). The title is the runtime's too: it is the first prompt only when it is what
// Paseo makes of that prompt (runtimetitle.go, #8133).
func setRuntimeContext(s *proto.Session, m quickMeta) {
	setContext(s, m)
	runtimeTitleSource(s, m)
	if s.ContextTokens == 0 || m.contextWindow > 0 || m.modelFallback != nil {
		return
	}
	run := strings.ToLower(strings.TrimSpace(s.Model))
	if !strings.HasSuffix(run, oneMillionSuffix) {
		return
	}
	run = strings.TrimSuffix(run, oneMillionSuffix)
	said := strings.ToLower(strings.TrimSpace(m.model))
	// Two Claude ids that are not one model (neither begins the other: a transcript may add the model's date).
	if strings.HasPrefix(run, "claude-") && strings.HasPrefix(said, "claude-") && !strings.HasPrefix(said, run) && !strings.HasPrefix(run, said) {
		return
	}
	s.ContextWindow = claudeWindow1M
	if existing := s.ContextBreakdown(); existing != nil && existing.Source == proto.ContextBreakdownExact {
		fillBreakdownDerived(s, existing, 0)
		return
	}
	s.SetContextBreakdown(transcript.BuildContextBreakdown(m.contextStats, int(s.ContextTokens), int(s.ContextWindow), 0, m.contextFullWindow))
}

// sessionManagedLauncher returns the managed launcher associated with session id (explicit row Launcher first, else
// ownLauncher: state record, runtime row, or inferred shell/managed launcher).
func (n *Node) sessionManagedLauncher(id, explicit string) (proto.ManagedLauncher, bool) {
	if n == nil {
		return proto.ManagedLauncher{}, false
	}
	name := explicit
	if name == "" && id != "" {
		if n.state != nil {
			if st, ok := n.state.get(id); ok && st.Launcher != "" {
				name = st.Launcher
			}
		}
		if name == "" {
			if s, _, listed := n.runtimes.rowOf(id); listed && s.Launcher != "" {
				name = s.Launcher
			}
		}
		if name == "" {
			name = n.inferLauncher(id)
		}
	}
	if name == "" {
		return proto.ManagedLauncher{}, false
	}
	return n.managedLauncher(name)
}

// sessionFallbackModels returns the session's configured/displayed model IDs to try after m.model when no refusal or
// cross-Claude overload fallback occurred on the latest reply.
func (n *Node) sessionFallbackModels(s *proto.Session, m quickMeta) []string {
	if s == nil || m.modelFallback != nil || differentClaudeModels(s.Model, m.model) {
		return nil
	}
	var requested string
	if s.RequestedModel != nil && *s.RequestedModel != "" {
		requested = *s.RequestedModel
	} else if n != nil && n.state != nil && s.SessionID != "" {
		if st, ok := n.state.get(s.SessionID); ok && st.Model != "" {
			requested = st.Model
		}
	}
	hasShown := s.Model != "" && s.Model != m.model
	hasReq := requested != "" && requested != m.model && requested != s.Model
	switch {
	case hasShown && hasReq:
		return []string{s.Model, requested}
	case hasShown:
		return []string{s.Model}
	case hasReq:
		return []string{requested}
	default:
		return nil
	}
}

// sessionContextWindow computes the context window for session id from quickMeta m, falling back to its managed
// launcher's MaxContext / Models when the transcript itself records no window.
func (n *Node) sessionContextWindow(id, explicitLauncher string, s *proto.Session, m quickMeta) int {
	if m.contextWindow > 0 {
		return m.contextWindow
	}
	l, ok := n.sessionManagedLauncher(id, explicitLauncher)
	if !ok {
		return contextWindowOf(m.model, m.contextWindow, m.contextTokens)
	}
	// When the reply came from a refusal or overload fallback onto a known Claude model, keep that fallback model's own
	// window rather than attributing the configured launcher's capacity to it.
	if s != nil && (m.modelFallback != nil || differentClaudeModels(s.Model, m.model)) {
		if w := contextWindowOf(m.model, m.contextWindow, m.contextTokens); w > 0 {
			return w
		}
		return contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens)
	}
	return contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens, n.sessionFallbackModels(s, m)...)
}

// sessionAutoCompactThreshold returns the session's explicitly known auto-compact token threshold
// from in-memory node state, managed launcher configuration, or active assistant process policy.
// Returns 0 when not explicitly known (never fabricated).
func (n *Node) sessionAutoCompactThreshold(id, explicitLauncher string, window int) int {
	if n == nil {
		return 0
	}
	threshold := 0
	if id != "" && n.state != nil {
		// The window the manager held it to: the main conversation's or a leader conversation's (#15318).
		if st, ok := n.state.get(id); ok && st.MainCompactWindow > 0 {
			threshold = smallerCapacity(threshold, st.MainCompactWindow)
		}
	}
	if l, ok := n.sessionManagedLauncher(id, explicitLauncher); ok && l.CompactWindow > 0 {
		threshold = smallerCapacity(threshold, l.CompactWindow)
	}
	if id != "" {
		n.mu.Lock()
		p := n.procs[id]
		n.mu.Unlock()
		if p != nil {
			p.mu.Lock()
			dw := p.declaredWindow
			p.mu.Unlock()
			if dw > 0 && (window <= 0 || dw < window) {
				threshold = smallerCapacity(threshold, dw)
			}
		}
	}
	if window > 0 && threshold > window {
		threshold = window
	}
	return threshold
}

func fillBreakdownDerived(s *proto.Session, existing *proto.ContextBreakdown, autoCompact int) {
	if s == nil || existing == nil {
		return
	}
	cp := existing.Clone()
	if cp.Free == nil && s.ContextWindow > 0 && s.ContextTokens > 0 {
		cp.Free = proto.Int(max(int(s.ContextWindow)-int(s.ContextTokens), 0))
	}
	if cp.AutoCompactThreshold == nil && autoCompact > 0 {
		if s.ContextWindow > 0 && autoCompact > int(s.ContextWindow) {
			autoCompact = int(s.ContextWindow)
		}
		cp.AutoCompactThreshold = proto.Int(autoCompact)
	}
	s.SetContextBreakdown(cp)
}

// setSessionContext is setContext with a managed-launcher fallback when the transcript records no context window.
func (n *Node) setSessionContext(s *proto.Session, m quickMeta) {
	var native *proto.ContextBreakdown
	if s != nil {
		if existing := s.ContextBreakdown(); existing != nil && existing.Source == proto.ContextBreakdownExact {
			native = existing.Clone()
		}
	}
	setContext(s, m)
	if s == nil || s.ContextTokens == 0 {
		return
	}
	if m.contextWindow <= 0 {
		s.ContextWindow = proto.Tokens32(n.sessionContextWindow(s.SessionID, s.Launcher, s, m))
	}
	autoCompact := n.sessionAutoCompactThreshold(s.SessionID, s.Launcher, int(s.ContextWindow))
	if native != nil && m.contextStats.Native == nil {
		fillBreakdownDerived(s, native, autoCompact)
		return
	}
	if existing := s.ContextBreakdown(); existing != nil && existing.Source == proto.ContextBreakdownExact {
		fillBreakdownDerived(s, existing, autoCompact)
		return
	}
	s.SetContextBreakdown(transcript.BuildContextBreakdown(m.contextStats, int(s.ContextTokens), int(s.ContextWindow), autoCompact, m.contextFullWindow))
}

// setRuntimeSessionContext is setRuntimeContext with a managed-launcher fallback when the transcript records no window.
func (n *Node) setRuntimeSessionContext(s *proto.Session, m quickMeta) {
	var native *proto.ContextBreakdown
	if s != nil {
		if existing := s.ContextBreakdown(); existing != nil && existing.Source == proto.ContextBreakdownExact {
			native = existing.Clone()
		}
	}
	setRuntimeContext(s, m)
	if s == nil || s.ContextTokens == 0 {
		return
	}
	if m.contextWindow <= 0 {
		if l, ok := n.sessionManagedLauncher(s.SessionID, s.Launcher); ok {
			if m.modelFallback != nil || differentClaudeModels(s.Model, m.model) {
				if s.ContextWindow == 0 {
					s.ContextWindow = proto.Tokens32(contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens))
				}
			} else if w := contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens, n.sessionFallbackModels(s, m)...); w > 0 {
				// Keep a runtime's explicit [1m] variant at 1M when MaxContext is unset and the catalog entry says 200k.
				if !(l.MaxContext <= 0 && w == claudeWindow && s.ContextWindow == claudeWindow1M) {
					s.ContextWindow = proto.Tokens32(w)
				}
			}
		}
	}
	autoCompact := n.sessionAutoCompactThreshold(s.SessionID, s.Launcher, int(s.ContextWindow))
	if native != nil && m.contextStats.Native == nil {
		fillBreakdownDerived(s, native, autoCompact)
		return
	}
	if existing := s.ContextBreakdown(); existing != nil && existing.Source == proto.ContextBreakdownExact {
		fillBreakdownDerived(s, existing, autoCompact)
		return
	}
	s.SetContextBreakdown(transcript.BuildContextBreakdown(m.contextStats, int(s.ContextTokens), int(s.ContextWindow), autoCompact, m.contextFullWindow))
}

// applyLauncherContextWindow fills ContextWindow on a decorated non-Codex row (such as a cached offline Claude row)
// whose transcript reported tokens without a recorded window before Launcher or the launcher catalog was available.
func (n *Node) applyLauncherContextWindow(s *proto.Session) {
	if n == nil || s == nil || s.ContextTokens == 0 || s.Launcher == "" || s.Agent == "codex" || s.ModelFallback != nil {
		return
	}
	if s.Agent == agyAgent {
		n.resolveAgyContextWindow(s)
		return
	}
	if s.ContextWindow != 0 && s.ContextWindow != claudeWindow {
		return
	}
	l, ok := n.managedLauncher(s.Launcher)
	if !ok {
		return
	}
	var req string
	if s.RequestedModel != nil {
		req = *s.RequestedModel
	}
	w := launcherContextWindow(l, int(s.ContextTokens), s.Model, req)
	if w > 0 {
		s.ContextWindow = proto.Tokens32(w)
		if existing := s.ContextBreakdown(); existing != nil {
			cp := existing.Clone()
			cp.Free = proto.Int(max(int(s.ContextWindow)-int(s.ContextTokens), 0))
			if autoCompact := n.sessionAutoCompactThreshold(s.SessionID, s.Launcher, int(s.ContextWindow)); autoCompact > 0 && cp.AutoCompactThreshold == nil {
				cp.AutoCompactThreshold = proto.Int(autoCompact)
			}
			s.SetContextBreakdown(cp)
		}
	}
}
