package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// One card per hand-off (confirmOutbound): a conversation that sends the same thing again while its card waits (a
// retry after a hub restart cut the call, a model that called twice) joins the card already on the phone instead of
// raising a second, and the same hand-off the owner allowed a moment ago is not asked about again. The owner saw the
// words and the target on the first card; the second card only repeated it.

// outboundAllowedFor is how long an allowed hand-off goes again without a card.
const outboundAllowedFor = 30 * time.Minute

type outboundOnce struct {
	mu      sync.Mutex
	allowed map[string]time.Time
	flight  map[string]*outboundFlight
}

type outboundFlight struct {
	done    chan struct{}
	joined  int // calls waiting on this card besides the one asking (under outboundOnce.mu)
	ok      bool
	outcome string // how the card ended (proto.Resolution.Outcome)
	err     error
}

// outboundKey identifies a hand-off and, when supplied, its exact proposal and privacy authority.
func outboundKey(self, tool, text, summary string, scope ...string) string {
	h := sha256.New()
	for _, p := range append([]string{self, tool, text, summary}, scope...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// confirm answers hand-off key, with how its card ended: at once when it was allowed within outboundAllowedFor (no
// card, outcome ""), else with the answer of the card already asking about it, else with ask's (and remembers an allow).
func (o *outboundOnce) confirm(ctx context.Context, key string, ask func() (bool, string, error)) (bool, string, error) {
	now := time.Now()
	o.mu.Lock()
	if o.allowed == nil {
		o.allowed, o.flight = map[string]time.Time{}, map[string]*outboundFlight{}
	}
	for k, at := range o.allowed {
		if now.Sub(at) > outboundAllowedFor {
			delete(o.allowed, k)
		}
	}
	if _, ok := o.allowed[key]; ok {
		o.mu.Unlock()
		return true, "", nil
	}
	if f, ok := o.flight[key]; ok {
		f.joined++
		o.mu.Unlock()
		select {
		case <-f.done:
			return f.ok, f.outcome, f.err
		case <-ctx.Done():
			return false, "", ctx.Err()
		}
	}
	f := &outboundFlight{done: make(chan struct{})}
	o.flight[key] = f
	o.mu.Unlock()
	f.ok, f.outcome, f.err = ask()
	o.mu.Lock()
	delete(o.flight, key)
	if f.err == nil && f.ok {
		o.allowed[key] = time.Now()
	}
	o.mu.Unlock()
	close(f.done)
	return f.ok, f.outcome, f.err
}
