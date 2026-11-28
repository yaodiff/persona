package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Rejoining hundreds of Codex panes against a shared app-server must bound concurrency
// and share a single paginated thread/loaded/list query instead of storming the daemon (#13578).
func TestCodexJoinThrottlesConcurrencyAndSharesLoadedList(t *testing.T) {
	const totalThreads = 300
	n := remoteTestNode(t)
	_ = withFakeHub(t, n)
	home := canonicalCodexHome(filepath.Join(n.home, ".codex"), n.home)

	ids := make([]string, totalThreads)
	for i := range totalThreads {
		ids[i] = fmt.Sprintf("019d0000-0000-7000-8000-%012d", i+1)
	}

	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	var resumed atomic.Int32
	var listCallsDuringJoin atomic.Int32
	var counting atomic.Bool

	gate := make(chan struct{})
	firstWave := make(chan struct{})
	var waveOnce sync.Once
	var writeMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		sendReply := func(id json.RawMessage, result any) {
			b, _ := json.Marshal(map[string]any{"id": id, "result": result})
			writeMu.Lock()
			wctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			_ = conn.Write(wctx, websocket.MessageText, b)
			cancel()
			writeMu.Unlock()
		}
		for {
			_, b, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var m codexMessage
			if json.Unmarshal(b, &m) != nil || len(m.ID) == 0 {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(m.Params, &p)
			switch m.Method {
			case "initialize":
				sendReply(m.ID, map[string]any{"codexHome": home})
			case "thread/loaded/list":
				if counting.Load() {
					listCallsDuringJoin.Add(1)
				}
				const pageSize = 100
				start := 0
				if cur, _ := p["cursor"].(string); cur != "" {
					_, _ = fmt.Sscanf(cur, "p%d", &start)
				}
				end := min(start+pageSize, len(ids))
				res := map[string]any{"data": ids[start:end]}
				if end < len(ids) {
					res["nextCursor"] = fmt.Sprintf("p%d", end)
				}
				sendReply(m.ID, res)
			case "thread/read":
				tid, _ := p["threadId"].(string)
				sendReply(m.ID, map[string]any{"thread": map[string]any{
					"id": tid, "name": "review", "cwd": home,
					"status": map[string]any{"type": "idle", "activeFlags": []string{}},
				}})
			case "model/list":
				sendReply(m.ID, map[string]any{"data": []any{}})
			case "thread/resume":
				// Handle asynchronously so concurrent client RPCs truly overlap in flight.
				msgID := append(json.RawMessage(nil), m.ID...)
				tid, _ := p["threadId"].(string)
				go func() {
					cur := inFlight.Add(1)
					for {
						prev := maxInFlight.Load()
						if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
							break
						}
					}
					if cur >= 4 {
						waveOnce.Do(func() { close(firstWave) })
					}
					<-gate
					inFlight.Add(-1)
					resumed.Add(1)
					sendReply(msgID, map[string]any{"thread": map[string]any{
						"id": tid, "cwd": home,
						"status": map[string]any{"type": "idle", "activeFlags": []string{}},
					}})
				}()
			default:
				sendReply(m.ID, map[string]any{})
			}
		}
	}))
	t.Cleanup(srv.Close)

	_ = connectFixtureEndpoint(t, n, home, "daemon", "ws"+strings.TrimPrefix(srv.URL, "http"))

	n.mu.Lock()
	for i, tid := range ids {
		n.agents[tid] = host.Agent{
			Agent:       "codex",
			PaneID:      fmt.Sprintf("w1:p%d", i+1),
			Cwd:         home,
			AgentStatus: proto.StatusIdle,
		}
	}
	n.mu.Unlock()

	counting.Store(true)
	n.joinCodexPanes(context.Background())

	select {
	case <-firstWave:
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("timed out waiting for first wave of joins")
	}
	// Let any excess goroutines (in unthrottled code) hit thread/resume before releasing the gate,
	// without sleeping: wait until either >4 arrive or a single short timer expires on throttled code.
	release := time.NewTimer(25 * time.Millisecond)
	defer release.Stop()
	<-release.C
	close(gate)

	waitFor(t, "all 300 Codex panes joined", func() bool {
		return resumed.Load() == totalThreads
	})

	if got := maxInFlight.Load(); got > 4 {
		t.Fatalf("max concurrent background joins = %d, want <= 4", got)
	}
	// 300 threads with page size 100 is 3 RPC pages for one shared loaded-list query (allow at most 1 refresh overlap = 6).
	if lists := listCallsDuringJoin.Load(); lists > 6 {
		t.Fatalf("thread/loaded/list calls during 300 joins = %d, want <= 6 (shared paginated query)", lists)
	}
}

// Failed background joins use capped exponential backoff with jitter and reset on success (#13578).
func TestCodexJoinBackoffAndJitter(t *testing.T) {
	oldRetry, oldMax, oldJitter := codexJoinRetry, codexJoinRetryMax, codexJoinJitter
	codexJoinRetry = 10 * time.Second
	codexJoinRetryMax = 40 * time.Second
	codexJoinJitter = func(base time.Duration) time.Duration { return base / 5 } // +20% deterministic
	t.Cleanup(func() {
		codexJoinRetry, codexJoinRetryMax, codexJoinJitter = oldRetry, oldMax, oldJitter
	})

	if got := codexJoinBackoff(1); got != 12*time.Second {
		t.Fatalf("fails=1 backoff = %v, want 12s", got)
	}
	if got := codexJoinBackoff(2); got != 24*time.Second {
		t.Fatalf("fails=2 backoff = %v, want 24s", got)
	}
	if got := codexJoinBackoff(3); got != 48*time.Second {
		t.Fatalf("fails=3 backoff = %v, want 48s (40s cap + 20%% jitter)", got)
	}
	if got := codexJoinBackoff(6); got != 48*time.Second {
		t.Fatalf("fails=6 backoff = %v, want 48s", got)
	}

	var cx codexCards
	if !cx.startJoin("t1") {
		t.Fatal("initial startJoin returned false")
	}
	cx.endJoin("t1", context.DeadlineExceeded)
	if cx.startJoin("t1") {
		t.Fatal("startJoin succeeded immediately during backoff")
	}
	cx.mu.Lock()
	s := cx.joins["t1"]
	if s.fails != 1 || time.Until(s.nextAt) <= 10*time.Second {
		cx.mu.Unlock()
		t.Fatalf("unexpected join state after first failure: %+v", s)
	}
	s.nextAt = time.Now().Add(-time.Second)
	cx.joins["t1"] = s
	cx.mu.Unlock()

	if !cx.startJoin("t1") {
		t.Fatal("startJoin did not retry after backoff elapsed")
	}
	cx.endJoin("t1", nil)
	cx.mu.Lock()
	after := cx.joins["t1"]
	cx.mu.Unlock()
	if after.fails != 0 || after.busy || !after.nextAt.IsZero() {
		t.Fatalf("successful join did not reset backoff state: %+v", after)
	}
}
