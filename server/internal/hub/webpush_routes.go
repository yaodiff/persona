package hub

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/webpush"
)

// webPushDeviceMax bounds a browser's device id (web lib/notify.ts deviceId: "web-" and a UUID).
const webPushDeviceMax = 128

// webPushTestTTL: a test push that cannot be delivered within this long is no test any more.
const webPushTestTTL = 5 * time.Minute

// webPushBody is what a page PUTs: its PushSubscription.toJSON(), the VAPID key it subscribed with, and what its
// pushes are written in and may carry.
type webPushBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Key      string   `json:"key"`
	Locale   string   `json:"locale"`
	Features []string `json:"features"`
	Browser  string   `json:"browser"`
}

// webPushStatus is a browser's subscription as GET / PUT /api/v1/push/web/{device} answer it.
type webPushStatus struct {
	Subscribed bool `json:"subscribed"`
	// Service is the push service it goes through ("FCM", "Mozilla", "Apple", "Windows"); Host its host.
	Service string `json:"service,omitempty"`
	Host    string `json:"host,omitempty"`
	Browser string `json:"browser,omitempty"`
	// Current: made with the hub's key now; false, the page subscribes again.
	Current   bool       `json:"current,omitempty"`
	Endpoint  string     `json:"endpoint,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	webDelivery
}

// webPushStatusOf is device's subscription now.
func (h *Hub) webPushStatusOf(device string) webPushStatus {
	w := h.push.webPusher()
	s, ok := w.get(device)
	if !ok {
		return webPushStatus{}
	}
	host := ""
	if u, err := url.Parse(s.Endpoint); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	created, updated := s.CreatedAt, s.UpdatedAt
	return webPushStatus{Subscribed: true, Service: webpush.Service(host), Host: host, Browser: s.Browser,
		Current: s.Key != "" && s.Key == w.currentKey(), Endpoint: s.Endpoint, CreatedAt: &created, UpdatedAt: &updated,
		webDelivery: w.delivery(device)}
}

// webPushCredential is the credential a request subscribes as: its device token, none for the hub's own token or a
// pairing token about to be traded for one.
func webPushCredential(r *http.Request) string {
	id := principalOf(r.Context()).ID
	if strings.HasPrefix(id, "pair_") {
		return ""
	}
	return id
}

// validWebPushDevice: a device id a page makes, printable and bounded.
func validWebPushDevice(id string) bool {
	if id == "" || len(id) > webPushDeviceMax {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) })
}

// webPushFeatures keeps the capabilities a push may require (notifyWSFrom's required) from what a page says.
func webPushFeatures(in []string) []string {
	var out []string
	for _, f := range in {
		if f == "elicitation" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// webPushRoutes are a browser's Web Push (#7459, docs/protocol/protocol.md "Web Push"): push delivery, the hub's own.
func (h *Hub) webPushRoutes(api *http.ServeMux) {
	// The VAPID public key a page subscribes with (PushManager.subscribe's applicationServerKey); made the first time.
	api.HandleFunc("GET /api/v1/push/web", func(w http.ResponseWriter, r *http.Request) {
		key, err := h.push.webPusher().key(r.Context())
		if err != nil {
			h.logf("web push: key: %v", err)
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		writeJSON(w, map[string]any{"key": key.Public})
	})
	api.HandleFunc("GET /api/v1/push/web/{device}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.webPushStatusOf(r.PathValue("device")))
	})
	api.HandleFunc("PUT /api/v1/push/web/{device}", func(w http.ResponseWriter, r *http.Request) {
		device := r.PathValue("device")
		if !validWebPushDevice(device) {
			writeErrCode(w, http.StatusBadRequest, "bad_device", i18n.Err("hub-webpush.err_device"))
			return
		}
		var body webPushBody
		if err := decodeJSONBody(w, r, &body, 8<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		wp := h.push.webPusher()
		if wp == nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		s := webSub{Device: device, Credential: webPushCredential(r), Endpoint: strings.TrimSpace(body.Endpoint),
			P256DH: body.Keys.P256DH, Auth: body.Keys.Auth, Key: body.Key, Locale: string(i18n.Resolve(body.Locale)),
			Features: webPushFeatures(body.Features), Browser: clip(strings.Join(strings.Fields(body.Browser), " "), webPushBrowserMax)}
		if host, err := wp.check(s.Endpoint); err != nil {
			if errors.Is(err, webpush.ErrUnknownService) {
				writeErrCode(w, http.StatusBadRequest, "unknown_push_service", i18n.Err("hub-webpush.err_service", i18n.Args{"host": hostOf(s.Endpoint, host)}))
				return
			}
			writeErrCode(w, http.StatusBadRequest, "bad_endpoint", i18n.Err("hub-webpush.err_endpoint"))
			return
		}
		if err := s.subscription().CheckKeys(); err != nil {
			writeErrCode(w, http.StatusBadRequest, "bad_keys", i18n.Err("hub-webpush.err_keys"))
			return
		}
		key, err := wp.key(r.Context())
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if s.Key != key.Public {
			// made with a key the hub no longer has (or never had): the page subscribes again with this one
			writeErrorBody(w, http.StatusConflict, mergeBody(localizedErrorBody(w, http.StatusConflict, "key_changed", i18n.Err("hub-webpush.err_key_changed")), map[string]any{"key": key.Public}))
			return
		}
		switch err := wp.subscribe(s); {
		case errors.Is(err, errWebPushOwned):
			writeErrCode(w, http.StatusForbidden, "owned", i18n.Err("hub-webpush.err_owned"))
			return
		case err != nil:
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		writeJSON(w, h.webPushStatusOf(device))
	})
	api.HandleFunc("DELETE /api/v1/push/web/{device}", func(w http.ResponseWriter, r *http.Request) {
		// the endpoint, when the page has it, proves the subscription is this browser's (signed in again since)
		wp := h.push.webPusher()
		if wp == nil {
			writeJSON(w, map[string]any{"ok": true, "removed": false})
			return
		}
		ok, err := wp.unsubscribe(r.PathValue("device"), webPushCredential(r), r.URL.Query().Get("endpoint"))
		switch {
		case errors.Is(err, errWebPushOwned):
			writeErrCode(w, http.StatusForbidden, "owned", i18n.Err("hub-webpush.err_owned"))
		case err != nil:
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
		default:
			writeJSON(w, map[string]any{"ok": true, "removed": ok})
		}
	})
	// A test push now, whether a page is open or not: what the push service answered is what the page shows.
	api.HandleFunc("POST /api/v1/push/web/{device}/test", func(w http.ResponseWriter, r *http.Request) {
		wp := h.push.webPusher()
		s, ok := wp.get(r.PathValue("device"))
		if !ok {
			writeErrCode(w, http.StatusNotFound, "not_subscribed", i18n.Err("hub-webpush.err_none"))
			return
		}
		if s.Credential != "" && s.Credential != webPushCredential(r) {
			writeErrCode(w, http.StatusForbidden, "owned", i18n.Err("hub-webpush.err_owned"))
			return
		}
		l := webPushLang(s, h.userLang)
		plain, _ := fitWebPush(map[string]any{"v": 1, "kind": "test", "title": i18n.T(l, "hub-webpush.test_title"), "body": i18n.T(l, "hub-webpush.test_body")})
		ctx, cancel := context.WithTimeout(r.Context(), webPushTimeout*time.Duration(len(wp.retryWaits)+1)+5*time.Second)
		defer cancel()
		status, err := wp.push(ctx, s, plain, webpush.Message{TTL: webPushTestTTL, Urgency: webpush.UrgencyHigh, Topic: webpush.TopicFor("webpush-test")})
		if err != nil {
			body := localizedErrorBody(w, http.StatusBadGateway, "push_failed", i18n.Err("hub-webpush.err_push", i18n.Args{"error": clipWebPushError(err.Error())}))
			writeErrorBody(w, http.StatusBadGateway, mergeBody(body, map[string]any{"push_status": status, "gone": webpush.Gone(status)}))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "push_status": status})
	})
}

// hostOf is an endpoint's host for a message ("" stays the checked one).
func hostOf(endpoint, checked string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return cmp.Or(checked, endpoint)
}

// localizedErrorBody is an error body in the request's language (apierr.WriteCode's), for one that carries more fields.
func localizedErrorBody(w http.ResponseWriter, status int, code string, err error) map[string]any {
	return apierr.LocalizedBody(status, code, err, i18n.OfWriter(w))
}

// mergeBody adds extra's fields to an error body.
func mergeBody(body, extra map[string]any) map[string]any {
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// webPushListed is one browser in GET /api/v1/devices/push (`hub doctor`).
type webPushListed struct {
	Device    string    `json:"device_id"`
	Browser   string    `json:"browser,omitempty"`
	Service   string    `json:"service,omitempty"`
	Current   bool      `json:"current"`
	UpdatedAt time.Time `json:"updated_at"`
	webDelivery
}

// webPushList is every subscribed browser, renewed last first.
func (h *Hub) webPushList() []webPushListed {
	out := []webPushListed{}
	w := h.push.webPusher()
	for _, s := range w.list() {
		st := h.webPushStatusOf(s.Device)
		out = append(out, webPushListed{Device: s.Device, Browser: s.Browser, Service: st.Service, Current: st.Current, UpdatedAt: s.UpdatedAt, webDelivery: st.webDelivery})
	}
	slices.SortFunc(out, func(a, b webPushListed) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return out
}
