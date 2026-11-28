package assistant

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskRepositoryOwnershipNativeEvidenceIsBoundToReportTurn(t *testing.T) {
	for _, entry := range []string{"report", "artifacts"} {
		for _, evidence := range []string{"same turn", "persisted timestamp precision", "same turn push", "earlier turn",
			"earlier turn push", "cached earlier turn", "cached identical reply", "side conversation", "fresh checkout"} {
			t.Run(entry+"/"+evidence, func(t *testing.T) {
				st := repositorystore.New(todoTestDB(t))
				s, f := newQuietServiceOn(t, st)
				todo := agentTask(t, s, f)
				at := todo.Agents[0].LinkedAt
				create := artifactCreationItems(todo, artifactPR)
				if evidence == "same turn push" || evidence == "earlier turn push" {
					raw, _ := json.Marshal(map[string]string{"cmd": "git push fork HEAD:fix/parser", "workdir": "/code/app"})
					create = []proto.Item{
						{Kind: proto.KindToolUse, ToolUseID: "push", ToolName: "functions.exec_command", ToolInput: raw, TS: at.Add(time.Millisecond)},
						{Kind: proto.KindToolResult, ToolUseID: "push", Text: "123..abc HEAD -> fix/parser\n",
							TS: at.Add(time.Millisecond), Outcome: &proto.ToolOutcome{Status: proto.OutcomeOK}},
					}
					agent := todo.Agents[0]
					f.setSessions(agent.MachineID, proto.Session{SessionID: agent.SessionID, MachineID: agent.MachineID,
						Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
					artifactNodeProof(f, artifactPR, "another-author", "contributor", "fix/parser", "fix/parser", "abc", "abc")
				}
				first := proto.Item{Role: "user", Kind: proto.KindText, Text: todo.Notes, TS: at}
				oldReply := proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Created " + artifactPR, TS: at.Add(500 * time.Millisecond)}
				prompt := proto.Item{Role: "user", Kind: proto.KindText, Text: "Check the deployment; the PR below is only a reference", TS: at.Add(time.Second)}
				reply := proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Deployment evidence mentions " + artifactPR, TS: at.Add(2 * time.Second)}
				items := append([]proto.Item{first}, create...)
				switch evidence {
				case "same turn", "persisted timestamp precision", "same turn push":
					items = append(items, reply)
				case "earlier turn", "earlier turn push":
					items = append(items, oldReply, prompt, reply)
				case "cached earlier turn", "fresh checkout":
					items = append(items, oldReply)
				case "cached identical reply":
					oldReply.Text = reply.Text
					items = append(items, oldReply)
				case "side conversation":
					first.Source = &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "another-conversation"}
					items = append([]proto.Item{first}, create...)
					items = append(items, reply)
				}
				f.setItems(items...)
				if evidence == "fresh checkout" {
					agent := todo.Agents[0]
					f.setSessions(agent.MachineID, proto.Session{SessionID: agent.SessionID, MachineID: agent.MachineID,
						Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
					artifactNodeProof(f, artifactPR, "contributor", "contributor", "fix/parser", "fix/parser", "abc", "abc")
				}
				agent := todo.Agents[0]
				recorded := reply
				if evidence == "persisted timestamp precision" {
					recorded.TS = recorded.TS.Truncate(time.Microsecond)
				}
				if entry == "report" {
					s.taskStageItems(t.Context(), SessionItems{MachineID: agent.MachineID, SessionID: agent.SessionID,
						Items: []proto.Item{recorded}})
				} else {
					work := artifactWork(todo)
					work.TurnAt = recorded.TS
					work.Work.Artifacts = work.Work.Artifacts[:1]
					s.putWork(t.Context(), work)
				}
				_, saved := savedArtifactURLs(t, st, todo.ID)
				want := evidence == "same turn" || evidence == "persisted timestamp precision" ||
					evidence == "same turn push" || evidence == "fresh checkout"
				if slices.Contains(saved, artifactPR) != want {
					t.Fatalf("native evidence from %s authorized the wrong reporting turn: saved=%v wantLinked=%v", evidence, saved, want)
				}
				if !want && todoNow(s, todo.ID).PRURL != "" {
					t.Fatal("rejected turn evidence polluted the legacy PR route")
				}
			})
		}
	}
}

func TestTaskRepositoryOwnershipIdenticalReplyInNewTurnIsRechecked(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	agent := todo.Agents[0]
	at := artifactTurnAt(todo)
	text := "Report: " + artifactPR
	artifactCreationProof(f, todo, artifactPR)
	session := proto.Session{SessionID: agent.SessionID, Status: proto.StatusIdle, LastText: text, UpdatedAt: at.Add(time.Second)}
	s.taskStageSessions(t.Context(), DeviceSessions{MachineID: agent.MachineID, Sessions: []proto.Session{session}})
	if _, urls := savedArtifactURLs(t, st, todo.ID); len(urls) != 0 {
		t.Fatal("the initial mismatched transcript was accepted", urls)
	}
	session.UpdatedAt = at.Add(2 * time.Second)
	proof := artifactCreationItems(todo, artifactPR)[0]
	proof.TS = session.UpdatedAt.Add(-time.Millisecond)
	f.setItems(
		proto.Item{Role: "user", Kind: proto.KindText, Text: todo.Notes, TS: session.UpdatedAt.Add(-time.Second)},
		proof,
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: text, TS: session.UpdatedAt},
	)
	s.taskStageSessions(t.Context(), DeviceSessions{MachineID: agent.MachineID, Sessions: []proto.Session{session}})
	if _, urls := savedArtifactURLs(t, st, todo.ID); !slices.Equal(urls, []string{artifactPR}) {
		t.Fatal("a previous reply cache hid fresh ownership evidence in the new turn", urls)
	}
}
