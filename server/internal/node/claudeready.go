package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// claudeStartWait is how long a new tab's shell may take to run claude: on a machine busy with a dozen agents,
// loading ~/.zshrc took most of a minute (#1396). claudeReadyWait is how long claude then has to show its input box.
// A node's claudeStart and claudeReady replace them when set (tests shorten both): fields, not package variables a
// test reassigns, since a start's goroutine reads them after the request that began it has returned (#4800).
const claudeStartWait, claudeReadyWait = 3 * time.Minute, time.Minute

// claudeGoneWait is how long herdr may go without naming the claude it saw in a starting pane, with only a shell in
// front, before the start fails as claude having exited (#1447): a wrapper that runs claude twice is back in it sooner.
const claudeGoneWait = 3 * time.Second

// claudeWaits is how long this node's starts wait for the shell to run claude, then for claude's input box.
func (n *Node) claudeWaits() (start, ready time.Duration) {
	start, ready = claudeStartWait, claudeReadyWait
	if n.claudeStart > 0 {
		start = n.claudeStart
	}
	if n.claudeReady > 0 {
		ready = n.claudeReady
	}
	return start, ready
}

// readinessPoll keeps normal terminal starts at their existing cadence. Scripted
// hosts produce their frames and rollouts synchronously and can be observed sooner.
func (n *Node) readinessPoll() time.Duration {
	if n.readyPoll > 0 {
		return n.readyPoll
	}
	return 500 * time.Millisecond
}

// startWait bounds a whole start: the shell, claude, and the first message after.
func (n *Node) startWait() time.Duration {
	start, ready := n.claudeWaits()
	return start + ready + 30*time.Second
}

// waitReady polls until claude's input box is on screen, accepting the folder-trust dialog on the way.
// herdr reports the trust dialog as idle, so the screen is the source of truth. The wait for the input box starts
// once claude runs in the pane: a slow shell before it is not claude being slow. A wait in a herdr pane that ends with
// the input box on screen and herdr knowing no agent there (agent_not_found, or none named) says so: herdr does not
// know that program as Claude (#3454). probe (nil: none) is a start's command: while claude has not come, the tab's
// shell saying it cannot run it ends the wait at once with a coded refusal (shellmiss.go, #1642).
func (n *Node) waitReady(ctx context.Context, pane string, probe *startProbe) (*host.Agent, error) {
	startWait, readyWait := n.claudeWaits()
	poll := n.readinessPoll()
	deadline := time.Now().Add(startWait)
	running := false
	h, _, _ := n.paneHost(pane)
	_, onHerdr := h.(*herdrHost)
	started := func() {
		if !running {
			running = true
			deadline = time.Now().Add(readyWait)
		}
	}
	readyStreak := 0
	unrecognised := false   // at the last look herdr knew no claude in the pane while its input box was on screen
	sawClaude := false      // herdr has named claude in the pane
	var goneSince time.Time // since when herdr has named no agent there after sawClaude, with only a shell in front
	shellSaid := ""         // a not-found line the tab's shell printed after the command, of something else than it
	bypassTries := 0        // times the Bypass Permissions confirmation was accepted (acceptBypass)
	gone := 0               // looks in a row at which the pane's host listed no such pane (errNoOrcaTerminal)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
		screen, err := n.readPane(ctx, pane, "visible", 60)
		if gone = goneLooks(gone, err); gone >= paneGoneLooks && !running {
			return nil, errPaneGone()
		}
		if err != nil {
			continue
		}
		if keys := trustKeys(screen); keys != nil {
			started()
			_ = n.sendKeys(ctx, pane, keys...)
			readyStreak = 0
			time.Sleep(time.Second)
			continue
		}
		// A machine that never ran claude stops at its first-run screens: the style picker is answered with its default
		// and the security notes with Enter (the folder's trust dialog comes next, above), and a screen that waits for a
		// person ends the wait now instead of at the timeout.
		if keys := firstRunKeys(screen); keys != nil {
			_ = n.sendKeys(ctx, pane, keys...)
			readyStreak = 0
			time.Sleep(time.Second)
			continue
		}
		// Claude Code's Bypass Permissions confirmation, on a start Persona itself asked for bypass because the user chose
		// it (Allow all on the task or the New session sheet): "Yes, I accept" is that same choice, so it is pressed here.
		// Any other start that meets it (a launcher's own flag, no mode chosen) still stops below with what to do (#3294).
		if probe != nil && probe.acceptBypass {
			if keys := bypassConfirmKeys(screen); keys != nil {
				if bypassTries >= bypassConfirmTries {
					return nil, errors.New(bypassNotAccepted)
				}
				bypassTries++
				started()
				log.Printf("start in pane %s: accepting Claude Code's Bypass Permissions confirmation (the start asked for bypass)", pane)
				_ = n.sendKeys(ctx, pane, keys...)
				readyStreak = 0
				time.Sleep(time.Second)
				continue
			}
		}
		if reason := firstRunScreen(unframed(screen)); reason != "" {
			return nil, errors.New(reason)
		}
		a, err := n.paneAgent(ctx, pane)
		if gone = goneLooks(gone, err); gone >= paneGoneLooks && !running {
			return nil, errPaneGone()
		}
		if err != nil || a.Agent != "claude" {
			// The tab's shell could not run the command (a launcher its rc files do not define, a claude not on its
			// PATH): it said so right after it and is back at its prompt, and claude will not come (#1642).
			// Or it did not take the typed line at all: a startup question ate its head (#2570). The start types it again.
			if probe != nil && !sawClaude && !running && (mayHaveShellMiss(screen) || mayBeRejected(screen)) {
				if full, rerr := n.readPane(ctx, pane, "recent-unwrapped", 60); rerr == nil {
					refused, other := probe.verdict(full, func() bool { return n.shellBackOrUnknown(ctx, pane) })
					if refused != nil {
						return nil, refused
					}
					if line := probe.rejectedLine(full); line != "" {
						return nil, &startLineRejected{line: line}
					}
					shellSaid = other
				}
			}
			if onHerdr && (err == nil || errors.Is(err, host.ErrNoAgent)) {
				unrecognised = inputBoxVisible(screen)
				// claude came up and exited (a launcher's bad config, a failed login): no input box will come, so the
				// start ends now rather than at the deadline; the caller shows the pane's last lines, which say why.
				switch {
				case !sawClaude || !n.shellInFront(ctx, pane):
					goneSince = time.Time{}
				case goneSince.IsZero():
					goneSince = time.Now()
				case time.Since(goneSince) >= claudeGoneWait:
					return nil, errors.New("claude exited before showing its input box")
				}
			}
			readyStreak = 0
			continue
		}
		sawClaude, goneSince = true, time.Time{}
		unrecognised = false
		started()
		if a.AgentStatus == proto.StatusBlocked {
			return a, nil // some other dialog; the phone shows it as a screen interaction
		}
		// An Orca fork that cannot read claude's title once the tab is renamed reports no state until claude's first hook
		// (#4438): with claude in front, the screen decides, as long as it does not show a turn running.
		quiet := a.AgentStatus == proto.StatusIdle || a.AgentStatus == proto.StatusDone ||
			a.AgentStatus == proto.StatusUnknown && !strings.Contains(screen, "esc to interrupt")
		if quiet && inputBoxVisible(screen) {
			readyStreak++
			if readyStreak >= 2 {
				return a, nil
			}
			continue
		}
		readyStreak = 0
	}
	if unrecognised {
		wait := startWait
		if running { // the trust dialog was answered: the wait after it
			wait = readyWait
		}
		return nil, n.unrecognisedClaude(ctx, pane, wait)
	}
	if !running && shellSaid != "" {
		return nil, fmt.Errorf("claude did not start within %s: after the command the tab's shell printed %q", startWait, shellSaid)
	}
	if !running && gone > 0 {
		return nil, errPaneGone()
	}
	if !running {
		return nil, fmt.Errorf("claude did not start within %s: the tab's shell had not run it yet (is the machine busy?)", startWait)
	}
	// A start stuck on first-run screens the screen matching does not name: say so from the onboarding flag instead
	// of leaving only the generic timeout (claudeonboarding.go).
	if hint := n.onboardingBlockHint(ctx); hint != "" {
		return nil, &claudeStalled{fmt.Errorf("claude did not become ready within %s of starting: %s", readyWait, hint)}
	}
	return nil, &claudeStalled{fmt.Errorf("claude did not become ready within %s of starting", readyWait)}
}

// claudeStalled is the error of a start whose claude ran but never showed its input box: its own screen stopped
// somewhere (a first-run screen, a dialog the node does not know). The start ends with agent_start_stalled and keeps
// the tab (control.go), as a Codex start does (#2558, #2570).
type claudeStalled struct{ err error }

func (e *claudeStalled) Error() string { return e.err.Error() }
func (e *claudeStalled) Unwrap() error { return e.err }

// unrecognisedClaude is why a start in a herdr pane failed whose claude showed its input box while herdr never
// reported it, with the programs herdr sees in front of the pane: herdr knows Claude by the program's name or by
// HERDR_AGENT (hostherdr.go), and a wrapper whose binary has another name gets neither when herdr does not read the
// variable, the pane's shell is not one the node types it for, or the launcher runs another command before claude
// (#3454).
func (n *Node) unrecognisedClaude(ctx context.Context, pane string, wait time.Duration) error {
	sees := ""
	if h, ref, err := n.paneHost(pane); err == nil {
		if f, ok := h.(host.ForegroundNamer); ok {
			if names, err := f.PaneForeground(ctx, ref); err == nil && len(names) > 0 {
				sees = " (herdr sees " + strings.Join(names, ", ") + " in front)"
			}
		}
	}
	return fmt.Errorf("claude's input box is on screen but herdr did not recognise the agent within %s%s: is claude a wrapper? "+
		"herdr knows Claude by its process name, or by HERDR_AGENT=claude, which the node types in front of the launcher "+
		"in a bash, zsh, fish, sh, dash or ksh pane; herdr on Windows and before 0.7.1 (0.7.5 on macOS) ignores it, and a "+
		"launcher that runs another command first (an alias starting with export or cd) does not pass it on to claude", wait, sees)
}

// shellInFront: the pane's host names its foreground programs and they are all shells (the agent in it has exited).
func (n *Node) shellInFront(ctx context.Context, pane string) bool {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return false
	}
	f, ok := h.(host.ForegroundNamer)
	if !ok {
		return false
	}
	names, err := f.PaneForeground(ctx, ref)
	if err != nil || len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !shellProgram(name) {
			return false
		}
	}
	return true
}

// inputBoxVisible reports whether Claude Code's prompt footer is rendered.
func inputBoxVisible(screen string) bool {
	return strings.Contains(screen, "shift+tab") || strings.Contains(screen, "? for shortcuts") ||
		strings.Contains(screen, "bypass permissions") || strings.Contains(screen, "esc to interrupt") ||
		// Claude Code 2.1.206 in default (manual) mode: "⏸ manual mode on · ← for agents", no shift+tab hint.
		strings.Contains(screen, "manual mode on") || strings.Contains(screen, "← for agents")
}

// trustKeys returns the keys that select "Yes, I trust this folder" on Claude Code's trust dialog.
func trustKeys(screen string) []string {
	if !strings.Contains(screen, "trust this folder") && !strings.Contains(screen, "Do you trust") {
		return nil
	}
	lines := strings.Split(screen, "\n")
	cur, yes := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "❯") {
			cur = i
		}
		if strings.Contains(t, "Yes, I trust") || strings.Contains(t, "Yes, proceed") {
			yes = i
		}
	}
	if yes < 0 {
		return nil
	}
	if cur < 0 {
		cur = yes
	}
	var keys []string
	for i := cur; i < yes; i++ {
		keys = append(keys, "down")
	}
	for i := cur; i > yes; i-- {
		keys = append(keys, "up")
	}
	return append(keys, "enter")
}

// themePickerQuestion is the first-run style picker's prompt (Claude Code 2.1.318, "Choose the text style that looks
// best with your terminal"). The node accepts its default so an agent can start on a machine where claude never ran.
const themePickerQuestion = "Choose the text style that looks best with your terminal"

// firstRunKeys returns the keys that get past one of Claude Code's first-run screens the node can answer by itself:
// the style picker, then the security notes. A fresh 2.1.318 shows them in that order, then the folder's trust dialog.
func firstRunKeys(screen string) []string {
	if keys := themePickerKeys(screen); keys != nil {
		return keys
	}
	return securityNotesKeys(screen)
}

// themePickerKeys returns the keys that accept a theme on Claude Code's first-run style picker. With numbered choices
// the cursor is walked back to the first one and Enter pressed. Claude Code 2.1.318 draws its choices without numbers
// ("Auto (match terminal)" first, the cursor on its default "✔ Dark mode"), so the old match never fired and the start
// timed out on the picker: there Enter takes the highlighted choice. Which theme it is does not matter.
func themePickerKeys(screen string) []string {
	if !strings.Contains(unframed(screen), themePickerQuestion) {
		return nil
	}
	cur, top, question := -1, -1, -1
	for i, l := range strings.Split(screen, "\n") {
		if question < 0 && strings.Contains(l, "Choose the text") {
			question = i
		}
		if question < 0 {
			continue // above the picker: the shell's own prompt may draw a ❯
		}
		if top < 0 && styleChoice(l) != "" {
			top = i
		}
		if strings.Contains(l, "❯") {
			cur = i
		}
	}
	if top < 0 {
		if cur < 0 {
			return nil // unnumbered choices whose cursor is not drawn yet
		}
		return []string{"enter"}
	}
	if cur < top {
		cur = top // no cursor rendered yet: the first choice is already selected
	}
	var keys []string
	for i := cur; i > top; i-- {
		keys = append(keys, "up")
	}
	return append(keys, "enter")
}

// securityNotesKeys returns Enter for Claude Code's first-run "Security notes" page, which waits only for it ("Press
// Enter to continue…", 2.1.318).
func securityNotesKeys(screen string) []string {
	if !strings.Contains(screen, "Security notes:") || !strings.Contains(screen, "Press Enter to continue") {
		return nil
	}
	return []string{"enter"}
}

// unframed is the screen with a box's borders and padding taken out and its wrapped lines joined, which is how the
// picker's question spans two lines inside its frame.
func unframed(screen string) string {
	var b strings.Builder
	for _, l := range strings.Split(screen, "\n") {
		t := strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "│|╭╮╰╯─"))
		if t == "" {
			continue
		}
		b.WriteString(t)
		b.WriteByte(' ')
	}
	return b.String()
}

// styleChoice is the option text of one line of the picker, or "" when the line is not one. The picker may be drawn
// inside a box, so the choice is found anywhere in the line.
func styleChoice(line string) string {
	t := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│|"))
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			continue
		}
		j := i
		for j < len(t) && t[j] >= '0' && t[j] <= '9' {
			j++
		}
		if j+1 < len(t) && t[j] == '.' && t[j+1] == ' ' {
			return strings.TrimSpace(strings.TrimRight(t[j+1:], "│| "))
		}
		i = j
	}
	return ""
}

// bypassConfirmQuestion is the warning that opens Claude Code's Bypass Permissions confirmation (2.1.322: shown on a
// start with --permission-mode bypassPermissions, --dangerously-skip-permissions or --allow-dangerously-skip-permissions
// until the profile has accepted it once). Its choices are "❯ No, exit" and "Yes, I accept", unnumbered.
const bypassConfirmQuestion = "Claude Code running in Bypass Permissions mode"

// bypassConfirmTries is how many times a start presses "Yes, I accept"; a confirmation still on screen after that is
// reported (bypassNotAccepted) instead of pressed again.
const bypassConfirmTries = 3

const bypassNotAccepted = "Claude Code's Bypass Permissions confirmation stayed on screen after Persona chose \"Yes, I accept\" " +
	"for this start: accept it in a terminal on this machine, or start the session in another permission mode"

// bypassConfirmKeys returns the keys that choose "Yes, I accept" on Claude Code's Bypass Permissions confirmation, nil
// on any other screen (a permission, the trust dialog, a picker: never these). Only the confirmation's own lines count:
// the warning, then both of its choices below it, the cursor on one of them.
func bypassConfirmKeys(screen string) []string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	warn, cur, yes, no := -1, -1, -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(strings.ReplaceAll(l, "\u00a0", " "))
		if strings.Contains(t, bypassConfirmQuestion) {
			warn, cur, yes, no = i, -1, -1, -1 // the last one drawn is the one on screen
			continue
		}
		if warn < 0 {
			continue
		}
		label := strings.TrimSpace(strings.TrimPrefix(t, "❯"))
		switch label {
		case "Yes, I accept", "2. Yes, I accept":
			yes = i
		case "No, exit", "1. No, exit":
			no = i
		default:
			continue
		}
		if strings.HasPrefix(t, "❯") {
			cur = i
		}
	}
	if warn < 0 || yes < 0 || no < 0 || cur < 0 {
		return nil
	}
	var keys []string
	for i := cur; i < yes; i++ {
		keys = append(keys, "down")
	}
	for i := cur; i > yes; i-- {
		keys = append(keys, "up")
	}
	return append(keys, "enter")
}

// firstRunScreen names the first-run screen the node cannot complete on its own, or "" when the screen is not one.
// These wait for a person, so the caller reports them at once instead of polling until the timeout.
func firstRunScreen(screen string) string {
	switch {
	case strings.Contains(screen, "WARNING: Claude Code running in Bypass Permissions mode") && strings.Contains(screen, "Yes, I accept"):
		return "Claude Code is waiting for its first Bypass Permissions confirmation: choose default permission mode, " +
			"or review and confirm that mode in a terminal on this machine before retrying"
	case strings.Contains(screen, "Select login method:"):
		return "this machine's Claude Code has not been set up yet: it is showing the sign-in screen, run claude once in a terminal there"
	case strings.Contains(screen, "Welcome to Claude Code") && strings.Contains(screen, "Select"):
		return "this machine's Claude Code has not been set up yet: it is showing its first-run screen, run claude once in a terminal there"
	}
	return ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// paneGoneLooks: this many looks in a row (500 ms apart) at which the pane's host lists no such pane end a start
// whose claude has not come: a pane that is gone does not come back.
const paneGoneLooks = 6

// goneLooks counts the looks in a row whose error says the pane's host lists no such pane (Orca's errNoOrcaTerminal),
// rather than failing to answer: n+1 then, else 0.
func goneLooks(n int, err error) int {
	if errors.Is(err, errNoOrcaTerminal) {
		return n + 1
	}
	return 0
}

// errPaneGone: not a slow shell, the node lost the pane (closed, or its engine moved it where the node does not find
// it, #5547).
func errPaneGone() error {
	return errors.New("claude did not start: the new tab's pane could not be found any more")
}
