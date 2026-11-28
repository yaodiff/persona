package prompts

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/modapi"
)

type AssistantSelectedFile struct{ Name, Path string }

func RenderAssistantSelectedFiles(files []AssistantSelectedFile, language string) string {
	quoted := make([]AssistantSelectedFile, len(files))
	for i, file := range files {
		quoted[i] = AssistantSelectedFile{Name: quoteSelectedFileValue(file.Name), Path: quoteSelectedFileValue(file.Path)}
	}
	return render("assistant/selected_files", struct {
		Files    []AssistantSelectedFile
		Language string
	}{Files: quoted, Language: language})
}

func quoteSelectedFileValue(value string) string {
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value) // Encoding a string into a strings.Builder cannot fail.
	// A literal '<' could open or close a prompt tag. Keep it JSON-escaped inside the value.
	return strings.ReplaceAll(strings.TrimSuffix(encoded.String(), "\n"), "<", `\u003c`)
}

type AssistantSelectedSkill struct {
	Name, Path, Ref string
}

func RenderAssistantSelectedSkills(skills []AssistantSelectedSkill) string {
	return render("assistant/selected_skills", skills)
}

// AssistantMachine is one computer in the assistant's CLAUDE.md, as seen from where the assistant runs.
type AssistantMachine struct {
	Name, ID string
	Local    bool   // the machine the assistant runs on
	SSHHost  string // "" uses ID
	// AgentsOnly: no command reaches it from where the assistant runs (the ssh aliases are the hub machine's, and a
	// link or tailcat machine has none), only the Agents tools do.
	AgentsOnly bool
}

// AssistantClaudeMD is the assistant directory's CLAUDE.md (assistant/claude_md).
type AssistantClaudeMD struct {
	WorkGuide    bool
	Owner        string // "" is "user"
	Machines     []AssistantMachine
	Notes        string // Settings › Assistant › About me, trimmed
	ExtraDirs    []string
	StateDir     string
	KnowledgeDir string
	OnHub        bool
	// LarkCLI: hub.json configures lark-cli (docs/plans/plan-lark-cli.md), on the assistant's PATH.
	LarkCLI bool
	// Language is the language the user reads, by its own name ("English", or Japanese written in Japanese); "" is Chinese, as before i18n.
	Language string
	// BackgroundSupervisor: the background supervisor follows up the tasks no leader supervises (#4927), so the main
	// conversation is told it no longer reviews them itself. False keeps the text as it was.
	BackgroundSupervisor bool
	// PhoneTools reports a real device phone_* tool other than calendar, whose query guidance is gated separately.
	// Always-available write-queue tools and a lone calendar tool do not enable unrelated iPhone procedures.
	PhoneTools bool
	// PhoneCalendar keeps calendar-specific guidance conditional on that device tool being available.
	PhoneCalendar bool
	// PhoneCalendarWrite keeps the exact-occurrence safeguards when only calendar update/delete tools are mounted.
	PhoneCalendarWrite bool
	// FullViewGuide retains the complete rich-view procedure for older nodes and nodes where the managed
	// persona-views skill is not ready. The default is the compact, on-demand form.
	FullViewGuide bool
	// UrgentCall: the owner turned urgent calls on (docs/assistant/urgent-call.md): when to call and the "what counts
	// as urgent" rules. nil adds nothing.
	UrgentCall *UrgentCall
}

// UrgentCall is the urgent-call section of the assistant's instructions.
type UrgentCall struct {
	Rules []UrgentCallRule
}

// UrgentCallRule is one "what counts as urgent" rule: its id (urgent_call's rule) and its words.
type UrgentCallRule struct {
	ID   string
	Text string
}

// urgentRuleTemplates are the built-in rules' definitions, used while the owner has not reworded them.
var urgentRuleTemplates = map[string]string{
	"money_loss":     "assistant/urgent_rule_money_loss",
	"harm":           "assistant/urgent_rule_harm",
	"agent_disaster": "assistant/urgent_rule_agent_disaster",
}

// UrgentRuleBuiltin is built-in rule id's definition in Chinese ("" for an id that is not one).
func UrgentRuleBuiltin(id string) string { return UrgentRuleBuiltinIn(id, "") }

// UrgentRuleBuiltinIn is built-in rule id's definition in language, the owner's by its own name ("English", or
// Japanese written in Japanese); "" is Chinese.
func UrgentRuleBuiltinIn(id, language string) string {
	name, ok := urgentRuleTemplates[id]
	if !ok {
		return ""
	}
	return strings.TrimSpace(render(name, struct{ Language string }{language}))
}

func RenderAssistantClaudeMD(d AssistantClaudeMD) string { return render("assistant/claude_md", d) }

// AssistantHealthClaudeMD is the health space's CLAUDE.md (docs/assistant/health.md).
type AssistantHealthClaudeMD struct {
	Owner    string // "" is "user"
	Notes    string // Settings › Assistant › About me, trimmed
	StateDir string
	// HealthDir is the health space's directory name in StateDir: "" is the built-in manager's "health", another's
	// "health-<id>" (identity.HealthDirName).
	HealthDir      string
	Language       string
	Water          bool // The owner's added intake items; false when the Health module is absent or unavailable.
	Strength       bool
	Protein        bool
	ItemsAvailable bool // The module can add items, including when neither intake item is added yet.
}

func RenderAssistantHealthClaudeMD(d AssistantHealthClaudeMD) string {
	return render("assistant/health_claude_md", d)
}

// AssistantGuestClaudeMD is a guest conversation's CLAUDE.md (docs/assistant/assistant.md ("Guest conversations")).
type AssistantGuestClaudeMD struct {
	Owner    string // An empty name uses the generic user label.
	Language string
}

func RenderAssistantGuestClaudeMD(d AssistantGuestClaudeMD) string {
	return render("assistant/guest_claude_md", d)
}

type AssistantViewRepair struct {
	Errors []string
}

func RenderAssistantViewRepair(d AssistantViewRepair) string {
	return render("assistant/view_repair", d)
}

// SnapshotMachine, SnapshotAgent… are the parts of the live snapshot (assistant/snapshot).
type SnapshotMachine struct {
	Name, ID string
	Online   bool
}

// SnapshotApp is the Persona app on one kind of device as it last said its version (assistant/snapshot_app). No
// time: the node sends only the snapshot's changed lines, and a reconnect must not make this one change.
// Platform: ios | ipados | watchos | macos | visionos | android | harmony.
type SnapshotApp struct {
	Platform, Version, Build, OS string // Build, OS: "" when not said
	Simulator                    bool
}

type SnapshotAgent struct {
	Label, Machine, Session string             // Session: the short id
	Line                    string             // what it waits for, or what it concluded ("" when not known)
	Link                    string             // its page in the hub's web app ("" when the hub has no public_url)
	Ultracode               string             // "on" / "off"; empty when the node does not know
	Plan                    *SnapshotAgentPlan // its own task list (Session.Plan), nil when it keeps none
	// Closeout: what its closeout says of merging and deploying, nil when it wrote none or says neither.
	Closeout *SnapshotAgentCloseout
}

// SnapshotAgentCloseout is the tail of an agent's line (assistant/snapshot_closeout): Merged and Deployed as the
// agent claimed them ("yes", "no", "" when unsaid), FoundMerged and FoundDeployed what the hub found where that
// differs from the claim ("" where it agrees or could not tell).
type SnapshotAgentCloseout struct {
	Merged, Deployed           string
	FoundMerged, FoundDeployed string
}

// SnapshotAgentPlan is an agent's own task list (TodoWrite / TaskCreate, docs/integrations/agent-goal.md "Plan"): Done of Total
// items completed, and the one in progress ("" when none is).
type SnapshotAgentPlan struct {
	Done, Total int
	Current     string
}

type SnapshotChat struct {
	Name    string
	Unread  int
	Preview string // @me chats: the clipped preview
}

type SnapshotChats struct {
	AtMe, Other []SnapshotChat
}

func (c SnapshotChats) Total() int { return len(c.AtMe) + len(c.Other) }

type SnapshotEvent struct {
	Start, End time.Time // in the user's zone
	Title      string
}

type SnapshotCalendar struct {
	Events []SnapshotEvent
}

type SnapshotFollowup struct {
	At    time.Time // in the user's zone
	Label string
	Every string // "30 minutes" for a repeating one
}

// SnapshotFollowing is one open inbox matter being followed: where it happens, who or what it waits on, the next step
// and when to look again (CheckAt in the user's zone, zero when none). The two flags select the state note in the
// template, so no prompt wording is assembled in Go.
type SnapshotFollowing struct {
	Title, Where, Who, Next string
	CheckAt                 time.Time
	FollowUpAsk, CheckDue   bool
}

// SnapshotPlan is a plan card in the main conversation still waiting for the user's "Start" (#4453): nothing of it has
// started, and it shows nowhere else.
type SnapshotPlan struct {
	ID     string    // the plan's id, for task_plan_withdraw (#6739)
	At     time.Time // when it was put up, in the user's zone
	Titles []string  // its tasks' titles (at most three)
	Tasks  int       // how many tasks it has
}

// SnapshotAsked is an ask_user question that became a to-do (#1617): what was asked, the to-do, when it was asked (in the
// user's zone).
type SnapshotAsked struct {
	TodoID   string
	Question string
	At       time.Time
}

// SnapshotRoutine is a daily scheduled task (morning brief, evening brief…): its rule, and how its last run went (LastAt zero: it never
// ran). LastStatus is the run's status (ok / failed / skipped), LastWhy why it failed or was skipped, in short.
type SnapshotRoutine struct {
	Label, Rule         string
	LastAt              time.Time // in the user's zone
	LastStatus, LastWhy string
}

// SnapshotMiss is a scheduled run that failed or was skipped since yesterday evening, with why, in short (#4124): the
// The morning brief says "last night's check failed" without calling list_followups.
type SnapshotMiss struct {
	At         time.Time // in the user's zone
	Label, Why string
	Skipped    bool
}

// SnapshotTask is a task opened from an assistant conversation: a todo and its agents (docs/assistant/unified-entry.md).
type SnapshotTask struct {
	ID, Title, State string
	Machine, Session string // its latest agent, when it has one
	Last             string // what that agent said last, in a line
	// Doing: the step that agent is on (its latest tool call, transcript.Activity) while it works or has said nothing
	// yet, and DoingFor how long that step has gone on ("3 minutes"), "" when unknown or not working.
	Doing, DoingFor string
	Subagents       []string // that agent's running subagents, what each is for: on top of its own step, never instead
	// Workflows: that agent's background workflow runs still running (#2935): its turn ended to wait for them.
	Workflows []SnapshotWorkflow
	Link      string // the todo's page in the hub's web app ("" when the hub has no public_url)
	// Cancellation and brief lineage keep the history visible in the same snapshot as the state (#5732).
	CancelledReason                string
	Continues, ContinuesLink       string
	SupersededBy, SupersededByLink string
	// Health: how a task the main conversation supervises stands (normal, stuck N minutes, waiting for an answer, to accept, rework round N, your call,
	// accepted; #3124), "" for any other; DoneWhen how many criteria it has.
	Health   string
	DoneWhen int
	// Orphan: why nobody works on an open task any more (error: its agent ended on a model error and is closed;
	// closed: its agent's tab was closed; offline: its computer is away), "" while an agent is at it or will be woken
	// (docs/assistant/todos.md "Nobody is working on this").
	Orphan string
	// Delivery: how far its finished work was delivered when that is short of live ("stopped at the PR, stuck on 'CI queued'"), ""
	// otherwise (docs/assistant/todos.md "Delivery").
	Delivery string
	// Reported: what its agent last reported to the supervisor (report_to_supervisor), nil when it has not.
	Reported *SnapshotReport
	// Plan: its agent's own task list, nil when it keeps none or Reported is newer (a report says more).
	Plan  *SnapshotAgentPlan
	Owner SnapshotOwner
	// Origin conversation attribution for tasks opened outside the main chat (rendered in the main overview, #13657).
	OriginID, OriginTitle, OriginLink string
	// SupervisedElsewhere marks a task supervised by another conversation, shown read-only in the main overview.
	SupervisedElsewhere bool
}

// SnapshotOwner is whose a task in the snapshot is, never rendered: the conversation that handed it out (its todo's
// origin, "" for the user's own to-do) and the one that supervises it, so a conversation other than the main one gets
// only its own tasks (assistant/context.go scopeSnapshot).
type SnapshotOwner struct {
	Origin, Supervisor string
}

// SnapshotOpenTodo is the compact index of one open todo. Full task details are
// limited separately so every open todo remains discoverable in the context.
type SnapshotOpenTodo struct {
	ID, Title, Link string
	Owner           SnapshotOwner
	// Failed: its agent did not start, and it still waits on the owner (stored done with its start error, counted open
	// as the app's To-dos count it).
	Failed bool
}

// SnapshotReport is a supervised task's agent's latest report: its kind (progress, question, blocked, done), the step
// of how many (0: not said), what it said in a line and how many minutes ago.
type SnapshotReport struct {
	Kind        string
	Step, Steps int
	Text        string
	Minutes     int
}

// SnapshotWorkflow is a Claude Code workflow run still running in an agent's background (#2935): the agent's turn
// ended to wait for its result, which wakes it.
type SnapshotWorkflow struct {
	Name, Phase        string
	PhaseIndex, Phases int    // Phase's 1-based place among the script's phases; 0 when not known
	Done, Started      int    // its agents that returned, and started so far
	For                string // how long it has run ("12 minutes"), "" under a minute
}

// SnapshotProfile is the model profile in use (docs/assistant/model-profiles.md) with its tiers, the Agent presets
// agent_start and task_plan take; Unreadable when the profiles could not be read, Gone when the one in use was deleted
// (a rollback to it).
type SnapshotProfile struct {
	ID, Name         string
	Tiers            []SnapshotTier // by name
	Unreadable, Gone bool
}

// SnapshotTier is one tier of the profile in use: what an Agent started on it runs on first ("" model or effort: the
// launcher's), how many other candidates follow it (the hub picks among them at dispatch), the older names it answers
// to, and when to use it.
type SnapshotTier struct {
	Name, Launcher, Model, Effort, Speed, PermissionMode, Description string
	More                                                              int
	Aliases                                                           []string
}

// Snapshot is the live context a starting assistant gets in its system prompt.
type Snapshot struct {
	ModelProfile  *SnapshotProfile // nil: none in use; always refreshed outside the cached sources
	Now           time.Time        // in the user's zone
	Zone          string
	Machines      []SnapshotMachine
	Needs, Unread []SnapshotAgent
	// Waiting: stopped on something the hub waits out for them (a retry, a deploy, another agent); Done: finished
	// within a day. Their details stay out of the snapshot; Done renders only a count and agents_list state=done.
	Waiting, Done []SnapshotAgent
	// Awaiting: stopped on a question to the user, who looked at it and has not answered (docs/protocol/agent-states.md
	// "Awaiting your reply", #5096).
	Awaiting []SnapshotAgent
	// Muted: agents (or their spaces) the user turned notifications off for that would be in one of the above; in
	// none of them, as the Live Activity and the Agents badge count none of them (#4562).
	Muted []SnapshotAgent
	// Working: running agents; the snapshot renders only their count in the Agents heading, leaving details to
	// agents_list (#11261).
	Working         []SnapshotAgent
	PhoneOnline     bool
	NoPhone         bool               // leaves the iPhone line out: the background supervisor's (assistant/context.go backgroundSnapshot)
	Apps            []SnapshotApp      // one per kind of device, the user's own before a simulator
	KB              string             // RenderKBDigest, "" leaves it out
	Chats           *SnapshotChats     // nil: Feishu did not answer in time
	Calendar        *SnapshotCalendar  // nil: the calendar did not answer in time
	Followups       []SnapshotFollowup // the next eight
	FollowupsTotal  int                // all of them
	Routines        []SnapshotRoutine  // the daily tasks that are on (at most eight)
	RoutinesTotal   int                // all of them
	Missed          []SnapshotMiss     // scheduled runs that failed or were skipped since yesterday 18:00, newest first
	MissedTotal     int                // all of them (Missed is the newest six)
	Tasks           []SnapshotTask     // tasks opened from assistant conversations (task_plan), newest first
	OpenTodos       []SnapshotOpenTodo // compact index of open todos, including personal todos
	OpenTodosTotal  int                // all open todos; OpenTodos is bounded for very large lists
	OpenTodosLoaded bool               // false when the store could not be read; do not report that as zero
	Plans           []SnapshotPlan     // plan cards in the main conversation waiting for "Start", newest first (at most five)
	PlansTotal      int

	FollowingEnabled bool                // Inbox is enabled and available; false leaves the whole section out
	Following        []SnapshotFollowing // open inbox matters being followed, earliest check first (at most five)
	FollowingTotal   int                 // all of them
	// Asked: the assistant's ask_user questions nobody answered in time, now to-dos waiting for the user's call (#1617),
	// oldest first.
	Asked []SnapshotAsked
	// Unaccepted: deployed tasks the user has not passed yet (their "Please verify" ask, with a Lark card or without) (#2768), and
	// supervised tasks' conclusions the user has not answered (#1226), oldest first; the morning briefing brings them up again.
	Unaccepted      []SnapshotAccept
	UnacceptedTotal int // all of them (Unaccepted is the oldest eight)
	// Approvals: open tasks whose finished work waits for the user's nod to go live (merge, deploy, turn on, send),
	// oldest first: told to the user as one list to approve at once (docs/assistant/todos.md "Delivery").
	Approvals []SnapshotApproval
	// Scoped: the snapshot of a conversation other than the main one and its stand-ins: Tasks, Unaccepted and Approvals
	// hold only the tasks it handed out or supervises, and OpenTodos only its open ones Tasks does not show; other
	// conversations' tasks and the user's own to-dos are left to todo_list (OpenTodosTotal still counts every open
	// to-do; assistant/context.go scopeSnapshot).
	Scoped bool
	// Omit: the sections left out to keep the block within its budget (assistant/context.go fitSnapshot).
	Omit SnapshotOmit
}

// SnapshotOmit marks the sections a snapshot leaves out for size (#3203). An omitted section still renders its heading
// and count with where to see the rows (a tool, or the App for Inbox follow-ups), never as empty: the node sends only
// what changed, so a section that went missing would read as gone, and one rendered without its items as "none".
type SnapshotOmit struct {
	Waiting, Unread, Routines, Following, Followups, Asked, KB, OtherChats, Unaccepted, OpenTodos, Tasks, Approvals bool
}

// SnapshotCut is the line a snapshot cut short ends with (assistant/snapshot_cut).
func SnapshotCut() string { return render("assistant/snapshot_cut", nil) }

// SnapshotApproval is a task held short of live until the user approves: what it is, how far it got, where it is held.
type SnapshotApproval struct {
	ID, Title, Stage, Gate, Link string
	Owner                        SnapshotOwner
}

// SnapshotAccept is a deployed task waiting for the user to check it, or a conclusion waiting for the user: its verdict
// line as its card says it, what changed (deployed), where to look, since when.
type SnapshotAccept struct {
	Title, Verdict, Changed, Link string
	Since                         time.Time // in the user's zone
	Owner                         SnapshotOwner
}

func (s Snapshot) Up() []SnapshotMachine   { return s.machines(true) }
func (s Snapshot) Down() []SnapshotMachine { return s.machines(false) }

func (s Snapshot) machines(online bool) []SnapshotMachine {
	var out []SnapshotMachine
	for _, m := range s.Machines {
		if m.Online == online {
			out = append(out, m)
		}
	}
	return out
}

// Upcoming is the calendar without the meetings over by Now (the snapshot's sources are read less often than it is
// rendered).
func (s Snapshot) Upcoming() *SnapshotCalendar {
	if s.Calendar == nil {
		return nil
	}
	out := SnapshotCalendar{}
	for _, e := range s.Calendar.Events {
		if e.End.After(s.Now) {
			out.Events = append(out.Events, e)
		}
	}
	return &out
}

// FollowingLeft is how many followed matters are not in the snapshot's bounded list.
func (s Snapshot) FollowingLeft() int {
	left := s.FollowingTotal - len(s.Following)
	if left < 0 {
		return 0
	}
	return left
}

func RenderSnapshot(s Snapshot) string { return render("assistant/snapshot", s) }

// RenderSnapshotWorkflow is one running workflow, in a line ("'parity-audit' stage Verify (2/2), 169/185 agents done, running 14 minutes").
func RenderSnapshotWorkflow(w SnapshotWorkflow) string {
	return render("assistant/snapshot_workflow", w)
}

// AssistantContext is the hub's answer to assistant.context: the snapshot, for the main conversation with a line
// saying so first (assistant/context). Node puts what changed of it at the start of each message (TurnContext).
type AssistantContext struct {
	ScheduledWorkPlan bool // Inline instructions for a granted scheduled round when the manager uses persona-work.
	Main              bool
	Leader            bool // a direction lead supervises its planned tasks and explicitly assigned Agents
	Owner             string
	Snapshot          string
	// Directions: for the main conversation, the current leaders used as direction labels;
	// LeaderName: for a leader, its direction's name.
	Directions []ContextDirection
	LeaderName string
	// LeaderStatus: for a leader whose direction has notes, the one-line status of the knowledge-base project it is
	// filed under (#10086). Here rather than in its system prompt: a summary rewrite changes it, and a changed system
	// prompt would restart the leader.
	LeaderStatus string
	// Background: the legacy background supervisor, which finishes only tasks assigned before universal main supervision.
	// BackgroundStarted: for the main conversation, once: that legacy supervisor's id, when it began following old tasks.
	Background        bool
	BackgroundStarted string
}

// ContextDirection is one current leader as the main conversation is told of it: its conversation id and name.
type ContextDirection struct{ ID, Name string }

func RenderAssistantContext(c AssistantContext) string { return render("assistant/context", c) }

// SupervisorStart is the message an explicitly started background supervisor's conversation starts with
// (assistant/supervisor_start): what it is for, before its first supervision message. Automatic replacement of a
// legacy background supervisor stays disabled under universal main supervision (#5732).
type SupervisorStart struct{ Owner string }

func RenderSupervisorStart(d SupervisorStart) string { return render("assistant/supervisor_start", d) }

// TaskRun is the message a scheduled task's run starts with (assistant/task_run).
type TaskRun struct {
	Label          string
	TodoID         string
	Once, Interval bool   // neither: daily
	Every          string // interval: "30 minutes"
	Late           string // "" when on time; else how late ("25 minutes")
	Due            time.Time
	Prompt         string
	KBHint         bool // daily runs with the knowledge base on
	// KnowledgeDir: the knowledge base directory the hint names; "" is the default ~/.persona/data.
	KnowledgeDir string
	// Continued: it goes into the task's own conversation, which got the background on the first run.
	Continued bool
	// PushAsked: the task pushes only what the run sends with notify_user (its push "asked", #2396).
	PushAsked bool
	// InboxWorthy, InboxEvery: the task's inbox setting (#6430): a run puts a row in the Inbox only when it finds something
	// worth a look (inbox_post), or the manager puts one after every run.
	InboxWorthy, InboxEvery bool
	// Facts: the run's facts Persona worked out (RenderBriefFacts), above the task's prompt; "" for a task without them.
	Facts string
	State string // formatted scheduled-task state and update instructions
	// PageHeader: for a routine bound to a dashboard page (#13020), the rendered page_routine_run header.
	PageHeader string
}

func RenderTaskRun(t TaskRun) string { return render("assistant/task_run", t) }

// BriefFacts and the briefing prompt types are defined in the pure contract package internal/modapi and aliased here
// so both prompts and internal/modules/briefing share them without importing each other.
type (
	BriefFacts           = modapi.BriefFacts
	BriefDay             = modapi.BriefDay
	BriefDayGap          = modapi.BriefDayGap
	BriefDayPart         = modapi.BriefDayPart
	BriefDayEntry        = modapi.BriefDayEntry
	BriefDayAction       = modapi.BriefDayAction
	BriefTask            = modapi.BriefTask
	BriefCriterion       = modapi.BriefCriterion
	BriefLeader          = modapi.BriefLeader
	BriefDirection       = modapi.BriefDirection
	BriefWait            = modapi.BriefWait
	BriefDisabledRoutine = modapi.BriefDisabledRoutine
	BriefFailed          = modapi.BriefFailed
	BriefAccept          = modapi.BriefAccept
	BriefFollow          = modapi.BriefFollow
	BriefProgress        = modapi.BriefProgress
)

// BriefDayRunes is how many runes day adds to the facts (the evening routine keeps it about 4000).
func BriefDayRunes(day *BriefDay) int {
	return utf8.RuneCountInString(RenderBriefFacts(BriefFacts{Day: day})) - utf8.RuneCountInString(RenderBriefFacts(BriefFacts{}))
}

func RenderBriefFacts(b BriefFacts) string { return render("assistant/routine_facts", b) }

// RenderNorthStarFacts is what a leader or main conversation's daily task opens with when it has no facts of its own
// (assistant/north_star_facts): the north star's lines, ready to read.
func RenderNorthStarFacts(lines []string) string { return render("assistant/north_star_facts", lines) }

// RenderMorningRoutine and RenderEveningRoutine are the default daily tasks' prompts.
func RenderMorningRoutine() string { return render("assistant/routine_morning", nil) }
func RenderEveningRoutine() string { return render("assistant/routine_evening", nil) }

// FormerMorningRoutines and FormerEveningRoutines are the earlier defaults of those prompts, oldest first: a hub that
// saved one unchanged gets the current default (assistant: upgradeDefaultRoutines). Never edited: each is what a hub
// may hold byte for byte.
func FormerMorningRoutines() []string {
	return []string{render("assistant/routine_morning_former1", nil), render("assistant/routine_morning_former2", nil), render("assistant/routine_morning_former3", nil)}
}
func FormerEveningRoutines() []string {
	return []string{render("assistant/routine_evening_former1", nil), render("assistant/routine_evening_former2", nil)}
}

// RenderLostTasks tells an assistant conversation that a restart stopped its background tasks (assistant/lost_tasks).
func RenderLostTasks(tasks []string) string {
	return render("assistant/lost_tasks", struct{ Tasks []string }{tasks})
}

// Interject is the note on a message the user sent while the conversation was still answering
// (node/assistant_interject.go). Background: its foreground work was moved to the background for it.
type Interject struct {
	Background bool
}

// RenderInterject goes with such a message: say in one sentence, before the next tool call, what is going on, then
// answer it (#4883: six minutes of tool calls with no word to the owner). A <persona-context> block only.
func RenderInterject(v Interject) string { return render("assistant/interject", v) }

// RenderUsageRetry sends a conversation's message to the model again after a gateway's impossible usage made Claude
// Code refuse it as too long and Persona repaired the transcript (assistant/usage_retry, node/assistant_usage.go). It
// is a <persona-context> block only: the apps show no bubble for it.
func RenderUsageRetry() string { return render("assistant/usage_retry", nil) }

// RenderModelRetry resumes the same request after a bounded transient model failure. It adds no user bubble and never
// rewrites the original request (node/assistant_recovery.go).
func RenderModelRetry() string { return render("assistant/model_retry", nil) }

// TaskNotice tells the main conversation (or a reporting one) about one of its quiet tasks (#1731): it was done, waits for the user, was
// cut off by the model service or did not start. The assistant decides what to tell the user.
type TaskNotice struct {
	Title, Machine string
	State          string   // done | needs | interrupted | failed
	Earlier        []string // what else it went through since the last notice ("waiting on the user", "done"), oldest first
	Ask            string   // needs: what it asks
	Text           string   // what its agent said last, or why it did not start
	// Files: the files it handed over, with where each is on the conversation's computer (task_files.go, #10511); the
	// agent named them in its reply, and the manager copied them here. They are kept out of Text's clip.
	Files []TaskFile
	// RetryWith: failed because of its launcher there, the launcher the assistant may start it with instead when the
	// user asks (task_retry's launcher; only the web's card has a button for it, #10365).
	RetryWith string
	// Reporting: the notice goes into the conversation that opened the task (one started with report_tasks, such as
	// a Feishu /agent's, #3192), not the main conversation.
	Reporting bool
}

func RenderTaskNotice(n TaskNotice) string { return render("assistant/task_notice", n) }

// TaskFile is one file a task handed over (task_files.go, #10511), as the task notice and a supervision message list
// it: Path is where the conversation finds it (its copy, else the original). Error says why it did not come; Path is
// then where it stayed, on the Agent's computer.
type TaskFile struct {
	Name, Path, Error string
}

// SpawnNotice tells the conversation that opened another with conversation_new that the other failed, or ended a turn
// without reporting back with conversation_send (#4669): once per opened conversation.
type SpawnNotice struct {
	ID, Title, Link string
	Failed          bool
	Error           string // Failed: the turn's error
	Last            string // what it said last, when it did not fail
}

func RenderSpawnNotice(n SpawnNotice) string { return render("assistant/spawn_notice", n) }

// TaskEnded tells a conversation that is not the main one that a task it handed out with task_plan, supervised by the
// main conversation, ended (#4669): accepted or cancelled, with the task's link (or its id without one).
type TaskEnded struct {
	ID, Title, Link string
	Verdict         string // accepted | cancelled
	Note            string
}

func RenderTaskEnded(n TaskEnded) string { return render("assistant/task_ended", n) }

// PRFollowupNotice tells a closed to-do's original supervisor conversation that a delivery PR it no longer watches
// through an open to-do changed after acceptance (#12371): merged, closed unmerged, a merge conflict, or a
// changes-requested review on the current head. Event is one of merged | closed | conflict | changes_requested. The
// to-do stays closed; this is a record, not a reopen and not an acceptance signal. Language is the owner's
// (answerLanguage: empty Chinese, English or Japanese).
type PRFollowupNotice struct {
	ID, Title, PRURL string
	Event            string
	Language         string
}

func RenderPRFollowupNotice(n PRFollowupNotice) string { return render("assistant/pr_followup", n) }

// RelayRefused tells a conversation that the message it sent another with conversation_send or conversation_new (Tool)
// was refused for good by that conversation (Title, ID) with a refusal Code, and is not tried again (#11393). Excerpt
// is how the message began. Language is the owner's (answerLanguage: "" Chinese, "English", "日本語").
type RelayRefused struct {
	Language                       string
	Tool, ID, Title, Code, Excerpt string
}

func RenderRelayRefused(d RelayRefused) string { return render("assistant/relay_refused", d) }

// RelayLater is what conversation_send with wake: false kept for a conversation (#11614): it goes in, as a context
// block, at the start of the next message the hub delivers there for any other reason. Language is the owner's
// (answerLanguage; empty: Chinese).
type RelayLater struct {
	Language string
	Items    []RelayLaterItem
}

// RelayLaterItem is one kept message: the sending conversation, when it was sent (the owner's local time) and its words.
type RelayLaterItem struct {
	FromID, FromTitle, At, Text string
}

func RenderRelayLater(d RelayLater) string { return render("assistant/relay_later", d) }

// ChatReply tells a conversation that wrote into a Lark chat as the user that others wrote there since (#10777): the
// chat's name (Group: a group, else a direct chat with that person), when the conversation last wrote there (local
// "01-02 15:04"), how many new messages and who wrote the newest, whether Context also repeats messages it was told
// before (Earlier), and the messages' text, which goes in a context block as someone else's words.
type ChatReply struct {
	Language                     string // answerLanguage: "" Chinese, "English", "日本語"
	Title, Sender, Sent, Context string
	Group, Earlier               bool
	Count                        int
}

func RenderChatReply(d ChatReply) string { return render("assistant/chat_reply", d) }

// AskTodoAnswer is the message to the conversation whose ask_user question became a to-do (#1617): the user's call on it
// (Thread: said in the to-do's Lark topic), or that the to-do was closed without one (Dropped). The questions and choices
// are escaped by the caller.
type AskTodoAnswer struct {
	When      string // when it was asked, MM-DD HH:MM in the user's zone
	TodoID    string
	Questions []string
	Choices   []AskTodoChoice
	Thread    bool
	Dropped   bool
	Several   bool // more than one question was asked
	// Defaulted: nobody decided by the question's deadline (Deadline, MM-DD HH:MM) and it could be undone, so its
	// recommended options were taken by default (#3127).
	Defaulted bool
	Deadline  string
}

// AskTodoChoice is one call of the user's: the question as they wrote it ("" for a reply in the topic), the choice, why.
type AskTodoChoice struct {
	Question string
	Choice   string
	Reason   string
}

func RenderAskTodoAnswer(a AskTodoAnswer) string { return render("assistant/ask_answer", a) }

// AskLater is the message to the conversation that asked with ask_user and wait false (#4928) once its card ended:
// the user's answers (Answers; Missing: some of the questions got none), that it expired (Expired: what became of
// it, as a waiting ask_user would have been told), that its time ran out after the user wrote in the conversation
// (Spoke: no to-do, no default), or, with none of them, that the card ended unanswered. The questions and answers
// are escaped by the caller.
type AskLater struct {
	Internal  bool   // supervisor decision, never attributed to the owner
	When      string // when it was asked, MM-DD HH:MM in the user's zone
	Questions []string
	Answers   []AskTodoChoice
	Missing   bool
	Expired   string
	Spoke     bool
}

func RenderAskLater(a AskLater) string { return render("assistant/ask_later", a) }

// Supervise is a supervise message: the tasks the main conversation handed out that need it now (#3124), what waits on a
// decision first.
type Supervise struct {
	Level   string
	Entries []SuperviseEntry
	// Leader: it goes to a leader conversation, for a manual Agent explicitly assigned to it (#5732); Escalated: an
	// entry is a leader's needs_user, told the main conversation.
	Leader, Escalated bool
	// Background: with Leader, it goes to the background supervisor (#4927): the leader's message, for tasks other
	// conversations handed out, whose cards go from it to the user.
	Background bool
	// BackgroundEscalated: with Escalated, one of those entries is the background supervisor's needs_user (#4927): no
	// leader is named for it.
	BackgroundEscalated bool
	// Side: a legacy route to an ordinary side conversation for tasks it supervised before universal main supervision.
	Side bool
	// LeaderStuck: an entry is a leader's task past its inactivity threshold (sigLeaderStuck), told the main conversation (#3609).
	LeaderStuck bool
	// Overdue: an entry is a task past its brief's clock (#3822).
	Overdue bool
	// ParkedDue: an entry is a parked task whose time came (#3804).
	ParkedDue bool
	// ParkDecided: an entry is a task whose parking a decision of the owner's ended (#8044).
	ParkDecided bool
	// Asked: an entry is an agent's cards that came to this conversation before the user (ask); AskMinutes how long
	// they wait here before going up a rung.
	Asked      bool
	AskMinutes int
	// PushGrading: the owner's push grading switch is on: ask_escalate pushes only what is the owner's alone (why_owner).
	// No card is handed to the user on a timeout either way.
	PushGrading bool
	// Decisions: an entry is about a decision the owner cannot answer as it is (recheck, undecidable,
	// owner_cannot_answer; #3804).
	Decisions bool
	// Inbox: an entry is new Inbox messages from other people about the task (#4293).
	Inbox bool
	// Fresh: it goes to the background supervisor as a round that starts from a clean context (#7315): a line on where
	// what earlier rounds settled is kept, the round marker (SuperviseRoundMarker) and each entry's last review. The
	// handling rules are in no supervision message (they are the static supervision instructions), so a round carries
	// only its entries' facts. Not Fresh renders as before, byte for byte.
	Fresh bool
}

// SuperviseEntry is one task in it, with what happened (Kind: review, idle, needs_you, stalled, interrupted, failed,
// started, escalated, ask, inbox…).
type SuperviseEntry struct {
	ID, Title, Kind string
	AgentReply      *DecisionAgentReply // decision_reply: a pending decision's owner message, not an inferred choice
	Detail          string              // why: the judge's reason, the step it is stuck on, the model service's error, why it did not start
	Minutes         int                 // how long it has waited (needs_you), not moved (stalled) or not progressed (no_progress)
	Calls, Waits    int                 // no_progress: the tool calls the hub saw since the last progress, and how many were waits or repeats
	Ask             string              // needs_you: what it asks
	Recommend       string              // escalated: the option the leader recommends
	LeaderGone      bool                // decide: its task leader agent cannot be reached, so the supervisor takes the task over
	SupervisorGone  bool                // decide: its assigned Assistant supervisor cannot receive the relay, so main takes over
	Background      bool                // escalated: by the background supervisor, not a leader conversation (#4927)
	DoneWhen        []string            // what done means
	Round           int                 // reworks so far
	Machine         string              // where its agent runs (a name)
	Session         string              // its agent's session id (short)
	AgentID         string              // full id for durable deduplication; not rendered
	Last            string              // what its agent said last
	Closeout        *SuperviseCloseout  // the closeout its agent ended the turn with (#3612), nil when it wrote none
	Link            string              // the task's page ("" when the hub has no public_url)
	// Origin conversation that dispatched the task, preserved across hand-overs (#13657).
	OriginID, OriginTitle, OriginLink string
	Quiet                             bool               // the user was not pushed about it
	Orphan                            string             // orphaned: why nobody works on it (error, closed, offline)
	Review                            *SuperviseReviewed // review: what the independent reviewer found (#2173), nil when none ran
	// report: what its agent reported (progress, question, blocked, done), its words, evidence and step.
	// Reported: on the entry of a turn's end (review, idle, unjudged, interrupted), the kind of the report its agent
	// made before it stopped, told in that one entry (assistant.joinTurnReports); Said, Evidence and Step are the report's.
	Reported    string
	Report      string
	Said        string
	Evidence    []string
	Step, Steps int
	// Files: the files its agent handed over, with where each is on this conversation's computer (task_files.go,
	// #10511). The manager copied them here when the agent's turn ended; they are kept out of Last's clip.
	Files []TaskFile
	// failed with its tab kept (#2570): until when (local "15:04"), the question its shell waits on, the tab's last
	// lines, and how many retries are left; KeptUntil "" when the start failed for good. Screen is also set for an
	// agent_start_stalled start that failed for good (its screen when it stopped, start_remedy.go remedyEntry).
	KeptUntil, Question, Screen string
	RetriesLeft                 int
	// RetryWith: failed because of its launcher on that computer, the other launchers there task_retry may start it
	// with (PlanTask.RetryWith, #10365), the first suggested.
	RetryWith []string
	// ask: the cards waiting, and the task's decisions in effect (prompts/todo/decisions.md, rendered), which
	// parked_due and park_decided carry too; escalated: the cards a leader gave the user.
	Asks      []SuperviseAsk
	Decisions string
	// recheck and owner_cannot_answer: the decision as it was asked; owner_cannot_answer: the owner's answer
	// (decide_yourself, not_what_i_asked, explain_first).
	Decision *SuperviseDecision
	Response string
	// Told: a check's decided entry whose answer is already in its Agent's chat (said there, or sent by agent_send).
	Told bool
	// parked_due: what the task was parked for; park_decided: what it was parked for again before the message went.
	Parked *SuperviseParked
	// From the task's brief (#3822): Asked is the owner's message that led to the work (clipped) and AskedAt its day;
	// Started says who started it when it was not the owner; Dropped are the criteria scope changes took out.
	// (assistant, leader, routine, hub) and StartedWhy the reason given.
	Asked, AskedAt, Started, StartedWhy string
	Dropped                             []string
	// AskedCut: Asked is only the start of the owner's message; the rest is on the task (todo_get, its Link).
	AskedCut bool
	// inbox: the Inbox item the new messages came in, the newest when several did.
	Inbox *SuperviseInbox
	// LastReview: the task's last review, shown only in a Fresh message (#7315): a round that does not see the earlier
	// ones does not ask or send back the same thing again. nil when it was never reviewed.
	LastReview *SuperviseLastReview
	// TodoReady: an open, unstarted dependent to-do whose after prerequisites are all satisfied (#12606).
	TodoReady *SuperviseTodoReady
	// ExternalInfo: new external information received while a needs_user ask is still pending (#13112).
	ExternalInfo *NeedsUserExternalInfo
	// ParentCloseout: localized closeout guidance when all part_of children of an open parent to-do are done (#13100).
	ParentCloseout string
	// OutageRecovery: localized coalesced recovery summary after a provider-wide outage held interrupted/unjudged signals (#13158).
	OutageRecovery string
}

// SuperviseTodoReady describes an open, unstarted to-do ready to advance once all of its after prerequisites are met.
type SuperviseTodoReady struct {
	PartOf    string                  // parent request to-do id (when set)
	Notes     string                  // saved notes / launch instructions on the ready to-do
	Satisfied []SupervisePrerequisite // satisfied prerequisites, with any merged PR URLs
}

// SupervisePrerequisite is one satisfied after prerequisite carried on a todo_ready wake.
type SupervisePrerequisite struct {
	ID        string
	Title     string
	Reason    string // "done" or "pr_merged"
	MergedPRs []string
}

// SuperviseLastReview is a task's last task_review as a Fresh supervision message repeats it: the verdict, when (local
// "01-02 15:04") and its reason or note, clipped.
type SuperviseLastReview struct {
	Verdict, At, Note string
}

// DecisionAgentReply asks a supervisor to confirm a pending decision after its owner writes in the task's Agent chat.
type DecisionAgentReply struct {
	Language, Machine, Session string
	Handled                    bool
	Text, Closeout             string
}

// SuperviseInbox is the Inbox item an inbox entry names (#4293): its id, title (the chat's name or the mail's subject),
// who wrote the newest message, a one-line preview, and how many messages from other people came in about the task
// since its supervisor last heard (the newest item's, and any other item's linked to the same task meanwhile).
type SuperviseInbox struct {
	Item, Title, Sender, Preview string
	Count                        int
	// URL is a GitHub PR's or issue's address; ToAgent is "sent" once the note reached the task's agent (InboxToAgent,
	// #7346), "unhandled" when its bounded retry failed, "" when only the supervisor is told.
	URL, ToAgent    string
	AfterAcceptance bool // new feedback on a still-open PR of an accepted task
}

// InboxToAgent is the note a task's agent gets when someone else writes on the task's GitHub thread (#7346): who,
// how many new messages, a line of the newest, and the thread to read.
type InboxToAgent struct {
	Sender, Preview, URL string
	Count                int
	Event                string // server-authored delivery identity, absent for legacy callers
}

// RenderInboxToAgent is that note.
func RenderInboxToAgent(d InboxToAgent) string { return render("assistant/inbox_to_agent", d) }

// SuperviseParked is what a task was parked for (task_review parked), as its parked_due entry repeats it: the reason,
// what kind of thing it waits on, until when (local), how many times in a row it was parked with no agent turn in
// between, and how many more times it can be.
type SuperviseParked struct {
	Reason, WaitingOn, Until string
	Count, Left              int
	// Resent: a parked_due told again because no task_review followed the earlier reminder; 1 is the first resend.
	Resent int
}

// SuperviseDecision is a decision a task's supervisor put to the owner (task_review needs_user), as a supervision
// entry repeats it: why only the owner can decide, the question, the options with what each leads to, the one
// recommended, what waiting costs, and whether it is never taken by default.
type SuperviseDecision struct {
	WhyOwner, Question, Recommend, Cost string
	Options                             []SuperviseOption
	NeverDefault                        bool
	// Background: where the task came from and where it stood, as the hub told the owner.
	Background string
}

// SuperviseOption is one option of a SuperviseDecision.
type SuperviseOption struct{ Label, LeadsTo string }

// SuperviseAsk is one card of a task's agent on its way to the user: its interaction id, kind (question, permission,
// plan), the agent's machine (a name) and short session id, what it asks, and whether the rung below did not handle it in time.
type SuperviseAsk struct {
	ID, Kind, Machine, Session, Text string
	Raised                           bool
	// Reminded: low-distraction mode kept the card from the user when its time ran out, and gives it to its supervisor once more.
	Reminded bool
}

func RenderSupervise(s Supervise) string { return render("assistant/supervise", s) }

type DraftPR struct {
	Language, URL, Head string
}

func RenderDraftPR(p DraftPR) string { return render("assistant/draft_pr", p) }

// EstimateDue is the localized fact attached to an automatic estimate_due supervision entry.
type EstimateDue struct {
	Language          string
	Expected, Elapsed int
}

func RenderEstimateDue(p EstimateDue) string { return render("assistant/estimate_due", p) }

// SuperviseReviewed is the independent review of a task waiting for review: State clean (nothing blocking), blocking
// (blocking findings left once the rework rounds ran out) or failed (Error says why), its counts and report.
type SuperviseReviewed struct {
	State           string
	Count, Blocking int
	Findings, Error string
}

// SuperviseRework is what a task's agent is sent when its supervisor sends the work back.
type SuperviseRework struct {
	Round, Max int
	Checks     []SuperviseCheck
	Note       string
}

type SuperviseCheck struct {
	Criterion, Evidence string
	Met                 bool
}

func RenderSuperviseRework(r SuperviseRework) string { return render("assistant/supervise_rework", r) }

// SuperviseReviewRework is what a task's agent is sent when the independent reviewer found blocking problems in its
// change (#2173): the round, and the reviewer's report as it wrote it.
type SuperviseReviewRework struct {
	Round, Max      int
	Count, Blocking int
	Findings        string
}

func RenderSuperviseReviewRework(r SuperviseReviewRework) string {
	return render("assistant/supervise_review_rework", r)
}

// RenderTaskDoneWhen is what follows a supervised task's prompt: what done means, and that the agent shows it.
func RenderTaskDoneWhen(criteria []string) string {
	return render("assistant/task_done_when", struct{ Criteria []string }{criteria})
}

// RenderTaskGoal is a task's done_when as one checkable condition, for the Claude Code or Codex /goal typed after the
// task's first message (docs/integrations/agent-goal.md "A goal set at the start"): one line, trimmed.
func RenderTaskGoal(criteria []string) string {
	return strings.TrimSpace(render("assistant/task_goal", struct{ Criteria []string }{criteria}))
}

// Relay is a message one assistant conversation sends another (conversation_send / conversation_new): what was
// written, behind a note saying where it comes from and how many hops it is from the user's own message.
type Relay struct {
	FromID, FromTitle string
	Tool              string // conversation_send | conversation_new
	Hop, MaxHops      int
	Text              string
	// Quote: the owner's own message the sender carried, as the hub verified it (#3822); nil sends none.
	Quote *RelayQuote
}

// RelayQuote is the owner's message a relay carries: its id, when and in which conversation they wrote it, the
// conversations that passed it on after that one (oldest first), and the text verbatim. ParseRelayQuoteID reads the id back.
type RelayQuote struct {
	Part                 bool // the text is an excerpt from the source message
	ID, At, Conversation string
	Via                  []string
	Text                 string
}

// RenderRelay is the message the other conversation gets (assistant/relay).
func RenderRelay(r Relay) string { return render("assistant/relay", r) }

// RenderRefused is passed into a running Codex turn when a tool call was refused (assistant/refused).
func RenderRefused(reason string) string { return render("assistant/refused", reason) }

// CodexMemory tells the Codex assistant about the memory it shares with Claude (assistant/codex_memory).
type CodexMemory struct {
	Dir       string
	Index     string // MEMORY.md, trimmed
	Truncated bool   // Index is the start of a longer one
}

func RenderCodexMemory(m CodexMemory) string { return render("assistant/codex_memory", m) }

// RenderMemoryDream asks the background pass to consolidate durable preferences.
func RenderMemoryDream() string { return render("assistant/memory_dream", nil) }

// RuntimeInstructions is what the assistant on an agent runtime (DSH…) gets with a new conversation's first message
// (assistant/runtime_instructions): the shared memory, and the skills' gated commands it runs as tools.
type RuntimeInstructions struct {
	Dir       string
	Index     string // MEMORY.md, trimmed
	Truncated bool   // Index is the start of a longer one
	// MemoryTools tells remote runtimes (caps.remote_agent) to read and write shared memory via memory_read / memory_write.
	MemoryTools bool
	// Gates are the skills' gated commands this machine runs as tools (a plugin's assistant.gate with a tool).
	Gates []RuntimeGate
}

// RuntimeGate is one gated command as a runtime's tool: what the card calls it, the command, the tool.
type RuntimeGate struct {
	Title, Command, Tool string
}

func RenderRuntimeInstructions(r RuntimeInstructions) string {
	return render("assistant/runtime_instructions", r)
}

// RenderCodexInstructions composes memory and optional supervisor policy as developer instructions.
// The live snapshot stays in each message (TurnContext), like Claude's.
func RenderCodexInstructions(memory string, supervision ...string) string {
	policy := ""
	if len(supervision) > 0 {
		policy = supervision[0]
	}
	return render("assistant/codex_instructions", struct{ Memory, Supervision string }{memory, policy})
}

// TurnContext is the <persona-context> block a message to the assistant starts with (assistant/turn_context): the
// sections of the live snapshot (AssistantContext) the conversation hasn't seen yet. Full: all of them (the
// conversation is new, was compacted, or node lost track); otherwise only those that changed, the time always, and
// in Gone the names of the sections the snapshot no longer has.
type TurnContext struct {
	Full     bool
	Sections []TurnSection
	Gone     []string
}

// TurnSection is one section of a TurnContext: all of it in Text, or with Partial only what changed in it (#9078).
// A partial section says so on its heading, so the rows it lists are never read as all the rows there are: Text is
// then the heading line when that changed ("" when it did not: Name, the section's name, stands in for it), Rows the
// rows that are new or changed, Removed the ids of the rows that are gone, and Order the ids of all its rows in
// their order now, given only when the reader could not tell it (the rows it knew in the order it knew them, the
// new ones after them).
type TurnSection struct {
	Text    string
	Partial bool
	Name    string
	Rows    []string
	Removed []string
	Order   []string
}

func RenderTurnContext(c TurnContext) string { return render("assistant/turn_context", c) }

// TaskDone is the system prompt deciding whether a task opened from an assistant conversation is finished, so the
// hub can mark its todo done (assistant/task_done, docs/assistant/todos.md "Automatic completion").
type TaskDone struct {
	Owner string
	// Delivery: also say how far the work was delivered (assistant.done_needs_delivery, docs/assistant/todos.md "Delivery"): only
	// live or nothing to ship closes the task.
	Delivery bool
	// Checks: also check each done_when criterion (checkable, met, evidence) and say the delivery, for the judge's own
	// check of a supervised task (assistant.review_sample_percent below 100, docs/assistant/todos.md "Spot-check reviews").
	Checks bool
	// Language: the user's language for the reason, which the apps show on the task ("" for Chinese, the prompt's own).
	Language string
}

func RenderTaskDone(d TaskDone) string { return render("assistant/task_done", d) }

// RenderTaskDelivery is the delivery standard a task's agent is handed after its prompt (assistant/task_delivery,
// docs/assistant/todos.md "Delivery"): done is live and checked, and its last line says how far it got. closeout: the
// closeout's form follows in the same prompt (agents/closeout_brief), so the line goes before that block, not last.
func RenderTaskDelivery(closeout bool) string {
	return render("assistant/task_delivery", struct{ Closeout bool }{closeout})
}

// TaskAttachments ends a message to an agent with the files and pictures the user gave the assistant for it (#2437):
// where they are on the agent's computer (Copied: copied there from the assistant's), and those that could not come.
// Pictures go last, in the <attached_images> block a picture sent straight to an agent ends with (node/send.go), so
// the apps draw them as they do those.
type TaskAttachments struct {
	Files, Missing, Pictures []string
	Copied                   bool
}

func RenderTaskAttachments(a TaskAttachments) string {
	return strings.TrimRight(render("assistant/task_attachments", a), "\n")
}

// RenderSideConversation marks a separate request for an Agent assigned to a task.
func RenderSideConversation(text string) string {
	return render("assistant/side_conversation", text)
}

// TaskDoneInput is the task as it was handed out and what its agents said when they stopped (assistant/task_done_input).
type TaskDoneInput struct {
	Title, Prompt string
	// From the task's brief (#3822): Asked is the owner's message that led to the work, verbatim, and AskedAt its day;
	// Dropped the criteria logged scope changes took out. Criteria are the brief's as those changes left them.
	Asked, AskedAt string
	Dropped        []string
	Replies        []TaskDoneReply
	Criteria       []string `json:"criteria,omitempty"`
	// Hints the hub found in the replies: a merged pull request, a TestFlight build number.
	MergedPR, Build bool
}

// TaskDoneReply is one agent's last reply, and what the user last asked it in that turn when it was not the task's
// own prompt (a follow-up the user sent the agent later).
type TaskDoneReply struct {
	Machine, Ask, Text string
	From               string `json:"from,omitempty"`
	Source             string `json:"source,omitempty"`
	// Earlier is the agent's reply in the turn before the user's own follow-up in this session (its report of the
	// work, when the follow-up only asks whether it is done); never set for a side-conversation turn (#3111).
	Earlier string `json:"earlier,omitempty"`
}

func RenderTaskDoneInput(in TaskDoneInput) string { return render("assistant/task_done_input", in) }

// MainHandoff is the first message of a new main conversation: what the old one left (its last compaction summary,
// the messages after it), behind a note, then what the user wrote (docs/assistant/assistant.md "New main chat").
type MainHandoff struct {
	FromID  string
	Until   string // when the old one was archived, in the user's words
	Summary string
	Recent  []HandoffTurn
	Text    string
}

// HandoffTurn is one message of the old conversation: Role user or assistant.
type HandoffTurn struct {
	Role string
	Text string
}

// RenderMainHandoff is assistant/main_handoff.
func RenderMainHandoff(h MainHandoff) string { return render("assistant/main_handoff", h) }

// RenderSessionHandoff renders background for a fresh side conversation or Agent.
func RenderSessionHandoff(summary string, recent []HandoffTurn) string {
	return render("assistant/session_handoff", struct {
		Summary string
		Recent  []HandoffTurn
	}{summary, recent})
}

// Acceptance is a deployed task, for the three lines of its "Please accept" card (assistant/acceptance_input; the system
// prompt is assistant/acceptance; docs/extensions/bots.md "Task progress and review requests").
type Acceptance struct {
	Title, Prompt, Reply string
	Links                []string // the only links it may pick
}

func RenderAcceptance() string                  { return render("assistant/acceptance", nil) }
func RenderAcceptanceInput(a Acceptance) string { return render("assistant/acceptance_input", a) }

// WorkspaceMention is what the assistant gets for a workspace the user @-mentioned (assistant/workspace_mention,
// #5594): where it is, the knowledge base's projects there and the agents that worked there lately. It replaces the
// client's <persona-context kind="workspace"> block in the message.
type WorkspaceMention struct {
	Title, Machine, MachineName, Cwd, Branch string
	Online                                   bool
	Projects                                 []WorkspaceProject
	Sessions                                 []WorkspaceSession
}

// WorkspaceProject is a knowledge-base project whose directories hold the workspace.
type WorkspaceProject struct {
	Name, Slug, OneLine string
}

// WorkspaceSession is an agent that worked in the workspace's directory: Cwd only when it is a folder under it.
type WorkspaceSession struct {
	Title, Agent, Status, When, ID, Cwd string
}

func RenderWorkspaceMention(w WorkspaceMention) string {
	return render("assistant/workspace_mention", w)
}

// SuperviseCloseout is an agent's own closeout as a supervision message shows it: its claim, to be checked.
type SuperviseCloseout struct {
	Status, Waiting   string
	Merged, Deployed  string // "yes", "no" or ""
	Did               []string
	Verified, Missing string
	Artifacts         []string
	// CheckedMerged, CheckedDeployed: what the hub found itself, "yes", "no" or "" when it could not tell.
	CheckedMerged, CheckedDeployed string
	LocalDeployment                bool
}

// RouteLeader is a current leader as the route model is shown it.
type RouteLeader struct{ ID, Title, Project string }

// RouteInput is what the route model reads: the sentence, the leaders and the knowledge base's project names.
type RouteInput struct {
	Sentence string
	Leaders  []RouteLeader
	Projects []string
}

func RenderRouteInput(in RouteInput) string { return render("assistant/route_input", in) }

// RenderRouteAgent is the system prompt of assistant.route for a new agent (assistant/route_agent): which leader's
// direction its work belongs to; it reads RouteInput (the agent's first message as the sentence, no projects).
func RenderRouteAgent() string { return render("assistant/route_agent", nil) }

// DirectionNotes is what a leader conversation is told of its own direction (#10086): the notes the owner wrote for it
// and the goal of the knowledge-base project it is filed under. Lang is the owner's language (i18n code; "" or
// zh-Hans: Chinese).
type DirectionNotes struct {
	Lang, Name, Notes, ProjectName, ProjectGoal string
}

// Empty: nothing the owner wrote and no project goal, so nothing is added to the leader's policy.
func (d DirectionNotes) Empty() bool { return d.Notes == "" && d.ProjectGoal == "" }

// RenderDirectionNotes is the block appended to a leader's role policy (supervision_instructions); empty when there
// is nothing to say, so a direction without notes keeps its policy byte for byte.
func RenderDirectionNotes(d DirectionNotes) string {
	if d.Empty() {
		return ""
	}
	return render("assistant/direction_notes", d)
}

// RenderTodoDirection is the block a new Agent working for a direction gets before its first prompt (todo/direction);
// empty without notes.
func RenderTodoDirection(d DirectionNotes) string {
	if d.Notes == "" {
		return ""
	}
	return render("todo/direction", d)
}

// SegmentHandoff continues the same logical conversation on a new engine.
type SegmentHandoff struct {
	Language     string
	FromLauncher string
	FromModel    string
	Summary      string
	Recent       []HandoffTurn
	Text         string
	Partial      bool
}

func RenderSegmentHandoff(h SegmentHandoff) string { return render("assistant/segment_handoff", h) }

// RenderSegmentSummary accepts the established language display names as well as BCP 47 codes.
// Empty and Chinese retain the original default bytes; Japanese and English are the other supported branches.
func RenderSegmentSummary(language string) string {
	return render("assistant/segment_summary", struct{ Language string }{Language: language})
}
