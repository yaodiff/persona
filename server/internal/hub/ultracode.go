package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// sessionModes keeps model and effort changes in their existing RPC (the node applies the model first), then
// changes the independent session-only ultracode switch. Sending it to session.modes would let older nodes
// silently ignore it and claim success. Dismissing a resume-loss notice uses the same new RPC without changing
// the switch: {"ultracode_dismiss_lost":true} becomes {"dismiss_lost":true}.
func (h *Hub) sessionModes(w http.ResponseWriter, r *http.Request, m *Machine) {
	p, ok := optionalObject(w, r, 32<<20)
	if !ok {
		return
	}
	if pr := principalOf(r.Context()); pr.Role == roleExtension { // extension_modes.go
		if why := h.extensionModeRefused(pr, m.cfg.ID, r.PathValue("sid"), p); why != "" {
			mode, _ := p["permission_mode"].(string)
			h.refuseExtensionMode(w, r, pr, m.cfg.ID, r.PathValue("sid"), mode, why)
			return
		}
	}
	if principalOf(r.Context()).Role == roleScoped {
		if mode, ok := p["permission_mode"].(string); !ok || mode == "" || len(p) != 1 {
			writeErr(w, http.StatusBadRequest, errors.New("provide only a nonempty permission_mode"))
			return
		}
	}
	if restrictedAgentData(r) && !h.publicSessionForControl(m, r.PathValue("sid")) {
		writeErr(w, http.StatusNotFound, errDeleteUnknown)
		return
	}
	// Keep speed separate from session.modes, which older nodes ignore unknown fields in.
	if tier, exists := p["service_tier"]; exists {
		if value, ok := tier.(string); !ok || value == "" || len(p) != 1 {
			writeErr(w, http.StatusBadRequest, errors.New("service_tier must be a nonempty string in a separate request"))
			return
		}
		out, ok := h.callNode(w, r, m, "session.speed", map[string]any{"session_id": r.PathValue("sid"), "service_tier": tier}, 30*time.Second, "rpc-errors.node_outdated", nil)
		if !ok {
			return
		}
		writeJSON(w, out)
		return
	}
	ultra := map[string]any{"session_id": r.PathValue("sid")}
	for _, field := range []struct{ http, rpc string }{{"ultracode", "ultracode"}, {"ultracode_dismiss_lost", "dismiss_lost"}} {
		if v, exists := p[field.http]; exists {
			if _, ok := v.(bool); !ok {
				writeErr(w, http.StatusBadRequest, errors.New(field.http+" must be a boolean"))
				return
			}
			ultra[field.rpc] = v
			delete(p, field.http)
		}
	}
	// A switch to another launcher (#5035), carried out by the user's next message (node/launcher_switch.go); from is
	// the launcher the client saw the session on. An older node would drop launcher and switch the model on the
	// launcher the Agent runs on.
	for _, field := range []string{"launcher", "from"} {
		if v, exists := p[field]; exists {
			if _, ok := v.(string); !ok {
				writeErr(w, http.StatusBadRequest, errors.New(field+" must be a string"))
				return
			}
		}
	}
	if launcher, _ := p["launcher"].(string); strings.TrimSpace(launcher) != "" && !m.switchesLaunchers() {
		writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(h.requestLang(r), "hub-machine.node_no_launcher_switch"), nil)
		return
	}
	p["session_id"] = r.PathValue("sid")
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	// Both node calls share the original 90-second deadline.
	r = r.WithContext(ctx)
	var out json.RawMessage
	modesApplied := false
	if len(p) > 1 || len(ultra) == 1 {
		sid := r.PathValue("sid")
		var ok bool
		out, ok = h.callNode(w, r, m, "session.modes", p, 90*time.Second, "", nil, func(err error) bool {
			if m.oldEngineRefusal(sid, p, err) {
				writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(h.requestLang(r), "hub-machine.node_no_engine_switch"), nil)
				return true
			}
			return false
		})
		if !ok {
			return
		}
		modesApplied = modesReplyChanged(p, out)
		if model, _ := p["model"].(string); strings.TrimSpace(model) != "" { // switched (the node read it back), or picked with a launcher
			s, _ := m.session(sid)
			h.modelUsed(h.sessionOwner(m.cfg.ID, sid), m.cfg.ID, modesLauncher(p, s), model)
		}
	}
	if len(ultra) > 1 {
		var (
			next json.RawMessage
			ok   bool
		)
		if modesApplied {
			next, ok = h.callNode(w, r, m, "session.ultracode", ultra, 90*time.Second, "", nil, func(err error) bool {
				return h.writePartialModesError(w, r, err)
			})
		} else {
			next, ok = h.callNode(w, r, m, "session.ultracode", ultra, 90*time.Second, "hub-ultracode.node_outdated", nil)
		}
		if !ok {
			return
		}
		if len(next) > 0 {
			out = next
		}
	}
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	writeRawJSON(w, out)
}

// writePartialModesError answers a failed second RPC (session.ultracode) after session.modes already succeeded in the
// same /modes request: it preserves the refusal's code and details while marking delivered="unknown" and partial=true
// so client_ops records the combined request as "unknown" instead of "failed" and users are not told nothing changed.
func (h *Hub) writePartialModesError(w http.ResponseWriter, r *http.Request, err error) bool {
	var re *rpc.Error
	if errors.As(err, &re) && strings.HasPrefix(re.Message, "unknown method") {
		lang := h.requestLang(r)
		writeCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(lang, "rpc-errors.modes_partial_node_outdated"), map[string]any{
			"delivered": "unknown",
			"partial":   true,
			"retryable": false,
			"detail":    i18n.T(lang, "hub-ultracode.node_outdated"),
		})
		return true
	}
	if errors.Is(err, errManagerOffline) {
		lang := h.requestLang(r)
		writeCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, i18n.T(lang, "rpc-errors.modes_partial_failed"), map[string]any{
			"delivered": "unknown",
			"partial":   true,
			"retryable": false,
			"detail":    i18n.Localize(errAssistantOffline, lang),
		})
		return true
	}
	lw := routeLanguageWriter{ResponseWriter: w, language: h.requestLang(r)}
	if errors.Is(err, apierr.ErrOffline) {
		body := apierr.CallStatusBody(lw, http.StatusServiceUnavailable, err)
		if _, hasDetail := body["detail"]; !hasDetail {
			body["detail"] = body["error"]
		}
		body["error"] = i18n.T(lw.language, "rpc-errors.modes_partial_failed")
		body["delivered"], body["partial"], body["retryable"] = "unknown", true, false
		apierr.WriteBody(lw, http.StatusServiceUnavailable, body)
		return true
	}
	if !errors.As(err, &re) {
		status := http.StatusBadGateway
		body := apierr.CallStatusBody(lw, status, err)
		if _, hasDetail := body["detail"]; !hasDetail {
			body["detail"] = body["error"]
		}
		body["error"] = i18n.T(lw.language, "rpc-errors.modes_partial_failed")
		body["delivered"], body["partial"], body["retryable"] = "unknown", true, false
		apierr.WriteBody(lw, status, body)
		return true
	}
	var data map[string]any
	if len(re.Data) > 0 {
		_ = json.Unmarshal(re.Data, &data)
	}
	if data == nil {
		data = map[string]any{}
	}
	data["delivered"] = "unknown"
	data["partial"] = true
	data["retryable"] = false
	msg := re.Message
	if re.Code != "" {
		partialKey := "rpc-errors.modes_partial_failed"
		switch re.Code {
		case "not_delivered":
			if reason, _ := data["reason"].(string); reason == "terminal_finished" {
				partialKey = "rpc-errors.modes_partial_terminal_finished"
			} else {
				partialKey = "rpc-errors.modes_partial_not_delivered"
			}
		case "dialog_open":
			partialKey = "rpc-errors.modes_partial_dialog_open"
		}
		if _, hasDetail := data["detail"]; !hasDetail && re.Message != "" {
			data["detail"] = re.Message
		}
		msg = i18n.T(i18n.Default, partialKey)
	}
	b, _ := json.Marshal(data)
	callErr(lw, &rpc.Error{Code: re.Code, Message: msg, Data: b})
	return true
}

// modesReplyChanged reports whether a successful session.modes reply confirmed (or, for an older node that omits
// "changed", was asked for) a settings change before a subsequent session.ultracode RPC runs.
func modesReplyChanged(p map[string]any, out json.RawMessage) bool {
	var reply struct {
		Changed       *bool  `json:"changed"`
		Applied       string `json:"applied"`
		PendingEffort string `json:"pending_effort"`
	}
	if json.Unmarshal(out, &reply) == nil && reply.Changed != nil {
		return *reply.Changed
	}
	if reply.Applied != "" || reply.PendingEffort != "" {
		return true
	}
	for _, k := range []string{"model", "permission_mode", "effort", "launcher"} {
		if s, _ := p[k].(string); strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// modesLauncher is the launcher a model in a POST …/modes body p runs on: the launcher it names (a switch, #5035),
// else the session's own.
func modesLauncher(p map[string]any, s proto.Session) string {
	if l, _ := p["launcher"].(string); strings.TrimSpace(l) != "" {
		return strings.TrimSpace(l)
	}
	if s.Launcher != "" {
		return s.Launcher
	}
	switch {
	case s.Agent == "codex" && s.AgentSurface == "desktop":
		return "codex-app"
	case s.Agent == "codex":
		return "codex"
	case s.SourceKind == "claude_desktop" || s.SpaceKind == "claude_desktop":
		return "claude-desktop"
	default:
		return "claude"
	}
}

// oldEngineRefusal says whether err, a node's refusal of POST …/modes with a launcher (p), is a node without
// engine_switch (#5054) refusing a launcher of the other engine: a Claude Code session's pick of a launcher of another
// agent (switch_other_agent), or a Codex session's pick of a launcher at all (switch_unsupported). The apps then say
// the node needs an update rather than the node's own reason, which reads as if the pick could never work.
func (m *Machine) oldEngineRefusal(sid string, p map[string]any, err error) bool {
	if launcher, _ := p["launcher"].(string); strings.TrimSpace(launcher) == "" || m.hasCapability(nodeproto.EngineSwitch) {
		return false
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		return false
	}
	switch re.Code {
	case proto.CodeSwitchOtherAgent:
		return true
	case proto.CodeSwitchUnsupported:
		s, ok := m.session(sid)
		return ok && (s.Agent == "codex" || s.Agent == "agy")
	}
	return false
}

// switchesLaunchers: the machine's node takes launcher in session.modes (capability launcher_switch), as its manager
// reported it. Not before the manager reported the node at all: hasCapability takes a node that says nothing for a
// test's, and an older node met right after a hub restart would drop the launcher and switch the model in place.
func (m *Machine) switchesLaunchers() bool {
	m.mu.Lock()
	reported := m.info.Protocol > 0 || m.info.Version != ""
	m.mu.Unlock()
	return reported && m.hasCapability(nodeproto.LauncherSwitch)
}
