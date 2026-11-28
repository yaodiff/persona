package devices

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// keptSource is one online machine whose answer says used% of a weekly window; a closed gate makes it answer at once,
// an open one makes it wait until the gate is closed.
type keptSource struct {
	asked atomic.Int32
	used  atomic.Int64
	gate  atomic.Pointer[chan struct{}]
	now   time.Time
}

func (k *keptSource) source() usageSource {
	return usageSource{id: "m1", online: true, ask: func(ctx context.Context) (proto.UsageSnapshot, error) {
		k.asked.Add(1)
		if g := k.gate.Load(); g != nil {
			select {
			case <-*g:
			case <-ctx.Done():
				return proto.UsageSnapshot{}, ctx.Err()
			}
		}
		win := []proto.UsageWindow{uw("7d", 10080, float64(k.used.Load()), k.now.Add(time.Hour))}
		return proto.UsageSnapshot{Enabled: true, Accounts: []proto.UsageNodeAccount{{UsageAccount: proto.UsageAccount{Provider: "codex", Name: "Codex", SampledAt: k.now, Windows: win}}}}, nil
	}}
}

func usedOf(t *testing.T, got []proto.UsageAccount) float64 {
	t.Helper()
	if len(got) != 1 || len(got[0].Windows) != 1 {
		t.Fatalf("want one account with one window: %+v", got)
	}
	return got[0].Windows[0].UsedPercent
}

// mergedAt waits until the hub's merged answer is the one built at at (a background poll landed).
func mergedAt(t *testing.T, u *usageWindowsHub, at time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		u.mu.Lock()
		done := u.at.Equal(at) && u.loading == nil
		u.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no merged answer built at %v", at)
		}
		time.Sleep(time.Millisecond) // sleep-ok: the background poll signals nothing when it lands; wait for its merged answer
	}
}

// The apps poll every minute and the merged answer stands for usageHubFresh (30 s), so nearly every poll found it
// expired and waited for the machines. Within usageHubKept the route's read answers with it at once, polls in the
// background (once, however many read meanwhile), and the next read has the new numbers.
func TestUsageReadAnswersAKeptAnswerAtOnceAndRefreshesIt(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	k := &keptSource{now: now}
	k.used.Store(12)
	src := []usageSource{k.source()}
	var u usageWindowsHub
	if got := u.read(context.Background(), src, now); usedOf(t, got) != 12 || k.asked.Load() != 1 {
		t.Fatalf("first read: %+v asked %d", got, k.asked.Load())
	}

	gate := make(chan struct{})
	k.gate.Store(&gate)
	k.used.Store(34)
	later := now.Add(usageHubFresh + time.Second)
	for i := range 3 {
		answered := make(chan []proto.UsageAccount, 1)
		go func() { answered <- u.read(context.Background(), src, later) }()
		select {
		case got := <-answered:
			if usedOf(t, got) != 12 {
				t.Fatalf("read %d: want the kept answer, got %+v", i, got)
			}
		case <-time.After(3 * time.Second):
			close(gate)
			t.Fatalf("read %d waited for the machines", i)
		}
	}
	close(gate)
	mergedAt(t, &u, later)
	if n := k.asked.Load(); n != 2 {
		t.Errorf("reads of a kept answer asked the machine %d times in all, want 2 (one refresh)", n)
	}
	if got := u.read(context.Background(), src, later.Add(time.Second)); usedOf(t, got) != 34 {
		t.Errorf("the read after the refresh: %+v", got)
	}
	if n := k.asked.Load(); n != 2 {
		t.Errorf("a fresh answer asked again: %d", n)
	}
}

// An answer as old as usageHubKept, or one invalidated (a gateway answered, the sources were edited), is not handed
// out: the read waits for the machines, as poll always did.
func TestUsageReadWaitsWhenTheKeptAnswerIsTooOldOrInvalidated(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	k := &keptSource{now: now}
	k.used.Store(12)
	src := []usageSource{k.source()}
	var u usageWindowsHub
	u.read(context.Background(), src, now)

	k.used.Store(34)
	old := now.Add(usageHubKept)
	if got := u.read(context.Background(), src, old); usedOf(t, got) != 34 || k.asked.Load() != 2 {
		t.Fatalf("an answer usageHubKept old was handed out: %+v asked %d", got, k.asked.Load())
	}

	u.invalidate()
	k.used.Store(56)
	if got := u.read(context.Background(), src, old.Add(usageHubFresh+time.Second)); usedOf(t, got) != 56 || k.asked.Load() != 3 {
		t.Fatalf("an invalidated answer was handed out: %+v asked %d", got, k.asked.Load())
	}
}

// The route reads through read, not poll: a kept answer comes back from GET /api/v1/usage/windows while the
// machines are asked behind it.
func TestUsageWindowsRouteAnswersAKeptAnswer(t *testing.T) {
	h := newTestDevices(t)
	at := time.Now().Add(-usageHubFresh - 5*time.Second)
	h.usageWin.mu.Lock()
	h.usageWin.merged = []proto.UsageAccount{{ID: "kept", Provider: "claude", Name: "Kept seat"}}
	h.usageWin.at = at
	h.usageWin.mu.Unlock()
	mux := h.Handler(http.NotFoundHandler())
	get := func() string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/usage/windows", nil))
		return w.Body.String()
	}
	if body := get(); !strings.Contains(body, `"name":"Kept seat"`) {
		t.Fatalf("the kept answer was not handed out: %s", body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.usageWin.mu.Lock()
		done := h.usageWin.at.After(at) && h.usageWin.loading == nil
		h.usageWin.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no refresh behind the kept answer")
		}
		time.Sleep(time.Millisecond) // sleep-ok: the refresh behind the kept answer signals nothing when it lands; wait for its result
	}
	if body := get(); strings.Contains(body, "Kept seat") {
		t.Errorf("the refreshed answer (no machines, no gateways) still has the kept seat: %s", body)
	}
}
