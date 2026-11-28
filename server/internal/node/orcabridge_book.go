package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The Orca bridge's book of chats (docs/integrations/orca.md "Structured sessions"): each chat it listed, under
// the Persona session id it gave it, with what a row of it needs once Orca no longer lists it. Kept in the node's
// private cache folder, so a node that restarts gives every chat the id it had (a chat listed under Orca's own id
// before its Claude session existed keeps that id) and keeps listing the chats closed in Orca, not running, rather
// than leaving their Claude transcripts to the node as ordinary sessions (which a Resume would open in a terminal).
//
// Ids: a chat Persona started is "persona-<the id the hub named>" in Orca and that id in Persona; a Claude chat whose
// Claude session the bridge knows when it first lists it is its Claude session id (the id the same conversation has in
// a terminal); any other chat is Orca's session id, and keeps it once its Claude session appears. Losing the book (the
// cache folder cleared) loses only that: such a Claude chat comes back under its Claude session id.

const (
	// orcaBookMax bounds the book: the chats listed last.
	orcaBookMax = 300
	// orcaClosedMax and orcaClosedWindow bound the closed chats listed, as the node's own list of sessions not running
	// is bounded (offlineWindow): the latest 80 of the last 7 days.
	orcaClosedMax    = 80
	orcaClosedWindow = offlineWindow
	// orcaStartPrefix starts the Orca session id of a chat Persona started (orcabridge_start.go): the rest is its id.
	orcaStartPrefix = "persona-"
)

// orcaBookPath is the book's file; "" keeps it in memory only (tests).
var orcaBookPath = func() string {
	dir, err := privateCacheDir("orca")
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chats.json")
}

// orcaBookEntry is one chat the bridge listed.
type orcaBookEntry struct {
	Orca     string `json:"orca"`
	Persona  string `json:"persona"`
	Agent    string `json:"agent"`
	Provider string `json:"provider,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Space    string `json:"space,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Title    string `json:"title,omitempty"`
	// Updated is the chat's last change (epoch ms), Seen when the bridge last saw it open in Orca.
	Updated int64 `json:"updated,omitempty"`
	Seen    int64 `json:"seen"`
}

// orcaBook is the bridge's book, by Orca session id.
type orcaBook struct {
	mu      sync.Mutex
	path    string
	byOrca  map[string]*orcaBookEntry
	dirty   bool
	savedAt time.Time
}

// loadOrcaBook reads the book at path ("": an empty one kept in memory).
func loadOrcaBook(path string) *orcaBook {
	b := &orcaBook{path: path, byOrca: map[string]*orcaBookEntry{}}
	if path == "" {
		return b
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return b
	}
	var entries []orcaBookEntry
	if json.Unmarshal(raw, &entries) != nil {
		return b
	}
	for i := range entries {
		e := entries[i]
		if e.Orca == "" || !runtimeIDRe.MatchString(e.Persona) {
			continue
		}
		b.byOrca[e.Orca] = &e
	}
	return b
}

// orcaPersonaID is the Persona session id of a chat the book has no entry for (above).
func orcaPersonaID(orca, agent, provider string) string {
	if rest, ok := strings.CutPrefix(orca, orcaStartPrefix); ok && runtimeIDRe.MatchString(rest) {
		return rest
	}
	if agent == "claude" && validID(provider) {
		return provider
	}
	if runtimeIDRe.MatchString(orca) {
		return orca
	}
	return "orca-" + strings.Trim(orca, "-_")
}

// note records a chat seen open in Orca now and returns its entry (a copy): the id it has, else the one it gets.
func (b *orcaBook) note(e orcaBookEntry, now time.Time) orcaBookEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	old := b.byOrca[e.Orca]
	if old != nil {
		e.Persona = old.Persona
		e.Provider = firstOf(e.Provider, old.Provider)
		e.Title = firstOf(e.Title, old.Title)
		e.Updated = max(e.Updated, old.Updated)
	} else {
		e.Persona = orcaPersonaID(e.Orca, e.Agent, e.Provider)
		if e.Updated == 0 { // a chat with no turn yet: it changed when it was opened, as far as anyone can tell
			e.Updated = now.UnixMilli()
		}
	}
	e.Seen = now.UnixMilli()
	if old == nil || *old != e {
		// Seen alone moving is not worth a write: it only orders the closed chats, and an hour does not change that.
		if old == nil || old.Seen < e.Seen-int64(time.Hour/time.Millisecond) || entryChanged(*old, e) {
			b.dirty = true
		}
		c := e
		b.byOrca[e.Orca] = &c
	}
	return e
}

// entryChanged: a and b differ in more than when they were last seen.
func entryChanged(a, b orcaBookEntry) bool {
	a.Seen, b.Seen = 0, 0
	return a != b
}

// closed is the chats of the book not open in Orca now (open: their Orca ids) seen within orcaClosedWindow, newest
// first, at most orcaClosedMax, skipping those whose Persona id an open chat has (Orca opened the same conversation
// again under a new chat).
func (b *orcaBook) closed(open map[string]bool, taken map[string]string, now time.Time) []orcaBookEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	since := now.Add(-orcaClosedWindow).UnixMilli()
	var out []orcaBookEntry
	for orca, e := range b.byOrca {
		if open[orca] || e.Seen < since || taken[e.Persona] != "" {
			continue
		}
		out = append(out, *e)
	}
	slices.SortFunc(out, func(x, y orcaBookEntry) int {
		if d := max(y.Updated, y.Seen) - max(x.Updated, x.Seen); d != 0 {
			if d > 0 {
				return 1
			}
			return -1
		}
		return strings.Compare(x.Orca, y.Orca)
	})
	if len(out) > orcaClosedMax {
		out = out[:orcaClosedMax]
	}
	return out
}

// forget drops a chat from the book (closed from Persona, or Orca no longer has it).
func (b *orcaBook) forget(orca string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.byOrca[orca]; ok {
		delete(b.byOrca, orca)
		b.dirty = true
	}
}

// entry is the book's entry of a chat (a copy), ok false when it has none.
func (b *orcaBook) entry(orca string) (orcaBookEntry, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.byOrca[orca]; e != nil {
		return *e, true
	}
	return orcaBookEntry{}, false
}

// flush writes the book when it changed, at most every few seconds (any time when force), keeping the orcaBookMax
// chats seen last. The write goes to a temporary file renamed over the book, so a node stopped in the middle leaves the
// old one whole.
func (b *orcaBook) flush(now time.Time, force bool) {
	b.mu.Lock()
	if !b.dirty || !force && now.Sub(b.savedAt) < 5*time.Second {
		b.mu.Unlock()
		return
	}
	entries := make([]orcaBookEntry, 0, len(b.byOrca))
	for _, e := range b.byOrca {
		entries = append(entries, *e)
	}
	slices.SortFunc(entries, func(x, y orcaBookEntry) int {
		if x.Seen != y.Seen {
			if x.Seen > y.Seen {
				return -1
			}
			return 1
		}
		return strings.Compare(x.Orca, y.Orca)
	})
	if len(entries) > orcaBookMax {
		for _, e := range entries[orcaBookMax:] {
			delete(b.byOrca, e.Orca)
		}
		entries = entries[:orcaBookMax]
	}
	b.dirty, b.savedAt = false, now
	path := b.path
	b.mu.Unlock()
	if path == "" {
		return
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}
