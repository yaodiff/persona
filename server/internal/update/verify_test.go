package update

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// otherPlatform is a release platform that is not this machine's: its binaries are checked without running them.
func otherPlatform() string {
	if runtime.GOOS+"-"+runtime.GOARCH == "linux-amd64" {
		return "linux-arm64"
	}
	return "linux-amd64"
}

// releaseDir writes a release's downloads into a new directory as publish.sh leaves them: personad for this machine
// (a script that answers `version -v` as the real one does) and for otherPlatform (bytes with the build number in
// them, as -X main.build leaves it), a program and a node build. build gives each one's build number by name.
func releaseDir(t *testing.T, version string, build func(name string) int64) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the binaries are shell scripts")
	}
	here, other := runtime.GOOS+"-"+runtime.GOARCH, otherPlatform()
	dir := t.TempDir()
	write := func(name string, body []byte) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(body)
		zw.Close()
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := func(name string) []byte {
		return []byte(fmt.Sprintf("#!/bin/sh\necho %s\necho 'channel release, build %d'\n", version, build(name)))
	}
	foreign := func(name string) []byte {
		return []byte(fmt.Sprintf("\x7fELF\x00main.version\x00%s\x00%d\x00rest of the binary", version, build(name)))
	}
	write("personad-"+here+".gz", script("personad-"+here+".gz"))
	write("persona-hub-"+here+".gz", script("persona-hub-"+here+".gz"))
	write("personad-"+other+".gz", foreign("personad-"+other+".gz"))
	write("persona-node-"+other+".gz", foreign("persona-node-"+other+".gz"))
	write("persona-link-windows-amd64.exe.gz", foreign("persona-link-windows-amd64.exe.gz"))
	return dir
}

func same(n int64) func(string) int64 { return func(string) int64 { return n } }

// A release whose binaries are all the manifest's build passes; it is what release manifest writes.
func TestVerifyReleaseAcceptsItsOwnBuild(t *testing.T) {
	dir := releaseDir(t, "0.2.1", same(202611110749))
	m, err := BuildManifest(dir, Manifest{Version: "0.2.1", Build: 202611110749})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 || len(m.Programs["persona-hub"]) != 1 || len(m.Nodes["persona-node"]) != 1 ||
		m.Links["windows-amd64"].Name != "persona-link-windows-amd64.exe.gz" {
		t.Fatalf("the release is not all there: %+v", m)
	}
	if err := VerifyRelease(context.Background(), dir, m); err != nil {
		t.Fatal(err)
	}
}

// v0.2.1 (#4522): latest.json said build 202611110749, every binary was 202611110745 (built by an earlier run). The
// binary this machine runs says so; one of another platform lacks the build number. Either way the release is refused.
func TestVerifyReleaseRefusesAnotherRunsBinaries(t *testing.T) {
	here, other := runtime.GOOS+"-"+runtime.GOARCH, otherPlatform()
	for _, tc := range []struct {
		stale string // the download built by the earlier run
		want  string
	}{
		{"personad-" + here + ".gz", "says"},
		{"persona-hub-" + here + ".gz", "says"},
		{"personad-" + other + ".gz", "is not build 202611110749"},
		{"persona-node-" + other + ".gz", "is not build 202611110749"},
		{"persona-link-windows-amd64.exe.gz", "is not build 202611110749"},
	} {
		t.Run(tc.stale, func(t *testing.T) {
			dir := releaseDir(t, "0.2.1", func(name string) int64 {
				if name == tc.stale {
					return 202611110745
				}
				return 202611110749
			})
			m, err := BuildManifest(dir, Manifest{Version: "0.2.1", Build: 202611110749})
			if err != nil {
				t.Fatal(err)
			}
			err = VerifyRelease(context.Background(), dir, m)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "another build") {
				t.Fatalf("%s from another run: %v", tc.stale, err)
			}
		})
	}
}

// A version that differs (the same build number, another -X main.version) is refused too, by the binary that runs here.
func TestVerifyReleaseRefusesAnotherVersion(t *testing.T) {
	dir := releaseDir(t, "0.2.0", same(202611110749))
	m, err := BuildManifest(dir, Manifest{Version: "0.2.1", Build: 202611110749})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRelease(context.Background(), dir, m); err == nil || !strings.Contains(err.Error(), `"0.2.0`) {
		t.Fatalf("another version: %v", err)
	}
}

// A download changed or gone after the manifest was written is not uploaded with it.
func TestVerifyReleaseRefusesAChangedOrMissingDownload(t *testing.T) {
	dir, other := releaseDir(t, "0.2.1", same(202611110749)), otherPlatform()
	m, err := BuildManifest(dir, Manifest{Version: "0.2.1", Build: 202611110749})
	if err != nil {
		t.Fatal(err)
	}
	name := "personad-" + other + ".gz"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not it"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRelease(context.Background(), dir, m); err == nil || !strings.Contains(err.Error(), "not the file the manifest lists") {
		t.Fatalf("a changed download: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRelease(context.Background(), dir, m); err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("a missing download: %v", err)
	}
	m.Files[other] = File{Name: "../" + name}
	if err := VerifyRelease(context.Background(), dir, m); err == nil || !strings.Contains(err.Error(), "invalid artifact name") {
		t.Fatalf("a name outside the directory: %v", err)
	}
}

// The build number is found wherever the binary is cut into writes, and only standing alone: inside a longer number it
// is another one.
func TestStampScan(t *testing.T) {
	const n = 202611110749
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"202611110749", true},
		{"x202611110749", true},
		{"202611110749x", true},
		{"..build 202611110749'\n..", true},
		{"1202611110749", false},
		{"2026111107490", false},
		{"202611110745", false},
		{"2026111107", false},
		{"", false},
		{"12026111107490 202611110749 ", true}, // a longer number first, then the build itself
	} {
		for chunk := 1; chunk <= len(tc.body)+1; chunk++ {
			s := newStampScan(n)
			for b := []byte(tc.body); len(b) > 0; {
				k := min(chunk, len(b))
				s.Write(b[:k])
				b = b[k:]
			}
			if got := s.done(); got != tc.want {
				t.Errorf("%q in writes of %d: found %v, want %v", tc.body, chunk, got, tc.want)
			}
		}
	}
}
