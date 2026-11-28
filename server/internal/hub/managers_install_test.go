package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
)

// A new manager's answer carries the commands that install it; GET /i/<code> is the script they fetch, a valid
// sh program with the hub and the code in it, and getting it does not use the code up; codes it does not know are
// 404 and count against the source.
func TestManagerInstallScript(t *testing.T) {
	h, srv, _ := managersHub(t)
	h.cfg.PublicURL = "" // the commands follow the address the app used (TestManagerCommandsUseTheHubsOwnAddress)
	t.Setenv("PERSONA_IMAGE", "")
	var made struct {
		Manager  managerInfo       `json:"manager"`
		Code     string            `json:"code"`
		Commands map[string]string `json:"commands"`
	}
	if code := managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "工作"}, &made); code != 201 {
		t.Fatalf("make: %d", code)
	}
	id := made.Manager.ID
	if want := "curl -fsSL " + srv.URL + "/i/" + made.Code + " | sh"; made.Commands["service"] != want {
		t.Fatalf("service %q, want %q", made.Commands["service"], want)
	}
	if want := "personad manager join " + srv.URL + " " + made.Code + " --install"; made.Commands["join"] != want {
		t.Fatalf("join %q", made.Commands["join"])
	}
	if _, ok := made.Commands["docker"]; ok {
		t.Fatalf("docker without an image: %q", made.Commands["docker"])
	}

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	status, script := get("/i/" + made.Code)
	if status != 200 || !strings.Contains(script, "HUB="+srv.URL+"\n") || !strings.Contains(script, "CODE="+made.Code+"\n") ||
		!strings.Contains(script, `manager join "$HUB" "$CODE"  --install "$@"`) || !strings.Contains(script, id) {
		t.Fatalf("%d:\n%s", status, script)
	}
	f := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}
	if status, _ := get("/i/mi_0123456789abcdef0123456789abcdef"); status != 404 {
		t.Fatalf("unknown code: %d", status)
	}
	if status, _ := get("/i/nonsense"); status != 404 {
		t.Fatalf("not a code: %d", status)
	}

	// The script did not use it up: the join trades it; then the script is gone.
	var joined struct{ ID, Token string }
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": made.Code}, &joined); code != 200 || joined.ID != id {
		t.Fatalf("join after the script: %d %+v", code, joined)
	}
	if status, _ := get("/i/" + made.Code); status != 404 {
		t.Fatalf("script of a used code: %d", status)
	}

	// With an image the hub knows, the container's command too, in the manager's own directory of the volume.
	t.Setenv("PERSONA_IMAGE", "registry.example/persona-hub:1")
	var again struct {
		Code     string            `json:"code"`
		Commands map[string]string `json:"commands"`
	}
	if code := managersDo(t, h, srv, "POST", "/api/v1/managers/"+id+"/code", nil, &again); code != 200 {
		t.Fatalf("new code: %d", code)
	}
	n := "persona-mgr-" + id
	if want := "curl -fsSL " + srv.URL + "/i/" + again.Code + "/docker | sh"; again.Commands["docker"] != want {
		t.Fatalf("docker %q, want %q", again.Commands["docker"], want)
	}
	status, script = get("/i/" + again.Code + "/docker")
	if status != 200 || !strings.Contains(script, "IMAGE=registry.example/persona-hub:1\n") || !strings.Contains(script, "CODE="+again.Code+"\n") {
		t.Fatalf("%d:\n%s", status, script)
	}
	// The hub makes up no password: the shell that runs the script does, and passes it through the environment only.
	if strings.Contains(script, "USER:PASSWORD") || strings.Contains(script, "--database-url") || strings.Contains(script, `POSTGRES_PASSWORD="`) {
		t.Fatalf("a password on a command line:\n%s", script)
	}
	runDockerScript(t, n, id, srv.URL, again.Code, script)
	if status, _ := get("/i/mi_0123456789abcdef0123456789abcdef/docker"); status != 404 {
		t.Fatalf("unknown code's docker script: %d", status)
	}
}

// On a computer with no personad, the service script downloads it (checked against the sums the hub gave) to
// bin/personad and joins with it: the file is the program itself, not a link, and the join runs.
func TestManagerInstallScriptDownloadsPersonad(t *testing.T) {
	plat := map[string]string{"linux": "linux", "darwin": "darwin"}[runtime.GOOS] + "-" + map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	if strings.HasPrefix(plat, "-") || strings.HasSuffix(plat, "-") {
		t.Skip("no build for this platform")
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(compatibleManagerBinary + "echo \"personad $*\" >> \"$HOME/calls\"\n"))
	_ = zw.Close()
	sum := sha256.Sum256(gz.Bytes())
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/personad-"+plat+".gz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(gz.Bytes())
	}))
	defer dl.Close()
	script := managerInstallScript(managerScript{ID: "m-0a1b2c", Name: "Work", Hub: "https://hub.example.com", Code: "mi_x", DL: dl.URL,
		Version: "v1", Sums: map[string]string{"personad-" + plat + ".gz": hex.EncodeToString(sum[:])}})
	home := t.TempDir()
	f := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(f, []byte(scriptOnlyHere(t, script, t.TempDir(), home)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, filepath.Join(home, ".persona", "bin")} { // where it is run from matters to ln
		_ = os.MkdirAll(dir, 0o755)
		cmd := exec.Command("sh", f)
		cmd.Dir = dir
		// No proxy of the machine running the test (scriptEnv takes none along): the download comes from this test's
		// own server. The system's programs (curl, gzip, shasum) are on PATH.
		cmd.Env = scriptEnv(home, "/usr/bin:/bin", "no_proxy=*", "NO_PROXY=*", "CURL_HOME="+home)
		out, err := cmd.CombinedOutput()
		bin := filepath.Join(home, ".persona", "bin", "personad")
		fi, lerr := os.Lstat(bin)
		calls, _ := os.ReadFile(filepath.Join(home, "calls"))
		if err != nil || lerr != nil || fi.Mode()&os.ModeSymlink != 0 || !strings.Contains(string(calls), "personad manager join https://hub.example.com mi_x") {
			t.Fatalf("from %s: %v %v\n%s\ncalls:\n%s", dir, err, lerr, out, calls)
		}
		_ = os.Remove(bin)
		_ = os.Remove(filepath.Join(home, "calls"))
	}
}

// On a computer whose node service runs the device's own program, the service script moves the node back to personad
// after the join (the manager's personad updates with the node), with the persona-link install.sh put in the data
// directory, the one in the Persona Link Mac app (in the system's Applications folder or the user's) or one on PATH;
// with the node on personad, it leaves it alone; with no persona-link anywhere, it says what to do and the install
// still succeeds. Every place it looks is the test's own (#16580): the persona-link it runs is the test's fake.
func TestManagerInstallScriptPutsTheNodeBackOnPersonad(t *testing.T) {
	script := managerInstallScript(managerScript{ID: "m-0a1b2c", Name: "Work", Hub: "https://hub.example.com", Code: "mi_x"})
	if bad := placesOutside(script); len(bad) != 1 || !strings.HasPrefix(bad[0], "/Applications/") {
		t.Fatalf("the served script looks for the Mac app in /Applications, and nothing else of this computer: %q", bad)
	}
	const appLink = "Persona Link.app/Contents/Helpers/persona-link"
	// The fakes say which file ran ($0), so the test knows it was its own.
	const fakeLink = "#!/bin/sh\necho \"link $0 $*\" >> \"$HOME/calls\"\n"
	for _, tc := range []struct {
		name, runs string
		in, link   string // persona-link at link under in: "home", "apps" (the system Applications folder) or "path"; "" none
		mac        bool
	}{
		{"linux", "persona-node", "home", ".persona/bin/persona-link", false},
		{"on personad", "personad", "home", ".persona/bin/persona-link", false},
		{"mac app", "persona-node", "apps", appLink, true},
		{"mac app in the home folder", "persona-node", "home", "Applications/" + appLink, true},
		{"on PATH", "persona-node", "path", "persona-link", false},
		{"no persona-link", "persona-node", "", "", false},
	} {
		home, apps, tools := t.TempDir(), t.TempDir(), scriptTools(t, "grep")
		bin := filepath.Join(home, ".persona", "bin")
		_ = os.MkdirAll(bin, 0o755)
		personad := filepath.Join(bin, "personad")
		_ = os.WriteFile(personad, []byte(compatibleManagerBinary+"echo \"personad $0 $*\" >> \"$HOME/calls\"\n"), 0o755)
		link := ""
		if tc.in != "" {
			link = filepath.Join(map[string]string{"home": home, "apps": apps, "path": tools}[tc.in], tc.link)
			_ = os.MkdirAll(filepath.Dir(link), 0o755)
			_ = os.WriteFile(link, []byte(fakeLink), 0o755)
		}
		// The node service, running tc.runs: persona-link's systemd unit, or its launchd agent.
		svc := filepath.Join(home, ".config", "systemd", "user", "personad-node.service")
		content := "[Service]\nExecStart=\"" + filepath.Join(bin, tc.runs) + "\" node --attach\n"
		if tc.mac {
			svc = filepath.Join(home, "Library", "LaunchAgents", "com.persona.node.plist")
			content = "<array><string>" + filepath.Join(bin, tc.runs) + "</string><string>node</string><string>--attach</string></array>\n"
		}
		_ = os.MkdirAll(filepath.Dir(svc), 0o755)
		_ = os.WriteFile(svc, []byte(content), 0o644)
		f := filepath.Join(t.TempDir(), "install.sh")
		if err := os.WriteFile(f, []byte(scriptOnlyHere(t, script, apps, home, tools)), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", f)
		cmd.Env = scriptEnv(home, tools)
		out, err := cmd.CombinedOutput()
		calls, _ := os.ReadFile(filepath.Join(home, "calls"))
		joined := strings.Index(string(calls), "personad "+personad+" manager join https://hub.example.com mi_x")
		back := strings.Index(string(calls), "link "+link+" node switch --to personad\n")
		wantBack := tc.runs == "persona-node" && tc.link != ""
		if err != nil || joined < 0 || wantBack != (back > joined) {
			t.Fatalf("%s: %v\n%s\ncalls:\n%s", tc.name, err, out, calls)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
			if (strings.HasPrefix(line, "link ") && (!wantBack || !strings.HasPrefix(line, "link "+link+" "))) ||
				(strings.HasPrefix(line, "personad ") && !strings.HasPrefix(line, "personad "+personad+" ")) {
				t.Fatalf("%s: ran something other than the test's own fake: %q\ncalls:\n%s", tc.name, line, calls)
			}
		}
		if tc.link == "" && !strings.Contains(string(out), "no persona-link") {
			t.Fatalf("%s: nothing said:\n%s", tc.name, out)
		}
	}
}

// systemLinkApp is the first place the service script looks for the Persona Link Mac app: the system's Applications
// folder.
const systemLinkApp = `"/Applications/Persona Link.app/Contents/Helpers/persona-link"`

// scriptAbsPath finds the absolute paths a shell script names: a word that starts with a slash (a URL's // is no word).
var scriptAbsPath = regexp.MustCompile(`(?:^|[\s"'=(<>|;])(/[^\s"';)|<>]*)`)

// placesOutside is what script names of the computer running it, outside dirs: the absolute paths (/dev/null and the
// node service check's grep pattern aside) and the service managers' commands.
func placesOutside(script string, dirs ...string) []string {
	var out []string
	for _, cmd := range []string{"launchctl", "systemctl"} {
		if strings.Contains(script, cmd) {
			out = append(out, cmd)
		}
	}
	for _, m := range scriptAbsPath.FindAllStringSubmatch(script, -1) {
		p := m[1]
		if p == "/dev/null" || p == "/persona-node[" || slices.ContainsFunc(dirs, func(d string) bool { return p == d || strings.HasPrefix(p, d+"/") }) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// scriptOnlyHere is the service script as a test runs it: the system Applications folder it looks in for the Mac app is
// apps, a directory of the test's own. It fails the test, before anything runs, when the script would still reach this
// computer rather than apps and dirs (placesOutside).
func scriptOnlyHere(t *testing.T, script, apps string, dirs ...string) string {
	t.Helper()
	if strings.Count(script, systemLinkApp) != 1 {
		t.Fatalf("the service script no longer looks for the Mac app at %s, once: point the test's stand-in at where it looks now", systemLinkApp)
	}
	out := strings.Replace(script, systemLinkApp, `"`+apps+`/Persona Link.app/Contents/Helpers/persona-link"`, 1)
	if bad := placesOutside(out, append(dirs, apps)...); len(bad) > 0 {
		t.Fatalf("the script would reach this computer, not the test's directories: %q", bad)
	}
	return out
}

// scriptEnv is all of the environment the service script gets: HOME, PATH and extra. Nothing of this computer's own
// (its PERSONA_HOME, XDG_CONFIG_HOME, proxies) comes along, so where the script looks follows home.
func scriptEnv(home, path string, extra ...string) []string {
	return append([]string{"HOME=" + home, "PATH=" + path}, extra...)
}

// scriptTools is a PATH of the test's own: a directory with links to the system programs named and nothing else, so a
// lookup on PATH (command -v persona-link) finds only what the test puts there.
func scriptTools(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		found := ""
		for _, sys := range []string{"/usr/bin", "/bin"} {
			if fi, err := os.Stat(filepath.Join(sys, name)); err == nil && fi.Mode()&0o111 != 0 {
				found = filepath.Join(sys, name)
				break
			}
		}
		if found == "" {
			t.Fatalf("no %s in /usr/bin or /bin", name)
		}
		if err := os.Symlink(found, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeDockerScript is docker for the install script: it keeps networks, containers, volumes and the passwords they
// were made with in files under $STATE, logs every call's arguments and the two variables a password goes in, and
// answers `run --rm` (reading manager.json in the home volume) with the password kept there.
const fakeDockerScript = `#!/bin/sh
S="$STATE"
echo "ARGV $*" >> "$S/log"
echo "ENV pg=${POSTGRES_PASSWORD:-} mdb=${PERSONA_MANAGER_DATABASE_URL:-}" >> "$S/log"
last=; for a in "$@"; do last=$a; done
case "$1 $2" in
"info "*) exit 0 ;;
"network inspect") [ -e "$S/net-$3" ] ;;
"network create") touch "$S/net-$3" ;;
"volume inspect") [ -e "$S/vol-$3" ] ;;
"inspect -f") [ -e "$S/c-$last" ] && echo "POSTGRES_PASSWORD=$(cat "$S/c-$last")" ;;
"start "*) [ -e "$S/c-$2" ] ;;
"run --rm")
	# The script's own sh -c, run here against the home volume (a directory under $S), as the image's sh would.
	[ -d "$S/homevol" ] || exit 1
	sh -c "$(printf '%s' "$last" | sed "s|/home/persona/.persona|$S/homevol|g")" ;;
"run -d")
	name=; prev=; for a in "$@"; do [ "$prev" = --name ] && name=$a; prev=$a; done
	printf %s "${POSTGRES_PASSWORD:-}" > "$S/c-$name"
	case "$name" in
	*-pg) touch "$S/vol-${name%-pg}-data" ;;
	*) touch "$S/vol-$name-home"; printf '%s' "$PERSONA_MANAGER_DATABASE_URL" > "$S/mdb" ;;
	esac ;;
"inspect "*) [ -e "$S/c-$2" ] ;;
"rm -f") rm -f "$S/c-$3" ;;
*) exit 0 ;;
esac
`

// runDockerScript runs the docker install script against fakeDockerScript: it makes the network and both containers
// with one password, given only through the environment; run again (a new code) before the manager joined, it makes
// the manager's container again with the new code and the same password, keeping one docker could not look into;
// once it joined it runs again without making anything; after the containers are gone (volumes kept) it makes them
// again with the same password; and with the database volume left alone (its password lost) it stops and says so.
func runDockerScript(t *testing.T, n, id, hub, code, script string) {
	t.Helper()
	stub, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(strings.ReplaceAll(fakeDockerScript, `\\`, `\`)), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "docker.sh")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	runScript := func(f string) (string, error) {
		_ = os.Remove(filepath.Join(state, "log"))
		cmd := exec.Command("sh", f)
		cmd.Env = append(os.Environ(), "PATH="+stub+":"+os.Getenv("PATH"), "STATE="+state)
		out, err := cmd.CombinedOutput()
		log, _ := os.ReadFile(filepath.Join(state, "log"))
		return string(out) + "\n--- log\n" + string(log), err
	}
	run := func() (string, error) { return runScript(f) }
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(state, name)); return string(b) }
	hex48 := regexp.MustCompile(`[0-9a-f]{48}`)

	out, err := run()
	pw := read("c-" + n + "-pg")
	if err != nil || len(pw) != 48 || read("c-"+n) != "" || !strings.Contains(out, "ARGV network create "+n) ||
		!strings.Contains(out, "ARGV run -d --name "+n+" --network "+n+" --restart unless-stopped -v "+n+"-home:/home/persona/.persona -e PERSONA_MANAGER_DATABASE_URL registry.example/persona-hub:1 manager run --dir /home/persona/.persona/managers/"+id+" --join "+hub+" "+code) ||
		!strings.Contains(out, "mdb=postgres://persona:"+pw+"@"+n+"-pg:5432/persona?sslmode=disable") {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	// Its Postgres runs with pgdocker.ServerArgs, as every container pgdocker makes.
	if !strings.Contains(out, "ARGV run -d --name "+n+"-pg --network "+n+" --restart unless-stopped -v "+n+"-data:/var/lib/postgresql -e POSTGRES_USER=persona -e POSTGRES_DB=persona -e POSTGRES_PASSWORD postgres:18-alpine postgres -c wal_compression=lz4 -c checkpoint_timeout=15min\n") {
		t.Fatalf("first run, the database container:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "ARGV") && hex48.MatchString(line) {
			t.Fatalf("a password on docker's command line: %s", line)
		}
	}

	// Run again before the manager joined, with the command of a new code (the hub refused the first: the container
	// waits for a manager.json that won't come). Docker can't look into its volume: the container is kept.
	fresh := "mi_" + strings.Repeat("f", 32)
	f2 := filepath.Join(t.TempDir(), "docker.sh")
	if err := os.WriteFile(f2, []byte(strings.Replace(script, "CODE="+code+"\n", "CODE="+fresh+"\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runScript(f2)
	if err != nil || strings.Contains(out, "ARGV rm") || strings.Contains(out, "ARGV run -d") || !strings.Contains(out, "there already") {
		t.Fatalf("run again, docker unsure: %v\n%s", err, out)
	}
	// Its volume there, no manager.json in it: made again with the new code, over the same database and password.
	if err := os.MkdirAll(filepath.Join(state, "homevol"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err = runScript(f2)
	if err != nil || !strings.Contains(out, "ARGV rm -f "+n+"\n") || !strings.Contains(out, "never joined") ||
		!strings.Contains(out, "ARGV run -d --name "+n+" ") || !strings.Contains(out, "--join "+hub+" "+fresh) ||
		strings.Contains(out, "ARGV run -d --name "+n+"-pg") || read("c-"+n+"-pg") != pw ||
		!strings.Contains(out, "mdb=postgres://persona:"+pw+"@"+n+"-pg:5432/persona?sslmode=disable") {
		t.Fatalf("run again before it joined: %v\n%s", err, out)
	}

	// What the manager writes on its first start: manager.json with the URL it was given, in its home volume.
	if err := assistant.WriteManagerConfig(filepath.Join(state, "homevol", "managers", id), assistant.ManagerConfig{ID: id,
		Hub: hub, Token: "jb_x", DatabaseURL: read("mdb")}); err != nil {
		t.Fatal(err)
	}

	out, err = run()
	if err != nil || strings.Contains(out, "ARGV run -d") || strings.Contains(out, "ARGV rm") || strings.Contains(out, "ARGV network create") ||
		!strings.Contains(out, "there already") {
		t.Fatalf("run again once it joined: %v\n%s", err, out)
	}

	_ = os.Remove(filepath.Join(state, "c-"+n+"-pg"))
	_ = os.Remove(filepath.Join(state, "c-"+n))
	out, err = run()
	if err != nil || read("c-"+n+"-pg") != pw || !strings.Contains(out, "ARGV run --rm") || !strings.Contains(read("mdb"), "persona:"+pw+"@") {
		t.Fatalf("run after the containers went (volumes kept): %v %q\n%s", err, read("c-"+n+"-pg"), out)
	}

	_ = os.Remove(filepath.Join(state, "c-"+n+"-pg"))
	_ = os.Remove(filepath.Join(state, "c-"+n))
	_ = os.RemoveAll(filepath.Join(state, "homevol"))
	out, err = run()
	if err == nil || !strings.Contains(out, "docker volume rm "+n+"-data") || strings.Contains(out, "ARGV run -d") {
		t.Fatalf("a database volume whose password is gone: %v\n%s", err, out)
	}

}

// A second manager with its own id connects beside the hub's own assistant: both are online in the roster, the
// assistant still answers the hub, the other's reports are not taken yet, and once the second is removed in the app its client stops (bus.ErrRevoked)
// instead of dialing again.
func TestSecondManagerBesideTheAssistant(t *testing.T) {
	h, srv, _ := managersHub(t)
	asst := dialRawBus(t, srv, busToken(t, h, assistantwire.AssistantID, "助手"))
	if err := asst.call("manager.hello", map[string]any{"id": assistantwire.AssistantID}, nil); err != nil {
		t.Fatal(err)
	}
	var made struct {
		Manager managerInfo `json:"manager"`
		Code    string      `json:"code"`
	}
	managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "家"}, &made)
	var joined struct{ ID, Token string }
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": made.Code}, &joined); code != 200 {
		t.Fatalf("join: %d", code)
	}
	c := &bus.Client{Hub: srv.URL, Token: joined.Token, HelloMethod: assistantwire.MethodHello, StopWhenRevoked: true,
		HelloParams: func() any { return map[string]any{"id": joined.ID, "deploy": "service"} },
		Backoff:     10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- c.Run(ctx) }()
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	var list managersReply
	managersDo(t, h, srv, "GET", "/api/v1/managers", nil, &list)
	a, _ := list.find(assistantwire.AssistantID)
	m, _ := list.find(joined.ID)
	if !a.Online || !a.Default || !m.Online || m.Default || m.Deploy != "service" {
		t.Fatalf("roster: %+v", list)
	}
	// Its snapshot is kept as its own: the assistant's stays as it was (manager_state.go). A conversation it lists on
	// the hub's machine m1 is not its (it owns no machine).
	var res assistantwire.SnapshotResult
	if err := c.Call(ctx, assistantwire.MethodSnapshot, map[string]any{"epoch": "x", "seq": 1,
		"sessions": []map[string]any{{"machine_id": "m1", "session_id": "conv-2"}}}, &res); err != nil || !res.OK {
		t.Fatalf("its snapshot: %+v %v", res, err)
	}
	if got := h.conversationOwner("conv-2"); got != "" {
		t.Fatalf("its claim on m1's conversation counted: %q", got)
	}
	if a := h.assistantOf(joined.ID); func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return a.sessions["conv-2"].SessionID == "" }() {
		t.Fatal("its snapshot was not kept as its own")
	}
	if err := c.Call(ctx, "ping", nil, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	var out struct {
		Got json.RawMessage `json:"got"`
	}
	if err := h.callBus(ctx, assistantwire.AssistantID, "echo", map[string]any{"x": 1}, &out); err != nil || string(out.Got) != `{"x":1}` {
		t.Fatalf("the assistant beside it: %s %v", out.Got, err)
	}
	if code := managersDo(t, h, srv, "DELETE", "/api/v1/managers/"+joined.ID, nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	select {
	case err := <-ran:
		if !errors.Is(err, bus.ErrRevoked) {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the removed manager kept going")
	}
	if err := h.callBus(ctx, assistantwire.AssistantID, "echo", map[string]any{}, nil); err != nil {
		t.Fatalf("the assistant after: %v", err)
	}
}

// A core-only hub's install script downloads the core edition's personad (core-<archive>, core-SHA256SUMS), and a
// full hub's script is what it always was: no core name in it.
func TestManagerInstallScriptKeepsTheHubsEdition(t *testing.T) {
	m := managerScript{ID: "m-0a1b2c", Name: "Work", Hub: "https://hub.example.com", Code: "mi_x", DL: "https://dl.example/dl"}
	full := managerInstallScript(m)
	if strings.Contains(full, "core-") {
		t.Fatalf("the full edition's script names a core file:\n%s", full)
	}
	m.Edition = nodeproto.EditionFull
	if managerInstallScript(m) != full {
		t.Fatal("the zero edition and the full edition give different scripts")
	}
	m.Edition = nodeproto.EditionCore
	core := managerInstallScript(m)
	for _, want := range []string{
		`fetch "$DL/core-$archive" "$tmp/$archive"`,
		`fetch "$DL/core-SHA256SUMS" "$tmp/SHA256SUMS"`,
		`-v s="core-$archive"`,
	} {
		if !strings.Contains(core, want) {
			t.Errorf("the core edition's script lacks %q", want)
		}
	}
	for _, never := range []string{`fetch "$DL/$archive"`, `fetch "$DL/SHA256SUMS"`} {
		if strings.Contains(core, never) {
			t.Errorf("the core edition's script still has %q", never)
		}
	}
}
