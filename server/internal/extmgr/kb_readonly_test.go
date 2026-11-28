package extmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

func TestKBReadOnlyBlocksExternalSourcesAndPluginWork(t *testing.T) {
	h := newTestManager(t)
	h.hello.Store(&assistantwire.Hello{KB: &assistantwire.KBSettings{ReadOnly: true}})
	ctx := context.Background()
	for _, method := range []string{assistantwire.ConnectorsArchives, assistantwire.ConnectorsChanges,
		assistantwire.ConnectorsArchiveMedia, assistantwire.ConnectorsItems, assistantwire.ConnectorsDocTitle, assistantwire.ConnectorsOwner,
		assistantwire.ConnectorsHistoryChats, assistantwire.ConnectorsHistory, assistantwire.ConnectorsHistoryAnchors} {
		_, handled, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: method})
		if !handled || !errors.Is(err, kb.ErrReadOnly) {
			t.Fatalf("%s: handled=%v err=%v", method, handled, err)
		}
	}
	posts := h.kbFor("feed")
	_, putErr := posts.PutPosts(ctx, nil)
	commentsErr := posts.PutComments(ctx, "p1", 1, nil)
	_, pendingErr := posts.PendingPosts(ctx, 1)
	_, chatsErr := posts.ArchivedChats(ctx, "feed")
	for _, result := range []struct {
		method string
		err    error
	}{
		{assistantwire.MethodKBPostsPut, putErr},
		{assistantwire.MethodKBPostsComments, commentsErr},
		{assistantwire.MethodKBPostsPending, pendingErr},
		{assistantwire.MethodKBArchivedChats, chatsErr},
	} {
		if !errors.Is(result.err, kb.ErrReadOnly) {
			t.Errorf("%s: %v", result.method, result.err)
		}
	}
}
