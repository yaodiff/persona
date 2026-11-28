//go:build persona_core_only || persona_without_github

package extmgr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/proc"
	"github.com/nwverse/persona/server/internal/githubwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// withoutModuleSnapshotConnector has the installed connector's capabilities, not the built-in GitHub interface.
type withoutModuleSnapshotConnector struct {
	host  string
	state string
	tools []proto.Tool
	text  string
	err   error
	calls int
	args  []string
}

func (*withoutModuleSnapshotConnector) ID() string   { return "github" }
func (*withoutModuleSnapshotConnector) Name() string { return "GitHub" }
func (c *withoutModuleSnapshotConnector) Manifest() connector.Manifest {
	host := c.host
	if host == "" {
		host = "github.com"
	}
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools}, Hosts: []string{host}}
}
func (c *withoutModuleSnapshotConnector) Status(context.Context) connector.Status {
	return connector.Status{State: cmp.Or(c.state, connector.StateOK)}
}
func (c *withoutModuleSnapshotConnector) Tools() []proto.Tool { return c.tools }
func (c *withoutModuleSnapshotConnector) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	c.calls++
	c.args = append(c.args, name+" "+string(args))
	return c.text, c.err
}

func withoutModuleSnapshotManager(t *testing.T, c connector.Connector) *Manager {
	t.Helper()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	return h
}

func newWithoutModuleSnapshotConnector() *withoutModuleSnapshotConnector {
	return &withoutModuleSnapshotConnector{tools: []proto.Tool{{Name: githubSnapshotTool}}, text: validSnapshot}
}

func TestPRChecksWithoutGitHubModuleConsumesInstalledSnapshot(t *testing.T) {
	c := newWithoutModuleSnapshotConnector()
	if _, builtin := any(c).(githubwire.Connector); builtin {
		t.Fatal("the installed-style fixture accidentally implements the built-in interface")
	}
	h := withoutModuleSnapshotManager(t, c)
	got, reset, available, err := h.PRChecks(t.Context(), "example/repo", 12)
	if err != nil || !available || !reset.IsZero() {
		t.Fatalf("PRChecks: available=%v reset=%v err=%v", available, reset, err)
	}
	var want githubwire.PRCheckSnapshot
	want.Head = "0123456789abcdef0123456789abcdef01234567"
	want.AddRun(1, "lint", "completed", "failure")
	want.AddStatus("preview", "pending")
	want.Finish()
	if got.Head != want.Head || got.Fingerprint != want.Fingerprint || got.Pending != want.Pending || got.Count != want.Count || !slices.Equal(got.Failures, want.Failures) {
		t.Fatalf("snapshot got %+v, want %+v", got, want)
	}
	if c.calls != 1 || len(c.args) != 1 || c.args[0] != `github_ci_snapshot_v1 {"number":12,"repo":"example/repo"}` {
		t.Fatalf("installed tool calls: %d %v", c.calls, c.args)
	}
}

func TestPRChecksWithoutGitHubModuleNoConnectorIsUnavailable(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	got, reset, available, err := h.PRChecks(t.Context(), "example/repo", 12)
	if err != nil || available || !reset.IsZero() || got.Count != 0 || got.Fingerprint != "" {
		t.Fatalf("no connector: snapshot=%+v available=%v reset=%v err=%v", got, available, reset, err)
	}
}

func TestPRChecksWithoutGitHubModuleUnavailableStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		host  string
		tools []proto.Tool
	}{
		{name: "missing versioned tool", tools: []proto.Tool{{Name: "github_checks"}}},
		{name: "signed out", state: connector.StateNotSignedIn, tools: []proto.Tool{{Name: githubSnapshotTool}}},
		{name: "permission denied", state: connector.StateNoPermission, tools: []proto.Tool{{Name: githubSnapshotTool}}},
		{name: "unavailable", state: connector.StateError, tools: []proto.Tool{{Name: githubSnapshotTool}}},
		{name: "cross-host registration", host: "github.example.com", tools: []proto.Tool{{Name: githubSnapshotTool}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &withoutModuleSnapshotConnector{state: tc.state, host: tc.host, tools: tc.tools, text: validSnapshot}
			h := withoutModuleSnapshotManager(t, c)
			if got, reset, available, err := h.PRChecks(t.Context(), "example/repo", 12); available || err != nil || !reset.IsZero() || got.Count != 0 || c.calls != 0 {
				t.Fatalf("snapshot=%+v available=%v reset=%v err=%v calls=%d", got, available, reset, err, c.calls)
			}
		})
	}

	t.Run("disabled and re-enabled", func(t *testing.T) {
		c := newWithoutModuleSnapshotConnector()
		h := withoutModuleSnapshotManager(t, c)
		if err := h.connectors.SetEnabled("github", false); err != nil {
			t.Fatal(err)
		}
		if _, _, available, err := h.PRChecks(t.Context(), "example/repo", 12); available || err != nil || c.calls != 0 {
			t.Fatalf("disabled: available=%v err=%v calls=%d", available, err, c.calls)
		}
		if err := h.connectors.SetEnabled("github", true); err != nil {
			t.Fatal(err)
		}
		if _, _, available, err := h.PRChecks(t.Context(), "example/repo", 12); !available || err != nil || c.calls != 1 {
			t.Fatalf("re-enabled: available=%v err=%v calls=%d", available, err, c.calls)
		}
	})
}

func TestPRChecksWithoutGitHubModuleRejectsUnsafeSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "malformed JSON", text: `{"version":1`},
		{name: "human prose", text: "CI failed: lint"},
		{name: "incomplete page", text: strings.Replace(validSnapshot, `"complete":true`, `"complete":false`, 1)},
		{name: "cross-host answer", text: strings.Replace(validSnapshot, `"host":"github.com"`, `"host":"github.example.com"`, 1)},
		{name: "another repository", text: strings.Replace(validSnapshot, `"repo":"example/repo"`, `"repo":"example/other"`, 1)},
		{name: "malformed head", text: strings.ReplaceAll(validSnapshot, "0123456789abcdef0123456789abcdef01234567", "not-a-head")},
		{name: "head changed during read", text: strings.Replace(validSnapshot, `"head_after":"0123456789abcdef0123456789abcdef01234567"`, `"head_after":"fedcba9876543210fedcba9876543210fedcba98"`, 1)},
		{name: "trailing value", text: validSnapshot + ` {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newWithoutModuleSnapshotConnector()
			c.text = tc.text
			h := withoutModuleSnapshotManager(t, c)
			if got, reset, available, err := h.PRChecks(t.Context(), "example/repo", 12); available || err != nil || !reset.IsZero() || got.Count != 0 || c.calls != 1 {
				t.Fatalf("snapshot=%+v available=%v reset=%v err=%v calls=%d", got, available, reset, err, c.calls)
			}
		})
	}
}

func TestPRChecksWithoutGitHubModuleRateLimits(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		c := newWithoutModuleSnapshotConnector()
		c.state = connector.StateRateLimited
		h := withoutModuleSnapshotManager(t, c)
		before := time.Now()
		got, reset, available, err := h.PRChecks(t.Context(), "example/repo", 12)
		if err != nil || !available || got.Count != 0 || c.calls != 0 || reset.Before(before.Add(59*time.Minute)) || reset.After(time.Now().Add(61*time.Minute)) {
			t.Fatalf("snapshot=%+v available=%v reset=%v err=%v calls=%d", got, available, reset, err, c.calls)
		}
	})

	for _, tc := range []struct {
		name   string
		err    error
		lowest time.Duration
	}{
		{name: "structured tool error", err: &rpc.Error{Code: proto.ToolErrorRateLimited, Message: "rate limited", Data: json.RawMessage(`{"retry_after":180}`)}, lowest: 2 * time.Minute},
		{name: "429 text", err: errors.New("GitHub API returned 429 rate limit"), lowest: 59 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newWithoutModuleSnapshotConnector()
			c.err = tc.err
			h := withoutModuleSnapshotManager(t, c)
			before := time.Now()
			got, reset, available, err := h.PRChecks(t.Context(), "example/repo", 12)
			if err != nil || !available || got.Count != 0 || c.calls != 1 || reset.Before(before.Add(tc.lowest)) {
				t.Fatalf("snapshot=%+v available=%v reset=%v err=%v calls=%d", got, available, reset, err, c.calls)
			}
		})
	}
}

// withoutModuleLegacyGitHub stands in for the hub-owned connector retained while an installed connector holds its id.
type withoutModuleLegacyGitHub struct{}

func (*withoutModuleLegacyGitHub) ID() string   { return "github" }
func (*withoutModuleLegacyGitHub) Name() string { return "GitHub" }
func (*withoutModuleLegacyGitHub) Manifest() connector.Manifest {
	return connector.Manifest{Hosts: []string{"github.com"}}
}

func withoutModuleInstalledConnector() *proc.Conn {
	return proc.New(proc.Config{ID: "github", Name: "GitHub", Manifest: connector.Manifest{
		Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools}, Hosts: []string{"github.com"},
	}})
}

// The takeover and handback paths are also used when an upgrade rolls back. Neither may forget the durable GitHub
// authorization choice or the connector switch, whether the installed connector is present, off, or removed again.
func TestGitHubCutoverRollbackWithoutModuleKeepsAuthorizationAndEnablement(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.store = newKeepStore()
	h.connectors = connector.NewRegistry(nil)
	choice := githubAuth{Source: githubwire.SourceMachine, Machine: "builder"}
	if err := h.store.PutSetting(ctx, githubAuthKey, choice); err != nil {
		t.Fatal(err)
	}
	if err := h.store.PutSetting(ctx, connectorSettingsKey, connectorSettings{Switch: map[string]bool{"github": false}}); err != nil {
		t.Fatal(err)
	}
	legacy := &withoutModuleLegacyGitHub{}
	if err := h.addConnector(legacy); err != nil {
		t.Fatal(err)
	}
	assertKept := func(stage string, installed bool, enabled bool) {
		t.Helper()
		var gotChoice githubAuth
		ok, err := h.store.Setting(ctx, githubAuthKey, &gotChoice)
		if err != nil || !ok || gotChoice != choice {
			t.Fatalf("%s: authorization=%+v ok=%v err=%v", stage, gotChoice, ok, err)
		}
		registered := h.connectors.Get("github")
		if installed {
			if _, ok := registered.(*proc.Conn); !ok {
				t.Fatalf("%s: installed connector is %T", stage, registered)
			}
		} else if registered != legacy {
			t.Fatalf("%s: hub-owned connector was not restored", stage)
		}
		if got := h.connectors.Enabled("github"); got != enabled {
			t.Fatalf("%s: enabled=%v, want %v", stage, got, enabled)
		}
	}

	if err := h.addExtensionConnector(ctx, withoutModuleInstalledConnector(), false); err != nil {
		t.Fatal(err)
	}
	assertKept("takeover with the saved switch off", true, false)
	h.removeExtensionConnector("github")
	assertKept("rollback with the saved switch off", false, false)

	if err := h.setConnectorEnabled(ctx, "github", true); err != nil {
		t.Fatal(err)
	}
	if err := h.addExtensionConnector(ctx, withoutModuleInstalledConnector(), false); err != nil {
		t.Fatal(err)
	}
	assertKept("second takeover after enablement", true, true)
	h.removeExtensionConnector("github")
	assertKept("second rollback after enablement", false, true)
}
