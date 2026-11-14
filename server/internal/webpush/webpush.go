// Package webpush sends a push to a browser's push subscription (docs/web/web.md "Web Push"): RFC 8030 delivery,
// RFC 8291 message encryption (aes128gcm) and RFC 8292 VAPID, with the standard library only. It knows nothing of the
// hub: what to send and to whom is the hub's (hub/webpush.go).
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Keys is a VAPID key pair (RFC 8292): Private is the P-256 scalar (32 bytes), Public the uncompressed point (65
// bytes), both base64url without padding: Public is what a browser takes as applicationServerKey.
type Keys struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}

// NewKeys makes a VAPID key pair.
func NewKeys() (Keys, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Keys{}, err
	}
	priv, err := k.Bytes()
	if err != nil {
		return Keys{}, err
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return Keys{}, err
	}
	return Keys{Private: b64(priv), Public: b64(pub)}, nil
}

// signer is k's private key, checked against its public half.
func (k Keys) signer() (*ecdsa.PrivateKey, error) {
	raw, err := unb64(k.Private)
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	if k.Public != "" && b64(pub) != k.Public {
		return nil, errors.New("vapid key pair does not match")
	}
	return priv, nil
}

// Valid reports whether k is a usable key pair.
func (k Keys) Valid() bool {
	_, err := k.signer()
	return err == nil && k.Public != ""
}

// Subscription is a browser's PushSubscription as its toJSON() gives it.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	// P256DH is the browser's ECDH public key (uncompressed P-256), Auth its 16-byte authentication secret, both
	// base64url.
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// keys decodes s's keys.
func (s Subscription) keys() (*ecdh.PublicKey, []byte, error) {
	raw, err := unb64(s.P256DH)
	if err != nil {
		return nil, nil, fmt.Errorf("p256dh: %w", err)
	}
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("p256dh: %w", err)
	}
	auth, err := unb64(s.Auth)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: %w", err)
	}
	if len(auth) != 16 {
		return nil, nil, fmt.Errorf("auth: %d bytes, not 16", len(auth))
	}
	return pub, auth, nil
}

// Check reports what is wrong with s, if anything: its endpoint (CheckEndpoint) or its keys (CheckKeys).
func (s Subscription) Check() error {
	if _, err := CheckEndpoint(s.Endpoint); err != nil {
		return err
	}
	return s.CheckKeys()
}

// CheckKeys reports what is wrong with s's keys, if anything.
func (s Subscription) CheckKeys() error {
	_, _, err := s.keys()
	return err
}

// The aes128gcm content coding (RFC 8188) as RFC 8291 uses it: one record of at most recordSize bytes, its header
// the salt, the record size, and the sender's public key.
const (
	recordSize = 4096
	saltSize   = 16
	headerSize = saltSize + 4 + 1 + 65
	tagSize    = 16
	// MaxBody is the most a push service takes (RFC 8030 section 7.2 asks for at least 4096 bytes; FCM takes no more).
	MaxBody = 4096
	// MaxPlaintext is the most a message carries so that its encrypted body fits MaxBody: the header, the AEAD tag and
	// the one byte padding delimiter take the rest.
	MaxPlaintext = MaxBody - headerSize - tagSize - 1
)

// Encrypt encrypts plaintext to s (RFC 8291): the body of the push, at most MaxBody bytes.
func Encrypt(s Subscription, plaintext []byte) ([]byte, error) {
	ua, auth, err := s.keys()
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(ua, auth, plaintext, salt, as)
}

// encrypt is Encrypt with the salt and the sender's key given (the RFC's test vector).
func encrypt(ua *ecdh.PublicKey, auth, plaintext, salt []byte, as *ecdh.PrivateKey) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, fmt.Errorf("message of %d bytes is over %d", len(plaintext), MaxPlaintext)
	}
	cek, nonce, err := contentKeys(ua.Bytes(), as.PublicKey().Bytes(), auth, salt, func() ([]byte, error) { return as.ECDH(ua) })
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, headerSize)
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(as.PublicKey().Bytes())))
	header = append(header, as.PublicKey().Bytes()...)
	// The last (only) record ends with the delimiter 2 and no padding (RFC 8188 section 2).
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 2)
	return gcm.Seal(header, nonce, record, nil), nil
}

// contentKeys derives the content encryption key and the nonce (RFC 8291 section 3.4) from the receiver's and the
// sender's public keys, the receiver's auth secret, the salt and their shared ECDH secret.
func contentKeys(uaPublic, asPublic, auth, salt []byte, shared func() ([]byte, error)) (cek, nonce []byte, err error) {
	secret, err := shared()
	if err != nil {
		return nil, nil, err
	}
	info := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, secret, auth, string(info), 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	if nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12); err != nil {
		return nil, nil, err
	}
	return cek, nonce, nil
}

// Authorization is the VAPID Authorization header (RFC 8292 section 3) for a push to endpoint: an ES256 JWT for the
// endpoint's origin, signed with k, expiring at exp (at most a day away), and k's public key. subject is a mailto: or
// https: URL the push service may reach the sender at.
func Authorization(endpoint, subject string, k Keys, exp time.Time) (string, error) {
	priv, err := k.signer()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("endpoint %q has no origin", endpoint)
	}
	header := b64([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub,omitempty"`
	}{u.Scheme + "://" + u.Host, exp.Unix(), subject})
	signing := header + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64(sig) + ", k=" + k.Public, nil
}

// Urgency of a push (RFC 8030 section 5.3): a browser on battery may hold back low ones.
const (
	UrgencyNormal = "normal"
	UrgencyHigh   = "high"
)

// Message is how a push is delivered: how long the push service keeps it for a browser that is away (0: it is
// dropped unless the browser is there now), its urgency, and its topic (a push waiting under the same topic replaces
// the earlier one, RFC 8030 section 5.4).
type Message struct {
	TTL     time.Duration
	Urgency string
	Topic   string
}

// vapidLife is how long a VAPID token stays valid: under the 24 hours push services allow.
const vapidLife = 12 * time.Hour

// NewRequest is the POST of plaintext, encrypted, to s, authorized with k for subject. The caller vets s's endpoint
// first (CheckEndpoint): this posts wherever it points.
func NewRequest(ctx context.Context, s Subscription, k Keys, subject string, plaintext []byte, m Message, now time.Time) (*http.Request, error) {
	body, err := Encrypt(s, plaintext)
	if err != nil {
		return nil, err
	}
	auth, err := Authorization(s.Endpoint, subject, k, now.Add(vapidLife))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(m.TTL/time.Second)))
	if m.Urgency != "" {
		req.Header.Set("Urgency", m.Urgency)
	}
	if m.Topic != "" {
		req.Header.Set("Topic", m.Topic)
	}
	return req, nil
}

// TopicFor is the Topic header for pushes about key: at most 32 base64url characters, as RFC 8030 asks.
func TopicFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return b64(sum[:16])
}

// Gone reports whether a push service's status says the subscription is no more (expired or unsubscribed).
func Gone(status int) bool { return status == http.StatusNotFound || status == http.StatusGone }

// Retryable reports whether a push service's status (0: the network's error) may pass if tried again.
func Retryable(status int) bool {
	return status == 0 || status == http.StatusTooManyRequests || status >= 500
}

// services are the push services the browsers use, by host: an endpoint elsewhere is refused, so a client cannot
// make the hub post to an address of its choosing. Chrome, Edge on Android, Opera, Brave, Samsung Internet: FCM;
// Firefox: Mozilla's autopush; Safari: Apple's; Edge on Windows and macOS: WNS.
var services = []struct {
	name, host string
	suffix     bool
}{
	{"FCM", "fcm.googleapis.com", false},
	{"Mozilla", "push.services.mozilla.com", true},
	{"Apple", "push.apple.com", true},
	{"Windows", "notify.windows.com", true},
}

// Service is the name of the push service at host ("FCM", "Mozilla", "Apple", "Windows"); "" for one it doesn't know.
func Service(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, s := range services {
		if host == s.host || s.suffix && strings.HasSuffix(host, "."+s.host) {
			return s.name
		}
	}
	return ""
}

// ErrUnknownService is an endpoint at a host that is no known push service.
var ErrUnknownService = errors.New("not a known push service")

// maxEndpoint bounds an endpoint's length (FCM's are about 200 characters).
const maxEndpoint = 1024

// CheckEndpoint returns the host of a subscription's endpoint: https, on the default port, without credentials, at
// a known push service (Service).
func CheckEndpoint(raw string) (string, error) {
	if len(raw) > maxEndpoint {
		return "", fmt.Errorf("endpoint of %d characters is over %d", len(raw), maxEndpoint)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("endpoint: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("endpoint is not a plain https URL")
	}
	if p := u.Port(); p != "" && p != "443" {
		return "", fmt.Errorf("endpoint on port %s", p)
	}
	if Service(u.Hostname()) == "" {
		return "", fmt.Errorf("%s: %w", u.Hostname(), ErrUnknownService)
	}
	return strings.ToLower(u.Hostname()), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// unb64 decodes base64url (what browsers give) and, leniently, standard base64, padded or not.
func unb64(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}
