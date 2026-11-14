package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The session list is rebuilt every second (pollAgents) and reads every running claude's sessions/<pid>.json each
// time (liveClaudes): on a machine with 67 claudes that was 67 file reads and JSON decodes a second for files that
// change only when a claude writes its status. claudeFiles keeps each file as last parsed with the size and
// modification time it had, and parses it again when either differs. The pid check (sameProcess) still runs on
// every pass, so a claude that exited drops out as before.
//
// A file whose modification time was within claudeFileRacy of when it was read is not kept: a claude rewrites its
// file in place (status busy → idle, the same size), and two writes inside one tick of the file system's clock
// would otherwise leave the first one cached (git's "racily clean" rule).
const claudeFileRacy = 2 * time.Second

// claudeFiles is the parsed sessions/<pid>.json by path. The zero value is ready to use.
type claudeFiles struct {
	mu    sync.Mutex
	files map[string]claudeFile
}

type claudeFile struct {
	size int64
	mod  time.Time
	proc claudeProc
	ok   bool // the file decoded
}

// parse is path's claudeProc (without pid and dir), read again only when st says the file changed since it was kept;
// ok is false when the file did not read or decode.
func (c *claudeFiles) parse(path string, st os.FileInfo) (claudeProc, bool) {
	c.mu.Lock()
	e, hit := c.files[path]
	c.mu.Unlock()
	if hit && e.size == st.Size() && e.mod.Equal(st.ModTime()) {
		return e.proc, e.ok
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return claudeProc{}, false // gone meanwhile: nothing to keep
	}
	var p claudeProc
	ok := json.Unmarshal(b, &p) == nil
	c.mu.Lock()
	if time.Since(st.ModTime()) > claudeFileRacy {
		if c.files == nil {
			c.files = map[string]claudeFile{}
		}
		c.files[path] = claudeFile{size: st.Size(), mod: st.ModTime(), proc: p, ok: ok}
	} else {
		delete(c.files, path)
	}
	c.mu.Unlock()
	return p, ok
}

// keep drops the kept files in dirs that were not listed this pass (their claude removed them on exit), so the map
// holds only files that exist. Files in other directories (another caller's scope) are left alone.
func (c *claudeFiles) keep(dirs []string, listed map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for path := range c.files {
		if listed[path] {
			continue
		}
		dir := filepath.Dir(path)
		for _, d := range dirs {
			if filepath.Clean(d) == dir {
				delete(c.files, path)
				break
			}
		}
	}
}
