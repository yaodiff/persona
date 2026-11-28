package assistantwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/wireids"
)

// Typed descriptors for the Inbox bus methods answered by the assistant's kernel.
var (
	InboxCollectedMethod     = buswire.Method[InboxRound, InboxAck]{Name: MethodInboxCollected}
	InboxItemMethod          = buswire.Method[InboxItemParams, InboxItemResult]{Name: MethodInboxItem}
	InboxForgetMethod        = buswire.Method[InboxForgetParams, InboxForgetResult]{Name: MethodInboxForget}
	InboxRepoMutesMethod     = buswire.Method[InboxRepoMutes, InboxRepoMutesResult]{Name: MethodInboxRepoMutes}
	InboxMutesMethod         = buswire.Method[InboxMutesParams, InboxMutesResult]{Name: MethodInboxMutes}
	InboxPostMethod          = buswire.Method[InboxPostParams, InboxPostResult]{Name: MethodInboxPost}
	InboxTriagePreviewMethod = buswire.Method[InboxTriagePreview, InboxTriagePreviewResult]{Name: MethodInboxTriagePreview}
	InboxUnmuteMethod        = buswire.Method[InboxUnmuteParams, InboxAck]{Name: MethodInboxUnmute}
)

// The Inbox's bus methods are its kernel module's (module_methods.go), in the order its own switch had them.
func init() {
	addModuleMethods(MethodInboxCollected, MethodInboxItem, MethodInboxForget, MethodInboxRepoMutes, MethodInboxMutes,
		MethodInboxPost, MethodInboxTriagePreview, MethodInboxUnmute)
}

// The params and answers of the Inbox's bus methods (MethodInbox*, inbox.go) that had no type of their own: the
// manager decodes and encodes them through these, so their JSON is the shape each method's comment gives. InboxRound,
// InboxRepoMutes, InboxTriagePreview and InboxItemResult are the others.

// InboxMuteID is a mute's id as the bus carries it (InboxUnmuteParams; InboxMute.ID lists the same).
type InboxMuteID string

// InboxAck is the answer {} of MethodInboxCollected and MethodInboxUnmute.
type InboxAck struct{}

// InboxItemParams are MethodInboxItem's params: the item's id.
type InboxItemParams struct {
	ID wireids.InboxItemID `json:"id"`
}

// InboxForgetParams are MethodInboxForget's params: the source whose data the user deleted.
type InboxForgetParams struct {
	Source string `json:"source"`
}

// InboxForgetResult is MethodInboxForget's answer: how many items went.
type InboxForgetResult struct {
	Deleted int `json:"deleted"`
}

// InboxRepoMutesResult is MethodInboxRepoMutes's answer: the source's muted repositories.
type InboxRepoMutesResult struct {
	Repos []string `json:"repos"`
}

// InboxMutesParams are MethodInboxMutes's params: one source's mutes when Source is set, else every mute.
type InboxMutesParams struct {
	Source string `json:"source"`
}

// InboxMutesResult is MethodInboxMutes's answer.
type InboxMutesResult struct {
	Mutes []InboxMute `json:"mutes"`
}

// InboxUnmuteParams are MethodInboxUnmute's params: the mute's id.
type InboxUnmuteParams struct {
	ID InboxMuteID `json:"id"`
}

// InboxPostParams are MethodInboxPost's params: whether to set the switch first, and to what.
type InboxPostParams struct {
	On InboxPostSet `json:"on,omitempty"`
}

// InboxPostSet is MethodInboxPost's "on", a JSON bool the caller may leave out: InboxPostLeftOut (left out or null:
// the switch is only read), InboxPostOn or InboxPostOff (it is set first). The zero InboxPostSet is left out.
type InboxPostSet uint8

// The values of InboxPostSet.
const (
	InboxPostLeftOut InboxPostSet = iota
	InboxPostOff
	InboxPostOn
)

// UnmarshalJSON reads a JSON bool, or null for left out.
func (s *InboxPostSet) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = InboxPostLeftOut
		return nil
	}
	var on bool
	if err := json.Unmarshal(b, &on); err != nil {
		return err
	}
	*s = InboxPostOff
	if on {
		*s = InboxPostOn
	}
	return nil
}

// MarshalJSON writes the JSON bool; a left-out value is null (InboxPostParams leaves it out instead).
func (s InboxPostSet) MarshalJSON() ([]byte, error) {
	switch s {
	case InboxPostOn:
		return []byte("true"), nil
	case InboxPostOff:
		return []byte("false"), nil
	}
	return []byte("null"), nil
}

// InboxPostResult is MethodInboxPost's answer: whether the assistant may put its conversations into the Inbox.
type InboxPostResult struct {
	On bool `json:"on"`
}

// InboxTriagePreviewResult is MethodInboxTriagePreview's answer: one verdict per dry-run item.
type InboxTriagePreviewResult struct {
	Verdicts []InboxTriageVerdict `json:"verdicts"`
}
