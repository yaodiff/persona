package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func resetAgyModelWindowCache(t *testing.T) {
	t.Helper()
	agyModelWindowCache.Lock()
	old := agyModelWindowCache.byID
	agyModelWindowCache.byID = map[string]int{}
	agyModelWindowCache.Unlock()
	t.Cleanup(func() {
		agyModelWindowCache.Lock()
		agyModelWindowCache.byID = old
		agyModelWindowCache.Unlock()
	})
}

func TestAgyModelWindowParsingAndDiscovery(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"1000000", 1_000_000},
		{"1,048,576", 1_048_576},
		{"200_000", 200_000},
		{"200k", 200_000},
		{"256K", 256_000},
		{"1m", 1_000_000},
		{"2M", 2_000_000},
		{"context_window=1000000", 1_000_000},
		{"ctx=200k", 200_000},
		{"window:512k tokens", 512_000},
		{"extra column", 0},
		{"", 0},
		{"0", 0},
		{"-100000", 0},
		{"512", 0},
		{"1.5m", 0},
	} {
		if got := parseAgyModelWindow(tc.raw); got != tc.want {
			t.Errorf("parseAgyModelWindow(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}

	output := "Loading models...\n" +
		"gemini-4-neon-high\tGemini 4 Neon (High)\t1000000\n" +
		"gemini-4-neon-low\tGemini 4 Neon (Low)\t1m\n" +
		"example\tExample Model\textra column\n" +
		"custom-ctx\tCustom\tdesc\tcontext_window=256k\n"
	models, windows, err := agyModelListWithWindows(strings.NewReader(output))
	if err != nil {
		t.Fatalf("agyModelListWithWindows: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("models = %+v", models)
	}
	wantWindows := map[string]int{
		"gemini-4-neon-high": 1_000_000,
		"gemini-4-neon-low":  1_000_000,
		"custom-ctx":         256_000,
	}
	if !reflect.DeepEqual(windows, wantWindows) {
		t.Fatalf("windows = %v, want %v", windows, wantWindows)
	}
}

func TestAgyContextWindowResolution(t *testing.T) {
	catalog := proto.ManagedLauncher{
		Name:  "agy-gw",
		Agent: agyAgent,
		Models: []proto.ProviderModel{
			{ID: "gemini-4-carbon", ContextWindow: 1_000_000},
			{ID: "gemini-4-carbon-xhigh", ContextWindow: 2_000_000},
			{ID: "route-narrow/gemini-4-carbon", ContextWindow: 512_000},
			{ID: "gemini-3.8-flash", ContextWindow: 200_000},
			{ID: "claude-sonnet-4-6", ContextWindow: 200_000},
		},
	}
	capped := catalog
	capped.MaxContext = 750_000
	undeclared := proto.ManagedLauncher{
		Name:   "agy-bare",
		Agent:  agyAgent,
		Models: []proto.ProviderModel{{ID: "gemini-4-neon"}},
	}
	agyCatalog := map[string]int{
		"gemini-4-argon":      1_048_576,
		"gemini-4-neon-high":  1_000_000,
		"gpt-oss-120b-medium": 128_000,
		"claude-opus-4-6":     512_000,
	}
	lookup := func(id string) int { return lookupAgyWindowMap(agyCatalog, id) }

	for _, tc := range []struct {
		name           string
		launcher       *proto.ManagedLauncher
		model          string
		recorded, used int
		catalog        func(string) int
		fallbacks      []string
		want           int
	}{
		{"transcript recorded window wins", &capped, "gemini-4-carbon-high", 400_000, 50_000, lookup, nil, 400_000},
		{"launcher MaxContext overrides model entry", &capped, "gemini-4-carbon-high", 0, 50_000, lookup, nil, 750_000},
		{"exact effort-suffixed launcher model wins over base", &catalog, "gemini-4-carbon-xhigh", 0, 50_000, nil, nil, 2_000_000},
		{"effort-suffixed model matches base launcher model", &catalog, "gemini-4-carbon-high", 0, 50_000, nil, nil, 1_000_000},
		{"routed effort-suffixed model matches launcher base model", &catalog, "passthrough/gemini-4-carbon-medium", 0, 50_000, nil, nil, 1_000_000},
		{"exact routed launcher entry beats bare suffix", &catalog, "route-narrow/gemini-4-carbon", 0, 50_000, nil, nil, 512_000},
		{"fallback model resolves via launcher when transcript echoed opaque id", &catalog, "ep-opaque", 0, 50_000, nil, []string{"gemini-4-carbon-low"}, 1_000_000},
		{"200k Claude entry promotes to 1M over 200k used", &catalog, "claude-sonnet-4-6-high", 0, 260_000, nil, nil, 1_000_000},
		{"agyModelList catalog resolves effort-suffixed base model", &undeclared, "gemini-4-argon-high", 0, 50_000, lookup, nil, 1_048_576},
		{"agyModelList catalog resolves exact effort model", nil, "gemini-4-neon-high", 0, 50_000, lookup, nil, 1_000_000},
		{"agyModelList catalog beats built-in fallback when launcher lacks entry", &undeclared, "claude-opus-4-6-medium", 0, 50_000, lookup, nil, 512_000},
		{"built-in Claude 1M model with agy effort suffix", nil, "claude-opus-5-5-high", 0, 50_000, nil, nil, 1_000_000},
		{"built-in Claude 200k model with agy effort suffix", nil, "claude-opus-4-6-medium", 0, 50_000, nil, nil, 200_000},
		{"built-in Claude 200k model promotes over 200k usage", nil, "claude-opus-4-6-medium", 0, 250_000, nil, nil, 1_000_000},
		{"Gemini without a declared window", &undeclared, "gemini-4-neon-high", 0, 50_000, nil, nil, 1_048_576},
		{"Gemini cap survives usage past 200k", &catalog, "gemini-3.8-flash-high", 0, 210_000, lookup, nil, 200_000},
		{"recorded window beats discovered size", nil, "gemini-4-neon-high", 200_000, 40_000, lookup, nil, 200_000},
		{"known requested model for an opaque reply", nil, "ep-opaque", 0, 50_000, nil, []string{"gpt-oss-120b-medium"}, 131_072},
		{"unlisted Gemini variant stays unknown", &undeclared, "gemini-4-custom-high", 0, 50_000, nil, nil, 0},
		{"unknown opaque model stays 0", nil, "unknown-model-high", 0, 50_000, lookup, nil, 0},
		{"synthetic placeholder stays 0", &catalog, "<synthetic>", 0, 5_000, lookup, nil, 0},
		{"empty model without MaxContext stays 0", &catalog, "", 0, 5_000, lookup, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agyContextWindow(tc.launcher, tc.model, tc.recorded, tc.used, tc.catalog, tc.fallbacks...)
			if got != tc.want {
				t.Fatalf("agyContextWindow = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAgyTranscriptTokenExtractionAndUsage(t *testing.T) {
	// Realistic agy JSONL: exclusive input_tokens + cache_read_tokens, optional cache_write_tokens,
	// in-place step completion (step 1 RUNNING -> DONE), nested usage fallback, checkpoint compaction,
	// and a subsequent reply after compaction.
	const fixture = "" +
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-21T10:00:00Z","content":"Inspect files"}` + "\n" +
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"RUNNING","created_at":"2026-11-21T10:00:01Z","model":"gemini-4-neon-high","thinking":"Drafting","input_tokens":100,"output_tokens":50}` + "\n" +
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:01Z","model":"gemini-4-neon-high","thinking":"Reading file first","content":"I will read it.","input_tokens":32396,"cache_read_tokens":0,"output_tokens":862,"tool_calls":[{"name":"read_file","args":{"Path":"/tmp/a.go"}}]}` + "\n" +
		`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-21T10:00:02Z","content":"package main"}` + "\n" +
		`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:03Z","model":"claude-opus-5-5-high","content":"All checked.","input_tokens":5727,"cache_read_tokens":32397,"cache_write_tokens":400,"cache_write_1h_tokens":100,"output_tokens":226}` + "\n" +
		`{"step_index":4,"source":"SYSTEM","type":"CHECKPOINT","status":"DONE","created_at":"2026-11-21T10:00:04Z","input_tokens":3201,"cache_read_tokens":509879,"output_tokens":4554}` + "\n" +
		`{"step_index":5,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-21T10:00:05Z","content":"Next question"}` + "\n" +
		`{"step_index":6,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:06Z","model":"gemini-4-neon-high","content":"Post-checkpoint answer.","usage":{"input_tokens":12000,"cache_read_input_tokens":3000,"cache_creation_input_tokens":500,"output_tokens":310,"model_context_window":1048576}}` + "\n"

	steps, _ := parseAgySteps([]byte(fixture), agyTestID)
	history := mapAgySteps(steps, time.Time{}, nil)
	extracted := extractAgyUsage(steps)

	// Step 1's RUNNING draft was replaced by DONE (output 862, not 50+862).
	// Checkpoint's output_tokens is system summarization, not assistant reply output.
	wantOutput := 862 + 226 + 310
	wantContext := 12000 + 3000 + 500
	if history.usage.outputTokens != wantOutput || extracted.outputTokens != wantOutput {
		t.Fatalf("outputTokens = %d / %d, want %d", history.usage.outputTokens, extracted.outputTokens, wantOutput)
	}
	if history.usage.contextTokens != wantContext || extracted.contextTokens != wantContext {
		t.Fatalf("contextTokens = %d / %d, want %d", history.usage.contextTokens, extracted.contextTokens, wantContext)
	}
	if history.usage.contextWindow != 1_048_576 || extracted.contextWindow != 1_048_576 {
		t.Fatalf("contextWindow = %d / %d, want 1048576", history.usage.contextWindow, extracted.contextWindow)
	}
	if history.usage.compactions != 1 || history.usage.turns != 2 || history.usage.compacted {
		t.Fatalf("compactions=%d turns=%d compacted=%v", history.usage.compactions, history.usage.turns, history.usage.compacted)
	}

	// Step 1 produced 3 items (thinking, text, tool_use): Usage must appear ONLY on the first item, Model on all 3.
	var step1Items []proto.Item
	var step3Item, step6Item proto.Item
	for _, it := range history.items {
		switch it.Seq {
		case 2:
			step1Items = append(step1Items, it)
		case 4:
			step3Item = it
		case 7:
			step6Item = it
		}
	}
	if len(step1Items) != 3 {
		t.Fatalf("step 1 items = %+v", step1Items)
	}
	wantStep1Usage := []proto.TokenUsage{{Model: "gemini-4-neon-high", Input: 32396, Output: 862}}
	if !reflect.DeepEqual(step1Items[0].Usage, wantStep1Usage) {
		t.Fatalf("step 1 first item usage = %+v, want %+v", step1Items[0].Usage, wantStep1Usage)
	}
	if step1Items[1].Usage != nil || step1Items[2].Usage != nil {
		t.Fatalf("step 1 secondary items must not duplicate usage: %+v, %+v", step1Items[1].Usage, step1Items[2].Usage)
	}
	for i, it := range step1Items {
		if it.Model != "gemini-4-neon-high" {
			t.Errorf("step 1 item %d model = %q, want gemini-4-neon-high", i, it.Model)
		}
	}

	wantStep3Usage := []proto.TokenUsage{{
		Model:        "claude-opus-5-5-high",
		Input:        5727,
		Output:       226,
		CacheRead:    32397,
		CacheWrite:   400,
		CacheWrite1h: 100,
	}}
	if !reflect.DeepEqual(step3Item.Usage, wantStep3Usage) || step3Item.Model != "claude-opus-5-5-high" {
		t.Fatalf("step 3 item = %+v, want usage %+v", step3Item, wantStep3Usage)
	}

	wantStep6Usage := []proto.TokenUsage{{
		Model:      "gemini-4-neon-high",
		Input:      12000,
		Output:     310,
		CacheRead:  3000,
		CacheWrite: 500,
	}}
	if !reflect.DeepEqual(step6Item.Usage, wantStep6Usage) {
		t.Fatalf("step 6 item usage = %+v, want %+v", step6Item.Usage, wantStep6Usage)
	}

	// FoldWork must aggregate a folded work run's usage into Fold.Usage without losing or duplicating tokens.
	const workOnly = "" +
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-21T10:00:00Z","content":"Inspect"}` + "\n" +
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:01Z","model":"gemini-4-neon-high","thinking":"Reading","input_tokens":32396,"output_tokens":862,"tool_calls":[{"name":"read_file","args":{"Path":"/tmp/a.go"}}]}` + "\n" +
		`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-21T10:00:02Z","content":"package main"}` + "\n" +
		`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:03Z","model":"gemini-4-neon-high","content":"Done."}` + "\n"
	workSteps, _ := parseAgySteps([]byte(workOnly), agyTestID)
	folded := transcript.FoldWork(mapAgySteps(workSteps, time.Time{}, nil).items)
	var foldUsage []proto.TokenUsage
	for _, it := range folded {
		if it.Kind == proto.KindFold && it.Fold != nil {
			foldUsage = append(foldUsage, it.Fold.Usage...)
		}
	}
	if !reflect.DeepEqual(foldUsage, wantStep1Usage) {
		t.Fatalf("folded work usage = %+v, want %+v", foldUsage, wantStep1Usage)
	}
}

func TestAgyTranscriptWindowFollowsTheModel(t *testing.T) {
	var u agyTranscriptUsage
	u.observePlanner(agyStep{Model: "custom", ContextWindow: 200_000, InputTokens: 8000})
	u.observePlanner(agyStep{InputTokens: 8500})
	if u.contextWindow != 200_000 {
		t.Fatalf("same model lost its recorded window: %+v", u)
	}
	u.observePlanner(agyStep{Model: "gemini-4-argon-low", InputTokens: 9000})
	if got := agyContextWindow(nil, u.model, u.contextWindow, u.contextTokens, nil); got != 1_048_576 {
		t.Fatalf("window after switching to Gemini = %d", got)
	}
}

func TestAgyTranscriptCheckpointResetsContextAndRewindExcludesAbandoned(t *testing.T) {
	// Compaction at the tail leaves ContextTokens == 0 until the next reply.
	const compacted = "" +
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-21T10:00:00Z","content":"hi"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-21T10:00:01Z","model":"claude-opus-5-5-high","content":"done","input_tokens":50000,"cache_read_tokens":450000,"output_tokens":300}` + "\n" +
		`{"step_index":2,"type":"CHECKPOINT","created_at":"2026-11-21T10:00:02Z","input_tokens":3000,"cache_read_tokens":500000,"output_tokens":4000}` + "\n"
	steps, _ := parseAgySteps([]byte(compacted), agyTestID)
	u := extractAgyUsage(steps)
	if u.contextTokens != 0 || !u.compacted || u.outputTokens != 300 || u.compactions != 1 {
		t.Fatalf("after checkpoint: %+v", u)
	}

	// Rewind drops abandoned step 2 and replaces step 1.
	const rewound = "" +
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-21T10:00:00Z","content":"hi"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-21T10:00:01Z","model":"gemini-4-neon-high","content":"old","input_tokens":40000,"output_tokens":900}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-21T10:00:02Z","model":"gemini-4-neon-high","content":"abandoned","input_tokens":60000,"output_tokens":1200}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-21T10:01:01Z","model":"gemini-4-neon-high","content":"replacement","input_tokens":15000,"cache_read_tokens":2000,"output_tokens":120}` + "\n"
	steps, _ = parseAgySteps([]byte(rewound), agyTestID)
	u = extractAgyUsage(steps)
	if u.contextTokens != 17000 || u.outputTokens != 120 {
		t.Fatalf("after rewind: context=%d output=%d, want 17000 / 120", u.contextTokens, u.outputTokens)
	}

	// Missing, zero, negative, and synthetic-model fields degrade honestly.
	const missing = "" +
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-21T10:00:00Z","content":"hi"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-21T10:00:01Z","model":"<synthetic>","content":"plain"}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-21T10:00:02Z","content":"neg","input_tokens":-10,"output_tokens":-5,"cache_read_tokens":-1}` + "\n"
	steps, _ = parseAgySteps([]byte(missing), agyTestID)
	h := mapAgySteps(steps, time.Time{}, nil)
	if h.usage.hasUsage || h.usage.contextTokens != 0 || h.usage.outputTokens != 0 || h.usage.model != "" {
		t.Fatalf("missing/negative usage should stay zero: %+v", h.usage)
	}
	for _, it := range h.items {
		if it.Usage != nil || it.Model != "" {
			t.Fatalf("unexpected usage or model on degraded item: %+v", it)
		}
	}
}

func TestAgyContextBreakdown(t *testing.T) {
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	n := &Node{home: t.TempDir()}
	const firstTurn = "" +
		`{"step_index":0,"type":"SYSTEM_MESSAGE","source":"SYSTEM","content":"Read only."}` + "\n" +
		`{"step_index":1,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"Inspect the sample."}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","content":"Looking now.","thinking":"Read the file first.","input_tokens":500,"tool_calls":[{"name":"read_file","args":{"path":"sample.txt"}}]}` + "\n" +
		`{"step_index":3,"type":"GENERIC","content":"The sample has one line of plain text."}` + "\n" +
		`{"step_index":4,"type":"PLANNER_RESPONSE","content":"The file looks good.","input_tokens":500}` + "\n"
	const secondTurn = "" +
		`{"step_index":6,"type":"USER_INPUT","content":"What next?"}` + "\n" +
		`{"step_index":7,"type":"PLANNER_RESPONSE","content":"Try the example.","input_tokens":800,"context_window":10000}` + "\n"
	const checkpoint = `{"step_index":5,"type":"CHECKPOINT","content":"Summary of prior work","input_tokens":2000}` + "\n"

	merge := func(t *testing.T, raw string, existing *proto.ContextBreakdown) *proto.ContextBreakdown {
		t.Helper()
		agyTestFile(t, n.agyDataRoot(), agyTestID, raw)
		s := proto.Session{Agent: agyAgent}
		s.SetContextBreakdown(existing)
		n.mergeAgyTranscript(&s, agyTestID)
		return s.ContextBreakdown()
	}
	bd := merge(t, firstTurn+secondTurn, nil)
	if bd == nil || bd.Source != proto.ContextBreakdownEstimated ||
		bd.SystemAndRules == nil || *bd.SystemAndRules <= 0 ||
		bd.ToolsAndMCP == nil || *bd.ToolsAndMCP <= 0 ||
		bd.Conversation == nil || bd.ToolOutputs == nil || *bd.Conversation <= *bd.ToolOutputs || *bd.ToolOutputs <= 0 ||
		bd.Free == nil || *bd.Free != 9200 {
		t.Fatalf("multi-turn category breakdown = %+v", bd)
	}
	if got := *bd.SystemAndRules + *bd.ToolsAndMCP + *bd.Conversation + *bd.ToolOutputs; got != 800 {
		t.Fatalf("classified %d context tokens, want 800", got)
	}

	steps, _ := parseAgySteps([]byte(firstTurn+checkpoint+secondTurn), agyTestID)
	stats := extractAgyUsage(steps).contextStats
	if want := (transcript.ContextStats{ConversationBytes: 26, CompactedInWindow: true}); stats != want {
		t.Fatalf("post-checkpoint stats = %+v, want %+v", stats, want)
	}
	bd = merge(t, firstTurn+checkpoint+secondTurn, nil)
	if want := (&proto.ContextBreakdown{Source: proto.ContextBreakdownEstimated, Conversation: proto.Int(800), Free: proto.Int(9200)}); !bd.Equal(want) {
		t.Fatalf("post-checkpoint breakdown = %+v, want %+v", bd, want)
	}

	// A pending or usage-only step can leave one of the two inputs to estimation absent.
	for name, raw := range map[string]string{
		"empty":          "",
		"missing usage":  `{"type":"USER_INPUT","content":"Hello"}` + "\n",
		"no input count": `{"type":"PLANNER_RESPONSE","content":"Hello","output_tokens":4}` + "\n",
		"no text":        `{"type":"PLANNER_RESPONSE","input_tokens":800,"context_window":10000}` + "\n",
		"after compact":  firstTurn + checkpoint,
	} {
		t.Run(name, func(t *testing.T) {
			if got := merge(t, raw, bd); got != nil {
				t.Fatalf("breakdown without evidence = %+v", got)
			}
		})
	}
	exact := &proto.ContextBreakdown{Source: proto.ContextBreakdownExact, Conversation: proto.Int(111), Free: proto.Int(9200)}
	if got := merge(t, firstTurn+secondTurn, exact); !got.Equal(exact) {
		t.Fatalf("native breakdown replaced: %+v, want %+v", got, exact)
	}
}

func TestAgyContextStatsBoundTextAndIgnoreMedia(t *testing.T) {
	blob := strings.Repeat("A+/a", 128<<10)
	stats := extractAgyUsage([]agyStep{
		{Type: "USER_INPUT", Content: "Input", Media: json.RawMessage(`[{"uri":"data:image/png;base64,` + blob + `"}]`)},
		{Type: "PLANNER_RESPONSE", Thinking: "考え", ToolCalls: []*agyToolCall{
			{Name: "upload", Args: json.RawMessage(`{"data":"` + blob + `"}`)},
		}},
		{Type: "GENERIC", Content: strings.Repeat("actual tool output ", 8192)},
	}).contextStats
	if stats.ConversationBytes != 13 || stats.SystemBytes != 0 || stats.ToolsBytes < 6 || stats.ToolsBytes > 100 ||
		stats.ToolOutputBytes < 100 || stats.ToolOutputBytes > 4096 {
		t.Fatalf("bounded context stats = %+v", stats)
	}
}

func TestAgyPaneSessionTokensAndInfo(t *testing.T) {
	resetAgyModelWindowCache(t)
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	n.state.put(agyTestRow, sessionState{Launcher: "my-agy", Cwd: home, Model: "gemini-4-carbon-high", StartedAt: time.Now()})
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))

	agyTestFile(t, n.agyDataRoot(), agyTestID, ""+
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-21T10:00:00Z","content":"Check"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:01Z","model":"gemini-4-carbon-high","thinking":"Looking","content":"Checking now","input_tokens":10000,"cache_read_tokens":20000,"output_tokens":150,"tool_calls":[{"name":"run_command","args":{"CommandLine":"ls"}}]}`+"\n"+
		`{"step_index":2,"type":"GENERIC","status":"DONE","created_at":"2026-11-21T10:00:02Z","content":"ok"}`+"\n"+
		`{"step_index":3,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:03Z","model":"gemini-4-carbon-high","content":"All good.","input_tokens":4500,"cache_read_tokens":30000,"cache_write_tokens":500,"output_tokens":250}`+"\n")

	if row := listed(t, n)[agyTestRow]; row.ContextWindow != 1_048_576 || row.ContextTokens != 35_000 {
		t.Fatalf("before launcher catalog: got %d / %d tokens", row.ContextTokens, row.ContextWindow)
	}
	n.setCatalog([]proto.ManagedLauncher{{
		Name:   "my-agy",
		Agent:  agyAgent,
		Model:  "gemini-4-carbon",
		Models: []proto.ProviderModel{{ID: "gemini-4-carbon", ContextWindow: 1_000_000}},
	}})
	row := listed(t, n)[agyTestRow]
	if row.Model != "gemini-4-carbon-high" || row.ContextTokens != 35_000 || row.ContextWindow != 1_000_000 || row.OutputTokensValue() != 400 {
		t.Fatalf("pane row = model:%q ctx:%d/%d out:%d", row.Model, row.ContextTokens, row.ContextWindow, row.OutputTokensValue())
	}
	bd := row.ContextBreakdown()
	if bd == nil || bd.Source != proto.ContextBreakdownEstimated || bd.Free == nil || *bd.Free != 965_000 {
		t.Fatalf("pane breakdown = %+v", bd)
	}

	items, _ := agyTestItems(t, n, agyTestRow, nil)
	var withUsage []proto.Item
	for _, it := range items {
		if len(it.Usage) > 0 {
			withUsage = append(withUsage, it)
		}
	}
	if len(withUsage) != 2 {
		t.Fatalf("items with usage = %+v", withUsage)
	}
	if !reflect.DeepEqual(withUsage[0].Usage, []proto.TokenUsage{{Model: "gemini-4-carbon-high", Input: 10000, CacheRead: 20000, Output: 150}}) {
		t.Fatalf("first reply usage = %+v", withUsage[0].Usage)
	}
	if !reflect.DeepEqual(withUsage[1].Usage, []proto.TokenUsage{{Model: "gemini-4-carbon-high", Input: 4500, CacheRead: 30000, CacheWrite: 500, Output: 250}}) {
		t.Fatalf("second reply usage = %+v", withUsage[1].Usage)
	}

	info := agyTestCall(t, n, "session.info", agyTestRow, nil).(map[string]any)
	if info["usage_unknown"] != nil || info["context_tokens"] != 35_000 || info["output_tokens"] != 400 || info["turns"] != 1 || info["compactions"] != 0 || info["model"] != "gemini-4-carbon-high" {
		t.Fatalf("pane session.info = %+v", info)
	}
	if got, _ := info["context_breakdown"].(*proto.ContextBreakdown); !got.Equal(bd) {
		t.Fatalf("pane session.info breakdown = %+v, want %+v", got, bd)
	}

	// Unknown model without catalog keeps ContextTokens and OutputTokens while leaving ContextWindow == 0.
	agyTestFile(t, n.agyDataRoot(), agyOtherTestID, ""+
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-21T10:00:00Z","content":"Hi"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:01Z","model":"unknown-model-high","content":"Hello","input_tokens":8000,"output_tokens":90}`+"\n")
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyOtherTestID))
	n.state.put(agyTestRow, sessionState{Launcher: "agy", Cwd: home, StartedAt: time.Now()})
	row = listed(t, n)[agyTestRow]
	if row.ContextTokens != 8000 || row.ContextWindow != 0 || row.OutputTokensValue() != 90 {
		t.Fatalf("unknown model row = ctx:%d/%d out:%d, want 8000/0 out 90", row.ContextTokens, row.ContextWindow, row.OutputTokensValue())
	}
	if bd := row.ContextBreakdown(); bd == nil || bd.Free != nil {
		t.Fatalf("unknown model breakdown = %+v, want an estimate with no free count", bd)
	}
}

func TestAgyRuntimeSessionTokensItemsAndInfo(t *testing.T) {
	resetAgyModelWindowCache(t)
	n, _ := runtimeNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	t.Cleanup(freshOffline)

	const (
		claudeConv  = agyTestID
		unknownConv = agyOtherTestID
		rewoundConv = "33333333-4444-4555-8666-777777777777"
		claudeSID   = "agy-" + claudeConv
		unknownSID  = "agy-" + unknownConv
		rewoundSID  = "agy-" + rewoundConv
	)

	// Put unfiltered raw transcripts on disk whose step indices and tails deliberately diverge from the
	// companion runtime's projected view (simulating both an inserted PERSONA_MODE_NOTICE shifting every
	// subsequent seq by +1 and rewound-away replies left behind in transcript_full.jsonl). The node must
	// trust the companion's projected items and session totals rather than overwriting them from raw disk.
	fullPath := filepath.Join(n.agyDataRoot(), "brain", claudeConv, agyFullTranscriptPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, fullPath, ""+
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-21T10:00:00Z","content":"Run check"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:01Z","model":"stale-disk-model","content":"old","input_tokens":99999,"output_tokens":999}`+"\n"+
		`{"step_index":2,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:02Z","model":"rewound-tail-model","content":"abandoned tail","input_tokens":888888,"output_tokens":8888}`+"\n")

	agyTestFile(t, n.agyDataRoot(), rewoundConv, ""+
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-21T10:00:00Z","content":"Rewound prompt"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:01Z","model":"claude-opus-5-5-high","content":"Rewound away","input_tokens":70000,"output_tokens":700}`+"\n")
	agyTestFile(t, n.agyDataRoot(), unknownConv, ""+
		`{"step_index":0,"type":"USER_INPUT","content":"Inspect"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Done","input_tokens":10000,"output_tokens":55}`+"\n")

	hello := map[string]any{
		"runtime": agyAgent, "name": "Antigravity", "agent": agyAgent, "launcher": agyAgent,
		"caps": map[string]any{"send": true, "interrupt": true},
	}
	rt := dialRuntime(t, n, hello)
	now := time.Now().UTC()
	wantStep3Usage := []proto.TokenUsage{{Model: "claude-opus-5-5-high", Input: 1200, CacheRead: 48800, CacheWrite: 300, Output: 180}}
	wantStep5Usage := []proto.TokenUsage{{Model: "claude-opus-5-5-high", Input: 500, CacheRead: 51000, Output: 70}}
	// Companion-projected items after an Auto-mode refusal inserted PERSONA_MODE_NOTICE at seq 1,
	// shifting user/assistant steps to seq 2, 3, 4, 5.
	projectedItems := []proto.Item{
		{ID: "1@notice#0", Seq: 1, TS: now, Role: "system", Kind: proto.KindNotice, Text: "mode fallback notice"},
		{ID: "2@a#0", Seq: 2, TS: now, Role: "user", Kind: proto.KindText, Text: "Run check"},
		{ID: "3@b#0", Seq: 3, TS: now, Role: "assistant", Kind: proto.KindThinking, Text: "Thinking", Model: "claude-opus-5-5-high", Usage: wantStep3Usage},
		{ID: "3@b#1", Seq: 3, TS: now, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "c1", Model: "claude-opus-5-5-high"},
		{ID: "4@c#0", Seq: 4, TS: now, Role: "user", Kind: proto.KindToolResult, ToolUseID: "c1", Text: "ok"},
		{ID: "5@d#0", Seq: 5, TS: now, Role: "assistant", Kind: proto.KindText, Text: "Done.", Model: "claude-opus-5-5-high", Usage: wantStep5Usage},
	}
	rt.mu.Lock()
	rt.answer = func(method string, raw json.RawMessage) (any, error) {
		switch method {
		case "session.items":
			return map[string]any{"items": projectedItems, "has_more": false, "to_seq": 5, "history": "agy:test:auto-refusal:0"}, nil
		case "session.item":
			var p struct {
				ItemID string `json:"item_id"`
			}
			_ = json.Unmarshal(raw, &p)
			for _, it := range projectedItems {
				if it.ID == p.ItemID {
					return it, nil
				}
			}
			return nil, os.ErrNotExist
		}
		return nil, nil
	}
	rt.mu.Unlock()

	_ = rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{
			"session_id": claudeSID, "agent": agyAgent, "cwd": n.home, "status": "done", "launcher": "agy",
			"model": "claude-opus-5-5-high", "context_tokens": 51_500, "output_tokens": 250,
		},
		{
			"session_id": unknownSID, "agent": agyAgent, "cwd": n.home, "status": "done", "launcher": "agy",
			"model": "opaque-agy-model-medium", "context_tokens": 10_000, "output_tokens": 55,
		},
		// Fully rewound session: companion reports no tokens/model despite stale raw transcript on disk.
		{
			"session_id": rewoundSID, "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": "agy",
		},
	}})
	waitFor(t, "agy runtime sessions listed", func() bool {
		g := listed(t, n)
		return g[claudeSID].SessionID != "" && g[unknownSID].SessionID != "" && g[rewoundSID].SessionID != ""
	})

	got := listed(t, n)
	if s := got[claudeSID]; s.Model != "claude-opus-5-5-high" || s.ContextTokens != 51_500 || s.ContextWindow != 1_000_000 || s.OutputTokensValue() != 250 {
		t.Fatalf("claudeSID row = model:%q ctx:%d/%d out:%d", s.Model, s.ContextTokens, s.ContextWindow, s.OutputTokensValue())
	}
	if row := got[claudeSID]; row.ContextBreakdown() != nil {
		t.Fatalf("runtime row used unrelated disk stats: %+v", row.ContextBreakdown())
	}
	if s := got[unknownSID]; s.Model != "opaque-agy-model-medium" || s.ContextTokens != 10_000 || s.ContextWindow != 0 || s.OutputTokensValue() != 55 {
		t.Fatalf("unknownSID row = model:%q ctx:%d/%d out:%d", s.Model, s.ContextTokens, s.ContextWindow, s.OutputTokensValue())
	}
	if s := got[rewoundSID]; s.Model != "" || s.ContextTokens != 0 || s.ContextWindow != 0 || s.OutputTokensValue() != 0 {
		t.Fatalf("rewoundSID row must stay cleared despite stale disk transcript = model:%q ctx:%d/%d out:%d", s.Model, s.ContextTokens, s.ContextWindow, s.OutputTokensValue())
	}
	if row := got[rewoundSID]; row.ContextBreakdown() != nil {
		t.Fatalf("rewound runtime row breakdown = %+v", row.ContextBreakdown())
	}

	// Verify session.items preserves companion-projected shifted-seq usage and model without raw-disk clobbering.
	rawPage, err := nodeCall(t, n, "session.items", map[string]any{"session_id": claudeSID})
	if err != nil {
		t.Fatalf("session.items: %v", err)
	}
	var page struct {
		Items []proto.Item `json:"items"`
	}
	if err := json.Unmarshal(rawPage, &page); err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Usage != nil || page.Items[0].Model != "" {
		t.Fatalf("notice item must not receive usage or model: %+v", page.Items[0])
	}
	if !reflect.DeepEqual(page.Items[2].Usage, wantStep3Usage) || page.Items[3].Usage != nil || page.Items[3].Model != "claude-opus-5-5-high" {
		t.Fatalf("shifted step 3 items = %+v / %+v", page.Items[2], page.Items[3])
	}
	if !reflect.DeepEqual(page.Items[5].Usage, wantStep5Usage) || page.Items[5].Model != "claude-opus-5-5-high" {
		t.Fatalf("shifted step 5 item = %+v", page.Items[5])
	}

	// Verify fold_work rolls companion-projected usage into Fold.Usage.
	rawFolded, err := nodeCall(t, n, "session.items", map[string]any{"session_id": claudeSID, "fold_work": true})
	if err != nil {
		t.Fatalf("session.items fold_work: %v", err)
	}
	var foldedPage struct {
		Items []proto.Item `json:"items"`
	}
	if err := json.Unmarshal(rawFolded, &foldedPage); err != nil {
		t.Fatal(err)
	}
	if len(foldedPage.Items) != 4 || foldedPage.Items[2].Fold == nil || !reflect.DeepEqual(foldedPage.Items[2].Fold.Usage, wantStep3Usage) {
		t.Fatalf("folded runtime page = %+v", foldedPage.Items)
	}

	// Verify session.item passes through primary (#0) and secondary (#1) without raw-disk interference.
	rawPrimary, err := nodeCall(t, n, "session.item", map[string]any{"session_id": claudeSID, "item_id": "3@b#0"})
	if err != nil {
		t.Fatalf("session.item primary: %v", err)
	}
	var primary proto.Item
	if err := json.Unmarshal(rawPrimary, &primary); err != nil || !reflect.DeepEqual(primary.Usage, wantStep3Usage) || primary.Model != "claude-opus-5-5-high" {
		t.Fatalf("primary item = %+v, err=%v", primary, err)
	}
	rawSecondary, err := nodeCall(t, n, "session.item", map[string]any{"session_id": claudeSID, "item_id": "3@b#1"})
	if err != nil {
		t.Fatalf("session.item secondary: %v", err)
	}
	var secondary proto.Item
	if err := json.Unmarshal(rawSecondary, &secondary); err != nil || secondary.Usage != nil || secondary.Model != "claude-opus-5-5-high" {
		t.Fatalf("secondary item = %+v, err=%v", secondary, err)
	}

	// Verify live pushed session.items from the runtime preserve companion-projected usage untouched.
	hub := withHub(t, n)
	_ = rt.peer.Notify("session.items", map[string]any{
		"session_id": claudeSID, "from_seq": 4, "to_seq": 5, "history": "agy:test:auto-refusal:0",
		"items": []proto.Item{{ID: "5@d#0", Seq: 5, TS: now, Role: "assistant", Kind: proto.KindText, Text: "Done.", Model: "claude-opus-5-5-high", Usage: wantStep5Usage}},
	})
	waitFor(t, "projected live push reaches hub", func() bool { return len(hub.got()) > 0 })
	if pushed := hub.got()[0]; !reflect.DeepEqual(pushed.Usage, wantStep5Usage) || pushed.Model != "claude-opus-5-5-high" {
		t.Fatalf("pushed runtime item = %+v", pushed)
	}

	// Verify session.info uses the companion's projected row totals and never resurrects rewound disk transcripts.
	rawInfo, err := nodeCall(t, n, "session.info", map[string]any{"session_id": claudeSID})
	if err != nil {
		t.Fatalf("session.info claudeSID: %v", err)
	}
	var info map[string]any
	_ = json.Unmarshal(rawInfo, &info)
	if info["usage_unknown"] != nil || info["context_tokens"] != float64(51_500) || info["output_tokens"] != float64(250) || info["model"] != "claude-opus-5-5-high" {
		t.Fatalf("runtime session.info (populated) = %+v", info)
	}

	rawRewoundInfo, err := nodeCall(t, n, "session.info", map[string]any{"session_id": rewoundSID})
	if err != nil {
		t.Fatalf("session.info rewoundSID: %v", err)
	}
	var rewoundInfo map[string]any
	_ = json.Unmarshal(rawRewoundInfo, &rewoundInfo)
	if rewoundInfo["usage_unknown"] != true || rewoundInfo["context_tokens"] != float64(0) || rewoundInfo["output_tokens"] != float64(0) || rewoundInfo["model"] != "" {
		t.Fatalf("runtime session.info (rewound) must not resurrect stale disk transcript = %+v", rewoundInfo)
	}

	// Verify runtime session with agyModelList catalog window resolves dynamically without fabricating for others.
	recordAgyModelWindows(map[string]int{"opaque-agy-model": 256_000})
	s := listed(t, n)[unknownSID]
	if s.ContextTokens != 10_000 || s.ContextWindow != 256_000 {
		t.Fatalf("after agyModelList window recorded: ctx=%d/%d, want 10000/256000", s.ContextTokens, s.ContextWindow)
	}
	want := &proto.ContextBreakdown{Source: proto.ContextBreakdownEstimated, Conversation: proto.Int(10_000), Free: proto.Int(246_000)}
	if bd := s.ContextBreakdown(); !bd.Equal(want) {
		t.Fatalf("companion row breakdown = %+v, want %+v", bd, want)
	}
	rawInfo, err = nodeCall(t, n, "session.info", map[string]any{"session_id": unknownSID})
	var summary struct {
		Breakdown *proto.ContextBreakdown `json:"context_breakdown"`
	}
	if err != nil || json.Unmarshal(rawInfo, &summary) != nil || !summary.Breakdown.Equal(want) {
		t.Fatalf("companion summary = %s, err=%v", rawInfo, err)
	}
}

func TestAgyPanePrefersCompanionProjectedUsageOverUnfilteredDisk(t *testing.T) {
	resetAgyModelWindowCache(t)
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	t.Cleanup(freshOffline)

	// Disk still holds a rewound-away second reply with huge token counts.
	agyTestFile(t, n.agyDataRoot(), agyTestID, ""+
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-21T10:00:00Z","content":"Hi"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:01Z","model":"claude-opus-5-5-high","content":"Turn 1","input_tokens":12000,"output_tokens":110}`+"\n"+
		`{"step_index":2,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-21T10:00:02Z","model":"stale-rewound","content":"Abandoned","input_tokens":400000,"output_tokens":5000}`+"\n")
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))

	n.runtimes = newRuntimeRegistry()
	runtime := n.runtimes.add(nil)
	runtime.ready, runtime.hello.Runtime, runtime.hello.Agent = true, agyAgent, agyAgent
	companion := proto.Session{
		SessionID: agyAgent + "-" + agyTestID, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle,
		Model: "claude-opus-5-5-high", ContextTokens: 12_000,
	}
	companion.SetOutputTokens(110)
	n.runtimes.setSessions(runtime, []proto.Session{companion})

	var row proto.Session
	for _, r := range listSessions(t, n) {
		if r.SessionID == agyTestRow {
			row = r
		}
	}
	if row.Model != "claude-opus-5-5-high" || row.ContextTokens != 12_000 || row.ContextWindow != 1_000_000 || row.OutputTokensValue() != 110 {
		t.Fatalf("pane row with companion = model:%q ctx:%d/%d out:%d", row.Model, row.ContextTokens, row.ContextWindow, row.OutputTokensValue())
	}
	if bd := row.ContextBreakdown(); bd != nil {
		t.Fatalf("companion row used stale transcript stats: %+v", bd)
	}
	info := agyTestCall(t, n, "session.info", agyTestRow, nil).(map[string]any)
	if info["usage_unknown"] != nil || info["context_tokens"] != 12_000 || info["output_tokens"] != 110 || info["model"] != "claude-opus-5-5-high" {
		t.Fatalf("pane session.info with companion = %+v", info)
	}

	// Once disk agrees with the companion, the estimate reaches the merged herdr row too.
	agyTestFile(t, n.agyDataRoot(), agyTestID, ""+
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"Hi"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","model":"claude-opus-5-5-high","content":"Done","input_tokens":12000,"output_tokens":110}`+"\n")
	companion.ContextWindow = 1_000_000
	n.runtimes.setSessions(runtime, []proto.Session{companion})
	row = listed(t, n)[agyTestRow]
	estimate := &proto.ContextBreakdown{Source: proto.ContextBreakdownEstimated, Conversation: proto.Int(12_000), Free: proto.Int(988_000)}
	if got := row.ContextBreakdown(); !got.Equal(estimate) {
		t.Fatalf("pane row omitted matching estimate: %+v", got)
	}

	exact := &proto.ContextBreakdown{Source: proto.ContextBreakdownExact, Conversation: proto.Int(12_000), Free: proto.Int(988_000)}
	companion.SetContextBreakdown(exact)
	n.runtimes.setSessions(runtime, []proto.Session{companion})
	row = listed(t, n)[agyTestRow]
	if got := row.ContextBreakdown(); !got.Equal(exact) {
		t.Fatalf("pane row changed companion's exact breakdown: %+v", got)
	}
	info = agyTestCall(t, n, "session.info", agyTestRow, nil).(map[string]any)
	if got, _ := info["context_breakdown"].(*proto.ContextBreakdown); !got.Equal(exact) {
		t.Fatalf("pane summary changed companion's exact breakdown: %+v", got)
	}

	// Full rewind on the companion row must also keep pane row and session.info cleared instead of reading stale disk.
	n.runtimes.setSessions(runtime, []proto.Session{{
		SessionID: agyAgent + "-" + agyTestID, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle,
	}})
	row = proto.Session{}
	for _, r := range listSessions(t, n) {
		if r.SessionID == agyTestRow {
			row = r
		}
	}
	if row.Model != "" || row.ContextTokens != 0 || row.ContextWindow != 0 || row.OutputTokensValue() != 0 {
		t.Fatalf("fully rewound pane row must stay cleared = model:%q ctx:%d/%d out:%d", row.Model, row.ContextTokens, row.ContextWindow, row.OutputTokensValue())
	}
	if got := row.ContextBreakdown(); got != nil {
		t.Fatalf("rewound pane row breakdown = %+v", got)
	}
	info = agyTestCall(t, n, "session.info", agyTestRow, nil).(map[string]any)
	if info["usage_unknown"] != true || info["context_tokens"] != 0 || info["output_tokens"] != 0 {
		t.Fatalf("fully rewound pane session.info must stay usage_unknown = %+v", info)
	}
}

func TestAgyRuntimeRowContextWindowWithoutLocalTranscript(t *testing.T) {
	resetAgyModelWindowCache(t)
	n, _ := runtimeNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	t.Cleanup(freshOffline)

	n.setCatalog([]proto.ManagedLauncher{{
		Name:   "my-agy",
		Agent:  agyAgent,
		Models: []proto.ProviderModel{{ID: "gemini-4-carbon", ContextWindow: 1_000_000}},
	}})
	rt := dialRuntime(t, n, map[string]any{"runtime": agyAgent, "agent": agyAgent})
	_ = rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": "agy-remote-1", "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": "my-agy", "model": "gemini-4-carbon-high", "context_tokens": 42_000, "output_tokens": 120},
		{"session_id": "agy-remote-2", "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": "agy", "model": "unknown-remote", "context_tokens": 9_000, "output_tokens": 30},
		{"session_id": "agy-remote-3", "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": "agy", "model": "gemini-3.8-flash-low", "context_tokens": 15_000},
	}})
	waitFor(t, "remote agy rows listed", func() bool {
		g := listed(t, n)
		return g["agy-remote-1"].SessionID != "" && g["agy-remote-2"].SessionID != ""
	})
	got := listed(t, n)
	if s := got["agy-remote-1"]; s.ContextTokens != 42_000 || s.ContextWindow != 1_000_000 || s.OutputTokensValue() != 120 {
		t.Fatalf("agy-remote-1 = ctx:%d/%d out:%d", s.ContextTokens, s.ContextWindow, s.OutputTokensValue())
	}
	if s := got["agy-remote-2"]; s.ContextTokens != 9_000 || s.ContextWindow != 0 || s.OutputTokensValue() != 30 {
		t.Fatalf("agy-remote-2 = ctx:%d/%d out:%d", s.ContextTokens, s.ContextWindow, s.OutputTokensValue())
	}
	if s := got["agy-remote-3"]; s.ContextTokens != 15_000 || s.ContextWindow != 1_048_576 {
		t.Fatalf("Gemini runtime row = %d/%d", s.ContextTokens, s.ContextWindow)
	}
}

func TestAgyPaneUpdatedAtFromRuntimeAndTranscript(t *testing.T) {
	resetAgyModelWindowCache(t)
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	t.Cleanup(freshOffline)

	const raw = `{"step_index":0,"type":"USER_INPUT","content":"Hello"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Hi there","model":"gemini-3.1-pro-low","input_tokens":500,"output_tokens":20}` + "\n"
	transcriptPath := agyTestFile(t, n.agyDataRoot(), agyTestID, raw)
	transcriptAt := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(transcriptPath, transcriptAt, transcriptAt); err != nil {
		t.Fatal(err)
	}
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))

	// Without a companion runtime row, the herdr agy pane falls back to the local transcript's mtime.
	findPane := func() proto.Session {
		for _, r := range listSessions(t, n) {
			if r.SessionID == agyTestRow {
				return r
			}
		}
		t.Fatalf("missing pane row %s", agyTestRow)
		return proto.Session{}
	}
	if got := findPane(); !got.UpdatedAt.Equal(transcriptAt) {
		t.Fatalf("pane UpdatedAt without runtime = %v, want %v", got.UpdatedAt, transcriptAt)
	}

	// With a companion runtime row, the herdr agy pane inherits UpdatedAt and LastText from the runtime row
	// and sorts ahead of older companion-only agy sessions.
	n.runtimes = newRuntimeRegistry()
	runtime := n.runtimes.add(nil)
	runtime.ready, runtime.hello.Runtime, runtime.hello.Agent = true, agyAgent, agyAgent
	olderAt := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	newerAt := time.Date(2026, 11, 24, 18, 30, 0, 0, time.UTC)
	n.runtimes.setSessions(runtime, []proto.Session{
		{SessionID: agyAgent + "-" + agyTestID, Agent: agyAgent, Cwd: home, Status: proto.StatusWorking, UpdatedAt: newerAt, LastText: "Latest reply"},
		{SessionID: agyAgent + "-older-companion", Agent: agyAgent, Cwd: home, Status: proto.StatusDone, UpdatedAt: olderAt, LastText: "Old reply"},
	})
	rows := listSessions(t, n)
	var paneIdx, olderIdx = -1, -1
	for i, r := range rows {
		if r.SessionID == agyTestRow {
			paneIdx = i
			if !r.UpdatedAt.Equal(newerAt) || r.LastText != "Latest reply" {
				t.Fatalf("pane with runtime = updated:%v lastText:%q, want %v / %q", r.UpdatedAt, r.LastText, newerAt, "Latest reply")
			}
		}
		if r.SessionID == agyAgent+"-older-companion" {
			olderIdx = i
		}
	}
	if paneIdx < 0 || olderIdx < 0 || paneIdx >= olderIdx {
		t.Fatalf("expected live herdr agy pane (idx=%d) to sort ahead of older companion row (idx=%d)", paneIdx, olderIdx)
	}

	companion := proto.Session{SessionID: agyAgent + "-" + agyTestID, Agent: agyAgent, Cwd: home, Status: proto.StatusDone,
		UpdatedAt: newerAt.Add(time.Minute), LastText: "BOTMUX_NOTHING_TO_SEND"}
	n.runtimes.setSessions(runtime, normalizeRuntimeSessions(runtime, []proto.Session{companion}))
	if got := findPane(); got.LastText != "Latest reply" || !got.UpdatedAt.Equal(companion.UpdatedAt) {
		t.Fatalf("pane after a delivery marker: last text %q, updated %v", got.LastText, got.UpdatedAt)
	}
	// A transcript with no step timestamps uses its modification time.
	companion.LastText = "Earlier companion reply"
	companion.UpdatedAt = transcriptAt.Add(-time.Minute)
	n.runtimes.setSessions(runtime, []proto.Session{companion})
	if got := findPane(); got.LastText != "Hi there" {
		t.Fatalf("older companion snapshot displaced transcript preview: %q", got.LastText)
	}

	// Later checkpoint writes do not change the time of the visible assistant text.
	agyTestAppend(t, transcriptPath, `{"step_index":2,"type":"PLANNER_RESPONSE","status":"ACTIVE","created_at":"2026-11-24T11:00:00Z","content":"Checking the example"}`+"\n"+
		`{"step_index":3,"type":"CHECKPOINT","created_at":"2026-11-24T13:00:00Z"}`+"\n")
	checkpointAt := transcriptAt.Add(3 * time.Hour)
	if err := os.Chtimes(transcriptPath, checkpointAt, checkpointAt); err != nil {
		t.Fatal(err)
	}
	companion.LastText = "Companion update"
	companion.UpdatedAt = transcriptAt.Add(2 * time.Hour)
	n.runtimes.setSessions(runtime, []proto.Session{companion})
	if got := findPane(); got.LastText != "Companion update" {
		t.Fatalf("checkpoint displaced newer companion preview: %q", got.LastText)
	}

	agyTestAppend(t, transcriptPath, `{"step_index":4,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:00:00Z","content":"Transcript update"}`+"\n")
	companion.UpdatedAt = transcriptAt.Add(5 * time.Hour)
	companion.LastText = "BOTMUX_NOTHING_TO_SEND"
	n.runtimes.setSessions(runtime, normalizeRuntimeSessions(runtime, []proto.Session{companion}))
	if got := findPane(); got.LastText != "Transcript update" || !got.UpdatedAt.Equal(companion.UpdatedAt) {
		t.Fatalf("marker displaced transcript preview: %q, updated %v", got.LastText, got.UpdatedAt)
	}

	agyTestAppend(t, transcriptPath, `{"step_index":5,"type":"USER_INPUT","content":"Next question"}`+"\n")
	if got := findPane(); got.LastText != "" {
		t.Fatalf("new turn showed previous companion preview: %q", got.LastText)
	}
}

// A herdr agy pane's Rev is its transcript's size, with or without a companion row: the light revs event carries only
// rows whose Rev changed, so it is what tells an app an unopened pane gained items (proto.Session.Rev, revsChanged).
// The companion's own rev counts steps, so borrowing it would switch the pane's one row between two scales as the
// companion came and went (#13710).
func TestAgyPaneRevFollowsTheTranscript(t *testing.T) {
	resetAgyModelWindowCache(t)
	n, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	t.Cleanup(freshOffline)

	const first = `{"step_index":0,"type":"USER_INPUT","content":"Hello"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Hi there","model":"gemini-3.1-pro-low","input_tokens":500,"output_tokens":20}` + "\n"
	transcriptPath := agyTestFile(t, n.agyDataRoot(), agyTestID, first)
	seedAgents(t, n, herdr, agyTestAgent(agyTestPane, home, agyTestID))

	findPane := func() proto.Session {
		for _, r := range listSessions(t, n) {
			if r.SessionID == agyTestRow {
				return r
			}
		}
		t.Fatalf("missing pane row %s", agyTestRow)
		return proto.Session{}
	}
	size := func() int64 {
		st, err := os.Stat(transcriptPath)
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}

	// Standalone pane: Rev is the transcript's size.
	if got, want := findPane().Rev, size(); got != want || got == 0 {
		t.Fatalf("standalone pane rev = %d, want the transcript's %d bytes", got, want)
	}

	// A companion row supplies its own step-count rev; the pane keeps the transcript's size instead.
	n.runtimes = newRuntimeRegistry()
	runtime := n.runtimes.add(nil)
	runtime.ready, runtime.hello.Runtime, runtime.hello.Agent = true, agyAgent, agyAgent
	companion := proto.Session{SessionID: agyAgent + "-" + agyTestID, Agent: agyAgent, Cwd: home, Status: proto.StatusWorking,
		UpdatedAt: time.Now().UTC(), Rev: 7}
	n.runtimes.setSessions(runtime, []proto.Session{companion})
	if got, want := findPane().Rev, size(); got != want {
		t.Fatalf("pane with a companion row rev = %d, want the transcript's %d, not the companion's %d", got, want, companion.Rev)
	}

	// A transcript that grows moves the pane's Rev, so the revs event can carry the pane.
	agyTestAppend(t, transcriptPath, `{"step_index":2,"type":"PLANNER_RESPONSE","content":"More"}`+"\n")
	if got, want := findPane().Rev, size(); got != want || got <= companion.Rev {
		t.Fatalf("pane rev after an append = %d, want %d", got, want)
	}

	// A pane whose transcript is not on this computer keeps Rev 0, never the companion's step count.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}
	if got := findPane().Rev; got != 0 {
		t.Fatalf("pane without a transcript rev = %d, want 0", got)
	}
}
