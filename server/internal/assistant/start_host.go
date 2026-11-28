package assistant

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// Where an Agent the assistant starts opens (#13447). A new Agent with the computer's default host runs agy in a herdr
// pane (#10237): someone is at that terminal. A task_plan task or an agent_start has nobody at the pane, and a pane
// Persona cannot read (herdr's antigravity-cli integration missing) leaves its supervisor nothing to judge. So an agy
// start that names no host goes to the computer's Antigravity companion when that runs its launcher: a conversation
// Persona reads as it goes, with approval cards, Persona's tools and token usage. The node refuses a start naming the
// companion while none is connected (agy_companion_offline, node/starthost.go); one the hub sent there by itself then
// starts again in the computer's default host, so a companion that just went away never fails the task. A start that
// names a host (agy, or an engine host such as herdr) keeps it.
//
// An Agent another Agent starts (a2a_send's new Agent, dispatch_start) has nobody at its pane either, and takes the
// same host (agentStartedHost, #14887). Such a start in plan (an a2a_send launch's mode unless its caller is attested
// for a higher one) whose launcher cannot be held read-only on the computer is refused as a read-only task is
// (readonlyLauncherRefusal): when the tool is called, before its A2A task is recorded, and again when the start goes
// out, since a launch can wait in the queue while its computer is away. A companion that went away meanwhile sends a
// writable start to the default host; a read-only one fails with the node's reason, as only the companion holds it.

// checkStartHostName refuses a host no start can name: agy, the Antigravity companion, or an engine host. "" is the
// computer's default host.
func checkStartHostName(host string) error {
	if host == "" || host == proto.HostAgy || proto.IsEngineHost(host) {
		return nil
	}
	return fmt.Errorf("host %q is not where an Agent can start: name agy (the Antigravity companion) or an engine host (%s), "+
		"or leave it out", host, strings.Join(proto.EngineHostIDs, ", "))
}

// checkHostLauncher refuses a start that names the companion for a launcher that does not start the Antigravity CLI:
// the companion runs nothing else. An agent the hub could not tell ("") is the node's to judge.
func checkHostLauncher(params map[string]any, fit startAgent) error {
	if host, _ := params["host"].(string); host != proto.HostAgy || fit.agent == "" || fit.agent == "agy" {
		return nil
	}
	launcher, _ := params["launcher"].(string)
	return fmt.Errorf("host agy (the Antigravity companion) runs only the Antigravity CLI, and launcher %s runs %s: "+
		"leave host out or pick an agy launcher", launcher, kindName(fit.agent))
}

// companionStartHost names the companion in params (host agy) for an agy start that names no host, when the companion
// runs its launcher on m (companionRuns). True when it did: a start the node then refuses as away (companionAway) goes
// again without the host.
func (s *Service) companionStartHost(ctx context.Context, m Device, fit startAgent, params map[string]any) bool {
	if host, _ := params["host"].(string); host != "" || fit.agent != "agy" || !s.companionRuns(ctx, m, fit) {
		return false
	}
	params["host"] = proto.HostAgy
	return true
}

// companionRuns: m's Antigravity companion runs the agy launcher fit is: its own agy launcher, which the computer's list
// names while the companion is connected (the list's entry for it is the companion's, source agy), or a launcher kept in
// Persona that starts agy, which the node hands to the companion while one is connected (node startRuntime).
func (s *Service) companionRuns(ctx context.Context, m Device, fit startAgent) bool {
	switch compat.Source(fit.source) {
	case proto.HostAgy:
		return true
	case "persona":
		list, known := s.launchersHere(withUnhidden(ctx, proto.HostAgy), m.ID)
		return known && slices.ContainsFunc(list, func(l proto.Launcher) bool {
			return compat.Source(l.Source) == proto.HostAgy && l.Agent == "agy"
		})
	}
	return false
}

// companionAway: err is the node's refusal of a start the hub sent to the companion by itself (chosen), because no
// companion is connected there now; the start then goes to the computer's default host without the host.
func companionAway(err error, chosen bool) bool {
	return chosen && refusedWith(err, proto.CodeAgyCompanionOffline)
}

// agentStartedHost readies params, the start of an Agent another Agent starts on m as it will reach the node (its
// launcher with Model configuration › Agents laid over it, and its permission mode), as a planned task's start is
// readied: an agy start that names no host goes to m's Antigravity companion where that runs its launcher
// (companionStartHost; true when it did, for companionAway), and a start in plan whose launcher cannot be held
// read-only on m is refused (readonlyLauncherRefusal: readonly_unsupported, naming the launchers that can). The
// launcher is only looked up, as dispatchAllowAll looks it up: one fitLauncher would refuse or replace, or one it
// cannot tell, goes to the node as before, which judges it.
func (s *Service) agentStartedHost(ctx context.Context, m Device, params map[string]any) (bool, error) {
	// The launcher list is read with the same bound as the other decisions around a start: a computer whose
	// launchers.list is slow must not delay the start, which goes on to the node with what could be told.
	ctx, cancel := context.WithTimeout(ctx, agentStartListTimeout)
	defer cancel()
	launcher, _ := params["launcher"].(string)
	probe := maps.Clone(params)
	_, fit, err := s.fitLauncher(ctx, m, startPick{launcher: strings.TrimSpace(launcher)}, probe)
	if err != nil || probe["launcher"] != params["launcher"] {
		return false, nil
	}
	companion := s.companionStartHost(ctx, m, fit, params)
	// Only a start that would run the work agent in a terminal needs refusing: a launcher kept in Persona (a managed
	// agy launcher, isTerminalLauncherSource) starts on its runtime, which holds plan itself.
	if mode, _ := params["permission_mode"].(string); mode == "plan" && isTerminalLauncherSource(fit.source, fit.agent) {
		if err := s.readonlyLauncherRefusal(ctx, m, params, fit); err != nil {
			return false, err
		}
	}
	return companion, nil
}
