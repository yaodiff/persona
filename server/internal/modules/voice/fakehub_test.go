package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
)

// fakeHub is the Persona hub's API as the voice extension uses it (docs/assistant/voice.md "Extension calls to hub"): the main conversation
// and its transcript, sends, the sessions list, the items pages and /full, the machines, and /api/v1/ws pushing what
// changes to its subscribers. A message the main conversation takes is worked on for work, then answered with
// reply(text) and a turn_duration line, unless the test drives the conversation itself (manual).
type fakeHub struct {
	t  *testing.T
	mu sync.Mutex

	main      string // the main conversation ("" none yet: starting one makes main1)
	sessions  map[string]*fakeSession
	sent      []sentMsg
	delivered map[string]bool
	full      map[string]string // item id -> its whole text (a page cuts it)

	reply    func(text string) string       // the main conversation's answer (nil: a fixed Chinese line, "a meeting at 3 pm tomorrow")
	onSend   func(m sentMsg) (int, string)  // answers a send instead of taking it (status 0: take it)
	manual   bool                           // a message is put in the transcript and left there: the test answers it
	work     time.Duration                  // how long a turn works (0: 50 ms)
	delay    time.Duration                  // how long a send takes
	subReady <-chan struct{}                // a received subscription starts delivering after this opens
	pageAt   int                            // items per after_seq page (0: all)
	noWS     bool                           // /api/v1/ws is not there
	cut      bool                           // a send's connection is dropped before the hub answers
	toSeq    func(sid string, last int) int // what turns=1 says is the last line (nil: the last item's)
	ws       map[*websocket.Conn]map[string]bool
	wsSaw    []map[string]any
	looks    int // GET /api/v1/sessions?machine_id= (a run's check, or a send's look first)
	// onLook runs as GET /api/v1/sessions?machine_id= is asked, onRead as an after_seq page is, before they answer
	// (unlocked).
	onLook, onRead func()
	onPageSent     func(string, []proto.Item) // observes the after_seq response snapshot; called with mu held

	cards      []proto.Interaction // GET /api/v1/interactions
	interrupts []string            // the sessions POST …/interrupt stopped
	offline    bool                // the machine is not connected
	// resolved are the cards POST /api/v1/interactions/{id}/resolve answered; onResolve answers one instead (status
	// 0: take it; below 0: the connection drops with no answer).
	resolved  []resolvedCard
	onResolve func(id string) (int, string)
	// onInterrupt runs as POST …/interrupt stops sid, before it answers (unlocked).
	onInterrupt func(sid string)
	// triage is the session triage's switches (GET /api/v1/agents/triage; nil: an older hub, 404); stopLag how long an
	// interrupted conversation is still listed as working (the hub answers once the agent got its Esc).
	triage  *triageSwitches
	stopLag time.Duration
	// rooms are the recordings' rooms GET /api/v1/recordings/{id}/live tells (a recording not in it: 404; nil: a
	// recording without one); liveStatus answers it instead when set; liveReads are the paths and queries it was asked.
	rooms      map[string]*liveRoom
	liveStatus int
	liveReads  []string
	// files are the text files POST /api/v1/machines/{mid}/files/write made ("<mid>:<path>" -> text), dirs the
	// folders …/files/mkdir made; onWrite answers a write instead (status 0: take it).
	files   map[string]string
	dirs    []string
	onWrite func(mid, path string) (int, string)
}

type resolvedCard struct {
	ID  string
	Res proto.Resolution
}

type fakeSession struct {
	id         string
	assistant  bool
	status     string
	pending    int
	background int
	turnError  string
	items      []proto.Item
	look       func(s *proto.Session) // what else the list says of it (an agent's title, its activity…)
	// What the hub says of where its last turn stands (#3503): its error's kind and code, its triaged work state, its
	// activity, and when its state and the session last changed.
	errKind, errCode, activity string
	work                       *proto.WorkState
	updated, since             time.Time
}

type sentMsg struct {
	Session, Text, ID string
	ClearDraft        string
	Dismiss           bool
}

func newFakeHub(t *testing.T) *fakeHub {
	return &fakeHub{t: t, main: "main1", sessions: map[string]*fakeSession{"main1": {id: "main1", assistant: true, status: "idle"}},
		delivered: map[string]bool{}, full: map[string]string{}, ws: map[*websocket.Conn]map[string]bool{}}
}

func (h *fakeHub) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/machines/{mid}/files/mkdir", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Dir, Name string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		h.mu.Lock()
		defer h.mu.Unlock()
		dir := r.PathValue("mid") + ":" + in.Dir + "/" + in.Name
		if in.Dir == "/" || in.Dir == "/Users" { // above the home (/Users/me), as the node's write guard has it
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "read only", "code": "read_only"})
			return
		}
		if slices.Contains(h.dirs, dir) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "exists", "code": "exists"})
			return
		}
		h.dirs = append(h.dirs, dir)
		_ = json.NewEncoder(w).Encode(map[string]string{"path": in.Dir + "/" + in.Name})
	})
	mux.HandleFunc("POST /api/v1/machines/{mid}/files/write", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path   string `json:"path"`
			Text   string `json:"text"`
			Create bool   `json:"create"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		mid := r.PathValue("mid")
		h.mu.Lock()
		f := h.onWrite
		h.mu.Unlock()
		if f != nil {
			if status, code := f(mid, in.Path); status != 0 {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "code": code})
				return
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		key := mid + ":" + in.Path
		if _, ok := h.files[key]; ok || !in.Create {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "exists", "code": "exists"})
			return
		}
		if h.files == nil {
			h.files = map[string]string{}
		}
		h.files[key] = in.Text
		_ = json.NewEncoder(w).Encode(map[string]any{"path": in.Path, "size": len(in.Text)})
	})
	mux.HandleFunc("GET /api/v1/kb-search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": []map[string]string{{"path": "people/x.md", "line": "x likes " + r.URL.Query().Get("q")}}})
	})
	mux.HandleFunc("GET /api/v1/assistant", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"machine_id": "m", "main": h.main})
	})
	mux.HandleFunc("POST /api/v1/assistant/conversations", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Prompt string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		h.mu.Lock()
		h.main = "main1"
		h.sessions["main1"] = &fakeSession{id: "main1", assistant: true, status: "working"}
		h.mu.Unlock()
		h.take(sentMsg{Session: "main1", Text: in.Prompt})
		_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"machine_id": "m", "session_id": "main1", "assistant": true}})
	})
	mux.HandleFunc("POST /api/v1/sessions/m/{sid}/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text          string `json:"text"`
			ClientMsgID   string `json:"client_msg_id"`
			ClearDraft    string `json:"clear_draft"`
			DismissNotice bool   `json:"dismiss_notice"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		m := sentMsg{Session: r.PathValue("sid"), Text: in.Text, ID: in.ClientMsgID, ClearDraft: in.ClearDraft, Dismiss: in.DismissNotice}
		h.mu.Lock()
		delay, onSend, cut := h.delay, h.onSend, h.cut
		h.mu.Unlock()
		time.Sleep(delay)
		if cut { // it got there, and the connection dropped before the answer (a redeploy)
			h.mu.Lock()
			h.sent = append(h.sent, m)
			h.mu.Unlock()
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		if onSend != nil {
			if status, body := onSend(m); status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
				return
			}
		}
		h.mu.Lock()
		again := h.delivered[m.ID]
		h.delivered[m.ID] = true
		h.mu.Unlock()
		if !again { // the hub sends an id once: the same one again answers 200 and does nothing
			h.take(m)
		}
		_, _ = io.WriteString(w, `{}`)
	})
	mux.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("machine_id") {
			h.mu.Lock()
			h.looks++
			onLook := h.onLook
			h.mu.Unlock()
			if onLook != nil {
				onLook()
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": h.list()})
	})
	mux.HandleFunc("GET /api/v1/machines", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		online := !h.offline
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"machines": []proto.Machine{{ID: "m", Name: "工作站", Online: online}}})
	})
	mux.HandleFunc("GET /api/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		names := map[string]string{"app": "App", "voice": "语音", "wechat": "微信", "feishu": "飞书机器人"}
		if r.Header.Get("Accept-Language") == "en" {
			names["wechat"] = "WeChat"
		}
		var list []map[string]string
		for _, id := range []string{"app", "voice", "wechat", "feishu"} {
			list = append(list, map[string]string{"id": id, "name": names[id]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"channels": list})
	})
	mux.HandleFunc("GET /api/v1/interactions", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"interactions": append([]proto.Interaction{}, h.cards...)})
	})
	mux.HandleFunc("POST /api/v1/interactions/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var res proto.Resolution
		_ = json.NewDecoder(r.Body).Decode(&res)
		id := r.PathValue("id")
		h.mu.Lock()
		onResolve := h.onResolve
		h.mu.Unlock()
		if onResolve != nil {
			if status, body := onResolve(id); status < 0 {
				if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
					_ = conn.Close()
				}
				return
			} else if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
				return
			}
		}
		h.mu.Lock()
		n := len(h.cards)
		h.cards = slices.DeleteFunc(h.cards, func(ix proto.Interaction) bool { return ix.ID == id })
		if len(h.cards) == n {
			h.mu.Unlock()
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, `{"error":"no longer pending"}`)
			return
		}
		h.resolved = append(h.resolved, resolvedCard{ID: id, Res: res})
		h.pushLocked("", map[string]any{"type": "interaction_resolved", "id": id})
		h.mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	mux.HandleFunc("POST /api/v1/sessions/m/{sid}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.interrupts = append(h.interrupts, r.PathValue("sid"))
		onInterrupt, lag := h.onInterrupt, h.stopLag
		h.mu.Unlock()
		sid := r.PathValue("sid")
		if onInterrupt != nil {
			onInterrupt(sid)
		}
		if lag == 0 {
			h.status(sid, "idle")
		} else {
			time.AfterFunc(lag, func() { h.status(sid, "idle") })
		}
		_, _ = io.WriteString(w, `{}`)
	})
	mux.HandleFunc("GET /api/v1/agents/triage", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		sw := h.triage
		h.mu.Unlock()
		if sw == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(sw)
	})
	mux.HandleFunc("GET /api/v1/sessions/m/{sid}/items", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		onRead := h.onRead
		h.mu.Unlock()
		if onRead != nil && r.URL.Query().Has("after_seq") {
			onRead()
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		s := h.sessions[r.PathValue("sid")]
		if s == nil {
			http.Error(w, `{"error":"no such session"}`, http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		last := 0
		if n := len(s.items); n > 0 {
			last = s.items[n-1].Seq
		}
		if !q.Has("after_seq") { // turns=n: the last n exchanges
			to := last
			if h.toSeq != nil {
				to = h.toSeq(s.id, last)
			}
			turns, _ := strconv.Atoi(q.Get("turns"))
			from := len(s.items)
			for n := 0; from > 0 && n < turns; {
				from--
				if it := s.items[from]; it.Role == "user" && it.Kind == proto.KindText {
					n++
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": append([]proto.Item{}, s.items[from:]...), "to_seq": to})
			return
		}
		after, _ := strconv.Atoi(q.Get("after_seq"))
		var page []proto.Item
		more := false
		for _, it := range s.items {
			if it.Seq <= after {
				continue
			}
			if h.pageAt > 0 && len(page) == h.pageAt {
				more = true
				break
			}
			page = append(page, it)
		}
		to := max(last, after)
		if more {
			to = page[len(page)-1].Seq
		}
		err := json.NewEncoder(w).Encode(map[string]any{"items": page, "more_after": more, "to_seq": to})
		if err == nil && h.onPageSent != nil {
			h.onPageSent(s.id, page)
		}
	})
	mux.HandleFunc("GET /api/v1/sessions/m/{sid}/items/{iid}/full", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, it := range h.sessions[r.PathValue("sid")].items {
			if it.ID == r.PathValue("iid") {
				if full, ok := h.full[it.ID]; ok {
					it.Text, it.FullSize = full, 0
				}
				_ = json.NewEncoder(w).Encode(it)
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /api/v1/recordings/{id}/live", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.liveReads = append(h.liveReads, r.URL.RequestURI())
		if h.liveStatus != 0 {
			w.WriteHeader(h.liveStatus)
			_, _ = io.WriteString(w, `{"error":"not now"}`)
			return
		}
		room, ok := h.rooms[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"error":"no such recording"}`, http.StatusNotFound)
			return
		}
		out := map[string]any{"revs": map[string]int{}}
		if room != nil {
			out["room"] = room
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		noWS := h.noWS
		h.mu.Unlock()
		if noWS || r.Header.Get("Authorization") != "Bearer hubtoken" {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		h.mu.Lock()
		h.ws[c] = map[string]bool{}
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			delete(h.ws, c)
			h.mu.Unlock()
		}()
		_ = writeJSON(context.Background(), c, map[string]any{"type": "hello", "locale": "zh-Hans", "hub_id": "hub1"})
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var f map[string]any
			_ = json.Unmarshal(b, &f)
			h.mu.Lock()
			h.wsSaw = append(h.wsSaw, f)
			sid, _ := f["session_id"].(string)
			switch f["type"] {
			case "subscribe":
				if gate := h.subReady; gate != nil {
					h.mu.Unlock()
					<-gate
					h.mu.Lock()
				}
				h.ws[c][sid] = true
				// What came after since_seq is sent at once, as the hub's sendBacklog does: a line written before the
				// subscription got here is not missed (#5554). From 0 (an empty conversation's base), the last two
				// exchanges, as the hub sends them: a turn that ended while the subscription was on its way is not
				// missed either (#6068).
				if s := h.sessions[sid]; s != nil {
					since, _ := f["since_seq"].(float64)
					after := lastExchanges(s.items, 2)
					if since > 0 {
						after = slices.DeleteFunc(slices.Clone(s.items), func(it proto.Item) bool { return it.Seq <= int(since) })
					}
					if len(after) > 0 {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						_ = writeJSON(ctx, c, map[string]any{"type": "items", "machine_id": "m", "session_id": sid, "items": after, "backlog": true})
						cancel()
					}
				}
			case "unsubscribe":
				delete(h.ws[c], sid)
			}
			h.mu.Unlock()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (h *fakeHub) list() []proto.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listLocked()
}

func (h *fakeHub) listLocked() []proto.Session {
	out := []proto.Session{}
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := h.sessions[id]
		ps := proto.Session{MachineID: "m", SessionID: s.id, Assistant: s.assistant, Status: s.status, State: s.status,
			PendingInteractions: s.pending, TurnError: s.turnError, Rev: int64(len(s.items)), TurnErrorKind: s.errKind,
			TurnErrorCode: s.errCode, Work: s.work, Activity: s.activity, UpdatedAt: s.updated, StateSince: s.since}
		for range s.background {
			ps.Background = append(ps.Background, proto.BackgroundTask{})
		}
		if n := len(s.items); n > 0 {
			ps.LastText = clipRunes(s.items[n-1].Text, 200)
		}
		if s.look != nil {
			s.look(&ps)
		}
		out = append(out, ps)
	}
	return out
}

// lastExchanges is items from the n-th last user message on (the hub's turns=n).
func lastExchanges(items []proto.Item, n int) []proto.Item {
	from := len(items)
	for k := 0; from > 0 && k < n; {
		from--
		if it := items[from]; it.Role == "user" && it.Kind == proto.KindText {
			k++
		}
	}
	return items[from:]
}

// take puts a message into its conversation and, unless manual, works on it and answers in the background. The
// message is in the transcript before it counts as sent (waitSent): a test that answers it by hand cannot get its
// answer in ahead of it, which no hub does (#6068).
func (h *fakeHub) take(m sentMsg) {
	h.add(m.Session, proto.Item{Role: "user", Kind: proto.KindText, Text: m.Text, ClientMsgID: m.ID})
	h.mu.Lock()
	h.sent = append(h.sent, m)
	manual := h.manual
	h.mu.Unlock()
	if !manual {
		go h.answer(m)
	}
}

// answer works on a message taken and answers it.
func (h *fakeHub) answer(m sentMsg) {
	h.mu.Lock()
	work, reply := h.work, h.reply
	h.mu.Unlock()
	if work == 0 {
		work = 50 * time.Millisecond
	}
	answer := "明天下午三点有会"
	if reply != nil {
		answer = reply(m.Text)
	}
	h.status(m.Session, "working")
	time.Sleep(work)
	h.add(m.Session, proto.Item{Role: "assistant", Kind: proto.KindText, Text: answer})
	h.end(m.Session)
}

// add appends items to a conversation's transcript and pushes them to its subscribers.
func (h *fakeHub) add(sid string, items ...proto.Item) {
	h.mu.Lock()
	s := h.sessions[sid]
	for i := range items {
		n := len(s.items) + 1
		if len(s.items) > 0 {
			n = s.items[len(s.items)-1].Seq + 1
		}
		items[i].Seq = n
		if items[i].ID == "" {
			items[i].ID = fmt.Sprintf("%s#%d", sid, n)
		}
		s.items = append(s.items, items[i])
	}
	h.pushLocked(sid, map[string]any{"type": "items", "machine_id": "m", "session_id": sid, "items": items})
	h.mu.Unlock()
}

// end ends a turn: its turn_duration line, and the conversation idle.
func (h *fakeHub) end(sid string) {
	h.add(sid, proto.Item{Role: "system", Kind: "turn_duration", TurnDuration: &proto.TurnDuration{DurationMS: 50, Source: "claude_turn_duration"}})
	h.status(sid, "idle")
}

func (h *fakeHub) status(sid, status string) {
	h.edit(sid, func(s *fakeSession) { s.status = status })
}

// edit changes a conversation and pushes the machine's sessions to every client.
func (h *fakeHub) edit(sid string, f func(s *fakeSession)) {
	h.mu.Lock()
	f(h.sessions[sid])
	h.pushLocked("", map[string]any{"type": "sessions", "machine_id": "m", "sessions": h.listLocked()})
	h.mu.Unlock()
}

// pushLocked sends v to the WebSocket clients: those subscribed to sid, every one for "".
func (h *fakeHub) pushLocked(sid string, v any) {
	for c, subs := range h.ws {
		if sid == "" || subs[sid] {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = writeJSON(ctx, c, v)
			cancel()
		}
	}
}

// raise puts a card up and tells every client, as the hub does (it broadcasts a card to all of them).
func (h *fakeHub) raise(ix proto.Interaction) {
	if ix.CreatedAt.IsZero() {
		ix.CreatedAt = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cards = append(h.cards, ix)
	h.pushLocked("", map[string]any{"type": "interaction", "interaction": ix})
}

// answerOnScreen takes a card down as the app's answer does, and tells every client.
func (h *fakeHub) answerOnScreen(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cards = slices.DeleteFunc(h.cards, func(ix proto.Interaction) bool { return ix.ID == id })
	h.pushLocked("", map[string]any{"type": "interaction_resolved", "id": id})
}

// resolutions are the cards answered through the hub so far.
func (h *fakeHub) resolutions() []resolvedCard {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.resolved)
}

// dropWS closes every /api/v1/ws connection, as the hub does to a client whose queue is full (or on a redeploy).
func (h *fakeHub) dropWS() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.ws {
		_ = c.Close(websocket.StatusPolicyViolation, "queue full")
	}
}

// subscribes is how many subscribe frames for sid the hub got, and the last one.
func (h *fakeHub) subscribes(sid string) (int, map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, last := 0, map[string]any(nil)
	for _, f := range h.wsSaw {
		if f["type"] == "subscribe" && f["session_id"] == sid {
			n, last = n+1, f
		}
	}
	return n, last
}

// lookCount is how many times a conversation was looked up.
func (h *fakeHub) lookCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.looks
}

func (h *fakeHub) messages() []sentMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sentMsg(nil), h.sent...)
}

// waitSent waits for the n-th message the conversations got.
func (h *fakeHub) waitSent(n int) sentMsg {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if msgs := h.messages(); len(msgs) >= n {
			return msgs[n-1]
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the hub never got message %d (it has %d)", n, len(h.messages()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// subscribed waits until a WebSocket client follows sid.
func (h *fakeHub) subscribed(sid string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		ok := false
		for _, subs := range h.ws {
			ok = ok || subs[sid]
		}
		h.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("nobody subscribed to %s", sid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isVoiceID(id string) bool {
	rest, ok := strings.CutPrefix(id, "voice-")
	return ok && len(rest) == 16 && strings.Trim(rest, "0123456789abcdef") == ""
}
