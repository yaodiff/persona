package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/rpc"
)

type mentionCapturingConnector struct {
	id      string
	things  []connector.InboxThing
	replies []connector.InboxReply
}

func (c *mentionCapturingConnector) ID() string   { return c.id }
func (c *mentionCapturingConnector) Name() string { return "飞书 (LarkAgentX)" }
func (c *mentionCapturingConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox}, Kinds: []string{KindMention}}
}
func (c *mentionCapturingConnector) Collect(context.Context) ([]connector.InboxThing, error) {
	return c.things, nil
}
func (c *mentionCapturingConnector) Reply(_ context.Context, r connector.InboxReply) error {
	c.replies = append(c.replies, r)
	return nil
}

func TestInboxSuggestMentionsValidation(t *testing.T) {
	h, _, it := handledHub(t)

	for _, tc := range []struct {
		name string
		args string
	}{
		{"non-reply", `{"kind":"conclusion","text":"@Alice done","mentions":[{"id":"101","name":"Alice"}]}`},
		{"missing in text", `{"kind":"reply","text":"Looks good","mentions":[{"id":"101","name":"Alice"}]}`},
		{"prefix only", `{"kind":"reply","text":"@AliceSmith looks good","mentions":[{"id":"101","name":"Alice"}]}`},
		{"inline code only", `{"kind":"reply","text":"See ` + "`@Alice`" + ` in code","mentions":[{"id":"101","name":"Alice"}]}`},
		{"fenced code only", `{"kind":"reply","text":"` + "```" + `\n@Alice\n` + "```" + `","mentions":[{"id":"101","name":"Alice"}]}`},
		{"link label only", `{"kind":"reply","text":"[ping @Alice](https://example.com/a)","mentions":[{"id":"101","name":"Alice"}]}`},
		{"duplicate name different id", `{"kind":"reply","text":"@Alice looks good","mentions":[{"id":"101","name":"Alice"},{"id":"102","name":"Alice"}]}`},
		{"at in name", `{"kind":"reply","text":"@@Alice looks good","mentions":[{"id":"101","name":"@Alice"}]}`},
		{"empty id", `{"kind":"reply","text":"@Alice looks good","mentions":[{"id":"   ","name":"Alice"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := suggestTool(t, h, tc.args); err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
		})
	}

	if _, err := suggestTool(t, h, `{"kind":"reply","text":"@Alice @Bob looks good","mentions":[{"id":"101","name":"Alice"},{"id":"101","name":"Alice"},{"id":"202","name":"Bob"}]}`); err != nil {
		t.Fatalf("valid mentions rejected: %v", err)
	}
	h.inbox.mu.Lock()
	listed := it.listed()
	h.inbox.mu.Unlock()
	want := []InboxMention{{ID: "101", Name: "Alice"}, {ID: "202", Name: "Bob"}}
	if listed.Suggestion == nil || !slices.Equal(listed.Suggestion.Mentions, want) {
		t.Fatalf("listed mentions = %+v, want %+v", listed.Suggestion, want)
	}
}

func TestInboxSuggestionConfirmForwardsAndPrunesMentions(t *testing.T) {
	t.Run("connector item unedited forwards all bound mentions", func(t *testing.T) {
		ctx := context.Background()
		h := agentHub(t)
		c := &mentionCapturingConnector{
			id: "larkx",
			things: []connector.InboxThing{{
				Thread: "7001", Kind: KindMention, Label: "群里 @ 了你", Title: "Release checklist sync",
				Sender: "Alice", Preview: "@you please check", Context: "context", At: time.Now(), Last: "55",
				ReplyTo: "Everyone in the group can see this", CanReply: true, Tier: TierAction,
			}},
		}
		h.addConnector(c, true)
		h.collectInbox(ctx, roundPoll)
		it := openItem(t, h)
		h.inbox.mu.Lock()
		it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1"}
		h.inbox.noteHandling(it, "server", "s1")
		h.inbox.setHandling(it, HandlingLooking, "", time.Now())
		h.inbox.mu.Unlock()

		if _, err := suggestTool(t, h, `{"kind":"reply","text":"@Alice done, @Bob @Carol please review","mentions":[{"id":"101","name":"Alice"},{"id":"202","name":"Bob"},{"id":"303","name":"Carol"}]}`); err != nil {
			t.Fatalf("suggest: %v", err)
		}

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
		if code != 200 {
			t.Fatalf("confirm failed: %d %v", code, out)
		}
		want := []connector.InboxMention{{ID: "101", Name: "Alice"}, {ID: "202", Name: "Bob"}, {ID: "303", Name: "Carol"}}
		if len(c.replies) != 1 || !slices.Equal(c.replies[0].Mentions, want) {
			t.Fatalf("connector replies = %+v, want mentions %+v", c.replies, want)
		}
		h.inbox.mu.Lock()
		attempts := listedSuggestionAttempts(it.SuggestionAttempts)
		h.inbox.mu.Unlock()
		if len(attempts) != 1 || !slices.Equal(attempts[0].Mentions, want) {
			t.Fatalf("listed attempt mentions = %+v, want %+v", attempts, want)
		}
	})

	t.Run("user edit prunes removed mention and ignores unverified manual @name", func(t *testing.T) {
		h, lark, it := handledHub(t)
		if _, err := suggestTool(t, h, `{"kind":"reply","text":"@Alice done, @Carol please review","mentions":[{"id":"101","name":"Alice"},{"id":"303","name":"Carol"}]}`); err != nil {
			t.Fatalf("suggest: %v", err)
		}

		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"@Alice done, also @Dave please check"}`)
		if code != 200 {
			t.Fatalf("confirm edited failed: %d %v", code, out)
		}
		want := []connector.InboxMention{{ID: "101", Name: "Alice"}}
		if len(lark.got) != 1 || !slices.Equal(lark.got[0].Mentions, want) {
			t.Fatalf("lark replies = %+v, want mentions %+v", lark.got, want)
		}
		if lark.got[0].Text != "@Alice done, also @Dave please check" {
			t.Fatalf("reply text = %q", lark.got[0].Text)
		}
	})

	t.Run("prefix edit or code-masked @name prunes mention and keeps actualReply empty", func(t *testing.T) {
		h, lark, it := handledHub(t)
		if _, err := suggestTool(t, h, `{"kind":"reply","text":"@Alice please check","mentions":[{"id":"101","name":"Alice"}]}`); err != nil {
			t.Fatalf("suggest: %v", err)
		}
		code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"@AliceSmith and `+"`@Alice`"+` please check"}`)
		if code != 200 {
			t.Fatalf("confirm failed: %d %v", code, out)
		}
		if len(lark.got) != 1 || len(lark.got[0].Mentions) != 0 {
			t.Fatalf("expected zero forwarded mentions, got %+v", lark.got)
		}
		// Explicitly recorded empty mentions (MentionsSet=true, Mentions=nil) must never fall back to proposal.
		explicitEmpty := &InboxSuggestionAttempt{
			Version:     1,
			Text:        "@Alice still in text",
			Mentions:    nil,
			MentionsSet: true,
			Proposal:    &InboxSuggestion{Kind: SuggestReply, Text: "@Alice", Mentions: []InboxMention{{ID: "101", Name: "Alice"}}},
		}
		if got := actualReply(explicitEmpty); got == nil || len(got.Mentions) != 0 {
			t.Fatalf("actualReply with MentionsSet=true fell back to proposal: %+v", got)
		}
		// Legacy attempts without MentionsSet keep fallback behavior.
		legacy := &InboxSuggestionAttempt{
			Version:  1,
			Text:     "@Alice legacy",
			Proposal: &InboxSuggestion{Kind: SuggestReply, Text: "@Alice", Mentions: []InboxMention{{ID: "101", Name: "Alice"}}},
		}
		wantLegacy := []InboxMention{{ID: "101", Name: "Alice"}}
		if got := actualReply(legacy); got == nil || !slices.Equal(got.Mentions, wantLegacy) {
			t.Fatalf("legacy actualReply mentions = %+v, want %+v", got, wantLegacy)
		}
	})
}

func TestInboxReplyPolicyConfigReadRetryPreservesMentions(t *testing.T) {
	const draft = "@Alice and @Bob, please review."
	all := []InboxMention{{ID: "101", Name: "Alice"}, {ID: "202", Name: "Bob"}}
	for _, tc := range []struct {
		name     string
		text     string
		mentions []InboxMention
	}{
		{name: "unedited", text: draft, mentions: all},
		{name: "one mention removed", text: "@Alice, please review.", mentions: all[:1]},
		{name: "all mentions removed", text: "Please review."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, lark, it := handledHub(t)
			replyPolicyReview8OrdinaryItem(h, it)
			if _, err := suggestOrdinaryReplyForFixture(t, h, `{"kind":"reply","text":"`+draft+`","mentions":[{"id":"101","name":"Alice"},{"id":"202","name":"Bob"}]}`); err != nil {
				t.Fatal(err)
			}
			h.in.setModel(fakeModel(t, `{"category":"ordinary_coordination"}`))
			reads := 0
			h.in.mu.Lock()
			h.in.configHook = func() {
				h.in.mu.Lock()
				defer h.in.mu.Unlock()
				reads++
				if reads == 2 {
					h.in.configErr = errors.New("synthetic outage at the post-final-veto fresh read")
				}
			}
			h.in.mu.Unlock()

			request := suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFirstID}
			if tc.text != draft {
				request.Text = &tc.text
			}
			_, err := h.actOnSuggestion(t.Context(), it.ID, request, true)
			var rpcErr *rpc.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != assistantwire.CodeInboxSendFailed || rpcErr.Message != "reply policy configuration unavailable" {
				t.Fatalf("first confirmation error = %v, want a reply-policy configuration failure", err)
			}
			if len(lark.got) != 0 {
				t.Fatalf("first confirmation reached the connector during the outage: %+v", lark.got)
			}
			attempt := recoveryAttempt(t, h, it, request.Version)
			if attempt.Status != suggestionFailed || !attempt.Retryable {
				t.Fatalf("configuration outage did not retain a retryable failure: %+v", attempt)
			}
			if !attempt.MentionsSet || !slices.Equal(attempt.Mentions, tc.mentions) {
				t.Errorf("failed attempt recorded mentions %v (set=%t), want %v", attempt.Mentions, attempt.MentionsSet, tc.mentions)
			}

			h.in.mu.Lock()
			h.in.configErr, h.in.configHook = nil, nil
			h.in.mu.Unlock()
			request.ClientID, request.Text = recoveryFreshID, nil
			if _, err := h.actOnSuggestion(t.Context(), it.ID, request, true); err != nil {
				t.Fatalf("retry after configuration recovery failed: %v", err)
			}
			if len(lark.got) != 1 || lark.got[0].Text != tc.text || !slices.Equal(lark.got[0].Mentions, tc.mentions) {
				t.Fatalf("connector replies = %+v, want one reply with text %q and mentions %v", lark.got, tc.text, tc.mentions)
			}
		})
	}
}

func TestInboxHandoffCardIncludesMentions(t *testing.T) {
	h, lark, it := handledHub(t)
	h.inbox.mu.Lock()
	it.Handoffs = []InboxHandoff{{MachineID: "server", SessionID: "main"}}
	h.inbox.mu.Unlock()

	args, _ := json.Marshal(map[string]any{
		"item":     it.ID,
		"kind":     "reply",
		"recap":    "Root cause confirmed",
		"text":     "@Alice looks good",
		"mentions": []InboxMention{{ID: "101", Name: "Alice"}},
	})
	if _, err := h.runTool(context.Background(), "server", "main", "inbox_suggest", args); err != nil {
		t.Fatalf("handoff suggest: %v", err)
	}

	var card handoffCard
	found := false
	items, err := h.feedItems(context.Background(), "main", time.Time{})
	if err != nil {
		t.Fatalf("feedItems: %v", err)
	}
	for _, f := range items {
		if f.Kind == feedInboxSuggestion && f.Ref == it.ID {
			if err := json.Unmarshal(f.Body, &card); err != nil {
				t.Fatalf("unmarshal handoff card: %v", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected inbox_suggestion feed card")
	}
	want := []InboxMention{{ID: "101", Name: "Alice"}}
	if !slices.Equal(card.Mentions, want) {
		t.Fatalf("handoff card mentions = %+v, want %+v", card.Mentions, want)
	}

	code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
	if code != 200 {
		t.Fatalf("confirm handed reply: %d %v", code, out)
	}
	if len(lark.got) != 1 || !slices.Equal(lark.got[0].Mentions, want) {
		t.Fatalf("lark got = %+v, want mentions %+v", lark.got, want)
	}
}
