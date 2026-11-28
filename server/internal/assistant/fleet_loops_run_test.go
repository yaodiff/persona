package assistant

import (
	"context"
	"strings"
	"testing"
)

// A manager run's fleet loops end with the run (#16215). FleetUp used to start them with the run's cancellation taken
// off, so a run that ended (its token revoked, a restart in place for a new binary) left liveLoop reading the run's
// closed store every 15 s ("assistant: settings: closed pool") and telling the hub an old fleet, and expireLoop with
// it. The seen times FleetUp reads are still read.
func TestFleetLoopsEndWithTheRun(t *testing.T) {
	loops := func() map[string]string { // goroutine id → the loop it runs
		out := map[string]string{}
		for _, stack := range goroutineStacks() {
			for _, loop := range []string{"liveLoop", "expireLoop"} {
				if strings.Contains(stack, "(*Service)."+loop+"(") {
					out[goroutineID(stack)] = loop
				}
			}
		}
		return out
	}
	before := loops() // a loop an earlier test left behind is not this run's
	ft := newFleetTest(t)
	run, end := context.WithCancel(context.Background())
	defer end()
	ft.s.FleetUp(run)

	started := map[string]string{}
	waitFor(t, "the fleet's two loops to start", func() bool {
		for id, loop := range loops() {
			if _, old := before[id]; !old {
				started[id] = loop
			}
		}
		return len(started) == 2
	})
	ft.s.fleet.mu.Lock()
	loaded := ft.s.fleet.seenLoaded
	ft.s.fleet.mu.Unlock()
	if !loaded {
		t.Fatal("FleetUp did not read the seen times")
	}

	end()
	waitFor(t, "the fleet's loops to end with the run", func() bool {
		now := loops()
		for id := range started {
			if _, running := now[id]; running {
				return false
			}
		}
		return true
	})
}
