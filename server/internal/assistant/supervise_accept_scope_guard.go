package assistant

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Multi-scope coverage guard for legacy bare `task_review accepted` (#13169). Structured task contracts with
// validated checks use the explicit deferred_scope and follow_up_todo_id fields for handoffs.
//
// The wording guard in supervise_accept_guard.go (#12026) catches explicit blocker, closeout-not-done and
// inconclusive phrasing, but a supervisor can reword leftover scope as a finished handoff ("not touched this round,
// tell me the scope if you want them handled") and close the task without a card or follow-up. This second layer
// compares reliably structured deliverable scopes from the brief/notes (plus explicit out-of-scope lists recorded
// while closing and unsettled owner-confirmation outcome decisions) against positive review coverage and linked
// follow-up to-dos / open asks.
//
// When the brief has no reliable multi-scope structure and nothing records an unsettled scope wait, this guard fails
// open so ordinary single-scope tasks and prose briefs are never blocked on guesswork.

var (
	// Markdown heading or short "Heading:" / "标题：" section label.
	scopeHeadingRe   = lazyre.New(`^\s*(?:#{1,6}\s+(.+?)\s*#*|\*{0,2}([^:*：\n]{2,48})\*{0,2}\s*[:：]\s*)$`) // i18n-ignore: parses original brief headings
	scopeNumberedRe  = lazyre.New(`^\s*(?:\d{1,2}[.)、]|[（(]\d{1,2}[)）]|[①②③④⑤⑥⑦⑧⑨⑩])\s*(.+)$`)             // i18n-ignore: parses original brief lists
	scopeBulletRe    = lazyre.New(`^ {0,3}(?:[-*•])\s+(?:\[[ xX]\]\s+)?(.+)$`)
	scopeMetadataKV  = lazyre.New(`(?i)^(?:repo|repository|branch|machine|cwd|path|url|link|pr|issue|owner|author|status|state|date|time|file|files|commit|ref|id|todo|session|engine|model|env|region|cluster|account|machine_id|session_id|todo_id)\s*[:：]\s*\S`) // i18n-ignore: parses metadata delimiters
	scopeExclusionRe = lazyre.New(`(?i)^(?:do not |don'?t |never |avoid |not in scope|out of scope|out of bounds|non-?goal|no changes to |exclude )`)
	scopeExclZhRe    = lazyre.New(`^(?:不要|不做|禁止|避免|不包含|不涉及|范围外|非目标|无需|不用)`) // i18n-ignore: parses original brief exclusion items
	todoIDMentionRe  = lazyre.New(`\btodo_[A-Za-z0-9_-]{4,}\b`)

	// Clauses that explicitly defer / leave out work rather than proving it was done.
	deferredClauseRe = lazyre.New(`(?i)\b(` +
		`(?:not|never|wasn'?t|weren'?t|haven'?t|hasn'?t)\s+(?:touched|handled|addressed|done|implemented|cleaned|included|covered|applied)(?:\s+(?:this|in this)\s+round)?|` +
		`left\s+(?:out|undone|untouched|unhandled|for\s+later|for\s+now|open)|` +
		`out\s+of\s+scope|` +
		`deferred\s+(?:to|for|until)|` +
		`skipped\s+(?:for\s+now|this\s+round)|` +
		`omitted\s+(?:for\s+now|this\s+round)|` +
		`pending\s+(?:owner|user)?\s*(?:confirmation|approval|decision|scope)|` +
		`(?:tell|let)\s+me\s+(?:know\s+)?(?:the\s+scope\s+)?if\s+you\s+(?:want|would\s+like|need)|` +
		`if\s+you\s+(?:want|would\s+like)\s+(?:them|it|these|those|this|us|me)\s+(?:handled|done|cleaned|changed|updated|included|addressed|fixed)|` +
		`(?:confirm|let\s+me\s+know)\s+(?:before|if|whether)\s+(?:we|i|you)\s+(?:should|want|proceed|touch|handle|run|clean|change|update)` +
		`)\b`)
	deferredClauseZhRe = lazyre.New(`(?:本轮|本次|暂时?|先)?(?:未|没|没有)(?:动|碰|处理|做|改|变更|修改|清理|涵盖|包含|涉及|覆盖)|` + // i18n-ignore: matches original review/decision text
		`(?:留待|留到|留给|暂不|先不)(?:后续|下轮|以后|用户|你)?(?:处理|确认|决定|做|改)?|` + // i18n-ignore: matches original review/decision text
		`(?:如需|如果需要|如果要|如果想|若需|要是需要)[^。\n]{0,30}(?:处理|做|改|清理|调整|推进|安排|再说|告诉我|告知|确认)|` + // i18n-ignore: matches original review/decision text
		`(?:告诉我|告知|确认)(?:一下)?(?:范围|要不要|是否|后再)|` + // i18n-ignore: matches original review/decision text
		`(?:待|等|需要)(?:用户|你|主人)?(?:确认|拍板|决定|明确)(?:范围|后|再|是否)|` + // i18n-ignore: matches original review/decision text
		`不在本(?:轮|次)范围|范围外未处理|遗留(?:项|事项|范围)`) // i18n-ignore: matches original review/decision text

	// Stronger signal: an explicit owner-confirmation handoff condition (for closing text & outcome decisions).
	ownerConfirmRe = lazyre.New(`(?i)\b(` +
		`(?:tell|let)\s+me\s+(?:know\s+)?(?:the\s+scope\s+)?if\s+you\s+(?:want|would\s+like|need)|` +
		`if\s+you\s+(?:want|would\s+like)\s+(?:them|it|these|those|this|us|me)\s+(?:handled|done|cleaned|changed|updated|included|addressed|fixed)|` +
		`(?:confirm|let\s+me\s+know)\s+(?:before|if|whether)\s+(?:we|i|you)\s+(?:should|want|proceed|touch|handle|run|clean|change|update)|` +
		`(?:pending|awaiting)\s+(?:owner|user)?\s*(?:confirmation|approval|decision|scope)` +
		`)\b`)
	ownerConfirmZhRe = lazyre.New(`(?:如需|如果需要|如果要|如果想|若需|要是需要)[^。\n]{0,30}(?:处理|做|改|清理|调整|推进|安排|再说|告诉我|告知|确认)|` + // i18n-ignore: matches original review/decision text
		`(?:告诉我|告知|确认)(?:一下)?(?:范围|要不要|是否|后再)|` + // i18n-ignore: matches original review/decision text
		`(?:待|等|需要)(?:用户|你|主人)?(?:确认|拍板|决定|明确)(?:范围|后|再|是否)`) // i18n-ignore: matches original review/decision text

	// Strip boilerplate confirmation / untouched tails when turning a deferred clause into a concise scope label.
	deferredTailRe = lazyre.New(`(?i)(?:,?\s*(?:were|was|are|is|have|has)?\s*(?:not|never|wasn'?t|weren'?t|haven'?t|hasn'?t)\s+(?:touched|handled|addressed|done|implemented|cleaned|included|covered|applied).*$|` +
		`,?\s*(?:tell|let)\s+me\s+(?:know\s+)?(?:the\s+scope\s+)?if\s+you.*$|` +
		`,?\s*if\s+you\s+(?:want|would\s+like).*$|` +
		`,?\s*(?:pending|awaiting)\s+(?:owner|user)?\s*(?:confirmation|approval|decision|scope).*$)`)
	deferredTailZhRe = lazyre.New(`(?:[，,]?\s*(?:本轮|本次|暂时?|先)?(?:未|没|没有)(?:动|碰|处理|做|改|变更|修改|清理|涵盖|包含|涉及|覆盖).*$|` + // i18n-ignore: trims deferred boilerplate
		`[，,]?\s*(?:如需|如果需要|如果要|如果想|若需|要是需要).*$|` + // i18n-ignore: trims deferred boilerplate
		`[，,]?\s*(?:待|等|需要)(?:用户|你|主人)?(?:确认|拍板|决定|明确).*$)`) // i18n-ignore: trims deferred boilerplate

	// Inline numbered sequences: "1. foo; 2. bar" / "1) foo; 2) bar" / "(1) foo (2) bar" / "1、foo；2、bar" / "① foo ② bar".
	inlineNumDotRe     = lazyre.New(`(?:^|[\s:：;；,，])(\d{1,2})\.\s+`) // i18n-ignore: parses inline numbered lists
	inlineNumParenRe   = lazyre.New(`(?:^|[\s:：;；,，])(\d{1,2})\)\s*`) // i18n-ignore: parses inline numbered lists
	inlineWrapParenRe  = lazyre.New(`[（(](\d{1,2})[)）]\s*`)           // i18n-ignore: parses inline numbered lists
	inlineZhDunhaoRe   = lazyre.New(`(?:^|[\s:：;；,，])(\d{1,2})、\s*`)  // i18n-ignore: parses inline numbered lists
	inlineCircledNumRe = lazyre.New(`([①②③④⑤⑥⑦⑧⑨⑩])\s*`)              // i18n-ignore: parses inline numbered lists
)

type scopeHeadingKind int

const (
	headingNeutral scopeHeadingKind = iota
	headingScope
	headingIgnore
	headingDeferred
)

func classifyScopeHeading(raw string) scopeHeadingKind {
	h := strings.ToLower(strings.TrimSpace(strings.Trim(raw, "*_`# ")))
	if h == "" {
		return headingNeutral
	}
	for _, k := range []string{
		"not touched", "not handled", "left out", "remaining", "deferred", "uncovered", "unhandled",
		"follow-up", "follow up", "followup", "pending confirmation", "not in this round",
		"未处理", "本轮未处理", "暂未处理", "未动", "遗留", "待确认", "后续", // i18n-ignore: matches original review headings
	} {
		if strings.Contains(h, k) {
			return headingDeferred
		}
	}
	for _, k := range []string{
		"out of bounds", "out of scope", "non-goal", "non goal", "not in scope", "exclude", "excluded",
		"do not", "don't", "avoid", "constraint", "background", "context", "reference", "caveat",
		"assumption", "environment", "setup", "risk", "repro", "reproduce", "reproduction", "symptom",
		"work report", "notes", "note",
		"不要做", "不做", "范围外", "非目标", "背景", "上下文", "约束", "注意", "参考", "前置条件", "环境", "复现", "重现", "现象", "工作报告", // i18n-ignore: matches original brief headings
	} {
		if strings.Contains(h, k) {
			return headingIgnore
		}
	}
	for _, k := range []string{
		"scope", "deliverable", "task", "work item", "subtask", "goal", "objective",
		"requirement", "item", "part", "checklist", "acceptance", "done when", "done_when", "todo", "to do", "plan",
		"范围", "交付", "任务", "子任务", "目标", "要求", "工作项", "事项", "清单", "需求", "验收", "待办", "计划", // i18n-ignore: matches original brief headings
	} {
		if strings.Contains(h, k) {
			return headingScope
		}
	}
	return headingNeutral
}

// acceptGuardUncoveredScopes returns the brief scopes (or explicit closing deferred scopes / unsettled
// owner-confirmation outcome waits) that are neither positively covered by the review nor carried by a linked
// follow-up to-do or open linked owner ask. Returns nil when the brief has no reliable multi-scope structure and no
// unsettled closing leftover is present (fail open).
func (s *Service) acceptGuardUncoveredScopes(ctx context.Context, t Todo, note, reason string, checks []ReviewCheck, alsoDone []string) []string {
	brief, hasBrief := s.briefOfTodo(t.ID)

	positiveTexts := collectPositiveReviewTexts(note, reason, checks, alsoDone)
	followupTexts := s.collectLinkedFollowupTexts(t, brief, hasBrief, note, reason, checks)

	coverageTexts := make([]string, 0, len(positiveTexts)+len(followupTexts))
	coverageTexts = append(coverageTexts, positiveTexts...)
	coverageTexts = append(coverageTexts, followupTexts...)
	coverageTokens := tokenizeTextSlice(coverageTexts)

	var uncovered []string

	// 1. Structured multi-scope deliverables from the task's brief / notes.
	if briefScopes := extractTaskBriefScopes(t, brief, hasBrief); len(briefScopes) >= 2 {
		termSets := make([]scopeTermSet, len(briefScopes))
		termFreq := map[string]int{}
		for i, sc := range briefScopes {
			ts := extractScopeTerms(sc)
			termSets[i] = ts
			for term := range ts.terms {
				termFreq[term]++
			}
		}
		for i, sc := range briefScopes {
			if !scopeCoveredBy(sc, termSets[i], termFreq, coverageTexts, coverageTokens) {
				uncovered = append(uncovered, sc)
			}
		}
	}

	followupTokens := tokenizeTextSlice(followupTexts)
	positiveTokens := tokenizeTextSlice(positiveTexts)

	// 2. Explicit out-of-scope / untouched list or owner-confirmation handoff recorded while closing.
	for _, item := range extractClosingDeferredScopes(note, reason, checks) {
		if scopeCoveredBySimple(item, followupTexts, followupTokens) {
			continue
		}
		if alreadyNamedUncovered(item, uncovered) {
			continue
		}
		uncovered = append(uncovered, item)
	}

	// 3. Effective outcome decisions in todo_decisions whose text records an owner-confirmation condition.
	for _, item := range s.unsettledOutcomeDecisions(ctx, t) {
		if scopeCoveredBySimple(item, positiveTexts, positiveTokens) ||
			scopeCoveredBySimple(item, followupTexts, followupTokens) {
			continue
		}
		if alreadyNamedUncovered(item, uncovered) {
			continue
		}
		uncovered = append(uncovered, item)
	}

	return uncovered
}

// acceptScopeGuardRefusal formats the refusal error naming the uncovered scope(s) and the valid exits.
func acceptScopeGuardRefusal(uncovered []string) string {
	shown := uncovered
	extra := 0
	if len(shown) > 3 {
		shown, extra = shown[:3], len(shown)-3
	}
	quoted := make([]string, len(shown))
	for i, sc := range shown {
		quoted[i] = fmt.Sprintf("%q", clip(oneLine(sc), 120))
	}
	list := strings.Join(quoted, ", ")
	if extra > 0 {
		list += fmt.Sprintf(" (+%d more)", extra)
	}
	return fmt.Sprintf("bare accepted refused: brief scope not covered (%s). "+
		"Re-review with verdict needs_user (why_owner, reason, recommend, options, cost_of_waiting), "+
		"or hand out the uncovered scope as a linked follow-up to-do (part_of / after) and then accept. "+
		"A question asked only in chat text never reaches Needs you.", list)
}

// extractTaskBriefScopes derives reliably structured deliverable scopes from t and its brief, excluding explicit
// out-of-bounds entries and criteria dropped by task_scope. Returns nil when fewer than two scopes can be derived.
func extractTaskBriefScopes(t Todo, brief TaskBrief, hasBrief bool) []string {
	candidates := []string{t.Notes}
	if hasBrief {
		if brief.Request != nil {
			candidates = append(candidates, brief.Request.Text)
		}
		candidates = append(candidates, brief.Handover, brief.Goal)
	}
	var scopes []string
	for _, src := range candidates {
		if parsed := parseStructuredScopes(src); len(parsed) >= 2 {
			scopes = parsed
			break
		}
	}
	if len(scopes) < 2 && t.Supervision == nil && hasBrief && len(brief.Criteria) >= 2 {
		scopes = slices.Clone(brief.Criteria)
	}
	if len(scopes) < 2 {
		return nil
	}

	var excluded []string
	if hasBrief {
		excluded = append(excluded, brief.OutOfBounds...)
		tb := &taskBrief{rec: brief.briefRecord, st: briefState{Log: brief.Log}}
		for _, dropped := range tb.dropped() {
			excluded = append(excluded, dropped.Criterion)
		}
	}
	exclTokens := tokenizeTextSlice(excluded)

	var out []string
	for _, sc := range scopes {
		sc = cleanScopeItem(sc)
		if sc == "" || utf8.RuneCountInString(sc) < 3 {
			continue
		}
		if len(excluded) > 0 && scopeCoveredBySimple(sc, excluded, exclTokens) {
			continue
		}
		if slices.ContainsFunc(out, func(existing string) bool { return sameCriterion(existing, sc) }) {
			continue
		}
		out = append(out, sc)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// parseStructuredScopes extracts deliverable items from a multi-item numbered/bulleted list or an explicit inline
// sequential numbered list. Returns nil when no reliable multi-item structure is present.
func parseStructuredScopes(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	inFence := false
	section := headingNeutral
	var headedScopes []string
	var neutralScopes []string

	for _, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" {
			continue
		}
		if m := scopeHeadingRe.FindStringSubmatch(rawLine); m != nil {
			title := m[1]
			if title == "" {
				title = m[2]
			}
			section = classifyScopeHeading(title)
			continue
		}
		if section == headingIgnore || section == headingDeferred {
			continue
		}
		item := ""
		if m := scopeNumberedRe.FindStringSubmatch(rawLine); m != nil {
			item = m[1]
		} else if m := scopeBulletRe.FindStringSubmatch(rawLine); m != nil {
			item = m[1]
		}
		if item == "" {
			continue
		}
		item = cleanScopeItem(item)
		if item == "" || isIgnoredScopeItem(item) {
			continue
		}
		if section == headingScope {
			headedScopes = append(headedScopes, item)
		} else {
			neutralScopes = append(neutralScopes, item)
		}
	}
	if len(headedScopes) >= 2 {
		return headedScopes
	}
	if len(neutralScopes) >= 2 {
		return neutralScopes
	}
	if inline := parseInlineNumberedScopes(text); len(inline) >= 2 {
		return inline
	}
	return nil
}

// parseInlineNumberedScopes splits a single-line sequential numbered list (1. ... 2. ... / (1) ... (2) ... / ① ... ② ...).
func parseInlineNumberedScopes(text string) []string {
	// Skip fenced code blocks.
	if strings.Contains(text, "```") {
		return nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, re := range []*lazyre.Regexp{inlineWrapParenRe, inlineZhDunhaoRe, inlineNumParenRe, inlineNumDotRe, inlineCircledNumRe} {
			if items := splitSequentialMatches(line, re); len(items) >= 2 {
				return items
			}
		}
	}
	return nil
}

func splitSequentialMatches(line string, re *lazyre.Regexp) []string {
	locs := re.FindAllStringSubmatchIndex(line, -1)
	if len(locs) < 2 {
		return nil
	}
	// Find a sequential run starting at 1, 2, ...
	var starts, ends []int
	expected := 1
	for _, loc := range locs {
		numStr := line[loc[2]:loc[3]]
		n := parseSmallItemNum(numStr)
		if n != expected {
			if expected > 1 {
				break
			}
			continue
		}
		starts = append(starts, loc[0])
		ends = append(ends, loc[1])
		expected++
	}
	if len(starts) < 2 {
		return nil
	}
	var out []string
	for i := range starts {
		end := len(line)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		seg := cleanScopeItem(line[ends[i]:end])
		if seg == "" || isIgnoredScopeItem(seg) {
			continue
		}
		out = append(out, seg)
	}
	return out
}

func parseSmallItemNum(s string) int {
	switch s {
	case "①":
		return 1
	case "②":
		return 2
	case "③":
		return 3
	case "④":
		return 4
	case "⑤":
		return 5
	case "⑥":
		return 6
	case "⑦":
		return 7
	case "⑧":
		return 8
	case "⑨":
		return 9
	case "⑩":
		return 10
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func cleanScopeItem(s string) string {
	s = strings.TrimSpace(s)
	// i18n-ignore: CJK punctuation trimmed from parsed scope items
	s = strings.TrimRight(s, ";；,，。.")
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "**") && strings.HasSuffix(s, "**") && len(s) > 4 {
		s = strings.TrimSpace(s[2 : len(s)-2])
	}
	return s
}

func isIgnoredScopeItem(s string) bool {
	if utf8.RuneCountInString(s) < 3 {
		return true
	}
	if scopeMetadataKV.MatchString(s) || scopeExclusionRe.MatchString(s) || scopeExclZhRe.MatchString(s) {
		return true
	}
	return false
}

// collectPositiveReviewTexts collects review text that proves completion, excluding clauses and sections that
// explicitly defer or mark items untouched / out of scope.
func collectPositiveReviewTexts(note, reason string, checks []ReviewCheck, alsoDone []string) []string {
	var out []string
	for _, c := range checks {
		if !c.Met {
			continue
		}
		if crit := strings.TrimSpace(c.Criterion); crit != "" && !isDeferredScopeClause(crit) {
			out = append(out, crit)
		}
		out = append(out, positiveClausesOf(c.Evidence)...)
	}
	for _, item := range alsoDone {
		if item = strings.TrimSpace(item); item != "" && !isDeferredScopeClause(item) {
			out = append(out, item)
		}
	}
	out = append(out, positiveClausesOf(note)...)
	out = append(out, positiveClausesOf(reason)...)
	return out
}

func positiveClausesOf(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []string
	section := headingNeutral
	for _, rawLine := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}
		if m := scopeHeadingRe.FindStringSubmatch(rawLine); m != nil {
			title := m[1]
			if title == "" {
				title = m[2]
			}
			section = classifyScopeHeading(title)
			continue
		}
		if section == headingDeferred || section == headingIgnore {
			continue
		}
		for _, clause := range splitClauses(trimmed) {
			if clause != "" && !isDeferredScopeClause(clause) {
				out = append(out, clause)
			}
		}
	}
	return out
}

// extractClosingDeferredScopes extracts explicit out-of-scope / untouched lists and explicit owner-confirmation
// handoff clauses recorded in the review while closing.
func extractClosingDeferredScopes(note, reason string, checks []ReviewCheck) []string {
	var out []string
	add := func(item string) {
		item = cleanScopeItem(item)
		if item == "" || utf8.RuneCountInString(item) < 3 {
			return
		}
		if !slices.ContainsFunc(out, func(existing string) bool { return sameCriterion(existing, item) }) {
			out = append(out, item)
		}
	}
	scan := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		section := headingNeutral
		for _, rawLine := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(rawLine)
			if trimmed == "" {
				continue
			}
			if m := scopeHeadingRe.FindStringSubmatch(rawLine); m != nil {
				title := m[1]
				if title == "" {
					title = m[2]
				}
				section = classifyScopeHeading(title)
				continue
			}
			if section == headingDeferred {
				if m := scopeNumberedRe.FindStringSubmatch(rawLine); m != nil {
					add(m[1])
					continue
				}
				if m := scopeBulletRe.FindStringSubmatch(rawLine); m != nil {
					add(m[1])
					continue
				}
			}
			for _, clause := range splitClauses(trimmed) {
				if hasOwnerConfirmationCondition(clause) {
					add(deferredClauseSubject(clause))
				}
			}
		}
	}
	scan(reason)
	scan(note)
	for _, c := range checks {
		scan(c.Evidence)
	}
	return out
}

// unsettledOutcomeDecisions returns effective todo_decisions rows with source "outcome" whose text records an
// owner-confirmation condition (Design note 3 of #13169: not treated as settled history).
func (s *Service) unsettledOutcomeDecisions(ctx context.Context, t Todo) []string {
	st := s.todoDB()
	if st == nil {
		return nil
	}
	rows, err := st.ListTodoDecisions(ctx, t.ID, false)
	if err != nil || len(rows) == 0 {
		return nil
	}
	var out []string
	for _, d := range rows {
		if d.Source != db.DecisionOutcome {
			continue
		}
		combined := strings.Join([]string{d.Question, d.Choice, d.Reason}, "\n")
		if !hasOwnerConfirmationCondition(combined) {
			continue
		}
		label := cleanScopeItem(d.Question)
		if label == "" || strings.EqualFold(label, "outcome") || label == "结论" || label == "结果" { // i18n-ignore: matches generic outcome question titles
			label = deferredClauseSubject(d.Choice)
		}
		if label == "" {
			label = clip(oneLine(d.Choice), 120)
		}
		if label != "" && !slices.ContainsFunc(out, func(existing string) bool { return sameCriterion(existing, label) }) {
			out = append(out, label)
		}
	}
	return out
}

// collectLinkedFollowupTexts gathers text from non-cancelled linked follow-up to-dos (pieces under t, dependents
// after t, siblings in the same request, other to-dos on the same brief, explicit ID back-links, or to-do IDs cited
// in the review) and any open asks attached to them.
func (s *Service) collectLinkedFollowupTexts(t Todo, brief TaskBrief, hasBrief bool, note, reason string, checks []ReviewCheck) []string {
	sameBrief := map[string]bool{}
	if hasBrief && brief.ID != "" {
		for _, id := range s.briefTodos(brief.ID) {
			if id != t.ID {
				sameBrief[id] = true
			}
		}
	}
	referenced := map[string]bool{}
	for _, text := range []string{note, reason} {
		for _, id := range todoIDMentionRe.FindAllString(text, -1) {
			if id != t.ID {
				referenced[id] = true
			}
		}
	}
	for _, c := range checks {
		for _, id := range todoIDMentionRe.FindAllString(c.Evidence, -1) {
			if id != t.ID {
				referenced[id] = true
			}
		}
	}

	var linked []Todo
	s.todos.mu.RLock()
	for _, other := range s.todos.todos {
		if other == nil || other.ID == t.ID {
			continue
		}
		if other.CancelledAt != nil || other.DoneBy == db.DoneByCancelled {
			continue
		}
		isLinked := other.PartOf == t.ID ||
			slices.Contains(other.After, t.ID) ||
			(t.PartOf != "" && other.PartOf == t.PartOf) ||
			sameBrief[other.ID] ||
			referenced[other.ID] ||
			strings.Contains(other.Notes, t.ID) ||
			strings.Contains(other.Title, t.ID)
		if isLinked {
			linked = append(linked, copyTodoLocked(other))
		}
	}
	s.todos.mu.RUnlock()

	var out []string
	for _, other := range linked {
		if s := strings.TrimSpace(other.Title); s != "" {
			out = append(out, s)
		}
		if s := strings.TrimSpace(other.Notes); s != "" {
			out = append(out, s)
		}
		if other.Supervision != nil {
			out = append(out, other.Supervision.DoneWhen...)
		}
		if ob, ok := s.briefOfTodo(other.ID); ok {
			if s := strings.TrimSpace(ob.Goal); s != "" {
				out = append(out, s)
			}
			out = append(out, ob.Criteria...)
		}
		if ask, ok := s.askCopy(other.ID); ok && !acceptAskStale(&ask, other) && ask.AcceptedAt.IsZero() {
			if s := strings.TrimSpace(ask.Reason); s != "" {
				out = append(out, s)
			}
			if s := strings.TrimSpace(ask.Recommend); s != "" {
				out = append(out, s)
			}
			for _, o := range ask.Options {
				if s := strings.TrimSpace(o.Label); s != "" {
					out = append(out, s)
				}
				if s := strings.TrimSpace(o.LeadsTo); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func isDeferredScopeClause(s string) bool {
	return deferredClauseRe.MatchString(s) || deferredClauseZhRe.MatchString(s)
}

func hasOwnerConfirmationCondition(s string) bool {
	return ownerConfirmRe.MatchString(s) || ownerConfirmZhRe.MatchString(s)
}

func deferredClauseSubject(clause string) string {
	s := deferredTailRe.ReplaceAllString(clause, "")
	s = deferredTailZhRe.ReplaceAllString(s, "")
	s = cleanScopeItem(s)
	if utf8.RuneCountInString(s) < 3 {
		return clip(oneLine(clause), 120)
	}
	return clip(oneLine(s), 120)
}

func splitClauses(s string) []string {
	var out []string
	var cur strings.Builder
	runes := []rune(s)
	flush := func() {
		if part := strings.TrimSpace(cur.String()); part != "" {
			out = append(out, part)
		}
		cur.Reset()
	}
	for i, r := range runes {
		switch r {
		case ';', '；', '。', '\n':
			flush()
		case '.':
			// Split on sentence-ending ". " (not decimals like 1.5 or paths like foo.go).
			prevSpace := i == 0 || unicode.IsSpace(runes[i-1])
			nextSpace := i+1 == len(runes) || unicode.IsSpace(runes[i+1])
			prevDigit := i > 0 && unicode.IsDigit(runes[i-1])
			if nextSpace && !prevSpace && !prevDigit {
				flush()
				continue
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// ---- Tokenization and coverage matching (EN + ZH) ----

type scopeTermSet struct {
	terms map[string]struct{}
}

func alreadyNamedUncovered(candidate string, existing []string) bool {
	if len(existing) == 0 {
		return false
	}
	candTokens := tokenizeTextSlice([]string{candidate})
	for _, ex := range existing {
		if sameCriterion(ex, candidate) {
			return true
		}
		if scopeCoveredBySimple(ex, []string{candidate}, candTokens) {
			return true
		}
	}
	return false
}

func scopeCoveredBySimple(scope string, texts []string, textTokens map[string]struct{}) bool {
	ts := extractScopeTerms(scope)
	return scopeCoveredBy(scope, ts, nil, texts, textTokens)
}

func scopeCoveredBy(scope string, ts scopeTermSet, siblingFreq map[string]int, texts []string, textTokens map[string]struct{}) bool {
	if len(texts) == 0 {
		return false
	}
	normScope := normalizeForSubstring(scope)
	if normScope != "" {
		for _, txt := range texts {
			if strings.Contains(normalizeForSubstring(txt), normScope) {
				return true
			}
		}
	}
	n := len(ts.terms)
	if n == 0 {
		return true // fail open when a scope carries no matchable terms
	}
	hasUnique := false
	matched := 0
	matchedUnique := 0
	for term := range ts.terms {
		unique := siblingFreq == nil || siblingFreq[term] <= 1
		if unique {
			hasUnique = true
		}
		if termPresentIn(term, textTokens) {
			matched++
			if unique {
				matchedUnique++
			}
		}
	}
	switch {
	case n == 1:
		return matched >= 1
	case n == 2:
		if matched >= 2 {
			return true
		}
		return matched == 1 && matchedUnique >= 1
	default:
		if matched < 2 {
			return false
		}
		if hasUnique && matchedUnique == 0 {
			return false
		}
		return true
	}
}

func termPresentIn(term string, textTokens map[string]struct{}) bool {
	if _, ok := textTokens[term]; ok {
		return true
	}
	if !isASCIIWord(term) || len(term) < 4 {
		return false
	}
	// Prefix stem match for related ASCII forms (e.g. clean/cleaned/cleanup, alarm/alarms, rule/rules, doc/docs/documentation).
	for tok := range textTokens {
		if !isASCIIWord(tok) || len(tok) < 4 {
			continue
		}
		short, long := term, tok
		if len(short) > len(long) {
			short, long = long, short
		}
		if strings.HasPrefix(long, short) && len(short)*2 >= len(long) {
			return true
		}
	}
	return false
}

func isASCIIWord(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return len(s) > 0
}

func normalizeForSubstring(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

func tokenizeTextSlice(texts []string) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, txt := range texts {
		for _, tok := range allTokensOf(txt) {
			tokens[tok] = struct{}{}
		}
	}
	return tokens
}

func extractScopeTerms(scope string) scopeTermSet {
	all := allTokensOf(scope)
	domain := map[string]struct{}{}
	fallback := map[string]struct{}{}
	for _, tok := range all {
		if _, stop := scopeStopWords[tok]; stop {
			continue
		}
		fallback[tok] = struct{}{}
		if _, gen := scopeGenericVerbs[tok]; gen {
			continue
		}
		domain[tok] = struct{}{}
	}
	if len(domain) > 0 {
		return scopeTermSet{terms: domain}
	}
	return scopeTermSet{terms: fallback}
}

func allTokensOf(s string) []string {
	var out []string
	var asciiBuf strings.Builder
	var hanBuf []rune
	flushASCII := func() {
		if asciiBuf.Len() > 0 {
			w := stemASCII(asciiBuf.String())
			if len(w) >= 2 {
				out = append(out, w)
			}
			asciiBuf.Reset()
		}
	}
	flushHan := func() {
		if len(hanBuf) == 0 {
			return
		}
		if len(hanBuf) == 1 {
			// Skip single stray Han characters (particles / counters).
			hanBuf = hanBuf[:0]
			return
		}
		for i := 0; i+1 < len(hanBuf); i++ {
			bg := string(hanBuf[i : i+2])
			out = append(out, bg)
		}
		hanBuf = hanBuf[:0]
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			flushHan()
			asciiBuf.WriteRune(r)
		case unicode.Is(unicode.Han, r):
			flushASCII()
			if _, drop := hanStopRune[r]; drop {
				flushHan()
				continue
			}
			hanBuf = append(hanBuf, r)
		default:
			flushASCII()
			flushHan()
		}
	}
	flushASCII()
	flushHan()
	return out
}

func stemASCII(w string) string {
	switch {
	case len(w) > 5 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 5 && strings.HasSuffix(w, "ing"):
		return w[:len(w)-3]
	case len(w) > 4 && strings.HasSuffix(w, "ed"):
		return w[:len(w)-2]
	case len(w) > 4 && strings.HasSuffix(w, "es"):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	default:
		return w
	}
}

var scopeStopWords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "to": {}, "of": {}, "in": {}, "on": {}, "for": {}, "and": {}, "or": {},
	"with": {}, "by": {}, "at": {}, "is": {}, "are": {}, "wa": {}, "were": {}, "be": {}, "been": {}, "as": {},
	"it": {}, "thi": {}, "that": {}, "these": {}, "those": {}, "from": {}, "into": {}, "up": {}, "out": {},
	"if": {}, "so": {}, "we": {}, "you": {}, "they": {}, "them": {}, "our": {}, "your": {}, "their": {},
	"should": {}, "must": {}, "can": {}, "will": {}, "would": {}, "all": {}, "each": {}, "every": {}, "any": {},
	"some": {}, "more": {}, "also": {}, "then": {}, "than": {}, "only": {}, "not": {}, "no": {}, "via": {}, "per": {},
	"round": {}, "scope": {}, "item": {}, "part": {}, "step": {}, "task": {}, "work": {},
}

var scopeGenericVerbs = map[string]struct{}{
	"fix": {}, "add": {}, "updat": {}, "update": {}, "chang": {}, "change": {}, "modif": {}, "modify": {},
	"creat": {}, "create": {}, "mak": {}, "make": {}, "handl": {}, "handle": {}, "check": {}, "verif": {}, "verify": {},
	"ship": {}, "complet": {}, "complete": {}, "finish": {}, "implement": {}, "do": {}, "run": {}, "ensur": {}, "ensure": {},
	"support": {}, "adjust": {}, "set": {}, "get": {}, "use": {}, "keep": {}, "need": {}, "want": {},
	// Chinese generic verbs/nouns (2-rune bigrams)
	"修复": {}, "修改": {}, "更新": {}, "调整": {}, "处理": {}, "完成": {}, "实现": {}, "进行": {}, "确保": {}, "支持": {}, // i18n-ignore: stop-list for Chinese scope matching
	"添加": {}, "新增": {}, "检查": {}, "验证": {}, "确认": {}, "需要": {}, "相关": {}, "部分": {}, "问题": {}, "一下": {}, // i18n-ignore: stop-list for Chinese scope matching
	"本轮": {}, "本次": {}, "范围": {}, // i18n-ignore: stop-list for Chinese scope matching
}

var hanStopRune = map[rune]struct{}{
	'的': {}, '了': {}, '和': {}, '与': {}, '及': {}, '并': {}, '在': {}, '对': {}, '把': {}, '将': {}, // i18n-ignore: Chinese particle delimiters
	'给': {}, '为': {}, '中': {}, '上': {}, '下': {}, '等': {}, '个': {}, '项': {}, '条': {}, '次': {}, // i18n-ignore: Chinese particle delimiters
	'轮': {}, '做': {}, '请': {}, '如': {}, '若': {}, '要': {}, '就': {}, '也': {}, '都': {}, '很': {}, // i18n-ignore: Chinese particle delimiters
}
