package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A desktop app session whose turn ended on the app's end-of-turn question to the owner, marked blocked, waits on the
// owner: endOfTurnAsk is that question (#16406). One not marked blocked, a turn that ended on another tool or on text
// alone, and one the owner has answered since are not waiting.
func TestEndOfTurnAskIsTheBlockedQuestion(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, lines ...string) string {
		t.Helper()
		p := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const (
		prompt  = `{"type":"user","uuid":"u1","timestamp":"2026-11-26T10:00:00Z","cwd":"/src/app","message":{"role":"user","content":"release the two links?"}}`
		result  = `{"type":"user","uuid":"r1","parentUuid":"a1","timestamp":"2026-11-26T10:00:05Z","cwd":"/src/app","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"shown"}]}}`
		closing = `{"type":"assistant","uuid":"a2","parentUuid":"r1","timestamp":"2026-11-26T10:00:06Z","cwd":"/src/app","message":{"role":"assistant","content":[{"type":"text","text":"The database is backed up."}]}}`
		answer  = `{"type":"user","uuid":"u2","parentUuid":"a2","timestamp":"2026-11-26T10:05:00Z","cwd":"/src/app","message":{"role":"user","content":"yes, release them"}}`
	)
	call := func(input string) string {
		return `{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-11-26T10:00:04Z","cwd":"/src/app","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"` +
			endOfTurnTool + `","input":` + input + `}]}}`
	}
	other := `{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-11-26T10:00:04Z","cwd":"/src/app","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`

	for name, c := range map[string]struct {
		lines []string
		want  string
	}{
		"blocked":     {[]string{prompt, call(`{"recap":"r","ask":"Release the two links now?","blocked":true}`), result, closing}, "Release the two links now?"},
		"not blocked": {[]string{prompt, call(`{"recap":"r","ask":"Want me to go on?"}`), result, closing}, ""},
		"recap only":  {[]string{prompt, call(`{"recap":"r"}`), result, closing}, ""},
		"other tool":  {[]string{prompt, other, `{"type":"user","uuid":"r1","parentUuid":"a1","timestamp":"2026-11-26T10:00:05Z","cwd":"/src/app","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"x"}]}}`, closing}, ""},
		"answered":    {[]string{prompt, call(`{"recap":"r","ask":"Release the two links now?","blocked":true}`), result, closing, answer}, ""},
		"text only":   {[]string{prompt, `{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-11-26T10:00:04Z","cwd":"/src/app","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`}, ""},
	} {
		if got := endOfTurnAsk(write(strings.ReplaceAll(name, " ", "-"), c.lines...)); got != c.want {
			t.Errorf("%s: ask %q, want %q", name, got, c.want)
		}
	}
	// The owner answering after it was read: the transcript grew, so it is read again and no longer waits.
	p := write("grows", prompt, call(`{"recap":"r","ask":"Go?","blocked":true}`), result, closing)
	if got := endOfTurnAsk(p); got != "Go?" {
		t.Fatalf("before the answer: %q", got)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(answer + "\n")
	f.Close()
	if got := endOfTurnAsk(p); got != "" {
		t.Fatalf("after the answer: %q, want none", got)
	}
}
