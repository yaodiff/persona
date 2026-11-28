package larkcli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

func TestOwnerPromiseSelectedGroupGraceAndRestart(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	request := adapter.Message{
		MessageID: "om_example_request", ThreadID: "omt_example_topic", ChatID: "oc_example_group",
		ChatType: "group", MsgType: "text", CreateTime: base.Add(time.Minute).Format(time.RFC3339),
		Sender:   adapter.Sender{ID: "ou_example_peer", SenderType: "user"},
		Mentions: []adapter.Mention{{ID: "ou_example_user"}},
		Content:  json.RawMessage(`"Can you check the example import?"`),
	}
	owner := request
	owner.MessageID, owner.ParentID = "om_example_owner", request.MessageID
	owner.CreateTime, owner.Sender.ID = base.Add(2*time.Minute).Format(time.RFC3339), "ou_example_user"
	owner.Content, owner.Mentions = json.RawMessage(`"I've asked my AI to check it."`), nil
	polite := owner
	polite.MessageID, polite.Content = "om_example_polite", json.RawMessage(`"Thanks!"`)
	unselected := owner
	unselected.MessageID, unselected.ChatID = "om_example_unselected", "oc_example_other"
	private := owner
	private.MessageID, private.ChatType = "om_example_private", "p2p"
	bot := owner
	bot.MessageID, bot.Sender.SenderType = "om_example_bot", "app"
	old := owner
	old.MessageID, old.CreateTime = "om_example_old", base.Add(-time.Minute).Format(time.RFC3339)
	history := []adapter.Message{owner, request}
	search := []adapter.Message{owner, polite, unselected, private, bot, old, request}
	store := &inboxStateStore{}
	options := json.RawMessage(`{"owner_promise_groups":["oc_example_group"],"owner_promise_model":true}`)
	poll := func(c *Connector) {
		t.Helper()
		if _, err := c.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertNoCandidate := func(c *Connector) {
		t.Helper()
		for _, thing := range inboxThings(t, c) {
			if thing.OwnerPromise != nil {
				t.Fatalf("unexpected observation: %+v", thing)
			}
		}
	}
	c := followupConnector(t, search, history)
	c.now = func() time.Time { return now }
	c.SetStateStore(store)
	poll(c)
	assertNoCandidate(c)
	if err := c.SetOptions(options); err != nil {
		t.Fatal(err)
	}
	poll(c) // first opted-in round establishes a baseline without replaying history
	now = base.Add(3 * time.Minute)
	poll(c)
	assertNoCandidate(c)

	c = followupConnector(t, search, history)
	c.now = func() time.Time { return now }
	c.SetStateStore(store)
	if err := c.SetOptions(options); err != nil {
		t.Fatal(err)
	}
	now = base.Add(17 * time.Minute)
	poll(c)
	var candidate connectorwire.InboxThing
	n := 0
	for _, thing := range inboxThings(t, c) {
		if thing.OwnerPromise != nil {
			candidate = thing
			n++
		}
	}
	if n != 1 || candidate.Last != owner.MessageID || string(candidate.OwnerPromise.RequestID) != request.MessageID ||
		!candidate.OwnerPromise.Model || !strings.Contains(candidate.OpenURL, "open_thread_id=omt_example_topic") ||
		candidate.CanReply {
		t.Fatalf("wanted one private delayed observation: %+v (%d)", candidate, n)
	}
	if oldRequest := inboxThings(t, c)["larkcli:chat:oc_example_group:thread:omt_example_topic"]; oldRequest.Done != connectorwire.DoneReplied {
		t.Fatalf("ordinary Inbox should still record the reply: %+v", oldRequest)
	}
	if err := c.SetOptions(json.RawMessage(`{"owner_promise_groups":[]}`)); err != nil {
		t.Fatal(err)
	}
	if after := inboxThings(t, c)[candidate.Thread]; after.Done != connectorwire.DoneRead || after.Preview != "" {
		t.Fatalf("removed group should retire its reminder: %+v", after)
	}
}
