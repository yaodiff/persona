package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
)

const sigTopicFollowup = "topic_followup"

type topicFollowupBook struct {
	running sync.Mutex
	asked   map[string]time.Time
}

type topicFollowupPending struct {
	Message        connectorwire.LarkReplyMessage `json:"message"`
	AgentSent      bool                           `json:"agent_sent,omitempty"`
	SupervisorTold bool                           `json:"supervisor_told,omitempty"`
}

// This is a bounded checkpoint on one existing topic binding, not another task lifecycle or delivery outbox.
type topicFollowupState struct {
	Since    time.Time                       `json:"since"`
	Identity connectorwire.LarkTopicIdentity `json:"identity"`
	// A nil Seen means no successful read has established the IDs at Since.
	Seen    []string              `json:"seen"`
	Pending *topicFollowupPending `json:"pending,omitempty"`
}

type topicFollowupRecord struct {
	TodoThread
	Followups topicFollowupState `json:"followups"`
}

type topicFollowupStore interface {
	TodoThreadState(context.Context, string) (json.RawMessage, error)
	SaveTodoThreadFollowups(context.Context, string, json.RawMessage, json.RawMessage) (json.RawMessage, bool, error)
}

// The minute source pass owns these reads, independently of parking, Inbox AI processing and its opt-in signals.
func (s *Service) sweepTopicFollowupsBounded(now time.Time) {
	b := &s.topicFollowups
	st, ok := s.store().(topicFollowupStore)
	if !ok || !b.running.TryLock() {
		return
	}
	defer b.running.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	var tasks []Todo
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if taskOpen(*t) && t.Thread != nil && t.Thread.Channel == "feishu" &&
			parkReplyID(t.Thread.ChatID) && parkReplyID(t.Thread.ThreadID) {
			tasks = append(tasks, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	if b.asked == nil {
		b.asked = map[string]time.Time{}
	}
	active := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		active[t.ID] = true
	}
	for id := range b.asked {
		if !active[id] {
			delete(b.asked, id)
		}
	}
	slices.SortFunc(tasks, func(a, c Todo) int {
		return cmp.Or(b.asked[a.ID].Compare(b.asked[c.ID]), strings.Compare(a.ID, c.ID))
	})
	for _, t := range tasks[:min(4, len(tasks))] {
		if ctx.Err() != nil {
			return
		}
		if at := b.asked[t.ID]; !at.IsZero() && now.Sub(at) < time.Minute {
			continue
		}
		b.asked[t.ID] = now
		if err := s.observeTopicFollowup(ctx, st, t); err != nil {
			s.logf("task %s: linked Lark topic follow-up: %v", t.ID, err)
		}
	}
}

func (s *Service) observeTopicFollowup(ctx context.Context, st topicFollowupStore, t Todo) error {
	raw, err := st.TodoThreadState(ctx, t.ID)
	if err != nil {
		return err
	}
	var record topicFollowupRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	if t.Thread == nil || !sameThread(*t.Thread, record.TodoThread) {
		return nil
	}
	before := record.Followups
	state := before
	if t.ReopenedAt != nil && t.ReopenedAt.After(state.Since) {
		state.Since, state.Seen, state.Pending = *t.ReopenedAt, nil, nil
	}
	watch := connectorwire.LarkTopicWatch{Identity: state.Identity, ChatID: connectorwire.LarkChatID(t.Thread.ChatID),
		ThreadID: connectorwire.LarkThreadID(t.Thread.ThreadID), Since: state.Since}
	var observation connectorwire.LarkTopicObservation
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	err = s.appCall(rctx, AppConnectors, assistantwire.ConnectorsLarkTopic, watch, &observation)
	cancel()
	if err != nil {
		// A source outage must not hold back delivery of a receipt already committed on this binding.
		if state.Pending == nil {
			return err
		}
	} else if !topicFollowupMatches(watch, observation) {
		return nil
	} else {
		state.Identity = observation.Watch.Identity
		if state.Since.IsZero() {
			// Legacy bindings have no trustworthy bind time: establish a baseline without replaying their history.
			state.Since = observation.CheckedAt
			state.Seen = []string{}
		} else {
			if state.Seen == nil {
				// Seed the timestamp-only checkpoint with the IDs already visible at its boundary.
				state.Seen = []string{}
				for _, msg := range observation.Messages {
					if msg.At.Equal(state.Since) && !slices.Contains(state.Seen, msg.ID) {
						state.Seen = append(state.Seen, msg.ID)
					}
				}
			}
			var latest connectorwire.LarkReplyMessage
			for _, msg := range observation.Messages {
				if msg.At.Before(state.Since) || msg.At.Equal(state.Since) && slices.Contains(state.Seen, msg.ID) {
					continue
				}
				if latest.ID == "" || msg.At.After(latest.At) || msg.At.Equal(latest.At) && msg.ID > latest.ID {
					latest = msg
				}
			}
			if latest.ID != "" && (state.Pending == nil || state.Pending.AgentSent && state.Pending.SupervisorTold) {
				seen := slices.Clone(state.Seen)
				if latest.At.After(state.Since) {
					seen = nil
				}
				for _, msg := range observation.Messages {
					if msg.At.Equal(latest.At) && !slices.Contains(seen, msg.ID) {
						seen = append(seen, msg.ID)
					}
				}
				if len(seen) > 200 {
					return fmt.Errorf("linked Lark topic exceeds the timestamp checkpoint bound")
				}
				state.Since, state.Seen = latest.At, seen
				state.Pending = &topicFollowupPending{Message: latest}
			}
		}
	}
	if !reflect.DeepEqual(before, state) {
		var changed bool
		raw, changed, err = s.saveTopicFollowups(ctx, st, t, raw, state)
		if err != nil || !changed {
			return err
		}
	}
	if state.Pending == nil {
		return nil
	}
	return s.deliverTopicFollowup(ctx, st, t, raw, state)
}

func topicFollowupMatches(w connectorwire.LarkTopicWatch, o connectorwire.LarkTopicObservation) bool {
	id := o.Watch.Identity
	if o.Watch.ChatID != w.ChatID || o.Watch.ThreadID != w.ThreadID || !o.Watch.Since.Equal(w.Since) ||
		w.Identity != (connectorwire.LarkTopicIdentity{}) && id != w.Identity ||
		(id.Brand != "feishu" && id.Brand != "lark") || !parkReplyID(string(id.AppID)) || !parkReplyID(string(id.AccountOpenID)) ||
		o.CheckedAt.IsZero() || o.CheckedAt.After(time.Now()) || o.CheckedAt.Before(w.Since) || len(o.Messages) > 200 {
		return false
	}
	for _, m := range o.Messages {
		if !parkReplyID(m.ID) || !parkReplyID(m.SenderID) || connectorwire.LarkOpenID(m.SenderID) == id.AccountOpenID ||
			m.SenderType != "user" || m.IDType != "open_id" || m.AppID != "" ||
			connectorwire.LarkChatID(m.ChatID) != w.ChatID || connectorwire.LarkThreadID(m.ThreadID) != w.ThreadID ||
			m.At.IsZero() || !m.Before.After(m.At) ||
			m.Before.Sub(m.At) > time.Minute || m.Before.After(o.CheckedAt) {
			return false
		}
	}
	return true
}

func (s *Service) topicStillOpen(t Todo) bool {
	cur, ok := s.todoNow(t.ID)
	return ok && taskOpen(cur) && cur.Thread != nil && t.Thread != nil && sameThread(*cur.Thread, *t.Thread) &&
		(cur.ReopenedAt == nil && t.ReopenedAt == nil || cur.ReopenedAt != nil && t.ReopenedAt != nil && cur.ReopenedAt.Equal(*t.ReopenedAt))
}

func (s *Service) saveTopicFollowups(ctx context.Context, st topicFollowupStore, t Todo, raw json.RawMessage, state topicFollowupState) (json.RawMessage, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	if !s.topicStillOpen(t) {
		return nil, false, nil
	}
	next, err := json.Marshal(state)
	if err != nil {
		return nil, false, err
	}
	return st.SaveTodoThreadFollowups(ctx, t.ID, raw, next)
}

func topicFollowupNote(t Todo, state topicFollowupState, delivery string) string {
	host := "applink.feishu.cn"
	if state.Identity.Brand == "lark" {
		host = "applink.larksuite.com"
	}
	m := state.Pending.Message
	q := url.Values{"open_chat_id": {t.Thread.ChatID}, "open_thread_id": {t.Thread.ThreadID}, "message_id": {m.ID}}
	return prompts.RenderTopicFollowup(prompts.TopicFollowup{URL: "https://" + host + "/client/thread/open?" + q.Encode(),
		Sender: m.SenderID, Message: m.ID, At: m.At.UTC().Format(time.RFC3339Nano), Delivery: delivery})
}

func (s *Service) deliverTopicFollowup(ctx context.Context, st topicFollowupStore, t Todo, raw json.RawMessage, state topicFollowupState) error {
	if !s.topicStillOpen(t) {
		return nil
	}
	current, err := st.TodoThreadState(ctx, t.ID)
	if err != nil || !bytes.Equal(current, raw) {
		return err
	}
	p := state.Pending
	if p.AgentSent && p.SupervisorTold {
		state.Pending = nil
		_, _, err := s.saveTopicFollowups(context.WithoutCancel(ctx), st, t, raw, state)
		return err
	}
	delivery := "sent; this notification is the supervisor's copy"
	if !p.AgentSent {
		cur, ok := s.todoNow(t.ID)
		if !ok {
			return nil
		}
		sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, err := s.sendTaskInput(sendCtx, cur, topicFollowupNote(t, state, ""), true, nil, false, func(ctx context.Context) error {
			if !s.topicStillOpen(t) {
				return db.ErrTodoCompleted
			}
			current, err := st.TodoThreadState(ctx, t.ID)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, raw) {
				return errors.New("linked topic receipt changed")
			}
			return nil
		})
		cancel()
		if err != nil {
			delivery = "not delivered; retry pending: " + clip(oneLine(err.Error()), 200)
		} else {
			p.AgentSent = true
			p.SupervisorTold = false // A recovery copy has a different delivery outcome than the failure notice.
			var changed bool
			raw, changed, err = s.saveTopicFollowups(context.WithoutCancel(ctx), st, t, raw, state)
			if err != nil || !changed {
				return err
			}
		}
	}
	if !s.topicStillOpen(t) {
		return nil
	}
	g := superviseSignal{Todo: t.ID, Kind: sigTopicFollowup, Thread: t.Thread,
		Key:    topicFollowupKey(t, state),
		Detail: topicFollowupNote(t, state, delivery)}
	if p.SupervisorTold {
		return nil
	}
	// Keep the source receipt pending until the existing digest path acknowledges it. A restart requeues it.
	s.signal(g)
	return nil
}

func topicFollowupKey(t Todo, state topicFollowupState) string {
	id := state.Identity
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s/%t", id.Brand, id.AppID, id.AccountOpenID,
		t.Thread.ChatID, t.Thread.ThreadID, state.Pending.Message.ID, state.Pending.AgentSent)
}

func (s *Service) topicFollowupDigestEntry(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool, bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok {
		return prompts.SuperviseEntry{}, false, false
	}
	valid, err := s.topicFollowupSignalHolds(ctx, t, g)
	if !valid {
		return prompts.SuperviseEntry{}, false, err != nil
	}
	return prompts.SuperviseEntry{
		ID: t.ID, Title: t.Title, Kind: g.Kind, Detail: g.Detail, Link: todoLink(web, t.ID), Quiet: t.Quiet,
	}, true, false
}

func (s *Service) topicFollowupSignalHolds(ctx context.Context, t Todo, g superviseSignal) (bool, error) {
	st, ok := s.store().(topicFollowupStore)
	if !ok || !taskOpen(t) || t.Thread == nil || g.Thread == nil || !sameThread(*t.Thread, *g.Thread) {
		return false, nil
	}
	raw, err := st.TodoThreadState(ctx, t.ID)
	if errors.Is(err, db.ErrTodoNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var record topicFollowupRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return false, err
	}
	return sameThread(record.TodoThread, *t.Thread) && record.Followups.Pending != nil &&
		!record.Followups.Pending.SupervisorTold && topicFollowupKey(t, record.Followups) == g.Key, nil
}

// A digest's provisional "sent" reservation is not delivery. Only its accepted/told callback commits this receipt.
func (s *Service) topicFollowupsTold(ctx context.Context, list []superviseSignal) {
	st, ok := s.store().(topicFollowupStore)
	if !ok {
		return
	}
	for _, g := range list {
		if g.Kind != sigTopicFollowup {
			continue
		}
		t, exists := s.todoNow(g.Todo)
		if !exists || !taskOpen(t) || t.Thread == nil || g.Thread == nil || !sameThread(*t.Thread, *g.Thread) {
			continue
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		raw, err := st.TodoThreadState(rctx, t.ID)
		var record topicFollowupRecord
		if err == nil {
			err = json.Unmarshal(raw, &record)
		}
		if err == nil && sameThread(record.TodoThread, *t.Thread) && record.Followups.Pending != nil &&
			!record.Followups.Pending.SupervisorTold && topicFollowupKey(t, record.Followups) == g.Key {
			record.Followups.Pending.SupervisorTold = true
			_, _, err = s.saveTopicFollowups(rctx, st, t, raw, record.Followups)
		}
		cancel()
		if err != nil {
			s.logf("task %s: keep linked-topic supervisor receipt: %v", t.ID, err)
			s.restoreDigest([]superviseSignal{g}, time.Time{}, time.Time{})
		}
	}
}

// Leader delivery records the same source fingerprint as the conversation digest.
func (s *Service) ackTopicFollowupDedup(ctx context.Context, signals []superviseSignal) {
	var followups []superviseSignal
	for _, g := range signals {
		if g.Kind == sigTopicFollowup {
			followups = append(followups, g)
		}
	}
	if len(followups) > 0 {
		s.ackSupervisionDedupLocked(ctx, followups)
	}
}
