package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const modeDictation = "dictation"

// refusesTextOnly reports whether provider p's service is already known to refuse text-only output
// (shared across calls, dictation and meeting transcription, per provider id and address).
func refusesTextOnly(p *Provider) bool {
	return trMemoOf(p).noTextOnly.Load()
}

// forgoTextOnly remembers that provider p's service refused text-only output; returns true the first time.
func forgoTextOnly(p *Provider) bool {
	return trMemoOf(p).noTextOnly.CompareAndSwap(false, true)
}

// startDictation starts one unbound, text-only session. A service that refuses the interim-caption extension or
// output_modalities gets a replacement without it, as meeting transcription does; no other failure opens another session.
func (c *conversation) startDictation(ctx context.Context, up upLink, p *Provider, w wire, model, instructions string) (int, bool, upLink, error) {
	partial := true
	textOnly := !refusesTextOnly(p)
	for {
		s := session{Model: model, InputRate: p.inputRate(), Instructions: instructions, Dictation: true, DictationCaptions: partial, TextOnly: textOnly}
		granted := false
		rate, err := startUpstream(ctx, up, w, p, s, func(e event) { granted = e.captions })
		if err == nil {
			return rate, partial && granted, up, nil
		}
		var refused *sessionRefused
		if !errors.As(err, &refused) || upstreamCode(err) != "" {
			return 0, false, up, err
		}
		switch refusedField(refused, partial) {
		case "captions":
			if !partial {
				return 0, false, up, err
			}
			partial = false
			c.logf("provider %s: the service refused interim captions (%s): dictation uses standard input transcripts", p.ID,
				firstNonEmpty(refused.code, "no code"))
		case "text_only":
			if !textOnly {
				return 0, false, up, err
			}
			if forgoTextOnly(p) {
				c.logf("provider %s: the service refused text-only output (%s): dictation output audio is discarded, but billed", p.ID,
					firstNonEmpty(refused.code, "no code"))
			}
			textOnly = false
		default:
			return 0, false, up, err
		}
		up, err = c.dial(ctx, p, w, model)
		if err != nil {
			return 0, false, nil, err
		}
	}
}

// dictationEvents adds the upstream item id and final flag of user captions to the call wire's events. The extension
// granted at session.started chooses one representation: negotiated partials are whole replacements, otherwise standard
// input transcript deltas are appended. A service that sends both never duplicates a line. Other frames keep their
// ordinary decoding, so handleDictation can explicitly discard generated output.
func (c *conversation) dictationEvents(raw []byte) []event {
	var f struct {
		Type    string `json:"type"`
		ItemID  string `json:"item_id"`
		Delta   string `json:"delta"`
		Text    string `json:"text"`
		IsFinal bool   `json:"is_final"`
	}
	if json.Unmarshal(raw, &f) == nil {
		switch {
		case strings.HasSuffix(f.Type, trCaptionSuffix):
			if !c.dictationPartial {
				return nil
			}
			return []event{{kind: evUser, id: f.ItemID, text: f.Text, replace: true, final: f.IsFinal}}
		case f.Type == "session.input_transcript.delta":
			if c.dictationPartial {
				return nil
			}
			return []event{{kind: evUser, id: f.ItemID, text: f.Delta, final: f.IsFinal}}
		}
	}
	return c.w.decode(raw)
}

// handleDictation forwards only the user's words. Generated text, audio, reply boundaries, interruptions, delegations
// and function calls have no client or conversation effect (mu held).
func (c *conversation) handleDictation(gen int, e event) {
	switch e.kind {
	case evUser:
		now := time.Now()
		if e.id != "" && c.user != nil && c.user.id != e.id {
			c.closeUser()
		}
		spoke := c.spokeAt(now)
		if strings.TrimSpace(e.text) != "" {
			c.gate.user = now
			c.gate.caption, c.userAt = now, now
		}
		c.userTextWithID(e.id, e.text, e.replace, e.final, spoke)
	case evError:
		c.logf("upstream: session error (%s, %s): %s", e.code, firstNonEmpty(e.param, "-"), clipRunes(e.text, errorRunes))
		if e.code == codeUpstreamUnavailable {
			c.down.session, c.down.text = sessionNo(gen), clipRunes(e.text, errorRunes)
		}
	}
}

// dictationLost ends one dictation where its only upstream ended. Unlike a call, there is no reconnect, clear, link or
// tool frame: the client may start a fresh dictation, which gets a fresh upstream session.
func (c *conversation) dictationLost(down bool, text string, why error) {
	if down {
		c.failCode(codeUpstreamUnavailable, i18n.T(c.language(), "voice-upstream.unavailable",
			i18n.Args{"reason": firstNonEmpty(text, codeUpstreamUnavailable)}), true)
		return
	}
	c.failCode(firstNonEmpty(upstreamCode(why), codeUpstreamFailed), i18n.T(c.language(), "voice-upstream.dictation_lost",
		i18n.Args{"reason": why.Error()}), true)
}
