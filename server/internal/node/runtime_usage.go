package node

import (
	"container/list"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

const (
	runtimeUsageRetention  = 30 * time.Minute
	runtimeUsageMaxIDs     = 65536
	runtimeUsageMaxCount   = 1_000_000_000
	runtimeUsageMaxPending = 4096
)

// Runtime usage is a per-request delta, without a replayable local transcript.
// Pending buckets share the file meter's clock and request IDs are scoped to a
// runtime connection.
type runtimeUsageReports struct {
	mu      sync.Mutex
	seen    map[runtimeUsageRequest]struct{}
	order   list.List
	pending map[usageKey]proto.UsageBucket
}
type runtimeUsageRequest struct {
	serial           int
	session, request string
}
type runtimeUsageSeen struct {
	key runtimeUsageRequest
	at  time.Time
}

type runtimeUsageReport struct {
	SessionID  string    `json:"session_id"`
	TS         time.Time `json:"ts"`
	Model      string    `json:"model"`
	RequestID  string    `json:"request_id"`
	Input      int64     `json:"input"`
	Output     int64     `json:"output"`
	CacheRead  int64     `json:"cache_read"`
	CacheWrite int64     `json:"cache_write"`
	Reasoning  int64     `json:"reasoning"`
}

func (n *Node) runtimeUsageReport(h *runtimeConn, params json.RawMessage, now time.Time) {
	var p runtimeUsageReport
	if json.Unmarshal(params, &p) != nil || n.peer == nil || n.runtimes == nil {
		return
	}
	minute := p.TS.Truncate(time.Minute)
	if minute.Before(now.Truncate(time.Minute).Add(-usageWindow)) || p.TS.After(now.Add(time.Minute)) || p.Model == "" || len(p.Model) > 256 || len(p.RequestID) > 512 {
		return
	}
	// Real requests fit comfortably below this ceiling. Reasoning is already in output.
	total := int64(0)
	for _, v := range []int64{p.Input, p.Output, p.CacheRead, p.CacheWrite} {
		if v < 0 || v > runtimeUsageMaxCount {
			return
		}
		total += v
	}
	if total == 0 || p.Reasoning < 0 || p.Reasoning > p.Output {
		return
	}
	agent := n.runtimes.usageAgent(h, p.SessionID)
	if !runtimeIDRe.MatchString(agent) || tokenuse.FileMetered(agent) {
		return
	}
	st, _ := n.state.get(p.SessionID)
	dir := ""
	if dirs := n.usageDirs.get(); dirs != nil { // a manager that keeps directories (usage_dirs.go)
		cwd := st.Cwd
		if cwd == "" && n.runtimes != nil {
			if row, _, ok := n.runtimes.rowOf(p.SessionID); ok {
				cwd = row.Cwd
			}
		}
		dir = n.usageDirKey(cwd, dirs.temps)
		dirs.want(dir)
	}
	u := &n.runtimeUsage
	u.mu.Lock()
	defer u.mu.Unlock()
	bucketKey := usageKey{minute.Unix(), p.Model, st.Launcher, agent, dir}
	if _, exists := u.pending[bucketKey]; !exists && len(u.pending) >= runtimeUsageMaxPending {
		return
	}
	if p.RequestID != "" {
		cutoff := now.Add(-runtimeUsageRetention)
		for first := u.order.Front(); first != nil && !first.Value.(runtimeUsageSeen).at.After(cutoff); first = u.order.Front() {
			delete(u.seen, u.order.Remove(first).(runtimeUsageSeen).key)
		}
		key := runtimeUsageRequest{h.serial, p.SessionID, p.RequestID}
		if _, ok := u.seen[key]; ok {
			return
		}
		if len(u.seen) == runtimeUsageMaxIDs {
			delete(u.seen, u.order.Remove(u.order.Front()).(runtimeUsageSeen).key)
		}
		if u.seen == nil {
			u.seen = map[runtimeUsageRequest]struct{}{}
		}
		u.seen[key] = struct{}{}
		u.order.PushBack(runtimeUsageSeen{key, now})
	}
	if u.pending == nil {
		u.pending = map[usageKey]proto.UsageBucket{}
	}
	b := u.pending[bucketKey]
	b.Minute, b.Model, b.Launcher, b.Agent, b.Dir = bucketKey.minute, bucketKey.model, bucketKey.launcher, bucketKey.agent, bucketKey.dir
	b.Input += p.Input
	b.Output += p.Output
	b.CacheRead += p.CacheRead
	b.CacheWrite += p.CacheWrite
	b.Requests++
	u.pending[bucketKey] = b
}

func (n *Node) flushRuntimeUsage() {
	u := &n.runtimeUsage
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.pending) > 0 && n.peer != nil {
		buckets := slices.Collect(maps.Values(u.pending))
		if n.peer.Notify("usage", map[string]any{"add": true, "buckets": buckets}) == nil {
			clear(u.pending)
		}
	}
}

// Resolve ownership, the listed row and its authorized attribution at one registry snapshot.
func (b *runtimeRegistry) usageAgent(h *runtimeConn, id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.owner[id] != h {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	defaultAgent := h.hello.Agent
	if defaultAgent == "" {
		defaultAgent = h.hello.Runtime
	}
	for _, row := range h.sessions {
		if row.SessionID != id {
			continue
		}
		if row.Agent == defaultAgent {
			return row.Agent
		}
		for _, launcher := range h.hello.Launchers {
			if launcher.Agent == row.Agent {
				return row.Agent
			}
		}
	}
	return ""
}
