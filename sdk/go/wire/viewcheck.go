package wire

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
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
			// a grid's or a carousel's tiles are components of the page each
			if n += max(1, len(e.Tiles)); n > ItemsMost {
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
				if u, perr := url.Parse(e.URL); perr != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
					err = errors.New("link needs an https url")
				}
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

// checkLineURL holds a code's line link to https.
func checkLineURL(raw string) error {
	u, err := url.Parse(strings.NewReplacer("{start}", "1", "{end}", "1").Replace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" {
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

// Reference links are outside the safe inline subset; rejecting definitions also covers multiline destinations.
var mdReference = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:`)

// mdAutolink is a CommonMark URI autolink, not an HTML tag.
var mdAutolink = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9+.-]*:[^<>\s]*)>`)

// mdLink is a Markdown link's or image's target: [text](target "title").
var mdLink = regexp.MustCompile(`\]\(\s*<?([^)\s>]*)`)

// mdCode is Markdown's code, a block or a span: what looks like a link in it is text.
var mdCode = regexp.MustCompile("(?s)```.*?(```|$)|`[^`\n]*`")

// checkMarkdown holds a markdown to its subset where it matters: links are https (the apps open nothing else), no
// images. HTML is drawn as the text it is.
func checkMarkdown(text string) error {
	text = mdCode.ReplaceAllString(text, "")
	if mdImage.MatchString(text) {
		return errors.New("markdown has no images")
	}
	if mdReference.MatchString(text) {
		return errors.New("markdown reference links are unsupported; use inline https links")
	}
	links := append(mdLink.FindAllStringSubmatch(text, -1), mdAutolink.FindAllStringSubmatch(text, -1)...)
	for _, m := range links {
		if u, err := url.Parse(m[1]); err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return fmt.Errorf("markdown links are https: %q", m[1])
		}
	}
	return nil
}

// Actions are a view's actions (its components', its search's, filter's and sections' more) and where each is.
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
			for _, a := range []*Action{e.Expand, e.Load, e.Comment, e.Reply, e.Context, e.MarkViewed, e.Draft} {
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
	}
	return out
}
