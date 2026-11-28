package extension

// Settings the user fills in for an extension that reach outside it (#5906, docs/extensions/extensions.md "Settings the user
// fills in"): an address in the settings the egress proxy lets it reach (permissions.network "config:<field>"), a
// secret shown by its last four characters once set (secrets[].hint "last4"), and a check by the extension of what is
// about to be saved (settings_check).

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extension/egress"
)

// ConfigNetworkPrefix starts a permissions.network entry that names a configuration field instead of a host:
// "config:<field>" lets the extension reach the https address the user wrote there, "config:<field>:http" an http one
// too.
const ConfigNetworkPrefix = "config:"

// HintLast4 is secrets[].hint for a secret shown by its last four characters once set.
const HintLast4 = "last4"

// hintMinLen: a shorter secret is not shown by its end (four characters of a short one say too much of it).
const hintMinLen = 12

// ConfiguredNetwork is one "config:<field>" or "config:<list>[].<field>" entry of permissions.network.
type ConfiguredNetwork struct {
	Field string // the fixed field, or the entry field when List is set
	List  string // the entry-list field; empty for a fixed field
	HTTP  bool   // http allowed too, as the manifest says (":http")
	// Malformed remembers a config: path that looks like an entry path but is not exactly list[].field.
	Malformed bool
}

// ParseConfigNetwork reads a permissions.network entry; ok false for a host name.
func ParseConfigNetwork(entry string) (ConfiguredNetwork, bool) {
	rest, ok := strings.CutPrefix(entry, ConfigNetworkPrefix)
	if !ok {
		return ConfiguredNetwork{}, false
	}
	field, http := strings.CutSuffix(rest, ":http")
	c := ConfiguredNetwork{Field: field, HTTP: http}
	if strings.ContainsAny(field, "[]") {
		list, entry, found := strings.Cut(field, "[].")
		c.List, c.Field = list, entry
		c.Malformed = !found || list == "" || entry == "" || strings.ContainsAny(list, "[].:") || strings.ContainsAny(entry, "[].:")
		if c.Malformed {
			c.List = ""
			c.Field = field
		}
	}
	return c, true
}

// ConfiguredNetworks are permissions.network's "config:" entries.
func (p Permissions) ConfiguredNetworks() []ConfiguredNetwork {
	var out []ConfiguredNetwork
	for _, h := range p.Network {
		if c, ok := ParseConfigNetwork(h); ok {
			out = append(out, c)
		}
	}
	return out
}

// hostNames are permissions.network's host names, without the "config:" entries.
func (p Permissions) hostNames() []string {
	var out []string
	for _, h := range p.normalized().Network {
		if _, ok := ParseConfigNetwork(h); !ok {
			out = append(out, h)
		}
	}
	return out
}

// checkConfigured holds m's settings-filled parts to the schema: a "config:" entry names a string field of format uri
// with no default (a default would let it reach an address the user never wrote), a hint is one this hub knows.
func (m *Manifest) checkConfigured() []error {
	var errs []error
	cs := m.Permissions.ConfiguredNetworks()
	if len(cs) > 0 {
		sc, err := ParseSchema(m.Configuration)
		for _, c := range cs {
			var f *Field
			var list *Field
			if err == nil && sc != nil {
				name := c.Field
				if c.List != "" {
					name = c.List
				}
				for i := range sc.Fields {
					if sc.Fields[i].Name == name {
						f = &sc.Fields[i]
					}
				}
			}
			switch {
			case c.Malformed:
				errs = append(errs, fmt.Errorf("permissions.network: %q: config:<list>[].<field> or config:<list>[].<field>:http", ConfigNetworkPrefix+c.Field))
			case c.Field == "" || strings.Contains(c.Field, ":"):
				errs = append(errs, fmt.Errorf("permissions.network: %q: config:<field> or config:<field>:http", ConfigNetworkPrefix+c.Field))
			case f == nil:
				name := c.Field
				if c.List != "" {
					name = c.List
				}
				errs = append(errs, fmt.Errorf("permissions.network: configuration has no field %q", name))
			case c.List != "" && !f.EntryList():
				errs = append(errs, fmt.Errorf("permissions.network: configuration.%s: not an entry list", c.List))
			case c.List != "":
				list = f
				f = nil
				for i := range list.Items.Fields {
					if list.Items.Fields[i].Name == c.Field {
						f = &list.Items.Fields[i]
					}
				}
				if f == nil {
					errs = append(errs, fmt.Errorf("permissions.network: configuration.%s has no entry field %q", c.List, c.Field))
				} else if f.Type != "string" || f.Format != "uri" {
					errs = append(errs, fmt.Errorf("permissions.network: configuration.%s[].%s: the address it reaches is a string of format uri", c.List, c.Field))
				} else if f.Default != nil {
					errs = append(errs, fmt.Errorf("permissions.network: configuration.%s[].%s: the address it reaches has a default, but only the user writes it", c.List, c.Field))
				}
			case f.Type != "string" || f.Format != "uri":
				errs = append(errs, fmt.Errorf("permissions.network: configuration.%s: the address it reaches is a string of format uri", c.Field))
			case f.Default != nil:
				errs = append(errs, fmt.Errorf("permissions.network: configuration.%s: the address it reaches has a default, but only the user writes it", c.Field))
			}
		}
	}
	for _, s := range m.Secrets {
		if s.Hint != "" && s.Hint != HintLast4 {
			errs = append(errs, fmt.Errorf("secrets.%s.hint: %q (only %q)", s.Name, s.Hint, HintLast4))
		}
	}
	return errs
}

// isLoopbackHost reports whether h is a loopback host on this machine (127.0.0.1, localhost or ::1).
func isLoopbackHost(h string) bool {
	switch strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(h), "[]"), ".")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// ConfiguredTarget is the host:port a "config:" entry lets through, from the field's value now ("" when it has none
// that may be reached: unset, not a URL, http or ws where only https is allowed, user info in it). A ws:// or wss://
// value counts as its http:// or https:// one. The value is the user's:
// only the settings pages and the hub write an extension's configuration, never the extension itself.
func ConfiguredTarget(c ConfiguredNetwork, config json.RawMessage) (egress.Target, string) {
	var values map[string]any
	_ = json.Unmarshal(config, &values)
	raw, _ := values[c.Field].(string)
	return configuredTargetValue(c, raw)
}

func configuredTargetValue(c ConfiguredNetwork, raw string) (egress.Target, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return egress.Target{}, ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return egress.Target{}, "not a URL"
	}
	host := strings.ToLower(strings.TrimSuffix(strings.Trim(u.Hostname(), "[]"), "."))
	if host == "" || net.ParseIP(host) == nil && !hostPattern.MatchString(host) {
		return egress.Target{}, "not a host"
	}
	port := u.Port()
	// A WebSocket address is its HTTP one's (#16264): wss as https, ws as plain http, which needs :http or loopback.
	plain := false
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		if port == "" {
			port = "443"
		}
	case "http", "ws":
		if !c.HTTP && !isLoopbackHost(host) {
			return egress.Target{}, "http (only https)"
		}
		if port == "" {
			port = "80"
		}
		plain = true
	default:
		return egress.Target{}, "not http(s) or ws(s)"
	}
	return egress.Target{Host: host, Port: port, HTTP: plain}, ""
}

// ConfiguredTargets are the host:ports p's "config:" entries let through with configuration config now.
func ConfiguredTargets(m *Manifest, config json.RawMessage) []egress.Target {
	var out []egress.Target
	for _, c := range m.Permissions.ConfiguredNetworks() {
		out = append(out, configuredTargets(c, config)...)
	}
	return out
}

func configuredTargets(c ConfiguredNetwork, config json.RawMessage) []egress.Target {
	var out []egress.Target
	var values map[string]json.RawMessage
	_ = json.Unmarshal(config, &values)
	if c.List == "" {
		if t, _ := ConfiguredTarget(c, config); t.Host != "" {
			out = append(out, t)
		}
		return out
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(values[c.List], &entries) != nil {
		return nil
	}
	for _, entry := range entries {
		var raw string
		_ = json.Unmarshal(entry[c.Field], &raw)
		if t, _ := configuredTargetValue(c, raw); t.Host != "" {
			out = append(out, t)
		}
	}
	return out
}

// SecretHint is what a set secret shows instead of 已设置 alone: its last four characters, when its spec asks and it
// is long enough ("" else).
func SecretHint(s SecretSpec, value string) string {
	if s.Hint != HintLast4 || len([]rune(value)) < hintMinLen {
		return ""
	}
	r := []rune(value)
	return string(r[len(r)-4:])
}

// SandboxConfig is config as a sandboxed program gets it (#13540): every permitted loopback address in a format uri
// setting is written as egress.LocalHost, the name the hub machine's loopback has inside a sandbox (`127.0.0.1` there is
// the sandbox itself). Its permissions decide whether an address is rewritten: a loopback address in
// permissions.network, a loopback "config:" target on that port (http only when the manifest allows it) or a loopback
// host the owner allowed. A schema default that is absent from config is filled in and rewritten too, so the program
// gets the rewritten default. config comes back unchanged when nothing needs rewriting.
func SandboxConfig(m *Manifest, config json.RawMessage, rule egress.Rule) json.RawMessage {
	if m == nil || len(config) == 0 {
		return config
	}
	sc, err := ParseSchema(m.Configuration)
	if err != nil || sc == nil {
		return config
	}
	var values map[string]any
	if json.Unmarshal(config, &values) != nil || values == nil {
		return config
	}
	changed := false
	for _, f := range sc.Fields {
		if f.Type == "string" && f.Format == "uri" {
			v, ok := values[f.Name].(string)
			if !ok {
				if f.Default == nil {
					continue
				}
				var d string
				if json.Unmarshal(f.Default, &d) != nil {
					continue
				}
				if r, rewritten := sandboxURL(d, rule); rewritten {
					values[f.Name], changed = r, true
				}
				continue
			}
			if r, rewritten := sandboxURL(v, rule); rewritten {
				values[f.Name], changed = r, true
			}
			continue
		}
		if !f.EntryList() || f.Items == nil {
			continue
		}
		entries, ok := values[f.Name].([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			row, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			for _, item := range f.Items.Fields {
				if item.Type != "string" || item.Format != "uri" {
					continue
				}
				v, ok := row[item.Name].(string)
				if !ok {
					continue
				}
				if r, rewritten := sandboxURL(v, rule); rewritten {
					row[item.Name], changed = r, true
				}
			}
		}
	}
	if !changed {
		return config
	}
	out, err := json.Marshal(values)
	if err != nil {
		return config
	}
	return out
}

// sandboxURL is raw rewritten to egress.LocalHost when it is a permitted loopback http(s) / ws(s) URL, and whether it
// was. User info stays in front of the new host (net/url keeps it apart from Host).
func sandboxURL(raw string, rule egress.Rule) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" || u.Host == "" {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss":
	default:
		return "", false
	}
	if !isLoopbackHost(u.Hostname()) || !loopbackPermitted(u, rule) {
		return "", false
	}
	if p := u.Port(); p != "" {
		u.Host = net.JoinHostPort(egress.LocalHost, p)
	} else {
		u.Host = egress.LocalHost
	}
	return u.String(), true
}

// loopbackPermitted says whether rule lets u reach this machine's loopback: permissions.network lists a loopback
// address, one of its Targets is that loopback port (plain http only when the target allows it), or the owner allowed a
// loopback host (on that port, or on any when it names none).
func loopbackPermitted(u *url.URL, rule egress.Rule) bool {
	if rule.Loopback() {
		return true
	}
	plain := strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "ws")
	port := u.Port()
	if port == "" {
		if plain {
			port = "80"
		} else {
			port = "443"
		}
	}
	if slices.ContainsFunc(rule.Targets, func(t egress.Target) bool {
		return isLoopbackHost(t.Host) && t.Port == port && (!plain || t.HTTP)
	}) {
		return true
	}
	return slices.ContainsFunc(rule.Owner, func(t egress.Target) bool {
		return isLoopbackHost(t.Host) && (t.Port == "" || t.Port == port)
	})
}

// HostLoopbackURL is raw rewritten from egress.LocalHost back to 127.0.0.1 on the hub machine itself, for a program
// outside any sandbox (a gate's credentials assistantCredentials hands over); raw comes back unchanged when it is not
// such a URL.
func HostLoopbackURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return raw
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss":
	default:
		return raw
	}
	if !strings.EqualFold(u.Hostname(), egress.LocalHost) {
		return raw
	}
	if p := u.Port(); p != "" {
		u.Host = net.JoinHostPort("127.0.0.1", p)
	} else {
		u.Host = "127.0.0.1"
	}
	return u.String()
}
