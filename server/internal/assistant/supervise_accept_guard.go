package assistant

import (
	"context"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Guarding a legacy bare `task_review accepted` (#12026, with scope coverage in #13169). A supervised task must not vanish from the owner's Needs-you list
// when its result does not settle anything: the agent was blocked on information only the owner has, its closeout
// status was not done, or a read-only evaluation / research came back inconclusive, ineffective or only partly done.
// Such an accept is refused with the fixes that keep the task with the owner: needs_user (owner-only information),
// accepted with user_check (the owner reads the result and passes it before it leaves Needs you, #3240), or a linked
// follow-up to-do. The hub judges words, not intent: the signals below are deliberately specific, and the refusal
// names which one matched, so a supervisor that disagrees can still close the loop the named way instead of being
// silently overridden.

// bareAcceptGuardError runs after the current done_when checks have been validated. A declared contract with
// required checks gives the review structured evidence. The open owner request, findings, delivery and deferred
// scope gates still apply to that review.
func (s *Service) bareAcceptGuardError(ctx context.Context, t Todo, note, reason string, checks []ReviewCheck, alsoDone []string) error {
	if t.Supervision != nil && len(t.Supervision.DoneWhen) > 0 {
		brief, _ := s.committedBriefOfTodo(t.ID)
		if brief.workModeDeclared() || brief.CompletionModeNow.Valid() {
			a, readable, ok := s.askCopyReadable(t.ID)
			if !readable {
				return errors.New("bare accepted refused: the owner request ledger could not be read. Retry the review")
			}
			if ok && a.AcceptedAt.IsZero() && !acceptAskStale(&a, t) && !acceptAskEnded(&a, t) {
				return errors.New("bare accepted refused: the owner request is still open. Resolve it or withdraw an unneeded decision before accepting")
			}
			return nil
		}
	}
	if matched, _ := acceptGuardBlock(s.acceptGuardEvidence(ctx, t, note, reason, checks)); matched != "" {
		return errors.New(acceptGuardRefusal(matched))
	}
	if uncovered := s.acceptGuardUncoveredScopes(ctx, t, note, reason, checks, alsoDone); len(uncovered) > 0 {
		return errors.New(acceptScopeGuardRefusal(uncovered))
	}
	return nil
}

// Signals that the result does not settle the task. English and Chinese, word-bounded where the language has words.
// Each pattern is one line of evidence in the refusal; keep them specific enough that an ordinary "done" note cannot
// match (e.g. "inconclusive" matches, "conclusive" does not).
var (
	// The result itself says it proves nothing or only partly works.
	acceptInconclusiveRe = lazyre.New(`(?i)\b(inconclusive|not conclusive|does not work|doesn'?t work|did not work|` +
		`no (?:measurable |visible |real )?effect|not effective|ineffective|partially (?:done|effective|working)|` +
		`partial (?:result|fix)|mixed results?|cannot confirm|could not confirm|unable to (?:confirm|prove|verify)|` +
		`still (?:retrieves?|shows?|fails?|wrong))\b`)
	acceptInconclusiveZhRe = lazyre.New(`不确定|没法确定|无法确定|没有结论|得不出结论|不成立|没效果|没有效果|无效果|` + // i18n-ignore: matches the original report text
		`不起作用|没有作用|部分(?:有效|完成|生效|成功)|只(?:完成|做|生效|成功)了?一?半|有待(?:验证|确认)|` + // i18n-ignore: matches the original report text
		`仍然(?:错误|不对|失败)|还是(?:错误|不对|失败)`) // i18n-ignore: matches the original report text
	// The work is blocked on something only the owner can give or decide.
	acceptOwnerBlockerRe = lazyre.New(`(?i)\b(blocked on|waiting on the (?:owner|user)|only the (?:owner|user) (?:knows|can)|` +
		`needs? (?:the |an? )?(?:owner|user)(?:'s)? (?:input|info|information|decision|approval|to)|` +
		`(?:owner|user) (?:has|needs) to (?:provide|confirm|supply|choose|decide)|` +
		`please (?:provide|confirm|share|send)\b[^。\n]{0,60}(?:id|title|time|account|link|url|address))\b`) // i18n-ignore: matches the original report's sentence boundaries
	acceptOwnerBlockerZhRe = lazyre.New(`只有用户(?:知道|能|才有)|用户(?:才)?(?:知道|能|需要)(?:提供|确认|给|决定|拍板|补充)|` + // i18n-ignore: matches the original report text
		`要用户(?:提供|确认|决定|拍板|补充|给)|等用户(?:提供|确认|决定|拍板|回复)|` + // i18n-ignore: matches the original report text
		`请(?:用户|你)(?:提供|确认|给|发|补充)[^。\n]{0,40}(?:ID|id|标题|时间|账号|链接|地址)|` + // i18n-ignore: matches the original report text
		`卡在用户|缺(?:少|的是)用户(?:才知道)?(?:的)?(?:信息|资料)`) // i18n-ignore: matches the original report text
	// The agent's own closeout report (its work report's Status line) says the work is not finished or is blocked.
	// Only an explicit non-done status matches: runtimes that write no report must never block an accept.
	acceptWaitingRe    = lazyre.New(`(?im)^[ \t]*[-*][ \t]*Waiting[ \t]*[:：][ \t]*([^\r\n]*)`)             // i18n-ignore: matches the original report
	acceptCloseoutRe   = lazyre.New(`(?im)^\s*[-*]\s*Status\s*[:：]\s*(not_done|blocked|waiting|unsure)\b`) // i18n-ignore: matches the original report's status delimiters
	acceptCloseoutZhRe = lazyre.New(`(?m)^\s*[-*]\s*状态\s*[:：]\s*(没做完|未完成|卡住|阻塞|受阻|等待中|在等)`)                // i18n-ignore: matches the original report text
)

// acceptGuardEvidence is the text a bare-accept guard scans: what the supervisor put in the review and, best effort,
// what its agents said last. Agent text is added only when the hub can read it right now; an offline node never blocks
// an accept by itself.
func (s *Service) acceptGuardEvidence(ctx context.Context, t Todo, note, reason string, checks []ReviewCheck) string {
	var b strings.Builder
	for _, part := range []string{note, reason} {
		if part = strings.TrimSpace(part); part != "" {
			b.WriteString(part)
			b.WriteByte('\n')
		}
	}
	for _, c := range checks {
		b.WriteString(c.Criterion)
		b.WriteByte('\n')
		b.WriteString(c.Evidence)
		b.WriteByte('\n')
	}
	if replies, sideBlocked, _ := s.taskReplies(ctx, t); !sideBlocked {
		for _, r := range replies {
			b.WriteString(r.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// acceptGuardBlock says why a bare accepted must not close task t, "" when the result settles the task. matched names
// the signal that fired, for the refusal message and the tests.
func acceptGuardBlock(evidence string) (matched, why string) {
	for _, line := range acceptWaitingRe.FindAllStringSubmatch(evidence, -1) {
		switch strings.ToLower(strings.TrimSpace(line[1])) {
		case "", "none", "n/a", "-", "—":
		default:
			return "closeout_waiting", "the agent's closeout lists unfinished follow-up in Waiting"
		}
	}
	switch {
	case acceptCloseoutRe.MatchString(evidence) || acceptCloseoutZhRe.MatchString(evidence):
		return "closeout_not_done", "the agent's closeout status is not done (not finished, blocked or waiting)"
	case acceptOwnerBlockerRe.MatchString(evidence) || acceptOwnerBlockerZhRe.MatchString(evidence):
		return "owner_only", "the result is blocked on information or a decision only the owner has"
	case acceptInconclusiveRe.MatchString(evidence) || acceptInconclusiveZhRe.MatchString(evidence):
		return "inconclusive", "the result is inconclusive, ineffective or only partly done and still needs a next step"
	}
	return "", ""
}

// acceptGuardRefusal is the error text for a refused bare accept, saying exactly how to close the loop instead.
func acceptGuardRefusal(matched string) string {
	base := "bare accepted refused: "
	switch matched {
	case "closeout_waiting":
		base += "the agent’s closeout lists unfinished follow-up in Waiting. Resolve the wait or use needs_user when only the owner can unblock it"
	case "closeout_not_done":
		base += "the agent's own closeout says the work is not done. Re-review with verdict rework to send the gap back " +
			"to the agent, or needs_user when only the owner can unblock it (why_owner, reason, recommend, options)"
	case "owner_only":
		base += "the task is blocked on something only the owner knows or can decide. Re-review with verdict needs_user " +
			"(why_owner: only_owner_knows; reason: what the owner is to provide or decide; recommend: the option you suggest), " +
			"so it stays in their Needs-you list"
	case "inconclusive":
		base += "the result does not settle the task. Re-review with accepted plus user_check: true (the owner reads the " +
			"result and passes it before it leaves Needs you; the default for read-only research and evaluations), or " +
			"needs_user with the next-step options, or hand out the remaining work as a linked follow-up to-do and then accept"
	default:
		base += "the task's result does not settle it. Re-review with needs_user, or accepted with user_check: true, or link a follow-up to-do"
	}
	return base + ". A question asked only in chat text never reaches Needs you."
}
