package extmgr

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
)

// A connector's program writes a feed's public posts into the knowledge base's post archive and reads which posts'
// comments to read again (connector.KnowledgeBase, docs/assistant/knowledge.md "Pipeline", External posts). The archive is the knowledge
// base's, the assistant's: the hub checks the connector is enabled with "Add to knowledge base" on and passes each call on
// with the connector's names (kb.posts_*, kb.archived_chats). If the assistant is unavailable, the call returns
// assistant_offline and the program can try again next round.

// kbFor is the knowledge base as connector id's program sees it.
func (h *Manager) kbFor(id string) connector.KnowledgeBase { return kbPosts{h: h, id: id} }

type kbPosts struct {
	h  *Manager
	id string
}

// callKBPosts passes on a program's call while its connector is allowed to use the knowledge base.
func callKBPosts[R any](ctx context.Context, k kbPosts, method buswire.Method[assistantwire.KBPosts, R], p assistantwire.KBPosts) (R, error) {
	k.h.kbPostsMu.RLock()
	defer k.h.kbPostsMu.RUnlock()
	var zero R
	if k.h.kbReadOnly() {
		return zero, kb.ErrReadOnly
	}
	if k.h.kbOff() {
		return zero, errKBOff
	}
	if k.h.connectors != nil && (!k.h.connectors.Enabled(k.id) || !k.h.connectors.Feeds(k.id, connector.FeedArchive)) {
		return zero, errArchiveOff
	}
	p.Connector, p.Sources = k.id, k.sources()
	return buswire.Call(ctx, k.h.kbCaller(), method, p)
}

// sources are the names the connector's conversations are filed under: its id, then its manifest's ArchiveSources.
func (k kbPosts) sources() []string {
	out := []string{k.id}
	if c := k.h.connectorByID(k.id); c != nil {
		out = append(out, c.Manifest().ArchiveSources...)
	}
	return out
}

func (k kbPosts) PutPosts(ctx context.Context, posts []connector.PostPut) ([]string, error) {
	if len(posts) > connector.PostsPerPut {
		return nil, errors.New("too many posts in one call")
	}
	out, err := callKBPosts(ctx, k, assistantwire.KBPostsPutMethod, assistantwire.KBPosts{Posts: posts})
	return out.Statuses, err
}

func (k kbPosts) PutComments(ctx context.Context, id string, count int, comments []connector.PostComment) error {
	if comments == nil {
		comments = []connector.PostComment{}
	}
	_, err := callKBPosts(ctx, k, assistantwire.KBPostsCommentsMethod, assistantwire.KBPosts{ID: id, Count: count, Comments: comments})
	return err
}

func (k kbPosts) PendingPosts(ctx context.Context, limit int) ([]connector.PendingPost, error) {
	out, err := callKBPosts(ctx, k, assistantwire.KBPostsPendingMethod, assistantwire.KBPosts{Limit: limit})
	return out.Posts, err
}

func (k kbPosts) ArchivedChats(ctx context.Context, source string) ([]connector.ArchivedChat, error) {
	out, err := callKBPosts(ctx, k, assistantwire.KBArchivedChatsMethod, assistantwire.KBPosts{Source: source})
	return out.Chats, err
}
