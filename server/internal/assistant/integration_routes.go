package assistant

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/extmgr"
)

// The projection reads the settings of the manager in this process. The request can select an owner at the hub,
// while every ref here is stamped with the identity this assistant was started with.
type integrationReads interface {
	IntegrationExtensions(context.Context) ([]assistantwire.IntegrationExtensionRead, error)
	IntegrationChannels(context.Context) ([]channel.View, error)
	IntegrationConnectors(context.Context) ([]assistantwire.IntegrationConnectorRead, error)
	IntegrationSubscriptions(context.Context) ([]assistantwire.IntegrationSubscriptionRead, error)
	IntegrationServices(context.Context) []assistantwire.IntegrationServiceRead
	IntegrationConfiguration(context.Context, string) ([]string, error)
}

var _ integrationReads = (*extmgr.Manager)(nil)

type integrationRoutes struct{ s *Service }

func (integrationRoutes) Name() seam.ModuleName { return seam.NewModuleName("integrations-projection") }

func (x integrationRoutes) Register(k seam.Kernel) {
	for _, route := range []struct {
		pattern string
		serve   http.HandlerFunc
	}{
		{assistantwire.IntegrationsCatalogRoute, x.catalog},
		{assistantwire.IntegrationsDetailRoute, x.detail},
	} {
		k.Route(seam.NewRoute(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			// The underlying settings reads are for full-access clients. A composite read keeps that boundary.
			if !k.Full(r) {
				apierr.Write(w, http.StatusForbidden, errors.New(http.StatusText(http.StatusForbidden)))
				return
			}
			route.serve(w, withRequestLang(r))
		}))
	}
}

func (x integrationRoutes) catalog(w http.ResponseWriter, r *http.Request) {
	apierr.WriteJSON(w, x.read(r.Context()).catalog())
}

func (x integrationRoutes) detail(w http.ResponseWriter, r *http.Request) {
	key := integrationKey{kind: assistantwire.IntegrationKind(r.PathValue("kind")), id: assistantwire.IntegrationResourceID(r.PathValue("id"))}
	if !key.kind.Valid() {
		apierr.Write(w, http.StatusNotFound, errors.New(http.StatusText(http.StatusNotFound)))
		return
	}
	book := x.read(r.Context())
	if node := book.nodes[book.canonical(key)]; node != nil {
		sections := book.sections(r.Context(), node, x.s.integrationReads)
		apierr.WriteJSON(w, assistantwire.IntegrationDetail{Item: book.displayItem(node), Sections: sections, Availability: book.availability})
		return
	}
	if book.failed(key.kind) {
		apierr.WriteCode(w, http.StatusServiceUnavailable, "unavailable", errors.New(http.StatusText(http.StatusServiceUnavailable)))
		return
	}
	apierr.Write(w, http.StatusNotFound, errors.New(http.StatusText(http.StatusNotFound)))
}

// Each source reads independently with a shorter deadline than the hub relay. The connector and channel readers
// pass the deadline to their status calls.
const integrationReadTimeout = 4 * time.Second

func (x integrationRoutes) read(ctx context.Context) *integrationBook {
	id := x.s.id
	if id == "" {
		id = assistantwire.AssistantID
	}
	var input integrationInput
	if source := x.s.integrationReads; source != nil {
		ctx, cancel := context.WithTimeout(ctx, integrationReadTimeout)
		defer cancel()
		var reads sync.WaitGroup
		reads.Go(func() { input.extensions, input.extensionsErr = source.IntegrationExtensions(ctx) })
		reads.Go(func() { input.channels, input.channelsErr = source.IntegrationChannels(ctx) })
		reads.Go(func() { input.connectors, input.connectorsErr = source.IntegrationConnectors(ctx) })
		reads.Go(func() { input.subscriptions, input.subscriptionsErr = source.IntegrationSubscriptions(ctx) })
		reads.Go(func() { input.services = source.IntegrationServices(ctx) })
		reads.Wait()
	} else {
		input.extensionsErr, input.channelsErr = errIntegrationRead, errIntegrationRead
		input.connectorsErr, input.subscriptionsErr = errIntegrationRead, errIntegrationRead
	}
	return newIntegrationBook(assistantwire.IntegrationManagerID(id), input)
}
