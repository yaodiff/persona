package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	deletedConversationsKey = db.ConversationDeletionsKey
	deletedConversationsTTL = 2 * time.Second
)

type durableConversationDeletions interface {
	PutConversationDeletion(context.Context, string, string, json.RawMessage) error
	ConversationDeletion(context.Context, string, string) (json.RawMessage, bool, error)
	ConversationDeletedID(context.Context, string) (bool, error)
}

// A tombstone keeps reference identity, never the deleted conversation's words.
// The computer's transcript, task origins and previously derived records remain.
type deletedConversation struct {
	Hidden  bool      `json:"hidden_segment,omitempty"`
	Machine string    `json:"machine_id"`
	ID      string    `json:"session_id"`
	Space   string    `json:"space,omitempty"`
	At      time.Time `json:"at"`
}

type conversationDeleteBook struct {
	mu       sync.Mutex
	loaded   bool
	loadedAt time.Time
	now      func() time.Time
	rows     map[string]deletedConversation
}

func (b *conversationDeleteBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (s *Service) loadDeletedConversations(ctx context.Context) error {
	b := &s.deletedConversations
	st := s.store()
	_, durable := st.(durableConversationDeletions)
	if b.loaded && !durable {
		return nil
	}
	rows := map[string]deletedConversation{}
	if st != nil {
		if _, err := st.Setting(ctx, deletedConversationsKey, &rows); err != nil {
			return err
		}
	}
	if rows == nil {
		rows = map[string]deletedConversation{}
	}
	b.rows, b.loaded, b.loadedAt = rows, true, b.clock()
	return nil
}

func (s *Service) loadDeletedConversationsCached(ctx context.Context) error {
	b := &s.deletedConversations
	st := s.store()
	_, durable := st.(durableConversationDeletions)
	if b.loaded {
		if !durable {
			return nil
		}
		if !b.loadedAt.IsZero() {
			if age := b.clock().Sub(b.loadedAt); age >= 0 && age < deletedConversationsTTL {
				return nil
			}
		}
	}
	return s.loadDeletedConversations(ctx)
}

// deletedConversationsSnapshot returns the cached deletion ledger, loading or
// refreshing it when empty or past its durable TTL. Callers must treat the
// returned map as read-only.
func (s *Service) deletedConversationsSnapshot(ctx context.Context) (map[string]deletedConversation, error) {
	b := &s.deletedConversations
	b.mu.Lock()
	err := s.loadDeletedConversationsCached(ctx)
	rows := b.rows
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) refreshDurableConversationDeletion(ctx context.Context, machine, id string, row deletedConversation) {
	b := &s.deletedConversations
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := s.store(); st != nil {
		rows := map[string]deletedConversation{}
		if _, err := st.Setting(ctx, deletedConversationsKey, &rows); err == nil {
			if rows == nil {
				rows = map[string]deletedConversation{}
			}
			key := runtimeListID(machine, id)
			if _, ok := rows[key]; !ok {
				rows[key] = row
			}
			b.rows, b.loaded, b.loadedAt = rows, true, b.clock()
			return
		}
	}
	next := make(map[string]deletedConversation, len(b.rows)+1)
	for k, v := range b.rows {
		next[k] = v
	}
	next[runtimeListID(machine, id)] = row
	b.rows, b.loaded, b.loadedAt = next, false, time.Time{}
}

func (s *Service) deletedConversation(ctx context.Context, machine, id string) (deletedConversation, bool, error) {
	if st, ok := s.store().(durableConversationDeletions); ok {
		for _, alias := range s.physicalAliases(machine) {
			raw, found, err := st.ConversationDeletion(ctx, alias, id)
			if err != nil {
				return deletedConversation{}, false, err
			}
			if !found {
				continue
			}
			var row deletedConversation
			if err := json.Unmarshal(raw, &row); err != nil {
				return row, false, err
			}
			return row, true, nil
		}
		return deletedConversation{}, false, nil
	}
	b := &s.deletedConversations
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.loadDeletedConversations(ctx); err != nil {
		return deletedConversation{}, false, err
	}
	if len(b.rows) == 0 {
		return deletedConversation{}, false, nil
	}
	for _, alias := range s.physicalAliases(machine) {
		if row, ok := lookupRuntimeListID(b.rows, alias, id); ok {
			return row, true, nil
		}
	}
	return deletedConversation{}, false, nil
}

func (s *Service) conversationAvailable(ctx context.Context, machine, id string) error {
	if id == "" {
		return nil
	}
	_, deleted, err := s.deletedConversation(ctx, machine, id)
	if err != nil {
		return err
	}
	if deleted {
		return &rpc.Error{Code: "conversation_deleted", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.deleted")}
	}
	return nil
}

// deletedRow is also sent through fleet.sessions so the hub can enforce the
// manager's decision on old links and subscriptions, even while it reconnects.
func (s *Service) deletedRow(ctx context.Context, row deletedConversation) proto.Session {
	session := proto.Session{MachineID: row.Machine, SessionID: row.ID, Assistant: true,
		Space: row.Space, Title: i18n.T(s.lang(ctx), "assistant-conversation_delete.deleted"), Status: proto.StatusOffline}
	session.SetDeleted(true)
	return session
}

func (s *Service) deletionRows(ctx context.Context, machine string, list []proto.Session, complete bool) []proto.Session {
	rows, err := s.deletedConversationsSnapshot(ctx)
	if err != nil {
		// Do not expose assistant history while its deletion ledger is unreadable.
		kept := list[:0]
		for _, row := range list {
			if !row.Assistant {
				kept = append(kept, row)
			}
		}
		return kept
	}
	if len(rows) == 0 {
		return list
	}
	aliases := s.physicalAliases(machine)
	var seen map[string]bool
	w := 0
	for i := range list {
		sid := list[i].SessionID
		var (
			row     deletedConversation
			deleted bool
		)
		for _, alias := range aliases {
			if r, ok := lookupRuntimeListID(rows, alias, sid); ok {
				r.Machine = machine
				row, deleted = r, true
				break
			}
		}
		if deleted {
			if row.Hidden {
				continue
			}
			list[w] = s.deletedRow(ctx, row)
			if complete {
				if seen == nil {
					seen = map[string]bool{}
				}
				seen[row.ID] = true
			}
			w++
			continue
		}
		if w != i {
			list[w] = list[i]
		}
		w++
	}
	list = list[:w]
	if complete {
		var missing []deletedConversation
		d := s.physicalDevices()
		var canonical string
		if d != nil && machine != "" {
			canonical = d.Canonical(machine)
		}
		for _, row := range rows {
			if seen[row.ID] || row.Hidden || row.Machine == "" || machine == "" {
				continue
			}
			same := row.Machine == machine
			if !same && d != nil {
				same = d.Canonical(row.Machine) == canonical
			}
			if same {
				row.Machine = machine
				if seen == nil {
					seen = map[string]bool{}
				}
				seen[row.ID] = true
				missing = append(missing, row)
			}
		}
		if len(missing) > 0 {
			sort.Slice(missing, func(i, j int) bool { return missing[i].ID < missing[j].ID })
			list = slices.Grow(list, len(missing))
			for _, row := range missing {
				list = append(list, s.deletedRow(ctx, row))
			}
		}
	}
	return list
}

func (s *Service) deleteConversation(ctx context.Context, machine, id string) error {
	// Main replacement and sending cannot race the eligibility check and commit.
	s.main.mu.Lock()
	defer s.main.mu.Unlock()
	s.conversationWrites.Lock()
	defer s.conversationWrites.Unlock()
	if !validSessionID(id) || machine == "" {
		return &rpc.Error{Code: "bad_request", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.invalid")}
	}
	if s.devs != nil && !s.devs.Has(machine) {
		return &rpc.Error{Code: "not_found", Message: i18n.T(s.lang(ctx), "devices-api.unknown_machine")}
	}
	if s.devs != nil {
		machine = s.devs.Canonical(machine)
	}
	if row, ok, err := s.deletedConversation(ctx, machine, id); err != nil {
		return err
	} else if ok {
		if _, durable := s.store().(durableConversationDeletions); durable {
			s.refreshDurableConversationDeletion(ctx, machine, id, row)
		}
		return s.finishDeletedConversation(ctx, machine, id)
	}
	cfg := s.settings(ctx)
	if cfg.Main == id || s.isBackground(ctx, id) {
		return &rpc.Error{Code: "conversation_protected", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.protected")}
	}
	if slices.Contains(cfg.Leaders, id) {
		// A current direction's lead conversation (#11392): unpinning it (…/end) ends the direction, after which it
		// deletes as any other conversation.
		return &rpc.Error{Code: "conversation_protected", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.lead_protected")}
	}
	c, found := s.session(ctx, machine, id)
	if !found || !c.Assistant {
		return &rpc.Error{Code: "not_found", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.unknown")}
	}
	if s.fleetOn() {
		if row, ok := s.decoratedSession(machine, id); ok {
			c = row
		}
	}
	waiting, readable := s.relayWaitingFor(id)
	if !readable {
		return i18n.Err("assistant-conversation_delete.queue_unavailable")
	}
	if c.Starting || c.Status == proto.StatusWorking || c.Status == proto.StatusBlocked || c.PendingInteractions > 0 || backgroundRunning(c) ||
		(c.Compaction != nil && c.Compaction.State == proto.CompactionRunning) || len(c.Queued) > 0 || waiting {
		return &rpc.Error{Code: "conversation_busy", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.busy")}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	s.todos.mu.RLock()
	responsible := false
	for _, task := range s.todos.todos {
		if task.DoneAt == nil && !task.Cancelled() && (task.Origin == id || task.Supervision != nil && task.Supervision.Supervisor == id) {
			responsible = true
			break
		}
	}
	s.todos.mu.RUnlock()
	if responsible {
		return &rpc.Error{Code: "conversation_busy", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.tasks_active")}
	}
	st := s.store()
	if st == nil {
		return errors.New("no database")
	}
	row := deletedConversation{Machine: machine, ID: id, Space: spaceOf(c), At: time.Now().UTC()}
	if durable, ok := st.(durableConversationDeletions); ok {
		raw, err := json.Marshal(row)
		if err != nil {
			return err
		}
		kept := s.conversationKeptPages(ctx, machine, id)
		// The deletion's transaction moves the pages it kept to Recently removed (db.RemoveConversationAssistantPagesTx).
		if err := durable.PutConversationDeletion(ctx, machine, id, raw); err != nil {
			return err
		}
		s.refreshDurableConversationDeletion(ctx, machine, id, row)
		for i, page := range kept {
			if i == 0 {
				s.pagesListChanged(ctx)
			}
			s.pageChanged(page, pageContent)
		}
		return s.finishDeletedConversation(ctx, machine, id)
	}
	b := &s.deletedConversations
	b.mu.Lock()
	if err := s.loadDeletedConversations(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	next := make(map[string]deletedConversation, len(b.rows)+1)
	for key, row := range b.rows {
		next[key] = row
	}
	next[runtimeListID(machine, id)] = row
	if err := st.PutSetting(ctx, deletedConversationsKey, next); err != nil {
		b.mu.Unlock()
		return err
	}
	b.rows, b.loaded, b.loadedAt = next, true, b.clock()
	b.mu.Unlock()
	return s.finishDeletedConversation(ctx, machine, id)
}

func (s *Service) conversationDeleteRoutes() {
	s.handle("DELETE /api/v1/assistant/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		var in struct {
			Machine string `json:"machine_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if err := s.deleteConversation(r.Context(), in.Machine, r.PathValue("id")); err != nil {
			var re *rpc.Error
			if errors.As(err, &re) && (re.Code == "bad_request" || re.Code == "not_found") {
				status := http.StatusBadRequest
				if re.Code == "not_found" {
					status = http.StatusNotFound
				}
				apierr.WriteCode(w, status, re.Code, err)
			} else {
				apierr.Call(w, err)
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "deleted": true})
	})
}

// Guard every conversation route, including feeds and view actions. A delete
// itself remains idempotent. The public route identifies the owning manager;
// within it conversation ids are the same ids used by archive and task origins.
func (s *Service) deletedConversationRequest(r *http.Request) error {
	path, ok := strings.CutPrefix(r.URL.Path, "/api/v1/assistant/conversations/")
	if !ok {
		return nil
	}
	id, rest, _ := strings.Cut(path, "/")
	if r.Method == "DELETE" && rest == "" {
		return nil
	}
	return s.conversationIDAvailable(r.Context(), id)
}

func conversationContentEvent(kind string) bool {
	switch kind {
	case "assistant_feed", "assistant_view", "assistant_view_action", "assistant_view_block":
		return true
	}
	return false
}

func (s *Service) conversationIDAvailable(ctx context.Context, id string) error {
	b := &s.deletedConversations
	b.mu.Lock()
	defer b.mu.Unlock()
	return s.conversationIDAvailableLocked(ctx, id)
}

func (s *Service) conversationIDAvailableLocked(ctx context.Context, id string) error {
	if st, ok := s.store().(durableConversationDeletions); ok {
		deleted, err := st.ConversationDeletedID(ctx, id)
		if err != nil {
			return err
		}
		if deleted {
			return &rpc.Error{Code: "conversation_deleted", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.deleted")}
		}
		return nil
	}
	if err := s.loadDeletedConversations(ctx); err != nil {
		return err
	}
	for _, row := range s.deletedConversations.rows {
		if row.ID == id {
			return &rpc.Error{Code: "conversation_deleted", Message: i18n.T(s.lang(ctx), "assistant-conversation_delete.deleted")}
		}
	}
	return nil
}

// A feed can finish its database reads after deletion. Serialize its final
// check and response write with the tombstone commit, not just the route entry.
func (s *Service) writeConversationJSON(ctx context.Context, w http.ResponseWriter, id string, value any) {
	s.deletedConversations.mu.Lock()
	defer s.deletedConversations.mu.Unlock()
	if err := s.conversationIDAvailableLocked(ctx, id); err != nil {
		apierr.Call(w, err)
		return
	}
	apierr.WriteJSON(w, value)
}

func conversationWrite(method string) bool {
	if a2aDeliveryMethod(method) {
		return true
	}
	switch method {
	case "assistant.send", "session.send", DeviceSessionSendOnce, "session.start", "session.resume", "session.fork", "session.rewind", assistantwire.DeviceAssistantAnswerAgain:
		return true
	}
	return false
}

// Check every referenced identity: a resume may name both a new session id and
// the old transcript it would copy, and neither may bypass a tombstone.
func (s *Service) conversationParamsAvailable(ctx context.Context, machine string, params any) error {
	var err error
	if _, durable := s.store().(durableConversationDeletions); !durable {
		b := &s.deletedConversations
		b.mu.Lock()
		err = s.loadDeletedConversations(ctx)
		marked := len(b.rows) > 0
		b.mu.Unlock()
		if err == nil && !marked {
			return nil
		}
	}
	var ids [3]string
	if p, ok := params.(map[string]any); ok {
		ids[0], _ = p["session_id"].(string)
		ids[1], _ = p["resume_session_id"].(string)
		ids[2], _ = p["new_session_id"].(string)
	} else {
		b, _ := json.Marshal(params)
		var p struct {
			SessionID string `json:"session_id"`
			Resume    string `json:"resume_session_id"`
			New       string `json:"new_session_id"`
		}
		_ = json.Unmarshal(b, &p)
		ids = [3]string{p.SessionID, p.Resume, p.New}
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err != nil {
			return err
		}
		if err := s.conversationAvailable(ctx, machine, id); err != nil {
			return err
		}
	}
	return nil
}
