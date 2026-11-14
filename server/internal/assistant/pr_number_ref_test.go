package assistant

import (
	"context"
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
	} {
		if got := prNumberIn(text); got != want {
			t.Errorf("prNumberIn(%q) = %q, want %q", text, got, want)
		}
	}
}

// A PR named by number alone is recorded as a full URL when its repository is known: the task's own PR, a link in the
// reply or the to-do, or what earlier replies from the same repository root linked. Unknown or several: nothing.
func TestPRFromNumber(t *testing.T) {
	s, f := newTaskServiceOn(t, &memStore{})
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
