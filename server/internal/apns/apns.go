// Package apns sends to Apple Push Notification service with a token-based (.p8) key. The hub uses it when it has
// its own key (~/.persona/apns.json); the push relay (personad relay, docs/operations/push-relay.md) uses it for everyone else.
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is the key: ~/.persona/apns.json on a hub, the "apns" object in relay.json.
type Config struct {
	KeyID   string `json:"key_id"`
	TeamID  string `json:"team_id"`
	KeyPath string `json:"key_path"`
	Topic   string `json:"topic,omitempty"` // the hub's default topic when a device registered none
}

// Client signs provider tokens and posts notifications. Safe for concurrent use.
type Client struct {
	Cfg  Config
	key  *ecdsa.PrivateKey
	http *http.Client
	// Base replaces Apple's host (tests).
	Base string

	mu  sync.Mutex
	jwt string
	// jwtIat is the cached token's iat in Unix seconds. Its age is measured on the wall clock, the clock APNs
	// checks iat against: a monotonic reading stops while the machine sleeps, so a Mac hub waking up after hours
	// would keep sending a token Apple already rejects as expired.
	jwtIat int64
}

// tokenMaxAge is how long a signed token is reused. APNs rejects tokens older than an hour and answers 429
// TooManyProviderTokenUpdates when they are re-signed more often than every 20 minutes.
const tokenMaxAge = 40 * 60

// New loads the .p8 key the config names ("~/" is the home directory). It refuses a config without key_id or
// team_id: every token signed without them is answered 403 InvalidProviderToken.
func New(cfg Config) (*Client, error) {
	cfg.KeyID, cfg.TeamID = strings.TrimSpace(cfg.KeyID), strings.TrimSpace(cfg.TeamID)
	if cfg.KeyID == "" {
		return nil, errors.New("apns: key_id is empty")
	}
	if cfg.TeamID == "" {
		return nil, errors.New("apns: team_id is empty")
	}
	path := cfg.KeyPath
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = home + path[1:]
	}
	keyPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("bad key file")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	ek, ok := k.(*ecdsa.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("key is not an EC private key")
	}
	if err := checkSigningKey(ek); err != nil {
		return nil, err
	}
	return NewWithKey(cfg, ek), nil
}

// checkSigningKey enforces ES256's curve and 32-byte signature components before signing.
func checkSigningKey(key *ecdsa.PrivateKey) error {
	if key == nil || key.Curve != elliptic.P256() {
		return errors.New("apns: signing key must use P-256 for ES256")
	}
	return nil
}

// NewWithKey uses a key already in memory (tests).
func NewWithKey(cfg Config, key *ecdsa.PrivateKey) *Client {
	return &Client{Cfg: cfg, key: key, http: &http.Client{Timeout: 15 * time.Second, Transport: NewTransport()}}
}

// Connection keepalive of NewTransport (#4800). APNs asks providers to keep one HTTP/2 connection open rather than
// connect per notification, and the hub's relay client wants the same of the relay. http.DefaultTransport closed an
// idle connection after 90 s, so most pushes paid TCP + TLS again (0.2-0.3 s to the relay). An idle connection now
// lives for idleConnTimeout, and HTTP/2 pings sent after pingAfter without a frame keep it from looking idle to the
// network in between and find a connection that died silently, which is closed when its pong is pingTimeout late.
const (
	idleConnTimeout = time.Hour
	pingAfter       = time.Minute
	pingTimeout     = 15 * time.Second
)

// NewTransport is a transport for one push service: http.DefaultTransport's settings (proxy from the environment,
// dial and TLS handshake timeouts, HTTP/2), with connections kept between pushes. Each client gets its own, so no
// other client's traffic shares or closes its connections.
func NewTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.IdleConnTimeout = idleConnTimeout
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: pingAfter, PingTimeout: pingTimeout}
	return t
}

// token returns the cached provider token, signing a new one when there is none or it is tokenMaxAge old.
func (c *Client) token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokenLocked()
}

// resign drops stale, the token APNs just called expired, and returns a fresh one. When another request already
// replaced stale, that newer token is returned instead of signing again.
func (c *Client) resign(stale string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwt == stale {
		c.jwt = ""
	}
	return c.tokenLocked()
}

func (c *Client) tokenLocked() (string, error) {
	if err := checkSigningKey(c.key); err != nil {
		return "", err
	}
	now := time.Now().Unix()
	if c.jwt != "" && now >= c.jwtIat && now-c.jwtIat < tokenMaxAge {
		return c.jwt, nil
	}
	headerJSON, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}{Alg: "ES256", Kid: c.Cfg.KeyID})
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}{Iss: c.Cfg.TeamID, Iat: now})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString(headerJSON)
	claims := enc.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(header + "." + claims))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	c.jwt, c.jwtIat = header+"."+claims+"."+enc.EncodeToString(sig), now
	return c.jwt, nil
}

// Request is one notification.
type Request struct {
	Token      string // device token (hex), or a Live Activity's
	Env        string // "sandbox" or production
	Topic      string // bundle id; for liveactivity the ".push-type.liveactivity" suffix is added here, for voip ".voip"
	PushType   string // alert | background | liveactivity | voip
	Priority   string // "10" | "5"
	CollapseID string
	// Expiration is when APNs stops trying to deliver (Unix seconds); 0 sends no apns-expiration, so APNs keeps the
	// push for a while as it chooses.
	Expiration int64
	Body       []byte
}

// Result is APNs' answer when it answered at all.
type Result struct {
	Status int
	Reason string
}

// Gone: the token is dead and should be forgotten.
func (r Result) Gone() bool {
	return r.Status == 410 || r.Reason == "BadDeviceToken" || r.Reason == "Unregistered"
}

// Host is APNs' host for an environment.
func Host(env string) string {
	if env == "sandbox" {
		return "https://api.sandbox.push.apple.com"
	}
	return "https://api.push.apple.com"
}

// maxResponseBodyBytes bounds how much of an APNs response body is decoded and drained for connection reuse.
const maxResponseBodyBytes = 64 << 10

// maxDeviceTokenLen bounds device, VoIP, and Live Activity tokens accepted into APNs' /3/device/<token> path.
const maxDeviceTokenLen = 512

// ErrInvalidDeviceToken reports a malformed device token rejected locally before contacting APNs.
var ErrInvalidDeviceToken = errors.New("apns: invalid device token")

// validDeviceToken enforces a strict ASCII identifier allowlist ([A-Za-z0-9_-], 1..maxDeviceTokenLen)
// so untrusted tokens cannot traverse paths, inject path parameters/queries/fragments, or bloat HTTP/2 :path.
func validDeviceToken(token string) error {
	if len(token) == 0 || len(token) > maxDeviceTokenLen {
		return ErrInvalidDeviceToken
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return ErrInvalidDeviceToken
		}
	}
	return nil
}

// Send posts one notification and waits for APNs' answer. A 403 ExpiredProviderToken is retried once with a newly
// signed token; InvalidProviderToken is not, since a wrong key or id stays wrong.
func (c *Client) Send(ctx context.Context, r Request) (Result, error) {
	if err := validDeviceToken(r.Token); err != nil {
		return Result{}, err
	}
	jwt, err := c.token()
	if err != nil {
		return Result{}, fmt.Errorf("apns jwt: %w", err)
	}
	res, err := c.post(ctx, r, jwt)
	if err != nil || res.Status != http.StatusForbidden || res.Reason != "ExpiredProviderToken" {
		return res, err
	}
	if jwt, err = c.resign(jwt); err != nil {
		return Result{}, fmt.Errorf("apns jwt: %w", err)
	}
	return c.post(ctx, r, jwt)
}

// post sends r once with the provider token jwt.
func (c *Client) post(ctx context.Context, r Request, jwt string) (Result, error) {
	if err := validDeviceToken(r.Token); err != nil {
		return Result{}, err
	}
	host := Host(r.Env)
	if c.Base != "" {
		host = c.Base
	}
	topic := r.Topic
	if topic == "" {
		topic = c.Cfg.Topic
	}
	if topic != "" {
		switch {
		case r.PushType == "liveactivity" && !strings.HasSuffix(topic, ".push-type.liveactivity"):
			topic += ".push-type.liveactivity"
		case r.PushType == "voip" && !strings.HasSuffix(topic, ".voip"):
			topic += ".voip"
		}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", host+"/3/device/"+r.Token, bytes.NewReader(r.Body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", topic)
	req.Header.Set("apns-push-type", r.PushType)
	req.Header.Set("apns-priority", r.Priority)
	if r.CollapseID != "" {
		req.Header.Set("apns-collapse-id", r.CollapseID)
	}
	if r.Expiration > 0 {
		req.Header.Set("apns-expiration", strconv.FormatInt(r.Expiration, 10))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The APNs connection is one long-lived HTTP/2 connection. Health pings (NewTransport) close one that
		// died silently within about a minute, but a push can still go out on it first: after the hub's proxy
		// routing broke and came back, every push timed out on the dead connection until the hub restarted.
		// Drop it so the next push dials a new one.
		c.http.CloseIdleConnections()
		return Result{}, err
	}
	limited := io.LimitReader(resp.Body, maxResponseBodyBytes)
	defer func() {
		_, _ = io.Copy(io.Discard, limited)
		_ = resp.Body.Close()
	}()
	res := Result{Status: resp.StatusCode}
	if resp.StatusCode != 200 {
		var e struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(limited).Decode(&e)
		res.Reason = e.Reason
	}
	return res, nil
}
