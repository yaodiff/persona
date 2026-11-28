package extmgr

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/compat"
)

// composePrompt says whether raw is the narrow link an extension page may open to put one of its own
// quick prompts in the assistant's composer: persona://compose?prompt=<id>&text=<text> (or the
// previous scheme's alias), one non-empty prompt id and one text, no user info, extra query, path or fragment. The caller
// checks both against the extension's manifest; this only holds the link's shape.
func composePrompt(raw string) (id, text string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil {
		return "", "", false
	}
	if !compat.IsAppScheme(strings.ToLower(u.Scheme)) {
		return "", "", false
	}
	if !strings.EqualFold(u.Host, "compose") || (u.Path != "" && u.Path != "/") {
		return "", "", false
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", "", false
	}
	q := u.Query()
	ids, ok := q["prompt"]
	texts, hasText := q["text"]
	if !ok || !hasText || len(ids) != 1 || len(texts) != 1 || len(q) != 2 {
		return "", "", false
	}
	id, text = ids[0], texts[0]
	if id == "" || utf8.RuneCountInString(id) > 64 || strings.ContainsAny(id, " \t\r\n") {
		return "", "", false
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 8000 {
		return "", "", false
	}
	return id, text, true
}
