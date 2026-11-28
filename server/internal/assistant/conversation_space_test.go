package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The conversations' spaces as the manager notes them (conversation_space.go, #10941): the rule that keeps the health
// space's words from an extension tells a conversation its computer no longer lists (quiet for a week, or past its 100
// newest) and one it has not reported since a restart by what was noted, so the owner's ordinary tasks stay shown, a
// health conversation's stay hidden, and one never noted stays hidden (fail closed). The names and words are made up.

// quietSessions are the assistant's computer's conversations: the main one conv1, an ordinary chat conv2, a direction
// conv_lead that supervises tasks, a health conversation h0, and a run s1 outside the space.
func quietSessions() (main, chat, lead, health, run proto.Session) {
	main, chat = scopeSession("conv1", "Persona", ""), scopeSession("conv2", "Moving house", "")
	lead, health = scopeSession("conv_lead", "Home", ""), scopeSession("h0", "Sleep", identity.HealthDir("/home/owner"))
	main.Assistant, chat.Assistant, lead.Assistant, health.Assistant = true, true, true, true
	return main, chat, lead, health, scopeSession("s1", "Paperwork", "/home/owner/forms")
}

// quietTasks are a task conv2 handed out, one of the main conversation's, one conv_lead supervises, one h0 handed out and
// one from a conversation no computer ever listed, made while their conversations are listed, and a to-do made by hand.
type quietTasks struct{ chat, main, led, health, ghost, own Todo }

func newQuietTasks(t *testing.T, e *todoEnv) quietTasks {
	t.Helper()
	ctx := context.Background()
	main, chat, lead, health, run := quietSessions()
	e.sessions("server", main, chat, lead, health, run)
	task := func(title, origin string) Todo {
		t.Helper()
		got, err := e.s.createTaskTodo(ctx, title, "", origin, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	q := quietTasks{chat: task("Book the movers", "conv2"), main: task("Renew the lease", "conv1"),
		led: task("Paint the hall", "conv1"), health: task("Book the sleep clinic", "h0"), ghost: task("Find the deeds", "ghost")}
	e.s.superviseTodo(ctx, q.led.ID, "conv_lead", nil, 0)
	q.own = e.create("Water the plants")
	return q
}

// extensionDo is a request of an extension's token, the hub naming the extension.
func extensionDo(e *todoEnv, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(RoleHeader, roleExtension)
	r.Header.Set(ExtensionHeader, "example-mover")
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

// extensionSees says which of ids GET /api/v1/todos answers an extension's token.
func extensionSees(t *testing.T, e *todoEnv, ids ...string) map[string]bool {
	t.Helper()
	w := e.doAs(roleExtension, "GET", "/api/v1/todos", "")
	if w.Code != http.StatusOK {
		t.Fatalf("an extension's list: %d %s", w.Code, w.Body)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = strings.Contains(w.Body.String(), id)
	}
	return seen
}

// wantUntold fails unless an extension's GET of to-do id and of its timeline answer 503 todo_not_told_yet with
// Retry-After, a to-do whose verdict cannot be told yet (#12216 item 12), and unless neither answer carries any of words.
func wantUntold(t *testing.T, e *todoEnv, when, id string, words ...string) {
	t.Helper()
	for _, path := range []string{"/api/v1/todos/" + id, "/api/v1/todos/" + id + "/timeline"} {
		got := extensionDo(e, "GET", path, "")
		if got.Code != http.StatusServiceUnavailable || !strings.Contains(got.Body.String(), `"todo_not_told_yet"`) ||
			got.Header().Get("Retry-After") == "" {
			t.Errorf("%s: an extension's GET %s: %d %s (Retry-After %q), want 503 todo_not_told_yet with Retry-After",
				when, path, got.Code, got.Body, got.Header().Get("Retry-After"))
		}
		noneOf(t, when+": an extension's GET "+path, got.Body.Bytes(), words...)
	}
}

// wantShown fails unless an extension's list has exactly the to-dos of shown among those of all.
func wantShown(t *testing.T, e *todoEnv, when string, all []Todo, shown ...Todo) {
	t.Helper()
	ids := make([]string, len(all))
	for i, td := range all {
		ids[i] = td.ID
	}
	seen := extensionSees(t, e, ids...)
	for _, td := range all {
		want := false
		for _, s := range shown {
			want = want || s.ID == td.ID
		}
		if seen[td.ID] != want {
			t.Errorf("%s: an extension is shown %q: %v, want %v", when, td.Title, seen[td.ID], want)
		}
	}
}

// The regression check of #10941: a task from a chat its computer no longer lists, one a quiet direction supervises
// and, while the computer lists nothing (a restart, before the node is back), the main conversation's, all stay on an
// extension's list and answer its routes; the health conversation's stays hidden (404, as one that does not exist),
// and one from a conversation never noted, whose space cannot be told yet, stays off the list while its routes answer
// 503 todo_not_told_yet with Retry-After rather than "does not exist" (#12216 item 12), carrying none of its words. On
// the store the spaces outlive a restart of the manager.
func TestQuietConversationsTasksStayShown(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		q := newQuietTasks(t, e)
		all := []Todo{q.chat, q.main, q.led, q.health, q.ghost, q.own}
		wantShown(t, e, "all listed", all, q.chat, q.main, q.led, q.own)

		// A week later the computer lists only the main conversation and the run.
		main, _, _, _, run := quietSessions()
		e.sessions("server", main, run)
		wantShown(t, e, "the chats quiet", all, q.chat, q.main, q.led, q.own)
		writes := []struct{ method, path, body string }{
			{"GET", "/api/v1/todos/" + q.chat.ID, ""},
			{"GET", "/api/v1/todos/" + q.led.ID, ""},
			{"PATCH", "/api/v1/todos/" + q.chat.ID, `{"notes":"two vans"}`},
			{"GET", "/api/v1/assistant/conversations/conv2/feed", ""},
			{"GET", "/api/v1/assistant/conversations/conv_lead/feed", ""},
		}
		if database != nil { // decisions are kept in the database only
			writes = append(writes, struct{ method, path, body string }{"POST", "/api/v1/todos/" + q.chat.ID + "/decisions",
				`{"question":"Which day?","choice":"Saturday"}`})
		}
		for _, w := range writes {
			if got := extensionDo(e, w.method, w.path, w.body); got.Code != http.StatusOK {
				t.Errorf("an extension's %s %s once its chat is quiet: %d %s", w.method, w.path, got.Code, got.Body)
			}
		}
		for _, path := range []string{"/api/v1/todos/" + q.health.ID,
			"/api/v1/assistant/conversations/h0/feed", "/api/v1/assistant/conversations/ghost/feed"} {
			if got := e.doAs(roleExtension, "GET", path, ""); got.Code != http.StatusNotFound {
				t.Errorf("an extension's GET %s: %d %s", path, got.Code, got.Body)
			}
		}
		wantUntold(t, e, "the chats quiet", q.ghost.ID, q.ghost.Title)

		// The computer lists nothing yet.
		e.sessions("server")
		wantShown(t, e, "nothing listed", all, q.chat, q.main, q.led, q.own)

		if database == nil {
			return
		}
		// The manager starts again on the same store; its computer has not reported.
		again := newTodoEnv(t, database)
		wantShown(t, again, "after a restart", all, q.chat, q.main, q.led, q.own)
	})
}

// No event is lost to the rule: todo.changed and todo.decision of a task whose chat its computer no longer lists, or
// of the main conversation's while it lists nothing, are told; one whose verdict cannot be told yet (its conversation
// never listed) waits, and is told once it can be; the health conversation's is never told.
func TestQuietConversationsEventsAreTold(t *testing.T) {
	todoEventStores(t, func(t *testing.T, e *todoEventEnv, database *db.DB) {
		q := newQuietTasks(t, e.todoEnv)
		// decide writes a decision on to-do td (in the database only: in memory there are none to tell).
		decide := func(td Todo, question, choice string) {
			t.Helper()
			if database != nil {
				e.must("POST", "/api/v1/todos/"+td.ID+"/decisions", `{"question":"`+question+`","choice":"`+choice+`"}`)
			}
		}
		told := func(when string, got []recordedKBEvent, want ...string) {
			t.Helper()
			var names []string
			for _, ev := range got {
				var p struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(ev.raw, &p)
				names = append(names, ev.name+" "+p.ID)
			}
			slices.Sort(names)
			want = slices.Sorted(slices.Values(want))
			if !slices.Equal(names, want) {
				t.Errorf("%s: told %v, want %v", when, names, want)
			}
		}
		changed := func(td Todo) string { return assistantwire.EventTodoChanged + " " + td.ID }
		decided := func(td Todo) []string {
			if database == nil {
				return nil
			}
			return []string{assistantwire.EventTodoDecision + " " + td.ID}
		}
		told("created, all listed", e.settle(), changed(q.chat), changed(q.main), changed(q.led), changed(q.own))

		main, _, _, _, run := quietSessions()
		e.sessions("server", main, run)
		e.must("PATCH", "/api/v1/todos/"+q.chat.ID, `{"title":"Book the movers for Saturday"}`)
		e.must("PATCH", "/api/v1/todos/"+q.led.ID, `{"title":"Paint the hall white"}`)
		e.must("PATCH", "/api/v1/todos/"+q.health.ID, `{"title":"Book the sleep clinic soon"}`)
		told("retitled once the chats are quiet", e.settle(), changed(q.chat), changed(q.led))
		decide(q.chat, "Which day?", "Saturday")
		told("a decision once its chat is quiet", e.told(), decided(q.chat)...)

		e.sessions("server")
		e.must("PATCH", "/api/v1/todos/"+q.main.ID, `{"title":"Renew the lease this week"}`)
		told("the main conversation's while nothing is listed", e.settle(), changed(q.main))

		// One never listed: nothing is told of it while its verdict cannot be told, not even after a retry.
		e.must("PATCH", "/api/v1/todos/"+q.ghost.ID, `{"title":"Find the deeds today"}`)
		decide(q.ghost, "Which drawer?", "The top one")
		decide(q.health, "Which night?", "Friday")
		told("never listed", e.settle())
		e.clock.advance(todoRetryAfter)
		told("never listed, judged again", e.told())
		// Its conversation is listed now, outside the space: what waited is told, its creation as it is now and its
		// decision; the health conversation's still is not.
		ghost := scopeSession("ghost", "Papers", "")
		ghost.Assistant = true
		e.sessions("server", ghost)
		e.clock.advance(todoRetryDelay(1))
		got := e.told()
		told("listed at last", got, append(decided(q.ghost), changed(q.ghost))...)
		for _, ev := range got {
			if ev.name != assistantwire.EventTodoChanged {
				continue
			}
			if c := changedOf(t, ev); c.What != assistantwire.TodoCreated || c.Title != "Find the deeds today" {
				t.Errorf("what waited: %+v", c)
			}
		}
		e.clock.advance(todoRetryMost)
		told("nothing more", e.told())
	})
}

// outcomeTimers holds the judgements again of s's held outcome lines (holdOutcomeLine) for the test to run.
type outcomeTimers struct {
	mu  sync.Mutex
	due []func()
}

func fakeOutcomeTimers(s *Service) *outcomeTimers {
	c := &outcomeTimers{}
	h := &s.outcomeLines
	h.mu.Lock()
	h.after = func(_ time.Duration, f func()) { c.mu.Lock(); c.due = append(c.due, f); c.mu.Unlock() }
	h.mu.Unlock()
	return c
}

// fire runs the judgements due now; it says how many there were.
func (c *outcomeTimers) fire() int {
	c.mu.Lock()
	due := c.due
	c.due = nil
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
	return len(due)
}

// A closed task's outcome line waits while its to-do's verdict cannot be told (the conversation that handed it out
// was never noted and is not listed): a project's timeline is outside the health space and kb.read reads it, so the
// line is written only once the conversation is told outside the space, and dropped once it is told in it
// (TestHiddenTaskOutcomeStaysOutOfProjects: one known at close is never held).
func TestOutcomeLineWaitsForItsVerdict(t *testing.T) {
	for _, store := range []string{"memory", "store"} {
		for _, health := range []bool{false, true} {
			name := store + "/main space"
			if health {
				name = store + "/health space"
			}
			t.Run(name, func(t *testing.T) {
				var st Store
				if store == "store" {
					st = todoTestDB(t)
				}
				s, f, todo := outcomeEnv(t, st, outcomeReply)
				timers := fakeOutcomeTimers(s)
				f.setSessions("server") // conv1, never noted, no longer listed
				if v := s.todoVerdicts(context.Background(), []string{todo.ID}, nil, nil)[todo.ID]; v != todoUntold {
					t.Fatalf("the task's verdict: %d, want untold", v)
				}
				f.outcome = `{"outcome":"修好登录页横屏崩溃，PR 已合并","links":["https://github.com/o/weather/pull/12"]}`
				s.recordTaskOutcome(todo, time.Now().UTC())
				timeline := func() []string { return kbEng(t, s).Store.Log("天气-app", 10) }
				if log := timeline(); len(log) != 0 {
					t.Fatalf("the project's timeline while the verdict cannot be told: %q", log)
				}
				if n := timers.fire(); n != 1 || len(timeline()) != 0 {
					t.Fatalf("judged again while still untold: %d judgements, timeline %q", n, timeline())
				}
				conv1 := proto.Session{SessionID: "conv1", Title: "Persona", Assistant: true}
				if health {
					conv1.Cwd, conv1.Space = identity.HealthDir("/home/owner"), SpaceHealth
				}
				f.setSessions("server", conv1) // listed again, as the backfill asks
				if n := timers.fire(); n != 1 {
					t.Fatalf("judgements once told: %d, want 1", n)
				}
				log := timeline()
				if health {
					if len(log) != 0 {
						t.Fatalf("the project's timeline once the task is told the health space's: %q", log)
					}
				} else if len(log) != 1 || !strings.Contains(log[0], "修好登录页横屏崩溃") {
					t.Fatalf("the project's timeline once the task is told outside the space: %q", log)
				}
				if n := timers.fire(); n != 0 {
					t.Fatalf("still judged again after it was told: %d", n)
				}
			})
		}
	}
}

// A health conversation that wrote to a task's run (a routed item in its feed), quiet since before the manager noted
// spaces, is neither the task's origin nor its supervisor: the backfill still has its computer list it, so the task's
// verdict is told the health space's, and its outcome line, held while untold, is never written where kb.read reads it.
func TestOutcomeLineOfAQuietHealthFeedConversation(t *testing.T) {
	database := todoTestDB(t)
	e := projectEnv(t, database)
	timers := fakeOutcomeTimers(e.s)
	ctx := context.Background()
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	main, h1 := scopeSession("conv1", "Persona", ""), scopeSession("h1", "Heart", identity.HealthDir("/home/owner"))
	main.Assistant, h1.Assistant = true, true
	run := scopeSession("s2", "Charts", "/home/owner/charts")
	run.LastText = "Charted the resting heart rate against the new dose"
	e.sessions("server", main, run) // h1 is quiet past a week, and was never noted
	task, err := e.s.createTaskTodo(ctx, "Chart the readings", "", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	e.must("PUT", "/api/v1/todos/"+task.ID+"/agents/server/s2", "")
	e.must("PATCH", "/api/v1/todos/"+task.ID, `{"project":"诊断能力"}`)
	if _, err := e.s.appendFeedAt(ctx, "h1", feedRouted, task.ID, map[string]string{"text": "Resting heart rate was 48",
		"machine_id": "server", "session_id": "s2"}, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.must("PATCH", "/api/v1/todos/"+task.ID, `{"done":true}`)
	e.s.tasks.outcomeRuns.Wait()
	if log := kbEng(t, e.s).Store.Log("诊断能力", 10); len(log) != 0 {
		t.Fatalf("the project's timeline while h1 cannot be told: %q", log)
	}
	if keep := e.s.profile(ctx, []TaskView{}, &TodoReport{}).Keep; !slices.Contains(keep, SessionRef{MachineID: "server", SessionID: "h1"}) {
		t.Fatalf("kept %v, want h1 listed for its space to be noted", keep)
	}
	e.sessions("server", main, run, h1) // listed as kept
	e.sessions("server", main, run)     // quiet again
	timers.fire()
	if log := kbEng(t, e.s).Store.Log("诊断能力", 10); len(log) != 0 {
		t.Fatalf("the project's timeline once h1 is told the health space's: %q", log)
	}
	if v := e.s.todoVerdicts(ctx, []string{task.ID}, nil, nil)[task.ID]; v != todoHidden {
		t.Fatalf("the task's verdict: %d, want hidden", v)
	}
}

// A conversation noted in the health space on any computer is told so wherever else it is noted or listed outside it,
// and so is one listed in the space after it was noted outside it: on the store, a note outside the space is read back
// rather than taken as it was written (convSpaceBook).
func TestConversationSpaceHealthNoteWins(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		ctx := context.Background()
		e.s.noteConversationSpaces(ctx, []db.ConversationSpace{{MachineID: "laptop", SessionID: "x", Space: SpaceHealth}})
		chat := scopeSession("x", "Sleep", "")
		chat.Assistant = true
		e.sessions("server", chat) // listed outside the space on the assistant's computer
		moved := scopeSession("y", "Moving house", "")
		moved.Assistant = true
		e.sessions("server", chat, moved)
		if got := e.s.conversationSpaces(ctx, []string{"y"})["y"]; !got.known || got.space != "" {
			t.Fatalf("y listed outside the space: %+v", got)
		}
		moved.Space = SpaceHealth
		e.sessions("server", chat, moved)
		e.sessions("server")
		got := e.s.conversationSpaces(ctx, []string{"x", "y"})
		for _, id := range []string{"x", "y"} {
			if !got[id].known || got[id].space != SpaceHealth {
				t.Errorf("%s: %+v, want the health space", id, got[id])
			}
		}
		if database == nil {
			return
		}
		// The manager starts again, and its computer lists x outside the space first.
		again := newTodoEnv(t, database)
		again.sessions("server", chat)
		got = again.s.conversationSpaces(ctx, []string{"x", "y"})
		for _, id := range []string{"x", "y"} {
			if !got[id].known || got[id].space != SpaceHealth {
				t.Errorf("after a restart, %s: %+v, want the health space", id, got[id])
			}
		}
	})
}

// backfillTimers is a clock the test moves for s's backfill (conversation_space_backfill.go), and its checks, which the
// test runs.
type backfillTimers struct {
	mu  sync.Mutex
	now time.Time
	due []func()
}

func fakeBackfill(s *Service) *backfillTimers {
	c := &backfillTimers{now: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
	b := &s.spaceFill
	b.mu.Lock()
	b.clock = func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
	b.after = func(_ time.Duration, f func()) { c.mu.Lock(); c.due = append(c.due, f); c.mu.Unlock() }
	b.mu.Unlock()
	return c
}

func (c *backfillTimers) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *backfillTimers) fire() {
	c.mu.Lock()
	due := c.due
	c.due = nil
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// keeps says which of ids the profile has the assistant's computer list however quiet, once the backfill's read of the
// to-dos a profile sets off (spaceBackfillScanSoon) is done.
func keeps(t *testing.T, s *Service, ids ...string) map[string]bool {
	t.Helper()
	s.profile(context.Background(), []TaskView{}, &TodoReport{})
	waitFor(t, "the backfill's read of the to-dos", s.spaceBackfillScanIdle)
	p := s.profile(context.Background(), []TaskView{}, &TodoReport{})
	if p == nil {
		t.Fatal("no profile")
	}
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = slices.Contains(p.Keep, SessionRef{MachineID: "server", SessionID: id})
	}
	return out
}

// The regression check of #10941, again: a conversation quiet since before the manager noted spaces (an upgrade; a
// chat quiet more than a week, or past the 100 newest, is listed by no computer) was never noted, so every to-do it
// opened, done or not, was hidden from extensions for good. The backfill has the assistant's computer list such a
// conversation however quiet until it is noted, then no longer: the to-dos are shown, their routes and its feed answer,
// and the chat leaves the list again. One whose transcript is gone is given up once kept a while with the computer on
// (not while it is off), and its to-do stays hidden (fail closed). On the store the conversations are found by reading
// the to-dos when the manager starts; in memory, by the verdict that could not tell them.
func TestNeverNotedConversationsAreListedUntilNoted(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		ctx := context.Background()
		clock := fakeBackfill(e.s)
		setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
		main, old := scopeSession("conv1", "Persona", ""), scopeSession("conv_old", "Moving house", "")
		main.Assistant, old.Assistant = true, true
		e.sessions("server", main) // conv_old and conv_gone are never listed: quiet since before the upgrade
		task := func(title, origin string) Todo {
			t.Helper()
			got, err := e.s.createTaskTodo(ctx, title, "", origin, "", "")
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		done, open, gone := task("Book the movers", "conv_old"), task("Renew the lease", "conv_old"), task("Find the deeds", "conv_gone")
		fromMain := task("Water the plants", "conv1")
		e.must("PATCH", "/api/v1/todos/"+done.ID, `{"done":true}`)
		e.must("PATCH", "/api/v1/todos/"+gone.ID, `{"done":true}`)
		// A done task of the main conversation another quiet chat wrote to (its history).
		told := task("Pay the deposit", "conv1")
		if _, err := e.s.appendFeedAt(ctx, "conv_told", feedRouted, told.ID, map[string]string{"text": "Paid by card"},
			time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		e.must("PATCH", "/api/v1/todos/"+told.ID, `{"done":true}`)
		all := []Todo{done, open, gone, fromMain, told}
		quiet := []string{"conv_old", "conv_gone", "conv_told"}
		if database != nil { // the manager starts again: it reads the to-dos for the conversations never noted
			e = newTodoEnv(t, database)
			clock = fakeBackfill(e.s)
			e.sessions("server", main)
			if got := keeps(t, e.s, quiet...); !got["conv_old"] || !got["conv_gone"] || !got["conv_told"] {
				t.Fatalf("kept once the to-dos are read: %v", got)
			}
		}
		wantShown(t, e, "never noted", all, fromMain)
		if got := keeps(t, e.s, append(quiet, "conv1")...); !got["conv_old"] || !got["conv_gone"] || !got["conv_told"] || !got["conv1"] {
			t.Fatalf("kept: %v", got)
		}

		// The computer lists what is kept: conv_old's and conv_told's transcripts are there, conv_gone's is not.
		toldChat := scopeSession("conv_told", "Deposit", "")
		toldChat.Assistant = true
		e.sessions("server", main, old, toldChat)
		if got := keeps(t, e.s, quiet...); got["conv_old"] || got["conv_told"] || !got["conv_gone"] {
			t.Fatalf("kept once conv_old and conv_told are noted: %v", got)
		}
		e.sessions("server", main) // and leaves them out again
		wantShown(t, e, "noted", all, done, open, fromMain, told)
		for _, path := range []string{"/api/v1/todos/" + done.ID, "/api/v1/todos/" + open.ID,
			"/api/v1/assistant/conversations/conv_old/feed"} {
			if got := e.doAs(roleExtension, "GET", path, ""); got.Code != http.StatusOK {
				t.Errorf("an extension's GET %s once conv_old is noted: %d %s", path, got.Code, got.Body)
			}
		}

		// conv_gone is kept while the computer is off, however long.
		e.f.mu.Lock()
		e.f.down = map[string]bool{"server": true}
		e.f.mu.Unlock()
		clock.advance(spaceBackfillWait)
		clock.fire()
		if got := keeps(t, e.s, "conv_gone"); !got["conv_gone"] {
			t.Fatal("conv_gone given up while the computer is off")
		}
		e.f.mu.Lock()
		e.f.down = nil
		e.f.mu.Unlock()
		clock.advance(spaceBackfillWait / 2)
		clock.fire()
		if got := keeps(t, e.s, "conv_gone"); !got["conv_gone"] {
			t.Fatal("conv_gone given up before it was kept spaceBackfillWait with the computer on")
		}
		clock.advance(spaceBackfillWait / 2)
		clock.fire()
		if got := keeps(t, e.s, "conv_gone"); got["conv_gone"] {
			t.Fatal("conv_gone still kept once given up")
		}
		wantShown(t, e, "conv_gone given up", all, done, open, fromMain, told)
		if got := keeps(t, e.s, "conv_gone"); got["conv_gone"] {
			t.Fatal("conv_gone taken up again by the verdict that could not tell it")
		}
		if database == nil {
			return
		}
		// A redeploy does not keep it again, by the scan or by a verdict; a week later one does.
		again := newTodoEnv(t, database)
		fakeBackfill(again.s)
		again.sessions("server", main)
		wantShown(t, again, "conv_gone given up, after a restart", all, done, open, fromMain, told)
		if got := keeps(t, again.s, "conv_gone"); got["conv_gone"] {
			t.Fatal("conv_gone kept again after a restart")
		}
		later := newTodoEnv(t, database)
		fakeBackfill(later.s).advance(spaceBackfillRetry + 2*spaceBackfillWait)
		later.sessions("server", main)
		if got := keeps(t, later.s, "conv_gone"); !got["conv_gone"] {
			t.Fatal("conv_gone not kept again a week after it was given up")
		}
	})
}

// The backfill keeps at most a batch at a time, and fills it again as the conversations in it are noted; a task
// leader's agent that supervises an open task is kept on its own computer, and a conversation already noted is not.
func TestSpaceBackfillBatches(t *testing.T) {
	e := newTodoEnv(t, nil)
	ctx := context.Background()
	fakeBackfill(e.s)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	noted := scopeSession("conv_noted", "Noted", "")
	noted.Assistant = true
	e.sessions("server", noted)
	var ids []string
	for i := range spaceBackfillBatch + 5 {
		id := fmt.Sprintf("conv_%03d", i)
		ids = append(ids, id)
		if _, err := e.s.createTaskTodo(ctx, "Task "+id, "", id, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	led, err := e.s.createTaskTodo(ctx, "Fix the gate", "", "conv_noted", "", "")
	if err != nil {
		t.Fatal(err)
	}
	e.s.superviseTodo(ctx, led.ID, "laptop/lead1", nil, 0)
	waitFor(t, "the backfill's read of the to-dos", e.s.spaceBackfillScanIdle)
	e.doAs(roleExtension, "GET", "/api/v1/todos", "") // in memory, the verdicts that could not tell them take them up
	kept := e.s.spaceBackfillKept()
	if len(kept) != spaceBackfillBatch || slices.Contains(kept, "conv_noted") {
		t.Fatalf("kept %d: %v", len(kept), kept)
	}
	p := e.s.profile(ctx, []TaskView{}, &TodoReport{})
	if !slices.Contains(p.Keep, SessionRef{MachineID: "laptop", SessionID: "lead1"}) {
		t.Fatalf("the leader's agent is not kept: %v", p.Keep)
	}
	var listed []proto.Session
	for _, id := range kept {
		c := scopeSession(id, "Chat", "")
		c.Assistant = true
		listed = append(listed, c)
	}
	e.sessions("server", listed...)
	rest := e.s.spaceBackfillKept()
	if len(rest) != 5 || slices.ContainsFunc(rest, func(id string) bool { return slices.Contains(kept, id) }) {
		t.Fatalf("kept once the first batch is noted: %v", rest)
	}
}

// The regression check of #10941, a forged session: a computer with no transcript of a conversation's id starts a new
// one under it when asked to resume it (an extension's resume, refused at the hub and the node now). A to-do whose
// health chat was never noted was then told by that session's row, a terminal's outside the space: shown to
// extensions, its held outcome line written to the project's timeline (kb.read). A row tells a conversation only as the
// assistant's or as one in the space now, as a note is taken; the chat's real row in the space still hides it. Until
// then the task cannot be told: its routes answer 503 todo_not_told_yet with Retry-After rather than "does not exist"
// (#12216 item 12), and neither they, the list nor the project's timeline carry its words; once the chat is listed in
// the space they answer 404 and its verdict is hidden.
func TestNeverNotedChatIsNotToldByASessionUnderItsID(t *testing.T) {
	const title, reply = "Book the sleep clinic", "Booked the sleep clinic for Tuesday 9:00 after the new dose"
	projectStores(t, func(t *testing.T, e *todoEnv) {
		ctx := context.Background()
		timers := fakeOutcomeTimers(e.s)
		setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
		main := scopeSession("conv1", "Persona", "")
		main.Assistant = true
		run := scopeSession("s2", "Clinic booking", "/home/owner/errands")
		run.LastText = reply
		e.sessions("server", main) // h0, the health chat that opened the task, is quiet since before the notes
		e.sessions("box", run)
		task, err := e.s.createTaskTodo(ctx, title, "", "h0", "", "")
		if err != nil {
			t.Fatal(err)
		}
		e.must("PUT", "/api/v1/todos/"+task.ID+"/agents/box/s2", "")
		e.must("PATCH", "/api/v1/todos/"+task.ID, `{"project":"诊断能力"}`)
		forged := scopeSession("h0", "hi", "/home/owner/errands")
		forged.PaneID, forged.Launcher = "w7:p1", "claude"
		e.sessions("server", main, forged)
		hidden := func(when string, untold bool) {
			t.Helper()
			if untold {
				wantUntold(t, e, when, task.ID, title, reply)
			} else {
				for _, path := range []string{"/api/v1/todos/" + task.ID, "/api/v1/todos/" + task.ID + "/timeline"} {
					if got := extensionDo(e, "GET", path, ""); got.Code != http.StatusNotFound {
						t.Errorf("%s: an extension's GET %s: %d %s", when, path, got.Code, got.Body)
					}
				}
			}
			noneOf(t, when+": an extension's GET /api/v1/todos", extensionDo(e, "GET", "/api/v1/todos", "").Body.Bytes(), title)
			noneOf(t, when+": the project's timeline", []byte(strings.Join(kbEng(t, e.s).Store.Log("诊断能力", 20), "\n")), title, reply)
		}
		hidden("a terminal's session under h0's id", true)
		e.must("PATCH", "/api/v1/todos/"+task.ID, `{"done":true}`)
		e.s.tasks.outcomeRuns.Wait()
		timers.fire()
		hidden("closed, its outcome line judged again", true)
		h0 := scopeSession("h0", "Sleep", identity.HealthDir("/home/owner"))
		h0.Assistant = true
		e.sessions("server", main, forged, h0) // the chat's own row, in the space, as the backfill asks
		waitFor(t, "the note written", e.s.listedSpacesWritten)
		timers.fire()
		hidden("h0 listed in the space", false)
		if v := e.s.todoVerdicts(ctx, []string{task.ID}, nil, nil)[task.ID]; v != todoHidden {
			t.Fatalf("the task's verdict: %d, want hidden", v)
		}
	})
}
