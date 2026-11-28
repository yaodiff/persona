// Package agentprompt holds the prompt-shape rules both the manager and the node must agree on.
//
//persona:layer library
package agentprompt

import "strings"

// Antigravity is the agent kind whose native workflow commands must stay at byte 0.
const Antigravity = "agy"

var antigravityWorkflowCommands = map[string]bool{
	"/boost":    true,
	"/teamwork": true,
	"/swarm":    true,
	"/goal":     true,
	"/plan":     true,
	"/grill-me": true,
	"/learn":    true,
	"/schedule": true,
	"/browser":  true,
}

// WorkflowCommand returns text with leading ASCII whitespace removed when it begins with one of
// Antigravity's native workflow slash commands. It deliberately does not trim Unicode whitespace or
// accept Unicode case/folding variants: agy recognizes the exact command token.
func WorkflowCommand(text string) (string, bool) {
	trimmed := strings.TrimLeft(text, " \t\r\n\v\f")
	if trimmed == "" || trimmed[0] != '/' {
		return "", false
	}
	end := len(trimmed)
	for i := 0; i < len(trimmed); i++ {
		if isASCIISpace(trimmed[i]) {
			end = i
			break
		}
	}
	return trimmed, antigravityWorkflowCommands[trimmed[:end]]
}

// WithContext places context before text, except for an Antigravity workflow command: there the
// command and its body stay first so agy sees "/<cmd>" at byte 0, and Persona context follows.
func WithContext(agent, text, context string) string {
	if agent == Antigravity {
		if ordered, ok := WorkflowCommand(text); ok {
			if context == "" {
				return ordered
			}
			return ordered + "\n\n" + context
		}
	}
	if context == "" {
		return text
	}
	return context + "\n\n" + text
}

func isASCIISpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}
