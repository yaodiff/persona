package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The manager while the hub is away (docs/operations/hub-restart.md "The manager while the hub is away"): it goes on with its
// own work, and the pushes and the apps' live updates it decides on are kept in its database (manager_outbox) instead
// of lost, and handed to the hub in order once it is back (manager.outbox), after it reported its state on the new
// connection. What the hub shows of the manager (the snapshot, the lists, the cards) is reported whole on every
// connection anyway and is not kept here.

// OutboxStore keeps the outbox (db.DB); nil: nothing is kept while the hub is away (tests, a manager without one).
type OutboxStore interface {
	OutboxAdd(ctx context.Context, it db.OutboxItem, keep int, staleBefore time.Time) error
	OutboxItems(ctx context.Context, limit int) ([]db.OutboxItem, error)
	OutboxCount(ctx context.Context) (int, error)
	OutboxDone(ctx context.Context, ids []string) error
}

const (
	outboxPush      = "push"
	outboxBroadcast = "broadcast"
)

var (
	// pushesKept and broadcastsKept bound the outbox, each kind on its own: a hub away for long gets the newest.
	pushesKept     = 1000
	broadcastsKept = 200
	// outboxBatch and outboxBatchBytes bound one manager.outbox.
	outboxBatch      = 100
	outboxBatchBytes = 4 << 20
	// pushKeep: a push older than this is no news any more and is not handed over.
	pushKeep = 24 * time.Hour
	// broadcastKeep: an apps' live update older than this is not handed over. The apps of a hub back after longer
	// start a new stream and load everything again (docs/operations/hub-restart.md "Event resume": the hand-over is taken for 10
	// minutes), and an old update arriving after that would only take them back.
	broadcastKeep = 10 * time.Minute
	// outboxRetry is how long the outbox waits after a hand-over that failed with the hub connected.
	outboxRetry = 5 * time.Second
	// outboxWrite bounds keeping one item: a database that does not answer does not hold the caller.
	outboxWrite = 5 * time.Second
)

// outbox is BusHub's part of it.
type outbox struct {
	store OutboxStore
	// waiting: items kept and not handed over yet (as far as this process knows); while there are, new ones are kept
	// too, behind them, so the hub gets everything in the order it was made.
	waiting atomic.Int64
	kick    chan struct{}
	// legacy is the connection whose hub answered manager.outbox unknown_method (a hub from before it): on it,
	// items go one by one.
	legacy atomic.Pointer[rpc.Peer]
	mu     sync.Mutex // one hand-over at a time
}

func newOutbox(s OutboxStore) *outbox {
	if s == nil {
		return nil
	}
	return &outbox{store: s, kick: make(chan struct{}, 1)}
}

// report is one connection's "the manager reported its state" (onHello's parts): the outbox waits for it.
type report struct {
	sync.WaitGroup
	done chan struct{}
}

// newReport starts the current connection's report; seal ends adding to it.
func (b *BusHub) newReport() *report {
	r := &report{done: make(chan struct{})}
	b.mu.Lock()
	b.reported = r
	b.mu.Unlock()
	return r
}

func (r *report) seal() {
	go func() {
		r.Wait()
		close(r.done)
	}()
}

// reportedNow is closed once the current connection's report is done (at once before any hello: nothing to wait for).
func (b *BusHub) reportedNow() <-chan struct{} {
	b.mu.Lock()
	r := b.reported
	b.mu.Unlock()
	if r == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return r.done
}

// answered: err is the hub's own answer about it (a coded refusal): sending it again would get the same. Anything
// else (the bus down or going down, a call that timed out, a write that failed, a connection the hub no longer takes
// calls on) may not have reached the hub, or not been looked at.
func answered(err error) bool {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code != bus.CodeStaleConnection
	}
	var ce *codedError
	return errors.As(err, &ce)
}

// keep puts it in the outbox; false when it could not (the caller then reports the error it had). A body that is
// not JSON is not kept: it could never be handed over, and would hold everything behind it.
func (b *BusHub) keep(ctx context.Context, id, kind string, body []byte) bool {
	o := b.out
	if !json.Valid(body) {
		b.opts.Logf("outbox: %s: not JSON, not kept", id)
		return false
	}
	keep := pushesKept
	if kind == outboxBroadcast {
		keep = broadcastsKept
	}
	now := time.Now()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outboxWrite)
	defer cancel()
	if err := o.store.OutboxAdd(wctx, db.OutboxItem{ID: id, Kind: kind, Body: body, CreatedAt: now}, keep, now.Add(-broadcastKeep)); err != nil {
		b.opts.Logf("outbox: keep %s: %v", id, err)
		return false
	}
	o.waiting.Add(1)
	select {
	case o.kick <- struct{}{}:
	default:
	}
	return true
}

// queued: new items go behind kept ones (or the hub is away).
func (b *BusHub) queued() bool { return b.out.waiting.Load() > 0 || !b.Connected() }

// sendPush is notify.request, kept in the outbox while the hub is away (or while older ones wait there), and when the
// call may not have reached it: the hub takes each notification id once.
func (b *BusHub) sendPush(ctx context.Context, p NotifyRequest) (NotifyResult, error) {
	o, id := b.out, p.Notification.ID
	var raw []byte
	if o != nil && id != "" {
		raw, _ = json.Marshal(p.Notification)
	}
	if raw != nil && b.queued() && b.keep(ctx, id, outboxPush, raw) {
		return NotifyResult{}, nil
	}
	var res NotifyResult
	err := busError(b.client.Call(ctx, assistantwire.MethodNotify, p, &res))
	if err != nil && !answered(err) && raw != nil && b.keep(ctx, id, outboxPush, raw) {
		return NotifyResult{}, nil
	}
	return res, err
}

// sendBroadcast is ws.broadcast, kept in the outbox while the hub is away (or while older ones wait there), and when
// it could not be written.
func (b *BusHub) sendBroadcast(ctx context.Context, m Broadcast) error {
	o := b.out
	if o != nil && b.queued() && b.keep(ctx, "bc_"+randomHex(8), outboxBroadcast, m.Message) {
		return nil
	}
	err := b.client.Notify(assistantwire.EventBroadcast, m)
	if err != nil && o != nil && b.keep(ctx, "bc_"+randomHex(8), outboxBroadcast, m.Message) {
		return nil
	}
	return err
}

// runOutbox hands the outbox over whenever the hub is connected, the manager reported its state there and something
// waits, until ctx ends.
func (b *BusHub) runOutbox(ctx context.Context) {
	o := b.out
	if o == nil {
		return
	}
	if n, err := o.store.OutboxCount(ctx); err == nil { // left by an earlier run of this manager
		o.waiting.Add(int64(n))
	}
	for {
		if b.client.Wait(ctx) != nil {
			return
		}
		down := b.down()
		select {
		case <-ctx.Done():
			return
		case <-down:
			continue
		case <-b.reportedNow():
		}
		if err := b.handOver(ctx); err != nil && ctx.Err() == nil {
			b.opts.Logf("outbox: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(outboxRetry):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-o.kick:
		case <-down:
		}
	}
}

// down is closed when the connection that is up now drops (or at once while there is none).
func (b *BusHub) down() <-chan struct{} {
	if p := b.client.Peer(); p != nil {
		return p.Done()
	}
	c := make(chan struct{})
	close(c)
	return c
}

// handOver sends what the outbox has, oldest first, until it is empty or the hub cannot take it now.
func (b *BusHub) handOver(ctx context.Context) error {
	o := b.out
	o.mu.Lock()
	defer o.mu.Unlock()
	for {
		seen := o.waiting.Load() // before the read: one kept after it counts, and is not lost by the reset below
		items, err := o.store.OutboxItems(ctx, outboxBatch)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			o.waiting.CompareAndSwap(seen, 0)
			return nil
		}
		now := time.Now()
		var send []assistantwire.OutboxItem
		var stale []string
		size := 0
		for _, it := range items {
			w := assistantwire.OutboxItem{ID: it.ID, At: it.CreatedAt}
			keepFor := pushKeep
			switch it.Kind {
			case outboxPush:
				var n Notification
				if json.Unmarshal(it.Body, &n) != nil {
					stale = append(stale, it.ID)
					continue
				}
				w.Notification = &n
			default:
				w.Message, keepFor = json.RawMessage(it.Body), broadcastKeep
			}
			if now.Sub(it.CreatedAt) > keepFor {
				stale = append(stale, it.ID)
				continue
			}
			if size += len(it.Body); size > outboxBatchBytes && len(send) > 0 {
				break // the rest in the next batch
			}
			send = append(send, w)
		}
		done, err := b.deliverOutbox(ctx, send)
		if err := o.store.OutboxDone(ctx, append(stale, done...)); err != nil {
			return err
		}
		o.waiting.Add(-int64(len(stale) + len(done)))
		if err != nil {
			return err
		}
		if len(done) < len(send) {
			return errors.New("the hub took part of the outbox")
		}
	}
}

// deliverOutbox hands items to the hub: in one manager.outbox, or one by one to a hub from before it. It returns the
// ids the hub took.
func (b *BusHub) deliverOutbox(ctx context.Context, items []assistantwire.OutboxItem) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	o, peer := b.out, b.client.Peer()
	if peer == nil {
		return nil, bus.ErrOffline
	}
	if o.legacy.Load() != peer {
		var res assistantwire.OutboxResult
		err := peer.Call(ctx, assistantwire.MethodOutbox, assistantwire.Outbox{Items: items}, &res)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != bus.CodeUnknownMethod {
			return res.Done, err
		}
		o.legacy.Store(peer) // a hub from before manager.outbox: one by one on this connection
	}
	var done []string
	for _, it := range items {
		var err error
		if it.Notification != nil {
			err = busError(peer.Call(ctx, assistantwire.MethodNotify, NotifyRequest{Notification: *it.Notification}, nil))
		} else {
			err = peer.Notify(assistantwire.EventBroadcast, Broadcast{Message: it.Message})
		}
		if err != nil && !answered(err) {
			return done, err
		}
		if err != nil {
			b.opts.Logf("outbox: %s: %v (dropped)", it.ID, err)
		}
		done = append(done, it.ID)
	}
	return done, nil
}
