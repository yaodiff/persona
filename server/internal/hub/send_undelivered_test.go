package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// keepManager is a manager on the real bus that answers SendParams with the message as it came (or refuses it with
// ahead) and records assistant.keep_undelivered (or refuses it when noKeep: an older manager).
type keepManager struct {
	mu             sync.Mutex
	kept           []assistantwire.KeepUndeliveredParams
	ahead          bool
	noKeep         bool
	keepReply      json.RawMessage
	approvalTarget string
}

func (k *keepManager) keeps() []assistantwire.KeepUndeliveredParams {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]assistantwire.KeepUndeliveredParams(nil), k.kept...)
}

func undeliveredHub(t *testing.T, send func(context.Context) error) (*Hub, *Machine, *keepManager) {
	t.Helper()
	h := newTestHub(t)
	h.store = newSentStore()
	pipeNode(t, h, "box", send)
	m := h.byID["box"]
	editSessions(m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "s1", Assistant: true, Status: proto.StatusOffline}}
	})
	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	token, err := h.createBusToken(context.Background(), assistantwire.AssistantID, "Test manager")
	if err != nil {
		t.Fatal(err)
	}
	k := &keepManager{}
	connectGuardManagerWith(t, srv.URL, assistantwire.AssistantID, token, func(method string, raw json.RawMessage) (any, error) {
		k.mu.Lock()
		defer k.mu.Unlock()
		switch method {
		case assistantwire.MethodSendParams:
			if k.ahead {
				return nil, &rpc.Error{Code: assistantwire.CodeUndeliveredAhead, Message: "older ones wait"}
			}
			var p assistantwire.SendParams
			_ = json.Unmarshal(raw, &p)
			if k.approvalTarget != "" {
				return map[string]any{"params": map[string]any{"owner_approval_handled": k.approvalTarget}}, nil
			}
			p.Params["live_snapshot"] = "added by the manager" // never kept: a later send makes its own
			return map[string]any{"params": p.Params}, nil
		case assistantwire.MethodKeepUndelivered:
			if k.noKeep {
				return nil, errors.New("unknown method")
			}
			var p assistantwire.KeepUndeliveredParams
			_ = json.Unmarshal(raw, &p)
			k.kept = append(k.kept, p)
			if k.keepReply != nil {
				return k.keepReply, nil
			}
			id, _ := p.Params["client_msg_id"].(string)
			text, _ := p.Params["text"].(string)
			return map[string]any{"undelivered": proto.UndeliveredMessage{ID: id, Text: text, State: proto.UndeliveredWaiting, At: time.Now()}}, nil
		}
		return map[string]any{}, nil
	})
	return h, m, k
}

func sendAs(h *Hub, m *Machine, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, sendPath, strings.NewReader(body))
	r.SetPathValue("sid", "s1")
	w := httptest.NewRecorder()
	h.sendMessage(w, r, m)
	return w
}

// The assistant's process does not start (nothing typed): the hub hands the message as the app sent it to the manager,
// answers 202 delivered "later" instead of a failure, and tells the chat's subscribers it is kept. A retry of the same
// id — the app's operation replayed, as the web did on the night this was found — is answered the same, never typed.
func TestSendToAnAssistantThatCannotStartIsKept(t *testing.T) {
	var tries atomic.Int32
	h, m, k := undeliveredHub(t, func(context.Context) error {
		tries.Add(1)
		return errors.New("the launcher did not start (16.6s), the message was not delivered")
	})
	c := sendSubscriber(h, "box", "s1")
	w := sendAs(h, m, `{"text":"check the backup job","client_msg_id":"keep-1"}`)
	var out struct {
		Delivered   string                    `json:"delivered"`
		Undelivered *proto.UndeliveredMessage `json:"undelivered"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusAccepted || out.Delivered != sendLater ||
		out.Undelivered == nil || out.Undelivered.ID != "keep-1" {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	kept := k.keeps()
	if len(kept) != 1 || kept[0].Params["text"] != "check the backup job" || kept[0].Params["client_msg_id"] != "keep-1" ||
		kept[0].Params["live_snapshot"] != nil || kept[0].MachineID != "box" || kept[0].SessionID != "s1" {
		t.Fatalf("kept %+v", kept)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendProcessing {
		t.Fatalf("first state %+v", event)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendKept {
		t.Fatalf("final state %+v", event)
	}
	if again := sendAs(h, m, `{"text":"check the backup job","client_msg_id":"keep-1"}`); again.Code != http.StatusAccepted ||
		!strings.Contains(again.Body.String(), `"delivered":"later"`) {
		t.Fatalf("retry: %d %s", again.Code, again.Body)
	}
	h.sends.mu.Lock()
	clear(h.sends.flights) // a hub that restarted remembers only its Postgres record
	h.sends.mu.Unlock()
	if again := sendAs(h, m, `{"text":"check the backup job","client_msg_id":"keep-1"}`); again.Code != http.StatusAccepted {
		t.Fatalf("retry after a restart: %d %s", again.Code, again.Body)
	}
	if tries.Load() != 1 || len(k.keeps()) != 1 {
		t.Fatalf("tries=%d keeps=%d, want one each", tries.Load(), len(k.keeps()))
	}
}

// A connection that drops before the send's result is known is kept too, with the original attempt's start time: the
// manager must look for it in the transcript before trying again, not resend blindly and not leave it only "unknown".
func TestSendWithUnknownOutcomeIsKeptForLandedCheck(t *testing.T) {
	h, m, k := undeliveredHub(t, func(context.Context) error {
		return &rpc.Error{Code: assistantwire.CodeDeliveryUnknown, Message: "connection lost", Data: json.RawMessage(`{"delivered":"unknown"}`)}
	})
	started := time.Now()
	w := sendAs(h, m, `{"text":"check whether this landed","client_msg_id":"keep-unknown"}`)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"delivered":"later"`) {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	kept := k.keeps()
	if len(kept) != 1 || kept[0].Code != assistantwire.CodeDeliveryUnknown {
		t.Fatalf("kept %+v, want one delivery_unknown", kept)
	}
	if kept[0].TriedAt.IsZero() || kept[0].TriedAt.Before(started) || time.Since(kept[0].TriedAt) > time.Minute {
		t.Fatalf("first attempt started at %s, want a recent start not before %s", kept[0].TriedAt, started)
	}
}

// The node refuses the owner's message because the conversation is at work while its task-follow-up role changes
// (CodeConversationBusy, nothing typed). The owner used to get 409 "wait and try again" on every send (five in a row
// while a background script ran for 25 minutes); now the message is kept, the app is told it waits, and the manager
// sends it once the work ends, on the new role. Other 409 refusals are still answered as before.
func TestSendRefusedBusyForARoleChangeIsKept(t *testing.T) {
	var tries atomic.Int32
	h, m, k := undeliveredHub(t, func(context.Context) error {
		tries.Add(1)
		return &rpc.Error{Code: assistantwire.CodeConversationBusy, Message: "role changed while at work"}
	})
	c := sendSubscriber(h, "box", "s1")
	w := sendAs(h, m, `{"text":"where are we?","client_msg_id":"keep-busy"}`)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"delivered":"later"`) {
		t.Fatalf("answer: %d %s, want 202 kept", w.Code, w.Body)
	}
	if kept := k.keeps(); len(kept) != 1 || kept[0].Code != assistantwire.CodeConversationBusy || kept[0].Params["text"] != "where are we?" {
		t.Fatalf("kept %+v", kept)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendProcessing {
		t.Fatalf("first state %+v", event)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendKept {
		t.Fatalf("final state %+v", event)
	}
	if again := sendAs(h, m, `{"text":"where are we?","client_msg_id":"keep-busy"}`); again.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", again.Code, again.Body)
	}
	if tries.Load() != 1 || len(k.keeps()) != 1 {
		t.Fatalf("tries=%d keeps=%d, want one each", tries.Load(), len(k.keeps()))
	}
}

// Another 409 refusal (here a launcher of another kind) is not a wait: the owner still gets it, and nothing is kept.
func TestSendRefusedForAnotherReasonIsNotKept(t *testing.T) {
	h, m, k := undeliveredHub(t, func(context.Context) error {
		return &rpc.Error{Code: assistantwire.CodeLauncherOtherKind, Message: "another kind", Data: json.RawMessage(`{"delivered":"none"}`)}
	})
	if w := sendAs(h, m, `{"text":"hi","client_msg_id":"keep-other"}`); w.Code != http.StatusConflict {
		t.Fatalf("answer: %d %s, want 409", w.Code, w.Body)
	}
	if kept := k.keeps(); len(kept) != 0 {
		t.Fatalf("kept %+v", kept)
	}
}

// Older owner messages to the conversation are kept: the manager refuses to prepare a newer one, and the hub keeps it
// behind them without typing anything.
func TestSendBehindKeptMessagesIsKeptToo(t *testing.T) {
	var tries atomic.Int32
	h, m, k := undeliveredHub(t, func(context.Context) error { tries.Add(1); return nil })
	k.mu.Lock()
	k.ahead = true
	k.mu.Unlock()
	if w := sendAs(h, m, `{"text":"second","client_msg_id":"keep-2"}`); w.Code != http.StatusAccepted {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	if kept := k.keeps(); len(kept) != 1 || kept[0].Code != assistantwire.CodeUndeliveredAhead || tries.Load() != 0 {
		t.Fatalf("kept %+v, tries %d", kept, tries.Load())
	}
}

func TestKeepUndeliveredCarriesAuthenticatedDeviceCredential(t *testing.T) {
	h, m, k := undeliveredHub(t, func(context.Context) error { return errors.New("machine unavailable") })
	ctx := context.WithValue(context.Background(), principalKey{}, principal{ID: "dev_sender", Role: roleFull})
	status, body, ok := h.keepUndelivered(ctx, m, "s1",
		map[string]any{"client_msg_id": "kept", "text": "hello", "credential_id": "untrusted"}, "machine_offline", "")
	if !ok || status != http.StatusAccepted {
		t.Fatalf("keep: %d %s", status, body)
	}
	if kept := k.keeps(); len(kept) != 1 || kept[0].CredentialID != "dev_sender" || !kept[0].Human {
		t.Fatalf("kept: %+v", kept)
	}
}

// When the manager keeps no message, the app gets the failure and a retry may send it.
func TestSendNotKeptFailsAsBefore(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{name: "unsupported method"},
		{name: "null message", reply: `{"undelivered":null}`},
		{name: "missing message", reply: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m, k := undeliveredHub(t, func(context.Context) error { return errors.New("did not start") })
			k.mu.Lock()
			k.noKeep = tc.reply == ""
			k.keepReply = json.RawMessage(tc.reply)
			k.mu.Unlock()
			if w := sendAs(h, m, `{"text":"hi","client_msg_id":"keep-3"}`); w.Code < 500 {
				t.Fatalf("answer: %d %s", w.Code, w.Body)
			}
			if tc.reply != "" && len(k.keeps()) != 1 {
				t.Fatal("the manager did not receive the keep request")
			}
			if st, _ := h.store.ClaimSend(t.Context(), "keep-3", "box", "s1"); st != "" {
				t.Fatalf("send record %q: a retry could not send it", st)
			}
		})
	}
}

// A message without an id cannot go in once later: it is not kept.
func TestSendWithoutIDIsNotKept(t *testing.T) {
	h, m, k := undeliveredHub(t, func(context.Context) error { return errors.New("did not start") })
	if w := sendAs(h, m, `{"text":"hi"}`); w.Code < 500 || len(k.keeps()) != 0 {
		t.Fatalf("answer: %d %s, keeps %d", w.Code, w.Body, len(k.keeps()))
	}
}
