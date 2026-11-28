package node

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// Claude Code's status line while it works (docs/protocol/protocol.md "Live status"): a spinner, the verb with an ellipsis and, in
// parentheses, the parts it has right now separated by "·":
//
//	✻ Frosting… (16m 15s · ↓ 22.8k tokens · thinking with xhigh effort)
//
// Only the screen has it (the transcript does not), so the node reads it off the pane while a client watches.

var (
	// statusLineRE: a spinner at column 0, the verb, "…" (or "..."), and the rest. Claude's own output starts with
	// "⏺", tool results and tips are indented, and what the user wrote starts with "❯" or ">": none of those match.
	statusLineRE = lazyre.New(`^(\S) +([^\s….(][^….(]*?)(?:…|\.\.\.)\s*(.*)$`)
	retryLineRE  = lazyre.New(`^(\S) +(.+) · (?i:retrying) in \d+(?:\.\d+)?s · attempt (\d+)/(\d+)\s*$`)
	durationRE   = lazyre.New(`^(?:(\d+)h)?\s*(?:(\d+)m)?\s*(?:(\d+)s)?$`)
	tokensRE     = lazyre.New(`^([↓↑])\s*([\d.,]+)\s*([kKmM]?)\s*tokens?$`)
)

// notSpinner are the column-0 marks Claude draws that are not a spinner: its replies, the prompt, quotes, tool output.
const notSpinner = "⏺❯>⎿│|─━╭╰"

// parseClaudeStatus finds the status line above Claude Code's input box and reads it. Rows are taken from the box's
// top rule upwards: blank and indented rows (the tip, the todo list, a right-aligned "/goal" pill) are skipped, and
// the first row that starts at column 0 is the status line or there is none — so a status line quoted further up in
// the conversation is never taken for the live one.
func parseClaudeStatus(text string) (proto.SessionLive, bool) {
	lines := screen.AboveInputBox(text)
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines[i], " \t\u00a0")
		if l == "" || l[0] == ' ' || l[0] == '\t' {
			continue
		}
		return parseStatusLine(l)
	}
	return proto.SessionLive{}, false
}

func parseStatusLine(line string) (proto.SessionLive, bool) {
	if live, ok := parseRetryStatusLine(line); ok {
		return live, true
	}
	m := statusLineRE.FindStringSubmatch(line)
	if m == nil {
		return proto.SessionLive{}, false
	}
	spin, _ := utf8.DecodeRuneInString(m[1])
	if unicode.IsLetter(spin) || unicode.IsDigit(spin) || strings.ContainsRune(notSpinner, spin) {
		return proto.SessionLive{}, false
	}
	live := proto.SessionLive{Verb: strings.TrimSpace(m[2])}
	rest := strings.TrimSpace(m[3])
	if rest == "" {
		return live, true
	}
	inside, ok := strings.CutPrefix(rest, "(")
	closing := strings.IndexByte(inside, ')')
	if !ok || closing < 0 {
		return proto.SessionLive{}, false // text after the verb that is not its parenthesis: not a status line
	}
	for _, part := range strings.Split(inside[:closing], "·") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "thinking") {
			live.Thinking = true
		} else if d, ok := strings.CutPrefix(part, "thought for "); ok {
			live.ThoughtS, _ = parseDuration(d)
		} else if t := tokensRE.FindStringSubmatch(part); t != nil {
			if t[1] == "↓" { // ↑ is the input sent so far, not what it wrote
				live.TokensDown = parseCount(t[2], t[3])
			}
		} else if s, ok := parseDuration(part); ok {
			live.ElapsedS = s
		} // anything else ("esc to interrupt", hints) is not ours
	}
	return live, true
}

// parseRetryStatusLine reads the retry banner Claude draws above the same input box while the request is still
// running: "✻ 429 … · retrying in 4s · attempt 5/10". Only facts on the screen become live state; the countdown is
// deliberately left out, so a new attempt (not elapsed time) is what changes the value.
func parseRetryStatusLine(line string) (proto.SessionLive, bool) {
	m := retryLineRE.FindStringSubmatch(line)
	if m == nil {
		return proto.SessionLive{}, false
	}
	spin, _ := utf8.DecodeRuneInString(m[1])
	if unicode.IsLetter(spin) || unicode.IsDigit(spin) || strings.ContainsRune(notSpinner, spin) {
		return proto.SessionLive{}, false
	}
	attempt, attemptErr := strconv.Atoi(m[3])
	maxAttempts, maxErr := strconv.Atoi(m[4])
	message := strings.TrimSpace(m[2])
	if attemptErr != nil || maxErr != nil || attempt <= 0 || maxAttempts <= 0 || message == "" {
		return proto.SessionLive{}, false
	}
	return proto.SessionLive{RetryError: message, RetryAttempt: attempt, RetryMax: maxAttempts}, true
}

// parseDuration reads "2s", "1m 5s", "1h 2m 3s" (and "2m", "1h") as seconds.
func parseDuration(s string) (int, bool) {
	m := durationRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || m[1]+m[2]+m[3] == "" {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	return h*3600 + mi*60 + sec, true
}

// parseCount reads "22.8" "k" as 22800 ("1,234" as 1234).
func parseCount(num, unit string) int {
	f, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", ""), 64)
	if err != nil {
		return 0
	}
	switch unit {
	case "k", "K":
		f *= 1e3
	case "m", "M":
		f *= 1e6
	}
	return int(math.Round(f))
}

// liveEvery is how often a watched, working Claude's status line is read (in whole ticks of the watch pump,
// session_watch.go); liveReadTimeout bounds one read.
var (
	liveEvery       = time.Second
	liveReadTimeout = 3 * time.Second
)

// liveLines is how much of the visible screen is read: the status line sits right above the input box at the bottom.
const liveLines = 60

// liveTicks is liveEvery in ticks of the watch pump (at least one).
func liveTicks() int { return max(1, int(liveEvery/watchEvery)) }

// liveWatch is a watch's status line: whether a read is under way, and what went out last.
type liveWatch struct {
	reading atomic.Bool // a read is under way: the pump does not start another
	mu      sync.Mutex  // orders the pushes, so a read that ends after the watch did sends nothing
	last    *proto.SessionLive
	ended   bool
}

// pumpLive is a watch's status line, read once every liveTicks ticks of the pump: about once a second while the agent
// works, pushed as session.live only when it changed (At aside), and live: null once the turn stops or the line goes
// (when something was sent; endLive sends it when the watch ends). The read runs in a goroutine of its own, one at a
// time per watch, so a slow screen read holds up neither the transcript nor another session; an agent at rest with
// nothing out costs no goroutine.
func (n *Node) pumpLive(w *watch) {
	lw := &w.live
	if lw.reading.Load() {
		return // the last read is still under way: this one is skipped, as a busy ticker's was
	}
	pane := n.livePane(w.id)
	if pane == "" {
		if live := n.pluginCompactionLive(w.id, time.Now()); live != nil {
			n.setLive(w, live)
			return
		}
		lw.mu.Lock()
		shown := lw.last != nil
		lw.mu.Unlock()
		if !shown {
			return
		}
	}
	lw.reading.Store(true)
	go func() {
		defer lw.reading.Store(false)
		var l *proto.SessionLive
		if pane != "" {
			var ok bool
			if l, ok = n.readLive(w.ctx, pane); !ok {
				return
			}
		}
		n.setLive(w, l)
	}()
}

// pluginCompactionLive is what Persona's plugin reported a pane-less outside Claude is compacting, refreshed every
// few seconds so clients keep the line. The pump calls it only when no readable pane exists. A report whose process
// exited, or whose end was lost past ccPluginCompactionTTL, is no live line.
func (n *Node) pluginCompactionLive(id string, now time.Time) *proto.SessionLive {
	c, ok := n.ccPlugins.compaction(id)
	if !ok {
		return nil
	}
	if now.Sub(c.started) >= ccPluginCompactionTTL || !processAlive(c.pid, c.procStart) {
		n.ccPlugins.endCompaction(id, c.pid)
		return nil
	}
	elapsed := int(now.Sub(c.started) / time.Second)
	every := max(1, ccPluginCompactionEveryS)
	if elapsed < every {
		elapsed = 1
	} else {
		elapsed -= elapsed % every
	}
	return &proto.SessionLive{
		Verb:     "Compacting conversation",
		ElapsedS: elapsed,
		At:       now.UTC().Format(time.RFC3339),
	}
}

// wakeCompactionLive lets a compaction report update its watched session now, rather than at the next live tick.
func (n *Node) wakeCompactionLive(id string) {
	n.mu.Lock()
	w := n.watches[id]
	n.mu.Unlock()
	if w != nil {
		go n.pumpLive(w)
	}
}

// setLive pushes l as the watch's status line, unless it is the one that went out last (At aside) or the watch ended.
func (n *Node) setLive(w *watch, l *proto.SessionLive) {
	lw := &w.live
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if l != nil && l.RetryError != "" && lw.last != nil {
		// The retry banner has no counters of its own. Keep the last ordinary metrics while it is on screen;
		// current thinking is different state and is not carried over.
		previous := *lw.last
		if l.Verb == "" {
			l.Verb = previous.Verb
		}
		if l.ElapsedS == 0 {
			l.ElapsedS = previous.ElapsedS
		}
		if l.TokensDown == 0 {
			l.TokensDown = previous.TokensDown
		}
		if l.ThoughtS == 0 {
			l.ThoughtS = previous.ThoughtS
		}
	}
	if lw.ended || sameLive(l, lw.last) {
		return
	}
	lw.last = l
	n.pushLive(w.id, l)
}

// endLive: the watch ended (session.unwatch, or its connection closed). A line that went out ends with null.
func (n *Node) endLive(w *watch) {
	lw := &w.live
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.ended = true
	if lw.last != nil {
		lw.last = nil
		n.pushLive(w.id, nil)
	}
}

func (n *Node) pushLive(id string, l *proto.SessionLive) {
	if n.peer != nil {
		_ = n.peer.Notify("session.live", map[string]any{"session_id": id, "live": l})
	}
}

// liveHosts are the hosts whose screens readLive reads: each reads a pane's text (herdr-core and Orca as plain text,
// which parseClaudeStatus takes as it is), so a Claude in the Herdr app or Orca shows its line too (#3904). botmux has
// no screen of its own.
var liveHosts = map[string]bool{host.Herdr: true, host.HerdrCore: true, host.Orca: true}

// livePane is the pane whose status line is read for session id: "" when it is not a working Claude on a screen
// readLive can read (then the line is null).
func (n *Node) livePane(id string) string {
	a, found := n.agent(id)
	if !found || a.Agent != "claude" || a.AgentStatus != proto.StatusWorking || a.PaneID == "" ||
		!liveHosts[n.hosts.Ref(a.PaneID).Kind] || n.isAssistantSession(id) {
		return ""
	}
	return a.PaneID
}

// readLive is the status line on pane now: nil when it shows none; ok is false when the screen could not be read (keep
// what was sent; a failing read is not logged, it would be every second).
func (n *Node) readLive(ctx context.Context, pane string) (*proto.SessionLive, bool) {
	rctx, cancel := context.WithTimeout(ctx, liveReadTimeout)
	defer cancel()
	text, err := n.readPane(rctx, pane, "visible", liveLines)
	if err != nil {
		return nil, false
	}
	l, ok := parseClaudeStatus(text)
	if !ok {
		return nil, true
	}
	l.At = time.Now().UTC().Format(time.RFC3339)
	return &l, true
}

// sameLive compares two status lines without when they were read.
func sameLive(a, b *proto.SessionLive) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	x.At, y.At = "", ""
	return x == y
}
