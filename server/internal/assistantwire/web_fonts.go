package assistantwire

// The web font settings are manager-owned; the hub only relays requests.
func init() {
	Routes = append(Routes,
		"GET /api/v1/settings/web-fonts",
		"PUT /api/v1/settings/web-fonts",
	)
}
