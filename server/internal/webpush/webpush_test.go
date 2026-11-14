package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

// RFC 8291 Appendix A: every value of its example, base64url.
const (
	vecPlaintext = "When I grow up, I want to be a watermelon"
	vecASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	vecASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	vecUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	vecUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	vecSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	vecAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	vecShared    = "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs"
	vecCEK       = "oIhVW04MRdy2XN9CiKLxTg"
	vecNonce     = "4h_95klXJ5E_qnoN"
	vecHeader    = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	vecCipher    = "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrdirN6GcG7sFz1y1sqLgVi1VhjVkHsUoEsbI_0LpXMuGvnzQ"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := unb64(s)
	if err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return b
}

func vecKey(t *testing.T, priv, pub string) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.P256().NewPrivateKey(mustB64(t, priv))
	if err != nil {
		t.Fatal(err)
	}
	if b64(k.PublicKey().Bytes()) != pub {
		t.Fatalf("public key of %s is %s, not %s", priv, b64(k.PublicKey().Bytes()), pub)
	}
	return k
}

// The RFC's example, step by step: the shared secret, the content key and nonce, and the body byte for byte.
func TestEncryptMatchesRFC8291Example(t *testing.T) {
	as := vecKey(t, vecASPrivate, vecASPublic)
	ua := vecKey(t, vecUAPrivate, vecUAPublic)
	auth, salt := mustB64(t, vecAuth), mustB64(t, vecSalt)
	shared, err := as.ECDH(ua.PublicKey())
	if err != nil || b64(shared) != vecShared {
		t.Fatalf("shared secret %s (%v), want %s", b64(shared), err, vecShared)
	}
	cek, nonce, err := contentKeys(ua.PublicKey().Bytes(), as.PublicKey().Bytes(), auth, salt, func() ([]byte, error) { return shared, nil })
	if err != nil || b64(cek) != vecCEK || b64(nonce) != vecNonce {
		t.Fatalf("cek %s nonce %s (%v), want %s %s", b64(cek), b64(nonce), err, vecCEK, vecNonce)
	}
	body, err := encrypt(ua.PublicKey(), auth, []byte(vecPlaintext), salt, as)
	if err != nil {
		t.Fatal(err)
	}
	if got := b64(body[:headerSize]); got != vecHeader {
		t.Fatalf("header\n got %s\nwant %s", got, vecHeader)
	}
	if got := b64(body[headerSize:]); got != vecCipher {
		t.Fatalf("ciphertext\n got %s\nwant %s", got, vecCipher)
	}
	if plain, err := decrypt(ua, auth, body); err != nil || string(plain) != vecPlaintext {
		t.Fatalf("decrypted %q (%v)", plain, err)
	}
}

// decrypt is what the browser does with a body (RFC 8291 section 3.4, one record), for the tests.
func decrypt(ua *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) < headerSize+tagSize {
		return nil, errors.New("short body")
	}
	salt := body[:saltSize]
	if rs := binary.BigEndian.Uint32(body[saltSize:]); rs != recordSize {
		return nil, errors.New("record size")
	}
	if body[saltSize+4] != 65 {
		return nil, errors.New("key id length")
	}
	asPub, err := ecdh.P256().NewPublicKey(body[saltSize+5 : headerSize])
	if err != nil {
		return nil, err
	}
	cek, nonce, err := contentKeys(ua.PublicKey().Bytes(), asPub.Bytes(), auth, salt, func() ([]byte, error) { return ua.ECDH(asPub) })
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[headerSize:], nil)
	if err != nil {
		return nil, err
	}
	i := bytes.LastIndexByte(plain, 2)
	if i < 0 || bytes.ContainsFunc(plain[i+1:], func(r rune) bool { return r != 0 }) {
		return nil, errors.New("no delimiter")
	}
	return plain[:i], nil
}

// browser is a subscriber: its keys and the Subscription it would give.
func browser(t *testing.T, endpoint string) (*ecdh.PrivateKey, []byte, Subscription) {
	t.Helper()
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return ua, auth, Subscription{Endpoint: endpoint, P256DH: b64(ua.PublicKey().Bytes()), Auth: b64(auth)}
}

// Each push gets its own salt and key; the browser opens it, up to MaxPlaintext bytes whose body fits MaxBody.
func TestEncryptRoundTripsUpToTheLimit(t *testing.T) {
	ua, auth, sub := browser(t, "https://fcm.googleapis.com/fcm/send/abc")
	for _, n := range []int{0, 1, 100, MaxPlaintext} {
		msg := bytes.Repeat([]byte("x"), n)
		a, err := Encrypt(sub, msg)
		if err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		b, _ := Encrypt(sub, msg)
		if bytes.Equal(a[:saltSize], b[:saltSize]) || bytes.Equal(a[saltSize+5:headerSize], b[saltSize+5:headerSize]) {
			t.Fatal("salt or key reused")
		}
		if len(a) > MaxBody {
			t.Fatalf("%d bytes encrypt to %d, over %d", n, len(a), MaxBody)
		}
		if plain, err := decrypt(ua, auth, a); err != nil || !bytes.Equal(plain, msg) {
			t.Fatalf("%d: %v", n, err)
		}
	}
	if _, err := Encrypt(sub, make([]byte, MaxPlaintext+1)); err == nil {
		t.Fatal("over the limit encrypted")
	}
	// What browsers give is base64url; padded or standard base64 is read too.
	padded := sub
	padded.Auth = strings.NewReplacer("-", "+", "_", "/").Replace(sub.Auth) + "=="
	if _, err := Encrypt(padded, []byte("x")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Subscription{{Endpoint: sub.Endpoint, P256DH: "AAAA", Auth: sub.Auth}, {Endpoint: sub.Endpoint, P256DH: sub.P256DH, Auth: "AAAA"}} {
		if bad.Check() == nil {
			t.Fatalf("%+v passed", bad)
		}
	}
}

// The header is an ES256 JWT for the endpoint's origin that the key's public half verifies, and that public half.
func TestAuthorizationIsAVerifiableVAPIDToken(t *testing.T) {
	k, err := NewKeys()
	if err != nil || !k.Valid() {
		t.Fatal(k, err)
	}
	exp := time.Unix(1_800_000_000, 0)
	h, err := Authorization("https://updates.push.services.mozilla.com/wpush/v2/abc?x=1", "https://example.com/persona", k, exp)
	if err != nil {
		t.Fatal(err)
	}
	tok, key, ok := strings.Cut(strings.TrimPrefix(h, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(h, "vapid t=") || key != k.Public {
		t.Fatalf("header %q", h)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", tok)
	}
	var head, claims map[string]any
	_ = json.Unmarshal(mustB64(t, parts[0]), &head)
	_ = json.Unmarshal(mustB64(t, parts[1]), &claims)
	if head["alg"] != "ES256" || head["typ"] != "JWT" {
		t.Fatalf("header %v", head)
	}
	if claims["aud"] != "https://updates.push.services.mozilla.com" || claims["exp"] != float64(exp.Unix()) || claims["sub"] != "https://example.com/persona" {
		t.Fatalf("claims %v", claims)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), mustB64(t, k.Public))
	if err != nil {
		t.Fatal(err)
	}
	sig := mustB64(t, parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	// A key pair whose halves don't match signs nothing.
	other, _ := NewKeys()
	if _, err := Authorization("https://fcm.googleapis.com/x", "", Keys{Private: k.Private, Public: other.Public}, exp); err == nil {
		t.Fatal("mismatched pair signed")
	}
}

func TestNewRequestCarriesTheHeadersAndAnOpenableBody(t *testing.T) {
	k, _ := NewKeys()
	ua, auth, sub := browser(t, "https://web.push.apple.com/QGuQ")
	now := time.Unix(1_800_000_000, 0)
	req, err := NewRequest(context.Background(), sub, k, "https://example.com", []byte(`{"title":"t"}`), Message{TTL: 30 * time.Minute, Urgency: UrgencyHigh, Topic: TopicFor("m/s#needs")}, now)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL.String() != sub.Endpoint {
		t.Fatalf("%s %s", req.Method, req.URL)
	}
	for k, want := range map[string]string{"Content-Encoding": "aes128gcm", "Content-Type": "application/octet-stream", "TTL": "1800", "Urgency": "high", "Topic": TopicFor("m/s#needs")} {
		if got := req.Header.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "vapid t=") {
		t.Fatalf("authorization %q", req.Header.Get("Authorization"))
	}
	body, _ := io.ReadAll(req.Body)
	if plain, err := decrypt(ua, auth, body); err != nil || string(plain) != `{"title":"t"}` {
		t.Fatalf("%q %v", plain, err)
	}
	if topic := TopicFor(strings.Repeat("long key ", 50)); len(topic) > 32 || strings.ContainsAny(topic, "+/=") {
		t.Fatalf("topic %q", topic)
	}
}

// Only a plain https URL at a known push service: a client cannot point the hub at an address of its own.
func TestCheckEndpointTakesOnlyKnownPushServices(t *testing.T) {
	for raw, host := range map[string]string{
		"https://fcm.googleapis.com/fcm/send/abc:def":                  "fcm.googleapis.com",
		"https://updates.push.services.mozilla.com/wpush/v2/gAAA":      "updates.push.services.mozilla.com",
		"https://web.push.apple.com/QGuQyavXutnMH":                     "web.push.apple.com",
		"https://wns2-par02p.notify.windows.com/w/?token=BQYAAAB":      "wns2-par02p.notify.windows.com",
		"https://FCM.googleapis.com:443/wp/abc":                        "fcm.googleapis.com",
		"https://updates.push.services.mozilla.com./wpush/v1/trailing": "updates.push.services.mozilla.com.",
	} {
		got, err := CheckEndpoint(raw)
		if err != nil || got != host {
			t.Errorf("%s: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://fcm.googleapis.com:8443/fcm/send/abc",
		(&url.URL{Scheme: "https", User: url.UserPassword("user", "pw"), Host: "fcm.googleapis.com", Path: "/fcm/send/abc"}).String(),
		"https://evilfcm.googleapis.com.example.com/x",
		"https://notfcm.googleapis.com/x",
		"https://xpush.apple.com/x",
		"https://127.0.0.1/x",
		"https://localhost/x",
		"https://example.com/push",
		"fcm.googleapis.com/fcm/send/abc",
		"https://fcm.googleapis.com/" + strings.Repeat("a", maxEndpoint),
		"",
	} {
		if host, err := CheckEndpoint(raw); err == nil {
			t.Errorf("%s passed as %q", raw, host)
		}
	}
	if !errors.Is(func() error { _, err := CheckEndpoint("https://example.com/x"); return err }(), ErrUnknownService) {
		t.Fatal("unknown host not ErrUnknownService")
	}
	if Service("web.push.apple.com") != "Apple" || Service("fcm.googleapis.com") != "FCM" || Service("example.com") != "" {
		t.Fatal("service names")
	}
}

func TestStatusMeaning(t *testing.T) {
	for status, gone := range map[int]bool{404: true, 410: true, 201: false, 400: false, 403: false, 413: false} {
		if Gone(status) != gone {
			t.Errorf("Gone(%d)", status)
		}
	}
	for status, retry := range map[int]bool{0: true, 429: true, 500: true, 503: true, 201: false, 400: false, 403: false, 410: false} {
		if Retryable(status) != retry {
			t.Errorf("Retryable(%d)", status)
		}
	}
}
