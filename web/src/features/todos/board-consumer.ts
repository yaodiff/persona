// Thin client consumer for the server-owned `board` projection tree under todos_v2 (#15800).
// Consumes `board.status_groups`, `board.direction_groups`, and `board.counts`
// directly without re-running client-side triage, goal folding, or direction derivation rules.

import type { Direction } from "../../lib/direction-tree";
import type { GoalChildEntry, GoalFoldResult, GoalSummary } from "../../lib/todo-goal-fold";
import type { ResolvedDirectionSection } from "../../lib/todo-reorganize";
import { ALL_TRIAGE_SECTIONS, viewWaitsOnOwner, type OwnerNeed, type TriageGroup, type TriageResult, type TriageRule, type TriageSection } from "../../lib/todo-triage";
import {
  isCancelled, isClosedForHistory,
  type Todo,
  type TodoBoardProjection,
  type TodoBoardSectionKey,
} from "../../lib/todos";

/** Maps a server `board.status_groups[*].key` to the Web To-dos section identifier. */
export function boardSectionToTriageSection(key: TodoBoardSectionKey): TriageSection {
  switch (key) {
    case "can_wait": return "can_wait";
    case "todo": return "todo";
    case "cancelled": return "cancelled";
    case "needs_you":
      return "waiting";
    case "in_progress":
      return "working";
    case "pending_review":
      return "just_done";
    case "waiting":
      return "parked";
    case "someday":
      return "shelved";
    case "done":
      return "done";
    default:
      return "other";
  }
}

function boardSectionDefaultRule(key: TodoBoardSectionKey): TriageRule {
  switch (key) {
    case "needs_you":
      return "owner_decides";
    case "in_progress":
      return "agent_running";
    case "pending_review":
      return "finished_unseen";
    case "waiting":
      return "parked";
    case "someday":
      return "stale";
    case "done":
      return "closed";
    case "cancelled":
      return "cancelled";
    default:
      return "rest";
  }
}

function todoTimestamp(todo: Todo): number {
  const ts = Date.parse(todo.updated_at || todo.created_at || "");
  return Number.isNaN(ts) ? 0 : ts;
}

function statusRow(todo: Todo, section: TriageSection, rule: TriageRule): TriageGroup {
  const last = todoTimestamp(todo);
  return {
    key: todo.id, section, lead: todo, members: [todo],
    triage: new Map([[todo.id, { section, rule, last }]]), last,
  };
}

function closedSection(todo: Todo): "done" | "cancelled" {
  return isCancelled(todo) || todo.view?.reason === "canceled" ? "cancelled" : "done";
}

function foldedSection(todo: Todo | undefined, parent: TriageSection): TriageSection {
  if (!todo) return parent;
  if (isClosedForHistory(todo) || todo.view?.phase === "done" || todo.view?.section === "done" || closedSection(todo) === "cancelled") return closedSection(todo);
  switch (todo.view?.section) {
    case "working": return "working";
    case "just_finished": return "just_done";
    case "can_wait": return "can_wait";
    case "waiting": return "parked";
    case "todo": return "todo";
    case "someday": return "shelved";
    default: return parent;
  }
}

export function isBoardOpen(section: TriageSection): boolean {
  return section !== "done" && section !== "cancelled" && section !== "shelved";
}

/** Builds a lookup map of `todo_id -> TriageSection` directly from `board.status_groups`. */
export function boardSectionMap(board: TodoBoardProjection, byId: ReadonlyMap<string, Todo>, olderClosed: readonly Todo[] = []): Map<string, TriageSection> {
  const map = new Map<string, TriageSection>();
  // First pass: every top-level entry (including surfaced blocked children) gets its exact server section.
  for (const group of board.status_groups) {
    const sec = boardSectionToTriageSection(group.key);
    for (const entry of group.entries) if (!map.has(entry.todo_id)) map.set(entry.todo_id, sec);
  }
  // Second pass: folded children not listed at top level inherit done if closed, else their parent's section.
  for (const group of board.status_groups) {
    const parentSec = boardSectionToTriageSection(group.key);
    for (const entry of group.entries) {
      for (const cid of entry.folded_child_ids ?? []) {
        if (map.has(cid)) continue;
        map.set(cid, foldedSection(byId.get(cid), parentSec));
      }
    }
  }
  for (const todo of olderClosed) if (!map.has(todo.id)) map.set(todo.id, closedSection(todo));
  return map;
}

/** Row affordances for immediate attention, in hub order. The aggregate count comes from board.counts. */
export function boardAttention(board: TodoBoardProjection, byId: ReadonlyMap<string, Todo>): ReadonlyMap<string, OwnerNeed> {
  const attention = new Map<string, OwnerNeed>();
  for (const group of board.status_groups) {
    if (group.key !== "needs_you") continue;
    for (const { todo_id } of group.entries) {
      const todo = byId.get(todo_id);
      if (!todo || todo.quiet || (todo.view && todo.view.section !== "needs_you")) continue;
      attention.set(todo_id, viewWaitsOnOwner(todo) || "owner_decides");
    }
  }
  return attention;
}

export interface BoardStatusConsumption {
  /** GoalFoldResult-compatible projection consumed directly by `Todos.tsx`. */
  goalFold: GoalFoldResult;
  /** Exact status-section display order from `board.status_groups`. */
  sectionOrder: TriageSection[];
  /** Displayed count per section (server `visible_count` when unfiltered, filtered row count otherwise). */
  sectionCounts: Record<TriageSection, number>;
}

/**
 * Thinly adapts server `board.status_groups` and `board.counts` for `Todos.tsx` under `todos_v2 === true`.
 * Preserves server section ordering, entry ordering, top-level goal folding (`folded_child_ids` / `folded_count`),
 * and surfaced blocked children (`surfaced_child_ids`) without re-running client triage or folding heuristics.
 */
export function consumeBoardStatusGroups(
  todos: readonly Todo[],
  board: TodoBoardProjection,
  matchesFilter?: (todo: Todo) => boolean,
  olderClosed: readonly Todo[] = [],
  sectionByTodo?: ReadonlyMap<string, TriageSection>,
): BoardStatusConsumption {
  const byId = new Map(todos.map((td) => [td.id, td]));
  const placement = sectionByTodo ?? boardSectionMap(board, byId, olderClosed);
  const sections = Object.fromEntries(ALL_TRIAGE_SECTIONS.map((s) => [s, [] as TriageGroup[]])) as Record<TriageSection, TriageGroup[]>;
  const sectionOrder: TriageSection[] = [];
  const goals = new Map<string, GoalSummary>();
  const surfacedParent = new Map<string, Todo>();
  const sectionCounts = Object.fromEntries(ALL_TRIAGE_SECTIONS.map((s) => [s, 0])) as Record<TriageSection, number>;
  const seen = new Set<string>();

  for (const group of board.status_groups) {
    const sec = boardSectionToTriageSection(group.key);
    const rule = boardSectionDefaultRule(group.key);
    if (!sectionOrder.includes(sec)) sectionOrder.push(sec);

    for (const entry of group.entries) {
      const todo = byId.get(entry.todo_id);
      if (!todo || seen.has(todo.id)) continue;
      seen.add(todo.id);

      const rawFolded = (entry.folded_child_ids ?? [])
        .map((cid) => byId.get(cid))
        .filter((td): td is Todo => Boolean(td));
      const rawSurfaced = (entry.surfaced_child_ids ?? [])
        .map((cid) => byId.get(cid))
        .filter((td): td is Todo => Boolean(td));

      const folded = matchesFilter ? rawFolded.filter(matchesFilter) : rawFolded;
      const surfaced = matchesFilter ? rawSurfaced.filter(matchesFilter) : rawSurfaced;
      const parentMatches = !matchesFilter || matchesFilter(todo);

      if (!parentMatches) {
        // When a local UI filter (e.g. search) matches a folded child but not its parent, keep the child visible.
        for (const child of folded) {
          const childSec = placement.get(child.id) ?? sec;
          sections[childSec].push(statusRow(child, childSec, rule));
        }
        continue;
      }

      sections[sec].push(statusRow(todo, sec, rule));

      for (const sc of surfaced) {
        surfacedParent.set(sc.id, todo);
      }

      const serverFoldedCount = entry.folded_count ?? 0;
      if (folded.length > 0 || surfaced.length > 0 || (!matchesFilter && serverFoldedCount > 0)) {
        const child = (todo: Todo, blocker: boolean): GoalChildEntry => {
          const section = placement.get(todo.id) ?? (blocker ? "waiting" : sec);
          return { todo, section, done: section === "done" || section === "cancelled", blocker };
        };
        const children = [...surfaced.map((c) => child(c, true)), ...folded.map((c) => child(c, false))];
        const done = children.reduce((n, c) => n + (c.done ? 1 : 0), 0);
        const total = matchesFilter
          ? children.length
          : Math.max(children.length, serverFoldedCount + surfaced.length);
        goals.set(todo.id, {
          parentId: todo.id,
          done,
          total,
          blockedCount: surfaced.length,
          children,
          blockers: children.filter((c) => c.blocker),
        });
      }
    }

    sectionCounts[sec] += group.visible_count;
  }

  // Cursor pages extend the closed sections even when their rows are absent from the current board snapshot.
  for (const todo of olderClosed) {
    if (seen.has(todo.id) || byId.has(todo.id) || (matchesFilter && !matchesFilter(todo))) continue;
    seen.add(todo.id);
    const section = closedSection(todo);
    sections[section].push(statusRow(todo, section, section === "cancelled" ? "cancelled" : "closed"));
  }
  for (const sec of ALL_TRIAGE_SECTIONS) {
    if (matchesFilter) sectionCounts[sec] = sections[sec].length;
    else if (sec === "done" || sec === "cancelled") sectionCounts[sec] = Math.max(sectionCounts[sec], sections[sec].length);
    if (!sectionOrder.includes(sec)) sectionOrder.push(sec);
  }

  const triaged: TriageResult = {
    sections,
    open: matchesFilter ? sectionOrder.reduce((count, sec) => count + (isBoardOpen(sec) ? sectionCounts[sec] : 0), 0) : board.counts.open_total,
    shelved: matchesFilter ? sectionCounts.shelved : board.counts.someday,
    finished: matchesFilter ? sectionCounts.done : board.counts.done,
    cancelled: matchesFilter ? sectionCounts.cancelled : board.counts.cancelled ?? sectionCounts.cancelled,
    todoCounts: sectionCounts,
  };

  return {
    goalFold: {
      triaged,
      goals,
      surfacedParent,
    },
    sectionOrder,
    sectionCounts,
  };
}

/**
 * Thinly adapts server `board.direction_groups` for `Todos.tsx` By-direction view under `todos_v2 === true`.
 * Preserves server group order, member order, and precomputed direction counts without client re-grouping.
 */
export function consumeBoardDirectionGroups({
  todos,
  board,
  matchesFilter,
  includeDone = false,
  resolveFallbackName,
  ungroupedLabel,
}: {
  todos: readonly Todo[];
  board: TodoBoardProjection;
  matchesFilter?: (todo: Todo) => boolean;
  includeDone?: boolean;
  resolveFallbackName?: (id: string) => string | undefined;
  ungroupedLabel: string;
}): ResolvedDirectionSection[] {
  const byId = new Map<string, Todo>();
  for (const td of todos) byId.set(td.id, td);

  const sectionByTodo = boardSectionMap(board, byId);
  const out: ResolvedDirectionSection[] = [];

  for (const group of board.direction_groups) {
    const members: Todo[] = [];
    for (const id of group.todo_ids) {
      const td = byId.get(id);
      if (!td) continue;
      const section = sectionByTodo.get(id);
      if (!includeDone && (section === "done" || section === "cancelled")) continue;
      if (matchesFilter && !matchesFilter(td)) continue;
      members.push(td);
    }
    if (members.length === 0) continue;

    const isUngrouped = !group.direction_id;
    const title = isUngrouped
      ? (group.label?.trim() || ungroupedLabel)
      : (group.label?.trim() || resolveFallbackName?.(group.direction_id)?.trim() || group.direction_id);

    const d: Direction = {
      id: isUngrouped ? "__ungrouped__" : group.direction_id,
      name: title,
      ...(isUngrouped ? { secretary: true } : {}),
      counts: {
        working: group.counts.in_progress,
        needs_you: group.counts.needs_you,
        stuck: 0,
        review: group.counts.pending_review,
      },
      running: group.counts.in_progress > 0,
    };

    out.push({ d, todos: members, title });
  }

  return out;
}
