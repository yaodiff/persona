package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Control is `herdr terminal session control <pane> --cols C --rows R`: the terminal's one attach slot, held over
// pipes. While it runs the pane's programs see C × R (Claude Code lays itself out again for it), what is written in
// goes to them as if typed, and the frames coming out are herdr's redraws of that grid — the same as an observer's.
// Ending it in any way (Close, the process killed, this process gone: its stdin closes) gives the slot back; herdr puts
// the pane back to its own size only while a herdr client is attached, so the node ends it with CloseAt the size the
// pane had before.
type Control struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	writes    chan []byte
	writeDone chan struct{}
	done      chan struct{}
	once      sync.Once
	reason    string // terminal.closed's reason, once it came
	cols      int    // the size the pane's programs see (the last asked for)
	rows      int
	mu        sync.Mutex
	aborted   bool // StartControl aborted before returning s; reader must not invoke fn
	inFirstFn bool // reader is currently running fn for the first frame
}

// ErrAttachBusy: another client (Ovis, a second herdr client) holds the terminal's attach slot; take it over to go on.
var ErrAttachBusy = errors.New("attach_busy")

// ErrTakenOver: the slot was taken by someone else while we held it.
var ErrTakenOver = errors.New("taken_over")

// controlWait bounds the wait for herdr's first answer (a frame, or why not).
const controlWait = 8 * time.Second

// StartControl takes the pane's terminal at cols × rows and streams frames to fn until it ends (ctx ending ends it). It returns once
// herdr answered: with the first frame already given to fn, or ErrAttachBusy (takeover false and the slot is held).
func (c *Client) StartControl(ctx context.Context, pane string, cols, rows int, takeover bool, fn func(Frame)) (*Control, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := []string{"terminal", "session", "control", pane, "--cols", fmt.Sprint(cols), "--rows", fmt.Sprint(rows)}
	if takeover {
		args = append(args, "--takeover")
	}
	cmd := c.command(args...) // ctx ending lets go through Close: stdin first, a kill only if that fails
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("herdr terminal session control: %w", err)
	}
	s := &Control{
		cmd:       cmd,
		in:        in,
		writes:    make(chan []byte, 1024),
		writeDone: make(chan struct{}),
		done:      make(chan struct{}),
		cols:      cols,
		rows:      rows,
	}
	first := make(chan error) // unbuffered handoff: frames after the first run only if StartControl returns s
	abortCh := make(chan struct{})
	firstFnDone := make(chan struct{})
	go s.writeLoop()
	go func() {
		defer close(s.done)
		r := bufio.NewReaderSize(stdout, 256<<10)
		started, discard := false, false
		for {
			line, err := readLine(r, maxLine)
			if err != nil {
				break
			}
			var m struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
				Frame
			}
			if json.Unmarshal(line, &m) != nil {
				continue
			}
			switch m.Type {
			case "terminal.frame":
				if discard {
					continue
				}
				if !started {
					s.mu.Lock()
					if s.aborted {
						s.mu.Unlock()
						discard = true
						continue
					}
					s.inFirstFn = true
					s.mu.Unlock()
					var aborted bool
					func() {
						defer func() {
							s.mu.Lock()
							s.inFirstFn = false
							aborted = s.aborted
							close(firstFnDone)
							s.mu.Unlock()
						}()
						fn(m.Frame)
					}()
					if aborted {
						discard = true
						continue
					}
					select {
					case first <- nil:
						started = true
					case <-abortCh:
						discard = true
					}
				} else {
					fn(m.Frame)
				}
			case "terminal.closed":
				s.mu.Lock()
				s.reason = m.Reason
				s.mu.Unlock()
			}
		}
		_ = cmd.Wait()
		if !started {
			select {
			case first <- s.endError(strings.TrimSpace(stderr.String())):
			case <-abortCh:
			}
		}
	}()
	timer := time.NewTimer(controlWait)
	defer timer.Stop()
	select {
	case err := <-first:
		if err != nil {
			s.Close()
			return nil, err
		}
		if ctx.Done() != nil {
			go func() {
				select {
				case <-ctx.Done():
					s.Close()
				case <-s.done:
				}
			}()
		}
		return s, nil
	case <-timer.C:
		return nil, s.abortStart(abortCh, firstFnDone, fmt.Errorf("herdr terminal session control: no frame in %v", controlWait))
	case <-ctx.Done():
		return nil, s.abortStart(abortCh, firstFnDone, ctx.Err())
	}
}

// abortStart stops a control session whose StartControl timed out or was canceled, ensuring no
// callback runs concurrently with or after StartControl returning an error.
func (s *Control) abortStart(abortCh, firstFnDone chan struct{}, err error) error {
	s.mu.Lock()
	s.aborted = true
	waitFirst := s.inFirstFn
	s.mu.Unlock()
	close(abortCh)
	s.Close()
	if waitFirst {
		<-firstFnDone
	}
	return err
}

// endError is why the session ended, from herdr's terminal.closed reason (or its stderr).
func (s *Control) endError(stderr string) error {
	s.mu.Lock()
	reason := s.reason
	s.mu.Unlock()
	switch {
	case strings.Contains(reason, "already has an attached client"):
		return fmt.Errorf("%w: %s", ErrAttachBusy, reason)
	case strings.Contains(reason, "taken over"):
		return fmt.Errorf("%w: %s", ErrTakenOver, reason)
	case reason != "":
		return fmt.Errorf("herdr terminal session control: %s", reason)
	case stderr != "":
		return fmt.Errorf("herdr terminal session control: %s", stderr)
	}
	return errors.New("herdr terminal session control: ended")
}

func (s *Control) writeLoop() {
	if s.writeDone != nil {
		defer close(s.writeDone)
	}
	for {
		select {
		case b := <-s.writes:
			if b == nil { // CloseAt: what was queued before is written
				s.Close()
				return
			}
			if _, err := s.in.Write(b); err != nil {
				s.Close()
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *Control) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	select {
	case <-s.done:
		return errors.New("terminal control ended")
	case <-s.writeDone:
		return errors.New("terminal control ended")
	default:
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case s.writes <- append(b, '\n'):
		return nil
	case <-s.done:
		return errors.New("terminal control ended")
	case <-s.writeDone:
		return errors.New("terminal control ended")
	case <-timer.C: // herdr stopped reading: the session is as good as gone
		return errors.New("terminal control is not taking input")
	}
}

// Input types text into the terminal: the bytes as they are (a key is its escape sequence: "\r", "\x1b[A").
func (s *Control) Input(text string) error {
	return s.send(map[string]any{"type": "terminal.input", "text": text})
}

// Resize gives the pane's programs a new size; herdr answers with a whole frame of it.
func (s *Control) Resize(cols, rows int) error {
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	return s.send(map[string]any{"type": "terminal.resize", "cols": cols, "rows": rows})
}

// Redraw asks for a whole frame of the screen as it is: a resize to the size it has, which herdr answers with a whole
// frame while the programs see no change (no SIGWINCH).
func (s *Control) Redraw() error {
	s.mu.Lock()
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	return s.Resize(cols, rows)
}

// maxWheel bounds the wheel events one Scroll sends (a fling is a few dozen rows; a phone sends a few at a time).
const maxWheel = 120

// Scroll is the mouse wheel over a cell of the screen (0-based; negative: the middle), lines up (back in history) or
// down: herdr gives it to a program that takes the mouse (Claude Code's fullscreen renderer and Codex scroll their
// conversation) and scrolls its own scrollback for one that does not (a shell) — what a herdr client does with the wheel.
// It goes as one event a line: herdr hands a program one wheel event per command whatever "lines" says (Codex moves a
// row for each), while its own scrollback moves "lines" either way. And over a cell, never herdr's default 1;1: there
// Codex has the prompt it pins on top, which takes no wheel (docs/integrations/terminal.md, section 16).
func (s *Control) Scroll(lines int, up bool, column, row int) error {
	dir := "down"
	if up {
		dir = "up"
	}
	if column < 0 || row < 0 {
		s.mu.Lock()
		column, row = s.cols/2, s.rows/2
		s.mu.Unlock()
	}
	for range min(lines, maxWheel) {
		if err := s.send(map[string]any{"type": "terminal.scroll", "lines": 1, "direction": dir, "column": column, "row": row}); err != nil {
			return err
		}
	}
	return nil
}

// Click is a left click at a cell of the screen (0-based column and row): a program that takes the mouse gets it —
// Claude Code's fullscreen renderer jumps to the prompt pinned on top, or to the bottom from "Jump to bottom".
func (s *Control) Click(column, row int) error {
	for _, action := range []string{"down", "up"} {
		if err := s.send(map[string]any{"type": "terminal.mouse", "action": action, "button": "left", "column": column, "row": row}); err != nil {
			return err
		}
	}
	return nil
}

// Mouse modifier bits of herdr's terminal.mouse / terminal.scroll "modifiers" (a u8): shift 1, ctrl 2, alt 4. Anything
// above is ignored by herdr, so it is masked off here (docs/integrations/terminal.md "Mouse").
const (
	ModShift    = 1
	ModCtrl     = 2
	ModAlt      = 4
	modifierMax = ModShift | ModCtrl | ModAlt
)

// maxMouseCell bounds a column or row: herdr takes a u16 and drops a cell outside the pane, so a bigger number is garbage.
const maxMouseCell = 4096

// MouseEvent is one terminal.mouse: action down | up | drag (motion with the button held) | move (motion with none;
// herdr hands it to a program only when that asked for any-event tracking, ?1003), button left | middle | right, a cell
// of the screen (0-based) and the modifier bits above.
type MouseEvent struct {
	Action    string
	Button    string
	Column    int
	Row       int
	Modifiers int
}

// Valid is whether herdr takes this event (it ignores, and logs, one it cannot parse).
func (e MouseEvent) Valid() bool {
	switch e.Action {
	case "down", "up", "drag", "move":
	default:
		return false
	}
	switch e.Button {
	case "left", "middle", "right":
	default:
		return false
	}
	return e.Column >= 0 && e.Row >= 0 && e.Column < maxMouseCell && e.Row < maxMouseCell
}

// Mouse sends one mouse event to the pane: herdr gives it to a program that takes the mouse (in the encoding that program
// asked for) and drops it for one that does not — it never selects or scrolls on its own, so the caller does not need to
// know the program's mode. An event herdr would not parse is refused here instead.
func (s *Control) Mouse(e MouseEvent) error {
	if !e.Valid() {
		return fmt.Errorf("terminal.mouse: invalid event %+v", e)
	}
	m := map[string]any{"type": "terminal.mouse", "action": e.Action, "button": e.Button, "column": e.Column, "row": e.Row}
	if mods := e.Modifiers & modifierMax; mods != 0 {
		m["modifiers"] = mods
	}
	return s.send(m)
}

// Done is closed when the session ended (released, taken over, the pane gone).
func (s *Control) Done() <-chan struct{} { return s.done }

// Err is why it ended (after Done): ErrTakenOver when someone took the slot.
func (s *Control) Err() error { return s.endError("") }

// CloseAt gives the slot back after resizing the pane to cols × rows: stdin closes once the resize is written. herdr
// lays a released pane out at its client's size only while a herdr client is attached; with none it leaves the pane at
// the size it last had, so a control that ends with Close leaves the pane at the controller's size (measured, herdr
// 0.9.3; docs/integrations/terminal.md section 11). Without a size (0), at the size it already has, or when the resize
// cannot be queued, it is Close.
func (s *Control) CloseAt(cols, rows int) {
	s.mu.Lock()
	same := cols == s.cols && rows == s.rows
	s.mu.Unlock()
	if cols <= 0 || rows <= 0 || same || s.Resize(cols, rows) != nil {
		s.Close()
		return
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case s.writes <- nil: // the write loop closes stdin when it gets here, after the resize
	case <-s.done:
		s.Close()
	case <-s.writeDone:
		s.Close()
	case <-timer.C: // herdr stopped reading
		s.Close()
	}
}

// Close gives the slot back: stdin closes, herdr detaches and, while a herdr client is attached, lays the pane out at
// that client's size again (with none it keeps the size it has: CloseAt). A process that does not exit soon after is
// killed (that releases it as well).
func (s *Control) Close() {
	s.once.Do(func() {
		if s.in != nil {
			_ = s.in.Close()
		}
		if s.cmd == nil {
			return
		}
		go func() {
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-s.done:
			case <-timer.C:
				if s.cmd.Process != nil {
					_ = s.cmd.Process.Kill()
				}
			}
		}()
	})
}
