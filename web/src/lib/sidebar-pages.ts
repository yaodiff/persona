// The sidebar's pages as the user arranged them (Settings › General › Sidebar pages, docs/web/web.md "The sidebar's pages"): their order, and which
// stay in the list and which wait under More. Kept per browser, as the pinned conversations are: a phone and a
// laptop may well want different pages at hand.
//
// What is not decided by the user: the built-in pages stay in the list; an extension's page does too while fewer
// than EXTENSION_CAP extension pages are in it, and goes under More after that, so installing a tenth extension
// does not make the list longer. Settings always stays in the list (it is where all this is changed).
import { useSyncExternalStore } from "react";

const KEY = "sidebar.pages";

/** How many extension pages the list takes before the next ones wait under More (the user's own choices aside). */
export const EXTENSION_CAP = 3;

/** The page that is never put away. */
export const LOCKED = "settings";

/** What the user chose: an order (keys, the first ones first) and, per key, shown in the list or put under More. */
export type SidebarPrefs = { order: string[]; shown: string[]; hidden: string[] };

const NONE: SidebarPrefs = { order: [], shown: [], hidden: [] };

const isExtensionPage = (k: string) => k.startsWith("p:");

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x !== "") : []);

/** The stored choices (an unreadable or broken value is none). */
export function parsePrefs(raw: string | null): SidebarPrefs {
  if (!raw) return NONE;
  try {
    const v: unknown = JSON.parse(raw);
    if (!v || typeof v !== "object") return NONE;
    const o = v as Record<string, unknown>;
    return { order: strings(o.order), shown: strings(o.shown), hidden: strings(o.hidden) };
  } catch {
    return NONE;
  }
}

/** `keys` in the user's order: the ordered ones as ordered; one the order does not know (a page added since, a new
 *  extension) right before the page that comes after it by default, so a new extension's page lands before Settings as it
 *  would have anyway, and Settings stays last. */
export function ordered(keys: readonly string[], order: readonly string[]): string[] {
  const present = new Set(keys);
  const out = order.filter((k, i) => present.has(k) && order.indexOf(k) === i);
  const known = new Set(out);
  keys.forEach((k, i) => {
    if (known.has(k)) return;
    const next = keys.slice(i + 1).find((p) => known.has(p));
    out.splice(next === undefined ? out.length : out.indexOf(next), 0, k);
  });
  return out;
}

/** Whether each page is in the list: the user's choice, else the default (built-in yes; an extension's while the
 *  list has fewer than EXTENSION_CAP of them, counted in the order shown). Settings is always in. */
export function inList(keys: readonly string[], prefs: SidebarPrefs): Set<string> {
  const shown = new Set(prefs.shown);
  const hidden = new Set(prefs.hidden);
  let extensions = keys.filter((k) => isExtensionPage(k) && shown.has(k) && !hidden.has(k)).length;
  const out = new Set<string>();
  for (const k of keys) {
    if (k === LOCKED) out.add(k);
    else if (hidden.has(k)) continue;
    else if (shown.has(k)) out.add(k);
    else if (!isExtensionPage(k)) out.add(k);
    else if (extensions < EXTENSION_CAP) {
      out.add(k);
      extensions++;
    }
  }
  return out;
}

/** The pages split as the sidebar draws them: the list, then what waits under More, both in the user's order. */
export function arrangePages<T extends { k: string }>(pages: readonly T[], prefs: SidebarPrefs): { shown: T[]; more: T[] } {
  const byKey = new Map(pages.map((p) => [p.k, p]));
  const keys = ordered(pages.map((p) => p.k), prefs.order);
  const listed = inList(keys, prefs);
  const all = keys.map((k) => byKey.get(k)!);
  return { shown: all.filter((p) => listed.has(p.k)), more: all.filter((p) => !listed.has(p.k)) };
}

// ── the stored choices ───────────────────────────────────

const listeners = new Set<() => void>();
let cached: { raw: string | null; prefs: SidebarPrefs } = { raw: null, prefs: NONE };
let volatile: { raw: string | null; prefs: SidebarPrefs } | null = null;

export function getSidebarPrefs(): SidebarPrefs {
  if (volatile) return volatile.prefs;
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(KEY);
  } catch {
    // storage blocked: the defaults
  }
  if (raw !== cached.raw) cached = { raw, prefs: parsePrefs(raw) };
  return cached.prefs;
}

function save(p: SidebarPrefs) {
  const raw = p.order.length || p.shown.length || p.hidden.length ? JSON.stringify(p) : null;
  try {
    if (raw !== null) localStorage.setItem(KEY, raw);
    else localStorage.removeItem(KEY);
    volatile = null;
  } catch {
    volatile = { raw, prefs: p }; // storage blocked: the change lasts until the page reloads at most
  }
  cached = { raw, prefs: p };
  listeners.forEach((l) => l());
}

/** Puts page `k` in the list (`on`) or under More; the choice sticks over the default. Settings cannot be put away. */
export function setPageShown(k: string, on: boolean) {
  if (k === LOCKED) return;
  const p = getSidebarPrefs();
  save({ ...p, shown: on ? [...p.shown.filter((x) => x !== k), k] : p.shown.filter((x) => x !== k), hidden: on ? p.hidden.filter((x) => x !== k) : [...p.hidden.filter((x) => x !== k), k] });
}

/** The whole order, as the settings list now has it. */
export function setPageOrder(keys: readonly string[]) {
  save({ ...getSidebarPrefs(), order: [...keys] });
}

/** Moves `k` one place up (-1) or down (+1) among `keys` (the order shown). */
export function movePage(keys: readonly string[], k: string, by: -1 | 1) {
  const i = keys.indexOf(k);
  const j = i + by;
  if (i < 0 || j < 0 || j >= keys.length) return;
  const next = [...keys];
  [next[i], next[j]] = [next[j], next[i]];
  setPageOrder(next);
}

/** Back to the defaults: order, list and More as if nothing had been chosen. */
export function resetSidebarPrefs() {
  save(NONE);
}

if (typeof window !== "undefined") {
  // another tab changed it
  window.addEventListener("storage", (e) => {
    if (e.key === KEY || e.key === null) {
      volatile = null;
      listeners.forEach((l) => l());
    }
  });
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

export function useSidebarPrefs(): SidebarPrefs {
  return useSyncExternalStore(subscribe, getSidebarPrefs, () => cached.prefs);
}
