package assistant

import (
	"cmp"
	"errors"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
)

var errIntegrationRead = errors.New("integration settings unavailable")

type integrationInput struct {
	extensions       []assistantwire.IntegrationExtensionRead
	channels         []channel.View
	connectors       []assistantwire.IntegrationConnectorRead
	subscriptions    []assistantwire.IntegrationSubscriptionRead
	services         []assistantwire.IntegrationServiceRead
	extensionsErr    error
	channelsErr      error
	connectorsErr    error
	subscriptionsErr error
}

type integrationKey struct {
	kind assistantwire.IntegrationKind
	id   assistantwire.IntegrationResourceID
}

func integrationResource(kind assistantwire.IntegrationKind, id assistantwire.IntegrationResourceID) integrationKey {
	return integrationKey{kind: kind, id: id}
}

type integrationNode struct {
	key               integrationKey
	item              assistantwire.IntegrationCatalogItem
	children          []*integrationNode
	readOnly          bool
	externalSources   bool
	storesData        bool
	serviceHasChannel bool
}

// All keys in the book belong to one assistant. Producer ids have meaning only with their resource kind.
type integrationBook struct {
	manager          assistantwire.IntegrationManagerID
	nodes            map[integrationKey]*integrationNode
	owners           map[integrationKey]integrationKey
	declared         map[integrationKey][]integrationKey
	declaresKind     map[integrationKey]map[assistantwire.IntegrationKind]bool
	serviceConnector map[integrationKey]integrationKey
	availability     []assistantwire.IntegrationSourceAvailability
	readFailed       map[assistantwire.IntegrationSource]bool
}

func newIntegrationBook(manager assistantwire.IntegrationManagerID, input integrationInput) *integrationBook {
	b := &integrationBook{
		manager: manager, nodes: make(map[integrationKey]*integrationNode), owners: make(map[integrationKey]integrationKey),
		declared: make(map[integrationKey][]integrationKey), declaresKind: make(map[integrationKey]map[assistantwire.IntegrationKind]bool),
		serviceConnector: make(map[integrationKey]integrationKey), readFailed: make(map[assistantwire.IntegrationSource]bool),
	}
	b.recordRead(assistantwire.IntegrationSourceExtensions, input.extensionsErr)
	b.recordRead(assistantwire.IntegrationSourceChannels, input.channelsErr)
	b.recordRead(assistantwire.IntegrationSourceConnectors, input.connectorsErr)
	b.recordRead(assistantwire.IntegrationSourceSubscriptions, input.subscriptionsErr)
	for _, v := range input.services {
		b.addService(v)
	}
	if input.extensionsErr == nil {
		for _, v := range input.extensions {
			b.addExtension(v)
		}
	}
	if input.channelsErr == nil {
		b.addChannels(input.channels)
	}
	if input.connectorsErr == nil {
		for _, v := range input.connectors {
			b.addConnector(v)
		}
	}
	if input.subscriptionsErr == nil {
		for _, v := range input.subscriptions {
			b.addSubscription(v)
		}
	}
	return b
}

func (b *integrationBook) recordRead(source assistantwire.IntegrationSource, err error) {
	state := integrationAvailable()
	if err != nil {
		b.readFailed[source] = true
		state = integrationUnavailable(assistantwire.IntegrationReasonReadFailed)
	}
	b.availability = append(b.availability, assistantwire.IntegrationSourceAvailability{Source: source, Availability: state})
}

func (b *integrationBook) failed(kind assistantwire.IntegrationKind) bool {
	switch kind {
	case assistantwire.IntegrationKindExtension:
		return b.readFailed[assistantwire.IntegrationSourceExtensions]
	case assistantwire.IntegrationKindChannel:
		return b.readFailed[assistantwire.IntegrationSourceChannels]
	case assistantwire.IntegrationKindConnector:
		return b.readFailed[assistantwire.IntegrationSourceConnectors]
	case assistantwire.IntegrationKindPushSource, assistantwire.IntegrationKindSubscription:
		return b.readFailed[assistantwire.IntegrationSourceSubscriptions]
	default:
		return false
	}
}

func (b *integrationBook) add(key integrationKey, name string, origin assistantwire.IntegrationOrigin, availability assistantwire.IntegrationAvailability, caps ...assistantwire.IntegrationFilterCapability) *integrationNode {
	if name == "" {
		name = string(key.id)
	}
	ref := assistantwire.IntegrationRef{Manager: b.manager, Kind: key.kind, ID: key.id}
	n := &integrationNode{key: key, item: assistantwire.IntegrationCatalogItem{Ref: ref, Name: name, Origin: origin, Availability: availability}}
	n.addCapabilities(caps...)
	b.nodes[key] = n
	return n
}

func (n *integrationNode) addCapabilities(caps ...assistantwire.IntegrationFilterCapability) {
	for _, cap := range caps {
		if !slices.Contains(n.item.Capabilities, cap) {
			n.item.Capabilities = append(n.item.Capabilities, cap)
		}
	}
}

func (b *integrationBook) canonical(key integrationKey) integrationKey {
	if owner, ok := b.owners[key]; ok {
		return owner
	}
	return key
}

func (b *integrationBook) attach(n *integrationNode, owner integrationKey) {
	owner = b.canonical(owner)
	root := b.nodes[owner]
	if root == nil || root == n {
		return
	}
	b.owners[n.key] = owner
	root.children = append(root.children, n)
	root.addCapabilities(n.item.Capabilities...)
}

func (b *integrationBook) addService(v assistantwire.IntegrationServiceRead) {
	key := integrationResource(assistantwire.IntegrationKindService, v.ID)
	n := b.add(key, v.Name, assistantwire.IntegrationOriginBuiltinService, v.Availability, v.Capability)
	n.readOnly = v.ConnectionReadOnly
	b.availability = append(b.availability, assistantwire.IntegrationSourceAvailability{
		Source: assistantwire.IntegrationSourceService, Ref: &n.item.Ref, ModuleFeature: v.ModuleFeature, Availability: v.Availability,
	})
	if v.Connector != "" {
		b.serviceConnector[integrationResource(assistantwire.IntegrationKindConnector, v.Connector)] = key
	}
	if v.Channel != "" && v.Availability.State != assistantwire.IntegrationStateModuleOff {
		n.serviceHasChannel = true
		channelKey := integrationResource(assistantwire.IntegrationKindChannel, v.Channel)
		child := b.add(channelKey, v.Name, assistantwire.IntegrationOriginBuiltinService, v.Availability, assistantwire.IntegrationFilterCapabilityChannels)
		child.externalSources = true
		b.attach(child, key)
	}
}

func (b *integrationBook) addExtension(v assistantwire.IntegrationExtensionRead) {
	key := integrationResource(assistantwire.IntegrationKindExtension, v.ID)
	n := b.add(key, v.Name, v.Origin, v.Availability)
	n.readOnly = v.ReadOnly
	if v.ReadOnly && n.item.Availability.State == assistantwire.IntegrationStateAvailable {
		n.item.Availability = integrationReadOnly()
	}
	b.declaresKind[key] = make(map[assistantwire.IntegrationKind]bool)
	for point, ids := range v.Contributions {
		if len(ids) == 0 {
			continue
		}
		switch point {
		case "channel", "channels":
			b.declare(key, assistantwire.IntegrationKindChannel, ids)
			n.addCapabilities(assistantwire.IntegrationFilterCapabilityChannels)
		case "voice":
			n.addCapabilities(assistantwire.IntegrationFilterCapabilityChannels)
		case "connector", "connectors":
			b.declare(key, assistantwire.IntegrationKindConnector, ids)
			n.addCapabilities(assistantwire.IntegrationFilterCapabilityData)
		case "messaging", "inbox", "knowledge", "sources", "sync":
			n.addCapabilities(assistantwire.IntegrationFilterCapabilityData)
		default:
			n.addCapabilities(assistantwire.IntegrationFilterCapabilityTools)
		}
	}
	b.declare(key, assistantwire.IntegrationKindConnector, v.ChannelConnectors)
	if len(n.item.Capabilities) == 0 {
		n.addCapabilities(assistantwire.IntegrationFilterCapabilityTools)
	}
}

func (b *integrationBook) declare(owner integrationKey, kind assistantwire.IntegrationKind, ids []string) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		b.declaresKind[owner][kind] = true
		key := integrationResource(kind, assistantwire.IntegrationResourceID(id))
		if !slices.Contains(b.declared[key], owner) {
			b.declared[key] = append(b.declared[key], owner)
		}
	}
}

func (b *integrationBook) addChannels(list []channel.View) {
	occurrences := make(map[string]int)
	for _, v := range list {
		if v.Source != channel.SourceMarket {
			occurrences[v.ID]++
		}
	}
	for _, v := range list {
		key := integrationResource(assistantwire.IntegrationKindChannel, assistantwire.IntegrationResourceID(v.ID))
		if v.ID == "" || v.Source == channel.SourceMarket || b.nodes[key] != nil {
			continue
		}
		origin := assistantwire.IntegrationOriginStandaloneChannel
		if v.Source == channel.SourceBuiltin {
			origin = assistantwire.IntegrationOriginBuiltinService
		}
		n := b.add(key, v.Name, origin, channelIntegrationState(v))
		n.readOnly = v.ReadOnly
		n.externalSources = v.Kind == channel.KindIM || v.Kind == channel.KindMention
		n.addCapabilities(assistantwire.IntegrationFilterCapabilityChannels)
		switch {
		case occurrences[v.ID] > 1:
			n.item.RelationshipIssue = assistantwire.IntegrationRelationshipIssueAmbiguousOwner
			n.readOnly = true
		case v.Source == channel.SourcePlugin:
			b.attachExtension(n, key, assistantwire.IntegrationResourceID(v.Plugin))
		}
	}
}

func (b *integrationBook) addConnector(v assistantwire.IntegrationConnectorRead) {
	key := integrationResource(assistantwire.IntegrationKindConnector, v.ID)
	service, serviceOwned := b.serviceConnector[key]
	serviceOwned = serviceOwned && v.Builtin && v.OwnerExtension == ""
	if serviceOwned && b.nodes[service].item.Availability.State == assistantwire.IntegrationStateModuleOff {
		return
	}
	origin := assistantwire.IntegrationOriginStandaloneConnector
	if v.Builtin && v.OwnerExtension == "" {
		origin = assistantwire.IntegrationOriginBuiltinService
	}
	capability := assistantwire.IntegrationFilterCapabilityChannels
	if v.StoresData {
		capability = assistantwire.IntegrationFilterCapabilityData
	}
	n := b.add(key, v.Name, origin, v.Availability, capability)
	n.storesData = v.StoresData
	n.externalSources = true
	if v.OwnerExtension != "" || !v.Builtin {
		b.attachExtension(n, key, v.OwnerExtension)
	} else if serviceOwned {
		n.externalSources = false // the service decides which external source settings to offer
		b.attach(n, service)
	}
}

func (b *integrationBook) addSubscription(v assistantwire.IntegrationSubscriptionRead) {
	kind, origin := assistantwire.IntegrationKindSubscription, assistantwire.IntegrationOriginStandaloneSubscription
	if v.Push {
		kind, origin = assistantwire.IntegrationKindPushSource, assistantwire.IntegrationOriginStandalonePushSource
	}
	n := b.add(integrationResource(kind, v.ID), v.Name, origin, v.Availability, assistantwire.IntegrationFilterCapabilityData)
	if v.Push {
		return
	}
	connector := integrationResource(assistantwire.IntegrationKindConnector, v.Connector)
	if v.Extension != "" {
		owner := integrationResource(assistantwire.IntegrationKindExtension, v.Extension)
		if registered := b.canonical(connector); registered != owner &&
			(registered.kind == assistantwire.IntegrationKindService || registered.kind == assistantwire.IntegrationKindExtension) {
			n.item.RelationshipIssue = assistantwire.IntegrationRelationshipIssueOwnerConflict
			return
		}
		b.attachExtension(n, connector, v.Extension)
	} else if b.nodes[connector] != nil {
		b.attach(n, connector)
	} else if owner, ok := b.serviceConnector[connector]; ok {
		b.attach(n, owner)
	} else if b.failed(assistantwire.IntegrationKindConnector) {
		n.item.RelationshipIssue = assistantwire.IntegrationRelationshipIssueOwnerUnavailable
	} else if v.Connector != "" {
		b.attachExtension(n, connector, "")
	}
}

func (b *integrationBook) attachExtension(n *integrationNode, resource integrationKey, registered assistantwire.IntegrationResourceID) {
	if b.failed(assistantwire.IntegrationKindExtension) {
		n.item.RelationshipIssue = assistantwire.IntegrationRelationshipIssueOwnerUnavailable
		return
	}
	if registered != "" {
		owner := integrationResource(assistantwire.IntegrationKindExtension, registered)
		if b.nodes[owner] == nil {
			n.item.RelationshipIssue = assistantwire.IntegrationRelationshipIssueOwnerMissing
			return
		}
		b.attach(n, owner)
		return
	}
	switch candidates := b.declared[resource]; len(candidates) {
	case 0:
		// Older producers used the extension id for a resource of the same type. Any declaration
		// for that type takes the producer out of the legacy fallback.
		legacy := integrationKey{kind: assistantwire.IntegrationKindExtension, id: resource.id}
		if b.nodes[legacy] != nil && !b.declaresKind[legacy][resource.kind] {
			b.attach(n, legacy)
		}
	case 1:
		b.attach(n, candidates[0])
	default:
		n.item.RelationshipIssue = assistantwire.IntegrationRelationshipIssueAmbiguousOwner
	}
}

func (b *integrationBook) catalog() assistantwire.IntegrationCatalog {
	out := assistantwire.IntegrationCatalog{Items: []assistantwire.IntegrationCatalogItem{}, Availability: b.availability}
	for key, n := range b.nodes {
		if b.canonical(key) != key || n.item.Availability.State == assistantwire.IntegrationStateModuleOff {
			continue
		}
		item := b.displayItem(n)
		out.Items = append(out.Items, item)
		out.Counts.All++
		for _, capability := range item.Capabilities {
			switch capability {
			case assistantwire.IntegrationFilterCapabilityChannels:
				out.Counts.Channels++
			case assistantwire.IntegrationFilterCapabilityData:
				out.Counts.Data++
			case assistantwire.IntegrationFilterCapabilityTools:
				out.Counts.Tools++
			}
		}
	}
	slices.SortFunc(out.Items, func(a, b assistantwire.IntegrationCatalogItem) int {
		return cmp.Or(cmp.Compare(a.Ref.Kind, b.Ref.Kind), cmp.Compare(a.Ref.ID, b.Ref.ID))
	})
	return out
}

func (b *integrationBook) displayItem(n *integrationNode) assistantwire.IntegrationCatalogItem {
	item := n.item
	item.Capabilities = slices.Clone(item.Capabilities)
	slices.Sort(item.Capabilities)
	related := make(map[assistantwire.IntegrationFilterCapability]assistantwire.IntegrationAvailability)
	record := func(cap assistantwire.IntegrationFilterCapability, status assistantwire.IntegrationAvailability) {
		current := related[cap]
		if cmp.Or(cmp.Compare(integrationAvailabilityRank(status), integrationAvailabilityRank(current)),
			cmp.Compare(status.Reason, current.Reason)) > 0 {
			related[cap] = status
		}
	}
	for _, child := range n.children {
		for _, cap := range child.item.Capabilities {
			record(cap, child.item.Availability)
		}
	}
	if b.declaresKind[n.key][assistantwire.IntegrationKindChannel] && b.failed(assistantwire.IntegrationKindChannel) {
		record(assistantwire.IntegrationFilterCapabilityChannels, integrationUnavailable(assistantwire.IntegrationReasonReadFailed))
	}
	if b.declaresKind[n.key][assistantwire.IntegrationKindConnector] && b.failed(assistantwire.IntegrationKindConnector) {
		record(assistantwire.IntegrationFilterCapabilityData, integrationUnavailable(assistantwire.IntegrationReasonReadFailed))
	}
	for _, cap := range item.Capabilities {
		if status := related[cap]; integrationAvailabilityRank(status) > 0 {
			item.CapabilityAvailability = append(item.CapabilityAvailability, assistantwire.IntegrationCapabilityAvailability{Capability: cap, Availability: status})
		}
	}
	return item
}

func integrationAvailabilityRank(status assistantwire.IntegrationAvailability) int {
	switch {
	case status.Reason == assistantwire.IntegrationReasonReadFailed:
		return 4
	case status.State == assistantwire.IntegrationStateUnavailable:
		return 3
	case status.State == assistantwire.IntegrationStateNeedsSetup:
		return 2
	case status.State == assistantwire.IntegrationStateDisabled:
		return 1
	default:
		return 0
	}
}

func integrationAvailable() assistantwire.IntegrationAvailability {
	return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateAvailable}
}

func integrationUnavailable(reason assistantwire.IntegrationReason) assistantwire.IntegrationAvailability {
	return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateUnavailable, Reason: reason}
}

func integrationReadOnly() assistantwire.IntegrationAvailability {
	return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateReadOnly, Reason: assistantwire.IntegrationReasonManagedElsewhere}
}

func channelIntegrationState(v channel.View) assistantwire.IntegrationAvailability {
	switch v.State {
	case channel.StateConnected:
		if v.ReadOnly {
			return integrationReadOnly()
		}
		return integrationAvailable()
	case channel.StateNotConnected:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateNeedsSetup}
	case channel.StateDisabled:
		return assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateDisabled}
	default:
		return integrationUnavailable(assistantwire.IntegrationReasonRuntimeError)
	}
}
