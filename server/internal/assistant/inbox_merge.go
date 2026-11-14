package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Merging rows by hand (#6480, docs/assistant/inbox.md "Merge rows"). The grouping model sometimes splits one matter into
// several rows; the user picks them in Select and merges them. The newest row survives and stands for all of them:
//
//   - the others become its members: hidden from every list (`merged_into`), not open as far as triage, the AI twin and
//     the counts go (InboxItem.open is false for them), but still collected into by their own source, so the survivor
//     reads them: a new message in a member's thread reopens the survivor with it (followMerged, after each round);
//   - the survivor lists its members (`merged_from`: id, source, title; a reply picks one of them), takes the highest tier,
//     the members' counts, and their chain entries by time, headed by "Merged from N rows";
//   - Undo gives the members their rows back (a message that came meanwhile stays in the row it came into).
//
// The survivor keeps its own processing conversation; a member's conversation stays with the member, its entries in the
// chain. A link or push naming a member opens the survivor (GET /api/v1/inbox/{id}).

// InboxMergeRef names a member of a merged row.
type InboxMergeRef struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	AccountName string `json:"account_name,omitempty"`
	Title       string `json:"title,omitempty"`
}

// tierRank orders tiers by how much they need the user: Needs you, FYI, Collapsed, untriaged.
func tierRank(t string) int {
	switch t {
	case TierAction:
		return 3
	case TierFYI:
		return 2
	case TierNoise:
		return 1
	}
	return 0
}

func clipRunesTo(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

const (
	mergeMax    = 20 // rows one merge takes
	mergeCtxMax = 6000
)

// merge makes the newest of items the survivor of all of them and returns it and the rows it took (their own members
// too). Under b.mu; the caller has checked that every item is open and not a member.
func (b *inboxBook) merge(items []*InboxItem, now time.Time) (*InboxItem, []*InboxItem) {
	sv := items[0]
	for _, it := range items[1:] {
		if newerItem(it, sv) {
			sv = it
		}
	}
	var took []*InboxItem
	var entries []InboxChainEntry
	for _, it := range items {
		if it == sv {
			continue
		}
		took = append(took, it)
		for _, ref := range it.MergedFrom { // a survivor merged again: its members follow it
			if m := b.items[ref.ID]; m != nil && m.MergedInto == it.ID {
				m.MergedInto = sv.ID
				took = append(took, m)
				sv.MergedFrom = append(sv.MergedFrom, ref)
			}
		}
		it.MergedFrom = nil
		it.MergedInto = sv.ID
		sv.MergedFrom = append(sv.MergedFrom, InboxMergeRef{ID: it.ID, Source: it.Source, AccountName: it.AccountName, Title: it.Title})
		sv.Count += it.Count
		if it.StartedAt.Before(sv.StartedAt) {
			sv.StartedAt = it.StartedAt
		}
		if it.Context != "" && it.Context != sv.Context {
			sv.Context = clipRunesTo(sv.Context+"\n\n"+it.Context, mergeCtxMax)
		}
		entries = append(entries, listedChain(it)...)
	}
	if sv.PreMergeTier == "" {
		sv.PreMergeTier = sv.Tier + "|" // the "|" tells an untriaged row from one with nothing recorded
	}
	for _, it := range took {
		if tierRank(it.Tier) > tierRank(sv.Tier) {
			sv.Tier, sv.Reason = it.Tier, it.Reason
		}
		sv.Urgent = sv.Urgent || it.Urgent
	}
	// The members' chain entries join the survivor's by time; "Merged from N rows" comes last.
	if len(sv.Chain) == 0 {
		sv.Chain = synthesizedChain(b.userLang(), sv)
		sv.ChainSeq = len(sv.Chain)
	}
	all := append(slices.Clone(sv.Chain), entries...)
	slices.SortStableFunc(all, func(x, y InboxChainEntry) int { return x.At.Compare(y.At) })
	sv.Chain = nil
	for _, e := range all {
		sv.ChainSeq++
		e.ID = "c" + strconv.Itoa(sv.ChainSeq)
		sv.Chain = append(sv.Chain, e)
	}
	if n := len(sv.Chain); n > chainMax {
		sv.Chain = slices.Clone(sv.Chain[n-chainMax:])
	}
	b.chainAdd(sv, InboxChainEntry{Kind: ChainMerged, At: now.UTC(),
		Text: i18n.T(b.userLang(), "assistant-inbox_chain.merged_from", i18n.Args{"n": len(sv.MergedFrom) + 1})})
	sv.Read = false
	sv.TriagedAt = "" // the merged row is judged again, with the members' text
	return sv, took
}

// liftMerged keeps a survivor's tier at the highest of the rows it stands for: its own before the merge and its members'
// now. A model's verdict on the merged row (triage) or a member's tier that moved since the merge never lowers it; the
// verdict still supplies the summary. The reason of the row the tier came from goes with it, and urgency with an action
// tier. Under b.mu; it reports whether it changed anything.
func (b *inboxBook) liftMerged(sv *InboxItem) bool {
	if len(sv.MergedFrom) == 0 {
		return false
	}
	tier, reason, urgent := "", "", false
	if t := strings.TrimSuffix(sv.PreMergeTier, "|"); t != "" {
		tier = t
	}
	for _, ref := range sv.MergedFrom {
		m := b.items[ref.ID]
		if m == nil || m.MergedInto != sv.ID {
			continue
		}
		if tierRank(m.Tier) > tierRank(tier) {
			tier, reason = m.Tier, m.Reason
		}
		urgent = urgent || m.Urgent
	}
	changed := false
	if tierRank(tier) > tierRank(sv.Tier) {
		sv.Tier, changed = tier, true
		if reason != "" {
			sv.Reason = reason
		}
	}
	if urgent && sv.Tier == TierAction && !sv.Urgent {
		sv.Urgent, changed = true, true
	}
	return changed
}

// unmerge gives the members of survivor sv their rows back. Under b.mu. It returns the rows that changed.
func (b *inboxBook) unmerge(sv *InboxItem, now time.Time) []*InboxItem {
	out := []*InboxItem{sv}
	for _, ref := range sv.MergedFrom {
		m := b.items[ref.ID]
		if m == nil || m.MergedInto != sv.ID {
			continue
		}
		m.MergedInto = ""
		restoreMergeTitle(m) // a row that was itself a merged row's survivor: its own title comes back too
		sv.Count = max(sv.Count-m.Count, 1)
		out = append(out, m)
	}
	sv.MergedFrom = nil
	if t := sv.PreMergeTier; t != "" {
		sv.Tier = t[:len(t)-1]
		sv.PreMergeTier = ""
	}
	sv.TriagedAt = ""
	restoreMergeTitle(sv)
	b.chainAdd(sv, InboxChainEntry{Kind: ChainMerged, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.unmerged")})
	return out
}

// followMerged reads what a round brought into members into their survivors: the survivor takes the newer message,
// reopens, and is judged again. A member whose survivor is gone is let go (its row shows again). Under b.mu; it
// returns changed with the survivors added.
func (b *inboxBook) followMerged(changed []*InboxItem, now time.Time) []*InboxItem {
	for _, it := range slices.Clone(changed) {
		if it.MergedInto == "" {
			continue
		}
		sv := b.items[it.MergedInto]
		if sv == nil || sv.MergedInto != "" {
			it.MergedInto = ""
			continue
		}
		if !it.UpdatedAt.After(sv.UpdatedAt) {
			continue
		}
		b.roundWrite(sv)
		sv.UpdatedAt, sv.Preview, sv.Sender, sv.Read = it.UpdatedAt, it.Preview, it.Sender, false
		sv.Count++
		sv.DoneAt, sv.DoneBy = nil, ""
		if tierRank(it.Tier) > tierRank(sv.Tier) {
			sv.Tier, sv.Reason = it.Tier, it.Reason
		}
		if it.Context != "" {
			sv.Context = clipRunesTo(it.Context+"\n\n"+sv.Context, mergeCtxMax)
		}
		sv.TriagedAt = ""
		b.chainAdd(sv, InboxChainEntry{Kind: ChainReceived, At: it.UpdatedAt.UTC(), Count: 1, Sender: it.Sender, Via: it.Source,
			Link: firstLink(it), Text: receivedText(b.userLang(), it, 1, it.Sender)})
		if !containsItem(changed, sv) {
			changed = append(changed, sv)
		}
	}
	return changed
}

// releaseMembers lets go the members whose survivor left memory (done long ago). Under b.mu.
func (b *inboxBook) releaseMembers() []*InboxItem {
	var out []*InboxItem
	for _, it := range b.items {
		if it.MergedInto != "" && b.items[it.MergedInto] == nil {
			it.MergedInto = ""
			out = append(out, it)
		}
	}
	return out
}

type inboxMergeReply struct {
	// Item is the merged row as listed now; Members are the ids that went into it (gone from the list).
	Item    InboxItem       `json:"item"`
	Members []string        `json:"members"`
	Skipped []inboxBulkSkip `json:"skipped"`
	// Undo is the request body of POST /api/v1/inbox/merge/undo.
	Undo struct {
		ID string `json:"id"`
	} `json:"undo"`
}

// mergeInbox merges the targets: the ones gone, a member already, done, or changed since the list are skipped; fewer
// than two left merges nothing (errMergeNone).
func (s *Service) mergeInbox(ctx context.Context, targets []inboxBulkTarget, now time.Time) (inboxMergeReply, error) {
	b := &s.inbox
	out := inboxMergeReply{Members: []string{}, Skipped: []inboxBulkSkip{}}
	b.mu.Lock()
	var items []*InboxItem
	seen := map[string]bool{}
	for _, t := range targets {
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		it, ok := b.items[t.ID]
		switch {
		case !ok:
			out.Skipped = append(out.Skipped, inboxBulkSkip{t.ID, bulkSkipMissing})
		case !it.open() || it.MergedInto != "" || b.filing[t.ID] != nil && b.holdsFiling(b.filing[t.ID]):
			out.Skipped = append(out.Skipped, inboxBulkSkip{t.ID, bulkSkipChanged})
		case t.Version != "" && t.Version != inboxVersion(it):
			out.Skipped = append(out.Skipped, inboxBulkSkip{t.ID, bulkSkipChanged})
		default:
			items = append(items, it)
		}
	}
	if len(items) < 2 {
		b.mu.Unlock()
		return out, errMergeNone
	}
	before := map[string]InboxItem{}
	for _, it := range items {
		before[it.ID] = *it
		for _, ref := range it.MergedFrom {
			if m := b.items[ref.ID]; m != nil {
				before[m.ID] = *m
			}
		}
	}
	sv, took := b.merge(items, now)
	changed := append([]*InboxItem{sv}, took...)
	b.forgetListed(changed)
	b.mu.Unlock()
	failed := s.saveInboxUndo(ctx, changed, func(failed map[string]bool) {
		if len(failed) == 0 {
			return
		}
		for _, it := range changed { // all or none: a merge half written would lose rows from the list
			if b.items[it.ID] == it {
				*it = before[it.ID]
			}
		}
		b.forgetListed(changed)
	})
	if len(failed) > 0 {
		return out, errMergeFailed
	}
	b.mu.Lock()
	out.Item = sv.listed()
	for _, m := range took {
		out.Members = append(out.Members, m.ID)
	}
	b.mu.Unlock()
	out.Undo.ID = sv.ID
	s.titleMerged(ctx, sv.ID) // the model's title for the whole row, when it answers (inbox_merge_title.go)
	return out, nil
}

var (
	errMergeNone   = i18n.ErrEnglish("inbox-errors.merge_none")
	errMergeFailed = i18n.ErrEnglish("inbox-errors.merge_failed")
)

func (s *Service) unmergeInbox(ctx context.Context, id string, now time.Time) ([]InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	sv := b.items[id]
	if sv == nil || len(sv.MergedFrom) == 0 {
		b.mu.Unlock()
		return nil, errNoInboxItem
	}
	changed := b.unmerge(sv, now)
	b.forgetListed(changed)
	b.mu.Unlock()
	s.saveInbox(ctx, changed)
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]InboxItem, 0, len(changed))
	for _, it := range changed {
		out = append(out, it.listed())
	}
	return out, nil
}

func (s *Service) inboxMergeRoutes() {
	// {"items": [{"id", "version"?}]}: the newest row takes the others in.
	s.handle("POST /api/v1/inbox/merge", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Items []inboxBulkTarget `json:"items"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		switch {
		case len(in.Items) < 2:
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.merge_too_few_rows"))
			return
		case len(in.Items) > mergeMax:
			apierr.Write(w, http.StatusRequestEntityTooLarge, errBulkTooMany)
			return
		}
		out, err := s.mergeInbox(r.Context(), in.Items, time.Now())
		switch err {
		case nil:
			apierr.WriteJSON(w, out)
		case errMergeNone:
			apierr.Write(w, http.StatusConflict, err)
		default:
			apierr.Write(w, http.StatusServiceUnavailable, err)
		}
	})
	// {"id": the merged row's}: the members have their rows back.
	s.handle("POST /api/v1/inbox/merge/undo", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil || in.ID == "" {
			apierr.Write(w, http.StatusBadRequest, errNoInboxItem)
			return
		}
		changed, err := s.unmergeInbox(r.Context(), in.ID, time.Now())
		if err != nil {
			apierr.Write(w, http.StatusNotFound, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"changed": changed})
	})
}
