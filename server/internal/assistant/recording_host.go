package assistant

// What the recording module asks of the core (internal/recordinghost.Host): recordingHost hands each on to the
// Service, and holds the parts of the core's work that are only the module's question (attendeeMachine, attendeeRow,
// turnSettling, the evening routine's clock the day recaps keep).

import (
	"context"
	"errors"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"maps"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/recordinghost"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcribejob"
)

// attendeeMachine says whether machine is one this manager carries, and whether its node can be reached now: the
// devices' own list when the manager has them (the fleet), else the hub's.
func (s *Service) attendeeMachine(ctx context.Context, machine string) (ours, online bool) {
	if s.devs == nil {
		d, ok := s.device(ctx, machine)
		return ok, d.Online && d.Reachable
	}
	for _, d := range s.devs.List() {
		if d.ID == machine {
			return true, d.Online && d.Reachable
		}
	}
	return false, false
}

// attendeeRow is machine's session as the apps get it (its state, cards and origin decorated), and whether it is
// listed there.
func (s *Service) attendeeRow(ctx context.Context, machine, id string) (proto.Session, bool, error) {
	if s.fleetOn() {
		if _, ok := s.fleetMachine(machine); !ok {
			return proto.Session{}, false, nil
		}
		sess, ok := s.decoratedSession(machine, id)
		return sess, ok, nil
	}
	list, err := s.sessions(ctx, machine)
	if errors.Is(err, ErrNoDevice) {
		return proto.Session{}, false, nil
	}
	if err != nil {
		return proto.Session{}, false, err
	}
	for _, c := range list {
		if c.SessionID == id {
			return c, true, nil
		}
	}
	return proto.Session{}, false, nil
}

// turnSettling: machine's session's last turn ended less than its push's delay ago (and a little more) and is still
// followed: its push and the hooks of its end are decided against the session as it is then, so a batch waits.
func (s *Service) turnSettling(machine, id string, now time.Time) bool {
	ended, ok := s.turnsBook().endedAt(machine + "/" + id)
	return ok && now.Sub(ended) < s.notifyTimings().doneDelay()+15*time.Second
}

// endedAt is when the session's followed turn ended; false when none is followed or it has not ended.
func (t *turnTracker) endedAt(key string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr := t.turns[key]; tr != nil && !tr.endedAt.IsZero() {
		return tr.endedAt, true
	}
	return time.Time{}, false
}

// eveningClock is the evening routine's time of day, whether or not it is on (kb.Engine.DayRecapClock: the recap is
// due 30 minutes before it); -1 while the schedule can't be read or has no evening routine, which the knowledge base
// takes as the default routine's 21:30.
func (s *Service) eveningClock() (int, int) {
	ctx, cancel := context.WithTimeout(s.runCtx(), 5*time.Second)
	defer cancel()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return -1, -1
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if t.ID != "evening" {
			continue
		}
		if m := hhmm.FindStringSubmatch(t.Time); m != nil {
			hh, _ := strconv.Atoi(m[1])
			mm, _ := strconv.Atoi(m[2])
			return hh, mm
		}
	}
	return -1, -1
}

// channelRefused: a channel answered a send with a refusal of its own (a coded error, after the hub's tries), which
// sending it again would get too; not a send that got no answer (no code: a time-out, its program not running), the
// hub's own (a link that dropped) or one the hub asks to be sent again.
func channelRefused(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code != "" && re.Code != bus.CodeStaleConnection && re.Code != CodeTryAgain
}

// kbOwnerLanguage is the owner's language (kb.Engine.OwnerLanguage): what the minutes, summary and recap of a
// recording that says no language of its own are written in.
func (s *Service) kbOwnerLanguage() i18n.Lang { return s.lang(s.runCtx()) }

// recordingHost is the core as the recording module reaches it (recordinghost.Host).
type recordingHost struct{ s *Service }

var _ recordinghost.Host = recordingHost{}

func (h recordingHost) Logf(format string, args ...any)            { h.s.logf(format, args...) }
func (h recordingHost) Handle(pattern string, fn http.HandlerFunc) { h.s.handle(pattern, fn) }
func (h recordingHost) RunCtx() context.Context                    { return h.s.runCtx() }
func (h recordingHost) Hello(ctx context.Context) Hello            { return h.s.hello(ctx) }
func (h recordingHost) Event(ctx context.Context, ev map[string]any) {
	h.s.event(ctx, ev)
}
func (h recordingHost) Timings() (map[string]time.Duration, map[string][]time.Duration) {
	o := &h.s.recOpts
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.timings) == 0 && len(o.lists) == 0 {
		return nil, nil
	}
	return maps.Clone(o.timings), maps.Clone(o.lists)
}
func (h recordingHost) FeedClock() (func() time.Time, func(time.Duration, func()) func() bool) {
	o := &h.s.recOpts
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.now, o.after
}
func (h recordingHost) Lang(ctx context.Context) i18n.Lang { return h.s.lang(ctx) }
func (h recordingHost) AppLang(ctx context.Context, lang string) i18n.Lang {
	return h.s.appLang(ctx, lang)
}
func (h recordingHost) Location(ctx context.Context) *time.Location { return h.s.location(ctx) }
func (h recordingHost) Owner(ctx context.Context) string            { return h.s.owner(ctx) }
func (h recordingHost) WebBase(ctx context.Context) string          { return h.s.webBase(ctx) }
func (h recordingHost) MainSession(ctx context.Context) (machine, session string) {
	set := h.s.settings(ctx)
	return set.MachineID, set.Main
}
func (h recordingHost) MeetingGlossary(ctx context.Context) []string {
	return meetingGlossaryOf(h.s.settings(ctx))
}

func (h recordingHost) KB(ctx context.Context) (*kb.Engine, error) { return h.s.kbGet(ctx) }
func (h recordingHost) KBOff(err error) bool                       { return errors.Is(err, errKBOff) }
func (h recordingHost) KBWake()                                    { h.s.kbWake() }
func (h recordingHost) KBReadOnly(ctx context.Context) bool        { return h.s.kbReadOnly(ctx) }
func (h recordingHost) KBConnected() bool                          { return h.s.kbConnected() }
func (h recordingHost) SpeakerTurnsImport() bool                   { return h.s.speakerTurnsImport.Load() }

// Setting and PutSetting read and write the manager's settings rows; without a store none is read or written.
func (h recordingHost) Setting(ctx context.Context, key string, out any) (bool, error) {
	if st := h.s.store(); st != nil {
		return st.Setting(ctx, key, out)
	}
	return false, nil
}
func (h recordingHost) PutSetting(ctx context.Context, key string, v any) error {
	if st := h.s.store(); st != nil {
		return st.PutSetting(ctx, key, v)
	}
	return nil
}

func (h recordingHost) Notify(ctx context.Context, n Notification) error {
	res, err := h.s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n})
	if err == nil && n.ID != "" {
		h.s.rememberNotifyHeld(n.ID, res)
	}
	return err
}
func (h recordingHost) NoteCard(ctx context.Context, source string, n Notification) {
	h.s.noteCardResult(ctx, source, n, h.s.consumeNotifyHeld(n.ID))
}
func (h recordingHost) Answered(err error) bool { return answered(err) }

// PushRefused: a channel's push will be refused again if sent again (nowhere to send it, or its own refusal).
func (h recordingHost) PushRefused(err error) bool {
	return errors.Is(err, errCannotSend) || errors.Is(err, errNoChannel) || channelRefused(err)
}
func (h recordingHost) ChannelsPushed(machine, session, title, body string) {
	h.s.channelsPushed(machine, session, title, body)
}
func (h recordingHost) FeishuPushKeyed(ctx context.Context, machine, session, title, body, key string) error {
	return h.s.feishuPushKeyed(ctx, machine, session, title, body, key)
}
func (h recordingHost) ChannelPushTo(ctx context.Context, machine, session, title, body, ref string, took func(id string) bool) (map[string]error, error) {
	return h.s.channelPushTo(ctx, machine, session, title, body, ref, took)
}

// ActionTodos makes the to-dos of actions of list (kind: the minutes' or a day recap's, which names their ids).
func (h recordingHost) ActionTodos(ctx context.Context, list []kb.MinutesAction, actions []string, kind, recording string, version int,
	note func(kb.MinutesAction) string) ([]any, map[string]string, map[string]string, error) {
	todoID := func(aid string) string { return minutesTodoID(recording, version, aid) }
	if kind == todosOfRecap {
		todoID = func(aid string) string { return dayRecapTodoID(recording, version, aid) }
	}
	todos, made, byTask, err := h.s.actionTodos(ctx, list, actions, todoID, note)
	if todos == nil {
		return nil, made, byTask, err
	}
	out := make([]any, len(todos))
	for i, t := range todos {
		out[i] = t
	}
	return out, made, byTask, err
}
func (h recordingHost) ActionTodo(ctx context.Context, todoID string) string {
	return h.s.actionTodo(ctx, todoID)
}

// Model is purpose's model (meeting.answer's: the one tests give, else the configured one).
func (h recordingHost) Model(purpose string) kb.Model {
	if purpose == purposeMeetingAnswer && h.s.meetingModel != nil {
		return h.s.meetingModel
	}
	return h.s.purposeModel(purpose)
}
func (h recordingHost) ModelUnavailable(err error) bool {
	return ai.IsNotConfigured(err) || ai.IsPaused(err)
}
func (h recordingHost) ApplyModel(ctx context.Context, purpose string, params map[string]any) error {
	return h.s.applyModel(ctx, purpose, params)
}

func (h recordingHost) Call(ctx context.Context, device, method string, params, out any) error {
	return h.s.call(ctx, device, method, params, out)
}
func (h recordingHost) SendOnce(ctx context.Context, machine string, params map[string]any) error {
	return h.s.sendOnce(ctx, machine, params)
}
func (h recordingHost) SendParams(ctx context.Context, p SendParams) (map[string]any, error) {
	return h.s.SendParams(ctx, p)
}
func (h recordingHost) Start(ctx context.Context, p StartParams) (proto.Session, error) {
	return h.s.start(ctx, p)
}
func (h recordingHost) StartSignedIn(ctx context.Context, p StartParams) (proto.Session, error) {
	return h.s.startSignedIn(ctx, p)
}
func (h recordingHost) UserSpoke(ctx context.Context, r SessionRef) { h.s.UserSpoke(ctx, r) }
func (h recordingHost) MissingAssistantMachine(ctx context.Context) error {
	return h.s.missingAssistantMachine(ctx)
}
func (h recordingHost) BeginAssistantStart(machine string) func() {
	return h.s.beginAssistantStart(machine)
}
func (h recordingHost) TitleFixed(machine, id string, started time.Time, kind, topic string) {
	h.s.titleFixed(machine, id, started, kind, topic)
}

// CanonicalMachine is machine as the devices name it (an id another one absorbed: that one).
func (h recordingHost) CanonicalMachine(machine string) string {
	if h.s.devs != nil {
		return h.s.devs.Canonical(machine)
	}
	return machine
}
func (h recordingHost) SameMachine(a, b string) bool      { return h.s.sameMachine(a, b) }
func (h recordingHost) IsWorkMachine(machine string) bool { return h.s.isWorkMachine(machine) }
func (h recordingHost) AttendeeMachine(ctx context.Context, machine string) (ours, online bool) {
	return h.s.attendeeMachine(ctx, machine)
}
func (h recordingHost) AttendeeRow(ctx context.Context, machine, id string) (proto.Session, bool, error) {
	return h.s.attendeeRow(ctx, machine, id)
}
func (h recordingHost) TurnSettling(machine, id string, now time.Time) bool {
	return h.s.turnSettling(machine, id, now)
}
func (h recordingHost) Session(ctx context.Context, machine, id string) (proto.Session, bool) {
	return h.s.session(ctx, machine, id)
}
func (h recordingHost) SessionsChanged(machine string) { h.s.sessionsChanged(machine) }
func (h recordingHost) IsMainOn(ctx context.Context, machine, session string) bool {
	return h.s.isMainOn(ctx, machine, session)
}
func (h recordingHost) LastSeq(ctx context.Context, machine, sid string) int {
	return h.s.lastSeq(ctx, machine, sid)
}
func (h recordingHost) TurnErrorTransient(sess proto.Session) bool { return turnErrorTransient(sess) }

func (h recordingHost) GuestOf(ctx context.Context, machine, session string) ([]string, bool, error) {
	return h.s.guestOf(ctx, machine, session)
}
func (h recordingHost) RoomOf(ctx context.Context, machine, session string) (string, bool, error) {
	return h.s.roomOf(ctx, machine, session)
}
func (h recordingHost) GuestSetRoom(ctx context.Context, machine, id, recording string) error {
	return h.s.guestSetRoom(ctx, machine, id, recording)
}
func (h recordingHost) GuestDrop(ctx context.Context, id string) error { return h.s.guestDrop(ctx, id) }
func (h recordingHost) IsRoom(session string) bool                     { return h.s.isRoom(session) }
func (h recordingHost) ConversationOrigins() (map[string]recordinghost.Origin, bool) {
	m, ok := h.s.conversationOrigins()
	if !ok {
		return nil, false
	}
	out := make(map[string]recordinghost.Origin, len(m))
	for id, o := range m {
		out[id] = recordinghost.Origin{Origin: o.origin, Ref: o.ref, Machine: o.machine, Ext: o.ext}
	}
	return out, true
}
func (h recordingHost) OriginsOf(ctx context.Context, origin string) []recordinghost.Origin {
	var out []recordinghost.Origin
	for _, o := range h.s.originsOf(ctx, origin) {
		out = append(out, recordinghost.Origin{Origin: origin, Ref: o.ref, Machine: o.machine, Session: o.session})
	}
	return out
}
func (h recordingHost) OriginSet(ctx context.Context, machine, id, origin, ref, ext string) {
	h.s.originSet(ctx, machine, id, origin, ref, ext)
}
func (h recordingHost) OriginDrop(ctx context.Context, id string) { h.s.originDrop(ctx, id) }

func (h recordingHost) TranscriptionTarget(ctx context.Context) (ref, choice string, err error) {
	return h.s.transcriptionTarget(ctx)
}

// TranscriptionInfo is contribution ref's info; transcribejob.ErrNoTranscribers without an extensions layer.
func (h recordingHost) TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error) {
	if h.s.trans == nil {
		return extwire.TranscriptionInfo{}, transcribejob.ErrNoTranscribers
	}
	return h.s.trans.TranscriptionInfo(ctx, ref)
}
func (h recordingHost) DialJob(ctx context.Context, ref string) (*transcribejob.Conn, error) {
	return h.s.dialJob(ctx, ref)
}
func (h recordingHost) TranscriptionReadyAwait() bool { return h.s.transcriptionReadyAwait() }

func (h recordingHost) InSpaceRecording(ctx context.Context, rawID string) bool {
	return h.s.inSpaceRecording(ctx, rawID)
}
func (h recordingHost) PutSpaceRecordingRoute(w http.ResponseWriter, r *http.Request, u recordingwire.RecordingUpload, clean recordingwire.UploadCleaner) {
	h.s.putSpaceRecordingRoute(w, r, u, clean)
}

// DeleteSpaceRecording answers DELETE /api/v1/recordings/{id} for recording id when it was saved into a track
// (space_recordings.go); false, having answered nothing, for any other.
func (h recordingHost) DeleteSpaceRecording(w http.ResponseWriter, r *http.Request, id string) bool {
	row, ok := h.s.spaceRecordingOf(r.Context(), id)
	if !ok {
		return false
	}
	deleted, err := h.s.deleteSpaceRecording(r.Context(), row, time.Now())
	if err != nil {
		writeSpaceRecordingError(w, err)
		return true
	}
	apierr.WriteJSON(w, map[string]any{"deleted": deleted})
	return true
}
func (h recordingHost) SpaceRecordingStates(ctx context.Context) (kept []recordingwire.RecordingState, deleted []string) {
	return h.s.spaceRecordingStates(ctx)
}

func (h recordingHost) DecodeLargeBody(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return decodeLargeBody(w, r, v, limit)
}

// todosOfRecap names a day recap's action items to ActionTodos (the module's todosOfRecap).
const todosOfRecap = "recap"
