package extmgr

// settings.check (#5906, docs/extensions/extensions.md "Settings the user fills in"): before the settings or a secret of an
// extension that declares settings_check are saved, its program is asked whether they work as they would be after the
// change (a gateway's address and key: the address right, the key taken, the gateway reachable). A refusal saves
// nothing and its words go back to the user, with every secret handed to the check taken out of them first; the check
// has a time limit and a rate limit, so that it is no oracle for guessing keys.

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

var (
	// settingsCheckTimeout: a check not answered by then is a failure (nothing saved).
	settingsCheckTimeout = 10 * time.Second
	// settingsChecksMost checks of one extension per settingsCheckWindow; more are refused before reaching it.
	settingsChecksMost  = 6
	settingsCheckWindow = time.Minute
	// settingsCheckProxyGrace: a check's proxy credential (checkProxy) stops working by itself this long after the
	// check's time limit, should it not be revoked when the check answers.
	settingsCheckProxyGrace = 5 * time.Second
)

// settingsCheckMessageMost is the most of an extension's refusal that goes back to the user (runes).
const settingsCheckMessageMost = 300

// settingsCheckFailed is the extension's refusal of a change, its words already scrubbed (the route answers
// check_failed).
type settingsCheckFailed struct{ msg string }

func (e settingsCheckFailed) Error() string { return e.msg }

// settingsCheckLimited: too many checks of one extension lately (the route answers check_rate_limited).
type settingsCheckLimited struct{ error }

func (e settingsCheckLimited) Unwrap() error { return e.error }

// checkLimiter counts each extension's checks (or, with most and window set, any calls) in the current window.
type checkLimiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
	now  func() time.Time
	// most per window; zero is a settings check's (settingsChecksMost per settingsCheckWindow).
	most   int
	window time.Duration
}

func (l *checkLimiter) allow(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.seen == nil {
		l.seen = map[string][]time.Time{}
	}
	most, window := l.most, l.window
	if most == 0 {
		most, window = settingsChecksMost, settingsCheckWindow
	}
	keep := l.seen[id][:0]
	for _, t := range l.seen[id] {
		if now.Sub(t) < window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= most {
		l.seen[id] = keep
		return false
	}
	l.seen[id] = append(keep, now)
	return true
}

// settingsCheck is the registry's SettingsCheck: extension p's program judges config and secrets.
func (ps *extensions) settingsCheck(ctx context.Context, p *extension.Extension, config json.RawMessage, secrets map[string]string) error {
	id := p.Record.ID
	hp := ps.get(id)
	if p.Record.Enabled && hp != nil && hp.proc == nil && hp.status == extensionNeedsSetup {
		return nil // it runs only once its settings are in, so the first ones go in unchecked
	}
	if !ps.checks.allow(id) {
		return settingsCheckLimited{i18n.Err("hub-extension_settings.check_rate_limited")}
	}
	lang := i18n.Default
	if ps.h != nil {
		lang = ps.h.userLang()
	}
	if !p.Record.Enabled || hp == nil || hp.proc == nil {
		return settingsCheckFailed{i18n.T(lang, "hub-extension_settings.check_not_running")}
	}
	// A reference among them is checked as the value it stands for (saving one is the owner's approval).
	secrets, err := ps.resolveStored(ctx, id, secrets)
	if err != nil {
		return settingsCheckFailed{secretRefError(err, lang)}
	}
	params := wire.SettingsCheckParams{Config: config, Secrets: secrets}
	scrub := secrets
	if proxy, cred, revoke := ps.checkProxy(p, config); revoke != nil {
		defer revoke() // the check's way to the proposed address ends with it, however it ends
		params.Proxy = proxy
		scrub = maps.Clone(secrets)
		if scrub == nil {
			scrub = map[string]string{}
		}
		scrub["\x00proxy"] = cred // a refusal that quotes the proxy URL gives back no credential
	}
	cctx, cancel := context.WithTimeout(ctx, settingsCheckTimeout)
	defer cancel()
	out, err := buswire.Call(cctx, hp.proc, extwire.SettingsCheckMethod, params)
	switch {
	case cctx.Err() != nil && ctx.Err() == nil:
		return settingsCheckFailed{i18n.T(lang, "hub-extension_settings.check_timeout", i18n.Args{"seconds": int(settingsCheckTimeout / time.Second)})}
	case err != nil:
		var re *rpc.Error
		if errors.As(err, &re) && re.Message != "" {
			return settingsCheckFailed{scrubSecrets(re.Message, scrub)}
		}
		return settingsCheckFailed{i18n.T(lang, "hub-extension_settings.check_failed_plain")}
	case !out.Ok:
		msg := scrubSecrets(out.Error, scrub)
		if strings.TrimSpace(msg) == "" {
			msg = i18n.T(lang, "hub-extension_settings.check_failed_plain")
		}
		return settingsCheckFailed{msg}
	}
	return nil
}

// checkProxy is the egress proxy URL settings.check gives p's program (#8459): a credential of this check's own,
// whose requests reach the addresses config would name, in place of the saved ones, until revoke or the check's time
// is up; the program's ordinary requests keep the saved ones (#1180). cred is the credential, to keep it out of a
// refusal. Nothing (revoke nil) when the program is not held to the proxy, or no config: entry of its granted
// permissions.network names an address.
func (ps *extensions) checkProxy(p *extension.Extension, config json.RawMessage) (proxy, cred string, revoke func()) {
	granted := &extension.Manifest{Permissions: extension.Permissions{Network: p.Record.Granted.Network}}
	if len(granted.Permissions.ConfiguredNetworks()) == 0 {
		return "", "", nil
	}
	px := ps.sandboxes.proxyIfStarted()
	if px == nil {
		return "", "", nil
	}
	id := p.Record.ID
	cred, revoke, ok := px.Check(id, extension.ConfiguredTargets(granted, config), settingsCheckTimeout+settingsCheckProxyGrace)
	if !ok {
		return "", "", nil // not registered: its sandbox is off, so nothing holds it to the proxy
	}
	addr := px.Addr()
	if px.Socketed(id) { // Linux's sandbox: its own loopback leads to its socket (sandbox.Linux)
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(sandbox.ProxyPort))
	}
	return (&url.URL{Scheme: "http", User: url.UserPassword(egress.User, cred), Host: addr}).String(), cred, revoke
}

// scrubFragmentMin: a prefix or suffix of a secret this long or longer is taken out of a refusal too (shorter ones are
// too likely to be ordinary words).
const scrubFragmentMin = 6

// scrubSecrets takes every secret out of msg (written as ***): whole, and the longest prefix and suffix of each of at
// least scrubFragmentMin characters that msg holds (an extension that cut the key short); then cuts msg to
// settingsCheckMessageMost.
func scrubSecrets(msg string, secrets map[string]string) string {
	for _, v := range secrets {
		if len([]rune(v)) < 4 {
			continue
		}
		msg = strings.ReplaceAll(msg, v, "***")
		r := []rune(v)
		for n := len(r) - 1; n >= scrubFragmentMin; n-- {
			msg = strings.ReplaceAll(msg, string(r[:n]), "***")
			msg = strings.ReplaceAll(msg, string(r[len(r)-n:]), "***")
		}
	}
	if r := []rune(msg); len(r) > settingsCheckMessageMost {
		msg = string(r[:settingsCheckMessageMost]) + "…"
	}
	return msg
}

// writeSettingsCheckError answers a change the check refused (400 check_failed, the extension's scrubbed words),
// one refused for too many checks (429 check_rate_limited), or one the settings moved under (409
// settings_moved); false for any other error.
func writeSettingsCheckError(w http.ResponseWriter, r *http.Request, err error) bool {
	var failed settingsCheckFailed
	switch {
	case errors.As(err, &failed):
		writeExtensionResponseError(w, r, http.StatusBadRequest, "check_failed", err)
	case errors.As(err, new(settingsCheckLimited)):
		writeExtensionResponseError(w, r, http.StatusTooManyRequests, "check_rate_limited", err)
	case errors.Is(err, extension.ErrSettingsMoved):
		writeExtensionResponseError(w, r, http.StatusConflict, "settings_moved", err)
	default:
		return false
	}
	return true
}
