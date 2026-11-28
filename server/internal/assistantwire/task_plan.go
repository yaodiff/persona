package assistantwire

// TaskPlanEventType identifies a task plan broadcast.
type TaskPlanEventType string

const TaskPlanChanged TaskPlanEventType = "task_plan"

// TaskPlanEvent carries the manager's plan projection over the existing socket event.
type TaskPlanEvent[Plan any] struct {
	Type TaskPlanEventType `json:"type"`
	Plan Plan              `json:"plan"`
}
