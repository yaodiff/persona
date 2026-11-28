package assistant

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount/fleetcountadapt"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/modelretry"
	"github.com/nwverse/persona/server/internal/proto"
)

// What comes from the nodes is the manager's (docs/server/machines.md "Who handles computer events"). The hub did all of this until the
// manager connected the computers itself; it moved here as it was (fleet_*.go):
//
//   - the interactions (approval / question / plan / screen / Codex cards): made from the nodes' interaction.create
//     and what their sessions show, kept, waited for, answered, expired (fleet_ix*.go);
//   - each Agent's state as the apps get it — the unified state, "Needs you", "Unread" / "Seen", the work and todos it shows —
//     and the session lists (this file, fleet_sessions.go, fleet_seen.go);
//   - the pushes and the Live Activity's content: when a finished turn is worth a push, a card's push, what the
//     activity shows (fleet_push.go, fleet_live.go);
//   - the nodes' MCP tools and assistant.lark (fleet_tools.go).
//
// The hub is told what the apps need (assistantwire fleet.*: the lists, the pending interactions, the fleet, the
// pushes to deliver) and keeps the last of them while the manager is away; it relays the apps' answers here
// (fleetRoutes) and delivers the pushes (APNs, the relay, its apps' notify events).

// interactionAdmission serializes first-create history checks and registration
// for one interaction ID without keeping fleet.mu held across database reads.
// refs includes both the holder and waiters, so an idle gate can be removed.
type interactionAdmission struct {
	mu   sync.Mutex
	refs int
}

// fleetBook is what the manager keeps of it.
type fleetBook struct {
	mu           sync.Mutex
	interactions map[string]*pending
	admissions   map[string]*interactionAdmission
	seen         map[[2]string]time.Time      // (machine, session) -> when the user last looked at it
	seenLoaded   bool                         // seen holds the table's times (loadSeen succeeded once)
	stateSince   map[[2]string]stateMark      // (machine, session) -> its state and when it entered it
	stateListed  map[string]bool              // scratch ids for pruning stateSince, cleared and reused under mu
	upstream     map[[2]string]upstreamStreak // (machine, session) -> consecutive no-work upstream 5xx turns (fleet_upstream.go)
	notified     map[string]time.Time         // assistant conversation -> when it last pushed with notify_user
	// reportSent: assistant conversation -> when a scheduled run or a task notice was sent into it (noteReportTurn):
	// the turn it starts is pushed however short it is.
	reportSent map[string]time.Time
	// cardTell serializes telling the hub a card with checking it is still held (fleet_ix.go tellCard).
	cardTell sync.Mutex
	// A card can be withdrawn while it is being told. Reconnect takes both locks to order its
	// complete inventory with new cards and withdrawals on the same connection.
	cardEnd sync.Mutex
	// codexAnnounced: Codex card id -> when it was announced (fleet_ix_codex.go)
	codexAnnounced map[string]time.Time
	// codexEnded: Codex card id -> when the node stopped listing it (fleet_ix_codex.go)
	codexEnded map[string]time.Time
	// screenAnswered: screen card id -> when it was answered (fleet_ix_screen.go screenJustAnsweredLocked)
	screenAnswered map[string]answeredScreen
	// screenDetached: screen card id -> the card its node's connection ended while it was open (fleet_ix_screen.go
	// screenBackLocked): read again off the same dialog once the node is back, it is that card, not a new question.
	screenDetached map[string]screenGone
	// screenHandedBack: screen card id -> the card handed back to the computer (fleet_ix_screen.go screenHandedBackLocked):
	// the same dialog still on screen gets no new card until the agent moves on (#4867).
	screenHandedBack map[string]screenGone
	// handoffBy: hand-off card id -> the approval that claimed it, with the device the hub vouched for (secret_handoff.go:
	// only such an approval counts).
	handoffBy map[string]handoffClaim
	// upgradeEvery / upgradeFor override screenUpgradeEvery / screenUpgradeFor (0: those; upgradeScreenCard). Tests
	// shorten them on their own Service before any session list comes, so an upgrade loop another test left running
	// never reads what this one writes (#4554).
	upgradeEvery, upgradeFor time.Duration
	upgrades                 sync.WaitGroup // active screen-upgrade loops; database-backed fixtures wait during teardown
	completionRuns           sync.WaitGroup // delayed turn completions already running when a fixture tears down
	resolvedRuns             sync.WaitGroup // answered cards' hooks still running (interactionResolvedSoon)
	completionTimers         map[*time.Timer]struct{}
	completionStopped        bool // test teardown only: no new delayed completion after the drain starts
	// hooked: Persona's hooks run in the session (a hook event came since the manager started).
	hooked map[[2]string]bool
	// stopSig: the signature of the reply each session's latest Stop ended its turn with; pushedSig: the one a
	// "finished" push was already sent for, cleared when the next prompt comes (stopDuplicate).
	stopSig, pushedSig map[[2]string]string
	turns              *turnTracker // "machine/session" -> the turn in progress, for the finished push
	clients            assistantwire.Clients
	prev               map[string]map[string]turnPrev // each machine's list setSessions saw last, by session id
	// prevConn: the node connection (devices' ConnectedAt) each machine's prev list came on.
	// heldRows: the last listed row of each Agent with a held message, by machine and session. These display-only
	// copies recreate a missing row; the devices' inventory stays authoritative for delivery.
	heldRows map[string]map[string]proto.Session
	prevConn map[string]time.Time

	// sent: what is kept of each machine's last session list told the hub (fleet.sessions): its length and digest, not
	// its bytes. sentMu is held while a list is decorated, encoded into enc and told (publishList), so the lists reach
	// the hub in the order they were made; enc is the one buffer every list is encoded into, reused.
	sentMu sync.Mutex
	sent   map[string]sentList
	enc    bytes.Buffer
	// archived is what was told of each machine's archived rows (fleet.archived, tellArchived), under sentMu.
	archived map[string]archivedSent
	// views and archViews: the arrays the views of each list and of its archived rows told apart are decided in
	// (fleet_views.go), under sentMu.
	views, archViews fleetcountadapt.Views
	// shown: the rows awaiting a reply of the list last told the hub for each machine, decorated (the reminders of
	// agents awaiting a reply read them instead of decorating every list each minute, agents_reply.go).
	shownMu sync.Mutex
	shown   map[string][]proto.Session
	// finishedDue: when the first Just finished row of each machine's list, as last decorated to be told, runs out
	// of its window (#9751), under shownMu. Nothing else tells a list again when only time passes (republishFinished).
	finishedDue map[string]time.Time
	// asks: what each session's last words ask, while they stay the same (fleet_asks.go).
	asks asksMemo
	// folds: what each machine's last complete list folded its children under (fleet_fold.go), for the rows and
	// cards sent without the rest of the list. foldMeta / crossParents / crossChildren track only machines
	// participating in cross-machine subagent hierarchies so unrelated machines stay on the local fast path.
	foldMu        sync.Mutex
	folds         map[string]sessionFold
	foldMeta      map[string][]foldEntry
	crossParents  map[string]map[string]bool // childMachine -> referenced parentMachines
	crossChildren map[string]map[string]bool // parentMachine -> referencing childMachines

	// subagents: durable child (machine, session) -> parent/task provenance restored on demand by visible
	// target identity from a2a_tasks, legacy dispatches, and task_spawn todo agents (subagent_index.go).
	subagentsMu           sync.RWMutex
	subagentsLegacyLoaded bool
	subagents             map[[2]string]subagentLink
	subagentsChecked      map[string]map[string]struct{} // machine -> checked sessionIDs
	subagentsByTask       map[string][2]string

	// rep is the last snapshot's parts the session lists show (fleetTakeSnapshot).
	rep fleetReport

	mutesMu sync.Mutex
	mutes   *Pins
	// mutes are the hub's, as fleet.mutes handed them over last (nil: not yet).

	askMu sync.Mutex
	asked map[string]*askedOne // the assistant's own questions and cards on the phone (by interaction id)

	// reanswered: the "Change answer" messages sent to agents, by interaction and answers (fleet_delegate.go): each goes once.
	reanswered map[string]bool

	undelivered heldActs // answers the manager's own Action did not take yet

	live liveState // the Live Activity's content as last told (fleet_live.go)

	started sync.Once
	// The first reconnect and the first arriving card both wait for prior-process cleanup.
	restored sync.Once

	q        fleetQueue // what the nodes' events start for the assistant, in order (fleet_follow.go)
	followMu sync.Mutex
	follows  map[[2]string]bool // the conversations the channels follow (fleet_follow.go)
	// unsettled: follows the devices did not take (fleet_follow.go): the next follow asks them again
	unsettled map[[2]string]bool
}

// fleetReport is what the session lists are decorated with from the assistant's snapshot.
type fleetReport struct {
	mu        sync.RWMutex
	got       bool
	seq       uint64                 // highest sequenced Snapshot.Seq applied (0: none yet)
	sessions  map[string]SessionView // by session id
	work      map[[2]string]proto.WorkState
	workOn    bool
	links     map[[2]string]TodoLink
	completed map[[2]string]TodoLink
	quiet     map[[2]string]bool
	marks     map[[2]string][]string  // conversation → the channels its messages came from, newest first
	turns     map[[2]string]time.Time // conversation → when a channel's turn in it was answered (zero: followed now)
	inbox     map[string]bool         // the inbox agents' conversations
	tasks     []TaskView              // the scheduled tasks (nil: not read yet)
	profile   *Profile                // its settings as the hub's features need them (nil: not read yet)
}

// fleetStore is what of the store the fleet keeps (db.DB); a store without it keeps nothing.
type fleetStore interface {
	InsertInteraction(ctx context.Context, ix proto.Interaction) error
	FinishInteraction(ctx context.Context, id, status string, res *proto.Resolution) error
	FinishInteractionOccurrence(ctx context.Context, ix proto.Interaction, status string, res *proto.Resolution) (matched, currentTerminal bool, err error)
	InteractionOutcome(ctx context.Context, id string) (status string, res *proto.Resolution, err error)
	Interactions(ctx context.Context, machine, session string, limit int) ([]db.InteractionRecord, error)
	AllowAlways(ctx context.Context, session, tool string, keys []string) error
	AlwaysAllowed(ctx context.Context, session string, keys []string) (bool, error)
	MarkSeen(ctx context.Context, machine, session string, at time.Time) error
	SeenTimes(ctx context.Context) (map[[2]string]time.Time, error)
}

func (s *Service) fleetStore() fleetStore {
	st, ok := s.store().(fleetStore)
	if !ok {
		return nil
	}
	return guardedStore{st}
}

// guardedStore is a store whose fleet methods may not be there after all (a test's store that embeds the interface
// and keeps only some tables): one that is not answers an error, as a database that failed.
type guardedStore struct{ st fleetStore }

var errNoTable = errors.New("the store does not keep this")

func guard(err *error) {
	if recover() != nil {
		*err = errNoTable
	}
}

func (g guardedStore) InsertInteraction(ctx context.Context, ix proto.Interaction) (err error) {
	defer guard(&err)
	return g.st.InsertInteraction(ctx, ix)
}

// interactionDeadlineUpdater is the durable counterpart of publishing an extended prepared-approval deadline.
// A store that keeps interaction history but cannot update it must not leave the database advertising a stale expiry.
type interactionDeadlineUpdater interface {
	UpdateInteractionDeadline(ctx context.Context, id string, created, expires time.Time) error
}

func (g guardedStore) UpdateInteractionDeadline(ctx context.Context, id string, created, expires time.Time) (err error) {
	defer guard(&err)
	st, ok := g.st.(interactionDeadlineUpdater)
	if !ok {
		return errNoTable
	}
	return st.UpdateInteractionDeadline(ctx, id, created, expires)
}

func (s *Service) updateInteractionDeadline(ctx context.Context, id string, created, expires time.Time) error {
	st := s.fleetStore()
	if st == nil {
		return nil // no interaction table: the in-memory card is the only card
	}
	return st.(interactionDeadlineUpdater).UpdateInteractionDeadline(ctx, id, created, expires)
}

// resolvedInteractionInserter is the atomic history write and final outcome update used by reversible-setting
// auto-approval. A store without both fails closed: the setting is not changed without its queryable decision record.
type resolvedInteractionInserter interface {
	InsertResolvedInteraction(ctx context.Context, ix proto.Interaction, res *proto.Resolution) error
	MarkResolvedInteractionOutcome(ctx context.Context, id string, created time.Time, outcome string, at time.Time) error
}

func (g guardedStore) InsertResolvedInteraction(ctx context.Context, ix proto.Interaction, res *proto.Resolution) (err error) {
	defer guard(&err)
	st, ok := g.st.(resolvedInteractionInserter)
	if !ok {
		return errNoTable
	}
	return st.InsertResolvedInteraction(ctx, ix, res)
}

func (g guardedStore) MarkResolvedInteractionOutcome(ctx context.Context, id string, created time.Time, outcome string, at time.Time) (err error) {
	defer guard(&err)
	st, ok := g.st.(resolvedInteractionInserter)
	if !ok {
		return errNoTable
	}
	return st.MarkResolvedInteractionOutcome(ctx, id, created, outcome, at)
}

func (g guardedStore) FinishInteraction(ctx context.Context, id, status string, res *proto.Resolution) (err error) {
	defer guard(&err)
	return g.st.FinishInteraction(ctx, id, status, res)
}

func (g guardedStore) FinishInteractionOccurrence(ctx context.Context, ix proto.Interaction, status string, res *proto.Resolution) (matched, currentTerminal bool, err error) {
	defer guard(&err)
	return g.st.FinishInteractionOccurrence(ctx, ix, status, res)
}

func (g guardedStore) InteractionOutcome(ctx context.Context, id string) (status string, res *proto.Resolution, err error) {
	defer guard(&err)
	return g.st.InteractionOutcome(ctx, id)
}

func (g guardedStore) Interactions(ctx context.Context, machine, session string, limit int) (out []db.InteractionRecord, err error) {
	defer guard(&err)
	return g.st.Interactions(ctx, machine, session, limit)
}

// lateAnswerer is a store that records an answer given after a card expired (db.DB); one without it records none.
type lateAnswerer interface {
	LateAnswer(ctx context.Context, id string, answers map[string]string, by, reason string) (bool, error)
}

func (g guardedStore) LateAnswer(ctx context.Context, id string, answers map[string]string, by, reason string) (ok bool, err error) {
	defer guard(&err)
	l, has := g.st.(lateAnswerer)
	if !has {
		return false, errNoTable
	}
	return l.LateAnswer(ctx, id, answers, by, reason)
}

// interactionPager is a store that pages the history by time (db.DB); one without it answers errNoTable.
type interactionPager interface {
	InteractionsPage(ctx context.Context, q db.InteractionQuery) (db.InteractionPage, error)
}

func (g guardedStore) InteractionsPage(ctx context.Context, q db.InteractionQuery) (out db.InteractionPage, err error) {
	defer guard(&err)
	p, ok := g.st.(interactionPager)
	if !ok {
		return out, errNoTable
	}
	return p.InteractionsPage(ctx, q)
}

func (g guardedStore) AllowAlways(ctx context.Context, session, tool string, keys []string) (err error) {
	defer guard(&err)
	return g.st.AllowAlways(ctx, session, tool, keys)
}

func (g guardedStore) AlwaysAllowed(ctx context.Context, session string, keys []string) (ok bool, err error) {
	defer guard(&err)
	return g.st.AlwaysAllowed(ctx, session, keys)
}

func (g guardedStore) MarkSeen(ctx context.Context, machine, session string, at time.Time) (err error) {
	defer guard(&err)
	return g.st.MarkSeen(ctx, machine, session, at)
}

func (g guardedStore) SeenTimes(ctx context.Context) (out map[[2]string]time.Time, err error) {
	defer guard(&err)
	return g.st.SeenTimes(ctx)
}

// fleetOn: the manager carries the machines (its process has the devices; unit tests without them have no fleet).
func (s *Service) fleetOn() bool { return s.devs != nil }

// fleetStart starts the fleet's loops, once. The seen times are read again on each call until they could be: a
// database that failed while the manager started does not leave every session the user had looked at unread.
// restoreInteractions stays once: expiring the stale rows later would cancel the ones created since.
//
// ctx is the manager run's: the loops end with it. They used to run on with its cancellation taken off, so a run that
// ended (a revoked token, a restart in place for a new binary) left them reading its closed store every 15 s and
// telling the hub an old fleet (#16215). The one-time reads still finish when the run ends meanwhile.
func (s *Service) fleetStart(ctx context.Context) {
	if !s.fleetOn() {
		return
	}
	once := context.WithoutCancel(ctx)
	s.fleet.started.Do(func() {
		s.restoreInteractions(once)
		go s.expireLoop(ctx)
		go s.liveLoop(ctx)
	})
	s.loadSeen(once)
}

// ---- the states (docs/protocol/agent-states.md) ----

// agentState maps a host status plus Persona's own knowledge onto the unified states:
//
//	herdr blocked, or a pending question / plan / permission  → needs_you (red)
//	herdr working                                              → working
//	herdr done (finished, unseen in herdr) and not seen in the app since it finished → unread (green)
//	herdr idle (seen in herdr), or done but seen in the app    → idle (gray)
func agentState(sess proto.Session, seenAt time.Time) string {
	if sess.PendingInteractions > 0 || sess.Status == proto.StatusBlocked {
		return proto.StateNeedsYou
	}
	switch sess.Status {
	case proto.StatusWorking:
		return proto.StateWorking
	case proto.StatusDone:
		// The transcript stops changing when the turn ends, so updated_at is when it finished.
		if !seenAt.IsZero() && !seenAt.Before(sess.UpdatedAt.Add(-2*time.Second)) {
			return proto.StateIdle
		}
		return proto.StateUnread
	case proto.StatusIdle:
		return proto.StateIdle
	case proto.StatusOffline:
		return proto.StateOffline
	}
	return proto.StateUnknown
}

// backgroundState is an agent's state once its background work counts (#2228): a Claude whose turn ended while its
// background commands, subagents, Monitors or workflows still run is working, not "Unread" or "Seen" — they wake it with their results, and it
// goes on. Needs-you stays first; a stopped one (offline) has nothing running. Not the assistant's conversations:
// their Monitors and background tasks stay on, and a conversation always "Working" would skip every scheduled run.
// What waits for a turn to end (the finished push, scheduled runs, the hub's own update) reads the host's status.
// Background commands the node found stalled, with nothing else running, do not make it working (#16418): they keep
// their rows and background_stalled_since for a look, and the finished turn settles.
func backgroundState(state string, sess proto.Session) string {
	if sess.Assistant || !backgroundRunning(sess) {
		return state
	}
	switch state {
	case proto.StateUnread, proto.StateIdle, proto.StateUnknown:
		if backgroundStalledShellOnly(sess) {
			return state
		}
		return proto.StateWorking
	}
	return state
}

// stalledAfterTurn: an agent's finished turn that backgroundState left as it was for its stalled background commands
// alone (backgroundStalledShellOnly): its row keeps background_stalled_since though it is not background_working.
func stalledAfterTurn(sess proto.Session) bool {
	switch sess.State {
	case proto.StateUnread, proto.StateIdle, proto.StateUnknown:
		return !sess.Assistant && sess.Status != proto.StatusWorking && backgroundStalledShellOnly(sess)
	}
	return false
}

// backgroundStalledShellOnly (#16418): all the background work left is background commands, and the node found them
// stalled (Session.BackgroundStalledSince: no CPU and no output past its grace, or their shells gone). No subagent,
// workflow or Monitor runs: a Monitor wakes the agent by itself, and those are work whatever its shells do.
func backgroundStalledShellOnly(sess proto.Session) bool {
	if sess.BackgroundStalledSince == 0 || len(sess.Background) == 0 || backgroundIsWork(sess) {
		return false
	}
	return !slices.ContainsFunc(sess.Background, func(t proto.BackgroundTask) bool { return t.Kind != "shell" })
}

// stateWithBackground is backgroundState, and whether the background work is all that makes it working
// (Session.BackgroundWorking: "Working (background)").
func stateWithBackground(host string, sess proto.Session) (string, bool) {
	state := backgroundState(host, sess)
	return state, state == proto.StateWorking && host != proto.StateWorking
}

// turnError is the model API error that cut s's latest turn off, while it has stopped (unread or idle): not finished
// (#1516), and its kind when the agent's record gives one. The node's word comes first: the agent's own record said
// the turn failed (Codex, #2185). Otherwise its last words when they are the error, as Claude Code writes it; only an
// answer that starts with the error counts: a reply that talks about one ("fixed the crash on API Error: …") is a reply.
func turnError(sess proto.Session) (text, kind string) {
	if sess.State != proto.StateUnread && sess.State != proto.StateIdle {
		return "", ""
	}
	return latestTurnError(sess)
}

// latestTurnError is turnError whatever sess's state now: the node's word, else its last words when they are the error.
func latestTurnError(sess proto.Session) (text, kind string) {
	if text := strings.TrimSpace(sess.TurnError); text != "" {
		return text, sess.TurnErrorKind
	}
	if text := strings.TrimSpace(sess.LastText); assistantwire.CutOff(text) {
		return text, ""
	}
	return "", ""
}

// rateLimited: a turn error that says the requests came too fast or used up a limit.
var rateLimited = lazyre.New(`(?i)\b429\b|rate[ _-]?limit|too many requests|usage limit`)

// turnErrorCode is what a turn's model API error says is wrong, for a card that says what to fix (Session.TurnErrorCode,
// docs/assistant/onboarding.md 5.2); "" when it does not say.
func turnErrorCode(text string) string {
	if code := proto.UnrecoverableTurnErrorCode(text); code != "" {
		return code
	}
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(strings.ToLower(t), "not logged in"):
		return proto.TurnErrorNotLoggedIn
	case strings.Contains(t, "There's an issue with the selected model"):
		return proto.TurnErrorModelRoute
	case assistantwire.ToolSearchRefused(t):
		// Before the classes below: a gateway's words for it may say "not supported" next to "model". The node resent
		// the turn once with every tool loaded and starts the conversation that way from now on (#10365).
		return proto.TurnErrorToolSearchUnsupported
	case assistantwire.ImageLimitErrorText(t):
		// Accumulated pictures pushed the request past the model API's size or multi-image limit (#13045).
		return proto.TurnErrorImagesTooLarge
	case modelretry.AccountExhausted(t):
		// Explicit upstream account-pool exhaustion (#13372): ahead of model_route in case the message also names the model.
		return proto.TurnErrorUpstreamUnavailable
	}
	switch classifyLauncherError(t) { // the launcher health checks' classes (launcher_health.go)
	case errKindUsageLimit:
		return proto.TurnErrorRateLimited
	case errKindUpstream:
		return proto.TurnErrorUpstreamUnavailable
	case errKindAuth:
		return proto.TurnErrorAuthFailed
	case errKindModelRoute:
		return proto.TurnErrorModelRoute
	}
	if rateLimited.MatchString(t) {
		return proto.TurnErrorRateLimited
	}
	return ""
}

var fallbackModelMissing = lazyre.New(`(?i)\b404\b|\bmodel_not_found\b`)

// sessionTurnErrorCode uses the active refusal fallback only for a model-route failure. A notice in another
// session, or a fallback the runtime has since cleared, cannot change the meaning of an ordinary model error.
func sessionTurnErrorCode(c proto.Session) string {
	if c.TurnError == "" {
		return ""
	}
	code := turnErrorCode(c.TurnError)
	if proto.UnrecoverableTurnErrorCode(c.TurnError) != "" {
		return code
	}
	if c.TurnErrorKind == proto.TurnErrorUsageLimit {
		return proto.TurnErrorRateLimited
	}
	if f := c.ModelFallback; f != nil && f.Subtype == "model_refusal_fallback" && f.FallbackModel != "" {
		if c.TurnErrorCode == proto.TurnErrorModelFallbackUnavailable ||
			c.TurnErrorKind != proto.TurnErrorTransient && fallbackModelMissing.MatchString(c.TurnError) &&
				(code == proto.TurnErrorModelRoute || strings.Contains(strings.ToLower(c.TurnError), "model_not_found")) {
			return proto.TurnErrorModelFallbackUnavailable
		}
	}
	// Preserve a node's transcript-streak upstream_unavailable code (#13372), including on a 503 whose words also
	// matched model_route, while keeping auth, sign-in, 429 and tool-search codes ahead of it.
	if c.TurnErrorCode == proto.TurnErrorUpstreamUnavailable &&
		(code == "" || code == proto.TurnErrorUpstreamUnavailable ||
			(code == proto.TurnErrorModelRoute && modelretry.UpstreamServerError(c.TurnError))) {
		return proto.TurnErrorUpstreamUnavailable
	}
	return code
}

// signedOut: conversation c's last turn ended "Not logged in". The node says so only in the turn's words: TurnError while
// the conversation's process runs, and only its last words (LastText, Claude Code's own reply) once it is listed from
// its transcript with no process, as a task's conversation between runs is (scanOffline). The lists work the error and
// its code out of those (latestTurnError, sessionTurnErrorCode); session.get answers the node's session as it is.
func signedOut(c proto.Session) bool {
	text, _ := latestTurnError(c)
	return c.TurnErrorCode == proto.TurnErrorNotLoggedIn || turnErrorCode(text) == proto.TurnErrorNotLoggedIn
}

// userTurnError: a turn error with code is the user's to fix; a rate limit passes (or is the user's when the
// agent's record says so).
func userTurnError(code string) bool {
	switch code {
	case proto.TurnErrorNotLoggedIn, proto.TurnErrorAuthFailed, proto.TurnErrorModelRoute, proto.TurnErrorModelFallbackUnavailable, proto.TurnErrorImagesTooLarge:
		return true
	}
	return false
}

type stateMark struct {
	state string
	at    time.Time
}

// fillStateMarks fills in StateSince: the time an agent's state last changed, kept until it changes again. A complete
// list also drops the marks and hook flags of the machine's sessions no longer listed.
func (s *Service) fillStateMarks(machine string, list []proto.Session, now time.Time, complete bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.stateSince == nil {
		s.fleet.stateSince = map[[2]string]stateMark{}
	}
	for i := range list {
		k := [2]string{machine, list[i].SessionID}
		mk, ok := s.fleet.stateSince[k]
		if !ok || mk.state != list[i].State {
			mk = stateMark{state: list[i].State, at: now}
			s.fleet.stateSince[k] = mk
		}
		list[i].StateSince = mk.at
	}
	if !complete {
		return
	}
	// The list is the node's whole list, kept across a disconnect (devices.Machine.setSessions): a session no longer
	// in it has ended. Forget its mark and its hook flag. A hook flag for a session never listed yet (its hook came
	// before the node's next list) has no mark and stays.
	listed := s.fleet.stateListed
	if listed == nil {
		listed = make(map[string]bool, len(list))
		s.fleet.stateListed = listed
	} else {
		clear(listed)
	}
	for _, sess := range list {
		listed[sess.SessionID] = true
	}
	for k := range s.fleet.stateSince {
		if k[0] == machine && !listed[k[1]] {
			delete(s.fleet.stateSince, k)
			delete(s.fleet.hooked, k)
		}
	}
}

// backgroundWaiting (Session.BackgroundWaiting, #6045): working only because of background work, and that work only
// waits: the node found its background commands waiting, and no subagent or workflow runs (those are work).
func backgroundWaiting(sess proto.Session) bool {
	return sess.BackgroundWorking && sess.BackgroundWaiting && !backgroundIsWork(sess)
}

// backgroundIsWork: a subagent or workflow runs, which is work whatever its background commands do.
func backgroundIsWork(sess proto.Session) bool {
	for _, a := range sess.Subagents {
		if a.Running {
			return true
		}
	}
	for _, w := range sess.Workflows {
		if w.Running {
			return true
		}
	}
	return slices.ContainsFunc(sess.Background, func(t proto.BackgroundTask) bool { return t.Kind == "agent" || t.Kind == "workflow" })
}

func backgroundRunning(sess proto.Session) bool {
	if len(sess.Background) > 0 {
		return true
	}
	for _, a := range sess.Subagents {
		if a.Running {
			return true
		}
	}
	for _, w := range sess.Workflows { // a running one is in Background too; not from every source
		if w.Running {
			return true
		}
	}
	return false
}
