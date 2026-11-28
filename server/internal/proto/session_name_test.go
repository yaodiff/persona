package proto

import "testing"

// RenamableTabName is the tab label Persona's guarded naming compares (#15674): herdr's and the Herdr App's as
// HerdrTabName gives it, and Orca's too (#17043), which HerdrTabName leaves out because clients mirror it for display.
func TestRenamableTabName(t *testing.T) {
	for _, c := range []struct {
		name string
		s    Session
		want string
	}{
		{"herdr", Session{SpaceKind: "herdr", SlotID: "w1:t1", SlotName: " fix-login "}, "fix-login"},
		{"herdr app", Session{SpaceKind: "herdr-core", SlotID: "t1", SlotName: "fix-login"}, "fix-login"},
		{"orca", Session{SpaceKind: "orca", SlotID: "tab1", SlotName: "Terminal"}, "Terminal"},
		{"orca by host", Session{Host: "orca", TabID: "tab1", SlotName: "fix-login"}, "fix-login"},
		{"orca without a tab", Session{SpaceKind: "orca", SlotName: "fix-login"}, ""},
		{"orca assistant", Session{SpaceKind: "orca", SlotID: "tab1", SlotName: "fix-login", Assistant: true}, ""},
		{"tern", Session{SpaceKind: "tern", SlotID: "b1", SlotName: "fix-login"}, ""},
	} {
		if got := c.s.RenamableTabName(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := (Session{SpaceKind: "orca", SlotID: "tab1", SlotName: "fix-login"}).HerdrTabName(); got != "" {
		t.Errorf("HerdrTabName of an Orca tab = %q: clients display it as herdr's only", got)
	}
}
