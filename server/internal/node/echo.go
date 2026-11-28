package node

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Which transcript line is the message an app sent (docs/protocol/protocol.md "Matching pending messages"). The app sends with a
// client_msg_id and shows a pending bubble until the transcript has the message, but Claude Code, Codex and the
// runtimes give that line ids of their own. After a send that reached the pane (or may have), the node expects its
// copy: in the order sent, the first user line written after the transcript's end at the time it was typed whose words
// fit what was typed. Every item of that line (its words, its pictures; the notice a /compact became) goes out with
// the client_msg_id, in pushes and pages alike. The hub keeps what the node expects and found (sent_messages.echo,
// told with session.echo) and hands it back to a node that started again (session.echoes): the link outlives the node
// process, and a message the node was still waiting for is found after the restart too.
//
// A session's items can come from somewhere else than when the message was sent (#8358). A runtime's session reads its
// runtime's own items until its row names a Claude transcript here (a new session before its first turn, a takeover
// answered without one, a list that left it out), then the transcript, with ids and seqs of its own. A record counts
// where it was sent or found (In): one still expected, or found among the runtime's items, is looked for again where
// the items come from now, by its words and its time from the start, since its seq counted elsewhere. A line found in
// the transcript stays the record's: the runtime's items stand in for the transcript only until it is named. Before,
// the id stayed on the runtime's line, and the app's bubble stayed pending or the message showed twice.

// echoRecord is one send the node ties to the transcript: expected while Line is "", found after.
type echoRecord struct {
	SessionID   string               `json:"session_id"`
	ClientMsgID string               `json:"client_msg_id"`
	Source      *proto.MessageSource `json:"source,omitempty"`
	Text        string               `json:"text,omitempty"`    // the start of what was typed (echoTextLen runes)
	Literal     string               `json:"literal,omitempty"` // its raw start when clipping a text-only send left Text empty
	Images      int                  `json:"images,omitempty"`
	SentAt      time.Time            `json:"sent_at"`
	AfterSeq    int                  `json:"after_seq"`          // the session's last item before it was typed (counted in In)
	Line        string               `json:"line,omitempty"`     // the line found: its items' id before "#" (one of In's)
	History     string               `json:"history,omitempty"`  // the transcript version AfterSeq counts in
	Accepted    bool                 `json:"accepted,omitempty"` // acknowledged by the send path, including queued input
	// In is where AfterSeq and Line count: echoInTranscript or echoInRuntime. A record sent after nothing (a start's
	// first message) or handed back by a hub from before #8358 has none: it is looked for and tagged wherever the
	// items come from, as before, and counts where it is found from then on.
	In string `json:"in,omitempty"`
	// Parts are the ids of the items of Line that are this send's when the line holds several sends (Claude Code writes
	// the messages it queued during a turn as one line: echoSpans); none when the whole line is its.
	Parts []string `json:"parts,omitempty"`
	// scanned: lines through this seq were looked at for it and did not fit (a line stays as it was parsed).
	scanned int
}

// Where a session's items come from (echoRecord.In).
const (
	echoInTranscript = "transcript" // the transcript the node reads for it
	echoInRuntime    = "runtime"    // the items its runtime pushes itself: no transcript here, or none yet
)

// movesTo: r is looked for again among items from in, which are not where it counts. It is still expected, or its line
// is one of a runtime's own items, which stood in for the transcript before it was named or written. A line found in
// the transcript stays its line (above), and one sent too long ago to be found again keeps what it has.
func (r *echoRecord) movesTo(in string) bool {
	return r.In != "" && r.In != in && (r.Line == "" || r.In == echoInRuntime) && time.Since(r.SentAt) <= echoWithin
}

// foundIn: r's line is one of the items from in (one with no In: wherever it was found).
func (r *echoRecord) foundIn(in string) bool {
	return r.Line != "" && (r.In == in || r.In == "")
}

const (
	// echoTextLen: what a record keeps of the text; matching looks at no more than echoPrefix of it.
	echoTextLen = 200
	// echoPrefix: a text this long or longer fits a line that starts with the same words (the page cuts long texts,
	// Claude Code turns pictures into chips); a shorter one must be the same.
	echoPrefix = 12
	// echoWithin: a line written later than this after the send is not its copy (Codex writes a message sent mid-turn
	// when the turn ends); the record stops being looked for.
	echoWithin = 2 * time.Hour
	// echoKeep: how long a found line keeps its client_msg_id here. The hub keeps sent_messages for good but hands a
	// restarted node only the last day's echoes (db.Echoes): keep the two in step.
	echoKeep = 24 * time.Hour
	// echoSlack: the transcript's writer runs on this computer; a line stamped a little before the send is still it.
	echoSlack = 5 * time.Second
	// echoPerSession bounds the records of one session.
	echoPerSession = 200
)

type echoLog struct {
	mu   sync.Mutex
	recs map[string][]*echoRecord // session id -> records in the order sent
}

// clip bounds what a record keeps. A text-only send made entirely of markup that echoClip treats as context keeps
// its literal as well: runtimes that preserve that markup still have an exact user line to match. Outer whitespace is
// immaterial to matching, so it does not spend the literal's bound.
func (r *echoRecord) clip() {
	original := r.Text
	if original == "" {
		original = r.Literal // a record restored from session.echoes has already clipped Text
	}
	r.Text = echoClip(r.Text)
	r.Literal = ""
	if r.Images == 0 && r.Text == "" {
		if original = strings.TrimSpace(original); original != "" {
			r.Literal = echoCap(original)
		}
	}
}

// expect records a send to tie; false when it was known already (a record handed back by the hub).
func (l *echoLog) expect(r echoRecord) bool {
	if r.SessionID == "" || r.ClientMsgID == "" {
		return false
	}
	r.clip()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recs == nil {
		l.recs = map[string][]*echoRecord{}
	}
	list := l.recs[r.SessionID]
	for _, o := range list {
		if o.ClientMsgID == r.ClientMsgID {
			if o.Line == "" && r.Line != "" {
				o.Line, o.In, o.Parts = r.Line, r.In, r.Parts
			}
			if o.SentAt.Equal(r.SentAt) {
				o.Accepted = o.Accepted || r.Accepted
			}
			return false
		}
	}
	list = append(list, &r)
	sort.SliceStable(list, func(i, j int) bool { return list[i].SentAt.Before(list[j].SentAt) })
	now := time.Now()
	list = slices.DeleteFunc(list, func(o *echoRecord) bool { return now.Sub(o.SentAt) > echoKeep })
	if len(list) > echoPerSession {
		list = list[len(list)-echoPerSession:]
	}
	l.recs[r.SessionID] = list
	return true
}

// retry replaces the checked attempt with the new send's baseline. A match made since the check wins over retry.
func (l *echoLog) retry(previous, next echoRecord) proto.SessionEchoState {
	next.clip()
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.recs[previous.SessionID]
	for i, r := range list {
		if r.ClientMsgID != previous.ClientMsgID {
			continue
		}
		if r.Line != "" {
			return proto.EchoSeen
		}
		if r.Accepted || !r.SentAt.Equal(previous.SentAt) || time.Since(r.SentAt) > echoWithin || next.History != r.History {
			break
		}
		copy(list[i:], list[i+1:])
		list[len(list)-1] = &next
		return proto.EchoKnownUnseen
	}
	return proto.EchoUnknown
}

func (l *echoLog) accept(id, clientMsgID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.recs[id] {
		if r.ClientMsgID == clientMsgID {
			r.Accepted = true
			return
		}
	}
}

// record is the session's record of clientMsgID as it is now.
func (l *echoLog) record(id, clientMsgID string) (echoRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.recs[id] {
		if r.ClientMsgID == clientMsgID {
			return *r, true
		}
	}
	return echoRecord{}, false
}

// forget drops the record of a send that did not reach the pane.
func (l *echoLog) forget(id, clientMsgID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs[id] = slices.DeleteFunc(l.recs[id], func(r *echoRecord) bool { return r.ClientMsgID == clientMsgID })
}

// waitingFrom is the seq after which the session's records expected among items from in (echoRecord.In) look for
// their lines; false when none is expected.
func (l *echoLog) waitingFrom(id, in string) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	from, waiting := 0, false
	for _, r := range l.recs[id] {
		at := max(r.AfterSeq, r.scanned)
		switch {
		case r.movesTo(in):
			at = 0 // looked for from the start: its seq counted elsewhere
		case r.Line != "" || time.Since(r.SentAt) > echoWithin:
			continue
		}
		if !waiting || at < from {
			from = at
		}
		waiting = true
	}
	return from, waiting
}

// match ties the session's expected records to lines among items (from in, after waitingFrom, in order) and returns
// the ones found now. Records counted elsewhere are looked for here first (movesTo).
func (l *echoLog) match(id, in string, items []proto.Item) []echoRecord {
	lines := echoLines(items)
	l.mu.Lock()
	defer l.mu.Unlock()
	recs := l.recs[id]
	taken := map[string]bool{}
	for _, r := range recs {
		if r.movesTo(in) {
			r.In, r.AfterSeq, r.scanned, r.Line, r.Parts = in, 0, 0, "", nil
		}
		if r.foundIn(in) {
			taken[r.Line] = true
		}
	}
	var found []echoRecord
	// A runtime that tied its own line says so on the item: that is the record's line, and no other's.
	for _, ln := range lines {
		for _, it := range ln.items {
			if it.ClientMsgID == "" {
				continue
			}
			taken[ln.key] = true
			for _, r := range recs {
				if r.ClientMsgID != it.ClientMsgID || r.Line == ln.key {
					continue
				}
				if r.Line != "" && (in != echoInRuntime || !r.foundIn(in) ||
					slices.ContainsFunc(lines, func(other echoLine) bool { return other.key == r.Line })) {
					continue
				}
				r.Line, r.In, r.Parts = ln.key, in, nil
				found = append(found, *r)
			}
		}
	}
	last := 0
	if len(items) > 0 {
		last = items[len(items)-1].Seq
	}
	for i, r := range recs {
		if r.Line != "" || time.Since(r.SentAt) > echoWithin {
			continue
		}
		for _, ln := range lines {
			if taken[ln.key] || !r.mayBe(ln) {
				continue
			}
			// Claude Code writes the messages it queued while a turn ran as one line when the turn ends: each send
			// takes its own items of it, in the order sent (#14338).
			if spans := echoSpans(ln, r, recs[i+1:]); len(spans) > 1 {
				for _, s := range spans {
					s.rec.Line, s.rec.In, s.rec.Parts = ln.key, in, s.ids
					found = append(found, *s.rec)
				}
				taken[ln.key] = true
				break
			}
			if echoFits(r, ln.items) {
				r.Line, r.In, taken[ln.key] = ln.key, in, true
				found = append(found, *r)
				break
			}
		}
		if r.Line == "" {
			// Looked at all of these: only later lines can be its copy. A later record may still take one of them
			// (they are claimed in the order sent), so this only moves its own start.
			r.scanned = max(r.scanned, last)
		}
	}
	return found
}

// tag puts the client_msg_id of records found among items from in on their lines' items (a copy when anything
// changes): on all of a line's items, or on the record's parts of a line several sends share.
func (l *echoLog) tag(id, in string, items []proto.Item) []proto.Item {
	l.mu.Lock()
	type metadata struct {
		id     string
		source *proto.MessageSource
	}
	var byLine, byItem map[string]metadata
	for _, r := range l.recs[id] {
		if !r.foundIn(in) {
			continue
		}
		if byLine == nil {
			byLine, byItem = map[string]metadata{}, map[string]metadata{}
		}
		if len(r.Parts) == 0 {
			byLine[r.Line] = metadata{r.ClientMsgID, r.Source}
			continue
		}
		for _, part := range r.Parts {
			byItem[part] = metadata{r.ClientMsgID, r.Source}
		}
	}
	l.mu.Unlock()
	if byLine == nil {
		return items
	}
	var out []proto.Item
	for i, it := range items {
		r, ok := byItem[it.ID]
		if !ok {
			r, ok = byLine[echoLineKey(it.ID)]
		}
		if !ok {
			continue
		}
		source := it.Source
		if it.Role == "user" && r.source != nil && (source == nil || *source != *r.source) {
			source = r.source
		}
		if it.ClientMsgID == r.id && it.Source == source {
			continue
		}
		if out == nil {
			out = slices.Clone(items)
		}
		out[i].ClientMsgID, out[i].Source = r.id, source
	}
	if out == nil {
		return items
	}
	return out
}

// echoLine is one transcript line's items as the apps see them.
type echoLine struct {
	key   string
	seq   int
	ts    time.Time
	items []proto.Item
}

// echoLineKey: the items of one transcript line share the id before "#" (Claude Code's uuid, Codex's item id).
func echoLineKey(id string) string {
	if i := strings.LastIndexByte(id, '#'); i >= 0 {
		return id[:i]
	}
	return id
}

// echoLines groups items into the lines that can be a message's copy: ones with something the user wrote (not
// another session's message, not a tool's result), or the notice a /compact became.
func echoLines(items []proto.Item) []echoLine {
	var out []echoLine
	at := map[string]int{}
	for _, it := range items {
		k := echoLineKey(it.ID)
		i, ok := at[k]
		if !ok {
			i = len(out)
			at[k] = i
			out = append(out, echoLine{key: k, seq: it.Seq, ts: it.TS})
		}
		out[i].items = append(out[i].items, it)
	}
	return slices.DeleteFunc(out, func(ln echoLine) bool {
		for _, it := range ln.items {
			// Another session's message is not the user's send — except one signed peerSender: the node's own send
			// through a claude's peer inbox (claudepeer.go), which the session writes as a peer's message.
			if it.From != "" && it.From != peerSender || it.Task != nil {
				return true
			}
		}
		for _, it := range ln.items {
			if it.Role == "user" && (it.Kind == proto.KindText || it.Kind == proto.KindImage) ||
				it.Role == "system" && it.Kind == proto.KindNotice && it.Text == transcript.CompactNotice {
				return false
			}
		}
		return true
	})
}

var echoDropped = []*lazyre.Regexp{
	transcript.ContextRe,
	transcript.ViewErrorsRe, // the assistant's view feedback before the message
	lazyre.New(`(?s)<system-reminder>.*?</system-reminder>`), // a hook's context on the user's line
	lazyre.New(`(?s)<attached_images>.*?</attached_images>`),
	lazyre.New(`<[\\/]*pasted_content[^>]*>`),
	boxChips, // [Image #1], [Pasted text #2 …]
}

// echoCap bounds text without changing any of its words.
func echoCap(s string) string {
	if rs := []rune(s); len(rs) > echoTextLen {
		s = string(rs[:echoTextLen])
	}
	return s
}

// echoClip is what a record keeps of a text: without the notes (a cut must not leave half of one), its start.
func echoClip(s string) string {
	s = transcript.StripInterject(s)
	for _, re := range echoDropped {
		s = re.ReplaceAllString(s, "")
	}
	return echoCap(strings.TrimSpace(s))
}

// echoWords is the comparable form of a text: the notes Persona and the agents add removed, no whitespace, without
// the line the page adds where it cut a long one.
func echoWords(s string) []rune {
	s = transcript.StripInterject(s)
	if i := strings.LastIndex(s, "\n…（已截断"); i >= 0 {
		s = s[:i]
	}
	for _, re := range echoDropped {
		s = re.ReplaceAllString(s, "")
	}
	return []rune(strings.Join(strings.Fields(s), ""))
}

// echoRawWords is echoWords without echoDropped: the literal was the user's text, not injected context.
func echoRawWords(s string) []rune {
	if i := strings.LastIndex(s, "\n…（已截断"); i >= 0 {
		s = s[:i]
	}
	return []rune(strings.Join(strings.Fields(s), ""))
}

// echoLiteralFits compares the full raw user text with the usual whitespace normalization. Empty raw text never
// fits. A line with visible words after context clipping must equal the literal: a raw prefix is not an association.
// When the line also clips to empty, a full prefix of a literal that reached the record's bound still fits its uncut
// original.
func echoLiteralFits(literal string, line []proto.Item) bool {
	sent := echoRawWords(literal)
	if len(sent) == 0 {
		return false
	}
	var words, clipped []rune
	for _, it := range line {
		if it.Role == "user" && it.Kind == proto.KindText {
			words = append(words, echoRawWords(it.Text)...)
			clipped = append(clipped, echoWords(it.Text)...)
		}
	}
	if len(words) == 0 {
		return false
	}
	if string(words) == string(sent) {
		return true
	}
	if len(clipped) != 0 {
		return false
	}
	return len([]rune(literal)) == echoTextLen && len(words) >= len(sent) && string(words[:len(sent)]) == string(sent)
}

// isCompactSend reports whether s is a /compact slash command (bare or with instructions), without matching
// unrelated commands that share the prefix (such as /compact-other).
func isCompactSend(s string) bool {
	s = transcript.StripInterject(s)
	for _, re := range echoDropped {
		s = re.ReplaceAllString(s, "")
	}
	f := strings.Fields(s)
	return len(f) > 0 && f[0] == "/compact"
}

func echoWordsMatch(words, sent []rune) bool {
	if len(sent) < echoPrefix {
		return string(words) == string(sent)
	}
	return len(words) >= echoPrefix && string(words[:echoPrefix]) == string(sent[:echoPrefix])
}

// echoFits: the line's words are what was typed — the same, or for a long text the same start. A message of only
// pictures fits a line with none; a bare command fits the command with the arguments picked for it; /compact (with or
// without instructions) fits the notice it became.
func echoFits(r *echoRecord, line []proto.Item) bool {
	if r.Literal != "" {
		return echoLiteralFits(r.Literal, line)
	}
	if inert := inertControls(r.Text); inert != r.Text { // a terminal is pasted the inert text (hostPrompt), a runtime the text
		e := *r
		e.Text = inert
		if echoFits(&e, line) {
			return true
		}
	}
	var words []rune
	user, notice := false, false
	first := "" // the line's first word
	for _, it := range line {
		switch {
		case it.Role == "user" && it.Kind == proto.KindText:
			if f := strings.Fields(it.Text); first == "" && len(f) > 0 {
				first = f[0]
			}
			words, user = append(words, echoWords(it.Text)...), true
		case it.Role == "user" && it.Kind == proto.KindImage:
			user = true
		case it.Role == "system" && it.Kind == proto.KindNotice && it.Text == transcript.CompactNotice:
			notice = true
		}
	}
	sent := echoWords(r.Text)
	if !user {
		return notice && isCompactSend(r.Text)
	}
	if len(sent) == 0 {
		return r.Images > 0 && len(words) == 0
	}
	if bare := strings.TrimSpace(r.Text); strings.HasPrefix(bare, "/") && !strings.ContainsFunc(bare, unicode.IsSpace) && first == bare {
		return true // a command picked its arguments in the terminal (/model opens a picker: "/model opus")
	}
	if echoWordsMatch(words, sent) {
		return true
	}
	// Antigravity prepends "/plan " to plain user prompts submitted while in plan mode.
	if first == "/plan" && !strings.HasPrefix(strings.TrimSpace(r.Text), "/") && strings.HasPrefix(string(words), "/plan") {
		return echoWordsMatch(words[len("/plan"):], sent)
	}
	return false
}

// mayBe: ln was written after r was typed, within the time its copy can come (from echoSlack before the send to
// echoWithin after it).
func (r *echoRecord) mayBe(ln echoLine) bool {
	if ln.seq <= r.AfterSeq || ln.seq <= r.scanned {
		return false
	}
	return ln.ts.IsZero() || !ln.ts.Before(r.SentAt.Add(-echoSlack)) && !ln.ts.After(r.SentAt.Add(echoWithin))
}

const (
	echoSpanSends = 20  // the most sends one line is split among (Claude Code's queue holds fewer)
	echoSpanItems = 200 // a line with more items is not split
)

// echoSpan is one send's part of a line it shares with other sends: the ids of its items.
type echoSpan struct {
	rec *echoRecord
	ids []string
}

// echoPart is what fitting a send to a run of a line's items reads of one item.
type echoPart struct {
	words []rune
	user  bool // the user's words, or a picture
	image bool
	cut   bool // the page cut its text: only its start can be compared
}

// echoSpans splits ln into the copies of several sends: first, then the sends after it in the order sent that are still
// expected and can be in ln. When a turn ends, Claude Code writes the messages it queued meanwhile as one user line,
// each message's blocks one after the other in the order typed (#14338). Each send takes a run of consecutive items whose
// words and pictures are its own (echoPartFits); the split that ties the most sends and covers every item wins, each
// run starting as late as it can (a part with no words between two messages, such as the context Persona puts before
// a message, goes with the message after it). Nil when fewer than two sends fit: the line is one send's, or none's.
func echoSpans(ln echoLine, first *echoRecord, later []*echoRecord) []echoSpan {
	n := len(ln.items)
	if n < 2 || n > echoSpanItems {
		return nil
	}
	recs := []*echoRecord{first}
	for _, r := range later {
		if len(recs) == echoSpanSends {
			break
		}
		if r.Line == "" && time.Since(r.SentAt) <= echoWithin && r.mayBe(ln) {
			recs = append(recs, r)
		}
	}
	if len(recs) < 2 {
		return nil
	}
	parts := make([]echoPart, n)
	for i, it := range ln.items {
		switch {
		case it.Role == "user" && it.Kind == proto.KindText:
			parts[i] = echoPart{words: echoWords(it.Text), user: true, cut: it.FullSize > 0}
		case it.Role == "user" && it.Kind == proto.KindImage:
			parts[i] = echoPart{user: true, image: true}
		}
	}
	sent, whole := make([][]rune, len(recs)), make([]bool, len(recs))
	for k, r := range recs {
		sent[k] = echoWords(r.Text)
		// markup only, a bare command, controls typed inert: echoFits knows how their copies read
		whole[k] = r.Literal != "" || strings.HasPrefix(strings.TrimSpace(r.Text), "/") || inertControls(r.Text) != r.Text
	}
	// start[k][e]: where the k-th send's run starts when the first k sends cover the line's first e items; -1 when
	// they cannot. The earliest start is kept, so that read back from the end each run starts as late as it can.
	start := make([][]int, len(recs)+1)
	for k := range start {
		start[k] = slices.Repeat([]int{-1}, n+1)
	}
	start[0][0] = 0
	most := 0
	for k := 1; k <= len(recs); k++ {
		reached := false
		for p := 0; p < n; p++ {
			if start[k-1][p] < 0 {
				continue
			}
			for e := p + 1; e <= n; e++ {
				if start[k][e] < 0 && echoPartFits(recs[k-1], sent[k-1], whole[k-1], parts[p:e], ln.items[p:e]) {
					start[k][e], reached = p, true
				}
			}
		}
		if !reached {
			break
		}
		if start[k][n] >= 0 {
			most = k
		}
	}
	if most < 2 {
		return nil
	}
	spans := make([]echoSpan, most)
	for k, e := most, n; k > 0; k-- {
		p := start[k][e]
		var ids []string
		for _, it := range ln.items[p:e] {
			if !slices.Contains(ids, it.ID) {
				ids = append(ids, it.ID)
			}
		}
		spans[k-1] = echoSpan{rec: recs[k-1], ids: ids}
		e = p
	}
	return spans
}

// echoPartFits: a run of a line's items (parts, read once; sent: r's words) is r's copy in a line it shares with other
// sends. It has as many pictures as r, and it starts with all of r's words (the next send's words may follow in a run
// that ends later: the split decides), or only with their start when the page cut the text. A short text must be the
// same; a send echoFits must read whole (markup only, a bare command, inert controls) is matched as on a line of its own.
func echoPartFits(r *echoRecord, sent []rune, whole bool, parts []echoPart, items []proto.Item) bool {
	images, user, cut := 0, false, false
	var words []rune
	for _, p := range parts {
		if p.image {
			images++
		}
		user, cut = user || p.user, cut || p.cut
		words = append(words, p.words...)
	}
	if !user || images != r.Images {
		return false
	}
	switch {
	case whole || cut:
		return echoFits(r, items)
	case len(sent) == 0:
		return len(words) == 0
	case len(sent) < echoPrefix:
		return string(words) == string(sent)
	}
	return len(words) >= len(sent) && string(words[:len(sent)]) == string(sent)
}

// echoAfter is where a session's items end before a send: the seq of the last one, and where it counts
// (echoRecord.In). The zero value is after nothing, wherever the items come from.
type echoAfter struct {
	seq     int
	in      string
	history string
}

// echoBase is where the session's transcript ends before a send is typed (what it has so far is read, and pushed to
// a watching hub, first); seq 0 when there is none yet (a session still starting).
func (n *Node) echoBase(id string) echoAfter {
	f, err := n.file(id)
	if err != nil {
		return echoAfter{in: echoInTranscript}
	}
	if err := n.refresh(id, f); err != nil {
		return echoAfter{seq: f.LastSeq(), in: echoInTranscript}
	}
	end := f.End()
	return echoAfter{seq: end.To, in: echoInTranscript, history: end.History}
}

// sendEchoed runs send and records it to tie; once it reached the pane, or may have, the hub is told (it hands the
// record back after a restart), and a send that surely did not is forgotten.
//
// The record is expected before the words are typed: Claude Code writes the line as soon as it takes the message, and
// a watch that pushed it before the send returned sent it without its client_msg_id, never to carry it in a push
// again (the app's bubble then waited for a page).
func (n *Node) sendEchoed(id, clientMsgID, text string, images int, after echoAfter, send func() error) error {
	return n.sendWithEcho(id, clientMsgID, text, images, after, nil, nil, send)
}

func (n *Node) sendWithEcho(id, clientMsgID, text string, images int, after echoAfter, source *proto.MessageSource, retry *echoRecord, send func() error) error {
	if clientMsgID == "" {
		return send()
	}
	if strings.HasPrefix(clientMsgID, "model-retry:") {
		if r, known := n.echoes.record(id, clientMsgID); known && r.Line != "" {
			return nil // a restored transcript receipt proves this continuation arrived
		}
	}
	if retry != nil && source == nil {
		source = retry.Source
	}
	r := echoRecord{SessionID: id, ClientMsgID: clientMsgID, Text: text, Source: source, Images: images, SentAt: time.Now(), AfterSeq: after.seq, In: after.in, History: after.history}
	fresh := true
	if retry == nil {
		fresh = n.echoes.expect(r)
	} else {
		switch n.echoes.retry(*retry, r) {
		case proto.EchoSeen:
			return nil
		case proto.EchoKnownUnseen:
		default:
			return echoUnconfirmed()
		}
	}
	err := send()
	if err == nil {
		n.echoes.accept(id, clientMsgID)
	}
	if err == nil || fresh && maybeDelivered(err) {
		if cur, ok := n.echoes.record(id, clientMsgID); ok {
			n.notifyEcho(cur) // found already (a push ran meanwhile): the hub hears the line too
		}
	} else if fresh {
		n.echoes.forget(id, clientMsgID)
	}
	return err
}

// expectFirstLine ties a new conversation's first message to its line once the conversation's id is known (a start
// that did not name it): sent at at, after nothing, the hub told as after a send.
func (n *Node) expectFirstLine(id, clientMsgID, text string, images int, at time.Time) {
	if !n.echoes.expect(echoRecord{SessionID: id, ClientMsgID: clientMsgID, Text: text, Images: images, SentAt: at}) {
		return
	}
	if cur, ok := n.echoes.record(id, clientMsgID); ok {
		n.notifyEcho(cur)
	}
}

func (n *Node) notifyEcho(r echoRecord) {
	if n.peer == nil {
		return
	}
	r.clip()
	_ = n.peer.Notify("session.echo", r)
}

type echoCheck struct {
	state    proto.SessionEchoState
	record   echoRecord
	previous *sendRecord
	after    echoAfter
}

// checkEcho reads the evidence for a send and checks that its RPC attempt is no longer in flight.
func (n *Node) checkEcho(ctx context.Context, id, clientMsgID string) echoCheck {
	check := n.readEcho(ctx, id, clientMsgID)
	if check.state == proto.EchoKnownUnseen {
		previous, canRetry := n.sends.completed(clientMsgID)
		if !canRetry {
			return echoCheck{state: proto.EchoUnknown}
		}
		check.previous = previous
	}
	return check
}

// readEcho requires a live, idle agent and a complete transcript through a turn that ended after the send before
// reporting absence. A queued prompt can be written only when the turn ends.
func (n *Node) readEcho(ctx context.Context, id, clientMsgID string) echoCheck {
	unknown := echoCheck{state: proto.EchoUnknown}
	r, ok := n.echoes.record(id, clientMsgID)
	if !ok || time.Since(r.SentAt) > echoKeep {
		return unknown
	}
	if r.Line != "" {
		return echoCheck{state: proto.EchoSeen}
	}
	if r.In != echoInTranscript || time.Since(r.SentAt) > echoWithin {
		return unknown
	}
	f, err := n.file(id)
	if err != nil || n.refresh(id, f) != nil {
		return unknown
	}
	end := f.End()
	n.echoed(id, f, nil)
	current, ok := n.echoes.record(id, clientMsgID)
	if !ok || !current.SentAt.Equal(r.SentAt) {
		return unknown
	}
	if current.Line != "" {
		return echoCheck{state: proto.EchoSeen}
	}
	// Runtime and terminal-only sends can take paths that do not write to this transcript. Commands can succeed
	// without a user line, and an unmatchable input cannot prove its own absence.
	if _, _, runtime := n.runtimes.belongs(id); runtime || isCodexWaitID(id) || n.isAssistantSession(id) ||
		strings.HasPrefix(strings.TrimSpace(r.Text), "/") || len(echoWords(r.Text)) == 0 && r.Images == 0 && r.Literal == "" {
		return unknown
	}
	if _, terminalOnly := n.terminalOnly(id); terminalOnly {
		return unknown
	}
	a, live := n.agent(id)
	if !live || a.AgentStatus != proto.StatusIdle || a.Agent == cursorAgent || a.Agent == agyAgent {
		return unknown
	}
	if current.Accepted || ctx.Err() != nil || !f.CoversClosedTurnAfter(end, r.History, r.AfterSeq, r.SentAt) {
		return unknown
	}
	return echoCheck{
		state: proto.EchoKnownUnseen, record: current,
		after: echoAfter{seq: end.To, in: echoInTranscript, history: end.History},
	}
}

// sendUnseen checks the original attempt again after acquiring the input lock. Another send can start a turn while
// this one waits for that lock.
func (n *Node) sendUnseen(ctx context.Context, id, text string, images []proto.Image, opts sendOpts, previous echoRecord) error {
	unlock, err := n.inputTurn(ctx, id)
	if err != nil {
		return echoUnconfirmed()
	}
	defer unlock()
	check := n.readEcho(ctx, id, opts.ClientMsgID)
	if check.state == proto.EchoSeen {
		return nil
	}
	if check.state != proto.EchoKnownUnseen || !check.record.SentAt.Equal(previous.SentAt) {
		return echoUnconfirmed()
	}
	return n.sendWithEcho(id, opts.ClientMsgID, text, len(images), check.after, nil, &check.record, func() error {
		return n.sendLocked(ctx, id, text, images, opts)
	})
}

func echoUnconfirmed() error {
	return &rpc.Error{Code: proto.CodeEchoUnconfirmed, Message: "the node could not confirm that the held message was unseen"}
}

// echoed ties the session's expected sends to its transcript (f; nil when it is not open) and returns items with the
// client_msg_ids of the lines found. A record looked for from the start (movesTo) may have its line past the first
// page of a long transcript: the pages go on to its end, each line looked at once (scanned).
func (n *Node) echoed(id string, f *transcript.File, items []proto.Item) []proto.Item {
	for f != nil {
		from, ok := n.echoes.waitingFrom(id, echoInTranscript)
		if !ok {
			break
		}
		page, more, _ := f.After(from, 2000)
		for _, r := range n.echoes.match(id, echoInTranscript, page) {
			n.notifyEcho(r)
		}
		if !more || len(page) == 0 {
			break
		}
	}
	items = n.echoes.tag(id, echoInTranscript, items)
	if f != nil && n.isAssistantSession(id) {
		items = f.FoldBackgroundPlaceholders(items)
	}
	return items
}

// restoreEchoes takes back the records the hub kept (session.echoes, after the node started).
func (n *Node) restoreEchoes(list []echoRecord) {
	for _, r := range list {
		n.echoes.expect(r)
	}
}

// known: the session has records (found or expected), so its items are worth looking at.
func (l *echoLog) known(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recs[id]) > 0
}

// A runtime's session (runtimes.go) has no transcript here: its items are tied as they pass, a batch (a push or a
// page) at a time, and the transcript's end before a send is its latest item. A runtime that knows the line itself
// may set client_msg_id on its items; that is kept. Before a send the node knows the latest seq only of a session it
// streams (no extra call to the runtime); otherwise the send's time alone says which lines came after it. Once its
// row names a transcript here, its records are looked for there again (movesTo).

// echoedRaw ties and tags the items under field of a runtime's batch; anything that does not read as one passes as
// it came.
func (n *Node) echoedRaw(id string, raw json.RawMessage, field string) json.RawMessage {
	if !n.echoes.known(id) {
		return raw
	}
	var batch map[string]json.RawMessage
	var items []proto.Item
	if json.Unmarshal(raw, &batch) != nil || json.Unmarshal(batch[field], &items) != nil || len(items) == 0 {
		return raw
	}
	for _, r := range n.echoes.match(id, echoInRuntime, items) {
		n.notifyEcho(r)
	}
	tagged := n.echoes.tag(id, echoInRuntime, items)
	if &tagged[0] == &items[0] {
		return raw // nothing changed
	}
	b, err := json.Marshal(tagged)
	if err != nil {
		return raw
	}
	batch[field] = b
	out, err := json.Marshal(batch)
	if err != nil {
		return raw
	}
	return out
}
