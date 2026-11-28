package node

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
)

const remoteFetchLimit = 2

// remoteFetchCoordinator limits network Git commands on one Node. Its zero
// value uses the default limit.
type remoteFetchCoordinator struct {
	mu    sync.Mutex
	slots chan struct{}
	turns map[string]*remoteFetchTurn
}

type remoteFetchTurn struct {
	slot  chan struct{}
	users int // holders and waiters, including callers waiting for a global slot
}

// run takes the URL turn before a global slot. A caller waiting for the same
// upstream cannot occupy a slot that another upstream could use.
func (c *remoteFetchCoordinator) run(ctx context.Context, cloneURL string, command func(context.Context) (string, error)) (string, error) {
	key := remoteFetchKey(cloneURL)
	if key == "" {
		return command(ctx)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	c.mu.Lock()
	if c.slots == nil {
		c.slots = make(chan struct{}, remoteFetchLimit)
	}
	slots := c.slots
	if c.turns == nil {
		c.turns = make(map[string]*remoteFetchTurn)
	}
	turn := c.turns[key]
	if turn == nil {
		turn = &remoteFetchTurn{slot: make(chan struct{}, 1)}
		c.turns[key] = turn
	}
	turn.users++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		turn.users--
		if turn.users == 0 {
			delete(c.turns, key)
		}
		c.mu.Unlock()
	}()

	select {
	case turn.slot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-turn.slot }()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-slots }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return command(ctx)
}

// remoteFetchKey is only for coordination. Git receives the original address.
// A local path or file URL has no network key.
func remoteFetchKey(cloneURL string) string {
	if filepath.IsAbs(cloneURL) || filepath.VolumeName(cloneURL) != "" {
		return ""
	}
	if !strings.Contains(cloneURL, "://") {
		// Git also accepts SSH addresses in [user@]host:path form.
		host, path, ok := strings.Cut(cloneURL, ":")
		if !ok || host == "" || strings.ContainsAny(host, "/\\") || path == "" || strings.HasPrefix(path, ":") {
			return ""
		}
		cloneURL = "ssh://" + host + "/" + strings.TrimPrefix(path, "/")
	}
	u, err := url.Parse(cloneURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	var defaultPort string
	switch u.Scheme {
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	case "ssh":
		defaultPort = "22"
	case "git":
		defaultPort = "9418"
	default:
		return ""
	}
	host := strings.ToLower(u.Host)
	if u.Port() == defaultPort {
		host = strings.TrimSuffix(host, ":"+defaultPort)
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		path = path[:len(path)-len(".git")]
	}
	return u.Scheme + "://" + host + path
}
