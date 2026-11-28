package wire

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	ItemsMost  = 300
	StringMost = 2000
	StateMost  = 16 << 10
	SeriesMost = 4
	PointsMost = 120
	StatsMost  = 6
	// The code a view carries (code texts and diff lines, UI version 4) is at most CodeMost bytes, and a diff at most
	// DiffLinesMost lines: an extension pages the rest (code's load, diff's collapsed and load).
	CodeMost      = 4 << 20
	DiffLinesMost = 20000
)

var tones = append([]string{""}, Tones...)

// CheckView holds a View to what the apps draw: first to the schema (known component types with what they need, short
// strings, https links, tones and styles from the fixed lists), then to what the schema cannot say (a page's size, a
// field's name once, a chart's series alike). A View that does not fit is refused, not half drawn.
func CheckView(v *View) error { return checkView(v, false) }

// CheckFitted is CheckView for a view the hub fitted for an older app (extension.Fit): the same, but the names the hub
// gives what it draws such an app (ReservedName) are let through.
func CheckFitted(v *View) error { return checkView(v, true) }

func checkView(v *View, hubNames bool) error {
	if v == nil {
		return errors.New("no view")
	}
	if err := Validate("View", v); err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			return errors.New(where(v, ve.Errors[0]))
		}
		return err
	}
	return checkViewSemantics(v, hubNames)
}

// CheckViewJSON validates the original native JSON before decoding it for semantic checks.
// Unlike CheckView, it does not re-encode Component: optional zero values and required
// empty fields must not disappear at a stream's validation boundary. Legacy encoding
// and CheckView behavior are unchanged. Stream callers also apply UIStreamView's overlay.
func CheckViewJSON(raw []byte) error { return checkViewJSON(raw, false) }

// CheckFittedJSON retains native source presence while allowing only fitter-owned names.
func CheckFittedJSON(raw []byte) error { return checkViewJSON(raw, true) }

func checkViewJSON(raw []byte, hubNames bool) error {
	_, err := decodeCheckedView(raw, hubNames)
	return err
}

// DecodeCheckedViewJSON is CheckViewJSON that also gives the View it decoded
// from raw and checked, for a caller that would decode raw again.
func DecodeCheckedViewJSON(raw []byte) (View, error) { return decodeCheckedView(raw, false) }

// CheckValidatedViewJSON is CheckViewJSON for raw that already holds to View,
// such as the view of a frame validated as UIStreamSnapshot (whose view is
// UIStreamView, View narrowed): it decodes raw and checks its semantics
// without walking the View schema again.
func CheckValidatedViewJSON(raw []byte) error {
	var v View
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return checkViewSemantics(&v, false)
}

// DecodeCheckedStreamViewJSON validates raw as a UI stream's view
// (UIStreamView), then decodes and checks it as DecodeCheckedViewJSON does.
// UIStreamView is View ($ref) narrowed by sibling keywords, and check holds a
// value to a $ref's target before its siblings, so a view that holds to
// UIStreamView holds to View: View is not validated a second time.
func DecodeCheckedStreamViewJSON(raw []byte) (View, error) {
	if err := ValidateJSON("UIStreamView", raw); err != nil {
		return View{}, err
	}
	var v View
	if err := json.Unmarshal(raw, &v); err != nil {
		return View{}, err
	}
	return v, checkViewSemantics(&v, false)
}

func decodeCheckedView(raw []byte, hubNames bool) (View, error) {
	if err := ValidateJSON("View", raw); err != nil {
		return View{}, err
	}
	var v View
	if err := json.Unmarshal(raw, &v); err != nil {
		return View{}, err
	}
	return v, checkViewSemantics(&v, hubNames)
}

func checkViewSemantics(v *View, hubNames bool) error {
	if len(v.State) > StateMost {
		return fmt.Errorf("state: more than %d bytes", StateMost)
	}
	for _, a := range Actions(v) {
		if a.Push != nil && len(a.Push.State) > StateMost {
			return fmt.Errorf("action %s: the state it pushes is more than %d bytes", a.ID, StateMost)
		}
	}
	n := 0
	pc := &pageCheck{names: map[string]bool{}, keys: map[string]bool{}, hubNames: hubNames}
	names, keys := pc.names, pc.keys
	if v.Search != nil {
		names["q"] = true
	}
	if t := v.Tabs; t != nil {
		if names[t.Name] {
			return fmt.Errorf("tabs: field %q twice (q is the search's)", t.Name)
		}
		names[t.Name] = true
		if !slices.ContainsFunc(t.Items, func(c Tab) bool { return c.Value == t.Value }) {
			return errors.New("tabs: value is one of its items")
		}
	}
	if r := v.Review; r != nil {
		if names[r.Name] {
			return fmt.Errorf("review: field %q twice", r.Name)
		}
		names[r.Name] = true
	}
	code := 0
	if f := v.Filter; f != nil {
		if names[f.Name] {
			return fmt.Errorf("filter: field %q twice (q is the search's)", f.Name)
		}
		names[f.Name] = true
		if !slices.ContainsFunc(f.Choices, func(c Choice) bool { return c.Value == f.Value }) {
			return errors.New("filter: value is one of its choices")
		}
	}
	for i, s := range v.Sections {
		for j, e := range s.Items {
			// a grid's or a carousel's tiles, a graph's nodes, a scene's pins and a map's markers are components of the page each
			if n += max(1, len(e.Tiles)+len(e.Nodes)+len(e.Pins)+len(e.Markers)); n > ItemsMost {
				return fmt.Errorf("more than %d items", ItemsMost)
			}
			var err error
			if e.Key != "" {
				if keys[e.Key] {
					return fmt.Errorf("section %d item %d (%s): key %q twice", i, j, e.Type, e.Key)
				}
				keys[e.Key] = true
			}
			switch e.Type {
			case "link":
				err = CheckHTTPS(e.URL, "link")
			case "field":
				switch {
				case e.Kind == "choice" && len(e.Choices) == 0:
					err = errors.New("a choice field needs choices")
				case e.Kind == "secret" && e.Value != "":
					err = errors.New("a secret field is never filled in")
				default:
					err = pc.name(e.Name)
				}
			case "segmented":
				switch {
				case pc.name(e.Name) != nil:
					err = fmt.Errorf("field %q twice, or a reserved name", e.Name)
				case !slices.ContainsFunc(e.Choices, func(c Choice) bool { return c.Value == e.Value }):
					err = errors.New("segmented: value is one of its choices")
				case slices.ContainsFunc(e.Choices, func(c Choice) bool { return utf8.RuneCountInString(cmp.Or(c.Title, c.Value)) > 12 }):
					err = errors.New("segmented: a choice has at most 12 characters")
				}
			case "chart":
				err = checkChart(e)
			case "stat_grid":
				err = checkStats(e.Stats)
			case "row":
				if (e.Icon != "") && (e.Avatar != nil || e.Thumbnail != nil) || e.Avatar != nil && e.Thumbnail != nil {
					err = errors.New("a row has at most one of icon, avatar and thumbnail")
				}
			case "table":
				for k, r := range e.Rows {
					if len(r) != len(e.Columns) {
						err = fmt.Errorf("row %d has %d cells for %d columns", k, len(r), len(e.Columns))
						break
					}
				}
			case "markdown":
				err = checkMarkdown(e.Text)
			case "composer":
				err = pc.name(e.Name)
			case "code":
				code += len(e.Text)
				if h := e.Highlight; h != nil && h.End < h.Start {
					err = errors.New("highlight: end is not before start")
				} else if e.LineURL != "" {
					err = checkLineURL(e.LineURL)
				}
			case "diff":
				var lines int
				lines, err = checkDiff(e)
				code += lines
			case "comment_thread":
				err = checkComments(e.Comments)
			}
			if err == nil {
				err = pc.checkV6(&e)
			}
			if err == nil {
				err = pc.checkV10(&e)
			}
			if err == nil {
				err = pc.checkV11(&e)
			}
			if err == nil {
				err = pc.checkV12(&e)
			}
			if err == nil && code > CodeMost {
				err = fmt.Errorf("a view carries at most %d bytes of code: page the rest (load)", CodeMost)
			}
			if err != nil {
				return fmt.Errorf("section %d item %d (%s): %v", i, j, e.Type, err)
			}
		}
	}
	return nil
}

// checkDiff holds a diff's lines to their prefixes and its size to DiffLinesMost, its threads' bodies to markdown's
// subset. It says how many bytes of lines it has.
func checkDiff(e Component) (int, error) {
	n, size := 0, 0
	for k, h := range e.Hunks {
		for l, line := range h.Lines {
			if line != "" && !strings.ContainsAny(line[:1], " +-\\") {
				return 0, fmt.Errorf("hunk %d line %d: starts with ' ', '+', '-' or '\\'", k, l)
			}
			size += len(line)
		}
		if n += len(h.Lines); n > DiffLinesMost {
			return 0, fmt.Errorf("a diff has at most %d lines: collapse it and load it, or split it", DiffLinesMost)
		}
	}
	for k, t := range e.Threads {
		if t.StartLine > t.Line {
			return 0, fmt.Errorf("thread %d: start_line is not after line", k)
		}
		if err := checkComments(t.Comments); err != nil {
			return 0, fmt.Errorf("thread %d: %v", k, err)
		}
	}
	return size, nil
}

// checkLineURL holds a code's line link to https without credentials.
func checkLineURL(raw string) error {
	if err := CheckHTTPS(strings.NewReplacer("{start}", "1", "{end}", "1").Replace(raw), "line_url"); err != nil {
		return errors.New("line_url: an https link")
	}
	return nil
}

// CheckLines holds the lines a diff's context answered with (ActionResult.lines) to DiffLinesMost and CodeMost.
func CheckLines(l *FileLines) error {
	if l == nil {
		return nil
	}
	if l.Path == "" || l.Start < 1 {
		return errors.New("lines: a path and a start from 1")
	}
	if l.Side != "" && l.Side != "old" && l.Side != "new" {
		return errors.New("lines: side is old or new")
	}
	if len(l.Lines) > DiffLinesMost {
		return fmt.Errorf("lines: at most %d", DiffLinesMost)
	}
	size := 0
	for _, line := range l.Lines {
		size += len(line)
	}
	if size > CodeMost {
		return fmt.Errorf("lines: at most %d bytes", CodeMost)
	}
	return nil
}

func checkComments(comments []Comment) error {
	for k, c := range comments {
		if err := checkMarkdown(c.Body); err != nil {
			return fmt.Errorf("comment %d: %v", k, err)
		}
	}
	return nil
}

// where turns the schema's "$.sections[1].items[2].tone: …" into "section 1 item 2 (metric): tone: …".
func where(v *View, msg string) string {
	var i, j int
	if k, err := fmt.Sscanf(msg, "$.sections[%d].items[%d]", &i, &j); err == nil && k == 2 && i < len(v.Sections) && j < len(v.Sections[i].Items) {
		rest := msg[len(fmt.Sprintf("$.sections[%d].items[%d]", i, j)):]
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "."), ": ")
		return fmt.Sprintf("section %d item %d (%s): %s", i, j, v.Sections[i].Items[j].Type, rest)
	}
	return strings.TrimPrefix(strings.TrimPrefix(msg, "$."), "$: ")
}

func checkChart(e Component) error {
	if !slices.Contains(ChartKinds, e.Kind) {
		return errors.New("chart kind is bar, line, area, candlestick, donut or pie")
	}
	most := PointsMost
	if e.Kind == ChartKindCandlestick {
		most = CandlesMost
	}
	if len(e.Series) == 0 || len(e.Series) > SeriesMost {
		return fmt.Errorf("a chart has 1 to %d series", SeriesMost)
	}
	first := e.Series[0].Points
	if len(first) == 0 || len(first) > most {
		return fmt.Errorf("a series has 1 to %d points", most)
	}
	for k, s := range e.Series {
		if utf8.RuneCountInString(s.Name) > 40 || !slices.Contains(tones, s.Tone) {
			return fmt.Errorf("series %d: a name of at most 40 characters; tone is neutral, positive, caution or danger", k)
		}
		if len(e.Series) > 1 && s.Name == "" {
			return fmt.Errorf("series %d: series of a chart with more than one are named", k)
		}
		if len(s.Points) != len(first) {
			return fmt.Errorf("series %d: every series has the same labels", k)
		}
		for n, pt := range s.Points {
			if pt.Label != first[n].Label {
				return fmt.Errorf("series %d point %d: every series has the same labels, in the same order", k, n)
			}
			if utf8.RuneCountInString(pt.Label) > 40 || utf8.RuneCountInString(pt.Display) > 40 {
				return fmt.Errorf("series %d point %d: label and display have at most 40 characters", k, n)
			}
			if math.IsNaN(pt.Value) || math.IsInf(pt.Value, 0) {
				return fmt.Errorf("series %d point %d: a number", k, n)
			}
		}
	}
	return checkChartV6(e)
}

func checkStats(stats []Stat) error {
	if len(stats) == 0 || len(stats) > StatsMost {
		return fmt.Errorf("a stat_grid has 1 to %d stats", StatsMost)
	}
	for k, st := range stats {
		if st.Label == "" || st.Value == "" {
			return fmt.Errorf("stat %d: label and value", k)
		}
		if utf8.RuneCountInString(st.Label)+utf8.RuneCountInString(st.Value)+utf8.RuneCountInString(st.Detail) > StringMost {
			return fmt.Errorf("stat %d: too long", k)
		}
		if !slices.Contains(tones, st.Tone) {
			return fmt.Errorf("stat %d: tone is neutral, positive, caution or danger", k)
		}
		if err := checkSpark(st.Spark); err != nil {
			return fmt.Errorf("stat %d: %v", k, err)
		}
	}
	return nil
}

// mdImage includes inline, reference and shortcut images.
var mdImage = regexp.MustCompile(`!\[[^\]]*\]`)

var (
	mdHTMLType1Open  = regexp.MustCompile(`(?i)^ {0,3}<(?:pre|script|style|textarea)(?:[ \t>]|$)`)
	mdHTMLType1Close = regexp.MustCompile(`(?i)</(?:pre|script|style|textarea)>`)
	mdHTMLType4Open  = regexp.MustCompile(`^ {0,3}<![A-Za-z]`)
	mdHTMLType6Open  = regexp.MustCompile(`(?i)^ {0,3}</?(?:address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h[1-6]|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|nav|noframes|ol|optgroup|option|p|param|search|section|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul)(?:[ \t>]|/>|$)`)
)

type mdContainer struct {
	kind          byte // '>' for blockquote, 'L' for list item
	marker        byte // list marker style ('-', '*', '+', '.', ')') when kind == 'L'
	contentIndent int  // columns required to continue list item
	empty         bool // CommonMark §5.2: an empty list item ends on the first blank line
}

type mdChunk struct {
	lines []string
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isASCIIPunct(b byte) bool {
	return (b >= '!' && b <= '/') || (b >= ':' && b <= '@') || (b >= '[' && b <= '`') || (b >= '{' && b <= '~')
}

func isBlankLine(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

// consumeIndent consumes up to n columns of leading spaces/tabs from line starting at (pos, col),
// returning the new byte offset, new column, remaining partially-consumed tab spaces, and whether
// at least n columns of whitespace were available.
func consumeIndent(line string, pos, col, virtSpaces, need int) (int, int, int, bool) {
	rem := need
	if virtSpaces > 0 {
		if virtSpaces >= rem {
			return pos, col + rem, virtSpaces - rem, true
		}
		rem -= virtSpaces
		col += virtSpaces
		virtSpaces = 0
	}
	for rem > 0 && pos < len(line) {
		switch line[pos] {
		case ' ':
			pos++
			col++
			rem--
		case '\t':
			tabW := 4 - (col % 4)
			pos++
			if tabW >= rem {
				return pos, col + rem, tabW - rem, true
			}
			col += tabW
			rem -= tabW
		default:
			return pos, col, 0, false
		}
	}
	return pos, col, 0, rem == 0
}

func peekIndentCols(line string, pos, col, virtSpaces int) (cols int, nextPos int) {
	cols = virtSpaces
	c := col + virtSpaces
	i := pos
	for i < len(line) {
		switch line[i] {
		case ' ':
			cols++
			c++
			i++
		case '\t':
			w := 4 - (c % 4)
			cols += w
			c += w
			i++
		default:
			return cols, i
		}
	}
	return cols, i
}

func remainderLine(line string, pos, col, virtSpaces int) string {
	cols, idx := peekIndentCols(line, pos, col, virtSpaces)
	if cols == 0 {
		return line[idx:]
	}
	return strings.Repeat(" ", cols) + line[idx:]
}

func isSetextUnderline(s string) bool {
	spaces := 0
	for spaces < len(s) && s[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 || spaces >= len(s) {
		return false
	}
	ch := s[spaces]
	if ch != '=' && ch != '-' {
		return false
	}
	i := spaces
	for i < len(s) && s[i] == ch {
		i++
	}
	return isBlankLine(s[i:])
}

func isThematicBreak(s string) bool {
	spaces := 0
	for spaces < len(s) && s[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 || spaces >= len(s) {
		return false
	}
	ch := s[spaces]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	count := 0
	for i := spaces; i < len(s); i++ {
		if s[i] == ch {
			count++
		} else if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return count >= 3
}

func isATXHeading(s string) bool {
	spaces := 0
	for spaces < len(s) && s[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 || spaces >= len(s) || s[spaces] != '#' {
		return false
	}
	n := 0
	for spaces+n < len(s) && s[spaces+n] == '#' {
		n++
	}
	if n < 1 || n > 6 {
		return false
	}
	return spaces+n == len(s) || s[spaces+n] == ' ' || s[spaces+n] == '\t'
}

func markdownCodeFenceOpen(line string) (byte, int, bool) {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 || spaces >= len(line) {
		return 0, 0, false
	}
	ch := line[spaces]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	width := 0
	for spaces+width < len(line) && line[spaces+width] == ch {
		width++
	}
	if width < 3 {
		return 0, 0, false
	}
	if ch == '`' && strings.ContainsRune(line[spaces+width:], '`') {
		return 0, 0, false
	}
	return ch, width, true
}

// markdownCodeFenceClose matches a CommonMark §4.5 closing code fence relative to its container:
// at most 3 leading spaces, at least openWidth repetitions of openChar, and only optional spaces/tabs after.
func markdownCodeFenceClose(line string, openChar byte, openWidth int) bool {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 || spaces >= len(line) {
		return false
	}
	width := 0
	for spaces+width < len(line) && line[spaces+width] == openChar {
		width++
	}
	if width < openWidth {
		return false
	}
	return isBlankLine(line[spaces+width:])
}

// matchHTMLOpenTag parses a CommonMark §6.6 HTML open tag starting at s[start] == '<',
// supporting quoted attribute values containing '<', '>', newlines, and backticks in strict linear time.
func matchHTMLOpenTag(s string, start int, noDQuote, noSQuote *int) (int, bool) {
	n := len(s)
	if start+2 >= n || s[start] != '<' || !isASCIIAlpha(s[start+1]) {
		return 0, false
	}
	i := start + 2
	for i < n && (isASCIIAlpha(s[i]) || isASCIIDigit(s[i]) || s[i] == '-') {
		i++
	}
	for {
		wsStart := i
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
			i++
		}
		if i >= n {
			return 0, false
		}
		if s[i] == '>' {
			return i + 1, true
		}
		if s[i] == '/' {
			if i+1 < n && s[i+1] == '>' {
				return i + 2, true
			}
			return 0, false
		}
		if i == wsStart {
			return 0, false
		}
		// Attribute name: [A-Za-z_:][A-Za-z0-9_.:-]*
		if !(isASCIIAlpha(s[i]) || s[i] == '_' || s[i] == ':') {
			return 0, false
		}
		i++
		for i < n && (isASCIIAlpha(s[i]) || isASCIIDigit(s[i]) || s[i] == '_' || s[i] == '.' || s[i] == ':' || s[i] == '-') {
			i++
		}
		// Optional attribute value specification.
		save := i
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
			i++
		}
		if i >= n || s[i] != '=' {
			i = save
			continue
		}
		i++
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
			i++
		}
		if i >= n {
			return 0, false
		}
		switch s[i] {
		case '"':
			if noDQuote != nil && *noDQuote >= i {
				return 0, false
			}
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				if noDQuote != nil {
					*noDQuote = n
				}
				return 0, false
			}
			i += 1 + end + 1
		case '\'':
			if noSQuote != nil && *noSQuote >= i {
				return 0, false
			}
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				if noSQuote != nil {
					*noSQuote = n
				}
				return 0, false
			}
			i += 1 + end + 1
		default:
			valStart := i
			for i < n {
				c := s[i]
				if c == ' ' || c == '\t' || c == '\n' || c == '"' || c == '\'' || c == '=' || c == '<' || c == '>' || c == '`' {
					break
				}
				i++
			}
			if i == valStart {
				return 0, false
			}
		}
	}
}

func matchHTMLCloseTag(s string, start int) (int, bool) {
	n := len(s)
	if start+3 >= n || s[start] != '<' || s[start+1] != '/' || !isASCIIAlpha(s[start+2]) {
		return 0, false
	}
	i := start + 3
	for i < n && (isASCIIAlpha(s[i]) || isASCIIDigit(s[i]) || s[i] == '-') {
		i++
	}
	for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	if i < n && s[i] == '>' {
		return i + 1, true
	}
	return 0, false
}

func startHTMLBlock(line string, canStartType7 bool) int {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 || spaces >= len(line) || line[spaces] != '<' {
		return 0
	}
	trimmed := line[spaces:]
	switch {
	case mdHTMLType1Open.MatchString(line):
		return 1
	case strings.HasPrefix(trimmed, "<!--"):
		return 2
	case strings.HasPrefix(trimmed, "<?"):
		return 3
	case strings.HasPrefix(trimmed, "<![CDATA["):
		return 5
	case mdHTMLType4Open.MatchString(line):
		return 4
	case mdHTMLType6Open.MatchString(line):
		return 6
	case canStartType7:
		if end, ok := matchHTMLOpenTag(trimmed, 0, nil, nil); ok && isBlankLine(trimmed[end:]) {
			return 7
		}
		if end, ok := matchHTMLCloseTag(trimmed, 0); ok && isBlankLine(trimmed[end:]) {
			return 7
		}
		return 0
	default:
		return 0
	}
}

func htmlBlockEnds(kind int, line string) bool {
	switch kind {
	case 1:
		return mdHTMLType1Close.MatchString(line)
	case 2:
		return strings.Contains(line, "-->")
	case 3:
		return strings.Contains(line, "?>")
	case 4:
		return strings.Contains(line, ">")
	case 5:
		return strings.Contains(line, "]]>")
	default:
		return false
	}
}

// tryListMarker checks for a CommonMark §5.2 list item marker at (pos, col, virtSpaces).
// When inParagraph is true, CommonMark forbids empty items and ordered items not starting at 1.
func tryListMarker(line string, pos, col, virtSpaces int, inParagraph bool) (newPos, newCol, newVirt, contentIndent int, marker byte, ok bool) {
	leadCols, idx := peekIndentCols(line, pos, col, virtSpaces)
	if leadCols > 3 || idx >= len(line) {
		return 0, 0, 0, 0, 0, false
	}
	// Thematic break takes precedence over bullet list markers.
	if isThematicBreak(remainderLine(line, pos, col, virtSpaces)) {
		return 0, 0, 0, 0, 0, false
	}
	markerStartCol := col + leadCols
	afterMarkerPos := idx
	ordered := false
	isOne := false
	switch line[idx] {
	case '-', '*', '+':
		marker = line[idx]
		afterMarkerPos = idx + 1
	default:
		if !isASCIIDigit(line[idx]) {
			return 0, 0, 0, 0, 0, false
		}
		ordered = true
		dStart := idx
		for afterMarkerPos < len(line) && afterMarkerPos-dStart < 9 && isASCIIDigit(line[afterMarkerPos]) {
			afterMarkerPos++
		}
		if afterMarkerPos >= len(line) || (line[afterMarkerPos] != '.' && line[afterMarkerPos] != ')') {
			return 0, 0, 0, 0, 0, false
		}
		marker = line[afterMarkerPos]
		isOne = line[dStart:afterMarkerPos] == "1"
		afterMarkerPos++
	}
	markerWidth := afterMarkerPos - idx
	afterMarkerCol := markerStartCol + markerWidth
	if afterMarkerPos < len(line) && line[afterMarkerPos] != ' ' && line[afterMarkerPos] != '\t' {
		return 0, 0, 0, 0, 0, false
	}
	spaceCols, contentIdx := peekIndentCols(line, afterMarkerPos, afterMarkerCol, 0)
	emptyItem := contentIdx >= len(line)
	if inParagraph && (emptyItem || (ordered && !isOne)) {
		return 0, 0, 0, 0, 0, false
	}
	pad := spaceCols
	if emptyItem || spaceCols > 4 {
		pad = 1
	}
	contentIndent = leadCols + markerWidth + pad
	if emptyItem {
		return len(line), afterMarkerCol + spaceCols, 0, contentIndent, marker, true
	}
	p, c, v, _ := consumeIndent(line, afterMarkerPos, afterMarkerCol, 0, pad)
	return p, c, v, contentIndent, marker, true
}

// extractMarkdownChunks walks the CommonMark block structure (blockquotes, list items, fenced code
// blocks, HTML blocks, headings, thematic breaks) in O(N) time, stripping real fenced code blocks
// and returning the remaining non-fenced paragraph/block chunks with container prefixes removed.
func extractMarkdownChunks(text string) []mdChunk {
	norm := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(norm, "\n")

	const (
		leafNone = iota
		leafParagraph
		leafFence
		leafHTML
	)
	var containers []mdContainer
	leafKind := leafNone
	var fenceChar byte
	fenceWidth := 0
	fenceDepth := 0
	htmlKind := 0
	htmlDepth := 0

	var chunks []mdChunk
	var cur []string
	flushChunk := func() {
		if len(cur) > 0 {
			chunks = append(chunks, mdChunk{lines: cur})
			cur = nil
		}
	}

	for _, line := range lines {
		trimmedEnd := len(line)
		for trimmedEnd > 0 && (line[trimmedEnd-1] == ' ' || line[trimmedEnd-1] == '\t') {
			trimmedEnd--
		}
		pos, col, virt := 0, 0, 0
		matched := 0
		for matched < len(containers) {
			c := containers[matched]
			if c.kind == '>' {
				lead, idx := peekIndentCols(line, pos, col, virt)
				if lead > 3 || idx >= len(line) || line[idx] != '>' {
					break
				}
				col = col + lead + 1
				pos = idx + 1
				virt = 0
				if pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
					pos, col, virt, _ = consumeIndent(line, pos, col, 0, 1)
				}
				matched++
			} else {
				if pos >= trimmedEnd {
					matched++
					continue
				}
				np, nc, nv, ok := consumeIndent(line, pos, col, virt, c.contentIndent)
				if !ok {
					break
				}
				pos, col, virt = np, nc, nv
				matched++
			}
		}

		// 1) Active fenced code block.
		if leafKind == leafFence {
			if matched < fenceDepth {
				containers = containers[:matched]
				leafKind = leafNone
				flushChunk()
			} else {
				if pos >= trimmedEnd {
					continue
				}
				rem := remainderLine(line, pos, col, virt)
				if markdownCodeFenceClose(rem, fenceChar, fenceWidth) {
					containers = containers[:fenceDepth]
					leafKind = leafNone
					continue
				}
				continue
			}
		}

		// 2) Active HTML block.
		if leafKind == leafHTML {
			if matched < htmlDepth {
				containers = containers[:matched]
				leafKind = leafNone
				flushChunk()
			} else {
				if pos >= trimmedEnd {
					flushChunk()
					if htmlKind == 6 || htmlKind == 7 {
						containers = containers[:htmlDepth]
						leafKind = leafNone
					}
					continue
				}
				rem := remainderLine(line, pos, col, virt)
				cur = append(cur, rem)
				if htmlBlockEnds(htmlKind, rem) {
					containers = containers[:htmlDepth]
					leafKind = leafNone
					flushChunk()
				}
				continue
			}
		}

		// Check if line is blank after matching existing containers.
		if pos >= trimmedEnd {
			containers = containers[:matched]
			// CommonMark §5.2: a list item that began empty cannot span a blank line.
			for len(containers) > 0 && containers[len(containers)-1].kind == 'L' && containers[len(containers)-1].empty {
				containers = containers[:len(containers)-1]
			}
			leafKind = leafNone
			flushChunk()
			continue
		}

		// Close unmatched containers unless this is a lazy paragraph continuation.
		if matched < len(containers) {
			if leafKind != leafParagraph {
				containers = containers[:matched]
				flushChunk()
			} else {
				// Check if remainder interrupts the paragraph (new container, sibling list item, fence, heading, HR, HTML 1-6).
				rem := remainderLine(line, pos, col, virt)
				lead, idx := peekIndentCols(line, pos, col, virt)
				_, _, isFence := markdownCodeFenceOpen(rem)
				_, _, _, _, _, isList := tryListMarker(line, pos, col, virt, true)
				siblingList := false
				if containers[matched].kind == 'L' {
					if _, _, _, _, mk, ok := tryListMarker(line, pos, col, virt, false); ok && mk == containers[matched].marker {
						siblingList = true
					}
				}
				interrupts := (lead <= 3 && idx < len(line) && line[idx] == '>') ||
					isList || siblingList || isFence || isATXHeading(rem) || isThematicBreak(rem) || startHTMLBlock(rem, false) != 0
				if interrupts {
					containers = containers[:matched]
					leafKind = leafNone
					flushChunk()
				} else {
					// Lazy paragraph continuation stays in the same paragraph chunk.
					cur = append(cur, rem)
					continue
				}
			}
		}

		// Setext heading underline (=== or ---) terminates an open paragraph without starting a new paragraph
		// or bullet list item (CommonMark §4.3), allowing a Type 7 HTML block on the following line.
		if leafKind == leafParagraph && matched == len(containers) {
			rem := remainderLine(line, pos, col, virt)
			if isSetextUnderline(rem) {
				flushChunk()
				leafKind = leafNone
				continue
			}
		}

		// 3) Open any new containers (blockquotes / list items) on this line.
		openedContainer := false
		for {
			lead, idx := peekIndentCols(line, pos, col, virt)
			if lead <= 3 && idx < len(line) && line[idx] == '>' {
				if !openedContainer {
					flushChunk()
				}
				openedContainer = true
				leafKind = leafNone
				for cIdx := len(containers) - 1; cIdx >= 0 && containers[cIdx].empty; cIdx-- {
					containers[cIdx].empty = false
				}
				containers = append(containers, mdContainer{kind: '>'})
				col = col + lead + 1
				pos = idx + 1
				virt = 0
				if pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
					pos, col, virt, _ = consumeIndent(line, pos, col, 0, 1)
				}
				continue
			}
			if np, nc, nv, ci, mk, ok := tryListMarker(line, pos, col, virt, leafKind == leafParagraph); ok {
				if !openedContainer {
					flushChunk()
				}
				openedContainer = true
				leafKind = leafNone
				for cIdx := len(containers) - 1; cIdx >= 0 && containers[cIdx].empty; cIdx-- {
					containers[cIdx].empty = false
				}
				containers = append(containers, mdContainer{
					kind:          'L',
					marker:        mk,
					contentIndent: ci,
					empty:         np >= trimmedEnd,
				})
				pos, col, virt = np, nc, nv
				continue
			}
			break
		}

		if pos >= trimmedEnd {
			leafKind = leafNone
			flushChunk()
			continue
		}
		rem := remainderLine(line, pos, col, virt)
		for idx := len(containers) - 1; idx >= 0 && containers[idx].empty; idx-- {
			containers[idx].empty = false
		}
		if isThematicBreak(rem) {
			leafKind = leafNone
			flushChunk()
			continue
		}
		if isATXHeading(rem) {
			flushChunk()
			chunks = append(chunks, mdChunk{lines: []string{rem}})
			leafKind = leafNone
			continue
		}
		if ch, width, ok := markdownCodeFenceOpen(rem); ok {
			flushChunk()
			leafKind = leafFence
			fenceChar = ch
			fenceWidth = width
			fenceDepth = len(containers)
			continue
		}
		if ht := startHTMLBlock(rem, leafKind != leafParagraph); ht != 0 {
			flushChunk()
			cur = append(cur, rem)
			if htmlBlockEnds(ht, rem) {
				leafKind = leafNone
				flushChunk()
			} else {
				leafKind = leafHTML
				htmlKind = ht
				htmlDepth = len(containers)
			}
			continue
		}
		// Indented code block (4+ columns when not continuing a paragraph): keep fail-closed in its own
		// chunk without marking leafParagraph so a Type 7 HTML block on a following line is still recognized.
		if lead, _ := peekIndentCols(rem, 0, 0, 0); lead >= 4 && leafKind != leafParagraph {
			flushChunk()
			chunks = append(chunks, mdChunk{lines: []string{rem}})
			leafKind = leafNone
			continue
		}

		leafKind = leafParagraph
		cur = append(cur, rem)
	}
	flushChunk()
	return chunks
}

// parseLinkDest parses a CommonMark §6.3 link destination starting at s[start], returning the raw
// destination slice (without enclosing <...>), the index immediately after the destination, and ok.
func parseLinkDest(s string, start int) (dest string, end int, ok bool) {
	n := len(s)
	if start >= n {
		return "", start, false
	}
	if s[start] == '<' {
		i := start + 1
		for i < n {
			c := s[i]
			if c == '\\' && i+1 < n {
				if s[i+1] == '\n' {
					return "", start, false
				}
				i += 2
				continue
			}
			if c == '<' || c == '\n' {
				return "", start, false
			}
			if c == '>' {
				return s[start+1 : i], i + 1, true
			}
			i++
		}
		return "", start, false
	}
	i := start
	parens := 0
	for i < n {
		c := s[i]
		if c == '\\' && i+1 < n && isASCIIPunct(s[i+1]) {
			i += 2
			continue
		}
		if c <= 0x20 || c == 0x7f {
			break
		}
		if c == '(' {
			parens++
			i++
			continue
		}
		if c == ')' {
			if parens == 0 {
				break
			}
			parens--
			i++
			continue
		}
		i++
	}
	if i == start || parens != 0 {
		return "", start, false
	}
	return s[start:i], i, true
}

// parseLinkTitle parses a CommonMark §6.3 link title ("...", '...', or (...)) starting at s[start].
func parseLinkTitle(s string, start int) (int, bool) {
	n := len(s)
	if start >= n {
		return start, false
	}
	open := s[start]
	var closeCh byte
	switch open {
	case '"':
		closeCh = '"'
	case '\'':
		closeCh = '\''
	case '(':
		closeCh = ')'
	default:
		return start, false
	}
	for i := start + 1; i < n; {
		c := s[i]
		if c == '\\' && i+1 < n && isASCIIPunct(s[i+1]) {
			i += 2
			continue
		}
		if open == '(' && c == '(' {
			return start, false
		}
		if c == closeCh {
			return i + 1, true
		}
		i++
	}
	return start, false
}

// hasMarkdownReferenceDef detects CommonMark §4.7 link reference definitions within a non-fenced
// chunk BEFORE inline code stripping so backticks inside labels/titles cannot hide `]:`.
// Requiring a valid §4.7 destination (+ optional title + EOL) avoids false positives on prose like
// `- [Fix]: Handle edge cases` or `> [Note]: See details below`.
func hasMarkdownReferenceDef(lines []string) bool {
	joined := strings.Join(lines, "\n")
	lineStarts := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		lineStarts[i] = off
		off += len(l) + 1
	}
	n := len(joined)
	for idx := range lines {
		i := lineStarts[idx]
		spaces := 0
		for i < n && joined[i] == ' ' && spaces < 3 {
			i++
			spaces++
		}
		if i >= n || joined[i] != '[' {
			continue
		}
		// Scan CommonMark §4.7 link label (non-empty non-whitespace, backslash escapes).
		// Do not impose a byte cap so multi-byte UTF-8 labels <= 999 runes are still caught.
		i++
		hasNonSpace := false
		colonPos := -1
		for i < n {
			c := joined[i]
			if c == '\\' && i+1 < n && joined[i+1] != '\n' {
				hasNonSpace = true
				i += 2
				continue
			}
			if c == '[' {
				break
			}
			if c == ']' {
				if hasNonSpace && i+1 < n && joined[i+1] == ':' {
					colonPos = i + 1
				}
				break
			}
			if c != ' ' && c != '\t' && c != '\n' {
				hasNonSpace = true
			}
			i++
		}
		if colonPos < 0 {
			continue
		}
		// Optional spaces/tabs and at most one newline before destination.
		j := colonPos + 1
		for j < n && (joined[j] == ' ' || joined[j] == '\t') {
			j++
		}
		if j < n && joined[j] == '\n' {
			j++
			for j < n && (joined[j] == ' ' || joined[j] == '\t') {
				j++
			}
		}
		dest, afterDest, ok := parseLinkDest(joined, j)
		if !ok || (dest == "" && (j >= n || joined[j] != '<')) {
			continue
		}
		k := afterDest
		wsAfterDest := 0
		for k < n && (joined[k] == ' ' || joined[k] == '\t') {
			k++
			wsAfterDest++
		}
		// Destination followed directly by EOL/EOF is a valid link reference definition.
		if k >= n || joined[k] == '\n' {
			return true
		}
		// Same-line title requires separating whitespace, a valid title delimiter, and only spaces/tabs to EOL.
		if wsAfterDest > 0 {
			if afterTitle, ok := parseLinkTitle(joined, k); ok {
				t := afterTitle
				for t < n && (joined[t] == ' ' || joined[t] == '\t') {
					t++
				}
				if t >= n || joined[t] == '\n' {
					return true
				}
			}
		}
	}
	return false
}

func unescapeMarkdownPunct(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]) {
			out.WriteByte(s[i+1])
			i += 2
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func isSafeMarkdownHTTPS(raw string) bool {
	if raw == "" {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] <= 0x20 || raw[i] == 0x7f {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func checkMarkdownLinkDest(dest string) error {
	if !isSafeMarkdownHTTPS(dest) {
		return fmt.Errorf("markdown links are https: %q", dest)
	}
	unesc := unescapeMarkdownPunct(dest)
	if !isSafeMarkdownHTTPS(unesc) {
		return fmt.Errorf("markdown links are https: %q", dest)
	}
	if !isSafeMarkdownHTTPS(html.UnescapeString(dest)) || !isSafeMarkdownHTTPS(html.UnescapeString(unesc)) {
		return fmt.Errorf("markdown links are https: %q", dest)
	}
	return nil
}

// matchAutolink matches a CommonMark §6.5 URI or email autolink starting at s[start] == '<'.
func matchAutolink(s string, start int) (end int, inner string, isURI bool, ok bool) {
	n := len(s)
	if start+3 >= n || s[start] != '<' {
		return 0, "", false, false
	}
	// Scan to '>' without spaces, control chars, or '<'.
	closeIdx := -1
	for i := start + 1; i < n; i++ {
		c := s[i]
		if c == '>' {
			closeIdx = i
			break
		}
		if c <= 0x20 || c == 0x7f || c == '<' {
			return 0, "", false, false
		}
	}
	if closeIdx < 0 {
		return 0, "", false, false
	}
	inner = s[start+1 : closeIdx]
	// URI autolink: scheme [A-Za-z][A-Za-z0-9+.-]{1,31} ':'
	if colon := strings.IndexByte(inner, ':'); colon >= 2 && colon <= 32 && isASCIIAlpha(inner[0]) {
		validScheme := true
		for i := 1; i < colon; i++ {
			c := inner[i]
			if !(isASCIIAlpha(c) || isASCIIDigit(c) || c == '+' || c == '.' || c == '-') {
				validScheme = false
				break
			}
		}
		if validScheme {
			return closeIdx + 1, inner, true, true
		}
	}
	// Email autolink (CommonMark §6.5): local@domain where local permits backticks (`).
	if at := strings.IndexByte(inner, '@'); at > 0 && at+1 < len(inner) {
		local, domain := inner[:at], inner[at+1:]
		valid := true
		for i := 0; i < len(local); i++ {
			c := local[i]
			if !(isASCIIAlpha(c) || isASCIIDigit(c) || strings.IndexByte(".!#$%&'*+/=?^_`{|}~-", c) >= 0) {
				valid = false
				break
			}
		}
		if valid {
			labels := strings.Split(domain, ".")
			for _, lb := range labels {
				if len(lb) == 0 || len(lb) > 63 || lb[0] == '-' || lb[len(lb)-1] == '-' {
					valid = false
					break
				}
				for i := 0; i < len(lb); i++ {
					if !(isASCIIAlpha(lb[i]) || isASCIIDigit(lb[i]) || lb[i] == '-') {
						valid = false
						break
					}
				}
				if !valid {
					break
				}
			}
		}
		if valid {
			return closeIdx + 1, inner, false, true
		}
	}
	return 0, "", false, false
}

// scanInlineAndValidate performs a strict O(N) left-to-right CommonMark §6 inline scan of one
// non-fenced paragraph chunk:
//   - strips inline code spans using pre-indexed exact-length backtick runs (O(N) amortized),
//   - consumes autolinks (validating URI autolinks), inline HTML comments/PIs/declarations/CDATA/tags
//     (with monotonic failure bounds so unclosed delimiters never go quadratic), and inline link
//     `]`(`dest` `"title"`) constructs (validating destinations) at matching left-to-right precedence
//     so backticks inside them can never pair with trailing backticks to hide live links/images.
func scanInlineAndValidate(s string) (string, error) {
	n := len(s)
	// Pre-index all maximal backtick runs once in O(N) so inline code span closing is O(1) amortized.
	runsByLen := make(map[int][]int)
	runCursor := make(map[int]int)
	for i := 0; i < n; {
		if s[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < n && s[i+run] == '`' {
			run++
		}
		runsByLen[run] = append(runsByLen[run], i)
		i += run
	}

	nextCommentClose, nextPIClose, nextCDATAClose, nextDeclClose := -1, -1, -1, -1
	noDQuote, noSQuote := -1, -1
	noBacktickUntil := 0
	findCached := func(needle string, from int, cache *int) int {
		if *cache == n {
			return -1
		}
		if *cache < from {
			idx := strings.Index(s[from:], needle)
			if idx < 0 {
				*cache = n
				return -1
			}
			*cache = from + idx
		}
		return *cache
	}

	var out strings.Builder
	out.Grow(n)

	for i := 0; i < n; {
		c := s[i]
		if c == '\\' && i+1 < n {
			if i+1 < noBacktickUntil && s[i+1] == '`' {
				out.WriteByte(c)
				out.WriteByte('_')
			} else {
				out.WriteByte(c)
				out.WriteByte(s[i+1])
			}
			i += 2
			continue
		}
		if c == '`' {
			if i < noBacktickUntil {
				out.WriteByte('_')
				i++
				continue
			}
			run := 0
			for i+run < n && s[i+run] == '`' {
				run++
			}
			positions := runsByLen[run]
			cur := runCursor[run]
			for cur < len(positions) && positions[cur] <= i {
				cur++
			}
			runCursor[run] = cur
			if cur < len(positions) {
				out.WriteByte('_')
				i = positions[cur] + run
				continue
			}
			out.WriteString(s[i : i+run])
			i += run
			continue
		}
		if c == '<' {
			if end, inner, isURI, ok := matchAutolink(s, i); ok {
				if isURI && !isSafeMarkdownHTTPS(inner) {
					return "", fmt.Errorf("markdown links are https: %q", inner)
				}
				out.WriteByte('_')
				i = end
				continue
			}
			// Inline HTML comments/PIs/CDATA/declarations/tags: neutralize backticks inside the
			// union of CommonMark 0.29 and 0.31.2 constructs via noBacktickUntil rather than erasing
			// inner text, so both 0.29 parsers (which treat inner text as live markdown) and 0.31.2
			// parsers (which consume inner backticks as HTML) stay fail-closed. Monotonic findCached
			// guarantees strict O(N) time even on repeated open delimiters with a closer at EOF.
			if strings.HasPrefix(s[i:], "<!--") {
				if i+4 < n && s[i+4] != '>' && !strings.HasPrefix(s[i+4:], "->") {
					if closeAt := findCached("-->", i+4, &nextCommentClose); closeAt >= 0 {
						end := closeAt + 3
						if end > noBacktickUntil {
							noBacktickUntil = end
						}
						out.WriteByte('_')
						i += 4
						continue
					}
				}
			} else if strings.HasPrefix(s[i:], "<?") {
				if closeAt := findCached("?>", i+2, &nextPIClose); closeAt >= 0 {
					end := closeAt + 2
					if end > noBacktickUntil {
						noBacktickUntil = end
					}
					out.WriteByte('_')
					i += 2
					continue
				}
			} else if strings.HasPrefix(s[i:], "<![CDATA[") {
				if closeAt := findCached("]]>", i+9, &nextCDATAClose); closeAt >= 0 {
					end := closeAt + 3
					if end > noBacktickUntil {
						noBacktickUntil = end
					}
					out.WriteByte('_')
					i += 9
					continue
				}
			} else if i+2 < n && s[i+1] == '!' && isASCIIAlpha(s[i+2]) {
				// CommonMark 0.29 ∪ 0.31.2 declaration: <! + [A-Za-z]+ + [ \t\n] + [^>]*>
				j := i + 2
				for j < n && isASCIIAlpha(s[j]) {
					j++
				}
				if j < n && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n') {
					if closeAt := findCached(">", j+1, &nextDeclClose); closeAt >= 0 {
						end := closeAt + 1
						if end > noBacktickUntil {
							noBacktickUntil = end
						}
						out.WriteByte('_')
						i = j + 1
						continue
					}
				}
			} else if end, ok := matchHTMLCloseTag(s, i); ok {
				if end > noBacktickUntil {
					noBacktickUntil = end
				}
				out.WriteByte('_')
				i = end
				continue
			} else if end, ok := matchHTMLOpenTag(s, i, &noDQuote, &noSQuote); ok {
				if end > noBacktickUntil {
					noBacktickUntil = end
				}
				out.WriteByte('_')
				i++
				continue
			}
			out.WriteByte('<')
			i++
			continue
		}
		if c == ']' && i+1 < n && s[i+1] == '(' {
			j := i + 2
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < n && s[j] == '\n' {
				j++
				for j < n && (s[j] == ' ' || s[j] == '\t') {
					j++
				}
			}
			if j < n && s[j] == ')' {
				return "", fmt.Errorf("markdown links are https: %q", "")
			}
			dest, afterDest, ok := parseLinkDest(s, j)
			if !ok {
				// Fail closed if `](` looks like a malformed/unsafe angle destination such as `<https://good @evil>`.
				bad := s[j:]
				if nl := strings.IndexByte(bad, '\n'); nl >= 0 {
					bad = bad[:nl]
				}
				if rp := strings.IndexByte(bad, ')'); rp >= 0 {
					bad = bad[:rp]
				}
				return "", fmt.Errorf("markdown links are https: %q", bad)
			}
			if err := checkMarkdownLinkDest(dest); err != nil {
				return "", err
			}
			k := afterDest
			ws := 0
			for k < n && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n') {
				k++
				ws++
			}
			hasTitle := false
			titleEnd := afterDest
			if k < n && ws > 0 && (s[k] == '"' || s[k] == '\'' || s[k] == '(') {
				if afterTitle, ok := parseLinkTitle(s, k); ok {
					hasTitle = true
					titleEnd = afterTitle
					k = afterTitle
					for k < n && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n') {
						k++
					}
				}
			}
			if k < n && s[k] == ')' {
				// Preserve ']' in cleaned output so `![alt](https://...)` still triggers mdImage.
				// Never erase link title text: neutralize backticks inside the title via noBacktickUntil
				// and advance only past the validated destination so any live markdown inside a fake or
				// unmatched `](dest "title")` is still inspected by this loop.
				out.WriteString("]_")
				if hasTitle {
					if titleEnd > noBacktickUntil {
						noBacktickUntil = titleEnd
					}
					i = afterDest
				} else {
					i = k + 1
				}
				continue
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String(), nil
}

func trimGFMAutolinkTrail(tok string) string {
	opens := strings.Count(tok, "(")
	closes := strings.Count(tok, ")")
	for len(tok) > 0 {
		last := tok[len(tok)-1]
		if strings.IndexByte("?!.,:*_~'\"", last) >= 0 {
			tok = tok[:len(tok)-1]
			continue
		}
		if last == ')' && closes > opens {
			tok = tok[:len(tok)-1]
			closes--
			continue
		}
		break
	}
	return tok
}

// checkGFMBareAutolinks validates GFM §6.9 bare autolinks (http://, https://, www.) in cleaned
// non-code markdown text so non-https schemes and userinfo phishing URLs cannot slip through.
func checkGFMBareAutolinks(s string) error {
	n := len(s)
	for i := 0; i < n; i++ {
		if i > 0 {
			prev := s[i-1]
			if isASCIIAlpha(prev) || isASCIIDigit(prev) {
				continue
			}
		}
		rem := s[i:]
		var isHTTP, isWWW bool
		if len(rem) >= 8 && strings.EqualFold(rem[:8], "https://") {
			isHTTP = true
		} else if len(rem) >= 7 && strings.EqualFold(rem[:7], "http://") {
			isHTTP = true
		} else if len(rem) >= 4 && strings.EqualFold(rem[:4], "www.") {
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '\n' || strings.IndexByte("*_~(|", s[i-1]) >= 0 {
				isWWW = true
			}
		}
		if !isHTTP && !isWWW {
			continue
		}
		j := i
		for j < n && s[j] > 0x20 && s[j] != 0x7f && s[j] != '<' && s[j] != '>' && s[j] != '|' {
			j++
		}
		tok := trimGFMAutolinkTrail(s[i:j])
		if isWWW {
			if len(tok) > 4 {
				rest := tok[4:]
				if strings.ContainsRune(rest, '.') && rest[0] != '.' && rest[len(rest)-1] != '.' {
					return fmt.Errorf("markdown links are https: %q", tok)
				}
			}
		} else {
			if !isSafeMarkdownHTTPS(tok) {
				return fmt.Errorf("markdown links are https: %q", tok)
			}
		}
		i = j - 1
	}
	return nil
}

// checkMarkdown holds a markdown to its subset where it matters: links are https (the apps open nothing else), no
// images. HTML is drawn as the text it is.
func checkMarkdown(text string) error {
	chunks := extractMarkdownChunks(text)
	for _, ch := range chunks {
		if hasMarkdownReferenceDef(ch.lines) {
			return errors.New("markdown reference links are unsupported; use inline https links")
		}
		cleaned, err := scanInlineAndValidate(strings.Join(ch.lines, "\n"))
		if err != nil {
			return err
		}
		if mdImage.MatchString(cleaned) {
			return errors.New("markdown has no images")
		}
		if err := checkGFMBareAutolinks(cleaned); err != nil {
			return err
		}
	}
	return nil
}

// Actions are a view's actions (its components', its search's, filter's, and its sections' more and link) and where
// each is.
func Actions(v *View) []*Action {
	var out []*Action
	if v == nil {
		return nil
	}
	if v.Search != nil {
		out = append(out, &v.Search.Action)
	}
	if v.Filter != nil {
		out = append(out, &v.Filter.Action)
	}
	if v.Tabs != nil {
		out = append(out, &v.Tabs.Action)
	}
	if r := v.Review; r != nil {
		for k := range r.Buttons {
			out = append(out, &r.Buttons[k].Action)
		}
		if r.Discard != nil {
			out = append(out, r.Discard)
		}
	}
	for i := range v.Sections {
		s := &v.Sections[i]
		for j := range s.Items {
			e := &s.Items[j]
			if e.Action != nil {
				out = append(out, e.Action)
			}
			if e.Button != nil {
				out = append(out, &e.Button.Action)
			}
			for _, a := range []*Action{e.Expand, e.Load, e.Comment, e.Reply, e.Context, e.MarkViewed, e.Draft, e.Navigate} {
				if a != nil {
					out = append(out, a)
				}
			}
			for k := range e.Crumbs {
				if e.Crumbs[k].Action != nil {
					out = append(out, e.Crumbs[k].Action)
				}
			}
			for k := range e.Buttons {
				out = append(out, &e.Buttons[k].Action)
			}
			if c := e.Control; c != nil {
				out = append(out, &c.Action)
			}
			for k := range e.Actions {
				out = append(out, &e.Actions[k].Action)
			}
			for k := range e.Tiles {
				t := &e.Tiles[k]
				if t.Action != nil {
					out = append(out, t.Action)
				}
				if t.Control != nil {
					out = append(out, &t.Control.Action)
				}
				for l := range t.Actions {
					out = append(out, &t.Actions[l].Action)
				}
			}
			for k := range e.Nodes {
				if e.Nodes[k].Action != nil {
					out = append(out, e.Nodes[k].Action)
				}
			}
			for k := range e.Pins {
				if e.Pins[k].Action != nil {
					out = append(out, e.Pins[k].Action)
				}
			}
			for k := range e.Markers {
				if e.Markers[k].Action != nil {
					out = append(out, e.Markers[k].Action)
				}
			}
			for k := range e.Threads {
				if e.Threads[k].Reply != nil {
					out = append(out, e.Threads[k].Reply)
				}
				if e.Threads[k].Resolve != nil {
					out = append(out, e.Threads[k].Resolve)
				}
			}
		}
		if s.More != nil {
			out = append(out, &s.More.Action)
		}
		if s.Link != nil {
			out = append(out, &s.Link.Action)
		}
	}
	return out
}
