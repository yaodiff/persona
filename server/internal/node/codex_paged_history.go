package node

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

// codexPagedSQLiteBackoff bounds how long an absent sqlite3 CLI suppresses the paginated read for one home. It stays at
// the database refresh interval: short enough that installing sqlite3 is noticed promptly, long enough that a list pass
// and its fresh history refreshes do not re-Glob the home and re-run PATH lookup for every session.
const codexPagedSQLiteBackoff = codexAppDBRefresh

// codexPagedNow is indirected so the cache TTL and negative backoff can be advanced without sleeping in tests.
var codexPagedNow = time.Now

// Paginated native threads keep their authoritative turns in the history database even while a rollout remains.
// A revert starts another rollout segment. Reading that segment alone loses the retained prefix.
var codexPagedDBs = struct {
	sync.Mutex
	byHome map[string]*codexAppDB
}{byHome: map[string]*codexAppDB{}}

func codexPaginatedThreads(home string) map[string]codexAppThread {
	return codexPaginatedThreadsMode(home, false)
}

// codexPaginatedThreadsFresh re-reads state_*.sqlite immediately when its file stamp changed on disk,
// while still returning cached rows with no SQLite query when the database files are unchanged.
func codexPaginatedThreadsFresh(home string) map[string]codexAppThread {
	return codexPaginatedThreadsMode(home, true)
}

func codexPaginatedThreadsMode(home string, fresh bool) map[string]codexAppThread {
	codexPagedDBs.Lock()
	c := codexPagedDBs.byHome[home]
	if c == nil {
		c = &codexAppDB{}
		codexPagedDBs.byHome[home] = c
	}
	now := codexPagedNow()
	// An absent sqlite3 CLI is shared by every read of this home: back off even fresh callers, which otherwise bypass the
	// refresh TTL and re-run the path Globs and LookPath on every high-frequency history refresh.
	if now.Before(c.sqliteMissingUntil) {
		rows := c.byID
		codexPagedDBs.Unlock()
		return rows
	}
	// TTL first, before either numbered-database Glob: a non-fresh call inside the refresh interval does no filesystem
	// work at all (codexAppThreads orders its fast path the same way).
	if !fresh && now.Sub(c.at) < codexAppDBRefresh {
		rows := c.byID
		codexPagedDBs.Unlock()
		return rows
	}
	c.at = now // one caller looks; concurrent callers keep the last answer meanwhile
	last := c.byID
	codexPagedDBs.Unlock()

	if codexHistoryDBPath(home) == "" {
		return nil
	}
	db := codexAppDBPath(home)
	if db == "" {
		return nil
	}
	stamp := fileStamp(db) + fileStamp(db+"-wal")
	codexPagedDBs.Lock()
	if c.stamp == stamp { // files did not change: keep the cached rows without a SQLite read, even for a fresh caller
		rows := c.byID
		codexPagedDBs.Unlock()
		return rows
	}
	codexPagedDBs.Unlock()
	rows, err := readCodexThreads(context.Background(), db, `archived = 0 AND history_mode = 'paginated'`)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			codexPagedDBs.Lock()
			c.sqliteMissingUntil = codexPagedNow().Add(codexPagedSQLiteBackoff)
			codexPagedDBs.Unlock()
		}
		return last // older schemas retain their existing rollout and App-history paths
	}
	for id, row := range rows {
		row.Paginated = true
		rows[id] = row
	}
	codexPagedDBs.Lock()
	c.byID, c.stamp, c.sqliteMissingUntil = rows, stamp, time.Time{}
	codexPagedDBs.Unlock()
	return rows
}

func codexPaginatedThreadOf(src codexSource) (codexAppThread, bool) {
	t, ok := codexPaginatedThreads(src.home)[src.thread]
	return t, ok
}

func invalidateCodexPaginated(home string) {
	codexPagedDBs.Lock()
	delete(codexPagedDBs.byHome, home)
	codexPagedDBs.Unlock()
	rolloutCaches.Lock()
	c, ok := rolloutCaches.byHome[home]
	rolloutCaches.Unlock()
	if ok {
		c.mu.Lock()
		c.at, c.olderAt = time.Time{}, time.Time{}
		c.mu.Unlock()
	}
}
