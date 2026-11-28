package assistant

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
)

type larkMentionKey struct {
	chat, sender, body string
	minute             time.Time
}

type inboxLarkDuplicates struct {
	hidden  map[string]bool
	related map[string]InboxRelated
}

// larkDuplicates projects a single Inbox row for an unambiguous latest mention.
// The stored rows and their source-specific actions keep their original identity.
// Called under b.mu with the same visibility rules as the list.
func (b *inboxBook) larkDuplicates(cutoff time.Time, cut inboxDoneCut, all bool) inboxLarkDuplicates {
	var duplicates inboxLarkDuplicates
	if !all {
		return duplicates
	}
	type candidates struct {
		cli, other *InboxItem
		ambiguous  bool
	}
	var matches map[larkMentionKey]candidates
	for _, it := range b.items {
		if (it.Source != "larkcli" && it.Source != "larkx") || !inboxShows(it, cutoff, cut, all) {
			continue
		}
		key, ok := larkMentionMatchKey(it)
		if !ok {
			continue
		}
		if matches == nil {
			matches = make(map[larkMentionKey]candidates)
		}
		match := matches[key]
		if it.Source == "larkcli" {
			match.ambiguous = match.ambiguous || match.cli != nil
			match.cli = it
		} else {
			match.ambiguous = match.ambiguous || match.other != nil
			match.other = it
		}
		matches[key] = match
	}
	for _, match := range matches {
		cli, other := match.cli, match.other
		if match.ambiguous || cli == nil || other == nil {
			continue
		}
		// Keep a known working reply route and any separately managed work visible.
		if (cli.Connector.CanReply && !other.Connector.CanReply) ||
			(cli.status() == StatusAction && other.DoneAt == nil && other.status() != StatusAction) ||
			cli.Follow != nil ||
			cli.Handling != nil || cli.Agent != nil || cli.Suggestion != nil || cli.Parent != nil ||
			len(cli.Handoffs) != 0 || len(cli.MergedFrom) != 0 || cli.FolderBy == FolderByUser || cli.TitleBy == TitleByUser {
			continue
		}
		if duplicates.hidden == nil {
			duplicates.hidden = make(map[string]bool)
			duplicates.related = make(map[string]InboxRelated)
		}
		duplicates.hidden[cli.ID] = true
		duplicates.related[other.ID] = InboxRelated{ID: cli.ID, Title: cli.Title, Source: cli.Source}
	}
	return duplicates
}

func (d inboxLarkDuplicates) link(it *InboxItem) {
	ref, ok := d.related[it.ID]
	if ok && !slices.ContainsFunc(it.Related, func(r InboxRelated) bool { return r.ID == ref.ID }) {
		it.Related = append(it.Related, ref)
	}
}

const (
	larkCurrentMessages = "\n\nCurrent messages (new activity):\n"
	larkBackground      = "\n\nBackground history (context only, not new requests):\n"
)

var larkContextMessageLine = lazyre.New(`(?m)^\[\d{4}-\d\d-\d\dT[^\]\n]+\] `)

func larkMentionMatchKey(it *InboxItem) (larkMentionKey, bool) {
	var key larkMentionKey
	if it.Connector == nil || it.Kind != "mention" || it.Last == "" ||
		it.UpdatedAt.IsZero() || it.Title == "" || it.Sender == "" {
		return key, false
	}
	for _, tag := range it.Connector.Tags {
		if tag.Key == "chat" && tag.Value != "" && tag.Value != tag.Name && tag.Name == it.Title {
			key.chat = tag.Name
		}
		if tag.Key == "sender" && tag.Value != "" && tag.Value != tag.Name && tag.Name == it.Sender {
			key.sender = tag.Name
		}
	}
	if key.chat == "" || key.sender == "" {
		return key, false
	}
	key.minute = it.UpdatedAt.UTC().Truncate(time.Minute)
	body := it.Preview
	if it.Source == "larkcli" {
		// Legacy CLI rows only keep the first line in Preview. Read the single
		// current message and ignore older context, which can contain the same text.
		_, current, ok := strings.Cut(it.Context, larkCurrentMessages)
		if !ok {
			return larkMentionKey{}, false
		}
		current, _, _ = strings.Cut(current, larkBackground)
		header := "[" + it.UpdatedAt.Format(time.RFC3339) + "] " + it.Sender
		body, ok = strings.CutPrefix(current, header+"（@了我）: ") // i18n-ignore: stored source marker
		if !ok || larkContextMessageLine.MatchString(body) {
			return larkMentionKey{}, false
		}
		firstLine, _, _ := strings.Cut(body, "\n")
		if strings.TrimSpace(firstLine) != it.Preview {
			return larkMentionKey{}, false
		}
	} else if !strings.HasSuffix(strings.TrimSpace(it.Context), strings.TrimSpace(body)) {
		// An independently clipped preview is insufficient evidence.
		return larkMentionKey{}, false
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	first, rest, multiline := strings.Cut(strings.TrimSpace(body), "\n")
	// The CLI renders a named emoji, while the other source can report a
	// generic emoji placeholder. Limit this to the observed trailing alias.
	if text, ok := strings.CutSuffix(first, "[害羞]"); ok { // i18n-ignore: source data
		first = text + "[表情]" // i18n-ignore: source data
	}
	if utf8.RuneCountInString(first) < 24 || strings.Contains(body, "…") {
		return larkMentionKey{}, false
	}
	key.body = first
	if multiline {
		key.body += "\n" + rest
	}
	return key, true
}
