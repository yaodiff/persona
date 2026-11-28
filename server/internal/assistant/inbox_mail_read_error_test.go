package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// collectedJSON hands the inbox a round as the wire carries it (inbox.collected through the kernel's decoding).
func collectedJSON(t *testing.T, h *inboxHub, round string) {
	t.Helper()
	if _, ok, err := h.kernel.Answer(context.Background(), assistantwire.MethodInboxCollected, json.RawMessage(round)); !ok || err != nil {
		t.Fatalf("inbox.collected: %v %v", ok, err)
	}
}

// mailAccountJSON is one mail account with one unread mail, as a round lists it.
func mailAccountJSON(id, email string, uid int) string {
	return fmt.Sprintf(`{"id":%q,"email":%q,"messages":[{"mailbox":"INBOX","uid":%d,"message_id":"<%d@example.com>",`+
		`"subject":"Report %d","from":{"name":"Bob","email":"bob@example.com"},"to":[{"name":"","email":%q}],`+
		`"date":%q,"snippet":"hello","seen":false}]}`, id, email, uid, uid, uid, email, time.Now().UTC().Format(time.RFC3339))
}

// A mail account that repeatedly fails to read shows in the inbox status like a connector that could not be read
// (#1849): GET /api/v1/inbox names it in triage.sources (source "mail:<account>", its address as the name) with the
// read code and the words for it, and its mail stays listed. A round without mail (a poll) leaves that as it was; the
// next mail round that reads the account clears it.
func TestInboxSaysWhichMailAccountCouldNotBeRead(t *testing.T) {
	h := newInboxHub(t)
	names := `"names":{"mail":"Mail"}`
	collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[`+
		mailAccountJSON("acc-me", "me@example.com", 1)+`,`+mailAccountJSON("acc-work", "work@example.com", 1)+`]}}`)
	if items, tr := inboxTriageOf(t, h); len(items) != 2 || len(tr.Sources) != 0 {
		t.Fatalf("after a round that read both accounts: %d items, triage %+v", len(items), tr)
	}

	for range mailNoticeFailures {
		collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[`+mailAccountJSON("acc-me", "me@example.com", 1)+
			`],"failed":[{"id":"acc-work","email":"work@example.com","read_error":"timeout"}]}}`)
	}
	items, tr := inboxTriageOf(t, h)
	if len(items) != 2 {
		t.Fatalf("the account that could not be read lost its mail: %d items", len(items))
	}
	want := InboxTriageSource{Source: "mail:acc-work", Name: "work@example.com", State: "ok", ReadError: assistantwire.ReadTimeout,
		ReadReason: i18n.T(h.lang(context.Background()), "assistant-inbox_admit.read_timeout")}
	if len(tr.Sources) != 1 || tr.Sources[0] != want || tr.State != "ok" || tr.Waiting != 0 {
		t.Fatalf("triage after a mail account could not be read: %+v, want the source %+v", tr, want)
	}

	collectedJSON(t, h, `{"round":"poll","sources":[],`+names+`}`)
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 1 || tr.Sources[0].Source != "mail:acc-work" {
		t.Fatalf("a round without mail cleared the mail account's failure: %+v", tr)
	}

	collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[`+
		mailAccountJSON("acc-me", "me@example.com", 1)+`,`+mailAccountJSON("acc-work", "work@example.com", 1)+`]}}`)
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 0 {
		t.Fatalf("a mail account read again still says it failed: %+v", tr)
	}
}

// Each code has its words: a refused login says so, a code this build does not know reads as failed (no raw i18n
// key reaches the apps). A change in which accounts failed is told to the apps (an inbox push) even when no item
// changed; mail itself gone (no "mail" among the round's connectors) takes its accounts' failures with it.
func TestInboxMailAccountReadCodes(t *testing.T) {
	h := newInboxHub(t)
	l := h.lang(context.Background())
	names := `"names":{"mail":"Mail"}`
	pushes := func() int {
		h.in.mu.Lock()
		defer h.in.mu.Unlock()
		return len(h.in.changed)
	}
	before := pushes()
	for range mailNoticeFailures {
		collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[],"failed":[`+
			`{"id":"acc-me","email":"me@example.com","read_error":"login_refused"},`+
			`{"id":"acc-work","email":"work@example.com","read_error":"a_code_from_a_newer_hub"}]}}`)
	}
	_, tr := inboxTriageOf(t, h)
	want := []InboxTriageSource{
		{Source: "mail:acc-me", Name: "me@example.com", State: "ok", ReadError: assistantwire.ReadLoginRefused,
			ReadReason: i18n.T(l, "assistant-inbox_admit.read_login_refused")},
		{Source: "mail:acc-work", Name: "work@example.com", State: "ok", ReadError: assistantwire.ReadFailed,
			ReadReason: i18n.T(l, "assistant-inbox_admit.read_failed")},
	}
	if len(tr.Sources) != 2 || tr.Sources[0] != want[0] || tr.Sources[1] != want[1] {
		t.Fatalf("triage sources %+v, want %+v", tr.Sources, want)
	}
	for _, x := range tr.Sources {
		if x.ReadReason == "" || x.ReadReason == "assistant-inbox_admit.read_"+x.ReadError {
			t.Fatalf("no words for %s: %+v", x.ReadError, x)
		}
	}
	if pushes() == before {
		t.Fatal("which mail accounts could not be read changed, and the apps were not told")
	}

	collectedJSON(t, h, `{"round":"poll","sources":[],"names":{}}`)
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 0 {
		t.Fatalf("mail is gone and its accounts still say they failed: %+v", tr)
	}
}

// A round from a hub that does not name failed accounts (it only leaves them out, as before #1849) reads as it always
// did: the account's mail stays and the status names nothing; a later round from it clears what a newer hub said.
func TestInboxMailRoundWithoutFailedAccounts(t *testing.T) {
	h := newInboxHub(t)
	names := `"names":{"mail":"Mail"}`
	collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[`+
		mailAccountJSON("acc-me", "me@example.com", 1)+`,`+mailAccountJSON("acc-work", "work@example.com", 1)+`]}}`)
	collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[`+mailAccountJSON("acc-me", "me@example.com", 1)+`]}}`)
	if items, tr := inboxTriageOf(t, h); len(items) != 2 || len(tr.Sources) != 0 || tr.State != "ok" {
		t.Fatalf("an account left out by an older hub: %d items, triage %+v", len(items), tr)
	}

	collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[],"failed":[`+
		`{"id":"acc-work","email":"work@example.com","read_error":"timeout"}]}}`)
	collectedJSON(t, h, `{"round":"full","sources":[],`+names+`,"mail":{"accounts":[`+mailAccountJSON("acc-me", "me@example.com", 1)+`]}}`)
	if _, tr := inboxTriageOf(t, h); len(tr.Sources) != 0 {
		t.Fatalf("a mail round without failed accounts kept an earlier one's: %+v", tr)
	}
}
