package voice

import (
	"fmt"
	"time"
)

// The reply frame's states (docs/assistant/voice.md "Extension → App", protocol 2 feature "reply", #14932).
const (
	replyStart = "start"
	replyEnd   = "end"
)

// replyMeasureSpan is the shortest arrival span over which a reply's upstream audio speed is reported.
// Matches iOS VoiceReply.measureSpan (1.5 s) so hub and phone logs compare like for like: shorter bursts
// reflect initial chunking rather than sustained synthesis rate.
const replyMeasureSpan = 1500 * time.Millisecond

// replyAudio tracks one reply's upstream PCM16 audio as it arrives from the realtime service (under conversation.mu),
// so the call's log can tell an upstream voice generating slower than real time (or with large inter-chunk gaps) apart
// from a slow phone link (#14932). Numbers and ids only, never what was said.
type replyAudio struct {
	startedAt time.Time // when evReply arrived (zero if the upstream sent no evReply)
	firstAt   time.Time
	lastAt    time.Time
	rate      int // output Hz (PCM16 mono = 2 bytes/sample)
	chunks    int
	bytes     int
	lastBytes int
	minBytes  int
	maxBytes  int
	maxGap    time.Duration
}

// start begins tracking a new reply opened by evReply at now.
func (r *replyAudio) start(now time.Time) {
	*r = replyAudio{startedAt: now}
}

// add records one upstream PCM16 audio chunk of n bytes at rate Hz arriving at now.
func (r *replyAudio) add(now time.Time, n, rate int) {
	if n <= 0 || rate <= 0 {
		return
	}
	if r.chunks == 0 {
		r.firstAt = now
		r.lastAt = now
		r.rate = rate
		r.minBytes = n
		r.maxBytes = n
	} else {
		if gap := now.Sub(r.lastAt); gap > r.maxGap {
			r.maxGap = gap
		}
		if now.After(r.lastAt) {
			r.lastAt = now
		}
		if n < r.minBytes {
			r.minBytes = n
		}
		if n > r.maxBytes {
			r.maxBytes = n
		}
		if r.rate == 0 {
			r.rate = rate
		}
	}
	r.chunks++
	r.bytes += n
	r.lastBytes = n
}

// speed is the upstream audio's arrival speed relative to real time (1.0 = real time, 4.0 = 4x real time),
// measured up to the last chunk once the arrival span reaches replyMeasureSpan. Matches iOS VoiceReply.speed.
func (r *replyAudio) speed() (float64, bool) {
	if r.chunks < 2 {
		return 0, false
	}
	span := r.lastAt.Sub(r.firstAt)
	if span < replyMeasureSpan {
		return 0, false
	}
	lead := pcmDuration(r.bytes-r.lastBytes, r.rate)
	if lead <= 0 {
		return 0, false
	}
	return float64(lead) / float64(span), true
}

// logLine formats the reply's upstream audio pacing for the call log (empty when the reply had no audio).
// Numbers and voice id only, never speech text.
func (r *replyAudio) logLine(voice string, cut bool) string {
	return r.logLineFor("upstream audio", voice, cut)
}

// logLineFor formats the reply's audio pacing with source ("upstream audio" or "tts audio") for the call log.
func (r *replyAudio) logLineFor(source, voice string, cut bool) string {
	if r.chunks == 0 {
		return ""
	}
	audioDur := pcmDuration(r.bytes, r.rate)
	span := r.lastAt.Sub(r.firstAt)
	pace := "too quickly to measure"
	if spd, ok := r.speed(); ok {
		pace = fmt.Sprintf("%.2fx", spd)
	}
	chunkWord := "chunks"
	if r.chunks == 1 {
		chunkWord = "chunk"
	}
	minMS := durMS(pcmDuration(r.minBytes, r.rate))
	maxMS := durMS(pcmDuration(r.maxBytes, r.rate))
	chunksPart := fmt.Sprintf("%d %s of %dms", r.chunks, chunkWord, minMS)
	if minMS != maxMS {
		chunksPart = fmt.Sprintf("%d %s %d-%dms", r.chunks, chunkWord, minMS, maxMS)
	}
	gapPart := ""
	if r.chunks >= 2 {
		gapPart = fmt.Sprintf(", max gap %dms", durMS(r.maxGap))
	}
	ttfaPart := ""
	if !r.startedAt.IsZero() {
		ttfa := r.firstAt.Sub(r.startedAt)
		if ttfa < 0 {
			ttfa = 0
		}
		ttfaPart = fmt.Sprintf(", ttfa %dms", durMS(ttfa))
	}
	outcome := "over"
	if cut {
		outcome = "cut off"
	}
	return fmt.Sprintf("%s (voice %q, %d Hz): %dms in %dms (%s), %s%s%s, %s",
		source, firstNonEmpty(voice, "default"), r.rate, durMS(audioDur), durMS(span), pace, chunksPart, gapPart, ttfaPart, outcome)
}

func pcmDuration(n, rate int) time.Duration {
	if n <= 0 || rate <= 0 {
		return 0
	}
	return time.Duration(int64(n/2)*int64(time.Second)) / time.Duration(rate)
}

func durMS(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Round(time.Millisecond).Milliseconds()
}

// openReply starts tracking an explicit upstream reply (evReply) and tells a protocol 2 client (mu held).
// If an earlier reply stayed open without its end, it is closed first so start and end frames stay paired.
func (c *conversation) openReply(now time.Time) {
	c.finishReplyAudio(false)
	if c.replyOpen {
		c.replyOpen = false
		if c.version >= 2 && !c.dictation {
			c.sendApp(map[string]any{"type": "reply", "state": replyEnd})
		}
	}
	c.replyAudio.start(now)
	if c.version >= 2 && !c.dictation {
		c.replyOpen = true
		c.sendApp(map[string]any{"type": "reply", "state": replyStart})
	}
}

// closeReply logs the current reply's upstream audio pacing and, if a reply{start} was sent on protocol 2,
// sends the matching reply{end} (mu held). On cuts and drops, callers send clear first so the client discards
// queued audio before closing the reply boundary.
func (c *conversation) closeReply(cut bool) {
	c.finishReplyAudio(cut)
	if !c.replyOpen {
		return
	}
	c.replyOpen = false
	if c.version >= 2 && !c.dictation {
		c.sendApp(map[string]any{"type": "reply", "state": replyEnd})
	}
}

// finishReplyAudio logs and resets the current reply's audio metrics when any audio arrived (mu held).
func (c *conversation) finishReplyAudio(cut bool) {
	source := "upstream audio"
	if c.tts != nil {
		source = "tts audio"
	}
	line := c.replyAudio.logLineFor(source, c.voice, cut)
	c.replyAudio = replyAudio{}
	if line != "" {
		c.logf("%s", line)
	}
}
