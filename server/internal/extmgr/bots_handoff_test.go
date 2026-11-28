package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func (w *botWorld) handoff(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return w.do(t, "POST", "/api/v1/bots/feishu/handoffs", body)
}

func (w *botWorld) putHandoff(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return w.do(t, "PUT", "/api/v1/bots/feishu", `{"handoff":`+body+`}`)
}

// The default policy confirms every chat; a listed chat is direct; a chat on both lists is blocked; off refuses all.
func TestHandoffPolicy(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	set := func() FeishuBotSettings { _, s := w.h.botAPI(); return s }
	if d, r := set().handoffDelivery("oc_any"); d != botHandoffDeliveryConfirm || r != "" {
		t.Fatalf("default: %s %s", d, r)
	}
	if code, out := w.putHandoff(t, `{"direct_chats":["oc_direct"],"blocked_chats":["oc_direct","oc_blocked"]}`); code != 200 || out["handoff"] == nil {
		t.Fatalf("put: %d %v", code, out)
	}
	for _, c := range []struct {
		chat     string
		delivery assistantwire.FeishuHandoffDelivery
		refusal  string
	}{
		{"oc_direct", "", "chat_blocked"},
		{"oc_blocked", "", "chat_blocked"},
		{"oc_other", botHandoffDeliveryConfirm, ""},
	} {
		if d, r := set().handoffDelivery(c.chat); d != c.delivery || r != c.refusal {
			t.Errorf("%s: %s %s", c.chat, d, r)
		}
	}
	if code, _ := w.putHandoff(t, `{"off":true}`); code != 200 {
		t.Fatal(code)
	}
	if _, r := set().handoffDelivery("oc_other"); r != "handoff_off" {
		t.Fatalf("off: %s", r)
	}
	// A chat on the blocklist stays blocked while replies are off: the blocklist wins.
	if _, r := set().handoffDelivery("oc_blocked"); r != "chat_blocked" {
		t.Fatalf("blocked while off: %s", r)
	}
	st := w.h.store.(*botStore)
	if rows := rowsWith(st.takeAudit(), auditBotHandoffPolicy); len(rows) != 2 {
		t.Fatalf("policy audited %d times, want 2", len(rows))
	}
	// The same policy again changes nothing, so nothing is audited.
	if code, _ := w.putHandoff(t, `{"off":true}`); code != 200 {
		t.Fatal(code)
	}
	if rows := rowsWith(st.takeAudit(), auditBotHandoffPolicy); len(rows) != 0 {
		t.Fatalf("unchanged policy audited: %v", rows)
	}
}

// A list is checked before it is kept: a bad id, a repeat and too many are refused, an absent key leaves the list and
// an empty one clears it. Unpairing and a new App ID clear the policy.
func TestHandoffPolicyValidation(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	for _, body := range []string{
		`{"direct_chats":["oc bad"]}`,
		`{"direct_chats":["oc_a","oc_a"]}`,
		`{"direct_chats":[` + strings.Repeat(`"oc_x",`, botHandoffChatsMax) + `"oc_last"]}`,
		`{"launcher":"has space"}`,
	} {
		if code, _ := w.putHandoff(t, body); code != 400 {
			t.Errorf("%s: %d, want 400", body[:40], code)
		}
	}
	if code, _ := w.putHandoff(t, `{"direct_chats":["oc_keep"]}`); code != 200 {
		t.Fatal(code)
	}
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"enabled":true}`); code != 200 || out["handoff"] == nil {
		t.Fatalf("absent key cleared the policy: %v", out["handoff"])
	}
	if code, out := w.putHandoff(t, `{"direct_chats":[]}`); code != 200 {
		t.Fatal(code)
	} else if h, _ := out["handoff"].(map[string]any); h["direct_chats"] != nil {
		t.Fatalf("empty list did not clear: %v", h)
	}
	if code, _ := w.putHandoff(t, `{"blocked_chats":["oc_b"]}`); code != 200 {
		t.Fatal(code)
	}
	// A new App ID clears the policy with the owner, before anything else of the request.
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":"cli_other","app_secret":"another-secret"}`); code != 200 {
		t.Fatal(code)
	}
	if _, out := w.do(t, "GET", "/api/v1/bots/feishu", ""); out["handoff"] != nil {
		t.Fatalf("a new App ID kept the policy: %v", out["handoff"])
	}
	if code, _ := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", ""); code != 200 {
		t.Fatal(code)
	}
	if _, out := w.do(t, "GET", "/api/v1/bots/feishu", ""); out["handoff"] != nil {
		t.Fatalf("unpair kept the policy: %v", out["handoff"])
	}
	// A list without an owner is refused.
	if code, _ := w.putHandoff(t, `{"direct_chats":["oc_x"]}`); code != 400 {
		t.Fatalf("no owner: %d", code)
	}
}

// A handoff is refused while the bot is off, replayed for the same ref and payload, and conflicts for another payload.
func TestHandoffRoute(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	shortInboundWait(t)
	body := `{"chat_id":"oc_group","ref":"r1","question":"How do I call this?"}`
	if code, out := w.handoff(t, body); code != 409 || out["code"] != "bot_off" {
		t.Fatalf("unpaired: %d %v", code, out)
	}
	w.pair(t)
	w.a.on(assistantwire.MethodFeishuHandoff, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	code, out := w.handoff(t, body)
	if code != 200 || out["replayed"] != false {
		t.Fatalf("take: %d %v", code, out)
	}
	h := out["handoff"].(map[string]any)
	if h["state"] != "accepted" || h["delivery"] != "confirm" || !strings.HasPrefix(h["id"].(string), "bh_") {
		t.Fatalf("handoff: %v", h)
	}
	if code, again := w.handoff(t, body); code != 200 || again["replayed"] != true || again["handoff"].(map[string]any)["id"] != h["id"] {
		t.Fatalf("replay: %d %v", code, again)
	}
	if code, out := w.handoff(t, `{"chat_id":"oc_group","ref":"r1","question":"A different question"}`); code != 409 || out["code"] != "handoff_conflict" {
		t.Fatalf("conflict: %d %v", code, out)
	}
	for _, bad := range []string{
		`{"chat_id":"oc bad","question":"q"}`,
		`{"chat_id":"oc_group","question":""}`,
		`{"chat_id":"oc_group","question":"q","in_thread":true}`,
		`{"chat_id":"oc_group","question":"q","ref":"has space"}`,
		`not json`,
	} {
		if code, out := w.handoff(t, bad); code != 400 || out["code"] != "invalid_handoff" {
			t.Errorf("%s: %d %v", bad, code, out)
		}
	}
}

// Caps refuse a chat's hour, the day and too many running at once. An assistant that is away answers 503 and leaves no source.
func TestHandoffLimitsAndAway(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	shortInboundWait(t)
	w.a.on(assistantwire.MethodFeishuHandoff, func(json.RawMessage) (any, error) { return nil, errAssistantOffline })
	if code, out := w.handoff(t, `{"chat_id":"oc_group","question":"q"}`); code != 503 || out["code"] != "assistant_unavailable" {
		t.Fatalf("away: %d %v", code, out)
	}
	if _, set := w.h.botAPI(); len(set.AISources) != 0 {
		t.Fatalf("a refused handoff left a source: %+v", set.AISources)
	}
	w.a.on(assistantwire.MethodFeishuHandoff, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	for i := 0; i < botHandoffRunning; i++ {
		if code, _ := w.handoff(t, fmt.Sprintf(`{"chat_id":"oc_group","question":"q%d"}`, i)); code != 200 {
			t.Fatalf("handoff %d: %d", i, code)
		}
	}
	if code, out := w.handoff(t, `{"chat_id":"oc_group","question":"one more"}`); code != 429 || out["code"] != "handoff_rate_limited" {
		t.Fatalf("over the running cap: %d %v", code, out)
	}
}

// A taken handoff is a proxy-reply source, which /ai and /read lookups never match, and its chat is not one the bot may use.
func TestHandoffSourceStaysIsolated(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	shortInboundWait(t)
	w.api.inChat("om_anchor", "oc_group")
	w.a.on(assistantwire.MethodFeishuHandoff, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	if code, _ := w.handoff(t, `{"chat_id":"oc_group","message_id":"om_anchor","question":"q"}`); code != 200 {
		t.Fatal(code)
	}
	_, set := w.h.botAPI()
	now := time.Now().Add(time.Minute)
	if src := set.aiSource("om_anchor", now); src != nil {
		t.Fatalf("/ai lookup matched a handoff: %+v", src)
	}
	if _, found, _ := aiSourceReply(set, "", "om_anchor", "hi", now, false); found {
		t.Fatal("aiSourceReply matched a handoff")
	}
	if _, found, _ := readSourceReply(set, "", "om_anchor", now); found {
		t.Fatal("readSourceReply matched a handoff")
	}
	if botChatAllowed(set, "oc_group") {
		t.Fatal("the handoff's chat became one the bot may send into")
	}
	src := set.handoffSource(set.AISources[0].HandoffID, now)
	if src == nil || src.Kind != botSourceProxyReply || !src.Confirm {
		t.Fatalf("source: %+v", src)
	}
	// A plain reply under the anchor is refused: it is not an /ai source.
	if _, err := w.h.feishuCall(context.Background(), assistantwire.FeishuText, handoffJSON(map[string]any{"reply_to": "om_anchor", "text": "hi"})); err == nil {
		t.Fatal("a plain reply under the anchor was allowed")
	}
}

// Delivery: the anchor gets a reply under the handoff's key and never a detour; no anchor gets one send; a confirm
// source posts only once confirmed; a third try and a handoff that ended are refused.
func TestHandoffDelivery(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	now := w.h.bots.clock().UTC()
	take := func(id, chat, anchor string, confirm bool) {
		t.Helper()
		if err := w.h.takeHandoffSource(BotAISource{ChatID: chat, MessageID: anchor, At: now, Kind: botSourceProxyReply,
			HandoffID: id, Confirm: confirm, By: "extension:demo"}); err != nil {
			t.Fatal(err)
		}
	}
	send := func(id, text string, confirmed bool) error {
		t.Helper()
		_, err := w.h.feishuCall(context.Background(), assistantwire.FeishuText, handoffJSON(map[string]any{"handoff": id, "text": text, "confirmed": confirmed}))
		return err
	}
	take("bh_direct", "oc_group", "om_anchor", false)
	w.api.inChat("om_anchor", "oc_group")
	if err := send("bh_direct", "the answer", false); err != nil {
		t.Fatal(err)
	}
	call := w.api.find(t, "/reply", "ho:bh_direct")
	if !strings.Contains(call, "om_anchor") {
		t.Fatalf("not a reply under the anchor: %s", call)
	}
	if err := send("bh_direct", "again", false); err == nil {
		t.Fatal("a handoff that already posted was sent again")
	}
	// A reply that fails still counts, and the third try is refused.
	take("bh_tries", "oc_group", "om_tries", false)
	w.api.inChat("om_tries", "oc_group")
	w.api.mu.Lock()
	w.api.rejectReply = 99991
	w.api.mu.Unlock()
	for i := 0; i < botSourceReplies; i++ {
		if err := send("bh_tries", "try", false); err == nil {
			t.Fatalf("try %d was accepted while the platform refused", i)
		}
	}
	w.api.mu.Lock()
	w.api.rejectReply = 0
	w.api.mu.Unlock()
	if err := send("bh_tries", "once more", false); err == nil {
		t.Fatal("a third try was allowed")
	}

	take("bh_new", "oc_group", "", false)
	before := w.api.posts()
	if err := send("bh_new", "hello", false); err != nil {
		t.Fatal(err)
	}
	sent := w.api.find(t, "POST /open-apis/im/v1/messages {", "ho:bh_new")
	if !strings.Contains(sent, "oc_group") || w.api.posts() != before+1 {
		t.Fatalf("unanchored send: %s", sent)
	}

	take("bh_confirm", "oc_group", "", true)
	if err := send("bh_confirm", "draft", false); err == nil {
		t.Fatal("a confirm handoff posted without confirmation")
	}
	if err := send("bh_confirm", "draft", true); err != nil {
		t.Fatal(err)
	}

	take("bh_long", "oc_group", "", false)
	if err := send("bh_long", strings.Repeat("x", botSourceTextMax+1), false); err == nil {
		t.Fatal("an over-long reply was allowed")
	}
	if err := w.h.endHandoff(context.Background(), "bh_long", "no_reply"); err != nil {
		t.Fatal(err)
	}
	if err := send("bh_long", "late", false); err == nil {
		t.Fatal("an ended handoff posted")
	}

	// A handoff that was blocked after it was taken is refused, and so is another chat, another parent, a full answer
	// and an id nobody took.
	take("bh_blocked", "oc_blocked", "", false)
	if code, _ := w.putHandoff(t, `{"blocked_chats":["oc_blocked"]}`); code != 200 {
		t.Fatal(code)
	}
	if err := send("bh_blocked", "too late", false); err == nil {
		t.Fatal("a chat blocked after the handoff was taken still posted")
	}
	take("bh_chat", "oc_group", "om_anchor2", false)
	w.api.inChat("om_anchor2", "oc_group")
	for _, body := range []map[string]any{
		{"handoff": "bh_chat", "chat_id": "oc_other", "text": "no"},
		{"handoff": "bh_chat", "reply_to": "om_other", "text": "no"},
		{"handoff": "bh_chat", "text": "no", "full_answer": true},
		{"handoff": "bh_missing", "text": "no"},
	} {
		if _, err := w.h.feishuCall(context.Background(), assistantwire.FeishuText, handoffJSON(body)); err == nil {
			t.Fatalf("allowed: %v", body)
		}
	}

	// An anchored reply whose parent is gone is lost, never sent into the chat.
	take("bh_gone", "oc_group", "om_gone", false)
	w.api.inChat("om_gone", "oc_group")
	w.api.mu.Lock()
	w.api.rejectReply = 230011
	w.api.mu.Unlock()
	w.api.mu.Lock()
	start := len(w.api.calls)
	w.api.mu.Unlock()
	if err := send("bh_gone", "lost", false); err == nil {
		t.Fatal("a reply whose parent is gone was accepted")
	}
	w.api.mu.Lock()
	made := append([]string(nil), w.api.calls[start:]...)
	w.api.rejectReply = 0
	w.api.mu.Unlock()
	for _, c := range made {
		if strings.Contains(c, "/messages ") {
			t.Fatalf("a lost reply was sent into the chat instead: %s", c)
		}
	}
	st := w.h.store.(*botStore)
	rows := st.takeAudit()
	for _, action := range []string{auditBotHandoffPosted, auditBotHandoffEnded} {
		if len(rowsWith(rows, action)) == 0 {
			t.Errorf("no %s audit", action)
		}
	}
	for _, r := range rows {
		raw, _ := json.Marshal(r.Detail)
		if strings.Contains(string(raw), "the answer") {
			t.Fatalf("audit carries the reply: %s", raw)
		}
	}
}

// A question, its context and the caller's names are never stored with the settings and never audited.
func TestHandoffAuditKeepsNoContent(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	shortInboundWait(t)
	secret := "the question text nobody should store"
	w.a.on(assistantwire.MethodFeishuHandoff, func(p json.RawMessage) (any, error) {
		if !strings.Contains(string(p), secret) {
			t.Errorf("the assistant was not told the question: %s", p)
		}
		return map[string]any{}, nil
	})
	if code, _ := w.handoff(t, `{"chat_id":"oc_group","chat_name":"Secret Chat","sender_name":"Ada","question":"`+secret+`","context":"some context"}`); code != 200 {
		t.Fatal(code)
	}
	raw, _ := json.Marshal(w.h.store.(*botStore).vals)
	for _, hidden := range []string{secret, "Secret Chat", "Ada", "some context"} {
		if strings.Contains(string(raw), hidden) {
			t.Errorf("settings store %q", hidden)
		}
	}
	for _, r := range w.h.store.(*botStore).takeAudit() {
		d, _ := json.Marshal(r.Detail)
		if strings.Contains(string(d), secret) || strings.Contains(string(d), "Ada") {
			t.Errorf("audit %s carries content: %s", r.Action, d)
		}
	}
	if rows := rowsWith(w.h.store.(*botStore).audit, auditBotHandoffTaken); len(rows) != 0 {
		// taken above already
	}
}

func handoffJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestFeishuBotSettingsHandoffJSONRoundTrip(t *testing.T) {
	in := FeishuBotSettings{
		Enabled: true,
		Handoff: &botHandoffPolicy{
			Off:          true,
			DirectChats:  []string{"oc_direct_1", "oc_direct_2"},
			BlockedChats: []string{"oc_blocked_1"},
			Launcher:     "codex",
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("unmarshal raw map: %v", err)
	}
	var handoffMap map[string]any
	if err := json.Unmarshal(rawMap["handoff"], &handoffMap); err != nil {
		t.Fatalf("unmarshal handoff field: %v", err)
	}
	if handoffMap["off"] != true || handoffMap["launcher"] != "codex" {
		t.Fatalf("unexpected handoff JSON fields: %v", handoffMap)
	}
	var out FeishuBotSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if !handoffPolicyEqual(in.Handoff, out.Handoff) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out.Handoff, in.Handoff)
	}

	emptyRaw, err := json.Marshal(FeishuBotSettings{})
	if err != nil {
		t.Fatalf("marshal empty settings: %v", err)
	}
	if strings.Contains(string(emptyRaw), `"handoff"`) {
		t.Fatalf("expected nil handoff to be omitted, got %s", emptyRaw)
	}
	var emptyOut FeishuBotSettings
	if err := json.Unmarshal(emptyRaw, &emptyOut); err != nil {
		t.Fatalf("unmarshal empty settings: %v", err)
	}
	if emptyOut.Handoff != nil {
		t.Fatalf("expected nil handoff after unmarshal, got %+v", emptyOut.Handoff)
	}

	for _, valid := range []string{"r1", "A_b:1.2-3"} {
		if !botHandoffRefPattern.MatchString(valid) {
			t.Errorf("expected ref %q to match botHandoffRefPattern", valid)
		}
		if e := checkHandoffRequest(&botHandoffRequest{ChatID: "oc_group", Ref: valid, Question: "q"}); e != nil {
			t.Errorf("expected ref %q to pass checkHandoffRequest: %v", valid, e)
		}
	}
	for _, invalid := range []string{"", "has space", "a/b"} {
		if botHandoffRefPattern.MatchString(invalid) {
			t.Errorf("expected ref %q to fail botHandoffRefPattern", invalid)
		}
		if invalid != "" {
			if e := checkHandoffRequest(&botHandoffRequest{ChatID: "oc_group", Ref: invalid, Question: "q"}); e == nil {
				t.Errorf("expected ref %q to fail checkHandoffRequest", invalid)
			}
		}
	}
}
