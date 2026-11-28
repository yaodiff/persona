package larksync

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Naming senders a large group or a departure hides.
//
// GET /open-apis/im/v1/chats/:chat_id/members gives 100 members a page. Reading a fixed 20 pages left the senders of
// larger external groups unnamed, so a chat's list is now read on demand: only while a sender of that chat is still
// unnamed, from where the last read stopped, until the sender is found or the list ends (chatMembersPagesMost, or the
// run's budget). Each page is read at most once per chat and run.
//
// A member who left the group is in no member list. Their name may still be known from before: every name the sync
// learns for a sender or a mentioned user (contact/v3, a member list, an @mention in any message, a single chat's peer)
// goes into the binding's name book (larksyncwire.SenderNamesCursor, at most senderNamesMost entries, the newest
// learned kept), and the book names a sender nothing else names now. The sender-name backfill also reads the chat
// around an unnamed sender's message for an @mention of them (mentionScanPagesMost pages each way).
//
// Other apps' bots: the only API that names an app by its app id (GET /open-apis/application/v6/applications/:app_id)
// takes a tenant token, needs an admin scope and answers only for custom apps of the same enterprise, so a user's
// sync cannot ask it. A bot is named when a message @mentions it (mentions carry its name), which the book keeps; a
// bot nothing mentions stays unnamed.

const (
	chatMembersPagesMost = 500  // 50,000 members of one chat, read only while one of its senders is unnamed
	senderNamesMost      = 5000 // names kept in the binding's name book
	mentionScanPagesMost = 4    // pages (50 messages each) read each way around an unnamed sender's message
)

// chatMemberList is one chat's member list as far as it was read this run.
type chatMemberList struct {
	names  map[string]string // open_id -> display name
	next   string            // page token of the next page
	pages  int               // pages read
	done   bool              // the list ended, was cut at chatMembersPagesMost, or cannot be read
	denied bool              // the chat-members permission is missing
	// stalled: a page failed for a passing reason (a rate limit, a broken connection) this run. The list is not
	// done, so a later run resumes at the same page; this run reads no more of it.
	stalled bool
}

// find reads chatID's members until want (an open id; "" reads the whole list) is among them, the list ends or the
// budget is spent, and says want's name. A missing permission ends the list without an error; a fatal or context
// error is returned, and so is a rate limit or a passing failure, which leaves the list stalled (not done) for this
// run.
func (l *chatMemberList) find(ctx context.Context, sess *session, chatID, want string, overBudget func() bool) (string, error) {
	if l.names == nil {
		l.names = map[string]string{}
	}
	for {
		if want != "" {
			if name := l.names[want]; name != "" {
				return name, nil
			}
		}
		if l.done || l.stalled {
			return "", nil
		}
		if l.pages >= chatMembersPagesMost {
			l.done = true
			return "", nil
		}
		if (overBudget != nil && overBudget()) || ctx.Err() != nil {
			return "", ctx.Err() // not done: a later call in a run with time left resumes here
		}
		var data struct {
			page
			Items []struct {
				MemberID string `json:"member_id"`
				Name     string `json:"name"`
			} `json:"items"`
		}
		q := map[string]string{"member_id_type": "open_id", "page_size": "100"}
		if l.next != "" {
			q["page_token"] = l.next
		}
		err := sess.getJSON(ctx, request{
			family: famIM,
			op:     "chat members",
			path:   "/open-apis/im/v1/chats/" + pathEsc(chatID) + "/members",
			query:  q,
		}, &data)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if fatal(err) {
				return "", err
			}
			if e := asSyncError(err); e != nil && (e.Kind == larksyncwire.ErrKindRateLimited || e.Kind == larksyncwire.ErrKindTransient) {
				l.stalled = true
				return "", err
			}
			l.done = true
			if e := asSyncError(err); e != nil && e.Kind == larksyncwire.ErrKindScope {
				l.denied = true
			}
			return "", nil
		}
		l.pages++
		for _, item := range data.Items {
			id, name := strings.TrimSpace(item.MemberID), strings.TrimSpace(item.Name)
			if id != "" && name != "" {
				l.names[id] = name
			}
		}
		if !data.HasMore || data.PageToken == "" {
			l.done = true
		}
		l.next = data.PageToken
	}
}

// nameBook is the binding's remembered names (larksyncwire.SenderNamesCursor): open_id or app_id -> display name,
// with when each was last learned so the oldest go first past senderNamesMost.
type nameBook struct {
	names   map[string]bookName
	changed bool
}

type bookName struct {
	Name string `json:"n"`
	At   int64  `json:"t"` // Unix seconds it was last learned
}

// parseNameBook reads a stored book; anything unreadable is an empty book (a run never fails on metadata).
func parseNameBook(raw string) *nameBook {
	b := &nameBook{names: map[string]bookName{}}
	if raw = strings.TrimSpace(raw); raw == "" {
		return b
	}
	var saved map[string]bookName
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return b
	}
	for id, n := range saved {
		if id = strings.TrimSpace(id); id != "" && strings.TrimSpace(n.Name) != "" {
			b.names[id] = bookName{Name: strings.TrimSpace(n.Name), At: n.At}
		}
	}
	return b
}

func (b *nameBook) name(id string) string {
	if b == nil {
		return ""
	}
	return b.names[id].Name
}

// learn records id's name as of now. A name already known the same is only touched once a day, so the book is not
// written again on every run.
func (b *nameBook) learn(id, name string, now time.Time) {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if b == nil || id == "" || name == "" || id == "all" {
		return
	}
	old, ok := b.names[id]
	if ok && old.Name == name && now.Unix()-old.At < int64(24*time.Hour/time.Second) {
		return
	}
	b.names[id] = bookName{Name: name, At: now.Unix()}
	b.changed = true
}

// encode is the book to store, its oldest entries dropped past senderNamesMost; "" when it is unchanged or empty.
func (b *nameBook) encode() string {
	if b == nil || !b.changed || len(b.names) == 0 {
		return ""
	}
	for len(b.names) > senderNamesMost {
		oldest, at := "", int64(0)
		for id, n := range b.names {
			if oldest == "" || n.At < at || (n.At == at && id < oldest) {
				oldest, at = id, n.At
			}
		}
		delete(b.names, oldest)
	}
	raw, err := json.Marshal(b.names)
	if err != nil {
		return ""
	}
	return string(raw)
}

// learnAll records every name of names (open id -> name).
func (b *nameBook) learnAll(names map[string]string, now time.Time) {
	for id, name := range names {
		b.learn(id, name, now)
	}
}

// nameBookNames reads a stored name book (larksyncwire.SenderNamesCursor) as open id -> name, for the sender-name
// backfill (larksyncwire.NameResolveRequest.NameBook).
func nameBookNames(raw string) map[string]string {
	out := map[string]string{}
	for id, n := range parseNameBook(raw).names {
		out[id] = n.Name
	}
	return out
}

// scanMentions reads chatID's messages around at (mentionScanPagesMost pages each way, 50 a page) for display names
// they carry (mentions, post @-nodes), into dst, stopping once every id of want is named. Errors other than fatal ones
// end the scan quietly.
func scanMentions(ctx context.Context, sess *session, chatID string, at time.Time, want []string, dst map[string]string, overBudget func() bool) error {
	named := func() bool {
		for _, id := range want {
			if dst[id] == "" {
				return false
			}
		}
		return true
	}
	if chatID == "" || at.IsZero() || named() {
		return nil
	}
	for _, sortType := range []string{"ByCreateTimeAsc", "ByCreateTimeDesc"} {
		q := map[string]string{"container_id_type": "chat", "container_id": chatID, "page_size": "50", "sort_type": sortType, "user_id_type": "open_id"}
		if sortType == "ByCreateTimeAsc" {
			q["start_time"] = unixCursor(at.Unix())
		} else {
			q["end_time"] = unixCursor(at.Unix())
		}
		tok := ""
		for range mentionScanPagesMost {
			if (overBudget != nil && overBudget()) || ctx.Err() != nil {
				return ctx.Err()
			}
			if tok != "" {
				q["page_token"] = tok
			}
			var data struct {
				page
				Items []apiMessage `json:"items"`
			}
			err := sess.getJSON(ctx, request{family: famIM, op: "mention scan", path: "/open-apis/im/v1/messages", query: q}, &data)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if fatal(err) {
					return err
				}
				break
			}
			for _, am := range data.Items {
				extractMessageNames(am, dst)
			}
			if named() {
				return nil
			}
			if !data.HasMore || data.PageToken == "" {
				break
			}
			tok = data.PageToken
		}
	}
	return nil
}
