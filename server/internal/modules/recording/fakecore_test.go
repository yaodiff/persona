package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcribejob"
)

// The core as the module's own tests stand it in: Service is a fake Host (what internal/recordinghost.Host asks of
// the assistant) around the module's state, with an in-memory store of settings rows, origins and guests, a fake hub
// for the calls to the computers (fakeHub: what they answer, what was called), a real knowledge base on the test's
// directory, and the transcription contributions a test scripts (fakeTranscriber). The core's own integration tests
// of the module run it inside the assistant (internal/assistant, recording_integration_test.go and the meeting
// tests there).

// Settings are the assistant's settings the module reads (Host.MainSession, Host.MeetingGlossary and the meeting
// transcription's choice).
type Settings struct {
	MachineID, Main                 string
	MeetingGlossary                 *[]string
	MeetingTranscriptionExtension   *string
	MeetingTranscriptionInstall     *string
	RecordingDays                   *int
	Timezone                        string
	OwnerName                       string
	MeetingTranscriptionDefaultHub  bool
	MeetingTranscriptionUnavailable bool // the chosen contribution cannot transcribe live (readiness)
}

// transcribers is the extensions' transcription contributions (the core's meeting_transcribe_ext.go).
type transcribers interface {
	TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error)
	TranscriptionDial(ctx context.Context, ref string) (conn *websocket.Conn, done func(), err error)
	IsTranscriptionContribution(ref string) bool
	TranscriptionInstall(ref string) string
}

// Service is the fake core: the Host of the module state it embeds.
type Service struct {
	*Recordings
	t   testing.TB
	f   *fakeHub
	mux *http.ServeMux

	mu                 sync.Mutex
	set                Settings
	st                 *memStore             // the settings rows (Host.Setting)
	titles             map[string]string     // TitleFixed's kinds, by session id
	origins            map[string]Origin     // by session id
	guests             map[string]*guestConv // guest conversations, by session id
	cards              []string              // NoteCard's sources
	todos              map[string]string     // ActionTodos' to-dos: id → title
	timings            map[string]time.Duration
	lists              map[string][]time.Duration
	now                func() time.Time
	after              func(time.Duration, func()) func() bool
	readOnly           bool
	speakerTurnsImport bool
	inSpace            map[string]bool
	work               map[string]bool // computers whose trust is work

	trans        transcribers
	meetingModel kb.Model
	kbDir        string
	kbOff        bool
	eng          *kb.Engine
	ctx          context.Context
	stop         context.CancelFunc
	logs         []string
}

var errKBOff = errors.New("the knowledge base is off")

// newCore is the fake core with the module in it, its knowledge base in a temporary directory (none with noKB).
func newCore(t testing.TB, f *fakeHub) *Service {
	t.Helper()
	return newServiceOn(t, f, &memStore{rows: map[string]json.RawMessage{}})
}

// memStore is the manager's settings rows (a database a restarted core reads again).
type memStore struct {
	mu   sync.Mutex
	rows map[string]json.RawMessage
}

// guestConv is a guest conversation: the tools it may call, a meeting room's recording.
type guestConv struct {
	MachineID string
	Tools     []string
	Kind      string
	Recording string
}

// newServiceOn is the fake core on the settings rows st (newCore's, or another core's: a restart).
func newServiceOn(t testing.TB, f *fakeHub, st *memStore) *Service {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	s := &Service{t: t, f: f, mux: http.NewServeMux(), st: st, origins: map[string]Origin{}, titles: map[string]string{},
		guests: map[string]*guestConv{}, todos: map[string]string{}, kbDir: t.TempDir(), ctx: ctx, stop: stop}
	s.set.MachineID = "server"
	f.mu.Lock()
	if f.settings != nil {
		s.set = *f.settings
	}
	f.mu.Unlock()
	s.Recordings = New(s)
	s.Recordings.Routes(s.engineFor)
	// The production Core owns the read-only list beside these module routes; stand it in for the module's HTTP tests.
	s.mux.HandleFunc("GET /api/v1/recordings/{id}/photos", s.corePhotoList)
	t.Cleanup(func() {
		wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Recordings.Wait(wctx); err != nil {
			t.Errorf("the module's background work still runs after the test: %v", err)
		}
		stop()
	})
	return s
}

// corePhotoList is the fake Core's photo list, with the production route's authorization, errors and response.
func (s *Service) corePhotoList(w http.ResponseWriter, r *http.Request) {
	if !meetingCaller(w, r) {
		return
	}
	e := s.meetingEngine(w, r)
	if e == nil {
		return
	}
	photos, _, err := e.PhotosRead(r.PathValue("id"))
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	apierr.WriteJSON(w, map[string]any{"photos": photos})
}

// ServeHTTP serves the module's routes, as the assistant's mux does.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// ServeSocket serves the audio socket, as the assistant's socket routes do.
func (s *Service) ServeSocket(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/recordings/{id}/audio", s.AudioSocket)
	mux.ServeHTTP(w, r)
}

// engineFor is the knowledge base for a route: 503 while it is off or cannot open.
func (s *Service) engineFor(w http.ResponseWriter, r *http.Request) *kb.Engine {
	e, err := s.KB(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return nil
	}
	return e
}

// setSettings changes the settings a test runs with.
func setSettings(t testing.TB, s *Service, fn func(*Settings)) {
	t.Helper()
	s.mu.Lock()
	fn(&s.set)
	s.mu.Unlock()
	s.Recordings.TranscriptionChanged()
}

// recTimings sets the module's times by name (the core's Options.RecordingTimings).
func recTimings(s *Service, one map[string]time.Duration, lists map[string][]time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timings == nil {
		s.timings, s.lists = map[string]time.Duration{}, map[string][]time.Duration{}
	}
	maps.Copy(s.timings, one)
	maps.Copy(s.lists, lists)
}

// transcriptionReadinessChanged: the contributions changed (the core probes them again; here they are read anew).
func (s *Service) transcriptionReadinessChanged() { s.Recordings.TranscriptionChanged() }

// engine is the knowledge base, open.
func (s *Service) engine() *kb.Engine {
	s.t.Helper()
	e, err := s.KB(context.Background())
	if err != nil {
		s.t.Fatalf("the knowledge base did not open: %v", err)
	}
	return e
}

// ---- the Host ----

func (s *Service) Logf(format string, args ...any) {
	s.mu.Lock()
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
	s.mu.Unlock()
	if os.Getenv("RECORDING_TEST_LOG") != "" {
		s.t.Logf(format, args...)
	}
}
func (s *Service) Handle(pattern string, fn http.HandlerFunc) { s.mux.HandleFunc(pattern, fn) }
func (s *Service) RunCtx() context.Context                    { return s.ctx }
func (s *Service) Hello(ctx context.Context) assistantwire.Hello {
	h, _ := s.f.Hello(ctx)
	return h
}
func (s *Service) Event(ctx context.Context, ev map[string]any) {
	raw, err := json.Marshal(ev)
	if err == nil {
		s.f.Broadcast(ctx, Broadcast{Message: raw})
	}
}
func (s *Service) Timings() (map[string]time.Duration, map[string][]time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.timings), maps.Clone(s.lists)
}
func (s *Service) FeedClock() (func() time.Time, func(time.Duration, func()) func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now, s.after
}
func (s *Service) Lang(context.Context) i18n.Lang {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return i18n.Lang(s.f.hello.Locale).Or(i18n.ZhHans)
}
func (s *Service) AppLang(ctx context.Context, lang string) i18n.Lang {
	return i18n.Lang(lang).Or(s.Lang(ctx))
}
func (s *Service) Location(context.Context) *time.Location {
	s.mu.Lock()
	tz := s.set.Timezone
	s.mu.Unlock()
	if loc, err := time.LoadLocation(tz); tz != "" && err == nil {
		return loc
	}
	return time.Local
}
func (s *Service) Owner(context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.OwnerName
}
func (s *Service) WebBase(context.Context) string { return "" }
func (s *Service) MainSession(context.Context) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.MachineID, s.set.Main
}
func (s *Service) MeetingGlossary(context.Context) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set.MeetingGlossary == nil {
		return nil
	}
	return slices.Clone(*s.set.MeetingGlossary)
}

// KB opens the knowledge base once, with the module's callbacks (Hook), as the core's kbOpenLocked does.
func (s *Service) KB(context.Context) (*kb.Engine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.Hello(context.Background())
	if s.kbOff || h.KB != nil && h.KB.Off {
		return nil, errKBOff
	}
	ro := h.KB != nil && h.KB.ReadOnly
	if s.eng != nil && s.eng.Store.ReadOnly() == ro {
		return s.eng, nil
	}
	if ro { // the settings made it read-only: opened so, as the core's kbOpenLocked does
		st, err := kb.OpenReadOnly(s.kbDir)
		if err != nil {
			return nil, err
		}
		s.eng = &kb.Engine{Store: st}
		return s.eng, nil
	}
	if err := os.MkdirAll(filepath.Join(s.kbDir), 0o700); err != nil {
		return nil, err
	}
	st, err := kb.NewStore(s.kbDir)
	if err != nil {
		return nil, err
	}
	e := &kb.Engine{Store: st, Logf: func(string, ...any) {}, TranscriptionReady: s.transcriptionReady,
		ModelFor: func(string) kb.Model { return s.Model(recordingwire.PurposeMeetingMinutes) }}
	s.Recordings.Hook(e, kb.RecordingCore(e))
	e.ObserveChanges()
	s.eng = e
	return e, nil
}
func (s *Service) KBOff(err error) bool { return errors.Is(err, errKBOff) }
func (s *Service) KBWake()              {}
func (s *Service) KBReadOnly(ctx context.Context) bool {
	if h := s.Hello(ctx); h.KB != nil && h.KB.ReadOnly {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOnly
}
func (s *Service) KBConnected() bool { return true }
func (s *Service) SpeakerTurnsImport() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speakerTurnsImport
}

func (s *Service) Setting(_ context.Context, key string, out any) (bool, error) {
	s.st.mu.Lock()
	raw, ok := s.st.rows[key]
	s.st.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}
func (s *Service) PutSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.st.mu.Lock()
	s.st.rows[key] = raw
	s.st.mu.Unlock()
	return nil
}

func (s *Service) Notify(ctx context.Context, n assistantwire.Notification) error {
	_, err := s.f.NotifyRequest(ctx, NotifyRequest{Notification: n})
	return err
}
func (s *Service) NoteCard(_ context.Context, source string, _ assistantwire.Notification) {
	s.mu.Lock()
	s.cards = append(s.cards, source)
	s.mu.Unlock()
}
func (s *Service) Answered(error) bool                                 { return false }
func (s *Service) PushRefused(error) bool                              { return false }
func (s *Service) ChannelsPushed(machine, session, title, body string) {}
func (s *Service) FeishuPushKeyed(context.Context, string, string, string, string, string) error {
	return nil
}
func (s *Service) ChannelPushTo(context.Context, string, string, string, string, string, func(string) bool) (map[string]error, error) {
	return nil, nil
}

// ActionTodos makes a plain to-do of each action named (ids as the core makes them).
func (s *Service) ActionTodos(_ context.Context, list []recordingwire.MinutesAction, actions []string, kind, recording string, version int,
	note func(recordingwire.MinutesAction) string) ([]any, map[string]string, map[string]string, error) {
	made, byTask := map[string]string{}, map[string]string{}
	var out []any
	for _, aid := range actions {
		for _, a := range list {
			if a.ID != aid {
				continue
			}
			id := fmt.Sprintf("todo_%s_%s_%d_%s", kind, recording, version, aid)
			s.mu.Lock()
			s.todos[id] = a.What
			s.mu.Unlock()
			out, made[aid], byTask[recordingwire.MinutesTaskKey(a)] = append(out, map[string]any{"id": id, "title": a.What}), id, id
		}
	}
	return out, made, byTask, nil
}
func (s *Service) ActionTodo(_ context.Context, todoID string) string {
	if todoID == "" {
		return "suggestion"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.todos[todoID]; ok {
		return "open"
	}
	return ""
}

func (s *Service) Model(purpose string) kb.Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meetingModel
}
func (s *Service) ModelUnavailable(err error) bool { return errors.Is(err, errModelUnavailable) }
func (s *Service) ApplyModel(context.Context, string, map[string]any) error {
	return nil
}

// errModelUnavailable is a model that is not configured or paused (the core's ai errors).
var errModelUnavailable = errors.New("no model configured")

// Call is a call to a computer, through the fake hub (the core's call, without its conversation guards).
func (s *Service) Call(ctx context.Context, device, method string, params, out any) error {
	if p, ok := params.(map[string]any); ok && (method == DeviceAssistantSend || method == DeviceSessionSend || method == DeviceSessionSendOnce) {
		// What the core's call does to a message the tests read: its source in its text, and is_main for a send.
		if text, ok := p["text"].(string); ok {
			if src, _ := p["message_source"].(*proto.MessageSource); src != nil {
				p["text"] = prompts.MessageSourceText(text, src)
			}
		}
		if _, set := p["is_main"]; !set && method == DeviceAssistantSend {
			m, main := s.MainSession(ctx)
			id, _ := p["session_id"].(string)
			p["is_main"] = id != "" && id == main && device == m
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	res, err := s.f.DeviceCall(ctx, DeviceCall{Device: device, Method: method, Params: raw})
	if err != nil || out == nil || len(res) == 0 {
		return err
	}
	return json.Unmarshal(res, out)
}
func (s *Service) SendOnce(ctx context.Context, machine string, params map[string]any) error {
	return s.Call(ctx, machine, assistantwire.DeviceSessionSendOnce, params, nil)
}
func (s *Service) SendParams(ctx context.Context, p assistantwire.SendParams) (map[string]any, error) {
	if err := s.Recordings.roomRefusesSend(ctx, p.MachineID, p.SessionID, ""); err != nil {
		return nil, err
	}
	out := maps.Clone(p.Params)
	if out == nil {
		out = map[string]any{}
	}
	out["session_id"] = p.SessionID
	return out, nil
}

// Start starts a conversation through the fake hub's session.start (the core's start, without its placement).
func (s *Service) Start(ctx context.Context, p assistantwire.StartParams) (proto.Session, error) {
	machine := p.MachineID
	if machine == "" {
		machine, _ = s.MainSession(ctx)
	}
	params := map[string]any{"text": p.Text, "new_session_id": p.NewSessionID, "client_msg_id": p.ClientMsgID, "is_main": p.Main}
	if p.ConfigDir != "" {
		params["config_dir"] = p.ConfigDir
	}
	if len(p.Images) > 0 {
		params["images"] = p.Images
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	err := s.Call(ctx, machine, DeviceAssistantSend, params, &res)
	return proto.Session{MachineID: machine, SessionID: res.SessionID, Assistant: true}, err
}
func (s *Service) StartSignedIn(ctx context.Context, p assistantwire.StartParams) (proto.Session, error) {
	p.ConfigDir = meetingProfile
	return s.Start(ctx, p)
}
func (s *Service) UserSpoke(context.Context, assistantwire.SessionRef) {}
func (s *Service) MissingAssistantMachine(context.Context) error {
	return errors.New("no computer runs the assistant")
}
func (s *Service) BeginAssistantStart(string) func() { return func() {} }
func (s *Service) TitleFixed(_, id string, _ time.Time, kind, _ string) {
	s.mu.Lock()
	s.titles[id] = kind
	s.mu.Unlock()
}

// titleOf is who titled conversation id: the task (a kind TitleFixed fixed), else "".
func (s *Service) titleOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.titles[id]; ok {
		return titleByTask
	}
	return ""
}

// recClock gives the module the clock now (the core's Options.RecordingFeedNow).
func recClock(s *Service, now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// guestPut notes guest conversation id.
func (s *Service) guestPut(_ context.Context, id string, g *guestConv) error {
	s.mu.Lock()
	s.guests[id] = g
	s.mu.Unlock()
	return nil
}
func (s *Service) CanonicalMachine(machine string) string { return machine }
func (s *Service) SameMachine(a, b string) bool           { return a == b }
func (s *Service) IsWorkMachine(machine string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.work[machine]
}

// AttendeeMachine: a computer the fake hub lists (devices.list, else one per machine with sessions).
func (s *Service) AttendeeMachine(ctx context.Context, machine string) (bool, bool) {
	var list struct{ Devices []Device }
	if err := s.Call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &list); err != nil {
		return false, false
	}
	for _, d := range list.Devices {
		if d.ID == machine {
			return true, d.Online && d.Reachable
		}
	}
	return false, false
}
func (s *Service) AttendeeRow(ctx context.Context, machine, id string) (proto.Session, bool, error) {
	var listed struct{ Sessions []proto.Session }
	if err := s.Call(ctx, machine, DeviceSessionsList, map[string]any{}, &listed); err != nil {
		return proto.Session{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range listed.Sessions {
		if c.SessionID == id {
			if o, ok := s.origins[id]; ok {
				c.Origin = o.Origin
			}
			return c, true, nil
		}
	}
	return proto.Session{}, false, nil
}
func (s *Service) TurnSettling(string, string, time.Time) bool { return false }
func (s *Service) Session(ctx context.Context, machine, id string) (proto.Session, bool) {
	c, ok, _ := s.AttendeeRow(ctx, machine, id)
	return c, ok
}
func (s *Service) SessionsChanged(string) {}
func (s *Service) IsMainOn(_ context.Context, machine, session string) bool {
	m, main := s.MainSession(context.Background())
	return m == machine && main == session
}
func (s *Service) LastSeq(context.Context, string, string) int {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	n := 0
	for _, it := range s.f.items {
		n = max(n, it.Seq)
	}
	return n
}
func (s *Service) TurnErrorTransient(sess proto.Session) bool {
	return sess.TurnErrorKind == proto.TurnErrorTransient
}

func (s *Service) GuestOf(_ context.Context, _, session string) ([]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.guests[session]
	if !ok {
		return nil, false, nil
	}
	return slices.Clone(g.Tools), true, nil
}
func (s *Service) RoomOf(_ context.Context, _, session string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.guests[session]
	if !ok || g.Recording == "" {
		return "", false, nil
	}
	return g.Recording, true, nil
}
func (s *Service) GuestSetRoom(ctx context.Context, machine, id, recording string) error {
	return s.guestPut(ctx, id, &guestConv{MachineID: machine, Tools: meetingRoomToolNames(), Kind: guestKindMeeting, Recording: recording})
}
func (s *Service) GuestDrop(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.guests, id)
	s.mu.Unlock()
	return nil
}
func (s *Service) IsRoom(session string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.guests[session]
	return ok && g.Recording != ""
}
func (s *Service) ConversationOrigins() (map[string]Origin, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.origins), true
}
func (s *Service) OriginsOf(_ context.Context, origin string) []Origin {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Origin
	for _, id := range slices.Sorted(maps.Keys(s.origins)) {
		if o := s.origins[id]; o.Origin == origin {
			out = append(out, o)
		}
	}
	return out
}
func (s *Service) OriginSet(_ context.Context, machine, id, origin, ref, ext string) {
	s.mu.Lock()
	s.origins[id] = Origin{Origin: origin, Ref: ref, Machine: machine, Session: id, Ext: ext}
	s.mu.Unlock()
}
func (s *Service) OriginDrop(_ context.Context, id string) {
	s.mu.Lock()
	delete(s.origins, id)
	s.mu.Unlock()
}

// TranscriptionTarget is the contribution the settings name ("" the built-in one) and the choice it is.
func (s *Service) TranscriptionTarget(context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := ""
	if s.set.MeetingTranscriptionExtension != nil {
		ref = *s.set.MeetingTranscriptionExtension
	}
	install := ""
	if s.set.MeetingTranscriptionInstall != nil {
		install = *s.set.MeetingTranscriptionInstall
		if s.trans == nil || s.trans.TranscriptionInstall(ref) != install {
			return ref, ref + "|" + install, errors.New("not chosen")
		}
	}
	return ref, ref + "|" + install, nil
}
func (s *Service) TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error) {
	s.mu.Lock()
	tr := s.trans
	s.mu.Unlock()
	if tr == nil {
		return extwire.TranscriptionInfo{}, transcribejob.ErrNoTranscribers
	}
	return tr.TranscriptionInfo(ctx, ref)
}
func (s *Service) DialJob(ctx context.Context, ref string) (*transcribejob.Conn, error) {
	s.mu.Lock()
	tr := s.trans
	s.mu.Unlock()
	if tr == nil {
		return nil, transcribejob.ErrNoTranscribers
	}
	c, done, err := tr.TranscriptionDial(ctx, ref)
	if err != nil {
		return nil, err
	}
	return transcribejob.New(c, done), nil
}

// transcriptionReady: the chosen contribution can transcribe live (the core's readiness rule, probed at once).
func (s *Service) transcriptionReady() bool {
	ref, _, err := s.TranscriptionTarget(context.Background())
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := s.TranscriptionInfo(ctx, ref)
	return err == nil && info.Protocol == extwire.TranscriptionProtocol && info.Setup == "" &&
		slices.Contains(info.Features, extwire.TranscriptionLive)
}
func (s *Service) TranscriptionReadyAwait() bool { return s.transcriptionReady() }

func (s *Service) InSpaceRecording(_ context.Context, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inSpace[id]
}
func (s *Service) PutSpaceRecordingRoute(w http.ResponseWriter, _ *http.Request, _ recordingwire.RecordingUpload, _ recordingwire.UploadCleaner) {
	http.Error(w, "no tracks in the module's tests", http.StatusBadRequest)
}
func (s *Service) DeleteSpaceRecording(http.ResponseWriter, *http.Request, string) bool { return false }
func (s *Service) SpaceRecordingStates(context.Context) ([]recordingwire.RecordingState, []string) {
	return nil, nil
}
func (s *Service) DecodeLargeBody(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
}

var _ Host = (*Service)(nil)
