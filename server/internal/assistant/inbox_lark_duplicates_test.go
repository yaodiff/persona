package assistant

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

func pairedLarkMentions() (*InboxItem, *InboxItem) {
	at := time.Now().UTC().Truncate(time.Minute)
	const first = "@Alex Could you check the upload report?"
	const second = "The second line identifies which report."
	cli := &InboxItem{
		ID: "larkcli:1", Source: "larkcli", Kind: "mention", Title: "Project group", Sender: "Jamie",
		Last: "om_test", Preview: first + "[害羞]", UpdatedAt: at, Tier: TierAction, Count: 4,
		Context: "Preamble" + larkCurrentMessages + "[" + at.Format(time.RFC3339) + "] Jamie（@了我）: " +
			first + "[害羞]\n" + second + larkBackground + "Earlier activity from only this source.",
		Connector: &InboxConnector{Thread: "cli-topic", CanReply: true, Tags: []connector.Tag{
			{Key: connector.TagChat, Value: "oc_test", Name: "Project group"},
			{Key: connector.TagSender, Value: "ou_test", Name: "Jamie"},
		}},
	}
	other := &InboxItem{
		ID: "larkx:1", Source: "larkx", Kind: "mention", Title: "Project group", Sender: "Jamie",
		Last: "1234", Preview: first + "[表情]\n" + second,
		Context: "Jamie: " + first + "[表情]\n" + second, UpdatedAt: at.Add(53 * time.Second), Tier: TierAction, Count: 3,
		Connector: &InboxConnector{Thread: "5678", CanReply: true, Tags: []connector.Tag{
			{Key: connector.TagChat, Value: "5678", Name: "Project group"},
			{Key: connector.TagSender, Value: "9012", Name: "Jamie"},
		}},
	}
	return cli, other
}

type larkDuplicateReplyRecorder struct {
	replierConnector
	id string
}

func (c *larkDuplicateReplyRecorder) ID() string { return c.id }

func TestInboxLarkDuplicateListDetailAndReply(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		cli, other := pairedLarkMentions()
		first, second := cli, other
		if reverse {
			first, second = second, first
		}
		h := memoInbox(t, nil)
		h.inbox.mu.Lock()
		h.inbox.items[first.ID] = first
		h.inbox.mu.Unlock()
		cliReply := &larkDuplicateReplyRecorder{id: cli.Source}
		otherReply := &larkDuplicateReplyRecorder{id: other.Source}
		h.addConnector(cliReply, false)
		h.addConnector(otherReply, true)

		before := getSince(t, h, "0", true)
		h.inbox.mu.Lock()
		h.inbox.items[second.ID] = second
		h.inbox.mu.Unlock()
		after := getSince(t, h, before.Cursor, true)
		if rows := byID(t, after.Items); len(rows) != 1 || !strings.Contains(rows[other.ID], cli.ID) {
			t.Fatalf("second arrival did not produce a single survivor with a link: %v", rows)
		}
		if !reverse && (len(after.Removed) != 1 || after.Removed[0].ID != cli.ID) {
			t.Fatalf("delta did not remove CLI copy: %+v", after.Removed)
		}
		if rows := h.inboxList(true); len(rows) != 1 || rows[0].ID != other.ID {
			t.Fatalf("non-memo list did not select the same row: %+v", rows)
		}
		code, detail := inboxCall(t, h.routes(), http.MethodGet, "/api/v1/inbox/"+other.ID, "")
		related, ok := detail["related"].([]any)
		if code != http.StatusOK || !ok || len(related) != 1 || related[0].(map[string]any)["id"] != cli.ID {
			t.Fatalf("related history is missing from the preferred detail: status=%d, detail=%+v", code, detail)
		}
		code, detail = inboxCall(t, h.routes(), http.MethodGet, "/api/v1/inbox/"+cli.ID, "")
		if code != http.StatusOK || detail["id"] != cli.ID || detail["context"] != cli.Context {
			t.Fatalf("original history not readable: status=%d, detail=%+v", code, detail)
		}
		code, detail = inboxCall(t, h.routes(), http.MethodPost, "/api/v1/inbox/"+other.ID+"/reply", `{"text":"I will check."}`)
		if code != http.StatusOK || detail["done_by"] != "replied" {
			t.Fatalf("reply failed: status=%d, detail=%+v", code, detail)
		}
		if len(cliReply.got) != 0 || len(otherReply.got) != 1 ||
			otherReply.got[0].Thread != other.Connector.Thread || otherReply.got[0].Last != other.Last {
			t.Fatalf("reply target changed: cli=%+v, preferred=%+v", cliReply.got, otherReply.got)
		}
		change(h, other.ID, func(it *InboxItem) { it.Tier = TierFYI })
		list := getSince(t, h, "0", true)
		if rows := byID(t, list.Items); len(rows) != 1 || !strings.Contains(rows[other.ID], `"done_by":"replied"`) {
			t.Fatalf("replied copy should remain the sole row: %v", rows)
		}
		change(h, other.ID, func(it *InboxItem) {
			it.Last = "1235"
			it.Preview = "@Alex Here is a different request from the same person."
			it.DoneAt = nil
		})
		delta := getSince(t, h, list.Cursor, true)
		if rows := byID(t, delta.Items); len(rows) != 2 || rows[cli.ID] == "" || strings.Contains(rows[other.ID], cli.ID) {
			t.Fatalf("distinct new activity should show both records and drop the stale link: %v", rows)
		}
	}
}

func TestInboxLarkDuplicateLeavesUncertainPairsVisible(t *testing.T) {
	tests := []struct {
		name   string
		change func(cli, other *InboxItem) []*InboxItem
	}{
		{"later minute", func(_, other *InboxItem) []*InboxItem { other.UpdatedAt = other.UpdatedAt.Add(time.Minute); return nil }},
		{"same first line", func(_, other *InboxItem) []*InboxItem { other.Preview += " Different report."; return nil }},
		{"clipped companion preview", func(_, other *InboxItem) []*InboxItem {
			other.Context += " A new line absent from the preview."
			return nil
		}},
		{"missing current body", func(cli, _ *InboxItem) []*InboxItem { cli.Context = ""; return nil }},
		{"distinct activity in CLI batch", func(cli, _ *InboxItem) []*InboxItem {
			cli.Context = strings.Replace(cli.Context, larkBackground, "\n["+cli.UpdatedAt.Format(time.RFC3339)+"] Someone: Another request."+larkBackground, 1)
			return nil
		}},
		{"CLI is the only reply route", func(_, other *InboxItem) []*InboxItem { other.Connector.CanReply = false; return nil }},
		{"same minute repeat", func(_, other *InboxItem) []*InboxItem {
			another := *other
			another.ID, another.Last = "larkx:2", "4321"
			return []*InboxItem{&another}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cli, other := pairedLarkMentions()
			items := append([]*InboxItem{cli, other}, test.change(cli, other)...)
			h := memoInbox(t, nil)
			h.inbox.mu.Lock()
			for _, it := range items {
				h.inbox.items[it.ID] = it
			}
			h.inbox.mu.Unlock()
			if rows := getSince(t, h, "0", true).Items; len(rows) != len(items) {
				t.Fatalf("uncertain pair hidden: got %d rows, want %d", len(rows), len(items))
			}
		})
	}
}
