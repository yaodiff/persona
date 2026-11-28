package assistant

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Short naming calls (todo.title / agent.title / assistant.title) run on thinking models behind compatible
// gateways. When thinking is not turned off, some relays surface the model's chain of thought as ordinary text
// deltas, and the 200-token title room is spent before the real answer exists: a cut-off monologue first line such
// as "We need answer title" used to be written as the title. The calls now ask each protocol to disable thinking;
// these guards are the second line of defense for relays that ignore the switch. A rejected candidate never
// overwrites a title: callers keep the title they already have (todo/agent) or use their deterministic fallback
// (assistant).
//
// The classifier is deliberately conservative. Matching a monologue opener ("we need", "I should", "the user", …)
// alone is not enough, because real titles can start that way ("We need new icons", "I should answer Sarah's
// email"). The line must additionally carry at least two independent meta-reasoning signals: words that refer to
// producing the answer/title, a grammatical shape specific to telegraphic self-instruction, or a generation
// constraint. The tests pin a corpus of ordinary Chinese, English and Japanese titles that must pass.

// titleThinkBlock is a complete <think>…</think> / <thinking>…</thinking> block, removed with its content:
// reasoning text must never become a title.
var titleThinkBlock = lazyre.New(`(?is)<(?:think|thinking)\b[^>]*>.*?</(?:think|thinking)\s*>`)

// titleThinkOpenTail is an unclosed reasoning opening tag and everything after it (a reply cut off inside
// reasoning): there is no trustworthy title after it.
var titleThinkOpenTail = lazyre.New(`(?is)<(?:think|thinking)\b[^>]*>.*$`)

// titleThinkingTag matches any reasoning tag residue, including a truncated one, after the stripping above.
var titleThinkingTag = lazyre.New(`(?i)<\s*/?\s*(?:think|thinking)\b`)

// stripTitleThinking removes explicit reasoning blocks from a naming reply. Text without the tags is returned
// unchanged.
func stripTitleThinking(s string) string {
	s = titleThinkBlock.ReplaceAllString(s, " ")
	s = titleThinkOpenTail.ReplaceAllString(s, "")
	return s
}

// firstTitleLine is the first non-empty trimmed line of a model reply (the line every title path names from).
func firstTitleLine(s string) string {
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// titleLeadingLabel is a model-added output label ("Title: …", "output: …"). It is stripped before classifying,
// so "Title: Q4 plan" is judged as "Q4 plan" and kept rather than rejected for its label.
var titleLeadingLabel = lazyre.New(`^\s*(?i:title|subject|headline|name|output|result|answer)\s*[:：]\s*`) // i18n-ignore: matches the model's output label

// titleCOTOpenerEN matches the start of an English line where the model is addressing itself or narrating the
// user's request, not naming the work. Word boundaries keep "The user manual" matching only as an opener; the
// meta-signal gate does the actual discrimination.
var titleCOTOpenerEN = lazyre.New(`^\s*(?i:we(?:\s+all)?\s+(?:need|needed|must|should|have to|got to|gotta)|` +
	`the user\b|let'?s\b|i\s+(?:need|needed|must|should|have to|got to|gotta)|` +
	`okay\s*,|ok\s*,|alright\s*,|first\s*[,.:]|first\s+(?:i|we)\b|now\s+(?:i|we)\b)`)

// titleCOTOpenerZH is the Chinese counterpart. A bare "需要" is an opener candidate too; like every opener it
// only counts together with meta-reasoning signals, so "需要购买机票" stays a title.
var titleCOTOpenerZH = lazyre.New(`^\s*(?:(?:我们|我|咱)(?:需要|得|应该|必须)|咱们需要|让我们|首先|好[的吧]?\s*[，,]|现在(?:我们|我)?(?:先)?|需要|得先)`) // i18n-ignore: matches the model's chain-of-thought openers

// titleTelegraphic matches the ungrammatical telegraphic shape of cut-off self-instruction ("we need answer
// only", "I need answer title", "we need need output"): the bare meta word directly after "need" (real titles
// put "to", an article or a concrete modifier there: "we need to reply", "we need an answer", "we need design
// output") and at most a small meta/constraint tail before the line ends. It is one structural signal; the line
// also needs a meta word.
var titleTelegraphic = lazyre.New(`(?i)\b(?:we|i|you|they)\s+need\s+(?:need\s+)?(?:answer|output|reply|respond)\b` +
	`(?:\s+(?:only|just|directly|now|please|title|json|yaml|format|a\s+title|the\s+title))*\s*[.!?。！？]?\s*$`) // i18n-ignore: sentence ends in the model's reply

// titleGenCueEN is a constraint on how the model must produce the title/answer: wording almost only chain-of-
// thought uses.
var titleGenCueEN = lazyre.New(`(?i)\b(?:one|single)[ -]?line\b|\bno explanation\b|\bwithout explanation\b` +
	`|\bno extra(?: text| words| output)?\b|\bnothing else\b|\bonly the title\b|\btitle only\b` +
	`|\b\d{1,3}\s*(?:chars?|characters?)\b`)

// titleGenCueZH is the Chinese counterpart of titleGenCueEN.
var titleGenCueZH = lazyre.New(`只输出|直接(?:给|输出|返回)|不要解释|无需解释|别解释|一行|不超过\s*\d+\s*字|\d+\s*字以内`) // i18n-ignore: matches the model's generation constraints

// titleMetaWordsEN are exact English tokens referring to the model's own output task. Exact-token matching keeps
// "answers" (a real team might provide them) distinct from the bare CoT form "answer".
var titleMetaWordsEN = map[string]struct{}{
	"answer":    {},
	"output":    {},
	"reply":     {},
	"response":  {},
	"respond":   {},
	"title":     {},
	"heading":   {},
	"headline":  {},
	"json":      {},
	"yaml":      {},
	"format":    {},
	"formatted": {},
	"reasoning": {},
	"thinking":  {},
}

// titleMetaWordsZH are Chinese substrings with the same meta-reference role. "一行" stays only in the constraint
// pattern: counting it both as a meta word and as a cue would evaluate the same occurrence twice.
var titleMetaWordsZH = []string{"标题", "题目", "答案", "回答", "回复", "输出", "格式", "解释", "思考", "结论", "分析"} // i18n-ignore: matched in the model's reply, not display copy

// titleLineIsThinking reports whether one title-sized first line is a cut-off chain-of-thought fragment.
func titleLineIsThinking(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	// A leading output label carries no meaning of its own; judge what follows it.
	if loc := titleLeadingLabel.FindStringIndex(line); loc != nil && loc[1] < len(line) {
		line = strings.TrimSpace(line[loc[1]:])
	}
	if titleMonologueLeak(line) {
		return true
	}
	// The assistant title format prefixes the topic with a known kind ("功能 | …") and possibly a day
	// ("1102 | 修复 | …"): the leak sits in the bar-delimited topic, so judge the last segment too.
	if i := strings.LastIndexAny(line, "|｜"); i >= 0 { // i18n-ignore: the title format's full-width bar
		_, size := utf8.DecodeRuneInString(line[i:])
		if titleLineIsThinking(line[i+size:]) {
			return true
		}
	}
	return false
}

// titleMonologueLeak is titleLineIsThinking without label or bar-segment handling.
func titleMonologueLeak(line string) bool {
	if line == "" {
		return false
	}
	low := strings.ToLower(line)
	opener := titleCOTOpenerEN.MatchString(line) || titleCOTOpenerZH.MatchString(line)
	if !opener {
		return false
	}
	meta := 0
	seenMeta := map[string]struct{}{}
	for _, w := range titleWordsEN(low) {
		if _, ok := titleMetaWordsEN[w]; ok {
			seenMeta[w] = struct{}{}
		}
	}
	meta += len(seenMeta)
	for _, w := range titleMetaWordsZH {
		if strings.Contains(line, w) {
			meta++
		}
	}
	// Two distinct output-task references are already meta-reasoning; with only one, a structural corruption or a
	// generation constraint must corroborate it, so "I should answer Sarah's email" stays safe.
	if meta >= 2 {
		return true
	}
	structural := titleTelegraphic.MatchString(line) ||
		titleGenCueEN.MatchString(line) || titleGenCueZH.MatchString(line) ||
		titleAdjacentRepeat(titleWordsEN(low))
	return meta >= 1 && structural
}

// titleReplyIsThinkingLeak judges a whole naming reply after stripTitleThinking: residual reasoning-tag syntax,
// or a meta-reasoning first line.
func titleReplyIsThinkingLeak(stripped string) bool {
	if titleThinkingTag.MatchString(stripped) {
		return true
	}
	return titleLineIsThinking(firstTitleLine(stripped))
}

// titleWordsEN returns the lowercase letter runs of s as words.
func titleWordsEN(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
}

// titleAdjacentRepeat reports two equal neighboring words ("we need need output"), a frequent truncation
// dysfluency of generated monologue.
func titleAdjacentRepeat(words []string) bool {
	for i := 1; i < len(words); i++ {
		if len(words[i]) >= 2 && words[i] == words[i-1] {
			return true
		}
	}
	return false
}
