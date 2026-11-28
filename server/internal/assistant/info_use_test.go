package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// infoUseService is a service with the registry tracks qa-plain (no default categories) and qa-finance (finance), a
// store that keeps information categories, and conversations noted in the main space, both tracks and the health space.
func infoUseService(t *testing.T) (*Service, *fakeHub, *infoCategoryMemStore) {
	t.Helper()
	ctx := context.Background()
	st := &infoCategoryMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: "qa-finance", Kind: trackKindSpace, Template: trackTemplateSpace, DefaultCategories: []string{categoryFinance}},
		{Slug: "qa-plain", Kind: trackKindSpace, Template: trackTemplateSpace},
	}}}
	f := newFakeHub()
	f.infoTagModel = true
	s := newServiceOn(t, f, st)
	s.noteConversationSpaces(ctx, []db.ConversationSpace{{MachineID: "qa-machine", SessionID: "qa-main", Space: ""},
		{MachineID: "qa-machine", SessionID: "qa-plain-1", Space: "qa-plain"},
		{MachineID: "qa-machine", SessionID: "qa-finance-1", Space: "qa-finance"},
		{MachineID: "qa-machine", SessionID: "qa-health-1", Space: SpaceHealth}})
	return s, f, st
}

// A text on its way out (#13246; scenes 3, 4, 5 and 7) carries its own tags and its track's default categories, never
// its conversation's accumulated ones: a general text from a conversation once tagged finance goes as general. A
// track sensitive by default, or one the registry does not know, asks no model; a text that cannot be tagged takes
// its conversation's categories when the conversation was tagged, else it is sensitive.
func TestExitUse(t *testing.T) {
	ctx := context.Background()
	s, f, st := infoUseService(t)
	if !s.infoTagConfigured(ctx) {
		t.Fatal("info.tag has no model")
	}
	if got := s.exitPolicy(ctx, "qa: a step count", "qa-health-1", SpaceHealth); got != strictPolicy || len(f.infoTagAsked) != 0 {
		t.Errorf("health = %+v after %d calls, want strict without a call", got, len(f.infoTagAsked))
	}
	if got := s.exitPolicy(ctx, "qa: anything", "qa-x", "qa-unknown"); got != strictPolicy || len(f.infoTagAsked) != 0 {
		t.Errorf("an unknown track = %+v after %d calls, want strict without a call", got, len(f.infoTagAsked))
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.exitPolicy(ctx, "qa: the build passed", "qa-main", ""); got != (TrackPolicy{}) || len(f.infoTagAsked) != 1 {
		t.Errorf("a general text from a conversation tagged finance = %+v (%d calls), want all off", got, len(f.infoTagAsked))
	}
	if f.infoTagAsked[0].User != "qa: the build passed" {
		t.Errorf("tagged %q", f.infoTagAsked[0].User)
	}
	f.mu.Lock()
	f.infoTag = `{"categories":["legal_id"]}`
	f.mu.Unlock()
	if got := s.exitPolicy(ctx, "qa: a placeholder passport number", "qa-main", ""); got != strictPolicy {
		t.Errorf("a sensitive text = %+v, want strict", got)
	}
	if got := s.exitPolicy(ctx, "qa: the build passed", "qa-main", ""); got != (TrackPolicy{}) {
		t.Errorf("the same general text again (cached) = %+v, want all off", got)
	}
	if got := s.exitPolicy(ctx, "  ", "qa-plain-1", "qa-plain"); got != (TrackPolicy{}) {
		t.Errorf("an empty text = %+v, want its track's (all off)", got)
	}

	// Tagging fails: the conversation's categories, when it was tagged.
	f.mu.Lock()
	f.infoTagFails = true
	f.mu.Unlock()
	if got := s.exitPolicy(ctx, "qa: new text 1", "qa-plain-1", "qa-plain"); got != strictPolicy {
		t.Errorf("failed, conversation never tagged = %+v, want strict", got)
	}
	if err := st.AddInfoCategories(ctx, db.InfoCategoryConversation, "qa-plain-1", []string{infoNoneTagged}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.exitPolicy(ctx, "qa: new text 2", "qa-plain-1", "qa-plain"); got != (TrackPolicy{}) {
		t.Errorf("failed, conversation tagged with none = %+v, want all off", got)
	}
	if got := s.exitPolicy(ctx, "qa: new text 3", "qa-main", ""); got != (TrackPolicy{}) {
		t.Errorf("failed, main-space conversation tagged finance = %+v, want all off (general)", got)
	}
	s.infoTagTurnEnded(ctx, "qa-main")
	if got := s.exitPolicy(ctx, "qa: new text 3b", "qa-main", ""); got != (TrackPolicy{}) {
		t.Errorf("failed, main-space conversation with a turn waiting = %+v, want all off (general)", got)
	}
	s.infoTagTurnEnded(ctx, "qa-plain-1")
	if got := s.exitPolicy(ctx, "qa: new text 4", "qa-plain-1", "qa-plain"); got != strictPolicy {
		t.Errorf("failed, a turn waiting to be tagged = %+v, want strict", got)
	}
	if got := s.exitPolicy(ctx, "qa: new text 5", "", ""); got != strictPolicy {
		t.Errorf("failed, no conversation = %+v, want strict", got)
	}
}

// A conversation as a whole (scenes 6 and 2) carries its accumulated categories: its track's floor and its tags; while
// a turn of it waits to be tagged they are not known.
func TestConversationUse(t *testing.T) {
	ctx := context.Background()
	s, _, _ := infoUseService(t)
	for sid, want := range map[string]TrackPolicy{"qa-main": {}, "qa-plain-1": {}, "qa-finance-1": strictPolicy, "qa-health-1": strictPolicy} {
		if got := s.conversationPolicy(ctx, sid); got != want {
			t.Errorf("%s = %+v, want %+v", sid, got, want)
		}
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryEmotion}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.conversationPolicy(ctx, "qa-main"); got != strictPolicy {
		t.Errorf("a conversation tagged emotion = %+v, want strict", got)
	}
	if !s.infoTagConfigured(ctx) {
		t.Fatal("info.tag has no model")
	}
	s.infoTagTurnEnded(ctx, "qa-plain-1")
	if got := s.conversationUse(ctx, "qa-plain-1"); got.Known {
		t.Errorf("a turn waiting to be tagged: %+v, want not known", got)
	}
	s.infoTagWaiting.set("qa-plain-1", false)
	if got := s.conversationPolicy(ctx, "qa-plain-1"); got != (TrackPolicy{}) {
		t.Errorf("tagged since = %+v, want all off", got)
	}
}

// Outside the health space a text is judged by what it says (#13246): a notify_user from the main space whose words
// are tagged sensitive reaches the lock screen as news without its words (a notice that names no space), and an
// agent_send whose words are tagged sensitive is confirmed on the phone with a card that names no space; general words
// go out as before.
func TestSensitiveTextOutsideHealth(t *testing.T) {
	f := healthHub()
	f.infoTagModel = true
	f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: "code-0000-0000", Cwd: "/home/me/src/app", Agent: "claude",
		Status: proto.StatusIdle, Title: "app"})
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	if !s.infoTagConfigured(context.Background()) {
		t.Fatal("info.tag has no model")
	}
	if _, isErr := spaceTool(t, s, "work-0000-0000", "notify_user", `{"title":"qa build","body":"all green"}`); isErr {
		t.Fatal("general notify_user")
	}
	f.mu.Lock()
	f.infoTag = `{"categories":["finance"]}`
	f.mu.Unlock()
	if _, isErr := spaceTool(t, s, "work-0000-0000", "notify_user", `{"title":"qa invoice","body":"qa amount 100"}`); isErr {
		t.Fatal("sensitive notify_user")
	}
	l := s.lang(context.Background())
	p := f.pushed()
	if len(p) != 2 || p[0].Title != "qa build" || p[0].Body != "all green" ||
		p[1].Title != i18n.T(l, "assistant-health.sensitive_notice_title") || p[1].Body != i18n.T(l, "assistant-health.sensitive_notice_body") {
		t.Fatalf("pushes: %+v", p)
	}
	// Words the owner just confirmed on the phone (an Inbox suggestion's instruction) are not asked about again.
	if ok, _, err := s.confirmOutbound(ownerConfirmed(context.Background()), "server", "work-0000-0000", "agent_start", "qa invoice total",
		"qa", nil); err != nil || !ok {
		t.Fatalf("owner-confirmed words: %v %v", ok, err)
	}
	answerOutboundIn(t, s, "work-0000-0000", "agent_send", "qa invoice total", "/home/me/.persona/assistant", false)
	out, isErr := spaceTool(t, s, "work-0000-0000", "agent_send", `{"session_id":"code-0000-0000","text":"qa invoice total"}`)
	if isErr || !strings.Contains(out, "declined") || len(f.agentSent) != 0 {
		t.Fatalf("declined agent_send: %s, sent %v", out, f.agentSent)
	}
}

// Exercise the outbound classifier prompt and the approval decision through agent_send. The fake model supplies
// categories, so this checks the prompt and gate together without relying on live model responses.
func TestAgentSendPrivacyClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		text       string
		categories string
	}{
		{"technical token usage", "请减少 token 消耗。更新技能说明并提交代码，先不要跑评测。", `{"categories":[]}`},
		{"project budget", "Keep API costs under $20 for the project evaluation. Update the config and leave the run paused.", `{"categories":[]}`},
		{"personal finance with token usage", "Reduce token usage. The API charged $20 to my personal credit card and I cannot pay the bill.", `{"categories":["finance"]}`},
		{"health with token usage", "Keep token usage low. I was diagnosed with diabetes last week.", `{"categories":["health"]}`},
		{"identity with token usage", "Keep token usage low. My passport number is TEST-0000.", `{"categories":["legal_id"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := healthHub()
			f.infoTagModel = true
			f.infoTag = tc.categories
			f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: "code-0000-0000",
				Cwd: "/home/me/src/app", Agent: "claude", Status: proto.StatusIdle, Title: "app"})
			s := newTestService(t, f)
			setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
			const caller = "work-0000-0000"
			const tool = "agent_send"
			private := tc.categories != `{"categories":[]}`
			if private {
				answerOutboundIn(t, s, caller, tool, tc.text, "/home/me/.persona/assistant", false)
			}
			raw, err := json.Marshal(map[string]string{"session_id": "code-0000-0000", "text": tc.text})
			if err != nil {
				t.Fatal(err)
			}
			out, isErr := spaceTool(t, s, caller, tool, string(raw))
			f.mu.Lock()
			defer f.mu.Unlock()
			if isErr || private && (!strings.Contains(out, "declined") || len(f.agentSent) != 0) ||
				!private && (len(f.agentSent) != 1 || f.agentSent[0]["text"] != tc.text) {
				t.Fatalf("agent_send = %q (error %v), sends = %v", out, isErr, f.agentSent)
			}
			if len(f.infoTagAsked) != 1 || f.infoTagAsked[0].User != tc.text {
				t.Fatalf("outbound classifier calls = %+v", f.infoTagAsked)
			}
			jsonOnly := strings.TrimSpace(prompts.RenderJSONOnly(""))
			golden(t, "info_tag", strings.TrimSuffix(f.infoTagAsked[0].System, "\n\n"+jsonOnly))
		})
	}
}

// A connector call says where it comes from (#13246): the conversation's track ("main" for the main track), its
// kind, and its categories as a whole; none for a conversation the manager cannot place.
func TestConnectorCallContext(t *testing.T) {
	ctx := context.Background()
	s, _, _ := infoUseService(t)
	main := s.connectorCallContext(ctx, "qa-main")
	if main.Track != "main" || main.TrackKind != "" || main.Categories == nil || len(main.Categories) != 0 {
		t.Errorf("main: %+v", main)
	}
	fin := s.connectorCallContext(ctx, "qa-finance-1")
	if fin.Track != "qa-finance" || fin.TrackKind != trackKindSpace || len(fin.Categories) != 1 || fin.Categories[0] != categoryFinance {
		t.Errorf("finance track: %+v", fin)
	}
	if got := s.connectorCallContext(ctx, "qa-nowhere"); got.Track != "" || got.Categories != nil {
		t.Errorf("a conversation the manager cannot place: %+v", got)
	}
}

// Tagging timeouts (context.DeadlineExceeded) and model failures count as sensitive across scenes 2-7 (#13246), while
// inside the source track (scene 1) the information remains allowed.
func TestTaggingTimeoutCountsAsSensitiveInScenes2To7(t *testing.T) {
	ctx := context.Background()
	s, _, _ := infoUseService(t)
	timedOutCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	info := s.exitUse(timedOutCtx, "qa: timed out text", "qa-plain-1", "qa-plain")
	if info.Known {
		t.Fatalf("timed-out exitUse should be unknown: %+v", info)
	}
	want := map[scene]usageRule{
		sceneSourceTrack: usageAllow,
		sceneElsewhere:   usageMinimal,
		sceneHandOff:     usageConfirm,
		sceneLockScreen:  usageRedact,
		sceneOthers:      usageConfirm,
		sceneExtension:   usageDeny,
		sceneKBWrite:     usageDeny,
	}
	for _, sc := range scenes {
		if got := usage(info, sc, extensionGrant{}); got != want[sc] {
			t.Errorf("timed-out scene %d = %d, want %d", sc, got, want[sc])
		}
	}
	if got := infoTagFailure(context.DeadlineExceeded); got != "timed out" {
		t.Errorf("infoTagFailure(DeadlineExceeded) = %q, want timed out", got)
	}
}

// assistant.send carries conversation categories for PERSONA_CATEGORIES (#13246): general [], accumulated tags,
// track floor + tags, and categories_unknown when a turn waits to be tagged or the store cannot be read.
func TestPutConversationCategories(t *testing.T) {
	ctx := context.Background()
	s, _, st := infoUseService(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	p := map[string]any{}
	s.putConversationCategories(ctx, "qa-main", p)
	if cats, ok := p["categories"].([]string); !ok || len(cats) != 0 || p["categories_unknown"] != nil {
		t.Fatalf("general main: %#v", p)
	}

	must(s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""))
	s.putConversationCategories(ctx, "qa-main", p)
	if cats, ok := p["categories"].([]string); !ok || !slices.Equal(cats, []string{categoryFinance}) || p["categories_unknown"] != nil {
		t.Fatalf("tagged main: %#v", p)
	}

	must(s.tagConversation(ctx, "qa-health-1", []string{categoryEmotion}, infoTaggedByModel, ""))
	s.putConversationCategories(ctx, "qa-health-1", p)
	if cats, ok := p["categories"].([]string); !ok || !slices.Equal(cats, []string{categoryEmotion, categoryHealth}) {
		t.Fatalf("health + emotion: %#v", p)
	}

	// Waiting turn -> categories_unknown.
	s.infoTagTurnEnded(ctx, "qa-main")
	s.putConversationCategories(ctx, "qa-main", p)
	if p["categories"] != nil || p["categories_unknown"] != true {
		t.Fatalf("turn waiting: %#v", p)
	}
	s.infoTagWaiting.set("qa-main", false)

	// Unreadable store -> categories_unknown.
	st.readErr = errors.New("qa: store down")
	s.putConversationCategories(ctx, "qa-main", p)
	if p["categories"] != nil || p["categories_unknown"] != true {
		t.Fatalf("store error: %#v", p)
	}
	st.readErr = nil

	// Unplaced conversation fails closed with categories_unknown rather than falling back to the directory default.
	s.putConversationCategories(ctx, "qa-nowhere", p)
	if p["categories"] != nil || p["categories_unknown"] != true {
		t.Fatalf("unplaced conversation: %#v", p)
	}
}

// SendParams attaches manager-attested categories to session.send for assistant conversations (#13246) and strips any
// client-supplied categories or categories_unknown fields.
func TestSendParamsAttestsConversationCategories(t *testing.T) {
	ctx := context.Background()
	s, f, _ := infoUseService(t)
	f.sessions["qa-machine"] = []proto.Session{
		{SessionID: "qa-main", Cwd: "/home/me/.persona/assistant", Assistant: true, Agent: "claude"},
		{SessionID: "qa-health-1", Cwd: "/home/me/.persona/health", Space: SpaceHealth, Assistant: true, Agent: "claude"},
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	p, err := s.SendParams(ctx, SendParams{
		MachineID: "qa-machine",
		SessionID: "qa-main",
		Params:    map[string]any{"text": "follow up", "categories": []string{}, "categories_unknown": true},
	})
	if err != nil {
		t.Fatalf("SendParams main: %v", err)
	}
	if cats, ok := p["categories"].([]string); !ok || !slices.Equal(cats, []string{categoryFinance}) || p["categories_unknown"] != nil {
		t.Fatalf("SendParams main categories: %#v", p)
	}

	s.infoTagTurnEnded(ctx, "qa-main")
	p, err = s.SendParams(ctx, SendParams{
		MachineID: "qa-machine",
		SessionID: "qa-main",
		Params:    map[string]any{"text": "while waiting", "categories": []string{}},
	})
	if err != nil {
		t.Fatalf("SendParams waiting: %v", err)
	}
	if p["categories"] != nil || p["categories_unknown"] != true {
		t.Fatalf("SendParams waiting categories: %#v", p)
	}
	s.infoTagWaiting.set("qa-main", false)

	p, err = s.SendParams(ctx, SendParams{
		MachineID: "qa-machine",
		SessionID: "qa-health-1",
		Params:    map[string]any{"text": "health follow up"},
	})
	if err != nil {
		t.Fatalf("SendParams health: %v", err)
	}
	if cats, ok := p["categories"].([]string); !ok || !slices.Equal(cats, []string{categoryHealth}) || p["categories_unknown"] != nil {
		t.Fatalf("SendParams health categories: %#v", p)
	}
}
