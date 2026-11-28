package hub

import (
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Structured owner-ask pushes ring once per reason or state while waiting on the owner:
// "todo:<id>:<reason>" (assistantwire.TodoPushKey) and "concern:<id>:<state>". Legacy keys
// ("todo:<id>@<round>") and unstructured keys are unaffected. Repeat pushes with the same
// structured key within gradingDedupWindow (12h) are held regardless of push level (including P0).
// For concerns, moving to another state releases the earlier state; EventFleetResolved releases
// the to-do, concern, or tracked card.

// todoPushBook tracks when each structured todo/concern push key last rang and linked card IDs.
type todoPushBook struct {
	mu    sync.Mutex
	at    map[string]time.Time
	cards map[string]string
}

func (p *Pusher) todoPushNow() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func structuredPushKeyParts(key string) (kind, id, token string, ok bool) {
	if id, reason, matched := assistantwire.TodoPushKeyParts(key); matched {
		return "todo", id, reason, true
	}
	rest, found := strings.CutPrefix(strings.TrimSpace(key), "concern:")
	if !found || strings.Contains(rest, "@") {
		return "", "", "", false
	}
	id, state, found := strings.Cut(rest, ":")
	id, state = strings.TrimSpace(id), strings.TrimSpace(state)
	if !found || id == "" || state == "" {
		return "", "", "", false
	}
	return "concern", id, state, true
}

func concernOfResolvedCard(cardID string) string {
	cardID = strings.TrimSpace(cardID)
	if rest, ok := strings.CutPrefix(cardID, "concern-ask:"); ok {
		return strings.TrimSpace(rest)
	}
	if rest, ok := strings.CutPrefix(cardID, "concern:"); ok {
		id, _, _ := strings.Cut(rest, ":")
		return strings.TrimSpace(id)
	}
	return ""
}

func deletePrefixedLocked(b *todoPushBook, skip string, prefixes ...string) {
	has := func(s string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(s, p) {
				return true
			}
		}
		return false
	}
	for k := range b.at {
		if k != skip && has(k) {
			delete(b.at, k)
		}
	}
	for cid, mapped := range b.cards {
		if mapped != skip && has(mapped) {
			delete(b.cards, cid)
		}
	}
}

func (p *Pusher) claimTodoPush(key string) bool {
	return p.claimTodoPushCard(key, "")
}

func (p *Pusher) claimTodoPushCard(key, cardID string) bool {
	if p == nil {
		return true
	}
	kind, id, token, ok := structuredPushKeyParts(key)
	if !ok {
		return true
	}
	canonical := kind + ":" + id + ":" + token
	now := p.todoPushNow()
	b := &p.todoPushes
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, at := range b.at {
		if now.Sub(at) >= gradingDedupWindow {
			delete(b.at, k)
		}
	}
	for cid, mapped := range b.cards {
		if _, alive := b.at[mapped]; !alive {
			delete(b.cards, cid)
		}
	}
	if at, seen := b.at[canonical]; seen && now.Sub(at) < gradingDedupWindow {
		return false
	}
	if kind == "concern" {
		deletePrefixedLocked(b, canonical, "concern:"+id+":")
	}
	if b.at == nil {
		b.at = map[string]time.Time{}
	}
	b.at[canonical] = now
	if cid := strings.TrimSpace(cardID); cid != "" {
		if b.cards == nil {
			b.cards = map[string]string{}
		}
		b.cards[cid] = canonical
	}
	return true
}

func (p *Pusher) todoPushOver(id string) {
	id = strings.TrimSpace(id)
	if p == nil || id == "" {
		return
	}
	b := &p.todoPushes
	b.mu.Lock()
	defer b.mu.Unlock()
	deletePrefixedLocked(b, "", "todo:"+id+":", "concern:"+id+":")
}

func (p *Pusher) resolvedPushOver(cardID string) {
	if p == nil {
		return
	}
	if todo := assistantwire.TodoOfAskCard(cardID); todo != "" {
		p.todoPushOver(todo)
		return
	}
	if concern := concernOfResolvedCard(cardID); concern != "" {
		p.todoPushOver(concern)
		return
	}
	cid := strings.TrimSpace(cardID)
	if cid == "" {
		return
	}
	b := &p.todoPushes
	b.mu.Lock()
	defer b.mu.Unlock()
	key, ok := b.cards[cid]
	if !ok {
		return
	}
	delete(b.cards, cid)
	if kind, id, _, valid := structuredPushKeyParts(key); valid {
		deletePrefixedLocked(b, "", kind+":"+id+":")
		return
	}
	delete(b.at, key)
}
