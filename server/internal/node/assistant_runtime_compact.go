package node

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type runtimeCompaction struct {
	runtime    *runtimeConn
	state      *proto.Compaction
	prevStatus string
	silent     bool
	revision   int64
	turnError  string
	sawWork    bool
	settled    chan string
	done       chan struct{}
	doneOnce   sync.Once
	mu         sync.Mutex
	failure    string
}

func (c *runtimeCompaction) failureMessage() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

func (n *Node) assistantCompactRuntime(ctx context.Context, id string, onlyRunning bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer n.typing.lock(id)()
	if err := ctx.Err(); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	host, label, _ := n.runtimes.belongs(id)
	if host == nil && !onlyRunning {
		var err error
		host, err = n.runtimeFor(callCtx, "session.send", id)
		if err != nil {
			return err
		}
		if err := callCtx.Err(); err != nil {
			return err
		}
		if host == nil {
			if label == "" {
				label, _, _ = strings.Cut(id, "-")
			}
			return errRuntimeAway(label)
		}
	}
	_, err := n.startRuntimeCompactionLocked(ctx, host, id, true, false, onlyRunning)
	return err
}

func (n *Node) startRuntimeCompactionLocked(ctx context.Context, host *runtimeConn, id string, isAssistant, silent, onlyRunning bool) (*runtimeCompaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	compaction, err := n.runtimes.beginCompaction(ctx, host, id, silent, onlyRunning)
	if err != nil {
		return nil, err
	}
	n.publishSessionsSoon()
	params := map[string]any{"session_id": id, "text": "/compact"}
	if isAssistant {
		params["assistant"] = true
		var st sessionState
		if n.state != nil {
			st, _ = n.state.get(id)
		}
		if env := n.assistantEnvForRuntime(host, n.runtimeAssistantCwd(id, "", st), id); env != nil {
			params["env"] = env
		}
	}
	var reply struct {
		Queued bool `json:"queued"`
	}
	sentAt := time.Now()
	finishAgy := n.runtimes.agyWorkBegin(host, "session.send", id)
	err = host.peer.Call(callCtx, "session.send", params, &reply)
	finishAgy(err, false)
	if err != nil {
		n.finishRuntimeCompaction(id, compaction, err.Error())
		return nil, err
	}
	if host.agent() == agyAgent && n.clearAgyStopped(id, sentAt) {
		n.publishSessionsSoon()
	}
	if isAssistant {
		n.contextCompacted(id)
	}
	if reply.Queued {
		const message = "Compaction was queued, so its completion could not be tracked."
		n.finishRuntimeCompaction(id, compaction, message)
		return nil, &rpc.Error{Code: "compaction_queued", Message: message}
	}
	go n.awaitRuntimeCompaction(id, compaction, i18n.From(ctx))
	return compaction, nil
}

func (b *runtimeRegistry) compacting(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.compactingLocked(id)
}

func (b *runtimeRegistry) activeCompaction(id string) *runtimeCompaction {
	if b == nil || id == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.compactions[id]
	if current == nil || current.state.State != proto.CompactionRunning {
		return nil
	}
	return current
}

func (b *runtimeRegistry) compactingLocked(id string) bool {
	current := b.compactions[id]
	return current != nil && current.state.State == proto.CompactionRunning
}

func runtimeCompactingRefusal(ctx context.Context) error {
	return refusal("compacting", i18n.T(i18n.From(ctx), "assistant-start.compacting"), nil)
}

func (b *runtimeRegistry) beginCompaction(ctx context.Context, host *runtimeConn, id string, silent, onlyRunning bool) (*runtimeCompaction, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.compactingLocked(id) {
		return nil, runtimeCompactingRefusal(ctx)
	}
	var row proto.Session
	if host != nil {
		row, _ = host.row(id)
	}
	if row.Starting || row.Status == proto.StatusWorking || row.Status == proto.StatusBlocked {
		return nil, refusal("busy", "Wait for the current turn to finish before compacting.", nil)
	}
	if onlyRunning {
		return nil, refusal("not_running", "The runtime cannot confirm this conversation is already running.", nil)
	}
	prevStatus := row.Status
	if prevStatus != proto.StatusIdle && prevStatus != proto.StatusDone {
		prevStatus = ""
	}
	compaction := &runtimeCompaction{
		runtime: host, prevStatus: prevStatus, silent: silent, revision: row.Rev, turnError: row.TurnError,
		settled: make(chan string, 1), done: make(chan struct{}),
		state: &proto.Compaction{State: proto.CompactionRunning, StartedAt: time.Now().UTC()},
	}
	if b.compactions == nil {
		b.compactions = make(map[string]*runtimeCompaction)
	}
	b.compactions[id] = compaction
	return compaction, nil
}

func (c *runtimeCompaction) settle(failure string) {
	if c.state == nil || c.state.State != proto.CompactionRunning {
		return
	}
	select {
	case c.settled <- failure:
	default:
	}
}

func (b *runtimeRegistry) settleCompaction(h *runtimeConn, id, failure string) bool {
	if b == nil || h == nil || id == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	compact := b.compactions[id]
	if compact == nil || compact.runtime != h || compact.state == nil || compact.state.State != proto.CompactionRunning {
		return false
	}
	compact.settle(failure)
	return true
}

func (c *runtimeCompaction) observe(row proto.Session) {
	if c.state == nil || c.state.State != proto.CompactionRunning {
		return
	}
	switch row.Status {
	case proto.StatusWorking, proto.StatusBlocked:
		c.sawWork = true
	case proto.StatusIdle, proto.StatusDone:
		if c.sawWork || row.Rev != c.revision || row.TurnError != c.turnError {
			c.settle(row.TurnError)
		}
	}
}

func (n *Node) awaitRuntimeCompaction(id string, compaction *runtimeCompaction, lang i18n.Lang) {
	timer := time.NewTimer(assistantCompactTimeout)
	defer timer.Stop()
	var failure string
	select {
	case failure = <-compaction.settled:
	case <-compaction.runtime.peer.Done():
		failure = i18n.T(lang, "rpc-errors.unreachable")
	case <-timer.C:
		failure = i18n.T(lang, "rpc-errors.timeout")
	}
	n.finishRuntimeCompaction(id, compaction, failure)
}

func (n *Node) finishRuntimeCompaction(id string, compaction *runtimeCompaction, failure string) {
	compaction.mu.Lock()
	compaction.failure = failure
	compaction.mu.Unlock()
	b := n.runtimes
	b.mu.Lock()
	finished := *compaction.state
	now := time.Now().UTC()
	finished.FinishedAt = &now
	finished.State = proto.CompactionDone
	if failure != "" {
		finished.State, finished.Error = proto.CompactionFailed, failure
	}
	compaction.state = &finished
	compaction.runtime = nil
	if _, known := b.known[id]; !known {
		delete(b.compactions, id)
	}
	b.mu.Unlock()
	compaction.doneOnce.Do(func() {
		if compaction.done != nil {
			close(compaction.done)
		}
	})
	n.publishSessionsSoon()
}

func (n *Node) assistantHandoffRuntime(ctx context.Context, id string) (Handoff, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	host, err := n.runtimeFor(ctx, "session.items", id)
	if err != nil {
		return Handoff{}, err
	}
	if host == nil {
		return Handoff{}, refusal("gone", "The runtime conversation history is unavailable.", nil)
	}
	var page struct {
		Items []proto.Item `json:"items"`
	}
	params := map[string]any{"session_id": id, "limit": handoffPageLimit}
	if err := host.peer.Call(ctx, "session.items", params, &page); err != nil {
		return Handoff{}, err
	}
	return Handoff{Recent: recentTurns(page.Items, 0)}, nil
}
