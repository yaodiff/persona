package prompts

// PRMergedWake is the localized event-driven closeout guidance for an unparked open supervised task
// whose delivery PR has merged with no other delivery PR remaining open (#13100).
type PRMergedWake struct {
	Language string
	PRURL    string
	OtherPRs []string
	// Parked: the reason the task is parked for, when it still is (its park waits on something other than these PRs).
	Parked string
}

// RenderPRMergedWake renders the localized PR-merge closeout wake text.
func RenderPRMergedWake(w PRMergedWake) string { return render("assistant/pr_merged_wake", w) }

// ParentCloseoutChild summarizes one completed part_of child to-do for a parent closeout wake.
type ParentCloseoutChild struct {
	ID    string
	Title string
	PRs   []string
}

// ParentCloseoutWake is the localized event-driven closeout guidance emitted when every part_of child
// of an open parent to-do (with no active Agent) has completed (#13100).
type ParentCloseoutWake struct {
	Language string
	Personal bool
	Children []ParentCloseoutChild
	Notes    string
}

// RenderParentCloseoutWake renders the localized parent closeout wake text.
func RenderParentCloseoutWake(w ParentCloseoutWake) string {
	return render("assistant/parent_closeout_wake", w)
}
