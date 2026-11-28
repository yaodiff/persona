package hub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The To-dos view (#10226, docs/assistant/todo-status-sop.md "The hub's view") is the managers': each works out its own
// to-dos' views, counts and places. The hub relays them and joins several managers' answers, adding their counts up
// field by field and key by key; it never works out a phase itself (docs/server/architecture.md "The hub is only a
// communication center"). Clients order a merged list by rank, then manager, then id: the hub does not rank again.

const (
	// featureTodoView: every row of GET /api/v1/todos carries `view`, the list `counts`, and GET /api/v1/todos/{id}
	// answers one row so (docs/protocol/protocol.md, the to-dos rows).
	featureTodoView = assistantwire.CapabilityTodoView
	// featureTodoRevision: GET /api/v1/todos/revision answers the pushed read's cheap merged validator.
	featureTodoRevision = assistantwire.CapabilityTodoRevision
	// featureTodoPlaces: GET /api/v1/todos/places answers the To-dos sidebar's places with the counts.
	featureTodoPlaces = assistantwire.CapabilityTodoPlaces
	// featureTodoAsks: POST /api/v1/todos/{id}/asks/{ask}/answer answers a to-do's own asks by their view's ids
	// (#10465), with a receipt under X-Persona-Op-Id (todo_receipts.go).
	featureTodoAsks = assistantwire.CapabilityTodoAsks
	// featureTodoTimeline: GET /api/v1/todos/{id}/timeline answers a to-do's conversation, and GET /api/v1/todos/{id}
	// carries the rounds the paper shows, its results and what changed since the owner looked (#10465).
	featureTodoTimeline = assistantwire.CapabilityTodoTimeline
	// featureTodoImages: POST /api/v1/todos persists images and serves their private bytes (#11084). A hub-wide flag
	// cannot promise image creation for only some managers, so an older or offline one hides the picker.
	featureTodoImages = assistantwire.CapabilityTodoImages
	// todoRevisionRoute is the pushed read's cheap validator, answered by every manager and merged (mergeTodoRevision).
	todoRevisionRoute = "GET /api/v1/todos/revision"
	// todoPlacesRoute is answered by every manager the to-dos list merges, joined (mergeTodoPlaces).
	todoPlacesRoute = "GET /api/v1/todos/places"
)

// todoViewFeatures are todo_view and todo_places, each while every manager whose to-dos the list merges (listManagers)
// is connected and names it in its hello: a hub-wide feature cannot promise the view for part of the list, so an older
// or offline manager hides it until it is ready, and clients keep their own rules until then.
func (h *Hub) todoViewFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	view, places := true, true
	for _, id := range h.listManagers(ctx) {
		view = view && h.archiveManagerHas(id, assistantwire.CapabilityTodoView) == http.StatusOK
		places = places && h.archiveManagerHas(id, assistantwire.CapabilityTodoPlaces) == http.StatusOK
	}
	switch {
	case view && places:
		return []string{featureTodoView, featureTodoPlaces}
	case view:
		return []string{featureTodoView}
	case places:
		return []string{featureTodoPlaces}
	}
	return nil
}

// todoRevisionFeatures promises the cheap merged read only when every manager has it. Otherwise the web sends no extra
// probe at all and keeps the original full-list refresh throughout a rolling upgrade.
func (h *Hub) todoRevisionFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, id := range h.listManagers(ctx) {
		if h.archiveManagerHas(id, assistantwire.CapabilityTodoRevision) != http.StatusOK {
			return nil
		}
	}
	return []string{featureTodoRevision}
}

// todoAsksFeatures is todo_asks while every manager whose to-dos the list merges is connected and names it, as
// todoViewFeatures lists the view's: an answer goes to the manager that holds the to-do, and a client that answers
// through the route before every manager has it would get 404 for the others' asks.
func (h *Hub) todoAsksFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, id := range h.listManagers(ctx) {
		if h.archiveManagerHas(id, assistantwire.CapabilityTodoAsks) != http.StatusOK {
			return nil
		}
	}
	return []string{featureTodoAsks}
}

// todoTimelineFeatures is todo_timeline while every manager whose to-dos the list merges is connected and names it, as
// todoViewFeatures has todo_view: a hub-wide feature cannot promise a to-do's history for part of the list, so an older
// or offline manager hides it until it is ready, and clients draw no conversation, results or recap until then.
func (h *Hub) todoTimelineFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, id := range h.listManagers(ctx) {
		if h.archiveManagerHas(id, assistantwire.CapabilityTodoTimeline) != http.StatusOK {
			return nil
		}
	}
	return []string{featureTodoTimeline}
}

// todoImageFeatures is todo_images only while every manager whose to-dos the list merges is connected and names it.
// An older manager would ignore the additive images field and create an image-less to-do, so the Web offers no picker
// in that case. It does not gate reading metadata or bytes that a capable manager already stored.
func (h *Hub) todoImageFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, id := range h.listManagers(ctx) {
		if h.archiveManagerHas(id, assistantwire.CapabilityTodoImages) != http.StatusOK {
			return nil
		}
	}
	return []string{featureTodoImages}
}

// todoListSums are the members of a merged to-dos list the hub adds up over its answers (mergeList, before the list's
// validator is taken): counts, closed cursors and the optional todos_v2 board and shared blockers.
func todoListSums(answers []listAnswer) map[string]json.RawMessage {
	var all []json.RawMessage
	var blockers []json.RawMessage
	var boards []json.RawMessage
	for i := range answers {
		if raw, ok := mergedMember(&answers[i], "todos", "counts"); ok {
			all = append(all, raw)
		}
		if raw, ok := mergedMember(&answers[i], "todos", "shared_blockers"); ok {
			blockers = append(blockers, raw)
		}
		if raw, ok := mergedMember(&answers[i], "todos", "board"); ok && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
			boards = append(boards, raw)
		}
	}
	var out map[string]json.RawMessage
	if sum, ok := sumTodoCounts(all); ok {
		out = map[string]json.RawMessage{"counts": sum}
	}
	if merged, ok := mergeSharedBlockers(blockers); ok {
		if out == nil {
			out = map[string]json.RawMessage{}
		}
		out["shared_blockers"] = merged
	}
	if merged, ok := mergeTodoBoards(boards); ok {
		if out == nil {
			out = map[string]json.RawMessage{}
		}
		out["board"] = merged
	}
	if cursors := todoClosedCursors(answers); len(cursors) > 0 {
		if out == nil {
			out = map[string]json.RawMessage{}
		}
		raw, _ := json.Marshal(cursors)
		out["closed_cursors"] = raw
	}
	return out
}

// mergeTodoBoards joins board objects in the list's manager order (default first, then by manager ID).
// The managers supply the membership, counts and order within their own boards.
func mergeTodoBoards(all []json.RawMessage) (json.RawMessage, bool) {
	switch len(all) {
	case 0:
		return nil, false
	case 1:
		return all[0], true
	}
	boards := make([]map[string]json.RawMessage, 0, len(all))
	out := map[string]json.RawMessage{}
	var counts []json.RawMessage
	sidebar := []json.RawMessage{}
	for _, raw := range all {
		var board map[string]json.RawMessage
		if json.Unmarshal(raw, &board) != nil {
			continue
		}
		boards = append(boards, board)
		for key, value := range board {
			if _, has := out[key]; !has {
				out[key] = value
			}
		}
		counts = append(counts, board["counts"])
		var nodes []json.RawMessage
		if json.Unmarshal(board["sidebar_tree"], &nodes) == nil {
			sidebar = append(sidebar, nodes...)
		}
	}
	out["sidebar_tree"], _ = json.Marshal(sidebar)
	out["status_groups"] = mergeTodoBoardGroups(boards, "status_groups", "key", "entries", "total_count", "visible_count")
	out["direction_groups"] = mergeTodoBoardGroups(boards, "direction_groups", "direction_id", "todo_ids", "counts")
	if sum, ok := sumTodoCounts(counts); ok {
		out["counts"] = sum
	}
	raw, err := json.Marshal(out)
	return raw, err == nil
}

// mergeTodoBoardGroups joins records with the same group key. Their first appearance sets the order, with the
// ungrouped direction last. Members stay in manager order and counts are added as sent.
func mergeTodoBoardGroups(boards []map[string]json.RawMessage, boardKey, groupKey, listKey string, countKeys ...string) json.RawMessage {
	type group struct {
		fields map[string]json.RawMessage
		items  []json.RawMessage
		counts []map[string]json.RawMessage
	}
	groups := map[string]*group{}
	var order []string
	for _, board := range boards {
		var rows []map[string]json.RawMessage
		if json.Unmarshal(board[boardKey], &rows) != nil {
			continue
		}
		for _, row := range rows {
			var key *string
			if json.Unmarshal(row[groupKey], &key) != nil || key == nil {
				continue
			}
			g := groups[*key]
			if g == nil {
				g = &group{fields: map[string]json.RawMessage{}, items: []json.RawMessage{}}
				groups[*key] = g
				order = append(order, *key)
			}
			for field, value := range row {
				if _, has := g.fields[field]; !has {
					g.fields[field] = value
				}
			}
			var items []json.RawMessage
			if json.Unmarshal(row[listKey], &items) == nil {
				g.items = append(g.items, items...)
			}
			counts := make(map[string]json.RawMessage, len(countKeys))
			for _, field := range countKeys {
				if value, has := row[field]; has {
					counts[field] = value
				}
			}
			g.counts = append(g.counts, counts)
		}
	}
	if i := slices.Index(order, ""); i >= 0 {
		copy(order[i:], order[i+1:])
		order[len(order)-1] = ""
	}
	out := make([]map[string]json.RawMessage, 0, len(order))
	for _, key := range order {
		g := groups[key]
		g.fields[listKey], _ = json.Marshal(g.items)
		for field, sum := range sumMembers(g.counts) {
			g.fields[field] = sum
		}
		out = append(out, g.fields)
	}
	raw, _ := json.Marshal(out)
	return raw
}

type todoClosedCursor struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// todoClosedCursors are the older-closed cursors each manager's default answer carries, keyed by the manager a
// paginated read must name. A manager from before #15683 has none.
func todoClosedCursors(answers []listAnswer) map[string]todoClosedCursor {
	out := map[string]todoClosedCursor{}
	for i := range answers {
		rawHasMore, ok := mergedMember(&answers[i], "todos", "closed_has_more")
		if !ok {
			continue
		}
		var hasMore bool
		if json.Unmarshal(rawHasMore, &hasMore) != nil || !hasMore {
			continue
		}
		rawCursor, ok := mergedMember(&answers[i], "todos", "closed_next_cursor")
		if !ok {
			continue
		}
		var cursor string
		if json.Unmarshal(rawCursor, &cursor) != nil || cursor == "" {
			continue
		}
		out[answers[i].id] = todoClosedCursor{NextCursor: cursor, HasMore: true}
	}
	return out
}

type mergedSharedBlocker struct {
	Key     string   `json:"key"`
	Kind    string   `json:"kind"`
	Label   string   `json:"label"`
	URL     string   `json:"url,omitempty"`
	TodoIDs []string `json:"todo_ids"`
	Count   int      `json:"count"`
}

func mergeSharedBlockers(all []json.RawMessage) (json.RawMessage, bool) {
	if len(all) == 0 {
		return nil, false
	}
	var valid [][]mergedSharedBlocker
	var first json.RawMessage
	for _, raw := range all {
		var list []mergedSharedBlocker
		if json.Unmarshal(raw, &list) != nil || len(list) == 0 {
			continue
		}
		if first == nil {
			first = raw
		}
		valid = append(valid, list)
	}
	switch len(valid) {
	case 0:
		return nil, false
	case 1:
		return first, true
	}
	type group struct {
		entry mergedSharedBlocker
		todos map[string]bool
	}
	groups := map[string]*group{}
	for _, list := range valid {
		for _, item := range list {
			if item.Key == "" {
				continue
			}
			g := groups[item.Key]
			if g == nil {
				g = &group{entry: item, todos: map[string]bool{}}
				groups[item.Key] = g
			} else if item.Label != "" && (g.entry.Label == "" || item.Label < g.entry.Label) {
				g.entry.Label = item.Label
			}
			for _, id := range item.TodoIDs {
				if id != "" {
					g.todos[id] = true
				}
			}
		}
	}
	var merged []mergedSharedBlocker
	for _, g := range groups {
		if len(g.todos) < 2 {
			continue
		}
		ids := make([]string, 0, len(g.todos))
		for id := range g.todos {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		g.entry.TodoIDs = ids
		g.entry.Count = len(ids)
		merged = append(merged, g.entry)
	}
	if len(merged) == 0 {
		return nil, false
	}
	slices.SortFunc(merged, func(a, b mergedSharedBlocker) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Key, b.Key))
	})
	b, err := json.Marshal(merged)
	return b, err == nil
}

// sumTodoCounts adds up the counts objects of all (each answer's; anything not an object, null included, is none):
// one as it was sent, else member by member over the raw JSON (sumMembers), so a member a newer manager adds, which this
// hub does not know, is passed on or added up too rather than dropped. False when none is an object.
func sumTodoCounts(all []json.RawMessage) (json.RawMessage, bool) {
	var objs []map[string]json.RawMessage
	var first json.RawMessage
	for _, raw := range all {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil || m == nil {
			continue
		}
		if first == nil {
			first = raw
		}
		objs = append(objs, m)
	}
	switch len(objs) {
	case 0:
		return nil, false
	case 1:
		return first, true
	}
	out, err := json.Marshal(sumMembers(objs))
	return out, err == nil
}

// sumMembers is objs added up member by member: a member that is an integer in the first object that has it is the sum
// of the integers the objects hold there; one that is an object, those objects added up so (sections, by key); any
// other, the first's as it is. The same objects in the same order give the same members.
func sumMembers(objs []map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, o := range objs {
		for k, v := range o {
			if _, done := out[k]; done {
				continue
			}
			v = bytes.TrimSpace(v)
			switch {
			case len(v) > 0 && v[0] == '{':
				var subs []map[string]json.RawMessage
				for _, o := range objs {
					var m map[string]json.RawMessage
					if json.Unmarshal(o[k], &m) == nil && m != nil {
						subs = append(subs, m)
					}
				}
				if b, err := json.Marshal(sumMembers(subs)); err == nil {
					v = b
				}
			default:
				if _, isInt := jsonInteger(v); isInt {
					var n int64
					for _, o := range objs {
						if i, ok := jsonInteger(o[k]); ok {
							n += i
						}
					}
					v = strconv.AppendInt(nil, n, 10)
				}
			}
			out[k] = v
		}
	}
	return out
}

// jsonInteger is raw as an integer, when it is one.
func jsonInteger(raw json.RawMessage) (int64, bool) {
	n, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	return n, err == nil
}

// mergedMember is the top-level member name of a, one answer of a list merged under key, as the merge reads it; false
// when a has none, or is no answer the merge takes (not 2xx, not a JSON object, its list not an array).
func mergedMember(a *listAnswer, key, name string) (json.RawMessage, bool) {
	if !a.ok() {
		return nil, false
	}
	s, use := a.scan(key)
	switch use {
	case scanMerge:
		for _, m := range s.members {
			if string(m.key) == name {
				return m.val, true
			}
		}
	case scanDecode: // as writeDecoded reads it
		var t map[string]json.RawMessage
		if json.Unmarshal(a.body, &t) != nil || t == nil {
			return nil, false
		}
		if raw, has := t[key]; has && json.Unmarshal(raw, new([]json.RawMessage)) != nil {
			return nil, false
		}
		v, has := t[name]
		return v, has
	}
	return nil, false
}

// mergeTodoPlaces answers GET /api/v1/todos/places from the managers a merged list asks (mergedAsk): each place that
// is no project (All To-dos, Today, Upcoming, and any fixed place a newer manager adds) once by id, its needs_you added
// up; then each manager's projects as it lists them, stamped with it, the default's first (a project is its manager's
// knowledge base's: two managers' project:<slug> are two places); and the counts added up as the list's are
// (sumTodoCounts). The hub reads only a place's id, kind and needs_you, and passes every other member, of a place or of
// the answer (the first answer's, the default's when it answers), on as sent: a newer manager's additions are not
// dropped. A manager without the route (an older one answers 404 or 405) adds nothing, nor does one that gives no
// places. When none gives places: the first other failure as it is (503 assistant_offline, say), else 404 as a hub
// without the route.
func (h *Hub) mergeTodoPlaces(w http.ResponseWriter, r *http.Request) {
	ids, body, ok := h.mergedAsk(w, r)
	if !ok {
		return
	}
	type joined struct {
		id       string
		place    map[string]json.RawMessage
		needsYou int64
	}
	var top map[string]json.RawMessage
	var fixed []joined
	var own, counts []json.RawMessage
	var failed *bufferedWriter
	for _, id := range ids {
		bw := newBufferedWriter()
		h.forwardHTTP(bw, withBody(r, body), id)
		bw.status = cmp.Or(bw.status, http.StatusOK)
		var ans map[string]json.RawMessage
		var places []json.RawMessage
		if bw.status >= 300 || json.Unmarshal(bw.body.Bytes(), &ans) != nil || ans == nil ||
			ans["places"] != nil && json.Unmarshal(ans["places"], &places) != nil {
			if failed == nil && bw.status >= 300 && bw.status != http.StatusNotFound && bw.status != http.StatusMethodNotAllowed {
				failed = bw
			}
			continue
		}
		if top == nil {
			top = ans
		}
		if c, has := ans["counts"]; has {
			counts = append(counts, c)
		}
		for _, raw := range places {
			var pl map[string]json.RawMessage
			var head struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			}
			if json.Unmarshal(raw, &pl) != nil || pl == nil || json.Unmarshal(raw, &head) != nil {
				continue // no place
			}
			if head.Kind == assistantwire.TodoPlaceKindProject {
				own = append(own, stampManager(raw, id))
				continue
			}
			n, _ := jsonInteger(pl["needs_you"])
			if i := slices.IndexFunc(fixed, func(f joined) bool { return f.id == head.ID }); i >= 0 {
				fixed[i].needsYou += n
				continue
			}
			delete(pl, ManagerQuery) // a place of every manager's is no one manager's
			fixed = append(fixed, joined{id: head.ID, place: pl, needsYou: n})
		}
	}
	switch {
	case top != nil:
		out := make([]json.RawMessage, 0, len(fixed)+len(own))
		for _, f := range fixed {
			if _, has := f.place["needs_you"]; has || f.needsYou != 0 {
				f.place["needs_you"] = strconv.AppendInt(nil, f.needsYou, 10)
			}
			b, _ := json.Marshal(f.place)
			out = append(out, b)
		}
		top["places"], _ = json.Marshal(append(out, own...))
		delete(top, "counts")
		if c, ok := sumTodoCounts(counts); ok {
			top["counts"] = c
		}
		writeJSON(w, top)
	case failed != nil:
		copyHeader(w.Header(), failed.header)
		w.WriteHeader(failed.status)
		_, _ = w.Write(failed.body.Bytes())
	default:
		writeErr(w, http.StatusNotFound, errors.New(http.StatusText(http.StatusNotFound)))
	}
}
