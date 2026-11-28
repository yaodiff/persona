package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// writeDesktopCopy writes a copy of session local's entry under the account and organization folders given in appDir,
// as last written age ago (extra: more JSON fields, or "").
func writeDesktopCopy(t *testing.T, appDir, account, org, local, transcript string, archived bool, prior []string, extra string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(appDir, "claude-code-sessions", account, org)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	priorJSON, _ := json.Marshal(prior)
	body := fmt.Sprintf(`{"sessionId":"local_%s","cliSessionId":%q,"isArchived":%v,"priorCliSessionIds":%s,"title":"t","cwd":"/src/app"%s}`,
		local, transcript, archived, priorJSON, extra)
	p := filepath.Join(dir, "local_"+local+".json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// touchTranscript makes transcript id's file under dir last written age ago.
func touchTranscript(t *testing.T, dir, id string, age time.Duration) {
	t.Helper()
	at := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(dir, id+".jsonl"), at, at); err != nil {
		t.Fatal(err)
	}
}

// rowAliases and rowParent read a row of a map (its methods take a pointer).
func rowAliases(s proto.Session) []string { return s.Aliases() }

func rowParent(s proto.Session) string { return s.ParentAgentID() }

// listedRows is the node's list now, by id, its offline scan read again.
func listedRows(t *testing.T, n *Node) map[string]proto.Session {
	t.Helper()
	forgetOfflineScan()
	return byID(n.sessionList(context.Background(), false))
}

// The app keeps copies of a session's entry under its other account and organization folders, each with the
// cliSessionId it had when it was written (#14394). A copy from before the session went on in a new transcript names
// the old transcript, unarchived: read entry by entry, it listed the old transcript beside the new one, with the same
// title and project. The session is read once, from the copy whose transcript no copy names as an earlier one, even
// where the stale copies were written later, and in either build's folder; lastAssistantCliSessionId names an earlier
// transcript as priorCliSessionIds does. Its earlier transcripts fold into its row, which names them as aliases, whatever
// order the transcripts were written in. Archived in the app (its newest copy), the session and every transcript it ran
// leave the list, whatever an older copy still says.
func TestADesktopSessionWithStaleCopiesIsListedOnce(t *testing.T) {
	const (
		local  = "0a0a0a0a-0000-4000-8000-000000000001" // the app's own id of the session
		oldest = "0a0a0a0a-0000-4000-8000-000000000002" // the transcript it ran first
		last   = "0a0a0a0a-0000-4000-8000-000000000003" // the one it went on from last: its last answer is there
		now    = "0a0a0a0a-0000-4000-8000-000000000004" // the one it runs now
		other  = "0a0a0a0a-0000-4000-8000-000000000005" // another session, whose copies agree
	)
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeEntrypointTranscript(t, dir, now, "claude-desktop", time.Minute)
	writeEntrypointTranscript(t, dir, last, "claude-desktop", 20*time.Minute)
	writeEntrypointTranscript(t, dir, oldest, "claude-desktop", 2*time.Hour)
	writeEntrypointTranscript(t, dir, other, "claude-desktop", 3*time.Hour)
	first, third := useDesktopDirs(t)
	// The copy the app keeps current, written before the stale ones were.
	writeDesktopCopy(t, third, "account", "org", local, now, false, []string{oldest}, `,"lastAssistantCliSessionId":"`+last+`"`, 30*time.Minute)
	writeDesktopCopy(t, third, "account2", "org", local, last, false, []string{oldest}, "", time.Minute)
	writeDesktopCopy(t, first, "account3", "org2", local, last, false, nil, "", time.Minute)
	writeDesktopCopy(t, third, "account", "org", other, other, false, nil, "", time.Hour)
	writeDesktopCopy(t, third, "account2", "org", other, other, false, nil, "", time.Hour)

	x := readClaudeDesktopIndex(home)
	if !x.listed(now) || x.listed(last) || x.listed(oldest) || !x.listed(other) {
		t.Fatalf("listed: now %v, last %v, oldest %v, other %v", x.listed(now), x.listed(last), x.listed(oldest), x.listed(other))
	}
	for t2, a := range x.about {
		if a.local == "local_"+local && t2 != now {
			t.Errorf("the session's entry is read for %s too", t2)
		}
	}
	if x.about[now].local != "local_"+local {
		t.Errorf("about = %+v", x.about[now])
	}
	for _, id := range []string{last, oldest} {
		if got := x.earlier[id]; len(got) != 1 || got[0] != (claudeDesktopNow{now, false}) {
			t.Errorf("earlier[%s] = %v", id, got)
		}
	}
	if got := x.before[now]; !slices.Equal(got, []string{last, oldest}) {
		t.Errorf("before = %v, want the last one first", got)
	}
	if len(x.before[other]) != 0 || len(x.earlier[other]) != 0 {
		t.Errorf("a session whose copies agree went on from %v", x.before[other])
	}

	rows := listedRows(t, n)
	for id, want := range map[string]bool{now: true, other: true, last: false, oldest: false} {
		if _, ok := rows[id]; ok != want {
			t.Errorf("%s listed = %v, want %v", id, ok, want)
		}
	}
	if got := rowAliases(rows[now]); !slices.Equal(got, []string{last, oldest}) {
		t.Errorf("aliases = %v", got)
	}

	// The earlier transcript written after the current one: the scan's newest-first rule misses it, the list does not.
	touchTranscript(t, dir, last, 0)
	forgetOfflineScan()
	scanned := map[string]bool{}
	for _, s := range n.scanOffline() {
		scanned[s.SessionID] = true
	}
	if !scanned[last] {
		t.Fatal("the scan left out the newer earlier transcript: the list below proves nothing")
	}
	rows = listedRows(t, n)
	if _, ok := rows[last]; ok {
		t.Error("an earlier transcript written after the current one is listed")
	}
	if _, ok := rows[now]; !ok {
		t.Error("the session is not listed")
	}

	// Archived in the app: its newest copy says so; an older copy that names the same transcript still says otherwise.
	writeDesktopCopy(t, first, "account4", "org", local, now, false, []string{oldest}, "", 40*time.Minute)
	writeDesktopCopy(t, third, "account", "org", local, now, true, []string{oldest}, `,"lastAssistantCliSessionId":"`+last+`"`, 0)
	x = readClaudeDesktopIndex(home)
	if x.listed(now) || !x.hidden(now, nil) || !x.hidden(last, nil) || !x.hidden(oldest, nil) {
		t.Fatalf("archived: listed %v, earlier %v", x.listed(now), x.earlier)
	}
	rows = listedRows(t, n)
	for _, id := range []string{now, last, oldest} {
		if _, ok := rows[id]; ok {
			t.Errorf("archived: %s is listed", id)
		}
	}
	if _, ok := rows[other]; !ok {
		t.Error("archived: another session went too")
	}
}

// A session's copies that disagree with no copy naming the other's transcript as an earlier one: the newest copy wins,
// and the other's transcript is one the session ran before. In a loop no app writes, the newest wins too.
func TestDesktopSessionsTakeTheNewestCopyWithoutOtherWord(t *testing.T) {
	const (
		a = "0b0b0b0b-0000-4000-8000-000000000001"
		b = "0b0b0b0b-0000-4000-8000-000000000002"
	)
	at := time.Now()
	entry := func(transcript string, age time.Duration, prior ...string) desktopEntryFile {
		return desktopEntryFile{entry: claudeDesktopEntry{transcript: transcript, local: "local_s", prior: prior}, mtime: at.Add(-age)}
	}
	for _, tc := range []struct {
		name string
		read []desktopEntryFile
		head string
	}{
		{"newest", []desktopEntryFile{entry(a, time.Hour), entry(b, time.Minute)}, b},
		{"newest, read first", []desktopEntryFile{entry(b, time.Minute), entry(a, time.Hour)}, b},
		{"named earlier", []desktopEntryFile{entry(a, time.Hour, b), entry(b, time.Minute)}, a},
		{"loop", []desktopEntryFile{entry(a, time.Hour, b), entry(b, time.Minute, a)}, b},
	} {
		s := desktopSessions(tc.read)
		if len(s) != 1 {
			t.Fatalf("%s: %d sessions", tc.name, len(s))
		}
		other := a
		if tc.head == a {
			other = b
		}
		if s[0].head.entry.transcript != tc.head || !slices.Equal(s[0].earlier, []string{other}) {
			t.Errorf("%s: head %s, earlier %v", tc.name, s[0].head.entry.transcript, s[0].earlier)
		}
	}
	// Entries with no id of the app's are sessions of their own.
	s := desktopSessions([]desktopEntryFile{{entry: claudeDesktopEntry{transcript: a}, path: "x"}, {entry: claudeDesktopEntry{transcript: b}, path: "y"}})
	if len(s) != 2 {
		t.Fatalf("entries without an id: %d sessions", len(s))
	}
}

// A claude of the app still running a transcript its session went on from folds into the session's row (#14394), live
// as at rest, unless it waits on the user: its question or permission prompt stays reachable. A terminal's claude that
// took up the old transcript runs a continuation of its own and stays a row.
func TestALiveClaudeOnAnEarlierDesktopTranscriptFolds(t *testing.T) {
	const (
		local  = "0c0c0c0c-0000-4000-8000-000000000001"
		before = "0c0c0c0c-0000-4000-8000-000000000002"
		now    = "0c0c0c0c-0000-4000-8000-000000000003"
	)
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeEntrypointTranscript(t, dir, now, "claude-desktop", time.Minute)
	writeEntrypointTranscript(t, dir, before, "claude-desktop", 10*time.Minute)
	_, third := useDesktopDirs(t)
	writeDesktopCopy(t, third, "account", "org", local, now, false, []string{before}, "", time.Minute)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	writeClaudeProc(t, home, os.Getpid(), map[string]any{"sessionId": now, "cwd": home, "kind": "interactive", "entrypoint": "claude-desktop", "status": "idle"})
	running := func(entrypoint, status string) {
		writeClaudeProc(t, home, os.Getppid(), map[string]any{"sessionId": before, "cwd": home, "kind": "interactive", "entrypoint": entrypoint, "status": status})
	}
	for _, tc := range []struct {
		entrypoint, status string
		listed             bool
		source             string
	}{
		{"claude-desktop", "busy", false, ""},
		{"claude-desktop", "waiting", true, proto.SourceClaudeDesktop},
		{"cli", "idle", true, proto.SourceClaudeTerminal},
	} {
		running(tc.entrypoint, tc.status)
		rows := byID(listSessions(t, n))
		if rows[now].SourceKind != proto.SourceClaudeDesktop || rows[now].Status != proto.StatusIdle {
			t.Fatalf("%s %s: the session's row = %+v", tc.entrypoint, tc.status, rows[now])
		}
		s, ok := rows[before]
		if ok != tc.listed || ok && s.SourceKind != tc.source {
			t.Errorf("%s %s: the earlier transcript listed %v (%q), want %v (%q)", tc.entrypoint, tc.status, ok, s.SourceKind, tc.listed, tc.source)
		}
		if want := []string{before}; tc.listed {
			if len(rowAliases(rows[now])) != 0 {
				t.Errorf("%s %s: aliases %v name a listed row", tc.entrypoint, tc.status, rowAliases(rows[now]))
			}
		} else if !slices.Equal(rowAliases(rows[now]), want) {
			t.Errorf("%s %s: aliases %v, want %v", tc.entrypoint, tc.status, rowAliases(rows[now]), want)
		}
	}
}

// foldDesktopEarlier leaves a session's earlier transcript as it is while the session is not listed through the one it
// runs now, and never takes a row in a pane, one Persona is starting, an assistant conversation or a kept one; an
// earlier transcript only archived sessions ran goes without one.
func TestFoldDesktopEarlierKeepsWhatItDoesNotOwn(t *testing.T) {
	const (
		now, gap, old = "0d0d0d0d-0000-4000-8000-000000000001", "0d0d0d0d-0000-4000-8000-000000000002", "0d0d0d0d-0000-4000-8000-000000000003"
		archivedOld   = "0d0d0d0d-0000-4000-8000-000000000004"
		missing       = "0d0d0d0d-0000-4000-8000-000000000005"
	)
	folds := desktopFolds{
		earlier: map[string][]claudeDesktopNow{old: {{now, false}}, gap: {{missing, false}}, archivedOld: {{missing, true}}},
		before:  map[string][]string{now: {old}},
	}
	rest := func(id string) proto.Session {
		return proto.Session{SessionID: id, Agent: "claude", Status: proto.StatusOffline, SourceKind: proto.SourceClaudeDesktop}
	}
	shared := rest(now)
	shared.SetParentAgentID(missing) // a hierarchy the list's copies share
	list := []proto.Session{shared, rest(old), rest(gap), rest(archivedOld)}
	got := byID(foldDesktopEarlier(slices.Clone(list), folds, nil))
	if _, ok := got[old]; ok || len(got) != 2 {
		t.Fatalf("rows = %v", got)
	}
	if _, ok := got[gap]; !ok {
		t.Error("an earlier transcript whose session's current one is not listed went: the session would not be listed at all")
	}
	if !slices.Equal(rowAliases(got[now]), []string{old}) || rowParent(got[now]) != missing {
		t.Errorf("aliases = %v, parent %q", rowAliases(got[now]), rowParent(got[now]))
	}
	if list[0].Aliases() != nil {
		t.Error("the aliases were set on the hierarchy another copy of the row shares")
	}
	// At rest with no source of its own (a transcript a terminal once took up): it goes as the scan hides it.
	plain := proto.Session{SessionID: old, Agent: "claude", Status: proto.StatusOffline}
	if got := byID(foldDesktopEarlier([]proto.Session{rest(now), plain}, folds, nil)); len(got) != 1 {
		t.Error("a resting earlier transcript with no source stayed")
	}
	for name, s := range map[string]proto.Session{
		"pane":      {SessionID: old, Agent: "claude", Status: proto.StatusIdle, PaneID: "w1:p1"},
		"starting":  {SessionID: old, Agent: "claude", Status: proto.StatusOffline, Starting: true},
		"assistant": {SessionID: old, Agent: "claude", Status: proto.StatusOffline, Assistant: true},
		"runtime":   {SessionID: old, Agent: "claude", Status: proto.StatusIdle, SourceKind: proto.SourceRuntime},
		"terminal":  {SessionID: old, Agent: "claude", Status: proto.StatusIdle, SourceKind: proto.SourceClaudeTerminal},
		"blocked":   {SessionID: old, Agent: "claude", Status: proto.StatusBlocked, SourceKind: proto.SourceClaudeDesktop},
	} {
		got := byID(foldDesktopEarlier([]proto.Session{rest(now), s}, folds, nil))
		if _, ok := got[old]; !ok {
			t.Errorf("%s: the row went", name)
		}
		if len(rowAliases(got[now])) != 0 {
			t.Errorf("%s: the session's row names a listed row as an alias", name)
		}
	}
	if got := byID(foldDesktopEarlier([]proto.Session{rest(now), rest(old)}, folds, map[string]bool{old: true})); len(got) != 2 {
		t.Error("a kept session went")
	}
	// A claude of the app still running an earlier transcript only archived sessions ran: running, it is not judged by
	// the archive (at rest it goes, above).
	live := proto.Session{SessionID: archivedOld, Agent: "claude", Status: proto.StatusWorking, SourceKind: proto.SourceClaudeDesktop}
	if got := byID(foldDesktopEarlier([]proto.Session{rest(now), live}, folds, nil)); len(got) != 2 {
		t.Error("a running claude of an archived session's earlier transcript went")
	}
}
