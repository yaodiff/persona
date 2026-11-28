package hub

import (
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTodoHandoffCarriesTheTargetAgent(t *testing.T) {
	for _, tc := range []struct {
		name           string
		body           string
		send           bool
		managerDefault bool
	}{
		{name: "start resolves the launcher", body: `{"machine_id":"box","cwd":"/work","launcher":"agy","prompt":"/boost fix","todo_id":"todo-agy"}`},
		{name: "start uses the manager default", body: `{"machine_id":"box","cwd":"/work","prompt":"/boost fix","todo_id":"todo-agy"}`, managerDefault: true},
		{name: "resume uses the listed session", body: `{"machine_id":"box","cwd":"/work","resume_session_id":"s1","prompt":"/boost fix","todo_id":"todo-agy"}`},
		{name: "message uses the listed session", body: `{"text":"/boost fix","todo_id":"todo-agy"}`, send: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m, mgr, _ := heldStartHub(t, true)
			m.cfg.Launchers = append(m.cfg.Launchers, assistantwire.ConfiguredLauncher{
				Launcher: proto.Launcher{Name: "agy"}, AgentType: "agy",
			})
			m.mu.Lock()
			m.sessions = []proto.Session{{SessionID: "s1", MachineID: "box", Launcher: "agy", Agent: "agy"}}
			m.mu.Unlock()
			if tc.managerDefault {
				mgr.onStartParam = func(p assistantwire.AgentStartParams) (any, error) {
					p.Params["launcher"] = "agy"
					p.Params["agent_type"] = "agy"
					return map[string]any{"params": p.Params}, nil
				}
			}
			mgr.onTodoHand = func(assistantwire.TodoHandoff) (any, error) {
				return assistantwire.TodoHandoffResult{Found: false}, nil
			}

			path := "/api/v1/sessions"
			if tc.send {
				path += "/box/s1/messages"
			}
			w := modelsRequest(h, http.MethodPost, path, tc.body)
			mgr.mu.Lock()
			defer mgr.mu.Unlock()
			if len(mgr.todoHandoffs) != 1 || mgr.todoHandoffs[0].Agent != "agy" {
				t.Fatalf("%s handoff = %+v, want agent agy (response %d %s)", tc.name, mgr.todoHandoffs, w.Code, w.Body)
			}
		})
	}
}
