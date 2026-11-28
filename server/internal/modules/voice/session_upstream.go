package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// upstreamError is a failure of the realtime service the app shows by its code.
type upstreamError struct {
	code string
	err  error
}

func (e *upstreamError) Error() string { return e.err.Error() }
func (e *upstreamError) Unwrap() error { return e.err }

// upstreamCode is the code of a failure to start the upstream session ("" when it is not one the app knows).
func upstreamCode(err error) string {
	var ue *upstreamError
	if errors.As(err, &ue) {
		return ue.code
	}
	return ""
}

// connect opens an upstream session and waits for it to start; the first one answers the app's start with ready.
// woken: it is the session a wake opens (suspend.go), not one after a drop: its instructions say the voice was paused.
func (c *conversation) connect(woken bool) error {
	c.mu.Lock()
	p, w, model, voice, wantTextOnly := c.p, c.w, c.model, c.voice, c.wantTextOnly
	c.mu.Unlock()
	history, err := c.historyForConnect()
	if err != nil {
		return err
	}

	began := time.Now()
	dctx, cancel := context.WithTimeout(c.ctx, startTimeout)
	defer cancel()
	_, auto := p.toolsMode()
	if auto && c.pl.refusesTools(p.ID) {
		c.toolless.Store(true) // another session found that its service refuses them (#1420)
	}
	bound := c.bind(dctx) // the bound conversation as it is now, read while the socket connects
	up, err := c.dial(dctx, p, w, model)
	if err != nil {
		return err
	}
	online, err := bound()
	if err != nil {
		_ = up.CloseNow()
		return err
	}
	c.mu.Lock()
	snap, waiting, hasTTS := c.snap, c.waiting(), c.tts != nil
	c.mu.Unlock()
	in := prompts.VoiceInstructions{
		Lang: string(c.language()), Owner: c.pl.cfg.Owner, Notes: c.pl.cfg.Notes, History: clipHistory(history),
		Target: snap, Waiting: waiting, Paused: woken, TTS: hasTTS,
	}
	tools := c.toolSet()
	var rate int
	partial, textOnly := false, false
	if c.dictation {
		rate, partial, up, err = c.startDictation(dctx, up, p, w, model, c.instructions(in, nil))
	} else {
		rate, textOnly, up, err = c.startCallSession(dctx, up, p, w, model, voice, in, tools, auto, wantTextOnly)
	}
	if err != nil {
		return err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = up.CloseNow()
		return errors.New("closed")
	}
	c.up, c.ready, c.connectedAt = up, true, time.Now()
	c.dictationPartial = partial
	c.textOnly = textOnly
	gen := c.gen
	first := !c.started
	why := "first"
	switch {
	case woken:
		why = "woken"
	case !first:
		why = fmt.Sprintf("reconnect %d", c.reconnects)
	}
	mode := ""
	if textOnly {
		mode = ", text-only"
	}
	c.logf("upstream: session %d started in %v (%s), %d Hz, tools %s%s", sessionNo(gen), logDur(c.connectedAt.Sub(began)), why, rate,
		c.toolsLog(p, c.toolSet()), mode)
	// A session after a drop is told the bound conversation as it is now: an agent's words the last session had not
	// taken in (its turn went on meanwhile) close the user-speech gate, as a result sent does. The same snapshot does
	// not: a request the user made just before the drop still counts.
	if words := agentWords(snap); words != c.snapWords {
		if !first {
			c.gate.told(c.connectedAt, agentReconnect)
		}
		c.snapWords = words
	}
	if first {
		outRate := rate
		if c.tts != nil {
			outRate = c.ttsOutRate
		}
		c.started, c.outRate = true, outRate
		ready := map[string]any{"type": "ready", "input_rate": p.inputRate(), "output_rate": outRate, "model": c.modelID, "voice": nilIfEmpty(voice)}
		if c.dictation {
			ready["protocol"], ready["features"], ready["voice"] = infoProtocol, features, nil
		} else if c.version >= 2 {
			ready["protocol"], ready["features"], ready["target"] = infoProtocol, features, c.target.frame()
		}
		c.sendApp(ready)
		if !online {
			c.failCode(codeMachineOffline, "这台电脑离线，说的话要等它上线才发得出去", false)
		}
	} else {
		if c.tts == nil && rate != c.outRate {
			c.logf("reconnected session speaks at %d Hz, the app plays %d", rate, c.outRate)
		}
		if c.version >= 2 {
			c.sendApp(map[string]any{"type": "link", "state": linkLive})
		} else {
			c.sendApp(map[string]any{"type": "tool", "id": "reconnect", "done": true})
		}
	}
	resume := c.resume
	c.resume = nil
	c.mu.Unlock()
	go c.readUpstream(up, gen)
	if !first {
		c.runs.reconnected()
		return nil
	}
	if c.dictation {
		return nil // no cards, runs, observed conversations or resume state for an unbound dictation
	}
	c.runs.arm() // its cards (asks.go): the call is set up; before the runs handed back, which a card may cover
	if len(resume) > 0 {
		c.runs.resume(resume)
	}
	switch {
	case c.meeting != nil:
		// Only what the call hands over is said: the room's own turns (batches, heard asks, photos) are not
		// followed (meeting.go).
	case c.version >= 2:
		c.runs.observeTarget(linkKey{c.target.MachineID, c.target.SessionID}) // after the runs handed back
	}
	return nil
}

// historyForConnect includes a line flushed by the old session only when its write succeeded. The socket
// may already have dropped while its writer still owns the outcome; a failed line goes to the new one as input.
func (c *conversation) historyForConnect() ([]prompts.VoiceLine, error) {
	c.mu.Lock()
	for c.sus.textWrite != nil {
		done := c.sus.textWrite
		c.mu.Unlock()
		select {
		case <-done:
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		}
		c.mu.Lock()
	}
	defer c.mu.Unlock()
	if !c.started && !c.resumed {
		return nil, nil
	}
	return voiceLines(c.history), nil
}

// startCallSession negotiates one call session on up (already dialed), falling back when the service refuses
// text-only output (error frame or echoed non-text output_modalities) or auto function tools (#1420).
// Returns the session's audio rate, whether text-only output was granted, the connected upLink, and any error.
func (c *conversation) startCallSession(ctx context.Context, up upLink, p *Provider, w wire, model, voice string,
	in prompts.VoiceInstructions, tools []string, auto, wantTextOnly bool) (int, bool, upLink, error) {
	upVoice := voice
	if wantTextOnly {
		// External TTS uses the app's voice; never send it to the realtime upstream (including fallback or codex).
		upVoice = ""
	}
	textOnly := wantTextOnly && supportsTextOnly(w) && !refusesTextOnly(p)
	droppedTools := false
	droppedTextOnly := false
	ambiguousTextOnly := false
	textOnlyWhy := ""

	for {
		var started event
		rate, err := c.startSession(ctx, up, w, p, model, upVoice, in, tools, textOnly, func(e event) { started = e })
		if err == nil {
			if textOnly && nonTextModalities(started.modalities) {
				// The service started the session but echoed output modalities other than text-only: close it,
				// remember the refusal and restart without requesting text-only output.
				_ = up.CloseNow()
				textOnly = false
				droppedTextOnly = true
				ambiguousTextOnly = false
				textOnlyWhy = fmt.Sprintf("echoed %v", started.modalities)
				if forgoTextOnly(p) {
					c.logf("provider %s: the service refused text-only output (%s): call output audio will be discarded", p.ID, textOnlyWhy)
				}
				up, err = c.dial(ctx, p, w, model)
				if err != nil {
					return 0, false, nil, err
				}
				continue
			}
			if droppedTools {
				c.toolless.Store(true)
				if c.pl.forgoTools(p.ID) {
					c.logf("provider %s: its sessions go without function tools from now on", p.ID)
				}
			}
			if droppedTextOnly && forgoTextOnly(p) {
				c.logf("provider %s: the service refused text-only output (%s): call output audio will be discarded", p.ID, textOnlyWhy)
			}
			return rate, textOnly, up, nil
		}

		// Start failed (startUpstream already closed up). Decide whether to retry without text-only or tools.
		switch {
		case textOnly && refusedTextOnly(err):
			textOnly = false
			droppedTextOnly = true
			ambiguousTextOnly = !explicitTextOnly(err)
			textOnlyWhy = textOnlyRefusalReason(err)
		case len(tools) > 0 && auto && refusedTools(err):
			// The service does not take function tools (#1420): the same session without them, once. Remembered only
			// once that one starts: a session refused all the same was not refused for its tools.
			c.logf("provider %s: the service refused the function tools as the session started (%s); starting it again without them", p.ID, failure(err))
			tools = nil
			droppedTools = true
			if ambiguousTextOnly {
				// Dropping text-only did not clear an ambiguous field error; try dropping tools while keeping text-only.
				textOnly = true
				droppedTextOnly = false
				ambiguousTextOnly = false
				textOnlyWhy = ""
			}
		default:
			return 0, false, up, err
		}
		up, err = c.dial(ctx, p, w, model)
		if err != nil {
			return 0, false, nil, err
		}
	}
}

// startSession starts the upstream session on up, declaring tools (nil: none), and waits for the service to start it:
// the rate of its audio. A failure closes up; the service's refusal is a *sessionRefused.
func (c *conversation) startSession(ctx context.Context, up upLink, w wire, p *Provider, model, voice string,
	in prompts.VoiceInstructions, tools []string, textOnly bool, onStarted ...func(event)) (int, error) {
	s := session{Model: model, Voice: voice, InputRate: p.inputRate(), Instructions: c.instructions(in, tools),
		Tools: toolDecls(tools, c.meeting != nil, string(c.language())), TextOnly: textOnly}
	return startUpstream(ctx, up, w, p, s, onStarted...)
}

// instructions are what a session that declares tools is told: voice/instructions, or a meeting call's voice/meeting
// (only the owner's name of in's owner fields, never their notes).
func (c *conversation) instructions(in prompts.VoiceInstructions, tools []string) string {
	if c.meeting != nil {
		return fitMeeting(meetingInstructions(in, len(tools) > 0))
	}
	in.Tools = c.toolMode(tools)
	return fitInstructions(in)
}

// startUpstream sends session s's start on up and waits for the service to start it: the rate of its audio. A failure
// closes up; the service's refusal is a *sessionRefused. A call's sessions and a read-aloud's (speak.go) start alike.
// onStarted, when given, receives the started event before the return (dictation reads its interim-caption grant;
// calls inspect echoed output_modalities).
func startUpstream(ctx context.Context, up upLink, w wire, p *Provider, s session, onStarted ...func(event)) (int, error) {
	if err := writeJSON(ctx, up, w.start(s)); err != nil {
		_ = up.CloseNow()
		return 0, err
	}
	for {
		typ, b, err := up.Read(ctx)
		if err != nil {
			_ = up.CloseNow()
			return 0, fmt.Errorf("the session did not start: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		for _, e := range w.decode(b) {
			switch e.kind {
			case evStarted:
				if s.TextOnly && e.name == "created" {
					// OpenAI Realtime sends session.created with the server's default modalities before answering
					// session.update; wait for session.updated (or an error refusing it).
					continue
				}
				if len(onStarted) > 0 {
					onStarted[0](e)
				}
				if e.rate == 0 {
					return w.outputRate(p.inputRate()), nil
				}
				return e.rate, nil
			case evError:
				_ = up.CloseNow()
				var err error = &sessionRefused{code: e.code, param: e.param, text: e.text}
				if e.code == codeUpstreamUnavailable {
					err = &upstreamError{code: codeUpstreamUnavailable, err: err}
				}
				return 0, err
			}
		}
	}
}

// sessionRefused is the service's error frame in answer to a session start.
type sessionRefused struct{ code, param, text string }

func (e *sessionRefused) Error() string {
	return firstNonEmpty(e.text, e.code, "the service refused the session")
}

// fieldCodes are the codes of an error about a field the service does not know or take (docs/lessons/voice.md
// "Errors": unknown_parameter, unsupported_extension).
var fieldCodes = []string{"unknown_parameter", "unsupported_parameter", "unsupported_extension", "unsupported_capability"}

// refusedTools: the service refused a session start for its function tools (#1420). Its error frame names them (param
// session.tools), or names no field and says "tools" or is about a field it does not know or take: the tools are all a
// session start has beyond what one without them has. Or it closed the socket before the session started, saying
// "tools". Never a refused key or handshake, a service that is down, nor a socket that just went away.
func refusedTools(err error) bool {
	var ue *upstreamError
	if errors.As(err, &ue) {
		return false
	}
	var r *sessionRefused
	if errors.As(err, &r) {
		if r.param != "" {
			return namesTools(r.param)
		}
		return namesTools(r.text) || slices.Contains(fieldCodes, r.code)
	}
	var ce websocket.CloseError
	return errors.As(err, &ce) && namesTools(ce.Reason)
}

func namesTools(s string) bool { return strings.Contains(strings.ToLower(s), "tools") }

// nonTextModalities reports whether a started/updated session echoed output modalities other than text-only.
// An omitted list means the service accepted the start without echoing modalities.
func nonTextModalities(modalities []string) bool {
	return len(modalities) > 0 && !(len(modalities) == 1 && modalities[0] == "text")
}

// refusedTextOnly reports whether a call session's start failed because the service refused text-only output:
// the same detection dictation and meeting transcription use (refusedField), except an error that explicitly
// names tools belongs to the function-tools fallback instead.
func refusedTextOnly(err error) bool {
	if upstreamCode(err) != "" {
		return false
	}
	var refused *sessionRefused
	if errors.As(err, &refused) {
		if refused.param != "" && namesTools(refused.param) {
			return false
		}
		if refused.param == "" && namesTools(refused.text) && !strings.Contains(strings.ToLower(refused.text), "modalit") {
			return false
		}
		return refusedField(refused, false) == "text_only"
	}
	var ce websocket.CloseError
	return errors.As(err, &ce) && strings.Contains(strings.ToLower(ce.Reason), "modalit")
}

// explicitTextOnly reports whether a text-only refusal error explicitly named modalities or output (rather than
// matching only on a bare unknown_parameter / unsupported_* code with no param).
func explicitTextOnly(err error) bool {
	var refused *sessionRefused
	if errors.As(err, &refused) {
		words := strings.ToLower(refused.param + " " + refused.text)
		return strings.Contains(words, "modalit") || strings.Contains(words, "output")
	}
	var ce websocket.CloseError
	return errors.As(err, &ce) && strings.Contains(strings.ToLower(ce.Reason), "modalit")
}

func textOnlyRefusalReason(err error) string {
	var refused *sessionRefused
	if errors.As(err, &refused) {
		return firstNonEmpty(refused.code, "no code")
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return fmt.Sprintf("close %d", ce.Code)
	}
	return "no code"
}

// instructionsMost is the most a session's instructions run to, in runes: the measured service started a session with
// 19000 characters of them and refused one with 25000, the limit counting what it adds itself (docs/lessons/voice.md
// "Instructions size"); refused, a session does not start at all. A long call's history (historyLines of up to 200
// runes) with a full snapshot comes near that in English.
const instructionsMost = 16000

// fitInstructions renders in, the oldest lines of its history left out as far as it takes to keep within
// instructionsMost.
func fitInstructions(in prompts.VoiceInstructions) string {
	s := prompts.RenderVoiceInstructions(in)
	for runeLen(s) > instructionsMost && len(in.History) > 0 {
		in.History = in.History[1:]
		s = prompts.RenderVoiceInstructions(in)
	}
	return s
}

// dial opens the upstream socket: the service's WebSocket, or the fake dialect's stand-in.
func (c *conversation) dial(ctx context.Context, p *Provider, w wire, model string) (upLink, error) {
	return dialUpstream(ctx, p, w, model)
}

// dialUpstream opens provider p's socket for model, with its credential: the service's WebSocket, or the fake
// dialect's stand-in.
func dialUpstream(ctx context.Context, p *Provider, w wire, model string) (upLink, error) {
	if p.Dialect == DialectFake {
		return newFakeLink(), nil
	}
	key, err := p.credential(ctx)
	if err != nil {
		return nil, err
	}
	u, err := w.url(p.URL, model)
	if err != nil {
		return nil, err
	}
	header := w.header()
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}
	opts := &websocket.DialOptions{HTTPHeader: header, HTTPClient: p.httpClient()}
	up, res, err := websocket.Dial(ctx, u, opts)
	if err != nil {
		return nil, dialError(err, res)
	}
	up.SetReadLimit(16 << 20)
	return up, nil
}

// toolSet is which of the plugin's function tools the session declares (docs/assistant/voice.md "Permissions"): all of them for a full
// device; for another extension calling (role extension) only ask_persona, to the main conversation, which is what it may
// reach: the others reach past that (the knowledge base, any agent), and without a tool the measured service hands
// nothing over at all (docs/lessons/voice.md "Other behavior"); none for an upstream that takes no tools (Provider.Tools
// false, the codex and fake dialects, a service that refused them: toolless).
func (c *conversation) toolSet() []string {
	declare, _ := c.p.toolsMode()
	switch {
	case c.dictation:
		return nil // transcription only: no function tools, however the provider's ordinary calls are configured
	case !declare || c.toolless.Load():
		return nil
	case c.meeting != nil:
		// A meeting call hands every question to the room, and has nothing else: the room may hear what a tool
		// brings back (meeting.go).
		return meetingTools
	case c.version >= 2:
		// Bound to a conversation: what hands work over goes to it (delegate_task); send_to_agent and ask_persona would
		// go past it. The measured service hands nothing over without a tool (docs/lessons/voice.md "Other behavior").
		return boundTools
	case c.role == "" || c.role == roleFull:
		return toolNames
	}
	return []string{"ask_persona"}
}

// tools: the session declares function tools (a delegation comes with its call).
func (c *conversation) tools() bool { return len(c.toolSet()) > 0 }

// toolMode is what the instructions say of tools (prompts.VoiceInstructions.Tools) for a session that declares tools.
func (c *conversation) toolMode(tools []string) string {
	switch {
	case c.meeting != nil && len(tools) > 0:
		return "meeting" // the log's word: voice/meeting has its own (meeting.go)
	case c.target != nil && len(tools) > 0:
		return "bound"
	case len(tools) == len(toolNames):
		return "all"
	case len(tools) > 0:
		return "ask_persona"
	}
	return ""
}

// dialError adds what the service answered to a refused handshake (the library keeps the first KB of its body).
func dialError(err error, res *http.Response) error {
	if res == nil {
		return err
	}
	if res.Body == nil {
		res.Body = http.NoBody
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	err = fmt.Errorf("HTTP %d", res.StatusCode)
	if msg != "" {
		err = fmt.Errorf("HTTP %d: %s", res.StatusCode, clipRunes(msg, 200))
	}
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &upstreamError{code: codeUpstreamForbidden, err: err}
	case http.StatusServiceUnavailable:
		return &upstreamError{code: codeUpstreamUnavailable, err: err}
	}
	return err
}

func (c *conversation) readUpstream(up upLink, gen int) {
	for {
		typ, b, err := up.Read(c.ctx)
		if err != nil {
			c.lost(gen, err)
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		c.mu.Lock()
		if c.gen != gen || c.closed {
			c.mu.Unlock()
			return
		}
		var events []event
		if c.dictation {
			events = c.dictationEvents(b)
		} else {
			events = c.w.decode(b)
		}
		for _, e := range events {
			c.handle(gen, e)
		}
		c.mu.Unlock()
	}
}

// handle applies one upstream event (mu held).
func (c *conversation) handle(gen int, e event) {
	if c.dictation {
		c.handleDictation(gen, e)
		return
	}
	switch e.kind {
	case evAudio:
		if c.tts != nil {
			return // streaming TTS replaces upstream audio (including text-only refusal fallback)
		}
		now := time.Now()
		c.replyVoiced = true
		if c.dropAudio && (now.Sub(c.lastAudio) > 1500*time.Millisecond || c.replyAfterCut(now)) {
			c.dropAudio, c.cutAt = false, time.Time{} // a pause: this is the next answer
		}
		c.lastAudio = now
		if c.dropAudio {
			return // the rest of an answer cut off: the user never hears it, so it answers nothing (nudge.go)
		}
		if !c.turnsEnd && c.replyAudio.chunks > 0 && now.Sub(c.replyAudio.lastAt) > 1500*time.Millisecond {
			c.finishReplyAudio(false) // upstream sends no turn-end signal: flush the earlier reply after a quiet gap
		}
		c.replyAudio.add(now, len(e.audio), c.outRate)
		c.modelActed(now) // before its caption of the user's line closes, if that is still open: this answers it
		c.pace.played(now, len(e.audio), c.outRate)
		c.closeUser()
		select {
		case c.out <- outFrame{audio: e.audio}:
		case <-c.ctx.Done():
		}
	case evUser:
		c.dropAudio, c.cutAt = false, time.Time{}
		if c.tts != nil && !c.turnsEnd {
			c.ttsDropText = false
		}
		spoke := c.spokeAt(time.Now()) // by the user's words before these
		if strings.TrimSpace(e.text) != "" {
			c.gate.user = time.Now()
			c.gate.caption, c.userAt = c.gate.user, c.gate.user
		}
		c.userLineDone(c.userText(e.text, e.replace, e.final, spoke)) // a line the service ends itself (codex)
	case evAssistant:
		if strings.TrimSpace(e.text) != "" && !(c.tts != nil && c.ttsDropText) {
			c.modelActed(time.Now())
		}
		c.asstText(e.text, e.replace, e.final, e.name)
		if c.tts != nil {
			c.feedTTSLocked(e)
		}
	case evReply:
		c.signals, c.turnsEnd = true, true
		c.reply, c.replyVoiced = e.id, false
		c.replying = true
		if c.tts != nil {
			if c.ttsCurGen != 0 {
				c.endTTSTextLocked()
			}
			c.ttsDropText = false
			c.ttsCutReply = ""
			c.ttsCutAt = time.Time{}
			c.ttsReplyHadTurn = false
			c.ttsCurReply = e.id
			c.ttsCurStart = time.Now()
			c.ttsFedText = ""
		} else {
			c.openReply(time.Now())
		}
	case evTurnEnd:
		c.dropAudio, c.cutAt = false, time.Time{}
		c.turnsEnd = true
		c.replying = false
		if c.createOwed {
			c.createOwed = false
			go c.sendUp(gen, responseCreate)
		}
		if e.id != "" {
			c.signals = true
			if c.replied = append(c.replied, e.id); len(c.replied) > repliedMost {
				c.replied = c.replied[1:]
			}
		}
		if c.tts != nil {
			wasCut := c.ttsDropText || (e.id != "" && e.id == c.ttsCutReply)
			hadTurn := c.ttsReplyHadTurn || c.ttsCurGen != 0
			c.endTTSTextLocked()
			c.ttsReplyHadTurn = false
			c.ttsDropText = false
			c.ttsCutReply = ""
			if !hadTurn && !wasCut && e.id != "" && e.id == c.reply && !c.replyVoiced {
				c.markSilentLocked(e.id)
			}
		} else {
			if e.id != "" && e.id == c.reply && !c.replyVoiced {
				c.markSilentLocked(e.id)
			}
			c.closeReply(false)
		}
		c.closeAsst()
	case evInterrupted:
		if e.id != "" {
			c.signals = true
		}
		if e.id != "" && slices.Contains(c.replied, e.id) {
			// A reply that was over already, superseded by the next one (a tool's result, a say): nothing of it is left
			// to drop, and a clear would cut the end of it the app still plays (docs/lessons/voice.md"Other behavior"). On
			// either protocol (#4719): protocol 1's apps play behind the upstream the same way.
			return
		}
		if c.tts != nil {
			now := time.Now()
			if c.ttsAlreadyCutLocked(now, e.id) {
				// Already cancelled and cleared by app interrupt or energy cut-in: drop remaining text without double-clearing.
				if c.replying || c.ttsCurGen != 0 {
					c.ttsDropText = true
				}
				c.closeAsst()
				return
			}
			c.ttsCutLocked(now, true)
			c.pace.playEnd = now // the app drops what it had not played
			c.barge = barge{}
			c.closeAsst()
			return
		}
		c.sendApp(map[string]any{"type": "clear"})
		c.closeReply(true)
		c.pace.playEnd = time.Now() // the app drops what it had not played
		c.closeAsst()
	case evDelegation:
		c.modelActed(time.Now())
		c.closeUser()
		c.pace.delegated = time.Now()
		if e.id == "" {
			return
		}
		c.delegations[e.id] = gen
		c.count(func(l *callLog) { l.delegations++ })
		at, reply := c.gate.agent, c.reply // the gate as it was when the model handed this over, and its reply doing so
		if c.tools() {
			c.logf("delegation %s: made in session %d, waiting %v for its function call", e.id, sessionNo(gen), toolCallWait)
			id, text := e.id, e.text
			c.pending[id] = time.AfterFunc(toolCallWait, func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				if _, ok := c.pending[id]; ok && !c.closed {
					delete(c.pending, id)
					c.logf("delegation %s: no function call came: handed over as it is", id)
					c.startTask(id, text, at, reply)
				}
			})
			return
		}
		c.logf("delegation %s: made in session %d", e.id, sessionNo(gen))
		c.startTask(e.id, e.text, at, reply)
	case evToolCall:
		c.modelActed(time.Now())
		c.closeUser()
		c.pace.delegated = time.Now()
		c.count(func(l *callLog) { l.calls++ })
		c.logf("function call %s (call %s, delegation %s, session %d)", compat.Tool(e.name), firstNonEmpty(e.callID, "-"),
			firstNonEmpty(e.id, "-"), sessionNo(gen))
		if t := c.pending[e.id]; t != nil {
			t.Stop()
			delete(c.pending, e.id)
		}
		c.runTool(gen, e)
	case evAck:
		if s := c.unacked[e.eventID]; s != nil {
			delete(c.unacked, e.eventID)
			if s.agent {
				c.gate.told(time.Now(), agentAcked) // the model has read them only now
			}
		}
	case evError:
		c.logf("upstream: session error (%s, %s): %s", e.code, firstNonEmpty(e.param, "-"), clipRunes(e.text, errorRunes))
		if e.code == codeUpstreamUnavailable {
			c.down.session, c.down.text = sessionNo(gen), clipRunes(e.text, errorRunes)
		}
		c.serviceRefused(e)
	}
}

// sendUp writes to the current upstream session, if it is still gen (any gen when gen < 0).
func (c *conversation) sendUp(gen int, frames ...any) bool {
	c.mu.Lock()
	up, cur, ready := c.up, c.gen, c.ready
	if ready && up != nil && (gen < 0 || gen == cur) && c.replying {
		// A response.create while a reply is under way is refused: it goes when that reply is over (evTurnEnd).
		frames = slices.DeleteFunc(slices.Clone(frames), func(f any) bool {
			if isResponseCreate(f) {
				c.createOwed = true
				return true
			}
			return false
		})
	}
	c.mu.Unlock()
	if !ready || up == nil || (gen >= 0 && gen != cur) {
		return false
	}
	for _, f := range frames {
		if err := c.writeUp(up, f); err != nil {
			return false
		}
	}
	return true
}

// responseCreate asks the service for a response (realtime dialect).
var responseCreate = map[string]any{"type": "response.create"}

func isResponseCreate(f any) bool {
	m, ok := f.(map[string]any)
	return ok && m["type"] == "response.create"
}

// lost: the upstream session went away while the app is still here; a new one is told what was said.
func (c *conversation) lost(gen int, why error) {
	c.mu.Lock()
	if c.closed || gen != c.gen {
		c.mu.Unlock()
		return
	}
	held := time.Duration(0)
	if !c.connectedAt.IsZero() {
		held = time.Since(c.connectedAt).Round(time.Second)
	}
	c.logf("upstream: session %d lost after %v: %v", sessionNo(gen), held, why)
	if c.dictation {
		down, text := c.down.session == sessionNo(gen), c.down.text
		c.mu.Unlock()
		c.dictationLost(down, text, why)
		return
	}
	if !c.connectedAt.IsZero() && time.Since(c.connectedAt) >= stableAfter {
		c.reconnects = 0 // it held: a drop now is not one of a run of them (docs/assistant/voice.md "Reconnect and resume")
	}
	if c.reconnects >= maxReconnects {
		if c.tts != nil {
			c.ttsDropText = false
			c.ttsCutReply = ""
			c.ttsCutAt = time.Time{}
			c.cancelTTSLocked(false)
		}
		c.closeReply(true)
		down, text := c.down.session == sessionNo(gen), c.down.text
		c.mu.Unlock()
		if down {
			// The service said this session's model is down (it may start a session and only then say so): what the
			// app shows is the service being unavailable, not a dropped connection.
			c.failCode(codeUpstreamUnavailable, i18n.T(c.language(), "voice-upstream.unavailable",
				i18n.Args{"reason": firstNonEmpty(text, codeUpstreamUnavailable)}), true)
			return
		}
		c.failCode(codeUpstreamFailed, "语音连接断了，重连也没成功。点下面的按钮重新开始", true)
		return
	}
	c.reconnects++
	c.count(func(l *callLog) { l.reconnects++ })
	c.gen++
	c.ready = false
	c.replying, c.createOwed = false, false // that session's reply, and what waited on it
	old := c.up
	c.up = nil
	for id, t := range c.pending {
		t.Stop()
		delete(c.pending, id)
	}
	clear(c.unacked) // that session's: an ack or refusal of it never comes
	c.pace.playEnd = time.Now()
	if c.tts != nil {
		c.ttsDropText = false
		c.ttsCutReply = ""
		c.cancelTTSLocked(false)
	}
	c.sendApp(map[string]any{"type": "clear"})
	c.closeReply(true)
	c.closeAsst()
	c.closeUser()
	if c.owed.result {
		c.owed = owedAnswer{} // what was handed to that session went with it; the user's line is in the next one's history
	}
	if c.version >= 2 {
		c.sendApp(map[string]any{"type": "link", "state": linkReconnecting})
	} else {
		c.sendApp(map[string]any{"type": "tool", "id": "reconnect", "label": "连接断了，重新连接中", "done": false})
	}
	c.mu.Unlock()
	if old != nil {
		_ = old.CloseNow()
	}
	go func() {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(time.Duration(attempt) * 2 * time.Second):
				}
			}
			if err = c.connect(false); err == nil {
				c.flushRing() // a woken session that dropped before it had what was kept while paused (suspend.go)
				return
			}
			if c.ctx.Err() != nil {
				return
			}
			c.logf("reconnect (try %d of 3) failed: %s", attempt+1, failure(err))
		}
		c.failCode(firstNonEmpty(upstreamCode(err), codeUpstreamFailed), "语音连接断了，重连没成功："+err.Error(), true)
	}()
}
