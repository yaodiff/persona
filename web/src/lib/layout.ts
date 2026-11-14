// Which columns of the shell are shown on a wide screen (the shortcuts ⌘⌥B and ⌘⇧↵, docs/web/web.md "Keyboard shortcuts"), and
// how wide the sidebar is. A narrow screen has a drawer and a full-screen panel instead; these do nothing there.
//
// The sidebar folds to a rail of icons (#2524), not away: the rail keeps the pages one click off and its own button to
// open it again, so the choice is kept in this browser (a reload keeps it) without leaving anyone stuck without a
// sidebar. Maximizing (⌘⇧↵) still hides it outright, for this page only.
import { useSyncExternalStore } from "react";

export interface Layout {
  /** The sidebar column is folded to a rail of icons (⌘⌥B, the button at its top), kept in this browser. */
  sidebarCollapsed: boolean;
  /** Only the middle is shown: the sidebar column and the right panel are hidden (⌘⇧↵). */
  maximized: boolean;
}

export const COLLAPSED_KEY = "persona.sidebarCollapsed";

function stored(): boolean {
  try {
    return typeof localStorage !== "undefined" && localStorage.getItem(COLLAPSED_KEY) === "1";
  } catch {
    return false;
  }
}

let layout: Layout = { sidebarCollapsed: stored(), maximized: false };
const subscribers = new Set<() => void>();

function set(next: Layout) {
  layout = next;
  subscribers.forEach((f) => f());
}

export const getLayout = () => layout;

export function setSidebarCollapsed(collapsed: boolean) {
  try {
    if (collapsed) localStorage.setItem(COLLAPSED_KEY, "1");
    else localStorage.removeItem(COLLAPSED_KEY);
  } catch {
    // storage refused (private mode): the choice lasts this page only
  }
  set({ ...layout, sidebarCollapsed: collapsed });
}

/** The column's own collapse button, and ⌘⌥B, fold it or open it. App.tsx draws the rail in place of the sidebar (and
 *  back), so a focus inside the column goes with what is taken away and would fall to the page's start: with `keepFocus`
 *  (the focus was in the column) the collapse button drawn in its place takes it (takeFocusHandoff). */
export function toggleSidebarColumn(keepFocus = false) {
  focusHandoff = keepFocus;
  setSidebarCollapsed(!layout.sidebarCollapsed);
}
let focusHandoff = false;
/** Whether the collapse button just drawn takes the focus (once: the next call says no). */
export function takeFocusHandoff(): boolean {
  const take = focusHandoff;
  focusHandoff = false;
  return take;
}
/** The focus is in the column (App.tsx marks it `data-sidebar-column`), so folding or opening it would take it away. */
export const focusInColumn = () => typeof document !== "undefined" && Boolean(document.activeElement?.closest("[data-sidebar-column]"));
export const toggleMaximized = () => set({ ...layout, maximized: !layout.maximized });

// another tab folding or opening it
if (typeof addEventListener !== "undefined") {
  addEventListener("storage", (e) => {
    if ((e.key === COLLAPSED_KEY || e.key === null) && stored() !== layout.sidebarCollapsed) set({ ...layout, sidebarCollapsed: stored() });
  });
}

export function useLayout(): Layout {
  return useSyncExternalStore(
    (f) => {
      subscribers.add(f);
      return () => void subscribers.delete(f);
    },
    getLayout,
    getLayout,
  );
}

/** Whether the sidebar column is drawn on a wide screen (full or as the rail). */
export const sidebarShown = (l: Layout) => !l.maximized;

/** Whether a wide screen draws the rail of icons instead of the full sidebar. */
export const sidebarRail = (l: Layout) => l.sidebarCollapsed && !l.maximized;

// ── the full sidebar's width on a wide screen: its right edge drags (views/SidebarResize.tsx), kept in this browser ──
// Its own store, not part of Layout: a new width redraws only the handle, not everything that reads the fold.

export const SIDEBAR_WIDTH_KEY = "persona.sidebarWidth";
/** The width it has until it is dragged (and always had before it could be). */
export const SIDEBAR_DEFAULT = 284;
/** Narrower, the Agents filters no longer fit on one row. */
export const SIDEBAR_MIN = 240;
export const SIDEBAR_MAX = 480;

/** What the conversation beside the side panel keeps at the least (#5148): its input box and header still fit. */
export const CHAT_MIN = 440;
/** The narrowest the side panel is drawn on a wide screen (views/SidePanel.tsx). */
export const PANEL_MIN = 360;

const clampWidth = (w: number) => Math.round(Math.max(SIDEBAR_MIN, Math.min(SIDEBAR_MAX, w)));

function storedWidth(): number {
  try {
    const n = Number(typeof localStorage === "undefined" ? null : localStorage.getItem(SIDEBAR_WIDTH_KEY));
    return n > 0 ? clampWidth(n) : SIDEBAR_DEFAULT;
  } catch {
    return SIDEBAR_DEFAULT;
  }
}

let width = storedWidth();
const widthSubscribers = new Set<() => void>();

function setWidth(next: number) {
  width = next;
  widthSubscribers.forEach((f) => f());
}

/** The width asked for (dragged, or remembered); what is drawn is sidebarWidth(), which also fits the window. */
export const getSidebarWidth = () => width;

export function setSidebarWidth(w: number) {
  const next = clampWidth(w);
  try {
    localStorage.setItem(SIDEBAR_WIDTH_KEY, String(next));
  } catch {
    // storage refused (private mode): the width lasts this page only
  }
  setWidth(next);
}

/** Back to the default (a double-click on the edge, or Enter on it): nothing is kept. */
export function resetSidebarWidth() {
  try {
    localStorage.removeItem(SIDEBAR_WIDTH_KEY);
  } catch {
    // as above
  }
  setWidth(SIDEBAR_DEFAULT);
}

// another tab dragging it
if (typeof addEventListener !== "undefined") {
  addEventListener("storage", (e) => {
    if ((e.key === SIDEBAR_WIDTH_KEY || e.key === null) && storedWidth() !== width) setWidth(storedWidth());
  });
}

export function useSidebarWidth(): number {
  return useSyncExternalStore(
    (f) => {
      widthSubscribers.add(f);
      return () => void widthSubscribers.delete(f);
    },
    getSidebarWidth,
    getSidebarWidth,
  );
}

/**
 * The width the full sidebar is drawn at: the one asked for, within SIDEBAR_MIN…SIDEBAR_MAX and within `room`, what
 * the window has left once the page beside it (and an open side panel) have what they need. The room never takes it
 * under SIDEBAR_DEFAULT, the width it always had; only a drag does.
 */
export function sidebarWidth(want: number, room: number) {
  return Math.round(Math.max(SIDEBAR_MIN, Math.min(want, SIDEBAR_MAX, Math.max(SIDEBAR_DEFAULT, room))));
}
