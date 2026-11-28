package proto

import (
	"strings"
	"testing"
)

// A prompt is a short last line that ends like one; whether it reads as secret decides only how the apps show the field.
func TestCommandPrompt(t *testing.T) {
	for _, tc := range []struct {
		line           string
		prompt         string
		secret, wanted bool
	}{
		{"Enter the authorization code: ", "Enter the authorization code:", true, true},
		{"[sudo] password for me:", "[sudo] password for me:", true, true},
		{"Paste your API token:", "Paste your API token:", true, true},
		{"Enter passphrase for key '~/.ssh/id_ed25519': ", "Enter passphrase for key '~/.ssh/id_ed25519':", true, true},
		{"Continue? [y/N] ", "Continue? [y/N]", false, true},
		{"Do you want to proceed (yes/no)", "Do you want to proceed (yes/no)", false, true},
		{">>> ", ">>>", false, true},
		{"Project name:", "Project name:", false, true},
		{"请输入验证码：", "请输入验证码：", true, true},
		{"プロジェクト名？", "プロジェクト名？", false, true},
		{"  45% |#####     | 1.2 GB/2.7 GB", "", false, false},
		{"Downloading model", "", false, false},
		{"", "", false, false},
		{"   ", "", false, false},
		{strings.Repeat("a", CommandInputMaxPrompt) + ":", "", false, false},
	} {
		prompt, secret, ok := CommandPrompt(tc.line)
		if prompt != tc.prompt || secret != tc.secret || ok != tc.wanted {
			t.Errorf("CommandPrompt(%q) = %q, %v, %v; want %q, %v, %v", tc.line, prompt, secret, ok, tc.prompt, tc.secret, tc.wanted)
		}
	}
}

// A line is typed as one line: no line break or other control key, not empty, not past the limit, valid text.
func TestCommandInputLineProblem(t *testing.T) {
	for line, want := range map[string]string{
		"test-code-123":         "",
		"pass word\twith a tab": "",
		"日本語のパスワード":             "",
		"":                      "empty",
		"one\ntwo":              "control",
		"one\rtwo":              "control",
		"stop\x03":              "control",
		"esc\x1b[A":             "control",
		"line\u2028separator":   "control",
		"\xff\xfe":              "not_text",
		strings.Repeat("x", CommandInputMaxLine+1): "too_long",
	} {
		if got := CommandInputLineProblem(line); got != want {
			t.Errorf("CommandInputLineProblem(%q) = %q, want %q", line, got, want)
		}
	}
}

// Only the node's shape passes for a command-input card: anything else naming the tool is refused by the manager.
func TestIsCommandInputCard(t *testing.T) {
	good := Interaction{ID: CommandInputPrefix + "0123456789abcdef", Kind: IxPermission, ToolName: CommandInputTool,
		Fields: []Field{{Key: CommandInputField, Label: "Password:", Secret: true}}}
	if !IsCommandInputCard(good) {
		t.Fatal("the node's card was refused")
	}
	for name, change := range map[string]func(*Interaction){
		"another id":         func(ix *Interaction) { ix.ID = "ix_1" },
		"a question":         func(ix *Interaction) { ix.Kind = IxQuestion },
		"another tool":       func(ix *Interaction) { ix.ToolName = "Bash" },
		"no field":           func(ix *Interaction) { ix.Fields = nil },
		"two fields":         func(ix *Interaction) { ix.Fields = append(ix.Fields, Field{Key: "more"}) },
		"a field not secret": func(ix *Interaction) { ix.Fields = []Field{{Key: CommandInputField}} },
		"an optional field": func(ix *Interaction) {
			ix.Fields = []Field{{Key: CommandInputField, Secret: true, Optional: true}}
		},
		"another key":  func(ix *Interaction) { ix.Fields = []Field{{Key: "password", Secret: true}} },
		"always grant": func(ix *Interaction) { ix.AlwaysKeys = []string{"tool:command_input"} },
	} {
		ix := good
		ix.Fields = append([]Field(nil), good.Fields...)
		change(&ix)
		if IsCommandInputCard(ix) {
			t.Errorf("%s: passed for a command-input card", name)
		}
	}
}
