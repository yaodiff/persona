package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

const projectReviewNoChange = "review_receipt_no_change"

func TestProjectReviewAuthenticatedAppIngress(t *testing.T) {
	h, server, store := newBusHub(t)
	manager, _ := managerDBOf(t, h)
	ctx := context.Background()
	config := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":true,"model":"gpt-6.2-sol",
		"target":{"machine_id":"box","session_id":"review"},
		"app_inputs":true,"projects":["project-one"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	start := func() *testAssistant {
		return dialAssistantWithStore(t, h, server.URL, false, manager, func(o *assistant.BusOptions) {
			o.ConfigFile, o.Outbox = config, manager
		})
	}
	a := start()
	if !h.managerCan(assistantwire.AssistantID, assistantwire.CapabilityProjectReview) {
		t.Fatal("project review capability missing")
	}
	const token = "isolated-project-review-device-token"
	if err := store.CreateDeviceToken(ctx, db.DeviceToken{ID: "review-device", Name: "Test app", Role: roleFull}, hashToken(token)); err != nil {
		t.Fatal(err)
	}
	pipeNode(t, h, "box", func(context.Context) error { return nil })
	post := func(id, auth, path string, extra ...string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path,
			strings.NewReader(`{"text":"private words","human":true,"submission":{"kind":"human_input"},`+strings.Join(extra, "")+`"client_msg_id":"`+id+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+auth)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("app send %s: %s", id, res.Status)
		}
	}
	post("owner-message", token, sendPath)
	post("owner-message", token, sendPath)
	post("forged-owner", h.cfg.Token, sendPath)
	post("unbound", token, "/api/v1/sessions/box/other/messages")
	// Siri, Shortcuts and the share sheet: delivered, but never the owner's own input (a mark of any kind counts).
	post("shared", token, sendPath, `"via":"share",`)
	post("intent-object", token, sendPath, `"via":{"kind":"intent"},`)
	// An answer-only device (the watch) is never a trusted human submission: the answer-only path rebuilds the
	// request without its via mark, and neither form of it is owner input.
	const answerToken = "isolated-project-review-answer-token"
	if err := store.CreateDeviceToken(ctx, db.DeviceToken{ID: "review-watch", Name: "Test watch", Role: roleAnswer}, hashToken(answerToken)); err != nil {
		t.Fatal(err)
	}
	h.byID["box"].setSessions([]proto.Session{{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle}}) // answer-only sends need a listed session
	post("answer-only", answerToken, sendPath)
	post("answer-only-shared", answerToken, sendPath, `"via":"share",`)

	pending, err := store.PendingProjectReviewOwners(ctx)
	if err != nil || len(pending) != 2 {
		t.Fatalf("trusted app inputs before routing: %+v %v", pending, err)
	}
	a.stop()
	<-a.done
	post("owner-while-manager-offline", token, sendPath)
	if err := h.deliverProjectReviewOwners(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingProjectReviewOwners(ctx)
	if err != nil || len(pending) != 3 {
		t.Fatalf("lost manager notices on disconnect: %+v %v", pending, err)
	}
	b := start()
	if err := h.deliverProjectReviewOwners(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate the hub replaying after a lost receipt. The manager already committed this ID.
	if err := store.SettleProjectReviewOwner(ctx, db.ProjectReviewOwnerRelay{
		MessageID: "owner-message", Manager: assistantwire.AssistantID, Machine: "box", Session: "s1", SendOutcome: sendDelivered,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverProjectReviewOwners(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingProjectReviewOwners(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("unacknowledged manager notices: %+v %v", pending, err)
	}

	const target = `{"machine_id":"box","session_id":"review"}`
	next, ok, err := manager.NextProjectReview(ctx, target)
	if err != nil || !ok {
		t.Fatalf("missing durable manager event: %+v %v", next, err)
	}
	var event map[string]any
	if err := json.Unmarshal(next.Event, &event); err != nil || event["kind"] != "owner_input" ||
		event["project"] != "" || event["reference"] != "owner-message" ||
		strings.Contains(string(next.Event), "private words") {
		t.Fatalf("wrong event: %s %v", next.Event, err)
	}
	if err := manager.SettleProjectReview(ctx, next.ID, projectReviewNoChange); err != nil {
		t.Fatal(err)
	}
	unbound, ok, err := manager.NextProjectReview(ctx, target)
	if err != nil || !ok || !strings.Contains(string(unbound.Event), "unbound") ||
		!strings.Contains(string(unbound.Event), `"project": ""`) {
		t.Fatalf("unbound input inferred a project: %s %v %v", unbound.Event, ok, err)
	}
	if err := manager.SettleProjectReview(ctx, unbound.ID, projectReviewNoChange); err != nil {
		t.Fatal(err)
	}
	offline, ok, err := manager.NextProjectReview(ctx, target)
	if err != nil || !ok || !strings.Contains(string(offline.Event), "owner-while-manager-offline") {
		t.Fatalf("missing offline input %s: %v %v", offline.Event, ok, err)
	}
	if err := manager.SettleProjectReview(ctx, offline.ID, projectReviewNoChange); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := manager.NextProjectReview(ctx, target); err != nil || ok {
		t.Fatalf("unauthorized or duplicate input made it to the queue: %v %v", ok, err)
	}
	b.stop()
	<-b.done
	post("queued-before-disable", token, sendPath)
	if pending, err = store.PendingProjectReviewOwners(ctx); err != nil || len(pending) != 1 {
		t.Fatalf("offline input before disabling: %+v %v", pending, err)
	}
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	start()
	post("while-disabled", token, sendPath)
	pending, err = store.PendingProjectReviewOwners(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("disabled route kept admitting events or kept a backlog to replay: %+v %v", pending, err)
	}
	t.Log("authenticated app -> Hub Postgres -> reconnect -> manager Postgres: online and offline references, zero raw words; disable stopped intake")
}

// relayDownStore fails the combined receipt-and-notice write, as a transient database error would.
type relayDownStore struct{ *db.DB }

func (relayDownStore) SettleProjectReviewOwner(context.Context, db.ProjectReviewOwnerRelay) error {
	return fmt.Errorf("relay write failed")
}

// A failed review notice never costs the send its receipt: the same id retried is answered as delivered, not
// "unknown", and nothing is typed twice.
func TestProjectReviewRelayFailureKeepsSendReceipt(t *testing.T) {
	h, server, store := newBusHub(t)
	manager, _ := managerDBOf(t, h)
	ctx := context.Background()
	config := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":true,"model":"gpt-6.2-sol",
		"target":{"machine_id":"box","session_id":"review"},"app_inputs":true,"projects":["project-one"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dialAssistantWithStore(t, h, server.URL, false, manager, func(o *assistant.BusOptions) { o.ConfigFile, o.Outbox = config, manager })
	h.store = relayDownStore{store}
	const token = "relay-down-device-token"
	if err := store.CreateDeviceToken(ctx, db.DeviceToken{ID: "relay-down-device", Name: "Test app", Role: roleFull}, hashToken(token)); err != nil {
		t.Fatal(err)
	}
	var typed atomic.Int32
	pipeNode(t, h, "box", func(context.Context) error { typed.Add(1); return nil })
	for try := range 2 {
		req, err := http.NewRequest(http.MethodPost, server.URL+sendPath,
			strings.NewReader(`{"text":"private words","human":true,"submission":{"kind":"human_input"},"client_msg_id":"relay-down"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("send %d: %s %s", try, res.Status, body)
		}
	}
	if n := typed.Load(); n != 1 {
		t.Fatalf("typed %d times, want 1", n)
	}
	if state, err := store.ClaimSend(ctx, "relay-down", "box", "s1"); err != nil || state != sendDelivered {
		t.Fatalf("receipt after a failed relay write: %q %v", state, err)
	}
}

// appInputsDownStore fails to save the manager's App-input switch, as a database blip during a hello would.
type appInputsDownStore struct{ *db.DB }

func (appInputsDownStore) SetProjectReviewAppInputs(context.Context, string, bool) error {
	return fmt.Errorf("database unavailable")
}

// The App-input switch is optional: failing to save it does not cost the manager its connection.
func TestProjectReviewAppInputsWriteFailureKeepsManager(t *testing.T) {
	h, server, store := newBusHub(t)
	manager, _ := managerDBOf(t, h)
	config := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":true,"model":"gpt-6.2-sol",
		"target":{"machine_id":"box","session_id":"review"},"app_inputs":true,"projects":["project-one"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	h.store = appInputsDownStore{store}
	dialAssistantWithStore(t, h, server.URL, false, manager, func(o *assistant.BusOptions) { o.ConfigFile, o.Outbox = config, manager })
	if !h.managerCan(assistantwire.AssistantID, assistantwire.CapabilityProjectReview) {
		t.Fatal("the manager was taken without its capabilities")
	}
}

func TestProjectReviewMainAndFirstAppInput(t *testing.T) {
	ctx := context.Background()
	h, server, hubDB := newBusHub(t)
	manager, _ := managerDBOf(t, h)
	node := newAssistantNode(t, h)
	node.m.info.Home = "/home/me"
	node.m.online = true
	if err := manager.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "server"}); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":true,"app_inputs":true,"model":"gpt-6.2-sol",
		"target":{"machine_id":"server","session_id":"review"},"projects":["alpha","beta"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := dialAssistantWithStore(t, h, server.URL, false, manager, func(o *assistant.BusOptions) {
		o.ConfigFile, o.Outbox = config, manager
	})
	var starts atomic.Int32
	startReceiptNode(t, h, func(context.Context, json.RawMessage) (any, error) {
		id := "app-started-agent"
		if n := starts.Add(1); n > 1 {
			id = fmt.Sprintf("app-started-agent-%d", n)
		}
		return map[string]any{"session": proto.Session{SessionID: id, Agent: "codex"}}, nil
	})
	const token = "isolated-main-owner-device-token"
	if err := hubDB.CreateDeviceToken(ctx, db.DeviceToken{ID: "project-review-main-device", Name: "Test app", Role: roleFull}, hashToken(token)); err != nil {
		t.Fatal(err)
	}
	post := func(path, auth, op, body string) []byte {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+auth)
		req.Header.Set("X-Persona-Op-Id", op)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("app request %s: %d %s %v", path, resp.StatusCode, out, err)
		}
		return out
	}

	const chatPath = "/api/v1/assistant/conversations"
	const chatOp = "10000000-0100-4000-8000-000000000001"
	var first struct{ Session proto.Session }
	if err := json.Unmarshal(post(chatPath, token, chatOp, `{"prompt":"private original","main":true}`), &first); err != nil || first.Session.SessionID == "" {
		t.Fatalf("did not start main: %+v %v", first, err)
	}
	if err := json.Unmarshal(post(chatPath, token, chatOp, `{"prompt":"private original","main":true}`), &first); err != nil {
		t.Fatal(err)
	}
	main := first.Session.SessionID
	editSessions(node.m, func([]proto.Session) []proto.Session { return []proto.Session{first.Session} })

	const nextOp = "10000000-0100-4000-8000-000000000002"
	mainPath := "/api/v1/sessions/server/" + main + "/messages"
	for range 2 {
		post(mainPath, token, nextOp, `{"text":"another private original"}`)
	}
	const newChatOp = "10000000-0100-4000-8000-000000000007"
	var separate struct{ Session proto.Session }
	for range 2 {
		if err := json.Unmarshal(post(chatPath, token, newChatOp, `{"prompt":"new conversation private original"}`), &separate); err != nil ||
			separate.Session.SessionID == "" || separate.Session.SessionID == main {
			t.Fatalf("did not start a new assistant conversation: %+v %v", separate, err)
		}
	}
	// A token with no paired device and body-supplied roles cannot originate owner input.
	post(mainPath, h.cfg.Token, "10000000-0100-4000-8000-000000000003", `{"text":"forward","human":true}`)
	post(chatPath, token, "10000000-0100-4000-8000-000000000004", `{"prompt":"app forwarded words","via":"share"}`)
	if _, err := a.svc.Start(ctx, assistantwire.StartParams{Text: "scheduled internal start", Origin: "schedule"}); err != nil {
		t.Fatal(err)
	}
	post("/api/v1/sessions/server/review/messages", token, "10000000-0100-4000-8000-000000000005", `{"text":"management output"}`)
	for range 2 {
		post("/api/v1/sessions", token, "10000000-0100-4000-8000-000000000006",
			`{"machine_id":"machine","agent_type":"codex","launcher":"codex","cwd":"/code","tab_name":"Task","prompt":"start private work"}`)
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("new Agent start repeated %d times", n)
	}
	// A screenshot with no words is the owner's first input too.
	post("/api/v1/sessions", token, "10000000-0100-4000-8000-000000000008",
		`{"machine_id":"machine","agent_type":"codex","launcher":"codex","cwd":"/code","tab_name":"Shot","images":[{"mime":"image/png","data":"iVBORw0KGgo="}]}`)
	// A start the app marks "via" (a share, an intent) is delivered, but it is not the owner's input.
	post("/api/v1/sessions", token, "10000000-0100-4000-8000-00000000a009",
		`{"machine_id":"machine","agent_type":"codex","launcher":"codex","cwd":"/code","tab_name":"Shared","prompt":"shared private words","via":"share"}`)
	if n := starts.Load(); n != 3 {
		t.Fatalf("the marked start reached the node %d times in all, want 3 starts", n)
	}
	if err := h.deliverProjectReviewOwners(ctx); err != nil {
		t.Fatal(err)
	}
	const target = `{"machine_id":"server","session_id":"review"}`
	want := map[string]bool{"first-assistant:" + main: true, "first-assistant:" + separate.Session.SessionID: true,
		nextOp: true, "first-agent:app-started-agent": true, "first-agent:app-started-agent-2": true}
	for len(want) > 0 {
		event, ok, err := manager.NextProjectReview(ctx, target)
		var captured struct{ Reference, Project string }
		if err != nil || !ok || json.Unmarshal(event.Event, &captured) != nil || !want[captured.Reference] ||
			captured.Project != "" || strings.Contains(string(event.Event), "private") {
			t.Fatalf("want one of %v: got %s (%v, %v)", want, event.Event, ok, err)
		}
		delete(want, captured.Reference)
		if err := manager.SettleProjectReview(ctx, event.ID, projectReviewNoChange); err != nil {
			t.Fatal(err)
		}
	}
	if event, ok, err := manager.NextProjectReview(ctx, target); err != nil || ok {
		t.Fatalf("automation, forward, retry, or management source loop: %s %v %v", event.Event, ok, err)
	}
	t.Log("authenticated new main and separate Assistant chats, main continuation and first Agent input once each; retries, app shares (a message and a start), internal start and review session excluded")
}

func TestProjectReviewReceiptThroughBus(t *testing.T) {
	ctx := context.Background()
	h, server, hubDB := newBusHub(t)
	manager, _ := managerDBOf(t, h)
	node := newAssistantNode(t, h)
	node.m.online = true
	editSessions(node.m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "review", Agent: "codex", Model: "gpt-6.2-sol", Status: proto.StatusIdle},
			{SessionID: "work", Agent: "codex", Model: "gpt-6.2-sol", Status: proto.StatusIdle}}
	})
	config := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":true,"app_inputs":true,"model":"gpt-6.2-sol",
		"target":{"machine_id":"server","session_id":"review"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	start := func() *testAssistant {
		return dialAssistantWithStore(t, h, server.URL, true, manager, func(o *assistant.BusOptions) {
			o.ConfigFile, o.Outbox = config, manager
		})
	}
	a := start()
	const token = "isolated-project-review-receipt-token"
	if err := hubDB.CreateDeviceToken(ctx, db.DeviceToken{ID: "review-receipt-device", Role: roleFull}, hashToken(token)); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sessions/server/work/messages",
		strings.NewReader(`{"text":"private original","client_msg_id":"bus-input"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("App input: %s", res.Status)
	}
	if err := h.deliverProjectReviewOwners(ctx); err != nil {
		t.Fatal(err)
	}
	const target = `{"machine_id":"server","session_id":"review"}`
	var delivery db.ProjectReviewDelivery
	eventually(t, 8*time.Second, "real bus send did not leave a persistent pending receipt", func() bool {
		var ok bool
		delivery, ok, err = manager.NextProjectReview(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		return ok && delivery.HistoryAfter != nil && delivery.Attempts > 0 && node.sent() >= 2
	})
	a.stop()
	<-a.done
	node.mu.Lock()
	node.items = []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: delivery.ID},
		{Seq: 2, Role: "assistant", Kind: proto.KindText, Model: "gpt-6.2-sol", Text: "PROJECT_REVIEW_RECEIPT " + delivery.ID + " triage_only"},
		{Seq: 3, Kind: "turn_duration"},
		{Seq: 4, Role: "user", Kind: proto.KindText, ClientMsgID: "later-bus-input"},
		{Seq: 5, Role: "assistant", Kind: proto.KindText, Model: "other-model", Text: "later response"},
		{Seq: 6, Kind: "turn_duration"},
	}
	node.mu.Unlock()
	start()
	eventually(t, 8*time.Second, "manager restart did not consume the original receipt", func() bool {
		_, ok, err := manager.NextProjectReview(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		return !ok
	})
	node.mu.Lock()
	defer node.mu.Unlock()
	count := 0
	for _, sent := range node.sends {
		if sent["session_id"] == "review" && sent["client_msg_id"] == delivery.ID {
			count++
			if msg, _ := sent["text"].(string); strings.Contains(msg, "private original") || !strings.Contains(msg, delivery.ID+" triage_only") {
				t.Fatalf("unexpected review payload: %s", msg)
			}
		}
	}
	if count != 1 {
		t.Fatalf("review was sent %d times, want 1", count)
	}
	t.Log("App → Hub → Manager → Hub's guarded native send → isolated node transcript; previous receipt consumed after Manager restart and a newer turn; one send, zero pending")
}

// A start records its first input before the machine lists the new session; the session the node returned still
// keeps space and meeting conversations out, and an untrusted request never counts.
func TestProjectReviewFirstInputBeforeListing(t *testing.T) {
	h := newTestHub(t)
	registerMachine(h, newMachine(h, MachineConfig{ID: "machine"}))
	r := httptest.NewRequest(http.MethodPost, "https://example.com/api/v1/sessions", nil)
	trusted := r.WithContext(context.WithValue(r.Context(), trustedSubmissionKey{}, devicewire.Submission{Kind: "human_input"}))
	cases := []struct {
		name     string
		req      *http.Request
		started  proto.Session
		indirect bool
		want     bool
	}{
		{"plain Agent", trusted, proto.Session{SessionID: "plain", Cwd: "/code"}, false, true},
		{"space", trusted, proto.Session{SessionID: "space", Cwd: "/code", Space: "health"}, false, false},
		{"meeting", trusted, proto.Session{SessionID: "meeting", Cwd: "/code", Origin: "meeting"}, false, false},
		// Before the computer lists a start with the metadata that says so, where it runs decides.
		{"health folder", trusted, proto.Session{SessionID: "health-cwd", Cwd: "/home/u/.persona/health/reports"}, false, false},
		{"meeting room", trusted, proto.Session{SessionID: "room", Cwd: "/home/u/.persona/guest-meeting/m1"}, false, false},
		{"guest", trusted, proto.Session{SessionID: "guest", Cwd: "/home/u/.persona/guest"}, false, false},
		{"a project folder named guest", trusted, proto.Session{SessionID: "guest-project", Cwd: "/code/guest"}, false, true},
		{"untrusted", r, proto.Session{SessionID: "untrusted", Cwd: "/code"}, false, false},
		{"share sheet", trusted, proto.Session{SessionID: "shared", Cwd: "/code"}, true, false},
	}
	for _, c := range cases {
		started := c.started
		p, ok := h.projectReviewAppSource(c.req, "machine", started.SessionID, "first-agent:"+started.SessionID, &started, c.indirect)
		if ok != c.want || ok && (p.Unscoped || p.Machine != "machine" || p.Session != started.SessionID) {
			t.Errorf("%s: got %+v %v, want eligible=%v", c.name, p, ok, c.want)
		}
	}
	// Another session's returned row is never used for this one.
	other := proto.Session{SessionID: "other", Space: "health"}
	if _, ok := h.projectReviewAppSource(trusted, "machine", "plain", "first-agent:plain", &other, false); !ok {
		t.Error("a mismatched started session changed eligibility")
	}
	for via, want := range map[any]bool{nil: false, "": false, "share": true, "intent": true, false: true, 0.0: true} {
		if got := viaMarked(via); got != want {
			t.Errorf("viaMarked(%#v)=%v, want %v", via, got, want)
		}
	}
}

// linkOrderStore notes, as the hub queues each first Agent input, whether the agent is already linked to its to-do.
type linkOrderStore struct {
	*db.DB
	linked func(session string) bool
	seen   map[string]bool
}

func (s linkOrderStore) RecordProjectReviewOwnerInput(ctx context.Context, p db.ProjectReviewOwnerRelay) error {
	before, again := s.seen[p.MessageID]
	s.seen[p.MessageID] = s.linked(p.Session) && (!again || before) // one write before the link is enough
	return s.DB.RecordProjectReviewOwnerInput(ctx, p)
}

// A start that names a to-do queues its first input only once the manager has linked the agent: the relay ticks every
// 3 s, and an input it delivers before the link would find no task (assistant/project_review.go).
func TestProjectReviewFirstInputAfterTodoLink(t *testing.T) {
	ctx := context.Background()
	h, server, hubDB := newBusHub(t)
	manager, _ := managerDBOf(t, h)
	config := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":true,"app_inputs":true,"model":"gpt-6.2-sol",
		"target":{"machine_id":"server","session_id":"review"},"projects":["alpha"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dialAssistantWithStore(t, h, server.URL, false, manager, func(o *assistant.BusOptions) { o.ConfigFile, o.Outbox = config, manager })
	startReceiptNode(t, h, func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"session": proto.Session{SessionID: "linked-agent", Agent: "codex"}}, nil
	})
	todo := plainTodo(t, h, "Linked work")
	seen := map[string]bool{}
	h.store = linkOrderStore{DB: hubDB, seen: seen, linked: func(session string) bool {
		rows, err := manager.ListAllTodoAgents(ctx)
		if err != nil {
			t.Error(err)
		}
		for _, r := range rows {
			if r.TodoID == todo.ID && r.SessionID == session && r.ReleasedAt == nil {
				return true
			}
		}
		return false
	}}
	const token = "first-input-after-link-device-token"
	if err := hubDB.CreateDeviceToken(ctx, db.DeviceToken{ID: "first-input-after-link-device", Name: "Test app", Role: roleFull}, hashToken(token)); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sessions", strings.NewReader(
		`{"machine_id":"machine","agent_type":"codex","launcher":"codex","cwd":"/code","tab_name":"Task","prompt":"start private work","todo_id":"`+todo.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Persona-Op-Id", "10000000-0100-4000-8000-000000000009")
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), todo.ID) {
		t.Fatalf("start with a to-do: %d %s", resp.StatusCode, body)
	}
	linked, queued := seen["first-agent:linked-agent"]
	if !queued || !linked {
		t.Fatalf("first input queued %v, with the to-do linked %v (%v)", queued, linked, seen)
	}
}

// appPost sends text to path as the App would; extra is more JSON fields, each with its trailing comma.
func appPost(t *testing.T, server *httptest.Server, id, auth, path string, extra ...string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+path,
		strings.NewReader(`{"text":"private words","human":true,"submission":{"kind":"human_input"},`+strings.Join(extra, "")+`"client_msg_id":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("Content-Type", "application/json")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app send %s: %s", id, res.Status)
	}
}

// reviewManager connects the default manager with project review and App inputs on; deviceToken is a full device's.
func reviewManager(t *testing.T) (h *Hub, server *httptest.Server, store *db.DB, start func() *testAssistant, deviceToken string) {
	h, server, store = newBusHub(t)
	manager, _ := managerDBOf(t, h)
	config := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(config, []byte(`{"project_review":{"enabled":true,"model":"gpt-6.2-sol",
		"target":{"machine_id":"box","session_id":"review"},"app_inputs":true,"projects":["project-one"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	start = func() *testAssistant {
		return dialAssistantWithStore(t, h, server.URL, false, manager, func(o *assistant.BusOptions) { o.ConfigFile, o.Outbox = config, manager })
	}
	deviceToken = "review-manager-device-token-" + t.Name() // the test process shares one database
	if err := store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: "review-manager-" + t.Name(), Name: "Test app", Role: roleFull},
		hashToken(deviceToken)); err != nil {
		t.Fatal(err)
	}
	pipeNode(t, h, "box", func(context.Context) error { return nil })
	return h, server, store, start, deviceToken
}

// pendingOwnerInputs counts the queued rows of the messages this test sent (appPost with a t.Name() prefix): the
// test process shares one database.
func pendingOwnerInputs(t *testing.T, store *db.DB) int {
	t.Helper()
	pending, err := store.PendingProjectReviewOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range pending {
		if strings.HasPrefix(p.MessageID, t.Name()+"/") {
			n++
		}
	}
	return n
}

// A manager rolled back to a version from before project review says hello with neither the switch nor the
// capability: what was queued for it is dropped and nothing more is queued, instead of rows it can never take
// piling up for good.
func TestProjectReviewRolledBackManagerDropsItsQueue(t *testing.T) {
	h, server, store, start, token := reviewManager(t)
	a := start()
	a.stop()
	<-a.done
	appPost(t, server, t.Name()+"/queued-while-away", token, sendPath)
	if n := pendingOwnerInputs(t, store); n != 1 {
		t.Fatalf("queued while the manager was away: %d, want 1", n)
	}
	old := dialRawBus(t, server, busToken(t, h, assistantwire.AssistantID, "assistant"))
	if err := old.call("manager.hello", map[string]any{"id": assistantwire.AssistantID,
		"capabilities": []string{assistantwire.CapabilityConfigOwn}}, nil); err != nil {
		t.Fatal(err)
	}
	if n := pendingOwnerInputs(t, store); n != 0 {
		t.Fatalf("a manager that cannot take them kept %d queued", n)
	}
	appPost(t, server, t.Name()+"/after-rollback", token, sendPath)
	if n := pendingOwnerInputs(t, store); n != 0 {
		t.Fatalf("queued %d for a manager that cannot take them", n)
	}
}

// relayReadsStore counts the relay's reads.
type relayReadsStore struct {
	*db.DB
	reads *atomic.Int32
}

func (s relayReadsStore) PendingProjectReviewOwnersOf(ctx context.Context, managers []string, limit int) ([]db.ProjectReviewOwnerRelay, error) {
	s.reads.Add(1)
	return s.DB.PendingProjectReviewOwnersOf(ctx, managers, limit)
}

func poked(h *Hub) bool {
	select {
	case <-h.projectReview.wakeup():
		return true
	default:
		return false
	}
}

// The relay reads its table only when there may be something to deliver: with no manager connected that takes the
// rows it reads nothing (project review is off by default), and a queued row or such a manager's hello wakes it,
// rather than a read every few seconds.
func TestProjectReviewRelayReadsOnlyWhenWoken(t *testing.T) {
	h, server, store, start, token := reviewManager(t)
	var reads atomic.Int32
	h.store = relayReadsStore{store, &reads}
	if retry, err := h.relayProjectReviewOwners(t.Context()); err != nil || retry != 0 || reads.Load() != 0 {
		t.Fatalf("with no manager to take them: retry %v err %v, %d reads", retry, err, reads.Load())
	}
	poked(h)
	a := start()
	if !poked(h) {
		t.Fatal("the manager's hello did not wake the relay")
	}
	appPost(t, server, t.Name()+"/owner-input", token, sendPath)
	if !poked(h) {
		t.Fatal("a queued row did not wake the relay")
	}
	if retry, err := h.relayProjectReviewOwners(t.Context()); err != nil || retry != 0 || reads.Load() != 1 {
		t.Fatalf("delivering: retry %v err %v, %d reads", retry, err, reads.Load())
	}
	if n := pendingOwnerInputs(t, store); n != 0 {
		t.Fatalf("%d left after delivery", n)
	}

	// End to end: a row queued while the manager is away is delivered once it is back, by its hello.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.projectReviewOwnerLoop(ctx)
	a.stop()
	<-a.done
	appPost(t, server, t.Name()+"/while-away", token, sendPath)
	start()
	eventually(t, 5*time.Second, "the row queued while away delivered", func() bool { return pendingOwnerInputs(t, store) == 0 })
}
