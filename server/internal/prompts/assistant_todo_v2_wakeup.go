package prompts

// SuperviseTodoV2Wakeup carries the live facts for one to-do v2 supervision entry.
type SuperviseTodoV2Wakeup struct {
	Lang, Kind                    string
	BlockerPRs                    []string
	PR, PreviousPR                string
	PreviousClosed, NeedsApproval bool
	PeerID, PeerTitle             string
	Overlaps                      []string
}

func RenderSuperviseTodoV2Wakeup(w SuperviseTodoV2Wakeup) string {
	return render("assistant/todo_v2_wakeup", w)
}
