package modapi

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/wireids"
)

// FeedKindBrief is the chat feed item kind for a routine briefing's fixed facts.
const FeedKindBrief = "brief"

// Ports is the bundle of narrow core ports the host hands to loaded modules through Host.
// Each field is as narrow as its callers need and may be nil when the host is created without runtime services.
type Ports struct {
	Tasks    TaskPort
	Todos    TodoPort
	Inbox    InboxPort
	Later    LaterPort
	Schedule SchedulePort
	Feed     FeedPort
	Delivery DeliveryPort
	DayRecap DayRecapPort
	Prompts  PromptPort
}

// TaskPort reads task/board/direction/metric state and weekly setup suggestions from the core.
type TaskPort interface {
	BotLang(ctx context.Context) string
	WebBase(ctx context.Context) string
	Location(ctx context.Context) *time.Location
	FleetReady() bool
	FeishuPushesOn(ctx context.Context) bool
	BoardRows(ctx context.Context, lang, web string, now time.Time) []BriefBoardRow
	AskCopy(todoID, lang string) (BriefAsk, bool)
	TodayFinished(ctx context.Context, since time.Time, loc *time.Location) []BriefDone
	TodayFailures(ctx context.Context, since time.Time, loc *time.Location) ([]BriefFailure, int)
	NorthStar(ctx context.Context, now time.Time, loc *time.Location, lang string) BriefNorthStar
	Confirmations(ctx context.Context, now time.Time, loc *time.Location, lang string) BriefConfirmations
	Leaders(ctx context.Context, now time.Time) []BriefLeader
	Directions(ctx context.Context, now time.Time, lang, web string) ([]BriefDirection, []BriefDisabledRoutine)
	Captures(ctx context.Context, lang string, since time.Time) []string
	TaskBriefDigest(ctx context.Context, now time.Time, loc *time.Location, markTold bool) (open, done []BriefTask)
	SetupFor(ctx context.Context, now time.Time, loc *time.Location, lang, web string) *BriefSetup
	SetupTold(setup *BriefSetup, at time.Time)
}

// TodoPort reads open progress to-dos, to-do titles and stale post-closure PR follow-ups from the core.
type TodoPort interface {
	OpenProgressTodos(ctx context.Context) []BriefTodoProgress
	TodoTitle(id string) (string, bool)
	StalePRFollowups(ctx context.Context, at time.Time, lang string) []BriefPRFollowup
	DeferredSupervision(ctx context.Context, at time.Time) []BriefDeferredSupervision
	SupervisionBriefed(ctx context.Context, ids []string, at time.Time)
}

// InboxPort reads open follow-up Inbox signals for the model-only briefing sections.
type InboxPort interface {
	FollowingSignals(ctx context.Context) []BriefInboxSignal
}

// LaterPort reads deferred questions and marks a question handled when its button is pressed.
type LaterPort interface {
	Handle(ctx context.Context, id string) (bool, error)
	Rows(ctx context.Context, lang, web string, now time.Time, limit int) ([]BriefBoardRow, int)
}

// DayRecapPort reads today's all-day recordings and recaps for the evening routine.
type DayRecapPort interface {
	TodaysRecordings(ctx context.Context, now time.Time, loc *time.Location) []BriefDayRecording
}

// SchedulePort exposes narrow schedule checks, fallback receipt persistence and retry scheduling.
type SchedulePort interface {
	// MorningFactsDue says whether the morning routine with facts on is enabled, daily, has a valid HH:MM time and
	// runs on weekday (1 = Monday … 7 = Sunday).
	MorningFactsDue(ctx context.Context, weekday int) bool
	// ClaimFallbackReceipt persists key before any fallback channel sends; false when already claimed or persistence fails.
	ClaimFallbackReceipt(ctx context.Context, key string) bool
	// ResolveFallbackTarget resolves the conversation and device for a pre-delivery fallback.
	ResolveFallbackTarget(ctx context.Context, task BriefTaskInfo, run BriefScheduleRun) (sessionID, machineID, machineName string, ok bool)
	// RetryNotStarted schedules the automatic one-shot retry for a failed Facts/Report routine start.
	RetryNotStarted(taskID string, failed BriefScheduleRun)
}

// FeedPort writes feed items into an assistant conversation.
type FeedPort interface {
	Append(ctx context.Context, session, kind, ref string, body any) error
	AppendOnce(ctx context.Context, id, session, kind, ref string, body any) error
}

// DeliveryPort delivers briefing fallbacks and notifies acceptance holds when a briefing reaches the owner.
type DeliveryPort interface {
	SendFallback(ctx context.Context, req BriefFallbackSend)
	MarkListed(f *BriefSnapshot)
}

// BriefCardPage pairs a physical card with its own plain-text substitute.
type BriefCardPage struct {
	Card     *assistantwire.BotCard
	Fallback string
}

// PromptPort renders briefing prompts, counts runes, formats board rows and stages, strips context tags and translates i18n keys.
type PromptPort interface {
	RenderBriefFacts(BriefFacts) string
	RenderNorthStarFacts(lines []string) string
	BriefDayRunes(day *BriefDay) int
	BoardRowMarkdown(lang string, n int, row BriefBoardRow) string
	OpenStageBar(lang, stage string) string
	BoardRowPlain(lang string, n int, row BriefBoardRow) string
	Translate(lang, key string, args map[string]any) string
	StripContext(text string) string
}

// ScheduleFactsRunner is the briefing module's runtime lifecycle for scheduled routines with facts or reports.
type ScheduleFactsRunner interface {
	// RenderCards builds the delivery pages, complete plain notification and IDs with visible Accept buttons.
	RenderCards(f *BriefSnapshot, label, answer, header, openLabel, openURL string) ([]BriefCardPage, string, []string)
	Start(ctx context.Context, task BriefTaskInfo, now time.Time) string
	StartReport(ctx context.Context, task BriefTaskInfo, now time.Time) string
	Sent(BriefScheduleRun)
	HasSession(session string) bool
	IsFailure(session, asked string, cut bool) bool
	Claim(session, asked, turn string, cut bool) (run *BriefRun, claimed bool)
	Push(session, lang string, report bool) string
	Reached(session string)
	Fallback(ctx context.Context, run BriefScheduleRun, task BriefTaskInfo, device BriefDevice)
	NotStarted(ctx context.Context, task BriefTaskInfo, failed BriefScheduleRun, retry bool)
	Grace(ctx context.Context, local time.Time, start int) int

	// Pure/point-in-time helpers exposed for core card rendering and integration tests.
	FactsAt(ctx context.Context, now time.Time, self string) *BriefSnapshot
	ModelText(f *BriefSnapshot) string
	CountsIn(f *BriefSnapshot, lang string) string
	Body(f *BriefSnapshot, title string) BriefBody
	Day(ctx context.Context, now time.Time, loc *time.Location) *BriefDay
	Following(items []BriefInboxSignal, now time.Time, loc *time.Location) (waiting, due []BriefFollow)
	BoundDay(d *BriefDay)

	// Retained-run inspection and test hooks (thread-safe).
	SessionRun(session string) *BriefRun
	SetSessionRun(session string, run *BriefRun)
	SetTaskRun(taskID string, run *BriefRun)
	TaskRunCount() int
}

// ---- Neutral briefing prompt types (aliased by internal/prompts/assistant.go) ----

// BriefFacts are the facts of a morning or evening routine's run, as the model is given them (assistant/routine_facts):
// the lines are ready to read; the totals count more than the lines when the lists were cut.
type BriefFacts struct {
	DeferredSupervisionHeading string
	DeferredSupervision        []string

	NorthStar                                                        []string
	ConfirmationSummary                                              string
	Confirmations                                                    []string
	Hours                                                            int
	WaitingTotal, DoneTotal, FailedTotal, AcceptTotal, ProgressTotal int
	WaitingUnknown                                                   bool
	Card                                                             bool
	LaterTotal                                                       int
	Waiting                                                          []string
	Done                                                             []string
	Failed                                                           []BriefFailed
	Accept                                                           []BriefAccept
	Progress                                                         []BriefProgress
	InboxWaiting, InboxDue                                           []BriefFollow
	Leaders                                                          []BriefLeader
	Directions                                                       []BriefDirection
	DisabledRoutines                                                 []BriefDisabledRoutine
	Briefs, BriefsDone                                               []BriefTask
	Day                                                              *BriefDay
	Setup                                                            []string
	Captures                                                         []string
}

// BriefDay is today's all-day recording in the evening routine's facts (#4741), the day's parts merged by time.
type BriefDay struct {
	Date                                     string
	From, To                                 string
	Transcribed                              string
	Gaps                                     []BriefDayGap
	GapReasonsLeft, GapsLeft, GapMinutesLeft int
	Parts                                    []BriefDayPart
	PartsLeft                                int
	Timeline                                 []BriefDayEntry
	Left                                     int
	Actions                                  []BriefDayAction
	ActionsLeft                              int
}

// BriefDayGap is the time of one reason with no text.
type BriefDayGap struct {
	Reason         string
	Count, Minutes int
	Ranges         []string
	More           bool
}

// BriefDayPart is one part of the day's recording/recap.
type BriefDayPart struct {
	Part           int
	Made, Upto     string
	Final, Partial bool
	Summary        string
	State          string
	Recorded       int
}

// BriefDayEntry is one entry of the day's timeline, without its points.
type BriefDayEntry struct {
	From, To string
	Meeting  bool
	Title    string
}

// BriefDayAction is a suggested to-do not confirmed yet.
type BriefDayAction struct{ Who, What string }

// BriefTask is one piece of work in the briefing, told from its brief.
type BriefTask struct {
	Date, Words, By, Goal string
	Now, Need             string
	Status                string
	Decide, Overdue       bool
	Cancelled             bool
	Parked                string
	Scope, AlsoDone       []string
	Result                []BriefCriterion
}

// BriefCriterion is one criterion's result in the briefing.
type BriefCriterion struct{ Criterion, State, Change string }

// BriefLeader is one direction in the briefing's facts.
type BriefLeader struct {
	Name                     string
	Open, Waiting, Stuck     int
	Reached, Doing, NeedsYou string
}

// BriefDirection is one direction's open-work count and deterministic wait rows.
type BriefDirection struct {
	ID, Name, Link             string
	Owner                      bool
	Open, Active, Undispatched int
	Waits                      []BriefWait
	More                       int
}

// BriefWait is one stopped task or unanswered card.
type BriefWait struct {
	TodoID, InteractionID string
	Title, Kind           string
	Since                 time.Time
	Until                 time.Time
	WaitingOn, Reason     string
	Link, Line            string
}

// BriefDisabledRoutine is a disabled daily report or patrol.
type BriefDisabledRoutine struct {
	ID, Title, Direction, Kind string
	DisabledAt                 *time.Time
	Link, Line                 string
}

// BriefFailed is one thing that did not go through for the model prompt.
type BriefFailed struct {
	Title, Why   string
	Run, Skipped bool
}

// BriefAccept is a task waiting to be verified, with what it changed.
type BriefAccept struct{ Line, Changed string }

// BriefFollow is one Inbox matter in the briefing's model-only facts.
type BriefFollow struct{ Title, Who, Next string }

// BriefProgress is a task being worked on and its stage, for the model prompt.
type BriefProgress struct{ Title, Stage string }

// ---- Neutral briefing runtime & feed wire types ----

// BriefBoardRow is one waiting card, task to verify, or later question row.
type BriefBoardRow struct {
	Blocking bool
	Waited   time.Duration
	ID       string
	Summary  string
	From     string
	Link     string
	Nudged   bool
	Machine  string
	Session  string
	Asked    time.Time

	Detail, EvidenceLink string
}

// BriefAsk is the briefing's read-only view of an acceptance ask for a to-do.
type BriefAsk struct {
	Changed       string
	Conclusion    bool
	Outcome       string
	Recommend     string
	Verdict       string
	RecommendText string
	Decide        bool
}

// BriefDone is one task closed in the briefing window.
type BriefDone struct {
	ID    string
	Title string
	At    time.Time
}

// BriefFailure is one thing that did not go through overnight (wire JSON matches todayFailed).
type BriefFailure struct {
	Kind    string    `json:"kind"`
	ID      string    `json:"id"`
	Ref     string    `json:"ref,omitempty"`
	Title   string    `json:"title"`
	At      time.Time `json:"at"`
	Why     string    `json:"why,omitempty"`
	Skipped bool      `json:"skipped,omitempty"`
}

// BriefTaskProgress is one task being worked on, with its stage.
type BriefTaskProgress struct{ ID, Title, Stage, StageKind string }

// BriefTodoProgress is one open to-do examined for the briefing's in-progress section.
type BriefTodoProgress struct {
	ID        string
	Title     string
	Stage     string
	StageKind string
	CreatedAt time.Time
	Active    bool
}

// BriefNorthStar is the briefing's north star numbers and ready-to-read lines.
type BriefNorthStar struct {
	Lines                  []string `json:"lines"`
	Interruptions          *int     `json:"interruptions"`
	InterruptionsYesterday *int     `json:"interruptions_yesterday"`
	Partial                bool     `json:"partial,omitempty"`
	Availability           *float64 `json:"availability"`
	AvailabilityYesterday  *float64 `json:"availability_yesterday"`
	Attempts               int      `json:"attempts"`
	OK                     int      `json:"ok"`
	HasStore               bool     `json:"-"`
}

// BriefConfirmationCount matches db.ConfirmationCount on the wire.
type BriefConfirmationCount struct {
	MachineID      string `json:"machine_id"`
	SessionID      string `json:"session_id"`
	TodoID         string `json:"todo_id,omitempty"`
	Kind           string `json:"kind"`
	SessionTitle   string `json:"session_title,omitempty"`
	UserNotified   int    `json:"user_notified"`
	AutoAuthorized int    `json:"auto_authorized"`
}

// BriefConfirmations is the confirmation metrics summary (wire JSON matches briefConfirmations).
type BriefConfirmations struct {
	Unknown        bool                     `json:"unknown,omitempty"`
	Since          time.Time                `json:"since"`
	UserNotified   int                      `json:"user_notified"`
	AutoAuthorized int                      `json:"auto_authorized"`
	Sources        []BriefConfirmationCount `json:"sources"`
	Summary        string                   `json:"-"`
	Lines          []string                 `json:"-"`
}

// BriefSetupSuggestion is one mechanism named in the weekly recommended-setup block (wire JSON matches setupSuggestion).
type BriefSetupSuggestion struct {
	ID      string `json:"id"`
	Setting string `json:"setting"`
	Count   int    `json:"count"`
	Minutes int    `json:"minutes,omitempty"`
	Text    string `json:"text"`
	Link    string `json:"link,omitempty"`
}

// BriefSetup is the weekly recommended-setup block.
type BriefSetup struct {
	Week        string
	Suggestions []BriefSetupSuggestion
}

// Lines are the setup block's lines as the model is given them.
func (b *BriefSetup) Lines() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.Suggestions))
	for _, sg := range b.Suggestions {
		out = append(out, sg.Text)
	}
	return out
}

// BriefInboxSignal is one Inbox item as the briefing's follow-up filter reads it.
type BriefInboxSignal struct {
	ID           string
	Title        string
	Now          string
	Open         bool
	WaitingSince *time.Time
	CheckAt      *time.Time
	Who          string
	Next         string
}

// BriefDayCoverageGap is one coverage gap in a day recording.
type BriefDayCoverageGap struct {
	From, To float64
	Reason   string
}

// BriefDayTimelineEntry is one entry in a day recap's timeline.
type BriefDayTimelineEntry struct {
	From, To float64
	Meeting  bool
	Title    string
}

// BriefDayRecapAction is one suggested action item in a day recap.
type BriefDayRecapAction struct {
	At     float64
	Who    string
	What   string
	TodoID string
}

// BriefDayRecap is one part's parsed day recap.
type BriefDayRecap struct {
	GeneratedAt time.Time
	CoversTo    float64
	Final       bool
	Partial     bool
	Summary     string
	Timeline    []BriefDayTimelineEntry
	Actions     []BriefDayRecapAction
}

// BriefDayRecording is one of today's all-day recordings, with its span and optional recap.
type BriefDayRecording struct {
	ID          string
	Part        int
	StartedAt   time.Time
	Duration    float64
	Chars       int
	RecapState  string
	SpanStart   time.Time
	SpanEnd     time.Time
	HasCoverage bool
	CoverageTo  float64
	Gaps        []BriefDayCoverageGap
	Recap       *BriefDayRecap
}

// BriefPRFollowup is one stale or unwatched post-closure PR subscription.
type BriefPRFollowup struct {
	TodoID         string     `json:"todo_id"`
	Title          string     `json:"title"`
	PRURL          string     `json:"pr_url"`
	State          string     `json:"state,omitempty"`
	WatchStatus    string     `json:"watch_status,omitempty"`
	WatchReason    string     `json:"watch_reason,omitempty"`
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
	WatchUpdatedAt *time.Time `json:"watch_updated_at,omitempty"`
	Note           string     `json:"note,omitempty"`
}

// BriefSupervisionDecisionID identifies one persisted supervision verdict.
type BriefSupervisionDecisionID string

// BriefDeferredSupervision is a saved signal the supervisor can review in a briefing.
type BriefDeferredSupervision struct {
	ID     BriefSupervisionDecisionID `json:"id"`
	TodoID wireids.TodoID             `json:"todo_id"`
	Title  string                     `json:"title"`
	Reason string                     `json:"reason"`
}

// BriefSnapshot is what one run of a routine with facts on says, as it was when the run started.
type BriefSnapshot struct {
	DeferredSupervision []BriefDeferredSupervision

	Contributions     []RoutineContribution
	At                time.Time
	NorthStar         BriefNorthStar
	NorthStarLines    []string
	Confirmations     BriefConfirmations
	ConfirmationLines []string
	Lang              string
	Web               string
	Loc               *time.Location
	Waiting           []BriefBoardRow
	Accept            []BriefBoardRow
	Later             []BriefBoardRow
	LaterTotal        int
	WaitingUnknown    bool
	Card              bool
	Done              []BriefDone
	DoneTotal         int
	Failed            []BriefFailure
	FailedTotal       int
	Progress          []BriefTaskProgress
	ProgressTotal     int
	Changed           map[string]string
	Asks              map[string]BriefAsk
	Leaders           []BriefLeader
	Directions        []BriefDirection
	DisabledRoutines  []BriefDisabledRoutine
	InboxWaiting      []BriefFollow
	InboxDue          []BriefFollow
	Briefs            []BriefTask
	BriefsDone        []BriefTask
	Day               *BriefDay
	Setup             *BriefSetup
	Captures          []string
}

// BriefRun is one retained routine snapshot, kept until its turn is mirrored and pushed.
type BriefRun struct {
	Facts        *BriefSnapshot
	Label        string
	FallbackOnly bool
	MirrorTurn   string
	Pushed       bool
}

// BriefTaskInfo is the narrow slice of a scheduled Task the briefing needs.
type BriefTaskInfo struct {
	Contributions []RoutineContribution
	ID            string
	Label         string
	Facts         bool
	Report        bool
	SessionID     string
	RunSessionID  string
	MachineID     string
}

// BriefScheduleRun is the narrow slice of a ScheduleRun the briefing needs.
type BriefScheduleRun struct {
	ID        string
	RefID     string
	SessionID string
	MachineID string
	Due       time.Time
	Retried   bool
	Manual    bool
}

// BriefDevice is the narrow slice of a Device the briefing fallback needs.
type BriefDevice struct {
	ID   string
	Name string
}

// BriefFallbackSend carries the resolved fallback payload to the core's card/notification delivery port.
type BriefFallbackSend struct {
	Run    BriefScheduleRun
	Task   BriefTaskInfo
	Device BriefDevice
	Label  string
	Header string
	ID     string
	Facts  *BriefSnapshot
}

// ---- Wire JSON payload of a `brief` chat feed item (byte-identical to historical briefBody) ----

// BriefBody is a `brief` item's JSON body.
type BriefBody struct {
	DeferredSupervision []BriefDeferredSupervision `json:"deferred_supervision,omitempty"`

	Contributions    []RoutineContribution  `json:"contributions,omitempty"`
	NorthStar        BriefBodyNorthStar     `json:"north_star"`
	Confirmations    BriefConfirmations     `json:"confirmations"`
	Title            string                 `json:"title"`
	At               time.Time              `json:"at"`
	Hours            int                    `json:"hours"`
	WaitingUnknown   bool                   `json:"waiting_unknown,omitempty"`
	Waiting          []BriefBodyRow         `json:"waiting"`
	Done             []BriefBodyDone        `json:"done"`
	DoneTotal        int                    `json:"done_total"`
	Failed           []BriefFailure         `json:"failed"`
	FailedTotal      int                    `json:"failed_total"`
	Accept           []BriefBodyRow         `json:"accept"`
	Later            []BriefBodyRow         `json:"later,omitempty"`
	LaterTotal       int                    `json:"later_total,omitempty"`
	Progress         []BriefBodyProgress    `json:"progress"`
	ProgressTotal    int                    `json:"progress_total"`
	Directions       []BriefDirection       `json:"directions,omitempty"`
	DisabledRoutines []BriefDisabledRoutine `json:"disabled_routines,omitempty"`
	Notice           string                 `json:"notice,omitempty"`
	Setup            []BriefSetupSuggestion `json:"setup,omitempty"`
	PRFollowups      []BriefPRFollowup      `json:"pr_followups,omitempty"`
}

// BriefBodyNorthStar is a `brief` item's north star JSON payload.
type BriefBodyNorthStar struct {
	Lines                  []string `json:"lines"`
	Interruptions          *int     `json:"interruptions"`
	InterruptionsYesterday *int     `json:"interruptions_yesterday"`
	Partial                bool     `json:"partial,omitempty"`
	Availability           *float64 `json:"availability"`
	AvailabilityYesterday  *float64 `json:"availability_yesterday"`
	Attempts               int      `json:"attempts"`
	OK                     int      `json:"ok"`
}

// BriefBodyRow is a row of what waits in a `brief` item.
type BriefBodyRow struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail,omitempty"`
	From      string    `json:"from,omitempty"`
	Link      string    `json:"link,omitempty"`
	WaitedS   int64     `json:"waited_s"`
	Nudged    bool      `json:"nudged,omitempty"`
	AskedAt   time.Time `json:"asked_at,omitzero"`
	Verdict   string    `json:"verdict,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	Recommend string    `json:"recommend,omitempty"`
	MachineID string    `json:"machine_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`

	EvidenceLink string `json:"evidence_link,omitempty"`
}

// BriefBodyDone is a task closed in the window in a `brief` item.
type BriefBodyDone struct {
	ID    string    `json:"id"`
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// BriefBodyProgress is a task being worked on in a `brief` item.
type BriefBodyProgress struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Stage     string `json:"stage,omitempty"`
	StageKind string `json:"stage_kind,omitempty"`
}
