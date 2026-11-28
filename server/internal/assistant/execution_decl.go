package assistant

import (
	"cmp"
	"context"
	"maps"
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// executionSettingsKey is assistant.execution (executionSettings).
const executionSettingsKey = "assistant.execution"

// executionSettings is assistant.execution: runtime declarations that add to or replace the built-in adapters' (a
// runtime plugin's own, or a correction of a built-in one), by runtime name.
type executionSettings struct {
	// Runtimes are the declarations.
	Runtimes []proto.RuntimeCapability `json:"runtimes,omitempty"`
}

func (s *Service) executionSettings(ctx context.Context) executionSettings {
	var v executionSettings
	if st := s.store(); st != nil {
		if _, err := memoSetting(ctx, st, executionSettingsKey, &v); err != nil {
			s.logf("execution: read %s: %v", executionSettingsKey, err)
		}
	}
	return v
}

// managedProtocol is the protocol a managed launcher's agent, or its direct API, speaks to its Base URL: the API it
// names, else the first its runtime declares.
func managedProtocol(l proto.ManagedLauncher, caps map[string]proto.RuntimeCapability) proto.WireProtocol {
	switch l.API {
	case "responses":
		return proto.ProtocolOpenAIResponses
	case "chat":
		return proto.ProtocolOpenAIChat
	case "messages":
		return proto.ProtocolAnthropicMessages
	}
	if l.Kind == "api" {
		return proto.ProtocolAnthropicMessages
	}
	if c, ok := caps[l.AgentType()]; ok && len(c.Protocols) > 0 {
		return c.Protocols[0]
	}
	return ""
}

// snapshotRuntime is the runtime a launcher a computer listed runs: its agent, Claude Code when it names none.
func snapshotRuntime(l proto.Launcher) string { return cmp.Or(namedAgent(l.Name, l.Agent), "claude") }

// snapshotChannel is the channel a launcher a computer listed stands for: its runtime's own sign-in when it is the
// runtime itself (named as it, no Base URL of its own, or the built-in companion), else a shell launcher of its own.
func snapshotChannel(l proto.Launcher, runtime string) (string, proto.ChannelKind) {
	if l.BuiltinCompanion || l.Name == runtime && l.BaseURL == "" {
		return proto.NativeChannel(runtime), proto.ChannelNative
	}
	return proto.ShellChannel(l.Name), proto.ChannelExternalShell
}

// execAdd is list with v added at its end unless it is there.
func execAdd(list []string, v string) []string {
	if v == "" || slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// execDecl gathers the three declarations as the hub knows them now. No node is asked: a computer's runtimes and
// sign-ins are the launchers the hub last saw there (as launcherProvider reads them), a managed channel's scope is
// its own. A store that cannot be read gives what could be read.
func (s *Service) execDecl(ctx context.Context) execDecl {
	ctx = withSettingsMemo(ctx)
	d := execDecl{runtimes: mergeRuntimeCaps(builtinRuntimeCaps(), s.executionSettings(ctx).Runtimes),
		channels: map[string]proto.ChannelSupply{}, machines: map[string]proto.MachineReadiness{}}
	addRuntime := func(r string) {
		if _, ok := d.runtimes[r]; !ok && r != "" {
			d.runtimes[r] = proto.RuntimeCapability{Runtime: r}
		}
	}
	aliases := map[string]execAlias{}
	catalog, _ := s.channelCatalog(ctx)
	for _, c := range catalog {
		d.channels[c.Name] = c.supply()
	}
	cfg := s.admissionSettings(ctx)
	stored := s.storedLaunchers(ctx)
	for _, l := range stored {
		if l.Kind == "api" && !messagesAPI(l.API) {
			continue // a Responses or Chat Completions connection runs no runtime: the API purposes call it
		}
		runtime := l.AgentType()
		addRuntime(runtime)
		channel := cmp.Or(l.Channel, l.Name)
		sup, ok := d.channels[channel]
		if !ok || l.Channel == "" {
			p := managedProtocol(l.ManagedLauncher, d.runtimes)
			sup = proto.ChannelSupply{Name: channel, DisplayName: l.DisplayName, Kind: proto.ChannelManaged,
				Models: channelModels(l.Models, p), Rev: l.rev()}
			if p != "" {
				sup.Endpoints = []proto.WireProtocol{p}
			}
			if g, ok := cfg.launcherGroup(l.Name); ok && g.QuotaPool != nil && g.QuotaPool.valid() {
				sup.Quota = &proto.ChannelQuota{Source: g.QuotaPool.Source, Provider: g.QuotaPool.Provider}
			}
		}
		sup.Runtimes, sup.Aliases = execAdd(sup.Runtimes, runtime), execAdd(sup.Aliases, l.Name)
		d.channels[channel] = sup
		aliases[l.Name] = execAlias{name: l.Name, spec: proto.ExecutionSpec{Runtime: runtime, Channel: channel,
			Model: l.Model, Effort: l.Effort}}
	}
	seen, _ := s.zshrcLaunchersLastSeen(ctx)
	for _, dev := range s.devices(ctx) {
		mr := proto.MachineReadiness{Machine: dev.ID, Name: dev.Name, Online: dev.Online}
		for _, l := range stored {
			if l.Kind == "api" && !messagesAPI(l.API) || s.scopeReason(l, dev.ID) != "" {
				continue
			}
			mr.Channels = execAdd(mr.Channels, cmp.Or(l.Channel, l.Name))
		}
		for _, z := range seen {
			if z.MachineID != dev.ID {
				continue
			}
			mr.Known = len(z.Launchers) > 0 || z.Sync.SyncedAt != nil
			for _, l := range z.Launchers {
				if l.Source == "persona" || proto.IsExternalLauncher(l.Name) {
					continue // a managed one is listed above; an external adapter is not a runtime
				}
				runtime := snapshotRuntime(l)
				addRuntime(runtime)
				channel, kind := snapshotChannel(l, runtime)
				mr.Runtimes, mr.Channels = execAdd(mr.Runtimes, runtime), execAdd(mr.Channels, channel)
				sup, ok := d.channels[channel]
				if !ok {
					sup = proto.ChannelSupply{Name: channel, Kind: kind}
				}
				sup.Runtimes, sup.Aliases = execAdd(sup.Runtimes, runtime), execAdd(sup.Aliases, l.Name)
				d.channels[channel] = sup
				if _, taken := aliases[l.Name]; !taken {
					aliases[l.Name] = execAlias{name: l.Name, spec: proto.ExecutionSpec{Runtime: runtime, Channel: channel,
						Model: l.Model}}
				}
			}
		}
		d.machines[dev.ID] = mr
	}
	for _, name := range slices.Sorted(maps.Keys(aliases)) {
		d.aliases = append(d.aliases, aliases[name])
	}
	return d
}

// catalog is the declarations as GET /api/v1/execution/catalog shows them, each list in name order.
func (d execDecl) catalog() proto.ExecutionCatalog {
	out := proto.ExecutionCatalog{Runtimes: []proto.RuntimeCapability{}, Channels: []proto.ChannelSupply{},
		Machines: []proto.MachineReadiness{}, Aliases: []proto.LauncherAlias{}}
	for _, name := range slices.Sorted(maps.Keys(d.runtimes)) {
		out.Runtimes = append(out.Runtimes, d.runtimes[name])
	}
	for _, name := range slices.Sorted(maps.Keys(d.channels)) {
		out.Channels = append(out.Channels, d.channels[name])
	}
	for _, id := range slices.Sorted(maps.Keys(d.machines)) {
		out.Machines = append(out.Machines, d.machines[id])
	}
	for _, a := range d.aliases {
		out.Aliases = append(out.Aliases, proto.LauncherAlias{Launcher: a.name, Spec: a.spec})
	}
	return out
}
