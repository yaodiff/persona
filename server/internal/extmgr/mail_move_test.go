package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/proto"
)

const mailMoveArgs = `{"account_id":"acc1","mailbox":"INBOX","uid":7,"uid_validity":2,"to":"archive"}`
const mailMoveSummary = `Move an email in "me@example.com" from "INBOX" to "Archive"`

type mockMailMove struct {
	Mail
	applied atomic.Int32
}

func (m *mockMailMove) PrepareMove(_ context.Context, args json.RawMessage) (string, mailwire.PreparedMove, error) {
	if string(args) != mailMoveArgs {
		panic("move arguments changed before preparing the approval")
	}
	return mailMoveSummary, func(context.Context) (mailwire.MoveResult, error) {
		m.applied.Add(1)
		return mailwire.MoveResult{Mailbox: "Archive", UID: 8, UIDValidity: 3}, nil
	}, nil
}

func TestMailMoveRequiresApproval(t *testing.T) {
	for _, tc := range []struct {
		name      string
		assistant bool
		allow     bool
		cancel    bool
	}{
		{"agent denied", false, false, false},
		{"assistant approved", true, true, false},
		{"agent approved", false, true, false},
		{"assistant canceled with a late approval", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestManager(t)
			mail := &mockMailMove{Mail: testMail(t)}
			h.mail = mail
			ledger := newSentLedgerStore()
			h.store = ledger
			h.connectors = h.newConnectors([]connector.Rule{{Connector: "mail", Action: "*", Decision: connector.Allow}})
			h.registerConnectors()
			card := make(chan proto.Interaction, 1)
			answer := make(chan struct{})
			h.opts.Ask = func(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
				card <- ix
				select {
				case <-answer:
				case <-ctx.Done():
					// A late allow must not execute a write for the canceled call.
				}
				return proto.Resolution{Allow: &tc.allow}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			args := json.RawMessage(mailMoveArgs)
			done := make(chan proto.ToolResult, 1)
			go func() {
				res, handled := h.CallTool(ctx, "box", "conv1", mailwire.ToolMove, args, tc.assistant)
				if !handled {
					t.Error("mail_move was not registered for this caller")
				}
				done <- res
			}()
			select {
			case ix := <-card:
				if ix.ToolName != mailwire.ToolMove || ix.Summary != mailMoveSummary || !bytes.Equal(ix.ToolInput, args) || ix.SessionID != "conv1" {
					t.Fatalf("approval card = %+v", ix)
				}
			case res := <-done:
				t.Fatalf("move returned without asking: %+v", res)
			case <-ctx.Done():
				t.Fatal("approval card did not appear")
			}
			if n := mail.applied.Load(); n != 0 {
				t.Fatalf("waiting for approval applied %d moves", n)
			}
			if tc.cancel {
				cancel()
			}
			close(answer)
			select {
			case res := <-done:
				if tc.allow && !tc.cancel {
					if res.IsError || mail.applied.Load() != 1 || res.Content[0].Text != `{"mailbox":"Archive","uid":8,"uid_validity":3}` {
						t.Fatalf("approved: result = %+v, moves = %d", res, mail.applied.Load())
					}
				} else if !res.IsError || mail.applied.Load() != 0 {
					t.Fatalf("no approval: result = %+v, moves = %d", res, mail.applied.Load())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("move did not return after the card was answered")
			}
			if rows := ledger.all(); len(rows) != 0 {
				t.Fatalf("move was recorded as a send: %+v", rows)
			}
		})
	}
}

func TestMailMoveRejectsUngatedCalls(t *testing.T) {
	h := newTestManager(t)
	mail := &mockMailMove{Mail: testMail(t)}
	h.mail = mail
	h.connectors = h.newConnectors(nil)
	h.registerConnectors()
	ctx := context.Background()
	args := json.RawMessage(mailMoveArgs)
	if res := h.connectors.Run(ctx, mailwire.ToolMove, args); !res.IsError || mail.applied.Load() != 0 {
		t.Fatalf("ungated registry call = %+v, moves = %d", res, mail.applied.Load())
	}
	if _, err := (mailConnector{h: h}).DoRef(ctx, mailwire.ToolMove, args); err == nil || mail.applied.Load() != 0 {
		t.Fatalf("ungated connector call: err = %v, moves = %d", err, mail.applied.Load())
	}
	if _, ok, err := (mailConnector{h: h}).CallJSON(ctx, mailwire.ToolMove, args); ok || err != nil {
		t.Fatalf("mail move exposed as a JSON read: ok = %v, err = %v", ok, err)
	}
}

func TestMailMoveHTTPRequiresAuthentication(t *testing.T) {
	h := newTestManager(t)
	h.mail = testMail(t)
	handler := h.routes()
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"", http.StatusUnauthorized},
		{"invalid", http.StatusUnauthorized},
		{h.cfg.Token, http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/acc1/0/move", strings.NewReader(`{}`))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("move HTTP %d %s, want %d", rec.Code, rec.Body, tc.status)
		}
	}
}
