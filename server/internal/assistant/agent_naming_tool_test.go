package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The actual assistant catalog includes the narrow write, never the coding Agents' catalog. Only two ids are accepted.
func TestAgentAutonameToolCatalog(t *testing.T) {
	s := newFleetTest(t).s
	found := false
	for _, tool := range s.Tools(context.Background()) {
		if tool.Name != "agent_autoname" {
			continue
		}
		found = true
		var schema struct {
			Required             []string        `json:"required"`
			Properties           map[string]any  `json:"properties"`
			AdditionalProperties json.RawMessage `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(schema.Required, []string{"machine", "session_id"}) || len(schema.Properties) != 2 || string(schema.AdditionalProperties) != "false" {
			t.Fatalf("the narrow schema: %s", tool.InputSchema)
		}
		for _, forbidden := range []string{"url", "headers", "token", "arguments", "title"} {
			if _, ok := schema.Properties[forbidden]; ok {
				t.Fatalf("credential or arbitrary input %q is accepted: %s", forbidden, tool.InputSchema)
			}
		}
		if !strings.Contains(tool.Description, "confirm this write on the phone every time") {
			t.Fatalf("the confirmation contract: %s", tool.Description)
		}
	}
	if !found || slices.Contains(agentToolNames, "agent_autoname") {
		t.Fatalf("assistant=%v, agent=%v", found, agentToolNames)
	}
	for _, tool := range s.sessionMCPTools(context.Background(), "mac", "task-agent", false, false) {
		if tool.Name == "agent_autoname" {
			t.Fatal("a coding Agent's catalog offers the assistant-only write")
		}
	}
}

func autonameToolFixture(t *testing.T, reply string) *fleetTest {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { namingReply(w, reply) }))
	t.Cleanup(model.Close)
	ft := newFleetTest(t)
	ft.d.add("personal", "Personal")
	ft.f.hello.Assistant.MachineID = "personal"
	ft.f.hello.Assistant.Machines = []string{"personal", "mac"}
	ft.f.ownModels = true
	ft.f.hello.Models.TriageAI = &ai.Config{BaseURL: model.URL, APIKey: "fixture", Model: "sorting", ReasoningEffort: "off"}
	ft.f.sessions["mac"] = nil // the node reads the transcript through the hub
	ft.f.items = []proto.Item{{Seq: 1, Role: "user", Kind: proto.KindText, Text: "修复 PDF 导出时丢失图片的问题。"}, {Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "正在检查导出图片的路径。"}}
	ft.sessions("mac",
		proto.Session{SessionID: "target-agent", Agent: "claude", Title: "Claude Code", TitleSource: proto.TitleSourceManual, Cwd: "/work/pdf-export-project", TitleManual: true, Status: proto.StatusIdle})
	ft.d.setSessions("personal", []proto.Session{{SessionID: "assistant-conversation", Agent: "claude", Title: "Persona", Cwd: "/home/u/.persona/assistant", Status: proto.StatusIdle}})
	return ft
}

func autonameToolResult(t *testing.T, s *Service, allow bool, args string) (proto.ToolResult, *proto.Interaction) {
	t.Helper()
	cards := make(chan *proto.Interaction, 1)
	go func() {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) { // sleep-ok: pendingQuestions exposes no signal for a card written by the tool goroutine
			if pending := s.pendingQuestions(); len(pending) == 1 {
				raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
				if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: pending[0].ID, Answer: raw}); err != nil {
					t.Error(err)
					cards <- nil
					return
				}
				cards <- &pending[0]
				return
			}
		}
		t.Error("no confirmation card")
		cards <- nil
	}()
	res := s.mcpCall(context.Background(), "personal", "assistant-conversation", "agent_autoname", json.RawMessage(args), true)
	var card *proto.Interaction
	select {
	case card = <-cards:
	case <-time.After(6 * time.Second):
		t.Fatal("the call did not ask for confirmation")
	}
	if card == nil {
		t.Fatal("the confirmation was not answered")
	}
	return res, card
}

// The normal assistant path confirms, then uses the same naming request and publishes the same automatic row. A
// refused call never reaches the model; every allowed call asks again rather than creating a standing permission.
func TestAgentAutonameToolConfirmsAndUsesTheSharedRequest(t *testing.T) {
	ft := autonameToolFixture(t, "修复 PDF 导出图片")
	args := `{"machine":"mac","session_id":"target-agent"}`
	res, card := autonameToolResult(t, ft.s, false, args)
	if res.IsError || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "declined") || card.ToolName != "agent_autoname" ||
		card.SessionID != "assistant-conversation" || !strings.Contains(card.Summary, "pdf-export-project") {
		t.Fatalf("declined: %+v, card %+v", res, card)
	}
	if rec := agentNameRecordOf(ft.s, "mac", "target-agent"); rec.Title != "" || rec.Requested {
		t.Fatalf("a declined call changed the title: %+v", rec)
	}
	res, card = autonameToolResult(t, ft.s, true, args)
	var answer map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &answer); err != nil || res.IsError || answer["title"] != "修复 PDF 导出图片" || answer["title_source"] != "auto" || answer["changed"] != true {
		t.Fatalf("allowed: %s, %+v, %v", res.Content[0].Text, res, err)
	}
	var input map[string]string
	if err := json.Unmarshal(card.ToolInput, &input); err != nil || len(input) != 2 || input["machine"] != "mac" || input["session_id"] != "target-agent" {
		t.Fatalf("only the scoped ids are on the card: %s", card.ToolInput)
	}
	row, ok := ft.s.listedSession("mac", "target-agent")
	if !ok || row.Title != "修复 PDF 导出图片" || row.TitleSource != proto.TitleSourceAuto || row.TitleManual {
		t.Fatalf("list row: %+v, %v", row, ok)
	}
	if !agentNameRecordOf(ft.s, "mac", "target-agent").Requested {
		t.Fatal("the shared ledger was not written")
	}
	res, _ = autonameToolResult(t, ft.s, true, args)
	if !res.IsError && len(ft.s.pendingQuestions()) != 0 {
		t.Fatal("an allowed call did not consume its one confirmation")
	}
	for _, call := range ft.d.callsTo("mac", assistantwire.DeviceSessionRename) {
		t.Fatalf("the tool typed into the Agent: %s", call)
	}
}

func TestAgentAutonameToolRefusesBeforeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, args, want string
	}{
		{"unknown fields including credentials", `{"machine":"mac","session_id":"target-agent","url":"http://example.invalid","token":"private-token"}`, "unknown field"},
		{"session prefix", `{"machine":"mac","session_id":"target-a"}`, "full machine and session ids"},
		{"machine missing", `{"session_id":"target-agent"}`, "full machine and session ids"},
		{"assistant conversation", `{"machine":"mac","session_id":"assistant-conversation"}`, "not a coding agent"},
		{"unknown session", `{"machine":"mac","session_id":"no-such-agent"}`, "no agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := autonameToolFixture(t, "修复 PDF 导出图片")
			if tc.name == "assistant conversation" {
				m, _ := ft.s.fleetMachine("personal")
				ft.d.setSessions("mac", append(ft.d.machines["mac"].sessions,
					proto.Session{SessionID: "own-conversation", Agent: "claude", Title: "Persona", Cwd: m.assistantCwd(), Status: proto.StatusIdle}))
				tc.args = `{"machine":"mac","session_id":"own-conversation"}`
			}
			res := ft.s.mcpCall(context.Background(), "personal", "assistant-conversation", "agent_autoname", json.RawMessage(tc.args), true)
			if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) || strings.Contains(res.Content[0].Text, "private-token") {
				t.Fatalf("answer: %+v", res)
			}
			if len(ft.s.pendingQuestions()) != 0 {
				t.Fatal("a refused call asked for confirmation")
			}
			if rec := agentNameRecordOf(ft.s, "mac", "target-agent"); rec.Title != "" {
				t.Fatalf("a refused call renamed: %+v", rec)
			}
		})
	}
}

func TestAgentAutonameToolReturnsTheSharedCodedFailure(t *testing.T) {
	ft := autonameToolFixture(t, "unused")
	ft.f.setItems()
	var mu sync.Mutex
	var lines []string
	ft.s.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	res, _ := autonameToolResult(t, ft.s, true, `{"machine":"mac","session_id":"target-agent"}`)
	var answer map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &answer); err != nil || res.IsError || answer["ok"] != false || answer["code"] != "no_dialogue" || answer["retryable"] != false || answer["error"] == "" {
		t.Fatalf("refusal: %+v, %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if log := strings.Join(lines, "\n"); !strings.Contains(log, `requested session="mac"/"target-agent" reason=no_dialogue`) {
		t.Fatalf("the shared failure log: %s", log)
	}
}

// An ordinary coding Agent cannot claim to be the assistant, even by calling the tool directly. The machine check
// happens before the naming request, so there is no confirmation or title write.
func TestAgentAutonameToolRefusesAnOrdinaryAgent(t *testing.T) {
	ft := autonameToolFixture(t, "修复 PDF 导出图片")
	res := ft.s.mcpCall(context.Background(), "mac", "target-agent", "agent_autoname",
		json.RawMessage(`{"machine":"mac","session_id":"target-agent"}`), true)
	if !res.IsError || len(ft.s.pendingQuestions()) != 0 || agentNameRecordOf(ft.s, "mac", "target-agent").Title != "" {
		t.Fatalf("the ordinary Agent was authorized: %+v", res)
	}
	if got := ft.s.agentDisplayName("mac", proto.Session{SessionID: "target-agent"}); got != "" {
		t.Fatalf("title: %q", got)
	}
}
