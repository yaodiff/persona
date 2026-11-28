package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

type entriesHub struct {
	*fakeHub
	events []assistantwire.AssistantEntriesChanged
}

func (h *entriesHub) EntriesEvent(_ string, p any) error {
	h.events = append(h.events, p.(assistantwire.AssistantEntriesChanged))
	return nil
}

func TestEntriesHTTPRPCAndWrongEvent(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	ctx := context.Background()
	e.f.setSessions("box", proto.Session{SessionID: "main-example", Assistant: true})
	fixture := proto.AssistantEntry{ID: "entry-example", Type: "rule", Text: "Use the example workflow.", Scope: proto.AssistantEntryScope{Executor: "system"}, By: "agent", Trust: "agent", Status: "active"}
	if _, err := database.AppendAssistantEntry(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	var rpcOut proto.AssistantEntriesResult
	if err := askConversations(t, e.s, assistantwire.MethodAssistantEntriesList, `{"by":"agent","tag":"example"}`, &rpcOut); err != nil {
		t.Fatal(err)
	}
	if len(rpcOut.Entries) != 0 {
		t.Fatal("tag filter ignored")
	}
	if err := askConversations(t, e.s, assistantwire.MethodAssistantEntriesList, `{"by":"agent","scope":{"executor":"system"}}`, &rpcOut); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/v1/assistant/entries?by=agent&scope="+url.QueryEscape(`{"executor":"system"}`), nil)
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, request)
	if err := wire.ValidateResult(assistantwire.MethodAssistantEntriesList, w.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	var httpOut proto.AssistantEntriesResult
	if err := json.Unmarshal(w.Body.Bytes(), &httpOut); err != nil || w.Code != 200 || !reflect.DeepEqual(rpcOut, httpOut) {
		t.Fatalf("HTTP != RPC: %d %s / %+v %v", w.Code, w.Body.String(), rpcOut, err)
	}
	h := &entriesHub{fakeHub: e.f}
	e.s.hub = h
	status, _ := serveJSON(t, e.s, "POST", "/api/v1/assistant/entries/entry-example/wrong", "", "")
	if status != 200 || len(h.events) != 1 || h.events[0].ID != fixture.ID || h.events[0].Status != "wrong" {
		t.Fatalf("wrong: %d %+v", status, h.events)
	}
	before := h.events[0].RecordID
	status, _ = serveJSON(t, e.s, "POST", "/api/v1/assistant/entries/entry-example/wrong", "", "")
	if status != 200 || h.events[len(h.events)-1].RecordID != before {
		t.Fatal("wrong retry appended twice")
	}
	for _, query := range []string{"cursor=x", "created_at=x", "by=robot", "limit=no", "limit=201", "scope=%7B%22unknown%22%3Atrue%7D", "by=user&by=agent"} {
		w := httptest.NewRecorder()
		e.s.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/entries?"+query, nil))
		if w.Code != 400 {
			t.Errorf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	denied := httptest.NewRequest("POST", "/api/v1/assistant/entries/entry-example/wrong", nil)
	denied.Header.Set(RoleHeader, roleExtension)
	w = httptest.NewRecorder()
	e.s.ServeHTTP(w, denied)
	if w.Code != http.StatusForbidden {
		t.Fatalf("extension marked wrong: %d", w.Code)
	}
	for _, method := range []string{"assistant.entries.write", "assistant.entries.withdraw"} {
		if _, ok, _ := e.s.conversationsRequest(ctx, method, json.RawMessage(`{}`)); ok {
			t.Fatalf("I6 exposed: %s", method)
		}
	}
}

func TestEntriesPersonalInstanceBoundary(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	ctx := context.Background()
	personal := proto.AssistantEntry{ID: "entry-personal", Type: "preference", Text: "Prefer concise replies.", Scope: proto.AssistantEntryScope{Executor: "any", Personal: true}, By: "agent", Trust: "inferred", Status: "active"}
	if _, err := database.AppendAssistantEntry(ctx, personal); err != nil {
		t.Fatal(err)
	}
	// Test the transport's authenticated local ping, not a machine named by a filter.
	owner := e.s.settings(ctx).MachineID
	if owner == "" {
		t.Fatal("fixture has no owner machine")
	}
	for _, tc := range []struct {
		name, raw string
		err       error
		want      int
	}{
		{"same machine", `{"ok":true,"machine_id":"` + owner + `"}`, nil, 1},
		{"other machine", `{"ok":true,"machine_id":"elsewhere"}`, nil, 0},
		{"older node", `{"ok":true}`, nil, 0},
		{"offline", "", errors.New("offline"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.s.entriesDial = func(context.Context, any) ([]byte, error) { return []byte(tc.raw), tc.err }
			var out proto.AssistantEntriesResult
			if err := askConversations(t, e.s, assistantwire.MethodAssistantEntriesList, `{"scope":{"personal":true}}`, &out); err != nil || len(out.Entries) != tc.want {
				t.Fatalf("got %+v %v", out, err)
			}
		})
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/assistant/entries?scope="+url.QueryEscape(`{"personal":true}`), nil)
	r.Header.Set(RoleHeader, roleExtension)
	e.s.ServeHTTP(w, r)
	var out proto.AssistantEntriesResult
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 403 {
		t.Fatalf("HTTP boundary: %d %s", w.Code, w.Body.String())
	}
}

func TestEntriesCardAnswerStructuredAndDefaultsExcluded(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	ctx := context.Background()
	yes := true
	ix := proto.Interaction{ID: "card-example", MachineID: "box", SessionID: "main-example", Kind: proto.IxQuestion, ToolName: "ask_user", Reversible: &yes,
		Questions: []proto.Question{{Question: "Which release path?", Options: []proto.QuestionOption{{Label: "Stage"}, {Label: "Release"}}}}}
	answer := proto.Resolution{Answers: map[string]string{"Which release path?": "Stage"}}
	if err := e.s.ledgerCardDecisions(ctx, ix, answer, "resolved"); err != nil {
		t.Fatal(err)
	}
	out, err := database.ListAssistantEntries(ctx, proto.AssistantEntriesParams{By: "user", Type: "decision"}, true)
	if err != nil || len(out.Entries) != 1 {
		t.Fatalf("card result: %+v %v", out, err)
	}
	got := out.Entries[0]
	if got.Source.Card != ix.ID || got.Source.Machine != "box" || got.Quote != "Stage" || got.Decision == nil || !reflect.DeepEqual(got.Decision.Rejected, []string{"Release"}) || got.Decision.Reversible == nil || !*got.Decision.Reversible {
		t.Fatalf("structured card: %+v", got)
	}
	if err := e.s.ledgerCardDecisions(ctx, ix, answer, "resolved"); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{proto.AnsweredByAssistant, proto.AnsweredByDefault, proto.AnsweredByTerminal} {
		ix.ID = "card-" + actor
		answer.AnsweredBy = actor
		if err := e.s.ledgerCardDecisions(ctx, ix, answer, "resolved"); err != nil {
			t.Fatal(err)
		}
	}
	out, err = database.ListAssistantEntries(ctx, proto.AssistantEntriesParams{}, true)
	if err != nil || len(out.Entries) != 1 {
		t.Fatalf("automatic answers entered as user: %+v %v", out, err)
	}
}

func TestEntriesAskUserAnswerIsQueryableAndModelQuotesStayInferred(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ctx := context.Background()
	ix, _ := e.askLater(askLaterArgs)
	e.mustEnd(ix.ID, `{"answers":{"Merge the release PR now?":"Wait"}}`)
	raw, err := e.s.toolEntries(ctx, "server", "c1x1x1", "decisions_query", json.RawMessage(`{"by":"user"}`))
	var out proto.AssistantEntriesResult
	if err != nil || json.Unmarshal([]byte(raw), &out) != nil || len(out.Entries) != 1 || out.Entries[0].Source.Card != ix.ID {
		t.Fatalf("query after ask_user: %s %v", raw, err)
	}
	input := json.RawMessage(`{"entry":{"text":"Stage the example release.","decision":{"options":["Stage","Release"],"selected":["Stage"],"rejected":["Release"],"reason":"Retain rollback.","reversible":true}},"user_quotes":[{"quote":"Use the example workflow.","source":{"machine":"server","session":"c1x1x1","msg_uuid":"message-example"},"trust":"user_verbatim"}]}`)
	for range 2 {
		if _, err := e.s.toolEntries(ctx, "server", "c1x1x1", "decision_record", input); err != nil {
			t.Fatal(err)
		}
	}
	quotes, err := database.ListAssistantEntries(ctx, proto.AssistantEntriesParams{Trust: "inferred"}, true)
	if err != nil || len(quotes.Entries) != 1 || quotes.Entries[0].By != "user" || quotes.Entries[0].Source.Message != "message-example" {
		t.Fatalf("unverified user quote: %+v %v", quotes, err)
	}
	agent, err := database.ListAssistantEntries(ctx, proto.AssistantEntriesParams{By: "agent", Type: "decision"}, true)
	if err != nil || len(agent.Entries) != 1 {
		t.Fatalf("agent decision replay: %+v %v", agent, err)
	}
}

type entriesAccessHub struct {
	*fakeHub
	allowed, personal bool
}

func (h *entriesAccessHub) EntriesReadAccess(id string) (bool, bool) {
	if id != "example-reader" {
		return false, false
	}
	return h.allowed, h.personal
}

func TestEntriesHTTPPersonalGrantRevocationAndCategories(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	ctx := context.Background()
	h := &entriesAccessHub{fakeHub: e.f, allowed: true, personal: true}
	e.s.hub = h
	e.s.entriesDial = func(context.Context, any) ([]byte, error) {
		return []byte(`{"ok":true,"machine_id":"a-different-computer"}`), nil
	}
	for _, space := range []string{"", "health", "finance", "relationship"} {
		item := proto.AssistantEntry{ID: "entry-space-" + space, Type: "decision", Text: "Keep the example choice.", Quote: "Stage", OriginSpace: space, Category: []string{"health", "finance"}, Source: proto.AssistantEntrySource{Machine: "computer-example", Session: "session-example", Message: "message-example"}, Scope: proto.AssistantEntryScope{Executor: "assistant", Personal: true}, By: "user", Trust: "user_verbatim", Status: "active", Decision: &proto.AssistantEntryDecision{Options: []string{"Stage", "Release"}, Selected: []string{"Stage"}, Rejected: []string{"Release"}, Reason: "Example rationale."}}
		if _, err := database.AppendAssistantEntry(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) (int, proto.AssistantEntriesResult) {
		r := httptest.NewRequest("GET", "/api/v1/assistant/entries?by=user", nil)
		r.Header.Set(RoleHeader, roleExtension)
		r.Header.Set(ExtensionHeader, id)
		w := httptest.NewRecorder()
		e.s.ServeHTTP(w, r)
		var out proto.AssistantEntriesResult
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	// The read below is of the user's own extension. Keep its real trust in the store the production filter reads;
	// the transport grant alone cannot make a third-party extension's categories visible.
	if err := database.SetExtensionTrust(ctx, "example-reader", assistantwire.ExtensionTrustOwner, nil, "user"); err != nil {
		t.Fatal(err)
	}
	code, out := get("example-reader")
	if code != 200 || len(out.Entries) != 4 || !reflect.DeepEqual(out.Entries[0].Category, []string{"health", "finance"}) {
		t.Fatalf("approved cross-space personal read: %d %+v", code, out)
	}
	rpcCtx := assistantwire.WithEntriesReader(ctx, "example-reader")
	value, handled, err := e.s.conversationsRequest(rpcCtx, assistantwire.MethodAssistantEntriesList, json.RawMessage(`{}`))
	if err != nil || !handled || len(value.(proto.AssistantEntriesResult).Entries) != 4 {
		t.Fatalf("authorized RPC: %+v %v", value, err)
	}
	code, _ = get("unapproved-reader")
	if code != 403 {
		t.Fatalf("unapproved read: %d", code)
	}
	// The token/extension ID stays identical; the live scope changes beneath it.
	h.allowed, h.personal = false, false
	code, _ = get("example-reader")
	if code != 403 {
		t.Fatalf("revoked grant still works: %d", code)
	}
	if _, _, err := e.s.conversationsRequest(rpcCtx, assistantwire.MethodAssistantEntriesList, json.RawMessage(`{}`)); err == nil {
		t.Fatal("revoked RPC still works")
	}
	// A read grant without explicit personal consent can use the retained local proof.
	h.allowed = true
	owner := e.s.settings(ctx).MachineID
	e.s.entriesDial = func(context.Context, any) ([]byte, error) {
		return []byte(`{"ok":true,"machine_id":"` + owner + `"}`), nil
	}
	code, out = get("example-reader")
	if code != 200 || len(out.Entries) != 4 {
		t.Fatalf("local alternative: %d %+v", code, out)
	}
	// Revoking the actual trust hides the entries even when the transport grant still answers; no local proof
	// turns a third-party extension back into the user's own one.
	if err := database.ForgetExtensionTrust(ctx, "example-reader"); err != nil {
		t.Fatal(err)
	}
	code, out = get("example-reader")
	if code != 200 || len(out.Entries) != 0 {
		t.Fatalf("revoked owner trust is still visible: %d %+v", code, out)
	}
}

func TestEntriesExtensionCategoryFilteringPaginationAndUnknowns(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	ctx := context.Background()
	h := &entriesAccessHub{fakeHub: e.f, allowed: true, personal: true}
	e.s.hub = h
	e.s.entriesDial = func(context.Context, any) ([]byte, error) {
		return []byte(`{"ok":true,"machine_id":"a-different-computer"}`), nil
	}
	appendRule := func(id, space string, cats []string, personal bool) {
		t.Helper()
		item := proto.AssistantEntry{ID: id, Type: "rule", Text: "Example entry " + id, OriginSpace: space, Category: cats, Scope: proto.AssistantEntryScope{Executor: "assistant", Personal: personal}, By: "agent", Trust: "agent", Status: "active"}
		if _, err := database.AppendAssistantEntry(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	// Interleave hidden sensitive entries before, between, and after readable entries.
	appendRule("e1-hidden-health", "health", nil, true)
	appendRule("e2-hidden-nonpersonal-fin", "", []string{categoryFinance}, false)
	appendRule("e3-gen-1", "", nil, true)
	appendRule("e4-hidden-unknown-cat", "", []string{"future_secret"}, false)
	appendRule("e5-gen-2", "", nil, false)
	appendRule("e6-hidden-fin", "", []string{categoryFinance}, true)
	appendRule("e7-gen-3", "", nil, true)
	appendRule("e8-hidden-tail", "health", []string{categoryHealth}, true)

	rpcCtx := assistantwire.WithEntriesReader(ctx, "example-reader")
	list := func(limit int, after string) proto.AssistantEntriesResult {
		t.Helper()
		raw, _ := json.Marshal(proto.AssistantEntriesParams{Limit: limit, After: after})
		val, handled, err := e.s.conversationsRequest(rpcCtx, assistantwire.MethodAssistantEntriesList, raw)
		if err != nil || !handled {
			t.Fatalf("list(%d, %q): %v", limit, after, err)
		}
		return val.(proto.AssistantEntriesResult)
	}

	// Page 1: skips e1/e2/e4, returns e3 and e5, HasMore=true because e7 exists.
	p1 := list(2, "")
	if !slices.Equal(entryIDList(p1.Entries), []string{"e3-gen-1", "e5-gen-2"}) || !p1.HasMore {
		t.Fatalf("page 1: ids=%v has_more=%v", entryIDList(p1.Entries), p1.HasMore)
	}
	// Page 2: skips e6 and trailing e8, returns e7 with HasMore=false.
	p2 := list(2, p1.After)
	if !slices.Equal(entryIDList(p2.Entries), []string{"e7-gen-3"}) || p2.HasMore {
		t.Fatalf("page 2: ids=%v has_more=%v", entryIDList(p2.Entries), p2.HasMore)
	}
	// Page 3: empty, retains p2.After without leaking hidden e8's id in the cursor.
	p3 := list(2, p2.After)
	if len(p3.Entries) != 0 || p3.HasMore || p3.After != p2.After {
		t.Fatalf("page 3: %+v (want after=%q)", p3, p2.After)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(p3.After)
	if err != nil || !strings.Contains(string(decoded), `"e7-gen-3"`) || strings.Contains(string(decoded), "e8-hidden-tail") {
		t.Fatalf("cursor decode: %q %v", string(decoded), err)
	}

	// Multi-batch scan: >51 hidden entries followed by a visible entry still returns the visible entry in one request.
	for i := range 55 {
		appendRule(fmt.Sprintf("e-hidden-pad-%02d", i), "health", nil, true)
	}
	appendRule("e9-gen-after-pad", "", nil, true)
	p4 := list(2, p2.After)
	if !slices.Equal(entryIDList(p4.Entries), []string{"e9-gen-after-pad"}) || p4.HasMore {
		t.Fatalf("multi-batch scan: ids=%v has_more=%v", entryIDList(p4.Entries), p4.HasMore)
	}

	// Granting all known categories still hides unknown category e4-hidden-unknown-cat, while opening non-personal finance e2.
	if err := database.SetExtensionTrust(ctx, "example-reader", assistantwire.ExtensionTrustThirdParty, infoCategories, "user"); err != nil {
		t.Fatal(err)
	}
	allKnown := list(100, "")
	if slices.Contains(entryIDList(allKnown.Entries), "e4-hidden-unknown-cat") {
		t.Fatalf("unknown category leaked to third party: %v", entryIDList(allKnown.Entries))
	}
	for _, want := range []string{"e1-hidden-health", "e2-hidden-nonpersonal-fin", "e3-gen-1", "e5-gen-2", "e6-hidden-fin", "e7-gen-3", "e8-hidden-tail", "e9-gen-after-pad"} {
		if !slices.Contains(entryIDList(allKnown.Entries), want) {
			t.Fatalf("all known categories missing %s: %v", want, entryIDList(allKnown.Entries))
		}
	}

	// Owner extension sees the unknown-category entry too.
	if err := database.SetExtensionTrust(ctx, "example-reader", assistantwire.ExtensionTrustOwner, nil, "user"); err != nil {
		t.Fatal(err)
	}
	ownerAll := list(100, "")
	if len(ownerAll.Entries) != 8+55+1 || !slices.Contains(entryIDList(ownerAll.Entries), "e4-hidden-unknown-cat") {
		t.Fatalf("owner extension should see all 64 entries, got %d", len(ownerAll.Entries))
	}

	// Unnamed RPC read (no WithEntriesReader) is treated as a third party with no category grants.
	var unnamed proto.AssistantEntriesResult
	if err := askConversations(t, e.s, assistantwire.MethodAssistantEntriesList, `{"limit":100}`, &unnamed); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entryIDList(unnamed.Entries), []string{"e5-gen-2"}) {
		t.Fatalf("unnamed RPC read: got %v", entryIDList(unnamed.Entries))
	}
}

func TestEntriesCollectOwnerCardAnswersAcrossSourceSpaces(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	ctx := context.Background()
	e.f.setSessions("server", proto.Session{SessionID: "health-example", Assistant: true, Space: "health"})
	ix := proto.Interaction{ID: "card-health-example", MachineID: "server", SessionID: "health-example", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "Keep the example reminder?", Options: []proto.QuestionOption{{Label: "Keep"}, {Label: "Skip"}}}}}
	if err := e.s.ledgerCardDecisions(ctx, ix, proto.Resolution{Answers: map[string]string{"Keep the example reminder?": "Keep"}}, "resolved"); err != nil {
		t.Fatal(err)
	}
	out, err := database.ListAssistantEntries(ctx, proto.AssistantEntriesParams{By: "user"}, true)
	if err != nil || len(out.Entries) != 1 || out.Entries[0].OriginSpace != "health" || len(out.Entries[0].Category) != 0 {
		t.Fatalf("source/category: %+v %v", out, err)
	}
}
