package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/nwverse/persona/server/internal/i18n"
)

// --- running it ---------------------------------------------------------------------------------------------------

func (e *setupEnv) health(ctx context.Context, cfg *setupConfig) error {
	wait := e.healthWait
	if e.o.noService {
		wait = min(wait, 5*time.Second) // nothing was started here: it runs already, or not
	}
	deadline := time.Now().Add(wait)
	for {
		if e.hubAnswers(ctx, cfg.Config) {
			e.report("health", true, i18n.T(e.l, "hub-setup.health_ok"), "")
			return nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			break
		}
		e.sleep(time.Second)
	}
	next := i18n.T(e.l, "hub-setup.health_next_journal")
	switch {
	case e.o.noService:
		next = i18n.T(e.l, "hub-setup.health_next_no_service")
	case e.goos == "darwin":
		next = i18n.T(e.l, "hub-setup.health_next_log", i18n.Args{"path": filepath.Join(e.root, "hub.log")})
	}
	return e.fail("health", i18n.T(e.l, "hub-setup.health_timeout", i18n.Args{"count": int(wait.Seconds()), "port": portOf(cfg.Listen)}), next)
}

func loopbackClient() (*http.Client, func()) {
	transport := &http.Transport{Proxy: nil}
	return &http.Client{Timeout: 5 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport.CloseIdleConnections
}

// hubAnswers: the hub this hub.json describes answers on loopback (with its token: another hub on the port does not).
func (e *setupEnv) hubAnswers(ctx context.Context, cfg *Config) bool {
	client, done := loopbackClient()
	defer done()
	for _, path := range []string{"/api/v1/health", "/api/v1/endpoints"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", portOf(cfg.Listen))+path, nil)
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
	}
	return true
}

// finish prints the way in: the phone's QR code and the admin URL with the setup code (docs/assistant/onboarding.md 4.1).
func (e *setupEnv) finish(ctx context.Context, cfg *setupConfig) error {
	p, err := cfg.requestPairCode(ctx)
	if err != nil {
		return e.fail("health", i18n.T(e.l, "hub-setup.pair_code_failed", i18n.Args{"error": err.Error()}),
			i18n.T(e.l, "hub-setup.pair_code_failed_next"))
	}
	code, codeExpires, err := e.setupCode(ctx, cfg.Config)
	if err != nil {
		return e.fail("health", i18n.T(e.l, "hub-setup.setup_code_failed", i18n.Args{"error": err.Error()}),
			i18n.T(e.l, "hub-setup.setup_code_failed_next"))
	}
	adminURL, localOnly := e.adminURL(cfg.Config)
	if code != "" {
		adminURL += "#setup=" + url.QueryEscape(code)
	}
	expires := p.ExpiresAt
	if code != "" && codeExpires.Before(expires) {
		expires = codeExpires
	}
	if e.o.json {
		done := map[string]any{"done": true, "pair_url": p.PairURL, "admin_url": adminURL, "admin_local_only": localOnly,
			"setup_code": code, "expires_at": expires.UTC().Format(time.RFC3339)}
		b, _ := marshalNoEscape(done)
		_, _ = e.out.Write(append(b, '\n'))
		return nil
	}
	fmt.Fprintf(e.out, "\n%s\n\n%s\n\n", i18n.T(e.l, "hub-setup.done"),
		i18n.T(e.l, "hub-setup.pair_qr", i18n.Args{"count": minutesLeft(p.ExpiresAt)}))
	qrterminal.GenerateHalfBlock(p.PairURL, qrterminal.L, e.out)
	fmt.Fprintf(e.out, "\n   %s\n\n", i18n.T(e.l, "hub-setup.pair_link", i18n.Args{"url": p.PairURL}))
	browser := i18n.T(e.l, "hub-setup.browser_phone")
	if localOnly {
		browser = i18n.T(e.l, "hub-setup.browser_local")
	}
	if code != "" {
		fmt.Fprintf(e.out, "%s\n   %s\n", i18n.T(e.l, "hub-setup.admin_open", i18n.Args{"browser": browser, "count": minutesLeft(codeExpires)}), adminURL)
	} else {
		fmt.Fprintf(e.out, "%s\n   %s\n", i18n.T(e.l, "hub-setup.admin_set"), adminURL)
	}
	if localOnly {
		fmt.Fprintf(e.out, "   %s\n", i18n.T(e.l, "hub-setup.admin_local_only",
			i18n.Args{"listen": cfg.Listen, "path": e.hubJSON(), "port": portOf(cfg.Listen)}))
	}
	fmt.Fprintln(e.out, "\n"+i18n.T(e.l, "hub-setup.expired_hint"))
	return nil
}

func minutesLeft(t time.Time) int { return max(1, int(time.Until(t).Round(time.Minute).Minutes())) }

// setupCode asks the running hub for a first-run setup code ("" when the admin password is set already).
func (e *setupEnv) setupCode(ctx context.Context, cfg *Config) (string, time.Time, error) {
	resp, err := adminLoopbackPost(ctx, cfg, "/api/v1/admin/setup-code") // `hub admin setup-code`'s request
	if err != nil {
		return "", time.Time{}, errors.New(i18n.T(e.l, "hub-setup.hub_unreachable"))
	}
	defer resp.Body.Close()
	var body struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(raw, &body); err != nil || body.Code == "" {
			return "", time.Time{}, errors.New(i18n.T(e.l, "hub-setup.hub_invalid_reply"))
		}
		return body.Code, body.ExpiresAt, nil
	case http.StatusConflict: // already_set
		return "", time.Time{}, nil
	}
	return "", time.Time{}, fmt.Errorf("HTTP %d", resp.StatusCode)
}

// adminURL is where the admin UI opens, on the plaintext port: a hub listening on loopback only answers there
// (localOnly: only a browser on this machine can open it), one bound to a single address answers on that one, and
// one on every address is opened by a phone on the LAN at this machine's LAN address.
func (e *setupEnv) adminURL(cfg *Config) (u string, localOnly bool) {
	port := portOf(cfg.Listen)
	if h, _, err := net.SplitHostPort(cfg.Listen); err == nil {
		switch ip := net.ParseIP(h); {
		case h == "localhost" || (ip != nil && ip.IsLoopback()):
			if ip == nil || ip.To4() != nil {
				h = "127.0.0.1"
			}
			return "http://" + net.JoinHostPort(h, port) + "/admin", true
		case ip != nil && !ip.IsUnspecified():
			return "http://" + net.JoinHostPort(h, port) + "/admin", false
		}
	}
	host := e.lanIPv4()
	if host == "" {
		if u, err := url.Parse(cfg.PublicURL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
	}
	if host == "" {
		host = i18n.T(e.l, "hub-setup.lan_ip_placeholder")
	}
	return "http://" + net.JoinHostPort(host, port) + "/admin", false
}
