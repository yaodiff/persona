package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestPushReturns503WhenAPNsNotConfigured(t *testing.T) {
	s, err := New(Config{Topics: []string{topic}}, nil, bytes.Repeat([]byte{7}, 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Health reports apns=false without panicking.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/relay/v1/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d", w.Code)
	}
	var health map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health["apns"] != false {
		t.Fatalf("health apns = %v, want false", health["apns"])
	}

	// Register works with nil logf and produces valid grants.
	g := register(t, h)
	voipG := registerVoIP(t, h)

	// Invalid auth and unserved topics still fail with 401/403 before the 503 APNs check.
	req := PushRequest{Type: "alert", Sealed: sealed}
	if code, _ := call(h, "/relay/v1/push", "", req); code != http.StatusUnauthorized {
		t.Fatalf("missing grant: got %d, want 401", code)
	}
	if code, _ := call(h, "/relay/v1/push", s.seal(grant{Token: devTok, Env: "sandbox", Topic: "other.app"}), req); code != http.StatusForbidden {
		t.Fatalf("unserved topic: got %d, want 403", code)
	}

	// Valid grant with nil APNs returns 503 cleanly without panicking or consuming rate-limit budget.
	for range pushBurst + 5 {
		code, out := call(h, "/relay/v1/push", g, req)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("push without apns: got %d (%v), want 503", code, out)
		}
	}
	if len(s.pushLimit.buckets) != 0 {
		t.Fatalf("pushLimit consumed %d buckets while apns was nil", len(s.pushLimit.buckets))
	}

	// VoIP pushes also fail with 503 before touching voipLimit or pushLimit.
	code, out := call(h, "/relay/v1/push", voipG, PushRequest{Type: "voip", Sealed: sealed})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("voip push without apns: got %d (%v), want 503", code, out)
	}
	if len(s.voipLimit.buckets) != 0 {
		t.Fatalf("voipLimit consumed %d buckets while apns was nil", len(s.voipLimit.buckets))
	}
}

func TestLimiterHardCeilingFailsClosedAndRecoversAfterRefill(t *testing.T) {
	l := newLimiter(regPerMin, regBurst)
	l.maxKeys = 4
	t0 := time.Unix(1_800_000_000, 0)

	for i := range 4 {
		if !l.allow("ip:"+strconv.Itoa(i), t0) {
			t.Fatalf("key %d rejected below cap", i)
		}
	}
	// A 5th distinct key while the table is full triggers one sweep and fails closed without growing the map.
	if l.allow("ip:overflow", t0) {
		t.Fatal("overflow key accepted past maxKeys")
	}
	if len(l.buckets) != 4 || !l.swept.Equal(t0) {
		t.Fatalf("buckets = %d, swept = %v; want 4, %v", len(l.buckets), l.swept, t0)
	}
	// Repeated unseen keys within the same refill window fail closed in O(1) without resweeping.
	for i, delta := range []time.Duration{l.refill / 2, l.refill - time.Millisecond} {
		if l.allow("ip:flood"+strconv.Itoa(i), t0.Add(delta)) {
			t.Fatalf("flood key %d accepted past maxKeys", i)
		}
		if len(l.buckets) != 4 || !l.swept.Equal(t0) {
			t.Fatalf("flood %d: buckets = %d, swept = %v; want 4, %v", i, len(l.buckets), l.swept, t0)
		}
	}
	// Existing tracked keys continue to be served from their own buckets.
	if !l.allow("ip:0", t0) {
		t.Fatal("existing key rejected while table at cap")
	}
	// Once past refill after the sweep, stale buckets are collected and new keys are accepted again.
	t1 := t0.Add(l.refill + time.Millisecond)
	if !l.allow("ip:after", t1) {
		t.Fatal("new key rejected after stale buckets refilled")
	}
	if len(l.buckets) != 1 {
		t.Fatalf("buckets after sweep = %d, want 1", len(l.buckets))
	}
}

func TestLimiterFullTableRecoversAfterBackwardClockJump(t *testing.T) {
	l := newLimiter(regPerMin, regBurst)
	l.maxKeys = 4
	t0 := time.Unix(1_800_000_000, 0)

	for i := range 4 {
		if !l.allow("ip:"+strconv.Itoa(i), t0) {
			t.Fatalf("key %d rejected below cap", i)
		}
	}
	// Trigger initial sweep at t0 so l.swept is also ahead of the jumped clock.
	if l.allow("ip:overflow", t0) {
		t.Fatal("overflow key accepted past maxKeys")
	}

	// Wall clock jumps backward by an hour; l.swept clamps to tBack on arrival.
	tBack := t0.Add(-time.Hour)
	if l.allow("ip:during-jump", tBack) {
		t.Fatal("unseen key accepted while table still full")
	}
	// First sweep after one refill clamps future bucket timestamps to t1 without evicting active keys prematurely.
	t1 := tBack.Add(l.refill)
	if l.allow("ip:first-sweep", t1) {
		t.Fatal("unseen key accepted before clamped buckets aged out")
	}
	if !l.swept.Equal(t1) {
		t.Fatalf("swept = %v, want %v", l.swept, t1)
	}
	// Second sweep after 2*refill collects all clamped buckets and restores capacity well before the 1h jump elapses.
	t2 := t1.Add(l.refill + time.Millisecond)
	if !l.allow("ip:recovered", t2) {
		t.Fatal("new key still locked out after 2*refill following backward clock jump")
	}
	if len(l.buckets) != 1 {
		t.Fatalf("buckets after recovery = %d, want 1", len(l.buckets))
	}
}

func TestLimiterBackwardClockStepDoesNotSubtractOrFreezeTokens(t *testing.T) {
	l := newLimiter(60, 2) // 1 token/sec, burst 2
	t0 := time.Unix(1_800_000_000, 0)
	if !l.allow("k", t0) {
		t.Fatal("first token rejected")
	}
	// Clock jumps backward by an hour: must neither go negative nor freeze refill for an hour.
	tBack := t0.Add(-time.Hour)
	if !l.allow("k", tBack) {
		t.Fatal("remaining token lost on backward clock step")
	}
	if l.allow("k", tBack) {
		t.Fatal("backward clock step granted extra token")
	}
	// After 1 second forward from the stepped clock, 1 token has refilled.
	if !l.allow("k", tBack.Add(time.Second)) {
		t.Fatal("bucket stayed frozen after backward clock step")
	}
}
