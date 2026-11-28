package node

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestAgyPaneTranscriptMapping(t *testing.T) {
	// The JSONL example from issue #10236.
	const issue = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"Show the example file"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:01Z","content":"I will read the example.","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/example/notes.txt"}}]}
{"step_index":2,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"sample content"}
{"step_index":3,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:03Z","content":"The file contains sample content."}
`
	// The ordered-steps fixture from integrations/antigravity/test/map.test.js, serialized as JSONL.
	const companion = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-05T10:00:00Z","content":"<USER_REQUEST>\nCheck files\n</USER_REQUEST>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-05T10:00:01Z","thinking":"Listing files first","content":"I will list the directory.","tool_calls":[{"name":"run_command","args":{"CommandLine":"ls -la","toolSummary":"List files"}}]}
{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-05T10:00:02Z","content":"Created At: 2026-11-05T10:00:02Z\nOutput:\ntotal 0\n"}
`
	const edges = `{"step_index":0,"type":"PLANNER_RESPONSE","created_at":"2026-01-01","tool_calls":[{"name":"replace_file_content","args":{"TargetFile":"/tmp/a.txt","TargetContent":"old","ReplacementContent":"new"}},{"name":"multi_edit","args":{"edits":[]}},{"name":"ls","args":[1,2]},{"name":"invoke_subagent","args":{}}]}
{"step_index":1,"type":"GENERIC","created_at":"not-a-time","content":"edited","media":[{"mime_type":"image/png","uri":"steps/1/image.png"}]}
{"step_index":2,"type":"GENERIC","content":"multi-edited"}
{"step_index":3,"type":"GENERIC","content":"listed"}
{"step_index":4,"type":"GENERIC","content":"delegated"}
{"step_index":5,"type":"ERROR_MESSAGE","error":"quota"}
`
	type mapped struct {
		seq              int
		role, kind, text string
		tool, input      string
		pair             int // 0 for non-tool items
	}
	for _, tc := range []struct {
		name, fixture string
		toSeq         int
		want          []mapped
	}{
		{"issue_fixture", issue, 4, []mapped{
			{seq: 1, role: "user", kind: "text", text: "Show the example file"},
			{seq: 2, role: "assistant", kind: "text", text: "I will read the example."},
			{seq: 2, role: "assistant", kind: "tool_use", tool: "Read", input: `{"file_path":"/example/notes.txt"}`, pair: 1},
			{seq: 3, role: "user", kind: "tool_result", text: "sample content", pair: 1},
			{seq: 4, role: "assistant", kind: "text", text: "The file contains sample content."},
		}},
		{"companion_fixture", companion, 3, []mapped{
			{seq: 1, role: "user", kind: "text", text: "Check files"},
			{seq: 2, role: "assistant", kind: "thinking", text: "Listing files first"},
			{seq: 2, role: "assistant", kind: "text", text: "I will list the directory."},
			{seq: 2, role: "assistant", kind: "tool_use", tool: "Bash", input: `{"command":"ls -la","description":"List files"}`, pair: 1},
			{seq: 3, role: "user", kind: "tool_result", text: "total 0\n", pair: 1},
		}},
		{"edge_cases", edges, 6, []mapped{
			{seq: 1, role: "assistant", kind: "tool_use", tool: "Edit", input: `{"file_path":"/tmp/a.txt","new_string":"new","old_string":"old"}`, pair: 1},
			{seq: 1, role: "assistant", kind: "tool_use", tool: "MultiEdit", input: `{"edits":[]}`, pair: 2},
			{seq: 1, role: "assistant", kind: "tool_use", tool: "LS", input: `[1,2]`, pair: 3},
			{seq: 1, role: "assistant", kind: "tool_use", tool: "Agent", input: `{"description":"subagent","prompt":""}`, pair: 4},
			{seq: 2, role: "user", kind: "tool_result", text: "edited", pair: 1},
			{seq: 2, role: "assistant", kind: "image", pair: 1},
			{seq: 3, role: "user", kind: "tool_result", text: "multi-edited", pair: 2},
			{seq: 4, role: "user", kind: "tool_result", text: "listed", pair: 3},
			{seq: 5, role: "user", kind: "tool_result", text: "delegated", pair: 4},
			{seq: 6, role: "assistant", kind: "text", text: "API Error: quota"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := agyTestFile(t, root, agyTestID, tc.fixture)
			mtime := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			if err := os.Chtimes(file, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			brain, err := agyBrain(root, agyTestID)
			if err != nil {
				t.Fatal(err)
			}
			defer brain.Close()
			var n Node
			history, _, err := n.agyRead(brain, file, agyTestID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if history.seq != tc.toSeq || len(history.items) != len(tc.want) {
				t.Fatalf("to_seq %d, items %+v", history.seq, history.items)
			}
			calls := map[string]int{}
			for i, it := range history.items {
				if it.Kind == "tool_use" {
					if it.ToolUseID == "" || calls[it.ToolUseID] != 0 {
						t.Fatalf("missing or duplicate tool-use ID: %+v", it)
					}
					calls[it.ToolUseID] = tc.want[i].pair
				}
				got := mapped{it.Seq, it.Role, it.Kind, it.Text, it.ToolName, string(it.ToolInput), calls[it.ToolUseID]}
				if got != tc.want[i] {
					t.Errorf("item %d = %+v, want %+v", i, got, tc.want[i])
				}
				if tc.name == "edge_cases" && !it.TS.Equal(mtime) {
					t.Errorf("item %d timestamp = %v, want file mtime %v", i, it.TS, mtime)
				}
			}
		})
	}
}

func TestAgyUserText(t *testing.T) {
	older := compat.ContextNames()[0] // the older context tag name, kept in compat
	for _, tc := range []struct{ input, want string }{
		{"<USER_REQUEST>\n<persona-context>\nsnapshot\n</persona-context>\n\nHello world\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\ntime\n</ADDITIONAL_METADATA>", "Hello world"},
		{"A<persona-context>x</persona-context>\u00a0<" + older + ">y</" + older + ">B", "AB"},
		{"\ufeff</PERSONA-CONTEXT>\v</" + older + "><persona-view-errors id=\"1\">hidden</persona-view-errors>\u2028Keep this\ufeff", "Keep this"},
		{"see <system-reminder>keep me</system-reminder> and <pasted_content id=\"1\">p</pasted_content>", "see <system-reminder>keep me</system-reminder> and <pasted_content id=\"1\">p</pasted_content>"},
		{"before <persona-context unclosed and after", "before <persona-context unclosed and after"},
		{"A<persona-context>x</" + older + ">B", "A<persona-context>x</" + older + ">B"},
		{"</perſona-context>Keep this", "</perſona-context>Keep this"},
	} {
		if got := agyUserText(tc.input); got != tc.want {
			t.Errorf("agyUserText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// Match the companion's projection using a synthetic native first USER_INPUT record.
func TestAgyContextResourceHistory(t *testing.T) {
	const user = "Please reply OK.\nKeep this second line."
	const label = "Content of persona-context:///snapshot.txt:\n"
	const context = "<persona-context>Attached context with /compact</persona-context>"
	for _, tc := range []struct{ name, content, want string }{
		{"native first request", "<USER_REQUEST>\n" + label + context + user + "\n</USER_REQUEST>\n<ADDITIONAL_METADATA>clock</ADDITIONAL_METADATA>", user},
		{"unwrapped resource", label + context + user, user},
		{"another Persona URI", "Content of persona-context:///other.txt:\r\n" + context + user, user},
		{"successive resources", label + context + "\n" + label + context + user, user},
		{"no prefix", user, user},
		{"literal label", label + user, label + user},
		{"other URI", "Content of file:///snapshot.txt:\n" + context + user, "Content of file:///snapshot.txt:\n" + user},
		{"similar scheme", "Content of persona-context-extra:///snapshot.txt:\n" + context + user, "Content of persona-context-extra:///snapshot.txt:\n" + user},
		{"quoted label", "Example:\n" + label + user, "Example:\n" + label + user},
		{"incomplete envelope", label + "<persona-context>unfinished", label + "<persona-context>unfinished"},
		{"leading resource with literal context tags", label + "<persona-context><persona-context>inner</persona-context>outer</persona-context>" + user, user},
		{"leading resource with nested prose", label + "<persona-context>outer\n<persona-context>inner</persona-context>\n\nGenerated prose\n</persona-context>\n" + user, user},
		{"trailing resource", user + "\n\n" + label + context, user},
		{"trailing resource with literal context tags", user + "\n\n" + label + "<persona-context><persona-context>inner</persona-context>outer</persona-context>", user},
		{"trailing resource before user prose", user + "\n\n" + label + "<persona-context>generated</persona-context>\n\nMy example: <persona-context>user</persona-context>", user + "\n\nMy example:"},
		{"resources around user prose", user + "\n\n" + label + context + "\n\nkeep me\n\n" + label + context, user + "\n\nkeep me"},
		{"trailing view errors", user + "\n\n<persona-view-errors>bad</persona-view-errors>", user},
		{"malformed trailing URI", user + "\n\nContent of persona-context:// /snapshot.txt:\n" + context, user + "\n\nContent of persona-context:// /snapshot.txt:"},
		{"quoted trailing label", "Example: " + label + context, "Example: Content of persona-context:///snapshot.txt:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, err := json.Marshal(tc.content)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			file := agyTestFile(t, root, agyTestID, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":`+string(content)+"}\n")
			brain, err := agyBrain(root, agyTestID)
			if err != nil {
				t.Fatal(err)
			}
			defer brain.Close()
			var n Node
			history, _, err := n.agyRead(brain, file, agyTestID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(history.items) != 1 || history.items[0].Role != "user" || history.items[0].Text != tc.want {
				t.Fatalf("history = %+v, want user text %q", history.items, tc.want)
			}
		})
	}
}

// Array tool arguments are bounded like any other tool input (review on #10364).
func TestAgyToolArrayArgsAreClipped(t *testing.T) {
	big := `["` + strings.Repeat("x", 1<<20) + `"]`
	_, input := agyTool("view_file", json.RawMessage(big))
	if len(input) >= len(big) {
		t.Fatalf("array input not clipped: %d bytes", len(input))
	}
	if _, small := agyTool("view_file", json.RawMessage(`[1,2]`)); string(small) != `[1,2]` {
		t.Fatalf("small array changed: %s", small)
	}
}

func TestAgyToolInput(t *testing.T) {
	const path = "/example/file.go"
	const content = "line1\nline2\nline3\n"
	encode := func(value string) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	written := func(path, content string) map[string]string {
		return map[string]string{"TargetFile": path, "CodeContent": content}
	}
	wantWrite := func(path, content string) map[string]string {
		return map[string]string{"file_path": path, "content": content}
	}
	for _, tc := range []struct {
		name, tool, wantTool string
		args, want           map[string]string
		truncated            bool
	}{
		{"encoded write", "write_to_file", "Write", written(encode(path), encode(content)), wantWrite(path, content), false},
		{"plain quoted query", "grep_search", "Grep", map[string]string{"Query": `"exact phrase"`, "SearchPath": "/example"},
			map[string]string{"pattern": `"exact phrase"`, "path": "/example"}, false},
		{"plain JSON file", "write_to_file", "Write", written("/example/file.json", `"hello"`), wantWrite("/example/file.json", `"hello"`), false},
		{"encoded tail cut", "write_to_file", "Write", written(encode(path), "\"line1\\nline2\\nli\n<truncated 41 bytes>"),
			wantWrite(path, "line1\nline2\nli"+transcript.ApproximateClipNote(55)), true},
		{"middle cut", "write_to_file", "Write", written(path, "HEAD-PART\n<truncated 40 bytes>\nTAIL-PART"),
			wantWrite(path, "HEAD-PART"+transcript.ClipNote(58)), true},
		{"encoded middle cut", "write_to_file", "Write", written(encode(path), "\"head\\n\n<truncated 10 bytes>\ntail\""),
			wantWrite(path, "head\n"+transcript.ApproximateClipNote(20)), true},
		{"tail cut after dangling backslash", "write_to_file", "Write", written(encode(path), "\"line1\\n\\\n<truncated 8 bytes>"),
			wantWrite(path, "line1\n"+transcript.ApproximateClipNote(14)), true},
		{"tail cut inside Unicode escape", "write_to_file", "Write", written(encode(path), "\"line1\\n\\u00\n<truncated 8 bytes>"),
			wantWrite(path, "line1\n"+transcript.ApproximateClipNote(14)), true},
		{"tail cut after escaped backslash", "write_to_file", "Write", written(encode(path), "\"line1\\n\\\\\n<truncated 8 bytes>"),
			wantWrite(path, "line1\n\\"+transcript.ApproximateClipNote(15)), true},
		{"read_file with JSON-encoded numeric line bounds", "read_file", "Read", map[string]string{"Path": encode(path), "StartLine": "1", "EndLine": "260"},
			map[string]string{"file_path": path}, false},
		{"run_command with JSON-encoded numeric and boolean args", "run_command", "Bash", map[string]string{"CommandLine": encode("go test ./..."), "WaitMsBeforeAsync": "10000", "IsDaemon": "false", "toolSummary": encode("Run tests")},
			map[string]string{"command": "go test ./...", "description": "Run tests"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			tool, input := agyToolInput(tc.tool, raw, tc.truncated)
			if tool != tc.wantTool || string(input) != string(want) {
				t.Fatalf("tool input = %s %s, want %s %s", tool, input, tc.wantTool, want)
			}
		})
	}
}

// The pane reader follows the companion's rules (integrations/antigravity/lib/map.js): a parallel call that fails at
// once is exported before its sibling's result, and a call that never got a result must not take a later one.
func TestAgyParallelResultsOutOfOrderPairWithTheirOwnCalls(t *testing.T) {
	const early = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-01-01T00:00:00Z","content":"go"}
{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-01-01T00:00:01Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"tail log"}},{"name":"manage_task","args":{"Action":"status"}}]}
{"step_index":3,"type":"GENERIC","status":"INVALID","created_at":"2026-01-01T00:00:02Z","content":"Error invalid tool call","error":"invalid arguments"}
`
	const late = `{"step_index":2,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"tail output"}
{"step_index":4,"type":"PLANNER_RESPONSE","created_at":"2026-01-01T00:00:03Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/tmp/a"}}]}
{"step_index":5,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:04Z","content":"file body"}
`
	steps, before := parseAgySteps([]byte(early), agyTestID)
	if len(steps) != 3 {
		t.Fatalf("early steps = %d", len(steps))
	}
	steps, after := parseAgySteps([]byte(early+late), agyTestID)
	var order []int
	for _, s := range steps {
		order = append(order, s.seq)
	}
	if want := []int{1, 2, 3, 4, 5, 6}; !slices.Equal(order, want) {
		t.Fatalf("step order = %v, want %v: a late step keeps its place and nothing is dropped", order, want)
	}
	if after.branch != before.branch {
		t.Errorf("a late arrival is not a rewind, but the branch went from %q to %q", before.branch, after.branch)
	}
	if after.native == before.native {
		t.Errorf("watchers must learn that earlier items changed: native stayed %q", after.native)
	}
	history := mapAgySteps(steps, time.Time{}, nil)
	results := map[string]string{}
	failed := map[string]bool{}
	for _, it := range history.items {
		if it.Kind == "tool_result" {
			results[it.ToolUseID], failed[it.ToolUseID] = it.Text, it.IsError
		}
	}
	var got []string
	var uses []string
	for _, it := range history.items {
		if it.Kind == "tool_use" {
			uses = append(uses, it.ToolUseID)
			got = append(got, results[it.ToolUseID])
		}
	}
	if want := []string{"tail output", "invalid arguments", "file body"}; !slices.Equal(got, want) {
		t.Fatalf("results by call = %q, want %q", got, want)
	}
	if !failed[uses[1]] {
		t.Error("the invalid call's result is an error")
	}
}

func TestAgyCallWithoutResultDoesNotShiftLaterResults(t *testing.T) {
	step := func(i int, typ, extra string) string {
		return `{"step_index":` + strconv.Itoa(i) + `,"type":"` + typ + `","status":"DONE","created_at":"2026-01-01T00:00:00Z"` + extra + "}\n"
	}
	calls := func(names ...string) string {
		var parts []string
		for _, n := range names {
			parts = append(parts, `{"name":"`+n+`","args":{}}`)
		}
		return `,"tool_calls":[` + strings.Join(parts, ",") + `]`
	}
	fixture := step(0, "USER_INPUT", `,"source":"USER_EXPLICIT","content":"go"`) +
		step(1, "PLANNER_RESPONSE", calls("view_file", "run_command")) +
		step(2, "GENERIC", `,"content":"first"`) + // the second call was interrupted: no result
		step(3, "USER_INPUT", `,"source":"USER_EXPLICIT","content":"again"`) +
		step(4, "PLANNER_RESPONSE", calls("view_file")) +
		step(5, "GENERIC", `,"content":"second"`) +
		step(6, "PLANNER_RESPONSE", calls("view_file", "grep_search")) +
		step(7, "SYSTEM_MESSAGE", `,"content":"a background task finished"`) +
		step(8, "GENERIC", `,"content":"third"`) +
		step(9, "GENERIC", `,"content":"fourth"`)
	steps, _ := parseAgySteps([]byte(fixture), agyTestID)
	history := mapAgySteps(steps, time.Time{}, nil)
	results := map[string]string{}
	for _, it := range history.items {
		if it.Kind == "tool_result" {
			results[it.ToolUseID] = it.Text
		}
	}
	var got []string
	for _, it := range history.items {
		if it.Kind == "tool_use" {
			got = append(got, results[it.ToolUseID])
		}
	}
	if want := []string{"first", "", "second", "third", "fourth"}; !slices.Equal(got, want) {
		t.Fatalf("results by call = %q, want %q", got, want)
	}
}

func TestAgyAssistantTextNormalizesLocalFileLinks(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"unix_file_link", "See [notes.md](file:///work/repo/notes.md).", "See [notes.md](/work/repo/notes.md)."},
		{"line_fragment", "Check [app.go](file:///work/repo/app.go#L10-L20).", "Check [app.go](/work/repo/app.go#L10-L20)."},
		{"localhost", "[doc](file://localhost/work/repo/doc.md)", "[doc](/work/repo/doc.md)"},
		{"uppercase_and_spaces", "[a](FILE:///work/My%20Notes/a.md)", "[a](/work/My%20Notes/a.md)"},
		{"angle_brackets", `[a](<file:///work/My Notes/a.md> "title")`, `[a](</work/My Notes/a.md> "title")`},
		{"balanced_parens", `[p](file:///work/photo(1).png "shot")`, `[p](/work/photo(1).png "shot")`},
		{"image", "![chart](file:///work/repo/chart.png)", "![chart](/work/repo/chart.png)"},
		{"windows_drive", "[win](file:///C:/work/notes.md#L5)", "[win](C:/work/notes.md#L5)"},
		{"windows_encoded_colon", "[win](file:///c%3A/work/notes.md)", "[win](c:/work/notes.md)"},
		{"ref_def", `[ref]: file:///work/repo/notes.txt "Ref"`, `[ref]: /work/repo/notes.txt "Ref"`},
		{"list_continuation", "- item\n    [notes](file:///work/notes.md)", "- item\n    [notes](/work/notes.md)"},
		{"inline_code", "Use `[notes](file:///work/notes.md)` literally.", "Use `[notes](file:///work/notes.md)` literally."},
		{"fenced_code", "```md\n[notes](file:///work/notes.md)\n```\n[live](file:///work/live.md)", "```md\n[notes](file:///work/notes.md)\n```\n[live](/work/live.md)"},
		{"indented_code", "Example:\n\n    [notes](file:///work/notes.md)\n\n[live](file:///work/live.md)", "Example:\n\n    [notes](file:///work/notes.md)\n\n[live](/work/live.md)"},
		{"remote_host", "[r](file://other-host/share/notes.md)", "[r](file://other-host/share/notes.md)"},
		{"unc_double_slash", "[u](file:////server/share/notes.md)", "[u](file:////server/share/notes.md)"},
		{"hub_route", "[api](file:///api/v1/sessions)", "[api](file:///api/v1/sessions)"},
		{"query_string", "[q](file:///work/notes.md?x=1)", "[q](file:///work/notes.md?x=1)"},
		{"nul_byte", "[n](file:///work/notes%00.md)", "[n](file:///work/notes%00.md)"},
		{"relative_file", "[rel](file:notes.md)", "[rel](file:notes.md)"},
		{"escaped_bracket", `text \](file:///work/notes.md)`, `text \](file:///work/notes.md)`},
		{"unclosed_link", "See [notes](file:///work/no", "See [notes](file:///work/no"},
	} {
		if got := agyAssistantText(tc.input); got != tc.want {
			t.Errorf("%s: agyAssistantText(%q) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}

	fixture := `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"Open [u](file:///work/u.md)"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:01Z","thinking":"Check [t](file:///work/t.md)","content":"Opened [notes.md](file:///work/notes.md#L12) and ![img](file:///work/img.png).","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/work/notes.md"}}]}
{"step_index":2,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"[out](file:///work/out.md)"}
`
	steps, _ := parseAgySteps([]byte(fixture), agyTestID)
	history := mapAgySteps(steps, time.Time{}, nil)
	if len(history.items) != 5 {
		t.Fatalf("items = %+v", history.items)
	}
	if history.items[0].Text != "Open [u](file:///work/u.md)" {
		t.Errorf("user text = %q", history.items[0].Text)
	}
	if history.items[1].Text != "Check [t](file:///work/t.md)" {
		t.Errorf("thinking text = %q", history.items[1].Text)
	}
	if history.items[2].Text != "Opened [notes.md](/work/notes.md#L12) and ![img](/work/img.png)." {
		t.Errorf("assistant text = %q", history.items[2].Text)
	}
	if history.items[4].Text != "[out](file:///work/out.md)" {
		t.Errorf("tool output = %q", history.items[4].Text)
	}
}

func TestAgyInterruptedErrorNoteIsDropped(t *testing.T) {
	const afterSystem = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"run task"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:01Z","content":"Working on it."}
{"step_index":2,"type":"SYSTEM_MESSAGE","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"background notice"}
{"step_index":3,"type":"ERROR_MESSAGE","source":"SYSTEM","status":"DONE","created_at":"2026-01-01T00:00:03Z","error":"The stream was interrupted. Please continue the task you were working on."}
`
	steps, _ := parseAgySteps([]byte(afterSystem), agyTestID)
	stopped := mapAgySteps(steps, time.Time{}, nil)
	if stopped.seq != 4 {
		t.Fatalf("stopped seq = %d, want 4", stopped.seq)
	}
	var stoppedTexts []string
	for _, it := range stopped.items {
		if it.Wake {
			t.Fatalf("interrupted stop note must not trigger a wake notice: %+v", it)
		}
		stoppedTexts = append(stoppedTexts, it.Text)
	}
	if want := []string{"run task", "Working on it."}; !slices.Equal(stoppedTexts, want) {
		t.Fatalf("stopped items = %q, want %q", stoppedTexts, want)
	}

	const fixture = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"run task"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:01Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"sleep 10"}}]}
{"step_index":2,"type":"ERROR_MESSAGE","source":"SYSTEM","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"The stream was interrupted."}
{"step_index":3,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:03Z","content":"try again"}
{"step_index":4,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:04Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/tmp/a"}}]}
{"step_index":5,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:05Z","content":"file body"}
{"step_index":6,"type":"ERROR_MESSAGE","source":"SYSTEM","status":"DONE","created_at":"2026-01-01T00:00:06Z","error":"The stream was interrupted: 503 unavailable"}
`
	steps, _ = parseAgySteps([]byte(fixture), agyTestID)
	history := mapAgySteps(steps, time.Time{}, nil)
	if history.seq != 7 {
		t.Fatalf("seq = %d, want 7", history.seq)
	}
	var texts []string
	var uses []string
	results := map[string]string{}
	for _, it := range history.items {
		if it.Kind == "tool_use" {
			uses = append(uses, it.ToolUseID)
		}
		if it.Kind == "tool_result" {
			results[it.ToolUseID] = it.Text
		}
		if it.Kind == "text" {
			texts = append(texts, it.Text)
		}
	}
	wantTexts := []string{"run task", "try again", "API Error: The stream was interrupted: 503 unavailable"}
	if !slices.Equal(texts, wantTexts) {
		t.Fatalf("text items = %q, want %q", texts, wantTexts)
	}
	if len(uses) != 2 || results[uses[0]] != "" || results[uses[1]] != "file body" {
		t.Fatalf("tool call pairing after interrupt = uses:%v results:%v", uses, results)
	}
}

func TestAgyToolOutputStripsTimestampHeadersWithoutAlteringPlainText(t *testing.T) {
	const receipt = `{"level":"triage","summary":"All checks quiet.","visibility":"silent"}`
	for _, tc := range []struct {
		name, input, want string
	}{
		{
			"duplicate_rfc3339_pairs_mcp_json",
			"Created At: 2026-11-23T16:10:40+08:00\nCompleted At: 2026-11-23T16:10:44+08:00\nCreated At: 2026-11-23T16:10:40+08:00\nCompleted At: 2026-11-23T16:10:44+08:00\n" + receipt,
			receipt,
		},
		{
			"single_rfc3339_pair_crlf_fractional",
			"Created At: 2026-11-23T09:10:40.25Z\r\nCompleted At: 2026-11-23T09:10:44.50Z\r\n" + receipt,
			receipt,
		},
		{
			"rfc3339_pair_with_immediate_output_marker",
			"Created At: 2026-11-23T09:10:40Z\nCompleted At: 2026-11-23T09:10:44Z\nOutput:\ntotal 0\n",
			"total 0\n",
		},
		{
			"rfc3339_pair_preserves_later_output_marker_in_body",
			"Created At: 2026-11-23T09:10:40Z\nCompleted At: 2026-11-23T09:10:44Z\nShowing lines 1-3\nOutput:\nkeep me",
			"Showing lines 1-3\nOutput:\nkeep me",
		},
		{
			"legacy_created_at_output",
			"Created At: 2026-11-01\nOutput:\nsample.txt\n",
			"sample.txt\n",
		},
		{
			"plain_text_date_only_untouched",
			"Created At: 2026-11-23\nCompleted At: 2026-11-23\nHello",
			"Created At: 2026-11-23\nCompleted At: 2026-11-23\nHello",
		},
		{
			"plain_text_single_created_at_untouched",
			"Created At: 2026-11-23T16:10:40+08:00\nTitle: Notes",
			"Created At: 2026-11-23T16:10:40+08:00\nTitle: Notes",
		},
		{
			"non_leading_timestamps_untouched",
			"Prefix\nCreated At: 2026-11-23T16:10:40+08:00\nCompleted At: 2026-11-23T16:10:44+08:00\nBody",
			"Prefix\nCreated At: 2026-11-23T16:10:40+08:00\nCompleted At: 2026-11-23T16:10:44+08:00\nBody",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := agyToolOutput(tc.input); got != tc.want {
				t.Fatalf("agyToolOutput(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	const fixture = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-11-23T08:10:39Z","content":"Supervision check"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-11-23T08:10:40Z","tool_calls":[{"name":"mcp_persona_supervision_finish","args":{"visibility":"silent","summary":"All checks quiet."}}]}
{"step_index":2,"type":"GENERIC","source":"MODEL","status":"DONE","created_at":"2026-11-23T08:10:44Z","content":"Created At: 2026-11-23T16:10:40+08:00\nCompleted At: 2026-11-23T16:10:44+08:00\nCreated At: 2026-11-23T16:10:40+08:00\nCompleted At: 2026-11-23T16:10:44+08:00\n{\"level\":\"triage\",\"summary\":\"All checks quiet.\",\"visibility\":\"silent\"}"}
{"step_index":3,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-11-23T08:10:45Z","content":"OK"}
`
	steps, _ := parseAgySteps([]byte(fixture), agyTestID)
	history := mapAgySteps(steps, time.Time{}, nil)
	if len(history.items) != 4 || history.items[2].Kind != "tool_result" || history.items[2].Text != receipt {
		t.Fatalf("mapped items = %+v, want stripped receipt %q", history.items, receipt)
	}
}
