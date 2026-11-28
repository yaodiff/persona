package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// groupPut sends the whole policy and returns the status and body.
func (w *botWorld) groupPut(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	return w.do(t, "PUT", "/api/v1/bots/feishu/group-access", body)
}

func (w *botWorld) groupGet(t *testing.T) (int, map[string]any) {
	t.Helper()
	return w.do(t, "GET", "/api/v1/bots/feishu/group-access", "")
}

// mentionEvent is a group message whose structured mentions name the bot.
func mentionEvent(id, from, text, chat, thread, bot string) []byte {
	extra := map[string]any{"chat_type": "group", "chat_id": chat, "mentions": []any{
		map[string]any{"key": "@_user_1", "name": "Persona", "id": map[string]string{"open_id": bot}},
	}}
	if thread != "" {
		extra["thread_id"] = thread
	}
	return botMessageEvent(id, from, "text", textContent("@_user_1 "+text), extra)
}

func policyOf(body map[string]any) map[string]any {
	p, _ := body["policy"].(map[string]any)
	return p
}

// The default is owner only: nothing is stored, and a non-owner's @bot changes nothing.
func TestGroupAccessDefaultIsOwnerOnly(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	code, out := w.groupGet(t)
	p := policyOf(out)
	if code != 200 || p["mode"] != "owner_only" || out["revision"].(float64) != 0 || p["per_chat_hourly"].(float64) != 20 {
		t.Fatalf("default: %d %v", code, out)
	}
	for _, key := range []string{"allowed_users", "allowed_chats", "blocked_users", "blocked_chats", "guest_tools"} {
		if list, _ := p[key].([]any); len(list) != 0 {
			t.Fatalf("%s = %v, want []", key, p[key])
		}
	}
	st := w.h.store.(*botStore)
	st.takeAudit()
	before := w.api.total()
	w.send(t, mentionEvent("om_guest0", "ou_stranger", "hi", topicChat, "omt_1", "ou_bot"))
	w.send(t, botMessageEvent("om_guest_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "the owner's message handed", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_guest_dm"
	})
	if n := w.api.total(); n != before {
		t.Fatalf("a dropped message called the open platform:\n%s", w.api.since(before))
	}
	if rows := rowsWith(st.takeAudit(), auditBotGuestTriggered); len(rows) != 0 {
		t.Fatalf("a dropped message was audited: %v", rows)
	}
	if _, set := w.h.botAPI(); len(set.GuestSources) != 0 || set.GroupAccess != nil {
		t.Fatalf("a dropped message wrote settings: %+v", set.GroupAccess)
	}
}

// The blocklist wins, then the mode, then the chat, then the user.
func TestGroupAccessPrecedence(t *testing.T) {
	owner := &BotOwner{OpenID: "ou_owner"}
	access := func(mode assistantwire.FeishuGroupAccessMode) FeishuBotSettings {
		return FeishuBotSettings{Owner: owner, GroupAccess: &botGroupAccess{Mode: mode,
			AllowedUsers: []botGroupMember{{ID: "ou_ok"}}, AllowedChats: []botGroupMember{{ID: "oc_ok"}},
			BlockedUsers: []botGroupMember{{ID: "ou_bad"}}, BlockedChats: []botGroupMember{{ID: "oc_bad"}}}}
	}
	for _, c := range []struct {
		name   string
		set    FeishuBotSettings
		sender string
		chat   string
		want   string
	}{
		{"blocked user, even on the allowlist", access(assistantwire.FeishuGroupAllowlist), "ou_bad", "oc_ok", botGuestBlockedUser},
		{"allowed user in a blocked chat", access(assistantwire.FeishuGroupAllowlist), "ou_ok", "oc_bad", botGuestBlockedChat},
		{"owner only mode", access(assistantwire.FeishuGroupOwnerOnly), "ou_ok", "oc_ok", botGuestOwnerOnly},
		{"no owner", FeishuBotSettings{GroupAccess: access(assistantwire.FeishuGroupAllowlist).GroupAccess}, "ou_ok", "oc_ok", botGuestOwnerOnly},
		{"chat not allowed", access(assistantwire.FeishuGroupAllowlist), "ou_ok", "oc_other", botGuestChatNotAllowed},
		{"user not allowed", access(assistantwire.FeishuGroupAllowlist), "ou_other", "oc_ok", botGuestUserNotAllowed},
		{"allowlist, both allowed", access(assistantwire.FeishuGroupAllowlist), "ou_ok", "oc_ok", ""},
		{"everyone in an allowed chat", access(assistantwire.FeishuGroupEveryoneInAllowedChats), "ou_other", "oc_ok", ""},
		{"everyone, chat not allowed", access(assistantwire.FeishuGroupEveryoneInAllowedChats), "ou_other", "oc_other", botGuestChatNotAllowed},
		{"everyone, blocked user", access(assistantwire.FeishuGroupEveryoneInAllowedChats), "ou_bad", "oc_ok", botGuestBlockedUser},
	} {
		if got := c.set.guestAccess(c.sender, c.chat); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if !access(assistantwire.FeishuGroupAllowlist).groupChatBlocked("oc_bad") {
		t.Fatal("a blocked chat was not blocked")
	}
}

// A blocked chat silences the owner too, and unblocking restores it.
func TestGroupAccessBlockedChatSilencesTheOwnerToo(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	if code, out := w.groupPut(t, `{"revision":0,"policy":{"mode":"owner_only","blocked_chats":[{"chat_id":"`+topicChat+`"}]}}`); code != 200 {
		t.Fatalf("block: %d %v", code, out)
	}
	before := w.api.total()
	for _, ev := range [][]byte{
		groupEvent("om_b1", "ou_me", "hello", topicChat, "omt_1"),
		groupEvent("om_b2", "ou_me", "/topics off", topicChat, ""),
		groupEvent("om_b3", "ou_me", "@bot /ai do it", topicChat, "omt_1"),
		groupEvent("om_b4", "ou_me", "@bot /read do it", topicChat, "omt_1"),
	} {
		w.send(t, ev)
	}
	w.send(t, botMessageEvent("om_b_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "the line drained", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_b_dm"
	})
	if n := w.api.total(); n != before || len(w.handed()) != 1 || len(w.settings().TopicChats) != 1 {
		t.Fatalf("a blocked chat still answered: calls %d (was %d), handed %v, chats %d",
			n, before, w.handed(), len(w.settings().TopicChats))
	}
	if code, _ := w.groupPut(t, `{"revision":1,"policy":{"mode":"owner_only"}}`); code != 200 {
		t.Fatal(code)
	}
	w.send(t, groupEvent("om_b5", "ou_me", "hello again", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "unblocked message handed", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_b5"
	})
}

func TestGroupAccessPutValidation(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	if code, out := w.groupPut(t, `{"revision":0,"policy":{"mode":"allowlist","allowed_chats":[{"chat_id":"oc_x"}]}}`); code != 400 || out["code"] != "invalid_group_access" {
		t.Fatalf("unpaired: %d %v", code, out)
	}
	w.pair(t)
	many := strings.Repeat(`{"chat_id":"oc_x"},`, botGroupListMax) + `{"chat_id":"oc_last"}`
	for _, body := range []string{
		`{"revision":0,"policy":{"mode":"everyone"}}`,
		`{"revision":0,"policy":{"mode":"owner_only","blocked_chats":[` + many + `]}}`,
		`{"revision":0,"policy":{"mode":"owner_only","allowed_users":[{"open_id":"ou_a"},{"open_id":"ou_a"}]}}`,
		`{"revision":0,"policy":{"mode":"owner_only","allowed_chats":[{"chat_id":"oc bad"}]}}`,
		`{"revision":0,"policy":{"mode":"owner_only","blocked_users":[{"open_id":"ou_me"}]}}`,
		`{"revision":0,"policy":{"mode":"owner_only","guest_tools":["no_such_tool"]}}`,
		`{"revision":0,"policy":{"mode":"owner_only","guest_tools":["a","b","c","d","e","f","g","h","i"]}}`,
		`{"revision":0,"policy":{"mode":"owner_only","per_chat_hourly":121}}`,
		`not json`,
	} {
		if code, out := w.groupPut(t, body); code != 400 || out["code"] != "invalid_group_access" {
			t.Errorf("%s: %d %v", body[:40], code, out)
		}
	}
	if code, out := w.groupPut(t, `{"revision":0,"policy":{"mode":"owner_only","per_chat_hourly":0}}`); code != 200 || policyOf(out)["per_chat_hourly"].(float64) != 20 {
		t.Fatalf("hourly 0: %d %v", code, out)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/bots/feishu/group-access", strings.NewReader(`{"revision":1,"policy":{"mode":"allowlist"}}`))
	req = req.WithContext(withPrincipal(req.Context(), principal{Role: roleAnswer}))
	w.mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("answer-only: %d", rec.Code)
	}
	if code, out := w.groupPut(t, `{"revision":0,"policy":{"mode":"allowlist"}}`); code != 409 || out["code"] != "group_access_conflict" {
		t.Fatalf("stale revision: %d %v", code, out)
	}
}

// A change bumps the revision and is audited with ids and tool names, never labels. The same policy again is quiet.
func TestGroupAccessPutReplacesAndAudits(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.h.connectors = connector.NewRegistry(nil)
	if err := w.h.connectors.Add(guestToolConn{"wiki"}); err != nil {
		t.Fatal(err)
	}
	st := w.h.store.(*botStore)
	st.takeAudit()
	body := `{"revision":0,"policy":{"mode":"allowlist","allowed_users":[{"open_id":"ou_a","name":"Teammate A"}],"allowed_chats":[{"chat_id":"oc_a","name":"Project"}],"guest_tools":["wiki_search"],"per_chat_hourly":40}}`
	code, out := w.groupPut(t, body)
	if code != 200 || out["revision"].(float64) != 1 {
		t.Fatalf("put: %d %v", code, out)
	}
	rows := rowsWith(st.takeAudit(), auditBotGroupPolicy)
	if len(rows) != 1 {
		t.Fatalf("audited %d times", len(rows))
	}
	raw, _ := json.Marshal(rows[0].Detail)
	if strings.Contains(string(raw), "Teammate") || strings.Contains(string(raw), "Project") || !strings.Contains(string(raw), "wiki_search") {
		t.Fatalf("audit carried a label or dropped the tool: %s", raw)
	}
	if code, out := w.groupPut(t, strings.Replace(body, `"revision":0`, `"revision":1`, 1)); code != 200 || out["revision"].(float64) != 1 {
		t.Fatalf("identical put: %d revision %v", code, out["revision"])
	}
	if rows := rowsWith(st.takeAudit(), auditBotGroupPolicy); len(rows) != 0 {
		t.Fatalf("an identical policy was audited: %v", rows)
	}
}

// A save that fails leaves the policy as it was.
func TestGroupAccessSaveFailureRollsBack(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if code, _ := w.groupPut(t, `{"revision":0,"policy":{"mode":"owner_only","per_chat_hourly":10}}`); code != 200 {
		t.Fatal(code)
	}
	w.h.store = topicPolicyFailStore{w.h.store}
	if code, _ := w.groupPut(t, `{"revision":1,"policy":{"mode":"allowlist","per_chat_hourly":10}}`); code != 503 {
		t.Fatalf("failed save: %d, want 503", code)
	}
	if g := w.settings().GroupAccess; g == nil || g.Mode != assistantwire.FeishuGroupOwnerOnly || g.Revision != 1 {
		t.Fatalf("the failed save changed the policy: %+v", g)
	}
}

// Unpairing clears the policy and the guest sources.
func TestGroupAccessGoesWithTheOwner(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if code, _ := w.groupPut(t, `{"revision":0,"policy":{"mode":"allowlist","allowed_chats":[{"chat_id":"oc_a"}]}}`); code != 200 {
		t.Fatal(code)
	}
	w.h.bots.mu.Lock()
	w.h.bots.set.GuestSources = []botGuestSource{{ID: "bg_x", ChatID: "oc_a"}}
	_ = w.h.saveBotSettingsLocked()
	w.h.bots.mu.Unlock()
	if code, _ := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", ""); code != 200 {
		t.Fatal(code)
	}
	if set := w.settings(); set.GroupAccess != nil || len(set.GuestSources) != 0 {
		t.Fatalf("unpair kept group access: %+v %v", set.GroupAccess, set.GuestSources)
	}
}

// A new App ID unpairs, and the group access goes with the owner.
func TestGroupAccessGoesWithTheApp(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if code, _ := w.groupPut(t, `{"revision":0,"policy":{"mode":"allowlist","allowed_chats":[{"chat_id":"oc_a"}]}}`); code != 200 {
		t.Fatal(code)
	}
	w.h.bots.mu.Lock()
	w.h.bots.set.GuestSources = []botGuestSource{{ID: "bg_x", ChatID: "oc_a"}}
	_ = w.h.saveBotSettingsLocked()
	w.h.bots.mu.Unlock()
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":"cli_other","app_secret":"another-secret"}`); code != 200 {
		t.Fatal(code)
	}
	if set := w.settings(); set.GroupAccess != nil || len(set.GuestSources) != 0 {
		t.Fatalf("a new App ID kept group access: %+v %v", set.GroupAccess, set.GuestSources)
	}
}

// A refused structured @bot is remembered once, never when the sender is blocked, and costs no open-platform call.
func TestGroupAccessRecentRequests(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	before := w.api.total()
	w.send(t, mentionEvent("om_r1", "ou_stranger", "hi", topicChat, "omt_1", "ou_bot"))
	w.send(t, mentionEvent("om_r2", "ou_stranger", "hi again", topicChat, "omt_1", "ou_bot"))
	w.send(t, groupEvent("om_r3", "ou_stranger", "no mention", topicChat, "omt_1"))
	w.send(t, botMessageEvent("om_r_dm", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "the line drained", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_r_dm"
	})
	if n := w.api.total(); n != before {
		t.Fatalf("a refused message called the open platform: %s", w.api.since(before))
	}
	_, out := w.groupGet(t)
	reqs, _ := out["recent_requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("recent requests: %v", reqs)
	}
	if code, _ := w.groupPut(t, `{"revision":0,"policy":{"mode":"owner_only","blocked_users":[{"open_id":"ou_blocked"}]}}`); code != 200 {
		t.Fatal(code)
	}
	w.send(t, mentionEvent("om_r4", "ou_blocked", "hi", topicChat, "omt_1", "ou_bot"))
	w.send(t, botMessageEvent("om_r_dm2", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "the line drained", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_r_dm2"
	})
	_, out = w.groupGet(t)
	reqs, _ = out["recent_requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("a blocked sender was recorded: %v", reqs)
	}
	for i := 0; i < botGuestAsksKeep+5; i++ {
		w.send(t, mentionEvent(fmt.Sprintf("om_m%d", i), fmt.Sprintf("ou_%d", i), "hi", topicChat, "", "ou_bot"))
	}
	w.send(t, botMessageEvent("om_r_dm3", "ou_me", "text", textContent("ping"), nil))
	eventually(t, 5*time.Second, "the line drained", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == "om_r_dm3"
	})
	_, out = w.groupGet(t)
	reqs, _ = out["recent_requests"].([]any)
	if len(reqs) != botGuestAsksKeep {
		t.Fatalf("kept %d recent requests, want %d", len(reqs), botGuestAsksKeep)
	}
}

// A handoff into a chat the group access blocks is refused.
func TestHandoffRefusedInAGroupBlockedChat(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	shortInboundWait(t)
	if code, _ := w.groupPut(t, `{"revision":0,"policy":{"mode":"owner_only","blocked_chats":[{"chat_id":"oc_blocked"}]}}`); code != 200 {
		t.Fatal(code)
	}
	if code, out := w.handoff(t, `{"chat_id":"oc_blocked","question":"q"}`); code != 403 || out["code"] != "chat_blocked" {
		t.Fatalf("handoff: %d %v", code, out)
	}
}

// A read-only connector tool is accepted; one of its actions is not.
func TestGroupAccessToolsMustBeReadOnly(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.h.connectors = connector.NewRegistry(nil)
	if err := w.h.connectors.Add(guestToolConn{"wiki"}); err != nil {
		t.Fatal(err)
	}
	if code, out := w.groupPut(t, `{"revision":0,"policy":{"mode":"owner_only","guest_tools":["wiki_write"]}}`); code != 400 || out["code"] != "invalid_group_access" {
		t.Fatalf("action tool: %d %v", code, out)
	}
	if code, out := w.groupPut(t, `{"revision":0,"policy":{"mode":"owner_only","guest_tools":["wiki_search"]}}`); code != 200 {
		t.Fatalf("read tool: %d %v", code, out)
	}
}

// guestToolConn offers one read tool and one action.
type guestToolConn struct{ id string }

func (c guestToolConn) ID() string   { return c.id }
func (c guestToolConn) Name() string { return "Wiki" }
func (c guestToolConn) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapTools, connector.CapActions}}
}
func (c guestToolConn) Tools() []proto.Tool { return []proto.Tool{{Name: c.id + "_search"}} }
func (c guestToolConn) Call(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}
func (c guestToolConn) Actions() []proto.Tool { return []proto.Tool{{Name: c.id + "_write"}} }
func (c guestToolConn) Describe(context.Context, string, json.RawMessage) (connector.Action, string, error) {
	return connector.Action{}, "", nil
}
func (c guestToolConn) Do(context.Context, string, json.RawMessage) (string, error) {
	return "", errors.New("not in a test")
}
