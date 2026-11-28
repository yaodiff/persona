package node

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// codexSQLiteProgramTTL bounds how long a lookup result is reused. A node started at login can retain an older PATH
// after SQLite is installed, so a missing sqlite3 is not cached forever: once this passes, PATH and the fallback
// directories are looked at again and a newly installed executable is picked up.
const codexSQLiteProgramTTL = time.Minute

// codexSQLiteLookPath and codexSQLiteNow are indirected so tests can count PATH lookups, simulate an install and
// advance the recovery TTL without touching the real environment or sleeping.
var (
	codexSQLiteLookPath = exec.LookPath
	codexSQLiteNow      = time.Now
)

// A node started at login can retain an older PATH after SQLite is installed. Keep PATH's explicit choice first, then
// use the platform's installed locations. The resolved program and a missing-sqlite3 verdict are cached for at most
// codexSQLiteProgramTTL: listing many sessions otherwise ran LookPath (and scanned every PATH directory) repeatedly.
func codexSQLiteProgram() (string, error) {
	dirs := codexSQLiteFallbackDirs()
	key := codexSQLiteLookupKey(dirs)
	now := codexSQLiteNow()

	codexSQLiteProgramMu.Lock()
	cached := codexSQLiteProgramCache
	fresh := cached.key == key && now.Sub(cached.at) < codexSQLiteProgramTTL
	if fresh {
		codexSQLiteProgramMu.Unlock()
		if cached.notFound {
			return "", codexSQLiteNotFound()
		}
		return cached.path, nil
	}
	codexSQLiteProgramMu.Unlock()

	path, err := lookupCodexSQLite(dirs)
	codexSQLiteProgramMu.Lock()
	codexSQLiteProgramCache = codexSQLiteProgramEntry{
		key:      key,
		at:       now,
		path:     path,
		notFound: errors.Is(err, exec.ErrNotFound),
	}
	codexSQLiteProgramMu.Unlock()
	return path, err
}

// codexSQLiteLookupKey names exactly what lookupCodexSQLite consults, so a test (or the user) changing PATH or the
// fallback directories starts from a fresh lookup instead of reading a verdict cached for different inputs.
func codexSQLiteLookupKey(dirs []string) string {
	return os.Getenv("PATH") + "\x00" + strings.Join(dirs, "\x00")
}

var (
	codexSQLiteProgramMu    sync.Mutex
	codexSQLiteProgramCache codexSQLiteProgramEntry
)

type codexSQLiteProgramEntry struct {
	key      string
	at       time.Time
	path     string
	notFound bool
}

// resetCodexSQLiteProgramCache clears the cached lookup; tests that replace the clock or LookPath hook call it first.
func resetCodexSQLiteProgramCache() {
	codexSQLiteProgramMu.Lock()
	codexSQLiteProgramCache = codexSQLiteProgramEntry{}
	codexSQLiteProgramMu.Unlock()
}

func codexSQLiteNotFound() error {
	return &exec.Error{Name: "sqlite3", Err: exec.ErrNotFound}
}

func lookupCodexSQLite(dirs []string) (string, error) {
	if path, err := codexSQLiteLookPath("sqlite3"); err == nil {
		return path, nil
	}
	return findCodexSQLite(dirs)
}

var codexSQLiteFallbackDirs = defaultCodexSQLiteFallbackDirs

func findCodexSQLite(dirs []string) (string, error) {
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if !filepath.IsAbs(dir) {
			continue // Neither an empty PATH entry nor the current directory is a fallback.
		}
		if path, err := codexSQLiteLookPath(filepath.Join(dir, "sqlite3")); err == nil {
			return path, nil
		}
	}
	return "", codexSQLiteNotFound()
}
