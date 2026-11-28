package assistant

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// The kind of work a task's stage strip follows (#3380, #11590, docs/assistant/todos.md "Data model"): code is a change
// on its way to production, drawn as the six steps (plan → develop → review → merge → deploy → verified); general is
// anything else (research, a verification, an errand), whose status already says how it goes, so no strip is drawn.
// Evidence outranks words, and a doubt reads general: a strip drawn for work that changes no code stays on Develop for
// good, while a code task read as general only goes without a strip until its change shows up.

// taskStageKind is the legacy classification for a brief without a work mode. A change is code, then a read-only
// dispatch is general. The remaining briefs use the goal and criteria to recover the original stage kind.
func taskStageKind(readonly bool, goal string, doneWhen []string, changed bool) string {
	switch {
	case changed:
		return "code"
	case readonly:
		return "general"
	}
	for _, text := range append([]string{goal}, doneWhen...) {
		if codeChangeAsked(text) {
			return "code"
		}
	}
	return "general"
}

var (
	// codeChangeWords name a change to code as a result: a PR or MR, a commit, a fix, an implementation, a refactor, unit
	// tests, CI. Words that only name what is read, tried or shipped (code, test, deploy, release, feature, bug) are not
	// among them: research cites code, a verification uses a test account and redeploys. PR, MR and CI are matched as
	// written ("Mr Lee" is no merge request).
	codeChangeWords = lazyre.New(`\b(?:PRs?|MRs?|CI)\b|(?i:\b(?:pull requests?|merge requests?|commit(?:s|ted|ting)?|(?:hot)?fix(?:es|ed|ing)?|` +
		`implement(?:s|ed|ing|ation)?|refactor(?:s|ed|ing)?|unit tests?|code changes?|(?:change|edit|modify|update) (?:the )?code)\b|` +
		`\bmerged? (?:in)?to (?:main|master|trunk)\b)|` +
		// 修 alone is how a title says fix ("修登录崩溃"), but not in 修改, 修订, 修理 (edit, revise, repair) and the like
		`修(?:[^改订理养饰辞剪建缮整葺行炼身心]|$)|实现|重构|合入|提交代码|代码提交|改代码|修改代码|代码改动|代码修改|改动代码|代码变更|单测|单元测试|测试用例|编译通过|` + // i18n-ignore: matches what the agent or user wrote, not text shown
		`合并到\s*(?:main|master|主干|主分支)|実装|コミット|プルリク|リファクタ|単体テスト`) // i18n-ignore: matches what the agent or user wrote, not text shown
	// checkWords name finding out, checking or reporting: before a change word in its clause they make the change what is
	// checked or reported ("verify the fix", "list what needs fixing").
	checkWords = lazyre.New(`(?i)\b(?:research\w*|investigat\w*|look into|report\w*|analy[sz]\w*|summar\w*|triage\w*|verif\w*|` +
		`validat\w*|check\w*|confirm\w*|audit\w*|review\w*|diagnos\w*|troubleshoot\w*|explor\w*|evaluat\w*|assess\w*|compar\w*|` +
		`reproduc\w*|list|identify|find|locate|explain|describe|document|propose|recommend|cite)\b|` +
		`调研|研究|调查|报告|分析|总结|分诊|验证|核实|核对|核查|确认|检查|审查|评审|诊断|排查|定位|复现|评估|对比|梳理|盘点|方案|列出|列举|给出|写出|` + // i18n-ignore: matches what the agent or user wrote, not text shown
		`说明|指出|查明|查清|調査|検証|確認|報告|比較|評価|レビュー`) // i18n-ignore: matches what the agent or user wrote, not text shown
	// conditionWords make a sentence a condition: what to do if something turns up ("if the check finds a bug, fix it",
	// "若…", "…则…", "otherwise") is not what the task is asked to deliver, nor is a choice still open ("whether").
	conditionWords = lazyre.New(`(?i)\b(?:if|unless|in case|otherwise|whether)\b|^\W*when\b|若|如果|假如|假若|倘若|万一|一旦|要是|如若|如有|` + // i18n-ignore: matches what the agent or user wrote, not text shown
		`如需|如发现|如遇|否则|不然|是否|(?:^|[^规原细总法守准否])则|もし|場合`) // i18n-ignore: matches what the agent or user wrote, not text shown
	// A change word right after one of these, in its clause, is one not to make ("leave the code alone" is said as
	// "don't change the code", "不改代码", "no PR").
	notChangeZh = lazyre.New(`不|没|未|别|无需|无须|勿|免|禁止`) // i18n-ignore: matches what the agent or user wrote, not text shown
	notChangeEn = lazyre.New(`(?i)\b(?:not|no|never|without|nor)\b|n't\b`)
)

// codeChangeAsked: text asks for a code change outright, in a sentence that is no condition, in a clause that names the
// change before any checking or reporting ("verify the fix" checks; "find the cause, then fix it" changes), not
// negated.
func codeChangeAsked(text string) bool {
	for _, sentence := range kindSentences(text) {
		if conditionWords.MatchString(sentence) {
			continue
		}
		for _, clause := range kindClauses(sentence) {
			change := firstUnnegated(codeChangeWords, clause)
			if change < 0 {
				continue
			}
			if check := checkWords.FindStringIndex(clause); check != nil && check[0] < change {
				continue
			}
			return true
		}
	}
	return false
}

// kindSentences splits text where a sentence ends: a condition holds up to there.
func kindSentences(text string) []string {
	return strings.FieldsFunc(sentenceEnds.ReplaceAllString(text, "\n"), func(r rune) bool { return r == '\n' })
}

var sentenceEnds = lazyre.New(`[。；;！？!?]|\.(?:\s|$)`) // i18n-ignore: punctuation of what the agent or user wrote

// kindClauses splits a sentence into its clauses: at commas, enumeration and colons, and at a conjunction joining two
// actions ("and", "then", 并 outside 合并, 然后, 之后, 再).
func kindClauses(sentence string) []string {
	var out []string
	start := 0
	prev := rune(0)
	for i, r := range sentence {
		cut := strings.ContainsRune("，,、：:", r) || r == '再' || r == '并' && !strings.ContainsRune("合吞兼归", prev) // i18n-ignore: what the agent or user wrote
		if cut {
			out = append(out, sentence[start:i])
			start = i + utf8.RuneLen(r)
		}
		prev = r
	}
	out = append(out, sentence[start:])
	var clauses []string
	for _, part := range out {
		for _, c := range clauseJoins.Split(part, -1) {
			if c = strings.TrimSpace(c); c != "" {
				clauses = append(clauses, c)
			}
		}
	}
	return clauses
}

var clauseJoins = lazyre.New(`(?i)\s+(?:and|then)\s+|然后|之后`) // i18n-ignore: matches what the agent or user wrote

// firstUnnegated is where the first match of re in clause starts that the few words right before it do not negate (four
// characters for Chinese, three words for English); -1 for none.
func firstUnnegated(re *lazyre.Regexp, clause string) int {
	for _, m := range re.FindAllStringIndex(clause, -1) {
		pre := clause[:m[0]]
		tail := pre
		for i, n := len(pre), 0; i > 0; n++ {
			if n == 4 {
				tail = pre[i:]
				break
			}
			_, w := utf8.DecodeLastRuneInString(pre[:i])
			i -= w
		}
		words := strings.Fields(pre)
		if len(words) > 3 {
			words = words[len(words)-3:]
		}
		if notChangeZh.MatchString(tail) || notChangeEn.MatchString(strings.Join(words, " ")) {
			continue
		}
		return m[0]
	}
	return -1
}

// changeSeen: a reply shows the work made a change to ship without naming its PR's link: its closeout names a pr,
// commit or branch artifact, or its delivery is held at a PR or a merge.
func changeSeen(text string, d TodoDelivery, marked bool) bool {
	if marked && (d.State == deliveryPR || d.State == deliveryMerged) {
		return true
	}
	if _, artifacts, ok := closeout.Parse(text); ok {
		for _, a := range artifacts {
			if a.Kind == "pr" || a.Kind == "commit" || a.Kind == "branch" {
				return true
			}
		}
	}
	return false
}

// restage updates the stage kind from the current work mode and delivery evidence. mark supplies a start's
// read-only flag or a reply's change artifact. A repository binding gives execute work a code stage before it
// produces an artifact. Actual code delivery evidence also gives research or coordination work a stage.
func (s *Service) restage(ctx context.Context, id string, mark func(*todoStage)) {
	t, ok := s.todoNow(id)
	if !ok {
		return
	}
	b := &s.reports
	b.stageWriting.Lock()
	defer b.stageWriting.Unlock()
	brief, hasBrief := s.committedBriefOfTodo(id)
	var boundRepository bool
	var repoErr error
	if brief.WorkModeNow == assistantwire.WorkModeExecute {
		if st, ok := s.store().(todoRepositoryStore); ok {
			set, err := st.TodoRepositories(ctx, id)
			boundRepository, repoErr = err == nil && len(set.Repositories) > 0, err
			if err != nil {
				s.logf("task %s: repositories for stage kind: %v", id, err)
			}
		}
	}
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		s.logf("task %s: stage kind not kept: the stages could not be read", id)
		return
	}
	was := b.stages[id]
	cur := was
	if mark != nil {
		mark(&cur)
	}
	changedCode := cur.PR != "" || cur.Change
	switch {
	case changedCode, boundRepository:
		cur.Kind = "code"
	case brief.WorkModeNow == "":
		goal, criteria := brief.Goal, brief.Criteria
		if !hasBrief && t.Supervision != nil {
			criteria = t.Supervision.DoneWhen
		}
		cur.Kind = taskStageKind(cur.Readonly, firstNonEmpty(goal, t.Title), criteria, false)
	case repoErr == nil:
		cur.Kind = "general"
	}
	if cur.Kind == was.Kind && cur.Readonly == was.Readonly && cur.Change == was.Change {
		b.mu.Unlock()
		return
	}
	b.stages[id] = cur
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, reportStagesKey, b.stages); err != nil {
			s.logf("task %s: stage kind: %v", id, err)
		}
	}
	b.mu.Unlock()
	if cur.Kind == was.Kind {
		return
	}
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.StageKind = cur.Kind
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
}

// stageDone: task t's work ended where its strip stands, with nothing left to deploy (#11590): it was closed as done
// (not cancelled, not a failed start), its strip is drawn (not general) and short of deploy, and nothing waits to ship:
// no delivery held at a PR, a merge or a switch, and with a PR only a delivery that says nothing ships. The apps end
// the strip with Done after the steps it went through. Work that went out keeps Deploy, or Verified once checked
// (#3240): acceptance alone is no check of a deployment.
func stageDone(t Todo) bool {
	at := stageIndex(t.Stage)
	if t.DoneAt == nil || t.Cancelled() || t.StartError != "" || t.StageKind == "general" || at < 0 || at >= stageIndex("deploy") {
		return false
	}
	if t.Delivery.held() {
		return false
	}
	return t.PRURL == "" || t.Delivery != nil && t.Delivery.State == deliveryNone
}
