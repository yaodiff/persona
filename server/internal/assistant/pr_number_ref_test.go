package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPRNumberIn(t *testing.T) {
	for text, want := range map[string]string{
		"Opened PR #7350 for review":                          "7350",
		"The fix is pushed to #7350; the PR waits for review": "7350",
		"PR #12 fixes issue #9":                               "12",
		"Fixes #9":                                            "", // no PR spoken of: an issue
		"PR #1 and PR #2":                                     "",
		"Both #5 and #9 are in the PR":                        "",
		"https://github.com/o/r/pull/3":                       "", // a link is prLink's
		"Earlier PR #4\n<closeout status=\"done\">\nartifact: pr #5\n</closeout>":                               "5",
		"Earlier PR #4\n<closeout status=\"done\">\nartifact: pr 5\n</closeout>":                                "5",
		"Earlier PR #4\n<closeout status=\"done\">\nartifact: pr o/r#5\n</closeout>":                            "5",
		"Earlier PR #4\n<closeout status=\"done\">\nartifact: pr https://github.com/o/r/pull/5\n</closeout>":    "",
		"<closeout status=\"done\">\nartifact: pr #5\nartifact: url https://github.com/o/r/pull/4\n</closeout>": "5",
	} {
		if got := prNumberIn(text); got != want {
			t.Errorf("prNumberIn(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestPRFromNumberRequiresPullRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		forge *fakeForge
		want  string
	}{
		{"issue mentioned with PR wording", "Research references issue #9; no PR was opened", &fakeForge{issueOnly: true}, ""},
		{"issue mislabeled as a PR", "PR #9", &fakeForge{issueOnly: true}, ""},
		{"explicit issue URL", "No PR: https://github.com/o/r/issues/9", &fakeForge{}, ""},
		{"linked issue number", "No PR: [#9](https://github.com/o/r/issues/9)", &fakeForge{issueOnly: true}, ""},
		{"unconfirmed type", "PR #9", &fakeForge{issueErr: errors.New("rate limited")}, ""},
		{"confirmed PR", "PR #9", &fakeForge{}, "https://github.com/o/r/pull/9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTaskServiceOn(t, &memStore{})
			s.deploy.forge = tc.forge
			todo := Todo{Notes: "https://github.com/o/r/issues/1"}
			if got := s.prFromNumber(context.Background(), todo, "", "", tc.text); got != tc.want {
				t.Fatalf("prFromNumber(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestTaskDoesNotRecordIssueAsPR(t *testing.T) {
	for _, text := range []string{
		"Research references issue #9; no PR was opened",
		"Research references https://github.com/o/r/issues/9; no PR was opened",
		"Research references [issue #9](https://github.com/o/r/issues/9); no PR was opened",
	} {
		t.Run(text, func(t *testing.T) {
			s, f := newTaskService(t)
			s.deploy.forge = &fakeForge{issueOnly: true}
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.setTaskStage(ctx, todo.ID, "develop", "")
			cur := todoNow(s, todo.ID)
			cur.Notes = "https://github.com/o/r/issues/1"
			s.advanceTaskReply(ctx, cur, "box", "s1", text)
			got := todoNow(s, todo.ID)
			if got.PRURL != "" || len(got.PRs) != 0 || got.Stage != "develop" {
				t.Fatalf("issue became delivery work: PR %q, history %v, stage %s", got.PRURL, got.PRs, got.Stage)
			}
		})
	}
}

// A PR named by number alone is recorded as a full URL when its repository is known: the task's own PR, a link in the
// reply or the to-do, or what earlier replies from the same repository root linked. Unknown or several: nothing.
func TestPRFromNumber(t *testing.T) {
	s, f := newTaskServiceOn(t, &memStore{})
	s.deploy.forge = &fakeForge{}
	ctx := context.Background()
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", RepoRoot: "/code/app"},
		proto.Session{SessionID: "s2", MachineID: "box", RepoRoot: "/code/other"})
	reply := "Pushed the fix to #7350; the PR waits for the maintainer"
	for _, tc := range []struct {
		name string
		t    Todo
		text string
		want string
	}{
		{"own PR", Todo{PRURL: "https://github.com/Owner/App/pull/7"}, reply, "https://github.com/owner/app/pull/7350"},
		{"earlier PR", Todo{PRs: []string{"https://github.com/o/a/pull/1"}}, reply, "https://github.com/o/a/pull/7350"},
		{"link in the reply", Todo{}, reply + " (issue https://github.com/o/b/issues/9)", "https://github.com/o/b/pull/7350"},
		{"link in the notes", Todo{Notes: "see https://github.com/o/c/issues/2"}, reply, "https://github.com/o/c/pull/7350"},
		{"two repositories", Todo{Notes: "https://github.com/o/c/issues/2 https://github.com/o/d/issues/3"}, reply, ""},
		{"unknown", Todo{}, reply, ""},
	} {
		if got := s.prFromNumber(ctx, tc.t, "box", "s1", tc.text); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// A full link from an agent in /code/app teaches its repository; a later number from the same root resolves, one
	// from another root does not.
	s.learnRepoRoot(ctx, "box", "s1", "https://github.com/o/app/pull/1")
	if got := s.prFromNumber(ctx, Todo{}, "box", "s1", reply); got != "https://github.com/o/app/pull/7350" {
		t.Fatalf("learned root: %q", got)
	}
	if got := s.prFromNumber(ctx, Todo{}, "box", "s2", reply); got != "" {
		t.Fatalf("other root: %q", got)
	}
}

// The incident: a supervised task's agent reports the PR by number alone; the task now carries it (pr_url and the
// review stage), so its PR status is read and it can wait for merge.
func TestTaskRecordsAPRNamedByNumber(t *testing.T) {
	s, f := newTaskService(t)
	s.deploy.forge = &fakeForge{}
	todo := startedTask(t, s, f)
	ctx := context.Background()
	s.setTaskStage(ctx, todo.ID, "develop", "")
	cur := todoNow(s, todo.ID)
	cur.Notes = "Upstream: https://github.com/o/app/issues/7343"
	s.advanceTaskReply(ctx, cur, "box", "s1", "The 423 fix is pushed to #7350 and deployed; the PR waits for review")
	if got := todoNow(s, todo.ID); got.PRURL != "https://github.com/o/app/pull/7350" {
		t.Fatalf("pr_url = %q (stage %s)", got.PRURL, got.Stage)
	}
}

func TestTaskCloseoutArtifactReplacesEarlierPR(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close string
	}{
		{"number with old PR in prose", "PR #4 was the earlier attempt.\n<closeout status=\"done\">\nartifact: pr #5\n</closeout>"},
		{"bare number with old PR in prose", "PR #4 was the earlier attempt.\n<closeout status=\"done\">\nartifact: pr 5\n</closeout>"},
		{"repo-qualified number with old PR in prose", "PR #4 was the earlier attempt.\n<closeout status=\"done\">\nartifact: pr o/r#5\n</closeout>"},
		{"full link with old PR in prose", "PR #4 was the earlier attempt.\n<closeout status=\"done\">\nartifact: pr https://github.com/o/r/pull/5\n</closeout>"},
		{"later wrong URL artifact", "<closeout status=\"done\">\nartifact: pr #5\nartifact: url https://github.com/o/r/pull/4\n</closeout>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			s.deploy.forge = &fakeForge{}
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/4")
			if got := todoNow(s, todo.ID).PRURL; got != "https://github.com/o/r/pull/4" {
				t.Fatalf("initial PR = %q, want /pull/4", got)
			}

			s.taskStageItems(ctx, SessionItems{MachineID: "box", SessionID: "s1", Items: []proto.Item{{
				Role: "assistant", Kind: proto.KindText, Text: tc.close,
			}}})
			if got := todoNow(s, todo.ID).PRURL; got != "https://github.com/o/r/pull/5" {
				t.Fatalf("current PR = %q, want /pull/5 (stage %s)", got, todoNow(s, todo.ID).Stage)
			}
		})
	}
}
