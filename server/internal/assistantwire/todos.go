package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the todos (the assistant's, docs/assistant/todos.md) ----

// todoRoutes are the to-dos' relayed routes listed since the central Routes literal (bus.go) closed; the hub relays
// them like Routes.
var todoRoutes = []string{
	"GET /api/v1/todos/revision",                     // the pushed read's cheap validator (todo_revision.go, #13804)
	"POST /api/v1/todos/reorganize",                  // batch reorganization without interrupting running Agents (todo_reorganize.go, #14704)
	"POST /api/v1/todos/decisions/batch",             // batch decision submission for Decision Queue (todo_decisions_batch.go, #15698)
	"GET /api/v1/todos/decisions/groups",             // same-cause pending decision grouping under todos_v2 (todo_decisions_batch.go, #16433)
	"POST /api/v1/todos/{id}/blockers",               // add external PR blocker under todos_v2 (todo_blockers_routes.go, #16198)
	"DELETE /api/v1/todos/{id}/blockers/{blockerId}", // cancel blocker under todos_v2 (todo_blockers_routes.go, #16198)
	"GET /api/v1/todos/mergetrain",                   // read-only per-repo merge-train queue snapshot under todos_v2 (todo_mergetrain_wiring.go, #16432)
	"GET /api/v1/todos/{id}/trace",                   // read-only causal audit trace under todos_v2 (todo_causal_trace.go, #16433)
	"POST /api/v1/todos/{id}/split",                  // atomically split a todo into child todos under todos_v2 (todo_topology_ops.go, #16433)
	"POST /api/v1/todos/{id}/merge",                  // atomically merge source todo into target todo under todos_v2 (todo_topology_ops.go, #16433)
}

func init() { Routes = append(Routes, todoRoutes...) }

// TodoReport is what the hub shows of the todos: active links, display-only completed ownership and the agents kept
// quiet (#1731: no pushes, badges or Live
// Activity; those of quiet tasks open or done in the last week). Small, whole every time.
type TodoReport struct {
	Links     []TodoLink   `json:"links"`
	Completed []TodoLink   `json:"completed,omitempty"` // display-only ownership; never active work or supervision
	Quiet     []SessionRef `json:"quiet,omitempty"`
}

// TodoLink is one session association: active in Links, display-only history in Completed.
type TodoLink struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	TodoID    string `json:"todo_id"`
	Title     string `json:"title"` // the todo's
	// Task: the todo is a task an assistant conversation opened (its agents' cards name it: proto.Interaction.TaskTitle).
	Task      bool `json:"task,omitempty"`
	Cancelled bool `json:"cancelled,omitempty"` // completed ownership ended by cancellation, not completion
}

// TodoHandoff is assistant.todo_handoff's params: an agent is handed work for a todo (POST /api/v1/sessions or
// …/messages with todo_id); Text is what it is handed ("" when only the todo is wanted).
type TodoHandoff struct {
	TodoID string `json:"todo_id"`
	Text   string `json:"text,omitempty"`
	// New: Text is a new agent's first prompt; the background of the project its directory (Cwd on MachineID) belongs
	// to goes before it too (#2820). An older assistant ignores these.
	New       bool   `json:"new,omitempty"`
	MachineID string `json:"machine_id,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	// Agent is the kind that will receive Text. An empty value keeps the legacy prompt order.
	Agent string `json:"agent,omitempty"`
}

// TodoHandoffResult is assistant.todo_handoff's answer: the todo (Found false: no such todo) and Text with the todo's
// decisions in effect before it (as it was without any, or when they could not be read). Status and Error, when set,
// say the hand-off may not go (404: no such todo, 409: it is done); the hub answers the app with them as they are.
type TodoHandoffResult struct {
	Found  bool          `json:"found"`
	Title  string        `json:"title,omitempty"`
	DoneAt *time.Time    `json:"done_at,omitempty"`
	Text   string        `json:"text,omitempty"`
	Images []proto.Image `json:"images,omitempty"`
	Status int           `json:"status,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// TodoLinkParams is assistant.todo_link's params: the agent handed work for a todo works for it from now on (a link
// by hand; title, cwd and agent as the session has them). Prompt is what the agent was started with: without a
// title, the link's comes from it (its first line past a context block).
type TodoLinkParams struct {
	TodoID    string `json:"todo_id"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
}

// TodoLinkResult is assistant.todo_link's answer: the todo's title (the session the start answers names it).
type TodoLinkResult struct {
	Title string `json:"title,omitempty"`
}

// TodoSpaceRenamed is todos.space_renamed's params: the hub renamed a workspace (PATCH …/spaces/{wid}); a todo linked
// to it by its name follows.
type TodoSpaceRenamed struct {
	MachineID string `json:"machine_id"`
	Old       string `json:"old"`
	New       string `json:"new"`
}

const (
	// MethodTodoHandoff: TodoHandoff → TodoHandoffResult (the hub calls it before an agent is handed work for a todo).
	MethodTodoHandoff = "assistant.todo_handoff"
	// MethodTodoLink: TodoLinkParams → TodoLinkResult (after the hand-off went through); an error says why it was not linked
	// (db.ErrTodoNotFound / ErrTodoCompleted's words).
	MethodTodoLink = "assistant.todo_link"
	// EventTodoSpaceRenamed: TodoSpaceRenamed, one way.
	EventTodoSpaceRenamed = "todos.space_renamed"
	// CodeSendInDoubt: a to-do's message or answer that failed while its words may be with its Agent already
	// (delivered "unknown") or are, with what followed failed ("yes"), #10465. Never retryable: a client checks its op
	// id, or looks at the to-do, instead of sending the words again.
	CodeSendInDoubt = "send_in_doubt"
)

// TodoHandoffMethod binds a to-do handoff to its prepared text and status.
var TodoHandoffMethod = buswire.Method[TodoHandoff, TodoHandoffResult]{Name: MethodTodoHandoff}

// TodoLinkMethod binds a to-do link request to the linked to-do's title.
var TodoLinkMethod = buswire.Method[TodoLinkParams, TodoLinkResult]{Name: MethodTodoLink}
