package kb

import (
	"context"
	"slices"
	"sort"
	"time"
)

// PutCollectedItem stores an item a source collected outside the hub's connectors (the Lark minutes and documents
// import, assistant/kblark): a new one is marked seen now; an existing one of the same source keeps what the source
// does not send (tags, projects, first seen) and is tagged again, while a note it already has is kept, so a re-import
// never overwrites a note the user or an earlier run wrote. A note this call brings is locked, so item tagging does
// not replace it. It keeps the item's index row in step. It reports whether it wrote (false: nothing changed, or the
// id belongs to another source).
func (e *Engine) PutCollectedItem(it *Item) (bool, error) {
	if e.Store.ReadOnly() {
		return false, ErrReadOnly
	}
	old, ok := e.Store.Item(it.Kind, it.ID)
	if ok {
		// An ID alone cannot identify an owner: an item another source filed stays that source's.
		if e.itemSource(old) != it.Source {
			return false, nil
		}
		// Nothing the source says changed, and no missing note to fill: leave it (and its tagging) as it is.
		if old.Title == it.Title && old.URL == it.URL && old.At.Equal(it.At) && (it.Note == "" || old.Note != "") {
			return false, nil
		}
		note := it.Note
		carryOver(it, old)
		if old.Note == "" {
			it.Note = note // a note that was never there is filled in now
		}
	} else {
		it.Seen = time.Now().UTC()
		if it.Kind == KindDoc {
			if sel, ret := e.selection(), e.Retention(); sel != nil &&
				!sel.DocSelected(ret, DocFacts{Source: it.Source, ID: it.ID, Signals: it.Signals}) {
				return false, nil
			}
		}
	}
	if it.Note != "" && !slices.Contains(it.LockedFields, "note") {
		// Item tagging rewrites an unlocked note; a note this source owns is kept as written.
		it.LockedFields = append(it.LockedFields, "note")
		sort.Strings(it.LockedFields)
	}
	if err := e.Store.PutItem(it); err != nil {
		return false, err
	}
	e.index(it.Kind, it.ID, it.Title, it.Tagging, it.Note, it.At)
	return true, nil
}

// FillDocTitles gives documents without a title one, at most max per run, asking title for each one's URL; want, when
// given, says which documents to ask about. A document the selection keeps from being read is left alone, and one
// whose title cannot be had waits for a later run. It keeps each filled document's index row in step and returns how
// many it filled.
func (e *Engine) FillDocTitles(ctx context.Context, max int, title func(context.Context, string) (string, error), want func(*Item) bool) int {
	if e.Store.ReadOnly() || max <= 0 {
		return 0
	}
	sel, n := e.selection(), 0
	for _, it := range e.Store.Items(KindDoc) {
		if n >= max || ctx.Err() != nil {
			break
		}
		if it.Title != "" || it.URL == "" || want != nil && !want(it) {
			continue
		}
		if sel != nil {
			if x := sel.Item(e.itemSource(it), ItemDoc, it.ID); x != nil && !x.Read {
				continue // the user keeps it from being read
			}
		}
		t, err := title(ctx, it.URL)
		if err != nil || t == "" {
			continue
		}
		it.Title = t
		if err := e.Store.PutItem(it); err != nil {
			continue
		}
		e.index(it.Kind, it.ID, it.Title, it.Tagging, it.Note, it.At)
		n++
	}
	return n
}
