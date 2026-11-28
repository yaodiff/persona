package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestMeetingRepositoryReadsDoNotImportLegacyLinks(t *testing.T) {
	needsRecordingModule(t)
	for _, entry := range []string{"todo_get", "todo_repositories_get", "agent"} {
		for _, populated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/populated=%t", entry, populated), func(t *testing.T) {
				dsn := newMigratedDatabase(t, db.ManagerSet)
				database, err := db.Open(t.Context(), dsn, db.ManagerSet)
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				e := newTodoEnv(t, database)
				e.f.setSessions("box", proto.Session{SessionID: "attendee"})
				id := e.create("Review related work").ID
				e.must("PUT", "/api/v1/todos/"+id+"/agents/box/attendee", "")
				if populated {
					if _, _, err := repositorystore.New(database).ImportTodoRepositoryLinks(t.Context(), id, []string{"https://github.com/example/web/pull/6"}, "explicit"); err != nil {
						t.Fatal(err)
					}
				}
				const legacy = "https://github.com/example/web/pull/7"
				if err := database.PutSetting(t.Context(), reportStagesKey, map[string]todoStage{id: {Stage: "review", PR: legacy}}); err != nil {
					t.Fatal(err)
				}
				// Reconstruct the service so the legacy state comes from durable storage.
				s, _ := repositoryService(t, database)
				if _, err := s.meetingTurnSent("box", "attendee", "recording", entry != "agent"); err != nil {
					t.Fatal(err)
				}
				conn, err := pgx.Connect(t.Context(), dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close(t.Context())
				snapshot := func() string {
					t.Helper()
					var state string
					if err := conn.QueryRow(t.Context(), `SELECT json_build_array(
					 (SELECT row_to_json(t) FROM todos t WHERE id=$1),
					 (SELECT row_to_json(r) FROM todo_repository_sets r WHERE todo_id=$1),
					 (SELECT row_to_json(r) FROM todo_repository_link_receipts r WHERE todo_id=$1))::text`, id).Scan(&state); err != nil {
						t.Fatal(err)
					}
					return state
				}
				before := snapshot()
				args := json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))
				name := entry
				if entry == "agent" {
					name = "todo_repositories_get"
				}
				call := func() proto.ToolResult {
					if entry == "agent" {
						return s.mcpCall(t.Context(), "box", "attendee", name, args, false)
					}
					return s.Tool(t.Context(), ToolCall{MachineID: "box", SessionID: "attendee", Name: name, Arguments: args})
				}
				res := call()
				if res.IsError || len(res.Content) == 0 {
					t.Fatal(res)
				}
				if after := snapshot(); after != before {
					t.Fatalf("meeting read changed the Todo, association revision/data or receipt:\nbefore %s\nafter  %s", before, after)
				}
				if strings.Contains(res.Content[0].Text, legacy) {
					t.Fatal("unimported link presented as a saved association", res)
				}
				if populated && !strings.Contains(res.Content[0].Text, "/pull/6") {
					t.Fatal("meeting read omitted an existing association", res)
				}
				s.meetingTurnUserSpoke("box", "attendee")
				if res := call(); res.IsError || !strings.Contains(res.Content[0].Text, legacy) || snapshot() == before {
					t.Fatal("ordinary user turn no longer imports legacy links", res)
				}
			})
		}
	}
}
