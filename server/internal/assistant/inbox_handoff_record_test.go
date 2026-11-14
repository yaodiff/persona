package assistant

import (
	"strings"
	"testing"
)

// Recording a hand-off to an existing conversation or Agent is separate from sending one to the main conversation:
// the client has already delivered the text, so the route only records the target and the chain entry.

func recordedHandoffBody(machine, session, kind, title, clientID string) map[string]any {
	body := map[string]any{
		"session": map[string]any{"machine_id": machine, "session_id": session},
		"kind":    kind,
	}
	if title != "" {
		body["title"] = title
	}
	if clientID != "" {
		body["client_id"] = clientID
	}
	return body
}

func callRecordedHandoff(t *testing.T, h *inboxHub, id string, body map[string]any) (int, map[string]any) {
	t.Helper()
	return handoffCall(t, h, id, string(rawJSON(body)))
}

func mustRecordSessionHandoff(t *testing.T, h *inboxHub, id string, body map[string]any) map[string]any {
	t.Helper()
	code, out := callRecordedHandoff(t, h, id, body)
	if code != 200 {
		t.Fatalf("record hand-off: %d %v", code, out)
	}
	return out
}

func recordedHandoffTarget(t *testing.T, out map[string]any) SessionRef {
	t.Helper()
	session, ok := out["session"].(map[string]any)
	if !ok {
		t.Fatalf("the response has no session: %v", out)
	}
	machine, _ := session["machine_id"].(string)
	sid, _ := session["session_id"].(string)
	return SessionRef{MachineID: machine, SessionID: sid}
}

func recordedHandoffCounts(h *inboxHub, it *InboxItem) (handoffs, chain int) {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	return len(it.Handoffs), len(it.Chain)
}

func recordedChainForSession(h *inboxHub, it *InboxItem, kind, sid string) []InboxChainEntry {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	var out []InboxChainEntry
	for _, e := range it.Chain {
		if e.Kind == kind && e.Link != nil && e.Link.SessionID == sid {
			out = append(out, e)
		}
	}
	return out
}

// recordedSessionActivity is every node call that could deliver text, start a session, or wake one.
func recordedSessionActivity(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		switch c.Method {
		case DeviceSessionSend, DeviceSessionSendOnce, DeviceSessionStart, DeviceAssistantSend, DeviceAssistantWarm:
			out = append(out, c.Method)
		}
	}
	return out
}

// recordedProcessingSnapshot includes the state that recording a hand-off must leave alone. JSON gives the pointer
// fields a deep, deterministic snapshot rather than one that would change with the item.
func recordedProcessingSnapshot(h *inboxHub, it *InboxItem) string {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	return string(rawJSON(map[string]any{
		"status":     it.status(),
		"done_at":    it.DoneAt,
		"done_by":    it.DoneBy,
		"agent":      it.Agent,
		"handling":   it.Handling,
		"suggestion": it.Suggestion,
	}))
}

func recordedNamedHandoffText(kind, title string) string {
	if kind == "agent" {
		return "交给了 Agent「" + title + "」"
	}
	return "交给了「" + title + "」"
}

func TestInboxRecordHandoffKinds(t *testing.T) {
	tests := []struct {
		name, kind, session, title, clientID, chainKind string
	}{
		{name: "agent", kind: "agent", session: "recorded-agent", title: "排查报价", clientID: "10000000-0000-4000-8000-000000000001", chainKind: ChainAgent},
		{name: "conversation", kind: "conversation", session: "recorded-conversation", title: "报价后续", clientID: "10000000-0000-4000-8000-000000000002", chainKind: ChainHandling},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, it := handoffHub(t)
			beforeActivity := len(recordedSessionActivity(h.f))
			beforeStatus := it.status()
			out := mustRecordSessionHandoff(t, h, it.ID, recordedHandoffBody("server", tt.session, tt.kind, tt.title, tt.clientID))
			if target := recordedHandoffTarget(t, out); target.MachineID != "server" || target.SessionID != tt.session {
				t.Fatalf("target: %+v", target)
			}

			listedItem, ok := out["item"].(map[string]any)
			if !ok {
				t.Fatalf("the response has no item: %v", out)
			}
			listedHandoffs, _ := listedItem["handoffs"].([]any)
			if len(listedHandoffs) != 1 {
				t.Fatalf("listed hand-offs: %v", listedItem["handoffs"])
			}
			listedHandoff, _ := listedHandoffs[0].(map[string]any)
			if listedHandoff["machine_id"] != "server" || listedHandoff["session_id"] != tt.session || listedHandoff["op"] != nil {
				t.Fatalf("listed hand-off exposes the wrong fields: %v", listedHandoff)
			}
			if listedItem["status"] != beforeStatus {
				t.Fatalf("listed status moved from %q: %v", beforeStatus, listedItem["status"])
			}

			h.inbox.mu.Lock()
			stored := append([]InboxHandoff(nil), it.Handoffs...)
			status, doneAt, doneBy := it.status(), it.DoneAt, it.DoneBy
			agent, handling, suggestion := it.Agent, it.Handling, it.Suggestion
			h.inbox.mu.Unlock()
			if len(stored) != 1 || stored[0].MachineID != "server" || stored[0].SessionID != tt.session ||
				stored[0].Op != strings.ToLower(tt.clientID) || stored[0].At.IsZero() {
				t.Fatalf("stored hand-offs: %+v", stored)
			}
			if status != beforeStatus || doneAt != nil || doneBy != "" || agent != nil || handling != nil || suggestion != nil {
				t.Fatalf("recording moved processing state: status %q, done %v/%q, agent %+v, handling %+v, suggestion %+v",
					status, doneAt, doneBy, agent, handling, suggestion)
			}

			entries := recordedChainForSession(h, it, tt.chainKind, tt.session)
			if len(entries) != 1 {
				t.Fatalf("%s chain entries for %s: %+v", tt.chainKind, tt.session, entries)
			}
			entry := entries[0]
			if entry.Text != recordedNamedHandoffText(tt.kind, tt.title) || entry.Via != ViaUser || entry.Link.Type != LinkSession ||
				entry.Link.MachineID != "server" || entry.Link.SessionID != tt.session || entry.ID == "" || entry.At.IsZero() {
				t.Fatalf("chain entry: %+v", entry)
			}
			if got := len(recordedSessionActivity(h.f)); got != beforeActivity {
				t.Fatalf("recording sent, started, or woke a session: %v", got)
			}
			if !h.st.saved(it.ID) {
				t.Fatal("the recorded hand-off was not saved")
			}
		})
	}
}

func TestInboxRecordHandoffTitles(t *testing.T) {
	longTitle := "  第一行\n第二行  " + strings.Repeat("长", 50) + "\n第三行"
	tests := []struct {
		name, kind, session, title, clientID, chainKind string
	}{
		{name: "agent untitled", kind: "agent", session: "untitled-agent", clientID: "10000000-0000-4000-8000-000000000003", chainKind: ChainAgent},
		{name: "conversation untitled", kind: "conversation", session: "untitled-conversation", clientID: "10000000-0000-4000-8000-000000000004", chainKind: ChainHandling},
		{name: "agent clipped", kind: "agent", session: "clipped-agent", title: longTitle, clientID: "10000000-0000-4000-8000-000000000005", chainKind: ChainAgent},
		{name: "conversation clipped", kind: "conversation", session: "clipped-conversation", title: longTitle, clientID: "10000000-0000-4000-8000-000000000006", chainKind: ChainHandling},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, it := handoffHub(t)
			mustRecordSessionHandoff(t, h, it.ID, recordedHandoffBody("server", tt.session, tt.kind, tt.title, tt.clientID))
			entries := recordedChainForSession(h, it, tt.chainKind, tt.session)
			if len(entries) != 1 {
				t.Fatalf("chain entries: %+v", entries)
			}
			text := entries[0].Text
			if tt.title == "" {
				// The exact untitled translations are not part of the wire contract; they must be generic, not a named empty title.
				if text == "" || strings.ContainsAny(text, "「」") {
					t.Fatalf("untitled hand-off text is not generic: %q", text)
				}
				if tt.kind == "agent" && !strings.Contains(text, "Agent") {
					t.Fatalf("untitled Agent hand-off text does not name the target kind: %q", text)
				}
				return
			}
			clipped := clip(oneLine(tt.title), 40)
			if clipped == oneLine(tt.title) {
				t.Fatal("the title fixture was not clipped")
			}
			if want := recordedNamedHandoffText(tt.kind, clipped); text != want || strings.Contains(text, "\n") {
				t.Fatalf("chain text %q, want %q", text, want)
			}
		})
	}
}

func TestInboxRecordHandoffReplaysNormalizedClientID(t *testing.T) {
	h, _, it := handoffHub(t)
	beforeActivity := len(recordedSessionActivity(h.f))
	clientID := "a0000000-0000-4000-8000-000000000007"
	first := mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "original-target", "agent", "原来的 Agent", strings.ToUpper(clientID)))
	if target := recordedHandoffTarget(t, first); target.SessionID != "original-target" {
		t.Fatalf("first target: %+v", target)
	}
	_, chainBefore := recordedHandoffCounts(h, it)

	// A normalized replay names the original operation even if the rest of an otherwise valid request differs.
	replay := mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "replacement-target", "conversation", "另一个对话", clientID))
	if target := recordedHandoffTarget(t, replay); target.MachineID != "server" || target.SessionID != "original-target" {
		t.Fatalf("replay target: %+v", target)
	}
	handoffs, chainAfter := recordedHandoffCounts(h, it)
	if handoffs != 1 || chainAfter != chainBefore {
		t.Fatalf("replay recorded another operation: %d hand-offs, chain %d -> %d", handoffs, chainBefore, chainAfter)
	}
	if entries := recordedChainForSession(h, it, ChainHandling, "replacement-target"); len(entries) != 0 {
		t.Fatalf("replay added the replacement target: %+v", entries)
	}
	h.inbox.mu.Lock()
	op := it.Handoffs[0].Op
	h.inbox.mu.Unlock()
	if op != clientID {
		t.Fatalf("stored client id %q, want normalized %q", op, clientID)
	}
	if got := len(recordedSessionActivity(h.f)); got != beforeActivity {
		t.Fatalf("recording or replay sent, started, or woke a session: %v", got)
	}
}

func TestInboxRecordHandoffDerivesStableClientID(t *testing.T) {
	h, _, it := handoffHub(t)
	first := recordedHandoffBody(" server ", " derived-target ", "agent", "第一次", "")
	firstOut := mustRecordSessionHandoff(t, h, it.ID, first)
	if target := recordedHandoffTarget(t, firstOut); target.MachineID != "server" || target.SessionID != "derived-target" {
		t.Fatalf("the session ids were not trimmed: %+v", target)
	}
	_, chainBefore := recordedHandoffCounts(h, it)
	h.inbox.mu.Lock()
	derived := it.Handoffs[0].Op
	h.inbox.mu.Unlock()
	if !validClientID(derived) {
		t.Fatalf("derived client id %q is not valid", derived)
	}

	// Missing and invalid ids derive the same operation from item, session, and kind; the title is not part of its identity.
	replay := recordedHandoffBody("server", "derived-target", "agent", "改名也不影响重放", "not-a-uuid")
	mustRecordSessionHandoff(t, h, it.ID, replay)
	handoffs, chainAfter := recordedHandoffCounts(h, it)
	if handoffs != 1 || chainAfter != chainBefore {
		t.Fatalf("derived-id replay recorded another operation: %d hand-offs, chain %d -> %d", handoffs, chainBefore, chainAfter)
	}

	// The kind is part of a derived id, so recording the same session as a conversation is a distinct operation.
	otherKind := recordedHandoffBody("server", "derived-target", "conversation", "同一 session 的对话", "")
	mustRecordSessionHandoff(t, h, it.ID, otherKind)
	handoffs, chainAfterOtherKind := recordedHandoffCounts(h, it)
	if handoffs != 2 || chainAfterOtherKind != chainBefore+1 {
		t.Fatalf("the other kind was not recorded once: %d hand-offs, chain %d -> %d", handoffs, chainBefore, chainAfterOtherKind)
	}
	h.inbox.mu.Lock()
	firstOp, secondOp := it.Handoffs[0].Op, it.Handoffs[1].Op
	h.inbox.mu.Unlock()
	if firstOp != derived || secondOp == derived || !validClientID(secondOp) {
		t.Fatalf("derived ops %q and %q", firstOp, secondOp)
	}
	if entries := recordedChainForSession(h, it, ChainAgent, "derived-target"); len(entries) != 1 {
		t.Fatalf("Agent chain: %+v", entries)
	}
	if entries := recordedChainForSession(h, it, ChainHandling, "derived-target"); len(entries) != 1 {
		t.Fatalf("conversation chain: %+v", entries)
	}
}

func TestInboxRecordHandoffDeduplicatesChainBySessionAndKind(t *testing.T) {
	h, _, it := handoffHub(t)
	beforeActivity := len(recordedSessionActivity(h.f))
	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "shared-target", "agent", "第一个 Agent 名称", "10000000-0000-4000-8000-000000000008"))
	_, chainAfterFirst := recordedHandoffCounts(h, it)

	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "shared-target", "agent", "第二个 Agent 名称", "10000000-0000-4000-8000-000000000009"))
	handoffs, chainAfterSecond := recordedHandoffCounts(h, it)
	if handoffs != 2 || chainAfterSecond != chainAfterFirst {
		t.Fatalf("same kind duplicated its chain: %d hand-offs, chain %d -> %d", handoffs, chainAfterFirst, chainAfterSecond)
	}
	if entries := recordedChainForSession(h, it, ChainAgent, "shared-target"); len(entries) != 1 {
		t.Fatalf("Agent chain: %+v", entries)
	}

	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "shared-target", "conversation", "同一会话作为对话", "10000000-0000-4000-8000-00000000000a"))
	handoffs, chainAfterConversation := recordedHandoffCounts(h, it)
	if handoffs != 3 || chainAfterConversation != chainAfterFirst+1 {
		t.Fatalf("the other kind did not add exactly one entry: %d hand-offs, chain %d -> %d", handoffs, chainAfterFirst, chainAfterConversation)
	}
	if entries := recordedChainForSession(h, it, ChainHandling, "shared-target"); len(entries) != 1 {
		t.Fatalf("conversation chain: %+v", entries)
	}

	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "shared-target", "conversation", "另一个对话名称", "10000000-0000-4000-8000-00000000000b"))
	handoffs, chainAfterLast := recordedHandoffCounts(h, it)
	if handoffs != 4 || chainAfterLast != chainAfterConversation {
		t.Fatalf("conversation kind duplicated its chain: %d hand-offs, chain %d -> %d", handoffs, chainAfterConversation, chainAfterLast)
	}
	if got := len(recordedSessionActivity(h.f)); got != beforeActivity {
		t.Fatalf("recording sent, started, or woke a session: %v", got)
	}
}

func TestInboxRecordHandoffValidation(t *testing.T) {
	tests := []struct {
		name, clientID string
		change         func(map[string]any)
	}{
		{name: "missing machine id", clientID: "10000000-0000-4000-8000-00000000000c", change: func(body map[string]any) {
			delete(body["session"].(map[string]any), "machine_id")
		}},
		{name: "missing session id", clientID: "10000000-0000-4000-8000-00000000000d", change: func(body map[string]any) {
			delete(body["session"].(map[string]any), "session_id")
		}},
		{name: "missing kind", clientID: "10000000-0000-4000-8000-00000000000e", change: func(body map[string]any) {
			delete(body, "kind")
		}},
		{name: "invalid kind", clientID: "10000000-0000-4000-8000-00000000000f", change: func(body map[string]any) {
			body["kind"] = "main"
		}},
		{name: "text with session", clientID: "10000000-0000-4000-8000-000000000010", change: func(body map[string]any) {
			body["text"] = "the client already sent this"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, it := handoffHub(t)
			beforeActivity := len(recordedSessionActivity(h.f))
			body := recordedHandoffBody("server", "invalid-target", "agent", "不应记录", tt.clientID)
			tt.change(body)
			code, out := callRecordedHandoff(t, h, it.ID, body)
			if code != 400 {
				t.Fatalf("invalid record: %d %v", code, out)
			}
			handoffs, chain := recordedHandoffCounts(h, it)
			if handoffs != 0 || chain != 0 {
				t.Fatalf("an invalid request recorded %d hand-offs and %d chain entries", handoffs, chain)
			}
			if got := len(recordedSessionActivity(h.f)); got != beforeActivity {
				t.Fatalf("an invalid request sent, started, or woke a session: %v", got)
			}
		})
	}
}

func TestInboxRecordHandoffUnknownItem(t *testing.T) {
	h, _, it := handoffHub(t)
	beforeActivity := len(recordedSessionActivity(h.f))
	body := recordedHandoffBody("server", "unknown-item-target", "conversation", "对话", "10000000-0000-4000-8000-000000000011")
	code, out := callRecordedHandoff(t, h, it.ID+"-missing", body)
	if code != 404 {
		t.Fatalf("unknown item: %d %v", code, out)
	}
	handoffs, chain := recordedHandoffCounts(h, it)
	if handoffs != 0 || chain != 0 {
		t.Fatalf("the unknown-item request changed another item: %d hand-offs, %d chain entries", handoffs, chain)
	}
	if got := len(recordedSessionActivity(h.f)); got != beforeActivity {
		t.Fatalf("the unknown-item request sent, started, or woke a session: %v", got)
	}
}

func TestInboxRecordHandoffPreservesProcessingState(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"保留这个建议","note":"记录交接不能替换它"}`); err != nil {

		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	populated := it.Agent != nil && it.Handling != nil && it.Suggestion != nil
	h.inbox.mu.Unlock()
	if !populated {
		t.Fatal("the fixture did not populate Agent, Handling, and Suggestion")
	}
	before := recordedProcessingSnapshot(h, it)
	beforeActivity := len(recordedSessionActivity(h.f))

	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "state-preserving-target", "agent", "另一个 Agent", "10000000-0000-4000-8000-000000000012"))
	if after := recordedProcessingSnapshot(h, it); after != before {
		t.Fatalf("recording moved processing state:\nbefore: %s\nafter:  %s", before, after)
	}
	if afterActivity := len(recordedSessionActivity(h.f)); afterActivity != beforeActivity {
		t.Fatalf("recording added session activity: %d -> %d", beforeActivity, afterActivity)
	}
}

func TestInboxRecordHandoffConversationCanSuggest(t *testing.T) {
	h, _, it := handoffHub(t)
	beforeActivity := len(recordedSessionActivity(h.f))
	const session = "write-back-conversation"
	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", session, "conversation", "负责报价的对话", "10000000-0000-4000-8000-000000000013"))
	h.inbox.mu.Lock()
	suggestion := it.Suggestion
	h.inbox.mu.Unlock()
	if suggestion != nil {
		t.Fatalf("recording itself created a suggestion: %+v", suggestion)
	}
	if got := len(recordedSessionActivity(h.f)); got != beforeActivity {
		t.Fatalf("recording sent, started, or woke a session: %v", got)
	}

	args := string(rawJSON(map[string]any{
		"item":  it.ID,
		"kind":  "reply",
		"text":  "可以，周五上午给你",
		"recap": "对话已经给出报价时间",
	}))
	if _, err := mainSuggest(h, session, args); err != nil {
		t.Fatalf("the recorded conversation could not write back: %v", err)
	}
	h.inbox.mu.Lock()
	g := it.Suggestion
	agent, handling := it.Agent, it.Handling
	entry := it.Chain[len(it.Chain)-1]
	h.inbox.mu.Unlock()
	if g == nil || g.Version != 1 || g.Kind != SuggestReply || g.Text != "可以，周五上午给你" ||
		g.Conversation == nil || g.Conversation.MachineID != "server" || g.Conversation.SessionID != session {
		t.Fatalf("suggestion does not name the recorded conversation: %+v", g)
	}
	if agent != nil || handling != nil {
		t.Fatalf("write-back created a processing conversation: agent %+v, handling %+v", agent, handling)
	}
	if entry.Kind != ChainHandling || entry.Link == nil || entry.Link.MachineID != "server" || entry.Link.SessionID != session ||
		entry.Detail != "可以，周五上午给你" {
		t.Fatalf("write-back chain entry: %+v", entry)
	}
}
