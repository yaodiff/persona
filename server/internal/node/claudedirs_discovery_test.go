package node

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func retainedClaudeDirs(t testing.TB) (*Node, []string) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	dirs := []string{filepath.Join(home, ".claude"), filepath.Join(home, "profile1"), filepath.Join(home, "profile2")}
	for _, dir := range dirs {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n := &Node{home: home, dir: filepath.Join(home, "node"), cdirs: newClaudeDirs(), state: &stateFile{Sessions: map[string]sessionState{}}}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.setBlockedClaudeDirs([]string{"~/unused"})
	for i := range 1000 {
		n.state.Sessions[strconv.Itoa(i)] = sessionState{ConfigDir: dirs[i%len(dirs)]}
	}
	return n, dirs
}

func TestClaudeDiscoveryResolvesDistinctDirectoriesOnce(t *testing.T) {
	n, dirs := retainedClaudeDirs(t)
	for range 2 {
		calls := map[string]int{}
		got := n.discoverClaudeDirs(func(path string) (string, error) {
			calls[path]++
			return filepath.EvalSymlinks(path)
		})
		if len(got) != len(dirs) || got[0] != dirs[0] || len(calls) != len(dirs) {
			t.Fatalf("directories %v, calls %v", got, calls)
		}
		for _, dir := range dirs {
			if calls[dir] != 1 || !slices.Contains(got, dir) {
				t.Fatalf("directories %v, calls %v", got, calls)
			}
		}
	}
}

func BenchmarkClaudeDiscoveryRetainedSessions(b *testing.B) {
	n, _ := retainedClaudeDirs(b)
	b.ReportAllocs()
	for b.Loop() {
		n.claudeConfigDirs()
	}
}

func TestClaudeDiscoverySeesDirectoryChangesImmediately(t *testing.T) {
	n, dirs := retainedClaudeDirs(t)
	profile := filepath.Join(n.home, "new-profile")
	contains := func(path string, want bool) {
		t.Helper()
		if got := n.claudeConfigDirs(); slices.Contains(got, path) != want {
			t.Fatalf("contains %s: want %v, got %v", path, want, got)
		}
	}
	n.setKnownClaudeDirs([]string{profile})
	contains(profile, false)
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	contains(profile, true)
	n.setKnownClaudeDirs(nil)
	contains(profile, false)
	n.claudeStartDir.Store(&profile)
	contains(profile, true)
	n.claudeStartDir.Store(nil)
	contains(profile, false)
	n.state.put("new", sessionState{ConfigDir: profile})
	contains(profile, true)
	n.setBlockedClaudeDirs([]string{profile})
	contains(profile, false)
	n.setBlockedClaudeDirs(nil)
	contains(profile, true)
	if err := os.Remove(profile); err != nil {
		t.Fatal(err)
	}
	contains(profile, false)
	contains(dirs[0], true)
}

func TestClaudeDiscoveryResolvesParentAfterSymlink(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	pid := os.Getpid()
	first, second := filepath.Join(home, "aaaaaaaa"), filepath.Join(home, "bbbbbbbb")
	for _, profile := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(profile, "subdir"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeClaudeConfigSession(t, profile, pid, filepath.Base(profile))
	}
	// A lexical join would read this session instead of following the link first.
	writeClaudeConfigSession(t, home, pid, "cccccccc")
	for _, tc := range []struct{ name, spelling string }{{"absolute", filepath.Join(home, "link")}, {"tilde", "~/link"}} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(home, "link")
			dir := tc.spelling + string(filepath.Separator) + ".."
			n := &Node{home: home, dir: filepath.Join(home, "node"), cdirs: newClaudeDirs()}
			n.cdirs.scan = func() map[int]procEnv { return map[int]procEnv{pid: {ConfigDir: dir}} }
			for _, profile := range []string{first, second} {
				if err := os.Symlink(filepath.Join(profile, "subdir"), link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				got := liveSessionID(t, n, pid)
				byPID := readClaudeSessionID(n.claudeSessionFile(pid))
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if want := filepath.Base(profile); got != want || byPID != want {
					t.Fatalf("with %q pointing to %q, session list returned %q and PID lookup returned %q", dir, profile, got, byPID)
				}
			}
		})
	}
}

// The default remains eligible. A different profile whose spelling cleans to the same path can still be blocked.
func TestClaudeDiscoveryBlocksDistinctParentSpelling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	profile := filepath.Join(home, "profile")
	subdir := filepath.Join(profile, "subdir")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(subdir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	n := &Node{home: home, dir: filepath.Join(home, "node"), cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.setKnownClaudeDirs([]string{link + string(filepath.Separator) + ".."})
	n.setBlockedClaudeDirs([]string{profile})
	if got := n.claudeConfigDirs(); !slices.Equal(got, []string{home}) {
		t.Fatalf("blocking the profile should leave just the default %q, got %v", home, got)
	}
}

func TestClaudeDiscoveryMissingDefaultWithParent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "gone") + string(filepath.Separator) + ".."
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	n := &Node{home: home, dir: filepath.Join(home, "node")}
	pid := os.Getpid()
	writeClaudeConfigSession(t, home, pid, "cccccccc")
	if got := n.claudeConfigDirs(); !slices.Equal(got, []string{dir}) {
		t.Fatalf("missing default fallback = %v, want %q", got, dir)
	}
	if got, byPID := liveSessionID(t, n, pid), readClaudeSessionID(n.claudeSessionFile(pid)); got != "" || byPID != "" {
		t.Fatalf("missing default: live list returned %q and PID lookup returned %q", got, byPID)
	}
}
