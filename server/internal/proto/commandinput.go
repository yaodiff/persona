package proto

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Commands waiting for input (#14527, docs/protocol/protocol.md "Commands waiting for input"): a command an Agent runs
// that stops at a prompt (a password, a pasted token, a sign-in code) is a permission card the owner answers with the
// line to type, delivered straight into the command through its engine's own input, never through the model.

// CommandInputTool is the card's tool name. Only a node makes such a card, in the list of cards it reads off its
// Agents (codex.interactions); the manager refuses it from every other path, as it refuses SecretRunTool.
const CommandInputTool = "command_input"

// CommandInputPrefix starts every command-input card's id.
const CommandInputPrefix = "ci_"

// CommandInputField is the key of the card's one field: the line to type. The field is secret whatever the prompt
// says, so what is typed is never stored (Interaction.Fields); CommandInput.Masked only says how the apps show it. It is
// required: an Allow without a line (an app that draws the card as a plain permission) types nothing.
const CommandInputField = "input"

// Limits on a card and its answer.
const (
	CommandInputMaxPrompt = 300  // characters of the prompt line
	CommandInputMaxOutput = 2048 // bytes of the output's last lines on the card
	CommandInputMaxLine   = 4096 // bytes of the line an answer types
)

// CommandInput is a command-input card's tool_input.
type CommandInput struct {
	// Command is what runs, in the words the Agent gave it ("" when unknown).
	Command string `json:"command,omitempty"`
	// Task is the engine's own name for it (an Antigravity background task's "task-12").
	Task string `json:"task,omitempty"`
	// Prompt is the line the command waits on, escapes removed.
	Prompt string `json:"prompt"`
	// Output is the output's last lines before the prompt, escapes and blank lines removed, at most CommandInputMaxOutput
	// bytes.
	Output string `json:"output,omitempty"`
	// Masked: the prompt reads as secret (a password, a token, a code), so the apps hide what is typed until Show.
	Masked bool `json:"masked,omitempty"`
}

// IsCommandInputCard says ix has the shape of a command-input card: the reserved tool name on a permission card whose
// id has the prefix, with exactly the one secret, required field. Any other card naming the tool is refused.
func IsCommandInputCard(ix Interaction) bool {
	return ix.Kind == IxPermission && ix.ToolName == CommandInputTool && strings.HasPrefix(ix.ID, CommandInputPrefix) &&
		len(ix.Fields) == 1 && ix.Fields[0].Key == CommandInputField && ix.Fields[0].Secret && !ix.Fields[0].Optional &&
		len(ix.AlwaysKeys) == 0
}

// CommandInputLineProblem is why line cannot be typed into a waiting command, "" when it can: one non-empty line of
// printable text (a tab allowed) no longer than CommandInputMaxLine, valid UTF-8. A line break or another control
// character would press a key the owner did not see: Enter early, or Ctrl-C.
func CommandInputLineProblem(line string) string {
	switch {
	case line == "":
		return "empty"
	case len(line) > CommandInputMaxLine:
		return "too_long"
	case !utf8.ValidString(line):
		return "not_text"
	case strings.ContainsFunc(line, func(r rune) bool { return r != '\t' && (unicode.IsControl(r) || r == '\u2028' || r == '\u2029') }):
		return "control"
	}
	return ""
}

// commandPromptEnd is how a prompt ends: a colon or a question mark (either width), a REPL's angle bracket, a closing
// bracket or parenthesis ("[y/N]", "(yes/no)"), each maybe followed by spaces. Compiled on first use with the standard
// library: a contract imports no internal/lazyre (#11793).
var commandPromptEnd = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`[:?>\])：？]\s*$`) })

// commandPromptSecret are the words that make a prompt read as secret (masked by default), in English and in the
// prompts' own words in Chinese and Japanese (data matched as written).
var commandPromptSecret = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(?:pass(?:word|phrase|code)?|pin|otp|token|secret|key|code|credentials?|api[ _-]?key|2fa|mfa|verification)\b|` +
		`密码|口令|验证码|令牌|密钥|パスワード|暗証|認証コード|確認コード|トークン`)
})

// CommandPromptMasked reports whether a prompt asks for a secret, including a runtime prompt with no trailing punctuation.
func CommandPromptMasked(line string) bool { return commandPromptSecret().MatchString(line) }

// CommandPrompt says whether line, a command's last output line that no newline ended, reads as a prompt waiting
// for input, and whether it reads as secret. line has its escapes removed already. A prompt is at most
// CommandInputMaxPrompt characters and ends like one (commandPromptEnd); progress output does not stop on such a line.
func CommandPrompt(line string) (prompt string, secret, ok bool) {
	prompt = strings.TrimSpace(line)
	if prompt == "" || utf8.RuneCountInString(prompt) > CommandInputMaxPrompt || !commandPromptEnd().MatchString(prompt) {
		return "", false, false
	}
	return prompt, CommandPromptMasked(prompt), true
}
