package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// dispatchPolicyRoutes serves GET/PUT /api/v1/assistant/dispatch-policy. Full devices read and write it like any
// assistant setting; an extension whose token grants the assistant.dispatch_policy hub scope reaches it too, and a
// policy it writes must expire within dispatchPolicyMaxExpiry, so a stopped or uninstalled extension leaves nothing
// in force.
func (s *Service) dispatchPolicyRoutes() {
	s.handle("GET /api/v1/assistant/dispatch-policy", func(w http.ResponseWriter, r *http.Request) {
		policy := s.activeDispatchPolicy(r.Context())
		if policy == nil {
			policy = &DispatchPolicy{Rules: []DispatchRule{}}
		}
		apierr.WriteJSON(w, map[string]any{"dispatch_policy": policy})
	})

	s.handle("PUT /api/v1/assistant/dispatch-policy", func(w http.ResponseWriter, r *http.Request) {
		// The hub relays an extension's request only to routes its granted scopes open, so an extension role here
		// already holds assistant.dispatch_policy; read-only devices are refused.
		role := r.Header.Get(assistantwire.RoleHeader)
		if role != "" && role != roleFull && role != roleExtension {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change the dispatch policy"))
			return
		}
		var in struct {
			DispatchPolicy *DispatchPolicy `json:"dispatch_policy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if in.DispatchPolicy == nil {
			apierr.Write(w, http.StatusBadRequest, errors.New("the body must name dispatch_policy"))
			return
		}
		fromExtension := role == roleExtension
		clearing := len(in.DispatchPolicy.Rules) == 0
		if !clearing {
			if err := in.DispatchPolicy.validate(!fromExtension, time.Now().UTC()); err != nil {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
		}
		var saved DispatchPolicy
		_, err := s.updateSettings(r.Context(), func(v *Settings) error {
			if clearing {
				v.DispatchPolicy = nil
				saved = DispatchPolicy{Rules: []DispatchRule{}}
				return nil
			}
			v.DispatchPolicy = in.DispatchPolicy
			saved = *in.DispatchPolicy
			return nil
		})
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		s.settingsChanged(r.Context())
		apierr.WriteJSON(w, map[string]any{"dispatch_policy": saved})
	})
}
