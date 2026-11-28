//go:build unix

package hubedge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var errUpdateRecovery = errors.New("update recovery failed")

// startAgain has the loop start binary as the serving slot, after the backoff the failures so far earn.
func (e *edge) startAgain(binary string) {
	e.retryBinary = binary
	e.retry = time.After(backoff(e.failures))
}

func (e *edge) supervise() error {
	defer e.stop()
	for {
		if e.ctx.Err() != nil {
			return nil
		}
		p := e.current()
		var msgs <-chan Msg
		var exited <-chan struct{}
		if p != nil {
			msgs, exited = p.msgs, p.exited
		}
		var confirm <-chan time.Time
		if e.confirmBy != nil {
			confirm = e.confirmBy.C
		}
		select {
		case <-e.ctx.Done():
			return nil
		case m, ok := <-msgs:
			if !ok {
				if e.ctx.Err() != nil {
					return nil
				}
				<-p.exited
				continue // exited, next round
			}
			if e.ctx.Err() != nil {
				return nil
			}
			if err := e.fromActive(p, m); err != nil {
				return err
			}
		case <-exited:
			e.setActive(nil)
			e.hold()
			e.clearConfirm()
			if e.ctx.Err() != nil {
				return nil
			}
			if p.exitCode == RespawnCode {
				e.logf("hub edge: slot %s asked to be started again", p.name)
				e.failures = 0
			} else {
				e.logf("hub edge: slot %s exited (code %d); starting it again", p.name, p.exitCode)
				if time.Since(p.started) > time.Minute {
					e.failures = 0
				}
				e.failures++
			}
			if e.failures >= maxFailedStarts {
				return fmt.Errorf("hub edge: the hub exited %d times in a row", e.failures)
			}
			e.startAgain(p.binary)
		case <-e.retry:
			e.retry = nil
			if e.ctx.Err() != nil {
				return nil
			}
			np, err := e.startActive(e.retryBinary)
			if err != nil {
				if e.ctx.Err() != nil {
					return nil
				}
				e.failures++
				e.logf("hub edge: the hub did not start: %v", err)
				if e.failures >= maxFailedStarts {
					return fmt.Errorf("hub edge: the hub did not start %d times in a row: %w", e.failures, err)
				}
				e.startAgain(e.retryBinary)
				continue
			}
			e.setActive(np)
		case r := <-e.reqs:
			if e.ctx.Err() != nil {
				r.done <- e.ctx.Err()
				return nil
			}
			if p == nil {
				r.done <- errors.New("no hub is serving right now (it is being started again)")
				continue
			}
			err := e.switchTo(r)
			r.done <- err
			if errors.Is(err, errUpdateRecovery) {
				return err
			}
		case <-confirm:
			if err := e.unconfirmed(); err != nil {
				return err
			}
		}
	}
}

func backoff(failures int) time.Duration {
	d := time.Second
	for i := 1; i < failures && d < 30*time.Second; i++ {
		d *= 2
	}
	return d
}

// fromActive handles what the serving slot says on its own.
func (e *edge) fromActive(p *proc, m Msg) error {
	switch m.T {
	case MsgSwitch:
		if err := validBinary(m.Binary); err != nil {
			p.send(Msg{T: MsgSwitchFailed, Reason: err.Error()})
			return nil
		}
		if err := e.switchTo(switchReq{m: m, from: p, progress: func(string) {}}); err != nil {
			if errors.Is(err, errUpdateRecovery) {
				return err
			}
			if e.ctx.Err() == nil && p.alive() && e.current() == p {
				p.send(Msg{T: MsgSwitchFailed, Reason: err.Error()})
			}
		}
	case MsgConfirmed:
		if e.confirmBy != nil {
			e.logf("hub edge: the update to build %d is confirmed", e.confirmBuild)
		}
		e.clearConfirm()
	}
	return nil
}

func (e *edge) clearConfirm() {
	if e.confirmBy != nil {
		e.confirmBy.Stop()
		e.confirmBy = nil
	}
}

// switchTo replaces the active slot by r's binary: the new one starts as a standby and checks itself while the old
// one serves; only then are connections held back, the old one released, the new one activated, and the route moved.
// A new binary that does not start leaves the old one serving; one that fails while taking over is replaced by the
// binary that ran before (r's Prev, else the old slot's).
func (e *edge) switchTo(r switchReq) error {
	m, old := r.m, e.current()
	progress := r.progress
	if progress == nil {
		progress = func(string) {}
	}
	e.logf("hub edge: switching to %s (%s)", m.Binary, m.Kind)
	progress("starting")
	green, err := e.spawn(m.Binary, true)
	if err != nil {
		return fmt.Errorf("start %s: %w", m.Binary, err)
	}
	err = e.await(green, MsgHello, e.o.StandbyWait)
	if err == nil && green.hello.Proto < 1 {
		err = errors.New("it does not speak the edge's protocol")
	}
	if err == nil && m.Expect != 0 && green.hello.Build != m.Expect {
		err = fmt.Errorf("it is build %d, not %d", green.hello.Build, m.Expect)
	}
	if err == nil {
		err = e.await(green, MsgStandby, e.o.StandbyWait)
	}
	if err != nil {
		e.kill(green)
		if e.ctx.Err() != nil {
			return e.ctx.Err()
		}
		e.logf("hub edge: %s did not start: %v; slot %s goes on", m.Binary, err, nameOf(old))
		return fmt.Errorf("the new hub did not start: %w", err)
	}
	if e.ctx.Err() != nil {
		e.kill(green)
		return e.ctx.Err()
	}
	progress("handing over")
	e.hold()
	e.clearConfirm()
	e.release(old)
	e.setActive(nil)
	if e.ctx.Err() != nil {
		e.kill(green)
		return e.ctx.Err()
	}
	green.send(Msg{T: MsgActivate})
	if err := e.await(green, MsgReady, e.o.ActivateWait); err != nil {
		e.kill(green)
		if e.ctx.Err() != nil {
			return e.ctx.Err()
		}
		back := m.Prev
		if m.Kind == KindUpdate && e.o.RestoreUpdate != nil {
			b, ok, restoreErr := e.o.RestoreUpdate(m.Expect, err)
			if restoreErr != nil {
				e.logf("hub edge: restoration failed; recovery retained: %v", restoreErr)
				return fmt.Errorf("%w: %w", errUpdateRecovery, restoreErr)
			}
			if ok {
				back = b // the update's files are back as they were: the binary at its path is the old one again
			}
		}
		if e.ctx.Err() != nil {
			return e.ctx.Err()
		}
		if back == "" && old != nil {
			back = old.binary
		}
		e.logf("hub edge: %s did not take over: %v; going back to %s", m.Binary, err, back)
		progress("going back")
		np, berr := e.startActive(back)
		if berr != nil {
			if e.ctx.Err() != nil {
				return e.ctx.Err()
			}
			// Nothing serves: the edge's loop starts it again, as after a crash (hold stays on meanwhile).
			e.logf("hub edge: %s did not start either: %v", back, berr)
			e.failures++
			e.startAgain(back)
			return fmt.Errorf("the new hub did not take over (%v), and %s did not start again (%v)", err, back, berr)
		}
		e.setActive(np)
		return fmt.Errorf("the new hub did not take over: %w; back on the one before", err)
	}
	e.setActive(green)
	e.logf("hub edge: slot %s serving (%s, build %d)", green.name, green.hello.Version, green.hello.Build)
	if m.Kind == KindUpdate {
		e.confirmBuild = m.Expect
		e.confirmBy = time.NewTimer(e.o.ConfirmWait)
	}
	progress("serving")
	return nil
}

func nameOf(p *proc) string {
	if p == nil {
		return "-"
	}
	return p.name
}

// unconfirmed: the slot an update started did not confirm it in time (it hangs, or cannot tell): what ran before is
// put back and runs again, the way the old watchdog did it (update-watch).
func (e *edge) unconfirmed() error {
	build := e.confirmBuild
	e.clearConfirm()
	if e.ctx.Err() != nil || e.o.RestoreUpdate == nil {
		return nil
	}
	binary, ok, err := e.o.RestoreUpdate(build, nil)
	if err != nil {
		e.logf("hub edge: restoration failed; recovery retained: %v", err)
		return fmt.Errorf("%w: %w", errUpdateRecovery, err)
	}
	if !ok || e.ctx.Err() != nil {
		return nil // confirmed, rolled back, or shutting down meanwhile
	}
	e.logf("hub edge: the update to build %d was not confirmed within %s: going back", build, e.o.ConfirmWait)
	if err := e.switchTo(switchReq{m: Msg{T: MsgSwitch, Binary: binary, Kind: KindRollback}}); err != nil {
		e.logf("hub edge: going back: %v", err)
	}
	return nil
}

// stop ends the edge: the listeners first (with socket activation, new connections wait in the socket for the next
// edge), then the serving slot the way a planned stop does.
func (e *edge) stop() {
	e.clearConfirm()
	for _, ln := range e.o.Listeners {
		_ = ln.Close()
	}
	p := e.current()
	e.hold()
	if p != nil {
		e.logf("hub edge: stopping slot %s", p.name)
		e.release(p)
	}
}

func validBinary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q is not an absolute path", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}
