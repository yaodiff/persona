package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// What an extension's token is shown of the to-dos (todo_scope.go, #10941): nothing of a to-do whose history may hold
// the health space's words, on any route of the to-dos, a write's answer, an event or the knowledge base it writes to;
// every row it is shown is a device's. The names, titles and words below are made up.

const (
	scopeAsk    = "Did the new dose help you sleep?"
	scopeAnswer = "Yes, much better"
	scopeReport = "Plotted the blood pressure readings against the new dose"
	scopeReply  = "Your resting heart rate dropped to 48 after the new dose"
	scopeTold   = "Resting heart rate was 48 all week"
)

// scopeSession is a session of the owner's, idle.
func scopeSession(id, title, cwd string) proto.Session {
	c := proto.Session{SessionID: id, Title: title, Cwd: cwd, Status: proto.StatusIdle, State: proto.StateIdle}
	if identity.InHealthSpace(cwd) {
		c.Space = SpaceHealth
	}
	return c
}

// scopeListed are the sessions server lists: the main conversation conv1, a health conversation h0, a health run h1
// whose last reply is scopeReply, a health run h3 in the Journal workspace and a run s1 outside the space.
func scopeListed() []proto.Session {
	health := identity.HealthDir("/home/owner")
	main, h0 := scopeSession("conv1", "Persona", ""), scopeSession("h0", "Health", health)
	main.Assistant, h0.Assistant = true, true
	h1, journal := scopeSession("h1", "Sleep log", health), scopeSession("h3", "Journal notes", health)
	h1.LastText, journal.SpaceName = scopeReply, "Journal"
	return []proto.Session{main, h0, h1, journal, scopeSession("s1", "Paperwork", "/home/owner/forms")}
}

// scopeHistory writes what to-do id's run sid was told in the main conversation.
func scopeHistory(t *testing.T, e *todoEnv, id, sid string) {
	t.Helper()
	if _, err := e.s.appendFeedAt(context.Background(), "conv1", feedRouted, id, map[string]string{"text": scopeTold,
		"machine_id": "server", "session_id": sid}, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// noneOf fails when body carries any of words.
func noneOf(t *testing.T, what string, body []byte, words ...string) {
	t.Helper()
	for _, w := range words {
		if bytes.Contains(body, []byte(w)) {
			t.Errorf("%s carries %q: %s", what, w, body)
		}
	}
}

// The probes of #10941 and of its review: an extension's list and items carry nothing of a to-do whose run works in
// the health space (its run's question, title, directory, words and report to its supervisor, the decision line its
// supervisor's review stands on), of a task a health conversation handed out (its title, notes and the owner's
// message in its brief), nor of a step of the space rolled up onto its parent's row: those to-dos are not on its list,
// and their routes answer 404. Every row it does get is a device's, byte for byte, the list's counts too, and so are
// the answers of a to-do outside the space; a device reads every word, and its answers are the same bytes whatever an
// extension read meanwhile.
func TestTodoRowsToAnExtension(t *testing.T) {
	const works, eggs, form, did = "charting the resting heart rate", "How many eggs this week?", "Which form do you need?",
		"Compared the sleep diary with the new dose"
	const clinic, notes, asked = "Book the sleep clinic for the insomnia", "Bring the sleep diary and the new dose",
		"I can't sleep since the new dose, please book the clinic"
	health := identity.HealthDir("/home/owner")
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		ctx := context.Background()
		// Universal-main supervision requires the fixture's supervisor to be the actual main conversation (#5732).
		setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
		at := time.Now().UTC().Add(-time.Minute)
		run := func(id, title, cwd, state, text string) proto.Session {
			c := scopeSession(id, title, cwd)
			c.State, c.StateSince, c.UpdatedAt = state, at, at
			switch state {
			case proto.StateNeedsYou:
				c.Status, c.Asks = proto.StatusBlocked, text
			case proto.StateWorking:
				c.Status, c.Activity = proto.StatusWorking, text
			}
			return c
		}
		e.sessions("server", append(scopeListed()[:2:2],
			run("h1", "Sleep log", health, proto.StateNeedsYou, scopeAsk),
			run("h2", "Heart log", health, proto.StateWorking, works),
			run("h4", "Diet diary", health, proto.StateNeedsYou, eggs),
			run("h5", "Dose chart", health, proto.StateIdle, ""),
			run("h6", "Blood pressure", health, proto.StateIdle, ""),
			run("s1", "Paperwork", "/home/owner/forms", proto.StateNeedsYou, form))...)
		asking, working, plain := e.create("Log the week's sleep").ID, e.create("Chart the heart rate").ID, e.create("Renew the passport").ID
		parent, step := e.create("Plan the meals").ID, e.create("Count the eggs").ID
		e.must("PATCH", "/api/v1/todos/"+step, `{"part_of":"`+parent+`"}`)
		for id, sid := range map[string]string{asking: "h1", working: "h2", step: "h4", plain: "s1"} {
			e.must("PUT", "/api/v1/todos/"+id+"/agents/server/"+sid, "")
		}
		// A supervised task whose health run reports to its supervisor, and one that waits on the owner's decision,
		// whose ask stands on its health run's closeout.
		task := func(title, sid string) string {
			t.Helper()
			task, err := e.s.createTaskTodo(ctx, title, "", "conv1", "", "")
			if err != nil {
				t.Fatal(err)
			}
			e.must("PUT", "/api/v1/todos/"+task.ID+"/agents/server/"+sid, "")
			e.s.todos.mu.Lock()
			e.s.todos.todos[task.ID].Supervision = &Supervision{Supervisor: "conv1"}
			e.s.todos.mu.Unlock()
			return task.ID
		}
		reporting, deciding := task("Chart the readings", "h5"), task("Chart the doses", "h6")
		raw, _ := json.Marshal(map[string]any{"kind": "progress", "text": scopeReport})
		if _, err := e.s.reportToSupervisor(ctx, "server", "h5", raw, time.Now()); err != nil {
			t.Fatal(err)
		}
		e.s.putWork(ctx, db.SessionWork{MachineID: "server", SessionID: "h6", TurnAt: time.Now().UTC(),
			Work: proto.WorkState{Line: did, Closeout: &proto.Closeout{Status: "not_done", Did: []string{did}}}})
		if out, err := reviewErr(e.s, deciding, map[string]any{"verdict": "needs_user", "why_owner": "priority",
			"reason": "Which dose schedule to chart", "recommend": "Morning", "cost_of_waiting": "a day",
			"options": []map[string]string{{"label": "Morning", "leads_to": "a"}, {"label": "Night", "leads_to": "b"}}}); err != nil {
			t.Fatalf("review: %v %s", err, out)
		}
		// A task a health conversation handed out, from the owner's message there.
		handed, err := e.s.createTaskTodo(ctx, clinic, notes, "h0", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.putTaskBrief(ctx, briefRecord{Request: &BriefRequest{Text: asked, At: time.Now().UTC(), Conversation: "h0"},
			StartedBy: briefByOwner, Conversation: "h0", Goal: clinic}, handed.ID); err != nil {
			t.Fatal(err)
		}
		if database != nil { // the owner answers each asking run's card: a decision of its to-do
			for _, c := range []struct{ sid, q, a string }{{"h1", scopeAsk, scopeAnswer}, {"s1", form, "The blue one"}} {
				e.s.cardDecisions(ctx, proto.Interaction{ID: "ix_" + c.sid, MachineID: "server", SessionID: c.sid, Kind: proto.IxQuestion,
					Questions: []proto.Question{{Question: c.q, Options: []proto.QuestionOption{{Label: c.a}, {Label: "Not yet"}}}}},
					proto.Resolution{Answers: map[string]string{c.q: c.a}}, "resolved")
			}
		}
		hidden := map[string]bool{asking: true, working: true, step: true, parent: true, reporting: true, deciding: true, handed.ID: true}
		healthWords := []string{scopeAsk, works, eggs, scopeReport, did, clinic, notes, asked, "Sleep log", "Heart log",
			"Diet diary", "Count the eggs", "Plan the meals", health, `"h0"`}

		dev, before := rowsAs(t, e, "")
		for _, word := range slices.Concat(healthWords, []string{form, "Paperwork"}) {
			if !bytes.Contains(before, []byte(word)) {
				t.Errorf("a device's list has no %q: %s", word, before)
			}
		}
		ext, body := rowsAs(t, e, roleExtension)
		noneOf(t, "an extension's list", body, healthWords...)
		for id := range dev {
			if got, listed := ext[id]; listed == hidden[id] || listed && !reflect.DeepEqual(got, dev[id]) {
				t.Errorf("%s on an extension's list (hidden %v): %v\na device's: %v", dev[id]["title"], hidden[id], got, dev[id])
			}
		}
		var counts [2]json.RawMessage
		for i, b := range [][]byte{before, body} {
			var l struct{ Counts json.RawMessage }
			_ = json.Unmarshal(b, &l)
			counts[i] = l.Counts
		}
		if !bytes.Equal(counts[0], counts[1]) {
			t.Errorf("counts: a device's %s, an extension's %s", counts[0], counts[1])
		}
		if _, again := rowsAs(t, e, roleExtension); !bytes.Equal(again, body) {
			t.Errorf("an extension's list read again:\n%s\n%s", body, again)
		}

		for id := range dev {
			for _, path := range []string{"/api/v1/todos/" + id, "/api/v1/todos/" + id + "/timeline", "/api/v1/todos/" + id + "/decisions",
				"/api/v1/todos/" + id + "/repositories"} {
				if database == nil && !strings.HasSuffix(path, id) && !strings.HasSuffix(path, "/timeline") {
					continue // decisions and repositories need the hub's database
				}
				devBody := e.must("GET", path, "").Body.Bytes()
				w := e.doAs(roleExtension, "GET", path, "")
				switch {
				case hidden[id]:
					noneOf(t, "an extension at "+path, w.Body.Bytes(), slices.Concat(healthWords, []string{scopeAnswer, scopeTold})...)
					if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"todo_not_found"`) {
						t.Errorf("an extension at %s of a hidden to-do: %d %s", path, w.Code, w.Body)
					}
				case w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), devBody):
					t.Errorf("an extension at %s: %d %s\na device's: %s", path, w.Code, w.Body, devBody)
				}
				if again := e.must("GET", path, "").Body.Bytes(); !bytes.Equal(again, devBody) {
					t.Errorf("a device's answer at %s changed with an extension's read:\n%s\n%s", path, devBody, again)
				}
			}
		}
		if database != nil {
			if w := e.doAs(roleExtension, "GET", "/api/v1/todos/"+plain+"/decisions", ""); !strings.Contains(w.Body.String(), "The blue one") {
				t.Errorf("an extension's decisions of a to-do outside the space: %s", w.Body)
			}
		}
		if _, after := rowsAs(t, e, ""); !bytes.Equal(after, before) {
			t.Errorf("a device's list changed with an extension's reads:\n%s\n%s", before, after)
		}
	})
}

// An extension's list reads each computer at most once for the runs it decides by (their links keep no directory and
// no view read them), whatever the number of to-dos, and nothing more than a device's does when every run is read
// already or keeps its directory; the conversations the to-dos came from are told by the spaces the manager noted of
// them (conversation_space.go) without reading the computer, and those it never noted by one read of the assistant's
// computer, not one a conversation.
func TestTodoListToAnExtensionReadsEachComputerOnce(t *testing.T) {
	read := func(t *testing.T, e *todoEnv, role string, want int) (lists, gets int) {
		t.Helper()
		// The hub's background workers can also read the computer. Measure the list request's calls.
		var trace []DeviceCall
		ctx := context.WithValue(t.Context(), deviceCallsForTest{}, &trace)
		w := e.doAsContext(ctx, role, "GET", "/api/v1/todos", "")
		rows, _ := readTodoRows(t, role, w)
		if len(rows) != want {
			t.Fatalf("%q reads %d rows, want %d", role, len(rows), want)
		}
		for _, r := range rows { // every run listed outside the space: shown
			if a := r["agents"].([]any); len(a) != 1 || a[0].(map[string]any)["title"] == "" {
				t.Fatalf("%q reads %v", role, r)
			}
		}
		e.f.mu.Lock()
		defer e.f.mu.Unlock()
		for _, call := range trace {
			switch call.Method {
			case DeviceSessionsList:
				lists++
			case DeviceSessionGet:
				gets++
			}
		}
		return lists, gets
	}
	t.Run("runs", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		var listed []proto.Session
		for i := range 5 {
			listed = append(listed, proto.Session{SessionID: fmt.Sprint("s", i), Title: fmt.Sprint("Letters ", i), Status: proto.StatusIdle, State: proto.StateIdle})
		}
		e.sessions("server", listed...)
		for i := range 5 {
			id := e.create(fmt.Sprint("Answer the letters ", i)).ID
			e.must("PUT", fmt.Sprintf("/api/v1/todos/%s/agents/server/s%d", id, i), "")
			e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`) // its view reads no run
		}
		device, _ := read(t, e, "", 5)
		if ext, _ := read(t, e, roleExtension, 5); ext != device+1 {
			t.Fatalf("sessions.list: a device's list %d calls, an extension's %d, want one more", device, ext)
		}
		e.sessions("server", append(listed, proto.Session{SessionID: "s9", Title: "Filed", Cwd: "/home/owner/letters"})...)
		id := e.create("File the letters").ID
		e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s9", "")
		for i := range 5 { // their runs' links keep a directory now
			e.s.todos.mu.Lock()
			for _, todo := range e.s.todos.todos {
				for k := range todo.Agents {
					if todo.Agents[k].SessionID == fmt.Sprint("s", i) {
						todo.Agents[k].Cwd = "/home/owner/letters"
					}
				}
			}
			e.s.todos.mu.Unlock()
		}
		device, _ = read(t, e, "", 6)
		if ext, _ := read(t, e, roleExtension, 6); ext != device {
			t.Fatalf("sessions.list: a device's list %d calls, an extension's %d, want the same", device, ext)
		}
	})
	t.Run("conversations", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		var listed []proto.Session
		chats := func(from, to int) {
			t.Helper()
			for i := from; i < to; i++ {
				c := scopeSession(fmt.Sprint("chat", i), fmt.Sprint("Chat ", i), "")
				c.Assistant = true
				listed = append(listed, c, scopeSession(fmt.Sprint("s", i), fmt.Sprint("Letters ", i), "/home/owner/letters"))
			}
		}
		tasks := func(from, to int) {
			t.Helper()
			for i := from; i < to; i++ {
				task, err := e.s.createTaskTodo(context.Background(), fmt.Sprint("Answer the letters ", i), "", fmt.Sprint("chat", i), "", "")
				if err != nil {
					t.Fatal(err)
				}
				e.must("PUT", fmt.Sprintf("/api/v1/todos/%s/agents/server/s%d", task.ID, i), "")
				e.must("PATCH", "/api/v1/todos/"+task.ID, `{"done":true}`)
			}
		}
		chats(0, 6)
		e.sessions("server", listed...) // noted from the list
		tasks(0, 6)
		device, _ := read(t, e, "", 6)
		if ext, gets := read(t, e, roleExtension, 6); ext != device || gets != 0 {
			t.Fatalf("an extension's list: %d sessions.list (a device's %d) and %d session.get, want as many lists and no get", ext, device, gets)
		}
		// Six more the computer lists that the manager never noted (no list of them came to it).
		chats(6, 12)
		e.f.setSessions("server", listed...)
		tasks(6, 12)
		device, _ = read(t, e, "", 12)
		if ext, gets := read(t, e, roleExtension, 12); ext != device+1 || gets != 0 {
			t.Fatalf("an extension's list: %d sessions.list (a device's %d) and %d session.get, want one more list and no get", ext, device, gets)
		}
	})
}

// An extension's token (todos.write) changes nothing of a to-do it is not shown and is answered nothing of it: every
// write to it answers 404 as for a to-do that does not exist, its runs stay on it (a run it would take off it, or one
// in the health space, is not taken onto a to-do of the extension's own, nor is the space a health run came through),
// and closing it, which would read its run's last reply as its outcome, is refused. Writes to a to-do outside the
// space answer as a device's.
func TestTodoWritesOfAnExtensionToAHiddenTodo(t *testing.T) {
	health := identity.HealthDir("/home/owner")
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		e.sessions("server", scopeListed()...)
		hidden := e.create("Log the week").ID
		e.must("PUT", "/api/v1/todos/"+hidden+"/agents/server/h1", "")
		scopeHistory(t, e, hidden, "h1")
		spaced := e.create("Write the journal").ID
		e.must("PUT", "/api/v1/todos/"+spaced+"/spaces/server/Journal", "")
		// A to-do hidden for the conversation that handed it out: its run is outside the space.
		handed, err := e.s.createTaskTodo(context.Background(), "Book the clinic", "", "h0", "", "")
		if err != nil {
			t.Fatal(err)
		}
		e.must("PUT", "/api/v1/todos/"+handed.ID+"/agents/server/s1", "")
		before := map[string][]byte{}
		for _, id := range []string{hidden, spaced, handed.ID} {
			raw, _ := json.Marshal(todoNow(e.s, id))
			before[id] = raw
		}
		words := []string{"Sleep log", "Journal notes", health, scopeTold, scopeReply, "Paperwork"}
		writes := []struct{ method, path, body string }{
			{"PATCH", "/api/v1/todos/" + hidden, `{}`},
			{"PATCH", "/api/v1/todos/" + hidden, `{"done":true}`},
			{"PATCH", "/api/v1/todos/" + hidden, `{"title":"Renamed"}`},
			{"DELETE", "/api/v1/todos/" + hidden + "/agents/server/h1", ""},
			{"PUT", "/api/v1/todos/" + hidden + "/agents/server/s1", ""},
			{"PUT", "/api/v1/todos/" + hidden + "/repositories", `{"bindings":[]}`},
			{"POST", "/api/v1/todos/" + hidden + "/decisions", `{"question":"Done?","choice":"Yes","source":"outcome"}`},
			{"DELETE", "/api/v1/todos/" + spaced + "/spaces/server/Journal", ""},
			{"PATCH", "/api/v1/todos/" + handed.ID, `{}`},
			{"DELETE", "/api/v1/todos/" + handed.ID + "/agents/server/s1", ""},
		}
		for _, wr := range writes {
			w := e.doAs(roleExtension, wr.method, wr.path, wr.body)
			noneOf(t, "an extension's "+wr.method+" "+wr.path, w.Body.Bytes(), words...)
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"todo_not_found"`) {
				t.Errorf("an extension's %s %s %s: %d %s", wr.method, wr.path, wr.body, w.Code, w.Body)
			}
		}
		// A to-do of its own: no run of the space, nor one off a to-do it is not shown, nor the space a health run came
		// through.
		var own struct{ Todo Todo }
		if err := json.Unmarshal(e.doAs(roleExtension, "POST", "/api/v1/todos", `{"title":"Sync"}`).Body.Bytes(), &own); err != nil || own.Todo.ID == "" {
			t.Fatalf("an extension's to-do: %v", err)
		}
		for _, path := range []string{"/agents/server/h1", "/agents/server/h3", "/agents/server/s1", "/spaces/server/Journal"} {
			w := e.doAs(roleExtension, "PUT", "/api/v1/todos/"+own.Todo.ID+path, "")
			noneOf(t, "an extension's PUT "+path, w.Body.Bytes(), words...)
			if w.Code != http.StatusNotFound {
				t.Errorf("an extension takes %s onto its own to-do: %d %s", path, w.Code, w.Body)
			}
		}
		// A run its computer does not list (yet: one just started is answered before it is listed): tried again later.
		if w := e.doAs(roleExtension, "PUT", "/api/v1/todos/"+own.Todo.ID+"/agents/server/gone", ""); w.Code != http.StatusConflict ||
			!strings.Contains(w.Body.String(), `"agent_not_listed"`) || w.Header().Get("Retry-After") == "" {
			t.Errorf("an extension takes a run its computer does not list: %d %s %v", w.Code, w.Body, w.Header())
		}
		for _, id := range []string{hidden, spaced, handed.ID} {
			if raw, _ := json.Marshal(todoNow(e.s, id)); !bytes.Equal(raw, before[id]) {
				t.Errorf("a hidden to-do after an extension's refused writes:\n%s\nwas\n%s", raw, before[id])
			}
		}
		if a := todoNow(e.s, own.Todo.ID).Agents; len(a) != 0 {
			t.Errorf("the extension's own to-do took %+v", a)
		}
		// A run outside the space no to-do it is not shown has: taken, and its to-do answers as a device's.
		e.sessions("server", append(scopeListed(), scopeSession("s2", "Letters", "/home/owner/letters"))...)
		if w := e.doAs(roleExtension, "PUT", "/api/v1/todos/"+own.Todo.ID+"/agents/server/s2", ""); w.Code != http.StatusOK {
			t.Errorf("an extension takes a run outside the space: %d %s", w.Code, w.Body)
		}
		if w := e.doAs(roleExtension, "PATCH", "/api/v1/todos/"+own.Todo.ID, `{}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Letters") {
			t.Errorf("an extension's PATCH of its own to-do: %d %s", w.Code, w.Body)
		}
	})
}

// todo.changed and todo.decision (todos.read) tell nothing of a to-do an extension is not shown: not its creation, a
// change, its deletion, a decision on its health run's card or the outcome read off that run's last reply; a to-do a
// health conversation handed out is not told either. A to-do outside the space is told as before, its deletion with
// the title it had.
func TestTodoEventsLeaveOutHiddenTodos(t *testing.T) {
	todoEventStores(t, func(t *testing.T, e *todoEventEnv, database *db.DB) {
		ctx := context.Background()
		e.sessions("server", scopeListed()...)
		hidden := e.create("Log the week").ID
		e.must("PUT", "/api/v1/todos/"+hidden+"/agents/server/h1", "")
		plain := e.create("Renew the passport").ID
		e.must("PUT", "/api/v1/todos/"+plain+"/agents/server/s1", "")
		if _, err := e.s.createTaskTodo(ctx, "Book the sleep clinic", "", "h0", "", ""); err != nil {
			t.Fatal(err)
		}
		words := []string{"Log the week", "Book the sleep clinic", "Renamed in the space", scopeAsk, scopeAnswer, scopeReply, hidden}
		told := func(what string, want int) []recordedKBEvent {
			t.Helper()
			got := e.settle()
			for _, ev := range got {
				noneOf(t, what+": "+ev.name, ev.raw, words...)
			}
			if len(got) != want {
				t.Errorf("%s: %d events, want %d: %+v", what, len(got), want, got)
			}
			return got
		}
		if got := told("created", 1); len(got) == 1 && changedOf(t, got[0]).ID != plain {
			t.Errorf("created: %s", got[0].raw)
		}
		e.must("PATCH", "/api/v1/todos/"+hidden, `{"title":"Renamed in the space"}`)
		e.must("PATCH", "/api/v1/todos/"+plain, `{"title":"Renew it"}`)
		told("renamed", 1)
		if database != nil {
			for _, c := range []struct{ sid, q, a string }{{"h1", scopeAsk, scopeAnswer}, {"s1", "Which form?", "The blue one"}} {
				e.s.cardDecisions(ctx, proto.Interaction{ID: "ix_" + c.sid, MachineID: "server", SessionID: c.sid, Kind: proto.IxQuestion,
					Questions: []proto.Question{{Question: c.q, Options: []proto.QuestionOption{{Label: c.a}, {Label: "Not yet"}}}}},
					proto.Resolution{Answers: map[string]string{c.q: c.a}}, "resolved")
			}
			got := e.told()
			for _, ev := range got {
				noneOf(t, "a card's decision: "+ev.name, ev.raw, words...)
			}
			if len(got) != 1 || !strings.Contains(string(got[0].raw), "The blue one") {
				t.Errorf("the cards' decisions: %+v", got)
			}
			// The owner closes a task of the health run: its outcome, read off the run's last reply, is a decision of
			// the to-do, which is not told.
			task, err := e.s.createTaskTodo(ctx, "Chart the readings", "", "conv1", "", "")
			if err != nil {
				t.Fatal(err)
			}
			e.must("PUT", "/api/v1/todos/"+task.ID+"/agents/server/h1", "")
			e.must("PATCH", "/api/v1/todos/"+task.ID, `{"done":true}`)
			e.s.tasks.outcomeRuns.Wait()
			if w := e.must("GET", "/api/v1/todos/"+task.ID+"/decisions", ""); !strings.Contains(w.Body.String(), "48") {
				t.Fatalf("no outcome written: %s", w.Body)
			}
			words = append(words, "Chart the readings", task.ID)
			for _, ev := range e.settle() {
				noneOf(t, "a hidden task's close: "+ev.name, ev.raw, words...)
			}
		}
		e.must("DELETE", "/api/v1/todos/"+hidden, "")
		e.must("DELETE", "/api/v1/todos/"+plain, "")
		if got := told("deleted", 1); len(got) == 1 {
			if c := changedOf(t, got[0]); c.ID != plain || c.What != "deleted" || c.Title != "Renew it" {
				t.Errorf("deleted: %s", got[0].raw)
			}
		}
	})
}

// A task whose run works in the health space writes nothing of its outcome, read off that run's last reply, into a
// knowledge-base project's timeline, outside the space, where kb.read reads it; one outside the space still does.
func TestHiddenTaskOutcomeStaysOutOfProjects(t *testing.T) {
	e := projectEnv(t, todoTestDB(t))
	e.sessions("server", append(scopeListed(), func() proto.Session {
		c := scopeSession("s2", "Letters", "/home/owner/letters")
		c.LastText = "Sent the three letters to the council"
		return c
	}())...)
	for _, c := range []struct{ title, sid string }{{"Chart the readings", "h1"}, {"Send the letters", "s2"}} {
		task, err := e.s.createTaskTodo(context.Background(), c.title, "", "conv1", "", "")
		if err != nil {
			t.Fatal(err)
		}
		e.must("PUT", "/api/v1/todos/"+task.ID+"/agents/server/"+c.sid, "")
		e.must("PATCH", "/api/v1/todos/"+task.ID, `{"project":"诊断能力"}`)
		e.must("PATCH", "/api/v1/todos/"+task.ID, `{"done":true}`)
	}
	e.s.tasks.outcomeRuns.Wait()
	log := strings.Join(kbEng(t, e.s).Store.Log("诊断能力", 10), "\n")
	noneOf(t, "the project's timeline", []byte(log), scopeReply, "Chart the readings")
	if !strings.Contains(log, "Send the letters") {
		t.Errorf("the project's timeline has no outcome of the task outside the space: %q", log)
	}
}
