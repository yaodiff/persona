package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/kb"
)

// kbRoundNow does what a round of the loop starts, every lane once, one after another (the loop runs each in its own
// goroutine).
func kbRoundNow(ctx context.Context, s *Service, e *kb.Engine) {
	lanes := s.kbLaneSet(&kbRounds{})
	if !s.kbPrepare(ctx, e) {
		return
	}
	for _, l := range lanes.lanes {
		s.kbRunLane(ctx, e, l)
	}
}

// waitEnded takes the next lane end from k, failing the test after a few seconds.
func waitEnded(t *testing.T, k *kbLanes) *kbLane {
	t.Helper()
	select {
	case l := <-k.done:
		k.ended(l)
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no lane ended")
		return nil
	}
}

// A slow lane does not hold back another (#4247): while one run of the slow lane goes on, the quick lane runs again at
// every round; the slow one is not started a second time, and runs again once it ends because it became due meanwhile.
func TestKBLaneSlowDoesNotHoldBackAnother(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{}, 2)
	var slowRuns, quickRuns atomic.Int32
	slow := &kbLane{name: "slow", run: func(context.Context, *kb.Engine) { slowRuns.Add(1); started <- struct{}{}; <-release }}
	quick := &kbLane{name: "quick", run: func(context.Context, *kb.Engine) { quickRuns.Add(1) }}
	k := newKBLanes(slow, quick)
	run := func(l *kbLane) { l.run(context.Background(), nil) }
	ready := func() bool { return true }

	k.startDue(ready, run)
	<-started
	if l := waitEnded(t, k); l != quick {
		t.Fatalf("%s ended first", l.name)
	}
	for round := 2; round <= 3; round++ {
		k.tick()
		k.startDue(ready, run)
		if l := waitEnded(t, k); l != quick || quickRuns.Load() != int32(round) {
			t.Fatalf("round %d: %s ended, quick ran %d time(s)", round, l.name, quickRuns.Load())
		}
	}
	if slowRuns.Load() != 1 || !slow.running || !slow.due {
		t.Fatalf("slow: %d run(s), running %v, due %v; want 1, true, true", slowRuns.Load(), slow.running, slow.due)
	}
	if !k.busy() {
		t.Fatal("not busy while the slow lane runs")
	}
	close(release)
	if l := waitEnded(t, k); l != slow {
		t.Fatalf("%s ended", l.name)
	}
	k.startDue(ready, run) // it became due while it ran: it starts again at once
	if l := waitEnded(t, k); l != slow || slowRuns.Load() != 2 {
		t.Fatalf("%s ended, slow ran %d time(s)", l.name, slowRuns.Load())
	}
	if k.busy() {
		t.Fatal("busy with every lane ended")
	}
}

// Nothing due: prepare is not called. prepare false: no lane starts and they stay due for the next round.
func TestKBLanePrepare(t *testing.T) {
	var runs, prepared atomic.Int32
	l := &kbLane{name: "one", run: func(context.Context, *kb.Engine) { runs.Add(1) }}
	k := newKBLanes(l)
	run := func(l *kbLane) { l.run(context.Background(), nil) }
	k.startDue(func() bool { prepared.Add(1); return false }, run)
	if prepared.Load() != 1 || runs.Load() != 0 || !l.due || l.running {
		t.Fatalf("prepared %d, ran %d, due %v, running %v", prepared.Load(), runs.Load(), l.due, l.running)
	}
	k.startDue(func() bool { prepared.Add(1); return true }, run)
	waitEnded(t, k)
	k.startDue(func() bool { prepared.Add(1); return true }, run) // not due
	if prepared.Load() != 2 || runs.Load() != 1 {
		t.Fatalf("prepared %d, ran %d; want 2, 1", prepared.Load(), runs.Load())
	}
}

type lanesModel struct{ err error }

func (m lanesModel) JSONMax(context.Context, string, string, int, any) error { return m.err }

// A lane run's model calls are counted through its context: answered, failed (an error, its own time running out) and
// put off; a call cancelled with its run counts as nothing, and calls outside a lane run are not counted.
func TestKBCountedModel(t *testing.T) {
	st := &kbLaneStats{}
	ctx := context.WithValue(context.Background(), kbLaneStatsKey{}, st)
	paused := &ai.PausedError{Pause: ai.Pause{Reason: ai.ReasonBusy, Until: time.Now()}}
	for _, err := range []error{nil, nil, errors.New("example"), paused, context.Canceled, context.DeadlineExceeded} {
		_ = kbCounted(lanesModel{err: err}).JSONMax(ctx, "", "", 1, nil)
	}
	_ = kbCounted(lanesModel{}).JSONMax(context.Background(), "", "", 1, nil)
	if st.calls.Load() != 4 || st.failed.Load() != 2 || st.putOff.Load() != 1 {
		t.Fatalf("calls %d, failed %d, put off %d; want 4, 2, 1", st.calls.Load(), st.failed.Load(), st.putOff.Load())
	}
	if kbCounted(nil) != nil {
		t.Fatal("a missing model became one")
	}
}

// The index lane (the search index, kb/search_index.go) builds the index on its first run and then follows the
// files at most every kbIndexEvery, since a run looks at every file and a live message kicks a round every few
// seconds; text deleted on purpose does not wait, so the index never keeps it past the next round.
func TestKBIndexLaneRunsWhenThereIsSomethingToDo(t *testing.T) {
	s := newKBService(t, newFakeHub())
	e := kbEng(t, s)
	ctx := context.Background()
	r := &kbIndexRuns{}
	if e.Store.SearchIndexReady() || !e.Store.SearchIndexWanted() {
		t.Fatal("a new archive says it has a search index")
	}
	if !s.kbIndexLane(ctx, e, r) || !e.Store.SearchIndexReady() || r.last.IsZero() {
		t.Fatal("the first run built no index")
	}
	if s.kbIndexLane(ctx, e, r) {
		t.Fatal("ran again at once with nothing asked of it")
	}
	e.Store.SearchIndexPurge()
	if !s.kbIndexLane(ctx, e, r) || e.Store.SearchIndexWanted() {
		t.Fatal("text was deleted on purpose and the index waited")
	}
	r.last = r.last.Add(-kbIndexEvery)
	if !s.kbIndexLane(ctx, e, r) {
		t.Fatalf("did not run %s after its last run", kbIndexEvery)
	}
	// The loop's lanes have it, and a round runs it.
	var lane *kbLane
	for _, l := range s.kbLaneSet(&kbRounds{}).lanes {
		if l.name == "index" {
			lane = l
		}
	}
	if lane == nil {
		t.Fatal("the knowledge base has no index lane")
	}
	if err := os.RemoveAll(filepath.Join(e.Store.Dir, kb.SearchIndexDir)); err != nil {
		t.Fatal(err)
	}
	s.kbRunLane(ctx, e, lane)
	if _, err := os.Stat(filepath.Join(e.Store.Dir, kb.SearchIndexDir, "manifest.json")); err != nil {
		t.Fatalf("the lane wrote no index: %v", err)
	}
}

// oldDay writes a day of a chat into the archive at dir, an hour old: a file the index reads at its next run.
func oldDay(t *testing.T, st *kb.Store, chat, day, text string) {
	t.Helper()
	if err := st.PutChat(&kb.Chat{ID: chat, Name: chat, Kind: "group"}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(st.Dir, kb.KindChat, chat, "messages", day+".md")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// indexManifestWithALog is the manifest of an index that has a log (built in an archive of its own), and the log's
// name: a whole manifest, as this binary writes it, for a test to put where it likes.
func indexManifestWithALog(t *testing.T) (manifest []byte, log string) {
	t.Helper()
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldDay(t, st, "lark-other", "2026-03-01", "# 2026-03-01\n- 09:00 someone: hello\n")
	if _, err := (&kb.Engine{Store: st}).UpdateSearchIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manifest, err = os.ReadFile(filepath.Join(st.Dir, kb.SearchIndexDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	var names struct {
		Log string `json:"log"`
	}
	if err := json.Unmarshal(manifest, &names); err != nil || names.Log == "" {
		t.Fatalf("the manifest names no log (%v): %s", err, manifest)
	}
	return manifest, names.Log
}

// An index run that fails (here: the index's directory cannot be made, as with a full disk or a directory the hub
// may not write) is not tried again at every kick: the next waits, longer after every failure in a row up to
// kbIndexRetryMax, and the log has the failure once however often it comes back. Another failure is said, also one
// with the same system error at another file; so is the run that works again, after which the lane is as it was.
// What a line says of the next run is what happens: no sooner than the wait, at the first round after it.
func TestKBIndexLaneWaitsAfterARunThatFailed(t *testing.T) {
	s := newKBService(t, newFakeHub())
	e := kbEng(t, s)
	ctx := context.Background()
	var logged []string
	s.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	index := filepath.Join(e.Store.Dir, kb.SearchIndexDir)
	// A file where the index's directory goes: nothing can be written in it, whoever runs the test.
	if err := os.WriteFile(index, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &kbIndexRuns{}
	if !s.kbIndexLane(ctx, e, r) {
		t.Fatal("the first run did not start")
	}
	if len(logged) != 1 || r.failed != 1 || r.wait != kbIndexRetry {
		t.Fatalf("after the first failed run: logged %q, %d failed, waiting %s; want one line, one failure, %s", logged, r.failed, r.wait, kbIndexRetry)
	}
	// Nothing runs "after 15s": the next run is at the first round once the wait is over, and the line says so, with
	// how the wait grows from here.
	for _, want := range []string{"no sooner than 15s from now", "at the first round after that", "twice as long", "up to 5m0s"} {
		if !strings.Contains(logged[0], want) {
			t.Errorf("the first failure's line does not say %q: %q", want, logged[0])
		}
	}
	if strings.Contains(logged[0], "trying again after") {
		t.Errorf("the first failure's line promises a run at a time: %q", logged[0])
	}
	if !e.Store.SearchIndexWanted() {
		t.Fatal("the index is not wanted: the kicks below would not have started a run anyway")
	}
	// Kicks: an index is still wanted, and no run starts until the wait is over.
	for range 5 {
		if s.kbIndexLane(ctx, e, r) {
			t.Fatalf("ran again within %s of a run that failed", r.wait)
		}
	}
	// The wait over, time after time: it grows to kbIndexRetryMax, and the same failure is not said again.
	waits := []time.Duration{}
	for range 8 {
		r.last = r.last.Add(-r.wait)
		if !s.kbIndexLane(ctx, e, r) {
			t.Fatalf("did not run %s after the run that failed", r.wait)
		}
		waits = append(waits, r.wait)
		if s.kbIndexLane(ctx, e, r) {
			t.Fatalf("ran again within %s of a run that failed", r.wait)
		}
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, kbIndexRetryMax, kbIndexRetryMax, kbIndexRetryMax, kbIndexRetryMax}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("the waits after each failure were %v, want %v", waits, want)
	}
	if len(logged) != 1 || r.failed != 9 {
		t.Fatalf("after %d failed runs the log has %d line(s): %q", r.failed, len(logged), logged)
	}
	// Another failure is said: the directory is there now, its manifest cannot be read.
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(index, "manifest.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.last = r.last.Add(-r.wait)
	if !s.kbIndexLane(ctx, e, r) || len(logged) != 2 || logged[1] == logged[0] || r.wait != kbIndexRetryMax {
		t.Fatalf("after another failure: logged %q, waiting %s", logged, r.wait)
	}
	// The wait is as long as it gets: the line says that wait, and nothing of a longer one.
	if l := logged[1]; !strings.Contains(l, "no sooner than 5m0s from now") || strings.Contains(l, "twice as long") || strings.Contains(l, "up to") || strings.Contains(l, "less often") {
		t.Errorf("the line of a failure at the longest wait: %q", l)
	}
	// A third failure, with the system error of the second (a directory where a file goes) at another file: the
	// manifest can be read now, and the log it names cannot. Told apart by the error alone it was the second one
	// again and went unsaid, and the log's last word was a failure that was over (#1273's second review).
	manifest, logName := indexManifestWithALog(t)
	if err := os.Remove(filepath.Join(index, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(index, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(index, logName), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file for the run to write into the log, on a system that reads an empty log from a directory.
	oldDay(t, e.Store, "lark-lane", "2026-03-01", "# 2026-03-01\n- 09:00 someone: hello\n")
	r.last = r.last.Add(-r.wait)
	if !s.kbIndexLane(ctx, e, r) || r.failed != 11 {
		t.Fatalf("the run over a log that is a directory: %d failed runs, want 11; logged %q", r.failed, logged)
	}
	if len(logged) != 3 || !strings.Contains(logged[2], logName) || !strings.Contains(logged[2], "is a directory") || !strings.Contains(logged[1], "is a directory") {
		t.Fatalf("a failure with the same system error at another file is not in the log (or is not that failure): %q", logged)
	}
	// The same once more: not said again.
	r.last = r.last.Add(-r.wait)
	if !s.kbIndexLane(ctx, e, r) || r.failed != 12 || len(logged) != 3 {
		t.Fatalf("the same failure again: %d failed runs, logged %q", r.failed, logged)
	}
	for _, l := range logged {
		if !strings.HasPrefix(l, "kb: search index: ") {
			t.Errorf("the log line %q is not the index's", l)
		}
	}
	// It works again: said once, the index is built, and the lane is back to its ordinary pace.
	if err := os.RemoveAll(index); err != nil {
		t.Fatal(err)
	}
	if s.kbIndexLane(ctx, e, r) {
		t.Fatal("ran before its wait was over")
	}
	r.last = r.last.Add(-r.wait)
	if !s.kbIndexLane(ctx, e, r) || !e.Store.SearchIndexReady() {
		t.Fatal("the run after the directory was put right built no index")
	}
	if r.wait != 0 || r.failed != 0 || r.said != "" {
		t.Fatalf("after a run that worked: %+v", r)
	}
	if n := len(logged); n < 4 || !strings.Contains(logged[3], "updating again after 12 failed run(s)") {
		t.Fatalf("the log after the run that worked: %q", logged)
	}
	if s.kbIndexLane(ctx, e, r) {
		t.Fatal("ran again at once with nothing asked of it")
	}
	// A run stopped with the manager is not a failure: nothing waits because of it.
	e.Store.SearchIndexPurge()
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	said := len(logged)
	if !s.kbIndexLane(stopped, e, r) || r.wait != 0 || r.failed != 0 || len(logged) != said {
		t.Fatalf("after a run stopped with the manager: %+v, logged %q", r, logged[said:])
	}
}

// What tells one failure of the index from another (kbIndexCause) is the operation, the file and the error: the
// same failure is the same cause at every try, whatever the new file of that try is called, and another operation
// or another file failing with the same system error is another cause (#1273's second review: "mkdir .search:
// permission denied", put right, then "open .search/manifest.json.tmp-…: permission denied").
func TestKBIndexCauseTellsFailuresApart(t *testing.T) {
	dir := filepath.Join("data", kb.SearchIndexDir)
	full := errors.New("no space left on device")
	at := func(op, name string, err error) error {
		return &fs.PathError{Op: op, Path: filepath.Join(dir, name), Err: err}
	}
	rename := func(from, to string, err error) error {
		return &os.LinkError{Op: "rename", Old: filepath.Join(dir, from), New: filepath.Join(dir, to), Err: err}
	}
	for _, same := range [][2]error{
		{at("open", "manifest.json.tmp-1234", fs.ErrPermission), at("open", "manifest.json.tmp-987654321", fs.ErrPermission)},
		{at("open", "seg-000001-0123456789abcdef.idx", full), fmt.Errorf("the second batch: %w", at("open", "seg-000002-fedcba9876543210.idx", full))},
		{at("open", "seg-000001-0123456789abcdef.idx.tmp", full), at("open", "seg-000017-00000000000000ff.idx.tmp", full)},
		{at("open", "log-000003-0123456789abcdef.log", fs.ErrPermission), at("open", "log-000009-fedcba9876543210.log", fs.ErrPermission)},
		{rename("manifest.json.tmp-1", "manifest.json", fs.ErrPermission), rename("manifest.json.tmp-22", "manifest.json", fs.ErrPermission)},
		{&fs.PathError{Op: "mkdir", Path: dir, Err: fs.ErrPermission}, &fs.PathError{Op: "mkdir", Path: filepath.Join("elsewhere", kb.SearchIndexDir), Err: fs.ErrPermission}},
		{errors.New("a check failed"), fmt.Errorf("while merging: %w", errors.New("a check failed"))},
	} {
		if a, b := kbIndexCause(same[0]), kbIndexCause(same[1]); a != b {
			t.Errorf("one failure at two tries is two causes:\n %q (%v)\n %q (%v)", a, same[0], b, same[1])
		}
	}
	causes := map[string]error{}
	for _, err := range []error{
		&fs.PathError{Op: "mkdir", Path: dir, Err: fs.ErrPermission},
		at("open", "manifest.json.tmp-1234", fs.ErrPermission),
		at("open", "manifest.json.tmp-1234", full),
		at("open", "manifest.json", fs.ErrPermission),
		at("read", "manifest.json", fs.ErrPermission),
		at("open", "seg-000001-0123456789abcdef.idx", fs.ErrPermission),
		at("open", "seg-000001-0123456789abcdef.idx.tmp", fs.ErrPermission),
		at("open", "log-000003-0123456789abcdef.log", fs.ErrPermission),
		at("read", "log-000003-0123456789abcdef.log", fs.ErrPermission),
		rename("manifest.json.tmp-1", "manifest.json", fs.ErrPermission),
		&os.SyscallError{Syscall: "fsync", Err: full},
		full,
		errors.New("a check failed"),
	} {
		cause := kbIndexCause(err)
		if other, ok := causes[cause]; ok {
			t.Errorf("two failures are one cause, %q:\n %v\n %v", cause, other, err)
		}
		causes[cause] = err
		if strings.Contains(cause, "data") || strings.Contains(cause, "0123456789abcdef") || strings.Contains(cause, "1234") {
			t.Errorf("the cause %q has the directory, or what is new in a name at every try", cause)
		}
	}
}
