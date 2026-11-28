package node

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// expireRollouts makes the next rollouts call list again as if 5 seconds (recent days) or a minute (older days too)
// had passed.
func expireRollouts(home string, older bool) {
	c := rolloutCacheFor(home)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
	if older {
		c.olderAt = time.Time{}
	}
}

// ageDirs dates every directory under root an hour back, as directories last changed long before a look.
func ageDirs(t testing.TB, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return os.Chtimes(p, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A thread with no rollout is globbed once, not on every look; a rollout written later is still found: in a recent
// day or archived_sessions at once, in an older day once the older days are listed again (a minute).
func TestRolloutPathMissCached(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	ageDirs(t, codex)
	const old, fresh = "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"

	if p := rolloutPath(codex, old); p != "" {
		t.Fatalf("no rollout yet: %q", p)
	}
	// Written into a day before the window (a resumed old thread's directory) right after the miss.
	writeRollout(t, home, old, "/w", now.AddDate(0, 0, -40), now)
	if p := rolloutPath(codex, old); p != "" {
		t.Fatalf("the miss is kept until the older days are listed again: %q", p)
	}
	expireRollouts(codex, false)
	if p := rolloutPath(codex, old); p != "" {
		t.Fatalf("listing the recent days does not see an older day: %q", p)
	}
	expireRollouts(codex, true)
	if p := rolloutPath(codex, old); filepath.Base(filepath.Dir(p)) != now.AddDate(0, 0, -40).Format("02") {
		t.Fatalf("after the older days are listed again: %q", p)
	}

	ageDirs(t, codex)
	if p := rolloutPath(codex, fresh); p != "" {
		t.Fatalf("no rollout yet: %q", p)
	}
	writeRollout(t, home, fresh, "/w", now, now)
	if p := rolloutPath(codex, fresh); p == "" {
		t.Fatal("a rollout written today is found at once")
	}
}

// A raw-UUID hit outside the window is reused like a miss: another thread's recent rollout does not invalidate it, but
// archiving the thread or losing every cached path does.
func TestRolloutPathHitCached(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	const id = "abababab-abab-4aba-8aba-abababababab"
	day := time.Now().AddDate(0, 0, -40)
	writeRollout(t, home, id, "/w", day, day)
	src := filepath.Join(codex, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"),
		"rollout-"+day.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	ageDirs(t, codex)
	perID, _ := countRolloutGlobs(t, codex)
	if got := rolloutPath(codex, id); got != src {
		t.Fatalf("first hit = %q, want %q", got, src)
	}
	if got := perID.Load(); got != 1 {
		t.Fatalf("first hit used %d per-ID globs, want 1", got)
	}
	if got := rolloutPath(codex, id); got != src {
		t.Fatalf("cached hit = %q, want %q", got, src)
	}
	if got := perID.Load(); got != 1 {
		t.Fatalf("unchanged hit globbed again: %d per-ID globs", got)
	}

	writeRollout(t, home, "cdcdcdcd-cdcd-4cdc-8cdc-cdcdcdcdcdcd", "/w", time.Now(), time.Now())
	if got := rolloutPath(codex, id); got != src {
		t.Fatalf("hit after another recent rollout = %q, want %q", got, src)
	}
	if got := perID.Load(); got != 1 {
		t.Fatalf("another thread's recent rollout invalidated the hit: %d per-ID globs", got)
	}

	dst := filepath.Join(codex, "archived_sessions", filepath.Base(src))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	ageDirs(t, codex)
	if got := rolloutPath(codex, id); got != dst {
		t.Fatalf("archived hit = %q, want %q", got, dst)
	}
	if got := perID.Load(); got != 3 {
		t.Fatalf("archiving did not rescan sessions and archived_sessions: %d per-ID globs", got)
	}
	if got := rolloutPath(codex, id); got != dst {
		t.Fatalf("cached archived hit = %q, want %q", got, dst)
	}
	if got := perID.Load(); got != 3 {
		t.Fatalf("unchanged archived hit globbed again: %d per-ID globs", got)
	}
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if got := rolloutPath(codex, id); got != "" {
		t.Fatalf("lookup after losing the cached path = %q, want missing", got)
	}
	if got := perID.Load(); got != 5 {
		t.Fatalf("a missing cached path did not force both fresh globs: %d per-ID globs", got)
	}
}

// A continuation written beside a cached old rollout changes that directory: the next lookup must select it, not keep
// returning the older segment until the minute listing.
func TestRolloutPathHitSeesNewContinuation(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	day := time.Now().AddDate(0, 0, -40)
	writeRollout(t, home, remoteRaw, "/w", day, day)
	ageDirs(t, codex)
	original := rolloutPath(codex, remoteRaw)
	if original == "" {
		t.Fatal("the original rollout is missing")
	}
	path := strings.TrimSuffix(original, ".jsonl") + "_" + continuationSegment + ".jsonl"
	continuationFixture(t, path, remoteRaw, "")
	if err := os.Chtimes(path, day.Add(time.Hour), day.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := rolloutPath(codex, remoteRaw); got != path {
		t.Fatalf("lookup after a new continuation = %q, want %q", got, path)
	}

	// A segment in a different old day directory is not covered by those parent stamps. The full older-day listing lets
	// the hit go, and the next lookup selects this newer segment too.
	ageDirs(t, codex)
	if got := rolloutPath(codex, remoteRaw); got != path {
		t.Fatalf("lookup before the older-day listing = %q, want %q", got, path)
	}
	otherDay := day.AddDate(0, 0, 1)
	otherDir := filepath.Join(codex, "sessions", otherDay.Format("2006"), otherDay.Format("01"), otherDay.Format("02"))
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(otherDir, "rollout-other-"+remoteRaw+"_bbbbbbbb-cccc-dddd-eeee-ffffffffffff.jsonl")
	continuationFixture(t, other, remoteRaw, "")
	if err := os.Chtimes(other, otherDay.Add(2*time.Hour), otherDay.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expireRollouts(codex, true)
	if got := rolloutPath(codex, remoteRaw); got != other {
		t.Fatalf("lookup after the older-day listing = %q, want %q", got, other)
	}
}

// A rollout archived after a miss (moved out of sessions/) is found at once.
func TestRolloutPathArchived(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	const id = "44444444-4444-4444-4444-444444444444"
	day := time.Now().AddDate(0, 0, -60)
	writeRollout(t, home, id, "/w", day, day)
	src := filepath.Join(codex, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"),
		"rollout-"+day.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	dst := filepath.Join(codex, "archived_sessions", filepath.Base(src))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	ageDirs(t, codex)
	const other = "55555555-5555-5555-5555-555555555555"
	if p := rolloutPath(codex, other); p != "" {
		t.Fatalf("no rollout: %q", p)
	}
	// The archived rollout's metadata must identify the thread being looked up as well as its filename.
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte(strings.ReplaceAll(string(data), id, other)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, filepath.Join(filepath.Dir(dst), "rollout-"+day.Format("2006-01-02T15-04-05")+"-"+other+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if p := rolloutPath(codex, other); p == "" {
		t.Fatal("archived after the miss: not found")
	}
}

// A miss looked up while a directory had just changed is not kept: within its mtime's granularity, a file created next
// may leave the mtime as it was.
func TestRolloutPathMissRacy(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	dir := filepath.Join(codex, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	tick := time.Now()
	if err := os.Chtimes(dir, tick, tick); err != nil {
		t.Fatal(err)
	}
	const id = "66666666-6666-6666-6666-666666666666"
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("no rollout yet: %q", p)
	}
	writeRollout(t, home, id, "/w", now, now)
	if err := os.Chtimes(dir, tick, tick); err != nil { // the same clock tick: the mtime did not move
		t.Fatal(err)
	}
	if p := rolloutPath(codex, id); p == "" {
		t.Fatal("written in the same tick as the miss: not found")
	}
}

// BenchmarkRolloutPathHit measures one old raw-UUID hit among 5,000 rollouts. Cached matches still need their file and
// metadata checks, but must not glob every historical day directory again.
func BenchmarkRolloutPathHit(b *testing.B) {
	home := b.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	const id = "abababab-abab-4aba-8aba-abababababab"
	for i := range 5000 {
		day := now.AddDate(0, 0, -(20 + i%300))
		raw := fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i)
		if i == 0 {
			raw = id
		}
		writeRollout(b, home, raw, "/w", day, day)
	}
	ageDirs(b, codex)
	if got := rolloutPath(codex, id); got == "" {
		b.Fatal("the benchmark rollout is missing")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if got := rolloutPath(codex, id); got == "" {
			b.Fatal("the benchmark rollout is missing")
		}
	}
}

// BenchmarkRolloutPathMiss: one look for a thread with no rollout, in a home of 200 rollouts over 70 days (the
// session list does this for every such thread every second).
func BenchmarkRolloutPathMiss(b *testing.B) {
	home := b.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	for i := range 200 {
		day := now.AddDate(0, 0, -(i % 70))
		writeRollout(b, home, fmt.Sprintf("%08d-0000-0000-0000-000000000000", i), "/w", day, day)
	}
	ageDirs(b, codex)
	const missing = "99999999-9999-9999-9999-999999999999"
	rolloutPath(codex, missing)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if rolloutPath(codex, missing) != "" {
			b.Fatal("found")
		}
	}
}

// countRolloutGlobs counts, until the test ends, rolloutPath's globs under home: perID for one thread's id
// (rollout-*-<id>*.jsonl*, across history), listings for one directory a new rollout appears in (rollout-*.jsonl*;
// the trailing * takes in a rollout Codex compressed to .jsonl.zst, #17768).
func countRolloutGlobs(t *testing.T, home string) (perID, listings *atomic.Int64) {
	t.Helper()
	perID, listings = new(atomic.Int64), new(atomic.Int64)
	glob := rolloutGlob
	rolloutGlob = func(pattern string) ([]string, error) {
		if strings.HasPrefix(pattern, home+string(filepath.Separator)) {
			if filepath.Base(pattern) == "rollout-*.jsonl*" {
				listings.Add(1)
			} else {
				perID.Add(1)
			}
		}
		return glob(pattern)
	}
	t.Cleanup(func() { rolloutGlob = glob })
	return perID, listings
}

// Old threads are globbed for once: those with no rollout (an app-server thread never wrote one), and those whose
// rollout Codex compressed to .jsonl.zst, which this node reads since #17768. Not again each minute when the older days
// are listed (528 compressed threads took 14 s of CPU every minute, #17809), nor when another thread's rollout is
// written: the directories it appears in are listed once per list pass instead.
func TestRolloutPathMissesOutliveRelisting(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now.AddDate(0, 0, -40), now.AddDate(0, 0, -40))
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	var ids []string
	want := map[string]string{} // id → its compressed rollout; none for a thread with no rollout
	for i := range 50 {
		id := fmt.Sprintf("%08x-aaaa-4aaa-8aaa-aaaaaaaaaaaa", i)
		ids = append(ids, id)
		day := now.AddDate(0, 0, -30-i)
		dir := filepath.Join(codex, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			continue
		}
		// What Codex leaves of an old rollout it compressed, as old as the day it started.
		zst := filepath.Join(dir, "rollout-"+day.Format("2006-01-02T15-04-05")+"-"+id+".jsonl.zst")
		meta := `{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/w"}}` + "\n"
		if err := os.WriteFile(zst, encoder.EncodeAll([]byte(meta), nil), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(zst, day, day); err != nil {
			t.Fatal(err)
		}
		want[id] = zst
	}
	ageDirs(t, codex)
	perID, listings := countRolloutGlobs(t, codex)
	list := func(when string, wantPerID, wantListings int64) {
		t.Helper()
		perID.Store(0)
		listings.Store(0)
		pass := newCodexListPass()
		for _, id := range ids {
			for range 2 { // the list asks twice for each: listedCodexSource, then the transcript lookup
				if p := rolloutPathIn(codex, id, pass); p != want[id] {
					t.Fatalf("%s: %s got %q, want %q", when, id, p, want[id])
				}
			}
		}
		if perID.Load() != wantPerID || listings.Load() != wantListings {
			t.Fatalf("%s: %d globs for a thread's id and %d directory listings, want %d and %d", when, perID.Load(),
				listings.Load(), wantPerID, wantListings)
		}
	}
	dirs := int64(len(missDirs(codex, now)))
	none := len(ids) - len(want)
	list("first look", int64(2*none+len(want)), 0) // no rollout: sessions/, then archived_sessions/; compressed: sessions/
	list("second look", 0, 0)
	expireRollouts(codex, true)
	list("after the older days were listed again", 0, 0)

	writeRollout(t, home, "22222222-2222-2222-2222-222222222222", "/w", now, now)
	list("just after another thread's rollout was written today", 0, dirs)
	list("in the next pass, the directory still within its mtime's granularity", 0, dirs)
	// Today's directory, the one that changed, has not changed since for an hour. Only it: re-dating the old day
	// directories would look like a change to the compressed rollouts' own directories.
	ageDirs(t, filepath.Join(codex, "sessions", now.Format("2006"), now.Format("01"), now.Format("02")))
	list("once the directories settled", 0, dirs)
	list("and after that", 0, 0)
	if p := rolloutPath(codex, "22222222-2222-2222-2222-222222222222"); p == "" {
		t.Fatal("the new rollout is not found")
	}
}

// A miss nobody asked about for a while is let go when the older days are listed, so the misses kept are those of
// threads still listed; two listings close together (a rewind lists them again at once) keep every recent one.
func TestRolloutPathMissesNotAskedAboutAreLetGo(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	ageDirs(t, codex)
	const asked, forgotten = "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"
	rolloutPath(codex, asked)
	rolloutPath(codex, forgotten)
	cache := rolloutCacheFor(codex)
	misses := func() []string {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		var out []string
		for id := range cache.misses {
			out = append(out, id)
		}
		slices.Sort(out)
		return out
	}
	for range 2 {
		expireRollouts(codex, true)
		rollouts(codex)
	}
	if got := misses(); len(got) != 2 {
		t.Fatalf("misses after two listings in a row = %v, want both", got)
	}
	cache.mu.Lock()
	m := cache.misses[forgotten]
	m.asked = m.asked.Add(-missKeptFor - time.Minute)
	cache.misses[forgotten] = m
	cache.mu.Unlock()
	expireRollouts(codex, true)
	rollouts(codex)
	if got := misses(); len(got) != 1 || got[0] != asked {
		t.Fatalf("misses = %v, want only %s", got, asked)
	}
}

// A rollout unarchived between rolloutPath's two globs (out of archived_sessions, into an older day) is in neither:
// no miss is kept on that look, and the next finds it.
func TestRolloutPathMovedDuringTheLook(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	const id = "88888888-8888-8888-8888-888888888888"
	day := time.Now().AddDate(0, 0, -40)
	writeRollout(t, home, id, "/w", day, day)
	src := filepath.Join(codex, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"),
		"rollout-"+day.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	dst := filepath.Join(codex, "archived_sessions", filepath.Base(src))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	ageDirs(t, codex)
	glob := rolloutGlob
	t.Cleanup(func() { rolloutGlob = glob })
	rolloutGlob = func(pattern string) ([]string, error) {
		if strings.Contains(pattern, "archived_sessions") && strings.Contains(pattern, id) {
			if err := os.Rename(dst, src); err != nil { // unarchived after sessions/ was globbed
				t.Error(err)
			}
		}
		return glob(pattern)
	}
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("moved while looked up: %q", p)
	}
	rolloutGlob = glob
	if p := rolloutPath(codex, id); p != src {
		t.Fatalf("after the move: %q, want %q", p, src)
	}
}

// A thread with a miss whose rollout is written in the same clock tick as a directory change just before is found:
// names listed while a directory had just changed are not kept for the next look.
func TestRolloutPathMissRacyAfterAMiss(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	ageDirs(t, codex)
	const id = "67676767-6767-6767-6767-676767676767"
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("no rollout yet: %q", p)
	}
	dir := filepath.Join(codex, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	tick := time.Now()
	if err := os.Chtimes(dir, tick, tick); err != nil { // another file came and went just now
		t.Fatal(err)
	}
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("still no rollout: %q", p)
	}
	writeRollout(t, home, id, "/w", now, now)
	if err := os.Chtimes(dir, tick, tick); err != nil { // the same clock tick: the mtime did not move
		t.Fatal(err)
	}
	if p := rolloutPath(codex, id); p == "" {
		t.Fatal("written in the same tick as the change before: not found")
	}
}

// A rollout of a thread with a miss that shows up in an older day, last written long ago (restored from a backup), is
// found once the older days are listed again: the listing has its name and lets the miss go.
func TestRolloutPathMissLetGoByTheListing(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	ageDirs(t, codex)
	const id = "56565656-5656-5656-5656-565656565656"
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("no rollout yet: %q", p)
	}
	old := now.AddDate(0, 0, -40)
	writeRollout(t, home, id, "/w", old, old) // too old for rollouts' own list
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("the miss is kept until the older days are listed again: %q", p)
	}
	expireRollouts(codex, true)
	if p := rolloutPath(codex, id); p == "" {
		t.Fatal("after the older days were listed again: not found")
	}
}

// Unarchiving puts a rollout back in its day directory, which may be older than the window: it is found there at once
// (codexArchive checks the move right after Codex answers).
func TestRolloutPathUnarchivedIntoAnOlderDay(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	const id = "77777777-7777-7777-7777-777777777777"
	day := time.Now().AddDate(0, 0, -40)
	writeRollout(t, home, id, "/w", day, day)
	src := filepath.Join(codex, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"),
		"rollout-"+day.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	dst := filepath.Join(codex, "archived_sessions", filepath.Base(src))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	ageDirs(t, codex)
	if p := rolloutPath(codex, id); p != src {
		t.Fatalf("before archiving: %q", p)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	if p := rolloutPath(codex, id); p != dst {
		t.Fatalf("archived: %q", p)
	}
	if err := os.Rename(dst, src); err != nil {
		t.Fatal(err)
	}
	if p := rolloutPath(codex, id); p != src {
		t.Fatalf("unarchived into its day directory: %q", p)
	}
}

// A lookup whose look was taken before a listing of every directory does not put back the miss that listing let go
// (here for a rollout written meanwhile into an older day).
func TestRolloutPathStaleLookKeepsNoMiss(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	ageDirs(t, codex)
	const id = "45454545-4545-4545-4545-454545454545"
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("no rollout yet: %q", p)
	}
	look := newMissLook(codex) // a lookup under way, globbing before the rollout is there
	old := now.AddDate(0, 0, -40)
	writeRollout(t, home, id, "/w", old, old)
	expireRollouts(codex, true)
	rollouts(codex) // the listing has its name: the miss goes
	look.keepMiss(id)
	if p := rolloutPath(codex, id); p == "" {
		t.Fatal("the lookup under way put the miss back: not found")
	}
}

// Names listed while one of their directories changed are not kept for later looks.
func TestRolloutPathNamesListedDuringAChangeAreNotKept(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	archived := filepath.Join(codex, "archived_sessions")
	if err := os.MkdirAll(archived, 0o755); err != nil {
		t.Fatal(err)
	}
	ageDirs(t, codex)
	const id = "34343434-3434-3434-3434-343434343434"
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("no rollout yet: %q", p)
	}
	changed := time.Now().Add(-30 * time.Minute) // archived_sessions changed, long enough ago to be settled
	if err := os.Chtimes(archived, changed, changed); err != nil {
		t.Fatal(err)
	}
	today := filepath.Join(codex, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	glob := rolloutGlob
	t.Cleanup(func() { rolloutGlob = glob })
	rolloutGlob = func(pattern string) ([]string, error) {
		if pattern == filepath.Join(archived, "rollout-*.jsonl*") { // today's directory changes while they are listed
			if err := os.WriteFile(filepath.Join(today, "note"), nil, 0o644); err != nil {
				t.Error(err)
			}
		}
		return glob(pattern)
	}
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("still no rollout: %q", p)
	}
	cache := rolloutCacheFor(codex)
	cache.mu.Lock()
	kept := cache.recentStamp
	cache.mu.Unlock()
	if kept != nil {
		t.Fatal("names listed while a directory changed were kept")
	}
}

// A list pass keeps the misses it finds after a listing of every directory overtook its look (a long pass, the first
// after the node starts).
func TestRolloutPathPassOvertakenByTheListing(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	ageDirs(t, codex)
	perID, _ := countRolloutGlobs(t, codex)
	pass := newCodexListPass()
	rolloutPathIn(codex, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", pass) // the pass takes its look
	expireRollouts(codex, true)                                        // a minute on: the next lookup lists every directory
	const id = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	rolloutPathIn(codex, id, pass)
	perID.Store(0)
	rolloutPathIn(codex, id, pass) // the list's second ask for the same thread, in the same pass
	if got := perID.Load(); got != 0 {
		t.Fatalf("globbed again in the same pass: %d", got)
	}
}

// A miss asked about again is kept by the next listing, however long ago it was recorded.
func TestRolloutPathMissAskedAgainIsKept(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	now := time.Now()
	writeRollout(t, home, "11111111-1111-1111-1111-111111111111", "/w", now, now)
	ageDirs(t, codex)
	const id = "23232323-2323-2323-2323-232323232323"
	if p := rolloutPath(codex, id); p != "" {
		t.Fatalf("no rollout: %q", p)
	}
	cache := rolloutCacheFor(codex)
	cache.mu.Lock()
	m := cache.misses[id]
	m.asked = m.asked.Add(-missKeptFor - time.Minute)
	cache.misses[id] = m
	cache.mu.Unlock()
	if p := rolloutPath(codex, id); p != "" { // asked about again
		t.Fatalf("no rollout: %q", p)
	}
	expireRollouts(codex, true)
	rollouts(codex)
	cache.mu.Lock()
	_, kept := cache.misses[id]
	cache.mu.Unlock()
	if !kept {
		t.Fatal("a miss asked about again was let go")
	}
}
