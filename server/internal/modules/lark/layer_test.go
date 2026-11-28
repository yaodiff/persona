package lark

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
)

// outboundHost records the outbound port's calls the layer hands to the core; the rest is the fake core's.
type outboundHost struct {
	*Service
	calls []any
}

func (h *outboundHost) Post(_ context.Context, to assistantwire.ChannelTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	h.calls = append(h.calls, []any{"post", to, replyTo, card, fallback, key})
	return "om_1", nil
}

func (h *outboundHost) Note(_ context.Context, to assistantwire.ChannelTarget, replyTo, text string) {
	h.calls = append(h.calls, []any{"note", to, replyTo, text})
}

func (h *outboundHost) UpdateCard(_ context.Context, messageID string, card *botcard.Card) error {
	h.calls = append(h.calls, []any{"update", messageID, card})
	return nil
}

func (h *outboundHost) StartGroupGuest(context.Context, string, string, string, []string) (string, string, error) {
	return "", "", nil
}

func (h *outboundHost) SendGroupGuest(context.Context, string, string, string, string) error {
	return nil
}

// The layer's outbound port hands each send to the core's host unchanged and gives back what it answers; the bot's
// status and switches are the chat's own, read from the hub (a to-do's topic lines are the module's own too,
// thread_posts_test.go).
func TestLayerForwardsTheOutboundPort(t *testing.T) {
	f := newFakeHub()
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return assistantwire.FeishuStatus{Ready: true, Owner: "ou_1", Features: assistantwire.FeishuFeatures{Reports: true}}, nil
	})
	f.answerApp(AppFeishu, FeishuText, func(json.RawMessage) (any, error) { return nil, errors.New("refused") })
	h := &outboundHost{Service: newTestService(t, f)}
	l := New(h)
	ctx := context.Background()
	card := &botcard.Card{}
	to := assistantwire.ChannelTarget{ChatID: "oc_1", InThread: true, NoDetour: true}
	if st, err := l.Status(ctx); err != nil || !st.Ready || st.Owner != "ou_1" {
		t.Fatalf("status %+v %v", st, err)
	}
	if f, ready := l.Features(ctx); !ready || !f.Reports {
		t.Fatalf("features %+v %v", f, ready)
	}
	if mid, err := l.Post(ctx, to, "om_0", card, "fb", "k1"); mid != "om_1" || err != nil {
		t.Fatalf("post %q %v", mid, err)
	}
	l.Note(ctx, to, "om_2", "a note")
	if err := l.Reply(ctx, to, "om_3", "a reply", "k2", true); err == nil || err.Error() != "refused" {
		t.Fatalf("reply %v", err)
	}
	got := f.appParams(AppFeishu, FeishuText)
	if len(got) != 1 || got[0]["reply_to"] != "om_3" || got[0]["text"] != "a reply" || got[0]["uuid"] != "k2" || got[0]["full_answer"] != true ||
		got[0]["chat_id"] != "oc_1" || got[0]["in_thread"] != true || got[0]["no_detour"] != true {
		t.Fatalf("the reply went to the hub as %v", got)
	}
	if err := l.UpdateCard(ctx, "om_4", card); err != nil {
		t.Fatal(err)
	}
	if n := len(f.appParams(AppFeishu, FeishuStatusGet)); n != 2 {
		t.Fatalf("the status was read %d times from the hub, want 2", n)
	}
	want := []any{[]any{"post", to, "om_0", card, "fb", "k1"}, []any{"note", to, "om_2", "a note"},
		[]any{"update", "om_4", card}}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls %v, want %v", h.calls, want)
	}
}
