package kb

import (
	"context"
	"sync"
	"testing"
	"time"
)

// topicBudgetCall is the deadline and output ceiling one topic model call received.
type topicBudgetCall struct {
	hasDeadline bool
	remaining   time.Duration
	maxTokens   int
}

// topicBudgetModel records every JSONMax call's context budget. It answers with an empty object, so the distill/merge
// stage only reaches the call whose budget is under test.
type topicBudgetModel struct {
	mu    sync.Mutex
	calls []topicBudgetCall
}

func (m *topicBudgetModel) JSONMax(ctx context.Context, _, _ string, maxTokens int, _ any) error {
	c := topicBudgetCall{maxTokens: maxTokens}
	if dl, ok := ctx.Deadline(); ok {
		c.hasDeadline = true
		c.remaining = time.Until(dl)
	}
	m.mu.Lock()
	m.calls = append(m.calls, c)
	m.mu.Unlock()
	return nil
}

func (m *topicBudgetModel) first(t *testing.T) topicBudgetCall {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) != 1 {
		t.Fatalf("model calls = %d, want exactly 1 (%+v)", len(m.calls), m.calls)
	}
	return m.calls[0]
}

// The topic distill/merge budget is the named six minute value matching projectRefreshTimeout. Asserting the constant
// directly means lowering the number on purpose moves this test.
func TestTopicModelTimeoutIsSixMinutes(t *testing.T) {
	if topicModelTimeout != 6*time.Minute {
		t.Fatalf("topicModelTimeout = %v, want 6m", topicModelTimeout)
	}
}

// assertSixMinuteBudget checks the call carried a deadline about six minutes out. The one-minute lower slack is the
// same headroom the project timeout test uses: far above the old three minute value, tolerant of scheduling stalls.
func assertSixMinuteBudget(t *testing.T, c topicBudgetCall) {
	t.Helper()
	if c.maxTokens != 12000 {
		t.Fatalf("maxTokens = %d, want the kb.topics 12000 ceiling", c.maxTokens)
	}
	if !c.hasDeadline {
		t.Fatal("the context passed to the model has no deadline")
	}
	// Fixed floor independent of topicModelTimeout: the pre-fix budget was 3m, so a call under 5m is the old cut-off
	// even if the named constant and this assertion's window were changed together.
	if c.remaining < 5*time.Minute {
		t.Fatalf("model context budget = %v, want about 6m (the old value was 3m)", c.remaining.Round(time.Second))
	}
	if c.remaining > topicModelTimeout {
		t.Fatalf("model context budget = %v, want no more than %v", c.remaining.Round(time.Second), topicModelTimeout)
	}
}

// A topic distill call gets the larger six minute background budget, not the old three minute cut-off.
func TestTopicDistillCallHasTheSixMinuteBudget(t *testing.T) {
	m := &topicBudgetModel{}
	e := testEngine(t, m)
	quietProject(t, e, "timeout-budget", "定了方案并推进落地")

	if n := e.DistillTopics(context.Background(), 1); n != 1 {
		t.Fatalf("DistillTopics ran %d calls, want 1", n)
	}
	assertSixMinuteBudget(t, m.first(t))
}

// A topic merge call gets the same six minute budget for its 12000-token JSON answer.
func TestTopicMergeCallHasTheSixMinuteBudget(t *testing.T) {
	m := &topicBudgetModel{}
	e := testEngine(t, m)
	putTopic(t, e, &Topic{Slug: "budget", Name: "Budget", Dirty: true}, topicFile{
		Pending: []TopicPoint{{Kind: "fact", Text: "a pending point",
			Sources: []TopicSource{{Kind: KindProject, ID: "p", Day: "2026-11-08"}}, When: "2026-11-08"}},
	})

	if n := e.RefreshTopics(context.Background(), 1); n != 1 {
		t.Fatalf("RefreshTopics ran %d calls, want 1", n)
	}
	assertSixMinuteBudget(t, m.first(t))
}
