package prompts

import "encoding/json"

func RenderProjectReview(id string, event json.RawMessage, reviewOnly bool) string {
	return render("agents/project_review", struct {
		ID, Event  string
		ReviewOnly bool
	}{id, string(event), reviewOnly})
}
