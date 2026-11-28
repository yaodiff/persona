package assistantwire_test

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The central Routes literal spells the prepare route out (it takes no new name, archguard central_lists_test.go);
// the hub relays it only while that string is still RouteBackendWorkspacePrepare, which the manager and the hub's
// timeout use (#17925).
func TestBackendWorkspacePrepareRouteIsRelayed(t *testing.T) {
	if !slices.Contains(assistantwire.Routes, assistantwire.RouteBackendWorkspacePrepare) {
		t.Fatalf("Routes does not have %q: the hub would not relay the manager's prepare route", assistantwire.RouteBackendWorkspacePrepare)
	}
}
