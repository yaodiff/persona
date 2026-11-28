package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishPackagesMigratesOnlyOwnedAliases(t *testing.T) {
	stage, dir := t.TempDir(), t.TempDir()
	put := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const canonical = "personad-linux-amd64"
	legacy := legacyDaemon + "-linux-amd64"
	put(dir, canonical, "old build")
	put(dir, legacy, "old build")
	put(dir, legacy+".version", "v1\n")
	put(stage, canonical, "new build")
	// An older Hub may already have prepared a regular legacy copy in its staging directory.
	put(stage, legacy, "stale staged copy")
	for _, name := range []string{"persona-node-linux-amd64", "persona-manager-linux-amd64", "persona-link-linux-amd64"} {
		put(stage, name, "new "+name)
	}
	const windows = "personad-windows-amd64.exe"
	put(stage, legacyDaemon+"-windows-amd64.exe", "old manifest name")
	put(stage, "personad-linux-arm64", "new arm64 build")
	put(dir, legacyDaemon+"-linux-arm64", "user file")
	outside := filepath.Join(t.TempDir(), "unrelated")
	if err := os.Symlink(outside, filepath.Join(dir, legacyDaemon+"-darwin-arm64")); err != nil {
		t.Fatal(err)
	}
	put(stage, "personad-darwin-arm64", "new darwin build")

	var previous os.FileInfo
	for range 2 {
		if err := PublishPackages(stage, dir, Build{Version: "v2"}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{canonical, windows} {
			alias := filepath.Join(dir, legacyDaemon+want[len("personad"):])
			if got, err := os.Readlink(alias); err != nil || got != want {
				t.Fatalf("alias = %q, %v, want relative %s", got, err, want)
			}
			if link, err := os.Stat(alias); err != nil {
				t.Fatal(err)
			} else if bin, err := os.Stat(filepath.Join(dir, want)); err != nil || !os.SameFile(link, bin) {
				t.Fatalf("%s and %s do not identify the same executable", alias, want)
			}
		}
		if b, _ := os.ReadFile(filepath.Join(dir, legacy)); string(b) != "new build" {
			t.Fatalf("legacy reads %q", b)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, legacy+".version")); string(b) != "v2\n" {
			t.Fatalf("legacy package version reads %q", b)
		}
		current, err := os.Lstat(filepath.Join(dir, legacy))
		if err != nil || previous != nil && !os.SameFile(previous, current) {
			t.Fatalf("republishing changed the existing alias: %v", err)
		}
		previous = current
		if b, _ := os.ReadFile(filepath.Join(dir, legacyDaemon+"-linux-arm64")); string(b) != "user file" {
			t.Fatalf("user file reads %q", b)
		}
		if link, err := os.Readlink(filepath.Join(dir, legacyDaemon+"-darwin-arm64")); link != outside || err != nil {
			t.Fatalf("user symlink = %q, %v", link, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "persona-node-linux-amd64.version")); string(b) != "v2\n" {
			t.Fatalf("split node version is %q", b)
		}
	}
}

func TestPublishPackagesDoesNotWriteThroughDirectoryPointer(t *testing.T) {
	stage, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(t.TempDir(), "nodes")
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "personad-linux-amd64"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PublishPackages(stage, dir, Build{Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(outside); err != nil || len(files) != 0 {
		t.Fatalf("publisher-owned tree changed: %v, %v", files, err)
	}
}
