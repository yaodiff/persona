package assistantwire

// Saved Agent searches belong to the manager, like other user-authored settings.
// Clients name the captured default manager; the hub only relays these routes.
func init() {
	Routes = append(Routes, "GET /api/v1/saved-agent-searches", "POST /api/v1/saved-agent-searches")
}
