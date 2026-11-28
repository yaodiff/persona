package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestRecoveredNumberedPRUsesReplyRepository(t *testing.T) {
	for _, tc := range []struct {
		saved, ref, replyRepo string
	}{
		{"4", "#5", "o/actual"},
		{"5", "#5", "o/actual"},
		{"4", "5", "o/actual"},
		{"5", "5", "o/actual"},
		// A repository-qualified artifact outranks the reply's incidental repository link.
		{"4", "o/actual#5", "o/r"},
		{"5", "o/actual#5", "o/r"},
	} {
		t.Run("saved_"+tc.saved+"/"+tc.ref, func(t *testing.T) {
			s, todo := deliveryReviewServiceOn(t, &fakeForge{}, nil, false, repositorystore.New(todoTestDB(t)))
			f := s.hub.(*fakeHub)
			ctx := context.Background()
			s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/"+tc.saved)
			f.setItems(append(artifactCreationItems(todo, "https://github.com/o/actual/pull/5"),
				proto.Item{Role: "assistant", Kind: proto.KindText, TS: artifactTurnAt(todo), Text: "Delivery repository: https://github.com/" + tc.replyRepo + "/issues/5\n" +
					"<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\nartifact: pr " + tc.ref + "\n</closeout>"})...)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/issues/5":
					_, _ = w.Write([]byte(`{"number":5,"pull_request":{}}`))
				case "/repos/o/r/pulls/5":
					_, _ = w.Write([]byte(fmt.Sprintf(`{"number":5,"merged":true,"merge_commit_sha":%q}`, mergeSHA)))
				case "/repos/o/actual/issues/5":
					_, _ = w.Write([]byte(`{"number":5,"pull_request":{}}`))
				case "/repos/o/actual/pulls/5":
					_, _ = w.Write([]byte(`{"number":5,"merged":false}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			s.deploy.forge = githubForge{base: server.URL, client: server.Client()}

			_, err := acceptReview(s, todo.ID, nil)
			if err == nil || !strings.Contains(err.Error(), "not merged") {
				got := todoNow(s, todo.ID)
				t.Fatalf("accepted the wrong repository's merged PR: err=%v, task PR=%s, done=%v", err, got.PRURL, got.DoneAt != nil)
			}
			if got := todoNow(s, todo.ID); got.DoneAt != nil || got.PRURL != "https://github.com/o/actual/pull/5" {
				t.Fatalf("recovery did not keep the actual delivery open: PR=%s, done=%v", got.PRURL, got.DoneAt != nil)
			}
		})
	}
}
