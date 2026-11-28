package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Closing a finished task (docs/assistant/todos.md "Automatic completion", #1092). When a task's agents have all stopped and its receipt
// went out, the judge model reads the task's title, the prompt it was handed out with and what its agents said last,
// and only "done" closes the todo (done_by auto, with why). Anything else — not finished, waiting on someone,
// needing the user, unsure, no model, a model error, an agent whose turn the model service cut off (#1516) — leaves
// it open. A todo the user reopened is not closed again by the same stretch of work. A supervised task (supervise.go)
// is not closed at all: finished, it waits for its supervisor's review. While assistant.done_needs_delivery is on (off
// by default), done also needs the work live and checked (or nothing to ship): one done but held at a PR, merged or off
// by default stays open as undelivered, with where it is held (delivery.go). Every verdict is kept on the todo as its
// check (task_check.go), so an open task can say why it is not done.

const (
	verdictDone      = "done"
	verdictNotDone   = "not_done"
	verdictWaiting   = "waiting"
	verdictNeedsUser = "needs_user"
	verdictUnsure    = "unsure"
	// verdictUndelivered: done as asked, but its work is not live yet (delivery.go); the hub's own, not the model's.
	verdictUndelivered = "undelivered"
)

// taskDoneTimeout bounds one judgment (reading the replies and the model).
var taskDoneTimeout = 90 * time.Second

// taskDoneRetryAfter is how long a failed judgment waits before it is asked once more.
var taskDoneRetryAfter = 3 * time.Second

// taskDoneSignals are cheap hints found in the replies, handed to the model (never enough on their own).
type taskDoneSignals struct {
	MergedPR bool `json:"merged_pr"`
	Build    bool `json:"build"`
}

var (
	mergedWords  = lazyre.New(`(?i)\bmerged\b|\bsquash(ed)?\b|已合并|合并了|已合入|合入了|自行合并`)
	buildNumbers = lazyre.New(`(?i)testflight[^\n]{0,40}?\d{3,}|构建号?\s*[:：#]?\s*\d{3,}|\bbuild\s*#?\s*\d{3,}`)
)

type taskDoneReply struct {
	prompts.TaskDoneReply
	pr         string
	otherPR    bool
	structured bool
}

func doneSignals(replies []taskDoneReply) taskDoneSignals {
	var s taskDoneSignals
	for _, r := range replies {
		if r.structured {
			continue
		}
		if r.pr != "" && !r.otherPR && said(mergedWords, r.Text) {
			s.MergedPR = true
		}
		if buildNumbers.MatchString(r.Text) {
			s.Build = true
		}
	}
	return s
}

const ownerSentenceEnds = ".!?;。！？；" // i18n-ignore: sentence punctuation for parser markers, not display copy

// ownerNoAction reports a complete no-action predicate after an owner marker. The sentence or clause must end there:
// "do nothing else but approve" is still an action, while an independent requirement after a period is handled by the
// next marker occurrence rather than being swallowed with this one.
func ownerNoAction(rest string) bool {
	rest = strings.TrimSpace(rest)
	for _, phrase := range []string{"do nothing else", "do nothing"} {
		if !strings.HasPrefix(rest, phrase) {
			continue
		}
		tail := strings.TrimSpace(rest[len(phrase):])
		terminator, _ := utf8.DecodeRuneInString(tail)
		if tail == "" || strings.ContainsRune(ownerSentenceEnds, terminator) {
			return true
		}
	}
	return false
}

// ownerContrastOffsets returns unquoted, word-boundary `but` conjunctions in [at,end). A comma before one is
// optional. Keeping these boundaries occurrence-local lets a condition govern only its own clause.
func ownerContrastOffsets(text, lower string, at, end int) []int {
	var out []int
	for from := at; from < end; {
		i := strings.Index(lower[from:end], "but")
		if i < 0 {
			break
		}
		i += from
		left := i > at && (lower[i-1] == ' ' || lower[i-1] == '\t' || lower[i-1] == ',')
		right := i+3 < end && (lower[i+3] == ' ' || lower[i+3] == '\t')
		clause := left && right && ownerButStartsClause(text, lower, at, i, end)
		if clause && !ownerMarkerQuoted(lower, i) {
			out = append(out, i)
		}
		from = i + 3
	}
	return out
}

// ownerButStartsClause distinguishes a genuine contrast from an exception phrase. An immediately following owner
// marker has no words in this slice and is a clause by construction. Otherwise `but` is a contrast by default, except
// for an indefinite-pronoun exception phrase or the "nothing … but …" idiom; a finite verb at the end of a
// determiner-led subject keeps that subject a clause, while a verb-like noun modifier does not create one.
func ownerButStartsClause(text, lower string, at, i, end int) bool {
	suffix := strings.TrimSpace(lower[i+3 : end])
	if suffix == "" {
		return true
	}
	before := strings.Fields(lower[at:i])
	local := ""
	if len(before) > 0 {
		index := len(before) - 1
		local = strings.Trim(before[index], ",:")
		if local == "else" && index > 0 {
			index--
			local = strings.Trim(before[index], ",:")
		}
		if local == "one" && index > 0 && strings.Trim(before[index-1], ",:") == "no" {
			local = "no one"
		}
	}
	switch local {
	case "nothing", "anything", "everything", "something",
		"everyone", "anyone", "someone", "everybody", "anybody", "somebody", "nobody", "none", "noone", "no one", "all", "both":
		// A local exception phrase; a finite clause below can still make it a contrast.
	default:
		return true
	}
	words := strings.Fields(suffix)
	if len(words) == 0 {
		return true
	}
	properSubject := false
	if len(text) == len(lower) { // equal byte lengths keep the case-folded offsets aligned
		original := strings.Fields(strings.TrimSpace(text[i+3 : end]))
		properSubject = len(original) == len(words) && len(original) > 0 && original[0] != strings.ToLower(original[0])
	}
	return ownerFiniteClauseShape(words, properSubject)
}

// ownerFiniteClauseShape looks for a predicate after the subject, rather than treating every inflected token as a
// verb. The subject head survives intervening modifiers and may be a pronoun or a nominal. Non-finite be forms do not
// establish a predicate, and final verbs follow the same agreement rules as verbs with an object.
func ownerFiniteClauseShape(words []string, properSubject bool) bool {
	if len(words) == 0 {
		return false
	}
	start := 0
	subject := words[0]
	trackHead := true
	bareNominal := false
	switch words[0] {
	case "the", "a", "an":
		if len(words) < 3 {
			return false
		}
		subject = words[1]
		start = 2
	case "i", "you", "we", "he", "she", "they", "it", "this", "that", "these", "those":
		trackHead = false
		start = 1
	default:
		if len(words) < 2 {
			return false
		}
		bareNominal = true
		start = 1
	}
	for i := start; i < len(words); i++ {
		word := words[i]
		if i < len(words)-1 {
			if !ownerPostmodifiedNoun(words, i, subject) && ownerLexicalVerb(word) && ownerPredicateAgrees(subject, word) &&
				(!bareNominal || properSubject || (ownerBarePluralSubject(subject) && !ownerThirdPersonInflection(word))) {
				return true
			}
			if trackHead && !ownerAdverb(word) && !ownerNonFiniteModifier(word) {
				subject = word
			}
		}
		if i == len(words)-1 {
			// A lowercase bare nominal needs a base-form predicate to prove a plural-subject clause
			// ("owners need approval"). Capitalized proper nouns also accept an inflected predicate.
			if ownerFinalIntransitive(word) && ownerPredicateAgrees(subject, word) &&
				(!bareNominal || properSubject || (ownerBarePluralSubject(subject) && !ownerThirdPersonInflection(word))) {
				return true
			}
			if ownerBaseIntransitive(word) && ownerBaseSubject(subject) &&
				(!bareNominal || properSubject || ownerBarePluralSubject(subject)) {
				return true
			}
		}
	}
	return false
}

func ownerAdverb(word string) bool {
	switch word {
	case "still", "also", "already", "just", "now", "then", "often", "never", "always", "sometimes", "usually":
		return true
	}
	return strings.HasSuffix(word, "ly")
}

func ownerNonFiniteModifier(word string) bool {
	switch word {
	case "be", "been", "being":
		return true
	}
	return strings.HasSuffix(word, "ed") || strings.HasSuffix(word, "ing")
}

// ownerPostmodifiedNoun keeps an s-final noun head with a following prepositional modifier in the noun phrase.
func ownerPostmodifiedNoun(words []string, i int, subject string) bool {
	if !ownerPluralNoun(subject) || i+1 >= len(words) {
		return false
	}
	switch words[i+1] {
	case "in", "on", "at", "for", "with", "without", "under", "over", "before", "after", "during", "by", "from", "to", "of":
		return true
	}
	return false
}

func ownerLexicalVerb(word string) bool {
	if ownerFiniteInflection(word) {
		return true
	}
	switch word {
	case "need", "wait", "fail", "work", "remain", "stay", "go", "come", "run", "stop", "start", "finish",
		"complete", "continue", "approve", "reject", "roll", "notify", "check", "verify", "decide", "determine",
		"review", "ask", "inspect", "assess", "require", "block", "allow", "depend", "expect", "include", "contain",
		"show", "say", "tell", "report", "return", "look", "seem", "appear", "make", "take", "use", "get", "give",
		"want", "read", "succeed", "deploy", "merge", "publish", "ship", "restart", "retry", "cancel", "delete",
		"create", "write", "update", "edit", "fix", "build", "test", "execute", "install", "remove", "send", "save",
		"commit", "push", "pull", "open", "close":
		return true
	}
	return false
}

func ownerFinalIntransitive(word string) bool {
	switch word {
	case "am", "is", "are", "was", "were",
		"do", "does", "did", "have", "has", "had", "can", "could", "will", "would", "shall", "should", "may", "might", "must",
		"waits", "fails", "remains", "stays", "goes", "comes", "runs", "stops", "starts", "finishes", "completes",
		"continues", "rolls", "depends", "looks", "seems", "appears", "succeeds":
		return true
	}
	return false
}

func ownerBaseIntransitive(word string) bool {
	switch word {
	case "wait", "fail", "remain", "stay", "go", "come", "run", "stop", "start", "finish", "complete", "continue",
		"roll", "depend", "look", "seem", "appear", "succeed":
		return true
	}
	return false
}

func ownerPluralNoun(word string) bool {
	return strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") && word != "this" && word != "is"
}

// ownerBaseSubject reports a subject that takes a base-form lexical verb: a plural noun or one of the pronouns that
// uses base-form agreement. Singular nouns and this/that/it take an inflected verb instead.
func ownerBaseSubject(word string) bool {
	switch word {
	case "i", "you", "we", "they", "these", "those":
		return true
	}
	return ownerPluralNoun(word)
}

func ownerBasePronoun(word string) bool {
	switch word {
	case "i", "you", "we", "they", "these", "those":
		return true
	}
	return false
}

// ownerBarePluralSubject is deliberately narrower than ownerBaseSubject: in a bare exception phrase, an -ous
// adjective or a possessive pronoun is a modifier, not evidence of a plural clause subject.
func ownerBarePluralSubject(word string) bool {
	if strings.HasSuffix(word, "ous") {
		return false
	}
	switch word {
	case "his", "its", "ours", "yours", "theirs", "hers":
		return false
	}
	return ownerPluralNoun(word)
}

func ownerAuxiliary(word string) bool {
	switch word {
	case "am", "is", "are", "was", "were",
		"do", "does", "did", "have", "has", "had", "can", "could", "will", "would", "shall", "should", "may", "might", "must":
		return true
	}
	return false
}

// ownerPredicateAgrees enforces pronoun agreement strictly. Nominal number is only evidence for a base-form verb;
// an inflected verb instead disambiguates singular s-final nouns such as analysis, status and series.
func ownerPredicateAgrees(subject, verb string) bool {
	if ownerAuxiliary(verb) {
		switch verb {
		case "was":
			return subject == "i" || !ownerBasePronoun(subject)
		case "is", "does", "has":
			return !ownerBasePronoun(subject)
		case "are", "were", "do", "have":
			return ownerBaseSubject(subject)
		default:
			return true
		}
	}
	if ownerFiniteInflection(verb) {
		return !ownerBasePronoun(subject)
	}
	return ownerBaseSubject(subject)
}

func ownerFiniteInflection(word string) bool {
	switch word {
	case "am", "is", "are", "was", "were",
		"do", "does", "did", "have", "has", "had",
		"can", "could", "will", "would", "shall", "should", "may", "might", "must",
		"needs", "waits", "fails", "works", "remains", "stays", "goes", "comes", "runs", "stops", "starts",
		"finishes", "completes", "continues", "approves", "rejects", "rolls", "notifies", "checks", "verifies",
		"decides", "determines", "reviews", "asks", "inspects", "assesses", "requires", "blocks", "allows",
		"depends", "expects", "includes", "contains", "shows", "says", "tells", "reports", "returns", "looks",
		"seems", "appears", "makes", "takes", "uses", "gets", "gives", "wants", "reads", "succeeds":
		return true
	}
	return false
}

// ownerThirdPersonInflection excludes base auxiliaries such as have/do/are, which also occur in the finite-verb list.
func ownerThirdPersonInflection(word string) bool {
	switch word {
	case "is", "does", "has":
		return true
	}
	return ownerFiniteInflection(word) && !ownerAuxiliary(word)
}

// ownerMarkerAfterContrast reports an owner marker immediately following an unquoted `but`, with optional whitespace.
func ownerMarkerAfterContrast(text, lower string, at int) bool {
	contrasts := ownerContrastOffsets(text, lower, 0, at)
	return len(contrasts) > 0 && strings.TrimSpace(lower[contrasts[len(contrasts)-1]+3:at]) == ""
}

// ownerWithoutQuotes blanks quoted text while preserving unquoted punctuation and condition words.
func ownerWithoutQuotes(text string) string {
	straight, curly, corner := false, 0, 0
	return strings.Map(func(r rune) rune {
		switch r {
		case '"':
			straight = !straight
			return ' '
		case '“':
			curly++
			return ' '
		case '”':
			if curly > 0 {
				curly--
			}
			return ' '
		case '「':
			corner++
			return ' '
		case '」':
			if corner > 0 {
				corner--
			}
			return ' '
		}
		if straight || curly > 0 || corner > 0 {
			return ' '
		}
		return r
	}, text)
}

// ownerDecimalPoint reports a full stop between two ASCII digits, as in a version or decimal number.
func ownerDecimalPoint(text string, at int) bool {
	return at > 0 && at+1 < len(text) && text[at] == '.' &&
		text[at-1] >= '0' && text[at-1] <= '9' && text[at+1] >= '0' && text[at+1] <= '9'
}

// ownerPreviousSentenceEnd returns the last sentence terminator before the clause, ignoring decimal points.
func ownerPreviousSentenceEnd(text string) int {
	for i := len(text); i > 0; {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		i -= size
		if strings.ContainsRune(ownerSentenceEnds, r) && !ownerDecimalPoint(text, i) {
			return i
		}
	}
	return -1
}

// ownerSentenceEnd returns the first unquoted sentence terminator at or after at, preserving original byte offsets.
func ownerSentenceEnd(lower string, at int) int {
	straight, curly, corner := false, 0, 0
	for i := at; i < len(lower); {
		r, size := utf8.DecodeRuneInString(lower[i:])
		switch r {
		case '"':
			straight = !straight
		case '“':
			curly++
		case '”':
			if curly > 0 {
				curly--
			}
		case '「':
			corner++
		case '」':
			if corner > 0 {
				corner--
			}
		default:
			if !straight && curly == 0 && corner == 0 && strings.ContainsRune(ownerSentenceEnds, r) && !ownerDecimalPoint(lower, i) {
				return i
			}
		}
		i += size
	}
	return len(lower)
}

var (
	ownerMarkdownListPrefix = lazyre.New(`^\s*(?:[-+*]\s+|\d{1,9}[.)]\s+)`)
	ownerMarkdownEmphasis   = strings.NewReplacer("**", "", "__", "", "*", "", "_", "")
)

// ownerMarkdownLine removes presentational list and emphasis markers before the same sentence-boundary analysis runs.
// It does not strip block quotes: those quote somebody else's words rather than presenting a new owner requirement.
func ownerMarkdownLine(text string) string {
	text = ownerMarkdownListPrefix.ReplaceAllString(strings.TrimSpace(text), "")
	return ownerMarkdownEmphasis.Replace(text)
}

// directOwnerMarker reports whether any unquoted occurrence of marker starts a direct requirement at the line or a
// sentence/clause boundary. Subordinate and narrative uses ("if …", "evaluates whether …") are not owner actions;
// skipping one such occurrence must not suppress a later direct one. A comma inside that same if-sentence does not end
// its conditional scope; a new sentence or an independent "otherwise" clause does (#5732).
func directOwnerMarker(text, marker string) bool {
	text = ownerMarkdownLine(text)
	lower := strings.ToLower(text)
	for from := 0; from < len(lower); {
		i := strings.Index(lower[from:], marker)
		if i < 0 {
			return false
		}
		i += from
		before := strings.TrimSpace(lower[:i])
		afterContrast := ownerMarkerAfterContrast(text, lower, i)
		direct := before == "" || before == "otherwise" || strings.HasSuffix(before, " otherwise") || afterContrast
		terminatorBefore := strings.TrimRight(before, "\"”」") // i18n-ignore: parser delimiters, not display copy; a closing quote does not hide the sentence's own terminator
		terminator, _ := utf8.DecodeLastRuneInString(terminatorBefore)
		direct = direct || strings.ContainsRune(ownerSentenceEnds, terminator)
		if afterContrast {
			clause := ownerWithoutQuotes(before)
			end := ownerPreviousSentenceEnd(clause)
			// A contrast does not close a leading if-sentence. A trailing condition, by contrast, owns only its own
			// clause, so the marker after its `but` remains an independent requirement.
			direct = !ownerConditionalOpen(strings.TrimSpace(clause[end+1:]))
		} else if strings.HasSuffix(before, ",") || strings.HasSuffix(before, ":") {
			clause := ownerWithoutQuotes(before)
			end := ownerPreviousSentenceEnd(clause)
			conditional := strings.TrimSpace(clause[end+1:])
			// Commas and colons introduce guidance without closing an "if" elsewhere in this sentence. Only a
			// sentence boundary or an explicit "otherwise" clause makes the next requirement independent.
			openConditional := ownerConditionalOpen(conditional)
			direct = !openConditional || strings.HasSuffix(clause, "otherwise,") || strings.HasSuffix(clause, "otherwise:")
		}
		rest := i + len(marker)
		if direct && !ownerNoAction(lower[rest:]) && !ownerTrailingConditional(text, lower, rest) && !ownerMarkerQuoted(lower, i) {
			return true
		}
		from = i + len(marker)
	}
	return false
}

// ownerConditionalOpen reports a clause-level "if" or "only if" whose condition still owns the requirement. An
// introductory colon or short discourse marker may precede it; a narrative verb's nested if-question ("checks if",
// "checks only if") may not suppress the following direct clause. "Even if" is different: its main clause applies
// regardless of the condition, so a later owner requirement stays direct.
func ownerConditionalOpen(clause string) bool {
	for from := 0; from < len(clause); {
		i := strings.Index(clause[from:], "if ")
		if i < 0 {
			return false
		}
		i += from
		prefix := strings.TrimSpace(clause[:i])
		boundary := prefix == "" || prefix == "only" || strings.HasSuffix(prefix, ":") || strings.HasSuffix(prefix, ": only")
		intro := strings.TrimSpace(strings.TrimSuffix(prefix, ","))
		switch intro {
		case "so", "then", "therefore", "and", "but", "now", "next":
			boundary = true
		}
		if boundary && !strings.HasSuffix(prefix, "even") {
			return true
		}
		from = i + 2
	}
	return false
}

// ownerTrailingConditional reports an if/only-if condition attached to the requirement starting at at. Its scope ends
// at the sentence boundary, so it cannot hide a later independent requirement. An if-question that is the
// requirement's own object ("check if …") is not a condition, and "even if" leaves the requirement unconditional;
// either owns later coordinated if-clauses. Quoted if-clauses are history, not conditions on an unquoted requirement.
func ownerTrailingConditional(text, lower string, at int) bool {
	end := ownerSentenceEnd(lower, at)
	const marker = " if "
	for from := at; from < end; {
		i := strings.Index(lower[from:end], marker)
		if i < 0 {
			return false
		}
		i += from
		predicateText := lower[at:i]
		predicate := strings.TrimSpace(predicateText)
		conditionEnd := end
		if contrasts := ownerContrastOffsets(text, lower, i+len(marker), end); len(contrasts) > 0 {
			conditionEnd = contrasts[0]
		}
		condition := strings.TrimSpace(lower[i+len(marker) : conditionEnd])
		if predicate != "" && condition != "" && !ownerMarkerQuoted(lower, i) {
			contrasts := ownerContrastOffsets(text, lower, at, i)
			if len(contrasts) > 0 {
				last := contrasts[len(contrasts)-1]
				if len(contrasts) > 1 || strings.TrimSpace(lower[last+3:i]) != "only" {
					return false // the condition belongs to a contrasted clause, not an earlier requirement
				}
			}
			words := strings.Fields(predicate)
			last := words[len(words)-1]
			if last == "even" {
				return false
			}
			if ownerIfQuestionPredicate(predicate) {
				return false
			}
			return true
		}
		from = i + len(marker)
	}
	return false
}

// ownerIfQuestionPredicate reports a predicate made only of interrogative verbs and their modifiers. The last
// non-modifier must be a verb whose if-clause is its object; an object noun ("add a test", "check the test") is not
// such a verb. Modifiers may appear before or after the governing verb.
func ownerIfQuestionPredicate(predicate string) bool {
	words := strings.Fields(predicate)
	if len(words) == 0 {
		return false
	}
	verb := func(word string) bool {
		switch word {
		case "check", "decide", "determine", "verify", "confirm", "evaluate", "test", "see", "review", "ask", "inspect", "assess":
			return true
		}
		return false
	}
	modifier := func(word string) bool {
		switch word {
		case "and", "or", "also", "carefully", "quickly", "first", "then", "now", "just", "please", "again", "manually", "explicitly", "thoroughly", "immediately":
			return true
		}
		return false
	}
	lastVerb := false
	for i := len(words) - 1; i >= 0; i-- {
		if modifier(words[i]) {
			continue
		}
		if !verb(words[i]) {
			return false
		}
		lastVerb = true
		break
	}
	if !lastVerb {
		return false
	}
	for _, word := range words {
		if !verb(word) && !modifier(word) {
			return false
		}
	}
	return true
}

// ownerOfferMarker reports an unquoted, unresolved occurrence of an English offer/question. Resolved narration is
// scoped to its own sentence, so a genuine later offer remains an owner action (#5732).
func ownerOfferMarker(lower, marker string) bool {
	for from := 0; from < len(lower); {
		i := strings.Index(lower[from:], marker)
		if i < 0 {
			return false
		}
		i += from
		start := strings.LastIndexAny(lower[:i], ownerSentenceEnds) + 1
		end := len(lower)
		if j := strings.IndexAny(lower[i+len(marker):], ownerSentenceEnds); j >= 0 {
			end = i + len(marker) + j
		}
		sentence := lower[start:end]
		resolved := false
		for _, phrase := range []string{
			"i answered", "i've answered", "i have answered", "already answered", "was answered", "has been answered",
			"already resolved", "was resolved", "has been resolved", "no further action is needed", "no further action needed",
			"nothing further is needed",
		} {
			resolved = resolved || strings.Contains(sentence, phrase)
		}
		if !resolved && !ownerMarkerQuoted(lower, i) {
			return true
		}
		from = i + len(marker)
	}
	return false
}

// ownerChineseOfferMarker reports an unquoted, unnegated, unresolved occurrence of a Chinese choice question.
// Each occurrence needs its own question mark. Negation and resolved narration belong only to that occurrence, so they
// cannot hide a later question or turn an earlier negated occurrence into a request (#5732).
func ownerChineseOfferMarker(text, marker string, negations []string) bool {
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], marker)
		if i < 0 {
			return false
		}
		i += from
		prefix := text[:i]
		negated := false
		for _, n := range negations {
			lead := strings.TrimSuffix(n, marker)
			if lead != n && strings.HasSuffix(prefix, lead) {
				negated = true
				break
			}
		}
		start := strings.LastIndexAny(prefix, "。！？；.!?;，,：:") + 1 // i18n-ignore: clause punctuation for parser markers
		resolved := false
		for _, phrase := range []string{
			"我回答了", "我已回答", "已经回答", "已回答", "已经解决", "已解决", // i18n-ignore: parser markers, not display copy
			"无需进一步操作", "不需要进一步操作", "不用进一步操作", "不必进一步操作", // i18n-ignore: parser markers, not display copy
		} { // i18n-ignore: parser markers, not display copy
			resolved = resolved || strings.Contains(prefix[start:], phrase)
		}
		// A resolution after the question must be its immediate, explicitly related explanation. Neither another
		// topic's no-action note nor a later sentence can resolve this occurrence.
		end := len(text)
		if next := strings.Index(text[i+len(marker):], marker); next >= 0 {
			end = i + len(marker) + next
		}
		tail := text[i+len(marker) : end]
		question := strings.IndexAny(tail, "？?") // i18n-ignore: question punctuation for parser markers
		if !resolved && question >= 0 {
			_, width := utf8.DecodeRuneInString(tail[question:])
			following := tail[question+width:]
			if stop := strings.IndexAny(following, "。！？；.!?;"); stop >= 0 { // i18n-ignore: sentence punctuation for parser markers
				following = following[:stop]
			}
			resolved = ownerResolutionSuffix(following)
		}
		if question >= 0 && !negated && !resolved && !ownerMarkerQuoted(text, i) {
			return true
		}
		from = i + len(marker)
	}
	return false
}

// ownerChineseConditionalPrefix reports whether the current clause is still conditional. A full stop or semicolon
// starts a new clause; an independent instruction there is not hidden by an earlier condition (#5732).
func ownerChineseConditionalPrefix(prefix string) bool {
	start := 0
	for i, r := range prefix {
		boundary := r == '\n' || strings.ContainsRune(ownerSentenceEnds, r)
		// A decimal point inside a version or number is not a sentence boundary; an ASCII stop between clauses is.
		if r == '.' && i > 0 && i+1 < len(prefix) &&
			prefix[i-1] >= '0' && prefix[i-1] <= '9' && prefix[i+1] >= '0' && prefix[i+1] <= '9' {
			boundary = false
		}
		if boundary {
			start = i + utf8.RuneLen(r)
		}
	}
	clause := prefix[start:]
	for _, word := range []string{"如果", "假如", "倘若", "若"} { // i18n-ignore: parser markers, not display copy
		for end := len(clause); end > 0; {
			i := strings.LastIndex(clause[:end], word)
			if i < 0 {
				break
			}
			if word == "若" && strings.HasPrefix(clause[i+len(word):], "干") { // i18n-ignore: 若干 is a quantifier, not conditional 若
				end = i
				continue
			}
			lead := strings.TrimSpace(clause[:i])
			if lead == "" || strings.HasSuffix(lead, "：") || strings.HasSuffix(lead, ":") || // i18n-ignore: parser punctuation
				strings.HasSuffix(lead, "，") || strings.HasSuffix(lead, ",") { // i18n-ignore: parser punctuation
				return true
			}
			break
		}
	}
	return false
}

// ownerChineseDirectMarker reports an unquoted, unnegated direct instruction. Quoting or negating one occurrence does
// not hide a later genuine requirement, and a historical quoted instruction is not a new owner action (#5732).
func ownerChineseDirectMarker(text, marker string, negations []string) bool {
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], marker)
		if i < 0 {
			return false
		}
		i += from
		prefix := text[:i]
		negated := false
		for _, n := range negations {
			lead := strings.TrimSuffix(n, marker)
			if lead != n && strings.HasSuffix(prefix, lead) {
				negated = true
				break
			}
		}
		if !negated && !strings.HasSuffix(prefix, "是否") && !ownerChineseConditionalPrefix(prefix) && !ownerMarkerQuoted(text, i) { // i18n-ignore: parser marker, not display copy
			return true
		}
		from = i + len(marker)
	}
	return false
}

// ownerResolutionSuffix recognizes only an immediate explanation tied to this question, not any later "answered" or
// "no action" words. A following comma/colon lets the explanation continue; "的是上一个问题" is another topic instead.
func ownerResolutionSuffix(clause string) bool {
	for _, phrase := range []string{
		"这个问题已回答", "这个问题已经回答", "该问题已回答", "该问题已经回答", "此问题已回答", "此问题已经回答", // i18n-ignore: parser markers, not display copy
		"这个问题已解决", "这个问题已经解决", "该问题已解决", "该问题已经解决", "此问题已解决", "此问题已经解决", // i18n-ignore: parser markers, not display copy
		"无需进一步操作", "不需要进一步操作", "不用进一步操作", "不必进一步操作", // i18n-ignore: parser markers, not display copy
	} { // i18n-ignore: parser markers, not display copy
		if !strings.HasPrefix(clause, phrase) {
			continue
		}
		rest := clause[len(phrase):]
		if rest == "" || strings.HasPrefix(rest, "，") || strings.HasPrefix(rest, ",") || // i18n-ignore: parser punctuation
			strings.HasPrefix(rest, "：") || strings.HasPrefix(rest, ":") { // i18n-ignore: parser punctuation
			return true
		}
	}
	return false
}

// ownerMarkerQuoted reports whether byte offset at is inside straight double quotes, Chinese curly quotes or corner brackets.
func ownerMarkerQuoted(text string, at int) bool {
	straight, curly, corner := false, 0, 0
	for i, r := range text {
		if i >= at {
			break
		}
		switch r {
		case '"':
			straight = !straight
		case '“':
			curly++
		case '”':
			if curly > 0 {
				curly--
			}
		case '「':
			corner++
		case '」':
			if corner > 0 {
				corner--
			}
		}
	}
	return straight || curly > 0 || corner > 0
}

// ownerActionLine is a final reply's explicit request for the owner to act. The judge still decides the task as a
// whole; these unambiguous offers and hand-offs are a safety net against calling the work done (#5732). Negated forms
// are informational, not a request.
func ownerActionLine(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		compact := strings.Join(strings.Fields(ownerMarkdownLine(line)), "")
		for _, p := range []struct {
			marker    string
			negations []string
		}{
			{"需要你本人操作", []string{"不需要你本人操作", "无需你本人操作", "不用你本人操作", "不必你本人操作"}}, // i18n-ignore: parser markers, not display copy
			{"由你发起", []string{"不需要由你发起", "无需由你发起", "不用由你发起", "不必由你发起"}},        // i18n-ignore: parser markers, not display copy
			{"要不要", []string{"不需要你决定要不要", "无需你决定要不要", "不用你决定要不要", "不是要不要"}},    // i18n-ignore: parser markers, not display copy
		} {
			if !strings.Contains(compact, p.marker) {
				continue
			}
			if p.marker == "要不要" && !strings.ContainsAny(line, "？?") { // i18n-ignore: parser markers, not display copy
				continue // evaluating whether to do something is narrative, not asking the owner to choose (#5732)
			}
			if p.marker == "要不要" { // i18n-ignore: parser markers, not display copy
				if ownerChineseOfferMarker(compact, p.marker, p.negations) {
					return line
				}
				continue
			}
			if ownerChineseDirectMarker(compact, p.marker, p.negations) {
				return line
			}
		}
		lower := strings.ToLower(ownerMarkdownLine(line))
		for _, p := range []struct {
			marker    string
			negations []string
		}{
			{"you need to", nil},
			{"you'll need to", nil},
			{"would you like me to", nil},
			{"do you want me to", nil},
			{"should i proceed", nil},
			{"shall i proceed", nil},
		} {
			if !strings.Contains(lower, p.marker) {
				continue
			}
			if p.marker == "you need to" || p.marker == "you'll need to" {
				// Negation is occurrence-local: its own prefix is not direct, and it never hides a later direct requirement.
				if directOwnerMarker(line, p.marker) {
					return line
				}
				continue
			}
			negated := false
			for _, n := range p.negations {
				negated = negated || strings.Contains(lower, n)
			}
			if !negated && ownerOfferMarker(lower, p.marker) {
				return line
			}
		}
	}
	return ""
}

// taskDoneCheck is one judgment, as POST /api/v1/todos/{id}/done-check answers it.
type taskDoneCheck struct {
	Verdict    string          `json:"verdict"`
	Reason     string          `json:"reason"`
	WouldClose bool            `json:"would_close"`
	Skip       string          `json:"skip"` // why the automatic pass would not judge it now; "" when it would
	Replies    int             `json:"replies"`
	Signals    taskDoneSignals `json:"signals"`
	// Delivery: how far the work went out, when the delivery is judged (done_needs_delivery).
	Delivery *TodoDelivery `json:"delivery,omitempty"`
	// AutoCheck: the judge's own check of a supervised task's done_when, when asked (taskDoneInput.Checks,
	// supervise_sample.go).
	AutoCheck *taskAutoCheck `json:"auto_check,omitempty"`
	// reasonKey: the i18n key of the hub's own reason, when it decided without the model (the todo's check says it in
	// the user's language, task_check.go); "" for the judge's.
	reasonKey string
	// A supervisor can look up candidates for these panes when it receives the notice.
	unlinked []unlinkedPane
}

// autoDoneSkip says why the hub would not judge t now: "" when it would. work is when one of its agents last started
// a turn (worked false: not known, a hub restarted since).
func autoDoneSkip(t Todo, work time.Time, worked bool) string {
	switch {
	case t.Origin == "":
		return "not_a_task" // a todo the user made by hand: only they close it
	case t.Cancelled():
		return "cancelled"
	case t.DoneAt != nil:
		return "done"
	case t.Supervision.state(t) == reviewTimedOut || t.Supervision.state(t) == reviewClosing:
		return "held"
	case t.ReopenedAt != nil && !(worked && work.After(*t.ReopenedAt)):
		return "reopened" // no new turn since the user reopened it
	}
	return ""
}

// taskReplies are the last replies of t's active agents: the whole last answer read from the agent's transcript, or
// the session list's clipped last_text when that cannot be read.
func (s *Service) taskReplies(ctx context.Context, t Todo) (out []taskDoneReply, sideTurnBlocked bool, unlinked []unlinkedPane) {
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		m, ok := s.device(ctx, a.MachineID)
		if !ok {
			continue
		}
		resolved, sid, listed := s.resolveTaskSession(ctx, m.ID, a.SessionID)
		earlier, items, read := s.lastTwoTurns(ctx, m.ID, sid)
		if read && taskTurnAwaitingAnswer(items) {
			return nil, false, nil // one unfinished agent invalidates the whole task's completion evidence
		}
		if listed && isUnlinkedAgyPane(resolved, items) {
			unlinked = append(unlinked, unlinkedPane{MachineID: a.MachineID, SessionID: a.SessionID, LinkedAt: a.LinkedAt})
			continue // the list's preview can predate the loss of the pane's binding
		}
		ask, text := turnExchange(items)
		text = s.taskAnswer(ctx, m.ID, a.SessionID, items, text)
		from, source := taskTurnSource(t, items)
		if source != "" {
			current, ok := s.todoNow(t.ID)
			s.tasks.mu.Lock()
			since := s.tasks.working[t.ID]
			s.tasks.mu.Unlock()
			if report, evidence, valid := freshTaskDoneReport(current, items, since); ok && valid && source != "meeting" {
				text = strings.Join(append([]string{text, report}, evidence...), "\n")
			} else {
				sideTurnBlocked = true
			}
		}
		if text == "" && listed {
			text = strings.TrimSpace(resolved.LastText)
		}
		if text == "" {
			continue
		}
		if source == "" && sameRequest(ask, t.Notes) {
			ask = "" // the turn that answers the task's own prompt: the prompt is already in the input
		}
		r := taskDoneReply{
			TaskDoneReply: prompts.TaskDoneReply{Machine: m.Name, From: from, Source: source, Ask: clipMiddle(ask, 500), Text: clipMiddle(text, 1500)},
			structured:    t.hasTaskRuntime(),
		}
		if !r.structured {
			r.pr = t.PRURL
			if source == "" {
				r.pr = s.ownedReplyPR(ctx, t, m.ID, a.SessionID, text, taskReplyItem(items).TS)
			}
			r.otherPR = replyNamesOtherPR(text, r.pr)
		}
		if ask != "" && source == "" {
			// The user's own follow-up in the task's session (not a side conversation): a short "done?" gets a short "yes,
			// all done"; the report of the work is in the turn before, which the judge reads with it (#3111).
			if _, before := turnExchange(earlier); before != "" && before != text {
				r.Earlier = clipMiddle(before, 1500)
				r.otherPR = r.otherPR || !r.structured && replyNamesOtherPR(before, r.pr)
			}
		}
		out = append(out, r)
	}
	return out, sideTurnBlocked, unlinked
}

// lastTurn reads the latest exchange, not necessarily a user request about the task.
// Completion separately retains and gates its peer/injected provenance.
func (s *Service) lastTurn(ctx context.Context, machine, sid string) (ask, answer string) {
	items, _ := s.lastTurnItems(ctx, machine, sid)
	return turnExchange(items)
}

// lastTurnItems are the items of the agent's latest turn, as the node pages them (from its last user prompt on);
// ok is false when the node could not be asked.
func (s *Service) lastTurnItems(ctx context.Context, machine, sid string) (items []proto.Item, ok bool) {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": 1}, &res); err != nil {
		return nil, false
	}
	_, items = splitLastTurn(res.Items)
	return items, true
}

// lastTwoTurns are the items of the agent's turn before its latest one (earlier; none when there is none) and of its
// latest one (last), from one read of its last two turns; ok is false when the node could not be asked.
func (s *Service) lastTwoTurns(ctx context.Context, machine, sid string) (earlier, last []proto.Item, ok bool) {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": 2}, &res); err != nil {
		return nil, nil, false
	}
	earlier, last = splitLastTurn(res.Items)
	return earlier, last, true
}

// splitLastTurn splits items at the last turn's prompt (the last user message not queued, as the node counts turns):
// all of them are the last turn when no prompt is among them.
func splitLastTurn(items []proto.Item) (earlier, last []proto.Item) {
	for i := len(items) - 1; i > 0; i-- {
		if it := items[i]; it.Role == "user" && it.Kind == proto.KindText && !it.Queued && strings.TrimSpace(it.Text) != "" {
			return items[:i], items[i:]
		}
	}
	return nil, items
}

// turnExchange is what a turn's items say: the user's ask (its first user message; "" when none) and the agent's
// last reply ("" when none).
func turnExchange(items []proto.Item) (ask, answer string) {
	for _, it := range items {
		if it.Kind == proto.KindText && it.Role == "user" && strings.TrimSpace(it.Text) != "" {
			ask = strings.TrimSpace(it.Text)
			break
		}
	}
	reply := taskReplyItem(items)
	return ask, turnReplyText(reply.Text, reply.FullSize > 0)
}

func taskReplyItem(items []proto.Item) proto.Item {
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; it.Kind == proto.KindText && it.Role == "assistant" && strings.TrimSpace(it.Text) != "" {
			return it
		}
	}
	return proto.Item{}
}

// Indentation is significant in a Markdown report. A clipped excerpt ending exactly at its rule
// still has more text after it and cannot be read as the final report.
func turnReplyText(text string, clipped bool) string {
	if !strings.Contains(text, "### Persona ") {
		return strings.TrimSpace(text)
	}
	if clipped {
		if _, ok := textfmt.FinalWorkReport(text); ok {
			return text + "\n…"
		}
	}
	return text
}

// userStoppedNotice is the notice the transcript reader puts where the user stopped a turn (Claude Code's "[Request
// interrupted by user]", Codex's turn_aborted; transcript/user_text.go, codex.go).
const userStoppedNotice = transcript.InterruptedNotice

// silentTurn: a turn the node could read (it has items, at least the prompt) in which the agent neither replied nor
// called a tool. Claude Code ends a turn so, without an error, when the model service answers 200 with nothing in it
// (it retries once with a hidden "no visible output" prompt, then stops); Codex's rollout maps to the same items.
// Thinking alone is no output: the user sees nothing.
func silentTurn(items []proto.Item) bool {
	none, stopped := turnWithoutOutput(items)
	return none && !stopped // stopped: the user stopped it (Esc), no failure to report
}

// turnWithoutOutput: a turn the node could read (none: it has items, at least the prompt) in which the agent neither
// replied nor called a tool, and whether the transcript says it was stopped (the interrupted notice). Thinking alone
// is no output.
func turnWithoutOutput(items []proto.Item) (none, stopped bool) {
	if len(items) == 0 {
		return false, false // nothing to go by (an older node, a runtime that does not page items): not judged silent
	}
	for _, it := range items {
		if it.Kind == proto.KindNotice && it.Text == userStoppedNotice {
			stopped = true
		}
		if it.Role != "assistant" {
			continue
		}
		switch it.Kind {
		case proto.KindToolUse, proto.KindImage:
			return false, stopped
		case proto.KindText:
			if strings.TrimSpace(it.Text) != "" {
				return false, stopped
			}
		}
	}
	return true, stopped
}

// sameRequest: ask is the task's own prompt (as sent, maybe with context around it).
func sameRequest(ask, prompt string) bool {
	ask, prompt = oneLine(ask), oneLine(prompt)
	if ask == "" || prompt == "" {
		return ask == prompt
	}
	if r := []rune(prompt); len(r) > 80 {
		prompt = string(r[:80])
	}
	return strings.Contains(ask, prompt)
}

// clipMiddle keeps a long text's first and last n runes: a final answer says what was done first and what is left last.
func clipMiddle(s string, n int) string {
	if utf8.RuneCountInString(s) <= 2*n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "\n…\n" + string(r[len(r)-n:])
}

// judgeTaskDone asks the judge model whether t's work is finished. An error: no model, or no answer to go by.
func (s *Service) judgeTaskDone(ctx context.Context, t Todo) (taskDoneCheck, error) {
	if current, ok := s.todoNow(t.ID); ok {
		t = current
	}
	if t.Phase == assistantwire.TaskPhaseWaiting {
		c := taskDoneCheck{Verdict: verdictWaiting}
		if wait := t.WaitState; wait != nil {
			c.Reason = clip(oneLine(wait.Reason), 40)
			if wait.WaitingOn == assistantwire.WaitUser {
				c.Verdict = verdictNeedsUser
			}
		}
		return c, nil
	}
	replies, sideTurnBlocked, unlinked := s.taskReplies(ctx, t)
	if c, ok := taskDoneWithoutModel(replies, sideTurnBlocked); ok {
		if c.reasonKey == checkReasonNoReply && len(unlinked) > 0 {
			c.reasonKey = checkReasonAgyUnlinked
			c.Reason = i18n.T(s.lang(ctx), c.reasonKey)
			c.unlinked = unlinked
		}
		if sideTurnBlocked {
			s.logf("task %s: side-conversation reply without current task completion evidence: left open", t.ID)
		}
		return c, nil
	}
	in := taskDoneInput{Owner: s.owner(ctx), Language: answerLanguage(s.hello(ctx).Locale), Delivery: s.doneNeedsDelivery(ctx),
		Title: t.Title, Notes: t.Notes, Replies: replies}
	if t.hasTaskRuntime() {
		d := t.Delivery
		if d == nil {
			d = &TodoDelivery{State: deliveryNone}
			if t.PRURL != "" || len(t.PRs) > 0 {
				d.State = deliveryPR
			}
		}
		in.RuntimeDelivery = d
		p := prFacts(t, t.PRURL)
		in.RuntimeMergedPR = p != nil && p.State == "merged" || d.State == deliveryMerged
	} else {
		pr, otherPR := "", false
		for _, r := range replies {
			if r.pr != "" && r.pr != t.PRURL {
				pr = r.pr
			}
			otherPR = otherPR || r.otherPR
		}
		in.Deployed = func(d TodoDelivery) TodoDelivery {
			return s.gateDelivery(ctx, t, d, pr, otherPR)
		}
	}
	// What it is judged against is its brief (#3822): the owner's words and the criteria as scope changes left them, not
	// the latest hand-over prompt's.
	if brief, ok := s.briefOfTodo(t.ID); ok {
		in.Brief = &brief
	}
	if in.Brief != nil && len(in.Brief.Criteria) > 0 {
		in.Criteria = in.Brief.Criteria
	}
	if t.Supervision != nil {
		if len(in.Criteria) == 0 {
			in.Criteria = t.Supervision.DoneWhen
		}
		// Spot-checking on: the judge records each criterion as delivery evidence; it never accepts or closes the task.
		in.Checks = len(in.Criteria) > 0 && s.reviewSamplePercent(ctx) < 100
	}
	return judgeTaskDoneWith(ctx, s.purposeModel("inbox.judge"), in)
}

// jsonModel is the one call each single-call judge makes (done, delegation, triage): a system and a user text in, a
// JSON answer decoded into out. *ai.Client is the real one; tests and behaviour checks give their own.
type jsonModel interface {
	JSON(ctx context.Context, system, user string, out any) error
}

// taskDoneInput is everything the done judge decides on, read from the todo, its agents and the assistant's
// settings (judgeTaskDone) or written out by a test.
type taskDoneInput struct {
	Owner    string          // how the prompt names the user
	Language string          // the user's language for the reason ("" Chinese, answerLanguage)
	Delivery bool            // assistant.done_needs_delivery
	Title    string          // the todo's title
	Notes    string          // what it was handed out with (clipped here)
	Criteria []string        // a supervised task's done_when
	Replies  []taskDoneReply // its agents' last replies
	Checks   bool            // also check each criterion and the delivery (supervise_sample.go)
	Brief    *TaskBrief      // what was asked when the work was handed out (task_brief.go); nil: none
	// Deployed checks a delivery said live against the running hub (gateDelivery); nil: as judged.
	Deployed func(TodoDelivery) TodoDelivery
	// RuntimeDelivery is the objective delivery for structured tasks.
	RuntimeDelivery *TodoDelivery
	RuntimeMergedPR bool
}

// taskDoneWithoutModel is the verdict given before any model is asked: a reply from a side conversation with no
// evidence for this task, no reply at all, or a turn the model service cut off. ok false: the model decides.
func taskDoneWithoutModel(replies []taskDoneReply, sideTurnBlocked bool) (c taskDoneCheck, ok bool) {
	c = taskDoneCheck{Replies: len(replies), Signals: doneSignals(replies)}
	if sideTurnBlocked {
		c.Verdict, c.Reason, c.reasonKey = verdictNotDone, verdictWords[verdictNotDone], checkReasonSideTurn
		return c, true
	}
	if len(replies) == 0 {
		c.Verdict, c.Reason, c.reasonKey = verdictUnsure, "没读到 Agent 的回复", checkReasonNoReply
		return c, true
	}
	for _, r := range replies {
		if cutOff(r.Text) { // its turn was cut off by the model service: not finished, whatever came before (#1516)
			c.Verdict, c.Reason, c.reasonKey = verdictNotDone, interruptedSummary, checkReasonInterrupted
			return c, true
		}
	}
	return c, false
}

// judgeTaskDoneWith asks m about in and applies the hub's own rules to what it answers: a verdict it made up is
// unsure, the reason is one short line, and while delivery is judged, done with the work not out yet is undelivered.
// It reads nothing else, so a behaviour check can run it on written-out cases.
func judgeTaskDoneWith(ctx context.Context, m jsonModel, in taskDoneInput) (taskDoneCheck, error) {
	c := taskDoneCheck{Replies: len(in.Replies), Signals: doneSignals(in.Replies)}
	if in.RuntimeDelivery != nil {
		c.Signals = taskDoneSignals{MergedPR: in.RuntimeMergedPR}
	}
	system := prompts.RenderTaskDone(prompts.TaskDone{Owner: in.Owner, Delivery: in.Delivery, Checks: in.Checks, Language: in.Language})
	din := prompts.TaskDoneInput{Title: in.Title, Prompt: clipMiddle(strings.TrimSpace(in.Notes), 1500),
		Criteria: in.Criteria, MergedPR: c.Signals.MergedPR, Build: c.Signals.Build}
	for _, r := range in.Replies {
		din.Replies = append(din.Replies, r.TaskDoneReply)
	}
	if b := in.Brief; b != nil { // held to what was asked, not to the latest hand-over prompt (task_brief.go)
		if b.Request != nil {
			din.Asked, din.AskedAt = clipMiddle(b.Request.Text, 600), b.Request.At.UTC().Format("2006-01-02")
		}
		for _, d := range (&taskBrief{rec: b.briefRecord, st: briefState{Log: b.Log}}).dropped() {
			din.Dropped = append(din.Dropped, d.Criterion)
		}
	}
	input := prompts.RenderTaskDoneInput(din)
	var out struct {
		Verdict  string          `json:"verdict"`
		Reason   string          `json:"reason"`
		Delivery string          `json:"delivery"`
		Gate     string          `json:"gate"`
		Unblock  string          `json:"unblock"`
		Checks   []autoCheckItem `json:"checks"`
	}
	// This deterministic safety net does not depend on the judge being available. Keep it with the model error so the
	// supervision path can still mark and push an explicit owner action (#5732).
	ownerAction := ""
	if in.RuntimeDelivery == nil {
		for _, r := range in.Replies {
			if action := ownerActionLine(r.Text); action != "" {
				ownerAction = action
				break
			}
		}
	}
	if err := m.JSON(ctx, system, input, &out); err != nil {
		if ownerAction != "" {
			c.Verdict, c.Reason = verdictNeedsUser, clip(oneLine(ownerAction), 40)
		}
		return c, err
	}
	switch v := strings.ToLower(strings.TrimSpace(out.Verdict)); v {
	case verdictDone, verdictNotDone, verdictWaiting, verdictNeedsUser:
		c.Verdict = v
	default:
		c.Verdict = verdictUnsure // a verdict it made up is no verdict
	}
	c.Reason = clip(oneLine(out.Reason), 40)
	if c.Verdict != verdictNeedsUser && ownerAction != "" {
		c.Verdict, c.Reason = verdictNeedsUser, clip(oneLine(ownerAction), 40)
	}
	if d := in.RuntimeDelivery; d != nil {
		if c.Verdict == verdictWaiting || c.Verdict == verdictNeedsUser {
			c.Verdict = verdictNotDone // the judge checks criteria. A report or an observation supplies the wait.
		}
		if in.Checks {
			c.AutoCheck = autoCheckOf(in.Criteria, out.Checks, d.State)
		}
		if in.Delivery {
			c.Delivery = d
			if c.Verdict == verdictDone && !deliveryClosable(d.State) {
				c.Verdict = verdictUndelivered
			}
		}
		return c, nil
	}
	// Kept for wire compatibility. A judge may deliver a task for review, never close it (#5732).
	c.WouldClose = false
	if in.Checks {
		d := judgedDelivery(out.Delivery, out.Gate, out.Unblock, in.Replies)
		if !strings.EqualFold(strings.TrimSpace(out.Delivery), deliveryLive) &&
			!strings.EqualFold(strings.TrimSpace(out.Delivery), deliveryOff) {
			d.State = cmp.Or(strings.ToLower(strings.TrimSpace(out.Delivery)), deliveryNone) // live only on the judge's own word, never the agent's line alone
		}
		if !strings.EqualFold(strings.TrimSpace(out.Delivery), deliveryLive) && d.State == deliveryLive {
			d.State = deliveryMerged
		}
		if in.Deployed != nil {
			d = in.Deployed(d)
		}
		c.AutoCheck = autoCheckOf(in.Criteria, out.Checks, d.State)
	}
	if in.Delivery {
		d := judgedDelivery(out.Delivery, out.Gate, out.Unblock, in.Replies)
		if in.Deployed != nil {
			d = in.Deployed(d) // live only when the running hub has the work (deploy_check.go)
		}
		if d.State != "" {
			d.At = time.Now()
			c.Delivery = &d
		}
		if c.Verdict == verdictDone && !deliveryClosable(d.State) {
			c.Verdict, c.WouldClose = verdictUndelivered, false
		}
	}
	return c, nil
}

// autoFinishTask: t's receipt went out (its agents all stopped). work is when one of them last started a turn. The
// judge records delivery and waiting state when it finds the work finished; it never closes the todo.
func (s *Service) autoFinishTask(t Todo, work time.Time, worked bool) {
	s.autoFinishTaskMode(t, work, worked, true)
}

// autoFinishTaskRetry is the supervision sweep's deferred pass. Its backoff replaces another inline three-second wait.
func (s *Service) autoFinishTaskRetry(t Todo, work time.Time, worked bool) {
	s.autoFinishTaskMode(t, work, worked, false)
}

func (s *Service) autoFinishTaskMode(t Todo, work time.Time, worked bool, immediateRetry bool) {
	ctx, cancel := context.WithTimeout(context.Background(), taskDoneTimeout)
	defer cancel()
	if skip := autoDoneSkip(t, work, worked); skip != "" {
		if skip == "reopened" {
			s.logf("task %s: reopened by the user and no new turn since: not judged", t.ID)
		}
		s.turnJudged(ctx, t.ID, taskDoneCheck{}, nil, skip) // nothing to judge: the sweep does not ask again (task_turns.go)
		return
	}
	s.taskJudging(t.ID, 1) // the sweep leaves a turn alone while it is judged (resumeTurn)
	defer s.taskJudging(t.ID, -1)
	defer s.judgingTurn(t.ID)() // another look at the same turn waits for this judgment (turnAgain)
	if !worked {
		work = t.CreatedAt
	}
	if !s.taskCompletionCurrent(ctx, t, work) {
		return
	}
	c, err := s.judgeTaskDone(ctx, t)
	if immediateRetry && err != nil && !ai.IsPaused(err) && !ai.IsNotConfigured(err) && ctx.Err() == nil {
		// One failed call (a model service hiccup, a reply that was no verdict) is asked once more before the task is
		// left unjudged (#3111); no model at all is not asked again.
		s.logf("task %s: done judge: %v (asked again)", t.ID, err)
		select {
		case <-time.After(taskDoneRetryAfter):
			c, err = s.judgeTaskDone(ctx, t)
		case <-ctx.Done():
		}
	}
	stateCtx := ctx
	if t.Supervision != nil && err != nil && ctx.Err() != nil {
		// A judge timeout expires its own context. Use a fresh one only to verify that this is still the same stopped
		// stretch and to persist its retry; otherwise the timeout would bypass the service-failure budget entirely.
		var cancelState context.CancelFunc
		stateCtx, cancelState = context.WithTimeout(context.Background(), taskDoneTimeout)
		defer cancelState()
	}
	if !s.taskCompletionCurrent(stateCtx, t, work) {
		if cur, ok := s.todoNow(t.ID); !ok || cur.DoneAt != nil {
			s.logf("task %s: closed while judged: judgment dropped", t.ID) // accepted, cancelled or closed by hand (#7539)
		} else {
			s.logf("task %s: work changed while judged: left open", t.ID)
		}
		return
	}
	if deferred, judgedErr := s.deferTaskDoneJudgment(stateCtx, t, work, err); deferred {
		return
	} else {
		err = judgedErr
	}
	s.applyTaskDone(stateCtx, t, work, worked, c, err)
}

// applyTaskDone writes judgment c (err: the judge could not say) of task t, whose agents last started a turn at work
// (worked false: not known): the verdict on the todo, where its delivery is held, and then what follows a finished
// stretch of work. Every task waits for the main conversation's review or explicit user completion; none is closed
// here. autoFinishTask and Re-check with apply (doneCheckRoute) end here. A task closed since that stretch began
// (accepted, cancelled, closed by hand, or a review that has written its acceptance and not closed the to-do yet)
// gets none of it: the judgment is of work that is over (#7539).
func (s *Service) applyTaskDone(ctx context.Context, t Todo, work time.Time, worked bool, c taskDoneCheck, err error) {
	if cur, ok := s.todoNow(t.ID); ok && cur.hasTaskRuntime() && !sameTaskRuntime(t, cur) {
		s.deferTurnJudgment(ctx, t.ID)
		return
	}
	since := work
	if !worked {
		since = t.CreatedAt
	}
	if s.closedSince(t.ID, since) {
		s.logf("task %s: closed since the work judged began: judged %s, dropped", t.ID, cmp.Or(c.Verdict, "with an error"))
		s.turnJudged(ctx, t.ID, taskDoneCheck{}, nil, "done") // nothing to judge: the sweep does not ask again
		return
	}
	if err == nil && c.Verdict == verdictDone {
		if active := s.taskCloseActivityError(ctx, t, time.Now()); active != nil {
			s.deferTurnJudgment(ctx, t.ID)
			s.logf("task %s: judgment deferred: %v", t.ID, active)
			return
		}
	}
	if ai.IsNotConfigured(err) && c.Verdict != verdictNeedsUser {
		// A missing provider is not a transient judge failure: keep the same localized explanation the settings row shows.
		// A deterministic owner-only action remains actionable without a judge and must reach the visible wait (#5732).
		c = taskDoneCheck{Verdict: verdictWaiting, Reason: mc(s.lang(ctx), s.noAPIStatus(ctx))}
	}
	if err == nil || ai.IsNotConfigured(err) {
		s.keepTaskCheck(ctx, t, work, c) // why it is (not) done, on the todo (task_check.go)
	}
	if !t.hasTaskRuntime() && err == nil && c.Delivery != nil && c.Verdict != verdictNotDone {
		s.writeTaskDelivery(ctx, t.ID, *c.Delivery, true)
	}
	if t.Supervision == nil {
		if _, reporting := s.reporterOf(ctx, t.Origin); reporting {
			// An active reporting conversation deliberately keeps its task unsupervised (#12152): preserve its ordinary
			// quiet completion instead of attaching main here. Other legacy unsupervised tasks join main below (#5732).
			// judgeTaskDoneWith suppresses WouldClose for universal review; this legacy route alone restores main's verdict.
			c.WouldClose = c.Verdict == verdictDone
			s.turnJudged(ctx, t.ID, c, err, "")
			if err != nil {
				if !ai.IsPaused(err) {
					s.logf("task %s: done judge: %v (left open)", t.ID, err)
				}
				return
			}
			if !c.WouldClose {
				s.logf("task %s: not closed: %s (%s)", t.ID, c.Verdict, c.Reason)
				return
			}
			reason := c.Reason
			if reason == "" {
				reason = mc(s.lang(ctx), doneReasonFallback)
			}
			if !worked {
				work = t.CreatedAt
			}
			ok, err := s.finishCurrentTask(ctx, t.ID, reason, work)
			switch {
			case err != nil:
				var hold *taskCloseHoldError
				if errors.As(err, &hold) {
					s.deferTurnJudgment(ctx, t.ID)
				}
				s.logf("task %s: auto done: %v", t.ID, err)
			case ok:
				s.logf("task %s: done (auto): %s", t.ID, reason)
				s.taskClosedReport(t, reason)
			default:
				s.logf("task %s: changed while judged: left as it is", t.ID)
			}
			return
		}
		// Tasks created before universal supervision still join the main conversation instead of being closed (#5732).
		main := s.settings(ctx).Main
		if main == "" {
			// Without a reviewer, leave the turn unjudged so the same completed work reaches main after settings recover.
			s.logf("task %s: no main conversation to review delivery; left unjudged", t.ID)
			return
		}
		if err := s.superviseTodo(ctx, t.ID, main, nil, 0); err != nil {
			// Leave the turn unjudged so the next sweep can attach this legacy delivery and tell main after the
			// store recovers. Marking it now would strand an open task without anybody allowed to review it (#5732).
			return
		}
		if current, ok := s.todoNow(t.ID); ok {
			t = current
		}
	}
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	if !s.taskWorkCurrent(t.ID, work) {
		return
	}
	if !worked {
		work = t.CreatedAt
	}
	if c.Verdict == verdictNeedsUser && !s.noteOwnerWait(ctx, t, work, c.Reason) {
		// Do not mark the turn judged before its owner wait exists: recovery must apply this same work again after
		// the supervision store returns. A successful write makes the second note below idempotent, push included.
		return
	}
	s.turnJudged(ctx, t.ID, c, err, "")     // on the turn's ledger before its signal: judged once, told once (task_turns.go)
	s.superviseJudged(ctx, t, work, c, err) // never closed here: its supervisor reviews it (supervise.go)
}

// doneCheckRoute: POST /api/v1/todos/{id}/done-check runs the judgment on a todo as it is and answers it. Without
// apply nothing is written (for looking into why a task was or was not closed). With {"apply": true} (Re-check, #4867)
// it first moves the task's stage and delivery from its agents' last replies, as their turn's end would have
// (advanceFromLastReplies: for a turn end the hub missed), then writes the judgment as a finished stretch of work's is
// (applyTaskDone), when the hub would judge it at all (no skip) and its agents are still: applied says whether it did.
func (s *Service) doneCheckRoute() {
	s.handle("POST /api/v1/todos/{id}/done-check", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot judge todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("todos service unavailable"))
			return
		}
		id := r.PathValue("id")
		t, ok, err := s.todo(r.Context(), id)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("todos service unavailable"))
			return
		}
		if !ok {
			apierr.Write(w, http.StatusNotFound, db.ErrTodoNotFound)
			return
		}
		var in struct {
			Apply bool `json:"apply"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
		}
		s.tasks.mu.Lock()
		work, worked := s.tasks.working[id]
		s.tasks.mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), taskDoneTimeout)
		defer cancel()
		apply := in.Apply && autoDoneSkip(t, work, worked) == "" && s.taskRuntimeSettled(ctx, t)
		if apply {
			s.advanceFromLastReplies(ctx, t)
			if cur, ok := s.todoNow(id); ok {
				t = cur
			}
		}
		c, err := s.judgeTaskDone(ctx, t)
		if err != nil && c.Verdict != verdictNeedsUser {
			// An explicit owner action is deterministic and must still be applied; every other model error leaves the
			// judgment indeterminate (#5732).
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		c.Skip = autoDoneSkip(t, work, worked)
		if c.Skip != "" {
			c.WouldClose = false
		}
		applied := false
		if apply && c.Skip == "" {
			since := work
			if !worked {
				since = t.CreatedAt
			}
			if s.taskCompletionCurrent(ctx, t, since) { // nothing started or changed while it was judged
				s.applyTaskDone(context.WithoutCancel(ctx), t, since, worked, c, nil)
				applied = true
			}
		}
		if c.reasonKey == checkReasonAgyUnlinked {
			c.Reason = i18n.T(s.requestLang(r), c.reasonKey)
		}
		if !in.Apply {
			apierr.WriteJSON(w, c)
			return
		}
		out := struct {
			taskDoneCheck
			Applied bool   `json:"applied"`
			Stage   string `json:"stage,omitempty"`
		}{taskDoneCheck: c, Applied: applied}
		if cur, ok := s.todoNow(id); ok {
			out.Stage = cur.Stage
		}
		apierr.WriteJSON(w, out)
	})
}

// advanceFromLastReplies moves task t's stage and delivery from its agents' last replies, as the end of their turn
// does (advanceTaskReply: a PR link, merged, deployed and verified words, any "deployed" checked against the running
// hub), and asks for the deployed work to be verified as that does. For Re-check with apply: a turn whose end the hub
// did not see (a restart in the middle of it) moved nothing, and the session list's last words are clipped.
func (s *Service) advanceFromLastReplies(ctx context.Context, t Todo) {
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		m, ok := s.device(ctx, a.MachineID)
		if !ok {
			continue
		}
		text := ""
		var replyAt time.Time
		resolved, sid, listed := s.resolveTaskSession(ctx, m.ID, a.SessionID)
		if items, read := s.lastTurnItems(ctx, m.ID, sid); read {
			if listed && isUnlinkedAgyPane(resolved, items) {
				continue
			}
			_, text = turnExchange(items)
			for i := len(items) - 1; i >= 0; i-- {
				if it := items[i]; it.Role == "assistant" && it.Kind == proto.KindText && strings.TrimSpace(it.Text) == text {
					replyAt = it.TS
					break
				}
			}
		}
		if text == "" && listed && !strings.HasSuffix(resolved.LastText, "…") {
			text = strings.TrimSpace(resolved.LastText)
			replyAt = resolved.UpdatedAt
		}
		if text == "" {
			continue
		}
		if cur, ok := s.todoNow(t.ID); ok {
			t = cur
		}
		if s.advanceTaskReplyAt(ctx, t, m.ID, a.SessionID, text, replyAt) {
			go s.askAcceptance(t, m.ID, a.SessionID, text) // deployed: verified, by the supervising assistant or the owner
		}
	}
}
