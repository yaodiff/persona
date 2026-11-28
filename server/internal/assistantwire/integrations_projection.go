package assistantwire

const (
	// CapabilityIntegrationsProjection is advertised in manager.hello. The hub publishes it only on that
	// assistant's roster entry, so older hubs and other assistants cannot opt a client into these routes.
	CapabilityIntegrationsProjection = "integrations_projection_v1"

	IntegrationsCatalogPath  = "/api/v1/assistant/integrations"
	IntegrationsCatalogRoute = "GET " + IntegrationsCatalogPath
	IntegrationsDetailRoute  = "GET " + IntegrationsCatalogPath + "/{kind}/{id}"
)

func init() {
	Routes = append(Routes, IntegrationsCatalogRoute, IntegrationsDetailRoute)
}
