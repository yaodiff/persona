package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// seedFeedPlans puts n plan cards, each naming its own plan, into a new conversation of st and returns the conversation
// and the plan ids in order.
func seedFeedPlans(tb testing.TB, s *Service, n int) (string, []string) {
	tb.Helper()
	ctx := context.Background()
	conversation := "feedplans_" + randomHex(6)
	prompt := strings.Repeat("Fix the crash on the login page and add a test. ", 30)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var ids []string
	for i := range n {
		p := &TaskPlan{ID: "plan_" + randomHex(6), Conversation: conversation, State: planOpen, CreatedAt: now, UpdatedAt: now,
			Tasks: []PlanTask{{Title: fmt.Sprintf("Task %d", i), Prompt: prompt, Machine: "box", Cwd: "/code/app"}}}
		if err := s.savePlan(ctx, p); err != nil {
			tb.Fatal(err)
		}
		if _, err := s.appendFeedAt(ctx, conversation, feedPlan, p.ID, map[string]any{"plan": p}, now.Add(time.Duration(i)*time.Millisecond)); err != nil {
			tb.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	return conversation, ids
}

// BenchmarkFeedPlans reads a feed of 100 plan cards with their plans from Postgres.
func BenchmarkFeedPlans(b *testing.B) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		b.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(database.Close)
	s := New(Options{Hub: newFakeHub(), Store: func() Store { return database }, Logf: func(string, ...any) {}})
	conversation, ids := seedFeedPlans(b, s, 100)
	b.ReportAllocs()
	for b.Loop() {
		items, plans, err := s.feed(ctx, conversation, time.Time{})
		if err != nil || len(items) != len(ids) || len(plans) != len(ids) {
			b.Fatalf("feed: %d items, %d plans, %v", len(items), len(plans), err)
		}
	}
}

// countingPlans counts the plan reads that reach the database; with failBatch set, the batch read fails.
type countingPlans struct {
	*db.DB
	one, batch atomic.Int64
	failBatch  atomic.Bool
}

func (c *countingPlans) TaskPlan(ctx context.Context, id string) (*db.TaskPlanRow, error) {
	c.one.Add(1)
	return c.DB.TaskPlan(ctx, id)
}

func (c *countingPlans) TaskPlansByID(ctx context.Context, ids []string) ([]db.TaskPlanRow, error) {
	c.batch.Add(1)
	if c.failBatch.Load() {
		return nil, errors.New("connection reset")
	}
	return c.DB.TaskPlansByID(ctx, ids)
}

// A feed's plans come from one read however many plan cards it has (one read per card was a round trip each), and
// they are the plans loadPlan reads; a card whose plan is gone has none, and when the read fails the cards still come.
func TestFeedReadsPlansInOneQuery(t *testing.T) {
	st := &countingPlans{DB: todoTestDB(t)}
	s := newServiceOn(t, newFakeHub(), st)
	ctx := context.Background()
	conversation, ids := seedFeedPlans(t, s, 20)
	// The first plan's card once more, and a card whose plan is gone.
	for _, ref := range []string{ids[0], "plan_gone"} {
		if _, err := s.appendFeed(ctx, conversation, feedPlan, ref, nil); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/conversations/"+conversation+"/feed", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Items []FeedItem           `json:"items"`
		Plans map[string]*TaskPlan `json:"plans"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if one, batch := st.one.Load(), st.batch.Load(); one != 0 || batch != 1 {
		t.Fatalf("%d TaskPlan and %d TaskPlansByID calls, want one TaskPlansByID", one, batch)
	}
	if len(out.Items) != len(ids)+2 || len(out.Plans) != len(ids) {
		t.Fatalf("%d items and %d plans, want %d and %d", len(out.Items), len(out.Plans), len(ids)+2, len(ids))
	}

	_, plans, err := s.feed(ctx, conversation, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		want, err := s.loadPlan(ctx, id)
		if err != nil || out.Plans[id] == nil || !reflect.DeepEqual(plans[id], want) {
			t.Fatalf("plan %s: %+v, loadPlan %+v (%v)", id, plans[id], want, err)
		}
	}
	if _, ok := plans["plan_gone"]; ok {
		t.Fatal("a plan that is gone is in the map")
	}

	st.failBatch.Store(true)
	items, plans, err := s.feed(ctx, conversation, time.Time{})
	if err != nil || len(items) != len(ids)+2 || len(plans) != 0 {
		t.Fatalf("failed plan read: %d items, %d plans, %v", len(items), len(plans), err)
	}
}
