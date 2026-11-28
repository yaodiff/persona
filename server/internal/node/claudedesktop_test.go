package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
// earlier transcript once its current one is listed, and not a deleted session's (#8314): a transcript the desktop app
// ran that no entry names, unless it was written in the last desktopEntryGrace. An entry that cannot be read keeps the
// ids it mentions, another client's transcript is never judged by the index, and without an index all are listed.
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
		unnamed   = "dddddddd-0000-4000-8000-00000000000a" // a desktop transcript no entry names: its session was deleted
		fresh     = "dddddddd-0000-4000-8000-000000000015" // the same, written within desktopEntryGrace: the entry may follow
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
		writeEntrypointTranscript(t, dir, fresh, "claude-desktop-3p", 30*time.Second)
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
		main: true, btwOpen: true, gap: true, both: true, bothFirst: true, unnamed: false, fresh: true, fork: true, kept: true, cli: true,
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
	for _, id := range append(transcripts, cli, fresh) {
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

func TestClaudeDesktopDataDirectories(t *testing.T) {
	home := t.TempDir()
	mac := claudeDesktopDataDirs(home, "darwin", "", "")
	if len(mac) != 2 || mac[0] != filepath.Join(home, "Library", "Application Support", "Claude") ||
		mac[1] != filepath.Join(home, "Library", "Application Support", "Claude-3p") {
		t.Fatalf("macOS directories = %v", mac)
	}
	if got := claudeDesktopDataDirs(home, "linux", "", ""); len(got) != 0 {
		t.Fatalf("unknown desktop layout = %v", got)
	}
	local, roaming := filepath.Join(t.TempDir(), "Local Data"), filepath.Join(t.TempDir(), "Roaming Data")
	store := filepath.Join(home, "AppData", "Local", "Packages", "Claude_example", "LocalCache")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	dirs := claudeDesktopDataDirs(home, "windows", local, roaming)
	for _, base := range []string{filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming"),
		local, roaming, filepath.Join(store, "Local"), filepath.Join(store, "Roaming")} {
		for _, name := range []string{"Claude", "Claude-3p", "Claude-Data"} {
			if !slices.Contains(dirs, filepath.Join(base, name)) {
				t.Errorf("missing %s under %s", name, base)
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range claudeDesktopDataDirs(home, "windows", filepath.Join(home, "AppData", "Local"), "relative") {
		key := strings.ToLower(p)
		if seen[key] || !filepath.IsAbs(p) {
			t.Fatalf("duplicate or relative directory %q", p)
		}
		seen[key] = true
	}
}

func TestClaudeDesktopDirectoriesDoNotUseAnotherHomesEnvironment(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(other, "Local"))
	t.Setenv("APPDATA", filepath.Join(other, "Roaming"))
	for _, p := range claudeDesktopDirs(home) {
		if !strings.HasPrefix(p, home+string(filepath.Separator)) {
			t.Fatalf("another home's desktop directory was included: %q", p)
		}
	}
	if runtime.GOOS == "windows" && len(claudeDesktopDirs(home)) == 0 {
		t.Fatal("Windows desktop directories are missing")
	}
}

// Use the real Windows directory depth, including Store redirection. Read no app configuration or credentials.
func TestClaudeDesktopWindowsIndexLocations(t *testing.T) {
	home := t.TempDir()
	store := filepath.Join(home, "AppData", "Local", "Packages", "Claude_example", "LocalCache", "Roaming", "Claude")
	local := filepath.Join(home, "AppData", "Local", "Claude-3p")
	roaming := filepath.Join(home, "AppData", "Roaming", "Claude")
	const current = "eeeeeeee-0000-4000-8000-000000000021"
	const prior = "eeeeeeee-0000-4000-8000-000000000022"
	const archived = "eeeeeeee-0000-4000-8000-000000000023"
	const packaged = "eeeeeeee-0000-4000-8000-000000000024"
	cwd := filepath.Join(home, "work", "project")
	extra, _ := json.Marshal(cwd)
	origin, _ := json.Marshal(filepath.Join(home, "work"))
	writeDesktopEntry(t, local, current, current, false, []string{prior}, `,"cwd":`+string(extra)+`,"originCwd":`+string(origin))
	writeDesktopEntry(t, roaming, archived, archived, true, nil, "")
	writeDesktopEntry(t, store, packaged, packaged, false, nil, "")
	old := claudeDesktopDirs
	claudeDesktopDirs = func(home string) []string { return claudeDesktopDataDirs(home, "windows", "", "") }
	t.Cleanup(func() { claudeDesktopDirs = old })
	x := readClaudeDesktopIndex(home)
	if !x.listed(current) || !x.listed(packaged) || x.listed(archived) || !x.hidden(archived, nil) {
		t.Fatalf("desktop visibility = %v", x.visible)
	}
	if !x.hidden(prior, map[string]bool{current: true}) || x.about[current].cwd != cwd ||
		x.about[current].originCwd != filepath.Join(home, "work") || x.about[current].local != "local_"+current {
		t.Fatalf("desktop entry = %+v, prior = %v", x.about[current], x.earlier[prior])
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
	silentCwd := filepath.Join(home, "project")
	silentCwdJSON, _ := json.Marshal(silentCwd)
	writeDesktopEntry(t, third, "silent", oldSilent, false, nil, `,"cwd":`+string(silentCwdJSON))
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
	if s := got[oldSilent]; s.Title != "t" || s.Cwd != silentCwd {
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

// A session the index gives a parent (a side session, a fork, an earlier run of a scheduled task) is drawn under another
// row, so it is counted apart from the top-level ones (#15279): the newest maxDesktopFolded of them are listed beside
// the newest maxDesktopOffline top-level sessions, however much newer the folded ones are. Before, they were one count,
// and a daily scheduled task's runs pushed the top-level sessions out.
func TestDesktopFoldedSessionsDoNotTakeTheTopLevelPlaces(t *testing.T) {
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, third := useDesktopDirs(t)
	top := func(i int) string { return fmt.Sprintf("dddddddd-0000-4000-8000-%012d", i) }
	side := func(i int) string { return fmt.Sprintf("ffffffff-0000-4000-8000-%012d", i) }
	for i := 0; i < maxDesktopOffline+5; i++ { // older than every folded one
		writeEntrypointTranscript(t, dir, top(i), "claude-desktop-3p", time.Duration(1000+i)*time.Minute)
		writeDesktopEntry(t, third, top(i), top(i), false, nil, "")
	}
	from := `,"spawnedFrom":{"sessionId":"local_` + top(0) + `","taskId":"task_1","sideSession":true}`
	for i := 0; i < maxDesktopFolded+5; i++ {
		writeEntrypointTranscript(t, dir, side(i), "claude-desktop-3p", time.Duration(i+1)*time.Minute)
		writeDesktopEntry(t, third, side(i), side(i), false, nil, from)
	}
	tops, sides := 0, 0
	listed := map[string]bool{}
	for _, s := range n.scanOffline() {
		listed[s.SessionID] = true
		switch {
		case strings.HasPrefix(s.SessionID, "dddddddd"):
			tops++
		case strings.HasPrefix(s.SessionID, "ffffffff"):
			sides++
		}
	}
	if tops != maxDesktopOffline || sides != maxDesktopFolded {
		t.Fatalf("top-level sessions %d, folded %d; want %d and %d", tops, sides, maxDesktopOffline, maxDesktopFolded)
	}
	if !listed[top(0)] || listed[top(maxDesktopOffline)] {
		t.Fatal("not the newest of the top-level sessions")
	}
	if !listed[side(0)] || listed[side(maxDesktopFolded)] {
		t.Fatal("not the newest of the folded sessions")
	}
}

// A sidebar of a couple of hundred sessions, quiet for more than the usual 7 days, is listed whole (#15522): with the old
// bound of 100 the older ones were cut while the app still showed them.
func TestDesktopSidebarOfHundredsIsListedWhole(t *testing.T) {
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	first, _ := useDesktopDirs(t)
	const sidebar = 250
	for i := 0; i < sidebar; i++ {
		id := fmt.Sprintf("dddddddd-0000-4000-8000-%012d", i)
		writeEntrypointTranscript(t, dir, id, "claude-desktop", offlineWindow+time.Duration(i+1)*time.Hour)
		writeDesktopEntry(t, first, fmt.Sprintf("d%d", i), id, false, nil, "")
	}
	listed := 0
	for _, s := range n.scanOffline() {
		if strings.HasPrefix(s.SessionID, "dddddddd") {
			listed++
		}
	}
	if listed != sidebar {
		t.Fatalf("listed %d of the sidebar's %d sessions", listed, sidebar)
	}
}

// A session New Agent starts in the desktop app is written by `claude -p` and taken over by the app
// (claudedesktop_new.go): until the app ends a turn in it, its transcript says only sdk-cli. The sidebar lists it, so the
// offline list does too; the same run that no entry names is still a program's one-shot run and stays out (#15522).
func TestDesktopListedOneShotTranscriptIsListed(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	first, _ := useDesktopDirs(t)
	const (
		adopted = "5d500000-0000-4000-8000-0000000000a1" // New Agent's session, in the app's sidebar
		script  = "5d500000-0000-4000-8000-0000000000a2" // a program's `claude -p`, no entry names it
	)
	writeClaudeRun(t, dir, adopted, time.Minute, "sdk-cli")
	writeClaudeRun(t, dir, script, 2*time.Minute, "sdk-cli")
	writeDesktopEntry(t, first, "adopted", adopted, false, nil, `,"fromCliTranscript":true,"adoptedFromOtherSurface":true`)
	got := listedIDs(n)
	if !got[adopted] {
		t.Fatal("a session the desktop app's sidebar lists is left out as a one-shot run")
	}
	if got[script] {
		t.Fatal("a program's one-shot run no entry names is listed")
	}
}

// A session deleted in the app leaves the list at once: Delete writes deleted_<uuid> for the session's own id and for each
// transcript it ran, so desktopEntryGrace is not waited out, whether or not this node read the entry before. A delete
// whose markers name no transcript, or an entry removed without any, still waits it out; a transcript an entry names
// is listed whatever a marker says.
func TestDesktopSessionDeletedInTheAppLeavesAtOnce(t *testing.T) {
	const (
		deleted   = "dddddddd-2222-4000-8000-000000000001" // deleted in the app: markers for its id and its transcripts
		earlier   = "dddddddd-2222-4000-8000-000000000002" // deleted's earlier transcript
		localOnly = "dddddddd-2222-4000-8000-000000000003" // deleted, but the only marker names the session's own id
		removed   = "dddddddd-2222-4000-8000-000000000004" // its entry went without a marker
		named     = "dddddddd-2222-4000-8000-000000000005" // a marker names it, but so does an entry
		kept      = "dddddddd-2222-4000-8000-000000000006"
	)
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{deleted, earlier, localOnly, removed, named, kept} {
		writeEntrypointTranscript(t, dir, id, "claude-desktop-3p", 10*time.Second) // all within desktopEntryGrace
	}
	_, third := useDesktopDirs(t)
	index := filepath.Join(third, "claude-code-sessions", "account", "org")
	writeDesktopEntry(t, third, "named", named, false, nil, "")
	writeDesktopEntry(t, third, "kept", kept, false, nil, "")
	for _, id := range []string{"aaaaaaaa-2222-4000-8000-000000000001", deleted, earlier, "aaaaaaaa-2222-4000-8000-000000000003", named} {
		if err := os.WriteFile(filepath.Join(index, "deleted_"+id), []byte("1794736695195"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for _, s := range n.scanOffline() {
		got[s.SessionID] = true
	}
	for id, want := range map[string]bool{deleted: false, earlier: false, localOnly: true, removed: true, named: true, kept: true} {
		if got[id] != want {
			t.Errorf("%s listed=%v, want %v", id, got[id], want)
		}
	}
}
