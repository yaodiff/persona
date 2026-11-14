package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// age makes the path transcriptPath found for id older than pathRecheck, so the next lookup looks again.
func age(n *Node, id string) {
	n.mu.Lock()
	n.pathsAt[id] = time.Now().Add(-2 * pathRecheck)
	n.mu.Unlock()
}

// A rollout found once stays the thread's file while it exists: looking again for a newer copy reads no projects
// directory, since a rollout has no copy there. One that is gone is looked up afresh.
func TestTranscriptPathKeepsAFoundRolloutWithoutReadingProjects(t *testing.T) {
	n, home := testNode(t)
	n.pathsAt = map[string]time.Time{}
	const thread = "0e000000-0000-4000-8000-0000000000a1"
	writeTranscript(t, home, "/work/other", "0e000000-0000-4000-8000-0000000000a2", time.Now())
	writeMetaRollout(t, n.traexHome(), thread, `{"id":"`+thread+`","cwd":"/work/app","source":"cli"}`, time.Now())
	want := rolloutPath(n.traexHome(), thread)
	if got := n.transcriptPath(thread); got != want || want == "" {
		t.Fatalf("first lookup %q, want the rollout %q", got, want)
	}
	for range 3 {
		age(n, thread)
		before := n.projectGlobs.Load()
		if got := n.transcriptPath(thread); got != want {
			t.Fatalf("looked again: %q, want %q", got, want)
		}
		if read := n.projectGlobs.Load() - before; read != 0 {
			t.Fatalf("looking again for a rollout read the projects directories %d times, want 0", read)
		}
	}
	if err := os.Remove(want); err != nil {
		t.Fatal(err)
	}
	age(n, thread)
	if got := n.transcriptPath(thread); got != "" {
		t.Fatalf("a removed rollout is still the thread's file: %q", got)
	}
}

// A live Codex or compatible-CLI agent (a bridge's parked thread, a pane) is found in its own rollouts, without
// reading the projects directories even the first time.
func TestTranscriptPathOfARolloutAgentReadsNoProjects(t *testing.T) {
	n, home := testNode(t)
	n.pathsAt = map[string]time.Time{}
	writeTranscript(t, home, "/work/other", "0e000000-0000-4000-8000-0000000000b0", time.Now())
	const traex, codex, gone = "0e000000-0000-4000-8000-0000000000b1", "0e000000-0000-4000-8000-0000000000b2", "0e000000-0000-4000-8000-0000000000b3"
	writeMetaRollout(t, n.traexHome(), traex, `{"id":"`+traex+`","cwd":"/work/app","source":"cli"}`, time.Now())
	writeRollout(t, home, codex, "/work/app", time.Now(), time.Now(), event("task_started"))
	n.agents[traex] = host.Agent{Agent: traexAgent}
	n.agents[codex] = host.Agent{Agent: "codex"}
	n.agents[gone] = host.Agent{Agent: traexAgent}
	before := n.projectGlobs.Load()
	if got, want := n.transcriptPath(traex), rolloutPath(n.traexHome(), traex); got != want || want == "" {
		t.Fatalf("compatible CLI thread: %q, want %q", got, want)
	}
	if got, want := n.transcriptPath(codex), rolloutPath(n.codexHome(), codex); got != want || want == "" {
		t.Fatalf("Codex thread: %q, want %q", got, want)
	}
	if got := n.transcriptPath(gone); got != "" {
		t.Fatalf("a thread with no rollout: %q, want none", got)
	}
	if read := n.projectGlobs.Load() - before; read != 0 {
		t.Fatalf("rollout agents read the projects directories %d times, want 0", read)
	}
}

// A cached base rollout must not hide a newer continuation (#7354).
func TestTranscriptPathFollowsContinuationForRolloutAgent(t *testing.T) {
	n, home := testNode(t)
	n.pathsAt = map[string]time.Time{}
	const thread = "0e000000-0000-4000-8000-0000000000f1"
	// The base was last written before the continuation began. Not dated time.Now(): Linux stamps a write with its
	// coarse clock, up to a tick behind time.Now(), so the continuation written next read as the older file (#7381).
	writeRollout(t, home, thread, "/work/app", time.Now(), time.Now().Add(-time.Hour))
	n.agents[thread] = host.Agent{Agent: "codex"}
	base := rolloutPath(n.codexHome(), thread)
	if got := n.transcriptPath(thread); got != base || base == "" {
		t.Fatalf("cached base = %q, want %q", got, base)
	}
	path := strings.TrimSuffix(base, ".jsonl") + "_" + continuationSegment + ".jsonl"
	continuationFixture(t, path, thread, "")
	expireRollouts(n.codexHome(), true)
	age(n, thread)
	before := n.projectGlobs.Load()
	if got := n.transcriptPath(thread); got != path {
		t.Fatalf("transcript = %q, want the continuation %q", got, path)
	}
	if read := n.projectGlobs.Load() - before; read != 0 {
		t.Fatalf("following a continuation read the projects directories %d times, want 0", read)
	}
}

// A list with many parked rollout rows (a chat bridge keeps a week of them) reads the projects directories a bounded
// number of times however often it is built, once their paths are known.
func TestSessionListOfParkedRolloutRowsReadsProjectsOnce(t *testing.T) {
	n, home := testNode(t)
	n.pathsAt, n.metas, n.bgScans = map[string]time.Time{}, map[string]quickMeta{}, map[string]*bgScan{}
	writeTranscript(t, home, "/work/other", "0e000000-0000-4000-8000-0000000000e0", time.Now())
	ids := make([]string, 0, 200)
	for i := range 200 {
		id := fmt.Sprintf("0e000000-0000-4000-8000-%012x", 0xe00+i)
		writeMetaRollout(t, n.traexHome(), id, `{"id":"`+id+`","cwd":"/work/app","source":"cli"}`, time.Now())
		n.agents[id] = host.Agent{Agent: traexAgent, AgentStatus: "offline"}
		ids = append(ids, id)
	}
	n.sessionList(context.Background(), false) // finds every path
	for _, id := range ids {
		age(n, id)
	}
	before := n.projectGlobs.Load()
	n.sessionList(context.Background(), false)
	if read := n.projectGlobs.Load() - before; read > 2 {
		t.Fatalf("a list of %d parked rollout rows read the projects directories %d times, want at most 2 (the offline scan)", len(ids), read)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects")); err != nil {
		t.Fatal(err)
	}
}
