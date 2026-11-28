package node

import (
	"cmp"
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Token usage backfill (docs/server/machines.md "Backfill", #9484): the hub keeps the token usage history from the
// day it started; the days before are only in the transcripts here. Once a machine, the node reads them with the
// live meter's rules (usage.go: a reply counted once, <synthetic> skipped), sums them by the hour, model and launcher,
// and sends the hours to the manager, oldest first. Only numbers leave: no text, no file names. The manager says
// where to start (where the history begins, or the hour after the last it stored) and where to stop (the live
// window's start), stores only hours that have no usage yet, and records how far it got, so a scan cut off continues
// and one run again counts nothing twice.
//
// It goes a week at a time (backfillSlice), oldest first, and sends each week before reading the next: the manager
// records the last hour stored, so a scan the connection cuts (the hub restarts several times an hour, and a
// computer with many gigabytes of transcripts takes longer than that, #9562) goes on from the next week on the next
// connection, and the node keeps nothing. A fork's copy of a reply keeps its timestamp, so it falls in the same week
// and is still counted once. Within a file, reading stops once its lines are past the week, so the weeks together
// read about one pass of the transcripts.
//
// The scan is gentle: it waits a few minutes after the connection, reads at backfillRate at most, and ends with the
// connection.

const (
	backfillDelay = 3 * time.Minute // after the connection: a node's start is busy enough
	backfillRate  = 4 << 20         // bytes a second, all transcripts together
	backfillRead  = 256 << 10       // the most one read takes before it waits its turn
	backfillChunk = 2000            // buckets a request, whole hours
	backfillCall  = 30 * time.Second
	// backfillSlice is what one pass reads and stores: a cut connection loses one week's reading at most.
	backfillSlice = 7 * 24 * time.Hour
	// backfillPast: this many lines in a row past a week's end end a file's read (a line that names a later time
	// inside it, a tool's input, does not).
	backfillPast = 50
	// backfillSmall: a file under this is read from its start; a larger one from about the week's start (startOffset).
	backfillSmall = 256 << 10
)

// backfillRange is what the manager wants read: nothing (done), or from Since to Before.
type backfillRange struct {
	Done   bool  `json:"done"`
	Since  int64 `json:"since"`  // Unix: the first hour to count
	Before int64 `json:"before"` // Unix: count only what came before (the live window starts there)
}

// backfillStatus is the manager's answer to usage.backfill.status: its own range (the agents read before agents
// were kept apart; an older manager's for every agent) and, from a manager that keeps a record per agent (#9984),
// each agent's the node named.
type backfillStatus struct {
	backfillRange
	Agents map[string]backfillRange `json:"agents"`
}

// backfillPlan is what to read of each of agents (those with nothing to read left out), and from when to when all
// of them; ok is false when there is nothing.
func backfillPlan(agents []string, st backfillStatus) (ranges map[string]backfillRange, since, before time.Time, ok bool) {
	ranges = map[string]backfillRange{}
	for _, a := range agents {
		r := st.backfillRange
		if st.Agents != nil {
			var answered bool
			if r, answered = st.Agents[a]; !answered {
				continue
			}
		}
		if r.Done || r.Since >= r.Before {
			continue
		}
		ranges[a] = r
		if s := time.Unix(r.Since, 0); since.IsZero() || s.Before(since) {
			since = s
		}
		if b := time.Unix(r.Before, 0); b.After(before) {
			before = b
		}
	}
	return ranges, since, before, len(ranges) > 0
}

// backfillWeek is what of a week's buckets (from start to end) the manager asked for: each agent's within its range,
// and the agents whose range the week reads (the requests say them: their records move on).
func backfillWeek(buckets []proto.UsageBucket, ranges map[string]backfillRange, start, end time.Time) ([]proto.UsageBucket, []string) {
	var agents []string
	for a, r := range ranges {
		if r.Since < end.Unix() && r.Before > start.Unix() {
			agents = append(agents, a)
		}
	}
	slices.Sort(agents)
	keep := buckets[:0]
	for _, b := range buckets {
		if r, ok := ranges[b.Agent]; ok && b.Minute >= r.Since && b.Minute < r.Before {
			keep = append(keep, b)
		}
	}
	return keep, agents
}

// backfillUsage asks the manager whether this machine's history wants a backfill and does it.
func (n *Node) backfillUsage(ctx context.Context, stateLoaded <-chan struct{}) {
	select { // the launchers come from the sessions' state, as the live meter's
	case <-ctx.Done():
		return
	case <-stateLoaded:
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(backfillDelay):
	}
	if n.peer == nil {
		return
	}
	names := n.usageAgents()
	var st backfillStatus
	cctx, cancel := context.WithTimeout(ctx, backfillCall)
	err := n.peer.Call(cctx, "usage.backfill.status", map[string]any{"agents": names}, &st)
	cancel()
	if err != nil { // an older manager does not know it
		return
	}
	ranges, since, before, ok := backfillPlan(names, st)
	if !ok {
		return
	}
	started := time.Now()
	lim := &readLimiter{rate: backfillRate, start: started}
	files := n.backfillFiles(ctx, since, lim)
	files = slices.DeleteFunc(files, func(f backfillFile) bool { _, want := ranges[f.ad.agent()]; return !want })
	queries := slices.DeleteFunc(n.usageQueries(), func(q usageQuery) bool { _, want := ranges[q.agent()]; return !want })
	var firsts []time.Time
	for _, q := range queries {
		firsts = append(firsts, q.first(ctx, n))
	}
	read, hours := 0, 0
	resolved := map[string]*proto.UsageDir{} // with a manager that keeps directories: each one's resolution, once
	// The weeks before the transcripts' first day hold nothing: start there (#9616).
	for start := backfillStart(files, firsts, since); start.Before(before); {
		end := start.Add(backfillSlice)
		if end.After(before) {
			end = before
		}
		buckets, err := n.backfillScan(ctx, files, queries, start, end, lim)
		if err != nil {
			return
		}
		buckets, agents := backfillWeek(buckets, ranges, start, end)
		for _, req := range backfillRequests(buckets, end, end.Equal(before)) {
			req["agents"] = agents
			if dirs := n.backfillDirs(ctx, req["buckets"].([]proto.UsageBucket), resolved); len(dirs) > 0 {
				req["dirs"] = dirs
			}
			var res struct {
				Stored int `json:"stored"`
			}
			cctx, cancel := context.WithTimeout(ctx, backfillCall)
			err := n.peer.Call(cctx, "usage.backfill", req, &res)
			cancel()
			if err != nil { // the next connection asks again, from the hour after the last stored
				n.logger.Printf("usage backfill: %v (%d hours stored)", err, hours)
				return
			}
			hours += res.Stored
		}
		read += len(buckets)
		start = end
	}
	n.logger.Printf("usage backfill: %d buckets read in %s, %d hours stored", read, time.Since(started).Round(time.Second), hours)
}

// backfillRequests are a week's usage.backfill requests: its buckets (in time order) in whole hours, each saying the
// last hour it holds, the last the hour before end (the week is read through it, hours without usage too) and, with
// done, that the backfill is.
func backfillRequests(buckets []proto.UsageBucket, end time.Time, done bool) []map[string]any {
	return backfillRequestsSized(buckets, end, done, backfillChunk)
}

func backfillRequestsSized(buckets []proto.UsageBucket, end time.Time, done bool, size int) []map[string]any {
	chunks := backfillChunks(buckets, size)
	out := make([]map[string]any, len(chunks))
	for i, chunk := range chunks {
		last := i == len(chunks)-1
		through := end.Add(-time.Hour).Unix()
		if !last {
			through = chunk[len(chunk)-1].Minute
		}
		out[i] = map[string]any{"buckets": chunk, "through": through, "done": done && last}
	}
	return out
}

// backfillFile is a transcript the backfill reads, with the time of its first line (zero: none found in its head).
type backfillFile struct {
	usagePath
	first time.Time
}

// backfillHead is how much of a transcript's start is read for its first timestamp.
const backfillHead = 64 << 10

// backfillFiles lists the transcripts changed since since, each with its first line's time (read once, through lim).
func (n *Node) backfillFiles(ctx context.Context, since time.Time, lim *readLimiter) []backfillFile {
	paths := n.usageFiles(since)
	out := make([]backfillFile, 0, len(paths))
	buf := make([]byte, backfillHead)
	for _, p := range paths {
		f := backfillFile{usagePath: p}
		if fh, err := os.Open(p.path); err == nil {
			nr, _ := io.ReadFull(fh, buf)
			fh.Close()
			_ = lim.wait(ctx, nr)
			f.first = firstTime(buf[:nr])
		}
		out = append(out, f)
	}
	return out
}

// backfillStart is where the weeks begin: since, or the hour of the earliest first line when all the files have one
// and it is later; firsts are the databases' earliest requests (zero: none).
func backfillStart(files []backfillFile, firsts []time.Time, since time.Time) time.Time {
	var first time.Time
	for _, f := range files {
		if f.first.IsZero() {
			return since
		}
		if first.IsZero() || f.first.Before(first) {
			first = f.first
		}
	}
	for _, t := range firsts {
		if !t.IsZero() && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	if first.IsZero() || !first.After(since) {
		return since
	}
	return first.Truncate(time.Hour)
}

// overlaps: the file can hold lines from start to end: it changed after start, and its first line is before end.
func (f backfillFile) overlaps(start, end time.Time) bool {
	return f.st.ModTime().After(start) && (f.first.IsZero() || f.first.Before(end))
}

// backfillScan sums the replies of every transcript and database from start to end by the hour, model, launcher and
// agent, in time order. Only ctx ending stops it (its error).
func (n *Node) backfillScan(ctx context.Context, files []backfillFile, queries []usageQuery, start, end time.Time, lim *readLimiter) ([]proto.UsageBucket, error) {
	return n.usageScan(ctx, files, queries, start, end, lim, 3600)
}

// usageScan is backfillScan by steps of step seconds (the re-split's ten minutes, usage_resplit.go).
func (n *Node) usageScan(ctx context.Context, files []backfillFile, queries []usageQuery, start, end time.Time, lim *readLimiter, step int64) ([]proto.UsageBucket, error) {
	m := newUsageMeter()
	m.step, m.until, m.seen = step, end, map[uint64]struct{}{}
	m.launcherOf = func(session string) string {
		st, _ := n.state.get(session)
		return st.Launcher
	}
	if dirs := n.usageDirs.get(); dirs != nil { // this connection's manager keeps directories (usage_dirs.go)
		m.fileDir = n.usageFileDirs(dirs.temps)
		m.recordDir = func(q usageQuery, session string) string {
			return n.usageSessionDir(session, q.dir(session), dirs.temps)
		}
	}
	for _, f := range files {
		if !f.overlaps(start, end) {
			continue
		}
		if _, err := m.scanSlice(ctx, f.usagePath, start, end, lim); err != nil {
			return nil, err
		}
	}
	for _, q := range queries {
		recs, err := q.between(ctx, n, start, end)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			m.add(&usageFile{dir: m.dirOfRecord(q, r)}, r, start)
		}
	}
	out := make([]proto.UsageBucket, 0, len(m.buckets))
	for _, b := range m.buckets {
		out = append(out, *b)
	}
	slices.SortFunc(out, func(a, b proto.UsageBucket) int {
		return cmp.Or(cmp.Compare(a.Minute, b.Minute), cmp.Compare(a.Model, b.Model), cmp.Compare(a.Launcher, b.Launcher))
	})
	return out, nil
}

// backfillChunks splits buckets (in time order) into requests of at most size buckets, never an hour across two (an
// hour with more is one request of its own); one empty request when there are none, so the manager hears it is done.
func backfillChunks(buckets []proto.UsageBucket, size int) [][]proto.UsageBucket {
	if len(buckets) == 0 {
		return [][]proto.UsageBucket{nil}
	}
	var out [][]proto.UsageBucket
	start := 0
	for i := 1; i < len(buckets); i++ {
		if i-start >= size && buckets[i].Minute != buckets[i-1].Minute {
			out = append(out, buckets[start:i])
			start = i
		}
	}
	return append(out, buckets[start:])
}

// scanSlice reads f from about start (startOffset, from its start when small) through lim until its lines are past
// end (backfillPast of them in a row), as scan reads what a file gained; read is how far it got.
func (m *usageMeter) scanSlice(ctx context.Context, f usagePath, start, end time.Time, lim *readLimiter) (read int64, err error) {
	fh, err := os.Open(f.path)
	if err != nil {
		return 0, ctx.Err()
	}
	defer fh.Close()
	off, probed := startOffsetFrom(f.path, f.st.Size(), start, backfillSmall)
	if err := lim.wait(ctx, int(probed)); err != nil { // the search's reads count against the rate too
		return 0, err
	}
	uf := m.newFile(f, off)
	r := &limitedReader{ctx: ctx, r: io.NewSectionReader(fh, off, f.st.Size()-off), lim: lim}
	past := 0
	read = off + eachLineAt(r, func(line []byte, _, _ int64) bool {
		if ts := firstTime(line); !ts.IsZero() {
			if ts.Before(end) {
				past = 0
			} else if past++; past >= backfillPast {
				return false
			}
		}
		if rec, ok := uf.parser.Line(line); ok {
			m.add(uf, rec, start)
		}
		return true
	})
	return read, ctx.Err()
}

// readLimiter keeps the reads of a backfill to rate bytes a second on average.
type readLimiter struct {
	rate  float64
	start time.Time
	read  int64
}

// wait takes n bytes read and waits until they are within the rate; ctx's error when it ended first.
func (l *readLimiter) wait(ctx context.Context, n int) error {
	l.read += int64(n)
	ahead := time.Duration(float64(l.read)/l.rate*float64(time.Second)) - time.Since(l.start)
	if ahead <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(ahead)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limitedReader reads r through lim, at most backfillRead a read; it ends with ctx.
type limitedReader struct {
	ctx context.Context
	r   io.Reader
	lim *readLimiter
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if len(p) > backfillRead {
		p = p[:backfillRead]
	}
	n, err := r.r.Read(p)
	if werr := r.lim.wait(r.ctx, n); werr != nil && err == nil {
		err = werr
	}
	return n, err
}

// backfillDirs is what the directories buckets name resolve to, for their request (the manager keeps them with the
// hours): each resolved once a run (resolved, nil for one that is not there), here and now, as the backfill is in the
// background anyway.
func (n *Node) backfillDirs(ctx context.Context, buckets []proto.UsageBucket, resolved map[string]*proto.UsageDir) []proto.UsageDir {
	var out []proto.UsageDir
	seen := map[string]bool{}
	for _, b := range buckets {
		if b.Dir == "" || strings.HasPrefix(b.Dir, ":") || seen[b.Dir] {
			continue
		}
		seen[b.Dir] = true
		r, ok := resolved[b.Dir]
		if !ok {
			if got, found := resolveUsageDir(ctx, n.home, b.Dir); found {
				r = &got
			}
			resolved[b.Dir] = r
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}
