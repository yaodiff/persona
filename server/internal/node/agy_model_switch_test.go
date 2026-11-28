package node

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

func withAgyCatalog(t *testing.T, models []proto.AgentModel) {
	t.Helper()
	agyModelCatalogCache.Lock()
	old := agyModelCatalogCache.byID
	agyModelCatalogCache.byID = map[string]agyCatalogEntry{}
	agyModelCatalogCache.Unlock()
	recordAgyModelCatalog(models)
	t.Cleanup(func() {
		agyModelCatalogCache.Lock()
		agyModelCatalogCache.byID = old
		agyModelCatalogCache.Unlock()
	})
}

func withFastModelLook(t *testing.T) {
	t.Helper()
	old := modelLook
	modelLook = 2 * time.Millisecond
	t.Cleanup(func() { modelLook = old })
}

const agySwitchIdleScreen = "" +
	" Antigravity CLI\n\n" +
	"────────────────────────────────────────────────────────────\n" +
	">\n" +
	"────────────────────────────────────────────────────────────\n" +
	"Gemini 4 Neon high │ Ctx 0.0% 0/1.05M\n"

func TestAgySwitchCommands(t *testing.T) {
	for in, want := range map[string]string{
		"gemini-4-neon-medium":   "/model gemini-4-neon-medium",
		" claude-opus-5-5-high ": "/model claude-opus-5-5-high",
		"gateway/model-x:free":   "/model gateway/model-x:free",
	} {
		got, err := agyModelCommand(in)
		if err != nil || got != want {
			t.Errorf("agyModelCommand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", " ", "--model", "a b", "foo\n/clear", "foo;bar"} {
		if got, err := agyModelCommand(bad); err == nil {
			t.Errorf("agyModelCommand(%q) = %q, want error", bad, got)
		}
	}
	for _, level := range proto.EffortLevels {
		got, err := agyEffortCommand(level)
		if err != nil || got != "/effort "+level {
			t.Errorf("agyEffortCommand(%q) = %q, %v; want /effort %s", level, got, err, level)
		}
	}
	for _, bad := range []string{"", "auto", "ultra", "minimal", "high;rm"} {
		if got, err := agyEffortCommand(bad); err == nil {
			t.Errorf("agyEffortCommand(%q) = %q, want error", bad, got)
		}
	}
}

func TestAgyScreenDeltaAndDraft(t *testing.T) {
	if agyHasDraft(agySwitchIdleScreen) {
		t.Fatal("empty agy input box reported as draft")
	}
	draftScreen := strings.Replace(agySwitchIdleScreen, "\n>\n", "\n> finish this edit\n", 1)
	if !agyHasDraft(draftScreen) {
		t.Fatal("draft in agy input box not detected")
	}

	// Background task / cron status bar rendered below the input box with an extra horizontal rule must not be treated as a draft.
	cronFooterScreen := "" +
		"Antigravity CLI\n\n" +
		"────────────────────────────────────────────────────────────\n" +
		">\n" +
		"────────────────────────────────────────────────────────────\n" +
		"  ● [01:15:13] cron (every hour) running\n" +
		"────────────────────────────────────────────────────────────\n" +
		"Gemini 4 Neon xhigh │ Ctx 20.8% 218.5k/1.05M\n"
	if agyHasDraft(cronFooterScreen) {
		t.Fatal("empty agy input box with background task footer reported as draft")
	}
	cronFooterDraft := strings.Replace(cronFooterScreen, "\n>\n", "\n> unsent text\n", 1)
	if !agyHasDraft(cronFooterDraft) {
		t.Fatal("draft in agy input box with background task footer not detected")
	}
	cronAfter := strings.Replace(cronFooterScreen, "Antigravity CLI\n\n", "Antigravity CLI\n> /model\n  ⎿  Model set to Gemini 4 Neon (XHigh)\n\n", 1)
	if delta := agyScreenDelta(cronFooterScreen, cronAfter); !strings.Contains(delta, "Model set to Gemini 4 Neon (XHigh)") || strings.Contains(delta, "cron (every hour)") {
		t.Fatalf("cron footer delta = %q", delta)
	}

	box := "" +
		"────────────────────────────────────────────────────────────\n" +
		">\n" +
		"────────────────────────────────────────────────────────────\n" +
		"Gemini 4 Neon high │ Ctx 0.0% 0/1.05M\n"
	before := "Antigravity CLI\n> /model\n  ⎿  Model set to Gemini 4 Neon (High)\n" + box
	if delta := agyScreenDelta(before, before); delta != "" {
		t.Fatalf("unchanged screen delta = %q, want empty", delta)
	}
	afterAppend := "Antigravity CLI\n> /model\n  ⎿  Model set to Gemini 4 Neon (High)\n> /model\n  ⎿  Model set to Claude Opus 5.5 (High)\n" + box
	if delta := agyScreenDelta(before, afterAppend); !strings.Contains(delta, "Model set to Claude Opus 5.5 (High)") || strings.Contains(delta, "Gemini 4 Neon (High)") {
		t.Fatalf("appended delta = %q", delta)
	}
	// Terminal scrolled: top line dropped, new confirmation appended.
	afterScroll := "> /model\n  ⎿  Model set to Gemini 4 Neon (High)\n> /model\n  ⎿  Model set to Claude Opus 5.5 (Low)\n" + box
	if delta := agyScreenDelta(before, afterScroll); !strings.Contains(delta, "Model set to Claude Opus 5.5 (Low)") || strings.Contains(delta, "Gemini 4 Neon (High)") {
		t.Fatalf("scrolled delta = %q", delta)
	}
}

func TestAgySwitchOutcomes(t *testing.T) {
	if err := agyModelOutcome("> /model\n  ⎿  Model set to Gemini 4 Neon (Medium)\n", "gemini-4-neon-medium"); err != nil {
		t.Fatalf("valid model switch: %v", err)
	}
	if err := agyModelOutcome("> /model\n  ⎿  Model already set to Gemini 4 Neon (Medium)\n", "gemini-4-neon-medium"); err != nil {
		t.Fatalf("already-set model switch: %v", err)
	}
	if err := agyModelOutcome("> /model\n  ⎿  Invalid model \"bogus\" (available: gemini-4-neon-high)\n", "bogus"); err == nil || errors.As(err, &agySwitchUnconfirmed{}) {
		t.Fatalf("invalid model err = %v, want explicit refusal", err)
	}
	if err := agyModelOutcome("", "gemini-4-neon-medium"); !errors.As(err, &agySwitchUnconfirmed{}) {
		t.Fatalf("empty model delta err = %v, want unconfirmed", err)
	}

	if err := agyEffortOutcome("> /effort\n  ⎿  Effort set to low\n", "low"); err != nil {
		t.Fatalf("valid effort switch: %v", err)
	}
	if err := agyEffortOutcome("> /effort\n  ⎿  Effort already set to xhigh\n", "xhigh"); err != nil {
		t.Fatalf("already-set effort switch: %v", err)
	}
	if err := agyEffortOutcome("> /effort\n  ⎿  claude-opus-5-5 has no \"xhigh\" effort (available: low, medium, high)\n", "xhigh"); err == nil || errors.As(err, &agySwitchUnconfirmed{}) {
		t.Fatalf("unsupported effort err = %v, want explicit refusal", err)
	}
	if err := agyEffortOutcome("> /effort\n  ⎿  Invalid effort \"bogus\"\n", "bogus"); err == nil || errors.As(err, &agySwitchUnconfirmed{}) {
		t.Fatalf("invalid effort err = %v, want explicit refusal", err)
	}
	if err := agyEffortOutcome("", "low"); !errors.As(err, &agySwitchUnconfirmed{}) {
		t.Fatalf("empty effort delta err = %v, want unconfirmed", err)
	}
}

func TestAgyPaneAdvertisesModelAndCatalogEfforts(t *testing.T) {
	catalog := []proto.AgentModel{
		{ID: "gemini-4-neon-low", Name: "Gemini 4 Neon (Low)", Efforts: []string{"low", "medium", "high", "xhigh"}, DefaultEffort: "low"},
		{ID: "gemini-4-neon-high", Name: "Gemini 4 Neon (High)", Efforts: []string{"low", "medium", "high", "xhigh"}, DefaultEffort: "high"},
		{ID: "plain-model", Name: "Plain Model"},
	}
	withAgyCatalog(t, catalog)

	n, f, home := herdrNode(t)
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, "gemini-4-neon-high", "high", agyStartMode(""))

	row := byID(listSessions(t, n))[agyTestRow]
	if row.Capabilities == nil || row.Capabilities.Model == nil || !*row.Capabilities.Model {
		t.Fatalf("expected Capabilities.Model=true on live agy pane, got %+v", row.Capabilities)
	}
	if row.Capabilities.Modes == nil || *row.Capabilities.Modes {
		t.Fatalf("expected Capabilities.Modes=false on live agy pane, got %+v", row.Capabilities)
	}
	if !slices.Equal(row.Efforts, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("Efforts = %v, want [low medium high xhigh]", row.Efforts)
	}

	// Plain model without effort variants advertises Capabilities.Model=true but empty Efforts.
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, "plain-model", "", agyStartMode(""))
	row = byID(listSessions(t, n))[agyTestRow]
	if row.Capabilities == nil || row.Capabilities.Model == nil || !*row.Capabilities.Model {
		t.Fatalf("expected Capabilities.Model=true on plain-model agy pane, got %+v", row.Capabilities)
	}
	if len(row.Efforts) != 0 {
		t.Fatalf("plain model Efforts = %v, want empty", row.Efforts)
	}
}

func TestAgyPaneLiveModelAndEffortSwitch(t *testing.T) {
	withFastModelLook(t)
	catalog := []proto.AgentModel{
		{ID: "gemini-4-neon-high", Name: "Gemini 4 Neon (High)", Efforts: []string{"low", "medium", "high", "xhigh"}, DefaultEffort: "high"},
		{ID: "claude-opus-5-5-low", Name: "Claude Opus 5.5 (Low)", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "low"},
		{ID: "claude-opus-5-5-high", Name: "Claude Opus 5.5 (High)", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high"},
		{ID: "plain-model", Name: "Plain Model"},
	}
	withAgyCatalog(t, catalog)

	n, f, home := herdrNode(t)
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, "gemini-4-neon-high", "high", agyStartMode(""))

	screenText := agySwitchIdleScreen
	f.setScreens(agyTestPane, screenText, screenText)
	var prompts []string
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		text, _ := c.Params["text"].(string)
		prompts = append(prompts, text)
		switch text {
		case "/model claude-opus-5-5-high":
			screenText = "> /model\n  ⎿  Model set to Claude Opus 5.5 (High)\n" + agySwitchIdleScreen
		case "/effort low":
			screenText = "> /model\n  ⎿  Model set to Claude Opus 5.5 (High)\n> /effort\n  ⎿  Effort set to low\n" + agySwitchIdleScreen
		case "/model plain-model":
			screenText = "> /model\n  ⎿  Model set to Claude Opus 5.5 (High)\n> /effort\n  ⎿  Effort set to low\n> /model\n  ⎿  Model set to Plain Model\n" + agySwitchIdleScreen
		}
		f.setScreens(agyTestPane, screenText, screenText)
	})

	// 1. Switch model + matching effort: only /model is sent; effort reconciles to high and efforts narrow to [low medium high].
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "model": "claude-opus-5-5-high", "effort": "high"}); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	if !slices.Equal(prompts, []string{"/model claude-opus-5-5-high"}) {
		t.Fatalf("prompts after model switch = %v", prompts)
	}
	row := byID(listSessions(t, n))[agyTestRow]
	if row.Model != "claude-opus-5-5-high" || row.Effort != "high" || !slices.Equal(row.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("row after model switch = model:%q effort:%q efforts:%v", row.Model, row.Effort, row.Efforts)
	}

	// 2. Unsupported effort for the active model is refused before typing anything.
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "effort": "xhigh"}); err == nil || !strings.Contains(err.Error(), "does not support effort") {
		t.Fatalf("unsupported effort err = %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("unsupported effort typed into pane: %v", prompts)
	}

	// 3. Switch effort to low: /effort low is sent and model updates to sibling variant.
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "effort": "low"}); err != nil {
		t.Fatalf("switch effort: %v", err)
	}
	if !slices.Equal(prompts, []string{"/model claude-opus-5-5-high", "/effort low"}) {
		t.Fatalf("prompts after effort switch = %v", prompts)
	}
	row = byID(listSessions(t, n))[agyTestRow]
	if row.Model != "claude-opus-5-5-low" || row.Effort != "low" {
		t.Fatalf("row after effort switch = model:%q effort:%q", row.Model, row.Effort)
	}

	// 4. Switch to plain model with no efforts: clears effort and efforts list.
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "model": "plain-model"}); err != nil {
		t.Fatalf("switch to plain model: %v", err)
	}
	row = byID(listSessions(t, n))[agyTestRow]
	if row.Model != "plain-model" || row.Effort != "" || len(row.Efforts) != 0 {
		t.Fatalf("row after plain model switch = model:%q effort:%q efforts:%v", row.Model, row.Effort, row.Efforts)
	}
}

func TestAgyPaneSwitchGuardsAndRefusals(t *testing.T) {
	withFastModelLook(t)

	t.Run("busy agent refuses without typing", func(t *testing.T) {
		n, f, home := herdrNode(t)
		a := agyTestAgent(agyTestPane, home, "")
		a["agent_status"] = proto.StatusWorking
		seedAgents(t, n, f, a)
		f.setScreens(agyTestPane, agySwitchIdleScreen, agySwitchIdleScreen)
		_, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "model": "gemini-4-neon-medium"})
		if refusalCode(err) != notDelivered {
			t.Fatalf("busy err = %v, want %s", err, notDelivered)
		}
		if got := f.trace("agent.prompt"); len(got) != 0 {
			t.Fatalf("typed into busy pane: %v", got)
		}
	})

	t.Run("draft in input box refuses without typing", func(t *testing.T) {
		n, f, home := herdrNode(t)
		seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
		draft := strings.Replace(agySwitchIdleScreen, "\n>\n", "\n> half typed prompt\n", 1)
		f.setScreens(agyTestPane, draft, draft)
		_, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "model": "gemini-4-neon-medium"})
		if refusalCode(err) != screen.InputDraft {
			t.Fatalf("draft err = %v, want %s", err, screen.InputDraft)
		}
		if got := f.trace("agent.prompt"); len(got) != 0 {
			t.Fatalf("typed over draft: %v", got)
		}
	})

	t.Run("stale confirmation on screen does not confirm new switch", func(t *testing.T) {
		n, f, home := herdrNode(t)
		seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
		stale := "> /model\n  ⎿  Model set to Old Model\n" + agySwitchIdleScreen
		f.setScreens(agyTestPane, stale, stale)
		_, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "model": "gemini-4-neon-medium"})
		if err == nil || !strings.Contains(err.Error(), "could not confirm") {
			t.Fatalf("stale screen err = %v, want unconfirmed", err)
		}
	})

	t.Run("tui rejection fails cleanly", func(t *testing.T) {
		n, f, home := herdrNode(t)
		seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
		f.setScreens(agyTestPane, agySwitchIdleScreen, agySwitchIdleScreen)
		f.after(func(c herdrCall) {
			if c.Method == "agent.prompt" {
				s := "> /model\n  ⎿  Invalid model \"bad-model\"\n" + agySwitchIdleScreen
				f.setScreens(agyTestPane, s, s)
			}
		})
		_, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "model": "bad-model"})
		if err == nil || !strings.Contains(err.Error(), "rejected model") {
			t.Fatalf("rejection err = %v", err)
		}
	})

	t.Run("interactive picker is dismissed with escape", func(t *testing.T) {
		n, f, home := herdrNode(t)
		seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
		f.setScreens(agyTestPane, agySwitchIdleScreen, agySwitchIdleScreen)
		f.after(func(c herdrCall) {
			if c.Method == "agent.prompt" {
				s := "Select model\n> Gemini 4 Neon (High)\n  ↑/↓ Navigate · enter Confirm\n"
				f.setScreens(agyTestPane, s, s)
			}
		})
		_, err := request(t, n, "session.modes", map[string]any{"session_id": agyTestRow, "model": "gemini-4-neon-medium"})
		if err == nil || !strings.Contains(err.Error(), "interactive picker") {
			t.Fatalf("picker err = %v", err)
		}
		if keys := f.trace("pane.send_keys"); len(keys) == 0 || !strings.Contains(keys[0], "escape") {
			t.Fatalf("expected escape to dismiss picker, got %v", keys)
		}
	})
}
