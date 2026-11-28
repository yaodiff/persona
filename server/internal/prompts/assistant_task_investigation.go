package prompts

// RenderTaskInvestigation is the finding-report contract of an opted-in investigation.
func RenderTaskInvestigation() string {
	return render("assistant/task_investigation", nil)
}
