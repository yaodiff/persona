package hub

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What a manager made while the hub was away comes with manager.outbox once it is back (docs/operations/hub-restart.md "The
// manager while the hub is away"): its pushes and the apps' live updates, oldest first, after it reported its state
// (its cards first: a card's push needs the card). The hub delivers each as it would have then, only about the
// manager's own machines, sessions and cards (deliverPushFrom, relayBroadcastFrom), and each once: a manager that did
// not hear the answer sends it again, and a push it sent live may come again from its outbox when the connection
// dropped before the answer. What was handed over is kept in the database (handed_over), so the next hub process does
// not deliver it again either.

func init() {
	handleBus(assistantwire.MethodOutbox, func(h *Hub, ctx context.Context, c *busConn, params json.RawMessage) (any, error) {
		var p assistantwire.Outbox
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: assistantwire.MethodOutbox + ": " + err.Error()}
		}
		from := c.managerID()
		out := assistantwire.OutboxResult{Done: []string{}}
		for _, it := range p.Items {
			if ctx.Err() != nil {
				break // the rest come again
			}
			if it.ID != "" && h.firstHandOver(ctx, from, it.ID) {
				var err error
				switch {
				case it.Notification != nil:
					_, err = h.deliverPushFrom(from, *it.Notification)
				case len(it.Message) > 0:
					err = h.relayBroadcastFrom(from, it.Message)
				}
				if err != nil {
					h.forgetHandOver(from, it.ID)
					h.logf("outbox: [%s] %s: %v (not delivered)", from, it.ID, err)
				}
			}
			out.Done = append(out.Done, it.ID) // delivered now or before, or not deliverable: not to come again
		}
		return out, nil
	})
}

// handedOverFor and handedOverMost bound what is remembered: an item comes again within seconds or minutes (the next
// connection), not hours later.
const (
	handedOverFor  = 2 * time.Hour
	handedOverMost = 4096
)

// handOverStore keeps what was handed over across hub processes (db.DB); without one (tests) it is remembered in
// memory only.
type handOverStore interface {
	HandOverFirst(ctx context.Context, manager, id string, keep time.Duration) (bool, error)
	HandOverForget(ctx context.Context, manager, id string) error
}

// firstHandOver records manager's item id and reports whether it is new. A store that does not answer: memory
// decides (the item is delivered rather than lost).
func (h *Hub) firstHandOver(ctx context.Context, manager, id string) bool {
	// Memory first: an item this process took (also while the store did not answer) is not taken again, and of two
	// sends of one id at once exactly one goes on to the store.
	if !h.handedOver.first(manager+"\x00"+id, time.Now()) {
		return false
	}
	if s, ok := h.store.(handOverStore); ok {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		first, err := s.HandOverFirst(sctx, manager, id, handedOverFor)
		cancel()
		if err == nil {
			return first // an earlier hub process may have taken it
		}
		h.logf("outbox: [%s] %s: %v", manager, id, err)
	}
	return true
}

// forgetHandOver: manager's item id was not delivered after all (refused): one that comes again is tried again.
func (h *Hub) forgetHandOver(manager, id string) {
	if s, ok := h.store.(handOverStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.HandOverForget(ctx, manager, id)
		cancel()
	}
	h.handedOver.forget(manager + "\x00" + id)
}

// seenItems are the keys of what managers handed the hub lately, in memory.
type seenItems struct {
	mu    sync.Mutex
	at    map[string]time.Time
	order []string
}

// first records key and reports whether it is new (not handed over within handedOverFor).
func (s *seenItems) first(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at == nil {
		s.at = map[string]time.Time{}
	}
	for len(s.order) > 0 && (len(s.order) >= handedOverMost || now.Sub(s.at[s.order[0]]) > handedOverFor) {
		delete(s.at, s.order[0])
		s.order = s.order[1:]
	}
	if t, ok := s.at[key]; ok && now.Sub(t) <= handedOverFor {
		return false
	}
	if _, ok := s.at[key]; !ok {
		s.order = append(s.order, key)
	}
	s.at[key] = now
	return true
}

// forget drops key (its order entry goes when it is the oldest).
func (s *seenItems) forget(key string) {
	s.mu.Lock()
	delete(s.at, key)
	s.mu.Unlock()
}
