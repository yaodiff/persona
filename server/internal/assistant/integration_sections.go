package assistant

import (
	"cmp"
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func (b *integrationBook) sections(ctx context.Context, node *integrationNode, reads integrationReads) []assistantwire.IntegrationSection {
	out := []assistantwire.IntegrationSection{}
	if node.key.kind == assistantwire.IntegrationKindExtension {
		out = append(out, integrationExtensionSections(ctx, node, reads)...)
	}
	for _, n := range append([]*integrationNode{node}, node.children...) {
		state := n.item.Availability
		if state.State == assistantwire.IntegrationStateModuleOff {
			continue
		}
		switch n.key.kind {
		case assistantwire.IntegrationKindService:
			out = append(out, n.section(assistantwire.IntegrationSectionKindConnection, state, n.readOnly))
			if n.serviceHasChannel {
				out = append(out, n.section(assistantwire.IntegrationSectionKindInboxAndSync, state, false))
			}
		case assistantwire.IntegrationKindChannel:
			out = append(out, n.section(assistantwire.IntegrationSectionKindConversationAndPush, state, n.readOnly))
		case assistantwire.IntegrationKindConnector:
			out = append(out, n.section(assistantwire.IntegrationSectionKindConnection, state, false))
			if n.storesData {
				out = append(out, n.section(assistantwire.IntegrationSectionKindInboxAndSync, state, false))
			}
		case assistantwire.IntegrationKindSubscription, assistantwire.IntegrationKindPushSource:
			out = append(out, n.section(assistantwire.IntegrationSectionKindInboxAndSync, state, false))
		}
		if n.externalSources {
			out = append(out, n.section(assistantwire.IntegrationSectionKindExternalSources, state, n.readOnly))
		}
	}
	slices.SortFunc(out, func(a, b assistantwire.IntegrationSection) int {
		return cmp.Or(cmp.Compare(slices.Index(assistantwire.IntegrationSectionKinds, a.Kind), slices.Index(assistantwire.IntegrationSectionKinds, b.Kind)),
			cmp.Compare(a.Ref.Kind, b.Ref.Kind), cmp.Compare(a.Ref.ID, b.Ref.ID))
	})
	return out
}

func integrationExtensionSections(ctx context.Context, node *integrationNode, reads integrationReads) []assistantwire.IntegrationSection {
	state := node.item.Availability
	out := []assistantwire.IntegrationSection{
		node.section(assistantwire.IntegrationSectionKindInformationAccess, integrationAvailable(), false),
		node.section(assistantwire.IntegrationSectionKindLifecycle, state, node.readOnly),
	}
	ctx, cancel := context.WithTimeout(ctx, integrationReadTimeout)
	defer cancel()
	groups, err := reads.IntegrationConfiguration(ctx, string(node.key.id))
	if err != nil {
		return append(out, node.section(assistantwire.IntegrationSectionKindExtensionConfiguration,
			integrationUnavailable(assistantwire.IntegrationReasonReadFailed), true))
	}
	byKind := make(map[assistantwire.IntegrationSectionKind][]assistantwire.IntegrationConfigurationGroup)
	for _, hint := range groups {
		group := assistantwire.IntegrationConfigurationGroup(hint)
		kind := assistantwire.IntegrationSectionKindExtensionConfiguration
		switch group {
		case assistantwire.IntegrationConfigurationGroupConnection:
			kind = assistantwire.IntegrationSectionKindConnection
		case assistantwire.IntegrationConfigurationGroupChannel:
			kind = assistantwire.IntegrationSectionKindConversationAndPush
		case assistantwire.IntegrationConfigurationGroupInbox:
			kind = assistantwire.IntegrationSectionKindInboxAndSync
		case assistantwire.IntegrationConfigurationGroupAdvanced:
		default:
			group = assistantwire.IntegrationConfigurationGroupGeneral
		}
		if !slices.Contains(byKind[kind], group) {
			byKind[kind] = append(byKind[kind], group)
		}
	}
	for kind, groups := range byKind {
		slices.Sort(groups)
		section := node.section(kind, integrationAvailable(), node.readOnly)
		section.ConfigurationGroups = groups
		out = append(out, section)
	}
	return out
}

func (n *integrationNode) section(kind assistantwire.IntegrationSectionKind, availability assistantwire.IntegrationAvailability, readOnly bool) assistantwire.IntegrationSection {
	edit := assistantwire.IntegrationSectionEditSettings
	if readOnly || availability.Reason == assistantwire.IntegrationReasonReadFailed {
		edit = assistantwire.IntegrationSectionEditReadOnly
		if availability.State == assistantwire.IntegrationStateAvailable {
			availability = integrationReadOnly()
		}
	}
	return assistantwire.IntegrationSection{Kind: kind, Ref: n.item.Ref, Availability: availability, Edit: edit}
}
