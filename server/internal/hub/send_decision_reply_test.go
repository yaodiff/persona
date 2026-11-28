package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The normal Agent send route must tell the manager even before the node finishes delivery.
// Waiting for a later turn-start hook leaves a pending decision's default free to run during startup.
func TestAgentSendTellsManagerTheOwnerSpoke(t *testing.T) {
	for _, tc := range []struct {
		name     string
		who      *principal
		text     string
		source   string
		terminal bool
	}{
		{"owner app", &principal{ID: "device-1", Role: roleFull}, "B", assistantwire.OwnerInputApp, false},
		{"owner terminal", &principal{ID: "device-2", Role: roleFull}, "B", assistantwire.OwnerInputTerminal, true},
		{"hub token", &principal{Role: roleFull}, "", "", false},
		{"extension", &principal{ID: "extension-1", Role: roleExtension, Extension: "helper"}, "", "", false},
		{"link", &principal{ID: "link-1", Role: roleLink, Machine: "box"}, "", "", false},
		{"no credential", nil, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = newSentStore()
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			pipeNode(t, h, "box", func(ctx context.Context) error {
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			m := h.byID["box"]
			editSessions(m, func([]proto.Session) []proto.Session {
				return []proto.Session{{SessionID: "s1", Agent: "codex", Status: proto.StatusWorking}}
			})
			h.bus.idents = &memIdents{}
			mux := http.NewServeMux()
			h.busRoutes(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			manager := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(string, json.RawMessage) (any, error) {
				return map[string]any{}, nil
			})
			spoke := make(chan assistantwire.UserSpokeParams, 1)
			manager.mu.Lock()
			manager.onEvent = func(method string, raw json.RawMessage) {
				if method == assistantwire.EventUserSpoke {
					var r assistantwire.UserSpokeParams
					if json.Unmarshal(raw, &r) == nil {
						spoke <- r
					}
				}
			}
			manager.mu.Unlock()
			rec := httptest.NewRecorder()
			body := `{"text":"B","human":true,"client_msg_id":"decision-reply-1"}`
			if tc.terminal {
				body = `{"text":"B","hold_when_away":false,"client_msg_id":"terminal-reply-1"}`
			}
			r := httptest.NewRequest(http.MethodPost, sendPath, strings.NewReader(body))
			if tc.who != nil {
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, *tc.who))
			}
			if tc.terminal {
				r = r.WithContext(context.WithValue(r.Context(), terminalAgentSendKey{}, true))
			}
			r.SetPathValue("sid", "s1")
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.sendMessage(rec, r, m)
			}()
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case got := <-spoke:
				if got.MachineID != "box" || got.SessionID != "s1" {
					t.Fatalf("wrong owner-message target: %+v", got)
				}
				if got.Text != tc.text {
					t.Fatalf("owner text for %s: %q, want %q", tc.name, got.Text, tc.text)
				}
				if got.Source != tc.source || got.At.IsZero() {
					t.Fatalf("owner provenance for %s: %+v, want source %q", tc.name, got, tc.source)
				}
				// The spoke names the send with its text (#13041), as the send's session.assistant_sent does.
				want := ""
				if tc.text != "" {
					want = "decision-reply-1"
					if tc.terminal {
						want = "terminal-reply-1"
					}
				}
				if got.ClientMsgID.String() != want {
					t.Fatalf("owner client_msg_id for %s (#13041): %+v, want %q", tc.name, got, want)
				}
			case <-timer.C:
				t.Fatal("the Agent send did not tell the manager the owner spoke")
			}
			unblock()
			select {
			case <-done:
				if rec.Code != http.StatusOK {
					t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
				}
			case <-timer.C:
				t.Fatal("the Agent send did not finish")
			}
		})
	}
}

// An owner message to the manager's own assistant (supervisor / direction) conversation also
// carries its text on the user_spoke event. That path is how needs_user decision cards raised
// inside a direction conversation get answered there: the event reaches the manager before the
// message is persisted, so the text cannot be recovered from history at that point.
func TestAssistantConversationSendCarriesOwnerText(t *testing.T) {
	for _, tc := range []struct {
		name string
		who  *principal
		text string
	}{
		{"owner app", &principal{ID: "device-1", Role: roleFull}, "直接全量"},
		{"hub token", &principal{Role: roleFull}, ""},
		{"extension", &principal{ID: "extension-1", Role: roleExtension, Extension: "helper"}, ""},
		{"no credential", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = newSentStore()
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			pipeNode(t, h, "box", func(ctx context.Context) error {
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			m := h.byID["box"]
			m.info.Home = "/home/me"
			editSessions(m, func([]proto.Session) []proto.Session {
				// convDir is this machine's assistant directory, so the session is an assistant one.
				return []proto.Session{{SessionID: "dir1", Cwd: convDir, Assistant: true, Status: proto.StatusIdle}}
			})
			h.bus.idents = &memIdents{}
			mux := http.NewServeMux()
			h.busRoutes(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			manager := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, raw json.RawMessage) (any, error) {
				if method == assistantwire.MethodSendParams { // the manager keeps the message's fields, client_msg_id too
					var p assistantwire.SendParams
					if err := json.Unmarshal(raw, &p); err != nil {
						return nil, err
					}
					return map[string]any{"params": p.Params}, nil
				}
				return map[string]any{}, nil
			})
			spoke := make(chan assistantwire.UserSpokeParams, 1)
			sent := make(chan assistantwire.ConversationSent, 1)
			manager.mu.Lock()
			manager.onEvent = func(method string, raw json.RawMessage) {
				switch method {
				case assistantwire.EventUserSpoke:
					var r assistantwire.UserSpokeParams
					if json.Unmarshal(raw, &r) == nil {
						spoke <- r
					}
				case assistantwire.EventConversationSent:
					var r assistantwire.ConversationSent
					if json.Unmarshal(raw, &r) == nil {
						sent <- r
					}
				}
			}
			manager.mu.Unlock()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, sendPath, strings.NewReader(`{"text":"直接全量","human":true,"client_msg_id":"dir-reply-1"}`))
			if tc.who != nil {
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, *tc.who))
			}
			r.SetPathValue("sid", "dir1")
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.sendMessage(rec, r, m)
			}()
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case got := <-spoke:
				if got.SessionID != "dir1" {
					t.Fatalf("wrong target: %+v", got)
				}
				if got.Text != tc.text {
					t.Fatalf("assistant-conversation owner text for %s: %q, want %q", tc.name, got.Text, tc.text)
				}
				// The owner's text names its send (#13041); owner provenance (#15641) is for Agents only.
				if want := map[bool]string{true: "dir-reply-1"}[tc.text != ""]; got.ClientMsgID.String() != want || got.Source != "" || got.At.IsZero() {
					t.Fatalf("assistant-conversation spoke for %s: %+v, want client_msg_id %q and no source", tc.name, got, want)
				}
			case <-timer.C:
				t.Fatal("the assistant-conversation send did not tell the manager the owner spoke")
			}
			unblock()
			<-done
			// The send's outcome names the same message, so the manager can join the two (#13041).
			select {
			case got := <-sent:
				if got.ClientMsgID != "dir-reply-1" || got.SessionID != "dir1" || got.Outcome != assistantwire.ConversationDelivered {
					t.Fatalf("the send's outcome cannot be joined to its spoke: %+v (status %d %s)", got, rec.Code, rec.Body.String())
				}
			case <-timer.C:
				t.Fatalf("no session.assistant_sent for the send (status %d %s)", rec.Code, rec.Body.String())
			}
		})
	}
}
