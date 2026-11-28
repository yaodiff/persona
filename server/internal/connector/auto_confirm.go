package connector

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// autoConfirmNow is the clock guarded auto-confirm reads (tests pin it to stay deterministic without sleeps).
var autoConfirmNow = time.Now

var (
	autoConfirmSemverPattern = lazyre.New(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
	autoConfirmExtIDPattern  = lazyre.New(`^[a-z0-9][a-z0-9._-]*$`)
	defaultAutoConfirmSource = []string{"installed", "builtin"}
	knownAutoConfirmSources  = []string{"installed", "builtin", "hub.json"}
)

type ownerCallerKey struct{}

// WithOwnerCaller marks ctx as an authenticated, non-guest, non-work-machine owner assistant conversation eligible to
// evaluate hub-side guarded auto-confirm policies. Unmarked contexts default to false (fail closed).
func WithOwnerCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerCallerKey{}, true)
}

// WithoutOwnerCaller clears any owner-caller eligibility on ctx (used for non-assistant callers, guests, work machines
// and unattributed HTTP requests).
func WithoutOwnerCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerCallerKey{}, false)
}

// OwnerCallerOf reports whether ctx carries a verified owner assistant caller mark.
func OwnerCallerOf(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ownerCallerKey{}).(bool)
	return v
}

// ValidateAutoConfirmPolicies checks manager.json's connector_auto_confirm entries strictly so a typo, wildcard or
// unsupported protocol stops startup instead of silently weakening confirmation checks.
func ValidateAutoConfirmPolicies(policies []AutoConfirmPolicy) error {
	for i, p := range policies {
		prefix := fmt.Sprintf("connector_auto_confirm[%d]", i)
		if err := validatePolicyName(p.Name); err != nil {
			return fmt.Errorf("%s: name: %w", prefix, err)
		}
		if p.Extension == "" || hasGlobOrSpace(p.Extension) || !autoConfirmExtIDPattern.MatchString(p.Extension) {
			return fmt.Errorf("%s: extension %q must be an exact extension id without wildcards", prefix, p.Extension)
		}
		if !validID.MatchString(p.Connector) {
			return fmt.Errorf("%s: connector %q must be an exact connector id (lowercase letters and digits only)", prefix, p.Connector)
		}
		if len(p.Actions) == 0 {
			return fmt.Errorf("%s: actions must name at least one exact action", prefix)
		}
		seenActions := map[string]bool{}
		wantPrefix := p.Connector + "_"
		for j, act := range p.Actions {
			if act == "" || hasGlobOrSpace(act) || !strings.HasPrefix(act, wantPrefix) || len(act) == len(wantPrefix) {
				return fmt.Errorf("%s: actions[%d] %q must be an exact action starting with %q without wildcards", prefix, j, act, wantPrefix)
			}
			if seenActions[act] {
				return fmt.Errorf("%s: actions[%d] %q is listed more than once", prefix, j, act)
			}
			seenActions[act] = true
		}
		if p.Protocol != "" && p.Protocol != ApprovalProtocolGuardedAutoConfirmV1 {
			return fmt.Errorf("%s: unsupported protocol %q (supported: %q)", prefix, p.Protocol, ApprovalProtocolGuardedAutoConfirmV1)
		}
		var minVer, maxVer parsedSemver
		var hasMin, hasMax bool
		if p.MinVersion != "" {
			v, err := parseAutoConfirmSemver(p.MinVersion)
			if err != nil {
				return fmt.Errorf("%s: min_version %q: %w", prefix, p.MinVersion, err)
			}
			minVer, hasMin = v, true
		}
		if p.MaxVersion != "" {
			v, err := parseAutoConfirmSemver(p.MaxVersion)
			if err != nil {
				return fmt.Errorf("%s: max_version %q: %w", prefix, p.MaxVersion, err)
			}
			maxVer, hasMax = v, true
		}
		if hasMin && hasMax && minVer.compare(maxVer) > 0 {
			return fmt.Errorf("%s: min_version %q is newer than max_version %q", prefix, p.MinVersion, p.MaxVersion)
		}
		seenSources := map[string]bool{}
		for j, src := range p.Sources {
			if !slices.Contains(knownAutoConfirmSources, src) {
				return fmt.Errorf("%s: sources[%d] %q must be one of %v", prefix, j, src, knownAutoConfirmSources)
			}
			if seenSources[src] {
				return fmt.Errorf("%s: sources[%d] %q is listed more than once", prefix, j, src)
			}
			seenSources[src] = true
		}
		if p.MarketKey != "" && hasControlOrSpace(p.MarketKey) {
			return fmt.Errorf("%s: market_key must not contain whitespace or control characters", prefix)
		}
		if p.MaxTTLSeconds < 0 || p.MaxTTLSeconds > AutoConfirmHardMaxTTLSeconds {
			return fmt.Errorf("%s: max_ttl_seconds %d must be between 0 and %d", prefix, p.MaxTTLSeconds, AutoConfirmHardMaxTTLSeconds)
		}
	}
	return nil
}

func validatePolicyName(name string) error {
	if name == "" {
		return nil
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("must not have leading or trailing whitespace")
	}
	if len([]rune(name)) > 128 {
		return fmt.Errorf("exceeds 128 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("must not contain control characters")
		}
	}
	return nil
}

func hasGlobOrSpace(s string) bool {
	if strings.ContainsAny(s, "*?[\\]") {
		return true
	}
	return hasControlOrSpace(s)
}

func hasControlOrSpace(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// matchedAutoConfirm holds the resolved parameters of the first matching AutoConfirmPolicy.
type matchedAutoConfirm struct {
	name     string
	protocol string
	maxTTL   time.Duration
}

// matchAutoConfirmPolicy finds the first enabled hub-side policy matching a hub-verified extension identity, connector
// id and exact action name. Unidentified or remote-socket connectors never match.
func matchAutoConfirmPolicy(policies []AutoConfirmPolicy, ident ExtensionIdentity, connectorID, action string) (matchedAutoConfirm, bool) {
	if ident.Extension == "" || ident.Remote {
		return matchedAutoConfirm{}, false
	}
	for _, p := range policies {
		if p.Disabled {
			continue
		}
		if p.Extension != ident.Extension || p.Connector != connectorID || !slices.Contains(p.Actions, action) {
			continue
		}
		sources := p.Sources
		if len(sources) == 0 {
			sources = defaultAutoConfirmSource
		}
		if !slices.Contains(sources, ident.Source) {
			continue
		}
		if p.MarketKey != "" && ident.MarketKey != p.MarketKey {
			continue
		}
		if p.MinVersion != "" || p.MaxVersion != "" {
			cur, err := parseAutoConfirmSemver(ident.Version)
			if err != nil {
				continue
			}
			if p.MinVersion != "" {
				minV, err := parseAutoConfirmSemver(p.MinVersion)
				if err != nil || cur.compare(minV) < 0 {
					continue
				}
			}
			if p.MaxVersion != "" {
				maxV, err := parseAutoConfirmSemver(p.MaxVersion)
				if err != nil || cur.compare(maxV) > 0 {
					continue
				}
			}
		}
		proto := p.Protocol
		if proto == "" {
			proto = ApprovalProtocolGuardedAutoConfirmV1
		}
		if proto != ApprovalProtocolGuardedAutoConfirmV1 {
			continue
		}
		ttlSec := p.MaxTTLSeconds
		if ttlSec <= 0 {
			ttlSec = AutoConfirmDefaultMaxTTLSeconds
		}
		if ttlSec > AutoConfirmHardMaxTTLSeconds {
			ttlSec = AutoConfirmHardMaxTTLSeconds
		}
		name := p.Name
		if name == "" {
			name = p.Extension + "/" + p.Connector
		}
		return matchedAutoConfirm{
			name:     name,
			protocol: proto,
			maxTTL:   time.Duration(ttlSec) * time.Second,
		}, true
	}
	return matchedAutoConfirm{}, false
}

// validateAutoConfirmGrant checks that grant is a well-formed, non-expired, TTL-bounded credential.
func validateAutoConfirmGrant(grant *AutoConfirmGrant, now time.Time, maxTTL time.Duration) bool {
	if grant == nil || maxTTL <= 0 {
		return false
	}
	tok := grant.Token
	if tok == "" || len(tok) > AutoConfirmTokenMaxLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		// Printable ASCII excluding whitespace and control bytes.
		if tok[i] < 0x21 || tok[i] > 0x7e {
			return false
		}
	}
	if grant.ExpiresAt.IsZero() || !grant.ExpiresAt.After(now) {
		return false
	}
	if grant.ExpiresAt.Sub(now) > maxTTL {
		return false
	}
	return true
}

func cloneAutoConfirmPolicies(in []AutoConfirmPolicy) []AutoConfirmPolicy {
	if len(in) == 0 {
		return nil
	}
	out := make([]AutoConfirmPolicy, len(in))
	for i, p := range in {
		p.Actions = slices.Clone(p.Actions)
		p.Sources = slices.Clone(p.Sources)
		out[i] = p
	}
	return out
}

type parsedSemver struct {
	core [3]string
	pre  string
}

func parseAutoConfirmSemver(s string) (parsedSemver, error) {
	m := autoConfirmSemverPattern.FindStringSubmatch(s)
	if m == nil {
		return parsedSemver{}, fmt.Errorf("not MAJOR.MINOR.PATCH")
	}
	return parsedSemver{
		core: [3]string{m[1], m[2], m[3]},
		pre:  strings.TrimPrefix(m[4], "-"),
	}, nil
}

func (a parsedSemver) compare(b parsedSemver) int {
	for i := range a.core {
		if c := compareSemverNumeric(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	as, bs := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, bn := isDigits(as[i]), isDigits(bs[i])
		switch {
		case an && bn:
			if c := compareSemverNumeric(as[i], bs[i]); c != 0 {
				return c
			}
		case an:
			return -1
		case bn:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	if len(as) < len(bs) {
		return -1
	}
	if len(as) > len(bs) {
		return 1
	}
	return 0
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func compareSemverNumeric(a, b string) int {
	ai, _ := new(big.Int).SetString(a, 10)
	bi, _ := new(big.Int).SetString(b, 10)
	return ai.Cmp(bi)
}
