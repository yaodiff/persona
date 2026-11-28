package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Opt-in end-to-end test against a real OpenAI-compatible streaming TTS server (#17843).
// Skipped unless PERSONA_VOICE_TTS_E2E_URL is set (for example http://127.0.0.1:18880).
//
// Runs a full call through Plugin against a mock text-only GPT-Live upstream and the real TTS server:
//   - GET /voices through the extension's HTTP handler;
//   - a Chinese answer with built-in voice zf_xiaoxiao;
//   - an English answer with built-in voice af_heart;
//   - an energy barge-in during playback of a multi-sentence answer after upstream evTurnEnd.
//
// Saves app-side WAV files under /tmp and logs first-text-delta to first-audio-frame timing, RMS,
// frame sizes, and duration.
func TestTTSRealSynthesisE2E(t *testing.T) {
	ttsURL := strings.TrimSpace(os.Getenv("PERSONA_VOICE_TTS_E2E_URL"))
	if ttsURL == "" {
		t.Skip("set PERSONA_VOICE_TTS_E2E_URL to run real TTS synthesis E2E")
	}

	const (
		zhVoice = "zf_xiaoxiao"
		enVoice = "af_heart"
		zhText  = "你好，今天的会议纪要已经整理好了，我们下午三点继续讨论。"
		enText  = "Hello! Your calendar is clear this afternoon, and the build finished with all tests passing."
		longEn  = "First, let me walk through every detail of the quarterly architecture review so nothing is missed. " +
			"Second, the storage pipeline migrated cleanly to the new schema with zero downtime across all replicas. " +
			"Third, latency dropped by forty percent once the streaming cache warmed up under peak traffic. " +
			"Fourth, we should review the remaining rollout milestones tomorrow morning before enabling the final flag."
	)

	hub := agentHub(t)
	var upMu sync.Mutex
	var upConn *websocket.Conn
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			upMu.Lock()
			upConn = c
			upMu.Unlock()
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{
				"audio":             map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 16000}},
				"output_modalities": []string{"text"},
			}})
		}
	})

	pl := testPlugin(t, Config{
		Providers:       []Provider{{ID: "rt-tts-e2e", Dialect: "live", URL: up.url(), Models: []string{"m1"}, InputRate: 16000}},
		TTSURL:          ttsURL,
		TTSModel:        "kokoro",
		TTSDefaultVoice: enVoice,
	}, hub.server(t))
	e2eMemoKey := "rt-tts-e2e\x00" + up.url()
	trMemos.Delete(e2eMemoKey)
	t.Cleanup(func() { trMemos.Delete(e2eMemoKey) })

	// 1. Check GET /voices on the extension handler against the real TTS server.
	extSrv := httptest.NewServer(pl.Handler())
	defer extSrv.Close()
	resp, err := http.Get(extSrv.URL + "/voices")
	if err != nil {
		t.Fatalf("GET /voices: %v", err)
	}
	var voicesResp struct {
		Voices  []VoiceChoice `json:"voices"`
		Default string        `json:"default"`
		Stale   bool          `json:"stale"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&voicesResp); err != nil {
		resp.Body.Close()
		t.Fatalf("decode /voices: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || voicesResp.Stale {
		t.Fatalf("GET /voices status=%d stale=%v count=%d", resp.StatusCode, voicesResp.Stale, len(voicesResp.Voices))
	}
	hasZH := slices.ContainsFunc(voicesResp.Voices, func(v VoiceChoice) bool { return v.ID == zhVoice })
	hasEN := slices.ContainsFunc(voicesResp.Voices, func(v VoiceChoice) bool { return v.ID == enVoice })
	if !hasZH || !hasEN {
		t.Fatalf("GET /voices missing %s or %s (got %d voices, default=%q)", zhVoice, enVoice, len(voicesResp.Voices), voicesResp.Default)
	}
	t.Logf("GET /voices: HTTP %d, stale=%v, count=%d, default=%q (has %s and %s)",
		resp.StatusCode, voicesResp.Stale, len(voicesResp.Voices), voicesResp.Default, zhVoice, enVoice)

	type wireEvent struct {
		kind string // "audio" | "reply:start" | "reply:end" | "clear" | other
		pcm  []byte
		at   time.Time
		msg  map[string]any
	}

	dialRaw := func(t *testing.T, voice string) (*websocket.Conn, <-chan wireEvent) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		wsURL := "ws" + strings.TrimPrefix(extSrv.URL, "http") + "/ws"
		c, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			t.Fatalf("dial /ws: %v", err)
		}
		t.Cleanup(func() { _ = c.CloseNow() })
		start := boundStart("a1", map[string]any{"voice": voice})
		if err := writeJSON(ctx, c, start); err != nil {
			t.Fatalf("write start: %v", err)
		}
		evCh := make(chan wireEvent, 4096)
		go func() {
			defer close(evCh)
			for {
				typ, b, err := c.Read(context.Background())
				if err != nil {
					return
				}
				now := time.Now()
				if typ == websocket.MessageBinary {
					evCh <- wireEvent{kind: "audio", pcm: b, at: now}
					continue
				}
				var f map[string]any
				if json.Unmarshal(b, &f) != nil {
					continue
				}
				kind := stringField(f, "type")
				if kind == "reply" {
					kind = "reply:" + stringField(f, "state")
				}
				evCh <- wireEvent{kind: kind, at: now, msg: f}
			}
		}()

		// Wait for ready and verify 24000 Hz + requested voice.
		deadline := time.After(10 * time.Second)
		for {
			select {
			case ev, ok := <-evCh:
				if !ok {
					t.Fatal("ws closed before ready")
				}
				if ev.kind == "ready" {
					if ev.msg["output_rate"] != float64(24000) || ev.msg["voice"] != voice {
						t.Fatalf("ready = %v, want output_rate=24000 voice=%s", ev.msg, voice)
					}
					return c, evCh
				}
			case <-deadline:
				t.Fatal("timed out waiting for ready")
			}
		}
	}

	runSynthesis := func(t *testing.T, label, voice, text, wavPath string) {
		t.Helper()
		wsConn, evCh := dialRaw(t, voice)
		defer func() { _ = wsConn.Close(websocket.StatusNormalClosure, "done") }()

		upMu.Lock()
		uConn := upConn
		upMu.Unlock()

		sentAt := time.Now()
		up.send(uConn, replyLifecycleEvent("r-"+label, "response.created"))
		up.send(uConn, map[string]any{"type": "response.event", "delegation_id": "r-" + label, "event": map[string]any{
			"type": "response.output_text.delta", "delta": text,
		}})
		up.send(uConn, replyLifecycleEvent("r-"+label, "response.completed"))

		var (
			sawStart       bool
			sawEnd         bool
			firstFrameAt   time.Time
			frames         [][]byte
			framesAfterEnd int
		)
		deadline := time.After(45 * time.Second)
		for !sawEnd {
			select {
			case ev, ok := <-evCh:
				if !ok {
					t.Fatalf("%s: ws closed before reply:end (frames=%d)", label, len(frames))
				}
				switch ev.kind {
				case "reply:start":
					if len(frames) > 0 {
						t.Fatalf("%s: audio frame arrived before reply:start", label)
					}
					sawStart = true
				case "audio":
					if !sawStart {
						t.Fatalf("%s: audio frame arrived before reply:start", label)
					}
					if len(ev.pcm) != 960 {
						t.Fatalf("%s: frame %d len=%d, want 960 (20 ms at 24000 Hz)", label, len(frames), len(ev.pcm))
					}
					if firstFrameAt.IsZero() {
						firstFrameAt = ev.at
					}
					frames = append(frames, ev.pcm)
				case "reply:end":
					sawEnd = true
				}
			case <-deadline:
				t.Fatalf("%s: timed out waiting for reply:end (frames=%d)", label, len(frames))
			}
		}
		// Ensure no trailing audio frame arrives after reply:end.
		afterTimer := time.NewTimer(150 * time.Millisecond)
		defer afterTimer.Stop()
	drain:
		for {
			select {
			case ev, ok := <-evCh:
				if !ok {
					break drain
				}
				if ev.kind == "audio" {
					framesAfterEnd++
				}
			case <-afterTimer.C:
				break drain
			}
		}
		if framesAfterEnd > 0 {
			t.Fatalf("%s: %d audio frames arrived after reply:end", label, framesAfterEnd)
		}
		if len(frames) == 0 {
			t.Fatalf("%s: received zero audio frames", label)
		}

		allPCM := bytes.Join(frames, nil)
		rms := math.Sqrt(meanSquare(allPCM))
		dur := pcmDuration(len(allPCM), 24000)
		ttfa := firstFrameAt.Sub(sentAt)
		if rms < 200 {
			t.Fatalf("%s: PCM RMS %.1f is too close to silence (want >= 200)", label, rms)
		}
		if dur < 1500*time.Millisecond || dur > 20*time.Second {
			t.Fatalf("%s: audio duration %v implausible for text (%d frames)", label, dur, len(frames))
		}
		if err := writeWAV16Mono(wavPath, 24000, allPCM); err != nil {
			t.Fatalf("%s: write WAV %s: %v", label, wavPath, err)
		}
		t.Logf("%s (%s): ttfa=%v, frames=%d (all 960 B @ 24000 Hz), dur=%v, rms=%.1f, wav=%s",
			label, voice, ttfa.Round(time.Millisecond), len(frames), dur.Round(time.Millisecond), rms, wavPath)
	}

	// 2. Chinese synthesis (zf_xiaoxiao)
	t.Run("chinese_zf_xiaoxiao", func(t *testing.T) {
		runSynthesis(t, "zh", zhVoice, zhText, "/tmp/persona-tts-e2e-zh.wav")
	})

	// 3. English synthesis (af_heart)
	t.Run("english_af_heart", func(t *testing.T) {
		runSynthesis(t, "en", enVoice, enText, "/tmp/persona-tts-e2e-en.wav")
	})

	// 4. Barge-in during playback of a multi-sentence answer after upstream evTurnEnd.
	t.Run("barge_in_mid_playback", func(t *testing.T) {
		wsConn, evCh := dialRaw(t, enVoice)
		defer func() { _ = wsConn.Close(websocket.StatusNormalClosure, "done") }()

		upMu.Lock()
		uConn := upConn
		upMu.Unlock()

		sentAt := time.Now()
		up.send(uConn, replyLifecycleEvent("r-barge", "response.created"))
		up.send(uConn, map[string]any{"type": "response.event", "delegation_id": "r-barge", "event": map[string]any{
			"type": "response.output_text.delta", "delta": longEn,
		}})
		up.send(uConn, replyLifecycleEvent("r-barge", "response.completed"))

		// Wait until TTS is actively streaming frames (at least 10 frames = 200 ms received) while the
		// remaining sentences are still being synthesized or queued.
		var (
			frames       [][]byte
			firstFrameAt time.Time
			sawStart     bool
		)
		deadline := time.After(30 * time.Second)
		for len(frames) < 10 {
			select {
			case ev, ok := <-evCh:
				if !ok {
					t.Fatal("barge-in: ws closed early")
				}
				switch ev.kind {
				case "reply:start":
					sawStart = true
				case "audio":
					if len(ev.pcm) != 960 {
						t.Fatalf("barge-in: frame len=%d, want 960", len(ev.pcm))
					}
					if firstFrameAt.IsZero() {
						firstFrameAt = ev.at
					}
					frames = append(frames, ev.pcm)
				case "reply:end":
					t.Fatalf("barge-in: reply ended before 10 frames arrived (frames=%d)", len(frames))
				}
			case <-deadline:
				t.Fatalf("barge-in: timed out waiting for initial frames (got %d)", len(frames))
			}
		}
		if !sawStart {
			t.Fatal("barge-in: did not receive reply:start before audio")
		}

		// Send 600 ms (> bargeMin = 500 ms) of voiced 16 kHz mic PCM to trigger energy cut-in.
		voicedFrame := pcmTone(320, 3000) // 20 ms at 16 kHz, RMS 3000
		for range 30 {
			if err := wsConn.Write(context.Background(), websocket.MessageBinary, voicedFrame); err != nil {
				t.Fatalf("barge-in: write mic audio: %v", err)
			}
		}

		var (
			sawClear         bool
			sawEnd           bool
			framesAfterClear int
		)
		cutDeadline := time.After(10 * time.Second)
		for !sawClear {
			select {
			case ev, ok := <-evCh:
				if !ok {
					t.Fatal("barge-in: ws closed before clear")
				}
				switch ev.kind {
				case "audio":
					if len(ev.pcm) != 960 {
						t.Fatalf("barge-in: frame len=%d, want 960", len(ev.pcm))
					}
					frames = append(frames, ev.pcm)
				case "clear":
					sawClear = true
				case "reply:end":
					sawEnd = true
				}
			case <-cutDeadline:
				t.Fatal("barge-in: timed out waiting for clear")
			}
		}

		// After clear, wait briefly to confirm reply:end arrives (if not already closed) and at most 1 frame follows clear.
		afterTimer := time.NewTimer(500 * time.Millisecond)
		defer afterTimer.Stop()
	afterLoop:
		for {
			select {
			case ev, ok := <-evCh:
				if !ok {
					break afterLoop
				}
				switch ev.kind {
				case "audio":
					framesAfterClear++
				case "reply:end":
					sawEnd = true
				}
			case <-afterTimer.C:
				break afterLoop
			}
		}
		if !sawEnd {
			t.Fatal("barge-in: reply:end never arrived")
		}
		if framesAfterClear > 1 {
			t.Fatalf("barge-in: %d frames arrived after clear (want <= 1)", framesAfterClear)
		}

		allPCM := bytes.Join(frames, nil)
		rms := math.Sqrt(meanSquare(allPCM))
		dur := pcmDuration(len(allPCM), 24000)
		wavPath := "/tmp/persona-tts-e2e-bargein.wav"
		if err := writeWAV16Mono(wavPath, 24000, allPCM); err != nil {
			t.Fatalf("barge-in: write WAV: %v", err)
		}
		if rms < 200 {
			t.Fatalf("barge-in: PCM RMS %.1f too close to silence", rms)
		}
		t.Logf("barge-in (%s): ttfa=%v, frames_before_clear=%d, frames_after_clear=%d, dur=%v, rms=%.1f, wav=%s",
			enVoice, firstFrameAt.Sub(sentAt).Round(time.Millisecond), len(frames), framesAfterClear, dur.Round(time.Millisecond), rms, wavPath)
	})
}

func stringField(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func pcmTone(samples int, amp int16) []byte {
	out := make([]byte, samples*2)
	for i := range samples {
		v := amp
		if i%2 == 1 {
			v = -amp
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func writeWAV16Mono(path string, rate int, pcm []byte) error {
	var hdr [44]byte
	dataSize := uint32(len(pcm))
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], 36+dataSize)
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)           // PCM fmt chunk size
	binary.LittleEndian.PutUint16(hdr[20:22], 1)            // PCM format
	binary.LittleEndian.PutUint16(hdr[22:24], 1)            // mono
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(rate)) // sample rate
	binary.LittleEndian.PutUint32(hdr[28:32], uint32(rate*2))
	binary.LittleEndian.PutUint16(hdr[32:34], 2)  // block align
	binary.LittleEndian.PutUint16(hdr[34:36], 16) // bits per sample
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], dataSize)
	var buf bytes.Buffer
	buf.Grow(44 + len(pcm))
	buf.Write(hdr[:])
	buf.Write(pcm)
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
