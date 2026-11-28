package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// installCountedDBGlobs routes both numbered-database Globs through counters for patterns in home (or its real path),
// and returns restore/cleanup via t.Cleanup.
func installCountedDBGlobs(t *testing.T, home string) (history, state *atomic.Int64) {
	t.Helper()
	history, state = &atomic.Int64{}, &atomic.Int64{}
	realHome := realPath(home)

	counting := func(counter *atomic.Int64, inner func(string) ([]string, error)) func(string) ([]string, error) {
		return func(pattern string) ([]string, error) {
			if dir := filepath.Dir(pattern); dir == home || dir == realHome {
				counter.Add(1)
			}
			return inner(pattern)
		}
	}
	oldNumbered := codexNumberedDBGlob
	oldApp := codexAppDBGlob
	codexNumberedDBGlob = counting(history, oldNumbered)
	codexAppDBGlob = counting(state, oldApp)
	t.Cleanup(func() {
		codexNumberedDBGlob = oldNumbered
		codexAppDBGlob = oldApp
	})
	return history, state
}

// installMissingSQLite makes every injected LookPath report sqlite3 absent and counts the real lookup attempts.
func installMissingSQLite(t *testing.T) *atomic.Int64 {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	sqliteFallbackForTest(t) // no fallback directories
	resetCodexSQLiteProgramCache()
	t.Cleanup(resetCodexSQLiteProgramCache)
	var lookups atomic.Int64
	old := codexSQLiteLookPath
	codexSQLiteLookPath = func(string) (string, error) {
		lookups.Add(1)
		return "", codexSQLiteNotFound()
	}
	t.Cleanup(func() { codexSQLiteLookPath = old })
	return &lookups
}

// Repeated codexSQLiteProgram calls reuse one LookPath result, including the ErrNotFound verdict, until the TTL passes;
// once sqlite3 "is installed" and the negative TTL elapses, lookup recovers the new path.
func TestCodexSQLiteProgramNegativeCacheAndRecovery(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	sqliteFallbackForTest(t)
	resetCodexSQLiteProgramCache()
	t.Cleanup(resetCodexSQLiteProgramCache)

	now := time.Unix(1_700_000_000, 0)
	oldNow := codexSQLiteNow
	codexSQLiteNow = func() time.Time { return now }
	t.Cleanup(func() { codexSQLiteNow = oldNow })

	var lookups int
	var installed atomic.Bool
	oldLook := codexSQLiteLookPath
	codexSQLiteLookPath = func(file string) (string, error) {
		lookups++
		if installed.Load() && file == "sqlite3" {
			return "/usr/local/bin/sqlite3", nil
		}
		return "", codexSQLiteNotFound()
	}
	t.Cleanup(func() { codexSQLiteLookPath = oldLook })

	for range 10 {
		if _, err := codexSQLiteProgram(); !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("err = %v, want exec.ErrNotFound while sqlite3 is absent", err)
		}
	}
	if lookups != 1 {
		t.Fatalf("LookPath ran %d times for 10 calls inside the TTL, want 1", lookups)
	}

	now = now.Add(codexSQLiteProgramTTL / 2) // still inside the TTL
	if _, err := codexSQLiteProgram(); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err = %v, want the cached not-found verdict", err)
	}
	if lookups != 1 {
		t.Fatalf("LookPath ran %d times before the TTL elapsed, want still 1", lookups)
	}

	installed.Store(true) // sqlite3 is installed partway; the bounded negative TTL is what re-checks PATH.
	now = now.Add(codexSQLiteProgramTTL)
	path, err := codexSQLiteProgram()
	if err != nil || path != "/usr/local/bin/sqlite3" {
		t.Fatalf("after the TTL: path = %q, err = %v; want the newly installed program", path, err)
	}
	if lookups != 2 {
		t.Fatalf("LookPath ran %d times after TTL recovery, want a second lookup", lookups)
	}

	for range 5 {
		if p, err := codexSQLiteProgram(); err != nil || p != "/usr/local/bin/sqlite3" {
			t.Fatalf("positive cache: path = %q err = %v", p, err)
		}
	}
	if lookups != 2 {
		t.Fatalf("the found path was looked up again: %d lookups, want 2", lookups)
	}
}

// The non-fresh refresh TTL must return the cached map before either numbered-database Glob. This home has a history
// database but no state database, so the old order globbed both on every call and returned at the empty state path.
func TestCodexPaginatedThreadsTTLBeforeGlobs(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "thread_history_1.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	historyGlobs, stateGlobs := installCountedDBGlobs(t, home)

	codexPagedDBs.Lock()
	delete(codexPagedDBs.byHome, home)
	codexPagedDBs.Unlock()
	t.Cleanup(func() {
		codexPagedDBs.Lock()
		delete(codexPagedDBs.byHome, home)
		codexPagedDBs.Unlock()
	})

	for range 20 {
		codexPaginatedThreadsMode(home, false)
	}
	if got := historyGlobs.Load(); got > 1 {
		t.Fatalf("thread_history glob ran %d times for 20 calls inside the TTL, want at most 1 (TTL must precede the Glob)", got)
	}
	if got := stateGlobs.Load(); got > 1 {
		t.Fatalf("state glob ran %d times for 20 calls inside the TTL, want at most 1 (TTL must precede the Glob)", got)
	}
}

// With sqlite3 absent, even fresh callers back off for a short TTL so repeated history refreshes do not re-Glob and
// re-run LookPath. Advancing the clock past both negative TTLs re-attempts the lookup.
func TestCodexPaginatedMissingSQLiteBackoff(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state_5.sqlite", "thread_history_1.sqlite"} {
		if err := os.WriteFile(filepath.Join(home, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	historyGlobs, _ := installCountedDBGlobs(t, home)
	lookups := installMissingSQLite(t)

	pagedNow := time.Unix(1_700_000_000, 0)
	oldPagedNow := codexPagedNow
	codexPagedNow = func() time.Time { return pagedNow }
	t.Cleanup(func() { codexPagedNow = oldPagedNow })

	codexPagedDBs.Lock()
	delete(codexPagedDBs.byHome, home)
	codexPagedDBs.Unlock()

	for range 20 { // fresh=true bypasses the ordinary refresh TTL but not the missing-sqlite backoff
		codexPaginatedThreadsMode(home, true)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("LookPath ran %d times for 20 fresh calls with sqlite3 absent, want 1", got)
	}
	if got := historyGlobs.Load(); got > 1 {
		t.Fatalf("history glob ran %d times for 20 fresh calls during the backoff, want at most 1", got)
	}

	// Past the paged backoff but inside the program negative TTL: the paged layer re-globs, while the program cache
	// still answers ErrNotFound without a new LookPath.
	pagedNow = pagedNow.Add(codexPagedSQLiteBackoff + time.Second)
	beforeGlobs := historyGlobs.Load()
	codexPaginatedThreadsMode(home, true)
	if historyGlobs.Load() <= beforeGlobs {
		t.Fatal("after the backoff elapsed a fresh call should re-check the database Glob")
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("the program negative cache should still bound LookPath, got %d lookups", got)
	}
}

// A single list pass over hundreds of mixed Claude/Agy sessions must not ask TRAE's snapshot once per row: fresh cached
// non-TRAE paths answer before the TRAE probe. Across several list rounds the database Globs and sqlite3 LookPath stay
// a small constant too.
func TestSessionListInMixedSessionsConstantTraexLookups(t *testing.T) {
	const nSessions = 500
	const rounds = 3

	n, _, home := herdrNode(t)
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.configureCodex(proto.CodexConfig{})

	traexHome := n.traexHome()
	if err := os.MkdirAll(traexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state_5.sqlite", "thread_history_1.sqlite"} {
		if err := os.WriteFile(filepath.Join(traexHome, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	historyGlobs, stateGlobs := installCountedDBGlobs(t, traexHome)
	lookups := installMissingSQLite(t)

	var snapshotLoads atomic.Int64
	oldLoader := traexThreadsForHome
	traexThreadsForHome = func(h string) map[string]codexAppThread {
		if h == traexHome || h == realPath(traexHome) {
			snapshotLoads.Add(1)
		}
		return oldLoader(h)
	}
	t.Cleanup(func() { traexThreadsForHome = oldLoader })

	claudeDir := filepath.Join(home, ".claude", "projects", "work")
	agyDir := filepath.Join(home, "agy-brain")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	n.mu.Lock()
	for i := range nSessions {
		id := fmt.Sprintf("0e000000-0000-4000-8000-%012x", i+1)
		agent := "claude"
		var transcript string
		if i%5 == 0 { // one fifth Antigravity rows, whose cached file is not named <id>.jsonl
			agent = agyAgent
			transcript = filepath.Join(agyDir, fmt.Sprintf("conv-%03d", i), "transcript_full.jsonl")
		} else {
			transcript = filepath.Join(claudeDir, id+".jsonl")
		}
		if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
			n.mu.Unlock()
			t.Fatal(err)
		}
		if err := os.WriteFile(transcript, nil, 0o600); err != nil {
			n.mu.Unlock()
			t.Fatal(err)
		}
		n.agents[id] = host.Agent{Agent: agent, AgentStatus: proto.StatusIdle, Cwd: home}
		n.paths[id] = transcript
		if n.pathsAt == nil {
			n.pathsAt = map[string]time.Time{}
		}
		n.pathsAt[id] = time.Now()
	}
	n.mu.Unlock()

	for range rounds {
		pass := newCodexListPass()
		rows := n.sessionListIn(context.Background(), false, pass)
		if len(rows) < nSessions {
			t.Fatalf("sessionListIn returned %d rows, want at least the %d agents", len(rows), nSessions)
		}
	}

	// The TRAE snapshot is built by the TRAE offline listing about once per round, never once per non-TRAE row.
	if got := snapshotLoads.Load(); got > int64(rounds*2+2) {
		t.Fatalf("TRAE snapshot loaded %d times for %d mixed sessions x %d rounds, want a small constant (per-row n.traexThread fan-out?)",
			got, nSessions, rounds)
	}
	if got := lookups.Load(); got > 1 {
		t.Fatalf("sqlite3 LookPath ran %d times across all rounds, want 1", got)
	}
	// The cold first pass resolves the two numbered databases once; the missing-sqlite backoff and refresh TTL skip
	// every later Glob across all rounds.
	if got := historyGlobs.Load(); got > 2 {
		t.Fatalf("thread_history Glob ran %d times for %d mixed sessions x %d rounds, want a small constant", got, nSessions, rounds)
	}
	if got := stateGlobs.Load(); got > 2 {
		t.Fatalf("state Glob ran %d times for %d mixed sessions x %d rounds, want a small constant", got, nSessions, rounds)
	}
	t.Logf("500 mixed sessions x %d rounds: TRAE snapshot loads=%d LookPath=%d historyGlobs=%d stateGlobs=%d",
		rounds, snapshotLoads.Load(), lookups.Load(), historyGlobs.Load(), stateGlobs.Load())
}

// Two TRAE-thread probes through one pass build the home's snapshot once and reuse pass.paginated.
func TestTraexThreadInReusesPassSnapshot(t *testing.T) {
	n := remoteTestNode(t)
	home := n.traexHome()
	pass := newCodexListPass()
	idA := "0e000000-0000-4000-8000-0000000000a1"
	idB := "0e000000-0000-4000-8000-0000000000b2"
	threads := map[string]codexAppThread{
		idA: {ID: idA, Title: "A"},
		idB: {ID: idB, Title: "B"},
	}
	pass.paginated[home] = threads

	calls := 0
	oldApp := codexAppDBGlob
	codexAppDBGlob = func(pattern string) ([]string, error) {
		calls++
		return oldApp(pattern)
	}
	t.Cleanup(func() { codexAppDBGlob = oldApp })

	if ta, ok := n.traexThreadIn(idA, pass); !ok || ta.Title != "A" || !ta.includeRunning {
		t.Fatalf("thread-a = %+v, %v", ta, ok)
	}
	if tb, ok := n.traexThreadIn(idB, pass); !ok || tb.Title != "B" {
		t.Fatalf("thread-b = %+v, %v", tb, ok)
	}
	if calls != 0 {
		t.Fatalf("a preloaded pass snapshot globbed %d times, want 0", calls)
	}
}

func BenchmarkFindOwnTranscriptPathMixed500(b *testing.B) {
	const nSessions = 500
	n, home := testNode(b)
	n.codices = newCodexRegistry()
	claudeDir := filepath.Join(home, ".claude", "projects", "work")
	agyDir := filepath.Join(home, "agy-brain")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(agyDir, 0o700); err != nil {
		b.Fatal(err)
	}
	ids := make([]string, nSessions)
	pass := newCodexListPass()
	pass.agentKinds = map[string]string{} // populated as sessionListIn does
	for i := range nSessions {
		id := fmt.Sprintf("0e000000-0000-4000-8000-%012x", i+1)
		ids[i] = id
		kind := "claude"
		var transcript string
		if i%5 == 0 {
			kind = agyAgent
			transcript = filepath.Join(agyDir, fmt.Sprintf("conv-%03d", i), "transcript_full.jsonl")
		} else {
			transcript = filepath.Join(claudeDir, id+".jsonl")
		}
		if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(transcript, nil, 0o600); err != nil {
			b.Fatal(err)
		}
		n.agents[id] = host.Agent{Agent: kind}
		n.setPath(id, transcript)
		pass.agentKinds[id] = kind
	}
	b.ResetTimer()
	for range b.N {
		for _, id := range ids {
			if p := n.findOwnTranscriptPath(id, pass); p == "" {
				b.Fatal("empty transcript path")
			}
		}
	}
}
