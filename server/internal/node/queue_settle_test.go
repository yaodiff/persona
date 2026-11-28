package node

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type queuePushRecorder struct {
	mu       sync.Mutex
	sessions [][]proto.Session
	notify   chan struct{}
}

func attachQueuePushRecorder(t *testing.T, n *Node) *queuePushRecorder {
	t.Helper()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	rec := &queuePushRecorder{notify: make(chan struct{}, 32)}
	hub.OnEvent = func(event string, params json.RawMessage) {
		var payload struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if json.Unmarshal(params, &payload) != nil || len(payload.Sessions) == 0 {
			return
		}
		rec.mu.Lock()
		rec.sessions = append(rec.sessions, payload.Sessions)
		rec.mu.Unlock()
		select {
		case rec.notify <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() {
		cancel()
		_ = n2hW.Close()
		_ = h2nW.Close()
	})
	return rec
}

func (r *queuePushRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

func (r *queuePushRecorder) waitForQueued(t *testing.T, sid string, want []string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		for i := len(r.sessions) - 1; i >= 0; i-- {
			for _, s := range r.sessions[i] {
				if s.SessionID == sid && slices.Equal(s.Queued, want) {
					r.mu.Unlock()
					return
				}
			}
		}
		r.mu.Unlock()
		select {
		case <-r.notify:
		case <-deadline.C:
			t.Fatalf("timed out waiting for pushed queued=%v for %s", want, sid)
		}
	}
}

func (r *queuePushRecorder) waitPushPast(t *testing.T, before int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for r.count() <= before {
		select {
		case <-r.notify:
		case <-deadline.C:
			t.Fatalf("timed out waiting for session push (>%d, got %d)", before, r.count())
		}
	}
}

func appendTranscriptLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestEditQueueUsesFreshMetaAndReconcilesSettle(t *testing.T) {
	n, f, home := herdrNode(t)
	rec := attachQueuePushRecorder(t, n)
	cwd := "/tmp/queue-settle"
	p := writeTranscript(t, home, cwd, sidA, time.Now(),
		`{"type":"queue-operation","operation":"enqueue","content":"first"}`)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, ""))

	// Populate the meta cache so n.meta stays stale within metaMinInterval.
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if q := n.meta(p, st).queue; !slices.Equal(q, []string{"first"}) {
		t.Fatalf("warm queue = %v, want [first]", q)
	}
	appendTranscriptLine(t, p, `{"type":"queue-operation","operation":"enqueue","content":"second"}`)
	st2, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if q := n.meta(p, st2).queue; !slices.Equal(q, []string{"first"}) {
		t.Fatalf("expected cached queue inside metaMinInterval, got %v", q)
	}

	pushesBefore := rec.count()
	res, err := request(t, n, "session.queue.edit", map[string]any{"session_id": sidA, "keep": []int{1}})
	if err != nil {
		t.Fatalf("session.queue.edit: %v", err)
	}
	if got := strs(res["queue"]); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("editQueue used stale meta: got %v, want [first second]", got)
	}
	rec.waitPushPast(t, pushesBefore)

	// Simulate Claude Code asynchronously writing popAll for each popped item + kept enqueue to the transcript after edit keys.
	appendTranscriptLine(t, p, `{"type":"queue-operation","operation":"popAll","content":"first"}`)
	appendTranscriptLine(t, p, `{"type":"queue-operation","operation":"popAll","content":"second"}`)
	appendTranscriptLine(t, p, `{"type":"queue-operation","operation":"enqueue","content":"second"}`)
	rec.waitForQueued(t, sidA, []string{"second"})
}

func TestSendQueuedNowClaudeInvalidatesMetaAndWatchesSettle(t *testing.T) {
	defer func(d, s time.Duration) { sendNowWait, sendNowScrollWait = d, s }(sendNowWait, sendNowScrollWait)
	sendNowWait, sendNowScrollWait = 600*time.Millisecond, 300*time.Millisecond

	h := &queueHost{bareHost: bareHost{kind: host.Orca}, queued: queuedScreen}
	n, _ := testNode(t)
	n.hosts = host.NewRegistry(h)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	n.agents = map[string]host.Agent{sidA: {Agent: "claude", PaneID: "orca:t1:l1", AgentStatus: proto.StatusWorking}}
	ctx, cancel := context.WithCancel(context.Background())
	n.firstPromptParent = ctx
	t.Cleanup(func() {
		cancel()
		n.firstWait.Wait()
	})
	rec := attachQueuePushRecorder(t, n)

	p := filepath.Join(t.TempDir(), sidA+".jsonl")
	writeLines(t, p, []string{`{"type":"queue-operation","operation":"enqueue","content":"Also run the linter at the end."}`})
	n.paths[sidA] = p
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if q := n.meta(p, st).queue; !slices.Equal(q, []string{"Also run the linter at the end."}) {
		t.Fatalf("warm queue = %v", q)
	}

	pushesBefore := rec.count()
	if err := n.sendQueuedNow(context.Background(), sidA); err != nil {
		t.Fatalf("sendQueuedNow: %v", err)
	}
	rec.waitPushPast(t, pushesBefore)

	appendTranscriptLine(t, p, `{"type":"queue-operation","operation":"popOne","content":"Also run the linter at the end."}`)
	rec.waitForQueued(t, sidA, nil)
}

func TestSessionInterruptInvalidatesMetaAndWatchesSettle(t *testing.T) {
	n, f, home := herdrNode(t)
	rec := attachQueuePushRecorder(t, n)
	cwd := "/tmp/interrupt-settle"
	p := writeTranscript(t, home, cwd, sidA, time.Now(),
		`{"type":"queue-operation","operation":"enqueue","content":"queued item"}`)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, ""))

	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if q := n.meta(p, st).queue; !slices.Equal(q, []string{"queued item"}) {
		t.Fatalf("warm queue = %v", q)
	}

	pushesBefore := rec.count()
	if _, err := request(t, n, "session.interrupt", map[string]any{"session_id": sidA}); err != nil {
		t.Fatalf("session.interrupt: %v", err)
	}
	rec.waitPushPast(t, pushesBefore)

	appendTranscriptLine(t, p, `{"type":"queue-operation","operation":"popOne","content":"queued item"}`)
	rec.waitForQueued(t, sidA, nil)
}
