package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAcceptedRecoversActualPRFromCloseout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ref    string
		merged bool
		status int
		want   string
		refuse bool
	}{
		{"full PR URL", "https://github.com/o/r/pull/5", true, 200, "5", false},
		{"numbered artifact", "#5", true, 200, "5", false},
		{"plain number artifact", "5", true, 200, "5", false},
		{"repository-qualified artifact", "o/r#5", true, 200, "5", false},
		{"recovered PR still unmerged", "https://github.com/o/r/pull/5", false, 200, "5", true},
		{"recovered PR unavailable", "https://github.com/o/r/pull/5", true, 503, "5", true},
		{"numbered artifact cannot be confirmed", "#5", true, 403, "4", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, todo := deliveryReviewService(t, &fakeForge{}, &runningHub{commit: oldHub}, false)
			ctx := context.Background()
			s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/4")
			f := s.hub.(*fakeHub)
			reply := "The issue was recorded as PR #4; the actual delivery is in the closeout.\n" +
				"<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\nartifact: pr " + tc.ref + "\n</closeout>"
			f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: reply})
			// The list preview does not carry the delivery artifact, and normal polling must not fetch it.
			s.taskStageSessions(ctx, DeviceSessions{MachineID: "box", Sessions: []proto.Session{
				{SessionID: "s1", Status: proto.StatusDone, LastText: "The actual delivery is in the closeout…"},
			}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/issues/4":
					_, _ = w.Write([]byte(`{"number":4}`))
				case "/repos/o/r/issues/5":
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"number":5,"pull_request":{}}`))
				case "/repos/o/r/pulls/5":
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprintf(w, `{"number":5,"merged":%v,"merge_commit_sha":"%s"}`, tc.merged, mergeSHA)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
			result, err := acceptReview(s, todo.ID, nil)
			if (err != nil) != tc.refuse {
				t.Fatalf("accepted=%v, want refusal=%v: %s %v", err == nil, tc.refuse, result, err)
			}
			got := todoNow(s, todo.ID)
			if got.PRURL != "https://github.com/o/r/pull/"+tc.want {
				t.Fatalf("delivery PR = %s, want %s", got.PRURL, tc.want)
			}
			if !tc.refuse && (got.DoneAt == nil || got.Supervision.State != reviewAccepted) {
				t.Fatalf("recovered PR was not accepted: %+v", got)
			}
			if tc.refuse && got.DoneAt != nil {
				t.Fatal("recovery bypassed a real PR's delivery gate")
			}
			if tc.want == "5" && slices.Contains(got.PRs, "https://github.com/o/r/pull/4") {
				t.Fatal("false issue record survived in PR history")
			}
		})
	}
}

func TestAcceptedReviewPRLinkOverridesFalseRecord(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprint(merged), func(t *testing.T) {
			s, todo := deliveryReviewService(t, &fakeForge{}, &runningHub{commit: oldHub}, false)
			s.setTaskStage(context.Background(), todo.ID, "review", "https://github.com/o/r/pull/4")
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/repos/o/r/pulls/5" {
					_, _ = fmt.Fprintf(w, `{"merged":%v,"merge_commit_sha":"%s"}`, merged, mergeSHA)
				} else {
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
			_, err := acceptReview(s, todo.ID, map[string]any{"link": "https://github.com/o/r/pull/5"})
			if merged && err != nil || !merged && (err == nil || !strings.Contains(err.Error(), "not merged")) {
				t.Fatalf("review link was not used: %v", err)
			}
			for _, path := range paths {
				if strings.HasSuffix(path, "/pulls/4") {
					t.Fatal("the false recorded PR overrode the explicit review link")
				}
			}
		})
	}
}
