package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func outsideBool(v bool) *bool { return &v }

func outsideFixture() assistantwire.OutsideDecisionRequest {
	return assistantwire.OutsideDecisionRequest{
		Ref: "nightly-2026-11-20", Title: "Publish the nightly build?", Background: "Checks passed; the notes are ready.",
		WhyOwner: "priority", Recommend: "Hold", CostOfWaiting: "The next nightly is delayed", Reversible: outsideBool(true),
		Options: []assistantwire.OutsideDecisionOption{{Label: "Publish", LeadsTo: "Users receive the build"},
			{Label: "Hold", LeadsTo: "No build goes out"}},
	}
}

func outsideCall(t *testing.T, e *todoEnv, ext, method, path string, in any) (int, assistantwire.OutsideDecisionReply) {
	t.Helper()
	var raw []byte
	if in != nil {
		raw, _ = json.Marshal(in)
	}
	w := e.doExt(ext, method, path, string(raw))
	var out assistantwire.OutsideDecisionReply
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func raiseOutside(t *testing.T, e *todoEnv, ext string, in assistantwire.OutsideDecisionRequest) assistantwire.OutsideDecision {
	t.Helper()
	code, out := outsideCall(t, e, ext, "POST", "/api/v1/decisions", in)
	if code != http.StatusOK || out.Decision == nil {
		t.Fatalf("raise: %d %+v", code, out)
	}
	return *out.Decision
}

func pollOutside(t *testing.T, e *todoEnv, ext, id string) assistantwire.OutsideDecision {
	t.Helper()
	code, out := outsideCall(t, e, ext, "GET", "/api/v1/decisions/"+id, nil)
	if code != http.StatusOK || out.Decision == nil {
		t.Fatalf("poll: %d %+v", code, out)
	}
	return *out.Decision
}

func TestOutsideDecisionRaiseAnswerAndPush(t *testing.T) {
	e := newTodoEnv(t, nil)
	in := outsideFixture()
	d := raiseOutside(t, e, "nightly", in)
	if d.ID == "" || d.AskTodoID == "" || d.State != assistantwire.OutsideDecisionPending || d.Source.Kind != "extension" ||
		d.Source.Name != "nightly" || d.Default != defaultTaken || d.Deadline == nil || time.Until(*d.Deadline) < 23*time.Hour {
		t.Fatalf("raised decision: %+v", d)
	}
	if d.Recommend != "Hold" || len(d.Options) != 2 || d.Options[0].Label != "Hold" {
		t.Fatalf("the recommendation was not moved first: %+v", d.Options)
	}
	// A replay with the same authenticated caller, ref and payload returns the
	// original durable id and neither duplicates the card nor the push.
	code, replay := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions", in)
	if code != http.StatusOK || replay.Inserted || replay.Decision == nil || replay.Decision.ID != d.ID {
		t.Fatalf("replay: %d %+v", code, replay)
	}
	changed := in
	changed.Title = "A different question"
	if code, _ := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions", changed); code != http.StatusConflict {
		t.Fatalf("same ref, another payload: %d, want 409", code)
	}
	for _, method := range []string{"GET", "POST"} {
		path := "/api/v1/decisions/" + d.ID
		if method == "POST" {
			path += "/withdraw"
		}
		if code, _ := outsideCall(t, e, "other", method, path, nil); code != http.StatusNotFound {
			t.Errorf("another caller %s: %d, want 404", method, code)
		}
	}

	ask := listedAsk(t, e.s, d.AskTodoID)
	if ask == nil || ask["question"] != in.Title || ask["recommend"] != "Hold" || ask["default"] != defaultTaken {
		t.Fatalf("Needs you ask: %v", ask)
	}
	background, _ := ask["background"].(string)
	if !strings.Contains(background, "nightly") || !strings.Contains(background, in.Background) {
		t.Fatalf("source and background: %q", background)
	}
	responses, _ := json.Marshal(ask["responses"])
	if string(responses) != `["decide_yourself","not_what_i_asked","explain_first"]` {
		t.Fatalf("fixed responses: %s", responses)
	}
	pushes := decidePushes(t, e.f)
	if len(pushes) != 1 {
		t.Fatalf("decision pushes: %v", pushes)
	}
	todoPush, _ := pushes[0]["todo"].(map[string]any)
	body, _ := pushes[0]["body"].(string)
	if todoPush["id"] != d.AskTodoID || !strings.Contains(body, "nightly") ||
		!strings.Contains(body, in.Background) || !strings.Contains(body, in.CostOfWaiting) {
		t.Fatalf("push: %v", pushes[0])
	}

	if rec := answerAsk(e.s, d.AskTodoID, map[string]string{}); rec.Code != http.StatusOK {
		t.Fatalf("one-tap recommendation: %d %s", rec.Code, rec.Body)
	}
	answered := pollOutside(t, e, "nightly", d.ID)
	if answered.State != assistantwire.OutsideDecisionAnswered || answered.Answer == nil ||
		answered.Answer.Option != "Hold" || answered.Answer.AnsweredBy != assistantwire.OutsideAnswerOwner || answered.Answer.AnsweredAt.IsZero() {
		t.Fatalf("owner answer: %+v", answered)
	}
	if ask := listedAsk(t, e.s, d.AskTodoID); ask != nil {
		t.Fatalf("answered decision is still in Needs you: %v", ask)
	}
	if backing := todoNow(e.s, d.AskTodoID); backing.DoneAt == nil {
		t.Fatal("the answered backing row was not closed")
	}
	if got := len(e.f.agentSent); got != 0 {
		t.Fatalf("an outside answer went to an Agent: %v", e.f.agentSent)
	}
}

// An outside decision is owner work, not a quiet background round: it counts exactly like a supervisor decision.
func TestOutsideDecisionSharesSupervisorNeedsYouCount(t *testing.T) {
	for _, tc := range []struct {
		name        string
		attached    bool
		quietParent bool
	}{{"standalone", false, false}, {"attached", true, false}, {"attached quiet parent", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, supervised := newSupervisedService(t)
			waitsForReview(t, s, f, supervised.ID, "两种走法：A 先灰度，B 直接全量。")
			row := todoNow(s, supervised.ID)
			row.Quiet = false
			putTodo(s, row)
			if _, err := reviewErr(s, supervised.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			e := &todoEnv{t: t, s: s, f: f}
			in := outsideFixture()
			if tc.attached {
				parent := e.create("Release checklist")
				parent.Quiet = tc.quietParent
				putTodo(s, parent)
				in.TodoID = parent.ID
			}
			outside := raiseOutside(t, e, "nightly", in)

			var page todosList
			if err := json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if got := page.Counts.NeedsYou; got != 2 {
				t.Errorf("projection Needs you count = %d, want one supervisor decision and outside decision %s", got, outside.ID)
			}
			if got := page.Counts.Sections[assistantwire.TodoListSectionNeedsYou]; got != 2 {
				t.Errorf("projection Needs you rows = %d, want one supervisor decision and outside decision %s", got, outside.ID)
			}
			if tc.attached {
				var parentRow, outsideRow *listedTodo
				for i := range page.Todos {
					switch page.Todos[i].ID {
					case in.TodoID:
						parentRow = &page.Todos[i]
					case outside.AskTodoID:
						outsideRow = &page.Todos[i]
					}
				}
				if parentRow == nil || outsideRow == nil || parentRow.View == nil || outsideRow.View == nil {
					t.Fatal("the attached parent and outside decision need their projected rows")
				}
				if len(parentRow.View.Asks) != 0 {
					t.Errorf("the outside decision also moved onto its parent: %+v", parentRow.View.Asks)
				}
				if len(outsideRow.View.Asks) == 0 {
					t.Error("the outside decision has no ask on its own row")
				}
			}
			legacy := triageTodos(page.Todos, lookupOf(nil), time.Now(), time.UTC, false)
			if got := len(legacy.Sections[triageWaiting]); got != 2 {
				t.Errorf("legacy Needs you count = %d, want one supervisor decision and outside decision %s", got, outside.ID)
			}
		})
	}
}

func TestOutsideDecisionReplayAfterParentClosed(t *testing.T) {
	e := newTodoEnv(t, nil)
	parent := e.create("Release checklist")
	in := outsideFixture()
	in.TodoID = parent.ID
	d := raiseOutside(t, e, "release-script", in)
	e.must("PATCH", "/api/v1/todos/"+parent.ID, `{"done":true}`)
	code, replay := outsideCall(t, e, "release-script", "POST", "/api/v1/decisions", in)
	if code != http.StatusOK || replay.Inserted || replay.Decision == nil || replay.Decision.ID != d.ID {
		t.Fatalf("replay after the parent closed: %d %+v", code, replay)
	}
	changed := in
	changed.Title = "A different question"
	if code, _ := outsideCall(t, e, "release-script", "POST", "/api/v1/decisions", changed); code != http.StatusConflict {
		t.Fatalf("changed replay after the parent closed: %d, want 409", code)
	}
}

func TestOutsideDecisionPendingBackingCannotEnd(t *testing.T) {
	e := newTodoEnv(t, nil)
	d := raiseOutside(t, e, "nightly", outsideFixture())
	for _, tc := range []struct{ method, body string }{{"PATCH", `{"done":true}`}, {"DELETE", ""}} {
		w := e.do(tc.method, "/api/v1/todos/"+d.AskTodoID, tc.body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "outside_decision_pending") {
			t.Fatalf("%s pending backing row: %d %s", tc.method, w.Code, w.Body)
		}
	}
	if got := pollOutside(t, e, "nightly", d.ID); got.State != assistantwire.OutsideDecisionPending || listedAsk(t, e.s, d.AskTodoID) == nil {
		t.Fatalf("pending decision after refused close/delete: %+v", got)
	}
	code, withdrawn := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions/"+d.ID+"/withdraw", nil)
	if code != http.StatusOK || withdrawn.Decision == nil || withdrawn.Decision.State != assistantwire.OutsideDecisionWithdrawn {
		t.Fatalf("withdraw: %d %+v", code, withdrawn)
	}
	if w := e.do("DELETE", "/api/v1/todos/"+d.AskTodoID, ""); w.Code != http.StatusOK {
		t.Fatalf("delete a withdrawn backing row: %d %s", w.Code, w.Body)
	}
	if got := pollOutside(t, e, "nightly", d.ID); got.State != assistantwire.OutsideDecisionWithdrawn {
		t.Fatalf("withdrawn history after deleting its row: %+v", got)
	}
}

func TestOutsideDecisionStandaloneHonorsMainMute(t *testing.T) {
	e := newTodoEnv(t, nil)
	setSettings(t, e.s, func(v *Settings) { v.Main = "main" })
	setMutes(e.s, Pins{Sessions: []string{"server/main"}})
	d := raiseOutside(t, e, "nightly", outsideFixture())
	if pushes := decidePushes(t, e.f); len(pushes) != 0 {
		t.Fatalf("decision pushes for a muted main chat: %v", pushes)
	}
	if listedAsk(t, e.s, d.AskTodoID) == nil {
		t.Fatal("a muted decision is still waiting in Needs you")
	}
}

func TestOutsideDecisionAttachedAndAnswerForms(t *testing.T) {
	e := newTodoEnv(t, nil)
	parent := e.create("Release checklist")
	in := outsideFixture()
	in.TodoID = parent.ID
	d := raiseOutside(t, e, "release-script", in)
	if d.TodoID != parent.ID || todoNow(e.s, d.AskTodoID).PartOf != parent.ID {
		t.Fatalf("attachment: %+v, backing %+v", d, todoNow(e.s, d.AskTodoID))
	}
	if rec := answerAsk(e.s, d.AskTodoID, map[string]string{"option": "Publish"}); rec.Code != http.StatusOK {
		t.Fatalf("alternate option: %d %s", rec.Code, rec.Body)
	}
	answered := pollOutside(t, e, "release-script", d.ID)
	if answered.Answer == nil || answered.Answer.Option != "Publish" || answered.Answer.AnsweredBy != "owner" {
		t.Fatalf("alternate answer: %+v", answered)
	}

	for _, response := range []string{respondDecideYourself, respondNotAsked, respondExplainFirst} {
		t.Run(response, func(t *testing.T) {
			modelCalls := len(e.f.modelCalls())
			in.Ref = "fixed-" + response
			in.TodoID = ""
			d := raiseOutside(t, e, "release-script", in)
			if rec := answerAsk(e.s, d.AskTodoID, map[string]string{"response": response}); rec.Code != http.StatusOK {
				t.Fatalf("fixed answer: %d %s", rec.Code, rec.Body)
			}
			got := pollOutside(t, e, "release-script", d.ID)
			if got.Answer == nil || got.Answer.Response != response || got.Answer.AnsweredBy != "owner" {
				t.Fatalf("fixed response: %+v", got)
			}
			if len(e.f.modelCalls()) != modelCalls {
				t.Fatal("the assistant answered an outside decision on the owner's behalf")
			}
			wantState := assistantwire.OutsideDecisionAnswered
			if response == respondExplainFirst {
				wantState = assistantwire.OutsideDecisionPending
				if got.Default != defaultNever || listedAsk(t, e.s, d.AskTodoID) == nil {
					t.Fatalf("explain_first did not stay pending without a default: %+v", got)
				}
				if rec := answerAsk(e.s, d.AskTodoID, map[string]string{"option": "Hold"}); rec.Code != http.StatusOK {
					t.Fatalf("answer after an explanation request: %d %s", rec.Code, rec.Body)
				}
				got = pollOutside(t, e, "release-script", d.ID)
				if got.State != assistantwire.OutsideDecisionAnswered || got.Answer.Option != "Hold" {
					t.Fatalf("final answer after explain_first: %+v", got)
				}
			}
			if got.State != wantState && response != respondExplainFirst {
				t.Fatalf("state %q, want %q", got.State, wantState)
			}
		})
	}

	in.Ref = "words"
	d = raiseOutside(t, e, "release-script", in)
	a := askOf(e.s, d.AskTodoID)
	result, err := e.s.answerDecisionText(context.Background(), d.AskTodoID, a.AskedAt.Format(timeKeyLayout), "Hold until the notes are checked")
	if err != nil || !result.taken {
		t.Fatalf("text answer: %+v, %v", result, err)
	}
	if got := pollOutside(t, e, "release-script", d.ID); got.Answer == nil || got.Answer.Text != "Hold until the notes are checked" || got.Answer.AnsweredBy != "owner" {
		t.Fatalf("text answer: %+v", got)
	}
}

func TestOutsideDecisionFeishuReply(t *testing.T) {
	e := newTodoEnv(t, nil)
	d := raiseOutside(t, e, "release-script", outsideFixture())
	const cardID = "om_outside_decision"
	e.s.accept.mu.Lock()
	a := e.s.accept.asks[d.AskTodoID]
	a.MessageID, a.Messages = cardID, []string{cardID}
	e.s.accept.mu.Unlock()

	const answer = "Hold until the notes are checked"
	if !e.s.acceptReply(t.Context(), "om_outside_reply", answer, false, cardID) {
		t.Fatal("the Feishu reply was not matched to the outside decision")
	}
	got := pollOutside(t, e, "release-script", d.ID)
	if got.State != assistantwire.OutsideDecisionAnswered || got.Answer == nil || got.Answer.Text != answer || got.Answer.AnsweredBy != "owner" {
		t.Fatalf("Feishu answer: %+v", got)
	}
}

func TestOutsideDecisionWithdrawFencesAnswers(t *testing.T) {
	e := newTodoEnv(t, nil)
	d := raiseOutside(t, e, "nightly", outsideFixture())
	code, out := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions/"+d.ID+"/withdraw", nil)
	if code != http.StatusOK || out.Decision == nil || out.Decision.State != assistantwire.OutsideDecisionWithdrawn || out.Decision.WithdrawnAt == nil {
		t.Fatalf("withdraw: %d %+v", code, out)
	}
	if listedAsk(t, e.s, d.AskTodoID) != nil {
		t.Fatal("a withdrawn decision remains in Needs you")
	}
	if !toldResolved(e.f, assistantwire.TodoAskCardID(d.AskTodoID)) {
		t.Fatalf("withdrawing outside decision did not emit EventFleetResolved %s", assistantwire.TodoAskCardID(d.AskTodoID))
	}
	if rec := answerAsk(e.s, d.AskTodoID, map[string]string{"option": "Hold"}); rec.Code != http.StatusConflict {
		t.Fatalf("answer after withdrawal: %d %s", rec.Code, rec.Body)
	}
	if code, again := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions/"+d.ID+"/withdraw", nil); code != http.StatusOK || again.Decision.State != "withdrawn" {
		t.Fatalf("repeat withdrawal: %d %+v", code, again)
	}
	if got := pollOutside(t, e, "nightly", d.ID); got.State != "withdrawn" || got.Answer != nil {
		t.Fatalf("withdrawn polling record: %+v", got)
	}
}

func TestOutsideDecisionDefaultRule(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		in := outsideFixture()
		off := false
		in.AllowDefault = &off
		d := raiseOutside(t, e, "nightly", in)
		if d.Default != defaultNever || d.Deadline != nil || listedAsk(t, e.s, d.AskTodoID)["default"] != defaultNever {
			t.Fatalf("default-off decision: %+v", d)
		}
		e.s.sweepTaskDeadlines(time.Now().Add(72 * time.Hour))
		e.s.sweepDecisionAges(time.Now().Add(72 * time.Hour))
		got := pollOutside(t, e, "nightly", d.ID)
		if got.State != assistantwire.OutsideDecisionPending || got.Answer != nil || listedAsk(t, e.s, d.AskTodoID) == nil {
			t.Fatalf("default-off was taken or returned to a supervisor: %+v", got)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		d := raiseOutside(t, e, "nightly", outsideFixture())
		b := &e.s.accept
		b.mu.Lock()
		b.asks[d.AskTodoID].Deadline = time.Now().Add(-time.Minute).UTC()
		if err := e.s.saveAcceptErrLocked(context.Background()); err != nil {
			t.Fatal(err)
		}
		b.mu.Unlock()
		e.s.sweepTaskDeadlines(time.Now())
		got := pollOutside(t, e, "nightly", d.ID)
		if got.State != assistantwire.OutsideDecisionAnswered || got.Answer == nil || got.Answer.Option != "Hold" ||
			got.Answer.AnsweredBy != assistantwire.OutsideAnswerDefault {
			t.Fatalf("deadline answer: %+v", got)
		}
	})
	t.Run("irreversible", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		in := outsideFixture()
		in.WhyOwner, in.Reversible = whyIrreversible, outsideBool(false)
		d := raiseOutside(t, e, "nightly", in)
		if d.Default != defaultNever || d.Deadline != nil || listedAsk(t, e.s, d.AskTodoID)["risk"] != assistantwire.OwnerAskRiskOpen {
			t.Fatalf("irreversible decision: %+v", d)
		}
		e.s.sweepTaskDeadlines(time.Now().Add(72 * time.Hour))
		if got := pollOutside(t, e, "nightly", d.ID); got.State != assistantwire.OutsideDecisionPending {
			t.Fatalf("irreversible decision defaulted: %+v", got)
		}
	})
}

func TestOutsideDecisionAnswerSurvivesManagerRestart(t *testing.T) {
	st := &memStore{}
	e := newTodoEnv(t, st)
	d := raiseOutside(t, e, "nightly", outsideFixture())
	if rec := answerAsk(e.s, d.AskTodoID, map[string]string{"option": "Hold"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body)
	}
	restarted := newServiceOn(t, e.f, st)
	code, out := outsideCall(t, &todoEnv{t: t, s: restarted, f: e.f}, "nightly", "GET", "/api/v1/decisions/"+d.ID, nil)
	if code != http.StatusOK || out.Decision == nil || out.Decision.Answer == nil || out.Decision.Answer.AnsweredBy != "owner" {
		t.Fatalf("answer after restart: %d %+v", code, out)
	}
}

func TestOutsideDecisionAssistantToolCannotAnswer(t *testing.T) {
	e := newTodoEnv(t, nil)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	d := raiseOutside(t, e, "nightly", outsideFixture())
	for _, toldAgent := range []bool{false, true} {
		got, isErr := tool(t, e.s, "task_decided", map[string]any{
			"todo_id": d.AskTodoID, "option": "Hold", "said": "Hold the build", "told_agent": toldAgent,
		})
		if !isErr || !strings.Contains(got, "outside decision") {
			t.Fatalf("task_decided told_agent=%v: %s (error %v)", toldAgent, got, isErr)
		}
	}
	if got := pollOutside(t, e, "nightly", d.ID); got.State != assistantwire.OutsideDecisionPending || got.Answer != nil || listedAsk(t, e.s, d.AskTodoID) == nil {
		t.Fatalf("the assistant tool answered an outside decision: %+v", got)
	}
}

func TestOutsideDecisionSourceLocalization(t *testing.T) {
	e := newTodoEnv(t, nil)
	d := raiseOutside(t, e, "nightly", outsideFixture())
	a := askOf(e.s, d.AskTodoID)
	for lang, want := range map[i18n.Lang]string{"en": "From extension: nightly", "zh-Hans": "来自扩展：nightly", "ja": "拡張機能から: nightly"} {
		got := backgroundText(lang, e.s.location(context.Background()), a)
		if !strings.Contains(got, want) || !strings.Contains(got, "Checks passed") {
			t.Errorf("%s: %q", lang, got)
		}
	}
}

func TestOutsideDecisionPostgresLifecycle(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	parent := e.create("Release checklist")
	in := outsideFixture()
	in.TodoID = parent.ID
	d := raiseOutside(t, e, "nightly", in)
	if rec := answerAsk(e.s, d.AskTodoID, map[string]string{"option": "Hold"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body)
	}
	rows, err := database.ListTodoDecisions(context.Background(), d.AskTodoID, false)
	if err != nil || len(rows) != 1 || rows[0].Choice != "Hold" {
		t.Fatalf("decision rows: %+v, %v", rows, err)
	}
	restarted := newServiceOn(t, e.f, database)
	got := pollOutside(t, &todoEnv{t: t, s: restarted, f: e.f}, "nightly", d.ID)
	if got.State != assistantwire.OutsideDecisionAnswered || got.Answer == nil || got.Answer.AnsweredBy != "owner" || got.TodoID != parent.ID {
		t.Fatalf("Postgres answer after restart: %+v", got)
	}
}

func TestOutsideDecisionNodeRequestLifecycle(t *testing.T) {
	e := newTodoEnv(t, nil)
	decision, err := json.Marshal(outsideFixture())
	if err != nil {
		t.Fatal(err)
	}
	wire := `{"decision":` + string(decision) + `,"source":"Terminal job","extra":true`
	raw := json.RawMessage(wire + `,"cwd":"/work/nightly"}`)
	out, ok, err := e.s.NodeAnswer(context.Background(), "server", assistantwire.MethodDecisionRaise, raw)
	if !ok || err != nil {
		t.Fatalf("node raise: ok %v, err %v", ok, err)
	}
	raised := out.(assistantwire.OutsideDecisionReply)
	if !raised.Inserted || raised.Decision == nil || raised.Decision.Source.Kind != "script" || raised.Decision.Source.Name != "Terminal job" {
		t.Fatalf("node raise: %+v", raised)
	}
	// Older assistants ignored cwd regardless of its JSON type.
	raw = json.RawMessage(wire + `,"cwd":{"ignored":true}}`)
	out, ok, err = e.s.NodeAnswer(context.Background(), "server", assistantwire.MethodDecisionRaise, raw)
	replay, _ := out.(assistantwire.OutsideDecisionReply)
	if !ok || err != nil || replay.Inserted || replay.Decision == nil || replay.Decision.ID != raised.Decision.ID {
		t.Fatalf("node replay with ignored cwd: %+v, ok %v, err %v", out, ok, err)
	}
	raw = json.RawMessage(`{"extra":true,"id":` + string(mustJSON(raised.Decision.ID)) + `}`)
	out, ok, err = e.s.NodeAnswer(context.Background(), "server", assistantwire.MethodDecisionGet, raw)
	if !ok || err != nil || out.(assistantwire.OutsideDecisionReply).Decision == nil {
		t.Fatalf("node poll: %+v, ok %v, err %v", out, ok, err)
	}
	out, ok, err = e.s.NodeAnswer(context.Background(), "other", assistantwire.MethodDecisionWithdraw, raw)
	if !ok || err != nil || out.(assistantwire.OutsideDecisionReply).Code != "decision_not_found" {
		t.Fatalf("another machine withdraw: %+v, ok %v, err %v", out, ok, err)
	}
	out, ok, err = e.s.NodeAnswer(context.Background(), "server", assistantwire.MethodDecisionWithdraw, raw)
	if !ok || err != nil || out.(assistantwire.OutsideDecisionReply).Decision.State != assistantwire.OutsideDecisionWithdrawn {
		t.Fatalf("node withdraw: %+v, ok %v, err %v", out, ok, err)
	}
}

func TestOutsideDecisionNodeRequestInvalidReplies(t *testing.T) {
	e := newTodoEnv(t, nil)
	for _, tc := range []struct {
		name, method, input, message string
	}{
		{"malformed raise", "decision.raise", `{"decision":`, "unexpected end of JSON input"},
		{"invalid source", "decision.raise", `{"source":42}`, "json: cannot unmarshal number into Go struct field .source of type string"},
		{"invalid source after ignored cwd", "decision.raise", `{"cwd":42,"source":42}`, "json: cannot unmarshal number into Go struct field .source of type string"},
		{"empty id", "decision.get", `{"id":""}`, "a decision id is required"},
		{"invalid id", "decision.withdraw", `{"id":42}`, "a decision id is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := assistantwire.OutsideDecisionReply{Code: "invalid_decision", Error: tc.message}
			out, ok, err := e.s.NodeAnswer(t.Context(), "server", tc.method, json.RawMessage(tc.input))
			if !ok || err != nil || out != want {
				t.Errorf("reply = %+v, handled = %v, err = %v, want %+v, true, nil", out, ok, err, want)
			}
		})
	}
}

func TestOutsideDecisionValidation(t *testing.T) {
	e := newTodoEnv(t, nil)
	for name, edit := range map[string]func(*assistantwire.OutsideDecisionRequest){
		"title":      func(in *assistantwire.OutsideDecisionRequest) { in.Title = "" },
		"background": func(in *assistantwire.OutsideDecisionRequest) { in.Background = "" },
		"why owner":  func(in *assistantwire.OutsideDecisionRequest) { in.WhyOwner = "" },
		"options":    func(in *assistantwire.OutsideDecisionRequest) { in.Options = in.Options[:1] },
		"recommend":  func(in *assistantwire.OutsideDecisionRequest) { in.Recommend = "missing" },
		"cost":       func(in *assistantwire.OutsideDecisionRequest) { in.CostOfWaiting = "" },
		"reversible": func(in *assistantwire.OutsideDecisionRequest) { in.Reversible = nil },
		"duplicate options": func(in *assistantwire.OutsideDecisionRequest) {
			in.Options[0].Label = in.Options[1].Label
		},
		"ambiguous normalized options": func(in *assistantwire.OutsideDecisionRequest) {
			in.Options = append(in.Options, assistantwire.OutsideDecisionOption{Label: "Pub-lish", LeadsTo: "A different outcome"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := outsideFixture()
			edit(&in)
			if code, _ := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions", in); code != http.StatusBadRequest {
				t.Fatalf("invalid decision: %d, want 400", code)
			}
		})
	}
	raw, err := json.Marshal(outsideFixture())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "reversible")
	if code, _ := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions", document); code != http.StatusBadRequest {
		t.Fatalf("omitted reversible: %d, want 400", code)
	}
	if code, _ := outsideCall(t, e, "nightly", "POST", "/api/v1/decisions", func() assistantwire.OutsideDecisionRequest {
		in := outsideFixture()
		in.TodoID = "todo_missing"
		return in
	}()); code != http.StatusNotFound {
		t.Fatalf("unknown to-do: %d, want 404", code)
	}
	if w := e.doAs("answer", "POST", "/api/v1/decisions", `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("read-only role: %d, want 403", w.Code)
	}
}
