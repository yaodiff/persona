package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/link"
)

// A Codex App chat whose rollout is gone (codex_appdb.go) keeps its history in <CODEX_HOME>/thread_history_<n>.sqlite:
// thread_turns, and thread_items holding the app-server's ThreadItems (v2: userMessage, agentMessage, commandExecution…).
// Its transcript is that history written back as rollout lines — session_meta, then per finished turn task_started, an
// item_completed per item, and task_complete (with its error when it failed) or turn_aborted — so the transcript reader,
// the clients and every check on a rollout read it as any other (#3305). The file is
// <state dir>/codex-app-history/<home hash>-<thread>.jsonl. It grows as a rollout does: the turns finished since it was
// last written are appended (the last turn while it still runs is left for later; one that never finished but has turns
// after it is written as interrupted), so a reader goes on where it was. <file>.mark records what was written; a file
// unlike its mark, or whose last turn is no longer in the database as it was, is written again from the start (and
// removed when no turn is left finished). It is brought up to date when the transcript is read (file) and, while the chat
// is watched, about once a second (pumpWatch), never by the list; one writer at a time, across processes too
// (<file>.lock: the others wait for its write). The database is read while the App has it closed too (codexDBTarget).
// Without sqlite3 or the history database there is none, as before.

// codexAppHistoryVersion changes when already-written JSONL no longer matches what the converter produces. A mark
// without it is a legacy cache and is rebuilt once, even when the App database has not changed.
const codexAppHistoryVersion = 4

// codexAppHistoryWait bounds one read of the history database; a long chat's turns stream in well under it.
const codexAppHistoryWait = 30 * time.Second

// codexAppHistoryLockWait bounds the wait for another writer of a chat's transcript (a variable: tests shorten it).
var codexAppHistoryLockWait = codexAppHistoryWait

// After a failed write the chat is not tried again for codexAppHistoryRetry while nothing changed, and for
// codexAppHistoryRetrySoon when only the history database did (the App writes it all the time it works; a database this
// code cannot read must not cost a query a second). A change of the chat (updated_at_ms) is tried at once.
const codexAppHistoryRetry = 5 * time.Minute

var codexAppHistoryRetrySoon = 10 * time.Second // a variable: tests shorten it

var codexAppHistoryFailed = struct {
	sync.Mutex
	byPath map[string]codexHistoryFailure
}{byPath: map[string]codexHistoryFailure{}}

type codexHistoryFailure struct {
	updated int64  // the chat's updated_at_ms it failed at
	history string // and the history database's files
	at      time.Time
}

// codexHistoryMark is what <file>.mark says: the last turn written (its rollout_ordinal, turn_id and status in the
// database then; -1 and "" for none), what it is up to date with — the chat's updated_at_ms and the history database's
// files (the App may write a turn's rows after updating the chat, and finish a turn without updating it) — and the size
// the file had then (-1: no file, the chat has no finished turn).
type codexHistoryMark struct {
	Version int    `json:"version,omitempty"`
	Turn    int64  `json:"turn"`
	TurnID  string `json:"turn_id"`
	Status  string `json:"status"`
	Updated int64  `json:"updated"`
	History string `json:"history"`
	Size    int64  `json:"size"`
	Lineage string `json:"lineage,omitempty"`
}

// codexHistoryDBPath is the newest history database in home (thread_history_1.sqlite in 2026-11).
func codexHistoryDBPath(home string) string {
	return codexNumberedDB(home, "thread_history_")
}

// codexNumberedDBGlob is what codexNumberedDB globs with, indirected so a test can count the history/log Globs.
var codexNumberedDBGlob = filepath.Glob

func codexNumberedDB(home, prefix string) string {
	paths, _ := codexNumberedDBGlob(filepath.Join(home, prefix+"*.sqlite"))
	best, bestN := "", -1
	for _, p := range paths {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), prefix), ".sqlite"))
		if err == nil && n > bestN {
			best, bestN = p, n
		}
	}
	return best
}

func codexHomeTag(home string) string {
	h := sha256.Sum256([]byte(home))
	return hex.EncodeToString(h[:6])
}

// codexAppHistoryDir is where the transcripts of App chats with no rollout are written ("" without a state directory, or
// in a scoped node, which reads transcripts only from its own directories).
func (n *Node) codexAppHistoryDir() string {
	if n.dir == "" || n.sessionScope != nil {
		return ""
	}
	return filepath.Join(n.dir, "codex-app-history")
}

// codexAppHistoryFile is where the transcript of src, an App chat with no rollout, is written ("" when none can be).
func (n *Node) codexAppHistoryFile(src codexSource) string {
	dir := n.codexAppHistoryDir()
	if dir == "" || !validID(src.thread) {
		return ""
	}
	return filepath.Join(dir, codexHomeTag(src.home)+"-"+src.thread+".jsonl")
}

// withoutGoneRollout clears a source's rollout when the file is gone: the registry keeps a path once known (the App
// deletes a rollout after moving its history into the database), as listedCodexSource does for the list.
func withoutGoneRollout(src codexSource, pass *codexListPass) codexSource {
	if src.path != "" {
		if _, err := pass.stat(src.path); err != nil && errors.Is(err, os.ErrNotExist) {
			src.path = ""
		}
	}
	return src
}

// codexAppHistoryPath: the transcript written for src while it exists, else "": for an App chat with no rollout, and for
// a paginated chat whose history goes on in another rollout segment (its rollout path, or the lineage the file was
// written from: codex_history_lineage.go) whether a rollout is there or not. The list asks for every Codex row each
// second, and almost every row is an ordinary chat whose rollout is its transcript: that is known from the rollout's
// stat, which the list pass already has, so the file is not stat'ed for it (#5970).
func (n *Node) codexAppHistoryPath(src codexSource, pass *codexListPass) string {
	p := n.codexAppHistoryFile(src)
	if p == "" {
		return ""
	}
	rolloutless := func() bool { // an App chat whose rollout is gone, as the App still lists it
		src := withoutGoneRollout(src, pass)
		if src.path != "" {
			return false
		}
		_, ok := codexAppThreadOf(src)
		return ok
	}
	t, paginated := pass.paginatedThreadOf(src)
	if !paginated && !rolloutless() {
		return ""
	}
	if _, err := pass.stat(p); err != nil {
		return ""
	}
	if paginated && !codexContinuationPath(t.RolloutPath) && !rolloutless() {
		if mark, _ := readCodexHistoryMark(p); !codexContinuationPath(mark.Lineage) {
			return ""
		}
	}
	return p
}

// codexAppHistoryChat: session id is an App chat with no rollout, whose transcript comes from the App's database.
func (n *Node) codexAppHistoryChat(id string) (codexSource, codexAppThread, bool) {
	if t, ok := n.traexThread(id); ok {
		return codexSource{home: n.traexHome(), thread: id}, t, true
	}
	src, ok := n.codexSource(id)
	if !ok || n.codexBlocked(id) || n.ownedAssistantSource(id) { // as findTranscriptPath: no transcript of these here
		return codexSource{}, codexAppThread{}, false
	}
	if t, paginated := codexPaginatedThreadOf(src); paginated {
		if _, thread, err := n.codexOwner(id); err == nil && thread.path != "" {
			t.RolloutPath = thread.path
		}
		if codexContinuationPath(t.RolloutPath) {
			return src, t, true
		}
	}
	src = withoutGoneRollout(src, nil)
	if src.path != "" || rolloutPath(src.home, src.thread) != "" {
		return codexSource{}, codexAppThread{}, false // a rollout is there: it is the transcript
	}
	t, ok := codexAppThreadOf(src)
	return src, t, ok
}

// refreshCodexAppHistory brings the transcript of session id up to date when it is an App chat with no rollout and the
// App's databases changed since: the turns finished since are appended, or the file is written anew. One writer at a
// time, across processes too (two nodes on one computer share the state directory): the others wait for its write (up to
// codexAppHistoryLockWait), so a chat opened by several requests at once has its transcript for each.
func (n *Node) refreshCodexAppHistory(id string) error {
	src, t, ok := n.codexAppHistoryChat(id)
	if !ok {
		return nil
	}
	p := n.codexAppHistoryFile(src)
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	release, err := lockCodexAppHistory(p + ".lock")
	if err != nil {
		n.logf("codex app history %s: %v", src.thread, err)
		return err
	}
	defer release()
	mark, size := readCodexHistoryMark(p)
	db := codexHistoryDBPath(src.home)
	history := ""
	if db != "" {
		history = fileStamp(db) + fileStamp(db+"-wal")
		if t.includeRunning {
			history = traexHistoryStamp(db, t.ID, history)
		}
	}
	if mark.Version == codexAppHistoryVersion && mark.Updated == t.UpdatedAtMs && mark.History == history && mark.Size == size && (!t.Paginated || mark.Lineage == t.RolloutPath) {
		return nil // up to date
	}
	if db == "" {
		return nil
	}
	codexAppHistoryFailed.Lock()
	f, failed := codexAppHistoryFailed.byPath[p]
	codexAppHistoryFailed.Unlock()
	if failed && f.updated == t.UpdatedAtMs && (time.Since(f.at) < codexAppHistoryRetrySoon || f.history == history && time.Since(f.at) < codexAppHistoryRetry) {
		return errors.New("Codex history is waiting to retry a failed read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexAppHistoryWait)
	defer cancel()
	lineage := ""
	if t.Paginated {
		lineage = t.RolloutPath
	}
	grow := !t.includeRunning && mark.Version == codexAppHistoryVersion && mark.Size >= 0 && mark.Size == size && mark.TurnID != "" && mark.Lineage == lineage
	res, err := writeCodexAppHistory(ctx, db, t, p, mark, grow)
	if errors.Is(err, errCodexHistoryChanged) { // a written turn is not what it was (the App rolled back): anew
		res, err = writeCodexAppHistory(ctx, db, t, p, codexHistoryMark{Turn: -1}, false)
	}
	if err == nil {
		_, size = readCodexHistoryMark(p)
		err = writeCodexHistoryMark(p, codexHistoryMark{Version: codexAppHistoryVersion, Turn: res.turn, TurnID: res.turnID, Status: res.status,
			Updated: t.UpdatedAtMs, History: history, Size: size, Lineage: lineage})
	}
	codexAppHistoryFailed.Lock()
	if err != nil {
		codexAppHistoryFailed.byPath[p] = codexHistoryFailure{t.UpdatedAtMs, history, time.Now()}
	} else {
		delete(codexAppHistoryFailed.byPath, p)
	}
	codexAppHistoryFailed.Unlock()
	if err != nil {
		n.logf("codex app history %s: %v", src.thread, err)
		return err
	}
	n.mu.Lock()
	delete(n.metas, p)
	n.mu.Unlock()
	live := map[string]codexAppThread{}
	for id, row := range codexAppThreads(src.home) {
		live[id] = row
	}
	for id, row := range codexPaginatedThreads(src.home) {
		live[id] = row
	}
	pruneCodexAppHistory(filepath.Dir(p), codexHomeTag(src.home), live)
	return nil
}

// lockCodexAppHistory takes the lock at path, waiting up to codexAppHistoryLockWait for another writer (of this process
// or another) to finish; any other error (the file cannot be made, the file system has no locks) is returned at once.
func lockCodexAppHistory(path string) (func(), error) {
	deadline := time.Now().Add(codexAppHistoryLockWait)
	for {
		release, err := link.LockFile(path, false)
		if err == nil || !errors.Is(err, link.ErrLocked) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// readCodexHistoryMark is p's mark and p's size now (-1 when there is no file).
func readCodexHistoryMark(p string) (codexHistoryMark, int64) {
	size := int64(-1)
	if st, err := os.Stat(p); err == nil {
		size = st.Size()
	}
	m := codexHistoryMark{Turn: -1, Size: -2} // no mark: nothing matches it
	if b, err := os.ReadFile(p + ".mark"); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m, size
}

func writeCodexHistoryMark(p string, m codexHistoryMark) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(p+".mark", b, 0o600)
}

// pruneCodexAppHistory removes the files of home's chats the App no longer lists, and temporary files a stopped write left
// behind.
func pruneCodexAppHistory(dir, tag string, live map[string]codexAppThread) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".write-") {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if live == nil || !strings.HasPrefix(name, tag+"-") {
			continue
		}
		thread := strings.TrimPrefix(name, tag+"-")
		for _, ext := range []string{".lock", ".mark", ".jsonl"} {
			thread = strings.TrimSuffix(thread, ext)
		}
		if _, ok := live[thread]; !ok {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

// errCodexHistoryChanged: a turn the file has is not in the database as it was (the App rolled the chat back).
var errCodexHistoryChanged = errors.New("a written turn changed")

type codexHistoryWrite struct {
	turn           int64  // the last turn written (rollout_ordinal); the mark's when none was
	turnID, status string // its turn_id, and its status in the database ("inProgress" for one written as interrupted)
}

// writeCodexAppHistory writes thread t's turns to path: when grow, those after the mark's last turn, appended (that turn
// must still be the first the database gives, as it was: else errCodexHistoryChanged); else all of them, as a new file
// (through a temporary one renamed over it) beginning with session_meta, and no file when no turn finished. A finished
// turn is written as it streams in; the last turn while it still runs is left for later; one that never finished but has
// turns after it is written as interrupted.
func writeCodexAppHistory(ctx context.Context, db string, t codexAppThread, path string, mark codexHistoryMark, grow bool) (codexHistoryWrite, error) {
	res := codexHistoryWrite{turn: -1}
	if grow {
		res = codexHistoryWrite{turn: mark.Turn, turnID: mark.TurnID, status: mark.Status}
	}
	sqlite, err := codexSQLiteProgram()
	if err != nil {
		return res, err
	}
	if !validID(t.ID) {
		return res, fmt.Errorf("bad thread id")
	}
	// The output first: a process once started is always waited for.
	var file *os.File
	if grow {
		file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	} else {
		file, err = os.CreateTemp(filepath.Dir(path), ".write-*")
	}
	if err != nil {
		return res, err
	}
	defer func() {
		file.Close()
		if !grow {
			os.Remove(file.Name()) // close first: Windows cannot remove the open temporary file
		}
	}()
	w := bufio.NewWriterSize(file, 256<<10)
	line := func(at time.Time, typ string, payload any) {
		b, _ := json.Marshal(map[string]any{"timestamp": at.UTC().Format(time.RFC3339Nano), "type": typ, "payload": payload})
		w.Write(b)
		w.WriteByte('\n')
	}

	from := int64(-1)
	if grow {
		from = mark.Turn // read again: it must still be the turn written
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// One query, one snapshot: each turn (in order) with its items (in order); the mark's turn, read again only to see it
	// is as it was, without its items. Every value is one line: JSON escapes control characters within its strings, so
	// neither the separator nor a newline appears in one, and the line breaks it may have between values are blanked.
	oneLine := func(col string) string {
		return `REPLACE(REPLACE(COALESCE(` + col + `, ''), char(10), ' '), char(13), ' ')`
	}
	cte, err := codexHistoryTurnsSQL(filepath.Dir(db), t)
	if err != nil {
		return res, err
	}
	q := cte + fmt.Sprintf(`SELECT t.history_ordinal, t.turn_id, t.status, COALESCE(t.duration_ms, -1), `+oneLine("t.error_json")+`, `+
		`COALESCE(i.created_at_ms, 0), COALESCE(i.item_type, ''), `+oneLine("i.item_json")+` FROM history t `+
		`LEFT JOIN thread_items i ON i.thread_id = t.thread_id AND i.turn_id = t.turn_id AND t.history_ordinal > %d `+
		`WHERE t.history_ordinal >= %d ORDER BY t.history_ordinal, i.rollout_ordinal`, from, from)
	cmd := exec.CommandContext(ctx, sqlite, "-readonly", "-bail", "-noheader", "-list", "-separator", "\x1f", "-cmd", ".timeout 2000", codexDBTarget(db), q)
	hideCodexSQLiteWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, err
	}
	if err := cmd.Start(); err != nil {
		return res, err
	}

	type turn struct {
		ordinal     int64
		id, status  string
		durationMs  int64
		errJSON     string
		last        time.Time
		final, done bool     // done: written before (the mark's turn)
		held        [][2]any // a turn not finished: its items, written only once a later turn shows it ended
		heldStart   time.Time
	}
	metaWritten := grow
	startTurn := func(c *turn, at time.Time) {
		if !metaWritten {
			line(at, "session_meta", map[string]any{"id": t.ID, "cwd": t.Cwd, "originator": "Codex Desktop", "source": "vscode"})
			metaWritten = true
		}
		line(at, "event_msg", map[string]any{"type": "task_started", "turn_id": c.id})
	}
	closeTurn := func(c *turn, aborted bool) {
		closing := map[string]any{"type": "task_complete", "turn_id": c.id}
		switch {
		case aborted || c.status == "interrupted":
			closing = map[string]any{"type": "turn_aborted", "turn_id": c.id}
		case c.status == "failed":
			if json.Valid([]byte(c.errJSON)) {
				closing["error"] = json.RawMessage(c.errJSON)
			} else {
				closing["error"] = map[string]string{"message": "The turn failed."}
			}
		}
		if c.durationMs >= 0 && closing["type"] == "task_complete" {
			closing["duration_ms"] = c.durationMs
		}
		line(c.last, "event_msg", closing)
		res.turn, res.turnID, res.status = c.ordinal, c.id, c.status
	}
	// ended: c is followed by a later turn, so it is over.
	ended := func(c *turn) {
		switch {
		case c == nil || c.done:
		case c.final:
			closeTurn(c, false)
		default:
			startTurn(c, c.heldStart)
			for _, it := range c.held {
				line(it[0].(time.Time), "event_msg", map[string]any{"type": "item_completed", "item": it[1]})
			}
			closeTurn(c, true)
		}
	}
	at := time.UnixMilli(t.UpdatedAtMs)
	if t.Paginated && !grow {
		originator := "Codex"
		if t.Source == "vscode" {
			originator = "Codex Desktop"
		}
		line(at, "session_meta", map[string]any{"id": t.ID, "cwd": t.Cwd, "originator": originator, "source": t.Source})
		metaWritten = true
	}
	var cur *turn
	changed := false
	r := bufio.NewReaderSize(stdout, 1<<20)
	var readErr error
	for !changed {
		row, err := r.ReadBytes('\n')
		if parts := strings.SplitN(strings.TrimSuffix(string(row), "\n"), "\x1f", 8); len(parts) == 8 {
			ordinal, _ := strconv.ParseInt(parts[0], 10, 64)
			if ms, _ := strconv.ParseInt(parts[5], 10, 64); ms > 0 {
				at = time.UnixMilli(ms)
			}
			if cur == nil || cur.ordinal != ordinal {
				if grow && cur == nil && (ordinal != mark.Turn || parts[1] != mark.TurnID || parts[2] != mark.Status) {
					changed = true
					continue
				}
				ended(cur)
				dur, _ := strconv.ParseInt(parts[3], 10, 64)
				status := parts[2]
				cur = &turn{ordinal: ordinal, id: parts[1], status: status, durationMs: dur, errJSON: parts[4], last: at, heldStart: at,
					final: status == "completed" || status == "failed" || status == "interrupted", done: grow && cur == nil}
				if cur.final && !cur.done {
					startTurn(cur, at)
				}
			}
			cur.last = at
			if item := codexRolloutItem(parts[6], []byte(parts[7])); item != nil && !cur.done {
				if cur.final {
					line(at, "event_msg", map[string]any{"type": "item_completed", "item": item})
				} else {
					cur.held = append(cur.held, [2]any{at, item})
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	if changed || readErr != nil {
		cancel() // the rest of the query is not needed
	}
	waitErr := cmd.Wait()
	switch {
	case changed:
		return res, errCodexHistoryChanged
	case readErr != nil:
		return res, readErr
	case waitErr != nil:
		return res, fmt.Errorf("%v: %s", waitErr, strings.TrimSpace(stderr.String()))
	case grow && cur == nil:
		return res, errCodexHistoryChanged // the mark's turn is gone
	}
	if cur != nil && !cur.done && cur.final {
		closeTurn(cur, false) // the last turn while it still runs is left for later
	} else if cur != nil && !cur.done && t.includeRunning {
		// Terminal CLIs have no separate live stream. Include their stored current items
		// without inventing a completion; the next revision rebuilds this snapshot.
		startTurn(cur, cur.heldStart)
		for _, it := range cur.held {
			line(it[0].(time.Time), "event_msg", map[string]any{"type": "item_completed", "item": it[1]})
		}
		res.turn, res.turnID, res.status = cur.ordinal, cur.id, cur.status
	}
	if err := w.Flush(); err != nil {
		return res, err
	}
	if err := file.Close(); err != nil {
		return res, err
	}
	switch {
	case grow: // appended
		return res, nil
	case res.turn < 0 && !t.Paginated: // an App-only chat without a completed turn keeps its previous behavior
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
		return res, nil
	}
	return res, os.Rename(file.Name(), path)
}

// codexShellArgv splits the App's command line ("/bin/bash -lc 'git status'") into the argv a rollout carries, so the
// transcript reader drops the login shell around it as it does for a rollout's (transcript.shellCommand). The command is
// one shell word, unquoted as a POSIX shell does; one that is not (several words, an open quote) is kept whole.
func codexShellArgv(command string) []string {
	fields := strings.SplitN(command, " ", 3)
	if len(fields) != 3 || (fields[1] != "-lc" && fields[1] != "-c") || !codexShells[path.Base(fields[0])] {
		return []string{command}
	}
	if inner, ok := shellWord(fields[2]); ok {
		return []string{fields[0], fields[1], inner}
	}
	return []string{command}
}

// codexShells are the shells whose "-c"/"-lc" the App puts around a command.
var codexShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true}

// shellWord unquotes s as one POSIX shell word: '…' as it is, "…" with \\ \" \$ \` and a backslashed newline, a
// backslash outside quotes escaping the next character. false when s is not one whole word.
func shellWord(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return "", false
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case '"':
			for i++; ; i++ {
				if i >= len(s) {
					return "", false
				}
				if s[i] == '"' {
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\\\"$`\n", s[i+1]) >= 0 {
					i++
					if s[i] == '\n' {
						continue
					}
				}
				b.WriteByte(s[i])
			}
		case '\\':
			if i+1 >= len(s) {
				return "", false
			}
			i++
			b.WriteByte(s[i])
		case ' ', '\t', '\n':
			return "", false // a second word
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// codexRolloutItem turns one of the app-server's ThreadItems (v2, camelCase) into the item a rollout's item_completed
// carries (transcript/codex.go codexItem); nil for a kind the chat does not show (a sleep, a subagent's progress) and for
// an item still running.
func codexRolloutItem(kind string, raw []byte) map[string]any {
	var v struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Path string `json:"path"`
		} `json:"content"`
		Text             string          `json:"text"`
		Phase            string          `json:"phase"`
		Delivery         string          `json:"delivery"`
		Questions        json.RawMessage `json:"questions"`
		Summary          []string        `json:"summary"`
		Command          string          `json:"command"`
		Status           string          `json:"status"`
		AggregatedOutput string          `json:"aggregatedOutput"`
		ExitCode         *int            `json:"exitCode"`
		DurationMs       *int64          `json:"durationMs"`
		Server           string          `json:"server"`
		Tool             string          `json:"tool"`
		Arguments        json.RawMessage `json:"arguments"`
		Result           *struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Data     string `json:"data"`
				MimeType string `json:"mimeType"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error     json.RawMessage `json:"error"`
		Path      string          `json:"path"`
		SavedPath string          `json:"savedPath"`
		Changes   []struct {
			Path string `json:"path"`
			Kind struct {
				Type string `json:"type"`
			} `json:"kind"`
			Diff string `json:"diff"`
		} `json:"changes"`
		Query  string `json:"query"`
		Action *struct {
			Type    string   `json:"type"`
			URL     string   `json:"url"`
			Queries []string `json:"queries"`
		} `json:"action"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Domain  string `json:"domain"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Status == "inProgress" {
		return nil
	}
	item := map[string]any{"id": v.ID}
	switch kind {
	case "userMessage":
		var content []map[string]string
		for _, c := range v.Content {
			switch c.Type {
			case "text":
				content = append(content, map[string]string{"type": "text", "text": c.Text})
			case "localImage":
				content = append(content, map[string]string{"type": "local_image", "path": c.Path})
			}
		}
		item["type"], item["content"] = "UserMessage", content
	case "agentMessage":
		item["type"], item["content"], item["phase"] = "AgentMessage", []map[string]string{{"type": "text", "text": v.Text}}, v.Phase
		if v.Delivery == "async" && len(v.Questions) > 0 {
			item["delivery"], item["questions"] = v.Delivery, v.Questions
		}
	case "reasoning":
		item["type"], item["summary_text"] = "Reasoning", v.Summary
	case "commandExecution":
		item["type"], item["command"], item["status"], item["aggregated_output"] = "CommandExecution", codexShellArgv(v.Command), v.Status, v.AggregatedOutput
		if v.ExitCode != nil {
			item["exit_code"] = *v.ExitCode
		}
		if v.DurationMs != nil && *v.DurationMs >= 0 {
			item["duration"] = map[string]int64{"secs": *v.DurationMs / 1000, "nanos": *v.DurationMs % 1000 * 1_000_000}
		}
	case "fileChange":
		changes := map[string]map[string]string{}
		for _, c := range v.Changes {
			changes[c.Path] = map[string]string{"type": c.Kind.Type, "unified_diff": c.Diff}
		}
		item["type"], item["changes"], item["status"] = "FileChange", changes, v.Status
	case "mcpToolCall":
		item["type"], item["server"], item["tool"], item["arguments"], item["status"] = "McpToolCall", v.Server, v.Tool, v.Arguments, v.Status
		if v.Result != nil {
			item["result"] = v.Result
		}
		if len(v.Error) > 0 {
			item["error"] = v.Error
		}
	case "imageView":
		item["type"], item["path"] = "ImageView", v.Path
	case "imageGeneration": // the picture it made, shown as a rollout shows an image it looked at
		if v.SavedPath == "" {
			return nil
		}
		item["type"], item["path"] = "ImageView", v.SavedPath
	case "webSearch":
		item["type"], item["kind"], item["query"], item["results"] = "Extension", "web.search", v.Query, v.Results
		if v.Action != nil {
			item["action"] = v.Action
		}
	case "collabAgentToolCall", "subAgentActivity", "plan":
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return nil
		}
		item["type"] = map[string]string{"collabAgentToolCall": "CollabAgentToolCall", "subAgentActivity": "SubAgentActivity", "plan": "Plan"}[kind]
		for from, to := range map[string]string{
			"senderThreadId": "sender_thread_id", "receiverThreadIds": "receiver_thread_ids",
			"receiverAgents": "receiver_agents", "agentsStates": "agents_states",
			"agentThreadId": "agent_thread_id", "agentPath": "agent_path", "reasoningEffort": "reasoning_effort",
		} {
			if value, ok := item[from]; ok {
				item[to] = value
				delete(item, from)
			}
		}
	case "contextCompaction":
		item["type"] = "ContextCompaction"
	default:
		return nil
	}
	return item
}
