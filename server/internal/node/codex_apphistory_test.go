package node

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Each kind of the app-server's ThreadItems the chat shows becomes the rollout item the transcript reader knows: the
// user's words and images, the answer, its reasoning, a command (without the login shell around it) with its output and
// exit code, a file change, an MCP call, a web search, an image looked at or made, a compaction. Kinds the chat does not
// show, and items still running, are left out.
func TestCodexRolloutItemsReadAsARollout(t *testing.T) {
	parse := func(kind, raw string) []proto.Item {
		t.Helper()
		item := codexRolloutItem(kind, []byte(raw))
		if item == nil {
			return nil
		}
		line, _ := json.Marshal(map[string]any{"timestamp": "2026-11-01T00:00:00Z", "type": "event_msg",
			"payload": map[string]any{"type": "item_completed", "item": item}})
		var m transcript.Meta
		return transcript.ParseLine(0, line, &m)
	}
	user := parse("userMessage", `{"type":"userMessage","id":"u1","content":[{"type":"text","text":"fix the export","text_elements":[]},{"type":"localImage","path":"/tmp/shot.png"}]}`)
	if len(user) != 1 || user[0].Role != "user" || !strings.Contains(user[0].Text, "fix the export") || !strings.Contains(user[0].Text, "/tmp/shot.png") {
		t.Fatalf("user = %+v", user)
	}
	answer := parse("agentMessage", `{"type":"agentMessage","id":"a1","text":"Done: the CSV is UTF-8 now.","phase":"final_answer"}`)
	if len(answer) != 1 || answer[0].Role != "assistant" || answer[0].Kind != proto.KindText || answer[0].Text != "Done: the CSV is UTF-8 now." {
		t.Fatalf("answer = %+v", answer)
	}
	if note := parse("agentMessage", `{"type":"agentMessage","id":"a2","text":"Looking at the writer.","phase":"commentary"}`); len(note) != 1 || note[0].Kind != proto.KindText || !note[0].Narration {
		t.Fatalf("commentary = %+v", note)
	}
	if r := parse("reasoning", `{"type":"reasoning","id":"r1","summary":["Check the encoder"],"content":[]}`); len(r) != 1 || r[0].Kind != proto.KindThinking {
		t.Fatalf("reasoning = %+v", r)
	}
	cmd := parse("commandExecution", `{"type":"commandExecution","id":"c1","command":"/bin/bash -lc 'go test ./...'","cwd":"/w","status":"failed","aggregatedOutput":"FAIL x","exitCode":1,"durationMs":1500}`)
	if len(cmd) != 2 || cmd[0].ToolName != "Bash" || !strings.Contains(string(cmd[0].ToolInput), `"go test ./..."`) ||
		strings.Contains(string(cmd[0].ToolInput), "bash") || !cmd[1].IsError || cmd[1].Outcome == nil || cmd[1].Outcome.DurationMS == nil || *cmd[1].Outcome.DurationMS != 1500 {
		t.Fatalf("command = %+v", cmd)
	}
	if running := parse("commandExecution", `{"type":"commandExecution","id":"c2","command":"sleep 9","status":"inProgress"}`); running != nil {
		t.Fatalf("a running command = %+v", running)
	}
	edit := parse("fileChange", `{"type":"fileChange","id":"f1","status":"completed","changes":[{"path":"/w/a.go","kind":{"type":"update","move_path":null},"diff":"@@ -1 +1 @@\n-a\n+b\n"}]}`)
	if len(edit) != 2 || edit[0].ToolName != "Edit" || !strings.Contains(string(edit[0].ToolInput), "/w/a.go") || edit[1].Text != "update /w/a.go" {
		t.Fatalf("file change = %+v", edit)
	}
	mcp := parse("mcpToolCall", `{"type":"mcpToolCall","id":"m1","server":"docs","tool":"search","status":"completed","arguments":{"q":"csv"},"result":{"content":[{"type":"text","text":"3 hits"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}],"isError":false},"error":null}`)
	if len(mcp) != 3 || mcp[0].ToolName != "mcp__docs__search" || mcp[1].Text != "3 hits" ||
		mcp[2].Image == nil || mcp[2].Image.Data != "iVBORw0KGgo=" || mcp[2].Image.Mime != "image/png" {
		t.Fatalf("mcp = %+v", mcp)
	}
	web := parse("webSearch", `{"type":"webSearch","id":"w1","query":"utf-8 bom csv","action":{"type":"search","url":""},"results":[{"title":"BOM","url":"https://example.com/bom","domain":"example.com","snippet":"…"}]}`)
	if len(web) != 2 || !strings.Contains(string(web[0].ToolInput), "utf-8 bom csv") || !strings.Contains(web[1].Text, "https://example.com/bom") {
		t.Fatalf("web search = %+v", web)
	}
	if img := parse("imageView", `{"type":"imageView","id":"i1","path":"/tmp/shot.png"}`); len(img) != 2 || img[0].ToolName != "Read" {
		t.Fatalf("image = %+v", img)
	}
	if made := parse("imageGeneration", `{"type":"imageGeneration","id":"g1","status":"completed","savedPath":"/tmp/made.png","revisedPrompt":"a cat"}`); len(made) != 2 || !strings.Contains(string(made[0].ToolInput), "/tmp/made.png") {
		t.Fatalf("image made = %+v", made)
	}
	if c := parse("contextCompaction", `{"type":"contextCompaction","id":"k1"}`); len(c) != 1 || c[0].Kind != proto.KindNotice {
		t.Fatalf("compaction = %+v", c)
	}
	for _, kind := range []string{"sleep", "somethingNew"} {
		if item := codexRolloutItem(kind, []byte(`{"type":"`+kind+`","id":"x"}`)); item != nil {
			t.Errorf("%s = %v, want left out", kind, item)
		}
	}
}

type historyTurn struct {
	thread, id, status string
	ordinal            int
	errJSON            string
	items              [][2]string // kind, JSON
}

// writeCodexHistory writes the App's history database in home, inserting the items in reverse order: the history is
// read in the order of their ordinals, not of their rows.
func writeCodexHistory(t *testing.T, home string, turns []historyTurn) {
	t.Helper()
	sqlite := sqlite3WithJSON(t)
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	var b strings.Builder
	b.WriteString(`CREATE TABLE IF NOT EXISTS thread_turns (thread_id TEXT NOT NULL, turn_id TEXT NOT NULL, rollout_ordinal INTEGER NOT NULL,
		status TEXT NOT NULL, error_json TEXT, duration_ms INTEGER, PRIMARY KEY (thread_id, turn_id));
		CREATE TABLE IF NOT EXISTS thread_items (thread_id TEXT NOT NULL, turn_id TEXT NOT NULL, item_id TEXT NOT NULL,
		rollout_ordinal INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, item_json TEXT NOT NULL, item_type TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (thread_id, turn_id, item_id)); DELETE FROM thread_turns; DELETE FROM thread_items;`)
	n := 0
	var rows []string
	for _, turn := range turns {
		errJSON := "NULL"
		if turn.errJSON != "" {
			errJSON = q(turn.errJSON)
		}
		fmt.Fprintf(&b, "INSERT INTO thread_turns VALUES (%s, %s, %d, %s, %s, 1200);", q(turn.thread), q(turn.id), turn.ordinal, q(turn.status), errJSON)
		for _, it := range turn.items {
			// Item ids sort against their ordinals (z…a): neither the rows' order nor the key's is the history's.
			rows = append(rows, fmt.Sprintf("INSERT INTO thread_items VALUES (%s, %s, 'i%04d', %d, %d, %s, %s);", q(turn.thread), q(turn.id), 9999-n, n,
				time.Date(2026, 11, 1, 10, 0, n, 0, time.UTC).UnixMilli(), q(it[1]), q(it[0])))
			n++
		}
	}
	for i := len(rows) - 1; i >= 0; i-- {
		b.WriteString(rows[i])
	}
	if out, err := exec.Command(sqlite, filepath.Join(home, "thread_history_1.sqlite"), b.String()).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
}

func userItem(id, text string) [2]string {
	return [2]string{"userMessage", fmt.Sprintf(`{"type":"userMessage","id":%q,"content":[{"type":"text","text":%q,"text_elements":[]}]}`, id, text)}
}

func answerItem(id, text string) [2]string {
	return [2]string{"agentMessage", fmt.Sprintf(`{"type":"agentMessage","id":%q,"text":%q,"phase":"final_answer"}`, id, text)}
}

func historyText(list []proto.Item) string {
	var parts []string
	for _, it := range list {
		parts = append(parts, it.Role+":"+string(it.Kind)+":"+it.Text)
	}
	return strings.Join(parts, " | ")
}

func statOf(t *testing.T, p string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The transcript of an App chat whose rollout is gone is its history in the App's database, written as a rollout: only
// this chat's finished turns, in order, a failed one with its error, an interrupted one marked, one with no item kept; a
// turn still running is left for later. The list writes nothing. Read again with no change, the file stays as it is; once
// the App finished more turns they are appended, so a reader goes on where it was (#3305).
func TestCodexAppChatHistoryComesFromTheAppsDatabase(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	updated := time.Now().Add(-48 * time.Hour).Truncate(time.Millisecond)
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", updated, 0))
	running := historyTurn{thread: appChat, id: "t4", status: "inProgress", ordinal: 4, items: [][2]string{userItem("u4", "and the PDF?")}}
	turns := []historyTurn{
		{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{userItem("u0", "fix the export"), answerItem("a0", "Fixed.")}},
		{thread: appChat, id: "t1", status: "failed", ordinal: 1, errJSON: `{"message":"Selected model is at capacity.","codexErrorInfo":"serverOverloaded"}`,
			items: [][2]string{userItem("u1", "try again")}},
		{thread: appChat, id: "t2", status: "interrupted", ordinal: 2, items: [][2]string{userItem("u2", "stop")}},
		{thread: appChat, id: "t3", status: "completed", ordinal: 3},
		running,
		{thread: appOld, id: "x0", status: "completed", ordinal: 0, items: [][2]string{userItem("x0", "another chat's words")}},
	}
	writeCodexHistory(t, home, turns)

	var id string
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SourceThreadID == appChat {
			id = s.SessionID
		}
	}
	if id == "" {
		t.Fatal("the chat is not listed")
	}
	if entries, _ := os.ReadDir(filepath.Join(n.dir, "codex-app-history")); len(entries) != 0 {
		t.Fatalf("the list wrote %d files", len(entries))
	}
	f, err := n.file(id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	s := historyText(got)
	for _, want := range []string{"user:text:fix the export", "assistant:text:Fixed.", "user:text:try again", "user:text:stop"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(s, "another chat") || strings.Contains(s, "and the PDF?") {
		t.Fatalf("another chat's or a running turn's words: %s", s)
	}
	if strings.Index(s, "fix the export") > strings.Index(s, "Fixed.") || strings.Index(s, "Fixed.") > strings.Index(s, "try again") {
		t.Fatalf("out of order: %s", s)
	}
	if !strings.Contains(s, transcript.InterruptedNotice) {
		t.Fatalf("the interrupted turn is not marked: %s", s)
	}
	p := n.transcriptPath(id)
	if !strings.HasPrefix(p, n.dir) {
		t.Fatalf("transcript at %q, want under the state directory", p)
	}
	before := statOf(t, p)

	// Nothing changed: read again, the file is as it was, and the database is not even asked (made unreadable here: a
	// query would fail and be remembered).
	db := filepath.Join(home, "thread_history_1.sqlite")
	if err := os.Chmod(db, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(db); err == nil { // root, or a system without these modes: a query would not fail
		f.Close()
		t.Log("the database stays readable here: whether it was asked is not seen")
	}
	if _, err := n.file(id); err != nil {
		t.Fatal(err)
	}
	codexAppHistoryFailed.Lock()
	_, asked := codexAppHistoryFailed.byPath[p]
	codexAppHistoryFailed.Unlock()
	if err := os.Chmod(db, 0o600); err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Fatal("the database was asked again without a change")
	}
	if st := statOf(t, p); !os.SameFile(st, before) || !st.ModTime().Equal(before.ModTime()) || st.Size() != before.Size() {
		t.Fatal("written again without a change")
	}

	// The App finished the running turn and another: they are appended to the same file, and the reader gets only them.
	running.status = "completed"
	running.items = append(running.items, answerItem("a4", "PDF too."))
	turns[4] = running
	turns = append(turns, historyTurn{thread: appChat, id: "t5", status: "completed", ordinal: 5, items: [][2]string{userItem("u5", "thanks")}})
	writeCodexHistory(t, home, turns)
	writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", updated.Add(time.Hour), 0))
	if _, err := n.file(id); err != nil {
		t.Fatal(err)
	}
	more, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if s := historyText(more); !strings.Contains(s, "user:text:and the PDF?") || !strings.Contains(s, "assistant:text:PDF too.") ||
		!strings.Contains(s, "user:text:thanks") || strings.Contains(s, "fix the export") {
		t.Fatalf("after the App's turns = %s", s)
	}
	if !os.SameFile(statOf(t, p), before) {
		t.Fatal("the file was replaced instead of grown")
	}

	// Archived in the App: no transcript of its history any more.
	writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", updated.Add(2*time.Hour), 1))
	src, _ := n.codexSource(id)
	if got := n.codexAppHistoryPath(src, nil); got != "" {
		t.Fatalf("archived chat's history at %q", got)
	}
}

func TestCodexAppHistoryRebuildsLegacyMarks(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Image chat", time.Now(), 0))
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{
		userItem("u0", "show the frame"),
		{"mcpToolCall", `{"type":"mcpToolCall","id":"m1","server":"persona","tool":"frames","status":"completed","arguments":{},"result":{"content":[{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}],"isError":false}}`},
	}}})
	var id string
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SourceThreadID == appChat {
			id = s.SessionID
		}
	}
	if id == "" {
		t.Fatal("the chat is not listed")
	}
	if _, err := n.file(id); err != nil {
		t.Fatal(err)
	}
	p := n.transcriptPath(id)
	const withImage = `{"type":"image","text":"","data":"iVBORw0KGgo=","mimeType":"image/png"}`
	raw, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(raw), withImage) {
		t.Fatalf("the current history did not retain its image: %v", err)
	}
	// A pre-image converter wrote this same row without data/mimeType. Keep every other mark field and the size
	// current so only its missing format version can force the rebuild.
	legacy := strings.Replace(string(raw), withImage, `{"type":"image","text":""}`, 1)
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	mark, size := readCodexHistoryMark(p)
	mark.Version, mark.Size = 0, size
	if err := writeCodexHistoryMark(p, mark); err != nil {
		t.Fatal(err)
	}
	if err := n.refreshCodexAppHistory(id); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(rebuilt), withImage) {
		t.Fatalf("a legacy mark kept the image-less history: %v; %s", err, rebuilt)
	}
	if got, _ := readCodexHistoryMark(p); got.Version != codexAppHistoryVersion {
		t.Fatalf("the rebuilt history did not advance its mark: %+v", got)
	}
}

// A chat registered with its rollout, which the App then deleted: its history comes from the database (the registry
// keeps the path it knew).
func TestCodexAppChatHistoryAfterItsRolloutWasDeleted(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	rollout := writeAppRollout(t, home, appChat, project, time.Now().Add(-time.Hour))
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, rollout, project, "Alpha chat", time.Now(), 0))
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0,
		items: [][2]string{userItem("u0", "from the database")}}})
	var id string
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SourceThreadID == appChat {
			id = s.SessionID
		}
	}
	if src, _ := n.codexSource(id); src.path != resolved(t, rollout) { // the node names it resolved (#9138)
		t.Fatalf("registered with %q, want its rollout", src.path)
	}
	if err := os.Remove(rollout); err != nil {
		t.Fatal(err)
	}
	expireRollouts(home, true)
	f, err := n.file(id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if s := historyText(got); !strings.Contains(s, "user:text:from the database") {
		t.Fatalf("history = %s", s)
	}
}

// Without the App's history database, or with one this code cannot read, nothing is written and no temporary file is
// left: the chat has no transcript, as before.
func TestCodexAppChatWithoutReadableHistoryHasNoTranscript(t *testing.T) {
	for name, sql := range map[string]string{
		"no database":  "",
		"other schema": "CREATE TABLE thread_turns (thread_id TEXT, turn_id TEXT);",
	} {
		t.Run(name, func(t *testing.T) {
			n := remoteTestNode(t)
			home := n.codexHome()
			project := t.TempDir()
			writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0))
			if sql != "" {
				if out, err := exec.Command(sqlite3WithJSON(t), filepath.Join(home, "thread_history_1.sqlite"), sql).CombinedOutput(); err != nil {
					t.Fatalf("sqlite3: %v: %s", err, out)
				}
			}
			var id string
			for _, s := range n.codexSessions(map[string]bool{}) {
				if s.SourceThreadID == appChat {
					id = s.SessionID
				}
			}
			if _, err := n.file(id); err == nil {
				t.Fatal("a transcript without a readable history")
			}
			entries, _ := os.ReadDir(filepath.Join(n.dir, "codex-app-history"))
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".jsonl") || strings.HasPrefix(e.Name(), ".write-") {
					t.Fatalf("left %s", e.Name())
				}
			}
			if sql == "" {
				return
			}
			// The failed query is not run again for the same change of the chat; it is for the next one.
			src, _ := n.codexSource(id)
			p := n.codexAppHistoryFile(src)
			failedAt := func() time.Time {
				codexAppHistoryFailed.Lock()
				defer codexAppHistoryFailed.Unlock()
				return codexAppHistoryFailed.byPath[p].at
			}
			first := failedAt()
			if first.IsZero() {
				t.Fatal("the failure is not remembered")
			}
			n.file(id)
			if !failedAt().Equal(first) {
				t.Fatal("asked again for the same change")
			}
			writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", time.Now().Add(time.Minute), 0))
			n.file(id)
			if failedAt().Equal(first) {
				t.Fatal("not asked again after the chat changed")
			}
		})
	}
}

func TestCodexHistoryDBPathTakesTheNewest(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"thread_history_1.sqlite", "thread_history_3.sqlite", "thread_history_x.sqlite", "state_5.sqlite"} {
		os.WriteFile(filepath.Join(home, name), nil, 0o600)
	}
	if got := codexHistoryDBPath(home); !strings.HasSuffix(got, "thread_history_3.sqlite") {
		t.Fatalf("db = %s", got)
	}
}

// A turn that failed keeps the App's error, as a rollout's task_complete does; a chat with only a running turn has no
// transcript yet.
func TestCodexAppChatFailedTurnAndRunningOnly(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0)+
		codexAppRow(appOld, "", project, "Old chat", time.Now(), 0))
	writeCodexHistory(t, home, []historyTurn{
		{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{userItem("u0", "hello")}},
		{thread: appChat, id: "t1", status: "failed", ordinal: 1, errJSON: `{"message":"Selected model is at capacity.","codexErrorInfo":"serverOverloaded"}`,
			items: [][2]string{userItem("u1", "again")}},
		{thread: appOld, id: "r0", status: "inProgress", ordinal: 0, items: [][2]string{userItem("r0", "working on it")}},
	})
	ids := map[string]string{}
	for _, s := range n.codexSessions(map[string]bool{}) {
		ids[s.SourceThreadID] = s.SessionID
	}
	f, err := n.file(ids[appChat])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if m := f.Meta(); m.TurnError == "" {
		t.Fatalf("the failed turn's error is lost: %+v", m)
	}
	if _, err := n.file(ids[appOld]); err == nil {
		t.Fatal("a transcript for a chat whose only turn still runs")
	}
}

// readChat lists the chats and reads one's transcript afresh.
func readChat(t *testing.T, n *Node, thread string) (string, string) {
	t.Helper()
	var id string
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SourceThreadID == thread {
			id = s.SessionID
		}
	}
	f, err := n.file(id)
	if err != nil {
		return id, ""
	}
	got, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	return id, historyText(got)
}

// A turn left "in progress" (the App quit during it) is over once a later turn comes: written as interrupted, it does not
// hold back the turns after it. The last turn finishing in the history database, with updated_at_ms unchanged, is
// written on the next read. A rolled back turn, and a file unlike its mark, are written anew.
func TestCodexAppChatHistoryFollowsTheDatabase(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	updated := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", updated, 0))
	turns := []historyTurn{
		{thread: appChat, id: "t0", status: "inProgress", ordinal: 0, items: [][2]string{userItem("u0", "left running")}},
		{thread: appChat, id: "t1", status: "completed", ordinal: 1, items: [][2]string{userItem("u1", "next one")}},
		{thread: appChat, id: "t2", status: "inProgress", ordinal: 2, items: [][2]string{userItem("u2", "still going")}},
	}
	writeCodexHistory(t, home, turns)
	id, s := readChat(t, n, appChat)
	if !strings.Contains(s, "user:text:left running") || !strings.Contains(s, transcript.InterruptedNotice) || !strings.Contains(s, "user:text:next one") ||
		strings.Contains(s, "still going") {
		t.Fatalf("first read = %s", s)
	}

	// The App finishes the last turn: the history database changes, updated_at_ms does not.
	turns[2].status = "completed"
	turns[2].items = append(turns[2].items, answerItem("a2", "done now"))
	writeCodexHistory(t, home, turns)
	f, _ := n.file(id)
	more, _ := f.Refresh()
	if s := historyText(more); !strings.Contains(s, "assistant:text:done now") || strings.Contains(s, "next one") {
		t.Fatalf("after the turn finished = %s", s)
	}

	// The App rolls the last turn back and runs another in its place: the file is written anew.
	turns[2] = historyTurn{thread: appChat, id: "t2b", status: "completed", ordinal: 2, items: [][2]string{userItem("u2b", "asked differently")}}
	writeCodexHistory(t, home, turns)
	writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", updated.Add(time.Minute), 0))
	if _, s := readChat(t, n, appChat); !strings.Contains(s, "asked differently") || strings.Contains(s, "done now") {
		t.Fatalf("after a rollback = %s", s)
	}

	// The file was cut short (a write that stopped halfway): unlike its mark, it is written anew.
	p := n.transcriptPath(id)
	if err := os.Truncate(p, 100); err != nil {
		t.Fatal(err)
	}
	if _, s := readChat(t, n, appChat); !strings.Contains(s, "next one") || !strings.Contains(s, "asked differently") {
		t.Fatalf("after a cut file = %s", s)
	}
}

// Another process writing the chat's transcript holds its lock: this one waits, and does not write meanwhile. A chat the
// App no longer lists has its file removed when another of the home's chats is written.
func TestCodexAppChatHistoryLockAndPrune(t *testing.T) {
	defer func(w time.Duration) { codexAppHistoryLockWait = w }(codexAppHistoryLockWait)
	codexAppHistoryLockWait = 300 * time.Millisecond
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0)+
		codexAppRow(appOld, "", project, "Old chat", time.Now(), 0))
	writeCodexHistory(t, home, []historyTurn{
		{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{userItem("u0", "alpha")}},
		{thread: appOld, id: "o0", status: "completed", ordinal: 0, items: [][2]string{userItem("o0", "old")}},
	})
	src := codexSource{home: canonicalCodexHome(home, n.home), thread: appChat}
	p := n.codexAppHistoryFile(src)
	os.MkdirAll(filepath.Dir(p), 0o700)
	release, err := link.LockFile(p+".lock", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, s := readChat(t, n, appChat); s != "" {
		t.Fatalf("written while another process holds the lock: %s", s)
	}
	release()
	if _, s := readChat(t, n, appChat); !strings.Contains(s, "alpha") {
		t.Fatalf("after the lock = %s", s)
	}
	// Archived in the App: its file goes when the other chat is written.
	writeCodexApp(t, home, project, "UPDATE threads SET archived = 1 WHERE id = '"+appChat+"';")
	if _, s := readChat(t, n, appOld); !strings.Contains(s, "old") {
		t.Fatalf("old chat = %s", s)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the archived chat's file stays: %v", err)
	}
}

// A watched App chat with no rollout: the turns the App finishes are pushed as they would be from a rollout.
func TestAWatchedCodexAppChatGetsItsNewTurns(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	updated := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", updated, 0))
	turns := []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{userItem("u0", "first")}}}
	writeCodexHistory(t, home, turns)
	id, _ := readChat(t, n, appChat)
	hub := withHub(t, n)
	if err := n.watch(id, 0); err != nil {
		t.Fatal(err)
	}
	defer n.unwatch(id)
	turns = append(turns, historyTurn{thread: appChat, id: "t1", status: "completed", ordinal: 1,
		items: [][2]string{userItem("u1", "second"), answerItem("a1", "the App's answer")}})
	writeCodexHistory(t, home, turns)
	writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", updated.Add(time.Minute), 0))
	waitFor(t, "the App's new turn", func() bool {
		return slices.ContainsFunc(hub.got(), func(it proto.Item) bool { return it.Text == "the App's answer" })
	})
}

// closeLikeTheApp leaves db as the Codex App does when it closes it: in WAL mode, checkpointed, without -wal and -shm.
func closeLikeTheApp(t *testing.T, db string) {
	t.Helper()
	if out, err := exec.Command(sqlite3WithJSON(t), db, "PRAGMA journal_mode=WAL; PRAGMA wal_checkpoint(TRUNCATE);").CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	os.Remove(db + "-wal")
	os.Remove(db + "-shm")
	if codexDBTarget(db) == db {
		t.Fatalf("%s is not left as the App leaves it", db)
	}
}

// With the App's databases closed (the App quit, or they were idle), its chats are listed and their history read all the
// same: sqlite3 -readonly cannot open a WAL database whose -shm is gone, so it is opened as immutable.
func TestCodexAppChatsReadWhileTheAppHasItsDatabasesClosed(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0))
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{userItem("u0", "while closed")}}})
	closeLikeTheApp(t, filepath.Join(home, "state_5.sqlite"))
	closeLikeTheApp(t, filepath.Join(home, "thread_history_1.sqlite"))
	if id, s := readChat(t, n, appChat); id == "" || !strings.Contains(s, "user:text:while closed") {
		t.Fatalf("listed as %q, transcript %s", id, s)
	}
	for _, ext := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(home, "state_5.sqlite"+ext)); err == nil {
			t.Fatalf("reading made the App's %s", ext)
		}
	}
}

// A database the App has open (its -shm is there), or one not in WAL mode, is opened by its path.
func TestCodexDBTargetIsThePathUnlessClosedInWALMode(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "a b.sqlite")
	if out, err := exec.Command(sqlite3WithJSON(t), db, "CREATE TABLE x(a);").CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	if got := codexDBTarget(db); got != db {
		t.Fatalf("rollback journal: %q", got)
	}
	closeLikeTheApp(t, db)
	target := codexDBTarget(db)
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.Query().Get("immutable") != "1" ||
		strings.TrimPrefix(u.Path, "/") != strings.TrimPrefix(filepath.ToSlash(db), "/") {
		t.Fatalf("closed: %q is not the immutable URI for %q: %v", target, db, err)
	}
	if out, err := exec.Command(sqlite3WithJSON(t), "-readonly", target, "SELECT count(*) FROM x;").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("closed database is not readable: %v: %s", err, out)
	}
	os.WriteFile(db+"-shm", nil, 0o600)
	if got := codexDBTarget(db); got != db {
		t.Fatalf("open: %q", got)
	}
	if got := codexDBTarget(filepath.Join(dir, "missing.sqlite")); got != filepath.Join(dir, "missing.sqlite") {
		t.Fatalf("missing: %q", got)
	}
}

// Several requests opening a chat at once (session.items, session.watch, session.info): each gets its transcript, the
// others waiting for the one writing it.
func TestCodexAppChatOpenedByManyAtOnce(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0))
	var turns []historyTurn
	for i := 0; i < 50; i++ {
		turns = append(turns, historyTurn{thread: appChat, id: fmt.Sprintf("t%d", i), status: "completed", ordinal: i,
			items: [][2]string{userItem(fmt.Sprintf("u%d", i), fmt.Sprintf("question %d", i)), answerItem(fmt.Sprintf("a%d", i), "answer")}})
	}
	writeCodexHistory(t, home, turns)
	var id string
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SourceThreadID == appChat {
			id = s.SessionID
		}
	}
	errs := make(chan error, 4)
	for range 4 {
		go func() { _, err := n.file(id); errs <- err }()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// The App's database as it may be met mid-change: the chat updated before its new turn's rows are written; a turn left
// in progress before the next one was started (written as interrupted), then finished; a turn rolled back with none left
// finished; an item whose JSON has line breaks between its values.
func TestCodexAppChatHistoryMidChange(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	updated := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", updated, 0))
	turns := []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0,
		items: [][2]string{userItem("u0", "first"), {"agentMessage", "{\"type\":\"agentMessage\",\n\"id\":\"a0\",\r\n\"text\":\"over lines\",\"phase\":\"final_answer\"}"}}}}
	writeCodexHistory(t, home, turns)
	id, s := readChat(t, n, appChat)
	if !strings.Contains(s, "assistant:text:over lines") {
		t.Fatalf("first read = %s", s)
	}

	// The chat is updated; its new turn's rows come later, the chat's updated_at_ms unchanged.
	writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", updated.Add(time.Minute), 0))
	readChat(t, n, appChat)
	turns = append(turns, historyTurn{thread: appChat, id: "t1", status: "completed", ordinal: 1, items: [][2]string{userItem("u1", "rows came late")}})
	writeCodexHistory(t, home, turns)
	if _, s := readChat(t, n, appChat); !strings.Contains(s, "rows came late") {
		t.Fatalf("after the late rows = %s", s)
	}

	// t2 still runs when t3 is started: t2 is written as interrupted; once the App finishes it, it is written as it is.
	turns = append(turns,
		historyTurn{thread: appChat, id: "t2", status: "inProgress", ordinal: 2, items: [][2]string{userItem("u2", "slow one")}},
		historyTurn{thread: appChat, id: "t3", status: "inProgress", ordinal: 3, items: [][2]string{userItem("u3", "next one")}})
	writeCodexHistory(t, home, turns)
	if _, s := readChat(t, n, appChat); !strings.Contains(s, "slow one") || !strings.Contains(s, transcript.InterruptedNotice) || strings.Contains(s, "next one") {
		t.Fatalf("with t2 left running = %s", s)
	}
	turns[2].status = "completed"
	turns[2].items = append(turns[2].items, answerItem("a2", "slow answer"))
	writeCodexHistory(t, home, turns)
	if _, s := readChat(t, n, appChat); !strings.Contains(s, "slow answer") || strings.Contains(s, transcript.InterruptedNotice) {
		t.Fatalf("after t2 finished = %s", s)
	}

	// Rolled back to its first message, asked again and still running: no finished turn, no transcript.
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "t0b", status: "inProgress", ordinal: 0, items: [][2]string{userItem("u0b", "asked again")}}})
	p := n.transcriptPath(id)
	n.file(id)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the rolled back turns' file stays: %v", err)
	}
	if got := n.transcriptPath(id); got != "" {
		t.Fatalf("transcript at %q with no finished turn", got)
	}
}

// A watched chat whose rollout the App deletes, having moved its history into its database: the turns it finishes
// next are pushed from the database.
func TestAWatchedCodexAppChatWhoseRolloutGoes(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	rollout := writeAppRollout(t, home, appChat, project, time.Now().Add(-time.Hour))
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, rollout, project, "Alpha chat", time.Now().Add(-time.Hour), 0))
	turns := []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{userItem("u0", "first")}}}
	writeCodexHistory(t, home, turns)
	id, _ := readChat(t, n, appChat)
	if src, _ := n.codexSource(id); src.path != resolved(t, rollout) { // the node names it resolved (#9138)
		t.Fatalf("registered with %q, want its rollout", src.path)
	}
	hub := withHub(t, n)
	if err := n.watch(id, 0); err != nil {
		t.Fatal(err)
	}
	defer n.unwatch(id)
	if err := os.Remove(rollout); err != nil {
		t.Fatal(err)
	}
	expireRollouts(home, true)
	turns = append(turns, historyTurn{thread: appChat, id: "t1", status: "completed", ordinal: 1,
		items: [][2]string{userItem("u1", "second"), answerItem("a1", "after the rollout went")}})
	writeCodexHistory(t, home, turns)
	writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0))
	waitFor(t, "the App's new turn", func() bool {
		return slices.ContainsFunc(hub.got(), func(it proto.Item) bool { return it.Text == "after the rollout went" })
	})
}

// After a failed read the chat is tried again soon once the history database changes, at once when the chat does; a lock
// that cannot be taken for another reason than another writer is not waited for.
func TestCodexAppChatHistoryFailureBackoffAndLockErrors(t *testing.T) {
	defer func(soon, wait time.Duration) { codexAppHistoryRetrySoon, codexAppHistoryLockWait = soon, wait }(codexAppHistoryRetrySoon, codexAppHistoryLockWait)
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0))
	db := filepath.Join(home, "thread_history_1.sqlite")
	if out, err := exec.Command(sqlite3WithJSON(t), db, "CREATE TABLE thread_turns (thread_id TEXT, turn_id TEXT);").CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	if _, s := readChat(t, n, appChat); s != "" {
		t.Fatalf("read with another schema: %s", s)
	}
	// The App writes its rows (the history database changes, the chat does not): tried again once the short wait is over.
	os.Remove(db)
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "t0", status: "completed", ordinal: 0, items: [][2]string{userItem("u0", "rows now")}}})
	if _, s := readChat(t, n, appChat); s != "" {
		t.Fatalf("tried again at once: %s", s)
	}
	codexAppHistoryRetrySoon = 0
	id, s := readChat(t, n, appChat)
	if !strings.Contains(s, "rows now") {
		t.Fatalf("after the short wait = %s", s)
	}

	// The directory cannot take the lock file: given up at once, not after the wait for another writer.
	p := n.transcriptPath(id)
	dir := filepath.Dir(p)
	for _, name := range []string{filepath.Base(p) + ".lock"} {
		os.Remove(filepath.Join(dir, name))
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil { // root, or a system without these modes
		f.Close()
		t.Skip("the directory stays writable here")
	}
	codexAppHistoryLockWait = 3 * time.Second
	writeCodexApp(t, home, project, "DELETE FROM threads;"+codexAppRow(appChat, "", project, "Alpha chat", time.Now().Add(time.Minute), 0))
	start := time.Now()
	n.file(id)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v on a lock that cannot be taken", d)
	}
}

func TestCodexRolloutCollaborationAndPlanItems(t *testing.T) {
	for _, tc := range []struct{ kind, raw, want string }{
		{"collabAgentToolCall", `{"id":"c1","tool":"wait","status":"completed","senderThreadId":"parent","receiverThreadIds":["child"],"receiverAgents":[],"agentsStates":{"child":{"status":"running"}}}`, `{"id":"c1","type":"CollabAgentToolCall","tool":"wait","status":"completed","sender_thread_id":"parent","receiver_thread_ids":["child"],"receiver_agents":[],"agents_states":{"child":{"status":"running"}}}`},
		{"subAgentActivity", `{"id":"s1","kind":"started","agentThreadId":"child","agentPath":"/review","model":"example-model","reasoningEffort":"high"}`, `{"id":"s1","type":"SubAgentActivity","kind":"started","agent_thread_id":"child","agent_path":"/review","model":"example-model","reasoning_effort":"high"}`},
		{"plan", `{"id":"p1","text":"Inspect then fix"}`, `{"id":"p1","type":"Plan","text":"Inspect then fix"}`},
	} {
		var want map[string]any
		if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
			t.Fatal(err)
		}
		if got := codexRolloutItem(tc.kind, []byte(tc.raw)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", tc.kind, got, want)
		}
	}
}

func TestCodexAppTerminalPaginatedRunningTurn(t *testing.T) {
	n := remoteTestNode(t)
	home, project := n.codexHome(), t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Terminal", time.Now(), 0, "codex-tui")+
		"ALTER TABLE threads ADD COLUMN history_mode TEXT; UPDATE threads SET source = 'cli', history_mode = 'paginated';")
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "running", status: "inProgress", ordinal: 0,
		items: [][2]string{userItem("u1", "keep working"), answerItem("a1", "Checking files")}}})
	if row := codexPaginatedThreads(home)[appChat]; !row.includeRunning || row.Originator != "codex-tui" {
		t.Fatalf("terminal row = %+v", row)
	}
	id, text := readChat(t, n, appChat)
	if !strings.Contains(text, "keep working") || !strings.Contains(text, "Checking files") {
		t.Fatalf("running transcript = %q", text)
	}
	src, _, _ := n.codexAppHistoryChat(id)
	if data, err := os.ReadFile(n.codexAppHistoryFile(src)); err != nil || strings.Contains(string(data), "task_complete") {
		t.Fatalf("running turn was completed: %s, %v", data, err)
	}
}
