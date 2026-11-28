package assistant

import (
	"strings"
	"testing"
)

func TestTaskAssignmentAndFrameworkInstructions(t *testing.T) {
	s, f, _ := taskHandoffTestFixture(t, "claude")
	f.mu.Lock()
	f.devices = []Device{{ID: "server", Online: true, Reachable: true}, {ID: "box", Online: true, Reachable: true, TaskInstructions: true}}
	f.mu.Unlock()
	closeoutOn(t, s)
	prompt := taskHandoffTestStart(t, s, f, "task_plan")
	instructions, _ := startsOf(f)[0]["task_instructions"].(string)
	if !strings.HasPrefix(prompt, "Inspect the regression") || strings.Contains(prompt, "<closeout") || instructions == "" || strings.Count(instructions, "<closeout status=") != 1 {
		t.Fatalf("assignment=%q instructions=%q", prompt, instructions)
	}
	t.Logf("assignment_bytes=%d framework_bytes=%d", len(prompt), len(instructions))
}

func TestStructuredDeliveryKeepsDispositionGateAndOwner(t *testing.T) {
	for _, state := range []string{deliveryLive, deliveryPR, deliveryMerged, deliveryOff, deliveryNone} {
		gate := "deployment approval"
		if state == deliveryOff {
			gate = "feature disabled by default; activation approval"
		}
		text := `<closeout status="blocked" merged="no" deployed="no">` + "\ndelivery: " + state + "\nmissing: " + gate + "\nunblock: user\n</closeout>"
		d, ok := deliveryMark(text)
		if !ok || d.State != state || !deliveryClosable(state) && (d.Gate != gate || d.Unblock != unblockUser) {
			t.Errorf("%s: %+v, %v", state, d, ok)
		}
	}
}

func TestTaskAssignmentHasProjectOnceAndKeepsCriteria(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "separate"}[modern], func(t *testing.T) {
			s, f, _ := outcomeEnv(t, nil, outcomeReply)
			f.mu.Lock()
			f.devices = []Device{{ID: "server", Online: true, Reachable: true}, {ID: "box", Online: true, Reachable: true, TaskInstructions: modern}}
			f.mu.Unlock()
			closeoutOn(t, s)
			task := PlanTask{Title: "Summarize checks", Prompt: "Summarize the failed checks in the sample repository.", Context: "New fact: the check failed after the last update.", Machine: "box", Cwd: "/code/weather", Readonly: true, DoneWhen: []string{"Name each failed check and its cause."}, Attachments: []string{"/x/conv1/images/1.png"}, AttachFrom: "box"}
			if _, _, err := s.startTaskAgent(t.Context(), &task, false); err != nil {
				t.Fatal(err)
			}
			start := startsOf(f)[0]
			prompt, _ := start["prompt"].(string)
			rules, _ := start["task_instructions"].(string)
			attachments, _ := start["task_attachments"].(string)
			if !strings.HasPrefix(prompt, task.Prompt) || strings.Count(prompt, "<persona-context") != 1 || strings.Count(prompt, task.Context) != 1 || !strings.Contains(prompt, task.DoneWhen[0]) {
				t.Fatalf("assignment=%s", prompt)
			}
			if modern && (strings.Contains(prompt, "<closeout") || rules == "" || !strings.HasSuffix(attachments, "</attached_images>")) {
				t.Fatalf("rules=%s attachments=%s", rules, attachments)
			}
			if !modern && (!strings.Contains(prompt, "<closeout") || !strings.HasSuffix(prompt, "</attached_images>")) {
				t.Fatal(prompt)
			}
			t.Logf("modern=%v assignment_bytes=%d framework_bytes=%d attachment_bytes=%d", modern, len(prompt), len(rules), len(attachments))
		})
	}
}
