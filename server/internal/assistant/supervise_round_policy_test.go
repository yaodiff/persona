package assistant

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A round lists fewer tools than the background supervisor's other turns, but it reads the same supervision
// instructions as every supervisor (supervision_instructions.go) and gets supervision messages for every signal it
// follows up. These tests keep the three in agreement: every tool the instructions name is listed in a round or
// withheld with a reason, the instructions tell the background supervisor where the withheld ones' work goes, the hub
// sends that work there, and every tool a round's own message names is one the round lists.

// mainOnlySignals are the signals the instructions' background line leaves to the main conversation; their handling
// rules name the tools a round withholds (task_start_screen, task_start_keys, task_retry, conversation_send).
var mainOnlySignals = []string{sigFailed, sigStarted, sigEscalated, sigLeaderStuck, sigTodoReady}

// backgroundSignals are the signals an existing background conversation can receive for explicitly addressed legacy
// notices. Without that explicit legacy target, all of them stay with main for new assistant-dispatched work (#5732).
var backgroundSignals = []string{sigReview, sigReviewNudge, sigIdle, sigUnjudged, sigNeedsYou, sigStalled, sigNoProgress, sigTimedOut, sigAllStopped, sigInterrupted,
	sigPRConflict, sigDraftPR, sigPRMerged, sigReport, sigOrphaned, sigRecovered, sigAsk, sigDecide, sigDecisionReply, sigRecheck, sigUndecidable, sigDeliveryHold, sigCardlessWait,
	sigOwnerCannot, sigParkedDue, sigParkedSummary, sigParkDecided, sigInbox, sigNeedsUserExternalInfo, sigOwnerPresent, sigOverdue, sigCheck}

// roundConnectorTools are the round's tools that come with the Lark extension (lark_search) or the lark-cli connector
// (larkcli_search) rather than the assistant's own list.
var roundConnectorTools = []string{"lark_search", "larkcli_search"}

// policyToolNames are the assistant tools the supervision instructions name: the leading word of each backticked span
// (`task_review accepted`, `leader_progress.doing`) that is a tool.
func policyToolNames(policy string) []string {
	tools := append(slices.Clone(toolNames), roundConnectorTools...)
	word := regexp.MustCompile("^[a-z_]+")
	var out []string
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(policy, -1) {
		name := word.FindString(m[1])
		if slices.Contains(tools, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// backgroundLine is the instructions' line for the background supervisor.
func backgroundLine(policy string) string {
	for _, line := range strings.Split(policy, "\n") {
		if strings.HasPrefix(line, "- Background supervises for main") {
			return line
		}
	}
	return ""
}

// Every tool the supervision instructions name is in a round's list or withheld with a reason, never both and never
// neither; nothing is withheld that the instructions do not name or that is no tool; and the instructions' background
// line says where the withheld tools' work goes: owner decisions through task_review, never ask_user, and the start
// remedies and a stuck leader to the main conversation.
func TestRoundToolsAgreeWithSupervisionInstructions(t *testing.T) {
	policy := prompts.RenderSupervisionInstructions()
	named := policyToolNames(policy)
	if len(named) < 10 || !slices.Contains(named, "task_review") || !slices.Contains(named, "supervision_finish") {
		t.Fatalf("the instructions name too few tools, or the reader missed some: %v", named)
	}
	for _, name := range named {
		listed := slices.Contains(roundToolNames, name)
		_, withheld := roundToolsWithheld[name]
		switch {
		case listed && withheld:
			t.Errorf("%s is listed in a round and withheld from it (roundToolNames, roundToolsWithheld): never both", name)
		case !listed && !withheld:
			t.Errorf("the supervision instructions name %s, which a round does not list: list it in roundToolNames, or "+
				"withhold it in roundToolsWithheld and say in the instructions' background line where its work goes", name)
		}
	}
	for name, why := range roundToolsWithheld {
		switch {
		case !slices.Contains(toolNames, name):
			t.Errorf("roundToolsWithheld names %s, which is no assistant tool", name)
		case !slices.Contains(named, name):
			t.Errorf("roundToolsWithheld names %s, which the supervision instructions no longer name: drop it", name)
		case strings.TrimSpace(why) == "":
			t.Errorf("roundToolsWithheld gives no reason for %s", name)
		}
	}
	for _, name := range roundToolNames {
		if !slices.Contains(roundConnectorTools, name) && !slices.Contains(toolNames, name) {
			t.Errorf("roundToolNames lists %s, which is no assistant tool", name)
		}
	}

	line := backgroundLine(policy)
	if line == "" {
		t.Fatal(`the supervision instructions have no line for the background supervisor ("- Background supervises for main")`)
	}
	if !strings.Contains(line, "never `ask_user`") || !strings.Contains(line, "`task_review`") {
		t.Errorf("the background line does not send owner decisions through task_review instead of ask_user, which a round withholds:\n%s", line)
	}
	for _, kind := range mainOnlySignals {
		if !strings.Contains(line, "`"+kind+"`") {
			t.Errorf("the background line does not leave %s to the main conversation, whose handling needs tools a round withholds:\n%s", kind, line)
		}
	}
}

// roundMessage is a Fresh supervision message to the background supervisor, rendered as deliverDigest renders one, with
// an entry of every signal it gets; lang is the owner's language as answerLanguage gives it.
func roundMessage(lang string) string {
	review := &prompts.SuperviseLastReview{Verdict: reviewRework, At: "11-17 09:30", Note: "the screenshot is missing"}
	decision := &prompts.SuperviseDecision{WhyOwner: "priority", Question: "Roll out to 10% first or to everyone",
		Recommend: "10% first", Cost: "the fix ships a day later", Background: "asked in the main chat",
		Options: []prompts.SuperviseOption{{Label: "10% first", LeadsTo: "a day of watching"}, {Label: "Everyone now", LeadsTo: "ships today"}}}
	decisions := prompts.RenderTodoDecisions(prompts.TodoDecisions{Title: "Example task", Decisions: []prompts.TodoDecision{
		{N: 1, Question: "Which branch", Choice: "main", When: "11-16 10:00", Source: prompts.TodoDecisionThread}}})
	parked := &prompts.SuperviseParked{Reason: "the upstream release", WaitingOn: "external_event", Until: "11-18 09:00", Count: 1, Left: 2}
	var entries []prompts.SuperviseEntry
	for i, kind := range backgroundSignals {
		e := prompts.SuperviseEntry{ID: "t" + string(rune('a'+i)), Title: "Example task", Kind: kind, Link: "https://hub.example.com/t",
			DoneWhen: []string{"the PR is merged"}, Machine: "box", Session: "s1", Last: "Pushed the fix.", Round: 1,
			Asked: "fix the login page", AskedAt: "11-16", LastReview: review}
		switch kind {
		case sigReview:
			e.Review = &prompts.SuperviseReviewed{State: "clean", Count: 1, Findings: "one naming nit"}
			e.Closeout = &prompts.SuperviseCloseout{Status: "done", Merged: "yes", Deployed: "no", Did: []string{"fixed it"}}
		case sigNeedsYou:
			e.Minutes, e.Ask, e.Quiet = 5, "Which port should it use?", true
		case sigStalled, sigOverdue, sigAllStopped:
			e.Minutes = 50
		case sigInterrupted, sigPRConflict:
			e.Detail = "the model service answered 529"
		case sigDraftPR:
			e.Detail = prompts.RenderDraftPR(prompts.DraftPR{Language: lang, URL: "https://github.com/example/repo/pull/1", Head: "abc123"})
		case sigPRMerged:
			e.Detail = prompts.RenderPRMergedWake(prompts.PRMergedWake{Language: lang, PRURL: "https://github.com/example/repo/pull/1"})
		case sigReport:
			e.Report, e.Said, e.Evidence, e.Step, e.Steps = "question", "Should it keep the old API?", []string{"https://example.com/log"}, 2, 3
		case sigOrphaned:
			e.Orphan = "error"
		case sigAsk:
			e.Asks = []prompts.SuperviseAsk{{ID: "i1", Kind: "question", Machine: "box", Session: "s1", Text: "Keep the old API?", Raised: true},
				{ID: "i2", Kind: "permission", Machine: "box", Session: "s1", Text: "Run the migration", Reminded: true}}
			e.Decisions = decisions
		case sigDecide:
			e.Recommend = "keep the old API"
		case sigDecisionReply:
			e.AgentReply = &prompts.DecisionAgentReply{Language: lang, Machine: "box", Session: "s1"}
		case sigRecheck, sigUndecidable:
			e.Decision = decision
		case sigOwnerCannot:
			e.Decision, e.Response = decision, "decide_yourself"
		case sigCheck: // the owner's answer to a check's decision, the fullest of the check's three
			e.Report, e.Decision, e.Response = checkDecidedReport, decision, "10% first"
		case sigParkedDue, sigParkDecided:
			e.Parked, e.Decisions = parked, decisions
		case sigInbox:
			e.Inbox = &prompts.SuperviseInbox{Item: "in1", Title: "Release thread", Sender: "A colleague", Preview: "Is it out?",
				Count: 2, URL: "https://github.com/example/repo/issues/2", ToAgent: "sent"}
		case sigNeedsUserExternalInfo:
			e.ExternalInfo = &prompts.NeedsUserExternalInfo{Language: lang, Source: "PR status", Summary: "PR #12 merged",
				PendingQuestion: "Merge PR #12 now?", PendingRecommend: "Merge after review"}
		}
		entries = append(entries, e)
	}
	return prompts.RenderSupervise(prompts.Supervise{Entries: sortEntries(entries), Leader: true, Background: true, Fresh: true,
		Overdue: true, ParkedDue: true, ParkDecided: true, Decisions: true, Inbox: true, Asked: true, AskMinutes: 5, PushGrading: true})
}

// roundScanTools are the tools the scans below look for: the assistant's own, the extensions' that Persona's own text
// names (lark_search, the Lark chats of the snapshot), and urgent_call.
var roundScanTools = append(slices.Clone(toolNames), "lark_search", "larkcli_search", "urgent_call", "lark_chats", "lark_messages")

// namedTools are the tools of roundScanTools text names, quoted or bare, each once in the order it first names them.
// The page tool (pages_tool.go) is named only quoted or with one of its actions: "page" alone is an English word.
func namedTools(text string) []string {
	var named []string
	for _, w := range regexp.MustCompile(`[a-z][a-z0-9_]*`).FindAllString(text, -1) {
		if w != pageToolName && slices.Contains(roundScanTools, w) && !slices.Contains(named, w) {
			named = append(named, w)
		}
	}
	if pageToolMention.MatchString(text) {
		named = append(named, pageToolName)
	}
	return named
}

var pageToolMention = regexp.MustCompile("`page`|\\bpage (create|update|get|list|remove)\\b")

// roundPassingMentions are the tools a round does not list that what it reads besides its own message still names, by
// where: the description (and schema) of a tool it lists, or "context", the background supervisor's <persona-context>.
// Each names one in passing (where an id comes from, what undoes or replaces a call, a step a round never takes: the
// Agent presets agent_start takes beside task_plan, the questions ask_user turned into to-dos that are not to be asked
// again, the main conversation's model profile, task_retry for a failed start, which belongs to main or the
// current supervising conversation) or beside the round's own way (task_plan with continues for a
// replacement, task_review needs_user for the owner), so none sends a round to a tool it lacks. A new mention is worded
// for the round's tools, or listed here when it is one of these.
var roundPassingMentions = map[string][]string{
	"agent_archive":      {"agent_unarchive", "conversation_archive"},
	"agent_send":         {"agent_start", "ask_user"},
	"agents_list":        {"agent_start", "agent_switch", "agent_unarchive"},
	"conversation_read":  {"conversations_list"},
	"kb_get":             {"kb_list"},
	"kb_search":          {"kb_list"},
	"schedule_followup":  {"inbox_post"},
	"supervision_finish": {"ask_user"},
	"task_plan":          {"kb_list", "repositories_list", "repository_get", "task_retry", "agent_transfer"},
	"task_review":        {"agent_start"},
	"task_scope":         {"agent_supervise", "ask_user"},
	"todo_get":           {"todo_repository_links_add"},
	"todo_list":          {"concern_report"},
	"context":            {"agent_start", "ask_user", "model_profile_switch", "model_profiles_list"},
}

// backgroundContexts are the background supervisor's <persona-context> as Context renders it (contextSnapshot) for
// every state of the model profile line, with every section that names a tool: the main conversation's plans and the
// owner's unread chats (left out for it with the iPhone line, backgroundSnapshot), the questions ask_user turned into
// to-dos, and a snapshot over its budget (each left-out section's hint).
func backgroundContexts() []string {
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	small := prompts.Snapshot{Now: now, Zone: "UTC", OpenTodosLoaded: true, OpenTodosTotal: 2,
		Plans: []prompts.SnapshotPlan{{ID: "plan_1", At: now, Titles: []string{"Fix the login page"}, Tasks: 1}}, PlansTotal: 1,
		Chats: &prompts.SnapshotChats{AtMe: []prompts.SnapshotChat{{Name: "Release", Unread: 2, Preview: "Is it out?"}}},
		Asked: []prompts.SnapshotAsked{{TodoID: "todo_1", Question: "Which branch?", At: now}}}
	var out []string
	for _, profile := range []*prompts.SnapshotProfile{nil, {ID: "p", Unreadable: true}, {ID: "p", Gone: true},
		{ID: "p", Name: "Default", Tiers: []prompts.SnapshotTier{{Name: "fast", Launcher: "claude"}}}} {
		for _, snap := range []prompts.Snapshot{small, bigSnapshot(now)} {
			snap.ModelProfile = profile
			out = append(out, prompts.RenderAssistantContext(prompts.AssistantContext{Background: true, Owner: "the owner",
				Snapshot: fitSnapshot(backgroundSnapshot(scopeSnapshot(snap, "")))}))
		}
	}
	return out
}

// Every Persona tool a round's own message names, for every signal the background supervisor gets and in every language
// the owner may read, is one the round lists: a round told to call a tool it lacks cannot do what its message asks.
// decision_reply (#9397) reaches it for a main-chat task whose owner wrote in the Agent's chat, and its entry asks for
// task_decided, without which the paused decision stays pending with no default and no reminder. The descriptions of
// the tools it lists and the background supervisor's context name other tools only in passing (roundPassingMentions):
// agent_send and task_review sent a round to agent_start and ask_user for the orphaned, idle and interrupted tasks whose
// Agent stopped, and the snapshot to task_plan_withdraw for plans only the main conversation withdraws.
func TestRoundListsTheToolsItsMessageNames(t *testing.T) {
	template, err := os.ReadFile("../prompts/assistant/supervise.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`eq \$e\.Kind "([a-z_]+)"`).FindAllStringSubmatch(string(template), -1) {
		if !slices.Contains(backgroundSignals, m[1]) && !slices.Contains(mainOnlySignals, m[1]) {
			t.Errorf("the supervision message words signal %s, which backgroundSignals and mainOnlySignals leave out", m[1])
		}
	}
	for _, lang := range []string{answerLanguage("zh-Hans"), answerLanguage("en"), answerLanguage("ja"), "Japanese"} {
		text := roundMessage(lang)
		named := namedTools(text)
		for _, want := range []string{"todo_get", "agent_read", "task_decided"} {
			if !slices.Contains(named, want) {
				t.Errorf("%q: the round's message does not name %s; the reader or the message changed:\n%s", lang, want, text)
			}
		}
		for _, name := range named {
			if !slices.Contains(roundToolNames, name) {
				t.Errorf("%q: the round's message names %s, which the round does not list (roundToolNames)", lang, name)
			}
		}
	}

	unlisted := map[string][]string{} // where: the tools named there that a round does not list
	note := func(where, text string) {
		for _, name := range namedTools(text) {
			if !slices.Contains(roundToolNames, name) && !slices.Contains(unlisted[where], name) {
				unlisted[where] = append(unlisted[where], name)
			}
		}
	}
	for _, tool := range toolsNamed(slices.DeleteFunc(slices.Clone(roundToolNames), func(n string) bool { return slices.Contains(roundConnectorTools, n) })) {
		note(tool.Name, tool.Description+"\n"+string(tool.InputSchema))
	}
	for _, text := range backgroundContexts() {
		note("context", text)
	}
	if !slices.Contains(unlisted["context"], "ask_user") || len(unlisted["task_plan"]) == 0 {
		t.Fatalf("the scan found too little, or the reader missed some: %v", unlisted)
	}
	for where, names := range unlisted {
		for _, name := range names {
			if !slices.Contains(roundPassingMentions[where], name) {
				t.Errorf("%s names %s, which a round does not list: word it for the round's tools, or list it in "+
					"roundPassingMentions when it sends a round nowhere", where, name)
			}
		}
	}
	for where, names := range roundPassingMentions {
		for _, name := range names {
			if !slices.Contains(unlisted[where], name) {
				t.Errorf("roundPassingMentions lists %s for %s, which no longer names it: drop it", name, where)
			}
		}
	}
}

// A stopped Agent's answers (agent_send without resume, a resume that failed, agent_interrupt) and what agent_send's and
// task_review's descriptions say of one send a supervisor that replaces it to task_plan with continues on its task's
// todo, and to the owner through ask_user or task_review needs_user; agent_start only for work on no task, which a
// round never has. A round got told agent_start or ask_user, neither of which it lists, for the orphaned, idle and
// interrupted tasks whose Agent stopped.
func TestAStoppedAgentsAnswersOfferARoundItsTools(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", stoppedBoxAgent("stop0001"))
	answers := map[string]string{"a resume that failed": resumeFailed(stoppedBoxAgent("stop0001"), Device{ID: "box", Name: "box"},
		machineOffline("box")).Error()}
	if _, err := s.runTool(t.Context(), "server", "conv1", "agent_send", sendArgs("stop0001", "go on", "")); err != nil {
		answers["agent_send"] = err.Error()
	}
	args, _ := json.Marshal(map[string]any{"machine": "box", "session_id": "stop0001", "then": "go on"})
	if _, err := s.runTool(t.Context(), "server", "conv1", "agent_interrupt", args); err != nil {
		answers["agent_interrupt"] = err.Error()
	}
	if len(answers) != 3 {
		t.Fatalf("a stopped agent was not refused: %v", answers)
	}
	// agent_send's and task_review's descriptions name agent_start in passing (roundPassingMentions); what a round gets
	// back when it acts on a stopped Agent is what has to point it at its own tools.
	for what, text := range answers {
		if !strings.Contains(text, "task_plan with continues") {
			t.Errorf("%s does not point a replacement at task_plan with continues: %s", what, text)
		}
		for _, name := range namedTools(text) {
			switch {
			case slices.Contains(roundToolNames, name):
			case name == "agent_start" && strings.Contains(text, "for work on no task, agent_start"):
			case name == "ask_user" && strings.Contains(text, "ask_user, or task_review needs_user"):
			default:
				t.Errorf("%s sends a round to %s, which it does not list: %s", what, name, text)
			}
		}
	}
}

// The background supervisor's snapshot leaves out the main conversation's plan cards waiting for Start (only the main
// conversation withdraws them), the owner's unread chats and the iPhone line; the main conversation's keeps them.
func TestBackgroundSnapshotLeavesOutWhatOnlyMainActsOn(t *testing.T) {
	s, _ := scopeFixture(t)
	now := time.Now()
	snap := prompts.Snapshot{Now: now, Zone: "UTC",
		Plans: []prompts.SnapshotPlan{{ID: "plan_1", At: now, Titles: []string{"Fix the login page"}, Tasks: 1}}, PlansTotal: 1,
		Chats: &prompts.SnapshotChats{AtMe: []prompts.SnapshotChat{{Name: "Release room", Unread: 2, Preview: "Is it out?"}}}}
	s.live.mu.Lock()
	s.live.snap, s.live.at = &snap, now // fresh: no read
	s.live.mu.Unlock()
	for id, keeps := range map[string]bool{"main": true, "bg": false} {
		got := scopedContext(t, s, id)
		for _, part := range []string{"plan_1", "task_plan_withdraw", "Release room", "lark_chats", "phone_"} {
			if strings.Contains(got, part) != keeps {
				t.Errorf("%s's context has %q: %v, want %v:\n%s", id, part, !keeps, keeps, got)
			}
		}
	}
}

// New task signals stay with main, including every kind a Fresh legacy round can render. Only an explicitly addressed
// old notice goes to the already configured background conversation; main-only signals never change that default.
func TestBackgroundRoundGetsOnlyExplicitLegacySignals(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	cfg := s.settings(context.Background())
	if own := s.ownSupervisor(cfg); own != "conv1" {
		t.Fatalf("new tasks are followed up by %q, not the main conversation", own)
	}
	for _, kind := range append(slices.Clone(mainOnlySignals), backgroundSignals...) {
		if got := s.digestTo(context.Background(), cfg, superviseSignal{Todo: todo.ID, Kind: kind}); got != "conv1" {
			t.Errorf("new %s goes to %q, not the main conversation", kind, got)
		}
	}
	for _, kind := range backgroundSignals {
		if got := s.digestTo(context.Background(), cfg, superviseSignal{Todo: todo.ID, Kind: kind, To: "bg1"}); got != "bg1" {
			t.Errorf("explicit legacy %s goes to %q, not the existing background conversation", kind, got)
		}
	}
	for name, g := range map[string]superviseSignal{
		"a task leader's decision for main":    {Todo: todo.ID, Kind: sigDecide, To: "conv1"},
		"a card held by the legacy background": {Todo: todo.ID, Kind: sigAsk, To: "bg1"},
	} {
		want := "bg1"
		if g.To == "conv1" {
			want = "conv1"
		}
		if got := s.digestTo(context.Background(), cfg, g); got != want {
			t.Errorf("%s goes to %q, want %q", name, got, want)
		}
	}
}

// The start remedies stay the main conversation's: the background supervisor calling them is refused, whatever a round
// lists.
func TestRemedyToolsRefuseTheBackgroundSupervisor(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	call := func(self, name string, args map[string]any) proto.ToolResult {
		raw, _ := json.Marshal(args)
		return s.Tool(context.Background(), ToolCall{Name: name, Arguments: raw, MachineID: "server", SessionID: self})
	}
	for name, args := range map[string]map[string]any{
		"task_start_screen": {"todo_id": todo.ID, "reason": "read the tab"},
		"task_start_keys":   {"todo_id": todo.ID, "keys": []string{"enter"}, "reason": "answer the shell's question"},
		"task_retry":        {"todo_id": todo.ID, "reason": "the shell was answered"},
	} {
		if r := call("bg1", name, args); !r.IsError || !strings.Contains(toolText(r), "only the main conversation") {
			t.Errorf("the background supervisor's %s: error %v, %q", name, r.IsError, toolText(r))
		}
		// The refusal is the caller's: the main conversation gets past it, to the task's own state (its start did not
		// fail here).
		if r := call("conv1", name, args); !r.IsError || strings.Contains(toolText(r), "only the main conversation") || !strings.Contains(toolText(r), todo.ID) {
			t.Errorf("the main conversation's %s: error %v, %q", name, r.IsError, toolText(r))
		}
	}
}

// The background supervisor asks the owner with task_review needs_user, the way its instructions say, with the tools a
// round lists: the task waits for the owner, the main conversation gets the escalated entry, and the answer never sends
// the background supervisor to ask_user, which a round does not list. With the Lark bot on the owner gets one
// conclusion card with the recommendation; with it off no card goes out, and the answer has the background supervisor
// tell the owner in its own answer.
func TestBackgroundRoundAsksTheOwnerWithNeedsUser(t *testing.T) {
	if !slices.Contains(roundToolNames, "task_review") || !slices.Contains(roundToolNames, "notify_user") {
		t.Fatal("a round does not list task_review and notify_user")
	}
	for _, bot := range []bool{true, false} {
		name := "the bot off"
		if bot {
			name = "the bot on"
		}
		t.Run(name, func(t *testing.T) {
			if bot {
				needsLarkLayer(t)
			}
			var s *Service
			var f *fakeHub
			var todo Todo
			posts := func() []string { return nil }
			if bot {
				s, f, todo, posts, _ = conclusionService(t, false)
				s.noBackground = false
				f.mu.Lock()
				f.made = 100
				f.mu.Unlock()
			} else {
				s, f, todo = newBackgroundService(t)
			}
			waitsForReview(t, s, f, todo.ID, "Two ways: roll out to 10% first, or to everyone.")
			bg := withBackgroundBG1(t, s, f)
			wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "legacy-review"})
			waitFor(t, "the background supervisor's message", func() bool { return len(f.noticesTo(bg)) == 1 })

			raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "needs_user",
				"why_owner": "priority", "reason": "Roll out to 10% first or to everyone", "recommend": "10% first",
				"options": []map[string]string{{"label": "10% first", "leads_to": "a day of watching before everyone gets it"},
					{"label": "Everyone now", "leads_to": "ships today, a bad build reaches everyone"}},
				"cost_of_waiting": "the fix ships a day later"})
			r := s.Tool(context.Background(), ToolCall{Name: "task_review", Arguments: raw, MachineID: "server", SessionID: bg})
			res := toolText(r)
			if r.IsError || !strings.Contains(res, "waits for the user") {
				t.Fatalf("the background supervisor's needs_user: error %v, %s", r.IsError, res)
			}
			if strings.Contains(res, "ask_user") {
				t.Errorf("the background supervisor's answer sends it to ask_user, which a round does not list:\n%s", res)
			}
			if st := todoNow(s, todo.ID).Supervision.State; st != reviewNeedsUser {
				t.Fatalf("the task's state is %q, not %q", st, reviewNeedsUser)
			}
			const conclusion = "结论：" // i18n-ignore: the conclusion card's own words
			if bot {
				waitFor(t, "the conclusion card", func() bool { return len(cardsWith(posts(), conclusion)) == 1 })
				card := cardsWith(posts(), conclusion)[0]
				for _, want := range []string{"Roll out to 10% first or to everyone", "10% first"} {
					if !strings.Contains(card, want) {
						t.Errorf("the conclusion card lacks %q:\n%s", want, card)
					}
				}
				if !strings.Contains(res, "Don't ask them again") {
					t.Errorf("the answer with a card out does not say not to ask again:\n%s", res)
				}
			} else {
				// No card: the main conversation was told; the owner does not read this conversation, so the answer
				// sends what is to decide to them with notify_user, unless the hub's own push already went to their phone.
				if !strings.Contains(res, "main conversation was told") || strings.Contains(res, "in your answer") {
					t.Errorf("with no card out, the answer does not say the main conversation was told, or sends the owner to this conversation:\n%s", res)
				}
				phone := strings.Contains(res, "A push with your recommendation also went to their phone")
				if phone && (strings.Contains(res, "notify_user") || !strings.Contains(res, "Don't push it again")) {
					t.Errorf("with the hub's push out, the answer asks for a second push:\n%s", res)
				}
				if !phone && !strings.Contains(res, "notify_user") {
					t.Errorf("with no push out, the answer does not send the decision to the owner with notify_user:\n%s", res)
				}
			}
			waitFor(t, "the main conversation's escalated entry", func() bool {
				for _, n := range f.noticesTo("conv1") {
					if strings.Contains(n, "signal="+sigEscalated) {
						return true
					}
				}
				return false
			})
		})
	}
}

// A round checks a Lark thread with the search the install gives: the Lark extension's lark_search or the lark-cli
// connector's larkcli_search (the open-source default). Neither one's chat and message tools are a round's.
func TestRoundKeepsEitherLarkSearch(t *testing.T) {
	var got []string
	for _, tool := range roundTools([]proto.Tool{{Name: "lark_chats"}, {Name: "lark_search"}, {Name: "larkcli_chats"},
		{Name: "larkcli_messages"}, {Name: "larkcli_search"}, {Name: "agent_start"}}) {
		got = append(got, tool.Name)
	}
	if !slices.Equal(got, []string{"lark_search", "larkcli_search"}) {
		t.Fatalf("a round lists %v of the Lark tools, want [lark_search larkcli_search]", got)
	}
}
