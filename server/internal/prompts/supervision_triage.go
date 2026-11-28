package prompts

// RenderSupervisionTriage asks the background model to classify routine signals.
func RenderSupervisionTriage(language string) string {
	return render("assistant/supervision_triage", struct{ Language string }{Language: language})
}
