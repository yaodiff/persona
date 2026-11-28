// Package updatetest builds tiny Go programs whose build info names their package the way builds of server/cmd/personad
// and server/cmd/persona-node do, for tests of update.ProgramOf and of what refuses or picks a program by it; and serves
// releases the way an update source does (Releases), for tests of what is downloaded while a newer one comes out.
package updatetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// goCache is the build cache as the test binary started: tests that move HOME would otherwise build the standard
// library again from a cold cache for every stub.
var goCache = func() string {
	if c := os.Getenv("GOCACHE"); c != "" {
		return c
	}
	out, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}()

// Program builds an empty main package at <stub module>/cmd/<name> for goos/goarch ("" for this machine's) and returns
// the executable's path. Skips the test when there is no go tool.
func Program(t testing.TB, name, goos, goarch string) string {
	t.Helper()
	return ProgramSource(t, name, goos, goarch, "package main\n\nfunc main() {}\n")
}

// VersionedProgram is Program linked with version, as a release links main.version: the build carries its version's
// bytes, which the manager looks for before it takes a local build for the hub's own (internal/devices).
func VersionedProgram(t testing.TB, name, goos, goarch, version string) string {
	t.Helper()
	return ProgramSource(t, name, goos, goarch, "package main\n\nimport \"os\"\n\nvar version = "+strconv.Quote(version)+
		"\n\nfunc main() { os.Stdout.WriteString(version + \"\\n\") }\n")
}

// CoreProgram is VersionedProgram built with the persona_core_only tag, as a core-only release builds its programs:
// its build info says the core edition (update.EditionOf).
func CoreProgram(t testing.TB, name, goos, goarch, version string) string {
	t.Helper()
	return CoreProgramSource(t, name, goos, goarch, "package main\n\nimport \"os\"\n\nvar version = "+strconv.Quote(version)+
		"\n\nfunc main() { os.Stdout.WriteString(version + \"\\n\") }\n")
}

// CoreProgramSource is ProgramSource built with the persona_core_only tag, for a test that needs its own main program.
func CoreProgramSource(t testing.TB, name, goos, goarch, mainGo string) string {
	t.Helper()
	return programSource(t, name, goos, goarch, mainGo, "persona_core_only")
}

// ProgramSource is Program with main.go's source given.
func ProgramSource(t testing.TB, name, goos, goarch, mainGo string) string {
	t.Helper()
	return programSource(t, name, goos, goarch, mainGo, "")
}

func programSource(t testing.TB, name, goos, goarch, mainGo, tags string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/stub\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(src, "cmd", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), name)
	// The synthetic module has no repository provenance. Ambient fake Git
	// commands from service tests must not stamp or break this fixture build.
	cmd := exec.Command(goBin, "build", "-buildvcs=false", "-trimpath", "-tags", tags, "-o", out, "./cmd/"+name)
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
	if goCache != "" {
		cmd.Env = append(cmd.Env, "GOCACHE="+goCache)
	}
	if goos != "" {
		cmd.Env = append(cmd.Env, "GOOS="+goos, "GOARCH="+goarch)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", name, err, b)
	}
	return out
}
