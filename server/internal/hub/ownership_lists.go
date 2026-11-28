package hub

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- merged lists ----

// mergedLists are the list routes whose answer the hub merges over every manager (route → the array that is the
// list); each item gets "manager". The rest of the answer is the default manager's.
var mergedLists = map[string]string{
	"GET /api/v1/inbox":      "items",
	"GET /api/v1/todos":      "todos",
	"GET /api/v1/extensions": "extensions",
	"GET /api/v1/plugins":    "plugins",
	"GET /api/v1/connectors": "connectors",
}

// pagedLists are list routes paged by a cursor (route → the array that is the list): a cursor is one manager's, so
// these are never merged. The manager the request names answers, else the default (pagedList); each item gets
// "manager" as in a merged list, so it is reached the same way. The apps ask each manager and merge the pages.
var pagedLists = map[string]string{
	"GET /api/v1/inbox/done": "items",
}

// listAnswer is one manager's answer to a list request.
type listAnswer struct {
	id     string
	status int
	header http.Header
	body   []byte
	// localize: a 2xx inbox list as its manager gave it, localized as it is merged (inbox_forms.go); nil when the body
	// is as the app reads it.
	localize *inboxLocalize
	// scanned: the merge's scan of body, when mergeList took it first to read the todos' ids; nil when not yet scanned.
	scanned *scannedAnswer
}

func (a listAnswer) ok() bool { return a.status >= 200 && a.status < 300 }

// bufferedWriter keeps what a handler writes (a manager's answer to a list request).
type bufferedWriter struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	localize *inboxLocalize // what its listAnswer gets (forwardInboxList)
}

func newBufferedWriter() *bufferedWriter { return &bufferedWriter{header: http.Header{}} }

func (b *bufferedWriter) Header() http.Header { return b.header }
func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}
func (b *bufferedWriter) Flush() {} // a relayed answer flushes as it streams; this one is written whole

func (b *bufferedWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

// listManagers are the managers a merged list asks: the default first, then the others connected now, by id.
func (h *Hub) listManagers(ctx context.Context) []string {
	def := h.defaultOwner(ctx)
	out := []string{def}
	var rest []string
	for _, p := range h.busRoster() {
		if p.Role == bus.RoleManager && p.ID != def {
			rest = append(rest, p.ID)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// retainedListManagers also asks installed offline managers, so relayTo can serve their retained GET answers.
// Identities, never cache keys, decide who still belongs to the roster (kept a moment, busIdentities). An
// unreadable roster fails closed.
func (h *Hub) retainedListManagers(ctx context.Context) ([]string, error) {
	present := map[string]bool{}
	if h.builtinManager(assistantwire.AssistantID) {
		present[assistantwire.AssistantID] = true
	}
	if s := h.busStore(); s != nil {
		idents, err := h.busIdentities(ctx, s)
		if err != nil {
			return nil, err
		}
		for _, p := range idents {
			if p.Role == bus.RoleManager {
				present[p.ID] = true
			}
		}
	}
	for _, p := range h.busRoster() {
		if p.Role == bus.RoleManager {
			present[p.ID] = true
		}
	}
	def, err := h.defaultOwnerOrErr(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for id := range present {
		if id != def {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	if present[def] { // a removed external default must not reappear from its retained answer
		out = append([]string{def}, out...)
	}
	return out, nil
}

// mergedAsk are the managers a merged request asks (mergeList) and the body each is asked with: the one the request is
// for (one it names, an extension's: its own, impliedManager), else every manager (listManagers; for a kept route also
// the installed offline ones, retainedListManagers). False when it answered.
func (h *Hub) mergedAsk(w http.ResponseWriter, r *http.Request) ([]string, []byte, bool) {
	ids := []string{}
	named, err := impliedManager(r)
	if err != nil {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, err)
		return nil, nil, false
	}
	if named != "" {
		id, ok := h.owner(w, r, h.byRequest)
		if !ok {
			return nil, nil, false
		}
		ids = append(ids, id)
	} else if r.Method == http.MethodGet && slices.Contains(extwire.KeptRoutes, r.URL.Path) {
		ids, err = h.retainedListManagers(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return nil, nil, false
		}
	} else {
		ids = h.listManagers(r.Context())
	}
	if len(ids) == 0 {
		writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
		return nil, nil, false
	}
	body, err := readBody(w, r)
	if err != nil {
		return nil, nil, false
	}
	return ids, body, true
}

// mergeList answers a list request: a request for a manager (one it names, an extension's: its own, impliedManager) gets
// that manager's answer, else every manager is asked (ask, one after the other: the managers are few) and the lists under key merged, the default's first. Every
// item is stamped with its manager. A manager that does not answer is left out; when none does, the default's
// answer is given as it is. A paginated to-dos read is one manager's (pagedTodoList): its cursor cannot name rows of
// another manager's list.
func (h *Hub) mergeList(w http.ResponseWriter, r *http.Request, key string, ask func(w http.ResponseWriter, r *http.Request, id string)) {
	if key == "todos" && (r.URL.Query().Has("limit") || r.URL.Query().Has("cursor")) {
		h.pagedTodoList(w, r, key, ask)
		return
	}
	ids, body, ok := h.mergedAsk(w, r)
	if !ok {
		return
	}
	var answers []listAnswer
	for _, id := range ids {
		bw := newBufferedWriter()
		asked := withBody(r, body)
		if key == "todos" {
			asked.Header.Del("If-None-Match") // the merged list's validator is the hub's: a manager's 304 would leave its todos out
		}
		ask(bw, asked, id)
		ans := listAnswer{id: id, status: cmp.Or(bw.status, http.StatusOK), header: bw.header, body: bw.body.Bytes(), localize: bw.localize}
		if key == "todos" && ans.ok() {
			h.noteTodoOwners(&ans)
		}
		answers = append(answers, ans)
	}
	if key == "todos" {
		writeTaggedList(w, r, key, answers, todoListSums(answers)) // the counts added up (todo_view.go)
		return
	}
	writeMerged(w, key, answers)
}

// writeTaggedList is writeMerged with a validator, for the to-dos (#9590): the merged list is the same bytes while
// nothing it lists changed, and most "todos" events change nothing it lists (a mark, a done to-do's note), yet each
// had every app read the whole list again (about 200 KB on a busy hub). A 200 carries ETag W/"t1-<the first 128 bits
// of the SHA-256 of its bytes>"; a request whose If-None-Match names it gets 304 with the 200's headers and no body.
// set are top-level members written instead of the default's (writeMergedSet), so the tag covers them too.
func writeTaggedList(w http.ResponseWriter, r *http.Request, key string, answers []listAnswer, set map[string]json.RawMessage) {
	out := newBufferedWriter()
	writeMergedSet(out, key, answers, set)
	status := cmp.Or(out.status, http.StatusOK)
	hd := w.Header()
	for k, v := range out.header {
		hd[k] = slices.Clone(v)
	}
	if status == http.StatusOK {
		if key == "todos" {
			hd.Del(assistantwire.TodoRevisionHeader) // the base answer's manager token is not a hub-wide one
			if revision, complete := todoRevisionOfAnswers(answers); complete {
				hd.Set(assistantwire.TodoRevisionHeader, revision)
			}
		}
		sum := sha256.Sum256(out.body.Bytes())
		tag := `W/"t1-` + hex.EncodeToString(sum[:16]) + `"`
		hd.Set("ETag", tag)
		if inm := r.Header.Values("If-None-Match"); len(inm) > 0 && tagMatches(inm, tag) {
			hd.Del("Content-Length")
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(out.body.Bytes())
}

// noteTodoOwners notes a's manager as the owner of each todo in a, a 2xx answer to a todos list. It reads the ids from
// the scan the merge then writes a from, where that scan alone says what decodedTodoIDs reads, else by decoding.
func (h *Hub) noteTodoOwners(a *listAnswer) {
	s, use := scanAnswer(a.body, "todos")
	a.scanned = &scannedAnswer{s: s, use: use}
	if ids, ok := scannedTodoIDs(s, use); ok {
		for _, id := range ids {
			if len(id) > 0 {
				h.noteTodoOwner(string(id), a.id)
			}
		}
		return
	}
	for _, id := range decodedTodoIDs(a.body) {
		if id != "" {
			h.noteTodoOwner(id, a.id)
		}
	}
}

// decodedTodoIDs are the ids of a todos list's items as encoding/json reads them, "" for an item without one; none
// when the body does not decode.
func decodedTodoIDs(body []byte) []string {
	var t struct {
		Todos []struct {
			ID string `json:"id"`
		} `json:"todos"`
	}
	if json.Unmarshal(body, &t) != nil {
		return nil
	}
	ids := make([]string, len(t.Todos))
	for i, td := range t.Todos {
		ids[i] = td.ID
	}
	return ids
}

// pagedList answers a request on one of pagedLists: the one manager's answer (the one it names, an extension's own,
// else the default), each item under key stamped with it; the rest of the answer (the cursor) as it is.
func (h *Hub) pagedList(w http.ResponseWriter, r *http.Request, key string, ask func(w http.ResponseWriter, r *http.Request, id string)) {
	id, ok := h.owner(w, r, h.byRequest)
	if !ok {
		return
	}
	bw := newBufferedWriter()
	ask(bw, r, id)
	writeMerged(w, key, []listAnswer{{id: id, status: cmp.Or(bw.status, http.StatusOK), header: bw.header, body: bw.body.Bytes(), localize: bw.localize}})
}

// pagedTodoList answers a paginated to-dos read from one manager. A cursor belongs to one manager's list, so merging
// pages would silently drop the other managers' rows. The request names its manager (X-Persona-Manager or ?manager=),
// an extension's token implies its own, and a hub whose roster holds exactly one manager (connected, or installed with
// a kept answer) answers from it; otherwise the read is refused rather than answered from part of the list.
func (h *Hub) pagedTodoList(w http.ResponseWriter, r *http.Request, key string, ask func(http.ResponseWriter, *http.Request, string)) {
	named, err := impliedManager(r)
	if err != nil {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, err)
		return
	}
	if named == "" {
		ids, err := h.retainedListManagers(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		if len(ids) == 0 {
			writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
			return
		}
		if len(ids) > 1 {
			writeErrCode(w, http.StatusBadRequest, "todos_pagination_manager_required",
				errors.New("a paginated to-do list is one manager's: name it with X-Persona-Manager or the manager query parameter"))
			return
		}
		named = ids[0]
		r = r.Clone(r.Context())
		r.Header.Set(assistantwire.ManagerHeader, named)
	}
	id, ok := h.owner(w, r, h.byRequest)
	if !ok {
		return
	}
	bw := newBufferedWriter()
	ask(bw, r, id)
	ans := listAnswer{id: id, status: cmp.Or(bw.status, http.StatusOK), header: bw.header, body: bw.body.Bytes(), localize: bw.localize}
	if ans.ok() {
		// Older managers ignore limit and cursor and answer the complete list. Even a final page
		// from a pagination-aware manager explicitly includes has_more=false.
		var page struct {
			HasMore *bool `json:"has_more"`
		}
		if json.Unmarshal(ans.body, &page) != nil || page.HasMore == nil {
			writeErrCode(w, http.StatusNotImplemented, "todos_pagination_unsupported", i18n.Err("hub-assistant_link.todos_pagination_unsupported"))
			return
		}
		h.noteTodoOwners(&ans) // a page teaches the hub its rows' manager as a complete list does
	}
	writeMerged(w, key, []listAnswer{ans})
}

// writeMerged writes answers merged (see mergeList): the top level of the first answer that is a JSON object (the
// default's, unless it failed), its list under key holding every such answer's items stamped with their manager, keys
// sorted, as encoding/json writes a map. An inbox answer its relay left to be localized here (listAnswer.localize)
// reads as InboxJSON on its whole body would have made it. Nobody answered that way: the default's answer as it is.
//
// Its bytes are those of decoding each answer and encoding it again (writeDecoded), written from one scan of each
// (writeScanned, merged_scan.go): 400 inbox items took 13 ms and 6.9 MB that way, about 3.5 ms and 0.5 MB this way.
func writeMerged(w http.ResponseWriter, key string, answers []listAnswer) {
	writeMergedSet(w, key, answers, inboxStoredSourceMerge(key, answers))
}

// writeMergedSet is writeMerged with the top-level members of set (a JSON value by key, never key itself) written
// instead of the default's, or beside them when it has none: what the hub adds up over the managers (the to-dos'
// counts). Nobody answered: the default's answer as it is, without them.
func writeMergedSet(w http.ResponseWriter, key string, answers []listAnswer, set map[string]json.RawMessage) {
	if writeScanned(w, key, answers, set) {
		return
	}
	for i := range answers {
		answers[i].localizeWhole()
	}
	writeDecoded(w, key, answers, set)
}

// writeScanned is writeMergedSet from a scan of each answer; false, having written nothing, when an answer is one only
// decoding reads right (scanDecode) or nobody answered.
func writeScanned(w http.ResponseWriter, key string, answers []listAnswer, set map[string]json.RawMessage) bool {
	type part struct {
		id    string
		items []scannedItem
	}
	var base *listAnswer
	var top []jsonMember
	var parts []part
	size := 0
	for i := range answers {
		a := &answers[i]
		if !a.ok() {
			continue
		}
		s, use := a.scan(key)
		switch use {
		case scanSkip:
			continue
		case scanDecode:
			return false
		}
		if base == nil {
			base, top = a, withMembers(s.members, set)
			for _, m := range top {
				size += len(m.key) + len(m.val) + len(`"":,`)
			}
		}
		parts = append(parts, part{a.id, s.items})
		for _, it := range s.items {
			size += len(it.raw) + len(`{"manager":"",},`) + len(a.id)
		}
	}
	if base == nil {
		return false
	}
	out := make([]byte, 0, size+len(key)+8)
	out = append(out, '{')
	listAt, _ := slices.BinarySearchFunc(top, key, func(m jsonMember, key string) int { return cmp.Compare(string(m.key), key) })
	for j := 0; j <= len(top); j++ {
		if j == listAt {
			if len(out) > 1 {
				out = append(out, ',')
			}
			out = append(out, '"')
			out = append(out, key...)
			out = append(out, `":[`...)
			first := true
			for _, p := range parts {
				var quoted [64]byte
				idJSON := appendJSONID(quoted[:0], p.id)
				for _, it := range p.items {
					if !first {
						out = append(out, ',')
					}
					first = false
					var ok bool
					if out, ok = appendStamped(out, it, p.id, idJSON); !ok {
						return false
					}
				}
			}
			out = append(out, ']')
		}
		if j == len(top) {
			break
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		m := top[j]
		out = append(out, '"')
		out = append(out, m.key...)
		out = append(out, '"', ':')
		if m.asIs {
			out = append(out, m.val...)
			continue
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if enc.Encode(json.RawMessage(m.val)) != nil {
			return false
		}
		out = append(out, bytes.TrimSuffix(b.Bytes(), []byte{'\n'})...)
	}
	out = append(out, '}', '\n')
	copyHeader(w.Header(), base.header)
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(base.status)
	_, _ = w.Write(out)
	return true
}

// withMembers is members (sorted by key, as an answerScan has them) with set's written instead of the ones of the same
// key, or in their place by key; members itself when set is empty, else a copy. A member of set is encoded as
// encoding/json writes a RawMessage, as writeDecoded writes it.
func withMembers(members []jsonMember, set map[string]json.RawMessage) []jsonMember {
	if len(set) == 0 {
		return members
	}
	out := slices.Clone(members)
	for k, v := range set {
		m := jsonMember{key: []byte(k), val: v}
		i, found := slices.BinarySearchFunc(out, m.key, func(x jsonMember, key []byte) int { return bytes.Compare(x.key, key) })
		if found {
			out[i] = m
		} else {
			out = slices.Insert(out, i, m)
		}
	}
	return out
}

// appendJSONID appends id as json.Marshal writes it, quoting it by hand when nothing in it is escaped.
func appendJSONID(out []byte, id string) []byte {
	for i := 0; i < len(id); i++ {
		if c := id[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			b, _ := json.Marshal(id)
			return append(out, b...)
		}
	}
	out = append(out, '"')
	out = append(out, id...)
	return append(out, '"')
}

// appendStamped appends item with "manager": id as json.Marshal writes stampManager's item in a list.
func appendStamped(out []byte, it scannedItem, id string, idJSON []byte) ([]byte, bool) {
	switch it.shape {
	case itemAsIs:
		return append(out, it.raw...), true
	case itemStamp:
		out = append(out, `{"manager":`...)
		out = append(out, idJSON...)
		if it.raw[1] == '}' {
			return append(out, '}'), true
		}
		out = append(out, ',')
		return append(out, it.raw[1:]...), true
	}
	b, err := json.Marshal(json.RawMessage(stampManager(it.raw, id)))
	return append(out, b...), err == nil
}

// writeDecoded is writeMergedSet by decoding each answer and encoding the merge again. A body that is not a JSON object
// (null included) is no answer.
func writeDecoded(w http.ResponseWriter, key string, answers []listAnswer, set map[string]json.RawMessage) {
	var base *listAnswer
	var items []json.RawMessage
	var top map[string]json.RawMessage
	for i := range answers {
		a := &answers[i]
		if !a.ok() {
			continue
		}
		var t map[string]json.RawMessage
		if json.Unmarshal(a.body, &t) != nil || t == nil {
			continue
		}
		raw, has := t[key]
		var list []json.RawMessage
		if has && json.Unmarshal(raw, &list) != nil {
			continue
		}
		for _, it := range list {
			items = append(items, stampManager(it, a.id))
		}
		if base == nil {
			base, top = a, t
		}
	}
	if base == nil { // nobody answered: the default's answer as it is
		a := answers[0]
		copyHeader(w.Header(), a.header)
		w.WriteHeader(a.status)
		_, _ = w.Write(a.body)
		return
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	list, _ := json.Marshal(items)
	top[key] = list
	for k, v := range set {
		top[k] = v
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(top)
	copyHeader(w.Header(), base.header)
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(base.status)
	_, _ = w.Write(buf.Bytes())
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = v
	}
}

// stampManager is item (a JSON object) with "manager": id; anything else as it is.
func stampManager(item json.RawMessage, id string) json.RawMessage {
	t := bytes.TrimSpace(item)
	if len(t) < 2 || t[0] != '{' {
		return item
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(t, &fields) != nil {
		return item
	}
	idJSON, _ := json.Marshal(id)
	if _, has := fields[ManagerQuery]; has {
		fields[ManagerQuery] = idJSON
		out, _ := json.Marshal(fields)
		return out
	}
	var b bytes.Buffer
	b.WriteString(`{"manager":`)
	b.Write(idJSON)
	if len(fields) > 0 {
		b.WriteByte(',')
		b.Write(bytes.TrimSpace(t[1:]))
	} else {
		b.WriteByte('}')
	}
	return b.Bytes()
}

// readBody reads r's body (a list request has none, or a small one) so every manager can be asked with it; it
// answered when it fails.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(http.MaxBytesReader(w, r.Body, busHTTPLimit)); err != nil {
		writeBodyErr(w, err, err)
		return nil, err
	}
	return b.Bytes(), nil
}

// withBody is r with body to read again.
func withBody(r *http.Request, body []byte) *http.Request {
	r2 := r.Clone(r.Context())
	if body == nil {
		r2.Body = http.NoBody
	} else {
		r2.Body = readCloser{bytes.NewReader(body)}
	}
	return r2
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

// notifyManagers sends every connected manager a one-way event (what the hub saw that is no one manager's: the apps
// it has, a mute, a source with something new); errBusOffline when none is connected.
func (h *Hub) notifyManagers(event string, params any) error {
	sent := false
	for _, p := range h.busRoster() {
		if p.Role == bus.RoleManager && h.notifyBus(p.ID, event, params) == nil {
			sent = true
		}
	}
	if !sent {
		return errBusOffline
	}
	return nil
}
