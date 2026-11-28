package assistant

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A finished, supervised task whose supervisor asked the owner to review or decide only in chat — with no
// needs_user conclusion, user_check ask, ask_user card or pending worker interaction — must not stay silent: after
// the settle time the watch hands it back to its supervisor once, naming the card to open (#12648).
func TestReviewCardlessWaitGoesBackOnce(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s) // until its message is let go below: the watches' claim stays pending to be read
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了，你看下要不要这样发布。")
	if got := todoNow(s, todo.ID).Supervision.State; got != reviewPending {
		t.Fatalf("state = %q, want pending_review", got)
	}
	// Inside the settle window: nothing yet.
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle-time.Minute))
	if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatal("a card-less review was handed back inside the settle window")
	}
	before := len(f.notices())
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("a card-less review was not handed back: %v", pendingKinds(s, todo.ID))
	}
	cur := todoNow(s, todo.ID)
	if cur.Supervision.CardlessToldAt == nil {
		t.Fatal("the hand-back was not kept on the ledger round")
	}
	setSuperviseTimings(s, shortSuperviseTimings) // the message goes out now, at the test's usual pace
	releaseDigest(s)
	waitFor(t, "the card-less message", func() bool { return len(f.notices()) > before })
	n := f.notices()[before]
	for _, want := range []string{"signal=cardless_wait", "没有任何卡片", "needs_user"} {
		if !strings.Contains(n, want) {
			t.Errorf("message lacks %q:\n%s", want, n)
		}
	}
	forgetSignals(t, s)
	// The same review round is never handed back again, however many passes pass.
	s.superviseWatch(ctx, time.Now().Add(2*time.Hour))
	s.superviseWatch(ctx, time.Now().Add(48*time.Hour))
	if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatal("the same review round was handed back twice")
	}
}

// A worker that still holds a card the owner can answer is not card-less.
func TestReviewCardlessWaitSkipsPendingCard(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	// The worker comes back with a question of its own: a real pending interaction stands for the owner.
	agentTurn(s, f, proto.StatusBlocked, "选哪个方案？", 1)
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Hour))
	if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("a task whose worker has a pending card is not card-less: %v", pendingKinds(s, todo.ID))
	}
}

// An open acceptance/conclusion ask already gives the owner something to answer.
func TestReviewCardlessWaitSkipsOpenAsk(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	// In the fake hub the bot card send fails, but the ask is still kept in the acceptance book (as the undecidable
	// tests rely on): the book is what the backstop reads.
	s.askConclusion(ctx, todoNow(s, todo.ID), conclusionInput{Outcome: outcomeDecide, Reason: "要不要发布", Recommend: "发布"})
	if a := askOf(s, todo.ID); a == nil {
		t.Fatal("the conclusion ask was not kept")
	}
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Hour))
	if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("a task with an open ask is not card-less: %v", pendingKinds(s, todo.ID))
	}
}

// A new review round clears the round marker, so a card-less wait in the new round is told again.
func TestReviewCardlessWaitRearmsOnNewRound(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("first round not handed back: %v", pendingKinds(s, todo.ID))
	}
	// A rework leaves pending_review; the next done judgment starts a fresh review round.
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": reviewRework, "note": "补上错误处理"}); err != nil {
		t.Fatal(err)
	}
	if cur := todoNow(s, todo.ID); cur.Supervision.CardlessToldAt != nil || cur.Supervision.State != reviewRework {
		t.Fatalf("the marker was not cleared when the round ended: %+v", cur.Supervision)
	}
	forgetSignals(t, s)
	holdDigest(s)
	waitsForReview(t, s, f, todo.ID, "补上了，再看。")
	cur := todoNow(s, todo.ID)
	if cur.Supervision.State != reviewPending || cur.Supervision.CardlessToldAt != nil {
		t.Fatalf("the new round did not start clean: %+v", cur.Supervision)
	}
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("the new card-less round was not handed back: %v", pendingKinds(s, todo.ID))
	}
}

// The round marker survives a manager restart on a Postgres-backed service, so the same round is not told twice.
func TestReviewCardlessWaitSurvivesRestart(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	quietDigest(s) // until the claim is let go below: the watch's claim stays pending to be read
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("card-less review not handed back: %v", pendingKinds(s, todo.ID))
	}
	setSuperviseTimings(s, shortSuperviseTimings)
	// The digest removes its in-memory batch before the send and its durable ACK.
	// This restart follows delivery. A claim still in Postgres belongs to crash recovery.
	releaseDigest(s)
	waitFor(t, "the cardless hand-back to be delivered and acknowledged", func() bool {
		_, pending := storedSupervisePending(t, s)[todo.ID+"|"+sigCardlessWait]
		return cardlessNotices(f) > 0 && !pending
	})
	stopCardlessManager(s)
	if n := cardlessNotices(f); n != 1 {
		t.Fatalf("the old manager delivered %d card-less hand-backs, want one", n)
	}
	again, nextHub := restartedOn(t, database)
	t.Cleanup(func() { stopCardlessManager(again) })
	quietDigest(again)
	holdDigest(again)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	cur := todoNow(again, todo.ID)
	if cur.Supervision.CardlessToldAt == nil || cur.Supervision.State != reviewPending {
		t.Fatalf("the marker did not survive the restart: %+v", cur.Supervision)
	}
	again.superviseWatch(ctx, time.Now().Add(2*time.Hour))
	if slices.Contains(pendingKinds(again, todo.ID), sigCardlessWait) {
		t.Fatal("a restart reissued the same card-less hand-back")
	}
	flushCardlessClaim(again)
	if n := cardlessNotices(nextHub); n != 0 {
		t.Fatalf("the restarted manager delivered %d duplicate card-less hand-backs", n)
	}
}

// cardlessWorkersStopped judges only the sessions it is given: a worker still working or starting keeps the review
// live, a worker holding a card stands for the owner, and only fully stopped, card-less sessions count as settled.
func TestCardlessWorkersStopped(t *testing.T) {
	stopped := proto.Session{SessionID: "s1", State: "idle"}
	if !cardlessWorkersStopped([]proto.Session{stopped}) {
		t.Error("an idle, card-less worker should count as stopped")
	}
	if cardlessWorkersStopped([]proto.Session{stopped, {SessionID: "s2", State: proto.StateWorking}}) {
		t.Error("a still-working worker should keep the review live")
	}
	if cardlessWorkersStopped([]proto.Session{stopped, {SessionID: "s3", Starting: true}}) {
		t.Error("a starting worker should keep the review live")
	}
	if cardlessWorkersStopped([]proto.Session{stopped, {SessionID: "s4", State: proto.StateNeedsYou}}) {
		t.Error("a worker holding an owner card means the review is not card-less")
	}
	if cardlessWorkersStopped([]proto.Session{stopped, {SessionID: "s5", PendingInteractions: 1}}) {
		t.Error("a worker with a pending interaction means the review is not card-less")
	}
}

func TestOwnerWaitCardlessSupervisionGoesBackOnce(t *testing.T) {
	for _, state := range []string{"", reviewRework} {
		t.Run(cmp.Or(state, "going_on"), func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			quietDigest(s)
			holdDigest(s)
			ctx := context.Background()
			stopOnOwner(t, s, f, todo.ID)
			if state != "" {
				if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
					v.State = state
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			wait := todoNow(s, todo.ID).Supervision.Waits
			s.superviseWatch(ctx, wait.At.Add(cardlessSettle-time.Minute))
			if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
				t.Fatal("an owner wait was handed back inside the settle window")
			}
			before := cardlessNotices(f)
			s.superviseWatch(ctx, wait.At.Add(cardlessSettle+time.Minute))
			g := pendingCardlessSignal(t, s, todo.ID)
			if !strings.HasPrefix(g.Key, "cardless-wait@") {
				t.Fatalf("owner-wait claim key = %q, want cardless-wait@", g.Key)
			}
			cur := todoNow(s, todo.ID).Supervision
			if cur.Waits == nil || cur.Waits.CardlessToldAt == nil {
				t.Fatalf("owner wait was not stamped: %+v", cur.Waits)
			}
			if cur.CardlessToldAt != nil {
				t.Fatal("owner-wait supervision changed the pending-review round marker")
			}
			setSuperviseTimings(s, shortSuperviseTimings)
			releaseDigest(s)
			waitFor(t, "the owner-wait card-less message", func() bool { return cardlessNotices(f) > before })
			var notice string
			for _, n := range f.notices() {
				if strings.Contains(n, "signal=cardless_wait") {
					notice = n
				}
			}
			if !strings.Contains(notice, "两版草稿等用户挑一版再发") {
				t.Fatalf("the delivered notice lacks the owner-wait reason:\n%s", notice)
			}
			forgetSignals(t, s)
			s.superviseWatch(ctx, wait.At.Add(48*time.Hour))
			if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
				t.Fatal("the same owner wait was handed back twice")
			}
		})
	}
}

func TestOwnerWaitCloneCopiesCardlessStampAndNotifiedAt(t *testing.T) {
	notified, told := time.Now().UTC(), time.Now().Add(time.Minute).UTC()
	wantNotified, wantTold := notified, told
	origNotified, origTold := notified, told
	v := &Supervision{Waits: &OwnerWait{NotifiedAt: &origNotified, CardlessToldAt: &origTold}}
	clone := v.clone()
	if clone.Waits.NotifiedAt == v.Waits.NotifiedAt || clone.Waits.CardlessToldAt == v.Waits.CardlessToldAt {
		t.Fatal("cloning an owner wait aliased one of its timestamp pointers")
	}
	*clone.Waits.NotifiedAt = clone.Waits.NotifiedAt.Add(time.Hour)
	*clone.Waits.CardlessToldAt = clone.Waits.CardlessToldAt.Add(time.Hour)
	if !v.Waits.NotifiedAt.Equal(wantNotified) || !v.Waits.CardlessToldAt.Equal(wantTold) {
		t.Fatal("cloning an owner wait shared one of its timestamps")
	}
}

func TestOwnerWaitCardlessSupervisionGuards(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(context.Context, *Service, *fakeHub, Todo)
		at   func(Todo) time.Time
	}{
		{name: "inside_settle", at: func(t Todo) time.Time { return t.Supervision.Waits.At.Add(cardlessSettle - time.Minute) }},
		{name: "needs_user", at: pastOwnerWait, edit: func(ctx context.Context, s *Service, _ *fakeHub, todo Todo) {
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
				t.Error(err)
			}
		}},
		{name: "parked", at: pastOwnerWait, edit: func(ctx context.Context, s *Service, _ *fakeHub, todo Todo) {
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
				v.State = reviewParked
				v.Reviews = append(v.Reviews, TaskReview{Verdict: reviewParked, At: time.Now().UTC(), Until: time.Now().Add(time.Hour).UTC()})
				return true
			}); err != nil {
				t.Error(err)
			}
		}},
		{name: "open_ask", at: pastOwnerWait, edit: func(ctx context.Context, s *Service, _ *fakeHub, todo Todo) {
			s.askConclusion(ctx, todoNow(s, todo.ID), conclusionInput{Outcome: outcomeDecide, Reason: "要不要发布", Recommend: "发布"})
		}},
		{name: "worker_working", at: pastOwnerWait, edit: func(_ context.Context, _ *Service, f *fakeHub, _ Todo) {
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusWorking, State: proto.StateWorking})
		}},
		{name: "worker_needs_user", at: pastOwnerWait, edit: func(_ context.Context, _ *Service, f *fakeHub, _ Todo) {
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", State: proto.StateNeedsYou, PendingInteractions: 1})
		}},
		{name: "worker_unreachable", at: pastOwnerWait, edit: func(_ context.Context, _ *Service, f *fakeHub, _ Todo) {
			f.mu.Lock()
			f.sessionsFail["box"] = 100
			f.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			quietDigest(s)
			holdDigest(s)
			stopOnOwner(t, s, f, todo.ID)
			if tc.edit != nil {
				tc.edit(context.Background(), s, f, todoNow(s, todo.ID))
			}
			s.superviseWatch(context.Background(), tc.at(todoNow(s, todo.ID)))
			if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
				t.Fatal("a guarded owner wait was handed back")
			}
			if w := todoNow(s, todo.ID).Supervision.Waits; w != nil && w.CardlessToldAt != nil {
				t.Fatalf("a guarded owner wait was stamped: %+v", w)
			}
		})
	}
}

func pastOwnerWait(t Todo) time.Time {
	return t.Supervision.Waits.At.Add(cardlessSettle + time.Minute)
}

func TestOwnerWaitCardlessSupervisionWaitsForStop(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	stopOnOwner(t, s, f, todo.ID)
	s.markReporter(ctx, "box", todo.Origin)
	s.setReporterStopped(ctx, todo.Origin, "app")
	wait := todoNow(s, todo.ID).Supervision.Waits
	s.superviseWatch(ctx, wait.At.Add(cardlessSettle+time.Minute))
	if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) || todoNow(s, todo.ID).Supervision.Waits.CardlessToldAt != nil {
		t.Fatal("an owner wait was handed back while its conversation was stopped")
	}
	s.setReporterStopped(ctx, todo.Origin, "")
	s.superviseWatch(ctx, wait.At.Add(cardlessSettle+2*time.Minute))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("the owner wait was not handed back after /stop was lifted: %v", pendingKinds(s, todo.ID))
	}
	if todoNow(s, todo.ID).Supervision.Waits.CardlessToldAt == nil {
		t.Fatal("the recovered owner wait was not stamped")
	}
}

func TestOwnerWaitCardlessDigestDropsStaleClaim(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(context.Context, *Service, *fakeHub, Todo)
	}{
		{name: "worker_resumes", edit: func(_ context.Context, _ *Service, f *fakeHub, _ Todo) {
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusWorking, State: proto.StateWorking})
		}},
		{name: "open_ask", edit: func(ctx context.Context, s *Service, _ *fakeHub, todo Todo) {
			s.askConclusion(ctx, todoNow(s, todo.ID), conclusionInput{Outcome: outcomeDecide, Reason: "要不要发布", Recommend: "发布"})
		}},
		{name: "wait_cleared", edit: func(ctx context.Context, s *Service, _ *fakeHub, todo Todo) {
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Waits = nil; return true }); err != nil {
				t.Error(err)
			}
		}},
		{name: "reviewed", edit: func(ctx context.Context, s *Service, _ *fakeHub, todo Todo) {
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
				t.Error(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			quietDigest(s)
			holdDigest(s)
			ctx := context.Background()
			stopOnOwner(t, s, f, todo.ID)
			wait := todoNow(s, todo.ID).Supervision.Waits
			s.superviseWatch(ctx, wait.At.Add(cardlessSettle+time.Minute))
			g := pendingCardlessSignal(t, s, todo.ID)
			if _, ok := s.digestEntry(ctx, g, ""); !ok {
				t.Fatal("the owner-wait entry did not render while the claim held")
			}
			tc.edit(ctx, s, f, todoNow(s, todo.ID))
			if _, ok := s.digestEntry(ctx, g, ""); ok {
				t.Fatal("a stale owner-wait entry rendered")
			}
		})
	}
}

// pendingCardlessSignal returns the queued card-less signal for id, failing the test if it is not there.
// quietDigest keeps the supervision digest from running in a test that reads what is pending and needs no message to
// go out. The card-less claim is immediate, so holdDigest does not keep it in, and a digest takes what it delivers out
// of the pending signals (#13877).
func quietDigest(s *Service) {
	setSuperviseTimings(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
}

// setSuperviseTimings changes s's supervision timings under the locks every reader of them holds, digestMu then b.mu
// in sendDigest's order, so a digest or timer reading them meanwhile does not race the write.
func setSuperviseTimings(s *Service, p superviseTimings) {
	b := &s.supervise
	b.digestMu.Lock()
	defer b.digestMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.superviseTimes = p
}

// rearmDigest puts s back on the short timings and arms the digest timer for what is pending, as queueing an
// immediate signal does, under the same locks as setSuperviseTimings: the real timer runs again from here.
func rearmDigest(s *Service) {
	b := &s.supervise
	b.digestMu.Lock()
	defer b.digestMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.superviseTimes = shortSuperviseTimings
	s.scheduleDigestLocked(time.Now())
}

// pendingCardlessSignal reads the pending claim. On a service with a store it reads the outbox (the relay state's
// supervise_pending, which is what is persisted), as supervisePendingDurably does: a digest takes what it delivers out
// of the in-memory pending signals until it ends, while the outbox keeps the claim until its delivery is acknowledged,
// and the claim's digest timer stays on in some of these tests (#13877). A service with no store has no outbox; its
// tests here keep the digest quiet (quietDigest), so what is pending in memory is read.
func pendingCardlessSignal(t *testing.T, s *Service, id string) superviseSignal {
	t.Helper()
	key := id + "|" + sigCardlessWait
	var g superviseSignal
	var ok bool
	if s.store() != nil {
		r := &s.relays
		r.mu.Lock()
		if s.relayLocked(context.Background()) {
			g, ok = r.st.SupervisePending[key]
		}
		r.mu.Unlock()
	} else {
		b := &s.supervise
		b.mu.Lock()
		g, ok = b.pending[key]
		b.mu.Unlock()
	}
	if !ok {
		t.Fatalf("no pending cardless signal for %s", id)
	}
	return g
}

// A machine whose session list fails is not a stopped worker: the hand-back must wait for a reachable pass rather
// than spend the round's one notification (and its CardlessToldAt stamp) on an unknown state (#12648 P1).
func TestReviewCardlessWaitWaitsForAReachableWorker(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	f.sessionsFail["box"] = 100
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Hour))
	if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatal("the hand-back fired while the worker's machine was unreachable")
	}
	if todoNow(s, todo.ID).Supervision.CardlessToldAt != nil {
		t.Fatal("the round marker was stamped on an unreachable worker")
	}
	// Once the machine answers again, the same review round is handed back normally.
	f.sessionsFail["box"] = 0
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+2*time.Hour))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("the hand-back did not fire after the machine recovered: %v", pendingKinds(s, todo.ID))
	}
}

// A digest entry whose conditions changed while it queued is dropped: if an owner card (acceptance ask) appears after
// the marker was stamped, the stale "no card" entry must not render (#12648 P1).
func TestCardlessDigestDropsWhenACardAppears(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	g := pendingCardlessSignal(t, s, todo.ID)
	if _, ok := s.digestEntry(ctx, g, ""); !ok {
		t.Fatal("the card-less entry did not render while still card-less")
	}
	// An owner-answerable ask now stands; the same queued entry must be suppressed on the next render.
	s.askConclusion(ctx, todoNow(s, todo.ID), conclusionInput{Outcome: outcomeDecide, Reason: "要不要发布", Recommend: "发布"})
	if askOf(s, todo.ID) == nil {
		t.Fatal("the conclusion ask was not kept")
	}
	if _, ok := s.digestEntry(ctx, g, ""); ok {
		t.Fatal("the stale card-less entry rendered after an owner card appeared")
	}
}

// A worker that goes back to work after the marker is stamped also invalidates the queued card-less entry. The
// session list is set directly so the ledger still reads pending_review (the snapshot window before
// supervisedWorking moves it): this exercises the digest's own worker revalidation rather than the state hold.
func TestCardlessDigestDropsWhenAWorkerResumes(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	quietDigest(s)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	g := pendingCardlessSignal(t, s, todo.ID)
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	if cur := todoNow(s, todo.ID).Supervision; cur.state(todoNow(s, todo.ID)) != reviewPending {
		t.Fatalf("the direct session edit must leave the ledger in pending_review: %+v", cur)
	}
	if _, ok := s.digestEntry(ctx, g, ""); ok {
		t.Fatal("the stale card-less entry rendered after its worker resumed")
	}
}

// A task leader's supervision message is rendered from worker_supervise.md, not the assistant supervise.md template:
// the cardless_wait entry must carry an explanation and the required review action there too, instead of rendering as
// an unexplained numbered line (#12648 P2).
func TestCardlessWaitRenderedForTaskLeader(t *testing.T) {
	e := prompts.SuperviseEntry{ID: "todo_1", Title: "Fix the build", Kind: sigCardlessWait, Minutes: 12}
	got := prompts.RenderWorkerSupervise([]prompts.SuperviseEntry{e})
	for _, want := range []string{"12 minutes for your review", "no card anyone can answer", "review it now", "Fix the build"} {
		if !strings.Contains(got, want) {
			t.Errorf("worker cardless_wait render lacks %q:\n%s", want, got)
		}
	}
}

// A card-less review that comes due while its conversation is stopped (/stop, #3308) is not stamped as told, since
// signal drops it then: once the user speaks again it is handed back (#13415).
func TestReviewCardlessWaitUnderStopWaitsForTheUser(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了，你看下要不要这样发布。")
	s.markReporter(ctx, "box", todo.Origin)
	s.setReporterStopped(ctx, todo.Origin, "app")
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	if todoNow(s, todo.ID).Supervision.CardlessToldAt != nil {
		t.Fatal("the round was stamped told while stopped, though nothing was queued")
	}
	s.setReporterStopped(ctx, todo.Origin, "")
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+2*time.Minute))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("not handed back once /stop was lifted: %v", pendingKinds(s, todo.ID))
	}
}

// A transient acceptance-book read failure is not proof that no owner card exists. The watch must not stamp the round
// or raise the hand-back while the ledger is unreadable, and a later pass after the book recovers still hands it back
// in the same review round (#12648 P1).
func TestReviewCardlessWaitHoldsWhenAcceptBookUnreadable(t *testing.T) {
	database := taskRecoveryDatabase(t)
	st := &acceptReadFailStore{Store: repositorystore.New(database)}
	s, f, todo := newSupervisedServiceOn(t, st)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	// Force the next acceptance-book read to fail rather than report an empty map.
	st.fail = true
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Hour))
	if slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatal("the hand-back fired while the acceptance book was unreadable")
	}
	if todoNow(s, todo.ID).Supervision.CardlessToldAt != nil {
		t.Fatal("the round marker was stamped while the acceptance book was unreadable")
	}
	// Once the book reads again, the same review round is handed back normally.
	st.fail = false
	s.accept.loaded = false
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+2*time.Hour))
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatalf("the hand-back did not fire after the acceptance book recovered: %v", pendingKinds(s, todo.ID))
	}
}

// A card-less entry waiting in a queued digest must be restored for retry, not rendered or dropped, when the
// acceptance book becomes unreadable at delivery time; once it reads again the entry renders normally (#12648 P1).
func TestCardlessDigestRetriesWhenAcceptBookUnreadable(t *testing.T) {
	database := taskRecoveryDatabase(t)
	st := &acceptReadFailStore{Store: repositorystore.New(database)}
	s, f, todo := newSupervisedServiceOn(t, st)
	holdDigest(s)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了。")
	s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
	g := pendingCardlessSignal(t, s, todo.ID)
	if _, ok := s.digestEntry(ctx, g, ""); !ok {
		t.Fatal("the card-less entry did not render while the book was readable and card-less")
	}
	st.fail = true
	s.accept.loaded = false
	if !s.cardlessDigestShouldRetry(g) {
		t.Fatal("an unreadable acceptance book should mark the card-less entry for delivery retry")
	}
	if _, ok, deferRetry := s.digestEntryChecked(ctx, g, ""); ok || !deferRetry {
		t.Fatalf("unreadable book: got ok=%v deferRetry=%v, want ok=false deferRetry=true", ok, deferRetry)
	}
	st.fail = false
	s.accept.loaded = false
	if s.cardlessDigestShouldRetry(g) {
		t.Fatal("a readable acceptance book must not mark the entry for retry")
	}
	if _, ok := s.digestEntry(ctx, g, ""); !ok {
		t.Fatal("the card-less entry did not render after the acceptance book recovered")
	}
}

// acceptReadFailStore wraps a real store but fails reads of the acceptance book while fail is set, letting a test
// inject a transient ledger outage without losing the task_plan / supervision tables every other read needs.
type acceptReadFailStore struct {
	Store
	fail bool
}

func (s *acceptReadFailStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if s.fail && key == acceptanceKey {
		return false, errors.New("synthetic acceptance store outage")
	}
	return s.Store.Setting(ctx, key, out)
}
