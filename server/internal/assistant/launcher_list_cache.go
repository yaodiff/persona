package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// launcherListCacheTTL is how long a machine's ordinary launchers.list answer is reused by the manager
// before querying its node again (matching the node's 1-minute shell cache and models_quota.go's quotaFresh).
var launcherListCacheTTL = time.Minute

type launcherListCacheKey struct {
	machine string
	host    string
}

type launcherListEntry struct {
	raw         json.RawMessage
	at          time.Time
	connectedAt time.Time
}

type launcherListFlight struct {
	id        uint64
	gen       uint64
	allGen    uint64
	ver       uint64
	connAt    time.Time
	waiters   int
	abandoned bool
	cancel    context.CancelFunc
}

type launcherListCache struct {
	mu         sync.Mutex
	entries    map[launcherListCacheKey]launcherListEntry
	entryVer   map[launcherListCacheKey]uint64
	nextSeq    map[launcherListCacheKey]uint64
	storedSeq  map[launcherListCacheKey]uint64
	refreshing map[launcherListCacheKey]int
	gen        map[string]uint64
	allGen     uint64
	flights    map[launcherListCacheKey]*launcherListFlight
	nextFlight uint64
	group      singleflight.Group

	now           func() time.Time
	onFlightEnter func(launcherListCacheKey) // test hook: called after a caller enters singleflight
}

type launcherListReadOpts struct {
	host       string
	refresh    bool // explicit forced refresh: sends refresh:true, bypasses cache, backfills cache on success
	bypassRead bool // bypass positive cache without sending refresh:true (used by zshrc snapshot refresh)
}

func (c *launcherListCache) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *launcherListCache) getLocked(key launcherListCacheKey, connAt time.Time) (json.RawMessage, bool) {
	if launcherListCacheTTL <= 0 || c.entries == nil {
		return nil, false
	}
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !entry.connectedAt.Equal(connAt) || c.nowTime().Sub(entry.at) >= launcherListCacheTTL {
		delete(c.entries, key)
		return nil, false
	}
	return entry.raw, true
}

func (c *launcherListCache) allocSeqLocked(key launcherListCacheKey) uint64 {
	if c.nextSeq == nil {
		c.nextSeq = map[launcherListCacheKey]uint64{}
	}
	c.nextSeq[key]++
	return c.nextSeq[key]
}

func (c *launcherListCache) storeLocked(key launcherListCacheKey, raw json.RawMessage, connAt time.Time, seq uint64) {
	if launcherListCacheTTL <= 0 || !json.Valid(raw) {
		return
	}
	if seq <= c.storedSeq[key] {
		return
	}
	if c.entries == nil {
		c.entries = map[launcherListCacheKey]launcherListEntry{}
	}
	if c.entryVer == nil {
		c.entryVer = map[launcherListCacheKey]uint64{}
	}
	if c.storedSeq == nil {
		c.storedSeq = map[launcherListCacheKey]uint64{}
	}
	c.entries[key] = launcherListEntry{
		raw:         raw,
		at:          c.nowTime(),
		connectedAt: connAt,
	}
	c.entryVer[key]++
	c.storedSeq[key] = seq
	delete(c.flights, key)
}

func (c *launcherListCache) invalidate(machine string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if machine == "" {
		c.allGen++
		clear(c.entries)
		clear(c.flights)
		return
	}
	if c.gen == nil {
		c.gen = map[string]uint64{}
	}
	c.gen[machine]++
	for k := range c.entries {
		if k.machine == machine {
			delete(c.entries, k)
		}
	}
	for k := range c.flights {
		if k.machine == machine {
			delete(c.flights, k)
		}
	}
}

func (s *Service) invalidateLauncherListCache(machine string) {
	s.models.launcherLists.invalidate(machine)
	if machine != "" {
		if canon := s.canonical(machine); canon != "" && canon != machine {
			s.models.launcherLists.invalidate(canon)
		}
		if phys := s.physicalDevices(); phys != nil {
			if canon := phys.Canonical(machine); canon != "" && canon != machine {
				s.models.launcherLists.invalidate(canon)
			}
		}
	}
}

func (s *Service) deviceConnectedAt(machine string) time.Time {
	if s.devs == nil {
		return time.Time{}
	}
	return s.devs.ConnectedAt(machine)
}

func (s *Service) fetchLauncherListRaw(ctx context.Context, machine, host string, refresh bool) (json.RawMessage, error) {
	params := map[string]any{}
	if refresh {
		params["refresh"] = true
	}
	if host != "" {
		params["host"] = host
	}
	var raw json.RawMessage
	if err := s.call(ctx, machine, DeviceLaunchersList, params, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return raw, nil
}

func unmarshalLauncherList(raw json.RawMessage, out any) error {
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func launcherListFetchTimeout(ctx context.Context) time.Duration {
	timeout := launcherSyncTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > timeout {
			timeout = rem
		}
	}
	return timeout
}

// cachedLauncherList reads a machine's launchers.list response through the manager's per-machine
// short-TTL cache and singleflight group. Explicit refreshes bypass the positive cache, forward
// refresh:true to the node, and backfill the cache on success. Failed or timed-out reads are never cached.
func (s *Service) cachedLauncherList(ctx context.Context, machine string, opts launcherListReadOpts, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := &s.models.launcherLists
	cacheMachine := s.canonical(machine)
	if cacheMachine == "" {
		cacheMachine = machine
	}
	key := launcherListCacheKey{machine: cacheMachine, host: opts.host}
	connAt := s.deviceConnectedAt(cacheMachine)

	if opts.refresh || opts.bypassRead {
		c.mu.Lock()
		gen, allGen := c.gen[cacheMachine], c.allGen
		seq := c.allocSeqLocked(key)
		if opts.refresh {
			if c.refreshing == nil {
				c.refreshing = map[launcherListCacheKey]int{}
			}
			c.refreshing[key]++
		}
		c.mu.Unlock()
		if opts.refresh {
			defer func() {
				c.mu.Lock()
				c.refreshing[key]--
				if c.refreshing[key] <= 0 {
					delete(c.refreshing, key)
				}
				c.mu.Unlock()
			}()
		}
		raw, err := s.fetchLauncherListRaw(ctx, machine, opts.host, opts.refresh)
		if err != nil {
			return err
		}
		if json.Valid(raw) {
			c.mu.Lock()
			if c.gen[cacheMachine] == gen && c.allGen == allGen &&
				s.deviceConnectedAt(cacheMachine).Equal(connAt) &&
				(opts.refresh || c.refreshing[key] == 0) {
				c.storeLocked(key, raw, connAt, seq)
			}
			c.mu.Unlock()
		}
		return unmarshalLauncherList(raw, out)
	}

	c.mu.Lock()
	if raw, hit := c.getLocked(key, connAt); hit {
		c.mu.Unlock()
		return unmarshalLauncherList(raw, out)
	}
	f := c.flights[key]
	if f != nil && !f.connAt.Equal(connAt) {
		delete(c.flights, key)
		f = nil
	}
	if f == nil {
		c.nextFlight++
		f = &launcherListFlight{
			id:     c.nextFlight,
			gen:    c.gen[cacheMachine],
			allGen: c.allGen,
			ver:    c.entryVer[key],
			connAt: connAt,
		}
		if c.flights == nil {
			c.flights = map[launcherListCacheKey]*launcherListFlight{}
		}
		c.flights[key] = f
	}
	f.waiters++
	sfKey := fmt.Sprintf("%s\x00%s\x00%d", key.machine, key.host, f.id)
	onEnter := c.onFlightEnter
	c.mu.Unlock()

	timeout := launcherListFetchTimeout(ctx)
	detachedBase := context.WithoutCancel(ctx)
	ch := c.group.DoChan(sfKey, func() (any, error) {
		c.mu.Lock()
		if f.abandoned {
			if c.flights[key] == f {
				delete(c.flights, key)
			}
			c.mu.Unlock()
			return nil, context.Canceled
		}
		if raw, hit := c.getLocked(key, f.connAt); hit && c.gen[cacheMachine] == f.gen && c.allGen == f.allGen {
			if c.flights[key] == f {
				delete(c.flights, key)
			}
			c.mu.Unlock()
			return raw, nil
		}
		fetchCtx, cancel := context.WithTimeout(detachedBase, timeout)
		f.cancel = cancel
		seq := c.allocSeqLocked(key)
		c.mu.Unlock()
		defer cancel()

		raw, err := s.fetchLauncherListRaw(fetchCtx, machine, opts.host, false)
		c.mu.Lock()
		if c.flights[key] == f {
			delete(c.flights, key)
		}
		if err == nil && json.Valid(raw) && !f.abandoned &&
			c.gen[cacheMachine] == f.gen && c.allGen == f.allGen && c.entryVer[key] == f.ver &&
			c.refreshing[key] == 0 &&
			s.deviceConnectedAt(cacheMachine).Equal(f.connAt) {
			c.storeLocked(key, raw, f.connAt, seq)
		}
		c.mu.Unlock()
		return raw, err
	})
	if onEnter != nil {
		onEnter(key)
	}

	select {
	case res := <-ch:
		c.mu.Lock()
		f.waiters--
		c.mu.Unlock()
		if res.Err != nil {
			if ctx.Err() != nil && errors.Is(res.Err, context.Canceled) {
				return ctx.Err()
			}
			return res.Err
		}
		raw, _ := res.Val.(json.RawMessage)
		return unmarshalLauncherList(raw, out)
	case <-ctx.Done():
		c.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.abandoned = true
			if f.cancel != nil {
				f.cancel()
			}
			if c.flights[key] == f {
				delete(c.flights, key)
			}
		}
		c.mu.Unlock()
		return ctx.Err()
	}
}
