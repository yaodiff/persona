package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/netaddr"
)

// reportText is the report as `personad hub doctor` prints it.
func reportText(d *doctorReport) string {
	var b bytes.Buffer
	d.write(&b)
	return b.String()
}

func TestDoctorReadOnly(t *testing.T) {
	dir := t.TempDir()
	d := &doctorReport{l: i18n.En}
	missing := filepath.Join(dir, "missing")
	if d.config(missing) != nil {
		t.Fatal("missing configuration was accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("doctor created a data directory")
	}
	cert, pin, err := loadOrCreateTLS(filepath.Join(dir, "tls"), []string{"example.com"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token := randomHex(24)
	var paths []string
	redirect := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("doctor request must be authenticated and read-only")
		}
		if redirect {
			http.Redirect(w, r, "/must-not-follow", http.StatusFound)
			return
		}
		if r.URL.Path == "/api/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"endpoints": []Endpoint{{URL: "https://[2001:db8::2]:19443", Kind: "ipv6", Port: 19443, Scheme: "https"}}})
		}
	}))
	defer srv.Close()
	cfg := Config{Listen: srv.Listener.Addr().String(), Token: token, Public: PublicConfig{TLSListen: ":19443"}}
	cfg.Security.AllowPublicPlaintext = true
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	files := []string{"hub.json", "tls/hub.crt", "tls/hub.key"}
	before := map[string][]byte{}
	for _, file := range files {
		before[file], err = os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded := d.config(dir)
	if loaded == nil {
		t.Fatal(reportText(d))
	}
	if endpoints := d.running(context.Background(), loaded); len(endpoints) != 1 {
		t.Fatalf("missing published endpoints: %s", reportText(d))
	}
	if !reflect.DeepEqual(paths, []string{"/api/v1/health", "/api/v1/endpoints", "/api/v1/devices/push"}) {
		t.Fatalf("unexpected requests: %v", paths)
	}
	out := reportText(d)
	for _, want := range []string{pin, cert.Leaf.NotAfter.UTC().Format(time.RFC3339), "Problem | security.allow_public_plaintext: Plain HTTP from the internet is on",
		"2001:db8::2", "OK | Running hub: The loopback health check passed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing output %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, token) {
		t.Fatal("doctor disclosed the master token")
	}
	for _, file := range files {
		after, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || !bytes.Equal(before[file], after) {
			t.Fatalf("doctor changed %s", file)
		}
	}
	redirect, paths = true, nil
	d.running(context.Background(), loaded)
	if !reflect.DeepEqual(paths, []string{"/api/v1/health", "/api/v1/endpoints"}) || !strings.Contains(reportText(d), "HTTP 302") {
		t.Fatal("doctor followed a redirect or failed to report it")
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded = d.config(dir)
	d.running(context.Background(), loaded)
	b, _ = os.ReadFile(filepath.Join(dir, "hub.json"))
	if string(b) != `{}` || loaded.Token != "" || !strings.Contains(reportText(d), "The configuration has no master token") {
		t.Fatal("doctor must not generate or persist missing credentials")
	}
}

// The credential check reports credential files and whole-secret folders that are readable by others, leaves every
// mode untouched (doctor is read-only), and stays quiet about ordinary files and credentials that are not there.
func TestDoctorCredentialPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	dir := t.TempDir()
	write := func(rel string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("credential-or-not"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil { // past the umask
			t.Fatal(err)
		}
	}
	chmod := func(rel string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(filepath.Join(dir, rel), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("hub.json", 0o600)
	write("secret.key", 0o600)
	write("relay-tunnel.token", 0o644)
	write("tls/hub.key", 0o600)
	chmod("tls", 0o700)
	write("docker/db-password", 0o644)
	chmod("docker", 0o755)
	write("mcp.json", 0o644) // an ordinary file: its mode is none of the check's business

	d := &doctorReport{l: i18n.En}
	d.credentials(dir)
	out := reportText(d)
	for _, want := range []string{"relay-tunnel.token", "docker", "db-password", "chmod 600", "chmod 700"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing finding %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "mcp.json") || len(d.problems) != 3 {
		t.Fatalf("want exactly the three open credentials, got %v", d.problems)
	}
	// Read-only: the modes the run found are still the ones it must report.
	if st, _ := os.Stat(filepath.Join(dir, "relay-tunnel.token")); st.Mode().Perm() != 0o644 {
		t.Fatalf("doctor changed a mode: %v", st.Mode())
	}

	chmod("relay-tunnel.token", 0o600)
	chmod("docker/db-password", 0o600)
	chmod("docker", 0o700)
	d = &doctorReport{l: i18n.En}
	d.credentials(dir)
	if out := reportText(d); len(d.problems) != 0 || !strings.Contains(out, "OK | Credential file permissions: ") {
		t.Fatalf("want a clean report, got %v:\n%s", d.problems, out)
	}

	// A deployment without the container's password file and a data directory with no credentials at all are clean.
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "mcp.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	d = &doctorReport{l: i18n.En}
	d.credentials(plain)
	if len(d.problems) != 0 {
		t.Fatalf("an absent credential is nothing to check: %v", d.problems)
	}
}

func TestDoctorRouteAndAddressDiagnostics(t *testing.T) {
	v4 := "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n" +
		"wg0 00000000 00000000 0001 0 0 0 00000000 0 0 0\n" +
		"eth0 000002C0 00000000 0001 0 0 0 00FFFFFF 0 0 0\n"
	v6 := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000000 00000000 00200200 lo\n" +
		"20010db8000000000000000000000000 20 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000000 00000000 00000001 tun0\n"
	if got := doctorRouteInterfaces(v4, false); !reflect.DeepEqual(got, []string{"wg0"}) {
		t.Fatalf("IPv4 routes: %v", got)
	}
	if got := doctorRouteInterfaces(v6, true); !reflect.DeepEqual(got, []string{"tun0"}) {
		t.Fatalf("IPv6 routes: %v", got)
	}
	d := &doctorReport{l: i18n.En}
	d.route("wg0", "test route")
	if !strings.Contains(reportText(d), "a TUN proxy may swallow replies to inbound connections") || len(d.next) == 0 {
		t.Fatal("missing actionable TUN warning")
	}
	a := netaddr.Addr{IP: netip.MustParseAddr("2001:db8::2"), InterfaceFlags: net.FlagUp,
		FlagsKnown: true, ScopeKnown: true, Virtual: true,
		Flags:          netaddr.FlagTemporary | netaddr.FlagDeprecated | netaddr.FlagTentative | netaddr.FlagDADFailed,
		LifetimesKnown: true}
	reason := doctorAddressReason(i18n.En, a)
	for _, want := range []string{"not a global IPv6 address", "virtual or loopback interface", "temporary privacy address", "deprecated address",
		"address detection not finished", "duplicate address detection failed", "address lifetime has ended"} {
		if !strings.Contains(reason, want) {
			t.Errorf("missing exclusion reason %q", want)
		}
	}
}

// The report prints as it always did: a title, "OK | item: detail" and "Problem | item: action" lines, an item's later
// lines indented under it, then the next steps; the same items go to the admin page as they are.
func TestDoctorReportLayout(t *testing.T) {
	d := &doctorReport{l: i18n.En}
	d.note("before any item")
	d.ok("Item A", "fine")
	d.problem("Item B", "fix it")
	d.note("detail line")
	d.info("Item C", "found")
	d.next = append(d.next, "Step two")
	want := "Hub self-check (read-only)\nbefore any item\nOK | Item A: fine\nProblem | Item B: fix it\n  detail line\n" +
		"Item C: found\n\nNext steps:\n- fix it\n- Step two\n"
	if got := reportText(d); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if !reflect.DeepEqual(d.problems, []string{"Item B: fix it"}) {
		t.Fatalf("problems %v", d.problems)
	}
	if d.items[2].Detail != "fix it\ndetail line" || d.items[3].Level != "info" {
		t.Fatalf("items %+v", d.items)
	}
	empty := &doctorReport{l: i18n.En}
	b, _ := json.Marshal(map[string]any{"items": empty.itemsOrEmpty(), "next": empty.nextOrEmpty()})
	if string(b) != `{"items":[],"next":[]}` {
		t.Fatalf("an empty report is empty lists, not null: %s", b)
	}
}

// Every language words the report from its catalog; zh-Hans reads exactly as the report did before it had any other.
func TestDoctorSpeaksEveryLanguage(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, l := range i18n.Langs {
		d := &doctorReport{l: l}
		d.config(missing)
		out := reportText(d)
		if !strings.HasPrefix(out, i18n.T(l, "hub-doctor.title")+"\n") || !strings.Contains(out, i18n.T(l, "hub-doctor.config_unreadable")) {
			t.Errorf("%s:\n%s", l, out)
		}
		if strings.Contains(out, "hub-doctor.") {
			t.Errorf("%s shows a key:\n%s", l, out)
		}
	}
	zh := &doctorReport{l: i18n.ZhHans}
	zh.config(missing)
	if got := reportText(zh); got != "hub 自检（只读）\n有问题｜配置：无法读取 ~/.persona/hub.json；请确认配置存在且当前用户有读取权限\n\n下一步：\n"+ // i18n-ignore: the zh-Hans wording
		"- 无法读取 ~/.persona/hub.json；请确认配置存在且当前用户有读取权限\n" { // i18n-ignore: the zh-Hans wording
		t.Fatalf("zh-Hans changed:\n%s", got)
	}
	ja := &doctorReport{l: i18n.Ja}
	ja.config(missing)
	if out := reportText(ja); !strings.Contains(out, "問題あり｜設定：") || !strings.Contains(out, "次の手順：") { // i18n-ignore: the ja wording
		t.Fatalf("ja:\n%s", out)
	}
}

// `personad hub doctor` speaks the terminal's language (#5520): English when it names none.
func TestDoctorCommandSpeaksTheTerminalLanguage(t *testing.T) {
	t.Setenv("PERSONA_HOME", t.TempDir()) // no hub here: the report is about a missing hub.json
	for _, c := range []struct{ env, title string }{
		{"ja_JP.UTF-8", "ハブの自己診断（読み取り専用）"}, // i18n-ignore: the ja wording
		{"C", "Hub self-check (read-only)"},
	} {
		t.Setenv("PERSONA_LANG", c.env)
		var out bytes.Buffer
		if err := Doctor(context.Background(), &out); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), c.title+"\n") {
			t.Fatalf("PERSONA_LANG=%s:\n%s", c.env, out.String())
		}
	}
}
