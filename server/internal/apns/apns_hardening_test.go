package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendRejectsMalformedDeviceToken(t *testing.T) {
	var hits atomic.Int32
	c, _ := testClient(t, func(_ int, w http.ResponseWriter) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	badTokens := []string{
		"",
		".",
		"..",
		"../other",
		"..;foo",
		"abcd;foo=bar",
		";",
		"abcd/extra",
		`abcd\extra`,
		"abcd?foo=bar",
		"abcd#frag",
		"abcd%2fother",
		"ab cd",
		"abcd\r\n",
		"abcd\x00ef",
		"abcd\x7f",
		"tok\xc3\xa9",
		`ab"cd`,
		"ab<cd>",
		"ab{cd}",
		"ab|cd",
		"ab^cd",
		"ab@cd",
		"ab:cd",
		strings.Repeat("a", maxDeviceTokenLen+1),
	}
	for _, tok := range badTokens {
		_, err := c.Send(context.Background(), Request{Token: tok, PushType: "alert", Priority: "10", Body: []byte(`{}`)})
		if !errors.Is(err, ErrInvalidDeviceToken) {
			t.Errorf("Send(Token=%q) error = %v, want ErrInvalidDeviceToken", tok, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("malformed tokens caused %d outbound HTTP requests, want 0", n)
	}
	// Valid hex (64-char APNs, 256-char & 512-char Live Activity/VoIP) and test fixture tokens remain accepted.
	validTokens := []string{
		"abcd",
		strings.Repeat("0123456789abcdef", 4),  // 64-char hex
		strings.Repeat("0123456789abcdef", 16), // 256-char hex
		strings.Repeat("a", maxDeviceTokenLen), // 512-char boundary
		"devtok",
		"tok-phone",
		"self_token",
	}
	for _, tok := range validTokens {
		res, err := c.Send(context.Background(), Request{Token: tok, PushType: "alert", Priority: "10", Body: []byte(`{}`)})
		if err != nil || res.Status != 200 {
			t.Fatalf("Send(Token=%q) = (%+v, %v), want 200", tok, res, err)
		}
	}
}

func TestTokenResignsWhenWallClockStepsBackward(t *testing.T) {
	c, _ := testClient(t, func(int, http.ResponseWriter) {})
	first, err := c.token()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate NTP stepping the wall clock backward so cached jwtIat is in the future.
	c.mu.Lock()
	c.jwtIat = time.Now().Add(2 * time.Hour).Unix()
	c.mu.Unlock()

	next, err := c.token()
	if err != nil {
		t.Fatal(err)
	}
	if next == first {
		t.Fatal("cached token with future iat was reused after backward wall-clock jump")
	}
	parts := strings.Split(next, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed JWT: %q", next)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatalf("claims JSON invalid: %v (%s)", err, claimsBytes)
	}
	if claims.Iat > time.Now().Add(5*time.Second).Unix() {
		t.Fatalf("resigned iat = %d, still far in future", claims.Iat)
	}
}

func TestTokenEncodesValidRFC8259JSON(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Characters like \v and \x07 are escaped by Go %q as \v and \a, which are invalid in RFC 8259 JSON.
	keyID := "K\v\a\"\\1"
	teamID := "T\v\a\"\\2"
	c := NewWithKey(Config{KeyID: keyID, TeamID: teamID}, k)
	tok, err := c.token()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed JWT: %q", tok)
	}
	hRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	cRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hRaw, &header); err != nil {
		t.Fatalf("JWT header is not valid JSON: %v (raw=%s)", err, hRaw)
	}
	if header.Alg != "ES256" || header.Kid != keyID {
		t.Fatalf("header = %+v, want alg=ES256 kid=%q", header, keyID)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}
	if err := json.Unmarshal(cRaw, &claims); err != nil {
		t.Fatalf("JWT claims is not valid JSON: %v (raw=%s)", err, cRaw)
	}
	if claims.Iss != teamID {
		t.Fatalf("claims.Iss = %q, want %q", claims.Iss, teamID)
	}
}

func TestLiveActivityAndVoIPTopicSuffixHandling(t *testing.T) {
	var mu sync.Mutex
	var topics []string
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		topics = append(topics, r.Header.Get("apns-topic"))
		mu.Unlock()
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	c := NewWithKey(Config{KeyID: "KEY1234567", TeamID: "TEAM123456"}, k)
	c.http, c.Base = srv.Client(), srv.URL

	reqs := []Request{
		{Token: "abcd", Topic: "app.test", PushType: "liveactivity", Priority: "10"},
		{Token: "abcd", Topic: "app.test.push-type.liveactivity", PushType: "liveactivity", Priority: "10"},
		{Token: "abcd", Topic: "", PushType: "liveactivity", Priority: "10"},
		{Token: "abcd", Topic: "", PushType: "voip", Priority: "10"},
	}
	for _, r := range reqs {
		if res, err := c.Send(context.Background(), r); err != nil || res.Status != 200 {
			t.Fatalf("Send(%+v) = (%+v, %v)", r, res, err)
		}
	}
	want := []string{
		"app.test.push-type.liveactivity",
		"app.test.push-type.liveactivity",
		"",
		"",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(topics) != len(want) {
		t.Fatalf("topics = %q, want %q", topics, want)
	}
	for i := range want {
		if topics[i] != want[i] {
			t.Errorf("req %d topic = %q, want %q", i, topics[i], want[i])
		}
	}
}

func TestSendErrorBodyDrainsForConnectionReuse(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var opened atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Valid JSON followed by trailing whitespace/bytes that json.Decoder leaves unread.
		_, _ = w.Write([]byte(`{"reason":"BadDeviceToken"}` + strings.Repeat(" ", 1024)))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			opened.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	c := NewWithKey(Config{KeyID: "KEY1234567", TeamID: "TEAM123456", Topic: "app.test"}, k)
	c.http, c.Base = srv.Client(), srv.URL

	for i := range 3 {
		res, err := c.Send(context.Background(), Request{Token: "abcd", PushType: "alert", Priority: "10", Body: []byte(`{}`)})
		if err != nil || res.Status != 400 || res.Reason != "BadDeviceToken" {
			t.Fatalf("call %d: Send = (%+v, %v)", i, res, err)
		}
	}
	if n := opened.Load(); n != 1 {
		t.Fatalf("HTTP/1.1 keepalive connections opened = %d, want 1 (error response body was not drained)", n)
	}
}

type countingReadCloser struct {
	r    io.Reader
	read *atomic.Int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func (c *countingReadCloser) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendBoundsLargeErrorBodyRead(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var bytesRead atomic.Int64
	payload := append([]byte(`{"reason":"BadDeviceToken"}`), bytes.Repeat([]byte(" "), 256<<10)...)
	c := NewWithKey(Config{KeyID: "KEY1234567", TeamID: "TEAM123456", Topic: "app.test"}, k)
	c.Base = "https://api.example.com"
	c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       &countingReadCloser{r: bytes.NewReader(payload), read: &bytesRead},
			Header:     make(http.Header),
		}, nil
	})}

	res, err := c.Send(context.Background(), Request{Token: "abcd", PushType: "alert", Priority: "10", Body: []byte(`{}`)})
	if err != nil || res.Status != 400 || res.Reason != "BadDeviceToken" {
		t.Fatalf("Send = (%+v, %v), want 400 BadDeviceToken", res, err)
	}
	if got := bytesRead.Load(); got > maxResponseBodyBytes {
		t.Fatalf("read %d bytes from 256 KiB error body, want <= %d", got, maxResponseBodyBytes)
	}
}
