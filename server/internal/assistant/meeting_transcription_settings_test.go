package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func transcriptionSettingsRequest(t *testing.T, s *Service, method, role, body string, status int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/assistant", strings.NewReader(body))
	r.Header.Set(RoleHeader, role)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d, want %d: %s", method, body, w.Code, status, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMeetingTranscriptionDefaultSetting(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	get := func() map[string]any { return transcriptionSettingsRequest(t, s, "GET", "", "", 200) }
	put := func(body string) map[string]any { return transcriptionSettingsRequest(t, s, "PUT", "", body, 200) }
	check := func(out map[string]any, mode, extension string, glossary []any, ready bool) {
		t.Helper()
		if out["meeting_transcription_default"] != mode || out["meeting_transcription_extension"] != extension ||
			!reflect.DeepEqual(out["meeting_glossary"], glossary) || out["meeting_transcription_ready"] != ready {
			t.Fatalf("transcription settings: %v", out)
		}
	}
	check(get(), "device", "", []any{}, false)
	for key, values := range map[string][]string{
		"meeting_transcription_default":   {`""`, `"Hub"`, `"automatic"`, `" hub "`, `1`, `true`, `[]`, `{}`, `null`},
		"meeting_glossary":                {`"Alice"`, `1`, `true`, `{}`, `null`, `["Alice",1]`, `[null]`, `[true]`, `[` + fmt.Sprintf("%q", strings.Repeat("a", 41)) + `]`},
		"meeting_transcription_extension": {`"missing/text"`, `"missing"`, `"/text"`, `"example/"`, `"example/text/other"`, `1`, `true`, `[]`, `{}`, `null`},
	} {
		code := map[string]string{"meeting_transcription_default": "bad_transcription_mode", "meeting_glossary": "bad_glossary", "meeting_transcription_extension": "bad_transcription_extension"}[key]
		for _, value := range values {
			out := transcriptionSettingsRequest(t, s, "PUT", "", `{"`+key+`":`+value+`}`, 400)
			if out["code"] != code {
				t.Fatalf("%s=%s: %v, want %s", key, value, out, code)
			}
		}
	}
	for _, body := range []string{`{"meeting_transcription_default":"hub"}`, `{"meeting_glossary":[]}`, `{"meeting_transcription_extension":""}`} {
		for _, role := range []string{"answer", "extension"} {
			if out := transcriptionSettingsRequest(t, s, "PUT", role, body, 403); out["code"] != "forbidden" {
				t.Fatalf("role %s: %v", role, out)
			}
		}
	}
	// Neither enabling a transcriber nor changing unrelated settings opts the user into sending audio.
	f := newSettingsTranscribers()
	s.trans = f
	s.transcriptionReadinessChanged()
	check(get(), "device", "", []any{}, true)
	check(put(`{"recording_days":30,"meeting_transcription_ready":false}`), "device", "", []any{}, true)
	check(put(`{"meeting_glossary":[" Alice\nBo ","alice bo","","\t ","  A   B  "]}`), "device", "", []any{"Alice Bo", "A B"}, true)
	check(put(`{"meeting_transcription_default":"hub","meeting_transcription_extension":"example/text"}`), "hub", "example/text", []any{"Alice Bo", "A B"}, true)
	check(put(`{"recording_days":40}`), "hub", "example/text", []any{"Alice Bo", "A B"}, true)
	restarted := New(Options{Hub: newFakeHub().withMachine(), Store: s.store})
	restarted.trans = f
	check(transcriptionSettingsRequest(t, restarted, "GET", "", "", 200), "hub", "example/text", []any{"Alice Bo", "A B"}, true)
	f.set(extwire.TranscriptionInfo{}, errors.New("disabled"))
	check(put(`{"meeting_transcription_extension":"example/text","meeting_glossary":[]}`), "hub", "example/text", []any{}, false)
	check(put(`{"meeting_transcription_ready":true}`), "hub", "example/text", []any{}, false)
	check(put(`{"meeting_transcription_ready":{"ignored":true},"meeting_transcription_extension":"","meeting_transcription_default":"device"}`), "device", "", []any{}, false)
	var saved Settings
	if _, err := s.store().Setting(context.Background(), settingsKey, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.MeetingTranscriptionDefault == nil || *saved.MeetingTranscriptionDefault != "device" || saved.MeetingGlossary == nil || len(*saved.MeetingGlossary) != 0 || saved.MeetingTranscriptionExtension == nil || *saved.MeetingTranscriptionExtension != "" {
		t.Fatalf("not persisted: %+v", saved)
	}
}

func TestCleanMeetingGlossary(t *testing.T) {
	terms := make([]string, 200)
	for i := range terms {
		terms[i] = fmt.Sprintf("Term %d", i)
	}
	for _, tc := range []struct {
		name string
		in   []string
		want []string
		bad  bool
	}{
		{"empty", nil, []string{}, false},
		{"one line and case folding", []string{"\n Alice\r\n Bo\t", "alice bo", "K", "k", " "}, []string{"Alice Bo", "K"}, false},
		{"Unicode limit", []string{strings.Repeat("界", 40)}, []string{strings.Repeat("界", 40)}, false},
		{"Unicode over limit", []string{strings.Repeat("界", 41)}, nil, true},
		{"term limit", terms, terms, false},
		{"duplicates removed before limit", append(append([]string{}, terms...), "term 1", " "), terms, false},
		{"too many terms", append(append([]string{}, terms...), "Another term"), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanGlossary(tc.in)
			if (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, %v; want %#v, bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
	s := newTestService(t, newFakeHub().withMachine())
	body, _ := json.Marshal(map[string]any{"meeting_glossary": append(terms, "Another term")})
	if got := transcriptionSettingsRequest(t, s, "PUT", "", string(body), 400); got["code"] != "bad_glossary" {
		t.Fatalf("too many terms: %v", got)
	}
	bad := "unknown"
	if got := meetingTranscriptionDefaultOf(Settings{MeetingTranscriptionDefault: &bad}); got != "device" {
		t.Fatalf("invalid saved default: %s", got)
	}
}

type settingsTranscribers struct {
	ExtensionTools
	mu    sync.Mutex
	info  extwire.TranscriptionInfo
	err   error
	calls []string
	hook  func(context.Context, string)
}

func newSettingsTranscribers() *settingsTranscribers {
	return &settingsTranscribers{info: extwire.TranscriptionInfo{Protocol: extwire.TranscriptionProtocol, Features: []string{extwire.TranscriptionLive, extwire.TranscriptionReplay}}}
}

func (f *settingsTranscribers) TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error) {
	f.mu.Lock()
	f.calls = append(f.calls, ref)
	info, err, hook := f.info, f.err, f.hook
	f.mu.Unlock()
	if hook != nil {
		hook(ctx, ref)
	}
	return info, err
}

func (*settingsTranscribers) TranscriptionDial(context.Context, string) (*websocket.Conn, func(), error) {
	return nil, nil, errors.New("not used by settings")
}

func (*settingsTranscribers) IsTranscriptionContribution(ref string) bool {
	return ref == "example/text"
}

func (*settingsTranscribers) TranscriptionInstall(ref string) string {
	if ref == "example/text" {
		return "example@1"
	}
	return ""
}

func (f *settingsTranscribers) set(info extwire.TranscriptionInfo, err error) {
	f.mu.Lock()
	f.info, f.err = info, err
	f.mu.Unlock()
}

func TestMeetingTranscriptionReadiness(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	if s.transcriptionReadyNow() {
		t.Fatal("ready without transcribers")
	}
	f := newSettingsTranscribers()
	s.SetExtensions(f)
	if !s.transcriptionReadyNow() {
		t.Fatal("not ready with a usable transcriber")
	}
	good := f.info
	for _, tc := range []struct {
		name string
		info extwire.TranscriptionInfo
		err  error
	}{
		{"unavailable", good, errors.New("offline")},
		{"setup", extwire.TranscriptionInfo{Protocol: 1, Features: good.Features, Setup: extwire.TSetupLiveUpstream}, nil},
		{"protocol", extwire.TranscriptionInfo{Protocol: 2, Features: good.Features}, nil},
		{"missing live", extwire.TranscriptionInfo{Protocol: 1, Features: []string{extwire.TranscriptionReplay}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.set(tc.info, tc.err)
			if !s.transcriptionReadyNow() {
				t.Fatal("did not use cached true")
			}
			s.transcriptionReadinessChanged()
			if s.transcriptionReadyNow() {
				t.Fatal("ready after availability changed")
			}
			f.set(good, nil)
			if s.transcriptionReadyNow() {
				t.Fatal("did not use cached false")
			}
			s.transcriptionReady.mu.Lock()
			s.transcriptionReady.at = time.Now().Add(-transcriptionReadyInterval)
			s.transcriptionReady.mu.Unlock()
			if !s.transcriptionReadyNow() {
				t.Fatal("expired cache did not refresh")
			}
		})
	}
	// A contribution without replay transcribes live; its live sentences become final (meeting-minutes.md "How it ends").
	f.set(extwire.TranscriptionInfo{Protocol: 1, Features: []string{extwire.TranscriptionLive}}, nil)
	s.transcriptionReadinessChanged()
	if !s.transcriptionReadyNow() {
		t.Fatal("a contribution with live and no replay is not ready")
	}
	s.SetExtensions(nil)
	if s.transcriptionReadyNow() {
		t.Fatal("ready after removing transcribers")
	}
}

func TestMeetingTranscriptionReadinessDiscardsOldProbe(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	f := newSettingsTranscribers()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.hook = func(ctx context.Context, ref string) {
		if ref == "" {
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	s.trans = f
	go func() { defer close(finished); s.transcriptionReadyNow() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not start")
	}
	f.set(extwire.TranscriptionInfo{}, errors.New("disabled"))
	ref := "example/text"
	setSettings(t, s, func(v *Settings) { v.MeetingTranscriptionExtension = &ref })
	if s.transcriptionReadyNow() {
		t.Fatal("an in-flight old probe made the new choice ready")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not finish")
	}
	if s.transcriptionReadyNow() {
		t.Fatal("published an old probe result")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.calls, []string{"", ref}) {
		t.Fatalf("wrong selection or fallback: %v", f.calls)
	}
}

func TestMeetingTranscriptionReadinessLoop(t *testing.T) {
	old := transcriptionReadyInterval
	transcriptionReadyInterval = 10 * time.Millisecond
	t.Cleanup(func() { transcriptionReadyInterval = old })
	s := newTestService(t, newFakeHub().withMachine())
	f := newSettingsTranscribers()
	s.trans = f
	calls := make(chan struct{}, 16)
	f.hook = func(context.Context, string) {
		select {
		case calls <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.transcriptionReadyLoop(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("readiness loop did not stop")
		}
	})
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("periodic probe missing")
		}
	}
}

func TestMeetingTranscriptionDefaultSettingsTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := newTestService(t, newFakeHub().withMachine())
	const key = "assistant.meeting_transcription_default"
	tool := func(action, value string) (string, bool) {
		raw := `{"action":"` + action + `","key":"` + key + `"`
		if value != "" {
			raw += `,"value":` + value
		}
		raw += `}`
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(raw), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	get := func(mode string, ready bool) {
		t.Helper()
		out, isErr := tool("get", "")
		var got map[string]any
		if isErr || json.Unmarshal([]byte(out), &got) != nil || got["meeting_transcription_default"] != mode || got["meeting_transcription_ready"] != ready {
			t.Fatalf("get: %s, error=%v", out, isErr)
		}
	}
	get("device", false)
	if out, isErr := tool("set", `"device"`); isErr || out != i18n.T(s.lang(ctx), "assistant-settings_tool.meeting_transcription_unchanged") || len(s.pendingQuestions()) != 0 {
		t.Fatalf("unchanged: %s, error=%v", out, isErr)
	}
	if _, isErr := tool("set", `"automatic"`); !isErr || len(s.pendingQuestions()) != 0 {
		t.Fatal("invalid mode was offered")
	}
	change, err := s.setMeetingTranscriptionDefault(ctx, json.RawMessage(`"hub"`))
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		q, ok := awaitCard(s)
		if !ok {
			t.Error("no confirmation card")
			cancel()
			return
		}
		if q.Summary != change.card(s.lang(ctx)) || q.ToolName != "persona_settings" || q.Kind != proto.IxPermission {
			t.Errorf("unexpected card: %+v", q)
		}
		if got := meetingTranscriptionDefaultOf(s.settings(ctx)); got != "device" {
			t.Errorf("changed before approval: %s", got)
		}
		allow := true
		raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
			t.Error(err)
		}
	}()
	if out, isErr := tool("set", `"hub"`); isErr || out != i18n.T(s.lang(ctx), "assistant-settings_tool.meeting_transcription_applied", i18n.Args{"mode": i18n.T(s.lang(ctx), "assistant-settings_tool.meeting_transcription_hub")}) {
		t.Fatalf("approved: %s, error=%v", out, isErr)
	}
	<-finished
	get("hub", false)
	s.trans = newSettingsTranscribers()
	s.transcriptionReadinessChanged()
	get("hub", true)
	for _, setting := range s.ownSettings() {
		if setting.Key == "assistant.meeting_glossary" || setting.Key == "assistant.meeting_transcription_extension" {
			t.Fatalf("API-only setting exposed: %s", setting.Key)
		}
	}
}

func TestMeetingTranscriptionCardsAndNoDatabase(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	for _, ready := range []bool{false, true} {
		if ready {
			s.trans = newSettingsTranscribers()
			s.transcriptionReadinessChanged()
		}
		change, err := s.setMeetingTranscriptionDefault(ctx, json.RawMessage(`"hub"`))
		if err != nil {
			t.Fatal(err)
		}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			key := "assistant-settings_tool.change_meeting_transcription_default"
			if !ready {
				key = "assistant-settings_tool.meeting_transcription_not_ready"
			}
			want := i18n.T(lang, key, i18n.Args{"before": i18n.T(lang, "assistant-settings_tool.meeting_transcription_device"), "after": i18n.T(lang, "assistant-settings_tool.meeting_transcription_hub")})
			if got := change.card(lang); got != want || strings.Contains(got, "{before}") {
				t.Fatalf("%s, ready=%v: %s, want %s", lang, ready, got, want)
			}
		}
		const privacy = "In hub mode, the meeting’s audio is sent to the hub’s transcription service. The hub deletes the audio once the minutes are written."
		if card := change.card(i18n.En); !strings.Contains(card, privacy) || strings.Contains(card, "start sheet offers it only once it is ready") == ready {
			t.Fatalf("missing privacy or availability disclosure: %s", card)
		}
	}
	change, err := s.setMeetingTranscriptionDefault(ctx, json.RawMessage(`"hub"`))
	if err != nil {
		t.Fatal(err)
	}
	// Finish the readiness check before replacing the store it reads.
	s.hubTr.wg.Wait()
	s.store = func() Store { return nil }
	if _, err := change.apply(ctx); err == nil {
		t.Fatal("applied after database disappeared")
	}
	for _, mode := range []string{`"device"`, `"hub"`} {
		if _, err := s.setMeetingTranscriptionDefault(ctx, json.RawMessage(mode)); err == nil {
			t.Fatal("tool allowed a hub without a database")
		}
	}
	noDB := New(Options{Hub: newFakeHub().withMachine()})
	got := transcriptionSettingsRequest(t, noDB, "GET", "", "", 200)
	if got["meeting_transcription_default"] != "device" || got["meeting_transcription_ready"] != false {
		t.Fatalf("no database defaults: %v", got)
	}
}

func TestMeetingTranscriptionReadinessWakesOnChange(t *testing.T) {
	old := transcriptionReadyInterval
	transcriptionReadyInterval = time.Hour
	t.Cleanup(func() { transcriptionReadyInterval = old })
	s := newTestService(t, newFakeHub().withMachine())
	f := newSettingsTranscribers()
	s.trans = f
	calls := make(chan struct{}, 8)
	f.hook = func(context.Context, string) { calls <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.transcriptionReadyLoop(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("readiness loop did not stop")
		}
	})
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("readiness did not refresh without a GET or timer tick")
		}
		if i == 0 {
			f.set(extwire.TranscriptionInfo{}, errors.New("disabled"))
			s.transcriptionReadinessChanged()
		}
	}
}

func TestMeetingTranscriptionDefaultSettingsToolDeclined(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := newTestService(t, newFakeHub().withMachine())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		q, ok := awaitCard(s)
		if !ok {
			t.Error("no confirmation card")
			cancel()
			return
		}
		allow := false
		raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
			t.Error(err)
		}
	}()
	r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "c1",
		Arguments: json.RawMessage(`{"action":"set","key":"assistant.meeting_transcription_default","value":"hub"}`)})
	<-finished
	if r.IsError || meetingTranscriptionDefaultOf(s.settings(ctx)) != "device" {
		t.Fatalf("declined change applied: %+v", r)
	}
}
