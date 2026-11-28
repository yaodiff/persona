// Package recordinghost is the seam between the core and the meeting recording module (internal/modules/recording,
// docs/architecture/core-and-modules.md "Recording"): what the core asks of the module (Layer) and what the module
// asks of the core (Host), each in terms both sides name (the standard library, the knowledge base, the contracts).
// The composition root (internal/modules/all) provides the module in the init of its file, under the module's build
// constraint; without it nothing is provided and the core runs with no recording layer: no recording routes, no
// meeting tools, feature flags or jobs.
//
// The module is provided while the program initializes, before main runs, and only read afterwards. A kernel slot
// for routes, tools, feature flags and jobs (#13844) replaces this seam once the kernel serves those slots.
package recordinghost

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/moduleswitch"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcribejob"
)

// ModuleID is the recording module's id (its module.json): the kernel's switch turns it off by this name.
const ModuleID = "recording"

// Layer is what the core asks of the recording module.
type Layer interface {
	// Routes registers the module's HTTP routes (through Host.Handle); engine is the knowledge base for a route.
	Routes(engine func(http.ResponseWriter, *http.Request) *kb.Engine)
	// AudioSocket serves GET /api/v1/recordings/{id}/audio, the phone's audio socket.
	AudioSocket(w http.ResponseWriter, r *http.Request)
	// Hook gives knowledge base e the module's callbacks (minutes written, live changes, day recaps) and its write
	// side; core is e as the storage port, which the module's writers reach the store through.
	Hook(e *kb.Engine, core recordingwire.Core)
	// AttendResume picks the attended meetings up again from their live logs when e opens.
	AttendResume(e *kb.Engine)
	// FinalPasses runs hub transcription's final passes until ctx ends.
	FinalPasses(ctx context.Context)
	// RoomsSweep and HubSweep end the rooms, audio sockets and jobs of transcripts cut and recordings gone.
	RoomsSweep(ctx context.Context, e *kb.Engine)
	HubSweep(e *kb.Engine)
	// Wait waits until the module's background work (hub transcription's sockets, runs and passes, the questions being
	// answered) has ended, or ctx ends: the service stops (kbEnd) and tests drain.
	Wait(ctx context.Context) error
	// TranscriptionChanged: the meeting transcription the owner chose, or its readiness, changed (the live jobs of a
	// choice no longer standing are ended in the background); TranscriptionJobsCheck ends them now.
	TranscriptionChanged()
	TranscriptionJobsCheck(ctx context.Context)

	// The Just me turns and the meeting turns of the conversations Persona attends meetings in.
	JustMeNow(machine, session string) bool
	JustMeSent(machine, session string)
	JustMeStarted(machine, session string, newTurn, woken bool)
	JustMeEnded(machine, session string, held bool)
	MeetingTurnSent(machine, session, recording string, persona bool) (time.Time, error)
	MeetingTurnNow(machine, session string) bool
	MeetingTurnStarted(machine, session string, newTurn bool) bool
	MeetingTurnEnded(machine, session string) bool
	MeetingTurnStopped(machine, session string, row proto.Session, restarted bool) bool
	MeetingTurnPrompt(machine, session string, p map[string]any)
	MeetingTurnRunning(ctx context.Context, machine, session string) (bool, error)
	MeetingTurnAgentRunning(ctx context.Context, machine string) (bool, error)
	MeetingTurnUserSpoke(machine, session string)
	MeetingRefuses(ctx context.Context, machine, session, name string, assistant bool) (proto.ToolResult, bool)

	// The meeting rooms: their tools and their refusal of what is not theirs.
	RoomRefusesSend(ctx context.Context, machine, session, lang string) error
	RoomToolCall(ctx context.Context, recording, name string, args json.RawMessage) proto.ToolResult
	RoomTools() []proto.Tool
	RoomToolNames() []string
}

// Origin is a conversation's origin as the core keeps it.
type Origin = struct{ Origin, Ref, Machine, Session, Ext string }

// Host is what the recording module asks of the core: the same methods as the module's own Host.
type Host interface {
	Logf(format string, args ...any)
	Handle(pattern string, fn http.HandlerFunc)
	RunCtx() context.Context
	Hello(ctx context.Context) assistantwire.Hello
	Event(ctx context.Context, ev map[string]any)

	// The module's times over its defaults, and the attending feeder's clock and timer (nil: the real ones): a test and
	// tuning knob of the core's (assistant.Options.RecordingTimings), never a user setting.
	Timings() (one map[string]time.Duration, lists map[string][]time.Duration)
	FeedClock() (now func() time.Time, after func(time.Duration, func()) func() bool)

	Lang(ctx context.Context) i18n.Lang
	AppLang(ctx context.Context, lang string) i18n.Lang
	Location(ctx context.Context) *time.Location
	Owner(ctx context.Context) string
	WebBase(ctx context.Context) string
	MainSession(ctx context.Context) (machine, session string)
	MeetingGlossary(ctx context.Context) []string

	KB(ctx context.Context) (*kb.Engine, error)
	KBOff(err error) bool
	KBWake()
	KBReadOnly(ctx context.Context) bool
	KBConnected() bool
	SpeakerTurnsImport() bool

	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error

	Notify(ctx context.Context, n assistantwire.Notification) error
	NoteCard(ctx context.Context, source string, n assistantwire.Notification)
	Answered(err error) bool
	PushRefused(err error) bool
	ChannelsPushed(machine, session, title, body string)
	FeishuPushKeyed(ctx context.Context, machine, session, title, body, key string) error
	ChannelPushTo(ctx context.Context, machine, session, title, body, ref string, took func(id string) bool) (map[string]error, error)

	ActionTodos(ctx context.Context, list []kb.MinutesAction, actions []string, kind, recording string, version int,
		note func(kb.MinutesAction) string) (out []any, made, byTask map[string]string, err error)
	ActionTodo(ctx context.Context, todoID string) string

	Model(purpose string) kb.Model
	ModelUnavailable(err error) bool
	ApplyModel(ctx context.Context, purpose string, params map[string]any) error

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

	GuestOf(ctx context.Context, machine, session string) ([]string, bool, error)
	RoomOf(ctx context.Context, machine, session string) (string, bool, error)
	GuestSetRoom(ctx context.Context, machine, id, recording string) error
	GuestDrop(ctx context.Context, id string) error
	IsRoom(session string) bool
	ConversationOrigins() (map[string]Origin, bool)
	OriginsOf(ctx context.Context, origin string) []Origin
	OriginSet(ctx context.Context, machine, id, origin, ref, ext string)
	OriginDrop(ctx context.Context, id string)

	TranscriptionTarget(ctx context.Context) (ref, choice string, err error)
	TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error)
	DialJob(ctx context.Context, ref string) (*transcribejob.Conn, error)
	TranscriptionReadyAwait() bool

	InSpaceRecording(ctx context.Context, rawID string) bool
	// PutSpaceRecordingRoute answers the upload u of a recording saved into a track; clean is the module's check of an
	// upload, run where the knowledge base's own upload runs it.
	PutSpaceRecordingRoute(w http.ResponseWriter, r *http.Request, u recordingwire.RecordingUpload, clean recordingwire.UploadCleaner)
	DeleteSpaceRecording(w http.ResponseWriter, r *http.Request, id string) bool
	SpaceRecordingStates(ctx context.Context) (kept []recordingwire.RecordingState, deleted []string)

	DecodeLargeBody(w http.ResponseWriter, r *http.Request, v any, limit int64) error
}

var port = moduleswitch.NewPort[func(Host) Layer]("recordinghost")

// Provide provides the module under its id: new makes its layer for one manager's host (init only; twice panics).
func Provide(module string, new func(Host) Layer) { port.Provide(module, new) }

// InBuild says whether the recording module is in this build and s has it on.
func InBuild(s moduleswitch.Set) bool { return port.On(s) }

// New is the module's layer for h; false when the module is not in this build or s switched it off (the `modules`
// setting the manager started with), so it is absent exactly as one built out.
func New(s moduleswitch.Set, h Host) (Layer, bool) {
	newLayer, ok := port.Get(s)
	if !ok {
		return nil, false
	}
	return newLayer(h), true
}
