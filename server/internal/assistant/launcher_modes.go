package assistant

import (
	"fmt"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- which agents take a permission mode and an effort at their start (#7559) ----
//
// The hub matches what the node does with a permission mode or effort a start names (node/control.go):
//   - work agents (proto.WorkAgentTypes: pi, cursor, #8151) start anyway, the two ignored. The hub drops them too:
//     no default mode, no tier's effort, and one the caller names is left out rather than refused. agy is the
//     exception: it takes both, in a terminal as at its ACP runtime, and runs in Allow all by default (allowAll).
//   - traex takes effort, and omp takes neither. Both refuse an explicit permission mode here before starting.
//   - every other agent keeps what the start sends: Claude Code (--permission-mode, --effort), Codex (its sandbox and
//     reasoning effort), an agent a runtime starts (paseo, whose bridge maps the mode, #8141), and "" (the hub can't
//     tell what the launcher runs). The node judges those: nothing is dropped or refused on a guess.
// For the first two a writable task runs without a mode, and a read-only one relies on the read-only constraint every
// read-only task's first message carries (prompts.RenderReadonlyTaskConstraint), the same constraint a Claude-family
// launcher, which starts in bypassPermissions, relies on.

// startTakesModes reports whether a start takes a Persona permission mode.
// Antigravity (agy) runs via its ACP runtime and supports permission modes and effort.
func startTakesModes(agent string) bool {
	return agent == "agy" || (!proto.IsWorkAgentType(agent) && !proto.IsNoModeAgentType(agent))
}

func startTakesEffort(agent string) bool {
	return agent == "traex" || startTakesModes(agent)
}

// startModesRefused is the refusal of a permission mode or effort the caller named for a launcher whose agent the node
// refuses them for (traex, omp); nil when it named none, or for a work agent, which the node starts without them.
func startModesRefused(launcher, agent, permission, effort string) error {
	switch {
	case !proto.IsNoModeAgentType(agent):
		return nil
	case permission != "":
		return fmt.Errorf("launcher %s runs %s, which takes no permission mode (%s): leave permission out; "+
			"set readonly true for a task that only reads, and its first message carries the read-only constraint", launcher, agent, permission)
	case effort != "" && !startTakesEffort(agent):
		return fmt.Errorf("launcher %s runs %s, which takes no effort (%s): leave effort out", launcher, agent, effort)
	}
	return nil
}
