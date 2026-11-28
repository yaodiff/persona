package node

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/transcript"
)

func claudeFastModel(model, version string, env procEnv) bool {
	model, _, _ = strings.Cut(model, "[")
	if i := strings.LastIndexAny(model, "-@"); i >= 0 && len(model[i+1:]) == 8 &&
		strings.IndexFunc(model[i+1:], func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		model = model[:i]
	}
	if env.FastUnavailable {
		return false
	}
	switch model {
	case "claude-fable-5-2":
		return versionAtLeast(version, [3]int{2, 1, 321})
	case "claude-opus-5-5", "claude-opus-5", "claude-opus-4-8":
		// On Vertex, /fast can switch an Opus session to Fable.
		return !env.Vertex && versionAtLeast(version, [3]int{2, 1, 205})
	default:
		return false
	}
}

func (n *Node) terminalSpeedPane(id, pane string) bool {
	if pane == "" || host.ParsePane(pane).Kind == host.Botmux || n.codexBlocked(id) || len(n.alsoOpenIn(id)) != 0 {
		return false
	}
	h, _ := n.hosts.Pane(pane)
	return h != nil && drivesScreen(h)
}

func (n *Node) claudeTerminalSpeedChoices(id string, m quickMeta) []proto.ServiceTier {
	env, _ := n.sessionEnv(id)
	if m.surface != transcript.SurfaceDesktop && claudeFastModel(n.shownModel(id, m.model), m.version, env) {
		return []proto.ServiceTier{{ID: "default", Name: "Standard"}, {ID: "priority", Name: "Fast"}}
	}
	return nil
}

func (n *Node) terminalSpeedAvailable(s *proto.Session) bool {
	return s.Status != proto.StatusOffline && s.Status != proto.StatusInterrupted && !s.Starting &&
		!s.IsSubagent && !s.Assistant && (s.Capabilities == nil || s.Capabilities.Send) &&
		n.terminalSpeedPane(s.SessionID, s.PaneID)
}

func (n *Node) claudeTerminalSpeedRow(s *proto.Session, m quickMeta) {
	if !n.terminalSpeedAvailable(s) {
		return
	}
	if tiers := n.claudeTerminalSpeedChoices(s.SessionID, m); len(tiers) > 0 {
		s.ServiceTiers = tiers
		if m.speed.Tier != "" {
			s.ServiceTier = m.speed.Tier
		}
	}
}

func speedUnsupported() error {
	return refusal("session_speed_unsupported", "This session does not offer the selected speed.", nil)
}

func terminalSpeedUnconfirmed() error {
	return &rpc.Error{Code: "session_speed_unconfirmed", Message: "The terminal did not confirm the speed change. Check the terminal before trying again.", Data: json.RawMessage(`{"delivered":"unknown"}`)}
}

func (n *Node) setTerminalSpeed(ctx context.Context, id, tier string) error {
	a, live := n.agent(id)
	n.mu.Lock()
	parked := n.onAgentsList[id]
	n.mu.Unlock()
	if parked || !live || n.isAssistantSession(id) || a.AgentStatus == proto.StatusOffline || a.AgentStatus == proto.StatusInterrupted {
		return speedUnsupported()
	}
	pane := a.PaneID
	if !n.terminalSpeedPane(id, pane) || n.sessionOfPane(pane) != id {
		return speedUnsupported()
	}
	switch a.Agent {
	case "codex":
		return n.setCodexTerminalSpeed(ctx, id, tier)
	case "claude":
		return n.setClaudeTerminalSpeed(ctx, id, pane, tier)
	default:
		return speedUnsupported()
	}
}

func (n *Node) setClaudeTerminalSpeed(ctx context.Context, id, pane, tier string) error {
	path := n.transcriptPath(id)
	st, err := os.Stat(path)
	if err != nil {
		return speedUnsupported()
	}
	m := n.metaNow(path, st)
	if !slices.ContainsFunc(n.claudeTerminalSpeedChoices(id, m), func(choice proto.ServiceTier) bool {
		return choice.ID == tier
	}) {
		return speedUnsupported()
	}
	if err := n.checkPaneFor(ctx, pane, "claude", sendOpts{readBox: true}); err != nil {
		return err
	}
	if n.plainScreen(pane) { // checkPaneFor reads the box with colors elsewhere; a plain screen's is read here
		text, draft, err := n.promptDraft(ctx, pane)
		if err != nil {
			return err
		}
		if draft {
			return n.inputDraftRefusal(pane, "speed", text)
		}
	}
	before, err := n.readPane(ctx, pane, "visible", 60)
	if err != nil {
		return err
	}
	cmd := "/fast off"
	if tier == "priority" {
		cmd = "/fast on"
	}
	started := time.Now()
	if err := n.prompt(ctx, pane, cmd); err != nil {
		if r := deliveryRefusal(err); r != nil {
			return r
		}
		return uncertain(err)
	}
	n.typing.typed(pane, cmd)
	wait, cancel := context.WithTimeout(ctx, codexEffortTimeout)
	defer cancel()
	tick := time.NewTicker(modelLook)
	defer tick.Stop()
	for {
		if now, err := os.Stat(path); err == nil && (now.Size() != st.Size() || !now.ModTime().Equal(st.ModTime())) {
			st = now
			if terminalSpeedRecorded(path, st.Size(), tier, started) {
				n.metaNow(path, st)
				n.notifySessionsSoon()
				return nil
			}
		}
		if shown, err := n.readPane(wait, pane, "visible", 60); err == nil {
			if reason := claudeTerminalSpeedRefusal(before, shown); reason != "" {
				return refusal("session_speed_refused", reason, nil)
			}
		}
		select {
		case <-wait.Done():
			return terminalSpeedUnconfirmed()
		case <-tick.C:
		}
	}
}

// Only a native settings acknowledgment after typing counts. Read a bounded suffix because Claude may rewrite its
// trailing metadata when it appends the command.
func terminalSpeedRecorded(path string, size int64, tier string, started time.Time) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	start := max(size-modelRecordMost, 0)
	r := bufio.NewReader(io.NewSectionReader(f, start, size-start))
	if start > 0 {
		_, _ = r.ReadBytes('\n') // partial first record
	}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return false
		}
		s := transcript.SessionSpeed(line)
		if s.Applied && s.Tier == tier && !s.At.Before(started.Truncate(time.Millisecond)) {
			return true
		}
	}
}

func claudeTerminalSpeedRefusal(before, shown string) string {
	lines := func(s string) []string {
		return strings.Split(screen.Plain(s), "\n")
	}
	old := make(map[string]int)
	clean := func(s string) string { return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "■•⎿")) }
	for _, line := range lines(before) {
		old[clean(line)]++
	}
	for _, line := range lines(shown) {
		line = clean(line)
		if old[line] > 0 {
			old[line]--
			continue
		}
		if strings.HasPrefix(line, "Fast mode ") {
			if strings.HasPrefix(line, "Fast mode is not available") || strings.HasPrefix(line, "Fast mode has been disabled") ||
				strings.HasPrefix(line, "Fast mode requires") || strings.HasPrefix(line, "Fast mode unavailable") ||
				strings.HasPrefix(line, "Fast mode needs") {
				return line
			}
		}
	}
	return ""
}
