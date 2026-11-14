package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A card's push carries the manager's level: with the switch on a P1 escalation is held (recorded, no alert) while a
// P0 one and a card that names no level (P0) alert.
func TestQuietCardPushHonoursTheManagersLevel(t *testing.T) {
	h, _ := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	card := func(id string) proto.Interaction {
		return proto.Interaction{ID: id, MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "git push"}
	}
	h.notifyInteractionAt("", card("ix_p1"), "box", false, assistantwire.LevelP1, "")
	if *attempts != 0 {
		t.Fatalf("a P1 escalation alerted %d times", *attempts)
	}
	view := h.push.lnInterceptsView("", 0)
	if len(view) != 1 || view[0].Reason != interceptReasonP1 || view[0].RelatedKey != "ix:ix_p1" {
		t.Fatalf("intercepts %+v, want the held card", view)
	}
	h.notifyInteractionAt("", card("ix_p0"), "box", false, assistantwire.LevelP0, "")
	h.notifyInteraction("", card("ix_plain"), "box", false)
	h.notifyInteractionAt("", card("ix_bad"), "box", false, "P9", "") // a level this hub does not know: a card interrupts
	if *attempts != 3 {
		t.Fatalf("got %d alerts for P0, plain and unknown-level cards, want 3", *attempts)
	}
}

// With the switch off every card alerts, whatever level the manager named.
func TestQuietOffCardPushIgnoresLevel(t *testing.T) {
	h, _ := lnHub(t)
	attempts := lnAttempts(h)
	h.notifyInteractionAt("", proto.Interaction{ID: "ix1", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "ls"},
		"box", false, assistantwire.LevelP1, "")
	if *attempts != 1 {
		t.Fatalf("got %d alerts, want 1", *attempts)
	}
	if view := h.push.lnInterceptsView("", 0); len(view) != 0 {
		t.Fatalf("held with the switch off: %+v", view)
	}
}

// Reply reminders merge per question, not per agent: with the switch on, one agent's second question within 12 hours
// still rings, while a repeat of the same question does not (an older manager without a key is keyed by the text).
func TestQuietReplyReminderKeyedByQuestion(t *testing.T) {
	h, _ := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	h.notifyReplyKeyed("", "m1", "box", "s1", "Agent", "Which branch?", "reply:s1@1")
	h.notifyReplyKeyed("", "m1", "box", "s1", "Agent", "Deploy now?", "reply:s1@2")
	if *attempts != 2 {
		t.Fatalf("got %d alerts for two questions of one agent, want 2", *attempts)
	}
	h.notifyReply("", "m1", "box", "s2", "Agent", "Which branch?")
	h.notifyReply("", "m1", "box", "s2", "Agent", "Deploy now?")
	if *attempts != 4 {
		t.Fatalf("got %d alerts, want 4: an older manager's two questions are two keys", *attempts)
	}
	if replyKey("s2", "Which branch?", "") == replyKey("s2", "Deploy now?", "") || replyKey("s", "x", "k") != "k" {
		t.Fatal("replyKey")
	}
}

// The switch reaches the manager over the bus, end to end: the hub's hello carries it (Hello.QuietPush), and turning
// it on or off through its route sends manager.updated, which the connected assistant takes. The manager never reads
// the hub's settings itself.
func TestQuietSwitchReachesTheManagerOverTheHello(t *testing.T) {
	h := newTestHub(t)
	h.push.store = &lnStore{settingsStore{vals: map[string][]byte{}}}
	a := connectAssistant(t, h)
	quietPush := func() bool { hello, _ := a.bh.Hello(context.Background()); return hello.QuietPush }
	if quietPush() {
		t.Fatal("the manager's hello says on before the switch was turned on")
	}
	mux := http.NewServeMux()
	h.lnRoutes(mux)
	put := func(on bool) {
		t.Helper()
		body := `{"enabled":false}`
		if on {
			body = `{"enabled":true}`
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/push/ln-mode", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
		}
	}
	put(true)
	if hello, _ := (assistantLink{h}).Hello(context.Background()); !hello.QuietPush {
		t.Fatal("the hub's hello does not carry the switch")
	}
	eventually(t, 5*time.Second, "the manager told the switch is on", quietPush)
	put(false)
	eventually(t, 5*time.Second, "the manager told the switch is off", func() bool { return !quietPush() })
}
