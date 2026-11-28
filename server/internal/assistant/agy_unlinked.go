package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// unlinkedPane identifies the task link that produced the node's unavailable-history notice.
// Candidate conversations belong to the recipient's current inventory, never to this record.
type unlinkedPane struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	LinkedAt  time.Time `json:"linked_at"`
}

func isUnlinkedAgyPane(pane proto.Session, items []proto.Item) bool {
	// The node returns this single notice until it can identify the conversation (agyNotice).
	return agyTerminalSession(pane) && len(items) == 1 &&
		items[0].ID == "agy#0" && items[0].Role == "system" && items[0].Kind == proto.KindNotice
}

// agyReaderSpace verifies the actual recipient can inspect a suggestion with agent_read.
func (s *Service) agyReaderSpace(ctx context.Context, machine, recipient string) (string, bool) {
	if _, _, worker := parseLeaderRef(recipient); worker || machine == "" || recipient == "" {
		return "", false
	}
	row, ok := s.session(ctx, machine, recipient)
	row.MachineID = machine
	if !ok || !s.isAssistantRow(row) || s.isLogicalOfflineRow(row) || row.IsDeleted() || row.Archived || row.HiddenByDirectory() || !s.assistantCaller(ctx, machine, recipient) {
		return "", false
	}
	// Meeting rooms and reply handoffs have a dedicated dispatcher ahead of ordinary tool calls.
	if _, room, err := s.roomOf(ctx, machine, recipient); room || err != nil {
		return "", false
	}
	if _, handoff, err := s.handoffOf(ctx, machine, recipient); handoff || err != nil {
		return "", false
	}
	if _, denied := s.guestRefuses(ctx, machine, recipient, "agent_read", false); denied {
		return "", false
	}
	if _, denied := s.inboxModelRefusal(ctx, machine, recipient, row.Origin, "agent_read"); denied {
		return "", false
	}
	if _, refused := s.meetingRefuses(ctx, machine, recipient, "agent_read", true); refused || s.guardRefusal(ctx, machine, recipient, "agent_read") != nil {
		return "", false
	}
	return runSpace(row), true
}

func (s *Service) agyUnlinkedDetail(ctx context.Context, t Todo, panes []unlinkedPane, machine, recipient string) string {
	lang := s.lang(ctx)
	shortReason := i18n.T(lang, checkReasonAgyUnlinked)
	space, ok := s.agyReaderSpace(ctx, machine, recipient)
	if !ok {
		return shortReason
	}
	detail := prompts.AgyUnlinked{Language: answerLanguage(string(lang))}
	if s.ensureTodosLoaded(ctx) != nil {
		detail.Panes = []prompts.AgyUnlinkedPane{{}}
		return strings.TrimSpace(prompts.RenderAgyUnlinked(detail))
	}
	devices := s.devices(ctx) // the same machine inventory that agent_read searches
	lists := machineSessions{}
	for _, ref := range panes {
		if !slices.ContainsFunc(devices, func(d Device) bool { return d.ID == ref.MachineID }) {
			detail.Panes = append(detail.Panes, prompts.AgyUnlinkedPane{})
			continue
		}
		l, loaded := lists[ref.MachineID]
		if !loaded {
			l.list, l.err = s.sessions(ctx, ref.MachineID)
			lists[ref.MachineID] = l
		}
		if l.err != nil {
			detail.Panes = append(detail.Panes, prompts.AgyUnlinkedPane{})
			continue
		}
		pane, listed := findListedSession(l.list, ref.SessionID)
		if !listed || pane.SessionID != ref.SessionID || !agyTerminalSession(pane) || len(pane.Aliases()) != 0 ||
			pane.HiddenByDirectory() || pane.IsDeleted() || pane.Archived || runSpace(pane) != space {
			continue
		}
		s.todos.mu.RLock()
		active := s.todos.todos[t.ID]
		var link *TodoAgent
		if active != nil {
			for i := range active.Agents {
				a := &active.Agents[i]
				if a.MachineID == ref.MachineID && a.SessionID == ref.SessionID && a.LinkedAt.Truncate(time.Microsecond).Equal(ref.LinkedAt.Truncate(time.Microsecond)) && a.ReleasedAt == nil && a.DismissedAt == nil {
					link = a
					break
				}
			}
		}
		if link != nil {
			dir := cmp.Or(sessionDir(pane), link.Cwd)
			p := prompts.AgyUnlinkedPane{}
			if !ref.LinkedAt.IsZero() && proto.NodeAbs(dir) {
				p.Pane = ref.MachineID + "/" + pane.SessionID
				p.Candidates = s.agyCandidatesLocked(t.ID, ref, proto.NodeKey(dir), space, l.list)
			}
			detail.Panes = append(detail.Panes, p)
		}
		s.todos.mu.RUnlock()
	}
	if len(detail.Panes) == 0 {
		return shortReason
	}
	return strings.TrimSpace(prompts.RenderAgyUnlinked(detail))
}

// agyCandidatesLocked reads retained links and current responsibilities together with the live task link.
func (s *Service) agyCandidatesLocked(todo string, ref unlinkedPane, dir, space string, list []proto.Session) []string {
	taken := map[string]bool{}
	for _, t := range s.todos.todos {
		if t.ID == todo {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID == ref.MachineID && a.ReleasedAt == nil && a.DismissedAt == nil {
				taken[a.SessionID] = true
			}
		}
		if r := t.Responsible; t.DoneAt == nil && r != nil && r.Kind == db.ResponsibleSession && (r.MachineID == "" || s.sameMachine(r.MachineID, ref.MachineID)) {
			taken[r.SessionID] = true
		}
	}
	var rows []proto.Session
	for _, c := range list {
		if c.Agent != "agy" || c.SourceKind != proto.SourceRuntime || c.PaneID != "" || c.AgentSurface == "desktop" || s.isAssistantRow(c) ||
			c.Archived || c.IsDeleted() || c.HiddenByDirectory() || runSpace(c) != space || c.SessionID == "" || c.UpdatedAt.Before(ref.LinkedAt) ||
			proto.NodeKey(sessionDir(c)) != dir || taken[c.SessionID] || slices.ContainsFunc(c.Aliases(), func(id string) bool { return taken[id] }) {
			continue
		}
		rows = append(rows, c)
	}
	slices.SortFunc(rows, func(a, b proto.Session) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), strings.Compare(a.SessionID, b.SessionID))
	})
	var refs []string
	for _, c := range rows[:min(3, len(rows))] {
		refs = append(refs, ref.MachineID+"/"+c.SessionID)
	}
	return refs
}
