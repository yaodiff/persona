package extension

import (
	"encoding/json"
	"slices"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/channel"
)

// Items are the ids of the manifest's contributions to point (nil when it has none there).
func (m *Manifest) Items(point string) []string {
	var items []json.RawMessage
	if json.Unmarshal(m.Contributes[point], &items) != nil {
		return nil
	}
	item, ok := ItemOf(point)
	if !ok {
		return nil
	}
	var out []string
	for _, raw := range items {
		if id, err := item(raw); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// Connectors are the manifest's contributions to connectors.
func (m *Manifest) Connectors() []ConnectorItem {
	var out []ConnectorItem
	_ = json.Unmarshal(m.Contributes["connectors"], &out)
	return out
}

// SendsAsUser are the connectors that say they send messages in the user's name on their own (ConnectorItem.SendsAsUser).
func (m *Manifest) SendsAsUser() []ConnectorItem {
	var out []ConnectorItem
	for _, c := range m.Connectors() {
		if c.SendsAsUser {
			out = append(out, c)
		}
	}
	return out
}

// Recalls are the connectors that say they can take back what they sent (the capability recall): the hub then asks the
// program to, once the user agreed on their own device. Not something to agree to: it adds nothing the program can start.
func (m *Manifest) Recalls() []ConnectorItem {
	var out []ConnectorItem
	for _, c := range m.Connectors() {
		if slices.Contains(c.Capabilities, "recall") {
			out = append(out, c)
		}
	}
	return out
}

// APIs are the manifest's contributions to api.
func (m *Manifest) APIs() []APIItem {
	var out []APIItem
	_ = json.Unmarshal(m.Contributes["api"], &out)
	return out
}

// Messaging are the manifest's contributions to messaging: its messaging sources.
func (m *Manifest) Messaging() []wire.MessagingItem {
	var out []wire.MessagingItem
	_ = json.Unmarshal(m.Contributes["messaging"], &out)
	return out
}

// Assistants are the manifest's contributions to assistant.
func (m *Manifest) Assistants() []AssistantItem {
	var out []AssistantItem
	_ = json.Unmarshal(m.Contributes["assistant"], &out)
	return out
}

// MCPServers are the manifest's contributions to mcp_servers.
func (m *Manifest) MCPServers() []MCPServerItem {
	var out []MCPServerItem
	_ = json.Unmarshal(m.Contributes["mcp_servers"], &out)
	return out
}

// UIs are the manifest's contributions to ui.
func (m *Manifest) UIs() []UIItem {
	var out []UIItem
	_ = json.Unmarshal(m.Contributes["ui"], &out)
	return out
}

// Shortcuts are the manifest's contributions to shortcuts.
func (m *Manifest) Shortcuts() []ShortcutItem {
	var out []ShortcutItem
	_ = json.Unmarshal(m.Contributes["shortcuts"], &out)
	return out
}

// SurfaceShortcuts is the surface of an extension with shortcuts: its actions in Shortcuts and Siri.
const SurfaceShortcuts = "shortcuts"

// Surfaces are the slots outside its own settings the plugin draws in (UISlot.Shown), each once, in UISlots' order,
// then shortcuts when it has some: the user is shown them before installing and agrees again when an upgrade adds one.
func (m *Manifest) Surfaces() []string {
	var out []string
	for _, sl := range UISlots {
		if sl.Shown && slices.ContainsFunc(m.UIs(), func(u UIItem) bool { return u.Placement == sl.Name }) {
			out = append(out, sl.Name)
		}
	}
	if len(m.Shortcuts()) > 0 {
		out = append(out, SurfaceShortcuts)
	}
	return out
}

// Events are the manifest's contributions to events.
func (m *Manifest) Events() []EventsItem {
	var out []EventsItem
	_ = json.Unmarshal(m.Contributes["events"], &out)
	return out
}

// ErrorReports are the manifest's contributions to error_reports.
func (m *Manifest) ErrorReports() []ErrorReportsItem {
	var out []ErrorReportsItem
	_ = json.Unmarshal(m.Contributes["error_reports"], &out)
	return out
}

// AuthRelays are the manifest's contributions to auth_relay.
func (m *Manifest) AuthRelays() []AuthRelayItem {
	var out []AuthRelayItem
	_ = json.Unmarshal(m.Contributes["auth_relay"], &out)
	return out
}

// SecretProviders are the manifest's contributions to secret_providers.
func (m *Manifest) SecretProviders() []SecretProviderItem {
	var out []SecretProviderItem
	_ = json.Unmarshal(m.Contributes["secret_providers"], &out)
	return out
}

// Schemes are the reference schemes the manifest's secret_providers contributions claim.
func (m *Manifest) Schemes() []string {
	var out []string
	for _, p := range m.SecretProviders() {
		out = append(out, p.Schemes...)
	}
	return out
}

// RepositoryProviders are the manifest's contributions to repository_providers that hold to the point's rules. Parse
// refuses a manifest with any other, but a stored one that no longer parses is loaded loosely (fromRow), unchecked: its
// items that break a rule (a built-in GitHub or GitLab host, an upper-case one) claim nothing, so the registry's
// lookups, the install's clash checks and the permission digest never see them.
func (m *Manifest) RepositoryProviders() []RepositoryProviderItem {
	var items []RepositoryProviderItem
	_ = json.Unmarshal(m.Contributes["repository_providers"], &items)
	var out []RepositoryProviderItem
	for _, it := range items {
		if checkRepositoryProvider(it) == nil {
			out = append(out, it)
		}
	}
	return out
}

// RepositoryHosts are the hosts the manifest's repository_providers contributions claim.
func (m *Manifest) RepositoryHosts() []string {
	var out []string
	for _, p := range m.RepositoryProviders() {
		out = append(out, p.Hosts...)
	}
	return out
}

// TextLookups are the manifest's contributions to text_lookup whose languages hold to the point's rules. An omitted
// languages list means ["en"]; any other list claims nothing.
func (m *Manifest) TextLookups() []TextLookupItem {
	var items []TextLookupItem
	_ = json.Unmarshal(m.Contributes["text_lookup"], &items)
	var out []TextLookupItem
	for _, it := range items {
		langs := it.Languages
		if langs == nil {
			langs = []string{"en"}
		}
		if len(langs) == 1 && langs[0] == "en" {
			out = append(out, it)
		}
	}
	return out
}

// Topics are the events the plugin is told about, each once.
func (m *Manifest) Topics() []string {
	var out []string
	for _, e := range m.Events() {
		for _, t := range e.Subscribe {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Quotas are the manifest's contributions to quota.
func (m *Manifest) Quotas() []QuotaItem {
	var out []QuotaItem
	_ = json.Unmarshal(m.Contributes["quota"], &out)
	return out
}

// Usages are the manifest's contributions to usage.
func (m *Manifest) Usages() []UsageItem {
	var out []UsageItem
	_ = json.Unmarshal(m.Contributes["usage"], &out)
	return out
}

// AgentSources are the manifest's contributions to agent_sources.
func (m *Manifest) AgentSources() []AgentSourceItem {
	var out []AgentSourceItem
	_ = json.Unmarshal(m.Contributes["agent_sources"], &out)
	return out
}

// Providers are the manifest's contributions to providers.
func (m *Manifest) Providers() []ProviderItem {
	var out []ProviderItem
	_ = json.Unmarshal(m.Contributes["providers"], &out)
	return out
}

// Channels are the plugin's contributions to channel.
func (m *Manifest) Channels() []channel.Item {
	raw := m.Contributes["channel"]
	if len(raw) == 0 {
		return nil
	}
	var out []channel.Item
	_ = json.Unmarshal(raw, &out)
	return out
}

// Contributions are the ids by extension point, the points this hub has only.
func (m *Manifest) Contributions() map[string][]string {
	out := map[string][]string{}
	for name := range m.Contributes {
		if ids := m.Items(name); len(ids) > 0 {
			out[name] = ids
		}
	}
	return out
}

// Kind is the first extension point the plugin contributes to, by a fixed order: what GET /api/v1/plugins has always
// said as "kind" (voice for a voice plugin).
func (m *Manifest) Kind() string {
	for _, name := range []string{"voice", "connectors", "channel", "api", "quota", "usage", "providers", "agent_sources", "assistant", "ui", "events"} {
		if len(m.Items(name)) > 0 {
			return name
		}
	}
	keys := sortedKeys(m.Contributes)
	if len(keys) > 0 {
		return keys[0]
	}
	if len(m.ExtensionPack) > 0 {
		return "extension_pack"
	}
	return ""
}
