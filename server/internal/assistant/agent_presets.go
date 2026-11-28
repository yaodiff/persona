package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Agent presets for Agents (#14672, docs/assistant/model-profiles.md "Agent presets (tiers)"): an Agent that starts
// another one (a2a_send's new Agent, dispatch_start) names a tier of the profile in use as the assistant does with
// agent_start, and reads beforehand which presets each computer can start (a2a_sessions with presets,
// dispatch_read's overview).

// a2aRun is what an Agent another Agent starts on a tier runs on: the tier's launcher, model, effort and faster speed,
// resolved when the start was accepted (agentStartTier); zero without a tier. It is kept on the stored launch beside
// the tier the caller named (a2aLaunch), so a start that goes out later runs what was checked, and a retry compares
// the caller's tier, not this.
type a2aRun struct {
	Launcher string `json:"launcher"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Speed    string `json:"speed,omitempty"`
}

// lay puts r on a session.start request in place of the caller's launcher; Model configuration › Agents then fills
// what r leaves out (applyModel), as for agent_start. Nothing without a tier.
func (r a2aRun) lay(params map[string]any) {
	if r.Launcher == "" {
		return
	}
	params["launcher"] = r.Launcher
	for k, v := range map[string]string{"model": r.Model, "effort": r.Effort, "speed": r.Speed} {
		if v != "" {
			params[k] = v
		}
	}
}

// runs is r in a line, as a tier's is said (ModelTier.runs).
func (r a2aRun) runs() string {
	return ModelTier{Launcher: r.Launcher, Model: r.Model, Effort: r.Effort, Speed: r.Speed}.runs()
}

// agentStartTier resolves the tier an Agent names when it starts another (a2a_send's new Agent, dispatch_start) as
// agent_start resolves the assistant's, before anything is recorded or started: the tier of the profile in use first
// (an unknown one is refused, tierModel); a launcher the caller names that is not the tier's takes nothing from it
// (layTier); then the start is held to what computer m runs, with Model configuration › Agents laid over it as the
// start lays it (applyModel): a tier's launcher m lacks is refused (launcher_not_on_machine, fitLauncher), as is a
// faster speed its agent cannot start with (checkStartSpeed), and an agent that takes no effort drops the tier's. The
// tier's permission mode is left out on purpose: an Agent another Agent starts keeps the mode its start path gives
// it (a2a_send's plan, dispatch_start's own rule). Zero without a tier, or for one that gave nothing.
func (s *Service) agentStartTier(ctx context.Context, m Device, tier, launcher string) (a2aRun, error) {
	tier, launcher = strings.TrimSpace(tier), strings.TrimSpace(launcher)
	if tier == "" {
		return a2aRun{}, nil
	}
	pick, err := s.pickTierCandidate(ctx, tier, tierFacts{Machine: m.ID}) // the tier's candidate for the computer it starts on
	if err != nil {
		return a2aRun{}, err
	}
	t := pick.Tier
	run := a2aRun{Launcher: launcher}
	if !layTier(t, &run.Launcher, &run.Model, &run.Effort) {
		return a2aRun{}, nil // a launcher of the caller's own: the tier gives it nothing
	}
	if proto.FasterSpeed(t.Speed) {
		run.Speed = t.Speed // these starts name no model of their own, so the tier's speed goes with the tier's model
	}
	probe := map[string]any{}
	run.lay(probe)
	if err := s.applyModel(ctx, "agent.new", probe); err != nil {
		return a2aRun{}, err
	}
	_, fit, err := s.fitLauncher(ctx, m, startPick{launcher: launcher, tier: tier}, probe)
	if err != nil {
		return a2aRun{}, err
	}
	if err := checkStartSpeed(probe, fit, ""); err != nil {
		return a2aRun{}, err
	}
	if !startTakesModes(fit.agent) {
		run.Effort = ""
	}
	return run, nil
}

// presetsWait bounds how long a list of several computers' presets waits for their launcher lists.
const presetsWait = 8 * time.Second

// computerPresets is one computer's presets as presetsOn read them, or why they could not be read.
type computerPresets struct {
	presets []workerPreset
	err     error
}

// presetsOn reads at once, by device id, the Agent presets each of machines can start where its new Agents start
// (machinePresets in its default host, as New Agent lists them), within presetsWait. No computer is asked when the
// profile in use has no tiers.
func (s *Service) presetsOn(ctx context.Context, machines []Device) map[string]computerPresets {
	out := map[string]computerPresets{}
	if p, _, err := s.activeModelProfile(ctx); err != nil || p == nil || len(p.Tiers) == 0 || len(machines) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, presetsWait)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range machines {
		wg.Go(func() {
			presets, err := s.machinePresets(ctx, m.ID, agentStartHost(m, ""))
			mu.Lock()
			out[m.ID] = computerPresets{presets: presets, err: err}
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// runs is what preset p runs on, in a line (ModelTier.runs without a permission mode, which Agent-started Agents do
// not take from a tier).
func (p workerPreset) runs() string {
	return ModelTier{Launcher: p.Launcher, Model: p.Model, Effort: p.Effort, Speed: p.Speed}.runs()
}

// writePresets puts the presets read for machine under its line in dispatch_read's overview: one line each, with
// what it runs and when to use it.
func writePresets(b *strings.Builder, read map[string]computerPresets, machine string) {
	r, ok := read[machine]
	switch {
	case !ok:
		return
	case r.err != nil:
		b.WriteString("  - its Agent presets could not be read this time\n")
		return
	}
	for _, p := range r.presets {
		fmt.Fprintf(b, "  - tier %s: %s", p.Name, p.runs())
		if p.Description != "" {
			fmt.Fprintf(b, " (%s)", p.Description)
		}
		if p.Unavailable {
			b.WriteString("; unavailable now: its launcher's latest check failed")
		}
		b.WriteString("\n")
	}
}

// a2aComputer is one computer a2a_sessions lists with presets: where a2a_send can start an Agent, and the Agent
// presets it can start there.
type a2aComputer struct {
	Machine string         `json:"machine_id"`
	Name    string         `json:"name,omitempty"`
	Online  bool           `json:"online"`
	Presets []workerPreset `json:"presets,omitempty"`
	// Unread: the computer is online but its launcher list could not be read this time.
	Unread bool `json:"presets_unread,omitempty"`
}

// a2aComputers are the computers an a2a_send launch can go to (machine: only that one), the caller's own included,
// each with its presets; none while launching is switched off (agents.a2a.launch).
func (s *Service) a2aComputers(ctx context.Context, cfg assistantwire.A2AConfig, machine string) []a2aComputer {
	out := []a2aComputer{}
	if !cfg.LaunchAllowed() {
		return out
	}
	var listed, online []Device
	for _, d := range s.devices(ctx) {
		if machine != "" && d.ID != machine || !s.a2aMachineAllowed(ctx, cfg, d.ID) {
			continue
		}
		listed = append(listed, d)
		if d.Online || d.Reachable {
			online = append(online, d)
		}
	}
	read := s.presetsOn(ctx, online)
	for _, d := range listed {
		c := a2aComputer{Machine: d.ID, Name: d.Name, Online: d.Online || d.Reachable}
		if r, ok := read[d.ID]; ok {
			c.Presets, c.Unread = r.presets, r.err != nil
		}
		out = append(out, c)
	}
	return out
}

// a2aLaunchTier resolves the tier a new a2a_send launch names on its target computer (agentStartTier) before anything
// is recorded, and keeps what it runs on with the launch. A computer a2aStart would refuse is not asked, so a refusal
// never names its launchers. A retry by message_id is not resolved again: a2aStart matches it on the tier.
func (s *Service) a2aLaunchTier(ctx context.Context, cfg assistantwire.A2AConfig, machine string, launch *a2aLaunch) error {
	if launch.Tier == "" {
		return nil
	}
	dev, ok := s.machineByName(ctx, machine)
	if !ok || dev.ID != machine || !s.a2aMachineAllowed(ctx, cfg, machine) || !cfg.LaunchAllowed() {
		return nil // a2aStart refuses it
	}
	run, err := s.agentStartTier(ctx, dev, launch.Tier, launch.Launcher)
	if err != nil {
		return fmt.Errorf("%w; nothing was started", err)
	}
	launch.Run = run
	return nil
}

// a2aLaunchWhere is where the phone's card for a new a2a_send launch from an assistant conversation says it goes: the
// folder, and the tier with what it runs on, as agent_start's card says it.
func (s *Service) a2aLaunchWhere(ctx context.Context, launch *a2aLaunch) string {
	if launch.Run.Launcher == "" {
		return launch.CWD
	}
	return launch.CWD + i18n.T(s.lang(ctx), "assistant-tools.with_tier", i18n.Args{"tier": launch.Tier, "runs": launch.Run.runs()})
}

// dispatchRetry: messageID names a dispatch this Agent asked for already (admitDispatchA2A answers it and matches the
// tier it named then), so its tier is not resolved again.
func (s *Service) dispatchRetry(ctx context.Context, from a2aRef, messageID string) bool {
	if messageID == "" {
		return false
	}
	st, err := s.a2aStore()
	if err != nil {
		return false
	}
	row, err := st.A2ARequest(ctx, a2aSessionCaller(from).Owner, messageID)
	return err == nil && row != nil
}
