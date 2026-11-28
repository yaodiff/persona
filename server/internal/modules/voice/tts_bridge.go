package voice

import (
	"context"
	"sync"
	"sync/atomic"
)

// Streaming TTS bridge for voice calls (issue #17843).
//
// How call code uses ttsBridge:
//   - Create one ttsBridge per call once the output sample rate (16000 or 24000 Hz) is known.
//   - When an assistant reply starts producing text, call gen := bridge.EnqueueTurn(voice) while
//     holding conversation.mu (EnqueueTurn queues consecutive replies so an earlier turn finishes
//     speaking; StartTurn cancels any active/queued turns first).
//   - On each assistant text delta, call bridge.Feed(gen, delta) under conversation.mu.
//   - On evTurnEnd, call bridge.EndText(gen) under conversation.mu.
//   - On any interruption, reconnect, suspend, or hang-up, call bridge.Cancel() under
//     conversation.mu and record the cancelled watermark; StartTurn, EnqueueTurn, Feed, EndText,
//     Cancel, and Active never block and are safe to call while holding conversation.mu.
//   - Callbacks (onStart, onFrame, onDone, onError) run from the bridge's own goroutine with NO
//     bridge lock held, so they may safely acquire conversation.mu. Inside them, check
//     gen > c.ttsCancelGen under conversation.mu to discard any callback that raced with Cancel.
//   - Every delivered frame is an exact 20 ms mono PCM16 LE frame at outputRate (640 B at 16 kHz,
//     960 B at 24 kHz); the turn's last partial frame (< 20 ms) is zero-padded to 20 ms.
//   - When the call ends, call bridge.Close() AFTER releasing conversation.mu (e.g. next to
//     closing the upstream socket in conversation.shutdown) so all bridge goroutines exit cleanly.

const ttsPrefetchChunks = 16 // at most 16 * 4096 B = 64 KiB buffered for the single prefetched piece

type ttsBridgeConfig struct {
	client          *ttsClient
	outputRate      int // 16000 or 24000 (defaults to 24000 if invalid)
	chunker         ttsChunkerConfig
	disablePrefetch bool
	onStart         func(gen uint64) // called once right before synthesizing the turn's first speakable piece
	onFrame         func(gen uint64, pcm []byte)
	onDone          func(gen uint64, cancelled bool, err error)
	onError         func(gen uint64, err error) // called when a single piece fails and is skipped
	logf            func(format string, args ...any)
}

// ttsBridge turns streamed assistant text deltas into ordered 20 ms PCM16 frames at the call's
// output sample rate, with generation-gated barge-in cancellation, FIFO turn queueing, and
// optional 1-piece prefetch.
type ttsBridge struct {
	client          *ttsClient
	outputRate      int
	chunkerCfg      ttsChunkerConfig
	disablePrefetch bool
	onStart         func(gen uint64)
	onFrame         func(gen uint64, pcm []byte)
	onDone          func(gen uint64, cancelled bool, err error)
	onError         func(gen uint64, err error)
	logf            func(format string, args ...any)

	nextGen      atomic.Uint64 // monotonic turn generation allocator (1, 2, ...)
	cancelBefore atomic.Uint64 // every turn with gen <= cancelBefore is cancelled
	active       atomic.Bool   // true while any uncancelled turn is running or queued

	mu        sync.Mutex
	closed    bool
	running   *ttsTurn   // turn currently owned by the worker goroutine
	pending   []*ttsTurn // FIFO queue of turns waiting for the worker goroutine
	cancelled []uint64   // queued turns cancelled before the worker picked them up
	wake      chan struct{}
	wg        sync.WaitGroup
}

type ttsTurn struct {
	gen       uint64
	voice     string
	ctx       context.Context
	cancel    context.CancelFunc
	chunker   *ttsChunker
	queue     []string // protected by ttsBridge.mu
	textEnded bool     // protected by ttsBridge.mu
	notify    chan struct{}
}

type ttsPrefetchChunk struct {
	pcm  []byte
	rate int
}

type ttsPrefetch struct {
	chunks chan ttsPrefetchChunk
	done   chan struct{}
	err    error
}

func newTTSBridge(cfg ttsBridgeConfig) *ttsBridge {
	rate := cfg.outputRate
	if !validTTSRate(rate) {
		rate = ttsDefaultRate
	}
	b := &ttsBridge{
		client:          cfg.client,
		outputRate:      rate,
		chunkerCfg:      cfg.chunker.normalized(),
		disablePrefetch: cfg.disablePrefetch,
		onStart:         cfg.onStart,
		onFrame:         cfg.onFrame,
		onDone:          cfg.onDone,
		onError:         cfg.onError,
		logf:            cfg.logf,
		wake:            make(chan struct{}, 1),
	}
	b.wg.Add(1)
	go b.loop()
	return b
}

// StartTurn begins a new turn for voice, cancelling any active or queued turns first, and returns
// its generation number (or 0 if the bridge is closed). Never blocks.
func (b *ttsBridge) StartTurn(voice string) uint64 {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0
	}
	cancelRunning := b.cancelAllLocked()
	gen := b.enqueueLocked(voice)
	b.mu.Unlock()

	if cancelRunning != nil {
		cancelRunning()
	}
	signalChan(b.wake)
	return gen
}

// EnqueueTurn appends a new turn for voice after any currently active or queued turns without
// cancelling them, and returns its generation number (or 0 if the bridge is closed). Never blocks.
func (b *ttsBridge) EnqueueTurn(voice string) uint64 {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0
	}
	gen := b.enqueueLocked(voice)
	b.mu.Unlock()

	signalChan(b.wake)
	return gen
}

func (b *ttsBridge) enqueueLocked(voice string) uint64 {
	gen := b.nextGen.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	t := &ttsTurn{
		gen:     gen,
		voice:   voice,
		ctx:     ctx,
		cancel:  cancel,
		chunker: newTTSChunker(b.chunkerCfg),
		notify:  make(chan struct{}, 1),
	}
	b.pending = append(b.pending, t)
	b.active.Store(true)
	return gen
}

// Feed pushes a streamed text delta to generation gen. Calls for an old or unknown generation
// or after EndText are ignored. Never blocks.
func (b *ttsBridge) Feed(gen uint64, delta string) {
	if delta == "" || gen == 0 {
		return
	}
	b.mu.Lock()
	t := b.turnForGenLocked(gen)
	if t == nil || t.textEnded {
		b.mu.Unlock()
		return
	}
	pieces := t.chunker.Push(delta)
	if len(pieces) > 0 {
		t.queue = append(t.queue, pieces...)
	}
	notify := t.notify
	b.mu.Unlock()

	if len(pieces) > 0 {
		signalChan(notify)
	}
}

// EndText marks the end of streamed text for generation gen and flushes any remaining clause
// from the chunker. Never blocks.
func (b *ttsBridge) EndText(gen uint64) {
	if gen == 0 {
		return
	}
	b.mu.Lock()
	t := b.turnForGenLocked(gen)
	if t == nil || t.textEnded {
		b.mu.Unlock()
		return
	}
	if pieces := t.chunker.Flush(); len(pieces) > 0 {
		t.queue = append(t.queue, pieces...)
	}
	t.textEnded = true
	notify := t.notify
	b.mu.Unlock()

	signalChan(notify)
}

// Cancel aborts the active turn and all queued turns immediately: advances the cancel watermark,
// cancels in-flight HTTP requests, and marks the bridge inactive. Never blocks.
func (b *ttsBridge) Cancel() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	cancelRunning := b.cancelAllLocked()
	b.mu.Unlock()

	if cancelRunning != nil {
		cancelRunning()
	}
	signalChan(b.wake)
}

// Active reports whether any uncancelled turn is currently running or queued. Never blocks.
func (b *ttsBridge) Active() bool {
	return b.active.Load()
}

// Close cancels any active or queued turns, stops accepting new turns, and waits for all bridge
// goroutines to exit. Call Close without holding any mutex that callbacks acquire.
func (b *ttsBridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.wg.Wait()
		return
	}
	b.closed = true
	cancelRunning := b.cancelAllLocked()
	b.mu.Unlock()

	if cancelRunning != nil {
		cancelRunning()
	}
	signalChan(b.wake)
	b.wg.Wait()
	b.client.closeIdle()
}

// cancelAllLocked marks every queued and running turn as cancelled (b.mu held) and returns
// the running turn's cancel func (if any) to call outside the lock.
func (b *ttsBridge) cancelAllLocked() context.CancelFunc {
	b.cancelBefore.Store(b.nextGen.Load())
	for _, p := range b.pending {
		b.cancelled = append(b.cancelled, p.gen)
		p.cancel()
	}
	b.pending = nil
	b.active.Store(false)
	if b.running != nil {
		return b.running.cancel
	}
	return nil
}

func (b *ttsBridge) updateActiveLocked() {
	hasRunning := b.running != nil && b.running.gen > b.cancelBefore.Load()
	b.active.Store(!b.closed && (hasRunning || len(b.pending) > 0))
}

func (b *ttsBridge) turnForGenLocked(gen uint64) *ttsTurn {
	if b.closed || gen == 0 || gen <= b.cancelBefore.Load() {
		return nil
	}
	if b.running != nil && b.running.gen == gen {
		return b.running
	}
	for _, p := range b.pending {
		if p.gen == gen {
			return p
		}
	}
	return nil
}

func (b *ttsBridge) loop() {
	defer b.wg.Done()
	for {
		b.mu.Lock()
		cancelled := b.cancelled
		b.cancelled = nil
		var next *ttsTurn
		if len(b.pending) > 0 {
			next = b.pending[0]
			b.pending[0] = nil
			b.pending = b.pending[1:]
			b.running = next
		}
		closed := b.closed
		b.mu.Unlock()

		for _, g := range cancelled {
			b.deliverDone(g, true, nil)
		}
		if next != nil {
			b.runTurn(next)
			b.mu.Lock()
			if b.running == next {
				b.running = nil
				b.updateActiveLocked()
			}
			b.mu.Unlock()
			continue
		}
		if closed {
			return
		}
		<-b.wake
	}
}

func (b *ttsBridge) runTurn(t *ttsTurn) {
	defer t.cancel()

	framer := newTTSFramer(b.outputRate)
	var lastErr error
	var prefetched *ttsPrefetch
	started := false

	defer func() {
		if prefetched != nil {
			t.cancel()
			<-prefetched.done
		}
	}()

	isStale := func() bool {
		return t.ctx.Err() != nil || t.gen <= b.cancelBefore.Load()
	}

	emitFrame := func(frame []byte) error {
		if isStale() {
			return context.Canceled
		}
		if b.onFrame != nil {
			b.onFrame(t.gen, frame)
		}
		return nil
	}

	maybePrefetch := func() {
		if b.disablePrefetch || prefetched != nil || b.client == nil || isStale() {
			return
		}
		piece, ok := b.popPiece(t)
		if !ok {
			return
		}
		prefetched = b.startPrefetch(t, piece)
	}

	for {
		if isStale() {
			b.finishTurn(t, true, nil)
			return
		}

		// Obtain the next piece: either already prefetched or pulled from the turn's queue.
		var pf *ttsPrefetch
		var piece string
		if prefetched != nil {
			pf = prefetched
			prefetched = nil
		} else {
			var ok, ended bool
			piece, ok, ended = b.nextPieceOrEnded(t)
			if !ok {
				if ended {
					if err := framer.flush(emitFrame); err != nil || isStale() {
						b.finishTurn(t, true, nil)
						return
					}
					b.finishTurn(t, false, lastErr)
					return
				}
				select {
				case <-t.notify:
				case <-t.ctx.Done():
					b.finishTurn(t, true, nil)
					return
				}
				continue
			}
		}

		if !started {
			started = true
			if b.onStart != nil {
				b.onStart(t.gen)
			}
			if isStale() {
				b.finishTurn(t, true, nil)
				return
			}
		}

		var resampler *ttsResampler
		resamplerRate := 0
		handleChunk := func(pcm []byte, rate int) error {
			if isStale() {
				return context.Canceled
			}
			// Once the current piece starts streaming, prefetch the next queued piece so its
			// TTFB overlaps with the remainder of this piece while preserving request order.
			maybePrefetch()

			if resampler == nil || resamplerRate != rate {
				if resampler != nil {
					if tail := resampler.flush(); len(tail) > 0 {
						if err := framer.push(tail, emitFrame); err != nil {
							return err
						}
					}
				}
				resampler = newTTSResampler(rate, b.outputRate)
				resamplerRate = rate
			}
			out := resampler.process(pcm)
			if len(out) == 0 {
				return nil
			}
			return framer.push(out, emitFrame)
		}

		var err error
		if pf != nil {
			for ch := range pf.chunks {
				if err = handleChunk(ch.pcm, ch.rate); err != nil {
					break
				}
			}
			if err != nil {
				// Current turn cancelled while draining prefetch: abort and join prefetch goroutine.
				t.cancel()
				<-pf.done
				b.finishTurn(t, true, nil)
				return
			}
			<-pf.done
			err = pf.err
		} else if b.client != nil {
			err = b.client.streamPCM(t.ctx, piece, t.voice, handleChunk)
		}

		if isStale() {
			b.finishTurn(t, true, nil)
			return
		}
		if err != nil {
			lastErr = err
			b.reportPieceError(t.gen, err)
			continue
		}
		if resampler != nil {
			if tail := resampler.flush(); len(tail) > 0 {
				if err := framer.push(tail, emitFrame); err != nil || isStale() {
					b.finishTurn(t, true, nil)
					return
				}
			}
		}
	}
}

func (b *ttsBridge) startPrefetch(t *ttsTurn, piece string) *ttsPrefetch {
	pf := &ttsPrefetch{
		chunks: make(chan ttsPrefetchChunk, ttsPrefetchChunks),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(pf.done)
		defer close(pf.chunks)
		pf.err = b.client.streamPCM(t.ctx, piece, t.voice, func(pcm []byte, rate int) error {
			select {
			case pf.chunks <- ttsPrefetchChunk{pcm: pcm, rate: rate}:
				return nil
			case <-t.ctx.Done():
				return context.Canceled
			}
		})
	}()
	return pf
}

func (b *ttsBridge) popPiece(t *ttsTurn) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(t.queue) == 0 {
		return "", false
	}
	piece := t.queue[0]
	t.queue[0] = "" // clear reference for GC
	t.queue = t.queue[1:]
	return piece, true
}

func (b *ttsBridge) nextPieceOrEnded(t *ttsTurn) (piece string, ok bool, ended bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(t.queue) > 0 {
		piece = t.queue[0]
		t.queue[0] = ""
		t.queue = t.queue[1:]
		return piece, true, false
	}
	return "", false, t.textEnded
}

func (b *ttsBridge) finishTurn(t *ttsTurn, cancelled bool, err error) {
	b.mu.Lock()
	if b.running == t {
		b.running = nil
	}
	b.updateActiveLocked()
	b.mu.Unlock()
	b.deliverDone(t.gen, cancelled, err)
}

func (b *ttsBridge) deliverDone(gen uint64, cancelled bool, err error) {
	if b.onDone != nil {
		b.onDone(gen, cancelled, err)
	}
}

func (b *ttsBridge) reportPieceError(gen uint64, err error) {
	if b.onError != nil {
		b.onError(gen, err)
		return
	}
	if b.logf != nil {
		b.logf("tts: piece failed (gen %d): %v", gen, err)
	}
}

func signalChan(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
