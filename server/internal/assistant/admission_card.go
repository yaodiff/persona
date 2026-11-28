package assistant

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// What the approval card says when assistant.admission is replaced: one line per line of the gate that changes, the
// old value and the new one in words. The card is all the user reads before allowing the change, so it names what each
// number bounds and on which computer, shows a value that is not set as the line it falls back to, and is not clipped.
// The setting itself stays under the card's "Show input".

// admissionSide is one side of a changed line: whether the setting carries the line itself, the value in force, and
// the line a value the setting does not carry falls back to.
type admissionSide struct {
	set   bool
	exact string // the value as stored: what tells two sides apart
	value string // the value as the card writes it
	// from marks a value the setting does not carry with the line it falls back to; nil when the setting carries it
	from func(value string) string
}

func admissionOwn(value string) admissionSide {
	return admissionSide{set: true, exact: value, value: value}
}

// admissionOwnBytes is a memory line the setting carries. The card writes it in binary units, which round: two sizes
// that read the same are still two values, so the byte count is kept beside it.
func admissionOwnBytes(n int64) admissionSide {
	return admissionSide{set: true, exact: strconv.FormatInt(n, 10) + " B", value: humanBytes(n)}
}

// admissionBuiltIn is the number built into the gate, in force when the setting does not carry the line.
func admissionBuiltIn(l i18n.Lang, value string) admissionSide {
	return admissionSide{exact: value, value: value, from: func(value string) string {
		return i18n.T(l, "assistant-settings_tool.admission_default", i18n.Args{"value": value})
	}}
}

// admissionBuiltInBytes is admissionBuiltIn for a memory line; it keeps the byte count like admissionOwnBytes.
func admissionBuiltInBytes(l i18n.Lang, n int64) admissionSide {
	v := admissionBuiltIn(l, humanBytes(n))
	v.exact = strconv.FormatInt(n, 10) + " B"
	return v
}

// admissionAbsent is a direction, project or launcher group the setting does not name: no line, in the words given.
func admissionAbsent(words string) admissionSide { return admissionSide{exact: words, value: words} }

// asGeneral is v as what a computer's own line falls back to: the setting's line for every computer when it carries
// one, the built-in number otherwise.
func (v admissionSide) asGeneral(l i18n.Lang) admissionSide {
	if !v.set {
		return v
	}
	return admissionSide{exact: v.exact, value: v.value, from: func(value string) string {
		return i18n.T(l, "assistant-settings_tool.admission_general", i18n.Args{"value": value})
	}}
}

// asHub is asGeneral for the hub's computer, which falls back to the hub lines.
func (v admissionSide) asHub(l i18n.Lang) admissionSide {
	if !v.set {
		return v
	}
	return admissionSide{exact: v.exact, value: v.value, from: func(value string) string {
		return i18n.T(l, "assistant-settings_tool.admission_hub", i18n.Args{"value": value})
	}}
}

// asEveryDirection is v, the cap for every direction, as what a direction's or project's own cap falls back to.
func (v admissionSide) asEveryDirection(l i18n.Lang) admissionSide {
	if !v.set {
		return v
	}
	return admissionSide{exact: v.exact, value: v.value, from: func(value string) string {
		return i18n.T(l, "assistant-settings_tool.admission_direction_general", i18n.Args{"value": value})
	}}
}

// changed: the setting carries the line on one side only, or carries a different value. A line that is set on neither
// side is not listed even when the line it falls back to moves: that line is listed itself.
func (v admissionSide) changed(now admissionSide) bool {
	return v.set != now.set || v.set && v.exact != now.exact
}

// shown is how the card writes the side: the value, with the stored value beside it when precise is asked for and the
// two differ, marked with the line it falls back to when the setting does not carry it.
func (v admissionSide) shown(l i18n.Lang, precise bool) string {
	text := v.value
	if precise && v.exact != v.value {
		text = i18n.T(l, "assistant-settings_tool.admission_exact", i18n.Args{"value": v.value, "exact": v.exact})
	}
	if v.from != nil {
		return v.from(text)
	}
	return text
}

// admissionSidesShown is how the two sides of a changed line read. Two different values that would be written the same
// (memory sizes within one rounding step, on a line the setting carries or one it falls back to) are both written with
// their stored value, so a row never hides the number being approved behind the same text on both sides.
func admissionSidesShown(l i18n.Lang, was, now admissionSide) (before, after string) {
	precise := was.value == now.value && was.exact != now.exact
	return was.shown(l, precise), now.shown(l, precise)
}

func admissionBytesSide(v *int64, fallback admissionSide) admissionSide {
	if v != nil && *v > 0 {
		return admissionOwnBytes(*v)
	}
	return fallback
}

func admissionCountSide(v *int, fallback admissionSide) admissionSide {
	if v != nil && *v > 0 {
		return admissionOwn(strconv.Itoa(*v))
	}
	return fallback
}

func admissionLoadSide(v *float64, fallback admissionSide) admissionSide {
	if v != nil && *v > 0 {
		return admissionOwn(admissionLoadText(*v))
	}
	return fallback
}

func admissionLoadText(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// admissionSwapSide is the swap line; like percentOr, a value outside (0, 100] is not the setting's own.
func admissionSwapSide(v *float64, fallback admissionSide) admissionSide {
	if v != nil && *v > 0 && *v <= 100 {
		return admissionOwn(admissionSwapText(*v))
	}
	return fallback
}

func admissionSwapText(v float64) string { return admissionLoadText(v) + "%" }

// admissionName is a name from the setting as one row's label: on one line, so a line break inside a computer,
// direction, project, group or launcher name cannot start a row of its own on the card.
func admissionName(name string) i18n.Verbatim { return i18n.Verbatim(oneLine(name)) }

// admissionLines are the resolved lines of one setting that do not depend on a name.
type admissionLines struct {
	gate, load, direction  admissionSide
	swap                   admissionSide
	memory, working        admissionSide
	hubMemory, hubWorking  admissionSide
	machines               map[string]MachineAdmission
	byDirection, byProject map[string]int
	groups                 map[string]LauncherGroup // by trimmed name
	hubMachine             string
	lang                   i18n.Lang
}

func resolveAdmissionLines(l i18n.Lang, cfg AdmissionSettings, hubMachine string) admissionLines {
	builtIn := func(value string) admissionSide { return admissionBuiltIn(l, value) }
	v := admissionLines{
		gate:        builtIn(i18n.T(l, "assistant-settings_tool.on")),
		load:        admissionLoadSide(cfg.LoadPerCore, builtIn(admissionLoadText(admissionLoadPerCore))),
		swap:        admissionSwapSide(cfg.MaxSwapPercent, builtIn(admissionSwapText(admissionMaxSwapPercent))),
		direction:   admissionCountSide(cfg.DirectionCap, builtIn(strconv.Itoa(admissionDirectionCap))),
		memory:      admissionBytesSide(cfg.MinMemoryBytes, admissionBuiltInBytes(l, admissionMinMemoryBytes)),
		working:     admissionCountSide(cfg.MaxClaudeProcs, builtIn(strconv.Itoa(admissionMaxClaudeProcs))),
		hubMemory:   admissionBytesSide(cfg.HubMinMemoryBytes, admissionBuiltInBytes(l, admissionHubMinMemory)),
		hubWorking:  admissionCountSide(cfg.HubMaxClaudeProcs, builtIn(strconv.Itoa(admissionHubMaxClaudeProcs))),
		machines:    cfg.Machines,
		byDirection: cfg.DirectionCapByDirection,
		byProject:   cfg.DirectionCapByProject,
		groups:      map[string]LauncherGroup{},
		hubMachine:  hubMachine,
		lang:        l,
	}
	if cfg.Enabled != nil {
		v.gate = admissionOwn(i18n.T(l, "assistant-settings_tool.on"))
		if !*cfg.Enabled {
			v.gate = admissionOwn(i18n.T(l, "assistant-settings_tool.off"))
		}
	}
	for _, g := range cfg.LauncherGroups {
		v.groups[strings.TrimSpace(g.Name)] = g
	}
	return v
}

// machine is one computer's four lines. A line its entry does not set follows limitsFor: the hub's computer takes the
// hub lines for memory and working Agents, every other computer the general ones, and load and swap are general
// everywhere.
func (v admissionLines) machine(id string) (memory, working, load, swap admissionSide) {
	over := v.machines[id]
	memoryFrom, workingFrom := v.memory.asGeneral(v.lang), v.working.asGeneral(v.lang)
	if id == v.hubMachine && id != "" {
		memoryFrom, workingFrom = v.hubMemory.asHub(v.lang), v.hubWorking.asHub(v.lang)
	}
	return admissionBytesSide(over.MinMemoryBytes, memoryFrom), admissionCountSide(over.MaxClaudeProcs, workingFrom),
		admissionLoadSide(over.LoadPerCore, v.load.asGeneral(v.lang)),
		admissionSwapSide(over.MaxSwapPercent, v.swap.asGeneral(v.lang))
}

// projectCap is one project's own cap. A project the setting does not name takes the cap for every direction.
func (v admissionLines) projectCap(project string) admissionSide {
	if cap, ok := v.byProject[project]; ok && cap > 0 {
		return admissionOwn(strconv.Itoa(cap))
	}
	return v.direction.asEveryDirection(v.lang)
}

// directionCap is one direction's own cap. A direction the setting does not name takes its task's project cap when the
// setting carries any (directionCap), so only without project caps is there one number to show for it.
func (v admissionLines) directionCap(direction string) admissionSide {
	if cap, ok := v.byDirection[direction]; ok && cap > 0 {
		return admissionOwn(strconv.Itoa(cap))
	}
	if len(v.byProject) > 0 {
		return admissionAbsent(i18n.T(v.lang, "assistant-settings_tool.admission_own_none"))
	}
	return v.direction.asEveryDirection(v.lang)
}

// group is one launcher group's cap, members and quota pool; a group the setting does not name has none of them.
func (v admissionLines) group(name string) (cap, launchers, pool admissionSide) {
	none := admissionAbsent(i18n.T(v.lang, "assistant-settings_tool.none"))
	g, ok := v.groups[name]
	if !ok {
		return none, none, none
	}
	// Two lists are told apart as the setting stores them, quoted, white space included, while the card writes each
	// name on one line.
	members, written := make([]string, 0, len(g.Launchers)), make([]string, 0, len(g.Launchers))
	for _, launcher := range g.Launchers {
		members = append(members, oneLine(launcher))
		written = append(written, strconv.Quote(launcher))
	}
	cap = admissionOwn(strconv.Itoa(g.Cap))
	launchers = admissionOwn(strings.Join(members, ", "))
	launchers.exact = strings.Join(written, ", ")
	pool = none
	if g.QuotaPool != nil {
		pool = admissionOwn(i18n.T(v.lang, "assistant-settings_tool.admission_pool",
			i18n.Args{"source": i18n.Verbatim(g.QuotaPool.Source), "provider": g.QuotaPool.Provider}))
	}
	return cap, launchers, pool
}

// admissionChangeCard is the card's text in l for replacing before with after. hubMachine is the computer the hub lines
// apply to. Names are listed in sorted order: the card is rebuilt when it is answered and must read the same.
func admissionChangeCard(l i18n.Lang, before, after AdmissionSettings, hubMachine string) string {
	was, now := resolveAdmissionLines(l, before, hubMachine), resolveAdmissionLines(l, after, hubMachine)
	var lines []string
	line := func(what string, was, now admissionSide) {
		if was.changed(now) {
			before, after := admissionSidesShown(l, was, now)
			lines = append(lines, i18n.T(l, "assistant-settings_tool.admission_line",
				i18n.Args{"what": what, "before": before, "after": after}))
		}
	}
	both := func(a, b []string) []string {
		names := append(slices.Clone(a), b...)
		slices.Sort(names)
		return slices.Compact(names)
	}

	line(i18n.T(l, "assistant-settings_tool.admission_gate"), was.gate, now.gate)
	line(i18n.T(l, "assistant-settings_tool.admission_load"), was.load, now.load)
	line(i18n.T(l, "assistant-settings_tool.admission_swap"), was.swap, now.swap)
	line(i18n.T(l, "assistant-settings_tool.admission_memory"), was.memory, now.memory)
	line(i18n.T(l, "assistant-settings_tool.admission_working"), was.working, now.working)
	line(i18n.T(l, "assistant-settings_tool.admission_hub_memory"), was.hubMemory, now.hubMemory)
	line(i18n.T(l, "assistant-settings_tool.admission_hub_working"), was.hubWorking, now.hubWorking)
	for _, id := range both(slices.Collect(maps.Keys(was.machines)), slices.Collect(maps.Keys(now.machines))) {
		wasMemory, wasWorking, wasLoad, wasSwap := was.machine(id)
		nowMemory, nowWorking, nowLoad, nowSwap := now.machine(id)
		machine := i18n.Args{"machine": admissionName(id)}
		line(i18n.T(l, "assistant-settings_tool.admission_machine_memory", machine), wasMemory, nowMemory)
		line(i18n.T(l, "assistant-settings_tool.admission_machine_working", machine), wasWorking, nowWorking)
		line(i18n.T(l, "assistant-settings_tool.admission_machine_load", machine), wasLoad, nowLoad)
		line(i18n.T(l, "assistant-settings_tool.admission_machine_swap", machine), wasSwap, nowSwap)
	}
	line(i18n.T(l, "assistant-settings_tool.admission_direction"), was.direction, now.direction)
	for _, key := range both(slices.Collect(maps.Keys(was.byDirection)), slices.Collect(maps.Keys(now.byDirection))) {
		line(i18n.T(l, "assistant-settings_tool.admission_direction_named", i18n.Args{"name": admissionName(key)}),
			was.directionCap(key), now.directionCap(key))
	}
	for _, key := range both(slices.Collect(maps.Keys(was.byProject)), slices.Collect(maps.Keys(now.byProject))) {
		line(i18n.T(l, "assistant-settings_tool.admission_direction_project", i18n.Args{"name": admissionName(key)}),
			was.projectCap(key), now.projectCap(key))
	}
	for _, name := range both(slices.Collect(maps.Keys(was.groups)), slices.Collect(maps.Keys(now.groups))) {
		wasCap, wasLaunchers, wasPool := was.group(name)
		nowCap, nowLaunchers, nowPool := now.group(name)
		group := i18n.Args{"name": admissionName(name)}
		line(i18n.T(l, "assistant-settings_tool.admission_group_cap", group), wasCap, nowCap)
		line(i18n.T(l, "assistant-settings_tool.admission_group_launchers", group), wasLaunchers, nowLaunchers)
		line(i18n.T(l, "assistant-settings_tool.admission_group_pool", group), wasPool, nowPool)
	}
	if len(lines) == 0 {
		lines = append(lines, i18n.T(l, "assistant-settings_tool.admission_same_lines"))
	}
	return i18n.T(l, "assistant-settings_tool.change_admission", i18n.Args{"lines": i18n.Verbatim(strings.Join(lines, "\n"))})
}
