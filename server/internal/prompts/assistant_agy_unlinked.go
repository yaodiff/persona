package prompts

// AgyUnlinkedPane has no identifiers when the task links or session inventory could not be checked.
type AgyUnlinkedPane struct {
	Pane       string
	Candidates []string
}

type AgyUnlinked struct {
	Language string
	Panes    []AgyUnlinkedPane
}

func RenderAgyUnlinked(p AgyUnlinked) string { return render("assistant/agy_unlinked", p) }
