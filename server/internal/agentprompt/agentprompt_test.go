package agentprompt

import "testing"

func TestWorkflowCommand(t *testing.T) {
	for _, text := range []string{
		"/boost fix the race",
		" \t\n/goal ship the release",
		"/teamwork\nwrap the branch",
		"/swarm split the work",
		"/plan create the migration",
		"/grill-me /Users/example/project",
		"/learn the codebase",
		"/schedule tomorrow",
		"/browser /tmp/report.html",
	} {
		got, ok := WorkflowCommand(text)
		if !ok || got[0] != '/' {
			t.Errorf("WorkflowCommand(%q) = %q,%v", text, got, ok)
		}
	}
	for _, text := range []string{
		"/compact",
		"/status now",
		"/model sonnet",
		"/Users/example/project",
		"/tmp/report",
		"/plan/Users/example",
		"/boosted finish",
		"/planning",
		"／plan create",
		"/ＰＬＡＮ create",
		"please /plan this",
		"ordinary prose",
	} {
		if got, ok := WorkflowCommand(text); ok {
			t.Errorf("WorkflowCommand(%q) unexpectedly matched as %q", text, got)
		}
	}
}

func TestWithContextKeepsOnlyAntigravityCommandsFirst(t *testing.T) {
	const context = "<persona-context>background</persona-context>"
	if got, want := WithContext(Antigravity, " /boost fix\nkeep this", context), "/boost fix\nkeep this\n\n"+context; got != want {
		t.Fatalf("agy prompt = %q, want %q", got, want)
	}
	if got, want := WithContext("claude", "/boost fix", context), context+"\n\n/boost fix"; got != want {
		t.Fatalf("Claude prompt = %q, want %q", got, want)
	}
	if got, want := WithContext(Antigravity, "/tmp/report", context), context+"\n\n/tmp/report"; got != want {
		t.Fatalf("path prompt = %q, want %q", got, want)
	}
	if got, want := WithContext(Antigravity, " \t/boost fix", ""), "/boost fix"; got != want {
		t.Fatalf("contextless workflow prompt = %q, want %q", got, want)
	}
	if got, want := WithContext("claude", " \t/boost fix", ""), " \t/boost fix"; got != want {
		t.Fatalf("contextless non-agy prompt = %q, want %q", got, want)
	}
}
