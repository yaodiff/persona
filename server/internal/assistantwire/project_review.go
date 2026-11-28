package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/buswire"
)

const (
	CapabilityProjectReview       = "project_review.owner_input"
	MethodProjectReviewOwnerInput = "project_review.owner_input"
)

// ProjectReviewOwnerInputMethod delivers a project review input with an empty acknowledgment.
var ProjectReviewOwnerInputMethod = buswire.Method[ProjectReviewOwnerInput, buswire.None]{Name: MethodProjectReviewOwnerInput}

// ProjectReviewMessageID names the owner message a project review notice is for: an app message's client_msg_id,
// "feishu:<message id>" for a Lark message, or "first-assistant:<session id>" for a new chat's first message.
type ProjectReviewMessageID string

// ProjectReviewOwnerInput comes from the hub's authenticated app-message route.
type ProjectReviewOwnerInput struct {
	SessionRef
	MessageID ProjectReviewMessageID `json:"message_id"`
	At        time.Time              `json:"at"`
	Unscoped  bool                   `json:"unscoped,omitempty"`
}
