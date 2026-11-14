package kb

import (
	"bytes"
	"cmp"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The summary card (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)"): besides the legacy lists the
// model groups the points and decisions by topic, marks each one's key figure or status with **…**, and writes a
// headline and the core conclusions. buildOverview turns that into Minutes.Overview, or leaves it out (and the
// minutes are written exactly as before); normalizeOverview keeps it whole once items leave it (a photo deleted);
// renderOverview is its Markdown.

// MinutesOverview is the minutes as one summary card: optional, present only when every point and decision is a
// bullet of one of its topics.
type MinutesOverview struct {
	Headline    string              `json:"headline,omitempty"`
	Conclusions []MinutesConclusion `json:"conclusions,omitempty"`
	// Groups: one titled "" (no heading), or 2 to 4 with distinct titles; never empty, and no group or topic is.
	Groups []MinutesGroup `json:"groups"`
}

// MinutesConclusion is a core conclusion: where the meeting landed, citing a line, or a photo a bullet cites.
type MinutesConclusion struct {
	Text   string   `json:"text"`
	Strong []string `json:"strong,omitempty"`
	At     float64  `json:"at"`
	Photo  int      `json:"photo,omitempty"`
}

// MinutesGroup is a heading over topics; Title is "" for the single untitled group.
type MinutesGroup struct {
	Title  string         `json:"title"`
	Topics []MinutesTopic `json:"topics"`
}

// MinutesTopic is a topic card: Icon one of minutesTopicIcons.
type MinutesTopic struct {
	Title   string          `json:"title"`
	Icon    string          `json:"icon"`
	Bullets []MinutesBullet `json:"bullets"`
}

// MinutesBullet is a point (Who as the point's) or a decision (Decision) of the minutes, with the same Text, At and
// Photo; Strong the places of Text set in bold (strongSpans).
type MinutesBullet struct {
	Text     string   `json:"text"`
	Strong   []string `json:"strong,omitempty"`
	At       float64  `json:"at"`
	Photo    int      `json:"photo,omitempty"`
	Who      string   `json:"who,omitempty"`
	Decision bool     `json:"decision,omitempty"`
}

const (
	minutesTopics          = 12  // topics of one overview, after same-titled ones are merged; more leaves it out
	minutesConclusions     = 4   // kept in the model's order
	minutesHeadlineRunes   = 120 // at most, as every limit below
	minutesConclusionRunes = 200
	minutesTopicRunes      = 40
	minutesGroupRunes      = 24
	minutesStrongRunes     = 20
	minutesStrongMax       = 2
	// Named groups only with at least this many topics, in 2 to minutesGroupsMax groups.
	minutesGroupedTopics = 4
	minutesGroupsMax     = 4
)

// minutesTopicIcons is what a topic's icon may be; anything else is general (clients do the same, so the list can grow).
var minutesTopicIcons = []string{"progress", "schedule", "release", "performance", "metrics", "tooling", "quality", "design",
	"people", "customer", "budget", "risk", "general"}

// UnmarshalJSON reads Minutes with its overview decoded on its own: one this hub cannot read (another hub's shape, a
// hand edit) or that has no bullet counts as absent, as on the clients, and never makes the rest unreadable.
func (m *Minutes) UnmarshalJSON(data []byte) error {
	type plain Minutes
	var aux struct {
		*plain
		Overview json.RawMessage `json:"overview"` // shallower than plain's: it takes the field
	}
	aux.plain = (*plain)(m)
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Overview = nil
	if len(aux.Overview) > 0 {
		var o MinutesOverview
		if json.Unmarshal(aux.Overview, &o) == nil && o.hasBullet() {
			m.Overview = &o
		}
	}
	return nil
}

func (o *MinutesOverview) hasBullet() bool {
	for _, g := range o.Groups {
		for _, t := range g.Topics {
			if len(t.Bullets) > 0 {
				return true
			}
		}
	}
	return false
}

// modelText is a string the model wrote; anything else (a number, an object) is "" rather than an unusable answer:
// a malformed headline or topic title costs only itself.
type modelText string

func (t *modelText) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) != nil {
		s = ""
	}
	*t = modelText(s)
	return nil
}

// modelList is a list the model wrote of what T decodes: not a list is none, and an entry T cannot decode is left out.
type modelList[T any] []T

func (l *modelList[T]) UnmarshalJSON(data []byte) error {
	*l = nil
	var raw []json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	for _, r := range raw {
		var x T
		if json.Unmarshal(r, &x) == nil {
			*l = append(*l, x)
		}
	}
	return nil
}

// answerTopics is the answer's topics: one that is not a list, or a member that is not an object (null too), makes the
// answer unusable, as a malformed points does. A null or missing topics is none, as for every list of the answer.
type answerTopics []answerTopic

func (l *answerTopics) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*l = nil
	for _, r := range raw {
		r = bytes.TrimSpace(r)
		if len(r) == 0 || r[0] != '{' {
			return &json.UnmarshalTypeError{Value: jsonKind(r), Type: reflect.TypeFor[answerTopic](), Field: "topics"}
		}
		var t answerTopic
		if err := json.Unmarshal(r, &t); err != nil {
			return err
		}
		*l = append(*l, t)
	}
	return nil
}

// jsonKind names the kind of the JSON value v, as encoding/json's type errors do.
func jsonKind(v []byte) string {
	switch {
	case len(v) == 0:
		return "nothing"
	case v[0] == '"':
		return "string"
	case v[0] == '[':
		return "array"
	case v[0] == 'n':
		return "null"
	case v[0] == 't' || v[0] == 'f':
		return "bool"
	}
	return "number"
}

// answerTopic is a topic of the model's answer: title, group and icon lenient, its points and decisions as strict as
// the answer's own (a malformed one makes the answer unusable).
type answerTopic struct {
	Title     modelText     `json:"title"`
	Group     modelText     `json:"group"`
	Icon      modelText     `json:"icon"`
	Points    []answerPoint `json:"points"`
	Decisions []answerItem  `json:"decisions"`
}

// answerConclusion is a conclusion of the model's answer; one that is not an object is left out (modelList).
type answerConclusion struct {
	Text  modelText  `json:"text"`
	Line  modelLine  `json:"line"`
	Photo modelPhoto `json:"photo"`
}

// placed is a point or decision kept by buildMinutes with what the overview needs of it: the answer's topic it came
// in (-1: outside topics) and its bold places.
type placed[T any] struct {
	item   T
	topic  int
	strong []string
}

func placedItems[T any](list []placed[T]) []T {
	var out []T
	for _, p := range list {
		out = append(out, p.item)
	}
	return out
}

// stripMarks is s without the model's ** marks.
func stripMarks(s string) string { return strings.ReplaceAll(s, "**", "") }

// plainLine is a short field the model writes (the minutes' title, the place, a name, a deadline as said) on one line
// and without marks: the answer marks only points, decisions and conclusions, so a ** elsewhere is a slip, and a name
// keeping one would match neither the calendar nor the transcript.
func plainLine(s string) string { return oneLine(stripMarks(s)) }

// cardLine is s without marks on one line, at most n runes ("…" marking a cut and counted): a headline, a title, an
// action item or an open question.
func cardLine(s string, n int) string {
	return clipAtMost(strings.Join(strings.Fields(stripMarks(s)), " "), n)
}

// clipAtMost is s cut to at most n runes, the last of them "…" when it was cut.
func clipAtMost(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return clip(s, n-1)
}

// topicIcon is icon when it is one of minutesTopicIcons (any case), else general.
func topicIcon(icon string) string {
	icon = strings.ToLower(strings.TrimSpace(icon))
	if slices.Contains(minutesTopicIcons, icon) {
		return icon
	}
	return "general"
}

// strongText is the model's text s without its **…** marks, on one line and at most n runes (n at least 1; "…" marking
// a cut and counted, as cardLine), and the places they marked that every client finds where the model put them
// (strongSpans): at most minutesStrongMax, each 1 to minutesStrongRunes runes and never the whole text, the spaces and
// punctuation at its ends moved outside it (strongEdge). A mark the cut reaches, or one the rule would find elsewhere
// (an earlier copy of the same figure), is left out; its words stay. An unclosed ** is dropped.
func strongText(s string, n int) (string, []string) {
	parts := strings.Split(s, "**")
	var runes []rune
	var mark []int // the mark each rune is in, from 1; 0 outside any
	for i, p := range parts {
		in := 0
		if i%2 == 1 && i < len(parts)-1 { // the last part after an odd mark is an unclosed one's
			in = (i + 1) / 2
		}
		for _, r := range p {
			runes, mark = append(runes, r), append(mark, in)
		}
	}
	// One line, as clipRunes: runs of spaces become one, none at the ends. A space is in a mark when both sides are.
	text, in := make([]rune, 0, len(runes)), make([]int, 0, len(runes))
	space := false
	for i, r := range runes {
		if unicode.IsSpace(r) {
			space = len(text) > 0
			continue
		}
		if space {
			m := 0
			if mark[i] != 0 && in[len(in)-1] == mark[i] {
				m = mark[i]
			}
			text, in = append(text, ' '), append(in, m)
			space = false
		}
		text, in = append(text, r), append(in, mark[i])
	}
	if len(text) > n { // cut to n-1 runes and "…"
		cut := map[int]bool{} // marks the cut reaches: not what the model marked any more
		for _, k := range in[n-1:] {
			cut[k] = k != 0
		}
		text, in = append(text[:n-1:n-1], '…'), append(in[:n-1:n-1], 0)
		for i, k := range in {
			if cut[k] {
				in[i] = 0
			}
		}
	}
	plain := string(text)
	whole := strings.TrimFunc(plain, strongEdge)
	at := make([]int, len(text)+1) // the byte each rune starts at in plain, as strongSpans counts
	for i, r := range text {
		at[i+1] = at[i] + utf8.RuneLen(r)
	}
	var strong []string
	cursor := 0
	for from := 0; from < len(in) && len(strong) < minutesStrongMax; {
		if in[from] == 0 {
			from++
			continue
		}
		to := from + 1 // a mark's runes are next to each other
		for to < len(in) && in[to] == in[from] {
			to++
		}
		next := to
		for from < to && strongEdge(text[from]) {
			from++
		}
		for to > from && strongEdge(text[to-1]) {
			to--
		}
		if n := to - from; n >= 1 && n <= minutesStrongRunes {
			entry, pos := plain[at[from]:at[to]], at[from]
			if i := strings.Index(plain[cursor:], entry); entry != whole && i >= 0 && cursor+i == pos {
				strong = append(strong, entry)
				cursor = pos + len(entry)
			}
		}
		from = next
	}
	return plain, strong
}

// strongEdge: r is moved out of the ends of a bold place: a space, or punctuation other than what a figure or a name
// holds (40%, #3, @Ada, -5).
func strongEdge(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	return unicode.IsPunct(r) && !strings.ContainsRune("%‰‱#@&-", r)
}

// strongSpans are the byte ranges of text that strong sets in bold, as every client finds them: from the start, each
// entry's first exact occurrence at or after the end of the one before (no case folding or normalization); an entry
// not found (or empty) is skipped and bolds nothing.
func strongSpans(text string, strong []string) [][2]int {
	var out [][2]int
	at := 0
	for _, s := range strong {
		if s == "" {
			continue
		}
		i := strings.Index(text[at:], s)
		if i < 0 {
			continue
		}
		out = append(out, [2]int{at + i, at + i + len(s)})
		at += i + len(s)
	}
	return out
}

// cardMarkdown is text for the summary card's Markdown: a literal * escaped, the places strong bolds in **.
func cardMarkdown(text string, strong []string) string {
	var b strings.Builder
	at := 0
	for _, sp := range strongSpans(text, strong) {
		b.WriteString(escapeStars(text[at:sp[0]]) + "**" + escapeStars(text[sp[0]:sp[1]]) + "**")
		at = sp[1]
	}
	b.WriteString(escapeStars(text[at:]))
	return b.String()
}

func escapeStars(s string) string { return strings.ReplaceAll(s, "*", `\*`) }

// topicDraft is a topic of an overview being arranged, with the group it names ("" none).
type topicDraft struct {
	topic MinutesTopic
	group string
}

// buildOverview is the overview of answer a given the points and decisions buildMinutes kept of it (in time order,
// each with its topic in a and its bold places), or why there is none: every one of them must be under a topic with a
// title, and there may be at most minutesTopics topics once same-titled ones are merged. Conclusions cite a line read
// (lineAt) or a photo read (photoAt) that a bullet cites; title is the minutes', which the headline must not repeat.
func buildOverview(a minutesAnswer, points []placed[MinutesPoint], decisions []placed[MinutesItem], lineAt, photoAt map[int]float64,
	title string) (*MinutesOverview, string) {
	if len(points)+len(decisions) == 0 {
		return nil, "no point or decision"
	}
	bullets := map[int][]MinutesBullet{} // by the answer's topic
	for _, p := range points {
		if p.topic < 0 {
			return nil, "points or decisions outside topics"
		}
		x := p.item
		bullets[p.topic] = append(bullets[p.topic], MinutesBullet{Text: x.Text, Strong: p.strong, At: x.At, Photo: x.Photo, Who: x.Who})
	}
	for _, d := range decisions {
		if d.topic < 0 {
			return nil, "points or decisions outside topics"
		}
		x := d.item
		bullets[d.topic] = append(bullets[d.topic], MinutesBullet{Text: x.Text, Strong: d.strong, At: x.At, Photo: x.Photo, Decision: true})
	}
	// The topics with a bullet in the answer's order; one titled as an earlier one (any case) joins it, which keeps
	// its title, group and icon.
	var drafts []topicDraft
	for i, t := range a.Topics {
		bs := bullets[i]
		if len(bs) == 0 {
			continue
		}
		name := cardLine(string(t.Title), minutesTopicRunes)
		if name == "" {
			return nil, "a topic with items has no title"
		}
		j := slices.IndexFunc(drafts, func(d topicDraft) bool { return strings.EqualFold(d.topic.Title, name) })
		if j < 0 {
			drafts = append(drafts, topicDraft{topic: MinutesTopic{Title: name, Icon: topicIcon(string(t.Icon))},
				group: cardLine(string(t.Group), minutesGroupRunes)})
			j = len(drafts) - 1
		}
		drafts[j].topic.Bullets = append(drafts[j].topic.Bullets, bs...)
	}
	if len(drafts) > minutesTopics {
		return nil, "more than 12 topics"
	}
	cites := map[int]bool{}
	for i := range drafts {
		slices.SortStableFunc(drafts[i].topic.Bullets, func(x, y MinutesBullet) int { return cmp.Compare(x.At, y.At) })
		for _, b := range drafts[i].topic.Bullets {
			if b.Photo > 0 {
				cites[b.Photo] = true
			}
		}
	}
	o := &MinutesOverview{Groups: arrangeTopics(drafts)}
	if h := cardLine(string(a.Headline), minutesHeadlineRunes); h != "" && !strings.EqualFold(h, strings.TrimSpace(title)) {
		o.Headline = h
	}
	// The conclusions never count as what makes the answer usable (cited), and cite a photo only when a bullet does:
	// the photos the items cite stay all the photos the minutes cite.
	seen := map[string]bool{}
	for _, c := range a.Conclusions {
		if len(o.Conclusions) == minutesConclusions {
			break
		}
		text, strong := strongText(string(c.Text), minutesConclusionRunes)
		if text == "" || seen[text] {
			continue
		}
		x := MinutesConclusion{Text: text, Strong: strong}
		if sec, ok := photoAt[int(c.Photo)]; ok && c.Photo > 0 && cites[int(c.Photo)] {
			x.At, x.Photo = sec, int(c.Photo)
		} else if sec, ok := lineAt[int(c.Line)]; ok {
			x.At = sec
		} else {
			continue
		}
		seen[text] = true
		o.Conclusions = append(o.Conclusions, x)
	}
	return o, ""
}

// arrangeTopics puts drafts (in the answer's order) into groups: their named groups when there are at least
// minutesGroupedTopics topics, every one names a group, and there are 2 to minutesGroupsMax of them (names compared
// ignoring case, the first topic's spelling kept); otherwise one group titled "". Bullets stay as they are; topics
// are ordered by their first bullet within a group, groups by their first topic, ties in drafts' order.
func arrangeTopics(drafts []topicDraft) []MinutesGroup {
	names := []string{} // the groups' titles, by first appearance in drafts
	named := len(drafts) >= minutesGroupedTopics
	for _, d := range drafts {
		if d.group == "" {
			named = false
			break
		}
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, d.group) }) {
			names = append(names, d.group)
		}
	}
	named = named && len(names) >= 2 && len(names) <= minutesGroupsMax
	sorted := slices.Clone(drafts)
	slices.SortStableFunc(sorted, func(x, y topicDraft) int { return cmp.Compare(firstAt(x.topic), firstAt(y.topic)) })
	if !named {
		g := MinutesGroup{Title: ""}
		for _, d := range sorted {
			g.Topics = append(g.Topics, d.topic)
		}
		return []MinutesGroup{g}
	}
	var groups []MinutesGroup
	for _, d := range sorted {
		name := names[slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, d.group) })]
		i := slices.IndexFunc(groups, func(g MinutesGroup) bool { return g.Title == name })
		if i < 0 {
			groups = append(groups, MinutesGroup{Title: name})
			i = len(groups) - 1
		}
		groups[i].Topics = append(groups[i].Topics, d.topic)
	}
	return groups
}

func firstAt(t MinutesTopic) float64 {
	if len(t.Bullets) == 0 {
		return 0
	}
	return t.Bullets[0].At
}

// normalizeOverview is o once bullets or conclusions left it (a photo deleted): a topic without bullets goes, then a
// group without topics, and the group rule is applied again (arrangeTopics); nil when no topic is left.
func normalizeOverview(o *MinutesOverview) *MinutesOverview {
	if o == nil {
		return nil
	}
	var drafts []topicDraft
	for _, g := range o.Groups {
		for _, t := range g.Topics {
			if len(t.Bullets) > 0 {
				drafts = append(drafts, topicDraft{topic: t, group: g.Title})
			}
		}
	}
	if len(drafts) == 0 {
		return nil
	}
	o.Groups = arrangeTopics(drafts)
	if len(o.Conclusions) == 0 {
		o.Conclusions = nil
	}
	return o
}

// dropOverviewPhotos takes the bullets and conclusions citing a photo gone says goes out of m's overview
// (normalizeOverview then); the items they mirror are dropPhotos'.
func dropOverviewPhotos(m *Minutes, cites func(n int) bool) {
	o := m.Overview
	if o == nil {
		return
	}
	o.Conclusions = slices.DeleteFunc(o.Conclusions, func(c MinutesConclusion) bool { return cites(c.Photo) })
	for gi := range o.Groups {
		for ti := range o.Groups[gi].Topics {
			t := &o.Groups[gi].Topics[ti]
			t.Bullets = slices.DeleteFunc(t.Bullets, func(b MinutesBullet) bool { return cites(b.Photo) })
		}
	}
	m.Overview = normalizeOverview(o)
}
