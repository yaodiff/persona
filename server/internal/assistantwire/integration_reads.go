package assistantwire

// These in-process records carry the owning extension manager's settings metadata to the assistant projection.
// The HTTP contract is defined separately by the integrations projection schema.
type IntegrationExtensionRead struct {
	ID                IntegrationResourceID
	Name              string
	Origin            IntegrationOrigin
	Availability      IntegrationAvailability
	Contributions     map[string][]string
	ChannelConnectors []string
	ReadOnly          bool
}

type IntegrationConnectorRead struct {
	ID             IntegrationResourceID
	Name           string
	Availability   IntegrationAvailability
	OwnerExtension IntegrationResourceID // the extension that registered this connector, if one did
	Builtin        bool                  // the host owns this registration
	StoresData     bool
}

type IntegrationSubscriptionRead struct {
	ID           IntegrationResourceID
	Name         string
	Connector    IntegrationResourceID
	Extension    IntegrationResourceID
	Availability IntegrationAvailability
	Push         bool
}

type IntegrationServiceRead struct {
	ID            IntegrationResourceID
	Name          string
	ModuleFeature string
	// Registered children of this service. Older subscriptions can refer to the connector while it is off.
	Channel            IntegrationResourceID
	Connector          IntegrationResourceID
	Capability         IntegrationFilterCapability
	Availability       IntegrationAvailability
	ConnectionReadOnly bool
}
