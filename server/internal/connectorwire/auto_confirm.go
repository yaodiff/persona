package connectorwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Guarded conditional auto-confirm protocol constants (docs/protocol/connector-protocol.md "Guarded conditional auto-confirm").
// All fields are generic and optional across every connector extension; no extension or action is hard-coded here.
const (
	// ApprovalProtocolGuardedAutoConfirmV1 is the generic negotiation identifier the hub sends on actions.describe when a
	// hub-side guarded auto-confirm policy matches the calling context, extension installation and action.
	ApprovalProtocolGuardedAutoConfirmV1 = "guarded-auto-confirm-v1"

	// ApprovalModeAutoConfirm is sent alongside approval_token on actions.do only when the hub gate let the write through
	// under a matching guarded auto-confirm policy and a valid short-lived credential from actions.describe.
	ApprovalModeAutoConfirm = "auto_confirm"

	// AutoConfirmTokenMaxLen caps the length of an opaque approval token in bytes.
	AutoConfirmTokenMaxLen = 512

	// AutoConfirmDefaultMaxTTLSeconds is the hub's default upper bound on a returned auto-confirm credential's lifetime.
	AutoConfirmDefaultMaxTTLSeconds = 120

	// AutoConfirmHardMaxTTLSeconds is the maximum policy-configurable credential lifetime in seconds.
	AutoConfirmHardMaxTTLSeconds = 300
)

// AutoConfirmGrant is the short-lived, single-use credential an extension may return from actions.describe after its
// own guard checks pass. The hub never trusts it without a matching hub-side AutoConfirmPolicy and never persists Token.
type AutoConfirmGrant struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AutoConfirmApproval carries the hub gate's guarded auto-confirm authorization from the gate to actions.do.
// It is kept in memory for the single call and never written to the sent-as-user ledger or logs.
type AutoConfirmApproval struct {
	Mode      string
	Token     string
	ExpiresAt time.Time
}

// AutoConfirmPolicy is one hub-side opt-in policy in manager.json (connector_auto_confirm) allowing a specific trusted
// extension installation and exact action list to participate in guarded conditional auto-confirm.
// All fields are exact-match (no wildcards); guarded auto-confirm is off by default when no policy matches.
type AutoConfirmPolicy struct {
	// Name labels the policy in the sent-as-user ledger (defaults to "<extension>/<connector>" when omitted).
	Name string `json:"name,omitempty"`
	// Extension is the exact installed extension id (from hub-verified extension registration, never self-reported).
	Extension string `json:"extension"`
	// Connector is the exact connector id contributed by Extension.
	Connector string `json:"connector"`
	// Actions lists the exact action tool names (each starting with "<connector>_", no wildcards) eligible under this policy.
	Actions []string `json:"actions"`
	// Protocol names the guarded auto-confirm protocol to negotiate (defaults to ApprovalProtocolGuardedAutoConfirmV1).
	Protocol string `json:"protocol,omitempty"`
	// MinVersion and MaxVersion optionally pin an inclusive semver range (X.Y.Z) for the extension version.
	MinVersion string `json:"min_version,omitempty"`
	MaxVersion string `json:"max_version,omitempty"`
	// Sources limits which hub-verified extension installation sources are trusted (defaults to ["installed", "builtin"]).
	Sources []string `json:"sources,omitempty"`
	// MarketKey optionally pins the extension's verified marketplace key (extension.Record.MarketKey).
	MarketKey string `json:"market_key,omitempty"`
	// MaxTTLSeconds caps the accepted lifetime of auto_confirm.expires_at (1..300; 0 uses AutoConfirmDefaultMaxTTLSeconds).
	MaxTTLSeconds int `json:"max_ttl_seconds,omitempty"`
	// Disabled turns the policy off without removing it from configuration.
	Disabled bool `json:"disabled,omitempty"`
}

// ExtensionIdentity is the hub-verified installation provenance attached to a connector proxy by the extension manager.
// It is never read from extension self-reports such as initialize.
type ExtensionIdentity struct {
	Extension string
	Version   string
	Source    string
	MarketKey string
	// Remote marks an unpackaged remote-socket extension (not a verified installed package).
	Remote bool
}

// AutoConfirmAudit records the non-secret attribution of a guarded auto-confirm let-through in LetThrough and the
// sent-as-user ledger. It intentionally excludes the sensitive approval token.
type AutoConfirmAudit struct {
	Policy          string
	Extension       string
	Version         string
	Source          string
	Protocol        string
	OwnerAuthorized bool
}

// ApprovalActions is an optional extension of Actions for connectors that support guarded conditional auto-confirm
// negotiation on Describe and approval-token forwarding on Do. Found by type assertion, like RefActions.
type ApprovalActions interface {
	Actions
	DescribeWithApproval(ctx context.Context, name string, args json.RawMessage, approvalProtocol string) (Action, string, *AutoConfirmGrant, error)
	DoWithApproval(ctx context.Context, name string, args json.RawMessage, approval AutoConfirmApproval) (DoResult, error)
}

// IdentifiedConnector is an optional interface implemented by hub-managed extension connector proxies to expose their
// hub-verified ExtensionIdentity to the write gate.
type IdentifiedConnector interface {
	ExtensionIdentity() (ExtensionIdentity, bool)
}

// WriteWithApproval runs the write name of a once the gate let it through. When approval is empty it behaves identically
// to Write; when approval carries a guarded auto-confirm authorization it requires ApprovalActions and refuses unpaired
// or unsupported approval fields rather than silently downgrading to an unapproved Write.
func WriteWithApproval(ctx context.Context, a Actions, name string, args json.RawMessage, approval AutoConfirmApproval) (DoResult, error) {
	if approval.Mode == "" && approval.Token == "" {
		return Write(ctx, a, name, args)
	}
	if approval.Mode == "" || approval.Token == "" {
		return DoResult{}, errors.New("actions.do requires approval_mode and approval_token together")
	}
	if approval.Mode != ApprovalModeAutoConfirm {
		return DoResult{}, fmt.Errorf("actions.do: unsupported approval_mode %q", approval.Mode)
	}
	aa, ok := a.(ApprovalActions)
	if !ok {
		return DoResult{}, errors.New("connector does not support guarded auto-confirm approval")
	}
	return aa.DoWithApproval(ctx, name, args, approval)
}
