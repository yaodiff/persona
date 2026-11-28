package kb

import (
	"context"
	"errors"
	"testing"
	"time"
)

// errProjectTimeoutCaptured is returned by projectTimeoutCaptureModel once it has recorded the context deadline, so the
// refresh stops before commitProject and the test only exercises the call budget.
var errProjectTimeoutCaptured = errors.New("project timeout captured")

type projectTimeoutCaptureModel struct {
	hasDeadline bool
	remaining   time.Duration
	maxTokens   int
}

func (m *projectTimeoutCaptureModel) JSONMax(ctx context.Context, _, _ string, maxTokens int, _ any) error {
	m.maxTokens = maxTokens
	if dl, ok := ctx.Deadline(); ok {
		m.hasDeadline = true
		m.remaining = time.Until(dl)
	}
	return errProjectTimeoutCaptured
}

// A kb.project call gets the named six minute budget. The constant is asserted directly (so lowering it moves the
// number on purpose) and the context handed to the model must actually carry that deadline, not just mention it.
func TestProjectRefreshCallUsesTheSixMinuteTimeout(t *testing.T) {
	if projectRefreshTimeout != 6*time.Minute {
		t.Fatalf("projectRefreshTimeout = %v, want 6m", projectRefreshTimeout)
	}

	model := &projectTimeoutCaptureModel{}
	e := testEngine(t, model)
	dirtyProject(t, e, "timeout-budget")
	p, ok := e.Store.Project("timeout-budget")
	if !ok {
		t.Fatal("stored project not found")
	}

	called, err := e.refreshProjectCall(context.Background(), p)
	if !called || !errors.Is(err, errProjectTimeoutCaptured) {
		t.Fatalf("called=%v err=%v, want the model to be asked and its sentinel back", called, err)
	}
	if model.maxTokens != 8000 {
		t.Fatalf("maxTokens=%d, want the kb.project 8000 ceiling", model.maxTokens)
	}
	if !model.hasDeadline {
		t.Fatal("the context passed to the model has no deadline")
	}
	// The parent carries no deadline, so the model's budget is projectRefreshTimeout counted from just before the call.
	// Allow a generous minute of setup/stall slack: this is still far above the old three minute value.
	if model.remaining > projectRefreshTimeout || model.remaining < projectRefreshTimeout-time.Minute {
		t.Fatalf("model context budget = %v, want about %v", model.remaining, projectRefreshTimeout)
	}
}
