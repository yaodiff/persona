package hub

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// What a manager made while the hub was away comes with manager.outbox once it is back (docs/operations/hub-restart.md "The
// manager while the hub is away"): its pushes and the apps' live updates, oldest first, after it reported its state
// (its cards first: a card's push needs the card). The hub delivers each as it would have then, only about the
// manager's own machines, sessions and cards (deliverPushFrom, relayBroadcastFrom), and each once: a manager that did
// not hear the answer sends it again, and a push it sent live may come again from its outbox when the connection
// dropped before the answer. What was handed over is kept in the database (handed_over), so the next hub process does
// not deliver it again either.

func init() {
	handleTypedBus(assistantwire.OutboxMethod, assistantwire.MethodOutbox+": ", func(h *Hub, ctx context.Context, c *busConn, p assistantwire.Outbox) (assistantwire.OutboxResult, error) {
		from := c.managerID()
		out := assistantwire.OutboxResult{Done: []string{}}
		for _, it := range p.Items {
			if ctx.Err() != nil {
				break // the rest come again
			}
			if it.ID != "" {
				first, release, paused := h.beginHandOver(ctx, from, it.ID)
				if paused {
					break // writes are paused: leave this and the rest for retry after resume
				}
				if first {
					var err error
					switch {
					case it.Notification != nil:
						_, err = h.deliverPushFrom(from, *it.Notification)
					case len(it.Message) > 0:
						err = h.relayBroadcastFrom(from, it.Message)
					}
					if err != nil {
						h.forgetHandOver(from, it.ID, release != nil)
						h.logf("outbox: [%s] %s: %v (not delivered)", from, it.ID, err)
					}
				}
				if release != nil {
					release()
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

// beginHandOver records manager's item id and reports whether it is new. When a durable store is configured, it
// acquires one write-quiesce turn before mutating in-memory state and returns release for the caller to hold across
// delivery and any forgetHandOver settlement. When writes are paused, paused is true and neither memory nor the store
// is mutated, so the caller leaves the item for retry after resume. A store that does not answer: memory decides (the
// item is delivered rather than lost).
func (h *Hub) beginHandOver(ctx context.Context, manager, id string) (first bool, release func(), paused bool) {
	s, hasStore := h.store.(handOverStore)
	if hasStore {
		if !h.quiesce.begin() {
			return false, nil, true
		}
		release = h.quiesce.end
	}
	// Memory first: an item this process took (also while the store did not answer) is not taken again, and of two
	// sends of one id at once exactly one goes on to the store.
	if !h.handedOver.first(manager+"\x00"+id, time.Now()) {
		if release != nil {
			release()
		}
		return false, nil, false
	}
	if hasStore {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		storeFirst, err := s.HandOverFirst(sctx, manager, id, handedOverFor)
		cancel()
		if err == nil {
			if !storeFirst {
				release()
				return false, nil, false // an earlier hub process took it
			}
			return true, release, false
		}
		h.logf("outbox: [%s] %s: %v", manager, id, err)
	}
	return true, release, false
}

// forgetHandOver: manager's item id was not delivered after all (refused): one that comes again is tried again.
// held reports whether the caller already holds the write-quiesce turn from beginHandOver.
func (h *Hub) forgetHandOver(manager, id string, held bool) {
	if s, ok := h.store.(handOverStore); ok {
		if held || h.quiesce.begin() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.HandOverForget(ctx, manager, id)
			cancel()
			if !held {
				h.quiesce.end()
			}
		}
	}
	h.handedOver.forget(manager + "\x00" + id)
}

type seenEntry struct {
	key string
	at  time.Time
}

// seenItems are the keys of what managers handed the hub lately, in memory.
type seenItems struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
}

// first records key and reports whether it is new (not handed over within handedOverFor).
func (s *seenItems) first(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]*list.Element{}
	}
	for front := s.order.Front(); front != nil; front = s.order.Front() {
		e := front.Value.(seenEntry)
		if now.Sub(e.at) <= handedOverFor {
			break
		}
		s.order.Remove(front)
		delete(s.entries, e.key)
	}
	if el, ok := s.entries[key]; ok {
		e := el.Value.(seenEntry)
		if now.Sub(e.at) <= handedOverFor {
			return false
		}
		s.order.Remove(el)
		delete(s.entries, key)
	}
	for s.order.Len() >= handedOverMost {
		front := s.order.Front()
		if front == nil {
			break
		}
		e := front.Value.(seenEntry)
		s.order.Remove(front)
		delete(s.entries, e.key)
	}
	s.entries[key] = s.order.PushBack(seenEntry{key: key, at: now})
	return true
}

// forget drops key immediately from both the lookup map and the eviction order.
func (s *seenItems) forget(key string) {
	s.mu.Lock()
	if el, ok := s.entries[key]; ok {
		s.order.Remove(el)
		delete(s.entries, key)
	}
	s.mu.Unlock()
}
