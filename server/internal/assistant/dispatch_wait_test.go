package assistant

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// `personad tool` waits for dispatch_read as long as the read itself may wait, plus its own short deadline
// (assistantwire.ReadToolWaits, #14400). A longer wait here would have the command line give up on a read that is
// still waiting as asked.
func TestDispatchReadWaitIsTheCommandLines(t *testing.T) {
	if dispatchWaitMost != assistantwire.DispatchReadWaitMost {
		t.Fatalf("dispatch_read waits up to %v, but personad tool allows for %v (assistantwire.DispatchReadWaitMost)",
			dispatchWaitMost, assistantwire.DispatchReadWaitMost)
	}
}
