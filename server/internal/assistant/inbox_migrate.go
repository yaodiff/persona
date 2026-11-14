package assistant

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"

	"github.com/nwverse/persona/server/internal/connector"
)

// The inbox of a connector a plugin took over from the hub (the hub's connectors_migrate.go): its things carry the
// items' own blocks (connector.ThingExtra), so the items keep the shape the apps know (Feishu's lark / doc / moment
// block, no generic connector block), and the items the hub made itself before the move go on as the same items: a
// thread is found by the block (migratedThread), the same key the plugin uses.

// migratedConnectorID is the connector a migrated item's reply goes through: the plugin's that took Feishu over
// (the hub answers that it cannot be replied to while none did).
const migratedConnectorID = "lark"

// migratedThread is the thread of a Feishu item by its block: "chat:<feed>" ("chat:<account>:<feed>" for an account
// other than the primary one), "doc:<notice>", "moment:<post>"; "" for anything else.
func migratedThread(it *InboxItem) string {
	var buf [64]byte
	return string(appendMigratedThread(buf[:0], it))
}

func appendMigratedThread(b []byte, it *InboxItem) []byte {
	switch {
	case it.Lark != nil:
		b = append(b, "chat:"...)
		if it.Lark.AccountID > 1 {
			b = append(strconv.AppendInt(b, int64(it.Lark.AccountID), 10), ':')
		}
		return append(b, it.Lark.FeedID...)
	case it.Doc != nil:
		return append(append(b, "doc:"...), it.Doc.NoticeID...)
	case it.Moment != nil:
		return append(append(b, "moment:"...), it.Moment.PostID...)
	}
	return b
}

// migratedKey is "<source>\x00<migratedThread>", what applyMigrated finds an item's thread by, built as one string: a
// collection keys every item of the inbox.
func migratedKey(it *InboxItem) string {
	var buf [96]byte
	return string(appendMigratedKey(buf[:0], it))
}

func appendMigratedKey(b []byte, it *InboxItem) []byte {
	return appendMigratedThread(append(append(b, it.Source...), 0), it)
}

// inMigratedThread says whether an item's migratedKey is key, without making the string: openFor asks it of each item
// filed under a thread it looks up.
func inMigratedThread(it *InboxItem, key string) bool {
	var buf [96]byte
	return it.Connector == nil && string(appendMigratedKey(buf[:0], it)) == key
}

// applyMigrated is applyConnector for the things with Extra of a migrated connector, under b.mu.
func (b *inboxBook) applyMigrated(id string, things []connector.InboxThing, now time.Time) []*InboxItem {
	var changed []*InboxItem
	index := b.threads(func(it *InboxItem) (string, bool) {
		if it.Connector != nil {
			return "", false
		}
		return migratedKey(it), true
	})
	index.in = inMigratedThread
	for _, t := range things {
		source := id
		if t.Extra.Source != "" {
			source = t.Extra.Source
		}
		open, done := index.openFor(source + "\x00" + t.Thread)
		if t.Done != "" {
			if open != nil && b.settleThing(open, t, now) {
				changed = append(changed, open)
			}
			continue
		}
		if open != nil {
			wrote := false
			switch {
			case open.Last != t.Last:
				prev := seenOf(open)
				b.roundWrite(open)
				wrote = true
				fillMigrated(open, t)
				b.noteReceived(open, prev, nil)
				if t.Tier != "" {
					b.noteTriaged(open)
				}
				open.Read = false
				changed = append(changed, open)
			case blocksDiffer(open, t.Extra):
				// The same move, read further (a chat's read position) or read again (a document's rows): blocks and a
				// renamed chat, not a new move.
				b.roundWrite(open)
				wrote = true
				setBlocks(open, t.Extra)
				if where := whereOf(t.Tags); where != "" {
					open.Where = where
				}
				changed = append(changed, open)
			}
			if t.Tier != "" && (open.Tier != string(t.Tier) || open.Reason != t.Why) {
				// The connector's rules decide this one (a new move, or the rules changed since it was tiered): it is
				// tiered here, not by the model, as a generic connector item is (fillMigrated, applyConnector).
				if !wrote {
					b.roundWrite(open)
				}
				setRuleTier(open, t)
				b.noteTriaged(open)
				if !containsItem(changed, open) {
					changed = append(changed, open)
				}
			}
			index.note(open) // its blocks may be another thread's now
			if t.Read && !open.Read {
				b.roundWrite(open)
				open.Read, open.Count = true, t.Extra.Count
				if open.Tier != "" && open.Tier != TierAction { // untriaged ones are settled by triage
					b.file(open, now, "read")
				}
				if !containsItem(changed, open) {
					changed = append(changed, open)
				}
			}
			continue
		}
		if t.Read || now.Sub(t.At) > inboxWindow || done != nil && (done.Last == t.Last || !t.At.After(done.UpdatedAt)) {
			continue
		}
		itemID := t.Extra.LegacyID
		if itemID == "" || b.has(itemID) {
			sum := sha1.Sum([]byte(t.Thread + "\n" + t.Last))
			itemID = id + ":" + hex.EncodeToString(sum[:8])
		}
		it := &InboxItem{ID: itemID, Source: source, Account: t.Extra.Account, AccountName: t.Extra.AccountName, StartedAt: t.Extra.StartedAt}
		if it.StartedAt.IsZero() {
			it.StartedAt = t.At
		}
		fillMigrated(it, t)
		b.noteReceived(it, inboxSeen{}, nil)
		if t.Tier != "" {
			b.noteTriaged(it)
		}
		b.add(id, it)
		b.roundNew(it)
		index.note(it)
		changed = append(changed, it)
	}
	return changed
}

// fillMigrated sets an item from a thing with Extra; an open chat item keeps where it started. A thing whose
// connector's rules decided its tier gets it here, as a generic connector item does (fillConnectorItem): the item is
// tiered without the model, and a rule the connector sends again as the tier changes re-tiers it (applyMigrated).
func fillMigrated(it *InboxItem, t connector.InboxThing) {
	it.Kind, it.Title, it.Sender, it.Preview, it.Context = t.Kind, t.Title, t.Sender, t.Preview, t.Context
	it.UpdatedAt, it.Last, it.Rules = t.At, t.Last, t.Rules
	it.Avatar, it.Count = t.Extra.Avatar, max(t.Extra.Count, 1)
	setBlocks(it, t.Extra)
	if where := whereOf(t.Tags); where != "" {
		it.Where = where
	}
	if t.Tier != "" {
		setRuleTier(it, t)
	}
}

// whereOf is the chat tag's display name, or else the repository tag's name or value, trimmed.
func whereOf(tags []connector.Tag) string {
	for _, tag := range tags {
		if tag.Key == connector.TagChat {
			if name := strings.TrimSpace(tag.Name); name != "" {
				return name
			}
		}
	}
	for _, tag := range tags {
		if tag.Key == connector.TagRepo {
			if name := strings.TrimSpace(tag.Name); name != "" {
				return name
			}
			if value := strings.TrimSpace(tag.Value); value != "" {
				return value
			}
		}
	}
	return ""
}

func setBlocks(it *InboxItem, x *connector.ThingExtra) {
	it.Lark, it.Doc, it.Moment = thingBlocks(it, x)
}

// thingBlocks are the blocks a thing's extra carries, as setBlocks sets them on it: a chat's block keeps the item's
// start position.
func thingBlocks(it *InboxItem, x *connector.ThingExtra) (lark *InboxLark, doc *InboxDoc, moment *InboxMoment) {
	start := ""
	if it.Lark != nil {
		start = it.Lark.StartPosition
	}
	if len(x.Lark) > 0 && json.Unmarshal(x.Lark, &lark) == nil && lark != nil && start != "" {
		lark.StartPosition = start
	}
	if len(x.Doc) > 0 {
		_ = json.Unmarshal(x.Doc, &doc)
	}
	if len(x.Moment) > 0 {
		_ = json.Unmarshal(x.Moment, &moment)
	}
	return lark, doc, moment
}

// blocksDiffer: the thing's block says something else than the item's (a chat's start position aside: the item keeps
// its own), as their JSON tells. A collection asks it of every open item its things name, which mostly say the same
// again: blocks with the same values (sameBlocks) encode alike, so only the others are encoded to compare.
func blocksDiffer(it *InboxItem, x *connector.ThingExtra) bool {
	lark, doc, moment := thingBlocks(it, x)
	if sameLark(it.Lark, lark) && sameDoc(it.Doc, doc) && sameMoment(it.Moment, moment) {
		return false
	}
	enc := func(lark *InboxLark, doc *InboxDoc, moment *InboxMoment) []byte {
		b, _ := json.Marshal([]any{lark, doc, moment})
		return b
	}
	return !bytes.Equal(enc(it.Lark, it.Doc, it.Moment), enc(lark, doc, moment))
}

// sameLark, sameDoc and sameMoment say two blocks hold the same values, so their JSON is the same. They can say
// false of two whose JSON is the same all the same (a nil list and an empty one, a time on two zones' clocks, invalid
// UTF-8): blocksDiffer then compares the JSON. A field added to a block's types is compared here too
// (TestBlocksDifferAsJSON finds one left out).
func sameLark(a, b *InboxLark) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameDoc(a, b *InboxDoc) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.URL == b.URL && a.Link == b.Link && a.Type == b.Type && a.Notice == b.Notice && a.NoticeID == b.NoticeID && a.Quote == b.Quote &&
		a.Error == b.Error && sameTime(a.ReadAt, b.ReadAt) && (a.Reply == nil) == (b.Reply == nil) &&
		(a.Reply == nil || *a.Reply == *b.Reply) && sameList(a.Rows, b.Rows, sameRow)
}

func sameRow(a, b *DocRow) bool {
	return a.Ref == b.Ref && a.Table == b.Table && a.Title == b.Title && sameValues(a.Fields, b.Fields) &&
		sameList(a.Status, b.Status, func(a, b *DocStatus) bool {
			return a.Field == b.Field && a.Name == b.Name && a.Value == b.Value && sameValues(a.Options, b.Options)
		})
}

func sameMoment(a, b *InboxMoment) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.PostID == b.PostID && a.Notice == b.Notice && a.ReplyTo == b.ReplyTo && a.MyPost == b.MyPost &&
		a.PostText == b.PostText && a.Anonymous == b.Anonymous && sameValues(a.Notices, b.Notices) &&
		sameTime(a.ReadAt, b.ReadAt)
}

// sameTime: the same instant on the same zone offset, which JSON writes alike.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	_, ao := a.Zone()
	_, bo := b.Zone()
	return a.Equal(*b) && ao == bo
}

// sameValues and sameList: lists of the same elements, both nil or neither.
func sameValues[T comparable](a, b []T) bool {
	return (a == nil) == (b == nil) && slices.Equal(a, b)
}

func sameList[T any](a, b []T, same func(a, b *T) bool) bool {
	if len(a) != len(b) || (a == nil) != (b == nil) {
		return false
	}
	for i := range a {
		if !same(&a[i], &b[i]) {
			return false
		}
	}
	return true
}

// replyMigrated sends the reply (and a document's status moves) of a Feishu item through the plugin. The client id
// is the one the hub would have used itself, so a retry across the move is still dropped by the Lark gateway.
func (s *Service) replyMigrated(ctx context.Context, r inboxReplyReq) (bool, error) {
	it := r.item
	b := &s.inbox
	clientID := ""
	if it.Lark != nil {
		clientID = r.clientID
		if !validClientID(clientID) {
			clientID = namedClientID(r.id + "\n" + r.text)
		}
		b.mu.Lock()
		sent := r.once.repeated(r.live, clientID)
		b.mu.Unlock()
		if sent {
			return true, nil
		}
	}
	var changes []connector.StatusChange
	for _, ch := range r.changes {
		changes = append(changes, connector.StatusChange{Ref: ch.Ref, Field: ch.Field, Value: ch.Value})
	}
	if len(changes) > 0 && it.Doc == nil {
		return false, i18n.ErrEnglish("inbox-errors.document_only")
	}
	if err := s.connectorReply(ctx, migratedConnectorID, connector.InboxReply{Thread: migratedThread(&it), ItemID: it.ID, Last: it.Last,
		Text: r.text, ClientID: clientID, Changes: changes}, r.sendID); err != nil {
		return false, err
	}
	if clientID != "" {
		r.once.clientID = clientID
	}
	return false, nil
}

// migratedDocStatus moves a document row's status through the plugin; the item's rows come back with its next things.
func (s *Service) migratedDocStatus(ctx context.Context, it *InboxItem, ch DocChange) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	thread, itemID, last := migratedThread(it), it.ID, it.Last
	b.mu.Unlock()
	if err := s.connectorReply(ctx, migratedConnectorID, connector.InboxReply{Thread: thread, ItemID: itemID, Last: last,
		Changes: []connector.StatusChange{{Ref: ch.Ref, Field: ch.Field, Value: ch.Value}}}, ""); err != nil {
		return InboxItem{}, err
	}
	b.mu.Lock()
	out := it.listed()
	b.mu.Unlock()
	return out, nil
}

// The client ids a Feishu reply goes with (the same the plugin, and the hub before it, derive): the Lark gateway drops a
// repeat with the same id.
// clientIDNamespace is the UUIDv5 namespace of namedClientID. Fixed forever: changing it would let a retry of an
// already delivered message post again.
var clientIDNamespace = [16]byte{0xa5, 0x1e, 0x3f, 0x21, 0xe1, 0xd7, 0x4c, 0xcd, 0x89, 0xbf, 0xcb, 0x02, 0x7f, 0x8c, 0xb8, 0xdc}

// namedClientID is a UUIDv5 clientId derived from name: the same name always gives the same id, so the same
// message sent again (after a timeout, from another device, after a restart) posts once.
func namedClientID(name string) string {
	h := sha1.New()
	h.Write(clientIDNamespace[:])
	h.Write([]byte(name))
	b := h.Sum(nil)[:16]
	b[6], b[8] = b[6]&0x0f|0x50, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// validClientID reports whether s is a UUID the Lark gateway accepts as a clientId (version 1-8, RFC variant, any case).
func validClientID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if ch != '-' {
				return false
			}
		case !('0' <= ch && ch <= '9' || 'a' <= ch && ch <= 'f' || 'A' <= ch && ch <= 'F'):
			return false
		}
	}
	return '1' <= s[14] && s[14] <= '8' && strings.ContainsRune("89abAB", rune(s[19]))
}
