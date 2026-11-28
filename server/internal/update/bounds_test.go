package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func setAtomicInt64(t *testing.T, v *atomic.Int64, next int64) {
	t.Helper()
	prev := v.Swap(next)
	t.Cleanup(func() { v.Store(prev) })
}

func TestUnpackRejectsDecompressionBombAndCleansUp(t *testing.T) {
	const limit int64 = 4096
	setAtomicInt64(t, &maxUnpackedBinaryOverride, limit)

	// Exact limit passes.
	exactBin := bytes.Repeat([]byte("A"), int(limit))
	exactFile, exactGZ := fileFor("personad-linux-amd64.gz", exactBin)
	exactSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(exactGZ)
	}))
	defer exactSrv.Close()
	exactDir := t.TempDir()
	binPath, err := Download(context.Background(), exactSrv.Client(), exactSrv.URL, exactFile, exactDir)
	if err != nil {
		t.Fatalf("exact limit failed: %v", err)
	}
	if got, err := os.ReadFile(binPath); err != nil || !bytes.Equal(got, exactBin) {
		t.Fatalf("exact limit read: err=%v len=%d", err, len(got))
	}

	// Exact limit + 1 byte fails with errMismatch and leaves no files behind.
	overOneBin := bytes.Repeat([]byte{0}, int(limit)+1)
	overOneFile, overOneGZ := fileFor("personad-linux-amd64.gz", overOneBin)
	overOneSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(overOneGZ)
	}))
	defer overOneSrv.Close()
	overOneDir := t.TempDir()
	_, err = Download(context.Background(), overOneSrv.Client(), overOneSrv.URL, overOneFile, overOneDir)
	if !errors.Is(err, errMismatch) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("limit+1 error = %v, want errMismatch exceeding limit", err)
	}
	if left, _ := filepath.Glob(filepath.Join(overOneDir, "*")); len(left) != 0 {
		t.Fatalf("leftover files after rejected unpack: %v", left)
	}

	// Concatenated 2-member gzip (limit bytes + 1 byte) must also be rejected cleanly.
	member1 := gzipped(bytes.Repeat([]byte("B"), int(limit)))
	member2 := gzipped([]byte("C"))
	multiGZ := append(append([]byte(nil), member1...), member2...)
	outerSum := sha256.Sum256(multiGZ)
	innerSum := sha256.Sum256(append(bytes.Repeat([]byte("B"), int(limit)), 'C'))
	multiFile := File{
		Name:         "personad-linux-amd64.gz",
		Size:         int64(len(multiGZ)),
		SHA256:       hex.EncodeToString(outerSum[:]),
		BinarySHA256: hex.EncodeToString(innerSum[:]),
	}
	multiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(multiGZ)
	}))
	defer multiSrv.Close()
	multiDir := t.TempDir()
	_, err = Download(context.Background(), multiSrv.Client(), multiSrv.URL, multiFile, multiDir)
	if !errors.Is(err, errMismatch) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("multi-member bomb error = %v, want errMismatch exceeding limit", err)
	}
	if left, _ := filepath.Glob(filepath.Join(multiDir, "*")); len(left) != 0 {
		t.Fatalf("leftover files after rejected multi-member unpack: %v", left)
	}

	// describe() and verifyDownload() enforce the same unpacked binary ceiling.
	checkDir := t.TempDir()
	bombPath := filepath.Join(checkDir, overOneFile.Name)
	if err := os.WriteFile(bombPath, overOneGZ, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := describe(bombPath); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("describe() bomb error = %v, want exceeds error", err)
	}
	if err := verifyDownload(checkDir, overOneFile, 0, ""); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("verifyDownload() bomb error = %v, want exceeds error", err)
	}
}

func TestFetchRejectsInvalidAndOversizedDownloads(t *testing.T) {
	const dlLimit int64 = 1024
	setAtomicInt64(t, &maxDownloadSizeOverride, dlLimit)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Rogue CDN ignores manifest size and streams far more bytes.
		_, _ = w.Write(bytes.Repeat([]byte("X"), int(dlLimit)+4096))
	}))
	defer srv.Close()

	// Invalid sizes (<= 0, > maxDownloadSize, math.MaxInt64 overflow) are rejected before any HTTP request or retry.
	for _, size := range []int64{0, -1, dlLimit + 1, math.MaxInt64} {
		f := File{Name: "personad-linux-amd64.gz", Size: size, SHA256: strings.Repeat("a", 64), BinarySHA256: strings.Repeat("b", 64)}
		if _, err := Download(context.Background(), srv.Client(), srv.URL, f, t.TempDir()); !errors.Is(err, errMismatch) {
			t.Fatalf("Download(size=%d): err=%v, want errMismatch", size, err)
		}
		if err := fetchFile(context.Background(), srv.Client(), srv.URL+"/"+f.Name, filepath.Join(t.TempDir(), f.Name), f); !errors.Is(err, errMismatch) {
			t.Fatalf("fetchFile(size=%d): err=%v, want errMismatch", size, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid sizes made %d HTTP requests, want 0", hits.Load())
	}

	// DownloadBinary rejects f.Size exceeding unpackedBinaryLimit before HTTP.
	setAtomicInt64(t, &maxUnpackedBinaryOverride, 512)
	sum := strings.Repeat("c", 64)
	if _, err := DownloadBinary(context.Background(), srv.Client(), srv.URL,
		File{Name: "personad-linux-amd64", Size: 513, SHA256: sum, BinarySHA256: sum}, t.TempDir()); !errors.Is(err, errMismatch) {
		t.Fatalf("DownloadBinary oversized: err=%v, want errMismatch", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("DownloadBinary oversized made %d HTTP requests, want 0", hits.Load())
	}

	// Rogue CDN streaming more than f.Size stops at f.Size+1, returns errMismatch without retrying, and leaves no files.
	validF, _ := fileFor("personad-linux-amd64.gz", []byte("small"))
	dir := t.TempDir()
	if _, err := Download(context.Background(), srv.Client(), srv.URL, validF, dir); !errors.Is(err, errMismatch) {
		t.Fatalf("rogue CDN stream: err=%v, want errMismatch", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("rogue CDN stream hit %d times, want 1", hits.Load())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("leftover files after rogue CDN stream: %v", left)
	}
}

type cancelAfter500Transport struct {
	cancel context.CancelFunc
	hits   atomic.Int32
}

func (rt *cancelAfter500Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.hits.Add(1)
	rt.cancel()
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestDownloadRetryBackoffCancelsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &cancelAfter500Transport{cancel: cancel}
	client := &http.Client{Transport: rt}
	f, _ := fileFor("personad-linux-amd64.gz", []byte("payload"))

	start := time.Now()
	_, err := Download(ctx, client, "http://127.0.0.1:1", f, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rt.hits.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", rt.hits.Load())
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("backoff did not cancel promptly: took %v", elapsed)
	}
}

func TestGetRejectsOversizedResponseInsteadOfTruncating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exact":
			_, _ = w.Write(bytes.Repeat([]byte("x"), 64))
		case "/over":
			_, _ = w.Write(bytes.Repeat([]byte("x"), 65))
		}
	}))
	defer srv.Close()

	got, err := get(context.Background(), srv.Client(), srv.URL+"/exact", 64)
	if err != nil || len(got) != 64 {
		t.Fatalf("exact: err=%v len=%d", err, len(got))
	}
	if _, err := get(context.Background(), srv.Client(), srv.URL+"/over", 64); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over: err=%v, want exceeds error", err)
	}
}

func TestHealthRejectsNon200RedirectsAndOversizedBody(t *testing.T) {
	const limit int64 = 128
	setAtomicInt64(t, &healthBodyLimitOverride, limit)

	exactBody := `{"build":202611021700,"update":{"confirmed":true},"pad":"`
	exactBody += strings.Repeat("a", int(limit)-len(exactBody)-2) + `"}`
	if int64(len(exactBody)) != limit {
		t.Fatalf("exactBody len = %d, want %d", len(exactBody), limit)
	}
	overBody := `{"build":202611021700,"update":{"confirmed":true},"pad":"`
	overBody += strings.Repeat("a", int(limit)+1-len(overBody)-2) + `"}`
	if int64(len(overBody)) != limit+1 {
		t.Fatalf("overBody len = %d, want %d", len(overBody), limit+1)
	}

	var redirectTargetHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status503":
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": true}})
		case "/redirect":
			http.Redirect(w, r, "/healthy", http.StatusFound)
		case "/healthy":
			redirectTargetHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": true}})
		case "/exact":
			_, _ = w.Write([]byte(exactBody))
		case "/over":
			_, _ = w.Write([]byte(overBody))
		}
	}))
	defer srv.Close()

	if _, err := health(context.Background(), srv.Client(), srv.URL+"/status503"); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("non-200 health: err=%v, want HTTP 503 error", err)
	}
	if _, err := health(context.Background(), srv.Client(), srv.URL+"/redirect"); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect health: err=%v, want HTTP 302 error", err)
	}
	if redirectTargetHits.Load() != 0 {
		t.Fatalf("health() followed redirect (%d target hits)", redirectTargetHits.Load())
	}
	got, err := health(context.Background(), srv.Client(), srv.URL+"/exact")
	if err != nil || got.Build != 202611021700 || !got.Update.Confirmed {
		t.Fatalf("exact health body: got=%+v err=%v", got, err)
	}
	if _, err := health(context.Background(), srv.Client(), srv.URL+"/over"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized health: err=%v, want exceeds error", err)
	}

	// Watch must not treat a 503 health response claiming confirmed=true as a healthy update; it rolls back at deadline.
	dir := t.TempDir()
	pending := PendingPath(dir)
	p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL + "/status503"}
	if err := Save(pending, p); err != nil {
		t.Fatal(err)
	}
	restored, restarted := false, false
	err = Watch(context.Background(), srv.Client(), p, pending, time.Now(), 50*time.Millisecond,
		func() error { restored = true; return nil }, func() error { restarted = true; return nil })
	if err != nil || !restored || !restarted {
		t.Fatalf("Watch on 503: err=%v restored=%v restarted=%v, want rollback", err, restored, restarted)
	}
}

func TestWatchCanceledContextDoesNotRollBack(t *testing.T) {
	t.Run("pre-canceled after deadline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": false}})
		}))
		defer srv.Close()

		dir := t.TempDir()
		pending := PendingPath(dir)
		p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL}
		if err := Save(pending, p); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		restored := false
		err := Watch(ctx, srv.Client(), p, pending, time.Now().Add(-time.Second), 50*time.Millisecond,
			func() error { restored = true; return nil }, func() error { return nil })
		if !errors.Is(err, context.Canceled) || restored {
			t.Fatalf("err=%v restored=%v, want context.Canceled without rollback", err, restored)
		}
	})

	t.Run("canceled during health after deadline", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		inFlight := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(inFlight)
			<-r.Context().Done()
		}))
		defer srv.Close()

		dir := t.TempDir()
		pending := PendingPath(dir)
		p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL}
		if err := Save(pending, p); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		var restored atomic.Bool
		go func() {
			done <- Watch(ctx, srv.Client(), p, pending, time.Now().Add(-time.Second), 50*time.Millisecond,
				func() error { restored.Store(true); return nil }, func() error { return nil })
		}()
		<-inFlight
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) || restored.Load() {
			t.Fatalf("err=%v restored=%v, want context.Canceled without rollback", err, restored.Load())
		}
	})

	t.Run("canceled during poll wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		polled := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"build": 202611021700, "update": map[string]bool{"confirmed": false}})
			select {
			case polled <- struct{}{}:
			default:
			}
		}))
		defer srv.Close()

		dir := t.TempDir()
		pending := PendingPath(dir)
		p := Pending{FromBuild: 202611021500, ToBuild: 202611021700, HealthURL: srv.URL}
		if err := Save(pending, p); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		var restored atomic.Bool
		start := time.Now()
		go func() {
			done <- Watch(ctx, srv.Client(), p, pending, time.Now().Add(time.Minute), 10*time.Second,
				func() error { restored.Store(true); return nil }, func() error { return nil })
		}()
		<-polled
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) || restored.Load() {
			t.Fatalf("err=%v restored=%v, want context.Canceled without rollback", err, restored.Load())
		}
		if elapsed := time.Since(start); elapsed >= time.Second {
			t.Fatalf("poll wait did not cancel promptly: took %v", elapsed)
		}
	})
}
