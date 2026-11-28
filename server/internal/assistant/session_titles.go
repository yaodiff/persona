package assistant

import (
	"context"
	"path"
	"strings"

	"github.com/nwverse/persona/server/internal/assistant/fleetview"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func feishuSessionNames(sess proto.Session, preferTab bool) (label, secondary string) {
	if tab := sess.DisplayTabName(preferTab); tab != "" {
		if title := transcript.Title(sess.Title); preferTab && title != tab {
			secondary = clip(oneLine(title), 70)
		}
		return tab, secondary
	}
	return agentLabel(sess), ""
}

// feishuDirections takes one snapshot for a card or a whole report. Use the same
// display names as the direction tree, including a name the owner just changed.
func (s *Service) feishuDirections(ctx context.Context) map[string]string {
	cfg := s.settings(ctx)
	if len(cfg.Leaders) == 0 {
		return nil
	}
	titles, _ := s.conversationTitles()
	return s.directionSessionNames(ctx, cfg, titles)
}

// feishuDirection names the conversation responsible for the work. The main
// conversation and its background supervisor share the Persona direction.
func (s *Service) feishuDirection(ctx context.Context, t Todo, names map[string]string) string {
	supervisor := t.Origin
	if t.Supervision != nil {
		supervisor = t.Supervision.Supervisor
		if _, _, worker := parseLeaderRef(supervisor); worker {
			supervisor, _ = s.supervisorAboveTask(ctx, t)
		}
	}
	if name, isDirection := names[supervisor]; isDirection {
		if strings.TrimSpace(name) == "" {
			name = supervisor
		}
		return "[" + clip(oneLine(name), directionNameMost) + "] "
	}
	return "[Persona] "
}

// feishuAgentPlaceParts is feishuAgentPlace's parts: the agent, its computer ("" when it says none), and the session's
// other name when the agent goes by its herdr tab name.
func (s *Service) feishuAgentPlaceParts(ctx context.Context, ix proto.Interaction) (agent, machine, secondary string) {
	agent = transcript.Title(ix.SessionTitle)
	sess, listed := s.nodeSession(ix.MachineID, ix.SessionID)
	if listed {
		if m, found := s.fleetMachine(ix.MachineID); found {
			sess.Assistant = m.isAssistant(sess)
		}
		if sess.HerdrTabName() != "" {
			preferTab := s.useHerdrTabName(ctx)
			if sess.DisplayTabName(preferTab) != "" {
				agent, secondary = feishuSessionNames(sess, preferTab)
			}
		}
	}
	if agent == "" && ix.Kind == proto.IxScreen {
		if listed {
			agent = fleetview.AgentTitle(sessionTitle(&sess), 60)
		}
		if (agent == "" || agent == ".") && ix.Cwd != "" {
			agent = path.Base(ix.Cwd)
		}
		if agent == "" || agent == "." {
			agent = short(ix.SessionID)
		}
	}
	if agent == "" {
		agent = "Persona"
	}
	agent = clip(oneLine(agent), 70)
	if ix.Kind == proto.IxScreen {
		if m, ok := s.fleetMachine(ix.MachineID); ok && m.Name != "" {
			return agent, clip(oneLine(m.Name), 40), secondary
		}
	}
	if d, ok := s.device(ctx, ix.MachineID); ok && d.Name != "" && agent != "Persona" {
		return agent, clip(oneLine(d.Name), 40), secondary
	}
	if ix.Kind == proto.IxScreen && ix.MachineID != "" {
		return agent, clip(oneLine(ix.MachineID), 40), secondary
	}
	return agent, "", secondary
}
