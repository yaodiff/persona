package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// sentLedgerStore is keepStore with the ledger in memory (the database's rows, same paging).
type sentLedgerStore struct {
	*keepStore
	mu     sync.Mutex
	rows   []db.SentAsUser // oldest first, as written
	pruned []time.Time     // the cuts PruneSentAsUser was given
}

func newSentLedgerStore() *sentLedgerStore { return &sentLedgerStore{keepStore: newKeepStore()} }

func (s *sentLedgerStore) RecordSentAsUser(_ context.Context, r db.SentAsUser) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = int64(len(s.rows) + 1)
	s.rows = append(s.rows, r)
	return r.ID, nil
}

func (s *sentLedgerStore) SentAsUsers(_ context.Context, before int64, limit int, connector string) ([]db.SentAsUser, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.SentAsUser
	for i := len(s.rows) - 1; i >= 0; i-- {
		r := s.rows[i]
		if (before == 0 || r.ID < before) && (connector == "" || r.Connector == connector) {
			out = append(out, r)
		}
	}
	if len(out) <= limit {
		return out, 0, nil
	}
	return out[:limit], out[limit-1].ID, nil
}

func (s *sentLedgerStore) PruneSentAsUser(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruned = append(s.pruned, before)
	return 0, nil
}

func (s *sentLedgerStore) SentAsUserByID(_ context.Context, id int64) (db.SentAsUser, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 1 || id > int64(len(s.rows)) {
		return db.SentAsUser{}, false, nil
	}
	return s.rows[id-1], true, nil
}

// MarkSentAsUserRecalled is the database's UPDATE … WHERE recalled_at IS NULL: once, and not with a ctx that is done.
func (s *sentLedgerStore) MarkSentAsUserRecalled(ctx context.Context, id int64, at time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 1 || id > int64(len(s.rows)) || s.rows[id-1].RecalledAt != nil {
		return false, nil
	}
	s.rows[id-1].RecalledAt = &at
	return true, nil
}

func (s *sentLedgerStore) all() []db.SentAsUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.SentAsUser(nil), s.rows...)
}

func objectOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return m
}

// A write that went out is a row with where it went, what it said, the conversation that asked and how it was let
// through: the rule, or the card with its id and who answered it. A write the user declined, and the reply the user
// sent from their own device, are none.
func TestSentLedgerRecordsWhatWentOut(t *testing.T) {
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	h.connectors = h.newConnectors([]connector.Rule{{Connector: "example", Action: "example_send", Match: map[string]any{"chat": "auto"}, Decision: connector.Allow}})
	if err := h.addConnector(newThreadsConnector()); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	answer := func(r proto.Resolution) { hubOf(h).answer = func(proto.Interaction) proto.Resolution { return r } }
	send := func(by asker, args string) proto.ToolResult {
		return h.callConnector(context.Background(), by, "example_send", json.RawMessage(args))
	}

	// The user allowed it on the phone.
	answer(proto.Resolution{Allow: &yes})
	if r := send(asker{machine: "box", session: "conv1"}, `{"chat":"u1","text":"hi there","reply_to":3}`); r.IsError {
		t.Fatalf("send: %+v", r)
	}
	rows := st.all()
	cards := hubOf(h).asked()
	if len(rows) != 1 || len(cards) != 1 {
		t.Fatalf("rows %+v, cards %+v", rows, cards)
	}
	row := rows[0]
	through := objectOf(t, row.LetThrough)
	if row.Connector != "example" || row.Action != "example_send" || row.Text != "hi there" || row.At.IsZero() ||
		!strings.HasPrefix(row.Summary, "发给示例用户") || strings.Contains(row.Summary, "\n") {
		t.Fatalf("row %+v", row)
	}
	if target := objectOf(t, row.Target); target["chat"] != "u1" || target["reply_to"] != float64(3) || len(target) != 2 {
		t.Fatalf("target %s", row.Target)
	}
	if origin := objectOf(t, row.Origin); origin["machine_id"] != "box" || origin["session_id"] != "conv1" || len(origin) != 2 {
		t.Fatalf("origin %s", row.Origin)
	}
	if through["kind"] != "card" || through["interaction_id"] != cards[0].ID || through["answered_by"] != nil || len(through) != 2 {
		t.Fatalf("let through %s, the card was %s", row.LetThrough, cards[0].ID)
	}

	// An assistant answer cannot authorize an outward write on the owner's behalf.
	answer(proto.Resolution{Allow: &yes, AnsweredBy: proto.AnsweredByAssistant})
	result := send(asker{machine: "box"}, `{"chat":"u1","text":"second"}`)
	rows = st.all()
	if !result.IsError || len(rows) != 1 {
		t.Fatalf("assistant authorization sent an outward message: result %+v, rows %+v", result, rows)
	}

	// A rule allowed it: no card, the rule is the reason.
	asked := len(hubOf(h).asked())
	send(asker{machine: "box", session: "conv2"}, `{"chat":"auto","text":"routine"}`)
	rows = st.all()
	if len(rows) != 2 || len(hubOf(h).asked()) != asked {
		t.Fatalf("by rule: %d rows, %d cards", len(rows), len(hubOf(h).asked())-asked)
	}
	through = objectOf(t, rows[1].LetThrough)
	rule, _ := through["rule"].(map[string]any)
	if through["kind"] != "rule" || through["interaction_id"] != nil || rule["connector"] != "example" || rule["action"] != "example_send" ||
		rule["decision"] != "allow" || objectOf(t, rows[1].Target)["chat"] != "auto" {
		t.Fatalf("let through by rule %s", rows[1].LetThrough)
	}

	// Declined: nothing went out, nothing is kept.
	answer(proto.Resolution{Allow: &no})
	if r := send(asker{machine: "box"}, `{"chat":"u1","text":"never sent"}`); !r.IsError || len(st.all()) != 2 {
		t.Fatalf("declined: %+v, %d rows", r, len(st.all()))
	}

	// The user's own reply from their own device runs without the gate and is not a row; the pairing token's goes
	// through the gate (here allowed on the phone) and is, with no conversation behind it.
	mux := h.routes()
	answer(proto.Resolution{Allow: &yes})
	body := `{"thread":"u1","action":"example_send","text":"from the thread page"}`
	if code, out := call(t, mux, "POST", "/api/v1/connectors/example/thread/reply", deviceToken("dev_phone", roleFull), body); code != 200 || len(st.all()) != 2 {
		t.Fatalf("from the user's device: %d %v, %d rows", code, out, len(st.all()))
	}
	if code, out := call(t, mux, "POST", "/api/v1/connectors/example/thread/reply", h.cfg.Token, body); code != 200 {
		t.Fatalf("from the pairing token: %d %v", code, out)
	}
	rows = st.all()
	if len(rows) != 3 || rows[2].Text != "from the thread page" || string(rows[2].Origin) != `{}` {
		t.Fatalf("the pairing token's reply: %+v", rows)
	}
}

// refThreads is threadsConnector that says which message each write made (connector.RefActions).
type refThreads struct {
	*threadsConnector
	ref string
}

func (c *refThreads) DoRef(ctx context.Context, name string, args json.RawMessage) (connector.DoResult, error) {
	text, err := c.threadsConnector.Do(ctx, name, args)
	return connector.DoResult{Text: text, MessageRef: c.ref}, err
}

// The id a connector gives for what it wrote is the row's message_ref, kept as the service wrote it but trimmed, with
// no NUL (Postgres refuses a row that has one); one over 200 characters is not cut (a cut id points at nothing) but
// left empty. A connector that gives none leaves it empty, and the row is written all the same.
func TestSentLedgerKeepsTheMessageRef(t *testing.T) {
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	h.connectors = h.newConnectors([]connector.Rule{{Connector: "*", Action: "*", Decision: connector.Allow}})
	c := &refThreads{threadsConnector: newThreadsConnector()}
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	send := func(ref string) db.SentAsUser {
		t.Helper()
		c.ref = ref
		before := len(st.all())
		if r := h.callConnector(context.Background(), asker{}, "example_send", json.RawMessage(`{"chat":"u1","text":"hi"}`)); r.IsError {
			t.Fatalf("send: %+v", r)
		}
		rows := st.all()
		if len(rows) != before+1 {
			t.Fatalf("%d rows after %d", len(rows), before)
		}
		return rows[before]
	}
	for _, tc := range []struct{ name, ref, want string }{
		{"a plain id", "41", "41"},
		{"an id of the service's own form", "x:123/45", "x:123/45"},
		{"trimmed and without NUL", " om_\x00a1 \n", "om_a1"},
		{"exactly 200 characters", strings.Repeat("长", 200), strings.Repeat("长", 200)},
		{"over 200 characters is none, not cut", strings.Repeat("长", 201), ""},
		{"none", "", ""},
	} {
		if row := send(tc.ref); row.MessageRef != tc.want || row.Text != "hi" {
			t.Errorf("%s: message_ref %q, want %q", tc.name, row.MessageRef, tc.want)
		}
	}

	// A connector without RefActions: the row is there, with no id.
	h2 := newTestManager(t)
	st2 := newSentLedgerStore()
	h2.store = st2
	h2.connectors = h2.newConnectors([]connector.Rule{{Connector: "*", Action: "*", Decision: connector.Allow}})
	if err := h2.addConnector(newThreadsConnector()); err != nil {
		t.Fatal(err)
	}
	if r := h2.callConnector(context.Background(), asker{}, "example_send", json.RawMessage(`{"chat":"u1","text":"hi"}`)); r.IsError {
		t.Fatalf("send: %+v", r)
	}
	if rows := st2.all(); len(rows) != 1 || rows[0].MessageRef != "" || rows[0].Text != "hi" {
		t.Fatalf("without RefActions: %+v", rows)
	}
}

// sendingMail is replyMail whose mail_send goes out with a Message-ID.
type sendingMail struct {
	replyMail
	id string
}

func (m *sendingMail) CallRef(_ context.Context, _ string, args json.RawMessage, _ func(context.Context, string, json.RawMessage) bool) (proto.ToolResult, string) {
	var req mailwire.SendRequest
	_ = json.Unmarshal(args, &req)
	m.sent = append(m.sent, req)
	return proto.TextResult("Mail sent to "+req.To[0].Email+".", false), m.id
}

// A mail the gate let through is a row with the Message-ID it went out with, and the model is told what it always was.
func TestSentLedgerKeepsAMailsMessageID(t *testing.T) {
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	m := &sendingMail{replyMail: *newReplyMail(t), id: "1A2B.3C4D@hub.example.com"}
	h.mail = m
	h.connectors = h.newConnectors([]connector.Rule{{Connector: "mail", Action: "mail_send", Decision: connector.Allow}})
	h.registerConnectors()
	args := json.RawMessage(`{"account_id":"acc1","to":[{"email":"someone@example.com"}],"subject":"季度汇报","text":"附件已更新"}`)
	r := h.callConnector(context.Background(), asker{machine: "box"}, "mail_send", args)
	if r.IsError || r.Content[0].Text != "Mail sent to someone@example.com." || len(m.sent) != 1 {
		t.Fatalf("send: %+v, sent %v", r, m.sent)
	}
	rows := st.all()
	if len(rows) != 1 || rows[0].Connector != "mail" || rows[0].Action != "mail_send" || rows[0].MessageRef != "1A2B.3C4D@hub.example.com" ||
		objectOf(t, rows[0].Target)["subject"] != "季度汇报" {
		t.Fatalf("rows %+v", rows)
	}
}

// Without a database nothing is kept and the write still goes out.
func TestSentLedgerWithoutADatabase(t *testing.T) {
	h := newTestManager(t)
	h.store = newKeepStore()
	h.connectors = h.newConnectors(nil)
	c := newThreadsConnector()
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	if r := h.callConnector(context.Background(), asker{}, "example_send", json.RawMessage(`{"chat":"u1","text":"x"}`)); r.IsError || len(c.sent) != 1 {
		t.Fatalf("%+v %v", r, c.sent)
	}
}

// Where a write went is read from the arguments the connector was given.
func TestSentTargets(t *testing.T) {
	for _, c := range []struct {
		name, action, args string
		target             string
		brief              string
	}{
		{"a GitHub comment", "github_comment", `{"repo":"example-org/example-repo","number":12,"body":"x"}`,
			`{"ref":"example-org/example-repo#12"}`, "example-org/example-repo#12"},
		{"a GitHub review", "github_review", `{"repo":"example-org/example-repo","number":12,"event":"APPROVE"}`,
			`{"event":"APPROVE","ref":"example-org/example-repo#12"}`, "example-org/example-repo#12 APPROVE"},
		{"a GitHub thread reply", "github_comment", `{"chat":"example-org/example-repo#12","text":"x"}`,
			`{"ref":"example-org/example-repo#12"}`, "example-org/example-repo#12"},
		{"mail", "mail_send", `{"account_id":"acc1","to":[{"name":"Someone","email":"someone@example.com"}],"cc":[{"email":"other@example.com"}],"subject":"季度汇报","text":"t"}`,
			`{"account_id":"acc1","cc":["other@example.com"],"subject":"季度汇报","to":["someone@example.com"]}`, "to someone@example.com: 季度汇报"},
		{"an extension's chat", "example_send", `{"chat":"u1","text":"secret words","reply_to":3,"extra":{"a":1}}`,
			`{"chat":"u1","reply_to":3}`, "chat u1, reply_to 3"},
		{"a list of recipients", "example_send", `{"to":["a@example.com","b@example.com"],"body":"x"}`,
			`{"to":["a@example.com","b@example.com"]}`, "to [a@example.com b@example.com]"},
		{"none of the usual fields", "example_post", `{"title":"t","body":"b"}`, `{"arg_keys":["body","title"]}`, ""},
		{"a GitHub write it cannot read", "github_comment", `{"body":"x"}`, `{"arg_keys":["body"]}`, ""},
		{"mail it cannot read", "mail_send", `{"to":"not a list"}`, `{"to":"not a list"}`, "to not a list"},
		{"mail it cannot read, no destination", "mail_send", `{"text":"t"}`, `{"arg_keys":["text"]}`, ""},
		{"arguments that are not an object", "example_send", `[1]`, `{}`, ""},
		{"no arguments", "example_send", `{}`, `{}`, ""},
	} {
		target, brief := sentTarget(c.action, json.RawMessage(c.args))
		got, _ := json.Marshal(target)
		if string(got) != c.target || brief != c.brief {
			t.Errorf("%s: %s (%q), want %s (%q)", c.name, got, brief, c.target, c.brief)
		}
	}
}

// What goes into a row is text Postgres takes, and a long value is cut.
func TestSentRowIsCleanAndShort(t *testing.T) {
	long := strings.Repeat("长", sentValueMost+50)
	row := sentRow(connector.Sent{Connector: "example", Action: "example_send", Text: "a\x00b\xffc",
		Args:    json.RawMessage(`{"chat":"c\u0000d","subject":"` + long + `"}`),
		Through: connector.LetThrough{Kind: connector.ThroughCard, Summary: "\n  " + strings.Repeat("字", 500) + "\nsecond line"}}, nil)
	if row.Text != "abc" || strings.Contains(string(row.Target), `\u0000`) || objectOf(t, row.Target)["chat"] != "cd" {
		t.Fatalf("text %q, target %s", row.Text, row.Target)
	}
	if got := []rune(objectOf(t, row.Target)["subject"].(string)); len(got) != sentValueMost {
		t.Fatalf("a long value is %d characters", len(got))
	}
	if n := len([]rune(row.Summary)); n != sentSummaryMost || strings.Contains(row.Summary, "second") {
		t.Fatalf("summary of %d characters: %.30q", n, row.Summary)
	}
	if string(row.Origin) != `{}` || objectOf(t, row.LetThrough)["kind"] != "card" || objectOf(t, row.LetThrough)["interaction_id"] != nil {
		t.Fatalf("origin %s, let through %s", row.Origin, row.LetThrough)
	}
	// A rule's write with no card says what it was and where it went.
	row = sentRow(connector.Sent{Connector: "github", Action: "github_comment", Args: json.RawMessage(`{"repo":"o/r","number":2,"body":"x"}`),
		Through: connector.LetThrough{Kind: connector.ThroughRule, Rule: &connector.Rule{Connector: "github", Action: "*", Decision: connector.Allow}}}, nil)
	if row.Summary != "github_comment o/r#2" {
		t.Fatalf("summary %q", row.Summary)
	}
}

// GET /api/v1/connectors/sent pages the ledger newest first with next, filters by connector, and is for a device with
// full rights only: not an answer-only device, not an extension's token.
func TestSentLedgerRoute(t *testing.T) {
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	for i := range 5 {
		c := "github"
		if i%2 == 1 {
			c = "example"
		}
		if _, err := st.RecordSentAsUser(context.Background(), db.SentAsUser{Connector: c, Action: c + "_x", Text: fmt.Sprint("row ", i),
			Target: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`), LetThrough: json.RawMessage(`{"kind":"rule"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	mux := h.routes()
	read := func(path string) (int, []string, any) {
		t.Helper()
		code, out := call(t, mux, "GET", path, h.cfg.Token, "")
		var texts []string
		items, _ := out["items"].([]any)
		for _, it := range items {
			texts = append(texts, it.(map[string]any)["text"].(string))
		}
		if code == 200 {
			if _, ok := out["items"].([]any); !ok {
				t.Fatalf("%s: items is %v", path, out["items"])
			}
			if _, ok := out["next"]; !ok {
				t.Fatalf("%s: no next in %v", path, out)
			}
		}
		return code, texts, out["next"]
	}
	if code, texts, next := read("/api/v1/connectors/sent"); code != 200 || fmt.Sprint(texts) != "[row 4 row 3 row 2 row 1 row 0]" || next != nil {
		t.Fatalf("all: %d %v next %v", code, texts, next)
	}
	code, texts, next := read("/api/v1/connectors/sent?limit=2")
	if code != 200 || fmt.Sprint(texts) != "[row 4 row 3]" || next != float64(4) {
		t.Fatalf("first page: %d %v next %v", code, texts, next)
	}
	if code, texts, next = read("/api/v1/connectors/sent?limit=2&before=4"); code != 200 || fmt.Sprint(texts) != "[row 2 row 1]" || next != float64(2) {
		t.Fatalf("second page: %d %v next %v", code, texts, next)
	}
	if code, texts, next = read("/api/v1/connectors/sent?limit=2&before=2"); code != 200 || fmt.Sprint(texts) != "[row 0]" || next != nil {
		t.Fatalf("last page: %d %v next %v", code, texts, next)
	}
	if code, texts, _ = read("/api/v1/connectors/sent?connector=example"); code != 200 || fmt.Sprint(texts) != "[row 3 row 1]" {
		t.Fatalf("one connector's: %d %v", code, texts)
	}
	if code, texts, next = read("/api/v1/connectors/sent?connector=nobody"); code != 200 || texts != nil || next != nil {
		t.Fatalf("nobody's: %d %v %v", code, texts, next)
	}
	if code, texts, _ = read("/api/v1/connectors/sent?limit=5000"); code != 200 || len(texts) != 5 {
		t.Fatalf("too many asked for: %d %v", code, texts)
	}
	for _, bad := range []string{"limit=0", "limit=-1", "limit=ten", "before=0", "before=x"} {
		if code, _, _ := read("/api/v1/connectors/sent?" + bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}

	as := func(p principal) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/connectors/sent", nil)
		h.apiMux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), p)))
		return rec.Code
	}
	if code := as(principal{ID: "dev_phone", Role: roleFull}); code != 200 {
		t.Fatalf("a full device: %d", code)
	}
	if code := as(principal{ID: "dev_watch", Role: roleAnswer}); code != 403 {
		t.Fatalf("an answer-only device: %d", code)
	}
	if code := as(principal{ID: "ext_example", Role: roleFull, Extension: "example"}); code != 403 {
		t.Fatalf("an extension's token: %d", code)
	}
	if code := as(principal{ID: "tok_script", Role: roleFull, Paired: true}); code != 403 {
		t.Fatalf("a pairing token: %d", code)
	}
	if code := as(principal{Role: roleFull, Owner: true}); code != 200 {
		t.Fatalf("the owner on this machine: %d", code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/connectors/sent", deviceToken("dev_watch", roleAnswer), ""); code != 403 {
		t.Fatalf("an answer-only device, by its token: %d", code)
	}

	h.store = newKeepStore() // no database
	if code, _, _ := read("/api/v1/connectors/sent"); code != 503 {
		t.Fatalf("without a database: %d", code)
	}
}

// The ledger is pruned to hub.json's sent_ledger_days: 90 when it says nothing, never when it is below 0. The cut
// is exactly that many days of 24 h back, also right after a DST change (US DST ended 2026-11-01): counted in the
// clock's zone it was an hour off.
func TestSentLedgerPrune(t *testing.T) {
	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, zone) // after the DST change; 7 and 90 days back are before it
	sentLedgerNow = func() time.Time { return now }
	t.Cleanup(func() { sentLedgerNow = time.Now })
	for _, c := range []struct {
		days int
		want int // days the cut is back from now; 0: no cut
	}{{0, 90}, {7, 7}, {-1, 0}} {
		h := newTestManager(t)
		st := newSentLedgerStore()
		h.store = st
		h.cfg.SentLedgerDays = c.days
		h.pruneSentLedger(context.Background())
		if c.want == 0 {
			if len(st.pruned) != 0 {
				t.Errorf("sent_ledger_days %d pruned: %v", c.days, st.pruned)
			}
			continue
		}
		if len(st.pruned) != 1 {
			t.Errorf("sent_ledger_days %d: %d cuts", c.days, len(st.pruned))
			continue
		}
		if back := now.Sub(st.pruned[0]); back != time.Duration(c.want)*24*time.Hour {
			t.Errorf("sent_ledger_days %d cut %v ago, want %v", c.days, back, time.Duration(c.want)*24*time.Hour)
		}
	}
}
