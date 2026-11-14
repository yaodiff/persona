// What the pages show once there are several assistants (docs/assistant/managers.md "Clients"): only the current one's
// computers, agents, cards, inbox items and to-dos. The hub's lists stay whole (every item carries its `manager`), so a
// link to another assistant's item still opens it; the pages filter what they list. With one assistant nothing is
// filtered: an item without `manager`, or of a manager the roster doesn't know, is the default's (ownerIn).
import { useMemo } from "react";
import { useChosenManager, useShownManager } from "./manager-choice";
import { installedManagers, managerOf, multiManager, ownerIn, useRoster, type Manager, type ManagerList } from "./managers";
import { stateCounts, useAgentListStatus, useAgentSessions, useCounts, useFullAgentSessions, type AgentList } from "./sessions";
import { useStore } from "./store";
import type { Session } from "./types";

export interface Scope {
  /** More than one assistant is installed: the pages filter. */
  several: boolean;
  /** The current assistant's id. */
  id: string;
  /** Its roster record; null with one assistant, or before the roster is read. */
  manager: Manager | null;
  /** Whether a thing with this `manager` is the current assistant's (always, with one assistant). */
  has: (manager: string | undefined) => boolean;
}

/** The scope for roster and the current assistant. `picked`: the pick this browser kept, which counts before the roster
 *  is read (a reload): the pages list its things only, rather than every assistant's until the roster comes. */
export function scopeOf(list: ManagerList | null, current: string, picked: string | null = null): Scope {
  if (!list && picked) return { several: true, id: picked, manager: null, has: (m) => m === picked };
  if (!multiManager(list)) return { several: false, id: current, manager: null, has: () => true };
  return { several: true, id: current, manager: managerOf(list, current), has: (m) => ownerIn(list, m) === current };
}

/** What a scope depends on: whether there are several assistants, the current one, the installed ones and the default
 *  (and the current one's name and state, which the inbox's dialogs show). A roster read again with only a count
 *  changed (`inbox_needs_you`, every `managers` push) keeps the scope, so the filtered lists stay the same arrays. */
export function scopeKey(list: ManagerList | null, current: string, picked: string | null = null): string {
  if (!list) return picked ? `pick|${picked}` : `one|${current}`;
  if (!multiManager(list)) return `one|${current}`;
  const me = managerOf(list, current);
  return JSON.stringify(["several", current, installedManagers(list).map((m) => m.id), list.default, me?.name, me?.online]);
}

/** The current assistant's scope; the same object while scopeKey stays. */
export function useScope(): Scope {
  const roster = useRoster();
  const current = useShownManager();
  const picked = useChosenManager();
  const key = scopeKey(roster, current, picked);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- the key is what the scope depends on
  return useMemo(() => scopeOf(roster, current, picked), [key]);
}

/** `items` of the current assistant (by each one's `manager`); the same array with one assistant. */
export function scoped<T extends { manager?: string }>(items: T[], scope: Scope): T[] {
  return scope.several ? items.filter((x) => scope.has(x.manager)) : items;
}

/** A list read from the store, of the current assistant only. */
export function useScoped<T extends { manager?: string }>(items: T[]): T[] {
  const scope = useScope();
  return useMemo(() => scoped(items, scope), [items, scope]);
}

/** The current assistant's computers. */
export function useScopedMachines() {
  return useScoped(useStore((s) => s.machines));
}

/** The current assistant's to-dos. */
export function useScopedTodos() {
  return useScoped(useStore((s) => s.todos));
}

/** The current assistant's pending cards. */
export function useScopedInteractions() {
  return useScoped(useStore((s) => s.interactions));
}

/** The agents the pages list: the current assistant's with several (from the complete list: the hub's first page
 *  can't be asked for one assistant's), else the list as the page had it. */
export function useScopedAgentSessions(): Session[] {
  const scope = useScope();
  const page = useAgentSessions();
  const { sessions } = useFullAgentSessions(scope.several);
  return useMemo(() => (scope.several ? scoped(sessions, scope) : page), [scope, sessions, page]);
}

/** useCounts for the current assistant: its agents by state, and its pending cards. */
export function useScopedCounts(): ReturnType<typeof useCounts> {
  const scope = useScope();
  const whole = useCounts();
  const { sessions } = useFullAgentSessions(scope.several);
  const interactions = useScopedInteractions().length;
  return useMemo(
    () => (scope.several ? { ...stateCounts(scoped(sessions, scope)), interactions, quietCounted: false } : whole),
    [scope, sessions, interactions, whole],
  );
}

/** Whether the agents useScopedAgentSessions and useScopedCounts give are known, from where they come: with several
 *  assistants the complete list ("loading" until it is read, "failed" while its read has failed, its `error`; it is read
 *  again every few seconds, and retryAgentList reads it at once, loading until the answer), else the Agents list's pages
 *  (useAgentListStatus). `whole`: they are every agent, so a page can count them by computer. */
export function useScopedAgentListStatus(): AgentList {
  const scope = useScope();
  const page = useAgentListStatus();
  const { loaded, error } = useFullAgentSessions(scope.several);
  return useMemo<AgentList>(
    () => (scope.several ? { status: loaded ? "loaded" : error ? "failed" : "loading", whole: loaded } : page),
    [scope.several, page, loaded, error],
  );
}
