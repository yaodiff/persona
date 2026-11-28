package node

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// screen reads the session's terminal. source "visible" is the screen; "recent" also reaches back into the pane's
// scrollback (the phone pages it: ask for more lines until fewer come back than were asked for). ansi keeps the colors
// (SGR escapes; rows end in \r\n).
func (n *Node) screen(ctx context.Context, id string, lines int, source string, ansi bool) (string, string, error) {
	if n.isAssistantSession(id) {
		return "", proto.StatusIdle, nil // headless: no terminal; live text arrives as session.delta
	}
	if source != "recent" {
		source = "visible"
	}
	if limit := map[string]int{"visible": 400, "recent": 20000}[source]; lines <= 0 || lines > limit {
		lines = min(max(lines, 60), limit)
	}
	a, ok := n.agent(id)
	if !ok {
		// A claude the user started in tmux has no host agent: its own record names the pane (#2562).
		if pane := n.outsidePaneOf(id); pane != "" {
			return n.readOutsideScreen(ctx, id, pane, source, lines, ansi)
		}
		pane, err := n.terminalPane(ctx, id) // also a "pane:" address (panes.go)
		if err != nil {
			return "", proto.StatusOffline, nil
		}
		a, _ = n.agent(id)
		a.PaneID = pane
	}
	if source == "recent" {
		// A host that keeps the output apart from the screen (Orca): its earlier output above the screen, as herdr's
		// "recent" read gives its scrollback.
		if all, _, ok := n.scrollback(ctx, a.PaneID, lines); ok {
			all = all[max(0, len(all)-lines):]
			eol := "\n"
			if ansi {
				eol = "\r\n" // plain rows, ended as colored ones are
			}
			return strings.Join(all, eol) + eol, a.AgentStatus, nil
		}
	}
	if ansi && n.plainScreen(a.PaneID) {
		// No colors in this host: the plain rows, ended as colored ones are.
		text, err := n.readPane(ctx, a.PaneID, source, lines)
		if err == nil && text != "" {
			text = strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\r\n") + "\r\n"
		}
		return text, a.AgentStatus, err
	}
	if ansi {
		text, err := n.readPaneANSI(ctx, a.PaneID, source, lines)
		return text, a.AgentStatus, err
	}
	if lines == screen.DialogLines && source == "visible" { // a screen card's read: what the answer will read too (#6318)
		text, err := n.dialogScreen(ctx, a.PaneID)
		return text, a.AgentStatus, err
	}
	text, err := n.readPane(ctx, a.PaneID, source, lines)
	return text, a.AgentStatus, err
}

// readOutsideScreen reads the pane of a claude no host lists as an agent (a Claude Code the user started in tmux,
// #2562): the same reads a host agent's pane gets, with the status its own record gives it.
func (n *Node) readOutsideScreen(ctx context.Context, id, pane, source string, lines int, ansi bool) (string, string, error) {
	status := proto.StatusUnknown
	if c, ok := n.outsideClaude(id); ok {
		if s, ok := claudeBgStatus[c.Status]; ok {
			status = s
		}
	}
	if ansi && n.plainScreen(pane) {
		text, err := n.readPane(ctx, pane, source, lines)
		if err == nil && text != "" {
			text = strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\r\n") + "\r\n"
		}
		return text, status, err
	}
	if ansi {
		text, err := n.readPaneANSI(ctx, pane, source, lines)
		return text, status, err
	}
	if lines == screen.DialogLines && source == "visible" { // a screen card's read: what the answer will read too
		text, err := n.dialogScreen(ctx, pane)
		return text, status, err
	}
	text, err := n.readPane(ctx, pane, source, lines)
	return text, status, err
}

// herdr keeps about 1000 lines per pane readable (pane read caps there whatever --lines asks); older output is only
// in Claude's transcript.
const herdrScrollbackLines = 1000

// screenBefore returns up to `lines` lines of the pane's scrollback right before the anchor (the first lines the phone
// already shows). Located by content, not by offset from the end, so output arriving meanwhile shifts nothing.
// loaded is how many lines the phone already has (from the end): Claude's redraws push the same screens into the
// scrollback again and again, so the anchor can match in several places — the one nearest len-loaded is ours.
// With ansi the page keeps its colors; the anchor is still matched on the plain text.
func (n *Node) screenBefore(ctx context.Context, id string, anchor []string, lines, loaded int, ansi bool) (map[string]any, error) {
	if lines <= 0 || lines > 500 {
		lines = 200
	}
	a, ok := n.agent(id)
	if !ok {
		pane, err := n.terminalPane(ctx, id) // also a "pane:" address (panes.go)
		if err != nil {
			return nil, err
		}
		a, _ = n.agent(id)
		a.PaneID = pane
	}
	if all, dropped, ok := n.scrollback(ctx, a.PaneID, 0); ok {
		return pageBefore(all, anchor, lines, loaded, dropped), nil
	}
	text, err := n.readPane(ctx, a.PaneID, "recent", herdrScrollbackLines)
	if err != nil {
		return nil, err
	}
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	at := nearestBlock(all, anchor, len(all)-loaded)
	if at < 0 { // the anchor scrolled out of the kept window: nothing older can be placed before it
		return map[string]any{"text": "", "has_more": false, "at_limit": len(all) >= herdrScrollbackLines-20}, nil
	}
	start := max(at-lines, 0)
	page := all[start:at]
	if ansi {
		// The same rows with their colors: herdr reads both from one grid, so row i is row i — unless output arrived
		// between the two reads (a full scrollback then keeps its row count but shifts), so the colored rows are used
		// only when they show the plain page's text; otherwise the plain page is still right.
		if colored, err := n.readPaneANSI(ctx, a.PaneID, "recent", herdrScrollbackLines); err == nil {
			rows := strings.Split(strings.TrimRight(strings.ReplaceAll(colored, "\r\n", "\n"), "\n"), "\n")
			if len(rows) == len(all) && sameRows(page, rows[start:at]) {
				page = rows[start:at]
			}
		}
	}
	return map[string]any{
		"text":     strings.Join(page, "\n"),
		"has_more": start > 0,
		// The start of what herdr keeps: older output was dropped (long panes) or never existed (short ones).
		"at_limit": start == 0 && len(all) >= herdrScrollbackLines-20,
	}, nil
}

// scrollback is what a client shows of a pane whose host keeps the output apart from the screen (host.Scrollbacker,
// Orca): the host's earlier output down to where the screen begins, then the screen as the plain read gives it — what
// the client polling it shows, so its anchor is found there. lines (0: all the host has) is how many lines from the
// end the caller wants. dropped: the host has no output older than the first line any more. ok false: the host has
// nothing to give (another kind of host, nothing above the screen, a failed read), and the caller reads as before.
func (n *Node) scrollback(ctx context.Context, pane string, lines int) (all []string, dropped, ok bool) {
	sb, ref, err := paneCap[host.Scrollbacker](n, pane)
	if err != nil {
		return nil, false, false
	}
	want := 0
	if lines > 0 {
		// The output's last lines are the screen's again (with blank lines the screen read leaves out): room for them.
		want = lines + scrollbackScreenRoom
	}
	out, dropped, err := sb.Scrollback(ctx, ref, want)
	if err != nil || len(out) == 0 {
		return nil, false, false
	}
	text, err := n.readPane(ctx, pane, "visible", 400)
	if err != nil {
		return nil, false, false
	}
	var shown []string
	if strings.TrimSpace(text) != "" {
		shown = strings.Split(strings.TrimRight(text, "\n"), "\n")
	}
	return aboveScreen(out, shown), dropped, true
}

// scrollbackScreenRoom: the lines of output a screen may take, asked for on top of what a caller wants (Orca's screen
// read sends at most 120 rows, and leaves blank ones out).
const scrollbackScreenRoom = 250

// aboveScreen is the output older than the screen, then the screen. The output ends with what the screen shows, so
// it is cut where the screen's rows start: the place in it where most of the screen's rows follow in order (the
// latest of equals). Blank lines are passed over, since a screen read may leave them out, and the screen's last rows
// may differ (a prompt the read shows without the text typed into it). No such place: all the output, then the
// screen.
func aboveScreen(out, shown []string) []string {
	var rows []string // the screen's rows with text
	for _, s := range shown {
		if s = strings.TrimRight(s, " "); s != "" {
			rows = append(rows, s)
		}
	}
	if len(rows) == 0 {
		return out
	}
	var text []int // where out has text
	for i, l := range out {
		if strings.TrimSpace(l) != "" {
			text = append(text, i)
		}
	}
	cut, best := len(out), 0
	for c := len(text) - 1; c >= 0; c-- {
		run := 0
		for run < len(rows) && c+run < len(text) && strings.TrimRight(out[text[c+run]], " ") == rows[run] {
			run++
		}
		if run > best {
			cut, best = text[c], run
		}
	}
	if best < min(2, len(rows)) {
		cut = len(out)
	}
	return append(append(make([]string, 0, cut+len(shown)), out[:cut]...), shown...)
}

// pageBefore is the page of all right before the anchor (screenBefore) for a host that says where its kept output
// begins: dropped, the output before all[0] is gone, so reaching all[0] says so (at_limit too, for clients that know
// only that, #17048); otherwise all[0] is where the output began.
func pageBefore(all, anchor []string, lines, loaded int, dropped bool) map[string]any {
	at := nearestBlock(all, anchor, len(all)-loaded)
	if at < 0 { // the anchor is no longer in what is kept: nothing older can be placed before it
		return map[string]any{"text": "", "has_more": false, "at_limit": dropped, "dropped": dropped}
	}
	start := max(at-lines, 0)
	top := start == 0 && dropped
	return map[string]any{"text": strings.Join(all[start:at], "\n"), "has_more": start > 0, "at_limit": top, "dropped": top}
}

// screenRefresh is how often a client watching the session's screen may read it again, in milliseconds (0: the usual
// once a second): the pane's host's own pace (host.QuickScreen) when its screen read is cheap. A client reads this
// often only while the screen changes.
func (n *Node) screenRefresh(id string) int64 {
	pane := ""
	if a, ok := n.agent(id); ok {
		pane = a.PaneID
	} else if p, ok := strings.CutPrefix(id, panePrefix); ok {
		pane = p
	} else if p, ok := n.keptPane(id); ok {
		pane = p.PaneID
	}
	if pane == "" {
		return 0
	}
	h, _, err := n.paneHost(pane)
	if err != nil {
		return 0
	}
	if q, ok := h.(host.QuickScreen); ok {
		return q.ScreenRefresh().Milliseconds()
	}
	return 0
}

// nearestBlock is the match of block whose start is closest to want, -1 when it does not appear.
func nearestBlock(all, block []string, want int) int {
	best := -1
	for i := len(all) - len(block); i >= 0; {
		j := findBlock(all[:i+len(block)], block)
		if j < 0 {
			break
		}
		if best < 0 || abs(j-want) < abs(best-want) {
			best = j
		}
		i = j - 1
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// findBlock returns where the lines of block start in all (the last match), -1 if they do not appear.
func findBlock(all, block []string) int {
	if len(block) == 0 {
		return len(all)
	}
	for i := len(all) - len(block); i >= 0; i-- {
		match := true
		for j, b := range block {
			if strings.TrimRight(all[i+j], " ") != strings.TrimRight(b, " ") {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
