package update

import (
	"debug/buildinfo"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// ProgramOf says which program the executable at path is, from the Go build info it carries (readable for any OS and
// architecture, nothing is run): nodeproto.ProgramPersonaNode for a build of server/cmd/persona-node, else
// nodeproto.ProgramPersonad (personad and every older build, which do everything persona-node does and more), or ""
// when path is not a Go executable that can be read. It knows persona-node by its package path, so a build of the
// package (go build ./cmd/persona-node, as deploy.sh, publish.sh and mac-link.sh do), not of a file list (go build
// main.go records command-line-arguments).
func ProgramOf(path string) string {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(info.Path, "/cmd/"+nodeproto.ProgramPersonaNode) {
		return nodeproto.ProgramPersonaNode
	}
	return nodeproto.ProgramPersonad
}

// CheckReplace refuses to put persona-node (next) where personad (current) is: personad may run a hub or a manager
// besides the node, which persona-node cannot. Every other swap is fine, persona-node to personad included (personad
// does everything persona-node does), and so is persona-node into the device's own file (bin/persona-node), whatever it
// holds now: no hub or manager service runs that file. A current file that cannot be read says nothing and does not
// block.
func CheckReplace(current, next string) error {
	// Keep the older, more specific program-identity refusal when both rules would refuse the swap. A device's own
	// persona-node file is the exception, but it still goes through the edition check below.
	if !IsDeviceFile(current) && ProgramOf(current) == nodeproto.ProgramPersonad && ProgramOf(next) == nodeproto.ProgramPersonaNode {
		return fmt.Errorf("%s is personad, which may run a hub or a manager too: not replacing it with persona-node", current)
	}
	// An install keeps its edition (docs/operations/core-install.md "Updates stay core-only"): a core-only program
	// is never replaced by a full build, nor a full one by a core-only build. Both are read from their build info; a
	// file that cannot be read says nothing and does not block, as below.
	if cur, ok := EditionOf(current); ok {
		if nxt, ok := EditionOf(next); ok && nxt != cur {
			return fmt.Errorf("%s is the %s edition: not replacing it with a %s build", current, cur, nxt)
		}
	}
	return nil
}

// IsDeviceFile says path is the device's own program file, <dir>/persona-node (persona-node.exe on Windows), which only
// a node service runs (docs/operations/link.md "The device's program").
func IsDeviceFile(path string) bool {
	return strings.TrimSuffix(filepath.Base(path), ".exe") == nodeproto.ProgramPersonaNode
}
