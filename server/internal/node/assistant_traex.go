package node

import (
	"encoding/json"
	"errors"
	"os/exec"

	"github.com/nwverse/persona/server/internal/compat"
)

var errTraexAssistantOrigin = errors.New("TRAE CLI headless execution requires an attested Inbox conversation")

func (n *Node) traexAssistantArgs(key, effort string) ([]string, bool) {
	personaMCPOnly := n.hubSupportsPersonaContracts()
	args := codexArgsForHome(n.bin, key, n.dir, !personaMCPOnly)
	args = append(args,
		"-c", "features.tool_call_mcp_elicitation=true",
		"-c", "features.request_permissions_tool=false",
		"-c", "features.skill_mcp_dependency_install=false",
		"-c", "features.hooks=false",
		"-c", "features.plugins=false",
	)
	if effort != "" {
		value, _ := json.Marshal(effort)
		args = append(args, "-c", "model_reasoning_effort="+string(value))
	}
	return args, personaMCPOnly
}

func (n *Node) traexAssistantCommand(shell, launcher string, args []string) *exec.Cmd {
	// app-server accepts -c and --stdio. Model and effort are also set in the RPC;
	// the terminal-only -m flag cannot follow this subcommand.
	return exec.Command(shell, append([]string{shellFlag(shell), n.launcherForScript(launcher) + ` app-server --stdio "$@"`, "persona"}, args...)...)
}

func (c *codexConn) traexMCPNames() []string {
	names := []string{"persona"}
	if !c.p.personaMCPOnly {
		names = append(names, compat.MCPServer)
	}
	return names
}

func (c *codexConn) isTraexPersonaMCP(server string) bool {
	return server == "persona" || !c.p.personaMCPOnly && server == compat.MCPServer
}
