package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// originErrorStore makes the origin book unreadable while every other assistant setting remains available.
type originErrorStore struct{ Store }

func (s originErrorStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == originsKey {
		return false, fmt.Errorf("origin store unavailable")
	}
	return s.Store.Setting(ctx, key, out)
}

// policyExtensions is an extension registry whose read/write declarations and calls can be observed without any
// connector running. Embedding keeps this test double small while ToolAccess and CallTool are the policy boundary.
type policyExtensions struct {
	ExtensionTools
	read       map[string]bool
	connectors map[string]bool
	calls      []string
}

func (e *policyExtensions) ConnectorTool(name string) bool {
	if e.connectors != nil {
		return e.connectors[name]
	}
	_, known := e.read[name]
	return known
}

func (e *policyExtensions) ToolAccess(name string) (bool, bool) {
	read, known := e.read[name]
	return known, known && read
}

func (e *policyExtensions) CallTool(_ context.Context, _, _, name string, _ json.RawMessage, _ bool) (proto.ToolResult, bool) {
	if _, known := e.read[name]; !known {
		return proto.ToolResult{}, false
	}
	e.calls = append(e.calls, name)
	return proto.TextResult("read "+name, false), true
}

func inboxTool(t *testing.T, h *inboxHub, session, name, args string) proto.ToolResult {
	t.Helper()
	if args == "" {
		args = `{}`
	}
	return h.Tool(t.Context(), ToolCall{MachineID: "server", SessionID: session, Name: name, Arguments: json.RawMessage(args)})
}

func requireInboxRefusal(t *testing.T, res proto.ToolResult, name string) {
	t.Helper()
	text := toolText(res)
	if !res.IsError || !strings.Contains(text, "Use inbox_suggest") && !strings.Contains(text, "use inbox_suggest") {
		t.Fatalf("%s was not refused with an actionable suggestion path: %+v", name, res)
	}
}

// The model-facing entry points are default-deny for Inbox handling chats. Connector actions are classified by the
// extension's declared interface, not by a list of send-shaped names, so a future action cannot slip through.
func TestInboxModelOutboundToolsDenied(t *testing.T) {
	h, _, _ := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{
		"mail_send": false, "github_comment": false, "github_review": false,
		"lark_image": false, "lark_agent_reply": false, "example_future_write": false, "computer_run": false,
	}}
	h.SetExtensions(ext)
	h.originSet(t.Context(), "server", "s1", OriginInbox, "lark:f1:100", "")

	own := []string{
		"notify_user", "ask_user", "agent_start", "agent_send", "conversation_send",
		"conversation_new", "task_plan", "schedule_followup", "urgent_call", "model_probe", "launcher_import", "agent_modes",
	}
	for _, name := range own {
		t.Run(name, func(t *testing.T) {
			requireInboxRefusal(t, inboxTool(t, h, "s1", name, `{}`), name)
		})
	}
	extensionWrites := []string{
		"mail_send", "github_comment", "github_review", "lark_agent_reply", "lark_image",
		"example_future_write", "computer_run", "subscription_run", "future_unknown_tool",
	}
	for _, name := range extensionWrites {
		t.Run(name, func(t *testing.T) {
			res := h.mcpCallWithOrigin(t.Context(), "server", "s1", name, json.RawMessage(`{}`), true, OriginInbox)
			requireInboxRefusal(t, res, name)
		})
	}
	for _, name := range []string{"phone_notify", "phone_messages_compose", "phone_clipboard_set", "phone_shortcut_run"} {
		t.Run(name, func(t *testing.T) {
			requireInboxRefusal(t, inboxTool(t, h, "s1", name, `{}`), name)
		})
	}
	if len(ext.calls) != 0 {
		t.Fatalf("refused extension calls executed: %v", ext.calls)
	}
	h.f.mu.Lock()
	pushes, starts, agentSends, conversations := len(h.f.pushes), len(h.f.starts), len(h.f.agentSent), len(h.f.sends)
	h.f.mu.Unlock()
	if pushes != 0 || starts != 0 || agentSends != 0 || conversations != 0 {
		t.Fatalf("a refused call had a side effect: pushes=%d starts=%d agent sends=%d conversations=%d", pushes, starts, agentSends, conversations)
	}
}

// Read-only connector investigation, knowledge-base reads and suggestion submission remain available at the model
// entry point. The suggestion itself is stored; no reply is sent before the owner's action.
func TestInboxModelReadOnlyInvestigationAndSuggestion(t *testing.T) {
	h, lark, it := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{"lark_chats": true, "lark_image": true, "mail_search": true, "subscription_list": true}}
	h.SetExtensions(ext)
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")

	for _, name := range []string{"lark_chats", "lark_image", "mail_search", "subscription_list"} {
		res := h.mcpCallWithOrigin(t.Context(), "server", "s1", name, json.RawMessage(`{}`), true, OriginInbox)
		if res.IsError || toolText(res) != "read "+name {
			t.Fatalf("read-only %s: %+v", name, res)
		}
	}
	h.kb.dir = t.TempDir()
	t.Cleanup(h.kbEnd)
	h.f.hello.KB = &KBSettings{ReadOnly: true}
	if res := inboxTool(t, h, "s1", "kb_search", `{"query":"project context"}`); res.IsError {
		t.Fatalf("KB read through the model entry point: %+v", res)
	}
	h.f.answerApp(assistantwire.AppPhone, assistantwire.PhoneCall, func(json.RawMessage) (any, error) {
		return proto.TextResult(`{"events":[]}`, false), nil
	})
	if res := h.mcpCallWithOrigin(t.Context(), "server", "s1", "phone_calendar_events", json.RawMessage(`{}`), true, OriginInbox); res.IsError {
		t.Fatalf("phone read through the model entry point: %+v", res)
	}
	if calls := h.f.appParams(assistantwire.AppPhone, assistantwire.PhoneCall); len(calls) != 1 {
		t.Fatalf("phone read executed %d times, want 1", len(calls))
	}
	if _, refused := h.inboxModelRefusal(t.Context(), "server", "s1", OriginInbox, "agents_list"); refused {
		t.Fatal("read-only Agent investigation was refused")
	}
	turn := inboxTestProcessingTurn(h, "s1")
	args := fmt.Sprintf(`{"processing_turn":%q,"kind":"reply","text":"Suggested reply","recap":"Waiting for owner confirmation"}`, turn)
	if res := inboxTool(t, h, "s1", "inbox_suggest", args); res.IsError {
		t.Fatalf("inbox_suggest: %+v", res)
	}
	h.inbox.mu.Lock()
	suggestion := it.Suggestion
	h.inbox.mu.Unlock()
	if suggestion == nil || suggestion.Text != "Suggested reply" || len(lark.got) != 0 {
		t.Fatalf("suggestion=%+v, outbound replies=%v", suggestion, lark.got)
	}
}

func TestSourceOnlyInboxToolPolicy(t *testing.T) {
	h, _, it := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{"lark_messages": true, "lark_chats": true, "lark_search": true}}
	h.SetExtensions(ext)
	h.setConfig(func(c *assistantwire.InboxConfig) {
		c.Agent.ReadProfile = "source_only"
		c.Agent.ConnectorTools = []string{"lark_messages", "lark_search"}
	})
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	for _, name := range []string{
		"kb_search", "kb_get", "kb_update", "conversation_read", "agent_read", "agents_list",
		"lark_search", "lark_chats", "phone_calendar_events", "inbox_folders",
	} {
		res := h.mcpCallWithOrigin(t.Context(), "server", "s1", name, json.RawMessage(`{}`), true, OriginInbox)
		if !res.IsError {
			t.Errorf("source_only allowed %s: %+v", name, res)
		}
	}
	if len(ext.calls) != 0 {
		t.Fatalf("refused source_only connector calls executed: %v", ext.calls)
	}
	for _, name := range []string{"inbox_suggest", "inbox_draft"} {
		if _, refused := h.inboxModelRefusal(t.Context(), "server", "s1", OriginInbox, name); refused {
			t.Errorf("source_only refused the suggestion tool %s", name)
		}
	}
	res := h.mcpCallWithOrigin(t.Context(), "server", "s1", "lark_messages", json.RawMessage(`{"chat_id":"synthetic-group"}`), true, OriginInbox)
	if res.IsError || toolText(res) != "read lark_messages" || len(ext.calls) != 1 {
		t.Fatalf("allowlisted source read: %+v, calls=%v", res, ext.calls)
	}
}

func TestSourceOnlyInboxToolPolicyRejectsSubscriptionReads(t *testing.T) {
	h, _, it := handledHub(t)
	ext := &policyExtensions{
		read:       map[string]bool{"subscription_get": true},
		connectors: map[string]bool{"subscription_get": false},
	}
	h.SetExtensions(ext)
	h.setConfig(func(c *assistantwire.InboxConfig) {
		c.Agent.ReadProfile = "source_only"
		c.Agent.ConnectorTools = []string{"subscription_get"}
	})
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	res := h.mcpCallWithOrigin(t.Context(), "server", "s1", "subscription_get", json.RawMessage(`{}`), true, OriginInbox)
	if !res.IsError {
		t.Fatalf("source_only allowed a non-connector subscription read: %+v", res)
	}
	if len(ext.calls) != 0 {
		t.Fatalf("refused subscription read executed: %v", ext.calls)
	}
}

func TestUnknownInboxReadProfileFailsClosed(t *testing.T) {
	h, _, it := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{"lark_messages": true}}
	h.SetExtensions(ext)
	h.setConfig(func(c *assistantwire.InboxConfig) {
		c.Agent.ReadProfile = "misspelled"
		c.Agent.ConnectorTools = []string{"lark_messages"}
	})
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	for _, name := range []string{"lark_messages", "kb_search", "inbox_folders"} {
		if _, refused := h.inboxModelRefusal(t.Context(), "server", "s1", OriginInbox, name); !refused {
			t.Errorf("an unknown read profile allowed %s", name)
		}
	}
}

// A native child Agent shares its parent's assistant process and MCP identity. The manager therefore sees the same
// Inbox origin for the child's calls too; this is deliberately a separate acceptance case from the parent table.
func TestInboxNativeChildAgentCannotSend(t *testing.T) {
	h, _, _ := handledHub(t)
	ext := &policyExtensions{read: map[string]bool{"mail_send": false}}
	h.SetExtensions(ext)
	for _, name := range []string{"agent_send", "conversation_send", "mail_send"} {
		res := h.mcpCallWithOrigin(t.Context(), "server", "s1", name, json.RawMessage(`{}`), true, OriginInbox)
		requireInboxRefusal(t, res, name)
	}
	if len(ext.calls) != 0 {
		t.Fatalf("a child Agent executed connector writes: %v", ext.calls)
	}
}

// The initial node request attests origin before startOn can add the session to the manager's origin book. No prior
// originSet is performed here: an early tool call must still be denied.
func TestInboxOriginHintDeniesBeforeOriginBook(t *testing.T) {
	h := agentHub(t)
	h.f.setSessions("server", proto.Session{SessionID: "early-session", Status: proto.StatusWorking})
	for _, name := range []string{"notify_user", "agent_start", "conversation_send"} {
		res := h.mcpCallWithOrigin(t.Context(), "server", "early-session", name, json.RawMessage(`{}`), true, OriginInbox)
		requireInboxRefusal(t, res, name)
	}
}

// Main/user, scheduled, spawned and task-plan conversations retain the tools the Inbox policy removes. Each tool is
// actually executed against the fake hub; this is not merely a negative assertion on the Inbox refusal helper.
func TestOrdinaryOriginsKeepNotifyDelegationAndConversationSend(t *testing.T) {
	origins := []struct{ name, origin string }{
		{"main or user", OriginUser},
		{"schedule or channel", OriginChannel},
		{"spawned conversation", OriginSpawned},
		{"task plan", OriginTask},
	}
	for _, origin := range origins {
		t.Run(origin.name, func(t *testing.T) {
			h := agentHub(t)
			const caller, target = "caller", "target"
			h.f.setSessions("server",
				proto.Session{SessionID: caller, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"},
				proto.Session{SessionID: target, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"})
			h.originSet(t.Context(), "server", caller, origin.origin, "", "")
			if res := inboxTool(t, h, caller, "notify_user", `{"title":"Update","body":"Read-only work is done"}`); res.IsError {
				t.Fatalf("notify_user: %+v", res)
			}
			if res := inboxTool(t, h, caller, "agent_start", `{"machine":"server","cwd":"/work","prompt":"Investigate"}`); res.IsError {
				t.Fatalf("agent_start: %+v", res)
			}
			if res := inboxTool(t, h, caller, "conversation_send", `{"id":"target","text":"Investigation result"}`); res.IsError {
				t.Fatalf("conversation_send: %+v", res)
			}
			h.f.mu.Lock()
			pushes, starts := len(h.f.pushes), len(h.f.starts)
			h.f.mu.Unlock()
			if pushes != 1 || starts != 1 {
				t.Fatalf("ordinary origin %q: pushes=%d starts=%d", origin.origin, pushes, starts)
			}
		})
	}
}

// Inbox ignores a caller-supplied mode and an inherited Agents mode. Only the AI twin's own saved
// permission override changes its safe default; clearing that override restores default.
// The initial node request always carries origin, and ordinary starts retain their selected mode.
func TestInboxStartUsesOnlyItsOwnPermissionOverride(t *testing.T) {
	h := agentHub(t)
	start := func(p StartParams) (map[string]any, proto.Session) {
		t.Helper()
		sess, err := h.Start(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		h.f.mu.Lock()
		defer h.f.mu.Unlock()
		sent := h.f.sends[len(h.f.sends)-1]
		if sent["origin"] != p.Origin || sent["origin_known"] != true {
			t.Fatalf("start origin=%v known=%v, want %q attested", sent["origin"], sent["origin_known"], p.Origin)
		}
		if p.Origin == OriginInbox && sent["inbox_permission_mode"] != sess.PermissionMode {
			t.Fatalf("Inbox manager-only mode=%v, session mode=%q, sent=%v", sent["inbox_permission_mode"], sess.PermissionMode, sent)
		}
		if p.Origin != OriginInbox {
			if _, exists := sent["inbox_permission_mode"]; exists {
				t.Fatalf("ordinary start gained the Inbox-only mode: %v", sent)
			}
		}
		return sent, sess
	}

	// The parent's bypass and an explicit caller mode never elevate an unconfigured Inbox chat.
	setModel(t, h.Service, "agent.new", ModelBinding{Provider: "launcher.claude", PermissionMode: proto.ModeBypass})
	inboxStart, sess := start(StartParams{Text: "Investigate", Purpose: purposeAgent, Origin: OriginInbox, PermissionMode: proto.ModeBypass})
	if inboxStart["permission_mode"] != proto.ModeDefault || sess.PermissionMode != proto.ModeDefault {
		t.Fatalf("unconfigured Inbox start params=%v session mode=%q", inboxStart, sess.PermissionMode)
	}

	// A mode saved on inbox.agent itself is the owner's explicit choice and takes effect.
	setModel(t, h.Service, purposeAgent, ModelBinding{Provider: "launcher.claude", PermissionMode: proto.ModeBypass})
	inboxStart, sess = start(StartParams{Text: "Investigate with the saved mode", Purpose: purposeAgent, Origin: OriginInbox})
	if inboxStart["permission_mode"] != proto.ModeBypass || sess.PermissionMode != proto.ModeBypass {
		t.Fatalf("configured Inbox start params=%v session mode=%q", inboxStart, sess.PermissionMode)
	}

	// A legacy record can contain a mode this build does not know. The manager must normalize it before assistant.send;
	// otherwise the node's generic mode validation rejects it before the Inbox-specific default fallback can run.
	setModel(t, h.Service, purposeAgent, ModelBinding{Provider: "launcher.claude", PermissionMode: "future-mode"})
	inboxStart, sess = start(StartParams{Text: "Investigate with an unknown saved mode", Purpose: purposeAgent, Origin: OriginInbox})
	if inboxStart["permission_mode"] != proto.ModeDefault || sess.PermissionMode != proto.ModeDefault {
		t.Fatalf("unknown saved mode start params=%v session mode=%q, want default", inboxStart, sess.PermissionMode)
	}

	// Clearing the override restores default rather than inheriting Agents' bypass.
	setModel(t, h.Service, purposeAgent, ModelBinding{Provider: "launcher.claude"})
	inboxStart, sess = start(StartParams{Text: "Investigate after reset", Purpose: purposeAgent, Origin: OriginInbox})
	if inboxStart["permission_mode"] != proto.ModeDefault || sess.PermissionMode != proto.ModeDefault {
		t.Fatalf("reset Inbox start params=%v session mode=%q", inboxStart, sess.PermissionMode)
	}

	ordinaryStart, sess := start(StartParams{Text: "Ordinary", Origin: OriginUser, PermissionMode: proto.ModeBypass})
	if ordinaryStart["permission_mode"] != proto.ModeBypass || sess.PermissionMode != proto.ModeBypass {
		t.Fatalf("ordinary start params=%v session mode=%q", ordinaryStart, sess.PermissionMode)
	}
}

// Later sends re-attest the manager's persisted origin. This closes the node-restart window: its in-memory Origin
// field is not a database column, so the first resumed process must receive Inbox from the manager again. Caller input
// cannot substitute another origin.
func TestInboxLaterSendReattestsManagerOrigin(t *testing.T) {
	h := agentHub(t)
	const session = "inbox-after-node-restart"
	h.f.setSessions("server", proto.Session{SessionID: session, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"})
	h.originSet(t.Context(), "server", session, OriginInbox, "lark:real-item:1", "")
	setModel(t, h.Service, purposeAgent, ModelBinding{Provider: "launcher.claude", PermissionMode: proto.ModeBypass})
	params, err := h.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: session,
		Params: map[string]any{"text": "Continue", "origin": OriginUser, "origin_known": false, "permission_mode": proto.ModePlan, "inbox_permission_mode": proto.ModeDefault}})
	if err != nil {
		t.Fatal(err)
	}
	if params["origin"] != OriginInbox || params["origin_known"] != true {
		t.Fatalf("later send origin=%v known=%v, want the manager's explicit Inbox attestation", params["origin"], params["origin_known"])
	}
	if params["inbox_permission_mode"] != proto.ModeBypass {
		t.Fatalf("later send manager-only mode=%v, want the AI twin's own bypass; params=%v", params["inbox_permission_mode"], params)
	}
}

// The real continuation path is deliverNotes -> sendToSession -> session.send, not assistant.send's SendParams.
// Re-attest there too: the manager's origin book, not a caller-supplied field or the node's pre-reload memory, is authoritative.
func TestInboxDeliverNotesReattestsSessionSendOrigin(t *testing.T) {
	h, _, it := handledHub(t)
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	setModel(t, h.Service, purposeAgent, ModelBinding{Provider: "launcher.claude", PermissionMode: proto.ModeBypass})
	h.inbox.mu.Lock()
	h.inbox.tell(it, inboxNote{Name: prompts.NoteNewMessages, Text: "A new message"}, true, time.Now())
	h.inbox.mu.Unlock()
	h.deliverNotes(t.Context(), it)
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if len(h.f.agentSent) != 1 {
		t.Fatalf("deliverNotes made %d session.send calls, want 1", len(h.f.agentSent))
	}
	if got := h.f.agentSent[0]["origin"]; got != OriginInbox || h.f.agentSent[0]["origin_known"] != true {
		t.Fatalf("session.send origin=%v known=%v, want the manager's explicit Inbox attestation", got, h.f.agentSent[0]["origin_known"])
	}
	if got := h.f.agentSent[0]["inbox_permission_mode"]; got != proto.ModeBypass {
		t.Fatalf("session.send manager-only mode=%v, want the AI twin's own bypass", got)
	}
}

// An origin-book read failure is unknown, not an explicit ordinary decision. Both assistant.model's SendParams and the
// real session.send decoration omit the attestation so the node retains a persisted Inbox decision or fails closed.
func TestInboxAttestationStoreErrorStaysUnknown(t *testing.T) {
	h := agentHub(t)
	noDirectionsTimers(h.Service)
	const session = "inbox-origin-store-error"
	h.f.setSessions("server", proto.Session{SessionID: session, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"})
	h.originSet(t.Context(), "server", session, OriginInbox, "lark:real-item:1", "")
	waitFor(t, "the backfill's read of the to-dos", h.spaceBackfillScanIdle)
	h.origins.mu.Lock()
	h.origins.loaded, h.origins.m = false, nil
	h.origins.mu.Unlock()
	h.setStore(func() Store { return originErrorStore{h.st} })
	params, err := h.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: session,
		Params: map[string]any{"text": "Continue", "origin": OriginUser, "origin_known": true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, set := params["origin"]; set {
		t.Fatalf("store error produced an origin attestation: %v", params)
	}
	if _, set := params["origin_known"]; set {
		t.Fatalf("store error produced an explicit ordinary decision: %v", params)
	}
	if err := h.call(t.Context(), "server", DeviceSessionSend, map[string]any{"session_id": session, "text": "Continue", "origin": OriginUser, "origin_known": true}, nil); err != nil {
		t.Fatal(err)
	}
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if len(h.f.agentSent) != 1 {
		t.Fatalf("session.send calls=%d, want 1", len(h.f.agentSent))
	}
	if _, set := h.f.agentSent[0]["origin"]; set {
		t.Fatalf("session.send store error produced an origin attestation: %v", h.f.agentSent[0])
	}
	if _, set := h.f.agentSent[0]["origin_known"]; set {
		t.Fatalf("session.send store error produced an explicit ordinary decision: %v", h.f.agentSent[0])
	}
}

// A missing origin-book entry is not an ordinary attestation: the asynchronous backfill may not have reached a legacy
// Inbox conversation yet. A positive ordinary record remains sufficient once the backfill has actually classified it.
func TestInboxAttestationMissingOriginStaysUnknown(t *testing.T) {
	h := agentHub(t)
	const session = "legacy-before-origin-backfill"
	h.f.setSessions("server", proto.Session{SessionID: session, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"})
	params, err := h.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: session,
		Params: map[string]any{"text": "Continue", "origin": OriginUser, "origin_known": true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, set := params["origin"]; set {
		t.Fatalf("missing origin record produced an origin attestation: %v", params)
	}
	if _, set := params["origin_known"]; set {
		t.Fatalf("missing origin record was attested ordinary before backfill: %v", params)
	}
	h.originSet(t.Context(), "server", session, OriginUser, "", "")
	params, err = h.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: session, Params: map[string]any{"text": "Continue"}})
	if err != nil || params["origin_known"] != true {
		t.Fatalf("a positive ordinary origin record was not attested: %v %v", params, err)
	}
}

// The hub forwards session.send_once to session.send. Attestation must therefore be attached at the manager's device
// call boundary, including on CI/view-action paths that do not pass through SendParams.
func TestInboxSendOnceReattestsManagerOrigin(t *testing.T) {
	h := agentHub(t)
	const session = "inbox-send-once"
	h.f.setSessions("server", proto.Session{SessionID: session, Assistant: true, Status: proto.StatusWorking, Cwd: "/work"})
	h.originSet(t.Context(), "server", session, OriginInbox, "lark:real-item:1", "")
	if err := h.call(t.Context(), "server", DeviceSessionSendOnce, map[string]any{
		"session_id": session, "text": "Continue", "client_msg_id": "send-once-1", "origin": OriginUser, "origin_known": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if len(h.f.sentOnce) != 1 {
		t.Fatalf("session.send_once calls=%d, want 1", len(h.f.sentOnce))
	}
	params := h.f.sentOnce[0]
	if params["origin"] != OriginInbox || params["origin_known"] != true {
		t.Fatalf("forwarded send_once origin=%v known=%v, want explicit Inbox attestation", params["origin"], params["origin_known"])
	}
}

// Owner-confirmed reply execution is separate from the model-call guard even while the handling conversation is Inbox.
func TestInboxOwnerConfirmedReplyRuns(t *testing.T) {
	h, lark, it := handledHub(t)
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Owner-approved reply"}`); err != nil {
		t.Fatal(err)
	}
	code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
	if code != 200 || len(lark.got) != 1 || lark.got[0].Text != "Owner-approved reply" {
		t.Fatalf("confirmReply code=%d sent=%v", code, lark.got)
	}
}

// confirmAgent reaches runTool(agent_start), not Tool, so the model-call policy cannot intercept this owner action.
func TestInboxOwnerConfirmedAgentRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := agentHub(t)
		h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
		it := disabledTwinItem(h, "item", HandlingDone, AgentSkipped, "parent-session")
		h.originSet(t.Context(), "server", "parent-session", OriginInbox, it.ID, "")
		it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestAgent, Text: "Inspect the request", At: time.Now(), MachineID: "server", Cwd: "/work"}
		if _, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm}, true); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		h.f.mu.Lock()
		starts := len(h.f.starts)
		h.f.mu.Unlock()
		if starts != 1 {
			t.Fatalf("confirmAgent started %d Agents, want 1", starts)
		}
	})
}

// confirmTodo is likewise an internal owner-confirmed execution path, not a model call on the handling conversation.
func TestInboxOwnerConfirmedTodoRuns(t *testing.T) {
	h := muteHub(t)
	mergeItems(h)
	it := h.inbox.items["b"]
	it.Agent = &InboxAgent{State: AgentDrafted, MachineID: "server", SessionID: "s1"}
	h.originSet(t.Context(), "server", "s1", OriginInbox, it.ID, "")
	it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestTodo, Text: "Send Ana the numbers", At: time.Now()}
	out, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm}, true)
	if err != nil || out.DoneBy != doneByTodo {
		t.Fatalf("confirmTodo: %v %+v", err, out)
	}
	last := out.Chain[len(out.Chain)-1]
	if last.Link == nil {
		t.Fatal("confirmTodo did not link the new to-do")
	}
	if todo, ok := h.todoNow(last.Link.ID); !ok || todo.Title != "Send Ana the numbers" {
		t.Fatalf("confirmTodo todo=%+v present=%v", todo, ok)
	}
}
