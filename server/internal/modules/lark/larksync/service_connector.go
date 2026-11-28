package larksync

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Connector returns the knowledge-base connector backed by this sync service's outbox.
func (s *Service) Connector() connectorwire.Connector { return syncConnector{s} }

// syncConnector is the knowledge base's way into what the sync's runs handed over (the outbox): its archive is
// the chat batches by sequence (the knowledge base keeps the cursor), its items the documents and minutes of the last
// week, newest revision of each, and its media a message's attachment, fetched as the binding that read it.
type syncConnector struct{ s *Service }

func (syncConnector) ID() string   { return larksyncwire.ConnectorID }
func (syncConnector) Name() string { return "Lark sync" }

func (syncConnector) Manifest() connectorwire.Manifest {
	return connectorwire.Manifest{Capabilities: []connectorwire.Capability{connectorwire.CapArchive, connectorwire.CapItems, connectorwire.CapStatus}}
}

// Changes are the chat batches after cursor (an outbox sequence, "" the first time), at most limit; next is the last
// one's sequence.
func (c syncConnector) Changes(ctx context.Context, cursor string, limit int) ([]connectorwire.ChatBatch, string, bool, error) {
	store := c.s.host.SyncStore()
	if store == nil {
		return nil, cursor, false, nil
	}
	after, _ := strconv.ParseInt(cursor, 10, 64)
	if limit < 1 {
		limit = changesMost
	}
	rows, err := store.LarkSyncOutboxAfter(ctx, larksyncwire.OutboxChat, after, limit)
	if err != nil {
		return nil, cursor, false, err
	}
	var out []connectorwire.ChatBatch
	next := cursor
	for _, r := range rows {
		next = strconv.FormatInt(r.Seq, 10)
		var cb connectorwire.ChatBatch
		if err := json.Unmarshal(r.Payload, &cb); err != nil {
			c.s.host.Logf("lark sync: outbox %d: %v", r.Seq, err)
			continue
		}
		out = append(out, cb)
	}
	return out, next, len(rows) == limit, nil
}

// ArchiveMedia fetches an attachment by its ref ("<binding key>|<message id>|<key>|<kind>") as its binding.
func (c syncConnector) ArchiveMedia(ctx context.Context, ref string) (connectorwire.Media, error) {
	key, _, ok := strings.Cut(ref, "|")
	if !ok || key == "" {
		return connectorwire.Media{}, errNoBinding
	}
	st, err := c.s.load(ctx)
	if err != nil {
		return connectorwire.Media{}, err
	}
	b := st.binding(key)
	if b == nil || !b.authorized() {
		return connectorwire.Media{}, errNoBinding
	}
	app, err := c.s.app(ctx, &st, b.BotID)
	if err != nil {
		return connectorwire.Media{}, err
	}
	reader := c.s.readerOf(c.s.runtime(key))
	if reader == nil {
		return connectorwire.Media{}, errUnavailable
	}
	binding := larksyncwire.Binding{Key: key, BotID: b.BotID, AccountID: b.AccountID, Label: b.Label, Tier: b.Tier, App: app}
	token, _ := c.s.tokenFunc(key)
	data, mime, err := reader.Media(ctx, binding, token, ref, connectorwire.ArchiveMediaMost)
	if err != nil {
		return connectorwire.Media{}, err
	}
	return connectorwire.Media{Mime: mime, Data: data}, nil
}

func (c syncConnector) ItemSource() kbwire.ItemSource { return syncItems(c) }

// syncItems are the connector's items.
type syncItems syncConnector

// Items are the newest revision of every document and minutes handed over in the last week.
func (c syncItems) Items(ctx context.Context) ([]kbwire.Item, error) {
	store := c.s.host.SyncStore()
	if store == nil {
		return nil, nil
	}
	rows, err := store.LarkSyncDocsSince(ctx, c.s.clock().Add(-docsWindow))
	if err != nil {
		return nil, err
	}
	out := make([]kbwire.Item, 0, len(rows))
	for _, r := range rows {
		var it kbwire.Item
		if err := json.Unmarshal(r.Payload, &it); err != nil {
			c.s.host.Logf("lark sync: outbox %d: %v", r.Seq, err)
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// DocTitle is not looked up: the documents come with their titles.
func (syncItems) DocTitle(context.Context, string) string { return "" }

// Status is how the sync is doing, from the settings as last read: ok while a binding reads, not configured before
// any, not signed in when every binding must be authorized again.
func (c syncConnector) Status(context.Context) connectorwire.Status {
	c.s.mu.Lock()
	st, have := c.s.last.clone(), c.s.haveLast
	c.s.mu.Unlock()
	if !have {
		var err error
		if st, err = c.s.load(context.Background()); err != nil {
			return connectorwire.Status{State: connectorwire.StateError}
		}
	}
	n, reauth := 0, 0
	var last connectorwire.Status
	for _, b := range st.bindings {
		if b.pending() || !b.Enabled {
			continue
		}
		n++
		if b.Health == larksyncwire.HealthNeedsReauth || !b.authorized() {
			reauth++
		}
		if b.LastRunAt.After(last.LastSync) {
			last.LastSync = b.LastRunAt
		}
		if b.LastIMRunAt.After(last.LastSync) {
			last.LastSync = b.LastIMRunAt
		}
	}
	switch {
	case n == 0:
		last.State = connectorwire.StateNotConfigured
	case reauth == n:
		last.State = connectorwire.StateNotSignedIn
	default:
		last.State = connectorwire.StateOK
	}
	return last
}
