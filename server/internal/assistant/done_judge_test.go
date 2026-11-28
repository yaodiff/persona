package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
)

// cannedJSON is a jsonModel that answers every call with the same JSON (or error) and keeps what it was asked.
type cannedJSON struct {
	answer       string
	err          error
	calls        int
	system, user string
}

func (m *cannedJSON) JSON(_ context.Context, system, user string, out any) error {
	m.calls++
	m.system, m.user = system, user
	if m.err != nil {
		return m.err
	}
	return json.Unmarshal([]byte(m.answer), out)
}

func doneReply(text string) taskDoneReply {
	return taskDoneReply{TaskDoneReply: prompts.TaskDoneReply{Machine: "desk", Ask: "Fix the login retry", Text: text}}
}

func TestTaskDoneWithoutModel(t *testing.T) {
	merged := doneReply("Merged https://github.com/example/app/pull/7 into main.")
	merged.pr = "https://github.com/example/app/pull/7"
	for _, tc := range []struct {
		name        string
		replies     []taskDoneReply
		blocked     bool
		ok          bool
		verdict     string
		reason      string
		wantReplies int
	}{
		{"side conversation without evidence", []taskDoneReply{merged}, true, true, verdictNotDone, verdictWords[verdictNotDone], 1},
		{"no reply", nil, false, true, verdictUnsure, "没读到 Agent 的回复", 0}, // i18n-ignore: the verdict's stored reason
		{"cut off by the model service", []taskDoneReply{merged, doneReply("API Error: 529 overloaded")}, false, true, verdictNotDone, interruptedSummary, 2},
		{"left to the model", []taskDoneReply{merged}, false, false, "", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := taskDoneWithoutModel(tc.replies, tc.blocked)
			if ok != tc.ok || c.Verdict != tc.verdict || c.Reason != tc.reason || c.Replies != tc.wantReplies {
				t.Fatalf("got %+v ok=%v, want verdict %q reason %q replies %d ok=%v", c, ok, tc.verdict, tc.reason, tc.wantReplies, tc.ok)
			}
			if len(tc.replies) > 0 && !c.Signals.MergedPR {
				t.Fatalf("signals = %+v: a merged PR link is a hint whatever the verdict", c.Signals)
			}
		})
	}
}

func TestJudgeTaskDoneWithVerdicts(t *testing.T) {
	deployed := []taskDoneReply{doneReply("Deployed to production and checked the login flow: retry works.")}
	for _, tc := range []struct {
		name     string
		delivery bool
		answer   string
		verdict  string
		close    bool
		state    string // the delivery's state; "" when delivery is not judged
		gate     string
		unblock  string
	}{
		{"done, any case and spacing", false, `{"verdict":" DONE ","reason":"finished"}`, verdictDone, false, "", "", ""},
		{"a made-up verdict is unsure", false, `{"verdict":"finished","reason":"looks finished"}`, verdictUnsure, false, "", "", ""},
		{"waiting stays open", false, `{"verdict":"waiting","reason":"waiting for CI"}`, verdictWaiting, false, "", "", ""},
		{"delivery off ignores the delivery fields", false, `{"verdict":"done","delivery":"pr","gate":"merge","unblock":"user"}`, verdictDone, false, "", "", ""},
		{"done but held at a PR is undelivered", true, `{"verdict":"done","delivery":"pr","gate":"merge approval","unblock":"user"}`, verdictUndelivered, false, deliveryPR, "merge approval", unblockUser},
		{"done and live stays open", true, `{"verdict":"done","delivery":"live","gate":"none","unblock":"user"}`, verdictDone, false, deliveryLive, "", ""},
		{"not done keeps its verdict while delivery is judged", true, `{"verdict":"not_done","delivery":"pr"}`, verdictNotDone, false, deliveryPR, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &cannedJSON{answer: tc.answer}
			c, err := judgeTaskDoneWith(context.Background(), m, taskDoneInput{Owner: "Alex", Delivery: tc.delivery, Title: "Fix the login retry", Replies: deployed})
			if err != nil {
				t.Fatal(err)
			}
			if m.calls != 1 {
				t.Fatalf("model calls = %d, want 1", m.calls)
			}
			if c.Verdict != tc.verdict || c.WouldClose != tc.close {
				t.Fatalf("verdict %q close %v, want %q %v", c.Verdict, c.WouldClose, tc.verdict, tc.close)
			}
			if tc.state == "" {
				if c.Delivery != nil {
					t.Fatalf("delivery = %+v, want none while it is not judged", c.Delivery)
				}
				return
			}
			if c.Delivery == nil || c.Delivery.State != tc.state || c.Delivery.Gate != tc.gate || c.Delivery.Unblock != tc.unblock || c.Delivery.At.IsZero() {
				t.Fatalf("delivery = %+v, want state %q gate %q unblock %q with a time", c.Delivery, tc.state, tc.gate, tc.unblock)
			}
		})
	}
}

func TestJudgeTaskDoneWithDetectsOwnerActionInTheReply(t *testing.T) {
	for _, tc := range []struct {
		name, reply, verdict, reason string
	}{
		{"manual operation", "Spec 写好了。需要你本人操作部署。", verdictNeedsUser, "需要你本人操作"},
		{"Markdown emphasis inside a Chinese manual operation", "实现已完成，需要你**本人操作**批准发布。", verdictNeedsUser, "本人操作"},
		{"Markdown emphasis inside a Chinese narrative stays informational", "已检查是否需要你**本人操作**，结论是无需操作。", verdictDone, ""},
		{"owner starts the next step", "Spec 已完成，后续由你发起实现。", verdictNeedsUser, "由你发起"},
		{"offer to continue", "方案已经完成。要不要开始实现？", verdictNeedsUser, "要不要开始实现"},
		{"English offer", "The spec is ready. Would you like me to start the implementation?", verdictNeedsUser, "Would you like me to"},
		{"English proceed offer", "The spec is ready. Should I proceed with the implementation?", verdictNeedsUser, "Should I proceed"},
		{"English shall offer", "The spec is ready. Shall I proceed with the implementation?", verdictNeedsUser, "Shall I proceed"},
		{"quoted and resolved English offer", `I answered the user's question "Would you like me to proceed?"; no further action is needed.`, verdictDone, ""},
		{"quoted English proceed question", `Earlier I asked, “Should I proceed with the implementation?” You already answered yes, and the work is complete.`, verdictDone, ""},
		{"already answered English request", `The question "Do you want me to start the implementation?" was already answered; nothing remains.`, verdictDone, ""},
		{"resolved sentence does not hide a later English offer", `I answered. Would you like me to continue?`, verdictNeedsUser, "Would you like me to"},
		{"full-width boundary after an inner ASCII question mark", `I answered the user's question "Would you like me to proceed?" 。 Would you like me to deploy?`, verdictNeedsUser, ""},
		{"full-width boundary without an inner ASCII question mark", `I answered the user's question "Would you like me to proceed" 。 Would you like me to deploy?`, verdictNeedsUser, ""},
		{"single resolved quoted offer with a full-width boundary", `I answered the user's question "Would you like me to proceed" 。 No further action is needed。`, verdictDone, ""},
		{"full-width resolved quote does not hide a later requirement", `I answered the user's question "Would you like me to proceed" 。 You need to approve the release yourself。`, verdictNeedsUser, ""},
		{"English direct owner operation", "The spec is ready. You need to deploy it yourself.", verdictNeedsUser, "You need to"},
		{"English direct owner operation after a semicolon", "The spec is ready; you need to deploy it yourself.", verdictNeedsUser, "you need to"},
		{"English direct requirement after a conditional", "If you need to roll back, use the backup. Otherwise, you need to approve the release yourself.", verdictNeedsUser, "you need to"},
		{"nested if-question does not hide a direct requirement", "The report checks if the deploy succeeded: you need to approve the release.", verdictNeedsUser, ""},
		{"genuine conditional after an if-question control", "If the deploy succeeded: you need to approve the release.", verdictDone, ""},
		{"English direct requirement after otherwise without a comma", "If you need to roll back, use the backup. Otherwise you need to approve the release yourself.", verdictNeedsUser, "you need to"},
		{"English direct contraction after a conditional", "If you'll need to roll back, use the backup. Otherwise, you'll need to approve the release yourself.", verdictNeedsUser, "you'll need to"},
		{"English unconditional requirement after even if", "Even if you need to roll back, you need to notify the owner first.", verdictNeedsUser, "you need to"},
		{"repeated English marker within one conditional", "If you need to deploy, you need to notify the owner first.", verdictDone, ""},
		{"repeated English marker after a conditional colon", "If you need to deploy: you need to notify the owner first.", verdictDone, ""},
		{"conditional colon does not end an open scope", "If you need to deploy: check the build, you need to notify the owner first.", verdictDone, ""},
		{"conditional colon after an independent introduction", "The spec is ready: if you need to deploy: check the build, you need to notify the owner first.", verdictDone, ""},
		{"English direct owner operation after a colon", "Deployment is ready: you need to notify the owner first.", verdictNeedsUser, "you need to"},
		{"repeated English contraction within one conditional", "If you'll need to deploy, you'll need to notify the owner first.", verdictDone, ""},
		{"conditional scope survives a semicolon introduction", "The spec is ready; if you need to deploy, you need to notify the owner first.", verdictDone, ""},
		{"conditional scope survives a colon introduction", "The spec is ready: if you'll need to deploy, you'll need to notify the owner first.", verdictDone, ""},
		{"narrative checks if does not hide a direct owner requirement", "The report checks if the deploy succeeded: you need to approve the release.", verdictNeedsUser, ""},
		{"English direct contraction after a colon", "Next: you'll need to restart the service.", verdictNeedsUser, "you'll need to"},
		{"English direct owner operation after an introduction", "Next, you'll need to restart the service.", verdictNeedsUser, "you'll need to"},
		{"negated manual operation", "不用你本人操作，已经全部完成。", verdictDone, ""},
		{"negated owner start", "无需由你发起，已自动开始。", verdictDone, ""},
		{"negated offer", "不需要你决定要不要继续，已按原方案完成。", verdictDone, ""},
		{"negated Chinese operation does not hide a later direct one", "不用你本人操作回滚。需要你本人操作批准发布。", verdictNeedsUser, "需要你本人操作"},
		{"another negated Chinese operation does not hide a later direct one", "无需你本人操作回滚，需要你本人操作部署。", verdictNeedsUser, "需要你本人操作"},
		{"negated Chinese start does not hide a later direct one", "无需由你发起回滚；后续由你发起发布。", verdictNeedsUser, "由你发起"},
		{"negated Chinese choice does not hide a later direct question", "不需要你决定要不要回滚；要不要继续发布？", verdictNeedsUser, "要不要继续发布"},
		{"quoted and resolved Chinese choice", "我回答了用户“要不要继续？”；无需进一步操作。", verdictDone, ""},
		{"quoted and resolved Chinese direct operation", "我回答了用户“需要你本人操作批准发布。”；无需进一步操作。", verdictDone, ""},
		{"quoted and resolved Chinese direct start", "历史记录写着“由你发起发布。”；该事项已解决。", verdictDone, ""},
		{"quoted Chinese direct operation does not hide a later requirement", "历史记录写着“需要你本人操作回滚。”；需要你本人操作批准发布。", verdictNeedsUser, "需要你本人操作批准发布"},
		{"corner-quoted and resolved Chinese direct operation", "我回答了用户「需要你本人操作批准发布。」；无需进一步操作。", verdictDone, ""},
		{"corner-quoted Chinese direct operation does not hide a later requirement", "历史记录写着「需要你本人操作回滚。」；需要你本人操作批准发布。", verdictNeedsUser, "需要你本人操作批准发布"},
		{"straight-quoted and resolved Chinese choice", "我回答了用户\"要不要继续？\"；无需进一步操作。", verdictDone, ""},
		{"resolved Chinese sentence does not hide a later direct question", "我回答了。要不要继续？", verdictNeedsUser, "要不要继续"},
		{"resolved Chinese clause does not hide a later direct question", "我回答了上一个问题，要不要继续？", verdictNeedsUser, "要不要继续"},
		{"negated Chinese choice stays informational beside a resolved quote", "不需要你决定要不要回滚；我回答了用户“要不要继续？”；无需进一步操作。", verdictDone, ""},
		{"resolved Chinese explanation after a choice", "要不要继续？这个问题已回答，无需进一步操作。", verdictDone, ""},
		{"explicit no action after a Chinese choice", "要不要继续？无需进一步操作。", verdictDone, ""},
		{"resolved Chinese suffix does not hide a later direct question", "要不要继续？这个问题已回答。要不要发布？", verdictNeedsUser, "要不要发布"},
		{"unrelated resolved Chinese suffix does not resolve the current question", "要不要继续发布？已回答的是上一个问题。", verdictNeedsUser, "要不要继续发布"},
		{"unrelated no-action Chinese suffix does not resolve the current question", "要不要继续？无需进一步操作的是上一个问题。", verdictNeedsUser, "要不要继续"},
		{"another topic's no-action Chinese suffix does not resolve the current question", "要不要继续？回滚的事无需进一步操作。", verdictNeedsUser, "要不要继续"},
		{"resolved Chinese clause before a colon does not hide a direct question", "上一个问题已回答：要不要继续发布？", verdictNeedsUser, "要不要继续发布"},
		{"conditional English guidance", "If you need to roll back, run make rollback.", verdictDone, ""},
		{"conditional English guidance with a time", "If you need to deploy at 10:30, you need to notify the owner first.", verdictDone, ""},
		{"decimal version keeps a trailing conditional", "The work is complete. You need to roll back to 1.2 if the release fails.", verdictDone, ""},
		{"decimal version keeps an even-if requirement direct", "The work is complete. You need to roll back to 1.2 even if the release fails.", verdictNeedsUser, "You need to"},
		{"conditional English guidance after so with a comma", "So, if you need to deploy, you need to notify the owner first.", verdictDone, ""},
		{"conditional English guidance after so without a comma", "So if you need to deploy, you need to notify the owner first.", verdictDone, ""},
		{"negated English owner requirement", "You don't need to approve the release; it is already live.", verdictDone, ""},
		{"negated English requirement does not hide a later direct one", "You don't need to roll back. You need to approve the release yourself.", verdictNeedsUser, "You need to"},
		{"negated English contraction does not hide a later direct one", "You won't need to roll back. You'll need to approve the release yourself.", verdictNeedsUser, "You'll need"},
		{"explicit no action", "You need to do nothing.", verdictDone, ""},
		{"explicit no further action after delivery", "The work is live. You need to do nothing else.", verdictDone, ""},
		{"explicit no action does not hide an independent requirement", "You need to do nothing else. Otherwise, you need to approve the release yourself.", verdictNeedsUser, "Otherwise"},
		{"explicit no action with contraction", "You'll need to do nothing else.", verdictDone, ""},
		{"explicit no action with a full-width terminator", "You need to do nothing else。", verdictDone, ""},
		{"full-width no action does not hide an independent requirement", "You need to do nothing else。Otherwise, you need to approve the release yourself。", verdictNeedsUser, "Otherwise"},
		{"full-width no action does not hide a direct requirement", "You need to do nothing else。You need to approve the release yourself。", verdictNeedsUser, "You need to"}, // i18n-ignore: parser fixture
		{"full-width genuine directive", "You need to approve the release yourself。", verdictNeedsUser, "You need to"},                                                        // i18n-ignore: parser fixture
		{"Chinese resolved full-width control", "要不要继续？这个问题已回答。", verdictDone, ""},                                                                                            // i18n-ignore: parser fixture
		{"English narrative about a choice", "The report evaluates whether you need to split the module and concludes no.", verdictDone, ""},
		{"Chinese narrative about a choice", "评估了要不要拆分模块，结论是不拆。", verdictDone, ""},
		{"Chinese narrative about a required operation", "已检查是否需要你本人操作，结论是无需操作。", verdictDone, ""},
		{"Chinese conditional about a required operation", "如果需要你本人操作，说明尚未完成；现已自动完成。", verdictDone, ""},
		{"Chinese narrative does not hide a later direct requirement", "已检查是否需要你本人操作回滚；需要你本人操作批准发布。", verdictNeedsUser, "需要你本人操作批准发布"},
		{"Chinese direct requirement after a conditional clause", "如果部署需要审批，先记录；需要你本人操作批准发布。", verdictNeedsUser, "需要你本人操作批准发布"},
		{"Chinese direct requirement after an ASCII semicolon", "如果部署需要审批，先记录;需要你本人操作批准发布。", verdictNeedsUser, "需要你本人操作批准发布"},
		{"Chinese direct requirement after an ASCII full stop", "如果部署需要审批，先记录.需要你本人操作批准发布。", verdictNeedsUser, "需要你本人操作批准发布"},
		{"Chinese conditional with a decimal version", "如果 iOS 18.2 需要你本人操作，先记录。", verdictDone, ""},
		{"Chinese quantifier is not a conditional", "还有若干事项需要你本人操作。", verdictNeedsUser, "需要你本人操作"},
		{"Chinese sentence-initial quantifier is not a conditional", "若干事项需要你本人操作。", verdictNeedsUser, "需要你本人操作"},
		{"Chinese conditional using ruo", "若需要你本人操作，说明尚未完成；现已自动完成。", verdictDone, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &cannedJSON{answer: `{"verdict":"done","reason":"finished"}`}
			c, err := judgeTaskDoneWith(context.Background(), m, taskDoneInput{Title: "Write the spec", Replies: []taskDoneReply{doneReply(tc.reply)}})
			if err != nil {
				t.Fatal(err)
			}
			if c.Verdict != tc.verdict || c.WouldClose {
				t.Fatalf("reply %q: verdict %q close %v, want %q false", tc.reply, c.Verdict, c.WouldClose, tc.verdict)
			}
			if tc.reason != "" && !strings.Contains(c.Reason, tc.reason) {
				t.Fatalf("reason %q does not carry %q from %q", c.Reason, tc.reason, tc.reply)
			}
		})
	}
}

func TestJudgeTaskDoneWithReasonIsOneShortLine(t *testing.T) {
	long := "The retry works now.\n\n" + strings.Repeat("Checked again on the staging hub. ", 4)
	m := &cannedJSON{answer: `{"verdict":"done","reason":` + judgeJSONText(t, long) + `}`}
	c, err := judgeTaskDoneWith(context.Background(), m, taskDoneInput{Title: "Fix the login retry", Replies: []taskDoneReply{doneReply("Done.")}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(c.Reason, "\n\r") || utf8.RuneCountInString(c.Reason) != 41 || !strings.HasSuffix(c.Reason, "…") {
		t.Fatalf("reason = %q (%d runes), want one line of 40 runes and an ellipsis", c.Reason, utf8.RuneCountInString(c.Reason))
	}
}

func TestJudgeTaskDoneWithModelError(t *testing.T) {
	m := &cannedJSON{err: errors.New("no model")}
	reply := doneReply("Merged https://github.com/example/app/pull/7 into main.")
	reply.pr = "https://github.com/example/app/pull/7"
	replies := []taskDoneReply{reply}
	c, err := judgeTaskDoneWith(context.Background(), m, taskDoneInput{Title: "Fix the login retry", Replies: replies})
	if err == nil {
		t.Fatal("a model error must come back: the todo is left as it is")
	}
	if c.Verdict != "" || c.WouldClose || c.Replies != 1 || !c.Signals.MergedPR {
		t.Fatalf("check = %+v, want no verdict with the replies counted and the hints found", c)
	}
}

func TestJudgeTaskDoneModelErrorKeepsExplicitOwnerAction(t *testing.T) {
	m := &cannedJSON{err: errors.New("no model")}
	reply := "The spec is ready. Should I proceed with the implementation?"
	c, err := judgeTaskDoneWith(context.Background(), m, taskDoneInput{Title: "Write the spec", Replies: []taskDoneReply{doneReply(reply)}})
	if err == nil {
		t.Fatal("the model error is still reported")
	}
	if c.Verdict != verdictNeedsUser || !strings.Contains(c.Reason, "Should I proceed") || c.WouldClose {
		t.Fatalf("the explicit owner action was lost with the model failure: %+v", c)
	}
}

func TestJudgeTaskDoneWithAsksWhatItWasGiven(t *testing.T) {
	notes := "Retry the token refresh once before signing the user out. " + strings.Repeat("x", 4000) + " Keep the old error text."
	m := &cannedJSON{answer: `{"verdict":"done","reason":"ok"}`}
	in := taskDoneInput{Owner: "Alex", Delivery: true, Title: "Fix the login retry", Notes: notes, Criteria: []string{"PR merged", "deployed and verified"},
		Replies: []taskDoneReply{doneReply("Deployed and verified.")}}
	if _, err := judgeTaskDoneWith(context.Background(), m, in); err != nil {
		t.Fatal(err)
	}
	if want := prompts.RenderTaskDone(prompts.TaskDone{Owner: "Alex", Delivery: true}); m.system != want {
		t.Fatalf("system prompt differs from the task_done template for this owner and delivery")
	}
	for _, want := range []string{"Fix the login retry", "Retry the token refresh once", "Keep the old error text.", "\n…\n", "PR merged", "deployed and verified", "Deployed and verified."} {
		if !strings.Contains(m.user, want) {
			t.Fatalf("user text lacks %q", want)
		}
	}
	if strings.Contains(m.user, strings.Repeat("x", 4000)) {
		t.Fatal("long notes go clipped in the middle")
	}
}

func judgeJSONText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
