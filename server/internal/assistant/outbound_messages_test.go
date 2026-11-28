package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type outboundMemoryStore struct {
	*inboxMemStore
	auditMu      sync.Mutex
	audit        []db.SentAsUser
	failAudit    bool
	failOutcome  bool
	claims       map[string]string
	identityRead chan struct{}
}

func (st *outboundMemoryStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	found, err := st.inboxMemStore.Setting(ctx, key, out)
	if key == outboundIdentityKey && st.identityRead != nil {
		select {
		case st.identityRead <- struct{}{}:
		default:
		}
	}
	return found, err
}

func (st *outboundMemoryStore) ClaimSend(_ context.Context, id, _, _ string) (string, error) {
	st.auditMu.Lock()
	defer st.auditMu.Unlock()
	if st.claims == nil {
		st.claims = map[string]string{}
	}
	before := st.claims[id]
	if before == "" {
		st.claims[id] = "sending"
	}
	return before, nil
}

func (st *outboundMemoryStore) SettleSend(_ context.Context, id, state string) error {
	st.auditMu.Lock()
	defer st.auditMu.Unlock()
	st.claims[id] = state
	return nil
}

func (st *outboundMemoryStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	var before StandingAuthorizations
	_, err := st.Setting(ctx, key, &before)
	if err != nil || before.Revision != revision {
		return false, err
	}
	return true, st.PutSetting(ctx, key, value)
}

func (st *outboundMemoryStore) RecordSentAsUser(_ context.Context, row db.SentAsUser) (int64, error) {
	st.auditMu.Lock()
	defer st.auditMu.Unlock()
	if st.failAudit {
		return 0, errors.New("audit unavailable")
	}
	row.ID = int64(len(st.audit) + 1)
	st.audit = append(st.audit, row)
	return row.ID, nil
}

func (st *outboundMemoryStore) SetSentAsUserOutcome(_ context.Context, id int64, outcome string) error {
	st.auditMu.Lock()
	defer st.auditMu.Unlock()
	if st.failOutcome {
		return errors.New("outcome write failed")
	}
	var through map[string]string
	_ = json.Unmarshal(st.audit[id-1].LetThrough, &through)
	through["outcome"] = outcome
	st.audit[id-1].LetThrough, _ = json.Marshal(through)
	return nil
}

type outboundConnector struct {
	extensionLark
	identities  []string
	fail        error
	beforeReply func()
}

func (*outboundConnector) ID() string { return "larkcli" }
func (c *outboundConnector) Reply(ctx context.Context, reply connector.InboxReply) error {
	if c.beforeReply != nil {
		c.beforeReply()
	}
	c.identities = append(c.identities, connectorwire.SendIdentity(ctx))
	if c.fail != nil {
		return c.fail
	}
	return c.extensionLark.Reply(ctx, reply)
}

func messageTestGrant(scenario string) StandingAuthorization {
	return StandingAuthorization{ID: "sa_messages", Actions: []string{"send"}, Conditions: []string{}, Quote: "Send updates in the example group.",
		Source: "settings", GrantedAt: time.Now().UTC(), Reversible: true,
		Messages: &MessageAuthorization{Channel: "larkcli", Target: "oc_example_group", Scenario: scenario, Identity: "bot"}}
}

func outboundTestHub(t *testing.T) (*inboxHub, *outboundMemoryStore, *outboundConnector, *InboxItem) {
	t.Helper()
	h, _, it := handledHub(t)
	st := &outboundMemoryStore{inboxMemStore: h.st}
	h.setStore(func() Store { return st })
	c := &outboundConnector{}
	h.in.conns = []connector.Connector{c}
	it.Source, it.Lark = "larkcli", nil
	it.Connector = &InboxConnector{Thread: "larkcli:chat:oc_example_group:thread:omt_example", CanReply: true}
	return h, st, c, it
}

func putMessageGrant(t *testing.T, st Store, scenario string) {
	t.Helper()
	if err := st.PutSetting(context.Background(), standingAuthorizationsKey, StandingAuthorizations{
		Revision: 1, Authorizations: []StandingAuthorization{messageTestGrant(scenario)},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOutboundMessageAndAgentTransferRemainAssistantTools(t *testing.T) {
	s := newTestService(t, newFakeHub())
	tools := s.Tools(context.Background())
	for _, name := range []string{"message_send", "agent_transfer"} {
		count := 0
		for _, tool := range tools {
			if tool.Name == name {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("assistant catalog has %d entries for %s, want exactly one", count, name)
		}
		for _, tool := range s.AgentTools() {
			if tool.Name == name {
				t.Fatalf("%s must not be exposed to ordinary agents", name)
			}
		}
	}
}

func TestOutboundPromptsFitExistingBudget(t *testing.T) {
	raw, err := os.ReadFile(promptSizeBaseline)
	if err != nil {
		t.Fatal(err)
	}
	var baseline map[string]int
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	limit := 0
	for _, size := range baseline {
		limit += size
	}

	sizes := promptSizePieces(t)
	// Measure live sources too: stale goldens must not hide a newly added tool or longer instructions.
	tool, err := json.Marshal(toolsNamed([]string{"message_send"})[0])
	if err != nil {
		t.Fatal(err)
	}
	sizes["mcp_tools.assistant#message_send"] = len(tool)
	s := newTestService(t, claudeMDHub(Device{ID: "server", Name: "服务器", Kind: "local"}))
	s.kb.dir = "<hub>/data"
	total := len(s.claudeMD(context.Background(), "server", true))
	for key, size := range sizes {
		if !strings.HasPrefix(key, "assistant_claude_md.lark_cli#") {
			total += size
		}
	}
	t.Logf("prompt footprint: %d bytes; existing budget: %d bytes", total, limit)
	if total > limit {
		t.Fatalf("outbound prompts exceed the unchanged budget by %d bytes", total-limit)
	}
}

func TestOutboundScopeIsStructuredAndConservative(t *testing.T) {
	grant := messageTestGrant("inbox_reply")
	if err := checkStandingGrant(grant); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*StandingAuthorization){
		func(g *StandingAuthorization) { g.Messages.Channel = "other" },
		func(g *StandingAuthorization) { g.Messages.Target = "oc_example_*" },
		func(g *StandingAuthorization) { g.Messages.Target = "oc_example_group:thread:other" },
		func(g *StandingAuthorization) { g.Messages.Scenario = "anything" },
		func(g *StandingAuthorization) { g.Messages.Identity = "auto" },
		func(g *StandingAuthorization) { g.Actions = []string{"send", "delete"} },
		func(g *StandingAuthorization) { g.Conditions = []string{"safe messages only"} },
		func(g *StandingAuthorization) { g.MachineID = "computer" },
		func(g *StandingAuthorization) { g.Directory = "/code/example" },
		func(g *StandingAuthorization) { g.Reversible = false },
	} {
		g := messageTestGrant("inbox_reply")
		mutate(&g)
		if checkStandingGrant(g) == nil {
			t.Fatalf("unsafe grant accepted: %+v / %+v", g, g.Messages)
		}
	}
}

func TestOutboundInboxAutomaticSendAndRevocation(t *testing.T) {
	ctx := context.Background()
	h, st, c, it := outboundTestHub(t)
	// Hold the handling pass the send's background delivery schedules, so it cannot move the item under the test.
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	putMessageGrant(t, st, "inbox_reply")
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"The example update is ready."}`); err != nil {
		t.Fatal(err)
	}
	if len(c.got) != 1 || c.got[0].Text != "The example update is ready." || c.identities[0] != "bot" {
		t.Fatalf("approved suggestion did not send as bot: %+v %v", c.got, c.identities)
	}
	if len(st.audit) != 1 || !strings.Contains(string(st.audit[0].LetThrough), `"authorization_id":"sa_messages"`) ||
		!strings.Contains(string(st.audit[0].LetThrough), `"outcome":"sent"`) || !strings.Contains(string(st.audit[0].Target), "oc_example_group") {
		t.Fatalf("missing exact audit: %+v", st.audit)
	}
	// The send tells the conversation in the background (tellSoon), and that prompt opens its next processing turn.
	// The later suggestion below belongs to that turn: finish the delivery first; the item's delivery lock waits for
	// one already under way.
	h.deliverNotes(ctx, it)
	if w := serve(h.Service, "DELETE", "/api/v1/assistant/standing-authorizations/sa_messages", ""); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	h.inbox.mu.Lock()
	it.Agent.State = AgentRunning
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"A later update must wait."}`); err != nil {
		t.Fatal(err)
	}
	if len(c.got) != 1 || currentSuggestion(it) == nil {
		t.Fatal("revoked grant still sent, or manual draft disappeared")
	}
	if grants, err := h.standingForDirectory(ctx, "", "/code/example"); err != nil || len(grants) != 0 {
		t.Fatalf("message scope leaked into repository grants: %+v %v", grants, err)
	}
}

func TestOutboundInboxDefaultsAndScopeMismatchesStayManual(t *testing.T) {
	for _, tc := range []string{"default", "wrong chat", "wrong scenario", "changed identity", "handback", "document changes", "repository send"} {
		t.Run(tc, func(t *testing.T) {
			h, st, c, it := outboundTestHub(t)
			g := messageTestGrant("inbox_reply")
			body := `{"kind":"reply","text":"An example reply."}`
			switch tc {
			case "wrong chat":
				g.Messages.Target = "oc_different_group"
			case "wrong scenario":
				g.Messages.Scenario = "assistant_message"
			case "changed identity":
				_ = st.PutSetting(context.Background(), outboundIdentityKey, "user")
			case "handback":
				body = `{"kind":"reply","text":"Please decide.","handback":true}`
			case "document changes":
				// A document/mutating suggestion is not an automatic plain-text chat reply.
				it.Source = "documents"
			case "repository send":
				g = standingRegistryGrant()
				g.Actions, g.Conditions = []string{"send"}, []string{}
			}
			if tc != "default" {
				_ = st.PutSetting(context.Background(), standingAuthorizationsKey, StandingAuthorizations{Revision: 1, Authorizations: []StandingAuthorization{g}})
			}
			if _, err := suggestTool(t, h, body); err != nil {
				t.Fatal(err)
			}
			if len(c.got) != 0 || currentSuggestion(it) == nil {
				t.Fatalf("%s bypassed confirmation", tc)
			}
		})
	}
}

func TestOutboundHandedOffSuggestionStaysManual(t *testing.T) {
	h, st, c, it := outboundTestHub(t)
	putMessageGrant(t, st, "inbox_reply")
	setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking},
		proto.Session{SessionID: "m1", Assistant: true})
	handOff(t, h, it)
	text := "A handed-off reply still needs the owner."
	if _, err := mainSuggest(h, "m1", string(standingJSON(t, map[string]string{
		"item": it.ID, "kind": "reply", "text": text,
	}))); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	g := currentSuggestion(it)
	waiting := g != nil && g.Conversation != nil && g.Conversation.SessionID == "m1"
	version := it.SuggestSeq
	h.inbox.mu.Unlock()
	if !waiting || len(c.got) != 0 || len(st.audit) != 0 {
		t.Fatalf("handed-off reply bypassed confirmation: waiting=%v sends=%+v audit=%+v", waiting, c.got, st.audit)
	}
	body := fmt.Sprintf(`{"version":%d,"action":"confirm"}`, version)
	if code, out := suggestionCall(t, h, it.ID, body); code != 200 {
		t.Fatalf("manual confirmation: %d %v", code, out)
	}
	if len(c.got) != 1 || c.got[0].Text != text || c.identities[0] != "bot" {
		t.Fatalf("manual handed-off reply no longer sends: %+v %v", c.got, c.identities)
	}
}

func TestOutboundAutomaticSendFailsClosedWithoutAudit(t *testing.T) {
	h, st, c, it := outboundTestHub(t)
	putMessageGrant(t, st, "inbox_reply")
	st.failAudit = true
	_, err := suggestTool(t, h, `{"kind":"reply","text":"Do not send without a ledger."}`)
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != assistantwire.CodeInboxSendUnrecorded {
		t.Fatalf("audit failure was hidden: %v", err)
	}
	if len(c.got) != 0 {
		t.Fatal("send escaped without durable audit")
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	a := suggestionAttempt(it, it.SuggestSeq)
	if a == nil || a.Status != suggestionFailed || a.Code != assistantwire.CodeInboxSendUnrecorded {
		t.Fatalf("audit failure should be recorded as unsent: %+v", a)
	}
}

func TestOutboundAssistantToolUsesGrantAndSendOnce(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	putMessageGrant(t, st, "assistant_message")
	args := json.RawMessage(`{"channel":"larkcli","target":"oc_example_group","text":"Assistant update.","idempotency_key":"example-update-1"}`)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := h.runTool(context.Background(), "server", "s1", "message_send", args)
		if (err != nil) != (attempt > 0) {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if len(c.got) != 1 || len(c.identities) != 1 || c.identities[0] != "bot" {
		t.Fatalf("send-once/identity: %+v %v", c.got, c.identities)
	}
}

func TestOutboundToolRejectsTextOverUTF8ByteLimit(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	putMessageGrant(t, st, "assistant_message")
	args := map[string]string{
		"channel": "larkcli", "target": "oc_example_group", "text": strings.Repeat("界", 5334), "idempotency_key": "oversize",
	}
	_, err := h.toolMessageSend(t.Context(), "server", "s1", standingJSON(t, args))
	if err == nil || !strings.Contains(err.Error(), "16000 bytes") || len(c.got) != 0 || len(st.audit) != 0 {
		t.Fatalf("oversize text must fail before audit or transport: %v", err)
	}
}

func TestOutboundToolRejectsRestrictedSpaceEvenWithGrant(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.MachineID = "server" })
	h.f.setSessions("server", proto.Session{SessionID: "health1", MachineID: "server", Assistant: true, Space: SpaceHealth})
	putMessageGrant(t, st, "assistant_message")
	args := json.RawMessage(`{"channel":"larkcli","target":"oc_example_group","text":"Synthetic update.","idempotency_key":"restricted-space"}`)
	_, err := h.runTool(t.Context(), "server", "health1", "message_send", args)
	if err == nil || !strings.Contains(err.Error(), "this conversation cannot send outward messages") || len(c.got) != 0 || len(st.audit) != 0 {
		t.Fatalf("restricted space must fail before audit or transport: %v", err)
	}
}

func TestOutboundManualInboxSenderMismatchPreventsTransport(t *testing.T) {
	h, _, c, it := outboundTestHub(t)
	path := "/api/v1/inbox/" + it.ID + "/reply"
	w := serve(h.Service, "POST", path, `{"text":"Synthetic manual reply","expected_sender_identity":"user"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "outbound_sender_changed") || len(c.got) != 0 {
		t.Fatalf("mismatched sender must fail before transport: %d %s", w.Code, w.Body)
	}
	w = serve(h.Service, "POST", path, `{"text":"Synthetic manual reply","expected_sender_identity":"bot"}`)
	if w.Code != 200 || len(c.got) != 1 || c.identities[0] != "bot" {
		t.Fatalf("matching sender must be passed to transport: %d %s", w.Code, w.Body)
	}
}

func TestOutboundAuditCapsTextWithoutTruncatingTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"short", "An example update.\nAnother line."},
		{"boundary", strings.Repeat("x", 4000)},
		{"long_ascii", strings.Repeat("x", 16000)},
		{"multibyte", strings.Repeat("\u754c", 4001)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st, c, _ := outboundTestHub(t)
			putMessageGrant(t, st, "assistant_message")
			payload := map[string]string{
				"channel": "larkcli", "target": "oc_example_group", "text": tc.text, "idempotency_key": "long-audit",
			}
			if _, err := h.toolMessageSend(context.Background(), "server", "s1", standingJSON(t, payload)); err != nil {
				t.Fatal(err)
			}
			if len(c.got) != 1 || c.got[0].Text != tc.text {
				t.Fatal("audit truncation changed the transport's full text")
			}
			if len(st.audit) != 1 {
				t.Fatalf("audit rows = %d, want 1", len(st.audit))
			}
			row := st.audit[0]
			runes := []rune(tc.text)
			if !utf8.ValidString(row.Text) || row.Text != string(runes[:min(len(runes), 4000)]) {
				t.Errorf("audit text has %d characters, want the first %d", utf8.RuneCountInString(row.Text), min(len(runes), 4000))
			}
			summary := []rune(oneLine(tc.text))
			if row.Summary != string(summary[:min(len(summary), 300)]) {
				t.Errorf("audit summary has %d characters, want a bounded single-line summary", utf8.RuneCountInString(row.Summary))
			}
		})
	}
}

func TestOutboundInboxDuplicateSettlesSentAndFollowUp(t *testing.T) {
	h, st, c, it := outboundTestHub(t)
	ctx := context.Background()
	text := "The example reply was already delivered."
	reply := connector.InboxReply{Thread: it.Connector.Thread, ItemID: it.ID, Last: it.Last, Text: text}
	sendID := "auto-reply:" + namedClientID(it.Source+"\n"+reply.Thread+"\n"+reply.Last+"\nbot")
	// The extension kept the delivered ID, but the assistant lost its audit
	// claim and Inbox bookkeeping before receiving the transport's answer.
	if _, err := h.in.sendOnce(sendID, func() error {
		return c.Reply(connectorwire.WithSendIdentity(ctx, "bot"), reply)
	}); err != nil {
		t.Fatal(err)
	}
	putMessageGrant(t, st, "inbox_reply")
	if _, err := suggestTool(t, h, string(standingJSON(t, map[string]string{"kind": "reply", "text": text}))); err != nil {
		t.Fatalf("delivered duplicate failed confirmation: %v", err)
	}
	if len(c.got) != 1 || len(c.identities) != 1 || c.identities[0] != "bot" {
		t.Fatalf("duplicate resent or changed identity: sends=%d identities=%v", len(c.got), c.identities)
	}
	if len(st.audit) != 1 || !strings.Contains(string(st.audit[0].LetThrough), `"outcome":"already_delivered"`) {
		t.Fatalf("duplicate lost its audit outcome: %+v", st.audit)
	}
	h.inbox.mu.Lock()
	if it.Handling.State != HandlingWaiting || it.Agent.SentOf != it.Last || it.Follow == nil ||
		it.Follow.CheckAt.IsZero() || currentSuggestion(it) != nil {
		t.Errorf("delivered duplicate lost sent/waiting state: handling=%+v agent=%+v follow=%+v", it.Handling, it.Agent, it.Follow)
	}
	kinds := chainKinds(it)
	if len(kinds) < 2 || kinds[len(kinds)-2] != ChainSent || kinds[len(kinds)-1] != ChainWaiting {
		t.Errorf("delivered duplicate lost sent/waiting chain: %v", kinds)
	}
	h.inbox.mu.Unlock()
	st.imu.Lock()
	raw := append([]byte(nil), st.rows[it.ID]...)
	st.imu.Unlock()
	var saved InboxItem
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Handling == nil ||
		saved.Handling.State != HandlingWaiting || saved.Agent == nil || saved.Agent.SentOf != it.Last || saved.Follow == nil {
		t.Errorf("delivered duplicate did not persist sent/waiting state: %s / %v", raw, err)
	}
	toldConv(t, h, text)
}

func TestOutboundFailureNeverRetriesOrChangesIdentity(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	putMessageGrant(t, st, "assistant_message")
	c.fail = errors.New("the bot is not in this chat")
	args := json.RawMessage(`{"channel":"larkcli","target":"oc_example_group","text":"Do not retry automatically.","idempotency_key":"failed-one"}`)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := h.runTool(context.Background(), "server", "s1", "message_send", args)
		if err == nil {
			t.Fatal("failed send reported success")
		}
	}
	if len(c.identities) != 1 || c.identities[0] != "bot" {
		t.Fatalf("retried or changed sender: %v", c.identities)
	}
	if len(st.audit) != 1 || !strings.Contains(string(st.audit[0].LetThrough), "failed_or_unknown") {
		t.Fatalf("failed attempt missing: %+v", st.audit)
	}
}

func TestOutboundInboxRewordingCannotRetryAnUncertainSend(t *testing.T) {
	h, st, c, it := outboundTestHub(t)
	putMessageGrant(t, st, "inbox_reply")
	c.fail = errors.New("delivery outcome unknown")
	for _, text := range []string{"First wording.", "A reworded retry."} {
		h.inbox.mu.Lock()
		it.Agent.State = AgentRunning
		h.inbox.mu.Unlock()
		body, _ := json.Marshal(map[string]string{"kind": "reply", "text": text})
		if _, err := suggestTool(t, h, string(body)); err == nil {
			t.Fatal("uncertain/repeated source-message reply reported success")
		}
	}
	if len(c.identities) != 1 || len(st.audit) != 1 {
		t.Fatalf("rewording caused another attempt: identities=%v audit=%+v", c.identities, st.audit)
	}
}

func TestOutboundPendingAuditNeverTriggersAnotherSend(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	putMessageGrant(t, st, "assistant_message")
	st.failOutcome = true
	args := json.RawMessage(`{"channel":"larkcli","target":"oc_example_group","text":"Possibly delivered.","idempotency_key":"pending-one"}`)
	for range 2 {
		if _, err := h.runTool(context.Background(), "server", "s1", "message_send", args); err == nil {
			t.Fatal("pending outcome reported as confirmed delivery")
		}
	}
	if len(c.got) != 1 || len(st.audit) != 1 || !strings.Contains(string(st.audit[0].LetThrough), `"outcome":"pending"`) {
		t.Fatalf("pending audit retried or lost: %+v %+v", c.got, st.audit)
	}
}

func TestOutboundTransportDuplicateIsNotANewDelivery(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	putMessageGrant(t, st, "assistant_message")
	payload := map[string]string{
		"channel": "larkcli", "target": "oc_example_group", "text": "The original message.", "idempotency_key": "surviving-send",
	}
	if _, err := h.toolMessageSend(context.Background(), "server", "s1", standingJSON(t, payload)); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the assistant without its audit claim; the manager still has
	// the delivered send ID, which must not certify a different attempted text.
	st = &outboundMemoryStore{inboxMemStore: st.inboxMemStore}
	h.setStore(func() Store { return st })
	payload["text"] = "This different text was never sent."
	_, err := h.toolMessageSend(context.Background(), "server", "s1", standingJSON(t, payload))
	var message *i18n.Message
	if !errors.As(err, &message) || message.Key != "assistant-outbound.already_delivered" {
		t.Fatalf("duplicate reported as a fresh successful send: %v", err)
	}
	if len(c.got) != 1 || c.got[0].Text != "The original message." || len(st.audit) != 1 ||
		st.audit[0].Text != payload["text"] || !strings.Contains(string(st.audit[0].LetThrough), `"outcome":"already_delivered"`) {
		t.Fatalf("duplicate lost its actual outcome: sends=%+v audit=%+v", c.got, st.audit)
	}
	if _, err := h.toolMessageSend(context.Background(), "server", "s1", standingJSON(t, payload)); err == nil ||
		len(c.got) != 1 || len(st.audit) != 1 {
		t.Fatalf("duplicate attempt was not settled durably: sends=%+v audit=%+v err=%v", c.got, st.audit, err)
	}
}

func TestOutboundOldTransportIsRejectedBeforeSending(t *testing.T) {
	h, st, _, _ := outboundTestHub(t)
	putMessageGrant(t, st, "assistant_message")
	h.f.inbox = nil
	h.f.answerApp(AppInbox, assistantwire.InboxMessagePolicy, func(json.RawMessage) (any, error) {
		return map[string]bool{}, nil
	})
	sends := 0
	h.f.answerApp(AppInbox, assistantwire.InboxReply, func(json.RawMessage) (any, error) { sends++; return assistantwire.InboxSent{}, nil })
	_, err := h.runTool(context.Background(), "server", "s1", "message_send",
		json.RawMessage(`{"channel":"larkcli","target":"oc_example_group","text":"Must not escape to an old transport.","idempotency_key":"old-one"}`))
	if err == nil || sends != 0 || len(st.audit) != 0 {
		t.Fatalf("old transport was used: sends=%d err=%v", sends, err)
	}
}

func TestOutboundSettingsRequireAFullOwnerDevice(t *testing.T) {
	h, _, _, _ := outboundTestHub(t)
	for _, role := range []string{"answer", "extension"} {
		for _, method := range []string{"GET", "POST"} {
			request := httptest.NewRequest(method, "/api/v1/assistant/outbound-messages", nil)
			request.Header.Set(RoleHeader, role)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != 403 {
				t.Fatalf("%s %s: %d %s", role, method, response.Code, response.Body)
			}
		}
	}
}

func TestOutboundTransportRechecksTheActualScope(t *testing.T) {
	for _, change := range []string{"recipient", "identity", "revocation", "invalid recipient"} {
		t.Run(change, func(t *testing.T) {
			h, st, c, _ := outboundTestHub(t)
			g := messageTestGrant("inbox_reply")
			target := "oc_example_group"
			switch change {
			case "recipient":
				target = "oc_different_group"
			case "identity":
				_ = st.PutSetting(context.Background(), outboundIdentityKey, "user")
			case "invalid recipient":
				g.Messages.Target, target = "", "unexpected"
			}
			list := StandingAuthorizations{Revision: 1, Authorizations: []StandingAuthorization{g}}
			if change == "revocation" {
				list.Authorizations = nil
			}
			_ = st.PutSetting(context.Background(), standingAuthorizationsKey, list)
			ctx := context.WithValue(context.Background(), outboundApprovalKey{}, outboundApproval{
				ID: g.ID, Kind: "standing_authorization", Scenario: "inbox_reply", Identity: "bot",
			})
			_, err := h.connectorReply(ctx, "larkcli", connectorMessage(target, "Do not send outside the checked scope."), "scope-race")
			if err == nil || len(c.identities) != 0 || len(st.audit) != 0 {
				t.Fatalf("changed %s escaped the final scope check: sends=%v audit=%+v err=%v", change, c.identities, st.audit, err)
			}
		})
	}
}

func TestOutboundStandingSettingKeepsScopeAndProposalGuard(t *testing.T) {
	h, st, _, _ := outboundTestHub(t)
	ctx := context.Background()
	grant := messageTestGrant("inbox_reply")
	grant.ID = ""
	change, err := h.setStandingAuthorizations(ctx, standingJSON(t, StandingAuthorizations{
		Authorizations: []StandingAuthorization{grant},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !change.applyChecksProposal {
		t.Fatal("standing settings must keep the original approved revision")
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		if !strings.HasPrefix(change.card(lang), messageScopeText(lang, *grant.Messages)+"\n\n") {
			t.Fatalf("%s card lost its explicit message scope: %s", lang, change.card(lang))
		}
	}
	if err := st.PutSetting(ctx, standingAuthorizationsKey, StandingAuthorizations{
		Revision: 1, Authorizations: []StandingAuthorization{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := change.apply(ctx); !errors.Is(err, errStandingConflict) {
		t.Fatalf("stale scope approval: %v", err)
	}
	list, err := h.standingAuthorizations(ctx)
	if err != nil || list.Revision != 1 || len(list.Authorizations) != 0 {
		t.Fatalf("stale approval changed authorizations: %+v, %v", list, err)
	}
}

func TestOutboundManualMessageNeedsItsOwnPermission(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	c.beforeReply = func() {
		if h.standingMu.TryLock() {
			h.standingMu.Unlock()
			t.Error("manual send allowed an identity change during transport")
		}
	}
	_ = st.PutSetting(context.Background(), outboundIdentityKey, "user")
	done := make(chan error, 1)
	go func() {
		_, err := h.runTool(context.Background(), "server", "s1", "message_send", json.RawMessage(`{"channel":"larkcli","target":"oc_example_group","text":"Owner-approved example.","idempotency_key":"manual-one"}`))
		done <- err
	}()
	card, ok := awaitCard(h.Service)
	if !ok || card.ToolName != "message_send" || !strings.Contains(card.Summary, "oc_example_group") || !strings.Contains(card.Summary, "Owner-approved example.") {
		t.Fatalf("missing precise manual card: %+v", card)
	}
	if len(c.got) != 0 {
		t.Fatal("sent before owner approval")
	}
	yes := true
	_, err := h.Action(context.Background(), Action{Op: "answer", InteractionID: card.ID, Answer: standingJSON(t, proto.Resolution{Allow: &yes})})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual send did not finish")
	}
	if len(c.got) != 1 || c.identities[0] != "user" || c.got[0].Thread != "larkcli:chat:oc_example_group" ||
		c.got[0].Text != "Owner-approved example." {
		t.Fatalf("selected sender ignored: %+v %v", c.got, c.identities)
	}
}

func TestOutboundPendingCardRejectsChangedIdentity(t *testing.T) {
	for _, before := range []string{"bot", "user"} {
		t.Run(before, func(t *testing.T) {
			h, st, c, _ := outboundTestHub(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := st.PutSetting(ctx, outboundIdentityKey, before); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := h.toolMessageSend(ctx, "server", "s1", json.RawMessage(
					`{"channel":"larkcli","target":"oc_example_group","text":"Only this approved sender.","idempotency_key":"pending-sender"}`))
				done <- err
			}()
			card, ok := awaitCard(h.Service)
			if !ok || !strings.Contains(card.Summary, "Only this approved sender.") ||
				!strings.Contains(card.Summary, "oc_example_group") {
				t.Fatalf("missing bound approval card: %+v", card)
			}
			after := "user"
			if before == after {
				after = "bot"
			}
			change, err := h.setOutboundIdentity(ctx, standingJSON(t, after))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := change.apply(ctx); err != nil {
				t.Fatal(err)
			}
			yes := true
			if _, err := h.Action(ctx, Action{Op: "answer", InteractionID: card.ID,
				Answer: standingJSON(t, proto.Resolution{Allow: &yes})}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var message *i18n.Message
				if !errors.As(err, &message) || message.Key != "assistant-outbound.scope_changed" {
					t.Fatalf("stale sender approval was not refused: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stale approval did not settle")
			}
			if len(c.identities) != 0 || len(st.audit) != 0 {
				t.Fatalf("stale approval reached transport: senders=%v audit=%+v", c.identities, st.audit)
			}
		})
	}
}

func TestOutboundQueuedMessageReadsTheNewDefaultAfterIdentityChange(t *testing.T) {
	h, st, c, _ := outboundTestHub(t)
	putMessageGrant(t, st, "assistant_message")
	st.identityRead = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.standingMu.Lock()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := h.runTool(ctx, "server", "s1", "message_send",
			json.RawMessage(`{"channel":"larkcli","target":"oc_example_group","text":"Read the new sender.","idempotency_key":"identity-race"}`))
		done <- err
	}()
	<-started
	readBeforeChange := false
	select {
	case <-st.identityRead:
		readBeforeChange = true
	case <-time.After(100 * time.Millisecond):
	}
	_ = st.PutSetting(context.Background(), outboundIdentityKey, "user")
	h.standingMu.Unlock()
	if readBeforeChange {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("queued automatic send read the old identity outside the grant/change lock")
	}
	card, ok := awaitCard(h.Service)
	if !ok || len(c.got) != 0 {
		t.Fatal("old bot grant covered the newly selected user identity")
	}
	no := false
	if _, err := h.Action(context.Background(), Action{Op: "answer", InteractionID: card.ID,
		Answer: standingJSON(t, proto.Resolution{Allow: &no})}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("declined message reported sent")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("declined identity card did not settle")
	}
}

func TestOutboundRegistryAndLedgerPersistInDatabase(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	f := newFakeHub().withMachine()
	f.answerApp(AppInbox, assistantwire.InboxMessagePolicy, func(json.RawMessage) (any, error) { return map[string]bool{"sender_identity": true}, nil })
	sends := 0
	f.answerApp(AppInbox, assistantwire.InboxReply, func(raw json.RawMessage) (any, error) {
		if !strings.Contains(string(raw), `"sender_identity":"bot"`) {
			t.Errorf("sender override missing: %s", raw)
		}
		sends++
		return assistantwire.InboxSent{}, nil
	})
	s := newServiceOn(t, f, database)
	putMessageGrant(t, s.store(), "assistant_message")
	text := "A durable synthetic example."
	payload := standingJSON(t, map[string]string{
		"channel": "larkcli", "target": "oc_example_group", "text": text, "idempotency_key": "durable-one",
	})
	if _, err := s.toolMessageSend(ctx, "server", "s1", payload); err != nil {
		t.Fatal(err)
	}
	rows, _, err := database.SentAsUsers(ctx, 0, 10, "larkcli")
	if err != nil || sends != 1 || len(rows) != 1 || rows[0].Text != text || !strings.Contains(string(rows[0].LetThrough), "sa_messages") ||
		!strings.Contains(string(rows[0].LetThrough), `"outcome": "sent"`) {
		t.Fatalf("durable ledger: %+v sends=%d err=%v", rows, sends, err)
	}
	restarted := newServiceOn(t, f, database)
	if _, err := restarted.toolMessageSend(ctx, "server", "s1", payload); err == nil || sends != 1 {
		t.Fatalf("reconstruction resent a delivered intent: sends=%d err=%v", sends, err)
	}
	if g, err := restarted.messageGrant(ctx, "larkcli", "oc_example_group", "assistant_message", "bot"); err != nil || g == nil {
		t.Fatalf("restart lost grant: %+v %v", g, err)
	}
	if response := serve(restarted, "DELETE", "/api/v1/assistant/standing-authorizations/sa_messages", ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if g, err := s.messageGrant(ctx, "larkcli", "oc_example_group", "assistant_message", "bot"); err != nil || g != nil {
		t.Fatalf("revoked grant cached: %+v %v", g, err)
	}
}
