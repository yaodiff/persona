package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
)

// ---- messages about a task ----

// messageCard puts the card into the conversation that opened the task (none when the task is gone or done).
func (s *Service) messageCard(ctx context.Context, itemID string, body MessageCard) {
	t, ok, err := s.todo(ctx, body.TodoID)
	if err != nil || !ok || t.DoneAt != nil || t.Origin == "" {
		return
	}
	body.ProgressVersion = s.draftProgressVersion(ctx, t)
	if _, err := s.appendFeed(ctx, t.Origin, feedMessage, itemID, body); err != nil {
		s.logf("task %s: message card: %v", body.TodoID, err)
	}
}

type draftFeedStore interface {
	AppendDraftFeed(context.Context, db.FeedRow) (bool, error)
}

// duplicateDismissedDraft is the store-less producer fence. Caller holds tasks.mu.
func (s *Service) duplicateDismissedDraft(card FeedItem) bool {
	var next MessageCard
	_ = json.Unmarshal(card.Body, &next)
	for _, list := range s.tasks.feed {
		for _, old := range list {
			if old.Kind != feedMessage || old.Ref != card.Ref || cardDismissalID(old) == "" {
				continue
			}
			var b MessageCard
			_ = json.Unmarshal(old.Body, &b)
			if b.TodoID == next.TodoID && b.MessageVersion == next.MessageVersion && (next.ProgressVersion == "" || b.ProgressVersion == next.ProgressVersion) {
				return true
			}
		}
	}
	return false
}

// draftProgressVersion changes with Agent output, not polling, read receipts or
// task-decision timestamps. Missing sessions fail closed: an offline Agent is not
// evidence of a new progress generation. No transcript text is exposed in the key.
func (s *Service) draftProgressVersion(ctx context.Context, t Todo) string {
	type progress struct {
		Machine, Session, Last, Activity string
		Rev                              int64
		ActivityAt                       int64
	}
	var rows []progress
	expected := 0
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			expected++
		}
	}
	agents := s.taskAgents(ctx, t)
	if len(agents) != expected {
		return ""
	}
	for _, a := range agents {
		r := progress{Machine: a.MachineID, Session: a.SessionID, Last: a.LastText, Activity: a.Activity, Rev: a.Rev}
		if a.ActivityAt != nil {
			r.ActivityAt = a.ActivityAt.UnixMicro()
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b progress) int {
		return cmp.Or(cmp.Compare(a.Machine, b.Machine), cmp.Compare(a.Session, b.Session))
	})
	raw, _ := json.Marshal(rows)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// markCardsSent records on the cards about an inbox item put in by at that its reply went out (text: what it said;
// empty: not known) and tells the clients (tell), which draw them sent; the cards it changed. The inbox says when a
// reply went out (replyInboxFrom).
func (s *Service) markCardsSent(ctx context.Context, itemID string, at time.Time, text string, tell bool) []FeedItem {
	at = at.UTC()
	patch, _ := json.Marshal(struct {
		SentAt time.Time `json:"sent_at"`
		Sent   string    `json:"sent,omitempty"`
	}{at, text})
	var out []FeedItem
	if st := s.store(); st == nil {
		s.tasks.mu.Lock()
		for c, list := range s.tasks.feed {
			for i, it := range list {
				if it.Kind != feedMessage || it.Ref != itemID || it.At.After(at) || cardSentAt(it) != nil {
					continue
				}
				var body map[string]json.RawMessage
				_ = json.Unmarshal(it.Body, &body)
				if body == nil {
					body = map[string]json.RawMessage{}
				}
				var p map[string]json.RawMessage
				_ = json.Unmarshal(patch, &p)
				for k, v := range p {
					body[k] = v
				}
				it.Body, _ = json.Marshal(body)
				s.tasks.feed[c][i] = it
				out = append(out, it)
			}
		}
		s.tasks.mu.Unlock()
	} else {
		rows, err := st.MarkFeedSent(ctx, feedMessage, itemID, at, patch)
		if err != nil {
			s.logf("inbox %s: mark the cards sent: %v", itemID, err)
		}
		for _, r := range rows {
			out = append(out, FeedItem{ID: r.ID, Conversation: r.Conversation, At: r.At, Kind: r.Kind, Ref: r.Ref, Body: r.Body})
		}
	}
	if tell {
		for _, it := range out {
			s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": it.Conversation, "item": it})
		}
	}
	return out
}

// cardSentAt is when a message card's reply went out; nil while it has not.
func cardSentAt(it FeedItem) *time.Time {
	var b struct {
		SentAt *time.Time `json:"sent_at"`
	}
	_ = json.Unmarshal(it.Body, &b)
	return b.SentAt
}

// settleCards restores a missing send time from the Inbox's send record. A source reply only files the draft.
func (s *Service) settleCards(ctx context.Context, items []FeedItem) {
	var ids []string
	for _, it := range items {
		if it.Kind == feedMessage && cardSentAt(it) == nil && !slices.Contains(ids, it.Ref) {
			ids = append(ids, it.Ref)
		}
	}
	if len(ids) == 0 {
		return
	}
	sent := s.inboxSentAt(ctx, ids)
	for _, it := range items {
		if it.Kind != feedMessage || cardSentAt(it) != nil {
			continue
		}
		at, ok := sent[it.Ref]
		if !ok || at.Before(it.At) {
			continue
		}
		for _, m := range s.markCardsSent(ctx, it.Ref, at, "", false) {
			for j := range items {
				if items[j].ID == m.ID {
					items[j] = m
				}
			}
		}
	}
}

// fileCards adds filed_at to the cards among items that do not say sent_at and whose item is filed (Done, Dismiss, read
// elsewhere, archived: not open), as of when it was filed: Send on them is refused (inbox_item_done), so the apps
// hide it. It is worked out on each read and not stored: moved back to the inbox, the item is open and the card
// offers Send again.
func (s *Service) fileCards(ctx context.Context, items []FeedItem) {
	var ids []string
	for _, it := range items {
		if it.Kind == feedMessage && cardSentAt(it) == nil && !slices.Contains(ids, it.Ref) {
			ids = append(ids, it.Ref)
		}
	}
	if len(ids) == 0 {
		return
	}
	filed := s.inboxFiledAt(ctx, ids)
	for i, it := range items {
		at, ok := filed[it.Ref]
		if it.Kind != feedMessage || !ok || cardSentAt(it) != nil {
			continue
		}
		var body map[string]json.RawMessage
		_ = json.Unmarshal(it.Body, &body)
		if body == nil {
			body = map[string]json.RawMessage{}
		}
		body["filed_at"], _ = json.Marshal(at.UTC())
		items[i].Body, _ = json.Marshal(body)
	}
}

// cardSent is whether card cardID, about inbox item itemID, says its reply went out ("Send" on the card sends once per
// card: sendFromCard).
func (s *Service) cardSent(ctx context.Context, cardID, itemID string) (bool, error) {
	card, err := s.feedItem(ctx, cardID)
	if err != nil {
		return false, err
	}
	if card != nil && card.Kind == feedMessage && card.Ref == itemID && cardDismissalID(*card) != "" {
		return false, errDraftDismissed
	}
	return card != nil && card.Kind == feedMessage && card.Ref == itemID && cardSentAt(*card) != nil, nil
}

// feedItem is one feed item by its id; nil when there is none.
func (s *Service) feedItem(ctx context.Context, id string) (*FeedItem, error) {
	st := s.store()
	if st == nil {
		s.tasks.mu.Lock()
		defer s.tasks.mu.Unlock()
		for _, list := range s.tasks.feed {
			for _, it := range list {
				if it.ID == id {
					return &it, nil
				}
			}
		}
		return nil, nil
	}
	r, err := st.FeedItem(ctx, id)
	if err != nil || r == nil {
		return nil, err
	}
	return &FeedItem{ID: r.ID, Conversation: r.Conversation, At: r.At, Kind: r.Kind, Ref: r.Ref, Body: r.Body}, nil
}

// activeTasks are the tasks going on, as the inbox's judge and AI twin see them.
func (s *Service) activeTasks(ctx context.Context) []prompts.InboxTask {
	var out []prompts.InboxTask
	for _, t := range s.todosOf(ctx, "*") {
		if t.Origin == "" || t.DoneAt != nil || t.Cancelled() {
			continue
		}
		agents := s.taskAgents(ctx, t)
		task := snapshotTask(t, taskState(t, agents), agents, time.Now())
		out = append(out, prompts.InboxTask{ID: t.ID, Title: t.Title, State: task.State, Last: task.Last, Notes: clip(t.Notes, 1200)})
	}
	// The assistant's brief is capped and age-filtered; the task judge must see every open candidate.
	slices.SortFunc(out, func(a, b prompts.InboxTask) int { return cmp.Compare(a.ID, b.ID) })
	return out
}
