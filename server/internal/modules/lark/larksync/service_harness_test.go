package larksync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larksyncwire"
	"github.com/nwverse/persona/server/internal/secretkey"
)

// The Lark user sync service's test world (service_*_test.go): a fake Host whose settings and sync tables are in
// memory, a real secretkey.Box for sealing tokens, the primary chat bot's app credentials, and fakes for the app
// registration, user OAuth and readers.

type testPrincipal struct {
	ID        string
	Role      string
	Extension string
}

type testPrincipalKey struct{}

const (
	testRoleFull   = "full"
	testRoleAnswer = "answer"
)

func withTestPrincipal(ctx context.Context, p testPrincipal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

func testPrincipalOf(ctx context.Context) testPrincipal {
	if p, ok := ctx.Value(testPrincipalKey{}).(testPrincipal); ok {
		return p
	}
	return testPrincipal{Role: testRoleFull}
}

// memSettingsStore is an in-memory settings store for tests.
type memSettingsStore struct {
	mu   sync.Mutex
	vals map[string]json.RawMessage
}

func newMemSettingsStore() *memSettingsStore {
	return &memSettingsStore{vals: map[string]json.RawMessage{}}
}

func (s *memSettingsStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	raw, ok := s.vals[key]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (s *memSettingsStore) PutSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.vals[key] = raw
	s.mu.Unlock()
	return nil
}

func (s *memSettingsStore) raw(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.vals[key])
}

// memSyncStore is memSettingsStore with the sync's tables: cursors, the dedup index and the outbox. commitErr, when
// set, fails every commit before anything is written (the transaction rolled back).
type memSyncStore struct {
	*memSettingsStore
	now func() time.Time

	lmu       sync.Mutex
	cursors   map[string]map[string]string
	index     map[string]larksyncwire.IndexRecord
	outbox    []larksyncwire.OutboxRow
	seq       int64
	commits   int
	commitErr error
}

func newMemSyncStore(now func() time.Time) *memSyncStore {
	return &memSyncStore{
		memSettingsStore: newMemSettingsStore(),
		now:              now,
		cursors:          map[string]map[string]string{},
		index:            map[string]larksyncwire.IndexRecord{},
	}
}

var _ larksyncwire.Store = (*memSyncStore)(nil)

func (s *memSyncStore) LarkSyncCursors(_ context.Context, binding string) (map[string]string, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	out := map[string]string{}
	for k, v := range s.cursors[binding] {
		out[k] = v
	}
	return out, nil
}

func (s *memSyncStore) DeleteLarkSyncCursors(_ context.Context, binding string) error {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	delete(s.cursors, binding)
	return nil
}

func (s *memSyncStore) LarkSyncIndex(_ context.Context, keys []string) (map[string]larksyncwire.IndexRecord, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	out := map[string]larksyncwire.IndexRecord{}
	for _, k := range keys {
		if r, ok := s.index[k]; ok {
			out[k] = r
		}
	}
	return out, nil
}

func (s *memSyncStore) CommitLarkSyncRun(_ context.Context, binding string, cursors map[string]string, recs []larksyncwire.IndexRecord, outbox []larksyncwire.OutboxRow) error {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	s.commits++
	if len(cursors) > 0 && s.cursors[binding] == nil {
		s.cursors[binding] = map[string]string{}
	}
	for k, v := range cursors {
		s.cursors[binding][k] = v
	}
	for _, r := range recs {
		have, ok := s.index[r.Key]
		if ok {
			if r.Revision < have.Revision {
				r.Revision, r.BodyHash = have.Revision, have.BodyHash
			}
			r.Sources = larksyncwire.MergeSources(have.Sources, r.Sources)
		}
		s.index[r.Key] = r
	}
	for _, o := range outbox {
		s.seq++
		o.Seq, o.CreatedAt = s.seq, s.now()
		s.outbox = append(s.outbox, o)
	}
	return nil
}

func (s *memSyncStore) LarkSyncOutboxAfter(_ context.Context, kind larksyncwire.OutboxKind, after int64, limit int) ([]larksyncwire.OutboxRow, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	var out []larksyncwire.OutboxRow
	for _, o := range s.outbox {
		if o.Kind == kind && o.Seq > after && len(out) < limit {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *memSyncStore) LarkSyncDocsSince(_ context.Context, since time.Time) ([]larksyncwire.OutboxRow, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	latest := map[string]larksyncwire.OutboxRow{}
	for _, o := range s.outbox {
		if o.Kind == larksyncwire.OutboxDoc && !o.CreatedAt.Before(since) {
			latest[o.CanonicalKey] = o
		}
	}
	var out []larksyncwire.OutboxRow
	for _, o := range latest {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b larksyncwire.OutboxRow) int { return int(a.Seq - b.Seq) })
	return out, nil
}

func (s *memSyncStore) PruneLarkSyncOutbox(_ context.Context, before time.Time) (int64, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	n := len(s.outbox)
	s.outbox = slices.DeleteFunc(s.outbox, func(o larksyncwire.OutboxRow) bool { return o.CreatedAt.Before(before) })
	return int64(n - len(s.outbox)), nil
}

func (s *memSyncStore) outboxOf(needle string) []larksyncwire.OutboxRow {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	var out []larksyncwire.OutboxRow
	for _, o := range s.outbox {
		if strings.Contains(string(o.Payload), needle) {
			out = append(out, o)
		}
	}
	return out
}

func (s *memSyncStore) cursorsOf(binding string) map[string]string {
	out, _ := s.LarkSyncCursors(context.Background(), binding)
	return out
}

// fakeRegistrar is the accounts site's app registration.
type fakeRegistrar struct {
	mu     sync.Mutex
	begins []larksyncwire.RegisterOptions
	polls  []botcard.RegisterTicket
	asked  []string
	fail   error
}

func (r *fakeRegistrar) BeginWith(_ context.Context, region, _ string, opts larksyncwire.RegisterOptions) (botcard.RegisterChallenge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return botcard.RegisterChallenge{}, r.fail
	}
	r.begins = append(r.begins, opts)
	r.asked = append(r.asked, "begin "+region)
	return botcard.RegisterChallenge{DeviceCode: "dc_sync_secret", URL: "https://accounts.example.com/register?user_code=UC",
		ExpiresIn: 600, Interval: 5}, nil
}

func (r *fakeRegistrar) Poll(_ context.Context, region, deviceCode string) (botcard.RegisterTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, "poll "+region+" "+deviceCode)
	if len(r.polls) == 0 {
		return botcard.RegisterTicket{Status: botcard.RegisterPending}, nil
	}
	t := r.polls[0]
	if len(r.polls) > 1 {
		r.polls = r.polls[1:]
	}
	return t, nil
}

func (r *fakeRegistrar) lastBegin(t *testing.T) larksyncwire.RegisterOptions {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.begins) == 0 {
		t.Fatal("no registration began")
	}
	return r.begins[len(r.begins)-1]
}

// fakeUserAuth is the user OAuth fake.
type fakeUserAuth struct {
	mu         sync.Mutex
	now        func() time.Time
	refreshes  atomic.Int32
	refresh    func(app larksyncwire.App, refreshToken string) (larksyncwire.Token, error)
	configure  larksyncwire.RedirectReport
	configured []string
	deviceErr  error
	devicePoll []larksyncwire.DevicePoll
	exchange   map[string]larksyncwire.Token
	exchanged  []string
	authorized []larksyncwire.AuthRequest
}

func (f *fakeUserAuth) AuthorizeURL(r larksyncwire.AuthRequest) string {
	f.mu.Lock()
	f.authorized = append(f.authorized, r)
	f.mu.Unlock()
	return "https://accounts.example.com/authorize?client_id=" + r.App.AppID + "&state=" + r.State
}

func (f *fakeUserAuth) Exchange(_ context.Context, _ larksyncwire.App, code, redirectURI, verifier string) (larksyncwire.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanged = append(f.exchanged, code+" "+redirectURI+" "+verifier)
	tok, ok := f.exchange[code]
	if !ok {
		return larksyncwire.Token{}, &larksyncwire.Error{Kind: larksyncwire.ErrKindInvalidGrant, Op: "exchange", Msg: "bad code"}
	}
	return tok, nil
}

func (f *fakeUserAuth) Refresh(_ context.Context, app larksyncwire.App, refreshToken string) (larksyncwire.Token, error) {
	f.refreshes.Add(1)
	f.mu.Lock()
	fn := f.refresh
	f.mu.Unlock()
	if fn != nil {
		return fn(app, refreshToken)
	}
	return larksyncwire.Token{AccessToken: "acc-" + refreshToken + "+", RefreshToken: refreshToken + "+",
		ExpiresAt: f.now().Add(2 * time.Hour)}, nil
}

func (f *fakeUserAuth) DeviceBegin(context.Context, larksyncwire.App, []string) (larksyncwire.DeviceChallenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deviceErr != nil {
		return larksyncwire.DeviceChallenge{}, f.deviceErr
	}
	return larksyncwire.DeviceChallenge{DeviceCode: "device_secret", URL: "https://accounts.example.com/device?code=UC",
		UserCode: "UC-1", ExpiresIn: 300, Interval: 5}, nil
}

func (f *fakeUserAuth) DevicePoll(_ context.Context, _ larksyncwire.App, deviceCode string) (larksyncwire.DevicePoll, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if deviceCode != "device_secret" {
		return larksyncwire.DevicePoll{Status: larksyncwire.DeviceFailed, ErrorText: "unknown device code"}, nil
	}
	if len(f.devicePoll) == 0 {
		return larksyncwire.DevicePoll{Status: larksyncwire.DevicePending}, nil
	}
	p := f.devicePoll[0]
	f.devicePoll = f.devicePoll[1:]
	return p, nil
}

func (f *fakeUserAuth) ConfigureRedirect(_ context.Context, app larksyncwire.App, redirectURI string, _ []string) larksyncwire.RedirectReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configured = append(f.configured, app.AppID+" "+redirectURI)
	return f.configure
}

// fakeSyncer is one binding's reader.
type fakeSyncer struct{ w *serviceWorld }

func (s *fakeSyncer) Run(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), cursors map[string]string, idx larksyncwire.Index) (larksyncwire.Result, error) {
	s.w.mu.Lock()
	fn := s.w.runs[b.BotID+"/"+b.AccountID]
	s.w.ran = append(s.w.ran, b.Key)
	s.w.mu.Unlock()
	if fn == nil {
		if _, err := token(ctx); err != nil {
			return larksyncwire.Result{}, err
		}
		return larksyncwire.Result{}, nil
	}
	return fn(ctx, b, token, cursors, idx)
}

func (s *fakeSyncer) Media(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), ref string, _ int64) ([]byte, string, error) {
	tok, err := token(ctx)
	if err != nil {
		return nil, "", err
	}
	return []byte(b.Key + " " + tok + " " + ref), "image/png", nil
}

func (s *fakeSyncer) ResolveUserNames(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), openIDs []string) (map[string]string, error) {
	s.w.mu.Lock()
	fn := s.w.resolveNames
	s.w.mu.Unlock()
	if fn != nil {
		return fn(ctx, b, token, openIDs)
	}
	return nil, nil
}

func (s *fakeSyncer) ResolveNamesWithFallback(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), req larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error) {
	s.w.mu.Lock()
	fn := s.w.resolveNamesWithFallback
	legacy := s.w.resolveNames
	s.w.mu.Unlock()
	if fn != nil {
		return fn(ctx, b, token, req)
	}
	if legacy != nil {
		rawIDs := make([]string, len(req.OpenIDs))
		for i, id := range req.OpenIDs {
			rawIDs[i] = string(id)
		}
		names, err := legacy(ctx, b, token, rawIDs)
		return larksyncwire.NameResolveResult{UserNames: names}, err
	}
	return larksyncwire.NameResolveResult{}, nil
}

func (s *fakeSyncer) ExtractDocLinks(text string) []larksyncwire.PendingLinkCandidate {
	s.w.mu.Lock()
	fn := s.w.extractDocLinks
	s.w.mu.Unlock()
	if fn != nil {
		return fn(text)
	}
	return nil
}

func (s *fakeSyncer) MergePendingLinks(existingCursor string, add []larksyncwire.PendingLinkCandidate) (string, int) {
	s.w.mu.Lock()
	fn := s.w.mergePendingLinks
	s.w.mu.Unlock()
	if fn != nil {
		return fn(existingCursor, add)
	}
	return existingCursor, 0
}

type syncRunFn func(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), cursors map[string]string, idx larksyncwire.Index) (larksyncwire.Result, error)

// fakeHost implements Host for unit testing Service.
type fakeHost struct {
	t          *testing.T
	secret     *secretkey.Box
	settings   *memSettingsStore
	syncStore  larksyncwire.Store
	primaryApp larksyncwire.App
	registrar  syncRegistrar
	lang       i18n.Lang
	member     atomic.Bool
	runCtx     context.Context

	mu           sync.Mutex
	kicks        []string
	pushes       []map[string]any
	larkMessages []larksyncwire.Message
}

func (h *fakeHost) HasSettings() bool { return h.settings != nil }

func (h *fakeHost) Setting(ctx context.Context, key string, out any) (bool, error) {
	return h.settings.Setting(ctx, key, out)
}

func (h *fakeHost) PutSetting(ctx context.Context, key string, v any) error {
	return h.settings.PutSetting(ctx, key, v)
}

func (h *fakeHost) SyncStore() larksyncwire.Store { return h.syncStore }

func (h *fakeHost) SealSecret(plain string) (string, error) { return h.secret.Seal(plain) }

func (h *fakeHost) OpenSecret(sealed string) (string, error) { return h.secret.Open(sealed) }

func (h *fakeHost) PrimaryApp(_ context.Context) (string, string, string) {
	return h.primaryApp.AppID, h.primaryApp.AppSecret, h.primaryApp.Domain
}

func (h *fakeHost) Registrar() syncRegistrar { return h.registrar }

func (h *fakeHost) RegisteredDomain(region string) string { return region }

func (h *fakeHost) CanWrite(r *http.Request) bool {
	p := testPrincipalOf(r.Context())
	return p.Role == testRoleFull && p.Extension == ""
}

func (h *fakeHost) RunContext() context.Context { return h.runCtx }

func (h *fakeHost) Logf(format string, args ...any) {}

func (h *fakeHost) BotLang(ctx context.Context) i18n.Lang { return i18n.From(ctx).Or(h.lang) }

func (h *fakeHost) UserLang() i18n.Lang { return h.lang }

func (h *fakeHost) Audit(_ context.Context, _ string, _ map[string]any) {}

func (h *fakeHost) KBKick(connectorID string, scope connectorwire.Scope) {
	h.mu.Lock()
	h.kicks = append(h.kicks, connectorID+":"+string(scope))
	h.mu.Unlock()
}

func (h *fakeHost) EmitLarkMessages(_ string, msgs []larksyncwire.Message) {
	h.mu.Lock()
	h.larkMessages = append(h.larkMessages, msgs...)
	h.mu.Unlock()
}

func (h *fakeHost) PushLocalized(build func(i18n.Lang) map[string]any, collapseID string) {
	payloads := map[string]any{
		string(i18n.En): build(i18n.En),
	}
	h.mu.Lock()
	h.pushes = append(h.pushes, map[string]any{"collapse_id": collapseID, "payloads": payloads})
	h.mu.Unlock()
}

func (h *fakeHost) IsMember() bool { return h.member.Load() }

// serviceWorld is a Service with fake Host, fakes for OAuth/registrar/readers, and an injectable clock.
type serviceWorld struct {
	svc   *Service
	host  *fakeHost
	store *memSyncStore
	ua    *fakeUserAuth
	reg   *fakeRegistrar
	mux   http.Handler

	mu                       sync.Mutex
	clock                    time.Time
	runs                     map[string]syncRunFn
	resolveNames             func(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), openIDs []string) (map[string]string, error)
	resolveNamesWithFallback func(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), req larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error)
	extractDocLinks          func(text string) []larksyncwire.PendingLinkCandidate
	mergePendingLinks        func(existingCursor string, add []larksyncwire.PendingLinkCandidate) (string, int)
	ran                      []string
	readers                  int
}

func newServiceWorld(t *testing.T) *serviceWorld {
	t.Helper()
	w := &serviceWorld{
		clock: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
		runs:  map[string]syncRunFn{},
	}
	box := secretkey.New(filepath.Join(t.TempDir(), "secret.key"))
	w.store = newMemSyncStore(w.now)
	w.ua = &fakeUserAuth{now: w.now, exchange: map[string]larksyncwire.Token{}}
	w.reg = &fakeRegistrar{}
	w.host = &fakeHost{
		t:          t,
		secret:     box,
		settings:   w.store.memSettingsStore,
		syncStore:  w.store,
		primaryApp: larksyncwire.App{AppID: "cli_primary", AppSecret: "primary-secret", Domain: "feishu"},
		registrar:  w.reg,
		lang:       i18n.En,
	}
	svc := NewService(w.host)
	svc.userAuth = w.ua
	svc.now = w.now
	svc.newSyncer = func() larksyncwire.Syncer {
		w.mu.Lock()
		w.readers++
		w.mu.Unlock()
		return &fakeSyncer{w}
	}
	mux := http.NewServeMux()
	svc.Routes(mux)
	w.svc, w.mux = svc, mux
	return w
}

func (w *serviceWorld) now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.clock
}

func (w *serviceWorld) advance(d time.Duration) {
	w.mu.Lock()
	w.clock = w.clock.Add(d)
	w.mu.Unlock()
}

func (w *serviceWorld) script(botID, accID string, fn syncRunFn) {
	w.mu.Lock()
	w.runs[botID+"/"+accID] = fn
	w.mu.Unlock()
}

func (w *serviceWorld) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	return w.doWith(t, testPrincipal{Role: testRoleFull}, method, path, body)
}

func (w *serviceWorld) doWith(t *testing.T, p testPrincipal, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(withTestPrincipal(req.Context(), p))
	w.mux.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (w *serviceWorld) addSyncBot(t *testing.T, botID, appID, tier string) {
	t.Helper()
	sealed, err := w.svc.seal(appID + "-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.change(context.Background(), func(st *syncState) error {
		st.bots = append(st.bots, syncBot{BotID: botID, Label: "Sync " + botID, AppID: appID, SecretEnc: sealed, Domain: "feishu",
			Role: larksyncwire.RoleSyncOnly, Tier: tier, Approval: larksyncwire.ApprovalReady, CreatedAt: w.now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (w *serviceWorld) adopt(t *testing.T, botID, openID, access, refresh string, expires time.Duration) (key, accID string) {
	t.Helper()
	ctx := context.Background()
	st, err := w.svc.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app, err := w.svc.app(ctx, &st, botID)
	if err != nil {
		t.Fatal(err)
	}
	tok := larksyncwire.Token{AccessToken: access, RefreshToken: refresh, ExpiresAt: w.now().Add(expires), OpenID: openID,
		TenantKey: "t1", Name: "Account " + openID, Scopes: larksyncwire.ScopesFor(larksyncwire.TierDocs)}
	if _, err := w.svc.adopt(ctx, botID, pendingKey(botID), "", app, tok); err != nil {
		t.Fatal(err)
	}
	accID = accountID(brand(app.Domain), "t1", openID)
	return bindingKey(botID, accID), accID
}

func (w *serviceWorld) binding(t *testing.T, key string) syncBinding {
	t.Helper()
	st, err := w.svc.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b := st.binding(key)
	if b == nil {
		t.Fatalf("no binding %s", key)
	}
	return *b
}

func (w *serviceWorld) pushes() []map[string]any {
	w.host.mu.Lock()
	defer w.host.mu.Unlock()
	return slices.Clone(w.host.pushes)
}

func (w *serviceWorld) kicks() []string {
	w.host.mu.Lock()
	defer w.host.mu.Unlock()
	return slices.Clone(w.host.kicks)
}

func noSecretIn(t *testing.T, what, s string, secrets ...string) {
	t.Helper()
	for _, x := range secrets {
		if x != "" && strings.Contains(s, x) {
			t.Fatalf("%s holds the secret %q: %s", what, x, s)
		}
	}
}

func marshalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
