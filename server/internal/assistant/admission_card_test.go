package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func admissionCardFor(t *testing.T, l i18n.Lang, before, after, hubMachine string) []string {
	t.Helper()
	var was, now AdmissionSettings
	if err := json.Unmarshal([]byte(before), &was); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(after), &now); err != nil {
		t.Fatal(err)
	}
	return strings.Split(admissionChangeCard(l, was, now, hubMachine), "\n")
}

// Each line of the gate that changes is one line of the card, old value to new value; a side the setting does not
// carry reads as the line it falls back to, and a line that keeps its value is left out.
func TestAdmissionChangeCardLines(t *testing.T) {
	const title = "Execution Agent admission gate:"
	for _, c := range []struct {
		name, before, after string
		want                []string
	}{
		{"a computer's lines, from the built-in ones",
			`{}`, `{"machines":{"laptop":{"max_claude_procs":2,"min_memory_bytes":4294967296}}}`,
			[]string{
				"- Free memory an Agent’s start needs, laptop: 2.0 GiB (default) → 4.0 GiB",
				"- Agents working at once, laptop: 20 (default) → 2",
			}},
		{"a computer's line falls back to the general line the setting carries",
			`{"max_claude_procs":10,"machines":{"laptop":{"max_claude_procs":2,"load_per_core":1.5}}}`,
			`{"max_claude_procs":10,"machines":{"laptop":{"load_per_core":1.5}}}`,
			[]string{"- Agents working at once, laptop: 2 → 10 (the line for every computer)"}},
		{"the hub's computer falls back to the hub lines",
			`{"hub_max_claude_procs":6,"machines":{"hub":{"max_claude_procs":3,"min_memory_bytes":1073741824}}}`,
			`{"hub_max_claude_procs":6}`,
			[]string{
				"- Free memory an Agent’s start needs, hub: 1.0 GiB → 2.5 GiB (default)",
				"- Agents working at once, hub: 3 → 6 (the hub computer’s line)",
			}},
		{"the general lines, the hub lines and the switch",
			`{"load_per_core":6,"hub_min_memory_bytes":3221225472}`,
			`{"enabled":false,"min_memory_bytes":1073741824,"max_claude_procs":12,"hub_max_claude_procs":4}`,
			[]string{
				"- The gate: On (default) → Off",
				"- Load per core, every computer: 6 → 4 (default)",
				"- Free memory an Agent’s start needs, every computer: 2.0 GiB (default) → 1.0 GiB",
				"- Agents working at once, every computer: 20 (default) → 12",
				"- Free memory an Agent’s start needs, the hub’s computer: 3.0 GiB → 2.5 GiB (default)",
				"- Agents working at once, the hub’s computer: 20 (default) → 4",
			}},
		{"direction caps: general, by direction and by project, names in order",
			`{"direction_cap_by_direction":{"dir-9":2,"alpha":5},"direction_cap_by_project":{"docs":1}}`,
			`{"direction_cap":3,"direction_cap_by_direction":{"dir-9":4,"alpha":5,"beta":1}}`,
			[]string{
				"- Agents running in one direction: 8 (default) → 3",
				// beta's tasks may sit in a project with a cap of its own, so the side without beta names no number
				"- Agents running, direction beta: no line of its own → 1",
				"- Agents running, direction dir-9: 2 → 4",
				"- Agents running, project docs: 1 → 3 (the cap for every direction)",
			}},
		{"a direction's own cap, from and back to the cap for every direction",
			`{"direction_cap":3,"direction_cap_by_direction":{"old":2}}`,
			`{"direction_cap":3,"direction_cap_by_direction":{"new":5}}`,
			[]string{
				"- Agents running, direction new: 3 (the cap for every direction) → 5",
				"- Agents running, direction old: 2 → 3 (the cap for every direction)",
			}},
		{"a direction's and a project's own cap, from the built-in cap",
			`{}`, `{"direction_cap_by_direction":{"dir-9":2},"direction_cap_by_project":{"docs":1}}`,
			[]string{
				"- Agents running, direction dir-9: 8 (default) → 2",
				"- Agents running, project docs: 8 (default) → 1",
			}},
		{"two memory lines that round to the same size are told apart by their bytes",
			`{"min_memory_bytes":4294967296,"machines":{"laptop":{"min_memory_bytes":1073741824}}}`,
			`{"min_memory_bytes":4296015872,"machines":{"laptop":{"min_memory_bytes":1073741825}}}`,
			[]string{
				"- Free memory an Agent’s start needs, every computer: 4.0 GiB (4294967296 B) → 4.0 GiB (4296015872 B)",
				"- Free memory an Agent’s start needs, laptop: 1.0 GiB (1073741824 B) → 1.0 GiB (1073741825 B)",
			}},
		{"launcher groups: a new one, a changed one and a removed one",
			`{"launcher_groups":[{"name":"night","launchers":["codex"],"cap":1},` +
				`{"name":"shared","launchers":["claude","claude-team"],"cap":3}]}`,
			`{"launcher_groups":[{"name":"shared","launchers":["claude","claude-team","claude-night"],"cap":2,` +
				`"quota_pool":{"source":"team-pool","provider":"claude"}},{"name":"fresh","launchers":["gemini"],"cap":4}]}`,
			[]string{
				"- Tasks running at once, launcher group fresh: (none) → 4",
				"- Launchers, launcher group fresh: (none) → gemini",
				"- Tasks running at once, launcher group night: 1 → (none)",
				"- Launchers, launcher group night: codex → (none)",
				"- Tasks running at once, launcher group shared: 3 → 2",
				"- Launchers, launcher group shared: claude, claude-team → claude, claude-team, claude-night",
				"- Quota pool, launcher group shared: (none) → team-pool (claude)",
			}},
		{"two launcher lists that read the same on one line are told apart as written",
			`{"launcher_groups":[{"name":"shared","launchers":["claude  team"],"cap":2}]}`,
			`{"launcher_groups":[{"name":"shared","launchers":["claude team"],"cap":2}]}`,
			[]string{`- Launchers, launcher group shared: claude team ("claude  team") → claude team ("claude team")`}},
		{"a launcher list that differs only in white space around a name is told apart as written",
			`{"launcher_groups":[{"name":"shared","launchers":["claude"],"cap":2}]}`,
			`{"launcher_groups":[{"name":"shared","launchers":[" claude"],"cap":2}]}`,
			[]string{`- Launchers, launcher group shared: claude ("claude") → claude (" claude")`}},
		{"a memory line that rounds to the size it falls back from shows both byte counts",
			`{}`, `{"min_memory_bytes":2147483649}`,
			[]string{"- Free memory an Agent’s start needs, every computer: 2.0 GiB (2147483648 B) (default) → 2.0 GiB (2147483649 B)"}},
		{"a computer's memory line that rounds to the line it falls back to shows both byte counts",
			`{"min_memory_bytes":1073741824,"machines":{"laptop":{"min_memory_bytes":1073741825,"max_claude_procs":2}}}`,
			`{"min_memory_bytes":1073741824,"machines":{"laptop":{"max_claude_procs":2}}}`,
			[]string{"- Free memory an Agent’s start needs, laptop: 1.0 GiB (1073741825 B) → 1.0 GiB (1073741824 B) (the line for every computer)"}},
		{"a memory line written out at its built-in size shows no byte counts",
			`{}`, `{"hub_min_memory_bytes":2684354560}`,
			[]string{"- Free memory an Agent’s start needs, the hub’s computer: 2.5 GiB (default) → 2.5 GiB"}},
		{"a line written out at its built-in value still reads as a change of where it comes from",
			`{}`, `{"max_claude_procs":20}`,
			[]string{"- Agents working at once, every computer: 20 (default) → 20"}},
		{"only the way the setting is written changes",
			`{}`, `{"machines":{"laptop":{}}}`,
			[]string{"- No line changes its value; only the way the setting is written does"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := admissionCardFor(t, i18n.En, c.before, c.after, "hub")
			if got[0] != title {
				t.Errorf("first line %q, want %q", got[0], title)
			}
			if strings.Join(got[1:], "\n") != strings.Join(c.want, "\n") {
				t.Errorf("card lines:\n%s\nwant:\n%s", strings.Join(got[1:], "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

// A name is one row's label and nothing more: a line break inside a computer, direction, project, group or launcher
// name cannot start a row of its own on the card the user approves.
func TestAdmissionChangeCardKeepsNamesOnTheirRow(t *testing.T) {
	forged := `laptop\n- The gate: On → Off`
	after := `{"machines":{"` + forged + `":{"max_claude_procs":2}},"direction_cap_by_direction":{"` + forged + `":2},` +
		`"direction_cap_by_project":{"` + forged + `":2},"launcher_groups":[{"name":"` + forged + `","launchers":["` + forged + `"],"cap":1}]}`
	lines := admissionCardFor(t, i18n.En, `{}`, after, "")
	// the title, one computer row, one direction row, one project row, and the group's cap and launchers
	if len(lines) != 6 {
		t.Fatalf("%d lines, want 6:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, line := range lines[1:] {
		if !strings.Contains(line, "laptop") || strings.HasPrefix(line, "- The gate") {
			t.Errorf("a name started a row of its own: %q", line)
		}
	}
}

// The card is built again when it is answered and compared with what was shown, so the same change must always read
// the same, whatever order the maps are walked in.
func TestAdmissionChangeCardReadsTheSameEveryTime(t *testing.T) {
	after := `{"machines":{"m5":{"max_claude_procs":1},"m1":{"max_claude_procs":1},"m3":{"max_claude_procs":1},` +
		`"m2":{"max_claude_procs":1},"m4":{"max_claude_procs":1}},"direction_cap_by_direction":{"d3":1,"d1":1,"d2":1},` +
		`"direction_cap_by_project":{"p2":1,"p1":1,"p3":1}}`
	first := strings.Join(admissionCardFor(t, i18n.En, `{}`, after, ""), "\n")
	for range 50 {
		if again := strings.Join(admissionCardFor(t, i18n.En, `{}`, after, ""), "\n"); again != first {
			t.Fatalf("the card changed between two builds:\n%s\n---\n%s", first, again)
		}
	}
	if m1, m5 := strings.Index(first, "m1"), strings.Index(first, "m5"); m1 < 0 || m5 < m1 {
		t.Fatalf("computers are not listed in order:\n%s", first)
	}
}

// Every language says the lines in its own words and leaves no placeholder or field name behind.
func TestAdmissionChangeCardInEveryLanguage(t *testing.T) {
	before := `{"machines":{"old":{"load_per_core":2}},"direction_cap_by_project":{"docs":1},` +
		`"launcher_groups":[{"name":"night","launchers":["codex"],"cap":1}]}`
	after := `{"enabled":false,"load_per_core":6,"min_memory_bytes":1073741824,"max_claude_procs":12,` +
		`"hub_min_memory_bytes":3221225472,"hub_max_claude_procs":4,"direction_cap":3,` +
		`"direction_cap_by_direction":{"dir-9":4},"machines":{"hub":{"max_claude_procs":3},"laptop":{"min_memory_bytes":4294967296}},` +
		`"launcher_groups":[{"name":"shared","launchers":["claude"],"cap":2,"quota_pool":{"source":"team-pool","provider":"claude"}}]}`
	want := len(admissionCardFor(t, i18n.En, before, after, "hub"))
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		lines := admissionCardFor(t, l, before, after, "hub")
		if len(lines) != want || want < 15 {
			t.Errorf("%s: %d lines, want %d (at least 15)", l, len(lines), want)
		}
		for _, line := range lines {
			if strings.ContainsAny(line, "{}") || strings.Contains(line, "assistant-settings_tool.") || strings.Contains(line, "_") {
				t.Errorf("%s: unfinished line %q", l, line)
			}
		}
	}
}
