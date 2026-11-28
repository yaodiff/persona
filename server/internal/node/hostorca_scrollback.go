package node

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
)

// What a client shows above an Orca terminal's screen, and how often it reads that screen (#17048).
//
// Orca keeps a terminal's output two ways, both read on its local socket with terminal.read and both stripped of
// escapes (measured on Orca 1.4.277):
//   - its output tail (no screen, no cursor): the newest lines, with rows a program redrew in place kept as they ended,
//     as a terminal's scrollback shows them. At most 2000 lines and 256 KiB are kept, and one read sends at most
//     32 KiB of them, the newest. For a program on the alternate screen Orca answers with the screen instead.
//   - its log by cursor: every completed line, also each redraw of a program's live rows (a spinner, an agent's input
//     box), so a terminal running an agent logs the same rows again and again. At most 2000 lines and 256 KiB.
//
// So the tail is the scrollback, and the log only reaches further back where it agrees with the tail line for line
// (plain output: a build, a shell), beyond the 32 KiB one tail read sends.

var _ host.Scrollbacker = (*orcaHost)(nil)
var _ host.QuickScreen = (*orcaHost)(nil)

// orcaScreenRefresh: a watched Orca screen may be read this often. One read is one request on the local socket that
// Orca answers from its own emulator's state, a fraction of a millisecond of its time (measured, #17048).
const orcaScreenRefresh = 250 * time.Millisecond

// ScreenRefresh: Orca's screen read is cheap (orcaScreenRefresh).
func (h *orcaHost) ScreenRefresh() time.Duration { return orcaScreenRefresh }

// Scrollback is Orca's output tail for the pane, reaching further back through its log when the tail holds more than
// one read sends and the log agrees with it.
func (h *orcaHost) Scrollback(ctx context.Context, pane host.PaneRef, lines int) ([]string, bool, error) {
	limit := lines
	if lines <= 0 || lines > orcart.TailLimit {
		limit = orcart.TailLimit
	}
	t, err := h.terminal(ctx, pane.ID, false)
	if err != nil {
		return nil, false, orcaErr(err)
	}
	var tail []string
	var truncated, limited bool
	var source string
	var tg orcart.Target
	err = h.onTerminal(ctx, t, false, func(target orcart.Target) error {
		var e error
		tail, truncated, limited, source, e = h.c.ReadTail(ctx, target, limit)
		tg = target
		return e
	})
	if err != nil {
		return nil, false, orcaErr(err)
	}
	if source == "screen" {
		return nil, false, nil // the alternate screen: nothing is above it
	}
	if !limited || len(tail) >= limit {
		return tail, truncated || limited, nil
	}
	// One read sent less than Orca keeps (32 KiB): the log, where it lines up with the tail, goes further back.
	// An Orca without cursor reads (orcart.ErrNoCursor), or one failing now (the client logs that), leaves the tail.
	log, oldest, err := h.c.ReadLog(ctx, tg, 0, orcart.TailLimit)
	if err != nil {
		return tail, true, nil
	}
	at := joinOrcaLog(log, tail)
	if at < 0 {
		return tail, true, nil
	}
	older := log[:at]
	if room := lines - (len(tail) - 1); lines > 0 && len(older) > room {
		older = older[len(older)-max(room, 0):]
	}
	return append(append([]string{}, older...), tail[1:]...), oldest > 0 || len(older) < at, nil
}

// joinOrcaLog is where in log the tail's second line is (its first may be cut at the read's size limit), -1 when the
// log and the tail do not agree from there on: every completed line after it in the log is the tail's next one, and
// at most the line still being written follows in the tail. A program that redrew rows in place makes them differ,
// and its log is not shown.
func joinOrcaLog(log, tail []string) int {
	if len(tail) < 3 {
		return -1
	}
	rest := tail[1:]
	same := func(a, b string) bool { return strings.TrimRight(a, " ") == strings.TrimRight(b, " ") }
	for at := max(0, len(log)-len(rest)); at < len(log); at++ {
		n := len(log) - at
		if len(rest)-n > 1 {
			continue // more than the line being written would be missing from the log
		}
		ok := true
		for i := range n {
			if !same(log[at+i], rest[i]) {
				ok = false
				break
			}
		}
		if ok {
			return at
		}
	}
	return -1
}
