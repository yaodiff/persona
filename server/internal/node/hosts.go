package node

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// The node drives every terminal through the host its pane belongs to (internal/host). Panes are kept as PaneRef
// strings everywhere in the node (n.panes, agents, the hub's database): a bare id is a herdr pane.

// paneHost is the host of a stored pane and the pane's ref in it.
func (n *Node) paneHost(pane string) (host.Host, host.PaneRef, error) {
	h, ref := n.hosts.Pane(pane)
	if h == nil {
		return nil, ref, fmt.Errorf("no %s host on this machine", ref.Kind)
	}
	return h, ref, nil
}

// paneCap is the pane's host as capability C (Launcher, Organizer…), with the pane's ref.
func paneCap[C any](n *Node, pane string) (C, host.PaneRef, error) {
	var none C
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return none, ref, err
	}
	c, ok := h.(C)
	if !ok {
		return none, ref, unsupported(h)
	}
	return c, ref, nil
}

// readPane reads a pane's screen as plain text.
func (n *Node) readPane(ctx context.Context, pane, source string, lines int) (string, error) {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return "", err
	}
	return h.Read(ctx, ref, host.ReadOpts{Source: source, Lines: lines})
}

// readPaneANSI reads a pane's screen with its colors. One herdr loses (an answer over 8 KB, herdr.ErrTruncated) is
// read in two smaller ones: the rows without colors, and the bottom rows with them (spliceRead).
func (n *Node) readPaneANSI(ctx context.Context, pane, source string, lines int) (string, error) {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return "", err
	}
	s, err := h.Read(ctx, ref, host.ReadOpts{Source: source, Lines: lines, ANSI: true})
	if errors.Is(err, herdr.ErrTruncated) {
		return spliceRead(ctx, h, ref, source, lines)
	}
	return s, err
}

// spliceRead is a colored read of lines rows that herdr could not send whole (#6039). herdr 0.9.4 on macOS writes an
// answer to its socket once, and past the socket's 8192-byte buffer the rest is lost: 60 colored rows of a wide pane
// with Claude Code's status bar are more than that, and every such read failed. So the screen is read without colors,
// all of it (a plain read cut too is the error: a screen with fewer rows than asked for would be taken for the whole,
// a box or a dialog's top above it unseen), and its bottom rows again with colors (herdr keeps the newest), where
// Claude's input box and a dialog's keys are: the plain rows with the bottom ones colored. Rows higher up have no
// colors, which only makes more of them count as typed (a box taller than the colored rows is a draft). When the
// colored rows are not the plain read's last ones (the screen moved between the reads) they are read again once;
// then the error.
func spliceRead(ctx context.Context, h host.Host, ref host.PaneRef, source string, lines int) (string, error) {
	plain, err := h.Read(ctx, ref, host.ReadOpts{Source: source, Lines: lines})
	if err != nil {
		return "", err
	}
	for range 2 {
		all := screenRows(strings.TrimSuffix(strings.ReplaceAll(plain, "\r\n", "\n"), "\n")) // the read's last line end is no row
		rows := contentRows(all)
		// The colored rows reach past the blank ones at the bottom (a short session in a tall pane), to the box.
		blank := len(all) - len(rows)
		// Never more rows than the plain read asked for: it ends in a line end, so all has an empty row past them.
		for _, n := range []int{min(len(all), blank+20, lines), min(len(all), blank+10, lines)} {
			colored, err := h.Read(ctx, ref, host.ReadOpts{Source: source, Lines: n, ANSI: true})
			if errors.Is(err, herdr.ErrTruncated) {
				continue
			}
			if err != nil {
				return "", err
			}
			// Both lined up from their last row with something on it: how many blank rows each ends in does not count.
			crows := contentRows(screenRows(colored))
			if len(crows) == 0 || len(crows) > len(rows) || !sameRows(rows[len(rows)-len(crows):], crows) {
				break // the screen moved between the reads, or the colored rows show nothing to go by
			}
			return strings.Join(append(slices.Clone(rows[:len(rows)-len(crows)]), crows...), "\r\n") + "\r\n", nil
		}
		if plain, err = h.Read(ctx, ref, host.ReadOpts{Source: source, Lines: lines}); err != nil {
			return "", err
		}
	}
	return "", herdr.ErrTruncated
}

// bottomRead is the bottom of a screen herdr cannot send whole even without colors (spliceRead's plain read cut too: a
// wide pane full of CJK text is past 8192 bytes in 30 plain rows): the most rows that fit, colored where spliceRead can
// splice them on, else plain (#6308). It is used only when those rows show Claude's input box or a dialog, which sit at
// the bottom: Claude draws no box while a dialog has the keyboard, and a dialog's keys and footer end it. Without
// either nothing is known of what is above (ok false), and the caller refuses as before. What is above stays unseen:
// a dialog's head among it, so whether it is a notice that Enter closes is not known either.
func (n *Node) bottomRead(ctx context.Context, pane string) (s string, colored, ok bool) {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return "", false, false
	}
	shows := func(s string) bool { return screen.ClaudeInput(s).Box || screen.Choosing(screen.Plain(s)) }
	for _, lines := range []int{40, 30, 24, 16, 12} {
		plain, err := h.Read(ctx, ref, host.ReadOpts{Source: "visible", Lines: lines})
		if errors.Is(err, herdr.ErrTruncated) {
			continue
		}
		if err != nil || !shows(plain) {
			return "", false, false
		}
		// The splice reads again, and the screen it gives is the one judged: a newer read that lines up and shows
		// neither says they left these rows. When no colors line up, a fresh plain read stands for it, judged the same.
		c, err := spliceRead(ctx, h, ref, "visible", lines)
		spliced := err == nil
		if err != nil {
			c, err = h.Read(ctx, ref, host.ReadOpts{Source: "visible", Lines: lines})
		}
		if err != nil || !shows(c) {
			return "", false, false
		}
		return c, spliced, true
	}
	return "", false, false
}

// dialogScreen is the plain screen a dialog is read from, screen.DialogLines rows: the same read for its card
// (session.screen) and for its answer (dialogKeys, dialogMulti), so both see the same rows and agree on its revision.
// When herdr cannot send that many rows whole (#6318: a wide pane full of CJK text is past 8 KB in 30 plain rows), it
// is the most bottom rows that fit (30, 24, 16, 12), but only with any dialog in them shown whole, from the rule that
// opens it (screen.DialogWhole): then it is the dialog a whole screen shows, its revision the same whatever rows fit.
// A dialog cut by those rows is unreadable (herdr.ErrTruncated) as before: its card would show part of it, a
// permission's command cut, and its revision would cover only what is shown.
func (n *Node) dialogScreen(ctx context.Context, pane string) (string, error) {
	text, err := n.readPane(ctx, pane, "visible", screen.DialogLines)
	for _, lines := range []int{30, 24, 16, 12} {
		if !errors.Is(err, herdr.ErrTruncated) {
			break
		}
		if text, err = n.readPane(ctx, pane, "visible", lines); err == nil {
			if (screen.Choosing(text) || screen.ParseDialog(text) != nil) && !screen.DialogWhole(text) {
				return "", errDialogCut
			}
		}
	}
	return text, err
}

// errDialogCut: dialogScreen sees a dialog at the bottom of the screen but cannot read all of it (#6318). It is
// herdr.ErrTruncated to callers that only ask whether the screen could be read; the answers say what to do.
var errDialogCut = fmt.Errorf("the dialog is taller than the rows herdr can send: %w", herdr.ErrTruncated)

// dialogCutMessage is the refusal for an answer to a dialog dialogScreen cannot read all of: nothing was pressed.
const dialogCutMessage = "终端上的这个对话框太长，读不全（这台电脑的终端一次只能读回 8 KB），这次什么都没按：在电脑上回答。"

// screenRows are a read's rows (\r\n or \n ends a row).
func screenRows(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// contentRows are rows without the blank ones they end in.
func contentRows(rows []string) []string {
	n := len(rows)
	for n > 0 && strings.TrimSpace(screen.Plain(rows[n-1])) == "" {
		n--
	}
	return rows[:n]
}

// sameRows: colored rows show the plain ones, trailing white space aside (herdr's plain read drops it all: the
// no-break space after an empty box's ❯, an ideographic space).
func sameRows(plain, colored []string) bool {
	for i := range colored {
		if strings.TrimRightFunc(screen.Plain(colored[i]), unicode.IsSpace) != strings.TrimRightFunc(plain[i], unicode.IsSpace) {
			return false
		}
	}
	return true
}

// plainScreen: the pane's host reads screens as plain text only (host.PlainScreen), so colored reads fail there.
func (n *Node) plainScreen(pane string) bool {
	h, _ := n.hosts.Pane(pane)
	p, ok := h.(host.PlainScreen)
	return ok && p.PlainScreen()
}

// drivesScreen: the host reads its panes' screens and presses keys in them (host.ScreenDriver).
func drivesScreen(h host.Host) bool {
	d, ok := h.(host.ScreenDriver)
	return !ok || d.CanDriveScreen()
}

// canTypeText: the pane's host can type text without submitting it (host.TextTyper).
func (n *Node) canTypeText(pane string) bool {
	h, _ := n.hosts.Pane(pane)
	t, ok := h.(host.TextTyper)
	return !ok || t.CanTypeText()
}

func (n *Node) sendText(ctx context.Context, pane, text string) error {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return err
	}
	return h.SendText(ctx, ref, text)
}

func (n *Node) sendKeys(ctx context.Context, pane string, keys ...string) error {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return err
	}
	return h.SendKeys(ctx, ref, keys...)
}

// hostPrompt is the host's own prompt (no fallback; prompt types when the host knows no agent there). Hosts paste the
// text, where a control character could end the paste and press keys: every caller's text goes in inert.
func (n *Node) hostPrompt(ctx context.Context, pane, text string) error {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return err
	}
	return h.Prompt(ctx, ref, inertControls(text))
}

// inertControls is text as it may go into a terminal in a paste: an ESC becomes inertEscape and every other control
// character but newline, carriage return and tab (Unicode Cc) becomes <U+XXXX>, so none of them is a key; bytes that
// are not UTF-8 become U+FFFD.
func inertControls(text string) string {
	if utf8.ValidString(text) && !strings.ContainsFunc(text, pasteControl) {
		return text
	}
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '\x1b':
			b.WriteString(inertEscape)
		case pasteControl(r):
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pasteControl: a control character inertControls makes inert.
func pasteControl(r rune) bool {
	return unicode.Is(unicode.Cc, r) && r != '\n' && r != '\r' && r != '\t'
}

// defaultHost is where new sessions start and where spaces without a host kind live: the set's default host (#2576;
// host kinds are engine names), classic herdr for herdr + Herdr App, its full-capability host. nil for the empty set,
// and for a default that is no terminal (Paseo).
func (n *Node) defaultHost() host.Host {
	kind := n.defaultKind()
	if kind == "" {
		return nil
	}
	return n.hosts.Get(kind)
}

// defaultEngineName names the host pane-less operations run on, for user-facing messages: herdr + Herdr App starts and
// resumes on classic herdr, so it says herdr, not "herdr + Herdr App".
func (n *Node) defaultEngineName() string {
	if name := proto.EngineNames[n.defaultKind()]; name != "" {
		return name
	}
	return n.engine()
}

// hostCap is the default host as capability C; with no host enabled, noEngineRefusal.
func hostCap[C any](n *Node) (C, error) {
	var none C
	kind := n.defaultKind()
	if kind == "" {
		return none, noEngineRefusal()
	}
	h := n.hosts.Get(kind)
	if h == nil {
		return none, fmt.Errorf("no %s host on this machine", kind)
	}
	c, ok := h.(C)
	if !ok {
		return none, unsupported(h)
	}
	return c, nil
}

func (n *Node) paneProcesses(ctx context.Context, pane string) ([]int, error) {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return nil, err
	}
	return h.PaneProcesses(ctx, ref)
}

// paneAgent is the host's view of the agent in a pane.
func (n *Node) paneAgent(ctx context.Context, pane string) (*host.Agent, error) {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return nil, err
	}
	return h.Agent(ctx, ref)
}

// renameSlot renames a slot through the launcher's host (a slot it just made).
func renameSlot(ctx context.Context, launch host.Launcher, slot, name string) error {
	org, ok := launch.(host.Organizer)
	if !ok {
		return fmt.Errorf("%w: cannot rename %s", host.ErrUnsupported, slot)
	}
	return org.RenameSlot(ctx, slot, name)
}

// unsupported: the host lacks the capability asked for ("unsupported: …", errors.Is host.ErrUnsupported). The words
// stay what they were, for the clients and the hub that tell it by its "unsupported" prefix; the code and the data
// (reason engine_unsupported, the engine) are for the hub to word it in the reader's language (#17057).
func unsupported(h host.Host) error {
	return host.Mark(failure("unsupported", fmt.Sprintf("%v: %s cannot do this", host.ErrUnsupported, h.Kind()),
		map[string]any{"reason": "engine_unsupported", "engine": h.Kind()}), host.ErrUnsupported)
}
