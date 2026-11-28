package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

func TestProgramOfTellsPersonaNodeFromPersonad(t *testing.T) {
	// Builds for another platform are told apart too: the hub reads the node builds it keeps for every platform.
	node := updatetest.Program(t, "persona-node", "darwin", "arm64")
	full := updatetest.Program(t, "personad", "linux", "amd64")
	if got := ProgramOf(node); got != nodeproto.ProgramPersonaNode {
		t.Fatalf("persona-node build: %q", got)
	}
	if got := ProgramOf(full); got != nodeproto.ProgramPersonad {
		t.Fatalf("personad build: %q", got)
	}
	self, _ := os.Executable() // any other Go program (this test) counts as personad, as older builds do
	if got := ProgramOf(self); got != nodeproto.ProgramPersonad {
		t.Fatalf("test binary: %q", got)
	}
	text := filepath.Join(t.TempDir(), "personad")
	_ = os.WriteFile(text, []byte("#!/bin/sh\n"), 0o755)
	for _, p := range []string{text, filepath.Join(t.TempDir(), "missing")} {
		if got := ProgramOf(p); got != "" {
			t.Fatalf("%s: %q, want unknown", p, got)
		}
	}

	if err := CheckReplace(full, node); err == nil || !strings.Contains(err.Error(), "not replacing it with persona-node") {
		t.Fatalf("personad -> persona-node: %v", err)
	}
	// The device's own file takes persona-node whatever it holds now (personad sent there by a hub without a
	// persona-node build): no hub or manager service runs it.
	device := filepath.Join(t.TempDir(), "persona-node")
	b, _ := os.ReadFile(full)
	_ = os.WriteFile(device, b, 0o755)
	for _, c := range [][2]string{{node, full}, {node, node}, {full, full}, {text, node}, {device, node}} {
		if err := CheckReplace(c[0], c[1]); err != nil {
			t.Fatalf("%s -> %s: %v", c[0], c[1], err)
		}
	}
}

func TestProgramRefusalPrecedesEditionWithoutWeakeningDeviceFiles(t *testing.T) {
	corePersonad := updatetest.CoreProgram(t, "personad", "", "", "old")
	fullNode := updatetest.Program(t, "persona-node", "", "")
	// Both rules refuse this, and the established program-identity reason stays stable in a core-only test run.
	if err := CheckReplace(corePersonad, fullNode); err == nil || !strings.Contains(err.Error(), "not replacing it with persona-node") {
		t.Fatalf("core personad -> full persona-node: %v", err)
	}
	// A device file may change program, but not edition.
	device := filepath.Join(t.TempDir(), "persona-node")
	b, _ := os.ReadFile(corePersonad)
	_ = os.WriteFile(device, b, 0o755)
	if err := CheckReplace(device, fullNode); err == nil || !strings.Contains(err.Error(), "edition") {
		t.Fatalf("a core device file accepted a full build: %v", err)
	}
	coreNode := updatetest.CoreProgram(t, "persona-node", "", "", "next")
	if err := CheckReplace(device, coreNode); err != nil {
		t.Fatalf("a core device file refused a core build: %v", err)
	}
}

// The suffix ProgramOf looks for is the package persona-node is built from.
func TestProgramOfMatchesTheCommandDirectory(t *testing.T) {
	if fi, err := os.Stat(filepath.Join("..", "..", "cmd", nodeproto.ProgramPersonaNode, "main.go")); err != nil || fi.IsDir() {
		t.Fatalf("server/cmd/%s/main.go: %v", nodeproto.ProgramPersonaNode, err)
	}
}
