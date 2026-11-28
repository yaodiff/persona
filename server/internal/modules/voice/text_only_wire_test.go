package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/modules/voice/voicetest"
)

// openCallForTest connects an app socket without sending start yet, waits until converse registers the *conversation
// in pl.open, optionally sets wantTextOnly under mu, and sends start. Returns the app and live *conversation.
func openCallForTest(t *testing.T, pl *Plugin, start map[string]any, wantTextOnly bool) (*app, *conversation) {
	t.Helper()
	known := map[*conversation]bool{}
	pl.open.Range(func(k, _ any) bool {
		known[k.(*conversation)] = true
		return true
	})
	a := dialApp(t, pl, nil)
	var conv *conversation
	waitFor(t, "conversation registered in pl.open", func() bool {
		pl.open.Range(func(k, _ any) bool {
			c := k.(*conversation)
			if !known[c] {
				conv = c
				return false
			}
			return true
		})
		return conv != nil
	})
	if wantTextOnly {
		conv.mu.Lock()
		conv.wantTextOnly = true
		conv.mu.Unlock()
	}
	a.send(start)
	return a, conv
}

func convTextOnly(c *conversation) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.textOnly
}

func marshalCompact(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// With TextOnly unset, every dialect's start frame is byte-for-byte identical to the pre-TTS wire format.
func TestStartFramesUnchangedWithoutTextOnly(t *testing.T) {
	tools := []toolDecl{{Type: "function", Name: "delegate_task", Description: "hand over", Parameters: json.RawMessage(`{"type":"object"}`)}}
	for _, tt := range []struct {
		name string
		wire wire
		sess session
		want string
	}{
		{
			name: "live call with voice and tools",
			wire: liveWire{},
			sess: session{Model: "m1", Voice: "alloy", Instructions: "be brief", InputRate: 16000, Tools: tools},
			want: `{"session":{"audio":{"format":{"rate":16000,"type":"audio/pcm"},"output":{"voice":"alloy"}},` +
				`"delegation":{"type":"client"},"instructions":"be brief","model":"m1",` +
				`"tools":[{"type":"function","name":"delegate_task","description":"hand over","parameters":{"type":"object"}}]},"type":"session.start"}`,
		},
		{
			name: "live call without voice or tools",
			wire: liveWire{},
			sess: session{Model: "m1", Instructions: "be brief", InputRate: 24000},
			want: `{"session":{"audio":{"format":{"rate":24000,"type":"audio/pcm"}},"delegation":{"type":"client"},` +
				`"instructions":"be brief","model":"m1"},"type":"session.start"}`,
		},
		{
			name: "live dictation with captions and text-only",
			wire: liveWire{},
			sess: session{Model: "m1", Instructions: "listen", InputRate: 16000, Dictation: true, DictationCaptions: true, TextOnly: true},
			want: `{"session":{"audio":{"format":{"rate":16000,"type":"audio/pcm"}},"extensions":{"input_transcript_partial":"v1"},` +
				`"instructions":"listen","model":"m1","output_modalities":["text"]},"type":"session.start"}`,
		},
		{
			name: "realtime call with voice and tools",
			wire: realtimeWire{},
			sess: session{Model: "gpt-realtime", Voice: "alloy", Instructions: "be brief", InputRate: 24000, Tools: tools},
			want: `{"session":{"audio":{"input":{"format":{"rate":24000,"type":"audio/pcm"},"transcription":{"model":"gpt-4o-mini-transcribe"}},` +
				`"output":{"format":{"rate":24000,"type":"audio/pcm"},"voice":"alloy"}},"instructions":"be brief",` +
				`"tools":[{"type":"function","name":"delegate_task","description":"hand over","parameters":{"type":"object"}}],"type":"realtime"},"type":"session.update"}`,
		},
		{
			name: "realtime call without voice",
			wire: realtimeWire{},
			sess: session{Model: "gpt-realtime", InputRate: 16000},
			want: `{"session":{"audio":{"input":{"format":{"rate":16000,"type":"audio/pcm"},"transcription":{"model":"gpt-4o-mini-transcribe"}},` +
				`"output":{"format":{"rate":24000,"type":"audio/pcm"}}},"type":"realtime"},"type":"session.update"}`,
		},
		{
			name: "codex call with voice",
			wire: codexWire{},
			sess: session{Voice: "cove", Instructions: "be brief"},
			want: `{"session":{"audio":{"output":{"voice":"cove"}},"delegation":{"type":"client"},"instructions":"be brief"},"type":"session.update"}`,
		},
		{
			name: "codex call without voice",
			wire: codexWire{},
			sess: session{Instructions: "be brief"},
			want: `{"session":{"delegation":{"type":"client"},"instructions":"be brief"},"type":"session.update"}`,
		},
		{
			name: "fake call",
			wire: fakeWire{},
			sess: session{},
			want: `{"read_aloud":false,"type":"fake.start"}`,
		},
		{
			name: "fake read aloud",
			wire: fakeWire{},
			sess: session{ReadAloud: true},
			want: `{"read_aloud":true,"type":"fake.start"}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := marshalCompact(t, tt.wire.start(tt.sess)); got != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

// With TextOnly set, live and realtime request text-only output with no output voice while keeping tools, delegation /
// input transcription and instructions; codex never requests text-only; fake marks text_only: true.
func TestStartFramesWithTextOnlyPerDialect(t *testing.T) {
	tools := []toolDecl{{Type: "function", Name: "delegate_task", Description: "hand over", Parameters: json.RawMessage(`{"type":"object"}`)}}
	for _, tt := range []struct {
		name     string
		wire     wire
		supports bool
		sess     session
		want     string
	}{
		{
			name:     "live call text-only keeps delegation, tools, instructions and omits voice",
			wire:     liveWire{},
			supports: true,
			sess:     session{Model: "m1", Voice: "alloy", Instructions: "be brief", InputRate: 16000, Tools: tools, TextOnly: true},
			want: `{"session":{"audio":{"format":{"rate":16000,"type":"audio/pcm"}},"delegation":{"type":"client"},` +
				`"instructions":"be brief","model":"m1","output_modalities":["text"],` +
				`"tools":[{"type":"function","name":"delegate_task","description":"hand over","parameters":{"type":"object"}}]},"type":"session.start"}`,
		},
		{
			name:     "realtime call text-only uses GA output_modalities, keeps input audio and transcription, omits audio.output",
			wire:     realtimeWire{},
			supports: true,
			sess:     session{Model: "gpt-realtime", Voice: "alloy", Instructions: "be brief", InputRate: 16000, Tools: tools, TextOnly: true},
			want: `{"session":{"audio":{"input":{"format":{"rate":16000,"type":"audio/pcm"},"transcription":{"model":"gpt-4o-mini-transcribe"}}},` +
				`"instructions":"be brief","output_modalities":["text"],` +
				`"tools":[{"type":"function","name":"delegate_task","description":"hand over","parameters":{"type":"object"}}],"type":"realtime"},"type":"session.update"}`,
		},
		{
			name:     "codex does not support or emit text-only output_modalities",
			wire:     codexWire{},
			supports: false,
			sess:     session{Voice: "cove", Instructions: "be brief", TextOnly: true},
			want:     `{"session":{"audio":{"output":{"voice":"cove"}},"delegation":{"type":"client"},"instructions":"be brief"},"type":"session.update"}`,
		},
		{
			name:     "fake requests text_only",
			wire:     fakeWire{},
			supports: true,
			sess:     session{TextOnly: true},
			want:     `{"read_aloud":false,"text_only":true,"type":"fake.start"}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsTextOnly(tt.wire); got != tt.supports {
				t.Errorf("supportsTextOnly = %v, want %v", got, tt.supports)
			}
			if got := marshalCompact(t, tt.wire.start(tt.sess)); got != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestTextOnlyWireDecodeEvents(t *testing.T) {
	for _, tt := range []struct {
		name string
		wire wire
		raw  string
		want []event
	}{
		{
			name: "live started text-only",
			wire: liveWire{},
			raw:  `{"type":"session.started","session":{"audio":{"format":{"rate":16000}},"output_modalities":["text"]}}`,
			want: []event{{kind: evStarted, rate: 16000, modalities: []string{"text"}}},
		},
		{
			name: "live started echoes audio",
			wire: liveWire{},
			raw:  `{"type":"session.started","session":{"output_modalities":["audio","text"]}}`,
			want: []event{{kind: evStarted, modalities: []string{"audio", "text"}}},
		},
		{
			name: "realtime session.created marked created with default modalities",
			wire: realtimeWire{},
			raw:  `{"type":"session.created","session":{"type":"realtime","output_modalities":["audio"]}}`,
			want: []event{{kind: evStarted, rate: 24000, name: "created", modalities: []string{"audio"}}},
		},
		{
			name: "realtime session.updated text-only",
			wire: realtimeWire{},
			raw:  `{"type":"session.updated","session":{"type":"realtime","output_modalities":["text"]}}`,
			want: []event{{kind: evStarted, rate: 24000, modalities: []string{"text"}}},
		},
		{
			name: "realtime session.updated beta modalities field",
			wire: realtimeWire{},
			raw:  `{"type":"session.updated","session":{"modalities":["audio","text"]}}`,
			want: []event{{kind: evStarted, rate: 24000, modalities: []string{"audio", "text"}}},
		},
		{
			name: "realtime output_text delta",
			wire: realtimeWire{},
			raw:  `{"type":"response.output_text.delta","delta":"hello"}`,
			want: []event{{kind: evAssistant, text: "hello", name: "text"}},
		},
		{
			name: "realtime legacy text delta",
			wire: realtimeWire{},
			raw:  `{"type":"response.text.delta","delta":"world"}`,
			want: []event{{kind: evAssistant, text: "world", name: "text"}},
		},
		{
			name: "fake started text-only",
			wire: fakeWire{},
			raw:  `{"type":"fake.started","output_modalities":["text"]}`,
			want: []event{{kind: evStarted, modalities: []string{"text"}}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.wire.decode([]byte(tt.raw)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decode = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRefusedTextOnlyAndNonTextModalities(t *testing.T) {
	started := func(err error) error { return fmt.Errorf("the session did not start: %w", err) }
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&sessionRefused{code: "unknown_parameter", param: "session.output_modalities", text: "Unknown parameter"}, true},
		{&sessionRefused{code: "invalid_value", param: "session.output_modalities", text: "Invalid value: 'text'"}, true},
		{&sessionRefused{code: "invalid_request", text: "output_modalities is not supported"}, true},
		{&sessionRefused{code: "unknown_parameter"}, true},
		{&sessionRefused{code: "unsupported_capability"}, true},
		{&sessionRefused{code: "unknown_parameter", param: "session.tools"}, false},
		{&sessionRefused{code: "invalid_request", param: "session.tools", text: "invalid tool output"}, false},
		{&sessionRefused{code: "invalid_request", text: "tools output not supported"}, false},
		{&sessionRefused{code: "invalid_request", param: "session.model", text: "unknown model"}, false},
		{&sessionRefused{code: "invalid_api_key", text: "bad key"}, false},
		{&upstreamError{code: codeUpstreamUnavailable, err: &sessionRefused{code: codeUpstreamUnavailable, text: "output overloaded"}}, false},
		{&upstreamError{code: codeUpstreamForbidden, err: errors.New("HTTP 401")}, false},
		{started(websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "session.output_modalities unsupported"}), true},
		{started(websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "session.tools unsupported"}), false},
		{started(websocket.CloseError{Code: websocket.StatusPolicyViolation}), false},
		{started(io.EOF), false},
	} {
		if got := refusedTextOnly(tc.err); got != tc.want {
			t.Errorf("refusedTextOnly(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}

	for _, tc := range []struct {
		modalities []string
		want       bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"text"}, false},
		{[]string{"audio"}, true},
		{[]string{"audio", "text"}, true},
		{[]string{"text", "audio"}, true},
	} {
		if got := nonTextModalities(tc.modalities); got != tc.want {
			t.Errorf("nonTextModalities(%v) = %v, want %v", tc.modalities, got, tc.want)
		}
	}
}

// When wantTextOnly is unset (the default), calls on every dialect behave as today and textOnly stays false.
func TestCallDefaultLeavesTextOnlyUnset(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "alloy"}, false)
		a.until(ofType("ready"))
		sess := up.next("session.start")["session"].(map[string]any)
		if _, ok := sess["output_modalities"]; ok {
			t.Fatalf("default live call sent output_modalities: %v", sess)
		}
		if v := sess["audio"].(map[string]any)["output"].(map[string]any)["voice"]; v != "alloy" {
			t.Fatalf("default live call voice = %v, want alloy", v)
		}
		if convTextOnly(conv) {
			t.Fatal("default live call reported textOnly = true")
		}
	})

	t.Run("realtime", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.update" {
				u.send(c, map[string]any{"type": "session.updated", "session": map[string]any{}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Dialect: DialectRealtime, Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "marin"}, false)
		a.until(ofType("ready"))
		sess := up.next("session.update")["session"].(map[string]any)
		if _, ok := sess["output_modalities"]; ok {
			t.Fatalf("default realtime call sent output_modalities: %v", sess)
		}
		if v := sess["audio"].(map[string]any)["output"].(map[string]any)["voice"]; v != "marin" {
			t.Fatalf("default realtime call voice = %v, want marin", v)
		}
		if convTextOnly(conv) {
			t.Fatal("default realtime call reported textOnly = true")
		}
	})

	t.Run("codex", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.update" {
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "cx", URL: up.url(), Dialect: DialectCodex}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "voice": "cove"}, false)
		a.until(ofType("ready"))
		sess := up.next("session.update")["session"].(map[string]any)
		if v := sess["audio"].(map[string]any)["output"].(map[string]any)["voice"]; v != "cove" {
			t.Fatalf("default codex call voice = %v, want cove", v)
		}
		if convTextOnly(conv) {
			t.Fatal("default codex call reported textOnly = true")
		}
	})
}

// With wantTextOnly set: live and realtime request text-only without an output voice and record textOnly=true when
// granted; codex does not request text-only and leaves textOnly=false; fake grants text-only and sends no fake audio.
func TestCallTextOnlyGrantedPerDialect(t *testing.T) {
	t.Run("live echoes text", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "tts-voice-1"}, true)
		ready := a.until(ofType("ready"))
		if ready["voice"] != "tts-voice-1" {
			t.Fatalf("ready voice = %v, want tts-voice-1", ready["voice"])
		}
		sess := up.next("session.start")["session"].(map[string]any)
		if mods, _ := sess["output_modalities"].([]any); len(mods) != 1 || mods[0] != "text" {
			t.Fatalf("output_modalities = %v, want [text]", sess["output_modalities"])
		}
		if _, hasOut := sess["audio"].(map[string]any)["output"]; hasOut {
			t.Fatalf("live text-only start sent audio.output: %v", sess["audio"])
		}
		if sess["delegation"] == nil || len(declared(sess)) == 0 || sess["instructions"] == "" {
			t.Fatalf("live text-only start lost call config: %v", sess)
		}
		if !convTextOnly(conv) {
			t.Fatal("live textOnly = false, want true")
		}
		if up.connections() != 1 || refusesTextOnly(&pl.cfg.Providers[0]) {
			t.Fatalf("connections = %d, refused = %v", up.connections(), refusesTextOnly(&pl.cfg.Providers[0]))
		}
	})

	t.Run("live omits output_modalities in session.started", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		if !convTextOnly(conv) {
			t.Fatal("live textOnly = false when session.started omitted output_modalities, want true")
		}
		if up.connections() != 1 || refusesTextOnly(&pl.cfg.Providers[0]) {
			t.Fatalf("connections = %d, refused = %v", up.connections(), refusesTextOnly(&pl.cfg.Providers[0]))
		}
	})

	t.Run("realtime waits past session.created for session.updated", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.update" {
				// Real OpenAI Realtime GA sends session.created (default audio) before answering session.update.
				u.send(c, map[string]any{"type": "session.created", "session": map[string]any{"type": "realtime", "output_modalities": []string{"audio"}}})
				u.send(c, map[string]any{"type": "session.updated", "session": map[string]any{"type": "realtime", "output_modalities": []string{"text"}}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Dialect: DialectRealtime, Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "tts-voice-1"}, true)
		a.until(ofType("ready"))
		sess := up.next("session.update")["session"].(map[string]any)
		if mods, _ := sess["output_modalities"].([]any); len(mods) != 1 || mods[0] != "text" {
			t.Fatalf("realtime output_modalities = %v, want [text]", sess["output_modalities"])
		}
		audio := sess["audio"].(map[string]any)
		if _, hasOut := audio["output"]; hasOut {
			t.Fatalf("realtime text-only start sent audio.output: %v", audio)
		}
		in := audio["input"].(map[string]any)
		if in["format"] == nil || in["transcription"] == nil {
			t.Fatalf("realtime text-only start lost input audio/transcription: %v", in)
		}
		if len(declared(sess)) == 0 || sess["instructions"] == "" {
			t.Fatalf("realtime text-only start lost tools/instructions: %v", sess)
		}
		if !convTextOnly(conv) {
			t.Fatal("realtime textOnly = false, want true")
		}
		if up.connections() != 1 || refusesTextOnly(&pl.cfg.Providers[0]) {
			t.Fatalf("connections = %d, refused = %v", up.connections(), refusesTextOnly(&pl.cfg.Providers[0]))
		}
	})

	t.Run("codex does not request text-only and omits tts voice", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.update" {
				u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "cx", URL: up.url(), Dialect: DialectCodex}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "voice": "tts-voice-1"}, true)
		ready := a.until(ofType("ready"))
		if ready["voice"] != "tts-voice-1" {
			t.Fatalf("ready voice = %v, want tts-voice-1", ready["voice"])
		}
		sess := up.next("session.update")["session"].(map[string]any)
		if _, ok := sess["output_modalities"]; ok {
			t.Fatalf("codex unexpectedly sent output_modalities: %v", sess)
		}
		if _, ok := sess["audio"]; ok {
			t.Fatalf("codex with wantTextOnly unexpectedly forwarded TTS voice in audio: %v", sess["audio"])
		}
		if convTextOnly(conv) {
			t.Fatal("codex textOnly = true, want false")
		}
		if up.connections() != 1 {
			t.Fatalf("codex connections = %d, want 1", up.connections())
		}
	})

	t.Run("fake grants text-only and sends no fake audio", func(t *testing.T) {
		hub := newFakeHub(t)
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "dev", Dialect: DialectFake}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start"}, true)
		a.until(ofType("ready"))
		if !convTextOnly(conv) {
			t.Fatal("fake textOnly = false, want true")
		}

		// Verify fakeLink with text_only: true suppresses fake.voice audio even if read_aloud is set.
		fl := newFakeLink()
		defer fl.CloseNow()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := writeJSON(ctx, fl, fakeWire{}.start(session{ReadAloud: true, TextOnly: true})); err != nil {
			t.Fatal(err)
		}
		_, raw, err := fl.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		evs := fakeWire{}.decode(raw)
		if len(evs) != 1 || evs[0].kind != evStarted || !slices.Equal(evs[0].modalities, []string{"text"}) {
			t.Fatalf("fake.started decoded = %+v", evs)
		}
		for _, frame := range (fakeWire{}).say("hello world", "") {
			if err := writeJSON(ctx, fl, frame); err != nil {
				t.Fatal(err)
			}
		}
		_, raw, err = fl.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		evs = fakeWire{}.decode(raw)
		for _, e := range evs {
			if e.kind == evAudio {
				t.Fatalf("text-only fakeLink emitted audio: %+v", e)
			}
		}
		if len(evs) == 0 || evs[0].kind != evAssistant || evs[0].text != "hello world" {
			t.Fatalf("text-only fakeLink reply = %+v, want assistant line", evs)
		}
	})
}

// An upstream refusal of text-only (error frame, close reason, or started/updated echoing non-text modalities)
// restarts the call session without output_modalities, remembers the refusal per provider, logs once, reports
// textOnly=false, and skips output_modalities on later calls.
func TestCallTextOnlyRefusalRetryAndMemory(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			refuse func(u *fakeUpstream, c *websocket.Conn)
		}{
			{
				name: "unknown_parameter session.output_modalities",
				refuse: func(u *fakeUpstream, c *websocket.Conn) {
					u.send(c, map[string]any{"type": "error", "error": map[string]any{
						"code": "unknown_parameter", "param": "session.output_modalities",
						"message": "Unknown parameter: 'session.output_modalities'.",
					}})
				},
			},
			{
				name: "invalid_request mentioning output_modalities",
				refuse: func(u *fakeUpstream, c *websocket.Conn) {
					u.send(c, map[string]any{"type": "error", "error": map[string]any{
						"code": "invalid_request", "message": "output_modalities is not supported",
					}})
				},
			},
			{
				name: "bare unknown_parameter",
				refuse: func(u *fakeUpstream, c *websocket.Conn) {
					u.send(c, map[string]any{"type": "error", "error": map[string]any{
						"code": "unknown_parameter", "message": "unknown field",
					}})
				},
			},
			{
				name: "session.started echoes audio modality",
				refuse: func(u *fakeUpstream, c *websocket.Conn) {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{
						"output_modalities": []string{"audio"},
					}})
				},
			},
			{
				name: "session.started echoes audio and text modalities",
				refuse: func(u *fakeUpstream, c *websocket.Conn) {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{
						"output_modalities": []string{"audio", "text"},
					}})
				},
			},
			{
				name: "close reason mentions modalities",
				refuse: func(_ *fakeUpstream, c *websocket.Conn) {
					_ = c.Close(websocket.StatusPolicyViolation, "output_modalities unsupported")
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				hub := newFakeHub(t)
				up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
					if f["type"] != "session.start" {
						return
					}
					if _, asked := f["session"].(map[string]any)["output_modalities"]; asked {
						tc.refuse(u, c)
						return
					}
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
				})
				pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
				logs := captureLog(t, pl)

				a, conv1 := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "tts-v1"}, true)
				a.until(ofType("ready"))
				s1 := up.next("session.start")["session"].(map[string]any)
				s2 := up.next("session.start")["session"].(map[string]any)
				if _, ok := s1["output_modalities"]; !ok {
					t.Fatalf("first start omitted output_modalities: %v", s1)
				}
				if _, ok := s2["output_modalities"]; ok {
					t.Fatalf("fallback start still carried output_modalities: %v", s2)
				}
				if _, hasOut := s2["audio"].(map[string]any)["output"]; hasOut {
					t.Fatalf("fallback start leaked TTS voice into upstream audio.output: %v", s2["audio"])
				}
				if convTextOnly(conv1) {
					t.Fatal("after refusal fallback, textOnly = true, want false")
				}
				if up.connections() != 2 || !refusesTextOnly(&pl.cfg.Providers[0]) {
					t.Fatalf("connections = %d (want 2), remembered = %v", up.connections(), refusesTextOnly(&pl.cfg.Providers[0]))
				}
				_ = a.c.CloseNow()

				// Second call on the same provider skips output_modalities immediately and logs nothing extra.
				b, conv2 := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "tts-v1"}, true)
				b.until(ofType("ready"))
				s3 := up.next("session.start")["session"].(map[string]any)
				if _, ok := s3["output_modalities"]; ok {
					t.Fatalf("remembered provider still requested output_modalities: %v", s3)
				}
				if convTextOnly(conv2) {
					t.Fatal("remembered call textOnly = true, want false")
				}
				if up.connections() != 3 {
					t.Fatalf("second call used %d connections, want 1 (total 3)", up.connections()-2)
				}
				if n := strings.Count(logs.String(), "refused text-only output"); n != 1 {
					t.Fatalf("refused text-only output logged %d times, want 1:\n%s", n, logs.String())
				}
			})
		}
	})

	t.Run("realtime", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			refuse func(u *fakeUpstream, c *websocket.Conn)
		}{
			{
				name: "session.created then error refusing output_modalities",
				refuse: func(u *fakeUpstream, c *websocket.Conn) {
					u.send(c, map[string]any{"type": "session.created", "session": map[string]any{"type": "realtime", "output_modalities": []string{"audio"}}})
					u.send(c, map[string]any{"type": "error", "error": map[string]any{
						"code": "unknown_parameter", "param": "session.output_modalities",
						"message": "Unknown parameter: 'session.output_modalities'.",
					}})
				},
			},
			{
				name: "session.created then session.updated echoing audio",
				refuse: func(u *fakeUpstream, c *websocket.Conn) {
					u.send(c, map[string]any{"type": "session.created", "session": map[string]any{"type": "realtime", "output_modalities": []string{"audio"}}})
					u.send(c, map[string]any{"type": "session.updated", "session": map[string]any{"type": "realtime", "output_modalities": []string{"audio"}}})
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				hub := newFakeHub(t)
				up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
					if f["type"] != "session.update" {
						return
					}
					if _, asked := f["session"].(map[string]any)["output_modalities"]; asked {
						tc.refuse(u, c)
						return
					}
					u.send(c, map[string]any{"type": "session.created", "session": map[string]any{"type": "realtime", "output_modalities": []string{"audio"}}})
					u.send(c, map[string]any{"type": "session.updated", "session": map[string]any{"type": "realtime", "output_modalities": []string{"audio"}}})
				})
				pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Dialect: DialectRealtime, Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
				logs := captureLog(t, pl)

				a, conv1 := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "tts-v1"}, true)
				a.until(ofType("ready"))
				s1 := up.next("session.update")["session"].(map[string]any)
				s2 := up.next("session.update")["session"].(map[string]any)
				if _, ok := s1["output_modalities"]; !ok {
					t.Fatalf("first realtime update omitted output_modalities: %v", s1)
				}
				if _, ok := s2["output_modalities"]; ok {
					t.Fatalf("fallback realtime update still carried output_modalities: %v", s2)
				}
				if out := s2["audio"].(map[string]any)["output"].(map[string]any); out["voice"] != nil {
					t.Fatalf("fallback realtime update leaked TTS voice: %v", out)
				}
				if convTextOnly(conv1) {
					t.Fatal("realtime fallback textOnly = true, want false")
				}
				if up.connections() != 2 || !refusesTextOnly(&pl.cfg.Providers[0]) {
					t.Fatalf("connections = %d (want 2), remembered = %v", up.connections(), refusesTextOnly(&pl.cfg.Providers[0]))
				}
				_ = a.c.CloseNow()

				// Second call skips output_modalities immediately.
				b, conv2 := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1", "voice": "tts-v1"}, true)
				b.until(ofType("ready"))
				s3 := up.next("session.update")["session"].(map[string]any)
				if _, ok := s3["output_modalities"]; ok {
					t.Fatalf("remembered realtime provider still requested output_modalities: %v", s3)
				}
				if convTextOnly(conv2) || up.connections() != 3 {
					t.Fatalf("second call textOnly = %v, connections = %d", convTextOnly(conv2), up.connections())
				}
				if n := strings.Count(logs.String(), "refused text-only output"); n != 1 {
					t.Fatalf("refused text-only output logged %d times, want 1:\n%s", n, logs.String())
				}
			})
		}
	})
}

// Calls and dictation share the same per-provider noTextOnly memory, and a refusal on one provider never affects another.
func TestCallTextOnlyMemorySharedWithDictationAndScopedPerProvider(t *testing.T) {
	t.Run("call refusal remembered by dictation", func(t *testing.T) {
		live := voicetest.NewLive(t, voicetest.Script{TextOnlyRefused: true})
		pl, _ := dictationTestPlugin(t, live)

		// Call hits refusal and remembers it for provider rt.
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		if convTextOnly(conv) {
			t.Fatal("call textOnly = true after refusal")
		}
		if got := len(live.Starts()); got != 2 {
			t.Fatalf("call starts = %d, want 2", got)
		}
		_ = a.c.CloseNow()

		// Dictation on the same provider skips output_modalities on its first try.
		d, _ := dictationTestReady(t, pl)
		_ = d.c.CloseNow()
		starts := live.Starts()
		if len(starts) != 3 {
			t.Fatalf("total starts after dictation = %d, want 3 (no extra retry for dictation)", len(starts))
		}
		if _, ok := starts[2]["output_modalities"]; ok {
			t.Fatalf("dictation did not honour call's refusal memory: %v", starts[2])
		}
	})

	t.Run("dictation refusal remembered by call, other provider unaffected", func(t *testing.T) {
		refusing := voicetest.NewLive(t, voicetest.Script{TextOnlyRefused: true})
		accepting := voicetest.NewLive(t, voicetest.Script{})
		hub := newFakeHub(t)
		pl := testPlugin(t, Config{
			Default: "refusing/m1",
			Providers: []Provider{
				{ID: "refusing", URL: refusing.URL(), Dialect: DialectLive, Key: "k", Models: []string{"m1"}, Tools: new(false)},
				{ID: "accepting", URL: accepting.URL(), Dialect: DialectLive, Key: "k", Models: []string{"m1"}, Tools: new(false)},
			},
		}, hub.server(t))

		// Dictation on refusing provider hits refusal and remembers it.
		d := dialApp(t, pl, map[string]any{"type": "start", "protocol": 2, "mode": "dictation", "model": "refusing/m1"})
		d.until(ofType("ready"))
		_ = d.c.CloseNow()
		if got := len(refusing.Starts()); got != 2 {
			t.Fatalf("dictation starts = %d, want 2", got)
		}

		// Call on refusing provider skips output_modalities on its very first try.
		a, convA := openCallForTest(t, pl, map[string]any{"type": "start", "model": "refusing/m1"}, true)
		a.until(ofType("ready"))
		if convTextOnly(convA) {
			t.Fatal("refusing provider call textOnly = true")
		}
		if starts := refusing.Starts(); len(starts) != 3 {
			t.Fatalf("refusing provider starts = %d, want 3 (call skipped output_modalities)", len(starts))
		} else if _, ok := starts[2]["output_modalities"]; ok {
			t.Fatalf("call did not honour dictation's refusal memory: %v", starts[2])
		}
		_ = a.c.CloseNow()

		// Call on accepting provider still requests output_modalities and gets textOnly=true.
		b, convB := openCallForTest(t, pl, map[string]any{"type": "start", "model": "accepting/m1"}, true)
		b.until(ofType("ready"))
		if !convTextOnly(convB) {
			t.Fatal("accepting provider call textOnly = false, want true")
		}
		if starts := accepting.Starts(); len(starts) != 1 {
			t.Fatalf("accepting provider starts = %d, want 1", len(starts))
		} else if mods, _ := starts[0]["output_modalities"].([]any); len(mods) != 1 || mods[0] != "text" {
			t.Fatalf("accepting provider start = %v, want output_modalities [text]", starts[0])
		}
		_ = b.c.CloseNow()
	})
}

// Mid-call reconnects, wakes from hold/suspension, and resumed calls all negotiate text-only and honour remembered refusals.
func TestCallTextOnlyReconnectWakeAndResumeHonourMemory(t *testing.T) {
	t.Run("reconnect after refusal skips output_modalities", func(t *testing.T) {
		hub := newFakeHub(t)
		var mu sync.Mutex
		var starts []map[string]any
		startedN := 0
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			mu.Lock()
			starts = append(starts, sess)
			mu.Unlock()
			if _, asked := sess["output_modalities"]; asked {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{
					"code": "unknown_parameter", "param": "session.output_modalities", "message": "not supported",
				}})
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			mu.Lock()
			startedN++
			n := startedN
			mu.Unlock()
			if n == 1 {
				// Drop the first live session to trigger mid-call reconnect.
				_ = c.Close(websocket.StatusGoingAway, "upstream restart")
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
		logs := captureLog(t, pl)
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		a.until(func(f map[string]any) bool {
			return f["type"] == "tool" && f["id"] == "reconnect" && f["done"] == false
		})
		a.until(func(f map[string]any) bool { return f["type"] == "tool" && f["id"] == "reconnect" && f["done"] == true })

		mu.Lock()
		got := slices.Clone(starts)
		mu.Unlock()
		if len(got) != 3 {
			t.Fatalf("starts = %d, want 3 (refused + fallback + reconnect without output_modalities)", len(got))
		}
		if _, ok := got[0]["output_modalities"]; !ok {
			t.Fatal("start 0 did not ask for output_modalities")
		}
		if _, ok := got[1]["output_modalities"]; ok {
			t.Fatal("fallback start 1 still asked for output_modalities")
		}
		if _, ok := got[2]["output_modalities"]; ok {
			t.Fatal("reconnect start 2 asked for output_modalities despite refusal memory")
		}
		if convTextOnly(conv) {
			t.Fatal("after reconnect, textOnly = true, want false")
		}
		if n := strings.Count(logs.String(), "refused text-only output"); n != 1 {
			t.Fatalf("refused text-only output logged %d times, want 1", n)
		}
	})

	t.Run("reconnect when granted keeps textOnly true, and handles mid-call refusal", func(t *testing.T) {
		hub := newFakeHub(t)
		var mu sync.Mutex
		var starts []map[string]any
		var refuseFrom atomic.Bool
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			mu.Lock()
			starts = append(starts, sess)
			n := len(starts)
			mu.Unlock()
			if _, asked := sess["output_modalities"]; asked && refuseFrom.Load() {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{
					"code": "unknown_parameter", "param": "session.output_modalities", "message": "not supported",
				}})
				return
			}
			out := map[string]any{}
			if _, asked := sess["output_modalities"]; asked {
				out["output_modalities"] = []string{"text"}
			}
			u.send(c, map[string]any{"type": "session.started", "session": out})
			if n == 1 {
				// First session granted text-only, then drops.
				_ = c.Close(websocket.StatusGoingAway, "restart 1")
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		a.until(func(f map[string]any) bool {
			return f["type"] == "tool" && f["id"] == "reconnect" && f["done"] == false
		})
		a.until(func(f map[string]any) bool { return f["type"] == "tool" && f["id"] == "reconnect" && f["done"] == true })
		if !convTextOnly(conv) {
			t.Fatal("reconnected granted session textOnly = false, want true")
		}

		// Now upstream starts refusing text-only on the next reconnect.
		refuseFrom.Store(true)
		up.mu.Lock()
		lastConn := up.conns[len(up.conns)-1]
		up.mu.Unlock()
		_ = lastConn.Close(websocket.StatusGoingAway, "restart 2")
		a.until(func(f map[string]any) bool {
			return f["type"] == "tool" && f["id"] == "reconnect" && f["done"] == false
		})
		a.until(func(f map[string]any) bool { return f["type"] == "tool" && f["id"] == "reconnect" && f["done"] == true })
		if convTextOnly(conv) {
			t.Fatal("after mid-call refusal on reconnect, textOnly = true, want false")
		}
		if !refusesTextOnly(&pl.cfg.Providers[0]) {
			t.Fatal("mid-call refusal was not remembered")
		}
		mu.Lock()
		got := slices.Clone(starts)
		mu.Unlock()
		if len(got) != 4 {
			t.Fatalf("starts = %d, want 4", len(got))
		}
		if _, ok := got[1]["output_modalities"]; !ok {
			t.Fatal("reconnect 1 did not request output_modalities")
		}
		if _, ok := got[3]["output_modalities"]; ok {
			t.Fatal("fallback start 3 still requested output_modalities")
		}
	})

	t.Run("wake from hold and resumed protocol-2 call honour refusal memory", func(t *testing.T) {
		setVar(t, &pauseAfter, time.Hour)
		hub := workingAgent(t)
		var mu sync.Mutex
		var starts []map[string]any
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			mu.Lock()
			starts = append(starts, sess)
			mu.Unlock()
			if _, asked := sess["output_modalities"]; asked {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{
					"code": "unknown_parameter", "param": "session.output_modalities", "message": "not supported",
				}})
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, boundStart("a1", nil), true)
		a.until(ofType("ready"))
		if convTextOnly(conv) {
			t.Fatal("initial refused call textOnly = true")
		}

		// Hold -> pause -> off hold -> wake: woken session skips output_modalities on its first try.
		a.send(map[string]any{"type": "hold", "on": true})
		a.until(func(f map[string]any) bool { return f["type"] == "link" && f["state"] == linkPaused })
		a.send(map[string]any{"type": "hold", "on": false})
		a.until(func(f map[string]any) bool { return f["type"] == "link" && f["state"] == linkLive })
		if convTextOnly(conv) {
			t.Fatal("woken call textOnly = true")
		}
		_ = a.c.CloseNow()

		// Resumed call with history skips output_modalities on its first try.
		resumed := boundStart("a1", nil)
		resumed["history"] = []map[string]string{{"role": "user", "text": "check tests"}}
		b, conv2 := openCallForTest(t, pl, resumed, true)
		b.until(ofType("ready"))
		if convTextOnly(conv2) {
			t.Fatal("resumed call textOnly = true")
		}
		_ = b.c.CloseNow()

		mu.Lock()
		got := slices.Clone(starts)
		mu.Unlock()
		if len(got) != 4 {
			t.Fatalf("starts = %d, want 4 (refused + fallback + wake + resume)", len(got))
		}
		for _, idx := range []int{1, 2, 3} {
			if _, ok := got[idx]["output_modalities"]; ok {
				t.Errorf("start %d still sent output_modalities", idx)
			}
		}
	})
}

// Unrelated failures (auth, unavailable, unknown model, or an error that persists without output_modalities)
// behave exactly as today and never poison the provider's noTextOnly memory.
func TestCallTextOnlyOtherErrorsDoNotFallbackOrRemember(t *testing.T) {
	t.Run("handshake 401", func(t *testing.T) {
		hub := newFakeHub(t)
		var tries atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			tries.Add(1)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/live"
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: url, Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
		a, _ := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		f := a.until(ofType("error"))
		if f["fatal"] != true || f["code"] != codeUpstreamForbidden {
			t.Fatalf("error frame = %v", f)
		}
		if tries.Load() != 1 || refusesTextOnly(&pl.cfg.Providers[0]) {
			t.Fatalf("tries = %d, refused = %v", tries.Load(), refusesTextOnly(&pl.cfg.Providers[0]))
		}
	})

	for _, tc := range []struct {
		name      string
		errFrame  map[string]any
		wantCode  string
		wantConns int
	}{
		{
			name:      "upstream_unavailable",
			errFrame:  map[string]any{"code": codeUpstreamUnavailable, "message": "model overloaded"},
			wantCode:  codeUpstreamUnavailable,
			wantConns: 1,
		},
		{
			name:      "invalid_api_key",
			errFrame:  map[string]any{"code": "invalid_api_key", "message": "Incorrect API key"},
			wantConns: 1,
		},
		{
			name:      "unknown model",
			errFrame:  map[string]any{"code": "invalid_request", "param": "session.model", "message": "unknown model"},
			wantConns: 1,
		},
		{
			name:      "persistent bare unknown_parameter fails without remembering",
			errFrame:  map[string]any{"code": "unknown_parameter", "message": "unknown parameter"},
			wantConns: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newFakeHub(t)
			up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "error", "error": tc.errFrame})
				}
			})
			pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
			a, _ := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
			f := a.until(ofType("error"))
			if f["fatal"] != true {
				t.Fatalf("expected fatal error: %v", f)
			}
			if tc.wantCode != "" && f["code"] != tc.wantCode {
				t.Fatalf("error code = %v, want %q", f["code"], tc.wantCode)
			}
			if up.connections() != tc.wantConns || refusesTextOnly(&pl.cfg.Providers[0]) {
				t.Fatalf("connections = %d (want %d), refused = %v (want false)",
					up.connections(), tc.wantConns, refusesTextOnly(&pl.cfg.Providers[0]))
			}
		})
	}
}

// Interaction between auto function tools (#1420) and text-only negotiation on the same call.
func TestCallTextOnlyAndAutoToolsInteraction(t *testing.T) {
	t.Run("explicit tools refusal keeps text-only", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			if _, hasTools := sess["tools"]; hasTools {
				unknownTools(u, c)
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		s1 := up.next("session.start")["session"].(map[string]any)
		s2 := up.next("session.start")["session"].(map[string]any)
		if len(declared(s1)) == 0 || s1["output_modalities"] == nil {
			t.Fatalf("first start: %v", s1)
		}
		if len(declared(s2)) != 0 || s2["output_modalities"] == nil {
			t.Fatalf("second start should drop tools and keep output_modalities: %v", s2)
		}
		if !convTextOnly(conv) || !pl.refusesTools("rt") || refusesTextOnly(&pl.cfg.Providers[0]) {
			t.Fatalf("textOnly=%v refusesTools=%v refusesTextOnly=%v", convTextOnly(conv), pl.refusesTools("rt"), refusesTextOnly(&pl.cfg.Providers[0]))
		}
	})

	t.Run("bare tools refusal rolls back ambiguous text-only drop and keeps text-only", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			if _, hasTools := sess["tools"]; hasTools {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{"code": "unsupported_extension", "message": "not supported"}})
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{"output_modalities": []string{"text"}}})
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		if !convTextOnly(conv) || !pl.refusesTools("rt") || refusesTextOnly(&pl.cfg.Providers[0]) {
			t.Fatalf("textOnly=%v refusesTools=%v refusesTextOnly=%v", convTextOnly(conv), pl.refusesTools("rt"), refusesTextOnly(&pl.cfg.Providers[0]))
		}
	})

	t.Run("refuses both tools and output_modalities", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			if _, hasTools := sess["tools"]; hasTools {
				unknownTools(u, c)
				return
			}
			if _, hasMods := sess["output_modalities"]; hasMods {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{
					"code": "unknown_parameter", "param": "session.output_modalities", "message": "Unknown parameter",
				}})
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		if convTextOnly(conv) || !pl.refusesTools("rt") || !refusesTextOnly(&pl.cfg.Providers[0]) || up.connections() != 3 {
			t.Fatalf("textOnly=%v refusesTools=%v refusesTextOnly=%v conns=%d",
				convTextOnly(conv), pl.refusesTools("rt"), refusesTextOnly(&pl.cfg.Providers[0]), up.connections())
		}
	})

	t.Run("refuses only output_modalities keeps auto tools", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			sess := f["session"].(map[string]any)
			if _, hasMods := sess["output_modalities"]; hasMods {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{
					"code": "unknown_parameter", "param": "session.output_modalities", "message": "Unknown parameter",
				}})
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}}}}, hub.server(t))
		a, conv := openCallForTest(t, pl, map[string]any{"type": "start", "model": "rt/m1"}, true)
		a.until(ofType("ready"))
		up.next("session.start")
		s2 := up.next("session.start")["session"].(map[string]any)
		if len(declared(s2)) == 0 {
			t.Fatalf("fallback dropped auto tools unexpectedly: %v", s2)
		}
		if convTextOnly(conv) || pl.refusesTools("rt") || !refusesTextOnly(&pl.cfg.Providers[0]) || up.connections() != 2 {
			t.Fatalf("textOnly=%v refusesTools=%v refusesTextOnly=%v conns=%d",
				convTextOnly(conv), pl.refusesTools("rt"), refusesTextOnly(&pl.cfg.Providers[0]), up.connections())
		}
	})
}
