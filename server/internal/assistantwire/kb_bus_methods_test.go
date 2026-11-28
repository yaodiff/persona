package assistantwire_test

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
)

func TestKBBusMethodsWire(t *testing.T) {
	checkMethodWire[buswire.None, assistantwire.KBOwner](t, assistantwire.KBOwnerMethod, "kb.owner",
		`{}`, `{"name":"Example","aliases":[],"handles":[]}`)
	checkMethodWire[assistantwire.KBContextParams, assistantwire.KBContextResult](t, assistantwire.KBContextMethod, "kb.context",
		`{"chat_id":"feed-room"}`, `{"context":{"ChatID":"feed-room","ChatName":"Example room","Summary":"A note","Projects":[{"Name":"Example","Summary":""}]}}`)
	checkMethodWire[assistantwire.KBProjectsParams, assistantwire.KBProjectsResult](t, assistantwire.KBProjectsMethod, "kb.projects",
		`{"machine_id":"computer","cwd":"/code/example"}`, `{"projects":[{"slug":"example","name":"Example"}]}`)
	checkMethodWire[assistantwire.KBForget, assistantwire.KBForgotten](t, assistantwire.KBForgetMethod, "kb.forget",
		`{"source":"feed","archive_sources":["archive"]}`, `{"chats":1,"people":2,"posts":3,"items":4}`)
	checkMethodWire[assistantwire.KBPosts, assistantwire.KBPostsPutResult](t, assistantwire.KBPostsPutMethod, "kb.posts_put",
		`{"connector":"feed","sources":["feed","archive"],"posts":[{"post":{"id":"p1","text":"Example post","comment_count":1,"at":"2026-01-01T00:00:00Z"},"comments":null}],"comments":null}`,
		`{"statuses":["new","changed","","refused"]}`)
	checkMethodWire[assistantwire.KBPosts, buswire.None](t, assistantwire.KBPostsCommentsMethod, "kb.posts_comments",
		`{"connector":"feed","sources":["feed","archive"],"id":"p1","count":2,"comments":[]}`, `{}`)
	checkMethodWire[assistantwire.KBPosts, assistantwire.KBPostsPendingResult](t, assistantwire.KBPostsPendingMethod, "kb.posts_pending",
		`{"connector":"feed","sources":["feed","archive"],"comments":null,"limit":2}`,
		`{"posts":[{"id":"p1","comment_count":2,"at":"2026-01-01T00:00:00Z"}]}`)
	checkMethodWire[assistantwire.KBPosts, assistantwire.KBArchivedChatsResult](t, assistantwire.KBArchivedChatsMethod, "kb.archived_chats",
		`{"connector":"feed","sources":["feed","archive"],"comments":null,"source":"archive"}`,
		`{"chats":[{"id":"room","name":"Example room","cursor":"page-2","last_at":"2026-01-01T00:00:00Z","messages":2}]}`)
}

func TestKBBusEmptyResultsWire(t *testing.T) {
	checkJSONRoundTrip[assistantwire.KBContextResult](t, `{"context":null}`)
	checkJSONRoundTrip[assistantwire.KBProjectsResult](t, `{"projects":[]}`)
	checkJSONRoundTrip[assistantwire.KBPostsPutResult](t, `{"statuses":[]}`)
	checkJSONRoundTrip[assistantwire.KBPostsPutResult](t, `{"statuses":null}`)
	checkJSONRoundTrip[assistantwire.KBPostsPendingResult](t, `{"posts":[]}`)
	checkJSONRoundTrip[assistantwire.KBPostsPendingResult](t, `{"posts":null}`)
	checkJSONRoundTrip[assistantwire.KBArchivedChatsResult](t, `{"chats":[]}`)
	checkJSONRoundTrip[assistantwire.KBArchivedChatsResult](t, `{"chats":null}`)
}
