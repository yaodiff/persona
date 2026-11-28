package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits of UI version 6 (docs/extensions/extensions.md "UI version 6").
const (
	// A page has at most ImagesMost pictures (image components, rows and tiles with one).
	ImagesMost = 50
	// A candlestick has at most CandlesMost points; the other charts PointsMost.
	CandlesMost = 240
	// A donut or a pie has 2 to SlicesMost slices.
	SlicesMost = 8
	// A row or a tile has at most RowActionsMost extra actions, SwipeMost of them under a swipe; a page at most
	// RowsWithActionsMost rows (and tiles) with actions.
	RowActionsMost      = 4
	SwipeMost           = 2
	RowsWithActionsMost = 100
	// A slider has at most SliderStepsMost steps.
	SliderStepsMost = 10000
	// A multichoice has 1 to MultiChoicesMost choices.
	MultiChoicesMost = 50
	// A text in the title style has at most TitleMost characters.
	TitleMost = 200
	// ReservedName starts the names the hub gives the fields it draws an older app (a multichoice as toggles):
	// an extension's names never start with it.
	ReservedName = "~"
)

// The wire formats of date, time and datetime fields: the user's local time, context.tz.
const (
	DateLayout     = "2006-01-02"
	ClockLayout    = "15:04"
	DateTimeLayout = "2006-01-02T15:04"
)

// FieldLayout is how a date, time or datetime field writes its value; "" for other kinds.
func FieldLayout(kind string) string {
	switch kind {
	case FieldKindDate:
		return DateLayout
	case FieldKindTime:
		return ClockLayout
	case FieldKindDatetime:
		return DateTimeLayout
	}
	return ""
}

// pageCheck is what CheckView keeps across a page's components.
type pageCheck struct {
	names, keys map[string]bool
	// hubNames lets names the hub makes (ReservedName) through: a view the hub fitted for an older app.
	hubNames bool
	images   int
	withActs int
	scenes   int
}

func (p *pageCheck) name(n string) error {
	if p.names[n] {
		return fmt.Errorf("field %q twice", n)
	}
	if !p.hubNames && strings.HasPrefix(n, ReservedName) {
		return fmt.Errorf("field %q: a name never starts with %q (the hub's own)", n, ReservedName)
	}
	p.names[n] = true
	return nil
}

func (p *pageCheck) key(k string) error {
	if k == "" {
		return nil
	}
	if p.keys[k] {
		return fmt.Errorf("key %q twice", k)
	}
	p.keys[k] = true
	return nil
}

// checkV6 holds a component to UI version 6's rules the schema cannot say: pictures, controls, row actions, tiles,
// charts, sparks, colors, dates and choices.
func (p *pageCheck) checkV6(e *Component) error {
	if e.Thumbnail != nil || e.Avatar != nil {
		if err := checkPictures(e.Thumbnail, e.Avatar); err != nil {
			return err
		}
		if e.Thumbnail != nil || e.Avatar != nil && (e.Avatar.URL != "" || e.Avatar.Ref != "") {
			p.images++
		}
	}
	if err := checkBadge(e.Badge); err != nil {
		return err
	}
	for k := range e.Badges {
		if err := checkBadge(&e.Badges[k]); err != nil {
			return fmt.Errorf("badge %d: %v", k, err)
		}
	}
	if err := checkSpark(e.Spark); err != nil {
		return err
	}
	if e.Type == "text" && (e.Style == TextStyleTitle || e.Style == TextStyleHeadline) && utf8.RuneCountInString(e.Text) > TitleMost {
		return fmt.Errorf("a %s has at most %d characters", e.Style, TitleMost)
	}
	switch e.Type {
	case "image":
		if e.Src == nil {
			return errors.New("an image has a src")
		}
		if err := checkSource(e.Src); err != nil {
			return err
		}
		p.images++
	case "row":
		if c := e.Control; c != nil {
			if e.Key == "" {
				return errors.New("a row with a control has a key")
			}
			if err := p.checkControl(c, false); err != nil {
				return err
			}
		}
		if len(e.Actions) > 0 {
			if err := checkRowActions(e.Actions); err != nil {
				return err
			}
			p.withActs++
		}
	case "grid", "carousel":
		for k := range e.Tiles {
			if err := p.checkTile(&e.Tiles[k]); err != nil {
				return fmt.Errorf("tile %d: %v", k, err)
			}
		}
	case "field":
		if err := checkFieldV6(e); err != nil {
			return err
		}
	case "header":
		if len(e.Facts) > 8 {
			return errors.New("a header has at most 8 facts")
		}
		for k := range e.Facts {
			if err := checkFact(&e.Facts[k]); err != nil {
				return fmt.Errorf("fact %d: %v", k, err)
			}
		}
		if a := e.Avatar; a != nil {
			if err := checkPictures(nil, a); err != nil {
				return err
			}
		}
	}
	for k := range e.Comments {
		if err := checkComment(&e.Comments[k]); err != nil {
			return fmt.Errorf("comment %d: %v", k, err)
		}
	}
	for k := range e.Threads {
		for l := range e.Threads[k].Comments {
			if err := checkComment(&e.Threads[k].Comments[l]); err != nil {
				return fmt.Errorf("thread %d comment %d: %v", k, l, err)
			}
		}
	}
	for k := range e.Entries {
		if err := checkBadge(e.Entries[k].Badge); err != nil {
			return fmt.Errorf("entry %d: %v", k, err)
		}
	}
	if p.images > ImagesMost {
		return fmt.Errorf("a page has at most %d pictures", ImagesMost)
	}
	if p.withActs > RowsWithActionsMost {
		return fmt.Errorf("a page has at most %d rows with actions", RowsWithActionsMost)
	}
	return nil
}

func checkComment(c *Comment) error {
	if c.Avatar != nil {
		if err := checkPictures(nil, c.Avatar); err != nil {
			return err
		}
	}
	return checkBadge(c.Badge)
}

// checkSource holds a picture's source to one of an https url and a ref; path is the hub's to write.
func checkSource(s *ImageSource) error {
	if s.Path != "" {
		return errors.New("src: path is written by the hub only (give a ref)")
	}
	if (s.URL == "") == (s.Ref == "") {
		return errors.New("src: exactly one of url and ref")
	}
	if s.URL != "" {
		return checkHTTPS(s.URL, "src")
	}
	return nil
}

// checkPictures holds a row's (or a header's, a comment's) thumbnail and avatar to their sources.
func checkPictures(t *Thumbnail, a *Avatar) error {
	if t != nil {
		if t.Path != "" {
			return errors.New("thumbnail: path is written by the hub only (give a ref)")
		}
		if (t.URL == "") == (t.Ref == "") {
			return errors.New("thumbnail: exactly one of url and ref")
		}
		if t.URL != "" {
			if err := checkHTTPS(t.URL, "thumbnail"); err != nil {
				return err
			}
		}
	}
	if a != nil {
		if a.Path != "" {
			return errors.New("avatar: path is written by the hub only (give a ref)")
		}
		if a.URL != "" && a.Ref != "" {
			return errors.New("avatar: at most one of url and ref")
		}
		if a.URL == "" && a.Text == "" && a.Ref == "" {
			return errors.New("an avatar has a url, a ref or a text")
		}
		if a.URL != "" {
			if err := checkHTTPS(a.URL, "avatar"); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckHTTPS holds raw to an https url without credentials (no user or password): what every link and picture's url is.
func CheckHTTPS(raw, what string) error {
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("%s needs an https url", what)
	}
	return nil
}

// checkHTTPS is CheckHTTPS under the name the picture checks use.
func checkHTTPS(raw, what string) error { return CheckHTTPS(raw, what) }

// checkFact holds a fact's label, value and icon: a label of at most 200 characters, a value of 1 to 2000, an icon among
// NavIcons.
func checkFact(f *Fact) error {
	if utf8.RuneCountInString(f.Label) > 200 {
		return errors.New("a fact's label has at most 200 characters")
	}
	if n := utf8.RuneCountInString(f.Value); n < 1 || n > 2000 {
		return errors.New("a fact has a value of 1 to 2000 characters")
	}
	if f.Icon != "" && !slices.Contains(NavIcons, f.Icon) {
		return fmt.Errorf("fact: unknown icon %q", f.Icon)
	}
	return nil
}

func checkBadge(b *Badge) error {
	if b != nil && b.Color != "" && b.Tone != "" {
		return errors.New("a badge has a tone or a color, not both")
	}
	return nil
}

func checkSpark(s *Spark) error {
	if s == nil {
		return nil
	}
	if s.Color != "" && s.Tone != "" {
		return errors.New("spark: a tone or a color, not both")
	}
	if len(s.Values) < 2 {
		return errors.New("spark: at least two values")
	}
	for _, x := range s.Values {
		if !finite(x) {
			return errors.New("spark: numbers")
		}
	}
	return nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

func finitePtr(x *float64) bool { return x == nil || finite(*x) }

// checkControl holds a row's or a tile's control to its kind: a switch has no numbers; a slider a min below a max,
// its value between them, a step more than 0 and at most SliderStepsMost steps; a stepper a value and a step more than
// 0, its value between its min and max when it has them.
func (p *pageCheck) checkControl(c *RowControl, tile bool) error {
	if err := p.name(c.Name); err != nil {
		return fmt.Errorf("control: %v", err)
	}
	if !finitePtr(c.Value) || !finitePtr(c.Min) || !finitePtr(c.Max) || !finitePtr(c.Step) {
		return errors.New("control: numbers")
	}
	if len(c.Action.Values) >= 8 && c.Action.Values[c.Name] == "" {
		return errors.New("control: its action carries at most 7 values (the control's own is the 8th)")
	}
	if tile && c.Kind != RowControlKindSwitch {
		return errors.New("a tile's control is a switch")
	}
	switch c.Kind {
	case RowControlKindSwitch:
		if c.Value != nil || c.Min != nil || c.Max != nil || c.Step != nil {
			return errors.New("a switch has on, no value, min, max or step")
		}
	case RowControlKindSlider:
		if c.Min == nil || c.Max == nil || *c.Min >= *c.Max {
			return errors.New("a slider has a min below its max")
		}
		if c.Value == nil || *c.Value < *c.Min || *c.Value > *c.Max {
			return errors.New("a slider has a value between its min and max")
		}
		step := (*c.Max - *c.Min) / 100
		if c.Step != nil {
			step = *c.Step
		}
		if step <= 0 {
			return errors.New("a slider's step is more than 0")
		}
		if (*c.Max-*c.Min)/step > SliderStepsMost {
			return fmt.Errorf("a slider has at most %d steps", SliderStepsMost)
		}
	case RowControlKindStepper:
		if c.Value == nil || c.Step == nil || *c.Step <= 0 {
			return errors.New("a stepper has a value and a step more than 0")
		}
		if c.Min != nil && c.Max != nil && *c.Min >= *c.Max {
			return errors.New("a stepper's min is below its max")
		}
		if c.Min != nil && *c.Value < *c.Min || c.Max != nil && *c.Value > *c.Max {
			return errors.New("a stepper's value is between its min and max")
		}
	default:
		return errors.New("a control is a switch, a slider or a stepper")
	}
	if c.On && c.Kind != RowControlKindSwitch {
		return errors.New("on is a switch's")
	}
	return nil
}

// checkRowActions holds a row's (a tile's) extra actions: titles once each, at most SwipeMost under a swipe, a
// destructive one confirmed.
func checkRowActions(acts []RowAction) error {
	if len(acts) > RowActionsMost {
		return fmt.Errorf("at most %d actions", RowActionsMost)
	}
	titles := map[string]bool{}
	swipe := 0
	for k, a := range acts {
		if titles[a.Title] {
			return fmt.Errorf("action %d: title %q twice", k, a.Title)
		}
		titles[a.Title] = true
		if a.Swipe {
			swipe++
		}
		if a.Role == ButtonRoleDestructive && strings.TrimSpace(a.Action.Confirm) == "" {
			return fmt.Errorf("action %d: a destructive action asks first (confirm)", k)
		}
	}
	if swipe > SwipeMost {
		return fmt.Errorf("at most %d actions under a swipe", SwipeMost)
	}
	return nil
}

func (p *pageCheck) checkTile(t *Tile) error {
	if err := p.key(t.Key); err != nil {
		return err
	}
	if t.Icon != "" && t.Image != nil {
		return errors.New("a tile has at most one of icon and image")
	}
	if t.Gauge != nil && (t.Image != nil || t.Spark != nil) {
		return errors.New("a tile with a gauge has no image or spark: the ring takes their place")
	}
	if t.Image != nil {
		if err := checkSource(t.Image); err != nil {
			return err
		}
		p.images++
	}
	if err := checkBadge(t.Badge); err != nil {
		return err
	}
	if err := checkSpark(t.Spark); err != nil {
		return err
	}
	if t.Control != nil {
		if t.Key == "" {
			return errors.New("a tile with a control has a key")
		}
		if err := p.checkControl(t.Control, true); err != nil {
			return err
		}
	}
	if len(t.Actions) > 0 {
		if err := checkRowActions(t.Actions); err != nil {
			return err
		}
		p.withActs++
	}
	return nil
}

// checkFieldV6 holds a field's UI version 6 parts: dates and times in their formats, earliest not after latest, a
// multichoice's value a JSON array of its choices.
func checkFieldV6(e *Component) error {
	layout := FieldLayout(e.Kind)
	if layout == "" && (e.Earliest != "" || e.Latest != "") {
		return errors.New("earliest and latest are a date's, a time's or a datetime's")
	}
	if layout != "" {
		var at [3]time.Time
		for k, s := range []string{e.Value, e.Earliest, e.Latest} {
			if s == "" {
				continue
			}
			t, err := time.Parse(layout, s)
			if err != nil || t.Format(layout) != s {
				return fmt.Errorf("a %s is written %s: %q", e.Kind, layout, s)
			}
			at[k] = t
		}
		if e.Earliest != "" && e.Latest != "" && at[1].After(at[2]) {
			return errors.New("earliest is not after latest")
		}
	}
	if e.Kind == FieldKindMultichoice {
		if len(e.Choices) == 0 || len(e.Choices) > MultiChoicesMost {
			return fmt.Errorf("a multichoice has 1 to %d choices", MultiChoicesMost)
		}
		if _, err := MultiValue(e.Value, e.Choices); err != nil {
			return err
		}
	}
	return nil
}

// MultiValue reads a multichoice's value (a JSON array of strings, or empty for none), each one of choices, once.
func MultiValue(value string, choices []Choice) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	var vs []string
	if err := json.Unmarshal([]byte(value), &vs); err != nil {
		return nil, errors.New("a multichoice's value is a JSON array of its choices' values")
	}
	seen := map[string]bool{}
	for _, x := range vs {
		if seen[x] {
			return nil, fmt.Errorf("multichoice: %q twice", x)
		}
		seen[x] = true
		if !slices.ContainsFunc(choices, func(c Choice) bool { return c.Value == x }) {
			return nil, fmt.Errorf("multichoice: %q is not one of its choices", x)
		}
	}
	return vs, nil
}

// checkChartV6 holds a chart to its kind: a candlestick's one series of candles, a donut's or a pie's one series of a
// few slices, colors that are no tones and differ.
func checkChartV6(e Component) error {
	colors := map[string]bool{}
	for k, s := range e.Series {
		if s.Color != "" && s.Tone != "" {
			return fmt.Errorf("series %d: a tone or a color, not both", k)
		}
		if s.Color != "" {
			if colors[s.Color] {
				return fmt.Errorf("series %d: color %s twice", k, s.Color)
			}
			colors[s.Color] = true
		}
	}
	if e.Rising != "" && e.Kind != ChartKindCandlestick {
		return errors.New("rising is a candlestick's")
	}
	if e.Center != "" && e.Kind != ChartKindDonut {
		return errors.New("center is a donut's")
	}
	ohlc := func(pt ChartPoint) bool {
		return pt.Open != nil || pt.High != nil || pt.Low != nil || pt.Close != nil || pt.Volume != nil
	}
	switch e.Kind {
	case ChartKindCandlestick:
		if len(e.Series) != 1 || e.Stacked {
			return errors.New("a candlestick has one series, not stacked")
		}
		pts := e.Series[0].Points
		if len(pts) > CandlesMost {
			return fmt.Errorf("a candlestick has at most %d points", CandlesMost)
		}
		vol := pts[0].Volume != nil
		for n, pt := range pts {
			if pt.Open == nil || pt.High == nil || pt.Low == nil || pt.Close == nil ||
				!finitePtr(pt.Open) || !finitePtr(pt.High) || !finitePtr(pt.Low) || !finitePtr(pt.Close) || !finitePtr(pt.Volume) {
				return fmt.Errorf("point %d: a candle has open, high, low and close", n)
			}
			o, h, l, c := *pt.Open, *pt.High, *pt.Low, *pt.Close
			if l > min(o, c) || h < max(o, c) {
				return fmt.Errorf("point %d: low is at most open and close, high at least", n)
			}
			if (pt.Volume != nil) != vol {
				return errors.New("every candle has a volume, or none")
			}
		}
		return nil
	case ChartKindDonut, ChartKindPie:
		if len(e.Series) != 1 || e.Stacked {
			return fmt.Errorf("a %s has one series, not stacked", e.Kind)
		}
		pts := e.Series[0].Points
		if len(pts) < 2 || len(pts) > SlicesMost {
			return fmt.Errorf("a %s has 2 to %d slices", e.Kind, SlicesMost)
		}
		labels, sum := map[string]bool{}, 0.0
		for n, pt := range pts {
			if pt.Value < 0 {
				return fmt.Errorf("point %d: a slice is not below 0", n)
			}
			if labels[pt.Label] {
				return fmt.Errorf("point %d: label %q twice", n, pt.Label)
			}
			labels[pt.Label] = true
			sum += pt.Value
		}
		if sum <= 0 {
			return fmt.Errorf("a %s adds up to more than 0", e.Kind)
		}
	}
	for _, s := range e.Series {
		if len(s.Points) > PointsMost {
			return fmt.Errorf("a series has 1 to %d points", PointsMost)
		}
		for n, pt := range s.Points {
			if ohlc(pt) {
				return fmt.Errorf("point %d: open, high, low, close and volume are a candlestick's", n)
			}
		}
	}
	return nil
}

// HasImageRef says whether a component shows a picture the extension serves (a ref): an image's src, a scene's
// poster, a row's thumbnail or avatar, a header's avatar, a tile's image, a comment's avatar, or a diff thread
// comment's avatar.
func HasImageRef(e *Component) bool {
	if e.Src != nil && e.Src.Ref != "" || e.Poster != nil && e.Poster.Ref != "" || e.Thumbnail != nil && e.Thumbnail.Ref != "" ||
		e.Avatar != nil && e.Avatar.Ref != "" {
		return true
	}
	for _, t := range e.Tiles {
		if t.Image != nil && t.Image.Ref != "" {
			return true
		}
	}
	for _, c := range e.Comments {
		if c.Avatar != nil && c.Avatar.Ref != "" {
			return true
		}
	}
	for _, th := range e.Threads {
		for _, c := range th.Comments {
			if c.Avatar != nil && c.Avatar.Ref != "" {
				return true
			}
		}
	}
	return false
}
