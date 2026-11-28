package archguard

import (
	"go/ast"
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"
)

// The Inbox registers its routes, bus methods, settled-turn and handshake hooks and snapshot part through the kernel
// (internal/assistant/inbox_module.go, kernel/seam), so the central files those lines came from name none of its
// code again: an Inbox entry goes through its module.

// inboxCentralNames are the names with "inbox" in them that internal/assistant/service.go may still use, each with
// how many times and why. Shrink-only: never add a name or raise a count; a lower count passes with a note, and
// lowering it is tidying anyone can do (docs/operations/testing.md, "Count ratchets"). bus.go and handler.go have
// none.
var inboxCentralNames = map[string]struct {
	n   int
	why string
}{
	"inbox":                  {2, "the Service's inbox field, and s.inbox.lang while open PRs edit the line above it (the plan's S4)"},
	"inboxBook":              {1, "the inbox field's type"},
	"inboxExt":               {1, "the field of what the extensions wrote for the Inbox (inbox_extension_context.go)"},
	"inboxExtensionCache":    {1, "its type"},
	"newInboxModule":         {1, "New registers the Inbox module through the kernel, where its routes were added"},
	"putInboxPermissionMode": {1, "Service.call's permission mode for an Inbox continuation's send"},
	"OriginInbox":            {1, "the conversation views: a conversation the Inbox started is archived unless it is the main one"},
}

const inboxCentralHint = "register the Inbox's entries through its module instead (internal/assistant/inbox_module.go: " +
	"Kernel.Route through routesThrough, Kernel.Bus, Kernel.TurnSettled, Kernel.Connected, Kernel.Snapshot; " +
	"docs/server/architecture.md \"Validated and sealed\")"

// TestCentralFilesNameNoInboxCode fails when bus.go or handler.go names an identifier with "inbox" in it (any case),
// or service.go uses one inboxCentralNames does not allow, or more often than it allows. Comments do not count.
func TestCentralFilesNameNoInboxCode(t *testing.T) {
	fset := token.NewFileSet()
	files := assistantFiles(t, fset)
	for _, name := range []string{"bus.go", "handler.go", "service.go"} {
		f := files[name]
		if f == nil {
			t.Fatalf("%s/%s is gone: move this guard with its lines", assistantDir, name)
		}
		seen := map[string][]ast.Node{}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "inbox") {
				seen[id.Name] = append(seen[id.Name], id)
			}
			return true
		})
		for _, id := range slices.Sorted(maps.Keys(seen)) {
			allowed, ok := inboxCentralNames[id]
			if name != "service.go" {
				ok = false
			}
			switch n := len(seen[id]); {
			case !ok:
				for _, at := range seen[id] {
					t.Errorf("%s: %s names the Inbox's %s: %s", pos(fset, at), name, id, inboxCentralHint)
				}
			case n > allowed.n:
				t.Errorf("%s: service.go uses %s %d times, inboxCentralNames allows %d (%s): %s",
					pos(fset, seen[id][n-1]), id, n, allowed.n, allowed.why, inboxCentralHint)
			case n < allowed.n:
				t.Logf("inboxCentralNames allows service.go %s %d times, and it has %d now: lowering the count "+
					"(dropping the entry at zero) is tidying anyone can do", id, allowed.n, n)
			}
		}
		if name == "service.go" {
			for _, id := range slices.Sorted(maps.Keys(inboxCentralNames)) {
				if len(seen[id]) == 0 {
					t.Logf("inboxCentralNames allows service.go %s, which it no longer uses: drop the entry", id)
				}
			}
		}
	}
}
