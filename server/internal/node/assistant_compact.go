package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The user compacting an assistant conversation (docs/assistant/assistant.md "Manual compaction"): /compact to claude, the way an idle
// one is compacted (compactIdle), or thread/compact/start to Codex. Where it is at goes in the session state
// (session.compaction): the apps show it, and one that a node restart cut off is not left running.

const assistantCompactTimeout = 10 * time.Minute

// assistantCompact starts compacting conversation id, resuming its process when it is not running; with onlyRunning
// (the hub compacting a meeting's room, #8253) it refuses not_running instead: a process that exited since the hub saw it
// running is never started only to be compacted.
func (n *Node) assistantCompact(ctx context.Context, id string, onlyRunning bool) (compactErr error) {
	if n.runtimeAssistant(id) {
		return n.assistantCompactRuntime(ctx, id, onlyRunning)
	}
	if !validID(id) {
		return errors.New("invalid session id")
	}
	if saved, ok := n.state.get(id); ok && proto.IsExternalLauncher(saved.Launcher) {
		var finish func()
		ctx, finish = n.beginExternalStartup(ctx, id)
		defer finish()
	}
	defer n.assistantLife.lock(id)() // not while a send or a restore has this conversation's process (model_restore.go)
	// As in assistantSend, a start after pruning the only, refused turn counts as starting only while it can still come up.
	drop := func() {}
	defer func() {
		if compactErr != nil {
			drop()
		}
	}()
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p != nil && p.alive() {
		p.mu.Lock()
		busy, compacting := p.busy, p.compacting
		p.mu.Unlock()
		switch {
		case compacting:
			return refusal("compacting", "正在压缩", nil)
		case busy:
			return refusal("busy", "正在回答，等这一轮结束再压缩", nil)
		}
	} else {
		if onlyRunning {
			return refusal("not_running", "这个对话的进程没有在运行，不为压缩启动它", nil)
		}
		if n.transcriptPath(id) == "" {
			return refusal("gone", "这个对话的记录已经不在了", nil)
		}
		st, _ := n.state.get(id)
		launcher, cwd := launcherOr("", st.Launcher), st.Cwd
		if launcher == "" {
			launcher = "claude"
		}
		if cwd == "" {
			cwd = identity.AssistantDir(n.home)
		}
		var agentKind string
		if !proto.IsExternalLauncher(launcher) {
			var err error
			agentKind, err = n.assistantAgent(ctx, launcher)
			if err != nil {
				return err
			}
			n.cancelAssistantRecovery(id)
			if held := n.takeAssistant(id); held != nil {
				stopAndWait([]*assistantProc{held})
			}
		}
		var err error
		// A guest (a meeting's room too) starts as on any resume: spawnClaude reads its tools and kind from its record
		// (no built-in tools, strict MCP config, project setting sources, its own settings and profile), and
		// spawnAssistant refuses one whose launcher runs another agent by now.
		execution := modelExecution{Source: st.ModelSource, Model: st.Model, Effort: st.ModelEffort, UseEffort: st.ModelFingerprint != "", Fingerprint: st.ModelFingerprint, IsMain: st.IsMain, MainCompactWindow: st.MainCompactWindow, ConfigDir: st.ConfigDir, SupervisionInstructions: st.SupervisionInstructions, Origin: st.Origin, OriginKnown: st.OriginKnown}
		resolved, current, _ := n.assistantModelDecision(ctx, id)
		if current.OriginKnown {
			execution.Origin, execution.OriginKnown = current.Origin, true
		}
		if st.Guest == nil && !execution.OriginKnown {
			// Cold compaction spawns directly rather than passing through assistantSend. Apply the same fail-closed
			// boundary: a successful empty model answer and an older hub are not ordinary-origin attestations.
			return errAssistantOriginUnknown
		}
		if proto.IsExternalLauncher(launcher) {
			if resolved != launcher || current.External == nil {
				return externalRefusal(ctx, "adapter_scope_required")
			}
			// Refresh execution authority, not the conversation's saved profile or unresolved policy.
			execution.Model, execution.Effort = current.Model, current.Effort
			execution.UseEffort, execution.Fingerprint = current.UseEffort, current.Fingerprint
			execution.Runtime, execution.IsMain, execution.External = current.Runtime, current.IsMain, current.External
			execution.MainCompactWindow = n.mainWindowOn(launcher, current.MainCompactWindow)
			if err := n.checkExternalAssistant(ctx, id, launcher, execution, false); err != nil {
				return err
			}
			agentKind, err = n.assistantAgent(ctx, launcher)
			if err != nil {
				return err
			}
			n.cancelAssistantRecovery(id)
			if held := n.takeAssistant(id); held != nil {
				stopAndWait([]*assistantProc{held})
			}
		}
		if current.SupervisionResolved {
			execution.SupervisionInstructions = current.SupervisionInstructions
		}
		// It starts as a send would: in interactive Claude Code while the setting is on (#10058; tuiWanted leaves guests
		// and plan mode in print mode), so neither the compaction nor the next message runs a process of the other kind.
		execution.Interactive = current.Interactive
		// The manager's current refresh role, read afresh (#12786): a main or lead conversation compacted cold
		// starts with its proxy again (a new, empty one: the compacted prefix is captured anew), anything else
		// without one.
		execution.CacheKeepalive = current.CacheKeepalive
		if proto.IsExternalLauncher(launcher) {
			execution.StartupContext = ctx
		}
		if st.Origin != execution.Origin || st.OriginKnown != execution.OriginKnown {
			// Tool selection reads session state during spawn. Record the explicit decision before the process can start.
			st.Origin, st.OriginKnown = execution.Origin, execution.OriginKnown
			n.state.put(id, st)
		}
		// A trailing refused turn is no context the compaction would summarize: it is cut, and a session left with no
		// file by that starts under the id rather than resuming nothing.
		if agentKind == "claude" {
			if pruned, clean := n.healClaudeRefusalTranscript(id); pruned && !clean {
				drop = n.startsBegun.begin(id, n.startWait()) // started again right below: an empty chat meanwhile
			}
		}
		if p, err = n.spawnAssistant(id, cwd, launcher, n.transcriptPath(id) != "", false, execution); err != nil {
			return err
		}
		if st.SupervisionInstructions != execution.SupervisionInstructions {
			st.SupervisionInstructions = execution.SupervisionInstructions
			n.state.put(id, st)
		}
	}
	if proto.IsExternalLauncher(p.launcher) && ctx.Err() != nil {
		return externalAdapterError(ctx, ctx.Err())
	}
	if err := n.compactAssistantProc(ctx, p, time.Now().UTC()); err != nil {
		return err
	}
	return finishExternalStartup(ctx)
}

// compactAssistantProc starts an in-place compaction while assistantLife is held.
// Both the user's request and idle-aware delivery use the same completion reporting.
func (n *Node) compactAssistantProc(ctx context.Context, p *assistantProc, startedAt time.Time) error {
	id := p.id
	n.forgetCacheWarm(id)
	p.mu.Lock()
	p.compacting, p.compacted, p.manual = true, true, true
	p.lastActive = startedAt
	p.mu.Unlock()
	n.setCompaction(id, &proto.Compaction{State: proto.CompactionRunning, StartedAt: startedAt})
	var err error
	if p.cx != nil {
		err = p.cx.compact(ctx)
	} else {
		line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": n.assistantCompactCommand(id)}})
		err = p.write(line)
	}
	if err != nil {
		p.mu.Lock()
		p.compacting, p.manual = false, false
		p.mu.Unlock()
		n.compactionEnded(id, "没能开始压缩："+err.Error())
		return err
	}
	n.logger.Printf("assistant %s: compacting in place", short(id))
	return nil
}

// setCompaction records where the user's compaction is at and pushes the session list.
func (n *Node) setCompaction(id string, c *proto.Compaction) {
	if !n.state.update(id, func(st *sessionState) bool {
		st.Compaction = c
		return true
	}) {
		n.state.put(id, sessionState{Compaction: c})
	}
	n.notifySessions()
}

// compactionEnded ends a running compaction: done with no failure, else failed with it.
func (n *Node) compactionEnded(id, failure string) {
	ended := false
	n.state.update(id, func(st *sessionState) bool {
		if st.Compaction == nil || st.Compaction.State != proto.CompactionRunning {
			return false
		}
		c := *st.Compaction
		now := time.Now().UTC()
		c.FinishedAt = &now
		c.State, c.Error = proto.CompactionDone, ""
		if failure != "" {
			c.State, c.Error = proto.CompactionFailed, failure
		}
		st.Compaction = &c
		ended = true
		return true
	})
	if !ended {
		return
	}
	if failure != "" {
		n.logger.Printf("assistant %s: compaction failed: %s", short(id), failure)
	}
	n.notifySessions()
}

// sweepCompactions fails the compactions whose process is gone: it exited, or the node restarted under them.
func (n *Node) sweepCompactions() {
	var lost []string
	n.state.mu.Lock()
	for id, st := range n.state.Sessions {
		if st.Compaction != nil && st.Compaction.State == proto.CompactionRunning {
			lost = append(lost, id)
		}
	}
	n.state.mu.Unlock()
	for _, id := range lost {
		n.mu.Lock()
		p := n.procs[id]
		n.mu.Unlock()
		if p == nil || !p.alive() {
			n.compactionEnded(id, "压缩时进程退出了，没有压完")
		}
	}
}

// compactFailure is what a /compact that ended without a boundary is shown as: claude's own words when it gave any.
func compactFailure(isError bool, result string, details ...string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		if isError {
			if detail := strings.TrimSpace(strings.Join(details, " ")); detail != "" {
				return snippet(detail)
			}
			return "压缩出错了"
		}
		return "没有压缩：对话太短，或者 Claude Code 没有执行 /compact"
	}
	return snippet(result)
}

// ---- what a new main conversation is told about the old one ----

const (
	handoffSummaryMax = 12000 // characters of the compaction summary
	handoffTurnMax    = 800   // characters of each recent message
	handoffRecentMax  = 6000  // characters of the recent messages together
	handoffTurns      = 12
	handoffPageLimit  = 400
)

// HandoffTurn is one recent message of a conversation handed off: role user or assistant.
type HandoffTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Handoff is what assistant.handoff reads from a conversation: its latest compaction summary ("" when it was never
// compacted) and the messages after it, the newest last.
type Handoff struct {
	Summary string        `json:"summary"`
	Recent  []HandoffTurn `json:"recent"`
}

// assistantHandoff reads what a new main conversation should know of conversation id (docs/assistant/assistant.md
// "New main chat").
func (n *Node) assistantHandoff(ctx context.Context, id string) (Handoff, error) {
	if n.runtimeAssistant(id) {
		return n.assistantHandoffRuntime(ctx, id)
	}
	if !validID(id) {
		return Handoff{}, errors.New("invalid session id")
	}
	path := n.transcriptPath(id)
	if path == "" {
		return Handoff{}, refusal("gone", "这个对话的记录已经不在了", nil)
	}
	r, err := os.Open(path)
	if err != nil {
		return Handoff{}, err
	}
	summary, at := lastCompactSummary(r)
	r.Close()
	f, err := n.file(id)
	if err != nil {
		return Handoff{}, err
	}
	n.refresh(id, f)
	page, _, _ := f.Page(0, handoffPageLimit)
	return Handoff{Summary: clipHandoff(summary, handoffSummaryMax), Recent: recentTurns(page, at)}, nil
}

// lastCompactSummary is the summary of a transcript's last compaction (Claude Code's JSONL or a Codex rollout) and
// its line (1-based; 0 when it was never compacted).
func lastCompactSummary(r io.Reader) (summary string, at int) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for seq := 1; sc.Scan(); seq++ {
		if s, ok := compactSummary(sc.Bytes()); ok {
			summary, at = s, seq
		}
	}
	return summary, at
}

// anyContextRe is every block Persona put before a message (the snapshot, an earlier handoff, a relay's note).
var anyContextRe = lazyre.New(`(?s)<` + compat.ContextPattern() + `[^>]*>.*?</` + compat.ContextPattern() + `>\s*`)

// recentTurns are the text of the user's and the assistant's messages after line `after`, the newest last, within
// handoffTurns messages and handoffRecentMax characters.
func recentTurns(items []proto.Item, after int) []HandoffTurn {
	out := []HandoffTurn{}
	for _, it := range items {
		if it.Seq <= after || it.Kind != proto.KindText || (it.Role != "user" && it.Role != "assistant") {
			continue
		}
		text := strings.TrimSpace(anyContextRe.ReplaceAllString(it.Text, ""))
		if text == "" {
			continue
		}
		out = append(out, HandoffTurn{Role: it.Role, Text: clipHandoff(text, handoffTurnMax)})
	}
	if len(out) > handoffTurns {
		out = out[len(out)-handoffTurns:]
	}
	total := 0
	for i := len(out) - 1; i >= 0; i-- { // the newest are kept
		total += len([]rune(out[i].Text))
		if total > handoffRecentMax {
			return out[i+1:]
		}
	}
	return out
}

// compactSummary is the summary a transcript line carries: Claude Code's user line with isCompactSummary, or a
// Codex rollout's compacted line.
func compactSummary(line []byte) (string, bool) {
	var e struct {
		Type             string `json:"type"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		Message          *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Payload struct {
			Message string `json:"message"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &e) != nil {
		return "", false
	}
	switch {
	case e.Type == "user" && e.IsCompactSummary && e.Message != nil:
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil {
			return strings.TrimSpace(s), true
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(e.Message.Content, &blocks)
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Type == "text" {
				b.WriteString(bl.Text)
			}
		}
		return strings.TrimSpace(b.String()), true
	case e.Type == "compacted" && strings.TrimSpace(e.Payload.Message) != "":
		return strings.TrimSpace(e.Payload.Message), true
	}
	return "", false
}

func clipHandoff(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// assistantCompactCommand supplies configured instructions for explicit and idle Claude compactions. Automatic
// compaction reads the same instructions from the manager-generated CLAUDE.md.
func (n *Node) assistantCompactCommand(id string) string {
	instructions := n.assistantCompactInstructions(id)
	if instructions == "" {
		return "/compact"
	}
	return "/compact " + instructions
}
func (n *Node) assistantCompactInstructions(id string) string {
	if n.peer != nil {
		if instructions := n.assistantContext(id).CompactInstructions; instructions != "" {
			return instructions
		}
	}
	st, _ := n.state.get(id)
	return st.CompactInstructions
}
