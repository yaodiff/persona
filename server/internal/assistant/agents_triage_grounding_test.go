package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Sanitized incident replies: no AskUserQuestion was called, but triage invented a question after each answer.
func TestTriageRequiresAnActualRequest(t *testing.T) {
	for _, tc := range []struct {
		name, reply, question string
		wantCard              bool
	}{
		{"arrangement", "好的，等 worker-a 交回验证结果后，我就在 #123 追加评论，写明验证结论。现在还没收到它的结果。", "worker-a 交回验证结果后，是否直接按计划在 #123 追加验证结论评论？", false},
		{"acknowledgment", "好，收到 worker-a 的结果后我直接在 #123 发评论。目前还没收到结果。", "worker-a 交回验证结果后，直接在 #123 发验证结论评论吗？", false},
		{"already confirmed", "收到，不用再确认了，结果一到我就直接在 #123 发评论。", "结果回来直接发吗？", false},
		{"short acknowledgment", "收到，结果到了就发", "", false},
		{"blank request", "等 worker-a 验证结果，回来直接发 #123", " \t ", false},
		{"genuine question", "结果已到。要现在在 #123 发验证结论评论吗？", "要现在在 #123 发验证结论评论吗？", true},
		{"explicit request", "请提供验证环境的地址。", "请提供验证环境的地址。", true},
		{"English request", "Please choose the target environment.", "Please choose the target environment.", true},
		{"Japanese request", "確認してください：テスト環境で実行します。", "確認してください：テスト環境で実行します。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workService(t)
			out := triageOut{Phase: proto.WorkBlocked, On: proto.WaitUser, Line: tc.reply}
			if tc.question != "" {
				out.Questions = []proto.Question{{Question: tc.question, Options: []proto.QuestionOption{{Label: "Continue"}, {Label: "Wait"}}}}
			}
			b, _ := json.Marshal(out)
			f.triage = string(b)
			// The latest user request contains the invented question; it is not evidence of an assistant request.
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "关于你上面的问题，我的回答：\n- " + tc.question + " → 直接发"},
				proto.Item{Kind: proto.KindText, Role: "assistant", Text: tc.reply})
			f.setSessions("box", stopped("s1", tc.reply))
			triage(t, s, "s1")
			e := entryOf(t, s, "s1")
			if got := len(s.workCards()); (got == 1) != tc.wantCard || (e.Work.On == proto.WaitUser) != tc.wantCard {
				t.Fatalf("cards=%d work=%+v", got, e.Work)
			}
			if e.WakeAt != nil || len(f.sentToAgents()) != 0 {
				t.Fatal("rejecting a fabricated request must not authorize an automatic action")
			}
			s.waitWorkEvents()
			s.work.mu.Lock()
			events := append([]db.WorkEvent(nil), s.work.testEvents...)
			s.work.mu.Unlock()
			var recorded bool
			for _, event := range events {
				if event.Kind == "triaged" {
					recorded = true
					if event.Detail["phase"] != e.Work.Phase {
						t.Fatalf("triaged phase=%v, stored phase=%q", event.Detail["phase"], e.Work.Phase)
					}
				}
			}
			if !recorded {
				t.Fatal("no triaged event")
			}
		})
	}
}

func TestTriageReadsDurableAnswers(t *testing.T) {
	store := newFleetMem()
	question := "Should I post the verification on #123?"
	for _, status := range []string{"resolved", "pending", "cancelled"} {
		store.recs = append(store.recs, db.InteractionRecord{
			Interaction: proto.Interaction{ID: status, MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
				Questions: []proto.Question{{Question: question + status}}},
			Status: status, Resolution: &proto.Resolution{Answers: map[string]string{question + status: "Post when worker-a returns"}},
		})
	}
	store.recs = append(store.recs, db.InteractionRecord{Interaction: proto.Interaction{MachineID: "other", SessionID: "s1",
		Questions: []proto.Question{{Question: "Private other session"}}}, Status: "resolved",
		Resolution: &proto.Resolution{Answers: map[string]string{"Private other session": "yes"}}})
	// Fresh services read the same durable history; no in-memory decision cache is required.
	for range 2 {
		s, f := workService(t)
		s.setStore(func() Store { return store })
		f.setSessions("box", stopped("s1", "收到，结果到了就发"))
		f.triage = `{"phase":"blocked","on":"user","questions":[{"question":"Post on #123 now?"}]}`
		triage(t, s, "s1")
		if len(f.triageAsked) != 1 {
			t.Fatalf("calls: %d", len(f.triageAsked))
		}
		input := f.triageAsked[0].User
		if !strings.Contains(input, question+"resolved") || !strings.Contains(input, "Post when worker-a returns") ||
			strings.Contains(input, question+"pending") || strings.Contains(input, question+"cancelled") || strings.Contains(input, "Private other session") {
			t.Fatalf("wrong decision context: %s", input)
		}
		if len(s.workCards()) != 0 || entryOf(t, s, "s1").WakeAt != nil {
			t.Fatal("an acknowledgment became a card or an automatic wake")
		}
		// A different target still needs a decision; history is not blanket suppression or authorization.
		f.triage = `{"phase":"blocked","on":"user","questions":[{"question":"Should I post on #124 too?"}]}`
		next := stopped("s1", "Should I post on #124 too?")
		next.UpdatedAt = turnEnd.Add(time.Minute)
		f.setSessions("box", next)
		triage(t, s, "s1")
		if len(s.workCards()) != 1 {
			t.Fatal("history suppressed a new target's question")
		}
	}
}

type brokenTriageHistory struct{ *fleetMem }

func (brokenTriageHistory) Interactions(context.Context, string, string, int) ([]db.InteractionRecord, error) {
	return nil, errors.New("history unavailable")
}

func TestTriageHistoryFailureIsRetryable(t *testing.T) {
	s, f := workService(t)
	s.setStore(func() Store { return brokenTriageHistory{newFleetMem()} })
	f.setSessions("box", stopped("s1", "Post now?"))
	triage(t, s, "s1")
	if _, ok := s.workEntry([2]string{"box", "s1"}); ok || len(f.triageAsked) != 0 || len(s.workCards()) != 0 {
		t.Fatal("triage proceeded without decision history")
	}
}

func TestReadOnlyTriageDoesNotStoreAnUnsupportedUserWait(t *testing.T) {
	s, f := workService(t)
	const reply = "The checks passed. The scheduled job will run next."
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: reply})
	f.triage = `{"phase":"blocked","on":"user","line":"Approval needed","questions":[{"question":"Should I run the job?"}]}`
	row := stopped("s1", reply)
	row.ReadOnlyReason, row.Capabilities = "Read-only history", &proto.SessionCapabilities{Read: true}
	f.setSessions("box", row)
	triage(t, s, "s1")
	if work := entryOf(t, s, "s1").Work; work.Phase != "" || work.On != "" {
		t.Fatalf("read-only triage kept an unsupported wait: %+v", work)
	}
	if len(s.workCards()) != 0 || len(f.sentToAgents()) != 0 {
		t.Fatal("read-only triage tried to act on the unsupported wait")
	}
}
