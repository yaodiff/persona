package extmgr

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

// agentsPollFallback re-lists agent sessions in case realtime agent-bridge.updated events are missed (the stream
// is down, or the Lark gateway predates the event); only sessions whose rank or unread count moved are reported.
const agentsPollFallback = 60 * time.Second

func (h *Manager) initAgents() {
	var kv agents.KV
	if h.store != nil {
		kv = h.store
	}
	// Plugin sources join later. Explicit bot registrations are independent sources so one outage cannot hide another.
	h.agents = agents.NewService(kv, h.agentsChanged, h.logf)
	registry := agents.NewBotRegistry(kv, agents.BotCallFunc(func(ctx context.Context, machineID, method string, params, out any) error {
		m := h.localMachine(machineID)
		if m == nil {
			return &rpc.Error{Code: "not_found", Message: "bot machine is not registered"}
		}
		return m.call(ctx, method, params, out)
	}))
	h.orcaBots.setRegistry(registry)
	for _, registration := range h.cfg.Bots.Endpoints {
		src, err := registry.Source(registration)
		if err != nil {
			h.logf("agents: bot endpoint %q is not registered: %v", registration.ID, err)
			continue
		}
		if !h.agents.Add(src) {
			h.logf("agents: bot endpoint %q duplicates another source", registration.ID)
		}
	}
}

// agentsChanged tells the apps which agent sessions to re-pull, like {"type":"lark"} does for chats.
func (h *Manager) agentsChanged(ids []string) {
	if ids == nil {
		ids = []string{}
	}
	h.broadcast(map[string]any{"type": "agents", "session_ids": ids})
}

func (h *Manager) agentsLoop(ctx context.Context) { h.agents.Poll(ctx, agentsPollFallback) }

func agentsErr(w http.ResponseWriter, err error) {
	var listErr *sessionlist.Error
	if errors.As(err, &listErr) {
		status := http.StatusBadRequest
		if listErr.Code == "cursor_expired" || listErr.Code == "revision_expired" {
			status = http.StatusConflict
		}
		writeErrCode(w, status, listErr.Code, i18n.Err("session-list."+listErr.Code))
		return
	}
	writeErr(w, agents.HTTPStatus(err), err)
}

// agentRoutes serve /api/v1/agents/*: every agent Persona can reach (the running plugins' agent sources). A session
// id is "<source>:<sessionKey>" and must be percent-encoded in paths (bridge keys contain "/").
func (h *Manager) agentRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"sources": h.agents.Statuses(r.Context())})
	})
	api.HandleFunc("GET /api/v1/agents/sessions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if sessionlist.Enabled(q) {
			opts, err := sessionlist.ParseProviderSets(q)
			if err != nil {
				agentsErr(w, err)
				return
			}
			if counts := q.Get("counts"); counts == "1" || counts == "true" {
				counts, err := h.agents.ListCounts(r.Context(), opts)
				if err != nil {
					agentsErr(w, err)
					return
				}
				writeJSON(w, counts)
			} else if with := q.Get("with_counts"); with == "1" || with == "true" {
				both, err := h.agents.ListPageWithCounts(r.Context(), opts) // one read where a refresh took two (#6496)
				if err != nil {
					agentsErr(w, err)
					return
				}
				writeJSON(w, both)
			} else {
				page, err := h.agents.ListPage(r.Context(), opts)
				if err != nil {
					agentsErr(w, err)
					return
				}
				writeJSON(w, page)
			}
			return
		}
		if text := q.Get("q"); text != "" {
			hits, err := h.agents.Search(r.Context(), q.Get("source"), text)
			if err != nil {
				agentsErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"items": hits})
			return
		}
		list, err := h.agents.Sessions(r.Context(), q.Get("source"), q.Get("provider"))
		if err != nil {
			agentsErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"sessions": list})
	})
	api.HandleFunc("GET /api/v1/agents/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, err := h.agents.Session(r.Context(), r.PathValue("id"))
		if err != nil {
			agentsErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"session": s})
	})
	api.HandleFunc("GET /api/v1/agents/sessions/{id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		a, err := h.agents.Avatar(r.Context(), r.PathValue("id"))
		if err != nil {
			agentsErr(w, err)
			return
		}
		etag := `"` + a.Version + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", a.ContentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(a.Data)))
		_, _ = w.Write(a.Data)
	})
	api.HandleFunc("GET /api/v1/agents/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p, err := h.agents.Messages(r.Context(), r.PathValue("id"), q.Get("cursor"), atoi(q.Get("limit")))
		if err != nil {
			agentsErr(w, err)
			return
		}
		writeJSON(w, p)
	})
	api.HandleFunc("POST /api/v1/agents/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ClientID string `json:"clientId"`
			Text     string `json:"text"`
			Origin   string `json:"origin"`
		}
		if err := decodeJSONBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if in.Origin == "" {
			in.Origin = "app"
		}
		res, err := h.agents.Send(r.Context(), r.PathValue("id"), in.ClientID, in.Text, in.Origin)
		if err != nil {
			agentsErr(w, err)
			return
		}
		h.logf("agents: send %s clientId=%s len=%d origin=%s duplicate=%v", res.Entry.SessionID, res.Entry.ClientID,
			res.Entry.Length, res.Entry.Origin, res.Duplicate)
		writeJSON(w, res)
	})
	api.HandleFunc("DELETE /api/v1/agents/sessions/{id}/messages/{clientId}", func(w http.ResponseWriter, r *http.Request) {
		if err := h.agents.DeleteMessage(r.Context(), r.PathValue("id"), r.PathValue("clientId")); err != nil {
			agentsErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	api.HandleFunc("POST /api/v1/agents/sessions/{id}/read", func(w http.ResponseWriter, r *http.Request) {
		if err := h.agents.MarkRead(r.Context(), r.PathValue("id")); err != nil {
			agentsErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	api.HandleFunc("GET /api/v1/agents/prefs", func(w http.ResponseWriter, r *http.Request) {
		p, err := h.agents.Prefs(r.Context())
		if err != nil {
			agentsErr(w, err)
			return
		}
		writeJSON(w, p)
	})
	api.HandleFunc("PUT /api/v1/agents/prefs", func(w http.ResponseWriter, r *http.Request) {
		var in agents.Prefs
		if err := decodeJSONBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		p, err := h.agents.PutPrefs(r.Context(), in)
		if err != nil {
			agentsErr(w, err)
			return
		}
		// other apps re-pull the prefs
		h.broadcast(map[string]any{"type": "agents.prefs"})
		writeJSON(w, p)
	})
	// Removing a conversation from the list (agents/hidden.go): the source keeps it, a new message brings it back.
	api.HandleFunc("GET /api/v1/agents/hidden", func(w http.ResponseWriter, r *http.Request) {
		list, err := h.agents.Hidden(r.Context())
		if err != nil {
			agentsErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"sessions": list})
	})
	hide := func(on bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if principalOf(r.Context()).Role != roleFull {
				writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
				return
			}
			do := h.agents.Unhide
			if on {
				do = h.agents.Hide
			}
			if _, err := do(r.Context(), r.PathValue("id")); err != nil {
				agentsErr(w, err)
				return
			}
			h.broadcast(map[string]any{"type": "agents.prefs"})
			writeJSON(w, map[string]any{"ok": true, "hidden": on})
		}
	}
	api.HandleFunc("POST /api/v1/agents/sessions/{id}/hide", hide(true))
	api.HandleFunc("POST /api/v1/agents/sessions/{id}/unhide", hide(false))
	api.HandleFunc("GET /api/v1/agents/outbox", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, map[string]any{"outbox": h.agents.Outbox(r.Context(), q.Get("session_id"), atoi(q.Get("limit")))})
	})
}
