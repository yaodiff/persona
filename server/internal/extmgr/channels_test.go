package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeChannel is a channel plugin's program as the hub's pipeline reaches it: what was sent, typing on and off.
type fakeChannel struct {
	mu          sync.Mutex
	sent        []channel.SendParams
	typing      []bool
	refused     bool   // cannot_send: nowhere to send to yet
	refuseMsg   string // optional custom cannot_send explanation
	sendAttempt int
}

func (f *fakeChannel) call(_ context.Context, id, method string, params, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case channel.MSend:
		f.sendAttempt++
		if f.refused {
			if f.refuseMsg != "" {
				return &rpc.Error{Code: channel.CodeCannotSend, Message: f.refuseMsg}
			}
			return channel.ErrCannotSend
		}
		p := params.(channel.SendParams)
		for _, s := range f.sent {
			if s.Ref == p.Ref {
				return nil // once by its ref
			}
		}
		f.sent = append(f.sent, p)
	case channel.MTyping:
		f.typing = append(f.typing, params.(channel.TypingParams).On)
	}
	return nil
}

func (f *fakeChannel) find(t *testing.T, want string) channel.SendParams {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, s := range f.sent {
			if strings.Contains(s.Text, want) {
				f.mu.Unlock()
				return s
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("nothing sent with %q; sent %+v", want, f.sent)
	return channel.SendParams{}
}

func (f *fakeChannel) count(want string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sent {
		if strings.Contains(s.Text, want) {
			n++
		}
	}
	return n
}

// The connectors' secrets, kept with the settings (prefixed).
func (s *botStore) ConnectorSecret(_ context.Context, id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.vals["secret:"+id]
	return string(b), ok, nil
}

func (s *botStore) PutConnectorSecret(_ context.Context, id, sealed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vals["secret:"+id] = []byte(sealed)
	return nil
}

func (s *botStore) DeleteConnectorSecret(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vals, "secret:"+id)
	return nil
}

// exampleChannel is a third-party chat app's bot, as a plugin contributes it: plain text, answers in text.
var exampleChannel = channel.Item{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM,
	Capabilities: channel.Capabilities{ImagesIn: true, Format: channel.FormatPlain, MaxText: 400, Asks: channel.AsksText, Push: true, Typing: true}}

type channelWorld struct {
	*botWorld
	ch *fakeChannel
}

// newChannelWorld is the bot world with a plugin contributing examplechat, connected.
func newChannelWorld(t *testing.T) *channelWorld {
	t.Helper()
	w := newBotWorld(t)
	raw, _ := json.Marshal([]channel.Item{exampleChannel})
	m := &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: "example-chat", Name: "Example Chat", Version: "1.0.0",
		Engines: extension.Engines{ExtensionProtocol: extension.Protocol}, ActivationEvents: []string{extension.OnStartup},
		Runtime: &extension.Runtime{Command: extension.Command{Path: "example-chat"}}, Contributes: map[string]json.RawMessage{"channel": raw}}
	reg := extension.NewRegistry(t.TempDir(), nil, nil, 0)
	if err := reg.AddBuiltin(m, "/opt/example", true); err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.h.extensions = newExtensions(t.TempDir(), nil, w.h.logf)
	w.h.extensions.reg = reg
	ch := &fakeChannel{}
	w.h.channels.call = ch.call
	w.h.channels.statuses = map[string]*channel.StatusParams{"examplechat": {State: channel.StateConnected, Account: &channel.Account{Name: "示例账号"}}}
	return &channelWorld{botWorld: w, ch: ch}
}

func (w *channelWorld) receive(t *testing.T, id, text string) {
	t.Helper()
	p, _ := json.Marshal(channel.ReceiveParams{Contribution: "examplechat", Message: channel.Message{ID: id, Text: text}})
	if _, err := w.h.extensionChannelCall("example-chat", channel.MReceive, p); err != nil {
		t.Fatal(err)
	}
}

// received are the channel's messages handed to the assistant (channels.receive).
func (w *channelWorld) received() []assistantwire.ChannelReceive {
	var out []assistantwire.ChannelReceive
	for _, p := range w.a.got(assistantwire.MethodChannelReceive) {
		var r assistantwire.ChannelReceive
		_ = json.Unmarshal(p, &r)
		out = append(out, r)
	}
	return out
}

// channels is one of the assistant's app.call channels.*.
func (w *channelWorld) channels(t *testing.T, method string, params any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	out, ok, err := w.h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppChannels, Method: method, Params: raw})
	if !ok {
		t.Fatalf("%s: not handled", method)
	}
	res := map[string]any{}
	_ = json.Unmarshal(out, &res)
	return res, err
}

// A message from a plugin's channel goes to the assistant as the program gave it, with the owner's action; what the
// assistant sends back goes through channel.send in the pieces it gives (the same ref once), "typing" on and off.
func TestChannelMessageGoesToTheAssistantAndTheAnswerComesBack(t *testing.T) {
	w := newChannelWorld(t)
	w.receive(t, "m1", "明天有什么会？")
	eventually(t, 5*time.Second, "the message handed to the assistant", func() bool { return len(w.received()) == 1 })
	r := w.received()[0]
	var msg channel.Message
	_ = json.Unmarshal(r.Message, &msg)
	if r.Channel != "examplechat" || msg.ID != "m1" || msg.Text != "明天有什么会？" || r.Action == "" {
		t.Fatalf("handed %+v %+v", r, msg)
	}
	if _, err := w.channels(t, assistantwire.ChannelsTyping, map[string]any{"channel": "examplechat", "on": true}); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"明天两个会。", "十点周会。"} {
		p := map[string]any{"channel": "examplechat", "ref": fmt.Sprintf("ans-%d", i), "text": text, "kind": channel.OutAnswer, "reply_to": "m1"}
		if _, err := w.channels(t, assistantwire.ChannelsSend, p); err != nil {
			t.Fatal(err)
		}
		if _, err := w.channels(t, assistantwire.ChannelsSend, p); err != nil { // sent again with its ref: once
			t.Fatal(err)
		}
	}
	if _, err := w.channels(t, assistantwire.ChannelsTyping, map[string]any{"channel": "examplechat", "on": false}); err != nil {
		t.Fatal(err)
	}
	first := w.ch.find(t, "明天两个会")
	if first.Kind != channel.OutAnswer || first.ReplyTo != "m1" {
		t.Fatalf("answer %+v", first)
	}
	w.ch.mu.Lock()
	typing := append([]bool(nil), w.ch.typing...)
	pieces := len(w.ch.sent)
	w.ch.mu.Unlock()
	if len(typing) != 2 || !typing[0] || typing[1] || pieces != 2 {
		t.Fatalf("typing %v, %d pieces", typing, pieces)
	}
}

// Nowhere to send to yet (cannot_send): the assistant is told so, by its code, at once, preserving the explanation.
func TestChannelCannotSendSaysSo(t *testing.T) {
	w := newChannelWorld(t)
	w.ch.refused = true
	_, err := w.channels(t, assistantwire.ChannelsSend, map[string]any{"channel": "examplechat", "ref": "x", "text": "hi"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeCannotSend {
		t.Fatalf("cannot send: %v", err)
	}
	w.ch.sendAttempt = 0
	w.ch.refuseMsg = "This channel only replies to incoming messages."
	_, err = w.channels(t, assistantwire.ChannelsSend, map[string]any{"channel": "examplechat", "ref": "x2", "text": "hi"})
	if !errors.As(err, &re) || re.Code != assistantwire.CodeCannotSend || re.Message != w.ch.refuseMsg {
		t.Fatalf("cannot send with message: %v", err)
	}
	if w.ch.sendAttempt != 1 {
		t.Fatalf("cannot_send retried %d times, want 1 attempt", w.ch.sendAttempt)
	}
}

// A channel declaring capabilities.reply_only exposes it in channels.list.
func TestChannelListExposesReplyOnlyCapability(t *testing.T) {
	w := newBotWorld(t)
	item := channel.Item{
		ID:           "replybot",
		Name:         "Reply Bot",
		Kind:         channel.KindIM,
		Capabilities: channel.Capabilities{Format: channel.FormatPlain, ReplyOnly: true},
	}
	raw, _ := json.Marshal([]channel.Item{item})
	m := &extension.Manifest{
		ManifestVersion:  extension.ManifestVersion,
		ID:               "reply-bot",
		Name:             "Reply Bot",
		Version:          "1.0.0",
		Engines:          extension.Engines{ExtensionProtocol: extension.Protocol},
		ActivationEvents: []string{extension.OnStartup},
		Runtime:          &extension.Runtime{Command: extension.Command{Path: "reply-bot"}},
		Contributes:      map[string]json.RawMessage{"channel": raw},
	}
	reg := extension.NewRegistry(t.TempDir(), nil, nil, 0)
	if err := reg.AddBuiltin(m, "/opt/reply-bot", true); err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.h.extensions = newExtensions(t.TempDir(), nil, w.h.logf)
	w.h.extensions.reg = reg
	w.h.channels.call = (&fakeChannel{}).call
	w.h.channels.statuses = map[string]*channel.StatusParams{
		"replybot": {State: channel.StateConnected, Account: &channel.Account{Name: "owner"}},
	}
	out, ok, err := w.h.AppCall(context.Background(), assistantwire.AppCall{
		AppID:  assistantwire.AppChannels,
		Method: assistantwire.ChannelsList,
		Params: json.RawMessage(`{}`),
	})
	if !ok || err != nil {
		t.Fatalf("channels.list: ok=%v err=%v", ok, err)
	}
	var res struct {
		Channels []channel.View `json:"channels"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	var found *channel.View
	for i := range res.Channels {
		if res.Channels[i].ID == "replybot" {
			found = &res.Channels[i]
			break
		}
	}
	if found == nil || !found.Capabilities.ReplyOnly || found.State != channel.StateConnected {
		t.Fatalf("replybot view = %+v", found)
	}
}

// channels.list: the apps first, then the connected, the rest; a plugin's channel with what its program said.
func TestChannelList(t *testing.T) {
	w := newChannelWorld(t)
	out, err := w.channels(t, assistantwire.ChannelsList, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := out["channels"].([]any)
	byID := map[string]map[string]any{}
	var order []string
	for _, c := range list {
		m := c.(map[string]any)
		byID[m["id"].(string)] = m
		order = append(order, m["id"].(string))
	}
	if order[0] != "app" {
		t.Fatalf("order %v", order)
	}
	ex := byID["examplechat"]
	if ex == nil || ex["state"] != "connected" || ex["source"] != "plugin" || ex["plugin"] != "example-chat" ||
		ex["account"].(map[string]any)["name"] != "示例账号" || ex["capabilities"].(map[string]any)["asks"] != "text" {
		t.Fatalf("examplechat %v", ex)
	}
	if fb := byID["feishu"]; fb == nil || fb["state"] == "connected" || fb["bind"] != "fields" { // not paired
		t.Fatalf("feishu %v", fb)
	}
	if byID["lark_agent"] != nil {
		t.Fatal("the hub's own /agent channel is a plugin's now")
	}
}

// The WeChat program's state from before the pipeline: where its messages went, its turns and asks move to the
// channels' state (the assistant's keys, before it starts); its token stays its own.
func TestWeChatStateMoves(t *testing.T) {
	h := newTestManager(t)
	st := newBotStore()
	h.store = st
	ctx := context.Background()
	sec := h.secretsFor("wechat")
	old := `{"creds":{"token":"BT"},"cursor":"c9","context":"ctx","target":{"machine":"box","session":"conv-2"},` +
		`"runs":[{"machine":"box","session":"main-1","base":10,"msg_ids":["wechat-message:1"],"started":"2026-11-04T09:00:00Z"}],` +
		`"asks":[{"id":"ix9","machine":"box","session":"main-1","kind":"permission","at":"2026-11-04T09:00:00Z"}]}`
	if err := sec.Save(ctx, []byte(old)); err != nil {
		t.Fatal(err)
	}
	_ = st.PutSetting(ctx, channelsKey, map[string]any{"examplechat": map[string]any{"features": map[string]bool{"sync": true}}})
	h.migrateWeChatState(ctx)
	data, _ := sec.Load(ctx)
	var left map[string]any
	_ = json.Unmarshal(data, &left)
	if left["target"] != nil || left["runs"] != nil || left["asks"] != nil || left["cursor"] != "c9" || left["creds"] == nil {
		t.Fatalf("left %s", data)
	}
	var state struct {
		Runs []channel.Run `json:"runs"`
		Asks []channel.Ask `json:"asks"`
	}
	var prefs map[string]struct {
		Target   *channel.Target `json:"target"`
		Features map[string]bool `json:"features"`
	}
	read := func() {
		state.Runs, state.Asks, prefs = nil, nil, nil
		_, _ = st.Setting(ctx, channelsStateKey, &state)
		_, _ = st.Setting(ctx, channelsKey, &prefs)
	}
	read()
	if len(state.Runs) != 1 || state.Runs[0].SessionID != "main-1" || state.Runs[0].MsgIDs[0] != "wechat-message:1" || len(state.Asks) != 1 {
		t.Fatalf("state %+v", state)
	}
	if tg := prefs["wechat"].Target; tg == nil || tg.Kind != channel.TargetConversation || tg.SessionID != "conv-2" || !prefs["examplechat"].Features["sync"] {
		t.Fatalf("prefs %+v", prefs)
	}
	h.migrateWeChatState(ctx) // once
	read()
	if len(state.Runs) != 1 {
		t.Fatalf("%d runs after a second move", len(state.Runs))
	}
}

// While the assistant can't take it a channel's message is answered that it can't be reached (once, however often
// the chat app delivers it); back, the channel's messages go to it again.
func TestChannelSaysTheAssistantIsAwayAndRecovers(t *testing.T) {
	w := newChannelWorld(t)
	shortInboundWait(t)
	w.away(true)
	w.receive(t, "a1", "在吗？")
	w.receive(t, "a1", "在吗？")
	away := w.ch.find(t, "助手暂时连不上")
	if away.ReplyTo != "a1" || away.Kind != channel.OutNote {
		t.Fatalf("note %+v", away)
	}
	time.Sleep(500 * time.Millisecond)
	if n := w.ch.count("助手暂时连不上"); n != 1 {
		t.Fatalf("told %d times", n)
	}
	w.away(false)
	w.receive(t, "a2", "回来了吗？")
	eventually(t, 5*time.Second, "handed once it is back", func() bool {
		got := w.received()
		if len(got) == 0 {
			return false
		}
		var m channel.Message
		_ = json.Unmarshal(got[len(got)-1].Message, &m)
		return m.ID == "a2"
	})
}

// In the user's language: the channel list the assistant shows (channels.list).
func TestChannelsInTheUsersLanguage(t *testing.T) {
	w := newChannelWorld(t)
	w.h.opts.Lang = func() i18n.Lang { return i18n.En }
	out, _ := w.channels(t, assistantwire.ChannelsList, nil)
	var feishu, app map[string]any
	for _, c := range out["channels"].([]any) {
		switch m := c.(map[string]any); m["id"] {
		case "feishu":
			feishu = m
		case "app":
			app = m
		}
	}
	if feishu["name"] != "Lark bot" || !strings.HasPrefix(app["description"].(string), "Persona's own apps") {
		t.Fatalf("feishu %v, app %v", feishu, app)
	}
}

func TestChannelReceivePreservesGroupTopicRoutingFields(t *testing.T) {
	w := newChannelWorld(t)
	p, _ := json.Marshal(channel.ReceiveParams{
		Contribution: "examplechat",
		Message: channel.Message{
			ID:       "m_topic",
			Text:     "group topic message",
			ChatID:   "oc_group_1",
			ChatKind: channel.ChatKindTopic,
			TopicID:  "omt_99",
			Thread:   "oc_group_1:omt_99",
		},
	})
	if _, err := w.h.extensionChannelCall("example-chat", channel.MReceive, p); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "message with routing fields handed to assistant", func() bool { return len(w.received()) == 1 })
	var got channel.Message
	if err := json.Unmarshal(w.received()[0].Message, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "m_topic" || got.ChatID != "oc_group_1" || got.ChatKind != channel.ChatKindTopic || got.TopicID != "omt_99" || got.Thread != "oc_group_1:omt_99" {
		t.Fatalf("forwarded message = %+v", got)
	}
}

func TestChannelReceiveIgnoresUnknownChatKind(t *testing.T) {
	w := newChannelWorld(t)
	p := json.RawMessage(`{"contribution":"examplechat","message":{"id":"future","chat_kind":"future_kind","chat_id":"chat","thread":"legacy"}}`)
	if _, err := w.h.extensionChannelCall("example-chat", channel.MReceive, p); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "message handed to assistant", func() bool { return len(w.received()) == 1 })
	var got channel.Message
	if err := json.Unmarshal(w.received()[0].Message, &got); err != nil {
		t.Fatal(err)
	}
	if got.ChatKind != "" || got.Thread != "legacy" || got.ChatID != "chat" {
		t.Fatalf("forwarded %+v", got)
	}
}
