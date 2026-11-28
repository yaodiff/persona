package voice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockTTSVoicesServer struct {
	srv      *httptest.Server
	status   atomic.Int32
	asked    atomic.Int32
	mu       sync.Mutex
	body     string
	lastAuth string
	lastPath string
}

func newMockTTSVoicesServer(t *testing.T, body string) *mockTTSVoicesServer {
	t.Helper()
	m := &mockTTSVoicesServer{body: body}
	m.status.Store(http.StatusOK)
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.asked.Add(1)
		m.mu.Lock()
		m.lastAuth = r.Header.Get("Authorization")
		m.lastPath = r.URL.Path
		payload := m.body
		m.mu.Unlock()
		if code := int(m.status.Load()); code != http.StatusOK {
			http.Error(w, "tts backend body should not be logged", code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockTTSVoicesServer) setBody(s string) {
	m.mu.Lock()
	m.body = s
	m.mu.Unlock()
}

func (m *mockTTSVoicesServer) auth() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastAuth
}

func (m *mockTTSVoicesServer) path() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPath
}

func newTTSTestCatalog(cfg Config) *testCatalog {
	tc := &testCatalog{at: time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)}
	tc.catalog = &catalog{
		cfg: &cfg,
		now: func() time.Time {
			tc.mu.Lock()
			defer tc.mu.Unlock()
			return tc.at
		},
		logf: func(format string, args ...any) {
			tc.mu.Lock()
			defer tc.mu.Unlock()
			tc.lines = append(tc.lines, fmt.Sprintf(format, args...))
		},
	}
	return tc
}

func TestTTSVoicesObjectShapeAndDefaults(t *testing.T) {
	body := `{"voices":[{"id":"af_heart","name":"Heart","languages":["en","zh"]},{"id":"bf_emma","name":"Emma","languages":["en"]}],"default":"bf_emma"}`
	srv := newMockTTSVoicesServer(t, body)

	// Without tts_default_voice: endpoint's default wins and metadata is preserved.
	tc := newTTSTestCatalog(Config{TTSURL: srv.srv.URL})
	v, err := tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "bf_emma" || ids(v) != "af_heart,bf_emma" {
		t.Fatalf("endpoint default: %+v %v", v, err)
	}
	if v.Voices[0].Name != "Heart" || !slices.Equal(v.Voices[0].Languages, []string{"en", "zh"}) {
		t.Fatalf("metadata not preserved: %+v", v.Voices[0])
	}
	if srv.path() != "/v1/audio/voices" {
		t.Fatalf("path = %q, want /v1/audio/voices", srv.path())
	}

	// With tts_default_voice matching an existing voice: marked default, no duplicate, metadata kept.
	tc = newTTSTestCatalog(Config{TTSURL: srv.srv.URL, TTSDefaultVoice: "af_heart"})
	v, err = tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "af_heart" || ids(v) != "af_heart,bf_emma" || v.Voices[0].Name != "Heart" {
		t.Fatalf("configured default in list: %+v %v", v, err)
	}

	// With tts_default_voice not listed by the endpoint: always included and marked default.
	tc = newTTSTestCatalog(Config{TTSURL: srv.srv.URL, TTSDefaultVoice: "custom_voice"})
	v, err = tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "custom_voice" || ids(v) != "custom_voice,af_heart,bf_emma" {
		t.Fatalf("configured default not in list: %+v %v", v, err)
	}

	// Without tts_default_voice and without endpoint default: first voice is default.
	srv.setBody(`{"voices":[{"id":"v1","name":"One"},{"id":"v2","name":"Two"}]}`)
	tc = newTTSTestCatalog(Config{TTSURL: srv.srv.URL})
	v, err = tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "v1" || ids(v) != "v1,v2" {
		t.Fatalf("first voice default: %+v %v", v, err)
	}
}

func TestTTSVoicesStringArrayShape(t *testing.T) {
	srv := newMockTTSVoicesServer(t, `{"voices":["a","b","bad voice","a"]}`)

	// String array without default: first valid voice becomes default; invalid and duplicate entries skipped.
	tc := newTTSTestCatalog(Config{TTSURL: srv.srv.URL})
	v, err := tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "a" || ids(v) != "a,b" {
		t.Fatalf("string array: %+v %v", v, err)
	}

	// String array with endpoint default.
	srv.setBody(`{"voices":["a","b"],"default":"b"}`)
	tc = newTTSTestCatalog(Config{TTSURL: srv.srv.URL})
	v, err = tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "b" || ids(v) != "a,b" {
		t.Fatalf("string array with default: %+v %v", v, err)
	}

	// String array with tts_default_voice not in array: included and default.
	tc = newTTSTestCatalog(Config{TTSURL: srv.srv.URL, TTSDefaultVoice: "c"})
	v, err = tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "c" || ids(v) != "c,a,b" {
		t.Fatalf("string array with configured default: %+v %v", v, err)
	}
}

func TestTTSVoices404GivesDefaultOnlyAndCaches(t *testing.T) {
	srv := newMockTTSVoicesServer(t, "")
	srv.status.Store(http.StatusNotFound)

	tc := newTTSTestCatalog(Config{TTSURL: srv.srv.URL, TTSDefaultVoice: "af_heart"})
	for range 2 {
		v, err := tc.voices(t.Context(), "")
		if err != nil || v.Stale || v.Default != "af_heart" || ids(v) != "af_heart" {
			t.Fatalf("404 with default: %+v %v", v, err)
		}
	}
	if srv.asked.Load() != 1 || len(tc.logged()) != 0 {
		t.Fatalf("404 should cache without failure log: asked=%d logged=%q", srv.asked.Load(), tc.logged())
	}

	// Without tts_default_voice, 404 yields an empty non-nil list.
	tc = newTTSTestCatalog(Config{TTSURL: srv.srv.URL})
	v, err := tc.voices(t.Context(), "")
	if err != nil || v.Stale || v.Default != "" || v.Voices == nil || len(v.Voices) != 0 {
		t.Fatalf("404 without default: %+v %v", v, err)
	}
}

func TestTTSVoicesCachingAndFailureServesStale(t *testing.T) {
	srv := newMockTTSVoicesServer(t, `{"voices":["a","b"]}`)
	tc := newTTSTestCatalog(Config{TTSURL: srv.srv.URL, TTSDefaultVoice: "a", TTSKey: "sk-secret-value"})

	v, err := tc.voices(t.Context(), "")
	if err != nil || v.Stale || ids(v) != "a,b" || srv.asked.Load() != 1 {
		t.Fatalf("initial read: %+v %v asked=%d", v, err, srv.asked.Load())
	}

	// Within 10 minutes: cached, endpoint not called again.
	tc.advance(9 * time.Minute)
	v, err = tc.voices(t.Context(), "")
	if err != nil || v.Stale || ids(v) != "a,b" || srv.asked.Load() != 1 {
		t.Fatalf("cached read: %+v %v asked=%d", v, err, srv.asked.Load())
	}

	// Past 10 minutes + failure: last good list served as stale, failure logged once without secret or body.
	tc.advance(2 * time.Minute)
	srv.status.Store(http.StatusBadGateway)
	for range 3 {
		v, err = tc.voices(t.Context(), "")
		if err != nil || !v.Stale || v.Default != "a" || ids(v) != "a,b" {
			t.Fatalf("stale fallback: %+v %v", v, err)
		}
	}
	if srv.asked.Load() != 4 {
		t.Fatalf("failures must not be cached: asked=%d", srv.asked.Load())
	}
	lines := tc.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], "voices:tts") || !strings.Contains(lines[0], "HTTP 502") ||
		strings.Contains(lines[0], "sk-secret-value") || strings.Contains(lines[0], "tts backend body") || strings.Contains(lines[0], srv.srv.URL) {
		t.Fatalf("unexpected log: %q", lines)
	}

	// Recovery returns the fresh list and clears stale.
	srv.status.Store(http.StatusOK)
	srv.setBody(`{"voices":["a","c"]}`)
	v, err = tc.voices(t.Context(), "")
	if err != nil || v.Stale || ids(v) != "a,c" {
		t.Fatalf("recovery: %+v %v", v, err)
	}
}

func TestTTSVoicesUnreachableGivesDefaultOnly(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	deadURL := closed.URL
	closed.Close()

	tc := newTTSTestCatalog(Config{TTSURL: deadURL, TTSDefaultVoice: "fallback_voice", TTSKey: "sk-unreachable-secret"})
	v, err := tc.voices(t.Context(), "")
	if err != nil || !v.Stale || v.Default != "fallback_voice" || ids(v) != "fallback_voice" {
		t.Fatalf("unreachable: %+v %v", v, err)
	}
	for _, line := range tc.logged() {
		if strings.Contains(line, "sk-unreachable-secret") || strings.Contains(line, deadURL) {
			t.Fatalf("log leaked secret or URL: %q", line)
		}
	}
}

func TestTTSVoicesBearerOnlyWithKey(t *testing.T) {
	srv := newMockTTSVoicesServer(t, `{"voices":["v1"]}`)

	// Without a key: no Authorization header is sent.
	tc := newTTSTestCatalog(Config{TTSURL: srv.srv.URL})
	if _, err := tc.voices(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if got := srv.auth(); got != "" {
		t.Fatalf("expected empty Authorization without key, got %q", got)
	}

	// With a key: Authorization: Bearer <key> is sent.
	tc = newTTSTestCatalog(Config{TTSURL: srv.srv.URL, TTSKey: "sk-tts-123"})
	if _, err := tc.voices(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if got := srv.auth(); got != "Bearer sk-tts-123" {
		t.Fatalf("Authorization = %q, want Bearer sk-tts-123", got)
	}
}

func TestPluginVoicesEndpointWithAndWithoutTTS(t *testing.T) {
	up := newListService(t)
	ttsSrv := newMockTTSVoicesServer(t, `{"voices":[{"id":"tts_v1","name":"TTS One","languages":["en"]}],"default":"tts_v1"}`)

	// With TTS configured (using a full speech URL with a prefix): GET /voices queries sibling .../v1/audio/voices
	// instead of the realtime upstream, regardless of the model query param.
	var logs bytes.Buffer
	pl := New(Config{
		Providers:       []Provider{{ID: "rt", URL: up.url(), Key: "k", Dialect: DialectLive}},
		TTSURL:          ttsSrv.srv.URL + "/prefix/v1/audio/speech",
		TTSDefaultVoice: "tts_def",
		TTSKey:          "sk-tts-handler",
	}, "http://127.0.0.1:1", "", log.New(&logs, "", 0))
	srv := httptest.NewServer(pl.Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/voices?model=rt/m1")
	if err != nil {
		t.Fatal(err)
	}
	var body voiceList
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || body.Default != "tts_def" || ids(body) != "tts_def,tts_v1" || body.Stale {
		t.Fatalf("GET /voices with TTS: status=%d body=%+v", res.StatusCode, body)
	}
	if ttsSrv.path() != "/prefix/v1/audio/voices" || ttsSrv.auth() != "Bearer sk-tts-handler" || up.voicesAsked.Load() != 0 {
		t.Fatalf("path=%q auth=%q upstreamAsked=%d", ttsSrv.path(), ttsSrv.auth(), up.voicesAsked.Load())
	}

	// Malformed model parameter still returns 400.
	res, err = http.Get(srv.URL + "/voices?model=bad")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad model status = %d, want 400", res.StatusCode)
	}

	// Without TTS configured: GET /voices still uses the realtime upstream as before.
	plNoTTS := New(Config{
		Providers: []Provider{{ID: "rt", URL: up.url(), Key: "k", Dialect: DialectLive}},
	}, "http://127.0.0.1:1", "", log.New(io.Discard, "", 0))
	srvNoTTS := httptest.NewServer(plNoTTS.Handler())
	defer srvNoTTS.Close()

	res, err = http.Get(srvNoTTS.URL + "/voices?model=rt/m1")
	if err != nil {
		t.Fatal(err)
	}
	var noTTSBody voiceList
	if err := json.NewDecoder(res.Body).Decode(&noTTSBody); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || ids(noTTSBody) != "v_m1" || up.voicesAsked.Load() != 1 {
		t.Fatalf("GET /voices without TTS: status=%d body=%+v upstreamAsked=%d", res.StatusCode, noTTSBody, up.voicesAsked.Load())
	}
}
