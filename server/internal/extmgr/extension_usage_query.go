package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/quota"
)

// The usage extension point (docs/extensions/extensions.md "Usage"): an extension contributing usage answers usage.query with the
// accounts and windows the usage bars draw (usagesource.ExtensionAnswer), reading the launcher's gateway through
// usage.http. Like quota.http, those requests are taken only while the query runs, from the extension it was asked of,
// and go out through whoever holds the launcher's key (launcherSender): the extension never sees the key.

// UsageQuery is how a usage source of type extension is read (devices.Options.UsageExtension): it asks extension id
// about the gateway of launcher and returns its answer as JSON, for usagesource.FromExtension to check.
func (h *Manager) UsageQuery(ctx context.Context, id, launcher string) (json.RawMessage, error) {
	if h == nil || h.extensions == nil {
		return nil, errors.New("no extensions")
	}
	p := h.extensions.reg.Get(id)
	if p == nil || !p.Record.Enabled || p.Problem != "" || !p.Record.Granted.LauncherKeys {
		return nil, errors.New("extension " + id + " cannot read usage")
	}
	hp := h.extensions.get(id)
	if hp == nil || hp.proc == nil {
		return nil, errors.New("extension " + id + " is not running")
	}
	gw, err := h.launcherGateway(ctx, launcher, "")
	if err != nil {
		return nil, err
	}
	root, err := quota.Root(gw.base)
	if err != nil {
		return nil, err
	}
	var item *extension.UsageItem
	for _, u := range p.Manifest.Usages() {
		if !u.Direct && (len(u.Hosts) == 0 || hostMatches(root.Hostname(), u.Hosts)) {
			item = &u
			break
		}
	}
	if item == nil {
		return nil, errors.New("extension " + id + " does not read this gateway")
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	qid := hex.EncodeToString(b[:])
	quotaQueries.Store(qid, &quotaQuery{extension: id, send: gw.send})
	defer quotaQueries.Delete(qid)
	params := map[string]string{"query_id": qid, "base_url": root.String(), "tz": h.userTZName(), "lang": string(h.contextLang(ctx))}
	return callUsageQuery(ctx, hp, params, item.ID)
}

// DirectUsageSources discovers installed and enabled contributions. Their settings version
// prevents the gateway poller from reusing samples after the extension configuration changes.
func (h *Manager) DirectUsageSources() []devices.DirectUsageSource {
	if h == nil || h.extensions == nil {
		return nil
	}
	var out []devices.DirectUsageSource
	for _, p := range h.directUsageRegistry().Snapshots() {
		if !p.Record.Enabled || p.Problem != "" || p.Manifest == nil {
			continue
		}
		for _, item := range p.Manifest.Usages() {
			if item.Direct {
				out = append(out, devices.NewDirectUsageSource(p.Record.ID, item.ID, item.Name, p.Record.UpdatedAt))
			}
		}
	}
	return out
}

// DirectUsageQuery invokes the contribution without opening a launcher-backed HTTP session.
func (h *Manager) DirectUsageQuery(ctx context.Context, source devices.DirectUsageSource) (json.RawMessage, error) {
	if h == nil || h.extensions == nil {
		return nil, errors.New("no extensions")
	}
	p := h.directUsageRegistry().SnapshotExact(source.Extension())
	if p == nil || !p.Record.Enabled || p.Problem != "" || p.Manifest == nil || !p.Record.UpdatedAt.Equal(source.UpdatedAt()) {
		return nil, errors.New("direct usage source is no longer active")
	}
	hp := h.extensions.get(source.Extension())
	if hp == nil || hp.proc == nil || !hp.recordUpdatedAt.Equal(source.UpdatedAt()) || !hp.proc.Ready() {
		return nil, errors.New("direct usage source is not ready")
	}
	for _, item := range p.Manifest.Usages() {
		if !item.Direct || item.ID != source.Contribution() {
			continue
		}
		params := map[string]string{"tz": h.userTZName(), "lang": string(h.contextLang(ctx))}
		return callUsageQuery(ctx, hp, params, item.ID)
	}
	return nil, errors.New("direct usage contribution is not enabled")
}

func callUsageQuery(ctx context.Context, hp *hubExtension, params map[string]string, contribution string) (json.RawMessage, error) {
	targeted, _ := extension.Targeted(params, contribution)
	var res json.RawMessage
	if err := hp.proc.Call(ctx, extension.MethodUsageQuery, targeted, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (h *Manager) directUsageRegistry() *extension.Registry {
	h.extensions.mu.Lock()
	defer h.extensions.mu.Unlock()
	return h.extensions.reg
}

func directUsageKeysToClear(p *extension.Extension, old, next json.RawMessage) []string {
	if p.Full == nil {
		return nil
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(old, &before)
	_ = json.Unmarshal(next, &after)
	var keys []string
	for _, item := range p.Full.Usages() {
		if item.Direct && item.RemoteKey != "" &&
			!sameOrigin(directUsageURL(before[item.RemoteAddress]), directUsageURL(after[item.RemoteAddress])) {
			keys = append(keys, item.RemoteKey)
		}
	}
	return keys
}

func directUsageKeyNeedsURL(p *extension.Extension, name string, raw json.RawMessage) error {
	if p.Full == nil {
		return nil
	}
	for _, item := range p.Full.Usages() {
		if !item.Direct || item.RemoteKey != name {
			continue
		}
		var cfg map[string]json.RawMessage
		_ = json.Unmarshal(raw, &cfg)
		if directUsageURL(cfg[item.RemoteAddress]) == "" {
			return keyNeedsURLError{i18n.Err("extension-status.direct_usage_needs_url")}
		}
	}
	return nil
}

func directUsageURL(value json.RawMessage) string {
	var raw string
	_ = json.Unmarshal(value, &raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return raw
}
