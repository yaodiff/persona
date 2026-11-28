package assistantwire

import (
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/kbwire"
)

const (
	// MethodKBOwner: {} → KBOwner: the user's name, editable aliases and channel identities (kb.read).
	MethodKBOwner = "kb.owner"
	// ConnectorsOwner: {} → KBOwnerSources: only the connectors' owner identities, without credentials.
	ConnectorsOwner = "connectors.owner"
)

// KBOwnerMethod binds the knowledge base's owner lookup to its empty request and owner reply.
var KBOwnerMethod = buswire.Method[buswire.None, KBOwner]{Name: MethodKBOwner}

// KBOwner is the knowledge base's account of the user. Raw identities are only exposed by this explicit read.
type KBOwner struct {
	Name    string          `json:"name"`
	Aliases []string        `json:"aliases"`
	Handles []kbwire.Handle `json:"handles"`
}

// KBOwnerSource keeps identity provenance so forgetting one source leaves identities supplied by another.
type KBOwnerSource struct {
	Source  string          `json:"source"`
	Handles []kbwire.Handle `json:"handles"`
}

type KBOwnerSources struct {
	Sources []KBOwnerSource `json:"sources"`
}
