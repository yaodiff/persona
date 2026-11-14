package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/webpush"
)

// Web Push (#7459, docs/web/web.md "Web Push"): a browser that turned it on in Settings › Notifications gets what a phone
// would be pushed (notifyWSFrom's events: cards, finished turns, reminders, decisions, notify API alerts) while no
// Persona page of it is open. Open, the page notifies by itself (web lib/attention-signals.ts), so nothing shows twice.
// The payload is encrypted to the browser (RFC 8291): its vendor's push service sees ciphertext only.

const (
	// webPushKeysSetting holds the hub's VAPID key pair (webpush.Keys), made the first time a browser asks for it.
	webPushKeysSetting = "hub.web_push.vapid"
	// webPushSubsSetting holds the subscriptions ([]webSub), written whole.
	webPushSubsSetting = "hub.web_push.subscriptions"
	// webPushMax is how many browsers a hub pushes to; one more drops the one that renewed longest ago.
	webPushMax = 32
	// webPushTTL is how long a push service keeps a push for a browser that is away: what the notify ring keeps
	// (notifyring.go), after which the card is in the lists and the news is old.
	webPushTTL = notifyRingMaxAge
	// webPushSubject is the VAPID sub claim, where a push service may reach the sender: the project, never the user.
	webPushSubject = "https://github.com/nwverse/persona"
	// webPushBrowserMax bounds the browser's name a page gives ("Chrome on macOS").
	webPushBrowserMax = 64
	// webPushErrorMax bounds what a push service's answer is kept as.
	webPushErrorMax = 200
	// webPushTimeout bounds one POST to a push service.
	webPushTimeout = 15 * time.Second
)

// webSub is one browser's subscription. Device is the browser's notification device id (its prefs, web lib/notify.ts
// deviceId); Credential the device token that made it (revoking it drops the subscription); Key the VAPID public key
// it was made with.
type webSub struct {
	Device     string    `json:"device"`
	Credential string    `json:"credential,omitempty"`
	Endpoint   string    `json:"endpoint"`
	P256DH     string    `json:"p256dh"`
	Auth       string    `json:"auth"`
	Key        string    `json:"key"`
	Locale     string    `json:"locale,omitempty"`
	Features   []string  `json:"features,omitempty"`
	Browser    string    `json:"browser,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s webSub) subscription() webpush.Subscription {
	return webpush.Subscription{Endpoint: s.Endpoint, P256DH: s.P256DH, Auth: s.Auth}
}

// webDelivery is how the last push to a browser went; in memory, as a phone's pushFailure is.
type webDelivery struct {
	OKAt     *time.Time `json:"last_ok_at,omitempty"`
	FailedAt *time.Time `json:"failed_at,omitempty"`
	Status   int        `json:"status,omitempty"`
	Error    string     `json:"error,omitempty"`
}

type webPusher struct {
	store Store
	logf  func(string, ...any)
	now   func() time.Time
	// client posts to the push services; check vets an endpoint (tests let their own server through).
	client     *http.Client
	check      func(string) (string, error)
	retryWaits []time.Duration
	// sent tells tests a push's outcome, after it is noted.
	sent func(device string, status int, err error)
	// storeTimeout bounds each settings read or write.
	storeTimeout time.Duration

	keyMu sync.Mutex // making the key: one at a time
	keys  webpush.Keys

	saveMu sync.Mutex // one write of the list at a time, the latest state
	mu     sync.Mutex
	subs   map[string]webSub // by device
	last   map[string]webDelivery
}

func newWebPusher(store Store, logf func(string, ...any)) *webPusher {
	w := &webPusher{store: store, logf: logf, now: time.Now, client: &http.Client{Timeout: webPushTimeout},
		check: webpush.CheckEndpoint, retryWaits: []time.Duration{time.Second, 4 * time.Second},
		storeTimeout: deviceStoreTimeout, subs: map[string]webSub{}, last: map[string]webDelivery{}}
	if store == nil {
		return w
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.storeTimeout)
	defer cancel()
	var list []webSub
	if _, err := store.Setting(ctx, webPushSubsSetting, &list); err != nil {
		logf("web push: subscriptions: %v", err)
	}
	for _, s := range list {
		if s.Device != "" && s.Endpoint != "" {
			w.subs[s.Device] = s
		}
	}
	var keys webpush.Keys
	if ok, err := store.Setting(ctx, webPushKeysSetting, &keys); err != nil {
		logf("web push: key: %v", err)
	} else if ok && keys.Valid() {
		w.keys = keys
	}
	return w
}

// key is the hub's VAPID key pair, made and stored the first time it is needed. A pair another hub process stored
// meanwhile (blue-green) wins over the one made here: what is stored is what is read back.
func (w *webPusher) key(ctx context.Context) (webpush.Keys, error) {
	if w == nil {
		return webpush.Keys{}, errors.New("web push: no pusher")
	}
	w.keyMu.Lock()
	defer w.keyMu.Unlock()
	if w.keys.Valid() {
		return w.keys, nil
	}
	if w.store != nil {
		var stored webpush.Keys
		if ok, err := w.store.Setting(ctx, webPushKeysSetting, &stored); err != nil {
			return webpush.Keys{}, err
		} else if ok && stored.Valid() {
			w.keys = stored
			return stored, nil
		}
	}
	made, err := webpush.NewKeys()
	if err != nil {
		return webpush.Keys{}, err
	}
	if w.store != nil {
		if err := w.store.PutSetting(ctx, webPushKeysSetting, made); err != nil {
			return webpush.Keys{}, err
		}
		var stored webpush.Keys
		if ok, err := w.store.Setting(ctx, webPushKeysSetting, &stored); err == nil && ok && stored.Valid() {
			made = stored
		}
	}
	w.keys = made
	w.logf("web push: made the VAPID key")
	return made, nil
}

// currentKey is the key without making one ("" when none was made yet).
func (w *webPusher) currentKey() string {
	if w == nil {
		return ""
	}
	w.keyMu.Lock()
	defer w.keyMu.Unlock()
	return w.keys.Public
}

// list is every subscription, in no order.
func (w *webPusher) list() []webSub {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Collect(maps.Values(w.subs))
}

func (w *webPusher) get(device string) (webSub, bool) {
	if w == nil {
		return webSub{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.subs[device]
	return s, ok
}

// save stores the list change makes of the current one, then makes it current: a write that fails changes nothing.
// change returns false to store nothing.
func (w *webPusher) save(change func(map[string]webSub) bool) error {
	w.saveMu.Lock()
	defer w.saveMu.Unlock()
	w.mu.Lock()
	next := maps.Clone(w.subs)
	w.mu.Unlock()
	if !change(next) {
		return nil
	}
	if w.store != nil {
		list := slices.SortedFunc(maps.Values(next), func(a, b webSub) int { return strings.Compare(a.Device, b.Device) })
		ctx, cancel := context.WithTimeout(context.Background(), w.storeTimeout)
		err := w.store.PutSetting(ctx, webPushSubsSetting, list)
		cancel()
		if err != nil {
			w.logf("web push: save subscriptions: %v", err)
			return err
		}
	}
	w.mu.Lock()
	for id := range w.last {
		if _, ok := next[id]; !ok {
			delete(w.last, id)
		}
	}
	for id, s := range next {
		if cur, ok := w.subs[id]; ok && cur.Endpoint != s.Endpoint {
			delete(w.last, id) // a new subscription starts clean
		}
	}
	w.subs = next
	w.mu.Unlock()
	return nil
}

// errWebPushOwned: the device's subscription is another credential's, and the request does not hold its endpoint.
var errWebPushOwned = errors.New("web push: another device's subscription")

// subscribe keeps s for its device. A subscription made with another credential is replaced only by a request that
// presents its endpoint (the same browser, signed in again); at webPushMax the one renewed longest ago goes.
func (w *webPusher) subscribe(s webSub) error {
	s.UpdatedAt = w.now().UTC()
	return w.saveErr(func(next map[string]webSub) (bool, error) {
		cur, had := next[s.Device]
		if had && cur.Credential != "" && cur.Credential != s.Credential && cur.Endpoint != s.Endpoint {
			return false, errWebPushOwned
		}
		s.CreatedAt = s.UpdatedAt
		if had && cur.Endpoint == s.Endpoint {
			s.CreatedAt = cur.CreatedAt
		}
		// One browser's subscription is one row: the same endpoint under another device id (its storage was cleared) goes.
		for id, other := range next {
			if id != s.Device && other.Endpoint == s.Endpoint {
				delete(next, id)
			}
		}
		next[s.Device] = s
		for len(next) > webPushMax {
			oldest := ""
			for id, other := range next {
				if id != s.Device && (oldest == "" || other.UpdatedAt.Before(next[oldest].UpdatedAt)) {
					oldest = id
				}
			}
			w.logf("web push: %d browsers, dropping %s", len(next), next[oldest].Browser)
			delete(next, oldest)
		}
		return true, nil
	})
}

// saveErr is save with a change that may refuse (its error, nothing stored).
func (w *webPusher) saveErr(change func(map[string]webSub) (bool, error)) error {
	var refused error
	err := w.save(func(next map[string]webSub) bool {
		ok, err := change(next)
		refused = err
		return ok && err == nil
	})
	return cmp.Or(refused, err)
}

// unsubscribe drops device's subscription for credential (or for whoever presents its endpoint). ok false: there was
// none.
func (w *webPusher) unsubscribe(device, credential, endpoint string) (bool, error) {
	found := false
	err := w.saveErr(func(next map[string]webSub) (bool, error) {
		cur, had := next[device]
		if !had {
			return false, nil
		}
		if cur.Credential != "" && cur.Credential != credential && (endpoint == "" || endpoint != cur.Endpoint) {
			return false, errWebPushOwned
		}
		found = true
		delete(next, device)
		return true, nil
	})
	return found, err
}

// signedOut drops the subscriptions a revoked credential made (Pusher.signedOut).
func (w *webPusher) signedOut(credential string) {
	if w == nil || credential == "" {
		return
	}
	_ = w.save(func(next map[string]webSub) bool {
		gone := false
		for id, s := range next {
			if s.Credential == credential {
				delete(next, id)
				gone = true
			}
		}
		return gone
	})
}

// forgetIfCurrent drops a subscription the push service says is no more, while it is still that one.
func (w *webPusher) forgetIfCurrent(s webSub) {
	dropped := false
	_ = w.save(func(next map[string]webSub) bool {
		cur, ok := next[s.Device]
		if dropped = ok && cur.Endpoint == s.Endpoint; dropped {
			delete(next, s.Device)
		}
		return dropped
	})
	if dropped {
		w.logf("web push %s: the push service says the subscription is gone; dropped", cmp.Or(s.Browser, s.Device))
	}
}

// delivery is how the last push to device went.
func (w *webPusher) delivery(device string) webDelivery {
	if w == nil {
		return webDelivery{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last[device]
}

// noted records how a push to s went (status 0 with err: the network's error).
func (w *webPusher) noted(s webSub, status int, err error) {
	at := w.now().UTC()
	w.mu.Lock()
	if cur, ok := w.subs[s.Device]; ok && cur.Endpoint == s.Endpoint {
		d := w.last[s.Device]
		if err == nil {
			d.OKAt, d.FailedAt, d.Status, d.Error = &at, nil, status, ""
		} else {
			d.FailedAt, d.Status, d.Error = &at, status, clipWebPushError(err.Error())
		}
		w.last[s.Device] = d
	}
	w.mu.Unlock()
	if w.sent != nil {
		w.sent(s.Device, status, err)
	}
}

// push posts plaintext to s, tried again after each of retryWaits while the failure may pass. It returns the push
// service's status (0: none came) and, when it did not take it, why. A subscription that is gone is dropped.
func (w *webPusher) push(ctx context.Context, s webSub, plaintext []byte, m webpush.Message) (int, error) {
	keys, err := w.key(ctx)
	if err != nil {
		w.noted(s, 0, err)
		return 0, err
	}
	if s.Key != "" && s.Key != keys.Public {
		err := errors.New("subscribed with another key")
		w.noted(s, 0, err)
		return 0, err
	}
	status, err := w.postOnce(ctx, s, keys, plaintext, m)
	for _, wait := range w.retryWaits {
		if err == nil || !webpush.Retryable(status) || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		status, err = w.postOnce(ctx, s, keys, plaintext, m)
	}
	if webpush.Gone(status) {
		w.forgetIfCurrent(s) // before noted: what it tells (tests) finds the subscription gone
		w.noted(s, status, err)
		return status, err
	}
	w.noted(s, status, err)
	if err != nil {
		w.logf("web push %s: %v", cmp.Or(s.Browser, s.Device), err)
	}
	return status, err
}

func (w *webPusher) postOnce(ctx context.Context, s webSub, keys webpush.Keys, plaintext []byte, m webpush.Message) (int, error) {
	if _, err := w.check(s.Endpoint); err != nil {
		return -1, err // not a status: an endpoint refused is not tried again
	}
	req, err := webpush.NewRequest(ctx, s.subscription(), keys, webPushSubject, plaintext, m, w.now())
	if err != nil {
		return -1, err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 == 2 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(text)))
}

// dispatch sends plaintext to s in the background, the delivery bounded by webPushTimeout and its retries.
func (w *webPusher) dispatch(s webSub, plaintext []byte, m webpush.Message) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), webPushTimeout*time.Duration(len(w.retryWaits)+1)+10*time.Second)
		defer cancel()
		_, _ = w.push(ctx, s, plaintext, m)
	}()
}

// webPushNotify sends a notify event (notifyWSFrom's msgIn) to every subscribed browser with no Persona page open
// now that has the capability it requires and whose settings take it (wantsOn, the decision a socket gets: manager
// mutes, the "Needs you" and "Done" choices, quiet hours, posted without a sound inside them). Called under h.mu by
// notifyWSFrom: what is sent goes in the background.
func (h *Hub) webPushNotify(kind string, required []string, wantsOn func(device string) (ok, quiet bool), msgIn func(i18n.Lang) map[string]any) {
	w := h.push.webPusher()
	subs := w.list()
	if len(subs) == 0 {
		return
	}
	open := h.openWebDevicesLocked()
	for _, s := range subs {
		if open[s.Device] || s.Credential != "" && open[credentialKey(s.Credential)] {
			continue
		}
		if len(required) > 0 && !slices.Contains(s.Features, required[0]) {
			continue
		}
		ok, quiet := wantsOn(s.Device)
		if !ok {
			continue
		}
		msg := webPushMessage(kind, msgIn(webPushLang(s, h.userLang)), quiet)
		plain, err := fitWebPush(msg)
		if err != nil {
			h.logf("web push %s: %v", cmp.Or(s.Browser, s.Device), err)
			continue
		}
		w.dispatch(s, plain, webpush.Message{TTL: webPushTTL, Urgency: webPushUrgency(kind), Topic: webpush.TopicFor(webPushTag(msg))})
	}
}

// credentialKey keys a connection by its credential in openWebDevicesLocked, apart from device ids.
func credentialKey(credential string) string { return "credential\x00" + credential }

// openWebDevicesLocked is every device with a connection now: by the device id its client_hello named, else by its
// credential (a page from before it named one). h.mu is held.
func (h *Hub) openWebDevicesLocked() map[string]bool {
	open := map[string]bool{}
	for c := range h.clients {
		if id := c.helloDevice(); id != "" {
			open[id] = true
		} else if c.device != "" {
			open[credentialKey(c.device)] = true
		}
	}
	return open
}

// helloDevice is the device id c's client_hello named ("": none).
func (c *client) helloDevice() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.meta.DeviceID)
}

// webPushLang is the language s's pushes are written in: the one it subscribed in, else the user's.
func webPushLang(s webSub, user func() i18n.Lang) i18n.Lang {
	if l := i18n.Lang(s.Locale); i18n.Valid(l) {
		return l
	}
	return user()
}

// webPushMessage is what the service worker is given for a notify event (web lib/sw-push.ts reads it): the words the
// hub writes for a phone, what the notification is about, and whether it comes without a sound.
func webPushMessage(kind string, notify map[string]any, quiet bool) map[string]any {
	msg := map[string]any{"v": 1, "kind": kind}
	for _, k := range []string{"title", "subtitle", "body", "machine_id", "session_id", "interaction_id", "id", "recording_id"} {
		if v, ok := notify[k]; ok && v != nil && v != "" {
			msg[k] = v
		}
	}
	if todo := todoID(notify["todo"]); todo != "" {
		msg["todo_id"] = todo
	}
	if quiet {
		msg["quiet"] = true
	}
	return msg
}

// todoID is the id of a decision's to-do as notifyMessage carries it ("" without one).
func todoID(v any) string {
	b, err := json.Marshal(v)
	if err != nil || v == nil {
		return ""
	}
	var t struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &t)
	return t.ID
}

// webPushTag is the tag the service worker shows msg under, the one the page gives the same event (web
// lib/attention-signals.ts notify): `<machine>/<session>#needs` or `#finished`, `card:<id>#needs` for a card no session
// has, `todo:<id>`, `alert:<id>`; "" for one that replaces nothing. The Topic is made from it, so pushes waiting at the
// push service replace each other the way the notifications do.
func webPushTag(msg map[string]any) string {
	str := func(k string) string { s, _ := msg[k].(string); return s }
	mid, sid, ix := str("machine_id"), str("session_id"), str("interaction_id")
	switch str("kind") {
	case "interaction", "reply":
		if sid != "" {
			return mid + "/" + sid + "#needs"
		}
		if ix != "" {
			return "card:" + ix + "#needs"
		}
	case "done":
		if sid != "" {
			return mid + "/" + sid + "#finished"
		}
	case "assistant":
		if t := str("todo_id"); t != "" {
			return "todo:" + t
		}
	}
	if id := str("id"); id != "" {
		return "alert:" + id
	}
	return ""
}

// webPushUrgency: something waiting on the user goes at once, even to a browser saving battery.
func webPushUrgency(kind string) string {
	switch kind {
	case "interaction", "reply", "assistant":
		return webpush.UrgencyHigh
	}
	return webpush.UrgencyNormal
}

// fitWebPush is msg as JSON of at most webpush.MaxPlaintext bytes: its words shortened (1000 characters, then half
// that…) until it fits; the full text is in the app.
func fitWebPush(msg map[string]any) ([]byte, error) {
	b, err := json.Marshal(msg)
	for limit := 1000; err == nil && len(b) > webpush.MaxPlaintext; limit /= 2 {
		if limit < 20 {
			return nil, fmt.Errorf("message of %d bytes does not fit", len(b))
		}
		for _, k := range []string{"title", "subtitle", "body"} {
			if s, ok := msg[k].(string); ok {
				msg[k] = clip(s, limit)
			}
		}
		b, err = json.Marshal(msg)
	}
	return b, err
}

// clipWebPushError keeps a push service's answer to one line of webPushErrorMax bytes.
func clipWebPushError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= webPushErrorMax {
		return s
	}
	cut := webPushErrorMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
