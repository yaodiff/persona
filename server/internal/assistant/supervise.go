package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Supervised tasks (#3124, docs/assistant/unified-entry.md"Supervision"). A task the main conversation hands out (task_plan, or an
// agent_start that became a quiet task) has the main conversation as its supervisor: the task keeps a ledger (what
// done means, how long it should take, where its review stands) and the hub watches it without a model call. What
// needs the supervisor — the work looks finished and waits for review, an agent stopped short of it, waits for an
// answer too long, makes no progress, was cut off or did not start — becomes a signal; the signals of all tasks go to
// the main conversation as one supervision message (the quiet tasks' notices are among them), at most every few minutes, and
// the turn it starts is not pushed unless the assistant pushes (notify_user). The assistant answers with task_review:
// accepted closes the todo unless personal verification is pending (user_check); its agents close later, tasks_close.go.
// rework sends the gaps back to the agent (at
// most maxReworkRounds times), needs_user waits for the user, parked waits for something else until a time
// (parked.go), cancelled ends it.
//
// A supervised todo is never closed by the hub's judge (done.go): a finished stretch of work waits for review instead.

const (
	reviewClosing   = "timeout_closing"
	reviewTimedOut  = "timed_out"
	reviewPending   = "pending_review"
	reviewUserCheck = "user_check"
	reviewAccepted  = "accepted"
	reviewRework    = "rework"
	reviewNeedsUser = "needs_user"
	reviewCancelled = "cancelled"
)

// maxReworkRounds is the strict circuit breaker for consecutive stalled/low-ROI rework rounds
// (repeated_error, low_roi_edge, or legacy unspecified rework).
const maxReworkRounds = 2

// maxTotalReworkRounds is the overall safety valve across all rework kinds (including external_input
// and substantive_new) to prevent runaway supervision loops.
const maxTotalReworkRounds = 10

// Optional rework_kind values on task_review(verdict="rework").
const (
	reworkExternalInput  = "external_input"
	reworkSubstantiveNew = "substantive_new"
	reworkRepeatedError  = "repeated_error"
	reworkLowROIEdge     = "low_roi_edge"
)

func validReworkKind(kind string) bool {
	switch kind {
	case "", reworkExternalInput, reworkSubstantiveNew, reworkRepeatedError, reworkLowROIEdge:
		return true
	default:
		return false
	}
}

// isStalledReworkKind reports whether kind counts toward the tight 2-round stalled/low-ROI cap.
// Legacy unspecified rework ("") defaults to stalled accounting for backward compatibility.
func isStalledReworkKind(kind string) bool {
	return kind != reworkExternalInput && kind != reworkSubstantiveNew
}

// Supervision is a supervised task's ledger, as the apps get it on the todo (supervision).
type Supervision struct {
	Supervisor       string             `json:"supervisor"`                  // the conversation currently supervising it
	SupervisorSource string             `json:"supervisor_source,omitempty"` // "plan" for a planned leader assignment; "explicit" after a move
	DoneWhen         []string           `json:"done_when"`
	Criteria         []db.TodoCriterion `json:"criteria"`
	ExpectMinutes    int                `json:"expect_minutes,omitempty"`
	State            string             `json:"state"` // "" going on, or one of review*
	// Parked: what a parked task waits for (parked.go); nil unless State is parked. Read off its latest review each time
	// the ledger is copied, not kept on its own.
	Parked              *Parked      `json:"parked,omitempty"`
	Round               int          `json:"round"`                           // total reworks so far
	StalledReworkRounds int          `json:"stalled_rework_rounds,omitempty"` // consecutive stalled/low-ROI reworks
	Reviews             []TaskReview `json:"reviews"`
	// AutoReview is the latest independent review of its change (supervise_autoreview.go, #2173); nil when none ran.
	AutoReview *AutoReview `json:"auto_review,omitempty"`
	// Waits: the done judge found the latest stretch of work stopped on a decision only the owner can make (verdict
	// needs_user, #1277). While it holds, task_review accepted is refused: closing the task would lose the decision.
	// Its agent working again, or any review, clears it. nil when the last judgment said otherwise.
	Waits *OwnerWait `json:"waits,omitempty"`
	// PendingSince: when the task entered pending_review (updateSupervision keeps it: set on the way in, kept while the
	// review waits, gone on the way out). The apps list a review younger than an hour with the tasks in progress, as
	// its supervisor's to do, and an older one under Needs you (docs/assistant/unified-entry.md "Needs you and In
	// progress"). nil in every other state, and for a task that waited for review before the hub kept the time.
	PendingSince *time.Time `json:"pending_since,omitempty"`
	// FollowedUpAt records a successful supervisor review or Agent send, not an automatic judgment or progress report.
	FollowedUpAt *time.Time `json:"followed_up_at,omitempty"`
	// CardlessToldAt: when the watch handed this pending_review round back to its supervisor because it waits on the
	// owner with no card they can answer (supervise_cardless.go); one hand-back per review round, nil outside
	// pending_review and on every new review round.
	CardlessToldAt *time.Time `json:"cardless_told_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// Progress: what its agent last reported to the supervisor (report_to_supervisor, agent_control.go). A live
	// reading kept in memory only (the next report brings it back after a restart), and apart from UpdatedAt, which
	// names the review a task waits for.
	Progress      *TaskProgress          `json:"progress,omitempty"`
	Investigation *investigationFindings `json:"investigation_findings,omitempty"`
	// OwnerWork is server-only evidence for the close guard (#15406): the latest owner-authored instruction and the
	// latest Persona assignment per linked Agent session. Keeping it out of the wire shape prevents another Agent or
	// an ordinary to-do reader from seeing an excerpt meant for the owner and the responsible supervisor.
	OwnerWork []ownerSessionWork `json:"-"`
}

// assigned records a current ownership decision, rather than a pre-policy legacy ledger.
func (v *Supervision) assigned() bool {
	return v != nil && v.Supervisor != "" && v.SupervisorSource != ""
}

// TaskProgress is a supervised task's agent's latest report: its kind, the step it is on of how many (0: not said),
// what it said in a line, and when. Line and Detail are for the apps only (#3181): no prompt, supervision message,
// briefing, card or push reads them.
type TaskProgress struct {
	Kind  string `json:"kind"`
	Step  int    `json:"step,omitempty"`
	Steps int    `json:"steps,omitempty"`
	Text  string `json:"text"`
	// Line: the report paraphrased in one sentence in the user's language (report_line.go), for the Progress row; ""
	// until the model wrote it, and for good when there is no model or it failed (the apps show Text).
	Line string `json:"line,omitempty"`
	// Detail: the report as the agent wrote it (Markdown, at most 2000 characters), for the task's details; "" when it
	// is Text (one line of at most 200 characters).
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
	// machine and session name the Agent that sent this report (in memory only, never on the wire), so a done report
	// only settles left-over shells of the Agent that wrote it (#16418).
	machine string
	session string
}

// TaskReview is one review: the assistant's verdict, with each criterion's evidence or gap.
type TaskReview struct {
	At                  time.Time     `json:"at"`
	Verdict             string        `json:"verdict"`
	By                  string        `json:"by,omitempty"`                    // "reviewer": the independent reviewer's rework (#2173); "judge": the done judge's own acceptance (#3128); "" the supervisor's
	Round               int           `json:"round,omitempty"`                 // the total rework round it started (rework)
	ReworkKind          string        `json:"rework_kind,omitempty"`           // external_input, substantive_new, repeated_error, low_roi_edge (rework)
	StalledReworkRounds int           `json:"stalled_rework_rounds,omitempty"` // consecutive stalled/low-ROI rework rounds after this rework
	Checks              []ReviewCheck `json:"checks,omitempty"`
	Note                string        `json:"note,omitempty"`
	// UserCheck: an accepted review that still wants the owner to look themselves (task_review user_check): its
	// "Please verify" waits for them instead of being passed by the supervisor (acceptance_supervisor.go, #3240).
	UserCheck bool   `json:"user_check,omitempty"`
	Link      string `json:"link,omitempty"` // Deliverable named by the supervisor for owner verification.
	// DeferredScope and FollowUpTodoID record what an accepted review handed back or assigned separately.
	DeferredScope  string `json:"deferred_scope,omitempty"`
	FollowUpTodoID string `json:"follow_up_todo_id,omitempty"`
	// A needs_user review's decision (#3804, decision.go): why only the owner can decide, what is to be decided, the
	// option recommended, the options (the recommended first) and what waiting costs.
	WhyOwner      string           `json:"why_owner,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	Recommend     string           `json:"recommend,omitempty"`
	Options       []DecisionOption `json:"options,omitempty"`
	CostOfWaiting string           `json:"cost_of_waiting,omitempty"`
	// Deadline and Reversible: the needs_user decision's deadline and whether its recommendation can be undone, as the
	// supervisor's task_review said them (decision_deadline.go); absent when it gave none, and on reviews from before
	// they were kept.
	Deadline   *time.Time `json:"deadline,omitempty"`
	Reversible *bool      `json:"reversible,omitempty"`
	// A parked review's (parked.go): until when it waits, what kind of thing it waits on (Reason says what), how many
	// parks in a row this one is with no agent turn in between, and when a supervision message carried its parked_due.
	Until     time.Time `json:"until,omitzero"`
	WaitingOn string    `json:"waiting_on,omitempty"`
	Parks     int       `json:"parks,omitempty"`
	DueAt     time.Time `json:"due_at,omitzero"`
	// DueResends: how many times its parked_due was told again because no task_review followed the reminder.
	DueResends int `json:"due_resends,omitempty"`
	// ParkEndedAt is recorded when this wait is replaced or resolved, so its time is excluded from stalled checks.
	ParkEndedAt time.Time `json:"park_ended_at,omitzero"`
	// Evidence is absent on legacy parks. A new park records its external wait, inspected source receipt and
	// recording conversation here, in the existing Reviews JSON; it needs no new state store.
	Evidence *ParkEvidence `json:"evidence,omitempty"`
	// Automatic keeps closeout admission and its unchanged-dependency streak in the existing review JSON.
	Automatic *automaticPark `json:"automatic,omitempty"`
	// Reply is the source receipt that ended this exact park, committed with the pending-review transition.
	Reply *connectorwire.LarkReplyObservation `json:"reply,omitempty"`
	// ExternalPR is this external-event park's observed object, independent of the task's PR associations.
	ExternalPR *externalPRObservation `json:"external_pr,omitempty"`
	// ExternalIssue is the last read of the GitHub issue this park's evidence names (park_issue_wake.go, #14050).
	ExternalIssue *externalIssueObservation `json:"external_issue,omitempty"`
	// Decided: the owner's decision (its id) that ended this park before its time (parkDecided, parked.go), and
	// DecidedTold: when a supervision message carried that to the supervisor.
	Decided     string    `json:"decided,omitempty"`
	DecidedTold time.Time `json:"decided_told,omitzero"`
	// RestateAt: when the hub handed this needs_user back to its supervisor because the owner could not answer it
	// (undecidable, or owner_cannot_answer): told once.
	RestateAt time.Time `json:"restate_at,omitzero"`
	// RecheckAt: when this decision went back to its supervisor to confirm stale facts. While it is the latest review,
	// the old round is neither shown nor answerable; a fresh needs_user review starts a new round (decision.go).
	RecheckAt time.Time `json:"recheck_at,omitzero"`
	// OwnerResponse: the fixed answer the owner gave its decision (decide_yourself, not_what_i_asked, explain_first)
	// and when; HoldReleased: after not_what_i_asked, the supervisor sent the task's agent on again.
	OwnerResponse   string    `json:"owner_response,omitempty"`
	OwnerResponseAt time.Time `json:"owner_response_at,omitzero"`
	HoldReleased    bool      `json:"hold_released,omitempty"`
	// Handoff is an internal wait recorded after session.send was acknowledged.
	Handoff *taskHandoff `json:"handoff,omitempty"`
	// SupersededReason records the supervisor's natural-language reason when this task_review superseded an earlier
	// pending needs_user decision without fabricating an owner selection (#13112).
	SupersededReason string `json:"superseded_reason,omitempty"`
	// WithdrewNeedsUser: this accepted review is the supervisor taking back the needs_user it had raised
	// (task_review withdraw_needs_user): no owner answered it, and nothing was pushed or carded for it.
	WithdrewNeedsUser bool `json:"withdrew_needs_user,omitempty"`
}

// OwnerWait is a judgment that a supervised task's stretch of work (begun at Work) stopped on a decision its agent
// says is the owner's: when it was judged and the judge's reason. It goes to the supervisor, never straight to the
// owner (noteOwnerWait). PushPending and NotifiedAt are what earlier versions recorded of their own push to the owner;
// they are read and kept, never set. CardlessToldAt records this wait's own card-less hand-back independently of the
// pending_review round marker on Supervision.
type OwnerWait struct {
	Work           time.Time  `json:"work"`
	At             time.Time  `json:"at"`
	Reason         string     `json:"reason,omitempty"`
	PushPending    bool       `json:"push_pending,omitempty"`
	NotifiedAt     *time.Time `json:"notified_at,omitempty"`
	CardlessToldAt *time.Time `json:"cardless_told_at,omitempty"`
}

type ReviewCheck struct {
	Criterion string `json:"criterion"`
	Met       bool   `json:"met"`
	Evidence  string `json:"evidence,omitempty"`
}

func (v *Supervision) clone() *Supervision {
	if v == nil {
		return nil
	}
	out := *v
	out.DoneWhen = slices.Clone(v.DoneWhen)
	out.Criteria = db.AlignTodoCriteria(v.DoneWhen, v.Criteria)
	out.Reviews = make([]TaskReview, len(v.Reviews))
	for i, r := range v.Reviews {
		out.Reviews[i] = r
		if r.Handoff != nil {
			handoff := *r.Handoff
			out.Reviews[i].Handoff = &handoff
		}
		out.Reviews[i].Checks = slices.Clone(r.Checks)
		out.Reviews[i].Options = slices.Clone(r.Options)
		out.Reviews[i].Evidence = r.Evidence.clone()
		if r.Automatic != nil {
			a := *r.Automatic
			out.Reviews[i].Automatic = &a
		}
		if r.Reply != nil {
			receipt := *r.Reply
			out.Reviews[i].Reply = &receipt
		}
		if r.ExternalPR != nil {
			observation := *r.ExternalPR
			out.Reviews[i].ExternalPR = &observation
		}
	}
	if out.DoneWhen == nil {
		out.DoneWhen = []string{}
	}
	if v.Progress != nil && !isCodexRouteProgress(v.Progress) {
		p := *v.Progress
		out.Progress = &p
	} else {
		out.Progress = nil
	}
	out.OwnerWork = make([]ownerSessionWork, len(v.OwnerWork))
	for i, w := range v.OwnerWork {
		out.OwnerWork[i] = w
		if w.Instruction != nil {
			instruction := *w.Instruction
			out.OwnerWork[i].Instruction = &instruction
		}
	}
	if v.AutoReview != nil {
		a := *v.AutoReview
		out.AutoReview = &a
	}
	if v.Waits != nil {
		w := *v.Waits
		if v.Waits.NotifiedAt != nil {
			at := *v.Waits.NotifiedAt
			w.NotifiedAt = &at
		}
		if v.Waits.CardlessToldAt != nil {
			at := *v.Waits.CardlessToldAt
			w.CardlessToldAt = &at
		}
		out.Waits = &w
	}
	if v.PendingSince != nil {
		at := *v.PendingSince
		out.PendingSince = &at
	}
	if v.FollowedUpAt != nil {
		at := *v.FollowedUpAt
		out.FollowedUpAt = &at
	}
	if v.CardlessToldAt != nil {
		at := *v.CardlessToldAt
		out.CardlessToldAt = &at
	}
	out.Investigation = v.Investigation.clone()
	out.Parked = out.parked()
	return &out
}

// state is the review state that holds now: an accepted or cancelled task the user reopened goes on again, and a
// parked one that was closed is no longer parked.
func (v *Supervision) state(t Todo) string {
	if v == nil {
		return ""
	}
	if v.State == reviewUserCheck && t.DoneAt != nil {
		return reviewAccepted
	}
	if v.State == reviewParked && (t.DoneAt != nil || v.parking() == nil) {
		return ""
	}
	if t.DoneAt == nil && (v.State == reviewAccepted || v.State == reviewCancelled) {
		return ""
	}
	return v.State
}

// resetReopenedUserCheck mirrors the store's reopen and also reads rows written before the ledger reset was atomic.
// A review written after the reopen starts a fresh check.
func (t *Todo) resetReopenedUserCheck() {
	v := t.Supervision
	if t.DoneAt != nil || t.ReopenedAt == nil || v == nil || v.State != reviewUserCheck {
		return
	}
	reviewedAt := v.UpdatedAt
	if userCheckAsked(*t) {
		reviewedAt = v.Reviews[len(v.Reviews)-1].At
	}
	if reviewedAt.Before(*t.ReopenedAt) {
		t.Supervision = v.clone()
		t.Supervision.State = ""
		t.Supervision.PendingSince = nil
		t.Supervision.UpdatedAt = maxTime(v.UpdatedAt, *t.ReopenedAt)
	}
}

// supervisionStore is what the ledgers keep in the database; a store without it keeps them in memory only.
type supervisionStore interface {
	ListTodoSupervision(ctx context.Context) ([]db.TodoSupervisionRow, error)
	PutTodoSupervision(ctx context.Context, r db.TodoSupervisionRow) error
	ReviewTodo(ctx context.Context, id, reason string, accepted bool, at time.Time, ledger *db.TodoSupervisionRow, guard func() error) (*db.TodoRow, bool, error)
	LinkTodoReviewer(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, via string, at time.Time) error
}

var _ supervisionStore = (*db.DB)(nil)

func (s *Service) supervisionDB() supervisionStore {
	st, _ := s.store().(supervisionStore)
	return st
}

func supervisionFromRow(r db.TodoSupervisionRow) *Supervision {
	v := &Supervision{Supervisor: r.Supervisor, SupervisorSource: r.SupervisorSource, DoneWhen: r.DoneWhen, Criteria: r.Criteria, ExpectMinutes: r.ExpectMinutes, State: r.State, Round: r.Round,
		PendingSince: r.PendingSince, CardlessToldAt: r.CardlessToldAt, FollowedUpAt: r.FollowedUpAt, UpdatedAt: r.UpdatedAt}
	_ = json.Unmarshal(r.Reviews, &v.Reviews)
	if len(r.OwnerWork) > 0 {
		_ = json.Unmarshal(r.OwnerWork, &v.OwnerWork)
	}
	v.StalledReworkRounds = v.stalledReworkRounds()
	if len(r.InvestigationFindings) != 0 {
		v.Investigation = &investigationFindings{}
		_ = json.Unmarshal(r.InvestigationFindings, v.Investigation)
	}
	if len(r.AutoReview) > 0 {
		var a AutoReview
		if json.Unmarshal(r.AutoReview, &a) == nil {
			v.AutoReview = &a
		}
	}
	if len(r.Waits) > 0 {
		var w OwnerWait
		if json.Unmarshal(r.Waits, &w) == nil {
			v.Waits = &w
		}
	}
	return v.clone()
}

// stalledReworkRounds returns the effective consecutive stalled/low-ROI rework count for the current
// supervision round. It reconstructs cleanly from persisted Reviews JSON (without needing a new DB column)
// and treats legacy/unaccounted Round counts conservatively as stalled for full backward compatibility.
func (v *Supervision) stalledReworkRounds() int {
	if v == nil || v.Round <= 0 {
		return 0
	}
	var last *TaskReview
	for i := len(v.Reviews) - 1; i >= 0; i-- {
		r := &v.Reviews[i]
		if (r.Verdict == reviewAccepted && !r.UserCheck) || r.Verdict == reviewCancelled {
			break
		}
		if r.Verdict == reviewRework {
			last = r
			break
		}
	}
	if last == nil {
		return max(v.StalledReworkRounds, v.Round)
	}
	base := last.StalledReworkRounds
	if last.ReworkKind == "" && last.StalledReworkRounds == 0 {
		base = max(1, last.Round)
	}
	if v.Round > last.Round {
		base += v.Round - last.Round
	}
	return max(v.StalledReworkRounds, base)
}

// reworkBlocked checks whether a rework of kind is blocked by either the overall safety valve
// (maxTotalReworkRounds) or the strict stalled/low-ROI circuit breaker (maxReworkRounds).
func (v *Supervision) reworkBlocked(kind string) (blocked, safetyCap bool, count int) {
	if v == nil {
		return false, false, 0
	}
	if v.Round >= maxTotalReworkRounds {
		return true, true, v.Round
	}
	if isStalledReworkKind(kind) {
		if stalled := v.stalledReworkRounds(); stalled >= maxReworkRounds {
			return true, false, stalled
		}
	}
	return false, false, 0
}

// applyReworkAccounting updates total Round and consecutive StalledReworkRounds for a rework of kind.
func (v *Supervision) applyReworkAccounting(kind string) (round, stalled int) {
	curStalled := v.stalledReworkRounds()
	v.Round++
	switch kind {
	case reworkExternalInput:
		v.StalledReworkRounds = curStalled
	case reworkSubstantiveNew:
		v.StalledReworkRounds = 0
	default:
		v.StalledReworkRounds = curStalled + 1
	}
	return v.Round, v.StalledReworkRounds
}

func (v *Supervision) row(todoID string) db.TodoSupervisionRow {
	raw, _ := json.Marshal(v.Reviews)
	var auto, waits, investigation json.RawMessage
	ownerWork, _ := json.Marshal(v.OwnerWork)
	if len(v.OwnerWork) == 0 {
		ownerWork = json.RawMessage("[]")
	}
	if v.AutoReview != nil {
		auto, _ = json.Marshal(v.AutoReview)
	}
	if v.Waits != nil {
		waits, _ = json.Marshal(v.Waits)
	}
	if v.Investigation != nil {
		investigation, _ = json.Marshal(v.Investigation)
	}
	return db.TodoSupervisionRow{TodoID: todoID, InvestigationFindings: investigation, Supervisor: v.Supervisor, SupervisorSource: v.SupervisorSource, DoneWhen: v.DoneWhen, Criteria: v.Criteria, ExpectMinutes: v.ExpectMinutes,
		State: v.State, Round: v.Round, Reviews: raw, AutoReview: auto, Waits: waits, OwnerWork: ownerWork,
		PendingSince: v.PendingSince, CardlessToldAt: v.CardlessToldAt, FollowedUpAt: v.FollowedUpAt, UpdatedAt: v.UpdatedAt}
}

// cleanDoneWhen is a task's criteria as kept: trimmed, no empty or repeated ones, at most 6 of at most 200 characters.
func cleanDoneWhen(list []string) []string {
	out := []string{}
	for _, c := range list {
		c = clip(oneLine(c), 200)
		if c != "" && !slices.Contains(out, c) && len(out) < 6 {
			out = append(out, c)
		}
	}
	return out
}

// superviseTodo makes todo id a supervised task of conversation supervisor. A persistence failure leaves its in-memory
// ledger untouched; callers that must not start untracked work check the returned error.
func (s *Service) superviseTodo(ctx context.Context, id, supervisor string, doneWhen []string, expect int, source ...string) error {
	_, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		if len(source) > 0 && !v.assigned() {
			v.SupervisorSource = source[0]
		}
		v.Supervisor, v.DoneWhen, v.ExpectMinutes = supervisor, cleanDoneWhen(doneWhen), max(0, min(expect, 24*60))
		return true
	})
	if err != nil {
		s.logf("task %s: supervision: %v", id, err)
	}
	return err
}

// startSupervisionRound: a plan's task took todo id over to start a new Agent on it (task_plan continues, or its
// todo_id; takeTodoForTask), so the work from here is a new round of its supervision (#11553). A terminal review of the
// work before it (accepted or cancelled) stays in the ledger's reviews, as history, and no longer decides the to-do's
// state: the ledger goes back to going on, with no reworks and no owner wait yet. Otherwise every client (web
// acceptanceStands, Apple, Android and the hub's triage alike) went on showing the earlier round's acceptance on the
// new work, filed the to-do under Just finished or Done whenever its new Agent stopped, and superviseJudged dropped the
// new round's stops as more work sent after an acceptance. That rule stays for what it was made for (#3156, #7539):
// more work sent to an accepted task's Agent, a message on its card, a Reopen by hand. A to-do with no ledger gets
// none here, and a ledger in any other state is the current round's already.
func (s *Service) startSupervisionRound(ctx context.Context, id string) {
	if t, ok := s.todoNow(id); !ok || t.Supervision == nil {
		return
	}
	if _, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		changed := false
		if v.State == reviewAccepted || v.State == reviewCancelled {
			v.State, v.Round, v.StalledReworkRounds, v.Waits = "", 0, 0, nil
			changed = true
		}
		// A new Agent round is distinct evidence even when a scope already cleared the terminal state, so there is no
		// database field left to change. This process-local epoch still prevents a failed takeover restoring acceptance.
		if s.supervise.roundEpochs == nil {
			s.supervise.roundEpochs = map[string]uint64{}
		}
		s.supervise.roundEpochs[id]++
		return changed
	}); err != nil {
		s.logf("task %s: new supervision round: %v", id, err)
	}
}

// pendingSince is a ledger's PendingSince after a write at now that took its stored state from before to after, given
// the one it held (since): now on the way into pending_review, unchanged while the review waits (whatever else the write
// changed: a criterion's report, a new supervisor), nil in every other state. A review that already waited with no
// time recorded gets none: the apps then keep it under Needs you, where it was.
func pendingSince(before, after string, since *time.Time, now time.Time) *time.Time {
	switch {
	case after != reviewPending:
		return nil
	case before != reviewPending:
		return &now
	}
	return since
}

// cardlessToldSince keeps a card-less hand-back only inside the pending_review round it was raised in: a new round
// (the way in, before != pending_review) and every other state clear it, so the supervisor is told once per review
// round. While the same round waits, a write keeps what the change set (the watch stamps it once, supervise_cardless.go).
func cardlessToldSince(before, after string, told *time.Time) *time.Time {
	if after != reviewPending || before != reviewPending {
		return nil
	}
	return told
}

// updateSupervision changes todo id's ledger (a new one when it has none) with change, which says whether it changed
// anything; the ledger as it is then. Writes are one at a time.
func (s *Service) updateSupervision(ctx context.Context, id string, change func(*Supervision) bool) (Supervision, bool, error) {
	return s.updateSupervisionAt(ctx, id, time.Now().UTC(), change)
}

type taskReviewClose struct {
	reason    string
	accepted  bool
	untouched SessionRef
	closed    bool
	row       *db.TodoRow
	after     func()
}

// updateSupervisionWithClose validates on a private ledger, then commits it with a terminal review's closure before
// the caller records any review effects. A late activity refusal therefore leaves no review on the open task.
func (s *Service) updateSupervisionWithClose(ctx context.Context, id string, change func(*Supervision) bool, closing *taskReviewClose) (Supervision, bool, error) {
	return s.updateSupervisionGuardedWithClose(ctx, id, nil, change, closing)
}

// updateSupervisionGuardedWithClose validates a current-todo guard and a private ledger in one write boundary,
// committing a terminal review's closure before any post-commit effects run.
func (s *Service) updateSupervisionGuardedWithClose(ctx context.Context, id string, guard func(Todo) bool, change func(*Supervision) bool, closing *taskReviewClose) (Supervision, bool, error) {
	if closing == nil {
		return s.updateSupervisionGuarded(ctx, id, guard, change)
	}
	s.loadSeen(ctx)
	_ = s.loadTaskActivity(ctx)
	// Match guarded supervision writes: the to-do writer must be acquired before the ledger writer.
	s.todos.writing.Lock()
	s.supervise.writeMu.Lock()
	v, changed, err := s.updateSupervisionGuardedLockedAt(ctx, id, time.Now().UTC(), guard, change, closing)
	s.supervise.writeMu.Unlock()
	s.todos.writing.Unlock()
	if closing.after != nil {
		closing.after()
	}
	return v, changed, err
}

// updateSupervisionAt is updateSupervision at the caller's supervision-pass time.
func (s *Service) updateSupervisionAt(ctx context.Context, id string, now time.Time, change func(*Supervision) bool) (Supervision, bool, error) {
	return s.updateSupervisionGuardedAt(ctx, id, now, nil, change)
}

// updateSupervisionGuarded is updateSupervision with a current-todo guard evaluated in the same write boundary.
func (s *Service) updateSupervisionGuarded(ctx context.Context, id string, guard func(Todo) bool, change func(*Supervision) bool) (Supervision, bool, error) {
	return s.updateSupervisionGuardedAt(ctx, id, time.Now().UTC(), guard, change)
}

// updateSupervisionGuardedAt preserves both the caller's supervision-pass time and the guarded write boundary.
func (s *Service) updateSupervisionGuardedAt(ctx context.Context, id string, now time.Time, guard func(Todo) bool, change func(*Supervision) bool) (Supervision, bool, error) {
	if guard != nil {
		// Match editTodo's write boundary. The guard, stored receipt and in-memory mirror are one admission relative
		// to a user close; taking writeMu first would invert the todos.writing → writeMu order used by other writers.
		s.todos.writing.Lock()
		defer s.todos.writing.Unlock()
	}
	b := &s.supervise
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return s.updateSupervisionGuardedLockedAt(ctx, id, now, guard, change, nil)
}

// updateSupervisionLockedAt is updateSupervisionAt with supervise.writeMu already held.
func (s *Service) updateSupervisionLockedAt(ctx context.Context, id string, now time.Time, change func(*Supervision) bool) (Supervision, bool, error) {
	return s.updateSupervisionGuardedLockedAt(ctx, id, now, nil, change, nil)
}

// updateSupervisionGuardedLockedAt is the guarded form with supervise.writeMu already held.
func (s *Service) updateSupervisionGuardedLockedAt(ctx context.Context, id string, now time.Time, guard func(Todo) bool, change func(*Supervision) bool, closing *taskReviewClose) (Supervision, bool, error) {
	t, ok := s.todoNow(id)
	if !ok {
		return Supervision{}, false, db.ErrTodoNotFound
	}
	v := t.Supervision.clone()
	if v == nil {
		v = (&Supervision{}).clone()
	}
	if guard != nil && !guard(t) {
		return *v, false, nil
	}
	before := v.State
	var parkedAt time.Time
	if p := v.parking(); p != nil {
		parkedAt = p.At
	}
	park := v.parking()
	if !change(v) {
		return *v, false, nil
	}
	v.Criteria = db.AlignTodoCriteria(v.DoneWhen, v.Criteria)
	v.UpdatedAt = now.UTC()
	if p := v.parking(); !parkedAt.IsZero() && (p == nil || !p.At.Equal(parkedAt)) {
		for i := len(v.Reviews) - 1; i >= 0; i-- {
			r := &v.Reviews[i]
			if r.Verdict == reviewParked && r.At.Equal(parkedAt) {
				r.ParkEndedAt = v.UpdatedAt
				if i+1 < len(v.Reviews) {
					r.ParkEndedAt = v.Reviews[i+1].At
				}
				break
			}
		}
	}
	v.PendingSince = pendingSince(before, v.State, v.PendingSince, v.UpdatedAt)
	v.CardlessToldAt = cardlessToldSince(before, v.State, v.CardlessToldAt)
	if park != nil && v.State == reviewPending && park.ExternalPR != nil && park.ExternalPR.wakes(park) {
		// Match the review period after a reload. Postgres stores PendingSince to microsecond precision.
		park.ExternalPR.PendingSince = v.UpdatedAt.Truncate(time.Microsecond)
	}
	if closing != nil && !t.isConcern() {
		var err error
		closing.row, closing.after, err = s.reviewDoneTodoLocked(ctx, id, closing.reason, closing.accepted, v, false, closing.untouched)
		closing.closed = closing.row != nil
		if err != nil {
			return Supervision{}, false, err
		}
		if closing.closed {
			return *v, true, nil // the row and ledger were published together
		}
		// ReviewTodo also commits the ledger when the task was already closed.
	} else if st := s.supervisionDB(); st != nil && s.todoDB() != nil {
		if err := st.PutTodoSupervision(ctx, v.row(id)); err != nil {
			return Supervision{}, false, err
		}
	}
	s.todos.mu.Lock()
	if cur := s.todos.todos[id]; cur != nil {
		next := v.clone()
		if cur.Supervision != nil { // the progress is noteProgress's: a report that came meanwhile stays
			next.Progress = cur.Supervision.clone().Progress
		}
		cur.Supervision = next
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	return *v, true, nil
}

// noteProgress keeps p as supervised task id's progress (in memory, no ledger write: see Supervision.Progress) and
// tells the apps; false when id is no supervised task or p is a legacy startup notice.
func (s *Service) noteProgress(ctx context.Context, id string, p TaskProgress) bool {
	if isCodexRouteProgress(&p) {
		return false
	}
	s.todos.mu.Lock()
	cur := s.todos.todos[id]
	ok := cur != nil && cur.Supervision != nil
	if ok {
		v := cur.Supervision.clone()
		v.Progress = &p
		cur.Supervision = v
	}
	s.todos.mu.Unlock()
	if ok {
		s.todosChanged(ctx)
	}
	return ok
}

// clearDoneProgress drops a prior turn's in-memory done report when a new turn begins on supervised task id, so an
// earlier done report cannot settle shells left behind by a later turn (#16418).
func (s *Service) clearDoneProgress(ctx context.Context, id string) {
	s.todos.mu.Lock()
	cur := s.todos.todos[id]
	cleared := false
	if cur != nil && cur.Supervision != nil && cur.Supervision.Progress != nil && cur.Supervision.Progress.Kind == upReportDone {
		v := cur.Supervision.clone()
		v.Progress = nil
		cur.Supervision = v
		cleared = true
	}
	s.todos.mu.Unlock()
	if cleared {
		s.todosChanged(ctx)
	}
}

// ---- the snapshot ----

// taskHealth is how supervised task t stands, for the snapshot: a label and a rank (lower: the more it needs the
// assistant). "" for a task that is not supervised.
func taskHealth(t Todo, agents []proto.Session, now time.Time) (string, int) {
	v := t.Supervision
	if v == nil {
		return "", 5
	}
	switch v.state(t) {
	case reviewUserCheck:
		return "待你验收", 0 // i18n-ignore: the snapshot is the assistant's prompt
	case reviewNeedsUser:
		if triageReviewState(t) == reviewNeedsUser {
			return "等你处理", 0
		}
	case reviewPending:
		return "已交付·待看", 1
	case reviewAccepted:
		return "已验收", 8
	case reviewCancelled:
		return "已取消", 8
	case reviewParked: // waits on purpose (parked.go): below everything that needs the assistant
		return parkedHealth(v.parked(), now.Location()), 6
	}
	if t.failedStart() {
		// Its agent never started: stored done (#1077) but nothing was done. Only the supervisor can retry it, so it
		// heads the list rather than sitting among the finished ones.
		return "启动失败，待你处理（重试只有你能做）", 0 // i18n-ignore: the snapshot is the assistant's prompt
	}
	if t.DoneAt != nil {
		return "", 8
	}
	if v.Waits != nil { // the judge found it stopped on what its agent says is the owner's decision (#1277)
		return "Agent 停下说要用户拍板：先判断是不是用户的事，别直接验收", 0 // i18n-ignore: the snapshot is the assistant's prompt
	}
	if snapshotDoneRule(t, agents, nil, nil, now, now.Location()) == ruleAccepted {
		// v.state masks an accepted review after reopening; acceptanceStands inside the shared triage rule still holds
		// until a newer review. Reopened accepted work is normal, not a second delivery awaiting review (#5732).
		return "正常", 5 // i18n-ignore: the snapshot is the assistant's prompt
	}
	if deliveryAwaitsReview(t, agents) {
		return "已交付·待看", 1 // i18n-ignore: the snapshot is the assistant's prompt
	}
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			if wait := now.Sub(cmp.Or(c.StateSince, c.UpdatedAt)); wait >= time.Minute {
				return "等回答 " + humanDuration(wait), 0
			}
			return "等回答", 0
		}
	}
	for _, c := range agents {
		if c.State != proto.StateWorking || aliveBackgroundWork(c, now) {
			continue
		}
		moved := stalledSessionMoved(c)
		if still := now.Sub(moved); !moved.IsZero() && still >= stallAfter(v.ExpectMinutes) {
			return "卡住 " + humanDuration(still), 2
		}
	}
	if v.state(t) == reviewRework {
		return fmt.Sprintf("返工第 %d 轮", v.Round), 3
	}
	return "正常", 5
}
