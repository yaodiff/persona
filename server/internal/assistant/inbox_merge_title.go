package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// A merged row's title (#7229, docs/assistant/inbox.md "Merge rows"): once the user merges rows, the small-task model
// writes one title for the row from every row's title and summary, in the background. Until it answers, and when it
// cannot, the row keeps the newest row's title. A title the user wrote is never replaced; one the handling conversation
// wrote is, by a merge. The title is the model's (TitleByMerged), so grouping leaves it alone; Undo puts the old one back.

const mergeTitleWait = 60 * time.Second

// noteMergeTitle remembers the title a survivor had before the first merge that retitled it. Under b.mu.
func noteMergeTitle(sv *InboxItem) {
	if sv.MergeTitle == "" {
		sv.PreMergeTitle, sv.PreMergeTitleBy = sv.Title, sv.TitleBy
	}
}

// restoreMergeTitle puts a survivor's own title back on Undo, unless someone changed it since the model's. Under b.mu.
func restoreMergeTitle(sv *InboxItem) {
	if sv.MergeTitle != "" && sv.Title == sv.MergeTitle && sv.TitleBy == TitleByMerged {
		sv.Title, sv.TitleBy = sv.PreMergeTitle, sv.PreMergeTitleBy
	}
	sv.PreMergeTitle, sv.PreMergeTitleBy, sv.MergeTitle = "", "", ""
}

// titleMerged starts the title for survivor id in the background (inboxBook.titling waits for it in tests).
func (s *Service) titleMerged(ctx context.Context, id string) {
	b := &s.inbox
	b.titling.Add(1)
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer b.titling.Done()
		s.rewriteMergedTitle(ctx, id)
	}()
}

// rewriteMergedTitle asks the model for the merged row's title and writes it when the row is still the same merge.
func (s *Service) rewriteMergedTitle(ctx context.Context, id string) {
	b := &s.inbox
	b.mu.Lock()
	sv := b.items[id]
	if sv == nil || len(sv.MergedFrom) == 0 || sv.TitleBy == TitleByUser {
		b.mu.Unlock()
		return
	}
	merged := len(sv.MergedFrom)
	in := prompts.InboxMergeTitleInput{}
	rows := []*InboxItem{sv}
	for _, ref := range sv.MergedFrom {
		if m := b.items[ref.ID]; m != nil && m.MergedInto == sv.ID {
			rows = append(rows, m)
		}
	}
	for _, it := range rows {
		in.Items = append(in.Items, prompts.InboxFolderItem{ID: it.ID, Kind: it.Kind, Source: b.sourceName(it.Source), Title: oneLine(it.Title),
			Sender: oneLine(it.Sender), Summary: oneLine(it.Summary), Preview: clip(oneLine(it.Preview), 200)})
	}
	b.mu.Unlock()
	hello := s.hello(ctx)
	system := prompts.RenderInboxMergeTitle(prompts.InboxMergeTitle{Owner: hello.Owner, Language: answerLanguage(hello.Locale)})
	var out struct {
		Title string `json:"title"`
	}
	cctx, cancel := context.WithTimeout(ctx, mergeTitleWait)
	err := s.inboxModelJSON(cctx, purposeTriage, system, prompts.RenderInboxMergeTitleInput(in), 0, &out)
	cancel()
	title := clip(oneLine(strings.TrimSpace(out.Title)), 40)
	if err != nil || title == "" {
		if err != nil {
			s.logf("inbox: merge title %s: %v", id, err)
		}
		return
	}
	b.mu.Lock()
	// Gone, undone, merged with others since, or retitled by the user meanwhile: the answer is about another row.
	if b.items[id] != sv || len(sv.MergedFrom) != merged || sv.TitleBy == TitleByUser || title == sv.Title {
		b.mu.Unlock()
		return
	}
	w := beginWrite(sv)
	noteMergeTitle(sv)
	sv.Title, sv.TitleBy, sv.MergeTitle = title, TitleByMerged, title
	b.settle(w)
	b.forgetListed([]*InboxItem{sv})
	b.mu.Unlock()
	s.saveWrites(ctx, []*inboxWrite{w}, "its merged title")
}
