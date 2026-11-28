package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestDeliveryMark(t *testing.T) {
	for _, tc := range []struct {
		text string
		want TodoDelivery
		ok   bool
	}{
		{"改好了。\n交付：停在 PR；卡点：CI 排队；谁来解：其他（CI）", TodoDelivery{State: deliveryPR, Gate: "CI 排队", Unblock: unblockOther}, true},
		{"交付：已合未上线；卡点：等你批准部署；谁来解：用户", TodoDelivery{State: deliveryMerged, Gate: "等你批准部署", Unblock: unblockUser}, true},
		{"交付: 已上线并实测; 卡点: 无; 谁来解: 无", TodoDelivery{State: deliveryLive}, true},
		{"交付：默认关待启用；卡点：hub.json 里没开；谁来解：Agent", TodoDelivery{State: deliveryOff, Gate: "hub.json 里没开", Unblock: unblockAgent}, true},
		{"交付：不涉及上线；卡点：无；谁来解：无", TodoDelivery{State: deliveryNone}, true},
		{"交付标准说完了", TodoDelivery{}, false},
	} {
		got, ok := deliveryMark(tc.text)
		if ok != tc.ok || got != tc.want {
			t.Errorf("deliveryMark(%q) = %+v %v, want %+v %v", tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

func TestJudgedDelivery(t *testing.T) {
	reply := func(text string) []taskDoneReply { return []taskDoneReply{doneReply(text)} }
	pr := "开了 https://github.com/o/r/pull/12"
	for _, tc := range []struct {
		name                  string
		state, gate, unblock  string
		replies               []taskDoneReply
		wantState, wantUnhold string
	}{
		{"the judge's hold", "pr", "等用户批准合并", "user", reply(pr), deliveryPR, unblockUser},
		{"live with a deploy", "live", "", "", reply(pr + "，已部署，实测通过"), deliveryLive, ""},
		{"live with only a PR is held", "live", "", "", reply(pr), deliveryPR, ""},
		{"merged with a TestFlight build is out", "", "", "", reply(pr + " 已合并，TestFlight 构建号 202611041200"), deliveryNone, ""},
		{"merged and called live is held", "live", "", "", reply(pr + "，已合并"), deliveryMerged, ""},
		{"the agent's own line says it is out", "live", "", "", reply(pr + "\n交付：已上线并实测；卡点：无；谁来解：无"), deliveryLive, ""},
		{"nothing to ship stands", "none", "", "", reply("参考了 " + pr + " 的做法，结论如下"), deliveryNone, ""},
		{"no answer: the agent's line", "", "", "", reply("交付：停在 PR；卡点：等对方 review；谁来解：其他"), deliveryPR, unblockOther},
		{"no answer and no line: the PR holds it", "", "", "", reply(pr), deliveryPR, ""},
		{"no answer, nothing named", "", "", "", reply("查清楚了"), deliveryNone, ""},
	} {
		got := judgedDelivery(tc.state, tc.gate, tc.unblock, tc.replies)
		if got.State != tc.wantState || got.Unblock != tc.wantUnhold {
			t.Errorf("%s: %+v, want %s / %s", tc.name, got, tc.wantState, tc.wantUnhold)
		}
		if deliveryClosable(got.State) && (got.Gate != "" || got.Unblock != "") {
			t.Errorf("%s: a closable delivery keeps a gate: %+v", tc.name, got)
		}
	}
}

// deliveryOn turns assistant.done_needs_delivery on (it is off by default).
func deliveryOn(t *testing.T, s *Service) {
	t.Helper()
	on := true
	if _, err := s.updateSettings(context.Background(), func(v *Settings) error { v.DoneNeedsDelivery = &on; return nil }); err != nil {
		t.Fatal(err)
	}
}

// With done_needs_delivery on, a task done as asked but stopped at a PR stays open with where it is held. Live work is
// delivered for review too: neither it nor the default (delivery off) closes the todo without the owner (#5732).
func TestAutoFinishHoldsUndeliveredWork(t *testing.T) {
	s, f := newTaskService(t)
	deliveryOn(t, s)
	now := time.Now()
	putTodo(s, Todo{ID: "todo_d", Title: "修登录", Origin: "conv1", CreatedAt: now.Add(-time.Hour),
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s9", LinkedAt: now.Add(-time.Minute)}}})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s9", Status: proto.StatusDone})
	artifactReportProof(f, todoNow(s, "todo_d"), "修好了，https://github.com/o/r/pull/7\n交付：停在 PR；卡点：等用户批准合并；谁来解：用户", "https://github.com/o/r/pull/7")
	f.judge = `{"verdict":"done","reason":"开了 PR","delivery":"pr","gate":"等用户批准合并","unblock":"user"}`
	s.autoFinishTask(todoNow(s, "todo_d"), now, true)
	got := todoNow(s, "todo_d")
	if got.DoneAt != nil || !got.Delivery.held() || got.Delivery.Gate != "等用户批准合并" || got.Stage != "review" {
		t.Fatalf("held work: %+v %+v", got, got.Delivery)
	}
	if line := deliverySummary(got); line != "停在 PR，卡在「等用户批准合并」，要用户点头" {
		t.Fatalf("summary = %q", line)
	}
	a := s.snapshotApprovals(context.Background(), "")
	if len(a) != 1 || a[0].ID != "todo_d" || a[0].Stage != "停在 PR" {
		t.Fatalf("approvals = %+v", a)
	}
	task := snapshotTask(got, "unread", nil, now)
	task.Delivery = deliverySummary(got)
	golden(t, "assistant_snapshot.delivery", prompts.RenderSnapshot(prompts.Snapshot{Now: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		Zone: "UTC", Tasks: []prompts.SnapshotTask{task}, Approvals: a}))

	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant",
		Text: "已合并并部署，接口实测返回 200\n交付：已上线并实测；卡点：无；谁来解：无"})
	f.judge = `{"verdict":"done","reason":"已上线","delivery":"live"}`
	s.autoFinishTask(todoNow(s, "todo_d"), time.Now(), true)
	if got := todoNow(s, "todo_d"); got.DoneAt != nil || got.Delivery.State != deliveryLive || got.Check == nil || got.Check.Verdict != verdictDone {
		t.Fatalf("live work not delivered for review: %+v", got)
	}
	if a := s.snapshotApprovals(context.Background(), ""); len(a) != 0 {
		t.Fatalf("a closed task still waits for a nod: %+v", a)
	}

	s2, f2 := newTaskService(t)
	putTodo(s2, Todo{ID: "todo_o", Title: "修", Origin: "conv1", CreatedAt: now.Add(-time.Hour),
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s9"}}})
	f2.setSessions("box", proto.Session{MachineID: "box", SessionID: "s9", Status: proto.StatusDone})
	f2.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "PR https://github.com/o/r/pull/7 已合并"})
	f2.judge = `{"verdict":"done","reason":"已合并"}`
	s2.autoFinishTask(todoNow(s2, "todo_o"), now, true)
	if got := todoNow(s2, "todo_o"); got.DoneAt != nil || got.Delivery != nil || got.Check == nil || got.Check.Verdict != verdictDone {
		t.Fatalf("by default (off): %+v", got)
	}
}

// With done_needs_delivery on, a task that changes things is handed the delivery standard after its prompt; a
// read-only one is not, nor any while it is off.
func TestTaskPromptCarriesTheDeliveryStandard(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	if _, _, err := s.startTaskAgent(ctx, &PlanTask{Title: "修", Prompt: "修好登录", Machine: "box", Cwd: "/code/app"}, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.starts[0]["prompt"].(string); !strings.HasPrefix(p, "修好登录") || !strings.Contains(p, "file: <这台电脑上的绝对路径>") || strings.Contains(p, "交付：") {
		t.Fatalf("off by default, yet the prompt is %q", p)
	}
	f.starts = nil
	deliveryOn(t, s)
	if _, _, err := s.startTaskAgent(ctx, &PlanTask{Title: "修", Prompt: "修好登录", Machine: "box", Cwd: "/code/app"}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.startTaskAgent(ctx, &PlanTask{Title: "查", Prompt: "查一下", Machine: "box", Cwd: "/code/app", Readonly: true}, false); err != nil {
		t.Fatal(err)
	}
	if len(f.starts) != 2 {
		t.Fatalf("%d starts", len(f.starts))
	}
	edit, _ := f.starts[0]["prompt"].(string)
	read, _ := f.starts[1]["prompt"].(string)
	if !strings.HasPrefix(edit, "修好登录") || !strings.Contains(edit, "交付：<已上线并实测") || !strings.HasPrefix(read, "查一下\n\n本任务只读：不改项目文件、不提交、不对外写入") {
		t.Fatalf("prompts:\n%s\n---\n%s", edit, read)
	}
}

// A supervised task done as asked but held short of live does not wait for review: its supervisor is told where it
// is held, to chase or bring to the user.
func TestSupervisedUndeliveredTellsWhereItIsHeld(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	deliveryOn(t, s)
	f.judge = `{"verdict":"done","reason":"开了 PR","delivery":"pr","gate":"等用户批准合并","unblock":"user"}`
	artifactNodeProof(f, "https://github.com/o/r/pull/901", "contributor", "contributor", "fix/work", "fix/work", "abc", "abc")
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，https://github.com/o/r/pull/901 等合并\n交付：停在 PR；卡点：等用户批准合并；谁来解：用户", 0)
	waitFor(t, "the message", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "做完了但还没上线：开了 PR（停在 PR，卡在「等用户批准合并」）") {
		t.Fatalf("message:\n%s", n)
	}
	now := todoNow(s, todo.ID)
	if now.DoneAt != nil || now.Supervision.State == reviewPending || !now.Delivery.held() {
		t.Fatalf("todo: %+v %+v", now.Supervision, now.Delivery)
	}
}

func TestDefaultOffVerificationRequiresExplicitReport(t *testing.T) {
	for _, tc := range []struct {
		name, reply, state string
	}{
		{"deployed without a switch", "### Persona 工作报告\n\n- Status: done\n- Merged: yes\n- Deployed: yes\n- Did: Released without a feature switch; effective immediately.\n- Delivery: live\n- Verified: Production returned names for 5/5 records.\n\n---", deliveryLive},
		{"legacy live report", "交付：已上线并实测；卡点：无；谁来解：无", deliveryLive},
		{"deployed closeout", "<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\n</closeout>", deliveryLive},
		{"explicit off", "<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\ndelivery: off\nmissing: Feature disabled by default; enable it in settings.\n</closeout>", deliveryOff},
		{"legacy explicit off", "交付：默认关待启用；卡点：功能默认关闭，需启用；谁来解：用户", deliveryOff},
		{"mislabeled deployed closeout", "<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\ndelivery: off\nmissing: Offline data backfill is pending.\n</closeout>", deliveryLive},
		{"original report shape", "<closeout status=\"done\">\ndelivery: off\nverified: Online HTTP 200; 5/5 names present.\nmissing: Offline data backfill needs the data team.\n</closeout>", deliveryMerged},
		{"unknown deployment", "The change was deployed; verification is pending.", deliveryMerged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delivery := judgedDelivery(deliveryOff, "Enable the feature", unblockUser, []taskDoneReply{{TaskDoneReply: prompts.TaskDoneReply{Text: tc.reply}}})
			if delivery.State != tc.state {
				t.Fatalf("state = %q, want %q", delivery.State, tc.state)
			}
			title := verdictText(i18n.ZhHans, &acceptAsk{}, &delivery)
			if strings.Contains(title, "默认关着") != (tc.state == deliveryOff) {
				t.Fatalf("unexpected default-off title: %q", title)
			}
			if tc.state == deliveryLive && title != "✅ 做完了：已部署，按下面验收" {
				t.Fatalf("live title = %q", title)
			}
		})
	}
}

func TestDefaultOffJudgeChecksUseNormalizedDelivery(t *testing.T) {
	model := &cannedJSON{answer: `{"verdict":"done","delivery":"off","gate":"Data backfill pending","unblock":"other"}`}
	check, err := judgeTaskDoneWith(context.Background(), model, taskDoneInput{
		Title: "Release the name fix", Delivery: true, Checks: true,
		Replies: []taskDoneReply{{TaskDoneReply: prompts.TaskDoneReply{Text: "<closeout status=\"done\" deployed=\"yes\">\ndelivery: live\nverified: HTTP 200, 5/5 records have names.\n</closeout>"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if check.Delivery == nil || check.Delivery.State != deliveryLive || check.AutoCheck == nil || check.AutoCheck.Delivery != deliveryMerged {
		t.Fatalf("unsupported default-off or automatic live check: %+v / %+v", check.Delivery, check.AutoCheck)
	}
}

func TestHistoricalDefaultOffWithoutFeatureEvidenceIsNeutral(t *testing.T) {
	delivery := &TodoDelivery{State: deliveryOff, Gate: "Offline data backfill is pending"}
	title := verdictText(i18n.ZhHans, &acceptAsk{}, delivery)
	if strings.Contains(title, "默认关着") || strings.Contains(title, "已部署") {
		t.Fatalf("unsupported deployment or feature claim: %q", title)
	}
}

func TestDefaultOffGateDoesNotTreatNegationAsEvidence(t *testing.T) {
	for _, gate := range []string{"The feature is not off by default", "The feature is not disabled by default", "the new flag isn't off by default", "the new flag aren't off by default", "the new flag wasn't off by default", "the new flag won't off by default", "the new flag doesn't off by default", "the new flag hasn't off by default", "并非默认关闭", "没有默认关闭", "既定でオフではない"} {
		if hasDefaultOffGate(gate) {
			t.Fatalf("negation accepted: %q", gate)
		}
		title := verdictText(i18n.ZhHans, &acceptAsk{}, &TodoDelivery{State: deliveryOff, Gate: gate})
		if strings.Contains(title, "默认关着") {
			t.Fatalf("negated gate in title: %q", title)
		}
	}
}

func TestDefaultOffGateSeparatorsAndWordBoundaries(t *testing.T) {
	type gateCase struct {
		gate string
		want bool
	}
	cases := []gateCase{
		{"Feature is default.off", true},
		{"Feature is default-off", true},
		{"Feature is default off", true},
		{"Feature is default_off", true},
		{"The default office location needs review", false},
		{"The default.offset needs review", false},
		{"The nodefault.off value needs review", false},
		{"The new flag won't be off-by-default", false},
		{"The new flag hasn't been disabled-by-default", false},
		{"The new flag doesn't default-off", false},
		{"The new flag isn’t off by default", false},
	}
	for _, first := range []string{"-", ".", "_", " "} {
		phrases := []string{"default" + first + "off"}
		for _, second := range []string{"-", ".", "_", " "} {
			phrases = append(phrases, "off"+first+"by"+second+"default", "disabled"+first+"by"+second+"default")
		}
		for _, phrase := range phrases {
			cases = append(cases,
				gateCase{"Feature is " + phrase + "; enable it in settings.", true},
				gateCase{strings.ToUpper(phrase), true},
				gateCase{"prefix" + phrase, false},
				gateCase{phrase + "suffix", false},
			)
			for _, negation := range []string{
				"not", "never", "no longer", "cannot",
				"isn't", "aren't", "wasn't", "weren't", "don't", "doesn't", "didn't",
				"won't", "wouldn't", "can't", "couldn't", "shouldn't", "mustn't",
				"needn't", "shan't", "hasn't", "haven't", "hadn't", "mightn't", "oughtn't",
			} {
				cases = append(cases, gateCase{"The new flag " + negation + " " + phrase, false})
				if strings.Contains(negation, "'") {
					cases = append(cases,
						gateCase{"The new flag " + strings.ReplaceAll(negation, "'", "’") + " " + phrase, false},
						gateCase{"The new flag " + strings.ReplaceAll(negation, "'", "") + " " + phrase, false},
					)
				}
			}
		}
	}
	for _, phrase := range []string{"offset by default", "office by default", "disabled by defaulted"} {
		cases = append(cases, gateCase{phrase, false})
	}
	for _, test := range cases {
		t.Run(test.gate, func(t *testing.T) {
			if got := hasDefaultOffGate(test.gate); got != test.want {
				t.Errorf("hasDefaultOffGate(%q) = %v, want %v", test.gate, got, test.want)
			}
			title := verdictText(i18n.ZhHans, &acceptAsk{}, &TodoDelivery{State: deliveryOff, Gate: test.gate})
			if got := strings.Contains(title, "默认关着"); got != test.want {
				t.Errorf("default-off claim in title %q = %v, want %v", title, got, test.want)
			}
			reply := "<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\ndelivery: off\nmissing: " + test.gate + "\n</closeout>"
			delivery := judgedDelivery(deliveryOff, test.gate, unblockUser, []taskDoneReply{{TaskDoneReply: prompts.TaskDoneReply{Text: reply}}})
			wantState := deliveryLive
			if test.want {
				wantState = deliveryOff
			}
			if delivery.State != wantState {
				t.Errorf("judgedDelivery(%q) = %q, want %q", test.gate, delivery.State, wantState)
			}
		})
	}
}
