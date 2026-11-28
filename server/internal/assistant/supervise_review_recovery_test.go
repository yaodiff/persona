package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAcceptedResearchWithIssueOnlyCloseout(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%v", missing), func(t *testing.T) {
			st := todoTestDB(t)
			s, f := newQuietServiceOn(t, st)
			ctx := context.Background()
			deliveryOn(t, s)
			criterion := "Research findings are documented"
			raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
				"title": "Read-only research", "prompt": "Document the research findings",
				"machine": "box", "cwd": "/code/app", "readonly": true, "why": "The source is here",
				"done_when": []string{criterion},
			}}})
			if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
			todo := onlyTodo(s)
			wrong := "https://github.com/o/r/pull/4"
			s.setTaskStage(ctx, todo.ID, "review", wrong)
			s.watchDeploy(ctx, todo.ID, deployWatch{PR: wrong, Since: time.Now()})
			s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryMerged})
			f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Research references issue #4; no PR was opened.\n" +
				"<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\n" +
				"artifact: url https://github.com/o/r/issues/4\n</closeout>"})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/issues/4":
					if missing {
						http.NotFound(w, r)
					} else {
						_, _ = w.Write([]byte(`{"number":4}`))
					}
				case "/repos/o/r/pulls":
					_, _ = w.Write([]byte(`[]`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
			s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
			s.deploy.listPaths = []string{}
			_, err := acceptReview(s, todo.ID, map[string]any{
				"link":   "https://github.com/o/r/issues/4",
				"checks": []ReviewCheck{{Criterion: criterion, Met: true, Evidence: "The research findings were reviewed"}},
			})
			if err != nil {
				t.Fatalf("research with no PR was not accepted: %v", err)
			}
			got := todoNow(s, todo.ID)
			if got.DoneAt == nil || got.Supervision.State != reviewAccepted || got.PRURL != "" || len(got.PRs) != 0 {
				t.Fatalf("research retained a false PR or was not accepted: %+v", got)
			}
			if !s.doneNeedsDelivery(ctx) {
				t.Fatal("research acceptance disabled the global delivery gate")
			}
			restarted, _ := newTaskServiceOn(t, st)
			if stage := restarted.todoStages(ctx)[todo.ID]; stage.PR != "" || len(stage.PRs) != 0 || stage.Deploy != nil || stage.Delivery != nil {
				t.Fatalf("false PR delivery hold survived restart: %+v", stage)
			}
			if !s.deploy.githubDown.IsZero() {
				t.Fatal("an issue-only research task put unrelated PR checks on cooldown")
			}
		})
	}
}

func TestAcceptedRecoversActualPRFromCloseout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ref        string
		merged     bool
		status     int
		kindStatus int
		want       string
		refuse     bool
	}{
		{"full PR URL", "https://github.com/o/r/pull/5", true, 200, 200, "5", false},
		{"numbered artifact", "#5", true, 200, 200, "5", false},
		{"plain number artifact", "5", true, 200, 200, "5", false},
		{"repository-qualified artifact", "o/r#5", true, 200, 200, "5", false},
		{"recovered PR still unmerged", "https://github.com/o/r/pull/5", false, 200, 200, "5", true},
		{"recovered PR unavailable", "https://github.com/o/r/pull/5", true, 503, 200, "5", true},
		{"numbered artifact cannot be confirmed", "#5", true, 403, 403, "4", true},
		{"full PR URL cannot be confirmed", "https://github.com/o/r/pull/5", true, 403, 403, "4", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f, todo := newSupervisedServiceOn(t, st)
			ctx := context.Background()
			deliveryOn(t, s)
			s.deploy.hubBuild = (&runningHub{commit: oldHub}).build
			s.deploy.listPaths = []string{}
			// Seed the mistaken issue in the stage record only. A saved delivery link is a separate gate input.
			s.setTaskStageOn(ctx, todo.ID, "review", "https://github.com/o/r/pull/4", "", false, "")
			reply := "The issue was recorded as PR #4; the actual delivery is in the closeout.\n" +
				"<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\nartifact: pr " + tc.ref + "\n</closeout>"
			artifactReportProof(f, todo, reply, "https://github.com/o/r/pull/5")
			// The list preview does not carry the delivery artifact, and normal polling must not fetch it.
			s.taskStageSessions(ctx, DeviceSessions{MachineID: "box", Sessions: []proto.Session{
				{SessionID: "s1", Status: proto.StatusDone, LastText: "The actual delivery is in the closeout…"},
			}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/issues/4":
					_, _ = w.Write([]byte(`{"number":4}`))
				case "/repos/o/r/issues/5":
					w.WriteHeader(tc.kindStatus)
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
			var wantLinks []string
			if tc.want == "5" {
				wantLinks = []string{"https://github.com/o/r/pull/5"}
			}
			if _, links := savedArtifactURLs(t, st, todo.ID); !slices.Equal(links, wantLinks) {
				t.Fatalf("saved repository links = %v, want %v", links, wantLinks)
			}
		})
	}
}

func TestReportedTaskPRDoesNotRecoverAnotherIssue(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "<closeout status=\"done\">\n" +
		"artifact: pr https://github.com/o/r/pull/5\n</closeout>"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/issues/5" {
			_, _ = w.Write([]byte(`{"number":5}`))
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
	if pr, _, err := s.reportedTaskPR(context.Background(), todo, "https://github.com/o/r/pull/4"); pr != "" || err != nil {
		t.Fatalf("recovered another ordinary issue as a PR: %q, %v", pr, err)
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

func TestAcceptedMRWithoutAutomaticOwnershipKeepsGitHubGate(t *testing.T) {
	const mr = "https://code.example.com/team/app/merge_requests/7"
	const githubPR = "https://github.com/o/r/pull/5"
	for _, tc := range []struct {
		name, artifact, saved, wantError string
	}{
		{name: "unverified non-GitHub MR", artifact: mr},
		{name: "unverified GitHub PR", artifact: githubPR, wantError: "delivery PR ownership could not be verified"},
		{name: "non-GitHub MR cannot replace saved GitHub PR", artifact: mr, saved: githubPR, wantError: "the PR is not merged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f, todo := newSupervisedServiceOn(t, st)
			deliveryOn(t, s)
			s.deploy.forge = &fakeForge{}
			s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
			s.deploy.listPaths = []string{}
			a := todo.Agents[0]
			f.setSessions(a.MachineID, proto.Session{SessionID: a.SessionID, Cwd: "/code/app", Status: proto.StatusDone})
			f.answerNode("git.pr", func(string, json.RawMessage) (any, error) { return nil, errors.New("gh is not signed in") })
			if tc.saved != "" {
				s.setTaskStage(t.Context(), todo.ID, "review", tc.saved)
			}
			_, originalLinks := savedArtifactURLs(t, st, todo.ID)
			artifactReportProof(f, todo, "<closeout status=\"done\" merged=\"yes\" deployed=\"no\">\nartifact: pr "+tc.artifact+"\n</closeout>")
			_, err := acceptReview(s, todo.ID, nil)
			got := todoNow(s, todo.ID)
			if tc.wantError == "" {
				if err != nil || got.DoneAt == nil || got.Supervision.State != reviewAccepted {
					t.Fatalf("manual acceptance of unsupported MR failed: done=%v, err=%v", got.DoneAt, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || got.DoneAt != nil {
				t.Fatalf("expected open task and %q, got done=%v, err=%v", tc.wantError, got.DoneAt, err)
			}
			if got.PRURL != tc.saved {
				t.Fatalf("unverified artifact replaced saved PR: got %q, want %q", got.PRURL, tc.saved)
			}
			if _, links := savedArtifactURLs(t, st, todo.ID); !slices.Equal(links, originalLinks) {
				t.Fatalf("unverified artifact changed links: got %v, want %v", links, originalLinks)
			}
			f.mu.Lock()
			lookup := false
			for _, call := range f.calls {
				lookup = lookup || call.Method == "git.pr"
			}
			f.mu.Unlock()
			if lookup != (tc.artifact == githubPR) {
				t.Fatalf("git.pr lookup=%v for %s", lookup, tc.artifact)
			}
		})
	}
}

func TestAcceptedRecoversCanonicalVerifiedPR(t *testing.T) {
	const rawGitHub = "https://github.com/Example/Project/pull/7"
	const rawGitLab = "https://gitlab.com/Example/Project/merge_requests/7"
	const canonicalGitLab = "https://gitlab.com/example/project/-/merge_requests/7"
	for _, tc := range []struct{ name, raw, proof, canonical string }{
		{"GitHub canonicalized on save", rawGitHub, rawGitHub, artifactPR},
		{"GitHub URL differs from native proof spelling", rawGitHub, artifactPR, artifactPR},
		{"GitLab legacy URL differs from native proof spelling", rawGitLab, canonicalGitLab, canonicalGitLab},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f, todo := newSupervisedServiceOn(t, st)
			deliveryOn(t, s)
			s.deploy.forge = &fakeForge{merged: true}
			s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
			s.deploy.listPaths = []string{}
			artifactReportProof(f, todo, "<closeout status=\"done\" merged=\"yes\" deployed=\"no\">\nartifact: pr "+tc.raw+"\n</closeout>", tc.proof)
			_, err := acceptReview(s, todo.ID, nil)
			got := todoNow(s, todo.ID)
			if err != nil || got.DoneAt == nil || got.PRURL != tc.canonical {
				t.Fatalf("verified alias was not accepted: PR=%q, done=%v, err=%v", got.PRURL, got.DoneAt, err)
			}
			if _, links := savedArtifactURLs(t, st, todo.ID); !slices.Equal(links, []string{tc.canonical}) {
				t.Fatalf("verified canonical link was not saved: %v", links)
			}
		})
	}
}
