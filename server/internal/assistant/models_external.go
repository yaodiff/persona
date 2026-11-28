package assistant

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// External launchers are machine-local advertisements, never configurable defaults or
// fallback providers. Only the actual main's persisted pick, or its manager-proven clear handoff, grants scope.
func externalProvider(provider string) bool {
	name, ok := strings.CutPrefix(provider, "launcher.")
	return ok && proto.IsExternalLauncher(name)
}

func checkExternalPurpose(b ModelBinding) error {
	if externalProvider(b.Provider) {
		return i18n.Err("external-adapter.purpose_unsupported")
	}
	return nil
}

func externalUnavailableError(code string) error {
	// Never localize or relay arbitrary adapter diagnostics: only protocol codes.
	switch code {
	case "adapter_disabled", "adapter_scope_required", "adapter_purpose_unsupported", "adapter_runtime_unsupported",
		"adapter_platform_unsupported", "adapter_sandbox_unsupported", "adapter_target_not_found", "adapter_model_not_found",
		"adapter_protocol_unsupported", "adapter_config_conflict", "adapter_credentials_missing", "adapter_revision_mismatch",
		"adapter_timeout", "adapter_inventory_invalid", "adapter_unavailable", "adapter_probe_unsupported", "adapter_busy":
		return i18n.Err("external-adapter." + strings.TrimPrefix(code, "adapter_"))
	}
	return i18n.Err("external-adapter.target_not_found")
}

func externalLauncherError(l proto.Launcher) error {
	e := l.External
	if !proto.IsExternalLauncher(l.Name) || l.Source != proto.ExternalLauncherSource || e == nil ||
		e.AdapterID == "" || e.ID == "" || e.Revision == "" ||
		l.Name != proto.ExternalLauncherName(e.AdapterID, e.ID) {
		return i18n.Err("external-adapter.target_not_found")
	}
	if e.UnavailableCode != "" {
		return externalUnavailableError(e.UnavailableCode)
	}
	if e.CredentialState == "missing" {
		return i18n.Err("external-adapter.credentials_missing")
	}
	if e.CredentialState != "configured" && e.CredentialState != "unknown" {
		return i18n.Err("external-adapter.target_not_found")
	}
	if e.Engine != "claude-code" || !slices.Contains(e.Capabilities.Contexts, "main-chat") {
		return i18n.Err("external-adapter.purpose_unsupported")
	}
	if !slices.Contains(e.Capabilities.Platforms, "posix") {
		return i18n.Err("external-adapter.platform_unsupported")
	}
	if !slices.Contains(e.Capabilities.Runtimes, "cli") {
		return i18n.Err("external-adapter.runtime_unsupported")
	}
	return nil
}

func (s *Service) externalLauncherRefusal(_ context.Context, l proto.Launcher) error {
	return externalLauncherError(l)
}

type externalDirectoryRefresh struct {
	done  chan struct{}
	again bool
	err   error
}

func externalInventoryCode(err error) string {
	var failure *rpc.Error
	if errors.As(err, &failure) && strings.HasPrefix(failure.Code, "adapter_") {
		switch failure.Code {
		case "adapter_disabled", "adapter_timeout", "adapter_inventory_invalid", "adapter_unavailable", "adapter_protocol_unsupported",
			"adapter_config_conflict", "adapter_credentials_missing", "adapter_target_not_found", "adapter_revision_mismatch":
			return failure.Code
		}
	}
	return "adapter_target_not_found"
}

// listExternalLaunchers never asks a legacy node to interpret a new selector: the authenticated
// handshake must attest an enabled local registry before this source-only RPC is sent.
func (s *Service) listExternalLaunchers(ctx context.Context, d Device, refresh bool) (launcherListing, error) {
	var res launcherListing
	if !d.ExternalInventory {
		return res, &rpc.Error{Code: "adapter_disabled", Message: "adapter_disabled"}
	}
	if !d.Online {
		return res, &rpc.Error{Code: "adapter_target_not_found", Message: "adapter_target_not_found"}
	}
	cctx, cancel := context.WithTimeout(ctx, launcherSyncTimeout)
	defer cancel()
	params := map[string]any{"source": proto.ExternalLauncherSource}
	if refresh {
		params["refresh"] = true
	}
	if err := s.call(cctx, d.ID, DeviceLaunchersList, params, &res); err != nil {
		code := externalInventoryCode(err)
		return res, &rpc.Error{Code: code, Message: code}
	}
	if res.Source != proto.ExternalLauncherSource {
		return launcherListing{}, &rpc.Error{Code: "adapter_protocol_unsupported", Message: "adapter_protocol_unsupported"}
	}
	for _, l := range res.Launchers {
		if !proto.IsExternalLauncher(l.Name) {
			return launcherListing{}, &rpc.Error{Code: "adapter_inventory_invalid", Message: "adapter_inventory_invalid"}
		}
	}
	return res, nil
}

// mergeLauncherSource replaces just one source. In particular, a fast external answer
// cannot turn a failed shell read into an authoritative empty ordinary directory.
func mergeLauncherSource(current, incoming []proto.Launcher, external bool) []proto.Launcher {
	out := make([]proto.Launcher, 0, len(current)+len(incoming))
	isExternal := func(l proto.Launcher) bool {
		return proto.IsExternalLauncher(l.Name) || l.Source == proto.ExternalLauncherSource
	}
	for _, l := range current {
		if isExternal(l) != external {
			out = append(out, l)
		}
	}
	for _, l := range incoming {
		if isExternal(l) == external {
			out = append(out, l)
		}
	}
	return out
}

func (s *Service) saveExternalDirectory(ctx context.Context, d Device, res launcherListing, failure error) error {
	// Restore ordinary last-seen entries before the first independent source update.
	s.zshrcLaunchersLastSeen(ctx)
	c := &s.models
	c.zshrcWrite.Lock()
	defer c.zshrcWrite.Unlock()
	c.zshrcMu.Lock()
	seen := maps.Clone(c.zshrc)
	c.zshrcMu.Unlock()
	if seen == nil {
		seen = map[string][]proto.Launcher{}
		if store := s.store(); store != nil {
			if _, err := store.Setting(ctx, launchersZshrcKey, &seen); err != nil {
				return err // never replace an ordinary snapshot that could not be read
			}
		}
		if seen == nil {
			seen = map[string][]proto.Launcher{}
		}
	}
	incoming := res.Launchers
	if failure != nil {
		incoming = nil
		for _, l := range seen[d.ID] {
			if proto.IsExternalLauncher(l.Name) && l.External != nil {
				e := *l.External
				e.UnavailableCode, e.Stale = externalInventoryCode(failure), true
				l.External = &e
				incoming = append(incoming, l)
			}
		}
	}
	seen[d.ID] = mergeLauncherSource(seen[d.ID], incoming, true)
	if store := s.store(); store != nil {
		if err := store.PutSetting(ctx, launchersZshrcKey, seen); err != nil {
			return err
		}
	}
	c.zshrcMu.Lock()
	c.zshrc = seen
	c.zshrcMu.Unlock()
	return nil
}

// syncExternalDirectory is independent of the ordinary shell refresher. Only enabled nodes
// enter it; concurrent UI reads and source events join one bounded refresh for this machine.
func (s *Service) syncExternalDirectory(ctx context.Context, d Device, refresh bool) error {
	if !d.ExternalInventory {
		return nil
	}
	c := &s.models
	c.zshrcMu.Lock()
	if pending := c.externalRefresh[d.ID]; pending != nil {
		if refresh {
			pending.again = true
		}
		c.zshrcMu.Unlock()
		select {
		case <-pending.done:
			return pending.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	pending := &externalDirectoryRefresh{done: make(chan struct{})}
	if c.externalRefresh == nil {
		c.externalRefresh = map[string]*externalDirectoryRefresh{}
	}
	c.externalRefresh[d.ID] = pending
	c.zshrcMu.Unlock()
	for {
		res, err := s.listExternalLaunchers(ctx, d, refresh)
		if saveErr := s.saveExternalDirectory(ctx, d, res, err); err == nil {
			err = saveErr
		}
		c.zshrcMu.Lock()
		if pending.again && ctx.Err() == nil {
			pending.again, refresh = false, true
			c.zshrcMu.Unlock()
			continue
		}
		pending.err = err
		delete(c.externalRefresh, d.ID)
		close(pending.done)
		c.zshrcMu.Unlock()
		return err
	}
}

func (s *Service) refreshExternalDirectory(ctx context.Context, d Device, refresh bool) {
	if !d.ExternalInventory {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launcherSyncTimeout+5*time.Second)
		defer cancel()
		_ = s.syncExternalDirectory(ctx, d, refresh)
	}()
}

// externalPickerLaunchers combines the ordinary last-seen snapshot with the independently
// loaded source. A slow or failed login shell never holds an enabled external directory.
func (s *Service) externalPickerLaunchers(ctx context.Context, d Device, refresh bool) ([]ConfiguredLauncher, error) {
	s.zshrcLaunchersLastSeen(ctx)
	s.refreshZshrcLaunchers(ctx, d.ID)
	if err := s.syncExternalDirectory(ctx, d, refresh); err != nil {
		return nil, err
	}
	c := &s.models
	c.zshrcMu.Lock()
	known := slices.Clone(c.zshrc[d.ID])
	c.zshrcMu.Unlock()
	return s.pickerLaunchersSeen(ctx, d.ID, known, true), nil
}

// externalTarget reads this computer's advertisement, without a global provider view or
// hub-configured overrides. Missing, disabled and unreadable sources all fail closed.
func (s *Service) externalTarget(ctx context.Context, machine, name string) (*proto.ExternalLauncher, error) {
	d, ok := s.device(ctx, machine)
	if !ok {
		return nil, i18n.Err("external-adapter.target_not_found")
	}
	res, err := s.listExternalLaunchers(ctx, d, false)
	if err != nil {
		return nil, externalUnavailableError(externalInventoryCode(err))
	}
	if proto.IsExternalLauncher(name) && !slices.ContainsFunc(res.Launchers, func(l proto.Launcher) bool { return l.Name == name }) {
		// A missing cold target gets one bounded source-only refresh, never a shell or host inventory read.
		res, err = s.listExternalLaunchers(ctx, d, true)
		if err != nil {
			return nil, externalUnavailableError(externalInventoryCode(err))
		}
	}
	for _, l := range res.Launchers {
		if l.Name == name {
			if err := s.externalLauncherRefusal(ctx, l); err != nil {
				return nil, err
			}
			return l.External, nil
		}
	}
	return nil, i18n.Err("external-adapter.target_not_found")
}

// externalLauncherAgent derives the agent kind only from the validated machine-local source,
// never from shell discovery, a configured override or a launcher's cached model metadata.
func (s *Service) externalLauncherAgent(ctx context.Context, machine, name string) (string, error) {
	e, err := s.externalTarget(ctx, machine, name)
	if err != nil {
		return "", err
	}
	if e.Engine != "claude-code" {
		return "", i18n.Err("external-adapter.purpose_unsupported")
	}
	return "claude", nil
}

func externalModel(e *proto.ExternalLauncher, model string) (string, error) {
	for _, m := range e.Models {
		if proto.ValidModelID(m.ID) && (model != "" && m.ID == model || model == "" && m.Default) {
			return m.ID, nil
		}
	}
	return "", i18n.Err("external-adapter.model_not_found")
}

func (s *Service) externalMainScope(ctx context.Context, machine, sid string) bool {
	cfg, known := s.savedSettings(ctx)
	cfg = s.effective(ctx, cfg)
	if !known || sid == "" || cfg.Main != sid || cfg.mainRouteMachine() != machine || s.isWorkMachine(machine) {
		return false
	}
	return s.externalSessionScope(ctx, machine, sid)
}

func (s *Service) externalSessionScope(ctx context.Context, machine, sid string) bool {
	_, guest, err := s.guestOf(ctx, machine, sid)
	if err != nil || guest {
		return false
	}
	if sess, ok := s.nodeSession(machine, sid); ok {
		if sess.PaneID != "" || sess.Origin == OriginMeeting || sess.Space != "" {
			return false
		}
		if sess.Cwd != "" {
			m, known := s.fleetMachine(machine)
			if !known || !m.isAssistant(sess) {
				return false
			}
		}
	}
	return true
}

type externalMainStartKey struct{}

type externalMainStart struct {
	session string
	pick    modelPick
}

// externalMainStartScope is a one-start authority made only by startMain under its lock.
// The persisted handoff, not a client's main flag or session ID, proves the cleared choice.
func (s *Service) externalMainStartScope(ctx context.Context, machine, sid string, pick modelPick) bool {
	start, ok := ctx.Value(externalMainStartKey{}).(externalMainStart)
	if !ok || start.session != sid || start.pick != pick || pick.MachineID != machine || sid == "" || s.isWorkMachine(machine) {
		return false
	}
	cfg, known := s.savedSettings(ctx)
	cfg = s.effective(ctx, cfg)
	if !known || cfg.Main != "" && (cfg.Main != sid || cfg.mainRouteMachine() != machine) {
		return false
	}
	h, err := s.readHandoff(ctx)
	return err == nil && h.From == "" && h.For == sid && h.Pick != nil && *h.Pick == pick && s.externalSessionScope(ctx, machine, sid)
}

// prepareExternalMain reserves the cleared choice's new ID before any node call. A failed
// start keeps this reservation, so retries and manager restarts cannot change its source.
func (s *Service) prepareExternalMain(ctx context.Context, p StartParams, h mainHandoff) (context.Context, StartParams, mainHandoff, error) {
	if h.Pick == nil || h.Pick.Model == "" {
		return ctx, p, h, i18n.Err("external-adapter.scope_required")
	}
	if h.For == "" {
		h.For = newGuestSessionID()
		if err := s.saveHandoff(ctx, h); err != nil {
			return ctx, p, h, err
		}
	}
	ctx = context.WithValue(ctx, externalMainStartKey{}, externalMainStart{session: h.For, pick: *h.Pick})
	if !s.externalMainStartScope(ctx, h.Pick.MachineID, h.For, *h.Pick) {
		return ctx, p, h, i18n.Err("external-adapter.scope_required")
	}
	conflict := false
	if err := s.updateModelPick(ctx, h.For, func(current modelPick, had bool) (*modelPick, bool) {
		if had && current != *h.Pick {
			conflict = true
			return nil, false // the user changed this reserved conversation while its start was being retried
		}
		return h.Pick, true
	}); err != nil {
		return ctx, p, h, err
	}
	if conflict {
		return ctx, p, h, i18n.Err("external-adapter.scope_required")
	}
	ctx, err := s.externalPickContext(ctx, h.Pick.MachineID, h.For, *h.Pick)
	if err != nil {
		return ctx, p, h, err
	}
	p.MachineID, p.NewSessionID = h.Pick.MachineID, h.For
	return ctx, p, h, nil
}

func (s *Service) resolveExternalPick(ctx context.Context, sid string, pick modelPick) (modelPick, error) {
	if !s.externalMainScope(ctx, pick.MachineID, sid) && !s.externalMainStartScope(ctx, pick.MachineID, sid, pick) {
		return pick, i18n.Err("external-adapter.scope_required")
	}
	e, err := s.externalTarget(ctx, pick.MachineID, pick.Launcher)
	if err != nil {
		return pick, err
	}
	// An external default is a selection made now, not a future directory default.
	// Persist its exact raw ID so later inventory changes cannot silently switch it.
	pick.Model, err = externalModel(e, pick.Model)
	return pick, err
}

type externalPickKey struct{}

type externalPickScope struct {
	pick      modelPick
	execution proto.ExternalExecution
}

// externalPickContext can only be made from the manager's kept selection and routing.
// Neither a request's external/source/is_main nor a carried prewarm pick is authority.
func (s *Service) externalPickContext(ctx context.Context, machine, sid string, pick modelPick) (context.Context, error) {
	kept, ok := s.modelPickOf(ctx, machine, sid)
	if !ok || kept != pick || !s.externalMainScope(ctx, machine, sid) && !s.externalMainStartScope(ctx, machine, sid, pick) {
		return ctx, i18n.Err("external-adapter.scope_required")
	}
	if pick.Model == "" {
		return ctx, i18n.Err("external-adapter.model_not_found")
	}
	e, err := s.externalTarget(ctx, machine, pick.Launcher)
	if err != nil {
		return ctx, err
	}
	if _, err := externalModel(e, pick.Model); err != nil {
		return ctx, err
	}
	scope := externalPickScope{pick: pick, execution: proto.ExternalExecution{
		Scope: proto.ExternalMainChatScope, SessionID: sid,
		AdapterID: e.AdapterID, TargetID: e.ID, Revision: e.Revision,
	}}
	return context.WithValue(ctx, externalPickKey{}, scope), nil
}

// applyExternalScope runs at the final model boundary. The directory revision is
// deliberately outside ModelBinding and its fingerprint: refreshing a recipe must not
// restart a live process. The node pins the actual recipe until that process ends.
func (s *Service) applyExternalScope(ctx context.Context, id string, params map[string]any, b ModelBinding) error {
	scope, ok := ctx.Value(externalPickKey{}).(externalPickScope)
	starting := ok && s.externalMainStartScope(ctx, scope.pick.MachineID, scope.execution.SessionID, scope.pick)
	if !ok || (id != "assistant" && id != purposeMain) || launcherProvider(scope.pick.Launcher) != b.Provider || scope.pick.Model != b.Model ||
		!s.externalMainScope(ctx, scope.pick.MachineID, scope.execution.SessionID) && !starting {
		return i18n.Err("external-adapter.scope_required")
	}
	kept, persisted := s.modelPickOf(ctx, scope.pick.MachineID, scope.execution.SessionID)
	if !persisted || kept != scope.pick {
		return i18n.Err("external-adapter.scope_required")
	}
	if raw, present := params["runtime"]; present {
		if runtime, ok := raw.(string); !ok || runtime != "" && runtime != "cli" {
			return i18n.Err("external-adapter.runtime_unsupported")
		}
	}
	if _, guest := params["guest"]; guest {
		return i18n.Err("external-adapter.scope_required")
	}
	if sid, _ := params["new_session_id"].(string); starting && sid != scope.execution.SessionID || !starting && sid != "" {
		return i18n.Err("external-adapter.scope_required")
	}
	binding := scope.execution
	params["external"], params["is_main"], params["launch"] = &binding, true, "headless"
	if starting {
		delete(params, "session_id") // new_session_id starts, rather than resumes, the manager-reserved conversation
	} else {
		params["session_id"] = binding.SessionID
	}
	return nil
}

// externalLauncherSet reads only the existing snapshot, outside health's lock. A
// historical health record cannot turn a known external source into a repair agent.
func (s *Service) externalLauncherSet(machine string) map[string]bool {
	c := &s.models
	c.zshrcMu.Lock()
	defer c.zshrcMu.Unlock()
	var names map[string]bool
	for _, l := range c.zshrc[machine] {
		if proto.IsExternalLauncher(l.Name) || l.Source == proto.ExternalLauncherSource {
			if names == nil {
				names = map[string]bool{}
			}
			names[l.Name] = true
		}
	}
	return names
}

// launchersChanged consumes the generic node event without blocking its read loop.
// Model caches are machine-scoped; the existing snapshot refresher owns persistence.
func (s *Service) launchersChanged(machine string) {
	s.invalidateLauncherListCache(machine)
	c := &s.models
	c.cacheMu.Lock()
	for key := range c.cacheAt {
		if strings.HasPrefix(key, machine+"\x00") {
			delete(c.cacheAt, key)
			delete(c.have, key)
		}
	}
	c.cacheMu.Unlock()
	c.zshrcMu.Lock()
	if c.zshrcRefreshing {
		c.zshrcRefreshAgain = true
	}
	c.zshrcMu.Unlock()
	if s.hub == nil && s.devs == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), launcherSyncTimeout+5*time.Second)
		defer cancel()
		if d, ok := s.device(ctx, machine); ok && d.ExternalInventory {
			_ = s.syncExternalDirectory(ctx, d, true)
		}
	}()
	s.refreshZshrcLaunchers(context.Background(), machine)
}
