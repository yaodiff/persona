package assistant

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const compactRecoveryBytes = 6 * 1024

// compactRecoveryData is the source boundary for the future unified ledger. Collection never summarizes quotes;
// assembly owns budgets and rendering, independent of today's todo, direction and knowledge-base stores.
type compactRecoveryData struct {
	State, Rules, Decisions, Pins []prompts.RecoveryEntry
}

func assembleCompactRecovery(language, id string, data compactRecoveryData, budget int) string {
	d := prompts.CompactRecovery{Language: language, ID: id}
	render := func() string { return prompts.RenderCompactRecovery(d) }
	if len(render()) > budget {
		return ""
	}
	// Reserve space for each category; the complete rendered block, including escaped text and wrappers, is bounded.
	add := func(entries []prompts.RecoveryEntry, target *[]prompts.RecoveryEntry, capBytes int, truncate bool) {
		used := 0
		for _, e := range entries {
			e.Source = html.EscapeString(e.Source)
			e.Text = html.EscapeString(e.Text)
			if strings.TrimSpace(e.Text) == "" {
				continue
			}
			before := len(render())
			*target = append(*target, e)
			cost := len(render()) - before
			entryLimit := capBytes - used
			if truncate && len(entries) > 1 {
				entryLimit = min(entryLimit, capBytes/2)
			}
			if truncate && cost > entryLimit {
				excess := cost - entryLimit
				n := len(e.Text) - excess - 4
				if n > 0 {
					for n > 0 && !utf8.ValidString(e.Text[:n]) {
						n--
					}
					e.Text = e.Text[:n] + " …"
					(*target)[len(*target)-1] = e
				}
			}
			cost = len(render()) - before
			if cost > capBytes-used || len(render()) > budget {
				*target = (*target)[:len(*target)-1]
				continue
			}
			used += cost
		}
	}
	available := budget - len(render())
	add(data.Rules, &d.Rules, available*12/100, false)
	add(data.State, &d.State, available*25/100, true)
	add(data.Decisions, &d.Decisions, available*45/100, false)
	add(data.Pins, &d.Pins, available*18/100, false)
	return render()
}

func recoveryTodoInScope(t Todo, session string, main bool) bool {
	return main || t.Origin == session || t.Supervision != nil && t.Supervision.Supervisor == session
}

// compactRecoverySource reads only records in this managed conversation's scope. Agent outcomes are deliberately
// excluded from user decisions. Thread/card/manual choices retain their own durable provenance.
func (s *Service) compactRecoverySource(ctx context.Context, p ContextParams) compactRecoveryData {
	var out compactRecoveryData
	if p.SessionID == "" || len(p.RecoveryID) > 128 {
		return out
	}
	cfg := s.settings(ctx)
	main := cfg.Main == p.SessionID && cfg.mainRouteMachine() == p.MachineID
	leader := s.isLeader(ctx, p.SessionID)
	if d := s.directionOf(p.SessionID); leader && (d == nil || cmp.Or(d.MachineID, cfg.MachineID) != p.MachineID || d.EndedAt != nil) {
		leader = false
	}
	_ = s.ensureTodosLoaded(ctx)
	worker, isWorker := s.openTodoOfAgent(p.MachineID, p.SessionID)
	if !main && !leader && !isWorker {
		return out
	}
	var todos []Todo
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if isAskTodo(t.ID) {
			continue
		}
		if isWorker && t.ID == worker.ID || !isWorker && recoveryTodoInScope(*t, p.SessionID, main) {
			todos = append(todos, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	slices.SortFunc(todos, func(a, b Todo) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	slugs := []string{}
	lead := p.SessionID
	if isWorker {
		if id, _, ok := s.directionForTodo(ctx, worker); ok {
			lead = id
		}
	}
	if d := s.directionOf(lead); d != nil && d.EndedAt == nil {
		for _, line := range strings.Split(d.Notes, "\n") {
			out.Rules = append(out.Rules, prompts.RecoveryEntry{Source: "direction/" + lead + "/notes", Text: line})
		}
		if d.Project != "" {
			slugs = append(slugs, d.Project)
		}
	}
	for _, line := range strings.Split(s.notes(ctx), "\n") {
		out.Rules = append(out.Rules, prompts.RecoveryEntry{Source: "assistant/about", Text: line})
	}
	var decisions []db.TodoDecisionRow
	recentStore, bulk := s.todoDB().(interface {
		TodoDecisionsSince(context.Context, time.Time) ([]db.TodoDecisionRow, error)
	})
	scope := map[string]bool{}
	for _, t := range todos {
		scope[t.ID] = true
		if t.DoneAt == nil {
			state := fmt.Sprintf("%s\nstage=%s\n%s", t.Title, t.Stage, t.Notes)
			if t.Supervision != nil {
				state += "\ndone_when: " + strings.Join(t.Supervision.DoneWhen, "; ") + "\nstate=" + t.Supervision.State
			}
			for _, a := range t.Agents {
				state += fmt.Sprintf("\nsession=%s/%s", a.MachineID, a.SessionID)
			}
			out.State = append(out.State, prompts.RecoveryEntry{Source: "todo/" + t.ID, Text: state})
			if t.Project != "" && !slices.Contains(slugs, t.Project) {
				slugs = append(slugs, t.Project)
			}
		}
		if st := s.todoDB(); st != nil && (isWorker || !bulk) {
			rows, err := st.ListTodoDecisions(ctx, t.ID, false)
			if err != nil {
				s.logf("compact recovery: decisions unavailable for %s", t.ID)
				continue
			}
			for _, r := range rows {
				if isWorker || !r.CreatedAt.Before(time.Now().AddDate(0, 0, -7)) {
					decisions = append(decisions, r)
				}
			}
		}
	}
	if bulk && !isWorker {
		rows, err := recentStore.TodoDecisionsSince(ctx, time.Now().AddDate(0, 0, -7))
		if err == nil {
			for _, r := range rows {
				if scope[r.TodoID] {
					decisions = append(decisions, r)
				}
			}
		} else {
			s.logf("compact recovery: recent decisions unavailable")
		}
	}
	slices.SortFunc(decisions, func(a, b db.TodoDecisionRow) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	seenDecisions := map[string]bool{}
	for _, r := range decisions {
		if r.SupersededBy != "" || r.Source != db.DecisionManual && r.Source != db.DecisionThread && r.Source != db.DecisionCard {
			continue
		}
		seenDecisions[normalizeMemoryLine(r.Choice)] = true
		out.Decisions = append(out.Decisions, prompts.RecoveryEntry{
			Source: fmt.Sprintf("todo/%s/decision/%s source=%s %s/%s/%s at=%s", r.TodoID, r.ID, r.Source, r.SourceMachine, r.SourceSession, r.SourceMessage, r.CreatedAt.Format(time.RFC3339)),
			Text:   r.Choice,
		})
	}
	if main || leader {
		projectSlug := ""
		if leader {
			projectSlug, _ = s.leaderBoundProject(ctx, p.SessionID)
		}
		for _, entry := range s.scopedLedgerUserDecisions(ctx, p.MachineID, p.SessionID, main, projectSlug, leaderDecisionsMax) {
			if norm := normalizeMemoryLine(entry.Text); norm != "" && !seenDecisions[norm] {
				seenDecisions[norm] = true
				out.Decisions = append(out.Decisions, entry)
			}
		}
	}
	if leader {
		if _, projectEntries := s.leaderProjectMemoryEntries(ctx, p.MachineID, p.SessionID); len(projectEntries) > 0 {
			out.State = append(out.State, projectEntries...)
		}
	}
	if e, err := s.kbGet(ctx); err == nil && e != nil {
		for _, slug := range slugs {
			project := e.FindProject(slug)
			if project == nil || !project.Live() {
				continue
			}
			state := e.Store.ProjectSummary(project.Slug, 2000)
			source := "project/" + project.Slug + "/summary.md"
			// STATE is optional in existing projects; never follow arbitrary paths supplied by a session.
			if f, err := os.Open(e.Store.Path(kb.KindProject, project.Slug, "STATE.md")); err == nil {
				raw, readErr := io.ReadAll(io.LimitReader(f, 8192))
				_ = f.Close()
				if readErr == nil {
					state = strings.ToValidUTF8(string(raw), "")
					source = "project/" + project.Slug + "/STATE.md"
				}
			}
			if state == "" {
				state = project.OneLine
			}
			out.State = append(out.State, prompts.RecoveryEntry{Source: source, Text: state})
		}
	}
	out.Pins = s.compactRecoveryPins(ctx, p)
	return out
}

// Only platform-owned roles get recovery. A machine/session pair must match the recorded owner.
func (s *Service) compactRecoveryBudget(ctx context.Context, p ContextParams) int {
	if p.SessionID == "" || len(p.RecoveryID) > 128 {
		return 0
	}
	cfg := s.settings(ctx)
	if cfg.Main == p.SessionID && cfg.mainRouteMachine() == p.MachineID {
		return compactRecoveryBytes
	}
	if s.isLeader(ctx, p.SessionID) {
		if d := s.directionOf(p.SessionID); d != nil && cmp.Or(d.MachineID, cfg.MachineID) == p.MachineID && d.EndedAt == nil {
			return compactRecoveryBytes
		}
	}
	if s.ensureTodosLoaded(ctx) == nil {
		if _, ok := s.openTodoOfAgent(p.MachineID, p.SessionID); ok {
			return 3 * 1024
		}
	}
	return 0
}

// Cache source records ahead of a native mid-turn hook. Cached data carries no checkpoint or authorization.
type compactRecoveryCache struct {
	mu      sync.Mutex
	entries map[[2]string]compactRecoveryCached
	busy    map[[2]string]bool
}
type compactRecoveryCached struct {
	data compactRecoveryData
	at   time.Time
}

func (s *Service) prepareCompactRecovery(ctx context.Context, p ContextParams) {
	if s.isWorkMachine(p.MachineID) || s.isGuest(ctx, p.MachineID, p.SessionID) || s.compactRecoveryBudget(ctx, p) == 0 {
		return
	}
	key := [2]string{p.MachineID, p.SessionID}
	c := &s.compactRecovery
	c.mu.Lock()
	if c.busy[key] {
		c.mu.Unlock()
		return
	}
	if c.busy == nil {
		c.busy = map[[2]string]bool{}
	}
	c.busy[key] = true
	c.mu.Unlock()
	go func() {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		data := s.compactRecoverySource(readCtx, p)
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.busy, key)
		if readCtx.Err() != nil {
			return
		}
		if c.entries == nil || len(c.entries) >= 512 {
			c.entries = map[[2]string]compactRecoveryCached{}
		}
		c.entries[key] = compactRecoveryCached{data: data, at: time.Now()}
	}()
}

func (s *Service) compactRecoveryContext(ctx context.Context, p ContextParams) ContextResult {
	if p.RecoveryID == "" || s.cleanRoundSession(p.SessionID) || s.isWorkMachine(p.MachineID) || s.isGuest(ctx, p.MachineID, p.SessionID) {
		return ContextResult{}
	}
	budget := s.compactRecoveryBudget(ctx, p)
	if budget == 0 {
		return ContextResult{}
	}
	var data compactRecoveryData
	cached := false
	if p.RecoveryOnly {
		c := &s.compactRecovery
		c.mu.Lock()
		entry, ok := c.entries[[2]string{p.MachineID, p.SessionID}]
		c.mu.Unlock()
		if ok && time.Since(entry.at) < 2*time.Minute {
			data = entry.data
			cached = true
		}
	}
	if !cached {
		data = s.compactRecoverySource(ctx, p)
	} else if pins := s.compactRecoveryPins(ctx, p); len(pins) > 0 {
		data.Pins = pins
	}
	return ContextResult{RecoveryID: p.RecoveryID, Recovery: assembleCompactRecovery(answerLanguage(s.hello(ctx).Locale), p.RecoveryID, data, budget)}
}

func (s *Service) compactRecoveryPins(ctx context.Context, p ContextParams) []prompts.RecoveryEntry {
	var pins []prompts.RecoveryEntry
	// Recent user inputs are pins, not inferred decisions. Exclude native senders as well as injected provenance.
	var recent struct {
		Items []proto.Item `json:"items"`
	}
	readCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if s.call(readCtx, p.MachineID, DeviceSessionItems, map[string]any{"session_id": p.SessionID, "turns": 4}, &recent) == nil {
		for i := len(recent.Items) - 1; i >= 0 && len(pins) < 3; i-- {
			it := recent.Items[i]
			if it.Role != "user" || it.Kind != proto.KindText || it.From != "" || it.FullSize > 0 || it.Stored != nil || prompts.ItemSource(it) != nil || contextTagRe.MatchString(it.Text) {
				continue
			}
			pins = append(pins, prompts.RecoveryEntry{Source: fmt.Sprintf("session/%s/%s/item/%s seq=%d", p.MachineID, p.SessionID, it.ID, it.Seq), Text: it.Text})
		}
	}
	return pins
}
