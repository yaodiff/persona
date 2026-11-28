package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func artifactNodeProof(f *fakeHub, url, author, viewer, branch, head, oid, localOID string) {
	f.answerNode("git.pr", func(_ string, args json.RawMessage) (any, error) {
		var in struct {
			Cwd       string `json:"cwd"`
			Ownership bool   `json:"ownership"`
		}
		if json.Unmarshal(args, &in) != nil || in.Cwd == "" || !in.Ownership {
			return nil, errors.New("ownership must use the actual session checkout")
		}
		return map[string]any{"branch": branch, "head_oid": localOID, "viewer": viewer,
			"pr": map[string]string{"url": url, "author": author, "head": head, "head_oid": oid}}, nil
	})
}

func TestTaskRepositoryOwnershipDeploymentReportCannotClaimOthersPRs(t *testing.T) {
	for _, entry := range []string{"progress report", "closeout", "model artifacts"} {
		t.Run(entry, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f := newQuietServiceOn(t, st)
			todo := agentTask(t, s, f)
			urls := []string{}
			for _, number := range []int{17, 18, 19, 20, 21, 22} {
				urls = append(urls, fmt.Sprintf("https://github.com/example/project/pull/%d", number))
			}
			// These PRs may even have the same author account. The deployment
			// checkout is not any of their published head branches.
			artifactNodeProof(f, urls[0], "contributor", "contributor", "local/integration", "fix/17", "abc", "abc")
			text := "Deployment evidence includes other tasks' changes:\n" + strings.Join(urls, "\n")
			switch entry {
			case "closeout":
				text += "\n<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\n"
				for _, url := range urls {
					text += "artifact: pr " + url + "\n"
				}
				text += "</closeout>"
			case "model artifacts":
				work := artifactWork(todo)
				work.Work.Artifacts = nil
				for _, url := range urls {
					work.Work.Artifacts = append(work.Work.Artifacts, proto.WorkArtifact{Kind: "pr", Ref: url})
				}
				s.putWork(t.Context(), work)
			}
			if entry != "model artifacts" {
				s.taskStageItems(t.Context(), SessionItems{MachineID: todo.Agents[0].MachineID,
					SessionID: todo.Agents[0].SessionID,
					Items:     []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: text}}})
			}
			it := artifactInboxItem(s, "github:deployment-ref", urls[0])
			s.linkInboxTaskURLs(t.Context())
			set, saved := savedArtifactURLs(t, st, todo.ID)
			current := todoNow(s, todo.ID)
			if entry == "model artifacts" && (set.Revision != 0 || len(saved) != 0) {
				t.Fatalf("unowned model artifacts polluted repository links: links=%v", saved)
			}
			for _, row := range set.Repositories {
				for _, link := range row.Links {
					if link.Role != repository.LinkRoleReference {
						t.Fatalf("referenced PRs claimed delivery role: %+v", row.Links)
					}
				}
			}
			if current.PRURL != "" || len(current.PRs) != 0 || it.Agent != nil && it.Agent.TodoID == todo.ID {
				t.Fatalf("referenced PRs polluted delivery or Inbox routing: links=%v, task=%+v, inbox=%+v", saved, current, it.Agent)
			}
		})
	}
}

func TestTaskRepositoryOwnershipRequiresIndependentPublishedHead(t *testing.T) {
	for _, name := range []string{"owned", "own push", "wrong push branch", "other author", "other branch", "unpushed commit", "other PR", "old node", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f := newQuietServiceOn(t, st)
			todo := agentTask(t, s, f)
			url, author, branch, oid := artifactPR, "contributor", "fix/parser", "abc"
			switch name {
			case "own push", "wrong push branch", "other author":
				author = "someone-else"
			case "other branch":
				branch = "local/integration"
			case "unpushed commit":
				oid = "unpublished"
			case "other PR":
				url = "https://github.com/example/another/pull/7"
			}
			artifactNodeProof(f, url, author, "contributor", branch, "fix/parser", "abc", oid)
			if name == "own push" || name == "wrong push branch" {
				pushBranch := "fix/parser"
				if name == "wrong push branch" {
					pushBranch = "another/branch"
				}
				at := todo.Agents[0].LinkedAt.Add(time.Millisecond)
				raw, err := json.Marshal(map[string]string{"cmd": "git push fork HEAD:" + pushBranch})
				if err != nil {
					t.Fatal(err)
				}
				f.setItems(
					proto.Item{Kind: proto.KindToolUse, TS: at, ToolUseID: "push", ToolName: "functions.exec_command", ToolInput: raw},
					proto.Item{Kind: proto.KindToolResult, TS: at, ToolUseID: "push", Text: " * [new branch] HEAD -> " + pushBranch,
						Outcome: &proto.ToolOutcome{Status: proto.OutcomeOK}},
					proto.Item{Kind: proto.KindNotice, TS: artifactTurnAt(todo)},
				)
			}
			if name == "old node" {
				f.answerNode("git.pr", func(string, json.RawMessage) (any, error) {
					return map[string]any{"branch": "fix/parser", "pr": map[string]string{"url": artifactPR, "head": "fix/parser"}}, nil
				})
			}
			if name == "unavailable" {
				f.answerNode("git.pr", func(string, json.RawMessage) (any, error) { return nil, errors.New("node offline") })
			}
			work := artifactWork(todo)
			work.Work.Artifacts = work.Work.Artifacts[:1]
			if name == "own push" {
				f.mu.Lock()
				f.items = append(f.items, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "PR: " + artifactPR, TS: work.TurnAt})
				f.mu.Unlock()
			}
			s.putWork(t.Context(), work)
			_, saved := savedArtifactURLs(t, st, todo.ID)
			want := name == "owned" || name == "own push"
			if slices.Contains(saved, artifactPR) != want {
				t.Fatalf("ownership=%s: saved=%v", name, saved)
			}
			s.advanceTaskReplyAt(t.Context(), todo, work.MachineID, work.SessionID, "PR: "+artifactPR, work.TurnAt)
			if got := todoNow(s, todo.ID).PRURL; (got == artifactPR) != want {
				t.Fatalf("legacy PR field bypassed ownership: %q", got)
			}
		})
	}
}

func TestTaskRepositoryOwnershipNativeCreateMustSucceedAfterTaskLink(t *testing.T) {
	for _, name := range []string{"created", "viewed", "failed", "old", "missing timestamp", "text only"} {
		t.Run(name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f := newQuietServiceOn(t, st)
			todo := agentTask(t, s, f)
			artifactCreationProof(f, todo, artifactPR)
			f.mu.Lock()
			it := &f.items[0]
			switch name {
			case "viewed":
				it.Outcome.Git.PR.Action = "viewed"
			case "failed":
				it.IsError = true
			case "old":
				it.TS = todo.Agents[0].LinkedAt.Add(-time.Second)
			case "missing timestamp":
				it.TS = time.Time{}
			case "text only":
				it.Kind, it.Text, it.Outcome = proto.KindText, "gh pr create returned "+artifactPR, nil
			}
			f.mu.Unlock()
			work := artifactWork(todo)
			work.Work.Artifacts = work.Work.Artifacts[:1]
			s.putWork(t.Context(), work)
			_, saved := savedArtifactURLs(t, st, todo.ID)
			if slices.Contains(saved, artifactPR) != (name == "created") {
				t.Fatalf("native evidence=%s: saved=%v", name, saved)
			}
		})
	}
}

func TestTaskRepositoryOwnershipAlreadyLinkedElsewhereDoesNotPolluteLegacyPR(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	owner, err := s.createPlainTodo(t.Context(), "Existing owner", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.importTodoRepositoryLinks(t.Context(), owner.ID, []string{artifactPR}, "explicit"); err != nil {
		t.Fatal(err)
	}
	reply := artifactReportProof(f, todo, "<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\nartifact: pr "+artifactPR+"\n</closeout>", artifactPR)
	s.advanceTaskReplyAt(t.Context(), todo, todo.Agents[0].MachineID, todo.Agents[0].SessionID, reply.Text, reply.TS)
	if _, links := savedArtifactURLs(t, st, todo.ID); len(links) != 0 {
		t.Fatal("automatic import duplicated another open task", links)
	}
	if got := todoNow(s, todo.ID); got.PRURL != "" || len(got.PRs) != 0 {
		t.Fatal("legacy PR fields bypassed the cross-task exclusion", got)
	}
	if _, links := savedArtifactURLs(t, st, owner.ID); !slices.Equal(links, []string{artifactPR}) {
		t.Fatal("automatic import changed the existing owner", links)
	}
}

func TestTaskRepositoryOwnershipConflictingSavedLinkIsNotAnAcceptedClaim(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	owner, err := s.createPlainTodo(t.Context(), "Existing owner", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	// Preserve existing associations, including historical duplicates. Their
	// presence in the returned set must not make a rejected claim successful.
	for _, id := range []string{todo.ID, owner.ID} {
		if err := s.importTodoRepositoryLinks(t.Context(), id, []string{artifactPR}, "explicit"); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := savedArtifactURLs(t, st, todo.ID)
	reply := artifactReportProof(f, todo, "PR: "+artifactPR, artifactPR)
	s.advanceTaskReplyAt(t.Context(), todo, todo.Agents[0].MachineID, todo.Agents[0].SessionID, reply.Text, reply.TS)
	if got := todoNow(s, todo.ID); got.PRURL != "" || len(got.PRs) != 0 {
		t.Fatal("a conflicting saved association was mistaken for an accepted claim", got)
	}
	if after, urls := savedArtifactURLs(t, st, todo.ID); after.Revision != before.Revision ||
		!slices.Equal(urls, []string{artifactPR}) {
		t.Fatal("automatic claim changed existing explicit associations", after, urls)
	}
}

func TestTaskRepositoryOwnershipUsesNativeCLIResultsNotReportedCommands(t *testing.T) {
	for _, name := range []string{"created", "pushed", "pushed with options", "viewed", "echoed", "conditional", "dry run", "push dry run value",
		"push dry run bundle", "abbreviated dry run", "abbreviated delete", "delete refspec", "mirror", "deleted output",
		"deleted with structured evidence", "merely up to date",
		"create dry run", "create dry run value", "help", "help value", "web", "dynamic flags", "failed",
		"read tool", "wrong result", "older call", "ambiguous output"} {
		t.Run(name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f := newQuietServiceOn(t, st)
			todo := agentTask(t, s, f)
			args := map[string]string{"cmd": "gh pr create --title Fix --body-file /tmp/body.md", "workdir": "/code/feature"}
			tool, output := "functions.exec_command", artifactPR
			switch name {
			case "pushed", "pushed with options", "dry run", "push dry run value", "push dry run bundle", "abbreviated dry run", "abbreviated delete":
				args["cmd"] = "git push fork HEAD:fix/parser"
				switch name {
				case "pushed with options":
					args["cmd"] = "git push -uv --force-with-lease fork HEAD:fix/parser"
				case "dry run":
					args["cmd"] += " --dry-run"
				case "push dry run value":
					args["cmd"] += " --dry-run=true"
				case "push dry run bundle":
					args["cmd"] += " -nu"
				case "abbreviated dry run":
					args["cmd"] = "git push --dry fork HEAD:fix/parser"
				case "abbreviated delete":
					args["cmd"] = "git push --del fork fix/parser"
				}
				output = "To https://github.com/contributor/project.git\n   123..abc HEAD -> fix/parser\n"
				artifactNodeProof(f, artifactPR, "someone-else", "contributor", "fix/parser", "fix/parser", "abc", "abc")
			case "delete refspec", "mirror", "deleted output", "deleted with structured evidence", "merely up to date":
				args["cmd"] = "git push fork HEAD:fix/parser"
				output = "To https://github.com/contributor/project.git\n - [deleted]         (none) -> fix/parser\n"
				switch name {
				case "delete refspec":
					args["cmd"] = "git push fork +:fix/parser"
					output = "   123..abc HEAD -> fix/parser\n"
				case "mirror":
					args["cmd"] = "git push --mirror fork"
					output = "   123..abc HEAD -> fix/parser\n"
				case "merely up to date":
					output = " = [up to date]      HEAD -> fix/parser\n"
				}
				artifactNodeProof(f, artifactPR, "someone-else", "contributor", "fix/parser", "fix/parser", "abc", "abc")
			case "viewed":
				args["cmd"] = "gh pr view --json url"
			case "create dry run":
				args["cmd"] += " --dry-run"
			case "create dry run value":
				args["cmd"] += " --dry-run=true"
			case "help":
				args["cmd"] += " --help"
			case "help value":
				args["cmd"] += " --help=true"
			case "web":
				args["cmd"] += " -w"
			case "dynamic flags":
				args["cmd"] += " \"$EXTRA_FLAGS\""
			case "echoed":
				args["cmd"] = "echo 'gh pr create'"
			case "conditional":
				args["cmd"] = "false && gh pr create --title Fix"
			case "read tool":
				tool = "Read"
			case "ambiguous output":
				output += "\nhttps://github.com/example/project/pull/8"
			}
			raw, _ := json.Marshal(args)
			at := todo.Agents[0].LinkedAt.Add(time.Millisecond)
			call := proto.Item{Kind: proto.KindToolUse, ToolUseID: "native", ToolName: tool, ToolInput: raw, TS: at}
			result := proto.Item{Kind: proto.KindToolResult, ToolUseID: "native", Text: output, TS: at,
				Outcome: &proto.ToolOutcome{Status: proto.OutcomeOK}}
			switch name {
			case "deleted with structured evidence":
				result.Outcome.Git = &proto.OutcomeGit{Push: &proto.OutcomePush{Branch: "fix/parser"}}
			case "failed":
				result.IsError, result.Outcome.Status = true, proto.OutcomeFailed
			case "wrong result":
				result.ToolUseID = "unrelated"
			case "older call":
				call.TS = at.Add(-time.Hour)
			}
			work := artifactWork(todo)
			f.setItems(call, result, proto.Item{Kind: proto.KindNotice, TS: work.TurnAt})
			work.Work.Artifacts = work.Work.Artifacts[:1]
			s.putWork(t.Context(), work)
			_, saved := savedArtifactURLs(t, st, todo.ID)
			want := name == "created" || name == "pushed" || name == "pushed with options"
			if slices.Contains(saved, artifactPR) != want {
				t.Fatalf("native CLI result %s: links=%v", name, saved)
			}
			if name == "pushed" {
				f.mu.Lock()
				defer f.mu.Unlock()
				found := false
				for _, call := range f.calls {
					var args struct {
						Cwd string `json:"cwd"`
					}
					if call.Method == "git.pr" && json.Unmarshal(call.Params, &args) == nil && args.Cwd == "/code/feature" {
						found = true
					}
				}
				if !found {
					t.Fatal("native push's actual worktree was not verified")
				}
			}
		})
	}
}

func TestTaskRepositoryOwnershipDeliveryRecoveryCannotClaimReferencePR(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	s.deploy.forge = &fakeForge{}
	artifactNodeProof(f, artifactPR, "contributor", "contributor", "local/integration", "fix/parser", "abc", "abc")
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: todo.Notes},
		proto.Item{Role: "assistant", Kind: proto.KindText, TS: todo.Agents[0].LinkedAt.Add(time.Millisecond),
			Text: "<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\nartifact: pr " + artifactPR + "\n</closeout>"})
	if pr, _, err := s.reportedTaskPR(t.Context(), todo, ""); pr != "" || err == nil {
		t.Fatal("delivery recovery bypassed ownership verification", pr, err)
	}
	if _, links := savedArtifactURLs(t, st, todo.ID); len(links) != 0 {
		t.Fatal("recovery imported a reference PR", links)
	}
}

func TestTaskRepositoryOwnershipRechecksAgentAfterNodeRead(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	f.answerNode("git.pr", func(string, json.RawMessage) (any, error) {
		now := time.Now()
		s.todos.mu.Lock()
		s.todos.todos[todo.ID].Agents[0].ReleasedAt = &now
		s.todos.mu.Unlock()
		return map[string]any{"branch": "fix/parser", "viewer": "contributor", "head_oid": "abc",
			"pr": map[string]string{"url": artifactPR, "author": "contributor", "head": "fix/parser", "head_oid": "abc"}}, nil
	})
	work := artifactWork(todo)
	work.Work.Artifacts = work.Work.Artifacts[:1]
	s.putWork(t.Context(), work)
	if _, links := savedArtifactURLs(t, st, todo.ID); len(links) != 0 {
		t.Fatal("a released Agent's late verification still changed associations", links)
	}
}
