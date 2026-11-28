package mergetrain

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/todo/conflict"
)

// TestMergeTrainStaysPure guards the package against accidental IO, network, clock,
// database, or unexpected internal/external dependencies: mergetrain must remain a pure
// state machine importing only pure Go stdlib packages and todo/conflict.
func TestMergeTrainStaysPure(t *testing.T) {
	allowed := []string{
		"cmp",
		"errors",
		"fmt",
		"net/url",
		"slices",
		"sort",
		"strconv",
		"strings",
		"sync",
		"github.com/nwverse/persona/server/internal/assistant/todo/conflict",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("expected non-empty Go source files in mergetrain package")
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !slices.Contains(allowed, p) {
				t.Errorf("%s imports %s: mergetrain package must stay pure (allowed: %v)", name, p, allowed)
			}
		}
	}
}

func TestSameRepoStrictSerialSingleHeadAndAdvance(t *testing.T) {
	tr := New()

	e1, err := tr.Enqueue(Entry{TodoID: "todo_1", PRURL: "https://github.com/nwverse/persona/pull/101"})
	if err != nil {
		t.Fatalf("Enqueue todo_1: %v", err)
	}
	if e1.RepoKey != "github.com/nwverse/persona" || e1.State != StateActive {
		t.Fatalf("unexpected e1: %+v", e1)
	}

	e2, err := tr.Enqueue(Entry{TodoID: "todo_2", PRURL: "https://github.com/nwverse/persona/pull/102"})
	if err != nil {
		t.Fatalf("Enqueue todo_2: %v", err)
	}
	if e2.State != StateQueued {
		t.Fatalf("expected todo_2 to be queued behind active head, got %s", e2.State)
	}

	// Idempotent re-enqueue must not duplicate or disturb state.
	e1Dup, err := tr.Enqueue(Entry{TodoID: "todo_1", PRURL: "https://github.com/nwverse/persona/pull/101"})
	if err != nil {
		t.Fatalf("duplicate Enqueue todo_1: %v", err)
	}
	if e1Dup.State != StateActive || e1Dup.PRNumber != 101 {
		t.Fatalf("duplicate enqueue mutated entry: %+v", e1Dup)
	}

	// Checks red -> wait.
	if dec := tr.EvaluateHead("github.com/nwverse/persona", false); dec.Action != ActionWaitQueue || dec.TodoID != "todo_1" {
		t.Fatalf("EvaluateHead(false) = %+v, want ActionWaitQueue for todo_1", dec)
	}

	// Checks green -> rebase and merge for todo_1.
	if dec := tr.EvaluateHead("github.com/nwverse/persona", true); dec.Action != ActionRebaseAndMerge || dec.TodoID != "todo_1" {
		t.Fatalf("EvaluateHead(true) = %+v, want ActionRebaseAndMerge for todo_1", dec)
	}

	// Merge todo_1 -> todo_2 automatically promoted to StateActive.
	dec, err := tr.RecordMerged("https://github.com/nwverse/persona/pull/101")
	if err != nil {
		t.Fatalf("RecordMerged todo_1: %v", err)
	}
	if dec.TodoID != "todo_2" || dec.State != StateActive {
		t.Fatalf("RecordMerged decision = %+v, want todo_2 promoted to active", dec)
	}

	// Cancel todo_2 -> queue becomes empty.
	dec, err = tr.Cancel("https://github.com/nwverse/persona/pull/102")
	if err != nil {
		t.Fatalf("Cancel todo_2: %v", err)
	}
	if dec.Action != ActionNoop || dec.TodoID != "" {
		t.Fatalf("Cancel last entry decision = %+v, want ActionNoop", dec)
	}
	if eval := tr.EvaluateHead("github.com/nwverse/persona", true); eval.Action != ActionNoop {
		t.Fatalf("EvaluateHead on empty queue = %+v, want ActionNoop", eval)
	}
}

func TestCanonicalPRIdentityDistinguishesTwoPRsFromOneTodo(t *testing.T) {
	tr := New()
	const first, second = "https://github.com/example/repo/pull/17", "https://github.com/example/repo/pull/18"
	for _, e := range []Entry{
		{TodoID: "todo_a", PRURL: first},
		{TodoID: "todo_a", PRURL: second},
		{TodoID: "todo_b", PRURL: " HTTPS://GitHub.COM/Example/Repo/pull/017/files?view=1#show "},
	} {
		if _, err := tr.Enqueue(e); err != nil {
			t.Fatal(err)
		}
	}
	repo := tr.Snapshot().Repos[0]
	if repo.Head.PRURL != first || repo.Head.TodoID != "todo_a" || len(repo.Queue) != 1 || repo.Queue[0].PRURL != second {
		t.Fatalf("PR identity collided or duplicated: %+v", repo)
	}
	if _, err := tr.RecordMerged(first); err != nil {
		t.Fatal(err)
	}
	if head, ok := tr.Head(repo.RepoKey); !ok || head.PRURL != second {
		t.Fatalf("merging the first PR lost the sibling: %+v", head)
	}
}

func TestCrossRepoParallelIndependence(t *testing.T) {
	tr := New()

	a1, err := tr.Enqueue(Entry{TodoID: "todo_a1", PRURL: "https://example.com/repo/alpha/pull/1"})
	if err != nil {
		t.Fatal(err)
	}
	b1, err := tr.Enqueue(Entry{TodoID: "todo_b1", PRURL: "https://example.com/repo/beta/pull/1"})
	if err != nil {
		t.Fatal(err)
	}
	if a1.State != StateActive || b1.State != StateActive {
		t.Fatalf("cross-repo heads must both be active: a1=%s b1=%s", a1.State, b1.State)
	}

	// Pause repo/alpha on rebase escalation; repo/beta must remain completely unblocked.
	_, _ = tr.RecordRebaseFailure("https://example.com/repo/alpha/pull/1", "conflict 1")
	esc, err := tr.RecordRebaseFailure("https://example.com/repo/alpha/pull/1", "conflict 2")
	if err != nil || esc.Action != ActionEscalateNeedsUser {
		t.Fatalf("expected repo/alpha escalation, got (%+v, %v)", esc, err)
	}

	betaDec := tr.EvaluateHead("example.com/repo/beta", true)
	if betaDec.Action != ActionRebaseAndMerge || betaDec.TodoID != "todo_b1" {
		t.Fatalf("repo/beta must advance independently, got %+v", betaDec)
	}
}

func TestPriorityOrderingNeverPreemptsActiveHead(t *testing.T) {
	tr := New()

	head, _ := tr.Enqueue(Entry{TodoID: "low_head", PRURL: "https://example.com/repo/app/pull/1", Priority: 1})
	if head.State != StateActive {
		t.Fatalf("expected low_head active, got %s", head.State)
	}

	_, _ = tr.Enqueue(Entry{TodoID: "med_1", PRURL: "https://example.com/repo/app/pull/2", Priority: 5})
	_, _ = tr.Enqueue(Entry{TodoID: "urgent", PRURL: "https://example.com/repo/app/pull/3", Priority: 100})
	_, _ = tr.Enqueue(Entry{TodoID: "med_2", PRURL: "https://example.com/repo/app/pull/4", Priority: 5})

	snap := tr.Snapshot()
	if len(snap.Repos) != 1 || snap.Repos[0].Head == nil {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.Repos[0].Head.TodoID != "low_head" {
		t.Fatalf("active head was preempted by higher priority entry: head=%s", snap.Repos[0].Head.TodoID)
	}
	gotQueue := make([]string, len(snap.Repos[0].Queue))
	for i, e := range snap.Repos[0].Queue {
		gotQueue[i] = e.TodoID
	}
	wantQueue := []string{"urgent", "med_1", "med_2"}
	if !reflect.DeepEqual(gotQueue, wantQueue) {
		t.Fatalf("queue order = %v, want %v", gotQueue, wantQueue)
	}

	// Once low_head merges, urgent becomes the new head.
	dec, err := tr.RecordMerged("https://example.com/repo/app/pull/1")
	if err != nil || dec.TodoID != "urgent" || dec.State != StateActive {
		t.Fatalf("expected urgent promoted after head merge, got (%+v, %v)", dec, err)
	}
}

func TestApplyConflictsRuntimeSerializationAndReasons(t *testing.T) {
	tr := New()

	_, _ = tr.Enqueue(Entry{TodoID: "todo_a", PRURL: "https://example.com/repo/app/pull/9"})
	_, _ = tr.Enqueue(Entry{TodoID: "todo_b", PRURL: "https://example.com/repo/app/pull/10"})

	// Detect conflicts using the real conflict package:
	// - todo_a & todo_b share a runtime changed file (ActionSerialize)
	// - todo_c & todo_d have static-only overlap with ForceParallel (ActionWarnOverridden -> ignored)
	pairs := conflict.Detect([]conflict.TaskScope{
		{
			TodoID:        "todo_b",
			RepoKey:       "example.com/repo/app",
			WritePrefixes: []string{"server/internal/hub"},
			ChangedFiles:  []string{"server/internal/db/todos.go"},
			ForceParallel: true,
		},
		{
			TodoID:        "todo_a",
			RepoKey:       "example.com/repo/app",
			WritePrefixes: []string{"server/internal/manager"},
			ChangedFiles:  []string{"./server/internal/db/todos.go"},
			ForceParallel: true,
		},
		{
			TodoID:        "todo_c",
			RepoKey:       "example.com/repo/other",
			WritePrefixes: []string{"web/src/views/"},
			ForceParallel: true,
		},
		{
			TodoID:        "todo_d",
			RepoKey:       "example.com/repo/other",
			WritePrefixes: []string{"web/src/views/Home.vue"},
		},
	})
	if len(pairs) != 2 {
		t.Fatalf("expected 2 detected pairs, got %+v", pairs)
	}

	decisions := tr.ApplyConflicts(pairs)
	if len(decisions) != 1 || decisions[0].RepoKey != "example.com/repo/app" || decisions[0].TodoID != "todo_a" {
		t.Fatalf("unexpected ApplyConflicts decisions: %+v", decisions)
	}

	snap := tr.Snapshot()
	if len(snap.Repos) != 1 || snap.Repos[0].RepoKey != "example.com/repo/app" {
		t.Fatalf("ActionWarnOverridden repo/other must not be serialized, got %+v", snap.Repos)
	}
	if snap.Repos[0].Head == nil || snap.Repos[0].Head.TodoID != "todo_a" || snap.Repos[0].Head.State != StateActive {
		t.Fatalf("expected todo_a active at head, got %+v", snap.Repos[0].Head)
	}
	if len(snap.Repos[0].Queue) != 1 || snap.Repos[0].Queue[0].TodoID != "todo_b" || snap.Repos[0].Queue[0].State != StateQueued {
		t.Fatalf("expected todo_b queued behind todo_a, got %+v", snap.Repos[0].Queue)
	}
	if len(snap.Repos[0].Head.ConflictReasons) != 1 || !strings.Contains(snap.Repos[0].Head.ConflictReasons[0], "server/internal/db/todos.go") {
		t.Fatalf("missing runtime conflict reason on head: %+v", snap.Repos[0].Head.ConflictReasons)
	}
	if len(snap.Repos[0].Queue[0].ConflictReasons) != 1 || !strings.Contains(snap.Repos[0].Queue[0].ConflictReasons[0], "todo_a") {
		t.Fatalf("missing runtime conflict reason on queued entry: %+v", snap.Repos[0].Queue[0].ConflictReasons)
	}

	// Re-applying conflicts in reverse order must be idempotent and not duplicate reasons.
	rev := slices.Clone(pairs)
	slices.Reverse(rev)
	tr.ApplyConflicts(rev)
	eA, _ := tr.Entry("https://example.com/repo/app/pull/9")
	if len(eA.ConflictReasons) != 1 {
		t.Fatalf("conflict reasons duplicated on idempotent ApplyConflicts: %+v", eA.ConflictReasons)
	}
}

func TestRebaseCircuitBreakerAndResetOnSuccess(t *testing.T) {
	tr := New()
	_, _ = tr.Enqueue(Entry{TodoID: "todo_1", PRURL: "https://example.com/repo/app/pull/5"})
	_, _ = tr.Enqueue(Entry{TodoID: "todo_2", PRURL: "https://example.com/repo/app/pull/6"})

	// First failure -> still active, retry permitted.
	d1, err := tr.RecordRebaseFailure("https://example.com/repo/app/pull/5", "merge conflict in a.go")
	if err != nil {
		t.Fatal(err)
	}
	if d1.Action != ActionWaitQueue || d1.State != StateActive {
		t.Fatalf("first rebase failure = %+v, want ActionWaitQueue/StateActive", d1)
	}

	// Success resets consecutive streak and last error.
	if err := tr.RecordRebaseSuccess("https://example.com/repo/app/pull/5"); err != nil {
		t.Fatalf("RecordRebaseSuccess: %v", err)
	}
	e1, _ := tr.Entry("https://example.com/repo/app/pull/5")
	if e1.RebaseFailures != 0 || e1.LastError != "" {
		t.Fatalf("expected failure counter cleared after success, got %+v", e1)
	}

	// Next single failure after reset must NOT trip circuit breaker.
	d2, err := tr.RecordRebaseFailure("https://example.com/repo/app/pull/5", "transient conflict 1")
	if err != nil || d2.Action != ActionWaitQueue || d2.State != StateActive {
		t.Fatalf("first failure after reset must not escalate: (%+v, %v)", d2, err)
	}

	// Second consecutive failure trips circuit breaker -> StateEscalated / ActionEscalateNeedsUser.
	d3, err := tr.RecordRebaseFailure("https://example.com/repo/app/pull/5", "persistent conflict 2")
	if err != nil || d3.Action != ActionEscalateNeedsUser || d3.State != StateEscalated {
		t.Fatalf("second consecutive failure must escalate: (%+v, %v)", d3, err)
	}

	// EvaluateHead on escalated entry must NEVER return ActionRebaseAndMerge even when checks are green.
	eval := tr.EvaluateHead("example.com/repo/app", true)
	if eval.Action != ActionEscalateNeedsUser || eval.State != StateEscalated || eval.TodoID != "todo_1" {
		t.Fatalf("EvaluateHead on escalated head = %+v, want ActionEscalateNeedsUser", eval)
	}

	// Default fail-closed policy keeps todo_2 queued until todo_1 is cancelled/resolved.
	e2, _ := tr.Entry("https://example.com/repo/app/pull/6")
	if e2.State != StateQueued {
		t.Fatalf("default head-hold policy must keep todo_2 queued, got %s", e2.State)
	}

	// Cancelling the escalated head advances todo_2 to active head.
	afterCancel, err := tr.Cancel("https://example.com/repo/app/pull/5")
	if err != nil || afterCancel.TodoID != "todo_2" || afterCancel.State != StateActive {
		t.Fatalf("cancelling escalated head should advance todo_2, got (%+v, %v)", afterCancel, err)
	}
}

func TestQueuedEscalatedEntryPreservesEscalationOnPromotion(t *testing.T) {
	t.Run("head_merged", func(t *testing.T) {
		tr := New()
		_, _ = tr.Enqueue(Entry{TodoID: "todo_1", PRURL: "https://example.com/repo/app/pull/5"})
		_, _ = tr.Enqueue(Entry{TodoID: "todo_2", PRURL: "https://example.com/repo/app/pull/6"})
		_, _ = tr.Enqueue(Entry{TodoID: "todo_3", PRURL: "https://example.com/repo/app/pull/7"})

		// Trip the circuit breaker on queued todo_2 while todo_1 is still active head.
		d1, err := tr.RecordRebaseFailure("https://example.com/repo/app/pull/6", "conflict 1")
		if err != nil || d1.Action != ActionWaitQueue || d1.State != StateQueued {
			t.Fatalf("queued failure 1 = (%+v, %v), want ActionWaitQueue/StateQueued", d1, err)
		}
		d2, err := tr.RecordRebaseFailure("https://example.com/repo/app/pull/6", "conflict 2")
		if err != nil || d2.Action != ActionEscalateNeedsUser || d2.State != StateEscalated {
			t.Fatalf("queued failure 2 = (%+v, %v), want ActionEscalateNeedsUser/StateEscalated", d2, err)
		}

		// Merging todo_1 promotes todo_2 to head, but MUST preserve StateEscalated.
		promoted, err := tr.RecordMerged("https://example.com/repo/app/pull/5")
		if err != nil {
			t.Fatalf("RecordMerged todo_1: %v", err)
		}
		if promoted.TodoID != "todo_2" || promoted.Action != ActionEscalateNeedsUser || promoted.State != StateEscalated {
			t.Fatalf("RecordMerged decision = %+v, want todo_2 ActionEscalateNeedsUser/StateEscalated", promoted)
		}

		e2, ok := tr.Entry("https://example.com/repo/app/pull/6")
		if !ok || e2.State != StateEscalated || e2.RebaseFailures != 2 {
			t.Fatalf("promoted todo_2 = %+v, want StateEscalated with RebaseFailures=2", e2)
		}

		// EvaluateHead, RecordRebaseSuccess, and Approve must all respect the tripped circuit breaker.
		if eval := tr.EvaluateHead("example.com/repo/app", true); eval.Action != ActionEscalateNeedsUser || eval.State != StateEscalated || eval.TodoID != "todo_2" {
			t.Fatalf("EvaluateHead after promoting escalated entry = %+v, want ActionEscalateNeedsUser", eval)
		}
		if err := tr.RecordRebaseSuccess("https://example.com/repo/app/pull/6"); !errors.Is(err, ErrEntryEscalated) {
			t.Fatalf("RecordRebaseSuccess on promoted escalated entry = %v, want ErrEntryEscalated", err)
		}
		if _, err := tr.Approve("https://example.com/repo/app/pull/6"); !errors.Is(err, ErrEntryEscalated) {
			t.Fatalf("Approve on promoted escalated entry = %v, want ErrEntryEscalated", err)
		}

		// Cancelling a queued entry behind an escalated head still reports the head as escalated.
		cancelQueued, err := tr.Cancel("https://example.com/repo/app/pull/7")
		if err != nil {
			t.Fatalf("Cancel todo_3: %v", err)
		}
		if cancelQueued.TodoID != "todo_2" || cancelQueued.Action != ActionEscalateNeedsUser || cancelQueued.State != StateEscalated {
			t.Fatalf("Cancel queued behind escalated head = %+v, want todo_2 ActionEscalateNeedsUser/StateEscalated", cancelQueued)
		}

		// Only after explicitly cancelling escalated head todo_2 does a fresh queued entry activate.
		_, _ = tr.Enqueue(Entry{TodoID: "todo_4", PRURL: "https://example.com/repo/app/pull/8"})
		afterCancel, err := tr.Cancel("https://example.com/repo/app/pull/6")
		if err != nil || afterCancel.TodoID != "todo_4" || afterCancel.State != StateActive {
			t.Fatalf("Cancel escalated head = (%+v, %v), want todo_4 StateActive", afterCancel, err)
		}
	})

	t.Run("head_cancelled", func(t *testing.T) {
		tr := New()
		_, _ = tr.Enqueue(Entry{TodoID: "todo_a", PRURL: "https://example.com/repo/app/pull/9"})
		_, _ = tr.Enqueue(Entry{TodoID: "todo_b", PRURL: "https://example.com/repo/app/pull/10"})
		_, _ = tr.RecordRebaseFailure("https://example.com/repo/app/pull/10", "conflict 1")
		_, _ = tr.RecordRebaseFailure("https://example.com/repo/app/pull/10", "conflict 2")

		promoted, err := tr.Cancel("https://example.com/repo/app/pull/9")
		if err != nil {
			t.Fatalf("Cancel todo_a: %v", err)
		}
		if promoted.TodoID != "todo_b" || promoted.Action != ActionEscalateNeedsUser || promoted.State != StateEscalated {
			t.Fatalf("Cancel head decision = %+v, want todo_b ActionEscalateNeedsUser/StateEscalated", promoted)
		}
		if eval := tr.EvaluateHead("example.com/repo/app", true); eval.Action != ActionEscalateNeedsUser || eval.State != StateEscalated {
			t.Fatalf("EvaluateHead after head cancel = %+v, want ActionEscalateNeedsUser", eval)
		}
	})
}

func TestAdvanceOnEscalationSkipsConflictingEntries(t *testing.T) {
	tr := New(WithAdvanceOnEscalation(true))
	_, _ = tr.Enqueue(Entry{TodoID: "todo_1", PRURL: "https://example.com/repo/app/pull/5"})
	_, _ = tr.Enqueue(Entry{TodoID: "todo_2", PRURL: "https://example.com/repo/app/pull/6"})
	_, _ = tr.Enqueue(Entry{TodoID: "todo_3", PRURL: "https://example.com/repo/app/pull/7"})

	// todo_1 conflicts with todo_2, but NOT with todo_3.
	tr.ApplyConflicts([]conflict.PairConflict{{
		TodoA:        "todo_1",
		TodoB:        "todo_2",
		RepoKey:      "example.com/repo/app",
		Layer:        conflict.LayerRuntime,
		Action:       conflict.ActionSerialize,
		RuntimeFiles: []string{"shared.go"},
	}})

	_, _ = tr.RecordRebaseFailure("https://example.com/repo/app/pull/5", "fail 1")
	esc, err := tr.RecordRebaseFailure("https://example.com/repo/app/pull/5", "fail 2")
	if err != nil || esc.Action != ActionEscalateNeedsUser {
		t.Fatalf("expected escalation, got (%+v, %v)", esc, err)
	}

	// todo_1 moved to Escalated; todo_2 conflicts with todo_1 so stays queued; todo_3 advances to head.
	snap := tr.Snapshot()
	rs := snap.Repos[0]
	if rs.Head == nil || rs.Head.TodoID != "todo_3" || rs.Head.State != StateActive {
		t.Fatalf("expected non-conflicting todo_3 promoted to head, got %+v", rs.Head)
	}
	if len(rs.Escalated) != 1 || rs.Escalated[0].TodoID != "todo_1" {
		t.Fatalf("expected todo_1 in escalated list, got %+v", rs.Escalated)
	}
	if len(rs.Queue) != 1 || rs.Queue[0].TodoID != "todo_2" {
		t.Fatalf("expected conflicting todo_2 to remain queued, got %+v", rs.Queue)
	}
}

func TestPolicyHumanReviewRequiredGuardAndApproval(t *testing.T) {
	tr := New()
	_, err := tr.Enqueue(Entry{
		TodoID: "todo_ui_refactor",
		PRURL:  "https://example.com/repo/web/pull/555",
		Policy: PolicyHumanReviewRequired,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Checks red -> wait.
	if dec := tr.EvaluateHead("example.com/repo/web", false); dec.Action != ActionWaitQueue || dec.State != StateActive {
		t.Fatalf("EvaluateHead(false) = %+v, want ActionWaitQueue/StateActive", dec)
	}

	// Checks green WITHOUT approval -> MUST pause at StateAwaitingApproval and NEVER return ActionRebaseAndMerge.
	dec := tr.EvaluateHead("example.com/repo/web", true)
	if dec.Action != ActionAwaitHumanReview || dec.State != StateAwaitingApproval {
		t.Fatalf("PolicyHumanReviewRequired with green checks = %+v, want ActionAwaitHumanReview/StateAwaitingApproval", dec)
	}

	// Repeated evaluation while still unapproved remains paused.
	decAgain := tr.EvaluateHead("example.com/repo/web", true)
	if decAgain.Action != ActionAwaitHumanReview || decAgain.State != StateAwaitingApproval {
		t.Fatalf("repeated EvaluateHead = %+v, want ActionAwaitHumanReview/StateAwaitingApproval", decAgain)
	}

	// Explicit human approval unlocks merge.
	approved, err := tr.Approve("https://example.com/repo/web/pull/555")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.Action != ActionRebaseAndMerge || approved.State != StateActive {
		t.Fatalf("Approve decision = %+v, want ActionRebaseAndMerge/StateActive", approved)
	}

	// Subsequent EvaluateHead(true) proceeds to ActionRebaseAndMerge.
	final := tr.EvaluateHead("example.com/repo/web", true)
	if final.Action != ActionRebaseAndMerge || final.State != StateActive {
		t.Fatalf("post-approval EvaluateHead(true) = %+v, want ActionRebaseAndMerge/StateActive", final)
	}
}

func TestValidationAndEdgeCases(t *testing.T) {
	tr := New()

	if _, err := tr.Enqueue(Entry{TodoID: "", PRURL: "https://example.com/repo/a/pull/1"}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("empty TodoID error = %v, want ErrInvalidEntry", err)
	}
	if _, err := tr.Enqueue(Entry{TodoID: "t1", PRURL: ""}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("empty PR URL error = %v, want ErrInvalidEntry", err)
	}
	if _, err := tr.Enqueue(Entry{TodoID: "t1", PRURL: "https://example.com/repo/a/pull/2", Policy: "yolo"}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid policy error = %v, want ErrInvalidPolicy", err)
	}
	if _, err := tr.Approve("https://example.com/repo/missing/pull/1"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("Approve missing error = %v, want ErrEntryNotFound", err)
	}
	if _, err := tr.RecordRebaseFailure("https://example.com/repo/missing/pull/1", "err"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("RecordRebaseFailure missing error = %v, want ErrEntryNotFound", err)
	}
	if err := tr.RecordRebaseSuccess("https://example.com/repo/missing/pull/1"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("RecordRebaseSuccess missing error = %v, want ErrEntryNotFound", err)
	}

	// Snapshot ordering is deterministic by RepoKey ascending regardless of enqueue order.
	_, _ = tr.Enqueue(Entry{TodoID: "t_z", PRURL: "https://example.com/repo/z/pull/1"})
	_, _ = tr.Enqueue(Entry{TodoID: "t_a", PRURL: "https://example.com/repo/a/pull/3"})
	snap := tr.Snapshot()
	if len(snap.Repos) != 2 || snap.Repos[0].RepoKey != "example.com/repo/a" || snap.Repos[1].RepoKey != "example.com/repo/z" {
		t.Fatalf("Snapshot repo ordering = %+v, want [repo/a, repo/z]", snap.Repos)
	}

	// Mutating returned snapshot must not mutate internal train state.
	snap.Repos[0].Head.TodoID = "mutated"
	eA, ok := tr.Entry("https://example.com/repo/a/pull/3")
	if !ok || eA.TodoID != "t_a" {
		t.Fatalf("internal state mutated via snapshot: %+v", eA)
	}
}

func TestConcurrentAccessRaceSafety(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup
	repos := []string{"example.com/repo/a", "example.com/repo/b", "example.com/repo/c"}

	for rIdx, rk := range repos {
		wg.Add(1)
		go func(repo string, base int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				tid := "todo_" + strconv.Itoa(base*100+i)
				_, _ = tr.Enqueue(Entry{
					TodoID:   tid,
					PRURL:    fmt.Sprintf("https://%s/pull/%d", repo, i+1),
					Priority: i % 3,
				})
				_ = tr.EvaluateHead(repo, i%2 == 0)
				_ = tr.Snapshot()
			}
		}(rk, rIdx)
	}
	wg.Wait()

	snap := tr.Snapshot()
	if len(snap.Repos) != len(repos) {
		t.Fatalf("expected %d repos, got %d", len(repos), len(snap.Repos))
	}
	for _, rs := range snap.Repos {
		if rs.Head == nil || rs.Head.State != StateActive {
			t.Fatalf("repo %s missing active head: %+v", rs.RepoKey, rs)
		}
		if len(rs.Queue) != 14 {
			t.Fatalf("repo %s expected 14 queued entries, got %d", rs.RepoKey, len(rs.Queue))
		}
	}
}
