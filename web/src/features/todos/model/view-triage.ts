// The To-dos page's status sections drawn from the hub's view (#10226, #14583; docs/web/web.md "Developing Persona"),
// gated by view-gates.ts viewDrawOn (on by default when the hub lists `todo_view`, off with `persona.todos.viewDraw=0`).
// Nothing but the source of each row's section, rank and page counts changes: the sections, titles, folds and rows are
// today's, so what moves is what the redesign decided (view-legacy.ts). Proposal 7.3: a row with a view is placed by
// it, alone (related-row merging retires, 6.4); a row without one, or with one this web cannot read, keeps today's
// triage. Browser-local restores still keep a locally restored inactive row in Other open across snapshots.

import { TODO_LIST_SECTIONS, type TodoSummary } from "../../../lib/todo-view-wire";
import { ALL_TRIAGE_SECTIONS, countsAsOpen, SHELVE_AFTER_MS, TRIAGE_SECTIONS, triageGroupKey, type Triage, type TriageGroup, type TriageResult, type TriageRule, type TriageSection } from "../../../lib/todo-triage";
import { compareWaitAge } from "../../../lib/task-wait";
import type { Todo } from "../../../lib/todos";
import { knownView, legacyOf } from "./view-legacy";

const VIEW_ORDER = new Map<string, number>(TODO_LIST_SECTIONS.map((s, i) => [s, i]));

/** The rule of today's that stands for the view's reason, for a drawn row's triage: the page reads only its section
 *  (the counts come from the view, openAsDrawn), but a rule says which of today's the view stands for. */
function ruleOf(view: TodoSummary, section: TriageSection): TriageRule {
  const same = SAME_RULE.get(view.reason);
  if (same) return same;
  if (view.reason === "owner_decides" || view.reason === "can_wait") return "owner_decides";
  if (section === "just_done" || section === "done") {
    if (view.phase === "done") return "closed";
    const settled = SAME_RULE.get(view.line?.key ?? "");
    if (settled === "accepted") return settled;
    return section === "done" ? "seen" : "finished_unseen";
  }
  return BY_SECTION[section] ?? "rest";
}

/** Reasons (and finished lines) named as today's rules are. */
const SAME_RULE = new Map<string, TriageRule>([
  ["failed_start", "failed_start"],
  ["superseded", "superseded"],
  ["pr_open", "pr_open"],
  ["parked", "parked"],
  ["accepted", "accepted"],
  ["canceled", "cancelled"],
]);
const BY_SECTION: Partial<Record<TriageSection, TriageRule>> = { waiting: "agent_asks", working: "agent_running", parked: "parked", shelved: "stale" };

/** Whether a row counts as open where it is drawn: from its view when it has one (D3: every to-do not closed, Someday
 *  apart; a To do with the Inactive mark is drawn in Shelved and counted there, as the header counts Shelved apart),
 *  else as today (countsAsOpen). */
export function openAsDrawn(todo: Todo, t: Triage): boolean {
  if (!knownView(todo.view)) return countsAsOpen(todo, t);
  return todo.view.phase !== "done" && todo.view.section !== "someday" && t.section !== "shelved";
}

function ms(at: string | null | undefined): number {
  const v = at ? Date.parse(at) : NaN;
  return Number.isNaN(v) ? 0 : v;
}

/** `local` (today's triage of the same to-dos, its order and Parked's wait order applied) with every row that has a view
 *  moved to the view's place: its own group, ahead of the rows without one in that section, by the view's section,
 *  then `rank`, then the manager and the id (#10226 D12). What is left of a merged row keeps today's order in the
 *  section its new lead takes. Browser-local restores (`restored`) keep a locally restored inactive row in Other open
 *  even when subsequent hub snapshots still carry `inactive`. The counts follow the rows. */
export function drawnFromView(local: TriageResult, now: number, restored?: ReadonlyMap<string, number>): TriageResult {
  const viewed = (m: Todo) => knownView(m.view);
  if (!TRIAGE_SECTIONS.some((s) => local.sections[s].some((g) => g.members.some(viewed)))) return local;
  const fromView: { group: TriageGroup; view: TodoSummary }[] = [];
  const kept = Object.fromEntries(TRIAGE_SECTIONS.map((s) => [s, [] as TriageGroup[]])) as Record<TriageSection, TriageGroup[]>;
  const moved: TriageGroup[] = [];
  for (const s of TRIAGE_SECTIONS) {
    for (const g of local.sections[s]) {
      const rest = g.members.filter((m) => !viewed(m));
      for (const m of g.members) {
        if (!knownView(m.view)) continue;
        const before = g.triage.get(m.id)!;
        let section = legacyOf(m.view, before.rule === "closed" && before.section === "just_done")!;
        const localRestored = restored?.get(m.id) ?? 0;
        if (section === "shelved" && localRestored > ms(m.restored_at) && now - localRestored < SHELVE_AFTER_MS) section = "other";
        const t: Triage = { section, rule: ruleOf(m.view, section), last: before.last };
        fromView.push({ view: m.view, group: { key: m.id, section, lead: m, members: [m], triage: new Map([[m.id, t]]), last: t.last } });
      }
      if (rest.length === g.members.length) kept[s].push(g);
      else if (rest.length) {
        // what is left of a merged row: led by its most urgent member, as triageTodos leads it
        const lead = rest[0];
        const section = g.triage.get(lead.id)!.section;
        const left = { key: triageGroupKey(rest), section, lead, members: rest, triage: new Map(rest.map((m) => [m.id, g.triage.get(m.id)!])), last: Math.max(...rest.map((m) => g.triage.get(m.id)!.last)) };
        if (section === s) kept[s].push(left);
        else moved.push(left);
      }
    }
  }
  // a row that changed section goes where today's order puts it there: Parked by its wait, the rest newest first
  for (const g of moved) {
    const rows = kept[g.section];
    const after = (o: TriageGroup) => (g.section === "parked" ? compareWaitAge(g.lead, o.lead, now) < 0 : o.last - g.last < 0 || (o.last === g.last && g.key < o.key));
    const i = rows.findIndex(after);
    rows.splice(i < 0 ? rows.length : i, 0, g);
  }
  fromView.sort(
    (a, b) =>
      (VIEW_ORDER.get(a.view.section) ?? 0) - (VIEW_ORDER.get(b.view.section) ?? 0) ||
      a.view.rank - b.view.rank ||
      (a.group.lead.manager ?? "").localeCompare(b.group.lead.manager ?? "") ||
      a.group.key.localeCompare(b.group.key),
  );
  const sections = Object.fromEntries(ALL_TRIAGE_SECTIONS.map((s) => [s, [] as TriageGroup[]])) as Record<TriageSection, TriageGroup[]>;
  for (const { group } of fromView) sections[group.section].push(group);
  for (const s of TRIAGE_SECTIONS) sections[s].push(...kept[s]);
  const todoCounts = Object.fromEntries(ALL_TRIAGE_SECTIONS.map((s) => [s, 0])) as Record<TriageSection, number>;
  let open = 0;
  let finished = 0;
  for (const s of TRIAGE_SECTIONS)
    for (const g of sections[s])
      for (const m of g.members) {
        const t = g.triage.get(m.id)!;
        todoCounts[t.section]++;
        if (openAsDrawn(m, t)) open++;
        // closed or accepted as today; from a view, closed (Someday is neither open nor finished)
        else if (t.section !== "cancelled" && (knownView(m.view) ? m.view.phase === "done" : t.section !== "shelved")) finished++;
      }
  return { sections, todoCounts, open, shelved: todoCounts.shelved, finished, cancelled: todoCounts.cancelled };
}
