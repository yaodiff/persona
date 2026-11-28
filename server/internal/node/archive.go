package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// Native archive (docs/protocol/protocol.md "session.archive"): when the user archives an agent session in Persona,
// its manager also asks the tool that owns it to archive it with the tool's own, reversible archive, and to restore
// it on unarchive. Nothing is ever deleted, and no file is moved by Persona itself:
//
//   - Codex: the app-server's thread/archive / thread/unarchive (it moves the rollout to archived_sessions and back,
//     the Codex App shows it under Archived). Through an app-server the node is connected to for that CODEX_HOME when
//     there is one (the Codex App, the shared daemon); otherwise `codex archive|unarchive <thread>`, which runs the same
//     method on an app-server of its own. A thread still open in a terminal pane, or (from the command line) held by
//     any Codex process, is not archived (Codex refuses a second writer too): it waits until that Codex exits.
//   - A runtime (docs/integrations/runtimes.md): its driver's archive / unarchive, when it declares caps.archive.
//   - Anything else (Claude Code, which has no archive of its own for local sessions; Orca; opencode; pi): answered
//     {native: "unsupported"}, and the archive stays Persona's alone.
//
// Both calls are idempotent: a session already in the asked state answers done (already: true).

const (
	nativeArchiveDone        = "done"
	nativeArchiveUnsupported = "unsupported"
)

// Refusal codes: the manager keeps the native archive pending and tries again later.
const (
	nativeArchiveBusy   = "native_archive_busy"   // the agent is working
	nativeArchiveLive   = "native_archive_live"   // a Codex process still has the thread open
	nativeArchiveFailed = "native_archive_failed" // the tool refused or failed
)

// nativeArchiveResult is what session.archive / session.unarchive answer.
type nativeArchiveResult struct {
	Native  string `json:"native"`            // done | unsupported
	Already bool   `json:"already,omitempty"` // it was so before the call
	Via     string `json:"via,omitempty"`     // app_server | cli | runtime
	Reason  string `json:"reason,omitempty"`  // why unsupported
}

func archiveUnsupported(why string) nativeArchiveResult {
	return nativeArchiveResult{Native: nativeArchiveUnsupported, Reason: why}
}

// codexArchiveTimeout bounds one archive / unarchive (an app-server answers at once; the command line starts one).
var codexArchiveTimeout = 30 * time.Second

// nativeArchive archives (archive) or restores session id in the tool that owns it. A runtime's session does not
// come here: runtimeHandle answers for it.
func (n *Node) nativeArchive(ctx context.Context, id string, archive bool) (nativeArchiveResult, error) {
	if !validID(id) {
		if strings.HasPrefix(id, agyAgent+"-") {
			// An agy row no runtime took (runtimes_calls.go): a pane's (otherSession), or a conversation's own while no
			// Antigravity companion lists it. Antigravity keeps no archive of its own either way (#14520).
			return archiveUnsupported(agyAgent + " has no archive of its own"), nil
		}
		return nativeArchiveResult{}, fmt.Errorf("invalid session id %q", id)
	}
	if n.isAssistantSession(id) || n.runtimeAssistant(id) {
		return archiveUnsupported("assistant conversations are archived by the assistant"), nil
	}
	if home, thread, ok := n.codexThread(id); ok {
		return n.codexArchive(ctx, id, home, thread, archive)
	}
	agent := "claude"
	if a, live := n.agent(id); live && a.Agent != "" {
		agent = a.Agent
	}
	return archiveUnsupported(agent + " has no archive of its own"), nil
}

// codexThread is the Codex home and thread of session id: a source the registry knows (an app-server's thread, a
// history row), or a herdr pane's thread in the default home, by its rollout.
func (n *Node) codexThread(id string) (home, thread string, ok bool) {
	if n.codices != nil {
		if src, known := n.codexSource(id); known {
			return src.home, src.thread, true
		}
	}
	if a, live := n.agent(id); live && a.Agent != "codex" {
		return "", "", false
	}
	if home := canonicalCodexHome(n.codexHome(), n.home); home != "" && rolloutPath(home, id) != "" {
		return home, id, true
	}
	return "", "", false
}

// codexArchived: the rollout at path is in home's archived_sessions (both resolved: a home named through a link).
func codexArchived(home, path string) bool {
	return realPath(filepath.Dir(path)) == realPath(filepath.Join(home, "archived_sessions"))
}

func (n *Node) codexArchive(ctx context.Context, id, home, thread string, archive bool) (nativeArchiveResult, error) {
	c := n.codexArchiveServer(id, home)
	path := rolloutPath(home, thread)
	if path == "" && (c == nil || !archive) {
		// A thread with no rollout yet is only on its app-server, which writes one when it archives it.
		return archiveUnsupported("no Codex rollout for this session"), nil
	}
	if path != "" && codexArchived(home, path) == archive {
		return nativeArchiveResult{Native: nativeArchiveDone, Already: true}, nil
	}
	if archive && n.codexArchiveBusy(id, c, thread) {
		return nativeArchiveResult{}, refusal(nativeArchiveBusy, "the agent is working: it is archived in Codex once it stops", nil)
	}
	// Never from under an open terminal: a pane still running it (its TUI would lose the thread, on a shared daemon
	// too), or, from the command line, any Codex process holding the rollout (or processes that could not be read).
	if archive {
		a, live := n.agent(id)
		if (live && a.Agent == "codex") || (c == nil && n.codices != nil && n.codexHeldNow(path)) {
			return nativeArchiveResult{}, refusal(nativeArchiveLive, "Codex still has this session open: it is archived in Codex once that Codex exits", nil)
		}
	}
	via := "app_server"
	var err error
	if c != nil {
		err = n.codexArchiveOnServer(ctx, c, thread, archive)
	} else {
		via = "cli"
		err = n.codexArchiveCLI(ctx, id, home, thread, archive)
	}
	// What Codex did is what its files say: an error after the move (a lost answer) is still done, and a call that
	// "succeeded" without moving anything is not.
	after := rolloutPath(home, thread)
	moved := after != "" && codexArchived(home, after) == archive
	n.notifyCodex()
	switch {
	case moved:
		if err != nil {
			n.logf("codex %s: %s answered %v, but the rollout moved", id, codexArchiveMethod(archive), err)
		}
		return nativeArchiveResult{Native: nativeArchiveDone, Via: via}, nil
	case errors.Is(err, errCodexNoArchiveCLI):
		return archiveUnsupported("this Codex has no archive command (update Codex)"), nil
	case err != nil:
		return nativeArchiveResult{}, refusal(nativeArchiveFailed, "Codex did not "+codexArchiveVerb(archive)+" it: "+err.Error(), nil)
	default:
		return nativeArchiveResult{}, refusal(nativeArchiveFailed, "Codex answered, but the session is not "+codexArchiveVerb(archive)+"d", nil)
	}
}

func codexArchiveMethod(archive bool) string {
	if archive {
		return "thread/archive"
	}
	return "thread/unarchive"
}

func codexArchiveVerb(archive bool) string {
	if archive {
		return "archive"
	}
	return "unarchive"
}

// codexArchiveServer is the app-server to archive through: the one running the thread, else any connected one of the
// same home (they share its thread store). nil: none is connected.
func (n *Node) codexArchiveServer(id, home string) *codexRemote {
	if n.codices == nil {
		return nil
	}
	if c, _, err := n.codexOwner(id); err == nil {
		return c
	}
	n.codices.mu.Lock()
	var remotes []*codexRemote
	for _, c := range n.codices.remotes {
		remotes = append(remotes, c)
	}
	n.codices.mu.Unlock()
	for _, c := range remotes {
		c.mu.Lock()
		ok := c.ready && c.spec.Home == home
		c.mu.Unlock()
		if ok {
			return c
		}
	}
	return nil
}

// codexArchiveBusy: a turn is running in the session (a pane working or asking, or a turn on its app-server).
func (n *Node) codexArchiveBusy(id string, c *codexRemote, thread string) bool {
	if a, live := n.agent(id); live && (a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked) {
		return true
	}
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.threads[thread]
	return ok && t.turn != ""
}

func (n *Node) codexArchiveOnServer(ctx context.Context, c *codexRemote, thread string, archive bool) error {
	cctx, cancel := context.WithTimeout(ctx, codexArchiveTimeout)
	defer cancel()
	return c.wire.call(cctx, codexArchiveMethod(archive), map[string]any{"threadId": thread}, nil)
}

// errCodexNoArchiveCLI: the codex command has no archive / unarchive subcommand (older than them).
var errCodexNoArchiveCLI = errors.New("codex has no archive command")

// codexArchiveCommand runs `codex archive|unarchive <thread>` for home and returns its output; a variable for tests.
var codexArchiveCommand = func(ctx context.Context, n *Node, launcher, home, sub, thread string) ([]byte, error) {
	shell, err := interactiveShell()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, shell, shellFlag(shell), codexSubcommandShell(n.launcherCommand(launcher), sub), "persona", thread)
	cmd.Dir = home
	cmd.Env = identity.Environment(os.Environ(), "CODEX_HOME="+home)
	killGroupOnCancel(cmd)
	return cmd.CombinedOutput()
}

// codexArchiveCLI archives or restores thread with the codex command: the launcher the session was started with when
// a resume would use it (an alias of codex in this home, not one on another app-server), else plain codex;
// CODEX_HOME is the thread's home either way.
func (n *Node) codexArchiveCLI(ctx context.Context, id, home, thread string, archive bool) error {
	launcher := "codex"
	if st, mine := n.state.get(id); mine && st.Launcher != "" {
		// A managed launcher (personad launch) runs its own words, not the shell's codex: plain codex instead.
		if _, managed := n.managedLauncher(st.Launcher); !managed {
			launcher = n.codexResumeLauncher(ctx, st.Launcher)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, codexArchiveTimeout)
	defer cancel()
	out, err := codexArchiveCommand(cctx, n, launcher, home, codexArchiveVerb(archive), thread)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(out))
	if strings.Contains(text, "unrecognized subcommand") {
		return errCodexNoArchiveCLI
	}
	if text != "" {
		return errors.New(lastLines(text, 3))
	}
	return err
}

// codexSubcommandShell runs `<launcher> <sub> "$@"` in an interactive shell: launcher is plain codex or an alias of
// it, whose words go before the subcommand, as a resume's do (codexShell does the same for app-server).
func codexSubcommandShell(launcher, sub string) string {
	return `__persona_args=("$@")
function codex { builtin command codex "$@" ` + sub + ` "${__persona_args[@]}"; }
function command { if [[ "$1" == codex ]]; then shift; \codex "$@"; else builtin command "$@"; fi; }
` + launcher
}
