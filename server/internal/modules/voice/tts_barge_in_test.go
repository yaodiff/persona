package voice

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Cut-in after upstream evTurnEnd while TTS audio is still playing (c.signals == true):
// covers both (a) TTS bridge still actively synthesizing and (b) bridge finished sending frames
// faster than real time so audio is still buffered on the app (now < pace.playEnd).
func TestTTSBargeInAfterUpstreamTurnEnd(t *testing.T) {
	t.Run("while bridge is actively synthesizing after evTurnEnd", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		r1Cancelled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			if strings.Contains(req.Input, "Long explanation") {
				_, _ = w.Write(taggedPCM(480*4, 1111)) // 80 ms sent, then stalls mid-turn
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

		// Upstream sends evReply + text + evTurnEnd (setting c.signals = true and finishing the upstream reply
		// long before TTS finishes speaking).
		up.send(c, replyLifecycleEvent("r1", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Long explanation that finishes on upstream immediately.",
		}})
		up.send(c, replyLifecycleEvent("r1", "response.completed"))

		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for TTS synthesis to stall")
		}
		a.heardAudio()
		// Drain initial reply:start and final assistant caption so subsequent frames come solely from the cut-in.
		var gotStart, gotCaption bool
		for !gotStart || !gotCaption {
			f := a.until(func(f map[string]any) bool { return f["type"] == "reply" || f["type"] == "transcript" })
			if f["type"] == "reply" && f["state"] == replyStart {
				gotStart = true
			}
			if finalLine("assistant", "Long explanation that finishes on upstream immediately.")(f) {
				gotCaption = true
			}
		}

		// User talks over TTS playback past bargeMin (with a short <= bargeGap word gap).
		// Upstream sends NO evInterrupted because r1 already finished on the upstream.
		a.speak(300*time.Millisecond, true)
		a.speak(60*time.Millisecond, false)
		a.speak(240*time.Millisecond, true)

		var seq []string
		for len(seq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(seq, []string{"clear:<nil>", "reply:end"}) {
			t.Fatalf("post-evTurnEnd cut-in sequence = %v, want [clear:<nil> reply:end]", seq)
		}
		select {
		case <-r1Cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("TTS HTTP stream was not cancelled on energy cut-in")
		}

		// Drain any frames already queued before clear; confirm zero new frames arrive after clear.
		for len(a.audio) > 0 {
			<-a.audio
		}
		select {
		case fr := <-a.audio:
			t.Fatalf("unexpected TTS frame after cut-in: tag=%d", int16(binary.LittleEndian.Uint16(fr[:2])))
		case <-time.After(100 * time.Millisecond):
		}

		// Follow-up reply speaks cleanly after the cut-in.
		up.send(c, replyLifecycleEvent("r2", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Next answer works.",
		}})
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
	})

	t.Run("while audio is still buffered on app after bridge finishes fast", func(t *testing.T) {
		// TTS synthesizes 3 seconds of PCM immediately (< 5 ms), so onTTSDone closes reply{end} and
		// bridge.Active() becomes false, while now < pace.playEnd for ~3 seconds. Energy cut-in must
		// STILL fire and send clear (without emitting a second unpaired reply{end}).
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, _ *http.Request, _ ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			_, _ = w.Write(taggedPCM(24000*3, 1500)) // 3 s at 24 kHz
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
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Fast synthesized long reply.",
		}})
		up.send(c, replyLifecycleEvent("r1", "response.completed"))

		// Wait until bridge finishes and sends reply:end (so bridge.Active() == false, playEnd ~3s in future).
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })

		// User speaks for 600 ms while app is still playing the buffered 3 s audio.
		a.speak(600*time.Millisecond, true)
		a.until(ofType("clear"))

		// Confirm NO unpaired reply:end follows clear.
		select {
		case f := <-a.frames:
			if f["type"] == "reply" {
				t.Fatalf("unexpected unpaired reply frame after clear on buffered playback: %v", f)
			}
		case <-time.After(100 * time.Millisecond):
		}
	})
}

// Cut-in during upstream text streaming (with queued turn and later upstream evInterrupted) cancels
// active and queued turns, drops late text deltas, closes the assistant caption, and never double-clears
// or leaves reply frames unpaired (both GPT-Live keyed evInterrupted and OpenAI Realtime unkeyed
// input_audio_buffer.speech_started, and in reverse order when evInterrupted arrives first).
func TestTTSBargeInDuringStreamingAndNoDoubleClear(t *testing.T) {
	t.Run("energy cut-in during streaming then keyed evInterrupted does not double-clear", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		var r2Synthesized atomic.Bool
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			switch {
			case strings.Contains(req.Input, "Streaming reply"):
				_, _ = w.Write(taggedPCM(480, 1111))
				fl.Flush()
				select {
				case r1Stalled <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			case strings.Contains(req.Input, "Queued follow-up"):
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

		// r1 finishes upstream text and stalls in TTS; r2 starts streaming (queued behind r1, still open).
		up.send(c, replyLifecycleEvent("r1", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Streaming reply one.",
		}})
		up.send(c, replyLifecycleEvent("r1", "response.completed"))
		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 TTS stall")
		}
		a.heardAudio()

		up.send(c, replyLifecycleEvent("r2", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Queued follow-up starts.",
		}})
		var preSeq []string
		for {
			f := a.until(func(f map[string]any) bool {
				return f["type"] != "transcript" || strings.Contains(fmt.Sprint(f["text"]), "Queued follow-up")
			})
			if f["type"] != "transcript" {
				preSeq = append(preSeq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
				continue
			}
			break
		}
		if !slices.Equal(preSeq, []string{"reply:start"}) {
			t.Fatalf("pre-cut sequence = %v, want [reply:start]", preSeq)
		}

		// User barges in by voice: must cancel r1 AND queued r2, send clear + reply:end, close r2 caption,
		// and drop late text deltas of r2.
		a.speak(600*time.Millisecond, true)
		var cutSeq []string
		var r2Closed bool
		for len(cutSeq) < 2 || !r2Closed {
			f := a.until(func(f map[string]any) bool {
				return f["type"] != "transcript" || finalLine("assistant", "Queued follow-up starts.")(f)
			})
			if f["type"] == "transcript" {
				r2Closed = true
				continue
			}
			cutSeq = append(cutSeq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(cutSeq, []string{"clear:<nil>", "reply:end"}) {
			t.Fatalf("cut sequence = %v, want [clear:<nil> reply:end]", cutSeq)
		}

		// Late text delta of r2 + upstream's own response.incomplete for r2 must NOT synthesize or double-clear.
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r2", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Dropped tail of r2.",
		}})
		up.send(c, replyLifecycleEvent("r2", "response.incomplete"))

		// Fresh reply r3 speaks normally, with only [reply:start, reply:end] (no duplicate clear).
		up.send(c, replyLifecycleEvent("r3", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r3", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Fresh answer after cut.",
		}})
		up.send(c, replyLifecycleEvent("r3", "response.completed"))

		var r3Seq []string
		for len(r3Seq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			r3Seq = append(r3Seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(r3Seq, []string{"reply:start", "reply:end"}) {
			t.Fatalf("post-cut sequence = %v, want [reply:start reply:end] (no double clear)", r3Seq)
		}
		if r2Synthesized.Load() {
			t.Fatal("queued turn r2 was synthesized after energy cut-in")
		}
		for _, req := range ttsSrv.recorded() {
			if strings.Contains(req.Input, "Dropped tail") {
				t.Fatalf("late text of cut reply reached TTS: %+v", req)
			}
		}
	})

	t.Run("energy cut-in then OpenAI Realtime unkeyed speech_started does not double-clear", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			if strings.Contains(req.Input, "Realtime reply") {
				_, _ = w.Write(taggedPCM(480, 1111))
				fl.Flush()
				select {
				case r1Stalled <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return
			}
			_, _ = w.Write(taggedPCM(480, 4444))
			fl.Flush()
		})

		hub := agentHub(t)
		var upConn *websocket.Conn
		var upMu sync.Mutex
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.update" {
				upMu.Lock()
				upConn = c
				upMu.Unlock()
				u.send(c, map[string]any{"type": "session.updated", "session": map[string]any{"output_modalities": []string{"text"}}})
			}
		})

		pl := ttsPlugin(t, up, ttsSrv, hub.server(t), func(c *Config) {
			c.Providers[0].Dialect = "realtime"
			c.Providers[0].InputRate = 16000 // matches a.speak's 16 kHz frames
		})
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))

		upMu.Lock()
		c := upConn
		upMu.Unlock()

		up.send(c, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp1"}})
		up.send(c, map[string]any{"type": "response.output_text.delta", "delta": "Realtime reply playing now."})
		up.send(c, map[string]any{"type": "response.done", "response": map[string]any{"id": "resp1"}})

		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for realtime TTS stall")
		}
		a.heardAudio()
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyStart })

		// Energy cut-in clears first; then OpenAI Realtime sends input_audio_buffer.speech_started (empty e.id).
		a.speak(600*time.Millisecond, true)
		var cutSeq []string
		for len(cutSeq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			cutSeq = append(cutSeq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(cutSeq, []string{"clear:<nil>", "reply:end"}) {
			t.Fatalf("realtime cut sequence = %v, want [clear:<nil> reply:end]", cutSeq)
		}

		up.send(c, map[string]any{"type": "input_audio_buffer.speech_started"})
		up.send(c, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp2"}})
		up.send(c, map[string]any{"type": "response.output_text.delta", "delta": "Second realtime reply."})
		up.send(c, map[string]any{"type": "response.done", "response": map[string]any{"id": "resp2"}})

		var r2Seq []string
		for len(r2Seq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			r2Seq = append(r2Seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(r2Seq, []string{"reply:start", "reply:end"}) {
			t.Fatalf("after unkeyed speech_started sequence = %v, want [reply:start reply:end] (no double clear)", r2Seq)
		}
	})

	t.Run("upstream evInterrupted first then continued user speech does not double-clear", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, req ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			if strings.Contains(req.Input, "Upstream cuts") {
				_, _ = w.Write(taggedPCM(480, 1111))
				fl.Flush()
				select {
				case r1Stalled <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return
			}
			_, _ = w.Write(taggedPCM(480, 5555))
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
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Upstream cuts this first.",
		}})
		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 stall")
		}
		a.heardAudio()
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyStart })

		// Upstream interrupts first.
		up.send(c, replyLifecycleEvent("r1", "response.incomplete"))
		var cutSeq []string
		for len(cutSeq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			cutSeq = append(cutSeq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(cutSeq, []string{"clear:<nil>", "reply:end"}) {
			t.Fatalf("evInterrupted sequence = %v, want [clear:<nil> reply:end]", cutSeq)
		}

		// User continues speaking past bargeMin right after evInterrupted: must NOT send a second clear.
		a.speak(700*time.Millisecond, true)
		a.send(map[string]any{"type": "text", "text": "sync"})
		for {
			f := a.until(func(f map[string]any) bool {
				return f["type"] == "clear" || f["type"] == "reply" || finalLine("user", "sync")(f)
			})
			if f["type"] == "clear" || f["type"] == "reply" {
				t.Fatalf("continued speech after evInterrupted sent unexpected frame: %v", f)
			}
			break
		}
	})
}

// Barge-in threshold and echo guard:
//   - speech below bargeMin (e.g. 400 ms backchannel) does not cut TTS;
//   - user speech before TTS starts does not carry over into a brand-new TTS turn;
//   - c.own (gate.go) rejects echoed TTS audio during playback + echoTail so it never opens c.gate.voice;
//   - echo_cancellation: false (c.gate.echoes) suppresses energy cut-in during TTS playback so uncancelled
//     speaker echo cannot cut TTS off (while explicit app interrupt still works).
func TestTTSBargeInThresholdAndEchoGuard(t *testing.T) {
	t.Run("below bargeMin and pre-TTS speech do not cut", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		r1Release := make(chan struct{})
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, _ ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			_, _ = w.Write(taggedPCM(480, 1111))
			fl.Flush()
			select {
			case r1Stalled <- struct{}{}:
			default:
			}
			select {
			case <-r1Release:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(taggedPCM(480, 1112))
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

		// User asks a long question BEFORE TTS starts (must not carry into barge state once TTS begins).
		a.speak(800*time.Millisecond, true)
		waitFor(t, "pre-TTS audio reached upstream", func() bool { return framesOf(up, 3000) >= 40 })

		upMu.Lock()
		c := upConn
		upMu.Unlock()

		up.send(c, replyLifecycleEvent("r1", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Answer that must not be cut by a backchannel.",
		}})
		up.send(c, replyLifecycleEvent("r1", "response.completed"))

		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 TTS stall")
		}
		a.heardAudio()
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyStart })

		// 400 ms backchannel (< bargeMin = 500 ms) followed by silence > bargeGap must NOT cut TTS.
		a.speak(400*time.Millisecond, true)
		a.speak(200*time.Millisecond, false)
		if a.clearedAfterInput() {
			t.Fatal("a 400 ms backchannel below bargeMin cut TTS off")
		}

		close(r1Release)
		if f := a.until(ofType("reply")); f["state"] != replyEnd {
			t.Fatalf("expected reply:end after releasing r1, got %v", f)
		}
	})

	t.Run("echo_cancellation false prevents TTS echo from self-cutting while explicit interrupt works", func(t *testing.T) {
		r1Stalled := make(chan struct{}, 1)
		r1Cancelled := make(chan struct{}, 1)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, r *http.Request, _ ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			_, _ = w.Write(taggedPCM(24000, 1111)) // 1 s of TTS audio
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
		a := dialApp(t, pl, boundStart("a1", map[string]any{"echo_cancellation": false}))
		a.until(ofType("ready"))

		upMu.Lock()
		c := upConn
		upMu.Unlock()

		up.send(c, replyLifecycleEvent("r1", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Speaker echo must not cut me off.",
		}})
		up.send(c, replyLifecycleEvent("r1", "response.completed"))

		select {
		case <-r1Stalled:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for r1 stall")
		}
		a.heardAudio()
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyStart })

		// 1 second of echoed TTS voice through the mic must NOT self-cut when echo_cancellation is false.
		a.speak(time.Second, true)
		if a.clearedAfterInput() {
			t.Fatal("TTS voice echoing through a non-AEC mic cut itself off")
		}

		// Explicit app interrupt still cancels TTS immediately.
		a.send(map[string]any{"type": "interrupt"})
		var seq []string
		for len(seq) < 2 {
			f := a.until(func(f map[string]any) bool { return f["type"] != "transcript" })
			seq = append(seq, fmt.Sprintf("%s:%v", f["type"], f["state"]))
		}
		if !slices.Equal(seq, []string{"clear:<nil>", "reply:end"}) {
			t.Fatalf("explicit interrupt sequence = %v, want [clear:<nil> reply:end]", seq)
		}
		select {
		case <-r1Cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("TTS stream was not cancelled on explicit interrupt")
		}
	})

	t.Run("ownVoice echo guard blocks TTS echo from opening user-speech gate during playback and echoTail", func(t *testing.T) {
		setVar(t, &echoTail, 150*time.Millisecond)
		ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, func(w http.ResponseWriter, _ *http.Request, _ ttsSpeechRequest) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			// 300 ms of TTS audio at 24 kHz -> advances pace.playEnd by 300 ms.
			_, _ = w.Write(taggedPCM(24000*300/1000, 1234))
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
		a, conv := openCallForTest(t, pl, boundStart("a1", map[string]any{"echo_cancellation": false}), false)
		a.until(ofType("ready"))

		upMu.Lock()
		c := upConn
		upMu.Unlock()

		up.send(c, replyLifecycleEvent("r1", "response.created"))
		up.send(c, map[string]any{"type": "response.event", "delegation_id": "r1", "event": map[string]any{
			"type": "response.output_text.delta", "delta": "Reading an agent result aloud.",
		}})
		up.send(c, replyLifecycleEvent("r1", "response.completed"))
		a.until(func(f map[string]any) bool { return f["type"] == "reply" && f["state"] == replyEnd })

		// While TTS audio plays (within 300 ms + 150 ms echoTail), 240 ms (> spokeVoice) of echoed sound must NOT open gate.voice.
		a.speak(240*time.Millisecond, true)
		waitFor(t, "echo frames reached upstream", func() bool { return framesOf(up, 3000) >= 12 })
		conv.mu.Lock()
		duringEcho := conv.gate.voice
		playEnd := conv.pace.playEnd
		conv.mu.Unlock()
		if !duringEcho.IsZero() {
			t.Fatalf("TTS echo opened gate.voice during playback/echoTail: %v (playEnd=%v)", duringEcho, playEnd)
		}

		// Once playEnd + echoTail has passed, real user speech (>= spokeVoice) DOES open gate.voice.
		wait := time.Until(playEnd.Add(echoTail)) + 50*time.Millisecond
		if wait > 0 {
			timer := time.NewTimer(wait)
			<-timer.C
		}
		a.speak(240*time.Millisecond, true)
		waitFor(t, "post-echoTail user voice opens gate.voice", func() bool {
			conv.mu.Lock()
			defer conv.mu.Unlock()
			return !conv.gate.voice.IsZero()
		})
	})
}

// Non-TTS calls keep their exact existing behavior: when c.signals is true, energy cut-in does NOT trigger.
func TestTTSBargeInLeavesNonTTSUnchanged(t *testing.T) {
	hub := agentHub(t)
	up := bargeUpstream(t, true) // signals == true, no TTS configured
	pl := testPlugin(t, liveProvider(up), hub.server(t))

	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": "讲个长故事"})
	a.heardAudio()
	a.speak(time.Second, true)
	if a.clearedAfterInput() {
		t.Fatal("non-TTS call with c.signals=true cleared on energy cut-in")
	}
}

// The barge-in after-acknowledgement quiet instruction (voice/tts_after_ack.md) is included in
// session.start instructions for TTS calls (bound and meeting, in zh-Hans, en and ja) and absent
// from non-TTS calls.
func TestTTSInstructionsIncludeAfterAckOnlyWithTTS(t *testing.T) {
	wantByLang := map[i18n.Lang]string{
		i18n.ZhHans: "被打断后简短应一声就停下等对方说，不要自己再补一句。",
		i18n.En:     "After a brief acknowledgement when interrupted, stop and wait for the user; do not add another line on your own.",
		i18n.Ja:     "途中で遮られたあとに短く相づちを打ったら、そのまま黙って相手が話すのを待ち、自分から続けて話さないでください。",
	}

	for _, lang := range i18n.Langs {
		t.Run("bound/"+string(lang), func(t *testing.T) {
			hdr := http.Header{extwire.HeaderLang: {string(lang)}}

			// With TTS: instruction is present.
			ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, nil)
			upTTS := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
				}
			})
			plTTS := ttsPlugin(t, upTTS, ttsSrv, agentHub(t).server(t), nil)
			aTTS := dialApp(t, plTTS, boundStart("a1", nil), hdr)
			aTTS.until(ofType("ready"))
			instTTS := upTTS.next("session.start")["session"].(map[string]any)["instructions"].(string)
			if !strings.Contains(instTTS, wantByLang[lang]) {
				t.Fatalf("TTS bound call (%s) instructions missing %q", lang, wantByLang[lang])
			}

			// Without TTS: instruction is absent.
			upNoTTS := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
				}
			})
			plNoTTS := testPlugin(t, liveProvider(upNoTTS), agentHub(t).server(t))
			aNoTTS := dialApp(t, plNoTTS, boundStart("a1", nil), hdr)
			aNoTTS.until(ofType("ready"))
			instNoTTS := upNoTTS.next("session.start")["session"].(map[string]any)["instructions"].(string)
			if strings.Contains(instNoTTS, wantByLang[lang]) {
				t.Fatalf("non-TTS bound call (%s) instructions unexpectedly included %q", lang, wantByLang[lang])
			}
		})

		t.Run("meeting/"+string(lang), func(t *testing.T) {
			hdr := http.Header{extwire.HeaderLang: {string(lang)}}

			ttsSrv := newMockTTSServer(t, `{"voices":["af_heart"]}`, nil)
			upTTS := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
				}
			})
			plTTS := ttsPlugin(t, upTTS, ttsSrv, roomHub(t).server(t), nil)
			aTTS := dialApp(t, plTTS, meetingStart("rec1", "room1", nil), hdr)
			aTTS.until(ofType("ready"))
			instTTS := upTTS.next("session.start")["session"].(map[string]any)["instructions"].(string)
			if !strings.Contains(instTTS, wantByLang[lang]) {
				t.Fatalf("TTS meeting call (%s) instructions missing %q", lang, wantByLang[lang])
			}

			upNoTTS := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
				}
			})
			plNoTTS := testPlugin(t, liveProvider(upNoTTS), roomHub(t).server(t))
			aNoTTS := dialApp(t, plNoTTS, meetingStart("rec1", "room1", nil), hdr)
			aNoTTS.until(ofType("ready"))
			instNoTTS := upNoTTS.next("session.start")["session"].(map[string]any)["instructions"].(string)
			if strings.Contains(instNoTTS, wantByLang[lang]) {
				t.Fatalf("non-TTS meeting call (%s) instructions unexpectedly included %q", lang, wantByLang[lang])
			}
		})
	}
}
