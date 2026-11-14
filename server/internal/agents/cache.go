package agents

import (
	"strings"
	"sync"
	"time"
)

// cache keeps each source's session list (for canSend checks and change detection) and the latest message page
// of recently opened sessions; agent-bridge.updated and the poll drop exactly the sessions that changed.
type cache struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]sessionsEntry // source → list
	latest   map[string]*latestEntry  // public id → newest page
	// gen counts each source's forgets: what was asked of it before one is not put back (a list or page answered
	// late by a removed source must not outlive it).
	gen map[string]int
	// Called under mu so a late response cannot overwrite a removed or invalidated
	// source's index. The callback never calls back into the cache.
	onSessions func(source string, list []Session)
}

type sessionsEntry struct {
	list []Session
	at   time.Time
}

type latestEntry struct {
	page  *MessagePage
	limit int
	at    time.Time
}

func newCache(ttl time.Duration) *cache {
	return &cache{ttl: ttl, sessions: map[string]sessionsEntry{}, latest: map[string]*latestEntry{}, gen: map[string]int{}}
}

func (c *cache) getSessions(source string) ([]Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.sessions[source]
	return e.list, ok && time.Since(e.at) < c.ttl
}

// generation is source's generation now: pass it to the put of what is asked of the source next.
func (c *cache) generation(source string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen[source]
}

func (c *cache) putSessions(source string, gen int, list []Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen[source] != gen {
		return
	}
	c.sessions[source] = sessionsEntry{list: list, at: time.Now()}
	if c.onSessions != nil {
		c.onSessions(source, list)
	}
}

func (c *cache) getLatest(id string, limit int) (*MessagePage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.latest[id]
	if e == nil || e.limit != limit || time.Since(e.at) >= c.ttl {
		return nil, false
	}
	return e.page, true
}

func (c *cache) putLatest(source string, gen int, id string, limit int, p *MessagePage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen[source] != gen {
		return
	}
	if len(c.latest) > 200 { // a bound, not an LRU: dropping all is fine for a cache this cheap to refill
		c.latest = map[string]*latestEntry{}
	}
	c.latest[id] = &latestEntry{page: p, limit: limit, at: time.Now()}
}

// invalidate drops the session list of source and the cached pages of the given ids.
func (c *cache) invalidate(source string, ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateLocked(source, ids)
}

func (c *cache) invalidateLocked(source string, ids []string) {
	c.gen[source]++ // reject in-flight lists fetched before this change
	delete(c.sessions, source)
	for _, id := range ids {
		delete(c.latest, id)
	}
}

// putChanged is invalidate for a change found in list itself (the poll's, asked at gen): the pages of ids go and
// lists asked before it are rejected, but list stays as source's list, so the re-reads the change's hint brings find
// it instead of each asking the source again. A change reported while list was being asked (gen moved) may be newer
// than list, which is then dropped as invalidate would.
func (c *cache) putChanged(source string, gen int, list []Session, ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.gen[source] == gen
	c.invalidateLocked(source, ids)
	if !current {
		return
	}
	c.sessions[source] = sessionsEntry{list: list, at: time.Now()}
	if c.onSessions != nil {
		c.onSessions(source, list)
	}
}

// forget drops everything cached of source: its list and its sessions' pages.
func (c *cache) forget(source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen[source]++
	delete(c.sessions, source)
	if c.onSessions != nil {
		c.onSessions(source, nil)
	}
	for id := range c.latest {
		if strings.HasPrefix(id, source+":") {
			delete(c.latest, id)
		}
	}
}
