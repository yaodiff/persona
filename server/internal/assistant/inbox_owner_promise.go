package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

const ownerPromiseClassifications = "inbox.owner_promise_classifications"

type promiseObservationStore interface {
	HasSentAsUserMessage(context.Context, string, time.Time) (bool, error)
	Setting(context.Context, string, any) (bool, error)
	PutSetting(context.Context, string, any) error
}

type promiseClassification struct {
	At       time.Time `json:"at"`
	Accepted bool      `json:"accepted"`
}

// prepareOwnerPromises only turns observations from the built-in pilot into
// private action items. Source-thread replies remain on the ordinary route.
func (s *Service) prepareOwnerPromises(ctx context.Context, src assistantwire.InboxSource) []connector.InboxThing {
	hasObservation := false
	for _, t := range src.Things {
		hasObservation = hasObservation || t.OwnerPromise != nil || t.Kind == connectorwire.OwnerPromiseKind
	}
	if !hasObservation {
		return src.Things
	}

	out := make([]connector.InboxThing, 0, len(src.Things))
	st, _ := s.store().(promiseObservationStore)
	cfg, cfgErr := s.inboxConfigFresh(ctx)
	now := time.Now()
	todosReady := s.ensureTodosLoaded(ctx) == nil
	var classifications map[string]promiseClassification
	var readClassifications, savedClassifications bool
	modelBudget := 2
	for _, t := range src.Things {
		p := t.OwnerPromise
		if p == nil && t.Kind != connectorwire.OwnerPromiseKind {
			out = append(out, t)
			continue
		}
		if src.ID != "larkcli" || src.Extension || p == nil || t.Kind != connectorwire.OwnerPromiseKind ||
			p.ChatID == "" || p.TopicID == "" || !strings.HasPrefix(t.Thread, "owner-promise:") || t.Last == "" {
			continue
		}
		if t.Done != "" {
			out = append(out, t)
			continue
		}
		if cfgErr != nil || st == nil || !todosReady || t.At.IsZero() || now.Before(t.At.Add(15*time.Minute)) ||
			now.Sub(t.At) > 2*time.Hour || inboxVisibilityMatch(inboxVisibilityThingItem(src.ID, t), cfg.Visibility) {
			continue
		}

		id := connectorInboxItemID(src.ID, t.Thread, t.Last)
		if s.ownerPromiseCovered(p, t.Last, id) {
			t.Done = connector.DoneRead
			out = append(out, t)
			continue
		}
		b := &s.inbox
		b.mu.Lock()
		alreadyRecorded := b.has(id)
		b.mu.Unlock()
		if alreadyRecorded {
			continue
		}
		echo, err := st.HasSentAsUserMessage(ctx, t.Last, t.At.Add(-5*time.Minute))
		if err != nil || echo {
			continue
		}
		if p.Model {
			if p.RequestID == "" || strings.TrimSpace(t.Context) == "" {
				continue
			}
			if !readClassifications {
				if _, err := st.Setting(ctx, ownerPromiseClassifications, &classifications); err != nil {
					continue
				}
				readClassifications = true
				if classifications == nil {
					classifications = map[string]promiseClassification{}
				}
				for key, receipt := range classifications {
					if now.Sub(receipt.At) > 2*time.Hour {
						delete(classifications, key)
					}
				}
			}
			receipt, known := classifications[id]
			if !known {
				if modelBudget == 0 {
					continue
				}
				modelBudget--
				accepted, err := s.classifyOwnerPromise(ctx, t)
				if err != nil {
					continue
				}
				receipt = promiseClassification{At: t.At, Accepted: accepted}
				classifications[id] = receipt
				savedClassifications = true
			}
			if !receipt.Accepted {
				continue
			}
		}

		t.CanReply, t.ReplyTo = false, ""
		lang := s.lang(ctx)
		t.Tier = connector.TierAction
		t.Why = i18n.T(lang, "assistant-owner_promise.uncovered")
		t.Label = i18n.T(lang, "assistant-owner_promise.label")
		t.Title = t.Label
		if t.Context == "" {
			t.Context = t.Preview
		} else {
			t.Context = t.Preview + "\n\n" + t.Context
		}
		out = append(out, t)
	}
	if savedClassifications {
		if err := st.PutSetting(ctx, ownerPromiseClassifications, classifications); err != nil {
			s.logf("inbox: save owner follow-up classifications: %v", err)
		}
	}
	return out
}

func (s *Service) classifyOwnerPromise(ctx context.Context, t connector.InboxThing) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	input, _ := json.Marshal(map[string]string{"owner_reply": t.Preview, "request": t.Context})
	var out struct {
		Status string `json:"status"`
	}
	err := s.inboxModelJSON(ctx, purposeTriage, prompts.RenderOwnerPromise(answerLanguage(string(s.lang(ctx)))), string(input), 64, &out)
	return out.Status == "promise", err
}

// A topic can carry several unrelated requests. Exact source messages and
// existing Inbox bindings prove coverage, while a topic or title alone does not.
func (s *Service) ownerPromiseCovered(p *connectorwire.OwnerPromise, message, itemID string) bool {
	if p == nil {
		return false
	}
	requestItems := map[string]bool{}
	if p.RequestID != "" {
		thread := "larkcli:chat:" + string(p.ChatID) + ":thread:" + string(p.TopicID)
		requestItems[connectorInboxItemID("larkcli", thread, string(p.RequestID))] = true
		// The open request can have accumulated multiple messages since the card
		// was created. Find the exact current move as well as the first-move id.
		b := &s.inbox
		b.mu.Lock()
		for _, it := range b.items {
			if it.Source == "larkcli" && it.Connector != nil && it.Connector.Thread == thread && it.Last == string(p.RequestID) {
				requestItems[it.ID] = true
			}
		}
		b.mu.Unlock()
	}

	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if !taskOpen(*t) {
			continue
		}
		if t.InboxItem == itemID || requestItems[t.InboxItem] {
			return true
		}
		if from := t.Source; from != nil && from.Channel == "feishu" && from.ChatID == string(p.ChatID) &&
			(from.MessageID == message || p.RequestID != "" && from.MessageID == string(p.RequestID)) {
			return true
		}
	}
	return false
}
