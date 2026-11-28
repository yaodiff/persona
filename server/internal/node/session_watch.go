package node

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// watch streams new transcript items of a session to the hub, and a working Claude's status line (claudestatus.go).
// The shared pump (watchPump) reads it; a watch still waiting for its transcript (n.waiting) has only cancel.
type watch struct {
	cancel context.CancelFunc

	ctx context.Context // ends with session.unwatch or the hub connection (watchFor)
	// parent is the hub connection's context the watch was made under (watchFor's): a runtime that takes the session
	// over is asked to watch it for as long as that connection lasts (watchTakenOver).
	parent context.Context
	id     string
	f      *transcript.File
	// armed: the watch's first read and catch-up are done, so the pump may read it (the first read of a file is the
	// subscriber's backlog, not news to push).
	armed atomic.Bool
	ticks int // the pump's ticks since the watch was armed; only the pump touches it
	// reading: new lines are being read and pushed in a goroutine of their own; the pump skips the watch meanwhile.
	reading atomic.Bool
	live    liveWatch
	tools   toolWatch
	// A TRAE or Codex transcript that may need a fresh database projection on a follow tick.
	dbHistory bool
	agy       *agyWatch
}

// watchEvery is how often the pump reads the watched transcripts; every followEvery ticks it also follows the copy
// the session list reads (followCopy), and every liveTicks it checks the status line.
var watchEvery = 250 * time.Millisecond

const followEvery = 4

// maxSubagentWatches bounds the subagent transcripts followed at once (a page open on one follows it, a stat on every
// tick of the watch pump and a read when it grew): sessions are few, but every subagent a person opens would be one
// more file to follow for as long as its page stays.
const maxSubagentWatches = 16

// subagentWatches counts the followed subagent transcripts. Callers hold n.mu.
func (n *Node) subagentWatches() int {
	count := 0
	for id := range n.watches {
		if _, _, sub := splitSubagent(id); sub {
			count++
		}
	}
	return count
}

// watch keeps a session's transcript streaming to the hub. Outside of a hub connection (tests that drive the method
// directly) there is nothing to end it but session.unwatch; Serve's calls go through watchFor, so the stream stops
// when its connection does.
func (n *Node) watch(id string, since int) error {
	return n.watchFor(context.Background(), id, since)
}

// watchFor is watch for the hub connection that owns parent: the transcript loop ends with that connection, so a
// resident node that opens a new Node per reconnect is not left with a reader per watch from the one before.
func (n *Node) watchFor(parent context.Context, id string, since int) error {
	_, dbHistory := n.codexSource(id)
	if !dbHistory {
		_, dbHistory = n.traexThread(id)
	}
	f, err := n.file(id)
	if err != nil {
		if !n.knownSession(id) {
			return err
		}
		// A session that just started has no transcript until claude writes its first line: watch once it exists,
		// sending everything written before the watch began (the phone's backlog was empty). The wait is kept in
		// n.waiting, so an unwatch meanwhile ends it and another watch does not start a second one (#5389).
		n.mu.Lock()
		if _, ok := n.watches[id]; ok || n.waiting[id] != nil {
			n.mu.Unlock()
			return nil
		}
		wctx, cancel := context.WithCancel(parent)
		pw := &watch{cancel: cancel, ctx: wctx, id: id, dbHistory: dbHistory}
		if n.waiting == nil {
			n.waiting = map[string]*watch{}
		}
		n.waiting[id] = pw
		n.mu.Unlock()
		go func() {
			defer func() {
				n.mu.Lock()
				if n.waiting[id] == pw {
					delete(n.waiting, id)
				}
				n.mu.Unlock()
			}()
			for i := 0; i < 120 || pw.dbHistory; i++ {
				select {
				case <-wctx.Done(): // unwatched, or the connection ended, before the transcript appeared
					return
				case <-time.After(500 * time.Millisecond):
				}
				if i%2 == 0 {
					n.pumpTools(pw)
				}
				f, err := n.file(id)
				if err != nil {
					continue
				}
				mu := n.pushLock(id)
				mu.Lock()
				if _, err := f.Refresh(); err == nil {
					since = n.catchUp(id, f, since)
				}
				mu.Unlock()
				// Under wctx: an unwatch that comes before this goroutine is done still cancels the watch it installs.
				_ = n.watchFor(wctx, id, since)
				return
			}
			cancel()
		}()
		return nil
	}
	n.mu.Lock()
	if _, ok := n.watches[id]; ok {
		n.mu.Unlock()
		return nil
	}
	if _, _, sub := splitSubagent(id); sub && n.subagentWatches() >= maxSubagentWatches {
		n.mu.Unlock()
		return fmt.Errorf("too many subagent conversations followed (%d): close one first", maxSubagentWatches)
	}
	ctx, cancel := context.WithCancel(parent)
	w := &watch{cancel: cancel, ctx: ctx, parent: parent, id: id, f: f, dbHistory: dbHistory}
	n.watches[id] = w
	if !n.pumping {
		n.pumping = true
		go n.watchPump()
	}
	n.mu.Unlock()
	context.AfterFunc(ctx, func() { n.watchEnded(w) })

	// Catch up on lines appended after the hub's last seen seq, then stream. Without a seq (the hub has not seen this
	// session since it started) the lines this Refresh appends still go out — the watch pump only pushes what
	// later Refreshes find — unless it is the first read of the file: that is the whole history, the subscriber's backlog.
	// The file is read before the answer (a local read), so what is appended after it is news for the pump, not
	// backlog; the pushes go out after the answer, under the push lock taken here. On a slow link a catch-up of
	// thousands of items is many seconds of bytes, and the hub gave up on a watch that answered only after them
	// (#13677). Pushes from a refresh wait for the catch-up behind that lock; a read of a page does not
	// (refreshForPage).
	mu := n.pushLock(id)
	mu.Lock()
	readBefore := f.LastSeq() > 0
	fresh, span, err := f.RefreshSpan()
	catchUp := func() {
		defer w.armed.Store(true)
		defer mu.Unlock()
		if err == nil && ctx.Err() == nil {
			if since > 0 {
				n.catchUp(id, f, since)
			} else if readBefore && span.To > span.From {
				n.pushItems(id, fresh, span)
			}
		}
		n.liveRecheck(id) // a reply Claude is showing now, after what the transcript has (livereply.go)
	}
	if n.peer == nil || parent == context.Background() {
		catchUp() // driven directly (tests, a runtime's own watch): done when it returns
		return nil
	}
	go catchUp()
	return nil
}

// catchUp pushes every item after since, a page at a time (one page used to be all: after 500 the rest was lost), and
// returns the last seq sent. Its images go as placeholders, as every page does: the apps subscribe with lazy_images and
// fetch an image when they show it (…/items/{iid}/image), and putting the data back in made the catch-up of a session
// with screenshots several megabytes that a slow link carried ahead of its heartbeat (#13677).
func (n *Node) catchUp(id string, f *transcript.File, since int) int {
	for {
		items, more, span := f.After(since, 500)
		n.pushItems(id, items, span)
		since = span.To
		if !more {
			return since
		}
	}
}

// pushImageMax is the most image data (base64 bytes) one image of a push carries inline, and pushImagesMax the most
// all the images of one batch of new lines carry together: the others go as placeholders the apps fetch on demand
// (every app subscribes with lazy_images, and the hub strips the data for them anyway), so the screenshot of the line
// just written goes whole and a burst of them does not hold the link. pushBytesMax is about the most one session.items
// push carries; a longer batch goes in several, each with its own span (#13677). Variables for the tests.
var (
	pushImageMax  = 128 << 10
	pushImagesMax = 256 << 10
	pushBytesMax  = 256 << 10
)

func (n *Node) pushItems(id string, items []proto.Item, span transcript.Span) {
	if n.peer == nil {
		return
	}
	n.mu.Lock()
	f := n.files[id]
	w := n.watches[id]
	n.mu.Unlock()
	items = n.applyBotmuxTurnsCached(id, f, items)
	agentUsage(f, items) // finished subagents' results carry what they used (agent_usage.go)
	items = n.echoed(id, f, items)
	n.toolViews.attach(id, items, true, f)
	n.ttft.annotate(id, items, true)
	// On the bulk lane with credit: a push of a page with images otherwise went to the transport at once, and the
	// pong behind it waited for all of it (rpc.Peer.NotifyBulk). Never called from the hub connection's read loop.
	for _, part := range pushParts(pushImages(items), span) {
		_ = n.peer.NotifyBulk(context.Background(), "session.items", withSpan(map[string]any{"session_id": id, "items": nonNil(part.items)}, part.span, true))
	}
	n.refreshWatchedBotmux(w)
	n.liveRecheck(id) // the words these items brought replace their live copy, right after them (livereply.go)
}

// pushImages returns items whose images carry at most pushImageMax bytes of data inline each and pushImagesMax
// together, the newest first; the others become placeholders. items is not changed.
func pushImages(items []proto.Item) []proto.Item {
	var out []proto.Item
	inline := 0
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Image == nil || it.Image.Data == "" {
			continue
		}
		if n := len(it.Image.Data); n <= pushImageMax && inline+n <= pushImagesMax {
			inline += n
			continue
		}
		if out == nil {
			out = append([]proto.Item(nil), items...)
		}
		out[i].Image = it.Image.Placeholder()
	}
	if out == nil {
		return items
	}
	return out
}

type pushPart struct {
	items []proto.Item
	span  transcript.Span
}

// pushParts cuts a batch into pushes of about pushBytesMax each, never between two items of one line (a line's items
// share a seq, and a span ends on a whole line). Each part's span starts where the one before ended; the last ends
// where the batch does. A batch that fits, or has no items, is one part.
func pushParts(items []proto.Item, span transcript.Span) []pushPart {
	var parts []pushPart
	start, size := 0, 0
	from := span.From
	for i := range items {
		size += itemSize(items[i])
		lineEnds := i+1 == len(items) || items[i+1].Seq != items[i].Seq
		if size >= pushBytesMax && lineEnds && i+1 < len(items) && items[i].Seq > from {
			sp := span
			sp.From, sp.To = from, items[i].Seq
			parts = append(parts, pushPart{items: items[start : i+1], span: sp})
			start, size, from = i+1, 0, items[i].Seq
		}
	}
	sp := span
	sp.From = from
	return append(parts, pushPart{items: items[start:], span: sp})
}

// itemSize is about how many bytes an item takes on the wire: its text, its image's data and its tool input, which are
// what make an item large, and a little for the rest.
func itemSize(it proto.Item) int {
	n := 256 + len(it.Text) + len(it.ToolInput)
	if it.Image != nil {
		n += len(it.Image.Data)
	}
	if len(it.ToolViews) > 0 {
		if b, err := json.Marshal(it.ToolViews); err == nil {
			n += len(b)
		}
	}
	return n
}

// pushLock serializes reading a session's new lines with pushing them, so its batches reach the hub in order.
func (n *Node) pushLock(id string) *sync.Mutex {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pushLocks == nil {
		n.pushLocks = map[string]*sync.Mutex{}
	}
	mu := n.pushLocks[id]
	if mu == nil {
		mu = &sync.Mutex{}
		n.pushLocks[id] = mu
	}
	return mu
}

func (n *Node) unwatch(id string) {
	n.mu.Lock()
	w, pw := n.watches[id], n.waiting[id]
	delete(n.watches, id)
	delete(n.waiting, id)
	n.mu.Unlock()
	if w != nil {
		w.cancel()
	}
	if pw != nil {
		pw.cancel()
	}
	n.liveUnwatched(id)
}

// refresh parses new transcript lines and pushes them to the hub when the session is watched — also when the new
// lines hold no items (titles, modes): the batch still tells clients they have everything up to its to_seq, so the
// next one does not look like it skipped something.
func (n *Node) refresh(id string, f *transcript.File) error {
	return n.refreshRead(id, f.RefreshSpan)
}

func (n *Node) refreshExport(id string, f *transcript.File) error {
	return n.refreshRead(id, f.RefreshExportSpan)
}

// refreshForPage is refresh for a read of the session's items, which does not wait for the session's pushes: while a
// watch's catch-up holds the push lock (it goes out after the watch's answer), a page read waited for all of it, on a
// slow link longer than the hub waits for the read (#13677). Then the page is read as the file is: the lines the catch-up
// read are being pushed, and what came after them is the pump's on its next tick.
func (n *Node) refreshForPage(id string, f *transcript.File) error {
	mu := n.pushLock(id)
	if !mu.TryLock() {
		if f.LastSeq() > 0 { // read by whoever holds it: a page of what it read
			return nil
		}
		mu.Lock() // never read: the page needs the read
	}
	defer mu.Unlock()
	return n.refreshLocked(id, f.RefreshSpan)
}

func (n *Node) refreshRead(id string, read func() ([]proto.Item, transcript.Span, error)) error {
	mu := n.pushLock(id)
	mu.Lock()
	defer mu.Unlock()
	return n.refreshLocked(id, read)
}

// refreshLocked is refreshRead with the session's push lock held.
func (n *Node) refreshLocked(id string, read func() ([]proto.Item, transcript.Span, error)) error {
	fresh, span, err := read()
	if err != nil || span.To == span.From {
		return err
	}
	n.mu.Lock()
	_, watched := n.watches[id]
	n.mu.Unlock()
	if watched {
		n.pushItems(id, fresh, span)
	}
	return nil
}

// watchPump reads every watched session on one ticker while any is watched, and ends when none is left. It used to be
// two tickers per watch, each in a goroutine of its own (250 ms for the transcript, 1 s for the status line): four
// watched sessions at rest woke the node twenty times a second, about 65 ms of CPU a minute each. A tick only stats
// each transcript (transcript.File.Changed); reading new lines and reading a screen run in goroutines of their own,
// one at a time per session, so a slow parse or a slow herdr never holds up another session.
func (n *Node) watchPump() {
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	live := liveTicks()
	var ws []*watch
	for range t.C {
		n.mu.Lock()
		if len(n.watches) == 0 {
			n.pumping = false
			n.mu.Unlock()
			return
		}
		for _, w := range n.watches {
			ws = append(ws, w)
		}
		n.mu.Unlock()
		for _, w := range ws {
			n.pumpWatch(w, live)
		}
		clear(ws)
		ws = ws[:0]
	}
}

// pumpWatch is one tick of a watch: new transcript lines four times a second. (The watch used to read the pane's
// screen every 400 ms too, a herdr call per watched session, for a live preview no view showed; the terminal pane reads
// the screen itself.) About once a second it follows the copy the session list reads (followCopy): a stream that only
// refreshed the file it opened kept pushing an old copy while the list's rev went on with the new one (#1423).
func (n *Node) pumpWatch(w *watch, live int) {
	if !w.armed.Load() || w.ctx.Err() != nil { // not caught up yet, or ended (watchEnded drops it)
		return
	}
	if w.agy != nil {
		if w.reading.CompareAndSwap(false, true) {
			go func() {
				defer w.reading.Store(false)
				n.pollAgyPane(w)
			}()
		}
		return
	}
	w.ticks++
	if w.ticks == 1 || w.ticks%followEvery == 0 {
		n.pumpTools(w)
	}
	if w.ticks%live == 0 {
		n.pumpLive(w) // claudestatus.go
	}
	if w.reading.Load() { // still reading: this tick is skipped, as a busy ticker's was
		return
	}
	// The path is cached: a lookup and a stat, a search at most every 30 s. A file being read (Changed says true without
	// waiting for it) is followed in the goroutine that reads it, so the pump never waits for another's parse.
	follow := w.ticks%followEvery == 0
	if follow {
		n.refreshWatchedBotmux(w)
	}
	// TRAE database changes and a Codex rollout disappearing can both require a new projection.
	refreshHistory := follow && w.dbHistory
	if !w.f.Changed() && !refreshHistory {
		if !follow || !n.followCopy(w.id, w.f) {
			return // at rest
		}
		follow = false // moved: what the new path has is read below
	}
	w.reading.Store(true)
	go func() {
		defer w.reading.Store(false)
		if refreshHistory {
			n.refreshCodexAppHistory(w.id)
		}
		if follow {
			n.followCopy(w.id, w.f)
		}
		_ = n.refresh(w.id, w.f)
	}()
}

// watchEnded: the watch's context ended (session.unwatch, or its hub connection closed). Drop the entry so the session
// is not left looking watched: a watch that ended with its connection has nobody to send to, and an unwatch for it
// never comes. Only drop our own: after an unwatch, a new watch may already hold the id, and deleting it would silently
// stop that stream. A status line that was out ends with null (claudestatus.go).
func (n *Node) watchEnded(w *watch) {
	n.mu.Lock()
	if n.watches[w.id] == w {
		delete(n.watches, w.id)
	}
	n.mu.Unlock()
	n.endLive(w)
}
