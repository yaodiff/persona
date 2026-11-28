package protectedapps

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
)

func TestStepsAppsAndCheckRejectMultiActionAndAppToolBypasses(t *testing.T) {
	// Multi-action step pairing launch_app with a second action targeting System Settings:
	// StepsApps must inspect every action in the step, not short-circuit after launch_app.
	launchAndClick := json.RawMessage(`[{"launch_app": {"name": "Notes"}, "click": {"app": "System Settings", "role": "button"}}]`)
	apps, all := StepsApps("", launchAndClick)
	if !all || !slices.Contains(apps, "Notes") || !slices.Contains(apps, "System Settings") {
		t.Fatalf("StepsApps(launch_app+click) = %v, all=%v; want Notes and System Settings with all=true", apps, all)
	}
	if !UnnamedSteps("", json.RawMessage(`[{"launch_app": {"name": "Notes"}, "press_key": {"key": "return"}}]`)) {
		t.Fatal("UnnamedSteps missed unnamed sibling action alongside launch_app")
	}

	// launch_app providing both a benign name and a protected bundle_id must check both.
	dualLaunch := json.RawMessage(`[{"launch_app": {"name": "Notes", "bundle_id": "com.apple.systempreferences"}}]`)
	apps, all = StepsApps("", dualLaunch)
	if !all || !slices.Contains(apps, "com.apple.systempreferences") {
		t.Fatalf("StepsApps(dual launch_app) = %v, all=%v; want com.apple.systempreferences", apps, all)
	}

	// Multi-action step with two different apps must deterministically report both
	// (never drop one depending on Go map iteration order).
	twoActions := json.RawMessage(`[{"click": {"app": "System Settings", "role": "button"}, "scroll": {"app": "Notes", "direction": "down"}}]`)
	for range 20 {
		apps, all = StepsApps("", twoActions)
		if !all || !slices.Contains(apps, "System Settings") || !slices.Contains(apps, "Notes") {
			t.Fatalf("StepsApps(two actions) = %v, all=%v; want both System Settings and Notes", apps, all)
		}
	}

	// End-to-end Look.Check must refuse all of these whether System Settings is visible or minimized.
	for _, shown := range []bool{true, false} {
		m := behindEdge()
		m.settings = shown
		runChecks(t, m, []checkCase{
			{"launch_app plus protected click", "run_steps", `{"steps": [{"launch_app": {"name": "Notes"}, "click": {"app": "System Settings", "role": "button"}}]}`, "named"},
			{"launch_app dual name and protected bundle_id", "run_steps", `{"steps": [{"launch_app": {"name": "Notes", "bundle_id": "com.apple.systempreferences"}}]}`, "named"},
			{"two actions with protected and benign app", "run_steps", `{"steps": [{"click": {"app": "System Settings", "role": "button"}, "scroll": {"app": "Notes", "direction": "down"}}]}`, "named"},
			{"bring_to_front by name in step", "run_steps", `{"steps": [{"bring_to_front": {"name": "System Settings"}}]}`, "named"},
			{"kill_app by bundle_id in step", "run_steps", `{"steps": [{"kill_app": {"bundle_id": "com.apple.systempreferences"}}]}`, "named"},
			{"tool form bring_to_front by bundle_id in step", "run_steps", `{"steps": [{"tool": "bring_to_front", "args": {"bundle_id": "com.apple.systempreferences"}}]}`, "named"},
			{"invoke_menu by name in step", "run_steps", `{"steps": [{"invoke_menu": {"name": "System Settings"}}]}`, "named"},
		})
	}
}

func TestTokenCheckDoesNotBypassOnNonBareRowTokensWithWindowID(t *testing.T) {
	m := behindEdge()
	runChecks(t, m, []checkCase{
		{"bare row of Edge window still allowed", "click", `{"window_id": 130, "element_token": "11"}`, ""},
		{"uppercase snapshot prefix with Edge window_id", "click", `{"window_id": 130, "element_token": "S00000099:3"}`, "named"},
		{"uppercase hex snapshot with Edge window_id", "click", `{"window_id": 130, "element_token": "s0000009A:3"}`, "named"},
		{"unrecognized colon token with Edge window_id fails closed", "click", `{"window_id": 130, "element_token": "snap99:3"}`, "token"},
		{"non-numeric token with Edge window_id fails closed", "click", `{"window_id": 130, "element_token": "row-abc"}`, "token"},
	})
}

func TestRayAndPointCheckHandleTinyPositiveCoordsWithoutOverflowOrNaN(t *testing.T) {
	notesRect := Rect{X: 460, Y: 195, Width: 1000, Height: 660}
	for _, tc := range []struct {
		name string
		p    point
	}{
		{"tiny diagonal", point{1e-306, 1e-306}},
		{"tiny vertical", point{0, 1e-306}},
		{"tiny horizontal", point{1e-306, 0}},
		{"subnormal ratio", point{1e-320, 1e200}},
	} {
		from, to := ray(notesRect, tc.p)
		for _, v := range []float64{from.x, from.y, to.x, to.y} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("%s: ray produced non-finite coordinate: from=%+v to=%+v", tc.name, from, to)
			}
		}
	}

	m := behindEdge()
	runChecks(t, m, []checkCase{
		{"tiny diagonal ray in Notes crosses System Settings", "click", `{"pid": 656, "window_id": 159, "x": 1e-306, "y": 1e-306}`, "covered"},
		{"tiny angled ray in Notes crosses System Settings below Finder", "click", `{"pid": 656, "window_id": 159, "x": 7e-307, "y": 4e-307}`, "covered"},
	})
}

func TestJSONTypeErrorAndCaseFoldedKeyBypasses(t *testing.T) {
	// Type-error sibling fields must not abort extraction of protected app identifiers.
	for _, raw := range []string{
		`[{"click": {"app": "System Settings", "bundle_id": 1}}]`,
		`[{"click": {"app": "System Settings", "name": 1}}]`,
		`[{"kill_app": {"app": 1, "bundle_id": "com.apple.systempreferences"}}]`,
		`[{"launch_app": {"name": "System Settings", "bundle_id": 1}}]`,
	} {
		apps, _ := StepsApps("", json.RawMessage(raw))
		if len(apps) == 0 || !Is(apps[0], apps[0]) {
			t.Fatalf("StepsApps(%s) = %v, want protected app extracted despite non-string sibling", raw, apps)
		}
	}

	// Case-folded uppercase duplicate keys must not overwrite lowercase canonical fields.
	for _, shown := range []bool{true, false} {
		m := behindEdge()
		m.settings = shown
		runChecks(t, m, []checkCase{
			{"step type-error bundle_id sibling", "run_steps", `{"steps": [{"click": {"app": "System Settings", "bundle_id": 1}}]}`, "named"},
			{"step type-error name sibling", "run_steps", `{"steps": [{"click": {"app": "System Settings", "name": 1}}]}`, "named"},
			{"step case-folded APP overwrite", "run_steps", `{"steps": [{"click": {"app": "System Settings", "APP": "Notes"}}]}`, "named"},
			{"step case-folded BUNDLE_ID overwrite", "run_steps", `{"steps": [{"kill_app": {"bundle_id": "com.apple.systempreferences", "BUNDLE_ID": "com.apple.Notes"}}]}`, "named"},
			{"top-level case-folded APP overwrite", "click", `{"app": "System Settings", "APP": "Notes"}`, "named"},
			{"top-level case-folded STEPS overwrite", "run_steps", `{"steps": [{"click": {"app": "System Settings"}}], "STEPS": []}`, "named"},
			{"top-level type-error name sibling", "click", `{"app": "System Settings", "name": 123}`, "named"},
			{"target object case-folded WINDOW_ID overwrite", "click", `{"target": {"kind": "window", "window_id": 18640, "WINDOW_ID": 130}, "x": 1, "y": 1}`, "named"},
			{"target object type-error pid sibling", "click", `{"target": {"kind": "window", "window_id": 18640, "pid": "bad"}, "x": 1, "y": 1}`, "named"},
		})
	}
}

func TestStepsTargetInheritanceIsolation(t *testing.T) {
	m := behindEdge()
	m.active = "System Settings"
	runChecks(t, m, []checkCase{
		{"same-step sibling click must not supply target for press_key", "run_steps", `{"steps": [{"click": {"app": "Notes", "role": "button"}, "press_key": {"key": "space"}}]}`, "front"},
		{"windowless clipboard_read must not pollute last target", "run_steps", `{"steps": [{"clipboard_read": {"app": "Notes"}}, {"press_key": {"key": "space"}}]}`, "front"},
		{"browser_click must not pollute last target", "run_steps", `{"steps": [{"browser_click": {"app": "Notes", "ref": "e1"}}, {"press_key": {"key": "space"}}]}`, "front"},
		{"page tool must not pollute last target", "run_steps", `{"steps": [{"page": {"app": "Notes"}}, {"press_key": {"key": "space"}}]}`, "front"},
		{"launch_app must not pollute last target", "run_steps", `{"steps": [{"launch_app": {"app": "Notes", "name": "Notes"}}, {"press_key": {"key": "space"}}]}`, "front"},
	})
}

func TestStepActionsInvalidToolDoesNotDropArgsKey(t *testing.T) {
	// When "tool" is empty/non-string, "args" was not consumed as tool args and must not be silently dropped.
	step := map[string]json.RawMessage{
		"tool": json.RawMessage(`""`),
		"args": json.RawMessage(`{"app": "System Settings"}`),
	}
	apps, _ := StepsApps("", mustMarshalSteps(t, step))
	if !slices.Contains(apps, "System Settings") {
		t.Fatalf("StepsApps with empty tool dropped args key: %v", apps)
	}
	// Whitespace-padded flat key should still recognize appTools.
	padded := map[string]json.RawMessage{
		" kill_app ": json.RawMessage(`{"bundle_id": "com.apple.systempreferences"}`),
	}
	apps, _ = StepsApps("", mustMarshalSteps(t, padded))
	if !slices.Contains(apps, "com.apple.systempreferences") {
		t.Fatalf("StepsApps missed whitespace-padded appTool key: %v", apps)
	}
}

func mustMarshalSteps(t *testing.T, steps ...map[string]json.RawMessage) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTokenCheckValidatesRowSuffixAndRayHandlesNonFiniteAndExtremeRect(t *testing.T) {
	m := behindEdge()
	runChecks(t, m, []checkCase{
		{"extra colon suffix fails closed", "click", `{"window_id": 130, "element_token": "s0000024a:3:extra"}`, "token"},
		{"empty row suffix fails closed", "click", `{"window_id": 130, "element_token": "s0000024a:"}`, "token"},
		{"nested snapshot token fails closed", "click", `{"window_id": 130, "element_token": "s0000024a:s00000099:3"}`, "token"},
	})

	notesRect := Rect{X: 460, Y: 195, Width: 1000, Height: 660}
	for _, p := range []point{
		{math.NaN(), 10},
		{10, math.NaN()},
		{math.Inf(1), 10},
		{10, math.Inf(-1)},
	} {
		from, to := ray(notesRect, p)
		for _, v := range []float64{from.x, from.y, to.x, to.y} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("ray(%+v, %+v) returned non-finite: from=%+v to=%+v", notesRect, p, from, to)
			}
		}
	}

	// Extreme synthetic aspect ratio: r.Height/ratio must stay <= r.Width and finite.
	wideRect := Rect{X: 0, Y: 0, Width: 1e200, Height: 1e-100}
	from, to := ray(wideRect, point{1, 1e-299})
	for _, v := range []float64{from.x, from.y, to.x, to.y} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("ray(wideRect) returned non-finite: from=%+v to=%+v", from, to)
		}
	}
	tallRect := Rect{X: 0, Y: 0, Width: 1e-100, Height: 1e200}
	from, to = ray(tallRect, point{1e-299, 1})
	for _, v := range []float64{from.x, from.y, to.x, to.y} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("ray(tallRect) returned non-finite: from=%+v to=%+v", from, to)
		}
	}
}
