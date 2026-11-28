// Effective direction resolution for Web To-dos under todos_v2 (#16939).
// Resolves the displayed direction consistently across list rows and TodoDetail,
// preferring explicit `direction_id`, then server `effective_direction_id`/`effective_direction_name`,
// then `board.direction_groups`, then older-hub fallbacks (supervisor -> origin -> cycle-safe `part_of` parent chain).

import type { Todo, TodoBoardProjection } from "./todos";

export interface EffectiveDirection {
  id: string;
  label: string;
  /** False when explicitly written on `todo.direction_id`; true when resolved via server projection, supervision, origin, or parent chain. */
  inherited: boolean;
}

export interface EffectiveDirectionContext {
  /** Loaded to-dos indexed by ID for cycle-safe `part_of` parent inheritance fallback. */
  todosById?: ReadonlyMap<string, Todo>;
  /** Server-owned board projection (`board.direction_groups`), when available. */
  board?: TodoBoardProjection | null;
  /** Known non-secretary direction IDs used to validate supervisor/origin fallback on older hubs. */
  knownDirectionIds?: ReadonlySet<string>;
  /** Label lookup for a direction ID (from direction tree, cached names, or conversation titles). */
  directionLabel?: (id: string) => string | undefined;
}

interface BoardDirectionEntry {
  id: string;
  label?: string;
}

function buildBoardIndex(board: TodoBoardProjection | null | undefined): {
  byTodo: Map<string, BoardDirectionEntry>;
  labelById: Map<string, string>;
} {
  const byTodo = new Map<string, BoardDirectionEntry>();
  const labelById = new Map<string, string>();
  if (!board?.direction_groups?.length) return { byTodo, labelById };

  for (const group of board.direction_groups) {
    const dirId = group.direction_id?.trim() ?? "";
    if (!dirId) continue;
    const label = group.label?.trim() || undefined;
    if (label && !labelById.has(dirId)) {
      labelById.set(dirId, label);
    }
    for (const todoId of group.todo_ids ?? []) {
      if (!todoId || byTodo.has(todoId)) continue;
      byTodo.set(todoId, { id: dirId, label });
    }
  }
  return { byTodo, labelById };
}

function resolveDirect(
  todo: Todo,
  byTodo: ReadonlyMap<string, BoardDirectionEntry>,
  labelById: ReadonlyMap<string, string>,
  knownDirectionIds: ReadonlySet<string> | undefined,
  directionLabel: ((id: string) => string | undefined) | undefined,
): EffectiveDirection | undefined {
  // 1. Explicit `todo.direction_id` (not inherited).
  const explicitId = todo.direction_id?.trim() ?? "";
  if (explicitId) {
    const serverEffId = todo.effective_direction_id?.trim() ?? "";
    const serverNameMatches = !serverEffId || serverEffId === explicitId;
    const label =
      directionLabel?.(explicitId)?.trim() ||
      (serverNameMatches ? todo.effective_direction_name?.trim() : undefined) ||
      labelById.get(explicitId) ||
      explicitId;
    return { id: explicitId, label, inherited: false };
  }

  // 2. Server-resolved `todo.effective_direction_id` / `effective_direction_name`.
  const serverEffId = todo.effective_direction_id?.trim() ?? "";
  if (serverEffId) {
    const label =
      todo.effective_direction_name?.trim() ||
      directionLabel?.(serverEffId)?.trim() ||
      labelById.get(serverEffId) ||
      serverEffId;
    return { id: serverEffId, label, inherited: true };
  }

  // 3. Server `board.direction_groups` entry containing `todo.id` (with non-empty `group.direction_id`).
  const fromBoard = byTodo.get(todo.id);
  if (fromBoard) {
    const label = fromBoard.label || directionLabel?.(fromBoard.id)?.trim() || fromBoard.id;
    return { id: fromBoard.id, label, inherited: true };
  }

  // 4a. Older-hub fallback: check `supervision.supervisor`, then `origin`, against known direction IDs.
  if (knownDirectionIds && knownDirectionIds.size > 0) {
    const supervisor = todo.supervision?.supervisor?.trim() ?? "";
    if (supervisor && knownDirectionIds.has(supervisor)) {
      const label = directionLabel?.(supervisor)?.trim() || labelById.get(supervisor) || supervisor;
      return { id: supervisor, label, inherited: true };
    }
    const origin = todo.origin?.trim() ?? "";
    if (origin && knownDirectionIds.has(origin)) {
      const label = directionLabel?.(origin)?.trim() || labelById.get(origin) || origin;
      return { id: origin, label, inherited: true };
    }
  }

  return undefined;
}

/**
 * Builds a pre-indexed effective direction resolver for repeated lookups across a to-do list.
 * Strictly intended for `todos_v2 === true` rendering paths.
 */
export function createEffectiveDirectionResolver(
  ctx: EffectiveDirectionContext = {},
): (todo: Todo) => EffectiveDirection | undefined {
  const { byTodo, labelById } = buildBoardIndex(ctx.board);
  const { todosById, knownDirectionIds, directionLabel } = ctx;

  return (todo: Todo): EffectiveDirection | undefined => {
    const direct = resolveDirect(todo, byTodo, labelById, knownDirectionIds, directionLabel);
    if (direct) return direct;

    // 4b. Cycle-safe `part_of` parent chain inheritance fallback.
    if (!todosById || todosById.size === 0) return undefined;
    const visited = new Set<string>([todo.id]);
    let parentId = todo.part_of?.trim() ?? "";
    while (parentId && !visited.has(parentId)) {
      visited.add(parentId);
      const parent = todosById.get(parentId);
      if (!parent) break;
      const parentDir = resolveDirect(parent, byTodo, labelById, knownDirectionIds, directionLabel);
      if (parentDir) {
        return { id: parentDir.id, label: parentDir.label, inherited: true };
      }
      parentId = parent.part_of?.trim() ?? "";
    }

    return undefined;
  };
}

/** Resolves the effective direction for a single to-do under `todos_v2 === true`. */
export function resolveEffectiveDirection(
  todo: Todo,
  ctx: EffectiveDirectionContext = {},
): EffectiveDirection | undefined {
  return createEffectiveDirectionResolver(ctx)(todo);
}
