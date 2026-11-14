package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// writeEntrypointTranscript writes a Claude transcript of the desktop app (or of the command line for "cli").
func writeEntrypointTranscript(t *testing.T, dir, id, entrypoint string, age time.Duration) {
	t.Helper()
	body := fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":"2026-11-05T07:31:12Z","cwd":"/src/app","entrypoint":%q,"message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n"+
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-05T07:31:20Z","entrypoint":%q,"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n", entrypoint, entrypoint)
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(body+strings.Repeat(" ", 4096)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	_ = os.Chtimes(p, at, at)
}

// writeDesktopEntry writes one session of the desktop app's index under appDir (extra: more JSON fields, or "").
func writeDesktopEntry(t *testing.T, appDir, name, transcript string, archived bool, prior []string, extra string) string {
	t.Helper()
	dir := filepath.Join(appDir, "claude-code-sessions", "account", "org")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	priorJSON, _ := json.Marshal(prior)
	body := fmt.Sprintf(`{"sessionId":"local_%s","cliSessionId":%q,"isArchived":%v,"priorCliSessionIds":%s,"title":"t","cwd":"/src/app"%s}`,
		name, transcript, archived, priorJSON, extra)
	p := filepath.Join(dir, "local_"+name+".json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// useDesktopDirs points the desktop app's data directories at two under a test directory: the first-party app's and
// the third-party build's.
func useDesktopDirs(t *testing.T) (first, third string) {
	t.Helper()
	base := t.TempDir()
	first, third = filepath.Join(base, "Claude"), filepath.Join(base, "Claude-3p")
	old, oldOpens := claudeDesktopDirs, claudeDesktopOpens
	claudeDesktopDirs = func(string) []string { return []string{first, third} }
	claudeDesktopOpens = true // the Mac's app, on any system the tests run on
	t.Cleanup(func() { claudeDesktopDirs, claudeDesktopOpens = old, oldOpens })
	return first, third
}

// The index is read from macOS's Application Support and Windows' local app data, both builds' directories; another
// system has none.
func TestDesktopDataDirs(t *testing.T) {
	home := filepath.Join("h", "me")
	for goos, base := range map[string]string{
		"darwin":  filepath.Join(home, "Library", "Application Support"),
		"windows": filepath.Join(home, "AppData", "Local"),
	} {
		got := desktopDataDirs(goos, home)
		want := []string{filepath.Join(base, "Claude"), filepath.Join(base, "Claude-3p")}
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", goos, got, want)
		}
	}
	if got := desktopDataDirs("linux", home); got != nil {
		t.Errorf("linux: %v", got)
	}
}

// The offline list shows the desktop app's sessions as its sidebar does (#3304): not archived sessions, not a session's
// earlier transcript once its current one is listed. Anything no entry names is listed as before.
func TestOfflineListFollowsTheDesktopSidebar(t *testing.T) {
	const (
		main      = "dddddddd-0000-4000-8000-000000000001"
		archived  = "dddddddd-0000-4000-8000-000000000002"
		btwDone   = "dddddddd-0000-4000-8000-000000000003" // a /btw question the app archived
		btwOpen   = "dddddddd-0000-4000-8000-000000000004" // one it did not: listed (spawnedFrom is not read)
		earlier   = "dddddddd-0000-4000-8000-000000000005" // main's earlier transcript
		gap       = "dddddddd-0000-4000-8000-000000000006" // an earlier transcript whose session's current one is not there
		missing   = "dddddddd-0000-4000-8000-000000000007"
		both      = "dddddddd-0000-4000-8000-000000000008" // named by an archived entry and by an unarchived one
		firstApp  = "dddddddd-0000-4000-8000-000000000009" // archived in the first-party app
		unnamed   = "dddddddd-0000-4000-8000-00000000000a" // a desktop transcript no entry names
		fork      = "dddddddd-0000-4000-8000-00000000000b" // Persona's "Branch to New Chat" copy, archived in the app
		kept      = "dddddddd-0000-4000-8000-00000000000c" // kept, archived in the app
		corrupted = "dddddddd-0000-4000-8000-00000000000d" // its entry cannot be read
		oldOfDone = "dddddddd-0000-4000-8000-00000000000e" // the earlier transcript of an archived session
		current2  = "dddddddd-0000-4000-8000-00000000000f" // a session's current transcript, also named earlier by another
		shared    = "dddddddd-0000-4000-8000-000000000010" // earlier for two sessions: one listed, one not there
		newerOld  = "dddddddd-0000-4000-8000-000000000011" // an earlier transcript written after its current one
		newerNow  = "dddddddd-0000-4000-8000-000000000012"
		bothFirst = "dddddddd-0000-4000-8000-000000000013" // as both, its unarchived entry read first
		oversized = "dddddddd-0000-4000-8000-000000000014" // archived, in an entry file too big to read
		cli       = "cccccccc-0000-4000-8000-000000000001"
	)
	// Newest first: main before its earlier transcript, newerOld before newerNow.
	transcripts := []string{newerOld, main, current2, archived, btwDone, btwOpen, earlier, gap, both, firstApp, unnamed, fork, kept,
		corrupted, oldOfDone, shared, newerNow, bothFirst, oversized}
	listed := func(t *testing.T, withIndex bool) map[string]bool {
		n, home := testNode(t)
		n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
		n.forked, n.kept = map[string]bool{fork: true}, map[string]bool{kept: true}
		dir := filepath.Join(home, ".claude", "projects", "-src-app")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for i, id := range transcripts {
			writeEntrypointTranscript(t, dir, id, "claude-desktop-3p", time.Duration(i+1)*time.Minute)
		}
		writeEntrypointTranscript(t, dir, cli, "cli", time.Hour)
		first, third := useDesktopDirs(t)
		if withIndex {
			writeDesktopEntry(t, third, "main", main, false, []string{earlier, shared}, "")
			writeDesktopEntry(t, third, "archived", archived, true, nil, "")
			writeDesktopEntry(t, third, "btwdone", btwDone, true, nil, `,"spawnedFrom":{"btw":true,"sideSession":true,"userFork":true}`)
			writeDesktopEntry(t, third, "btwopen", btwOpen, false, nil, `,"spawnedFrom":{"btw":true,"sideSession":true,"userFork":true}`)
			writeDesktopEntry(t, third, "gap", missing, false, []string{gap, shared}, "")
			writeDesktopEntry(t, third, "both1", both, true, nil, "")
			writeDesktopEntry(t, third, "both2", both, false, nil, "")
			writeDesktopEntry(t, third, "a-bothfirst", bothFirst, false, nil, "")
			writeDesktopEntry(t, third, "z-bothfirst", bothFirst, true, nil, "")
			writeDesktopEntry(t, first, "first", firstApp, true, nil, "")
			writeDesktopEntry(t, third, "fork", fork, true, nil, "")
			writeDesktopEntry(t, third, "kept", kept, true, nil, "")
			bad := writeDesktopEntry(t, third, "corrupted", corrupted, true, nil, "")
			os.WriteFile(bad, []byte(`{"cliSessionId":"`+corrupted+`","isArchived":tru`), 0o600)
			writeDesktopEntry(t, third, "done", missing, true, []string{oldOfDone}, "")
			writeDesktopEntry(t, third, "current2", current2, false, nil, "")
			writeDesktopEntry(t, third, "other", main, false, []string{current2}, "")
			writeDesktopEntry(t, third, "newer", newerNow, false, []string{newerOld}, "")
			writeDesktopEntry(t, third, "oversized", oversized, true, nil, `,"pad":"`+strings.Repeat("x", 4<<20)+`"`)
		}
		out := map[string]bool{}
		for _, s := range n.scanOffline() {
			out[s.SessionID] = true
		}
		return out
	}

	got := listed(t, true)
	want := map[string]bool{
		main: true, btwOpen: true, gap: true, both: true, bothFirst: true, unnamed: true, fork: true, kept: true, cli: true,
		corrupted: true, current2: true, newerOld: true, newerNow: true, oversized: true,
		archived: false, btwDone: false, earlier: false, firstApp: false, oldOfDone: false, shared: false,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s listed=%v, want %v", id, got[id], w)
		}
	}
	// Without the app's index every transcript is listed, as before.
	got = listed(t, false)
	for _, id := range append(transcripts, cli) {
		if !got[id] {
			t.Errorf("without an index %s is missing", id)
		}
	}
}

// Rows left out never count toward the 80 the offline list shows: a busy desktop app's archived sessions do not push
// an older session out.
func TestDesktopRowsLeftOutDoNotTakeTheCap(t *testing.T) {
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, third := useDesktopDirs(t)
	for i := 0; i < 85; i++ {
		id := fmt.Sprintf("ffffffff-0000-4000-8000-%012d", i)
		writeEntrypointTranscript(t, dir, id, "claude-desktop-3p", time.Duration(i+1)*time.Minute)
		writeDesktopEntry(t, third, id, id, true, nil, "")
	}
	const older = "aaaaaaaa-0000-4000-8000-000000000001"
	writeEntrypointTranscript(t, dir, older, "cli", 3*time.Hour)
	rows := n.scanOffline()
	if len(rows) != 1 || rows[0].SessionID != older {
		t.Fatalf("rows = %d, want only the older session", len(rows))
	}
}

// An entry is read again when its file changes: archiving a session in the app takes it off the list, unarchiving
// brings it back, and a removed entry hides nothing.
func TestClaudeDesktopIndexFollowsChanges(t *testing.T) {
	_, third := useDesktopDirs(t)
	const id = "eeeeeeee-0000-4000-8000-000000000001"
	present := map[string]bool{id: true}
	hidden := func() bool { return readClaudeDesktopIndex("").hidden(id, present) }
	p := writeDesktopEntry(t, third, "s", id, false, nil, "")
	if hidden() {
		t.Fatal("an unarchived session is hidden")
	}
	writeDesktopEntry(t, third, "s", id, true, nil, "")
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(p, later, later)
	if !hidden() {
		t.Fatal("an archived session is listed")
	}
	os.Remove(p)
	if hidden() {
		t.Fatal("without an entry nothing is hidden")
	}
}

// The desktop app's sidebar lists its unarchived sessions however long they have been quiet: so does the offline list,
// on a count of its own, so they don't push the newest other sessions out (#3304). Old ones the app archived, or no
// entry names, stay out as before.
func TestDesktopSessionsAreListedWhateverTheirAge(t *testing.T) {
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, third := useDesktopDirs(t)
	const (
		oldOpen     = "bbbbbbbb-0000-4000-8000-000000000001"
		oldArchived = "bbbbbbbb-0000-4000-8000-000000000002"
		oldUnnamed  = "bbbbbbbb-0000-4000-8000-000000000003"
		oldSilent   = "bbbbbbbb-0000-4000-8000-000000000004" // opened in the app, nothing said yet
	)
	month := 30 * 24 * time.Hour
	for _, id := range []string{oldOpen, oldArchived, oldUnnamed} {
		writeEntrypointTranscript(t, dir, id, "claude-desktop-3p", month)
	}
	silent := filepath.Join(dir, oldSilent+".jsonl")
	os.WriteFile(silent, []byte(`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-10-01T00:00:00Z"}`+"\n"), 0o600)
	_ = os.Chtimes(silent, time.Now().Add(-month), time.Now().Add(-month))
	writeDesktopEntry(t, third, "open", oldOpen, false, nil, "")
	writeDesktopEntry(t, third, "archived", oldArchived, true, nil, "")
	writeDesktopEntry(t, third, "silent", oldSilent, false, nil, "")
	for i := 0; i < 85; i++ { // the newest 80 others stay the newest 80
		writeEntrypointTranscript(t, dir, fmt.Sprintf("cccccccc-0000-4000-8000-%012d", i), "cli", time.Duration(i+1)*time.Minute)
	}
	got := map[string]proto.Session{}
	others := 0
	for _, s := range n.scanOffline() {
		got[s.SessionID] = s
		if strings.HasPrefix(s.SessionID, "cccccccc") {
			others++
		}
	}
	for id, want := range map[string]bool{oldOpen: true, oldSilent: true, oldArchived: false, oldUnnamed: false} {
		if _, listed := got[id]; listed != want {
			t.Errorf("%s listed=%v, want %v", id, listed, want)
		}
	}
	if others != 80 {
		t.Fatalf("other sessions = %d, want the newest 80", others)
	}
	// A session whose transcript says nothing yet has the title and the directory its entry gives (a message to it
	// resumes it there).
	if s := got[oldSilent]; s.Title != "t" || s.Cwd != "/src/app" {
		t.Fatalf("silent session title %q cwd %q, want the entry's", s.Title, s.Cwd)
	}
}

// The desktop app's sessions are counted on their own, recent ones too: the newest maxDesktopOffline of them are listed,
// and they take none of the others' 80. Another platform's run (agentfilters.go) is left out even when the index names
// it.
func TestDesktopSessionsHaveACountOfTheirOwn(t *testing.T) {
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, third := useDesktopDirs(t)
	for i := 0; i < maxDesktopOffline+5; i++ {
		id := fmt.Sprintf("dddddddd-0000-4000-8000-%012d", i)
		writeEntrypointTranscript(t, dir, id, "claude-desktop-3p", time.Duration(i+1)*time.Minute)
		writeDesktopEntry(t, third, fmt.Sprintf("d%d", i), id, false, nil, "")
	}
	for i := 0; i < 85; i++ {
		writeEntrypointTranscript(t, dir, fmt.Sprintf("cccccccc-0000-4000-8000-%012d", i), "cli", time.Duration(i+1)*time.Minute)
	}
	const platform = "eeeeeeee-0000-4000-8000-000000000001"
	run := filepath.Join(dir, platform+".jsonl")
	os.WriteFile(run, []byte(`{"type":"user","uuid":"u1","timestamp":"2026-11-05T07:31:12Z","cwd":"/src/app","entrypoint":"claude-desktop-3p","message":{"role":"user","content":"[bot run] summarize"}}`+"\n"+strings.Repeat(" ", 4096)+"\n"), 0o600)
	writeDesktopEntry(t, third, "platform", platform, false, nil, "")
	n.setAgentFilters([]proto.AgentFilter{{Extension: "bot", ID: "bot", Agents: []string{"claude"}, FirstMessagePrefixes: []string{"[bot run]"}}})
	desktop, others := 0, 0
	listed := map[string]bool{}
	for _, s := range n.scanOffline() {
		listed[s.SessionID] = true
		switch {
		case strings.HasPrefix(s.SessionID, "dddddddd"):
			desktop++
		case strings.HasPrefix(s.SessionID, "cccccccc"):
			others++
		}
	}
	if desktop != maxDesktopOffline || others != 80 {
		t.Fatalf("desktop sessions %d, others %d; want %d and 80", desktop, others, maxDesktopOffline)
	}
	if !listed[fmt.Sprintf("dddddddd-0000-4000-8000-%012d", 0)] || listed[fmt.Sprintf("dddddddd-0000-4000-8000-%012d", maxDesktopOffline)] {
		t.Fatal("not the newest of the desktop sessions")
	}
	if listed[platform] {
		t.Fatal("another platform's run is listed because the index names it")
	}
}
