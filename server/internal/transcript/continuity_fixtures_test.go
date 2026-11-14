package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// These fixtures describe ChatStore, including behavior outside the corpus simulator:
// optional pagination metadata, folds, reload races, and AppModel's request decisions.
// The XCTest replay beside PersonaCore uses the real ChatStore and the Rust binding.
type continuityState struct {
	Items   []proto.Item `json:"items"`
	Covered int          `json:"covered"`
	History *string      `json:"history"`
	HasMore *bool        `json:"has_more"`
	Loaded  bool         `json:"loaded_once"`
}
type continuityAction struct {
	Kind       string `json:"kind"`
	AfterSeq   *int   `json:"after_seq,omitempty"`
	SinceSeq   *int   `json:"since_seq,omitempty"`
	BeforeSeq  *int   `json:"before_seq,omitempty"`
	FromSeq    *int   `json:"from_seq,omitempty"`
	ThroughSeq *int   `json:"through_seq,omitempty"`
	FoldWork   bool   `json:"fold_work,omitempty"`
}
type continuityEvent struct {
	Kind      string       `json:"kind"`
	Items     []proto.Item `json:"items,omitempty"`
	History   *string      `json:"history,omitempty"`
	From      *int         `json:"from_seq,omitempty"`
	To        *int         `json:"to_seq,omitempty"`
	Backlog   bool         `json:"backlog,omitempty"`
	HasMore   *bool        `json:"has_more,omitempty"`
	MoreAfter bool         `json:"more_after,omitempty"`
	More      bool         `json:"more,omitempty"`
	FoldID    string       `json:"fold_id,omitempty"`
}
type continuityExpected struct {
	continuityState
	IDs     []string           `json:"item_ids"`
	Gap     string             `json:"gap"`
	Actions []continuityAction `json:"actions"`
}
type continuityCase struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      []string `json:"source"`
	FromTest    string   `json:"from_test,omitempty"`
	Derived     bool     `json:"derived,omitempty"`
	Input       struct {
		Initial continuityState   `json:"initial"`
		Events  []continuityEvent `json:"events"`
	} `json:"input"`
	Expected []continuityExpected `json:"expected"`
}
type continuityClient struct {
	continuityState
	filling    bool
	fillPages  int
	heard      int
	asked      int
	unfolding  *proto.Item
	opened     map[string]proto.Item
	work       []proto.Item
	rangePages int
}

func continuityPtr[T any](v T) *T { return &v }
func continuityWork(i proto.Item) bool {
	switch i.Kind {
	case proto.KindThinking, proto.KindToolUse, proto.KindToolResult, proto.KindFold:
		return true
	case proto.KindText:
		// Within a fold range assistant text is narration; a final answer outside the range stays.
		return i.Role == "assistant"
	case proto.KindImage:
		return i.ToolUseID != ""
	}
	return false
}
func continuityPositionLE(aSeq, aPart, bSeq, bPart int) bool {
	return aSeq < bSeq || aSeq == bSeq && aPart <= bPart
}
func continuityFolds(f, i proto.Item) bool {
	return f.Fold != nil && continuityPositionLE(f.Seq, part(f.ID), i.Seq, part(i.ID)) &&
		continuityPositionLE(i.Seq, part(i.ID), f.Fold.ThroughSeq, part(f.Fold.ThroughID))
}
func (c *continuityClient) merge(in []proto.Item, backlog bool, more *bool) {
	// ChatStore's newestCopies keeps the last duplicate in cache order before applying incoming items.
	cached := map[string]proto.Item{}
	for _, i := range c.Items {
		cached[i.ID] = i
	}
	evidence := make([]proto.Item, 0, len(c.Items)+len(c.opened)+len(in))
	// A restored cache can repeat an id. Keep the original copies long enough to collect earlier promotion evidence.
	for _, i := range c.Items {
		if i.SupervisionUser || i.Fold != nil && i.Fold.SupervisionUser {
			evidence = append(evidence, i)
		}
	}
	for _, i := range c.opened {
		if i.SupervisionUser {
			evidence = append(evidence, i)
		}
	}
	for _, i := range in {
		if i.SupervisionUser || i.Fold != nil && i.Fold.SupervisionUser {
			evidence = append(evidence, i)
		}
	}
	promotedIDs := map[string]bool{}
	var promotedFolds, promotedWork []proto.Item
	for _, i := range evidence {
		if i.SupervisionUser {
			promotedIDs[i.ID] = true
		}
		if i.Fold != nil {
			promotedFolds = append(promotedFolds, i)
		}
		if continuityWork(i) {
			promotedWork = append(promotedWork, i)
		}
	}
	retain := func(i proto.Item) proto.Item {
		keep := promotedIDs[i.ID]
		if continuityWork(i) {
			for _, f := range promotedFolds {
				keep = keep || continuityFolds(f, i)
			}
		}
		if i.Kind == proto.KindFold {
			for _, w := range promotedWork {
				keep = keep || continuityFolds(i, w)
			}
		}
		if keep {
			i.SupervisionUser = true
			if i.Fold != nil {
				f := *i.Fold
				f.SupervisionUser = true
				i.Fold = &f
			}
		}
		return i
	}
	freshMap := map[string]proto.Item{}
	for _, i := range in {
		freshMap[i.ID] = retain(i)
	}
	c.Items = c.Items[:0]
	for _, i := range cached {
		c.Items = append(c.Items, retain(i))
	}
	var fresh []proto.Item
	for _, i := range freshMap {
		if was, ok := c.opened[i.ID]; ok && was.Fold != nil && i.Fold != nil &&
			continuityPositionLE(i.Fold.ThroughSeq, part(i.Fold.ThroughID), was.Fold.ThroughSeq, part(was.Fold.ThroughID)) {
			continue
		}
		overlapped := false
		for _, work := range freshMap {
			if i.Kind == proto.KindFold && work.Kind != proto.KindFold && continuityWork(work) && continuityFolds(i, work) {
				overlapped = true
			}
		}
		if !overlapped {
			fresh = append(fresh, i)
		}
	}
	var batchWork []proto.Item
	for _, i := range fresh {
		if i.Kind != proto.KindFold && continuityWork(i) {
			batchWork = append(batchWork, i)
		}
	}
	kept := make([]proto.Item, 0, len(c.Items))
	for _, old := range c.Items {
		drop := false
		for _, i := range fresh {
			if i.Kind == proto.KindFold {
				if continuityWork(old) && continuityFolds(i, old) {
					drop = true
				}
				for _, work := range batchWork {
					if continuityFolds(i, work) {
						drop = true
					}
				}
			}
		}
		if old.Kind == proto.KindFold {
			for _, work := range batchWork {
				if continuityFolds(old, work) {
					drop = true
				}
			}
		}
		if !drop {
			kept = append(kept, old)
		}
	}
	c.Items = kept
	ids := map[string]bool{}
	for _, i := range c.Items {
		ids[i.ID] = true
	}
	for _, i := range fresh {
		updated := false
		for n, old := range c.Items {
			if old.ID == i.ID {
				c.Items[n] = i
				updated = true
				break
			}
		}
		if !updated {
			c.Items = append(c.Items, i)
		}
		ids[i.ID] = true
	}
	// ChatStore evaluates this before sorting newly prepended items.
	reaches := false
	for _, i := range in {
		if len(c.Items) > 0 && i.Seq <= c.Items[0].Seq {
			reaches = true
		}
	}
	if more != nil && backlog && (!c.Loaded || len(c.Items) <= len(in)) && (*more || reaches) {
		c.HasMore = continuityPtr(*more)
	}
	if backlog {
		c.Loaded = true
	}
	sort.SliceStable(c.Items, func(i, j int) bool {
		a, b := c.Items[i], c.Items[j]
		return a.Seq < b.Seq || a.Seq == b.Seq && part(a.ID) < part(b.ID)
	})
}
func (c *continuityClient) reset() {
	c.Items = []proto.Item{}
	c.opened = nil
	c.Covered = 0
	c.HasMore = nil
	c.Loaded = false
}
func (c *continuityClient) replace(e continuityEvent, more bool) {
	var keep []proto.Item
	if e.History == nil || c.History != nil && *e.History == *c.History {
		for _, i := range c.Items {
			if e.To != nil && i.Seq > *e.To {
				keep = append(keep, i)
			}
		}
	}
	c.Items = []proto.Item{}
	c.opened = nil
	c.HasMore = continuityPtr(more)
	if e.History != nil {
		c.History = e.History
	}
	c.merge(e.Items, false, nil)
	c.merge(keep, false, nil)
	c.Covered = 0
	if e.To != nil {
		c.Covered = *e.To
	} else if len(c.Items) > 0 {
		c.Covered = c.Items[len(c.Items)-1].Seq
	}
}
func (c *continuityClient) apply(e continuityEvent) gap {
	if e.History != nil && c.History != nil && *e.History != *c.History {
		c.reset()
		c.History = e.History
		if e.From != nil && *e.From > 0 {
			return gapReload
		}
	}
	if e.History != nil {
		c.History = e.History
	}
	if e.To == nil {
		c.merge(e.Items, e.Backlog, e.HasMore)
		if len(c.Items) > 0 {
			c.Covered = max(c.Covered, c.Items[len(c.Items)-1].Seq)
		}
		if e.MoreAfter {
			return gapReload
		}
		return gapNone
	}
	if e.From != nil {
		c.merge(e.Items, e.Backlog, e.HasMore)
		if c.Covered == 0 && *e.From > 0 {
			if c.Loaded {
				return gapReload
			}
			return gapNone
		}
		if *e.From > c.Covered {
			return gapFill
		}
		c.Covered = max(c.Covered, *e.To)
		if e.MoreAfter {
			return gapFill
		}
		return gapNone
	}
	if c.Covered > 0 && len(e.Items) > 0 && e.Items[0].Seq > c.Covered+1 {
		more := true
		if e.HasMore != nil {
			more = *e.HasMore
		}
		c.replace(e, more)
		if e.Backlog {
			c.Loaded = true
		}
		return gapNone
	}
	c.merge(e.Items, e.Backlog, e.HasMore)
	c.Covered = max(c.Covered, *e.To)
	return gapNone
}
func continuityLatest() continuityAction {
	return continuityAction{Kind: "fetch_latest", FoldWork: true}
}
func (c *continuityClient) after() continuityAction {
	return continuityAction{Kind: "fetch_after", AfterSeq: continuityPtr(c.Covered)}
}
func (c *continuityClient) step(e continuityEvent) (gap, []continuityAction, error) {
	actions := []continuityAction{}
	g := gapNone
	switch e.Kind {
	case "http_latest":
		e.Backlog = true
		e.MoreAfter = false
		g = c.apply(e)
	case "ws_items":
		g = c.apply(e)
	case "http_after":
		if !c.filling {
			return g, nil, fmt.Errorf("after response without active fill")
		}
		e.Backlog, e.HasMore = false, nil // AppModel.fill does not pass has_more to apply.
		before, to := c.Covered, e.To
		g = c.apply(e)
		c.fillPages++
		again := g == gapNone && c.heard > c.asked
		progress := c.Covered > before || to != nil && *to > before
		if g == gapNone && !again || g == gapFill && !progress {
			c.filling = false
		} else if g == gapReload || (g == gapFill || again) && c.fillPages == 8 {
			c.filling = false
			actions = append(actions, continuityLatest())
		} else {
			actions = append(actions, c.after())
		}
		if len(actions) > 0 {
			c.asked = c.heard
		}
		return g, actions, nil
	case "continue_fill":
		if !c.filling {
			c.filling = true
			c.fillPages = 0
			actions = append(actions, c.after())
		}
	case "http_reload":
		if e.HasMore == nil {
			return g, nil, fmt.Errorf("reload needs has_more")
		}
		c.replace(e, *e.HasMore)
	case "http_older":
		if e.HasMore == nil {
			return g, nil, fmt.Errorf("older page needs has_more")
		}
		c.HasMore = e.HasMore
		c.merge(e.Items, false, nil)
	case "load_older":
		if c.HasMore != nil && *c.HasMore && len(c.Items) > 0 {
			actions = append(actions, continuityAction{Kind: "fetch_older", BeforeSeq: continuityPtr(c.Items[0].Seq), FoldWork: true})
		}
	case "reconnect":
		a := continuityAction{Kind: "subscribe", FoldWork: true}
		if c.Covered > 0 {
			a.SinceSeq = continuityPtr(c.Covered)
		}
		actions = append(actions, a)
	case "open_fold":
		if c.unfolding != nil {
			return g, nil, fmt.Errorf("overlapping unfold is outside this fixture format")
		}
		for _, i := range c.Items {
			if i.ID == e.FoldID && i.Fold != nil {
				c.unfolding = continuityPtr(i)
			}
		}
		if c.unfolding == nil {
			return g, nil, fmt.Errorf("fold not found: %s", e.FoldID)
		}
		c.work = nil
		c.rangePages = 0
		actions = append(actions, continuityAction{Kind: "fetch_range", FromSeq: continuityPtr(c.unfolding.Seq), ThroughSeq: continuityPtr(c.unfolding.Fold.ThroughSeq)})
	case "http_range":
		if c.unfolding == nil {
			return g, nil, fmt.Errorf("range response without unfold")
		}
		c.rangePages++
		c.work = append(c.work, e.Items...)
		if e.More && len(e.Items) > 0 && c.rangePages < 100 {
			actions = append(actions, continuityAction{Kind: "fetch_range", FromSeq: continuityPtr(e.Items[len(e.Items)-1].Seq + 1), ThroughSeq: continuityPtr(c.unfolding.Fold.ThroughSeq)})
		} else {
			for n, i := range c.Items {
				if i.ID == c.unfolding.ID {
					c.Items = append(c.Items[:n:n], c.Items[n+1:]...)
					var work []proto.Item
					for _, w := range c.work {
						if w.Kind != proto.KindFold {
							work = append(work, w)
						}
					}
					if c.opened == nil {
						c.opened = map[string]proto.Item{}
					}
					c.opened[c.unfolding.ID] = *c.unfolding
					c.merge(work, false, nil)
					break
				}
			}
			c.unfolding = nil
			c.work = nil
		}
	default:
		return g, nil, fmt.Errorf("unknown event kind %q", e.Kind)
	}
	switch g {
	case gapFill:
		c.heard++
		if !c.filling {
			c.filling = true
			c.fillPages = 0
			c.asked = c.heard
			actions = append(actions, c.after())
		}
	case gapReload:
		actions = append(actions, continuityLatest())
	}
	return g, actions, nil
}

func TestContinuityFixtures(t *testing.T) {
	paths, err := filepath.Glob("../../../spec/fixtures/continuity/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("fixtures: %v (%d files)", err, len(paths))
	}
	names := map[string]bool{}
	validName := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var file struct {
			Version int              `json:"version"`
			Cases   []continuityCase `json:"cases"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&file); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if file.Version != 1 || len(file.Cases) == 0 {
			t.Fatalf("%s: missing cases or unsupported version", path)
		}
		for _, tc := range file.Cases {
			if !validName.MatchString(tc.Name) || names[tc.Name] {
				t.Fatalf("invalid or duplicate name %q", tc.Name)
			}
			names[tc.Name] = true
			t.Run(tc.Name, func(t *testing.T) {
				if tc.Description == "" || len(tc.Source) == 0 || tc.FromTest == "" && !tc.Derived {
					t.Fatal("missing provenance")
				}
				if len(tc.Input.Events) == 0 || len(tc.Input.Events) != len(tc.Expected) {
					t.Fatal("each event needs one expected state")
				}
				c := continuityClient{continuityState: tc.Input.Initial}
				// Reuse the existing corpus implementation on the scenarios it models exactly.
				corpusCases := map[string]bool{
					"append_then_fill_lost_batch":               true,
					"empty_batch_advances_covered":              true,
					"overlapping_batch_never_regresses_covered": true,
					"first_line_push_is_baseline":               true,
					"wait_for_first_backlog":                    true,
					"capped_subscription_requests_rest":         true,
					"latest_page_replaces_distant_cache":        true,
				}
				corpus := newClient(tc.Name)
				corpus.covered, corpus.loaded = c.Covered, c.Loaded
				if c.History != nil {
					corpus.history = *c.History
				}
				corpus.merge(c.Items)
				for n, e := range tc.Input.Events {
					g, actions, err := c.step(e)
					if err != nil {
						t.Fatalf("step %d: %v", n+1, err)
					}
					if corpusCases[tc.Name] && (e.Kind == "ws_items" || e.Kind == "http_latest" || e.Kind == "http_after") {
						sp := Span{History: *e.History, To: *e.To}
						if e.From != nil {
							sp.From = *e.From
						}
						cg := corpus.apply(e.Items, sp, e.From != nil, e.MoreAfter)
						if cg != g || corpus.covered != c.Covered || corpus.history != *c.History || !reflect.DeepEqual(ordered(corpus.items), c.Items) {
							t.Fatalf("step %d differs from existing simClient.apply", n+1)
						}
					}
					ids := make([]string, 0, len(c.Items))
					for _, i := range c.Items {
						ids = append(ids, i.ID)
					}
					got := continuityExpected{continuityState: c.continuityState, IDs: ids, Gap: []string{"none", "fill", "reload"}[g], Actions: actions}
					if !reflect.DeepEqual(got, tc.Expected[n]) {
						a, _ := json.MarshalIndent(got, "", "  ")
						b, _ := json.MarshalIndent(tc.Expected[n], "", "  ")
						t.Fatalf("step %d (%s)\ngot %s\nwant %s", n+1, e.Kind, a, b)
					}
				}
			})
		}
	}
}
