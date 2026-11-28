package assistantwire

import "time"

// TaskPhase describes the current work on a task. The zero value is absent on
// a legacy task. Newer values round-trip with Valid returning false.
type TaskPhase string

const (
	TaskPhasePreparing TaskPhase = "preparing"
	TaskPhaseWorking   TaskPhase = "working"
	TaskPhaseWaiting   TaskPhase = "waiting"
	TaskPhaseReviewing TaskPhase = "reviewing"
	TaskPhaseDone      TaskPhase = "done"
)

// Valid reports whether p is a supported task phase.
func (p TaskPhase) Valid() bool {
	switch p {
	case TaskPhasePreparing, TaskPhaseWorking, TaskPhaseWaiting, TaskPhaseReviewing, TaskPhaseDone:
		return true
	default:
		return false
	}
}

// WaitKind identifies what can move a waiting task forward.
type WaitKind string

const (
	WaitUser       WaitKind = "user"
	WaitExternal   WaitKind = "external"
	WaitDependency WaitKind = "dependency"
	WaitDate       WaitKind = "date"
)

// Valid reports whether k is a supported wait kind.
func (k WaitKind) Valid() bool {
	switch k {
	case WaitUser, WaitExternal, WaitDependency, WaitDate:
		return true
	default:
		return false
	}
}

// WaitTargetKind describes the type of reference in a wait target. Its zero
// value leaves Ref as an ordinary label.
type WaitTargetKind string

const (
	WaitTargetTodo  WaitTargetKind = "todo"
	WaitTargetPR    WaitTargetKind = "pr"
	WaitTargetIssue WaitTargetKind = "issue"
	WaitTargetChat  WaitTargetKind = "chat"
	WaitTargetURL   WaitTargetKind = "url"
	WaitTargetTime  WaitTargetKind = "time"
)

// Valid reports whether k is a supported reference type.
func (k WaitTargetKind) Valid() bool {
	switch k {
	case WaitTargetTodo, WaitTargetPR, WaitTargetIssue, WaitTargetChat, WaitTargetURL, WaitTargetTime:
		return true
	default:
		return false
	}
}

// TaskWaitTarget names the specific dependency, event, decision or time.
type TaskWaitTarget struct {
	Kind    WaitTargetKind `json:"kind,omitempty"`
	Ref     string         `json:"ref"`
	Summary string         `json:"summary,omitempty"`
}

// TaskWaitState records an active wait. Until is an optional absolute time.
type TaskWaitState struct {
	WaitingOn WaitKind       `json:"waiting_on"`
	Target    TaskWaitTarget `json:"wait_target"`
	Reason    string         `json:"reason"`
	Until     time.Time      `json:"until,omitzero"`
}
