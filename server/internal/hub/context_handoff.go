package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

func (h *Hub) continueInNewChat(w http.ResponseWriter, r *http.Request, m *Machine) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, i18n.Err("hub-context_handoff.full_device"))
		return
	}
	sid := r.PathValue("sid")
	source, ok := m.session(sid)
	if !ok || paneAddress(sid) {
		writeErr(w, http.StatusNotFound, errDeleteUnknown)
		return
	}
	if h.machineUntrusted(m.cfg.ID) || h.mainOf(m.cfg.ID, sid) || source.Archived ||
		source.IsSubagent || source.ReadOnlyReason != "" || source.Space != "" ||
		source.Compaction != nil && source.Compaction.State == "running" {
		writeErr(w, http.StatusConflict, i18n.Err("hub-context_handoff.unavailable"))
		return
	}
	if !source.Assistant && (source.Cwd == "" || source.Launcher == "") {
		writeErr(w, http.StatusConflict, i18n.Err("hub-context_handoff.target_missing"))
		return
	}
	// Finish an admitted operation even if the browser loses its connection. The receipt keeps a retry from starting twice.
	r = r.Clone(context.WithoutCancel(r.Context()))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var old struct {
		Summary string                `json:"summary"`
		Recent  []prompts.HandoffTurn `json:"recent"`
	}
	if err := m.call(ctx, assistantwire.DeviceAssistantHandoff, map[string]string{"session_id": sid}, &old); err != nil {
		callErr(w, err)
		return
	}
	old.Summary = strings.TrimSpace(strings.Map(handoffCharacter, old.Summary))
	for i := range old.Recent {
		old.Recent[i].Text = strings.TrimSpace(strings.Map(handoffCharacter, old.Recent[i].Text))
	}
	if old.Summary == "" && !slices.ContainsFunc(old.Recent, func(turn prompts.HandoffTurn) bool { return turn.Text != "" }) {
		writeErr(w, http.StatusConflict, i18n.Err("hub-context_handoff.empty"))
		return
	}
	prompt := prompts.RenderSessionHandoff(old.Summary, old.Recent)
	if !source.Assistant {
		h.startSessionWith(w, r, sessionStartRequest{
			MachineID: m.cfg.ID, Cwd: source.Cwd, Launcher: source.Launcher,
			AgentType: source.Agent, Host: source.SpaceKind, Prompt: prompt, TabName: source.Title,
		}, true, true)
		return
	}
	// Use the same conversation start on the source's manager, with generated context excluded from human input.
	r.URL.Path = "/api/v1/assistant/conversations"
	r.URL.RawPath, r.URL.RawQuery = "", ""
	body, _ := json.Marshal(map[string]string{"prompt": prompt, "via": "handoff", "machine_id": m.cfg.ID})
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	h.forwardSend(w, r, h.sessionOwner(m.cfg.ID, sid))
}

func handoffCharacter(r rune) rune {
	if isTerminalControl(r) {
		return -1
	}
	return r
}
