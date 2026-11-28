package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

// A Codex-compatible CLI can run in herdr panes without being recognized by the host. The node therefore finds
// it itself in every host's panes:
//   - foreground client arguments (`traex resume <id>`, `traecli resume <id>`, `--resume <id>`) explicitly name
//     the conversation opened in that terminal and take precedence over shared writer metadata;
//   - <CLI data>/thread-writer-locks/<thread id>.owner.json names the pid writing each thread (session); a dedicated
//     per-terminal writer's environment holds the host's pane variable (host.PaneEnv: HERDR_PANE_ID / HC_TERMINAL_ID),
//     whereas a shared app-server writer can own conversations across multiple terminals while retaining only its
//     launching terminal's environment, so shared writers never bind unrelated conversations to a terminal;
//   - its conversation is paginated SQLite history (state_<n>.sqlite + thread_history_<n>.sqlite) or a legacy rollout
//     under <CLI data>/sessions/YYYY/MM/DD (transcript/codex.go);
//   - native host lifecycle reports (Working/Done/Blocked/Idle) are preserved; when the host reports none (or only the
//     synthetic classic-herdr resume fallback), a spinner in the terminal title or "esc to interrupt" on screen is
//     working, a numbered choice on screen is blocked, and anything else is a finished turn.

const traexAgent = "traex"

func (n *Node) traexHome() string {
	if n.sessionScope != nil {
		return filepath.Join(n.dir, "traex")
	}
	return filepath.Join(n.home, ".trae", "cli")
}

func (n *Node) traexPath(id string) string {
	if _, ok := n.traexThread(id); ok {
		p := n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: id})
		if fileExists(p) {
			return p
		}
	}
	return rolloutPath(n.traexHome(), id)
}

func (n *Node) traexTitle(id string) string {
	if t, ok := n.traexThread(id); ok && t.Title != "" {
		return t.Title
	}
	return threadTitle(n.traexHome(), id)
}

var traexCache = struct {
	sync.Mutex
	panes  map[int]string // pid → its pane, a stored PaneRef (from its environment; a pid never moves)
	status map[string]traexState
}{panes: map[int]string{}, status: map[string]traexState{}}

type traexState struct {
	status string
	at     time.Time
}

type traexOwnerSet struct {
	direct        map[int][]string // non-shared per-terminal writer pid -> main thread IDs (newest first)
	shared        map[int][]string // shared app-server writer pid -> main thread IDs (newest first)
	sharedThreads map[string]bool  // main thread IDs currently locked by a shared app-server writer
}

// traexOwners maps each live dedicated (non-shared) compatible CLI process to its newest main conversation.
func (n *Node) traexOwners() map[int]string {
	out := map[int]string{}
	for pid, threads := range n.traexOwnerThreads(n.traexThreads()) {
		out[pid] = threads[0]
	}
	return out
}

// Fresh starts need positive evidence from the writer or foreground client, including when old IDs were archived,
// outside the rollout scan window, or absent from a cached metadata list.
func (n *Node) traexLiveThread(pane, preferred string) string {
	// Revalidate the winner of the combined host inventory, including when
	// a generic multi-thread writer was demoted during discovery.
	n.mu.Lock()
	var fresh string
	for id, a := range n.agents {
		if a.PaneID == pane && a.Agent == traexAgent && a.AgentSession != nil && a.AgentSession.Source == traexForegroundSource {
			fresh = id
			break
		}
	}
	n.mu.Unlock()
	if fresh != "" {
		if preferred != "" {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if p := n.traexCurrentPane(ctx, pane); p != nil {
			if n.traexFreshThreads(ctx, []host.Agent{*p}, nil, &traexHostEvidence{})[pane] == fresh {
				return fresh
			}
		}
		return ""
	}
	threads := n.traexThreads()
	ownerSet := n.traexReadOwners(threads)
	if preferred != "" || len(ownerSet.shared) > 0 {
		paneAgent := n.layoutPane(pane)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		fgID := n.traexPaneForegroundResumed(ctx, pane, paneAgent, true, true, nil)
		cancel()
		if fgID != "" {
			if t, known := threads[fgID]; !known || traexConversation(t) {
				if preferred == "" || fgID == preferred {
					return fgID
				}
			}
		}
	}
	owners := map[int][]string{}
	for pid, ids := range ownerSet.direct {
		if n.paneOfPID(pid) == pane {
			owners[pid] = ids
		}
	}
	if id := traexPaneThread(owners, preferred, parentPID); id != "" {
		return id
	}
	if len(ownerSet.sharedThreads) > 0 {
		n.mu.Lock()
		for id, a := range n.agents {
			if a.PaneID == pane && a.Agent == traexAgent && ownerSet.sharedThreads[id] && (preferred == "" || id == preferred) {
				n.mu.Unlock()
				return id
			}
		}
		n.mu.Unlock()
	}
	return ""
}

// layoutPane returns the cached snapshot pane entry for pane, if any (used to read herdr-core foreground argv
// without an extra host RPC).
func (n *Node) layoutPane(pane string) *host.Agent {
	kind := host.ParsePane(pane).Kind
	n.mu.Lock()
	defer n.mu.Unlock()
	l, ok := n.layouts[kind]
	if !ok {
		return nil
	}
	for i := range l.panes {
		if l.panes[i].PaneID == pane {
			p := l.panes[i]
			return &p
		}
	}
	return nil
}

// traexOwnerThreads keeps every main writer owned by a dedicated (non-shared) process, newest first: a host's
// existing session may still have its own writer even when another thread is newer. Known subagents never fall back
// to their own id. Legacy subagents may supply a root confirmed by rollout metadata; older CLIs without metadata keep
// the owner-file fallback.
func (n *Node) traexOwnerThreads(threads map[string]codexAppThread) map[int][]string {
	return n.traexReadOwners(threads).direct
}

func (n *Node) traexReadOwners(threads map[string]codexAppThread) traexOwnerSet {
	set := traexOwnerSet{
		direct:        map[int][]string{},
		shared:        map[int][]string{},
		sharedThreads: map[string]bool{},
	}
	dir := filepath.Join(n.traexHome(), "thread-writer-locks")
	files, _ := filepath.Glob(filepath.Join(dir, "*.owner.json"))
	type owner struct {
		thread string
		mtime  time.Time
	}
	byPID := map[int][]owner{}
	procName := map[int]string{}
	sharedByName := map[int]bool{}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var o struct {
			PID         int    `json:"pid"`
			ProcessName string `json:"process_name"`
		}
		if json.Unmarshal(b, &o) != nil || o.PID <= 0 || !alive(o.PID) {
			continue
		}
		thread := strings.TrimSuffix(filepath.Base(f), ".owner.json")
		if validID(thread) {
			byPID[o.PID] = append(byPID[o.PID], owner{thread, st.ModTime()})
			if o.ProcessName != "" {
				if procName[o.PID] == "" {
					procName[o.PID] = o.ProcessName
				}
				if traexAppServerName(o.ProcessName) {
					sharedByName[o.PID] = true
				}
			}
		}
	}
	home := n.traexHome()
	paginated := codexHistoryDBPath(home) != ""
	for pid, owners := range byPID {
		sort.Slice(owners, func(i, j int) bool {
			if owners[i].mtime.Equal(owners[j].mtime) {
				return owners[i].thread < owners[j].thread
			}
			return owners[i].mtime.After(owners[j].mtime)
		})
		var main, fallback []string
		for _, o := range owners {
			t, known := threads[o.thread]
			if known && traexConversation(t) {
				main = append(main, o.thread)
				continue
			}
			p := rolloutPath(home, o.thread)
			if p == "" {
				// A new paginated owner can precede its state row. Wait for its source.
				if !known && !paginated {
					fallback = append(fallback, o.thread)
				}
				continue
			}
			m := metaOf(home, p)
			if !known && !m.sub {
				main = append(main, o.thread)
				continue
			}
			root := m.root
			if root == o.thread || !validID(root) {
				continue
			}
			parent, listed := threads[root]
			if listed && !traexConversation(parent) {
				continue
			}
			rootPath := rolloutPath(home, root)
			if (listed || rootPath != "") && (rootPath == "" || !metaOf(home, rootPath).sub) {
				fallback = append(fallback, root)
			}
		}
		ids := main
		if len(ids) == 0 {
			ids = fallback
		}
		if len(ids) == 0 {
			continue
		}
		if sharedByName[pid] || isTraexSharedWriter(pid, procName[pid]) {
			set.shared[pid] = ids
			for _, id := range ids {
				set.sharedThreads[id] = true
			}
		} else {
			set.direct[pid] = ids
		}
	}
	return set
}

func isTraexSharedWriter(pid int, processName string) bool {
	if traexAppServerName(processName) {
		return true
	}
	argv, err := processArgv(pid)
	return err == nil && traexAppServerArgs(argv)
}

func traexAppServerName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "app-server") ||
		strings.Contains(lower, "app_server") ||
		strings.Contains(lower, "appserver")
}

func traexAppServerArgs(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if traexAppServerName(filepath.Base(argv[0])) {
		return true
	}
	args := traexUnwrapRunner(argv)
	if len(args) == 0 {
		return false
	}
	if traexAppServerName(filepath.Base(args[0])) {
		return true
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			if !strings.Contains(arg, "=") && traexFlagTakesValue(arg) && i+1 < len(args) {
				i++
			}
			continue
		}
		lower := strings.ToLower(arg)
		return lower == "app-server" || lower == "app_server" || lower == "appserver"
	}
	return false
}

// traexUnwrapRunner strips leading `env` or script-runtime wrappers so subcommand parsing sees the CLI itself.
func traexUnwrapRunner(argv []string) []string {
	args := argv
	for len(args) > 0 {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(args[0]), ".exe"))
		switch base {
		case "env":
			i := 1
			for i < len(args) {
				a := args[i]
				if a == "--" {
					i++
					break
				}
				if strings.HasPrefix(a, "-") {
					if (a == "-u" || a == "-C" || a == "-S") && i+1 < len(args) {
						i += 2
						continue
					}
					i++
					continue
				}
				if strings.Contains(a, "=") {
					i++
					continue
				}
				break
			}
			if i >= len(args) {
				return nil
			}
			args = args[i:]
		case "node", "bun", "deno", "python", "python3":
			i := 1
			for i < len(args) {
				a := args[i]
				if a == "--" {
					i++
					break
				}
				if strings.HasPrefix(a, "-") {
					if (a == "-r" || a == "--require" || a == "--import" || a == "-e" || a == "-c" || a == "-m") && i+1 < len(args) {
						i += 2
						continue
					}
					i++
					continue
				}
				break
			}
			if i >= len(args) {
				return nil
			}
			args = args[i:]
		default:
			return args
		}
	}
	return args
}

func isTraexProgram(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	base = strings.TrimSuffix(base, ".exe")
	for _, ext := range []string{".js", ".mjs", ".cjs", ".sh", ".cmd", ".bat"} {
		base = strings.TrimSuffix(base, ext)
	}
	switch base {
	case "traex", "traecli", "trae-cli", "trae_cli":
		return true
	}
	return false
}

func (n *Node) isTraexLauncher(name string) bool {
	if isTraexProgram(name) {
		return true
	}
	base := filepath.Base(name)
	if base == "" || base == "." || base == "/" {
		return false
	}
	if l, ok := n.managedLauncher(base); ok && l.AgentType() == traexAgent {
		return true
	}
	return false
}

func traexFlagTakesValue(flag string) bool {
	switch flag {
	case "-c", "--config", "-C", "--cd", "--cwd", "-m", "--model", "-p", "--profile",
		"-s", "--sandbox", "--sandbox-mode", "-a", "--ask-for-approval", "--approval-policy",
		"-i", "--image", "--add-dir", "--color", "-o", "--output-last-message", "--output-schema",
		"--local-provider", "--enable", "--disable", "--remote", "--remote-auth-token-env",
		"--listen", "--reasoning-effort":
		return true
	}
	return false
}

// traexResumedIDFromArgs extracts an explicitly resumed conversation ID from a TRAE CLI client command line.
// Non-resume commands and UUIDs inside prompts or other subcommands are ignored.
func traexResumedIDFromArgs(argv []string, allowAnyProgram bool, isManaged func(string) bool) string {
	args := traexUnwrapRunner(argv)
	if len(args) == 0 {
		return ""
	}
	prog := args[0]
	if !isTraexProgram(prog) && (isManaged == nil || !isManaged(prog)) {
		if !allowAnyProgram || shellProgram(prog) || windowsPowerShellProgram(prog) {
			return ""
		}
	}
	inResume := false
	afterDoubleDash := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if !afterDoubleDash && arg == "--" {
			afterDoubleDash = true
			continue
		}
		if !afterDoubleDash {
			if id, handled := traexResumeFlag(args, &i, inResume); handled {
				return id
			}
			if !inResume {
				if arg == "resume" {
					inResume = true
					continue
				}
				if strings.HasPrefix(arg, "-") {
					if !strings.Contains(arg, "=") && traexFlagTakesValue(arg) && i+1 < len(args) {
						i++
					}
					continue
				}
				return ""
			}
			if strings.HasPrefix(arg, "-") {
				if !strings.Contains(arg, "=") && traexFlagTakesValue(arg) && i+1 < len(args) {
					i++
				}
				continue
			}
		}
		if inResume && validID(arg) {
			return arg
		}
		return ""
	}
	return ""
}

func traexResumeFlag(args []string, i *int, inResume bool) (string, bool) {
	arg := args[*i]
	prefixes := []string{"--resume=", "--resume-session-id="}
	flags := []string{"--resume", "--resume-session-id"}
	if inResume {
		prefixes = append(prefixes, "--session-id=", "--thread-id=", "--conversation-id=")
		flags = append(flags, "--session-id", "--thread-id", "--conversation-id")
	}
	for _, p := range prefixes {
		if strings.HasPrefix(arg, p) {
			val := strings.TrimPrefix(arg, p)
			if validID(val) {
				return val, true
			}
			return "", true
		}
	}
	for _, f := range flags {
		if arg == f {
			if *i+1 < len(args) {
				*i++
				if validID(args[*i]) {
					return args[*i], true
				}
			}
			return "", true
		}
	}
	return "", false
}

func (n *Node) traexPaneForegroundResumed(ctx context.Context, pane string, p *host.Agent, allowAnyProgram, allowHostQuery bool, observed map[string][]int) string {
	parse := func(argv []string, permissive bool) string {
		return traexResumedIDFromArgs(argv, permissive, n.isTraexLauncher)
	}
	if p != nil && len(p.ForegroundArgv) > 0 {
		if id := parse(p.ForegroundArgv, false); id != "" {
			return id
		}
		if allowAnyProgram {
			return parse(p.ForegroundArgv, true)
		}
		return ""
	}
	if p != nil && p.ForegroundPID > 0 {
		if argv, err := processArgv(p.ForegroundPID); err == nil && len(argv) > 0 {
			if id := parse(argv, false); id != "" {
				return id
			}
			if allowAnyProgram {
				return parse(argv, true)
			}
			return ""
		}
	}
	if !allowHostQuery || pane == "" || n.hosts == nil {
		return ""
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	pids, err := n.paneProcesses(pctx, pane)
	cancel()
	if observed != nil {
		observed[pane] = pids
	}
	if err != nil || len(pids) == 0 {
		return ""
	}
	argvs := make([][]string, 0, len(pids))
	for _, pid := range pids {
		if argv, err := processArgv(pid); err == nil && len(argv) > 0 {
			argvs = append(argvs, argv)
		}
	}
	for _, argv := range argvs {
		if id := parse(argv, false); id != "" {
			return id
		}
	}
	if allowAnyProgram {
		for _, argv := range argvs {
			if id := parse(argv, true); id != "" {
				return id
			}
		}
	}
	return ""
}

// traexNativeStatus returns a host's native lifecycle status for a TRAE terminal when present.
// Classic herdr's PaneReportResume fallback claims unrecognized panes with a synthetic idle state
// (no AgentSession, or Source == resumeSource); those still fall back to title/screen detection.
func traexNativeStatus(a host.Agent) string {
	switch a.AgentStatus {
	case proto.StatusWorking, proto.StatusDone, proto.StatusBlocked:
		return a.AgentStatus
	case proto.StatusIdle:
		if host.ParsePane(a.PaneID).Kind != host.Herdr {
			return a.AgentStatus
		}
		if a.AgentSession != nil && a.SessionID() != "" && a.AgentSession.Source != resumeSource {
			return a.AgentStatus
		}
		return ""
	default:
		return ""
	}
}

// Classic Herdr can report synthetic Idle before Persona's second RPC
// supplies a session. Only a distinct native reporter positively identifies it.
func traexAppliedStatus(a host.Agent, source string) string {
	if source == traexForegroundSource && a.AgentStatus == proto.StatusIdle && a.AgentSession != nil && a.AgentSession.Source != "" && a.AgentSession.Source != resumeSource {
		return proto.StatusIdle
	}
	return traexNativeStatus(a)
}

// alive says whether the process behind a pid file this user's claude or a compatible CLI wrote still runs. A pid now held by
// another user's process is a reused one, not the program that wrote the file (#2141). The cost: a claude run as
// another user that still writes into this user's home (`sudo claude`, sudo keeping HOME on macOS) counts as gone.
func alive(pid int) bool { return osproc.AliveMine(pid) }

// paneEnvRe finds a host's pane variable (host.PaneEnv) in a process's environment, NUL- (Linux) or space-separated
// (ps -E).
func paneEnvRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\x00\s])` + regexp.QuoteMeta(name) + `=([^\x00\s]+)`)
}

var herdrPaneEnvRe = paneEnvRe(herdrPaneEnv)

// paneEnvRes caches paneEnvRe per variable.
var paneEnvRes sync.Map // name -> *regexp.Regexp

// paneOfPID reads the pane a process runs in from its environment (Linux: /proc; macOS: ps -E): the variable of the
// first host whose PaneEnv it holds, as a stored PaneRef. Cached per pid.
func (n *Node) paneOfPID(pid int) string {
	traexCache.Lock()
	p, ok := traexCache.panes[pid]
	traexCache.Unlock()
	if ok {
		return p
	}
	if len(n.hosts.All()) == 0 {
		return "" // nothing to look for; not cached, the pid's pane is not known to be none
	}
	var env []byte
	if runtime.GOOS == "linux" {
		env, _ = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		env, _ = exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		cancel()
	}
	for _, h := range n.hosts.All() {
		re, ok := paneEnvRes.Load(h.PaneEnv())
		if !ok {
			re, _ = paneEnvRes.LoadOrStore(h.PaneEnv(), paneEnvRe(h.PaneEnv()))
		}
		if m := re.(*regexp.Regexp).FindSubmatch(env); m != nil {
			p = host.PaneRef{Kind: h.Kind(), ID: string(m[1])}.String()
			if h.Kind() == host.Herdr { // which server's pane: a named session's is qualified (#2196)
				picked := pickedEnv(string(env))
				p = n.envPane(func(name string) string { return picked[name] })
			}
			break
		}
	}
	traexCache.Lock()
	traexCache.panes[pid] = p
	traexCache.Unlock()
	return p
}

// traexSnapshotAgents turns the panes running a compatible CLI into agents, preserving native host lifecycle state,
// preferring an explicitly resumed foreground conversation ID over shared app-server writer metadata, and never
// assigning an unrelated shared-server conversation to a terminal.
func (n *Node) traexSnapshotAgents(ctx context.Context, panes []host.Agent, agents []host.Agent, proof *traexHostEvidence) []host.Agent {
	threads := n.traexThreads()
	ownerSet := n.traexReadOwners(threads)
	isAgent := map[string]bool{}
	compatible := map[string]int{}
	for i, a := range agents {
		// PaneReportResume claims a compatible-CLI pane in herdr so resume_argv persists, which makes herdr list a
		// session-less traex entry in snap.Agents; keep filling its session id and screen status when not native.
		if a.Agent != traexAgent {
			isAgent[a.PaneID] = true
		} else {
			compatible[a.PaneID] = i
			if t, known := threads[a.SessionID()]; known && !traexConversation(t) {
				agents[i].AgentSession = nil // a stale host claim cannot promote a known guardian to a terminal chat
			}
		}
	}
	directByPane := map[string]map[int][]string{}
	hasMultiThreadWriter := false
	for pid, ids := range ownerSet.direct {
		if len(ids) > 1 {
			hasMultiThreadWriter = true
		}
		pane := n.paneOfPID(pid)
		if pane == "" {
			continue
		}
		if directByPane[pane] == nil {
			directByPane[pane] = map[int][]string{}
		}
		directByPane[pane][pid] = ids
	}
	hasSharedOrMulti := len(ownerSet.shared) > 0 || hasMultiThreadWriter

	// Inspect foreground resumed conversation IDs. Hosts that already include ForegroundArgv (herdr-core) are checked
	// with zero extra RPCs; classic herdr queries pane.process_info only when shared/multi-thread writers exist or a
	// host-detected traex pane lacks a direct writer lock.
	fgResumed := map[string]string{}
	observed := map[string][]int{}
	var freshPanes []host.Agent
	paneByID := make(map[string]host.Agent, len(panes))
	for i := range panes {
		p := panes[i]
		pane := p.PaneID
		paneByID[pane] = p
		if isAgent[pane] {
			continue
		}
		_, isCompat := compatible[pane]
		allowHostQuery := hasSharedOrMulti || (isCompat && len(directByPane[pane]) == 0)
		id := n.traexPaneForegroundResumed(ctx, pane, &p, isCompat || p.Agent == traexAgent, allowHostQuery, observed)
		if id != "" {
			if t, known := threads[id]; !known || traexConversation(t) {
				fgResumed[pane] = id
			}
		} else if kind := host.ParsePane(pane).Kind; (kind == host.Herdr || kind == host.HerdrCore) &&
			(!isCompat || agents[compatible[pane]].SessionID() == "") && (len(p.ForegroundArgv) == 0 || n.traexPlainClient(p.ForegroundArgv)) {
			freshPanes = append(freshPanes, p)
		}
	}

	// Defense in depth: if a writer PID mapped to one pane owns a conversation explicitly claimed by a different
	// terminal (via foreground resume or host session), treat that writer as shared so it cannot cross-bind panes.
	paneClaims := map[string]map[string]bool{}
	addClaim := func(id, pane string) {
		if id == "" || pane == "" {
			return
		}
		if paneClaims[id] == nil {
			paneClaims[id] = map[string]bool{}
		}
		paneClaims[id][pane] = true
	}
	fresh := n.traexFreshThreads(ctx, freshPanes, observed, proof)
	proof.fresh = fresh
	proof.writers = directByPane
	for pane, id := range fgResumed {
		addClaim(id, pane)
	}
	for pane, i := range compatible {
		if fgResumed[pane] == "" {
			addClaim(agents[i].SessionID(), pane)
		}
	}
	for ownerPane, byPID := range directByPane {
		for pid, ids := range byPID {
			// A multi-thread writer that cannot account for its own terminal's
			// proven foreground thread is also shared.
			crossPane := traexWriterContested(ownerPane, ids, fresh)
			for _, id := range ids {
				for claimPane := range paneClaims[id] {
					if claimPane != ownerPane {
						crossPane = true
						break
					}
				}
				if crossPane {
					break
				}
			}
			if crossPane {
				delete(byPID, pid)
				ownerSet.shared[pid] = ids
				for _, id := range ids {
					ownerSet.sharedThreads[id] = true
				}
			}
		}
		if len(byPID) == 0 {
			delete(directByPane, ownerPane)
		}
	}

	var out []host.Agent
	claimedByPane := map[string]string{}
	resolved := map[string]bool{}
	apply := func(pane string, p host.Agent, hasPane bool, thread, source string) {
		applyCwd := func(a *host.Agent) {
			// A host workspace is a terminal container, which may be unrelated to where
			// Run started this CLI. Keep its placement, but report the conversation's cwd.
			if t, ok := threads[thread]; ok && t.Cwd != "" {
				a.Cwd = t.Cwd
			} else if cwd := metaOf(n.traexHome(), rolloutPath(n.traexHome(), thread)).cwd; cwd != "" {
				a.Cwd = cwd
			}
		}
		if i, ok := compatible[pane]; ok {
			orig := agents[i]
			dst := orig
			if hasPane {
				if p.RawTitle != "" {
					dst.RawTitle = p.RawTitle
				}
				if p.Title != "" {
					dst.Title = p.Title
				}
				if dst.Cwd == "" {
					dst.Cwd = p.Cwd
				}
				if dst.ForegroundCwd == "" {
					dst.ForegroundCwd = p.ForegroundCwd
				}
				if dst.ForegroundPID == 0 {
					dst.ForegroundPID = p.ForegroundPID
				}
				if len(dst.ForegroundArgv) == 0 && len(p.ForegroundArgv) > 0 {
					dst.ForegroundArgv = p.ForegroundArgv
				}
				if dst.TabID == "" {
					dst.TabID = p.TabID
				}
				if dst.WorkspaceID == "" {
					dst.WorkspaceID = p.WorkspaceID
				}
			}
			dst.Agent = traexAgent
			if orig.AgentSession != nil && orig.SessionID() == thread {
				source = orig.AgentSession.Source
			}
			dst.AgentSession = &host.AgentSession{Source: source, Agent: traexAgent, Kind: "id", Value: thread}
			applyCwd(&dst)
			if status := traexAppliedStatus(orig, source); status != "" {
				dst.AgentStatus = status
			} else {
				dst.AgentStatus = n.traexStatus(ctx, dst)
			}
			agents[i] = dst
			return
		}
		original := p
		p.Agent = traexAgent
		p.AgentSession = &host.AgentSession{Source: source, Agent: traexAgent, Kind: "id", Value: thread}
		applyCwd(&p)
		status := traexNativeStatus(p)
		if source == traexForegroundSource {
			status = traexAppliedStatus(original, source)
		}
		if status != "" {
			p.AgentStatus = status
		} else {
			p.AgentStatus = n.traexStatus(ctx, p)
		}
		out = append(out, p)
	}

	// Pass 1: Explicit foreground resumed conversation IDs take precedence over writer metadata and host claims.
	for _, p := range panes {
		pane := p.PaneID
		if isAgent[pane] {
			continue
		}
		thread := fgResumed[pane]
		if thread == "" {
			continue
		}
		if owner, taken := claimedByPane[thread]; taken && owner != pane {
			if i, ok := compatible[pane]; ok {
				agents[i].AgentSession = nil
			}
			resolved[pane] = true
			continue
		}
		claimedByPane[thread] = pane
		resolved[pane] = true
		apply(pane, p, true, thread, "")
	}

	// Pass 2: Dedicated (non-shared) per-pane writer locks resolve panes without an explicit foreground resume.
	for _, p := range panes {
		pane := p.PaneID
		if isAgent[pane] || resolved[pane] || len(directByPane[pane]) == 0 {
			continue
		}
		filtered := map[int][]string{}
		for pid, ids := range directByPane[pane] {
			var keep []string
			for _, id := range ids {
				if owner := claimedByPane[id]; owner == "" || owner == pane {
					keep = append(keep, id)
				}
			}
			if len(keep) > 0 {
				filtered[pid] = keep
			}
		}
		if len(filtered) == 0 {
			continue
		}
		preferred := ""
		if i, ok := compatible[pane]; ok {
			preferred = agents[i].SessionID()
		}
		hostID := preferred
		if preferred == "" {
			preferred = fresh[pane]
		}
		thread := traexPaneThread(filtered, preferred, parentPID)
		if thread == "" {
			continue
		}
		source := ""
		for pid, ids := range filtered {
			if len(directByPane[pane][pid]) > 1 && slices.Contains(ids, thread) && thread != hostID {
				// A different host may know this writer is shared. Keep the
				// distinction until both Herdr inventories have been merged.
				source = traexMultiWriterSource
				if fresh[pane] == thread {
					source = traexForegroundSource
				}
				break
			}
		}
		claimedByPane[thread] = pane
		resolved[pane] = true
		apply(pane, p, true, thread, source)
	}

	// Pass 3: Retain valid host-reported sessions on compatible panes not resolved above, clearing any session
	// already claimed by another terminal. Never assign unrelated shared-server conversations to sessionless panes.
	for i, a := range agents {
		if a.Agent != traexAgent || resolved[a.PaneID] {
			continue
		}
		hostID := a.SessionID()
		if hostID == "" {
			continue
		}
		if owner, taken := claimedByPane[hostID]; taken && owner != a.PaneID {
			agents[i].AgentSession = nil
			resolved[a.PaneID] = true
			continue
		}
		claimedByPane[hostID] = a.PaneID
		resolved[a.PaneID] = true
		p, hasPane := paneByID[a.PaneID]
		apply(a.PaneID, p, hasPane, hostID, "")
	}

	// Other hosts' claims are reconciled when the snapshot inventories merge.
	for _, p := range freshPanes {
		id := fresh[p.PaneID]
		if id != "" && !resolved[p.PaneID] && claimedByPane[id] == "" {
			apply(p.PaneID, p, true, id, traexForegroundSource)
		}
	}

	return out
}

// traexPaneThread picks one terminal conversation per pane. Nested exec writers inherit the pane environment,
// but cannot replace an ancestor's conversation. Among independent roots a still-owned host id wins, else the
// lowest pid makes the choice stable. Without ancestry and a host claim, ambiguous writers are left unclaimed.
func traexPaneThread(owners map[int][]string, preferred string, parent func(int) (int, error)) string {
	pids := make([]int, 0, len(owners))
	for pid := range owners {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	roots := pids
	complete := true
	if len(pids) > 1 { // most panes have one writer process: no process-tree reads needed
		roots = nil
		for _, pid := range pids {
			ancestors := lineage(pid, func(pid int) (int, error) {
				next, err := parent(pid)
				if err != nil {
					complete = false
				}
				return next, err
			})
			if len(ancestors) == ancestorsMost {
				complete = false
			}
			nested := false
			for _, ancestor := range ancestors[1:] {
				if len(owners[ancestor]) > 0 {
					nested = true
					break
				}
			}
			if !nested {
				roots = append(roots, pid)
			}
		}
	}
	for _, pid := range roots {
		for _, id := range owners[pid] {
			if id == preferred {
				return id
			}
		}
	}
	if len(roots) == 0 || !complete && len(roots) > 1 {
		return ""
	}
	return owners[roots[0]][0]
}

var traexChoiceRe = lazyre.New(`(?m)^\s*[›❯>]\s*1\.\s+\S`)

// traexStatus: working from the title's spinner at once; otherwise the screen, read at most every 2s.
func (n *Node) traexStatus(ctx context.Context, p host.Agent) string {
	if r := []rune(strings.TrimSpace(p.RawTitle)); len(r) > 0 && r[0] >= 0x2800 && r[0] <= 0x28FF {
		return proto.StatusWorking // braille spinner: a turn is running
	}
	traexCache.Lock()
	s, ok := traexCache.status[p.PaneID]
	traexCache.Unlock()
	if ok && time.Since(s.at) < 2*time.Second {
		return s.status
	}
	status := proto.StatusDone
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	text, err := n.readPane(rctx, p.PaneID, "visible", 40)
	cancel()
	switch {
	case err != nil:
		status = s.status // keep the last known
		if status == "" {
			status = proto.StatusUnknown
		}
	case strings.Contains(text, "esc to interrupt"):
		status = proto.StatusWorking
	case traexChoiceRe.MatchString(text):
		status = proto.StatusBlocked
	}
	traexCache.Lock()
	traexCache.status[p.PaneID] = traexState{status, time.Now()}
	traexCache.Unlock()
	return status
}
