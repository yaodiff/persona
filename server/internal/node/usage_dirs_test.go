package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// Every way of writing one repository's remote is one key; user names, passwords, tokens and ports never reach it,
// nor its name or host (#16530).
func TestUsageRemote(t *testing.T) {
	same := []string{
		"github.com:Acme/App.git",
		"https://github.com/Acme/App",
		"https://github.com/Acme/App.git/",
		"ssh://github.com:22/Acme/App.git",
		"ssh://ssh.github.com:443/acme/app",
		"git://github.com/acme/app.git",
		"https://www.github.com//Acme//App",
		"https://GITHUB.COM/acme/app",
	}
	for _, remote := range same {
		host, path, key, ok := usageRemote(remote)
		if !ok || key != "git:github.com/acme/app" || host != "github.com" || !strings.EqualFold(path, "acme/app") {
			t.Errorf("%s = %q %q %q %v", remote, host, path, key, ok)
		}
		for _, secret := range []string{"someone", "s3cret", "token", "22", "443"} {
			if strings.Contains(key+path+host, secret) {
				t.Errorf("%s leaked %q into %q %q %q", remote, secret, key, path, host)
			}
		}
	}
	if _, path, _, _ := usageRemote("github.com:Acme/App.git"); path != "Acme/App" {
		t.Errorf("name %q: the remote's own case", path)
	}
	// Elsewhere a path keeps its case; a self-hosted server over SSH on another port is the same as over HTTPS.
	for _, remote := range []string{"ssh://deploy@git.example.com:2222/Team/Tool.git", "https://git.example.com/Team/Tool",
		"deploy@git.example.com:Team/Tool.git", "https://someone:s3cret-token@git.example.com:8443/Team/Tool.git"} {
		host, path, key, ok := usageRemote(remote)
		if !ok || key != "git:git.example.com/Team/Tool" {
			t.Errorf("%s = %q %v", remote, key, ok)
		}
		for _, secret := range []string{"deploy", "someone", "s3cret", "token", "2222", "8443"} {
			if strings.Contains(key+path+host, secret) {
				t.Errorf("%s leaked %q into %q %q %q", remote, secret, key, path, host)
			}
		}
	}
	// Azure DevOps over SSH and HTTPS.
	_, _, a, _ := usageRemote("ssh.dev.azure.com:v3/Org/Project/Repo")
	_, _, b, _ := usageRemote("https://dev.azure.com/Org/Project/_git/Repo")
	if a != b || a != "git:dev.azure.com/org/project/_git/repo" {
		t.Errorf("azure %q %q", a, b)
	}
	for _, remote := range []string{"", "/srv/git/app.git", "../app.git", "./app", "~/repos/app.git", "file:///srv/git/app.git",
		"https://github.com/acme/app?token=x", "https://github.com/acme/app#frag", "ftp://example.com/app", "github.com/acme/app",
		"https://github.com/acme/../app", "https://github.com/acme/a pp", "host::path", "C:/repos/app", "https://[::1]/app"} {
		if _, _, key, ok := usageRemote(remote); ok {
			t.Errorf("%q = %q, want refused", remote, key)
		}
	}
}

func TestConfigRemote(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "config")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for body, want := range map[string]string{
		"[core]\n\tbare = false\n[remote \"fork\"]\n\turl = ssh://example.com/me/app.git\n[remote \"origin\"]\n\turl = https://github.com/acme/app\n": "origin https://github.com/acme/app",
		"[remote \"fork\"]\n\turl = a\n[remote \"upstream\"]\n\turl = b\n":                                                                            "upstream b",
		"[remote \"mine\"]\n\tURL = \"git@example.com:x/y.git\" ; a comment\n\turl = second\n":                                                        "mine git@example.com:x/y.git",
		"[core]\n\tbare = false\n": " ",
	} {
		if name, url, read := configRemote(write(body)); !read || name+" "+url != want {
			t.Errorf("%q = %q %q %v, want %q", body, name, url, read, want)
		}
	}
	if _, _, read := configRemote(filepath.Join(dir, "missing")); read {
		t.Error("a missing config was read")
	}
	// A huge config is read only so far: a remote past the limit is not seen, and nothing waits on it.
	if _, url, read := configRemote(write("[core]\n" + strings.Repeat("# filler line\n", usageDirConfigMost/14+10) + "[remote \"origin\"]\n\turl = x\n")); !read || url != "" {
		t.Errorf("past the limit = %q", url)
	}
}

func TestFirstCwd(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"mode"}` + "\n" + `{"parentUuid":null,"cwd":"/Users/me/code/app","message":{"content":"cd /tmp && \"cwd\":\"/x\""}}`: "/Users/me/code/app",
		`{"type":"session_meta","payload":{"id":"t","cwd":"/work/a \"quoted\" dir","originator":"codex"}}`:                            `/work/a "quoted" dir`,
		`{"type":"session","id":"s","cwd":"/srv/pi"}`:                                                                                 "/srv/pi",
		`{"message":{"content":"he said \"cwd\":\"/no\""}}`:                                                                           "",
		`{"cwd":"/cut-of`: "",
	} {
		if got := firstCwd([]byte(in)); got != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
}

func usageGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// usageTempRootsAre makes roots (and their physical paths) the temporary folders while the test runs: t.TempDir is in
// the system's own, so a test of anything else says there are none.
func usageTempRootsAre(t *testing.T, roots ...string) {
	t.Helper()
	old := usageTempRoots
	var all []string
	for _, r := range roots {
		all = append(all, r, realPath(r))
	}
	usageTempRoots = func() []string { return all }
	t.Cleanup(func() { usageTempRoots = old })
}

func usageMkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A start directory resolves to its repository's remote in any working tree and subfolder of it, a submodule to its
// own; without a remote to its root commit, without a commit to its folder; outside a repository to its folder, home
// to home; and what cannot be read is a folder, what is gone is not resolved (#16530).
func TestResolveUsageDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	usageTempRootsAre(t) // home is in the system's temporary folder; here it is not one
	ctx := context.Background()
	home := t.TempDir()
	resolve := func(dir string) proto.UsageDir {
		t.Helper()
		r, ok := resolveUsageDir(ctx, home, dir)
		if !ok {
			t.Fatalf("%s not resolved", dir)
		}
		return r
	}

	app := usageMkdir(t, home, "code", "app")
	usageGit(t, app, "init", "-q")
	usageGit(t, app, "remote", "add", "origin", "https://github.com/Acme/App.git")
	usageGit(t, app, "commit", "-q", "--allow-empty", "-m", "first")
	usageMkdir(t, app, "web")
	usageGit(t, app, "worktree", "add", "-q", filepath.Join(home, "code", "app-wt", "feature"))
	want := proto.UsageDir{Dir: "~/code/app", Project: "git:github.com/acme/app", Name: "Acme/App", Host: "github.com", Place: "~/code/app"}
	if r := resolve("~/code/app"); r != want {
		t.Fatalf("main checkout = %+v", r)
	}
	if r := resolve("~/code/app/web"); r.Project != want.Project || r.Place != "~/code/app" || r.Worktree {
		t.Fatalf("subfolder = %+v", r)
	}
	if r := resolve("~/code/app-wt/feature"); r.Project != want.Project || r.Place != "~/code/app-wt/feature" || !r.Worktree {
		t.Fatalf("linked worktree = %+v", r)
	}
	// Absolute, as a transcript outside home would name it.
	if r := resolve(app); r.Project != want.Project || r.Dir != app {
		t.Fatalf("absolute = %+v", r)
	}

	// A submodule's checkout is its own repository (its git dir in the parent's .git/modules).
	lib := usageMkdir(t, app, "lib")
	modules := usageMkdir(t, app, ".git", "modules", "lib")
	if err := os.WriteFile(filepath.Join(lib, ".git"), []byte("gitdir: ../.git/modules/lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modules, "config"), []byte("[remote \"origin\"]\n\turl = git@gitlab.example.com:Team/Lib.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := resolve("~/code/app/lib"); r.Project != "git:gitlab.example.com/Team/Lib" || r.Name != "Team/Lib" || r.Place != "~/code/app/lib" {
		t.Fatalf("submodule = %+v", r)
	}

	// No remote: the root commit, the same for a copy on another machine.
	local := usageMkdir(t, home, "scratch")
	usageGit(t, local, "init", "-q")
	usageGit(t, local, "commit", "-q", "--allow-empty", "-m", "first")
	root := usageGit(t, local, "rev-list", "--max-parents=0", "HEAD")
	if r := resolve("~/scratch"); r.Project != "local:"+root || r.Name != "scratch" || r.Host != "" {
		t.Fatalf("no remote = %+v", r)
	}
	fresh := usageMkdir(t, home, "Fresh")
	usageGit(t, fresh, "init", "-q")
	if r := resolve("~/Fresh"); r.Project != "local:dir:fresh" || r.Name != "Fresh" {
		t.Fatalf("no commit = %+v", r)
	}
	// An alias (insteadOf) is what git makes of it.
	tool := usageMkdir(t, home, "tool")
	usageGit(t, tool, "init", "-q")
	usageGit(t, tool, "config", "url.https://github.com/.insteadOf", "gh:")
	usageGit(t, tool, "remote", "add", "origin", "gh:acme/tool")
	if r := resolve("~/tool"); r.Project != "git:github.com/acme/tool" || r.Name != "acme/tool" {
		t.Fatalf("alias = %+v", r)
	}

	// Not in a repository: the folder; the home folder: home.
	usageMkdir(t, home, "Documents", "Trip")
	if r := resolve("~/Documents/Trip"); r != (proto.UsageDir{Dir: "~/Documents/Trip", Project: "folder:trip", Name: "Trip", Place: "~/Documents/Trip"}) {
		t.Fatalf("folder = %+v", r)
	}
	if r := resolve("~"); r != (proto.UsageDir{Dir: "~", Project: "home", Place: "~"}) {
		t.Fatalf("home = %+v", r)
	}
	// A broken .git file is no repository.
	broken := usageMkdir(t, home, "broken")
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := resolve("~/broken"); r.Project != "folder:broken" {
		t.Fatalf("broken = %+v", r)
	}
	// A repository whose config cannot be read is a folder.
	if os.Geteuid() != 0 {
		locked := usageMkdir(t, home, "locked")
		usageGit(t, locked, "init", "-q")
		if err := os.Chmod(filepath.Join(locked, ".git", "config"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, ".git", "config"), 0o644) })
		if r := resolve("~/locked"); r.Project != "folder:locked" {
			t.Fatalf("unreadable = %+v", r)
		}
	}
	// Gone: not resolved, so the manager keeps what it was.
	if _, ok := resolveUsageDir(ctx, home, "~/gone"); ok {
		t.Fatal("a missing directory resolved")
	}

	// Dotfiles in git at home: home is that repository, a folder in it is its own.
	usageGit(t, home, "init", "-q")
	usageGit(t, home, "remote", "add", "origin", "https://github.com/me/dotfiles.git")
	if r := resolve("~"); r.Project != "git:github.com/me/dotfiles" {
		t.Fatalf("home with dotfiles = %+v", r)
	}
	if r := resolve("~/Documents/Trip"); r.Project != "folder:trip" {
		t.Fatalf("a folder under dotfiles = %+v", r)
	}
	if r := resolve("~/code/app"); r.Project != want.Project {
		t.Fatalf("a repository under dotfiles = %+v", r)
	}
}

// A temporary folder is :temp for an older manager, and named by its path for one that takes them (temp_dirs, #16827).
func TestUsageDirKey(t *testing.T) {
	home := "/home/tester" // not under the temporary folder, as a home is (t.TempDir is)
	n := &Node{home: home}
	inTemp := filepath.Join(os.TempDir(), "y")
	for cwd, want := range map[string][2]string{
		home:                               {"~", "~"},
		filepath.Join(home, "code", "app"): {"~/code/app", "~/code/app"},
		"~/code/app":                       {"~/code/app", "~/code/app"},
		"/srv/work":                        {"/srv/work", "/srv/work"},
		"/tmp/x":                           {proto.UsageDirTemp, "/tmp/x"},
		"/tmp/x/../x/sub/":                 {proto.UsageDirTemp, "/tmp/x/sub"},
		inTemp:                             {proto.UsageDirTemp, filepath.Clean(inTemp)},
		"":                                 {proto.UsageDirUnknown, proto.UsageDirUnknown},
		"relative/path":                    {proto.UsageDirUnknown, proto.UsageDirUnknown},
	} {
		for i, temps := range []bool{false, true} {
			if got := n.usageDirKey(cwd, temps); got != want[i] {
				t.Errorf("%q (temps %v) = %q, want %q", cwd, temps, got, want[i])
			}
		}
	}
	for dir, want := range map[string]bool{"/tmp/x": true, proto.UsageDirTemp: true, "~/code/app": false, "/srv/work": false,
		proto.UsageDirAssistant: false, "": false} {
		if n.usageTempKey(dir) != want {
			t.Errorf("usageTempKey(%q) = %v", dir, !want)
		}
	}
}

// A temporary folder resolves like any directory when a repository was made in it: a worktree added there, a clone,
// a repository with a first commit and no remote. In no repository, in one at the temporary folder's top, in one with
// no commit or a broken .git, and gone, it is temp: no name and no place, so the apps show one "Temporary folders"
// (#16827).
func TestResolveUsageDirTemp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	base := t.TempDir()
	home, tmp, other := usageMkdir(t, base, "home"), usageMkdir(t, base, "tmp"), usageMkdir(t, base, "var-tmp")
	usageTempRootsAre(t, tmp, other)
	resolve := func(dir string) proto.UsageDir {
		t.Helper()
		r, ok := resolveUsageDir(ctx, home, dir)
		if !ok {
			t.Fatalf("%s not resolved", dir)
		}
		return r
	}
	isTemp := func(dir string) {
		t.Helper()
		if r := resolve(dir); r != (proto.UsageDir{Dir: dir, Project: "temp"}) {
			t.Errorf("%s = %+v, want temp", dir, r)
		}
	}

	app := usageMkdir(t, home, "code", "app")
	usageGit(t, app, "init", "-q")
	usageGit(t, app, "remote", "add", "origin", "https://github.com/Acme/App.git")
	usageGit(t, app, "commit", "-q", "--allow-empty", "-m", "first")
	lane := filepath.Join(tmp, "app-lane-x")
	usageGit(t, app, "worktree", "add", "-q", lane)
	if r := resolve(lane); r.Project != "git:github.com/acme/app" || r.Name != "Acme/App" || !r.Worktree || r.Place != realPath(lane) {
		t.Fatalf("worktree in a temporary folder = %+v", r)
	}
	usageMkdir(t, lane, "web")
	if r := resolve(filepath.Join(lane, "web")); r.Project != "git:github.com/acme/app" || r.Place != realPath(lane) {
		t.Fatalf("a folder in it = %+v", r)
	}
	clone := usageMkdir(t, tmp, "clone-y")
	usageGit(t, clone, "init", "-q")
	usageGit(t, clone, "remote", "add", "origin", "git@gitlab.example.com:Team/Tool.git")
	if r := resolve(clone); r.Project != "git:gitlab.example.com/Team/Tool" || r.Worktree || r.Place != realPath(clone) {
		t.Fatalf("repository in a temporary folder = %+v", r)
	}
	local := usageMkdir(t, tmp, "local-z")
	usageGit(t, local, "init", "-q")
	usageGit(t, local, "commit", "-q", "--allow-empty", "-m", "first")
	if r := resolve(local); r.Project != "local:"+usageGit(t, local, "rev-list", "--max-parents=0", "HEAD") || r.Name != "local-z" {
		t.Fatalf("no remote = %+v", r)
	}

	isTemp(usageMkdir(t, tmp, "scratch-1"))
	fresh := usageMkdir(t, tmp, "fresh-2")
	usageGit(t, fresh, "init", "-q")
	isTemp(fresh) // no commit: only its folder's name to know it by
	broken := usageMkdir(t, tmp, "broken-3")
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	isTemp(broken)
	isTemp(filepath.Join(tmp, "gone-4"))
	usageGit(t, other, "init", "-q") // a repository at a temporary folder's top: every folder in it would be one project
	usageGit(t, other, "remote", "add", "origin", "https://github.com/acme/stray.git")
	isTemp(usageMkdir(t, other, "inside-5"))
	// Outside the temporary folders, as before: a folder is its own, a gone one is not resolved.
	if r := resolve(usageMkdir(t, base, "Trip")); r.Project != "folder:trip" {
		t.Fatalf("folder = %+v", r)
	}
	if _, ok := resolveUsageDir(ctx, home, "~/gone"); ok {
		t.Fatal("a missing directory outside the temporary folders resolved")
	}
}

// The resolver resolves each directory once, in the background; a report takes what is new, a whole window all.
func TestUsageDirsResolver(t *testing.T) {
	u := newUsageDirs("/home/me")
	calls := map[string]int{}
	resolved := make(chan string, 8)
	u.resolve = func(_ context.Context, _, dir string) (proto.UsageDir, bool) {
		calls[dir]++
		defer func() { resolved <- dir }()
		return proto.UsageDir{Dir: dir, Project: "folder:" + dir[2:]}, dir != "~/gone"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.run(ctx)
	for _, d := range []string{"~/a", "~/b", "~/a", "~/gone", proto.UsageDirAssistant, ""} {
		u.want(d)
	}
	for range 3 { // ~/a, ~/b and ~/gone, each once
		select {
		case <-resolved:
		case <-time.After(5 * time.Second):
			t.Fatal("not resolved")
		}
	}
	got := u.take(false)
	if len(got) != 2 || got[0].Dir != "~/a" || got[1].Dir != "~/b" {
		t.Fatalf("taken %+v", got)
	}
	if again := u.take(false); len(again) != 0 {
		t.Fatalf("taken again %+v", again)
	}
	if all := u.take(true); len(all) != 2 {
		t.Fatalf("all %+v", all)
	}
	cancel()
	if calls["~/a"] != 1 || calls[proto.UsageDirAssistant] != 0 {
		t.Fatalf("calls %v", calls)
	}
}

// With directories kept, one minute of one model in two directories is two buckets, and a report carries what the
// directories resolved to.
func TestUsageMeterByDir(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.jsonl", claudeReply(now, "ra", "claude-opus-5-5", 1, 0, 0, 10))
	b := write("b.jsonl", claudeReply(now, "rb", "claude-opus-5-5", 1, 0, 0, 20))
	m := newUsageMeter()
	m.dirs = newUsageDirs(dir)
	m.dirs.done = []proto.UsageDir{{Dir: "~/code/a", Project: "folder:a", Name: "a", Place: "~/code/a"}}
	m.fileDir = func(f usagePath) string {
		if f.path == a {
			return "~/code/a"
		}
		return "~/code/b"
	}
	cutoff := now.Add(-usageWindow)
	scanFile(t, m, a, cutoff)
	scanFile(t, m, b, cutoff)
	minute := now.Unix() / 60 * 60
	ka, kb := usageKey{minute, "claude-opus-5-5", "", tokenuse.Claude, "~/code/a"}, usageKey{minute, "claude-opus-5-5", "", tokenuse.Claude, "~/code/b"}
	if m.buckets[ka] == nil || m.buckets[ka].Output != 10 || m.buckets[ka].Dir != "~/code/a" || m.buckets[kb] == nil || m.buckets[kb].Output != 20 {
		t.Fatalf("buckets %+v", m.buckets)
	}
	params, ok := m.report(now)
	raw, _ := json.Marshal(params)
	if !ok || !strings.Contains(string(raw), `"dirs":[{"dir":"~/code/a","project":"folder:a"`) {
		t.Fatalf("report %s", raw)
	}
	// A meter that does not keep directories (an older manager) sends none, and its buckets name none.
	plain := newUsageMeter()
	scanFile(t, plain, a, cutoff)
	params, _ = plain.report(now)
	raw, _ = json.Marshal(params)
	if strings.Contains(string(raw), `"dir`) {
		t.Fatalf("plain report %s", raw)
	}
}

// The meter asks the manager whether it keeps directories: one that does gets each bucket's start directory (where
// the transcript's head says the session started, never where a later line cd'd); an older one, buckets as before.
func TestUsageMeterAsksTheManager(t *testing.T) {
	for _, keeps := range []bool{true, false} {
		n, reports := usageStartupNodeDirs(t, keeps)
		cfg := os.Getenv("CLAUDE_CONFIG_DIR")
		project := filepath.Join(cfg, "projects", "fixture")
		n.home = "/home/tester" // a home outside the temporary folder the fixture is in; only the transcript is read
		start := filepath.Join(n.home, "code", "shop")
		now := time.Now().UTC()
		body := `{"type":"user","cwd":"` + start + `","timestamp":"` + now.Format(time.RFC3339Nano) + `","message":{"content":"hi"}}` + "\n" +
			`{"type":"user","cwd":"/tmp/elsewhere","timestamp":"` + now.Format(time.RFC3339Nano) + `"}` + "\n" +
			claudeReply(now, "r1", "fixture-model", 1, 0, 0, 2)
		if err := os.WriteFile(filepath.Join(project, "fixture-session.jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ready := make(chan struct{})
		close(ready)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); n.meterUsage(ctx, ready) }()
		select {
		case p := <-reports:
			var report struct {
				Params struct {
					Buckets []proto.UsageBucket `json:"buckets"`
				} `json:"params"`
			}
			if err := json.Unmarshal(p, &report); err != nil || len(report.Params.Buckets) == 0 {
				t.Fatalf("report %s, %v", p, err)
			}
			for _, b := range report.Params.Buckets {
				if want := map[bool]string{true: "~/code/shop", false: ""}[keeps]; b.Dir != want {
					t.Errorf("keeps=%v: bucket %+v, want dir %q", keeps, b, want)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("keeps=%v: no report", keeps)
		}
		if keeps != (n.usageDirs.get() != nil) {
			t.Errorf("keeps=%v: resolver %v", keeps, n.usageDirs.get())
		}
		cancel()
		<-done
		if n.usageDirs.get() != nil {
			t.Error("the resolver outlived its connection")
		}
	}
}

// A manager that takes temporary folders (temp_dirs) gets a session that started in one by its path, which the node
// resolves in the background (TestResolveUsageDirTemp); an older one gets :temp, as before (#16827).
func TestUsageMeterTempDirs(t *testing.T) {
	for _, temps := range []bool{true, false} {
		n, reports := usageStartupNodeFeatures(t, true, temps)
		project := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "fixture")
		n.home = "/home/tester"
		lane := filepath.Join(t.TempDir(), "app-lane-x") // in the system's temporary folder
		now := time.Now().UTC()
		body := `{"type":"user","cwd":"` + lane + `","timestamp":"` + now.Format(time.RFC3339Nano) + `","message":{"content":"hi"}}` + "\n" +
			claudeReply(now, "r1", "fixture-model", 1, 0, 0, 2)
		if err := os.WriteFile(filepath.Join(project, "fixture-session.jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ready := make(chan struct{})
		close(ready)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); n.meterUsage(ctx, ready) }()
		want := map[bool]string{true: filepath.Clean(lane), false: proto.UsageDirTemp}[temps]
		select {
		case p := <-reports:
			var report struct {
				Params struct {
					Buckets []proto.UsageBucket `json:"buckets"`
				} `json:"params"`
			}
			if err := json.Unmarshal(p, &report); err != nil || len(report.Params.Buckets) == 0 {
				t.Fatalf("report %s, %v", p, err)
			}
			for _, b := range report.Params.Buckets {
				if b.Dir != want {
					t.Errorf("temps=%v: bucket %+v, want dir %q", temps, b, want)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("temps=%v: no report", temps)
		}
		dirs := n.usageDirs.get()
		dirs.mu.Lock()
		asked := dirs.asked[want]
		dirs.mu.Unlock()
		if dirs.temps != temps || asked != temps { // a token is never resolved
			t.Errorf("temps=%v: resolver temps %v, asked to resolve %v", temps, dirs.temps, asked)
		}
		cancel()
		<-done
	}
}

// A backfill request carries what its buckets' directories resolve to, each resolved once a run; tokens and
// directories that are gone carry nothing.
func TestBackfillDirs(t *testing.T) {
	usageTempRootsAre(t)
	home := t.TempDir()
	usageMkdir(t, home, "Notes")
	n := &Node{home: home}
	resolved := map[string]*proto.UsageDir{}
	buckets := []proto.UsageBucket{{Dir: "~/Notes"}, {Dir: "~/Notes"}, {Dir: "~/gone"}, {Dir: proto.UsageDirAssistant}, {}}
	got := n.backfillDirs(context.Background(), buckets, resolved)
	if len(got) != 1 || got[0].Project != "folder:notes" || len(resolved) != 2 || resolved["~/gone"] != nil {
		t.Fatalf("dirs = %+v, resolved = %v", got, resolved)
	}
	if err := os.RemoveAll(filepath.Join(home, "Notes")); err != nil {
		t.Fatal(err)
	}
	if again := n.backfillDirs(context.Background(), buckets[:1], resolved); len(again) != 1 || again[0].Name != "Notes" {
		t.Fatalf("not resolved again: %+v", again)
	}
}
