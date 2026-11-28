package extmgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// allowEveryone opens chat to every member, at revision, and returns the new revision.
func (w *botWorld) allowEveryone(t *testing.T, rev int, chat string, hourly int) int {
	t.Helper()
	body := fmt.Sprintf(`{"revision":%d,"policy":{"mode":"everyone_in_allowed_chats","allowed_chats":[{"chat_id":%q}],"per_chat_hourly":%d}}`, rev, chat, hourly)
	code, out := w.groupPut(t, body)
	if code != 200 {
		t.Fatalf("allow %s: %d %v", chat, code, out)
	}
	return int(out["revision"].(float64))
}

// takeGuest sends one @bot and returns the guest turn the assistant was given, once the line has drained.
func (w *botWorld) takeGuest(t *testing.T, id, from, text, chat, thread string) assistantwire.FeishuGuestTurn {
	t.Helper()
	before := len(w.a.got(assistantwire.MethodFeishuGuest))
	w.send(t, mentionEvent(id, from, text, chat, thread, "ou_bot"))
	drain := "dm_" + id
	w.send(t, botMessageEvent(drain, "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "the line drained", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == drain
	})
	got := w.a.got(assistantwire.MethodFeishuGuest)
	if len(got) != before+1 {
		t.Fatalf("%s: the assistant got %d guest turns, want %d", id, len(got), before+1)
	}
	var turn assistantwire.FeishuGuestTurn
	if err := json.Unmarshal(got[len(got)-1], &turn); err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestGuestNeedsAStructuredMention(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	for _, ev := range [][]byte{
		groupEvent("om_g1", "ou_s", "@Persona hi", topicChat, "omt_1"),
		mentionEvent("om_g2", "ou_s", "hi", topicChat, "omt_1", "ou_someone"),
	} {
		w.send(t, ev)
	}
	w.h.bots.mu.Lock()
	w.h.bots.info.OpenID = ""
	w.h.bots.mu.Unlock()
	w.api.mu.Lock()
	w.api.botInfoFail = true
	w.api.mu.Unlock()
	w.send(t, mentionEvent("om_g3", "ou_s", "hi", topicChat, "omt_1", "ou_bot"))
	w.send(t, botMessageEvent("om_g_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "drained", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_g_dm"
	})
	if n := len(w.a.got(assistantwire.MethodFeishuGuest)); n != 0 {
		t.Fatalf("a mention the bot could not own was taken %d times", n)
	}
	w.api.mu.Lock()
	w.api.botInfoFail = false
	w.api.mu.Unlock()
	w.h.bots.mu.Lock()
	w.h.bots.info.OpenID = "ou_bot"
	w.h.bots.mu.Unlock()
	if turn := w.takeGuest(t, "om_g4", "ou_s", "hello there", topicChat, "omt_1"); turn.Question != "hello there" {
		t.Fatalf("taken question: %q", turn.Question)
	}
}

func TestGuestIsHandedAsAGuestNeverAsAMessage(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.h.connectors = connector.NewRegistry(nil)
	if err := w.h.connectors.Add(guestToolConn{"wiki"}); err != nil {
		t.Fatal(err)
	}
	if code, out := w.groupPut(t, `{"revision":0,"policy":{"mode":"everyone_in_allowed_chats","allowed_chats":[{"chat_id":"oc_topic"}],"guest_tools":["wiki_search"]}}`); code != 200 {
		t.Fatalf("allow: %d %v", code, out)
	}
	turn := w.takeGuest(t, "om_h1", "ou_s", "what time is it", topicChat, "omt_9")
	if turn.Conversation != "omt_9" || strings.Contains(string(mustJSON(turn)), "om_h1") {
		t.Fatalf("turn: %+v", turn)
	}
	if tools := turn.Tools; len(tools) != 1 || tools[0] != "wiki_search" {
		t.Fatalf("tools: %v", tools)
	}
	if len(w.handed()) != 1 || w.handed()[0] == "om_h1" {
		t.Fatalf("handed: %v", w.handed())
	}
}

func TestGuestConversationKey(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	if got := w.takeGuest(t, "om_k1", "ou_s", "one", topicChat, "omt_a").Conversation; got != "omt_a" {
		t.Fatalf("thread: %s", got)
	}
	root := groupEvent("om_k2", "ou_s", "two", topicChat, "")
	var ev map[string]any
	_ = json.Unmarshal(root, &ev)
	ev["event"].(map[string]any)["message"].(map[string]any)["root_id"] = "om_root"
	ev["event"].(map[string]any)["message"].(map[string]any)["mentions"] = []any{map[string]any{"key": "@_user_1", "id": map[string]string{"open_id": "ou_bot"}}}
	ev["event"].(map[string]any)["message"].(map[string]any)["content"] = textContent("@_user_1 two")
	raw, _ := json.Marshal(ev)
	w.send(t, raw)
	w.send(t, botMessageEvent("om_k_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "drained", func() bool { g := w.handed(); return len(g) > 0 && g[len(g)-1] == "om_k_dm" })
	got := w.a.got(assistantwire.MethodFeishuGuest)
	var turn assistantwire.FeishuGuestTurn
	_ = json.Unmarshal(got[len(got)-1], &turn)
	if turn.Conversation != "om_root" {
		t.Fatalf("root: %s", turn.Conversation)
	}
	if got := w.takeGuest(t, "om_k3", "ou_s2", "three", topicChat, "").Conversation; got != "om_k3" {
		t.Fatalf("own message: %s", got)
	}
}

func TestGuestCommandsStayTheOwners(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	for _, c := range []struct{ id, text string }{
		{"om_c1", "/topics"}, {"om_c2", "/topics off"}, {"om_c3", "/ai do it"},
		{"om_c4", "/read do it"}, {"om_c5", "/stop"},
	} {
		w.send(t, mentionEvent(c.id, "ou_s", c.text, topicChat, "omt_1", "ou_bot"))
	}
	w.send(t, botMessageEvent("om_c_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "drained", func() bool { g := w.handed(); return len(g) > 0 && g[len(g)-1] == "om_c_dm" })
	if n := len(w.a.got(assistantwire.MethodFeishuGuest)); n != 0 || len(w.settings().TopicChats) != 0 {
		t.Fatalf("a guest command was taken: turns %d, chats %d", n, len(w.settings().TopicChats))
	}
}

func TestGuestNeverTouchesOwnerRouting(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.send(t, mentionEvent("om_ai", "ou_me", "/ai remember this", topicOther, "", "ou_bot"))
	w.send(t, groupEvent("om_own", "ou_me", "mine", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "the owner's message handed", func() bool {
		for _, id := range w.handed() {
			if id == "om_own" {
				return true
			}
		}
		return false
	})
	w.allowEveryone(t, 0, topicOther, 120)

	var once sync.Once
	entered := make(chan struct{})
	release := make(chan struct{})
	w.a.on(assistantwire.MethodFeishuGuest, func(json.RawMessage) (any, error) {
		once.Do(func() { close(entered) })
		<-release
		return map[string]any{}, nil
	})
	w.send(t, mentionEvent("om_hold", "ou_s", "hold", topicOther, "omt_h", "ou_bot"))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the guest hand-over never started")
	}
	w.send(t, botMessageEvent("om_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "the owner's message handed while the guest waits", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_dm"
	})
	close(release)

	for i := 0; i < 600; i++ {
		w.send(t, mentionEvent(fmt.Sprintf("om_f%d", i), "ou_s", "flood", topicOther, fmt.Sprintf("omt_%d", i), "ou_bot"))
	}
	w.send(t, botMessageEvent("om_end", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 30*time.Second, "the flood drained", func() bool { g := w.handed(); return len(g) > 0 && g[len(g)-1] == "om_end" })

	set := w.settings()
	if set.Owner.ChatID != "oc_p2p" || len(set.TopicChats) != 1 || set.TopicChats[0].ChatID != topicChat {
		t.Fatalf("owner routing changed: chat %s, topics %+v", set.Owner.ChatID, set.TopicChats)
	}
	if src := set.aiSource("om_ai", time.Now().Add(time.Minute)); src == nil {
		t.Fatal("the owner's /ai source was evicted")
	}
	w.h.bots.mu.Lock()
	ownerKept := w.h.bots.msgChat.m["om_own"] == topicChat
	guestKept := w.h.bots.msgChat.m["om_hold"] != ""
	queued := len(w.h.bots.queuedMessages)
	w.h.bots.mu.Unlock()
	if !ownerKept || guestKept || queued != 0 {
		t.Fatalf("msgChat owner %v guest %v queued %d", ownerKept, guestKept, queued)
	}
}

func TestGuestCardPressIsRefused(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	b, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{"event_type": "card.action.trigger"},
		"event": map[string]any{"operator": map[string]any{"open_id": "ou_s"},
			"action":  map[string]any{"value": map[string]any{"a": "ix"}, "tag": "button"},
			"context": map[string]any{"open_chat_id": topicChat, "open_message_id": "om_card"}}})
	res, err := w.h.botEvent(w.ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), "owner") && !strings.Contains(string(raw), "主人") {
		t.Fatalf("card press: %s", raw)
	}
	if len(w.a.got(assistantwire.MethodFeishuAction)) != 0 {
		t.Fatal("a guest's card press reached the assistant")
	}
}

func TestGuestAuditHasNoContent(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	st := w.h.store.(*botStore)
	st.takeAudit()
	turn := w.takeGuest(t, "om_a1", "ou_s", "the secret plan", topicChat, "omt_1")
	rows := rowsWith(st.takeAudit(), auditBotGuestTriggered)
	if len(rows) != 1 || rows[0].Actor != "feishu:ou_s" {
		t.Fatalf("audit: %+v", rows)
	}
	for _, key := range []string{"chat_id", "guest_id", "mode", "in_thread"} {
		if _, ok := rows[0].Detail[key]; !ok {
			t.Fatalf("audit missing %s: %v", key, rows[0].Detail)
		}
	}
	if len(rows[0].Detail) != 4 {
		t.Fatalf("audit has extra fields: %v", rows[0].Detail)
	}
	raw, _ := json.Marshal(w.settings())
	if strings.Contains(string(raw), "secret plan") || strings.Contains(string(mustJSON(rows)), "secret plan") || turn.Question != "the secret plan" {
		t.Fatal("the question leaked into the audit or the settings")
	}
}

func TestGuestRateLimits(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	w.h.bots.now = func() time.Time { return now }
	w.allowEveryone(t, 0, topicChat, 2)
	st := w.h.store.(*botStore)
	st.takeAudit()
	w.takeGuest(t, "om_l1", "ou_s", "one", topicChat, "omt_1")
	w.takeGuest(t, "om_l2", "ou_s", "two", topicChat, "omt_2")
	before := w.api.total()
	w.send(t, mentionEvent("om_l3", "ou_s", "three", topicChat, "omt_3", "ou_bot"))
	w.send(t, botMessageEvent("om_l_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "drained", func() bool { g := w.handed(); return len(g) > 0 && g[len(g)-1] == "om_l_dm" })
	if w.api.total() == before {
		t.Fatal("the chat's hourly cap posted no note")
	}
	if rows := rowsWith(st.takeAudit(), auditBotGuestRefused); len(rows) != 1 || rows[0].Detail["reason"] != "chat" {
		t.Fatalf("refusal: %v", rows)
	}
	now = now.Add(time.Minute)
	w.send(t, mentionEvent("om_l4", "ou_s", "four", topicChat, "omt_4", "ou_bot"))
	w.send(t, botMessageEvent("om_l_dm2", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "drained", func() bool { g := w.handed(); return len(g) > 0 && g[len(g)-1] == "om_l_dm2" })
	if rows := rowsWith(st.takeAudit(), auditBotGuestRefused); len(rows) != 0 {
		t.Fatalf("a second note inside ten minutes was audited: %v", rows)
	}
}

func TestGuestReplyIsAnchoredOnce(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	turn := w.takeGuest(t, "om_p1", "ou_s", "hello", topicChat, "omt_1")
	id := string(turn.ID)
	before := w.api.total()
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"guest": id, "text": "the answer"}); err != nil {
		t.Fatal(err)
	}
	call := w.api.since(before)
	if !strings.Contains(call, "/messages/"+"om_p1"+"/reply") || !strings.Contains(call, "bg:"+id) || !strings.Contains(call, `"reply_in_thread":true`) {
		t.Fatalf("reply: %s", call)
	}
	for _, bad := range []map[string]any{
		{"guest": id, "text": "again"},
		{"guest": id, "chat_id": "oc_other", "text": "x"},
		{"guest": id, "reply_to": "om_other", "text": "x"},
		{"guest": id, "text": "x", "full_answer": true},
		{"guest": id, "text": strings.Repeat("x", botSourceTextMax+1)},
	} {
		if err := w.sendErr(assistantwire.FeishuText, bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	w.h.bots.now = func() time.Time { return time.Now().Add(botGuestReplyFor + time.Minute) }
	turn2 := w.takeGuest(t, "om_p2", "ou_s", "later", topicChat, "omt_2")
	w.h.bots.now = func() time.Time { return time.Now().Add(2*botGuestReplyFor + time.Minute) }
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"guest": string(turn2.ID), "text": "late"}); err == nil {
		t.Fatal("a reply past its half hour was accepted")
	}
}

func TestGuestReplyFollowsThePolicy(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	rev := w.allowEveryone(t, 0, topicChat, 20)
	turn := w.takeGuest(t, "om_q1", "ou_s", "hello", topicChat, "omt_1")
	if code, out := w.groupPut(t, fmt.Sprintf(`{"revision":%d,"policy":{"mode":"owner_only"}}`, rev)); code != 200 {
		t.Fatalf("reset: %d %v", code, out)
	}
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"guest": string(turn.ID), "text": "too late"}); err == nil {
		t.Fatal("a reply was accepted after the mode went back to owner only")
	}
}

func TestGuestChatIsNotASendChat(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	w.takeGuest(t, "om_s1", "ou_s", "hello", topicChat, "omt_1")
	for _, c := range []struct {
		method string
		param  map[string]any
	}{
		{assistantwire.FeishuText, map[string]any{"chat_id": topicChat, "text": "hi"}},
		{assistantwire.FeishuPost, map[string]any{"chat_id": topicChat, "card": "{}"}},
		{assistantwire.FeishuCotCreate, map[string]any{"chat_id": topicChat}},
	} {
		if err := w.sendErr(c.method, c.param); err == nil {
			t.Fatalf("%s into the guest chat was accepted", c.method)
		}
	}
	raw, _ := json.Marshal(w.feishu(t, assistantwire.FeishuStatusGet, nil))
	if strings.Contains(string(raw), topicChat) {
		t.Fatalf("status lists the guest chat: %s", raw)
	}
}

func TestGuestReplyIsSanitizedAtTheHub(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	turn := w.takeGuest(t, "om_z1", "ou_s", "hello", topicChat, "")
	before := w.api.total()
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"guest": string(turn.ID), "text": "see /Users/ada/notes and https://hub.example/inbox"}); err != nil {
		t.Fatal(err)
	}
	call := w.api.since(before)
	if !strings.Contains(call, "[path]") || !strings.Contains(call, "[link]") || strings.Contains(call, "/Users/ada") || strings.Contains(call, "hub.example") {
		t.Fatalf("not sanitized: %s", call)
	}
}

func TestGuestCotCreateIsBoundToItsSource(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.allowEveryone(t, 0, topicChat, 20)
	turn := w.takeGuest(t, "om_cot", "ou_s", "hello", topicChat, "omt_1")
	out := w.feishu(t, assistantwire.FeishuCotCreate, map[string]any{"guest": string(turn.ID)})
	if out["cot_id"] == "" || out["message_id"] == "" {
		t.Fatalf("cot: %v", out)
	}
	if err := w.sendErr(assistantwire.FeishuCotCreate, map[string]any{"guest": string(turn.ID)}); err == nil {
		t.Fatal("a second agent message was accepted")
	}
	w.h.finishGuest(string(turn.ID), "posted")
	if err := w.sendErr(assistantwire.FeishuCotCreate, map[string]any{"guest": "bg_missing"}); err == nil {
		t.Fatal("an unknown guest got an agent message")
	}
}

func TestGuestAssistantAwayEndsTheSource(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	shortInboundWait(t)
	w.allowEveryone(t, 0, topicChat, 20)
	w.a.on(assistantwire.MethodFeishuGuest, func(json.RawMessage) (any, error) { return nil, errAssistantOffline })
	w.send(t, mentionEvent("om_away", "ou_s", "hello", topicChat, "omt_1", "ou_bot"))
	w.send(t, botMessageEvent("om_away_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 8*time.Second, "the source ended", func() bool {
		set := w.settings()
		return len(set.GuestSources) == 1 && set.GuestSources[0].Ended == "failed"
	})
	set := w.settings()
	if len(set.GuestSources) != 1 || set.GuestSources[0].Ended != "failed" {
		t.Fatalf("source: %+v", set.GuestSources)
	}
	if why := set.guestLimits(topicChat, "ou_s", set.GuestSources[0].Conv, time.Now()); why == "busy" {
		t.Fatal("a failed turn still holds the thread")
	}
}
