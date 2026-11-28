package node

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/osproc"
)

const (
	traexForegroundSource  = "traex:foreground"
	traexMultiWriterSource = "traex:multi-writer"
)

// Keep the complete lock set alongside the host snapshot until all hosts have
// contributed their foreground claims. No additional writer row is published.
type traexHostEvidence struct {
	writers   map[string]map[int][]string
	fresh     map[string]string
	uncertain map[string]bool
	timedOut  bool
}

func (n *Node) saveTraexHostEvidence(kind string, proof traexHostEvidence) {
	if kind != host.Herdr && kind != host.HerdrCore {
		return
	}
	if n.traexEvidence == nil {
		n.traexEvidence = map[string]traexHostEvidence{}
	}
	n.traexEvidence[kind] = proof
}

func traexWriterContested(pane string, ids []string, fresh map[string]string) bool {
	if len(ids) < 2 {
		return false
	} // fresh inference cannot displace a dedicated single-thread writer
	for other, id := range fresh {
		owns := slices.Contains(ids, id)
		if (other != pane && owns) || (other == pane && !owns) {
			return true
		}
	}
	return false
}

// Startup confirmation may run after the classic terminal changed its title.
// Read that pane from its own server, including named sessions.
func (n *Node) traexCurrentPane(ctx context.Context, pane string) *host.Agent {
	ref := host.ParsePane(pane)
	if ref.Kind != host.Herdr {
		return n.layoutPane(pane)
	} // Herdr App gets terminal.get during verification.
	h, ok := n.hosts.Get(ref.Kind).(*herdrHost)
	if !ok {
		return nil
	}
	t, err := h.target(ctx, ref.ID)
	if err != nil {
		return nil
	}
	snap, err := t.c.Snapshot(ctx)
	if err != nil {
		return nil
	}
	for _, raw := range snap.Panes {
		if raw.PaneID == t.id {
			p := fromHerdrAgent(t.session, raw)
			return &p
		}
	}
	return nil
}

// The whole title can itself contain " | ". Also consider its conversation part
// when the suffix is the directory that this terminal reports. Both spellings must
// resolve to at most one thread, including when a title on screen is truncated.
func traexPaneTitles(p host.Agent) []string {
	title := p.RawTitle
	if title == "" {
		title = p.Title
	}
	if title == "" {
		return nil
	}
	if r := []rune(title); len(r) > 2 && r[0] >= 0x2800 && r[0] <= 0x28ff && r[1] == ' ' {
		title = string(r[2:])
	}
	titles := []string{title}
	if i := strings.LastIndex(title, " | "); i > 0 {
		dir := title[i+3:]
		for _, cwd := range []string{p.ForegroundCwd, p.Cwd} {
			if cwd != "" && (dir == cwd || dir == filepath.Base(cwd)) {
				return append(titles, title[:i])
			}
		}
	}
	return titles
}

func traexLogStart(pid int) (time.Time, bool) {
	if runtime.GOOS == "windows" {
		raw, ok := strings.CutPrefix(osproc.Identify(pid).Start, "ft:")
		ns, err := strconv.ParseInt(raw, 10, 64)
		return time.Unix(0, ns), ok && err == nil
	}
	t, ok := agyProcessStart(pid)
	if ok && runtime.GOOS == "linux" {
		// procfs rounds btime down to seconds and the process start to ticks.
		// Only times beyond the upper bound distinguish a recycled PID.
		t = t.Add(time.Second + time.Second/agyUserHZ)
	}
	return t, ok
}

type traexFreshClient struct {
	pane   host.Agent
	thread string
	pid    int
	start  time.Time
}

// Read the full titles from state, including legacy rows. The display cache
// shortens unnamed threads and only includes paginated history. SQLite groups the
// exact matches so duplicate names cannot be resolved by recency or by row order.
func traexMatchingTitles(ctx context.Context, db string, panes []host.Agent) map[string]string {
	var names []string
	for _, p := range panes {
		for _, title := range traexPaneTitles(p) {
			names = append(names, fmt.Sprintf("CAST(X'%x' AS TEXT)", title))
		}
	}
	if len(names) == 0 {
		return nil
	}
	q := `WITH main AS (SELECT id, COALESCE(NULLIF(name,''),title) COLLATE BINARY AS label
		FROM threads WHERE archived=0 AND COALESCE(thread_source,'') <> 'subagent' AND source <> 'subagent'
		AND CASE WHEN json_valid(source) THEN json_type(source,'$.subagent') IS NULL ELSE 1 END)
		SELECT label, CASE WHEN count(*) = 1 THEN min(id) ELSE '' END AS id
		FROM main WHERE label IN (` + strings.Join(names, ",") + `) GROUP BY label`
	var rows []struct{ Label, ID string }
	if readSQLiteJSON(ctx, db, strings.NewReader(q+";"), &rows) != nil {
		return nil
	}
	matches := map[string]string{}
	for _, r := range rows {
		matches[r.Label] = r.ID
	}
	return matches
}

func (n *Node) traexPlainClient(argv []string) bool {
	args := traexUnwrapRunner(argv)
	return len(args) == 1 && n.isTraexLauncher(args[0])
}

// One read-only title query and one log query share a budget for the entire
// snapshot. Herdr supplies the candidate processes. A daemon cannot add a row.
func (n *Node) traexFreshThreads(ctx context.Context, panes []host.Agent, observed map[string][]int, proof *traexHostEvidence) map[string]string {
	if len(panes) == 0 {
		return nil
	}
	home := n.traexHome()
	state, logs := codexAppDBPath(home), codexNumberedDB(home, "logs_")
	if state == "" || logs == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	defer func() { proof.timedOut = ctx.Err() != nil }()
	matches := traexMatchingTitles(ctx, state, panes)
	var clients []traexFreshClient
	var predicates []string
	var earliest time.Time
	for _, p := range panes {
		var ids []string
		for _, name := range traexPaneTitles(p) {
			if id, ok := matches[name]; ok {
				ids = append(ids, id)
			}
		}
		if len(ids) != 1 || !validID(ids[0]) {
			continue
		}
		pid := p.ForegroundPID
		if pid <= 0 && host.ParsePane(p.PaneID).Kind == host.Herdr {
			pids, seen := observed[p.PaneID]
			if !seen {
				pids, _ = n.paneProcesses(ctx, p.PaneID)
			}
			if len(pids) != 1 {
				continue
			}
			pid = pids[0]
		}
		if pid <= 0 {
			continue
		}
		start, ok := traexLogStart(pid)
		argv, err := processArgv(pid)
		if !ok || err != nil || !n.traexPlainClient(argv) {
			continue
		}
		clients = append(clients, traexFreshClient{p, ids[0], pid, start})
		predicates = append(predicates, fmt.Sprintf("(process_uuid GLOB 'pid:%d:?*' AND (ts,ts_nanos)>(%d,%d))", pid, start.Unix(), start.Nanosecond()))
		if earliest.IsZero() || start.Before(earliest) {
			earliest = start
		}
	}
	if len(clients) == 0 {
		return nil
	}
	// Scan the indexed time range once for all foreground processes. Exact PID
	// delimiters prevent prefix collisions. Multiple threads or process tokens are ambiguous.
	q := fmt.Sprintf(`SELECT CAST(substr(process_uuid,5) AS INTEGER) AS pid,
		CASE WHEN min(thread_id)=max(thread_id) THEN min(thread_id) END AS id FROM logs
		WHERE (ts,ts_nanos)>(%d,%d) AND target GLOB 'traex_tui::*' AND thread_id IS NOT NULL
		AND (%s) GROUP BY process_uuid`, earliest.Unix(), earliest.Nanosecond(), strings.Join(predicates, " OR "))
	var rows []struct {
		PID int
		ID  string
	}
	if querySQLite(ctx, logs, q, &rows) != nil {
		return nil
	}
	recorded := map[int]string{}
	for _, r := range rows {
		if _, duplicate := recorded[r.PID]; duplicate {
			recorded[r.PID] = ""
		} else {
			recorded[r.PID] = r.ID
		}
	}
	fresh := map[string]string{}
	for _, c := range clients {
		if recorded[c.pid] != c.thread {
			continue
		}
		verified, err := n.traexStillForeground(ctx, c)
		if err != nil {
			if proof.uncertain == nil {
				proof.uncertain = map[string]bool{}
			}
			proof.uncertain[c.thread] = true
		}
		if !verified {
			continue
		}
		fresh[c.pane.PaneID] = c.thread
	}
	if ctx.Err() != nil {
		return nil
	}
	for pane, id := range fresh {
		if proof.uncertain[id] {
			delete(fresh, pane)
		}
	}
	return fresh
}

func (n *Node) traexStillForeground(ctx context.Context, c traexFreshClient) (bool, error) {
	ref := host.ParsePane(c.pane.PaneID)
	switch ref.Kind {
	case host.HerdrCore:
		// Windows inventory can substitute a descendant of a wrapper. Read the
		// core's unmodified foreground report before attributing that PID's logs.
		h, ok := n.hosts.Get(ref.Kind).(*herdrCoreHost)
		if !ok {
			return false, nil
		}
		t, err := h.c.Get(ctx, ref.ID)
		if err != nil {
			return false, err
		}
		if t.Foreground == nil || t.Foreground.Pid != c.pid || t.State != "live" || !slices.Equal(traexPaneTitles(fromTerminal(*t)), traexPaneTitles(c.pane)) {
			return false, nil
		}
	case host.Herdr:
		pids, err := n.paneProcesses(ctx, c.pane.PaneID)
		if err != nil {
			return false, err
		}
		if len(pids) != 1 || pids[0] != c.pid {
			return false, nil
		}
	default:
		return false, nil
	}
	start, ok := traexLogStart(c.pid)
	return ok && start.Equal(c.start), nil
}

// Each host collects its panes independently. Suppress an inferred binding if
// another terminal claims that thread before publishing the merged inventory.
func traexDropConflictingFresh(agents []host.Agent, hosts map[string]traexHostEvidence) {
	allFresh := map[string]string{}
	uncertain := map[string]bool{}
	timedOut := false
	for _, proof := range hosts {
		for pane, id := range proof.fresh {
			allFresh[pane] = id
		}
		for id := range proof.uncertain {
			uncertain[id] = true
		}
		timedOut = timedOut || proof.timedOut
	}
	type claims struct {
		fresh     int
		confirmed bool
	}
	byThread := map[string]claims{}
	for _, a := range agents {
		if a.Agent != traexAgent || a.SessionID() == "" || host.ParsePane(a.PaneID).Kind == host.Botmux {
			continue
		}
		id := a.SessionID()
		c := byThread[id]
		switch a.AgentSession.Source {
		case traexForegroundSource:
			c.fresh++
		case traexMultiWriterSource:
			// A multi-thread writer can inherit another terminal's environment.
		default:
			c.confirmed = true
		}
		byThread[id] = c
	}
	for i, a := range agents {
		if a.Agent != traexAgent || a.AgentSession == nil {
			continue
		}
		c := byThread[a.SessionID()]
		switch a.AgentSession.Source {
		case traexForegroundSource:
			if timedOut || uncertain[a.SessionID()] || c.confirmed || c.fresh > 1 {
				agents[i].AgentSession = nil
			}
		case traexMultiWriterSource:
			if timedOut || uncertain[a.SessionID()] || c.fresh > 0 {
				agents[i].AgentSession = nil
				continue
			}
			for _, ids := range hosts[host.ParsePane(a.PaneID).Kind].writers[a.PaneID] {
				if slices.Contains(ids, a.SessionID()) && (slices.ContainsFunc(ids, func(id string) bool { return uncertain[id] }) || traexWriterContested(a.PaneID, ids, allFresh)) {
					agents[i].AgentSession = nil
					break
				}
			}
		}
	}
}
