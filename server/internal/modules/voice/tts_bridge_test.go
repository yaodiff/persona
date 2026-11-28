package voice

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitDone(t *testing.T, ch <-chan ttsDoneEvent, timeout time.Duration) ttsDoneEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for onDone")
		return ttsDoneEvent{}
	}
}

type ttsDoneEvent struct {
	gen       uint64
	cancelled bool
	err       error
}

func TestTTSBridgeOrderedPlaybackAndResampling(t *testing.T) {
	// Server returns 16 kHz PCM tagged with a distinct constant sample per sentence;
	// bridge outputs 24 kHz 20 ms frames (960 B).
	var reqMu sync.Mutex
	var gotInputs []string
	var gotVoices []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ttsSpeechRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		reqMu.Lock()
		gotInputs = append(gotInputs, req.Input)
		gotVoices = append(gotVoices, req.Voice)
		idx := len(gotInputs)
		reqMu.Unlock()

		// 320 samples at 16 kHz = 20 ms -> 480 samples (960 B) at 24 kHz.
		pcm := make([]byte, 320*2)
		val := uint16(int16(idx * 1000))
		for i := range 320 {
			binary.LittleEndian.PutUint16(pcm[i*2:], val)
		}
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		_, _ = w.Write(pcm)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var frames [][]byte
	doneCh := make(chan ttsDoneEvent, 4)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:  srv.URL + "/v1/audio/speech",
			model:      "test-tts",
			voice:      "fallback-voice",
			httpClient: srv.Client(),
		}),
		outputRate: 24000,
		onFrame: func(gen uint64, pcm []byte) {
			mu.Lock()
			defer mu.Unlock()
			frames = append(frames, pcm)
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})
	defer b.Close()

	if b.Active() {
		t.Fatal("bridge should start inactive")
	}

	gen := b.StartTurn("voice-bright")
	if gen == 0 {
		t.Fatal("expected non-zero generation")
	}
	if !b.Active() {
		t.Fatal("bridge should be active after StartTurn")
	}
	b.Feed(gen, "好的，我马上帮你查一下，")
	b.Feed(gen, "结果已经出来了。")
	b.Feed(gen, "还有需要帮忙的吗？")
	b.EndText(gen)

	done := waitDone(t, doneCh, 3*time.Second)
	if done.gen != gen || done.cancelled || done.err != nil {
		t.Fatalf("unexpected done event: %+v", done)
	}
	if b.Active() {
		t.Fatal("bridge should be inactive after turn finishes")
	}

	reqMu.Lock()
	inputs := slices.Clone(gotInputs)
	voices := slices.Clone(gotVoices)
	reqMu.Unlock()

	wantInputs := []string{"好的，我马上帮你查一下，", "结果已经出来了。", "还有需要帮忙的吗？"}
	if !slices.Equal(inputs, wantInputs) {
		t.Fatalf("requests = %v, want %v", inputs, wantInputs)
	}
	for i, v := range voices {
		if v != "voice-bright" {
			t.Fatalf("request %d voice = %q, want voice-bright", i, v)
		}
	}

	mu.Lock()
	gotFrames := slices.Clone(frames)
	mu.Unlock()

	if len(gotFrames) != 3 {
		t.Fatalf("got %d frames, want 3", len(gotFrames))
	}
	for i, fr := range gotFrames {
		if len(fr) != 960 {
			t.Fatalf("frame %d size = %d, want 960", i, len(fr))
		}
		// Check midpoint sample reflects sentence ordering (1000, 2000, 3000).
		mid := int16(binary.LittleEndian.Uint16(fr[480:482]))
		wantVal := int16((i + 1) * 1000)
		if mid != wantVal {
			t.Fatalf("frame %d midpoint sample = %d, want %d (out of order)", i, mid, wantVal)
		}
	}
}

func TestTTSBridgePrefetchOverlapsAndPreservesOrder(t *testing.T) {
	// Piece 1 sends 1 frame and pauses on p1Gate; piece 2 must start arriving at the server
	// while piece 1 is still open, yet piece 2's frames must not be delivered until piece 1 ends.
	p1Started := make(chan struct{}, 1)
	p1Gate := make(chan struct{})
	p2Arrived := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ttsSpeechRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)

		makeTaggedFrame := func(tag int16) []byte {
			f := make([]byte, 640) // 20 ms at 16 kHz
			for i := range 320 {
				binary.LittleEndian.PutUint16(f[i*2:], uint16(tag))
			}
			return f
		}

		if strings.Contains(req.Input, "First") {
			_, _ = w.Write(makeTaggedFrame(111))
			fl.Flush()
			select {
			case p1Started <- struct{}{}:
			default:
			}
			select {
			case <-p1Gate:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(makeTaggedFrame(112))
			fl.Flush()
			return
		}
		// Piece 2: signal arrival, then write a big body (> 64 KiB prefetch buffer) to test bounded memory.
		select {
		case p2Arrived <- struct{}{}:
		default:
		}
		for range 120 { // 120 * 640 B = 76800 B (> 64 KiB)
			select {
			case <-r.Context().Done():
				return
			default:
			}
			if _, err := w.Write(makeTaggedFrame(222)); err != nil {
				return
			}
			fl.Flush()
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var tags []int16
	doneCh := make(chan ttsDoneEvent, 2)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:  srv.URL + "/v1/audio/speech",
			httpClient: srv.Client(),
		}),
		outputRate: 16000,
		onFrame: func(_ uint64, pcm []byte) {
			mu.Lock()
			tags = append(tags, int16(binary.LittleEndian.Uint16(pcm[:2])))
			mu.Unlock()
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})
	defer b.Close()

	gen := b.StartTurn("voice-a")
	b.Feed(gen, "First sentence is here. Second sentence follows right away.")
	b.EndText(gen)

	select {
	case <-p1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for piece 1 start")
	}

	// Prefetch must start piece 2 while piece 1 is still streaming.
	select {
	case <-p2Arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("expected piece 2 to be prefetched while piece 1 streams")
	}

	// Before piece 1 resumes, ZERO frames of piece 2 may have been delivered.
	mu.Lock()
	for _, tag := range tags {
		if tag == 222 {
			mu.Unlock()
			t.Fatal("prefetched piece 2 delivered out of order before piece 1 finished")
		}
	}
	mu.Unlock()

	close(p1Gate)
	done := waitDone(t, doneCh, 3*time.Second)
	if done.cancelled || done.err != nil {
		t.Fatalf("unexpected done: %+v", done)
	}

	mu.Lock()
	gotTags := slices.Clone(tags)
	mu.Unlock()

	if len(gotTags) != 122 {
		t.Fatalf("got %d frames, want 122", len(gotTags))
	}
	if gotTags[0] != 111 || gotTags[1] != 112 || gotTags[2] != 222 || gotTags[121] != 222 {
		t.Fatalf("unexpected frame ordering: first 4 = %v, last = %d", gotTags[:4], gotTags[len(gotTags)-1])
	}
}

func TestTTSBridgeCancelMidStreamWithCallerMutex(t *testing.T) {
	// Simulates conversation.mu: caller holds callMu across StartTurn/Feed/EndText/Cancel,
	// and onFrame/onDone acquire that SAME callMu.
	serverDisconnected := make(chan struct{}, 2)
	got3Frames := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "24000")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)

		// Send 3 frames (960 B each) in odd-byte pieces, then wait until client cancels.
		frame := buildTestPCM(480, 1)
		for range 3 {
			_, _ = w.Write(frame[:479])
			fl.Flush()
			_, _ = w.Write(frame[479:])
			fl.Flush()
		}
		<-r.Context().Done()
		serverDisconnected <- struct{}{}
	}))
	defer srv.Close()

	var callMu sync.Mutex
	var currentGen uint64
	var received int
	var staleAfterCancel int
	var cancelledFlag bool
	doneCh := make(chan ttsDoneEvent, 4)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:  srv.URL + "/v1/audio/speech",
			httpClient: srv.Client(),
		}),
		outputRate: 24000,
		onFrame: func(gen uint64, pcm []byte) {
			callMu.Lock()
			defer callMu.Unlock()
			if len(pcm) != 960 {
				t.Errorf("frame size = %d, want 960", len(pcm))
			}
			if gen != currentGen {
				return
			}
			if cancelledFlag {
				staleAfterCancel++
				return
			}
			received++
			if received == 3 {
				select {
				case got3Frames <- struct{}{}:
				default:
				}
			}
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			callMu.Lock()
			defer callMu.Unlock()
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})
	defer b.Close()

	callMu.Lock()
	gen := b.StartTurn("voice-a")
	currentGen = gen
	b.Feed(gen, "这是一句比较长的测试语音，应该在收到三帧之后立刻被打断。后面还有第二句。")
	b.EndText(gen)
	callMu.Unlock()

	select {
	case <-got3Frames:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for 3 frames")
	}

	// Cancel while holding callMu (exactly as conversation.go does under conversation.mu).
	callMu.Lock()
	b.Cancel()
	currentGen = 0
	cancelledFlag = true
	activeAfterCancel := b.Active()
	callMu.Unlock()

	if activeAfterCancel {
		t.Fatal("Active() must be false immediately after Cancel()")
	}

	// Server must observe disconnect promptly.
	select {
	case <-serverDisconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not see disconnect after Cancel()")
	}

	done := waitDone(t, doneCh, 2*time.Second)
	if done.gen != gen || !done.cancelled || done.err != nil {
		t.Fatalf("expected cancelled done for gen %d, got %+v", gen, done)
	}

	callMu.Lock()
	leaked := staleAfterCancel
	callMu.Unlock()
	if leaked != 0 {
		t.Fatalf("received %d frames after Cancel", leaked)
	}
}

func TestTTSBridgeSkipsFailedPieceAndContinues(t *testing.T) {
	secretKey := "sk-never-leak-this-key"
	var count atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		switch n {
		case 1:
			// Piece 1: 503 JSON error echoing the key (must be scrubbed).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"backend down for ` + secretKey + `"}}`))
		case 2:
			// Piece 2: succeeds with 1 frame at 16 kHz (640 B).
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "16000")
			_, _ = w.Write(buildTestPCM(320, 200))
		case 3:
			// Piece 3: breaks mid-stream.
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "16000")
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buildTestPCM(64, 300))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
		default:
			// Piece 4: succeeds with 1 frame at 16 kHz.
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "16000")
			_, _ = w.Write(buildTestPCM(320, 400))
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var frames [][]byte
	var pieceErrs []error
	doneCh := make(chan ttsDoneEvent, 2)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:  srv.URL + "/v1/audio/speech",
			apiKey:     secretKey,
			httpClient: srv.Client(),
		}),
		outputRate:      16000,
		disablePrefetch: true, // deterministic request sequence for scripted 1..4 failures
		onFrame: func(_ uint64, pcm []byte) {
			mu.Lock()
			frames = append(frames, pcm)
			mu.Unlock()
		},
		onError: func(_ uint64, err error) {
			mu.Lock()
			pieceErrs = append(pieceErrs, err)
			mu.Unlock()
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})
	defer b.Close()

	gen := b.StartTurn("voice-a")
	b.Feed(gen, "First sentence fails with 503. Second sentence plays cleanly. Third sentence breaks mid stream. Fourth sentence finishes the turn.")
	b.EndText(gen)

	done := waitDone(t, doneCh, 3*time.Second)
	if done.gen != gen || done.cancelled {
		t.Fatalf("expected non-cancelled completion, got %+v", done)
	}
	if done.err == nil {
		t.Fatal("expected last piece error on done event")
	}

	mu.Lock()
	gotErrs := slices.Clone(pieceErrs)
	gotFrames := slices.Clone(frames)
	mu.Unlock()

	if len(gotErrs) != 2 {
		t.Fatalf("expected 2 reported piece errors, got %d: %v", len(gotErrs), gotErrs)
	}
	if !strings.Contains(gotErrs[0].Error(), "HTTP 503") {
		t.Fatalf("first error = %v, want HTTP 503", gotErrs[0])
	}
	for _, err := range gotErrs {
		if strings.Contains(err.Error(), secretKey) {
			t.Fatalf("secret key leaked in piece error: %v", err)
		}
	}
	// Piece 2 (320 samples) + Piece 3 partial (64 samples) + Piece 4 (320 samples) = 704 samples -> 2 full + 1 padded = 3 frames of 640 B.
	if len(gotFrames) < 2 {
		t.Fatalf("expected frames from succeeding sentences 2 and 4, got %d", len(gotFrames))
	}
	for i, fr := range gotFrames {
		if len(fr) != 640 {
			t.Fatalf("frame %d size = %d, want 640", i, len(fr))
		}
	}
}

func TestTTSBridgeEmptyTurnAndSupersedingStartTurn(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var req ttsSpeechRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		if strings.Contains(req.Input, "Hold") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buildTestPCM(320, 1))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(buildTestPCM(320, 99))
	}))
	defer srv.Close()

	var mu sync.Mutex
	var frameGens []uint64
	doneCh := make(chan ttsDoneEvent, 8)
	gotHoldFrame := make(chan struct{}, 1)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:  srv.URL + "/v1/audio/speech",
			httpClient: srv.Client(),
		}),
		outputRate: 16000,
		onFrame: func(gen uint64, _ []byte) {
			mu.Lock()
			frameGens = append(frameGens, gen)
			mu.Unlock()
			select {
			case gotHoldFrame <- struct{}{}:
			default:
			}
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})
	defer b.Close()

	// Empty / whitespace-only turn makes zero HTTP requests.
	g0 := b.StartTurn("voice-a")
	b.Feed(g0, "   \n\t ")
	b.EndText(g0)
	d0 := waitDone(t, doneCh, 2*time.Second)
	if d0.gen != g0 || d0.cancelled || d0.err != nil {
		t.Fatalf("empty turn done = %+v", d0)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected 0 HTTP requests for empty turn, got %d", hits.Load())
	}

	// Start turn 1 that holds mid-stream, then supersede with turn 2.
	g1 := b.StartTurn("voice-a")
	b.Feed(g1, "Hold on this stream.")
	select {
	case <-gotHoldFrame:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for turn 1 frame")
	}

	g2 := b.StartTurn("voice-b")
	if g2 <= g1 {
		t.Fatalf("expected increasing gen: g1=%d g2=%d", g1, g2)
	}
	// Late Feed/EndText for old generation must be ignored.
	b.Feed(g1, "Should be ignored.")
	b.EndText(g1)

	b.Feed(g2, "Fresh reply.")
	b.EndText(g2)

	d1 := waitDone(t, doneCh, 3*time.Second)
	d2 := waitDone(t, doneCh, 3*time.Second)
	if d1.gen != g1 || !d1.cancelled {
		t.Fatalf("first done = %+v, want cancelled gen %d", d1, g1)
	}
	if d2.gen != g2 || d2.cancelled || d2.err != nil {
		t.Fatalf("second done = %+v, want completed gen %d", d2, g2)
	}

	// Verify no frame of g1 appeared after any frame of g2.
	mu.Lock()
	gens := slices.Clone(frameGens)
	mu.Unlock()
	sawG2 := false
	for _, g := range gens {
		if g == g2 {
			sawG2 = true
		} else if sawG2 && g == g1 {
			t.Fatalf("old generation %d frame delivered after new generation %d: %v", g1, g2, gens)
		}
	}
	if !sawG2 {
		t.Fatalf("expected frames for gen %d, got %v", g2, gens)
	}
}

func TestTTSBridgeCloseLeavesNoGoroutines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ttsSpeechRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "24000")
		if strings.Contains(req.Input, "stall") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buildTestPCM(480, 1))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(buildTestPCM(480, 2))
	}))
	defer srv.Close()

	// Warm up httptest server pool before taking goroutine baseline.
	warmClient := newTTSClient(ttsClientConfig{
		speechURL:  srv.URL + "/v1/audio/speech",
		httpClient: srv.Client(),
	})
	_ = warmClient.streamPCM(t.Context(), "warm", "v", nil)
	srv.Client().CloseIdleConnections()
	runtime.GC()
	runtime.Gosched()
	baseline := runtime.NumGoroutine()

	gotStallFrame := make(chan struct{}, 4)
	doneCh := make(chan ttsDoneEvent, 16)
	var callMu sync.Mutex

	// Let ttsClient own its own http.Transport to verify Close also tears down idle client connections.
	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL: srv.URL + "/v1/audio/speech",
		}),
		outputRate: 24000,
		onFrame: func(_ uint64, _ []byte) {
			callMu.Lock()
			callMu.Unlock()
			select {
			case gotStallFrame <- struct{}{}:
			default:
			}
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			callMu.Lock()
			callMu.Unlock()
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})

	// Run a rapid burst of StartTurn/Cancel plus a normal turn and an active stalled turn with prefetch.
	for i := range 5 {
		callMu.Lock()
		g := b.StartTurn("voice-a")
		b.Feed(g, fmt.Sprintf("Burst sentence %d.", i))
		b.Cancel()
		callMu.Unlock()
	}

	gNormal := b.StartTurn("voice-a")
	b.Feed(gNormal, "Normal completion test.")
	b.EndText(gNormal)
	for {
		ev := waitDone(t, doneCh, 3*time.Second)
		if ev.gen == gNormal {
			if ev.cancelled || ev.err != nil {
				t.Fatalf("normal turn done = %+v", ev)
			}
			break
		}
	}

	// Drain signal channel before starting stalled turn with a second prefetched sentence.
	for len(gotStallFrame) > 0 {
		<-gotStallFrame
	}
	gStall := b.StartTurn("voice-a")
	b.Feed(gStall, "Please stall right now. Second sentence to prefetch and stall too.")
	b.EndText(gStall)
	select {
	case <-gotStallFrame:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stalled turn frame")
	}

	// Close must abort both active and prefetched requests and wait for all bridge goroutines.
	b.Close()
	// Idempotent second Close and post-Close calls must be safe no-ops.
	b.Close()
	if g := b.StartTurn("voice-a"); g != 0 {
		t.Fatalf("StartTurn after Close = %d, want 0", g)
	}
	b.Feed(gStall, "ignored")
	b.EndText(gStall)
	b.Cancel()
	if b.Active() {
		t.Fatal("Active() must be false after Close()")
	}

	// Close test server connections too so transport teardown completes deterministically.
	srv.CloseClientConnections()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		if runtime.NumGoroutine() <= baseline+1 {
			return
		}
	}
	if after := runtime.NumGoroutine(); after > baseline+1 {
		t.Fatalf("goroutine leak after Close: baseline=%d after=%d", baseline, after)
	}
}

func TestTTSBridgeHeaderTimeoutSkipsPieceAndPlaysNext(t *testing.T) {
	p1Disconnected := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ttsSpeechRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Input, "Never") {
			// Accept connection and request body, never write headers.
			<-r.Context().Done()
			p1Disconnected <- struct{}{}
			return
		}
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		frame := make([]byte, 640)
		for i := range 320 {
			binary.LittleEndian.PutUint16(frame[i*2:], uint16(2222))
		}
		_, _ = w.Write(frame)
	}))
	defer srv.Close()

	baseline := runtime.NumGoroutine()
	var mu sync.Mutex
	var frames [][]byte
	var pieceErrs []error
	doneCh := make(chan ttsDoneEvent, 2)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:     srv.URL + "/v1/audio/speech",
			headerTimeout: 80 * time.Millisecond,
			idleTimeout:   2 * time.Second,
			httpClient:    srv.Client(),
		}),
		outputRate: 16000,
		onFrame: func(_ uint64, pcm []byte) {
			mu.Lock()
			frames = append(frames, pcm)
			mu.Unlock()
		},
		onError: func(_ uint64, err error) {
			mu.Lock()
			pieceErrs = append(pieceErrs, err)
			mu.Unlock()
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})

	gen := b.StartTurn("voice-a")
	b.Feed(gen, "Never answers headers. Second sentence plays cleanly.")
	b.EndText(gen)

	done := waitDone(t, doneCh, 3*time.Second)
	if done.gen != gen || done.cancelled {
		t.Fatalf("expected non-cancelled turn completion, got %+v", done)
	}
	if done.err == nil || !strings.Contains(done.err.Error(), "header timeout") {
		t.Fatalf("done.err = %v, want header timeout", done.err)
	}

	select {
	case <-p1Disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("timed-out piece 1 connection was not closed")
	}

	mu.Lock()
	gotErrs := slices.Clone(pieceErrs)
	gotFrames := slices.Clone(frames)
	mu.Unlock()

	if len(gotErrs) != 1 || !strings.Contains(gotErrs[0].Error(), "header timeout") {
		t.Fatalf("pieceErrs = %v, want 1 header timeout error", gotErrs)
	}
	if len(gotFrames) != 1 || len(gotFrames[0]) != 640 {
		t.Fatalf("got %d frames (want 1 frame of 640 B from piece 2)", len(gotFrames))
	}
	if gotTag := int16(binary.LittleEndian.Uint16(gotFrames[0][:2])); gotTag != 2222 {
		t.Fatalf("frame sample = %d, want 2222 from piece 2", gotTag)
	}

	b.Close()
	srv.CloseClientConnections()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		if runtime.NumGoroutine() <= baseline+1 {
			return
		}
	}
	if after := runtime.NumGoroutine(); after > baseline+1 {
		t.Fatalf("goroutine leak after header timeout + Close: baseline=%d after=%d", baseline, after)
	}
}

func TestTTSBridgeIdleTimeoutKeepsFramesAndPlaysNext(t *testing.T) {
	p1Disconnected := make(chan struct{}, 1)
	makeTaggedFrame := func(tag int16) []byte {
		f := make([]byte, 640) // 20 ms at 16 kHz
		for i := range 320 {
			binary.LittleEndian.PutUint16(f[i*2:], uint16(tag))
		}
		return f
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ttsSpeechRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)

		if strings.Contains(req.Input, "Stalls") {
			// Emit 2 full frames then stall until client closes on idle timeout.
			_, _ = w.Write(makeTaggedFrame(1111))
			_, _ = w.Write(makeTaggedFrame(1111))
			fl.Flush()
			<-r.Context().Done()
			p1Disconnected <- struct{}{}
			return
		}
		_, _ = w.Write(makeTaggedFrame(2222))
		fl.Flush()
	}))
	defer srv.Close()

	baseline := runtime.NumGoroutine()
	var mu sync.Mutex
	var tags []int16
	var pieceErrs []error
	doneCh := make(chan ttsDoneEvent, 2)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:     srv.URL + "/v1/audio/speech",
			headerTimeout: 2 * time.Second,
			idleTimeout:   80 * time.Millisecond,
			httpClient:    srv.Client(),
		}),
		outputRate: 16000,
		onFrame: func(_ uint64, pcm []byte) {
			if len(pcm) != 640 {
				t.Errorf("frame len = %d, want 640", len(pcm))
			}
			mu.Lock()
			tags = append(tags, int16(binary.LittleEndian.Uint16(pcm[:2])))
			mu.Unlock()
		},
		onError: func(_ uint64, err error) {
			mu.Lock()
			pieceErrs = append(pieceErrs, err)
			mu.Unlock()
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})

	gen := b.StartTurn("voice-a")
	b.Feed(gen, "Stalls halfway through streaming. Second sentence still plays.")
	b.EndText(gen)

	done := waitDone(t, doneCh, 3*time.Second)
	if done.gen != gen || done.cancelled {
		t.Fatalf("expected non-cancelled turn completion, got %+v", done)
	}
	if done.err == nil || !strings.Contains(done.err.Error(), "idle timeout") {
		t.Fatalf("done.err = %v, want idle timeout", done.err)
	}

	select {
	case <-p1Disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled piece 1 connection was not closed")
	}

	mu.Lock()
	gotErrs := slices.Clone(pieceErrs)
	gotTags := slices.Clone(tags)
	mu.Unlock()

	if len(gotErrs) != 1 || !strings.Contains(gotErrs[0].Error(), "idle timeout") {
		t.Fatalf("pieceErrs = %v, want 1 idle timeout error", gotErrs)
	}
	wantTags := []int16{1111, 1111, 2222}
	if !slices.Equal(gotTags, wantTags) {
		t.Fatalf("frame tags = %v, want %v (frames before stall kept, next piece played)", gotTags, wantTags)
	}

	b.Close()
	srv.CloseClientConnections()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		if runtime.NumGoroutine() <= baseline+1 {
			return
		}
	}
	if after := runtime.NumGoroutine(); after > baseline+1 {
		t.Fatalf("goroutine leak after idle timeout + Close: baseline=%d after=%d", baseline, after)
	}
}

func TestTTSBridgeLogsErrorsWhenOnErrorUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer srv.Close()

	var logMu sync.Mutex
	var logBuf bytes.Buffer
	doneCh := make(chan ttsDoneEvent, 2)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:  srv.URL + "/v1/audio/speech",
			httpClient: srv.Client(),
		}),
		outputRate: 16000,
		logf: func(format string, args ...any) {
			logMu.Lock()
			fmt.Fprintf(&logBuf, format+"\n", args...)
			logMu.Unlock()
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})
	defer b.Close()

	g := b.StartTurn("voice-a")
	b.Feed(g, "Trigger error log.")
	b.EndText(g)

	done := waitDone(t, doneCh, 2*time.Second)
	if done.cancelled || done.err == nil {
		t.Fatalf("expected error on done, got %+v", done)
	}
	logMu.Lock()
	logged := logBuf.String()
	logMu.Unlock()
	if !strings.Contains(logged, "HTTP 502") {
		t.Fatalf("expected HTTP 502 in log, got %q", logged)
	}
}

func TestTTSBridgeEnqueueTurnQueuesInOrderAndCancelsAll(t *testing.T) {
	// Piece 1 blocks until released so Turn 2 and Turn 3 are queued while Turn 1 is active.
	p1Started := make(chan struct{})
	p1Release := make(chan struct{})
	p3Started := make(chan struct{})
	p3Canceled := make(chan struct{})

	makeFrame := func(rate int, val int16) []byte {
		samples := rate / 50
		buf := make([]byte, samples*2)
		for i := range samples {
			binary.LittleEndian.PutUint16(buf[i*2:], uint16(val))
		}
		return buf
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		flusher, _ := w.(http.Flusher)

		switch {
		case strings.Contains(req.Input, "first"):
			close(p1Started)
			<-p1Release
			_, _ = w.Write(makeFrame(16000, 1001))
		case strings.Contains(req.Input, "second"):
			_, _ = w.Write(makeFrame(16000, 2002))
		case strings.Contains(req.Input, "third"):
			close(p3Started)
			_, _ = w.Write(makeFrame(16000, 3003))
			if flusher != nil {
				flusher.Flush()
			}
			<-r.Context().Done()
			close(p3Canceled)
			return
		default:
			_, _ = w.Write(makeFrame(16000, 4004))
		}
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var events []string
	doneCh := make(chan ttsDoneEvent, 8)

	b := newTTSBridge(ttsBridgeConfig{
		client: newTTSClient(ttsClientConfig{
			speechURL:  srv.URL + "/v1/audio/speech",
			httpClient: srv.Client(),
		}),
		outputRate: 16000,
		onStart: func(gen uint64) {
			mu.Lock()
			events = append(events, fmt.Sprintf("start:%d", gen))
			mu.Unlock()
		},
		onFrame: func(gen uint64, frame []byte) {
			sample := int16(binary.LittleEndian.Uint16(frame[:2]))
			mu.Lock()
			events = append(events, fmt.Sprintf("frame:%d:%d", gen, sample))
			mu.Unlock()
		},
		onDone: func(gen uint64, cancelled bool, err error) {
			mu.Lock()
			events = append(events, fmt.Sprintf("done:%d:%v", gen, cancelled))
			mu.Unlock()
			doneCh <- ttsDoneEvent{gen: gen, cancelled: cancelled, err: err}
		},
	})
	defer b.Close()

	g1 := b.EnqueueTurn("voice-a")
	b.Feed(g1, "first reply.")
	b.EndText(g1)

	select {
	case <-p1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn 1 synthesis")
	}

	// Enqueue turn 2 while turn 1 is still active: must queue without cancelling turn 1.
	g2 := b.EnqueueTurn("voice-a")
	b.Feed(g2, "second reply.")
	b.EndText(g2)

	if !b.Active() {
		t.Fatal("bridge should be active while turn 1 is running and turn 2 is queued")
	}

	close(p1Release)

	d1 := waitDone(t, doneCh, 2*time.Second)
	d2 := waitDone(t, doneCh, 2*time.Second)
	if d1.gen != g1 || d1.cancelled || d1.err != nil {
		t.Fatalf("turn 1 done = %+v, want gen=%d cancelled=false err=nil", d1, g1)
	}
	if d2.gen != g2 || d2.cancelled || d2.err != nil {
		t.Fatalf("turn 2 done = %+v, want gen=%d cancelled=false err=nil", d2, g2)
	}

	mu.Lock()
	gotEvents := slices.Clone(events)
	mu.Unlock()
	wantEvents := []string{
		fmt.Sprintf("start:%d", g1),
		fmt.Sprintf("frame:%d:1001", g1),
		fmt.Sprintf("done:%d:false", g1),
		fmt.Sprintf("start:%d", g2),
		fmt.Sprintf("frame:%d:2002", g2),
		fmt.Sprintf("done:%d:false", g2),
	}
	if !slices.Equal(gotEvents, wantEvents) {
		t.Fatalf("events = %v, want %v", gotEvents, wantEvents)
	}

	// Now start turn 3, queue turn 4 behind it, then Cancel(): both must cancel and Active() becomes false.
	g3 := b.EnqueueTurn("voice-a")
	b.Feed(g3, "third reply.")
	b.EndText(g3)

	select {
	case <-p3Started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn 3 synthesis")
	}

	g4 := b.EnqueueTurn("voice-a")
	b.Feed(g4, "fourth reply.")
	b.EndText(g4)

	b.Cancel()

	select {
	case <-p3Canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("turn 3 HTTP request was not canceled on Cancel()")
	}

	d3 := waitDone(t, doneCh, 2*time.Second)
	d4 := waitDone(t, doneCh, 2*time.Second)
	if d3.gen != g3 || !d3.cancelled {
		t.Fatalf("turn 3 done = %+v, want gen=%d cancelled=true", d3, g3)
	}
	if d4.gen != g4 || !d4.cancelled {
		t.Fatalf("turn 4 done = %+v, want gen=%d cancelled=true", d4, g4)
	}
	if b.Active() {
		t.Fatal("bridge should not be active after Cancel()")
	}
}
