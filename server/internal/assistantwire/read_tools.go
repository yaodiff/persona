package assistantwire

import "time"

// ReadToolWaits are the Agents' own tools that only read, each with the longest it waits by design before it answers:
// dispatch_read waits for its task's turn to end (wait_seconds, at most DispatchReadWaitMost), the others answer at
// once. A read changes nothing, so a call that got no answer can simply be made again: `personad tool` gives these a
// short deadline on top of that wait, and every other tool the long one a write waiting for the owner's answer needs
// (internal/toolcli, #14400). a2a_read reads another session's conversation (#13831).
var ReadToolWaits = map[string]time.Duration{
	"a2a_sessions": 0, "a2a_get": 0, "a2a_read": 0,
	"dispatch_read":   DispatchReadWaitMost,
	"resource_status": 0, "resource_get": 0,
	"todo_decisions": 0, "todo_repositories_get": 0,
}

// DispatchReadWaitMost is the longest dispatch_read waits for its task's turn to end before it answers (the manager's
// dispatchWaitMost, in internal/assistant/dispatch.go; a test there keeps the two equal).
const DispatchReadWaitMost = 50 * time.Second
