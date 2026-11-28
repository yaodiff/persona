package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestMainEntityMemoryTriggersAndGuards(t *testing.T) {
	ctx := context.Background()
	s, _, st, _ := memoryAssembleService(t)
	s.memoryEntries = &memEntrySource{}
	s.setStore(func() Store { return &entriesMemStore{infoCategoryMemStore: st} })
	setSettings(t, s, func(v *Settings) { v.Main = "qa-main" })

	page := wire.MemoryEntriesResult{Tagged: true, Entries: []wire.MemoryEntry{
		{ID: "qa-standing", Text: "qa: standing rule for acme-gateway", Subjects: []string{"acme-gateway"}, Standing: true, Rank: 950, Status: "active"},
		{ID: "qa-gateway", Text: "qa: acme-gateway runs on port 8443", Subjects: []string{"acme-gateway", "org/acme-gateway"}, Rank: 800, Status: "active"},
		{ID: "qa-short-ascii", Text: "qa: short ascii should not match substrings", Subjects: []string{"ST", "launch"}, Rank: 900, Status: "active"},
		{ID: "qa-generic", Text: "qa: generic stopword should not trigger", Subjects: []string{"Persona", "任务", "测试", "文档", "workflow"}, Rank: 900, Status: "active"},
		{ID: "qa-person", Text: "qa: 张伟负责发布评审", Subjects: []string{"张伟"}, Rank: 750, Status: "active"},
		{ID: "qa-sensitive", Text: "qa: acme-gateway private billing account", Subjects: []string{"acme-gateway"}, Categories: []string{categoryFinance}, OriginSpace: "qa-finance", Rank: 999, Status: "active"},
	}}
	s.memoryExtension = &memExtensionSource{batch: memoryExtensionBatchOf([]wire.MemoryEntriesResult{page})}

	// 1. High-discriminability ASCII repo/service + Chinese person name trigger local memory; standing duplicate and sensitive entry excluded.
	got := s.Context(ctx, ContextParams{MachineID: "qa-machine", SessionID: "qa-main", Query: "帮我看一下 acme-gateway 和张伟最近的安排"}).Text
	if !strings.Contains(got, "qa: acme-gateway runs on port 8443") || !strings.Contains(got, "qa: 张伟负责发布评审") {
		t.Fatalf("expected entity-triggered entries in main context: %s", got)
	}
	if strings.Count(got, "qa: standing rule for acme-gateway") != 1 {
		t.Fatalf("standing entry duplicated in entity section: %s", got)
	}
	if strings.Contains(got, "private billing account") {
		t.Fatalf("sensitive entry leaked into main entity memory: %s", got)
	}

	// 2. Generic stopwords and short/substring ASCII terms must not trigger entity memory.
	noise := s.mainEntityMemorySection(ctx, "qa-machine", "qa-main", "请帮我检查 Persona 的任务、测试、文档、starfield 和 launcher workflow")
	if noise != "" {
		t.Fatalf("generic/substring noise triggered entity memory: %q", noise)
	}

	// 3. Non-main conversation and other machine never get main entity memory.
	if other := s.mainEntityMemorySection(ctx, "qa-machine", "qa-side", "acme-gateway"); other != "" {
		t.Fatalf("non-main session received main entity memory: %q", other)
	}
	if wrongMachine := s.mainEntityMemorySection(ctx, "qa-other", "qa-main", "acme-gateway"); wrongMachine != "" {
		t.Fatalf("non-owner machine received main entity memory: %q", wrongMachine)
	}

	// 4. nodeAnswer forwards query into ContextParams.
	raw, _ := json.Marshal(map[string]any{"session_id": "qa-main", "query": "check org/acme-gateway"})
	out, handled, err := s.nodeAnswer(ctx, "qa-machine", "assistant.context", raw)
	if err != nil || !handled {
		t.Fatalf("nodeAnswer: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(out.(ContextResult).Text, "qa: acme-gateway runs on port 8443") {
		t.Fatalf("nodeAnswer did not forward query: %+v", out)
	}
}

func TestMainEntityMemoryRespectsBudget(t *testing.T) {
	ctx := context.Background()
	s, _, st, _ := memoryAssembleService(t)
	s.memoryEntries = &memEntrySource{}
	s.setStore(func() Store { return &entriesMemStore{infoCategoryMemStore: st} })
	setSettings(t, s, func(v *Settings) { v.Main = "qa-main" })

	var entries []wire.MemoryEntry
	for i := 0; i < 12; i++ {
		entries = append(entries, wire.MemoryEntry{
			ID:       fmt.Sprintf("qa-b-%d", i),
			Text:     fmt.Sprintf("qa: buildbox rule %02d %s", i, strings.Repeat("a", 220)),
			Subjects: []string{"buildbox"},
			Rank:     900 - i,
			Status:   "active",
		})
	}
	s.memoryExtension = &memExtensionSource{batch: memoryExtensionBatchOf([]wire.MemoryEntriesResult{{Tagged: true, Entries: entries}})}
	section := s.mainEntityMemorySection(ctx, "qa-machine", "qa-main", "check buildbox status")
	if section == "" {
		t.Fatal("expected entity memory section")
	}
	// Count payload bytes across bullet lines (each line starts with "  - ").
	var payloadBytes int
	for _, ln := range strings.Split(section, "\n") {
		if body, ok := strings.CutPrefix(ln, "  - "); ok {
			payloadBytes += len(body)
		}
	}
	if payloadBytes == 0 || payloadBytes > memoryMainEntityMax {
		t.Fatalf("payloadBytes = %d, want (0, %d]", payloadBytes, memoryMainEntityMax)
	}
}
