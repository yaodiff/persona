package voice

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Call-side streaming TTS integration (#17843, docs/assistant/voice.md "Streaming TTS").
//
// When Config.HasTTS() is true, every speaking call (bound calls, meeting calls and protocol 1 calls;
// not dictation, not POST /speak read-aloud, not meeting transcription) requests text-only output from
// the realtime upstream (with refusal fallback dropping upstream audio), feeds the model's text to a
// per-call ttsBridge, and sends the bridge's 20 ms PCM16 frames to the app at a fixed output rate
// (24000 Hz, or 16000 Hz when tts_sample_rate is 16000).

// ttsCallTurn tracks one queued or active TTS bridge turn for a conversation (under conversation.mu).
type ttsCallTurn struct {
	gen       uint64    // ttsBridge turn generation
	replyID   string    // upstream evReply id, if any
	startedAt time.Time // when the upstream reply began (for ttfa in replyAudio log)
	voiced    bool      // at least one TTS PCM frame reached the app
	opened    bool      // reply{start} was sent for this turn (protocol 2)
}

// ttsCallOutputRate returns the fixed call output rate announced in ready.output_rate when TTS is
// active: tts_sample_rate when it is 16000 or 24000, otherwise 24000. The bridge resamples any other
// endpoint rate to this fixed rate across the whole call including reconnects.
func ttsCallOutputRate(tts TTSEndpoint) int {
	if r := tts.SampleRate(); r == 16000 || r == 24000 {
		return r
	}
	return ttsDefaultRate
}

// resolveTTSVoice picks the voice sent to the TTS endpoint for this call: the app's chosen voice if
// the cached TTS voice list contains it or it equals tts_default_voice; else tts_default_voice; else
// the list's default; else the first listed voice; else "" (logged once). A stale realtime voice id
// from before TTS was enabled is never sent to the TTS endpoint.
func (c *conversation) resolveTTSVoice(ctx context.Context, requested string, tts TTSEndpoint) string {
	if !voicePattern.MatchString(requested) {
		requested = ""
	}
	defVoice := tts.DefaultVoice()
	if requested != "" && requested == defVoice {
		return requested
	}
	if requested == "" && defVoice != "" {
		return defVoice
	}
	list := c.pl.cat.ttsVoices(ctx, tts)
	if requested != "" && slices.ContainsFunc(list.Voices, func(v VoiceChoice) bool { return v.ID == requested }) {
		return requested
	}
	if defVoice != "" {
		return defVoice
	}
	if list.Default != "" {
		return list.Default
	}
	if len(list.Voices) > 0 {
		return list.Voices[0].ID
	}
	c.logf("tts: no voice configured or listed; sending empty voice")
	return ""
}

// newCallTTSBridge constructs the per-call ttsBridge using the resolved TTS endpoint and fixed output rate.
func (c *conversation) newCallTTSBridge(tts TTSEndpoint, outRate int) *ttsBridge {
	client := newTTSClient(ttsClientConfig{
		speechURL:   tts.SpeechURL(),
		apiKey:      tts.Key(),
		model:       tts.Model(),
		voice:       tts.DefaultVoice(),
		defaultRate: tts.SampleRate(),
	})
	return newTTSBridge(ttsBridgeConfig{
		client:     client,
		outputRate: outRate,
		onStart:    c.onTTSStart,
		onFrame:    c.onTTSFrame,
		onDone:     c.onTTSDone,
		onError:    c.onTTSError,
		logf:       c.logf,
	})
}

// onTTSStart runs on the bridge goroutine right before synthesizing a turn's first speakable piece.
func (c *conversation) onTTSStart(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || gen <= c.ttsCancelGen {
		return
	}
	t := c.ttsTurnLocked(gen)
	if t == nil || t.opened {
		return
	}
	t.opened = true
	c.ttsCutAt = time.Time{}
	startedAt := t.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	c.openReply(startedAt)
}

// onTTSFrame delivers one 20 ms PCM16 frame from the TTS bridge along the same app audio path as evAudio.
func (c *conversation) onTTSFrame(gen uint64, pcm []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || gen <= c.ttsCancelGen || len(pcm) == 0 {
		return
	}
	t := c.ttsTurnLocked(gen)
	if t == nil {
		return
	}
	now := time.Now()
	if !t.opened {
		t.opened = true
		startedAt := t.startedAt
		if startedAt.IsZero() {
			startedAt = now
		}
		c.openReply(startedAt)
	}
	t.voiced = true
	c.ttsCutAt = time.Time{}
	if t.replyID == "" || t.replyID == c.reply {
		c.replyVoiced = true
	}
	c.replyAudio.add(now, len(pcm), c.outRate)
	c.modelActed(now) // before its caption of the user's line closes, if that is still open: this answers it
	c.pace.played(now, len(pcm), c.outRate)
	c.closeUser()
	select {
	case c.out <- outFrame{audio: pcm}:
	case <-c.ctx.Done():
	}
}

// onTTSDone runs on the bridge goroutine when a turn finishes speaking or is cancelled.
// Silent-reply detection (c.silent) and protocol-2 reply{end} happen here for TTS turns rather than at
// the upstream's much earlier evTurnEnd.
func (c *conversation) onTTSDone(gen uint64, cancelled bool, _ error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.popTTSTurnLocked(gen)
	if c.closed || cancelled || gen <= c.ttsCancelGen || t == nil {
		return
	}
	if !t.voiced && t.replyID != "" {
		c.markSilentLocked(t.replyID)
	}
	if t.opened {
		c.closeReply(false)
	}
}

// onTTSError logs a failed piece (skipped by the bridge) without exposing the TTS URL, key or speech text.
func (c *conversation) onTTSError(gen uint64, err error) {
	c.logf("tts: piece failed (turn %d): %s", gen, transportError(err))
}

// ttsAcceptsSource selects one assistant text stream per reply when TTS is active: "text" deltas in a
// text-only session, "transcript" deltas in a refusal-fallback (audio) session, and "" for dialects
// with a single assistant text stream (codex, fake). Never both for one reply.
func (c *conversation) ttsAcceptsSource(name string) bool {
	if name == "" {
		return true
	}
	if c.textOnly {
		return name == "text"
	}
	return name == "transcript"
}

// feedTTSLocked feeds an upstream evAssistant text delta into the TTS bridge (mu held).
func (c *conversation) feedTTSLocked(e event) {
	if !c.ttsAcceptsSource(e.name) {
		return
	}
	if c.ttsDropText {
		if e.final {
			c.ttsDropText = false
		}
		return
	}
	delta := e.text
	if e.replace {
		switch {
		case c.ttsFedText == "":
			delta = e.text
		case strings.HasPrefix(e.text, c.ttsFedText):
			delta = e.text[len(c.ttsFedText):]
		default:
			delta = "" // revised text after streaming started: do not re-speak from the beginning
		}
	}
	if c.ttsCurGen == 0 {
		if strings.TrimSpace(delta) == "" {
			if e.final {
				c.ttsFedText = ""
			}
			return
		}
		gen := c.tts.EnqueueTurn(c.voice)
		if gen == 0 {
			return
		}
		c.ttsLastGen = gen
		c.ttsCurGen = gen
		c.ttsReplyHadTurn = true
		startedAt := c.ttsCurStart
		if startedAt.IsZero() {
			startedAt = time.Now()
		}
		c.ttsTurns = append(c.ttsTurns, &ttsCallTurn{gen: gen, replyID: c.ttsCurReply, startedAt: startedAt})
	}
	if delta != "" {
		c.tts.Feed(c.ttsCurGen, delta)
		if e.replace {
			c.ttsFedText = e.text
		} else {
			c.ttsFedText += delta
		}
	}
	if e.final {
		c.endTTSTextLocked()
	}
}

// endTTSTextLocked finishes text input for the currently streaming upstream reply (mu held).
func (c *conversation) endTTSTextLocked() {
	if c.ttsCurGen != 0 {
		c.tts.EndText(c.ttsCurGen)
		c.ttsCurGen = 0
	}
	c.ttsCurReply = ""
	c.ttsCurStart = time.Time{}
	c.ttsFedText = ""
}

// cancelTTSLocked cancels the active and all queued TTS bridge turns (mu held), advancing the
// generation watermark so late callbacks are dropped. When sendClear is true, sends clear before
// closing any open reply{end}.
func (c *conversation) cancelTTSLocked(sendClear bool) {
	if c.tts == nil {
		return
	}
	c.tts.Cancel()
	c.ttsCancelGen = c.ttsLastGen
	c.ttsCurGen = 0
	c.ttsReplyHadTurn = false
	c.ttsCurReply = ""
	c.ttsCurStart = time.Time{}
	c.ttsFedText = ""
	c.ttsTurns = nil
	if sendClear {
		c.sendApp(map[string]any{"type": "clear"})
		c.closeReply(true)
	}
}

// ttsPlaying reports whether TTS speech is still playing or queued for the app (mu held): either
// the bridge still has an active/queued turn or the app is still playing frames already sent
// (now < c.pace.playEnd). Used to keep the energy cut-in armed even when c.signals is true.
func (c *conversation) ttsPlaying(now time.Time) bool {
	return c.tts != nil && (c.tts.Active() || now.Before(c.pace.playEnd))
}

// ttsAlreadyCutLocked reports whether an upstream evInterrupted refers to TTS speech that was
// already cancelled and cleared (by energy cut-in or app interrupt), avoiding a duplicate clear
// or unpaired reply frame (mu held).
func (c *conversation) ttsAlreadyCutLocked(now time.Time, replyID string) bool {
	if replyID != "" {
		return replyID == c.ttsCutReply
	}
	return !c.ttsPlaying(now) && !c.replyOpen && !c.ttsCutAt.IsZero()
}

// ttsCutLocked cancels TTS due to an interruption (upstream evInterrupted, app interrupt, or energy
// cut-in) and drops remaining text deltas of the interrupted upstream reply if one is still open (mu held).
func (c *conversation) ttsCutLocked(now time.Time, sendClear bool) {
	if c.replying || c.ttsCurGen != 0 {
		c.ttsDropText = true
		if c.replying && c.reply != "" {
			c.ttsCutReply = c.reply
		}
	}
	c.ttsCutAt = now
	c.cancelTTSLocked(sendClear)
}

func (c *conversation) ttsTurnLocked(gen uint64) *ttsCallTurn {
	for _, t := range c.ttsTurns {
		if t.gen == gen {
			return t
		}
	}
	return nil
}

func (c *conversation) popTTSTurnLocked(gen uint64) *ttsCallTurn {
	for i, t := range c.ttsTurns {
		if t.gen == gen {
			c.ttsTurns = slices.Delete(c.ttsTurns, i, i+1)
			return t
		}
	}
	return nil
}

func (c *conversation) markSilentLocked(id string) {
	if id == "" || slices.Contains(c.silent, id) {
		return
	}
	if c.silent = append(c.silent, id); len(c.silent) > repliedMost {
		c.silent = c.silent[1:]
	}
}
