package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type projectReviewConfig struct {
	Enabled           bool       `json:"enabled"`
	Target            SessionRef `json:"target"`
	Model             string     `json:"model"`
	Projects          []string   `json:"projects"`
	AppInputs         bool       `json:"app_inputs"`
	FeishuOwnerInputs bool       `json:"feishu_owner_inputs"`
	ReviewUnassigned  bool       `json:"review_unassigned_tasks"`
}

type projectReviewBinding struct {
	SessionRef
	Project  string    `json:"project"`
	Todo     string    `json:"todo"`
	LinkedAt time.Time `json:"linked_at,omitzero"`
}

var projectReviewRef = lazyre.New(`^[a-zA-Z0-9][a-zA-Z0-9_.:/@-]{0,255}$`)

// projectReviewOwnerRef names an App message in the review event. The send API takes any non-empty client_msg_id;
// one that is not a plain reference is named by its hash, so it is still reviewed once and its text never reaches
// the review prompt as it is.
func projectReviewOwnerRef(id string) string {
	if projectReviewRef.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "app-message:" + hex.EncodeToString(sum[:16])
}

func (c projectReviewConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if !projectReviewRef.MatchString(c.Target.MachineID) || !projectReviewRef.MatchString(c.Target.SessionID) ||
		!projectReviewRef.MatchString(c.Model) || len(c.Projects) > 512 {
		return errors.New("project_review: valid target and model required, at most 512 projects")
	}
	for _, project := range c.Projects {
		if !projectReviewRef.MatchString(project) {
			return errors.New("project_review: invalid project slug")
		}
	}
	return nil
}

type projectReview struct {
	projectReviewConfig
	db   *db.DB
	wake chan struct{}
}

type projectReviewEvent struct {
	Source    SessionRef `json:"source"`
	Project   string     `json:"project"`
	Todo      string     `json:"todo"`
	Kind      string     `json:"kind"`
	Reference string     `json:"reference"`
	LinkedAt  time.Time  `json:"linked_at,omitzero"`
}

// taskBinding uses the authoritative project and active agent assignment. A non-zero at is a time on the
// hub's own clock (an owner input as the hub queued it) and must not predate the assignment. Node times (turn
// ends, session rows) pass zero: the fleet reports them for the agent as it is assigned now, a first turn can end
// before the hub writes the link, and a node clock can run behind the hub's.
func (p *projectReview) taskBinding(ref SessionRef, task Todo, at time.Time) (projectReviewBinding, bool) {
	if p == nil || !p.Enabled || ref == p.Target || task.Cancelled() || task.Origin == "" ||
		!projectReviewRef.MatchString(task.ID) ||
		!(task.Project == "" && p.ReviewUnassigned || slices.Contains(p.Projects, task.Project)) {
		return projectReviewBinding{}, false
	}
	for _, active := range task.Agents {
		if active.active() && active.MachineID == ref.MachineID && active.SessionID == ref.SessionID && (at.IsZero() || !at.Before(active.LinkedAt)) {
			// Use the ledger's Postgres precision both before and after the task is reloaded.
			linked := active.LinkedAt.UTC().Truncate(time.Microsecond)
			return projectReviewBinding{SessionRef: ref, Project: task.Project, Todo: task.ID, LinkedAt: linked}, true
		}
	}
	return projectReviewBinding{}, false
}

func (p *projectReview) record(ctx context.Context, b projectReviewBinding, kind, ref, phase string, at time.Time) error {
	base, _ := json.Marshal(b)
	target, _ := json.Marshal(p.Target)
	e := projectReviewEvent{Source: b.SessionRef, Project: b.Project, Todo: b.Todo, Kind: kind, Reference: ref, LinkedAt: b.LinkedAt}
	raw, _ := json.Marshal(e)
	// Project reassignment or revocation cannot change the identity of a redelivered input.
	identity, _ := json.Marshal([]any{b.SessionRef, kind, ref})
	id := namedClientID("project review\n" + string(target) + "\n" + string(identity))
	observed, err := p.db.RecordProjectReview(ctx, id, string(target), string(base), raw, phase, at)
	if observed && err == nil {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
	return err
}

// projectReviewFeishuInput queues the owner's message in the paired Feishu chat (ref "feishu:<message id>", sent to
// target), which the lark module checked is the owner's, in a bound chat, handed over by the paired bot itself. It is
// a review signal only (no project): the chat's target may discuss any. A message to the review session itself is not
// its input.
func (s *Service) projectReviewFeishuInput(ctx context.Context, ref string, target SessionRef) error {
	p := s.projectReview
	if p == nil || !p.FeishuOwnerInputs || !projectReviewRef.MatchString(ref) || target == p.Target {
		return nil
	}
	return p.record(ctx, projectReviewBinding{}, "owner_input", ref, "", time.Time{})
}

func (s *Service) projectReviewOwnerInput(ctx context.Context, in assistantwire.ProjectReviewOwnerInput) error {
	p := s.projectReview
	if p == nil || !p.AppInputs || in.SessionRef == p.Target || in.MessageID == "" {
		return nil
	}
	_, guest, err := s.guestOf(ctx, in.MachineID, in.SessionID)
	if err != nil || guest {
		return err
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	b := projectReviewBinding{SessionRef: in.SessionRef}
	// A source without one current authorized task is a review signal only.
	// In particular the main conversation may discuss multiple projects.
	// in.At is the hub's clock when it queued the input. The task is authorized again at delivery
	// (projectReviewStep), so a later task update does not take the input out of scope.
	// A new Agent's first prompt: that session did not exist before this start, so the input belongs to the
	// task it is linked to whatever the hub's queue clock and the link time say. A start that names a to-do queues it
	// only after the link (hub startSession). One that names none has no task when it is delivered (an app start gets
	// its quiet task when a turn ends, agent_tasks.go), so like any input from a session without a task it is a
	// review signal only.
	at := in.At
	if string(in.MessageID) == "first-agent:"+in.SessionID {
		at = time.Time{}
	} else if at.IsZero() {
		in.Unscoped = true
	}
	if !in.Unscoped {
		if task, ok := s.taskOf(ctx, in.MachineID, in.SessionID); ok {
			if binding, found := p.taskBinding(in.SessionRef, task, at); found {
				b = binding
			}
		}
	}
	return p.record(ctx, b, "owner_input", projectReviewOwnerRef(string(in.MessageID)), "", time.Time{})
}

func (s *Service) projectReviewTurn(ctx context.Context, task Todo, row proto.Session, end turnClose, state string) {
	p := s.projectReview
	// The task ledger already decided that this turn is the task's (task_turns.go); end.At is the node's clock.
	b, ok := p.taskBinding(SessionRef{MachineID: row.MachineID, SessionID: row.SessionID}, task, time.Time{})
	// A turn without its own id ends at an item; Codex names an aborted turn's item by position (line12#0).
	if !ok || end.At.IsZero() || !projectReviewRef.MatchString(end.Key) && !proto.PositionalItemID(end.Key) ||
		(state != turnDone && state != turnInterrupted) {
		return
	}
	kind, phase := "execution_completed", "completed"
	if state == turnInterrupted {
		kind, phase = "execution_interrupted", "blocked"
	}
	if err := p.record(ctx, b, kind, end.Key, phase, end.At); err != nil {
		s.logf("project review: record turn: %v", err)
	}
}

func projectReviewProgress(row proto.Session) time.Time {
	at := row.UpdatedAt
	if row.ActivityAt != nil && row.ActivityAt.After(at) {
		at = *row.ActivityAt
	}
	return at
}

// The fleet already observes these changes. Repeated inventory rows and cards have no
// effect on an open blocking episode. Only newer work closes that episode.
func (s *Service) projectReviewSession(ctx context.Context, row proto.Session, previous string) {
	p := s.projectReview
	if p == nil || row.Assistant || row.Starting || row.Status == proto.StatusUnknown || row.Status == proto.StatusOffline {
		return
	}
	at := projectReviewProgress(row)
	if at.IsZero() {
		return
	}
	var kind, phase string
	switch {
	case row.Status == proto.StatusBlocked || row.State == proto.StateNeedsYou || row.PendingInteractions > 0:
		kind, phase = "waiting_for_owner", "blocked"
	case row.Status == proto.StatusInterrupted:
		kind, phase = "execution_interrupted", "blocked"
	case row.Status != proto.StatusWorking && row.TurnError != "":
		kind, phase = "execution_failed", "blocked"
	case row.Status == proto.StatusWorking:
		switch {
		case previous == proto.StatusBlocked || previous == proto.StatusInterrupted ||
			row.ActivityAt != nil && activityCall(row.Activity) == callProgress:
			phase = "working"
		case previous == "":
			phase = "working_after_reconnect"
		default:
			return
		}
	default:
		return
	}
	task, _ := s.taskOf(ctx, row.MachineID, row.SessionID)
	// at (the node's clock) only names the episode: a row blocked since before the link never advances it.
	b, ok := p.taskBinding(SessionRef{MachineID: row.MachineID, SessionID: row.SessionID}, task, time.Time{})
	if !ok {
		return
	}
	if err := p.record(ctx, b, kind, at.UTC().Format(time.RFC3339Nano), phase, at); err != nil {
		s.logf("project review: record session: %v", err)
	}
}

func (s *Service) projectReviewStall(ctx context.Context, g superviseSignal, rows []proto.Session) {
	if g.Kind != sigStalled && g.Kind != sigNoProgress && g.Kind != sigNeedsYou {
		return
	}
	p := s.projectReview
	if p == nil {
		return
	}
	task, ok := s.taskOf(ctx, g.Machine, g.Session)
	if !ok || task.ID != g.Todo {
		return
	}
	for _, row := range rows {
		if row.MachineID == g.Machine && row.SessionID == g.Session {
			at := projectReviewProgress(row)
			b, authorized := p.taskBinding(SessionRef{MachineID: g.Machine, SessionID: g.Session}, task, time.Time{})
			kind := "stalled"
			if g.Kind == sigNeedsYou {
				kind = "waiting_for_owner"
			}
			if authorized && !at.IsZero() && projectReviewRef.MatchString(g.Key) {
				if err := p.record(ctx, b, kind, g.Key, "blocked", at); err != nil {
					s.logf("project review: record stall: %v", err)
				}
			}
			return
		}
	}
}

func (s *Service) projectReviewLoop(ctx context.Context) {
	p := s.projectReview
	if p == nil || !p.Enabled {
		return
	}
	for {
		bound, cancel := context.WithTimeout(ctx, projectReviewStepWait)
		wait, err := s.projectReviewStep(bound, time.Now())
		cancel()
		if err != nil && ctx.Err() == nil {
			s.logf("project review: delivery queue: %v", err)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-p.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

const projectReviewIdle = 10 * time.Second

// projectReviewStepWait bounds one step. It outlasts the hub's wait for a node to take a message to an idle session
// (90 s, hub sendWait): a shorter deadline turns a slow send into an uncertain one.
const projectReviewStepWait = 2 * time.Minute

// projectReviewMaxSends bounds the native sends of one delivery. A send whose outcome stays uncertain, or whose
// review turn closes without a valid receipt, or whose confirmed send never echoes (projectReviewEchoWait), is sent
// again under a new client message ID; after this many the delivery is settled with that outcome, so one event
// cannot hold the target's queue forever.
const projectReviewMaxSends = 5

// projectReviewEchoWait bounds the wait for the echo of a send the hub confirmed. The echo normally shows up within
// seconds; one still missing this long after the confirmation while the target sits idle was lost (the node dropped
// it after taking it, or the transcript was rewritten below the saved position), so the event is sent again, within
// projectReviewMaxSends, instead of holding the target's queue forever.
const projectReviewEchoWait = 10 * time.Minute

// projectReviewSendID names send n of delivery id. The hub sends a client message ID at most once and refuses one
// whose outcome it never learned, so every send needs its own. The first keeps the delivery's ID.
func projectReviewSendID(id string, n int) string {
	if n <= 1 {
		return id
	}
	return fmt.Sprintf("%s:%d", id, n)
}

// projectReviewOurs says whether a transcript item's client message ID is one of delivery e's sends.
func projectReviewOurs(e db.ProjectReviewDelivery, clientID string) bool {
	for n := 1; n <= max(e.Sends, 1); n++ {
		if clientID == projectReviewSendID(e.ID, n) {
			return true
		}
	}
	return false
}

func (s *Service) projectReviewStep(ctx context.Context, now time.Time) (time.Duration, error) {
	p := s.projectReview
	target, _ := json.Marshal(p.Target)
	delivery, ok, err := p.db.NextProjectReview(ctx, string(target))
	if err != nil || !ok {
		return projectReviewIdle, err
	}
	if wait := delivery.RetryAt.Sub(now); wait > 0 {
		return wait, nil
	}
	// The next delivery is looked at right away only after this one settled; a failed write waits like any error.
	settle := func(outcome string) (time.Duration, error) {
		if err := p.db.SettleProjectReview(ctx, delivery.ID, outcome); err != nil {
			return projectReviewIdle, err
		}
		return 0, nil
	}
	var event projectReviewEvent
	if err := json.Unmarshal(delivery.Event, &event); err != nil {
		return projectReviewIdle, err
	}
	if event.Source == p.Target {
		return settle("self_source")
	}
	writeAllowed := false
	if event.Project != "" && event.Todo != "" && (event.Kind != "owner_input" || p.AppInputs) {
		t, ok, err := s.todo(ctx, event.Todo)
		if err != nil {
			return projectReviewIdle, err
		}
		if ok && t.Project == event.Project {
			current, active := p.taskBinding(event.Source, t, time.Time{})
			writeAllowed = active && current.LinkedAt.Equal(event.LinkedAt)
		}
	}
	retry := func(reason string) (time.Duration, error) {
		wait := time.Second << min(delivery.Attempts, 6)
		return wait, p.db.RetryProjectReview(ctx, delivery.ID, now.Add(wait), reason)
	}
	sess, found := s.session(ctx, p.Target.MachineID, p.Target.SessionID)
	if !found || sess.Agent != "codex" || sess.Archived {
		return retry("target_not_ready")
	}
	ready := sess.Model == p.Model && (sess.Status == proto.StatusIdle || sess.Status == proto.StatusDone) && sess.PendingInteractions == 0
	// Reconcile against the original turn, even when the target has since started
	// a different turn or changed models. Its newest status is unrelated.
	if delivery.HistoryAfter != nil {
		r, err := s.projectReviewReceipt(ctx, p.Target, p.Model, delivery)
		if err != nil {
			return retry("target_history_unavailable")
		}
		switch {
		case r.outcome != "":
			return settle("review_receipt_" + r.outcome)
		case r.seen && !r.closed: // the review turn is still running
			return retry("review_receipt_unconfirmed")
		case r.seen: // it closed without a valid receipt: send again after it, under a new ID
			if delivery.Sends >= projectReviewMaxSends {
				return settle("review_receipt_missing")
			}
			if err := p.db.AdvanceProjectReviewHistory(ctx, delivery.ID, r.closedAt); err != nil {
				return projectReviewIdle, err
			}
		case delivery.SendConfirmed && (!ready || now.Sub(delivery.SendConfirmedAt) < projectReviewEchoWait):
			return retry("waiting_for_review_receipt") // the hub took the latest send; its echo is on the way
		case delivery.Sends >= projectReviewMaxSends && delivery.SendConfirmed:
			return settle("send_echo_missing")
		case delivery.Sends >= projectReviewMaxSends:
			return settle("send_unconfirmed")
		}
		// Otherwise send again under a new ID: the last send was uncertain, its turn closed without a receipt, or the
		// hub confirmed it long ago and its echo never came while the target sat idle.
	}
	if !ready {
		return retry("target_not_ready")
	}
	reviewOnly := !writeAllowed || delivery.ReviewOnly
	if delivery.HistoryAfter == nil {
		var page projectReviewPage
		if err := s.call(ctx, p.Target.MachineID, DeviceSessionItems, map[string]any{
			"session_id": p.Target.SessionID, "limit": 1, "lazy_images": true,
		}, &page); err != nil {
			return retry("target_history_unavailable")
		}
		after := 0
		if len(page.Items) > 0 {
			after = page.Items[len(page.Items)-1].Seq
		}
		if err := p.db.ReserveProjectReviewHistory(ctx, delivery.ID, after, reviewOnly); err != nil {
			return projectReviewIdle, err
		}
	} else if reviewOnly && !delivery.ReviewOnly {
		// Authorization ended after a scoped attempt was reserved: the retry sends only the triage prompt, and the
		// receipt is checked against what was sent. Save that before sending, or a triage_only receipt never settles
		// it and every later event for the target waits behind it.
		if err := p.db.DowngradeProjectReview(ctx, delivery.ID); err != nil {
			return projectReviewIdle, err
		}
	}
	if reviewOnly {
		delivery.Event = json.RawMessage(`{"kind":"review_only"}`)
	} else {
		event.LinkedAt = time.Time{} // assignment epoch is only used for authorization and deduplication
		delivery.Event, _ = json.Marshal(event)
	}
	text := prompts.RenderProjectReview(delivery.ID, delivery.Event, reviewOnly)
	n, err := p.db.BeginProjectReviewSend(ctx, delivery.ID)
	if err != nil {
		return projectReviewIdle, err
	}
	s.workTold(p.Target.MachineID, p.Target.SessionID)
	err = s.call(ctx, p.Target.MachineID, DeviceSessionSendOnce, map[string]any{
		"session_id": p.Target.SessionID, "client_msg_id": projectReviewSendID(delivery.ID, n), "text": text,
		"message_source": &proto.MessageSource{Kind: "project_review"},
	}, nil)
	if err != nil {
		return retry("send_unconfirmed")
	}
	if err := p.db.ConfirmProjectReviewSend(ctx, delivery.ID, n, now); err != nil {
		return projectReviewIdle, err
	}
	return retry("waiting_for_review_receipt")
}

type projectReviewPage struct {
	Items     []proto.Item `json:"items"`
	MoreAfter bool         `json:"more_after"`
}

// projectReviewScan is what the transcript after a delivery's saved position shows. seen: the echo of one of its
// sends. closed: the turn that echo started has ended; closedAt is the seq of the item that ended it. outcome: the
// valid receipt that turn ended with.
type projectReviewScan struct {
	seen, closed bool
	closedAt     int
	outcome      string
}

// Look for the exact native echo and the explicit receipt in the turn that it started.
// Pages can split the turn; keep the parser state until that turn closes.
func (s *Service) projectReviewReceipt(ctx context.Context, target SessionRef, model string, e db.ProjectReviewDelivery) (projectReviewScan, error) {
	after := *e.HistoryAfter
	var r projectReviewScan
	var reply proto.Item
	for {
		var page projectReviewPage
		if err := s.call(ctx, target.MachineID, DeviceSessionItems, map[string]any{
			"session_id": target.SessionID, "after_seq": after, "lazy_images": true,
		}, &page); err != nil {
			return r, err
		}
		start := after
		for _, item := range page.Items {
			if item.Seq <= after {
				continue
			}
			after = item.Seq
			ours := item.Role == "user" && !item.Queued && projectReviewOurs(e, item.ClientMsgID)
			if !r.seen {
				r.seen = ours
				continue
			}
			closed := func() (projectReviewScan, error) {
				r.closed, r.closedAt = true, item.Seq
				return r, nil
			}
			switch {
			case ours: // a later send of this delivery landed in the same turn: its reply is the one that counts
				reply = proto.Item{}
			case item.Role == "user" && !item.Queued,
				item.Wake, item.Kind == proto.KindNotice && item.Text == userStoppedNotice:
				return closed()
			case item.Kind == "turn_duration":
				if reply.Model != model {
					return closed()
				}
				if reply.FullSize > 0 {
					if err := s.call(ctx, target.MachineID, "session.item", map[string]any{
						"session_id": target.SessionID, "item_id": reply.ID,
					}, &reply); err != nil {
						return r, err
					}
				}
				r.outcome = projectReviewOutcome(reply.Text, e.ID, e.ReviewOnly, !e.ReviewOnly || e.Downgraded)
				return closed()
			case item.Role == "assistant" && item.Kind == proto.KindText && !item.Narration:
				reply = item
			}
		}
		if !page.MoreAfter {
			return r, nil
		}
		if after == start {
			return r, errors.New("project review transcript page did not advance")
		}
	}
}

// projectReviewOutcome reads the receipt line. triage: a triage prompt was sent; scoped: a scoped one may have been
// (a downgraded delivery's first, unconfirmed attempt may have landed before its triage resend).
func projectReviewOutcome(reply, id string, triage, scoped bool) string {
	lines := strings.Split(strings.TrimSpace(reply), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) != 3 || fields[0] != "PROJECT_REVIEW_RECEIPT" || fields[1] != id {
		return ""
	}
	switch fields[2] {
	case "triage_only":
		// Only a review-only event may be settled without reconciling a project.
		if triage {
			return fields[2]
		}
	case "facts_recorded", "no_change", "needs_evidence":
		if scoped {
			return fields[2]
		}
	}
	return ""
}
