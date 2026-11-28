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
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Streaming PCM client, resampler, and 20 ms framer for the OpenAI-compatible TTS contract
// (POST <speechURL> with JSON {model, input, voice, response_format:"pcm"}, issue #17843).

const (
	ttsDefaultRate          = 24000
	ttsMinRate              = 8000
	ttsMaxRate              = 48000
	ttsFrameDurationMs      = 20
	ttsReadChunkBytes       = 4096
	ttsMaxWAVHeaderBytes    = 64 * 1024
	ttsMaxErrBodyBytes      = 4096
	ttsErrClipRunes         = 200
	ttsDefaultHeaderTimeout = 10 * time.Second
	ttsDefaultIdleTimeout   = 10 * time.Second
)

type ttsSpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed,omitempty"`
}

type ttsClientConfig struct {
	speechURL     string        // already-resolved POST .../v1/audio/speech URL
	apiKey        string        // optional Bearer token; never logged or included in errors
	model         string        // sent as "model"; empty sends "default"
	voice         string        // default voice when a turn does not specify one
	speed         float64       // optional; omitted when <= 0
	defaultRate   int           // fallback sample rate when no header/WAV rate is present (default 24000)
	headerTimeout time.Duration // max wait for response headers before failing the piece (0 -> 10s)
	idleTimeout   time.Duration // max wait without a byte once streaming before failing the piece (0 -> 10s)
	httpClient    *http.Client  // optional; nil creates a pool matching dial.go's directClient
}

// ttsClient streams headerless PCM16 mono audio from an OpenAI-compatible speech endpoint.
type ttsClient struct {
	speechURL     string
	apiKey        string
	model         string
	voice         string
	speed         float64
	defaultRate   int
	headerTimeout time.Duration
	idleTimeout   time.Duration
	httpClient    *http.Client
	ownedHTTP     bool
}

func newTTSClient(cfg ttsClientConfig) *ttsClient {
	rate := cfg.defaultRate
	if !validTTSRate(rate) {
		rate = ttsDefaultRate
	}
	model := strings.TrimSpace(cfg.model)
	if model == "" {
		model = "default"
	}
	headerTimeout := cfg.headerTimeout
	if headerTimeout <= 0 {
		headerTimeout = ttsDefaultHeaderTimeout
	}
	idleTimeout := cfg.idleTimeout
	if idleTimeout <= 0 {
		idleTimeout = ttsDefaultIdleTimeout
	}
	hc := cfg.httpClient
	owned := false
	if hc == nil {
		// Mirror dial.go's directClient conventions (ProxyFromEnvironment, 30 s dial/keepalive,
		// dialTLS, 90 s idle timeout) in a dedicated pool so ttsBridge.Close can close idle
		// connections without affecting shared realtime sockets. No whole-stream Client.Timeout:
		// long sentences keep streaming as long as bytes arrive within idleTimeout.
		hc = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				DialTLSContext:        dialTLS,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       90 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
		}
		owned = true
	}
	return &ttsClient{
		speechURL:     strings.TrimSpace(cfg.speechURL),
		apiKey:        cfg.apiKey,
		model:         model,
		voice:         strings.TrimSpace(cfg.voice),
		speed:         cfg.speed,
		defaultRate:   rate,
		headerTimeout: headerTimeout,
		idleTimeout:   idleTimeout,
		httpClient:    hc,
		ownedHTTP:     owned,
	}
}

func (c *ttsClient) closeIdle() {
	if c != nil && c.ownedHTTP && c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
}

func validTTSRate(rate int) bool {
	return rate >= ttsMinRate && rate <= ttsMaxRate
}

// ttsStatusError represents a non-2xx HTTP response from the TTS endpoint.
// Its message is clipped and scrubbed so the configured API key can never appear in it.
type ttsStatusError struct {
	status  int
	message string
}

func (e *ttsStatusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("HTTP %d", e.status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, e.message)
}

// streamPCM posts text to the configured speech URL and invokes onChunk as 2-byte aligned
// PCM16 mono bytes arrive. Cancelling ctx closes resp.Body immediately.
func (c *ttsClient) streamPCM(ctx context.Context, text, voice string, onChunk func(pcm []byte, rate int) error) error {
	if c.speechURL == "" {
		return errors.New("tts: empty speech URL")
	}
	if voice == "" {
		voice = c.voice
	}
	payload := ttsSpeechRequest{
		Model:          c.model,
		Input:          text,
		Voice:          voice,
		ResponseFormat: "pcm",
		Speed:          c.speed,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Derive a per-piece cancellable context (with no whole-stream deadline) so headerTimeout
	// and idleTimeout abort only this piece's request and close its connection without
	// cancelling the turn's parent context.
	reqCtx, reqCancel := context.WithCancel(ctx)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.speechURL, bytes.NewReader(body))
	if err != nil {
		return scrubTTSKey(err, c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/pcm, application/octet-stream, */*")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	var headerTimedOut atomic.Bool
	headerTimer := time.AfterFunc(c.headerTimeout, func() {
		headerTimedOut.Store(true)
		reqCancel()
	})
	resp, err := c.httpClient.Do(req)
	stoppedHeader := headerTimer.Stop()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if headerTimedOut.Load() {
			return fmt.Errorf("tts: response header timeout after %s", c.headerTimeout)
		}
		return scrubTTSKey(err, c.apiKey)
	}
	defer resp.Body.Close()

	// Force-close the response body immediately if either the parent context or a piece
	// timeout cancels reqCtx so a stalled Read unblocks and the server sees the disconnect.
	stopClose := context.AfterFunc(reqCtx, func() {
		_ = resp.Body.Close()
	})
	defer stopClose()

	if !stoppedHeader && headerTimedOut.Load() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("tts: response header timeout after %s", c.headerTimeout)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBodyTimedOut atomic.Bool
		errTimer := time.AfterFunc(c.idleTimeout, func() {
			errBodyTimedOut.Store(true)
			reqCancel()
			_ = resp.Body.Close()
		})
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, ttsMaxErrBodyBytes))
		errTimer.Stop()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errBodyTimedOut.Load() && len(raw) == 0 {
			return &ttsStatusError{status: resp.StatusCode}
		}
		msg := extractTTSErrorMessage(raw, c.apiKey)
		return &ttsStatusError{status: resp.StatusCode, message: msg}
	}

	rate := resolveTTSHeaderRate(resp.Header, c.defaultRate)
	buf := make([]byte, ttsReadChunkBytes)
	var sniffer ttsWAVSniffer
	var carry byte
	hasCarry := false

	emitAligned := func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		var aligned []byte
		if hasCarry {
			aligned = make([]byte, 1+len(data))
			aligned[0] = carry
			copy(aligned[1:], data)
			hasCarry = false
		} else {
			aligned = make([]byte, len(data))
			copy(aligned, data)
		}
		if len(aligned)%2 == 1 {
			carry = aligned[len(aligned)-1]
			hasCarry = true
			aligned = aligned[:len(aligned)-1]
		}
		if len(aligned) == 0 || onChunk == nil {
			return nil
		}
		return onChunk(aligned, rate)
	}

	// Arm idleTimeout strictly around resp.Body.Read (stopped before onChunk runs) so a
	// stalled upstream fails the piece while slow downstream playback/prefetch backpressure
	// never triggers a false timeout.
	var idleTimedOut atomic.Bool
	idleTimer := time.AfterFunc(c.idleTimeout, func() {
		idleTimedOut.Store(true)
		reqCancel()
		_ = resp.Body.Close()
	})
	idleTimer.Stop()
	defer idleTimer.Stop()
	idleArmed := false

	for {
		if !idleArmed {
			idleTimer.Reset(c.idleTimeout)
			idleArmed = true
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 || rerr != nil {
			idleTimer.Stop()
			idleArmed = false
		}
		if n > 0 {
			pcm, wavRate, serr := sniffer.feed(buf[:n])
			if serr != nil {
				return serr
			}
			if validTTSRate(wavRate) {
				rate = wavRate
			}
			if err := emitAligned(pcm); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			if rem := sniffer.finish(); len(rem) > 0 {
				if err := emitAligned(rem); err != nil {
					return err
				}
			}
			return nil
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if idleTimedOut.Load() {
				return fmt.Errorf("tts: stream idle timeout after %s", c.idleTimeout)
			}
			return scrubTTSKey(rerr, c.apiKey)
		}
		if idleTimedOut.Load() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("tts: stream idle timeout after %s", c.idleTimeout)
		}
	}
}

// resolveTTSHeaderRate picks the sample rate from X-Audio-Sample-Rate, then
// X-Persona-Audio-Rate, then the configured fallback, then 24000.
func resolveTTSHeaderRate(h http.Header, fallback int) int {
	for _, name := range []string{"X-Audio-Sample-Rate", "X-Persona-Audio-Rate"} {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			if n, err := strconv.Atoi(v); err == nil && validTTSRate(n) {
				return n
			}
		}
	}
	if validTTSRate(fallback) {
		return fallback
	}
	return ttsDefaultRate
}

func extractTTSErrorMessage(raw []byte, apiKey string) string {
	trimmed := bytes.TrimSpace(raw)
	var msg string
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var body struct {
			Error   json.RawMessage `json:"error"`
			Detail  json.RawMessage `json:"detail"`
			Message string          `json:"message"`
		}
		if json.Unmarshal(trimmed, &body) == nil {
			msg = parseJSONErrorField(body.Error)
			if msg == "" {
				msg = parseJSONErrorField(body.Detail)
			}
			if msg == "" {
				msg = strings.TrimSpace(body.Message)
			}
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(trimmed))
	}
	if apiKey != "" && msg != "" {
		msg = strings.ReplaceAll(msg, apiKey, "[redacted]")
	}
	return clipRunes(msg, ttsErrClipRunes)
}

func parseJSONErrorField(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return strings.TrimSpace(firstNonEmpty(obj.Message, obj.Msg))
	}
	return ""
}

func scrubTTSKey(err error, apiKey string) error {
	if err == nil || apiKey == "" {
		return err
	}
	s := err.Error()
	if !strings.Contains(s, apiKey) {
		return err
	}
	return errors.New(strings.ReplaceAll(s, apiKey, "[redacted]"))
}

// ttsWAVSniffer detects and strips a RIFF/WAVE header at the start of a PCM stream,
// even when the header is split across small reads or includes extra sub-chunks before "data".
// For normal headerless PCM streams (first byte != 'R'), it passes bytes through with zero delay.
type ttsWAVSniffer struct {
	done bool
	wav  bool // confirmed "RIFF....WAVE" header
	buf  []byte
	rate int
}

func (w *ttsWAVSniffer) feed(chunk []byte) (pcm []byte, wavRate int, err error) {
	if w.done {
		return chunk, 0, nil
	}
	if len(w.buf) == 0 && len(chunk) > 0 && chunk[0] != 'R' {
		w.done = true
		return chunk, 0, nil
	}
	w.buf = append(w.buf, chunk...)

	// Phase 1: check "RIFF" prefix (4 bytes).
	if !w.wav {
		prefixLen := min(len(w.buf), 4)
		if !bytes.Equal(w.buf[:prefixLen], []byte("RIFF")[:prefixLen]) {
			w.done = true
			out := w.buf
			w.buf = nil
			return out, 0, nil
		}
		if len(w.buf) < 12 {
			return nil, 0, nil
		}
		if !bytes.Equal(w.buf[8:12], []byte("WAVE")) {
			w.done = true
			out := w.buf
			w.buf = nil
			return out, 0, nil
		}
		w.wav = true
	}

	// Phase 2: walk RIFF sub-chunks starting at offset 12 until "data" header.
	off := 12
	for {
		if len(w.buf)-off < 8 {
			if len(w.buf) > ttsMaxWAVHeaderBytes {
				return nil, 0, errors.New("tts: WAV header too large")
			}
			return nil, 0, nil
		}
		chunkID := string(w.buf[off : off+4])
		rawSize := binary.LittleEndian.Uint32(w.buf[off+4 : off+8])
		if chunkID == "data" {
			dataStart := off + 8
			out := w.buf[dataStart:]
			rate := w.rate
			w.done = true
			w.buf = nil
			return out, rate, nil
		}
		if rawSize > ttsMaxWAVHeaderBytes {
			return nil, 0, errors.New("tts: invalid WAV header chunk size")
		}
		chunkSize := int(rawSize)
		padded := chunkSize
		if padded%2 == 1 {
			padded++
		}
		nextOff := off + 8 + padded
		if nextOff > ttsMaxWAVHeaderBytes {
			return nil, 0, errors.New("tts: WAV header too large")
		}
		if len(w.buf) < nextOff {
			return nil, 0, nil
		}
		if chunkID == "fmt " && chunkSize >= 16 {
			sr := int(binary.LittleEndian.Uint32(w.buf[off+8+4 : off+8+8]))
			if validTTSRate(sr) {
				w.rate = sr
			}
		}
		off = nextOff
	}
}

// finish returns any buffered non-WAV bytes when EOF arrives before 12 bytes.
func (w *ttsWAVSniffer) finish() []byte {
	if w.done {
		return nil
	}
	w.done = true
	if w.wav {
		// Truncated WAV header with no "data" chunk: drop header bytes.
		w.buf = nil
		return nil
	}
	out := w.buf
	w.buf = nil
	return out
}

// ttsResampler performs streaming linear-interpolation resampling of mono PCM16 little-endian
// audio from inRate (8000-48000 Hz) to outRate (typically 16000 or 24000 Hz).
// Exact rational phase and the boundary sample carry across chunks so joins do not click.
// When inRate == outRate, work is skipped and the input slice is returned as-is.
type ttsResampler struct {
	inRate     int64
	outRate    int64
	posSample  int64 // integer input sample index relative to current chunk (-1 = prevSample)
	posFrac    int64 // fractional numerator in [0, outRate)
	prevSample int16
	havePrev   bool
}

func newTTSResampler(inRate, outRate int) *ttsResampler {
	return &ttsResampler{
		inRate:  int64(inRate),
		outRate: int64(outRate),
	}
}

// process resamples an even-length PCM16 LE chunk and returns the resampled PCM16 LE bytes.
func (r *ttsResampler) process(pcm []byte) []byte {
	if len(pcm) < 2 || r.inRate <= 0 || r.outRate <= 0 || r.inRate == r.outRate {
		return pcm
	}
	inSamples := int64(len(pcm) / 2)
	estOut := (inSamples*r.outRate)/r.inRate + 4
	out := make([]byte, 0, estOut*2)

	sampleAt := func(idx int64) int16 {
		if idx == -1 {
			return r.prevSample
		}
		off := int(idx) * 2
		return int16(binary.LittleEndian.Uint16(pcm[off : off+2]))
	}

	for {
		if r.posFrac == 0 {
			if r.posSample >= inSamples {
				break
			}
			s := sampleAt(r.posSample)
			out = binary.LittleEndian.AppendUint16(out, uint16(s))
		} else {
			if r.posSample+1 >= inSamples {
				break
			}
			s0 := sampleAt(r.posSample)
			s1 := sampleAt(r.posSample + 1)
			v := float64(s0) + float64(int32(s1)-int32(s0))*float64(r.posFrac)/float64(r.outRate)
			s := clampInt16(math.Round(v))
			out = binary.LittleEndian.AppendUint16(out, uint16(s))
		}
		total := r.posFrac + r.inRate
		r.posSample += total / r.outRate
		r.posFrac = total % r.outRate
	}

	r.prevSample = sampleAt(inSamples - 1)
	r.havePrev = true
	r.posSample -= inSamples
	return out
}

// flush emits any final fractional output sample(s) remaining after the last input sample
// (e.g. when upsampling leaves a pending position between the final sample and stream end).
func (r *ttsResampler) flush() []byte {
	if !r.havePrev || r.inRate <= 0 || r.outRate <= 0 || r.inRate == r.outRate {
		return nil
	}
	var out []byte
	for r.posSample < 0 {
		out = binary.LittleEndian.AppendUint16(out, uint16(r.prevSample))
		total := r.posFrac + r.inRate
		r.posSample += total / r.outRate
		r.posFrac = total % r.outRate
	}
	return out
}

func clampInt16(v float64) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}

// ttsFramer buffers resampled PCM16 mono bytes and emits fixed 20 ms frames at the output rate
// (640 bytes at 16 kHz, 960 bytes at 24 kHz).
//
// Final partial frame policy:
// At the end of a turn, flush zero-pads any remaining complete samples (< 20 ms) with silence
// to a full 20 ms frame (dropping any stray single odd byte). This preserves the end of the
// final word while keeping every emitted frame strictly 20 ms for downstream playback and pacing.
// Between sentences of the same turn, the framer is not flushed so sentences join without padding.
type ttsFramer struct {
	size int
	buf  []byte
}

func newTTSFramer(outRate int) *ttsFramer {
	if !validTTSRate(outRate) {
		outRate = ttsDefaultRate
	}
	size := outRate * 2 * ttsFrameDurationMs / 1000
	if size <= 0 || size%2 != 0 {
		size = ttsDefaultRate * 2 * ttsFrameDurationMs / 1000
	}
	return &ttsFramer{size: size}
}

func (f *ttsFramer) frameBytes() int { return f.size }

func (f *ttsFramer) push(pcm []byte, emit func(frame []byte) error) error {
	if len(pcm) == 0 {
		return nil
	}
	f.buf = append(f.buf, pcm...)
	for len(f.buf) >= f.size {
		frame := make([]byte, f.size)
		copy(frame, f.buf[:f.size])
		// Compact remaining tail to the front to avoid unbounded backing-array growth.
		rem := copy(f.buf, f.buf[f.size:])
		f.buf = f.buf[:rem]
		if err := emit(frame); err != nil {
			return err
		}
	}
	return nil
}

// flush emits the turn's final partial frame zero-padded to a full 20 ms frame when at least
// one 16-bit sample remains buffered, then clears the buffer.
func (f *ttsFramer) flush(emit func(frame []byte) error) error {
	usable := len(f.buf) &^ 1
	if usable == 0 {
		f.buf = f.buf[:0]
		return nil
	}
	frame := make([]byte, f.size)
	copy(frame, f.buf[:usable])
	f.buf = f.buf[:0]
	return emit(frame)
}

func (f *ttsFramer) reset() {
	f.buf = f.buf[:0]
}
