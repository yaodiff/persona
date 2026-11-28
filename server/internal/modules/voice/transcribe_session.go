package voice

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// One upstream session of a transcription job (docs/assistant/meeting-minutes.md "The built-in adapter"): a caption
// session or a verbatim one, dialed and started like a call's (dialUpstream), with a reader of its own context
// (cancelling a WebSocket's read closes it at once, and a polite close needs the reader until the service answers)
// and a writer that sends it the job's audio from wherever it starts (a reconnect or a rollover replays the job's
// buffer to it) at no more than its speed.

// Session kinds.
const (
	trCaption  = "caption"
	trVerbatim = "verbatim"
)

// trChunkMost is the most audio one append carries: 100 ms.
const trChunkMost = 2 * trRate / 10

// The audio the job keeps, and how a session that is behind gets it (variables so tests can change them).
var (
	// trKeep is how far back a live job keeps its audio: a session opened again (a reconnect, a replacement, a
	// rollover) is given its audio from the first window still to be sent, as far as this goes back.
	trKeep = 60 * time.Second
	// trCatchUp is how much faster than real time a live verbatim session that is behind gets its audio (the final
	// pass runs at four times real time; faster, the service falls behind).
	trCatchUp = 4.0
	// trCaptionJump is how far a live caption session may fall behind the live edge (a burst of audio: the manager's
	// backlog after ready, a resend) before it skips to the edge. It is never given a backlog: captions break faster
	// than real time (59% character error rate measured at four times), and paced at real time it would stay behind
	// for good, its captions too late to be interim ones (trInterimLag). Three seconds, not one: a busy hub's writer
	// that stalls for a second (measured under load in the end-to-end test) catches up instead of leaving a window
	// without captions.
	trCaptionJump = 3 * time.Second
)

// trBuffer is a job's audio by recording position (samples), for its sessions' writers.
type trBuffer struct {
	mu     sync.Mutex
	chunks []trChunk // in order, never overlapping
	keep   int64     // samples kept behind the newest (0: all)
	edge   int64     // the end of the newest audio
	wake   chan struct{}
}

type trChunk struct {
	at  int64
	pcm []byte
}

func newTrBuffer(keep int64) *trBuffer { return &trBuffer{keep: keep, wake: make(chan struct{})} }

// add stores pcm at at, less any part of it the buffer holds already.
func (b *trBuffer) add(at int64, pcm []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if skip := b.edge - at; skip > 0 && len(b.chunks) > 0 {
		if skip*2 >= int64(len(pcm)) {
			return
		}
		pcm, at = pcm[skip*2:], b.edge
	}
	if len(pcm) == 0 {
		return
	}
	b.chunks = append(b.chunks, trChunk{at: at, pcm: pcm})
	b.edge = at + int64(len(pcm)/2)
	if b.keep > 0 {
		b.dropLocked(b.edge - b.keep)
	}
	close(b.wake)
	b.wake = make(chan struct{})
}

// drop forgets the audio before at.
func (b *trBuffer) drop(at int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropLocked(at)
}

func (b *trBuffer) dropLocked(at int64) {
	i := 0
	for i < len(b.chunks) && b.chunks[i].at+int64(len(b.chunks[i].pcm)/2) <= at {
		i++
	}
	if i > 0 {
		b.chunks = append(b.chunks[:0:0], b.chunks[i:]...)
	}
}

// oldest is the first position the buffer holds (its edge when it holds nothing).
func (b *trBuffer) oldest() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.chunks) == 0 {
		return b.edge
	}
	return b.chunks[0].at
}

// read is the audio at or after from, up to most bytes and never past stop (-1: no stop): where it starts and its
// samples. None yet: nil and a channel closed when more comes.
func (b *trBuffer) read(from, stop int64, most int) (int64, []byte, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := sort.Search(len(b.chunks), func(i int) bool { return b.chunks[i].at+int64(len(b.chunks[i].pcm)/2) > from })
	if i == len(b.chunks) {
		return 0, nil, b.wake
	}
	c := b.chunks[i]
	at, pcm := c.at, c.pcm
	if from > at {
		pcm, at = pcm[(from-at)*2:], from
	}
	if stop >= 0 {
		if at >= stop {
			return 0, nil, b.wake
		}
		if end := at + int64(len(pcm)/2); end > stop {
			pcm = pcm[:(stop-at)*2]
		}
	}
	if len(pcm) > most {
		pcm = pcm[:most]
	}
	return at, pcm, nil
}

// trMark is a stretch of what a session was sent: session samples [clock, clock+n) are recording samples [at, at+n),
// or silence the job added at recording position at (silent).
type trMark struct {
	clock, at, n int64
	silent       bool
}

// trSession is one upstream session of a job. The job loop owns the fields below "job loop only"; the writer and the
// reader share the rest through mu and atomics.
type trSession struct {
	j      *trJob
	no     int    // the job's session number, in the log
	kind   string // trCaption or trVerbatim
	slot   *trSlot
	window int // live: the first window it writes for; replay: its window
	why    string

	up      upLink
	rctx    context.Context
	stopR   context.CancelFunc
	began   time.Time
	expires time.Time // when the service ends it (session.started's expires_at; zero: not said)
	feed    int64

	pos           atomic.Int64 // the recording sample after the last one sent
	stop          atomic.Int64 // replay: no audio past this (-1 none); the writer then adds the tail and is done
	limit         atomic.Int64 // replay: no audio past this for now, while stop is not known (-1 none)
	clock         atomic.Int64 // session samples sent, audio and silence
	written       atomic.Bool  // replay: everything is sent, the tail too
	closing       atomic.Bool
	forced        atomic.Bool
	stopWrite     chan struct{} // closed once writing must stop (polite or forced close)
	stopWriteOnce sync.Once
	writerDone    chan struct{} // closed when write() exits (non-nil once run() started)
	gone          chan struct{} // closed once the writer and the socket are done with
	once          sync.Once
	politeOnce    sync.Once
	nowOnce       sync.Once
	poke          chan struct{}

	mu    sync.Mutex
	marks []trMark
	quiet []trQuiet  // silence to send once the audio reached a position
	skips [][2]int64 // live caption session: the recording audio [from, to) it left out to catch up (skip)

	// job loop only
	started  bool
	lost     bool
	last     int // live: the last window it writes for after a rollover (-1: no end)
	retireAt time.Time
	cur      *trReply
	replies  []*trReply
	claimed  int64 // live: the speech its replies have covered so far
	usage    float64
	activity time.Time // its last reply or caption
	passed   map[int]time.Time
	lastAt   int64 // replay: where it was when its last reply began
	// caption sessions
	items  []*trItem // its captions not forgotten yet, in order
	capCur *trItem   // the caption it writes now (nil: none yet, or the last one was final)
}

// trQuiet is n samples of silence to send once the session's audio reached recording position at.
type trQuiet struct{ at, n int64 }

// trReply is one reply of a verbatim session: the sentence (or two) it wrote after a turn.
type trReply struct {
	window int
	at     int64 // where the session's audio was when it began
	text   []byte
	whole  string
	done   bool
}

func (r *trReply) String() string {
	if r.whole != "" {
		return r.whole
	}
	return string(r.text)
}

// trItem is one caption: the service's whole text so far of one sentence (captionOf).
type trItem struct {
	id     string // the service's item_id (on the measured service the same for every caption of a session)
	start  int64  // recording samples (captionStart)
	at     int64  // where the session's audio was when its first frame came
	text   string
	parts  []trPart // the windows its text is of, in order
	closed bool     // final, or written over by the next sentence: no later frame of its item is of it
}

// trPart: a caption's text from rune from on is of window window (up to the next part).
type trPart struct{ window, from int }

func newTrSession(j *trJob, kind string, slot *trSlot, window int, why string) *trSession {
	j.opened++
	s := &trSession{j: j, no: j.opened, kind: kind, slot: slot, window: window, why: why, last: -1,
		stopWrite: make(chan struct{}), gone: make(chan struct{}), poke: make(chan struct{}, 1), passed: map[int]time.Time{}}
	s.stop.Store(-1)
	s.limit.Store(-1)
	return s
}

// rollDue: s is to be rolled over now: trSessionMost after it began, or trExpiryMargin before the service said it
// ends (session.started's expires_at), whichever comes first, never before half of its life.
func (s *trSession) rollDue(now time.Time) bool {
	due := s.began.Add(trSessionMost)
	if !s.expires.IsZero() && s.expires.After(s.began) {
		by := s.expires.Add(-trExpiryMargin)
		if half := s.began.Add(s.expires.Sub(s.began) / 2); by.Before(half) {
			by = half
		}
		if by.Before(due) {
			due = by
		}
	}
	return !now.Before(due)
}

// trStartedHook, when set (tests), runs once a session started, before it is handed to the job loop.
var trStartedHook func()

// trUp is a session's dial outcome, for the job loop.
type trUp struct {
	s        *trSession
	up       upLink
	captions bool
	err      error
}

// connect dials and starts s (in its own goroutine): the outcome goes to the job loop. Every session asks for the
// interim-caption extension (a verbatim session uses none of its captions: with it the service ends a turn at each
// sentence) unless the provider is known not to grant it. A service that refuses it on a verbatim session gets the
// session again without it, and one that refuses the text-only output gets it again without that (its output audio
// is then billed); both are remembered for that provider. A refusal on the caption session ends the job's captions
// (errNoCaptions, sessionUp). What a session asks for is decided as the job opens it: the sessions a job opens
// together ask alike, whichever of them the service answers first.
func (s *trSession) connect(ctx context.Context, instructions string) {
	j := s.j
	caption := s.kind == trCaption
	partial := caption || j.memo().captions.Load() != trNotGranted
	textOnly := !j.memo().noTextOnly.Load()
	go func() {
		began := time.Now()
		sctx, cancel := context.WithTimeout(ctx, startTimeout)
		defer cancel()
		var up upLink
		var started tevent
		var err error
		for {
			up, started, err = j.startOne(sctx, partial, textOnly, instructions)
			var refused *sessionRefused
			switch {
			case err == nil:
			case !caption && partial && errors.Is(err, errNoCaptions):
				j.memo().captions.Store(trNotGranted)
				j.logf("session %d (verbatim): the service refused the interim-caption extension: started again without it", s.no)
				partial = false
				continue
			case textOnly && errors.As(err, &refused) && upstreamCode(err) == "" && refusedField(refused, partial) == "text_only":
				if j.memo().noTextOnly.CompareAndSwap(false, true) {
					j.logf("provider %s: the service refused text-only output (%s): its sessions get output audio, which is billed", j.p.ID,
						firstNonEmpty(refused.code, "no code"))
				}
				textOnly = false
				continue
			}
			break
		}
		granted := started.captions
		if err == nil {
			s.began, s.expires = began, started.expires
			if trStartedHook != nil {
				trStartedHook()
			}
		}
		if j.ctx.Err() != nil { // the job is over: nobody takes the session
			if up != nil {
				_ = up.CloseNow()
			}
			return
		}
		select {
		case j.ups <- trUp{s: s, up: up, captions: granted, err: err}: // unbuffered: taken by the loop
		case <-j.ctx.Done():
			if up != nil {
				_ = up.CloseNow()
			}
		}
	}()
}

// startOne dials the upstream and starts one session on it: its session.started (whether the caption extension was
// granted, when the session ends). partial asks for the caption extension; its refusal is errNoCaptions.
func (j *trJob) startOne(ctx context.Context, partial, textOnly bool, instructions string) (upLink, tevent, error) {
	up, err := dialUpstream(ctx, j.p, liveWire{}, j.model)
	if err != nil {
		return nil, tevent{}, err
	}
	if err := writeJSON(ctx, up, transcribeStart(j.model, instructions, partial, textOnly)); err != nil {
		_ = up.CloseNow()
		return nil, tevent{}, err
	}
	for {
		typ, b, err := up.Read(ctx)
		if err != nil {
			_ = up.CloseNow()
			return nil, tevent{}, fmt.Errorf("the session did not start: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		for _, e := range decodeTranscribe(b) {
			switch e.kind {
			case tevStarted:
				if textOnly && len(e.modalities) > 0 && !(len(e.modalities) == 1 && e.modalities[0] == "text") {
					j.logf("provider %s: the service answers with %v output, not text only: output audio is billed", j.p.ID, e.modalities)
				}
				return up, e, nil
			case tevError:
				_ = up.CloseNow()
				r := &sessionRefused{code: e.code, param: e.param, text: e.text}
				switch {
				case e.code == codeUpstreamUnavailable:
					return nil, tevent{}, &upstreamError{code: codeUpstreamUnavailable, err: r}
				case partial && refusedField(r, partial) == "captions":
					return nil, tevent{}, fmt.Errorf("%w (%s)", errNoCaptions, firstNonEmpty(e.code, "no code"))
				}
				return nil, tevent{}, r
			}
		}
	}
}

// trRead is what a session's reader got: events, or the end of the socket.
type trRead struct {
	s      *trSession
	events []tevent
	pos    int64 // where the session's audio was when they came
	clock  int64
	err    error
}

// run starts s's reader and writer once it started on up.
func (s *trSession) run(up upLink, feed int64) {
	if s.closing.Load() {
		s.end()
		_ = up.CloseNow()
		return
	}
	s.up, s.feed = up, feed
	s.pos.Store(feed)
	s.claimed, s.lastAt = feed, feed
	s.rctx, s.stopR = context.WithCancel(context.Background())
	s.writerDone = make(chan struct{})
	go s.read()
	go s.write()
}

func (s *trSession) read() {
	defer s.end()
	j := s.j
	for {
		typ, b, err := s.up.Read(s.rctx)
		if err != nil {
			s.end()
			select {
			case j.reads <- trRead{s: s, err: err}:
			case <-j.ctx.Done():
				s.close(true)
			}
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		if evs := decodeTranscribe(b); len(evs) > 0 {
			select {
			case j.reads <- trRead{s: s, events: evs, pos: s.pos.Load(), clock: s.clock.Load()}:
			case <-j.ctx.Done():
				s.close(true)
				return
			}
		}
	}
}

// end lets the writer go.
func (s *trSession) end() { s.once.Do(func() { close(s.gone) }) }

// stopWriting tells write() to exit immediately without waiting for s.gone.
func (s *trSession) stopWriting() {
	s.closing.Store(true)
	s.stopWriteOnce.Do(func() { close(s.stopWrite) })
}

// addSilence has the writer send n samples of silence once the session's audio reached recording position at: a
// turn's end for the service, which needs audio to hear a silence.
func (s *trSession) addSilence(at, n int64) {
	s.mu.Lock()
	s.quiet = append(s.quiet, trQuiet{at: at, n: n})
	s.mu.Unlock()
	s.wake()
}

func (s *trSession) wake() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// write sends the job's audio to the session from its feed on, as it comes (live: a verbatim session at most trCatchUp
// times real time while it is behind, a caption session skips to the live edge once it is more than trCaptionJump
// behind) or at the job's speed (replay), and the silence asked for; in replay, once its audio reached its stop, the
// tail of silence that ends the last turn, and then nothing.
func (s *trSession) write() {
	defer close(s.writerDone)
	j := s.j
	var paceFrom time.Time
	var paced int64
	silence := make([]byte, trChunkMost)
	tail := false
	for {
		if s.closing.Load() {
			return
		}
		select {
		case <-s.stopWrite:
			return
		case <-s.gone:
			return
		default:
		}
		pos := s.pos.Load()
		s.mu.Lock()
		var n int64
		if len(s.quiet) > 0 && pos >= s.quiet[0].at {
			n = min(s.quiet[0].n, trChunkMost/2)
			if s.quiet[0].n -= n; s.quiet[0].n == 0 {
				s.quiet = s.quiet[1:]
			}
		}
		s.mu.Unlock()
		if n > 0 {
			if !s.send(silence[:n*2], -1) {
				return
			}
			continue
		}
		stop := s.stop.Load()
		if stop >= 0 && pos >= stop {
			if !tail {
				tail = true
				s.addSilence(stop, trSamples(trReplayTail))
				continue
			}
			s.written.Store(true)
			select {
			case <-s.poke:
			case <-s.stopWrite:
				return
			case <-s.gone:
				return
			}
			continue
		}
		bound := stop
		if bound < 0 {
			bound = s.limit.Load()
		}
		at, pcm, wait := j.buf.read(pos, bound, trChunkMost)
		if pcm == nil {
			if wait == nil {
				wait = s.poke
			}
			select {
			case <-wait:
			case <-s.poke:
			case <-s.stopWrite:
				return
			case <-s.gone:
				return
			}
			paceFrom = time.Time{}
			continue
		}
		speed := j.speed
		if j.mode == modeLive {
			speed = 0
			switch edge := j.buf.edgeNow(); {
			case s.kind == trCaption && edge-at > trSamples(trCaptionJump):
				s.skip(at, edge)
				continue
			case s.kind != trCaption && edge-at > trRate:
				speed = trCatchUp
			}
		}
		if speed > 0 {
			if paceFrom.IsZero() {
				paceFrom, paced = time.Now(), 0
			}
			due := paceFrom.Add(time.Duration(float64(paced) / trRate / speed * float64(time.Second)))
			if d := time.Until(due); d > 0 {
				t := time.NewTimer(d)
				select {
				case <-t.C:
				case <-s.stopWrite:
					t.Stop()
					return
				case <-s.gone:
					t.Stop()
					return
				}
			}
			paced += int64(len(pcm) / 2)
		} else {
			paceFrom = time.Time{}
		}
		if s.closing.Load() {
			return
		}
		if !s.send(pcm, at) {
			return
		}
	}
}

// send writes one append: audio at recording position at, or silence (at -1). False: the session is gone.
func (s *trSession) send(pcm []byte, at int64) bool {
	n := int64(len(pcm) / 2)
	ctx, cancel := context.WithTimeout(s.rctx, upWriteTimeout)
	err := writeJSON(ctx, s.up, liveWire{}.audio(pcm))
	cancel()
	if err != nil {
		_ = s.up.CloseNow() // the reader sees it and tells the job
		return false
	}
	s.mu.Lock()
	clock := s.clock.Load()
	pos := s.pos.Load()
	if at < 0 {
		s.marks = append(s.marks, trMark{clock: clock, at: pos, n: n, silent: true})
	} else {
		if k := len(s.marks) - 1; k >= 0 && !s.marks[k].silent && s.marks[k].at+s.marks[k].n == at && s.marks[k].clock+s.marks[k].n == clock {
			s.marks[k].n += n
		} else {
			s.marks = append(s.marks, trMark{clock: clock, at: at, n: n})
		}
		s.pos.Store(at + n)
	}
	s.clock.Store(clock + n)
	s.mu.Unlock()
	return true
}

// skip has a live caption session go on from the live edge to, leaving out the audio [from, to) (trCaptionJump): the
// service hears the audio jump, as across a hole, and the windows of what it left out get no captions (skipped).
func (s *trSession) skip(from, to int64) {
	s.mu.Lock()
	s.skips = append(s.skips, [2]int64{from, to})
	s.pos.Store(to)
	s.mu.Unlock()
	s.j.logf("session %d (%s) was %.1f s behind the live edge: it skipped to it", s.no, s.kind, trSeconds(to-from))
}

// skipped says whether s left out any of the recording audio [from, to) (skip).
func (s *trSession) skipped(from, to int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.skips {
		if k[0] < to && from < k[1] {
			return true
		}
	}
	return false
}

// clockAt is the session sample at which recording position at was sent, or the session went on past it (false: not
// yet).
func (s *trSession) clockAt(at int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.marks {
		switch {
		case !m.silent && at <= m.at:
			return m.clock, true
		case !m.silent && at <= m.at+m.n:
			return m.clock + (at - m.at), true
		case m.silent && at <= m.at:
			return m.clock, true
		}
	}
	return 0, false
}

// pruneMarks forgets what was sent, or skipped, before recording position at.
func (s *trSession) pruneMarks(at int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := 0
	for i < len(s.marks)-1 && s.marks[i].at+s.marks[i].n < at {
		i++
	}
	s.marks = s.marks[i:]
	s.skips = slices.DeleteFunc(s.skips, func(k [2]int64) bool { return k[1] <= at })
}

// forceClose tears down s's reader, writer, and underlying socket immediately without
// blocking on a WebSocket close handshake.
func (s *trSession) forceClose() {
	s.forced.Store(true)
	s.nowOnce.Do(func() {
		s.end()
		s.stopR()
		_ = s.up.CloseNow()
	})
}

// close ends s: politely (session.close, and the service's session.closed with its usage, which the reader passes
// on) unless now, then the socket. A forced close (now) always overrides an in-flight polite close.
func (s *trSession) close(now bool) {
	s.stopWriting()
	if s.up == nil {
		s.end()
		return
	}
	if now {
		s.forceClose()
		return
	}
	if s.forced.Load() {
		return
	}
	s.politeOnce.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(s.rctx, 2*time.Second)
			defer cancel()
			// Wait for write() to finish any in-flight append before sending session.close,
			// so audio frames cannot interleave with or follow session.close.
			select {
			case <-s.writerDone:
			case <-s.gone:
			case <-ctx.Done():
			}
			if !s.forced.Load() && ctx.Err() == nil {
				select {
				case <-s.gone:
				default:
					if writeJSON(ctx, s.up, liveWire{}.close()) == nil {
						select {
						case <-s.gone: // the service closed it after session.closed
						case <-ctx.Done():
						}
					}
				}
			}
			// Always finish through forceClose (end -> stopR -> CloseNow) rather than
			// up.Close so a wedged writer or peer cannot stall teardown or block a
			// concurrent close(true) on coder/websocket's closing handshake lock.
			s.forceClose()
		}()
	})
}

// edgeNow is the end of the newest audio.
func (b *trBuffer) edgeNow() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.edge
}
