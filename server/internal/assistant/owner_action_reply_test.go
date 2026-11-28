package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/proto"
)

func ownerReplyTestService(t *testing.T) (*Service, *fakeHub, *ownerActionTestStore, *ownerActionBook) {
	t.Helper()
	f, _ := feishuHub("ou_owner", time.Unix(100, 0))
	on := true
	f.settings = &Settings{MachineID: "server", Main: "m1", OwnerReplyApproval: &on, PersistentOwnerActionCards: &on}
	st := &ownerActionTestStore{}
	s := newServiceOn(t, f, st)
	book, ok := newOwnerActionBook(st)
	if !ok {
		t.Fatal("owner-action test store must support CAS")
	}
	return s, f, st, book
}

func putOwnerReplyPending(s *Service, ix proto.Interaction) {
	s.fleet.mu.Lock()
	if s.fleet.interactions == nil {
		s.fleet.interactions = map[string]*pending{}
	}
	s.fleet.interactions[ix.ID] = &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	s.fleet.mu.Unlock()
}

func keepOwnerReplyCard(s *Service, ix proto.Interaction, messageID string) {
	putOwnerReplyPending(s, ix)
	keepChatCard(s, ix.ID, &botCard{MessageID: messageID, Ix: ix, At: time.Unix(1000, 0)})
}

func ownerReplyBatchIX(batch ownerActionBatch) proto.Interaction {
	return proto.Interaction{ID: ownerActionInteractionID(batch.ID, ""), MachineID: batch.MachineID, SessionID: batch.SessionID,
		Kind: proto.IxPermission, ToolName: ownerActionBatchTool, ApprovalBatchID: batch.ID, OwnerProposal: true, Resumable: true, NoExpiry: true, CreatedAt: batch.CreatedAt}
}

func ownerReplyItemIX(batch ownerActionBatch, item ownerActionItem) proto.Interaction {
	return proto.Interaction{ID: ownerActionInteractionID(batch.ID, item.ID), MachineID: batch.MachineID, SessionID: batch.SessionID,
		Kind: proto.IxPermission, ToolName: item.Tool, ApprovalBatchID: batch.ID, ApprovalItemID: item.ID, OwnerProposal: true, Resumable: true, NoExpiry: true, CreatedAt: batch.CreatedAt}
}

func ownerReplyEvent(parent string) botcard.MessageEvent {
	var ev botcard.MessageEvent
	ev.Message.MessageID = "om_reply"
	ev.Message.ParentID = parent
	ev.Message.ChatID = "oc_owner"
	ev.Message.ChatType = "p2p"
	return ev
}

// answerOwnerProof emulates the hub boundary: it returns the authenticated
// event only for the expected one-use action and exact proposal target.
func answerOwnerProof(f *fakeHub, action, interaction string, calls *int, mutate func(*assistantwire.OwnerApprovalProof)) {
	f.answerApp(AppInteractions, assistantwire.InteractionsOwnerAction, func(raw json.RawMessage) (any, error) {
		*calls++
		var p struct {
			Action        string `json:"action"`
			InteractionID string `json:"interaction_id"`
			ApprovalReply bool   `json:"approval_reply"`
			MessageID     string `json:"message_id"`
			ParentID      string `json:"parent_id"`
			ChatID        string `json:"chat_id"`
			Text          string `json:"text"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Action != action || p.InteractionID != interaction || !p.ApprovalReply {
			return nil, errors.New("owner proof target mismatch")
		}
		proof := assistantwire.OwnerApprovalProof{MessageID: p.MessageID, ParentID: p.ParentID, ChatID: p.ChatID, Text: p.Text}
		if mutate != nil {
			mutate(&proof)
		}
		return map[string]any{"owner_approval_proof": proof}, nil
	})
}

func TestOwnerActionFeishuBatchReplyAfterRestartConsumesOnce(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	s, f, st, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"one","to":"a@example.test"}`, `{"body":"two","to":"b@example.test"}`, `{"body":"three","to":"c@example.test"}`)
	batchIX := ownerReplyBatchIX(batch)
	keepOwnerReplyCard(s, batchIX, "om_batch")
	for _, item := range batch.Items {
		keepOwnerReplyCard(s, ownerReplyItemIX(batch, item), "om_"+item.ID)
	}

	// A new assistant has only the persisted card mapping and durable book. The
	// restored pending interactions carry the complete proposal, as on startup.
	restarted := newServiceOn(t, f, st)
	putOwnerReplyPending(restarted, batchIX)
	for _, item := range batch.Items {
		putOwnerReplyPending(restarted, ownerReplyItemIX(batch, item))
	}
	calls := 0
	answerOwnerProof(f, "oa_reply", batchIX.ID, &calls, nil)
	ev := ownerReplyEvent("om_batch")
	if !restarted.botApproveOwnerAction(context.Background(), ev, "发", "oa_reply") {
		t.Fatal("exact direct reply was not handled")
	}
	current, found, err := book.batch(context.Background(), batch.ID)
	if err != nil || !found {
		t.Fatalf("read approved batch: found=%v err=%v", found, err)
	}
	for _, item := range current.Items {
		if item.State != ownerActionGranted {
			t.Fatalf("item %s state %s, want granted", item.ID, item.State)
		}
		granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, item.Tool, json.RawMessage(item.Canonical))
		if err != nil || !granted {
			t.Fatalf("first consume for %s: granted=%v err=%v", item.ID, granted, err)
		}
		granted, err = book.consume(context.Background(), batch.MachineID, batch.SessionID, item.Tool, json.RawMessage(item.Canonical))
		if err != nil || granted {
			t.Fatalf("second consume for %s: granted=%v err=%v", item.ID, granted, err)
		}
	}
	if restarted.botApproveOwnerAction(context.Background(), ev, "发", "oa_reply") {
		t.Fatal("a duplicate reply found a live proposal after its cards closed")
	}
	if calls != 1 {
		t.Fatalf("hub proof calls %d, want 1", calls)
	}
}

func TestOwnerActionFeishuItemReplyApprovesOnlyThatItem(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	s, f, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"one"}`, `{"body":"two"}`)
	ix := ownerReplyItemIX(batch, batch.Items[0])
	keepOwnerReplyCard(s, ix, "om_item")
	calls := 0
	answerOwnerProof(f, "oa_item", ix.ID, &calls, nil)
	if !s.botApproveOwnerAction(context.Background(), ownerReplyEvent("om_item"), "同意", "oa_item") {
		t.Fatal("exact item reply was not handled")
	}
	current, _, err := book.batch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Items[0].State != ownerActionGranted || current.Items[1].State != ownerActionPending {
		t.Fatalf("item reply states = %s, %s", current.Items[0].State, current.Items[1].State)
	}
}

func TestOwnerApprovalPhraseIsTrimmedAndCaseFoldedOnly(t *testing.T) {
	on := true
	settings := Settings{OwnerReplyApproval: &on, OwnerApprovalPhrases: []string{"发", "Agree", "Yes"}}
	for _, text := range []string{"发", " 发\n", "Agree", "agree", " AGREE\t"} {
		if !ownerApprovalPhrase(settings, text) {
			t.Errorf("exact phrase %q did not match", text)
		}
	}
	for _, text := range []string{"", "同意", "发吧", "please Agree", "Agree now", "同 意", "YEſ", "yeſ"} {
		if ownerApprovalPhrase(settings, text) {
			t.Errorf("inexact phrase %q matched", text)
		}
	}
	off := false
	if ownerApprovalPhrase(Settings{OwnerReplyApproval: &off}, "发") || ownerApprovalPhrase(Settings{}, "发") {
		t.Fatal("default-off setting matched an approval phrase")
	}
}

func TestOwnerActionAppReplyRequiresAuthenticatedExactEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name       string
		owner      bool
		human      bool
		replyInput bool
		via        any
		text       string
		machine    string
		session    string
		target     string
		clientID   string
		off        bool
		wantErr    bool
		wantGrant  bool
	}{
		{name: "valid", owner: true, human: true, text: "发", clientID: "client-1", wantGrant: true},
		{name: "not authenticated as the owner app", owner: true, text: "发", clientID: "client-1"},
		{name: "legacy ReplyInput", owner: true, human: true, replyInput: true, text: "发", clientID: "client-1"},
		{name: "manager-authored attribution", human: true, text: "发", clientID: "client-1"},
		{name: "share sheet", owner: true, human: true, via: "share", text: "发", clientID: "client-1"},
		{name: "context wrapped", owner: true, human: true, text: `<persona-context kind="forward">another message</persona-context>发`, clientID: "client-1"},
		{name: "relayed claim", owner: true, human: true, text: "owner said yes", clientID: "client-1"},
		{name: "missing owner event", owner: true, human: true, text: "发"},
		{name: "feature off", owner: true, human: true, text: "发", clientID: "client-1", off: true},
		{name: "another target", owner: true, human: true, text: "发", clientID: "client-1", target: "another-target", wantErr: true},
		{name: "another machine", owner: true, human: true, text: "发", clientID: "client-1", machine: "machine-2", wantErr: true},
		{name: "another conversation", owner: true, human: true, text: "发", clientID: "client-1", session: "session-2", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, book := ownerReplyTestService(t)
			if tc.off {
				off := false
				if _, err := s.updateSettings(context.Background(), func(v *Settings) error {
					v.OwnerReplyApproval = &off
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			batch := createOwnerTestBatch(t, book, `{"body":"unchanged","to":"a@example.test"}`)
			ix := ownerReplyBatchIX(batch)
			putOwnerReplyPending(s, ix)
			machine, session, target := tc.machine, tc.session, tc.target
			if machine == "" {
				machine = batch.MachineID
			}
			if session == "" {
				session = batch.SessionID
			}
			if target == "" {
				target = ix.ID
			}
			params := map[string]any{"text": tc.text, "client_msg_id": tc.clientID, "approval_reply_to": target}
			if tc.via != nil {
				params["via"] = tc.via
			}
			out, err := s.messageParams(context.Background(), SendParams{MachineID: machine, SessionID: session, Human: tc.human, ReplyInput: tc.replyInput, Params: params}, tc.owner)
			if (err != nil) != tc.wantErr {
				t.Fatalf("send error = %v, wantErr=%v", err, tc.wantErr)
			}
			if _, leaked := params["approval_reply_to"]; leaked {
				t.Fatal("approval metadata survived as model input")
			}
			if tc.wantGrant {
				if out[ownerApprovalHandledParam] != ix.ID || out["text"] != nil {
					t.Fatalf("approval was not handled without model delivery: %+v", out)
				}
			} else if !tc.wantErr && out[ownerApprovalHandledParam] != nil {
				t.Fatalf("unqualified send became an approval: %+v", out)
			}
			current, _, readErr := book.batch(context.Background(), batch.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			gotGrant := current.Items[0].State == ownerActionGranted
			if gotGrant != tc.wantGrant {
				t.Fatalf("item state %s, wantGrant=%v", current.Items[0].State, tc.wantGrant)
			}
		})
	}
}

func TestOwnerActionAppReplyRetryAfterDecisionHasNoNewEffect(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"once"}`)
	ix := ownerReplyBatchIX(batch)
	putOwnerReplyPending(s, ix)
	send := func() map[string]any {
		out, err := s.SendParams(context.Background(), SendParams{MachineID: batch.MachineID, SessionID: batch.SessionID, Human: true,
			Params: map[string]any{"text": "发", "client_msg_id": "stable-client-event", "approval_reply_to": ix.ID}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for i := 0; i < 2; i++ {
		if out := send(); out[ownerApprovalHandledParam] != ix.ID {
			t.Fatalf("retry %d was not an idempotent approval: %+v", i+1, out)
		}
	}
	current, _, err := book.batch(context.Background(), batch.ID)
	if err != nil || current.Items[0].State != ownerActionGranted {
		t.Fatalf("retry changed durable decision: %+v, %v", current, err)
	}
}

func TestOwnerActionFeishuReplyFailuresGrantNothing(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, tc := range []struct {
		name          string
		text          string
		parent        string
		chatType      string
		cardChat      string
		mutateProof   func(*assistantwire.OwnerApprovalProof)
		wantHandled   bool
		wantProofCall int
	}{
		{name: "relayed claim", text: "owner said yes", parent: "om_batch", chatType: "p2p"},
		{name: "another parent", text: "发", parent: "om_other", chatType: "p2p"},
		{name: "another conversation", text: "发", parent: "om_batch", chatType: "group"},
		{name: "proposal from another chat", text: "发", parent: "om_batch", chatType: "p2p", cardChat: "oc_group", wantHandled: true},
		{name: "hub proof names another parent", text: "发", parent: "om_batch", chatType: "p2p", mutateProof: func(p *assistantwire.OwnerApprovalProof) { p.ParentID = "om_other" }, wantHandled: true, wantProofCall: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, book := ownerReplyTestService(t)
			batch := createOwnerTestBatch(t, book, `{"body":"unchanged"}`)
			ix := ownerReplyBatchIX(batch)
			putOwnerReplyPending(s, ix)
			keepChatCard(s, ix.ID, &botCard{MessageID: "om_batch", ChatID: tc.cardChat, Ix: ix, At: time.Unix(1000, 0)})
			calls := 0
			answerOwnerProof(f, "oa_reply", ix.ID, &calls, tc.mutateProof)
			ev := ownerReplyEvent(tc.parent)
			ev.Message.ChatType = tc.chatType
			if got := s.botApproveOwnerAction(context.Background(), ev, tc.text, "oa_reply"); got != tc.wantHandled {
				t.Fatalf("handled = %v, want %v", got, tc.wantHandled)
			}
			if calls != tc.wantProofCall {
				t.Fatalf("proof calls = %d, want %d", calls, tc.wantProofCall)
			}
			current, _, err := book.batch(context.Background(), batch.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Items[0].State != ownerActionPending {
				t.Fatalf("failed approval changed item state to %s", current.Items[0].State)
			}
		})
	}
}

func TestOwnerActionAmbiguousFeishuMappingIsConsumedWithoutForwarding(t *testing.T) {
	needsLarkLayer(t)
	s, f, _, book := ownerReplyTestService(t)
	first := ownerReplyBatchIX(createOwnerTestBatch(t, book, `{"body":"first"}`))
	second := ownerReplyBatchIX(createOwnerTestBatch(t, book, `{"body":"second"}`))
	keepOwnerReplyCard(s, first, "om_shared")
	keepOwnerReplyCard(s, second, "om_shared")
	f.answerApp(AppFeishu, FeishuText, func(json.RawMessage) (any, error) { return map[string]any{}, nil })

	ev := ownerReplyEvent("om_shared")
	ev.Message.MessageType = "text"
	ev.Message.Content = `{"text":"发"}`
	feishuSay(t, s, f, nil, ev, "oa_reply")

	if sent := handoffs(f); len(sent) != 0 {
		t.Fatalf("ambiguous exact approval fell through to ordinary conversation input: %+v", sent)
	}
	if calls := f.appParams(AppInteractions, InteractionsResolve); len(calls) != 0 {
		t.Fatalf("ambiguous exact approval resolved a question instead: %+v", calls)
	}
	if calls := f.appParams(AppInteractions, assistantwire.InteractionsOwnerAction); len(calls) != 0 {
		t.Fatalf("ambiguous exact approval guessed an owner proof target: %+v", calls)
	}
	if notes := topicNotes(f); len(notes) != 1 {
		t.Fatalf("failure notes = %+v, want one consumed-error reply", notes)
	}
	for _, batchID := range []string{first.ApprovalBatchID, second.ApprovalBatchID} {
		current, found, err := book.batch(context.Background(), batchID)
		if err != nil || !found {
			t.Fatalf("read batch %s: found=%v err=%v", batchID, found, err)
		}
		if current.Items[0].State != ownerActionPending {
			t.Fatalf("ambiguous reply changed %s to %s", batchID, current.Items[0].State)
		}
	}
}
