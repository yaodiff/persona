package larkcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"

	"github.com/nwverse/persona/server/internal/i18n"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

func testConnector(t *testing.T, scenario string) *Connector {
	return testConnectorWithProfile(t, scenario, "example")
}

func testConnectorWithProfile(t *testing.T, scenario, profile string) *Connector {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "..", "larkcli", "testdata", "fake-lark-cli.sh"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKCLI_FAKE_SCENARIO", scenario)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if scenario == "needs_refresh" || scenario == "ready" {
		now = time.Date(2026, 11, 10, 6, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	}
	return &Connector{runner: adapter.New(adapter.Config{Bin: bin, Profile: profile, Timeout: time.Second, Now: func() time.Time {
		return now
	}}), now: func() time.Time { return now }, profile: profile}
}

func TestManifestAndTools(t *testing.T) {
	c := testConnector(t, "success") // connector.Check of it: TestLarkCLIConnectorPassesTheFrameworkCheck (internal/extmgr)
	if c.ID() != "larkcli" || c.Name() != "Lark via lark-cli" {
		t.Fatalf("identity = %s / %s", c.ID(), c.Name())
	}
	var names []string
	for _, tool := range c.Tools() {
		names = append(names, tool.Name)
		if tool.Description == "" || !strings.HasPrefix(string(tool.InputSchema), `{"type":"object"`) {
			t.Fatalf("bad tool: %+v", tool)
		}
	}
	if strings.Join(names, ",") != "larkcli_chats,larkcli_messages,larkcli_search" {
		t.Fatalf("tools = %v", names)
	}
}

func TestLoginDeviceFlow(t *testing.T) {
	c := testConnector(t, "success")
	p, err := c.LoginStep(context.Background(), "", nil)
	if err != nil || p.Kind != connectorwire.PromptDeviceCode || p.Step != "device" || p.UserCode != "ABCD-1234" ||
		!strings.Contains(p.URL, "accounts.feishu.cn") {
		t.Fatalf("start prompt = %+v, err = %v", p, err)
	}
	p = waitForLogin(t, c)
	if !p.Done || p.Account != "Example User" {
		t.Fatalf("done prompt = %+v", p)
	}
}

func TestLoginPendingFailureAndRestartCancel(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv("LARKCLI_FAKE_LOGIN_RELEASE", release)
	c := testConnector(t, "login_wait")
	first, err := c.LoginStep(context.Background(), "", nil)
	if err != nil || first.UserCode != "ABCD-1234" {
		t.Fatalf("first login = %+v, err = %v", first, err)
	}
	second, err := c.LoginStep(context.Background(), "", nil)
	if err != nil || second.UserCode != "ABCD-1234" {
		t.Fatalf("restart login = %+v, err = %v", second, err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p := waitForLogin(t, c); !p.Done {
		t.Fatalf("restarted login prompt = %+v", p)
	}

	c = testConnector(t, "login_failure")
	if _, err := c.LoginStep(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	if p := waitForLogin(t, c); p.Error == "" || p.Done {
		t.Fatalf("failure prompt = %+v", p)
	}
}

func waitForLogin(t *testing.T, c *Connector) connectorwire.Prompt {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		p, err := c.LoginStep(context.Background(), "device", nil)
		if err != nil {
			t.Fatal(err)
		}
		if p.Done || p.Error != "" {
			return p
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("login did not reach a terminal state: %+v", p)
		}
	}
}

func TestStatusSuccessAndExpiryReminder(t *testing.T) {
	c := testConnector(t, "rolling")
	st := c.Status(context.Background())
	if st.State != connectorwire.StateOK || st.Account != "Example User" || st.Via != "lark-cli" {
		t.Fatalf("status = %+v", st)
	}
	if st.Error != "" {
		t.Fatalf("unexpected reminder: %q", st.Error)
	}

	c = testConnector(t, "refresh_expiring")
	st = c.Status(context.Background())
	if st.State != connectorwire.StateOK || !strings.Contains(st.Error, "1 天") ||
		!strings.Contains(st.Error, "lark-cli --profile example auth login --scope") {
		t.Fatalf("expiry status = %+v", st)
	}
}

func TestStatusRefreshWindowLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scenario  string
		state     string
		wantError string
		wantGuide bool
	}{
		{name: "healthy rolling login", scenario: "rolling", state: connectorwire.StateOK},
		{name: "refresh window nearly used", scenario: "refresh_expiring", state: connectorwire.StateOK, wantError: "1 天"},
		{name: "expired login", scenario: "expired", state: connectorwire.StateNotSignedIn, wantError: "过期", wantGuide: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testConnectorWithProfile(t, tc.scenario, "").Status(context.Background())
			if st.State != tc.state {
				t.Fatalf("state = %q, want %q; status = %+v", st.State, tc.state, st)
			}
			if tc.wantError == "" && st.Error != "" {
				t.Fatalf("unexpected error = %q", st.Error)
			}
			if tc.wantError != "" && !strings.Contains(st.Error, tc.wantError) {
				t.Fatalf("error = %q, want %q", st.Error, tc.wantError)
			}
			if tc.wantGuide != (len(st.Guide) == 1) {
				t.Fatalf("guide = %#v", st.Guide)
			}
		})
	}
}

func TestLoginGuideUsesConfiguredProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		command string
	}{
		{name: "default profile", command: "lark-cli auth login --scope"},
		{name: "named profile", profile: "example", command: "lark-cli --profile example auth login --scope"},
		{name: "profile with spaces and non ASCII", profile: "工作 profile", command: "lark-cli --profile '工作 profile' auth login --scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testConnectorWithProfile(t, "not_logged_in", tc.profile).Status(context.Background())
			if len(st.Guide) != 1 || !strings.Contains(st.Guide[0], tc.command) {
				t.Fatalf("guide = %#v, want command %q", st.Guide, tc.command)
			}
		})
	}
}

func TestStatusCategories(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scenario string
		state    string
	}{
		{"not signed in", "not_logged_in", connectorwire.StateNotSignedIn},
		{"expired", "expired", connectorwire.StateNotSignedIn},
		{"old version", "old", connectorwire.StateError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testConnector(t, tc.scenario).Status(context.Background())
			if st.State != tc.state || st.Error == "" {
				t.Fatalf("status = %+v", st)
			}
		})
	}
	c := testConnector(t, "success")
	// The CLI status fixture has no missing scopes; exercise the mapped state with
	// a runner error from a read call through the tool path below.
	t.Setenv("LARKCLI_FAKE_SCENARIO", "missing_scope")
	_, err := c.Call(context.Background(), "larkcli_search", json.RawMessage(`{"query":"example"}`))
	if adapter.KindOf(err) != adapter.KindMissingPermission {
		t.Fatalf("kind = %q, err = %v", adapter.KindOf(err), err)
	}
	if !strings.Contains(err.Error(), "im:chat:read") {
		t.Fatalf("missing scope = %v", err)
	}
	c = testConnector(t, "auth_missing_scope")
	st := c.Status(context.Background())
	if st.State != connectorwire.StateNoPermission || !strings.Contains(st.Error, "search:message") || len(st.Guide) != 1 ||
		len(st.MissingScopes) == 0 || st.MissingScopes[0] != "search:message" ||
		!strings.Contains(st.Guide[0], "lark-cli --profile example auth login --scope") ||
		st.GuideURL != "https://"+strings.Join([]string{"open", "larksuite", "com"}, ".")+"/app/cli_example/auth" {
		t.Fatalf("missing scope status = %+v", st)
	}
}

func TestStatusUsesRefreshExpiryAndReportsFeatureScopes(t *testing.T) {
	for _, scenario := range []string{"needs_refresh", "ready"} {
		st := testConnector(t, scenario).Status(context.Background())
		if st.State != connectorwire.StateNoPermission || strings.Contains(st.Error, "not signed") || len(st.Guide) != 1 ||
			!strings.Contains(st.Error, "im:chat:read") || !strings.Contains(st.Guide[0], "lark-cli --profile example auth login --scope") {
			t.Fatalf("%s status = %+v", scenario, st)
		}
	}
}

func TestStatusAcceptsExactlyReadAsMeScopesAndNamesOneGap(t *testing.T) {
	if st := testConnector(t, "success").Status(context.Background()); st.State != connectorwire.StateOK {
		t.Fatalf("all connector scopes should be enough: %+v", st)
	}
	c := testConnector(t, "auth_read_scope_missing")
	st := c.Status(context.Background())
	if st.State != connectorwire.StateNoPermission || len(st.MissingScopes) != 1 || st.MissingScopes[0] != "wiki:node:retrieve" {
		t.Fatalf("one missing connector scope = %+v", st)
	}
}

func TestReadToolsCallTheAdapter(t *testing.T) {
	c := testConnector(t, "success")
	out, err := c.Call(context.Background(), "larkcli_chats", json.RawMessage(`{"filter":"recent","limit":2}`))
	if err != nil || !strings.Contains(out, "oc_example_group") {
		t.Fatalf("chats = %s, err = %v", out, err)
	}
	out, err = c.Call(context.Background(), "larkcli_messages", json.RawMessage(`{"chat_id":"oc_example_group"}`))
	if err != nil || !strings.Contains(out, "om_example_older") {
		t.Fatalf("messages = %s, err = %v", out, err)
	}
	out, err = c.Call(context.Background(), "larkcli_search", json.RawMessage(`{"query":"example","kind":"messages"}`))
	if err != nil || !strings.Contains(out, "om_example_search") {
		t.Fatalf("search = %s, err = %v", out, err)
	}
}

func inboxTestConnector(t *testing.T, scenario string) *Connector {
	t.Helper()
	c := testConnector(t, scenario)
	c.SetInboxSource("cli", nil)
	c.SetPollInterval(time.Minute)
	return c
}

func inboxThings(t *testing.T, c *Connector) map[string]connectorwire.InboxThing {
	t.Helper()
	items, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]connectorwire.InboxThing{}
	for _, item := range items {
		out[item.Thread] = item
	}
	return out
}

func TestInboxPollKeepsRulesGroupsAndDetectsOwnReply(t *testing.T) {
	c := inboxTestConnector(t, "inbox")
	moved, err := c.Poll(context.Background())
	if err != nil || !moved {
		t.Fatalf("poll = moved=%v err=%v", moved, err)
	}
	items := inboxThings(t, c)
	if len(items) != 3 {
		t.Fatalf("items = %#v", items)
	}
	group := items["larkcli:chat:oc_example_group:thread:omt_topic_one"]
	if group.Thread == "" || !strings.Contains(group.Context, "A second line") || group.Done != connectorwire.DoneReplied {
		t.Fatalf("group item = %+v", group)
	}
	if dm := items["larkcli:chat:oc_example_dm"]; dm.Tier != "" || dm.Done != "" { // the Inbox triage tiers it
		t.Fatalf("direct item = %+v", dm)
	}
	if group.Kind != "mention" || group.Title != "Example Group" || len(group.Tags) != 2 ||
		group.Tags[0].Key != connectorwire.TagChat || group.Tags[0].Name != "Example Group" ||
		group.Tags[1].Key != connectorwire.TagSender || group.Tags[1].Value != "ou_example_peer" || group.Tags[1].Name != "Example Peer" {
		t.Fatalf("group title/tag changed = %+v", group)
	}
	// ReplyTo says where the reply goes, not which message it answers (Last does that)
	if group.ReplyTo != "由机器人（不是你本人）发到「Example Group」的这个话题里，群里的人都能看到" || group.Last == "" {
		t.Fatalf("topic reply_to = %q, last = %q", group.ReplyTo, group.Last)
	}
	if topic := items["larkcli:chat:oc_example_group:thread:omt_topic_two"]; topic.Thread == "" {
		t.Fatal("seeded topic reply was dropped")
	}
	if _, ok := items["larkcli:chat:oc_example_group:thread:omt_topic_three"]; ok {
		t.Fatal("unmentioned topic and own-only message entered the Inbox")
	}
}

func TestInboxP2PTitleUsesOtherSender(t *testing.T) {
	c := inboxTestConnector(t, "inbox_p2p")
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := inboxThings(t, c)
	dm, ok := items["larkcli:chat:oc_example_dm"]
	if !ok {
		t.Fatalf("direct item missing: %#v", items)
	}
	if dm.Kind != "dm" || dm.Title != "Example Peer" || len(dm.Tags) != 2 || dm.Tags[0].Name != "Example Peer" ||
		dm.Tags[1].Key != connectorwire.TagSender || dm.Tags[1].Value != "ou_example_peer" || dm.Tags[1].Name != "Example Peer" {
		t.Fatalf("direct title/tag = %+v", dm)
	}
	if dm.ReplyTo != "由机器人（不是你本人）发到「Example Peer」，会话里的人都能看到" {
		t.Fatalf("direct reply_to = %q", dm.ReplyTo)
	}
	if dm.Done != connectorwire.DoneReplied {
		t.Fatalf("own reply was not observed = %+v", dm)
	}
}

func TestInboxDropsBoundAppMessagesButKeepsOtherBots(t *testing.T) {
	c := inboxTestConnector(t, "inbox_bot_loop")
	moved, err := c.Poll(context.Background())
	if err != nil || !moved {
		t.Fatalf("poll = moved=%v err=%v", moved, err)
	}
	items := inboxThings(t, c)
	if _, ok := items["larkcli:chat:oc_example_bound_dm"]; ok {
		t.Fatalf("bound app message entered Inbox: %#v", items)
	}
	bot, ok := items["larkcli:chat:oc_example_other_dm"]
	if !ok || bot.Kind != "bot" || bot.Tier != "" { // triage decides, not a fixed tier
		t.Fatalf("other app bot = %+v", bot)
	}
	if bot.Tags[1].Key != connectorwire.TagSender || bot.Tags[1].Value != "cli_other" {
		t.Fatalf("other app sender tag = %+v", bot.Tags)
	}
	c.inboxMu.Lock()
	_, seen := c.inboxSeen["om_example_bound_app"]
	c.inboxMu.Unlock()
	if !seen {
		t.Fatal("bound app message was not marked seen")
	}
	moved, err = c.Poll(context.Background())
	if err != nil || moved {
		t.Fatalf("second poll = moved=%v err=%v", moved, err)
	}
}

func TestP2PTitleUsesChatPartnerWhenBatchHasOnlyOwnMessages(t *testing.T) {
	partner := json.RawMessage(`{"open_id":"ou_example_peer","name":"Example Partner","sender_type":"user"}`)
	group := &inboxGroup{chatID: "oc_example_dm", messages: []adapter.Message{{
		MessageID: "om_example_mine", ChatID: "oc_example_dm", ChatType: "p2p", Sender: adapter.Sender{ID: "ou_example_user", Name: "Example User"}, ChatPartner: partner,
	}}}
	if got := p2pTitle(group, nil, "ou_example_user"); got != "Example Partner" {
		t.Fatalf("title = %q", got)
	}
	// Only ids known: the title falls back to one, ReplyTo names no chat rather than an id
	unnamed := &inboxGroup{chatID: "oc_example_dm", messages: []adapter.Message{{
		MessageID: "om_example_mine", ChatID: "oc_example_dm", ChatType: "p2p", Sender: adapter.Sender{ID: "ou_example_user"},
		ChatPartner: json.RawMessage(`{"open_id":"ou_example_peer","sender_type":"user"}`),
	}}}
	if title, name := p2pTitle(unnamed, nil, "ou_example_user"), p2pName(unnamed, nil, "ou_example_user"); title != "ou_example_peer" || name != "" {
		t.Fatalf("title = %q, name = %q", title, name)
	}
	if got := replyTo(i18n.En, "", false); got != "sent by the bot, not as you, to this chat (everyone in it sees it)" {
		t.Fatalf("unnamed reply_to = %q", got)
	}
	if got := replyTo(i18n.En, "", true); got != "sent by the bot, not as you, into this topic (everyone in the chat sees it)" {
		t.Fatalf("unnamed topic reply_to = %q", got)
	}
}

func TestInboxCursorOverlapDedupAndPageCap(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("LARKCLI_FAKE_LOG", logPath)
	c := inboxTestConnector(t, "inbox_pages")
	c.inboxPageLimit = 1
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := inboxThings(t, c)
	if len(first) != 1 {
		t.Fatalf("first page items = %d", len(first))
	}
	state := c.inboxStateForTests()
	if state.Cursor == "" {
		t.Fatal("cursor did not advance")
	}
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := inboxThings(t, c)
	if len(second) != 1 {
		t.Fatalf("overlap or capped page duplicated items: %#v", second)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "+messages-search") < 3 { // bounded seed plus one page per round
		t.Fatalf("search calls were not paged: %q", b)
	}
}

// The cursor stays inboxCursorOverlap behind the newest message, so one search indexes late is still searched for.
func TestInboxCursorKeepsIndexLagOverlap(t *testing.T) {
	c := inboxTestConnector(t, "inbox")
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The newest search result is 12:06.
	if got, want := c.inboxStateForTests().Cursor, "2030-01-01T11:56:00Z"; got != want {
		t.Fatalf("cursor = %q, want %q", got, want)
	}
}

// A direct message search has not indexed yet comes in from the chat's own history, once; one older than the lookback,
// group chats and the chat list's order beyond inboxDirectChats are left to search.
func TestInboxDirectChatsSweepFindsWhatSearchMissed(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("LARKCLI_FAKE_LOG", logPath)
	c := inboxTestConnector(t, "inbox_lag")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	moved, err := c.Poll(context.Background())
	if err != nil || !moved {
		t.Fatalf("poll = moved=%v err=%v", moved, err)
	}
	items := inboxThings(t, c)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	dm := items["larkcli:chat:oc_lag_dm"]
	if dm.Kind != "dm" || dm.Title != "Lag Peer" || dm.Last != "om_lag_recent" || !strings.Contains(dm.Preview, "not indexed yet") {
		t.Fatalf("direct item = %+v", dm)
	}
	c.inboxMu.Lock()
	_, oldTaken := c.inboxSeen["om_lag_old"]
	_, recentTaken := c.inboxSeen["om_lag_recent"]
	c.inboxMu.Unlock()
	if oldTaken || !recentTaken {
		t.Fatalf("lookback not applied: old taken=%v, recent taken=%v", oldTaken, recentTaken)
	}
	bot := items["larkcli:chat:oc_lag_bot"]
	if bot.Kind != "bot" || bot.Title != "Approval Bot" || bot.Last != "om_lag_bot" {
		t.Fatalf("bot item = %+v", bot)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	if !strings.Contains(log, "+chat-list --types p2p --sort active_time") ||
		!strings.Contains(log, "+chat-messages-list --chat-id oc_lag_dm --start 2029-12-31T22:00:00Z") {
		t.Fatalf("direct chats were not read from history since the lookback: %q", log)
	}
	if strings.Contains(log, "--chat-id oc_lag_group --start") {
		t.Fatalf("a group chat was swept: %q", log)
	}

	// The next round reads the same history again: nothing new, nothing doubled.
	moved, err = c.Poll(context.Background())
	if err != nil || moved {
		t.Fatalf("second poll = moved=%v err=%v", moved, err)
	}
	if again := inboxThings(t, c); len(again) != 2 || again["larkcli:chat:oc_lag_dm"].Last != "om_lag_recent" {
		t.Fatalf("second round items = %#v", again)
	}
}

func TestInboxCoexistsWithGateway(t *testing.T) {
	c := inboxTestConnector(t, "inbox")
	c.SetInboxSource("", func(context.Context) bool { return true })
	moved, err := c.Poll(context.Background())
	if err != nil || moved {
		t.Fatalf("gateway-primary poll = moved=%v err=%v", moved, err)
	}
	items, _ := c.Collect(context.Background())
	if len(items) != 0 {
		t.Fatalf("CLI collected while gateway was primary: %+v", items)
	}
	c.SetInboxSource("cli", func(context.Context) bool { return true })
	moved, err = c.Poll(context.Background())
	if err != nil || !moved {
		t.Fatalf("CLI-primary poll = moved=%v err=%v", moved, err)
	}
}

func TestInboxReplyUsesBot(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("LARKCLI_FAKE_LOG", logPath)
	c := inboxTestConnector(t, "inbox")
	if err := c.Reply(context.Background(), connectorwire.InboxReply{Thread: "larkcli:chat:oc_example_group:thread:omt_topic_one", Last: "om_example_mention_one", Text: "Received", ClientID: "reply-1"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "+messages-reply --message-id om_example_mention_one --text Received --idempotency-key reply-1 --reply-in-thread --as bot --format json") {
		t.Fatalf("reply command = %q", b)
	}
}

func TestInboxReplyHonorsExplicitSender(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("LARKCLI_FAKE_LOG", logPath)
	c := inboxTestConnector(t, "inbox")
	ctx := connectorwire.WithSendIdentity(context.Background(), "user")
	if err := c.Reply(ctx, connectorwire.InboxReply{Thread: "larkcli:chat:oc_example_group", Text: "Explicit owner message"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(logPath)
	if !strings.Contains(string(raw), "--as user --format json") || strings.Contains(string(raw), "--as bot") {
		t.Fatalf("selected identity ignored: %s", raw)
	}
}

func TestInboxAllChatsOptionIsOptIn(t *testing.T) {
	c := inboxTestConnector(t, "inbox")
	var defaults InboxOptions
	if err := json.Unmarshal(c.Options(), &defaults); err != nil {
		t.Fatal(err)
	}
	if len(defaults.InboxAllChats) != 0 || len(defaults.Documents) != 0 || len(defaults.WikiSpaces) != 0 {
		t.Fatalf("default options = %+v", defaults)
	}
	if err := c.SetOptions(json.RawMessage(`{"inbox_all_chats":["oc_example_group"]}`)); err != nil {
		t.Fatal(err)
	}
	var options InboxOptions
	if err := json.Unmarshal(c.Options(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.InboxAllChats) != 1 || options.InboxAllChats[0] != "oc_example_group" || len(options.Documents) != 0 || len(options.WikiSpaces) != 0 {
		t.Fatalf("options = %+v", options)
	}
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := inboxThings(t, c)
	if _, ok := items["larkcli:chat:oc_example_group:thread:omt_topic_three"]; !ok {
		t.Fatalf("selected chat did not admit its ordinary message: %#v", items)
	}
	if err := c.SetOptions(json.RawMessage(`{"inbox_all_chats":[""]}`)); err == nil {
		t.Fatal("empty chat id accepted")
	}
}

func TestInboxWatchSignalsChanged(t *testing.T) {
	c := inboxTestConnector(t, "inbox")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var scope connectorwire.Scope
	err := c.Watch(ctx, func(got connectorwire.Scope) {
		scope = got
		cancel()
	})
	if err != nil || scope != connectorwire.ScopeInbox {
		t.Fatalf("watch = scope=%q err=%v", scope, err)
	}
}

type inboxStateStore struct{ raw json.RawMessage }

func (s *inboxStateStore) Setting(_ context.Context, _ string, out any) (bool, error) {
	if len(s.raw) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(s.raw, out)
}

func (s *inboxStateStore) PutSetting(_ context.Context, _ string, value any) error {
	b, err := json.Marshal(value)
	s.raw = b
	return err
}

func TestInboxCursorAndTopicsPersistInStore(t *testing.T) {
	store := &inboxStateStore{}
	c1 := inboxTestConnector(t, "inbox")
	c1.SetStateStore(store)
	if _, err := c1.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := c1.inboxStateForTests()
	if state.Cursor == "" || len(state.Topics) == 0 {
		t.Fatalf("state was not saved: %+v", state)
	}
	c2 := inboxTestConnector(t, "inbox")
	c2.SetStateStore(store)
	if _, err := c2.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded := c2.inboxStateForTests()
	if loaded.Cursor == "" || len(loaded.Topics) == 0 || !loaded.TopicsSeeded {
		t.Fatalf("state was not restored: %+v", loaded)
	}
}

func TestMessageTimeReadsLarkCLIWallClockMinutes(t *testing.T) {
	// lark-cli prints "2006-01-02 15:04" in the local time zone; RFC 3339 and Unix times still work.
	want := time.Date(2030, 1, 1, 15, 14, 0, 0, time.Local).UTC()
	if got := messageTime(adapter.Message{CreateTime: "2030-01-01 15:14"}); !got.Equal(want) {
		t.Fatalf("wall-clock minute = %v, want %v", got, want)
	}
	if got := messageTime(adapter.Message{CreateTime: "2030-01-01 15:14:30"}); !got.Equal(want.Add(30 * time.Second)) {
		t.Fatalf("wall-clock second = %v", got)
	}
	if got := messageTime(adapter.Message{CreateTime: "2030-01-01T12:00:00Z"}); !got.Equal(time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("RFC 3339 = %v", got)
	}
	if got := messageTime(adapter.Message{CreateTime: "not a time"}); !got.IsZero() {
		t.Fatalf("garbage = %v", got)
	}
}

func TestCardTextReadsAsPlainText(t *testing.T) {
	in := "<card title=\"Merge Request Merged\">\n  Example change was merged\n</card>"
	if got := cardText(in); got != "Merge Request Merged\nExample change was merged" {
		t.Fatalf("card = %q", got)
	}
	if got := cardText("<card>\n:OK:\n</card>"); got != ":OK:" {
		t.Fatalf("untitled card = %q", got)
	}
	if got := cardText("plain <b>text</b>"); got != "plain <b>text</b>" {
		t.Fatalf("plain text changed: %q", got)
	}
}
