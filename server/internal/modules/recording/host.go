package recording

import (
	"context"
	"github.com/nwverse/persona/server/internal/modules/recording/writers"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcribejob"
)

// Host is what the module needs of the core: the assistant's Service, through internal/recordinghost.Host, which has
// the same methods (internal/modules/all checks it). Each takes and gives only what both sides can name: the standard
// library's, the knowledge base's (kb), and the contract packages'.
type Host interface {
	// Logging, routes, the service's life.
	Logf(format string, args ...any)
	Handle(pattern string, fn http.HandlerFunc)
	RunCtx() context.Context
	Hello(ctx context.Context) assistantwire.Hello
	Event(ctx context.Context, ev map[string]any)

	// The module's times over its defaults, and the attending feeder's clock and timer (nil: the real ones): a test and
	// tuning knob of the core's (assistant.Options.RecordingTimings), never a user setting.
	Timings() (one map[string]time.Duration, lists map[string][]time.Duration)
	FeedClock() (now func() time.Time, after func(time.Duration, func()) func() bool)

	// The owner: language, zone, name, the web app, the main conversation, the meetings' glossary.
	Lang(ctx context.Context) i18n.Lang
	AppLang(ctx context.Context, lang string) i18n.Lang
	Location(ctx context.Context) *time.Location
	Owner(ctx context.Context) string
	WebBase(ctx context.Context) string
	MainSession(ctx context.Context) (machine, session string)
	MeetingGlossary(ctx context.Context) []string

	// The knowledge base the manager has open.
	KB(ctx context.Context) (*kb.Engine, error)
	KBOff(err error) bool
	KBWake()
	KBReadOnly(ctx context.Context) bool
	KBConnected() bool
	SpeakerTurnsImport() bool

	// The manager's settings rows (nil store: none written, none read).
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error

	// Pushes and channels.
	Notify(ctx context.Context, n assistantwire.Notification) error
	NoteCard(ctx context.Context, source string, n assistantwire.Notification)
	Answered(err error) bool
	PushRefused(err error) bool
	ChannelsPushed(machine, session, title, body string)
	FeishuPushKeyed(ctx context.Context, machine, session, title, body, key string) error
	ChannelPushTo(ctx context.Context, machine, session, title, body, ref string, took func(id string) bool) (map[string]error, error)

	// To-dos made of action items (minutes and day recaps).
	ActionTodos(ctx context.Context, list []recordingwire.MinutesAction, actions []string, kind, recording string, version int,
		note func(recordingwire.MinutesAction) string) (out []any, made, byTask map[string]string, err error)
	ActionTodo(ctx context.Context, todoID string) string

	// Models.
	Model(purpose string) kb.Model
	ModelUnavailable(err error) bool
	ApplyModel(ctx context.Context, purpose string, params map[string]any) error

	// Computers and their conversations.
	Call(ctx context.Context, device, method string, params, out any) error
	SendOnce(ctx context.Context, machine string, params map[string]any) error
	SendParams(ctx context.Context, p assistantwire.SendParams) (map[string]any, error)
	Start(ctx context.Context, p assistantwire.StartParams) (proto.Session, error)
	StartSignedIn(ctx context.Context, p assistantwire.StartParams) (proto.Session, error)
	UserSpoke(ctx context.Context, r assistantwire.SessionRef)
	MissingAssistantMachine(ctx context.Context) error
	BeginAssistantStart(machine string) func()
	TitleFixed(machine, id string, started time.Time, kind, topic string)
	CanonicalMachine(machine string) string
	SameMachine(a, b string) bool
	IsWorkMachine(machine string) bool
	AttendeeMachine(ctx context.Context, machine string) (ours, online bool)
	AttendeeRow(ctx context.Context, machine, id string) (proto.Session, bool, error)
	TurnSettling(machine, id string, now time.Time) bool
	Session(ctx context.Context, machine, id string) (proto.Session, bool)
	SessionsChanged(machine string)
	IsMainOn(ctx context.Context, machine, session string) bool
	LastSeq(ctx context.Context, machine, sid string) int
	TurnErrorTransient(sess proto.Session) bool

	// Guest conversations and the conversations' origins.
	GuestOf(ctx context.Context, machine, session string) ([]string, bool, error)
	RoomOf(ctx context.Context, machine, session string) (string, bool, error)
	GuestSetRoom(ctx context.Context, machine, id, recording string) error
	GuestDrop(ctx context.Context, id string) error
	IsRoom(session string) bool
	ConversationOrigins() (map[string]Origin, bool)
	OriginsOf(ctx context.Context, origin string) []Origin
	OriginSet(ctx context.Context, machine, id, origin, ref, ext string)
	OriginDrop(ctx context.Context, id string)

	// The meeting transcription the owner chose (the core's transcription seam).
	TranscriptionTarget(ctx context.Context) (ref, choice string, err error)
	TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error)
	DialJob(ctx context.Context, ref string) (*transcribejob.Conn, error)
	TranscriptionReadyAwait() bool

	// Recordings saved into a track (the core's tracks).
	InSpaceRecording(ctx context.Context, rawID string) bool
	PutSpaceRecordingRoute(w http.ResponseWriter, r *http.Request, u recordingwire.RecordingUpload, clean recordingwire.UploadCleaner)
	DeleteSpaceRecording(w http.ResponseWriter, r *http.Request, id string) bool
	SpaceRecordingStates(ctx context.Context) (kept []recordingwire.RecordingState, deleted []string)

	// Requests.
	DecodeLargeBody(w http.ResponseWriter, r *http.Request, v any, limit int64) error
}

// Origin is a conversation's origin as the core keeps it (ConversationOrigins, OriginsOf).
type Origin = struct{ Origin, Ref, Machine, Session, Ext string }

// The module's code says what it said in the assistant's package; these hand each on to the host.

func (s *Recordings) logf(format string, args ...any)            { s.h.Logf(format, args...) }
func (s *Recordings) handle(pattern string, fn http.HandlerFunc) { s.h.Handle(pattern, fn) }
func (s *Recordings) runCtx() context.Context                    { return s.h.RunCtx() }
func (s *Recordings) hello(ctx context.Context) Hello            { return s.h.Hello(ctx) }
func (s *Recordings) event(ctx context.Context, ev map[string]any) {
	s.h.Event(ctx, ev)
}
func (s *Recordings) lang(ctx context.Context) i18n.Lang { return s.h.Lang(ctx) }
func (s *Recordings) appLang(ctx context.Context, lang string) i18n.Lang {
	return s.h.AppLang(ctx, lang)
}
func (s *Recordings) location(ctx context.Context) *time.Location { return s.h.Location(ctx) }
func (s *Recordings) owner(ctx context.Context) string            { return s.h.Owner(ctx) }
func (s *Recordings) webBase(ctx context.Context) string          { return s.h.WebBase(ctx) }

// mainRef is the main conversation as the settings name it.
type mainRef struct{ MachineID, Main string }

func (s *Recordings) settings(ctx context.Context) mainRef {
	m, sess := s.h.MainSession(ctx)
	return mainRef{MachineID: m, Main: sess}
}

func (s *Recordings) kbGet(ctx context.Context) (*kb.Engine, error) { return s.h.KB(ctx) }
func (s *Recordings) kbWake()                                       { s.h.KBWake() }
func (s *Recordings) kbReadOnly(ctx context.Context) bool           { return s.h.KBReadOnly(ctx) }
func (s *Recordings) kbConnected() bool                             { return s.h.KBConnected() }
func (s *Recordings) speakerTurnsImport() bool                      { return s.h.SpeakerTurnsImport() }

// settingStore is the manager's settings rows.
type settingStore interface {
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
}

func (s *Recordings) store() settingStore { return s.h }

func (s *Recordings) noteCard(ctx context.Context, source string, n Notification) {
	s.h.NoteCard(ctx, source, n)
}
func (s *Recordings) channelsPushed(machine, session, title, body string) {
	s.h.ChannelsPushed(machine, session, title, body)
}
func (s *Recordings) feishuPushKeyed(ctx context.Context, machine, session, title, body, key string) error {
	return s.h.FeishuPushKeyed(ctx, machine, session, title, body, key)
}
func (s *Recordings) channelPushTo(ctx context.Context, machine, session, title, body, ref string, took func(id string) bool) (map[string]error, error) {
	return s.h.ChannelPushTo(ctx, machine, session, title, body, ref, took)
}
func (s *Recordings) actionTodo(ctx context.Context, todoID string) string {
	return s.h.ActionTodo(ctx, todoID)
}
func (s *Recordings) applyModel(ctx context.Context, purpose string, params map[string]any) error {
	return s.h.ApplyModel(ctx, purpose, params)
}
func (s *Recordings) call(ctx context.Context, device, method string, params, out any) error {
	return s.h.Call(ctx, device, method, params, out)
}
func (s *Recordings) sendOnce(ctx context.Context, machine string, params map[string]any) error {
	return s.h.SendOnce(ctx, machine, params)
}
func (s *Recordings) SendParams(ctx context.Context, p SendParams) (map[string]any, error) {
	return s.h.SendParams(ctx, p)
}
func (s *Recordings) start(ctx context.Context, p StartParams) (proto.Session, error) {
	return s.h.Start(ctx, p)
}
func (s *Recordings) startSignedIn(ctx context.Context, p StartParams) (proto.Session, error) {
	return s.h.StartSignedIn(ctx, p)
}
func (s *Recordings) UserSpoke(ctx context.Context, r SessionRef) { s.h.UserSpoke(ctx, r) }
func (s *Recordings) missingAssistantMachine(ctx context.Context) error {
	return s.h.MissingAssistantMachine(ctx)
}
func (s *Recordings) beginAssistantStart(machine string) func() {
	return s.h.BeginAssistantStart(machine)
}
func (s *Recordings) titleFixed(machine, id string, started time.Time, kind, topic string) {
	s.h.TitleFixed(machine, id, started, kind, topic)
}
func (s *Recordings) canonical(machine string) string   { return s.h.CanonicalMachine(machine) }
func (s *Recordings) sameMachine(a, b string) bool      { return s.h.SameMachine(a, b) }
func (s *Recordings) isWorkMachine(machine string) bool { return s.h.IsWorkMachine(machine) }
func (s *Recordings) attendeeMachine(ctx context.Context, machine string) (ours, online bool) {
	return s.h.AttendeeMachine(ctx, machine)
}
func (s *Recordings) attendeeRow(ctx context.Context, machine, id string) (proto.Session, bool, error) {
	return s.h.AttendeeRow(ctx, machine, id)
}
func (s *Recordings) turnSettling(machine, id string, now time.Time) bool {
	return s.h.TurnSettling(machine, id, now)
}
func (s *Recordings) session(ctx context.Context, machine, id string) (proto.Session, bool) {
	return s.h.Session(ctx, machine, id)
}
func (s *Recordings) sessionsChanged(machine string) { s.h.SessionsChanged(machine) }
func (s *Recordings) isMainOn(ctx context.Context, machine, session string) bool {
	return s.h.IsMainOn(ctx, machine, session)
}
func (s *Recordings) lastSeq(ctx context.Context, machine, sid string) int {
	return s.h.LastSeq(ctx, machine, sid)
}
func (s *Recordings) guestOf(ctx context.Context, machine, session string) ([]string, bool, error) {
	return s.h.GuestOf(ctx, machine, session)
}
func (s *Recordings) roomOf(ctx context.Context, machine, session string) (string, bool, error) {
	return s.h.RoomOf(ctx, machine, session)
}
func (s *Recordings) guestSetRoom(ctx context.Context, machine, id, recording string) error {
	return s.h.GuestSetRoom(ctx, machine, id, recording)
}
func (s *Recordings) guestDrop(ctx context.Context, id string) error { return s.h.GuestDrop(ctx, id) }
func (s *Recordings) isRoom(session string) bool                     { return s.h.IsRoom(session) }

// shownOrigin is a conversation's origin as the apps are shown it.
type shownOrigin struct{ origin, ref, machine, ext string }

func (s *Recordings) conversationOrigins() (map[string]shownOrigin, bool) {
	m, ok := s.h.ConversationOrigins()
	if !ok {
		return nil, false
	}
	out := make(map[string]shownOrigin, len(m))
	for id, o := range m {
		out[id] = shownOrigin{o.Origin, o.Ref, o.Machine, o.Ext}
	}
	return out, true
}

// originRecord is one conversation's origin as originsOf lists it.
type originRecord struct{ machine, session, ref string }

func (s *Recordings) originsOf(ctx context.Context, origin string) []originRecord {
	var out []originRecord
	for _, o := range s.h.OriginsOf(ctx, origin) {
		out = append(out, originRecord{machine: o.Machine, session: o.Session, ref: o.Ref})
	}
	return out
}
func (s *Recordings) originSet(ctx context.Context, machine, id, origin, ref, ext string) {
	s.h.OriginSet(ctx, machine, id, origin, ref, ext)
}
func (s *Recordings) originDrop(ctx context.Context, id string) { s.h.OriginDrop(ctx, id) }

func (s *Recordings) transcriptionTarget(ctx context.Context) (ref, choice string, err error) {
	return s.h.TranscriptionTarget(ctx)
}
func (s *Recordings) dialJob(ctx context.Context, ref string) (*transcribejob.Conn, error) {
	return s.h.DialJob(ctx, ref)
}
func (s *Recordings) transcriptionReadyAwait() bool { return s.h.TranscriptionReadyAwait() }

func (s *Recordings) inSpaceRecording(ctx context.Context, rawID string) bool {
	return s.h.InSpaceRecording(ctx, rawID)
}
func (s *Recordings) putSpaceRecordingRoute(w http.ResponseWriter, r *http.Request, u recordingwire.RecordingUpload) {
	s.h.PutSpaceRecordingRoute(w, r, u, writers.CleanUpload)
}
func (s *Recordings) spaceRecordingStates(ctx context.Context) (kept []recordingwire.RecordingState, deleted []string) {
	return s.h.SpaceRecordingStates(ctx)
}

func (s *Recordings) decodeLargeBody(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return s.h.DecodeLargeBody(w, r, v, limit)
}
