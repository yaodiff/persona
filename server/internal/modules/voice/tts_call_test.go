package voice

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// taggedPCM returns a mono PCM16 LE buffer of n samples where every sample equals tag.
func taggedPCM(samples int, tag int16) []byte {
	b := make([]byte, samples*2)
	u := uint16(tag)
	for i := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], u)
	}
	return b
}

// mockTTSServer serves /v1/audio/voices and /v1/audio/speech for call-integration tests.
type mockTTSServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	voices   string // JSON body for GET /v1/audio/voices; empty status!=200 uses voiceStatus
	vStatus  int
	requests []ttsSpeechRequest
	auths    []string
	onSpeech func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest)
}

func newMockTTSServer(t *testing.T, voicesJSON string, onSpeech func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest)) *mockTTSServer {
	t.Helper()
	m := &mockTTSServer{voices: voicesJSON, vStatus: http.StatusOK, onSpeech: onSpeech}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/audio/voices", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		status, body := m.vStatus, m.voices
		m.mu.Unlock()
		if status != http.StatusOK {
			http.Error(w, "no voices", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("POST /v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		var req ttsSpeechRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.requests = append(m.requests, req)
		m.auths = append(m.auths, r.Header.Get("Authorization"))
		fn := m.onSpeech
		m.mu.Unlock()
		if fn != nil {
			fn(w, r, req)
			return
		}
		// Default: 40 ms (2 frames) of tagged 24 kHz PCM.
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "24000")
		_, _ = w.Write(taggedPCM(960, 1234))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockTTSServer) recorded() []ttsSpeechRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

func (m *mockTTSServer) lastAuth() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.auths) == 0 {
		return ""
	}
	return m.auths[len(m.auths)-1]
}

var ttsTestProviderSeq atomic.Uint64

// ttsPlugin builds a voice Plugin configured with a live upstream and a streaming TTS endpoint.
// Uses a distinct provider ID and clears its trMemos entry on cleanup so ephemeral loopback port
// reuse across subtests never inherits another test's cached text-only refusal.
func ttsPlugin(t *testing.T, up *fakeUpstream, ttsSrv *mockTTSServer, hub *httptest.Server, edit func(*Config)) *Plugin {
	t.Helper()
	id := fmt.Sprintf("rt-tts-%d", ttsTestProviderSeq.Add(1))
	cfg := Config{
		Providers:       []Provider{{ID: id, URL: up.url(), Dialect: DialectLive, Key: "k", Models: []string{"m1"}, Tools: new(false)}},
		TTSURL:          ttsSrv.srv.URL,
		TTSModel:        "kokoro-test",
		TTSDefaultVoice: "af_heart",
		TTSKey:          "sk-tts-secret-99",
	}
	if edit != nil {
		edit(&cfg)
	}
	for _, p := range cfg.Providers {
		key := p.ID + "\x00" + p.URL
		trMemos.Delete(key)
		t.Cleanup(func() { trMemos.Delete(key) })
	}
	return testPlugin(t, cfg, hub)
}

// Text-only call with TTS configured: requests output_modalities ["text"] without an upstream voice,
// announces output_rate 24000 (or tts_sample_rate when 16000/24000), delays reply{end} until the
// TTS bridge finishes speaking (not at the upstream's earlier evTurnEnd), resamples endpoint PCM to
// 20 ms frames, preserves captions, and logs "tts audio" without leaking secrets or speech words.
func TestTTSCallTextOnlyEndToEnd(t *testing.T) {
	const secretWords = "这是要朗读的秘密回答。"
	ttsGate := make(chan struct{})
	ttsHit := make(chan struct{}, 1)

	ttsSrv := newMockTTSServer(t, `{"voices":["af_heart","zf_xiaoxiao"],"default":"af_heart"}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
		select {
		case ttsHit <- struct{}{}:
		default:
		}
		select {
		case <-ttsGate:
		case <-r.Context().Done():
			return
		}
		// Endpoint streams 16 kHz PCM (640 samples = 40 ms) -> bridge must resample to 24 kHz (2 frames of 960 B).
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		_, _ = w.Write(taggedPCM(640, 2200))
	})

	hub := agentHub(t)
	var upConn *websocket.Conn
	var upMu sync.Mutex
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

	pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", map[string]any{"voice": "zf_xiaoxiao"}))

	ready := a.until(ofType("ready"))
	if ready["output_rate"] != float64(24000) || ready["voice"] != "zf_xiaoxiao" {
		t.Fatalf("ready = %v, want output_rate=24000 voice=zf_xiaoxiao", ready)
	}
	sess := up.next("session.start")["session"].(map[string]any)
	if mods, _ := sess["output_modalities"].([]any); len(mods) != 1 || mods[0] != "text" {
		t.Fatalf("upstream output_modalities = %v, want [text]", sess["output_modalities"])
	}
	if audio, _ := sess["audio"].(map[string]any); audio["output"] != nil {
		t.Fatalf("upstream session.start sent audio.output when TTS is on: %v", audio["output"])
	}

	// Upstream finishes the entire text reply before TTS finishes synthesizing.
	upMu.Lock()
	c := upConn
	upMu.Unlock()
	up.send(c, replyLifecycleEvent("r1", "response.created"))
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
		"type": "response.output_text.delta", "delta": secretWords,
	}})
	up.send(c, replyLifecycleEvent("r1", "response.completed"))

	// Wait until TTS received the piece, captions finalized, and reply{start} arrived (in any order).
	// reply{end} must NOT arrive yet at upstream evTurnEnd before TTS finishes speaking.
	var gotCaption, gotReplyStart bool
	for !gotCaption || !gotReplyStart {
		select {
		case f := <-a.frames:
			if finalLine("assistant", secretWords)(f) {
				gotCaption = true
			} else if f["type"] == "reply" {
				if f["state"] != replyStart {
					t.Fatalf("unexpected reply frame before TTS done: %v", f)
				}
				gotReplyStart = true
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for caption=%v reply:start=%v", gotCaption, gotReplyStart)
		}
	}
	select {
	case <-ttsHit:
	case <-time.After(5 * time.Second):
		t.Fatal("TTS endpoint was never called")
	}
	select {
	case f := <-a.frames:
		if f["type"] == "reply" && f["state"] == replyEnd {
			t.Fatal("reply:end arrived at upstream evTurnEnd before TTS finished speaking")
		}
	case <-time.After(120 * time.Millisecond):
	}

	// Release TTS audio; collect 20 ms 24 kHz frames and confirm reply{end} arrives after the last frame.
	close(ttsGate)
	if f := a.until(ofType("reply")); f["state"] != replyEnd {
		t.Fatalf("expected reply:end after TTS done, got %v", f)
	}
	var frames [][]byte
	for len(a.audio) > 0 {
		frames = append(frames, <-a.audio)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d audio frames, want 2 (40 ms at 24 kHz)", len(frames))
	}
	for i, fr := range frames {
		if len(fr) != 960 {
			t.Fatalf("frame %d len = %d, want 960 (20 ms at 24 kHz)", i, len(fr))
		}
		mid := int16(binary.LittleEndian.Uint16(fr[480:482]))
		if mid < 2100 || mid > 2300 {
			t.Fatalf("frame %d sample = %d, want ~2200", i, mid)
		}
	}

	reqs := ttsSrv.recorded()
	if len(reqs) != 1 || reqs[0].Model != "kokoro-test" || reqs[0].Voice != "zf_xiaoxiao" || reqs[0].ResponseFormat != "pcm" || reqs[0].Input != secretWords {
		t.Fatalf("tts requests = %+v", reqs)
	}
	if auth := ttsSrv.lastAuth(); auth != "Bearer sk-tts-secret-99" {
		t.Fatalf("tts Authorization = %q", auth)
	}

	logs.wait(`call 1: tts audio (voice "zf_xiaoxiao", 24000 Hz): 40ms in`, "2 chunks of 20ms", "ttfa", ", over")
	_ = a.c.Close(websocket.StatusNormalClosure, "bye")
	logs.wait("call 1: conversation closed")
	if strings.Contains(logs.String(), "upstream audio") {
		t.Fatalf("TTS call logged upstream audio instead of tts audio:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), secretWords) || strings.Contains(logs.String(), "sk-tts-secret-99") {
		t.Fatalf("call log leaked speech words or TTS key:\n%s", logs.String())
	}
}

// Output rate rule: tts_sample_rate of 16000 keeps 16000 Hz (640 B frames); any other rate (e.g. 22050)
// announces 24000 Hz and resamples endpoint PCM to 24000 Hz (960 B frames).
func TestTTSCallOutputRatePolicy(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cfgRate        int
		headerRate     string
		wantOutRate    int
		wantFrameBytes int
	}{
		{"default 24000", 0, "24000", 24000, 960},
		{"explicit 16000", 16000, "16000", 16000, 640},
		{"explicit 24000", 24000, "24000", 24000, 960},
		{"22050 resampled to 24000", 22050, "", 24000, 960},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, _ *http.Request, _ ttsSpeechRequest) {
				w.Header().Set("Content-Type", "audio/pcm")
				if tc.headerRate != "" {
					w.Header().Set("X-Audio-Sample-Rate", tc.headerRate)
				}
				srcRate := tc.cfgRate
				if srcRate == 0 {
					srcRate = 24000
				}
				// 40 ms at srcRate.
				_, _ = w.Write(taggedPCM(srcRate*40/1000, 1500))
			})
			hub := agentHub(t)
			up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
					u.send(c, replyLifecycleEvent("r1", "response.created"))
					u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Hello world."}})
					u.send(c, replyLifecycleEvent("r1", "response.completed"))
				}
			})
			pl := ttsPlugin(t, up, ttsSrv, hub.server(t), func(c *Config) { c.TTSSampleRate = tc.cfgRate })
			a := dialApp(t, pl, boundStart("a1", nil))
			ready := a.until(ofType("ready"))
			if ready["output_rate"] != float64(tc.wantOutRate) {
				t.Fatalf("ready.output_rate = %v, want %d", ready["output_rate"], tc.wantOutRate)
			}
			a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })
			if len(a.audio) == 0 {
				t.Fatal("expected TTS audio frames")
			}
			for len(a.audio) > 0 {
				fr := <-a.audio
				if len(fr) != tc.wantFrameBytes {
					t.Fatalf("frame len = %d, want %d", len(fr), tc.wantFrameBytes)
				}
			}
		})
	}
}

// Voice resolution: honors an app voice listed by the TTS endpoint or matching tts_default_voice;
// otherwise falls back to tts_default_voice, list default, first listed voice, or empty (logged once).
// Never sends a stale realtime voice id to the TTS endpoint.
func TestTTSCallVoiceResolution(t *testing.T) {
	cases := []struct {
		name         string
		voicesStatus int
		voicesJSON   string
		defaultVoice string
		appVoice     string
		wantVoice    string
		wantEmptyLog bool
	}{
		{"chosen in list", http.StatusOK, `{"voices":["af_heart","zf_xiaoxiao"],"default":"af_heart"}`, "af_heart", "zf_xiaoxiao", "zf_xiaoxiao", false},
		{"chosen equals tts_default_voice without list", http.StatusNotFound, "", "custom_v", "custom_v", "custom_v", false},
		{"stale realtime voice falls back to tts_default_voice", http.StatusOK, `{"voices":["af_heart","zf_xiaoxiao"],"default":"zf_xiaoxiao"}`, "af_heart", "marin", "af_heart", false},
		{"stale realtime voice falls back to list default", http.StatusOK, `{"voices":["af_heart","zf_xiaoxiao"],"default":"zf_xiaoxiao"}`, "", "cove", "zf_xiaoxiao", false},
		{"stale realtime voice falls back to first listed", http.StatusOK, `{"voices":["bf_emma","zf_xiaoxiao"]}`, "", "alloy", "bf_emma", false},
		{"no voices and no default logs once and sends empty", http.StatusNotFound, "", "", "marin", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ttsSrv := newMockTTSServer(t, tc.voicesJSON, nil)
			ttsSrv.mu.Lock()
			ttsSrv.vStatus = tc.voicesStatus
			ttsSrv.mu.Unlock()

			hub := agentHub(t)
			up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
					u.send(c, replyLifecycleEvent("r1", "response.created"))
					u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Testing voice selection."}})
					u.send(c, replyLifecycleEvent("r1", "response.completed"))
				}
			})
			pl := ttsPlugin(t, up, ttsSrv, hub.server(t), func(c *Config) { c.TTSDefaultVoice = tc.defaultVoice })
			logs := captureLog(t, pl)
			start := boundStart("a1", nil)
			if tc.appVoice != "" {
				start["voice"] = tc.appVoice
			}
			a := dialApp(t, pl, start)
			ready := a.until(ofType("ready"))
			wantReadyVoice := any(tc.wantVoice)
			if tc.wantVoice == "" {
				wantReadyVoice = nil
			}
			if ready["voice"] != wantReadyVoice {
				t.Fatalf("ready.voice = %v, want %v", ready["voice"], wantReadyVoice)
			}
			a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })
			reqs := ttsSrv.recorded()
			if len(reqs) != 1 || reqs[0].Voice != tc.wantVoice {
				t.Fatalf("tts requests = %+v, want voice %q", reqs, tc.wantVoice)
			}
			if tc.wantEmptyLog {
				logs.wait("tts: no voice configured or listed; sending empty voice")
				if n := strings.Count(logs.String(), "sending empty voice"); n != 1 {
					t.Fatalf("expected empty-voice warning logged once, got %d", n)
				}
			}
		})
	}
}

// Refusal fallback: when upstream refuses output_modalities and streams its own audio alongside both
// text and transcript deltas, upstream audio never reaches the app and TTS speaks only the transcript
// deltas (never both). In a granted text-only session that also emits transcript deltas, TTS speaks
// only the text deltas (never both).
func TestTTSCallRefusalFallbackAndNoDoubleSpeak(t *testing.T) {
	t.Run("refusal fallback drops upstream audio and speaks transcript only", func(t *testing.T) {
		upstreamMarker := taggedPCM(480, 7777) // must NEVER reach the app
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, _ *http.Request, _ ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			_, _ = w.Write(taggedPCM(480, 3333))
		})
		hub := agentHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			if _, asked := sess["output_modalities"]; asked {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{
					"code": "unknown_parameter", "param": "session.output_modalities", "message": "not supported",
				}})
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			u.send(c, replyLifecycleEvent("r1", "response.created"))
			// Service sends both text and transcript deltas, plus its own upstream audio.
			u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
				"type": "response.output_text.delta", "delta": "Text version should not be spoken.",
			}})
			u.send(c, map[string]any{"type": "session.output_audio.delta", "delta": b64(upstreamMarker)})
			u.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": "Transcript version speaks once."})
			u.send(c, replyLifecycleEvent("r1", "response.completed"))
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		a := dialApp(t, pl, boundStart("a1", nil))
		ready := a.until(ofType("ready"))
		if ready["output_rate"] != float64(24000) {
			t.Fatalf("ready.output_rate = %v, want 24000", ready["output_rate"])
		}
		a.until(finalLine("assistant", "Transcript version speaks once."))
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })

		reqs := ttsSrv.recorded()
		if len(reqs) != 1 || reqs[0].Input != "Transcript version speaks once." {
			t.Fatalf("TTS requests = %+v, want only transcript delta once", reqs)
		}
		if len(a.audio) != 1 {
			t.Fatalf("got %d audio frames, want 1 TTS frame", len(a.audio))
		}
		fr := <-a.audio
		if bytes.Equal(fr, upstreamMarker) {
			t.Fatal("upstream audio leaked to app in refusal fallback")
		}
		if tag := int16(binary.LittleEndian.Uint16(fr[:2])); tag != 3333 {
			t.Fatalf("app audio sample = %d, want 3333 (TTS audio)", tag)
		}
	})

	t.Run("text-only session speaks text deltas only when transcript also arrives", func(t *testing.T) {
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, nil)
		hub := agentHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
			u.send(c, replyLifecycleEvent("r1", "response.created"))
			u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
				"type": "response.output_text.delta", "delta": "Text delta speaks once.",
			}})
			u.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": "Duplicate transcript delta."})
			u.send(c, replyLifecycleEvent("r1", "response.completed"))
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })

		reqs := ttsSrv.recorded()
		if len(reqs) != 1 || reqs[0].Input != "Text delta speaks once." {
			t.Fatalf("TTS requests = %+v, want only text delta once", reqs)
		}
	})
}

// Consecutive non-interruption replies queue in the TTS bridge so Reply 1 finishes speaking before
// Reply 2 begins, with strictly paired reply{start}/reply{end} boundaries. A superseded
// response.incomplete for the already-completed Reply 1 does not cut Reply 1 off.
func TestTTSCallConsecutiveRepliesQueue(t *testing.T) {
	r1Gate := make(chan struct{})
	r1Started := make(chan struct{}, 1)

	ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "24000")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		if strings.Contains(req.Input, "First reply") {
			_, _ = w.Write(taggedPCM(480, 1111))
			fl.Flush()
			select {
			case r1Started <- struct{}{}:
			default:
			}
			select {
			case <-r1Gate:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(taggedPCM(480, 1112))
			fl.Flush()
			return
		}
		_, _ = w.Write(taggedPCM(480, 2222))
		fl.Flush()
	})

	hub := agentHub(t)
	var upConn *websocket.Conn
	var upMu sync.Mutex
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			upMu.Lock()
			upConn = c
			upMu.Unlock()
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
		}
	})
	pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))

	upMu.Lock()
	c := upConn
	upMu.Unlock()

	// Reply 1 streams and finishes text on upstream, blocking mid-TTS.
	up.send(c, replyLifecycleEvent("r1", "response.created"))
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "First reply plays out."}})
	up.send(c, replyLifecycleEvent("r1", "response.completed"))
	select {
	case <-r1Started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Reply 1 TTS start")
	}

	// Superseded incomplete for already-completed r1 + Reply 2 arriving while Reply 1 still plays.
	up.send(c, replyLifecycleEvent("r1", "response.incomplete"))
	up.send(c, replyLifecycleEvent("r2", "response.created"))
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{"type": "response.output_text.delta", "delta": "Second reply waits."}})
	up.send(c, replyLifecycleEvent("r2", "response.completed"))

	var seq []string
	for {
		f := a.until(func(f map[string]any) bool {
			return f["type"] != "transcript" || finalLine("assistant", "Second reply waits.")(f)
		})
		if f["type"] != "transcript" {
			seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
			continue
		}
		break
	}

	// Release Reply 1 and read remaining non-transcript frames through Reply 2 end.
	close(r1Gate)
	for len(seq) < 4 {
		f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
		seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
	}
	wantSeq := []string{"reply:start", "reply:end", "reply:start", "reply:end"}
	if !slices.Equal(seq, wantSeq) {
		t.Fatalf("frame sequence = %v, want %v", seq, wantSeq)
	}

	var tags []int16
	for len(a.audio) > 0 {
		fr := <-a.audio
		tags = append(tags, int16(binary.LittleEndian.Uint16(fr[:2])))
	}
	if !slices.Equal(tags, []int16{1111, 1112, 2222}) {
		t.Fatalf("audio frame tags = %v, want [1111 1112 2222]", tags)
	}
}

// Cancellation via upstream evInterrupted, app interrupt, upstream reconnect, hold/suspend, and hang-up.
func TestTTSCallCancellations(t *testing.T) {
	t.Run("evInterrupted cancels bridge, drops late text, and next reply speaks", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		r1Cancelled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			if strings.Contains(req.Input, "Interrupted") {
				_, _ = w.Write(taggedPCM(480, 1111))
				fl.Flush()
				select {
				case r1Stalled <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case r1Cancelled <- struct{}{}:
				default:
				}
				return
			}
			_, _ = w.Write(taggedPCM(480, 2222))
			fl.Flush()
		})
		hub := agentHub(t)
		var upConn *websocket.Conn
		var upMu sync.Mutex
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				upMu.Lock()
				upConn = c
				upMu.Unlock()
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
			}
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))

		upMu.Lock()
		c := upConn
		upMu.Unlock()

		up.send(c, replyLifecycleEvent("r1", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Interrupted story starts here."}})
		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 TTS stall")
		}
		a.heardAudio() // drain r1's first frame

		// Upstream interrupts r1, then sends a stray late delta of r1 that must NOT be spoken.
		up.send(c, replyLifecycleEvent("r1", "response.incomplete"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Late text must not speak."}})

		var cutSeq []string
		for len(cutSeq) < 3 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			cutSeq = append(cutSeq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(cutSeq, []string{"reply:start", "clear:<nil>", "reply:end"}) {
			t.Fatalf("evInterrupted sequence = %v, want [reply:start clear:<nil> reply:end]", cutSeq)
		}
		select {
		case <-r1Cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("TTS HTTP stream was not cancelled on evInterrupted")
		}
		// No stray frame of r1 after clear.
		select {
		case fr := <-a.audio:
			t.Fatalf("unexpected audio frame after evInterrupted: tag=%d", int16(binary.LittleEndian.Uint16(fr[:2])))
		case <-time.After(80 * time.Millisecond):
		}

		// Fresh Reply 2 speaks normally.
		up.send(c, replyLifecycleEvent("r2", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{"type": "response.output_text.delta", "delta": "Fresh answer."}})
		up.send(c, replyLifecycleEvent("r2", "response.completed"))
		var r2Seq []string
		for len(r2Seq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			r2Seq = append(r2Seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(r2Seq, []string{"reply:start", "reply:end"}) {
			t.Fatalf("r2 sequence = %v, want [reply:start reply:end]", r2Seq)
		}
		fr := <-a.audio
		if tag := int16(binary.LittleEndian.Uint16(fr[:2])); tag != 2222 {
			t.Fatalf("r2 audio tag = %d, want 2222", tag)
		}
		for _, req := range ttsSrv.recorded() {
			if strings.Contains(req.Input, "Late text") {
				t.Fatalf("late text of interrupted reply reached TTS: %+v", req)
			}
		}
	})

	t.Run("app interrupt cancels active and queued turns", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		var r2Synthesized atomic.Bool
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			switch {
			case strings.Contains(req.Input, "First"):
				_, _ = w.Write(taggedPCM(480, 1111))
				fl.Flush()
				select {
				case r1Stalled <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			case strings.Contains(req.Input, "Queued"):
				r2Synthesized.Store(true)
			default:
				_, _ = w.Write(taggedPCM(480, 3333))
				fl.Flush()
			}
		})
		hub := agentHub(t)
		var upConn *websocket.Conn
		var upMu sync.Mutex
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				upMu.Lock()
				upConn = c
				upMu.Unlock()
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
			}
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))

		upMu.Lock()
		c := upConn
		upMu.Unlock()

		up.send(c, replyLifecycleEvent("r1", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "First reply hangs."}})
		up.send(c, replyLifecycleEvent("r1", "response.completed"))
		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 stall")
		}
		a.heardAudio()
		// Queue r2 behind r1, then app sends interrupt.
		up.send(c, replyLifecycleEvent("r2", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{"type": "response.output_text.delta", "delta": "Queued reply must drop."}})
		var seq []string
		for {
			f := a.until(func(f map[string]any) bool {
				return f["type"] != "transcript" || strings.Contains(fmt.Sprint(f["text"]), "Queued")
			})
			if f["type"] != "transcript" {
				seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
				continue
			}
			break
		}
		a.send(map[string]any{"type": "interrupt"})

		for len(seq) < 3 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(seq, []string{"reply:start", "clear:<nil>", "reply:end"}) {
			t.Fatalf("app interrupt sequence = %v, want [reply:start clear:<nil> reply:end]", seq)
		}
		// Late delta of r2 must be dropped too, then r3 speaks.
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{"type": "response.output_text.delta", "delta": "More dropped text."}})
		up.send(c, replyLifecycleEvent("r2", "response.completed"))
		up.send(c, replyLifecycleEvent("r3", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r3", "event": map[string]any{"type": "response.output_text.delta", "delta": "After interrupt."}})
		up.send(c, replyLifecycleEvent("r3", "response.completed"))

		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })
		if r2Synthesized.Load() {
			t.Fatal("queued reply r2 was synthesized despite app interrupt")
		}
		fr := <-a.audio
		if tag := int16(binary.LittleEndian.Uint16(fr[:2])); tag != 3333 {
			t.Fatalf("r3 audio tag = %d, want 3333", tag)
		}
	})

	t.Run("reconnect cancels bridge and keeps fixed output rate", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		r1Cancelled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			if strings.Contains(req.Input, "Before drop") {
				_, _ = w.Write(taggedPCM(480, 1111))
				fl.Flush()
				select {
				case r1Stalled <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case r1Cancelled <- struct{}{}:
				default:
				}
				return
			}
			_, _ = w.Write(taggedPCM(480, 4444))
			fl.Flush()
		})
		hub := agentHub(t)
		var sess int
		var mu sync.Mutex
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			mu.Lock()
			sess++
			n := sess
			mu.Unlock()
			// Reconnected session reports 16 kHz upstream rate; TTS call must stay at fixed 24 kHz.
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{
				"audio":             map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 16000}},
				"output_modalities": []string{"text"},
			}})
			if n == 1 {
				u.send(c, replyLifecycleEvent("r1", "response.created"))
				u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Before drop text."}})
				go func() {
					<-r1Stalled
					_ = c.Close(websocket.StatusGoingAway, "drop")
				}()
				return
			}
			u.send(c, replyLifecycleEvent("r2", "response.created"))
			u.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{"type": "response.output_text.delta", "delta": "After reconnect."}})
			u.send(c, replyLifecycleEvent("r2", "response.completed"))
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		logs := captureLog(t, pl)
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))
		a.heardAudio() // r1's first frame

		var seq []string
		for len(seq) < 7 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		want := []string{"reply:start", "clear:<nil>", "reply:end", "link:reconnecting", "link:live", "reply:start", "reply:end"}
		if !slices.Equal(seq, want) {
			t.Fatalf("reconnect sequence = %v, want %v", seq, want)
		}
		select {
		case <-r1Cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("r1 TTS request was not cancelled on reconnect")
		}
		fr := <-a.audio
		if len(fr) != 960 || int16(binary.LittleEndian.Uint16(fr[:2])) != 4444 {
			t.Fatalf("post-reconnect frame len=%d tag=%d, want 960 / 4444", len(fr), int16(binary.LittleEndian.Uint16(fr[:2])))
		}
		if strings.Contains(logs.String(), "reconnected session speaks at") {
			t.Fatalf("unexpected rate mismatch warning on TTS reconnect:\n%s", logs.String())
		}
	})

	t.Run("hold cancels bridge and quiet pause waits while TTS is active", func(t *testing.T) {
		setVar(t, &pauseAfter, 80*time.Millisecond)
		r1Stalled := make(chan struct{}, 1)
		r1Cancelled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			if strings.Contains(req.Input, "Held") {
				_, _ = w.Write(taggedPCM(480, 1111))
				fl.Flush()
				select {
				case r1Stalled <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case r1Cancelled <- struct{}{}:
				default:
				}
				return
			}
			_, _ = w.Write(taggedPCM(480, 5555))
			fl.Flush()
		})
		hub := workingAgent(t)
		var sess atomic.Int32
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			n := sess.Add(1)
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
			if n == 1 {
				u.send(c, replyLifecycleEvent("r1", "response.created"))
				u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Held mid speech."}})
				u.send(c, replyLifecycleEvent("r1", "response.completed"))
			}
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))
		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 stall")
		}
		a.heardAudio()
		// Even past pauseAfter (80 ms), quiet pause must NOT fire while TTS is actively synthesizing.
		if f := a.until(ofType("reply")); f["state"] != replyStart {
			t.Fatalf("expected reply:start, got %v", f)
		}
		select {
		case f := <-a.frames:
			if f["type"] == "link" && f["state"] == linkPaused {
				t.Fatal("quiet pause fired while TTS bridge was still active")
			}
		case <-time.After(200 * time.Millisecond):
		}

		// Explicit hold cancels TTS immediately, closes reply{end}, and sends link{paused}.
		a.send(map[string]any{"type": "hold", "on": true})
		var seq []string
		for len(seq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(seq, []string{"reply:end", "link:paused"}) {
			t.Fatalf("hold sequence = %v, want [reply:end link:paused]", seq)
		}
		select {
		case <-r1Cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("r1 TTS request was not cancelled on hold")
		}
	})

	t.Run("hang-up cancels bridge and shuts down cleanly", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		r1Cancelled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, _ ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(taggedPCM(480, 1111))
			w.(http.Flusher).Flush()
			select {
			case r1Stalled <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			select {
			case r1Cancelled <- struct{}{}:
			default:
			}
		})
		hub := agentHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
				u.send(c, replyLifecycleEvent("r1", "response.created"))
				u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Hang up mid speech."}})
				u.send(c, replyLifecycleEvent("r1", "response.completed"))
			}
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))
		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 stall")
		}
		_ = a.c.Close(websocket.StatusNormalClosure, "bye")
		select {
		case <-r1Cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("TTS stream was not cancelled on hang-up")
		}
		waitFor(t, "conversation closed cleanly", func() bool { return pl.active.Load() == 0 })
	})
}

// Silent-reply detection (#5790) with TTS: a reply with text is NOT marked silent when evTurnEnd
// arrives before TTS finishes (and stays non-silent once TTS voices it); a tool-only reply with no
// text IS marked silent so whenSaid proceeds immediately without waiting gateWait.
func TestTTSCallSilentReplyLogic(t *testing.T) {
	setVar(t, &gateWait, 500*time.Millisecond)
	ttsGate := make(chan struct{})
	ttsHit := make(chan struct{}, 1)
	ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, _ ttsSpeechRequest) {
		select {
		case ttsHit <- struct{}{}:
		default:
		}
		select {
		case <-ttsGate:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "24000")
		_, _ = w.Write(taggedPCM(480, 1234))
	})

	hub := agentHub(t)
	var upConn *websocket.Conn
	var upMu sync.Mutex
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			upMu.Lock()
			upConn = c
			upMu.Unlock()
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
		}
	})
	pl := ttsPlugin(t, up, ttsSrv, hub.server(t), func(c *Config) {
		c.Providers[0].Tools = new(true)
	})
	a, conv := openCallForTest(t, pl, boundStart("a1", nil), false)
	a.until(ofType("ready"))

	upMu.Lock()
	c := upConn
	upMu.Unlock()

	// Speaking reply r1 finishes evTurnEnd BEFORE TTS emits any audio: must NOT be marked silent.
	up.send(c, replyLifecycleEvent("r1", "response.created"))
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "好的，我去查一下。"}})
	up.send(c, replyLifecycleEvent("r1", "response.completed"))
	select {
	case <-ttsHit:
	case <-time.After(5 * time.Second):
		t.Fatal("TTS was never hit for r1")
	}
	conv.mu.Lock()
	r1SilentEarly := slices.Contains(conv.silent, "r1")
	conv.mu.Unlock()
	if r1SilentEarly {
		t.Fatal("r1 was falsely marked silent at evTurnEnd before TTS spoke")
	}

	close(ttsGate)
	a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })
	conv.mu.Lock()
	r1SilentAfter := slices.Contains(conv.silent, "r1")
	conv.mu.Unlock()
	if r1SilentAfter {
		t.Fatal("r1 was falsely marked silent after TTS delivered audio")
	}

	// Tool-only reply r2 with NO text: MUST be marked silent so whenSaid goes immediately.
	up.send(c, replyLifecycleEvent("r2", "response.created"))
	up.send(c, replyLifecycleEvent("r2", "response.completed"))
	waitFor(t, "r2 marked silent", func() bool {
		conv.mu.Lock()
		defer conv.mu.Unlock()
		return slices.Contains(conv.silent, "r2")
	})

	// Verify whenSaid proceeds immediately for r2 (silent) and waits gateWait for r1 (voiced).
	checkWhenSaid := func(replyID string) time.Duration {
		conv.mu.Lock()
		now := time.Now()
		conv.gate = speechGate{user: now.Add(-2 * time.Second)}
		conv.pace.voice = now
		done := make(chan bool, 1)
		conv.whenSaid("test", time.Time{}, replyID, func(s bool) { done <- s })
		conv.mu.Unlock()
		start := time.Now()
		if !<-done {
			t.Fatalf("whenSaid(%s) refused", replyID)
		}
		return time.Since(start)
	}
	if d := checkWhenSaid("r2"); d >= gateWait/2 {
		t.Fatalf("silent tool-only reply r2 waited %v (want immediate)", d)
	}
	if d := checkWhenSaid("r1"); d < gateWait-50*time.Millisecond {
		t.Fatalf("spoken reply r1 did not wait for caption (%v < %v)", d, gateWait)
	}
}

// TTS piece failures: a single failed piece is skipped and logged (without key or URL) while later
// pieces play; a turn whose pieces all fail still ends with paired reply{start}/reply{end}, keeps
// captions intact, and lets the call continue with subsequent replies.
func TestTTSCallPieceFailures(t *testing.T) {
	const secretKey = "sk-fail-secret-42"
	ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, _ *http.Request, req ttsSpeechRequest) {
		switch {
		case strings.Contains(req.Input, "Broken piece"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"down for `+secretKey+`"}}`)
		case strings.Contains(req.Input, "All fail"):
			http.Error(w, "upstream failure", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			_, _ = w.Write(taggedPCM(480, 6666))
		}
	})
	hub := agentHub(t)
	var upConn *websocket.Conn
	var upMu sync.Mutex
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			upMu.Lock()
			upConn = c
			upMu.Unlock()
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
		}
	})
	pl := ttsPlugin(t, up, ttsSrv, hub.server(t), func(c *Config) { c.TTSKey = secretKey })
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))

	upMu.Lock()
	c := upConn
	upMu.Unlock()

	collectReply := func(wantCaption string) []string {
		var seq []string
		var gotCaption bool
		for !gotCaption || len(seq) < 2 {
			f := a.until(func(f map[string]any) bool {
				return f["type"] != "transcript" || finalLine("assistant", wantCaption)(f)
			})
			if f["type"] == "transcript" {
				gotCaption = true
				continue
			}
			seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		return seq
	}

	// Reply 1: piece 1 fails (503), piece 2 succeeds.
	up.send(c, replyLifecycleEvent("r1", "response.created"))
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Broken piece fails. Second piece plays."}})
	up.send(c, replyLifecycleEvent("r1", "response.completed"))
	if r1Seq := collectReply("Broken piece fails. Second piece plays."); !slices.Equal(r1Seq, []string{"reply:start", "reply:end"}) {
		t.Fatalf("r1 sequence = %v", r1Seq)
	}
	fr := <-a.audio
	if tag := int16(binary.LittleEndian.Uint16(fr[:2])); tag != 6666 {
		t.Fatalf("r1 piece 2 audio tag = %d, want 6666", tag)
	}
	logs.wait("tts: piece failed", "HTTP 503")

	// Reply 2: ALL pieces fail -> still paired reply:start + reply:end, captions shown, call continues.
	up.send(c, replyLifecycleEvent("r2", "response.created"))
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{"type": "response.output_text.delta", "delta": "All fail here."}})
	up.send(c, replyLifecycleEvent("r2", "response.completed"))
	if r2Seq := collectReply("All fail here."); !slices.Equal(r2Seq, []string{"reply:start", "reply:end"}) {
		t.Fatalf("all-failed r2 sequence = %v, want [reply:start reply:end]", r2Seq)
	}

	// Reply 3: call continues cleanly after an all-failed turn.
	up.send(c, replyLifecycleEvent("r3", "response.created"))
	up.send(c, map[string]any{"type": "response.event", "delegation_id": "r3", "event": map[string]any{"type": "response.output_text.delta", "delta": "Call goes on."}})
	up.send(c, replyLifecycleEvent("r3", "response.completed"))
	a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })
	fr = <-a.audio
	if tag := int16(binary.LittleEndian.Uint16(fr[:2])); tag != 6666 {
		t.Fatalf("r3 audio tag = %d, want 6666", tag)
	}
	if strings.Contains(logs.String(), secretKey) || strings.Contains(logs.String(), ttsSrv.srv.URL) {
		t.Fatalf("log leaked TTS secret or URL:\n%s", logs.String())
	}
}

// Scope: meeting calls speak through TTS when configured; dictation and POST /speak never use TTS.
func TestTTSCallScopeMeetingVsDictationAndSpeak(t *testing.T) {
	t.Run("meeting call uses TTS", func(t *testing.T) {
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, nil)
		hub := roomHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
				u.send(c, replyLifecycleEvent("r1", "response.created"))
				u.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{"type": "response.output_text.delta", "delta": "Meeting reply via TTS."}})
				u.send(c, replyLifecycleEvent("r1", "response.completed"))
			}
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)
		a := dialApp(t, pl, meetingStart("rec1", "room1", nil))
		ready := a.until(ofType("ready"))
		if ready["output_rate"] != float64(24000) {
			t.Fatalf("meeting ready.output_rate = %v, want 24000", ready["output_rate"])
		}
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })
		if reqs := ttsSrv.recorded(); len(reqs) != 1 || reqs[0].Input != "Meeting reply via TTS." {
			t.Fatalf("meeting TTS requests = %+v", reqs)
		}
	})

	t.Run("dictation and POST /speak do not use TTS", func(t *testing.T) {
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, nil)
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			switch f["type"] {
			case "session.start":
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{
					"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 16000}},
				}})
				u.send(c, map[string]any{"type": "session.input_transcript.delta", "delta": "dictated words"})
			case "session.commentary.append":
				// POST /speak read-aloud response from upstream audio.
				u.send(c, map[string]any{"type": "session.output_audio.delta", "delta": b64(taggedPCM(320, 4242))})
				u.send(c, replyLifecycleEvent("sp1", "response.completed"))
			}
		})
		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), nil)

		// Dictation: ready output_rate is upstream's 16000, no TTS requests.
		d := dialApp(t, pl, map[string]any{"type": "start", "protocol": 2, "mode": modeDictation})
		ready := d.until(ofType("ready"))
		if ready["output_rate"] != float64(16000) || ready["voice"] != nil {
			t.Fatalf("dictation ready = %v", ready)
		}
		d.until(func(f map[string]any) bool { return f["type"] == "transcript" && f["text"] == "dictated words" })
		_ = d.c.Close(websocket.StatusNormalClosure, "bye")

		// POST /speak: audio comes from upstream read-aloud session, not TTS.
		srv := httptest.NewServer(pl.Handler())
		defer srv.Close()
		res, err := http.Post(srv.URL+"/speak", "application/json", strings.NewReader(`{"text":"Read this aloud."}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("X-Persona-Audio-Rate") != "16000" || len(body) != 640 {
			t.Fatalf("POST /speak status=%d rate=%q len=%d", res.StatusCode, res.Header.Get("X-Persona-Audio-Rate"), len(body))
		}
		if reqs := ttsSrv.recorded(); len(reqs) != 0 {
			t.Fatalf("dictation or POST /speak hit TTS endpoint: %+v", reqs)
		}
	})
}
