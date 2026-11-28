// The sidebar's To-dos badge and the To-dos page's marks (#11085, #11838). The v2 board supplies the hub's counts.
// The fallback uses the current assistant's to-dos, linked Agents, local restores and a shared minute clock.

import { useEffect, useMemo, useSyncExternalStore } from "react";
import { answeredLookup } from "./answered-lookup";
import { useScopedTodos, type Scope } from "./current-assistant";
import { useAgentSessions } from "./sessions";
import { useStore, withTaskRows } from "./store";
import { ownerNeeds, type OwnerNeed } from "./todo-triage";
import { useRestoredTodos } from "./todo-restore";
import { makeSessionLookup, type SessionLookup } from "./todos";

const MINUTE_MS = 60_000;
let minute = Date.now();
let timer: ReturnType<typeof setInterval> | undefined;
const listeners = new Set<() => void>();

function onMinute(listener: () => void): () => void {
  listeners.add(listener);
  if (!timer) {
    // the first reader since none: the time it reads is now, not when the last one left
    minute = Date.now();
    timer = setInterval(() => {
      minute = Date.now();
      for (const l of listeners) l();
    }, MINUTE_MS);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size || !timer) return;
    clearInterval(timer);
    timer = undefined;
  };
}

/** The minute the To-dos page places its to-dos at and the badge counts them at: one clock for both, ticking once a
 *  minute while either is drawn. */
export function useTodoMinute(): number {
  return useSyncExternalStore(onMinute, () => minute, () => minute);
}

/** The to-dos' Agents as the To-dos page reads them: the Agents list's rows, an open task's Agent outside the list's
 *  window (`taskRows`), and an Agent whose card was just answered here counted as back at work until the hub catches
 *  up (lib/answered-lookup.ts). */
export function useTodoLookup(): SessionLookup {
  const sessions = useAgentSessions();
  const taskRows = useStore((s) => s.taskRows);
  const interactions = useStore((s) => s.interactions);
  const answered = useStore((s) => s.answered);
  return useMemo(() => answeredLookup(withTaskRows(makeSessionLookup(sessions), taskRows), interactions, answered), [sessions, taskRows, interactions, answered]);
}

/** The to-dos the sidebar's badge counts, each with why (ownerNeeds): its size is the badge, and the To-dos page marks
 *  exactly these rows. The current assistant's to-dos only, as the badge has always counted. */
export function useOwnerNeeds(enabled = true): ReadonlyMap<string, OwnerNeed> {
  const todos = useScopedTodos();
  const lookup = useTodoLookup();
  const [restored] = useRestoredTodos();
  // placed again on each minute's tick as on each change, at the moment it is placed (as the page's sections are)
  const minute = useTodoMinute();
  return useMemo(() => enabled ? ownerNeeds(todos, lookup, Date.now(), restored) : new Map(), [enabled, todos, lookup, restored, minute]);
}

export function useTodoBoard({ id, several }: Scope, enabled = true) {
  const source = useStore(({ assistant, todoBoard }) =>
    enabled && assistant?.todos_v2 === true && (!assistant.manager || assistant.manager === id) ? todoBoard : null);
  const scoped = useStore(({ scopedTodoBoard }) => several && scopedTodoBoard?.manager === id ? scopedTodoBoard.board : null);
  useEffect(() => {
    if (several && source) void import("../features/todos/board-wire").then((wire) => wire.refreshScopedTodoBoard(id, source), () => {});
  }, [id, several, source]);
  return several && source ? scoped : source;
}
