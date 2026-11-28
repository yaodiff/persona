// Package projection works out the To-dos view (#10226): for each to-do its phase, section, reason, line 2, times,
// marks, asks and steps (assistantwire.TodoSummary), its rank in its section, and the list's counts and places, by the
// decision table of docs/design/proposals/todos-redesign.md 6.3. The rules are pinned by the shared cases in
// spec/fixtures/todo-view and spec/fixtures/todo-counts, whose READMEs list every rule, line key and judgement call.
//
// It is pure: no IO and no clock (the caller passes now), and it imports only the standard library and wire
// packages. Its input, Facts, is the to-do as GET /api/v1/todos lists it, decoded from the same JSON, plus what
// the row does not carry: the linked sessions, the steps
// and the owner's zone. The manager's adapter (internal/assistant/todo_projection.go) builds it; the legacy triage
// (internal/assistant/todo_triage.go) keeps serving the direction tree and leader reports until they read the view.
package projection

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Facts is one to-do as the projection reads it. The JSON fields are the ones of a row of GET /api/v1/todos (the
// manager's listedTodo) that the rules read, under the same names, so a listed row decodes straight into it; the
// fields without JSON come from elsewhere.
type Facts struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	DoneAt     *time.Time `json:"done_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Origin     string     `json:"origin,omitempty"`
	PlanID     string     `json:"plan_id,omitempty"`
	StartError string     `json:"start_error,omitempty"`
	// DoneBy as the list sends it: auto, review (also for a cancellation: the ledger's cancelled state tells them
	// apart), reported, or "" when the owner closed it.
	DoneBy          string       `json:"done_by,omitempty"`
	ReopenedAt      *time.Time   `json:"reopened_at,omitempty"`
	CancelledAt     *time.Time   `json:"cancelled_at,omitempty"`
	CancelledReason string       `json:"cancelled_reason,omitempty"`
	Agents          []Agent      `json:"agents"`
	Quiet           bool         `json:"quiet"`
	Supervision     *Supervision `json:"supervision,omitempty"`
	PRURL           string       `json:"pr_url,omitempty"`
	PRStatus        *PRStatus    `json:"pr_status,omitempty"`
	OtherPRs        []PRStatus   `json:"other_prs,omitempty"`
	Delivery        *Delivery    `json:"delivery,omitempty"`
	DueAt           *time.Time   `json:"due_at,omitempty"`
	DueDate         string       `json:"due_date,omitempty"`
	Priority        string       `json:"priority,omitempty"`
	Project         string       `json:"project,omitempty"`
	Kind            string       `json:"kind,omitempty"`
	SeenAt          *time.Time   `json:"seen_at,omitempty"`
	PartOf          string       `json:"part_of,omitempty"`
	After           []string     `json:"after,omitempty"`
	// Ask is the to-do's own open ask, DecisionReview its decision round and whether the owner chose Don't handle on it.
	Ask            *Ask            `json:"ask,omitempty"`
	DecisionReview *DecisionReview `json:"decision_review,omitempty"`
	// RestoredAt: the owner's latest restore from Inactive, which counts as activity; SomedayUntil: until when the owner
	// put it off (somedayNoDate and later: with no date), which counts as activity once it passed. The list carries both
	// once the hub keeps them (#10226).
	RestoredAt   *time.Time `json:"restored_at,omitempty"`
	SomedayUntil *time.Time `json:"someday_until,omitempty"`

	Phase     assistantwire.TaskPhase      `json:"phase,omitempty"`
	WaitState *assistantwire.TaskWaitState `json:"wait_state,omitempty"`
	// The current task contract and the provenance of the runtime wait come from the brief and stage ledgers.
	WorkModeNow       assistantwire.WorkMode       `json:"-"`
	CompletionModeNow assistantwire.CompletionMode `json:"-"`
	RuntimeAt         time.Time                    `json:"-"`
	ObservedWait      bool                         `json:"-"`
	// RepoGate and ActiveBlockers describe v2 delivery and blocker evidence, as the waits they give the row: RepoGate
	// the pr_merge wait of the open delivery PR holding the weakest bound repository's gate (RepoGateWait builds it),
	// ActiveBlockers one wait per persisted blocker (its kind, its display target as WaitingOn, its source as URL).
	RepoGate       *assistantwire.TodoWait  `json:"-"`
	ActiveBlockers []assistantwire.TodoWait `json:"-"`

	// Sessions are the to-do's linked runs' sessions as the hub sends them to the apps (state worked out); a linked
	// run whose session is not among them is offline.
	Sessions []proto.Session `json:"-"`
	// Interactions are the pending cards of its linked runs' sessions as the hub holds them, a health conversation's
	// marked Private (its push says nothing of what it asks, as a Just me card's); a run that asks is asked by one of
	// its own (asksBefore), which says what kind of ask it is and how risky answering it is.
	Interactions []proto.Interaction `json:"-"`
	// Unreached: a computer one of its active runs is on did not answer, so their sessions are unknown; it then gets no
	// Inactive mark, since a run there may be working on it (as the direction tree judges no task stuck then).
	Unreached bool `json:"-"`
	// Steps are the to-dos whose part_of names this one, oldest first (Views fills them from the list).
	Steps []Facts `json:"-"`
	// Superseded: a failed start whose work finished in a related to-do (Views works it out over the list); it reads as
	// closed.
	Superseded bool `json:"-"`
	// Loc is the owner's time zone, for a due day; nil is UTC.
	Loc *time.Location `json:"-"`
	// TodosV2 gates experimental New To-dos & Tasks projection rules (#14620); false preserves legacy behavior identically.
	TodosV2 bool `json:"-"`
	// PendingReviewSLAHours overrides the default 24h pending_review quiet window under todos_v2=true when > 0 (#16098).
	// PendingReviewSLADisabled disables pending_review quiet sinking so it surfaces immediately to needs_you.
	PendingReviewSLAHours    int  `json:"-"`
	PendingReviewSLADisabled bool `json:"-"`
	// CanWaitSLAHours overrides the default 48h can_wait auto-escalation window under todos_v2=true when > 0 (#16098).
	// CanWaitSLADisabled disables automatic aging of can_wait decisions into active needs_you.
	CanWaitSLAHours    int  `json:"-"`
	CanWaitSLADisabled bool `json:"-"`
}

// Agent is one of the to-do's runs (an Agent session) as the list links it (agents[]).
type Agent struct {
	MachineID   string     `json:"machine_id"`
	SessionID   string     `json:"session_id"`
	LinkedAt    *time.Time `json:"linked_at,omitempty"`
	ReleasedAt  *time.Time `json:"released_at,omitempty"`
	Title       string     `json:"title"`
	StartError  string     `json:"start_error,omitempty"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
}

// Ask is the to-do's own open ask (the list's ask): a conclusion's outcome (done, cannot, decide; "" a deployed task's
// Please verify), the round it was asked in, how risky answering it is, and a decision's question, options, deadline
// and whether it can wait. Risk is the manager's rule (askRisk), which reads what the list does not carry (whether
// taking it at its deadline can be undone, also once that deadline passed): the view reads it as listed.
type Ask struct {
	Outcome     string                         `json:"outcome,omitempty"`
	Verdict     string                         `json:"verdict"`
	Recommend   string                         `json:"recommend,omitempty"`
	AskedAt     time.Time                      `json:"asked_at"`
	Risk        string                         `json:"risk,omitempty"`
	Question    string                         `json:"question,omitempty"`
	Options     []assistantwire.OwnerAskOption `json:"options,omitempty"`
	Deadline    *time.Time                     `json:"deadline,omitempty"`
	Default     string                         `json:"default,omitempty"`
	ForBriefing bool                           `json:"for_briefing,omitempty"`
}

// DecisionReview is the decision round under review; DismissalID is set when the owner chose Don't handle on it.
type DecisionReview struct {
	ReviewAt    time.Time `json:"review_at"`
	DismissalID string    `json:"dismissal_id,omitempty"`
}

// Supervision is a supervised task's ledger: the review state, the park that holds it, the reviews (latest last) and
// the moments that count as activity.
type Supervision struct {
	// Supervisor: the conversation that handed it out and supervises it. Its failed starts and the cards it routes are
	// that conversation's to handle, not the owner's.
	Supervisor   string     `json:"supervisor,omitempty"`
	State        string     `json:"state"`
	Parked       *Parked    `json:"parked,omitempty"`
	Reviews      []Review   `json:"reviews"`
	PendingSince *time.Time `json:"pending_since,omitempty"`
	FollowedUpAt *time.Time `json:"followed_up_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Progress     *Progress  `json:"progress,omitempty"`
}

// Parked is what a parked task waits for: why, until when, on what kind of thing.
type Parked struct {
	Reason    string                        `json:"reason"`
	Until     time.Time                     `json:"until"`
	WaitingOn string                        `json:"waiting_on"`
	Evidence  *ParkEvidence                 `json:"evidence,omitempty"`
	Handoff   *assistantwire.TodoHandoffRef `json:"handoff,omitempty"`
}

// ParkEvidence carries the fields of parked evidence the projection reads (owner-request link, shared-blocker root cause, and waiting details).
type ParkEvidence struct {
	Kind          string                         `json:"kind,omitempty"`
	Target        string                         `json:"target,omitempty"`
	URL           string                         `json:"url,omitempty"`
	Wake          string                         `json:"wake,omitempty"`
	InteractionID string                         `json:"interaction_id,omitempty"`
	Contact       *assistantwire.TodoWaitContact `json:"contact,omitempty"`
}

// Review is one review of the ledger: when, its verdict, its note (a cancellation's why), and a needs_user review's
// question and recommended option.
type Review struct {
	At        time.Time `json:"at"`
	Verdict   string    `json:"verdict"`
	By        string    `json:"by,omitempty"`
	Note      string    `json:"note,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Recommend string    `json:"recommend,omitempty"`
}

// Progress is the moment of the task's Agent's latest report.
type Progress struct {
	At time.Time `json:"at"`
}

// Delivery is how far the finished work went out (live: deployed) and when that was judged.
type Delivery struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
	Owner bool      `json:"owner,omitempty"` // an open delivery hold asks the owner to release it
	Gate  string    `json:"gate,omitempty"`
}

// PRStatus is an observation of one of the task's PRs.
type PRStatus struct {
	URL       string    `json:"url"`
	Title     string    `json:"title,omitempty"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	Review    string    `json:"review,omitempty"`
	Checks    string    `json:"checks,omitempty"`
	Conflicts bool      `json:"conflicts,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Values the rules compare, as the manager writes them.
const (
	kindConcern = "concern"

	doneByCancelled = "cancelled" // the stored value; the list sends review, with the ledger cancelled
	doneByReview    = "review"
	doneByReported  = "reported"

	reviewPending   = "pending_review"
	reviewAccepted  = "accepted"
	reviewUserCheck = "user_check"
	reviewNeedsUser = "needs_user"
	reviewCancelled = "cancelled"
	reviewParked    = "parked"

	parkSomeoneElse   = "someone_else"
	parkExternalEvent = "external_event"
	parkDate          = "date"
	parkTodoHandoff   = "todo_handoff"

	parkEvidencePersonReply   = "person_reply"
	parkEvidenceMergeReview   = "merge_review"
	parkEvidenceExternalEvent = "external_event"
	parkEvidenceDate          = "date"

	outcomeDone   = "done"
	outcomeCannot = "cannot"
	outcomeDecide = "decide"
	defaultTaken  = "taken"

	deliveryLive   = "live"
	deliveryMerged = "merged"
	prOpen         = "open"

	priorityHigh = "high"
	priorityLow  = "low"
)

// somedayNoDate is the someday_until of a to-do put off with no date: the hub stores Someday with no date as this
// moment, so "put off until" is one rule for both.
var somedayNoDate = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// closed: the to-do's lifecycle is closed. A failed start keeps done_at as stored but reads as open, unless its work
// finished in a related to-do (6.2 invariant 1; #10226).
func (f *Facts) closed() bool {
	return f.DoneAt != nil && (f.StartError == "" || f.Superseded)
}

// outsideDecisionRow has its own answer home: part_of associates it with
// existing work, but does not make the request a step of that work (#8235).
func outsideDecisionRow(f Facts) bool { return strings.HasPrefix(f.ID, "todo_od_") }

// relate fills in what a to-do's facts take from the rest of the list: its steps (the to-dos whose part_of names it,
// oldest first) and whether a failed start is superseded (a Superseded already set stays). It returns a new list; the
// steps it attaches carry no steps of their own (steps are one level deep).
func relate(list []Facts) []Facts {
	out := slices.Clone(list)
	superseded := supersededFailedStarts(list)
	steps := map[string][]Facts{}
	for _, f := range list {
		if f.PartOf != "" && f.PartOf != f.ID && !outsideDecisionRow(f) {
			f.Steps, f.Superseded = nil, f.Superseded || superseded[f.ID]
			steps[f.PartOf] = append(steps[f.PartOf], f)
		}
	}
	for i := range out {
		out[i].Superseded = out[i].Superseded || superseded[out[i].ID]
		out[i].Steps = steps[out[i].ID]
		slices.SortStableFunc(out[i].Steps, func(a, b Facts) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
		})
	}
	return out
}

// ---- related to-dos (ported from the legacy triage's related rows, which the view keeps only for superseded) ----

var (
	relatedIssueRef  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`#(\d{2,7})\b`) })
	relatedTitleDrop = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`[\s\p{Z}\p{P}\p{S}]+`) })
)

// relatedKeys are the keys that make two to-dos the same piece of work: the same PR, the same issue number in the
// title, the same title without case, spaces and punctuation (six characters or more), the same plan
// (docs/assistant/todo-status-sop.md "Related to-dos").
func relatedKeys(f Facts) []string {
	var keys []string
	if pr := normalizePRURL(f.PRURL); pr != "" {
		keys = append(keys, "pr:"+pr)
	}
	if m := relatedIssueRef().FindStringSubmatch(f.Title); m != nil {
		keys = append(keys, "issue:"+m[1])
	}
	if title := relatedTitleDrop().ReplaceAllString(strings.ToLower(f.Title), ""); utf8.RuneCountInString(title) >= 6 {
		keys = append(keys, "title:"+title)
	}
	if f.PlanID != "" {
		keys = append(keys, "plan:"+f.PlanID)
	}
	return keys
}

// Superseded are the failed starts of list whose work finished in a related to-do (supersededFailedStarts). It reads
// only each to-do's id, title, pr_url, plan_id, done_at, start_error and kind: a caller that works out the views of a
// part of a list sets Superseded from the whole list's (relate keeps it), so they read as the whole list's views do.
func Superseded(list []Facts) map[string]bool { return supersededFailedStarts(list) }

// supersededFailedStarts are the failed starts related to a to-do that really finished (closed without a start
// error): the work got done another way, so nothing is left to retry (#3713). Related to-dos chain; closed and open
// ones never relate, and an open concern relates to nothing.
func supersededFailedStarts(list []Facts) map[string]bool {
	parent := map[string]string{}
	for _, f := range list {
		parent[f.ID] = f.ID
	}
	var find func(string) string
	find = func(id string) string {
		r := id
		for parent[r] != r {
			r = parent[r]
		}
		parent[id] = r
		return r
	}
	owner := map[string]string{}
	for _, f := range list {
		if f.DoneAt == nil && f.Kind == kindConcern {
			continue
		}
		side := "o"
		if f.DoneAt != nil {
			side = "c"
		}
		for _, k := range relatedKeys(f) {
			if other, ok := owner[side+"|"+k]; ok {
				parent[find(f.ID)] = find(other)
			} else {
				owner[side+"|"+k] = f.ID
			}
		}
	}
	finished := map[string]bool{}
	for _, f := range list {
		if f.DoneAt != nil && f.StartError == "" {
			finished[find(f.ID)] = true
		}
	}
	out := map[string]bool{}
	for _, f := range list {
		if f.StartError != "" && finished[find(f.ID)] {
			out[f.ID] = true
		}
	}
	return out
}
