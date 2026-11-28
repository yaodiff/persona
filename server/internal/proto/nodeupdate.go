package proto

const CodeNodeUpdateBusy = "node_update_busy"

const (
	DefaultNodeUpdateHandoffGraceSeconds = 60
	MaxNodeUpdateHandoffGraceSeconds     = 86400
)

// NodeUpdateBeginParams describes the uncompressed executable, never an archive.
type NodeUpdateBeginParams struct {
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}
type NodeUpdateBeginResult struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
}
type NodeUpdateChunkParams struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
}
type NodeUpdateChunkResult struct {
	Offset int64 `json:"offset"`
}
type NodeUpdateCommitParams struct {
	ID                  string `json:"id"`
	HandoffGraceSeconds *int   `json:"handoff_grace_seconds,omitempty"`
}
type NodeUpdateCommitResult struct {
	Accepted bool   `json:"accepted"`
	ID       string `json:"id"`
}
type NodeUpdateAbortParams struct {
	ID string `json:"id"`
}
type NodeUpdateConfirmParams struct {
	Version string `json:"version"`
}

// NodeUpdatePreflightFailure identifies the committed update whose replacement could not be started.
type NodeUpdatePreflightFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// NodeUpdateID identifies a node update transaction.
type NodeUpdateID string

// NodeUpdateDraining identifies a committed update waiting for active work to drain before handover.
type NodeUpdateDraining struct {
	ID     NodeUpdateID `json:"id"`
	Target string       `json:"target,omitempty"`
}
