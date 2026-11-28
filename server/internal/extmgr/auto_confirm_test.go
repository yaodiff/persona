package extmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// extAutoConfirmConnector is a test connector implementing ApprovalActions and IdentifiedConnector.
type extAutoConfirmConnector struct {
	id    string
	ident connector.ExtensionIdentity
	token string
}

func (c *extAutoConfirmConnector) ID() string   { return c.id }
func (c *extAutoConfirmConnector) Name() string { return "Example" }
func (c *extAutoConfirmConnector) Manifest() connector.Manifest {
	return connector.Manifest{
		Capabilities:  []connector.Capability{connector.CapStatus, connector.CapActions},
		AgentTools:    []string{c.id + "_submit"},
		ConfirmAlways: []string{c.id + "_submit"},
		Proxy:         true,
	}
}
func (c *extAutoConfirmConnector) Status(context.Context) connector.Status {
	return connector.Status{State: connector.StateOK}
}
func (c *extAutoConfirmConnector) ExtensionIdentity() (connector.ExtensionIdentity, bool) {
	return c.ident, true
}
func (c *extAutoConfirmConnector) Actions() []proto.Tool {
	return []proto.Tool{{Name: c.id + "_submit"}}
}
func (c *extAutoConfirmConnector) Describe(ctx context.Context, name string, args json.RawMessage) (connector.Action, string, error) {
	act, sum, _, err := c.DescribeWithApproval(ctx, name, args, "")
	return act, sum, err
}
func (c *extAutoConfirmConnector) DescribeWithApproval(_ context.Context, name string, args json.RawMessage, protocol string) (connector.Action, string, *connector.AutoConfirmGrant, error) {
	var a struct {
		Amount float64 `json:"amount"`
		Text   string  `json:"text"`
	}
	_ = json.Unmarshal(args, &a)
	var grant *connector.AutoConfirmGrant
	if protocol == connector.ApprovalProtocolGuardedAutoConfirmV1 && a.Amount > 0 && a.Amount <= 100 {
		grant = &connector.AutoConfirmGrant{
			Token:     c.token,
			ExpiresAt: time.Now().UTC().Add(45 * time.Second),
		}
	}
	return connector.Action{Name: name, Visible: "example service"}, "Submit " + a.Text, grant, nil
}
func (c *extAutoConfirmConnector) Do(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	return "manual-ok", nil
}
func (c *extAutoConfirmConnector) DoWithApproval(_ context.Context, _ string, _ json.RawMessage, approval connector.AutoConfirmApproval) (connector.DoResult, error) {
	if approval.Mode == connector.ApprovalModeAutoConfirm && approval.Token == c.token {
		return connector.DoResult{Text: "auto-ok", MessageRef: "ext-ref-1"}, nil
	}
	return connector.DoResult{Text: "manual-ok", MessageRef: "ext-ref-manual"}, nil
}

func TestLoadConfigValidatesConnectorAutoConfirm(t *testing.T) {
	dir := t.TempDir()
	mgrFile := filepath.Join(dir, "manager.json")

	// Valid policy loads cleanly.
	validJSON := `{
		"connector_auto_confirm": [{
			"name": "small-tier",
			"extension": "example-ext",
			"connector": "example",
			"actions": ["example_submit"],
			"min_version": "1.0.0",
			"max_ttl_seconds": 60
		}]
	}`
	if err := os.WriteFile(mgrFile, []byte(validJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir, mgrFile)
	if err != nil {
		t.Fatalf("valid connector_auto_confirm failed: %v", err)
	}
	if len(cfg.ConnectorAutoConfirm) != 1 || cfg.ConnectorAutoConfirm[0].Name != "small-tier" {
		t.Fatalf("loaded policies: %+v", cfg.ConnectorAutoConfirm)
	}

	// Wildcard action fails LoadConfig closed.
	badJSON := `{
		"connector_auto_confirm": [{
			"extension": "example-ext",
			"connector": "example",
			"actions": ["example_*"]
		}]
	}`
	if err := os.WriteFile(mgrFile, []byte(badJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(dir, mgrFile); err == nil || !strings.Contains(err.Error(), "connector_auto_confirm") {
		t.Fatalf("expected connector_auto_confirm validation error, got %v", err)
	}
}

func TestSentLedgerRecordsGuardedAutoConfirmWithoutTokenAndEnforcesCallerBoundaries(t *testing.T) {
	const secretToken = "super_secret_one_time_approval_token_98765"
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	h.cfg.ConnectorAutoConfirm = []connector.AutoConfirmPolicy{{
		Name:          "small-tier",
		Extension:     "example-ext",
		Connector:     "example",
		Actions:       []string{"example_submit"},
		MaxTTLSeconds: 90,
	}}
	h.connectors = h.newConnectors(nil)
	conn := &extAutoConfirmConnector{
		id: "example",
		ident: connector.ExtensionIdentity{
			Extension: "example-ext",
			Version:   "2.1.0",
			Source:    "installed",
		},
		token: secretToken,
	}
	if err := h.addConnector(conn); err != nil {
		t.Fatal(err)
	}
	yes := true
	hubOf(h).answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &yes} }

	// 1. Eligible owner assistant call: 0 cards, ledger records auto_confirm with non-secret attribution and NO token.
	ownerCtx := connector.WithOwnerCaller(context.Background())
	res, ok := h.CallTool(ownerCtx, "box", "conv-owner", "example_submit", json.RawMessage(`{"amount":20,"text":"small op"}`), true)
	if !ok || res.IsError || res.Content[0].Text != "auto-ok" {
		t.Fatalf("eligible owner call: ok=%v res=%+v", ok, res)
	}
	if cards := hubOf(h).asked(); len(cards) != 0 {
		t.Fatalf("expected 0 cards for guarded auto-confirm, got %d", len(cards))
	}
	rows := st.all()
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	row := rows[0]
	through := objectOf(t, row.LetThrough)
	if through["kind"] != connector.ThroughAutoConfirm ||
		through["policy"] != "small-tier" ||
		through["extension"] != "example-ext" ||
		through["version"] != "2.1.0" ||
		through["source"] != "installed" ||
		through["protocol"] != connector.ApprovalProtocolGuardedAutoConfirmV1 ||
		through["owner_authorized"] != true ||
		through["interaction_id"] != nil {
		t.Fatalf("unexpected auto_confirm let_through: %s", row.LetThrough)
	}
	for label, field := range map[string]string{
		"let_through": string(row.LetThrough),
		"origin":      string(row.Origin),
		"target":      string(row.Target),
		"summary":     row.Summary,
		"text":        row.Text,
		"message_ref": row.MessageRef,
	} {
		if strings.Contains(field, secretToken) {
			t.Fatalf("approval token leaked into ledger %s: %s", label, field)
		}
	}

	// 2. High-tier call (extension returns no grant) falls back to phone card and records kind=card + interaction_id.
	res, ok = h.CallTool(ownerCtx, "box", "conv-owner", "example_submit", json.RawMessage(`{"amount":500,"text":"large op"}`), true)
	if !ok || res.IsError || res.Content[0].Text != "manual-ok" {
		t.Fatalf("large tier call: ok=%v res=%+v", ok, res)
	}
	if cards := hubOf(h).asked(); len(cards) != 1 {
		t.Fatalf("expected 1 card for large tier, got %d", len(cards))
	}
	rows = st.all()
	if len(rows) != 2 || objectOf(t, rows[1].LetThrough)["kind"] != connector.ThroughCard || objectOf(t, rows[1].LetThrough)["interaction_id"] == nil {
		t.Fatalf("large tier must record real card approval: %+v", rows[1])
	}

	// 3. Coding Agent call (assistant == false) strips OwnerCaller and must show a phone card even for small amount.
	res, ok = h.CallTool(ownerCtx, "box", "conv-agent", "example_submit", json.RawMessage(`{"amount":10,"text":"from agent"}`), false)
	if !ok || res.IsError || res.Content[0].Text != "manual-ok" {
		t.Fatalf("agent call: ok=%v res=%+v", ok, res)
	}
	if cards := hubOf(h).asked(); len(cards) != 2 {
		t.Fatalf("expected card for coding agent call, got %d total cards", len(cards))
	}

	// 4. Unattributed call (empty asker, e.g. pairing token reply) strips OwnerCaller and must show a phone card.
	res = h.callConnector(ownerCtx, asker{}, "example_submit", json.RawMessage(`{"amount":10,"text":"unattributed"}`))
	if res.IsError || res.Content[0].Text != "manual-ok" {
		t.Fatalf("unattributed call: %+v", res)
	}
	if cards := hubOf(h).asked(); len(cards) != 3 {
		t.Fatalf("expected card for unattributed call, got %d total cards", len(cards))
	}

	// 5. ConfirmEach (space/health conversation) always shows a phone card even for owner assistant + small amount.
	res, ok = h.CallTool(connector.ConfirmEach(ownerCtx), "box", "conv-space", "example_submit", json.RawMessage(`{"amount":10,"text":"space"}`), true)
	if !ok || res.IsError || res.Content[0].Text != "manual-ok" {
		t.Fatalf("ConfirmEach call: ok=%v res=%+v", ok, res)
	}
	if cards := hubOf(h).asked(); len(cards) != 4 {
		t.Fatalf("expected card for ConfirmEach call, got %d total cards", len(cards))
	}
}
