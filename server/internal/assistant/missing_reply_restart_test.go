package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type replyRecoveryMemStore struct {
	*delayedWaitTestStore
	mem *planMemStore
}

func (s *replyRecoveryMemStore) FeedItem(_ context.Context, id string) (*db.FeedRow, error) {
	s.mem.pmu.Lock()
	defer s.mem.pmu.Unlock()
	for _, it := range s.mem.feed {
		if it.ID == id {
			return &it, nil
		}
	}
	return nil, nil
}

func (s *replyRecoveryMemStore) SettingsPrefix(ctx context.Context, prefix string) (map[string]json.RawMessage, error) {
	return s.mem.SettingsPrefix(ctx, prefix)
}

func newReplyRecoveryMemStore() *replyRecoveryMemStore {
	mem := &planMemStore{memStore: &memStore{}}
	return &replyRecoveryMemStore{delayedWaitTestStore: &delayedWaitTestStore{Store: mem}, mem: mem}
}

func TestMissingReplyAfterSupervisionAndManagerRestart(t *testing.T) {
	for _, sid := range []string{"main-chat", "direction-chat"} {
		t.Run(sid, func(t *testing.T) {
			const ask = "What did the checks find?"
			const inputID = "owner-input"
			chat := newTranscriptChat(t, false)
			for range 3 {
				chat.user(prompts.MessageSourceText("Check the work.", &proto.MessageSource{Kind: proto.SourceSupervision}))
				chat.assistant("No response requested.")
			}
			chat.user(ask)
			chat.toolCall("Two checks passed.")
			chat.write(`{"type":"assistant","uuid":"placeholder","message":{"role":"assistant","model":"<synthetic>","stop_reason":"stop_sequence","stop_sequence":"","content":[{"type":"text","text":"No response requested."}]}}`)

			f := newFakeHub().withMachine()
			f.settings = &Settings{MachineID: "server", Main: "main-chat"}
			f.hello.Locale = string(i18n.En)
			row := proto.Session{MachineID: "server", SessionID: sid, Assistant: true, Agent: "claude", Status: proto.StatusDone}
			f.setSessions("server", row)
			// The node restores its echo from the hub's send ledger and attributes the original input to the app.
			f.page = func(turns int) []proto.Item {
				items := chat.page(turns)
				for i := range items {
					if items[i].Role == "user" && items[i].Text == ask {
						items[i].ClientMsgID = inputID
					}
				}
				return items
			}
			if items := chat.page(1); len(items) != 4 || items[0].Text != ask || items[3].Text != "No response requested." {
				t.Fatalf("unexpected reproduction page: %+v", items)
			}
			st := newReplyRecoveryMemStore()
			first := newServiceOn(t, f, st)
			if _, err := first.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: sid, ReplyInput: true,
				Params: map[string]any{"session_id": sid, "client_msg_id": inputID, "text": ask}}); err != nil {
				t.Fatal(err)
			}
			var sent []map[string]any
			f.answerNode(assistantwire.DeviceAssistantAnswerAgain, func(_ string, raw json.RawMessage) (any, error) {
				var p map[string]any
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, err
				}
				sent = append(sent, p)
				return map[string]any{}, nil
			})

			second := newServiceOn(t, f, st)
			second.assistantTurnSettled(t.Context(), "server", sid)
			third := newServiceOn(t, f, st)
			third.assistantTurnSettled(t.Context(), "server", sid)
			if len(sent) != 1 || sent[0]["session_id"] != sid {
				t.Fatalf("guarded follow-ups after restart = %v, want one in %s", sent, sid)
			}
			chat.user(sent[0]["text"].(string))
			chat.assistant("No response requested.")
			third.assistantTurnSettled(t.Context(), "server", sid)
			third.assistantTurnSettled(t.Context(), "server", sid)
			assertMissingReplyNotices(t, third, sid, 1)
			if len(sent) != 1 {
				t.Fatalf("an exhausted retry prompted again: %v", sent)
			}
		})
	}
}

func TestMissingReplyOnFirstInventoryAfterRestart(t *testing.T) {
	for _, mode := range []string{"offline", "user stopped", "new owner send"} {
		t.Run(mode, func(t *testing.T) {
			const sid, ask = "main-chat", "What did the checks find?"
			const mid, id = "server", "owner-input"
			f := newFakeHub().withMachine()
			f.settings = &Settings{MachineID: mid, Main: sid}
			f.hello.Locale = string(i18n.En)
			row := proto.Session{MachineID: mid, SessionID: sid, Assistant: true, Agent: "claude", Status: proto.StatusDone}
			f.setSessions(mid, row)
			st := newReplyRecoveryMemStore()
			first := newServiceOn(t, f, st)
			send := func(id, text string) {
				t.Helper()
				if _, err := first.SendParams(t.Context(), SendParams{MachineID: mid, SessionID: sid, Human: true,
					Params: map[string]any{"session_id": sid, "client_msg_id": id, "text": text}}); err != nil {
					t.Fatal(err)
				}
			}
			send(id, ask)
			if mode == "new owner send" {
				send("newer-input", "I have a different question.")
			}

			devs := newFleetDevs()
			devs.add(mid, "Server")
			row.Cwd, row.Status = "/home/u/.persona/assistant", proto.StatusOffline
			if mode == "user stopped" {
				row.Capabilities = &proto.SessionCapabilities{StopCause: proto.StopUser}
			}
			devs.setSessions(mid, []proto.Session{row})
			var attempts atomic.Int32
			f.answerNode(assistantwire.DeviceAssistantAnswerAgain, func(string, json.RawMessage) (any, error) {
				attempts.Add(1)
				return nil, errors.New("conversation process unavailable")
			})
			f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
				return map[string]any{"items": []proto.Item{
					{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: ask, ClientMsgID: id},
				}, "has_more": true}, nil
			})
			second := withSettings(t, f, New(Options{Hub: f, Devices: devs, Store: func() Store { return st }, Logf: testLogf(t)}))
			if mode != "offline" {
				second.recheckMissingReply(t.Context(), mid, sid)
				assertMissingReplyNotices(t, second, sid, 0)
				if got := attempts.Load(); got != 0 {
					t.Fatalf("obsolete retry calls = %d, want zero", got)
				}
				return
			}
			(&fleetTest{s: second, d: devs, f: f}).sessions(mid, row)
			deadline := time.Now().Add(2 * time.Second)
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				feed, err := second.feedItems(t.Context(), sid, time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				if len(feed) > 0 || time.Now().After(deadline) {
					break
				}
				<-tick.C
			}
			assertMissingReplyNotices(t, second, sid, 1)
			if got := attempts.Load(); got != 1 {
				t.Fatalf("guarded offline retry calls = %d, want one", got)
			}
		})
	}
}
