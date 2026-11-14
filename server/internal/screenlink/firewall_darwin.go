package screenlink

import (
	"context"
	"os"
	"os/exec"
	"time"
)

const socketfilterfw = "/usr/libexec/ApplicationFirewall/socketfilterfw"

// firewallBlocks reports whether macOS's application firewall would stop or ask about incoming connections to this
// program. It only reads the firewall's state; it never changes it, so no prompt ever appears on the Mac.
func firewallBlocks() bool {
	exe, err := os.Executable()
	if err != nil {
		return true
	}
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, socketfilterfw, args...).Output()
		return string(out)
	}
	return firewallVerdict(run("--getglobalstate"), run("--getblockall"), run("--getappblocked", exe))
}
