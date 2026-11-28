package nodeproto

import (
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// A node built from this code must be one its own manager drives, and speak a range that overlaps it.
func TestANodeOfThisBuildIsCompatibleWithItsManager(t *testing.T) {
	for _, c := range Required {
		if !slices.Contains(Announced, c) {
			t.Errorf("required capability %q is not announced", c)
		}
	}
	seen := map[string]bool{}
	for _, c := range Announced {
		if c == "" || seen[c] {
			t.Errorf("capability %q is empty or announced twice", c)
		}
		seen[c] = true
	}
	if MinVersion > Version || MinManagerVersion > Version || MinVersion < 1 {
		t.Errorf("protocol range: version %d, min node %d, min manager %d", Version, MinVersion, MinManagerVersion)
	}
}

// The contract stays pure: no imports at all, so neither side drags the other in through it.
func TestContractImportsNothing(t *testing.T) {
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		for name, f := range p.Files {
			if p.Name == "nodeproto" && len(f.Imports) > 0 && name != "nodeproto_test.go" {
				t.Errorf("%s imports %d packages: the node contract imports nothing", name, len(f.Imports))
			}
		}
	}
}

// The lane table is one list per lane: no name on two, none twice, and the node_lanes_v1 capability announced with it.
func TestLaneTable(t *testing.T) {
	seen := map[string]string{}
	for lane, names := range map[string][]string{"control": ControlLane, "bulk": BulkLane} {
		for _, n := range names {
			if n == "" {
				t.Fatalf("%s: an empty name", lane)
			}
			if other, ok := seen[n]; ok {
				t.Fatalf("%s is on %s and %s", n, other, lane)
			}
			seen[n] = lane
		}
	}
	if !slices.Contains(Announced, NodeLanes) {
		t.Fatalf("%s is not announced", NodeLanes)
	}
}

func TestValidScratchID(t *testing.T) {
	valid := []string{
		"todo_1234567890abcdef",
		"t1",
		"Task-Alpha_01",
		"0",
		strings.Repeat("a", 96),
	}
	for _, id := range valid {
		if !ValidScratchID(id) {
			t.Errorf("ValidScratchID(%q) = false, want true", id)
		}
	}
	invalid := []string{
		"",
		".",
		"..",
		"../assistant",
		"..\\assistant",
		"todo/sub",
		"todo\\sub",
		".persona-managed",
		"-leading-dash",
		"_leading_underscore",
		"todo.with.dot",
		"todo with space",
		"todo\x00null",
		"todo:colon",
		strings.Repeat("a", 97),
	}
	for _, id := range invalid {
		if ValidScratchID(id) {
			t.Errorf("ValidScratchID(%q) = true, want false", id)
		}
	}
}

func TestScratchWorkspaceCapabilityAnnounced(t *testing.T) {
	if !slices.Contains(Announced, ScratchWorkspace) {
		t.Fatalf("Announced lacks %q", ScratchWorkspace)
	}
}
