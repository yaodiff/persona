package extmgr

import (
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/extwire"
)

// One bounded FIFO keeps snapshot/delta order without holding session locks over
// the bus writer. A stalled hub cannot stall the extension's source reader.
type pageDelivery struct {
	mu      sync.Mutex
	stopped bool
	packets chan extwire.UITarget
	done    chan struct{}
	bytes   atomic.Int64
}

func (h *Manager) pageEmit(w extwire.UIPrincipal, frame json.RawMessage) bool {
	if h.opts.Hub == nil || w.Connection == "" {
		return true
	}
	if len(frame) > 1<<20 {
		return false
	}
	q := &h.uiDelivery
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return false
	}
	if q.packets == nil {
		q.packets = make(chan extwire.UITarget, 64)
		q.done = make(chan struct{})
		packets, done := q.packets, q.done
		go func() {
			for {
				select {
				case <-done:
					return
				case packet := <-packets:
					_ = h.opts.Hub.Notify(extwire.EventUIStream, packet)
					q.bytes.Add(-int64(len(packet.Envelope)))
				}
			}
		}()
	}
	if q.bytes.Add(int64(len(frame))) > 4<<20 {
		q.bytes.Add(-int64(len(frame)))
		q.mu.Unlock()
		return false
	}
	select {
	case q.packets <- extwire.UITarget{Who: w, Envelope: frame}:
		q.mu.Unlock()
		return true
	default:
		q.bytes.Add(-int64(len(frame)))
		q.mu.Unlock()
		return false
	}
}
func (h *Manager) pageStopDelivery() {
	q := &h.uiDelivery
	q.mu.Lock()
	if !q.stopped {
		q.stopped = true
		if q.done != nil {
			close(q.done)
		}
	}
	q.mu.Unlock()
}
