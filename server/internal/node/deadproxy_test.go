package node

import (
	"cmp"
	"context"
	"io"
	"log"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// proxyPorts: an address something listens on and one nothing does (a proxy app that has quit). The answers the
// checks remember are forgotten after the test.
func proxyPorts(t *testing.T) (alive, dead string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead = gone.Addr().String()
	_ = gone.Close()
	forgetProxyChecks(t)
	return "http://" + ln.Addr().String(), "http://" + dead
}

func forgetProxyChecks(t *testing.T) {
	t.Helper()
	reset := func() {
		proxySeen.Lock()
		proxySeen.m = nil
		proxySeen.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// A provider's own environment (Env) goes last: it wins over what Persona sets, the shell's copy of each variable is
// dropped first, a proxy variable counts in both spellings, and an empty value sets a variable empty.
func TestLauncherEnvWinsAndClears(t *testing.T) {
	l := proto.ManagedLauncher{Name: "p", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1",
		Env: "ANTHROPIC_MODEL=m2\n# a note\nexport FOO='bar baz'\nHTTP_PROXY=\nnot an assignment\nNO_PROXY=a\nno_proxy=b"}
	env := providerEnv(l, "", "", false)
	if env["ANTHROPIC_MODEL"] != "m2" || env["FOO"] != "bar baz" {
		t.Errorf("env: model %q, FOO %q", env["ANTHROPIC_MODEL"], env["FOO"])
	}
	for _, k := range []string{"HTTP_PROXY", "http_proxy"} {
		if v, ok := env[k]; !ok || v != "" {
			t.Errorf("%s is not set empty: %q %v", k, v, ok)
		}
	}
	if env["NO_PROXY"] != "a" || env["no_proxy"] != "b" {
		t.Errorf("both spellings given: %q %q", env["NO_PROXY"], env["no_proxy"])
	}
	if _, ok := env["not an assignment"]; ok {
		t.Error("a line that is no assignment went in")
	}
	clear := envClearFor(l)
	for _, k := range []string{"FOO", "HTTP_PROXY", "http_proxy", "ANTHROPIC_MODEL"} {
		if !slices.ContainsFunc(clear, func(c string) bool {
			return c == k || strings.HasSuffix(c, "*") && strings.HasPrefix(k, strings.TrimSuffix(c, "*"))
		}) {
			t.Errorf("%s is not dropped from the shell: %q", k, clear)
		}
	}
	if !providerShaped(proto.ManagedLauncher{Name: "e", Env: "FOO=1"}) {
		t.Error("a launcher with its own environment is not a provider")
	}
	// An assistant conversation decides its own thinking level; its compact window is the launcher's (#3066).
	main := providerEnv(proto.ManagedLauncher{Name: "p", Kind: "api", Env: "CLAUDE_CODE_EFFORT_LEVEL=high\nCLAUDE_CODE_AUTO_COMPACT_WINDOW=5\nFOO=1"}, "", "", true)
	if _, ok := main["CLAUDE_CODE_EFFORT_LEVEL"]; ok || main["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "5" || main["FOO"] != "1" {
		t.Errorf("main conversation: %v", main)
	}
	// An agent that is no Claude Code gets it too, over what Persona sets.
	codex := managedEnv(proto.ManagedLauncher{Name: "c", Agent: "codex", BaseURL: "https://a.example.com", Env: "OPENAI_BASE_URL=https://b.example.com\nFOO=1"})
	if !slices.Equal(codex, []string{"OPENAI_BASE_URL=https://b.example.com", "FOO=1"}) {
		t.Errorf("codex: %q", codex)
	}
}

func TestManagedLauncherProxyEnv(t *testing.T) {
	const proxy = "http://proxy.example:3128"
	const bypass = "gateway.example"
	inherited := []string{
		"HTTP_PROXY=old-upper-http", "http_proxy=old-lower-http",
		"HTTPS_PROXY=old-upper-https", "https_proxy=old-lower-https",
		"NO_PROXY=old-upper-bypass", "no_proxy=old-lower-bypass",
		"ALL_PROXY=old-upper-all", "all_proxy=old-lower-all", "PATH=/bin",
	}
	for _, tc := range []struct {
		name, proxy, bypass, env string
		want                     map[string]string
	}{
		{name: "inherit"},
		{name: "bypass only", bypass: bypass, want: map[string]string{"NO_PROXY": bypass, "no_proxy": bypass}},
		{name: "proxy and bypass", proxy: proxy, bypass: bypass, want: map[string]string{
			"HTTP_PROXY": proxy, "http_proxy": proxy, "HTTPS_PROXY": proxy, "https_proxy": proxy,
			"NO_PROXY": bypass, "no_proxy": bypass,
		}},
		{name: "proxy clears bypass", proxy: proxy, want: map[string]string{
			"HTTP_PROXY": proxy, "http_proxy": proxy, "HTTPS_PROXY": proxy, "https_proxy": proxy,
			"NO_PROXY": "", "no_proxy": "",
		}},
		{name: "env overrides fields", proxy: proxy, bypass: bypass,
			env: "HTTP_PROXY=\nhttps_proxy=http://override.example:8080\nno_proxy=override.example",
			want: map[string]string{
				"HTTP_PROXY": "", "http_proxy": "",
				"HTTPS_PROXY": "http://override.example:8080", "https_proxy": "http://override.example:8080",
				"NO_PROXY": "override.example", "no_proxy": "override.example",
			}},
		{name: "explicit spellings stay distinct", bypass: bypass, env: "NO_PROXY=upper.example\nno_proxy=lower.example\nNO_PROXY=last.example",
			want: map[string]string{"NO_PROXY": "last.example", "no_proxy": "lower.example"}},
		{name: "env alone", env: "all_proxy=http://override.example:8080\nNO_PROXY=override.example", want: map[string]string{
			"ALL_PROXY": "http://override.example:8080", "all_proxy": "http://override.example:8080",
			"NO_PROXY": "override.example", "no_proxy": "override.example",
		}},
	} {
		for _, agent := range []string{"claude", "codex", "agy"} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				l := proto.ManagedLauncher{Agent: agent, Proxy: tc.proxy, NoProxy: tc.bypass, Env: tc.env}
				env := managedEnv(l)
				remaining := withoutEnv(inherited, envClearFor(l))
				for _, old := range inherited {
					key, _, _ := strings.Cut(old, "=")
					value, overrides := tc.want[key]
					var got, want []string
					for _, entry := range env {
						if strings.HasPrefix(entry, key+"=") {
							got = append(got, entry)
						}
					}
					if overrides {
						want = []string{key + "=" + value}
					}
					if !slices.Equal(got, want) {
						t.Errorf("managed %s: got %q, want %q", key, got, want)
					}
					if kept := slices.Contains(remaining, old); kept == overrides {
						t.Errorf("kept inherited %s: got %t, want %t", key, kept, !overrides)
					}
				}
			})
		}
	}
}

const printProxyEnv = `printf '%s|%s|%s|%s\n' "${HTTPS_PROXY-unset}" "${https_proxy-unset}" "${NO_PROXY-unset}" "${no_proxy-unset}"`
const inheritedProxyEnv = "export HTTPS_PROXY=old https_proxy=old NO_PROXY=old no_proxy=old\n"

func TestManagedLauncherCheckProxyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX launch scripts")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("requires zsh")
	}
	for _, agent := range []string{"codex", "agy"} {
		t.Run(agent, func(t *testing.T) {
			home := launchKeyHome(t)
			t.Setenv("SHELL", zsh)
			t.Setenv("ZDOTDIR", home)
			t.Setenv("PATH", home+":/usr/bin:/bin")
			writeFile(t, filepath.Join(home, ".zshrc"), "export PATH="+shellQuote(home+":/usr/bin:/bin")+"\n"+inheritedProxyEnv)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			personad := filepath.Join(home, "personad")
			if err := os.WriteFile(personad, []byte("#!/bin/sh\nNODE_TEST_LAUNCH=1 exec "+shellQuote(self)+" \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, agent), []byte("#!/bin/sh\n"+printProxyEnv+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			const proxy = "http://proxy.example:3128"
			n, _ := testNode(t)
			n.bin, n.home = personad, home
			n.setCatalog([]proto.ManagedLauncher{{Name: "managed-proxy", Agent: agent, Proxy: proxy}})
			ln, err := n.listenOnce(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			got := checkRun(t, n, map[string]any{"name": "managed-proxy"})
			if want := proxy + "|" + proxy + "||"; !got.OK || got.Answer != want {
				t.Fatalf("check result = %+v, want proxy environment %q", got, want)
			}
		})
	}
}

func TestManagedCodexAssistantProxyEnv(t *testing.T) {
	fakeCodexHome(t, testThread)
	home := os.Getenv("HOME")
	bin := filepath.Join(home, "bin")
	for _, rc := range []string{".zshrc", ".bashrc"} {
		writeFile(t, filepath.Join(home, rc), "export PATH="+shellQuote(bin+":/usr/bin:/bin")+"\n"+inheritedProxyEnv)
	}
	program := filepath.Join(bin, "codex")
	script, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(home, "proxy-env")
	t.Setenv("PROXY_RECORD", record)
	writeFile(t, program, "#!/bin/sh\n"+printProxyEnv+` > "$PROXY_RECORD"`+"\n"+strings.TrimPrefix(string(script), "#!/bin/sh\n"))
	n := codexTestNode(t)
	withFakeHub(t, n)
	const proxy = "http://override.example:8080"
	n.setCatalog([]proto.ManagedLauncher{{Name: "managed-proxy", Agent: "codex", Proxy: "http://proxy.example:3128", Env: "HTTPS_PROXY=" + proxy}})
	p, err := n.spawnCodex("", t.TempDir(), "managed-proxy", false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.stop)
	got, err := os.ReadFile(record)
	if want := proxy + "|" + proxy + "||\n"; err != nil || string(got) != want {
		t.Fatalf("Codex proxy environment = %q, %v, want %q", got, err, want)
	}
}

// One rule for a line, the hub's (checkEnv) and the apps' too: "export" and any space after it, a byte-order mark,
// quotes, and a comment after an unquoted value.
func TestLauncherEnvLines(t *testing.T) {
	for line, want := range map[string][3]string{
		"export\tFOO=1":            {"FOO", "1", "ok"},
		"\uFEFFFOO=1":              {"FOO", "1", "ok"},
		"HTTP_PROXY= # off":        {"HTTP_PROXY", "", "ok"},
		`A="x # y" # z`:            {"A", "x # y", "ok"},
		"A='it''s'":                {"A", "it", "ok"},
		"B=p#q":                    {"B", "p#q", "ok"},
		"  # a note":               {"", "", "ok"},
		"export =1":                {"", "", "bad"},
		"1A=x":                     {"", "", "bad"},
		"no assignment":            {"", "", "bad"},
		"export FOO_BAR=a b":       {"FOO_BAR", "a b", "ok"},
		"exportFOO=1":              {"exportFOO", "1", "ok"},
		"FOO = spaced  ":           {"FOO", "spaced", "ok"},
		"PATH_LIKE=$HOME/bin:/usr": {"PATH_LIKE", "$HOME/bin:/usr", "ok"},
	} {
		k, v, ok := envLine(line)
		if got := [3]string{k, v, map[bool]string{true: "ok", false: "bad"}[ok]}; got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
}

// A provider's proxy goes in both spellings: Node's fetch reads the lower-case one first, so a lower-case proxy in the
// user's settings would otherwise win. So does its bypass list: Claude Code's own check reads both, but the runtime
// under it takes no_proxy || NO_PROXY, so the shell's lower-case list would still win (#6238). With a proxy of its
// own, an empty list is set too.
func TestProviderProxyBothSpellings(t *testing.T) {
	env := providerEnv(proto.ManagedLauncher{Name: "p", Kind: "api", Proxy: "http://proxy.example:3128", NoProxy: "gw.example.com"}, "", "", false)
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if env[k] != "http://proxy.example:3128" {
			t.Errorf("%s = %q", k, env[k])
		}
	}
	if env["NO_PROXY"] != "gw.example.com" || env["no_proxy"] != "gw.example.com" {
		t.Errorf("no proxy: %v", env)
	}
	env = providerEnv(proto.ManagedLauncher{Name: "p", Kind: "api", Proxy: "http://proxy.example:3128"}, "", "", false)
	if v, ok := env["no_proxy"]; !ok || v != "" || env["NO_PROXY"] != "" {
		t.Errorf("its own proxy with no list: %v", env)
	}
	if _, ok := providerEnv(proto.ManagedLauncher{Name: "p", Kind: "api"}, "", "", false)["no_proxy"]; ok {
		t.Error("following the computer's proxy with no list sets one")
	}
	shell := []string{"HTTPS_PROXY=p", "NO_PROXY=old", "no_proxy=old", "PATH=/bin"}
	if got := withoutEnv(shell, envClearFor(proto.ManagedLauncher{Name: "p", Kind: "api", NoProxy: "gw.example.com"})); !slices.Equal(got, []string{"HTTPS_PROXY=p", "PATH=/bin"}) {
		t.Errorf("no proxy alone: %q", got)
	}
	// At launch the inherited lists go into the provider's (their hosts keep going direct for what Claude runs), each
	// host once: the user's settings' first, as Claude Code applies them over the shell's. One spelling given is both
	// spellings'; with two, each keeps its own.
	ours := []string{"NO_PROXY=gw.example.com", "no_proxy=gw.example.com", "PATH=/bin"}
	for _, tc := range []struct {
		user, shell  map[string]string
		upper, lower string
	}{
		{nil, map[string]string{"no_proxy": "localhost, .corp.example,GW.example.com"}, "gw.example.com,localhost,.corp.example", "gw.example.com,localhost,.corp.example"},
		{nil, map[string]string{"NO_PROXY": ".corp.example"}, "gw.example.com,.corp.example", "gw.example.com,.corp.example"},
		{nil, map[string]string{"no_proxy": "localhost", "NO_PROXY": ".corp.example"}, "gw.example.com,.corp.example", "gw.example.com,localhost"},
		{nil, map[string]string{}, "gw.example.com", "gw.example.com"},
		// The user's settings set NO_PROXY only: the runtime under Claude Code, reading no_proxy, keeps those hosts.
		{map[string]string{"NO_PROXY": "localhost,.corp.example"}, map[string]string{}, "gw.example.com,localhost,.corp.example", "gw.example.com,localhost,.corp.example"},
		{map[string]string{"NO_PROXY": ".corp.example"}, map[string]string{"no_proxy": "localhost", "NO_PROXY": "shell.example"}, "gw.example.com,.corp.example", "gw.example.com,localhost"},
	} {
		got := withInheritedNoProxy(ours, func(k string) string { return cmp.Or(tc.user[k], tc.shell[k]) })
		if want := []string{"NO_PROXY=" + tc.upper, "no_proxy=" + tc.lower, "PATH=/bin"}; !slices.Equal(got, want) {
			t.Errorf("user %v, shell %v: %q", tc.user, tc.shell, got)
		}
	}
	if got := mergeNoProxy("a.example", "*"); got != "*" {
		t.Errorf("a list with *: %q", got)
	}
}

// Only a provider that follows the computer's proxy adds its bypass list to the computer's (#6238). One with a proxy
// of its own, in its fields or its environment, brings its whole list, as one whose environment sets the list does:
// the computer's "*", or the gateway in its list, would turn the provider's proxy off. "*" is no proxy at all.
func TestOnlyAFollowingProviderMergesItsBypassList(t *testing.T) {
	follow := proto.ManagedLauncher{Name: "p", Kind: "api", NoProxy: "gw.example.com"}
	for _, tc := range []struct {
		name string
		l    proto.ManagedLauncher
		want bool
	}{
		{"follow", follow, true},
		{"another variable of its own", proto.ManagedLauncher{Name: "p", NoProxy: "gw.example.com", Env: "X_TEAM=1"}, true},
		{"its own proxy", proto.ManagedLauncher{Name: "p", Proxy: "http://proxy.example:3128", NoProxy: "gw.example.com"}, false},
		{"a proxy in its environment", proto.ManagedLauncher{Name: "p", NoProxy: "gw.example.com", Env: "https_proxy=http://proxy.example:3128"}, false},
		{"the list in its environment", proto.ManagedLauncher{Name: "p", NoProxy: "gw.example.com", Env: "export NO_PROXY=other.example"}, false},
		{"no proxy at all", proto.ManagedLauncher{Name: "p", NoProxy: "*"}, false},
		{"no list", proto.ManagedLauncher{Name: "p", Kind: "api"}, false},
	} {
		if got := mergesNoProxy(tc.l); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	// `personad launch` is told to merge, with the user's settings' lists, which come before the shell's.
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "profile"))
	writeFile(t, filepath.Join(home, "profile", "settings.json"), `{"env":{"NO_PROXY":".corp.example","HTTPS_PROXY":"http://proxy.example:3128"}}`)
	n := &Node{home: home}
	n.setManaged([]proto.ManagedLauncher{follow, {Name: "o", Kind: "api", Proxy: "http://proxy.example:3128", NoProxy: "gw.example.com"}})
	if got, _ := n.launcherEnv("p")["no_proxy_merge"].(map[string]string); !maps.Equal(got, map[string]string{"NO_PROXY": ".corp.example"}) {
		t.Errorf("follow: %v", n.launcherEnv("p"))
	}
	if _, told := n.launcherEnv("o")["no_proxy_merge"]; told {
		t.Errorf("its own proxy: %v", n.launcherEnv("o"))
	}
}

// At launch a proxy the shell still names that no longer answers is dropped, with a word on stderr, when the gateway
// answers without it; one that answers stays (a gateway reachable only through it), so does one the launcher sets
// itself, and so does every one when the gateway does not answer either (the proxy may come back).
func TestLaunchDropsDeadInheritedProxies(t *testing.T) {
	alive, dead := proxyPorts(t)
	var said []string
	warn := func(s string) { said = append(said, s) }
	env := []string{"HTTP_PROXY=" + dead, "https_proxy=" + alive, "ALL_PROXY=" + dead, "PATH=/bin"}
	got := withoutDeadProxies(env, []string{"ALL_PROXY=" + dead}, alive, warn)
	if !slices.Equal(got, []string{"https_proxy=" + alive, "ALL_PROXY=" + dead, "PATH=/bin"}) {
		t.Fatalf("env: %q", got)
	}
	if len(said) != 1 || !strings.Contains(said[0], "HTTP_PROXY") || !strings.Contains(said[0], "does not answer") {
		t.Errorf("warned: %q", said)
	}
	if got := withoutDeadProxies(env, nil, dead, warn); !slices.Equal(got, env) {
		t.Errorf("the gateway does not answer either, yet: %q", got)
	}
	// Bare host:port and a user in the URL: read, and the user never shown.
	if got := withoutDeadProxies([]string{"HTTPS_PROXY=" + strings.TrimPrefix(dead, "http://")}, nil, alive, warn); len(got) != 0 {
		t.Errorf("a bare dead host:port stayed: %q", got)
	}
	said = nil
	withoutDeadProxies([]string{"HTTPS_PROXY=http://me:secret@" + strings.TrimPrefix(dead, "http://")}, nil, alive, warn)
	if len(said) != 1 || strings.Contains(said[0], "secret") || strings.Contains(said[0], "me:") {
		t.Errorf("the warning shows the proxy's user: %q", said)
	}
}

func TestProxyAddr(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1:7890":     "127.0.0.1:7890",
		"127.0.0.1:7890":            "127.0.0.1:7890",
		"user:pw@proxy.example:808": "proxy.example:808",
		"http://[::1]:7890":         "[::1]:7890",
		"https://proxy.example":     "proxy.example:443",
		"http://proxy.example":      "proxy.example:80",
		"socks5h://proxy.example":   "proxy.example:1080",
		"ftp://proxy.example":       "",
		"":                          "",
	} {
		if got := proxyAddr(raw); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}

// The user's settings.json (in the Claude config directory) names a proxy that no longer answers: a managed launcher's
// settings env sets it empty (Claude Code applies settings env over the environment, and settings env cannot unset)
// when its gateway answers without it, and keeps what the launcher sets itself. A proxy that answers is left alone,
// and so is a dead one when the gateway does not answer either.
func TestManagedSettingsTakeAwayADeadUserProxy(t *testing.T) {
	alive, dead := proxyPorts(t)
	for _, tc := range []struct {
		proxy, gateway string
		empty          bool
	}{{dead, alive, true}, {alive, alive, false}, {dead, dead, false}} {
		home := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "profile"))
		n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
		writeFile(t, filepath.Join(home, "profile", "settings.json"), `{"env":{"HTTP_PROXY":"`+tc.proxy+`","https_proxy":"`+tc.proxy+`"}}`)
		n.setManaged([]proto.ManagedLauncher{{Name: "m", Kind: "api", BaseURL: tc.gateway, Model: "m1", NoProxy: "gw.example.com",
			Env: "HTTPS_PROXY=" + alive}})
		env, _ := readSettings(t, n.claudeSettings(context.Background(), "m", "", home))["env"].(map[string]any)
		v, set := env["HTTP_PROXY"]
		if tc.empty && (!set || v != "") || !tc.empty && set {
			t.Errorf("proxy %s, gateway %s: HTTP_PROXY in the settings env = %v (%v)", tc.proxy, tc.gateway, v, set)
		}
		if env["https_proxy"] != alive || env["HTTPS_PROXY"] != alive {
			t.Errorf("the launcher's own proxy: %v", env)
		}
		// Its own proxy brings its own bypass list, pinned over the user's settings in both spellings.
		if env["NO_PROXY"] != "gw.example.com" || env["no_proxy"] != "gw.example.com" {
			t.Errorf("the launcher's own bypass list: %v", env)
		}
	}
}

// A provider that follows the computer's proxy adds its bypass list to each spelling the user's settings set (which
// Claude Code applies over the environment) and pins those; a spelling they leave alone is the launch environment's,
// with the shell's (#6238). One with its own proxy pins its own list, an empty one too; "*" is pinned as it is.
func TestManagedSettingsPinTheBypassList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "profile"))
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
	n.setManaged([]proto.ManagedLauncher{
		{Name: "f", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1", NoProxy: "gw.example.com"},
		{Name: "o", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1", Proxy: "http://proxy.example:3128", NoProxy: "gw.example.com"},
		{Name: "d", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1", NoProxy: "*"},
		{Name: "e", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1", Proxy: "http://proxy.example:3128"},
	})
	pinned := func(name string) (upper, lower any) {
		env, _ := readSettings(t, n.claudeSettings(context.Background(), name, "", home))["env"].(map[string]any)
		return env["NO_PROXY"], env["no_proxy"]
	}
	settings := filepath.Join(home, "profile", "settings.json")
	for _, tc := range []struct {
		user, name   string
		upper, lower any
	}{
		{`{}`, "f", nil, nil},
		{`{"env":{"NO_PROXY":".corp.example"}}`, "f", "gw.example.com,.corp.example", nil},
		{`{"env":{"no_proxy":"localhost,.corp.example","NO_PROXY":"upper.example"}}`, "f", "gw.example.com,upper.example", "gw.example.com,localhost,.corp.example"},
		{`{"env":{"no_proxy":"localhost"}}`, "o", "gw.example.com", "gw.example.com"},
		{`{"env":{"no_proxy":"*"}}`, "e", "", ""},
		{`{"env":{"no_proxy":"localhost"}}`, "d", "*", "*"},
	} {
		writeFile(t, settings, tc.user)
		if up, low := pinned(tc.name); up != tc.upper || low != tc.lower {
			t.Errorf("%s with %s: %v %v", tc.name, tc.user, up, low)
		}
	}
}
