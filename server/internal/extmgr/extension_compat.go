package extmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The plugin → extension rename (docs/extensions/extensions.md "Naming and compatibility", #1844): the hub's routes live under
// /api/v1/extensions/… and /api/v1/admin/extensions…, answering with the new keys (extensions, extension,
// extension_name). The apps already out there still call /api/v1/plugins/… and /api/v1/admin/plugins…: those paths are
// rewritten to the new ones and answered with the keys they have always read, and every such call is counted, so the
// compatibility layer can go once nobody calls it (kept at least until 2027-05-05).

// legacyExtensionsKey marks a request that came in under an old path.
type legacyExtensionsKey struct{}

// legacyExtensionRequest says whether r came in under /api/v1/plugins or /api/v1/admin/plugins.
func legacyExtensionRequest(r *http.Request) bool {
	v, _ := r.Context().Value(legacyExtensionsKey{}).(bool)
	return v
}

// extKey is current, or legacy for a request under an old path.
func extKey(r *http.Request, current, legacy string) string {
	if legacyExtensionRequest(r) {
		return legacy
	}
	return current
}

// legacyExtensionRoutes answers from…, from/… by rewriting them to to…, to/… on mux.
func (h *Manager) legacyExtensionRoutes(mux *http.ServeMux, from, to string) {
	rewrite := func(w http.ResponseWriter, r *http.Request) {
		route := h.legacyCalls.add(r.Method, r.URL.Path, from, h.logf)
		if h.extensions != nil { // kept with the extensions' counts, so they outlast a restart (extension_metrics.go)
			h.extensions.stats.of(legacyStatsID).Count(route, 1)
		}
		r2 := r.Clone(context.WithValue(r.Context(), legacyExtensionsKey{}, true))
		r2.URL.Path = to + strings.TrimPrefix(r.URL.Path, from)
		if r.URL.RawPath != "" {
			r2.URL.RawPath = to + strings.TrimPrefix(r.URL.RawPath, from)
		}
		mux.ServeHTTP(w, r2)
	}
	mux.HandleFunc(from, rewrite)
	mux.HandleFunc(from+"/", rewrite)
}

// legacyCalls counts the calls under the old paths, by route; once a day (on the first call after it) the hub logs
// the counts of the past day.
type legacyCalls struct {
	mu     sync.Mutex
	since  time.Time
	window map[string]int64 // since since
	total  map[string]int64 // since the hub started
}

// add counts one call of method path under prefix; the route it was counted as.
func (c *legacyCalls) add(method, path, prefix string, logf func(string, ...any)) string {
	route := method + " " + legacyRoute(path, prefix)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window == nil {
		c.window, c.total, c.since = map[string]int64{}, map[string]int64{}, now
	}
	if now.Sub(c.since) >= 24*time.Hour {
		if logf != nil {
			logf("extensions: old plugin API calls since %s: %s", c.since.Format(time.RFC3339), countsLine(c.window))
		}
		c.window, c.since = map[string]int64{}, now
	}
	c.window[route]++
	c.total[route]++
	return route
}

// Snapshot is the calls under the old paths since the hub started, by route.
func (c *legacyCalls) Snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.total))
	for k, v := range c.total {
		out[k] = v
	}
	return out
}

func countsLine(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
		b.WriteString(" ×")
		b.WriteString(strconv.FormatInt(m[k], 10))
	}
	return b.String()
}

// legacyRoute is path under prefix with its ids left out: /api/v1/plugins/{id}/-/ui/…, /api/v1/plugins/-/markets/….
func legacyRoute(path, prefix string) string {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return prefix
	}
	seg := strings.Split(rest, "/")
	out := []string{prefix}
	switch {
	case seg[0] == "-":
		out = append(out, "-")
		if len(seg) > 1 {
			out = append(out, seg[1])
		}
		if len(seg) > 2 {
			out = append(out, "…")
		}
	case len(seg) > 1 && seg[1] == "-":
		out = append(out, "{id}", "-")
		if len(seg) > 2 && seg[2] != "" {
			out = append(out, seg[2])
		}
		if len(seg) > 3 {
			out = append(out, "…")
		}
	case len(seg) == 1:
		out = append(out, "{id}")
	default: // relayed to the extension
		out = append(out, "{id}", "…")
	}
	return strings.Join(out, "/")
}

// legacyUIPlace is UIPlace as the old paths have always answered it.
type legacyUIPlace struct {
	Extension     string           `json:"plugin"`
	ExtensionName string           `json:"plugin_name,omitempty"`
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	Placement     string           `json:"placement"`
	Size          string           `json:"size,omitempty"`
	Icon          string           `json:"icon,omitempty"`
	Opens         string           `json:"opens,omitempty"`
	Badge         *extension.Badge `json:"badge,omitempty"`
	Connector     string           `json:"connector,omitempty"`
	// Group as on the new path: an app old enough for these keys reads no group and ignores it
	Group *extension.UIGroup `json:"group,omitempty"`
	// IconColor as on the new path; such an app ignores it too
	IconColor string `json:"icon_color,omitempty"`
}

func legacyUIPlaces(us []UIPlace) []legacyUIPlace {
	out := make([]legacyUIPlace, 0, len(us))
	for _, u := range us {
		if extension.PageSlot(u.Placement) {
			u.Placement = "plugin_page" // the name the apps out there ask for
		}
		out = append(out, legacyUIPlace(u))
	}
	return out
}

// uiPlacesOut is us for r: the old keys under an old path.
func uiPlacesOut(r *http.Request, us []UIPlace) any {
	if legacyExtensionRequest(r) {
		return legacyUIPlaces(us)
	}
	return us
}

// legacyDetail is ExtensionDetail as the old paths have always answered it: its ui with the old keys.
type legacyDetail struct {
	ExtensionDetail
	UI []legacyUIPlace `json:"ui,omitempty"`
}

// detailOut is d for r: the old keys under an old path. Entry-list editors require an explicit capability opt-in;
// raw configuration stays intact, but older clients cannot turn object entries into destructive string-list controls.
func detailOut(r *http.Request, d ExtensionDetail) any {
	if r.URL.Query().Get("entry_lists") != "1" {
		if d.Form != nil {
			form := *d.Form
			form.Fields = slices.DeleteFunc(slices.Clone(d.Form.Fields), func(f extension.Field) bool { return f.EntryList() })
			d.Form = &form
		}
		d.Secrets = slices.DeleteFunc(slices.Clone(d.Secrets), func(s ExtensionSecretView) bool { return s.List != "" })
	}
	if !legacyExtensionRequest(r) {
		return d
	}
	l := legacyDetail{ExtensionDetail: d}
	if len(d.UI) > 0 {
		l.UI = legacyUIPlaces(d.UI)
	}
	return l
}

// legacyMarketView is MarketView as the old paths have always answered it.
type legacyMarketView struct {
	ID                 string              `json:"id"`
	Spec               string              `json:"spec"`
	Name               string              `json:"name"`
	Description        string              `json:"description,omitempty"`
	Homepage           string              `json:"homepage,omitempty"`
	DownloadsUpdatedAt string              `json:"downloads_updated_at,omitempty"`
	HasToken           bool                `json:"has_token"`
	Extensions         int                 `json:"plugins"`
	Warnings           []string            `json:"warnings"`
	Error              string              `json:"error,omitempty"`
	FetchedAt          *time.Time          `json:"fetched_at,omitempty"`
	AddedAt            time.Time           `json:"added_at"`
	Official           bool                `json:"official,omitempty"`
	Signature          MarketSignatureView `json:"signature"`
	TrustedKeys        map[string]string   `json:"trusted_keys,omitempty"`
}

// marketOut is v for r: the old keys under an old path.
func marketOut(r *http.Request, v MarketView) any {
	var out any = v
	if legacyExtensionRequest(r) {
		out = legacyMarketView(v)
	}
	if l := i18n.FromRequest(r); l != "" {
		if raw, err := json.Marshal(out); err == nil {
			return json.RawMessage(i18n.ExtensionClientJSON(l, raw))
		}
	}
	return out
}

// marketsOut is vs for r.
func marketsOut(r *http.Request, vs []MarketView) any {
	out := make([]any, 0, len(vs))
	for _, v := range vs {
		out = append(out, marketOut(r, v))
	}
	return out
}

// broadcastExtension sends an event to the apps under its new type (extension_…, with "extension") and, for the apps
// out there, its old one (plugin_…, with "plugin").
func (h *Manager) broadcastExtension(kind, id string, fields map[string]any) {
	current := map[string]any{"type": "extension_" + kind, "extension": id}
	legacy := map[string]any{"type": "plugin_" + kind, "plugin": id}
	for k, v := range fields {
		current[k], legacy[k] = v, v
	}
	h.broadcast(legacy)
	h.broadcast(current)
}

// extensionsRoot is where the extensions are kept under the hub's data directory: extensions/. A hub that kept them
// in plugins/ (before #1844) has that directory moved there once, with plugins left as a link to it, so a socket
// path set up by hand (ssh -R onto ~/.persona/plugins/<id>.sock) and an older binary rolled back to still find them.
// The move never stops the hub: when it fails, or both directories are there, the old one stays in use.
func extensionsRoot(home string, logf func(string, ...any)) string {
	cur, old := filepath.Join(home, "extensions"), filepath.Join(home, "plugins")
	fi, err := os.Lstat(old)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return cur // a new hub, or moved already
	}
	if _, err := os.Lstat(cur); err == nil {
		logf("extensions: both %s and %s are there: still using %s (move it to %s by hand)", old, cur, old, cur)
		return old
	}
	if err := os.Rename(old, cur); err != nil {
		logf("extensions: could not move %s to %s: %v (still using it)", old, cur, err)
		return old
	}
	if err := os.Symlink("extensions", old); err != nil {
		logf("extensions: moved %s to %s, but could not leave a link: %v", old, cur, err)
	} else {
		logf("extensions: moved %s to %s (the old path is a link to it)", old, cur)
	}
	return cur
}

// withExtensionEnv gives every PERSONA_PLUGIN_<NAME> in env also as PERSONA_EXTENSION_<NAME> (the host adds the
// PERSONA_ spellings of both): extensions written before #1844 read the old names, new ones the new.
func withExtensionEnv(env []string) []string {
	out := slices.Clone(env)
	for _, e := range env {
		if rest, ok := strings.CutPrefix(e, "PERSONA_PLUGIN_"); ok {
			out = append(out, "PERSONA_EXTENSION_"+rest)
		}
	}
	return out
}

// extensionConfigs are hub.json's extensions: "extensions", then the old "plugins" (an id in both is taken from
// "extensions"). Legacy entries warn once at manager startup and keep this merge behavior for one release.
func (c *Config) extensionConfigs(logf func(string, ...any)) []ExtensionConfig {
	if len(c.LegacyExtensions) > 0 {
		logf("manager.json: plugins is deprecated; use extensions instead (plugins will stop being read after the one-release warning window)")
	}
	out := slices.Clone(c.Extensions)
	for _, e := range c.LegacyExtensions {
		if slices.ContainsFunc(out, func(x ExtensionConfig) bool { return x.ID == e.ID }) {
			logf("manager.json: extension %q is in both extensions and plugins: using the one in extensions", e.ID)
			continue
		}
		out = append(out, e)
	}
	return out
}

// legacyAdminExtensions is AdminExtensions as GET /api/v1/admin/plugins has always answered it.
type legacyAdminExtensions struct {
	Extensions []any `json:"plugins"`
	CanInstall bool  `json:"can_install"`
	UploadMost int64 `json:"upload_most"`
}
