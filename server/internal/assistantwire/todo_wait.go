package assistantwire

import "github.com/nwverse/persona/server/internal/wireids"

// TodoHandoffRef names another to-do that a handoff-parked task waits on (#16168).
// Kept in the contract package so todo/projection reads it without declaring an application record.
type TodoHandoffRef struct {
	TodoID wireids.TodoID `json:"todo_id"`
	Title  string         `json:"title"`
	Check  string         `json:"check"`
}
