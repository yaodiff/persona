package assistant

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const outboundSettingsCardPrefix = "ix_outbound_settings_"
const outboundSettingsCardTTL = 24 * time.Hour

var errOutboundSettingsChanged = i18n.Err("assistant-outbound.settings_changed")

var errOutboundOwnerAnswer = i18n.Err("assistant-standing_authorizations.full_access")

type outboundSettingsProposal struct {
	Identity string                `json:"identity,omitempty"`
	Before   string                `json:"before,omitempty"`
	Revision int64                 `json:"revision"`
	Scope    *MessageAuthorization `json:"scope,omitempty"`
}

type outboundSettingsStore interface {
	FinishSettingInteraction(context.Context, string, string, proto.Resolution, *db.ApprovedSetting) (string, error)
}

func outboundSettingsCard(ix proto.Interaction) bool {
	return ix.Kind == proto.IxPermission && ix.ToolName == "persona_settings" && ix.Resumable &&
		ix.MachineID == "" && ix.SessionID == "" && strings.HasPrefix(ix.ID, outboundSettingsCardPrefix)
}

func (p outboundSettingsProposal) standingChange(before StandingAuthorizations, now time.Time) (StandingAuthorizations, error) {
	scope, _ := json.Marshal(p.Scope)
	grant := StandingAuthorization{Actions: []string{"send"}, Conditions: []string{},
		Quote: "Allow automatic messages within this exact scope: " + string(scope), Source: "settings", Reversible: true, Messages: p.Scope}
	in := StandingAuthorizations{Revision: p.Revision, Authorizations: append(before.Authorizations, grant)}
	return prepareStandingChange(before, in, now)
}

func (s *Service) outboundMessageRoutes() {
	s.handle("GET /api/v1/assistant/outbound-messages", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errOutboundOwnerAnswer)
			return
		}
		if id := r.URL.Query().Get("approval"); id != "" {
			s.outboundApprovalStatus(w, r, id)
			return
		}
		identity, err := s.outboundIdentity(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		result := map[string]string{"identity": identity}
		s.fleet.mu.Lock()
		var latest proto.Interaction
		for _, p := range s.fleet.interactions {
			if !p.claimed && outboundSettingsCard(p.ix) && time.Now().Before(p.ix.ExpiresAt) && p.ix.CreatedAt.After(latest.CreatedAt) {
				latest = p.ix
			}
		}
		s.fleet.mu.Unlock()
		if latest.ID != "" {
			result["approval_id"] = latest.ID
		}
		apierr.WriteJSON(w, result)
	})
	s.handle("POST /api/v1/assistant/outbound-messages", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errOutboundOwnerAnswer)
			return
		}
		// Before is captured by the manager; it is never supplied by the client.
		var in struct {
			Identity string                `json:"identity"`
			Revision int64                 `json:"revision"`
			Scope    *MessageAuthorization `json:"scope"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		ix, err := s.createOutboundSettingsCard(r.Context(), outboundSettingsProposal{Identity: in.Identity, Revision: in.Revision, Scope: in.Scope})
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errStandingConflict) {
				status = http.StatusConflict
			}
			apierr.Write(w, status, err)
			return
		}
		if ix.ID == "" {
			apierr.WriteJSON(w, map[string]string{"status": "unchanged"})
			return
		}
		s.tellApps(ix)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"approval_id": ix.ID, "status": "pending"})
	})
}

func (s *Service) createOutboundSettingsCard(ctx context.Context, proposal outboundSettingsProposal) (proto.Interaction, error) {
	var ix proto.Interaction
	st := s.fleetStore()
	if _, ok := s.store().(outboundSettingsStore); !ok || st == nil {
		return ix, errors.New("outward-message approvals need persistent settings")
	}
	s.standingMu.Lock()
	defer s.standingMu.Unlock()
	now, lang := time.Now().UTC(), s.lang(ctx)
	var summary string
	if proposal.Scope == nil {
		if !validMessageIdentity(proposal.Identity) {
			return ix, errors.New("identity must be bot or user")
		}
		before, err := s.outboundIdentity(ctx)
		if err != nil || before == proposal.Identity {
			return ix, err
		}
		proposal.Before = before
		summary = i18n.T(lang, "assistant-outbound.identity_change", i18n.Args{"identity": i18n.T(lang, "assistant-outbound."+proposal.Identity)})
	} else {
		before, err := s.standingAuthorizations(ctx)
		if err != nil {
			return ix, err
		}
		if _, err := proposal.standingChange(before, now); err != nil {
			return ix, err
		}
		summary = messageScopeText(lang, *proposal.Scope)
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		return ix, err
	}
	s.fleet.mu.Lock()
	active := 0
	for _, p := range s.fleet.interactions {
		if outboundSettingsCard(p.ix) && now.Before(p.ix.ExpiresAt) {
			active++
			if !p.claimed && bytes.Equal(p.ix.ToolInput, raw) {
				s.fleet.mu.Unlock()
				return p.ix, nil
			}
		}
	}
	s.fleet.mu.Unlock()
	if active >= 64 {
		return ix, errors.New("too many pending outward-message approvals")
	}
	ix = s.ownCard(ctx, proto.Interaction{ID: outboundSettingsCardPrefix + rand.Text(),
		Kind: proto.IxPermission, ToolName: "persona_settings", SessionTitle: "Persona", Summary: summary,
		ToolInput: raw, CreatedAt: now, ExpiresAt: now.Add(outboundSettingsCardTTL), Resumable: true})
	if err := st.InsertInteraction(ctx, ix); err != nil {
		return proto.Interaction{}, err
	}
	s.fleet.mu.Lock()
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	s.ixMapLocked()[ix.ID] = p
	s.orphanLocked(p) // a settings card has no hook; its own deadline governs recovery
	s.fleet.mu.Unlock()
	return ix, nil
}

func (s *Service) resolveOutboundSettingsCard(ctx context.Context, id string, r proto.Resolution) error {
	if !devicewire.IsOwnerAnswer(ctx) {
		return errOutboundOwnerAnswer
	}
	if r.Always || r.EditedText != "" || !(r.UserAllowed() || r.UserDenied() || r.Dismiss && r.Allow == nil) {
		return errInvalidEditedAnswer
	}
	// The click may finish even if this client disconnects while the database commits.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), historyTimeout)
	defer cancel()
	s.standingMu.Lock()
	defer s.standingMu.Unlock()
	ix, ok := s.pendingIx(id)
	if !ok || !outboundSettingsCard(ix) || !time.Now().Before(ix.ExpiresAt) {
		return errNotPending
	}
	st, ok := s.store().(outboundSettingsStore)
	if !ok {
		return errors.New("outward-message approvals need persistent settings")
	}
	answer := proto.Resolution{Allow: r.Allow, Dismiss: r.Dismiss}
	status := endStatus(answer)
	var change *db.ApprovedSetting
	if answer.UserAllowed() {
		var proposal outboundSettingsProposal
		if err := json.Unmarshal(ix.ToolInput, &proposal); err != nil {
			return err
		}
		if proposal.Scope == nil {
			if !validMessageIdentity(proposal.Identity) || !validMessageIdentity(proposal.Before) {
				return errOutboundOwnerAnswer
			}
			change = &db.ApprovedSetting{Key: outboundIdentityKey, Before: proposal.Before, Default: "bot", Value: proposal.Identity}
		} else {
			before, err := s.standingAuthorizations(ctx)
			if err != nil {
				return err
			}
			after, err := proposal.standingChange(before, time.Now())
			if errors.Is(err, errStandingConflict) {
				status = "failed"
			} else if err != nil {
				return err
			} else {
				change = &db.ApprovedSetting{Key: standingAuthorizationsKey, Revision: &proposal.Revision, Value: after}
			}
		}
	}
	outcome, err := st.FinishSettingInteraction(ctx, id, status, answer, change)
	if err != nil {
		return err
	}
	if outcome == "" {
		return errNotPending
	}
	s.finishInteraction(id)
	if outcome == "failed" {
		return errOutboundSettingsChanged
	}
	return nil
}

func (s *Service) outboundApprovalStatus(w http.ResponseWriter, r *http.Request, id string) {
	st := s.fleetStore()
	if !strings.HasPrefix(id, outboundSettingsCardPrefix) || st == nil {
		apierr.Write(w, http.StatusNotFound, errNotPending)
		return
	}
	status, answer, err := st.InteractionOutcome(r.Context(), id)
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return
	}
	switch status {
	case "resolved":
		status = "declined"
		if answer != nil && answer.UserAllowed() {
			status = "approved"
		}
	case "pending", "failed":
		// Both statuses are directly visible to the settings page.
	case "":
		apierr.Write(w, http.StatusNotFound, errNotPending)
		return
	default:
		status = "declined"
	}
	apierr.WriteJSON(w, map[string]string{"status": status})
}
