package assistant

import (
	"testing"

	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// needsLarkModule skips a test that drives the Feishu / Lark bot's real client in a build without the lark module
// (persona_without_lark, persona_core_only): the client is the module's (internal/botport).
func needsLarkModule(t testing.TB) {
	t.Helper()
	if !botport.Available(moduleswitch.Set{}) {
		t.Skip("the lark module is not in this build")
	}
}

// needsLarkLayer skips a test of what the assistant shows in the Feishu chat (its cards, notes and thread lines) in a
// build without the lark module: the outbound channel port is then larkhost.None, a bot switched off.
func needsLarkLayer(t testing.TB) {
	t.Helper()
	if !larkhost.InBuild(moduleswitch.Set{}) {
		t.Skip("the lark module is not in this build")
	}
}

// needsLarkCLI skips a test that runs lark-cli (the knowledge base's Lark import) in a build without the lark module:
// the adapter is the module's (internal/cliport).
func needsLarkCLI(t testing.TB) {
	t.Helper()
	if !cliport.Available(moduleswitch.Set{}) {
		t.Skip("the lark module is not in this build")
	}
}
