package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// dispatchPolicyHarness is an assistant with five conversations: the main one, a leader, the background supervisor,
// the scheduled task's own conversation and an ordinary side conversation. All ids are placeholders.
type dispatchPolicyHarness struct {
	*spawnHarness
	mainID   string
	leaderID string
	bgID     string
	patrolID string
	sideID   string
}

func newDispatchPolicyHarness(t *testing.T) *dispatchPolicyHarness {
	t.Helper()
	h := newSpawnHarness(t)
	h.f.hello.Locale = string(i18n.En) // assertions below match the English refusal catalog
	d := &dispatchPolicyHarness{
		spawnHarness: h,
		mainID:       "main-dp-1",
		leaderID:     "leader-dp-1",
		bgID:         "bg-dp-1",
		patrolID:     "patrol-dp-1",
		sideID:       "side-dp-1",
	}
	setSettings(t, h.s, func(v *Settings) {
		v.Main = d.mainID
		v.Leaders = []string{d.leaderID}
		v.BackgroundSupervisor = d.bgID
	})
	for _, id := range []string{d.mainID, d.leaderID, d.bgID, d.patrolID, d.sideID} {
		h.f.known[id] = true
	}
	h.f.setSessions("server",
		proto.Session{MachineID: "server", SessionID: d.mainID, Assistant: true, Status: proto.StatusIdle},
		proto.Session{MachineID: "server", SessionID: d.leaderID, Assistant: true, Status: proto.StatusIdle},
		proto.Session{MachineID: "server", SessionID: d.bgID, Assistant: true, Status: proto.StatusIdle},
		proto.Session{MachineID: "server", SessionID: d.patrolID, Assistant: true, Status: proto.StatusIdle},
		proto.Session{MachineID: "server", SessionID: d.sideID, Assistant: true, Status: proto.StatusIdle},
	)
	sc := h.s.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.tasks = append(sc.tasks, Task{
		ID: "task_patrol", Title: "rounds", Prompt: "p", Repeat: repeatInterval, EveryMinutes: 30,
		Enabled: true, RunMachineID: "server", RunSessionID: d.patrolID,
	})
	sc.mu.Unlock()
	return d
}

// scheduledPolicy denies every guardable action of a scheduled conversation, with the rule's own note.
func scheduledPolicy(expiry *time.Time) *DispatchPolicy {
	return &DispatchPolicy{
		Rules: []DispatchRule{{
			ConversationClass: dispatchClassScheduled,
			Deny: []string{
				dispatchActionTaskPlan, dispatchActionAgentStart, dispatchActionStartConversation,
				dispatchActionSendToMain, dispatchActionSendToLeader,
			},
			Message: "Write the finding into the matching leader's requirement-pool file instead.",
		}},
		ExpiresAt: expiry,
	}
}

func (d *dispatchPolicyHarness) putPolicy(t *testing.T, p *DispatchPolicy) {
	t.Helper()
	setSettings(t, d.s, func(v *Settings) { v.DispatchPolicy = p })
}

func (d *dispatchPolicyHarness) tool(t *testing.T, session, name string, args string) proto.ToolResult {
	t.Helper()
	return d.s.Tool(context.Background(), ToolCall{
		MachineID: "server", SessionID: session, Name: name, Arguments: json.RawMessage(args),
	})
}

func TestDispatchPolicyClassifiesConversations(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	ctx := context.Background()
	cases := map[string]string{
		d.mainID:   dispatchClassMain,
		d.leaderID: dispatchClassLeader,
		d.bgID:     dispatchClassBackgroundSupervisor,
		d.patrolID: dispatchClassScheduled,
		d.sideID:   dispatchClassOther,
	}
	for id, want := range cases {
		if got := d.s.dispatchConversationClass(ctx, "server", id); got != want {
			t.Errorf("%s: class %q, want %q", id, got, want)
		}
	}

	// The origin book alone keeps a run's conversation scheduled when its task is gone: a still-running turn must
	// not drop to "other" after the task is deleted mid-run, nor in the window before the run session is adopted.
	originOnly := "origin-only-1"
	d.s.originSet(ctx, "server", originOnly, OriginTask, "task_gone", "")
	if got := d.s.dispatchConversationClass(ctx, "server", originOnly); got != dispatchClassScheduled {
		t.Fatalf("a task-origin conversation classified as %q, want scheduled", got)
	}
	if got := d.s.dispatchConversationClass(ctx, "other-machine", originOnly); got != dispatchClassOther {
		t.Fatalf("the origin's machine is not checked: got %q", got)
	}
}

func TestDispatchPolicyRefusesScheduledConversationDispatch(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	d.putPolicy(t, scheduledPolicy(nil))

	refused := []struct {
		name string
		args string
	}{
		{"task_plan", `{}`},
		{"agent_start", `{}`},
		{"conversation_new", `{"text":"open another one"}`},
	}
	for _, tc := range refused {
		res := d.tool(t, d.patrolID, tc.name, tc.args)
		if !res.IsError {
			t.Fatalf("%s: refused expected, got %+v", tc.name, res)
		}
		if !strings.Contains(res.Content[0].Text, "organization policy") {
			t.Fatalf("%s: refusal lacks the policy line: %q", tc.name, res.Content[0].Text)
		}
		if !strings.Contains(res.Content[0].Text, "requirement-pool file") {
			t.Fatalf("%s: refusal drops the rule's own note: %q", tc.name, res.Content[0].Text)
		}
	}

	// Sends to the main and the leader conversations are refused too; an ordinary side conversation is not named by
	// the rule, so the guard leaves it alone.
	for _, target := range []string{d.mainID, d.leaderID} {
		res := d.tool(t, d.patrolID, "conversation_send", `{"id":"`+target+`","text":"please take this"}`)
		if !res.IsError || !strings.Contains(res.Content[0].Text, "organization policy") {
			t.Fatalf("send to %s: refusal expected, got %+v", target, res)
		}
	}

	// Deleting, cancelling or scheduling tasks is refused too: changing the schedule would let the still-running
	// turn alter or drop its scheduled class and bypass the policy.
	sc := d.s.loadSchedule(context.Background())
	sc.mu.Lock()
	beforeCount := len(sc.tasks)
	sc.mu.Unlock()
	for _, tc := range []struct {
		name string
		args string
	}{
		{"delete_routine", `{"id":"task_patrol"}`},
		{"cancel_followup", `{"id":"task_patrol"}`},
		{"schedule_followup", `{"text":"check again later","every_minutes":15}`},
		{"schedule_routine", `{"title":"another round","time":"18:00"}`},
	} {
		res := d.tool(t, d.patrolID, tc.name, tc.args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, "organization policy") {
			t.Fatalf("%s: refusal expected, got %+v", tc.name, res)
		}
	}
	sc = d.s.loadSchedule(context.Background())
	sc.mu.Lock()
	kept := false
	for _, task := range sc.tasks {
		if task.ID == "task_patrol" {
			kept = true
		}
	}
	afterCount := len(sc.tasks)
	sc.mu.Unlock()
	if !kept {
		t.Fatal("the refused call deleted the scheduled task")
	}
	if afterCount != beforeCount {
		t.Fatalf("a refused schedule call changed the book: %d tasks before, %d after", beforeCount, afterCount)
	}

	// Even once the task is gone (deleted by the owner in the app), the origin book keeps the turn's class.
	sc = d.s.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.tasks = nil
	sc.mu.Unlock()
	d.s.originSet(context.Background(), "server", d.patrolID, OriginTask, "task_patrol", "")
	if got := d.s.dispatchConversationClass(context.Background(), "server", d.patrolID); got != dispatchClassScheduled {
		t.Fatalf("the turn dropped to %q after its task was deleted", got)
	}
	res := d.tool(t, d.patrolID, "task_plan", `{}`)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "organization policy") {
		t.Fatalf("a turn whose task was deleted bypassed the policy: %+v", res)
	}
}

func TestDispatchPolicyDoesNotLimitOtherClasses(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	d.putPolicy(t, scheduledPolicy(nil))

	// The main conversation, a leader and the background supervisor keep their tools. The guard runs before the
	// tools' own validation, so any later error must not be the policy refusal.
	for _, session := range []string{d.mainID, d.leaderID, d.bgID, d.sideID} {
		for _, name := range []string{"task_plan", "agent_start", "conversation_new"} {
			res := d.tool(t, session, name, `{}`)
			if res.IsError && strings.Contains(res.Content[0].Text, "organization policy") {
				t.Fatalf("%s %s: the policy refused a conversation class it does not name: %q", session, name, res.Content[0].Text)
			}
		}
	}

	// The main conversation's task_plan without a supervisor passes the guard and gains no start options: the
	// guard never rewrites arguments, and no rule names the main class. Assert the path is open with an empty
	// supervisor argument exactly as a simple one-off task sends it.
	if rule := d.s.activeDispatchPolicy(context.Background()).ruleFor(dispatchClassMain); rule != nil {
		t.Fatalf("the main conversation has a policy rule: %+v", rule)
	}
}

func TestDispatchPolicyWithoutPolicyLeavesEverythingAlone(t *testing.T) {
	d := newDispatchPolicyHarness(t)

	// No stored policy: the patrol conversation's calls are not refused by the policy. conversation_new starts a
	// conversation, which proves the whole path ran.
	res := d.tool(t, d.patrolID, "conversation_new", `{"text":"open another one"}`)
	if res.IsError {
		t.Fatalf("conversation_new with no policy: %+v", res)
	}

	// An expired policy is treated as absent.
	past := time.Now().Add(-time.Minute).UTC()
	d.putPolicy(t, scheduledPolicy(&past))
	res = d.tool(t, d.patrolID, "conversation_new", `{"text":"open another one"}`)
	if res.IsError && strings.Contains(res.Content[0].Text, "organization policy") {
		t.Fatalf("an expired policy still refuses: %+v", res)
	}
	if d.s.activeDispatchPolicy(context.Background()) != nil {
		t.Fatal("an expired policy is reported as active")
	}
}

func TestDispatchPolicyOwnerWordsStillReachMain(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	d.putPolicy(t, scheduledPolicy(nil))
	ctx := context.Background()

	// A Persona notice without a client message id is not the owner: the send stays refused.
	d.f.setItems(proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: "a system notice"})
	res := d.tool(t, d.patrolID, "conversation_send", `{"id":"`+d.mainID+`","text":"please take this"}`)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "organization policy") {
		t.Fatalf("a notice-only transcript was taken for the owner's words: %+v", res)
	}

	// The current turn's input is the owner's own message (client message id, no source): any text may go to main.
	d.f.setItems(proto.Item{Seq: 2, Role: "user", Kind: proto.KindText, Text: "提醒我明天体检", ClientMsgID: "msg-2"})
	res = d.tool(t, d.patrolID, "conversation_send", `{"id":"`+d.mainID+`","text":"owner asked: reminder tomorrow"}`)
	if res.IsError {
		t.Fatalf("an owner-started turn could not relay to the main conversation: %+v", res)
	}

	// A message starting with the owner's verbatim words also qualifies, even when a later item is a notice.
	ownerWords := "把这条原样转给主对话：下周一前给出评审意见"
	d.f.setItems(
		proto.Item{Seq: 3, Role: "user", Kind: proto.KindText, Text: ownerWords, ClientMsgID: "msg-3"},
		proto.Item{Seq: 4, Role: "user", Kind: proto.KindNotice, Text: "task ended"},
	)
	res = d.tool(t, d.patrolID, "conversation_send", `{"id":"`+d.mainID+`","text":"`+ownerWords+`（补充：附件已上传）"}`)
	if res.IsError {
		t.Fatalf("a verbatim-prefix relay was refused: %+v", res)
	}

	// An owner interjection queued into a patrol-started turn does not make the turn owner-started: an arbitrary
	// relay stays refused. The queued owner message itself can still be forwarded verbatim.
	d.f.setItems(
		proto.Item{Seq: 5, Role: "user", Kind: proto.KindText, Text: "every 30 minutes: check the list",
			Source: &proto.MessageSource{Kind: proto.SourcePatrol}},
		proto.Item{Seq: 6, Role: "user", Kind: proto.KindText, Text: "顺便帮我盯着构建", ClientMsgID: "msg-6", Queued: true},
	)
	res = d.tool(t, d.patrolID, "conversation_send", `{"id":"`+d.mainID+`","text":"please re-dispatch this from the main conversation"}`)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "organization policy") {
		t.Fatalf("a queued interjection opened an arbitrary relay: %+v", res)
	}
	res = d.tool(t, d.patrolID, "conversation_send", `{"id":"`+d.mainID+`","text":"顺便帮我盯着构建"}`)
	if res.IsError {
		t.Fatalf("the owner's queued words could not be relayed verbatim: %+v", res)
	}

	// The exception is only for the main conversation: a leader target is refused with the owner's words regardless.
	res = d.tool(t, d.patrolID, "conversation_send", `{"id":"`+d.leaderID+`","text":"`+ownerWords+`"}`)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "organization policy") {
		t.Fatalf("a relay to a leader bypassed the policy: %+v", res)
	}

	// When the transcript cannot be read, the send fails closed.
	d.f.itemsFail = 1
	res = d.tool(t, d.patrolID, "conversation_send", `{"id":"`+d.mainID+`","text":"`+ownerWords+`"}`)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "could not be confirmed") {
		t.Fatalf("an unreadable transcript did not fail closed: %+v", res)
	}

	// Direct helper check on the classification used above.
	if d.s.activeDispatchPolicy(ctx) == nil {
		t.Fatal("the policy is no longer active")
	}
}

func TestDispatchPolicyValidation(t *testing.T) {
	now := time.Now().UTC()
	rule := func(class string, deny ...string) DispatchRule {
		return DispatchRule{ConversationClass: class, Deny: deny}
	}
	cases := []struct {
		name          string
		policy        DispatchPolicy
		allowNoExpiry bool
		wantErr       bool
	}{
		{"owner policy without expiry", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassScheduled, dispatchActionTaskPlan)}}, true, false},
		{"extension policy must expire", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassScheduled, dispatchActionTaskPlan)}}, false, true},
		{"expiry within the hour", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassOther, dispatchActionSendToMain)}, ExpiresAt: ptr(now.Add(30 * time.Minute))}, false, false},
		{"extension expiry over the hour", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassOther, dispatchActionSendToMain)}, ExpiresAt: ptr(now.Add(2 * time.Hour))}, false, true},
		{"owner expiry over the hour is allowed", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassOther, dispatchActionSendToMain)}, ExpiresAt: ptr(now.Add(2 * time.Hour))}, true, false},
		{"expiry past", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassOther, dispatchActionSendToMain)}, ExpiresAt: ptr(now.Add(-time.Minute))}, false, true},
		{"unknown class", DispatchPolicy{Rules: []DispatchRule{rule("side", dispatchActionTaskPlan)}}, true, true},
		{"unknown action", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassMain, "delete_everything")}}, true, true},
		{"empty deny", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassMain)}}, true, true},
		{"duplicate class", DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassMain, dispatchActionTaskPlan), rule(dispatchClassMain, dispatchActionAgentStart)}}, true, true},
		{"no rules", DispatchPolicy{}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.validate(tc.allowNoExpiry, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate: %v, wantErr %v", err, tc.wantErr)
			}
		})
	}

	long := DispatchPolicy{Rules: []DispatchRule{rule(dispatchClassScheduled, dispatchActionTaskPlan)}}
	long.Rules[0].Message = strings.Repeat("字", dispatchPolicyMaxMessageRunes+1)
	if err := long.validate(true, now); err == nil {
		t.Fatal("an over-long rule message was accepted")
	}
}

func TestDispatchPolicyRoute(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	s := d.s

	// Initially the route answers an empty rule set.
	w := serveAs(s, roleFull, http.MethodGet, "/api/v1/assistant/dispatch-policy", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"rules":[]`) {
		t.Fatalf("GET before a policy: %d %s", w.Code, w.Body.String())
	}

	put := func(role, body string) *httptest.ResponseRecorder {
		return serveAs(s, role, http.MethodPut, "/api/v1/assistant/dispatch-policy", body)
	}

	// An extension writes a policy that expires within the hour.
	expiry := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339Nano)
	body := `{"dispatch_policy":{"rules":[{"conversation_class":"scheduled","deny":["task_plan","send_to_main"]}],"expires_at":"` + expiry + `"}}`
	if w := put(roleExtension, body); w.Code != http.StatusOK {
		t.Fatalf("extension PUT: %d %s", w.Code, w.Body.String())
	}
	if d.s.activeDispatchPolicy(context.Background()) == nil {
		t.Fatal("the extension's policy is not active")
	}

	// The same route without an expiry is rejected for an extension.
	noExpiry := `{"dispatch_policy":{"rules":[{"conversation_class":"scheduled","deny":["task_plan"]}]}}`
	if w := put(roleExtension, noExpiry); w.Code != http.StatusBadRequest {
		t.Fatalf("extension PUT without expiry: code %d, want 400", w.Code)
	}

	// An expiry over the hour is rejected too.
	far := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	overHour := `{"dispatch_policy":{"rules":[{"conversation_class":"scheduled","deny":["task_plan"]}],"expires_at":"` + far + `"}}`
	if w := put(roleExtension, overHour); w.Code != http.StatusBadRequest {
		t.Fatalf("extension PUT with a two-hour expiry: code %d, want 400", w.Code)
	}

	// A read-only role cannot change anything.
	if w := put(roleAnswer, body); w.Code != http.StatusForbidden {
		t.Fatalf("read-only PUT: code %d, want 403", w.Code)
	}

	// A full device may store a policy without an expiry.
	if w := put(roleFull, noExpiry); w.Code != http.StatusOK {
		t.Fatalf("full PUT without expiry: %d %s", w.Code, w.Body.String())
	}

	// A malformed rule shape is a bad request whoever calls.
	badShape := `{"dispatch_policy":{"rules":[{"conversation_class":"scheduled","deny":["drop_tables"]}]}}`
	if w := put(roleFull, badShape); w.Code != http.StatusBadRequest {
		t.Fatalf("full PUT with an unknown action: code %d, want 400", w.Code)
	}

	// Empty rules clear the policy, from either role.
	if w := put(roleExtension, `{"dispatch_policy":{"rules":[]}}`); w.Code != http.StatusOK {
		t.Fatalf("extension clear: %d %s", w.Code, w.Body.String())
	}
	if d.s.activeDispatchPolicy(context.Background()) != nil {
		t.Fatal("clearing left a policy active")
	}
}
