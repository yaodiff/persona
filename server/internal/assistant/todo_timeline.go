package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/todo/timeline"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// A to-do's timeline (#10465, docs/assistant/todo-status-sop.md "The to-do's timeline"): its conversation grouped into
// rounds, its results and what changed since the owner looked, worked out by the pure todo/timeline (its rules:
// spec/fixtures/todo-rounds) from the stores the manager already keeps. This file is the adapter: once per read it
// reads the to-do as GET /api/v1/todos/{id} lists it, the feed's items about it, its decisions, its runs' sessions and
// the owner's zone, and hands them over as timeline.Input. GET /api/v1/todos/{id}/timeline pages through the rounds;
// GET /api/v1/todos/{id} carries the ones the paper shows (todo_view_routes.go). Neither has a push of its own:
// {"type":"todos"} comes on a decision, a supervision or a scope change, and assistant_feed with every feed item,
// which clients match to the to-do by its ref. The manager names CapabilityTodoTimeline in its hello.
//
// An extension's token (todos.read) reads less (docs/extensions/extensions.md "Never shown to extensions"): nothing of
// a to-do whose history may hold a private space's words, whose routes answer it 404 (todoScoped, todo_scope.go), and
// no answered cards, whose records hold what the owner typed into a card's fields and do not say whose card it was (a
// Just me card, a space run's).

const (
	// todoTimelineFeedCap is how many of a to-do's feed items a read takes, the newest: a to-do has a few hundred at
	// most, and the cap keeps a runaway one's read bounded. A to-do past it serves the rounds that began within them
	// (windowTimeline).
	todoTimelineFeedCap = 1000
	// todoTimelinePage is how many rounds GET …/timeline answers when it is not told, todoTimelineMaxPage at most.
	todoTimelinePage    = 20
	todoTimelineMaxPage = 100
)

func (s *Service) todoTimelineRoutes() {
	// GET /api/v1/todos/{id}/timeline?before=<round id>&limit=<n>: the to-do's conversation → assistantwire.TodoTimeline,
	// its latest limit rounds (todoTimelinePage when not given, at most todoTimelineMaxPage) before the round named
	// before (none: up to the latest), oldest first, with more while earlier ones are left. An unknown id 404
	// todo_not_found, and so does a to-do an extension is not shown (todoScoped); a before that names no round (one
	// read from rows the stores no longer keep, or never one) 409 round_not_found, and the client reads the latest page
	// again; the to-dos or their history unreadable 503.
	s.handle("GET /api/v1/todos/{id}/timeline", func(w http.ResponseWriter, r *http.Request) {
		ctx, id, q := r.Context(), r.PathValue("id"), r.URL.Query()
		ext := r.Header.Get(RoleHeader) == roleExtension
		list, err := s.listedTodosOf(ctx, false, map[string]bool{id: true}) // loads the to-dos first
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		if len(list) == 0 {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		sessions := s.todoFactsOf(ctx, list)[0].Sessions
		list[0].Todo = s.withBrief(list[0].Todo) // as the item route lists it
		raw, err := json.Marshal(list[0])
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		d, err := s.todoTimelineOf(ctx, raw, sessions, ext)
		if err != nil {
			s.logf("todo %s: timeline: %v", id, err)
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = todoTimelinePage
		}
		rounds, more, ok := timelinePage(d.Rounds, q.Get("before"), min(limit, todoTimelineMaxPage))
		if !ok {
			apierr.WriteCode(w, http.StatusConflict, "round_not_found", i18n.Err("assistant-todos.round_not_found"))
			return
		}
		apierr.WriteJSON(w, assistantwire.TodoTimeline{Rounds: rounds, More: more})
	})
}

// supervisorOf is the conversation that follows to-do t up ("" none).
func supervisorOf(t Todo) string {
	if t.Supervision == nil {
		return ""
	}
	return t.Supervision.Supervisor
}

// conversationSpace is the space of the conversation a to-do names, id ("" the assistant's own, as for no
// conversation), and whether it could be told, as conversationSpaces tells it.
func (s *Service) conversationSpace(ctx context.Context, id string) (string, bool) {
	t := s.conversationSpaces(ctx, []string{id})[id]
	return t.space, t.known
}

// leaderSpace is the space of task leader agent machine/sid, and whether it could be told: a space's when any of its
// links keeps a directory there (identity.SpaceAt: a subfolder counts, this only hides), else outside any when one
// keeps a directory, else as its computer lists it (runSpace).
func (s *Service) leaderSpace(ctx context.Context, machine, sid string) (string, bool) {
	s.todos.mu.RLock()
	space, kept := "", false
	for _, t := range s.todos.todos {
		for _, a := range t.Agents {
			if a.MachineID == machine && a.SessionID == sid && a.Cwd != "" {
				if space == "" {
					space = identity.SpaceAt(a.Cwd)
				}
				kept = true
			}
		}
	}
	s.todos.mu.RUnlock()
	if space != "" {
		return space, true
	} else if kept {
		return "", true
	}
	list, err := s.todoRunSessions(ctx, machine, map[string]bool{sid: true})
	if err != nil {
		return "", false
	}
	for _, c := range list {
		if c.SessionID == sid {
			return runSpace(c), true
		}
	}
	return "", false
}

// timelinePage is the page GET …/timeline answers of rounds (oldest first): the latest limit before the round whose id
// is before ("": up to the latest), in the same order, and whether earlier ones are left; ok false when no round has
// that id. The page is never nil, so an empty one writes [].
func timelinePage(rounds []assistantwire.TodoRound, before string, limit int) (page []assistantwire.TodoRound, more, ok bool) {
	end := len(rounds)
	if before != "" {
		if end = slices.IndexFunc(rounds, func(r assistantwire.TodoRound) bool { return r.ID == before }); end < 0 {
			return nil, false, false
		}
	}
	start := max(0, end-limit)
	return append([]assistantwire.TodoRound{}, rounds[start:end]...), start > 0, true
}

// todoItemDetail is what GET /api/v1/todos/{id} answers beside to-do t's row raw, whose active runs' sessions are
// sessions (assistantwire.TodoItemDetail): the rounds the paper shows (timeline.Latest), how many there are in all, its
// results and what changed since the owner looked; for an extension (ext) as todoTimelineOf reads it. Nothing when its
// history cannot be read: the row still answers, and the paper draws what it has as from an older manager.
func (s *Service) todoItemDetail(ctx context.Context, t Todo, raw json.RawMessage, sessions []proto.Session, ext bool) assistantwire.TodoItemDetail {
	d, err := s.todoTimelineOf(ctx, raw, sessions, ext)
	if err != nil {
		s.logf("todo %s: timeline: %v", t.ID, err)
		return assistantwire.TodoItemDetail{}
	}
	total, rounds := len(d.Rounds), timeline.Latest(d.Rounds, timeline.PaperRounds)
	if rounds == nil {
		rounds = []assistantwire.TodoRound{} // a to-do with no rounds yet: [], told apart from an older manager's none
	}
	return assistantwire.TodoItemDetail{Rounds: rounds, RoundsTotal: &total, Recap: d.Recap, Results: d.Results}
}

// todoTimelineOf works out the timeline of the to-do whose row is raw, as GET /api/v1/todos/{id} lists it (with its
// brief and lineage; its view, when there, is not read), with sessions its active runs' sessions as its view reads them
// (todoFactsOf). For an extension (ext) it leaves out the answered cards; a to-do it is not shown never gets here
// (todoScoped). The row is read from its JSON, as the shared cases give it, so the timeline sees exactly what the apps
// do: an open ask's id is formed from asked_at as listed, as the view forms it. An error when the feed's items about it
// or its decisions cannot be read.
func (s *Service) todoTimelineOf(ctx context.Context, raw json.RawMessage, sessions []proto.Session, ext bool) (d timeline.Derived, err error) {
	in := timeline.Input{Sessions: sessions, Loc: s.location(ctx)}
	if err := json.Unmarshal(raw, &in.Todo); err != nil {
		return timeline.Derived{}, err
	}
	if in.Feed, err = s.todoFeed(ctx, in.Todo.ID, ext); err != nil {
		return timeline.Derived{}, err
	}
	if in.Decisions, err = s.todoTimelineDecisions(ctx, in.Todo.ID); err != nil {
		return timeline.Derived{}, err
	}
	in.Machines = s.todoMachineNames(ctx, in.Feed)
	if len(in.Feed) < todoTimelineFeedCap {
		return timeline.Derive(in), nil
	}
	return windowTimeline(in), nil
}

// windowTimeline is the timeline of a to-do whose feed items in were cut at todoTimelineFeedCap: the rounds that began
// at or after the oldest item kept, worked out only from what is no older than that item (its decisions, brief log and
// ledger reviews are cut at the same time as its feed), so that every round served is whole and a round read again
// after newer items is the same round. Earlier rounds are not served, so rounds_total and more count from there, and
// a recap that began in a round left out starts at the first one served.
func windowTimeline(in timeline.Input) timeline.Derived {
	cut := in.Feed[0].At
	for _, f := range in.Feed {
		if f.At.Before(cut) {
			cut = f.At
		}
	}
	in.Decisions = slices.DeleteFunc(in.Decisions, func(d timeline.Decision) bool { return d.CreatedAt.Before(cut) })
	if b := in.Todo.Brief; b != nil {
		b.Log = slices.DeleteFunc(b.Log, func(e timeline.BriefEvent) bool { return e.At.Before(cut) })
	}
	if v := in.Todo.Supervision; v != nil {
		v.Reviews = slices.DeleteFunc(v.Reviews, func(r timeline.Review) bool { return r.At.Before(cut) })
	}
	d := timeline.Derive(in)
	d.Rounds = slices.DeleteFunc(d.Rounds, func(r assistantwire.TodoRound) bool { return r.At.Before(cut) })
	if d.Recap != nil && !slices.ContainsFunc(d.Rounds, func(r assistantwire.TodoRound) bool { return r.ID == d.Recap.Round }) {
		if len(d.Rounds) == 0 {
			d.Recap = nil
		} else {
			d.Recap.Round = d.Rounds[0].ID
		}
	}
	return d
}

// todoFeed are the feed's items about to-do id of the kinds the timeline reads, in every conversation (the origin's,
// a supervisor's, "" for a hand-written to-do's relays), the newest todoTimelineFeedCap of them, newest first: from
// the store (FeedByRef), else from memory. For an extension (ext) without the answered cards.
func (s *Service) todoFeed(ctx context.Context, id string, ext bool) ([]timeline.FeedItem, error) {
	kinds := timeline.FeedKinds()
	if ext {
		kinds = slices.DeleteFunc(kinds, func(k string) bool { return k == feedAnswered })
	}
	var items []FeedItem
	if st := s.store(); st != nil {
		rows, err := st.FeedByRef(ctx, id, kinds, todoTimelineFeedCap)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			items = append(items, FeedItem{ID: r.ID, Conversation: r.Conversation, At: r.At, Kind: r.Kind, Ref: cmp.Or(r.Ref, id), Body: r.Body})
		}
	} else {
		s.tasks.mu.Lock()
		for _, list := range s.tasks.feed {
			for _, it := range list {
				if it.Ref == id && slices.Contains(kinds, it.Kind) {
					items = append(items, it)
				}
			}
		}
		s.tasks.mu.Unlock()
		slices.SortFunc(items, func(a, b FeedItem) int { return cmp.Or(b.At.Compare(a.At), strings.Compare(b.ID, a.ID)) })
		items = items[:min(len(items), todoTimelineFeedCap)]
	}
	out := make([]timeline.FeedItem, 0, len(items))
	for _, it := range items {
		if readTaskNotice(it).Kind != feedNotice {
			out = append(out, timeline.FeedItem{ID: it.ID, At: it.At, Kind: it.Kind, Body: it.Body})
		}
	}
	return out, nil
}

// todoTimelineDecisions are to-do id's decisions, superseded ones included; none without the hub's database, which
// alone keeps them.
func (s *Service) todoTimelineDecisions(ctx context.Context, id string) ([]timeline.Decision, error) {
	st := s.todoDB()
	if st == nil {
		return nil, nil
	}
	rows, err := st.ListTodoDecisions(ctx, id, true)
	if err != nil {
		return nil, err
	}
	out := make([]timeline.Decision, len(rows))
	for i, d := range rows {
		out[i] = timeline.Decision{ID: d.ID, Question: d.Question, Choice: d.Choice, Reason: d.Reason, Source: d.Source,
			SourceMachine: d.SourceMachine, SourceSession: d.SourceSession, SourceMessage: d.SourceMessage,
			SupersededBy: d.SupersededBy, CreatedAt: d.CreatedAt}
	}
	return out, nil
}

// todoMachineNames are the computers' names by id, which a run's receipt written before #10465 names its computer by
// (as the hub names it, finishTurn); nil when every turn's receipt in feed names its run, as the timeline reads it
// (both ids), so only an older history needs them. From the fleet this process carries, else asked of the hub.
func (s *Service) todoMachineNames(ctx context.Context, feed []timeline.FeedItem) map[string]string {
	older := slices.ContainsFunc(feed, func(f timeline.FeedItem) bool {
		var b struct {
			State     string `json:"state"`
			MachineID string `json:"machine_id"`
			SessionID string `json:"session_id"`
		}
		return f.Kind == feedReceipt && json.Unmarshal(f.Body, &b) == nil && b.State != "failed" &&
			(b.MachineID == "" || b.SessionID == "")
	})
	if !older {
		return nil
	}
	names := map[string]string{}
	if s.fleetOn() {
		for _, m := range s.fleetMachines() {
			names[m.ID] = m.Name
		}
		return names
	}
	for _, d := range s.devices(ctx) {
		names[d.ID] = d.Name
	}
	return names
}
