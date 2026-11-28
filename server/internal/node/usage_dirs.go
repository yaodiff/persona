package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// Token usage by project (docs/server/machines.md "Token usage by project", #16530). Each bucket names where its
// session started (proto.UsageBucket.Dir): the start directory written from home, or a token. The node resolves each
// directory once, in the background (usageDirs), to the project the manager sums by across machines: a git remote's
// host and path (credentials, user and port dropped), a repository's root commit when it has no remote, a folder's
// name when it is in no repository, "temp" for a temporary folder in none (#16827: one made in it, a worktree added
// in /tmp, is its repository's). Reading only files (.git, its config) and running git only for an aliased remote
// or a root commit, with a time limit; a directory that cannot be read is a folder, and one that is gone is not
// resolved at all (the manager keeps its last resolution). The meter never waits for it.

const (
	// usageDirConfigMost is the most of a git config file read for its remotes.
	usageDirConfigMost = 256 << 10
	// usageDirHeadMost is the most of a transcript's head read for where its session started.
	usageDirHeadMost = 256 << 10
	// usageDirGitWait bounds each git command a resolution runs (an aliased remote, a root commit in a long history).
	usageDirGitWait = 10 * time.Second
)

// usageForgeHosts are the forges whose paths are case-insensitive: their paths are keyed in lower case.
var usageForgeHosts = []string{"github.com", "gitlab.com", "bitbucket.org", "codeberg.org", "gitee.com", "dev.azure.com"}

// usageHostAliases are other names of a forge's host (its SSH-over-443 and www hosts).
var usageHostAliases = map[string]string{"ssh.github.com": "github.com", "www.github.com": "github.com",
	"altssh.gitlab.com": "gitlab.com", "altssh.bitbucket.org": "bitbucket.org", "ssh.dev.azure.com": "dev.azure.com"}

// usageProjectTemp is the project of a temporary folder that is in no repository made inside it (proto.UsageDir):
// the one "Temporary folders" the apps show.
const usageProjectTemp = "temp"

// usageDirKey is how a bucket names the directory cwd: written from home ("~", "~/code/app"), as it is outside home,
// UsageDirAssistant for the assistant's own conversations, UsageDirUnknown when there is none. A temporary folder is
// named by its path too when the manager takes them (temps, usage.features' temp_dirs, #16827), and resolved like any
// other directory (resolveUsageDir: a worktree made there is its repository's); for an older manager it is UsageDirTemp.
func (n *Node) usageDirKey(cwd string, temps bool) string {
	if cwd == "~" || strings.HasPrefix(cwd, "~/") {
		cwd = n.home + strings.TrimPrefix(cwd, "~")
	}
	if cwd == "" || !filepath.IsAbs(cwd) {
		return proto.UsageDirUnknown
	}
	cwd = filepath.Clean(cwd)
	switch {
	case identity.IsAssistantDir(cwd):
		return proto.UsageDirAssistant
	case !temps && usageTempDir(cwd):
		return proto.UsageDirTemp
	}
	return homeRelative(cwd, n.home)
}

// usageTempKey: a bucket's directory (usageDirKey's) is a temporary folder, or the token for one.
func (n *Node) usageTempKey(dir string) bool {
	if dir == proto.UsageDirTemp {
		return true
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		dir = n.home + strings.TrimPrefix(dir, "~")
	}
	return filepath.IsAbs(dir) && usageTempDir(filepath.Clean(dir))
}

// homeRelative is path written from home ("~" for home itself), through either spelling of home (as written, and
// with its links resolved); as it is outside home.
func homeRelative(path, home string) string {
	if home == "" {
		return path
	}
	for _, h := range []string{filepath.Clean(home), realPath(filepath.Clean(home))} {
		if path == h {
			return "~"
		}
		if rest, ok := strings.CutPrefix(path, h+string(filepath.Separator)); ok {
			return "~/" + filepath.ToSlash(rest)
		}
	}
	return path
}

// usageTempRoots are the temporary folders (the system's temp directory, /tmp, /var/tmp, macOS's /var/folders), each
// as written and with its links resolved. A variable for the tests, whose own folders are in one.
var usageTempRoots = func() []string {
	tmp := filepath.Clean(os.TempDir())
	return []string{tmp, realPath(tmp), "/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp", "/var/folders", "/private/var/folders"}
}

// usageTempRoot is the temporary folder path is in (the longest of usageTempRoots that holds it or is it); "" when it
// is in none.
func usageTempRoot(path string) string {
	best := ""
	for _, root := range usageTempRoots() {
		if (path == root || strings.HasPrefix(path, root+string(filepath.Separator))) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

// usageTempDir: path is in a temporary folder.
func usageTempDir(path string) bool {
	return usageTempRoot(path) != ""
}

// headCwd is the first directory a transcript's head names ("cwd": Claude Code's lines, a Codex rollout's
// session_meta, a pi or omp session's header): where its session started. Only the head is read (usageDirHeadMost):
// the key comes before a line's message in all of them, so a first prompt with a picture in it does not hide it.
func headCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// Usually in the first few kilobytes (a Claude Code transcript's first lines are short records, a rollout's
	// session_meta names it early): read more only while it has not turned up.
	for size := 16 << 10; ; size *= 4 {
		buf := make([]byte, min(size, usageDirHeadMost))
		nr, _ := f.ReadAt(buf, 0)
		if cwd := firstCwd(buf[:nr]); cwd != "" || nr < len(buf) || len(buf) >= usageDirHeadMost {
			return cwd
		}
	}
}

// firstCwd is the value of the first `"cwd":"…"` in b; "" when there is none (or it is cut off). A "cwd" inside a
// message is escaped there (\"cwd\"), so it does not match.
func firstCwd(b []byte) string {
	key := []byte(`"cwd":"`)
	i := bytes.Index(b, key)
	if i < 0 {
		return ""
	}
	start := i + len(key) - 1 // the opening quote
	for j := start + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			var s string
			if err := json.Unmarshal(b[start:j+1], &s); err != nil {
				return ""
			}
			return s
		case '\n':
			return ""
		}
	}
	return ""
}

// usageRemote is a remote URL's host and repository path as the project key takes them: the SSH (scp-like or
// ssh://), HTTPS and git:// spellings of one repository give the same, the user, password or token and the port are
// dropped, the host is lower case (a forge's other host names folded into its own), and the path has no ".git",
// leading or trailing "/" or empty segment; a forge's path is also given in lower case (key). ok false for a local
// path, a URL with a query or fragment, or anything else that is not a network remote.
func usageRemote(remote string) (host, path, key string, ok bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" || strings.ContainsAny(remote, " \t\r\n\x00") {
		return "", "", "", false
	}
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return "", "", "", false
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return "", "", "", false
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-like: [user@]host:path. A local path has a "/" before any ":" (or starts with one, ".", "~").
		at := strings.IndexByte(remote, ':')
		if at <= 0 || strings.ContainsAny(remote[:at], "/\\") || strings.HasPrefix(remote, ".") || strings.HasPrefix(remote, "~") {
			return "", "", "", false
		}
		host, path = remote[:at], remote[at+1:]
		if i := strings.LastIndexByte(host, '@'); i >= 0 {
			host = host[i+1:]
		}
		if len(host) == 1 { // a drive letter (C:/repos/app): a local path, as git reads it
			return "", "", "", false
		}
		if strings.HasPrefix(path, "//") { // host::path, a transport helper
			return "", "", "", false
		}
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if alias, ok := usageHostAliases[host]; ok {
		host = alias
	}
	var segs []string
	for _, s := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if n := len(segs); n > 0 {
		segs[n-1] = strings.TrimSuffix(segs[n-1], ".git")
	}
	if host == "dev.azure.com" && len(segs) == 4 && segs[0] == "v3" { // SSH: v3/org/project/repo
		segs = []string{segs[1], segs[2], "_git", segs[3]}
	}
	if !usageHost(host) || len(segs) == 0 {
		return "", "", "", false
	}
	for _, s := range segs {
		if !usageSegment(s) {
			return "", "", "", false
		}
	}
	path = strings.Join(segs, "/")
	key = "git:" + host + "/" + path
	if slices.Contains(usageForgeHosts, host) {
		key = "git:" + host + "/" + strings.ToLower(path)
	}
	return host, path, key, true
}

// usageHost: a host name of letters, digits, dots and hyphens (no user, port or address in brackets).
func usageHost(h string) bool {
	if h == "" || h[0] == '.' || h[0] == '-' || h[len(h)-1] == '-' {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// usageSegment: a repository path segment of the characters forges allow, never "." or "..".
func usageSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._~+-", c)) {
			return false
		}
	}
	return true
}

// gitConfigOf is the config file of the repository whose working tree is worktree: its .git directory's, or the
// git dir its .git file points at (a linked worktree's shared one, a submodule's own). "" when it cannot be told.
func gitConfigOf(worktree string) string {
	dotGit := filepath.Join(worktree, ".git")
	st, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if st.IsDir() {
		return filepath.Join(dotGit, "config")
	}
	gitDir := pointedGitDir(dotGit, worktree)
	if gitDir == "" {
		return ""
	}
	if common := commonDir(gitDir); common != "" {
		gitDir = common
	}
	return filepath.Join(gitDir, "config")
}

// configRemote is the remote a repository is known by, from its config file: origin, else upstream, else the first
// one it names; name is the remote's and url its first url, "" without one. The file is read up to
// usageDirConfigMost; read false when it cannot be read at all (no permission, a broken repository).
func configRemote(config string) (name, rawURL string, read bool) {
	f, err := os.Open(config)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	urls, order := map[string]string{}, []string{}
	section := ""
	sc := bufio.NewScanner(io.LimitReader(f, usageDirConfigMost))
	sc.Buffer(make([]byte, 0, 4096), usageDirConfigMost)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = ""
			head := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if kind, sub, ok := strings.Cut(head, " "); ok && strings.EqualFold(kind, "remote") {
				section = strings.Trim(strings.TrimSpace(sub), `"`)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if section == "" || !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			value, _, _ = strings.Cut(value[1:], `"`)
		} else if fields := strings.Fields(value); len(fields) > 0 {
			value = fields[0]
		}
		if _, seen := urls[section]; !seen && value != "" {
			urls[section] = value
			order = append(order, section)
		}
	}
	for _, want := range []string{"origin", "upstream"} {
		if u, ok := urls[want]; ok {
			return want, u, true
		}
	}
	if len(order) > 0 {
		return order[0], urls[order[0]], true
	}
	return "", "", true
}

// rootCommit is the first commit of worktree's history (the smallest one, when histories were merged), or "" when it
// has none, git cannot tell in usageDirGitWait, or what it says is not a commit id.
func rootCommit(ctx context.Context, worktree string) string {
	out, err := gitTimed(ctx, worktree, usageDirGitWait, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return ""
	}
	var best string
	for _, line := range strings.Fields(out) {
		if (len(line) == 40 || len(line) == 64) && strings.Trim(line, "0123456789abcdef") == "" && (best == "" || line < best) {
			best = line
		}
	}
	return best
}

// resolveUsageDir resolves the directory a bucket names (usageDirKey's; never a token) on this machine: ok false
// when it is not there (removed since: the manager keeps what it resolved to before).
//
// A temporary folder (#16827) is the project of a repository made inside it (a worktree added there, a clone), as
// any directory is, and otherwise usageProjectTemp: in no repository, in one at the temporary folder's top or above
// it (which would make every temporary folder one project), in one with no commit or whose .git cannot be read (only
// its folder's name, random in such folders, to know it by), or gone. The manager keeps a repository it resolved a
// directory to before over usageProjectTemp, so a worktree removed since keeps its project.
func resolveUsageDir(ctx context.Context, home, dir string) (proto.UsageDir, bool) {
	abs := dir
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		abs = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	if !filepath.IsAbs(abs) {
		return proto.UsageDir{}, false
	}
	temp := usageTempDir(filepath.Clean(abs))
	tempDir := proto.UsageDir{Dir: dir, Project: usageProjectTemp}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		if temp {
			return tempDir, true
		}
		return proto.UsageDir{}, false
	}
	physical, physHome := realPath(filepath.Clean(abs)), realPath(filepath.Clean(home))
	out := proto.UsageDir{Dir: dir}
	p := readGitPlace(physical)
	if temp {
		if root := usageTempRoot(physical); root != "" && !strings.HasPrefix(p.worktree, root+string(filepath.Separator)) {
			return tempDir, true
		}
	}
	// A repository at home or above it (dotfiles kept in git) is the project of the home folder only: a folder in it
	// is its own (a trip's notes are not the dotfiles). A repository whose config cannot be read is a folder too.
	aboveHome := p.worktree != "" && (p.worktree == physHome || strings.HasPrefix(physHome, p.worktree+string(filepath.Separator)))
	var name, raw string
	read := false
	if p.worktree != "" && (!aboveHome || physical == p.worktree) {
		name, raw, read = configRemote(gitConfigOf(realPath(p.worktree)))
	}
	if read {
		worktree, root := realPath(p.worktree), realPath(p.root)
		out.Place, out.Worktree = homeRelative(worktree, home), worktree != root
		host, path, key, ok := usageRemote(raw)
		if (!ok || !strings.Contains(host, ".")) && raw != "" && strings.ContainsRune(raw, ':') && !filepath.IsAbs(raw) &&
			!strings.HasPrefix(raw, "file:") {
			// A shorthand (url.<base>.insteadOf, "gh:owner/repo": no host has no dot): what git makes of it. Any other
			// remote stays as it is written, the way it was cloned, so a rewrite to a mirror does not change its key.
			if got, err := gitTimed(ctx, worktree, usageDirGitWait, "remote", "get-url", name); err == nil {
				if h, p, k, good := usageRemote(strings.TrimSpace(got)); good && strings.Contains(h, ".") {
					host, path, key, ok = h, p, k, true
				}
			}
		}
		if ok {
			out.Project, out.Name, out.Host = key, path, host
			return out, true
		}
		out.Name = strings.TrimSuffix(filepath.Base(root), ".git")
		if commit := rootCommit(ctx, worktree); commit != "" {
			out.Project = "local:" + commit
		} else if temp {
			return tempDir, true
		} else {
			out.Project = "local:dir:" + strings.ToLower(out.Name)
		}
		if out.Name == "" || out.Name == "." || out.Name == string(filepath.Separator) {
			return proto.UsageDir{}, false
		}
		return out, true
	}
	if temp {
		return tempDir, true
	}
	if physical == physHome {
		out.Project, out.Place = "home", "~"
		return out, true
	}
	folder := filepath.Base(filepath.Clean(abs))
	if folder == "" || folder == string(filepath.Separator) || folder == "." {
		return proto.UsageDir{}, false
	}
	out.Project, out.Name, out.Place = "folder:"+strings.ToLower(folder), folder, dir
	return out, true
}

// usageDirs resolves the directories the meter names, one at a time in the background, and keeps what it resolved
// for the reports: each once per connection (the manager keeps them across connections).
type usageDirs struct {
	home string
	// temps: the manager takes temporary folders by their paths (usage.features' temp_dirs), not as UsageDirTemp.
	temps   bool
	resolve func(ctx context.Context, home, dir string) (proto.UsageDir, bool)
	mu      sync.Mutex
	asked   map[string]bool // every directory asked for: resolved, waiting, or not there
	waiting []string
	done    []proto.UsageDir // resolved, in order
	sent    int              // done[:sent] went out
	wake    chan struct{}
}

func newUsageDirs(home string) *usageDirs {
	return &usageDirs{home: home, resolve: resolveUsageDir, asked: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// want asks for dir to be resolved, once; a token needs nothing.
func (u *usageDirs) want(dir string) {
	if u == nil || dir == "" || strings.HasPrefix(dir, ":") {
		return
	}
	u.mu.Lock()
	if !u.asked[dir] {
		u.asked[dir] = true
		u.waiting = append(u.waiting, dir)
		select {
		case u.wake <- struct{}{}:
		default:
		}
	}
	u.mu.Unlock()
}

// run resolves what is asked for until ctx ends.
func (u *usageDirs) run(ctx context.Context) {
	for {
		u.mu.Lock()
		var dir string
		if len(u.waiting) > 0 {
			dir, u.waiting = u.waiting[0], u.waiting[1:]
		}
		u.mu.Unlock()
		if dir == "" {
			select {
			case <-ctx.Done():
				return
			case <-u.wake:
			}
			continue
		}
		if r, ok := u.resolve(ctx, u.home, dir); ok {
			u.mu.Lock()
			u.done = append(u.done, r)
			u.mu.Unlock()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// take is what was resolved since the last take; with all, everything resolved so far (a whole window).
func (u *usageDirs) take(all bool) []proto.UsageDir {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	from := u.sent
	if all {
		from = 0
	}
	u.sent = len(u.done)
	return slices.Clone(u.done[from:])
}

// usageDirsOn is the connection's resolver while its manager keeps directories (usage.features); nil otherwise.
type usageDirsOn struct{ p atomic.Pointer[usageDirs] }

func (o *usageDirsOn) get() *usageDirs { return o.p.Load() }

// usageKeepsDirs asks the manager whether it keeps the usage's directories (usage.features, #16530): an older one
// answers unknown method (the hub's), and its buckets stay summed as before, since there buckets that differ only by
// directory would replace each other. temps: it also takes a temporary folder by its path (temp_dirs, #16827); one
// that does not gets UsageDirTemp for them, as before.
func (n *Node) usageKeepsDirs(ctx context.Context) (keeps, temps bool) {
	if n.peer == nil {
		return false, false
	}
	var out struct {
		Dirs     bool `json:"dirs"`
		TempDirs bool `json:"temp_dirs"`
	}
	cctx, cancel := context.WithTimeout(ctx, backfillCall)
	defer cancel()
	if n.peer.Call(cctx, "usage.features", map[string]any{}, &out) != nil || !out.Dirs {
		return false, false
	}
	return true, out.TempDirs
}

// meterDirs has m name each file's and each database row's start directory, asking dirs to resolve it.
func (n *Node) meterDirs(m *usageMeter, dirs *usageDirs) {
	m.dirs = dirs
	fileDir := n.usageFileDirs(dirs.temps)
	m.fileDir = func(f usagePath) string {
		dir := fileDir(f)
		dirs.want(dir)
		return dir
	}
	m.recordDir = func(q usageQuery, session string) string {
		dir := n.usageSessionDir(session, q.dir(session), dirs.temps)
		dirs.want(dir)
		return dir
	}
}

// newFile is newUsageFile with the start directory of the file's session, when m keeps directories.
func (m *usageMeter) newFile(f usagePath, off int64) *usageFile {
	uf := newUsageFile(f, off)
	if m.fileDir != nil {
		uf.dir = m.fileDir(f)
	}
	return uf
}

// dirOfRecord is where a database row's session started, when m keeps directories.
func (m *usageMeter) dirOfRecord(q usageQuery, r tokenuse.Record) string {
	if m.recordDir == nil {
		return ""
	}
	return m.recordDir(q, r.Session)
}

// usageFileDirs names where the session a usage file belongs to started, as a bucket names it: the directory Persona
// started it in (its state), else the first directory its transcript names (the adapter's startDir), each session's
// read once (a session's subagent files all take its own transcript's). Never the latest one: Claude Code's lines
// follow the model's cd (docs/lessons/transcript.md). temps as usageDirKey's.
func (n *Node) usageFileDirs(temps bool) func(usagePath) string {
	sessions := map[string]string{}
	return func(f usagePath) string {
		session := f.ad.session(f.path)
		key := f.ad.agent() + "\x00" + session
		if dir, ok := sessions[key]; ok && session != "" {
			return dir
		}
		dir := n.usageSessionDir(session, f.ad.startDir(f.path), temps)
		sessions[key] = dir
		return dir
	}
}

// usageSessionDir is where session started: the directory Persona started it in, else recorded (what its own record
// says), as a bucket names it (temps as usageDirKey's).
func (n *Node) usageSessionDir(session, recorded string, temps bool) string {
	if session != "" {
		if st, ok := n.state.get(session); ok && st.Cwd != "" {
			return n.usageDirKey(st.Cwd, temps)
		}
	}
	return n.usageDirKey(recorded, temps)
}
