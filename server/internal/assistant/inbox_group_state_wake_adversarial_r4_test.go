package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The source can echo Persona's direct reply after a due check has already interrupted the wait. SentOf still proves
// that this position is Persona's own send: the echo must not become another owner reply or read wake.
func TestAdversarialInboxGroupStateWakeOwnSendDuringDueCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		read bool
		done string
	}{
		{name: "replied", done: connector.DoneReplied},
		{name: "read", read: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := inboxGroupStateTestHub(t, true)
			it := inboxGroupStateTestItem(h, "event-own-send-during-check-"+tc.name, true)
			h.inbox.mu.Lock()
			// This is the persisted state POST /reply leaves: SentOf marks its send and the matter waits.
			it.Agent.SentOf = it.Last
			it.Follow = defaultInboxFollow(time.Now().UTC())
			h.inbox.setHandling(it, HandlingWaiting, "", time.Now().UTC())
			// The scheduled check has begun but its conversation has not answered yet.
			it.Follow.Checks, it.Follow.CheckAt = 1, nil
			h.inbox.tell(it, inboxNote{Name: prompts.NoteCheck, Checks: 1}, true, time.Now().UTC())
			h.inbox.setHandling(it, HandlingLooking, "", time.Now().UTC())
			h.inbox.mu.Unlock()
			if state, wake, back := handlingNow(h, it); state != HandlingLooking || !wake || back != HandlingWaiting {
				t.Fatalf("due check did not interrupt the wait: handling=%q wake=%v back=%q", state, wake, back)
			}

			h.inbox.mu.Lock()
			before := rawJSON(it)
			pending := len(it.Agent.Pending)
			h.inbox.mu.Unlock()
			report := inboxGroupStateReport(it, tc.read, tc.done)
			rest, changed := h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{report})
			if len(rest) != 0 {
				t.Fatalf("state report left as an ordinary thing: %+v", rest)
			}

			var want InboxItem
			if err := json.Unmarshal(before, &want); err != nil {
				t.Fatal(err)
			}
			if tc.read {
				want.Read = true
				if len(changed) != 1 || changed[0] != it {
					t.Fatalf("own-send read changed = %+v, want only its Read flag", changed)
				}
			} else {
				if len(changed) != 0 {
					t.Errorf("Persona's own source reply was processed as a new reply: %+v", changed)
				}
				if got := len(inboxGroupStateSelfReplies(it)); got != 0 {
					t.Errorf("Persona's own source reply recorded %d self_replied entries, want none", got)
				}
			}
			if got := string(rawJSON(it)); got != string(rawJSON(&want)) {
				t.Errorf("own-send source report changed more than allowed:\nwant %s\n got %s", rawJSON(&want), got)
			}
			if f := followOf(h, it); f == nil || f.Checks != 1 || f.CheckAt != nil {
				t.Errorf("own-send source report disturbed the active check: %+v", f)
			}
			h.inbox.mu.Lock()
			afterPending := len(it.Agent.Pending)
			h.inbox.mu.Unlock()
			if afterPending != pending {
				t.Fatalf("own-send source report queued another wake: pending %d -> %d", pending, afterPending)
			}
		})
	}
}
