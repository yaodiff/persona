package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// Claude Code keeps everything (projects/<cwd>/<session>.jsonl, sessions/<pid>.json, settings.json) under its config
// directory: $CLAUDE_CONFIG_DIR, else ~/.claude. Launchers for several accounts or profiles set CLAUDE_CONFIG_DIR, so one
// machine can have many. The node looks in all of them:
//   - the node's own default ($CLAUDE_CONFIG_DIR of the node, else ~/.claude);
//   - the machine's Agent directory, private assistant profiles, and explicitly selected assistant directories;
//   - every running claude process's CLAUDE_CONFIG_DIR (scanned every 30 s; also tells which directory a pid uses);
//   - the hub's list: hub.json machines[].claude_config_dirs plus directories scanned earlier (node.configure), so a
//     profile's past sessions stay listed when none of its claudes runs.
// Directories are compared by their real path: ~/.claude/projects symlinked into a profile is the same directory.
// Directories the user blocked for this machine (node.configure claude_dirs_blocked, proto.ClaudeDirBlocked) are left
// out of all of it: not looked in, not reported, their sessions not listed. The node's default directory is never
// blocked. Launch selection is separate from discovery below.

// claudeScanEvery is how often running claude processes are scanned for their config directory.
const claudeScanEvery = 30 * time.Second

type claudeDirs struct {
	mu        sync.Mutex
	known     []string           // from the hub: configured and remembered
	blocked   []string           // from the hub: patterns of directories kept out (proto.ClaudeDirBlocked)
	found     []string           // seen in running claude processes, in order of discovery
	byPID     map[int]string     // live claude pid -> its config directory
	bySession map[string]procEnv // session a live claude runs (its sessions/<pid>.json) -> that claude's environment
	scanned   time.Time
	report    func(dirs []string) // tells the hub about newly found directories
	scan      func() map[int]procEnv

	// runs: session a live claude runs -> that claude, when the scan read its command line (offlauncher.go).
	runs map[string]claudeRun
}

// procEnv is what the scan keeps from a running claude's environment: its config directory, and the API endpoint
// and model a launcher function exported (used to tell which launcher started a claude Persona did not start).
type procEnv struct {
	ConfigDir       string // CLAUDE_CONFIG_DIR
	BaseURL         string // ANTHROPIC_BASE_URL
	Model           string // ANTHROPIC_MODEL
	FastUnavailable bool   // disabled by the environment or by a provider without fast mode
	Vertex          bool
	// From its command line, not its environment (offlauncher.go): Cmd says it was kept (not on Windows, scanClaudeProcesses), MCPConfig
	// is then each --mcp-config value it was started with, NUL-separated ("" for none).
	Cmd       bool
	MCPConfig string
}

func newClaudeDirs() *claudeDirs {
	return &claudeDirs{byPID: map[int]string{}, bySession: map[string]procEnv{}, runs: map[string]claudeRun{}, scan: scanClaudeProcesses}
}

// defaultClaudeDir is the inherited legacy default, also kept for old-session discovery.
func (n *Node) defaultClaudeDir() string {
	if n.sessionScope != nil {
		return n.sessionScope.claude
	}
	return claudeDirOf(n.home)
}

// claudeDirOf is the node's own default Claude Code directory for home: $CLAUDE_CONFIG_DIR, else ~/.claude.
func claudeDirOf(home string) string { return nodeclient.ClaudeDir(home) }

// filepath.Join cleans parent components before following symlinks. Keep those components during expansion,
// then use the resolved directory for child lookups. Ordinary symlink aliases keep their configured spelling.
func claudeDirHasParent(dir string) bool {
	if !strings.Contains(dir, "..") {
		return false
	}
	for component := range strings.SplitSeq(filepath.ToSlash(dir), "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

func (n *Node) expandClaudeDir(dir string) string {
	if n.home != "" && len(dir) > 1 && dir[0] == '~' && os.IsPathSeparator(dir[1]) && claudeDirHasParent(dir) {
		return strings.TrimSuffix(n.home, string(filepath.Separator)) + dir[1:]
	}
	return proto.ExpandHome(dir, n.home)
}

// claudeConfigDirs also supplies the configured default when a caller needs a path before any directory exists.
func (n *Node) claudeConfigDirs() []string {
	if dirs := n.existingClaudeDirs(); len(dirs) > 0 {
		return dirs
	}
	return []string{n.defaultClaudeDir()}
}

func (n *Node) existingClaudeDirs() []string {
	return n.discoverClaudeDirs(filepath.EvalSymlinks)
}

func (n *Node) discoverClaudeDirs(resolve func(string) (string, error)) []string {
	if n.sessionScope != nil {
		return []string{n.sessionScope.claude}
	}
	n.refreshClaudeDirs()
	var candidates []string
	spellings := make(map[string]bool)
	add := func(dir string) {
		// Keep the spelling passed to EvalSymlinks: cleaning link/../dir before following link can name a different directory.
		if spellings[dir] {
			return
		}
		expanded := n.expandClaudeDir(dir)
		if !spellings[expanded] {
			candidates = append(candidates, expanded)
			spellings[expanded] = true
		}
		spellings[dir] = true
	}
	def := n.defaultClaudeDir()
	add(def)
	profiles, _ := filepath.Glob(filepath.Join(n.dir, "profiles", "*"))
	for _, profile := range profiles {
		add(profile)
	}
	add(filepath.Join(n.home, ".claude"))
	if dir := n.claudeStartDir.Load(); dir != nil && *dir != "" {
		add(*dir)
	}
	if n.state != nil {
		n.state.mu.Lock()
		for id := range n.state.Sessions {
			if dir := n.state.Sessions[id].ConfigDir; dir != "" && dir != "default" {
				add(dir)
			}
		}
		n.state.mu.Unlock()
	}
	var blocked []string
	if d := n.cdirs; d != nil {
		d.mu.Lock()
		for _, dir := range d.found {
			add(dir)
		}
		for _, dir := range d.known {
			add(dir)
		}
		blocked = d.blocked
		d.mu.Unlock()
	}

	var out []string
	var defaultReal string
	seen := make(map[string]bool)
	for i, dir := range candidates {
		real, err := resolve(dir)
		if err != nil || seen[real] {
			continue
		}
		if i == 0 {
			defaultReal = real
		} else if n.blockedBy(dir, real, candidates[0], defaultReal, blocked) {
			continue
		}
		seen[real] = true
		if claudeDirHasParent(dir) {
			dir = real
		}
		out = append(out, dir)
	}
	return out
}

// projectDirs: the projects directory of each config directory. Two config directories may share one through a
// symlink; that one is listed once.
func (n *Node) projectDirs() []string {
	var out, seen []string
	for _, d := range n.existingClaudeDirs() {
		p := filepath.Join(d, "projects")
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if !slices.Contains(seen, real) {
			seen = append(seen, real)
			out = append(out, p)
		}
	}
	return out
}

// globProjects globs pattern (relative to a projects directory) in every projects directory.
func (n *Node) globProjects(pattern ...string) []string {
	n.projectGlobs.Add(1)
	var out []string
	for _, p := range n.projectDirs() {
		if n.sessionScope != nil && !sessionScopeWithin(n.sessionScope.claude, p) {
			continue
		}
		m, _ := filepath.Glob(filepath.Join(append([]string{p}, pattern...)...))
		for _, path := range m {
			if n.scopedTranscript(path) {
				out = append(out, path)
			}
		}
	}
	return out
}

// claudeSessionFile is sessions/<pid>.json of that claude: in the directory its environment names when known, else
// the newest one among the config directories (a stale file of a reused pid may sit in another).
func (n *Node) claudeSessionFile(pid int) string {
	name := filepath.Join("sessions", strconv.Itoa(pid)+".json")
	if d := n.cdirs; d != nil {
		n.refreshClaudeDirs()
		d.mu.Lock()
		dir, ok := d.byPID[pid]
		d.mu.Unlock()
		if ok {
			return n.claudeSessionFileIn(dir, name)
		}
	}
	var best string
	var bestT time.Time
	for _, dir := range n.existingClaudeDirs() {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.ModTime().After(bestT) {
			best, bestT = p, st.ModTime()
		}
	}
	if best == "" {
		return n.claudeSessionFileIn(n.defaultClaudeDir(), name)
	}
	return best
}

func (n *Node) claudeSessionFileIn(dir, name string) string {
	dir = n.expandClaudeDir(dir)
	if claudeDirHasParent(dir) {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return ""
		}
		dir = real
	}
	return filepath.Join(dir, name)
}

// primaryClaudeDir is the config directory most running claudes use (the default when none runs): the one whose
// settings the machine settings page shows.
func (n *Node) primaryClaudeDir() string {
	dirs := n.claudeConfigDirs()
	d := n.cdirs
	if d == nil {
		return dirs[0]
	}
	count := map[string]int{}
	d.mu.Lock()
	for _, dir := range d.byPID {
		if real, err := filepath.EvalSymlinks(n.expandClaudeDir(dir)); err == nil {
			count[real]++
		}
	}
	d.mu.Unlock()
	best, bestN := dirs[0], -1
	for _, dir := range dirs {
		real, _ := filepath.EvalSymlinks(dir)
		if count[real] > bestN {
			best, bestN = dir, count[real]
		}
	}
	return best
}

// claudeDirBlocked: dir (or its real path) matches a blocked pattern. The default directory never does.
func (n *Node) claudeDirBlocked(dir, real string) bool {
	d := n.cdirs
	if d == nil {
		return false
	}
	d.mu.Lock()
	blocked := d.blocked
	d.mu.Unlock()
	def := n.expandClaudeDir(n.defaultClaudeDir())
	var defReal string
	if len(blocked) != 0 && real != "" {
		defReal, _ = filepath.EvalSymlinks(def)
	}
	return n.blockedBy(dir, real, def, defReal, blocked)
}

// cwdBlocked: true if a session's working directory matches any of the machine's blocked patterns.
func (n *Node) cwdBlocked(cwd string) bool {
	if cwd == "" {
		return false
	}
	if n.agentDirBlocked(cwd) { // a blocked Agent folder (agentdirs.go)
		return true
	}
	return n.claudeDirBlocks(cwd)
}

// claudeDirBlocks: cwd is under a blocked Claude config folder (the claude_dirs_blocked part of cwdBlocked).
func (n *Node) claudeDirBlocks(cwd string) bool {
	d := n.cdirs
	if d == nil {
		return false
	}
	d.mu.Lock()
	blocked := d.blocked
	d.mu.Unlock()
	return n.dirMatches(cwd, blocked)
}

func (n *Node) blockedBy(dir, real, def, defReal string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	dir = n.expandClaudeDir(dir)
	if dir == def || (real != "" && real == defReal) {
		return false
	}
	if !claudeDirHasParent(dir) && !claudeDirHasParent(def) && filepath.Clean(dir) == filepath.Clean(def) {
		return false
	}
	// Either name of a linked home (a resolved spelling names it by its real path), as dirMatches compares.
	homeReal := realHome(n.home)
	return proto.DirPatternMatchedOn(dir, n.home, homeReal, patterns) != "" ||
		(real != "" && proto.DirPatternMatchedOn(real, n.home, homeReal, patterns) != "")
}

// reportableDirs: the found directories the hub may remember, those patterns do not block.
func (n *Node) reportableDirs(found, patterns []string) []string {
	var out []string
	def := n.expandClaudeDir(n.defaultClaudeDir())
	var defReal string
	if len(patterns) > 0 && len(found) > 0 {
		defReal, _ = filepath.EvalSymlinks(def)
	}
	for _, dir := range found {
		real, _ := filepath.EvalSymlinks(n.expandClaudeDir(dir))
		if !n.blockedBy(dir, real, def, defReal, patterns) {
			out = append(out, dir)
		}
	}
	return out
}

// setBlockedClaudeDirs takes the hub's blocked patterns (node.configure). The session list is rebuilt on its next
// read; directories found while blocked are reported once unblocked, so the hub remembers them again.
func (n *Node) setBlockedClaudeDirs(patterns []string) {
	d := n.cdirs
	if d == nil {
		return
	}
	patterns = proto.CleanClaudeDirPatterns(patterns)
	d.mu.Lock()
	before := d.blocked
	d.blocked = patterns
	found, report := slices.Clone(d.found), d.report
	d.mu.Unlock()
	if slices.Equal(before, patterns) {
		return
	}
	offlineCache.Lock()
	offlineCache.at = time.Time{}
	offlineCache.Unlock()
	if report == nil {
		return
	}
	if now := n.reportableDirs(found, patterns); len(now) > len(n.reportableDirs(found, before)) {
		report(now)
	}
}

// claudeScanGOOS is the platform refreshClaudeDirs reads running claudes' directories on (tests set it).
var claudeScanGOOS = runtime.GOOS

// scannedConfigDir is a running claude's CLAUDE_CONFIG_DIR as the node names its directories. On Windows the
// assistant's claude gets it with forward slashes (scriptPath, #5487) and every other start with backslashes: cleaned
// to backslashes, one profile is listed once. filepath.Clean does that on Windows; the replacement makes it so where
// tests run.
func scannedConfigDir(goos, dir string) string {
	if goos != "windows" {
		return dir
	}
	dir = strings.ReplaceAll(dir, "/", `\`)
	if claudeDirHasParent(dir) {
		return dir
	}
	return filepath.Clean(dir)
}

// setKnownClaudeDirs takes the hub's list (node.configure).
func (n *Node) setKnownClaudeDirs(dirs []string) {
	if n.cdirs == nil {
		return
	}
	n.cdirs.mu.Lock()
	n.cdirs.known = dirs
	n.cdirs.mu.Unlock()
}

// refreshClaudeDirs rescans running claudes when the last scan is old; new directories are reported to the hub.
func (n *Node) refreshClaudeDirs() {
	if n.sessionScope != nil {
		return
	}
	d := n.cdirs
	if d == nil {
		return
	}
	d.mu.Lock()
	if time.Since(d.scanned) < claudeScanEvery {
		d.mu.Unlock()
		return
	}
	d.scanned = time.Now()
	scan := d.scan
	d.mu.Unlock()
	envs := scan()
	byPID := map[int]string{}
	for pid, e := range envs {
		if e.ConfigDir != "" {
			byPID[pid] = scannedConfigDir(claudeScanGOOS, e.ConfigDir)
		}
	}
	d.mu.Lock()
	d.byPID = byPID
	d.mu.Unlock()
	// Which session each claude runs: its sessions/<pid>.json (found through the directory just scanned).
	bySession := map[string]procEnv{}
	runs := map[string]claudeRun{}
	twice := map[string]bool{}
	for pid, e := range envs {
		launcher := e.BaseURL != "" || e.Model != "" || e.ConfigDir != "" || e.FastUnavailable || e.Vertex
		if !launcher && !e.Cmd {
			continue
		}
		id := readClaudeSessionID(n.claudeSessionFile(pid))
		if id == "" {
			continue
		}
		if launcher {
			bySession[id] = e
		}
		if e.Cmd {
			_, twice[id] = runs[id]
			runs[id] = claudeRun{PID: pid, Start: procStartMark(pid), MCPConfig: e.MCPConfig}
		}
	}
	for id, two := range twice {
		if two { // two claudes have it open: which one is it is not for the scan to say
			delete(runs, id)
		}
	}
	d.mu.Lock()
	d.bySession = bySession
	d.runs = runs
	var added []string
	for _, dir := range byPID {
		if !slices.Contains(d.found, dir) {
			d.found = append(d.found, dir)
			added = append(added, dir)
		}
	}
	found, report := slices.Clone(d.found), d.report
	d.mu.Unlock()
	// A blocked directory is kept in found (unblocking it lists it again at once) but never reported.
	if report != nil {
		d.mu.Lock()
		blocked := d.blocked
		d.mu.Unlock()
		if len(n.reportableDirs(added, blocked)) > 0 {
			report(n.reportableDirs(found, blocked))
		}
	}
}

// sessionEnv is the scanned environment of the live claude running session id. It returns false when the scan
// found no launcher or fast-mode information for the session.
func (n *Node) sessionEnv(id string) (procEnv, bool) {
	d := n.cdirs
	if d == nil {
		return procEnv{}, false
	}
	n.refreshClaudeDirs()
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.bySession[id]
	return e, ok
}

// sessionRun is the live claude running session id as the last scan saw it (false when none was seen, two were, or the
// scan cannot read command lines here).
func (n *Node) sessionRun(id string) (claudeRun, bool) {
	d := n.cdirs
	if d == nil {
		return claudeRun{}, false
	}
	n.refreshClaudeDirs()
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.runs[id]
	return r, ok
}

var (
	claudeConfigEnvRe = envVarRe("CLAUDE_CONFIG_DIR")
	anthropicBaseRe   = envVarRe("ANTHROPIC_BASE_URL")
	anthropicModelRe  = envVarRe("ANTHROPIC_MODEL")
)

// envVarRe matches NAME=value in a NUL- (/proc/<pid>/environ) or space-separated (ps -E) environment.
func envVarRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\x00\s])` + name + `=([^\x00\s]+)`)
}

// parseProcEnv picks the variables procEnv keeps out of an environment listing.
var (
	disableFastRe = envVarRe("CLAUDE_CODE_DISABLE_FAST_MODE")
	bedrockRe     = envVarRe("CLAUDE_CODE_USE_BEDROCK")
	foundryRe     = envVarRe("CLAUDE_CODE_USE_FOUNDRY")
	vertexRe      = envVarRe("CLAUDE_CODE_USE_VERTEX")
)

func claudeEnvFlag(value string) bool {
	return slices.Contains([]string{"1", "true", "yes", "on"}, strings.ToLower(value))
}

func parseProcEnv(env string) procEnv {
	get := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(env); m != nil {
			return m[1]
		}
		return ""
	}
	return procEnv{
		ConfigDir: get(claudeConfigEnvRe), BaseURL: get(anthropicBaseRe), Model: get(anthropicModelRe),
		FastUnavailable: claudeEnvFlag(get(disableFastRe)) || claudeEnvFlag(get(bedrockRe)) || claudeEnvFlag(get(foundryRe)),
		Vertex:          claudeEnvFlag(get(vertexRe)),
	}
}

// scanClaudeProcesses maps this user's running claude processes to the variables procEnv keeps from their
// environment and command line. Linux reads the environ of the claudes in the shared walk of this user's /proc entries
// (userProcesses); macOS asks ps once for all; Windows reads the process table (scanClaudeProcessesOS). Windows keeps
// no command line, and so leaves out the processes that set none of the variables (default directory and endpoint):
// how the start's --mcp-config path reaches claude through PowerShell and Git Bash is not verified, and a different
// spelling would list every Agent off its launcher (offlauncher.go).
func scanClaudeProcesses() map[int]procEnv {
	out := map[int]procEnv{}
	if runtime.GOOS == "windows" {
		return scanClaudeProcessesOS()
	}
	if runtime.GOOS == "linux" {
		for pid, argv := range userProcesses() {
			if !isClaudeCmd(argv) {
				continue
			}
			env, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
			pe := parseProcEnv(string(env))
			pe.Cmd, pe.MCPConfig = true, claudeMCPConfigs(argv)
			out[pid] = pe
		}
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "pid=,command=", "-U", strconv.Itoa(os.Getuid())).Output()
	if err != nil {
		return out
	}
	return parsePSEnv(string(b))
}

// parsePSEnv reads `ps -E -o pid=,command=` output: the command line is followed by the environment.
func parsePSEnv(s string) map[int]procEnv {
	out := map[int]procEnv{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || !isClaudeCmd(f[1:]) {
			continue
		}
		pe := parseProcEnv(line)
		pe.Cmd, pe.MCPConfig = true, psMCPConfigs(line)
		out[pid] = pe
	}
	return out
}

// isClaudeCmd: the command line runs Claude Code — its native binary, or its script under node / bun.
func isClaudeCmd(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch progName(argv[0]) {
	case "claude":
		return true
	case "node", "bun":
		return len(argv) > 1 && strings.Contains(argv[1], "claude")
	}
	return false
}

// listClaudeDirs: every config directory the node knows of (default, found in running claudes, from the hub),
// existing, each once, blocked ones marked: what the settings page offers to block and unblock.
func (n *Node) listClaudeDirs() []proto.ClaudeDir {
	n.refreshClaudeDirs()
	type cand struct{ dir, source string }
	cands := []cand{{n.defaultClaudeDir(), "default"}}
	running := map[string]int{}
	if d := n.cdirs; d != nil {
		d.mu.Lock()
		for _, f := range d.found {
			cands = append(cands, cand{f, "found"})
		}
		for _, k := range d.known {
			cands = append(cands, cand{k, "found"}) // the hub tells config ones apart
		}
		for _, dir := range d.byPID {
			if real, err := filepath.EvalSymlinks(n.expandClaudeDir(dir)); err == nil {
				running[real]++
			}
		}
		d.mu.Unlock()
	}
	var out []proto.ClaudeDir
	var seen []string
	for i, c := range cands {
		real, err := filepath.EvalSymlinks(n.expandClaudeDir(c.dir))
		if err != nil || slices.Contains(seen, real) {
			continue
		}
		seen = append(seen, real)
		files, _ := filepath.Glob(filepath.Join(real, "projects", "*", "*.jsonl"))
		r := running[real]
		if i == 0 {
			r = 0 // claudes without CLAUDE_CONFIG_DIR are not scanned: unknown
		}
		out = append(out, proto.ClaudeDir{Path: c.dir, Default: i == 0, Blocked: i > 0 && n.claudeDirBlocked(c.dir, real),
			Source: c.source, Running: r, Sessions: len(files)})
	}
	return out
}
