package lark

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/rpc"
)

func snapshotRun(s *Service, r *channel.Run) channel.Run {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	snap := *r
	snap.Ext = cloneExt(r.Ext)
	return snap
}

type sendAsHookHost struct {
	Host
	onSendAs func(ctx context.Context, kind, machine, session string) context.Context
}

func (h *sendAsHookHost) SendAs(ctx context.Context, kind, machine, session string) context.Context {
	if h.onSendAs != nil {
		return h.onSendAs(ctx, kind, machine, session)
	}
	return h.Host.SendAs(ctx, kind, machine, session)
}

func TestFeishuUnifiedTimeoutDuringBlockedProgressDoesNotDuplicateAnswer(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	s.setLarkTiming("cot_answer_wait", time.Nanosecond)
	r, prog := cotRun(s)

	entered, release := make(chan struct{}), make(chan struct{})
	var blockOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	fhub := s.hub.(*fakeHub)
	fhub.answerAppBlocking(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) {
		blocked := false
		blockOnce.Do(func() {
			blocked = true
			close(entered)
		})
		if blocked {
			<-release
		}
		return map[string]any{}, nil
	})

	// Queue a progress tick so the worker creates the agent message and blocks inside its first append.
	s.sink().progress(t.Context(), r, time.Second, []string{"运行 `ls`"}, "")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("progress append did not start")
	}

	// Finish the turn while the progress append is blocked. cot_answer_wait expires immediately so answer()
	// falls back to posting the answer card.
	snap := snapshotRun(s, r)
	if err := s.sink().answer(t.Context(), &snap, "结论在这里", "", time.Second, true, false, channel.EndDone); err != nil {
		t.Fatal(err)
	}
	postedAnswer(t, *posts, "结论在这里")

	// Let the blocked progress append return so the worker advances to its terminal iteration and exits.
	unblock()
	s.chans.mu.Lock()
	w := prog.native()
	s.chans.mu.Unlock()
	if w == nil {
		t.Fatal("expected native cotWorker")
	}
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cotWorker did not exit after blocked progress released")
	}

	types, body := appendedBodies(s)
	if strings.Contains(strings.Join(types, " "), "TEXT_MESSAGE") || strings.Contains(body, "结论在这里") {
		t.Fatalf("worker posted duplicate answer inside agent message after card fallback: types=%v body=%q", types, body)
	}
	if s.runExt(r, "cot_answer") != "" {
		t.Fatalf("cot_answer persisted after card fallback: %q", s.runExt(r, "cot_answer"))
	}
	if !strings.HasSuffix(strings.Join(types, " "), "RUN_FINISHED") && len(completeReasons(s)) == 0 {
		t.Fatalf("agent message was not closed: types=%v completes=%v", types, completeReasons(s))
	}
}

func TestFeishuUnifiedTimeoutBeforeCreateDoesNotSpawnOrphanAgentMessage(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	s.setLarkTiming("cot_answer_wait", time.Nanosecond)
	r, prog := cotRun(s)

	entered, release := make(chan struct{}), make(chan struct{})
	var blockOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	// Block the progress tick's FeishuCotCreate with a genuinely unsent retryable error so cotStart does not
	// set cot_off="1"; this ensures the !hasOpenCot guard itself prevents creating an orphan agent message.
	fhub := s.hub.(*fakeHub)
	fhub.answerAppBlocking(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) {
		blocked := false
		blockOnce.Do(func() {
			blocked = true
			close(entered)
		})
		if blocked {
			<-release
			return nil, rpc.ErrNotSent
		}
		return map[string]any{"cot_id": "cot2", "message_id": "om_cot2"}, nil
	})

	s.sink().progress(t.Context(), r, time.Second, []string{"运行 `ls`"}, "")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("progress create did not start")
	}

	snap := snapshotRun(s, r)
	if err := s.sink().answer(t.Context(), &snap, "结论在这里", "", time.Second, true, false, channel.EndDone); err != nil {
		t.Fatal(err)
	}
	postedAnswer(t, *posts, "结论在这里")

	unblock()
	s.chans.mu.Lock()
	w := prog.native()
	s.chans.mu.Unlock()
	if w == nil {
		t.Fatal("expected native cotWorker")
	}
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cotWorker did not exit")
	}

	if off := s.runExt(r, "cot_off"); off != "" {
		t.Fatalf("expected cot_off to stay empty after unsent create error, got %q", off)
	}
	// Only the initial canceled progress create should have been attempted; the terminal pass must not create
	// an orphan agent message after the card fallback already carried the process and answer.
	if creates := len(fhub.appParams(AppFeishu, FeishuCotCreate)); creates > 1 {
		t.Fatalf("orphan agent message created after card fallback: creates=%d", creates)
	}
	types, body := appendedBodies(s)
	if len(types) != 0 || strings.Contains(body, "结论在这里") {
		t.Fatalf("unexpected agent message appends after card fallback: types=%v body=%q", types, body)
	}

	// A completed worker's result() must still return its outcome even with an already-canceled context.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := w.result(canceled, 0); !ok {
		t.Fatal("result() on completed worker with canceled ctx returned ok=false")
	}
}

func TestFeishuUnifiedTimeoutDuringSaveRunsAfterAnswerAckDoesNotPostCard(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	s.setLarkTiming("cot_answer_wait", 5*time.Second)
	r, prog := cotRun(s)

	progressAppended := make(chan struct{})
	var progressOnce sync.Once
	fhub := s.hub.(*fakeHub)
	fhub.answerApp(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) {
		progressOnce.Do(func() { close(progressAppended) })
		return map[string]any{}, nil
	})

	// Block SaveRuns right after Feishu acknowledges CotFinishWithAnswer and persists cot_answer="1".
	saveEntered, releaseSave := make(chan struct{}), make(chan struct{})
	var saveOnce, unblockSaveOnce sync.Once
	unblockSave := func() { unblockSaveOnce.Do(func() { close(releaseSave) }) }
	defer unblockSave()
	s.save = func(runs []*channel.Run) error {
		for _, run := range runs {
			if run.ID == r.ID && run.Ext["cot_answer"] != "" {
				blocked := false
				saveOnce.Do(func() {
					blocked = true
					close(saveEntered)
				})
				if blocked {
					<-releaseSave
				}
			}
		}
		return nil
	}

	s.sink().progress(t.Context(), r, time.Second, []string{"运行 `ls`"}, "")
	select {
	case <-progressAppended:
	case <-time.After(2 * time.Second):
		t.Fatal("progress append did not complete")
	}
	s.chans.mu.Lock()
	w := prog.native()
	s.chans.mu.Unlock()
	if w == nil {
		t.Fatal("expected native cotWorker")
	}

	ansCtx, cancelAns := context.WithCancel(t.Context())
	defer cancelAns()
	snap := snapshotRun(s, r)
	answerErr := make(chan error, 1)
	go func() {
		answerErr <- s.sink().answer(ansCtx, &snap, "结论在这里", "", time.Second, true, false, channel.EndDone)
	}()

	select {
	case <-saveEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("SaveRuns did not block after cot_answer persisted")
	}

	// Timer expiration while SaveRuns is still blocked must recognize cot_answer="1" as answered.
	if out, ok := w.result(t.Context(), time.Nanosecond); !ok || !out.native || !out.answered {
		t.Fatalf("result() during blocked SaveRuns = (%+v, %v), want ({true true}, true)", out, ok)
	}

	// Canceling answer()'s result wait while SaveRuns is still blocked must not post a duplicate fallback card.
	cancelAns()
	select {
	case err := <-answerErr:
		if err != nil {
			t.Fatalf("answer() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer() did not return after cancel during SaveRuns")
	}
	if len(*posts) != 0 {
		t.Fatalf("duplicate fallback card posted after native answer acknowledged: posts=%v", *posts)
	}
	select {
	case <-w.done:
		t.Fatal("expected cotWorker to still be blocked in SaveRuns")
	default:
	}

	unblockSave()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cotWorker did not exit after SaveRuns unblocked")
	}
	if len(*posts) != 0 {
		t.Fatalf("unexpected fallback card posted: posts=%v", *posts)
	}
	if s.runExt(r, "cot_answer") != "1" || s.runExt(r, "cot_done") != "1" {
		t.Fatalf("expected cot_answer=1 and cot_done=1, got answer=%q done=%q", s.runExt(r, "cot_answer"), s.runExt(r, "cot_done"))
	}
	_, body := appendedBodies(s)
	if !strings.Contains(body, "结论在这里") {
		t.Fatalf("expected unified answer inside agent message, got body=%q", body)
	}

	// Also verify w.finished prevents a duplicate fallback when result() times out before close(w.done).
	finWorker := &cotWorker{
		done:     make(chan struct{}),
		finished: true,
		outcome:  cotOutcome{native: true, answered: true},
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if out, ok := finWorker.result(canceled, time.Nanosecond); !ok || !out.native || !out.answered {
		t.Fatalf("result() with finished=true before close(done) = (%+v, %v), want ({true true}, true)", out, ok)
	}
}

func TestFeishuUnifiedTimeoutCancelBeforeFinishStillClosesOpenAgentMessage(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	enteredTerminal, releaseTerminal := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var enterOnce, releaseOnce sync.Once
	unblockTerminal := func() { releaseOnce.Do(func() { close(releaseTerminal) }) }
	defer unblockTerminal()

	hook := &sendAsHookHost{
		Host: s,
		onSendAs: func(ctx context.Context, kind, machine, session string) context.Context {
			if kind == "progress" && armed.CompareAndSwap(true, false) {
				enterOnce.Do(func() { close(enteredTerminal) })
				<-releaseTerminal
			}
			return s.SendAs(ctx, kind, machine, session)
		},
	}
	s.layer = New(hook)
	s.layer.turns.board.now = s.clock

	cotFeatures(s, "dm_cot", "cot_answer")
	s.setLarkTiming("cot_answer_wait", 5*time.Second)
	r, prog := cotRun(s)

	progressAppended := make(chan struct{})
	var progressOnce sync.Once
	fhub := s.hub.(*fakeHub)
	fhub.answerApp(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) {
		progressOnce.Do(func() { close(progressAppended) })
		return map[string]any{}, nil
	})

	// Open the agent message in a normal progress tick.
	s.sink().progress(t.Context(), r, time.Second, []string{"运行 `ls`"}, "")
	select {
	case <-progressAppended:
	case <-time.After(2 * time.Second):
		t.Fatal("progress append did not complete")
	}
	s.chans.mu.Lock()
	w := prog.native()
	s.chans.mu.Unlock()
	if w == nil {
		t.Fatal("expected native cotWorker")
	}

	// Pause the worker right after it picks up the terminal snapshot (after w.mu.Unlock, before cotFinishNowItems).
	armed.Store(true)
	ansCtx, cancelAns := context.WithCancel(t.Context())
	defer cancelAns()
	snap := snapshotRun(s, r)
	answerErr := make(chan error, 1)
	go func() {
		answerErr <- s.sink().answer(ansCtx, &snap, "结论在这里", "", time.Second, true, false, channel.EndDone)
	}()

	select {
	case <-enteredTerminal:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach terminal SendAs")
	}

	// Cancel result() while the worker is paused before cotFinishNowItems; answer() cancels w.cancel() and posts
	// the fallback card.
	cancelAns()
	select {
	case err := <-answerErr:
		if err != nil {
			t.Fatalf("answer() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer() did not return after cancel")
	}
	postedAnswer(t, *posts, "结论在这里")

	// Resume the worker into cotFinishNowItems with a canceled ctx; it must still close the open agent message
	// via cotEnd without duplicating the answer text.
	unblockTerminal()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cotWorker did not exit after terminal resume")
	}

	reasons := completeReasons(s)
	if len(reasons) != 1 || reasons[0] != string(cotReasonDone) {
		t.Fatalf("expected open agent message to be closed via cot_complete(done), got completes=%v", reasons)
	}
	if s.runExt(r, "cot_done") != "1" {
		t.Fatalf("expected cot_done=1 after cotEnd, got %q", s.runExt(r, "cot_done"))
	}
	if s.runExt(r, "cot_answer") != "" {
		t.Fatalf("cot_answer persisted after card fallback: %q", s.runExt(r, "cot_answer"))
	}
	types, body := appendedBodies(s)
	if strings.Contains(strings.Join(types, " "), "TEXT_MESSAGE") || strings.Contains(body, "结论在这里") {
		t.Fatalf("worker duplicated answer after canceled finish: types=%v body=%q", types, body)
	}
}
