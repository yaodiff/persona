package assistantwire

// Input settlement and replay are full-credential operations owned by the
// logical conversation domain. The hub relays them to the assistant manager.
func init() {
	Routes = append(Routes, "POST /api/v1/assistant/conversations/{id}/inputs/resolve")
}
