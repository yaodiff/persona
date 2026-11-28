package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	a2aSubmitted = "submitted"
	a2aWorking   = "working"
	a2aInput     = "input_required"
	a2aAuth      = "auth_required"
	a2aComplete  = "completed"
	a2aFailed    = "failed"
	a2aCanceled  = "canceled"
	a2aQueued    = "queued"
	a2aSending   = "sending"
	a2aDelivered = "delivered"
	a2aUncertain = "uncertain"
	a2aExpired   = "unconfirmed"
	a2aDiscarded = "discarded"
	a2aMaxText   = 12000
	a2aMaxEvents = 96
)

var (
	errA2AUnavailable = errors.New("A2A is unavailable for this session")
	errA2ANotFound    = errors.New("A2A task not found")
	errA2AConflict    = errors.New("A2A task changed, retry with the same message_id")
	errA2ACancel      = errors.New("the A2A task cannot be canceled while its turn may be running")
	errA2AInvalid     = errors.New("invalid A2A request")
	errA2ADuplicate   = errors.New("message_id already identifies a different A2A message")
)

type a2aStore interface {
	A2ACreate(context.Context, db.A2ARow) (*db.A2ARow, error)
	A2ARequest(context.Context, string, string) (*db.A2ARow, error)
	A2AGet(context.Context, string) (*db.A2ARow, error)
	A2AList(context.Context, string, string, string, string, string, string, bool, *time.Time, int) ([]db.A2ARow, error)
	A2ACount(context.Context, string, string, string, string, string, *time.Time) (int, error)
	A2AUpdate(context.Context, db.A2ARow, time.Time, *time.Time) (bool, error)
	A2AClaim(context.Context, int) ([]db.A2ARow, error)
	A2AReady(context.Context, string, string) error
	A2ADispatches(context.Context, string, string, string, time.Time) ([]db.A2ARow, error)
	A2ALaunchesForTargets(context.Context, string, []string) ([]db.A2ARow, error)
}

type a2aRef struct {
	Machine string `json:"machine_id"`
	Session string `json:"session_id"`
}

type a2aCaller struct {
	Ref   a2aRef
	Owner string
}

type a2aEvent struct {
	// Seq is the position in the task's append-only conversation.
	Seq            int       `json:"seq"`
	ID             string    `json:"message_id"`
	Kind           string    `json:"kind"`
	Text           string    `json:"text,omitempty"`
	State          string    `json:"state,omitempty"`
	At             time.Time `json:"at"`
	Delivery       string    `json:"delivery,omitempty"`
	Attempted      time.Time `json:"attempted,omitzero"`
	WaitingSince   time.Time `json:"waiting_since,omitzero"`
	DeliveryReason string    `json:"delivery_reason,omitempty"`
}

type a2aBody struct {
	Wake         bool       `json:"wake"`
	Events       []a2aEvent `json:"events"`
	Launch       *a2aLaunch `json:"launch,omitempty"`
	Disconnected bool       `json:"disconnected,omitempty"`
	TargetClosed bool       `json:"target_closed,omitempty"`
}

type a2aLaunch struct {
	CWD            string `json:"cwd"`
	Worktree       string `json:"worktree,omitempty"`
	Launcher       string `json:"launcher,omitempty"`
	Tier           string `json:"tier,omitempty"` // the Agent preset the caller named; a retry by message_id names it too
	Run            a2aRun `json:"run,omitzero"`   // what Tier resolved to when the launch was accepted (agent_presets.go)
	PermissionMode string `json:"permission_mode,omitempty"`
	Pending        bool   `json:"pending"`
	Dispatch       bool   `json:"dispatch,omitempty"`
	Subagent       bool   `json:"subagent,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Title          string `json:"title,omitempty"`
	PromptHash     string `json:"prompt_hash,omitempty"`
}

type a2aTask struct {
	db.A2ARow
	a2aBody
}

func (t a2aTask) target() a2aRef { return a2aRef{t.TargetMachine, t.TargetSession} }
func (t a2aTask) source() a2aRef { return a2aRef{t.SourceMachine, t.SourceSession} }

func (t a2aTask) participant(c a2aCaller) bool {
	return t.Owner == c.Owner || c.Ref != (a2aRef{}) && t.target() == c.Ref
}

func a2aTerminal(state string) bool {
	return state == a2aComplete || state == a2aFailed || state == a2aCanceled
}

func (t *a2aTask) append(e a2aEvent) {
	e.Seq, e.At = len(t.Events)+1, time.Now().UTC()
	if a2aTerminal(e.State) {
		for i := range t.Events {
			old := &t.Events[i]
			if old.Kind != "message" && old.Delivery == a2aQueued {
				old.Delivery = a2aDiscarded
			}
		}
	}
	t.Events = append(t.Events, e)
	if e.State != "" {
		t.State = e.State
	}
}

func decodeA2A(row *db.A2ARow, err error) (*a2aTask, error) {
	if row == nil || err != nil {
		return nil, err
	}
	t := &a2aTask{A2ARow: *row}
	if err := json.Unmarshal(row.Body, &t.a2aBody); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) a2aConfig(ctx context.Context) assistantwire.A2AConfig {
	cfg := s.hello(ctx)
	if cfg.Assistant.Standby {
		return assistantwire.A2AConfig{Machines: []string{}}
	}
	return cfg.Agents.A2A
}

func (s *Service) a2aStore() (a2aStore, error) {
	st, ok := s.store().(a2aStore)
	if !ok || st == nil {
		return nil, errA2AUnavailable
	}
	return st, nil
}

func (s *Service) a2aMachineAllowed(ctx context.Context, cfg assistantwire.A2AConfig, machine string) bool {
	if machine == "" || cfg.Machines != nil && !slices.Contains(cfg.Machines, machine) ||
		s.isWorkMachine(machine) || s.machinePending(ctx, machine) {
		return false
	}
	if s.devs != nil {
		return s.devs.Has(machine)
	}
	dev, ok := s.machineByName(ctx, machine)
	return ok && dev.ID == machine
}

// Persona binds callers to Node-attested sessions and keeps discovery and delivery
// within the owning manager's confirmed computers. Peer discovery and unsolicited
// peer sends exclude subagents; A2A-launched subagents remain eligible as the target
// endpoint of their own launch task (a2aTaskEndpointAllowed / a2aToolAllowed).
func (s *Service) a2aAllowed(ctx context.Context, cfg assistantwire.A2AConfig, ref a2aRef) bool {
	return s.a2aEndpointAllowed(ctx, cfg, ref, false)
}

func (s *Service) a2aEndpointAllowed(ctx context.Context, cfg assistantwire.A2AConfig, ref a2aRef, allowSubagent bool) bool {
	if ref.Session == "" || !s.a2aMachineAllowed(ctx, cfg, ref.Machine) || s.conversationAvailable(ctx, ref.Machine, ref.Session) != nil {
		return false
	}
	if machine := s.logicalExecutionMachine(ctx, ref.Machine, ref.Session); machine != ref.Machine && !s.a2aMachineAllowed(ctx, cfg, machine) {
		return false
	}
	if !allowSubagent && s.isA2ASubagent(ctx, ref) {
		return false
	}
	if row, ok := s.nodeSession(ref.Machine, ref.Session); ok {
		visible := a2aDiscoverable(row)
		if allowSubagent {
			visible = a2aEndpointVisible(row)
		}
		if !visible || s.hiddenByDirectory(ref.Machine, row) {
			return false
		}
	}
	if row, ok, _ := s.archivedRow(ctx, ref.Machine, ref.Session); ok && row.Archived {
		return false
	}
	_, guest, err := s.guestOf(ctx, ref.Machine, ref.Session)
	return err == nil && !guest && !s.selfPolicy(ctx, ref.Session).HideFromExtensions
}

// a2aTaskEndpointAllowed permits an A2A-launched subagent to act as the target endpoint
// of its own launch task while keeping peer discovery closed to subagents.
func (s *Service) a2aTaskEndpointAllowed(ctx context.Context, cfg assistantwire.A2AConfig, ref a2aRef, t *a2aTask) bool {
	allowSubagent := t != nil && t.Launch != nil && t.target() == ref
	return s.a2aEndpointAllowed(ctx, cfg, ref, allowSubagent)
}

// a2aToolAllowed checks whether ref may invoke A2A tools (peers, plus A2A-launched
// subagents reporting or reading their own task).
func (s *Service) a2aToolAllowed(ctx context.Context, cfg assistantwire.A2AConfig, ref a2aRef) bool {
	return s.a2aEndpointAllowed(ctx, cfg, ref, s.isA2ASubagent(ctx, ref))
}

// a2aDiscoverable reports whether session may appear in peer discovery and accept
// new unsolicited A2A requests. Subagents are always excluded.
func a2aDiscoverable(session proto.Session) bool {
	return a2aEndpointVisible(session) && !session.IsSubagent
}

// a2aEndpointVisible checks session visibility without rejecting subagents, for
// task-bound delivery and observation of an A2A-launched subagent's own task.
func a2aEndpointVisible(session proto.Session) bool {
	return !session.Archived && !session.IsDeleted() && !session.HiddenByDirectory() &&
		runSpace(session) == "" // any space: a2aEndpointVisible has no registry, so it fails closed
}

func a2aVisible(session proto.Session) bool {
	return a2aDiscoverable(session)
}

func a2aSendable(session proto.Session) bool {
	return a2aDiscoverable(session) && a2aCanSendCapability(session)
}

func a2aEndpointSendable(session proto.Session) bool {
	return a2aEndpointVisible(session) && a2aCanSendCapability(session)
}

func a2aTaskSendable(session proto.Session, t *a2aTask, to a2aRef) bool {
	if t != nil && t.Launch != nil && t.target() == to {
		return a2aEndpointSendable(session)
	}
	return a2aSendable(session)
}

func a2aCanSendCapability(session proto.Session) bool {
	return session.Capabilities == nil && session.SourceKind == "" || session.Capabilities != nil && session.Capabilities.Send
}

func (s *Service) a2aSession(ctx context.Context, cfg assistantwire.A2AConfig, ref a2aRef) (proto.Session, error) {
	if !s.a2aAllowed(ctx, cfg, ref) {
		return proto.Session{}, errA2AUnavailable
	}
	for _, sess := range s.a2aSessionsOn(ctx, ref.Machine) {
		if sess.SessionID == ref.Session && a2aSendable(sess) {
			return sess, nil
		}
	}
	return proto.Session{}, errA2ANotFound
}

func (s *Service) a2aSessionsOn(ctx context.Context, machine string) []proto.Session {
	list, _ := s.sessions(ctx, machine)
	return list
}

func a2aValidID(id string) bool {
	return id != "" && len(id) <= 200 && strings.IndexFunc(id, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

func a2aValidText(text string) bool {
	return len(text) <= a2aMaxText && strings.TrimSpace(text) != "" && strings.IndexFunc(text, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t'
	}) < 0
}

func a2aCleanText(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	return clip(strings.TrimSpace(text), 2800)
}

func (s *Service) a2aSave(ctx context.Context, st a2aStore, t *a2aTask) error {
	raw, err := json.Marshal(t.a2aBody)
	if err != nil {
		return err
	}
	t.Body = raw
	var next *time.Time
	if t.State != a2aQueued && (!a2aTerminal(t.State) || slices.ContainsFunc(t.Events, a2aActiveDelivery)) {
		now := time.Now().Add(time.Second)
		next = &now
	}
	status := t.Created
	for i := len(t.Events) - 1; i >= 0; i-- {
		if t.Events[i].State != "" {
			status = t.Events[i].At
			break
		}
	}
	ok, err := st.A2AUpdate(ctx, t.A2ARow, status, next)
	if err != nil {
		return err
	}
	if !ok {
		return errA2AConflict
	}
	t.Revision++
	if a2aTerminal(t.State) {
		s.a2aSignal()
	}
	return nil
}

func a2aActiveDelivery(e a2aEvent) bool {
	return e.Delivery == a2aQueued || e.Delivery == a2aSending || e.Delivery == a2aUncertain
}

func (s *Service) a2aStart(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, to a2aRef, id, contextID, text string, wake bool, launch *a2aLaunch) (*a2aTask, error) {
	if !a2aValidID(id) || !a2aValidText(text) || contextID != "" && !a2aValidID(contextID) {
		return nil, errA2AInvalid
	}
	if launch != nil {
		if c.Ref.Session == "" || to.Session != "" || contextID != "" {
			return nil, errA2AInvalid
		}
		dev, ok := s.machineByName(ctx, to.Machine)
		if !ok || dev.ID != to.Machine || !s.a2aMachineAllowed(ctx, cfg, to.Machine) || !a2aValidText(launch.CWD) || s.trackPolicy(ctx, identity.SpaceAt(launch.CWD)).HideFromExtensions {
			return nil, errA2AUnavailable
		}
		launch.PermissionMode = strings.TrimSpace(launch.PermissionMode)
		launch.Subagent = true
		launch.CWD = proto.ExpandHome(launch.CWD, dev.Home)
		// Check for an idempotent retry BEFORE validating current launch toggle or caller permission,
		// so a caller recovering from a lost launch reply is never blocked by later policy/mode changes.
		if old, err := decodeA2A(st.A2ARequest(ctx, c.Owner, id)); old != nil || err != nil {
			return a2aCheckRetry(old, err, to, contextID, text, wake, launch)
		}
		if !cfg.LaunchAllowed() {
			return nil, fmt.Errorf("%w: starting new A2A subagent sessions is disabled by configuration", errA2AUnavailable)
		}
		if launch.PermissionMode != "" {
			mode, err := s.a2aLaunchPermissionMode(ctx, c.Ref, launch.PermissionMode)
			if err != nil {
				return nil, err
			}
			launch.PermissionMode = mode
		}
		to.Session = newStartSessionID()
		s.dispatch.admit.Lock()
		defer s.dispatch.admit.Unlock()
	}
	if to == c.Ref {
		return nil, errA2AInvalid
	}
	if !s.a2aEndpointAllowed(ctx, cfg, to, launch != nil) {
		return nil, errA2AUnavailable
	}
	if old, err := decodeA2A(st.A2ARequest(ctx, c.Owner, id)); old != nil || err != nil {
		return a2aCheckRetry(old, err, to, contextID, text, wake, launch)
	}
	if launch == nil {
		if c.Ref.Session != "" && !s.a2aAllowed(ctx, cfg, c.Ref) {
			return nil, errA2AUnavailable
		}
		if _, err := s.a2aSession(ctx, cfg, to); err != nil {
			return nil, err
		}
	} else if err := s.checkDispatchCapacity(ctx, c.Ref, time.Now()); err != nil {
		return nil, err
	}
	if contextID == "" {
		contextID = randomHex(16)
	} else {
		rows, err := st.A2AList(ctx, c.Owner, to.Machine, to.Session, contextID, "", "", false, nil, 1)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, errA2ANotFound
		}
	}
	t := a2aTask{A2ARow: db.A2ARow{ID: randomHex(16), Owner: c.Owner, Request: id, Context: contextID,
		TargetMachine: to.Machine, TargetSession: to.Session, SourceMachine: c.Ref.Machine, SourceSession: c.Ref.Session},
		a2aBody: a2aBody{Wake: wake, Launch: launch}}
	t.append(a2aEvent{ID: id, Kind: "message", Text: text, State: a2aSubmitted, Delivery: a2aQueued})
	t.Body, _ = json.Marshal(t.a2aBody)
	created, err := decodeA2A(st.A2ACreate(ctx, t.A2ARow))
	out, err := a2aCheckRetry(created, err, to, contextID, text, wake, launch)
	if err == nil && out != nil && out.Launch != nil {
		s.recordA2ASubagent(out)
	}
	return out, err
}

// a2aPermissionRank assigns an explicit privilege rank to every supported permission mode.
// Unknown or unmapped modes return (_, false) and fail closed — never silently treated as plan.
func a2aPermissionRank(mode string) (int, bool) {
	switch mode {
	case "plan":
		return 0, true
	case "default":
		return 1, true
	case "acceptEdits":
		return 2, true
	case "auto":
		return 3, true
	case "bypassPermissions":
		return 4, true
	default:
		return -1, false
	}
}

// a2aLaunchPermissionMode validates an explicit permission_mode on an A2A launch.
// "plan" is always permitted. Any higher mode requires live node-attested SourcePermission
// evidence bound to the calling session (never a cached session-list snapshot, #9671) and
// fails closed when missing or insufficient:
//   - prompting attestation permits at most "default" (interactive approval on every write/command);
//   - bypass attestation permits any allowlisted mode up to "bypassPermissions".
func (s *Service) a2aLaunchPermissionMode(ctx context.Context, caller a2aRef, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	reqRank, ok := a2aPermissionRank(requested)
	if !ok || !slices.Contains(proto.PermissionModes, requested) {
		return "", fmt.Errorf("%w: unsupported permission_mode %q", errA2AInvalid, requested)
	}
	if reqRank == 0 {
		return "plan", nil
	}
	proof := devicewire.SourcePermissionFrom(ctx)
	if !proof.Valid() || proof.Session != caller.Session {
		return "", fmt.Errorf("%w: permission_mode %q requires live source-node permission attestation; omit permission_mode or use \"plan\"", errA2AInvalid, requested)
	}
	maxRank := -1
	switch proof.Class {
	case "prompting":
		maxRank = 1 // "default"
	case "bypass":
		maxRank = 4 // "bypassPermissions"
	}
	if reqRank > maxRank {
		return "", fmt.Errorf("%w: requested permission_mode %q exceeds live source permission class %q", errA2AInvalid, requested, proof.Class)
	}
	return requested, nil
}

func a2aCheckRetry(t *a2aTask, err error, to a2aRef, contextID, text string, wake bool, launch *a2aLaunch) (*a2aTask, error) {
	if err != nil {
		return nil, err
	}
	if t == nil || len(t.Events) == 0 {
		return nil, errA2ANotFound
	}
	targetMatches := t.target() == to && t.Launch == nil && launch == nil
	textMatches := t.Events[0].Text == text
	if t.Launch != nil && launch != nil {
		targetMatches = t.TargetMachine == to.Machine && t.Launch.CWD == launch.CWD && t.Launch.Launcher == launch.Launcher &&
			t.Launch.PermissionMode == launch.PermissionMode && t.Launch.Dispatch == launch.Dispatch && t.Launch.Branch == launch.Branch
		targetMatches = targetMatches && t.Launch.Tier == launch.Tier // the tier as named, not what it resolved to
		if t.Launch.Dispatch {
			textMatches = t.Launch.PromptHash == launch.PromptHash
		}
	}
	if !targetMatches || contextID != "" && t.Context != contextID || !textMatches || t.Wake != wake {
		return nil, errA2ADuplicate
	}
	return t, nil
}

func (s *Service) a2aGet(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, id string) (*a2aTask, error) {
	t, err := decodeA2A(st.A2AGet(ctx, id))
	if err != nil {
		return nil, err
	}
	if t == nil || !t.participant(c) || !s.a2aTaskVisible(ctx, cfg, c, t) {
		return nil, errA2ANotFound
	}
	return t, nil
}

func (s *Service) a2aTaskVisible(ctx context.Context, cfg assistantwire.A2AConfig, c a2aCaller, t *a2aTask) bool {
	if t.SourceSession != "" && !s.a2aTaskEndpointAllowed(ctx, cfg, t.source(), t) {
		return false
	}
	return s.a2aTaskEndpointAllowed(ctx, cfg, t.target(), t) ||
		t.Owner == c.Owner && s.a2aMachineAllowed(ctx, cfg, t.TargetMachine) &&
			(t.TargetClosed || a2aTerminal(t.State) && s.a2aEndpointClosed(ctx, cfg, t.target()))
}

func (s *Service) a2aMutate(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, c a2aCaller, id string, change func(*a2aTask) (bool, error)) (*a2aTask, error) {
	for range 5 {
		t, err := s.a2aGet(ctx, st, cfg, c, id)
		if err != nil {
			return nil, err
		}
		changed, err := change(t)
		if err != nil || !changed {
			return t, err
		}
		if err = s.a2aSave(ctx, st, t); !errors.Is(err, errA2AConflict) {
			return t, err
		}
	}
	return nil, errA2AConflict
}

func a2aAppendOnce(t *a2aTask, ev a2aEvent) (bool, error) {
	for _, old := range t.Events {
		if old.ID == ev.ID {
			if old.Kind != ev.Kind || old.Text != ev.Text || old.State != ev.State {
				return false, errA2ADuplicate
			}
			return false, nil
		}
	}
	limit := a2aMaxEvents - 2
	if a2aTerminal(ev.State) {
		limit = a2aMaxEvents
	}
	if a2aTerminal(t.State) || len(t.Events) >= limit {
		return false, fmt.Errorf("%w: task is closed or its history is full", errA2AInvalid)
	}
	t.append(ev)
	return true, nil
}

func a2aReturn(t *a2aTask, ev a2aEvent) a2aEvent {
	if t.SourceSession != "" && t.Wake && ev.Kind == "status" {
		ev.Delivery = a2aQueued
	}
	return ev
}

func a2aContinue(t *a2aTask, id, text string) (bool, error) {
	if !a2aValidID(id) || !a2aValidText(text) {
		return false, errA2AInvalid
	}
	ev := a2aEvent{ID: id, Kind: "message", Text: text, State: a2aSubmitted, Delivery: a2aQueued}
	if slices.ContainsFunc(t.Events, func(e a2aEvent) bool { return e.ID == id }) {
		return a2aAppendOnce(t, ev)
	}
	if t.State != a2aInput {
		return false, fmt.Errorf("%w: task is not waiting for an answer", errA2AInvalid)
	}
	changed, err := a2aAppendOnce(t, ev)
	if changed {
		for i := len(t.Events) - 2; i >= 0; i-- {
			if t.Events[i].State == a2aInput {
				if t.Events[i].Delivery == a2aQueued {
					t.Events[i].Delivery = a2aDiscarded
				}
				break
			}
		}
	}
	return changed, err
}

func a2aCancel(t *a2aTask) (bool, error) {
	if t.State == a2aCanceled {
		return false, nil
	}
	if t.State == a2aInput {
		t.append(a2aEvent{ID: randomHex(16), Kind: "status", State: a2aCanceled})
		return true, nil
	}
	if t.State != a2aSubmitted && t.State != a2aQueued {
		return false, errA2ACancel
	}
	for i := len(t.Events) - 1; i >= 0; i-- {
		e := &t.Events[i]
		if e.Kind != "message" {
			continue
		}
		if e.Delivery != a2aQueued {
			break
		}
		e.Delivery = a2aDiscarded
		t.append(a2aEvent{ID: randomHex(16), Kind: "status", State: a2aCanceled})
		return true, nil
	}
	return false, errA2ACancel
}
