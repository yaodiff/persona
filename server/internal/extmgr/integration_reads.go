package extmgr

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extmgr/mailport"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/imessagewire"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

func (h *Manager) IntegrationExtensions(ctx context.Context) ([]assistantwire.IntegrationExtensionRead, error) {
	if !h.ready(ctx) || h.extensions == nil {
		return nil, errAssistantOffline
	}
	ps := h.extensions
	var out []assistantwire.IntegrationExtensionRead
	for _, p := range ps.reg.Snapshots() {
		v := ps.summaryView(ctx, p)
		origin := assistantwire.IntegrationOriginInstalledExtension
		switch p.Record.Source {
		case extension.SourceBuiltin:
			origin = assistantwire.IntegrationOriginBuiltinExtension
		case extension.SourceHubJSON:
			origin = assistantwire.IntegrationOriginConfiguredExtension
		}
		item := assistantwire.IntegrationExtensionRead{ID: assistantwire.IntegrationResourceID(v.ID), Name: v.Name,
			Origin: origin, Availability: extensionIntegrationState(v.Status),
			Contributions: v.Contributes, ReadOnly: p.ReadOnly()}
		for _, ch := range p.Manifest.Channels() {
			if ch.Connector != "" {
				item.ChannelConnectors = append(item.ChannelConnectors, ch.Connector)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// IntegrationConfiguration reads the group hints of one readable settings form.
func (h *Manager) IntegrationConfiguration(ctx context.Context, id string) ([]string, error) {
	if !h.ready(ctx) || h.extensions == nil {
		return nil, errAssistantOffline
	}
	p := h.extensions.reg.Snapshot(id)
	if p == nil {
		return nil, errors.New("extension settings unavailable")
	}
	var groups []string
	if form := withoutIPhonePhones(p, p.Schema); form != nil {
		for _, field := range form.Fields {
			groups = append(groups, field.Section)
		}
	}
	if !p.ReadOnly() && len(p.Full.Secrets) > 0 {
		groups = append(groups, "")
	}
	return groups, nil
}

// IntegrationChannels uses the installed settings views. The Lark bot is read on its own and the market is on
// the installation path, so neither can delay this read.
func (h *Manager) IntegrationChannels(ctx context.Context) ([]channel.View, error) {
	if !h.ready(ctx) {
		return nil, errAssistantOffline
	}
	out := []channel.View{appChannel(h.botLang(ctx))}
	if v, ok := h.voiceChannel(ctx); ok {
		out = append(out, v)
	}
	for _, pc := range h.extensionChannels() {
		out = append(out, h.extensionChannelView(ctx, pc))
	}
	return out, nil
}

func (h *Manager) IntegrationConnectors(ctx context.Context) ([]assistantwire.IntegrationConnectorRead, error) {
	if !h.ready(ctx) || h.connectors == nil {
		return nil, errAssistantOffline
	}
	var out []assistantwire.IntegrationConnectorRead
	for _, c := range h.connectors.All() {
		manifest := c.Manifest()
		v := assistantwire.IntegrationConnectorRead{ID: assistantwire.IntegrationResourceID(c.ID()), Name: connector.DisplayName(ctx, c),
			Availability: connectorIntegrationState(connector.StatusOf(ctx, c).State), Builtin: !manifest.Proxy,
			StoresData: len(connector.Capabilities(c)) == 0 ||
				manifest.Has(connector.CapArchive) || manifest.Has(connector.CapInbox) || manifest.InboxSwitch}
		if owner := h.extensions.ownerOf(c); owner != nil {
			v.OwnerExtension, v.Builtin = assistantwire.IntegrationResourceID(owner.id), false
		} else if c.ID() == imessagewire.ID && h.extensions != nil {
			// applyIMessage registers the host adapter for the built-in settings extension.
			if _, ok := c.(imessagewire.Remote); ok {
				if p := h.extensions.reg.Snapshot(imessageExtensionID); p != nil && p.Record.Source == extension.SourceBuiltin {
					v.OwnerExtension = assistantwire.IntegrationResourceID(p.Record.ID)
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (h *Manager) IntegrationSubscriptions(ctx context.Context) ([]assistantwire.IntegrationSubscriptionRead, error) {
	if !h.ready(ctx) || h.subs == nil {
		return nil, errAssistantOffline
	}
	list, _, err := h.subs.List(ctx, subscriptions.Filter{})
	if err != nil {
		return nil, err
	}
	out := make([]assistantwire.IntegrationSubscriptionRead, 0, len(list))
	for _, sub := range list {
		push := subscriptions.IsPush(sub) || sub.RecipeOrigin == subscriptions.OriginPush
		name := firstNonEmpty(sub.Label, sub.SourceName)
		if push && name == "" {
			name = i18n.T(h.contextLang(ctx), "subscriptions-pages.list.push_section")
		}
		out = append(out, assistantwire.IntegrationSubscriptionRead{ID: assistantwire.IntegrationResourceID(sub.ID), Name: name,
			Connector: assistantwire.IntegrationResourceID(sub.Connector), Extension: assistantwire.IntegrationResourceID(sub.Extension),
			Availability: subscriptionIntegrationState(sub.State), Push: push})
	}
	return out, nil
}

// IntegrationServices describes the host registrations and their provider switches, including dormant routes
// so an existing settings link can report the module state.
func (h *Manager) IntegrationServices(ctx context.Context) []assistantwire.IntegrationServiceRead {
	l := h.contextLang(ctx)
	botStatus, botReadOnly := h.integrationBotAvailability(ctx)
	return []assistantwire.IntegrationServiceRead{
		{ID: "feishu-bot", Name: i18n.T(l, "hub-channels.feishu_name"), ModuleFeature: "module.lark",
			Channel: channel.Feishu, Connector: "larkcli", Capability: assistantwire.IntegrationFilterCapabilityChannels,
			Availability: botStatus, ConnectionReadOnly: botReadOnly},
		{ID: "mail", Name: i18n.T(l, "hub-self_settings.title_mail"), ModuleFeature: "module.mail",
			Connector: "mail", Capability: assistantwire.IntegrationFilterCapabilityData,
			Availability: h.integrationMailAvailability(ctx)},
	}
}

func (h *Manager) integrationBotAvailability(ctx context.Context) (assistantwire.IntegrationAvailability, bool) {
	if !botport.Available(h.modules()) {
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateModuleOff}, false
	}
	if !h.ready(ctx) {
		return integrationReadFailed(), false
	}
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := h.loadBotsLocked(ctx); err != nil {
		return integrationReadFailed(), false
	}
	appID, _, _, fromConfig, err := h.botCredentialsLocked()
	switch {
	case err != nil:
		return integrationReadFailed(), fromConfig
	case appID == "" || b.set.Owner == nil:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateNeedsSetup}, fromConfig
	case !b.set.Enabled:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateDisabled}, fromConfig
	case b.conn == nil || !b.conn.State().Connected:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateUnavailable, Reason: assistantwire.IntegrationReasonNotConnected}, fromConfig
	default:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateAvailable}, fromConfig
	}
}

func (h *Manager) integrationMailAvailability(ctx context.Context) assistantwire.IntegrationAvailability {
	if mailport.Factory(h.modules()) == nil {
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateModuleOff}
	}
	if !h.ready(ctx) || h.mail == nil {
		return integrationReadFailed()
	}
	accounts, err := h.mail.ListAccounts()
	switch {
	case err != nil:
		return integrationReadFailed()
	case len(accounts) == 0:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateNeedsSetup}
	}
	for _, account := range accounts {
		if account.Status == "error" {
			return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateUnavailable, Reason: assistantwire.IntegrationReasonRuntimeError}
		}
	}
	return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateAvailable}
}

func integrationReadFailed() assistantwire.IntegrationAvailability {
	return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateUnavailable, Reason: assistantwire.IntegrationReasonReadFailed}
}

func integrationAvailable() assistantwire.IntegrationAvailability {
	return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateAvailable}
}

func integrationUnavailable(reason assistantwire.IntegrationReason) assistantwire.IntegrationAvailability {
	return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateUnavailable, Reason: reason}
}

func extensionIntegrationState(status string) assistantwire.IntegrationAvailability {
	switch status {
	case "needs_setup":
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateNeedsSetup}
	case "disabled":
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateDisabled}
	case "incompatible":
		return integrationUnavailable(assistantwire.IntegrationReasonIncompatible)
	case host.StateError:
		return integrationUnavailable(assistantwire.IntegrationReasonRuntimeError)
	case host.StateRunning, host.StateStopped:
		return integrationAvailable()
	default:
		return integrationUnavailable(assistantwire.IntegrationReasonNotConnected)
	}
}

func connectorIntegrationState(status string) assistantwire.IntegrationAvailability {
	switch status {
	case connector.StateOK:
		return integrationAvailable()
	case connector.StateNotConfigured, connector.StateNotSignedIn, connector.StateNoPermission:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateNeedsSetup}
	case connector.StateDisabled:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateDisabled}
	case connector.StateRateLimited:
		return integrationUnavailable(assistantwire.IntegrationReasonRateLimited)
	default:
		return integrationUnavailable(assistantwire.IntegrationReasonRuntimeError)
	}
}

func subscriptionIntegrationState(state string) assistantwire.IntegrationAvailability {
	switch state {
	case subscriptions.StateOK:
		return integrationAvailable()
	case subscriptions.StateOff:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateDisabled}
	case subscriptions.StateNeedsRecipe:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateNeedsSetup}
	case subscriptions.StateNotConnected:
		return integrationUnavailable(assistantwire.IntegrationReasonNotConnected)
	case subscriptions.StateRateLimited:
		return integrationUnavailable(assistantwire.IntegrationReasonRateLimited)
	default:
		return integrationUnavailable(assistantwire.IntegrationReasonRuntimeError)
	}
}
