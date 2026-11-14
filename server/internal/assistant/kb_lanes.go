package assistant

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/kb"
)

// The knowledge base's background work runs in lanes (#4247), each in its own goroutine, so one lane's slow step (a
// meeting's minutes, a slow model) no longer holds back the others. The lanes are split by what they write, so no two
// write the same thing at once:
//
//   - archive: the sources, compression, project rewrites, recordings' summaries and retention, duplicate projects,
//     Moments links, item tagging and pruning (chats, projects, people, items); its steps keep their order, so the
//     project rewrites see what this run's compression made due;
//   - topics: knowledge points' distill and merge (topics and their sources' marks, under the store's topicMu);
//   - minutes: meeting minutes from recordings (the recordings' minutes, under recMu);
//   - recap: the daily recap of all-day recordings (their notes and recaps, under recMu; kb_day.go), so a long day
//     never holds back a meeting's minutes;
//   - index: the search index (kb/search_index.go; .search/ in the data dir, which only this lane writes). It reads
//     the files changed since its last run; its first run on an existing archive reads them all, which is why it
//     has a lane of its own: search answers meanwhile by reading the files the index has not reached. Not at every
//     kick (kbIndexEvery): a run looks at every file. A run that failed waits longer each time (kbIndexRetry).
//
// The loop (kbLoop) starts a lane when it is due (a round's time, a wake, a kick) and not still running; one still
// running when it became due starts again as soon as it ends, as a round used to after a long one.

// kbLane is one lane: its name (for the log) and one run of its work.
type kbLane struct {
	name string
	run  func(ctx context.Context, e *kb.Engine)

	running bool // only the loop's goroutine reads and writes these two
	due     bool
}

// kbLanes are the loop's lanes and where they say they ended.
type kbLanes struct {
	lanes []*kbLane
	done  chan *kbLane
}

func newKBLanes(lanes ...*kbLane) *kbLanes {
	for _, l := range lanes {
		l.due = true // the first round runs every lane
	}
	return &kbLanes{lanes: lanes, done: make(chan *kbLane, len(lanes))} // one run a lane at a time: never blocks
}

// tick makes every lane due.
func (k *kbLanes) tick() {
	for _, l := range k.lanes {
		l.due = true
	}
}

// busy: some lane is still running (it holds the engine it started with).
func (k *kbLanes) busy() bool {
	for _, l := range k.lanes {
		if l.running {
			return true
		}
	}
	return false
}

// ended takes a lane's end (from done).
func (k *kbLanes) ended(l *kbLane) { l.running = false }

// startDue starts every due lane that is not running, each in its own goroutine through run. prepare runs first, once,
// and only when a lane would start; false: none starts and they stay due.
func (k *kbLanes) startDue(prepare func() bool, run func(l *kbLane)) {
	var start []*kbLane
	for _, l := range k.lanes {
		if l.due && !l.running {
			start = append(start, l)
		}
	}
	if len(start) == 0 || !prepare() {
		return
	}
	for _, l := range start {
		l.due, l.running = false, true
		go func() {
			defer func() { k.done <- l }()
			run(l)
		}()
	}
}

// kbLaneSet is the knowledge base's lanes; r is the archive lane's own bookkeeping (only that lane touches it).
func (s *Service) kbLaneSet(r *kbRounds) *kbLanes {
	indexed := &kbIndexRuns{} // the index lane's bookkeeping (only that lane touches it)
	return newKBLanes(
		&kbLane{name: "archive", run: func(ctx context.Context, e *kb.Engine) { s.kbArchiveLane(ctx, e, r) }},
		&kbLane{name: "topics", run: func(ctx context.Context, e *kb.Engine) {
			// Knowledge points by topic (kb/topics.go; assistant.kb_topics): a couple of distill calls, then a couple of
			// merges, topic workers at a time.
			e.DistillTopics(ctx, 2)
			e.RefreshTopics(ctx, 2)
		}},
		// Meetings' minutes (kb/minutes.go): a recording's can take many calls, so they have their own lane.
		&kbLane{name: "minutes", run: func(ctx context.Context, e *kb.Engine) { e.WriteMinutes(ctx, 2) }},
		// All-day recordings' recaps (kb/day_recap_write.go): one recording's notes or recap a run, and the day's push
		// at its recap time; appends that finished an hour wake it.
		&kbLane{name: "recap", run: func(ctx context.Context, e *kb.Engine) { e.WriteDayRecaps(ctx, 1) }},
		&kbLane{name: "index", run: func(ctx context.Context, e *kb.Engine) { s.kbIndexLane(ctx, e, indexed) }},
	)
}

const (
	// kbIndexEvery is the least time between two index runs that only follow the files: a run stats every file of
	// the archive (half a second at four million lines), and live messages kick a round every few seconds. A file
	// the index has not read yet costs a search one read, so nothing waits for it. A first build, an index that
	// failed a check and text deleted on purpose (kb.Store.SearchIndexWanted) do not wait.
	kbIndexEvery = time.Minute
	// A run that failed (a full disk, a directory it may not write) is not tried again at the next kick: each try
	// looks at every file and reads a batch before it fails. The next waits kbIndexRetry, twice as long after every
	// failure in a row, up to kbIndexRetryMax, whatever is asked of it meanwhile. Nothing starts a run when the wait
	// is over: it starts at the first round after that (a kick, or the loop's next tick, kbPoll at the latest). The
	// first wait is short because one kind of failure mends itself: an index that fails a check while it is merged
	// is built again by the run after, and search reads every file until then.
	kbIndexRetry    = 15 * time.Second
	kbIndexRetryMax = 5 * time.Minute
	// An index run that read this many files, or took this long, is worth a log line (a first build, the catch-up
	// after an import); the few files of an ordinary round are not.
	kbIndexSayFiles = 1000
	kbIndexSayTook  = 5 * time.Second
)

// kbIndexRuns is what the index lane keeps between its runs.
type kbIndexRuns struct {
	last time.Time // when it last ran
	// After a run that failed: how long the next one waits, how many failed in a row, and the failure the log has
	// last (kbIndexCause): not said again while the runs fail the same way, said when they fail another way.
	wait   time.Duration
	failed int
	said   string
}

// kbIndexLane is one run of the index lane: the search index brought up to date with the files, unless the last
// run was within kbIndexEvery and nothing is asked of this one, or it failed and its wait is not over; it reports
// whether it ran. A run cut short (the manager stops) keeps what it read. The log says counts and times, and an
// error names at most a file of the index: never a file of the archive, never a word.
func (s *Service) kbIndexLane(ctx context.Context, e *kb.Engine, r *kbIndexRuns) bool {
	since := time.Since(r.last)
	if since < r.wait || since < kbIndexEvery && !e.Store.SearchIndexWanted() {
		return false
	}
	stats, err := e.UpdateSearchIndex(ctx)
	r.last = time.Now()
	switch {
	case err != nil && ctx.Err() != nil: // stopped with the manager: neither done nor failed
	case err != nil:
		r.wait = min(max(2*r.wait, kbIndexRetry), kbIndexRetryMax)
		r.failed++
		if cause := kbIndexCause(err); cause != r.said {
			r.said = cause
			longer := ""
			if r.wait < kbIndexRetryMax {
				longer = fmt.Sprintf("; twice as long after each further failure in a row, up to %s", kbIndexRetryMax)
			}
			s.logf("kb: search index: %v (search reads the files it does not have; the next run is no sooner than %s from now, at the first round after that%s; not logged again while it fails the same way)", err, r.wait, longer)
		}
	default:
		if r.failed > 0 {
			s.logf("kb: search index: updating again after %d failed run(s)", r.failed)
		}
		r.wait, r.failed, r.said = 0, 0, ""
		if stats.Rebuilt || stats.Read >= kbIndexSayFiles || stats.Took >= kbIndexSayTook {
			s.logf("kb: search index: read %d of %d file(s) in %s (waiting %d, dropped %d, merged %v, started again %v)",
				stats.Read, stats.Files, stats.Took.Round(time.Millisecond), stats.Waiting, stats.Dropped, stats.Merged, stats.Rebuilt)
		}
	}
	return true
}

// kbIndexCause is what tells one failure of the index from another: what was being done, to which kind of file
// of the index, and what the system said. The operation and the file count: "permission denied" when the index's
// directory is made and "permission denied" when its manifest is written are two failures, and the second is worth
// a line after the first was put right. The file is its kind (kb.SearchIndexFileKind), not its name: a new segment,
// log or temporary file has another name at every try, and the same failure would be another one each time. An
// error that names no file is told by its innermost text.
func kbIndexCause(err error) string {
	// the text of the error err wraps last
	innermost := func(err error) string {
		for {
			next := errors.Unwrap(err)
			if next == nil {
				return err.Error()
			}
			err = next
		}
	}
	var at *fs.PathError
	var link *os.LinkError
	var call *os.SyscallError
	switch {
	case errors.As(err, &at):
		return at.Op + " " + kb.SearchIndexFileKind(at.Path) + ": " + innermost(at.Err)
	case errors.As(err, &link):
		return link.Op + " " + kb.SearchIndexFileKind(link.New) + ": " + innermost(link.Err)
	case errors.As(err, &call):
		return call.Syscall + ": " + innermost(call.Err)
	}
	return innermost(err)
}

// kbRunLane runs one go of l with e, under kbRun, and logs what it did when it called the model or took longer than a
// round: how long, how many calls, how many failed and how many were put off (a paused connection).
func (s *Service) kbRunLane(ctx context.Context, e *kb.Engine, l *kbLane) {
	st := &kbLaneStats{}
	start := time.Now()
	s.kbRun(ctx, func() { l.run(context.WithValue(ctx, kbLaneStatsKey{}, st), e) })
	took := time.Since(start)
	if calls := st.calls.Load(); calls > 0 || took >= kbPoll {
		s.logf("kb: %s lane: %s, %d model call(s), %d failed, %d put off", l.name, took.Round(time.Second), calls, st.failed.Load(), st.putOff.Load())
	}
}

// kbLaneStats counts a lane run's model calls (kbCountedModel, through the run's context).
type kbLaneStats struct{ calls, failed, putOff atomic.Int32 }

type kbLaneStatsKey struct{}

// kbCountedModel counts each call in the lane run its context belongs to; calls from elsewhere (tools, the history
// import) carry none and are not counted. A call cancelled with its run counts as nothing; one that ran out of its own
// time (DeadlineExceeded) failed.
type kbCountedModel struct{ m kb.Model }

func kbCounted(m kb.Model) kb.Model {
	if m == nil {
		return nil
	}
	return kbCountedModel{m}
}

func (c kbCountedModel) JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error {
	err := c.m.JSONMax(ctx, system, user, maxTokens, out)
	if st, ok := ctx.Value(kbLaneStatsKey{}).(*kbLaneStats); ok {
		switch {
		case errors.Is(err, context.Canceled): // the run was stopped (shutdown): neither answered nor failed
		case ai.IsPaused(err):
			st.putOff.Add(1)
		case err != nil:
			st.calls.Add(1)
			st.failed.Add(1)
		default:
			st.calls.Add(1)
		}
	}
	return err
}
