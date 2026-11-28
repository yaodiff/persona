package voice

import (
	"context"
	"sync"
	"sync/atomic"
)

// Streaming TTS bridge for voice calls (issue #17843).
//
// How call code (PR 4) uses ttsBridge:
//   - Create one ttsBridge per call once the output sample rate (16000 or 24000 Hz) is known.
//   - When an assistant reply starts (evReply, or first evAssistant on dialects without evReply),
//     call gen := bridge.StartTurn(voice) while holding conversation.mu and record c.ttsGen = gen.
//   - On each assistant text delta, call bridge.Feed(c.ttsGen, delta) under conversation.mu.
//   - On evTurnEnd, call bridge.EndText(c.ttsGen) under conversation.mu.
//   - On any interruption, reconnect, suspend, or hang-up, call bridge.Cancel() under
//     conversation.mu and clear c.ttsGen = 0; StartTurn, Feed, EndText, Cancel, and Active
//     never block and are safe to call while holding conversation.mu.
//   - Callbacks (onFrame, onDone, onError) run from the bridge's own goroutine with NO bridge
//     lock held, so they may safely acquire conversation.mu. Inside onFrame/onDone, check
//     gen == c.ttsGen under conversation.mu to discard any frame that raced with Cancel.
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
	onFrame         func(gen uint64, pcm []byte)
	onDone          func(gen uint64, cancelled bool, err error)
	onError         func(gen uint64, err error) // called when a single piece fails and is skipped
	logf            func(format string, args ...any)
}

// ttsBridge turns streamed assistant text deltas into ordered 20 ms PCM16 frames at the call's
// output sample rate, with generation-gated barge-in cancellation and optional 1-piece prefetch.
type ttsBridge struct {
	client          *ttsClient
	outputRate      int
	chunkerCfg      ttsChunkerConfig
	disablePrefetch bool
	onFrame         func(gen uint64, pcm []byte)
	onDone          func(gen uint64, cancelled bool, err error)
	onError         func(gen uint64, err error)
	logf            func(format string, args ...any)

	gen       atomic.Uint64 // bumped atomically on StartTurn, Cancel, and Close
	activeGen atomic.Uint64 // generation of active turn, or 0 when idle/cancelled/closed

	mu        sync.Mutex
	closed    bool
	running   *ttsTurn // turn currently owned by the worker goroutine
	pending   *ttsTurn // latest turn waiting for the worker goroutine to pick up
	cancelled []uint64 // pending turns cancelled before the worker picked them up
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

// StartTurn begins a new turn for voice, cancelling any active turn first, and returns its
// generation number (or 0 if the bridge is closed). Never blocks.
func (b *ttsBridge) StartTurn(voice string) uint64 {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0
	}
	cancelRunning := b.cancelActiveLocked()
	gen := b.gen.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	t := &ttsTurn{
		gen:     gen,
		voice:   voice,
		ctx:     ctx,
		cancel:  cancel,
		chunker: newTTSChunker(b.chunkerCfg),
		notify:  make(chan struct{}, 1),
	}
	b.pending = t
	b.activeGen.Store(gen)
	b.mu.Unlock()

	if cancelRunning != nil {
		cancelRunning()
	}
	signalChan(b.wake)
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

// Cancel aborts the active turn immediately: bumps the generation atomically, cancels in-flight
// HTTP requests, and marks the bridge inactive. Never blocks.
func (b *ttsBridge) Cancel() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.gen.Add(1)
	b.activeGen.Store(0)
	cancelRunning := b.cancelActiveLocked()
	b.mu.Unlock()

	if cancelRunning != nil {
		cancelRunning()
	}
	signalChan(b.wake)
}

// Active reports whether a turn is currently active (started and neither finished nor cancelled).
// Never blocks.
func (b *ttsBridge) Active() bool {
	return b.activeGen.Load() != 0
}

// Close cancels any active turn, stops accepting new turns, and waits for all bridge goroutines
// to exit. Call Close without holding any mutex that callbacks acquire.
func (b *ttsBridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.wg.Wait()
		return
	}
	b.closed = true
	b.gen.Add(1)
	b.activeGen.Store(0)
	cancelRunning := b.cancelActiveLocked()
	b.mu.Unlock()

	if cancelRunning != nil {
		cancelRunning()
	}
	signalChan(b.wake)
	b.wg.Wait()
	b.client.closeIdle()
}

// cancelActiveLocked marks any pending or running turn as cancelled (b.mu held) and returns
// the running turn's cancel func (if any) to call outside the lock.
func (b *ttsBridge) cancelActiveLocked() context.CancelFunc {
	if b.pending != nil {
		b.cancelled = append(b.cancelled, b.pending.gen)
		b.pending.cancel()
		b.pending = nil
	}
	if b.running != nil {
		return b.running.cancel
	}
	return nil
}

func (b *ttsBridge) turnForGenLocked(gen uint64) *ttsTurn {
	if b.closed || b.gen.Load() != gen {
		return nil
	}
	if b.pending != nil && b.pending.gen == gen {
		return b.pending
	}
	if b.running != nil && b.running.gen == gen {
		return b.running
	}
	return nil
}

func (b *ttsBridge) loop() {
	defer b.wg.Done()
	for {
		b.mu.Lock()
		cancelled := b.cancelled
		b.cancelled = nil
		next := b.pending
		if next != nil {
			b.pending = nil
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

	defer func() {
		if prefetched != nil {
			t.cancel()
			<-prefetched.done
		}
	}()

	isStale := func() bool {
		return t.ctx.Err() != nil || b.gen.Load() != t.gen
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
	b.activeGen.CompareAndSwap(t.gen, 0)
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
