package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func waitUntil(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %s", desc)
}

type pushRevokeRaceStore struct {
	*meshStore
	mu           sync.Mutex
	onUpsert     func(db.Device) error
	onSetPrefs   func(id, credential string) error
	onPutSetting func(key string) error
	onDeleteHook func(id string)
}

func (s *pushRevokeRaceStore) UpsertDevice(ctx context.Context, d db.Device) error {
	s.mu.Lock()
	hook := s.onUpsert
	s.mu.Unlock()
	if hook != nil {
		if err := hook(d); err != nil {
			return err
		}
	}
	return s.meshStore.UpsertDevice(ctx, d)
}

func (s *pushRevokeRaceStore) SetDevicePrefs(ctx context.Context, id, credential string, prefs json.RawMessage) error {
	s.mu.Lock()
	hook := s.onSetPrefs
	s.mu.Unlock()
	if hook != nil {
		if err := hook(id, credential); err != nil {
			return err
		}
	}
	return s.meshStore.SetDevicePrefs(ctx, id, credential, prefs)
}

func (s *pushRevokeRaceStore) PutSetting(ctx context.Context, key string, v any) error {
	s.mu.Lock()
	hook := s.onPutSetting
	s.mu.Unlock()
	if hook != nil {
		if err := hook(key); err != nil {
			return err
		}
	}
	return s.meshStore.PutSetting(ctx, key, v)
}

func (s *pushRevokeRaceStore) DeleteDevice(ctx context.Context, id string) error {
	s.mu.Lock()
	hook := s.onDeleteHook
	s.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return s.meshStore.DeleteDevice(ctx, id)
}

func newPushRevokeTestHub(t *testing.T) (*Hub, *pushRevokeRaceStore) {
	t.Helper()
	h := newTestHub(t)
	store := &pushRevokeRaceStore{meshStore: newMeshStore()}
	h.store = store
	h.push.store = store
	h.push.web.store = store
	return h, store
}

func issueTestDeviceToken(t *testing.T, store Store, id, rawTok string) {
	t.Helper()
	if err := store.CreateDeviceToken(context.Background(), db.DeviceToken{
		ID:   id,
		Name: "Phone " + id,
		Kind: "phone",
		Role: roleFull,
	}, hashToken(rawTok)); err != nil {
		t.Fatalf("CreateDeviceToken(%s): %v", id, err)
	}
}

func storeHasDevice(t *testing.T, store Store, deviceID string) (db.Device, bool) {
	t.Helper()
	list, err := store.Devices(context.Background())
	if err != nil {
		t.Fatalf("store.Devices: %v", err)
	}
	for _, d := range list {
		if d.DeviceID == deviceID {
			return d, true
		}
	}
	return db.Device{}, false
}

// An in-flight POST /api/v1/devices authenticated before revocation must not leave a revoked
// credential registered in either memory or the database once revocation completes.
func TestPushRegisterInFlightDuringRevocationDoesNotResurrectDevice(t *testing.T) {
	h, store := newPushRevokeTestHub(t)
	const (
		credID = "dev_revoked_reg"
		rawTok = "jd_revoked_reg_token_0001"
	)
	issueTestDeviceToken(t, store, credID, rawTok)

	upsertEntered := make(chan struct{})
	releaseUpsert := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseUpsert) }) }
	t.Cleanup(unblock)

	store.mu.Lock()
	store.onUpsert = func(d db.Device) error {
		if d.DeviceID == "phone-1" {
			enterOnce.Do(func() { close(upsertEntered) })
			<-releaseUpsert
		}
		return nil
	}
	store.mu.Unlock()

	mux := h.routes()
	const body = `{"device_id":"phone-1","name":"Phone","kind":"phone","apns_token":"aabbccdd","relay_url":"https://relay.example.com","relay_grant":"grant-1","push_key":"key-1"}`
	reqDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+rawTok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		reqDone <- w.Code
	}()

	select {
	case <-upsertEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("POST /api/v1/devices did not reach UpsertDevice")
	}

	// Revoke the device token while UpsertDevice is in flight.
	revokeDone := make(chan struct{})
	go func() {
		defer close(revokeDone)
		_, _ = store.RevokeDeviceToken(context.Background(), credID)
		h.revoked(credID)
	}()

	waitUntil(t, "revocation marked in h.devtok", func() bool {
		return h.isDeviceRevoked(credID)
	})
	unblock()

	var status int
	select {
	case status = <-reqDone:
	case <-time.After(3 * time.Second):
		t.Fatal("POST /api/v1/devices did not complete")
	}
	select {
	case <-revokeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("h.revoked did not complete")
	}

	if status != http.StatusUnauthorized {
		t.Fatalf("POST /api/v1/devices status = %d, want 401", status)
	}
	if _, ok := h.push.device("phone-1"); ok {
		t.Fatal("revoked device remained in h.push.devices")
	}
	if d, ok := storeHasDevice(t, store, "phone-1"); ok {
		t.Fatalf("revoked device remained in store: %+v", d)
	}
}

// An in-flight PUT /api/v1/devices/{id}/prefs (e.g. a browser that has not registered a push token)
// authenticated before revocation must not create or keep a revoked credential's entry in h.push.devices
// or the database after revocation completes.
func TestPushPrefsUpdateInFlightDuringRevocationDoesNotResurrectDevice(t *testing.T) {
	h, store := newPushRevokeTestHub(t)
	const (
		credID = "dev_revoked_prefs"
		rawTok = "jd_revoked_prefs_token_0001"
	)
	issueTestDeviceToken(t, store, credID, rawTok)

	prefsEntered := make(chan struct{})
	releasePrefs := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releasePrefs) }) }
	t.Cleanup(unblock)

	store.mu.Lock()
	store.onSetPrefs = func(id, credential string) error {
		if id == "browser-prefs" {
			enterOnce.Do(func() { close(prefsEntered) })
			<-releasePrefs
		}
		return nil
	}
	store.mu.Unlock()

	mux := h.routes()
	reqDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/devices/browser-prefs/prefs", strings.NewReader(`{"input":true}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+rawTok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		reqDone <- w.Code
	}()

	select {
	case <-prefsEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("PUT /prefs did not reach SetDevicePrefs")
	}

	revokeDone := make(chan struct{})
	go func() {
		defer close(revokeDone)
		_, _ = store.RevokeDeviceToken(context.Background(), credID)
		h.revoked(credID)
	}()

	waitUntil(t, "revocation marked in h.devtok", func() bool {
		return h.isDeviceRevoked(credID)
	})
	unblock()

	var status int
	select {
	case status = <-reqDone:
	case <-time.After(3 * time.Second):
		t.Fatal("PUT /prefs did not complete")
	}
	select {
	case <-revokeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("h.revoked did not complete")
	}

	if status != http.StatusUnauthorized {
		t.Fatalf("PUT /prefs status = %d, want 401", status)
	}
	if d, ok := h.push.device("browser-prefs"); ok {
		t.Fatalf("revoked device resurrected via prefs in h.push.devices: %+v", d)
	}
	if d, ok := storeHasDevice(t, store, "browser-prefs"); ok {
		t.Fatalf("revoked device resurrected via prefs in store: %+v", d)
	}
}

// When Postgres rejects a revoked credential's UpsertDevice or SetDevicePrefs with
// db.ErrPushRegistrationConflict, the routes must answer 401 rather than 409.
func TestPushConflictForRevokedCredentialReturnsUnauthorized(t *testing.T) {
	h, store := newPushRevokeTestHub(t)
	const (
		credID = "dev_conflict_revoked"
		rawTok = "jd_conflict_revoked_token_0001"
	)
	issueTestDeviceToken(t, store, credID, rawTok)
	revokeDone1 := make(chan struct{})
	var revokeOnce1 sync.Once
	store.mu.Lock()
	store.onUpsert = func(db.Device) error {
		revokeOnce1.Do(func() {
			go func() {
				defer close(revokeDone1)
				h.revoked(credID)
			}()
		})
		waitUntil(t, "credID marked revoked during UpsertDevice conflict", func() bool {
			return h.isDeviceRevoked(credID)
		})
		return db.ErrPushRegistrationConflict
	}
	store.mu.Unlock()

	mux := h.routes()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"device_id":"phone-c","name":"Phone","kind":"phone"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+rawTok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	select {
	case <-revokeDone1:
	case <-time.After(3 * time.Second):
		t.Fatal("h.revoked(credID) did not complete")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/v1/devices conflict after revoke = %d, want 401", w.Code)
	}

	// Verify PUT /prefs conflict after concurrent revocation with a second credential.
	const (
		credID2 = "dev_conflict_revoked_2"
		rawTok2 = "jd_conflict_revoked_token_0002"
	)
	issueTestDeviceToken(t, store, credID2, rawTok2)
	revokeDone2 := make(chan struct{})
	var revokeOnce2 sync.Once
	store.mu.Lock()
	store.onSetPrefs = func(string, string) error {
		revokeOnce2.Do(func() {
			go func() {
				defer close(revokeDone2)
				h.revoked(credID2)
			}()
		})
		waitUntil(t, "credID2 marked revoked during SetDevicePrefs conflict", func() bool {
			return h.isDeviceRevoked(credID2)
		})
		return db.ErrPushRegistrationConflict
	}
	store.mu.Unlock()

	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/devices/phone-c/prefs", strings.NewReader(`{"input":true}`))
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.Header.Set("Authorization", "Bearer "+rawTok2)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	select {
	case <-revokeDone2:
	case <-time.After(3 * time.Second):
		t.Fatal("h.revoked(credID2) did not complete")
	}
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("PUT /prefs conflict after revoke = %d, want 401", w2.Code)
	}
}

// signedOut for an old credential must not delete a device registration that has already been
// re-registered under a newer credential for the same device_id.
func TestSignedOutDoesNotClobberNewerCredentialForSameDeviceID(t *testing.T) {
	for range 8 {
		h, store := newPushRevokeTestHub(t)
		if err := h.push.register(Device{
			DeviceID: "phone-1", Name: "Old", Kind: "phone",
			APNSToken: "tok-old", Credential: "cred_old", CredentialID: "cred_old",
		}); err != nil {
			t.Fatal(err)
		}
		upsertEntered := make(chan struct{})
		releaseUpsert := make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(releaseUpsert) }) }

		store.mu.Lock()
		store.onUpsert = func(d db.Device) error {
			if d.Credential == "cred_new" {
				enterOnce.Do(func() { close(upsertEntered) })
				<-releaseUpsert
			}
			return nil
		}
		store.mu.Unlock()

		regDone := make(chan error, 1)
		go func() {
			regDone <- h.push.register(Device{
				DeviceID: "phone-1", Name: "New", Kind: "phone",
				APNSToken: "tok-new", Credential: "cred_new", CredentialID: "cred_new",
			})
		}()

		select {
		case <-upsertEntered:
		case <-time.After(3 * time.Second):
			unblock()
			t.Fatal("new registration did not enter UpsertDevice")
		}

		// Start signedOut("cred_old") while cred_new holds saveMu (before cred_new updates p.devices).
		signedOutStarted := make(chan struct{})
		signedOutDone := make(chan struct{})
		go func() {
			defer close(signedOutDone)
			close(signedOutStarted)
			h.push.signedOut("cred_old")
		}()

		<-signedOutStarted
		for range 32 {
			runtime.Gosched()
		}
		unblock()

		if err := <-regDone; err != nil {
			t.Fatalf("new registration failed: %v", err)
		}
		select {
		case <-signedOutDone:
		case <-time.After(3 * time.Second):
			t.Fatal("signedOut did not complete")
		}

		got, ok := h.push.device("phone-1")
		if !ok {
			t.Fatal("signedOut(cred_old) clobbered cred_new's registration in h.push.devices")
		}
		if got.Credential != "cred_new" || got.APNSToken != "tok-new" {
			t.Fatalf("unexpected device after signedOut(cred_old): %+v", got)
		}
		stored, ok := storeHasDevice(t, store, "phone-1")
		if !ok || stored.Credential != "cred_new" {
			t.Fatalf("signedOut(cred_old) clobbered cred_new in store: ok=%v stored=%+v", ok, stored)
		}
	}
}

// webPusher.subscribe must refuse a credential that has already been revoked.
func TestWebPushSubscribeRejectsRevokedCredential(t *testing.T) {
	h, _ := newPushRevokeTestHub(t)
	h.revoked("cred_web_revoked")
	err := h.push.web.subscribe(webSub{
		Device:     "web-1",
		Credential: "cred_web_revoked",
		Endpoint:   "https://fcm.googleapis.com/revoked",
	})
	if !errors.Is(err, errCredentialRevoked) {
		t.Fatalf("webPusher.subscribe err = %v, want %v", err, errCredentialRevoked)
	}
	if _, ok := h.push.web.get("web-1"); ok {
		t.Fatal("revoked credential's web push subscription was stored")
	}
}

// Revoking a credential while webPusher.subscribe's PutSetting is in flight must never publish the
// revoked subscription to w.subs, must roll back the persisted setting to the prior list, and must
// preserve the oldest valid browser subscription when len(subs) was at webPushMax.
func TestWebPushSubscribeInFlightDuringRevocationRollsBackWithoutPublishing(t *testing.T) {
	h, store := newPushRevokeTestHub(t)
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for i := range webPushMax {
		ts := base.Add(time.Duration(i) * time.Minute)
		h.push.web.now = func() time.Time { return ts }
		if err := h.push.web.subscribe(webSub{
			Device:     fmt.Sprintf("web-%02d", i),
			Credential: fmt.Sprintf("cred-valid-%02d", i),
			Endpoint:   fmt.Sprintf("https://fcm.googleapis.com/valid-%02d", i),
		}); err != nil {
			t.Fatalf("seed web-%02d: %v", i, err)
		}
	}
	h.push.web.now = func() time.Time { return base.Add(time.Hour) }

	putEntered := make(chan struct{})
	releasePut := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releasePut) }) }
	t.Cleanup(unblock)

	store.mu.Lock()
	store.onPutSetting = func(key string) error {
		if key == webPushSubsSetting {
			enterOnce.Do(func() { close(putEntered) })
			<-releasePut
		}
		return nil
	}
	store.mu.Unlock()

	subDone := make(chan error, 1)
	go func() {
		subDone <- h.push.web.subscribe(webSub{
			Device:     "web-revoked-new",
			Credential: "cred_web_inflight",
			Endpoint:   "https://fcm.googleapis.com/revoked-new",
		})
	}()

	select {
	case <-putEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe did not reach PutSetting")
	}

	revokeDone := make(chan struct{})
	go func() {
		defer close(revokeDone)
		h.revoked("cred_web_inflight")
	}()
	waitUntil(t, "web revocation marked in h.devtok", func() bool {
		return h.isDeviceRevoked("cred_web_inflight")
	})

	// Clear hook before releasing so rollback PutSetting proceeds cleanly.
	store.mu.Lock()
	store.onPutSetting = nil
	store.mu.Unlock()
	unblock()

	var err error
	select {
	case err = <-subDone:
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe did not complete")
	}
	select {
	case <-revokeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("h.revoked did not complete")
	}

	if !errors.Is(err, errCredentialRevoked) {
		t.Fatalf("subscribe err = %v, want %v", err, errCredentialRevoked)
	}
	if _, ok := h.push.web.get("web-revoked-new"); ok {
		t.Fatal("revoked web push subscription leaked into w.subs")
	}
	if _, ok := h.push.web.get("web-00"); !ok {
		t.Fatal("oldest valid web push subscription web-00 was evicted by a revoked subscribe")
	}
	if got := len(h.push.web.list()); got != webPushMax {
		t.Fatalf("len(w.list()) = %d, want %d", got, webPushMax)
	}

	// Verify persisted setting rolled back to prev (contains web-00, excludes web-revoked-new).
	var persisted []webSub
	ok, serr := store.Setting(context.Background(), webPushSubsSetting, &persisted)
	if serr != nil || !ok {
		t.Fatalf("store.Setting: ok=%v err=%v", ok, serr)
	}
	if len(persisted) != webPushMax {
		t.Fatalf("persisted web push count = %d, want %d", len(persisted), webPushMax)
	}
	var sawOldest, sawRevoked bool
	for _, s := range persisted {
		if s.Device == "web-00" {
			sawOldest = true
		}
		if s.Device == "web-revoked-new" {
			sawRevoked = true
		}
	}
	if !sawOldest || sawRevoked {
		t.Fatalf("persisted rollback mismatch: sawOldest=%v sawRevoked=%v", sawOldest, sawRevoked)
	}
}
