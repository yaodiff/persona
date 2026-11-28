package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const agyTranscriptPath = ".system_generated/logs/transcript.jsonl"
const agyFullTranscriptPath = ".system_generated/logs/transcript_full.jsonl"
const agyWakeReceipts = ".persona-observed-wakes"

type agyStepUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadTokens          int64 `json:"cache_read_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheWriteTokens         int64 `json:"cache_write_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheWrite1hTokens       int64 `json:"cache_write_1h_tokens"`
	ContextWindow            int   `json:"context_window"`
	ModelContextWindow       int   `json:"model_context_window"`
}

type agyToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type agyStep struct {
	Index                    *int            `json:"step_index"`
	Type                     string          `json:"type"`
	Source                   string          `json:"source"`
	Status                   string          `json:"status"`
	CreatedAt                string          `json:"created_at"`
	UpdatedAt                string          `json:"updated_at"`
	Content                  string          `json:"content"`
	ClientMsgID              string          `json:"client_msg_id"`
	Thinking                 string          `json:"thinking"`
	Error                    string          `json:"error"`
	Model                    string          `json:"model"`
	InputTokens              int64           `json:"input_tokens"`
	OutputTokens             int64           `json:"output_tokens"`
	CacheReadTokens          int64           `json:"cache_read_tokens"`
	CacheReadInputTokens     int64           `json:"cache_read_input_tokens"`
	CacheWriteTokens         int64           `json:"cache_write_tokens"`
	CacheCreationInputTokens int64           `json:"cache_creation_input_tokens"`
	CacheWrite1hTokens       int64           `json:"cache_write_1h_tokens"`
	ContextWindow            int             `json:"context_window"`
	ModelContextWindow       int             `json:"model_context_window"`
	Usage                    *agyStepUsage   `json:"usage"`
	TruncatedFields          []string        `json:"truncated_fields"`
	Media                    json.RawMessage `json:"media"`
	ToolCalls                []*agyToolCall  `json:"tool_calls"`
	seq                      int
	key                      string
	branch                   string
	record                   [32]byte
	rendered                 [32]byte
	backgroundStart          string
}

type agyStepMemo struct {
	seq      int
	key      string
	rendered [32]byte
	running  bool
}

type agyTranscriptUsage struct {
	model         string
	contextTokens int
	contextWindow int
	outputTokens  int
	compactions   int
	turns         int
	compacted     bool
	hasUsage      bool
	contextStats  transcript.ContextStats
	first         string // the first message the user typed, as shown (agyUserText): an untitled pane's title (agytitle.go)
	turn          agyTurnEvidence
	modTime       time.Time // transcript file mtime in UTC, used as fallback UpdatedAt for herdr agy panes
	size          int64     // transcript file size in bytes: a herdr agy pane row's Rev (proto.Session.Rev)
}

type agyHistory struct {
	token                string
	observedWakeRevision string
	seq                  int
	items                []proto.Item
	usage                agyTranscriptUsage
	// A compact export can leave full tool output in its step directory.
	outputs    map[string]int
	media      map[string]agyMedia
	stepHashes map[int][32]byte
}

type agyRevision struct {
	length                  int
	hash                    [32]byte
	branch, native, rewrite string
	steps                   []agyStepMemo
	full                    os.FileInfo // snapshot that supplied restored text
}

func isAgyStepRunning(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "RUNNING", "PENDING", "IN_PROGRESS", "QUEUED":
		return true
	default:
		return false
	}
}

// renderedHash hashes only the fields projected into transcript items so
// status-only transitions (such as RUNNING -> DONE) or unrendered metadata
// changes do not masquerade as history rewrites.
func (s agyStep) renderedHash() [32]byte {
	h := sha256.New()
	write := func(v string) {
		var lenBuf [8]byte
		n := len(v)
		for i := range lenBuf {
			lenBuf[i] = byte(n >> (i * 8))
		}
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(v))
	}
	write(s.Type)
	write(s.Source)
	write(s.Content)
	write(s.ClientMsgID)
	write(s.Thinking)
	write(s.Error)
	if s.Status == "ERROR" {
		write("ERROR")
	} else {
		write("")
	}
	write(strings.Join(s.TruncatedFields, "\x00"))
	write(string(bytes.TrimSpace(s.Media)))
	for _, tc := range s.ToolCalls {
		if tc == nil {
			write("")
			continue
		}
		write(tc.Name)
		write(string(bytes.TrimSpace(tc.Args)))
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

// agyStepsRewritten reports whether a prefix change or file truncation altered
// settled history, rather than normal RUNNING -> DONE progress or non-rendered
// metadata updates.
func agyStepsRewritten(oldSteps, newSteps []agyStepMemo) bool {
	if len(newSteps) < len(oldSteps) {
		return true
	}
	bySeq := make(map[int]agyStepMemo, len(newSteps))
	for _, s := range newSteps {
		bySeq[s.seq] = s
	}
	for _, prev := range oldSteps {
		cur, ok := bySeq[prev.seq]
		if !ok {
			return true
		}
		if cur.key != prev.key {
			return true
		}
		if cur.rendered != prev.rendered && !prev.running {
			return true
		}
	}
	return false
}

func agyDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:10])
}

// agyBrain opens exactly the conversation directory. The directory entry must be
// a real directory, and subsequent reads stay inside it even through symlinks.
func agyBrain(dir, id string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	path := filepath.Join("brain", id)
	entry, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !entry.IsDir() {
		return nil, os.ErrNotExist
	}
	brain, err := root.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	info, err := brain.Stat(".")
	if err != nil || !os.SameFile(entry, info) {
		brain.Close()
		return nil, os.ErrNotExist
	}
	return brain, nil
}

func agyReadFile(brain *os.Root, path string) ([]byte, error) {
	info, err := brain.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	return brain.ReadFile(path)
}

// parseAgySteps follows the native export rules in integrations/antigravity/lib/map.js.
// A same-step completion replaces its earlier record. A rewind invalidates the
// abandoned tail and gives replacement steps a new identity.
func parseAgySteps(raw []byte, conversation string) ([]agyStep, agyRevision) {
	f := agyFold{conversation: conversation}
	f.add(raw)
	return f.steps, f.revision(len(raw), sha256.Sum256(raw))
}

// agyFold is parseAgySteps' state after the complete lines it has read: the steps kept and the branch and native
// revisions so far. Each line folds in on its own, so lines appended later go on from it (agyParse, agypane.go).
type agyFold struct {
	conversation   string
	steps          []agyStep
	branch, native string
}

// add folds raw's lines in, raw starting at a line.
func (f *agyFold) add(raw []byte) {
	steps, rev := f.steps, agyRevision{branch: f.branch, native: f.native}
	conversation := f.conversation
	defer func() { f.steps, f.branch, f.native = steps, rev.branch, rev.native }()
	for line := range bytes.SplitSeq(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var step agyStep
		if json.Unmarshal(line, &step) != nil {
			continue
		}
		step.record = sha256.Sum256(line)
		step.rendered = step.renderedHash()
		if step.Index != nil && *step.Index >= 0 {
			step.seq = *step.Index + 1
		} else if len(steps) > 0 {
			step.seq = steps[len(steps)-1].seq + 1
		} else {
			step.seq = 1
		}
		at := len(steps)
		if len(steps) > 0 && step.seq <= steps[len(steps)-1].seq {
			last := steps[len(steps)-1]
			// A step whose sequence nothing retained has is a late arrival, not a rewind: parallel tool calls are
			// exported as they finish, so a call that fails at once lands before its sibling's result. It takes its
			// place in order, keeps the branch (every item ID) and marks the content as changed.
			floor := len(steps) - 1
			for floor >= 0 && steps[floor].seq > step.seq {
				floor--
			}
			updatesRunning := false
			if floor >= 0 {
				prev := steps[floor]
				if prev.seq == step.seq && step.Type == prev.Type && prev.CreatedAt == step.CreatedAt {
					updatesRunning = isAgyStepRunning(prev.Status)
					if prev.Type == "GENERIC" {
						step.backgroundStart = prev.backgroundStart
						if step.backgroundStart == "" && updatesRunning && agyBackgroundNotice(prev.Content) != "" {
							step.backgroundStart = prev.Content
						}
					}
				}
			}
			if step.seq < last.seq && (floor < 0 || steps[floor].seq != step.seq) {
				at = floor + 1
				rev.native = fmt.Sprintf("%x", step.record[:10])
			} else if step.seq < last.seq && (updatesRunning || step.backgroundStart != "") {
				// An earlier RUNNING step (such as a background tool call) finished after later steps were logged.
				// Update it in place without treating it as a branch rewind or discarding later steps.
				step.branch = steps[floor].branch
				step.key = steps[floor].key
				steps[floor] = step
				continue
			} else {
				if step.seq < last.seq || step.Type != last.Type || step.CreatedAt != last.CreatedAt {
					rev.branch = agyDigest(rev.branch + "\n" + string(line))
					rev.native = ""
				} else if !isAgyStepRunning(last.Status) && step.rendered != last.rendered {
					rev.native = fmt.Sprintf("%x", step.record[:10])
				}
				for len(steps) > 0 && steps[len(steps)-1].seq >= step.seq {
					steps = steps[:len(steps)-1]
				}
				at = len(steps)
			}
		}
		// Pane item IDs deliberately differ from the companion's. Mixing in the conversation
		// isolates IDs across pane session switches, and named suffixes survive step completions.
		step.branch = rev.branch
		step.key = strconv.Itoa(step.seq) + "@" + agyDigest(conversation+"\n"+step.CreatedAt+"\n"+rev.branch)
		steps = slices.Insert(steps, at, step)
	}
}

// revision is the fold's agyRevision for the length bytes it read, whose SHA-256 is hash.
func (f *agyFold) revision(length int, hash [32]byte) agyRevision {
	rev := agyRevision{length: length, hash: hash, branch: f.branch, native: f.native, steps: make([]agyStepMemo, len(f.steps))}
	for i, s := range f.steps {
		rev.steps[i] = agyStepMemo{
			seq:      s.seq,
			key:      s.key,
			rendered: s.rendered,
			running:  isAgyStepRunning(s.Status),
		}
	}
	return rev
}

// Match the whitespace used by JavaScript's \s and String.trim in cleanUserText.
const agyJSWhitespace = `\x09-\x0D\x20\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
const agySpace = `[` + agyJSWhitespace + `]`

var agyUserRequest = lazyre.New(`(?s)<USER_REQUEST>` + agySpace + `*(.*?)` + agySpace + `*</USER_REQUEST>`)

// Go regexp has no backreferences. Use one alternative per context tag (compat.ContextNames: the current and older
// names, as the companion's SNAPSHOT_RE).
var agySnapshot = lazyre.New(`(?s)(?:` + agyContextBlocks() + `|<persona-view-errors\b[^>]*>.*?</persona-view-errors>)` + agySpace + `*`)

// Keep s/S ASCII-only as in the companion's case-insensitive regexp (Go's (?i) would also match ſ).
var agyContextEnd = lazyre.New(`^` + agySpace + `*(?i:</)(?:` + agyContextEndNames() + `)(?i:>)` + agySpace + `*`)

func agyContextBlocks() string {
	var alt []string
	for _, n := range compat.ContextNames() {
		q := regexp.QuoteMeta(n)
		alt = append(alt, `<`+q+`>.*?</`+q+`>`)
	}
	return strings.Join(alt, "|")
}

func agyContextEndNames() string {
	var alt []string
	for _, n := range compat.ContextNames() {
		var b strings.Builder
		for _, r := range n {
			switch {
			case r == 's' || r == 'S':
				b.WriteString(`[sS]`)
			case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
				b.WriteString(`(?i:` + string(r) + `)`)
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		alt = append(alt, b.String())
	}
	return strings.Join(alt, "|")
}

// Antigravity writes embedded ACP resources into USER_INPUT.content. Require the complete
// Persona envelope, with a non-empty URI containing no whitespace, so a quoted label or
// another URI scheme keeps its text. Workflow prompts carry the resource after the user
// body. The close is found by depth, not the last close tag: literal context tags inside a
// snapshot stay part of it, while a paragraph that starts after its first nested close is
// kept as the user's text.
var (
	agyContextResourceLabel = lazyre.New(`Content of persona-context://[^\s<>]+:\r?\n`)
	agyContextResourceOpen  = lazyre.New(`(?i)^<` + compat.ContextPattern() + `\b[^>]*>`)
	agyContextResourceTag   = lazyre.New(`(?i)<` + compat.ContextPattern() + `\b[^>]*>|</` + compat.ContextPattern() + `\s*>`)
	agyViewErrorsTrailing   = lazyre.New(`(?is)(?:^|\n)<persona-view-errors\b[^>]*>.*?</persona-view-errors>` + agySpace + `*$`)
)

var agyTrimSpace = lazyre.New(`^` + agySpace + `+|` + agySpace + `+$`)

var agyInterruptedError = lazyre.New(`(?is)^(?:api error:\s*)?the stream was interrupted\.?(?:\s+please continue\b.*)?$`)

func isAgyInterruptedStep(step agyStep) bool {
	if step.Type != "ERROR_MESSAGE" {
		return false
	}
	text := step.Error
	if text == "" {
		text = step.Content
	}
	return agyInterruptedError.MatchString(strings.TrimSpace(text))
}

func isAgyCompactCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	return len(fields) > 0 && strings.EqualFold(fields[0], "/compact")
}

func agyUserText(text string) string {
	if m := agyUserRequest.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	for {
		clean := agyStripContextResources(text)
		clean = agyViewErrorsTrailing.ReplaceAllString(clean, "")
		clean = agySnapshot.ReplaceAllString(clean, "")
		clean = agyContextEnd.ReplaceAllString(clean, "")
		if clean == text {
			return agyTrimSpace.ReplaceAllString(text, "")
		}
		text = clean
	}
}

func agyStripContextResources(text string) string {
	for start := 0; start < len(text); {
		label := agyContextResourceLabel.FindStringIndex(text[start:])
		if label == nil {
			break
		}
		at := start + label[0]
		labelEnd := start + label[1]
		if at > 0 && text[at-1] != '\n' {
			start = labelEnd
			continue
		}
		openAt := labelEnd
		open := agyContextResourceOpen.FindStringIndex(text[openAt:])
		if open == nil || open[0] != 0 {
			start = openAt
			continue
		}
		end, ok := agyContextResourceEnd(text, openAt, strings.TrimSpace(text[:at]) != "")
		if !ok {
			start = openAt
			continue
		}
		before := strings.TrimRight(text[:at], "\r\n")
		after := strings.TrimLeft(text[end:], "\r\n")
		switch {
		case before == "":
			text = after
		case after == "":
			text = before
		default:
			text = before + "\n\n" + after
		}
		start = len(before)
	}
	return text
}

func agyContextResourceEnd(text string, open int, allowUserBoundary bool) (int, bool) {
	depth, firstNestedClose := 0, -1
	for _, tag := range agyContextResourceTag.FindAllStringIndex(text[open:], -1) {
		start, end := open+tag[0], open+tag[1]
		raw := strings.TrimSpace(text[start:end])
		if strings.HasPrefix(raw, "</") {
			depth--
			if depth < 0 {
				return 0, false
			}
			if depth == 1 && firstNestedClose < 0 {
				firstNestedClose = end
			}
			if depth == 0 {
				if allowUserBoundary && firstNestedClose >= 0 && agyContextResourceHasUserParagraph(text[firstNestedClose:end]) {
					return firstNestedClose, true
				}
				return end, true
			}
		} else if !strings.HasSuffix(raw, "/>") {
			depth++
		}
	}
	return 0, false
}

// A nested close that is followed by a blank line and prose belongs to the user's own text,
// not the snapshot: snapshots' literal context tags are tag-like or adjacent to their text.
// Stopping at the nested close keeps ordinary prose rather than deleting it with the resource.
func agyContextResourceHasUserParagraph(text string) bool {
	blank := false
	for _, line := range strings.Split(text, "\n")[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blank = true
			continue
		}
		if blank && !strings.HasPrefix(trimmed, "<") {
			return true
		}
		blank = false
	}
	return false
}

func agyToolOutput(text string) string {
	if stripped := transcript.StripToolResultTimestampHeaders(text); stripped != text {
		return stripped
	}
	if strings.HasPrefix(text, "Created At:") {
		if _, output, ok := strings.Cut(text, "\nOutput:\n"); ok {
			return output
		}
	}
	return text
}

var (
	agyFenceRe    = lazyre.New(`^((?:[ \t]*>[ \t]?)*)[ \t]*(?:(?:[-*+]|\d{1,9}[.)])[ \t]+)?[ \t]*(` + "`{3,}" + `|~{3,})(.*)$`)
	agyListItemRe = lazyre.New(`^(?:[ \t]*>)*[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]`)
	agyQuotePreRe = lazyre.New(`^(?:[ \t]*>[ \t]?)+`)
	agyRefDefRe   = lazyre.New(`^([ ]{0,3}\[[^\]\n]+\]:[ \t]+)(<([^>\r\n]+)>|(\S+))(.*)$`)
	agyLinkTailRe = lazyre.New(`^[ \t]*(?:(?:"(?:\\[\s\S]|[^"\\])*"|'(?:\\[\s\S]|[^'\\])*'|\((?:\\[\s\S]|[^)\\])*\))[ \t]*)?\)`)
	agyWinDriveRe = lazyre.New(`^/([A-Za-z])(?::|%3[Aa])/(.+)$`)
)

func agyLocalFileURLToPath(dest string) string {
	if len(dest) <= 8 || !strings.EqualFold(dest[:7], "file://") {
		return ""
	}
	for i := 0; i < len(dest); i++ {
		if dest[i] < 0x20 || dest[i] == 0x7f {
			return ""
		}
	}
	if strings.Contains(strings.ToLower(dest), "%00") {
		return ""
	}
	raw, frag := dest[7:], ""
	if hash := strings.IndexByte(raw, '#'); hash >= 0 {
		raw, frag = raw[:hash], raw[hash:]
	}
	if strings.ContainsRune(raw, '?') {
		return ""
	}
	var p string
	switch {
	case strings.HasPrefix(raw, "/"):
		p = raw
	case len(raw) >= 10 && strings.EqualFold(raw[:10], "localhost/"):
		p = raw[9:]
	default:
		return ""
	}
	if p == "/" || strings.HasPrefix(p, "//") {
		return ""
	}
	decoded := p
	if u, err := url.PathUnescape(p); err == nil {
		decoded = u
	}
	lower := strings.ToLower(decoded)
	if lower == "/api/v1" || strings.HasPrefix(lower, "/api/v1/") {
		return ""
	}
	if m := agyWinDriveRe.FindStringSubmatch(p); m != nil {
		return m[1] + ":/" + m[2] + frag
	}
	return p + frag
}

func agyUnescapedBracket(line string, idx int) bool {
	slashes := 0
	for i := idx - 1; i >= 0 && line[i] == '\\'; i-- {
		slashes++
	}
	return slashes%2 == 0
}

func agyRewriteFileLinksLine(line string) string {
	if !strings.Contains(strings.ToLower(line), "file://") {
		return line
	}
	if def := agyRefDefRe.FindStringSubmatch(line); def != nil {
		dest, angled := def[4], false
		if def[3] != "" {
			dest, angled = def[3], true
		}
		if next := agyLocalFileURLToPath(dest); next != "" {
			if angled {
				return def[1] + "<" + next + ">" + def[5]
			}
			return def[1] + next + def[5]
		}
	}
	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] == '`' {
			n := 0
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			j, closed := i+n, -1
			for j < len(line) {
				if line[j] == '`' {
					m := 0
					for j+m < len(line) && line[j+m] == '`' {
						m++
					}
					if m == n {
						closed = j + m
						break
					}
					j += m
				} else {
					j++
				}
			}
			end := i + n
			if closed >= 0 {
				end = closed
			}
			out.WriteString(line[i:end])
			i = end
			continue
		}
		if line[i] == ']' && i+1 < len(line) && line[i+1] == '(' && agyUnescapedBracket(line, i) {
			j := i + 2
			for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
			if j < len(line) && line[j] == '<' {
				k := j + 1
				for k < len(line) && line[k] != '>' && line[k] != '<' {
					if line[k] == '\\' && k+1 < len(line) {
						k += 2
					} else {
						k++
					}
				}
				if k < len(line) && line[k] == '>' && agyLinkTailRe.MatchString(line[k+1:]) {
					if next := agyLocalFileURLToPath(line[j+1 : k]); next != "" {
						out.WriteString(line[i:j])
						out.WriteByte('<')
						out.WriteString(next)
						out.WriteByte('>')
						i = k + 1
						continue
					}
				}
			} else {
				k, depth := j, 0
				for k < len(line) {
					ch := line[k]
					if ch == '\\' && k+1 < len(line) {
						k += 2
						continue
					}
					if ch == ' ' || ch == '\t' || ch == '\r' {
						break
					}
					if ch == '(' {
						depth++
						k++
					} else if ch == ')' {
						if depth == 0 {
							break
						}
						depth--
						k++
					} else {
						k++
					}
				}
				if k > j && depth == 0 && agyLinkTailRe.MatchString(line[k:]) {
					if next := agyLocalFileURLToPath(line[j:k]); next != "" {
						out.WriteString(line[i:j])
						out.WriteString(next)
						i = k
						continue
					}
				}
			}
		}
		out.WriteByte(line[i])
		i++
	}
	return out.String()
}

// agyAssistantText mirrors cleanAssistantText in integrations/antigravity/lib/file-links.js:
// Antigravity assistant replies emit local file:// links and images, which Persona clients
// exclude at the generic Markdown boundary (#12102, #12296). Normalize local file:// links
// outside code blocks and inline code spans into plain machine paths.
func agyAssistantText(raw string) string {
	if !strings.Contains(strings.ToLower(raw), "file://") {
		return raw
	}
	lines := strings.Split(raw, "\n")
	var fenceCh byte
	fenceLen := 0
	blank, inList, inIndented := true, false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		indented := strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
		if fenceLen > 0 {
			rest := strings.TrimSpace(agyQuotePreRe.ReplaceAllString(line, ""))
			if len(rest) >= fenceLen && rest[0] == fenceCh && strings.Trim(rest, string(fenceCh)) == "" {
				fenceLen = 0
			}
			blank = false
			continue
		}
		if inIndented {
			if trimmed == "" || indented {
				continue
			}
			inIndented = false
		}
		if trimmed != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inList = agyListItemRe.MatchString(line) || (inList && !blank)
		}
		if open := agyFenceRe.FindStringSubmatch(line); open != nil && !(open[2][0] == '`' && strings.ContainsRune(open[3], '`')) {
			fenceCh, fenceLen = open[2][0], len(open[2])
			blank = false
			continue
		}
		if blank && !inList && trimmed != "" && indented {
			inIndented = true
			continue
		}
		blank = trimmed == ""
		lines[i] = agyRewriteFileLinksLine(line)
	}
	return strings.Join(lines, "\n")
}

// Match the opaque, branch-specific native step identities written by the AGY companion.
func (s agyStep) wakeKey() string {
	index, identity, fallback := "", "", ""
	if s.Index != nil {
		index = strconv.Itoa(*s.Index)
	}
	if s.CreatedAt != "" || s.branch != "" {
		identity = agyDigest(s.CreatedAt + "\n" + s.branch)
	}
	if s.CreatedAt == "" {
		fallback = s.Content
	}
	return agyDigest(strings.Join([]string{index, s.Type, identity, fallback}, "\n"))
}

func mapAgySteps(steps []agyStep, fallback time.Time, receipts map[string]bool) agyHistory {
	var history agyHistory
	if len(steps) > 0 {
		history.stepHashes = make(map[int][32]byte, len(steps))
	}
	var pending []string
	if fallback.IsZero() {
		fallback = time.Now()
	}
	fallback = fallback.UTC()
	var delivered, priorWakeKey string
	var matchedWakes []string
	var notified bool
	for _, step := range steps {
		history.seq = step.seq
		history.stepHashes[step.seq] = step.rendered
		at, err := time.Parse(time.RFC3339Nano, step.CreatedAt)
		if err != nil || at.IsZero() {
			at = fallback
		}
		add := func(suffix, role, kind, text string) *proto.Item {
			history.items = append(history.items, proto.Item{ID: step.key + "#" + suffix, Seq: step.seq,
				Role: role, Kind: kind, TS: at, Text: text})
			return &history.items[len(history.items)-1]
		}
		addMedia := func(role, toolUseID, clientMsgID string, attached map[string]bool) {
			var entries []json.RawMessage
			if len(step.Media) == 0 || json.Unmarshal(step.Media, &entries) != nil {
				return
			}
			for index, entry := range entries {
				var media agyMedia
				if json.Unmarshal(entry, &media) != nil || !media.isImage() || attached[agyImagePath(media.URI)] {
					continue
				}
				image := add("media"+strconv.Itoa(index), role, proto.KindImage, "")
				image.ToolUseID, image.ClientMsgID = toolUseID, clientMsgID
				image.Image = &proto.Image{Mime: media.Mime, Lazy: true}
				if history.media == nil {
					history.media = make(map[string]agyMedia)
				}
				history.media[image.ID] = media
			}
		}
		// Per the companion projection, a system message starts a new exchange only once work resumes.
		// Block -1 precedes that work at its sequence, even if a watch already passed the notification.
		plannerWork := step.Type == "PLANNER_RESPONSE" && (strings.TrimSpace(step.Content) != "" ||
			strings.TrimSpace(step.Thinking) != "" || len(step.ToolCalls) != 0)
		if plannerWork || step.Type == "GENERIC" || (step.Type == "ERROR_MESSAGE" && !isAgyInterruptedStep(step)) {
			observed := false
			if delivered != "" && len(receipts) > 0 {
				link := priorWakeKey + " " + step.wakeKey()
				if receipts[link] {
					observed = true
					matchedWakes = append(matchedWakes, link)
				}
			}
			if delivered != "" && (notified || observed) {
				history.items = append(history.items, proto.Item{ID: delivered + "#-1", Seq: step.seq, TS: at,
					Role: "system", Kind: proto.KindNotice, Wake: true, Text: transcript.BackgroundTaskUpdate})
			}
			delivered, notified = "", false
			if step.Type == "PLANNER_RESPONSE" && step.Status == "DONE" && strings.TrimSpace(step.Content) != "" && len(step.ToolCalls) == 0 {
				delivered = step.key
				if len(receipts) > 0 {
					priorWakeKey = step.wakeKey()
				}
			}
		}
		// A call's result follows its planner step. One that never came (interrupted, dropped) must not stay queued
		// to take the next result, which would shift every later result onto the wrong call.
		switch step.Type {
		case "PLANNER_RESPONSE", "USER_INPUT", "ERROR_MESSAGE":
			pending = pending[:0]
		}
		switch step.Type {
		case "SYSTEM_MESSAGE":
			notified = delivered != ""
		case "USER_INPUT":
			if step.Source == "" || step.Source == "USER_EXPLICIT" {
				delivered, notified = "", false
				text := agyUserText(step.Content)
				if isAgyCompactCommand(text) {
					add("0", "system", proto.KindNotice, transcript.CompactNotice).ClientMsgID = step.ClientMsgID
					break
				}
				history.usage.turns++
				if text != "" {
					add("0", "user", proto.KindText, text).ClientMsgID = step.ClientMsgID
				}
				addMedia("user", "", step.ClientMsgID, agyAttachedImagePaths(text))
			}
		case "CHECKPOINT":
			pre := history.usage.contextTokens
			if _, stepCtx, _, ok := step.tokenUsage(); ok && stepCtx > 0 {
				pre = stepCtx
			}
			history.usage.observeCheckpoint()
			it := add("0", "system", proto.KindNotice, transcript.CompactedNotice)
			if pre > 0 {
				it.Compact = &proto.Compact{PreTokens: pre}
			}
		case "PLANNER_RESPONSE":
			stepModel := step.normalizedModel()
			stepUsage, hasUsage := history.usage.observePlanner(step)
			firstIdx := len(history.items)
			for _, part := range []struct{ kind, text string }{{proto.KindThinking, step.Thinking}, {proto.KindText, step.Content}} {
				if text := strings.TrimSpace(part.text); text != "" {
					if part.kind == proto.KindText {
						text = agyAssistantText(text)
					}
					it := add(part.kind, "assistant", part.kind, text)
					it.Model = stepModel
				}
			}
			truncatedCalls := slices.Contains(step.TruncatedFields, "tool_calls")
			for k, call := range step.ToolCalls {
				if call == nil {
					continue
				}
				name, input := agyToolInput(call.Name, call.Args, truncatedCalls)
				callID := fmt.Sprintf("call_%s_%d", step.key, k)
				it := add("tool"+strconv.Itoa(k), "assistant", proto.KindToolUse, "")
				it.Model = stepModel
				it.ToolName, it.ToolInput, it.ToolUseID = name, input, callID
				pending = append(pending, callID)
			}
			if hasUsage && len(history.items) > firstIdx {
				history.items[firstIdx].Usage = []proto.TokenUsage{stepUsage}
			}
		case "GENERIC":
			callID := "call_" + step.key + "_0"
			if len(pending) != 0 {
				callID, pending = pending[0], pending[1:]
			}
			text := agyToolOutput(step.Content)
			if step.Error != "" {
				text = step.Error
			}
			it := add("0", "user", proto.KindToolResult, text)
			it.ToolUseID, it.IsError = callID, step.Error != "" || step.Status == "ERROR"
			for _, field := range step.TruncatedFields {
				if field == "content" && step.Error == "" {
					if history.outputs == nil {
						history.outputs = make(map[string]int)
					}
					history.outputs[it.ID] = step.seq - 1
					break
				}
			}
			addMedia("assistant", callID, "", nil)
		case "ERROR_MESSAGE":
			if isAgyInterruptedStep(step) {
				break
			}
			text := step.Error
			if text == "" {
				text = step.Content
			}
			if text == "" {
				text = "Error"
			}
			if !strings.HasPrefix(text, "API Error:") {
				text = "API Error: " + text
			}
			add("0", "assistant", proto.KindText, text)
		}
	}
	if len(matchedWakes) > 0 {
		history.observedWakeRevision = agyDigest(strings.Join(matchedWakes, "\n"))
	}
	return history
}

func agyAttachedImagePaths(text string) map[string]bool {
	var paths map[string]bool
	for {
		_, rest, open := strings.Cut(text, "<attached_images>")
		block, tail, closed := strings.Cut(rest, "</attached_images>")
		if !open || !closed {
			return paths
		}
		for line := range strings.SplitSeq(block, "\n") {
			if path := agyImagePath(strings.TrimSpace(line)); path != "" {
				if paths == nil {
					paths = make(map[string]bool)
				}
				paths[path] = true
			}
		}
		text = tail
	}
}

var agyToolNames = map[string]string{
	"run_command": "Bash", "view_file": "Read", "read_file": "Read", "write_to_file": "Write", "write_file": "Write",
	"replace_file_content": "Edit", "edit_file": "Edit", "grep_search": "Grep", "find_by_name": "Glob",
	"search_web": "WebSearch", "read_url_content": "WebFetch", "ask_question": "AskUserQuestion", "invoke_subagent": "Agent",
	"bash": "Bash", "read": "Read", "write": "Write", "edit": "Edit", "multi_edit": "MultiEdit", "ls": "LS", "grep": "Grep", "glob": "Glob",
	"web_fetch": "WebFetch", "web_search": "WebSearch", "todo_write": "TodoWrite",
}

// agyArgPreview splits a clipped argument at the exporter's marker. A zero
// omitted count means the value has no valid marker.
func agyArgPreview(value string) (head, tail string, omitted int) {
	match := agyTailTruncation.FindStringSubmatchIndex(value)
	if match == nil {
		match = agyTruncation.FindStringSubmatchIndex(value)
	}
	if match != nil {
		if n, err := strconv.Atoi(value[match[2]:match[3]]); err == nil && n > 0 {
			return value[:match[0]], value[match[1]:], n
		}
	}
	return value, "", 0
}

// agyDecodeArgString decodes one JSON string, closing a clipped prefix after
// dropping any escape the exporter cut in half.
func agyDecodeArgString(value string, clipped bool) (string, bool) {
	if !strings.HasPrefix(value, `"`) {
		return "", false
	}
	if clipped {
		if slash := strings.LastIndexByte(value, '\\'); slash >= 0 {
			escape := value[slash+1:]
			if escape == "" || strings.HasPrefix(escape, "u") && len(escape) < 5 {
				start := slash
				for start > 0 && value[start-1] == '\\' {
					start--
				}
				if (slash-start)%2 == 0 {
					value = value[:slash]
				}
			}
		}
		value += `"`
	}
	var decoded string
	err := json.Unmarshal([]byte(value), &decoded)
	return decoded, err == nil
}

// agyNormalizeArgValue copies the arguments so a failed decode leaves the original
// call available. Older exports can contain literal quotes in plain arguments.
func agyNormalizeArgValue(v any, truncated, encoded bool) (any, bool) {
	switch v := v.(type) {
	case string:
		head, tail, omitted := v, "", 0
		if truncated {
			head, tail, omitted = agyArgPreview(v)
		}
		if encoded {
			if decoded, ok := agyDecodeArgString(head, omitted > 0); ok {
				if omitted > 0 {
					// The missing text and any retained suffix are counted in encoded bytes.
					return decoded + transcript.ApproximateClipNote(len(decoded)+omitted+len(tail)), true
				}
				return decoded, true
			}
			// Compact exports also JSON-encode non-string values (e.g. "1", "true", "null", "[...]").
			if omitted == 0 {
				var value any
				if err := json.Unmarshal([]byte(head), &value); err == nil {
					return value, true
				}
			}
			return nil, false
		}
		if omitted > 0 {
			return head + transcript.ClipNote(len(head)+omitted+len(tail)), true
		}
	case []any:
		out := make([]any, len(v))
		for i, elem := range v {
			normalized, ok := agyNormalizeArgValue(elem, truncated, encoded)
			if !ok {
				return nil, false
			}
			out[i] = normalized
		}
		return out, true
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, elem := range v {
			normalized, ok := agyNormalizeArgValue(elem, truncated, encoded)
			if !ok {
				return nil, false
			}
			out[k] = normalized
		}
		return out, true
	}
	return v, true
}

func agyTool(name string, raw json.RawMessage) (string, json.RawMessage) {
	return agyToolInput(name, raw, false)
}

func agyToolInput(name string, raw json.RawMessage, truncated bool) (string, json.RawMessage) {
	if name == "" {
		name = "tool"
	}
	appName := agyToolNames[name]
	if appName == "" {
		appName = name
	}
	if raw = bytes.TrimSpace(raw); len(raw) > 0 && raw[0] == '[' {
		// Kept as an array, as the companion does; one too long to send is bounded like any other input, under "args".
		wrapped := append(append([]byte(`{"args":`), raw...), '}')
		if clipped := transcript.ClipToolInput(wrapped); len(clipped) < len(wrapped) {
			return appName, clipped
		}
		return appName, raw
	}
	args, ok := agyDecodeToolArgs(raw)
	if !ok {
		return appName, json.RawMessage("{}")
	}
	// The compact export wraps every string argument in a JSON string literal.
	// Decode only when the whole call fits that format.
	if decoded, ok := agyNormalizeArgValue(args, truncated, true); ok {
		args = decoded.(map[string]any)
	} else if truncated {
		preview, _ := agyNormalizeArgValue(args, true, false)
		args = preview.(map[string]any)
	}
	first := func(keys ...string) any {
		for _, key := range keys {
			if v := args[key]; v != nil {
				return v
			}
		}
		return ""
	}
	switch name {
	case "run_command":
		args = map[string]any{"command": first("CommandLine", "command"), "description": first("toolSummary", "Cwd")}
	case "view_file", "read_file":
		args = map[string]any{"file_path": first("AbsolutePath", "Path", "file_path")}
	case "write_to_file", "write_file":
		args = map[string]any{"file_path": first("TargetFile", "Path", "file_path"), "content": first("CodeContent", "Content", "content")}
	case "replace_file_content", "edit_file":
		args = map[string]any{"file_path": first("TargetFile", "Path", "file_path"), "old_string": first("TargetContent", "Target", "old_string"),
			"new_string": first("ReplacementContent", "Replacement", "new_string")}
	case "grep_search":
		args = map[string]any{"pattern": first("Query", "Pattern", "pattern"), "path": first("SearchPath", "Path", "path")}
	case "find_by_name":
		args = map[string]any{"pattern": first("Pattern", "pattern"), "path": first("SearchDirectory", "Path", "path")}
	case "search_web":
		args = map[string]any{"query": first("query", "Query")}
	case "read_url_content":
		args = map[string]any{"url": first("Url", "url")}
	case "invoke_subagent":
		description := args["toolSummary"]
		if description == nil {
			description = "subagent"
		}
		// An absent Subagents field renders as an empty prompt. The companion JSON-encodes its empty fallback.
		prompt := first("Subagents")
		if _, ok := prompt.(string); !ok {
			encoded, _ := json.Marshal(prompt)
			prompt = string(encoded)
		}
		args = map[string]any{"description": description, "prompt": prompt}
	}
	encoded, _ := json.Marshal(args)
	return appName, transcript.ClipToolInput(encoded)
}
