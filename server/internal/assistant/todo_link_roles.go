package assistant

import (
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/lazyre"
)

var (
	// referenceLineRe matches a line opened by a related-work marker or an explicit "background:" marker; every link
	// on such a line is a reference. The (?m) flags anchor ^/$ to line boundaries rather than the whole report.
	referenceLineRe = lazyre.New(`(?im)^\s*(?:[-*]\s*)?(?:(?:related\s+(?:work|prs?|pull\s+requests?)|see\s+also|references?)\b|background\s*:)`) // i18n-ignore: matches Agent report prose
	// referenceClauseRe matches an in-sentence disclaimer that a named PR is related work, explicitly framed as
	// background or not this task's delivery. A bare "background" is deliberately not enough: ordinary delivery
	// prose ("fixed the background sync worker in <PR>") must keep the delivery role. Only links inside the same
	// sentence carry the marker, so a delivery PR in the next sentence keeps its role.
	referenceClauseRe = lazyre.New(`(?i)(?:related\s+work|background(?:\s+work)?\s*:|(?:as|for|is)\s+background(?:\s+work)?\b|not\s+this\s+(?:task|research|work)'?s?\s+delivery|not\s+(?:our|my)\s+delivery|rather\s+than\s+(?:this|our|my)\s+delivery)`) // i18n-ignore: matches Agent report prose
	citationBlockRe   = lazyre.New(`(?s)<oai-mem-citation>.*?</oai-mem-citation>`)
)

// referenceReportLinks recognizes historical reference wording for unbound tasks using legacy single-PR extraction.
// Links outside that fallback get their roles from stored bindings and structured closeout artifacts.
func referenceReportLinks(text string, raws []string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(text) == "" || len(raws) == 0 {
		return out
	}
	// URLs inside runtime citation footers are citations, never deliveries.
	for _, block := range citationBlockRe.FindAllString(text, -1) {
		for _, raw := range raws {
			if containsURL(block, raw) {
				out[raw] = true
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		lineIsReference := referenceLineRe.MatchString(line)
		var sentences []string
		if lineIsReference {
			sentences = []string{line}
		} else {
			sentences = splitSentencesKeepingURLs(line, raws)
		}
		for _, sentence := range sentences {
			if !lineIsReference && !referenceClauseRe.MatchString(sentence) {
				continue
			}
			for _, raw := range raws {
				if containsURL(sentence, raw) {
					out[raw] = true
				}
			}
		}
	}
	return out
}

// legacyReportPR accepts one distinct candidate. A lone prose reference can still be shown in the legacy stage
// without gaining a delivery role. Runtime citations are metadata and cannot select a PR.
func legacyReportPR(text string) (pr string, seen, reference bool) {
	text = citationBlockRe.ReplaceAllString(text, "")
	raws := prLink.FindAllString(text, -1)
	references := referenceReportLinks(text, raws)
	first, firstKey, prKey := "", "", ""
	multiple := false
	for _, raw := range raws {
		key := reportLinkKey(raw)
		if first == "" {
			first, firstKey = raw, key
		}
		multiple = multiple || key != firstKey
		if references[raw] {
			continue
		}
		if pr == "" {
			pr, prKey = raw, key
		} else if key != prKey {
			return "", true, false
		}
	}
	if pr == "" && first != "" && !multiple {
		return first, true, true
	}
	return pr, first != "", false
}

// reportBindingsBlockLegacy allows the legacy fallback with reference-only repositories. A target repository
// awaiting its first delivery or an existing reference to the candidate requires an explicit report.
func reportBindingsBlockLegacy(rows []repository.TaskRepository, pr string) bool {
	key := reportLinkKey(pr)
	for _, row := range rows {
		if len(row.Links) == 0 {
			return true // a repository with no links is a delivery target
		}
		for _, link := range row.Links {
			if link.Role != repository.LinkRoleReference || key != "" && reportLinkKey(link.CanonicalURL) == key {
				return true
			}
		}
	}
	return false
}

// reportBindingsBlockAcceptedLegacy checks bindings at stage write time, after ownership verification has saved
// pr itself as a delivery link: another delivery link, an unlinked target repository, or a reference for pr still blocks.
func reportBindingsBlockAcceptedLegacy(rows []repository.TaskRepository, pr string) bool {
	key := reportLinkKey(pr)
	if key == "" {
		return reportBindingsBlockLegacy(rows, pr)
	}
	for _, row := range rows {
		if len(row.Links) == 0 {
			return true
		}
		for _, link := range row.Links {
			linkKey := reportLinkKey(link.CanonicalURL)
			if link.Role == repository.LinkRoleReference {
				if linkKey == key {
					return true
				}
				continue
			}
			if linkKey != key {
				return true
			}
		}
	}
	return false
}

// reportLinkRoles preserves stored roles and treats new incidental links as references. Keep an unlinked target
// empty until its first explicit delivery so the presence of reference links cannot turn it into a reference repo.
func reportLinkRoles(raws, deliveries []string, rows []repository.TaskRepository) (links []string, roles map[string]string) {
	keep := map[string]bool{}
	var unlinkedTargets []string
	for _, row := range rows {
		if target := row.SourceSnapshot.Identity.CanonicalURL; len(row.Links) == 0 && target != "" {
			unlinkedTargets = append(unlinkedTargets, strings.TrimRight(target, "/")+"/")
		}
		for _, link := range row.Links {
			keep[reportLinkKey(link.CanonicalURL)] = true
		}
	}
	for _, raw := range deliveries {
		keep[reportLinkKey(raw)] = true
	}
	roles = map[string]string{}
	for _, raw := range raws {
		key := reportLinkKey(raw)
		if !keep[key] {
			if slices.ContainsFunc(unlinkedTargets, func(target string) bool { return strings.HasPrefix(key, target) }) {
				continue
			}
			roles[raw] = repository.LinkRoleReference
		}
		links = append(links, raw)
	}
	return links, roles
}

func reportLinkKey(raw string) string {
	if raw == "" {
		return ""
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimRight(raw, "/")
	if _, link, err := repository.ParseTaskLink(raw); err == nil {
		return link.CanonicalURL
	}
	return raw
}

// containsURL reports whether text contains raw as a complete URL, not as a prefix of a longer address. A match whose
// next rune is alphanumeric or a path separator is a longer URL (e.g. ".../pull/1" inside ".../pull/11"), not a hit.
func containsURL(text, raw string) bool {
	start := 0
	for {
		at := strings.Index(text[start:], raw)
		if at < 0 {
			return false
		}
		after := start + at + len(raw)
		if after >= len(text) {
			return true
		}
		switch r := text[after]; {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '/':
			start = after // longer address; keep looking for a complete occurrence
		default:
			return true
		}
	}
}

// splitSentencesKeepingURLs breaks a line at sentence/semicolon boundaries, but never at punctuation that sits inside
// a reported URL (URLs contain '.' and ';'), so a disclaimer stays bound to the link it actually sits next to.
func splitSentencesKeepingURLs(line string, raws []string) []string {
	protected := make([]bool, len(line))
	for _, raw := range raws {
		start := 0
		for {
			at := strings.Index(line[start:], raw)
			if at < 0 {
				break
			}
			lo, hi := start+at, start+at+len(raw)
			for i := lo; i < hi; i++ {
				protected[i] = true
			}
			start = hi
		}
	}
	var out []string
	start := 0
	for i, r := range line {
		if (r == '.' || r == ';' || r == '。' || r == '；') && !protected[i] {
			out = append(out, line[start:i])
			start = i + 1
		}
	}
	out = append(out, line[start:])
	return out
}
