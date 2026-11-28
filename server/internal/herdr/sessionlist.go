package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

// `herdr session list` needs no server: herdr reads the sessions directory and knocks on each session's socket. A
// node on the default server asks for it every 5 s (node/hostherdr_sessions.go), so a computer with herdr installed and
// nothing running started 12 herdr processes a minute for the same answer. With nothing running, that answer changes
// only when:
//   - another herdr would run (exec.LookPath finds another file, or the file changed),
//   - a session directory is made or removed (an entry of <config dir>/sessions),
//   - a server starts: it creates its socket file, so one of the listed sockets appears.
//
// So a list that found nothing running is kept with those three and given again while none of them changed, for the
// cost of a lookup, a directory read and a stat per session. A list with a session running is never kept: a server
// that stops can leave its socket file behind, and only a connect tells that from a live one.

// listMemo is the last `herdr session list` that found nothing running, and what it depended on.
type listMemo struct {
	mu      sync.Mutex
	out     []byte // nil: none kept
	bin     string // the herdr that answered (exec.LookPath)
	binInfo os.FileInfo
	dir     string   // the sessions directory
	names   []string // its entries, read before herdr read them
	sockets []string // every listed session's socket: none was there
}

// sessionList is the output of `herdr session list --json` (run for at most limit), or the last one while it cannot
// have changed (listMemo).
func (c *Client) sessionList(ctx context.Context, limit time.Duration) ([]byte, error) {
	m := &c.lists
	if out := m.still(c.Bin); out != nil && ctx.Err() == nil {
		return out, nil
	}
	// What herdr will read, read first: a session made while it runs then shows up as a change.
	bin, binInfo := lookBin(c.Bin)
	dir := filepath.Join(configDir(), "sessions")
	names, namesErr := dirNames(dir)
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Bin, "session", "list", "--json").Output()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = nil
	// On Windows the sockets are named pipes, not files: a server starting there creates nothing a stat can see.
	if err != nil || binInfo == nil || namesErr != nil || configDir() == "" || runtime.GOOS == "windows" {
		return out, err
	}
	if sockets, ok := nothingRunning(out, dir); ok {
		m.out, m.bin, m.binInfo, m.dir, m.names, m.sockets = out, bin, binInfo, dir, names, sockets
	}
	return out, nil
}

// still is the kept list when nothing it depended on changed, nil otherwise.
func (m *listMemo) still(name string) []byte {
	m.mu.Lock()
	out, bin, binInfo, dir, names, sockets := m.out, m.bin, m.binInfo, m.dir, m.names, m.sockets
	m.mu.Unlock()
	if out == nil || filepath.Join(configDir(), "sessions") != dir {
		return nil
	}
	if p, info := lookBin(name); p != bin || info == nil || !sameFile(info, binInfo) {
		return nil
	}
	for _, s := range sockets {
		if _, err := os.Lstat(s); !errors.Is(err, fs.ErrNotExist) {
			return nil
		}
	}
	if now, err := dirNames(dir); err != nil || !slices.Equal(now, names) {
		return nil
	}
	return out
}

// lookBin is the file a command named name runs and its stat; a nil FileInfo when there is none.
func lookBin(name string) (string, os.FileInfo) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", nil
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", nil
	}
	return p, info
}

// sameFile: a and b are the same file, unchanged in size and modification time.
func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// dirNames are the sorted entry names of dir; none for a directory not there.
func dirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}

// nothingRunning: the list out says no session is running, all are in the layout under the sessions directory dir
// (the default one in its parent), and none of their sockets is there now. It returns the sockets.
func nothingRunning(out []byte, dir string) ([]string, bool) {
	var res struct {
		Sessions []struct {
			Default bool   `json:"default"`
			Running bool   `json:"running"`
			Dir     string `json:"session_dir"`
			Socket  string `json:"socket_path"`
		} `json:"sessions"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil, false
	}
	sockets := make([]string, 0, len(res.Sessions)+1)
	hasDefault := false
	for _, s := range res.Sessions {
		got, want := filepath.Dir(s.Dir), dir // a named session's directory is in the sessions directory
		if s.Default {
			hasDefault = true
			got, want = s.Dir, filepath.Dir(dir) // the default one's is the config directory
		}
		if s.Running || s.Socket == "" || filepath.Clean(got) != filepath.Clean(want) {
			return nil, false
		}
		if _, err := os.Lstat(s.Socket); !errors.Is(err, fs.ErrNotExist) {
			return nil, false
		}
		sockets = append(sockets, s.Socket)
	}
	if !hasDefault {
		defSock := filepath.Join(filepath.Dir(dir), "herdr.sock")
		if _, err := os.Lstat(defSock); !errors.Is(err, fs.ErrNotExist) {
			return nil, false
		}
		sockets = append(sockets, defSock)
	}
	return sockets, true
}
