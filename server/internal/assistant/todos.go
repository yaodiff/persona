package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/agentprompt"
	"github.com/nwverse/persona/server/internal/compat"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The todos (docs/assistant/todos.md): something to do, handed to one or more agents; a task the assistant hands out is a todo
// whose origin is the conversation (tasks.go). They are the assistant's: it keeps them (the hub's Postgres tables
// todos, todo_spaces, todo_agents, todo_decisions, as they were) and answers the apps' /api/v1/todos… routes, which the
// hub relays. The hub shows what the assistant reports of them (TodoReport, in the snapshot: the todo each agent works
// for, the quiet agents). What follows from the machines' session lists — agents joining the todo of their workspace or
// of the agent that spawned them, the herdr marks, the agents' titles — is todo_sessions.go's, on sessions.changed.

type TodoSpace struct {
	MachineID string    `json:"machine_id"`
	Space     string    `json:"space"`
	LinkedAt  time.Time `json:"linked_at"`
}

type TodoAgent struct {
	MachineID   string     `json:"machine_id"`
	SessionID   string     `json:"session_id"`
	LinkedAt    time.Time  `json:"linked_at"`
	ReleasedAt  *time.Time `json:"released_at,omitempty"`
	Title       string     `json:"title"`
	Cwd         string     `json:"cwd"`
	Agent       string     `json:"agent"`
	StartError  string     `json:"start_error,omitempty"`
	Source      string     `json:"source"`
	Via         string     `json:"via,omitempty"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`

	// A resume check begun before this binding was observed running, resumable or closed cannot supply a terminal.
	resumeInvalidatedAt time.Time
}

type Todo struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	DoneAt    *time.Time `json:"done_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Origin: the assistant conversation that opened it as a task; PlanID: the plan it came from (#930,
	// docs/assistant/unified-entry.md). Empty for a todo made by hand.
	Origin string `json:"origin,omitempty"`
	PlanID string `json:"plan_id,omitempty"`
	// StartError: a task whose agent did not start ends done with why (#1077); reopening clears it.
	StartError string `json:"start_error,omitempty"`
	// DoneBy is "auto" for the hub's completion judgment, "review" for supervisor acceptance, or "cancelled" for
	// cancellation, with DoneReason; ReopenedAt: when it was last reopened after it was done.
	DoneBy     string     `json:"done_by,omitempty"`
	DoneReason string     `json:"done_reason,omitempty"`
	ReopenedAt *time.Time `json:"reopened_at,omitempty"`
	// CancelledAt: when the task was cancelled; CancelledReason: why it was cancelled (#2272).
	CancelledAt     *time.Time  `json:"cancelled_at,omitempty"`
	CancelledReason string      `json:"cancelled_reason,omitempty"`
	Spaces          []TodoSpace `json:"spaces"`
	Agents          []TodoAgent `json:"agents"`
	// Quiet: a task from the main conversation while assistant.main_tasks_quiet was on (#1731,
	// docs/assistant/unified-entry.md "Quiet tasks"): no pushes, Live Activity or task badges for its agents; the main
	// conversation is told instead (quiet.go). Set when it is made, never changed.
	Quiet bool `json:"quiet"`
	// Supervision: the task's ledger when the main conversation handed it out and follows it up (#3124,
	// supervise.go); nil for any other todo. Clients that do not know it show the todo as ever.
	Supervision *Supervision `json:"supervision,omitempty"`
	// Phase and WaitState come from the task stage ledger. They are present in
	// either to-do mode once the task uses structured runtime reports.
	Phase               assistantwire.TaskPhase      `json:"phase,omitempty"`
	WaitState           *assistantwire.TaskWaitState `json:"wait_state,omitempty"`
	runtimeAt           time.Time
	runtimeObservedWait bool
	// Stage is how far its work got (plan, develop, review, merge, deploy, verify; "" not known) and PRURL the PR it
	// named, read off its agents' replies (reports.go, #2768); the apps draw the stage bar from them.
	Stage    string        `json:"stage,omitempty"`
	PRURL    string        `json:"pr_url,omitempty"`
	PRStatus *TodoPRStatus `json:"pr_status,omitempty"`
	// OtherPRs: the observed facts of its earlier PRs (PRs), newest first (#7935); the apps show each open one.
	OtherPRs      []TodoPRStatus `json:"other_prs,omitempty"`
	StageKind     string         `json:"stage_kind,omitempty"` // code or general; absent on older hubs
	StageDone     bool           `json:"stage_done,omitempty"` // closed, nothing left to deploy: Done ends the strip (stageDone, set by formatTodo, #11590)
	stageVerified bool           // explicit acceptance, kept with the stage setting
	planPending   bool           // a live plan-approval card; never persisted as progress
	// PRs are the earlier PRs its agents named before PRURL, newest last (reports.go todoStage.PRs): the Inbox links
	// a comment on any of them to the task (inbox_task_links.go, #6507). Not sent to the apps.
	PRs []string `json:"-"`
	// Delivery: how far its finished work went out, and while short of live where it is held and who can unhold it
	// (delivery.go, #3306); nil until judged.
	Delivery *TodoDelivery `json:"delivery,omitempty"`
	// Check: the hub's latest automatic completion judgment of its work, while still current (task_check.go, #3180); nil
	// until judged, and again once it is reopened or one of its agents starts a new turn. Clients that do not know it
	// show the todo as ever.
	Check *TodoCheck `json:"check,omitempty"`
	// Thread: the chat thread the todo is bound to (#5279, todo_thread.go); nil while it has none.
	Thread *TodoThread `json:"thread,omitempty"`
	// Source: where the todo was asked for when that is another chat (#6146, todo_source.go): the outcome is told
	// there once; nil for any other todo.
	Source *TodoSource `json:"source,omitempty"`
	// Responsible: an external responsible party that is not a Persona-started agent (#12712, todo_responsible.go):
	// a long-running session or a person; nil while none is set.
	Responsible *TodoResponsible `json:"responsible,omitempty"`
	// Audience: optional routing hint distinguishing human-facing deliverables ("human") from autonomous engineering
	// tasks ("autonomous"); omitted when unset (#14619).
	Audience string `json:"audience,omitempty"`
	// DirectionID: optional organizational direction membership, decoupled from runtime supervision.supervisor and
	// origin; omitted when unset (#14660).
	DirectionID string `json:"direction_id,omitempty"`
	// ItemType: stored explicit 2-type classification ("personal" or "task"), or "" when unset (#15758).
	// Emitted only on listedTodo when todos_v2=true so legacy/mobile wire payloads remain byte-identical when off.
	ItemType string `json:"-"`
	// When it is due (DueAt a moment, DueDate a day "2006-01-02" in the user's time zone; at most one), when the phone
	// is reminded of it and whether it was, and its priority ("" normal, high, low): #1683, todo_plan.go. Clients that do
	// not know them show the todo as ever.
	DueAt      *time.Time `json:"due_at,omitempty"`
	DueDate    string     `json:"due_date,omitempty"`
	RemindAt   *time.Time `json:"remind_at,omitempty"`
	RemindedAt *time.Time `json:"reminded_at,omitempty"`
	Priority   string     `json:"priority,omitempty"`
	// Project: the slug of the knowledge-base project it belongs to (#2581, todo_project.go); the apps group open
	// todos by it. Clients that do not know it show the todo as ever.
	Project string `json:"project,omitempty"`
	// Kind: "concern" for something the owner raised in the main conversation (#3561, concerns.go), which closes only
	// once its outcome was reported to them; left out for a to-do or a task. Set when it is made, never changed.
	Kind string `json:"kind,omitempty"`
	// SeenAt: when the owner last looked at it in Just finished, by Got it or by opening it (#5707, todo_seen.go); a to-do
	// seen since it got there is filed under Done (todo_triage.go). Absent when never seen.
	SeenAt *time.Time `json:"seen_at,omitempty"`
	// AutoSeenUndone: internal undo guard set when the owner explicitly calls DELETE /api/v1/todos/{id}/seen (#14639).
	// Never emitted on the wire.
	AutoSeenUndone bool `json:"-"`
	// RestoredAt: when the owner last brought it back from Inactive; SomedayUntil: when a to-do the owner put off to
	// Someday comes back, the year 9999 for no date (#10226, todo_marks.go). Absent when never restored, and while not
	// put off. Clients that do not know them show the to-do as ever.
	RestoredAt   *time.Time `json:"restored_at,omitempty"`
	SomedayUntil *time.Time `json:"someday_until,omitempty"`
	// InboxItem is the Inbox row it was made from (#6480); the to-do's page opens that row. Empty for any other to-do.
	InboxItem string `json:"inbox_item,omitempty"`
	// PartOf: the to-do whose request this one is a piece of; After: the pieces of that request it waits for
	// (todo_pieces.go, docs/assistant/todos.md "Requests in pieces"). Both empty for a to-do that is no piece. Clients
	// that do not know them show the to-do as ever.
	PartOf               string   `json:"part_of,omitempty"`
	After                []string `json:"after,omitempty"`
	RequireAcceptedAfter bool     `json:"require_accepted_after,omitempty"`
	// Brief: what was asked when the work was handed out, with its log, and Lineage: the to-dos that carried the work
	// (#3822, task_brief.go). Filled in where the apps read the to-dos (withBrief); absent for a to-do with no brief.
	Brief   *TaskBrief    `json:"brief,omitempty"`
	Lineage []LineageStep `json:"lineage,omitempty"`
	// Images are metadata and private media URLs only. The storeless service keeps their bytes separately; a database
	// reload reconstructs the same metadata without reading those bytes in the to-do list.
	Images    []TodoImage `json:"images,omitempty"`
	imageData map[string][]byte
}

// todoStore is what the todos keep in the database (internal/db). A Store without it (the unit tests' memStore) keeps
// them in memory only, as with no store at all.
type todoStore interface {
	ListTodos(ctx context.Context) ([]db.TodoRow, error)
	ListAllTodoSpaces(ctx context.Context) ([]db.TodoSpaceRow, error)
	ListAllTodoAgents(ctx context.Context) ([]db.TodoAgentRow, error)
	CreateTodo(ctx context.Context, t db.TodoRow) error
	UpdateTodo(ctx context.Context, id string, title *string, notes *string, done *bool, updatedAt time.Time) (*db.TodoRow, error)
	EditTodo(ctx context.Context, id string, e db.TodoEdit, updatedAt time.Time) (*db.TodoRow, error)
	SeeTodo(ctx context.Context, id string, at *time.Time) (*db.TodoRow, error)
	AutoSeeTodo(ctx context.Context, id string, doneAt time.Time, reopenedAt *time.Time, at time.Time) (*db.TodoRow, bool, error)
	RestoreTodo(ctx context.Context, id string, at time.Time) (*db.TodoRow, error)
	SetTodoSomeday(ctx context.Context, id string, until *time.Time) (*db.TodoRow, error)
	ClaimTodoReminder(ctx context.Context, id string, remindAt, now time.Time) (bool, error)
	RetitleTodo(ctx context.Context, id, from, to string, at time.Time) (*db.TodoRow, bool, error)
	FailTodo(ctx context.Context, id, startError string, at time.Time) (*db.TodoRow, error)
	AutoDoneTodo(ctx context.Context, id, reason string, at, workSince time.Time, guard func() error) (*db.TodoRow, bool, error)
	RetryTodo(ctx context.Context, id string, at time.Time) (*db.TodoRow, bool, error)
	DeleteTodo(ctx context.Context, id string) error
	LinkTodoSpace(ctx context.Context, todoID, machineID, space string, linkedAt time.Time, keep db.KeptLinks) (string, error)
	UnlinkTodoSpace(ctx context.Context, todoID, machineID, space string, keep db.KeptLinks) error
	RenameTodoSpace(ctx context.Context, machineID, oldSpace, newSpace string, keep db.KeptLinks) (bool, error)
	LinkTodoAgentWithSource(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error)
	LinkTodoAgentOfDoneTask(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error)
	AutoLinkUnlinkedTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error)
	UnlinkTodoAgent(ctx context.Context, todoID, machineID, sessionID string) error
	UpdateTodoAgentSnapshot(ctx context.Context, machineID, sessionID, title, startError string) error
	ListTodoDecisions(ctx context.Context, todoID string, all bool) ([]db.TodoDecisionRow, error)
	InsertTodoDecision(ctx context.Context, r db.TodoDecisionRow) (bool, error)
	CommitTodoAnswer(ctx context.Context, id string, decisions []db.TodoDecisionRow, settings map[string]any, at time.Time) (*db.TodoRow, []db.TodoDecisionRow, error)
	SetTodoDecisionSupersedes(ctx context.Context, todoID, id, supersedesID string, at time.Time) (*db.TodoDecisionRow, error)
	ReportConcern(ctx context.Context, r db.TodoDecisionRow, reason string) (*db.TodoRow, bool, error)
	ListTodoThreads(ctx context.Context) (map[string]db.TodoThread, error)
	SetTodoThread(ctx context.Context, todoID string, th db.TodoThread, at time.Time) error
	ClearTodoThread(ctx context.Context, todoID string, at time.Time) (bool, error)
	ListTodoSources(ctx context.Context) (map[string]db.TodoSource, error)
	SetTodoSource(ctx context.Context, todoID string, src db.TodoSource) error
	StopTodoSourceReport(ctx context.Context, todoID string, at time.Time) (bool, error)
	PlanTodoSourceReport(ctx context.Context, todoID, text string, due time.Time, fullReply bool, doneAt *time.Time) (bool, error)
	ClaimTodoSourceReport(ctx context.Context, todoID string, now time.Time, maxAttempts int) (int, string, bool, bool, error)
	ReleaseTodoSourceReport(ctx context.Context, todoID string) error
	ClearTodoSourcePlan(ctx context.Context, todoID string) (bool, error)
}

// The database keeps them: a Store that is it but lacked one of these would keep them in memory only.
var _ todoStore = (*db.DB)(nil)

// todoDB is the store the todos are in; nil: they live in memory only.
func (s *Service) todoDB() todoStore {
	st, _ := s.store().(todoStore)
	return st
}

// todoBook is the todos as the assistant has them: loaded from the store once, then kept as it writes them.
type todoBook struct {
	mu sync.RWMutex
	// writing is held across a todo's store write and putting the row it returns into memory (PATCH, reopening,
	// linking and unlinking an agent by hand, closing it on a judgment or a review, a failed start, a retitle), so
	// memory takes the rows in the order the store committed them: two devices writing the same todo at once
	// otherwise leave memory with whichever finished last, not the newer row. Every writer that puts a stored row
	// into memory takes it. Workspace ownership writes and deletion also take it through publication; fanout
	// rechecks ownership under it after session lookup. A write of part_of or after checks the request graph again
	// under it before storing (todo_pieces.go). Automatic links take it for memory changes and again for background persistence, so a
	// removed workspace cannot gain a late stored agent. Agent snapshots do not take it. Taken before supervise.writeMu and mu, never
	// while either is held; node calls and change notifications run after it is released.
	writing             sync.Mutex
	loaded              bool
	todos               map[string]*Todo
	deletedRequestGates map[string]bool
	activeBySession     map[[2]string]string        // (machine_id, session_id) -> the open todo it works for
	refusedResumes      map[resumeAttempt]time.Time // pending manual-resume decisions, scoped to this binding
	snapshotSyncing     map[[2]string]bool          // (machine_id, session_id) whose title is being written
	spacesByKey         map[string]string           // "<machine_id>/<space>" -> todo_id
	botmux              map[string]botmuxSeen       // by machine: its botmux sessions' chats and dispatches (todo_sessions.go)

	tokensMu sync.Mutex
	tokens   map[string]*tokenSync // by machine (todo_sessions.go)
	linked   map[[2]string]string  // the links the marks were last synced for: todo id and title, by agent

	titling sync.WaitGroup // the model naming tasks' todos (todo_titles.go)

	// reporting are the todos whose report back to their source is waiting for the outcome (todo_source.go).
	reportMu  sync.Mutex
	reporting map[string]time.Time
	// reportWaits are the longest answer waits found for todos still waiting (todo_source_answer.go).
	reportWaits map[string]time.Duration
	runMu       sync.Mutex // one report step at a time (sourceReportNow)

	// importing is held across a batch of imported reminders, and imports are their links when there is no store
	// (todo_imports.go).
	importing sync.Mutex
	imports   map[[2]string]db.TodoImport

	// remindWake wakes remindLoop when the todos change (todo_plan.go).
	remindOnce sync.Once
	remindWake chan struct{}

	// readyWakes persists idempotent todo_ready wake markers per open dependent to-do (todo_after_ready.go).
	readyWakeMu      sync.Mutex
	readyWakes       map[string]todoReadyWakeMarker
	readyWakesLoaded bool

	// prMergeWakes persists idempotent (todo_id, pr_url, merged) wake markers for unparked open tasks (pr_merge_wake.go).
	prMergeWakeMu      sync.Mutex
	prMergeWakes       map[string]prMergeWakeMarker
	prMergeWakesLoaded bool

	// parentCloseoutWakes persists idempotent parent_closeout wake markers per open parent to-do (todo_parent_closeout.go).
	parentCloseoutMu          sync.Mutex
	parentCloseoutWakes       map[string]parentCloseoutWakeMarker
	parentCloseoutWakesLoaded bool
}

// spaceKey computes the unified space key string ("machineID/space").
func spaceKey(machineID, space string) string {
	if machineID == "" || space == "" {
		return ""
	}
	return machineID + "/" + space
}

// sessionSpaceKey computes the unified space key string for a session.
func sessionSpaceKey(machineID string, c proto.Session) string {
	space := c.SpaceName
	if space == "" {
		space = c.SpaceID
	}
	return spaceKey(machineID, space)
}

// ensureTodosLoaded reads the todos from the store the first time; an error says why they can't be read.
func (s *Service) ensureTodosLoaded(ctx context.Context) error {
	loaded, err := s.loadTodoInventory(ctx)
	if loaded {
		s.backfillPlanSupervisors(ctx)
		s.restoreSupervisePending(ctx)
		s.migrateOwnerWaits(ctx) // waits earlier versions pushed straight to the owner go to the supervisor
		// The links as read are the ones the marks go by from now on: one that goes away later syncs its machine
		// again (syncLinkedTodoTokens), even before any of the links changed.
		s.syncLinkedTodoTokens()
		s.sweepSourceReports()
	}
	return err
}

// loadTodoInventory reads durable state without publishing session lists.
func (s *Service) loadTodoInventory(ctx context.Context) (bool, error) {
	b := &s.todos
	b.mu.RLock()
	loaded := b.loaded
	b.mu.RUnlock()
	if loaded {
		return false, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loaded {
		return false, nil
	}
	err := s.loadTodosLocked(ctx)
	return b.loaded, err
}

func (s *Service) loadTodosLocked(ctx context.Context) error {
	b := &s.todos
	if b.snapshotSyncing == nil {
		b.snapshotSyncing = make(map[[2]string]bool)
	}
	st := s.todoDB()
	if st == nil {
		if b.todos == nil {
			b.todos = make(map[string]*Todo)
			b.activeBySession = make(map[[2]string]string)
			b.spacesByKey = make(map[string]string)
		}
		b.loaded = true
		s.todoMarksBaseline(b.todos)
		return nil
	}

	tlist, err := st.ListTodos(ctx)
	if err != nil {
		return err
	}
	if gates, ok := st.(interface {
		DeletedRequestGates(context.Context) (map[string]bool, error)
	}); ok {
		b.deletedRequestGates, err = gates.DeletedRequestGates(ctx)
		if err != nil {
			return err
		}
	}
	slist, err := st.ListAllTodoSpaces(ctx)
	if err != nil {
		return err
	}
	alist, err := st.ListAllTodoAgents(ctx)
	if err != nil {
		return err
	}

	tmap := make(map[string]*Todo, len(tlist))
	active := make(map[[2]string]string)
	spaces := make(map[string]string)

	for _, tr := range tlist {
		images, imageData := todoImageMetadata(tr.ID, tr.Images)
		tmap[tr.ID] = &Todo{
			ID:         tr.ID,
			Title:      tr.Title,
			Notes:      tr.Notes,
			DoneAt:     tr.DoneAt,
			CreatedAt:  tr.CreatedAt,
			UpdatedAt:  tr.UpdatedAt,
			Origin:     tr.Origin,
			PlanID:     tr.PlanID,
			StartError: tr.StartError,
			DoneBy:     tr.DoneBy,
			DoneReason: tr.DoneReason,
			ReopenedAt: tr.ReopenedAt,
			Quiet:      tr.Quiet,
			Spaces:     make([]TodoSpace, 0),
			Agents:     make([]TodoAgent, 0),
			Images:     images,
			imageData:  imageData,
		}
		tmap[tr.ID].setPlan(tr)
	}

	for _, sr := range slist {
		if t, ok := tmap[sr.TodoID]; ok {
			t.Spaces = append(t.Spaces, TodoSpace{
				MachineID: sr.MachineID,
				Space:     sr.Space,
				LinkedAt:  sr.LinkedAt,
			})
			spaces[spaceKey(sr.MachineID, sr.Space)] = sr.TodoID
		}
	}

	for _, ar := range alist {
		t, ok := tmap[ar.TodoID]
		if !ok {
			continue
		}
		t.Agents = append(t.Agents, TodoAgent{
			MachineID:   ar.MachineID,
			SessionID:   ar.SessionID,
			LinkedAt:    ar.LinkedAt,
			ReleasedAt:  ar.ReleasedAt,
			Title:       ar.Title,
			Cwd:         ar.Cwd,
			Agent:       ar.Agent,
			StartError:  ar.StartError,
			Source:      ar.Source,
			Via:         ar.Via,
			DismissedAt: ar.DismissedAt,
		})
		if ar.ReleasedAt == nil && ar.DismissedAt == nil && t.DoneAt == nil {
			active[[2]string{ar.MachineID, ar.SessionID}] = t.ID
		}
	}

	if sst, ok := st.(supervisionStore); ok {
		rows, err := sst.ListTodoSupervision(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if t, ok := tmap[r.TodoID]; ok {
				t.Supervision = supervisionFromRow(r)
				t.resetReopenedUserCheck()
			}
		}
	}
	if err := s.loadTaskBriefs(ctx); err != nil {
		return err
	}
	threads, err := st.ListTodoThreads(ctx)
	if err != nil {
		return err
	}
	for id, th := range threads {
		if t, ok := tmap[id]; ok {
			t.Thread = &th
		}
	}
	sources, err := st.ListTodoSources(ctx)
	if err != nil {
		return err
	}
	for id, src := range sources {
		if t, ok := tmap[id]; ok {
			t.Source = &src
		}
	}
	for id, st := range s.todoStages(ctx) {
		if t, ok := tmap[id]; ok {
			t.Stage, t.PRURL, t.PRs, t.StageKind, t.Delivery, t.stageVerified, t.Check = st.Stage, st.PR, st.PRs, st.Kind, st.Delivery, st.Verified, st.Check
			t.PRStatus, t.OtherPRs = st.PRStatus, st.otherPRFacts()
			st.applyRuntime(t)
		}
	}

	b.todos = tmap
	b.activeBySession = active
	b.spacesByKey = spaces
	b.loaded = true
	s.todoMarksBaseline(b.todos)
	return nil
}

// todosLoaded: the todos were read (the session-list work waits for that rather than reading them on every list).
func (s *Service) todosLoaded() bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	return s.todos.loaded
}

// todosChanged tells the apps to read the todos again ({"type":"todos"}), once the rest of the write is in
// (todosPushSoon).
func (s *Service) todosChanged(ctx context.Context) {
	s.todoRevisionChanged() // seen/restored/Someday come through here too, without moving updated_at
	s.todoChangedSoon()
	s.remindSoon()        // a reminder set, moved or done (todo_plan.go)
	s.boardChanged()      // a to-do closed, reopened or gone leaves "Please verify"
	s.interruptsSoon()    // an interrupted receipt of it may read cancelled, rerouted or done now (interruptions.go)
	s.directionTreeSoon() // its direction's tasks and counts (direction_tree.go)
}

// todosSettle is how long a {"type":"todos"} waits for the rest of its write. One step of a task writes its to-do from
// several places a few milliseconds apart (the to-do, its report, its check, its supervision, its leader), and every
// event makes every app read the whole list again (#5995): they go out as one.
const todosSettle = 100 * time.Millisecond

// todosPush is the {"type":"todos"} waiting for todosSettle.
type todosPush struct {
	mu      sync.Mutex
	armed   bool
	sending int                             // armed or going out: tests wait for none before they count events
	after   func(d time.Duration, f func()) // tests replace the timer
}

// todosPushSoon sends the apps {"type":"todos"} todosSettle from now, unless one is waiting already. It stops waiting
// before it is sent: a change made from then on, even while it goes out, sends another, so the last read of every
// change starts after it.
func (s *Service) todosPushSoon() {
	p := &s.tpush
	p.mu.Lock()
	if p.armed {
		p.mu.Unlock()
		return
	}
	p.armed = true
	p.sending++
	after := p.after
	p.mu.Unlock()
	send := func() {
		p.mu.Lock()
		p.armed = false
		p.mu.Unlock()
		s.todoViewPushed() // every app reads the list again from here (todo_view_push.go)
		s.todosCountSoon() // and the counts view's to-dos follow, read by the manager if no app does (counts_view.go)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.event(ctx, map[string]any{"type": "todos"})
		p.mu.Lock()
		p.sending--
		p.mu.Unlock()
	}
	if after != nil {
		after(todosSettle, send)
		return
	}
	time.AfterFunc(todosSettle, send)
}

// linksChanged: which todo an agent works for changed (or a todo's title, or whether it is done): the hub is told
// (the snapshot's TodoReport: it pushes the session lists that look different now) and the herdr marks follow.
func (s *Service) linksChanged(ctx context.Context) {
	s.mcpChanges.notify()
	s.taskPlanApproval()
	s.publish(context.WithoutCancel(ctx))
	s.syncLinkedTodoTokens()
}

// quietTask: a task opened now from conversation origin is quiet (#1731): it comes from the main conversation while
// the user wants those told only there (its own settings: the main conversation, main_tasks_quiet), or from a
// reporting conversation (#3192, reporters.go). The one place this is decided.
func (s *Service) quietTask(ctx context.Context, origin string) bool {
	if origin == "" {
		return false
	}
	if _, ok := s.reporterOf(ctx, origin); ok {
		return true
	}
	return s.settings(ctx).Main == origin && s.mainTasksQuiet(ctx)
}

// removeTodo deletes todo id from the store and from memory (its agents' and spaces' links with it) and tells the
// apps; db.ErrTodoNotFound when it is not there. The DELETE route and the code that must take back a todo it just
// made (a topic that turned out to be taken, the lark module's thread_open.go) both go through it.
func (s *Service) removeTodo(ctx context.Context, id string) error {
	s.todos.writing.Lock()
	if current, ok := s.todoNow(id); ok {
		err := s.receivedFindingsCloseError(current, nil)
		if err == nil {
			err = s.investigationParentDeleteError(current)
		}
		if err == nil && current.Supervision != nil && current.Supervision.Investigation != nil &&
			current.Supervision.Investigation.Version != 0 {
			err = s.investigationTransitionError(ctx, current, current.Supervision, nil)
		}
		if err != nil {
			s.todos.writing.Unlock()
			return err
		}
	}
	if st := s.todoDB(); st != nil {
		if err := st.DeleteTodo(ctx, id); err != nil {
			s.todos.writing.Unlock()
			return err
		}
	}

	s.todos.mu.Lock()
	if cur, ok := s.todos.todos[id]; ok {
		if cur.RequireAcceptedAfter {
			if s.todos.deletedRequestGates == nil {
				s.todos.deletedRequestGates = make(map[string]bool)
			}
			s.todos.deletedRequestGates[id] = true
		}
		for _, a := range cur.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil {
				delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
			}
		}
		for _, sp := range cur.Spaces {
			delete(s.todos.spacesByKey, sp.MachineID+"/"+sp.Space)
		}
		s.todoGone(cur)
		delete(s.todos.todos, id)
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()

	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// createTodo makes a todo (origin and planID set when a plan from an assistant conversation opens it as a task) and
// tells the apps.
func (s *Service) createTodo(ctx context.Context, title, notes, origin, planID string) (Todo, error) {
	return s.newTodo(ctx, title, notes, origin, planID, s.quietTask(ctx, origin))
}

// createTaskTodo is createTodo for a plan's task, in the knowledge-base project the task names (a slug; "" for none).
func (s *Service) createTaskTodo(ctx context.Context, title, notes, origin, planID, project string) (Todo, error) {
	return s.createTaskTodoAs(ctx, "todo_"+randomHex(8), title, notes, origin, planID, project, "", "", "")
}

func (s *Service) createTaskTodoAs(ctx context.Context, id, title, notes, origin, planID, project, inboxItem, audience, directionID string) (Todo, error) {
	now := time.Now().UTC()
	return s.insertTodo(ctx, db.TodoRow{ID: id, Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now,
		Origin: origin, PlanID: planID, Quiet: s.quietTask(ctx, origin), Project: project, InboxItem: inboxItem, Audience: audience, DirectionID: strings.TrimSpace(directionID)})
}

// newTodo is createTodo, quiet or not as given (a launcher repair's has no conversation and is quiet,
// launcher_repair.go).
func (s *Service) newTodo(ctx context.Context, title, notes, origin, planID string, quiet bool) (Todo, error) {
	return s.newTodoAs(ctx, "todo_"+randomHex(8), title, notes, origin, planID, quiet)
}

// newTodoAs is newTodo with the id given (a question's to-do has its card's, ask_todo.go).
func (s *Service) newTodoAs(ctx context.Context, id, title, notes, origin, planID string, quiet bool) (Todo, error) {
	now := time.Now().UTC()
	return s.insertTodo(ctx, db.TodoRow{ID: id, Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now, Origin: origin, PlanID: planID,
		Quiet: quiet})
}

// insertTodo stores todo row, puts it in memory and tells the apps.
func (s *Service) insertTodo(ctx context.Context, row db.TodoRow, bindings ...*repository.CreateBindings) (Todo, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, err
	}
	var out Todo
	var err error
	if row.PartOf != "" || len(row.After) > 0 {
		if row.Responsible != nil && row.Responsible.Kind == db.ResponsibleSession && row.Responsible.SessionID != "" {
			if err := s.waitIdleCloseFence(ctx, row.Responsible.MachineID, row.Responsible.SessionID); err != nil {
				return Todo{}, err
			}
		}
		out, err = s.insertPiece(ctx, row, bindings...)
	} else if row.Responsible != nil && row.Responsible.Kind == db.ResponsibleSession && row.Responsible.SessionID != "" {
		if err := s.lockTodoWritingWithIdleFence(ctx, row.Responsible.MachineID, row.Responsible.SessionID); err != nil {
			return Todo{}, err
		}
		out, err = s.putNewTodo(ctx, row, bindings...)
		s.todos.writing.Unlock()
	} else {
		out, err = s.putNewTodo(ctx, row, bindings...)
	}
	if err != nil {
		return Todo{}, err
	}
	s.todosChanged(ctx)
	return out, nil
}

// insertPiece is putNewTodo for a piece of a request: its links checked again and the row stored with todoBook.writing
// held, as editTodo writes them (todo_pieces.go).
func (s *Service) insertPiece(ctx context.Context, row db.TodoRow, bindings ...*repository.CreateBindings) (Todo, error) {
	if f := s.pieceWriteHook; f != nil {
		f(row.ID)
	}
	if row.Responsible != nil && row.Responsible.Kind == db.ResponsibleSession && row.Responsible.SessionID != "" {
		if err := s.lockTodoWritingWithIdleFence(ctx, row.Responsible.MachineID, row.Responsible.SessionID); err != nil {
			return Todo{}, err
		}
	} else {
		s.todos.writing.Lock()
	}
	defer s.todos.writing.Unlock()
	e, err := s.recheckPieces("", db.TodoEdit{PartOf: &row.PartOf, After: &row.After})
	if err != nil {
		return Todo{}, err
	}
	row.PartOf, row.After = *e.PartOf, *e.After
	return s.putNewTodo(ctx, row, bindings...)
}

// putNewTodo is insertTodo's store write and memory, without telling anyone.
func (s *Service) putNewTodo(ctx context.Context, row db.TodoRow, bindings ...*repository.CreateBindings) (Todo, error) {
	if err := db.ValidateTodoAudience(row.Audience); err != nil {
		return Todo{}, err
	}
	if err := db.ValidateTodoItemType(row.ItemType); err != nil {
		return Todo{}, err
	}
	row.DirectionID = strings.TrimSpace(row.DirectionID)
	if len(bindings) > 0 && bindings[0] != nil {
		st, ok := s.store().(todoRepositoryStore)
		if !ok {
			return Todo{}, repository.Problem("store_unavailable")
		}
		if err := st.CreateTodoWithRepositories(ctx, row, *bindings[0]); err != nil {
			return Todo{}, err
		}
	} else if st := s.todoDB(); st != nil {
		if err := st.CreateTodo(ctx, row); err != nil {
			return Todo{}, err
		}
	}
	s.todos.mu.Lock()
	out := s.addTodoRowLocked(row)
	s.todos.mu.Unlock()
	return out, nil
}

// addTodoRowLocked puts a newly committed row into the book with todos.mu held.
func (s *Service) addTodoRowLocked(row db.TodoRow) Todo {
	id := row.ID
	images, imageData := todoImageMetadata(id, row.Images)
	t := &Todo{ID: id, Title: row.Title, Notes: row.Notes, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Origin: row.Origin,
		PlanID: row.PlanID, Quiet: row.Quiet, Spaces: []TodoSpace{}, Agents: []TodoAgent{}, Images: images, imageData: imageData}
	t.setPlan(row)
	s.todos.todos[id] = t
	return formatTodo(copyTodoLocked(t))
}

func copyTodoLocked(t *Todo) Todo {
	out := *t
	if t.WaitState != nil {
		wait := *t.WaitState
		out.WaitState = &wait
	}
	if t.Phase.Valid() {
		switch {
		case t.DoneAt != nil:
			out.Phase, out.WaitState = assistantwire.TaskPhaseDone, nil
		case t.ReopenedAt != nil && t.runtimeAt.Before(*t.ReopenedAt):
			out.Phase, out.WaitState = assistantwire.TaskPhaseWorking, nil
		}
	}
	if t.planPending && t.DoneAt == nil && !t.stageVerified && stageIndex(t.Stage) <= stageIndex("develop") {
		out.Stage = "plan" // approval is current, not a rollback of the stored forward-only stage
	}
	if t.DoneAt != nil {
		da := *t.DoneAt
		out.DoneAt = &da
	}
	if t.ReopenedAt != nil {
		ra := *t.ReopenedAt
		out.ReopenedAt = &ra
	}
	if t.CancelledAt != nil {
		ca := *t.CancelledAt
		out.CancelledAt = &ca
	}
	if t.RestoredAt != nil {
		ra := *t.RestoredAt
		out.RestoredAt = &ra
	}
	if t.SomedayUntil != nil {
		su := *t.SomedayUntil
		out.SomedayUntil = &su
	}
	out.Spaces = make([]TodoSpace, len(t.Spaces))
	copy(out.Spaces, t.Spaces)
	out.After = slices.Clone(t.After)
	out.Images = slices.Clone(t.Images)
	out.Agents = make([]TodoAgent, len(t.Agents))
	for i, a := range t.Agents {
		out.Agents[i] = a
		if a.ReleasedAt != nil {
			ra := *a.ReleasedAt
			out.Agents[i].ReleasedAt = &ra
		}
		if a.DismissedAt != nil {
			da := *a.DismissedAt
			out.Agents[i].DismissedAt = &da
		}
	}
	out.Supervision = t.Supervision.clone()
	if t.Thread != nil {
		th := *t.Thread
		out.Thread = &th
	}
	if t.Source != nil {
		src := *t.Source
		out.Source = &src
	}
	out.Responsible = cloneResponsible(t.Responsible)
	return out
}

// formatTodo is a todo as the apps get it: no dismissed agents, the active ones first (newest link first), then those
// that worked for it (latest released first).
func formatTodo(t Todo) Todo {
	out := t
	out.Delivery = currentDelivery(t)
	out.StageDone = stageDone(t) // before the wire's done_by hides a cancellation
	if out.Source != nil && out.Source.ReportFullReply {
		source := *out.Source
		source.ReportText = "" // the delivery payload is not part of the to-do list
		out.Source = &source
	}
	// Existing clients use review for cancellation labels and reasons. Keep that wire value; Cancelled covers both the
	// stored marker and legacy review rows whose supervision verdict records the cancellation (#5732).
	if out.Cancelled() {
		// Cancellation is stored as a closed row. Derive its independent outcome at the API boundary so every client,
		// including one without a supervision ledger, can keep it out of Completed (#5732).
		if out.CancelledAt == nil {
			out.CancelledAt = out.DoneAt
		}
		if out.CancelledReason == "" {
			out.CancelledReason = out.DoneReason
			if out.Supervision != nil {
				for i := len(out.Supervision.Reviews) - 1; i >= 0; i-- {
					if r := out.Supervision.Reviews[i]; r.Verdict == reviewCancelled && r.Note != "" {
						out.CancelledReason = r.Note
						break
					}
				}
			}
		}
		out.DoneBy = db.DoneByReview
	}
	if out.Spaces == nil {
		out.Spaces = []TodoSpace{}
	}
	out.Agents = make([]TodoAgent, 0, len(t.Agents))
	var curr, past []TodoAgent
	for _, a := range t.Agents {
		// Dismissed rows are not returned by the API (#762)
		if a.DismissedAt != nil {
			continue
		}
		if a.ReleasedAt == nil {
			curr = append(curr, a)
		} else {
			past = append(past, a)
		}
	}
	sort.Slice(curr, func(i, j int) bool {
		return curr[i].LinkedAt.After(curr[j].LinkedAt)
	})
	sort.Slice(past, func(i, j int) bool {
		if past[i].ReleasedAt != nil && past[j].ReleasedAt != nil {
			return past[i].ReleasedAt.After(*past[j].ReleasedAt)
		}
		return false
	})
	out.Agents = append(out.Agents, curr...)
	out.Agents = append(out.Agents, past...)
	if out.Supervision != nil && isCodexRouteProgress(out.Supervision.Progress) {
		out.Supervision = out.Supervision.clone()
	}
	if out.Supervision != nil && out.Supervision.State == reviewNeedsUser && triageReviewState(out) != reviewNeedsUser {
		out.Supervision = out.Supervision.clone()
		out.Supervision.State = "" // its old review stays in history; only a fresh review may ask the owner again
	}
	return out
}

// ---- reading them ----

// todo is todo id; an error when the todos can't be read.
func (s *Service) todo(ctx context.Context, id string) (Todo, bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false, err
	}
	t, ok := s.todoNow(id)
	return t, ok, nil
}

// todoNow is a copy of todo id as it is now.
func (s *Service) todoNow(id string) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t, ok := s.todos.todos[id]
	if !ok {
		return Todo{}, false
	}
	return copyTodoLocked(t), true
}

// todosOf are the todos opened from conversation ("*": from any) as tasks; none when they can't be read. A question's
// to-do (ask_todo.go) is no task: it has its own line in the snapshot and its own row in the briefing. It has no origin;
// the skip still keeps one out that carries an old origin, from a database 0453_ask_todo_no_origin.sql hasn't reached.
func (s *Service) todosOf(ctx context.Context, conversation string) []Todo {
	return s.todosWhere(ctx, conversation, nil)
}

// todosWhere is todosOf with only the to-dos keep says yes to (nil: all) copied: a pass that looks at a few of them
// (the open ones) does not copy every finished one first (#5995). keep reads the to-do under the to-dos' lock and must
// not change it.
func (s *Service) todosWhere(ctx context.Context, conversation string, keep func(*Todo) bool) []Todo {
	if s.ensureTodosLoaded(ctx) != nil {
		return nil
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	list := []Todo{}
	for _, t := range s.todos.todos {
		if isAskTodo(t.ID) {
			continue
		}
		if (conversation == "*" && t.Origin != "" || conversation != "*" && t.Origin == conversation) && (keep == nil || keep(t)) {
			list = append(list, copyTodoLocked(t))
		}
	}
	return list
}

// todoView is todo id as the apps get it.
func (s *Service) todoView(ctx context.Context, id string) (json.RawMessage, error) {
	t, ok, err := s.todo(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, db.ErrTodoNotFound
	}
	return json.Marshal(s.withBrief(formatTodo(t)))
}

// taskOf is the task (a todo opened from an assistant conversation, still open) the agent mid/sid works for.
// Cancelled reports whether the task is cancelled (by review, cancellation marker, or interaction).
func (t Todo) Cancelled() bool {
	if t.CancelledAt != nil {
		return true
	}
	if t.DoneBy == db.DoneByCancelled {
		return true
	}
	if t.DoneBy == db.DoneByReview && t.Supervision != nil && t.Supervision.state(t) == reviewCancelled {
		return true // legacy cancellation: the compatible review marker plus its supervision verdict (#5732)
	}
	return false
}

// failedStart: its agent did not start, and it still waits on the owner. Such a to-do is stored done with its
// start_error (#1077, failTodo), but nothing was done: the To-dos SOP keeps it open (todo-status-sop.md rule 1), a
// retry or a plan's todo_id reopens it, and the assistant's snapshot counts it open while the app does. A cancelled one
// is closed for good.
func (t Todo) failedStart() bool {
	return t.DoneAt != nil && t.StartError != "" && !t.Cancelled()
}

// closedForGood: done (by hand, by the hub's judge or a review) or cancelled, and not a failed start that still waits
// on the owner. Only such a to-do is refused as "already done" where a failed start is taken up again.
func (t Todo) closedForGood() bool {
	return t.DoneAt != nil && !t.failedStart()
}

// AgentAborted reports whether the agent (machineID, sessionID) was let go, replaced or reassigned.
func (t Todo) AgentAborted(machineID, sessionID string) bool {
	for _, a := range t.Agents {
		if a.MachineID == machineID && a.SessionID == sessionID {
			return a.ReleasedAt != nil || a.DismissedAt != nil
		}
	}
	return false
}

func (s *Service) taskOf(ctx context.Context, mid, sid string) (Todo, bool) {
	if s.ensureTodosLoaded(ctx) != nil {
		return Todo{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	id, ok := s.todos.activeBySession[[2]string{mid, sid}]
	if !ok {
		return Todo{}, false
	}
	t := s.todos.todos[id]
	if t == nil || t.Origin == "" || t.DoneAt != nil {
		return Todo{}, false
	}
	return copyTodoLocked(t), true
}

// completedTodo reports whether mid/sid still belongs to a completed todo and no open one, or to an open task whose
// timeout holds automatic follow-ups. Unlike closedTaskOf, manual todos and user completions count too: a new turn
// alone does not authorize automatic follow-ups.
func (s *Service) completedTodo(ctx context.Context, mid, sid string) (bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, err
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	if id, open := s.todos.activeBySession[[2]string{mid, sid}]; open {
		t := s.todos.todos[id]
		return t != nil && (t.Supervision.state(*t) == reviewTimedOut || t.Supervision.state(*t) == reviewClosing), nil
	}
	for _, t := range s.todos.todos {
		if t.DoneAt == nil {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID == mid && a.SessionID == sid && a.ReleasedAt == nil && a.DismissedAt == nil {
				return true, nil
			}
		}
	}
	return false, nil
}

// closedTaskOf is the task the hub closed (judged done, or accepted by its supervisor) the agent mid/sid still belongs
// to: linked, neither released nor dismissed, and working for no open todo since; the one closed last when there are
// several (#5892). Cancelled tasks and ones the user closed stay theirs to reopen, even when an agent starts work.
func (s *Service) closedTaskOf(ctx context.Context, mid, sid string) (Todo, bool) {
	if s.ensureTodosLoaded(ctx) != nil {
		return Todo{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	if _, open := s.todos.activeBySession[[2]string{mid, sid}]; open {
		return Todo{}, false
	}
	var found *Todo
	for _, t := range s.todos.todos {
		if !hubClosedTask(t) || found != nil && !t.DoneAt.After(*found.DoneAt) {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID == mid && a.SessionID == sid && a.ReleasedAt == nil && a.DismissedAt == nil {
				found = t
				break
			}
		}
	}
	if found == nil {
		return Todo{}, false
	}
	return copyTodoLocked(found), true
}

// hubClosedTask: t is a task the hub closed, judged done or accepted by a review, and not cancelled: more work for it
// may open it again (closedTaskOf, routeToTaskAs). Older cancellations used done_by=review; the ledger keeps their
// verdict. Cancelled tasks and ones the user closed stay theirs to reopen.
func hubClosedTask(t *Todo) bool {
	return t.Origin != "" && t.DoneAt != nil && t.StartError == "" && (t.DoneBy == db.DoneByAuto || t.DoneBy == db.DoneByReview) &&
		t.Supervision.state(*t) != reviewCancelled
}

// ---- HTTP ----

var errTodosUnavailable = errors.New("todos service unavailable")

func (s *Service) todoRoutes() {
	s.todoImageRoutes()
	s.todoRepositoryRoutes()
	s.workspaceNamingRoutes()
	s.taskWorkspaceRoutes()
	s.taskReviewDismissalRoutes()
	s.todoDecisionRoutes()
	s.outsideDecisionRoutes()
	s.todoThreadRoutes()
	s.todoSeenRoutes()
	s.todoMarkRoutes()
	s.todoRetitleRoute()
	s.todoReorganizeRoute()
	s.todoDecisionsBatchRoute()
	s.todoBlockersRoutes()
	s.todoMergeTrainRoutes()
	s.todoTopologyOpsRoutes()
	s.todoImportRoutes()
	s.todoViewRoutes()
	s.todoHandoffRoutes()
	s.todoAskRoutes()
	s.todoTimelineRoutes()
	s.todoCausalTraceRoutes()
	// GET /api/v1/todos: list all todos (newest created_at first), agents sorted (active first, then released); a todo
	// waiting for the owner carries its ask (todo_ask.go), an open one nobody works on why (tasks_orphan.go); each its
	// view, and the list its counts (todo_view_routes.go); to an extension's token without the to-dos it is not shown,
	// marked partial when one it cannot be told about yet is left out, 503 when the store cannot tell (todo_scope.go).
	// Without parameters the answer is complete. ?closed_limit= bounds the closed rows for clients that opt in
	// (todo_pagination.go). With ?limit or ?cursor it answers one page of the whole list and how to read the next.
	s.handle("GET /api/v1/todos", func(w http.ResponseWriter, r *http.Request) {
		limit, after, paged, err := todoListPageQuery(r.URL.Query())
		if err != nil {
			code := "invalid_todo_limit"
			if errors.Is(err, errTodoPageCursor) {
				code = "invalid_todo_cursor"
			}
			apierr.WriteCode(w, http.StatusBadRequest, code, err)
			return
		}
		closedLimit := 0
		boundedClosed := false
		if !paged {
			closedLimit, boundedClosed, err = todoClosedLimitQuery(r.URL.Query())
			if err != nil {
				apierr.WriteCode(w, http.StatusBadRequest, "invalid_todo_closed_limit", err)
				return
			}
		}
		writes := s.trevision.writes.Load()
		ctx := r.Context()
		isExtension := r.Header.Get(RoleHeader) == roleExtension
		var cfg Settings
		if !isExtension {
			cfg, _ = s.settingsLastGood(ctx)
		}
		list, p, err := s.todosViewed(ctx, true)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		var counts assistantwire.TodoCounts
		if !paged {
			counts = p.counts()
		}
		partial := false
		if isExtension {
			if list, partial, err = s.extensionRows(ctx, list, p); err != nil {
				apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
				return
			}
		}
		var sharedBlockers []projection.SharedBlocker
		var board *todoBoardProjection
		if !isExtension {
			sharedBlockers = p.sharedBlockers()
			prefs := todoRevisionPrefsOf(cfg)
			var directions map[string]string
			if prefs.V2 || !paged {
				directions = s.todoDirectionLabels(ctx, cfg)
			}
			if prefs.V2 {
				sharedBlockers = mergePersistedSharedBlockers(list, p, sharedBlockers)
				s.populateTodoEffectiveDirections(list, directions)
				if !paged {
					// The board is the same complete projection as Counts. The closed_limit option bounds the rows a list
					// client has to parse; it does not silently create a second, smaller set of board totals.
					board = buildTodoBoardProjectionWithOptions(list, directions, prefs.FoldChildren, counts)
				}
			}
			if !paged {
				s.tagTodoRevision(ctx, w, writes, p, prefs, directions)
			}
		}
		var closedNext string
		var closedHasMore bool
		if boundedClosed {
			list, closedNext, closedHasMore = boundedTodoList(list, closedLimit)
		}
		if paged {
			writeTodoPage(w, list, p, sharedBlockers, limit, after)
			return
		}
		apierr.WriteJSON(w, todosList{
			Todos:            list,
			Counts:           counts,
			SharedBlockers:   sharedBlockers,
			Board:            board,
			ClosedNextCursor: closedNext,
			ClosedHasMore:    closedHasMore,
			Partial:          partial,
		})
	})
	s.handle("GET /api/v1/todos/revision", s.todoRevisionRead)

	// POST /api/v1/todos: create todo {title, notes, due_at | due_date, remind_at, priority, project, kind}; kind concern
	// records one of the owner's concerns (concerns.go): it closes only once its outcome is reported (…/decisions
	// {source: "reported"}).
	s.handle("POST /api/v1/todos", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		var in struct {
			Title                string               `json:"title"`
			Notes                string               `json:"notes"`
			Images               []todoImageInputJSON `json:"images"`
			Project              *string              `json:"project"`
			Kind                 string               `json:"kind"`
			RequireAcceptedAfter *bool                `json:"require_accepted_after"`
			RepositoryBindings   json.RawMessage      `json:"repository_bindings"`
			Responsible          json.RawMessage      `json:"responsible"`
			todoPlanIn
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		images, imageStatus, err := parseTodoImages(in.Images)
		if err != nil {
			apierr.Write(w, imageStatus, err)
			return
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("title cannot be empty"))
			return
		}
		kind, err := todoKind(in.Kind)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		var plan db.TodoEdit
		if setResp, respVal, err := parseResponsiblePatch(in.Responsible); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		} else if setResp {
			plan.SetResponsible, plan.Responsible = true, respVal
		}
		if in.RequireAcceptedAfter != nil {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, errors.New("only the owner can change request prerequisite enforcement"))
				return
			}
			if kind == db.KindConcern && *in.RequireAcceptedAfter {
				apierr.Write(w, http.StatusBadRequest, errRequestGateRoot)
				return
			}
			plan.RequireAcceptedAfter = in.RequireAcceptedAfter
		}
		if err := in.todoPlanIn.edit(&plan); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if s.settings(r.Context()).todosV2On() && in.ItemType != nil {
			it, err := todoItemType(*in.ItemType)
			if err != nil {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
			plan.ItemType = &it
		}
		if err := s.setTodoProject(r.Context(), in.Project, &plan); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		bindings, err := parseCreateBindings(in.RepositoryBindings)
		if err == nil {
			err = s.resolveCreateBindings(r.Context(), bindings) // before the store, outside todos.writing
		}
		if err != nil {
			taskRepositoriesError(w, err)
			return
		}
		var resTodo Todo
		if kind == db.KindConcern {
			resTodo, err = s.createConcernWithImages(r.Context(), title, strings.TrimSpace(in.Notes), plan, images, bindings)
		} else {
			resTodo, err = s.createPlainTodoWithImages(r.Context(), title, in.Notes, plan, images, bindings)
		}
		if bindings != nil && err != nil {
			taskRepositoriesError(w, err)
			return
		}
		if errors.Is(err, errConcernNoStore) {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"todo": resTodo})
	})

	// PATCH /api/v1/todos/{id}: update title, notes, done, due_at | due_date, remind_at, priority, project ("" clears it),
	// part_of ("" clears it), after ([] clears it), and responsible (null clears it), checked as todo_pieces.go and
	// todo_responsible.go say
	s.handle("PATCH /api/v1/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		id := r.PathValue("id")
		cur, ok := s.todoNow(id)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}

		var in struct {
			Title                *string         `json:"title"`
			Notes                *string         `json:"notes"`
			Done                 *bool           `json:"done"`
			Project              *string         `json:"project"`
			PartOf               json.RawMessage `json:"part_of"`
			After                json.RawMessage `json:"after"`
			RequireAcceptedAfter *bool           `json:"require_accepted_after"`
			Responsible          json.RawMessage `json:"responsible"`
			todoPlanIn
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		e := db.TodoEdit{Title: in.Title, Notes: in.Notes, Done: in.Done}
		if in.RequireAcceptedAfter != nil && !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("only the owner can change request prerequisite enforcement"))
			return
		}
		e.RequireAcceptedAfter = in.RequireAcceptedAfter
		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if t == "" {
				apierr.Write(w, http.StatusBadRequest, errors.New("title cannot be empty"))
				return
			}
			e.Title = &t
		}
		if setResp, respVal, err := parseResponsiblePatch(in.Responsible); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		} else if setResp {
			e.SetResponsible, e.Responsible = true, respVal
		}
		if err := in.todoPlanIn.edit(&e); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if s.settings(r.Context()).todosV2On() && in.ItemType != nil {
			it, err := todoItemType(*in.ItemType)
			if err != nil {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
			e.ItemType = &it
		}
		if err := s.setTodoProject(r.Context(), in.Project, &e); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		pieces, err := pieceArgs(in.PartOf, in.After)
		if err == nil && pieces.set() && cur.isConcern() {
			err = errors.New("a concern is no piece of a request")
		}
		var links db.TodoEdit
		if err == nil {
			links, err = s.checkPieces(id, pieces)
		}
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		e.PartOf, e.After = links.PartOf, links.After
		ctx := r.Context()
		if e.Done != nil && *e.Done {
			if work := s.taskCloseWorkSnapshot(cur, time.Now()); work != nil {
				if !fullRole(r) {
					apierr.WriteCode(w, http.StatusConflict, "todo_close_active", &taskCloseHoldError{todo: id, work: work})
					return
				}
				ctx, err = s.confirmOwnerTaskClose(ctx, cur, work, false)
				if err != nil {
					apierr.WriteCode(w, http.StatusUnprocessableEntity, "todo_close_not_confirmed", err)
					return
				}
			}
		}
		resTodo, err := s.editTodo(ctx, id, e)
		if err != nil {
			if errors.Is(err, db.ErrTodoNotFound) {
				apierr.Write(w, http.StatusNotFound, err)
			} else if errors.Is(err, errOutsideDecisionPending) || isInvestigationGuardError(err) {
				apierr.WriteCode(w, http.StatusConflict, "outside_decision_pending", err)
			} else if errors.Is(err, db.ErrConcernUnreported) {
				apierr.Write(w, http.StatusConflict, err) // report its outcome instead: POST …/decisions {source: "reported"}
			} else if hold := (*taskCloseHoldError)(nil); errors.As(err, &hold) {
				apierr.WriteCode(w, http.StatusConflict, "todo_close_active", i18n.Err("assistant-todos.close_not_confirmed"))
			} else if pe := (*pieceError)(nil); errors.As(err, &pe) {
				apierr.Write(w, http.StatusBadRequest, err) // another write changed the request since it was checked
			} else {
				apierr.Write(w, http.StatusInternalServerError, err)
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"todo": resTodo})
	})

	// DELETE /api/v1/todos/{id}: delete todo
	s.handle("DELETE /api/v1/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		id := r.PathValue("id")
		if _, ok := s.todoNow(id); !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if err := s.deleteTodo(r.Context(), id); err != nil {
			if errors.Is(err, db.ErrTodoNotFound) {
				apierr.Write(w, http.StatusNotFound, err)
			} else if errors.Is(err, errOutsideDecisionPending) || isInvestigationGuardError(err) {
				apierr.WriteCode(w, http.StatusConflict, "outside_decision_pending", err)
			} else {
				apierr.Write(w, http.StatusInternalServerError, err)
			}
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})

	// PUT /api/v1/todos/{id}/spaces/{mid}/{space...}: link whole space to todo
	s.handle("PUT /api/v1/todos/{id}/spaces/{mid}/{space...}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		mid := r.PathValue("mid")
		space := r.PathValue("space")
		if space == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("space is required"))
			return
		}

		tInfo, ok := s.todoNow(todoID)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if tInfo.DoneAt != nil {
			apierr.Write(w, http.StatusConflict, errors.New("todo is already completed"))
			return
		}
		if r.Header.Get(RoleHeader) == roleExtension && s.spaceTakeRefused(r.Context(), todoID, mid, space) {
			apierr.Write(w, http.StatusNotFound, errors.New("space not found")) // what it is not shown (todo_scope.go)
			return
		}

		linkedCount, err := s.linkTodoSpace(r.Context(), todoID, mid, space)
		if err != nil {
			todoErr(w, err)
			return
		}

		apierr.WriteJSON(w, map[string]any{"ok": true, "linked": linkedCount})
	})

	// DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}: unlink space from todo
	s.handle("DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		if _, ok := s.todoNow(todoID); !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if err := s.unlinkTodoSpace(r.Context(), todoID, r.PathValue("mid"), r.PathValue("space")); err != nil {
			todoErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})

	// PUT /api/v1/todos/{id}/agents/{mid}/{sid}: link existing agent
	s.handle("PUT /api/v1/todos/{id}/agents/{mid}/{sid}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		mid := r.PathValue("mid")
		sid := r.PathValue("sid")

		tInfo, ok := s.todoNow(todoID)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		if tInfo.DoneAt != nil {
			apierr.Write(w, http.StatusConflict, errors.New("todo is already completed"))
			return
		}

		var title, cwd, agent string
		c, found, getErr := s.sessionNow(r.Context(), mid, sid)
		if found {
			title, cwd, agent = c.Title, c.Cwd, c.Agent
		}
		// An extension's token takes no run it cannot be shown (todo_scope.go): one in a hidden space or one off a
		// to-do it is not shown answers 404. One whose computer does not list it, whose space cannot be told, answers
		// by the computer (#12216): 404 machine_not_found for a computer that is not one of the hub's, 503
		// machine_offline for one that is off now, and only on one that is on 409 agent_not_listed, to be tried again
		// (a session just started is answered before its computer lists it).
		if r.Header.Get(RoleHeader) == roleExtension {
			if !found {
				switch s.takeComputer(r.Context(), mid, getErr) {
				case standingComputerRemoved:
					apierr.WriteCode(w, http.StatusNotFound, "machine_not_found", i18n.Err("assistant-todos.machine_not_found"))
				case standingComputerOffline:
					w.Header().Set("Retry-After", strconv.Itoa(todoTakeOfflineRetrySeconds))
					apierr.WriteCode(w, http.StatusServiceUnavailable, "machine_offline", i18n.Err("assistant-todos.machine_offline"))
				default:
					w.Header().Set("Retry-After", strconv.Itoa(todoTakeRetrySeconds))
					apierr.WriteCode(w, http.StatusConflict, "agent_not_listed", i18n.Err("assistant-todos.agent_not_listed"))
				}
				return
			}
			if s.todoTakeRefused(r.Context(), mid, c) {
				apierr.Write(w, http.StatusNotFound, errors.New("agent not found"))
				return
			}
		}
		if err := s.linkTodoAgent(r.Context(), todoID, mid, sid, title, cwd, agent); err != nil {
			todoErr(w, err)
			return
		}
		s.superviseLinkedTodo(r.Context(), todoID) // the user's own todo gets a supervisor (agent_tasks.go)
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})

	// DELETE /api/v1/todos/{id}/agents/{mid}/{sid}: unlink agent (soft unlink: dismissed_at + released_at #762)
	s.handle("DELETE /api/v1/todos/{id}/agents/{mid}/{sid}", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		todoID := r.PathValue("id")
		mid := r.PathValue("mid")
		sid := r.PathValue("sid")

		target, ok := s.todoNow(todoID)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
			return
		}
		agentFound := false
		for _, a := range target.Agents {
			if a.MachineID == mid && a.SessionID == sid {
				agentFound = true
				break
			}
		}
		if !agentFound {
			apierr.Write(w, http.StatusNotFound, errors.New("agent link not found"))
			return
		}
		if !s.loadWork(r.Context()) {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("the agents' work could not be read yet"))
			return
		}

		key := [2]string{mid, sid}
		s.todos.writing.Lock()
		if st := s.todoDB(); st != nil {
			if err := st.UnlinkTodoAgent(r.Context(), todoID, mid, sid); err != nil {
				s.todos.writing.Unlock()
				todoErr(w, err)
				return
			}
		}

		now := time.Now().UTC()
		var unlinkedCwd string
		var detached bool
		s.todos.mu.Lock()
		if target, ok := s.todos.todos[todoID]; ok {
			for i := range target.Agents {
				if target.Agents[i].MachineID == mid && target.Agents[i].SessionID == sid {
					detached = target.Agents[i].DismissedAt == nil
					unlinkedCwd = target.Agents[i].Cwd
					target.Agents[i].ReleasedAt = &now
					target.Agents[i].DismissedAt = &now
					break
				}
			}
			target.UpdatedAt = now
		}
		if s.todos.activeBySession[key] == todoID {
			delete(s.todos.activeBySession, key)
		}
		_, stillAttached := s.todos.activeBySession[key]
		s.todos.mu.Unlock()
		var retired *db.SessionWork
		var hiddenCard string
		if detached && !stillAttached {
			retired, hiddenCard = s.retireDetachedRetry(key, now)
		}
		s.todos.writing.Unlock()

		if retired != nil {
			s.saveWork(*retired)
			if hiddenCard != "" {
				s.recordEnd(hiddenCard, "cancelled", nil)
			}
			s.bindRetiredTurn(key, now)
		}
		s.linksChanged(r.Context())
		s.todosChanged(r.Context())
		if unlinkedCwd != "" {
			s.drainWorkspaceQueue(r.Context(), mid, unlinkedCwd)
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// todoErr answers a todo write that failed: no such todo 404, a done one 409, anything else 500.
func todoErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTodoNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, db.ErrTodoCompleted):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

// ---- writing them ----

// linkTodoSpace links a space to todoID and attaches all current online agents in that space.
func (s *Service) linkTodoSpace(ctx context.Context, todoID, mid, space string) (int, error) {
	s.todos.writing.Lock()
	now := time.Now().UTC()
	spKey := spaceKey(mid, space)

	var oldTodoID string
	// The links in an assistant space of the to-do it takes the space from stay (spaceLinksOf), in the same write: memory
	// is the store's while todoBook.writing is held.
	s.todos.mu.RLock()
	holder := s.todos.spacesByKey[spKey]
	s.todos.mu.RUnlock()
	var keep db.KeptLinks
	if holder != "" && holder != todoID {
		keep = s.spaceLinksOf(holder, mid, spKey, now)
	}
	if st := s.todoDB(); st != nil {
		var err error
		oldTodoID, err = st.LinkTodoSpace(ctx, todoID, mid, space, now, keep)
		if err != nil {
			s.todos.writing.Unlock()
			return 0, err
		}
	} else {
		s.todos.mu.RLock()
		t, ok := s.todos.todos[todoID]
		if ok {
			if oldID, has := s.todos.spacesByKey[spKey]; has && oldID != todoID {
				oldTodoID = oldID
			}
		}
		done := ok && t.DoneAt != nil
		s.todos.mu.RUnlock()
		if !ok {
			s.todos.writing.Unlock()
			return 0, db.ErrTodoNotFound
		}
		if done {
			s.todos.writing.Unlock()
			return 0, db.ErrTodoCompleted
		}
	}

	s.todos.mu.Lock()
	// Clean up from old todo if space belonged to another todo
	if oldTodoID != "" && oldTodoID != todoID {
		if oldTodoID == holder {
			s.keepLinksLocked(oldTodoID, mid, spKey, keep)
		}
		if oldT, ok := s.todos.todos[oldTodoID]; ok {
			var keptSpaces []TodoSpace
			for _, sp := range oldT.Spaces {
				if !(sp.MachineID == mid && sp.Space == space) {
					keptSpaces = append(keptSpaces, sp)
				}
			}
			oldT.Spaces = keptSpaces
			var keptAgents []TodoAgent
			for _, a := range oldT.Agents {
				if a.Source == "space" && a.Via == spKey {
					if s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}] == oldTodoID {
						delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
					}
					continue
				}
				keptAgents = append(keptAgents, a)
			}
			oldT.Agents = keptAgents
			oldT.UpdatedAt = now
		}
	}

	// Add or update in target todo
	if target := s.todos.todos[todoID]; target != nil {
		spFound := false
		for i := range target.Spaces {
			if target.Spaces[i].MachineID == mid && target.Spaces[i].Space == space {
				target.Spaces[i].LinkedAt = now
				spFound = true
				break
			}
		}
		if !spFound {
			target.Spaces = append(target.Spaces, TodoSpace{MachineID: mid, Space: space, LinkedAt: now})
		}
		s.todos.spacesByKey[spKey] = todoID
		target.UpdatedAt = now
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()

	// Link all online agents currently in this space
	linkedCount := 0
	list, _ := s.sessions(ctx, mid)
	for _, c := range list {
		if sessionSpaceKey(mid, c) != spKey || c.Status == proto.StatusOffline {
			continue
		}
		if linked, err := s.linkTodoSpaceAgent(ctx, todoID, mid, spKey, now, c); err == nil && linked {
			linkedCount++
		}
	}

	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return linkedCount, nil
}

// applyTodoRow puts a todo's row as it now is in the store into memory and lets everything that follows from done
// happen as for the user's PATCH: its agents' links count again or no longer do (sessions lose or regain the todo,
// the nodes' persona_todo tokens follow), and the apps refetch. guard, when given, is checked under the lock first
// (false: nothing changes); false also when the todo is gone.
func (s *Service) applyTodoRow(ctx context.Context, id string, row db.TodoRow, guard func(*Todo) bool) (Todo, bool) {
	out, doneKeys, ok := s.setTodoRow(id, row, guard)
	if ok {
		s.todoRowSet(ctx, doneKeys)
	}
	return out, ok
}

// setTodoRow is applyTodoRow's change to memory, without telling anyone: the todo as it is now and the agents done
// releases (their pending work is retired); not ok when the guard refused or the todo is gone. A writer holding
// todoBook.writing calls it before letting go, and todoRowSet after.
func (s *Service) setTodoRow(id string, row db.TodoRow, guard func(*Todo) bool) (Todo, [][2]string, bool) {
	s.todos.mu.Lock()
	out, keys, after, ok := s.setTodoRowLocked(id, row, guard)
	s.todos.mu.Unlock()
	if after != nil {
		after()
	}
	return out, keys, ok
}

// setTodoRowLocked applies one committed row and returns effects to run outside todos.mu.
// Topology writes apply a batch under that lock so readers see the entire new graph at once.
func (s *Service) setTodoRowLocked(id string, row db.TodoRow, guard func(*Todo) bool) (Todo, [][2]string, func(), bool) {
	t, ok := s.todos.todos[id]
	if !ok || guard != nil && !guard(t) || t.Kind == db.KindConcern && t.DoneAt == nil && row.DoneAt != nil && row.DoneBy != db.DoneByReported {
		// A concern closes only by its report (concerns.go): no other writer's row closes it in memory either.
		return Todo{}, nil, nil, false
	}
	t.Title = row.Title
	t.Notes = row.Notes
	becameDone := t.DoneAt == nil && row.DoneAt != nil
	if becameDone {
		// A stopped Agent can keep the same binding when its task is closed and later reopened.
		now := time.Now()
		for i := range t.Agents {
			if t.Agents[i].active() {
				t.Agents[i].resumeInvalidatedAt = now
			}
		}
		s.todos.pruneResumeChecksLocked(now)
	}
	reopened := t.DoneAt != nil && row.DoneAt == nil
	var reopenedEvent map[string]any
	if reopened {
		reopenedEvent = reopenedDetail(t, row) // before t changes: who had closed it, and when
	}
	t.DoneAt = row.DoneAt
	t.StartError = row.StartError
	t.DoneBy, t.DoneReason, t.ReopenedAt = row.DoneBy, row.DoneReason, row.ReopenedAt
	t.resetReopenedUserCheck()
	if reopened {
		t.Check = nil // the work it judged is over (task_check.go)
	}
	t.setPlan(row)
	t.UpdatedAt = row.UpdatedAt
	var doneKeys [][2]string
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			key := [2]string{a.MachineID, a.SessionID}
			if t.DoneAt != nil {
				if becameDone && s.todos.activeBySession[key] == t.ID {
					doneKeys = append(doneKeys, key) // its pending wake and work card are withdrawn with the todo
				}
				delete(s.todos.activeBySession, key)
			} else {
				s.todos.activeBySession[key] = t.ID
			}
		}
	}
	out := formatTodo(copyTodoLocked(t))
	closing := threadClosing(becameDone, t.StartError, t.DoneReason)
	if !reopened && closing == nil {
		return out, doneKeys, nil, true
	}
	return out, doneKeys, func() {
		if reopened {
			s.dropSourcePlan(id) // what was planned to be told may not be what it comes to now (todo_source.go)
			s.dropReopenedCheck(id)
			s.workEvent("", "", todoReopenedEvent, reopenedEvent) // a wrong "done" in the work stats (agents_work_wrong_done.go)
		}
		if closing != nil {
			s.chat().ThreadPost(id, *closing) // after the lock: it looks the todo up again
			s.sourceReportSoon(id, sourceReportWait)
		}
	}, true
}

// todoRowSet tells everyone what setTodoRow changed: the hub and the herdr marks, the apps, and the done agents'
// pending work withdrawn.
func (s *Service) todoRowSet(ctx context.Context, doneKeys [][2]string) {
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	if len(doneKeys) > 0 {
		s.retireTodoWork(doneKeys...)
	}
}

// unlinkTodoSpace unlinks a space from todoID and physically deletes source=space agents linked via this space.
func (s *Service) unlinkTodoSpace(ctx context.Context, todoID, mid, space string) error {
	s.todos.writing.Lock()
	spKey := spaceKey(mid, space)

	s.todos.mu.RLock()
	t, ok := s.todos.todos[todoID]
	spFound := false
	if ok {
		for _, sp := range t.Spaces {
			if sp.MachineID == mid && sp.Space == space {
				spFound = true
				break
			}
		}
	}
	s.todos.mu.RUnlock()
	if !spFound {
		s.todos.writing.Unlock()
		return db.ErrTodoNotFound
	}
	now := time.Now().UTC()
	keep := s.spaceLinksOf(todoID, mid, spKey, now) // they stay, in the same write

	if st := s.todoDB(); st != nil {
		if err := st.UnlinkTodoSpace(ctx, todoID, mid, space, keep); err != nil {
			s.todos.writing.Unlock()
			return err
		}
	}

	s.todos.mu.Lock()
	s.keepLinksLocked(todoID, mid, spKey, keep)
	if cur, ok := s.todos.todos[todoID]; ok {
		var keptSpaces []TodoSpace
		for _, sp := range cur.Spaces {
			if !(sp.MachineID == mid && sp.Space == space) {
				keptSpaces = append(keptSpaces, sp)
			}
		}
		cur.Spaces = keptSpaces
		delete(s.todos.spacesByKey, spKey)

		var keptAgents []TodoAgent
		for _, a := range cur.Agents {
			if a.Source == "space" && a.Via == spKey {
				if actTid, ok := s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}]; ok && actTid == todoID {
					delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
				}
				continue
			}
			keptAgents = append(keptAgents, a)
		}
		cur.Agents = keptAgents
		cur.UpdatedAt = now
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()

	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// TodoSpaceRenamed: the hub renamed a workspace (todos.space_renamed): todo_spaces and the agents' via follow; when
// the new name is already linked to another todo, the old link is void (#873).
func (s *Service) TodoSpaceRenamed(ctx context.Context, r TodoSpaceRenamed) {
	if r.Old == "" || r.New == "" || r.Old == r.New {
		return
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("rename space: todos not loaded: %v", err)
		return
	}
	s.todos.writing.Lock()
	oldKey := spaceKey(r.MachineID, r.Old)
	s.todos.mu.RLock()
	_, voided := s.todos.spacesByKey[spaceKey(r.MachineID, r.New)]
	holder := s.todos.spacesByKey[oldKey]
	s.todos.mu.RUnlock()
	// The holder's links in an assistant space stay when the rename voids the space, in the same write (the store
	// decides whether it does).
	var keep db.KeptLinks
	if holder != "" {
		keep = s.spaceLinksOf(holder, r.MachineID, oldKey, time.Now().UTC())
	}
	if st := s.todoDB(); st != nil {
		var err error
		if voided, err = st.RenameTodoSpace(ctx, r.MachineID, r.Old, r.New, keep); err != nil {
			s.todos.writing.Unlock()
			s.logf("rename space: migrate todo spaces in db from %s/%s to %s failed: %v", r.MachineID, r.Old, r.New, err)
			return
		}
	}
	if voided {
		s.logf("rename space: target space %s/%s already linked to another todo; voiding old link for %s", r.MachineID, r.New, r.Old)
		if holder != "" {
			s.todos.mu.Lock()
			s.keepLinksLocked(holder, r.MachineID, oldKey, keep)
			s.todos.mu.Unlock()
		}
		s.voidTodoSpaceInMemory(r.MachineID, r.Old)
	} else {
		s.renameTodoSpaceInMemory(r.MachineID, r.Old, r.New)
	}
	s.todos.writing.Unlock()
	if voided {
		s.linksChanged(ctx)
	}
	s.todosChanged(ctx)
}

// renameTodoSpaceInMemory carries todo_spaces and agent via over when space is renamed.
func (s *Service) renameTodoSpaceInMemory(machineID, oldSpace, newSpace string) {
	oldKey := spaceKey(machineID, oldSpace)
	newKey := spaceKey(machineID, newSpace)
	now := time.Now().UTC()

	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()

	if tid, ok := s.todos.spacesByKey[oldKey]; ok {
		delete(s.todos.spacesByKey, oldKey)
		s.todos.spacesByKey[newKey] = tid
		if t, ok := s.todos.todos[tid]; ok {
			for i := range t.Spaces {
				if t.Spaces[i].MachineID == machineID && t.Spaces[i].Space == oldSpace {
					t.Spaces[i].Space = newSpace
					break
				}
			}
			for i := range t.Agents {
				if t.Agents[i].Source == "space" && t.Agents[i].Via == oldKey {
					t.Agents[i].Via = newKey
				}
			}
			t.UpdatedAt = now
		}
	}
}

// voidTodoSpaceInMemory cleans up a space link and its space-derived agents from memory when voided due to unique conflict (#873).
func (s *Service) voidTodoSpaceInMemory(machineID, oldSpace string) {
	oldKey := spaceKey(machineID, oldSpace)
	now := time.Now().UTC()

	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()

	if tid, ok := s.todos.spacesByKey[oldKey]; ok {
		delete(s.todos.spacesByKey, oldKey)
		if t, ok := s.todos.todos[tid]; ok {
			var keptSpaces []TodoSpace
			for _, sp := range t.Spaces {
				if !(sp.MachineID == machineID && sp.Space == oldSpace) {
					keptSpaces = append(keptSpaces, sp)
				}
			}
			t.Spaces = keptSpaces
			var keptAgents []TodoAgent
			for _, a := range t.Agents {
				if a.Source == "space" && a.Via == oldKey {
					if actTid, ok := s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}]; ok && actTid == tid {
						delete(s.todos.activeBySession, [2]string{a.MachineID, a.SessionID})
					}
					continue
				}
				keptAgents = append(keptAgents, a)
			}
			t.Agents = keptAgents
			t.UpdatedAt = now
		}
	}
}

// linkTodoAgent links or reactivates an agent by hand on todoID (the user's link, a task's started agent, a hand-off).
func (s *Service) linkTodoAgent(ctx context.Context, todoID, mid, sid, title, cwd, agent string) error {
	return s.linkTodoAgentWithSource(ctx, todoID, mid, sid, title, cwd, agent, "", "manual", "")
}

// linkTodoAgentOfDoneTask links the agent of a task that started while the user finished it, to that done todo: the
// task keeps the agent (and shows it) instead of the start reading as failed, and the close loop (tasks_close.go)
// retires it. Unlike linkTodoAgent, a finished todo is not refused.
func (s *Service) linkTodoAgentOfDoneTask(ctx context.Context, todoID, mid, sid, title, cwd, agent string) error {
	return s.linkTodoAgentWithSourceAllowingDone(ctx, todoID, mid, sid, title, cwd, agent, "", "manual", "", true)
}

// linkTodoAgentWithSource links an agent with a specific source ("manual", "space", "spawned") and via.
func (s *Service) linkTodoAgentWithSource(ctx context.Context, todoID, mid, sid, title, cwd, agent, startError, source, via string) error {
	return s.linkTodoAgentWithSourceAllowingDone(ctx, todoID, mid, sid, title, cwd, agent, startError, source, via, false)
}

// linkTodoAgentWithSourceAllowingDone links the agent, refusing a todo that is done (db.ErrTodoCompleted) unless
// allowDone: agents of done tasks stay linked (tasks_close.go retires them), new ones are not attached by hand.
func (s *Service) linkTodoAgentWithSourceAllowingDone(ctx context.Context, todoID, mid, sid, title, cwd, agent, startError, source, via string, allowDone bool) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	if !allowDone {
		if err := s.lockTodoWritingWithIdleFence(ctx, mid, sid); err != nil {
			return err
		}
	} else {
		s.todos.writing.Lock()
	}
	err := s.writeTodoAgentLink(ctx, todoID, mid, sid, title, cwd, agent, startError, source, via, allowDone)
	s.todos.writing.Unlock()
	if err != nil {
		return err
	}
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// linkTodoSpaceAgent retires fanout when a newer move/unlink published while the
// session lookup was in flight. Check and write share the ownership write boundary.
func (s *Service) linkTodoSpaceAgent(ctx context.Context, todoID, mid, key string, linkedAt time.Time, c proto.Session) (bool, error) {
	if err := s.lockTodoWritingWithIdleFence(ctx, mid, c.SessionID); err != nil {
		return false, err
	}
	s.todos.mu.RLock()
	current := false
	if target := s.todos.todos[todoID]; target != nil && s.todos.spacesByKey[key] == todoID {
		for _, sp := range target.Spaces {
			if spaceKey(sp.MachineID, sp.Space) == key && sp.LinkedAt.Equal(linkedAt) {
				current = true
				break
			}
		}
	}
	alreadyLinked := s.todos.activeBySession[[2]string{mid, c.SessionID}] == todoID
	s.todos.mu.RUnlock()
	if !current || alreadyLinked {
		s.todos.writing.Unlock()
		return false, nil
	}
	err := s.writeTodoAgentLink(ctx, todoID, mid, c.SessionID, c.Title, c.Cwd, c.Agent, c.StartError, "space", key, false)
	s.todos.writing.Unlock()
	return err == nil, err
}

// writeTodoAgentLink writes and publishes a membership with todoBook.writing held.
// Callers release it before notifying the hub or contacting nodes.
func (s *Service) writeTodoAgentLink(ctx context.Context, todoID, mid, sid, title, cwd, agent, startError, source, via string, allowDone bool) error {
	now := time.Now().UTC()
	// A link keeps a directory in a space (identity.InSpace): a run's directory follows its shell's cd, and where it
	// worked tells the to-do's history as the space's for good (todosHidden, #10941).
	if cwd != "" && !identity.InSpace(cwd) {
		s.todos.mu.RLock()
		if t := s.todos.todos[todoID]; t != nil {
			for _, a := range t.Agents {
				if a.MachineID == mid && a.SessionID == sid && identity.InSpace(a.Cwd) {
					cwd = a.Cwd
				}
			}
		}
		s.todos.mu.RUnlock()
	}

	if st := s.todoDB(); st != nil {
		link := st.LinkTodoAgentWithSource
		if allowDone {
			link = st.LinkTodoAgentOfDoneTask
		}
		if _, err := link(ctx, todoID, mid, sid, title, cwd, agent, startError, source, via, now); err != nil {
			return err
		}
	} else {
		t, ok := s.todoNow(todoID)
		if !ok {
			return db.ErrTodoNotFound
		}
		if t.DoneAt != nil && !allowDone {
			return db.ErrTodoCompleted
		}
	}

	s.todos.mu.Lock()
	// mark active rows in all other todos (including completed) as released
	for oid, other := range s.todos.todos {
		if oid != todoID {
			for i := range other.Agents {
				if other.Agents[i].MachineID == mid && other.Agents[i].SessionID == sid && other.Agents[i].ReleasedAt == nil {
					other.Agents[i].ReleasedAt = &now
					other.UpdatedAt = now
				}
			}
		}
	}

	if target, ok := s.todos.todos[todoID]; ok {
		found := false
		for i := range target.Agents {
			if target.Agents[i].MachineID == mid && target.Agents[i].SessionID == sid {
				target.Agents[i].ReleasedAt = nil
				target.Agents[i].DismissedAt = nil
				target.Agents[i].Source = source
				target.Agents[i].Via = via
				target.Agents[i].LinkedAt = now
				if title != "" {
					target.Agents[i].Title = title
				}
				if cwd != "" {
					target.Agents[i].Cwd = cwd
				}
				if agent != "" {
					target.Agents[i].Agent = agent
				}
				if startError != "" {
					target.Agents[i].StartError = startError
				}
				found = true
				break
			}
		}
		if !found {
			target.Agents = append(target.Agents, TodoAgent{MachineID: mid, SessionID: sid, LinkedAt: now, Title: title, Cwd: cwd,
				Agent: agent, StartError: startError, Source: source, Via: via})
		}
		target.UpdatedAt = now
		if target.Supervision != nil && target.Supervision.Progress != nil && target.Supervision.Progress.Kind == upReportDone &&
			(target.Supervision.Progress.machine != mid || target.Supervision.Progress.session != sid) {
			v := target.Supervision.clone()
			v.Progress = nil
			target.Supervision = v
		}
		if target.DoneAt == nil { // as loadTodosLocked has it: only an open todo's agents work for it
			s.todos.activeBySession[[2]string{mid, sid}] = todoID
		}
	}
	s.todos.mu.Unlock()

	return nil
}

// failTodo ends a todo whose agent did not start: done, with why, and its agents no longer count for it.
func (s *Service) failTodo(ctx context.Context, id, reason string) error {
	_, err := s.failTodoIf(ctx, id, reason, nil)
	return err
}

// failTodoIf checks keep under the same write lock as completion and agent linking. Startup recovery must not
// turn a task the user just completed, or one whose agent was just linked, into a failed start.
func (s *Service) failTodoIf(ctx context.Context, id, reason string, keep func(Todo) bool) (bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, err
	}
	s.todos.writing.Lock() // memory takes the rows in the order the store committed them (todoBook.writing)
	if t, ok := s.todoNow(id); ok && t.isConcern() {
		s.todos.writing.Unlock()
		return false, nil // an agent of a concern not starting does not close it (concerns.go)
	}
	if keep != nil {
		t, ok := s.todoNow(id)
		if !ok || !keep(t) {
			s.todos.writing.Unlock()
			return false, nil
		}
	}
	now := time.Now().UTC()
	doneAt := now
	if st := s.todoDB(); st != nil {
		row, err := st.FailTodo(ctx, id, reason, now)
		if err != nil {
			s.todos.writing.Unlock()
			return false, err
		}
		if row.DoneAt != nil {
			doneAt = *row.DoneAt
		}
	}
	s.todos.mu.Lock()
	t := s.todos.todos[id]
	var before Todo
	var failKeys [][2]string
	var closing *larkhost.ThreadEvent
	if t != nil {
		before = copyTodoLocked(t)
		closing = threadClosing(t.DoneAt == nil, reason, "")
		if t.DoneAt == nil {
			t.DoneAt = &doneAt
		}
		t.StartError = reason
		t.UpdatedAt = now
		for _, a := range t.Agents {
			key := [2]string{a.MachineID, a.SessionID}
			if s.todos.activeBySession[key] == id {
				failKeys = append(failKeys, key) // its pending wake and work card are withdrawn with the todo
				delete(s.todos.activeBySession, key)
			}
		}
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()
	if t == nil {
		return false, db.ErrTodoNotFound
	}
	if closing != nil {
		s.chat().ThreadPost(id, *closing)
		s.sourceReportSoon(id, threadPostSettle) // no outcome is coming for a start that failed
	}
	if len(failKeys) > 0 {
		s.retireTodoWork(failKeys...)
	}
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	s.onTodoWorkspaceReleased(ctx, before, false)
	return true, nil
}

// autoDoneTodo marks todo id done by the assistant's judge when it is still open and was not reopened after work
// began; false when the user got there first (done it, reopened it), it is gone, or it is a concern (never closed on
// its own, concerns.go).
func (s *Service) autoDoneTodo(ctx context.Context, id, reason string, work time.Time) (bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, err
	}
	if t, ok := s.todoNow(id); ok && t.isConcern() {
		return false, nil
	}
	s.loadSeen(ctx)
	_ = s.loadTaskActivity(ctx)
	s.todos.writing.Lock() // no other store write lands between the store's close and memory's
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			s.todos.writing.Unlock()
		}
	}
	defer unlock()
	guard := func() error {
		if t, ok := s.todoNow(id); ok {
			if err := s.taskCloseSnapshotError(t, time.Now()); err != nil {
				return err
			}
			if t.DoneAt == nil {
				if err := s.investigationCloseError(ctx, t); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := guard(); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	var row db.TodoRow
	st := s.todoDB()
	if st != nil {
		r, ok, err := st.AutoDoneTodo(ctx, id, reason, now, work, guard)
		if errors.Is(err, db.ErrTodoNotFound) {
			return false, nil
		}
		if err != nil || !ok {
			return false, err
		}
		row = *r
	} else {
		t, ok := s.todoNow(id)
		if !ok {
			return false, nil
		}
		row = db.TodoRow{ID: t.ID, Title: t.Title, Notes: t.Notes, CreatedAt: t.CreatedAt, Origin: t.Origin, PlanID: t.PlanID,
			ReopenedAt: t.ReopenedAt}
		row.DoneAt, row.DoneBy, row.DoneReason, row.UpdatedAt = &now, db.DoneByAuto, reason, now
		t.keepPlan(&row)
	}
	var before Todo
	var activityErr error
	_, doneKeys, ok := s.setTodoRow(id, row, func(t *Todo) bool {
		if st == nil {
			if activityErr = s.taskCloseSnapshotError(*t, time.Now()); activityErr != nil {
				return false
			}
		}
		// With a store its conditional write decided it (open, not reopened since work began) and, writing held, no
		// writer that stores a row committed after it (all of them take writing): memory takes the stored row even
		// when an in-memory bump (an agent's title synced) moved updated_at past it. Memory alone has only its own
		// state to judge by.
		if st == nil && (t.UpdatedAt.After(row.UpdatedAt) || t.DoneAt != nil || t.ReopenedAt != nil && !t.ReopenedAt.Before(work)) {
			return false
		}
		before = copyTodoLocked(t) // its agents as they worked on it, before done releases them
		return true
	})
	unlock() // the notifications run without writing, as for the other writers
	if activityErr != nil {
		return false, activityErr
	}
	if !ok {
		return false, nil
	}
	s.todoRowSet(ctx, doneKeys)
	if row.DoneAt != nil {
		s.goRecordTaskOutcome(before, *row.DoneAt) // what the task came to (todo_outcome.go, #2820)
		s.onTodoWorkspaceReleased(ctx, before, true)
		s.evaluateTodoAfterDependents(ctx, id)
		s.evaluateParentCloseoutForChild(ctx, id)
	}
	return ok, nil
}

// reopenTodo reopens done todo id as the user's PATCH {done: false} does (reopened_at set: the stretch of work before
// does not close it again). Feedback also restarts an open personal-verification task and invalidates its old Pass.
func (s *Service) reopenTodo(ctx context.Context, id string) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	cur, ok := s.todoNow(id)
	if !ok {
		return db.ErrTodoNotFound
	}
	userCheck := cur.Supervision != nil && cur.Supervision.State == reviewUserCheck
	if cur.DoneAt == nil && !userCheck {
		return nil
	}
	open := false
	_, err := s.editTodo(ctx, id, db.TodoEdit{Done: &open, RestartWork: userCheck})
	return err
}

// reopenFailedTodo reopens a task whose agent did not start, for its start again (retryTask): its error cleared, the
// same todo; not ok when another assistant sharing the store reopened it first (the todo as it is then).
func (s *Service) reopenFailedTodo(ctx context.Context, id string) (Todo, bool, error) {
	cur, ok, err := s.todo(ctx, id)
	if err != nil {
		return Todo{}, false, err
	}
	if !ok {
		return Todo{}, false, db.ErrTodoNotFound
	}
	if err := s.lockTodoWritingForSessions(ctx, todoProtectedSessions(cur, nil)); err != nil {
		return Todo{}, false, err
	}
	now := time.Now().UTC()
	row := db.TodoRow{ID: cur.ID, Title: cur.Title, Notes: cur.Notes, CreatedAt: cur.CreatedAt, UpdatedAt: now,
		Origin: cur.Origin, PlanID: cur.PlanID, ReopenedAt: &now}
	cur.keepPlan(&row)
	row.SomedayUntil = nil // retried: out of Someday, as db.RetryTodo does on the store
	row.SeenAt, row.AutoSeenUndone = nil, false
	if st := s.todoDB(); st != nil {
		r, ok, err := st.RetryTodo(ctx, id, now)
		if err != nil {
			s.todos.writing.Unlock()
			return Todo{}, false, err
		}
		if !ok {
			s.todos.writing.Unlock()
			cur, _ := s.todoNow(id)
			return formatTodo(cur), false, nil
		}
		row = *r
	}
	out, doneKeys, applied := s.setTodoRow(id, row, nil)
	s.todos.writing.Unlock()
	if applied {
		s.todoRowSet(ctx, doneKeys)
	}
	s.resetTodoReadyWake(ctx, id)
	s.resetParentCloseoutWake(ctx, id)
	s.resolveCard(taskFailedCardID(id)) // started again: the failure's push is over
	return out, true, nil
}

// ---- what the hub asks about them ----

// TodoHandoff: an agent is handed work for todo h.TodoID (assistant.todo_handoff, POST /api/v1/sessions or
// …/messages with todo_id): the todo as it is, whether the hand-off may go (no such todo: 404, a done one: 409, the
// words the hub answers with), and the text with the todo's and its actual continuation ancestors' decisions in
// effect before it (#1618) and, for a new agent, its project's background before those (#2820); a blank text stays as it is.
func (s *Service) TodoHandoff(ctx context.Context, h TodoHandoff) (TodoHandoffResult, error) {
	t, ok, err := s.todo(ctx, h.TodoID)
	if err != nil {
		return TodoHandoffResult{}, err
	}
	if !ok {
		return TodoHandoffResult{Text: h.Text, Status: http.StatusNotFound, Error: "todo not found"}, nil
	}
	text := h.Text
	if strings.TrimSpace(text) != "" {
		var context []string
		if h.New {
			lead := ""
			if t.Supervision != nil {
				lead = t.Supervision.Supervisor
			}
			if background := s.projectBackgroundForTodo(ctx, h.MachineID, h.Cwd, lead, t, text); background != "" {
				context = append(context, background)
			}
		}
		if decisions := s.todoHandoffDecisionsContext(ctx, h.TodoID); decisions != "" {
			context = append(context, decisions)
		}
		text = agentprompt.WithContext(h.Agent, text, strings.Join(context, "\n\n"))
	}
	res := TodoHandoffResult{Found: true, Title: t.Title, DoneAt: t.DoneAt, Text: text}
	if t.DoneAt == nil && len(t.Images) > 0 {
		images, err := s.todoImageContents(ctx, t)
		if err != nil {
			return TodoHandoffResult{}, err
		}
		res.Images = todoProtoImages(images)
	}
	if t.DoneAt != nil {
		res.Status, res.Error = http.StatusConflict, "todo is already completed"
	}
	return res, nil
}

// TodoLink: the agent handed work for a todo works for it from now on (assistant.todo_link); the todo's title.
// Without a title the link's comes from the prompt the agent was started with.
func (s *Service) TodoLink(ctx context.Context, p TodoLinkParams) (TodoLinkResult, error) {
	title := p.Title
	if title == "" {
		title = cleanPromptTitle(p.Prompt)
	}
	if err := s.linkTodoAgent(ctx, p.TodoID, p.MachineID, p.SessionID, title, p.Cwd, p.Agent); err != nil {
		return TodoLinkResult{}, err
	}
	s.superviseLinkedTodo(ctx, p.TodoID) // the user's own todo gets a supervisor (agent_tasks.go)
	res := TodoLinkResult{Title: title}
	if t, ok, err := s.todo(ctx, p.TodoID); err == nil && ok {
		res.Title = t.Title
	}
	return res, nil
}

// contextLeadRe and contextCloseRe are where a prompt's context block starts and ends (contextOpenRe: channels_plugin.go).
var (
	contextLeadRe  = lazyre.New(`^<` + compat.ContextPattern() + `\b`)
	contextCloseRe = lazyre.New(`</` + compat.ContextPattern() + `>`)
)

// cleanPromptTitle is a title for an agent from the prompt it was started with: its first line past a
// context block (<persona-context>, as older ones were, #3182).
func cleanPromptTitle(prompt string) string {
	s := strings.TrimSpace(prompt)
	if contextLeadRe.MatchString(s) {
		if loc := contextCloseRe.FindStringIndex(s); loc != nil {
			s = s[loc[1]:]
		} else if endTagIdx := strings.Index(s, ">"); endTagIdx != -1 {
			s = s[endTagIdx+1:]
		} else {
			s = ""
		}
	}
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !contextOpenRe.MatchString(trimmed) {
			return trimmed
		}
	}
	return ""
}

// threadClosing is what the topic is told when a todo became done (nil when it did not): why, when the hub's judge
// closed it or its agent never started.
func threadClosing(becameDone bool, startError, doneReason string) *larkhost.ThreadEvent {
	switch {
	case !becameDone:
		return nil
	case startError != "":
		return &larkhost.ThreadEvent{Kind: larkhost.ThreadTodoFailed, Text: clip(oneLine(startError), 80)}
	}
	return &larkhost.ThreadEvent{Kind: larkhost.ThreadTodoDone, Text: clip(oneLine(doneReason), 80)}
}
