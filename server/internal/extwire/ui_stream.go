package extwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
)

const (
	FeatureUIStream       = "extension_ui_stream_v1"
	MethodUIStream        = "extensions.ui_stream"
	EventUIStream         = "extensions.ui_stream_frame"
	EventUIConnectionGone = "extensions.ui_connection_gone"
)

// UIStreamMethod acknowledges the call. Stream frames arrive through EventUIStream.
var UIStreamMethod = buswire.Method[UIStreamCall, buswire.None]{Name: MethodUIStream}

// UIPrincipal comes only from the authenticated hub connection. Neither source
// code nor an app envelope supplies these fields. Hub scopes multi-group sessions.
type UIPrincipal struct {
	Hub        string `json:"hub"`
	Principal  string `json:"principal"`
	Connection string `json:"connection"`
	Role       string `json:"role"`
	Paired     bool   `json:"paired,omitempty"`
	Lang       string `json:"lang,omitempty"`
}
type UIStreamCall struct {
	Who      UIPrincipal     `json:"who"`
	Envelope json.RawMessage `json:"envelope"`
}

// UITarget is never broadcast or recorded in the global event replay log.
type UITarget struct {
	Who      UIPrincipal     `json:"who"`
	Envelope json.RawMessage `json:"envelope"`
}
