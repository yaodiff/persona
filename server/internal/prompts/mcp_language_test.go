package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Recorded from the unlocalized upstream tool inventory, including task_plan
// ownership (#13362), agent_supervise leader adoption (#13357), default
// cross-machine/cross-harness A2A subagent launches (#14215), and the Agent
// presets an Agent starts another on (#14672). This covers the description
// and schema of every tool, not only a representative golden.
func TestMCPLegacyBytes(t *testing.T) {
	raw, err := os.ReadFile("testdata/mcp_legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	// These tools were introduced after the legacy inventory was frozen.
	newTools := []string{"decision_record", "decisions_query", "space_state_update", "lark_image", "ask_user_withdraw", "conversations_switch", "health_strength", "file_transfer", "a2a_read", "page", "model_profile_capture", "model_profile_deactivate", "model_profile_clear", "model_probe", "launcher_import", "external_sources_read", "external_sources_write", "bot_group_reply", "todo_split", "todo_merge", "browser_fetch_download", "mail_move", "orca_bot_list", "orca_bot_read", "orca_bot_send", "agent_modes"}
	for _, name := range newTools {
		if _, exists := expected[name]; exists {
			t.Fatalf("%s is not a legacy tool", name)
		}
	}
	if len(expected)+len(newTools) != len(mcpTools()) {
		t.Fatal("legacy inventory changed; classify new tools explicitly")
	}
	for name, want := range expected {
		for _, lang := range []string{"", "zh-Hans"} {
			tool := Tool(name, lang)
			digest := sha256.Sum256([]byte(tool.Description + "\x00" + string(tool.InputSchema)))
			if got := hex.EncodeToString(digest[:]); got != want {
				t.Errorf("%s/%s: legacy digest = %s, want %s", name, lang, got, want)
			}
		}
	}
}

func withoutDescriptions(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "description")
		for k, value := range x {
			x[k] = withoutDescriptions(value)
		}
	case []any:
		for i, value := range x {
			x[i] = withoutDescriptions(value)
		}
	}
	return v
}

func TestMCPLanguagesKeepSchemasAndSourceContracts(t *testing.T) {
	for name := range mcpTools() {
		var original any
		if err := json.Unmarshal(Tool(name).InputSchema, &original); err != nil {
			t.Fatal(err)
		}
		for _, lang := range []string{"en", "ja"} {
			tool := Tool(name, lang)
			var localized any
			if err := json.Unmarshal(tool.InputSchema, &localized); err != nil {
				t.Fatalf("%s/%s: %v", name, lang, err)
			}
			if tool.Name != name || !reflect.DeepEqual(withoutDescriptions(localized), withoutDescriptions(original)) {
				t.Errorf("%s/%s: schema contract changed", name, lang)
			}
			if tool.Description == Tool(name).Description {
				t.Errorf("%s/%s has no language guidance", name, lang)
			}
		}
	}
	for _, lang := range []string{"en", "ja"} {
		for _, prefix := range []string{"状态：", "停止：", "回答："} {
			if !strings.Contains(Tool("delegate_task", lang).Description, prefix) {
				t.Errorf("%s lost %s", lang, prefix)
			}
		}
		for _, name := range []string{"task_plan", "task_scope", "agent_send", "agent_supervise", "task_review", "leader_progress"} {
			if strings.Contains(string(Tool(name, lang).InputSchema), "Chinese") {
				t.Errorf("%s/%s still requires Chinese output", name, lang)
			}
		}
	}
	for _, literal := range []string{"{{since}}", "{{since_date}}"} {
		if !strings.Contains(Tool("subscription_sources", "en").Description, literal) {
			t.Errorf("lost recipe literal %s", literal)
		}
	}
}

func TestMCPLocalizationPreservesAuthoredToolsAndAliases(t *testing.T) {
	for _, name := range []string{"task_plan", "unknown_tool"} {
		authored := MCPTool{Name: name, Description: "An extension's own definition", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
		if got := LocalizeTool(authored, "ja"); !reflect.DeepEqual(got, authored) {
			t.Fatal("rewrote authored tool")
		}
	}
	changedSchema := Tool("task_plan")
	changedSchema.InputSchema = json.RawMessage(`{"type":"object","properties":{"external_only":{"type":"string"}}}`)
	if got := LocalizeTool(changedSchema, "ja"); !reflect.DeepEqual(got, changedSchema) {
		t.Fatal("rewrote a source-specific schema")
	}
	leader := Tool("leader_task_plan")
	leader.Name = "task_plan"
	got := LocalizeTool(leader, "ja")
	if got.Name != "task_plan" || got.Description != Tool("leader_task_plan", "ja").Description {
		t.Fatal("lost restricted leader definition")
	}
	if again := LocalizeTool(got, "en"); again.Description != Tool("leader_task_plan", "en").Description {
		t.Fatal("failed language change")
	}
	raw := Tool("task_plan", "en")
	raw.InputSchema[0] = 'X'
	if Tool("task_plan", "en").InputSchema[0] != '{' {
		t.Fatal("caller mutated cached schema")
	}
}
