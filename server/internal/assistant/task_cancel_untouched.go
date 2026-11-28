package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// freshDispatchAgent accepts only the first dispatch to a newly planned to-do.
// Work that transferred, restarted, delivered, or entered review keeps the ordinary guard.
func (s *Service) freshDispatchAgent(t Todo, now time.Time) SessionRef {
	if t.DoneAt != nil || t.PlanID == "" || t.CreatedAt.IsZero() || now.Sub(t.CreatedAt) >= taskUserIdle ||
		t.ReopenedAt != nil || len(t.Agents) != 1 || t.Check != nil || t.Delivery != nil || t.PRURL != "" ||
		t.Supervision == nil || t.Supervision.State != "" || len(t.Supervision.Reviews) != 0 || t.Supervision.Progress != nil ||
		len(s.taskFilesNow(t.ID)) != 0 {
		return SessionRef{}
	}
	a := t.Agents[0]
	if !a.active() || a.SessionID == "" || a.LinkedAt.Before(t.CreatedAt) {
		return SessionRef{}
	}
	return SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}
}

// untouchedDispatch checks the full short transcript outside the to-do writer.
// The original plan prompt is allowed to be present before the Agent starts work.
func (s *Service) untouchedDispatch(ctx context.Context, t Todo) SessionRef {
	ref := s.freshDispatchAgent(t, time.Now())
	if ref.SessionID == "" || s.sessionActivitySnapshotReasonExcept(ref.MachineID, ref.SessionID, time.Now(), true) != "" {
		return SessionRef{}
	}
	plan, err := s.loadPlan(ctx, t.PlanID)
	if err != nil {
		return SessionRef{}
	}
	var prompt, inputID string
	for _, task := range plan.Tasks {
		if task.TodoID == t.ID && task.ReuseTodo == "" && task.Continues == "" {
			prompt, inputID = strings.TrimSpace(task.Prompt), task.WaitInputID
			break
		}
	}
	if prompt == "" {
		return SessionRef{}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var transcript struct {
		Items   []proto.Item `json:"items"`
		HasMore bool         `json:"has_more"`
	}
	if err := s.call(ctx, ref.MachineID, DeviceSessionItems,
		map[string]any{"session_id": ref.SessionID, "limit": 80, "lazy_images": true}, &transcript); err != nil ||
		transcript.Items == nil || transcript.HasMore {
		return SessionRef{}
	}
	dispatchSeen := false
	readCalls := map[string]bool{}
	for _, item := range transcript.Items {
		switch {
		case item.SupervisionUser || item.Kind == proto.KindVisualization ||
			item.Role == "assistant" && (item.Kind == proto.KindImage || item.Kind == proto.KindText &&
				(!item.Narration || len(taskFilePaths(item.Text)) != 0)):
			return SessionRef{}
		case item.Kind == proto.KindToolUse:
			// These native tools only read. Unknown calls can already have changed files
			// or reported a result, even when no final answer has been written.
			switch item.ToolName {
			case "Read", "Glob", "Grep":
				readCalls[item.ToolUseID] = true
			default:
				return SessionRef{}
			}
		case item.Kind == proto.KindToolResult:
			if item.ToolUseID == "" || !readCalls[item.ToolUseID] {
				return SessionRef{}
			}
		case item.Role == "user" && (item.Kind == proto.KindText || item.Kind == proto.KindImage):
			text := strings.TrimSpace(item.Text)
			if dispatchSeen || item.Kind != proto.KindText || item.From != "" || item.Queued ||
				item.ClientMsgID != "" && item.ClientMsgID != inputID ||
				text != prompt && !strings.HasPrefix(text, prompt+"\n\n") {
				return SessionRef{}
			}
			dispatchSeen = true
		}
	}
	return ref
}
