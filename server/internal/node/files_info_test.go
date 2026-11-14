package node

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
)

// linkFixture makes a folder with a file, a subfolder and links: relative to the file, absolute to the subfolder, to
// nothing, a loop, and into ~/.ssh. Returns the folder.
func linkFixture(t *testing.T, home string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := filepath.Join(home, "proj")
	for _, d := range []string{dir, filepath.Join(dir, "sub"), filepath.Join(home, ".ssh")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world"), 0o640); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies the umask; TestStatDetail expects 0640 under any umask (077 on the PR watcher's host).
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"rel":  "a.txt",
		"abs":  filepath.Join(dir, "sub"),
		"gone": "missing.txt",
		"l1":   "l2",
		"l2":   "l1",
		"key":  filepath.Join(home, ".ssh", "id_ed25519"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The listing says what each link says and which lead nowhere, never following one further than telling a folder from
// a file; a link into a secret folder is neither broken nor followed.
func TestListDirShowsLinkTargets(t *testing.T) {
	n, home := testNode(t)
	dir := linkFixture(t, home)
	ents, _ := listNames(t, n, dir)
	by := map[string]DirEntry{}
	for _, e := range ents {
		by[e.Name] = e
	}
	if e := by["rel"]; !e.Symlink || e.LinkTarget != "a.txt" || e.LinkBroken || e.Dir || e.Size != int64(len("hello world")) {
		t.Fatalf("rel = %+v, want a link to a.txt with its size", e)
	}
	if e := by["abs"]; !e.Symlink || !e.Dir || e.LinkTarget != filepath.Join(dir, "sub") || e.LinkBroken {
		t.Fatalf("abs = %+v, want a link to the sub folder", e)
	}
	for _, name := range []string{"gone", "l1", "l2"} {
		if e := by[name]; !e.Symlink || !e.LinkBroken || e.Dir {
			t.Fatalf("%s = %+v, want a broken link", name, e)
		}
	}
	if e := by["key"]; !e.Symlink || e.LinkBroken || e.LinkTarget == "" || e.Dir {
		t.Fatalf("key = %+v, want a link into ~/.ssh: not broken, not followed", e)
	}
	if e := by["a.txt"]; e.Symlink || e.LinkTarget != "" || e.LinkBroken {
		t.Fatalf("a.txt = %+v, want a plain file", e)
	}
}

func TestStatDetail(t *testing.T) {
	n, home := testNode(t)
	dir := linkFixture(t, home)
	stats := n.statPaths([]string{"a.txt", "sub", ".", "rel", "abs", "gone", "l1", "key", "~/.ssh/id_ed25519", "nope"}, dir, true)
	by := map[string]PathStat{}
	for _, s := range stats {
		by[s.Path] = s
	}
	me, _ := user.Current()

	f := by["a.txt"]
	if f.Kind != "file" || f.Size != 11 || f.Mode == nil || *f.Mode != 0o640 || f.ModTime == nil || f.Symlink {
		mode := "none"
		if f.Mode != nil {
			mode = fmt.Sprintf("%04o", *f.Mode)
		}
		t.Fatalf("a.txt: mode %s, want 0640; %+v", mode, f)
	}
	if runtime.GOOS != "windows" {
		if me != nil && f.Owner != me.Username {
			t.Fatalf("owner %q, want %q", f.Owner, me.Username)
		}
		if f.Group == "" || f.ChangeTime == nil {
			t.Fatalf("a.txt has no group or ctime: %+v", f)
		}
	}
	if runtime.GOOS == "darwin" && f.BirthTime == nil {
		t.Fatalf("no birth time on a Mac: %+v", f)
	}
	if s := by["sub"]; s.Kind != "dir" || s.Items == nil || *s.Items != 0 || s.ItemsMore {
		t.Fatalf("sub = %+v, want an empty folder", s)
	}
	if s := by["."]; s.Kind != "dir" || s.Items == nil || *s.Items != 8 {
		t.Fatalf(". = %+v, want 8 items", s)
	}

	realA, _ := filepath.EvalSymlinks(filepath.Join(dir, "a.txt"))
	if s := by["rel"]; !s.Symlink || s.LinkTarget != "a.txt" || s.LinkReal != realA || s.Kind != "file" || s.Size != 11 || s.LinkBroken {
		t.Fatalf("rel = %+v, want a link to %s", s, realA)
	}
	if s := by["abs"]; !s.Symlink || s.Kind != "dir" || s.LinkReal == "" {
		t.Fatalf("abs = %+v", s)
	}
	for _, name := range []string{"gone", "l1"} {
		if s := by[name]; !s.Symlink || !s.LinkBroken || s.Kind != "link" || s.LinkReal != "" || s.LinkTarget == "" {
			t.Fatalf("%s = %+v, want a broken link", name, s)
		}
	}
	if s := by["key"]; !s.Symlink || s.LinkTarget == "" || s.LinkReal != "" || s.Kind != "missing" || s.Mode != nil {
		t.Fatalf("key = %+v: a link into ~/.ssh says what it says and nothing about what it leads to", s)
	}
	if s := by["~/.ssh/id_ed25519"]; s.Kind != "missing" || s.Mode != nil {
		t.Fatalf("the key itself = %+v, want missing", s)
	}
	if s := by["nope"]; s.Kind != "missing" {
		t.Fatalf("nope = %+v", s)
	}

	// Without detail, as before: no Info fields.
	plain := n.statPaths([]string{"a.txt", "rel"}, dir)
	if plain[0].Mode != nil || plain[0].ModTime != nil || plain[1].Symlink {
		t.Fatalf("fs.stat without detail grew fields: %+v", plain)
	}
}

func TestUnixModeKeepsSpecialBits(t *testing.T) {
	if m := unixMode(os.ModeSetuid | os.ModeSticky | 0o755); m != 0o5755 {
		t.Fatalf("mode %o, want 5755", m)
	}
	if m := unixMode(os.ModeSetgid | 0o640); m != 0o2640 {
		t.Fatalf("mode %o, want 2640", m)
	}
}
