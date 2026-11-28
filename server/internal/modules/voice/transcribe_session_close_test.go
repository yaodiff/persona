package voice

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/coder/websocket"
)

type fakeTrUpLink struct {
	mu             sync.Mutex
	frames         []string
	closed         chan struct{}
	closeOnce      sync.Once
	closeNowCnt    atomic.Int32
	closeCnt       atomic.Int32
	inFlightWrites atomic.Int32
	writeOverlap   atomic.Bool

	readMsgs chan []byte

	writeHook func(ctx context.Context, frameType string) error
}

func newFakeTrUpLink() *fakeTrUpLink {
	return &fakeTrUpLink{
		closed:   make(chan struct{}),
		readMsgs: make(chan []byte, 4),
	}
}

func (f *fakeTrUpLink) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case msg := <-f.readMsgs:
		return websocket.MessageText, msg, nil
	case <-f.closed:
		return 0, nil, io.EOF
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (f *fakeTrUpLink) Write(ctx context.Context, _ websocket.MessageType, p []byte) error {
	if f.inFlightWrites.Add(1) > 1 {
		f.writeOverlap.Store(true)
	}
	defer f.inFlightWrites.Add(-1)
	var env struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(p, &env)
	if f.writeHook != nil {
		if err := f.writeHook(ctx, env.Type); err != nil {
			return err
		}
	}
	select {
	case <-f.closed:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	f.mu.Lock()
	f.frames = append(f.frames, env.Type)
	f.mu.Unlock()
	return nil
}

func (f *fakeTrUpLink) Close(websocket.StatusCode, string) error {
	f.closeCnt.Add(1)
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeTrUpLink) CloseNow() error {
	f.closeNowCnt.Add(1)
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeTrUpLink) snapshotFrames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.frames))
	copy(out, f.frames)
	return out
}

func newTestTrJob(ctx context.Context, mode string, speed float64) *trJob {
	ctx, cancel := context.WithCancel(ctx)
	return &trJob{
		pl:     &Plugin{logger: log.New(io.Discard, "", 0)},
		ctx:    ctx,
		cancel: cancel,
		mode:   mode,
		speed:  speed,
		buf:    newTrBuffer(0),
		reads:  make(chan trRead),
	}
}

func TestTrSessionForceCloseOverridesPoliteClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestTrJob(context.Background(), modeLive, 0)
		defer j.cancel()

		up := newFakeTrUpLink()
		s := newTrSession(j, trVerbatim, nil, 0, "test")
		s.run(up, 0)
		synctest.Wait()

		// Polite close sends session.close and waits up to 2s for upstream close.
		s.close(false)
		synctest.Wait()

		frames := up.snapshotFrames()
		if len(frames) != 1 || frames[0] != "session.close" {
			t.Fatalf("after polite close frames = %v, want [session.close]", frames)
		}
		select {
		case <-s.gone:
			t.Fatal("s.gone closed before upstream closed or force close")
		default:
		}

		// Force close (as in trJob.cleanup) must override an in-flight polite close immediately.
		s.close(true)
		synctest.Wait()

		select {
		case <-s.gone:
		default:
			t.Fatal("force close after polite close did not close s.gone immediately")
		}
		if up.closeNowCnt.Load() == 0 {
			t.Fatal("force close after polite close did not call up.CloseNow()")
		}
		if s.rctx.Err() == nil {
			t.Fatal("force close after polite close did not cancel reader context")
		}
		// Drain the reader's final error message if it is waiting on j.reads.
		select {
		case <-j.reads:
		default:
		}
		j.cancel()
		synctest.Wait()
	})
}

func TestTrSessionPoliteCloseStopsWriterBeforeSessionClose(t *testing.T) {
	t.Run("waits for in-flight audio write and sends no audio after session.close", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			j := newTestTrJob(context.Background(), modeLive, 0)
			defer j.cancel()

			chunk := make([]byte, trChunkMost)
			j.buf.add(0, chunk)
			j.buf.add(int64(len(chunk)/2), chunk)

			firstWriteStarted := make(chan struct{})
			releaseFirstWrite := make(chan struct{})
			var writeNum atomic.Int32

			up := newFakeTrUpLink()
			up.writeHook = func(ctx context.Context, frameType string) error {
				if frameType != "session.close" && writeNum.Add(1) == 1 {
					close(firstWriteStarted)
					<-releaseFirstWrite
				}
				return nil
			}

			s := newTrSession(j, trVerbatim, nil, 0, "test")
			s.run(up, 0)

			<-firstWriteStarted
			s.close(false)
			synctest.Wait()

			// session.close must not race ahead of the in-flight audio write.
			if got := up.snapshotFrames(); len(got) != 0 {
				t.Fatalf("frames written before first audio write finished = %v, want empty", got)
			}

			close(releaseFirstWrite)
			synctest.Wait()

			frames := up.snapshotFrames()
			if len(frames) != 2 || frames[0] == "session.close" || frames[1] != "session.close" {
				t.Fatalf("frames = %v, want exactly [audio, session.close]", frames)
			}

			// Upstream responds by closing the socket so polite close finishes cleanly.
			_ = up.CloseNow()
			synctest.Wait()
			if up.writeOverlap.Load() {
				t.Fatal("detected concurrent up.Write calls")
			}
			select {
			case <-j.reads:
			default:
			}
			j.cancel()
			synctest.Wait()
		})
	})

	t.Run("stuck audio write unblocks cleanly at polite-close timeout without concurrent session.close", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			j := newTestTrJob(context.Background(), modeLive, 0)
			defer j.cancel()

			chunk := make([]byte, trChunkMost)
			j.buf.add(0, chunk)

			writeEntered := make(chan struct{})
			up := newFakeTrUpLink()
			up.writeHook = func(ctx context.Context, frameType string) error {
				if frameType != "session.close" {
					close(writeEntered)
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}

			s := newTrSession(j, trVerbatim, nil, 0, "test")
			s.run(up, 0)

			<-writeEntered
			s.close(false)

			// Wait for the 2s polite-close timeout to trigger forceClose (stopR + CloseNow)
			// without writing session.close concurrently with the wedged audio write.
			<-s.gone
			synctest.Wait()

			if up.writeOverlap.Load() {
				t.Fatal("polite close wrote session.close concurrently with stuck audio write")
			}
			if got := up.snapshotFrames(); len(got) != 0 {
				t.Fatalf("frames written during stuck-write polite timeout = %v, want empty", got)
			}
			if up.closeCnt.Load() != 0 {
				t.Fatalf("expected forceClose via CloseNow, saw %d up.Close calls", up.closeCnt.Load())
			}
			if up.closeNowCnt.Load() == 0 {
				t.Fatal("expected CloseNow to be called on polite-close timeout")
			}
			if s.rctx.Err() == nil {
				t.Fatal("polite-close timeout did not cancel s.rctx to unblock stuck writer")
			}
			select {
			case <-s.writerDone:
			default:
				t.Fatal("writer did not exit after polite-close timeout canceled s.rctx")
			}
			select {
			case <-j.reads:
			default:
			}
			j.cancel()
			synctest.Wait()
		})
	})

	t.Run("paced writer exits immediately on polite close without sending queued audio", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			j := newTestTrJob(context.Background(), modeReplay, 1.0)
			defer j.cancel()

			chunk := make([]byte, trChunkMost)
			j.buf.add(0, chunk)
			j.buf.add(int64(len(chunk)/2), chunk)

			firstWritten := make(chan struct{})
			var writeNum atomic.Int32
			up := newFakeTrUpLink()
			up.writeHook = func(ctx context.Context, frameType string) error {
				if frameType != "session.close" && writeNum.Add(1) == 1 {
					close(firstWritten)
				}
				return nil
			}

			s := newTrSession(j, trVerbatim, nil, 0, "test")
			s.run(up, 0)

			<-firstWritten
			// Wait until writer is parked on the 100ms pacing timer for chunk 2.
			synctest.Wait()

			s.close(false)
			synctest.Wait()

			// Let fake time advance past the 100ms pacing timer (and 2s polite timeout) to verify no audio follows session.close.
			// First verify session.close was sent before upstream closes.
			frames := up.snapshotFrames()
			if len(frames) != 2 || frames[0] == "session.close" || frames[1] != "session.close" {
				t.Fatalf("frames immediately after polite close = %v, want [audio, session.close]", frames)
			}

			// Let the 2s polite close timeout expire; no second audio frame may ever be written.
			<-s.gone
			synctest.Wait()

			frames = up.snapshotFrames()
			if len(frames) != 2 || frames[0] == "session.close" || frames[1] != "session.close" {
				t.Fatalf("frames after polite close completed = %v, want [audio, session.close] with no post-close audio", frames)
			}
			select {
			case <-j.reads:
			default:
			}
			j.cancel()
			synctest.Wait()
		})
	})
}

func TestTrSessionReadJobCancelClosesGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestTrJob(context.Background(), modeLive, 0)
		up := newFakeTrUpLink()
		up.readMsgs <- []byte(`{"type":"session.closed"}`)

		s := newTrSession(j, trVerbatim, nil, 0, "test")
		s.run(up, 0)
		// Wait until read() is blocked delivering the decoded event on unbuffered j.reads.
		synctest.Wait()

		j.cancel()
		synctest.Wait()

		select {
		case <-s.gone:
		default:
			t.Fatal("s.gone stayed open after read() exited on j.ctx.Done()")
		}
		if up.closeNowCnt.Load() != 1 {
			t.Fatalf("read() exit on j.ctx.Done() CloseNow count = %d, want 1", up.closeNowCnt.Load())
		}
		if s.rctx.Err() == nil {
			t.Fatal("read() exit on j.ctx.Done() did not cancel s.rctx")
		}
	})
}

func TestTrSessionCloseOrderingAndConcurrency(t *testing.T) {
	t.Run("close(false) before run closes gone and immediately closes late upLink", func(t *testing.T) {
		j := newTestTrJob(context.Background(), modeLive, 0)
		defer j.cancel()

		s := newTrSession(j, trVerbatim, nil, 0, "test")
		s.close(false)
		select {
		case <-s.gone:
		default:
			t.Fatal("close(false) with nil up did not close s.gone")
		}
		up := newFakeTrUpLink()
		s.run(up, 0)
		if up.closeNowCnt.Load() != 1 {
			t.Fatalf("run() after close(false) CloseNow count = %d, want 1", up.closeNowCnt.Load())
		}
		if s.writerDone != nil {
			t.Fatal("run() after close(false) spawned writer goroutine")
		}
	})

	t.Run("close(true) then close(false) does not spawn polite close or send frames", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			j := newTestTrJob(context.Background(), modeLive, 0)
			defer j.cancel()

			up := newFakeTrUpLink()
			s := newTrSession(j, trVerbatim, nil, 0, "test")
			s.run(up, 0)
			synctest.Wait()

			s.close(true)
			s.close(false)
			synctest.Wait()

			if got := up.snapshotFrames(); len(got) != 0 {
				t.Fatalf("frames after close(true)+close(false) = %v, want empty", got)
			}
			if up.closeNowCnt.Load() == 0 {
				t.Fatal("expected CloseNow to be called on polite-close timeout")
			}
			select {
			case <-j.reads:
			default:
			}
			j.cancel()
			synctest.Wait()
		})
	})

	t.Run("concurrent close(false) and close(true) under -race", func(t *testing.T) {
		for range 16 {
			synctest.Test(t, func(t *testing.T) {
				j := newTestTrJob(context.Background(), modeLive, 0)
				defer j.cancel()

				chunk := make([]byte, trChunkMost)
				j.buf.add(0, chunk)
				up := newFakeTrUpLink()
				s := newTrSession(j, trVerbatim, nil, 0, "test")
				s.run(up, 0)

				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					s.close(false)
				}()
				go func() {
					defer wg.Done()
					<-start
					s.close(true)
				}()
				close(start)
				wg.Wait()
				synctest.Wait()

				if up.writeOverlap.Load() {
					t.Fatal("concurrent writes detected during simultaneous close(false)/close(true)")
				}
				if up.closeCnt.Load() != 0 {
					t.Fatalf("expected teardown via CloseNow, saw %d up.Close calls", up.closeCnt.Load())
				}
				if up.closeNowCnt.Load() == 0 {
					t.Fatal("expected CloseNow to be called during simultaneous close")
				}
				select {
				case <-s.gone:
				default:
					t.Fatal("s.gone not closed")
				}
				select {
				case <-j.reads:
				default:
				}
				j.cancel()
				synctest.Wait()
			})
		}
	})
}
