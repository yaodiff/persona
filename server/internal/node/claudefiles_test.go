package node

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// claudeFileAt writes home's sessions/<pid>.json for session id and dates it at mod.
func claudeFileAt(t *testing.T, home string, pid int, id string, mod time.Time) string {
	t.Helper()
	writeClaudeSession(t, home, pid, id, "cli")
	path := filepath.Join(home, ".claude", "sessions", fmt.Sprintf("%d.json", pid))
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func liveSessionID(t *testing.T, n *Node, pid int) string {
	t.Helper()
	for _, c := range n.liveClaudes() {
		if c.pid == pid {
			return c.SessionID
		}
	}
	return ""
}

// A file that has not changed since it was read is not read again on the next rebuild; a change of its size or
// modification time is.
func TestLiveClaudesReadsAQuietFileOnce(t *testing.T) {
	n, _, home := herdrNode(t)
	const a, b = "0bd545f1-681d-4d98-bf5a-eb7c1251a0f0", "2e2f25fd-6a4c-4ebb-8615-788af4aaa7e2" // the same length
	pid := os.Getpid()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	claudeFileAt(t, home, pid, a, old)
	if got := liveSessionID(t, n, pid); got != a {
		t.Fatalf("first read: %q", got)
	}
	// The same size and time: kept, so the new contents are not read (only a test can rewrite a file and keep both).
	claudeFileAt(t, home, pid, b, old)
	if got := liveSessionID(t, n, pid); got != a {
		t.Fatalf("an unchanged file was read again: %q", got)
	}
	claudeFileAt(t, home, pid, b, old.Add(time.Second))
	if got := liveSessionID(t, n, pid); got != b {
		t.Fatalf("a file with a new time was not read again: %q", got)
	}
}

// A file written just now is read again on every pass until it has been quiet for claudeFileRacy: a second write
// within one tick of the file system's clock, of the same size (busy → idle), would otherwise go unseen.
func TestLiveClaudesRereadsAFileWrittenJustNow(t *testing.T) {
	n, _, home := herdrNode(t)
	const a, b = "0bd545f1-681d-4d98-bf5a-eb7c1251a0f0", "2e2f25fd-6a4c-4ebb-8615-788af4aaa7e2"
	pid := os.Getpid()
	now := time.Now()
	claudeFileAt(t, home, pid, a, now)
	if got := liveSessionID(t, n, pid); got != a {
		t.Fatalf("first read: %q", got)
	}
	claudeFileAt(t, home, pid, b, now) // same size, same time
	if got := liveSessionID(t, n, pid); got != b {
		t.Fatalf("a file written within claudeFileRacy was taken from the cache: %q", got)
	}
}

// A claude that exits removes its file: the kept copy goes with it, and a file left by a claude that exited does not
// count though it was kept while that claude ran.
func TestLiveClaudesForgetsRemovedAndDeadFiles(t *testing.T) {
	n, _, home := herdrNode(t)
	old := time.Now().Add(-time.Hour)
	path := claudeFileAt(t, home, os.Getpid(), "0bd545f1-681d-4d98-bf5a-eb7c1251a0f0", old)
	claudeFileAt(t, home, 999999, "2e2f25fd-6a4c-4ebb-8615-788af4aaa7e2", old) // no such process
	if got := len(n.liveClaudes()); got != 1 {
		t.Fatalf("%d live claudes, want 1 (the dead one's file does not count)", got)
	}
	if got := len(n.cfiles.files); got != 2 {
		t.Fatalf("%d files kept, want 2", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := len(n.liveClaudes()); got != 0 {
		t.Fatalf("%d live claudes after the file went", got)
	}
	if _, ok := n.cfiles.files[path]; ok || len(n.cfiles.files) != 1 {
		t.Fatalf("kept files after one was removed: %v", n.cfiles.files)
	}
}
