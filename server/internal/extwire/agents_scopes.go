package extwire

// The Agents' settings an extension's token may read with agents.read (#17851): the triage's switches, which say
// whether the hub triages a stopped turn and goes on by itself after a transient error or a usage limit (booleans and
// a number of minutes, nothing of a conversation). An extension that tells the user about an Agent's turn needs them
// to say "it will go on by itself" rather than "it stopped". Changing them (the PUT) stays with full credentials, as
// the push switches' PUTs do beside their GETs under notify.
func init() {
	addRouteScopes(map[string]string{
		"GET /api/v1/agents/triage": ScopeAgentsRead,
	})
}
