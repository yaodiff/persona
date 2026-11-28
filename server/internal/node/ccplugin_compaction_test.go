package node

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// compactionPushes filters a node's hub pushes down to session.live.
func compactionPushes(n *Node) (<-chan json.RawMessage, *io.PipeWriter) {
	all, pw := hubPushes(n)
	out := make(chan json.RawMessage, 32)
	go func() {
		defer close(out)
		for m := range all {
			if m.Event == "session.live" {
				out <- m.Params
			}
		}
	}()
	return out, pw
}

func TestCCPluginCompactionAuthenticatesLifecycleAndClearsLive(t *testing.T) {
	n, _, home := herdrNode(t)
	noPaneEnv(t)
	bgTranscript(t, home, home, sidA, "compact")
	writeOutsideClaude(t, home, sidA, "claude-desktop", "busy", nil, nil)
	pushes, pw := compactionPushes(n)
	t.Cleanup(func() { _ = pw.Close() })
	oldEvery := liveEvery
	liveEvery = 20 * time.Millisecond
	t.Cleanup(func() { liveEvery = oldEvery })
	stop := startLive(t, n)
	defer stop()

	c := pluginSocket(t, n)
	status, out := pluginPost(t, c, "compact", map[string]any{
		"session_id": sidA, "phase": "start", "trigger": "auto",
	})
	if status != 200 || out["ok"] != true {
		t.Fatalf("start: %d %v", status, out)
	}
	if live := nextLive(t, pushes); live == nil || live.Verb != "Compacting conversation" || live.ElapsedS != 1 || live.At == "" {
		t.Fatalf("start live: %+v", live)
	}
	report, ok := n.ccPlugins.compaction(sidA)
	if !ok || report.pid != os.Getpid() || report.trigger != "auto" {
		t.Fatalf("report: %+v %v", report, ok)
	}

	status, out = pluginPost(t, c, "compact", map[string]any{
		"session_id": sidA, "phase": "end", "trigger": "auto",
	})
	if status != 200 || out["ok"] != true {
		t.Fatalf("end: %d %v", status, out)
	}
	if live := nextLive(t, pushes); live != nil {
		t.Fatalf("end live: %+v, want null", live)
	}
	if _, ok := n.ccPlugins.compaction(sidA); ok {
		t.Fatal("end left the report behind")
	}

	status, _ = pluginPost(t, c, "compact", map[string]any{
		"session_id": sidA, "phase": "later", "trigger": "manual",
	})
	if status != 400 {
		t.Fatalf("invalid phase: %d", status)
	}
	status, _ = pluginPost(t, c, "compact", map[string]any{
		"session_id": sidA, "phase": "start", "trigger": "guess",
	})
	if status != 400 {
		t.Fatalf("invalid trigger: %d", status)
	}
	status, _ = pluginPost(t, c, "compact", map[string]any{
		"session_id": sidB, "phase": "start", "trigger": "manual",
	})
	if status != 403 {
		t.Fatalf("another session: %d", status)
	}
}

func TestCCPluginCompactionLiveRefreshesAndEndsOnProcessOrTimeout(t *testing.T) {
	n, _, home := herdrNode(t)
	noPaneEnv(t)
	writeOutsideClaude(t, home, sidA, "claude-desktop", "busy", nil, nil)
	now := time.Now()
	n.ccPlugins.beginCompaction(sidA, ccPluginCompaction{pid: os.Getpid(), started: now}, now)
	if first := n.pluginCompactionLive(sidA, now.Add(100*time.Millisecond)); first == nil || first.ElapsedS != 1 {
		t.Fatalf("first live: %+v", first)
	}
	n.ccPlugins.beginCompaction(sidA, ccPluginCompaction{
		pid: os.Getpid(), trigger: "auto", started: now.Add(-7 * time.Second),
	}, now)
	live := n.pluginCompactionLive(sidA, now)
	if live == nil || live.Verb != "Compacting conversation" || live.ElapsedS != 6 || live.At == "" {
		t.Fatalf("live: %+v", live)
	}
	if again := n.pluginCompactionLive(sidA, now.Add(time.Second)); again == nil || again.ElapsedS != 6 {
		t.Fatalf("refresh before the next three-second bucket: %+v", again)
	}
	if next := n.pluginCompactionLive(sidA, now.Add(3*time.Second)); next == nil || next.ElapsedS != 9 {
		t.Fatalf("refresh after the next three-second bucket: %+v", next)
	}

	oldAlive := processAlive
	processAlive = func(int, string) bool { return false }
	t.Cleanup(func() { processAlive = oldAlive })
	if live := n.pluginCompactionLive(sidA, now.Add(time.Second)); live != nil {
		t.Fatalf("dead process: %+v", live)
	}
	if _, ok := n.ccPlugins.compaction(sidA); ok {
		t.Fatal("dead process left the report behind")
	}
	processAlive = oldAlive

	oldTTL := ccPluginCompactionTTL
	ccPluginCompactionTTL = time.Second
	t.Cleanup(func() { ccPluginCompactionTTL = oldTTL })
	n.ccPlugins.beginCompaction(sidA, ccPluginCompaction{pid: os.Getpid(), started: now}, now)
	if live := n.pluginCompactionLive(sidA, now.Add(2*time.Second)); live != nil {
		t.Fatalf("expired: %+v", live)
	}
	if _, ok := n.ccPlugins.compaction(sidA); ok {
		t.Fatal("expired report stayed")
	}
}

func TestCCPluginCompactionRegistryRacesCleanly(t *testing.T) {
	var r ccPluginRegistry
	r.beginCompaction(sidA, ccPluginCompaction{pid: 10, started: time.Now()}, time.Now())
	r.endCompaction(sidA, 11)
	if _, ok := r.compaction(sidA); !ok {
		t.Fatal("a different process ended the report")
	}
	r.endCompaction(sidA, 10)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := sidA
			if i%2 == 0 {
				id = sidB
			}
			r.beginCompaction(id, ccPluginCompaction{pid: os.Getpid(), started: time.Now()}, time.Now())
			_, _ = r.compaction(id)
			r.endCompaction(id, os.Getpid())
		}(i)
	}
	wg.Wait()
}
