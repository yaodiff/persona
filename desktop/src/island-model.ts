import { notificationRoute } from "./contract.cjs";

// The island at the MacBook notch (#7669, docs/desktop/desktop.md "Island at the MacBook notch"): what the web sends,
// checked as untrusted input, the pill's two sides and where its window goes. No Electron here: tests run it as is.

export type IslandCategory = "needs_you" | "working" | "unread";

/** The bridge's DesktopIsland (spec/desktop-bridge.d.ts, which the preload checks against): can this shell draw the
 *  island here, and is it on. */
export interface IslandStatus {
  readonly supported: boolean;
  readonly enabled: boolean;
}

export interface IslandRow {
  readonly title: string;
  readonly state: IslandCategory;
  readonly label: string;
  readonly line: string;
  readonly route: string;
}

export interface IslandState {
  readonly needsYou: number;
  readonly working: number;
  readonly unread: number;
  readonly rows: readonly IslandRow[];
  readonly summary: string;
}

/** A side of the pill: a category and how many are in it. */
export interface IslandSide {
  readonly category: IslandCategory;
  readonly count: number;
}

export const EMPTY_ISLAND: IslandState = { needsYou: 0, working: 0, unread: 0, rows: [], summary: "" };

/** The most rows the list shows. */
export const ISLAND_ROWS = 5;

/** Most urgent first, as the iPhone's island and the Agents list rank them. */
const CATEGORIES: readonly IslandCategory[] = ["needs_you", "working", "unread"];

function text(value: unknown, maximum: number): string {
  return typeof value === "string" ? value.replace(/\s+/g, " ").trim().slice(0, maximum) : "";
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? Math.min(value, 1_000_000) : 0;
}

/** The state the web sent, as the island draws it; undefined for anything that is not one. A row without a title, a
 *  known state or a route of the hub's own (a notification's rule, contract.cts) is left out. */
export function islandState(value: unknown): IslandState | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const v = value as Record<string, unknown>;
  const rows: IslandRow[] = [];
  for (const raw of Array.isArray(v.rows) ? v.rows : []) {
    if (rows.length === ISLAND_ROWS) break;
    if (!raw || typeof raw !== "object") continue;
    const r = raw as Record<string, unknown>;
    const state = CATEGORIES.find(c => c === r.state);
    const route = typeof r.route === "string" ? notificationRoute(r.route) : undefined;
    const title = text(r.title, 200);
    if (!state || !route || !title) continue;
    rows.push({ title, state, label: text(r.label, 60), line: text(r.line, 200), route });
  }
  return { needsYou: count(v.needsYou), working: count(v.working), unread: count(v.unread), rows, summary: text(v.summary, 200) };
}

/** The pill's two sides, the iPhone's preset A (IslandCell.sides): the most urgent category with anyone in it on the
 *  left, the next such one on the right; neither when nobody needs you, works or finished unseen. */
export function islandSides(s: IslandState): readonly [IslandSide | undefined, IslandSide | undefined] {
  const counts: Record<IslandCategory, number> = { needs_you: s.needsYou, working: s.working, unread: s.unread };
  const shown = CATEGORIES.filter(c => counts[c] > 0).map(category => ({ category, count: counts[category] }));
  return [shown[0], shown[1]];
}

export interface IslandRect {
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
}

export interface IslandDisplay {
  readonly bounds: IslandRect;
  readonly workArea: IslandRect;
  readonly internal: boolean;
}

/** A plain menu bar is 24 pt high; a MacBook's with the notch is 32 or more. */
const PLAIN_MENU_BAR = 30;
/** The notch's width on the MacBooks that have one (about 180 to 205 pt): the pill's middle, nothing drawn in it. */
export const NOTCH_WIDTH = 200;
/** Each side's room: a symbol and up to four digits. */
export const ISLAND_SIDE = 64;
/** The open list's width: the pill's around the notch, so opening only grows the island downward and covers no more of
 *  the menu bar than the closed pill (#16301: at 400 pt it reached the menu bar items beside the pill). The same width
 *  on a display without a notch, where the closed pill is only its two sides. */
export const LIST_WIDTH = NOTCH_WIDTH + 2 * ISLAND_SIDE;
/** One row's height (island.css: a row is 3rem, the list's padding 14 px). */
export const ROW_HEIGHT = 48;
const LIST_PAD = 14;

/** The island's window apart from where it goes and its page's preferences (island.ts): frameless, transparent, always
 *  on top, a panel that takes no focus. `roundedCorners: false` takes the title bar style away: Electron keeps every
 *  frameless window titled unless told otherwise, and macOS rounds all four corners of a titled window (16 pt on macOS
 *  27), which pulled the island's top corners away from the top of the screen (#16301). The page draws the bottom
 *  ones. */
export const ISLAND_WINDOW = {
  show: false,
  frame: false,
  roundedCorners: false,
  transparent: true,
  backgroundColor: "#00000000",
  resizable: false,
  movable: false,
  minimizable: false,
  maximizable: false,
  fullscreenable: false,
  focusable: false,
  skipTaskbar: true,
  hasShadow: false,
  type: "panel",
  acceptFirstMouse: true,
  enableLargerThanScreen: true,
  alwaysOnTop: true,
  title: "Persona",
} as const;

/** Electron does not say where a notch is. The built-in display whose menu bar is taller than a plain one is taken to
 *  have one, at the top center. */
export function hasNotch(d: IslandDisplay): boolean {
  return d.internal && d.workArea.y - d.bounds.y >= PLAIN_MENU_BAR;
}

/** The display the island belongs on: the built-in one, where the notch is; else the primary. */
export function islandDisplay<T extends IslandDisplay>(all: readonly T[], primary: T): T {
  return all.find(d => d.internal) ?? primary;
}

/** The menu bar's height on that display: the closed pill's, and the open list's top row. */
export function menuBar(d: IslandDisplay): number {
  return Math.max(d.workArea.y - d.bounds.y, 24);
}

/** Where the island's window goes: closed, the menu bar's height around the notch (or the same pill without one) at
 *  the top center; open, the list's width (the pill's around the notch) and tall enough for its rows. */
export function islandRect(d: IslandDisplay, open: boolean, rows: number): IslandRect {
  const bar = menuBar(d);
  const width = open ? LIST_WIDTH : (hasNotch(d) ? NOTCH_WIDTH : 0) + 2 * ISLAND_SIDE;
  const height = open ? bar + Math.max(rows, 1) * ROW_HEIGHT + LIST_PAD : bar;
  return { x: Math.round(d.bounds.x + (d.bounds.width - width) / 2), y: d.bounds.y, width, height };
}

/** What the island's page draws. */
export interface IslandView {
  readonly sides: readonly [IslandSide | undefined, IslandSide | undefined];
  readonly rows: readonly IslandRow[];
  readonly summary: string;
  readonly notch: boolean;
  readonly open: boolean;
  /** The menu bar's height, the pill's and the open list's top row. */
  readonly bar: number;
}

export function islandView(s: IslandState, d: IslandDisplay, open: boolean): IslandView {
  return { sides: islandSides(s), rows: s.rows, summary: s.summary, notch: hasNotch(d), open, bar: menuBar(d) };
}
