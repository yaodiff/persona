package assistantwire

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"time"
)

const (
	ExternalBindingsPath = "/api/v1/assistant/external-source-bindings"
	ExternalPoliciesPath = "/api/v1/assistant/external-source-policies"

	ExternalBindingsListRoute    = "GET " + ExternalBindingsPath
	ExternalBindingsCreateRoute  = "POST " + ExternalBindingsPath
	ExternalBindingsUpdateRoute  = "PUT " + ExternalBindingsPath + "/{id}"
	ExternalBindingsDeleteRoute  = "DELETE " + ExternalBindingsPath + "/{id}"
	ExternalBindingsLookupRoute  = "GET " + ExternalBindingsPath + "/lookup"
	ExternalBindingsSummaryRoute = "GET " + ExternalBindingsPath + "/summary"
	ExternalPoliciesReadRoute    = "GET " + ExternalPoliciesPath
	ExternalPoliciesWriteRoute   = "PUT " + ExternalPoliciesPath
	ExternalPolicyEffectiveRoute = "GET " + ExternalPoliciesPath + "/effective"
)

func init() {
	Routes = append(Routes,
		ExternalBindingsListRoute, ExternalBindingsCreateRoute, ExternalBindingsUpdateRoute, ExternalBindingsDeleteRoute,
		ExternalBindingsLookupRoute, ExternalBindingsSummaryRoute,
		ExternalPoliciesReadRoute, ExternalPoliciesWriteRoute, ExternalPolicyEffectiveRoute)
}

// ExternalRef identifies a node in an external source. A channel is the provider's
// default channel when Channel is empty. Container and Item are optional path segments.
type ExternalRef struct {
	Provider  ExternalProvider  `json:"provider"`
	Channel   ExternalChannelID `json:"channel,omitempty"`
	Container ExternalRefNode   `json:"container,omitzero"`
	Item      ExternalRefNode   `json:"item,omitzero"`
}

type ExternalProvider string
type ExternalChannelID string
type ExternalRefKind string
type ExternalRefID string

const (
	ExternalProviderGitHub ExternalProvider = "github"
	ExternalKindRepository ExternalRefKind  = "repository"
	ExternalKindIssue      ExternalRefKind  = "issue"
	ExternalKindPull       ExternalRefKind  = "pull_request"
)

type ExternalRefNode struct {
	Kind ExternalRefKind `json:"kind"`
	ID   ExternalRefID   `json:"id"`
}

// UnmarshalJSON distinguishes omitted nodes from explicit empty nodes so a malformed item cannot select a parent.
func (n *ExternalRefNode) UnmarshalJSON(data []byte) error {
	type node ExternalRefNode
	var in node
	if err := jsonv2.Unmarshal(data, &in, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if in == (node{}) {
		return errors.New("empty external reference node")
	}
	*n = ExternalRefNode(in)
	return nil
}

type ExternalTargetKind string
type ExternalTargetID string
type ExternalBindingID string

const (
	ExternalTargetDirection ExternalTargetKind = "direction"
	ExternalTargetTodo      ExternalTargetKind = "todo"
)

type ExternalForwardMode string
type ExternalActionRole string
type ExternalNewItemPolicy string

// ExternalPolicy is a sparse override. A nil field inherits, and an empty PreFilter
// explicitly clears the inherited filter.
type ExternalPolicy struct {
	Mode                  *ExternalForwardMode   `json:"mode,omitempty"`
	DigestIntervalMinutes *int                   `json:"digest_interval_minutes,omitempty"`
	ActionRole            *ExternalActionRole    `json:"action_role,omitempty"`
	NewItemPolicy         *ExternalNewItemPolicy `json:"new_item_policy,omitempty"`
	PreFilter             *ExternalPreFilter     `json:"pre_filter,omitempty"`
}

type ExternalPreFilter struct {
	Keywords []string `json:"keywords,omitempty"`
	Mentions []string `json:"mentions,omitempty"`
	Senders  []string `json:"senders,omitempty"`
}

type ExternalScopedPolicy struct {
	ExternalRef ExternalRef    `json:"external_ref"`
	Policy      ExternalPolicy `json:"policy"`
}

type ExternalPolicySource string

const (
	ExternalPolicyDefault   ExternalPolicySource = "default"
	ExternalPolicyChannel   ExternalPolicySource = "channel"
	ExternalPolicyContainer ExternalPolicySource = "container"
	ExternalPolicyItem      ExternalPolicySource = "item"
	ExternalPolicyBinding   ExternalPolicySource = "binding"
)

type ExternalPolicyOrigin struct {
	Source      ExternalPolicySource `json:"source"`
	ExternalRef *ExternalRef         `json:"external_ref,omitempty"`
	BindingID   ExternalBindingID    `json:"binding_id,omitempty"`
}

type ExternalPolicyProvenance struct {
	Mode                  ExternalPolicyOrigin  `json:"mode"`
	DigestIntervalMinutes *ExternalPolicyOrigin `json:"digest_interval_minutes,omitempty"`
	ActionRole            ExternalPolicyOrigin  `json:"action_role"`
	NewItemPolicy         ExternalPolicyOrigin  `json:"new_item_policy"`
	PreFilter             ExternalPolicyOrigin  `json:"pre_filter"`
}

type ExternalEffectivePolicy struct {
	ExternalRef ExternalRef              `json:"external_ref"`
	Policy      ExternalPolicy           `json:"policy"`
	Provenance  ExternalPolicyProvenance `json:"provenance"`
}

type ExternalBindingInput struct {
	ExternalRef ExternalRef        `json:"external_ref"`
	TargetKind  ExternalTargetKind `json:"target_kind"`
	TargetID    ExternalTargetID   `json:"target_id"`
	Policy      ExternalPolicy     `json:"policy"`
}

type ExternalBinding struct {
	ID ExternalBindingID `json:"id"`
	ExternalBindingInput
}

type ExternalBindingList struct {
	Bindings []ExternalBinding `json:"bindings"`
}

type ExternalBindingSource string
type ExternalRepositoryRole string

const (
	ExternalBindingExplicit       ExternalBindingSource = "explicit"
	ExternalBindingRepositoryLink ExternalBindingSource = "repository_link"
	ExternalBindingLarkTopic      ExternalBindingSource = "lark_topic"
)

type ExternalBindingMatch struct {
	ExternalRef    ExternalRef            `json:"external_ref"`
	TargetKind     ExternalTargetKind     `json:"target_kind"`
	TargetID       ExternalTargetID       `json:"target_id"`
	Source         ExternalBindingSource  `json:"source"`
	BindingID      ExternalBindingID      `json:"binding_id,omitempty"`
	RepositoryRole ExternalRepositoryRole `json:"repository_role,omitempty"`
	TargetTitle    string                 `json:"target_title,omitempty"`
	TargetDone     bool                   `json:"target_done,omitempty"`
}

type ExternalBindingLookup struct {
	Path    []ExternalRef          `json:"path"`
	Matches []ExternalBindingMatch `json:"matches"`
}

type ExternalSourceSummary struct {
	Bindings         []ExternalBindingMatch `json:"bindings"`
	OmittedDoneCount int                    `json:"omitted_done_count,omitempty"`
	TotalCount       int                    `json:"total_count,omitempty"`
}

type ExternalEventID string
type ExternalInboxItemID string

// ExternalInboxEvent is the durable routing input for one observed source event.
type ExternalInboxEvent struct {
	Key      string              `json:"key"`
	Ref      ExternalRef         `json:"ref"`
	EventID  ExternalEventID     `json:"event_id"`
	Path     []ExternalRef       `json:"path"`
	InboxID  ExternalInboxItemID `json:"inbox_id"`
	Cursor   string              `json:"cursor"`
	Title    string              `json:"title"`
	Summary  string              `json:"summary"`
	Senders  []string            `json:"senders,omitempty"`
	Mentions []string            `json:"mentions,omitempty"`
	URL      string              `json:"url,omitempty"`
}

// ExternalInboxTarget names a recipient selected from the effective bindings.
type ExternalInboxTarget struct {
	ID   ExternalTargetID   `json:"id"`
	Kind ExternalTargetKind `json:"kind"`
}

// ExternalInboxDelivery pins a routing decision and, once resolved, its recipient.
type ExternalInboxDelivery struct {
	Target    ExternalInboxTarget `json:"target"`
	Scope     ExternalInboxTarget `json:"scope"` // authorizing target, including a created to-do's direction
	AutoTodo  bool                `json:"auto_todo,omitempty"`
	Method    string              `json:"method,omitempty"`
	Machine   string              `json:"machine,omitempty"`
	Session   string              `json:"session,omitempty"`
	Tried     bool                `json:"tried,omitempty"`
	Delivered bool                `json:"delivered,omitempty"`
}

// ExternalInboxPlan is the resumable routing decision kept in Postgres.
type ExternalInboxPlan struct {
	Owner *ExternalInboxDelivery  `json:"owner,omitempty"`
	FYI   []ExternalInboxDelivery `json:"fyi,omitempty"`
	Ask   bool                    `json:"ask,omitempty"`
}

// ExternalInboxWork pairs an event with the outbox's clock and resumable decision.
type ExternalInboxWork struct {
	ExternalInboxEvent
	ReceivedAt time.Time
	Plan       *ExternalInboxPlan
}

// ExternalRoutingConfiguration reads lookup matches and policy inputs from one snapshot.
type ExternalRoutingConfiguration struct {
	Lookup   ExternalBindingLookup
	Policies []ExternalScopedPolicy
	Bindings []ExternalBinding
}
