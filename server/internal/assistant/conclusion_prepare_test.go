package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Preparing the durable conclusion must not release the in-memory claim: another save retains it, while a sweep or
// duplicate initial conclusion cannot send it until the original call ends (#10393).
func TestConclusionPreparedBeforeProviderCall(t *testing.T) {
	needsLarkLayer(t)
	st := conclusionDatabase(t)
	s, f, todo, _, _ := conclusionServiceOn(t, st, true)
	calls := 0
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var card struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal(p, &card); err != nil {
			return nil, err
		}
		calls++
		if calls > 1 {
			t.Fatal("a second initial send or sweep bypassed the claim")
		}
		check := func() {
			t.Helper()
			var saved map[string]*acceptAsk
			if _, err := st.Setting(t.Context(), acceptanceKey, &saved); err != nil {
				t.Fatal(err)
			}
			a := saved[todo.ID]
			if a == nil || !a.Conclusion || !a.BySupervisor || a.AcceptedAt.IsZero() || a.MessageID != "" || a.NoCard ||
				acceptCardKey(todo.ID, a.AskedAt) != card.UUID {
				t.Fatalf("provider reached without the complete keyed conclusion on disk: %+v", a)
			}
		}
		check()
		// Another book write while the provider is blocked must not overwrite the durable record with its empty claim.
		s.accept.mu.Lock()
		s.accept.asks[todo.ID].Earlier = append(s.accept.asks[todo.ID].Earlier, "om_earlier")
		err := s.saveAcceptErrLocked(t.Context())
		s.accept.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		check()
		var saved map[string]*acceptAsk
		_, _ = st.Setting(t.Context(), acceptanceKey, &saved)
		if len(saved[todo.ID].Earlier) != 1 {
			t.Fatal("an earlier round's card was lost during preparation")
		}
		s.retryAcceptance(time.Now().Add(10 * time.Minute))
		if s.askConclusion(t.Context(), todo, conclusionInput{Outcome: outcomeDone, Verified: true}) {
			t.Fatal("a duplicate initial conclusion was sent")
		}
		return nil, errLarkDown
	})
	a := supervisorConclusion(t, s, todo.ID)
	if calls != 1 || len(a.Earlier) != 1 {
		t.Fatalf("calls=%d, ask=%+v", calls, a)
	}
}

type conclusionFailStore struct {
	Store
	unavailable atomic.Bool
}

func (s *conclusionFailStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == acceptanceKey && s.unavailable.Load() {
		return errors.New("synthetic acceptance store unavailable")
	}
	return s.Store.PutSetting(ctx, key, value)
}

// A failed preparation releases the claim for retry but must never reach the provider from memory alone. The retry
// must also save successfully before sending; when storage recovers it uses the original key once.
func TestConclusionSaveFailureWaitsBeforeSending(t *testing.T) {
	needsLarkLayer(t)
	st := &conclusionFailStore{Store: conclusionDatabase(t)}
	s, f, todo, _, _ := conclusionServiceOn(t, st, true)
	lark := newCardLark(f, "结论：")
	st.unavailable.Store(true)
	a := supervisorConclusion(t, s, todo.ID)
	s.retryAcceptance(a.AskedAt.Add(10 * time.Minute))
	if _, keys := lark.sent(); len(keys) != 0 {
		t.Fatalf("sent without durable preparation: %d", len(keys))
	}
	st.unavailable.Store(false)
	s.retryAcceptance(a.AskedAt.Add(20 * time.Minute))
	s.retryAcceptance(a.AskedAt.Add(30 * time.Minute))
	cards, keys := lark.sent()
	if len(cards) != 1 || len(keys) != 1 || keys[0] != acceptCardKey(todo.ID, a.AskedAt) {
		t.Fatalf("cards=%d keys=%q, want one under the original key", len(cards), keys)
	}
	var saved map[string]*acceptAsk
	if _, err := st.Setting(t.Context(), acceptanceKey, &saved); err != nil {
		t.Fatal(err)
	}
	if saved[todo.ID] == nil || saved[todo.ID].MessageID == "" {
		t.Fatal("delivered conclusion was not kept")
	}
}

func conclusionDatabase(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}
