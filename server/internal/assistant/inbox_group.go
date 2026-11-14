package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Inbox events (#1680, docs/assistant/inbox.md "Event grouping"). A connector that declares group hands a conversation's new messages
// over as a batch (InboxThing.Group) instead of an item. The hub asks the model which events — inbox items of that
// conversation — the messages belong to: it reads the events (the events tool, its own) and the conversation (the
// connector's tools, group.tool through the hub), at most groupCalls calls, and answers with a title, a summary and a tier for every
// event it touches. The connector writes each event (group.append: one event a call, its meta in and out; the hub never
// reads meta) and the hub stores what comes back with the model's words, triaged_by "model". When the model can't (not
// set up, held, failing, too slow, or an answer that does not hold up) the connector groups the batch by structure
// itself (group.fallback). Its title, rule tier, "By rule" and triaged_by "rules" describe only events it opens. An
// existing event takes the new messages but keeps its title, summary, urgency and triager. Its tier never falls: it may
// rise to this batch's rule floor (the aggregated assignment and rewritten-entry floors for a direct-chat join), except
// out of noise or while a processing conversation is handling the event. Only an actual rise replaces the reason with "By rule".

const (
	groupCalls        = 4                  // call allowances per batch (tools and a possible correction)
	groupNewMax       = 3                  // new events the prompt suggests for one batch; also sizes the output budget
	groupOpenMax      = 10                 // open events the events tool lists, independently of done events
	groupDoneMax      = 6                  // recent done events the events tool lists, independently of open events
	groupDoneFor      = 3 * 24 * time.Hour // how long a done event is still listed; keep whole days so DoneDays matches
	groupDoneCountMax = 50                 // 50 is a starting value with no supporting measurements; done non-direct-chat events at or above it are not candidates; direct-chat done events are exempt
	groupConcurrency  = 4                  // batches related at once (the others wait for the next round)
	groupAttempts     = 3                  // tries of one event's append
	groupDMJoinWithin = 2 * time.Hour      // 2 hours is a starting value with no supporting measurements
)

// Bounds, variables for tests.
var (
	groupTimeout     = 2 * time.Minute  // the model's part of one batch, its tool calls included
	groupStepTimeout = 45 * time.Second // one model call
	groupCallTimeout = 30 * time.Second // one call to the connector (a tool, the fallback, an append)
	groupRetryWait   = time.Second      // before an append is tried again (doubling)
)

// InboxGroup is an event's place: the connector that writes it and the conversation (thread) it is in.
type InboxGroup struct {
	Connector string `json:"connector"`
	Thread    string `json:"thread"`
}

// inboxGrouping is the batches being related: one conversation at a time (two batches of one conversation would
// write the same event's meta over each other), a batch handed again meanwhile is left alone.
type inboxGrouping struct {
	mu      sync.Mutex
	running map[string]string // connector id + thread → the batch being related
	why     map[string]string // connector id → why its last batch went by rules ("" by the model): logged on change
	wg      sync.WaitGroup
}

var undeclaredGroups sync.Map

// relateBatches starts relating c's batches (things with Group) in the background and returns its other things.
// Batches past groupConcurrency, and those of a conversation being related, wait for the next round: the connector
// hands them again until each of their messages is in an event.
func (s *Service) relateBatches(ctx context.Context, c assistantwire.InboxSource, things []connector.InboxThing) []connector.InboxThing {
	rest := things[:0:0]
	var batches []connector.InboxThing
	for _, t := range things {
		if t.Group == nil {
			rest = append(rest, t)
		} else {
			batches = append(batches, t)
		}
	}
	if len(batches) == 0 {
		return rest
	}
	if !c.Group {
		if _, logged := undeclaredGroups.LoadOrStore(c.ID, true); !logged {
			s.logf("inbox: %s: hands batches to relate but does not declare group: they are dropped", c.ID)
		}
		return rest
	}
	ctx = context.WithoutCancel(ctx) // the round (a pull to refresh) does not wait for them
	gr := &s.inbox.grouping
	for _, t := range batches {
		if t.Thread == "" || t.Group.Batch == "" || len(t.Group.New) == 0 {
			continue
		}
		key := c.ID + "\x00" + t.Thread
		gr.mu.Lock()
		if gr.running == nil {
			gr.running = map[string]string{}
		}
		if _, busy := gr.running[key]; busy || len(gr.running) >= groupConcurrency {
			gr.mu.Unlock()
			continue
		}
		gr.running[key] = t.Group.Batch
		gr.wg.Add(1)
		gr.mu.Unlock()
		go func() {
			defer func() {
				gr.mu.Lock()
				delete(gr.running, key)
				gr.mu.Unlock()
				gr.wg.Done()
			}()
			s.relateBatch(ctx, c, t)
		}()
	}
	return rest
}

// groupEvent is an event as the events tool lists it, and as the model's answer may name it.
type groupEvent struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Summary string          `json:"summary,omitempty"`
	Tier    string          `json:"tier,omitempty"`
	Version string          `json:"version"`
	Done    bool            `json:"done"`
	Meta    json.RawMessage `json:"meta"`
}

// groupPlan is which messages go to which event, and what each event is: the model's, or the connector's fallback.
type groupPlan struct {
	model              bool
	fallbackCandidates []string // the exact candidate ids supplied to group.fallback; empty on a model plan
	events             []plannedEvent
}

// plannedEvent is one event a batch touches: an existing one or a new one (which may continue a done one).
type plannedEvent struct {
	existing     string // the event's id; "" a new one
	messages     []string
	continues    string
	reopen       *bool // nil means true for a done event; ignored for open and new events
	twinDisabled bool  // writeGroup snapshots the AI-twin switch before taking b.mu
	dmJoined     bool  // at least one fallback-new entry was rewritten into this existing event
	dmAssigned   bool  // an ordinary assignment to the same target also contributes the appended thing's rule floor
	// The highest explicit rule tier among those rewritten entries. ruleFloorDefault records that at least one had no
	// explicit tier and still needs putEvent's fallback-new default; dmJoined distinguishes them from an ordinary append.
	ruleFloor        string
	ruleFloorDefault bool
	// The model's words for it; under the fallback only a new one's title and rule tier (the connector's).
	title, summary, tier, reason string
	urgent                       bool
}

// relateBatch relates one batch: by the model, else by the connector's fallback; then has each event written.
func (s *Service) relateBatch(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing) {
	events := s.groupEvents(c.ID, t.Thread, "")
	plan, why := s.askGroup(ctx, c, t, events)
	if plan == nil {
		var err error
		if plan, err = s.fallbackGroup(ctx, c, t, events); err != nil {
			if errors.Is(err, errNoFallback) { // the batch waits for the model, as an inbox item without a fallback does
				s.noteGroupWhy(c.ID, why+"; no fallback: waiting for the model")
				return
			}
			s.logf("inbox: %s: batch %s: not related (%s), and its fallback failed: %v", c.ID, t.Group.Batch, why, err)
			return
		}
	}
	s.noteGroupWhy(c.ID, why)
	s.writeGroup(ctx, c, t, plan)
}

// errNoFallback: the connector has no group.fallback (it answered an unknown method).
var errNoFallback = errors.New("no group.fallback")

// unsupported says whether a connector answered that it has no such method.
func unsupported(err error) bool {
	var re *rpc.Error
	if errors.As(err, &re) {
		switch re.Code {
		case "unknown_method", "unsupported", "no_capability":
			return true
		}
	}
	return false
}

// noteGroupWhy logs when a connector's batches start going by rules (or why changes), and when the model is back.
func (s *Service) noteGroupWhy(id, why string) {
	gr := &s.inbox.grouping
	gr.mu.Lock()
	if gr.why == nil {
		gr.why = map[string]string{}
	}
	prev, seen := gr.why[id]
	gr.why[id] = why
	gr.mu.Unlock()
	switch {
	case why != "" && why != prev:
		s.logf("inbox: %s: relating by the connector's rules: %s", id, why)
	case why == "" && seen && prev != "":
		s.logf("inbox: %s: relating by the model again", id)
	}
}

// groupEvents are the events of a conversation as the events tool answers: at most groupOpenMax open ones, then at
// most groupDoneMax done ones within groupDoneFor that are not noise. Non-direct-chat done events also have fewer than
// groupDoneCountMax messages; direct-chat done events have no message-count cap. Each group is newest first and has its
// own quota; query keeps those whose title, summary or meta has it before either cap. Hidden members of merged rows are
// never candidates.
func (s *Service) groupEvents(connectorID, thread, query string) []groupEvent {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := time.Now().Add(-groupDoneFor)
	q := strings.ToLower(strings.TrimSpace(query))
	var open, done []*InboxItem
	for _, it := range b.items {
		if it.Group == nil || it.Group.Connector != connectorID || !sameConversation(it.Group.Thread, thread) || it.MergedInto != "" {
			continue
		}
		isOpen := it.DoneAt == nil // merged members, the other way open() is false, are skipped above
		if !isOpen && (it.DoneAt.Before(cutoff) || it.Tier == TierNoise || (it.Kind != KindDM && it.Count >= groupDoneCountMax)) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Title+"\n"+it.Summary+"\n"+string(it.Meta)), q) {
			continue
		}
		if isOpen {
			open = append(open, it)
		} else {
			done = append(done, it)
		}
	}
	newestFirst := func(list []*InboxItem) {
		sort.Slice(list, func(i, j int) bool { return newerItem(list[i], list[j]) })
	}
	newestFirst(open)
	newestFirst(done)
	out := make([]groupEvent, 0, min(len(open), groupOpenMax)+min(len(done), groupDoneMax))
	appendEvents := func(list []*InboxItem, limit int) {
		for _, it := range list[:min(len(list), limit)] {
			out = append(out, groupEvent{ID: it.ID, Title: it.Title, Summary: it.Summary, Tier: it.Tier, Version: inboxVersion(it),
				Done: it.DoneAt != nil, Meta: it.Meta})
		}
	}
	appendEvents(open, groupOpenMax)
	appendEvents(done, groupDoneMax)
	return out
}

// sameConversation: an event's thread is the conversation's, or one under it (the conversation's thread followed by
// a separator, not more of its id).
func sameConversation(event, thread string) bool {
	if event == thread {
		return true
	}
	if !strings.HasPrefix(event, thread) {
		return false
	}
	c := event[len(thread)]
	return !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-')
}

// groupAnswer is one model reply: a tool call, or the answer.
type groupAnswer struct {
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
	Assign []struct {
		Message string `json:"message"`
		Event   string `json:"event"`
	} `json:"assign"`
	Events []struct {
		Event     string `json:"event"`
		Title     string `json:"title"`
		Summary   string `json:"summary"`
		Tier      string `json:"tier"`
		Reason    string `json:"reason"`
		Urgent    bool   `json:"urgent"`
		Reopen    *bool  `json:"reopen"`
		Continues string `json:"continues"`
	} `json:"events"`
}

// askGroup has the model relate the batch, calling tools at most groupCalls times, within groupTimeout. One invalid
// answer is handed back for correction, consuming one of those call allowances without adding a tool call. nil and why
// when the model can't or its answer still does not hold up (the fallback then groups it).
func (s *Service) askGroup(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing, events []groupEvent) (*groupPlan, string) {
	ctx, cancel := context.WithTimeout(ctx, groupTimeout)
	defer cancel()
	rules := strings.TrimSpace(t.Rules)
	if rules == "" {
		rules = s.inboxConfig(ctx).Rules
	}
	hello := s.hello(ctx)
	system := prompts.RenderInboxGroup(prompts.InboxGroup{Owner: hello.Owner, MetaDescription: strings.TrimSpace(c.MetaDescription),
		Rules: rules, MaxCalls: groupCalls, NewMax: groupNewMax, OpenMax: groupOpenMax, DoneMax: groupDoneMax,
		DoneDays: int(groupDoneFor / (24 * time.Hour)), Language: answerLanguage(hello.Locale)})
	seen := map[string]groupEvent{} // every event the model was shown: the only ones its answer may name
	for _, e := range events {
		seen[e.ID] = e
	}
	in := prompts.InboxGroupInput{Thread: t.Thread, New: t.Group.New, Context: t.Context, Events: jsonText(events)}
	budget := groupOutputBudget(len(t.Group.New), len(events))
	retried := false
	for {
		in.Left = groupCalls - len(in.Calls)
		if retried {
			in.Left-- // the correction spends an allowance, but is not a tool call in Calls
		}
		var out groupAnswer
		sctx, scancel := context.WithTimeout(ctx, groupStepTimeout)
		input := prompts.RenderInboxGroupInput(in)
		in.Problem = "" // a correction is shown on the immediately following request only
		err := s.inboxModelJSON(sctx, purposeTriage, system, input, budget, &out)
		scancel()
		paused, _ := modelPaused(err)
		var problem error
		why := "模型出错："
		switch {
		case modelUnconfigured(err):
			return nil, "没有配置分拣模型"
		case paused:
			return nil, "模型暂时不可用：" + err.Error()
		case err != nil && groupJSONProblem(err):
			problem = err
		case err != nil:
			return nil, why + err.Error()
		case out.Tool == "":
			plan, checkErr := checkGroupAnswer(out, t.Group.New, seen)
			if checkErr == nil {
				return plan, ""
			}
			problem, why = checkErr, "模型的结果不合格："
		case in.Left == 0:
			return nil, fmt.Sprintf("模型查了 %d 次还要查", len(in.Calls))
		}
		if problem != nil {
			if retried {
				return nil, "模型重问后仍不合格：" + problem.Error()
			}
			if in.Left == 0 {
				return nil, why + problem.Error()
			}
			retried = true
			in.Problem = problem.Error()
			continue
		}
		result := s.groupTool(ctx, c.ID, t.Thread, out.Tool, out.Args, seen)
		in.Calls = append(in.Calls, prompts.InboxGroupCall{Tool: out.Tool, Args: argsText(out.Args), Result: result})
	}
}

// groupOutputBudget sizes every grouping response from the batch's messages and candidate events, within the model's
// usual 1024-token floor and a 4096-token ceiling.
func groupOutputBudget(fresh, candidates int) int {
	return max(1024, min(4096, 256+48*fresh+160*min(fresh, candidates+groupNewMax)))
}

// groupJSONProblem is a malformed model answer that one correction may fix, not a transport or availability failure.
func groupJSONProblem(err error) bool {
	if errors.Is(err, ai.ErrNoObject) || errors.Is(err, ai.ErrUnterminated) || errors.Is(err, ai.ErrTruncated) {
		return true
	}
	var syntax *json.SyntaxError
	var valueType *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &valueType)
}

// groupTool runs one tool for the model: events here, the conversation's through the connector (at the hub). A
// failure is the tool's answer ({"error": …}), the call counted.
func (s *Service) groupTool(ctx context.Context, connectorID, thread, name string, args json.RawMessage, seen map[string]groupEvent) string {
	switch name {
	case "events":
		var a struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(args, &a)
		events := s.groupEvents(connectorID, thread, a.Query)
		for _, e := range events {
			seen[e.ID] = e
		}
		return jsonText(events)
	case connector.GroupMessagesBefore, connector.GroupMessagesAround, connector.GroupSearch:
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage(`{}`)
		}
		cctx, cancel := context.WithTimeout(ctx, groupCallTimeout)
		defer cancel()
		var res struct {
			Result string `json:"result"`
		}
		err := s.appCall(cctx, AppInbox, inboxGroupTool, map[string]any{"source": connectorID,
			"call": connector.GroupToolCall{Thread: thread, Name: name, Args: args}}, &res)
		if unsupported(err) {
			return jsonText(map[string]string{"error": "这个工具用不了：连接器没有它，别再调，用已有的信息判断"})
		}
		if err != nil {
			return jsonText(map[string]string{"error": err.Error()})
		}
		return res.Result
	}
	return jsonText(map[string]string{"error": "没有这个工具：" + name})
}

// checkGroupAnswer holds the model's answer to the rules: every new message in exactly one event; an existing event
// only one it was shown, whether open or done; a new one (new:<n>) may continue a done one it was shown; every event
// used described, with a known tier and a title. Reopen is retained only for an event shown done. The prompt's
// new-event limit is guidance, not a validation failure.
func checkGroupAnswer(out groupAnswer, fresh []string, seen map[string]groupEvent) (*groupPlan, error) {
	want := map[string]bool{}
	for _, m := range fresh {
		want[m] = true
	}
	byEvent := map[string][]string{}
	var order []string
	for _, a := range out.Assign {
		if !want[a.Message] {
			return nil, fmt.Errorf("消息 %q 不是这一批的，或者归了两次", a.Message)
		}
		delete(want, a.Message)
		if _, ok := byEvent[a.Event]; !ok {
			order = append(order, a.Event)
		}
		byEvent[a.Event] = append(byEvent[a.Event], a.Message)
	}
	if len(want) > 0 {
		var left []string
		for _, m := range fresh {
			if want[m] {
				left = append(left, m)
			}
		}
		return nil, fmt.Errorf("消息 %s 没有归到事件", strings.Join(left, "、"))
	}
	described := map[string]int{}
	for i, e := range out.Events {
		if _, twice := described[e.Event]; twice {
			return nil, fmt.Errorf("事件 %q 写了两次", e.Event)
		}
		described[e.Event] = i
	}
	plan := &groupPlan{model: true}
	for _, ev := range order {
		i, ok := described[ev]
		if !ok {
			return nil, fmt.Errorf("事件 %q 没有标题和分级", ev)
		}
		d := out.Events[i]
		if !connector.Tier(d.Tier).Known() {
			return nil, fmt.Errorf("事件 %q 的分级 %q 不对", ev, d.Tier)
		}
		if strings.TrimSpace(d.Title) == "" {
			return nil, fmt.Errorf("事件 %q 没有标题", ev)
		}
		pe := plannedEvent{messages: byEvent[ev], title: d.Title, summary: d.Summary, tier: d.Tier, reason: d.Reason,
			urgent: d.Urgent && d.Tier == TierAction}
		if isNewEvent(ev) {
			if d.Continues != "" {
				if s, ok := seen[d.Continues]; !ok || !s.Done {
					return nil, fmt.Errorf("事件 %q 接着的 %q 不是查到的已处理的事", ev, d.Continues)
				}
				pe.continues = d.Continues
			}
		} else {
			s, ok := seen[ev]
			if !ok {
				return nil, fmt.Errorf("事件 %q 不是查到的事", ev)
			}
			if s.Done {
				pe.reopen = d.Reopen
			}
			pe.existing = ev
		}
		plan.events = append(plan.events, pe)
	}
	return plan, nil
}

// isNewEvent: new:<n>, the model's name for an event it opens.
func isNewEvent(ev string) bool {
	n, ok := strings.CutPrefix(ev, "new:")
	if !ok || n == "" {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// fallbackGroup has the connector group the batch by structure, with the same events the model would have seen,
// and holds its answer to the same rules (each new message once; an existing event a listed one, open or done).
func (s *Service) fallbackGroup(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing, events []groupEvent) (*groupPlan, error) {
	listed := map[string]bool{}
	candidateIDs := make([]string, 0, len(events))
	in := connector.GroupFallback{Thread: t.Thread, Batch: t.Group.Batch, Events: []connector.GroupEvent{}}
	for _, e := range events {
		listed[e.ID] = true
		candidateIDs = append(candidateIDs, e.ID)
		in.Events = append(in.Events, connector.GroupEvent{ID: e.ID, Meta: e.Meta, Done: e.Done})
	}
	cctx, cancel := context.WithTimeout(ctx, groupCallTimeout)
	defer cancel()
	var res connector.GroupFallbackResult
	err := s.appCall(cctx, AppInbox, inboxGroupFallback, map[string]any{"source": c.ID, "fallback": in}, &res)
	if unsupported(err) {
		return nil, errNoFallback
	}
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, m := range t.Group.New {
		want[m] = true
	}
	take := func(m string) error {
		if !want[m] {
			return fmt.Errorf("message %q is not one of the batch's, or placed twice", m)
		}
		delete(want, m)
		return nil
	}
	plan := &groupPlan{fallbackCandidates: candidateIDs}
	at := map[string]int{}
	for _, a := range res.Assign {
		if !listed[a.Event] {
			return nil, fmt.Errorf("event %q is not one of those it was given", a.Event)
		}
		if err := take(a.Message); err != nil {
			return nil, err
		}
		i, ok := at[a.Event]
		if !ok {
			i = len(plan.events)
			at[a.Event] = i
			plan.events = append(plan.events, plannedEvent{existing: a.Event})
		}
		plan.events[i].messages = append(plan.events[i].messages, a.Message)
	}
	keys := map[string]bool{}
	for _, n := range res.New {
		if n.Key == "" || keys[n.Key] || len(n.Messages) == 0 {
			return nil, fmt.Errorf("new event %q: no key, a key twice, or no messages", n.Key)
		}
		keys[n.Key] = true
		for _, m := range n.Messages {
			if err := take(m); err != nil {
				return nil, err
			}
		}
		pe := plannedEvent{messages: n.Messages, title: n.Title}
		if n.RuleTier.Known() {
			pe.tier = string(n.RuleTier)
		}
		plan.events = append(plan.events, pe)
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("%d message(s) placed nowhere", len(want))
	}
	return plan, nil
}

// writeGroup writes each event of the plan and keeps it: through the connector when it keeps the events' meta (w; an
// event that failed is tried again, alone), else composed here from the batch, without meta. An existing event takes
// the append with its current meta whether it is open or done; only one that no longer exists gets a new event that
// continues it instead.
func (s *Service) writeGroup(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing, plan *groupPlan) {
	twinDisabled := !s.inboxConfig(ctx).Agent.Enabled // read before taking b.mu below
	b := &s.inbox
	b.mu.Lock()
	b.joinDMFallback(c.ID, t, plan)
	b.foldNoise(c.ID, t.Thread, plan)
	b.mu.Unlock()
	var saved []*InboxItem
	for _, e := range plan.events {
		a := connector.GroupAppend{Thread: t.Thread, Messages: e.messages, Continues: e.continues}
		if e.existing != "" {
			b.mu.Lock()
			if cur := b.items[e.existing]; cur != nil {
				a.Event, a.Meta = cur.ID, cur.Meta
			} else {
				a.Continues = e.existing
			}
			b.mu.Unlock()
		}
		var thing connector.InboxThing
		var err error
		if c.Meta {
			thing, err = s.appendEvent(ctx, c.ID, a)
		} else {
			thing = s.composeEvent(c.ID, t, a)
		}
		if err != nil {
			s.logf("inbox: %s: batch %s: writing %s: %v", c.ID, t.Group.Batch, orNew(a.Event), err)
			continue
		}
		e.continues = a.Continues // a done event's follow-up continues it (above)
		e.twinDisabled = twinDisabled
		if it := s.putEvent(c, t.Thread, e, thing, plan.model); it != nil && !containsItem(saved, it) {
			saved = append(saved, it)
		}
	}
	// No rollback here (#4366): the batch is spent once its events are written (a connector with meta stops sending
	// what group.append took, and a repeated batch is related once), so an undone event would never come back and an
	// existing one would hand the connector stale meta. Memory keeps the events; the row catches up at their next save.
	b.mu.Lock()
	saved = b.followMerged(saved, time.Now())
	b.mu.Unlock()
	for id := range s.saveInbox(ctx, saved) {
		s.logf("inbox: %s: event %s could not be saved; it stays in memory until its next save", c.ID, id)
	}
	if len(saved) > 0 {
		s.handInbox(ctx) // a new "Needs you" may be the AI twin's
	}
}

func orNew(event string) string {
	if event == "" {
		return "a new event"
	}
	return event
}

// appendEvent is one group.append (at the hub), tried groupAttempts times; an answer without an id is a failure. The
// tries carry one append id: one whose answer was lost on the way (the plugin wrote it) is answered by the hub from
// what the plugin said, not written again.
func (s *Service) appendEvent(ctx context.Context, source string, a connector.GroupAppend) (connector.InboxThing, error) {
	raw, _ := json.Marshal(a)
	appendID := namedClientID("append\n" + source + "\n" + string(raw))
	var err error
	wait := groupRetryWait
	for try := range groupAttempts {
		if try > 0 {
			select {
			case <-ctx.Done():
				return connector.InboxThing{}, ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		cctx, cancel := context.WithTimeout(ctx, groupCallTimeout)
		var res struct {
			Thing connector.InboxThing `json:"thing"`
		}
		err = s.appCall(cctx, AppInbox, inboxGroupAppend, map[string]any{"source": source, "append": a, "append_id": appendID}, &res)
		cancel()
		thing := res.Thing
		if err == nil && thing.ID == "" {
			err = errors.New("the answer has no id")
		}
		if err == nil {
			return thing, nil
		}
	}
	return connector.InboxThing{}, err
}

// keepsWords reports whether the handling conversation or the user owns the item's title and summary. Grouping still
// advances the tier, reason and urgency, but leaves those words and their provenance alone.
func keepsWords(it *InboxItem) bool {
	return it.TitleBy == TitleByAssistant || it.TitleBy == TitleByUser || it.TitleBy == TitleByMerged
}

// fallbackEventTitle removes a redundant conversation-name prefix only when a real title remains.
func fallbackEventTitle(title, where string) string {
	title = strings.TrimSpace(title)
	if where == "" {
		return title
	}
	rest, ok := strings.CutPrefix(title, where+"：") // i18n-ignore: the connector's fallback title separator, matched as written
	if !ok {
		return title
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		return rest
	}
	return title
}

// putEvent keeps an event the connector wrote (or the hub composed), under the item's id: the model's title, summary
// and tier, or under the fallback a new event's title and rule tier with "By rule". Fallback messages joining an
// existing event leave its title, summary, urgency and triager unchanged and never lower its tier. The batch's rule
// floor may raise it, except that noise stays noise and a processing conversation keeps deciding its event's tier;
// only an actual rise replaces the reason with "By rule".
// A done existing event reopens by default; reopen:false or a matching mute keeps it done while its messages advance.
// It is triaged at this version either way. Its count is the hub's (the messages it put in it; extra.count is not read
// for an event) and its start is set when it is made. An answer at the version the item has already is a repeat:
// nothing is stored or announced again (nil).
func (s *Service) putEvent(c assistantwire.InboxSource, thread string, e plannedEvent, thing connector.InboxThing, byModel bool) *InboxItem {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it := b.items[thing.ID]
	if it != nil && it.Last == thing.Last {
		return nil
	}
	wasDone := it != nil && it.DoneAt != nil
	var prev inboxSeen
	if it != nil {
		prev = seenOf(it)
	}
	if it == nil {
		it = &InboxItem{ID: thing.ID, Source: c.ID, Account: c.ID, AccountName: c.Name, StartedAt: thing.At}
		if x := thing.Extra; x != nil {
			if x.Source != "" {
				it.Source = x.Source
			}
			it.Account, it.AccountName = x.Account, x.AccountName
			if !x.StartedAt.IsZero() {
				it.StartedAt = x.StartedAt
			}
		}
	}
	count, started, title := it.Count, it.StartedAt, it.Title
	doneAt, doneBy := it.DoneAt, it.DoneBy
	summary, tier, reason, triagedBy, urgent := it.Summary, it.Tier, it.Reason, it.TriagedBy, it.Urgent
	if thing.Extra != nil {
		fillMigrated(it, thing)
	} else {
		thing.Tier = connector.Tier(eventRuleTier(thing)) // the rule tier, not a decision: set below
		fillConnectorItem(it, thing)
	}
	// The hub assigns the messages, so it keeps the count (the connector's meta may hold only the newest ones): the
	// event had count, it takes these. Its start is set once, when it is made.
	it.Count, it.StartedAt = count+len(e.messages), started
	m := mutedBy(b.mutes, it)
	reopened := wasDone && (e.reopen == nil || *e.reopen) && m == nil
	keepDone := wasDone && !reopened
	it.Meta, it.Group = thing.Meta, &InboxGroup{Connector: c.ID, Thread: thread}
	if !keepDone {
		it.Read = false
	}
	b.noteReceived(it, prev, nil)
	if reopened {
		it.DoneAt, it.DoneBy = nil, ""
		b.chainAdd(it, InboxChainEntry{Kind: ChainReopened, Via: it.Source,
			Text: i18n.T(b.userLang(), "assistant-inbox_chain.reopened")})
	}
	tierBefore := it.Tier
	fallbackFloorRaised := false
	raiseFallbackFloor := func() {
		if tier == TierNoise || it.handled() {
			return
		}
		floor := eventRuleTier(thing)
		if e.dmJoined {
			joinedFloor := e.ruleFloor
			if e.ruleFloorDefault || joinedFloor == "" {
				joinedFloor = higherTier(joinedFloor, b.fallbackEventTier(thing, it))
			}
			if e.dmAssigned {
				// An ordinary assignment merged into the rewrite contributes the returned thing's floor too.
				if floor == "" {
					floor = b.fallbackEventTier(thing, it)
				}
				floor = higherTier(floor, joinedFloor)
			} else {
				floor = joinedFloor
			}
		} else if floor == "" {
			floor = b.fallbackEventTier(thing, it)
		}
		if higherTier(tier, floor) != tier {
			// The existing triager and urgency deliberately stay unchanged.
			it.Tier, it.Reason = floor, b.byRules()
			fallbackFloorRaised = true
		}
	}
	switch {
	case keepDone:
		// A wrap-up-only update advances the messages without retriaging or reopening the event. A fallback append still
		// takes its rule floor, including when a mute keeps the target done; a model closeout has no rule floor.
		it.Title, it.Summary, it.Tier, it.Reason, it.TriagedBy = title, summary, tier, reason, triagedBy
		it.Urgent = urgent
		it.DoneAt, it.DoneBy = doneAt, doneBy
		tierBefore = tier
		if !byModel && !it.handled() {
			raiseFallbackFloor()
		}
	case prev.last != "" && it.handled() && !reopened:
		// Messages that join an event being handled follow it: its tier, words and urgency stay, even for a rewritten
		// direct-chat join; its processing conversation decides (inbox_handle.go). A reopen bypasses this branch so a
		// model reopen takes the model's triage; raiseFallbackFloor keeps a fallback reopen's handled triage unchanged.
		it.Title, it.Summary, it.Tier, it.Reason = title, summary, tier, reason
		it.TriagedBy = cmp.Or(it.TriagedBy, "model")
		it.Urgent = urgent
		tierBefore = tier // The fill's provisional rule tier is not a triage change.
	case byModel:
		if keepsWords(it) {
			it.Title, it.Summary = title, summary
		} else {
			it.Title, it.Summary, it.TitleBy = clip(oneLine(e.title), 40), clip(oneLine(e.summary), 80), TitleByModel
		}
		it.Tier, it.Reason, it.TriagedBy = e.tier, clip(oneLine(e.reason), 30), "model"
		it.Urgent = e.urgent && e.tier == TierAction
	case prev.last != "":
		// The fallback adds messages to an existing event; its structural title is only for new events. The saved tier,
		// not the fill's provisional rule tier, is the original against which this batch's floor may raise it.
		it.Title, it.Summary, it.Tier, it.Reason, it.TriagedBy = title, summary, tier, reason, triagedBy
		it.Urgent = urgent
		tierBefore = tier
		raiseFallbackFloor()
	default:
		tier := e.tier
		if tier == "" {
			tier = b.fallbackEventTier(thing, it)
		}
		if t := fallbackEventTitle(e.title, it.Where); t != "" {
			it.Title = t
		}
		it.Summary, it.Tier, it.Reason, it.TriagedBy, it.TitleBy = "", tier, b.byRules(), "rules", TitleByRules
	}
	policyChanged := false
	if !keepDone && !it.handled() { // what an event's conversation handles keeps its tier
		policyChanged = b.applyPolicyTier(it)
	}
	it.TriagedAt = it.Last
	switch {
	case fallbackFloorRaised && !policyChanged && higherTier(tierBefore, it.Tier) != tierBefore:
		// Only an actual final rise from this batch's fallback rule floor bypasses noteTriaged's same-text dedup: a
		// conversation may since have concluded at a lower tier, making this a new decision despite the earlier text.
		b.chainAdd(it, triagedEntry(b.userLang(), it))
	case !keepDone && (tierBefore != it.Tier || prev.last == ""):
		b.noteTriaged(it)
	}
	if e.continues != "" && e.continues != it.ID {
		b.offerParent(it, &InboxParent{Kind: ParentInbox, ID: e.continues, By: ParentByContinues})
	}
	b.offerParent(it, thingParent(thing))
	now := time.Now()
	if reopened && it.Agent != nil {
		// A close note belongs to the filing that just ended, not to the conversation's new turn.
		it.Agent.Pending = slices.DeleteFunc(it.Agent.Pending, func(n inboxNote) bool { return n.Name == prompts.NoteClosed })
		if !e.twinDisabled && b.policyTwin(it) != TwinNever && it.Agent.SessionID != "" {
			// Looking makes handled() true, so escalate queues the new messages to this session regardless of tier.
			b.setHandling(it, HandlingLooking, "", now)
		}
	}
	if m != nil && it.open() {
		b.fileMuted(it, now, m.Name)
	}
	b.items[it.ID] = it
	delete(b.listMemo.kept, it.ID) // saved after the batch's other appends (writeGroup): listed as it is meanwhile
	return it
}

// joinDMFallback rewrites fallback-new entries in a direct chat into the most recently active supplied candidate.
// UpdatedAt is the latest message activity, never the handling clock. Under b.mu, before foldNoise.
func (b *inboxBook) joinDMFallback(connectorID string, t connector.InboxThing, plan *groupPlan) {
	if plan.model || len(plan.fallbackCandidates) == 0 {
		return
	}
	hasNew := false
	for _, e := range plan.events {
		if e.existing == "" {
			hasNew = true
			break
		}
	}
	if !hasNew {
		return
	}
	direct := t.Kind == KindDM
	cutoff := time.Now().Add(-groupDMJoinWithin)
	var target *InboxItem
	for _, id := range plan.fallbackCandidates {
		it := b.items[id]
		if it == nil || it.Group == nil || it.Group.Connector != connectorID || !sameConversation(it.Group.Thread, t.Thread) {
			continue
		}
		if it.Kind == KindDM {
			direct = true
		}
		if it.UpdatedAt.Before(cutoff) {
			continue
		}
		if target == nil || newerItem(it, target) {
			target = it
		}
	}
	if !direct || target == nil {
		return
	}

	// Keep the first participating entry's place in the plan and append later messages in plan order. An ordinary
	// assignment to the target joins the same append and contributes the returned thing's rule floor in putEvent.
	out := plan.events[:0]
	joined := -1
	for _, e := range plan.events {
		fresh := e.existing == ""
		if !fresh && e.existing != target.ID {
			out = append(out, e)
			continue
		}
		if joined < 0 {
			e.existing = target.ID
			joined = len(out)
			out = append(out, e)
		} else {
			out[joined].messages = append(out[joined].messages, e.messages...)
		}
		if fresh {
			if e.tier == "" {
				out[joined].ruleFloorDefault = true
			} else {
				out[joined].ruleFloor = higherTier(out[joined].ruleFloor, e.tier)
			}
		} else {
			out[joined].dmAssigned = true
		}
	}
	joinedEvent := &out[joined]
	joinedEvent.continues = ""
	joinedEvent.reopen = nil // a done target follows putEvent's default reopen path
	joinedEvent.title, joinedEvent.summary, joinedEvent.tier, joinedEvent.reason = "", "", "", ""
	joinedEvent.urgent = false
	joinedEvent.dmJoined = true
	plan.events = out
}

// foldNoise keeps noise one event per atom (docs/assistant/inbox.md "Events and handling chain"): the noise a plan opens goes into the
// conversation's open noise event, or into one new event for all of it; an existing noise event never takes what
// the model sorts higher (that opens its own event instead). A rewritten direct-chat join and a planned existing done
// event pass through unchanged. Under b.mu.
func (b *inboxBook) foldNoise(connectorID, thread string, plan *groupPlan) {
	var open *InboxItem // the conversation's open noise event
	for _, it := range b.items {
		if it.Group != nil && it.Group.Connector == connectorID && it.Group.Thread == thread && it.open() && it.Tier == TierNoise &&
			(open == nil || newerItem(it, open)) {
			open = it
		}
	}
	out := plan.events[:0:0]
	noise := -1
	for _, e := range plan.events {
		cur := ""
		if e.existing != "" {
			if it := b.items[e.existing]; it != nil {
				if e.dmJoined || !it.open() {
					out = append(out, e)
					continue
				}
				cur = it.Tier
			}
		}
		if cur == TierNoise && e.tier != "" && e.tier != TierNoise {
			e.existing = "" // a noise event is not escalated: the rest is an event of its own
		}
		isNoise := e.tier == TierNoise || e.tier == "" && cur == TierNoise
		if isNoise {
			if noise >= 0 {
				out[noise].messages = append(out[noise].messages, e.messages...)
				continue
			}
			if e.existing == "" && open != nil {
				e.existing, e.continues = open.ID, ""
			}
			noise = len(out)
		}
		out = append(out, e)
	}
	plan.events = out
}

// eventRuleTier is the tier the connector's rules give an event it wrote: extra's rule_tier, else its tier.
func eventRuleTier(t connector.InboxThing) string {
	if t.Extra != nil && connector.Tier(t.Extra.RuleTier).Known() {
		return t.Extra.RuleTier
	}
	if t.Tier.Known() {
		return string(t.Tier)
	}
	return ""
}

// fallbackEventTier resolves the same tier a fallback-new event takes when its connector supplied no explicit rule tier.
func (b *inboxBook) fallbackEventTier(thing connector.InboxThing, it *InboxItem) string {
	tier := eventRuleTier(thing)
	if tier == "" {
		tier = b.ruleTier(it)
	}
	if tier == "" { // nothing says: the user looks (when unsure, pick action)
		tier = TierAction
	}
	return tier
}

// higherTier compares rule floors as noise < fyi < action, keeping a on an equal or unknown rank.
func higherTier(a, b string) string {
	rank := func(tier string) int {
		switch tier {
		case TierNoise:
			return 1
		case TierFYI:
			return 2
		case TierAction:
			return 3
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// jsonText is v as compact JSON for a prompt.
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// argsText is a tool call's arguments as one line of JSON ({} when there are none).
func argsText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "{}"
	}
	return jsonText(v)
}
