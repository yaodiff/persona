package assistant

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// executionRoutes are the four dimensions' routes (docs/assistant/execution-spec.md "Routes"):
//
//	GET    /api/v1/execution/channels              the managed channels and their rollback points (ChannelCatalogView)
//	PUT    /api/v1/execution/channels/{name}       save one (ChannelConfig, optional "revision"); {ok, channel, revision}
//	DELETE /api/v1/execution/channels/{name}       remove one (?revision=); {ok, revision}
//	POST   /api/v1/execution/channels/rollback     restore a point ({revision}, none: the newest); {ok, revision}
//	GET    /api/v1/execution/catalog               the declarations (ExecutionCatalog); ?machine= adds every alias's verdict there
//	POST   /api/v1/execution/validate              one combination's verdict (ExecutionValidateRequest → CombinationVerdict)
//
// The reads take any role; a change takes a full one. No change stops a running session.
func (s *Service) executionRoutes() {
	full := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-execution.err_role"))
				return
			}
			fn(w, r)
		}
	}
	changed := func(w http.ResponseWriter, r *http.Request, err error, out map[string]any) {
		switch {
		case errors.Is(err, errChannelRevision):
			apierr.Write(w, http.StatusConflict, channelErr(err))
		case errors.Is(err, errChannelNotFound), errors.Is(err, errChannelNoRollback):
			apierr.Write(w, http.StatusNotFound, channelErr(err))
		case errors.Is(err, errManagedLaunchersRead):
			apierr.Write(w, http.StatusInternalServerError, err)
		case err != nil:
			apierr.Write(w, http.StatusBadRequest, err)
		default:
			v, verr := s.channelCatalogView(r.Context())
			if verr != nil {
				apierr.Write(w, http.StatusInternalServerError, verr)
				return
			}
			out["ok"], out["revision"] = true, v.Revision
			apierr.WriteJSON(w, out)
		}
	}
	s.handle("GET /api/v1/execution/channels", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.channelCatalogView(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, v)
	})
	s.handle("PUT /api/v1/execution/channels/{name}", full(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			proto.ChannelConfig
			// Revision, when given, is the catalog's revision the change was made on: another change since is refused.
			Revision *int64 `json:"revision,omitempty"`
		}
		if err := decodeBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		in.Name = r.PathValue("name")
		err := s.saveChannel(r.Context(), in.ChannelConfig, in.Revision)
		out := map[string]any{}
		if err == nil {
			if catalog, rerr := s.readChannelCatalog(r.Context()); rerr == nil {
				for _, c := range catalog {
					if c.Name == strings.TrimSpace(in.Name) {
						out["channel"] = c.view()
					}
				}
			}
		}
		changed(w, r, err, out)
	}))
	s.handle("DELETE /api/v1/execution/channels/{name}", full(func(w http.ResponseWriter, r *http.Request) {
		revision, ok := queryRevision(r)
		if !ok {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-execution.err_channel_revision"))
			return
		}
		changed(w, r, s.deleteChannel(r.Context(), r.PathValue("name"), revision), map[string]any{})
	}))
	s.handle("POST /api/v1/execution/channels/rollback", full(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			// Revision is the point to restore; none: the newest.
			Revision *int64 `json:"revision,omitempty"`
		}
		if r.ContentLength != 0 {
			if err := decodeBody(w, r, &in, 1<<12); err != nil {
				writeBodyErr(w, err, err)
				return
			}
		}
		changed(w, r, s.rollbackChannels(r.Context(), in.Revision), map[string]any{})
	}))
	s.handle("GET /api/v1/execution/catalog", func(w http.ResponseWriter, r *http.Request) {
		d := s.execDecl(r.Context())
		out := d.catalog()
		if machine := strings.TrimSpace(r.URL.Query().Get("machine")); machine != "" {
			lang := s.requestLang(r)
			out.Combinations = []proto.CombinationVerdict{}
			for _, a := range d.aliases {
				out.Combinations = append(out.Combinations, d.judge(a.spec, machine).wire(lang))
			}
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("POST /api/v1/execution/validate", func(w http.ResponseWriter, r *http.Request) {
		var in proto.ExecutionValidateRequest
		if err := decodeBody(w, r, &in, 1<<14); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		v := s.execDecl(r.Context()).resolve(in.Spec, strings.TrimSpace(in.Machine))
		apierr.WriteJSON(w, v.wire(s.requestLang(r)))
	})
}

// queryRevision is the ?revision= of a change: nil when none is given, false when it is not a number.
func queryRevision(r *http.Request) (*int64, bool) {
	q := strings.TrimSpace(r.URL.Query().Get("revision"))
	if q == "" {
		return nil, true
	}
	n, err := strconv.ParseInt(q, 10, 64)
	if err != nil || n < 0 {
		return nil, false
	}
	return &n, true
}
