package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTriageKeepsGroundedNaturalRequests(t *testing.T) {
	for _, tc := range []struct {
		lang, reply, request string
	}{
		{"zh-Hans", "预览已经好了。这两个配色你拿个主意就行", "这两个配色你拿个主意就行"},
		{"en", "The preview is ready. I'll need your pick between the two palettes.", "I'll need your pick between the two palettes."},
		{"ja", "プレビューができました。配色はどちらにするか決めてもらえると助かります。", "配色はどちらにするか決めてもらえると助かります。"},
	} {
		for _, todosV2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/todos_v2=%t", tc.lang, todosV2), func(t *testing.T) {
				s, f := workService(t)
				setSettings(t, s, func(cfg *Settings) { cfg.TodosV2 = boolPtr(todosV2) })
				out, err := json.Marshal(triageOut{Phase: proto.WorkBlocked, On: proto.WaitUser,
					Questions: []proto.Question{{Question: tc.request}}})
				if err != nil {
					t.Fatal(err)
				}
				setTriageAnswer(f, string(out))
				f.setSessions("box", stopped("s1", tc.reply))
				triage(t, s, "s1")
				if n := len(triageCalls(f)); n != 1 {
					t.Fatalf("model calls = %d, want 1", n)
				}
				cards := s.workCards()
				if len(cards) != 1 || len(cards[0].Questions) != 1 || cards[0].Questions[0].Question != tc.request {
					t.Fatalf("cards = %+v, want one asking %q", cards, tc.request)
				}
			})
		}
	}
}

func TestTriageStructuredInteractionSkipsModel(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		pending      int
	}{
		{"pending interaction", proto.StatusDone, 1},
		{"blocked terminal", proto.StatusBlocked, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workService(t)
			setTriageAnswer(f, `{"phase":"blocked","on":"user","questions":[{"question":"Which palette should I use?"}]}`)
			c := stopped("s1", "Which palette should I use?")
			c.Status, c.PendingInteractions = tc.status, tc.pending
			f.setSessions("box", c)
			triage(t, s, "s1")
			if reads, calls := itemReads(f), len(triageCalls(f)); reads != 0 || calls != 0 {
				t.Fatalf("structured interaction triggered %d transcript reads and %d model calls", reads, calls)
			}
			if cards := s.workCards(); len(cards) != 0 {
				t.Fatalf("structured interaction got duplicate cards: %+v", cards)
			}
		})
	}
}

func TestTriageNonUserClassificationsDoNotCreateQuestionCards(t *testing.T) {
	const reply = "The preview is ready."
	for _, tc := range []struct {
		name, answer, phase, on string
	}{
		{"done", `{"phase":"done","on":"user","questions":[{"question":"The preview is ready."}]}`, proto.WorkDone, ""},
		{"external", `{"phase":"blocked","on":"external","questions":[{"question":"The preview is ready."}]}`, proto.WorkBlocked, proto.WaitExternal},
		{"agent", `{"phase":"blocked","on":"agent","wait":{"agent":"helper"},"questions":[{"question":"The preview is ready."}]}`, proto.WorkBlocked, proto.WaitAgent},
		{"empty questions", `{"phase":"blocked","on":"user","line":"The preview is ready.","questions":[]}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workService(t)
			setTriageAnswer(f, tc.answer)
			f.setSessions("box", stopped("s1", reply), proto.Session{SessionID: "helper", Status: proto.StatusWorking})
			triage(t, s, "s1")
			w := entryOf(t, s, "s1").Work
			if w.Phase != tc.phase || w.On != tc.on || len(w.Questions) != 0 || len(s.workCards()) != 0 {
				t.Fatalf("work = %+v, want phase %q on %q without a question card", w, tc.phase, tc.on)
			}
		})
	}
}

func answeredRecord(question, answer string) db.InteractionRecord {
	return db.InteractionRecord{Status: "resolved", Interaction: proto.Interaction{Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: question}}}, Resolution: &proto.Resolution{Answers: map[string]string{question: answer}}}
}

func TestTriageHistoryExcludesNonDecisions(t *testing.T) {
	for _, lang := range i18n.Langs {
		for _, key := range []string{answerSkip, answerNotNow, answerHold, answerWait, answerStop} {
			t.Run(string(lang)+"/"+key, func(t *testing.T) {
				rec := answeredRecord("Continue?", answerLabel(lang, key))
				if got := triageAnswered([]db.InteractionRecord{rec}); len(got) != 0 {
					t.Fatalf("deferral became a decision: %+v", got)
				}
			})
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*db.InteractionRecord)
	}{
		{"structured defer", func(r *db.InteractionRecord) {
			r.Interaction.Questions[0].Options = []proto.QuestionOption{{Label: "Custom pause", Action: proto.QuestionOptionDefer}}
			r.Resolution.Answers["Continue?"] = "Custom pause"
		}},
		{"legacy defer", func(r *db.InteractionRecord) {
			r.Interaction.Questions[0].Options = []proto.QuestionOption{{Label: "先不管，稍后再决定"}}
			r.Resolution.Answers["Continue?"] = "先不管，稍后再决定"
		}},
		{"assistant answer", func(r *db.InteractionRecord) { r.Resolution.AnsweredBy = proto.AnsweredByAssistant }},
		{"permission", func(r *db.InteractionRecord) { r.Interaction.Kind = proto.IxPermission }},
		{"pending", func(r *db.InteractionRecord) { r.Status = "pending" }},
		{"cancelled", func(r *db.InteractionRecord) { r.Status = "cancelled" }},
		{"cancel", func(r *db.InteractionRecord) { r.Resolution.Cancel = true }},
		{"dismiss", func(r *db.InteractionRecord) { r.Resolution.Dismiss = true }},
		{"missing resolution", func(r *db.InteractionRecord) { r.Resolution = nil }},
		{"empty answer", func(r *db.InteractionRecord) { r.Resolution.Answers["Continue?"] = "  " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := answeredRecord("Continue?", "Use beta")
			tc.edit(&rec)
			got := triageAnswered([]db.InteractionRecord{rec, answeredRecord("Which target?", "Use beta")})
			if len(got) != 1 || got[0].Question != "Which target?" || got[0].Answer != "Use beta" {
				t.Fatalf("wrong decision history: %+v", got)
			}
		})
	}
}

func TestTriageHistoryBoundsPairsAndRunes(t *testing.T) {
	// The store orders newest first. Several questions on one card count as separate pairs.
	records := []db.InteractionRecord{answeredRecord("Pause?", answerLabel("en", answerSkip))}
	var questions []string
	for i := range 10 {
		rec := answeredRecord("", "")
		rec.Interaction.Questions = nil
		for j := range 3 {
			q := fmt.Sprintf("question-%02d-%d-", i, j) + strings.Repeat("界", 250)
			questions = append(questions, q)
			rec.Interaction.Questions = append(rec.Interaction.Questions, proto.Question{Question: q})
			rec.Resolution.Answers[q] = strings.Repeat("答", 250)
		}
		records = append(records, rec)
	}
	got := triageAnswered(records)
	if len(got) != 20 {
		t.Fatalf("got %d pairs", len(got))
	}
	for i, pair := range got {
		if pair.Question != string([]rune(questions[i])[:200]) || pair.Answer != strings.Repeat("答", 200) ||
			utf8.RuneCountInString(pair.Question) != 200 || !utf8.ValidString(pair.Question) || !utf8.ValidString(pair.Answer) {
			t.Fatalf("pair %d is out of order or not bounded by runes: %+v", i, pair)
		}
	}
}
