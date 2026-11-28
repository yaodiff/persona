package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

const researchNoPRCloseout = "<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\n" +
	"artifact: doc docs/research.md\nartifact: url https://github.com/o/r/issues/4\n" +
	"verified: Research findings checked against the original criteria.\n</closeout>"

const researchMarkdown = "### Persona work report\n\n- Status: done\n- Merged: n/a\n- Deployed: n/a\n" +
	"- Artifact: doc docs/research.md\n- Verified: Research findings checked against the original criteria.\n\n---"

const relatedMemoryFooter = "\n<oai-mem-citation>\n<citation_entries>\n" +
	"notes.md:1-2|note=[related work https://github.com/o/r/pull/10]\n" +
	"</citation_entries>\n</oai-mem-citation>"

func researchDeliveryTask(t *testing.T) (*Service, *fakeHub, Todo, string) {
	t.Helper()
	st := todoTestDB(t)
	s, f := newQuietServiceOn(t, st)
	deliveryOn(t, s)
	criterion := "Research findings are documented"
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Read-only research", "prompt": "Document the research findings",
		"machine": "box", "cwd": "/code/app", "readonly": true, "why": "The source is here",
		"done_when": []string{criterion},
	}}})
	if _, err := s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the research agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	return s, f, onlyTodo(s), criterion
}

func TestResearchReferencesAreNotDeliveryPRs(t *testing.T) {
	for _, text := range []string{
		"Related work: https://github.com/o/r/pull/9 and https://github.com/o/r/pull/10\n" + researchNoPRCloseout,
		"Related work: https://github.com/o/r/pull/9\n" + researchMarkdown,
		"Related PR #9 is background for https://github.com/o/r/issues/4\n" + researchNoPRCloseout,
		"Background: https://github.com/o/r/pull/9\n" + researchNoPRCloseout,
		"PR #9 is related work, not this research's delivery.\n" + researchNoPRCloseout,
	} {
		t.Run(text, func(t *testing.T) {
			s, _, todo, _ := researchDeliveryTask(t)
			s.deploy.forge = &fakeForge{}
			s.advanceTaskReply(context.Background(), todo, "box", todo.Agents[0].SessionID, text)
			if got := todoNow(s, todo.ID); got.PRURL != "" || len(got.PRs) != 0 {
				t.Fatalf("a research reference became a delivery PR: %+v", got)
			}
		})
	}
}

func TestAcceptedResearchDropsUnownedPRReferences(t *testing.T) {
	for _, current := range []string{"4", "9", "10"} {
		t.Run("recorded_"+current, func(t *testing.T) {
			s, f, todo, criterion := researchDeliveryTask(t)
			ctx := context.Background()
			for _, n := range []string{"9", "10", "4", current} {
				s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/"+n)
			}
			wrong := "https://github.com/o/r/pull/" + current
			if err := s.applyPRStatus(ctx, todoNow(s, todo.ID), TodoPRStatus{URL: wrong, State: "open"}); err != nil {
				t.Fatal(err)
			}
			s.watchDeploy(ctx, todo.ID, deployWatch{PR: wrong, Since: time.Now()})
			s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryMerged})
			report := researchNoPRCloseout
			if current == "9" {
				report = researchMarkdown
			}
			if current == "10" {
				report += relatedMemoryFooter
			}
			f.setItems(
				proto.Item{Role: "user", Kind: proto.KindText, Text: todo.Notes},
				proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Research references " +
					"https://github.com/o/r/pull/9 and https://github.com/o/r/pull/10; neither is our delivery.\n" +
					report},
			)
			var checkedRelated atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/issues/4":
					_, _ = w.Write([]byte(`{"number":4}`))
				case "/repos/o/r/pulls/9", "/repos/o/r/pulls/10":
					checkedRelated.Store(true)
					_, _ = w.Write([]byte(`{"merged":false}`))
				case "/repos/o/r/issues/9", "/repos/o/r/issues/10":
					_, _ = w.Write([]byte(`{"pull_request":{}}`))
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
				"checks": []ReviewCheck{{Criterion: criterion, Met: true, Evidence: "The research document was reviewed"}},
			})
			if err != nil {
				t.Fatalf("unowned PR references blocked research acceptance: %v", err)
			}
			got := todoNow(s, todo.ID)
			if got.DoneAt == nil || got.Supervision.State != reviewAccepted || got.PRURL != "" || len(got.PRs) != 0 || got.PRStatus != nil {
				t.Fatalf("research kept unowned delivery references: %+v", got)
			}
			if checkedRelated.Load() {
				t.Fatal("an ordinary research reference was checked as a delivery PR")
			}
			stage := s.todoStages(ctx)[todo.ID]
			if stage.PR != "" || len(stage.PRs) != 0 || stage.Deploy != nil || stage.Delivery != nil {
				t.Fatalf("unowned delivery hold survived acceptance: %+v", stage)
			}
			restarted, _ := newTaskServiceOn(t, s.store())
			if saved := restarted.todoStages(ctx)[todo.ID]; saved.PR != "" || len(saved.PRs) != 0 ||
				saved.Deploy != nil || saved.Delivery != nil || saved.PRStatus != nil {
				t.Fatalf("unowned PR facts survived restart: %+v", saved)
			}
			if !s.doneNeedsDelivery(ctx) {
				t.Fatal("research acceptance disabled the delivery gate")
			}
		})
	}
}

func TestNoPRCloseoutCannotDiscardDeliveryWithoutOwnership(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  proto.Item
		text string
	}{
		{"missing applicability", proto.Item{}, "<closeout status=\"done\">\nartifact: doc docs/research.md\n</closeout>"},
		{"duplicate invalid applicability", proto.Item{}, strings.Replace(researchNoPRCloseout, `merged="n/a"`, `merged="n/a" merged="bogus"`, 1)},
		{"duplicate applicability", proto.Item{}, strings.Replace(researchNoPRCloseout, `deployed="n/a"`, `deployed="n/a" deployed="n/a"`, 1)},
		{"work not done", proto.Item{}, strings.Replace(researchNoPRCloseout, `status="done"`, `status="not_done"`, 1)},
		{"empty record", proto.Item{}, "<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\n</closeout>"},
		{"empty PR artifact", proto.Item{}, strings.Replace(researchNoPRCloseout, "</closeout>", "artifact: pr\n</closeout>", 1)},
		{"PR beyond artifact preview", proto.Item{}, "<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\n" +
			strings.Repeat("artifact: doc docs/research.md\n", 5) + "artifact: pr https://github.com/o/r/pull/5\n</closeout>"},
		{"peer evidence", proto.Item{Role: "user", Kind: proto.KindText, From: "another-agent", Text: "Unrelated research"}, researchNoPRCloseout},
		{"different request", proto.Item{Role: "user", Kind: proto.KindText, Text: "Research an unrelated repository"}, researchNoPRCloseout},
		{"injected evidence", proto.Item{Role: "user", Kind: proto.KindText, Text: "<persona-side-conversation/> Research this instead"}, researchNoPRCloseout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, todo := deliveryReviewService(t, &fakeForge{}, nil, false)
			s.hub.(*fakeHub).setItems(tc.ask, proto.Item{Role: "assistant", Kind: proto.KindText, Text: tc.text})
			if _, err := acceptReview(s, todo.ID, nil); err == nil {
				t.Fatalf("unowned or incomplete evidence bypassed a real delivery: %v", err)
			}
			if got := todoNow(s, todo.ID); got.DoneAt != nil || got.PRURL != "https://github.com/o/r/pull/9" {
				t.Fatalf("unowned evidence changed the delivery: %+v", got)
			}
		})
	}
}

func TestSeveralDeliveryArtifactsCannotHideAnUnmergedPR(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	s.deploy.forge = &fakeForge{}
	f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\n" +
		"artifact: pr https://github.com/o/r/pull/5\nartifact: pr https://github.com/o/r/pull/6\n</closeout>"})
	if _, _, err := s.reportedTaskPR(context.Background(), todo, "https://github.com/o/r/pull/4"); err == nil {
		t.Fatal("several explicit deliveries were silently reduced to one PR")
	}
}

func TestReportedPRFromActiveAgentsRequiresUnambiguousOwnership(t *testing.T) {
	pr := func(n string) string {
		return "<closeout status=\"done\" merged=\"no\" deployed=\"no\">\n" +
			"artifact: pr https://github.com/o/r/pull/" + n + "\n</closeout>"
	}
	for _, tc := range []struct {
		name    string
		replies []string
		want    string
		noPR    bool
		err     bool
	}{
		{"different deliveries", []string{pr("5"), pr("6")}, "", false, true},
		{"same delivery", []string{pr("5"), pr("5")}, "https://github.com/o/r/pull/5", false, false},
		{"one agent still has a PR", []string{researchNoPRCloseout, pr("5")}, "https://github.com/o/r/pull/5", false, false},
		{"all agents have no PR", []string{researchNoPRCloseout, researchNoPRCloseout}, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedServiceOn(t, repositorystore.New(todoTestDB(t)))
			s.deploy.forge = &fakeForge{}
			other := todo.Agents[0]
			other.SessionID = "s2"
			if err := s.linkTodoAgent(t.Context(), todo.ID, other.MachineID, other.SessionID, other.Title, other.Cwd, other.Agent); err != nil {
				t.Fatal(err)
			}
			todo = todoNow(s, todo.ID)
			f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
				var args struct {
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
				index := 1
				if args.SessionID == other.SessionID {
					index = 0
				}
				items := append(artifactCreationItems(todo, "https://github.com/o/r/pull/5", "https://github.com/o/r/pull/6"),
					proto.Item{Role: "assistant", Kind: proto.KindText, Text: tc.replies[index], TS: artifactTurnAt(todo)})
				return map[string]any{"items": items}, nil
			})
			got, noPR, err := s.reportedTaskPR(context.Background(), todo, "")
			if got != tc.want || noPR != tc.noPR || (err != nil) != tc.err {
				t.Fatalf("multi-agent delivery = %q, noPR=%v, %v; want %q, noPR=%v, error=%v",
					got, noPR, err, tc.want, tc.noPR, tc.err)
			}
		})
	}
}

func TestUnknownPRHistoryRequiresExplicitDeliveryEvidence(t *testing.T) {
	s, todo := deliveryReviewService(t, &fakeForge{}, nil, false)
	ctx := context.Background()
	s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/4")
	s.hub.(*fakeHub).setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "A related PR was mentioned earlier."})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/issues/4" {
			_, _ = w.Write([]byte(`{"number":4}`))
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
	for i := 0; i < 2; i++ {
		if _, err := acceptReview(s, todo.ID, nil); err == nil || !strings.Contains(err.Error(), "delivery ownership") {
			t.Fatalf("unattributed history was silently promoted or discarded: %v", err)
		}
		if got := todoNow(s, todo.ID); got.DoneAt != nil || got.PRURL != "https://github.com/o/r/pull/4" {
			t.Fatalf("uncertain delivery state was lost: %+v", got)
		}
	}
}

func TestReportedPRRequiresTaskOwnedCloseout(t *testing.T) {
	pr := "https://github.com/o/r/pull/5"
	explicit := "<closeout status=\"done\" merged=\"no\" deployed=\"no\">\nartifact: pr " + pr + "\n</closeout>"
	markdown := strings.Replace(researchMarkdown, "doc docs/research.md", "pr "+pr, 1)
	for _, tc := range []struct {
		name  string
		items []proto.Item
		want  string
	}{
		{"indented Markdown example", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "    " + markdown}}, ""},
		{"own final Markdown", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: markdown + relatedMemoryFooter}}, pr},
		{"clipped Markdown", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: markdown, FullSize: 10000}}, ""},
		{"Markdown written by user", []proto.Item{{Role: "user", Kind: proto.KindText, Text: markdown}, {Role: "assistant", Kind: proto.KindText, Text: "That is a prior result."}}, ""},
		{"Markdown from another turn", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: markdown}, {Role: "user", Kind: proto.KindText, Text: "Review a different note."}, {Role: "assistant", Kind: proto.KindText, Text: "Still checking."}}, ""},
		{"ordinary related link", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "Related work: " + pr}}, ""},
		{"document artifact", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "<closeout status=\"done\">\nartifact: doc " + pr + "\n</closeout>"}}, ""},
		{"URL artifact", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "<closeout status=\"done\">\nartifact: url " + pr + "\n</closeout>"}}, ""},
		{"fenced example", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "Example:\n```text\n" + explicit + "\n```"}}, ""},
		{"tilde-fenced example", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "Example:\n~~~text\n" + explicit + "\n~~~"}}, ""},
		{"quoted example", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "> " + strings.ReplaceAll(explicit, "\n", "\n> ")}}, ""},
		{"earlier in answer", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: explicit + "\nThat was another task's example."}}, ""},
		{"unterminated closeout", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: strings.TrimSuffix(explicit, "</closeout>")}}, ""},
		{"artifact contains an incidental URL", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "<closeout status=\"done\">\nartifact: pr see related " + pr + "\n</closeout>"}}, ""},
		{"artifact annotation", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "<closeout status=\"done\">\nartifact: pr " + pr + " (related work)\n</closeout>"}}, ""},
		{"artifact in user text", []proto.Item{
			{Role: "user", Kind: proto.KindText, Text: explicit},
			{Role: "assistant", Kind: proto.KindText, Text: "That is a reference, not this task's delivery."},
		}, ""},
		{"unrelated request", []proto.Item{
			{Role: "user", Kind: proto.KindText, Text: "Review an unrelated repository's changes"},
			{Role: "assistant", Kind: proto.KindText, Text: explicit},
		}, ""},
		{"before agent linked", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: explicit, TS: time.Unix(1, 0)}}, ""},
		{"clipped PR closeout", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: explicit, FullSize: 10000}}, ""},
		{"clipped no-PR closeout", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: researchNoPRCloseout, FullSize: 10000}}, ""},
		{"earlier turn", []proto.Item{
			{Role: "assistant", Kind: proto.KindText, Text: explicit},
			{Role: "user", Kind: proto.KindText, Text: "Document the research findings"},
			{Role: "assistant", Kind: proto.KindText, Text: researchNoPRCloseout},
		}, ""},
		{"peer turn", []proto.Item{
			{Role: "user", Kind: proto.KindText, From: "another-agent", Text: "Review my PR"},
			{Role: "assistant", Kind: proto.KindText, Text: explicit},
		}, ""},
		{"injected turn", []proto.Item{
			{Role: "user", Kind: proto.KindText, Text: "<persona-side-conversation/> Review this PR"},
			{Role: "assistant", Kind: proto.KindText, Text: explicit},
		}, ""},
		{"latest own artifact", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "Related: https://github.com/o/r/pull/9\n" + explicit}}, pr},
		{"own artifact before citation metadata", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: explicit + relatedMemoryFooter}}, pr},
		{"citation metadata only", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "Research is complete.\n<oai-mem-citation>\n" +
			explicit + "\n</oai-mem-citation>"}}, ""},
		{"indented artifact in own record", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: strings.Replace(explicit, "\nartifact:", "\n    artifact:", 1)}}, pr},
		{"inline tag mentioned in own record", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: strings.Replace(explicit, "\nartifact:", "\ndid: Fixed the <closeout> reader.\nartifact:", 1)}}, pr},
		{"own artifact after example", []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "Example:\n````text\n" +
			strings.ReplaceAll(explicit, "/pull/5", "/pull/9") + "\n````\nOur result:\n" + explicit}}, pr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedServiceOn(t, repositorystore.New(todoTestDB(t)))
			s.deploy.forge = &fakeForge{}
			for i := range tc.items {
				if tc.items[i].TS.IsZero() {
					tc.items[i].TS = artifactTurnAt(todo)
				}
			}
			f.setItems(append(artifactCreationItems(todo, pr), tc.items...)...)
			got, noPR, err := s.reportedTaskPR(context.Background(), todo, "https://github.com/o/r/pull/4")
			if got != tc.want || noPR || tc.want != "" && err != nil {
				t.Fatalf("delivery recovery = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestExplicitUnmergedDeliveryStillBlocksWithRelatedReferences(t *testing.T) {
	for _, tc := range []struct {
		recorded string
		mirrored bool
	}{{"", false}, {"4", false}, {"9", false}, {"4", true}, {"9", true}} {
		name := "recorded_" + tc.recorded
		if tc.mirrored {
			name += "_mirrored"
		}
		recorded := tc.recorded
		t.Run(name, func(t *testing.T) {
			s, f, todo := newSupervisedServiceOn(t, repositorystore.New(todoTestDB(t)))
			ctx := context.Background()
			deliveryOn(t, s)
			if recorded != "" {
				ref := "https://github.com/o/r/pull/" + recorded
				s.setTaskStage(ctx, todo.ID, "review", ref)
				// The legacy stage still points to an incidental reference. Only the reported PR is delivery. Mirrored
				// keeps the default-role link setTaskStage saved for it, which is still only the stage's own record.
				if !tc.mirrored {
					setTestTodoLinkRole(t, s, todo.ID, ref, repository.LinkRoleReference)
				}
			}
			f.setItems(append(artifactCreationItems(todo, "https://github.com/o/r/pull/5"),
				proto.Item{Role: "assistant", Kind: proto.KindText, TS: artifactTurnAt(todo), Text: "Related merged PR: https://github.com/o/r/pull/9\n" +
					"<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\n" +
					"artifact: pr https://github.com/o/r/pull/5\n</closeout>"})...)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/issues/4":
					_, _ = w.Write([]byte(`{"number":4}`))
				case "/repos/o/r/issues/5":
					_, _ = w.Write([]byte(`{"pull_request":{}}`))
				case "/repos/o/r/pulls/5":
					_, _ = w.Write([]byte(`{"merged":false}`))
				case "/repos/o/r/pulls/9":
					_, _ = w.Write([]byte(`{"merged":true}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
			s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
			s.deploy.listPaths = []string{}
			_, err := acceptReview(s, todo.ID, nil)
			if err == nil || !strings.Contains(err.Error(), "not merged") {
				t.Fatalf("the actual unmerged delivery was not blocked: %v", err)
			}
			got := todoNow(s, todo.ID)
			if got.DoneAt != nil || got.PRURL != "https://github.com/o/r/pull/5" {
				t.Fatalf("the explicit delivery did not replace the incidental reference: %+v", got)
			}
		})
	}
}

func TestNoPRCloseoutOutranksIncidentalPRNumbers(t *testing.T) {
	text := fmt.Sprintf("Related PR #9: https://github.com/o/r/pull/9\n%s", researchNoPRCloseout)
	if got := prNumberIn(text); got != "" {
		t.Fatalf("a no-PR closeout fell back to incidental PR number %s", got)
	}
	if _, got := stageOf(text); got != "" {
		t.Fatalf("a no-PR closeout fell back to incidental PR URL %s", got)
	}
}

func TestLegacyProgressWithoutAnAuthoritativeCloseout(t *testing.T) {
	for _, text := range []string{
		"Example:\n```text\n<closeout status=\"done\">\nartifact: doc docs/research.md\n</closeout>\n```\n",
		"> <closeout status=\"done\">\n> artifact: doc docs/research.md\n> </closeout>\n",
		"Implementing the <closeout> parser.\n",
	} {
		t.Run(text, func(t *testing.T) {
			if got := prNumberIn(text + "Pushed PR #5"); got != "5" {
				t.Fatalf("a non-authoritative example suppressed legacy PR progress: %q", got)
			}
			if _, got := stageOf(text + "Opened https://github.com/o/r/pull/5"); got != "https://github.com/o/r/pull/5" {
				t.Fatalf("a non-authoritative example suppressed legacy PR URL progress: %q", got)
			}
		})
	}
}

func TestCurrentNumberedArtifactKeepsOfflineDeploymentProof(t *testing.T) {
	for _, ref := range []string{"#9", "9", "o/r#9"} {
		t.Run(ref, func(t *testing.T) {
			forge := &fakeForge{err: fmt.Errorf("the forge is offline")}
			s, todo := deliveryReviewService(t, forge, &runningHub{commit: oldHub}, false)
			writeDeployed(t, s, oldHub, mergeSHA+" fix: the task's delivery (#9)")
			s.hub.(*fakeHub).setItems(proto.Item{Role: "assistant", Kind: proto.KindText,
				Text: "<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\nartifact: pr " + ref + "\n</closeout>"})
			if _, err := acceptReview(s, todo.ID, nil); err != nil {
				t.Fatalf("an already-recorded delivery lost its local deployment proof: %v", err)
			}
			// The running build's receipt proves deployment without a forge request.
			if got := todoNow(s, todo.ID); got.DoneAt == nil || forge.calls != 0 {
				t.Fatalf("local delivery was not accepted while the forge failed: done=%v, calls=%d", got.DoneAt, forge.calls)
			}
		})
	}
}
