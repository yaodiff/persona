package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/netaddr"
)

// Doctor prints the hub's read-only self-check (`personad hub doctor`) in the terminal's language (i18n.Terminal,
// #5520). It inspects local state without creating configuration, renewing certificates or changing network rules.
func Doctor(ctx context.Context, out io.Writer) error {
	runDoctor(ctx, dataDir(), i18n.Terminal()).write(out)
	return ctx.Err()
}

// runDoctor runs every check on the hub whose data directory is dir and words the report in l: the terminal's for the
// command, the request's for the admin page (POST /api/v1/admin/doctor).
func runDoctor(ctx context.Context, dir string, l i18n.Lang) *doctorReport {
	d := &doctorReport{l: l}
	cfg := d.config(dir)
	var endpoints []Endpoint
	if cfg != nil {
		endpoints = d.running(ctx, cfg)
	}
	d.credentials(dir)
	addrs := d.ipv6()
	d.system(ctx)
	port := i18n.T(l, "hub-doctor.port_placeholder")
	if cfg != nil {
		if p, err := doctorPort(cfg.Public.TLSListen); err == nil {
			port = p
		}
	}
	d.next = append(d.next, i18n.T(l, "hub-doctor.next_firewall", i18n.Args{"port": port}))
	var probes []string
	for _, e := range endpoints {
		u, err := url.Parse(e.URL)
		if err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && e.Kind != "lan" {
			u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/health"
			u.RawPath, u.RawQuery, u.Fragment = "", "", ""
			probes = append(probes, u.String())
		}
	}
	if len(probes) == 0 {
		for _, a := range netaddr.Candidates(addrs) {
			probes = append(probes, "https://"+net.JoinHostPort(a.IP.String(), port)+"/api/v1/health")
		}
	}
	if len(probes) == 0 {
		probes = []string{"https://[" + i18n.T(l, "hub-doctor.ipv6_placeholder") + "]:" + port + "/api/v1/health"}
	}
	d.info(i18n.T(l, "hub-doctor.item_reach"), i18n.T(l, "hub-doctor.reach_unverified"))
	for _, probe := range probes {
		d.next = append(d.next, i18n.T(l, "hub-doctor.next_probe", i18n.Args{"url": probe}))
	}
	d.next = append(d.next, i18n.T(l, "hub-doctor.next_certificate"))
	return d
}

// doctorItem is one line of the report: a check that is fine (ok) or wants something done (problem), or what the
// check found out without judging it (info). Detail's later lines belong to it (addresses, devices, rules).
type doctorItem struct {
	Level  string `json:"level"` // ok | problem | info
	Item   string `json:"item"`
	Detail string `json:"detail"`
}

// doctorReport is a self-check's findings, worded in l.
type doctorReport struct {
	l     i18n.Lang
	items []doctorItem
	next  []string
	// problems are the "Problem" lines, as "item: action" (`personad hub setup` prints only these).
	problems []string
}

// write prints the report as `personad hub doctor` does: a title, a line per item (its later lines indented), then the
// next steps.
func (d *doctorReport) write(out io.Writer) {
	fmt.Fprintln(out, i18n.T(d.l, "hub-doctor.title"))
	for _, it := range d.items {
		first, rest, _ := strings.Cut(it.Detail, "\n")
		args := i18n.Args{"item": it.Item, "detail": first}
		switch {
		case it.Level == "ok":
			fmt.Fprintln(out, i18n.T(d.l, "hub-doctor.line_ok", args))
		case it.Level == "problem":
			fmt.Fprintln(out, i18n.T(d.l, "hub-doctor.line_problem", args))
		case it.Item != "":
			fmt.Fprintln(out, i18n.T(d.l, "hub-doctor.item_detail", args))
		default:
			fmt.Fprintln(out, first)
		}
		if rest != "" {
			for _, line := range strings.Split(rest, "\n") {
				fmt.Fprintln(out, "  "+line)
			}
		}
	}
	fmt.Fprintln(out, "\n"+i18n.T(d.l, "hub-doctor.next_title"))
	for _, step := range d.next {
		fmt.Fprintln(out, "- "+step)
	}
}

// itemsOrEmpty and nextOrEmpty are the report for JSON: empty lists, never null.
func (d *doctorReport) itemsOrEmpty() []doctorItem { return append([]doctorItem{}, d.items...) }
func (d *doctorReport) nextOrEmpty() []string      { return append([]string{}, d.next...) }

// ipv6 checks this machine's global IPv6 addresses (for reaching the hub from outside) and returns every address.
func (d *doctorReport) ipv6() []netaddr.Addr {
	item := i18n.T(d.l, "hub-doctor.item_ipv6")
	addrs, err := netaddr.List()
	if err != nil {
		d.problem(item, i18n.T(d.l, "hub-doctor.ipv6_unreadable"))
	} else {
		stable := 0
		for _, a := range addrs {
			if !a.IP.Is6() {
				continue
			}
			if netaddr.Stable(a) {
				stable++
				d.ok(item, i18n.T(d.l, "hub-doctor.ipv6_stable", i18n.Args{"ip": a.IP.String(), "interface": a.Interface}))
			} else {
				d.note(i18n.T(d.l, "hub-doctor.ipv6_excluded", i18n.Args{"ip": a.IP.String(), "interface": a.Interface,
					"reason": doctorAddressReason(d.l, a)}))
			}
		}
		if stable == 0 {
			d.problem(item, i18n.T(d.l, "hub-doctor.ipv6_none"))
		}
	}
	return addrs
}

// setupDoctor is the check at the end of `personad hub setup` (docs/assistant/onboarding.md 4.1), in l: only what is
// wrong, none of the excluded addresses or the routes and firewall, which are about reaching the hub from outside
// (outside: asked for with --outside, then those too).
func setupDoctor(ctx context.Context, dir string, outside bool, l i18n.Lang) []string {
	d := &doctorReport{l: l}
	if cfg := d.config(dir); cfg != nil {
		d.running(ctx, cfg)
	}
	if outside {
		d.ipv6()
		d.system(ctx)
	}
	return d.problems
}

func (d *doctorReport) ok(item, detail string) {
	d.items = append(d.items, doctorItem{Level: "ok", Item: item, Detail: detail})
}

func (d *doctorReport) problem(item, action string) {
	d.items = append(d.items, doctorItem{Level: "problem", Item: item, Detail: action})
	d.next = append(d.next, action)
	d.problems = append(d.problems, i18n.T(d.l, "hub-doctor.item_detail", i18n.Args{"item": item, "detail": action}))
}

// info is what a check found out without judging it, such as a check it skipped.
func (d *doctorReport) info(item, detail string) {
	d.items = append(d.items, doctorItem{Level: "info", Item: item, Detail: detail})
}

// note adds a line to the item before it (an address it left out, a device's push path, a firewall rule).
func (d *doctorReport) note(line string) {
	if len(d.items) == 0 {
		d.info("", line)
		return
	}
	last := &d.items[len(d.items)-1]
	last.Detail += "\n" + line
}

func (d *doctorReport) config(dir string) *Config {
	l := d.l
	item := i18n.T(l, "hub-doctor.item_config")
	b, err := os.ReadFile(filepath.Join(dir, "hub.json"))
	if err != nil {
		d.problem(item, i18n.T(l, "hub-doctor.config_unreadable"))
		return nil
	}
	// Match the runtime's omitted-listen default, without generating a token or host name.
	cfg := &Config{Listen: ":18660", dir: dir}
	if json.Unmarshal(b, cfg) != nil {
		d.problem(item, i18n.T(l, "hub-doctor.config_invalid"))
		return nil
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	listen := i18n.T(l, "hub-doctor.item_listen")
	if _, err := doctorPort(cfg.Listen); err != nil {
		d.problem(listen, i18n.T(l, "hub-doctor.listen_invalid"))
	} else {
		value := cfg.Listen
		if _, set := raw["listen"]; !set {
			value = i18n.T(l, "hub-doctor.listen_default", i18n.Args{"listen": cfg.Listen})
		}
		d.ok(listen, i18n.T(l, "hub-doctor.listen_ok", i18n.Args{"listen": value}))
	}
	if _, err := doctorPort(cfg.Public.TLSListen); err != nil {
		d.problem("public.tls_listen", i18n.T(l, "hub-doctor.tls_listen_invalid"))
	} else {
		d.ok("public.tls_listen", i18n.T(l, "hub-doctor.tls_listen_ok", i18n.Args{"listen": cfg.Public.TLSListen}))
	}
	if cfg.Security.AllowPublicPlaintext {
		d.problem("security.allow_public_plaintext", i18n.T(l, "hub-doctor.public_plaintext_on"))
	} else {
		d.ok("security.allow_public_plaintext", i18n.T(l, "hub-doctor.public_plaintext_off"))
	}
	switch {
	case cfg.Security.PlaintextLoopbackOnly && cfg.Public.TLSListen == "":
		d.problem("security.plaintext_loopback_only", i18n.T(l, "hub-doctor.loopback_only_without_tls"))
	case cfg.Security.PlaintextLoopbackOnly:
		d.ok("security.plaintext_loopback_only", i18n.T(l, "hub-doctor.loopback_only_on"))
	default:
		d.ok("security.plaintext_loopback_only", i18n.T(l, "hub-doctor.loopback_only_off"))
	}
	if pp := cfg.Security.ProxyProtocol; pp != nil {
		switch p, err := pp.policy(); {
		case err != nil:
			d.problem("security.proxy_protocol", i18n.T(l, "hub-doctor.proxy_protocol_invalid", i18n.Args{"error": err.Error()}))
		case p.tls && !p.http && cfg.Public.TLSListen == "":
			d.problem("security.proxy_protocol", i18n.T(l, "hub-doctor.proxy_protocol_idle"))
		default:
			d.ok("security.proxy_protocol", i18n.T(l, "hub-doctor.proxy_protocol_ok",
				i18n.Args{"sources": strings.Join(pp.From, i18n.T(l, "hub-doctor.list_separator"))}))
		}
	}
	certFile, keyFile := cfg.Public.CertFile, cfg.Public.KeyFile
	if certFile == "" && keyFile == "" {
		certFile, keyFile = filepath.Join(dir, "tls", "hub.crt"), filepath.Join(dir, "tls", "hub.key")
	}
	certItem := i18n.T(l, "hub-doctor.item_tls_certificate")
	cert, fp, err := loadTLSFiles(certFile, keyFile)
	if err != nil {
		d.problem(certItem, i18n.T(l, "hub-doctor.certificate_unreadable"))
	} else {
		args := i18n.Args{"pin": fp, "expires": cert.Leaf.NotAfter.UTC().Format(time.RFC3339)}
		now := time.Now()
		if now.Before(cert.Leaf.NotBefore) || !now.Before(cert.Leaf.NotAfter) {
			d.problem(certItem, i18n.T(l, "hub-doctor.certificate_expired", args))
		} else if !cert.Leaf.NotAfter.After(now.Add(30 * 24 * time.Hour)) {
			d.problem(certItem, i18n.T(l, "hub-doctor.certificate_expiring", args))
		} else {
			d.ok(certItem, i18n.T(l, "hub-doctor.certificate_ok", args))
		}
	}
	return cfg
}

// doctorCredentialFiles are credentials below the top level, in a folder the path guard and the agent sandbox close
// whole: the Postgres superuser password the container entrypoint writes (docs/operations/docker.md). Everything else
// this check classifies is directly in the data directory.
var doctorCredentialFiles = []string{filepath.Join("docker", "db-password")}

// credentials checks that the data directory's credential files are readable by their owner only (0600) and the
// folders closed whole are not open to others (0700). It reports only: doctor never changes a file; the load path
// tightens hub.json itself (readConfigFile), and every credential Persona writes is made 0600 as it is written, so a
// finding means a file was copied in or chmoded by hand. POSIX permission bits do not exist on Windows, where the
// check is skipped.
func (d *doctorReport) credentials(dir string) {
	l := d.l
	item := i18n.T(l, "hub-doctor.item_credentials")
	if runtime.GOOS == "windows" {
		d.info(item, i18n.T(l, "hub-doctor.credentials_windows"))
		return
	}
	bad := 0
	check := func(rel string, want os.FileMode) {
		path := filepath.Join(dir, rel)
		st, err := os.Stat(path) // follow a link: the target's mode is what keeps the secret
		if os.IsNotExist(err) {
			return // a deployment without this credential has nothing to check
		}
		if err != nil {
			bad++
			d.problem(item, i18n.T(l, "hub-doctor.credentials_stat", i18n.Args{"path": filepath.ToSlash(rel), "error": err.Error()}))
			return
		}
		if st.Mode().Perm() != want {
			bad++
			d.problem(item, i18n.T(l, "hub-doctor.credentials_mode", i18n.Args{"path": filepath.ToSlash(rel),
				"mode": fmt.Sprintf("%04o", st.Mode().Perm()), "want": fmt.Sprintf("%04o", want),
				"command": fmt.Sprintf("chmod %o %s", want, path)}))
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		d.problem(item, i18n.T(l, "hub-doctor.credentials_dir", i18n.Args{"dir": dir, "error": err.Error()}))
		return
	}
	for _, e := range entries {
		switch {
		case e.IsDir():
			if slices.Contains(identity.DataSecretDirs, e.Name()) {
				check(e.Name(), 0o700)
			}
		case identity.DataSecretName(e.Name()):
			check(e.Name(), 0o600)
		}
	}
	for _, rel := range doctorCredentialFiles {
		check(rel, 0o600)
	}
	if bad == 0 {
		d.ok(item, i18n.T(l, "hub-doctor.credentials_ok"))
	}
}

func doctorPort(listen string) (string, error) {
	_, port, err := net.SplitHostPort(listen)
	n, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid listen port %q", listen)
	}
	return port, nil
}

func (d *doctorReport) running(ctx context.Context, cfg *Config) []Endpoint {
	l := d.l
	port, err := doctorPort(cfg.Listen)
	if err != nil {
		return nil
	}
	item := i18n.T(l, "hub-doctor.item_running")
	if strings.TrimSpace(cfg.Token) == "" {
		d.problem(item, i18n.T(l, "hub-doctor.running_no_token"))
		return nil
	}
	get := doctorLoopback(cfg, port)
	for _, path := range []string{"/api/v1/health", "/api/v1/endpoints"} {
		resp, err := get(ctx, path)
		if err != nil {
			d.problem(item, i18n.T(l, "hub-doctor.running_unreachable"))
			return nil
		}
		var body struct {
			OK        bool       `json:"ok"`
			Endpoints []Endpoint `json:"endpoints"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			d.problem(path, i18n.T(l, "hub-doctor.running_http", i18n.Args{"status": resp.StatusCode}))
			continue
		}
		if decodeErr != nil || (path == "/api/v1/health" && !body.OK) || (path == "/api/v1/endpoints" && body.Endpoints == nil) {
			d.problem(path, i18n.T(l, "hub-doctor.running_invalid"))
			continue
		}
		if path == "/api/v1/health" {
			d.ok(item, i18n.T(l, "hub-doctor.running_ok"))
			continue
		}
		endpointsItem := i18n.T(l, "hub-doctor.item_endpoints")
		if len(body.Endpoints) == 0 {
			d.problem(endpointsItem, i18n.T(l, "hub-doctor.endpoints_none"))
		} else {
			d.ok(endpointsItem, i18n.T(l, "hub-doctor.endpoints_ok"))
			for _, e := range body.Endpoints {
				d.note(i18n.T(l, "hub-doctor.endpoint", i18n.Args{"url": e.URL, "kind": e.Kind, "port": e.Port,
					"seen": e.SeenAt.Format(time.RFC3339)}))
			}
		}
		d.pushRoutes(ctx, get)
		return body.Endpoints
	}
	return nil
}

// doctorLoopback GETs a path from the running hub with the master token. Never resolves a configured host or sends
// the token outside loopback.
func doctorLoopback(cfg *Config, port string) func(ctx context.Context, path string) (*http.Response, error) {
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	host, _, _ := net.SplitHostPort(cfg.Listen)
	loopbacks := []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.IPv6Loopback()}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
		loopbacks[0], loopbacks[1] = loopbacks[1], loopbacks[0]
	}
	return func(ctx context.Context, path string) (resp *http.Response, err error) {
		for _, ip := range loopbacks {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(ip.String(), port)+path, nil)
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
			if resp, err = client.Do(req); err == nil {
				return resp, nil
			}
		}
		return nil, err
	}
}

// pushRoutes reports which way the running hub pushes to each device (#5998): straight to APNs, through the relay the
// phone registered with, or not at all and why.
func (d *doctorReport) pushRoutes(ctx context.Context, get func(context.Context, string) (*http.Response, error)) {
	l := d.l
	resp, err := get(ctx, "/api/v1/devices/push")
	if err != nil {
		return // the checks before said whether the hub answers at all
	}
	var body struct {
		Direct  bool            `json:"direct"`
		Devices []pushDevice    `json:"devices"`
		Web     []webPushListed `json:"web"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	resp.Body.Close()
	item := i18n.T(l, "hub-doctor.item_push")
	switch {
	case resp.StatusCode == http.StatusNotFound:
		d.problem(item, i18n.T(l, "hub-doctor.push_old_hub"))
		return
	case resp.StatusCode != http.StatusOK || decodeErr != nil:
		d.problem(item, i18n.T(l, "hub-doctor.push_http", i18n.Args{"status": resp.StatusCode}))
		return
	}
	way := i18n.T(l, "hub-doctor.push_way_relay")
	if body.Direct {
		way = i18n.T(l, "hub-doctor.push_way_direct")
	}
	defer d.webPushRoutes(body.Web)
	if len(body.Devices) == 0 {
		d.ok(item, i18n.T(l, "hub-doctor.push_no_devices", i18n.Args{"way": way}))
		return
	}
	bad := 0
	for _, dev := range body.Devices {
		if dev.Push.Route == pushNone || dev.Push.Reason == pushFailed {
			bad++
		}
	}
	if bad == 0 {
		d.ok(item, way)
	} else {
		d.problem(item, i18n.T(l, "hub-doctor.push_bad", i18n.Args{"way": way, "count": bad}))
	}
	for _, dev := range body.Devices {
		name := dev.Name
		if name == "" {
			name = dev.DeviceID
		}
		if dev.Kind != "" {
			name = i18n.T(l, "hub-doctor.device_kind", i18n.Args{"name": name, "kind": dev.Kind})
		}
		d.note(i18n.T(l, "hub-doctor.item_detail", i18n.Args{"item": name, "detail": pushRouteLine(l, dev.Push)}))
	}
}

// webPushRoutes reports the browsers with Web Push (#7459), when there are any: through which push service, and
// whether the last push went out or the subscription was made with an earlier key.
func (d *doctorReport) webPushRoutes(web []webPushListed) {
	if len(web) == 0 {
		return
	}
	l := d.l
	item := i18n.T(l, "hub-doctor.item_web_push")
	bad := 0
	for _, b := range web {
		if !b.Current || b.FailedAt != nil {
			bad++
		}
	}
	if bad == 0 {
		d.ok(item, i18n.T(l, "hub-doctor.web_push_ok", i18n.Args{"count": len(web)}))
	} else {
		d.problem(item, i18n.T(l, "hub-doctor.web_push_bad", i18n.Args{"count": bad}))
	}
	for _, b := range web {
		name := cmp.Or(b.Browser, b.Device)
		route := i18n.T(l, "hub-doctor.web_push_route", i18n.Args{"service": cmp.Or(b.Service, "?")})
		line := route
		switch {
		case !b.Current:
			line = i18n.T(l, "hub-doctor.web_push_stale", i18n.Args{"route": route})
		case b.FailedAt != nil:
			line = i18n.T(l, "hub-doctor.push_reason_failed_at", i18n.Args{"route": route, "detail": b.Error,
				"at": b.FailedAt.UTC().Format("01-02 15:04 UTC")})
		}
		d.note(i18n.T(l, "hub-doctor.item_detail", i18n.Args{"item": name, "detail": line}))
	}
}

// doctorAddressReasons are the address flags that keep an address out, with their words.
var doctorAddressReasons = []struct {
	bit netaddr.Flags
	key string
}{
	{netaddr.FlagTemporary, "hub-doctor.reason_temporary"},
	{netaddr.FlagDeprecated, "hub-doctor.reason_deprecated"},
	{netaddr.FlagTentative, "hub-doctor.reason_tentative"},
	{netaddr.FlagDADFailed, "hub-doctor.reason_dad_failed"},
}

func doctorAddressReason(l i18n.Lang, a netaddr.Addr) string {
	var reasons []string
	if class := netaddr.Classify(a.IP); class != netaddr.GlobalIPv6 {
		reasons = append(reasons, i18n.T(l, "hub-doctor.reason_not_global", i18n.Args{"class": string(class)}))
	}
	if !a.FlagsKnown || !a.ScopeKnown {
		reasons = append(reasons, i18n.T(l, "hub-doctor.reason_flags_unknown"))
	}
	if a.ScopeKnown && a.Scope != 0 {
		reasons = append(reasons, i18n.T(l, "hub-doctor.reason_scope"))
	}
	if a.Virtual || a.InterfaceFlags&net.FlagLoopback != 0 {
		reasons = append(reasons, i18n.T(l, "hub-doctor.reason_virtual"))
	}
	if a.InterfaceFlags&net.FlagUp == 0 {
		reasons = append(reasons, i18n.T(l, "hub-doctor.reason_down"))
	}
	for _, flag := range doctorAddressReasons {
		if a.Flags&flag.bit != 0 {
			reasons = append(reasons, i18n.T(l, flag.key))
		}
	}
	if a.LifetimesKnown && (a.PreferredLifetime == 0 || a.ValidLifetime == 0) {
		reasons = append(reasons, i18n.T(l, "hub-doctor.reason_expired"))
	}
	return strings.Join(reasons, i18n.T(l, "hub-doctor.list_separator"))
}

func doctorCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

func (d *doctorReport) system(ctx context.Context) {
	l := d.l
	if runtime.GOOS != "linux" {
		d.info(i18n.T(l, "hub-doctor.item_routes_firewall"), i18n.T(l, "hub-doctor.routes_not_linux"))
		return
	}
	routeItem := i18n.T(l, "hub-doctor.item_route")
	for _, item := range []struct {
		path string
		ipv6 bool
	}{{"/proc/net/route", false}, {"/proc/net/ipv6_route", true}} {
		b, err := os.ReadFile(item.path)
		if err != nil {
			d.info(routeItem, i18n.T(l, "hub-doctor.route_unreadable", i18n.Args{"path": item.path}))
			continue
		}
		ifaces := doctorRouteInterfaces(string(b), item.ipv6)
		if len(ifaces) == 0 {
			d.info(routeItem, i18n.T(l, "hub-doctor.route_empty", i18n.Args{"path": item.path}))
		}
		for _, iface := range ifaces {
			d.route(iface, item.path)
		}
	}
	query := i18n.T(l, "hub-doctor.item_route_query")
	if b, err := doctorCommand(ctx, "ip", "-6", "route", "get", "2001:db8::1"); err == nil {
		fields := strings.Fields(string(b))
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				d.route(fields[i+1], query)
				break
			}
		}
	} else {
		d.info(query, i18n.T(l, "hub-doctor.route_query_skipped"))
	}
	firewall := i18n.T(l, "hub-doctor.item_firewall")
	for _, command := range [][]string{{"ufw", "status"}, {"nft", "list", "ruleset"}} {
		b, err := doctorCommand(ctx, command[0], command[1:]...)
		if err != nil {
			d.info(firewall, i18n.T(l, "hub-doctor.firewall_skipped", i18n.Args{"command": command[0]}))
			continue
		}
		if strings.Contains(string(b), "Status: inactive") || strings.TrimSpace(string(b)) == "" {
			d.problem(firewall, i18n.T(l, "hub-doctor.firewall_off", i18n.Args{"command": command[0]}))
		} else {
			d.ok(i18n.T(l, "hub-doctor.item_firewall_read"), i18n.T(l, "hub-doctor.firewall_read", i18n.Args{"command": strings.Join(command, " ")}))
			for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
				d.note(strings.TrimRight(line, " \t\r"))
			}
		}
	}
}

func (d *doctorReport) route(iface, source string) {
	name := strings.ToLower(iface)
	suspect := false
	for _, prefix := range []string{"tun", "utun", "tap", "wg", "tailscale", "vpn", "ipsec"} {
		suspect = suspect || strings.HasPrefix(name, prefix)
	}
	if target, err := filepath.EvalSymlinks(filepath.Join("/sys/class/net", iface)); err == nil {
		suspect = suspect || strings.HasPrefix(target, "/sys/devices/virtual/") && iface != "lo"
	}
	item := i18n.T(d.l, "hub-doctor.item_route")
	args := i18n.Args{"source": source, "interface": iface}
	if suspect {
		d.problem(item, i18n.T(d.l, "hub-doctor.route_suspect", args))
	} else {
		d.ok(item, i18n.T(d.l, "hub-doctor.route_ok", args))
	}
}

func doctorRouteInterfaces(raw string, ipv6 bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		var iface, flags string
		if ipv6 {
			if len(f) < 10 {
				continue
			}
			iface, flags = f[9], f[8]
		} else {
			if len(f) < 11 || f[1] != "00000000" || f[7] != "00000000" {
				continue
			}
			iface, flags = f[0], f[3]
		}
		bits, err := strconv.ParseUint(flags, 16, 32)
		if err != nil || bits&1 == 0 || bits&0x200 != 0 || iface == "lo" || seen[iface] {
			continue
		}
		seen[iface] = true
		out = append(out, iface)
	}
	return out
}

// pushRouteLine is a device's way in `hub doctor`'s words, in l.
func pushRouteLine(l i18n.Lang, r pushRoute) string {
	var route string
	switch r.Route {
	case pushDirect:
		route = i18n.T(l, "hub-doctor.push_route_direct")
	case pushRelay:
		route = i18n.T(l, "hub-doctor.push_route_relay")
	default:
		route = i18n.T(l, "hub-doctor.push_route_none")
	}
	switch r.Reason {
	case pushNoToken:
		return i18n.T(l, "hub-doctor.push_reason_no_token", i18n.Args{"route": route})
	case pushRelayFailed:
		return i18n.T(l, "hub-doctor.push_reason_relay_failed", i18n.Args{"route": route, "detail": r.Detail})
	case pushNoRelay:
		return i18n.T(l, "hub-doctor.push_reason_no_relay", i18n.Args{"route": route})
	case pushFailed:
		if r.FailedAt != nil {
			return i18n.T(l, "hub-doctor.push_reason_failed_at", i18n.Args{"route": route, "detail": r.Detail,
				"at": r.FailedAt.UTC().Format("01-02 15:04 UTC")})
		}
		return i18n.T(l, "hub-doctor.push_reason_failed", i18n.Args{"route": route, "detail": r.Detail})
	}
	return route
}
