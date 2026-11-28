package assistant

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/externalbinding/externalbindingadapt"
	"github.com/nwverse/persona/server/internal/assistant/externalsourcestore"
	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

type externalSourceStore interface {
	ListExternalBindings(context.Context, assistantwire.ExternalTargetKind, assistantwire.ExternalTargetID, assistantwire.ExternalProvider, *assistantwire.ExternalRef) ([]assistantwire.ExternalBinding, error)
	CreateExternalBinding(context.Context, assistantwire.ExternalBindingInput) (assistantwire.ExternalBinding, error)
	UpdateExternalBinding(context.Context, assistantwire.ExternalBindingID, assistantwire.ExternalBindingInput) (assistantwire.ExternalBinding, error)
	DeleteExternalBinding(context.Context, assistantwire.ExternalBindingID) error
	LookupExternalBindings(context.Context, []assistantwire.ExternalRef) (assistantwire.ExternalBindingLookup, error)
	ExternalSourceSummary(context.Context, assistantwire.ExternalProvider, *assistantwire.ExternalChannelID, bool) (assistantwire.ExternalSourceSummary, error)
	ExternalSourcePolicies(context.Context, []assistantwire.ExternalRef, assistantwire.ExternalBindingID) ([]assistantwire.ExternalScopedPolicy, *assistantwire.ExternalBinding, error)
	PutExternalSourcePolicy(context.Context, assistantwire.ExternalScopedPolicy) error
}

type externalSourceRoutes struct{ service *Service }

func (externalSourceRoutes) Name() seam.ModuleName { return seam.NewModuleName("external-sources") }

func (x externalSourceRoutes) Register(k seam.Kernel) {
	route := func(pattern string, serve func(http.ResponseWriter, *http.Request, externalSourceStore)) {
		k.Route(seam.NewRoute(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !k.Full(r) {
				writeExternalSourceError(w, http.StatusForbidden, "forbidden")
				return
			}
			store, ok := x.service.store().(externalSourceStore)
			if !ok {
				writeExternalSourceError(w, http.StatusServiceUnavailable, "external_sources_unavailable")
				return
			}
			serve(w, r, store)
		}))
	}
	route(assistantwire.ExternalBindingsListRoute, x.list)
	route(assistantwire.ExternalBindingsCreateRoute, x.saveBinding)
	route(assistantwire.ExternalBindingsUpdateRoute, x.saveBinding)
	route(assistantwire.ExternalBindingsDeleteRoute, x.deleteBinding)
	route(assistantwire.ExternalBindingsLookupRoute, x.lookup)
	route(assistantwire.ExternalBindingsSummaryRoute, x.summary)
	route(assistantwire.ExternalPoliciesReadRoute, x.readPolicy)
	route(assistantwire.ExternalPoliciesWriteRoute, x.writePolicy)
	route(assistantwire.ExternalPolicyEffectiveRoute, x.resolve)
}

func (x externalSourceRoutes) list(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	q := r.URL.Query()
	var (
		kind     assistantwire.ExternalTargetKind
		target   assistantwire.ExternalTargetID
		provider assistantwire.ExternalProvider
		ref      *assistantwire.ExternalRef
		err      error
	)
	if q.Has("target_kind") {
		kind, err = externalbindingadapt.TargetKind(assistantwire.ExternalTargetKind(q.Get("target_kind")))
		if err != nil {
			writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_target")
			return
		}
	}
	if q.Has("target_id") {
		target, err = externalbindingadapt.TargetID(assistantwire.ExternalTargetID(q.Get("target_id")))
		if err != nil {
			writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_target")
			return
		}
	}
	if q.Has("provider") {
		provider, err = externalbindingadapt.Provider(assistantwire.ExternalProvider(q.Get("provider")))
		if err != nil {
			writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_ref")
			return
		}
	}
	if q.Has("external_ref") {
		path, err := requestExternalPath(w, r)
		if err != nil {
			return
		}
		ref = &path[0]
	}
	bindings, err := store.ListExternalBindings(r.Context(), kind, target, provider, ref)
	if err != nil {
		x.storeError(w, err)
		return
	}
	apierr.WriteJSON(w, assistantwire.ExternalBindingList{Bindings: bindings})
}

func (x externalSourceRoutes) saveBinding(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	input, err := readExternalBody[assistantwire.ExternalBindingInput](w, r)
	if err != nil {
		return
	}
	in, err := externalbindingadapt.Binding(input)
	if err != nil {
		writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_binding")
		return
	}
	ctx := r.Context()
	if in.TargetKind == assistantwire.ExternalTargetDirection {
		exists, err := x.service.externalDirectionExists(ctx, in.TargetID)
		if err != nil {
			x.storeError(w, err)
			return
		}
		if !exists {
			writeExternalSourceError(w, http.StatusNotFound, "external_target_not_found")
			return
		}
	}
	id := assistantwire.ExternalBindingID(r.PathValue("id"))
	if id != "" {
		saved, err := store.UpdateExternalBinding(ctx, id, in)
		if err != nil {
			x.storeError(w, err)
			return
		}
		apierr.WriteJSON(w, saved)
		return
	}
	saved, err := store.CreateExternalBinding(ctx, in)
	if err != nil {
		x.storeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = jsonv2.MarshalWrite(w, saved)
}

func (s *Service) externalDirectionExists(ctx context.Context, id assistantwire.ExternalTargetID) (bool, error) {
	book := &s.directions
	book.mu.Lock()
	loaded := s.directionsLocked(ctx)
	exists := book.m[string(id)] != nil
	book.mu.Unlock()
	if !loaded {
		return false, errors.New("could not read directions")
	}
	if exists {
		return true, nil
	}
	// A newly named leader can be in settings before the direction book is reconciled.
	settings, ok := s.savedSettings(ctx)
	if !ok {
		return false, errors.New("could not read direction membership")
	}
	return slices.Contains(settings.Leaders, string(id)), nil
}

func (x externalSourceRoutes) deleteBinding(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	if err := store.DeleteExternalBinding(r.Context(), assistantwire.ExternalBindingID(r.PathValue("id"))); err != nil {
		x.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (x externalSourceRoutes) lookup(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	path, err := requestExternalPath(w, r)
	if err != nil {
		return
	}
	result, err := store.LookupExternalBindings(r.Context(), path)
	if err != nil {
		x.storeError(w, err)
		return
	}
	apierr.WriteJSON(w, result)
}

func (x externalSourceRoutes) summary(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_query")
		return
	}
	var includeDone bool
	if values, present := q["include_done"]; present {
		if len(values) != 1 {
			writeExternalSourceError(w, http.StatusBadRequest, "invalid_include_done")
			return
		}
		switch values[0] {
		case "true", "1":
			includeDone = true
		case "false", "0":
		default:
			writeExternalSourceError(w, http.StatusBadRequest, "invalid_include_done")
			return
		}
	}
	var provider assistantwire.ExternalProvider
	if q.Has("provider") {
		p, err := externalbindingadapt.Provider(assistantwire.ExternalProvider(q.Get("provider")))
		if err != nil {
			writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_ref")
			return
		}
		provider = p
	}
	var channel *assistantwire.ExternalChannelID
	if q.Has("channel") {
		path, err := externalbindingadapt.Path(assistantwire.ExternalRef{
			Provider: provider, Channel: assistantwire.ExternalChannelID(q.Get("channel")),
		})
		if err != nil {
			writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_ref")
			return
		}
		channel = &path[0].Channel
	}
	summary, err := store.ExternalSourceSummary(r.Context(), provider, channel, includeDone)
	if err != nil {
		x.storeError(w, err)
		return
	}
	apierr.WriteJSON(w, summary)
}

func (x externalSourceRoutes) readPolicy(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	path, err := requestExternalPath(w, r)
	if err != nil {
		return
	}
	policies, _, err := store.ExternalSourcePolicies(r.Context(), path[:1], "")
	if err != nil {
		x.storeError(w, err)
		return
	}
	saved := assistantwire.ExternalScopedPolicy{ExternalRef: path[0]}
	if len(policies) != 0 {
		saved = policies[0]
	}
	apierr.WriteJSON(w, saved)
}

func (x externalSourceRoutes) writePolicy(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	path, err := requestExternalPath(w, r)
	if err != nil {
		return
	}
	body, err := readExternalBody[assistantwire.ExternalPolicy](w, r)
	if err != nil {
		return
	}
	policy, err := externalbindingadapt.Policy(body)
	if err != nil {
		writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_policy")
		return
	}
	row := assistantwire.ExternalScopedPolicy{ExternalRef: path[0], Policy: policy}
	if err := store.PutExternalSourcePolicy(r.Context(), row); err != nil {
		x.storeError(w, err)
		return
	}
	apierr.WriteJSON(w, row)
}

func (x externalSourceRoutes) resolve(w http.ResponseWriter, r *http.Request, store externalSourceStore) {
	path, err := requestExternalPath(w, r)
	if err != nil {
		return
	}
	id := assistantwire.ExternalBindingID(r.URL.Query().Get("binding_id"))
	resolved, _, err := resolveExternalSourcePolicy(r.Context(), store, path, id)
	if err != nil {
		x.storeError(w, err)
		return
	}
	apierr.WriteJSON(w, resolved)
}

func resolveExternalSourcePolicy(ctx context.Context, store externalSourceStore, path []assistantwire.ExternalRef, id assistantwire.ExternalBindingID) (assistantwire.ExternalEffectivePolicy, []assistantwire.ExternalScopedPolicy, error) {
	policies, binding, err := store.ExternalSourcePolicies(ctx, path, id)
	if err != nil {
		return assistantwire.ExternalEffectivePolicy{}, nil, err
	}
	if binding != nil && !slices.Contains(path, binding.ExternalRef) {
		return assistantwire.ExternalEffectivePolicy{}, nil, externalsourcestore.ErrBindingNotFound
	}
	resolved, err := externalbindingadapt.Resolve(path[0], policies, binding)
	return resolved, policies, err
}

func requestExternalPath(w http.ResponseWriter, r *http.Request) ([]assistantwire.ExternalRef, error) {
	refs := r.URL.Query()["external_ref"]
	var ref assistantwire.ExternalRef
	if len(refs) != 1 || len(refs[0]) > 16<<10 || jsonv2.Unmarshal([]byte(refs[0]), &ref, jsonv2.RejectUnknownMembers(true)) != nil {
		writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_ref")
		return nil, errors.New("invalid external reference")
	}
	path, err := externalbindingadapt.Path(ref)
	if err != nil {
		writeExternalSourceError(w, http.StatusBadRequest, "invalid_external_ref")
	}
	return path, err
}

func readExternalBody[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var in *T
	err := jsonv2.UnmarshalRead(http.MaxBytesReader(w, r.Body, 64<<10), &in, jsonv2.RejectUnknownMembers(true))
	if err == nil && in != nil {
		return *in, nil
	}
	if err == nil {
		err = errors.New("expected a JSON object")
	}
	writeBodyErr(w, err, i18n.Err("http-status.bad_request"))
	var zero T
	return zero, err
}

func (x externalSourceRoutes) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, externalsourcestore.ErrBindingNotFound):
		writeExternalSourceError(w, http.StatusNotFound, "external_binding_not_found")
	case errors.Is(err, db.ErrTodoNotFound):
		writeExternalSourceError(w, http.StatusNotFound, "external_target_not_found")
	case errors.Is(err, externalsourcestore.ErrBindingExists):
		writeExternalSourceError(w, http.StatusConflict, "external_binding_exists")
	default:
		x.service.logf("external sources: %v", err)
		writeExternalSourceError(w, http.StatusServiceUnavailable, "external_sources_unavailable")
	}
}

func writeExternalSourceError(w http.ResponseWriter, status int, code string) {
	apierr.WriteCode(w, status, code, errors.New(http.StatusText(status)))
}
