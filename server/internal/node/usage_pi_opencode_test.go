package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/tokenuse"
)

func piReply(ts time.Time, id, model string, in, out, read int) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":null,"timestamp":%q,"message":{"role":"assistant","model":%q,"provider":"p",`+
		`"content":[],"usage":{"input":%d,"output":%d,"cacheRead":%d,"cacheWrite":0,"totalTokens":%d}}}`+"\n",
		id, ts.Format("2006-01-02T15:04:05.000Z07:00"), model, in, out, read, in+out+read)
}

// pi's and omp's sessions under their agent directories are metered as their agents, under their sessions' launchers;
// a fork's copy of a request (the same entry id and time) is counted once.
func TestUsageMeterPiAndOmp(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, home := testNode(t)
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	pi := filepath.Join(home, ".pi", "agent", "sessions", "--w--")
	omp := filepath.Join(home, ".omp", "agent", "sessions", "-w-app")
	files := map[string]string{
		filepath.Join(pi, "2026-11-17T08-00-00-000Z_0f1e2d3c.jsonl"): `{"type":"session","version":3,"id":"0f1e2d3c","timestamp":"2026-11-17T08:00:00.000Z","cwd":"/w"}` + "\n" +
			piReply(now, "a1", "model-a", 3, 40, 500),
		filepath.Join(pi, "2026-11-17T09-00-00-000Z_4b5a6978.jsonl"): piReply(now, "a1", "model-a", 3, 40, 500) + // the fork's copy
			piReply(now, "a2", "model-a", 1, 2, 0),
		filepath.Join(omp, "2026-11-17T08-00-00-000Z_019a7c00-0000-7000-8000-000000000001.jsonl"): piReply(now, "b1", "model-b", 5, 6, 7),
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := newUsageMeter()
	sessions := map[string]bool{}
	m.launcherOf = func(s string) string { sessions[s] = true; return "l" }
	cutoff := now.Add(-time.Hour)
	listed := 0
	for _, f := range n.usageFiles(cutoff) {
		listed++
		m.scan(f, cutoff)
	}
	if listed != 3 {
		t.Fatalf("listed %d files, want pi's two and omp's one", listed)
	}
	minute := now.Unix() / 60 * 60
	if b := m.buckets[usageKey{minute, "model-a", "l", tokenuse.Pi, ""}]; b == nil || b.Requests != 2 || b.Output != 42 || b.CacheRead != 500 {
		t.Fatalf("pi = %+v, want a1 once and a2", b)
	}
	if b := m.buckets[usageKey{minute, "model-b", "l", tokenuse.Omp, ""}]; b == nil || b.Requests != 1 || b.Input != 5 {
		t.Fatalf("omp = %+v", b)
	}
	for _, s := range []string{"0f1e2d3c", "4b5a6978", "019a7c00-0000-7000-8000-000000000001"} {
		if !sessions[s] {
			t.Errorf("launcher not asked for session %s (asked %v)", s, sessions)
		}
	}
}

// opencode's steps are read from its database: the finished ones since the cutoff, then only the rows added since and
// the unfinished ones again; nothing while the database has not changed. The backfill reads a span by the steps' times.
func TestUsageOpencode(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3:", err)
	}
	n, _ := testNode(t)
	db := filepath.Join(t.TempDir(), "opencode.db")
	t.Setenv("PERSONA_OPENCODE_DB", db)
	sql := func(stmts ...string) {
		t.Helper()
		if out, err := exec.Command("sqlite3", db, strings.Join(stmts, ";\n")).CombinedOutput(); err != nil {
			t.Fatalf("sqlite3: %v %s", err, out)
		}
	}
	now := time.Now().Truncate(time.Second)
	ms := func(t time.Time) int64 { return t.UnixMilli() }
	step := func(id string, at time.Time, done bool, in, out, reasoning int) string {
		completed := "null"
		if done {
			completed = fmt.Sprint(ms(at) + 500)
		}
		// SQL string literals stay single-quoted: sqlite3 builds with DQS disabled read Go's %q as an identifier.
		return fmt.Sprintf(`INSERT INTO session_message VALUES ('%s', 'ses_1', 'assistant', 0, %d, %d, json_object('model', json_object('id', 'model-o', 'providerID', 'p'), `+
			`'time', json_object('created', %d, 'completed', %s), 'tokens', json_object('input', %d, 'output', %d, 'reasoning', %d, 'cache', json_object('read', 100, 'write', 0))))`,
			id, ms(at), ms(at), ms(at), completed, in, out, reasoning)
	}
	sql(`CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, seq integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)`,
		`CREATE INDEX session_message_time_created_idx ON session_message (time_created)`,
		step("msg_old", now.Add(-30*time.Hour), true, 1, 1, 0), // before the window
		step("msg_1", now.Add(-time.Minute), true, 10, 20, 5),
		step("msg_2", now, false, 7, 0, 0), // under way
		`INSERT INTO session_message VALUES ('msg_u', 'ses_1', 'user', 1, `+fmt.Sprint(ms(now))+`, 0, '{}')`)
	ctx, cutoff := context.Background(), now.Add(-usageWindow)
	q := &opencodeUsage{}
	recs := q.poll(ctx, n, cutoff)
	if len(recs) != 1 || recs[0].Key != "opencode:msg_1" || recs[0].Output != 25 || recs[0].CacheRead != 100 || recs[0].Session != "ses_1" {
		t.Fatalf("first poll = %+v", recs)
	}
	if recs := q.poll(ctx, n, cutoff); recs != nil {
		t.Fatalf("unchanged database = %+v", recs)
	}
	sql(`UPDATE session_message SET data = json_set(data, '$.time.completed', `+fmt.Sprint(ms(now)+900)+`, '$.tokens.output', 3) WHERE id = 'msg_2'`,
		step("msg_3", now, true, 1, 2, 0))
	later := time.Now().Add(time.Minute) // the change as the file's time says it, whatever the clock's resolution
	if err := os.Chtimes(db, later, later); err != nil {
		t.Fatal(err)
	}
	recs = q.poll(ctx, n, cutoff)
	if len(recs) != 2 || recs[0].Key != "opencode:msg_2" || recs[0].Output != 3 || recs[1].Key != "opencode:msg_3" {
		t.Fatalf("second poll = %+v", recs)
	}
	// Through the meter: the session's launcher, the agent.
	m := newUsageMeter()
	m.launcherOf = func(s string) string { return "launcher-of-" + s }
	for _, r := range (&opencodeUsage{}).poll(ctx, n, cutoff) {
		m.add(&usageFile{}, r, cutoff)
	}
	var total int64
	for k, b := range m.buckets {
		if k.agent != tokenuse.Opencode || k.launcher != "launcher-of-ses_1" || k.model != "model-o" {
			t.Fatalf("bucket %+v", k)
		}
		total += int64(b.Requests)
	}
	if total != 3 {
		t.Fatalf("requests %d, want 3", total)
	}
	// Each session's directory (#16530): none without a session table, while its usage is read all the same; with
	// one, read once for the sessions the rows name.
	if q.dir("ses_1") != "" {
		t.Fatalf("dir without a session table = %q", q.dir("ses_1"))
	}
	sql(`CREATE TABLE session (id text PRIMARY KEY, directory text NOT NULL)`, `INSERT INTO session VALUES ('ses_1', '/work/shop')`)
	withDirs := &opencodeUsage{}
	if recs := withDirs.poll(ctx, n, cutoff); len(recs) != 3 || withDirs.dir("ses_1") != "/work/shop" {
		t.Fatalf("poll = %d records, dir %q", len(recs), withDirs.dir("ses_1"))
	}
	// The backfill: a span by the steps' times; the earliest step.
	old, err := q.between(ctx, n, now.Add(-31*time.Hour), now.Add(-29*time.Hour))
	if err != nil || len(old) != 1 || old[0].Key != "opencode:msg_old" {
		t.Fatalf("between = %+v, %v", old, err)
	}
	if first := q.first(ctx, n); first.UnixMilli() != ms(now.Add(-30*time.Hour)) {
		t.Fatalf("first = %v", first)
	}
	// No database: nothing, quietly.
	t.Setenv("PERSONA_OPENCODE_DB", filepath.Join(t.TempDir(), "none.db"))
	if recs := (&opencodeUsage{}).poll(ctx, n, cutoff); recs != nil || !(&opencodeUsage{}).first(ctx, n).IsZero() {
		t.Fatalf("without a database = %+v", recs)
	}
}

// Codex (#10002): every segment of a continued thread is read, under the thread's session (the session list follows
// its newest file); a rollout older than the session list's two weeks is read when the backfill reaches back to it.
func TestUsageCodexSegmentsAndOldRollouts(t *testing.T) {
	n, home := testNode(t)
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	const thread = "0199a213-81c0-7800-8aa1-0000000000aa"
	const segment = "0199a213-81c0-7800-8aa1-0000000000bb"
	count := func(at time.Time, total, in, out int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},`+
			`"last_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":%d}}}}`, at.Format(time.RFC3339Nano), total, in, out)
	}
	day := filepath.Join(home, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	meta := `{"timestamp":"` + now.Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"` + thread + `"}}`
	write := func(p string, at time.Time, lines ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(day, "rollout-2026-11-17T08-00-00-"+thread+".jsonl"), now.Add(-time.Minute), meta, count(now, 100, 60, 40))
	write(filepath.Join(day, "rollout-2026-11-17T09-00-00-"+thread+"_"+segment+".jsonl"), now, meta, count(now, 150, 30, 20))
	oldAt := now.Add(-40 * 24 * time.Hour)
	old := filepath.Join(home, ".codex", "sessions", oldAt.Format("2006"), oldAt.Format("01"), oldAt.Format("02"),
		"rollout-2026-10-08T08-00-00-0199a213-81c0-7800-8aa1-0000000000cc.jsonl")
	write(old, oldAt, `{"timestamp":"`+oldAt.Format(time.RFC3339)+`","type":"session_meta","payload":{"id":"0199a213-81c0-7800-8aa1-0000000000cc"}}`,
		count(oldAt, 9, 4, 5))

	m := newUsageMeter()
	sessions := map[string]bool{}
	m.launcherOf = func(s string) string { sessions[s] = true; return "codex" }
	cutoff := now.Add(-usageWindow)
	listed := map[string]bool{}
	for _, f := range n.usageFiles(cutoff) {
		listed[filepath.Base(f.path)] = true
		m.scan(f, cutoff)
	}
	if len(listed) != 2 || listed[filepath.Base(old)] {
		t.Fatalf("listed %v, want the thread's two segments", listed)
	}
	if b := m.buckets[usageKey{now.Unix() / 60 * 60, "codex", "codex", tokenuse.Codex, ""}]; b == nil || b.Requests != 2 || b.Output != 60 {
		t.Fatalf("bucket = %+v, want both segments' requests", b)
	}
	if len(sessions) != 1 || !sessions[thread] {
		t.Fatalf("sessions asked %v, want the thread", sessions)
	}
	found := false
	for _, f := range n.usageFiles(now.Add(-60 * 24 * time.Hour)) {
		found = found || f.path == old
	}
	if !found {
		t.Fatal("a backfill reaching back 60 days does not read a rollout written 40 days ago")
	}
}
