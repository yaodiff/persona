package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// What an extension's token (assistant.ask) is shown of the assistant's conversations (feed_scope.go, #10941): none of
// the health space's, listed or read by id, and of a conversation it is shown, nothing of a to-do it is not shown, no
// answered card and no digest. A device reads every word. The names, titles and words below are made up.

// GET /api/v1/assistant lists no health conversation to an extension, nor its title, directory or last reply, in
// either space, archived or not, nor one whose directory is inside the space.
func TestAssistantViewToAnExtension(t *testing.T) {
	const said, title = "My blood pressure was 150 over 95 this morning", "Blood pressure after the new dose"
	health := identity.HealthDir("/home/owner")
	e := newTodoEnv(t, nil)
	main, h0 := scopeSession("conv1", "Persona", ""), scopeSession("h0", title, health)
	h0.LastText = said
	// Of no space by its own Space, but started in a folder of the health space.
	notes := proto.Session{SessionID: "hd", Title: "Sleep notes", Cwd: filepath.Join(health, "notes"), Status: proto.StatusIdle,
		State: proto.StateIdle, LastText: "Slept five hours"}
	archived := scopeSession("ha", "Old dose", health)
	archived.Archived = true
	for _, c := range []*proto.Session{&main, &h0, &notes, &archived} {
		c.Assistant = true
	}
	e.sessions("server", main, h0, notes, archived)
	for _, q := range []string{"", "?archived=1", "?space=health", "?space=health&archived=1"} {
		device := e.must("GET", "/api/v1/assistant"+q, "").Body.Bytes()
		w := e.doAs(roleExtension, "GET", "/api/v1/assistant"+q, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
		noneOf(t, "GET /api/v1/assistant"+q, w.Body.Bytes(), `"h0"`, `"hd"`, `"ha"`, said, title, "Sleep notes", "Slept five hours", "Old dose")
		var out struct{ Conversations []proto.Session }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Conversations == nil {
			t.Fatalf("%s: conversations %s", q, w.Body)
		}
		if q == "?space=health" && (!bytes.Contains(device, []byte(said)) || len(out.Conversations) != 0) {
			t.Fatalf("a device is listed the health conversation, an extension none: %s / %s", device, w.Body)
		}
		if q == "" && (!bytes.Contains(device, []byte(`"hd"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"conv1"`))) {
			t.Fatalf("the assistant's own space: a device %s, an extension %s", device, w.Body)
		}
	}
}

// The re-check of #10941: a conversation's feed is no way around the to-do routes. The main conversation's feed shows
// an extension nothing of a to-do whose run works in the health space (what the owner told the run, its turn's
// summary and report, a message card about it, its task card, a plan with it or with a task in the space), no
// answered card, no routine brief or Today card, on the plain and the paged feed, and the rest as a device reads it.
// A health conversation's feed, and one whose space cannot be told, answer it 404.
func TestConversationFeedToAnExtension(t *testing.T) {
	const told, summary, renew, notice = "Resting heart rate was 48 all week", "Logged the new prescription",
		"Bring the old passport", "The morning run was skipped"
	const providerSection = "Private notes from the briefing provider"
	const formAnswer, clinicDraft, officeDraft = "The renewal form", "About your sleep clinic", "Thanks, see you on Monday"
	health := identity.HealthDir("/home/owner")
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		ctx := context.Background()
		e.sessions("server", scopeListed()...)
		task := func(title, sid string) string {
			t.Helper()
			task, err := e.s.createTaskTodo(ctx, title, "", "conv1", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.appendFeed(ctx, "conv1", feedTask, task.ID, nil); err != nil {
				t.Fatal(err)
			}
			e.must("PUT", "/api/v1/todos/"+task.ID+"/agents/server/"+sid, "")
			return task.ID
		}
		feed := func(conversation, kind, ref string, body any) {
			t.Helper()
			if _, err := e.s.appendFeed(ctx, conversation, kind, ref, body); err != nil {
				t.Fatal(err)
			}
		}
		plan := func(id string, tasks ...PlanTask) {
			t.Helper()
			now := time.Now().UTC()
			p := &TaskPlan{ID: id, Conversation: "conv1", Tasks: tasks, State: planStarted, CreatedAt: now, UpdatedAt: now}
			if err := e.s.savePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			feed("conv1", feedPlan, id, map[string]any{"plan": p})
		}
		hidden, shown := task("Chart the readings", "h1"), task("Renew the passport", "s1")
		feed("conv1", feedRouted, hidden, map[string]string{"text": told, "machine_id": "server", "session_id": "h1"})
		feed("conv1", feedReceipt, hidden, map[string]string{"state": "done", "summary": summary, "machine_id": "server", "session_id": "h1"})
		feed("conv1", feedReport, hidden, map[string]string{"kind": "progress", "text": scopeReport})
		feed("conv1", feedNotice, hidden, noticeBody{Text: "A private task's launcher changed"})
		feed("conv1", feedAnswered, hidden, answeredBody{Question: scopeAsk, Answer: scopeAnswer})
		feed("conv1", feedMessage, "inbox_clinic", MessageCard{TodoID: hidden, Source: "mail", Chat: "Clinic", Text: clinicDraft, Draft: clinicDraft})
		feed("conv1", feedRouted, shown, map[string]string{"text": renew, "machine_id": "server", "session_id": "s1"})
		feed("conv1", feedNotice, shown, noticeBody{Text: "A shared task's launcher changed"})
		feed("conv1", feedAnswered, shown, answeredBody{Question: "Which form do you need?", Answer: formAnswer})
		feed("conv1", feedMessage, "inbox_office", MessageCard{TodoID: shown, Source: "mail", Chat: "Passport office", Text: officeDraft, Draft: officeDraft})
		plan("plan_shown", PlanTask{Title: "Renew the passport", TodoID: shown, Cwd: "/home/owner/forms"})
		plan("plan_hidden", PlanTask{Title: "Renew the passport", TodoID: shown, Cwd: "/home/owner/forms"},
			PlanTask{Title: "Chart the readings", TodoID: hidden, Cwd: health})
		plan("plan_in_space", PlanTask{Title: "Sort the sleep diary", Cwd: filepath.Join(health, "diary")})
		feed("conv1", feedBrief, "routine_morning", map[string]any{"title": "Morning", "waiting": []map[string]string{{"id": "ix_sleep", "title": scopeAsk}}})
		feed("conv1", feedToday, "2026-11-19", map[string]any{"date": "2026-11-19", "done": []map[string]string{{"title": "Chart the readings"}}})
		feed("conv1", feedNotice, "", noticeBody{Text: notice})
		// Even an otherwise visible ref must not make another provider's report readable.
		if _, err := e.s.appendFeedOnce(ctx, routineContributionNoticePrefix+"fixture", "conv1", feedNotice, shown, noticeBody{Text: providerSection}); err != nil {
			t.Fatal(err)
		}
		feed("h0", feedRouted, hidden, map[string]string{"text": told, "machine_id": "server", "session_id": "h1"})
		if w := e.doAs(roleExtension, "GET", "/api/v1/todos/"+hidden, ""); w.Code != http.StatusNotFound {
			t.Fatalf("the to-do is hidden from the to-do routes: %d", w.Code)
		}

		words := []string{providerSection, told, summary, scopeReport, scopeAsk, scopeAnswer, formAnswer, clinicDraft, hidden, "Chart the readings",
			health, "Sort the sleep diary", "plan_hidden", "plan_in_space", "routine_morning", "A private task's launcher changed", `"today"`}
		for _, q := range []string{"", "?paginate=1"} {
			path := "/api/v1/assistant/conversations/conv1/feed" + q
			device := e.must("GET", path, "").Body.Bytes()
			for _, w := range append(slices.Clone(words), shown, renew, notice, "plan_shown") {
				if !bytes.Contains(device, []byte(w)) {
					t.Fatalf("%s: a device does not read %q: %s", path, w, device)
				}
			}
			w := e.doAs(roleExtension, "GET", path, "")
			if w.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body)
			}
			noneOf(t, "an extension's "+path, w.Body.Bytes(), append(slices.Clone(words), "Sleep log")...)
			for _, want := range []string{shown, renew, notice, "plan_shown", "A shared task's launcher changed"} {
				if !bytes.Contains(w.Body.Bytes(), []byte(want)) {
					t.Errorf("%s: an extension does not read %q: %s", path, want, w.Body)
				}
			}
			if bytes.Contains(device, []byte(officeDraft)) && !bytes.Contains(w.Body.Bytes(), []byte(officeDraft)) {
				t.Errorf("%s: the message card about the to-do it is shown is gone: %s", path, w.Body)
			}
			var out struct {
				TaskCards []TaskCard `json:"task_cards"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			cards := map[string]bool{}
			for _, c := range out.TaskCards {
				cards[c.TodoID] = true
			}
			if !cards[shown] || cards[hidden] {
				t.Errorf("%s: an extension's task cards %+v", path, out.TaskCards)
			}
		}

		for _, id := range []string{"h0", "never-listed"} {
			w := e.doAs(roleExtension, "GET", "/api/v1/assistant/conversations/"+id+"/feed", "")
			if w.Code != http.StatusNotFound {
				t.Errorf("an extension reads %s's feed: %d %s", id, w.Code, w.Body)
			}
			noneOf(t, "an extension's feed of "+id, w.Body.Bytes(), words...)
		}
		if device := e.must("GET", "/api/v1/assistant/conversations/h0/feed", "").Body.Bytes(); !bytes.Contains(device, []byte(told)) {
			t.Fatalf("a device reads the health conversation's feed: %s", device)
		}
	})
}

// The regression check of #10941, a forged session: an extension read a health chat's feed (its notices, views and
// view actions) once a session started under the chat's id was listed outside the space, though the chat was noted in
// the space, and a never-noted one's likewise. The note is read first now, and a listed row counts only as the
// assistant's conversation or as one in the space; the main conversation's feed is still shown.
func TestFeedOfAHealthChatUnderASessionStartedUnderItsID(t *testing.T) {
	const notice = "Switched to the sleep-clinic model for the follow-up"
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		ctx := context.Background()
		setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
		main, h0 := scopeSession("conv1", "Persona", ""), scopeSession("h0", "Sleep", identity.HealthDir("/home/owner"))
		main.Assistant, h0.Assistant = true, true
		e.sessions("server", main, h0) // h0 noted in the space; h1 never noted
		waitFor(t, "the notes written", e.s.listedSpacesWritten)
		for _, id := range []string{"h0", "h1", "conv1"} {
			if _, err := e.s.appendFeedAt(ctx, id, feedNotice, "", map[string]string{"text": notice}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		if database != nil { // the manager starts again: what it noted is read from the store
			e = newTodoEnv(t, database)
			setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
		}
		forged := func(id string) proto.Session {
			c := scopeSession(id, "hi", "/home/owner/errands")
			c.PaneID, c.Launcher = "w7:p1", "claude"
			return c
		}
		e.sessions("server", main, forged("h0"), forged("h1")) // h0 quiet, its transcript gone
		for _, id := range []string{"h0", "h1"} {
			got := extensionDo(e, "GET", "/api/v1/assistant/conversations/"+id+"/feed", "")
			if got.Code != http.StatusNotFound || bytes.Contains(got.Body.Bytes(), []byte(notice)) {
				t.Errorf("an extension's feed of %s under a forged session: %d %s", id, got.Code, got.Body)
			}
		}
		if got := extensionDo(e, "GET", "/api/v1/assistant/conversations/conv1/feed", ""); got.Code != http.StatusOK ||
			!bytes.Contains(got.Body.Bytes(), []byte(notice)) {
			t.Fatalf("an extension's feed of the main conversation: %d %s", got.Code, got.Body)
		}
	})
}
