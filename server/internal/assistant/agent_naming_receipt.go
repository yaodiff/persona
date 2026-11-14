package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// agent_autoname_receipt is a read-only view of the calling assistant's existing autoname confirmation cards and
// one target's existing naming record. It is not a request-scoped execution receipt: the model call id never reached
// the manager, so no later query can invent that association. In particular, a requested name may have come from an
// earlier HTTP call, and LastFailure may belong to background naming. Neither is attributed to the call being asked
// about. Nothing here starts a model, approves a card, retries a request, resolves an interaction or publishes a row.
const (
	autonameReceiptWindow  = 24 * time.Hour
	autonameReceiptPage    = 50
	autonameReceiptMaxPage = 2
)

type autonameReceiptQuery struct {
	Machine   string `json:"machine"`
	SessionID string `json:"session_id"`
	Since     string `json:"since"`
	Before    string `json:"before"`
}

type autonameReceiptCard struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	Allow      *bool      `json:"allow,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

type autonameReceiptSnapshot struct {
	Status     string     `json:"status"` // current: the target's existing record; not_found: none; unknown: unreadable
	Requested  bool       `json:"requested"`
	Title      string     `json:"title,omitempty"`
	NamedAt    *time.Time `json:"named_at,omitempty"`
	ObservedAt time.Time  `json:"observed_at"`
}

type autonameReceiptAnswer struct {
	Confirmation string                  `json:"confirmation"`
	Card         *autonameReceiptCard    `json:"card,omitempty"`
	Cards        []autonameReceiptCard   `json:"cards,omitempty"`
	Coverage     string                  `json:"coverage"`
	Naming       autonameReceiptSnapshot `json:"naming"`
}

// toolAgentAutonameReceipt reads only through the existing owning-manager's stores. The caller's identity is the
// trusted machine/self of mcpCall, never a caller_machine/caller_session argument. An old card's own two-field
// autoname input identifies its target; the four-field evidence query is not part of that identity. Unknown fields
// or corrupt input never match. Unreadable, incomplete or multiple matching cards remain unknown or ambiguous.
func (s *Service) toolAgentAutonameReceipt(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a autonameReceiptQuery
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("agent_autoname_receipt arguments: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", errors.New("agent_autoname_receipt expects one JSON object")
	}
	if strings.TrimSpace(a.Machine) != a.Machine || strings.TrimSpace(a.SessionID) != a.SessionID || a.Machine == "" || a.SessionID == "" {
		return "", errors.New("agent_autoname_receipt requires the full machine and session ids")
	}
	m, found := s.fleetMachine(a.Machine)
	if !found || m.ID != a.Machine {
		return "", errors.New("agent_autoname_receipt requires the exact machine id")
	}
	c, found, err := s.autonameReceiptTarget(ctx, a.Machine, a.SessionID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("agent_autoname_receipt requires the exact existing machine and full session id")
	}
	if c.Assistant || m.isAssistant(c) || s.isMain(ctx, c.SessionID) {
		return "", errors.New("agent_autoname_receipt is for coding agents, not Persona conversations")
	}
	since, before, err := autonameReceiptBounds(a.Since, a.Before)
	if err != nil {
		return "", err
	}
	out := autonameReceiptAnswer{Confirmation: "unknown", Coverage: "unknown"}
	out.Naming, err = s.autonameReceiptNaming(ctx, a.Machine, a.SessionID)
	if err != nil {
		out.Naming = autonameReceiptSnapshot{Status: "unknown", ObservedAt: time.Now().UTC()}
	}
	cards, coverage, err := s.autonameReceiptCards(ctx, machine, self, a.Machine, a.SessionID, since, before)
	if err != nil {
		out.Confirmation = "unknown"
	} else {
		out.Coverage = coverage
	}
	switch len(cards) {
	case 0:
		out.Confirmation = "unknown" // no matching card is not evidence that the request never ran
	case 1:
		card := cards[0]
		if coverage == "complete" {
			out.Card, out.Confirmation = &card, "found"
		} else {
			out.Cards = cards // one match in partial coverage is not enough to identify the original call
		}
	default:
		out.Confirmation, out.Cards = "ambiguous", cards
	}
	text, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(text), nil
}

func autonameReceiptBounds(sinceText, beforeText string) (time.Time, time.Time, error) {
	var since, before time.Time
	var err error
	if sinceText != "" {
		since, err = time.Parse(time.RFC3339Nano, sinceText)
		if err != nil {
			return since, before, errors.New("agent_autoname_receipt since must be RFC 3339")
		}
	}
	if beforeText != "" {
		before, err = time.Parse(time.RFC3339Nano, beforeText)
		if err != nil {
			return since, before, errors.New("agent_autoname_receipt before must be RFC 3339")
		}
	}
	if since.IsZero() != before.IsZero() {
		return since, before, errors.New("agent_autoname_receipt requires both since and before, or neither")
	}
	if !since.IsZero() && (!since.Before(before) || before.Sub(since) > autonameReceiptWindow) {
		return since, before, errors.New("agent_autoname_receipt requires a bounded window of at most 24 hours")
	}
	return since, before, nil
}

func (s *Service) autonameReceiptTarget(ctx context.Context, machine, sid string) (proto.Session, bool, error) {
	if s.fleetOn() {
		if row, ok := s.nodeSession(machine, sid); ok {
			return row, true, nil
		}
		row, ok, err := s.archivedRow(ctx, machine, sid)
		return row, ok, err
	}
	row, ok := s.session(ctx, machine, sid)
	return row, ok, nil
}

func (s *Service) autonameReceiptCards(ctx context.Context, machine, self, targetMachine, targetSID string, since, before time.Time) ([]autonameReceiptCard, string, error) {
	st := s.fleetStore()
	if st == nil {
		return nil, "unknown", errors.New("agent_autoname_receipt cannot read the confirmation history")
	}
	// The pager's Before is exclusive; the query is [since,before), with the cursor advancing by (created_at,id).
	// Supply the upper bound on the first read too, so later unrelated cards cannot crowd out the original window.
	query := db.InteractionQuery{Machine: machine, Session: self, Kind: string(proto.IxPermission), Since: since, Before: before, Limit: autonameReceiptPage}
	var found []autonameReceiptCard
	for pageNumber := 0; pageNumber < autonameReceiptMaxPage; pageNumber++ {
		page, err := guardedStore{st}.InteractionsPage(ctx, query)
		if err != nil {
			return nil, "unknown", errors.New("agent_autoname_receipt could not read the confirmation history")
		}
		for _, rec := range page.Records {
			ix := rec.Interaction
			if ix.Kind != proto.IxPermission || ix.MachineID != machine || ix.SessionID != self || ix.ToolName != "agent_autoname" || !autonameReceiptTargetInput(ix.ToolInput, targetMachine, targetSID) {
				continue
			}
			if (!since.IsZero() && ix.CreatedAt.Before(since)) || (!before.IsZero() && !ix.CreatedAt.Before(before)) {
				continue
			}
			card := autonameReceiptCard{ID: ix.ID, Status: rec.Status, ResolvedAt: rec.ResolvedAt}
			if rec.Resolution != nil {
				card.Allow = rec.Resolution.Allow // never copy Values, Summary, ToolInput, Reason or provider text
			}
			found = append(found, card)
		}
		if page.NextBefore == nil {
			return found, "complete", nil
		}
		query.Before, query.BeforeID = *page.NextBefore, page.NextBeforeID
	}
	return found, "partial", nil // an ambiguous partial history never becomes a positive claim
}

// autonameReceiptTargetInput is the exact target in an actual autoname card, not the evidence query's time window.
// Only the two fields accepted by the write tool are allowed; different legal JSON layouts mean the same target.
func autonameReceiptTargetInput(raw json.RawMessage, machine, sid string) bool {
	var input struct {
		Machine   string `json:"machine"`
		SessionID string `json:"session_id"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil || dec.Decode(new(any)) != io.EOF {
		return false
	}
	return input.Machine == machine && input.SessionID == sid
}

func (s *Service) autonameReceiptNaming(ctx context.Context, machine, sid string) (autonameReceiptSnapshot, error) {
	b := &s.agentNames
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.agentNamesLocked(ctx) {
		return autonameReceiptSnapshot{}, errors.New("naming snapshot unreadable")
	}
	r, present := b.records[agentNameKey(machine, sid)]
	out := autonameReceiptSnapshot{Status: "not_found", ObservedAt: time.Now().UTC()}
	if !present {
		return out, nil
	}
	if r.Machine != machine || r.Session != sid {
		return autonameReceiptSnapshot{}, errors.New("naming snapshot is inconsistent")
	}
	out.Status = "current"
	out.Requested, out.Title = r.Requested, r.Title
	if !r.NamedAt.IsZero() {
		at := r.NamedAt
		out.NamedAt = &at
	}
	return out, nil
}
