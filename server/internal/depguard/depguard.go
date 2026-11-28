// Package depguard checks what a program links (go list -deps), for the tests that keep the hub's and the manager's
// programs apart (cmd/persona-hub, cmd/persona-manager; docs/server/server.md "Programs").
package depguard

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Module is the server's module path.
const Module = "github.com/nwverse/persona/server/"

// Platforms are the ones the guards check: the four the programs are released for, and Windows, where the server
// builds too (CI's Windows build).
var Platforms = []string{"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64", "windows/amd64"}

// HubOwn are the hub's own packages: persona-manager links none of them. A contract between the hub and a helper of
// its own (relayproto with the push relay, shareproof with the sharing ingress, passwordpake with the web app's
// password WASM) is the hub's too, as is a push to a browser (webpush, the hub's Web Push).
var HubOwn = []string{"cmd/persona-hub", "internal/hub", "internal/hubcli", "internal/hubedge", "internal/apns",
	"internal/lookmedia", "internal/mdns", "internal/operations/recovery", "internal/passwordpake", "internal/proxyproto", "internal/pushcrypto", "internal/relay",
	"internal/relayproto", "internal/shareproof", "internal/webasset", "internal/webpush"}

// Shared are the packages both programs link (the wire types, the database, the configuration, identity, the update
// code and its proxy, netproxy): the hub links these and its own, nothing else; a new package the hub needs is put in
// one of the two lists on purpose, never one of the manager's.
// sessionsearch is a pure projection of immutable runtime DTOs for sessionlist;
// it owns no store, route, process, model call or transcript reader. tokenuse is the parsers of token usage that
// transcript uses: pure functions of a record's lines. sessionhistory is the window, order, cursor and merge of
// sessions.history pages (docs/protocol/protocol.md "Session history"): pure functions over wire rows, no store or route.
// modelretry is the classification of a model failure's text (temporary, permanent, an upstream 5xx, an exhausted
// account) and the retry delays: pure functions, no store, route, process or model call. transcript uses the
// classification to mark a turn's upstream errors (#13465).
// conversationlog bounds normalized item DTOs and merges source-position ranges;
// it owns no store, route, process or model call. The shared database uses these types. larksyncwire is the Lark
// sync's types and dedup rules (Decide, MergeSources): pure values, no store, route or network call; the shared
// database's sync tables use them. modapi is the stdlib-only module contract shared by prompts, hub and manager
// without pulling in any manager business package. wireids holds generated stdlib-free contract wire ID types shared
// by assistantwire and other wire packages. buswire (the bus methods' names bound to their request and reply types) and
// recordingwire (the recording's data types) are contract packages too: the hub's handlers and assistantwire use them.
// The hub's backup quiesce and path validation use the same shared backup infrastructure as the manager; it pulls in
// no manager business package.
var Shared = []string{"internal/acceptloop", "internal/agentviewwire", "internal/aiwire", "internal/apierr", "internal/approval", "internal/assistantwire",
	"internal/bus", "internal/buswire", "internal/channel", "internal/clitext", "internal/compat", "internal/connectorwire", "internal/conversationlog", "internal/db",
	"internal/devicewire", "internal/dialogwire", "internal/errreport", "internal/extwire", "internal/i18n",
	"internal/identity", "internal/kbwire", "internal/larksyncwire", "internal/lazyre", "internal/link", "internal/linkwire", "internal/mailwire", "internal/managerconf", "internal/memtrim", "internal/modapi", "internal/modelretry", "internal/netaddr",
	"internal/netproxy", "internal/nodeproto", "internal/operations/backup", "internal/osproc", "internal/pgdocker", "internal/preview", "internal/procinfo", "internal/procs", "internal/prompts",
	"internal/proto", "internal/recordingwire", "internal/rpc", "internal/screen", "internal/secretkey", "internal/sessionhistory", "internal/sessionlist", "internal/sessionorigin", "internal/sessionsearch", "internal/streams",
	"internal/textfmt", "internal/tokenuse", "internal/transcript", "internal/update", "internal/wireids", "internal/workspaceboard"}

// HubExceptions are manager-side packages the hub still links, and may only shrink: internal/extension (and what it
// pulls in) for the looks the hub serves and the database's extension types, until extensions are a part of their own.
var HubExceptions = []string{"internal/extension", "internal/extension/egress", "internal/extension/sandbox",
	"internal/extension/sourcevalidate"}

// Deps are the module's packages the package in dir links on goos/goarch, relative to the module
// ("internal/hub"). It skips the test without a go tool.
func Deps(t *testing.T, dir, goos, goarch string) []string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps (%s/%s): %v", goos, goarch, err)
	}
	var deps []string
	for _, dep := range strings.Fields(string(out)) {
		if rel, ok := strings.CutPrefix(dep, Module); ok {
			deps = append(deps, rel)
		}
	}
	return deps
}

// Under: pkg is p or a package under it.
func Under(pkg, p string) bool { return pkg == p || strings.HasPrefix(pkg, p+"/") }
