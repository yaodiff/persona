package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// Live model and effort switching on running Antigravity CLI (agy) terminal sessions (#14870).
//
// Terminal agy sessions accept `/model <id>` and `/effort <level>` directly in the TUI.
// Unlike Claude Code, agy echoes only `> /model` or `> /effort` without the argument, so
// confirmation compares newly printed transcript lines against the pre-command screen snapshot.

type agyCatalogEntry struct {
	efforts       []string
	defaultEffort string
}

var agyModelCatalogCache = struct {
	sync.Mutex
	byID map[string]agyCatalogEntry
}{byID: map[string]agyCatalogEntry{}}

// recordAgyModelCatalog caches discovered `agy models` effort metadata so live terminal
// agy sessions can advertise supported efforts without spawning a subprocess per list call.
func recordAgyModelCatalog(models []proto.AgentModel) bool {
	if len(models) == 0 {
		return false
	}
	agyModelCatalogCache.Lock()
	defer agyModelCatalogCache.Unlock()
	changed := false
	put := func(key string, entry agyCatalogEntry) {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return
		}
		prev, ok := agyModelCatalogCache.byID[key]
		if ok && prev.defaultEffort == entry.defaultEffort && slices.Equal(prev.efforts, entry.efforts) {
			return
		}
		agyModelCatalogCache.byID[key] = agyCatalogEntry{
			efforts:       slices.Clone(entry.efforts),
			defaultEffort: entry.defaultEffort,
		}
		changed = true
	}
	for _, m := range models {
		entry := agyCatalogEntry{efforts: m.Efforts, defaultEffort: m.DefaultEffort}
		put(m.ID, entry)
		if base := trimAgyEffortSuffix(m.ID); base != "" {
			if _, exists := agyModelCatalogCache.byID[strings.ToLower(base)]; !exists && len(m.Efforts) > 0 {
				put(base, agyCatalogEntry{efforts: m.Efforts})
			}
		}
	}
	return changed
}

func lookupAgyCatalogEntry(models []proto.AgentModel, model string) (agyCatalogEntry, bool) {
	candidates := agyModelCandidates(model)
	for _, c := range candidates {
		target := strings.ToLower(proto.TrimModelWindow(strings.TrimSpace(c)))
		if target == "" {
			continue
		}
		for _, m := range models {
			id := strings.ToLower(strings.TrimSpace(m.ID))
			if id == target {
				return agyCatalogEntry{efforts: slices.Clone(m.Efforts), defaultEffort: m.DefaultEffort}, true
			}
		}
	}
	for _, c := range candidates {
		target := strings.ToLower(proto.TrimModelWindow(strings.TrimSpace(c)))
		if target == "" {
			continue
		}
		for _, m := range models {
			base := strings.ToLower(trimAgyEffortSuffix(m.ID))
			if base != "" && base == target {
				return agyCatalogEntry{efforts: slices.Clone(m.Efforts)}, true
			}
		}
	}
	return agyCatalogEntry{}, false
}

// agyCatalogFor looks up a model's discovered effort metadata without blocking on CLI execution.
func (n *Node) agyCatalogFor(launcher, model string) (agyCatalogEntry, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return agyCatalogEntry{}, false
	}
	if n != nil {
		name := cmp.Or(strings.TrimSpace(launcher), agyAgent)
		key := n.home + "\x00" + name + "\x00" + agyAgent + "\x00" + n.executionFingerprint(name, modelExecution{})
		launcherModelsCache.Lock()
		cached, ok := launcherModelsCache.got[key]
		launcherModelsCache.Unlock()
		if ok && len(cached.Models) > 0 {
			if entry, found := lookupAgyCatalogEntry(cached.Models, model); found {
				return entry, true
			}
		}
	}
	agyModelCatalogCache.Lock()
	for _, c := range agyModelCandidates(model) {
		key := strings.ToLower(proto.TrimModelWindow(strings.TrimSpace(c)))
		if key == "" {
			continue
		}
		if entry, ok := agyModelCatalogCache.byID[key]; ok {
			agyModelCatalogCache.Unlock()
			return agyCatalogEntry{efforts: slices.Clone(entry.efforts), defaultEffort: entry.defaultEffort}, true
		}
	}
	agyModelCatalogCache.Unlock()
	if n != nil && n.runtimes != nil {
		if res, ok := launcherModelsOf(n.runtimes.launcherRuntime(agyAgent), agyAgent); ok && len(res.Models) > 0 {
			if entry, found := lookupAgyCatalogEntry(res.Models, model); found {
				return entry, true
			}
		}
	}
	return agyCatalogEntry{}, false
}

// agyModelEffort extracts the trailing effort tier from a suffixed agy model ID, or "" if none.
func agyModelEffort(model string) string {
	trimmed := strings.TrimSpace(proto.TrimModelWindow(model))
	lower := strings.ToLower(trimmed)
	for _, level := range proto.EffortLevels {
		if strings.HasSuffix(lower, "-"+level) && len(trimmed) > len(level)+1 {
			return level
		}
	}
	return ""
}

// agyPaneControls populates live model/effort capabilities and current switch state on a terminal agy row.
func (n *Node) agyPaneControls(s *proto.Session, id string) {
	if s == nil {
		return
	}
	fromTranscript := s.Model
	s.Model = n.shownModel(id, fromTranscript)

	var stLauncher, stModel string
	if n.state != nil {
		if st, ok := n.state.get(id); ok {
			stLauncher, stModel = st.Launcher, st.Model
		}
	}
	n.mu.Lock()
	m, hasModes := n.modes[id]
	n.mu.Unlock()
	if hasModes && m.Effort != "" && time.Since(m.EffortAt) <= pendingModelTTL {
		if m.Model == nil || fromTranscript == m.Model.From {
			s.Effort = m.Effort
		}
	}
	if s.Effort == "" {
		if e := agyModelEffort(s.Model); e != "" {
			s.Effort = e
		}
	}

	if s.Capabilities == nil || !s.Capabilities.Send {
		return
	}
	s.Capabilities.Model = proto.Bool(true)

	activeModel := cmp.Or(s.Model, stModel)
	if entry, ok := n.agyCatalogFor(cmp.Or(s.Launcher, stLauncher), activeModel); ok && len(entry.efforts) > 0 {
		s.Efforts = entry.efforts
		if s.Effort == "" && entry.defaultEffort != "" {
			s.Effort = entry.defaultEffort
		}
	}
}

func (n *Node) hasLiveAgyPane() bool {
	if n == nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.agents {
		if a.Agent == agyAgent && a.PaneID != "" && a.AgentStatus != proto.StatusOffline && a.AgentStatus != proto.StatusInterrupted {
			return true
		}
	}
	return false
}

// agyModelCommand builds `/model <model>` after validating model against proto.ValidModelID.
func agyModelCommand(model string) (string, error) {
	model = strings.TrimSpace(model)
	if !proto.ValidModelID(model) {
		return "", fmt.Errorf("invalid model %q", model)
	}
	return "/model " + model, nil
}

// agyEffortCommand builds `/effort <effort>` for agy's native effort levels (low..max).
func agyEffortCommand(effort string) (string, error) {
	effort = strings.TrimSpace(effort)
	if !slices.Contains(proto.EffortLevels, effort) {
		return "", fmt.Errorf("unknown effort level %q", effort)
	}
	return "/effort " + effort, nil
}

// agySwitchUnconfirmed indicates that the agy terminal screen showed neither confirmation nor rejection.
type agySwitchUnconfirmed struct {
	kind  string
	value string
}

func (e agySwitchUnconfirmed) Error() string {
	return fmt.Sprintf("could not confirm %s switch to %s in the Antigravity terminal; check the terminal", e.kind, e.value)
}

func isAgyRuleLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	count := 0
	for _, r := range trimmed {
		if r != '─' {
			return false
		}
		count++
	}
	return count >= 4
}

// agyInputBoxBounds locates the top and bottom rule indices of agy's bottom input box, or (-1, -1) if absent.
// Antigravity may render footer bars separated by horizontal rules below the input box (for example, running
// background tasks or cron schedules), so scan rule pairs bottom-up and match the prompt box starting with `>`.
func agyInputBoxBounds(lines []string) (top, bottom int) {
	var rules []int
	for i, line := range lines {
		if isAgyRuleLine(line) {
			rules = append(rules, i)
		}
	}
	for k := len(rules) - 2; k >= 0; k-- {
		t, b := rules[k], rules[k+1]
		if t+1 >= b || len(lines)-b > 16 {
			continue
		}
		for _, line := range lines[t+1 : b] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, ">") {
				return t, b
			}
			break
		}
	}
	return -1, -1
}

// agyHasDraft reports whether agy's input box currently contains unsent user text.
func agyHasDraft(text string) bool {
	lines := strings.Split(text, "\n")
	top, bottom := agyInputBoxBounds(lines)
	if top < 0 || top+1 >= bottom {
		return false
	}
	for _, line := range lines[top+1 : bottom] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, ">"); ok {
			if strings.TrimSpace(rest) != "" {
				return true
			}
			continue
		}
		return true
	}
	return false
}

// agyTranscriptLines extracts non-empty transcript lines above agy's bottom input box.
func agyTranscriptLines(text string) []string {
	lines := strings.Split(text, "\n")
	if top, _ := agyInputBoxBounds(lines); top >= 0 {
		lines = lines[:top]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// agyScreenDelta returns the lines newly appended in after relative to before, tolerating terminal scroll.
func agyScreenDelta(before, after string) string {
	b := agyTranscriptLines(before)
	a := agyTranscriptLines(after)
	if len(b) == 0 {
		return strings.Join(a, "\n")
	}
	for shift := 0; shift <= len(b); shift++ {
		rem := len(b) - shift
		if rem <= len(a) && slices.Equal(b[shift:], a[:rem]) {
			return strings.Join(a[rem:], "\n")
		}
	}
	return strings.Join(a, "\n")
}

// agyPickerOpen reports whether an interactive agy model/effort picker is currently open.
func agyPickerOpen(text string) bool {
	flat := strings.Join(strings.Fields(text), " ")
	if strings.Contains(flat, "Select model") || strings.Contains(flat, "Select a model") {
		return true
	}
	return strings.Contains(flat, "↑/↓ Navigate") && (strings.Contains(flat, "enter Confirm") || strings.Contains(flat, "esc to cancel"))
}

// agyModelOutcome checks newly printed agy output after `/model <model>`.
func agyModelOutcome(delta, model string) error {
	flat := strings.Join(strings.Fields(delta), " ")
	if flat == "" {
		return agySwitchUnconfirmed{kind: "model", value: model}
	}
	if strings.Contains(flat, "Model set to ") || strings.Contains(flat, "Model already set to ") {
		return nil
	}
	lower := strings.ToLower(flat)
	for _, bad := range []string{"invalid model", "not available", "isn't available", "not found", "unknown model", "does not exist"} {
		if strings.Contains(lower, bad) {
			return fmt.Errorf("Antigravity CLI rejected model %q", model)
		}
	}
	return agySwitchUnconfirmed{kind: "model", value: model}
}

// agyEffortOutcome checks newly printed agy output after `/effort <effort>`.
func agyEffortOutcome(delta, effort string) error {
	flat := strings.Join(strings.Fields(delta), " ")
	if flat == "" {
		return agySwitchUnconfirmed{kind: "effort", value: effort}
	}
	lower := strings.ToLower(flat)
	target := strings.ToLower(effort)
	if strings.Contains(lower, "effort set to "+target) || strings.Contains(lower, "effort already set to "+target) {
		return nil
	}
	for _, bad := range []string{"has no \"", "invalid effort", "not supported", "does not support", "not available"} {
		if strings.Contains(lower, bad) {
			return fmt.Errorf("Antigravity CLI rejected effort %q", effort)
		}
	}
	return agySwitchUnconfirmed{kind: "effort", value: effort}
}

func (n *Node) agyTranscriptModel(id, pane string) string {
	conversation := n.agyConversation(id, pane)
	if conversation == "" {
		return ""
	}
	if runtimeRow, _, ok := n.runtimes.rowOf(agyAgent + "-" + conversation); ok && runtimeRow.Model != "" {
		return runtimeRow.Model
	}
	if u, ok := n.agyTranscriptMeta(conversation); ok {
		return u.model
	}
	return ""
}

func (n *Node) noteAgySwitch(id, model, from, effort string, setEffort bool) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.modes == nil {
		n.modes = map[string]sessionModes{}
	}
	m := n.modes[id]
	if model != "" {
		m.Model = &pendingModel{Model: model, From: from, At: now}
		m.Awaited = nil
	}
	if setEffort {
		if effort == "" {
			m.Effort, m.EffortAt = "", time.Time{}
		} else {
			m.Effort, m.EffortAt = effort, now
		}
	}
	n.modes[id] = m
}

func (n *Node) runAgySwitchCommand(ctx context.Context, pane, cmd, before string, check func(delta string) error) (string, error) {
	if err := n.hostPrompt(ctx, pane, cmd); err != nil {
		if r := deliveryRefusal(err); r != nil {
			return before, r
		}
		return before, uncertain(err)
	}
	n.typing.typed(pane, cmd)
	last := before
	var lastErr error
	for range 15 {
		time.Sleep(modelLook)
		s, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil {
			continue
		}
		last = s
		if agyPickerOpen(s) {
			_ = n.sendKeys(ctx, pane, "escape")
			return s, fmt.Errorf("Antigravity CLI opened an interactive picker instead of applying %q; cancelled", cmd)
		}
		err = check(agyScreenDelta(before, s))
		if err == nil {
			return s, nil
		}
		lastErr = err
		var unconfirmed agySwitchUnconfirmed
		if !errors.As(err, &unconfirmed) {
			return s, err
		}
	}
	if lastErr != nil {
		return last, lastErr
	}
	return last, check(agyScreenDelta(before, last))
}

func (n *Node) agyPaneModesCall(ctx context.Context, id string, a host.Agent, params json.RawMessage) error {
	var p struct {
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		Effort         string `json:"effort"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	return n.setAgyPaneModes(ctx, id, a, p.Model, p.PermissionMode, p.Effort)
}

// setAgyPaneModes switches model and/or effort on a live terminal agy session.
func (n *Node) setAgyPaneModes(ctx context.Context, id string, a host.Agent, model, mode, effort string) error {
	model = strings.TrimSpace(model)
	mode = strings.TrimSpace(mode)
	effort = strings.TrimSpace(effort)

	if mode != "" {
		return refusal(proto.CodeModesUnsupported, i18n.T(i18n.Default, "rpc-errors.modes_unsupported"), nil)
	}
	if model == "" && effort == "" {
		return nil
	}

	var modelCmd, effortCmd string
	var err error
	if model != "" {
		if modelCmd, err = agyModelCommand(model); err != nil {
			return err
		}
	}
	if effort != "" {
		if effortCmd, err = agyEffortCommand(effort); err != nil {
			return err
		}
	}
	if err := n.switchingRefusal(id); err != nil {
		return err
	}

	defer n.typing.lock(id)()

	live, ok := n.agent(id)
	if !ok || live.Agent != agyAgent || live.PaneID == "" || live.AgentStatus == proto.StatusOffline || live.AgentStatus == proto.StatusInterrupted {
		return n.noPaneForModes(id)
	}
	if live.AgentStatus == proto.StatusWorking || live.AgentStatus == proto.StatusBlocked || n.workSent.recent(live.PaneID, time.Now()) {
		return refusal(notDelivered, "Agent is busy; wait for the current turn to finish before switching model or effort", nil)
	}
	if err := n.checkPane(ctx, live.PaneID, agyAgent, ""); err != nil {
		return err
	}
	before, err := n.readPane(ctx, live.PaneID, "visible", 60)
	if err != nil {
		return unreadBoxRefusal()
	}
	if agyHasDraft(before) {
		return refusal(screen.InputDraft, "Terminal input box has an unsent draft; clear it before switching model or effort", nil)
	}

	var launcher, stModel string
	if n.state != nil {
		if st, ok := n.state.get(id); ok {
			launcher, stModel = st.Launcher, st.Model
		}
	}
	fromTranscript := n.agyTranscriptModel(id, live.PaneID)
	curModel := cmp.Or(n.shownModel(id, fromTranscript), stModel)
	targetModel := cmp.Or(model, curModel)

	if effort != "" && targetModel != "" {
		if entry, found := n.agyCatalogFor(launcher, targetModel); found {
			if len(entry.efforts) == 0 {
				return fmt.Errorf("Antigravity model %q does not support reasoning effort", targetModel)
			}
			if !slices.Contains(entry.efforts, effort) {
				return fmt.Errorf("Antigravity model %q does not support effort %q (available: %s)", targetModel, effort, strings.Join(entry.efforts, ", "))
			}
		}
	}

	screenNow := before
	if model != "" {
		screenNow, err = n.runAgySwitchCommand(ctx, live.PaneID, modelCmd, screenNow, func(delta string) error {
			return agyModelOutcome(delta, model)
		})
		if err != nil {
			return err
		}
		reconciledEffort := n.reconcileAgyEffort(id, launcher, model)
		n.noteAgySwitch(id, model, fromTranscript, reconciledEffort, true)
		if effort != "" && reconciledEffort == effort {
			effort = ""
		}
	}

	if effort != "" {
		if _, err := n.runAgySwitchCommand(ctx, live.PaneID, effortCmd, screenNow, func(delta string) error {
			return agyEffortOutcome(delta, effort)
		}); err != nil {
			if model != "" {
				n.notifySessions()
			}
			return err
		}
		nextModel := ""
		if active := cmp.Or(model, curModel); active != "" {
			if variant := agyEffortModel(active, effort); variant != active {
				nextModel = variant
			}
		}
		n.noteAgySwitch(id, nextModel, fromTranscript, effort, true)
	}

	n.notifySessions()
	return nil
}

// reconcileAgyEffort computes the effective effort after switching to model: a suffixed variant
// selects its own tier; an unsuffixed model keeps the previous effort only if supported.
func (n *Node) reconcileAgyEffort(id, launcher, model string) string {
	if e := agyModelEffort(model); e != "" {
		return e
	}
	entry, found := n.agyCatalogFor(launcher, model)
	if found {
		if len(entry.efforts) == 0 {
			return ""
		}
		if entry.defaultEffort != "" {
			return entry.defaultEffort
		}
	}
	n.mu.Lock()
	prev := n.modes[id].Effort
	n.mu.Unlock()
	if prev != "" && (!found || slices.Contains(entry.efforts, prev)) {
		return prev
	}
	return ""
}
