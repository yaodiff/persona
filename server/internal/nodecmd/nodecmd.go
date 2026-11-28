// Package nodecmd is the device's command line: what a computer runs for its node and its Agents (`node --stdio`,
// `node --attach`, `hook`, `mcp`, `ask`, `notify`, `create-direction`, `write-progress`, `launch`, …). The device's own
// program `persona-node` is this alone;
// `personad` dispatches the same commands here and adds the hub's and the manager's. It must
// not link the hub, the manager or the database (cmd/persona-node/main_test.go guards that).
package nodecmd

import (
	_ "github.com/nwverse/persona/server/internal/modules/all/node" // the modules that run on the node (Provider seams)
	// A command started in a folder that does not answer: `tool` goes on from "/", the rest goes back to the folder
	// (internal/startdir moves it before Go's os package would wait for the folder; settle decides once os has started).
	_ "github.com/nwverse/persona/server/internal/startdir/settle"

	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/machineops"
	"github.com/nwverse/persona/server/internal/mcpserver"
	"github.com/nwverse/persona/server/internal/node"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodecmd/commandport"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/nodeupdate"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/procs"
	"github.com/nwverse/persona/server/internal/scriptcli"
	"github.com/nwverse/persona/server/internal/toolcli"
)

// Build is what the program was built as (its -X flags).
type Build struct {
	Version string
	Program string // nodeproto.ProgramPersonad or ProgramPersonaNode: what node.info says the node runs as
	// Edition is the edition the program was built as (update.ThisEdition), which node.info says so that an update
	// brings the same one. The zero value is the full build.
	Edition nodeproto.Edition
}

// Usage is the device commands' part of the usage text (the node part is NodeUsage), with the lines of the commands
// the node-side modules of this build add (internal/nodecmd/commandport).
func Usage() string { return strings.TrimSuffix(deviceUsage+"\n"+commandport.CommandUsage(), "\n") }

const deviceUsage = `  personad resource status [resource]  shared runner status as JSON
  personad gate <project> <ref> [groups...]  join or reuse identical gate work
  personad hook             Claude Code hook entry (reads hook JSON on stdin)
  personad mcp              Claude Code MCP server (stdio)
  personad ask "question" [--options a,b,c] [--timeout 10m] [--require-remote]
                           block until the question is answered on the phone; exit 0 allowed / option chosen (printed),
                           1 denied, 2 usage, 3 timeout, 4 phone unreachable (docs/integrations/script-cli.md)
  personad notify "title" [body] [--silent] [--error] [--strict]
                           one notification to the phone; exit 0 whatever happens unless --strict (docs/integrations/script-cli.md)
  personad decision raise --file decision.json | get <id> | withdraw <id>
                           raise a rich owner decision without waiting, then poll or withdraw it (docs/integrations/script-cli.md)
  personad tool <tool> [<json args> | -] | --cli <connector> [--format json|text] <words…>
                           a connector's tools from the assistant's shell, or an Agent's own tools from its pane
                           (docs/protocol/connector-protocol.md "Command line"; personad tool --help)
  personad ops storage|hotspots|toolchain|network|ports|processes|services|service-logs|mcp|skill …
                           on-demand read-only machine diagnostics CLI, stdio MCP server, and opt-in skill manager: personad ops --help
  personad claude-plugin status|install|uninstall|on|off
                           the Persona plugin for Claude Code on this computer, as JSON (persona-link plugin says it in
                           words; docs/operations/link.md "Claude Code plugin")
  personad codex-start <file>
                           run the Codex launcher in file (written by the node, removed once read) with the first
                           message as its last argument, no shell in between (docs/integrations/codex.md "Starting from the phone")`

// SysmonUsage is `sysmon`'s line in the usage text.
const SysmonUsage = `  personad sysmon [n]       print this machine's system info and 1 + n metric samples (n default 3, at most 8640,
                           every 10 s), with what they cost`

// LaunchUsage is `launch`'s line in the usage text.
const LaunchUsage = `  personad launch <name> …   run claude with a launcher kept in Settings › Models (from the local node)`

// Run runs args (args[0] is the command) when it is a device command, and says whether it was. stop undoes the caller's
// signal handling (launch execs claude and leaves signals as they came). A command that has an exit code of its own
// exits here; err is for the caller to report (UsageError: print it and exit 2).
func Run(ctx context.Context, stop func(), args []string, b Build) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	rest := args[1:]
	switch args[0] {
	case "resource":
		return true, resourceCommand(ctx, rest)
	case "gate":
		os.Exit(gateCommand(ctx, rest, os.Stdin, os.Stdout, os.Stderr))
	case sandbox.AgentExecCommand:
		stop()
		return true, sandbox.RunAgentExec(rest)
	case "node":
		return true, runNode(ctx, rest, b)
	case "hook":
		nodeclient.RunHook(ctx, rest, os.Stdin, os.Stdout)
	case "launch": // a launcher kept in Settings › Models: its settings from the local node, then claude (docs/integrations/launchers.md)
		ticket, rest, err := launchTicket(rest) // --ticket: a hand-off's values too (node/secretenv.go)
		if err != nil || len(rest) < 1 {
			return true, UsageError("usage: " + CommandName() + " launch [--ticket <ticket>] <launcher> [claude args…]\n       " +
				CommandName() + " launch --ticket <ticket> -- claude [claude args…]")
		}
		return true, runLaunch(stop, ticket, rest[0], rest[1:]) // launch.go: bounded wait for the node, Ctrl-C ends it
	case "assistant-tui": // an assistant conversation's interactive Claude Code, as claude -p to the node (node/assistant_tui.go)
		os.Exit(node.AssistantTUI(rest, os.Stdin, os.Stdout, os.Stderr))
	case "model-source": // final Codex provider attribution, after launcher expansion
		return true, node.ModelSourceLaunch(rest)
	case "codex-start": // a Codex launcher with the phone's first message, read from a file (node/codex_start.go)
		return true, node.CodexStart(rest)
	case "tab-start": // a command a runtime gave to resume its session in a new tab (`pi --session …`, node/pi.go)
		return true, node.TabStart(rest)
	case "mcp":
		return true, mcpserver.Serve(ctx, b.Version, os.Stdin, os.Stdout)
	case "tool": // a connector's tools and command line from the assistant's shell (internal/toolcli)
		os.Exit(toolcli.Main(ctx, rest, os.Stdin, os.Stdout, os.Stderr))
	case "ask": // a script asks the phone (internal/scriptcli)
		os.Exit(scriptcli.Ask(ctx, rest, os.Stdin, os.Stdout, os.Stderr))
	case "notify":
		os.Exit(scriptcli.Notify(ctx, rest, os.Stdout, os.Stderr))
	case "create-direction":
		os.Exit(scriptcli.CreateDirection(ctx, rest, os.Stdout, os.Stderr))
	case "write-progress":
		os.Exit(scriptcli.WriteProgress(ctx, rest, os.Stdout, os.Stderr))
	case "decision":
		os.Exit(scriptcli.Decision(ctx, rest, os.Stdin, os.Stdout, os.Stderr))
	case "ops":
		os.Exit(machineops.Main(ctx, b.Version, rest, os.Stdin, os.Stdout, os.Stderr))
	case "claude-plugin": // the Persona plugin for Claude Code here, as JSON (node/claudeplugin.go; persona-link plugin)
		os.Exit(node.ClaudePluginCommand(ctx, rest, os.Stdout, os.Stderr))
	case "sysmon":
		n, err := sysmonCount(rest)
		if err != nil {
			return true, UsageError(err.Error())
		}
		runSysmon(ctx, n)
	case "node-update-check":
		return true, json.NewEncoder(os.Stdout).Encode(nodeupdate.Check{
			Version: b.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Program: b.Program,
			ResidentHandover: node.ResidentHandoverSupported, AdoptionMarker: true,
		})
	case "node-update-watch":
		return true, runNodeUpdateWatch(ctx, rest)
	default:
		run, ok := commandport.LookupCommand(args[0]) // a module's command (internal/modules/all/node)
		if !ok {
			return false, nil
		}
		os.Exit(run(ctx, rest, os.Stdin, os.Stdout, os.Stderr))
	}
	return true, nil
}

// runNode is `node …`: the node protocol on stdin / stdout (relayed to a resident node when one runs), the resident
// node, tailcat, doctor, reset-uid.
func runNode(ctx context.Context, args []string, b Build) error {
	if len(args) == 1 && IsHelpArg(args[0]) {
		os.Stdout.WriteString(Named(NodeUsage) + "\n")
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "--attach":
			supervised, err := residentArgs(args[1:])
			if err != nil {
				exitNodeUsage(err)
			}
			osproc.SealInherited() // as the node on stdin / stdout does, below
			// runResident replaces itself on a restart or update (syscall.Exec): what the previous image started (the
			// update watchdog, the herdr server, bridges) is this one's child now, and only this reaps it.
			osproc.ReapInherited()
			procs.AtStart()
			return runResident(ctx, b, supervised)
		case "tailcat":
			return runNodeTailcat(ctx, args[1:])
		case "doctor":
			failed, err := node.DoctorCommand(ctx, args[1:], os.Stdout)
			if err == nil && failed {
				os.Exit(1)
			}
			return err
		case "agy-hooks": // Persona's entries in the Antigravity CLI's hooks file (node/agyhooks.go)
			return node.AgyHooksCommand(args[1:], os.Stdout)
		case "reset-uid":
			if err := noExtraArgs("reset-uid", args[1:]); err != nil {
				exitNodeUsage(err)
			}
			return resetMachineUID()
		}
	}
	if err := checkNodeStdioArgs(args); err != nil {
		exitNodeUsage(err)
	}
	// What a parent left inheritable (an older hub's listening socket, through its manager) stops here, not in every
	// agent and program the node starts. Here, not in main: other commands of this binary hand descriptors on by exec.
	osproc.SealInherited()
	procs.AtStart() // the resident node's too, above
	relayed, err := node.Relay(ctx, os.Stdin, os.Stdout)
	if relayed {
		if errors.Is(err, io.EOF) {
			err = nil
		}
		return err
	}
	// Serving itself, it starts children: whatever a parent that exec'd into it left is reaped first, as the resident
	// node does above. A relay starts none, so it skips the scan.
	osproc.ReapInherited()
	n, err := node.New(b.Version)
	if err != nil {
		return err
	}
	n.Program = b.Program
	n.Edition = b.Edition
	if err = n.ServeStdio(ctx); errors.Is(err, io.EOF) {
		err = nil
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	n.StopUnusedHerdr(stop) // a herdr it started and nobody used does not outlive it (#10114)
	return err
}
