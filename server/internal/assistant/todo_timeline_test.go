package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/timeline"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// A to-do's timeline on the wire (todo_timeline.go, #10465): GET /api/v1/todos/{id}/timeline pages through its rounds,
// and GET /api/v1/todos/{id} carries the ones the paper shows, how many there are, its results and its recap, all
// read from the stores the manager keeps (the feed's items about it, its decisions, its runs and their sessions).

// todoHistory is a to-do a run works on (server/s1, working) with a history: three instructions of the owner's, a turn
// of the run after the first (a receipt from before #10465, naming its computer only, with a PR), a rework long ago
// (a Background round, on its day in the owner's zone, Asia/Tokyo) and, with a database, a decision of the owner's.
// It answers the to-do's id and its rounds' ids, oldest first.
func todoHistory(t *testing.T, e *todoEnv, database *db.DB) (string, []string) {
	t.Helper()
	ctx := context.Background()
	setSettings(t, e.s, func(v *Settings) { v.Timezone = "Asia/Tokyo" })
	began := time.Now().UTC().Add(-time.Minute)
	e.sessions("server", proto.Session{SessionID: "s1", Title: "Hedge trimming", Status: proto.StatusWorking,
		State: proto.StateWorking, StateSince: began, UpdatedAt: began, Activity: "measuring the hedge"})
	id := e.create("Prune the hedge").ID
	e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s1", "")
	rounds := []string{"start"}
	if database != nil {
		var d struct{ Decision db.TodoDecisionRow }
		_ = json.Unmarshal(e.must("POST", "/api/v1/todos/"+id+"/decisions", `{"question":"Which side first?","choice":"The street side"}`).Body.Bytes(), &d)
		rounds = append(rounds, "r:dec:"+d.Decision.ID)
	}
	t0 := time.Now().UTC().Add(time.Minute)
	add := func(kind string, body any, at time.Time) string {
		t.Helper()
		it, err := e.s.appendFeedAt(ctx, "conv1", kind, id, body, at)
		if err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	add(feedReview, reviewFeedBody{Title: "Prune the hedge", Verdict: reviewRework, Round: 1, Note: "the corner is uneven"},
		time.Date(2026, 1, 15, 20, 0, 0, 0, time.UTC)) // 2026-01-16 in Tokyo
	ask := map[string]string{"machine_id": "server", "session_id": "s1"}
	for i, text := range []string{"trim the street side first", "then the corner", "and sweep up after"} {
		body := map[string]string{"text": text}
		for k, v := range ask {
			body[k] = v
		}
		rounds = append(rounds, "r:feed:"+add(feedRouted, body, t0.Add(time.Duration(2*i)*time.Minute)))
		if i == 0 {
			add(feedReceipt, receiptBody{Title: "Prune the hedge", State: "done", Machine: "server", Summary: "Trimmed the street side.",
				PR: "https://github.com/o/garden/pull/7"}, t0.Add(time.Minute))
		}
	}
	rounds = slices.Insert(rounds, 1, "bg:2026-01-16") // after start, before every round that began later
	return id, rounds
}

func roundIDs(rounds []assistantwire.TodoRound) []string {
	out := make([]string, len(rounds))
	for i, r := range rounds {
		out[i] = r.ID
	}
	return out
}

// The timeline route: every round oldest first, in pages back from the latest; the turn of an older receipt given to
// the run on its computer (the computers' names read for it), the working run live in the latest round, the rework on
// its day in the owner's zone. A before that names no round 409, an unknown to-do 404; at most 100 rounds a page.
func TestTodoTimelineRoute(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		id, want := todoHistory(t, e, database)
		read := func(query string) assistantwire.TodoTimeline {
			t.Helper()
			var tl assistantwire.TodoTimeline
			body := e.must("GET", "/api/v1/todos/"+id+"/timeline"+query, "").Body.Bytes()
			if err := json.Unmarshal(body, &tl); err != nil || tl.Rounds == nil {
				t.Fatalf("%s: %v %s", query, err, body)
			}
			return tl
		}
		all := read("")
		if got := roundIDs(all.Rounds); !slices.Equal(got, want) || all.More {
			t.Fatalf("rounds %v (more %v), want %v", got, all.More, want)
		}
		first := all.Rounds[len(want)-3] // the first instruction's
		if i := slices.IndexFunc(first.Entries, func(e assistantwire.TodoTimelineEntry) bool {
			return e.ID == "run:server/s1@"+first.ID && e.Turns == 1 && e.Text == "Trimmed the street side."
		}); i < 0 {
			t.Errorf("the older receipt is not the run's turn: %+v", first.Entries)
		}
		last := all.Rounds[len(want)-1]
		if i := slices.IndexFunc(last.Entries, func(e assistantwire.TodoTimelineEntry) bool {
			return e.ID == "run:server/s1@"+last.ID && e.Live && e.Text == "measuring the hedge"
		}); i < 0 {
			t.Errorf("no live line of the working run in the latest round: %+v", last.Entries)
		}

		if p := read("?limit=2"); !slices.Equal(roundIDs(p.Rounds), want[len(want)-2:]) || !p.More {
			t.Errorf("the latest two: %v (more %v)", roundIDs(p.Rounds), p.More)
		}
		before := "?before=" + strings.ReplaceAll(want[len(want)-2], ":", "%3A") + "&limit=1"
		if p := read(before); !slices.Equal(roundIDs(p.Rounds), want[len(want)-3:len(want)-2]) || !p.More {
			t.Errorf("one before %s: %v (more %v)", want[len(want)-2], roundIDs(p.Rounds), p.More)
		}
		if p := read("?before=start"); len(p.Rounds) != 0 || p.More {
			t.Errorf("before the first: %v (more %v)", roundIDs(p.Rounds), p.More)
		}
		for _, q := range []string{"?limit=0", "?limit=x", "?limit=-3"} {
			if p := read(q); len(p.Rounds) != len(want) {
				t.Errorf("%s: %d rounds, want every one", q, len(p.Rounds))
			}
		}
		w := e.do("GET", "/api/v1/todos/"+id+"/timeline?before=r%3Afeed%3Agone", "")
		var refused struct{ Code string }
		if _ = json.Unmarshal(w.Body.Bytes(), &refused); w.Code != http.StatusConflict || refused.Code != "round_not_found" {
			t.Errorf("a round that is not there: %d %s", w.Code, w.Body)
		}
		w = e.do("GET", "/api/v1/todos/todo_0000000000000000/timeline", "")
		if _ = json.Unmarshal(w.Body.Bytes(), &refused); w.Code != http.StatusNotFound || refused.Code != "todo_not_found" {
			t.Errorf("an unknown to-do: %d %s", w.Code, w.Body)
		}
	})
}

// A page has the 20 latest rounds when it is not told how many, and 100 at most.
func TestTodoTimelinePageLimits(t *testing.T) {
	e := newTodoEnv(t, nil)
	id := e.create("Answer the letters").ID
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for i := range 130 {
		if _, err := e.s.appendFeedAt(context.Background(), "conv1", feedRouted, id, map[string]string{"text": fmt.Sprint("letter ", i)},
			t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for query, n := range map[string]int{"": todoTimelinePage, "?limit=1000": todoTimelineMaxPage, "?limit=130": todoTimelineMaxPage, "?limit=7": 7} {
		var tl assistantwire.TodoTimeline
		_ = json.Unmarshal(e.must("GET", "/api/v1/todos/"+id+"/timeline"+query, "").Body.Bytes(), &tl)
		if len(tl.Rounds) != n || !tl.More || tl.Rounds[n-1].Header == nil || tl.Rounds[n-1].Header.Text != "letter 129" {
			t.Errorf("%q: %d rounds (more %v), want the latest %d", query, len(tl.Rounds), tl.More, n)
		}
	}
}

// The item route: the row as the list has it, with the rounds the paper shows (the latest two that are not Background
// ones), how many there are, the results and what changed since the owner looked, the same as the timeline route has
// them; the same state read twice is the same bytes.
func TestTodoTimelineOnTheItem(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		id, want := todoHistory(t, e, database)
		e.must("PUT", "/api/v1/todos/"+id+"/seen", "") // before the instructions: the run's turn is news
		body := e.must("GET", "/api/v1/todos/"+id, "").Body.Bytes()
		var item struct {
			Todo json.RawMessage `json:"todo"`
			assistantwire.TodoItemDetail
		}
		if err := json.Unmarshal(body, &item); err != nil || item.RoundsTotal == nil {
			t.Fatalf("%v %s", err, body)
		}
		var row listedTodo
		if err := json.Unmarshal(item.Todo, &row); err != nil || row.ID != id || row.View == nil {
			t.Fatalf("the row: %v %s", err, item.Todo)
		}
		if got := roundIDs(item.Rounds); !slices.Equal(got, want[len(want)-2:]) || *item.RoundsTotal != len(want) {
			t.Fatalf("rounds %v of %d, want %v of %d", got, *item.RoundsTotal, want[len(want)-2:], len(want))
		}
		var tl assistantwire.TodoTimeline
		_ = json.Unmarshal(e.must("GET", "/api/v1/todos/"+id+"/timeline", "").Body.Bytes(), &tl)
		paper, _ := json.Marshal(timeline.Latest(tl.Rounds, timeline.PaperRounds))
		if got, _ := json.Marshal(item.Rounds); !bytes.Equal(got, paper) {
			t.Fatalf("the item's rounds:\n%s\nthe timeline's latest:\n%s", got, paper)
		}
		if len(item.Results) != 1 || item.Results[0].Kind != assistantwire.TodoResultKindChange ||
			item.Results[0].URL != "https://github.com/o/garden/pull/7" {
			t.Errorf("results: %+v", item.Results)
		}
		if r := item.Recap; r == nil || r.Round != want[len(want)-3] || r.Counts[assistantwire.TodoEntryTypeRun] != 1 {
			t.Errorf("recap: %+v", item.Recap)
		}
		if again := e.must("GET", "/api/v1/todos/"+id, "").Body.Bytes(); !bytes.Equal(again, body) {
			t.Fatalf("unchanged state read twice:\n%s\n%s", body, again)
		}
	})
}

// A to-do with no history yet answers rounds [] and rounds_total 0, which a client tells apart from an older manager
// that sends neither; no recap and no results.
func TestTodoTimelineOnAnItemWithNoHistory(t *testing.T) {
	e := newTodoEnv(t, nil)
	id := e.create("Water the roses").ID
	var item map[string]json.RawMessage
	body := e.must("GET", "/api/v1/todos/"+id, "").Body.Bytes()
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatal(err)
	}
	if string(item["rounds"]) != "[]" || string(item["rounds_total"]) != "0" || item["recap"] != nil || item["results"] != nil {
		t.Fatalf("%s", body)
	}
	var tl map[string]json.RawMessage
	_ = json.Unmarshal(e.must("GET", "/api/v1/todos/"+id+"/timeline", "").Body.Bytes(), &tl)
	if string(tl["rounds"]) != "[]" || tl["more"] != nil {
		t.Fatalf("its timeline: %v", tl)
	}
}

// When the to-do's history cannot be read (here a store without the feed), the item route still answers the row,
// without the history's members, as an older manager does; the timeline route answers 503.
func TestTodoTimelineWithoutItsStores(t *testing.T) {
	e := &todoEnv{t: t, f: newFakeHub().withMachine()}
	e.s = newTestService(t, e.f) // memStore: no assistant_feed
	id := e.create("Order compost").ID
	var item map[string]json.RawMessage
	body := e.must("GET", "/api/v1/todos/"+id, "").Body.Bytes()
	if err := json.Unmarshal(body, &item); err != nil || item["todo"] == nil {
		t.Fatalf("%v %s", err, body)
	}
	for _, k := range []string{"rounds", "rounds_total", "recap", "results"} {
		if item[k] != nil {
			t.Errorf("%s with no readable history: %s", k, body)
		}
	}
	if w := e.do("GET", "/api/v1/todos/"+id+"/timeline", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("the timeline: %d %s", w.Code, w.Body)
	}
}

// An extension's token (todos.read) reads a to-do's history without the answered cards, whose records hold what the
// owner typed into a card's fields, and nothing of a to-do one of whose runs works in the health space: its timeline
// and its item answer 404 as for a to-do that is not there (docs/extensions/extensions.md "Never shown to
// extensions"). A device reads both whole.
func TestTodoTimelineToAnExtension(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		ctx := context.Background()
		e.sessions("server", proto.Session{SessionID: "s1", Title: "Passport renewal", Status: proto.StatusIdle, State: proto.StateIdle},
			proto.Session{SessionID: "h1", Title: "Sleep log", Status: proto.StatusIdle, State: proto.StateIdle, Space: SpaceHealth},
			proto.Session{SessionID: "conv1", Title: "Persona", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle})
		id, health := e.create("Renew the passport").ID, e.create("Log the week's sleep").ID
		e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s1", "")
		e.must("PUT", "/api/v1/todos/"+health+"/agents/server/h1", "")
		const typed = "1980-01-01"
		for _, ref := range []string{id, health} {
			at := time.Now().UTC().Add(time.Minute)
			if _, err := e.s.appendFeedAt(ctx, "conv1", feedRouted, ref, map[string]string{"text": "fill in the form"}, at); err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.appendFeedAt(ctx, "conv1", feedAnswered, ref, answeredBody{Question: "Your details", Answer: "Accepted",
				Items: []answeredItem{{Question: "Your details", Answer: "Accepted\nDate of birth: " + typed}}}, at.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range []string{"/api/v1/todos/" + id + "/timeline", "/api/v1/todos/" + id, "/api/v1/todos/" + health + "/timeline"} {
			if w := e.must("GET", path, ""); !strings.Contains(w.Body.String(), typed) {
				t.Errorf("a device does not read the answer at %s: %s", path, w.Body)
			}
		}
		for _, path := range []string{"/api/v1/todos/" + id + "/timeline", "/api/v1/todos/" + id} {
			w := e.doAs(roleExtension, "GET", path, "")
			if body := w.Body.String(); w.Code != http.StatusOK || strings.Contains(body, typed) || !strings.Contains(body, "fill in the form") {
				t.Errorf("an extension at %s: %d %s", path, w.Code, body)
			}
		}
		w := e.doAs(roleExtension, "GET", "/api/v1/todos/"+health+"/timeline", "")
		var refused struct{ Code string }
		if _ = json.Unmarshal(w.Body.Bytes(), &refused); w.Code != http.StatusNotFound || refused.Code != "todo_not_found" {
			t.Errorf("a health to-do's timeline to an extension: %d %s", w.Code, w.Body)
		}
		w = e.doAs(roleExtension, "GET", "/api/v1/todos/"+health, "")
		if _ = json.Unmarshal(w.Body.Bytes(), &refused); w.Code != http.StatusNotFound || refused.Code != "todo_not_found" {
			t.Errorf("a health to-do's item to an extension: %d %s", w.Code, w.Body)
		}
	})
}

// Whether an extension is shown a to-do is decided from what the to-do keeps, not only from what is live (todosHidden):
// a health run hides it once the to-do is done, while the extension tries to take the run off it, once the owner
// takes it off by hand or with the space it came through, once its shell left the space, and while its computer does
// not answer; so do a health conversation or a task leader in the space it was opened from (its history written
// elsewhere), a health conversation that wrote to it, a conversation or run whose space cannot be told, a step of it
// and a to-do on its brief that are hidden. A to-do whose conversations and runs are all known to be outside the space
// still reads, from its links' directories, its conversations' deletion records and the computer its main
// conversation is kept on too. Hidden, it is on neither the list nor any route of its own (its item, timeline and
// decisions answer 404, #10941); one that cannot be told yet is not on the list either, but its routes answer 503 to
// be tried again (#12216); shown, an extension reads its row, history and decisions as a device does, which reads
// every one.
func TestTodoTimelineHiddenFromAnExtension(t *testing.T) {
	const told, summary = "Resting heart rate was 48 all week", "Logged the new prescription"
	health := identity.HealthDir("/home/owner")
	idle := proto.Session{Status: proto.StatusIdle, State: proto.StateIdle}
	session := func(id, title, space, cwd string, assistant bool) proto.Session {
		c := idle
		c.SessionID, c.Title, c.Space, c.Cwd, c.Assistant = id, title, space, cwd, assistant
		return c
	}
	journal := session("h3", "Journal notes", SpaceHealth, health, false)
	journal.SpaceName = "Journal" // a workspace a space link brings in
	listed := []proto.Session{
		session("conv1", "Persona", "", "", true),
		session("c0", "Garden plans", "", "", true),
		session("h0", "Health", SpaceHealth, health, true),
		session("h1", "Sleep log", SpaceHealth, health, false),
		session("h2", "Sleep diary", SpaceHealth, "", false), // its link keeps no directory
		journal,
		session("s1", "Paperwork", "", "", false),
		session("s2", "Letters", "", "/home/owner/letters", false),
	}
	without := func(id string) []proto.Session {
		return slices.DeleteFunc(slices.Clone(listed), func(c proto.Session) bool { return c.SessionID == id })
	}
	// history writes what a to-do's run sid was told in conversation, and its turn's summary.
	history := func(t *testing.T, e *todoEnv, conversation, id, sid string) {
		t.Helper()
		at := time.Now().UTC().Add(time.Minute)
		if _, err := e.s.appendFeedAt(context.Background(), conversation, feedRouted, id, map[string]string{"text": told,
			"machine_id": "server", "session_id": sid}, at); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.appendFeedAt(context.Background(), conversation, feedReceipt, id, map[string]string{"state": "done",
			"summary": summary, "machine_id": "server", "session_id": sid}, at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// linked is a hand-written to-do with runs sids, its history written in conversation about the first.
	linked := func(t *testing.T, e *todoEnv, conversation string, sids ...string) string {
		id := e.create("Log the week").ID
		for _, sid := range sids {
			e.must("PUT", "/api/v1/todos/"+id+"/agents/server/"+sid, "")
		}
		history(t, e, conversation, id, sids[0])
		return id
	}
	// spaced is a hand-written to-do the Journal workspace is linked to, which brings in its health run, its history
	// written about that run.
	spaced := func(t *testing.T, e *todoEnv) string {
		id := e.create("Log the week").ID
		e.must("PUT", "/api/v1/todos/"+id+"/spaces/server/Journal", "")
		if a := todoNow(e.s, id).Agents; len(a) != 1 || a[0].SessionID != "h3" {
			t.Fatalf("the space brings in %+v", a)
		}
		history(t, e, "conv1", id, "h3")
		return id
	}
	unreached := func(e *todoEnv) {
		e.f.mu.Lock()
		e.f.sessionsFail["server"] = 1000
		e.f.mu.Unlock()
	}
	opened := func(t *testing.T, e *todoEnv, origin string) string {
		task, err := e.s.createTaskTodo(context.Background(), "Book the clinic", "", origin, "", "")
		if err != nil {
			t.Fatal(err)
		}
		history(t, e, origin, task.ID, "s1")
		return task.ID
	}
	// worker is a to-do a task leader's agent (lead1, its link keeping directory cwd) opened for a worker.
	worker := func(t *testing.T, e *todoEnv, cwd string) string {
		ctx := context.Background()
		lead := e.create("Ship the release").ID
		if err := e.s.linkTodoAgentWithSource(ctx, lead, "server", "lead1", "Release lead", cwd, "", "", sourceLeader, ""); err != nil {
			t.Fatal(err)
		}
		task, err := e.s.createTaskTodo(ctx, "Write the notes", "", leaderRef("server", "lead1"), "", "")
		if err != nil {
			t.Fatal(err)
		}
		history(t, e, "conv1", task.ID, "s1")
		return task.ID
	}
	cases := []struct {
		name    string
		verdict todoVerdict // what an extension is told of it: shown, hidden, or not yet (#12216)
		setup   func(t *testing.T, e *todoEnv, database *db.DB) string
	}{
		{"a health run, the to-do done", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "h1")
			e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
			return id
		}},
		{"a health run whose link keeps no directory, the to-do done", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "h2")
			e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
			return id
		}},
		{"a health run taken off", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "h1", "s1")
			e.must("DELETE", "/api/v1/todos/"+id+"/agents/server/h1", "")
			return id
		}},
		{"a health run the extension tries to take off", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "h1")
			if w := e.doAs(roleExtension, "DELETE", "/api/v1/todos/"+id+"/agents/server/h1", ""); w.Code != http.StatusNotFound {
				t.Fatalf("an extension takes the run off: %d %s", w.Code, w.Body)
			}
			if a := todoNow(e.s, id).Agents; len(a) != 1 || a[0].ReleasedAt != nil {
				t.Fatalf("the run after an extension's refused take-off: %+v", a)
			}
			return id
		}},
		{"a health run whose computer does not answer", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "h1")
			unreached(e)
			return id
		}},
		{"a run that keeps no directory, its computer not answering", todoUntold, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "s1")
			unreached(e)
			return id
		}},
		{"a health run relinked once its shell left the space, the to-do done", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "h1")
			e.sessions("server", append(without("h1"), session("h1", "Sleep log", "", "/tmp", false))...)
			e.must("PUT", "/api/v1/todos/"+id+"/agents/server/h1", "")
			e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
			return id
		}},
		{"a health run that came through a space the owner unlinked", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := spaced(t, e)
			e.must("DELETE", "/api/v1/todos/"+id+"/spaces/server/Journal", "")
			return id
		}},
		{"a health run that came through a space the owner moved to another to-do", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := spaced(t, e)
			e.must("PUT", "/api/v1/todos/"+e.create("Plan the trip").ID+"/spaces/server/Journal", "")
			return id
		}},
		{"a health run that came through a space voided by a rename", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := spaced(t, e)
			other := e.create("Plan the trip").ID
			e.sessions("server", append(listed, session("s3", "Diary", "", "/home/owner/diary", false))...)
			e.must("PUT", "/api/v1/todos/"+other+"/spaces/server/Diary", "")
			e.s.TodoSpaceRenamed(context.Background(), TodoSpaceRenamed{MachineID: "server", Old: "Journal", New: "Diary"})
			if sp := todoNow(e.s, id).Spaces; len(sp) != 0 {
				t.Fatalf("the space was not voided: %+v", sp)
			}
			return id
		}},
		{"opened from a health conversation no longer listed", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := opened(t, e, "h0")
			e.sessions("server", without("h0")...)
			return id
		}},
		{"opened from a health conversation, its history written elsewhere", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			task, err := e.s.createTaskTodo(context.Background(), "Book the clinic", "", "h0", "", "")
			if err != nil {
				t.Fatal(err)
			}
			history(t, e, "conv1", task.ID, "s1")
			return task.ID
		}},
		{"written to by a health conversation", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			return linked(t, e, "h0", "s1")
		}},
		{"written to by a conversation not listed", todoUntold, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			return linked(t, e, "gone", "s1")
		}},
		{"opened by a task leader in the space", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			return worker(t, e, health)
		}},
		{"opened by a task leader linked twice, once in the space", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := worker(t, e, "/home/owner/code/app")
			// The leader's agent led another task before, working in the space: whichever link a read meets first, the
			// leader is in it.
			at := time.Now().UTC()
			e.s.todos.mu.Lock()
			e.s.todos.todos["todo_hotfix"] = &Todo{ID: "todo_hotfix", Title: "Ship the hotfix", CreatedAt: at, UpdatedAt: at, DoneAt: &at,
				Spaces: []TodoSpace{}, Agents: []TodoAgent{{MachineID: "server", SessionID: "lead1", Cwd: health, Source: sourceLeader,
					LinkedAt: at, ReleasedAt: &at}}}
			e.s.todos.mu.Unlock()
			for range 20 {
				if space, _ := e.s.leaderSpace(context.Background(), "server", "lead1"); space != SpaceHealth {
					t.Fatalf("the leader's space: %q", space)
				}
			}
			return id
		}},
		{"a step of it whose run works in the space", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			parent, step := e.create("Plan the meals").ID, e.create("Count the eggs").ID
			e.must("PATCH", "/api/v1/todos/"+step, `{"part_of":"`+parent+`"}`)
			e.must("PUT", "/api/v1/todos/"+step+"/agents/server/h1", "")
			history(t, e, "conv1", parent, "s1")
			return parent
		}},
		{"on a brief with a to-do whose run works in the space", todoHidden, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			ctx := context.Background()
			first := e.create("Log the week").ID
			e.must("PUT", "/api/v1/todos/"+first+"/agents/server/h1", "")
			brief, err := e.s.putTaskBrief(ctx, briefRecord{Goal: "the week logged", StartedBy: briefByOwner}, first)
			if err != nil {
				t.Fatal(err)
			}
			id := linked(t, e, "conv1", "s1")
			if err := e.s.linkTaskBrief(ctx, brief.ID, id, first, "the first run stopped"); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		{"opened by a task leader outside the space", todoShown, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			return worker(t, e, "/home/owner/code/app")
		}},
		{"done, its run outside the space", todoShown, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "s1")
			e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`)
			return id
		}},
		{"its run taken off, its computer not answering, the link's directory outside the space", todoShown,
			func(t *testing.T, e *todoEnv, _ *db.DB) string {
				id := linked(t, e, "conv1", "s2")
				e.must("DELETE", "/api/v1/todos/"+id+"/agents/server/s2", "")
				unreached(e)
				return id
			}},
		{"its run taken off, listed outside the space", todoShown, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			id := linked(t, e, "conv1", "s1")
			e.must("DELETE", "/api/v1/todos/"+id+"/agents/server/s1", "")
			return id
		}},
		{"opened from a conversation since deleted, outside the space", todoShown, func(t *testing.T, e *todoEnv, database *db.DB) string {
			id := opened(t, e, "c0")
			row := deletedConversation{Machine: "server", ID: "c0", At: time.Now().UTC()}
			if database != nil {
				raw, _ := json.Marshal(row)
				if err := database.PutConversationDeletion(context.Background(), "server", "c0", raw); err != nil {
					t.Fatal(err)
				}
			} else {
				b := &e.s.deletedConversations
				b.mu.Lock()
				b.rows, b.loaded = map[string]deletedConversation{runtimeListID("server", "c0"): row}, true
				b.mu.Unlock()
			}
			e.sessions("server", without("c0")...)
			return id
		}},
		{"opened from the main conversation, kept on the assistant's former computer", todoShown, func(t *testing.T, e *todoEnv, _ *db.DB) string {
			setSettings(t, e.s, func(v *Settings) { v.Main, v.MainMachineID = "m1", "laptop" })
			e.sessions("laptop", session("m1", "Persona", "", "", true))
			return opened(t, e, "m1")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
				e.sessions("server", listed...)
				id := c.setup(t, e, database)
				if database != nil { // the owner's answer to a health run's card: a decision of its to-do
					if _, err := e.s.insertDecision(context.Background(), e.s.todoDB(), db.TodoDecisionRow{ID: "dec_" + randomHex(8),
						TodoID: id, Question: "Take the new dose at night?", Choice: "At night", Source: db.DecisionCard,
						SourceMachine: "server", SourceSession: "h1", SourceMessage: "ix_dose", CreatedAt: decisionNow()}); err != nil {
						t.Fatal(err)
					}
					if w := e.must("GET", "/api/v1/todos/"+id+"/decisions", ""); !strings.Contains(w.Body.String(), "At night") {
						t.Errorf("a device's decisions: %s", w.Body)
					}
				}
				if w := e.must("GET", "/api/v1/todos/"+id+"/timeline", ""); !strings.Contains(w.Body.String(), told) ||
					!strings.Contains(w.Body.String(), summary) {
					t.Errorf("a device's timeline: %s", w.Body)
				}
				extensionReads(t, e, id, c.verdict, told, summary)
			})
		})
	}
}

// extensionReads checks what an extension's token reads of to-do id: shown, its row on the list and on its own is a
// device's, and its timeline (which has words) and its decisions answer as a device's; not shown, it is not on the
// list, and its item, timeline and decisions answer 404 todo_not_found as for a to-do that does not exist (503
// todo_not_told_yet while it cannot be told yet, verdict todoUntold), with none of words.
func extensionReads(t *testing.T, e *todoEnv, id string, verdict todoVerdict, words ...string) {
	t.Helper()
	shown := verdict == todoShown
	dev, _ := rowsAs(t, e, "")
	ext, list := rowsAs(t, e, roleExtension)
	if _, listed := ext[id]; listed != shown || shown && !reflect.DeepEqual(ext[id], dev[id]) {
		t.Errorf("an extension's list (shown %v): %s\na device's row: %v", shown, list, dev[id])
	}
	paths := []string{"/api/v1/todos/" + id, "/api/v1/todos/" + id + "/timeline"}
	if e.s.todoDB() != nil {
		paths = append(paths, "/api/v1/todos/"+id+"/decisions", "/api/v1/todos/"+id+"/decisions?all=1")
	}
	for _, path := range paths {
		w := e.doAs(roleExtension, "GET", path, "")
		var refused struct{ Code string }
		_ = json.Unmarshal(w.Body.Bytes(), &refused)
		if !shown {
			status, code := http.StatusNotFound, "todo_not_found"
			if verdict == todoUntold { // not yet told: tried again, never read as a to-do that does not exist (#12216)
				status, code = http.StatusServiceUnavailable, "todo_not_told_yet"
			}
			if w.Code != status || refused.Code != code || slices.ContainsFunc(words, func(word string) bool {
				return strings.Contains(w.Body.String(), word)
			}) {
				t.Errorf("an extension at %s (not shown): %d %s", path, w.Code, w.Body)
			}
			continue
		}
		if dw := e.must("GET", path, ""); w.Code != http.StatusOK || w.Body.String() != dw.Body.String() {
			t.Errorf("an extension at %s: %d %s\na device's: %s", path, w.Code, w.Body, dw.Body)
		}
	}
	if shown && !strings.Contains(e.doAs(roleExtension, "GET", "/api/v1/todos/"+id+"/timeline", "").Body.String(), words[0]) {
		t.Errorf("an extension's timeline of %s has no %q", id, words[0])
	}
}

// rowsAs is every row of GET /api/v1/todos as role reads it ("" a device), by id, as JSON values, and the answer's
// bytes.
func rowsAs(t *testing.T, e *todoEnv, role string) (map[string]map[string]any, []byte) {
	t.Helper()
	return readTodoRows(t, role, e.doAs(role, "GET", "/api/v1/todos", ""))
}

func readTodoRows(t *testing.T, role string, w *httptest.ResponseRecorder) (map[string]map[string]any, []byte) {
	t.Helper()
	var list struct {
		Todos []map[string]any `json:"todos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK {
		t.Fatalf("the list as %q: %d %s", role, w.Code, w.Body)
	}
	out := map[string]map[string]any{}
	for _, r := range list.Todos {
		out[r["id"].(string)] = r
	}
	return out, w.Body.Bytes()
}

// A to-do past the feed items one read takes (todoTimelineFeedCap, the newest) serves the rounds that began within
// them, each whole: not its start, nor the round of a decision older than the oldest item kept; rounds_total counts
// from there, and the page before the first round served is empty with nothing more.
func TestTodoTimelinePastItsFeedCap(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		ctx := context.Background()
		e.sessions("server", proto.Session{SessionID: "s1", Title: "Letters", Status: proto.StatusIdle, State: proto.StateIdle})
		id := e.create("Answer the letters").ID
		e.must("PUT", "/api/v1/todos/"+id+"/agents/server/s1", "") // its start round: the run linked
		if database != nil {
			e.must("POST", "/api/v1/todos/"+id+"/decisions", `{"question":"Which first?","choice":"The oldest"}`)
		}
		t0 := time.Now().UTC().Add(time.Minute)
		var ids []string
		for i := range todoTimelineFeedCap + 1 {
			it, err := e.s.appendFeedAt(ctx, "conv1", feedRouted, id, map[string]string{"text": fmt.Sprint("letter ", i)},
				t0.Add(time.Duration(i)*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, "r:feed:"+it.ID)
		}
		var item struct{ assistantwire.TodoItemDetail }
		body := e.must("GET", "/api/v1/todos/"+id, "").Body.Bytes()
		if err := json.Unmarshal(body, &item); err != nil || item.RoundsTotal == nil || *item.RoundsTotal != todoTimelineFeedCap {
			t.Fatalf("rounds_total: %v %s", err, body)
		}
		var tl assistantwire.TodoTimeline
		_ = json.Unmarshal(e.must("GET", "/api/v1/todos/"+id+"/timeline?limit=5&before="+url.QueryEscape(ids[3]), "").Body.Bytes(), &tl)
		if got := roundIDs(tl.Rounds); !slices.Equal(got, ids[1:3]) || tl.More {
			t.Errorf("the oldest page: %v (more %v), want %v", got, tl.More, ids[1:3])
		}
		_ = json.Unmarshal(e.must("GET", "/api/v1/todos/"+id+"/timeline?before="+url.QueryEscape(ids[1]), "").Body.Bytes(), &tl)
		if len(tl.Rounds) != 0 || tl.More {
			t.Errorf("before the first round served: %v (more %v)", roundIDs(tl.Rounds), tl.More)
		}
	})
}
