package voice

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Meeting calls (#3862 phase 4; docs/assistant/meeting-minutes.md "Phase 4: realtime voice in the meeting",
// docs/assistant/voice.md "Meeting calls"). The iPhone holds one call for a whole meeting, on the recorder's own capture,
// bound to the meeting's room conversation: a protocol 2 start with mode "meeting", the recording and the room as its
// target. The room is checked with the hub first (GET …/live: that recording's room, ready), and a room takes no other
// call (meeting_room): an ordinary call would bring search_knowledge and list_agents in front of the room.
//
// Everyone in the room may hear the fast layer, so it is told only the owner's name and the language (voice/meeting:
// never their notes), its only tool is delegate_task, which hands the question to the room, and its snapshot is the
// room's own last questions and answers, read from the room (never the app's recent, which may hold lines the room
// must not know). It says only what it handed over: the room's other turns (batches, heard asks, photos) are not
// followed, and the upstream never pauses (the meeting goes on for an hour, and a wake would cost the first words of
// the next Talk).
//
// The model hears only Talk: the app sends talk{on} and talk{off} around what the owner says to it, and between those
// spans the extension feeds the upstream silence itself, so the session stays open and the model hears nothing. The
// app is not trusted with that: its audio outside a span is dropped and logged, and the silence goes in its place, and
// a span it leaves open past talkMost is closed, so a phone bug cannot stream the meeting to the voice service.
//
// What it hands the room goes as the owner's words, so the user-speech gate (gate.go) always holds: only what the owner
// just said in Talk, or typed, opens it. A start's speech_gate (#5904) neither turns it off nor brings Send Anyway: a
// question the gate held back is not kept for a resend (the meeting page has no such button), and the owner says it
// again in the next Talk.

const (
	// modeMeeting is start.mode of a meeting call.
	modeMeeting = "meeting"
	// originMeeting is the origin of a meeting's room conversation (proto.Session.Origin, the manager's
	// assistant.OriginMeeting).
	originMeeting = "meeting"
	// roomTurns is how many of the room's last exchanges its snapshot is read from: most are batches of the meeting's
	// lines, which it leaves out, so it reads further back than an ordinary call's three.
	roomTurns = 12
)

// meetingTools are a meeting call's function tools: only the one that hands the question to the room.
var meetingTools = []string{"delegate_task"}

// silenceTick is how often, and how much, silence the extension feeds the upstream between Talk spans.
var silenceTick = 100 * time.Millisecond

// talkMost is how long a Talk span stays open without the app's talk{off}: a little longer than the app's own
// longest (the Live Activity's talk window, 60 s). Past it the extension closes the span itself (capTalk), so a lost
// off (a gesture the system took over, a window whose timer never ran) cannot send the meeting to the voice service.
var talkMost = 90 * time.Second

// meetingCall is a meeting call's state beyond its target (under conversation.mu).
type meetingCall struct {
	recording string
	talk      bool      // a Talk span is open (talk on, not yet off)
	talkAt    time.Time // it opened
	spans     int       // Talk spans so far
	frames    int       // the app's audio frames of the open span
	inside    int       // the app's audio frames forwarded, in every span
	outside   int       // the app's audio frames outside a span: dropped, the extension's silence went instead
	stray     bool      // the frames outside a span now were logged (once a gap between spans)
	capped    int       // spans the extension closed itself, open past talkMost
}

// roomRead is what the hub said of a meeting call's recording's room.
type roomRead struct {
	room *liveRoom
	err  error
}

// checkRoom is nil when target machine/session is recording's room and it is ready (r, what the hub said of it); else
// the call's fatal error: not_meeting_room, or none when the hub could not say (as when the target cannot be looked
// up: not known not to be the room).
func (c *conversation) checkRoom(recording string, r roomRead, machine, session string) error {
	notRoom := func(why string) error {
		c.logf("meeting %s: %s: not_meeting_room", recording, why)
		return &codedError{code: codeNotMeetingRoom, msg: i18n.T(c.language(), "voice-meeting.not_meeting_room")}
	}
	var he *hubError
	switch {
	case r.err != nil && errors.As(r.err, &he) && he.Status >= 400 && he.Status < 500 && he.Status != http.StatusTooManyRequests:
		return notRoom(fmt.Sprintf("the hub answers HTTP %d for its room", he.Status)) // no such recording, a bad id
	case r.err != nil:
		c.logf("meeting %s: its room: %s", recording, hubOutcome(r.err))
		return &codedError{msg: i18n.T(c.language(), "voice-meeting.unreachable")}
	case r.room == nil:
		return notRoom("the recording has no room")
	case r.room.State != "ready":
		return notRoom(fmt.Sprintf("its room is %q", r.room.State))
	case r.room.MachineID != machine || r.room.SessionID != session:
		return notRoom("the target is not its room")
	}
	return nil
}

// isMeetingRoom: s is a meeting's room. The manager says origin meeting on the room's row; a manager from before it
// did lists the room as a plain Claude Code session, so its directory tells as well: <state dir>/guest-meeting/<id>,
// where only rooms run (node/assistant_guest.go meetingRoomDir; the recording's id is a UUID).
func isMeetingRoom(s *proto.Session) bool {
	if s.Origin == originMeeting {
		return true
	}
	if s.Cwd == "" || s.PaneID != "" {
		return false
	}
	dir := path.Clean(filepath.ToSlash(s.Cwd))
	parent := path.Dir(dir)
	return roomID.MatchString(path.Base(dir)) && path.Base(parent) == "guest-meeting" && compat.IsStateDir(path.Base(path.Dir(parent)))
}

// roomID is a room directory's name: its recording's id.
var roomID = lazyre.New(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// snapshotTurns is how many of the bound conversation's last exchanges its snapshot is read from.
func (c *conversation) snapshotTurns() int {
	if c.meeting != nil {
		return roomTurns
	}
	return 3
}

// recentFrom is the snapshot's last turns of the bound conversation, from its last exchanges (items).
func (c *conversation) recentFrom(items []proto.Item) []prompts.VoiceLine {
	if c.meeting != nil {
		return roomRecent(c.language(), items)
	}
	return recentOf(c.language(), items)
}

// roomRecent is a room's last questions and answers in items: the owner's own messages (no message source: not a
// batch, a heard ask, a photo or the meeting's header) and the room's replies to them, made speakable and cut. What
// the room wrote after any other message (a heard ask's answer, a batch or a photo it wrote about) goes with that
// message: kept, it would read as the answer to the owner's question before it. Tool calls (the topics) are left out.
func roomRecent(l i18n.Lang, items []proto.Item) []prompts.VoiceLine {
	var out []prompts.VoiceLine
	answering := false // the last user message was the owner's own: the room's text from here on answers it
	for _, it := range items {
		if it.Kind != proto.KindText {
			continue
		}
		switch it.Role {
		case "user":
			answering = it.Source == nil && it.Injected == nil && it.From == ""
		case "assistant":
		default:
			continue
		}
		if !answering {
			continue // a reply whose question the window does not show is left out as well
		}
		if line := recentLine(l, it.Role, it.Text); line != nil {
			out = append(out, *line)
		}
	}
	if len(out) > recentMost {
		out = out[len(out)-recentMost:]
		for len(out) > 0 && out[0].Role != "user" {
			out = out[1:] // its question was cut
		}
	}
	return out
}

// meetingInstructions is what a meeting call's session is told (voice/meeting), from what any call's would be (in):
// the owner's name, the language, the room's title and last turns, what this call said and waits for. Never in.Notes.
func meetingInstructions(in prompts.VoiceInstructions, tool bool) prompts.VoiceMeeting {
	m := prompts.VoiceMeeting{Lang: in.Lang, Owner: in.Owner, Tool: tool, History: in.History, Waiting: in.Waiting, TTS: in.TTS}
	if in.Target != nil {
		m.Title, m.Recent = in.Target.Title, in.Target.Recent
	}
	return m
}

// fitMeeting renders in, the oldest lines of its history left out as far as it takes to keep within
// instructionsMost (fitInstructions).
func fitMeeting(in prompts.VoiceMeeting) string {
	s := prompts.RenderVoiceMeeting(in)
	for runeLen(s) > instructionsMost && len(in.History) > 0 {
		in.History = in.History[1:]
		s = prompts.RenderVoiceMeeting(in)
	}
	return s
}

// talkFrame takes the app's talk frame: a Talk span opens (on) or closes. A call that is not a meeting call ignores it
// (logged once): its audio is the user speaking to it all along.
func (c *conversation) talkFrame(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.meeting
	if m == nil {
		if !c.talkIgnored {
			c.talkIgnored = true
			c.logf("talk frames on a call that is not a meeting call: ignored")
		}
		return
	}
	now := time.Now()
	c.capTalk(now)
	if on == m.talk {
		return // said again, or the off of a span closed past talkMost
	}
	if on {
		m.talk, m.talkAt, m.frames, m.stray = true, now, 0, false
		m.spans++
		c.logf("talk %d: on", m.spans)
		return
	}
	m.talk = false
	c.logf("talk %d: off after %v, %d frames of audio", m.spans, logDur(now.Sub(m.talkAt)), m.frames)
}

// talkGate says whether a frame of the app's audio, n bytes long, goes to the upstream (mu held): always on an ordinary
// call; on a meeting call only inside a Talk span. Outside one it is dropped (the extension's silence goes instead,
// feedSilence) and logged, once a gap.
func (c *conversation) talkGate(n int) bool {
	m := c.meeting
	if m == nil {
		return true
	}
	c.capTalk(time.Now())
	switch {
	case m.talk:
		m.frames++
		m.inside++
		return true
	}
	m.outside++
	if !m.stray {
		m.stray = true
		c.logf("the app sent audio outside a Talk span (%d bytes): dropped, the upstream hears silence", n)
	}
	return false
}

// capTalk closes a meeting call's Talk span that has been open longer than talkMost at now (mu held): from then on
// the upstream hears the extension's silence and the app's audio is dropped, until the next talk{on}.
func (c *conversation) capTalk(now time.Time) {
	m := c.meeting
	if m == nil || !m.talk || now.Sub(m.talkAt) <= talkMost {
		return
	}
	m.talk, m.stray = false, false
	m.capped++
	c.logf("talk %d: no talk off after %v, %d frames of audio: closed, the upstream hears silence until the next talk on",
		m.spans, logDur(now.Sub(m.talkAt)), m.frames)
}

// feedSilence feeds a meeting call's upstream silence, tick by tick, while no Talk span is open and a session is up,
// until the call ends.
func (c *conversation) feedSilence(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	var zeros []byte
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		c.capTalk(time.Now()) // a span whose off never came: closed even while the app sends nothing
		up, w, ready, open, rate := c.up, c.w, c.ready, c.meeting.talk, 0
		if c.p != nil {
			rate = c.p.inputRate()
		}
		c.mu.Unlock()
		if !ready || up == nil || open || rate <= 0 {
			continue
		}
		if n := int(int64(rate)*int64(tick)/int64(time.Second)) * 2; len(zeros) != n { // PCM16, mono
			zeros = make([]byte, n)
		}
		_ = c.writeUp(up, w.audio(zeros)) // a stuck session: its reader sees the close and lost runs
	}
}

// meetingLog is what the call's last line says of a meeting call ("" for another).
func (m *meetingCall) meetingLog() string {
	if m == nil {
		return ""
	}
	capped := ""
	if m.capped > 0 {
		capped = fmt.Sprintf(" (%d closed past %v)", m.capped, logDur(talkMost))
	}
	return fmt.Sprintf(", talk spans %d%s, audio frames %d (outside a span %d)", m.spans, capped, m.inside, m.outside)
}
