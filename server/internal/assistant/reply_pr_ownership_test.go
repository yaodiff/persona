package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	replyOwnPR   = "https://github.com/o/r/pull/9"
	replyOtherPR = "https://github.com/o/r/pull/12"
)

func TestTaskReplyCannotAdvanceFromMentionedPR(t *testing.T) {
	s, f := newTaskServiceOn(t, repositorystore.New(todoTestDB(t)))
	todo := startedTask(t, s, f)
	ctx := t.Context()
	s.deploy.forge = &fakeForge{}
	s.setTaskStage(ctx, todo.ID, "review", replyOwnPR)

	for _, text := range []string{
		"#12 is merged, so I can start.",
		"PR #12 is merged, so I can start.",
		replyOtherPR + " has merged.",
		"another/project#9 is merged.",
		"Dependency #12 is merged. Our PR remains open: " + replyOwnPR,
		replyOtherPR + " is deployed and verified.",
	} {
		artifactReportProof(f, todo, text, replyOwnPR)
		if s.advanceTaskReplyAt(ctx, todoNow(s, todo.ID), "box", "s1", text, artifactTurnAt(todo)) {
			t.Fatalf("dependency was treated as deployed: %q", text)
		}
		if got := todoNow(s, todo.ID); got.Stage != "review" || got.PRURL != replyOwnPR {
			t.Fatalf("dependency moved task progress: %q: stage=%q PR=%q", text, got.Stage, got.PRURL)
		}
	}
	s.advanceTaskReply(ctx, todoNow(s, todo.ID), "box", "s1", "Merged.")
	if got := todoNow(s, todo.ID); got.Stage != "merge" || got.PRURL != replyOwnPR {
		t.Fatalf("the task's own merge was lost: stage=%q PR=%q", got.Stage, got.PRURL)
	}
}

// The dependency is already in the running hub. The task's PR is an independent check.
type replyPRForge struct {
	fakeForge
	checked []int
}

func (f *replyPRForge) PRMerge(_ context.Context, _ string, number int) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, number)
	if number == 12 || f.merged {
		return true, f.sha, nil
	}
	return false, "", nil
}

func TestTaskDoneDeliveryDoesNotUseMentionedPR(t *testing.T) {
	for _, tc := range []struct {
		name, saved, proof, text, wantState string
		ownDeployed                         bool
		wantChecks                          []int
	}{
		{"only a dependency", "", "", replyOtherPR + " is merged and deployed.", "", false, nil},
		{"saved PR is open", replyOwnPR, "", replyOtherPR + " is merged and deployed.", deliveryPR, false, []int{9}},
		{"saved PR is deployed", replyOwnPR, "", replyOtherPR + " is merged and deployed.", deliveryLive, true, []int{9}},
		{"new own PR", "", replyOwnPR, "Deployed " + replyOwnPR + ". Dependency: " + replyOtherPR, deliveryLive, true, []int{9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			deliveryOn(t, s)
			build := &runningHub{commit: newHub}
			forge := &replyPRForge{fakeForge: fakeForge{sha: mergeSHA, merged: tc.ownDeployed, contains: map[string]bool{newHub: true}}}
			s.deploy.hubBuild, s.deploy.forge = build.build, forge
			s.deploy.listPaths = []string{}
			if tc.saved != "" {
				s.setTaskStage(t.Context(), todo.ID, "review", tc.saved)
			}
			if tc.proof != "" {
				artifactReportProof(f, todo, tc.text, tc.proof)
			} else {
				f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: tc.text})
			}
			f.judge = `{"verdict":"done","delivery":"live"}`
			c, err := s.judgeTaskDone(t.Context(), todoNow(s, todo.ID))
			if err != nil {
				t.Fatal(err)
			}
			state := ""
			if c.Delivery != nil {
				state = c.Delivery.State
			}
			forge.mu.Lock()
			checked := slices.Clone(forge.checked)
			forge.mu.Unlock()
			wantVerdict := verdictUndelivered
			if tc.ownDeployed {
				wantVerdict = verdictDone
			}
			if state != tc.wantState || c.Verdict != wantVerdict || c.WouldClose || !slices.Equal(checked, tc.wantChecks) {
				t.Fatalf("delivery=%+v, verdict=%q, closes=%v, checked PRs=%v; want state=%q, verdict=%q, no close, checked PRs=%v",
					c.Delivery, c.Verdict, c.WouldClose, checked, tc.wantState, wantVerdict, tc.wantChecks)
			}
		})
	}
}

func TestTaskDoneDeliveryRequiresStateAfterExcludingMentionedPR(t *testing.T) {
	for _, tc := range []struct {
		name, text, judgment, state, verdict string
	}{
		{"omitted", replyOtherPR + " is merged.", `{"verdict":"done"}`, "", verdictUndelivered},
		{"invalid", replyOtherPR + " is merged.", `{"verdict":"done","delivery":"unknown"}`, "", verdictUndelivered},
		{"suppressed delivery mark", replyOtherPR + " is deployed.\n<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\n" +
			"artifact: pr " + replyOtherPR + "\ndelivery: live\n</closeout>", `{"verdict":"done"}`, "", verdictUndelivered},
		{"explicit none", "The report cites " + replyOtherPR, `{"verdict":"done","delivery":"none"}`, deliveryNone, verdictDone},
		{"ordinary research", "The investigation is complete.", `{"verdict":"done"}`, deliveryNone, verdictDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			deliveryOn(t, s)
			f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: tc.text})
			f.judge = tc.judgment
			check, err := s.judgeTaskDone(t.Context(), todo)
			state := ""
			if check.Delivery != nil {
				state = check.Delivery.State
			}
			if err != nil || check.Verdict != tc.verdict || check.WouldClose || state != tc.state {
				t.Fatalf("check=%+v, state=%q, err=%v; want verdict=%q, no close, state=%q", check, state, err, tc.verdict, tc.state)
			}
		})
	}
}

func TestTaskReplyPreservesCanonicalOwnedPR(t *testing.T) {
	for _, tc := range []struct{ name, reported, canonical string }{
		{"GitHub case", "https://github.com/Example/Project/pull/7", artifactPR},
		{"GitHub HTTP", "http://github.com/example/project/pull/7", artifactPR},
		{"GitLab legacy route", "https://gitlab.com/group/project/merge_requests/17", "https://gitlab.com/group/project/-/merge_requests/17"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f := newQuietServiceOn(t, st)
			todo := agentTask(t, s, f)
			text := "Merged " + tc.reported
			reply := artifactReportProof(f, todo, text, tc.canonical)
			a := todo.Agents[0]
			s.advanceTaskReplyAt(t.Context(), todo, a.MachineID, a.SessionID, text, reply.TS)
			_, saved := savedArtifactURLs(t, st, todo.ID)
			if got := todoNow(s, todo.ID); got.PRURL != tc.canonical || got.Stage != "merge" || !slices.Equal(saved, []string{tc.canonical}) {
				t.Fatalf("canonical link not applied: PR=%q stage=%q associations=%v", got.PRURL, got.Stage, saved)
			}
		})
	}
}

func TestTaskDoneSignalsUseOnlyOwnedPR(t *testing.T) {
	for _, tc := range []struct {
		name, saved, proof, text string
		merged                   bool
	}{
		{"dependency URL", "", "", replyOtherPR + " was merged.", false},
		{"dependency number", replyOwnPR, "", "#12 was merged.", false},
		{"mixed reply", replyOwnPR, "", replyOwnPR + " is open. " + replyOtherPR + " was merged.", false},
		{"saved PR", replyOwnPR, "", "PR #9 was merged.", true},
		{"native creation", "", replyOwnPR, "Merged " + replyOwnPR, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			s.deploy.forge = &fakeForge{}
			if tc.saved != "" {
				s.setTaskStage(t.Context(), todo.ID, "review", tc.saved)
			}
			if tc.proof != "" {
				artifactReportProof(f, todo, tc.text, tc.proof)
			} else {
				f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: tc.text})
			}
			f.judge = `{"verdict":"not_done"}`
			c, err := s.judgeTaskDone(t.Context(), todoNow(s, todo.ID))
			if err != nil || c.Signals.MergedPR != tc.merged {
				t.Fatalf("merged signal=%v, err=%v; want merged=%v", c.Signals.MergedPR, err, tc.merged)
			}
		})
	}
}

func TestTaskReceiptOmitsMentionedPR(t *testing.T) {
	for _, tc := range []struct{ name, saved, proof, want string }{
		{"dependency only", "", "", ""},
		{"already recorded", replyOwnPR, "", replyOwnPR},
		{"native creation", "", replyOwnPR, replyOwnPR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			ctx := t.Context()
			if tc.saved != "" {
				s.setTaskStage(ctx, todo.ID, "review", tc.saved)
			}
			f.judge = `{"verdict":"not_done"}`
			agentTurn(s, f, proto.StatusWorking, "", 0)
			text := "The dependency " + replyOtherPR + " was merged."
			if tc.proof != "" {
				text = "Opened " + tc.proof + ". " + text
				artifactReportProof(f, todo, text, tc.proof)
			}
			agentTurn(s, f, proto.StatusDone, text, 0)
			feed, _, err := s.feed(ctx, "conv1", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range feed {
				if item.Kind == feedReceipt {
					var body receiptBody
					if err := json.Unmarshal(item.Body, &body); err != nil || body.PR != tc.want {
						t.Fatalf("receipt PR=%q, err=%v; want %q", body.PR, err, tc.want)
					}
					return
				}
			}
			t.Fatal("no task receipt")
		})
	}
}
