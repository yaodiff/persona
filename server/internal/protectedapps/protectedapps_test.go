package protectedapps

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIs(t *testing.T) {
	for _, c := range []struct {
		id, name string
		want     bool
	}{
		{"com.apple.systempreferences", "Réglages Système", true},
		{"", "System Settings", true},
		{"name:系统设置", "系统设置", true},
		{"", "com.apple.SecurityAgent", true},
		{"", "coreautha", true},
		{"com.apple.calculator", "Calculator", false},
		{"", "Settings", false},
	} {
		if got := Is(c.id, c.name); got != c.want {
			t.Errorf("%q %q: %v", c.id, c.name, got)
		}
	}
	// The prompts are strict (nothing is operated while one shows), System Settings is not one of them.
	if !IsPrompt("", "universalAccessAuthWarn") || !IsPrompt("com.apple.SecurityAgent", "") || IsPrompt("com.apple.systempreferences", "System Settings") {
		t.Error("IsPrompt")
	}
	if _, ok := Windows(json.RawMessage(`{"ok": true}`)); ok {
		t.Fatal("not a window list")
	}
}

func TestScriptAndStepsApps(t *testing.T) {
	if apps, all := ScriptApps(`const a = await cua.getApp("Calculator"); await cua.launch('Notes')`); strings.Join(apps, ",") != "Calculator,Notes" || !all {
		t.Errorf("literal: %v %v", apps, all)
	}
	if _, all := ScriptApps(`const a = await cua.getApp(name)`); all {
		t.Error("by expression: not all known")
	}
	if apps, all := StepsApps("Notes", json.RawMessage(`[{"launch_app": {"bundle_id": "com.apple.systempreferences"}}, {"click": {"name": "x"}}]`)); strings.Join(apps, ",") != "com.apple.systempreferences,Notes" || !all {
		t.Errorf("steps: %v %v", apps, all)
	}
	if !UnnamedSteps("", json.RawMessage(`[{"press_key": {"key": "return"}}]`)) || UnnamedSteps("Notes", json.RawMessage(`[{"press_key": {"key": "return"}}]`)) {
		t.Error("unnamed steps")
	}
}

func TestActs(t *testing.T) {
	for tool, want := range map[string]bool{"click": true, "run_script": true, "some_new_tool": true, "list_windows": false, "get_window_state": false} {
		if Acts(tool) != want {
			t.Errorf("%s: %v", tool, !want)
		}
	}
}
