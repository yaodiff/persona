package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
)

const standingAuthorizationsKey = "assistant.standing_authorizations"

var errStandingConflict = errors.New("standing authorizations changed; read the current revision and try again")

// StandingAuthorization is a user-approved computer/directory grant or explicit message scope.
// Conditions are retained verbatim; only independently verified conditions permit automatic answers.
type StandingAuthorization struct {
	ID         string                `json:"id"`
	MachineID  string                `json:"machine_id"`
	Directory  string                `json:"directory"`
	Actions    []string              `json:"actions"`
	Conditions []string              `json:"conditions"`
	Quote      string                `json:"quote"`
	Source     string                `json:"source"`
	GrantedAt  time.Time             `json:"granted_at"`
	Reversible bool                  `json:"reversible"`
	Messages   *MessageAuthorization `json:"messages,omitempty"`
}

// StandingAuthorizations keeps the revision even after all grants are revoked.
type StandingAuthorizations struct {
	Revision       int64                   `json:"revision"`
	Authorizations []StandingAuthorization `json:"authorizations"`
}

type standingCASStore interface {
	CompareSetting(context.Context, string, int64, any) (bool, error)
}

func (s *Service) standingAuthorizations(ctx context.Context) (StandingAuthorizations, error) {
	list := StandingAuthorizations{Authorizations: []StandingAuthorization{}}
	st := s.store()
	if st == nil {
		return list, nil
	}
	if _, err := st.Setting(ctx, standingAuthorizationsKey, &list); err != nil {
		return list, err
	}
	if list.Authorizations == nil {
		list.Authorizations = []StandingAuthorization{}
	}
	return list, nil
}

func (s *Service) standingForDirectory(ctx context.Context, machine, cwd string) ([]StandingAuthorization, error) {
	list, err := s.standingAuthorizations(ctx)
	if err != nil {
		return nil, err
	}
	out := []StandingAuthorization{}
	if !path.IsAbs(cwd) || strings.Contains(cwd, "\\") {
		return out, nil
	}
	cwd = path.Clean(cwd)
	for _, grant := range list.Authorizations {
		if grant.Messages != nil {
			continue
		}
		if grant.MachineID != machine {
			continue
		}
		matches, err := s.standingScopeMatches(ctx, machine, grant.Directory, cwd)
		if err != nil {
			return nil, err
		}
		if matches {
			out = append(out, grant)
		}
	}
	return out, nil
}

func validStandingDirectory(dir string) bool {
	return path.IsAbs(dir) && dir == path.Clean(dir) && dir != "/" &&
		strings.Count(dir, "/") >= 2 && !strings.ContainsAny(dir, "*?[]{}\\\n\r\x00")
}

func checkStandingGrant(grant StandingAuthorization) error {
	if grant.Messages != nil {
		if err := checkMessageAuthorization(grant); err != nil {
			return err
		}
	} else {
		if grant.MachineID == "" || strings.TrimSpace(grant.MachineID) != grant.MachineID ||
			len(grant.MachineID) > 200 || strings.ContainsAny(grant.MachineID, "*?/\\\n\r\x00") {
			return errors.New("machine_id must name one computer")
		}
		if !validStandingDirectory(grant.Directory) || len(grant.Directory) > 2000 {
			return errors.New("directory must be a specific absolute directory without wildcards")
		}
	}
	if len(grant.Actions) == 0 || len(grant.Actions) > 7 {
		return errors.New("actions must contain one or more supported actions")
	}
	seen := map[string]bool{}
	for _, action := range grant.Actions {
		if !slices.Contains([]string{"merge", "push", "deploy-test", "deploy-production", "send", "delete", "spend"}, action) || seen[action] {
			return fmt.Errorf("unsupported or duplicate action %q", action)
		}
		seen[action] = true
	}
	if len(grant.Conditions) > 20 {
		return errors.New("at most 20 conditions are allowed")
	}
	for _, condition := range grant.Conditions {
		if strings.TrimSpace(condition) == "" || utf8.RuneCountInString(condition) > 1000 {
			return errors.New("conditions must contain 1–1000 characters")
		}
	}
	if strings.TrimSpace(grant.Quote) == "" || utf8.RuneCountInString(grant.Quote) > 4000 ||
		strings.TrimSpace(grant.Source) == "" || utf8.RuneCountInString(grant.Source) > 1000 {
		return errors.New("quote (1–4000 characters) and source (1–1000 characters) are required")
	}
	return nil
}

// prepareStandingChange never changes an existing grant in place. A changed scope gets a new identity and approval.
func prepareStandingChange(before StandingAuthorizations, in StandingAuthorizations, now time.Time) (StandingAuthorizations, error) {
	if in.Revision != before.Revision {
		return StandingAuthorizations{}, errStandingConflict
	}
	if len(in.Authorizations) > 64 {
		return StandingAuthorizations{}, errors.New("at most 64 standing authorizations are allowed")
	}
	out := StandingAuthorizations{Revision: before.Revision + 1, Authorizations: []StandingAuthorization{}}
	seen := map[string]bool{}
	for _, grant := range in.Authorizations {
		if err := checkStandingGrant(grant); err != nil {
			return StandingAuthorizations{}, err
		}
		if grant.Conditions == nil {
			grant.Conditions = []string{}
		}
		if grant.ID == "" {
			grant.ID, grant.GrantedAt = "sa_"+randomHex(12), now.UTC()
		} else {
			i := slices.IndexFunc(before.Authorizations, func(old StandingAuthorization) bool { return old.ID == grant.ID })
			if i < 0 || !reflect.DeepEqual(before.Authorizations[i], grant) {
				return StandingAuthorizations{}, errors.New("existing grant IDs cannot be edited; remove the old grant and add a new one without an ID")
			}
		}
		if seen[grant.ID] {
			return StandingAuthorizations{}, errors.New("duplicate authorization ID")
		}
		seen[grant.ID] = true
		out.Authorizations = append(out.Authorizations, grant)
	}
	return out, nil
}

func (s *Service) saveStandingAuthorizations(ctx context.Context, expected int64, after StandingAuthorizations) error {
	st, ok := s.store().(standingCASStore)
	if !ok {
		return errors.New("standing authorizations need a persistent revision-aware database")
	}
	updated, err := st.CompareSetting(ctx, standingAuthorizationsKey, expected, after)
	if err != nil {
		return err
	}
	if !updated {
		return errStandingConflict
	}
	return nil
}

func (s *Service) getStandingAuthorizations(ctx context.Context) (string, error) {
	list, err := s.standingAuthorizations(ctx)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(list)
	return string(raw), err
}

func (s *Service) setStandingAuthorizations(ctx context.Context, value json.RawMessage) (*ownChange, error) {
	var wire struct {
		Revision       *int64                   `json:"revision"`
		Authorizations *[]StandingAuthorization `json:"authorizations"`
	}
	if err := json.Unmarshal(value, &wire); err != nil {
		return nil, err
	}
	if wire.Revision == nil || wire.Authorizations == nil {
		return nil, errors.New("value must contain revision and authorizations")
	}
	in := StandingAuthorizations{Revision: *wire.Revision, Authorizations: *wire.Authorizations}
	before, err := s.standingAuthorizations(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := s.store().(standingCASStore); !ok {
		return nil, errors.New("standing authorizations need a persistent revision-aware database")
	}
	after, err := prepareStandingChange(before, in, time.Now())
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(before.Authorizations, after.Authorizations) {
		return &ownChange{summary: "Standing authorizations are unchanged."}, nil
	}
	oldJSON, _ := json.MarshalIndent(before, "", "  ")
	newJSON, _ := json.MarshalIndent(after, "", "  ")
	return &ownChange{applyChecksProposal: true, cardText: func(l i18n.Lang) string {
		text := ""
		for _, grant := range after.Authorizations {
			if grant.Messages != nil && !slices.ContainsFunc(before.Authorizations, func(old StandingAuthorization) bool { return old.ID == grant.ID }) {
				text += messageScopeText(l, *grant.Messages) + "\n\n"
			}
		}
		return text + i18n.T(l, "assistant-standing_authorizations.change", i18n.Args{"before": string(oldJSON), "after": string(newJSON)})
	}, apply: func(ctx context.Context) (string, error) {
		s.standingMu.Lock()
		defer s.standingMu.Unlock()
		if err := s.saveStandingAuthorizations(ctx, before.Revision, after); err != nil {
			if errors.Is(err, errStandingConflict) {
				return "", fmt.Errorf("%w: %w", approvalops.ErrStale, err)
			}
			return "", err
		}
		return s.getStandingAuthorizations(ctx)
	}}, nil
}

func (s *Service) standingAuthorizationRoutes() {
	s.handle("GET /api/v1/assistant/standing-authorizations", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-standing_authorizations.full_access"))
			return
		}
		if s.store() == nil {
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		list, err := s.standingAuthorizations(r.Context())
		if err != nil {
			s.logf("standing authorizations: list: %v", err)
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		apierr.WriteJSON(w, list)
	})
	s.handle("DELETE /api/v1/assistant/standing-authorizations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-standing_authorizations.full_access"))
			return
		}
		s.standingMu.Lock()
		defer s.standingMu.Unlock()
		before, err := s.standingAuthorizations(r.Context())
		if err != nil || s.store() == nil {
			s.logf("standing authorizations: read before revocation: %v", err)
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		i := slices.IndexFunc(before.Authorizations, func(grant StandingAuthorization) bool { return grant.ID == r.PathValue("id") })
		if i >= 0 {
			after := StandingAuthorizations{Revision: before.Revision + 1, Authorizations: slices.Delete(slices.Clone(before.Authorizations), i, i+1)}
			if err := s.saveStandingAuthorizations(r.Context(), before.Revision, after); err != nil {
				s.logf("standing authorizations: revoke: %v", err)
				status, key := http.StatusServiceUnavailable, "http-status.unavailable"
				if errors.Is(err, errStandingConflict) {
					status, key = http.StatusConflict, "assistant-standing_authorizations.conflict"
				}
				apierr.Write(w, status, i18n.Err(key))
				return
			}
			before = after
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "revision": before.Revision})
	})
}
