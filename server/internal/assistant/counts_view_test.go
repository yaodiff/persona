package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount/fleetcountadapt"
	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/proto"
)

// needsYouFixtures defines the Agents list's Needs you (spec/fixtures/needs-you/README.md), which agents_needs_you is.
const needsYouFixtures = "../../../spec/fixtures/needs-you/cases.json"

type needsYouCase struct {
	Name  string `json:"name"`
	Input struct {
		Sessions     []proto.Session     `json:"sessions"`
		Interactions []proto.Interaction `json:"interactions"`
		Mutes        Pins                `json:"mutes"`
		Quiet        []string            `json:"quiet"`
	} `json:"input"`
	Expected struct {
		BadgeRows []struct {
			ID    string   `json:"id"`
			Cards []string `json:"cards"`
		} `json:"badge_rows"`
		Badge int `json:"badge"`
	} `json:"expected"`
}

func readNeedsYouCases(t *testing.T) []needsYouCase {
	t.Helper()
	b, err := os.ReadFile(needsYouFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []needsYouCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("%s: %v (%d cases)", needsYouFixtures, err, len(f.Cases))
	}
	return f.Cases
}

// On one live round, agents_needs_you is the needs-you fixture's badge over the round's inputs: the case's sessions as
// fleetInputsInto hands them over (quiet and directory-hidden ones left out, muted by the owner's mutes) and its cards.
// Its counted rows are the badge's rows, and totals repeat its number.
func TestCountsViewFollowsTheSharedFixtures(t *testing.T) {
	for _, c := range readNeedsYouCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			s := newFleetTestNoStore(t).s
			setMutes(s, c.Input.Mutes)
			var agents []fleetInput
			for i := range c.Input.Sessions {
				sess := &c.Input.Sessions[i]
				sess.Quiet = slices.Contains(c.Input.Quiet, sess.MachineID+"/"+sess.SessionID)
				if fleetcountadapt.Visibility(sess).LeftOut() { // as fleetInputsInto
					continue
				}
				agents = append(agents, fleetInput{Machine: "Mac", Session: sess,
					Muted: s.mutedAs(sess.MachineID, sess.SessionID, sess.SpaceName)})
			}
			var r fleetRound
			_, v := r.linkAndCounts("en", agents, c.Input.Interactions, nil, nil, opt.None[assistantwire.TodoCounts]())
			count := v.Counts.AgentsNeedsYou
			if count == nil || v.Totals == nil || v.Totals.AgentsNeedsYou == nil {
				t.Fatalf("no agents_needs_you: %+v", v)
			}
			var rows, want []string
			for _, k := range count.Keys {
				row := string(k.ID)
				for _, card := range k.Cards {
					row += " " + string(card)
				}
				rows = append(rows, row)
			}
			for _, k := range c.Expected.BadgeRows {
				row := k.ID
				for _, card := range k.Cards {
					row += " " + card
				}
				want = append(want, row)
			}
			if count.N != c.Expected.Badge || *v.Totals.AgentsNeedsYou != count.N || !slices.Equal(rows, want) {
				t.Fatalf("n %d (totals %d), rows %q; want the badge %d, rows %q", count.N, *v.Totals.AgentsNeedsYou,
					rows, c.Expected.Badge, want)
			}
		})
	}
}

// countsRound is one of liveLoop's rounds over s's fleet, the Live Activity left out: the summary and the counts view
// made, with the to-dos as the manager last counted them, and the view told.
func countsRound(s *Service, r *fleetRound) {
	var quiet []fleetInput
	agents, ixs, told := s.fleetInputsForRound(nil, &quiet)
	_, v := r.linkAndCounts("en", agents, ixs, told, quiet, s.fleet.live.counts.todos.kept())
	s.tellCounts(v)
}

// countsTold is every counts view told the hub so far, decoded as the hub's relay will read it.
func countsTold(t *testing.T, ft *fleetTest) ([]assistantwire.CountsView, []json.RawMessage) {
	t.Helper()
	raw := ft.toldOf(assistantwire.EventCountsView)
	var out []assistantwire.CountsView
	for _, b := range raw {
		var v assistantwire.CountsView
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err := d.Decode(&v); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		out = append(out, v)
	}
	return out, raw
}

// The manager tells its view when its counts change: a new rev in this run's epoch, the build that made it and when.
// A round that changes nothing tells nothing; after a handshake the last view is told again as it was.
func TestTheCountsViewIsToldWhenItChanges(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	s.build = "1.2.3"
	at := time.Now().Add(-time.Hour).UTC()
	waiting := func(ids ...string) { // each session waits on a card of its own
		s.fleet.mu.Lock()
		clear(s.ixMapLocked())
		s.fleet.mu.Unlock()
		var list []proto.Session
		for _, id := range ids {
			list = append(list, proto.Session{SessionID: id, Cwd: "/home/user/project", Status: proto.StatusIdle, UpdatedAt: at})
			putPending(s, &pending{ix: proto.Interaction{ID: "ix-" + id, MachineID: "mac", SessionID: id,
				Kind: proto.IxPermission, ToolName: "Bash", Summary: "npm test", CreatedAt: at}})
		}
		ft.d.setSessions("mac", list)
	}
	var r fleetRound
	waiting("s-1")
	before := time.Now().UnixMilli()
	countsRound(s, &r)
	views, _ := countsTold(t, ft)
	if len(views) != 1 {
		t.Fatalf("told %d views, want 1", len(views))
	}
	first := views[0]
	if first.Epoch == "" || first.Rev != 1 || first.AsOf < before || first.Build != "1.2.3" ||
		first.Counts.AgentsNeedsYou == nil || first.Counts.AgentsNeedsYou.N != 1 ||
		len(first.Counts.AgentsNeedsYou.Keys) != 1 || first.Counts.AgentsNeedsYou.Keys[0].ID != "agent:mac/s-1" ||
		!slices.Equal(first.Counts.AgentsNeedsYou.Keys[0].Cards, []assistantwire.CountsCardID{"ix-s-1"}) ||
		*first.Totals.AgentsNeedsYou != 1 {
		_, raw := countsTold(t, ft)
		t.Fatalf("the first view: %s", raw[0])
	}
	countsRound(s, &r)
	countsRound(s, &r)
	if views, _ := countsTold(t, ft); len(views) != 1 {
		t.Fatalf("rounds that changed nothing told %d views", len(views)-1)
	}
	s.liveChanged(true) // a handshake: the hub may have restarted
	countsRound(s, &r)
	views, raw := countsTold(t, ft)
	if len(views) != 2 || !bytes.Equal(raw[0], raw[1]) {
		t.Fatalf("after a handshake: %d views, the last %s", len(views), raw[len(raw)-1])
	}
	waiting("s-1", "s-2")
	countsRound(s, &r)
	views, _ = countsTold(t, ft)
	last := views[len(views)-1]
	if len(views) != 3 || last.Epoch != first.Epoch || last.Rev != 2 || last.Counts.AgentsNeedsYou.N != 2 ||
		*last.Totals.AgentsNeedsYou != 2 {
		t.Fatalf("after a change: %d views, the last %+v", len(views), last)
	}
	waiting()
	countsRound(s, &r)
	views, raw = countsTold(t, ft)
	if len(views) != 4 || views[3].Rev != 3 || views[3].Counts.AgentsNeedsYou == nil || views[3].Counts.AgentsNeedsYou.N != 0 ||
		*views[3].Totals.AgentsNeedsYou != 0 {
		t.Fatalf("nothing waiting is told as 0, never left out: %s", raw[len(raw)-1])
	}
}

// liveLoop tells the view in its round, after the Live Activity's state.
func TestLiveLoopTellsTheCountsView(t *testing.T) {
	ft := newFleetTestNoStore(t)
	at := time.Now().Add(-time.Hour).UTC()
	ft.d.setSessions("mac", []proto.Session{{SessionID: "s-1", Cwd: "/home/user/project", Status: proto.StatusIdle,
		State: proto.StateNeedsYou, StateSince: at, UpdatedAt: at}})
	startAttention(t, ft.s)
	ft.s.liveChanged(true)
	waitUntil(t, func() bool { return len(ft.toldOf(assistantwire.EventCountsView)) > 0 })
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	live, counts := -1, -1
	for i, told := range ft.f.told {
		switch told.event {
		case assistantwire.EventFleetLive:
			live = i
		case assistantwire.EventCountsView:
			if counts < 0 {
				counts = i
			}
		}
	}
	if live < 0 || live > counts {
		t.Fatalf("the Live Activity's state at %d, the counts view at %d: the view goes after the state", live, counts)
	}
}

// The manager names the capability that tells clients its views can be drawn.
func TestTheHelloNamesTheCountsView(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://localhost:1", Token: "t"})
	p, _ := b.client.HelloParams().(map[string]any)
	caps, _ := p["capabilities"].([]string)
	if !slices.Contains(caps, assistantwire.CapabilityCountsView) {
		t.Fatalf("the hello does not name %s: %v", assistantwire.CapabilityCountsView, caps)
	}
}

// todoCountsFixtures are the to-do list's counts (spec/fixtures/todo-counts/README.md), which todos_needs_you is.
const todoCountsFixtures = "../../../spec/fixtures/todo-counts/todo_counts.json"

// On every todo-counts case the view's todos_needs_you is the needs_you of the counts GET /api/v1/todos answers for
// that list, the projection's Counts() (todoProjection.counts) of the list a read worked out: n, totals repeating it,
// and no row listed, so all of n is more.
func TestCountsViewCountsTheToDosAsTheListDoes(t *testing.T) {
	b, err := os.ReadFile(todoCountsFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name  string `json:"name"`
			Input struct {
				Now          time.Time            `json:"now"`
				Todos        []json.RawMessage    `json:"todos"`
				Sessions     []proto.Session      `json:"sessions"`
				RestoredAt   map[string]time.Time `json:"restored_at"`
				SomedayUntil map[string]time.Time `json:"someday_until"`
				Loc          string               `json:"loc"`
			} `json:"input"`
			Expected struct {
				Counts assistantwire.TodoCounts `json:"counts"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil || len(file.Cases) == 0 {
		t.Fatalf("%s: %v (%d cases)", todoCountsFixtures, err, len(file.Cases))
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var loc *time.Location
			if c.Input.Loc != "" {
				var err error
				if loc, err = time.LoadLocation(c.Input.Loc); err != nil {
					t.Fatal(err)
				}
			}
			facts := make([]projection.Facts, 0, len(c.Input.Todos))
			for _, raw := range c.Input.Todos { // the rows as GET /api/v1/todos lists them, with their marks
				var f projection.Facts
				if err := json.Unmarshal(raw, &f); err != nil {
					t.Fatal(err)
				}
				f.Sessions, f.Loc = c.Input.Sessions, loc
				if at, ok := c.Input.RestoredAt[f.ID]; ok {
					f.RestoredAt = &at
				}
				if at, ok := c.Input.SomedayUntil[f.ID]; ok {
					f.SomedayUntil = &at
				}
				facts = append(facts, f)
			}
			p := todoProjection{facts: facts, views: projection.Views(facts, c.Input.Now)}
			want := c.Expected.Counts.NeedsYou
			if listed := p.counts(); listed.NeedsYou != want {
				t.Fatalf("the list's counts say needs_you %d, the case %d", listed.NeedsYou, want)
			}
			s := newFleetTestNoStore(t).s
			s.todosCounted(s.todoViewGen(), p)
			var r fleetRound
			_, v := r.linkAndCounts("en", nil, nil, nil, nil, s.fleet.live.counts.todos.kept())
			got := v.Counts.TodosNeedsYou
			if got == nil || v.Totals == nil || v.Totals.TodosNeedsYou == nil || got.N != want ||
				*v.Totals.TodosNeedsYou != want || got.More != want || got.Keys != nil || got.Held != nil {
				out, _ := json.Marshal(v)
				t.Fatalf("view %s; want todos_needs_you %d, all of it more", out, want)
			}
		})
	}
}

// todosClock holds what the to-dos' timers scheduled (the {"type":"todos"} pushes, the counts view's recounts and the
// views' own checks) until the test runs it.
type todosClock struct {
	mu    sync.Mutex
	until map[string][]func()
}

func (c *todosClock) on(kind string) func(time.Duration, func()) {
	return func(_ time.Duration, f func()) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.until[kind] = append(c.until[kind], f)
	}
}

// run runs what kind scheduled so far, and says how many.
func (c *todosClock) run(kind string) int {
	c.mu.Lock()
	due := c.until[kind]
	c.until[kind] = nil
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
	return len(due)
}

// waiting says how many of kind's are scheduled and not run yet.
func (c *todosClock) waiting(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.until[kind])
}

// The view's to-dos follow the list GET /api/v1/todos answers: absent until the manager has counted them, then the
// list's counts.needs_you. A read of the list keeps the number; once a {"type":"todos"} went out that no read
// followed, the manager reads the list itself when todosCountSettle passed, and a recount after a read since does not.
// The view goes up one rev when the number moves, and a round that moves nothing tells nothing.
func TestCountsViewFollowsTheToDoList(t *testing.T) {
	f := newFakeHub().withMachine()
	d := newFleetDevs()
	d.add("mac", "Mac")
	s := withSettings(t, f, New(Options{Hub: f, Logf: testLogf(t), Devices: d}))
	e := &todoEnv{t: t, s: s, f: f}
	ft := &fleetTest{t: t, s: s, d: d, f: f}
	clock := &todosClock{until: map[string][]func(){}}
	s.tpush.after, s.fleet.live.counts.todos.after, s.tview.after = clock.on("push"), clock.on("recount"), clock.on("view")
	ctx := context.Background()
	listed := func() int {
		var list struct{ Counts assistantwire.TodoCounts }
		if err := json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		return list.Counts.NeedsYou
	}
	var r fleetRound
	last := func(views int) assistantwire.CountsView {
		t.Helper()
		countsRound(s, &r)
		told, raw := countsTold(t, ft)
		if len(told) != views {
			t.Fatalf("told %d views, want %d; the last %s", len(told), views, raw[len(raw)-1])
		}
		return told[len(told)-1]
	}
	needsYou := func(v assistantwire.CountsView) int {
		t.Helper()
		c := v.Counts.TodosNeedsYou
		if c == nil || v.Totals == nil || v.Totals.TodosNeedsYou == nil || *v.Totals.TodosNeedsYou != c.N ||
			c.More != c.N || c.Keys != nil {
			t.Fatalf("todos_needs_you %+v, totals %+v", c, v.Totals)
		}
		return c.N
	}

	// Nothing read yet: the to-dos' count is unknown, so absent with its total, never 0.
	if v := last(1); v.Counts.TodosNeedsYou != nil || v.Totals.TodosNeedsYou != nil {
		t.Fatalf("before the to-dos are counted: %+v, totals %+v", v.Counts.TodosNeedsYou, v.Totals)
	}
	failed := e.create("Book the ferry")
	if err := s.failTodo(ctx, failed.ID, "The computer is offline"); err != nil {
		t.Fatal(err)
	}
	first := listed()
	if first == 0 {
		t.Fatal("a failed start does not need the owner: the test needs a to-do that does")
	}
	v := last(2)
	if needsYou(v) != first || v.Rev != 2 {
		t.Fatalf("after a read: todos_needs_you %d at rev %d, want the list's %d at rev 2", needsYou(v), v.Rev, first)
	}
	epoch := v.Epoch

	// The run's first view asked for a recount, but the read since counted the list: it reads nothing, so another
	// failed start, whose {"type":"todos"} has not gone out yet, is not counted.
	other := e.create("Renew the passport")
	if err := s.failTodo(ctx, other.ID, "The computer is offline"); err != nil {
		t.Fatal(err)
	}
	if n := clock.run("recount"); n != 1 {
		t.Fatalf("%d recounts were waiting, want the first view's", n)
	}
	last(2)

	// The {"type":"todos"} goes out and no app reads the list: the manager does, once todosCountSettle passed.
	if clock.run("push") == 0 {
		t.Fatal("no {\"type\":\"todos\"} was waiting")
	}
	last(2)
	if n := clock.run("recount"); n != 1 {
		t.Fatalf("%d recounts were waiting after the push, want 1", n)
	}
	v = last(3)
	if needsYou(v) != first+1 || v.Rev != 3 || v.Epoch != epoch {
		t.Fatalf("after the recount: todos_needs_you %d at rev %d, want %d at rev 3 of the same epoch", needsYou(v), v.Rev, first+1)
	}
	if n := listed(); n != needsYou(v) {
		t.Fatalf("GET /api/v1/todos says needs_you %d, the view %d", n, needsYou(v))
	}
	last(3) // the read since changed nothing
}

// todosSlowStore holds the first read of the decision reviews' dismissals, which every whole-list read loads, until
// release is closed: a store slow enough that a recount's read is still running when the next rounds come.
type todosSlowStore struct {
	*fleetMem
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *todosSlowStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == taskReviewDismissalsKey {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.fleetMem.Setting(ctx, key, out)
}

// The manager reads its to-do list one read at a time. While a recount's read waits on a slow store, the rounds that
// still have no count and a {"type":"todos"} that goes out start no second read; once it ends, the push has the
// manager read the list once more, and that read, with nothing pushed since, arms nothing.
func TestCountsViewRecountsOneReadAtATime(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	slow := &todosSlowStore{fleetMem: ft.st, entered: make(chan struct{}), release: make(chan struct{})}
	s.setStore(func() Store { return slow })
	clock := &todosClock{until: map[string][]func(){}}
	s.tpush.after, s.fleet.live.counts.todos.after, s.tview.after = clock.on("push"), clock.on("recount"), clock.on("view")
	var r fleetRound

	countsRound(s, &r) // nothing counted yet: the round asks for a count
	if n := clock.waiting("recount"); n != 1 {
		t.Fatalf("%d recounts after the first round, want 1", n)
	}
	read := make(chan struct{})
	go func() {
		defer close(read)
		clock.run("recount")
	}()
	select {
	case <-slow.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the recount did not read the list")
	}

	for range 3 {
		countsRound(s, &r) // still no count: each round asks
	}
	s.todosPushSoon()
	if clock.run("push") != 1 {
		t.Fatal("no {\"type\":\"todos\"} was waiting")
	}
	if n := clock.waiting("recount"); n != 0 {
		t.Fatalf("%d recounts were armed while the first read was still running, want none", n)
	}

	close(slow.release)
	select {
	case <-read:
	case <-time.After(10 * time.Second):
		t.Fatal("the recount's read did not end")
	}
	if n := clock.waiting("recount"); n != 1 {
		t.Fatalf("%d recounts after the read the push went out during, want 1", n)
	}
	if n := clock.run("recount"); n != 1 {
		t.Fatalf("ran %d recounts, want 1", n)
	}
	if n := clock.waiting("recount"); n != 0 {
		t.Fatalf("%d recounts after a read nothing was pushed during, want none", n)
	}
	countsRound(s, &r)
	if n := clock.waiting("recount"); n != 0 {
		t.Fatalf("a round with the to-dos counted asked for %d recounts", n)
	}
	told, raw := countsTold(t, ft)
	if v := told[len(told)-1]; v.Counts.TodosNeedsYou == nil || v.Counts.TodosNeedsYou.N != 0 {
		t.Fatalf("the last view %s; want the empty list's todos_needs_you 0", raw[len(raw)-1])
	}
}
