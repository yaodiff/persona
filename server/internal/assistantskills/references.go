package assistantskills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
)

// Entry is an executable environment's inventory entry. Paths stay on the node.
type Entry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Path        string `json:"-"`
}

// Catalog deliberately distinguishes a bounded local inventory from a runtime's complete answer.
type Catalog struct {
	Version  int     `json:"catalog_version"`
	Skills   []Entry `json:"skills"`
	Complete bool    `json:"complete"`
}

// Bind makes a skill's identity specific to its assistant, machine, launcher and profile.
// No lookup table, client path, or display-name matching is involved.
func Bind(scope string, entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	for i, e := range entries {
		raw, _ := json.Marshal([]string{scope, e.Path, e.Name, e.Source})
		sum := sha256.Sum256(raw)
		e.ID = hex.EncodeToString(sum[:])
		out[i] = e
	}
	return out
}

var ErrReference = errors.New("selected skill is unavailable or its execution environment changed; remove it and select it again")

func isTagSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

func isAttrNameDelim(b byte) bool {
	return isTagSpace(b) || b == '=' || b == '/' || b == '"' || b == '\''
}

// nextLeadingContext extracts one leading <persona-context ...>...</persona-context> (or legacy
// <jarvis-context ...>...</jarvis-context>) block in O(|text|) time, tracking single/double quotes
// so a literal '>' inside an attribute value (e.g. title="a > b") does not truncate the opening tag.
func nextLeadingContext(text string) (tag, rest string, ok bool) {
	i := 0
	for i < len(text) && isTagSpace(text[i]) {
		i++
	}
	if i >= len(text) || text[i] != '<' {
		return "", "", false
	}
	var tagName string
	for _, cand := range compat.ContextNames() {
		if strings.HasPrefix(text[i+1:], cand) {
			tagName = cand
			break
		}
	}
	if tagName == "" {
		return "", "", false
	}
	tagStart := i + 1 + len(tagName)
	if tagStart >= len(text) || (!isTagSpace(text[tagStart]) && text[tagStart] != '>' && text[tagStart] != '/') {
		return "", "", false
	}
	firstGT := -1
	openEnd := -1
	var quote byte
	for j := tagStart; j < len(text); j++ {
		c := text[j]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else if c == '>' && firstGT < 0 {
				firstGT = j
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			openEnd = j
			break
		}
	}
	if openEnd < 0 {
		openEnd = firstGT
	}
	if openEnd < 0 {
		return "", "", false
	}
	closeTag := "</" + tagName + ">"
	closeRel := strings.Index(text[openEnd+1:], closeTag)
	if closeRel < 0 && firstGT >= 0 && firstGT < openEnd {
		openEnd = firstGT
		closeRel = strings.Index(text[openEnd+1:], closeTag)
	}
	if closeRel < 0 {
		return "", "", false
	}
	end := openEnd + 1 + closeRel + len(closeTag)
	return text[tagStart:openEnd], text[end:], true
}

// hasMalformedSkillKind scans an XML attribute list that failed xml.Decoder in O(|tag|) time
// and reports whether it contains a kind attribute whose value is "skill" (quoted, unquoted,
// or inside an unclosed quote), without matching "kind=skill" inside another attribute's value.
func hasMalformedSkillKind(tag string) bool {
	i, n := 0, len(tag)
	for i < n {
		for i < n && (isTagSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		if i >= n {
			break
		}
		if tag[i] == '=' || tag[i] == '"' || tag[i] == '\'' {
			i++
			continue
		}
		nameStart := i
		for i < n && !isAttrNameDelim(tag[i]) {
			i++
		}
		name := tag[nameStart:i]
		if idx := strings.LastIndexByte(name, ':'); idx >= 0 {
			name = name[idx+1:]
		}
		j := i
		for j < n && isTagSpace(tag[j]) {
			j++
		}
		if j >= n || tag[j] != '=' {
			i = j
			continue
		}
		i = j + 1
		for i < n && isTagSpace(tag[i]) {
			i++
		}
		if i >= n {
			break
		}
		var val string
		quoted := false
		unclosed := false
		if tag[i] == '"' || tag[i] == '\'' {
			quoted = true
			q := tag[i]
			i++
			valStart := i
			for i < n && tag[i] != q {
				i++
			}
			val = tag[valStart:i]
			if i < n {
				i++
			} else {
				unclosed = true
			}
		} else {
			valStart := i
			for i < n && !isTagSpace(tag[i]) && tag[i] != '/' && tag[i] != '"' && tag[i] != '\'' && tag[i] != '=' {
				i++
			}
			k := i
			for k < n && isTagSpace(tag[k]) {
				k++
			}
			if k < n && tag[k] == '=' {
				// Previous attribute had no value before the next attribute (e.g. `bad= kind="skill"`).
				i = valStart
				continue
			}
			val = tag[valStart:i]
		}
		if strings.EqualFold(name, "kind") {
			if strings.EqualFold(val, "skill") {
				return true
			}
			if unclosed || (quoted && (strings.Contains(val, "=") || (i < n && !isTagSpace(tag[i]) && tag[i] != '/'))) {
				tok := val
				if k := strings.IndexAny(tok, " \t\n\r\f/'\""); k >= 0 {
					tok = tok[:k]
				}
				if strings.EqualFold(tok, "skill") {
					return true
				}
			}
		}
	}
	return false
}

// References reads only explicit leading context selections, never @name, paths, quoted tags or later prose.
func References(text string) ([]string, error) {
	var refs []string
	for {
		tag, rest, ok := nextLeadingContext(text)
		if !ok {
			return refs, nil
		}
		text = rest
		decoder := xml.NewDecoder(strings.NewReader("<context" + tag + "/>"))
		token, err := decoder.Token()
		if err != nil {
			if hasMalformedSkillKind(tag) {
				return nil, ErrReference
			}
			continue
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		skill := false
		for _, a := range start.Attr {
			skill = skill || strings.EqualFold(a.Name.Local, "kind") && strings.EqualFold(a.Value, "skill")
		}
		if !skill {
			continue
		}
		attrs := map[string]string{}
		for _, a := range start.Attr {
			key := a.Name.Local
			if _, duplicate := attrs[key]; duplicate || a.Name.Space != "" {
				return nil, ErrReference
			}
			switch key {
			case "kind", "title", "url", "ref":
			default:
				return nil, ErrReference
			}
			attrs[key] = a.Value
		}
		if attrs["kind"] != "skill" || attrs["ref"] == "" || len(attrs["ref"]) > 128 || len(refs) >= 16 {
			return nil, ErrReference
		}
		refs = append(refs, attrs["ref"])
	}
}

// Resolve rechecks every selected identity against the target's current inventory; never substitute by name.
func Resolve(text string, catalog []Entry) ([]Entry, error) {
	refs, err := References(text)
	if err != nil {
		return nil, err
	}
	var out []Entry
	seen := map[string]bool{}
	for _, ref := range refs {
		found := false
		for _, e := range catalog {
			if e.ID == ref {
				found = true
				if !seen[ref] {
					seen[ref] = true
					out = append(out, e)
				}
				break
			}
		}
		if !found {
			return nil, ErrReference
		}
	}
	return out, nil
}
