package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The interactions as the manager keeps them (fleet_ix.go), moved from the hub's tests with them.

func TestResolveRequiresDecisionForInteractionKind(t *testing.T) {
	for _, tc := range []struct {
		name, kind, invalid, valid string
	}{
		{"approve on permission", proto.IxPermission, `{"approve":true}`, `{"allow":true}`},
		{"allow on plan", proto.IxPlan, `{"allow":true}`, `{"approve":true}`},
		{"empty permission decision", proto.IxPermission, `{}`, `{"allow":false}`},
		{"null plan decision", proto.IxPlan, `{"approve":null}`, `{"approve":false}`},
		{"cancel is only for questions", proto.IxPermission, `{"cancel":true}`, `{"dismiss":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			ix := proto.Interaction{ID: "ix_decision", MachineID: "mac", SessionID: "s1", Kind: tc.kind, OutwardWrite: tc.kind == proto.IxPermission}
			announced := make(chan struct{})
			done := make(chan proto.Resolution, 1)
			go func() {
				r, _ := ft.s.waitInteractionReady(t.Context(), ix, nil, announced)
				done <- r
			}()
			select {
			case <-announced:
			case <-time.After(5 * time.Second):
				t.Fatal("interaction was not announced")
			}
			h := ft.s.fleetHandler(http.NotFoundHandler())
			post := func(body string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+ix.ID+"/resolve", strings.NewReader(body)))
				return rec
			}
			if rec := post(tc.invalid); rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid decision: HTTP %d, body %s", rec.Code, rec.Body)
			}
			if _, ok := ft.s.pendingIx(ix.ID); !ok {
				t.Fatal("invalid decision removed the pending card")
			}
			if status, answer, err := ft.st.InteractionOutcome(t.Context(), ix.ID); err != nil || status != "pending" || answer != nil {
				t.Fatalf("invalid decision changed the stored card: %s %+v %v", status, answer, err)
			}
			if rec := post(tc.valid); rec.Code != http.StatusOK {
				t.Fatalf("valid decision: HTTP %d, body %s", rec.Code, rec.Body)
			}
			select {
			case answer := <-done:
				raw, err := json.Marshal(answer)
				if err != nil || string(raw) != tc.valid {
					t.Fatalf("hook got %s, want %s (%v)", raw, tc.valid, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("valid decision did not release the waiter")
			}
		})
	}
}

func TestWaitInteractionResolved(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ix := proto.Interaction{ID: "ix_1", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, Summary: "send mail"}
	announced := make(chan struct{})
	done := make(chan proto.Resolution, 1)
	go func() {
		r, err := s.waitInteractionReady(t.Context(), ix, nil, announced)
		if err != nil {
			t.Errorf("waitInteraction: %v", err)
		}
		done <- r
	}()
	// The phone answers an announced card. A pending entry alone can still be on its way to the Hub.
	select {
	case <-announced:
	case <-time.After(5 * time.Second):
		t.Fatal("interaction was not announced")
	}
	if got := s.pendingInteractions(); len(got) != 1 || got[0].ID != ix.ID {
		t.Fatalf("pending after announcement = %+v, want %s", got, ix.ID)
	}
	if !ft.hubCards()[ix.ID] || len(ft.toldOf(assistantwire.EventFleetResolved)) != 0 {
		t.Fatalf("expected the Hub to hold %s before answering, told %s", ix.ID, ft.toldText())
	}
	allow := true
	if err := s.resolve("ix_1", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	select {
	case r := <-done:
		if r.Allow == nil || !*r.Allow {
			t.Fatalf("resolution = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitInteraction did not return")
	}
	ft.waitPending(0)
	if err := s.resolve("ix_1", proto.Resolution{}); err == nil || !strings.Contains(err.Error(), "no longer pending") {
		t.Fatalf("a second resolve should fail, got %v", err)
	}
	// The hub was told: the card, then its end.
	if len(ft.toldOf(assistantwire.EventFleetInteraction)) != 1 || len(ft.toldOf(assistantwire.EventFleetResolved)) != 1 || ft.hubCards()[ix.ID] {
		t.Fatalf("told %s", ft.toldText())
	}
}

func TestWaitInteractionExpiresAsDismiss(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ix := proto.Interaction{ID: "ix_exp", MachineID: "mac", Kind: proto.IxQuestion, ExpiresAt: time.Now().Add(50 * time.Millisecond)}
	r, err := ft.s.waitInteraction(context.Background(), ix)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Dismiss {
		t.Fatalf("an expired interaction must dismiss, got %+v", r)
	}
	ft.waitPending(0)
}

// The default expiry is manager.json's hook_timeout_minutes (hello's notify).
func TestWaitInteractionDefaultExpiry(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.f.hello.Notify.HookTimeoutMS = (45 * time.Minute).Milliseconds()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = ft.s.waitInteraction(ctx, proto.Interaction{ID: "ix_def", MachineID: "mac", Kind: proto.IxPlan})
	}()
	got := ft.waitPending(1)[0]
	want := time.Now().Add(45 * time.Minute)
	if diff := got.ExpiresAt.Sub(want); diff > time.Minute || diff < -time.Minute {
		t.Fatalf("default expiry = %v, want about %v", got.ExpiresAt, want)
	}
}

func TestWaitInteractionCancelledDismisses(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan proto.Resolution, 1)
	go func() {
		r, _ := ft.s.waitInteraction(ctx, proto.Interaction{ID: "ix_ctx", MachineID: "mac", Kind: proto.IxPermission, OutwardWrite: true,
			ExpiresAt: time.Now().Add(time.Hour)})
		done <- r
	}()
	ft.waitPending(1)
	cancel()
	select {
	case r := <-done:
		if !r.Dismiss {
			t.Fatalf("cancelled interaction should dismiss, got %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not unblock waitInteraction")
	}
}

func TestWaitInteractionReplacesScreenGuess(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	var ended []string
	s.fleetEnded = func(ix proto.Interaction, _ *proto.Resolution) { ended = append(ended, ix.ID) }
	// A screen interaction (a guess from a blocked agent) is already pending for the session.
	scr := proto.Interaction{ID: "scr_mac_s1", MachineID: "mac", SessionID: "s1", Kind: proto.IxScreen, ExpiresAt: time.Now().Add(time.Hour)}
	s.record(scr)
	s.fleet.mu.Lock()
	s.ixMapLocked()[scr.ID] = &pending{ix: scr, done: make(chan proto.Resolution, 1)}
	s.fleet.mu.Unlock()
	ix := proto.Interaction{ID: "ix_hook", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, ExpiresAt: time.Now().Add(time.Hour)}
	announced := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.waitInteractionReady(t.Context(), ix, nil, announced)
	}()
	select {
	case <-announced:
	case <-time.After(5 * time.Second):
		t.Fatal("hook interaction was not announced")
	}
	if got := ids(s.pendingInteractions()); !slices.Equal(got, []string{ix.ID}) {
		t.Fatalf("pending interactions: %v", got)
	}
	if st, _, _ := ft.st.InteractionOutcome(context.Background(), scr.ID); st != "cancelled" {
		t.Fatalf("screen guess status %q, want cancelled", st)
	}
	resolved := false
	for _, b := range ft.toldOf(assistantwire.EventFleetResolved) {
		resolved = resolved || strings.Contains(string(b), scr.ID)
	}
	if !resolved {
		t.Fatal("the hub never heard the screen guess was withdrawn")
	}
	found := false
	for _, id := range ended {
		found = found || id == scr.ID
	}
	if !found {
		t.Fatal("the screen guess was never said to be over (the bot's card for it stays open)")
	}
	if err := s.resolve(ix.ID, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dismiss did not release the hook")
	}
}

func TestScreenInteractionResolveRunsLocally(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ran := make(chan proto.Resolution, 1)
	s.fleet.mu.Lock()
	s.ixMapLocked()["scr_x"] = &pending{
		ix:    proto.Interaction{ID: "scr_x", MachineID: "mac", SessionID: "s1", Kind: proto.IxScreen},
		local: func(_ context.Context, r proto.Resolution) error { ran <- r; return nil },
	}
	s.fleet.mu.Unlock()
	if err := s.resolve("scr_x", proto.Resolution{Keys: []string{"down", "enter"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-ran:
		if len(r.Keys) != 2 {
			t.Fatalf("keys = %+v", r.Keys)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local handler did not run")
	}
	ft.waitPending(0)
}

func TestPendingInteractionsSortedByCreatedAt(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	base := time.Now().UTC()
	s.fleet.mu.Lock()
	for i, id := range []string{"b", "a", "c"} {
		s.ixMapLocked()[id] = &pending{ix: proto.Interaction{ID: id, CreatedAt: base.Add(time.Duration(i) * time.Second)}}
	}
	s.fleet.mu.Unlock()
	var got []string
	for _, ix := range s.pendingInteractions() {
		got = append(got, ix.ID)
	}
	if strings.Join(got, ",") != "b,a,c" {
		t.Fatalf("ids = %v", got)
	}
}

func TestCancelMachineInteractions(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	s.fleet.mu.Lock()
	s.ixMapLocked()["a"] = &pending{ix: proto.Interaction{ID: "a", MachineID: "mac"}, done: make(chan proto.Resolution, 1)}
	s.ixMapLocked()["b"] = &pending{ix: proto.Interaction{ID: "b", MachineID: "laptop"}, done: make(chan proto.Resolution, 1)}
	s.ixMapLocked()["r"] = &pending{ix: proto.Interaction{ID: "r", MachineID: "mac", Resumable: true}, done: make(chan proto.Resolution, 1)}
	s.fleet.mu.Unlock()
	s.NodeDetached("mac")
	if got := strings.Join(ids(s.pendingInteractions()), ","); got != "b,r" {
		t.Fatalf("left = %s", got)
	}
}

// One of the hub's own features asks (fleet.ask): a card like a hook's, its answer back to the hub.
func TestAskHub(t *testing.T) {
	ft := newFleetTestNoStore(t)
	b := &BusHub{svc: ft.s, helloed: make(chan struct{})}
	close(b.helloed)
	ask := assistantwire.FleetAskParams{Interaction: proto.Interaction{
		MachineID: "mac", Kind: proto.IxPermission, OutwardWrite: true, ToolName: "mail_send", Summary: "send it", ExpiresAt: time.Now().Add(time.Minute),
	}}
	done := make(chan any, 1)
	go func() {
		r, err := b.answer(t.Context(), assistantwire.FleetAskMethod.Name, mustJSON(ask))
		if err != nil {
			t.Errorf("fleet ask: %v", err)
		}
		done <- r
	}()
	ix := ft.waitPending(1)[0]
	if ix.Kind != proto.IxPermission || ix.ToolName != "mail_send" || ix.Summary != "send it" || !strings.HasPrefix(ix.ID, "ix_") {
		t.Fatalf("unexpected interaction %+v", ix)
	}
	no := false
	if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &no}); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if r, ok := out.(proto.Resolution); !ok || r.Allow == nil || *r.Allow {
		t.Fatalf("answer = %+v (%T), want a denied resolution", out, out)
	}
	// It was pushed as any card (to the devices that take "needs you" pushes).
	if got := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(got) != 1 || got[0].Interaction == nil || got[0].Interaction.ID != ix.ID {
		t.Fatalf("pushes = %+v", got)
	}
}

func TestTrimScreen(t *testing.T) {
	if got := trimScreen("\n  \n⏺ hi\n> \n\n"); got != "⏺ hi\n>" {
		t.Fatalf("trimScreen = %q", got)
	}
	if trimScreen("") != "" {
		t.Fatal("empty screen should stay empty")
	}
}

// ---- resumable cards ----

func resumable(id string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true,
		ExpiresAt: time.Now().Add(time.Hour), Resumable: true}
}

type overlappingAdmissionStore struct {
	*fleetMem
	mu         sync.Mutex
	reads      int
	firstRead  chan struct{}
	secondRead chan struct{}
	unblock    chan struct{}
}

func (s *overlappingAdmissionStore) InteractionRecord(ctx context.Context, id string) (*db.InteractionRecord, error) {
	s.mu.Lock()
	s.reads++
	n := s.reads
	if n == 1 {
		close(s.firstRead)
	} else if n == 2 {
		close(s.secondRead)
	}
	s.mu.Unlock()
	if n == 1 {
		select {
		case <-s.unblock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.fleetMem.InteractionRecord(ctx, id)
}

func TestConcurrentFirstCreatesShareOneAdmission(t *testing.T) {
	store := &overlappingAdmissionStore{
		fleetMem: newFleetMem(), firstRead: make(chan struct{}),
		secondRead: make(chan struct{}), unblock: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(store.unblock) }) }
	defer release()
	ft := newFleetTestOn(t, store)
	ix := resumable("ix_concurrent_first_create")
	ix.CreatedAt = time.Now().UTC()
	type result struct {
		r   proto.Resolution
		err error
	}
	results := make(chan result, 2)
	go func() {
		r, err := ft.s.waitInteraction(context.Background(), ix)
		results <- result{r, err}
	}()
	select {
	case <-store.firstRead:
	case <-time.After(time.Second):
		t.Fatal("the first create did not reach its occurrence-history read")
	}
	go func() {
		r, err := ft.s.waitInteraction(context.Background(), ix)
		results <- result{r, err}
	}()
	select {
	case <-store.secondRead:
		release()
		t.Fatal("a concurrent first create passed the history check before the first create registered")
	case <-time.After(75 * time.Millisecond):
	}
	release()
	ft.waitPending(1)
	allow := true
	if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil || got.r.Allow == nil || !*got.r.Allow {
				t.Fatalf("joined create = %+v, %v", got.r, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a joined first create did not receive the shared answer")
		}
	}
}

// The node goes away under a waiting hook; the hook comes back through the next node and gets the answer.
func TestResumableSurvivesNodeRestart(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	ctx, nodeGone := context.WithCancel(context.Background())
	first := make(chan proto.Resolution, 1)
	go func() { r, _ := s.waitInteraction(ctx, resumable("ix_r")); first <- r }()
	ft.waitPending(1)
	nodeGone()
	<-first
	s.NodeDetached("mac") // the machine going offline leaves it for the hook
	ft.waitPending(1)
	second := make(chan proto.Resolution, 1)
	go func() { r, _ := s.waitInteraction(context.Background(), resumable("ix_r")); second <- r }()
	time.Sleep(20 * time.Millisecond)
	ft.waitPending(1) // the same card, not a second one
	yes := true
	if err := s.resolve("ix_r", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-second:
		if r.Allow == nil || !*r.Allow {
			t.Fatalf("resumed hook got %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resumed hook never got the answer")
	}
	ft.waitPending(0)
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, 200*time.Millisecond); len(got) != 1 {
		t.Fatalf("one card, %d pushes", len(got))
	}
}

// Answered while no node was there: the hook that comes back reads the answer.
func TestResumableAnsweredDuringGap(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	ctx, nodeGone := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = s.waitInteraction(ctx, resumable("ix_g")); close(done) }()
	ft.waitPending(1)
	nodeGone()
	<-done
	no := false
	if err := s.resolve("ix_g", proto.Resolution{Allow: &no, Message: "不行"}); err != nil {
		t.Fatal(err)
	}
	ft.waitPending(0)
	r, err := s.waitInteraction(context.Background(), resumable("ix_g"))
	if err != nil || r.Allow == nil || *r.Allow || r.Message != "不行" {
		t.Fatalf("got %+v %v", r, err)
	}
}

// A resumable id is a reference to one occurrence, not permission to reuse its answer.
func TestResumableReusedIDDoesNotInheritAnotherOccurrence(t *testing.T) {
	for _, state := range []string{"live", "pending", "resolved"} {
		t.Run(state, func(t *testing.T) {
			ft := newFleetTest(t)
			old := resumable("ix_reused_" + state)
			old.CreatedAt = time.Unix(1000, 0).UTC()
			var cancelOld context.CancelFunc
			var oldDone chan struct{}
			switch state {
			case "live":
				var ctx context.Context
				ctx, cancelOld = context.WithCancel(context.Background())
				oldDone = make(chan struct{})
				go func() {
					_, _ = ft.s.waitInteraction(ctx, old)
					close(oldDone)
				}()
				ft.waitPending(1)
			case "pending":
				ft.s.record(old)
			case "resolved":
				ft.s.record(old)
				allow := true
				if err := ft.st.FinishInteraction(context.Background(), old.ID, "resolved", &proto.Resolution{Allow: &allow}); err != nil {
					t.Fatal(err)
				}
			}
			changed := old
			changed.CreatedAt = changed.CreatedAt.Add(time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			r, err := ft.s.waitInteraction(ctx, changed)
			cancel()
			if err == nil || !strings.Contains(err.Error(), "different occurrence") {
				t.Fatalf("reused id returned the old operation: resolution=%+v err=%v", r, err)
			}
			if cancelOld != nil {
				cancelOld()
				select {
				case <-oldDone:
				case <-time.After(time.Second):
					t.Fatal("the original waiter did not finish")
				}
			}
		})
	}
}

func TestNodeLifecycleEventRejectsDifferentOccurrence(t *testing.T) {
	for _, event := range []string{"interaction.cancel", "interaction.dismiss"} {
		t.Run(event, func(t *testing.T) {
			ft := newFleetTest(t)
			original := resumable("ix_node_lifecycle_occurrence")
			original.CreatedAt = time.Now().UTC()
			type result struct {
				r   proto.Resolution
				err error
			}
			first := make(chan result, 1)
			go func() {
				r, err := ft.s.waitInteraction(context.Background(), original)
				first <- result{r, err}
			}()
			ft.waitPending(1)
			replacement := original
			replacement.CreatedAt = original.CreatedAt.Add(time.Second)
			if _, err := ft.s.waitInteraction(context.Background(), replacement); !errors.Is(err, errInteractionIDReused) {
				t.Fatalf("replacement create = %v, want occurrence rejection", err)
			}
			params, _ := json.Marshal(map[string]any{"id": original.ID, "created_at": replacement.CreatedAt})
			if ft.s.NodeEvent("mac", event, params) {
				t.Fatal("a node lifecycle event must stay manager-owned")
			}
			pending := ft.waitPending(1)
			if !pending[0].CreatedAt.Equal(original.CreatedAt) {
				t.Fatalf("the old lifecycle event ended the replacement: %+v", pending[0])
			}
			select {
			case got := <-first:
				t.Fatalf("the original waiter ended on another occurrence's %s: %+v", event, got)
			default:
			}
			deny := false
			if err := ft.s.resolve(original.ID, proto.Resolution{Allow: &deny}); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-first:
				if got.err != nil || got.r.Allow == nil || *got.r.Allow {
					t.Fatalf("original cleanup = %+v, %v", got.r, got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the original waiter did not finish")
			}
		})
	}
}

func TestCodexSyncReplacesSameIDOccurrence(t *testing.T) {
	ft := newFleetTest(t)
	ix := permission("ix_codex_sync_replacement")
	ix.CreatedAt = time.Now().UTC()
	params := func(card proto.Interaction) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"interactions": []proto.Interaction{card}})
		return raw
	}
	ft.s.syncCodexInteractions("mac", params(ix))
	first := ft.waitPending(1)[0]
	if !first.CreatedAt.Equal(ix.CreatedAt) {
		t.Fatalf("first occurrence = %+v", first)
	}
	replacement := ix
	replacement.CreatedAt = ix.CreatedAt.Add(time.Second)
	ft.s.syncCodexInteractions("mac", params(replacement))
	current := ft.waitPending(1)[0]
	if !current.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("same-ID sync retained the earlier occurrence: first=%s current=%s want=%s", first.CreatedAt, current.CreatedAt, replacement.CreatedAt)
	}
	rec, err := ft.st.InteractionRecord(context.Background(), ix.ID)
	if err != nil || rec == nil || rec.Status != "pending" || !rec.Interaction.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("replacement durable record = %+v, %v", rec, err)
	}
}

func TestCodexReplacementIsAnnounced(t *testing.T) {
	ft := newFleetTest(t)
	ix := permission("ix_codex_replacement_announcement")
	ix.CreatedAt = time.Now().UTC()
	params := func(card proto.Interaction) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"interactions": []proto.Interaction{card}})
		return raw
	}
	ft.s.syncCodexInteractions("mac", params(ix))
	if got := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(got) != 1 {
		t.Fatalf("first occurrence pushes = %d, want 1", len(got))
	}
	replacement := ix
	replacement.CreatedAt = ix.CreatedAt.Add(time.Second)
	ft.s.syncCodexInteractions("mac", params(replacement))
	if got := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(got) != 2 {
		t.Fatalf("same-ID replacement was treated as a quiet reconnect: pushes = %d, want 2", len(got))
	}
}

func TestOldLocalCompletionCannotFinalizeReplacement(t *testing.T) {
	ft := newFleetTest(t)
	original := permission("ix_local_completion_replacement")
	original.CreatedAt = time.Now().UTC()
	replacement := original
	replacement.CreatedAt = original.CreatedAt.Add(time.Second)
	p := &pending{ix: original, codex: true}
	p.local = func(context.Context, proto.Resolution) error {
		ft.s.record(replacement)
		ft.s.fleet.mu.Lock()
		ft.s.ixMapLocked()[replacement.ID] = &pending{ix: replacement, codex: true}
		ft.s.fleet.mu.Unlock()
		return nil
	}
	ft.s.record(original)
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()[original.ID] = p
	ft.s.fleet.mu.Unlock()
	allow := true
	if err := ft.s.resolve(original.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	rec, err := ft.st.InteractionRecord(context.Background(), replacement.ID)
	if err != nil || rec == nil || rec.Status != "pending" || !rec.Interaction.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("old local completion finalized the replacement: record=%+v err=%v", rec, err)
	}
	current := ft.waitPending(1)[0]
	if !current.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("old local completion removed the replacement: %+v", current)
	}
}

type expiryOccurrenceStore struct {
	Store
	onFinish func()
	finishes int
}

func (s *expiryOccurrenceStore) ReserveConversationInteraction(context.Context, proto.Interaction, string, string, string, string, int64) error {
	return nil
}
func (s *expiryOccurrenceStore) ConversationInteraction(context.Context, string) (*db.ConversationInteractionRoute, error) {
	return nil, nil
}
func (s *expiryOccurrenceStore) FinishConversationInteraction(context.Context, string) error {
	s.finishes++
	s.onFinish()
	return nil
}

func TestLocalExpiryCannotFinalizeReplacement(t *testing.T) {
	ft := newFleetTest(t)
	original := permission("ix_local_expiry_replacement")
	original.CreatedAt = time.Now().UTC()
	original.ExpiresAt = original.CreatedAt.Add(-time.Second)
	replacement := original
	replacement.CreatedAt = original.CreatedAt.Add(time.Second)
	replacement.ExpiresAt = replacement.CreatedAt.Add(time.Hour)
	p := &pending{ix: original, local: func(context.Context, proto.Resolution) error { return nil }}
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()[original.ID] = p
	ft.s.fleet.mu.Unlock()
	routes := &expiryOccurrenceStore{Store: ft.st}
	routes.onFinish = func() {
		// The route write is the deterministic race window: B replaces A after
		// the expiry pass collected A but before its old ID-only finalizer returns.
		ft.s.fleet.mu.Lock()
		ft.s.ixMapLocked()[replacement.ID] = &pending{ix: replacement, local: func(context.Context, proto.Resolution) error { return nil }}
		ft.s.fleet.mu.Unlock()
	}
	ft.s.setStore(func() Store { return routes })
	ft.s.expireLocalInteractions(time.Now())
	current, ok := ft.s.pendingIx(original.ID)
	if !ok || !current.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("the earlier occurrence's expiry finalized its replacement: current=%+v ok=%v", current, ok)
	}
	if routes.finishes != 1 {
		t.Fatalf("logical route finalizations = %d, want the original's one finalization", routes.finishes)
	}
}

func TestAlwaysForANewOccurrenceDoesNotResolveThePendingOriginal(t *testing.T) {
	ft := newFleetTest(t)
	old := alwaysIx("ix_always_reused", "s1", "tool:old")
	old.CreatedAt = time.Unix(1000, 0).UTC()
	first := make(chan detachedTestResult, 1)
	go func() {
		res, err := ft.s.nodeInteraction(context.Background(), old)
		first <- detachedTestResult{res: res, err: err}
	}()
	ft.waitPending(1)

	changed := alwaysIx(old.ID, "s1", "tool:new")
	changed.CreatedAt = old.CreatedAt.Add(time.Second)
	if err := ft.st.AllowAlways(context.Background(), "s1", "Bash", changed.AlwaysKeys); err != nil {
		t.Fatal(err)
	}
	res, err := ft.s.nodeInteraction(context.Background(), changed)
	if !errors.Is(err, errInteractionIDReused) || (res.Allow != nil && *res.Allow) {
		t.Fatalf("Always for the new occurrence = %+v, %v; want occurrence rejection", res, err)
	}
	if got := len(ft.s.pendingInteractions()); got != 1 {
		t.Fatalf("the new occurrence's Always rule resolved the original: pending=%d", got)
	}
	select {
	case got := <-first:
		t.Fatalf("the original waiter received the unrelated Always answer: %+v", got)
	default:
	}
	deny := false
	if err := ft.s.resolve(old.ID, proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	if got := awaitDetachedTest(t, first); got.err != nil || (got.res.Allow != nil && *got.res.Allow) {
		t.Fatalf("original cleanup = %+v, %v", got.res, got.err)
	}
}

// Nobody claims it in time: it ends as cancelled, and a late hook is told to hand over to the terminal.
func TestResumableGraceEnds(t *testing.T) {
	old := resumeGrace
	resumeGrace = 50 * time.Millisecond
	defer func() { resumeGrace = old }()
	ft := newFleetTest(t)
	s := ft.s
	ctx, nodeGone := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = s.waitInteraction(ctx, resumable("ix_x")); close(done) }()
	ft.waitPending(1)
	nodeGone()
	<-done
	ft.waitPending(0)
	if st, _, _ := ft.st.InteractionOutcome(context.Background(), "ix_x"); st != "cancelled" {
		t.Fatalf("status %q", st)
	}
	if r, _ := s.waitInteraction(context.Background(), resumable("ix_x")); !r.Dismiss {
		t.Fatalf("a late hook must dismiss, got %+v", r)
	}
}

// Without the flag (an older node, mail approvals) the old behaviour stays: the node going away cancels it.
func TestNonResumableCancelledWithNode(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ctx, nodeGone := context.WithCancel(context.Background())
	ix := resumable("ix_old")
	ix.Resumable = false
	done := make(chan struct{})
	go func() { _, _ = ft.s.waitInteraction(ctx, ix); close(done) }()
	ft.waitPending(1)
	nodeGone()
	<-done
	ft.waitPending(0)
}

// A manager that starts again (the hub restarted, or it did) takes back the resumable cards the last one left: the
// hook that reconnects claims its card without a second push, and the hub gets it in the first full list.
func TestResumableSurvivesAManagerRestart(t *testing.T) {
	ft := newFleetTest(t)
	ctx, nodeGone := context.WithCancel(context.Background())
	go func() { _, _ = ft.s.waitInteraction(ctx, resumable("ix_m")) }()
	ft.waitPending(1)
	ft.pushesOf(assistantwire.NotifyInput, 1, time.Second)
	nodeGone()

	// The next manager, on the same table.
	st := &restoreMem{fleetMem: ft.st}
	f2 := newFakeHub()
	// testLogf: restoring the cards starts deliveries of their results that can still be logging as the test ends (#16045).
	s2 := newRestartedService(t, Options{Hub: f2, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	s2.FleetUp(context.Background())
	var full assistantwire.FleetInteractions
	for _, tl := range f2.told {
		if tl.event == assistantwire.EventFleetInteractions {
			_ = json.Unmarshal(tl.params, &full)
		}
	}
	if len(full.Interactions) != 1 || full.Interactions[0].ID != "ix_m" {
		t.Fatalf("the hub's full list = %+v", full)
	}
	second := make(chan proto.Resolution, 1)
	go func() { r, _ := s2.waitInteraction(context.Background(), resumable("ix_m")); second <- r }()
	time.Sleep(50 * time.Millisecond)
	yes := true
	if err := s2.resolve("ix_m", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	if r := <-second; r.Allow == nil || !*r.Allow {
		t.Fatalf("the hook got %+v", r)
	}
	f2.mu.Lock()
	n := len(f2.pushes)
	f2.mu.Unlock()
	if n != 0 {
		t.Fatalf("the claimed card was pushed again (%d)", n)
	}
}

func TestStaleAwakenedWaiterCannotFinishReplacement(t *testing.T) {
	ft := newFleetTest(t)
	first := resumable("ix_stale_awakened_waiter")
	first.CreatedAt = time.Now().UTC()
	second := first
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	allow := true
	resolved := make(chan struct{})
	close(resolved)
	old := &pending{ix: first, done: make(chan proto.Resolution, 1), resolved: resolved,
		resolution: &proto.Resolution{Allow: &allow}}
	replacement := &pending{ix: second, done: make(chan proto.Resolution, 1)}
	putPending(ft.s, replacement)
	got, err := ft.s.await(context.Background(), old)
	if err != nil || got.Allow == nil || !*got.Allow {
		t.Fatalf("stale waiter's own result = %+v, %v", got, err)
	}
	ft.s.fleet.mu.Lock()
	kept := ft.s.fleet.interactions[second.ID] == replacement
	ft.s.fleet.mu.Unlock()
	if !kept {
		t.Fatal("a stale awakened waiter deleted the replacement occurrence")
	}
	for _, raw := range ft.toldOf(assistantwire.EventFleetResolved) {
		var event assistantwire.FleetResolved
		if json.Unmarshal(raw, &event) == nil && event.ID == second.ID && event.CreatedAt.Equal(second.CreatedAt) {
			t.Fatalf("stale waiter withdrew the replacement occurrence: %+v", event)
		}
	}
}

func TestFinishPreparedCannotFinishReplacement(t *testing.T) {
	ft := newFleetTest(t)
	first := resumable("ix_stale_prepared_finish")
	first.CreatedAt = time.Now().UTC()
	second := first
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	old := &pending{ix: first, done: make(chan proto.Resolution, 1), prepared: &preparedApproval{
		result: proto.Resolution{Dismiss: true},
	}}
	replacement := &pending{ix: second, done: make(chan proto.Resolution, 1)}
	putPending(ft.s, replacement)
	if err := ft.s.finishPrepared(old, "expired"); err != nil {
		t.Fatal(err)
	}
	ft.s.fleet.mu.Lock()
	kept := ft.s.fleet.interactions[second.ID] == replacement
	ft.s.fleet.mu.Unlock()
	if !kept {
		t.Fatal("a stale prepared finalizer deleted the replacement occurrence")
	}
	for _, raw := range ft.toldOf(assistantwire.EventFleetResolved) {
		var event assistantwire.FleetResolved
		if json.Unmarshal(raw, &event) == nil && event.ID == second.ID && event.CreatedAt.Equal(second.CreatedAt) {
			t.Fatalf("stale prepared finalizer withdrew the replacement occurrence: %+v", event)
		}
	}
}

// restoreMem is the interactions table as the next manager reads it: the resumable ones still pending, the others
// cancelled (db ExpireStaleInteractions).
type restoreMem struct{ *fleetMem }

func (r *restoreMem) ExpireStaleInteractions(context.Context) ([]proto.Interaction, []proto.Interaction, error) {
	r.fmu.Lock()
	defer r.fmu.Unlock()
	var out []proto.Interaction
	var cancelled []proto.Interaction
	for _, rec := range r.recs {
		switch id := rec.Interaction.ID; {
		case r.status[id] != "pending":
		case rec.Interaction.Resumable:
			out = append(out, rec.Interaction)
		default:
			r.status[id] = "cancelled"
			cancelled = append(cancelled, rec.Interaction)
		}
	}
	return out, cancelled, nil
}

// ResumableInteractions is the read-only startup retry: unlike
// ExpireStaleInteractions it never cancels cards created by the current process.
func (r *restoreMem) ResumableInteractions(context.Context) ([]proto.Interaction, error) {
	r.fmu.Lock()
	defer r.fmu.Unlock()
	var out []proto.Interaction
	for _, rec := range r.recs {
		if r.status[rec.Interaction.ID] == "pending" && rec.Interaction.Resumable && rec.Interaction.ExpiresAt.After(time.Now()) {
			out = append(out, rec.Interaction)
		}
	}
	return out, nil
}

// The assistant's question that a hub switch took its manager away from (#4568): the next manager cancels it, as no
// tool call waits on it any more, and tells the hub it is over, so an app that resumed its event stream across the
// switch drops the card instead of showing it next to the question the assistant asks again.
func TestRestartResolvesTheCardsTheLastManagerLeft(t *testing.T) {
	ft := newFleetTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // the first manager's call: it is gone with its process, nobody ends it
	go func() {
		_, _ = ft.s.toolAskUser(ctx, "mac", "s1", json.RawMessage(`{"why_owner":"priority","questions":[{"header":"Merge","question":"Merge it?",
			"options":[{"label":"Merge"},{"label":"Not yet"}]}]}`))
	}()
	asked := ft.waitPending(1)[0]
	if asked.ToolName != "ask_user" {
		t.Fatalf("pending %+v", asked)
	}

	// The next manager, on the same table.
	st := &restoreMem{fleetMem: ft.st}
	f2 := newFakeHub()
	// testLogf: restoring the cards starts deliveries of their results that can still be logging as the test ends (#16045).
	s2 := newRestartedService(t, Options{Hub: f2, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	s2.FleetUp(context.Background())
	if status, _, _ := st.InteractionOutcome(context.Background(), asked.ID); status != "cancelled" {
		t.Fatalf("status %q", status)
	}
	var resolved []assistantwire.FleetResolved
	for _, tl := range f2.told {
		var p assistantwire.FleetResolved
		if tl.event == assistantwire.EventFleetResolved && json.Unmarshal(tl.params, &p) == nil {
			resolved = append(resolved, p)
		}
		var full assistantwire.FleetInteractions
		if tl.event == assistantwire.EventFleetInteractions && json.Unmarshal(tl.params, &full) == nil && len(full.Interactions) != 0 {
			t.Fatalf("the hub's full list = %+v", full.Interactions)
		}
	}
	if len(resolved) != 1 || resolved[0].ID != asked.ID || asked.CreatedAt.IsZero() || !resolved[0].CreatedAt.Equal(asked.CreatedAt) {
		t.Fatalf("resolved for the hub = %+v, want occurrence %s at %s", resolved, asked.ID, asked.CreatedAt)
	}
}

// A node's interaction.create through the manager (devices.Options.Answer): the node's connection ending ends the
// wait, so the hook that comes back through the next connection is the one that gets the answer.
func TestNodeRequestResumes(t *testing.T) {
	ft := newFleetTest(t)
	ask := func(ctx context.Context) <-chan proto.Resolution {
		params, _ := json.Marshal(map[string]any{"interaction": resumable("ix_b")})
		out := make(chan proto.Resolution, 1)
		go func() {
			res, ok, err := ft.s.NodeRequest(ctx, "mac", "interaction.create", params)
			if !ok || err != nil {
				t.Errorf("%v %v", ok, err)
			}
			r, _ := res.(proto.Resolution)
			out <- r
		}()
		return out
	}
	ctx, gone := context.WithCancel(context.Background())
	first := ask(ctx)
	ft.waitPending(1)
	gone()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request still waits after its connection ended")
	}
	ft.waitPending(1)
	second := ask(context.Background())
	time.Sleep(20 * time.Millisecond)
	yes := true
	if err := ft.s.resolve("ix_b", proto.Resolution{Allow: &yes}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-second:
		if r.Allow == nil || !*r.Allow {
			t.Fatalf("the hook that came back got %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hook that came back never got the answer")
	}
}

// slowEndMem holds the first interaction outcome write until release is closed, so a test can act inside it.
type slowEndMem struct {
	*fleetMem
	entered, release chan struct{}
	once             sync.Once
	mu               sync.Mutex
	ends             []string // the statuses written, in order
}

func (m *slowEndMem) FinishInteraction(ctx context.Context, id, status string, res *proto.Resolution) error {
	m.mu.Lock()
	m.ends = append(m.ends, status)
	m.mu.Unlock()
	m.once.Do(func() {
		close(m.entered)
		<-m.release
	})
	return m.fleetMem.FinishInteraction(ctx, id, status, res)
}

func (m *slowEndMem) written() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.ends)
}

// slowEndTest is a fleet test whose first outcome write waits for st.release, and a resumable card id whose hook
// went away, with a grace of its own that ends in grace (resumeGrace is shared with goroutines other tests leave
// waiting: the timer is started again with an expiry that close, then the card's own expiry is put back).
func slowEndTest(t *testing.T, id string, grace time.Duration) (*fleetTest, *slowEndMem) {
	t.Helper()
	ft := newFleetTest(t)
	st := &slowEndMem{fleetMem: ft.st, entered: make(chan struct{}), release: make(chan struct{})}
	ft.s = New(Options{Hub: ft.f, Store: func() Store { return st }, Logf: t.Logf, Devices: ft.d})
	s := ft.s
	ctx, nodeGone := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = s.waitInteraction(ctx, resumable(id)); close(done) }()
	ft.waitPending(1)
	nodeGone()
	<-done
	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	p.orphaned.Stop()
	p.orphaned = nil
	p.ix.ExpiresAt = time.Now().Add(grace)
	s.orphanLocked(p)
	p.ix.ExpiresAt = time.Now().Add(time.Hour)
	s.fleet.mu.Unlock()
	return ft, st
}

// The grace period ends while the hook comes back: whichever wins, the hook is not left waiting on a card nobody can
// see or answer. Here the grace end is already writing the history when the hook asks again: the hook is told to hand
// over (dismiss), as a hook coming back just after would be. An answer given then finds the card no longer pending.
func TestResumableReclaimedAsTheGraceEnds(t *testing.T) {
	ft, st := slowEndTest(t, "ix_e", 50*time.Millisecond)
	s := ft.s
	select {
	case <-st.entered: // the grace timer decided to end it and is writing "cancelled"
	case <-time.After(5 * time.Second):
		t.Fatal("the grace period never ended")
	}
	back := make(chan proto.Resolution, 1)
	go func() { r, _ := s.waitInteraction(context.Background(), resumable("ix_e")); back <- r }()
	yes := true
	if err := s.resolve("ix_e", proto.Resolution{Allow: &yes}); !errors.Is(err, errNotPending) {
		t.Fatalf("an answer while the grace end is written: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // the hook is back before the write returns
	close(st.release)
	ft.waitPending(0)
	select {
	case r := <-back:
		if !r.Dismiss {
			t.Fatalf("the hook back as the grace ended got %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the hook back as the grace ended waits on a card that is gone")
	}
	if got := st.written(); !slices.Equal(got, []string{"cancelled"}) {
		t.Fatalf("written %v", got)
	}
}

// The grace period ends while an answer given meanwhile is being written: the answer stands, not cancelled over, and
// the hook that comes back reads it.
func TestResumableAnswerWrittenAsTheGraceEnds(t *testing.T) {
	ft, st := slowEndTest(t, "ix_a", 300*time.Millisecond)
	s := ft.s
	answered := make(chan error, 1)
	yes := true
	go func() { answered <- s.resolve("ix_a", proto.Resolution{Allow: &yes}) }()
	select {
	case <-st.entered: // the answer is being written
	case <-time.After(5 * time.Second):
		t.Fatal("the answer was never written")
	}
	time.Sleep(400 * time.Millisecond) // the grace runs out meanwhile
	close(st.release)
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
	ft.waitPending(0)
	if got := st.written(); !slices.Equal(got, []string{"resolved"}) {
		t.Fatalf("written %v: the grace end cancelled over the answer", got)
	}
	r, err := s.waitInteraction(context.Background(), resumable("ix_a"))
	if err != nil || r.Allow == nil || !*r.Allow {
		t.Fatalf("the hook back got %+v %v", r, err)
	}
}

// hubCards are the cards the hub holds after what it was told, kept as fleet_link.go keeps them: a card is added by
// fleet.interaction and taken off by fleet.interaction_resolved, in the order they came.
func (ft *fleetTest) hubCards() map[string]bool {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	cards := map[string]bool{}
	for _, t := range ft.f.told {
		switch t.event {
		case assistantwire.EventFleetInteraction:
			var p assistantwire.FleetInteraction
			if json.Unmarshal(t.params, &p) == nil {
				cards[p.Interaction.ID] = true
			}
		case assistantwire.EventFleetResolved:
			var p assistantwire.FleetResolved
			if json.Unmarshal(t.params, &p) == nil {
				delete(cards, p.ID)
			}
		}
	}
	return cards
}

// A runtime that withdraws its card while the manager is still telling it leaves no card on the hub. OpenCode's TUI in
// auto accept answers a permission the moment it is asked, and the plugin then cancels the card it was raising: the
// cancel ends the card before it reached the apps, and the card told afterwards stayed on the hub for good, "needs
// you" with nothing to answer (410).
func TestCardCancelledWhileToldLeavesNoCardOnTheHub(t *testing.T) {
	ft := newFleetTest(t)
	ft.st.insertGate = make(chan struct{}) // the card is registered; its history row and the apps wait
	ix := proto.Interaction{ID: "opencode-1", MachineID: "mac", SessionID: "ses_1", Kind: proto.IxPermission, OutwardWrite: true,
		ToolName: "external_directory", Resumable: true, ExpiresAt: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = ft.s.waitInteraction(ctx, ix) }()
	ft.waitPending(1)
	ft.event("mac", "interaction.cancel", map[string]any{"id": ix.ID})
	ft.waitPending(0)
	close(ft.st.insertGate)
	waitFor(t, "the card told to the hub", func() bool { return len(ft.toldOf(assistantwire.EventFleetInteraction)) > 0 })
	waitFor(t, "the hub to hold no card", func() bool { return len(ft.hubCards()) == 0 })
}

// hookedHub is a fake hub that runs hook (when set) before it takes each tell, so a test can have something happen
// right before the hub gets a card or its end.
type hookedHub struct {
	*fakeHub
	hook func(event string, params any)
}

func (h *hookedHub) Tell(event string, params any) error {
	if h.hook != nil {
		h.hook(event, params)
	}
	return h.fakeHub.Tell(event, params)
}

// newHookedFleet is newFleetTestNoStore on a hookedHub.
func newHookedFleet(t *testing.T) (*fleetTest, *hookedHub) {
	t.Helper()
	h := &hookedHub{fakeHub: newFakeHub()}
	d := newFleetDevs()
	d.add("mac", "Mac")
	s := New(Options{Hub: h, Logf: testLogf(t), Devices: d})
	return &fleetTest{t: t, s: s, d: d, f: h.fakeHub}, h
}

// hold registers card ix as pending, not told yet (as a card the assistant or a supervisor holds).
func (ft *fleetTest) hold(ix proto.Interaction) {
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()[ix.ID] = &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	ft.s.fleet.mu.Unlock()
}

// cancelBeforeTold has the node withdraw card id (interaction.cancel) right before the hub is told it: the cancel lands
// between the caller's own check that the card is pending and its tell.
func cancelBeforeTold(ft *fleetTest, h *hookedHub, id string) {
	var once sync.Once
	h.hook = func(event string, params any) {
		if p, ok := params.(assistantwire.FleetInteraction); ok && event == assistantwire.EventFleetInteraction && p.Interaction.ID == id {
			once.Do(func() { ft.event("mac", "interaction.cancel", map[string]any{"id": id}) })
		}
	}
}

// A card the assistant hands back (escalate) and its node withdraws as it is told leaves no card on the hub.
func TestEscalatedCardCancelledAsItIsToldLeavesNoCardOnTheHub(t *testing.T) {
	ft, h := newHookedFleet(t)
	ix := proto.Interaction{ID: "ix_esc", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	ft.hold(ix)
	cancelBeforeTold(ft, h, ix.ID)
	ft.s.escalate(ix)
	if len(ft.toldOf(assistantwire.EventFleetInteraction)) == 0 || len(ft.s.pendingInteractions()) != 0 {
		t.Fatalf("the card was not told and withdrawn: told %s", ft.toldText())
	}
	if cards := ft.hubCards(); len(cards) != 0 {
		t.Fatalf("the hub kept the withdrawn card: %v", cards)
	}
}

// A card a supervisor passes up (ringAsks) and its node withdraws as it is told leaves no card on the hub.
func TestRungCardCancelledAsItIsToldLeavesNoCardOnTheHub(t *testing.T) {
	ft, h := newHookedFleet(t)
	ix := proto.Interaction{ID: "ix_up", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	ft.hold(ix)
	cancelBeforeTold(ft, h, ix.ID)
	if rung := ft.s.ringAsks("todo_1", []string{ix.ID}, nil, escalation{}); len(rung) != 1 {
		t.Fatalf("rung %+v", rung)
	}
	if len(ft.toldOf(assistantwire.EventFleetInteraction)) == 0 || len(ft.s.pendingInteractions()) != 0 {
		t.Fatalf("the card was not told and withdrawn: told %s", ft.toldText())
	}
	if cards := ft.hubCards(); len(cards) != 0 {
		t.Fatalf("the hub kept the withdrawn card: %v", cards)
	}
}

// The end told again for a card that ended on its way never takes a newer card under the same id off the hub: a newer
// card shown while that end is being told reaches the hub after it.
func TestEndToldAgainLeavesANewerCardUnderTheSameID(t *testing.T) {
	ft, h := newHookedFleet(t)
	old := proto.Interaction{ID: "ix_same", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, ToolName: "Bash",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	newer := old
	newer.CreatedAt = old.CreatedAt.Add(time.Second)
	var once sync.Once
	h.hook = func(event string, params any) {
		if p, ok := params.(assistantwire.FleetResolved); !ok || event != assistantwire.EventFleetResolved || p.ID != old.ID {
			return
		}
		once.Do(func() {
			// Right after the old card was found ended, before its end reaches the hub: the newer card is held and
			// shown.
			ft.hold(newer)
			shown := make(chan struct{})
			go func() {
				ft.s.show(newer)
				close(shown)
			}()
			select {
			case <-shown: // without cardTell the newer card reaches the hub first and the old card's end removes it
			case <-time.After(200 * time.Millisecond): // held behind the old card's end, as it should be
			}
		})
	}
	ft.s.show(old) // ended before it reached the hub: not held
	waitFor(t, "the newer card told", func() bool { return len(ft.toldOf(assistantwire.EventFleetInteraction)) == 2 })
	if cards := ft.hubCards(); !cards[old.ID] {
		t.Fatalf("the newer card was taken off the hub: told %s", ft.toldText())
	}
}

// toldText is what the hub was told, one event per line.
func (ft *fleetTest) toldText() string {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	var b strings.Builder
	for _, t := range ft.f.told {
		fmt.Fprintf(&b, "\n  %s %s", t.event, t.params)
	}
	return b.String()
}
