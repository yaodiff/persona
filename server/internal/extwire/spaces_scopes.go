package extwire

// ScopeSpaces is the explicit grant for managing domain-space metadata and
// SPACE.md, and for listing a space's conversations (each row still subject to
// the extension's information policy). It is not implied by the legacy
// every-scope token. Other conversation reads and starts still require
// assistant.ask and their existing policies.
const ScopeSpaces = "spaces"

func init() {
	addRouteScopes(map[string]string{
		"GET /api/v1/assistant/tracks":                        ScopeSpaces,
		"POST /api/v1/assistant/tracks":                       ScopeSpaces,
		"PUT /api/v1/assistant/tracks/{slug}":                 ScopeSpaces,
		"GET /api/v1/assistant/track-kinds":                   ScopeSpaces,
		"GET /api/v1/assistant/tracks/{slug}/space-md":        ScopeSpaces,
		"PUT /api/v1/assistant/tracks/{slug}/space-md":        ScopeSpaces,
		"GET /api/v1/assistant/tracks/{slug}/conversations":   ScopeSpaces,
		"GET /api/v1/assistant/tracks/launchers":              ScopeSpaces,
		"GET /api/v1/assistant/conversations/{id}/categories": ScopeSpaces,
		"PUT /api/v1/assistant/conversations/{id}/categories": ScopeSpaces,
	})
}
