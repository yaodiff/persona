package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func buildTestPCM(samples int, startVal int16) []byte {
	out := make([]byte, samples*2)
	for i := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(startVal+int16(i)))
	}
	return out
}

func buildTestWAV(sampleRate int, pcm []byte, extraChunkID string, extraChunkData []byte, streamingSizes bool) []byte {
	var body bytes.Buffer
	body.WriteString("WAVE")
	// fmt sub-chunk (16 bytes PCM16 mono)
	body.WriteString("fmt ")
	_ = binary.Write(&body, binary.LittleEndian, uint32(16))
	_ = binary.Write(&body, binary.LittleEndian, uint16(1))            // PCM
	_ = binary.Write(&body, binary.LittleEndian, uint16(1))            // mono
	_ = binary.Write(&body, binary.LittleEndian, uint32(sampleRate))   // sample rate
	_ = binary.Write(&body, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	_ = binary.Write(&body, binary.LittleEndian, uint16(2))            // block align
	_ = binary.Write(&body, binary.LittleEndian, uint16(16))           // bits per sample

	if extraChunkID != "" {
		body.WriteString(extraChunkID)
		_ = binary.Write(&body, binary.LittleEndian, uint32(len(extraChunkData)))
		body.Write(extraChunkData)
		if len(extraChunkData)%2 == 1 {
			body.WriteByte(0) // RIFF odd-size pad byte
		}
	}

	body.WriteString("data")
	dataSize := uint32(len(pcm))
	if streamingSizes {
		dataSize = 0xFFFFFFFF
	}
	_ = binary.Write(&body, binary.LittleEndian, dataSize)
	body.Write(pcm)

	var wav bytes.Buffer
	wav.WriteString("RIFF")
	riffSize := uint32(body.Len())
	if streamingSizes {
		riffSize = 0xFFFFFFFF
	}
	_ = binary.Write(&wav, binary.LittleEndian, riffSize)
	wav.Write(body.Bytes())
	return wav.Bytes()
}

func TestTTSClientContractAndBearerAuth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		apiKey    string
		model     string
		wantModel string
		wantAuth  string
	}{
		{name: "without key omits Authorization", apiKey: "", model: "", wantModel: "default", wantAuth: ""},
		{name: "with key sends Bearer", apiKey: "test-secret-token", model: "tts-model-1", wantModel: "tts-model-1", wantAuth: "Bearer test-secret-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotCT string
			var gotReq ttsSpeechRequest
			wantPCM := buildTestPCM(120, 10)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				gotAuth = r.Header.Get("Authorization")
				gotCT = r.Header.Get("Content-Type")
				if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
					t.Errorf("decode body: %v", err)
				}
				w.Header().Set("Content-Type", "audio/pcm")
				w.Header().Set("X-Audio-Sample-Rate", "24000")
				_, _ = w.Write(wantPCM)
			}))
			defer srv.Close()

			cli := newTTSClient(ttsClientConfig{
				speechURL:  srv.URL + "/v1/audio/speech",
				apiKey:     tc.apiKey,
				model:      tc.model,
				voice:      "default-voice",
				httpClient: srv.Client(),
			})
			var gotPCM []byte
			var gotRate int
			if err := cli.streamPCM(context.Background(), "Hello world.", "voice-a", func(pcm []byte, rate int) error {
				gotPCM = append(gotPCM, pcm...)
				gotRate = rate
				return nil
			}); err != nil {
				t.Fatalf("streamPCM: %v", err)
			}
			if gotAuth != tc.wantAuth {
				t.Fatalf("Authorization = %q, want %q", gotAuth, tc.wantAuth)
			}
			if gotCT != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", gotCT)
			}
			if gotReq.Model != tc.wantModel || gotReq.Input != "Hello world." || gotReq.Voice != "voice-a" || gotReq.ResponseFormat != "pcm" {
				t.Fatalf("request payload = %+v", gotReq)
			}
			if gotRate != 24000 {
				t.Fatalf("rate = %d, want 24000", gotRate)
			}
			if !bytes.Equal(gotPCM, wantPCM) {
				t.Fatalf("PCM mismatch: got %d bytes, want %d bytes", len(gotPCM), len(wantPCM))
			}
		})
	}
}

func TestTTSClientSlowStreamAndOddByteAlignment(t *testing.T) {
	wantPCM := buildTestPCM(500, -250) // 1000 bytes
	// Send odd-length pieces (1, 3, 5, 479, 512) gated by a channel so we prove incremental streaming.
	splits := []int{1, 3, 5, 479, 512}
	gate := make(chan struct{})
	firstDelivered := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "16000")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)

		off := 0
		for i, n := range splits {
			if i == 3 {
				// Pause before writing the second half until the client proves it already received the first chunks.
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = w.Write(wantPCM[off : off+n])
			fl.Flush()
			off += n
		}
		// Write one stray trailing odd byte to verify it is dropped cleanly at EOF.
		_, _ = w.Write([]byte{0x7F})
		fl.Flush()
	}))
	defer srv.Close()

	cli := newTTSClient(ttsClientConfig{
		speechURL:  srv.URL + "/v1/audio/speech",
		httpClient: srv.Client(),
	})

	var gotPCM []byte
	chunks := 0
	done := make(chan error, 1)
	go func() {
		done <- cli.streamPCM(context.Background(), "Slow stream test.", "voice-a", func(pcm []byte, rate int) error {
			if len(pcm)%2 != 0 {
				return fmt.Errorf("odd chunk length %d", len(pcm))
			}
			if rate != 16000 {
				return fmt.Errorf("rate = %d, want 16000", rate)
			}
			gotPCM = append(gotPCM, pcm...)
			chunks++
			if len(gotPCM) >= 8 {
				select {
				case firstDelivered <- struct{}{}:
				default:
				}
			}
			return nil
		})
	}()

	select {
	case <-firstDelivered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for incremental chunk before server finished")
	}
	close(gate)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("streamPCM: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for streamPCM to finish")
	}
	if chunks < 2 {
		t.Fatalf("expected multiple streamed chunks, got %d", chunks)
	}
	if !bytes.Equal(gotPCM, wantPCM) {
		t.Fatalf("PCM mismatch after odd-byte alignment: got %d bytes, want %d bytes", len(gotPCM), len(wantPCM))
	}
}

func TestTTSClientWAVHeaderStripping(t *testing.T) {
	wantPCM := buildTestPCM(240, 100)

	cases := []struct {
		name           string
		payload        []byte
		writeStep      int
		headerRate     string
		wantRate       int
		wantExactBytes []byte
	}{
		{
			name:           "canonical 44-byte WAV in one write overrides header rate",
			payload:        buildTestWAV(16000, wantPCM, "", nil, false),
			writeStep:      0,
			headerRate:     "24000",
			wantRate:       16000,
			wantExactBytes: wantPCM,
		},
		{
			name:           "WAV header split across 3-byte chunks",
			payload:        buildTestWAV(22050, wantPCM, "", nil, false),
			writeStep:      3,
			headerRate:     "24000",
			wantRate:       22050,
			wantExactBytes: wantPCM,
		},
		{
			name:           "extended WAV with odd-sized LIST chunk and streaming 0xFFFFFFFF sizes",
			payload:        buildTestWAV(32000, wantPCM, "LIST", []byte("INFO-odd!"), true),
			writeStep:      7,
			headerRate:     "",
			wantRate:       32000,
			wantExactBytes: wantPCM,
		},
		{
			name:           "raw PCM starting with R is not stripped",
			payload:        append([]byte("RIFXnotawav!"), wantPCM...),
			writeStep:      5,
			headerRate:     "16000",
			wantRate:       16000,
			wantExactBytes: append([]byte("RIFXnotawav!"), wantPCM...),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.headerRate != "" {
					w.Header().Set("X-Audio-Sample-Rate", tc.headerRate)
				}
				w.WriteHeader(http.StatusOK)
				fl := w.(http.Flusher)
				if tc.writeStep <= 0 {
					_, _ = w.Write(tc.payload)
					return
				}
				for off := 0; off < len(tc.payload); off += tc.writeStep {
					end := min(off+tc.writeStep, len(tc.payload))
					_, _ = w.Write(tc.payload[off:end])
					fl.Flush()
				}
			}))
			defer srv.Close()

			cli := newTTSClient(ttsClientConfig{
				speechURL:  srv.URL + "/v1/audio/speech",
				httpClient: srv.Client(),
			})
			var gotPCM []byte
			var gotRate int
			if err := cli.streamPCM(context.Background(), "WAV test.", "voice-a", func(pcm []byte, rate int) error {
				if len(pcm)%2 != 0 {
					return fmt.Errorf("odd chunk length %d", len(pcm))
				}
				gotPCM = append(gotPCM, pcm...)
				gotRate = rate
				return nil
			}); err != nil {
				t.Fatalf("streamPCM: %v", err)
			}
			if gotRate != tc.wantRate {
				t.Fatalf("rate = %d, want %d", gotRate, tc.wantRate)
			}
			if !bytes.Equal(gotPCM, tc.wantExactBytes) {
				t.Fatalf("PCM mismatch: got %d bytes, want %d bytes", len(gotPCM), len(tc.wantExactBytes))
			}
		})
	}
}

func TestTTSClientRateResolutionPriority(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		defaultSet int
		wantRate   int
	}{
		{
			name:       "X-Audio-Sample-Rate wins over X-Persona-Audio-Rate and fallback",
			headers:    map[string]string{"X-Audio-Sample-Rate": "16000", "X-Persona-Audio-Rate": "22050"},
			defaultSet: 32000,
			wantRate:   16000,
		},
		{
			name:       "X-Persona-Audio-Rate wins over fallback",
			headers:    map[string]string{"X-Persona-Audio-Rate": "22050"},
			defaultSet: 32000,
			wantRate:   22050,
		},
		{
			name:       "configured fallback used when headers absent or invalid",
			headers:    map[string]string{"X-Audio-Sample-Rate": "not-a-rate", "X-Persona-Audio-Rate": "999999"},
			defaultSet: 32000,
			wantRate:   32000,
		},
		{
			name:       "defaults to 24000 when nothing valid configured",
			headers:    nil,
			defaultSet: 0,
			wantRate:   24000,
		},
	}

	pcm := buildTestPCM(10, 1)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				_, _ = w.Write(pcm)
			}))
			defer srv.Close()

			cli := newTTSClient(ttsClientConfig{
				speechURL:   srv.URL + "/v1/audio/speech",
				defaultRate: tc.defaultSet,
				httpClient:  srv.Client(),
			})
			var gotRate int
			if err := cli.streamPCM(context.Background(), "Rate test.", "voice-a", func(_ []byte, rate int) error {
				gotRate = rate
				return nil
			}); err != nil {
				t.Fatalf("streamPCM: %v", err)
			}
			if gotRate != tc.wantRate {
				t.Fatalf("rate = %d, want %d", gotRate, tc.wantRate)
			}
		})
	}
}

func TestTTSClientHTTPErrorsAndKeyRedaction(t *testing.T) {
	secretKey := "sk-super-secret-tts-key-12345"
	cases := []struct {
		name        string
		status      int
		body        string
		wantContain string
	}{
		{
			name:        "4xx OpenAI JSON error",
			status:      http.StatusBadRequest,
			body:        `{"error":{"message":"unknown voice id"}}`,
			wantContain: "HTTP 400: unknown voice id",
		},
		{
			name:        "5xx FastAPI detail error",
			status:      http.StatusServiceUnavailable,
			body:        `{"detail":"synthesis queue overloaded"}`,
			wantContain: "HTTP 503: synthesis queue overloaded",
		},
		{
			name:        "long error body is clipped and key redacted",
			status:      http.StatusUnauthorized,
			body:        `{"error":{"message":"rejected key ` + secretKey + `: ` + strings.Repeat("x", 400) + `"}}`,
			wantContain: "HTTP 401: rejected key [redacted]:",
		},
		{
			name:        "empty body still carries status",
			status:      http.StatusInternalServerError,
			body:        "",
			wantContain: "HTTP 500",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			cli := newTTSClient(ttsClientConfig{
				speechURL:  srv.URL + "/v1/audio/speech",
				apiKey:     secretKey,
				httpClient: srv.Client(),
			})
			err := cli.streamPCM(context.Background(), "Error test.", "voice-a", nil)
			if err == nil {
				t.Fatal("expected error on non-2xx response")
			}
			var se *ttsStatusError
			if !errors.As(err, &se) || se.status != tc.status {
				t.Fatalf("expected ttsStatusError(%d), got %T: %v", tc.status, err, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantContain) {
				t.Fatalf("error = %q, want substring %q", msg, tc.wantContain)
			}
			if strings.Contains(msg, secretKey) {
				t.Fatalf("API key leaked into error message: %q", msg)
			}
			if len([]rune(se.message)) > ttsErrClipRunes+1 {
				t.Fatalf("error message not clipped: %d runes", len([]rune(se.message)))
			}
		})
	}
}

func TestTTSClientBrokenStreamMidSentence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Promise 4096 bytes, send 320 bytes, then forcefully close the TCP connection.
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("Content-Length", strconv.Itoa(4096))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buildTestPCM(160, 1))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	cli := newTTSClient(ttsClientConfig{
		speechURL:  srv.URL + "/v1/audio/speech",
		httpClient: srv.Client(),
	})
	var received int
	err := cli.streamPCM(context.Background(), "Broken mid stream.", "voice-a", func(pcm []byte, _ int) error {
		received += len(pcm)
		return nil
	})
	if err == nil {
		t.Fatal("expected error from truncated stream")
	}
	if received == 0 {
		t.Fatal("expected initial bytes before stream broke")
	}
}

func TestTTSClientCancelMidStreamDisconnectsServer(t *testing.T) {
	gotFirstChunk := make(chan struct{}, 1)
	serverSawDisconnect := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		w.Header().Set("X-Audio-Sample-Rate", "24000")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = w.Write(buildTestPCM(480, 1))
		fl.Flush()
		<-r.Context().Done()
		serverSawDisconnect <- struct{}{}
	}))
	defer srv.Close()

	cli := newTTSClient(ttsClientConfig{
		speechURL:  srv.URL + "/v1/audio/speech",
		httpClient: srv.Client(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- cli.streamPCM(ctx, "Long stalled stream.", "voice-a", func(_ []byte, _ int) error {
			select {
			case gotFirstChunk <- struct{}{}:
			default:
			}
			return nil
		})
	}()

	select {
	case <-gotFirstChunk:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streamPCM did not abort immediately on cancel")
	}

	select {
	case <-serverSawDisconnect:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe client disconnect on cancel")
	}
}

func TestTTSClientDefaultTransportAndTimeoutDefaults(t *testing.T) {
	cli := newTTSClient(ttsClientConfig{
		speechURL: "https://tts.example.com/v1/audio/speech",
	})
	defer cli.closeIdle()

	if cli.httpClient == nil {
		t.Fatal("expected default httpClient")
	}
	if cli.httpClient.Timeout != 0 {
		t.Fatalf("httpClient.Timeout = %v, want 0 (no whole-stream timeout)", cli.httpClient.Timeout)
	}
	tr, ok := cli.httpClient.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("Transport = %T, want *http.Transport", cli.httpClient.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("expected Transport.Proxy (http.ProxyFromEnvironment)")
	}
	if tr.DialContext == nil {
		t.Fatal("expected Transport.DialContext with dial timeout")
	}
	if tr.DialTLSContext == nil {
		t.Fatal("expected Transport.DialTLSContext (dialTLS)")
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout = %v, want 90s", tr.IdleConnTimeout)
	}
	if cli.headerTimeout != ttsDefaultHeaderTimeout {
		t.Fatalf("headerTimeout = %v, want %v", cli.headerTimeout, ttsDefaultHeaderTimeout)
	}
	if cli.idleTimeout != ttsDefaultIdleTimeout {
		t.Fatalf("idleTimeout = %v, want %v", cli.idleTimeout, ttsDefaultIdleTimeout)
	}
}

func TestTTSClientHeaderAndIdleTimeouts(t *testing.T) {
	t.Run("header timeout closes connection without cancelling parent ctx", func(t *testing.T) {
		serverDisconnected := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Drain the POST JSON body (as a real TTS endpoint does) so net/http watches
			// for client disconnect, then never write response headers.
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			serverDisconnected <- struct{}{}
		}))
		defer srv.Close()
		defer srv.CloseClientConnections()

		cli := newTTSClient(ttsClientConfig{
			speechURL:     srv.URL + "/v1/audio/speech",
			headerTimeout: 80 * time.Millisecond,
			idleTimeout:   2 * time.Second,
			httpClient:    srv.Client(),
		})
		parentCtx := context.Background()
		err := cli.streamPCM(parentCtx, "Never responds.", "voice-a", nil)
		if err == nil {
			t.Fatal("expected header timeout error")
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("header timeout should not surface as context.Canceled: %v", err)
		}
		if !strings.Contains(err.Error(), "header timeout") {
			t.Fatalf("err = %v, want header timeout", err)
		}
		select {
		case <-serverDisconnected:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not observe disconnect after header timeout")
		}
	})

	t.Run("idle timeout closes stalled stream after initial chunks", func(t *testing.T) {
		serverDisconnected := make(chan struct{}, 1)
		wantPrefix := buildTestPCM(160, 7)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "16000")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(wantPrefix)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			serverDisconnected <- struct{}{}
		}))
		defer srv.Close()
		defer srv.CloseClientConnections()

		cli := newTTSClient(ttsClientConfig{
			speechURL:     srv.URL + "/v1/audio/speech",
			headerTimeout: 2 * time.Second,
			idleTimeout:   80 * time.Millisecond,
			httpClient:    srv.Client(),
		})
		var got []byte
		err := cli.streamPCM(context.Background(), "Stalls mid-stream.", "voice-a", func(pcm []byte, _ int) error {
			got = append(got, pcm...)
			return nil
		})
		if err == nil {
			t.Fatal("expected idle timeout error")
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("idle timeout should not surface as context.Canceled: %v", err)
		}
		if !strings.Contains(err.Error(), "idle timeout") {
			t.Fatalf("err = %v, want idle timeout", err)
		}
		if !bytes.Equal(got, wantPrefix) {
			t.Fatalf("prefix bytes before stall = %d, want %d", len(got), len(wantPrefix))
		}
		select {
		case <-serverDisconnected:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not observe disconnect after idle timeout")
		}
	})

	t.Run("long steady stream outlives both header and idle timeouts", func(t *testing.T) {
		// 5 chunks spaced 30ms apart = ~150ms total (> 80ms headerTimeout and > 80ms idleTimeout).
		chunk := buildTestPCM(80, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "audio/pcm")
			w.Header().Set("X-Audio-Sample-Rate", "16000")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			tk := time.NewTicker(30 * time.Millisecond)
			defer tk.Stop()
			for i := range 5 {
				if i > 0 {
					select {
					case <-tk.C:
					case <-r.Context().Done():
						return
					}
				}
				_, _ = w.Write(chunk)
				fl.Flush()
			}
		}))
		defer srv.Close()

		cli := newTTSClient(ttsClientConfig{
			speechURL:     srv.URL + "/v1/audio/speech",
			headerTimeout: 80 * time.Millisecond,
			idleTimeout:   80 * time.Millisecond,
			httpClient:    srv.Client(),
		})
		var got int
		if err := cli.streamPCM(context.Background(), "Long steady stream.", "voice-a", func(pcm []byte, _ int) error {
			got += len(pcm)
			return nil
		}); err != nil {
			t.Fatalf("steady stream should not time out: %v", err)
		}
		if got != len(chunk)*5 {
			t.Fatalf("got %d bytes, want %d", got, len(chunk)*5)
		}
	})
}

func TestTTSResamplerLengthAndContinuity(t *testing.T) {
	inRates := []int{8000, 11025, 16000, 22050, 24000, 32000, 44100, 48000}
	outRates := []int{16000, 24000}

	for _, inRate := range inRates {
		for _, outRate := range outRates {
			t.Run(fmt.Sprintf("%d_to_%d", inRate, outRate), func(t *testing.T) {
				// 0.25 s sine wave at 440 Hz.
				nSamples := inRate / 4
				src := make([]byte, nSamples*2)
				for i := range nSamples {
					v := int16(math.Round(16000 * math.Sin(2*math.Pi*440*float64(i)/float64(inRate))))
					binary.LittleEndian.PutUint16(src[i*2:], uint16(v))
				}

				// One-shot resampling.
				rWhole := newTTSResampler(inRate, outRate)
				whole := append(rWhole.process(src), rWhole.flush()...)

				// Identity passthrough check.
				if inRate == outRate {
					if !bytes.Equal(whole, src) {
						t.Fatal("matching rate did not preserve input identically")
					}
					return
				}

				// Expected sample count within +-1 sample.
				wantSamples := int(math.Round(float64(nSamples) * float64(outRate) / float64(inRate)))
				gotSamples := len(whole) / 2
				if diff := gotSamples - wantSamples; diff < -1 || diff > 1 {
					t.Fatalf("output samples = %d, want %d (+-1)", gotSamples, wantSamples)
				}

				// Chunked resampling with uneven small chunk sizes must be bit-for-bit identical.
				rChunked := newTTSResampler(inRate, outRate)
				chunkSizes := []int{2, 6, 14, 26, 62, 194, 410}
				var chunked []byte
				off := 0
				for step := 0; off < len(src); step++ {
					sz := chunkSizes[step%len(chunkSizes)]
					end := min(off+sz, len(src))
					chunked = append(chunked, rChunked.process(src[off:end])...)
					off = end
				}
				chunked = append(chunked, rChunked.flush()...)

				if !bytes.Equal(chunked, whole) {
					t.Fatalf("chunked output (%d B) differs from whole output (%d B): discontinuity at chunk joins",
						len(chunked), len(whole))
				}

				// Continuity check: adjacent sample jump must stay bounded by the 440 Hz sine's max slope.
				maxExpectedStep := 16000 * 2 * math.Pi * 440 / float64(outRate) * 1.35
				for i := 1; i < len(chunked)/2; i++ {
					a := int16(binary.LittleEndian.Uint16(chunked[(i-1)*2:]))
					b := int16(binary.LittleEndian.Uint16(chunked[i*2:]))
					if delta := math.Abs(float64(int32(b) - int32(a))); delta > maxExpectedStep {
						t.Fatalf("click detected at sample %d: jump %.1f > max %.1f", i, delta, maxExpectedStep)
					}
				}
			})
		}
	}
}

func TestTTSFramerSizesAndFinalPadding(t *testing.T) {
	for _, tc := range []struct {
		rate      int
		wantBytes int
	}{
		{rate: 16000, wantBytes: 640},
		{rate: 24000, wantBytes: 960},
	} {
		t.Run(fmt.Sprintf("%dHz", tc.rate), func(t *testing.T) {
			f := newTTSFramer(tc.rate)
			if f.frameBytes() != tc.wantBytes {
				t.Fatalf("frameBytes = %d, want %d", f.frameBytes(), tc.wantBytes)
			}

			// Feed 2 full frames + 100 bytes partial tail in uneven pieces.
			totalBytes := tc.wantBytes*2 + 100
			src := buildTestPCM(totalBytes/2, 50)
			var frames [][]byte
			emit := func(frame []byte) error {
				frames = append(frames, frame)
				return nil
			}
			for _, span := range [][2]int{{0, 100}, {100, tc.wantBytes + 50}, {tc.wantBytes + 50, totalBytes}} {
				if err := f.push(src[span[0]:span[1]], emit); err != nil {
					t.Fatalf("push: %v", err)
				}
			}
			if len(frames) != 2 {
				t.Fatalf("frames before flush = %d, want 2", len(frames))
			}
			if err := f.flush(emit); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if len(frames) != 3 {
				t.Fatalf("frames after flush = %d, want 3", len(frames))
			}
			for i, fr := range frames {
				if len(fr) != tc.wantBytes {
					t.Fatalf("frame %d len = %d, want %d", i, len(fr), tc.wantBytes)
				}
			}
			// First two frames match source bytes exactly.
			if !bytes.Equal(frames[0], src[:tc.wantBytes]) || !bytes.Equal(frames[1], src[tc.wantBytes:tc.wantBytes*2]) {
				t.Fatal("full frames altered PCM bytes")
			}
			// Third frame keeps the 100-byte tail followed by zero padding.
			if !bytes.Equal(frames[2][:100], src[tc.wantBytes*2:]) {
				t.Fatal("final frame prefix does not match trailing PCM")
			}
			for i, b := range frames[2][100:] {
				if b != 0 {
					t.Fatalf("final frame pad byte %d = %d, want 0", 100+i, b)
				}
			}

			// Flushing when empty emits nothing.
			if err := f.flush(emit); err != nil {
				t.Fatalf("second flush: %v", err)
			}
			if len(frames) != 3 {
				t.Fatalf("empty flush emitted extra frame (total %d)", len(frames))
			}
		})
	}
}
