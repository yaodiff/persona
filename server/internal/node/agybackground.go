package node

import (
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	agyTaskPrefix           = "Tool is running as a background task with task id:"
	agyBackgroundEndedLimit = 20
	agyEmptyTaskList        = "No background tasks are currently running."
	agyTimerExpiryGrace     = time.Minute
)

var (
	agyTaskName        = lazyre.New(`^task-[0-9]+$`)
	agyTaskEnd         = lazyre.New(`(?i)\bTask id "([^"\n]+)" (?:was )?(finished|failed|canceled|cancelled|killed|stopped)\b`)
	agyTaskMessage     = lazyre.New(`(?m)^[ \t]*\[Message\] timestamp=(\S+) sender=(\S+) priority=\S+ content=([\s\S]*)`)
	agyCronIteration   = lazyre.New(`\(iteration ([0-9]+)\)$`)
	agyTimerDescRe     = lazyre.New(`(?i)^Timer:\s*((?:\d+(?:\.\d+)?[hms]\s*)+)(?:,|$)`)
	agyTimerUnitRe     = lazyre.New(`(?i)(\d+(?:\.\d+)?)([hms])`)
	agyTaskHeartbeat   = lazyre.New(`(?i)^(?:\[Notice\]\s+)?Task(?:\s+id\s+"[^"\n]+")?\s+is\s+still\s+running\b`)
	agyServerRestart   = lazyre.New(`(?i)^(?:\[Notice\]\s+)?All your subagents and background tasks have been stopped due to server restart\b`)
	agySubagentIdleRe  = lazyre.New(`(?i)^Subagent ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}) has gone idle\b`)
	agyTaskListRow     = lazyre.New("^- .* \\(`([^`\\s]+)`\\)$")
	agyTaskStatusRe    = lazyre.New(`^Task:\s*(\S+)\r?\nStatus:\s*([A-Za-z_]+)(?:\r?\n|$)`)
	agyEmptySubagentRe = lazyre.New(`^You have 0 active subagent\(s\):(?:\s*\[\])?$`)
)

func agyBackgroundNotice(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "Created At:") {
		_, content, _ = strings.Cut(content, "\n")
	}
	if content, ok := strings.CutPrefix(strings.TrimSpace(content), agyTaskPrefix); ok {
		return content
	}
	return ""
}

func agyToolString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	// Native exports can encode a tool argument as JSON inside a string.
	var decoded string
	if strings.HasPrefix(value, `"`) && json.Unmarshal([]byte(value), &decoded) == nil {
		return decoded
	}
	return value
}

func agyToolFloat(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	text := strings.TrimSpace(firstNonEmpty(agyToolString(raw), string(raw)))
	if text == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func agyToolStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) == nil {
		return out
	}
	if decoded := agyToolString(raw); strings.HasPrefix(strings.TrimSpace(decoded), "[") {
		if json.Unmarshal([]byte(decoded), &out) == nil {
			return out
		}
	}
	return nil
}

func agyParseTimerDescription(description string) time.Duration {
	m := agyTimerDescRe.FindStringSubmatch(strings.TrimSpace(description))
	if m == nil {
		return 0
	}
	var totalSec float64
	for _, unit := range agyTimerUnitRe.FindAllStringSubmatch(m[1], -1) {
		n, err := strconv.ParseFloat(unit[1], 64)
		if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			continue
		}
		switch strings.ToLower(unit[2]) {
		case "h":
			totalSec += n * 3600
		case "m":
			totalSec += n * 60
		default:
			totalSec += n
		}
	}
	if nanos := totalSec * float64(time.Second); totalSec > 0 && totalSec < 1e9 && nanos < 1<<63 {
		return time.Duration(nanos)
	}
	return 0
}

type agyBackgroundTasks struct {
	running    []proto.BackgroundTask
	ended      []proto.BackgroundTask
	nextExpiry time.Time
}

func (b agyBackgroundTasks) apply(s *proto.Session) {
	s.Background = nil
	if !agyStopped(s.Status) {
		s.Background = b.running
	}
	s.SetBackgroundEnded(recentBackgroundEnded(b.ended))
}

type agyTaskRecord struct {
	task          proto.BackgroundTask
	lastSeen      time.Time
	lastSeenOrder int
	inferred      bool
	oneShot       bool
	condition     string
	maxIterations int
	deadline      time.Time
	subagentID    string
}

func (r *agyTaskRecord) recordSeen(at time.Time, order int) {
	if !at.IsZero() {
		if r.lastSeen.IsZero() || at.After(r.lastSeen) {
			r.lastSeen = at
			r.lastSeenOrder = order
		} else if at.Equal(r.lastSeen) && order > r.lastSeenOrder {
			r.lastSeenOrder = order
		}
		return
	}
	if r.lastSeen.IsZero() && order > r.lastSeenOrder {
		r.lastSeenOrder = order
	}
}

func (r *agyTaskRecord) finish(status string, at time.Time, inferred bool) {
	if r == nil || (r.task.Status != "" && (!r.inferred || inferred)) {
		return
	}
	r.task.Status, r.task.EndedAt, r.inferred = status, &at, inferred
}

type agyBackgroundCall struct {
	planner      *agyStep
	plannerOrder int
	tool         *agyToolCall
}

type agyReconcileEvent struct {
	at         time.Time
	order      int
	status     string
	sender     string
	subagentID string
}

func compareAgyReconcileEvents(a, b agyReconcileEvent) int {
	if !a.at.IsZero() && !b.at.IsZero() {
		if cmp := a.at.Compare(b.at); cmp != 0 {
			return cmp
		}
	}
	return a.order - b.order
}

func agyEventAfterTask(ev agyReconcileEvent, lastSeen time.Time, lastSeenOrder int) bool {
	if !ev.at.IsZero() && !lastSeen.IsZero() {
		return ev.at.After(lastSeen) || (ev.at.Equal(lastSeen) && ev.order > lastSeenOrder)
	}
	return ev.order > lastSeenOrder
}

func agyTerminalStatus(raw string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DONE", "COMPLETED", "FINISHED":
		return proto.WorkflowCompleted, true
	case "ERROR", "FAILED":
		return proto.WorkflowFailed, true
	case "CANCELED", "CANCELLED", "KILLED", "STOPPED":
		return proto.WorkflowKilled, true
	default:
		return "", false
	}
}

func agyLiveStatus(raw string) bool {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "RUNNING", "PENDING", "IN_PROGRESS", "QUEUED":
		return true
	default:
		return false
	}
}

func agyIsOwnTaskID(conversation, id string) bool {
	name, ok := strings.CutPrefix(id, conversation+"/")
	return ok && agyTaskName.MatchString(name)
}

func agyFirstForeignUUID(text, conversation string) string {
	own := strings.ToLower(conversation)
	for _, m := range agyEmbeddedUUIDRe.FindAllString(text, -1) {
		if id := strings.ToLower(m); id != own {
			return id
		}
	}
	return ""
}

func agyBackground(steps []agyStep, conversation, dir string, brain *os.Root) agyBackgroundTasks {
	return agyBackgroundAt(steps, conversation, dir, brain, time.Time{})
}

func agyBackgroundAt(steps []agyStep, conversation, dir string, brain *os.Root, now time.Time) agyBackgroundTasks {
	byID := make(map[string]*agyTaskRecord)
	var ordered []*agyTaskRecord
	pending := make(map[int]agyBackgroundCall)
	var restarts []agyReconcileEvent
	var messages []agyReconcileEvent
	var inventories []agyReconcileEvent
	inventorySeen := make(map[string]agyReconcileEvent)
	statusSeen := make(map[string]agyReconcileEvent)
	statusEnds := make(map[string][]agyReconcileEvent)
	var subagentKillAlls []agyReconcileEvent
	subagentKills := make(map[string][]agyReconcileEvent)
	var subagentEmptyLists []agyReconcileEvent
	recordMapSeen := func(m map[string]agyReconcileEvent, id string, ev agyReconcileEvent) {
		if prev, exists := m[id]; !exists || compareAgyReconcileEvents(ev, prev) > 0 {
			m[id] = ev
		}
	}
	for i := range steps {
		step := &steps[i]
		at, _ := time.Parse(time.RFC3339Nano, step.CreatedAt)
		slot := pending[step.seq]
		delete(pending, step.seq)
		switch step.Type {
		case "PLANNER_RESPONSE":
			for offset, call := range step.ToolCalls {
				// Late results retain their native step index. An overlapping claim makes the slot ambiguous.
				seq := step.seq + 1 + offset
				if _, claimed := pending[seq]; claimed {
					pending[seq] = agyBackgroundCall{}
				} else {
					pending[seq] = agyBackgroundCall{planner: step, plannerOrder: i, tool: call}
				}
				if call == nil || call.Name != "manage_task" {
					continue
				}
				var args map[string]json.RawMessage
				if json.Unmarshal(call.Args, &args) == nil && strings.EqualFold(agyToolString(args["Action"]), "kill") {
					byID[agyQualifiedTaskID(conversation, agyToolString(args["TaskId"]))].finish(proto.WorkflowKilled, at, false)
				}
			}
		case "SYSTEM_MESSAGE":
			end := agyTaskEnd.FindStringSubmatch(step.Content)
			if end != nil {
				status := proto.WorkflowKilled
				switch strings.ToLower(end[2]) {
				case "finished":
					status = proto.WorkflowCompleted
				case "failed":
					status = proto.WorkflowFailed
				}
				byID[agyQualifiedTaskID(conversation, end[1])].finish(status, at, false)
			}
			m := agyTaskMessage.FindStringSubmatch(step.Content)
			if restartAt, ok := agyServerRestartTime(step.Content, m, at); ok {
				restarts = append(restarts, agyReconcileEvent{at: restartAt, order: i, status: proto.WorkflowKilled})
				continue
			}
			if m != nil {
				sender := agyQualifiedTaskID(conversation, m[2])
				if uuidRe.MatchString(strings.ToLower(m[2])) {
					sender = strings.ToLower(m[2])
				}
				message := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[3]), "</SYSTEM_MESSAGE>"))
				heartbeat := agyTaskHeartbeat.MatchString(message)
				var iteration []string
				if agyIsOwnTaskID(conversation, sender) {
					iteration = agyCronIteration.FindStringSubmatch(message)
				}
				cronCompleted := false
				msgAt := at
				if msgAt.IsZero() {
					msgAt, _ = time.Parse(time.RFC3339Nano, m[1])
				}
				if record := byID[sender]; record != nil {
					record.recordSeen(msgAt, i)
					if end == nil {
						if iteration != nil {
							record.oneShot = false
							record.condition = ""
							record.deadline = time.Time{}
							if n, err := strconv.Atoi(iteration[1]); err == nil && record.maxIterations > 0 && n >= record.maxIterations {
								record.finish(proto.WorkflowCompleted, msgAt, true)
								cronCompleted = true
							}
						} else if record.oneShot && !heartbeat {
							record.finish(proto.WorkflowCompleted, msgAt, false)
						}
					}
				}
				if message != "" && !heartbeat && !msgAt.IsZero() && (iteration == nil || cronCompleted) {
					var subID string
					if sender == "system" {
						if idle := agySubagentIdleRe.FindStringSubmatch(message); idle != nil {
							subID = strings.ToLower(idle[1])
						}
					}
					// General runtime notices do not deliver a task or subagent result.
					if end == nil && subID == "" && (sender == "system" || strings.HasPrefix(message, "[Notice]")) {
						continue
					}
					messages = append(messages, agyReconcileEvent{at: msgAt, order: i, sender: sender, subagentID: subID})
				}
			}
		case "GENERIC":
			var call *agyToolCall
			if slot.planner != nil && slot.planner.branch == step.branch {
				calledAt, _ := time.Parse(time.RFC3339Nano, slot.planner.CreatedAt)
				if at.IsZero() || calledAt.IsZero() || !at.Before(calledAt) {
					call = slot.tool
				}
				ev := agyReconcileEvent{at: calledAt, order: slot.plannerOrder}
				if ids, ok := agyTaskInventory(step, slot, calledAt, at, conversation); ok {
					inventories = append(inventories, ev)
					for id := range ids {
						recordMapSeen(inventorySeen, id, ev)
					}
				}
				if obs, ok := agyTaskStatusObservation(step, slot, calledAt, at, conversation); ok {
					if obs.live {
						recordMapSeen(statusSeen, obs.id, ev)
					} else {
						statusEnds[obs.id] = append(statusEnds[obs.id], agyReconcileEvent{at: calledAt, order: slot.plannerOrder, status: obs.status})
					}
				}
				if act, ok := agySubagentAction(step, slot, calledAt, at); ok {
					switch act.kind {
					case "kill_all":
						subagentKillAlls = append(subagentKillAlls, agyReconcileEvent{at: calledAt, order: slot.plannerOrder, status: proto.WorkflowKilled})
					case "empty_list":
						subagentEmptyLists = append(subagentEmptyLists, ev)
					case "kill":
						for _, subID := range act.ids {
							subagentKills[subID] = append(subagentKills[subID], agyReconcileEvent{at: calledAt, order: slot.plannerOrder, status: proto.WorkflowKilled})
						}
					}
				}
			}
			notice := agyBackgroundNotice(firstNonEmpty(step.backgroundStart, step.Content))
			id, details, _ := strings.Cut(notice, "\n")
			id = strings.TrimSpace(id)
			name, ours := strings.CutPrefix(id, conversation+"/")
			if !ours || !agyTaskName.MatchString(name) {
				continue
			}
			record := byID[id]
			if record == nil {
				task := proto.BackgroundTask{ID: id, Kind: "shell", StartedAt: at}
				for line := range strings.SplitSeq(details, "\n") {
					line = strings.TrimSpace(line)
					if value, ok := strings.CutPrefix(line, "Task Description:"); ok && task.Description == "" {
						task.Description = strings.Clone(strings.TrimSpace(value))
					}
					if value, ok := strings.CutPrefix(line, "Task logs are available at:"); ok && task.OutputFile == "" {
						task.OutputFile = agyTaskLog(strings.TrimSpace(value), dir, conversation, name, brain)
					}
				}
				if call != nil {
					setAgyTaskKind(&task, call.Name, call.Args)
				}
				oneShot, condition, duration, maxIterations := agyScheduleConfig(task.Description, call, conversation)
				record = &agyTaskRecord{
					task:          task,
					lastSeenOrder: i,
					oneShot:       oneShot,
					condition:     condition,
					maxIterations: maxIterations,
				}
				if oneShot && duration > 0 && !at.IsZero() {
					record.deadline = at.Add(duration)
				}
				if task.Kind == "agent" {
					record.subagentID = agyFirstForeignUUID(details, conversation)
				}
				byID[id] = record
				ordered = append(ordered, record)
			}
			record.recordSeen(at, i)
			if updated, err := time.Parse(time.RFC3339Nano, step.UpdatedAt); err == nil {
				at = updated
				record.recordSeen(updated, i)
			}
			if status, ok := agyTerminalStatus(step.Status); ok {
				record.finish(status, at, true)
			}
		}
	}
	slices.SortFunc(inventories, compareAgyReconcileEvents)
	slices.SortFunc(subagentEmptyLists, compareAgyReconcileEvents)
	var result agyBackgroundTasks
	pickEarliest := func(best *agyReconcileEvent, ev agyReconcileEvent, status string) *agyReconcileEvent {
		if status != "" {
			ev.status = status
		}
		if best == nil || compareAgyReconcileEvents(ev, *best) < 0 {
			copyEv := ev
			return &copyEv
		}
		return best
	}
	for _, record := range ordered {
		if record.task.Status == "" {
			lastSeen, lastSeenOrder := record.lastSeen, record.lastSeenOrder
			for _, seenMap := range []map[string]agyReconcileEvent{inventorySeen, statusSeen} {
				if seen, ok := seenMap[record.task.ID]; ok {
					if lastSeen.IsZero() || seen.at.After(lastSeen) || (seen.at.Equal(lastSeen) && seen.order > lastSeenOrder) {
						lastSeen, lastSeenOrder = seen.at, seen.order
					}
				}
			}
			var best *agyReconcileEvent
			for _, ev := range restarts {
				if agyEventAfterTask(ev, lastSeen, lastSeenOrder) {
					best = pickEarliest(best, ev, proto.WorkflowKilled)
				}
			}
			if !record.task.StartedAt.IsZero() && !lastSeen.IsZero() && len(inventories) > 0 {
				// Any snapshot strictly after the last evidence of this task omitted it.
				if j := sort.Search(len(inventories), func(j int) bool { return inventories[j].at.After(lastSeen) }); j < len(inventories) {
					best = pickEarliest(best, inventories[j], proto.WorkflowCompleted)
				}
			}
			if record.oneShot && record.condition != "" && !record.task.StartedAt.IsZero() {
				for _, ev := range messages {
					if ev.sender == record.task.ID {
						continue
					}
					if record.condition != "any" && record.condition != ev.sender &&
						(ev.subagentID == "" || record.condition != ev.subagentID) {
						continue
					}
					if !record.deadline.IsZero() && !ev.at.Before(record.deadline) {
						continue
					}
					if agyEventAfterTask(ev, lastSeen, lastSeenOrder) {
						best = pickEarliest(best, ev, proto.WorkflowKilled)
					}
				}
			}
			for _, ev := range statusEnds[record.task.ID] {
				if agyEventAfterTask(ev, lastSeen, lastSeenOrder) {
					best = pickEarliest(best, ev, "")
				}
			}
			if record.task.Kind == "agent" {
				for _, ev := range subagentKillAlls {
					if agyEventAfterTask(ev, lastSeen, lastSeenOrder) {
						best = pickEarliest(best, ev, "")
					}
				}
				if record.subagentID != "" {
					for _, ev := range subagentKills[record.subagentID] {
						if agyEventAfterTask(ev, lastSeen, lastSeenOrder) {
							best = pickEarliest(best, ev, "")
						}
					}
				}
				if !record.task.StartedAt.IsZero() && !lastSeen.IsZero() && len(subagentEmptyLists) > 0 {
					if j := sort.Search(len(subagentEmptyLists), func(j int) bool { return subagentEmptyLists[j].at.After(lastSeen) }); j < len(subagentEmptyLists) {
						best = pickEarliest(best, subagentEmptyLists[j], proto.WorkflowCompleted)
					}
				}
			}
			if best != nil {
				record.finish(best.status, best.at, true)
			}
		}
		if record.task.EndedAt == nil && record.oneShot && !record.deadline.IsZero() && !now.IsZero() {
			// Allow time for the timer's own completion message to reach the export.
			expiry := record.deadline.Add(agyTimerExpiryGrace)
			if !now.Before(expiry) {
				record.finish(proto.WorkflowCompleted, record.deadline, true)
			} else if result.nextExpiry.IsZero() || expiry.Before(result.nextExpiry) {
				result.nextExpiry = expiry
			}
		}
		if record.task.EndedAt != nil {
			result.ended = append(result.ended, record.task)
		} else if len(result.running) < maxRuntimeBackground {
			result.running = append(result.running, record.task)
		}
	}
	slices.SortStableFunc(result.ended, func(a, b proto.BackgroundTask) int { return a.EndedAt.Compare(*b.EndedAt) })
	if extra := len(result.ended) - agyBackgroundEndedLimit; extra > 0 {
		result.ended = slices.Clone(result.ended[extra:])
	}
	return result
}

func agyQualifiedTaskID(conversation, id string) string {
	if agyTaskName.MatchString(id) {
		return conversation + "/" + id
	}
	return id
}

func agySystemMessageBody(content string) string {
	trimmed := strings.TrimSpace(content)
	if i := strings.LastIndex(trimmed, "<SYSTEM_MESSAGE>"); i >= 0 {
		trimmed = trimmed[i+len("<SYSTEM_MESSAGE>"):]
	}
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimSuffix(trimmed, "</SYSTEM_MESSAGE>")
	return strings.TrimSpace(trimmed)
}

// agyServerRestartTime recognizes the global stop notice Antigravity writes when its server restarts.
func agyServerRestartTime(content string, message []string, createdAt time.Time) (time.Time, bool) {
	if message != nil {
		if message[2] != "system" {
			return time.Time{}, false
		}
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(message[3]), "</SYSTEM_MESSAGE>"))
		if !agyServerRestart.MatchString(body) {
			return time.Time{}, false
		}
		if !createdAt.IsZero() {
			return createdAt, true
		}
		ts, _ := time.Parse(time.RFC3339Nano, message[1])
		return ts, true
	}
	if !agyServerRestart.MatchString(agySystemMessageBody(content)) {
		return time.Time{}, false
	}
	return createdAt, true
}

func agyValidToolResult(step *agyStep, slot agyBackgroundCall, calledAt, at time.Time) bool {
	if step.Error != "" || !strings.EqualFold(step.Status, "DONE") ||
		step.Index == nil || *step.Index < 0 || slot.planner.Index == nil || *slot.planner.Index < 0 ||
		calledAt.IsZero() || at.IsZero() || at.Before(calledAt) ||
		slices.Contains(slot.planner.TruncatedFields, "tool_calls") {
		return false
	}
	// The tool's timestamp can predate the result step when an old result is exported late.
	if header, ok := strings.CutPrefix(step.Content, "Created At: "); ok {
		created, _, _ := strings.Cut(header, "\n")
		started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(created))
		if err != nil || started.Before(calledAt) {
			return false
		}
	}
	return true
}

// agyTaskInventory accepts complete, unfiltered manage_task(Action="list") results from the current conversation.
func agyTaskInventory(step *agyStep, slot agyBackgroundCall, calledAt, at time.Time, conversation string) (map[string]struct{}, bool) {
	call := slot.tool
	if call == nil || call.Name != "manage_task" || !agyValidToolResult(step, slot, calledAt, at) ||
		slices.Contains(step.TruncatedFields, "content") {
		return nil, false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(call.Args, &args) != nil || !strings.EqualFold(agyToolString(args["Action"]), "list") {
		return nil, false
	}
	// A nonempty selector or an unknown option may make the inventory a subset.
	delete(args, "Action")
	if raw, ok := args["TaskId"]; ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "null" || (strings.HasPrefix(trimmed, `"`) && agyToolString(raw) == "") {
			delete(args, "TaskId")
		}
	}
	if len(args) != 0 {
		return nil, false
	}
	output := strings.TrimSpace(agyToolOutput(step.Content))
	if output == agyEmptyTaskList {
		return nil, true
	}
	header, rows, found := strings.Cut(output, "\n")
	if !found {
		return nil, false
	}
	countText, ok := strings.CutPrefix(strings.TrimSpace(header), "You have ")
	if !ok {
		return nil, false
	}
	countText, ok = strings.CutSuffix(countText, " background task(s) currently running:")
	count, err := strconv.Atoi(countText)
	if !ok || err != nil || count <= 0 {
		return nil, false
	}
	body := strings.TrimSpace(rows)
	ids := make(map[string]struct{})
	if strings.HasPrefix(body, "[") {
		var items []struct {
			TaskID string `json:"taskId"`
		}
		if json.Unmarshal([]byte(body), &items) != nil || len(items) != count {
			return nil, false
		}
		for _, item := range items {
			id := agyQualifiedTaskID(conversation, strings.TrimSpace(item.TaskID))
			if !agyIsOwnTaskID(conversation, id) {
				return nil, false
			}
			if _, duplicate := ids[id]; duplicate {
				return nil, false
			}
			ids[id] = struct{}{}
		}
		return ids, len(ids) == count
	}
	for line := range strings.SplitSeq(body, "\n") {
		match := agyTaskListRow.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			return nil, false
		}
		id := agyQualifiedTaskID(conversation, match[1])
		if !agyIsOwnTaskID(conversation, id) {
			return nil, false
		}
		if _, duplicate := ids[id]; duplicate {
			return nil, false
		}
		ids[id] = struct{}{}
	}
	return ids, len(ids) == count
}

type agyStatusObservation struct {
	id     string
	live   bool
	status string
}

// agyTaskStatusObservation reads authoritative manage_task(Action="status", TaskId=...) results.
func agyTaskStatusObservation(step *agyStep, slot agyBackgroundCall, calledAt, at time.Time, conversation string) (agyStatusObservation, bool) {
	call := slot.tool
	if call == nil || call.Name != "manage_task" || !agyValidToolResult(step, slot, calledAt, at) {
		return agyStatusObservation{}, false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(call.Args, &args) != nil || !strings.EqualFold(agyToolString(args["Action"]), "status") {
		return agyStatusObservation{}, false
	}
	requested := agyQualifiedTaskID(conversation, strings.TrimSpace(agyToolString(args["TaskId"])))
	if !agyIsOwnTaskID(conversation, requested) {
		return agyStatusObservation{}, false
	}
	m := agyTaskStatusRe.FindStringSubmatch(strings.TrimLeft(agyToolOutput(step.Content), " \t\r\n"))
	if m == nil || agyQualifiedTaskID(conversation, strings.TrimSpace(m[1])) != requested {
		return agyStatusObservation{}, false
	}
	if agyLiveStatus(m[2]) {
		return agyStatusObservation{id: requested, live: true}, true
	}
	if status, ok := agyTerminalStatus(m[2]); ok {
		return agyStatusObservation{id: requested, status: status}, true
	}
	return agyStatusObservation{}, false
}

type agySubagentOp struct {
	kind string
	ids  []string
}

// agySubagentAction reads successful manage_subagents actions for agent-kind background tasks.
func agySubagentAction(step *agyStep, slot agyBackgroundCall, calledAt, at time.Time) (agySubagentOp, bool) {
	call := slot.tool
	if call == nil || call.Name != "manage_subagents" || !agyValidToolResult(step, slot, calledAt, at) {
		return agySubagentOp{}, false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(call.Args, &args) != nil {
		return agySubagentOp{}, false
	}
	switch strings.ToLower(strings.TrimSpace(agyToolString(args["Action"]))) {
	case "kill_all":
		return agySubagentOp{kind: "kill_all"}, true
	case "kill":
		var ids []string
		for _, raw := range agyToolStringSlice(args["ConversationIds"]) {
			id := strings.ToLower(strings.TrimSpace(raw))
			if uuidRe.MatchString(id) {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			return agySubagentOp{kind: "kill", ids: ids}, true
		}
	case "list":
		if !slices.Contains(step.TruncatedFields, "content") &&
			agyEmptySubagentRe.MatchString(strings.TrimSpace(agyToolOutput(step.Content))) {
			return agySubagentOp{kind: "empty_list"}, true
		}
	}
	return agySubagentOp{}, false
}

// agyScheduleConfig extracts timer/cron settings from the schedule tool call or task description.
// Cron takes precedence over one-shot timer classification.
func agyScheduleConfig(description string, call *agyToolCall, conversation string) (oneShot bool, condition string, duration time.Duration, maxIterations int) {
	var args map[string]json.RawMessage
	if call != nil && call.Name == "schedule" {
		_ = json.Unmarshal(call.Args, &args)
	}
	if rawMax, ok := agyToolFloat(args["MaxIterations"]); ok && rawMax > 0 && rawMax == math.Trunc(rawMax) && rawMax < 1e9 {
		maxIterations = int(rawMax)
	}
	if strings.HasPrefix(description, "Cron:") || strings.TrimSpace(agyToolString(args["CronExpression"])) != "" {
		return false, "", 0, maxIterations
	}
	rawDur := args["DurationSeconds"]
	hasDur := rawDur != nil && string(rawDur) != "null"
	if hasDur || strings.HasPrefix(description, "Timer:") {
		condition = strings.TrimSpace(agyToolString(args["TimerCondition"]))
		switch lower := strings.ToLower(condition); lower {
		case "never":
			condition = ""
		case "any":
			condition = "any"
		default:
			if uuidRe.MatchString(lower) {
				condition = lower
			} else if condition != "" {
				condition = agyQualifiedTaskID(conversation, condition)
			}
		}
		if secs, ok := agyToolFloat(rawDur); ok && secs > 0 && secs < 1e9 {
			duration = time.Duration(secs * float64(time.Second))
		} else {
			duration = agyParseTimerDescription(description)
		}
		return true, condition, duration, 0
	}
	return false, "", 0, maxIterations
}

func setAgyTaskKind(task *proto.BackgroundTask, tool string, raw json.RawMessage) {
	switch tool {
	case "schedule":
		task.Kind = "monitor"
	case "run_workflow":
		task.Kind = "workflow"
	case "invoke_subagent":
		task.Kind = "agent"
	case "run_command":
		var args map[string]json.RawMessage
		if json.Unmarshal(raw, &args) == nil {
			task.Command = firstNonEmpty(agyToolString(args["CommandLine"]), agyToolString(args["command"]))
		}
	}
}

// The export can name only this task's log inside its conversation directory.
func agyTaskLog(uri, dir, conversation, name string, brain *os.Root) string {
	path, err := url.PathUnescape(agyLocalFileURLToPath(uri))
	rel := filepath.Join(".system_generated", "tasks", name+".log")
	expected, absErr := filepath.Abs(filepath.Join(dir, "brain", conversation, rel))
	// The log can be announced before it is created.
	if err != nil || absErr != nil || !filepath.IsAbs(path) || filepath.Clean(path) != expected {
		return ""
	}
	info, err := brain.Stat(rel)
	if os.IsNotExist(err) || (err == nil && info.Mode().IsRegular()) {
		return expected
	}
	return ""
}

func agyStopped(status string) bool {
	return status == proto.StatusOffline || status == proto.StatusInterrupted
}

// agyPaneBackgroundFromRows reads the tasks the companion hides on ACP history rows.
func agyPaneBackgroundFromRows(params json.RawMessage) map[string][]proto.BackgroundTask {
	var push struct {
		Sessions []struct {
			ID    string                 `json:"session_id"`
			Tasks []proto.BackgroundTask `json:"agy_pane_background"`
		} `json:"sessions"`
	}
	if json.Unmarshal(params, &push) != nil {
		return nil
	}
	var background map[string][]proto.BackgroundTask
	for _, row := range push.Sessions {
		if len(row.Tasks) == 0 || !runtimeIDRe.MatchString(row.ID) {
			continue
		}
		if background == nil {
			background = make(map[string][]proto.BackgroundTask)
		}
		background[row.ID] = runtimeBackground(row.Tasks, maxRuntimeBackground, true)
	}
	return background
}

// agyPaneRuntimeRow reads the companion row and its pane inventory from the same list update.
func (b *runtimeRegistry) agyPaneRuntimeRow(id string) (proto.Session, agyPreview, bool) {
	h := b.ownerOf(id)
	if h == nil {
		return proto.Session{}, agyPreview{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hello.Runtime != agyRuntimeID || h.daemon != "" {
		return proto.Session{}, agyPreview{}, false
	}
	row, ok := h.rowLocked(id)
	if tasks := h.agyPaneBackground[id]; len(row.Background) == 0 && len(tasks) > 0 {
		row.Background = tasks
	}
	return row, h.agyPreviews[id], ok
}

// Runtime task metadata cannot grant access to node-local files. Join the log
// paths independently confirmed by the local conversation's transcript.
func (n *Node) agyRuntimeTaskLogs(s *proto.Session, conversation string) {
	if len(s.Background) == 0 && len(s.BackgroundEnded()) == 0 {
		return
	}
	meta, ok := n.agyTranscriptMeta(conversation)
	if !ok {
		return
	}
	logs := make(map[string]string)
	for _, tasks := range [][]proto.BackgroundTask{meta.background.running, meta.background.ended} {
		for _, task := range tasks {
			if task.OutputFile != "" {
				logs[task.ID] = task.OutputFile
			}
		}
	}
	if len(logs) == 0 {
		return
	}
	attach := func(tasks []proto.BackgroundTask) []proto.BackgroundTask {
		tasks = slices.Clone(tasks)
		for i := range tasks {
			tasks[i].OutputFile = logs[tasks[i].ID]
		}
		return tasks
	}
	s.Background = attach(s.Background)
	s.SetBackgroundEnded(attach(s.BackgroundEnded()))
}
