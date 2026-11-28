package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/i18n"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/connector"
)

// An item's parent (#2128, docs/assistant/inbox.md "Relationships: parents and children"): what the item grew out of, so a thing that started in a chat, became
// a todo and went to a few agents reads as one line. The parent is another item, a todo, an Agent session or a personal
// Agent session; it comes from the event it continues, from its connector (InboxThing.Parent) or from the user. Persona
// only keeps it, lists it and links to it: nothing guesses one, and todos keep no tree of their own
// (docs/assistant/todos.md "Design trade-offs").

// The kinds of parent.
const (
	ParentInbox   = "inbox"
	ParentTodo    = "todo"
	ParentSession = "session" // id "<machine>/<session>", a coding Agent's conversation
	ParentAgent   = "agent"   // id "<agent source>:<sessionKey>", a personal Agent's session
)

// Who set a parent: a user's is never replaced by one that comes on its own; a connector's replaces continues.
const (
	ParentByContinues = "continues"
	ParentByConnector = "connector"
	ParentByUser      = "user"
)

// maxParentDepth is how many parents up an item may sit (its parent's parent… counted); maxParentID bounds an id.
const (
	maxParentDepth = 8
	maxParentID    = 256
)

type InboxParent struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	By   string `json:"by,omitempty"`
}

var (
	errParentKind  = i18n.ErrEnglish("inbox-errors.parent_kind")
	errParentID    = i18n.ErrEnglish("inbox-errors.parent_id")
	errParentCycle = i18n.ErrEnglish("inbox-errors.parent_cycle")
	errParentDeep  = i18n.ErrEnglish("inbox-errors.parent_deep")
)

func knownParentKind(k string) bool {
	return k == ParentInbox || k == ParentTodo || k == ParentSession || k == ParentAgent
}

func checkParentRef(kind, id string) error {
	if !knownParentKind(kind) {
		return errParentKind
	}
	if id == "" || len(id) > maxParentID || strings.TrimSpace(id) != id || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return errParentID
	}
	if kind == ParentAgent {
		source, key, ok := strings.Cut(id, ":")
		if !ok || source == "" || key == "" {
			return errParentID
		}
	}
	return nil
}

// thingParent is the parent a connector gave a thing, nil when it gave none or one that does not hold up.
func thingParent(t connector.InboxThing) *InboxParent {
	if t.Parent == nil || checkParentRef(t.Parent.Kind, t.Parent.ID) != nil {
		return nil
	}
	return &InboxParent{Kind: t.Parent.Kind, ID: t.Parent.ID, By: ParentByConnector}
}

// offerParent gives it p unless what it has outranks p (the user's beats a connector's beats continues). An inbox
// parent that would make a cycle or sit it too deep is dropped. Under b.mu.
func (b *inboxBook) offerParent(it *InboxItem, p *InboxParent) {
	if p == nil {
		return
	}
	if cur := it.Parent; cur != nil && (cur.By == ParentByUser || (cur.By == ParentByConnector && p.By == ParentByContinues)) {
		return
	}
	if p.Kind == ParentInbox && b.parentFits(it.ID, p.ID) != nil {
		return
	}
	it.Parent = p
	if p.Kind == ParentInbox { // grown out of another chat / source's item: related across atoms (inbox_events.go)
		relate(it, b.items[p.ID])
	}
}

// parentFits says whether item id may hang under the item parent: not itself or one of its own descendants, and
// with its ancestors within maxParentDepth. Under b.mu.
func (b *inboxBook) parentFits(id, parent string) error {
	depth := 1
	for cur, seen := parent, map[string]bool{}; ; depth++ {
		if cur == id || seen[cur] {
			return errParentCycle
		}
		seen[cur] = true
		if depth > maxParentDepth {
			return errParentDeep
		}
		up := b.items[cur]
		if up == nil || up.Parent == nil {
			break
		}
		if up.Parent.Kind != ParentInbox {
			depth++ // the todo or session on top is a level too
			break
		}
		cur = up.Parent.ID
	}
	if depth > maxParentDepth {
		return errParentDeep
	}
	return nil
}

// setInboxParent is the user's PUT on an item's parent: p, or nil to take it off.
func (s *Service) setInboxParent(ctx context.Context, id string, p *InboxParent) (InboxItem, int, error) {
	b := &s.inbox
	b.mu.Lock()
	it, ok := b.items[id]
	if !ok {
		b.mu.Unlock()
		return InboxItem{}, http.StatusNotFound, errNoInboxItem
	}
	if p != nil {
		if err := checkParentRef(p.Kind, p.ID); err != nil {
			b.mu.Unlock()
			return InboxItem{}, http.StatusBadRequest, err
		}
		if p.Kind == ParentInbox {
			if _, ok := b.items[p.ID]; !ok {
				b.mu.Unlock()
				return InboxItem{}, http.StatusNotFound, errNoInboxItem
			}
			if err := b.parentFits(id, p.ID); err != nil {
				b.mu.Unlock()
				return InboxItem{}, http.StatusConflict, err
			}
		}
		p = &InboxParent{Kind: p.Kind, ID: p.ID, By: ParentByUser}
	}
	// Keep the old parent so a failed write puts it back: the answer must not say moved while the row did not.
	prevParent := it.Parent
	it.Parent = p
	gen := b.touched(id)
	out := it.listed()
	b.mu.Unlock()
	failed := s.saveInboxUndo(ctx, []*InboxItem{it}, func(failed map[string]bool) {
		// Restore only when no concurrent move (even a same-valued unlink) landed; otherwise leave it.
		if failed[id] && b.items[id] == it && b.mark[id] == gen && it.Parent == p {
			it.Parent = prevParent
		}
	})
	if failed[id] {
		return InboxItem{}, http.StatusInternalServerError, errInboxNotPersisted
	}
	return out, http.StatusOK, nil
}

// InboxTree is a node's line: its ancestors (nearest first; the last may be a todo's or a session's reference with no
// item) and the items right under it, oldest first.
type InboxTree struct {
	Node      InboxParent   `json:"node"`
	Item      *InboxItem    `json:"item,omitempty"` // the node itself when it is an item
	Ancestors []InboxTreeUp `json:"ancestors"`
	Children  []InboxItem   `json:"children"`
}

// InboxTreeUp is one ancestor: an item (Item set) or the todo / session / personal Agent / gone item a chain ends on
// (Item nil).
type InboxTreeUp struct {
	InboxParent
	Item *InboxItem `json:"item,omitempty"`
}

func (s *Service) inboxTree(kind, id string) (InboxTree, error) {
	if err := checkParentRef(kind, id); err != nil {
		return InboxTree{}, err
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	t := InboxTree{Node: InboxParent{Kind: kind, ID: id}, Ancestors: []InboxTreeUp{}, Children: []InboxItem{}}
	if kind == ParentInbox {
		it, ok := b.items[id]
		if !ok {
			return InboxTree{}, errNoInboxItem
		}
		c := it.listed()
		t.Item = &c
		for p, n := it.Parent, 0; p != nil && n < maxParentDepth+1; n++ {
			up := InboxTreeUp{InboxParent: *p}
			next := (*InboxParent)(nil)
			if p.Kind == ParentInbox {
				if x, ok := b.items[p.ID]; ok && x.ID != id {
					c := x.listed()
					up.Item, next = &c, x.Parent
				}
			}
			t.Ancestors = append(t.Ancestors, up)
			p = next
		}
	}
	b.each(func(x *InboxItem) {
		if x.Parent != nil && x.Parent.Kind == kind && x.Parent.ID == id && x.ID != id {
			t.Children = append(t.Children, x.listed())
		}
	})
	slices.SortStableFunc(t.Children, func(a, c InboxItem) int {
		if d := a.StartedAt.Compare(c.StartedAt); d != 0 {
			return d
		}
		return strings.Compare(a.ID, c.ID)
	})
	return t, nil
}

func (s *Service) inboxParentRoutes() {
	// PUT {kind, id} sets the item's parent; null or {} takes it off. (No DELETE: it would clash with
	// DELETE /api/v1/inbox/mutes/{id}.)
	s.handle("PUT /api/v1/inbox/{id}/parent", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in *InboxParent
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if in != nil && in.Kind == "" && in.ID == "" {
			in = nil
		}
		it, code, err := s.setInboxParent(r.Context(), r.PathValue("id"), in)
		if err != nil {
			apierr.Write(w, code, err)
			return
		}
		apierr.WriteJSON(w, it)
	})
	s.handle("GET /api/v1/inbox/tree/{kind}/{id...}", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		t, err := s.inboxTree(r.PathValue("kind"), r.PathValue("id"))
		switch {
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case err != nil:
			apierr.Write(w, http.StatusBadRequest, err)
		default:
			apierr.WriteJSON(w, t)
		}
	})
}
