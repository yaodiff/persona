package node

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/moduleswitch"
	"github.com/nwverse/persona/server/internal/node/imessageport"
	"github.com/nwverse/persona/server/internal/node/iphoneport"
	"github.com/nwverse/persona/server/internal/node/larkgateport"
	"github.com/nwverse/persona/server/internal/node/screenport"
	"github.com/nwverse/persona/server/internal/nodecmd/commandport"
)

// want are the node-side modules this build's tags keep (each one's <id>_test.go adds it).
var want []string

// The iMessage module is provided to the node exactly when the build tags keep it. Run with -tags
// persona_without_imessage or persona_core_only as well (scripts/ci/server.sh).
func TestIMessageFollowsTheBuildTags(t *testing.T) {
	if got, want := imessageport.Serve(moduleswitch.Set{}) != nil, slices.Contains(want, "imessage"); got != want {
		t.Fatalf("imessage provided to the node: %v, want %v for this build's tags", got, want)
	}
}

// personad office is a device command exactly when the build tags keep the Office files module.
func TestOfficeFollowsTheBuildTags(t *testing.T) {
	if _, got := commandport.LookupCommand("office"); got != slices.Contains(want, "office") {
		t.Fatalf("personad office provided: %v, want %v for this build's tags", got, !got)
	}
}

// The node's screen sessions come from the screen-share module exactly when the build tags keep it.
func TestScreenShareFollowsTheBuildTags(t *testing.T) {
	if got := screenport.New() != nil; got != slices.Contains(want, "screen-share") {
		t.Fatalf("screen sessions provided to the node: %v, want %v for this build's tags", got, !got)
	}
}

// The iphone program and `personad iphone` come from the phone-extra module exactly when the build tags keep it.
func TestPhoneExtraFollowsTheBuildTags(t *testing.T) {
	want := slices.Contains(want, "phone-extra")
	if got := iphoneport.Get(moduleswitch.Set{}) != nil; got != want {
		t.Fatalf("the iphone program provided to the node: %v, want %v for this build's tags", got, want)
	}
	if _, got := commandport.LookupCommand("iphone"); got != want {
		t.Fatalf("personad iphone provided: %v, want %v for this build's tags", got, want)
	}
}

// The Lark module's gate rules are provided to the node exactly when the build tags keep it.
func TestLarkFollowsTheBuildTags(t *testing.T) {
	if got, want := larkgateport.Get(moduleswitch.Set{}) != nil, slices.Contains(want, "lark"); got != want {
		t.Fatalf("lark gate rules provided to the node: %v, want %v for this build's tags", got, want)
	}
}
