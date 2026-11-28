package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/githubwire"
)

// extensionGitHub is an extension's connector that took the "github" id over (connector/proc in production): its
// account comes only through its status.
type extensionGitHub struct{ status connector.Status }

func (extensionGitHub) ID() string   { return "github" }
func (extensionGitHub) Name() string { return "GitHub" }
func (extensionGitHub) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus}}
}

func TestHubCallKeepsNestedReplyBytes(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(extensionGitHub{connector.Status{State: connector.StateOK, Account: "example<&>"}}); err != nil {
		t.Fatal(err)
	}

	params := json.RawMessage(`{"method":"github_login","future_field":true}`)
	out, handled, err := h.Request(t.Context(), extwire.MethodHubCall, params)
	if err != nil || !handled {
		t.Fatalf("handled = %t, error = %v", handled, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // The RPC peer uses the same setting.
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "{\"known\":true,\"login\":\"example<&>\"}\n"; got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}
func (c extensionGitHub) Status(context.Context) connector.Status { return c.status }

// The persona tiers read the GitHub account from whichever connector holds the id: an extension's counts as the hub's
// own does, through its status, instead of reading as "known, not signed in" (the lowest tier).
func TestGitHubLoginFromTheConnectorHoldingTheID(t *testing.T) {
	for _, c := range []struct {
		name  string
		conn  connector.Connector
		login string
		known bool
	}{
		{"no GitHub connector", nil, "", true},
		{"the extension's, signed in", extensionGitHub{connector.Status{State: connector.StateOK, Account: " octocat "}}, "octocat", true},
		{"the extension's, signed out", extensionGitHub{connector.Status{State: connector.StateNotSignedIn}}, "", true},
		{"the extension's, not said yet", extensionGitHub{}, "", false},
		{"the hub's own", signedInGitHub{newTestGitHub(t, githubwire.Config{}, "Mac", nil).(fullGitHub)}, "", true},
	} {
		h := newTestManager(t)
		h.connectors = connector.NewRegistry(nil)
		if c.conn != nil {
			if err := h.addConnector(c.conn); err != nil {
				t.Fatal(err)
			}
		}
		if login, known := h.githubLogin(context.Background()); login != c.login || known != c.known {
			t.Errorf("%s: (%q, %v), want (%q, %v)", c.name, login, known, c.login, c.known)
		}
	}
}
