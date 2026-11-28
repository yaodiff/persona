package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"weak"

	"github.com/nwverse/persona/server/internal/proto"
)

// Synthetic transcripts for the branch rules (branch.go). The shapes are the ones recorded from Claude Code 2.1.318
// (a /rewind writes the next message as a second child of the line before the one it went back to; the lines of the
// turns gone back over stay), reduced to the fields the reader looks at.

type tline struct{ b strings.Builder }

func (l *tline) add(s string) { l.b.WriteString(s + "\n") }

func ts(n int) string { return fmt.Sprintf("2026-11-05T10:%02d:%02dZ", n/60, n%60) }

func parentJSON(p string) string {
	if p == "" {
		return "null"
	}
	return `"` + p + `"`
}

// user writes one of the user's messages.
func (l *tline) user(uuid, parent, text string, n int) {
	l.add(fmt.Sprintf(`{"type":"user","uuid":%q,"parentUuid":%s,"timestamp":%q,"message":{"role":"user","content":%q}}`, uuid, parentJSON(parent), ts(n), text))
}

func (l *tline) reply(uuid, parent, text string, n int) {
	l.add(fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%s,"timestamp":%q,"message":{"role":"assistant","id":"m_%s","content":[{"type":"text","text":%q}]}}`, uuid, parentJSON(parent), ts(n), uuid, text))
}

func (l *tline) toolUse(uuid, parent, id string, n int) {
	l.add(fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%s,"timestamp":%q,"message":{"role":"assistant","id":"m_%s","content":[{"type":"tool_use","id":%q,"name":"Read","input":{"file_path":"a.txt"}}]}}`, uuid, parentJSON(parent), ts(n), uuid, id))
}

func (l *tline) toolResult(uuid, parent, id, out string, n int) {
	l.add(fmt.Sprintf(`{"type":"user","uuid":%q,"parentUuid":%s,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`, uuid, parentJSON(parent), ts(n), id, out))
}

func (l *tline) turnEnd(uuid, parent string, n int) {
	l.add(fmt.Sprintf(`{"type":"system","subtype":"turn_duration","uuid":%q,"parentUuid":%s,"timestamp":%q,"durationMs":1000}`, uuid, parentJSON(parent), ts(n)))
}

func (l *tline) hook(uuid, parent string, n int) {
	l.add(fmt.Sprintf(`{"type":"attachment","uuid":%q,"parentUuid":%s,"timestamp":%q,"attachment":{"type":"hook_success","hookName":"Stop"}}`, uuid, parentJSON(parent), ts(n)))
}

func writeLines(t *testing.T, l *tline) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(l.b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendLines(t *testing.T, p string, l *tline) {
	t.Helper()
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(l.b.String()); err != nil {
		t.Fatal(err)
	}
}

// texts are the words of a page's items, in order.
func texts(items []proto.Item) []string {
	var out []string
	for _, it := range items {
		if it.Kind == proto.KindText {
			out = append(out, it.Text)
		}
	}
	return out
}

func allItems(f *File) []proto.Item {
	items, _, _ := f.Page(0, 1000)
	return items
}

func want(t *testing.T, got []proto.Item, words ...string) {
	t.Helper()
	if g := texts(got); strings.Join(g, "|") != strings.Join(words, "|") {
		t.Fatalf("items %q, want %q", g, words)
	}
}

// A conversation of three exchanges; the user goes back to the second message and writes another. The turns gone back
// over (second, third) are left out; what comes after the new message is shown.
func rewound() *tline {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "二", 4)
	l.reply("a2", "u2", "答二", 5)
	l.hook("h2", "a2", 6)
	l.turnEnd("t2", "h2", 7)
	l.user("u3", "t2", "三", 8)
	l.reply("a3", "u3", "答三", 9)
	l.turnEnd("t3", "a3", 10)
	// the user went back to the second message (parent: where it hung from) and wrote another
	l.user("u4", "t1", "二改", 11)
	l.reply("a4", "u4", "答二改", 12)
	l.turnEnd("t4", "a4", 13)
	return l
}

func TestAbandonedBranchIsLeftOutOfARead(t *testing.T) {
	f := NewFile(writeLines(t, rewound()))
	fresh, span, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want(t, fresh, "一", "答一", "二改", "答二改")
	want(t, allItems(f), "一", "答一", "二改", "答二改")
	if !strings.HasSuffix(span.History, ".r1") {
		t.Fatalf("history %q does not say there is an abandoned branch", span.History)
	}
	// The lines are still numbered as in the file: the abandoned ones are gaps in seq.
	if items := allItems(f); items[len(items)-1].Seq != 12 && items[len(items)-1].Seq != 13 {
		t.Fatalf("seq %d", items[len(items)-1].Seq)
	}
	// Single items too.
	if _, err := f.Full("u2#0"); err == nil {
		t.Fatal("an abandoned item is still there")
	}
}

// File windows parse slices of one read buffer. The cached items and branch topology
// must stay usable after the buffer itself becomes unreachable.
func TestFileReleasesParsedWindow(t *testing.T) {
	f := &File{tree: &branchTree{}}
	buf := []byte(rewound().b.String() + "\n{bad json}\npartial")
	ref := weak.Make(&buf[0])
	f.parseLines(buf, 0, 1, &f.meta, f.tree, &f.items, nil)
	runtime.GC()
	if ref.Value() != nil {
		t.Error("parsed window is still reachable")
	}
	for _, rec := range f.tree.recs {
		if rec.raw != nil {
			t.Fatalf("cached branch record %s holds its raw line", rec.uuid)
		}
	}
	want(t, f.items, "一", "答一", "二改", "答二改")
	if !f.tree.hidden["u2"] {
		t.Fatal("lost the rewound branch")
	}
	runtime.KeepAlive(f)
}

// The usual case for a running node: the conversation is read, shown, and the user goes back later. The batch with the
// new message carries the new history token, which is what tells clients to drop what they have.
func TestGoingBackWhileWatchedChangesTheHistoryToken(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "二", 4)
	l.reply("a2", "u2", "答二", 5)
	l.turnEnd("t2", "a2", 6)
	p := writeLines(t, l)
	f := NewFile(p)
	first, span1, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want(t, first, "一", "答一", "二", "答二")
	if strings.Contains(span1.History, ".r") {
		t.Fatalf("history %q before any branch", span1.History)
	}

	more := &tline{}
	more.user("u3", "t1", "二改", 7)
	more.reply("a3", "u3", "答二改", 8)
	appendLines(t, p, more)
	fresh, span2, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want(t, fresh, "二改", "答二改")
	if span2.History == span1.History || !strings.HasSuffix(span2.History, ".r1") {
		t.Fatalf("history %q → %q", span1.History, span2.History)
	}
	if span2.From != span1.To || span2.To != span2.From+2 {
		t.Fatalf("span %+v after %+v", span2, span1)
	}
	want(t, allItems(f), "一", "答一", "二改", "答二改")

	// A node that starts later and reads the whole file arrives at the same token and the same items.
	g := NewFile(p)
	_, span3, _ := g.RefreshSpan()
	if span3.History != span2.History || !strings.EqualFold(strings.Join(texts(allItems(g)), "|"), strings.Join(texts(allItems(f)), "|")) {
		t.Fatalf("a fresh read: %q, %q", span3.History, texts(allItems(g)))
	}
	// Lines that go on from the new message keep coming.
	more = &tline{}
	more.user("u4", "a3", "接着", 9)
	appendLines(t, p, more)
	fresh, span4, _ := f.RefreshSpan()
	want(t, fresh, "接着")
	if span4.History != span2.History {
		t.Fatalf("history changed with an ordinary line: %q", span4.History)
	}
}

// Going back to the same message twice leaves two abandoned branches; every one of them is out.
func TestThreeMessagesFromOneParent(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.user("u2", "a1", "二", 3)
	l.reply("a2", "u2", "答二", 4)
	l.user("u3", "a1", "二改", 5)
	l.reply("a3", "u3", "答二改", 6)
	l.user("u4", "a1", "二再改", 7)
	l.reply("a4", "u4", "答二再改", 8)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	want(t, allItems(f), "一", "答一", "二再改", "答二再改")
	if !strings.HasSuffix(span.History, ".r2") {
		t.Fatalf("history %q", span.History)
	}
}

// Forks that are not the user going back are everywhere in a transcript: parallel tool results, an attachment beside a
// reply. None of them hides anything, and a message whose parent is one of the user's own (a chat that was branched with
// a message of its own) is not a fork.
func TestOrdinaryForksStay(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "读两个文件", 1)
	l.toolUse("a1", "u1", "tu1", 2)
	l.toolUse("a2", "a1", "tu2", 3) // parallel calls: the second hangs from the first
	l.toolResult("r1", "a1", "tu1", "文件一", 4)
	l.toolResult("r2", "a2", "tu2", "文件二", 5)
	l.hook("h1", "a1", 6) // an attachment beside the results
	l.reply("a3", "r2", "都读了", 7)
	l.user("u2", "a3", "好", 8)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	items := allItems(f)
	var results int
	for _, it := range items {
		if it.Kind == proto.KindToolResult {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("%d tool results, want both", results)
	}
	want(t, items, "读两个文件", "都读了", "好")
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q says a branch was abandoned", span.History)
	}
}

// A compaction starts a new chain (parentUuid null, the old one in logicalParentUuid): what came before it is still the
// conversation and is shown.
func TestCompactionKeepsTheEarlierConversation(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.add(`{"type":"system","subtype":"compact_boundary","uuid":"c1","parentUuid":null,"logicalParentUuid":"a1","timestamp":"2026-11-05T10:00:03Z","compactMetadata":{"trigger":"manual","preTokens":100}}`)
	l.user("s1", "c1", "摘要", 4)
	l.user("u2", "s1", "二", 5)
	l.reply("a2", "u2", "答二", 6)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	got := texts(allItems(f))
	if strings.Join(got, "|") != "一|答一|摘要|二|答二" {
		t.Fatalf("items %q", got)
	}
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q", span.History)
	}
}

// Sidechain lines (a subagent's work, if one is written into the main file) and lines without a uuid (older versions,
// titles, modes) are no part of the tree.
func TestSidechainsAndLinesWithoutUuid(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.add(`{"type":"user","uuid":"s1","parentUuid":"u1","isSidechain":true,"timestamp":"2026-11-05T10:00:02Z","message":{"role":"user","content":"子任务"}}`)
	l.add(`{"type":"user","uuid":"s2","parentUuid":"u1","isSidechain":true,"timestamp":"2026-11-05T10:00:03Z","message":{"role":"user","content":"另一个子任务"}}`)
	l.reply("a1", "u1", "答一", 4)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	want(t, allItems(f), "一", "答一")
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q", span.History)
	}

	// An older Claude Code wrote no uuids: file order is all there is.
	old := &tline{}
	for i, w := range []string{"甲", "乙", "丙"} {
		old.add(fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`, ts(i), w))
	}
	g := NewFile(writeLines(t, old))
	if _, _, err := g.RefreshSpan(); err != nil {
		t.Fatal(err)
	}
	want(t, allItems(g), "甲", "乙", "丙")
	// A subagent's own transcript is all sidechain and never branch-followed.
	sub := &tline{}
	sub.add(`{"type":"user","uuid":"x1","parentUuid":"p","isSidechain":true,"timestamp":"2026-11-05T10:00:01Z","message":{"role":"user","content":"一"}}`)
	sub.add(`{"type":"user","uuid":"x2","parentUuid":"p","isSidechain":true,"timestamp":"2026-11-05T10:00:02Z","message":{"role":"user","content":"二"}}`)
	sf := NewSubagentFile(writeLines(t, sub))
	if _, _, err := sf.RefreshSpan(); err != nil {
		t.Fatal(err)
	}
	want(t, allItems(sf), "一", "二")
}

// A session resumed into a new file starts with lines whose parents are in the earlier file: they are roots here.
func TestParentsInAnotherFile(t *testing.T) {
	l := &tline{}
	l.user("u5", "gone-1", "接着上次", 1)
	l.reply("a5", "u5", "好", 2)
	l.user("u6", "gone-2", "另一个起点", 3)
	f := NewFile(writeLines(t, l))
	f.RefreshSpan()
	want(t, allItems(f), "接着上次", "好", "另一个起点")
}

// A message queued while Claude worked, one a background task woke it with, and a report from another session are not
// the user going back, even with the same parent.
func TestOnlyTheUsersOwnMessagesFork(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.user("u2", "a1", "二", 3)
	l.add(`{"type":"user","uuid":"w1","parentUuid":"a1","timestamp":"2026-11-05T10:00:04Z","message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<summary>done</summary>\n</task-notification>"}}`)
	l.reply("a2", "u2", "答二", 5)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	if strings.Contains(span.History, ".r") {
		t.Fatalf("a notification was taken for going back: %q", span.History)
	}
	got := texts(allItems(f))
	if len(got) < 4 || got[2] != "二" {
		t.Fatalf("items %q", got)
	}
}

// The node tells the file it rewound: nothing is written until the next message, but from then on the message and what
// followed are out, and the token says so. The message that comes next hangs from the same parent and does not count
// a second time.
func TestRewoundByTheNode(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "继续", 4)
	l.reply("a2", "u2", "答二", 5)
	l.turnEnd("t2", "a2", 6)
	l.user("u3", "t2", "继续", 7)
	l.reply("a3", "u3", "答三", 8)
	p := writeLines(t, l)
	f := NewFile(p)
	_, before, _ := f.RefreshSpan()

	// Go back to the second "continue" (the last one), then to the first (the new last one is the remaining one).
	if _, ok := f.Rewound("没有这条", 1); ok {
		t.Fatal("found a message that was never sent")
	}
	if _, ok := f.Rewound("继续", 3); ok {
		t.Fatal("found a third of two")
	}
	span, ok := f.Rewound("继 续", 1)
	if !ok || span.History == before.History || span.From != span.To {
		t.Fatalf("rewound: %v %+v", ok, span)
	}
	want(t, allItems(f), "一", "答一", "继续", "答二")
	if span, ok = f.Rewound("继续", 1); !ok || !strings.HasSuffix(span.History, ".r2") {
		t.Fatalf("second: %v %+v", ok, span)
	}
	want(t, allItems(f), "一", "答一")

	// The next message hangs from where the user went back to.
	more := &tline{}
	more.user("u4", "a1", "新的", 9)
	appendLines(t, p, more)
	fresh, after, _ := f.RefreshSpan()
	want(t, fresh, "新的")
	if after.History != span.History && !strings.HasSuffix(after.History, ".r2") && !strings.HasSuffix(after.History, ".r3") {
		t.Fatalf("history %q", after.History)
	}
	want(t, allItems(f), "一", "答一", "新的")
}

// A big file is read from a window at its end; the lines before it come later. A branch that starts in the lines before
// the window and is abandoned inside it takes its lines in the window with it once they are read; the items match a
// whole read in the end.
func TestBranchesAcrossWindows(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	windowBytes = 4 << 10
	pad := strings.Repeat("x", 3000)
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "二", 4) // the branch that will be abandoned
	l.reply("a2", "u2", "答二", 5)
	for i := range 6 {
		l.add(fmt.Sprintf(`{"type":"file-history-snapshot","messageId":"m%d","snapshot":{"pad":%q}}`, i, pad))
	}
	l.toolUse("a2b", "a2", "tu9", 6) // still on the abandoned branch, inside the window
	l.turnEnd("t2", "a2b", 7)
	for i := range 6 {
		l.add(fmt.Sprintf(`{"type":"file-history-snapshot","messageId":"n%d","snapshot":{"pad":%q}}`, i, pad))
	}
	l.user("u3", "t1", "二改", 8)
	l.reply("a3", "u3", "答二改", 9)
	p := writeLines(t, l)

	windowed := NewFile(p)
	if _, _, err := windowed.RefreshSpan(); err != nil {
		t.Fatal(err)
	}
	if windowed.base == 0 {
		t.Fatal("the file was not read in a window")
	}
	windowedTokenAtOpen := windowed.history()
	// Page back to the start.
	var seq int
	for i := 0; i < 20; i++ {
		page, more, _ := windowed.PageTurns(seq, 1, 1000)
		if len(page) > 0 {
			seq = page[0].Seq
		}
		if !more {
			break
		}
	}
	windowedAll := texts(allItems(windowed))

	windowBytes = 1 << 40
	whole := NewFile(p)
	if _, _, err := whole.RefreshSpan(); err != nil {
		t.Fatal(err)
	}
	wholeAll := texts(allItems(whole))
	if strings.Join(windowedAll, "|") != strings.Join(wholeAll, "|") || strings.Join(wholeAll, "|") != "一|答一|二改|答二改" {
		t.Fatalf("windowed %q, whole %q", windowedAll, wholeAll)
	}
	if windowed.history() != whole.history() {
		t.Fatalf("history after paging back %q (opened with %q), whole read %q", windowed.history(), windowedTokenAtOpen, whole.history())
	}
	m := windowed.Meta()
	if windowed.partialMeta || m.FirstUser != "一" {
		t.Fatalf("full metadata did not reach the first prompt: %q", m.FirstUser)
	}
	if m.tree.raw != nil || windowed.meta.tree.raw != nil {
		t.Fatal("full metadata retained the last raw line")
	}
}

// Truncating or replacing the file starts the tree over.
func TestTreeStartsOverWithTheFile(t *testing.T) {
	p := writeLines(t, rewound())
	f := NewFile(p)
	f.RefreshSpan()
	if f.tree == nil || len(f.tree.roots) == 0 {
		t.Fatal("no branch seen")
	}
	l := &tline{}
	l.user("n1", "", "新文件", 1)
	if err := os.WriteFile(p, []byte(l.b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	_, span, _ := f.RefreshSpan()
	want(t, allItems(f), "新文件")
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q", span.History)
	}
}

// #3036: pressing Esc on a message that has not been answered and sending it again writes the second one as a sibling
// of the first (same parent, usually the same words). Nothing was gone back over: the first one has no reply, there is
// nothing to hide, and the token must not move (a moving token makes every client start over).
func TestResentUnansweredMessageIsNotARewind(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "二", 4) // sent, then cancelled before any reply
	p := writeLines(t, l)
	f := NewFile(p)
	_, span1, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	more := &tline{}
	more.user("u3", "t1", "二", 5) // the same words, sent again
	more.reply("a3", "u3", "答二", 6)
	appendLines(t, p, more)
	fresh, span2, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want(t, fresh, "二", "答二")
	want(t, allItems(f), "一", "答一", "二", "二", "答二")
	if span2.History != span1.History || strings.Contains(span2.History, ".r") {
		t.Fatalf("history %q → %q: an unanswered message resent was taken for going back", span1.History, span2.History)
	}
	// A fresh read of the whole file says the same.
	g := NewFile(p)
	_, span3, _ := g.RefreshSpan()
	want(t, allItems(g), "一", "答一", "二", "二", "答二")
	if span3.History != span2.History {
		t.Fatalf("a fresh read: %q, want %q", span3.History, span2.History)
	}
}

// The same after an interrupted turn: both messages hang from the "[Request interrupted by user]" line (the lead's own
// session had exactly this), and the work before it stays.
func TestResentAfterAnInterruptKeepsTheInterruptedTurn(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "读文件", 1)
	l.toolUse("a1", "u1", "tu1", 2)
	l.user("i1", "a1", "[Request interrupted by user for tool use]", 3)
	l.user("u2", "i1", "换个办法", 4)
	l.user("u3", "i1", "换个办法", 5)
	l.reply("a3", "u3", "好", 6)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	got := texts(allItems(f))
	if n := strings.Count(strings.Join(got, "|"), "换个办法"); n != 2 {
		t.Fatalf("items %q: both sends of the message stay", got)
	}
	var calls int
	for _, it := range allItems(f) {
		if it.Kind == proto.KindToolUse {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("%d tool calls, the interrupted one stays", calls)
	}
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q", span.History)
	}
}

// A message that was answered only by tool calls was still answered: going back over it is a rewind.
func TestRewindOverATurnOfOnlyToolCalls(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "二", 4)
	l.toolUse("a2", "u2", "tu2", 5)
	l.toolResult("r2", "a2", "tu2", "结果", 6)
	l.user("u3", "t1", "二改", 7)
	l.reply("a3", "u3", "答二改", 8)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	want(t, allItems(f), "一", "答一", "二改", "答二改")
	if !strings.HasSuffix(span.History, ".r1") {
		t.Fatalf("history %q", span.History)
	}
}

func (l *tline) apiError(uuid, parent string, n int) {
	l.add(fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%s,"timestamp":%q,"isApiErrorMessage":true,"message":{"role":"assistant","id":"m_%s","model":"<synthetic>","content":[{"type":"text","text":"API Error: the reply ended during reasoning"}]}}`, uuid, parentJSON(parent), ts(n), uuid))
}

func (l *tline) thinking(uuid, parent string, n int) {
	l.add(fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%s,"timestamp":%q,"message":{"role":"assistant","id":"m_%s","content":[{"type":"thinking","thinking":"想一想"}]}}`, uuid, parentJSON(parent), ts(n), uuid))
}

func (l *tline) awaySummary(uuid, parent string, n int) {
	l.add(fmt.Sprintf(`{"type":"system","subtype":"away_summary","uuid":%q,"parentUuid":%s,"timestamp":%q,"content":"刚才在做的事"}`, uuid, parentJSON(parent), ts(n)))
}

// A turn that ended in an API error, then the user goes back over it in the terminal. Claude writes nothing for the
// rewind; the next line it writes hangs from where the user went back to: here a recap after a while away, no message.
func rewoundThenAway() (before, after *tline) {
	before = &tline{}
	before.user("u1", "", "一", 1)
	before.reply("a1", "u1", "答一", 2)
	before.turnEnd("t1", "a1", 3)
	before.user("u2", "t1", "按这个做", 4)
	before.add(`{"type":"user","uuid":"m2","parentUuid":"u2","isMeta":true,"timestamp":"2026-11-05T10:00:05Z","message":{"role":"user","content":[{"type":"text","text":"[Image #1]"}]}}`)
	before.thinking("k2", "m2", 6)
	before.apiError("e2", "k2", 7)
	before.turnEnd("t2", "e2", 8)
	after = &tline{}
	after.awaySummary("s1", "t1", 200)
	return before, after
}

// The system line beside the answered message is what says it was gone back over: the turn is out and the token moves.
func TestASystemLineAtTheRewindPointAbandonsTheAnsweredTurn(t *testing.T) {
	before, after := rewoundThenAway()
	p := writeLines(t, before)
	f := NewFile(p)
	_, span1, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want(t, allItems(f), "一", "答一", "按这个做", "API Error: the reply ended during reasoning")
	appendLines(t, p, after)
	_, span2, err := f.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	want(t, allItems(f), "一", "答一")
	if span2.History == span1.History || !strings.HasSuffix(span2.History, ".r1") {
		t.Fatalf("history %q → %q", span1.History, span2.History)
	}
	// A fresh read of the whole file says the same.
	g := NewFile(p)
	_, span3, _ := g.RefreshSpan()
	want(t, allItems(g), "一", "答一")
	if span3.History != span2.History {
		t.Fatalf("a fresh read: %q, want %q", span3.History, span2.History)
	}
	// The message the user writes next hangs from the same place and does not count a second time.
	more := &tline{}
	more.user("u3", "t1", "换个做法", 201)
	more.reply("a3", "u3", "好", 202)
	appendLines(t, p, more)
	fresh, span4, _ := f.RefreshSpan()
	want(t, fresh, "换个做法", "好")
	if span4.History != span2.History {
		t.Fatalf("history %q → %q", span2.History, span4.History)
	}
	want(t, allItems(f), "一", "答一", "换个做法", "好")
}

// #3036 again, with a system line: a message nothing answered stays, whatever hangs beside it.
func TestASystemLineBesideAnUnansweredMessageHidesNothing(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "二", 4) // Esc before any reply
	l.awaySummary("s1", "t1", 200)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	want(t, allItems(f), "一", "答一", "二")
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q", span.History)
	}
}

// A turn still going (no turn_duration yet) is not one the user went back over: a system line written with the parent
// it had before the message came in leaves it be.
func TestASystemLineBesideATurnStillGoingHidesNothing(t *testing.T) {
	l := &tline{}
	l.user("u1", "", "一", 1)
	l.reply("a1", "u1", "答一", 2)
	l.turnEnd("t1", "a1", 3)
	l.user("u2", "t1", "二", 4)
	l.toolUse("a2", "u2", "tu2", 5)
	l.awaySummary("s1", "t1", 6)
	l.toolResult("r2", "a2", "tu2", "结果", 7)
	l.reply("a3", "r2", "答二", 8)
	f := NewFile(writeLines(t, l))
	_, span, _ := f.RefreshSpan()
	want(t, allItems(f), "一", "答一", "二", "答二")
	if strings.Contains(span.History, ".r") {
		t.Fatalf("history %q", span.History)
	}
}

// The quick read of a file's end (the node's session list) follows the same rule: the agent's last words are the ones
// still in the conversation, not the error of the turn gone back over.
func TestTailLastWordsLeaveOutAnAbandonedTurn(t *testing.T) {
	before, after := rewoundThenAway()
	read := func(l string) Meta {
		var m Meta
		var tail Tail
		for _, line := range strings.Split(strings.TrimSpace(l), "\n") {
			tail.Line(&m, ParseLine(0, []byte(line), &m))
		}
		tail.Settle(&m)
		return m
	}
	if m := read(before.b.String()); m.LastText != "API Error: the reply ended during reasoning" {
		t.Fatalf("before the rewind: %q", m.LastText)
	}
	m := read(before.b.String() + after.b.String())
	if m.LastText != "答一" || !m.LastTextAt.Equal(time.Date(2026, 11, 5, 10, 0, 2, 0, time.UTC)) {
		t.Fatalf("after: %q at %v", m.LastText, m.LastTextAt)
	}
}

// What the reported transcript did next (#4966): the next message hangs from the recap, not from where the user went
// back to, so the two-messages-one-parent rule alone would never have seen the rewind. Read a line at a time and fresh,
// the turn stays out and the token stays where the recap put it.
func TestTheMessageAfterTheRecapHangsFromIt(t *testing.T) {
	before, after := rewoundThenAway()
	p := writeLines(t, before)
	f := NewFile(p)
	if _, _, err := f.RefreshSpan(); err != nil {
		t.Fatal(err)
	}
	appendLines(t, p, after)
	_, span1, _ := f.RefreshSpan()
	more := &tline{}
	more.user("u3", "s1", "换个做法", 201)
	more.reply("a3", "u3", "好", 202)
	more.turnEnd("t3", "a3", 203)
	appendLines(t, p, more)
	_, span2, _ := f.RefreshSpan()
	want(t, allItems(f), "一", "答一", "换个做法", "好")
	if !strings.HasSuffix(span1.History, ".r1") || span2.History != span1.History {
		t.Fatalf("history %q → %q", span1.History, span2.History)
	}
	g := NewFile(p)
	_, span3, _ := g.RefreshSpan()
	want(t, allItems(g), "一", "答一", "换个做法", "好")
	if span3.History != span2.History {
		t.Fatalf("a fresh read: %q, want %q", span3.History, span2.History)
	}
}

// A line the quick read leaves unparsed (Tail.Skipped) is the same in the tree as one parsed whole: an image prompt
// is a message, a big tool result is not, a compaction's summary is not.
func TestTailSkippedLineIsTheSameInTheTree(t *testing.T) {
	big := strings.Repeat("A", 20<<10)
	for _, c := range []struct {
		line   string
		prompt bool
	}{
		{`{"type":"user","uuid":"u","parentUuid":"p","timestamp":"2026-11-05T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"按这个做"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + big + `"}}]}}`, true},
		{`{"type":"user","uuid":"u","parentUuid":"p","timestamp":"2026-11-05T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"` + big + `"}]},"toolUseResult":{"stdout":"` + big + `"}}`, false},
		{`{"type":"user","uuid":"u","parentUuid":"p","isCompactSummary":true,"timestamp":"2026-11-05T10:00:00Z","message":{"role":"user","content":"` + big + `"}}`, false},
	} {
		var whole, skipped Tail
		var wm, sm Meta
		for i, line := range []string{prompt("seed", "go"), replyLine, turnEndLine} {
			whole.Line(&wm, ParseLine(i, []byte(line), &wm))
			skipped.Line(&sm, ParseLine(i, []byte(line), &sm))
		}
		whole.Line(&wm, ParseLine(3, []byte(c.line), &wm))
		skipped.Skipped([]byte(c.line))
		if len(skipped.tree.recs) != 4 {
			t.Fatalf("%.80s…: skipped %+v, want the seed and one record", c.line, skipped.tree.recs)
		}
		got, want := skipped.tree.recs[3], whole.tree.recs[3]
		if got.uuid != want.uuid || got.parent != want.parent || got.prompt != want.prompt || got.reply != want.reply ||
			got.system != want.system || got.turnEnd != want.turnEnd || got.prompt != c.prompt || got.hasOutcome != c.prompt {
			t.Fatalf("%.80s…: skipped %+v, whole %+v; a skipped line must keep only the tree shape, not new outcome evidence", c.line, got, want)
		}
	}
}

func TestTailSkippedLineDoesNotResetOutcomeEvidence(t *testing.T) {
	var whole, skipped Tail
	var wm, sm Meta
	for i, line := range []string{prompt("seed", "go"), replyLine, turnEndLine} {
		whole.Line(&wm, ParseLine(i, []byte(line), &wm))
		skipped.Line(&sm, ParseLine(i, []byte(line), &sm))
	}
	big := strings.Repeat("A", 20<<10)
	line := `{"type":"user","uuid":"big","parentUuid":"p","timestamp":"2026-11-05T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"` + big + `"}]},"toolUseResult":{"stdout":"` + big + `"}}`
	whole.Line(&wm, ParseLine(3, []byte(line), &wm))
	skipped.Skipped([]byte(line))
	recap := `{"type":"system","subtype":"away_summary","uuid":"recap","parentUuid":"p","timestamp":"2026-11-05T10:01:00Z","content":"recap"}`
	whole.Line(&wm, ParseLine(4, []byte(recap), &wm))
	skipped.Line(&sm, ParseLine(4, []byte(recap), &sm))
	whole.Settle(&wm)
	skipped.Settle(&sm)
	if sm.TurnOpen != wm.TurnOpen || sm.TurnClosed != wm.TurnClosed || sm.TurnSucceeded() != wm.TurnSucceeded() ||
		sm.TurnError != wm.TurnError || sm.TurnErrorResumes != wm.TurnErrorResumes {
		t.Fatalf("a visible recap after a skipped line reset outcome evidence: skipped open=%v closed=%v success=%v error=%q resumes=%v; whole open=%v closed=%v success=%v error=%q resumes=%v",
			sm.TurnOpen, sm.TurnClosed, sm.TurnSucceeded(), sm.TurnError, sm.TurnErrorResumes,
			wm.TurnOpen, wm.TurnClosed, wm.TurnSucceeded(), wm.TurnError, wm.TurnErrorResumes)
	}
}
