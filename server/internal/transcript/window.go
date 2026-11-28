package transcript

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// Opening a big transcript cold (a node that just started, a session not opened for a while) used to parse all of it
// to answer "the last two exchanges": 22 s for 317 MB on a busy machine, past the hub's timeout. Now a cold open of a
// file over 2×windowBytes counts its lines (fast: no JSON) and parses only a window at the end. Pages and after_seq
// that reach before the window parse the window before it, and so on back to the first line; a full text parses back
// only to the line that names its item (findOlder).
// Session-level Meta (turns, tokens, compactions) needs the whole file: it is worked out when first asked for
// (Meta), outside the file's lock.
//
// The items are exactly the ones a full read gives: what a line turns into depends on earlier lines only through
// the state below, and a window starts with it as it would be — the background-task notifications already
// shown (a repeat is dropped) and the calls they name, the reports subagents handed back before their
// notification, and the last "! cmd" input (its output line becomes the call) are read from the earlier lines that
// mention them, as is ultracode's pending command (its delayed attachment repeats command output), and the request
// whose usage the lines just before put on items (a reply's blocks are lines of their own; item_usage.go), and the
// backgrounded Codex commands still running (their terminal record gives only the result, codexPendingAt).
// Those lines are found by a byte search while counting lines, so a Claude window may start at any line; a
// Codex rollout records an item twice only within its turn, so its windows start at a turn (task_started).

// windowBytes is how much of a big file's end a cold open parses (and how much more each step back parses).
var windowBytes int64 = 8 << 20

// window is what a File keeps while lines at its start are not parsed: where each of those lines starts (and where
// the first parsed one does), and which of them carry state for later lines.
type window struct {
	starts    []int64 // starts[i]: offset of line i+1, for the lines before the first window and its first line
	notif     []notifSeed
	bash      []int // lines mentioning a "! cmd" (input or output)
	ultracode []int // lines mentioning ultracode attachments or local command output
	// background: lines mentioning a backgrounded Codex command (codexPendingAt); none in most rollouts.
	background []int
	codex      bool
	start      int             // the first window's first line: the File's meta parses from there
	ids        map[string]bool // tasks whose earlier notifications the File's meta has seen
}

// notifSeed is a line mentioning a background-task notification, or a subagent handing its report back, and the
// task's id ("" when it could not be read).
type notifSeed struct {
	line int
	id   string
}

var (
	seedNotification = []byte("<task-notification>")
	seedBash         = []byte("<bash-")
	seedUltracode    = []byte("ultra_effort_")
	seedStdout       = []byte("local-command-stdout")
	seedHandback     = []byte(`"senderTaskId":"`)
	seedAgent        = []byte(`"Agent"`)
	seedTask         = []byte(`"Task"`)
	seedAgentID      = []byte(`agentId:`)
	seedAgentRecord  = []byte(`"agentId"`)
	seedPeer         = []byte(`"peer"`)
	seedBackgrounded = []byte(`"backgrounded"`)
	// seedMarkers are what scanLines looks for; seedBackgrounded stays last (it is left out of a Claude transcript).
	seedMarkers = [][]byte{seedNotification, seedBash, seedUltracode, seedStdout, seedHandback, seedAgent, seedTask, seedAgentID,
		seedAgentRecord, seedPeer, seedBackgrounded}
)

// scanLines reads a file once without parsing it: where each complete line starts, which lines mention a seed marker,
// the first line, and where the last complete line ends (what follows is a line still being written). Big blocks and
// byte searches: a few hundred MB in a fraction of a second.
func scanLines(fh *os.File, size int64) (starts []int64, notif []notifSeed, bash, ultracode, background []int, first []byte, lastNL int64, err error) {
	var s scanState
	if err := scanBlocks(fh, &s, 0, size); err != nil {
		return nil, nil, nil, nil, nil, nil, 0, err
	}
	return scanFinish(fh, s)
}

// openWindow reads a big file cold: counts its lines, parses the window at its end. ok is false when no turn start
// is close enough to the end to make a window worth it (the caller reads the file whole). The scan starts from what
// an earlier one of the same file kept (scan_cache.go), except for a strict read. Callers hold f.mu.
func (f *File) openWindow(fh *os.File, st os.FileInfo, strict bool) (fresh []proto.Item, ok bool, err error) {
	size := st.Size()
	scan := scanLinesCached
	if strict {
		scan = func(_ string, fh *os.File, _ os.FileInfo) ([]int64, []notifSeed, []int, []int, []int, []byte, int64, error) {
			return scanLines(fh, size)
		}
	}
	starts, notif, bash, ultracode, background, first, lastNL, err := scan(f.Path, fh, st)
	if err != nil {
		return nil, false, err
	}
	n := len(starts)
	if n == 0 {
		return nil, false, nil
	}
	w := &window{starts: starts, notif: notif, bash: bash, ultracode: ultracode, background: background, codex: isCodexLine(first)}
	// The window starts at the line windowBytes before the end (Codex: the turn starting there or before).
	from := sort.Search(n, func(i int) bool { return starts[i] >= size-windowBytes })
	b := max(from, 1)
	if w.codex {
		b = f.findTurn(fh, w, b, lastNL, size-4*windowBytes)
	}
	if b <= 1 {
		return nil, false, nil
	}
	data := make([]byte, lastNL-starts[b-1])
	if _, err := fh.ReadAt(data, starts[b-1]); err != nil {
		return nil, false, err
	}
	w.start, w.starts = b, starts[:b]
	// The tasks the seed read the earlier lines of, notifications and messages (senderTaskId) alike: a new line naming
	// one of them does not read those lines again (seeNotifications), which would start its agent over as running
	// and then drop the notification that ended it as one already shown.
	w.ids = seedTaskIDs(data)
	meta := f.seeded(fh, w, b, lastNL, w.ids)
	var kept []proto.Item
	f.refs = nil
	f.tree = f.freshTree()
	f.parseLines(data, starts[b-1], b, &meta, f.tree, &kept, &fresh)
	tail := make([]byte, size-lastNL)
	if _, err := fh.ReadAt(tail, lastNL); err != nil && err != io.EOF {
		return nil, false, err
	}
	f.meta, f.items, f.win = meta, kept, w
	f.lines, f.base, f.offset, f.partial = n, b-1, size, tail
	f.firstHash = crc32.ChecksumIEEE(first)
	f.partialMeta = true
	return fresh, true, nil
}

// seeNotifications brings the File's meta up to date before it parses new lines (data): notifications of tasks
// it has not seen yet, from lines before the first window, so a repeat arriving now is dropped as a full read
// drops it. Callers hold f.mu.
func (f *File) seeNotifications(data []byte) {
	w := f.win
	if w == nil || !f.partialMeta {
		return
	}
	fresh := map[string]bool{}
	for id := range seedTaskIDs(data) {
		if !w.ids[id] {
			fresh[id], w.ids[id] = true, true
		}
	}
	if len(fresh) == 0 {
		return
	}
	fh, err := openTranscript(f.Path)
	if err != nil {
		return
	}
	defer fh.Close()
	scratch := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, notified: f.meta.notified, taskCalls: maps.Clone(f.meta.taskCalls),
		handbacks: f.meta.handbacks, agentCalls: maps.Clone(f.meta.agentCalls)}
	last := 0
	for _, s := range w.notif {
		if s.line >= w.start {
			break
		}
		if s.line == last || (s.id != "" && !fresh[s.id]) {
			continue
		}
		last = s.line
		line := make([]byte, w.lineStart(s.line+1, 0)-w.starts[s.line-1])
		if _, err := fh.ReadAt(line, w.starts[s.line-1]); err == nil || err == io.EOF {
			ParseLine(s.line, bytes.TrimRight(line, "\n"), &scratch)
		}
	}
	f.meta.notified, f.meta.handbacks = scratch.notified, scratch.handbacks
	for id := range fresh {
		call := scratch.taskCalls[id]
		if call == "" {
			continue
		}
		if f.meta.taskCalls == nil {
			f.meta.taskCalls = map[string]string{}
		}
		f.meta.taskCalls[id] = call
		if agent, ok := scratch.agentCalls[call]; ok {
			if f.meta.agentCalls == nil {
				f.meta.agentCalls = map[string]agentCall{}
			}
			f.meta.agentCalls[call] = agent
		}
	}
}

// lineStart is where line n (1-based) starts; one past the last line known gives end.
func (w *window) lineStart(n int, end int64) int64 {
	if n-1 < len(w.starts) {
		return w.starts[n-1]
	}
	return end
}

// findTurn finds the last line at or before line `at` that starts a turn, not looking at lines starting before
// floor; 0 when there is none. Lines are read back from `at` a block at a time; end is where line `at` ends at the
// latest (the start of the line after it, or the end of the last line).
func (f *File) findTurn(fh *os.File, w *window, at int, end, floor int64) int {
	hi := at
	for hi >= 1 {
		lo := hi
		for lo > 1 && w.starts[hi-1]-w.starts[lo-2] < windowBytes {
			lo--
		}
		blockEnd := end
		if hi < len(w.starts) {
			blockEnd = w.starts[hi]
		}
		data := make([]byte, blockEnd-w.starts[lo-1])
		if _, err := fh.ReadAt(data, w.starts[lo-1]); err != nil && err != io.EOF {
			return 0
		}
		for n := hi; n >= lo; n-- {
			if w.starts[n-1] < floor {
				return 0
			}
			a := w.starts[n-1] - w.starts[lo-1]
			z := int64(len(data))
			if n < hi {
				z = w.starts[n] - w.starts[lo-1]
			}
			line := bytes.TrimRight(data[a:z], "\n")
			if f.startsTurn(n, line, w.codex) {
				return n
			}
		}
		hi = lo - 1
	}
	return 0
}

// startsTurn says whether a Codex rollout's line starts a task (a turn).
func (f *File) startsTurn(_ int, line []byte, _ bool) bool {
	return bytes.Contains(line, []byte(`"type":"task_started"`))
}

// seeded is the parser state for a window starting at line b naming the tasks ids: what the earlier lines leave
// behind that changes what later lines turn into. Notifications already shown (a repeat is dropped), the calls
// they name and the reports handed back: only the earlier lines for a task the window mentions are read. The pending "! cmd": the last earlier line that is a "! cmd"
// input or output decides it. The request whose usage items already carry: the lines just before (seedUsage).
func (f *File) seeded(fh *os.File, w *window, b int, end int64, ids map[string]bool) Meta {
	readLine := func(n int) []byte {
		line := make([]byte, w.lineStart(n+1, end)-w.starts[n-1])
		if _, err := fh.ReadAt(line, w.starts[n-1]); err != nil && err != io.EOF {
			return nil
		}
		return bytes.TrimRight(line, "\n")
	}
	scratch := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex}
	last := 0
	for _, s := range w.notif {
		if s.line >= b {
			break
		}
		if s.line == last || (s.id != "" && !ids[s.id]) {
			continue
		}
		last = s.line
		if line := readLine(s.line); line != nil {
			ParseLine(s.line, line, &scratch)
		}
	}
	const unset = "\x00"
	bash := ""
	for i := len(w.bash) - 1; i >= 0; i-- {
		if w.bash[i] >= b {
			continue
		}
		probe := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, bashInput: unset}
		if line := readLine(w.bash[i]); line != nil {
			ParseLine(w.bash[i], line, &probe)
		}
		if probe.bashInput != unset {
			bash = probe.bashInput
			break
		}
	}
	ultra := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, UltracodeSince: f.meta.UltracodeSince}
	for _, n := range w.ultracode {
		if n >= b {
			break
		}
		// Request blobs repeat command output and reminders from the entire conversation. Like node's bgScan,
		// skip huge lines before allocating or decoding them, preserving the bounded cold-open window.
		if w.lineStart(n+1, end)-w.starts[n-1] > 64<<10 {
			continue
		}
		if line := readLine(n); line != nil {
			ParseLine(n, line, &ultra)
		}
	}
	size := func(n int) int64 { return w.lineStart(n+1, end) - w.starts[n-1] }
	usage, codexTotal := f.seedUsage(seedLines{line: readLine, size: size,
		head: func(n int, most int64) []byte {
			head := make([]byte, min(size(n), most))
			if _, err := fh.ReadAt(head, w.starts[n-1]); err != nil && err != io.EOF {
				return nil
			}
			return head
		},
		tokenCount: func(before int) int {
			at := w.lineStart(before, end)
			return lastMentionWithin(fh, w.starts, at, max(at-seedCodexSearch, 0), [][]byte{codexTokenCount})
		},
	}, b, w.codex)
	var pending map[string]bool
	var pendingIDs []string
	if w.codex {
		pending, pendingIDs = f.codexPendingAt(fh, w, b, end, readLine)
	}
	// A subagent's own work starts at its first user line (a fork's copied context is its first two lines at most): a
	// later window is past it. A window at the second line is past it when the first line is the subagent's prompt.
	sawUser := b > 2
	if b == 2 && f.meta.Sidechain {
		if line := readLine(1); line != nil {
			probe := Meta{Sidechain: true}
			ParseLine(1, line, &probe)
			sawUser = probe.sawUser
		}
	}
	return Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, notified: scratch.notified, taskCalls: scratch.taskCalls,
		handbacks: scratch.handbacks, agentCalls: scratch.agentCalls, bashInput: bash,
		Ultracode: ultra.Ultracode, UltracodeAt: ultra.UltracodeAt, UltracodeSince: ultra.UltracodeSince, ultracodeCommand: ultra.ultracodeCommand,
		usage: usage, codexTotal: tokenuse.CodexTotalFrom(codexTotal), sawUser: sawUser,
		codexPending: pending, codexPendingIDs: pendingIDs}
}

// codexPendingAt is the backgrounded Codex commands a full read has pending when it reaches line b: started in the
// background (codexCommandLines) with no terminal record yet. A window starting with them gives only the result of
// their terminal record, under the call an earlier window shows (#10817). Only the lines that mention such a command
// are read: the backgrounded records scanLines found, and back from b to the turn of each command's last one, the lines
// naming its id. That last record leaves the command pending unless a terminal record for it is in that turn (before
// it: the repeat is dropped as one already finished; after it: it finished) or in a later turn before b. Rollouts
// with no backgrounded command before b read nothing.
func (f *File) codexPendingAt(fh *os.File, w *window, b int, end int64, readLine func(int) []byte) (map[string]bool, []string) {
	last := map[string]int{} // a command's last backgrounded record before b
	for _, n := range w.background {
		if n >= b {
			break
		}
		line := readLine(n)
		if line == nil {
			continue
		}
		items := ParseLine(n, line, &Meta{Codex: true})
		for _, it := range items {
			if it.Kind == proto.KindToolUse && it.ToolUseID != "" && !slices.ContainsFunc(items, func(r proto.Item) bool {
				return r.Kind == proto.KindToolResult && r.ToolUseID == it.ToolUseID
			}) {
				last[it.ToolUseID] = n
			}
		}
	}
	if len(last) == 0 {
		return nil, nil
	}
	turn := map[string]int{} // the line starting the turn of a command's last backgrounded record
	floor := int64(-1)
	var needles [][]byte
	for id, n := range last {
		t := max(f.findTurn(fh, w, n, w.lineStart(n+1, end), 0), 1)
		turn[id] = t
		if floor < 0 || w.starts[t-1] < floor {
			floor = w.starts[t-1]
		}
		needles = append(needles, quoteID(id))
	}
	finished := map[string]bool{}
	for at := w.starts[b-1]; at > floor && len(finished) < len(last); {
		m := lastMentionWithin(fh, w.starts, at, floor, needles)
		if m == 0 {
			break
		}
		if line := readLine(m); line != nil {
			for _, it := range ParseLine(m, line, &Meta{Codex: true}) {
				if it.Kind == proto.KindToolResult && last[it.ToolUseID] > 0 && m >= turn[it.ToolUseID] {
					finished[it.ToolUseID] = true
				}
			}
		}
		at = w.starts[m-1]
	}
	var ids []string
	for id := range last {
		if !finished[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// oldest first, as a full read queues them; the cap keeps the newest (codexPendingAdd)
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(last[a], last[b]) })
	if len(ids) > codexPendingKept {
		ids = ids[len(ids)-codexPendingKept:]
	}
	pending := make(map[string]bool, len(ids))
	for _, id := range ids {
		pending[id] = true
	}
	return pending, ids
}

// codexTokenCount marks a rollout's token_count line (seedCodexTotal).
var codexTokenCount = []byte(`"type":"token_count"`)

var taskIDRe = lazyre.New(`<task-id>([^<]{1,200})</task-id>`)
var seedRequestTypeRe = lazyre.New(`"type"\s*:\s*"(?:api-request-blob|api-request-shape|tools-used)"`)
var peerTaskIDRe = lazyre.New(`"(?:senderTaskId|from)"\s*:\s*"([^"\\]{1,200})"`)

func seedTaskIDs(data []byte) map[string]bool {
	ids := map[string]bool{}
	for _, re := range []*lazyre.Regexp{taskIDRe, peerTaskIDRe} {
		for _, m := range re.FindAllSubmatch(data, -1) {
			ids[string(m[1])] = true
		}
	}
	return ids
}

// parseLines parses complete lines in data (starting at file offset pos, line number n) into meta: the stored (light)
// items go to kept, the ones to send (light, images kept) to sent when it is not nil. Callers hold f.mu.
func (f *File) parseLines(data []byte, pos int64, n int, meta *Meta, tree *branchTree, kept, sent *[]proto.Item) {
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return
		}
		line, at := data[:i], pos
		data, pos = data[i+1:], pos+int64(i)+1
		if len(bytes.TrimSpace(line)) > 0 {
			items := ParseLine(n, line, meta)
			if trackLine(tree, meta, items, kept, sent) { // on a branch that was gone back over (branch.go)
				n++
				continue
			}
			for _, it := range items {
				if sent != nil {
					s, _ := light(it, true)
					*sent = append(*sent, s)
				}
				k, cut := light(it, false)
				*kept = append(*kept, k)
				if cut || it.SourceFingerprint != "" {
					f.rememberLine(n, lineRef{at, i})
				}
			}
		}
		n++
	}
}

// loadOlder parses the window before the parsed lines; it says how many bytes that was (0: nothing left, or the
// file changed). Callers hold f.mu.
func (f *File) loadOlder() int64 {
	if f.base == 0 || f.win == nil {
		return 0
	}
	fh, err := openTranscript(f.Path)
	if err != nil {
		return 0
	}
	defer fh.Close()
	if st, err := statOpenedTranscript(fh); err != nil || (f.ino != 0 && inode(st) != f.ino) {
		return 0 // replaced: the next Refresh starts over
	}
	w := f.win
	end := w.starts[f.base] // where the parsed lines start
	from := sort.Search(f.base, func(i int) bool { return w.starts[i] >= end-windowBytes })
	b := max(min(from+1, f.base), 1)
	if w.codex {
		if b = f.findTurn(fh, w, b, end, 0); b == 0 {
			b = 1 // the first line starts everything
		}
	}
	data := make([]byte, end-w.starts[b-1])
	if _, err := fh.ReadAt(data, w.starts[b-1]); err != nil && err != io.EOF {
		return 0
	}
	meta := f.seeded(fh, w, b, end, seedTaskIDs(data))
	var older []proto.Item
	pre := &branchTree{}
	f.parseLines(data, w.starts[b-1], b, &meta, pre, &older, nil)
	// A branch can start in the lines before and be abandoned in the lines after: the tree is worked out again over
	// both, and what it now holds abandoned goes out of the items already there and the ones just read. A client that
	// had them sees the history token change.
	if f.tree == nil {
		f.tree = &branchTree{}
	}
	if f.coldInputs != nil {
		older = withoutLines(older, f.tree.hidden)
	} else if len(f.tree.prepend(pre.recs)) > 0 || len(pre.hidden) > 0 {
		older = withoutLines(older, f.tree.hidden)
		f.items = withoutLines(f.items, f.tree.hidden)
	}
	f.items = append(older, f.items...)
	f.base = b - 1
	if f.base == 0 {
		f.coldInputs = nil
	}
	if f.base == 0 && !f.partialMeta {
		f.win = nil
	}
	return int64(len(data))
}

// pageBudget is how much one page may parse back: a page of a long turn (a subagent's transcript is one prompt and
// hundreds of MB of work) comes back shorter, its earlier part on the next page, instead of parsing the whole file.
func pageBudget() int64 { return 3 * windowBytes }

// ensureItems parses back until more than n items come before beforeSeq (0: the end) — one more than a page of n
// needs, so the page knows whether anything is older — or the first line is parsed, or the page's budget is spent.
// Callers hold f.mu.
func (f *File) ensureItems(beforeSeq, n int) {
	for spent := int64(0); f.base > 0 && f.before(beforeSeq) <= n && (spent < pageBudget() || f.before(beforeSeq) == 0); {
		got := f.loadOlder()
		if got == 0 {
			return
		}
		spent += got
	}
}

// ensureTurns parses back until the page PageTurns takes (`turns` exchanges or maxItems items before beforeSeq) has
// an item before it, or the first line is parsed, or the page's budget is spent and it has an item. Callers hold f.mu.
func (f *File) ensureTurns(beforeSeq, turns, maxItems int) {
	for spent := int64(0); f.base > 0; {
		end := f.before(beforeSeq)
		// Past the budget the page comes back short — but never empty while there is more: an empty page has no seq
		// to ask further back from.
		if spent >= pageBudget() && end > 0 {
			return
		}
		start, seen := end, 0
		for start > 0 && seen < turns && end-start < maxItems {
			start--
			if pagePrompt(f.items[start]) {
				seen++
			}
		}
		if start > 0 {
			return
		}
		got := f.loadOlder()
		if got == 0 {
			return
		}
		spent += got
	}
}

// ensureAfter parses back until everything after afterSeq is parsed. Callers hold f.mu.
func (f *File) ensureAfter(afterSeq int) {
	for f.base > afterSeq && f.loadOlder() > 0 {
	}
}

// findOlder finds an item that is not among the parsed ones in the lines not parsed yet, parsing back only to the last
// of them that names its line: the uuid or Codex item id before the id's "#<n>", quoted as JSON writes it (a Codex
// FileChange's "<id>-<n>" is named by "<id>"), or the seq of a "line<seq>" id unless that Claude line has a uuid
// (its items are named by the uuid). A line that names it without holding it (a child's parentUuid, a call's output)
// sends the search further back. An id no line names loads nothing: a stale or mistyped one used to parse the whole
// file under the lock and keep all of it. Callers hold f.mu.
func (f *File) findOlder(id string) (proto.Item, bool, error) {
	if f.base == 0 || f.win == nil || id == "" {
		return proto.Item{}, false, nil
	}
	fh, err := openTranscript(f.Path)
	if err != nil {
		return proto.Item{}, false, err
	}
	defer fh.Close()
	stem := uuidOf(id)
	if rest, ok := strings.CutPrefix(stem, "line"); ok {
		if n, err := strconv.Atoi(rest); err == nil && n > 0 && strconv.Itoa(n) == rest {
			if n <= f.base && !f.win.codex && claudeLineUUID(fh, f.win.starts, n) != "" {
				return proto.Item{}, false, nil // a stale id: a rewind or a replaced file moved its line
			}
			f.ensureAfter(n - 1)
			if n <= f.base {
				return proto.Item{}, false, fmt.Errorf("could not read transcript line %d", n)
			}
			it, found := f.item(id)
			return it, found, nil
		}
	}
	needles := [][]byte{quoteID(stem)}
	if i := strings.LastIndexByte(stem, '-'); i > 0 && i < len(stem)-1 && strings.Trim(stem[i+1:], "0123456789") == "" {
		needles = append(needles, quoteID(stem[:i]))
	}
	for f.base > 0 && f.win != nil {
		line := lastMention(fh, f.win.starts, f.win.starts[f.base], needles)
		if line == 0 {
			return proto.Item{}, false, nil
		}
		base := f.base
		f.ensureAfter(line - 1)
		if it, ok := f.item(id); ok {
			return it, true, nil
		}
		if f.base == base { // the file changed: loadOlder parsed nothing
			return proto.Item{}, false, nil
		}
	}
	return proto.Item{}, false, nil
}

// claudeLineUUID is the uuid of line n (1-based, starts as in window) of a Claude transcript, which names the line's
// items by it; "" when it has none or cannot be read. The caller knows the file is not a Codex rollout, so the line is
// not tested again.
func claudeLineUUID(fh *os.File, starts []int64, n int) string {
	if n < 1 || n >= len(starts) {
		return ""
	}
	data := make([]byte, starts[n]-starts[n-1])
	if _, err := fh.ReadAt(data, starts[n-1]); err != nil && err != io.EOF {
		return ""
	}
	var e struct {
		UUID string `json:"uuid"`
	}
	if unmarshalLine(data, &e) != nil {
		return ""
	}
	return e.UUID
}

// quoteID is an id as a JSON string in a transcript line (ids are plain ASCII; the escaping is for odd ones).
func quoteID(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// lastMention is the line (1-based, starts as in window) holding the last of the needles found in the file's bytes
// before end, read back a block at a time; 0 when none is (or the file cannot be read).
func lastMention(fh *os.File, starts []int64, end int64, needles [][]byte) int {
	return lastMentionWithin(fh, starts, end, 0, needles)
}

// lastMentionWithin is lastMention looking back only to floor.
func lastMentionWithin(fh *os.File, starts []int64, end, floor int64, needles [][]byte) int {
	const block = 4 << 20
	keep := 0 // a needle split across two blocks
	for _, n := range needles {
		keep = max(keep, len(n)-1)
	}
	buf := make([]byte, min(end-floor, block+int64(keep)))
	for hi := end; hi > floor; {
		lo := max(hi-block, floor)
		data := buf[:min(hi+int64(keep), end)-lo]
		if _, err := fh.ReadAt(data, lo); err != nil && err != io.EOF {
			return 0
		}
		at := -1
		for _, n := range needles { // the last one, by forward searches: bytes.Index is much faster than LastIndex
			for i := 0; ; {
				j := bytes.Index(data[i:], n)
				if j < 0 {
					break
				}
				at, i = max(at, i+j), i+j+1
			}
		}
		if at >= 0 {
			off := lo + int64(at)
			return sort.Search(len(starts), func(i int) bool { return starts[i] > off })
		}
		hi = lo
	}
	return 0
}

// fullMeta works out the session-level Meta of a file opened by its end: every line, in order, as a full read
// would. It parses outside the lock (it takes as long as a full read), then catches up with lines read meanwhile.
func (f *File) fullMeta() Meta {
	f.mu.Lock()
	if !f.partialMeta {
		m := f.meta
		f.mu.Unlock()
		return m
	}
	end, lines, gen, path := f.offset-int64(len(f.partial)), f.lines, f.gen, f.Path
	m := Meta{Sidechain: f.meta.Sidechain}
	f.mu.Unlock()
	if parseRange(path, 0, end, 1, &m) != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.meta
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gen != gen || !f.partialMeta {
		return f.meta
	}
	if f.lines > lines {
		if parseRange(f.Path, end, f.offset-int64(len(f.partial)), lines+1, &m) != nil {
			return f.meta
		}
	}
	m.tree = treeLine{} // The last line was parser scratch, including its read buffer.
	f.meta, f.partialMeta = m, false
	if f.base == 0 {
		f.win = nil
	}
	return m
}

// parseRange runs the lines in [from, to) of a file (the first numbered n) through ParseLine for their Meta.
func parseRange(path string, from, to int64, n int, m *Meta) error {
	fh, err := openTranscript(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	r := bufio.NewReaderSize(io.NewSectionReader(fh, from, to-from), 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			if body := line[:len(line)-1]; len(bytes.TrimSpace(body)) > 0 {
				ParseLine(n, body, m)
			}
			n++
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
