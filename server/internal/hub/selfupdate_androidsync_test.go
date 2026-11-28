package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/update"
)

// syncLog collects the hub's log lines.
type syncLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(string(p)))
	return len(p), nil
}

func (l *syncLog) about(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			n++
		}
	}
	return n
}

// A hub that follows the releases offers its phones the Android app the release carries, through the endpoints they
// already update from, with no setup (#5563). It says so in its log once, and a refusal once, not at every check.
func TestAndroidAppFromTheRelease(t *testing.T) {
	p := newPublisher(t, "v2-202611021700", 202611021700)
	h, _, _, _ := updatingHub(t, p, update.Release)
	logs := &syncLog{}
	h.logger = log.New(logs, "", 0)
	apk := bytes.Repeat([]byte("signed dex bytes "), 1024)
	sum := sha256.Sum256(apk)
	digest := hex.EncodeToString(sum[:])
	release := map[string]any{
		"version": "0.3.0", "name": "Persona-0.3.0-android.apk", "package_name": "app.example.persona",
		"version_code": 105000, "version_name": "0.3.0", "min_sdk": 29, "certificate_sha256": strings.Repeat("c", 64),
		"sha256": digest, "size": len(apk), "source_sha": strings.Repeat("a", 40), "notes": "",
	}
	manifest, _ := json.Marshal(release)
	p.extra = map[string][]byte{"/dl/android.json": manifest, "/dl/Persona-0.3.0-android.apk": apk}

	h.upd.syncAndroid(context.Background())
	h.upd.syncAndroid(context.Background()) // the next check: already there
	if n := logs.about("offered the release's Android app"); n != 1 {
		t.Fatalf("logged the new Android app %d times: %v", n, logs.lines)
	}

	h.store = newPairStore()
	token := "jp_android-release"
	if err := h.store.CreatePairToken(context.Background(), hashToken(token), time.Now().Add(time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	handler := h.routes()
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/app/android/"+path, nil)
		r.RemoteAddr = net.JoinHostPort(netip.AddrFrom4([4]byte{127, 0, 0, 1}).String(), "1")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("latest")
	var latest map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &latest); err != nil || w.Code != http.StatusOK {
		t.Fatalf("latest: %d %s", w.Code, w.Body)
	}
	if latest["version_code"] != float64(105000) || latest["version_name"] != "0.3.0" || latest["download_url"] != "/api/v1/app/android/apk?sha256="+digest {
		t.Fatalf("latest: %v", latest)
	}
	if w := request("apk?sha256=" + digest); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), apk) {
		t.Fatalf("apk: %d", w.Code)
	}

	// A newer release signed with another key is refused, and said once.
	release["version_code"], release["certificate_sha256"] = 106000, strings.Repeat("d", 64)
	manifest, _ = json.Marshal(release)
	p.extra["/dl/android.json"] = manifest
	h.upd.syncAndroid(context.Background())
	h.upd.syncAndroid(context.Background())
	if n := logs.about("signed with another key"); n != 1 {
		t.Fatalf("logged the refusal %d times: %v", n, logs.lines)
	}
	if w := request("latest"); !strings.Contains(w.Body.String(), `"version_code":105000`) {
		t.Fatalf("the refused release replaced the published one: %s", w.Body)
	}
}

// Without an update source there is nothing to look at, and nothing is asked.
func TestAndroidAppWithoutUpdateSource(t *testing.T) {
	h := newTestHub(t)
	h.version = "v1"
	h.build = update.Make(h.version, update.Release, "202611021500", "")
	asked := false
	h.upd.client = &http.Client{Transport: updateLoopTransport(func(r *http.Request) (*http.Response, error) {
		asked = true
		return nil, http.ErrHandlerTimeout
	})}
	h.upd.syncAndroid(context.Background())
	if _, err := h.upd.check(context.Background()); !errors.Is(err, errNotRelease) {
		t.Fatalf("check without a source: %v", err)
	}
	if v := h.upd.view(); v.CanUpdate || v.Auto || h.upd.wantsAuto() {
		t.Fatalf("updates enabled without a source: %+v", v)
	}
	if asked {
		t.Fatal("a hub with no update source tried to fetch a release or Android APK")
	}
}
