package devicewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// MaxTerminalAgentMessageBytes caps the plain text of one terminal send.
const MaxTerminalAgentMessageBytes = 64 << 10

// ClientOpTarget is the canonical path and normalized query stored on a receipt.
func ClientOpTarget(path, query string) json.RawMessage {
	target, _ := json.Marshal(map[string]string{"path": path, "query": query})
	return target
}

// ClientOpTargetWithOccurrence also pins an interaction action's normalized occurrence.
// Receipts created without an occurrence keep ClientOpTarget's original digest.
func ClientOpTargetWithOccurrence(path, query, occurrence string) json.RawMessage {
	target, _ := json.Marshal(map[string]string{"path": path, "query": query, "occurrence": occurrence})
	return target
}

// ClientOpDigest binds a receipt to the target and normalized input the hub claimed.
// The caller must normalize or scrub any fields the operation's contract requires.
func ClientOpDigest(kind string, target json.RawMessage, valid bool, params any) string {
	canonical, _ := json.Marshal([]any{kind, target, valid, params})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
