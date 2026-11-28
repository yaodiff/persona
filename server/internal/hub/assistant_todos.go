package hub

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The todos are the manager's (internal/assistant, todos.go; docs/assistant/todos.md): it keeps them and answers their routes,
// which the hub relays (assistantwire.Routes). The hub shows what the manager reports (the snapshot's TodoReport):
// the todo on each agent's session (decorated), the task named on its agents' cards (withTask), which agents are
// quiet (quietSession: no pushes, badges or Live Activity). An agent handed work for a todo through the hub's own
// routes (POST /api/v1/sessions, …/messages with todo_id) is the manager's to check, to give the todo's decisions
// (and a new agent its project's background) and to link (todoHandoff; assistant.todo_link once it went through):
// the hub passes on what it says. A workspace the hub renames is told (todos.space_renamed, pins.go).

// todoHandoff asks the manager about handing p.Text to an agent for p.TodoID (assistant.todo_handoff): the text to
// hand over (the todo's decisions before it, and for a new agent its project's background), or false after writing
// the app the manager's refusal (404 no such todo, 409 a done one) or 503 assistant_offline while it is not connected.
func (h *Hub) todoHandoff(ctx context.Context, w http.ResponseWriter, p assistantwire.TodoHandoff) (string, []proto.Image, bool) {
	res, err := h.asst().TodoHandoff(ctx, p)
	if err != nil {
		writeSendAssistantErr(w, err)
		return "", nil, false
	}
	if res.Status != 0 {
		writeErr(w, res.Status, errors.New(res.Error))
		return "", nil, false
	}
	return res.Text, res.Images, true
}

// mergeTodoImages appends a handoff's stored pictures to the client's own array instead of replacing it. HTTP JSON
// decoding produces []any; the []proto.Image case keeps direct internal callers intact too.
func mergeTodoImages(p map[string]any, images []proto.Image) {
	if len(images) == 0 {
		return
	}
	switch existing := p["images"].(type) {
	case nil:
		p["images"] = images
	case []any:
		for _, img := range images {
			existing = append(existing, img)
		}
		p["images"] = existing
	case []proto.Image:
		p["images"] = append(existing, images...)
	}
}

// todoStartText is the first message of an agent started for a todo without a prompt, as the manager hands it over
// (its project's background and the todo's decisions first), with its stored pictures. Text as it is when the manager
// could not say carries no pictures, so a fresh start never sends images without actual text.
func (h *Hub) todoStartText(ctx context.Context, p assistantwire.TodoHandoff) (string, []proto.Image) {
	res, err := h.asst().TodoHandoff(ctx, p)
	if err != nil {
		h.logf("todo start: todo %s: %v", p.TodoID, err)
		return p.Text, nil
	}
	if !res.Found || res.Text == "" {
		return p.Text, nil
	}
	return res.Text, res.Images
}

// todoLinks is the manager's last TodoReport's links (the task notices on the items the apps get name their task,
// injected.go); replaced, never written into.
type todoLinks struct {
	mu    sync.RWMutex
	links map[[2]string]assistantwire.TodoLink // every manager's, merged (the session owner's wins a clash)
	by    map[string]map[[2]string]assistantwire.TodoLink
}

// takeTodoReport keeps a manager's report (nil: it could not read the todos; the last one stands).
func (h *Hub) takeTodoReport(from string, r *assistantwire.TodoReport) {
	if r == nil {
		return
	}
	links := make(map[[2]string]assistantwire.TodoLink, len(r.Links))
	for _, l := range r.Links {
		links[[2]string{l.MachineID, l.SessionID}] = l
	}
	tl := &h.todoLinks
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if tl.by == nil {
		tl.by = map[string]map[[2]string]assistantwire.TodoLink{}
	}
	tl.by[from] = links
	merged := map[[2]string]assistantwire.TodoLink{}
	for _, m := range tl.by {
		for k, l := range m {
			if _, has := merged[k]; has {
				continue
			}
			// The session owner's link wins, then the built-in assistant's, then the first encountered.
			if owned, ok := tl.by[h.sessionOwner(k[0], k[1])][k]; ok {
				l = owned
			} else if fallback, ok := tl.by[assistantwire.AssistantID][k]; ok {
				l = fallback
			}
			merged[k] = l
		}
	}
	tl.links = merged
}

// freshStarts are the agents started for a todo with nothing to say yet (the apps start Claude in the background and
// send the first message themselves, #2820): their first message is a new agent's first prompt, which gets the
// project background, whether or not it names the todo (iOS and Android do not). Kept in memory for freshStartTTL; a
// hub restarted in between sends that message as it is.
type freshStarts struct {
	mu   sync.Mutex
	byID map[[2]string]freshStart
}

type freshStart struct {
	todoID, cwd string
	at          time.Time
}

const freshStartTTL = 15 * time.Minute

// add remembers agent mid/sid, started for todoID in cwd, until its first message.
func (f *freshStarts) add(mid, sid, todoID, cwd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if f.byID == nil {
		f.byID = map[[2]string]freshStart{}
	}
	for k, v := range f.byID {
		if now.Sub(v.at) > freshStartTTL {
			delete(f.byID, k)
		}
	}
	f.byID[[2]string{mid, sid}] = freshStart{todoID: todoID, cwd: cwd, at: now}
}

// peek is agent mid/sid's fresh start, still kept: a first message that does not go through (refused, nothing typed)
// and is sent again gets the background again.
func (f *freshStarts) peek(mid, sid string) (freshStart, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.byID[[2]string{mid, sid}]
	if !ok || time.Since(v.at) > freshStartTTL {
		return freshStart{}, false
	}
	return v, true
}

// done forgets agent mid/sid's fresh start: its first message went through.
func (f *freshStarts) done(mid, sid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, [2]string{mid, sid})
}

// startCwd is where a start asked for cwd runs on m: as the node resolves it ("" its home, "~/" under it).
func startCwd(m *Machine, cwd string) string {
	m.mu.Lock()
	home := m.info.Home
	m.mu.Unlock()
	if cwd == "" {
		return home
	}
	return proto.ExpandHome(cwd, home) // as the node's checkStart does
}
