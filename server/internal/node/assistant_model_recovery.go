package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Visible, reversible model recovery for the main and leader/supervisor conversations (#7824).
//
// The runtime's own refusal fallback is recorded by the runtime itself (ModelRefusal). When the runtime does not
// switch, the node's bounded recovery (assistant_recovery.go) restarts it on the next launcher model. That switch used
// to exist only in memory and was invisible to clients; the records here make it a Session field the apps render,
// persist it through session state so a node restart keeps it, and return the conversation to its configured model as
// soon as that model answers an availability probe again. Worker Agent conversations keep their own cheaper-tier
// behavior and carry no marker.

// ModelRecoveryReason classifies the failure that moved a conversation off its configured model.
func ModelRecoveryReason(result string) string {
	t := strings.ToLower(strings.TrimSpace(result))
	switch {
	case assistantSafeguardsRefusal(result):
		return proto.ModelRecoveryReasonSafeguards
	case strings.Contains(t, "model_not_found") || strings.Contains(t, "model not found") ||
		strings.Contains(t, "model does not exist") || strings.Contains(t, "issue with the selected model"):
		return proto.ModelRecoveryReasonUnavailable
	case assistantServerError.MatchString(t):
		return proto.ModelRecoveryReasonServer
	case assistantRateLimit.MatchString(t) || strings.Contains(t, "rate limit") || strings.Contains(t, "rate_limit") ||
		strings.Contains(t, "too many requests"):
		if strings.Contains(t, "quota_exhausted") || strings.Contains(t, "quota usage limit") || strings.Contains(t, "usage limit") {
			return proto.ModelRecoveryReasonQuota
		}
		return proto.ModelRecoveryReasonRateLimit
	default:
		return proto.ModelRecoveryReasonServer
	}
}

const modelRecoveryProbeEvery = time.Minute

// GatewayAvailableAgain shares the transcript parser used by worker usage-limit waits.
func GatewayAvailableAgain(text string, at time.Time) (time.Time, bool) {
	return transcript.GatewayAvailableAgain(text, at)
}

// recoveryProtected says the conversation is the main one or a leader/supervisor one, the only conversations the
// owner's auto-switch-and-return rule applies to. The manager marks both through follows_setting (assistant/start.go);
// worker Agents have neither flag and keep their own fallback behavior.
func (n *Node) recoveryProtected(id string) bool {
	if n.state == nil {
		return false
	}
	st, ok := n.state.get(id)
	return ok && (st.IsMain || st.FollowsSetting)
}

// modelRecovery is the node-owned switch record for a conversation, hydrated from persisted session state.
// Returned markers are immutable snapshots: copy one before changing any field.
func (n *Node) modelRecovery(id string) *proto.ModelRecovery {
	n.mu.Lock()
	r := n.recoveryMarks[id]
	n.mu.Unlock()
	if r != nil {
		return r
	}
	if st, ok := n.state.get(id); ok && st.ModelRecovery != nil {
		return st.ModelRecovery
	}
	return nil
}

// modelRecoverySwitched says the marker is an active move to another model, not an exhausted or no-switch record.
// From may legitimately be empty when the conversation follows the launcher default.
func modelRecoverySwitched(r *proto.ModelRecovery) bool {
	return r != nil && !r.ChainExhausted && r.To != "" && r.From != r.To
}

// modelRecoveryProbeFrom is the concrete model to probe. Explicit and older persisted records use From; a launcher
// default keeps its requested From and carries its resolved identity separately.
func modelRecoveryProbeFrom(r *proto.ModelRecovery) string {
	if r == nil {
		return ""
	}
	if r.ProbeFrom != "" {
		return r.ProbeFrom
	}
	return r.From
}

// putModelRecovery stores the marker in memory and persists the session state so a node restart keeps it.
func (n *Node) putModelRecovery(id string, r *proto.ModelRecovery) {
	n.mu.Lock()
	n.putModelRecoveryLocked(id, r)
	n.mu.Unlock()
	n.persistModelRecovery(id)
}

// putModelRecoveryLocked updates only the in-memory marker. Caller must hold n.mu.
func (n *Node) putModelRecoveryLocked(id string, r *proto.ModelRecovery) {
	if r == nil {
		delete(n.recoveryMarks, id)
		return
	}
	if n.recoveryMarks == nil {
		n.recoveryMarks = map[string]*proto.ModelRecovery{}
	}
	n.recoveryMarks[id] = r
}

// persistModelRecovery copies the in-memory marker into just that field of the latest session record. The dedicated
// mutex keeps an older snapshot from persisting after a newer one. n.mu must not be held: it is taken only for the
// snapshot, before state.update takes the state lock and may notify over the peer.
func (n *Node) persistModelRecovery(id string) {
	n.recoveryPersistMu.Lock()
	defer n.recoveryPersistMu.Unlock()
	n.mu.Lock()
	r := n.recoveryMarks[id]
	n.mu.Unlock()
	n.state.update(id, func(v *sessionState) bool {
		v.ModelRecovery = r
		return true
	})
}

// recordRecoverySwitch marks that recovery moved id from its configured model to the model that answers now. The
// marker accumulates every model the chain tried and keeps the first failure's reason, reset time and probe identity.
func (n *Node) recordRecoverySwitch(id, launcher, from, probeFrom, to, reason string, resets *time.Time, tried []string, original ...modelExecution) {
	if !n.recoveryProtected(id) {
		return
	}
	prev := n.modelRecovery(id)
	var choice *proto.ModelRecoveryChoice
	if len(original) > 0 {
		e := original[0]
		choice = &proto.ModelRecoveryChoice{Effort: e.Effort, UseEffort: e.UseEffort, Fingerprint: e.Fingerprint, Launcher: launcher}
	} else {
		n.mu.Lock()
		if current := n.recoveries[id]; current != nil {
			e := current.from
			choice = &proto.ModelRecoveryChoice{Effort: e.Effort, UseEffort: e.UseEffort, Fingerprint: e.Fingerprint, Launcher: launcher}
		}
		n.mu.Unlock()
	}
	r := &proto.ModelRecovery{From: from, ProbeFrom: probeFrom, To: to, Reason: reason, At: time.Now().UTC(), Tried: append([]string{}, tried...), Choice: choice}
	if prev != nil && prev.From == from {
		if prev.Choice != nil && choice != nil && !prev.ChainExhausted {
			e := modelExecution{Model: prev.From, Effort: prev.Choice.Effort, UseEffort: prev.Choice.UseEffort, Fingerprint: prev.Choice.Fingerprint}
			current := modelExecution{Model: from, Effort: choice.Effort, UseEffort: choice.UseEffort, Fingerprint: choice.Fingerprint}
			if prev.Choice.Launcher == launcher && sameModelChoice(e, current) {
				r.Choice = prev.Choice // the first configured execution identity wins throughout one recovery chain
			}
		}
		if prev.ProbeFrom != "" {
			r.ProbeFrom = prev.ProbeFrom // the first configured identity wins throughout one recovery chain
		}
		if resets == nil {
			r.ResetsAt = prev.ResetsAt // keep the first reset the upstream named for this configured model
		}
	}
	if resets != nil {
		r.ResetsAt = resets
	}
	n.putModelRecovery(id, r)
	n.notifySessions()
}

// recordRecoveryExhausted marks that every model the chain could try failed: the turn ends on the failure card, which
// offers Retry with the cause and, when known, the reset time.
func (n *Node) recordRecoveryExhausted(id, reason string, resets *time.Time, tried []string) {
	if !n.recoveryProtected(id) {
		return
	}
	prev := n.modelRecovery(id)
	r := &proto.ModelRecovery{Reason: reason, At: time.Now().UTC(), Tried: append([]string{}, tried...), ChainExhausted: true}
	if prev != nil {
		r.From, r.ProbeFrom, r.To = prev.From, prev.ProbeFrom, prev.To
		r.Choice = prev.Choice
	}
	if resets != nil {
		r.ResetsAt = resets
	} else if prev != nil {
		r.ResetsAt = prev.ResetsAt
	}
	n.putModelRecovery(id, r)
	n.notifySessions()
}

// clearModelRecovery ends the marker once the configured model runs again, the conversation compacted its refused
// turns, or the owner picked another model.
func (n *Node) clearModelRecovery(id string) {
	if n.modelRecovery(id) == nil {
		return
	}
	n.putModelRecovery(id, nil)
	n.notifySessions()
}

// modelRecoveryLoop probes the configured model of every protected conversation while it answers on a fallback, and
// flips the marker to Returning once that model is reachable again. The actual switch happens on the next request
// (assistantSend), so a probe never spends a real conversation turn.
func (n *Node) modelRecoveryLoop(ctx context.Context) {
	t := time.NewTicker(modelRecoveryProbeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.probeModelRecoveries(ctx)
		}
	}
}

func (n *Node) probeModelRecoveries(ctx context.Context) {
	ids := map[string]bool{}
	n.mu.Lock()
	for id, r := range n.recoveryMarks {
		if modelRecoverySwitched(r) && modelRecoveryProbeFrom(r) != "" {
			ids[id] = true
		}
	}
	// A reconnect starts with an empty recoveryMarks map. Include markers that exist only in session state, with any
	// live map entry winning over its persisted copy. n.mu -> state.mu is the established lock order.
	n.state.mu.Lock()
	for id, st := range n.state.Sessions {
		if _, live := n.recoveryMarks[id]; live {
			continue
		}
		if r := st.ModelRecovery; modelRecoverySwitched(r) && modelRecoveryProbeFrom(r) != "" {
			ids[id] = true
		}
	}
	n.state.mu.Unlock()
	n.mu.Unlock()
	for id := range ids {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		ok := n.configuredModelAvailable(cctx, id)
		cancel()
		if !ok {
			continue
		}
		r := n.modelRecovery(id)
		if r == nil || r.Returning {
			continue
		}
		c := *r
		c.Returning = true
		n.putModelRecovery(id, &c)
		n.notifySessions()
	}
}

// returnToConfiguredAt decides whether id's next request must run on its configured model again. A successful
// availability probe (or an exhausted reset time that has passed) ends the fallback hold; an unprobed or still-failing
// configured model keeps the conversation on the fallback.
func (n *Node) returnToConfiguredAt(ctx context.Context, id string, at time.Time) bool {
	r := n.modelRecovery(id)
	if !modelRecoverySwitched(r) {
		return true
	}
	if r.ResetsAt != nil && at.After(*r.ResetsAt) {
		return true
	}
	if r.Returning {
		return true
	}
	return n.configuredModelAvailable(ctx, id)
}

// applyModelRecoveryReturn applies a protected conversation's temporary recovery to its next ordinary assistantSend.
// askedModel is the request before heldFallback, so an empty or literal default remains distinguishable from a concrete
// model. ProbeFrom is deliberately never copied into the execution.
func (n *Node) applyModelRecoveryReturn(ctx context.Context, id, askedModel string, execution modelExecution, holdExpired bool) modelExecution {
	if execution.NewModelChoice {
		// An explicit choice wins even when it names marker.To. Its old marker and hold are committed only after delivery.
		return execution
	}
	marker := n.modelRecovery(id)
	if !modelRecoverySwitched(marker) || !n.recoveryProtected(id) {
		return execution
	}
	if askedModel != marker.From && askedModel != marker.To {
		// Compatibility for callers that do not carry full-choice provenance. End the old marker without rewriting the request.
		n.clearModelRecovery(id)
		return execution
	}
	now := time.Now()
	// An existing hold that expired for this exact request counts as natural expiry. So does an aged persisted record
	// whose original full identity still matches: age must not turn it into a cold fallback.
	naturalExpiry := holdExpired && askedModel == marker.From && execution.Model == marker.From ||
		!marker.At.IsZero() && now.Sub(marker.At) >= assistantRefusalSticky && askedModel == marker.From && execution.Model == marker.From
	// Its two-hour sticky record must still skip a wasted same-model retry. A confirmed recovery/reset releases all recovery bookkeeping.
	confirmed := marker.Returning || marker.ResetsAt != nil && now.After(*marker.ResetsAt)
	if !confirmed && !naturalExpiry {
		confirmed = n.returnToConfiguredAt(ctx, id, now)
	}
	switch {
	case confirmed:
		// Confirmed recovery is stronger than simultaneous natural expiry: release the hold and unavailable record too.
		execution.Model = marker.From
		n.releaseModelRecovery(id, marker.From)
	case naturalExpiry:
		execution.Model = marker.From
		n.clearModelRecovery(id)
	default:
		if askedModel == marker.From && execution.Model == marker.From {
			execution.Model = marker.To // a cold restart has no hold, but the persisted fallback still applies
			execution.PersistedModel = false
			execution.FromRecovery = true
		}
	}
	return execution
}

// releaseModelRecovery clears every in-memory bookkeeping that would keep id off its configured model: the fallback
// hold, the sticky refusal window and the "unavailable" entry for the configured model. It takes n.mu itself, so it
// must not be called while n.mu is held; use releaseModelRecoveryLocked there.
func (n *Node) releaseModelRecovery(id, configured string) {
	n.mu.Lock()
	had := n.releaseModelRecoveryLocked(id, configured)
	n.mu.Unlock()
	if had {
		n.persistModelRecovery(id)
		n.notifySessions()
	}
}

// releaseModelRecoveryLocked does the n.mu-guarded half of a release and reports whether an in-memory or persisted
// marker was dropped. Caller must hold n.mu and must persist and notify only after releasing it (see
// releaseModelRecovery and clearAssistantRecovery, which already holds n.mu).
func (n *Node) releaseModelRecoveryLocked(id, configured string) (dropped bool) {
	marker := n.recoveryMarks[id]
	if marker == nil {
		st, ok := n.state.get(id) // n.mu -> state.mu is the established order; a reconnect may have state only.
		if ok {
			marker = st.ModelRecovery
		}
	}
	delete(n.fallbacks, id)
	launcher := ""
	if marker != nil && marker.Choice != nil {
		launcher = marker.Choice.Launcher
	}
	if launcher == "" {
		if st, ok := n.state.get(id); ok {
			launcher = st.Launcher
		}
	}
	probeFrom := modelRecoveryProbeFrom(marker)
	for k := range n.unavailable {
		l, model, ok := strings.Cut(k, "\x00")
		// Only this conversation's original launcher supplied the availability evidence. A same-named model on another
		// launcher, or on an unidentified one, keeps its own unavailable record.
		if !ok || l != launcher {
			continue
		}
		if model == configured || marker != nil && (model == marker.From || model == probeFrom) {
			delete(n.unavailable, k)
		}
	}
	if marker == nil {
		return false
	}
	n.putModelRecoveryLocked(id, nil)
	return true
}

func unavailableKey(launcher, model string) string { return launcher + "\x00" + model }

// configuredModelAvailable asks the launcher's gateway whether the conversation's configured model can answer now.
// A 2xx (including a content refusal, which still proves the model is served) or a 4xx other than 429 counts as
// reachable: the model exists and is authenticated. A 429/5xx or a connection failure keeps the fallback in place.
// Launchers without a reachable gateway answer false rather than guess, so the probe can never strand a conversation.
func (n *Node) configuredModelAvailable(ctx context.Context, id string) bool {
	// Probe the concrete configured model, never session state's Model: a recovery send overwrites that with the
	// fallback. For a launcher default, ProbeFrom carries that concrete identity without changing the requested From.
	r := n.modelRecovery(id)
	st, ok := n.state.get(id)
	probeFrom := modelRecoveryProbeFrom(r)
	if !modelRecoverySwitched(r) || probeFrom == "" || !ok || st.Launcher == "" {
		return false
	}
	return n.launcherModelAnswers(ctx, st.Launcher, probeFrom)
}

// launcherModelAnswers is one availability probe of model through launcher's gateway (assistantModelProbe, replaceable
// in tests): a one-token request whose answer says whether the model is served and has quota right now.
func (n *Node) launcherModelAnswers(ctx context.Context, launcher, model string) bool {
	return assistantModelProbe(ctx, n, launcher, model)
}

// assistantModelProbe is the real probe; tests replace it with a scripted upstream. Only a 2xx answer counts as
// available: a 429 (quota or throttle), a 5xx, a 404 (model not served), an auth failure or a connection error all keep
// the conversation on its fallback. A 200 that the upstream ends with a refusal still proves the model is served and
// has quota, so the conversation returns to it.
var assistantModelProbe = func(ctx context.Context, n *Node, launcher, model string) bool {
	base, key, headers := n.launcherGateway(ctx, launcher)
	if base == "" || key == "" || model == "" {
		return false
	}
	return probeGateway(ctx, base, key, n.resolvedLauncherModel(ctx, launcher, model), headers)
}

// probeGateway is the probe's HTTP seam: one one-token request to the gateway. A 2xx (including a stream ended with a
// refusal) proves the model is served and has quota; a 429/5xx/auth error or a connection failure keeps the fallback.
func probeGateway(ctx context.Context, base, key, model, headers string) bool {
	root, err := rootURL(base)
	if err != nil || model == "" || key == "" {
		return false
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(root, "/v1")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return false
	}
	for name, value := range piHeaders(headers) {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", key)
	req.Header.Set("Authorization", "Bearer "+key)
	modelhttp.SetDefaultUserAgent(req.Header)
	modelhttp.SetOpenCodeSession(req.Header, req.URL.String(), modelhttp.OpenCodeSessionHeaderValue(headers))
	resp, err := modelProbeHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return probeStatusOK(resp.StatusCode)
}

// probeStatusOK is the probe's rule: only a 2xx proves the configured model is served and has quota right now.
func probeStatusOK(status int) bool { return status >= 200 && status < 300 }

// modelProbeHTTPClient sends availability probes; a gateway that hangs must not hold the probe loop.
var modelProbeHTTPClient = &http.Client{Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// launcherGateway is the gateway address, key, and custom headers the launcher's agent sends with, layered as Claude Code layers them:
// the launcher's --settings env wins, then the user settings of its config directory, then the process environment.
// The key never leaves this machine and is never logged.
func (n *Node) launcherGateway(ctx context.Context, launcher string) (base, key, headers string) {
	p, err := n.launcherProbe(ctx, launcher)
	if err != nil {
		return "", "", ""
	}
	flag := map[string]string{}
	if arg := settingsArg(p.Args); arg != "" {
		if s, err := readLauncherSettings(arg, n.home, n.home); err == nil {
			flag = settingsEnv(s)
		}
	}
	dir := p.Env["CLAUDE_CONFIG_DIR"]
	if dir == "" {
		dir = filepath.Join(n.home, ".claude")
	}
	user := map[string]string{}
	if s, err := readLauncherSettings(filepath.Join(dir, "settings.json"), n.home, n.home); err == nil {
		user = settingsEnv(s)
	}
	get := func(k string) string {
		if v, ok := flag[k]; v != "" || (k == "ANTHROPIC_CUSTOM_HEADERS" && ok) {
			return v
		}
		if v, ok := user[k]; v != "" || (k == "ANTHROPIC_CUSTOM_HEADERS" && ok) {
			return v
		}
		return p.Env[k]
	}
	return firstNonEmpty(get("ANTHROPIC_BASE_URL"), get("OPENAI_BASE_URL")),
		firstNonEmpty(get("ANTHROPIC_AUTH_TOKEN"), get("ANTHROPIC_API_KEY"), get("OPENAI_API_KEY")),
		get("ANTHROPIC_CUSTOM_HEADERS")
}

// settingsEnv is a Claude Code settings document's string env values.
func settingsEnv(s map[string]any) map[string]string {
	out := map[string]string{}
	env, _ := s["env"].(map[string]any)
	for k, v := range env {
		if v, ok := v.(string); ok {
			out[k] = v
		}
	}
	return out
}

// resolvedLauncherModel is the concrete model an alias (opus, default…) stands for on launcher, so the probe asks the
// gateway for the model the conversation actually runs; the model itself when the list does not say.
func (n *Node) resolvedLauncherModel(ctx context.Context, launcher, model string) string {
	list, err := assistantRecoveryModels(ctx, n, launcher)
	if err != nil {
		return model
	}
	if r := resolvedAssistantDefault(list, model); r != "" {
		return r
	}
	for _, m := range list.Models {
		if m.ID == model && m.Resolved != "" {
			return m.Resolved
		}
	}
	return model
}

func rootURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("bad gateway url %q", base)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// decorateModelRecovery projects the node-owned marker onto Session.model_fallback, the shape every shipped client
// already renders as "switched to X" with "back to the configured model" (#2528, #5202). The runtime's own
// ModelFallback always wins: it is first-hand evidence. An exhausted chain is projected too (RecoveryExhausted, no
// FallbackModel), so the failure card can name the cause and reset time; the clients' fallback card stays hidden for
// it because their gate needs a fallback model (#7824).
func (n *Node) decorateModelRecovery(s *proto.Session) {
	if s == nil || s.ModelFallback != nil {
		return // no marker to show, or the runtime's own refusal fallback is the stronger record
	}
	r := n.modelRecovery(s.SessionID)
	if r == nil {
		return
	}
	original := r.From
	if original == "" {
		original = "default" // every client suppresses a returning line with an empty original; keep the default visible
	}
	f := &proto.ModelRefusal{
		Subtype:           "model_refusal_fallback",
		OriginalModel:     original,
		Category:          r.Reason,
		Scope:             "session",
		Explanation:       ModelRecoveryExplanation(r.Reason),
		RecoveryReset:     r.ResetsAt,
		RecoveryExhausted: r.ChainExhausted,
		RecoveryReturning: r.Returning,
		RecoveryTried:     strings.Join(r.Tried, " → "),
	}
	if !r.ChainExhausted && r.To != "" && r.From != r.To {
		f.FallbackModel = r.To
	}
	if f.FallbackModel == "" && !r.ChainExhausted {
		return // nothing true to say yet
	}
	s.ModelFallback = f
}

// ModelRecoveryExplanation is the public, non-internal reason text for a recovery reason enum.
func ModelRecoveryExplanation(reason string) string {
	switch reason {
	case proto.ModelRecoveryReasonSafeguards:
		return "the model's safety classifier refused the request"
	case proto.ModelRecoveryReasonQuota:
		return "the account's quota was exhausted"
	case proto.ModelRecoveryReasonRateLimit:
		return "the model service was rate limited"
	case proto.ModelRecoveryReasonUnavailable:
		return "the model was not available on the gateway"
	default:
		return "the model service failed"
	}
}
