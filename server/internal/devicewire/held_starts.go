package devicewire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Held new-Agent starts (#10674): hub ↔ manager bus methods, error codes, and relayed HTTP routes.
const (
	// MethodHoldStart (hub → manager): persist a new-Agent start while the computer is offline.
	MethodHoldStart = "devices.hold_start"
	// MethodReplayHeldStart (manager → hub): replay a held start through the hub's real start/validation path
	// once the computer reconnects (or finalize post-start todo handoff after node-side op_id reconciliation).
	MethodReplayHeldStart = "devices.replay_held_start"
)

var (
	HoldStartMethod       = buswire.Method[HoldStartRequest, HoldStartReply]{Name: MethodHoldStart}
	ReplayHeldStartMethod = buswire.Method[ReplayHeldStartRequest, ReplayHeldStartReply]{Name: MethodReplayHeldStart}
)

const (
	// CodeMachineOnline: MethodHoldStart with OnlyIfOffline found the computer connected right now.
	CodeMachineOnline = "machine_online"
	// CodeHeldStartFull: too many Agent starts are already held for this computer.
	CodeHeldStartFull = "held_start_full"
)

// Relayed HTTP routes for listing, retrying, and cancelling/deleting held Agent starts on a computer.
const (
	HeldStartsListRoute  = "GET /api/v1/machines/{mid}/held-starts"
	HeldStartRetryRoute  = "POST /api/v1/machines/{mid}/held-starts/{op}/retry"
	HeldStartDeleteRoute = "DELETE /api/v1/machines/{mid}/held-starts/{op}"
)

func init() {
	Routes = append(Routes, HeldStartsListRoute, HeldStartRetryRoute, HeldStartDeleteRoute)
}

// HeldStartMeta is the display metadata kept inside held_operations.call so listing held starts never loads image blobs.
type HeldStartMeta struct {
	Cwd         string `json:"cwd,omitempty"`
	Launcher    string `json:"launcher,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
	Model       string `json:"model,omitempty"`
	Host        string `json:"host,omitempty"`
	TabName     string `json:"tab_name,omitempty"`
	TodoID      string `json:"todo_id,omitempty"`
	WorkSession bool   `json:"work_session,omitempty"`
}

// HeldStartPrincipal is the owner's device credential identity preserved across hold and replay.
type HeldStartPrincipal struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Role string `json:"role"`
}

// HeldStartCall is the durable call payload stored in held_operations.call for kind = "start".
// Params holds the original POST /api/v1/sessions JSON body (including prompt and images).
type HeldStartCall struct {
	MachineID  string             `json:"machine_id"`
	OpID       string             `json:"op_id"`
	Params     json.RawMessage    `json:"params"`
	Meta       HeldStartMeta      `json:"meta"`
	Principal  HeldStartPrincipal `json:"principal"`
	Caller     *Caller            `json:"caller,omitempty"`
	Submission *Submission        `json:"submission,omitempty"`
	Lang       string             `json:"lang,omitempty"`
}

// HoldStartRequest is MethodHoldStart's params.
type HoldStartRequest struct {
	Call          HeldStartCall `json:"call"`
	OnlyIfOffline bool          `json:"only_if_offline,omitempty"`
}

// HoldStartReply is MethodHoldStart's answer.
type HoldStartReply struct {
	Delivered string          `json:"delivered"` // "later"
	HeldStart proto.HeldStart `json:"held_start"`
}

// ReplayHeldStartRequest is MethodReplayHeldStart's params.
// When ReconciledSessionID is set, the node already started that session for Call.OpID and the hub only finishes
// post-start linkage (todo handoff / fresh start) without launching another Agent.
type ReplayHeldStartRequest struct {
	Call                HeldStartCall `json:"call"`
	ReconciledSessionID string        `json:"reconciled_session_id,omitempty"`
}

// Replay outcomes returned by MethodReplayHeldStart.
const (
	ReplayOutcomeStarted   = "started"
	ReplayOutcomeNotSent   = "not_sent"
	ReplayOutcomeRefused   = "refused"
	ReplayOutcomeUnclear   = "unclear"
	ReplayOutcomeSignedOut = "signed_out"
)

// ReplayHeldStartReply is MethodReplayHeldStart's answer.
type ReplayHeldStartReply struct {
	Outcome   string `json:"outcome"`
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
