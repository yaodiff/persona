package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// wakeSlotsBypassTestFillCapacity occupies the only programmatic wake slot with another conversation.
func wakeSlotsBypassTestFillCapacity(t *testing.T, h *inboxHub) *InboxItem {
	t.Helper()
	h.setConfig(func(c *assistantwire.InboxConfig) {
		c.Agent.Enabled = true
		c.Agent.Concurrency = 1
	})
	now := time.Now().UTC()
	busy := actionItem("wake-slots-bypass-busy", now)
	busy.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "busy-session", Of: busy.Last}
	h.inbox.mu.Lock()
	h.inbox.items[busy.ID] = busy
	h.inbox.setHandling(busy, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	return busy
}

func wakeSlotsBypassTestHandlingState(out map[string]any) string {
	handling, _ := out["handling"].(map[string]any)
	state, _ := handling["state"].(string)
	return state
}

func wakeSlotsBypassTestRequireBusy(t *testing.T, h *inboxHub, busy *InboxItem) {
	t.Helper()
	if state, _, _ := handlingNow(h, busy); state != HandlingLooking {
		t.Fatalf("the conversation occupying the slot is %q, want looking", state)
	}
}

// User actions keep their existing immediate delivery paths even when every programmatic wake slot is occupied.
// Confirming a reply and replying directly still send a non-waking notice; the other cases still ask for a turn.
func TestInboxWakeSlotsBypassUserActionsAtCapacity(t *testing.T) {
	t.Run("instruct", func(t *testing.T) {
		h, _, it := handledHub(t)
		h.inbox.mu.Lock()
		it.Agent.Pending = nil
		it.Agent.Wake = false
		h.inbox.setHandling(it, HandlingWaiting, "", time.Now())
		h.inbox.mu.Unlock()
		busy := wakeSlotsBypassTestFillCapacity(t, h)

		code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"先问时间"}`)
		if code != 200 || wakeSlotsBypassTestHandlingState(out) != HandlingLooking {
			t.Fatalf("instruct at full capacity: %d %v", code, out)
		}
		toldConv(t, h, "先问时间")
		wakeSlotsBypassTestRequireBusy(t, h, busy)
	})

	t.Run("reject", func(t *testing.T) {
		h, _, it := handledHub(t)
		if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的"}`); err != nil {
			t.Fatal(err)
		}
		busy := wakeSlotsBypassTestFillCapacity(t, h)

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"reject"}`)
		if code != 200 || wakeSlotsBypassTestHandlingState(out) != HandlingLooking {
			t.Fatalf("reject at full capacity: %d %v", code, out)
		}
		toldConv(t, h, "驳回了你的建议")
		wakeSlotsBypassTestRequireBusy(t, h, busy)
	})

	t.Run("confirm reply", func(t *testing.T) {
		h, lark, it := handledHub(t)
		if _, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四给你"}`); err != nil {
			t.Fatal(err)
		}
		busy := wakeSlotsBypassTestFillCapacity(t, h)

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != 200 || wakeSlotsBypassTestHandlingState(out) != HandlingWaiting || out["done_at"] != nil {
			t.Fatalf("confirm at full capacity: %d %v", code, out)
		}
		if len(lark.got) != 1 || lark.got[0].Text != "可以，周四给你" {
			t.Fatalf("confirmed reply sends = %+v", lark.got)
		}
		toldConv(t, h, "可以，周四给你")
		if state, wake, _ := handlingNow(h, it); state != HandlingWaiting || wake {
			t.Fatalf("confirmed reply changed its non-waking behavior: handling %q, wake %v", state, wake)
		}
		wakeSlotsBypassTestRequireBusy(t, h, busy)
	})

	t.Run("confirm handback", func(t *testing.T) {
		h, _, it := handledHub(t)
		if _, err := suggestTool(t, h, `{"kind":"reply","text":"先问张三","handback":true}`); err != nil {
			t.Fatal(err)
		}
		busy := wakeSlotsBypassTestFillCapacity(t, h)

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != 200 || wakeSlotsBypassTestHandlingState(out) != HandlingLooking {
			t.Fatalf("handback confirmation at full capacity: %d %v", code, out)
		}
		toldConv(t, h, "交回给你去做")
		wakeSlotsBypassTestRequireBusy(t, h, busy)
	})

	t.Run("confirm after handoff", func(t *testing.T) {
		h, lark, it := handoffHub(t)
		handOff(t, h, it)
		if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"好的，周四给你"}`); err != nil {
			t.Fatal(err)
		}
		busy := wakeSlotsBypassTestFillCapacity(t, h)

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != 200 || out["done_by"] != "replied" {
			t.Fatalf("handed-off confirmation at full capacity: %d %v", code, out)
		}
		if len(lark.got) != 1 || lark.got[0].Text != "好的，周四给你" {
			t.Fatalf("handed-off reply sends = %+v", lark.got)
		}
		toldMain(t, h, "已经发出")
		wakeSlotsBypassTestRequireBusy(t, h, busy)
	})

	t.Run("direct reply", func(t *testing.T) {
		h, lark, it := handledHub(t)
		h.inbox.mu.Lock()
		it.Agent.Pending = nil
		it.Agent.Wake = false
		h.inbox.setHandling(it, HandlingWaiting, "", time.Now())
		h.inbox.mu.Unlock()
		busy := wakeSlotsBypassTestFillCapacity(t, h)

		code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"我自己回了"}`)
		if code != 200 || wakeSlotsBypassTestHandlingState(out) != HandlingWaiting || out["done_at"] != nil {
			t.Fatalf("direct reply at full capacity: %d %v", code, out)
		}
		if len(lark.got) != 1 || lark.got[0].Text != "我自己回了" {
			t.Fatalf("direct reply sends = %+v", lark.got)
		}
		toldConv(t, h, "我自己回了")
		if state, wake, _ := handlingNow(h, it); state != HandlingWaiting || wake {
			t.Fatalf("direct reply changed its non-waking behavior: handling %q, wake %v", state, wake)
		}
		wakeSlotsBypassTestRequireBusy(t, h, busy)
	})
}
