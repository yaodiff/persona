package assistant

import (
	"slices"
	"strings"
	"time"
)

// GET /api/v1/inbox lists every open item and the newest inboxListDone of the items done within inboxKeepDone. It
// used to list every item done within inboxKeepDone: after a few thousand were marked done at once, a hub listed
// about 10,000 items (21 MB, 0.8 s), every app downloaded and decoded all of them on every inbox push, and Android's
// bounded reader (4 MiB, 100,000 structural tokens) turned the list down altogether. The done items it no longer
// carries are where they were: GET /api/v1/inbox/done pages every done item from the newest (inbox_history.go),
// GET /api/v1/inbox/{id} reads any of them, and the day counts count them all.
//
// The newest are by done_at, not by updated_at: an old item marked done just now is listed, so a client that marked it
// finds it in the next list (its Undo, its Done row). Ties are by id, so the same items give the same list (the
// merged answer's validator, #4739).

// inboxDoneCut is the oldest done item a list still carries (by done_at, then id): a done item older than it is left
// out. The zero value leaves none out (the window holds at most inboxListDone).
type inboxDoneCut struct {
	at time.Time
	id string
}

// listedDoneCut is where a list of all (connectors' items too) or not cuts the done items within cutoff. Under b.mu.
func (b *inboxBook) listedDoneCut(cutoff time.Time, all bool) inboxDoneCut {
	n := 0
	for _, it := range b.items {
		if doneWithin(it, cutoff, all) {
			n++
		}
	}
	if n <= inboxListDone {
		return inboxDoneCut{}
	}
	done := make([]*InboxItem, 0, n)
	for _, it := range b.items {
		if doneWithin(it, cutoff, all) {
			done = append(done, it)
		}
	}
	slices.SortFunc(done, func(x, y *InboxItem) int { return newerDone(x.DoneAt, x.ID, y.DoneAt, y.ID) })
	last := done[inboxListDone-1]
	return inboxDoneCut{at: *last.DoneAt, id: last.ID}
}

// doneWithin: a done item within cutoff that a list of all (or not) would show.
func doneWithin(it *InboxItem, cutoff time.Time, all bool) bool {
	return it.DoneAt != nil && !it.DoneAt.Before(cutoff) && (all || it.Connector == nil)
}

// newerDone orders done items newest done first, by id (descending) among those of the same moment, as the history
// pages them (inbox_items' done_at DESC, id DESC).
func newerDone(at1 *time.Time, id1 string, at2 *time.Time, id2 string) int {
	if c := at2.Compare(*at1); c != 0 {
		return c
	}
	return strings.Compare(id2, id1)
}

// inboxShows says whether a list of all (or not) made with cutoff and cut shows the item.
func inboxShows(it *InboxItem, cutoff time.Time, cut inboxDoneCut, all bool) bool {
	if it.MergedInto != "" { // a member of a merged row: its survivor shows
		return false
	}
	if !all && it.Connector != nil {
		return false
	}
	if it.DoneAt == nil {
		return true
	}
	if it.DoneAt.Before(cutoff) {
		return false
	}
	return cut.at.IsZero() || newerDone(it.DoneAt, it.ID, &cut.at, cut.id) <= 0
}
