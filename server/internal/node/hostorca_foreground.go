package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
)

// What runs in an Orca terminal (#17039, docs/integrations/engines.md "Agents run in a terminal"). Orca's
// terminal.inspectProcess, fenced to the terminal's incarnation, names the program in front of the terminal's shell, or
// none when the shell itself is in front; it also says when it proved the shell idle (nothing else in the terminal,
// idleShellProven). Its answers marked unverifiable say nothing. An Orca without the call (method_not_found) leaves
// only the node's own process scan, which knows the supported agents and nothing else: a terminal it finds no agent in
// may hold a shell, an editor, a dev server or another CLI, so that is "cannot tell", never "only a shell".

var _ host.ForegroundNamer = (*orcaHost)(nil)

// errOrcaCannotTell: Orca's answer does not say what runs in the terminal (unverifiable, or a program it does not
// name); nothing that depends on it happens.
var errOrcaCannotTell = errors.New("orca cannot tell what runs in the terminal")

// inspect is terminal.inspectProcess of the pane's terminal; supported false when this Orca has no such call.
func (h *orcaHost) inspect(ctx context.Context, paneKey string) (p *orcart.Process, supported bool, err error) {
	t, err := h.terminal(ctx, paneKey, false)
	if err != nil {
		return nil, true, orcaErr(err)
	}
	err = h.onTerminal(ctx, t, false, func(tg orcart.Target) error {
		var e error
		p, e = h.c.InspectProcess(ctx, tg)
		return e
	})
	if orcart.IsCode(err, "method_not_found") || orcart.IsCode(err, "method_not_supported") {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, orcaErr(err)
	}
	return p, true, nil
}

// PaneForeground names what is in front of the pane's terminal: the program Orca names, or the terminal's shell (its
// argv[0], "-zsh" for a login shell) when nothing is in front of it. On an Orca without terminal.inspectProcess, the
// supported agent the process scan finds in front, else an error: the scan cannot tell a shell from anything else.
func (h *orcaHost) PaneForeground(ctx context.Context, pane host.PaneRef) ([]string, error) {
	p, supported, err := h.inspect(ctx, pane.ID)
	if err != nil {
		return nil, err
	}
	if !supported {
		procs, err := h.agentProcs(orcaProcsMaxAge)
		if err != nil {
			return nil, fmt.Errorf("orca: cannot tell what runs in %s: %w", pane.ID, err)
		}
		if ps := h.procsIn(procs, pane.ID); len(ps) > 0 {
			return []string{ps[0].agent}, nil
		}
		return nil, fmt.Errorf("%w: this Orca cannot say what runs in %s (no terminal.inspectProcess)", host.ErrUnsupported, pane.ID)
	}
	switch {
	case !p.Known():
		return nil, fmt.Errorf("%w in %s (%s)", errOrcaCannotTell, pane.ID, p.Unverified())
	case p.Foreground() != "":
		return []string{p.Foreground()}, nil
	case p.HasChildren():
		return nil, fmt.Errorf("%w in %s: something other than the shell is in front", errOrcaCannotTell, pane.ID)
	}
	shell := orcaShellName(p.ShellPID())
	if shell == "" {
		return nil, fmt.Errorf("%w in %s: the shell in front has no name", errOrcaCannotTell, pane.ID)
	}
	return []string{shell}, nil
}

// PaneShellAtRest: Orca proved the pane's terminal holds its shell at rest and nothing else — no program in front, no
// job behind the shell or stopped, no output since it looked (restingShell). false, without an error, for a terminal
// it can read that is busy or holds a job; an error when it cannot tell, or has no terminal.inspectProcess.
func (h *orcaHost) PaneShellAtRest(ctx context.Context, pane host.PaneRef) (bool, error) {
	p, supported, err := h.inspect(ctx, pane.ID)
	if err != nil {
		return false, err
	}
	if !supported {
		return false, fmt.Errorf("%w: this Orca cannot say whether %s is at rest (no terminal.inspectProcess)", host.ErrUnsupported, pane.ID)
	}
	if !p.Known() {
		return false, fmt.Errorf("%w in %s (%s)", errOrcaCannotTell, pane.ID, p.Unverified())
	}
	return orcaIdleShell(p), nil
}

func orcaIdleShell(p *orcart.Process) bool {
	return p.Known() && p.IdleShell() && p.Foreground() == "" && !p.HasChildren()
}

// shellInFront: Orca reads t (the exact terminal and incarnation, no stale-handle retry) with only its shell in front;
// for the guarded Run into a terminal the node did not just open (orcaHost.AdoptPane). An Orca without
// terminal.inspectProcess cannot say, which refuses.
func (h *orcaHost) shellInFront(ctx context.Context, t orcart.Terminal) error {
	p, err := h.c.InspectProcess(ctx, t.Target())
	switch {
	case orcart.IsCode(err, "method_not_found"), orcart.IsCode(err, "method_not_supported"):
		return host.Mark(fmt.Errorf("%w: this Orca cannot say what runs in %s", host.ErrUnsupported, t.PaneKey()), host.ErrNotDelivered)
	case err != nil:
		return orcaInputErr(err)
	case !p.Known():
		return host.Mark(fmt.Errorf("%w in %s (%s)", errOrcaCannotTell, t.PaneKey(), p.Unverified()), host.ErrNotDelivered)
	case p.HasChildren() || p.Foreground() != "" && !shellProgram(p.Foreground()):
		what := p.Foreground()
		if what == "" {
			what = "a program"
		}
		return host.Mark(fmt.Errorf("orca: %s runs in %s, not its shell at rest: nothing typed", what, t.PaneKey()), host.ErrNotDelivered)
	}
	return nil
}

// orcaShellName names a terminal's shell from its process (Orca's evidence fence): its argv[0], "-zsh" for a login
// shell, else the user's shell, which Orca opens by default (as a start into a fresh Orca terminal assumes,
// freshPaneShells); "" when neither is known.
func orcaShellName(pid int) string {
	if pid > 0 {
		if argv, err := processArgv(pid); err == nil && len(argv) > 0 && argv[0] != "" {
			return argv[0]
		}
	}
	return herdrPaneShell()
}
