package extmgr

import (
	"context"
	"net/http"

	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// The extension manager's side of the Lark user sync (internal/modules/lark/larksync, #13404): a thin Host adapter
// and lazy lookup of the module's service behind botport. With the lark module left out or switched off, larkSync
// returns nil, no routes or connector are registered, and the scheduler loop does nothing.

var _ larksyncwire.Store = (*db.DB)(nil)

type larkSyncHost struct{ h *Manager }

func (a larkSyncHost) HasSettings() bool { return a.h.store != nil }

func (a larkSyncHost) Setting(ctx context.Context, key string, out any) (bool, error) {
	return a.h.store.Setting(ctx, key, out)
}

func (a larkSyncHost) PutSetting(ctx context.Context, key string, v any) error {
	return a.h.store.PutSetting(ctx, key, v)
}

func (a larkSyncHost) SyncStore() larksyncwire.Store {
	st, _ := a.h.store.(larksyncwire.Store)
	return st
}

func (a larkSyncHost) SealSecret(plain string) (string, error) { return a.h.sealKey(plain) }

func (a larkSyncHost) OpenSecret(sealed string) (string, error) { return a.h.openKey(sealed) }

func (a larkSyncHost) PrimaryApp(ctx context.Context) (appID, secret, domain string) {
	return a.h.botAppCredentials(ctx)
}

func (a larkSyncHost) Registrar() botport.SyncRegistrar {
	r := a.h.registrar()
	if r == nil {
		return nil
	}
	return r
}

func (a larkSyncHost) RegisteredDomain(region string) string { return a.h.registeredDomain(region) }

func (a larkSyncHost) CanWrite(r *http.Request) bool {
	p := principalOf(r.Context())
	return p.Role == roleFull && p.Extension == ""
}

func (a larkSyncHost) RunContext() context.Context { return a.h.ctx }

func (a larkSyncHost) Logf(format string, args ...any) { a.h.logf(format, args...) }

func (a larkSyncHost) BotLang(ctx context.Context) i18n.Lang { return a.h.botLang(ctx) }

func (a larkSyncHost) UserLang() i18n.Lang { return a.h.userLang() }

func (a larkSyncHost) Audit(ctx context.Context, action string, detail map[string]any) {
	a.h.auditBot(ctx, botRecord{action: action, detail: detail})
}

func (a larkSyncHost) KBKick(connectorID string, scope connectorwire.Scope) {
	a.h.kbKick(connectorID, scope)
}

func (a *larkSyncHost) EmitLarkMessages(binding string, msgs []larksyncwire.Message) {
	if a == nil || a.h == nil {
		return
	}
	a.h.emitLarkMessages(binding, msgs)
}

func (a larkSyncHost) PushLocalized(build func(i18n.Lang) map[string]any, collapseID string) {
	a.h.pushLocalized(build, collapseID)
}

func (a larkSyncHost) IsMember() bool { return a.h.isMember() }

// larkSync returns this manager's Lark user sync service, or nil when the lark module is absent or switched off.
func (h *Manager) larkSync() botport.SyncService {
	if !botport.Available(h.modules()) {
		return nil
	}
	h.bots.syncMu.Lock()
	defer h.bots.syncMu.Unlock()
	if h.bots.sync == nil {
		h.bots.sync = botport.NewSyncService(&larkSyncHost{h})
	}
	return h.bots.sync
}

func (h *Manager) larkSyncRoutes(api *http.ServeMux) {
	if svc := h.larkSync(); svc != nil {
		svc.Routes(api)
	}
}

func (h *Manager) larkSyncConnector() connectorwire.Connector {
	if svc := h.larkSync(); svc != nil {
		return svc.Connector()
	}
	return nil
}

func (h *Manager) larkSyncLoop(ctx context.Context) {
	if svc := h.larkSync(); svc != nil {
		svc.Loop(ctx)
	}
}

func (h *Manager) larkSyncPrimaryTier(ctx context.Context) string {
	if svc := h.larkSync(); svc != nil {
		return svc.PrimaryTier(ctx)
	}
	return larksyncwire.TierDocs
}
