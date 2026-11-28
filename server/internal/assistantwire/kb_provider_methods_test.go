package assistantwire_test

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestKBProviderMethodsWire(t *testing.T) {
	checkMethodWire[assistantwire.KBProviderRequest, wire.KBImportCheckpointResult](t,
		assistantwire.KBProviderCheckpointMethod, "kb.provider_import_checkpoint",
		`{"extension":"example","storage_id":"old-install","installed_at":"2026-01-02T00:00:00Z","params":{"day":"","approval":""}}`,
		`{"state":"running","approval":"plan","read":3,"written":2}`)
	checkMethodWire[assistantwire.KBProviderRequest, wire.KBSentMarkersResult](t,
		assistantwire.KBProviderSentMarkersMethod, "kb.provider_sent_markers",
		`{"extension":"example","storage_id":"old-install","installed_at":"2026-01-02T00:00:00Z","params":{"day":"2026-01-02","approval":"plan","after":2}}`,
		`{"markers":[{"n":4,"sent_by":"assistant","recalled_at":"2026-01-02T00:01:00Z"}],"next_after":4}`)
	checkJSONRoundTrip[wire.KBSentMarkersResult](t, `{"markers":[]}`)
}
