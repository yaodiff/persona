package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Work privacy limits data tools, while personad ask and ordinary agent hooks still
// show their cards on the phone and receive the user's answer through the manager.
func TestWorkMachineQuestionsAndApprovals(t *testing.T) {
	for _, tc := range []struct {
		name, kind, agent, tool, session string
	}{
		{"script question", proto.IxQuestion, "script", "personad_ask", ""},
		{"script permission", proto.IxPermission, "script", "personad_ask", ""},
		{"agent question", proto.IxQuestion, "claude", "AskUserQuestion", "work-session"},
		{"agent permission", proto.IxPermission, "claude", "Bash", "work-session"},
		{"agent plan", proto.IxPlan, "claude", "ExitPlanMode", "work-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			ft.d.machines["mac"].trust = "work"
			if res := ft.s.mcpCall(context.Background(), "mac", tc.session, "phone_contacts", json.RawMessage(`{}`), true); !res.IsError {
				t.Fatal("the fixture must enforce work privacy before testing its cards")
			}
			ix := proto.Interaction{ID: "ix_work", MachineID: "untrusted-payload", SessionID: tc.session,
				Kind: tc.kind, OutwardWrite: tc.kind == proto.IxPermission, Agent: tc.agent, ToolName: tc.tool, Summary: "Continue the build?",
				ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now().UTC()}
			answer := `{"allow":true}`
			switch tc.kind {
			case proto.IxQuestion:
				ix.Questions = []proto.Question{{Question: "Which build?", Options: []proto.QuestionOption{{Label: "Debug"}, {Label: "Release"}}}}
				answer = `{"answers":{"Which build?":"Debug"}}`
			case proto.IxPlan:
				answer = `{"approve":true}`
			}
			params, err := json.Marshal(map[string]any{"interaction": ix})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				value   any
				handled bool
				err     error
			}
			done := make(chan result, 1)
			go func() {
				value, handled, err := ft.s.NodeRequest(ctx, "mac", "interaction.create", params)
				done <- result{value, handled, err}
			}()
			card := ft.waitPending(1)[0]
			if card.MachineID != "mac" || card.SessionID != tc.session || card.Kind != tc.kind || card.ToolName != tc.tool {
				t.Fatalf("the phone's card lost its trusted attribution: %+v", card)
			}
			if pushes := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(pushes) != 1 || pushes[0].DeviceID != "mac" {
				t.Fatalf("work card notification: %+v", pushes)
			}
			response := httptest.NewRecorder()
			ft.s.fleetHandler(http.NotFoundHandler()).ServeHTTP(response,
				httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+ix.ID+"/resolve", strings.NewReader(answer)))
			if response.Code != http.StatusOK {
				t.Fatalf("phone answer: %d %s", response.Code, response.Body.String())
			}
			select {
			case got := <-done:
				if !got.handled || got.err != nil {
					t.Fatalf("node answer: %+v", got)
				}
				resolution, ok := got.value.(proto.Resolution)
				if !ok || tc.kind == proto.IxQuestion && resolution.Answers["Which build?"] != "Debug" ||
					tc.kind == proto.IxPermission && (resolution.Allow == nil || !*resolution.Allow) ||
					tc.kind == proto.IxPlan && (resolution.Approve == nil || !*resolution.Approve) {
					t.Fatalf("node resolution: %+v", got.value)
				}
				status, saved, err := ft.st.InteractionOutcome(ctx, ix.ID)
				if err != nil || status != "resolved" || !reflect.DeepEqual(saved, &resolution) {
					t.Fatalf("persisted answer: %s %+v %v", status, saved, err)
				}
			case <-ctx.Done():
				t.Fatal("the work machine did not receive the phone's answer")
			}
			ft.waitPending(0)
		})
	}
}

// personad notify belongs to the hub's node request handler. Neither manager
// handler may claim or refuse it because the connection is a work machine.
func TestWorkMachineNotifyStillReachesHub(t *testing.T) {
	ft := newFleetTest(t)
	ft.d.machines["mac"].trust = "work"
	params := json.RawMessage(`{"title":"Build finished","session_id":"work-session"}`)
	for _, handler := range []func(context.Context, string, string, json.RawMessage) (any, bool, error){ft.s.NodeRequest, ft.s.NodeAnswer} {
		if value, handled, err := handler(context.Background(), "mac", "notify", params); handled || err != nil || value != nil {
			t.Fatalf("the manager intercepted a work notification: value=%v handled=%v error=%v", value, handled, err)
		}
	}
}
