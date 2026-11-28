package screen

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const askScreen = ` Ran 1 shell command
────────────────────────────────────────────────
 ☐ 发布方式

 │第一次构建失败：镜像里缺一个依赖，补上以后
 │编译通过，修复也已推送（a1b2c3d）。

❯ 1. 取消这次发布 (Recommended)
     用修复后的 commit 重新发一次，
     旧的那次先撤掉。
  2. 先不取消
     我停在这里，什么都不动。
  3. Type something.
────────────────────────────────────────────────
  4. Chat about this

 Enter to select · ↑/↓ to navigate · Esc to cancel`

func TestParseAskUserQuestion(t *testing.T) {
	d := ParseDialog(askScreen)
	if d == nil {
		t.Fatal("no dialog")
	}
	if d.Header != "发布方式" || d.Question != "第一次构建失败：镜像里缺一个依赖，补上以后编译通过，修复也已推送（a1b2c3d）。" {
		t.Fatalf("header %q question %q", d.Header, d.Question)
	}
	keys := []string{}
	for _, o := range d.Options {
		keys = append(keys, o.Key)
	}
	if !slices.Equal(keys, []string{"1", "2", "3", "4"}) || d.Options[0].Label != "取消这次发布 (Recommended)" {
		t.Fatalf("options %+v", d.Options)
	}
	if d.Options[0].Detail != "用修复后的 commit 重新发一次，旧的那次先撤掉。" || d.Options[1].Detail != "我停在这里，什么都不动。" {
		t.Fatalf("details %q / %q", d.Options[0].Detail, d.Options[1].Detail)
	}
	if d.Options[2].Kind != KindTypeSomething || d.Options[2].Detail != "" || d.Options[3].Kind != KindChat {
		t.Fatalf("kinds %+v", d.Options)
	}
	if !slices.Equal(d.Options[1].Keys, []string{"2"}) || len(d.Rev) != 12 {
		t.Fatalf("keys %v rev %q", d.Options[1].Keys, d.Rev)
	}
}

const permissionScreen = `────────────────────────────────────────────────
 Bash command

   git push origin main
   Push the release commit

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again for git push commands in /code/app
   3. No, and tell Claude what to do differently (esc)`

func TestParsePermissionPrompt(t *testing.T) {
	d := ParseDialog(permissionScreen)
	if d == nil || d.Header != "" || d.Question != "Do you want to proceed?" || !strings.Contains(d.Detail, "git push origin main") {
		t.Fatalf("%+v", d)
	}
	if len(d.Options) != 3 || d.Options[0].Detail != "" {
		t.Fatalf("%+v", d.Options)
	}
}

// The revision follows what is asked, not where the cursor is or what moves around it.
func TestRevision(t *testing.T) {
	a := ParseDialog(permissionScreen)
	moved := strings.Replace(strings.Replace(permissionScreen, " ❯ 1. Yes", "   1. Yes", 1), "   2. Yes,", " ❯ 2. Yes,", 1)
	if b := ParseDialog("✻ Working… (3s)\n" + moved); b.Rev != a.Rev {
		t.Fatal("cursor or spinner changed the revision")
	}
	other := strings.Replace(permissionScreen, "git push origin main", "git push --force origin main", 1)
	if ParseDialog(other).Rev == a.Rev {
		t.Fatal("another command, same revision")
	}
}

func TestParseFixtures(t *testing.T) {
	trust := ParseDialog(Plain(fixture(t, "claude-2.1.312-trust-162.txt")))
	if trust == nil || len(trust.Options) != 2 || trust.Options[0].Label != "No, exit" || trust.Options[1].Label != "Yes, I trust this folder" {
		t.Fatalf("trust %+v", trust)
	}
	if !strings.HasPrefix(trust.Question, "Quick safety check: Is this a project you created or one you trust?") {
		t.Fatalf("trust question %q", trust.Question)
	}
	if !slices.Equal(trust.Options[0].Keys, []string{"enter"}) || !slices.Equal(trust.Options[1].Keys, []string{"down", "enter"}) {
		t.Fatalf("trust keys %v %v", trust.Options[0].Keys, trust.Options[1].Keys)
	}
	picker := ParseDialog(fixture(t, "claude-2.1.312-model-picker-162.txt"))
	if picker == nil || len(picker.Options) != 6 || !strings.HasPrefix(picker.Options[3].Label, "Sonnet") {
		t.Fatalf("picker %+v", picker)
	}
	rewind := ParseDialog(fixture(t, "claude-rewind-2.1.304-confirm-apple-restore-120.txt"))
	if rewind == nil || len(rewind.Options) != 4 || rewind.Options[3].Label != "Never mind" {
		t.Fatalf("rewind %+v", rewind)
	}
	for _, name := range []string{"claude-2.1.312-empty-162.txt", "claude-2.1.312-typed-162.txt"} {
		if d := ParseDialog(fixture(t, name)); d != nil {
			t.Fatalf("%s: %+v", name, d)
		}
	}
	if ParseDialog("> hello\n  some output") != nil {
		t.Fatal("no options, no dialog")
	}
}

// Claude Code 2.1.316's AskUserQuestion with multiSelect, and with several questions (recorded in herdr at 100 and 56
// columns, the text replaced by placeholders): check boxes are state, not label; the free-text field keeps its
// label with the typed text beside it; the Submit / Next row, the tab bar and the review tab are read; and none of
// what changes while answering (boxes, cursor, typed text, answered tabs) changes the revision.
func TestParseMultiSelect(t *testing.T) {
	parse := func(name string) *Dialog {
		t.Helper()
		d := ParseDialog(fixture(t, "claude-2.1.316-"+name+".txt"))
		if d == nil {
			t.Fatalf("%s: no dialog", name)
		}
		return d
	}
	checked := func(d *Dialog) []string {
		var keys []string
		for _, o := range d.Options {
			if o.Checked {
				keys = append(keys, o.Key)
			}
		}
		return keys
	}
	fresh := parse("multiselect-100")
	if !fresh.Multi || fresh.Submit != "Submit" || fresh.Header != "示例修复" || fresh.Question != "示例清单里哪些现在就做？" || fresh.Steps != nil {
		t.Fatalf("fresh %+v", fresh)
	}
	if len(fresh.Options) != 6 || fresh.Options[0].Label != "甲 低风险（推荐）" || fresh.Options[0].Detail != "示例说明一：只改服务端的推送频率，界面不变。" {
		t.Fatalf("options %+v", fresh.Options)
	}
	if o := fresh.Options[4]; o.Kind != KindTypeSomething || o.Label != "Type something" || o.Text != "" || o.Checked {
		t.Fatalf("free text %+v", o)
	}
	if o := fresh.Options[5]; o.Kind != KindChat || fresh.Cursor != "1" || len(checked(fresh)) != 0 {
		t.Fatalf("chat %+v, cursor %q, checked %v", o, fresh.Cursor, checked(fresh))
	}

	typed := parse("multiselect-typed-100")
	if !slices.Equal(checked(typed), []string{"1", "3", "5"}) || typed.Cursor != "5" {
		t.Fatalf("typed: checked %v cursor %q", checked(typed), typed.Cursor)
	}
	if o := typed.Options[4]; o.Kind != KindTypeSomething || o.Label != "Type something" || o.Text != "示例自由回答" {
		t.Fatalf("typed field %+v", o)
	}
	onSubmit := parse("multiselect-on-submit-100")
	if onSubmit.Cursor != CursorSubmit || !slices.Equal(checked(onSubmit), []string{"1", "3"}) {
		t.Fatalf("on submit: cursor %q checked %v", onSubmit.Cursor, checked(onSubmit))
	}
	if typed.Rev != fresh.Rev || onSubmit.Rev != fresh.Rev {
		t.Fatalf("answering moved the revision: %s %s %s", fresh.Rev, typed.Rev, onSubmit.Rev)
	}
	narrow := parse("multiselect-checked-56")
	if !narrow.Multi || !slices.Equal(checked(narrow), []string{"1", "3"}) || narrow.Cursor != "1" || narrow.Options[4].Kind != KindTypeSomething {
		t.Fatalf("56 columns %+v", narrow)
	}

	review := parse("multiselect-review-100")
	if !review.Review || review.Incomplete || review.Question != "Ready to submit your answers?" || len(review.Options) != 2 ||
		review.Options[0].Label != "Submit answers" || !strings.Contains(review.Detail, "→ 甲 低风险（推荐）, 丙 中等风险, 新的回答") {
		t.Fatalf("review %+v", review)
	}

	single := parse("questions-single-100")
	want := []Step{{Label: "环境", Done: false}, {Label: "平台", Done: false}, {Label: "通知", Done: false}}
	if single.Multi || single.Header != "" || !slices.Equal(single.Steps, want) || single.Question != "示例一：先发哪个环境？" {
		t.Fatalf("questions, single %+v", single)
	}
	if o := single.Options[2]; o.Kind != KindTypeSomething || len(single.Options) != 4 {
		t.Fatalf("questions, single: %+v", single.Options)
	}
	next := parse("questions-multi-next-100")
	if !next.Multi || next.Submit != "Next" || !slices.Equal(next.Steps, []Step{{Label: "环境", Done: true}, {Label: "平台", Done: true}, {Label: "通知", Done: false}}) ||
		!slices.Equal(checked(next), []string{"1", "3"}) || next.Cursor != "1" {
		t.Fatalf("questions, next %+v", next)
	}
	last := parse("questions-multi-last-100")
	if last.Submit != "Submit" || last.Cursor != CursorSubmit || !slices.Equal(checked(last), []string{"2"}) {
		t.Fatalf("questions, last %+v", last)
	}
	picked := parse("questions-picked-100")
	if picked.Options[0].Label != "预览" || !picked.Options[0].Checked || picked.Rev != single.Rev {
		t.Fatalf("questions, picked %+v (rev %s, unanswered %s)", picked.Options[0], picked.Rev, single.Rev)
	}
	incomplete := parse("questions-review-incomplete-100")
	if !incomplete.Review || !incomplete.Incomplete || !strings.Contains(incomplete.Detail, "● 示例二：哪些平台要一起发？") {
		t.Fatalf("questions, incomplete review %+v", incomplete)
	}
	if done := parse("questions-review-100"); !done.Review || done.Incomplete {
		t.Fatalf("questions, review %+v", done)
	}

	one := parse("single-100")
	oneTyped := parse("single-typed-100")
	if one.Header != "环境" || one.Multi || oneTyped.Options[2].Text != "示例文字" || oneTyped.Options[2].Label != "Type something." || oneTyped.Rev != one.Rev {
		t.Fatalf("single %+v / typed %+v", one, oneTyped.Options[2])
	}
}

// Claude Code 2.1.316's permission prompt for a dangerous rm in auto mode (a real capture; the command is a
// placeholder): a boxed warning, then a countdown to blocking the request by itself. The countdown moved the
// revision every second, so every answer was refused as a changed prompt (#564). Now the warning and the deadline
// are told apart from the question and stay out of the revision.
func TestParseDangerousCommandPrompt(t *testing.T) {
	d := ParseDialog(fixture(t, "claude-2.1.316-danger-rm-120.txt"))
	later := ParseDialog(fixture(t, "claude-2.1.316-danger-rm-later-120.txt"))
	if d == nil || later == nil {
		t.Fatal("no dialog")
	}
	if d.Question != "Do you want to proceed?" ||
		d.Detail != "Bash command\nDelete all files directly inside the lab scratch directory\nrm -f $LAB_SCRATCH/*" ||
		!strings.HasPrefix(d.Warning, "Dangerous rm operation on possibly-empty variable path: $LAB_SCRATCH/*") ||
		d.Deadline != "Claude Code will automatically block this request in 1:46, to avoid holding up an unattended session" {
		t.Fatalf("dialog: question %q\ndetail %q\nwarning %q\ndeadline %q", d.Question, d.Detail, d.Warning, d.Deadline)
	}
	if len(d.Options) != 2 || d.Options[0].Key != "1" || d.Options[0].Label != "Yes" || d.Options[1].Key != "2" || d.Options[1].Label != "No" {
		t.Fatalf("options %+v", d.Options)
	}
	if later.Deadline == d.Deadline || later.Rev != d.Rev {
		t.Fatalf("three seconds later: deadline %q, rev %s → %s", later.Deadline, d.Rev, later.Rev)
	}
	// Blocked when the countdown ran out: no dialog left, the card goes.
	if after := ParseDialog(fixture(t, "claude-2.1.316-danger-rm-blocked-120.txt")); after != nil {
		t.Fatalf("a dialog after the request was blocked: %+v", after)
	}
	// The same prompt without the clock (another mode): the same card.
	noClock := strings.Replace(fixture(t, "claude-2.1.316-danger-rm-120.txt"),
		" ⚠ Claude Code will automatically block this request in 1:46, to avoid holding up an unattended session\n", "", 1)
	if nd := ParseDialog(noClock); nd == nil || nd.Rev != d.Rev || nd.Deadline != "" {
		t.Fatalf("without the countdown: %+v", nd)
	}
	// A different command is a different card.
	other := strings.ReplaceAll(fixture(t, "claude-2.1.316-danger-rm-120.txt"), "$LAB_SCRATCH/*", "$OTHER/*")
	if od := ParseDialog(other); od == nil || od.Rev == d.Rev {
		t.Fatalf("another command, same rev: %+v", od)
	}
}

const borderlessBashScreen = `────────────────────────────────────────────────
 Bash command
   │ target=$WORKSPACE/example.tmp
   │ rm -- "$target"
 Clean the temporary
 workspace files

 │ Dangerous rm operation on statically-unresolvable target: $target
 │ Review the target before proceeding.

 Do you want to proceed?
 ❯ 1. Yes
   2. No`

func TestParseBorderlessCommandPrompt(t *testing.T) {
	const bashCommand = "target=$WORKSPACE/example.tmp\nrm -- \"$target\""
	const warning = "Dangerous rm operation on statically-unresolvable target: $target Review the target before proceeding."
	const powershellScreen = `────────────────────────────────────────────────
 PowerShell command

   │ $target = './example'
   │ Get-ChildItem -Path $target
 List the example files
 Do you want to proceed?
 ❯ 1. Yes
   2. No`
	withoutDescription := strings.Replace(borderlessBashScreen, " Clean the temporary\n workspace files\n", "", 1)
	for _, tc := range []struct {
		name, text, command, description, warning, risk string
	}{
		{"Bash", borderlessBashScreen, bashCommand, "Clean the temporary workspace files", warning, RiskDelete},
		{"Bash with an ASCII warning and no description", strings.ReplaceAll(withoutDescription, "\n │ ", "\n | "), bashCommand, "", warning, RiskDelete},
		{"PowerShell without a warning", powershellScreen, "$target = './example'\nGet-ChildItem -Path $target", "List the example files", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ParseDialog(tc.text)
			if d == nil {
				t.Fatal("no dialog")
			}
			if d.Command != tc.command || d.Description != tc.description || d.Warning != tc.warning || d.Risk != tc.risk {
				t.Errorf("command %q, description %q, warning %q, risk %q", d.Command, d.Description, d.Warning, d.Risk)
			}
			if d.Question != "Do you want to proceed?" || len(d.Options) != 2 || d.Options[0].Label != "Yes" || d.Options[1].Label != "No" {
				t.Errorf("question %q, options %+v", d.Question, d.Options)
			}
			if !strings.Contains(d.Detail, tc.command) {
				t.Errorf("detail omits the command: %q", d.Detail)
			}
		})
	}
}

func TestBorderlessCommandRevision(t *testing.T) {
	first := ParseDialog(borderlessBashScreen)
	changedCommand := ParseDialog(strings.Replace(borderlessBashScreen, "example.tmp", "another.tmp", 1))
	changedWarning := ParseDialog(strings.Replace(borderlessBashScreen, "Review the target", "Check the target", 1))
	if first == nil || changedCommand == nil || changedWarning == nil {
		t.Fatal("no dialog")
	}
	if first.Rev == changedCommand.Rev {
		t.Errorf("different commands share revision %q", first.Rev)
	}
	if first.Rev != changedWarning.Rev {
		t.Errorf("changing the warning changed the revision from %q to %q", first.Rev, changedWarning.Rev)
	}
}

// Claude Code 2.1.322's safety check on a command its parser cannot read (real captures in a private pty, the command
// a placeholder), at 120, 100, 57 and 40 columns and a few seconds apart. 2.1.322 draws a │ before each of the
// command's rows, as before the warning under it, so the command was read as part of the warning: no client showed
// it, and it was not in Rev. On a narrow pane the countdown wraps ("… block this request in" / "1:53, to avoid …"),
// and its seconds went into Rev: every answer was refused as a changed dialog (#5265).
func TestParseShellSafetyCheck(t *testing.T) {
	const cmd = `cd ./dist && F=(${(f)"$(grep -oE '(src|href)="\./assets/[^"]+\.js"' index.html | sed -E 's/.*="\.\///; s/"$//' | sort -u)"}); echo "${#F[@]} files"; for k in alpha_name beta_name gamma_name delta_name epsilon_name; do echo "$k: $(cat "${F[@]}" | grep -o "$k" | wc -l | xargs)"; done`
	names := []string{"120", "later-120", "100", "57", "later-57", "40"}
	var rev string
	seconds := map[string]int{}
	for _, name := range names {
		screen := fixture(t, "claude-2.1.322-danger-shell-"+name+".txt")
		d := ParseDialog(screen)
		if d == nil {
			t.Fatalf("%s: no dialog", name)
		}
		if d.Question != "Do you want to proceed?" || d.Command != cmd || d.Description != "Count the example names in first-screen chunks" ||
			d.Warning != "Dangerous rm operation not ruled out — the shell may read this command differently than it was parsed" ||
			d.Risk != RiskDelete || d.DeadlineAction != DeadlineDeny ||
			!regexp.MustCompile(`^Claude Code will automatically block this request in 1:\d\d, to avoid holding up an unattended session$`).MatchString(d.Deadline) {
			t.Fatalf("%s: question %q\ncommand %q\ndescription %q\nwarning %q\nrisk %q\ndeadline %q (%s)", name, d.Question, d.Command,
				d.Description, d.Warning, d.Risk, d.Deadline, d.DeadlineAction)
		}
		// Older clients show Detail: the command is in it again, the countdown is not.
		if !strings.HasPrefix(d.Detail, "Bash command\nCount the example names in") || !strings.Contains(squash(d.Detail), squash(cmd)) ||
			strings.Contains(d.Detail, "unattended") {
			t.Fatalf("%s: detail %q", name, d.Detail)
		}
		if len(d.Options) != 2 || d.Options[0].Label != "Yes" || d.Options[1].Label != "No" || !slices.Equal(d.Options[0].Keys, []string{"1"}) {
			t.Fatalf("%s: options %+v", name, d.Options)
		}
		seconds[name] = d.DeadlineIn
		// The same dialog as the clock runs and at every width.
		if rev == "" {
			rev = d.Rev
		} else if d.Rev != rev {
			t.Errorf("%s: rev %s, at 120 columns %s", name, d.Rev, rev)
		}
	}
	if s := seconds["120"]; s < 100 || s > 120 || seconds["later-120"] >= s || seconds["later-57"] >= seconds["57"] {
		t.Errorf("seconds left %v", seconds)
	}
	narrow := fixture(t, "claude-2.1.322-danger-shell-57.txt")
	// Another command, or another question, is another dialog.
	at := strings.LastIndex(narrow, "epsilon_name") // the box's, not the conversation's above it
	if d := ParseDialog(narrow[:at] + "omega_name" + narrow[at+len("epsilon_name"):]); d == nil || d.Rev == rev || !strings.Contains(d.Command, "omega_name") {
		t.Errorf("another command: %+v", d)
	}
	if d := ParseDialog(strings.Replace(narrow, "Do you want to proceed?", "Do you want to run it?", 1)); d == nil || d.Rev == rev {
		t.Errorf("another question: %+v", d)
	}
	// The countdown gone (another mode), or the warning reworded: the same command asked about.
	noClock := strings.Replace(narrow, " ⚠ Claude Code will automatically block this request in\n 1:53, to avoid holding up an unattended session\n", "", 1)
	if d := ParseDialog(noClock); noClock == narrow || d == nil || d.Rev != rev || d.Deadline != "" || d.DeadlineAction != "" {
		t.Errorf("without the countdown: %+v", d)
	}
	// The countdown over three rows (40 columns) reads the same, and a row after it is no part of it.
	d := ParseDialog(fixture(t, "claude-2.1.322-danger-shell-40.txt"))
	if d.DeadlineIn != 114 {
		t.Errorf("40 columns: %d s left, want 114", d.DeadlineIn)
	}
}

// The countdown's rows: one, or wrapped onto several, ending with its sentence; anything else is not one.
func TestDeadlineRows(t *testing.T) {
	for _, c := range []struct {
		rows []string
		n    int
		in   int
		act  string
	}{
		{[]string{"⚠ Claude Code will automatically block this request in 1:58, to avoid holding up an unattended session", "", "Do you want to proceed?"}, 1, 118, DeadlineDeny},
		{[]string{"⚠ Claude Code will automatically block this request in 1:58, to avoid holding up an unattended", "session", "", ""}, 2, 118, DeadlineDeny},
		{[]string{"⚠ Claude Code will automatically block", "this request in 1:54, to avoid", "holding up an unattended session", "Do you want to proceed?"}, 3, 114, DeadlineDeny},
		{[]string{"⚠ Claude Code will automatically deny this request in 0:42, to avoid holding up an unattended session", "Another row"}, 1, 42, DeadlineDeny},
		{[]string{"Claude Code will automatically approve this in 10:05"}, 1, 605, DeadlineAllow},
		{[]string{"⚠ Something else entirely", "on two rows"}, 0, 0, ""},
		{[]string{"⚠ Claude Code will automatically block this request in", "Do you want to proceed? 1:00"}, 0, 0, ""},
	} {
		text, n := deadlineRows(c.rows)
		if n != c.n || n > 0 && (deadlineSeconds(text) != c.in || deadlineAction(text) != c.act || strings.HasPrefix(text, "⚠")) {
			t.Errorf("%q: %d rows %q (%d s, %s), want %d rows, %d s, %s", c.rows, n, text, deadlineSeconds(text), deadlineAction(text), c.n, c.in, c.act)
		}
	}
}

// A question whose options have previews (AskUserQuestion's preview field, Claude Code 2.1.303): the options sit
// beside a box showing the focused one's preview, and a number only moves the cursor — enter picks ("Enter to
// select · ↑/↓ to navigate"). Pressing the number alone left the card answered and the question waiting
// (2026-11-04). The box is not part of any label or detail, and the cursor's place does not change the revision.
func TestQuestionWithPreviews(t *testing.T) {
	d := ParseDialog(fixture(t, "claude-2.1.303-questions-preview-198.txt"))
	if d == nil || d.Question != "文案可以吗？" || len(d.Options) != 3 {
		t.Fatalf("preview question: %+v", d)
	}
	for k, want := range []string{"可以", "要改", "不发"} {
		o := d.Options[k]
		if o.Label != want || o.Detail != "" || o.Kind != "" {
			t.Errorf("option %d = %+v, want label %q and no detail", k+1, o, want)
		}
		if n := strconv.Itoa(k + 1); !slices.Equal(o.Keys, []string{n, "enter"}) {
			t.Errorf("option %d keys = %v, want [%s enter]", k+1, o.Keys, n)
		}
	}
	moved := ParseDialog(fixture(t, "claude-2.1.303-questions-preview-cursor-198.txt"))
	if moved == nil || moved.Rev != d.Rev || moved.Cursor != "2" || moved.Options[1].Label != "要改" {
		t.Fatalf("cursor moved: %+v (rev %s, before %s)", moved, moved.Rev, d.Rev)
	}
	// Without previews a number still picks by itself.
	if o := ParseDialog(fixture(t, "claude-2.1.316-questions-single-100.txt")).Options[0]; !slices.Equal(o.Keys, []string{"1"}) {
		t.Errorf("no previews: keys %v", o.Keys)
	}
}

// #2872: the agent's own answer above a dialog is often a numbered list ("I'll do three things: 1. … 2. …"). Those
// numbers are the conversation's words, not the dialog's options: the options start under the rule that opens the
// dialog (dialogTop), and the dialog is the last thing on screen. Read as options they made the auto mode notice a
// choice, so NoticeOnly said no and dismiss_notice refused to close it (#2612, #2553).
func TestNumberedListItemInTheAnswerIsNotAnOption(t *testing.T) {
	const answer = "⏺ I'll do three things:\n  1. Fix the race in the watcher\n  2. Add a regression test\n  3. Push the branch"
	notice := strings.Replace(fixture(t, "claude-2.1.309-auto-mode-notice-120.txt"), "⏺ All 48 tests pass.", answer, 1)
	if !strings.Contains(notice, "Fix the race in the watcher") {
		t.Fatal("the committed notice no longer holds the answer this test swaps")
	}
	if d := ParseDialog(notice); d != nil {
		t.Fatalf("the answer's numbered list read as the notice's options: %q %+v", d.Question, d.Options)
	}
	if !NoticeOnly(notice) {
		t.Error("the notice under a numbered answer is no longer a notice")
	}
	if got := DialogHead(notice); got != "We're changing auto mode to no longer charge for classifier requests in Claude Code." {
		t.Errorf("the notice's first line: %q", got)
	}

	// A finished turn ends the conversation: the list is not a dialog either, whatever is (or is not) below it.
	if d := ParseDialog("❯ run the tests\n\n⏺ What I'll do:\n  1. Fix the flaky test\n  2. Push the branch\n\n✻ Cooked for 12s\n"); d != nil {
		t.Fatalf("a finished answer read as a dialog: %q %+v", d.Question, d.Options)
	}

	// The rules of the conversation above do not count: the dialog is read under its own rule all the same.
	const twoLists = `❯ run the tests

⏺ What I'll do:
  1. Fix the flaky test
  2. Push the branch

✻ Cooked for 12s
────────────────────────────────────
 Pick one

 ❯ 1. Yes
   2. No

 Enter to confirm · Esc to cancel
`
	if d := ParseDialog(twoLists); d == nil || d.Question != "Pick one" || len(d.Options) != 2 || d.Options[1].Label != "No" {
		t.Fatalf("the dialog under the answer's list: %+v", d)
	}
}

// #4159: a panel without options (/help, a notice) under the user's own prompt. The prompt is `❯ label` at the left
// edge, and the row it wraps onto (or a slash command's `⎿` output) lines up with it, so the unnumbered reader took
// the two rows for a dialog: the phone offered the user's words as buttons, and NoticeOnly refused the notice.
// Recorded on Claude Code 2.1.318 at 120 columns.
func TestUserPromptAboveAPanelIsNotAnOption(t *testing.T) {
	for _, name := range []string{"claude-2.1.318-help-wrapped-prompt-120.txt", "claude-2.1.318-help-after-command-120.txt"} {
		s := fixture(t, name)
		if !Choosing(s) {
			t.Fatalf("%s: the panel no longer holds the keyboard", name)
		}
		if d := ParseDialog(s); d != nil {
			t.Errorf("%s: the conversation read as a dialog: %q %+v", name, d.Question, d.Options)
		}
		if got := DialogHead(s); got != "Help  General   Commands   Custom commands" {
			t.Errorf("%s: the panel's first line: %q", name, got)
		}
	}

	const wrapped = "❯ run the tests again, and when they pass push the branch and open a pull request for the watcher\n  fix we talked about this morning"
	notice := strings.Replace(fixture(t, "claude-2.1.309-auto-mode-notice-120.txt"), "❯ run the tests again", wrapped, 1)
	if !strings.Contains(notice, "fix we talked about") {
		t.Fatal("the committed notice no longer holds the prompt this test swaps")
	}
	if d := ParseDialog(notice); d != nil {
		t.Fatalf("the wrapped prompt read as the notice's options: %+v", d.Options)
	}
	if !NoticeOnly(notice) {
		t.Error("the notice under a wrapped prompt is no longer a notice")
	}

	// The trust prompt's own unnumbered list is still read, under the conversation as well.
	trust := ParseDialog("❯ hello there, this prompt wraps\n  onto a second row\n\n⏺ ok\n\n" + fixture(t, "claude-2.1.312-trust-162.txt"))
	if trust == nil || len(trust.Options) != 2 || trust.Options[1].Label != "Yes, I trust this folder" {
		t.Fatalf("trust prompt under a conversation: %+v", trust)
	}

	// A list taller than the 30 rows the cursor is looked for in keeps its top options and its question.
	var tall strings.Builder
	tall.WriteString("────────────────────────────────────\n Pick a session to resume\n\n")
	for k := range 30 {
		tall.WriteString("   session " + strconv.Itoa(k) + "\n")
	}
	tall.WriteString(" ❯ session 30\n\n Enter to select · Esc to cancel\n")
	long := ParseDialog(tall.String())
	if long == nil || len(long.Options) != 31 || long.Options[0].Label != "session 0" || long.Question != "Pick a session to resume" {
		t.Fatalf("a tall list: %+v", long)
	}
	if o := long.Options[0]; len(o.Keys) != 31 || o.Keys[0] != "up" || o.Keys[30] != "enter" {
		t.Fatalf("a tall list's first option keys: %v", o.Keys)
	}
}

// #5538: the same AskUserQuestion redrawn at another width (the pane resized) wraps its long question and its
// options' details at other places, and a colored read has escapes around every word. It is the same dialog: same
// Rev. Before, Question was only the wrapped row with the question mark and Detail the rows around it, so the
// revision changed with the width and an answer to a card made before the resize was refused with prompt_changed.
// The fixtures are drawn from the issue's description (two questions in a tab bar, a long Chinese question over
// three rows, background tasks above).
func TestRevisionIgnoresWrapAndColor(t *testing.T) {
	wide := ParseDialog(fixture(t, "claude-2.1.318-questions-wrapped-100.txt"))
	narrow := ParseDialog(fixture(t, "claude-2.1.318-questions-wrapped-72.txt"))
	if wide == nil || narrow == nil || len(wide.Options) != 4 || len(narrow.Options) != 4 || len(wide.Steps) != 2 {
		t.Fatalf("wide %+v narrow %+v", wide, narrow)
	}
	if wide.Options[0].Detail == narrow.Options[0].Detail && wide.Question == narrow.Question {
		t.Fatal("the fixtures do not wrap differently")
	}
	if wide.LegacyRev == narrow.LegacyRev {
		t.Fatal("the old revision did not move with the width: the fixtures do not show #5538")
	}
	if wide.Rev != narrow.Rev {
		t.Fatalf("another width, another revision: %s → %s", wide.Rev, narrow.Rev)
	}
	colored := regexp.MustCompile(`\S+`).ReplaceAllString(fixture(t, "claude-2.1.318-questions-wrapped-100.txt"), "\x1b[38;5;153m$0\x1b[39m")
	if c := ParseDialog(colored); c == nil || c.Rev != wide.Rev {
		t.Fatalf("colored: %+v", c)
	}

	// What is asked or offered does change it.
	if changed := ParseDialog(fixture(t, "claude-2.1.318-questions-wrapped-changed-100.txt")); changed == nil || changed.Rev == wide.Rev {
		t.Fatal("another question, same revision")
	}
	wideText := fixture(t, "claude-2.1.318-questions-wrapped-100.txt")
	for name, edit := range map[string][2]string{
		"option label":  {"等回归跑完\n", "等回归跑完再说\n"},
		"option detail": {"大概要多等十分钟", "大概要多等五分钟"},
	} {
		other := strings.Replace(wideText, edit[0], edit[1], 1)
		if other == wideText {
			t.Fatalf("%s: no edit", name)
		}
		if d := ParseDialog(other); d == nil || d.Rev == wide.Rev {
			t.Errorf("another %s, same revision", name)
		}
	}
}

// #6318: a read of only the screen's bottom rows is taken for a dialog only when it shows the dialog from its opening
// rule; one cut by the read (a permission's command above it) is not whole.
func TestDialogWhole(t *testing.T) {
	bottom := func(s string, n int) string {
		rows := strings.Split(strings.TrimRight(s, "\n"), "\n")
		return strings.Join(rows[max(0, len(rows)-n):], "\n")
	}
	for _, name := range []string{"claude-2.1.316-danger-rm-120.txt", "claude-2.1.318-questions-wrapped-100.txt", "claude-2.1.312-trust-162.txt"} {
		s := fixture(t, name)
		if !DialogWhole(s) {
			t.Errorf("%s: the whole screen is not whole", name)
		}
	}
	if DialogWhole(bottom(fixture(t, "claude-2.1.316-danger-rm-120.txt"), 12)) {
		t.Error("the permission without its title and command is whole")
	}
	if DialogWhole(bottom(fixture(t, "claude-2.1.318-questions-wrapped-100.txt"), 9)) {
		t.Error("the question without its top is whole")
	}
	if DialogWhole(fixture(t, "claude-2.1.312-empty-162.txt")) {
		t.Error("no dialog is whole")
	}
}

// A trust prompt drawn with a `>` cursor and a `↑/↓ Navigate · enter Confirm` footer (Antigravity CLI), the path
// synthetic: its rows are options, picked by arrow moves and enter.
const angleCursorScreen = `Accessing workspace:

/work/example-project

Do you trust the contents of this project?

Antigravity CLI requires permission to read, edit, and execute files here.

> Yes, I trust this folder
  No, exit

  ↑/↓ Navigate · enter Confirm
                                                                     auto · Example model · medium`

func TestAngleCursorListUnderNavigateFooterIsADialog(t *testing.T) {
	if !Choosing(angleCursorScreen) {
		t.Fatal("the footer should say a dialog has the keyboard")
	}
	d := ParseDialog(angleCursorScreen)
	if d == nil || len(d.Options) != 2 {
		t.Fatalf("dialog %+v", d)
	}
	if d.Options[0].Label != "Yes, I trust this folder" || d.Options[1].Label != "No, exit" {
		t.Fatalf("options %+v", d.Options)
	}
	if !slices.Equal(d.Options[0].Keys, []string{"enter"}) || !slices.Equal(d.Options[1].Keys, []string{"down", "enter"}) {
		t.Fatalf("keys %v / %v", d.Options[0].Keys, d.Options[1].Keys)
	}
	// With the cursor on the second row, the first is reached by up.
	moved := strings.Replace(strings.Replace(angleCursorScreen, "> Yes", "  Yes", 1), "  No, exit", "> No, exit", 1)
	if d := ParseDialog(moved); d == nil || !slices.Equal(d.Options[0].Keys, []string{"up", "enter"}) {
		t.Fatalf("moved cursor: %+v", d)
	}
}

func TestAngleRowsWithoutNavigateFooterAreNotOptions(t *testing.T) {
	for name, screen := range map[string]string{
		// The input box: a `> text` row, its wrapped line lined up under it, and a status hint.
		"input box": "Some answer\n\n> fix the login bug and\n  then run the tests\n\n  ? for shortcuts",
		// Quoted output with `>` in it, no footer.
		"output": "$ cat notes.md\n> first quote\n  second line\n> third quote\nDone.",
		// The footer is there but the `>` row has no row lined up with it: nothing to choose between.
		"single": "Pick one\n\n> only row\n\n  ↑/↓ Navigate · enter Confirm",
		// A footer of other hints (or a `>` row far above it, under other text) is not the arrow-key list.
		"other footer": "Pick one\n\n> Yes\n  No\n\n  Esc to cancel",
	} {
		if d := ParseDialog(screen); d != nil {
			t.Errorf("%s: read as a dialog %+v", name, d)
		}
	}
}
