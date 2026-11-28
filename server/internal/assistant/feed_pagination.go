package assistant

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

var errFeedCursor = errors.New("invalid feed cursor")

type feedCursor struct {
	Version      int       `json:"version"`
	Conversation string    `json:"conversation"`
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`
	AfterAt      time.Time `json:"after_at"`
	AfterID      string    `json:"after_id"`
}

func parseFeedCursor(raw, conversation string) (feedCursor, error) {
	var c feedCursor
	if len(raw) > 8192 {
		return c, errFeedCursor
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(body, &c) != nil || c.Version != 1 || c.Conversation != conversation ||
		c.Until.IsZero() || c.AfterAt.IsZero() || c.AfterID == "" || c.AfterAt.After(c.Until) || c.Since.After(c.Until) {
		return feedCursor{}, errFeedCursor
	}
	return c, nil
}

// feedPageStore is optional so legacy Store implementations retain their original
// Feed behavior. Explicit pagination needs a store with this bounded query.
type feedPageStore interface {
	FeedPage(context.Context, string, time.Time, time.Time, time.Time, string, int) ([]db.FeedRow, error)
}

func inFeedWindow(it FeedItem, c feedCursor) bool {
	within := func(at *time.Time) bool { return at != nil && at.After(c.Since) && !at.After(c.Until) }
	return !it.At.After(c.Until) && (it.At.After(c.Since) || it.Kind == feedMessage &&
		(within(cardSentAt(it)) || within(cardChangedAt(it))))
}

// pagedFeed preserves since and until across every page. The cursor advances by
// (at,id), never by since: old updates may fill an entire first page.
func (s *Service) pagedFeed(ctx context.Context, conversation string, since time.Time, cursor string) ([]FeedItem, string, error) {
	c := feedCursor{Version: 1, Conversation: conversation, Since: since, Until: decisionNow()}
	if cursor != "" {
		var err error
		c, err = parseFeedCursor(cursor, conversation)
		if err != nil {
			return nil, "", err
		}
	}
	var items []FeedItem
	if st := s.store(); st != nil {
		ps, ok := st.(feedPageStore)
		if !ok {
			return nil, "", errors.New("feed pagination requires a compatible store")
		}
		rows, err := ps.FeedPage(ctx, conversation, c.Since, c.Until, c.AfterAt, c.AfterID, feedPage+1)
		if err != nil {
			return nil, "", err
		}
		for _, row := range rows {
			items = append(items, FeedItem{ID: row.ID, Conversation: row.Conversation, At: row.At, Kind: row.Kind, Ref: row.Ref, Body: row.Body})
		}
	} else {
		s.tasks.mu.Lock()
		for _, it := range s.tasks.feed[conversation] {
			after := it.At.After(c.AfterAt) || it.At.Equal(c.AfterAt) && it.ID > c.AfterID
			if after && inFeedWindow(it, c) {
				items = append(items, it)
			}
		}
		s.tasks.mu.Unlock()
		slices.SortFunc(items, func(a, b FeedItem) int { return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.ID, b.ID)) })
	}
	next := ""
	if len(items) > feedPage {
		items = items[:feedPage]
		last := items[len(items)-1]
		c.AfterAt, c.AfterID = last.At, last.ID
		raw, _ := json.Marshal(c)
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	for i, item := range items {
		items[i] = readTaskNotice(item)
	}
	s.settleCards(ctx, items)
	s.fileCards(ctx, items)
	return items, next, nil
}
