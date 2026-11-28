package projection

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The projection stays pure (docs/design/proposals/todos-redesign.md 7.1): no IO and no clock, and nothing of the
// manager, so the same facts always give the same view and the hub's contract types are all it shares. The standard
// library it may use is listed, so a package that reads the disk, the network or randomness never gets in unnoticed.
func TestProjectionStaysPure(t *testing.T) {
	allowed := []string{"cmp", "errors", "fmt", "maps", "net/url", "reflect", "regexp", "slices", "sort", "strconv", "strings",
		"sync", "time", "unicode/utf8", "github.com/nwverse/persona/server/internal/proto",
		"github.com/nwverse/persona/server/internal/assistantwire", "github.com/nwverse/persona/server/internal/wireids"}
	clock := []string{"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker", "LoadLocation"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		timeAs := ""
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !slices.Contains(allowed, path) {
				t.Errorf("%s imports %s: the projection reads only its facts (the listed standard library and wire packages)", name, path)
			}
			if path == "time" {
				timeAs = "time"
				if imp.Name != nil {
					timeAs = imp.Name.Name
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && timeAs != "" && id.Name == timeAs && slices.Contains(clock, sel.Sel.Name) {
					t.Errorf("%s: time.%s: the caller passes now", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// testNow is the moment the tests below read their to-dos at; hours(h) is h hours from it.
var testNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func hours(h int) time.Time { return testNow.Add(time.Duration(h) * time.Hour) }

func at(t time.Time) *time.Time { return &t }

// askingStep is a step whose run waits on the owner, and its parent.
func askingStep(parentDone *time.Time) (parent, step Facts) {
	parent = Facts{ID: "todo_p", Title: "Plan the trip", CreatedAt: hours(-30), UpdatedAt: hours(-30), DoneAt: parentDone,
		Origin: "conv_main", Agents: []Agent{}}
	step = Facts{ID: "todo_s", Title: "Pick the hotel", CreatedAt: hours(-29), UpdatedAt: hours(-29), PartOf: "todo_p",
		Agents:   []Agent{{MachineID: "m1", SessionID: "s1", Title: "Hotel pick"}},
		Sessions: []proto.Session{{MachineID: "m1", SessionID: "s1", State: proto.StateNeedsYou, StateSince: hours(-1)}}}
	return parent, step
}

// A closed parent stays done whatever its steps do (6.2 invariant 1), and its open step is a row of its own then:
// counted, where an open parent carries it and counts once.
func TestClosedParentKeepsItsStepsApart(t *testing.T) {
	parent, step := askingStep(at(hours(-2)))
	list := []Facts{parent, step}
	views := Views(list, testNow)
	if v := views[0]; v.Phase != assistantwire.TodoPhaseDone || v.Section != assistantwire.TodoListSectionJustFinished ||
		v.Steps == nil || v.Steps.Total != 1 || v.Steps.Done != 0 || len(v.Asks) != 0 {
		t.Fatalf("closed parent: %+v", v)
	}
	if c := Counts(list, views); c.NeedsYou != 1 || c.Open != 1 || c.Sections["just_finished"] != 1 {
		t.Fatalf("a closed parent's asking step counts %+v", c)
	}

	parent.DoneAt = nil
	list = []Facts{parent, step}
	views = Views(list, testNow)
	v := views[0]
	if v.Section != assistantwire.TodoListSectionNeedsYou || v.Reason != assistantwire.TodoReasonAgentAsks ||
		v.Line == nil || v.Line.Args["step"] != "Pick the hotel" || len(v.Asks) != 1 ||
		v.Asks[0].ID != "step:todo_s/run:m1/s1@"+hours(-1).Format(time.RFC3339Nano) {
		t.Fatalf("open parent: %+v (line %+v)", v, v.Line)
	}
	if c := Counts(list, views); c.NeedsYou != 1 || c.Open != 1 || c.Sections["needs_you"] != 1 {
		t.Fatalf("an open parent and its asking step count %+v", c)
	}
}

// A part of a list reads a failed start as the whole list does once Superseded, worked out over the whole list, is set
// on it: the related to-do that finished need not be in the part.
func TestSupersededFromTheWholeList(t *testing.T) {
	failed := Facts{ID: "todo_f", Title: "Book the ferry", CreatedAt: hours(-30), UpdatedAt: hours(-3),
		DoneAt: at(hours(-3)), StartError: "Desk Mac is offline"}
	finished := Facts{ID: "todo_d", Title: "book the ferry!", CreatedAt: hours(-30), UpdatedAt: hours(-2),
		DoneAt: at(hours(-2))}
	whole := Views([]Facts{failed, finished}, testNow)[0]
	if whole.Phase != assistantwire.TodoPhaseDone || whole.Line == nil || whole.Line.Key != "superseded" {
		t.Fatalf("the whole list: %+v", whole)
	}
	if v := Views([]Facts{failed}, testNow)[0]; v.Reason != assistantwire.TodoReasonFailedStart {
		t.Fatalf("the part alone: %+v", v)
	}
	sup := Superseded([]Facts{{ID: failed.ID, Title: failed.Title, DoneAt: failed.DoneAt, StartError: failed.StartError},
		{ID: finished.ID, Title: finished.Title, DoneAt: finished.DoneAt}})
	failed.Superseded = sup[failed.ID]
	if v := Views([]Facts{failed}, testNow)[0]; !sameView(v, whole) {
		t.Fatalf("the part with Superseded set: %+v, the whole list's %+v", v, whole)
	}
}

// A step that only waits or is To do does not lift a parent that needs the owner; the more urgent of the two wins.
func TestStepRollsUpOnlyWhenMoreUrgent(t *testing.T) {
	parent, step := askingStep(nil)
	step.Sessions[0].State = proto.StateWorking
	parent.StartError, parent.DoneAt = "Desk Mac is offline", at(hours(-1))
	f := relate([]Facts{parent, step})[0]
	if v := View(f, testNow); v.Reason != assistantwire.TodoReasonFailedStart || v.Line.Args["step"] != "" {
		t.Fatalf("a failed start with a working step: %+v", v)
	}
	parent.StartError, parent.DoneAt = "", nil
	f = relate([]Facts{parent, step})[0]
	if v := View(f, testNow); v.Reason != assistantwire.TodoReasonAgentRunning || v.Line.Args["step"] != "Pick the hotel" {
		t.Fatalf("an idle parent with a working step: %+v", v)
	}
}

// Every run that asks is an ask, the oldest first; line 2 names the oldest, and the to-do's own decision follows.
func TestRunAsksOldestFirst(t *testing.T) {
	f := Facts{ID: "todo_a", CreatedAt: hours(-5), UpdatedAt: hours(-5), Origin: "conv_main",
		Agents: []Agent{{MachineID: "m1", SessionID: "new", Title: "Newer"}, {MachineID: "m1", SessionID: "old", Title: "Older"}},
		Sessions: []proto.Session{
			{MachineID: "m1", SessionID: "new", State: proto.StateNeedsYou, StateSince: hours(-1), Asks: "Which day?"},
			{MachineID: "m1", SessionID: "old", State: proto.StateNeedsYou, StateSince: hours(-3), AttentionReason: "Allow the edit?"},
		},
		Ask: &Ask{Outcome: outcomeDecide, Verdict: "Needs your decision", AskedAt: hours(-2), Question: "Which hotel?"}}
	v := View(f, testNow)
	var ids []string
	for _, a := range v.Asks {
		ids = append(ids, a.ID)
	}
	want := []string{"run:m1/old@" + hours(-3).Format(time.RFC3339Nano), "run:m1/new@" + hours(-1).Format(time.RFC3339Nano),
		"todo:decision@" + hours(-2).Format(time.RFC3339Nano)}
	if !slices.Equal(ids, want) || v.Line.Args["run"] != "Older" || v.Line.Args["text"] != "Allow the edit?" ||
		!v.Since.Equal(hours(-3)) {
		t.Fatalf("asks %v, line %+v, since %v", ids, v.Line, v.Since)
	}
	// Don't handle on the decision round takes the to-do's own ask away.
	f.DecisionReview = &DecisionReview{ReviewAt: hours(-2), DismissalID: "d1"}
	if v := View(f, testNow); len(v.Asks) != 2 {
		t.Fatalf("after Don't handle: %+v", v.Asks)
	}
}

// A needs_user ledger with no review listed still needs the owner, with nothing to say on line 2 but the key.
func TestOwnerDecidesWithoutAReview(t *testing.T) {
	f := Facts{ID: "todo_r", CreatedAt: hours(-5), UpdatedAt: hours(-5), Agents: []Agent{},
		Supervision: &Supervision{State: reviewNeedsUser, UpdatedAt: hours(-1)}}
	if v := View(f, testNow); v.Reason != assistantwire.TodoReasonOwnerDecides || v.Line == nil ||
		v.Line.Key != assistantwire.TodoLineKeyDecision || v.Line.Args != nil || !v.Since.IsZero() {
		t.Fatalf("%+v", v)
	}
}

// TestPersonalVerificationStaysOutOfNeedsYou is the #15680 (#15638) resolution of the placement #16002 briefly gave
// personal verification: the owner chose a calm, separate "to verify" layer (tree review + counts.user_check, a calm
// web tray), so an open user_check must not be counted as red Needs you. #16080 explicitly left that product call to
// #15680. Reintroducing the ownerDecides verify placement makes these assertions fail.
func TestPersonalVerificationStaysOutOfNeedsYou(t *testing.T) {
	f := Facts{ID: "todo_verify", Origin: "conv_lead", CreatedAt: hours(-5), UpdatedAt: hours(-1),
		Delivery: &Delivery{State: deliveryLive, At: hours(-2)},
		Ask:      &Ask{Verdict: "Please verify the result", AskedAt: hours(-1)}}
	if v := View(f, testNow); v.Section != assistantwire.TodoListSectionJustFinished {
		t.Fatalf("ordinary verification section = %q", v.Section)
	}
	f.Supervision = &Supervision{State: reviewUserCheck, UpdatedAt: hours(-1), Reviews: []Review{{Verdict: reviewAccepted, At: hours(-1)}}}
	views := Views([]Facts{f}, testNow)
	v := views[0]
	if v.Section == assistantwire.TodoListSectionNeedsYou || v.Phase == assistantwire.TodoPhaseNeedsYou || v.Reason == assistantwire.TodoReasonVerify {
		t.Fatalf("personal verification must stay out of Needs you under #15680, view: %+v", v)
	}
	if c := Counts([]Facts{f}, views); c.NeedsYou != 0 {
		t.Fatalf("personal verification counts = %+v, want NeedsYou=0", c)
	}
}

// A run's ask is what its pending card asks, by the card's id: the session's own card before the manager's about its
// turn, then the oldest, then the lesser id, whatever order the cards come in. It is safe only when a notification may
// answer the card too, one question with options and nothing else, and its asker did not say going ahead cannot be
// undone: a password, fields to fill in, an MCP server's form, a tool approval, a plan, a question to answer in words,
// several questions, a Just me turn's card (a health conversation's comes marked so), a dialog on the screen, a card
// that cannot be undone and a run whose card the hub does not hold are open, and open_only on the lock screen. The
// to-do's own decision is as risky as the list says, and open when the list says nothing it knows.
func TestRunAskKindAndRiskFromItsCard(t *testing.T) {
	asks := func(cards ...proto.Interaction) assistantwire.OwnerAsk {
		f := Facts{ID: "todo_c", CreatedAt: hours(-5), UpdatedAt: hours(-5), Origin: "conv_main",
			Agents:       []Agent{{MachineID: "m1", SessionID: "s1", Title: "Visa form"}},
			Sessions:     []proto.Session{{MachineID: "m1", SessionID: "s1", State: proto.StateNeedsYou, StateSince: hours(-1)}},
			Interactions: cards}
		v := View(f, testNow)
		if len(v.Asks) != 1 {
			t.Fatalf("asks %+v", v.Asks)
		}
		return v.Asks[0]
	}
	card := func(id, kind string, edit func(*proto.Interaction)) proto.Interaction {
		c := proto.Interaction{ID: id, MachineID: "m1", SessionID: "s1", Kind: kind, CreatedAt: hours(-1)}
		if edit != nil {
			edit(&c)
		}
		return c
	}
	one := func(kind string, edit func(*proto.Interaction)) []proto.Interaction {
		return []proto.Interaction{card("ix", kind, edit)}
	}
	choose := []proto.Question{{Question: "Which office?",
		Options: []proto.QuestionOption{{Label: "Tokyo"}, {Label: "Osaka"}}}}
	safe, open := assistantwire.OwnerAskRiskSafe, assistantwire.OwnerAskRiskOpen
	for _, tc := range []struct {
		name       string
		cards      []proto.Interaction
		kind, risk string
	}{
		{"a question with options", one(proto.IxQuestion, func(c *proto.Interaction) { c.Questions = choose }),
			assistantwire.OwnerAskKindQuestion, safe},
		{"one of its questions in words", one(proto.IxQuestion, func(c *proto.Interaction) {
			c.Questions = append(slices.Clone(choose), proto.Question{Question: "Anything else?"})
		}), assistantwire.OwnerAskKindQuestion, open},
		{"a tool approval", one(proto.IxPermission, nil), assistantwire.OwnerAskKindTool, open},
		{"an approval with fields", one(proto.IxPermission, func(c *proto.Interaction) {
			c.Fields = []proto.Field{{Key: "passport", Label: "Passport number"}}
		}), assistantwire.OwnerAskKindForm, open},
		{"an approval that needs a password", one(proto.IxPermission, func(c *proto.Interaction) {
			c.Fields = []proto.Field{{Key: "user", Label: "User"}, {Key: "password", Label: "Password", Secret: true}}
		}), assistantwire.OwnerAskKindSecret, open},
		{"a screen that asks for a password", one(proto.IxScreen, func(c *proto.Interaction) { c.Secret = true }),
			assistantwire.OwnerAskKindSecret, open},
		{"a dialog on the screen", one(proto.IxScreen, nil), assistantwire.OwnerAskKindQuestion, open},
		{"an MCP server's form", one(proto.IxElicitation, nil), assistantwire.OwnerAskKindForm, open},
		{"a plan", one(proto.IxPlan, nil), assistantwire.OwnerAskKindPlan, open},
		{"one to choose several of", one(proto.IxQuestion, func(c *proto.Interaction) {
			c.Questions = []proto.Question{{Question: "Which sights?", MultiSelect: true,
				Options: []proto.QuestionOption{{Label: "Kinkaku-ji"}, {Label: "Ginkaku-ji"}}}}
		}), assistantwire.OwnerAskKindQuestion, safe},
		{"several questions with options", one(proto.IxQuestion, func(c *proto.Interaction) {
			c.Questions = append(slices.Clone(choose), proto.Question{Question: "Which day?",
				Options: []proto.QuestionOption{{Label: "Friday"}, {Label: "Saturday"}}})
		}), assistantwire.OwnerAskKindQuestion, open},
		{"a Just me turn's question", one(proto.IxQuestion, func(c *proto.Interaction) {
			c.Questions, c.Private = choose, true
		}), assistantwire.OwnerAskKindQuestion, open},
		{"a question its asker says cannot be undone", one(proto.IxQuestion, func(c *proto.Interaction) {
			c.Questions, c.Reversible = choose, new(bool)
		}), assistantwire.OwnerAskKindQuestion, open},
		{"a question its asker says can be undone", one(proto.IxQuestion, func(c *proto.Interaction) {
			yes := true
			c.Questions, c.Reversible = choose, &yes
		}), assistantwire.OwnerAskKindQuestion, safe},
		{"the oldest of two cards", []proto.Interaction{
			card("newer", proto.IxPermission, func(c *proto.Interaction) { c.CreatedAt = hours(-1) }),
			card("ix", proto.IxQuestion, func(c *proto.Interaction) { c.Questions, c.CreatedAt = choose, hours(-2) }),
		}, assistantwire.OwnerAskKindQuestion, safe},
		{"the session's own card before the manager's older one about its turn", []proto.Interaction{
			card("work", proto.IxQuestion, func(c *proto.Interaction) {
				c.ToolName, c.Questions, c.CreatedAt = assistantwire.WorkCardTool, choose, hours(-2)
			}),
			card("ix", proto.IxPermission, nil),
		}, assistantwire.OwnerAskKindTool, open},
		{"two cards of one instant, the lesser id", []proto.Interaction{
			card("ix_z", proto.IxQuestion, func(c *proto.Interaction) { c.Questions = choose }),
			card("ix", proto.IxPermission, nil),
		}, assistantwire.OwnerAskKindTool, open},
	} {
		wantLock := assistantwire.OwnerAskLockscreenOpenOnly
		if tc.risk == safe {
			wantLock = assistantwire.OwnerAskLockscreenAnswer
		}
		backward := slices.Clone(tc.cards)
		slices.Reverse(backward)
		for _, cards := range [][]proto.Interaction{tc.cards, backward} {
			a := asks(cards...)
			if a.Kind != tc.kind || a.Risk != tc.risk || a.Lockscreen != wantLock || a.Interaction != "ix" {
				t.Errorf("%s: kind %s, risk %s, lockscreen %s, interaction %q; want %s, %s, %s, ix", tc.name, a.Kind,
					a.Risk, a.Lockscreen, a.Interaction, tc.kind, tc.risk, wantLock)
			}
		}
	}
	another := card("ix_other", proto.IxQuestion, func(c *proto.Interaction) { c.SessionID, c.Questions = "s2", choose })
	if a := asks(another); a.Kind != assistantwire.OwnerAskKindQuestion || a.Interaction != "" || a.Risk != open ||
		a.Lockscreen != assistantwire.OwnerAskLockscreenOpenOnly {
		t.Errorf("a run whose card the hub does not hold: %+v", a)
	}

	for listed, want := range map[string]string{safe: safe, open: open, "": open, "later": open} {
		f := Facts{ID: "todo_d", CreatedAt: hours(-5), UpdatedAt: hours(-5), Origin: "conv_main", Agents: []Agent{},
			Ask: &Ask{Outcome: outcomeDecide, Verdict: "Needs your decision", AskedAt: hours(-2), Question: "Which hotel?",
				Risk: listed}}
		if v := View(f, testNow); len(v.Asks) != 1 || v.Asks[0].Risk != want {
			t.Errorf("a decision listed %q: %+v", listed, v.Asks)
		}
	}
}
