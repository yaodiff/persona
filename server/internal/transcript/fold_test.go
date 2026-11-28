package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// longCodexRollout writes a Codex rollout of two exchanges; the second one runs `commands` commands (each with a
// kilobyte of output), the agent's commentary every few of them (`notes` in all) and `replies` replies on the way —
// the shape of a long session whose first page was 1412 rows and a megabyte, nearly all of it folded on screen.
func longCodexRollout(t testing.TB, commands, notes, replies int) string {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	n := 0
	var lines []string
	add := func(typ string, payload any) {
		b, _ := json.Marshal(map[string]any{"timestamp": t0.Add(time.Duration(n) * time.Second).Format(time.RFC3339Nano), "type": typ, "payload": payload})
		lines = append(lines, string(b))
		n++
	}
	item := func(v map[string]any) { add("event_msg", map[string]any{"type": "item_completed", "item": v}) }
	text := func(s string) []map[string]any { return []map[string]any{{"type": "Text", "text": s}} }
	add("session_meta", map[string]any{"id": "s1", "cwd": "/work/project", "cli_version": "0.163.0"})
	add("turn_context", map[string]any{"model": "model-x"})
	item(map[string]any{"type": "UserMessage", "id": "u1", "content": []map[string]any{{"type": "text", "text": "first question"}}})
	item(map[string]any{"type": "AgentMessage", "id": "a1", "phase": "final_answer", "content": text("first answer")})
	item(map[string]any{"type": "UserMessage", "id": "u2", "content": []map[string]any{{"type": "text", "text": "second question"}}})
	output := strings.Repeat("output line of a command\n", 40) // ~1 KB
	for i := 0; i < commands; i++ {
		if i%(commands/notes+1) == 0 {
			item(map[string]any{"type": "AgentMessage", "id": fmt.Sprintf("n%d", i), "phase": "commentary", "content": text(fmt.Sprintf("note %d before the next command", i))})
		}
		if replies > 1 && i > 0 && i%(commands/(replies-1)+1) == 0 {
			item(map[string]any{"type": "AgentMessage", "id": fmt.Sprintf("r%d", i), "phase": "final_answer", "content": text(fmt.Sprintf("progress %d", i))})
		}
		item(map[string]any{"type": "CommandExecution", "id": fmt.Sprintf("c%d", i), "command": []string{"/bin/zsh", "-lc", fmt.Sprintf("make step-%d", i)},
			"status": "completed", "aggregated_output": output, "exit_code": 0})
	}
	item(map[string]any{"type": "AgentMessage", "id": "a2", "phase": "final_answer", "content": text("second answer")})
	path := filepath.Join(t.TempDir(), "rollout-2026-01-01T00-00-00-s1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func foldable(it proto.Item) bool {
	return it.Kind == proto.KindToolUse || it.Kind == proto.KindToolResult || it.Kind == proto.KindThinking ||
		(it.Kind == proto.KindText && it.Narration) || (it.Kind == proto.KindImage && it.ToolUseID != "")
}

func TestFoldKeepsLatestBashDescription(t *testing.T) {
	run := []proto.Item{
		{Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"description":"First"}`)},
		{Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"description":"确认判断用的是哪个模型\nextra","command":"raw arguments"}`)},
		{Kind: proto.KindToolResult, Text: "raw output"},
		{Kind: proto.KindThinking, Text: "hidden"},
	}
	if got := fold(run).Text; got != "确认判断用的是哪个模型" {
		t.Fatal(got)
	}
	for _, last := range []proto.Item{
		{Kind: proto.KindToolUse, ToolName: "Read"},
		{Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"unlabelled"}`)},
	} {
		if got := fold(append(run, last)).Text; got != "" {
			t.Fatalf("stale caption %q", got)
		}
	}
}

// The phone's first page of a long session carries the prompts, the replies and one fold per run of work (tools,
// thoughts) with its counts — not the work itself, which the chat shows folded anyway. Opening a fold fetches
// exactly what it stands for.
func TestFoldedFirstPageOfALongSession(t *testing.T) {
	f := NewFile(longCodexRollout(t, 650, 110, 6))
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	page, _, _ := f.PageTurns(0, 2, 2000)
	whole, _ := json.Marshal(page)
	folded := FoldWork(page)
	b, _ := json.Marshal(folded)
	t.Logf("first page: %d items, %d bytes; folded: %d items, %d bytes", len(page), len(whole), len(folded), len(b))
	if len(folded) > 40 || len(b) > 32<<10 {
		t.Fatalf("the folded first page has %d items, %d bytes; want at most 40 items and 32 KB", len(folded), len(b))
	}

	var kept []string
	tools, thoughts := 0, 0
	for _, it := range page {
		switch {
		case it.Kind == proto.KindToolUse:
			tools++
		case it.Kind == proto.KindThinking:
			thoughts++
		case !foldable(it):
			kept = append(kept, it.ID)
		}
	}
	var got []string
	var folds []proto.Item
	ftools, fthoughts := 0, 0
	for _, it := range folded {
		if it.Kind != proto.KindFold {
			if foldable(it) {
				t.Fatalf("work left unfolded on the page: %+v", it)
			}
			got = append(got, it.ID)
			continue
		}
		folds = append(folds, it)
		ftools += it.Fold.Tools
		fthoughts += it.Fold.Thoughts
	}
	if strings.Join(got, " ") != strings.Join(kept, " ") {
		t.Fatalf("prompts, replies and notices changed:\n got %v\nwant %v", got, kept)
	}
	if ftools != tools || fthoughts != thoughts || tools != 650 {
		t.Fatalf("folds count %d tools / %d thoughts; the page has %d / %d", ftools, fthoughts, tools, thoughts)
	}

	// Each fold opens into what it folded: the items between it and the next kept item, nothing else.
	at := map[string]int{}
	for i, it := range page {
		at[it.ID] = i
	}
	for k, fo := range folds {
		first, ok := at[strings.TrimPrefix(fo.ID, "fold:")]
		if !ok || page[first].Seq != fo.Seq || !page[first].TS.Equal(fo.TS) {
			t.Fatalf("fold %s does not start at the item it is named after", fo.ID)
		}
		var want []string
		for _, it := range page[first:] {
			if !foldable(it) {
				break
			}
			want = append(want, it.ID)
		}
		var opened []string
		for after := fo.Seq - 1; ; {
			items, more := f.Range(after+1, fo.Fold.ThroughSeq, 100)
			for _, it := range items {
				if foldable(it) {
					opened = append(opened, it.ID)
				}
			}
			if !more {
				break
			}
			after = items[len(items)-1].Seq
		}
		if strings.Join(opened, " ") != strings.Join(want, " ") || fo.Fold.Items != len(want) || fo.Fold.ThroughID != want[len(want)-1] {
			t.Fatalf("fold %d (%s, %d items through %s) opens into %d items, want %d", k, fo.ID, fo.Fold.Items, fo.Fold.ThroughID, len(opened), len(want))
		}
		if last := page[at[fo.Fold.ThroughID]]; !fo.Fold.Until.Equal(last.TS) {
			t.Fatalf("fold %s ends at %v, its last item at %v", fo.ID, fo.Fold.Until, last.TS)
		}
	}
}

// A running page still has an assistant answer to show; once its final answer arrives, the earlier commentary joins
// the completed work. Both reads go through the same real rollout pagination as the chat's first page.
func TestFoldedPageKeepsRunningCommentaryVisible(t *testing.T) {
	path := longCodexRollout(t, 6, 2, 1)
	complete, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(complete)), "\n")
	running := strings.Join(lines[:len(lines)-1], "\n") + "\n" // drop the second turn's final answer
	if err := os.WriteFile(path, []byte(running), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFile(path)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	page, _, _ := f.PageTurns(0, 2, 2000)
	folded := FoldWorkLatest(page)
	visible := 0
	for _, it := range folded {
		if it.Kind == proto.KindText && it.Narration {
			visible++
			if it.Text != "note 4 before the next command" {
				t.Fatalf("the visible commentary is not the latest: %+v", it)
			}
		}
	}
	if visible != 1 {
		t.Fatalf("a running folded page hides its latest commentary: %+v", folded)
	}
	for _, it := range FoldWork(page) {
		if it.Kind == proto.KindText && it.Narration {
			t.Fatalf("a historical page keeps commentary visible without knowing no final answer follows: %+v", it)
		}
	}

	if err := os.WriteFile(path, complete, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	page, _, _ = f.PageTurns(0, 2, 2000)
	folded = FoldWorkLatest(page)
	final := 0
	for _, it := range folded {
		if it.Kind == proto.KindText && it.Narration {
			t.Fatalf("finished commentary escaped the work fold: %+v", it)
		}
		if it.Kind == proto.KindText && it.Text == "second answer" {
			final++
		}
	}
	if final != 1 {
		t.Fatalf("the finished page lost its final answer: %+v", folded)
	}
}

// A tool call still running (its result not in the page) and a result whose call is on an older page stay as they
// are: folded away, the app would show the call running forever or lose the result. A short run is not worth a
// round trip and stays too.
func TestFoldKeepsUnpairedCallsAndShortRuns(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(seq int, role, kind, tool string) proto.Item {
		return proto.Item{ID: fmt.Sprintf("l%d#0", seq), Seq: seq, TS: ts.Add(time.Duration(seq) * time.Second), Role: role, Kind: kind, ToolUseID: tool, Text: "x"}
	}
	page := []proto.Item{
		mk(1, "user", proto.KindToolResult, "old"), // its call is on an older page
		mk(2, "user", proto.KindText, ""),
		mk(3, "assistant", proto.KindThinking, ""),
		mk(4, "assistant", proto.KindToolUse, "a"),
		mk(5, "user", proto.KindToolResult, "a"),
		mk(6, "assistant", proto.KindText, ""),
		mk(7, "assistant", proto.KindToolUse, "b"),
		mk(8, "user", proto.KindToolResult, "b"),
		mk(9, "assistant", proto.KindToolUse, "c"),
		mk(10, "user", proto.KindToolResult, "c"),
		mk(11, "assistant", proto.KindThinking, ""),
		mk(12, "assistant", proto.KindToolUse, "running"),
	}
	var got []string
	for _, it := range FoldWork(page) {
		if it.Kind == proto.KindFold {
			got = append(got, fmt.Sprintf("fold%d-%d", it.Seq, it.Fold.ThroughSeq))
		} else {
			got = append(got, fmt.Sprint(it.Seq))
		}
	}
	want := "1 2 fold3-5 6 fold7-11 12"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
	short := []proto.Item{mk(2, "user", proto.KindText, ""), mk(3, "assistant", proto.KindToolUse, "a"), mk(4, "user", proto.KindToolResult, "a"), mk(5, "assistant", proto.KindText, "")}
	if out := FoldWork(short); len(out) != 4 {
		t.Fatalf("a call and its result were folded: %+v", out)
	}
}

// A subagent's call and its result are never folded: the chat draws them as the subagent's card, which stays on the
// page (#5602). The work on either side of it folds on its own.
func TestFoldLeavesSubagentCallsOnThePage(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(seq int, role, kind, call, name string) proto.Item {
		return proto.Item{ID: fmt.Sprintf("l%d#0", seq), Seq: seq, TS: ts.Add(time.Duration(seq) * time.Second), Role: role, Kind: kind, ToolUseID: call, ToolName: name, Text: "x"}
	}
	page := []proto.Item{
		mk(1, "user", proto.KindText, "", ""),
		mk(2, "assistant", proto.KindToolUse, "a", "Bash"), mk(3, "user", proto.KindToolResult, "a", ""),
		mk(4, "assistant", proto.KindToolUse, "b", "Read"), mk(5, "user", proto.KindToolResult, "b", ""),
		mk(6, "assistant", proto.KindToolUse, "sub", "Agent"), mk(7, "user", proto.KindToolResult, "sub", ""),
		mk(8, "assistant", proto.KindToolUse, "old", "Task"), mk(9, "user", proto.KindToolResult, "old", ""),
		mk(10, "assistant", proto.KindToolUse, "c", "Bash"), mk(11, "user", proto.KindToolResult, "c", ""),
		mk(12, "assistant", proto.KindThinking, "", ""), mk(13, "assistant", proto.KindToolUse, "d", "Grep"), mk(14, "user", proto.KindToolResult, "d", ""),
	}
	var got []string
	for _, it := range FoldWork(page) {
		if it.Kind == proto.KindFold {
			got = append(got, fmt.Sprintf("fold%d-%d", it.Seq, it.Fold.ThroughSeq))
		} else {
			got = append(got, fmt.Sprint(it.Seq))
		}
	}
	if want := "1 fold2-5 6 7 8 9 fold10-14"; strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

// A fold counts its run's failed tool results and names its tools, each once in the order first called and at most
// proto.FoldToolNamesMax, so a reader tells the run that went wrong without opening it. A run without tools says so
// with an empty list, never an absent one (absent: an older node, which says neither).
func TestFoldCountsErrorsAndNamesTools(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seq := 0
	var page []proto.Item
	call := func(tool string, failed bool) {
		seq++
		id := fmt.Sprintf("t%d", seq)
		page = append(page,
			proto.Item{ID: fmt.Sprintf("l%d#0", seq), Seq: seq, TS: ts, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: id, ToolName: tool},
			proto.Item{ID: fmt.Sprintf("l%d#1", seq), Seq: seq, TS: ts, Role: "user", Kind: proto.KindToolResult, ToolUseID: id, IsError: failed, Text: "out"})
	}
	call("Bash", true)
	call("Bash", false)
	call("Read", true)
	call("Edit", false)
	for i := range 12 {
		call(fmt.Sprintf("mcp__example__tool_%02d", i), false)
	}
	out := FoldWork(page)
	if len(out) != 1 || out[0].Fold == nil {
		t.Fatalf("not one fold: %+v", out)
	}
	fo := out[0].Fold
	if fo.Errors != 2 || fo.Tools != 16 {
		t.Fatalf("errors %d, tools %d", fo.Errors, fo.Tools)
	}
	if len(fo.ToolNames) != proto.FoldToolNamesMax || strings.Join(fo.ToolNames[:4], ",") != "Bash,Read,Edit,mcp__example__tool_00" {
		t.Fatalf("tool names %v", fo.ToolNames)
	}

	thoughts := []proto.Item{}
	for i := range 3 {
		thoughts = append(thoughts, proto.Item{ID: fmt.Sprintf("th%d", i), Seq: i + 1, TS: ts, Role: "assistant", Kind: proto.KindThinking, Text: "thinking"})
	}
	out = FoldWork(thoughts)
	b, _ := json.Marshal(out[0].Fold)
	if out[0].Fold.Errors != 0 || !strings.Contains(string(b), `"errors":0`) || !strings.Contains(string(b), `"tool_names":[]`) {
		t.Fatalf("a run of thoughts: %s", b)
	}
}

func TestRangeSpanReportsHistoryAndBounds(t *testing.T) {
	path := longCodexRollout(t, 4, 2, 1)
	f := NewFile(path)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	_, _, endSpan := f.Page(0, 1)
	if endSpan.History == "" {
		t.Fatal("expected non-empty file history")
	}
	// Paginated range (limit 2) advances from/to contiguously and keeps history.
	first, more, sp1 := f.RangeSpan(2, endSpan.To, 2)
	if !more || len(first) == 0 || sp1.History != endSpan.History || sp1.From != 1 || sp1.To != first[len(first)-1].Seq {
		t.Fatalf("first page: len=%d more=%v span=%+v", len(first), more, sp1)
	}
	rest, more2, sp2 := f.RangeSpan(sp1.To+1, endSpan.To, 1000)
	if more2 || len(rest) == 0 || sp2.History != endSpan.History || sp2.From != sp1.To || sp2.To != endSpan.To {
		t.Fatalf("rest page: len=%d more=%v span=%+v", len(rest), more2, sp2)
	}
	// Inverted / past-end ranges return an empty bounded span with history preserved.
	empty, moreEmpty, spEmpty := f.RangeSpan(endSpan.To+5, endSpan.To+10, 100)
	if moreEmpty || len(empty) != 0 || spEmpty.History != endSpan.History || spEmpty.From != endSpan.To || spEmpty.To != endSpan.To {
		t.Fatalf("past-end range: len=%d more=%v span=%+v", len(empty), moreEmpty, spEmpty)
	}
}
