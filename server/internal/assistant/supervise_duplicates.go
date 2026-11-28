package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- duplicates ----

// duplicateHints are the tasks going on and the agents at work that look like the same work as task t: a shared
// issue / PR number, or the same machine and directory with a similar title, or a very similar title anywhere.
func duplicateHints(t PlanTask, todos []Todo, sessions []proto.Session) []string {
	refs := issueRefs(t.Title + " " + t.Prompt)
	var out []string
	seen := map[string]bool{}
	add := func(key, line string) {
		if !seen[key] && len(out) < 4 {
			seen[key] = true
			out = append(out, line)
		}
	}
	for _, o := range todos {
		if o.DoneAt != nil {
			continue
		}
		sameDir := false
		for _, a := range o.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil && a.MachineID == t.Machine && dirsOverlap(a.Cwd, t.Cwd) {
				sameDir = true
			}
		}
		if shared := sharedRef(refs, issueRefs(o.Title+" "+o.Notes+" "+o.PRURL)); shared != "" {
			add("todo:"+o.ID, fmt.Sprintf("任务「%s」（%s）也提到 %s", o.Title, o.ID, shared))
		} else if sim := titleSimilarity(t.Title, o.Title); sim >= 0.7 || sameDir && sim >= 0.4 {
			add("todo:"+o.ID, fmt.Sprintf("任务「%s」（%s）标题相近%s", o.Title, o.ID, map[bool]string{true: "，在同一个目录", false: ""}[sameDir]))
		}
	}
	for _, c := range sessions {
		if c.Assistant || c.State != proto.StateWorking && c.State != proto.StateNeedsYou {
			continue
		}
		title := cmp.Or(c.Title, c.LastText)
		where := fmt.Sprintf("%s 上的 Agent「%s」（session_id=%s）", c.MachineID, clip(oneLine(title), 40), short(c.SessionID))
		sameDir := c.MachineID == t.Machine && dirsOverlap(c.Cwd, t.Cwd)
		if shared := sharedRef(refs, issueRefs(c.Title+" "+c.LastText)); shared != "" {
			add("agent:"+c.MachineID+"/"+c.SessionID, where+"也在做 "+shared)
		} else if sim := titleSimilarity(t.Title, c.Title); sim >= 0.7 || sameDir && sim >= 0.4 {
			add("agent:"+c.MachineID+"/"+c.SessionID, where+"在做相近的事")
		}
	}
	return out
}

// issueRefs are the issue / PR numbers s names: #123, issues/123, pull/123 (three digits or more: not a list's #1).
func issueRefs(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		var rest string
		switch {
		case s[i] == '#':
			rest = s[i+1:]
		case strings.HasPrefix(s[i:], "issues/"):
			rest = s[i+len("issues/"):]
		case strings.HasPrefix(s[i:], "pull/"):
			rest = s[i+len("pull/"):]
		default:
			continue
		}
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n >= 3 && n <= 7 {
			ref := "#" + rest[:n]
			if !slices.Contains(out, ref) {
				out = append(out, ref)
			}
		}
	}
	return out
}

func sharedRef(a, b []string) string {
	for _, x := range a {
		if slices.Contains(b, x) {
			return x
		}
	}
	return ""
}

// dirsOverlap: one directory is the other or inside it.
func dirsOverlap(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// titleSimilarity is how alike two short titles are: the Dice coefficient of their character bigrams (spaces and
// punctuation dropped, letters folded), 0 to 1.
func titleSimilarity(a, b string) float64 {
	ga, gb := bigrams(a), bigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	common := 0
	left := map[string]int{}
	for _, g := range gb {
		left[g]++
	}
	for _, g := range ga {
		if left[g] > 0 {
			left[g]--
			common++
		}
	}
	return 2 * float64(common) / float64(len(ga)+len(gb))
}

func bigrams(s string) []string {
	var r []rune
	for _, c := range strings.ToLower(s) {
		if c == ' ' || c == '\t' || utf8.RuneLen(c) == 1 && !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') ||
			strings.ContainsRune("，。、：；！？「」（）《》【】·…—", c) {
			continue
		}
		r = append(r, c)
	}
	out := make([]string, 0, len(r))
	for i := 0; i+1 < len(r); i++ {
		out = append(out, string(r[i:i+2]))
	}
	return out
}

// reuseHint is an active agent on an open to-do the caller opened or supervises, working in the same directory as new
// work being planned. Unlike duplicateHints it does not rely on title similarity: a follow-up is often worded very
// differently from the original task, so a same-directory active agent is itself the signal (#13192).
type reuseHint struct {
	todoID    string
	todoTitle string
	machineID string
	sessionID string
}

// reuseHints lists, at most limit, active agents on still-open to-dos conversation self opened or supervises whose
// working directory overlaps cwd. Hints never block: only the model can tell whether the new work is the same topic.
func (s *Service) reuseHints(ctx context.Context, self, cwd string, limit int) []reuseHint {
	if self == "" || cwd == "" || limit <= 0 || s.ensureTodosLoaded(ctx) != nil {
		return nil
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var out []reuseHint
	seen := map[string]bool{}
	for _, t := range s.todos.todos {
		if t.DoneAt != nil || (t.Origin != self && (t.Supervision == nil || t.Supervision.Supervisor != self)) {
			continue
		}
		for _, a := range t.Agents {
			if !a.active() || !dirsOverlap(a.Cwd, cwd) {
				continue
			}
			key := a.MachineID + "/" + a.SessionID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, reuseHint{todoID: t.ID, todoTitle: t.Title, machineID: a.MachineID, sessionID: a.SessionID})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// reuseHintText renders reuseHints as a non-blocking paragraph for a tool answer, with the exact ids the next call
// needs. "" when there is none.
func reuseHintText(kind string, hints []reuseHint) string {
	if len(hints) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nAn Agent is already active on related work in this directory. Before starting a new one, check whether the owner's request can go to it (the id is exact, copy it):\n")
	for _, h := range hints {
		fmt.Fprintf(&b, "- to-do %s (%q): agent_send machine=%s session_id=%s", h.todoID, clip(oneLine(h.todoTitle), 40), h.machineID, short(h.sessionID))
		switch kind {
		case "task_plan":
			b.WriteString("; once that to-do is closed and its Agent released, task_plan with its todo_id and continues reuses the to-do")
		case "todo_create":
			b.WriteString("; use task_plan with todo_id to put an Agent on the new to-do, or agent_send for work that belongs on the existing one")
		}
		b.WriteString("\n")
	}
	b.WriteString("Parallel work is still allowed: start the new task anyway and say in why it cannot reuse that Agent. When the Agent has stopped and the follow-up belongs on its still-open to-do, resume that same session with agent_send resume=true and the to-do's todo_id; a task_plan continues run starts a successor Agent on the to-do instead.")
	return b.String()
}

// activeReuseHints lists active agents on open to-dos conversation self opened or supervises, regardless of
// directory: todo_create carries no cwd, so the conversation's own active agents are the signal there (#13192).
func (s *Service) activeReuseHints(ctx context.Context, self string, limit int) []reuseHint {
	if self == "" || limit <= 0 || s.ensureTodosLoaded(ctx) != nil {
		return nil
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var out []reuseHint
	seen := map[string]bool{}
	for _, t := range s.todos.todos {
		if t.DoneAt != nil || (t.Origin != self && (t.Supervision == nil || t.Supervision.Supervisor != self)) {
			continue
		}
		for _, a := range t.Agents {
			if !a.active() {
				continue
			}
			key := a.MachineID + "/" + a.SessionID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, reuseHint{todoID: t.ID, todoTitle: t.Title, machineID: a.MachineID, sessionID: a.SessionID})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// planReuseHints gathers reuseHints for every planned task's working directory, de-duplicated by agent session.
func (s *Service) planReuseHints(ctx context.Context, self string, tasks []PlanTask) []reuseHint {
	var out []reuseHint
	seen := map[string]bool{}
	for _, t := range tasks {
		for _, h := range s.reuseHints(ctx, self, t.Cwd, 4) {
			key := h.machineID + "/" + h.sessionID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, h)
		}
	}
	return out
}

// planDuplicates adds existing work and idle reuse hints to the task_plan receipt.
// A nil reuseSpace excludes idle hints for Agent callers with restricted messaging.
func (s *Service) planDuplicates(ctx context.Context, tasks []PlanTask, reuseSpace *string) string {
	var open []Todo
	todosLoaded := s.ensureTodosLoaded(ctx) == nil
	if todosLoaded {
		own := map[string]bool{} // a todo a task runs on is that task, not a copy of it
		for _, t := range tasks {
			if t.ReuseTodo != "" {
				own[t.ReuseTodo] = true
			}
		}
		s.todos.mu.RLock()
		for _, t := range s.todos.todos {
			if t.DoneAt == nil && !own[t.ID] {
				open = append(open, copyTodoLocked(t))
			}
		}
		s.todos.mu.RUnlock()
	}
	var sessions []proto.Session
	for _, d := range s.devices(ctx) {
		if !d.Reachable {
			continue
		}
		list, err := s.sessions(ctx, d.ID)
		if err != nil {
			continue
		}
		for _, c := range list {
			if c.HiddenByDirectory() {
				continue
			}
			if c.MachineID == "" {
				c.MachineID = d.ID
			}
			sessions = append(sessions, c)
		}
	}
	var b strings.Builder
	for _, t := range tasks {
		todos := open
		if t.Continues != "" {
			// The to-do it says it carries on (task_brief_plan.go) is not a copy of it; any other open to-do like it is
			// (#6427: two continuations of one stopped task).
			todos = slices.DeleteFunc(slices.Clone(open), func(o Todo) bool { return o.ID == t.Continues })
		}
		hints := duplicateHints(t, todos, sessions)
		if len(hints) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n- 「%s」可能和这些重复：%s", t.Title, strings.Join(hints, "；"))
	}
	reuse := ""
	if reuseSpace != nil && todosLoaded {
		reuse = s.idlePlanReuse(*reuseSpace, tasks, sessions)
	}
	if b.Len() == 0 {
		return reuse
	}
	return "\n\nPossible duplicates (work already going on): look at them (agent_read, the todo) before this goes on; if it is the " +
		"same work, cancel the new task (task_review cancelled, or dismiss the plan) or send what is new to the agent already on it. " +
		"If the new task takes over from one of them (a retry elsewhere, a rework round, a part split off), plan it again with " +
		"continues set to that todo id and continues_why, so it keeps that task's brief (the owner's request and criteria); " +
		"if it is different work, say why in your answer." + b.String() + reuse
}
