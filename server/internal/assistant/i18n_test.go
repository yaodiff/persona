package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's texts for people follow the hub's user language (hello's locale, docs/protocol/i18n.md); a hub that says
// none keeps them Chinese, as they were.
func TestLangFollowsHello(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub()
	s := newStorelessService(t, f)
	if got := s.lang(ctx); got != i18n.ZhHans {
		t.Fatalf("no locale: %q", got)
	}
	if got := s.inbox.byRules(); got != "按规则" {
		t.Fatalf("by rules = %q", got)
	}
	f.mu.Lock()
	f.hello.Locale = "ja"
	f.mu.Unlock()
	if got := s.lang(ctx); got != i18n.Ja {
		t.Fatalf("ja: %q", got)
	}
	if got := s.inbox.mutedReason("dev"); got != "ミュート中：dev" {
		t.Fatalf("muted = %q", got)
	}
	f.mu.Lock()
	f.hello.Locale = "fr" // not one of ours: the default
	f.mu.Unlock()
	if got := s.lang(ctx); got != i18n.ZhHans {
		t.Fatalf("fr: %q", got)
	}
}

// A card's record reads in the language it is given; none is Chinese.
func TestAnsweredInLanguage(t *testing.T) {
	yes, no := true, false
	plan := proto.Interaction{Kind: proto.IxPlan, Plan: "Step one"}
	if got := answeredItems(i18n.En, plan, proto.Resolution{Approve: &yes, Mode: "acceptEdits", Always: true}); len(got) != 1 ||
		got[0].Question != "Approve this plan?\n\nStep one" || got[0].Answer != "Approved · Accept edits automatically · Don’t ask again for these" {
		t.Fatalf("en plan = %+v", got)
	}
	perm := proto.Interaction{Kind: proto.IxPermission, ToolName: "mcp__persona__mail_send",
		Fields: []proto.Field{{Key: "pw", Label: "Password", Secret: true}}}
	if got := answeredItems(i18n.Ja, perm, proto.Resolution{Allow: &yes, Values: map[string]string{"pw": "hunter2"}}); len(got) != 1 ||
		got[0].Question != "Persona · mail sendを使用" || got[0].Answer != "承認しました\nPassword：（入力済み、保存しません）" {
		t.Fatalf("ja permission = %+v", got)
	}
	if got := resolutionAnswer(i18n.En, perm, proto.Resolution{Allow: &no, Message: "later"}); got != "Denied: later" {
		t.Fatalf("en denied = %q", got)
	}
	if got := resolutionAnswer("", perm, proto.Resolution{Dismiss: true}); got != "忽略了" {
		t.Fatalf("default dismissed = %q", got)
	}
}

// The manager's own cards are in the user's language, and their answers are read back in any language.
func TestWorkCardsInLanguage(t *testing.T) {
	w, _, _ := triageRules(proto.Session{Status: proto.StatusDone, TurnError: "OAuth token has expired"}, time.UTC, i18n.En)
	if w == nil || len(w.Questions) != 1 || w.Questions[0].Options[0].Label != "Fixed, go on" || w.Questions[0].Options[1].Label != "Leave it" ||
		!strings.HasPrefix(w.Line, "Model service error: ") {
		t.Fatalf("en card = %+v", w)
	}
	w, _, _ = triageRules(proto.Session{Status: proto.StatusDone, TurnError: "OAuth token has expired"}, time.UTC, "")
	if w.Questions[0].Options[1].Label != answerSkip {
		t.Fatalf("default card = %+v", w)
	}
	for _, a := range []string{"Leave it", "今は放置", answerSkip} {
		if got := canonicalAnswer(a); got != answerSkip {
			t.Errorf("canonicalAnswer(%q) = %q", a, got)
		}
	}
	if got := canonicalAnswer("something else"); got != "something else" {
		t.Errorf("free text = %q", got)
	}
}

// A real owner-step plan's push reads in the user's language.
func TestPlanPushInLanguage(t *testing.T) {
	s, f := newTaskService(t)
	f.mu.Lock()
	f.hello.Locale = "en"
	f.mu.Unlock()
	if _, err := s.toolTaskPlan(context.Background(), "conv1", planArgsWithStep(t, ownerStepMoney)); err != nil {
		t.Fatal(err)
	}
	if p := f.pushed(); len(p) != 1 || p[0].Title != "Something is waiting for you to start" {
		t.Fatalf("pushes = %+v", p)
	}
}

// An app's request relayed to the assistant gets an i18n error in its Accept-Language, else the user's; with
// neither, Chinese.
func TestRelayedErrorLanguage(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-schedule.gone"))
	})
	errorOf := func(header map[string][]string, fallback func(context.Context) i18n.Lang) string {
		res, err := serveHTTP(context.Background(), h, assistantwire.HTTPRequest{Method: "GET", Path: "/x", Header: header}, fallback)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(res.Body, &body)
		return body.Error
	}
	ja := func(context.Context) i18n.Lang { return i18n.Ja }
	if got := errorOf(map[string][]string{"Accept-Language": {"en"}}, ja); got != "This scheduled task no longer exists" {
		t.Fatalf("en = %q", got)
	}
	if got := errorOf(nil, ja); got != "この定期タスクはもうありません" {
		t.Fatalf("user's ja = %q", got)
	}
	if got := errorOf(nil, nil); got != "这个定时任务已经不在了" {
		t.Fatalf("default = %q", got)
	}
}

// Supervision's punctuation is a trim set, not UI text: remove trailing stops, preserving diagnostics inside the reason.
func TestTaskFailureNoticePreservesReasonAndLocalizesAdvice(t *testing.T) {
	s, f := newTaskService(t)
	for _, tc := range []struct{ locale, title, stop, advice string }{
		{"en", "“Demo task” couldn’t be started", ". ", "Open the conversation to tap “Retry”, or assign to another computer"},
		{"zh-Hans", "「Demo task」没开起来", "。", "打开对话点『重试』，或换一台电脑再派"},
		{"ja", "「Demo task」を開始できませんでした", "。", "会話を開いて「再試行」をタップするか、別のパソコンに割り当ててください"},
	} {
		f.mu.Lock()
		f.hello.Locale = tc.locale
		f.mu.Unlock()
		for _, reason := range []struct{ raw, trimmed string }{
			{"  Original。diagnostic.。  ", "Original。diagnostic"},
			{" .。 ", ""},
		} {
			before := len(f.pushed())
			s.notifyTaskFailed(context.Background(), Todo{Title: "Demo task", Origin: "conv9"}, "box", reason.raw)
			pushes := f.pushed()
			if len(pushes) != before+1 {
				t.Fatalf("locale=%q reason=%q: got %d pushes, want %d", tc.locale, reason.raw, len(pushes), before+1)
			}
			p := pushes[before]
			want := tc.advice
			if reason.trimmed != "" {
				want = reason.trimmed + tc.stop + want
			}
			if p.Title != tc.title || p.Body != want {
				t.Fatalf("locale=%q reason=%q: title=%q body=%q, want %q / %q", tc.locale, reason.raw, p.Title, p.Body, tc.title, want)
			}
		}
	}
}
