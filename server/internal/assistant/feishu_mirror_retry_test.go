package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	feishuMirrorRetry   = 30 * time.Second // expected production bounds
	feishuMirrorRetries = 3
	quickMirrorRetry    = 10 * time.Millisecond
)

type mirrorWorld struct {
	f        *fakeHub
	s        *Service
	mu       sync.Mutex
	keys     []string // the uuid of each post tried
	features FeishuFeatures
}

func (w *mirrorWorld) tried() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.keys...)
}

func (w *mirrorWorld) setFeatures(f FeishuFeatures) { w.mu.Lock(); w.features = f; w.mu.Unlock() }

// mirrorRetryHub: the main conversation's turn is synced to Feishu; fail decides each post (n from 1).
func mirrorRetryHub(t *testing.T, fail func(n int) error) *mirrorWorld {
	t.Helper()
	w := &mirrorWorld{features: FeishuFeatures{Main: true, Sync: true}}
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: w.features}, nil
	})
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "早报"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "今天三件事"})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		w.mu.Lock()
		k, _ := m["uuid"].(string)
		w.keys = append(w.keys, k)
		n := len(w.keys)
		w.mu.Unlock()
		if err := fail(n); err != nil {
			return nil, err
		}
		return map[string]any{"message_id": "om_card"}, nil
	})
	w.f, w.s = f, newTestService(t, f)
	w.s.setLarkTiming("mirror_retry", quickMirrorRetry)
	return w
}

var errLost = errors.New("context deadline exceeded") // sent, the reply lost on the way back

// A morning brief whose first card's reply was lost goes again with the same key: Feishu lands a key once, so the owner does
// not see it twice.
func TestFeishuMirrorRetryKeepsItsKey(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	w := mirrorRetryHub(t, func(n int) error {
		if n == 1 {
			return errLost
		}
		return nil
	})
	w.s.feishuMirror(context.Background(), "server", "m1")
	want := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "m1"}
	// The transport records an attempt before returning the message ID needed to bind its thread.
	waitFor(t, "retried card bound to its original conversation", func() bool {
		return w.s.larkModule().Route("om_card", "") == want
	})
	keys := w.tried()
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys %q", keys)
	}
}

// One that keeps failing is tried feishuMirrorRetries more times, then given up.
func TestFeishuMirrorGivesUpAfterItsTries(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	w := mirrorRetryHub(t, func(int) error { return errLost })
	w.s.feishuMirror(context.Background(), "server", "m1")
	waitFor(t, "all tries", func() bool { return len(w.tried()) == 1+feishuMirrorRetries })
	time.Sleep(100 * time.Millisecond)
	if got := len(w.tried()); got != 1+feishuMirrorRetries {
		t.Fatalf("tried %d times", got)
	}
}

// A refusal Feishu will repeat (the card's content, a permission) is not tried again.
func TestFeishuMirrorRefusedIsNotTriedAgain(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	w := mirrorRetryHub(t, func(int) error { return &botcard.APIError{Op: "send", Code: 230002, Msg: "bot not in chat"} })
	w.s.feishuMirror(context.Background(), "server", "m1")
	time.Sleep(100 * time.Millisecond)
	if got := len(w.tried()); got != 1 {
		t.Fatalf("tried %d times", got)
	}
}

// The main conversation's mirror needs sync: with sync turned off between tries (pushes still on), it stops.
func TestFeishuMirrorRetryLooksAtItsOwnSwitch(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	w := mirrorRetryHub(t, func(int) error { return errLost })
	w.s.feishuMirror(context.Background(), "server", "m1")
	w.setFeatures(FeishuFeatures{Main: true, Pushes: true})
	time.Sleep(150 * time.Millisecond)
	if got := len(w.tried()); got != 1 {
		t.Fatalf("tried %d times after sync went off", got)
	}
}

// A newer turn meanwhile: the old answer is not sent under it.
func TestFeishuMirrorRetryDropsAnOldTurn(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	w := mirrorRetryHub(t, func(int) error { return errLost })
	w.s.feishuMirror(context.Background(), "server", "m1")
	w.f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "早报"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "今天三件事"},
		proto.Item{Role: "user", Kind: proto.KindText, Text: "再说说第二件"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "第二件是……"})
	time.Sleep(150 * time.Millisecond)
	if got := len(w.tried()); got != 1 {
		t.Fatalf("tried %d times after a newer turn", got)
	}
}

// Two runs that asked and answered alike (a task's "no change" twice within Feishu's hour) are two sends: the turn is
// in the key, so Feishu does not drop the second as a repeat.
func TestFeishuMirrorKeyIsPerTurn(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	w := mirrorRetryHub(t, func(int) error { return nil })
	for _, id := range []string{"a1", "a2"} {
		w.f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "早报"}, proto.Item{ID: id, Role: "assistant", Kind: proto.KindText, Text: "无变动"})
		w.s.feishuMirror(context.Background(), "server", "m1")
	}
	if keys := w.tried(); len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("keys %q", keys)
	}
}
