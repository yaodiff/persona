// Shared blockers projection helpers for the Web To-dos view under todos_v2 (#14728, #17938).
// Mirrors the wire format emitted by server/internal/assistant/todo/projection/shared_blockers.go,
// supporting external blockers (PR/issue/external target) and internal prerequisite to-dos with readable titles.

import { t } from "../i18n";
import type { Todo } from "./todos";

export type SharedBlockerKind = "pull_request" | "issue" | "todo" | "external_target";

export interface SharedBlocker {
  key: string;
  kind: SharedBlockerKind;
  label: string;
  url?: string;
  /** Internal prerequisite to-do ID when `kind === "todo"`, allowing direct navigation in the list. */
  todo_id?: string;
  todo_ids: string[];
  count: number;
}

const VALID_KINDS: ReadonlySet<string> = new Set<SharedBlockerKind>([
  "pull_request",
  "issue",
  "todo",
  "external_target",
]);

const RAW_TODO_ID_RE = /^todo_[a-z0-9_-]{3,}$/i;

/** Reports whether a string looks like a raw internal to-do identifier (e.g. `todo_e8acf635da201b7a`). */
export function isRawTodoId(raw: string): boolean {
  return RAW_TODO_ID_RE.test(raw.trim());
}

function lookupTodoById(
  id: string,
  todosById?: ReadonlyMap<string, Pick<Todo, "id" | "title">>,
): Pick<Todo, "id" | "title"> | undefined {
  if (!id || !todosById) return undefined;
  const direct = todosById.get(id);
  if (direct) return direct;
  const lower = id.toLowerCase();
  for (const [k, td] of todosById) {
    if (k.toLowerCase() === lower) return td;
  }
  return undefined;
}

function detectBlockerTodoId(
  rec: Record<string, unknown>,
  key: string,
  rawLabel: string,
  todosById?: ReadonlyMap<string, Pick<Todo, "id" | "title">>,
): string | undefined {
  for (const field of [rec.todo_id, rec.target_id, rec.target_todo_id]) {
    if (typeof field === "string" && field.trim()) return field.trim();
  }
  if (isRawTodoId(rawLabel)) return rawLabel;
  if (key.startsWith("todo:")) {
    const id = key.slice(5).trim();
    if (id) return lookupTodoById(id, todosById)?.id ?? id;
  }
  if (key.startsWith("target:")) {
    const target = key.slice(7).trim();
    if (isRawTodoId(target)) return lookupTodoById(target, todosById)?.id ?? target;
  }
  if (rec.kind === "todo" && rawLabel) {
    const matched = lookupTodoById(rawLabel, todosById);
    if (matched) return matched.id;
  }
  return undefined;
}

/** Returns a trimmed http(s) URL string, or undefined for missing / unsafe URLs. */
export function safeBlockerUrl(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  const trimmed = raw.trim();
  if (!trimmed || /\s/.test(trimmed)) return undefined;
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    return trimmed;
  } catch {
    return undefined;
  }
}

/**
 * Tolerantly parses the optional `shared_blockers` field from `GET /api/v1/todos`.
 * - Safely degrades to `[]` when omitted (older hubs) or malformed.
 * - Retains only valid entries with `count >= 2` and `todo_ids.length >= 2`.
 * - When `visibleTodoIds` is supplied, scopes `todo_ids` to the currently visible
 *   assistant's to-dos and re-checks the `>= 2` threshold so cross-manager or deleted
 *   items never leave dead filter chips.
 * - Resolves internal prerequisite `todo_...` blockers to human-readable titles (preferring
 *   server-resolved readable labels, falling back to `todosById`, and never exposing raw `todo_...` IDs).
 */
export function parseSharedBlockers(
  raw: unknown,
  visibleTodoIds?: ReadonlySet<string>,
  todosById?: ReadonlyMap<string, Pick<Todo, "id" | "title">>,
): SharedBlocker[] {
  if (!Array.isArray(raw) || raw.length === 0) return [];

  const seenKeys = new Set<string>();
  const out: SharedBlocker[] = [];

  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const rec = item as Record<string, unknown>;

    const key = typeof rec.key === "string" ? rec.key.trim() : "";
    const rawLabel = typeof rec.label === "string" ? rec.label.trim() : "";
    if (!key || seenKeys.has(key)) continue;

    const blockerTodoId = detectBlockerTodoId(rec, key, rawLabel, todosById);
    let label = rawLabel;
    if (!label || isRawTodoId(label)) {
      if (blockerTodoId) {
        const matched = lookupTodoById(blockerTodoId, todosById);
        label = matched?.title?.trim() || t("todos.shared_blockers_unknown_todo");
      } else {
        continue;
      }
    }

    const rawCount = typeof rec.count === "number" && Number.isFinite(rec.count) ? Math.trunc(rec.count) : 0;
    if (rawCount < 2 || !Array.isArray(rec.todo_ids)) continue;

    const idSet = new Set<string>();
    for (const id of rec.todo_ids) {
      if (typeof id !== "string") continue;
      const trimmed = id.trim();
      if (!trimmed) continue;
      if (visibleTodoIds && !visibleTodoIds.has(trimmed)) continue;
      idSet.add(trimmed);
    }
    if (idSet.size < 2) continue;

    const rawKind = typeof rec.kind === "string" ? rec.kind : "";
    const kind: SharedBlockerKind =
      rawKind === "todo" || blockerTodoId
        ? "todo"
        : VALID_KINDS.has(rawKind)
          ? (rawKind as SharedBlockerKind)
          : "external_target";
    const url = safeBlockerUrl(rec.url);
    const todoIds = [...idSet];

    seenKeys.add(key);
    out.push({
      key,
      kind,
      label,
      ...(url ? { url } : {}),
      ...(kind === "todo" && blockerTodoId ? { todo_id: blockerTodoId } : {}),
      todo_ids: todoIds,
      count: visibleTodoIds ? todoIds.length : Math.max(rawCount, todoIds.length),
    });
  }

  out.sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
  return out;
}

/** Returns `selectedKey` only if it still exists in `blockers`; otherwise `null` to prevent empty deadlocks. */
export function resolveSelectedBlockerKey(
  selectedKey: string | null,
  blockers: readonly SharedBlocker[],
): string | null {
  if (!selectedKey) return null;
  return blockers.some((b) => b.key === selectedKey) ? selectedKey : null;
}

/** Finds the active shared blocker for `selectedKey`, or `undefined` if none is selected or it disappeared. */
export function findSelectedBlocker(
  selectedKey: string | null,
  blockers: readonly SharedBlocker[],
): SharedBlocker | undefined {
  if (!selectedKey) return undefined;
  return blockers.find((b) => b.key === selectedKey);
}

/** Predicate matching a to-do against an active shared blocker (always true when no blocker is selected). */
export function matchesSharedBlocker(
  todo: Pick<Todo, "id">,
  blocker: SharedBlocker | undefined,
): boolean {
  if (!blocker) return true;
  return blocker.todo_ids.includes(todo.id);
}

/** Pure filter returning only the to-dos blocked by the selected shared blocker (or all to-dos if none/stale). */
export function filterTodosBySharedBlocker<T extends Pick<Todo, "id">>(
  todos: readonly T[],
  blockers: readonly SharedBlocker[],
  selectedKey: string | null,
): T[] {
  const active = findSelectedBlocker(selectedKey, blockers);
  if (!active) return [...todos];
  const ids = new Set(active.todo_ids);
  return todos.filter((todo) => ids.has(todo.id));
}
