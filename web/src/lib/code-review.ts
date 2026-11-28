// UI version 5's code review (docs/extensions/extensions.md "UI version 5", #3136), the logic without the looks: which words of a
// changed line changed, a change that is only whitespace drawn as unchanged, generated files and lockfiles, the lines
// hidden between hunks and those shown of them, viewed marks kept in this browser, ```suggestion blocks in a comment,
// a range of lines picked for a comment, and the keys of a page of diffs. Drawn by views/extensions/DiffReview.tsx.
import { hunkRows, type DiffRow, type Hunk } from "./code-view";
import { safeWebURL } from "./format";

// ── words that changed ────────────────────────────────

/** Characters [start, end) of a line's text. */
export interface Span {
  start: number;
  end: number;
}

const WORD = /[\p{L}\p{N}_]+|\s+|[^\s\p{L}\p{N}_]/gu;

/** A line as words: runs of letters, digits and _, runs of whitespace, and each other character on its own. */
export function words(s: string): string[] {
  return s.match(WORD) ?? [];
}

/** Lines longer than this, or with more words, are not compared word by word (too slow, too noisy). */
const WORDS_MOST = 500;
const CHARS_MOST = 1000;
/** Under this share of characters in common (of both lines') two lines read as rewritten: the row's color says enough. */
const ALIKE_LEAST = 0.4;

/**
 * The spans that changed between an old line and the new one it became, word by word (the longest common sequence of
 * words); null when the lines are too long or too unlike for it to help. Whitespace between two changed words joins
 * them into one span.
 */
export function wordDiff(a: string, b: string): { old: Span[]; new: Span[] } | null {
  if (a === b) return { old: [], new: [] };
  if (a.length > CHARS_MOST || b.length > CHARS_MOST) return null;
  const x = words(a);
  const y = words(b);
  if (x.length > WORDS_MOST || y.length > WORDS_MOST) return null;
  // lcs[i][j]: the longest common sequence of x[i:] and y[j:]
  const n = x.length;
  const m = y.length;
  const lcs = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--)
    for (let j = m - 1; j >= 0; j--) lcs[i][j] = x[i] === y[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
  const keptX = new Array<boolean>(n).fill(false);
  const keptY = new Array<boolean>(m).fill(false);
  let common = 0;
  for (let i = 0, j = 0; i < n && j < m; ) {
    if (x[i] === y[j]) {
      keptX[i++] = keptY[j++] = true;
      common += x[i - 1].length;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) i++;
    else j++;
  }
  // alike enough as a whole, or one of them is nearly all in the other (words put in, or taken out)
  if ((2 * common) / (a.length + b.length) < ALIKE_LEAST && common / Math.min(a.length, b.length) < 0.7) return null;
  return { old: spansOf(x, keptX), new: spansOf(y, keptY) };
}

function spansOf(ws: string[], kept: boolean[]): Span[] {
  const out: Span[] = [];
  let at = 0;
  for (let i = 0; i < ws.length; i++) {
    const w = ws[i];
    if (!kept[i]) {
      const last = out[out.length - 1];
      if (last && last.end === at) last.end = at + w.length;
      else out.push({ start: at, end: at + w.length });
    } else if (/^\s+$/.test(w) && out.length && out[out.length - 1].end === at && i + 1 < ws.length && !kept[i + 1]) {
      // a space between two changed words: one span
      out[out.length - 1].end = at + w.length;
    }
    at += w.length;
  }
  return out;
}

/**
 * The changed spans of each row of a hunk (by its index): a run of deletions and the run of additions right after it
 * are paired line by line (the first deleted with the first added…), and each pair compared word by word.
 */
export function wordSpans(rows: readonly DiffRow[]): Map<number, Span[]> {
  const out = new Map<number, Span[]>();
  for (let i = 0; i < rows.length; ) {
    if (rows[i].kind !== "del") {
      i++;
      continue;
    }
    const dels: number[] = [];
    const adds: number[] = [];
    while (i < rows.length && rows[i].kind === "del") dels.push(i++);
    while (i < rows.length && rows[i].kind === "add") adds.push(i++);
    for (let k = 0; k < Math.min(dels.length, adds.length); k++) {
      const d = wordDiff(rows[dels[k]].text, rows[adds[k]].text);
      if (!d) continue;
      if (d.old.length) out.set(dels[k], d.old);
      if (d.new.length) out.set(adds[k], d.new);
    }
  }
  return out;
}

/** A run of text to draw, with its highlighting classes and whether it is a changed word. */
export interface Piece {
  text: string;
  cls: string;
  changed: boolean;
}

/** Highlighted tokens (or the plain text) cut where the changed spans begin and end. */
export function overlay(tokens: readonly { text: string; cls: string }[] | null | undefined, text: string, spans: readonly Span[] | undefined): Piece[] {
  const toks = tokens && tokens.length ? tokens : [{ text, cls: "" }];
  if (!spans?.length) return toks.map((t) => ({ ...t, changed: false }));
  const out: Piece[] = [];
  let at = 0;
  let s = 0;
  for (const tk of toks) {
    let from = 0;
    while (from < tk.text.length) {
      while (s < spans.length && spans[s].end <= at + from) s++;
      const span = spans[s];
      const inside = !!span && span.start <= at + from;
      const stop = span ? (inside ? span.end : span.start) - at : tk.text.length;
      const to = Math.min(tk.text.length, Math.max(stop, from + 1));
      out.push({ text: tk.text.slice(from, to), cls: tk.cls, changed: inside });
      from = to;
    }
    at += tk.text.length;
  }
  return out;
}

// ── whitespace ────────────────────────────────────────

const squeeze = (s: string) => s.replace(/\s+/g, "");

/**
 * A hunk's rows with changes that are only whitespace drawn as unchanged: in each run of deletions and additions, the
 * deleted and added lines that are the same but for whitespace (in order) become one unchanged row with both numbers
 * (and the new text); what is left stays as it was.
 */
export function ignoreWhitespace(rows: readonly DiffRow[]): DiffRow[] {
  const out: DiffRow[] = [];
  for (let i = 0; i < rows.length; ) {
    if (rows[i].kind !== "del" && rows[i].kind !== "add") {
      out.push(rows[i++]);
      continue;
    }
    const dels: DiffRow[] = [];
    const adds: DiffRow[] = [];
    while (i < rows.length && rows[i].kind === "del") dels.push(rows[i++]);
    while (i < rows.length && rows[i].kind === "add") adds.push(rows[i++]);
    const x = dels.map((r) => squeeze(r.text));
    const y = adds.map((r) => squeeze(r.text));
    const n = x.length;
    const m = y.length;
    const lcs = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
    for (let a = n - 1; a >= 0; a--) for (let b = m - 1; b >= 0; b--) lcs[a][b] = x[a] === y[b] ? lcs[a + 1][b + 1] + 1 : Math.max(lcs[a + 1][b], lcs[a][b + 1]);
    let a = 0;
    let b = 0;
    let pendD: DiffRow[] = [];
    let pendA: DiffRow[] = [];
    const flush = () => {
      out.push(...pendD, ...pendA);
      pendD = [];
      pendA = [];
    };
    while (a < n || b < m) {
      if (a < n && b < m && x[a] === y[b]) {
        flush();
        out.push({ kind: "context", old: dels[a].old, new: adds[b].new, text: adds[b].text, oldAt: dels[a].oldAt, newAt: adds[b].newAt });
        a++;
        b++;
      } else if (b >= m || (a < n && lcs[a + 1][b] >= lcs[a][b + 1])) pendD.push(dels[a++]);
      else pendA.push(adds[b++]);
    }
    flush();
  }
  return out;
}

/** Whether a hunk has anything left to show once whitespace is ignored. */
export function onlyWhitespace(hunks: readonly Hunk[]): boolean {
  return hunks.length > 0 && hunks.every((h) => ignoreWhitespace(hunkRows(h)).every((r) => r.kind === "context" || r.kind === "note"));
}

// ── generated files ───────────────────────────────────

const GENERATED: RegExp[] = [
  /(^|\/)(package-lock\.json|npm-shrinkwrap\.json|yarn\.lock|pnpm-lock\.yaml|bun\.lockb?|Cargo\.lock|Gemfile\.lock|composer\.lock|poetry\.lock|Pipfile\.lock|uv\.lock|go\.sum|go\.work\.sum|Podfile\.lock|Package\.resolved|gradle\.lockfile|flake\.lock|mix\.lock|pubspec\.lock|packages\.lock\.json)$/i,
  /\.(min\.js|min\.css|js\.map|css\.map|pb\.go|pb\.gw\.go|pb\.ts|pb\.swift|pb\.h|pb\.cc|g\.dart|freezed\.dart)$/i,
  /(_gen\.go|_generated\.go|\.generated\.[a-z]+|_pb2\.py|_pb2_grpc\.py|\.designer\.cs)$/i,
  /(^|\/)(dist|vendor|node_modules|__generated__|__snapshots__)\//,
];

/** A lockfile, a generated or a vendored file, by its path (or as the extension says). */
export function isGenerated(path: string, said?: boolean): boolean {
  return !!said || GENERATED.some((re) => re.test(path));
}

/** A file with more changed lines than this is folded at first. */
export const BIG_DIFF = 400;

/** Why a diff is folded at first: the extension said so, a generated file, a big one; or not. */
export function foldedAtFirst(item: { path?: string; collapsed?: boolean; generated?: boolean; additions?: number; deletions?: number; viewed?: boolean }): "collapsed" | "generated" | "big" | "viewed" | null {
  if (isGenerated(item.path ?? "", item.generated)) return "generated";
  if ((item.additions ?? 0) + (item.deletions ?? 0) > BIG_DIFF) return "big";
  if (item.collapsed) return "collapsed";
  if (item.viewed) return "viewed";
  return null;
}

// ── hidden lines ──────────────────────────────────────

/** How many lines of the new side a hunk has. */
const newCount = (h: Hunk) => h.lines.filter((l) => l[0] !== "-" && l[0] !== "\\").length;
const oldCount = (h: Hunk) => h.lines.filter((l) => l[0] !== "+" && l[0] !== "\\").length;

/**
 * The lines hidden before hunk `at` (at = hunks.length: after the last one), by their numbers on the new side: from
 * `from` to `to` (to undefined: to the file's end, not known yet). An old line is the new one plus `delta`.
 */
export interface Gap {
  at: number;
  from: number;
  to?: number;
  delta: number;
}

/** Every gap of a diff: before each hunk, and after the last (to the end, when total, the file's lines, says where). */
export function gaps(hunks: readonly Hunk[], total?: number): Gap[] {
  const out: Gap[] = [];
  let from = 1;
  let delta = 0;
  for (let i = 0; i <= hunks.length; i++) {
    const h = hunks[i];
    if (h) {
      if (h.new_start === 0) return []; // the file is gone: nothing unchanged to show
      out.push({ at: i, from, to: h.new_start - 1, delta: h.old_start - h.new_start });
      from = h.new_start + newCount(h);
      delta = h.old_start + oldCount(h) - from;
    } else out.push({ at: i, from, to: total, delta });
  }
  return out;
}

/** How many lines a gap hides (Infinity when its end is not known yet). */
export const gapSize = (g: Gap) => (g.to === undefined ? Infinity : Math.max(0, g.to - g.from + 1));

/** The lines shown of a gap: `top` from its start down (below the hunk before), `bottom` from its end up. */
export interface Shown {
  top: number;
  bottom: number;
}

/** A gap's lines shown at most this many at a time. */
export const STEP = 20;

/**
 * Showing more of a gap: down (below the hunk before it), up (above the hunk after it), or all. Says what is shown now
 * and which lines it needs (by new numbers; to undefined: to the end).
 */
export function reveal(g: Gap, shown: Shown, direction: "up" | "down" | "all"): { shown: Shown; need?: { from: number; to?: number } } {
  const size = gapSize(g);
  if (direction === "all") return { shown: { top: size === Infinity ? Infinity : size, bottom: 0 }, need: { from: g.from, to: g.to } };
  if (direction === "down" || g.to === undefined) {
    const top = Math.min(size - shown.bottom, shown.top + STEP);
    return { shown: { ...shown, top }, need: { from: g.from + shown.top, to: g.from + top - 1 } };
  }
  const bottom = Math.min(size - shown.top, shown.bottom + STEP);
  return { shown: { ...shown, bottom }, need: { from: g.to - bottom + 1, to: g.to - shown.bottom } };
}

/** Lines of one side of a file, as they came (any part of it), and how many it has once known. */
export class LineCache {
  private lines: (string | undefined)[] = [];
  total?: number;

  put(start: number, lines: readonly string[], total?: number) {
    lines.forEach((l, i) => (this.lines[start - 1 + i] = l));
    if (total !== undefined) this.total = total;
    else if (lines.length === 0 && this.total === undefined) this.total = start - 1; // asked past the end: there is none
  }

  /** Line n (1 the first), when it came. */
  get(n: number): string | undefined {
    return this.lines[n - 1];
  }

  /** Every line from..to came (to undefined: to the end, known). */
  has(from: number, to?: number): boolean {
    const end = to ?? this.total;
    if (end === undefined) return false;
    for (let n = from; n <= end; n++) if (this.lines[n - 1] === undefined) return false;
    return true;
  }
}

/** The rows a gap shows, as unchanged lines from the cache: those at its top, those at its bottom, and how many still hidden. */
export function gapRows(g: Gap, shown: Shown, cache: LineCache | undefined): { top: DiffRow[]; bottom: DiffRow[]; hidden: number } {
  const to = g.to ?? cache?.total;
  const size = to === undefined ? Infinity : Math.max(0, to - g.from + 1);
  const row = (n: number): DiffRow | null => {
    const text = cache?.get(n);
    return text === undefined ? null : { kind: "context", old: n + g.delta, new: n, text };
  };
  const top: DiffRow[] = [];
  for (let n = g.from; n < g.from + Math.min(shown.top, size); n++) {
    const r = row(n);
    if (!r) break;
    top.push(r);
  }
  const bottom: DiffRow[] = [];
  if (to !== undefined)
    for (let n = Math.max(g.from + top.length, to - shown.bottom + 1); n <= to; n++) {
      const r = row(n);
      if (r) bottom.push(r);
    }
  return { top, bottom, hidden: size === Infinity ? Infinity : Math.max(0, size - top.length - bottom.length) };
}

// ── viewed, kept in this browser ──────────────────────

const VIEWED_KEY = "persona.review.viewed";
const VIEWED_MOST = 5000;

/** What a file's change is, for a viewed mark kept here: its revision, else a digest of its lines. */
export function changeId(path: string, revision?: string, hunks?: readonly Hunk[]): string {
  if (revision) return `${path}@${revision}`;
  let h = 0x811c9dc5;
  for (const hk of hunks ?? [])
    for (const l of hk.lines)
      for (let i = 0; i < l.length; i++) {
        h ^= l.charCodeAt(i);
        h = Math.imul(h, 0x01000193);
      }
  return `${path}#${(h >>> 0).toString(36)}`;
}

/** The changes marked viewed in this browser (for extensions that keep none: no mark_viewed). */
export function viewedHere(): Set<string> {
  try {
    const raw = JSON.parse(localStorage.getItem(VIEWED_KEY) ?? "[]");
    return new Set(Array.isArray(raw) ? raw.filter((x) => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

/** Marks a change viewed here or not; the oldest marks go past VIEWED_MOST. */
export function setViewedHere(id: string, viewed: boolean) {
  const all = [...viewedHere()].filter((x) => x !== id);
  if (viewed) all.push(id);
  try {
    localStorage.setItem(VIEWED_KEY, JSON.stringify(all.slice(-VIEWED_MOST)));
  } catch {
    // private mode: for this page only
  }
}

// ── suggestions ───────────────────────────────────────

export type BodyPart = { kind: "text"; text: string } | { kind: "suggestion"; lines: string[] };

const SUGGESTION_OPEN = /^ {0,3}(`{3,})suggestion[ \t]*$/;
const SUGGESTION_CLOSE = /^ {0,3}(`{3,})[ \t]*$/;
const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;

/** A comment's body cut at its ```suggestion blocks: the text around them, and each block's lines. */
export function splitSuggestions(body: string): BodyPart[] {
  const out: BodyPart[] = [];
  let textStart = 0;
  let outerFence: { ch: string; len: number } | null = null;
  let sugg: { len: number; openStart: number; lines: string[] } | null = null;

  let pos = 0;
  while (pos < body.length) {
    const nl = body.indexOf("\n", pos);
    const lineEnd = nl === -1 ? body.length : nl + 1;
    const contentEnd = nl === -1 ? body.length : nl;
    const rawEnd = contentEnd > pos && body[contentEnd - 1] === "\r" ? contentEnd - 1 : contentEnd;
    const raw = body.slice(pos, rawEnd);

    if (sugg !== null) {
      const close = SUGGESTION_CLOSE.exec(raw);
      if (close && close[1].length >= sugg.len) {
        const before = body.slice(textStart, sugg.openStart);
        if (before.trim()) out.push({ kind: "text", text: before });
        out.push({ kind: "suggestion", lines: sugg.lines });
        textStart = rawEnd;
        sugg = null;
      } else {
        sugg.lines.push(raw);
      }
    } else if (outerFence !== null) {
      const close = FENCE_CLOSE.exec(raw);
      if (close && close[1][0] === outerFence.ch && close[1].length >= outerFence.len) {
        outerFence = null;
      }
    } else {
      const openSugg = nl !== -1 ? SUGGESTION_OPEN.exec(raw) : null;
      if (openSugg) {
        sugg = { len: openSugg[1].length, openStart: pos, lines: [] };
      } else {
        const openFence = FENCE_OPEN.exec(raw);
        if (openFence && !(openFence[1][0] === "`" && openFence[2].includes("`"))) {
          outerFence = { ch: openFence[1][0], len: openFence[1].length };
        }
      }
    }

    pos = lineEnd;
  }

  const rest = body.slice(textStart);
  if (rest.trim() || !out.length) out.push({ kind: "text", text: rest });
  return out;
}

/** The block a "Suggest a change" inserts: the lines picked, to edit into what they should be. */
export function suggestionBlock(lines: readonly string[]): string {
  return "```suggestion\n" + lines.join("\n") + "\n```\n";
}

// ── picking lines ─────────────────────────────────────

/** A side and a line of it. */
export interface Spot {
  side: "old" | "new";
  line: number;
}

/** The lines from one spot to another, on one side (start_line ≤ line); a spot on the other side starts afresh. */
export function pickRange(anchor: Spot | null, to: Spot): { side: "old" | "new"; line: number; start_line?: number } {
  if (!anchor || anchor.side !== to.side || anchor.line === to.line) return { side: to.side, line: to.line };
  return { side: to.side, start_line: Math.min(anchor.line, to.line), line: Math.max(anchor.line, to.line) };
}

/** A line link from a code's line_url: {start} and {end} filled in. */
export function lineLink(template: string | undefined, start: number, end = start): string | undefined {
  if (!template) return undefined;
  const url = template.replaceAll("{start}", String(start)).replaceAll("{end}", String(end));
  return safeWebURL(url, true);
}

// ── keys ──────────────────────────────────────────────

export type ReviewCommand =
  | "next-file" | "prev-file" | "next-change" | "prev-change" | "next-thread" | "prev-thread"
  | "comment" | "viewed" | "fold" | "mode" | "whitespace" | "filter" | "help";

const KEYS: Record<string, ReviewCommand> = {
  n: "next-file", p: "prev-file", j: "next-change", k: "prev-change", "]": "next-thread", "[": "prev-thread",
  c: "comment", v: "viewed", x: "fold", s: "mode", w: "whitespace", "/": "filter", "?": "help",
};

/** What a key pressed on a page of diffs does; nothing while typing, or with a modifier (the browser's own). */
export function reviewCommand(e: { key: string; ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean; target?: EventTarget | null; getModifierState?: (key: string) => boolean }): ReviewCommand | null {
  // AltGr (reported as Ctrl+Alt on Windows) types the character: "[" and "]" on a German or French keyboard
  const altGr = e.getModifierState?.("AltGraph") ?? false;
  if (e.metaKey || (!altGr && (e.ctrlKey || e.altKey))) return null;
  const el = e.target as HTMLElement | null;
  if (el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName ?? ""))) return null;
  return KEYS[e.key] ?? null;
}

/** The keys, for the help sheet (in the order shown). */
export const KEY_HELP: [string, ReviewCommand][] = [
  ["n / p", "next-file"], ["j / k", "next-change"], ["] / [", "next-thread"], ["c", "comment"], ["v", "viewed"], ["x", "fold"], ["s", "mode"], ["w", "whitespace"], ["/", "filter"], ["?", "help"],
];

/** Of elements top to bottom, the one after (or before) the one at the top of the screen: where n / p, j / k go. */
export function nextOf(tops: readonly number[], line: number, forward: boolean): number {
  if (!tops.length) return -1;
  if (forward) {
    const i = tops.findIndex((t) => t > line + 2);
    return i;
  }
  for (let i = tops.length - 1; i >= 0; i--) if (tops[i] < line - 2) return i;
  return -1;
}

// ── components in place ───────────────────────────────

interface Keyed {
  key?: string;
  type: string;
  hunks?: Hunk[];
  load?: unknown;
}
interface ViewOf<T extends Keyed> {
  sections: { items: T[] }[];
}

/** A view with the components of an action's answer (UI version 5's items) in place of those with the same key. */
export function patchView<T extends Keyed, V extends ViewOf<T>>(view: V, items: readonly T[] | undefined): V {
  const by = new Map((items ?? []).filter((it) => it.key).map((it) => [it.key!, it]));
  if (!by.size) return view;
  let hit = false;
  const sections = view.sections.map((s) => {
    if (!s.items.some((it) => it.key && by.has(it.key))) return s;
    hit = true;
    return { ...s, items: s.items.map((it) => (it.key && by.get(it.key)) || it) };
  });
  return hit ? { ...view, sections } : view;
}

/**
 * A view drawn again for the same page, keeping the diffs loaded since (by load) that it gives without their lines
 * again: the extension does not have to remember what was opened.
 */
export function keepLoaded<T extends Keyed, V extends ViewOf<T>>(before: V | null | undefined, next: V): V {
  if (!before) return next;
  const loaded = new Map<string, T>();
  for (const s of before.sections) for (const it of s.items) if (it.type === "diff" && it.key && it.hunks?.length) loaded.set(it.key, it);
  if (!loaded.size) return next;
  const keep = next.sections.flatMap((s) => s.items).filter((it) => it.type === "diff" && it.key && !it.hunks?.length && it.load && loaded.has(it.key));
  return keep.length ? patchView(next, keep.map((it) => loaded.get(it.key!)!)) : next;
}

// ── reading a file ────────────────────────────────────

/** Where q is in each line (any case): the lines that have it, and its spans in each. */
export function findIn(lines: readonly string[], q: string): { line: number; spans: Span[] }[] {
  const needle = q.toLowerCase();
  if (!needle) return [];
  const out: { line: number; spans: Span[] }[] = [];
  lines.forEach((l, i) => {
    const hay = l.toLowerCase();
    const spans: Span[] = [];
    for (let at = hay.indexOf(needle); at >= 0; at = hay.indexOf(needle, at + needle.length)) spans.push({ start: at, end: at + needle.length });
    if (spans.length) out.push({ line: i, spans });
  });
  return out;
}

/** A line number typed to go to, within the file (first..last); null when it is not one. */
export function lineTyped(text: string, first: number, last: number): number | null {
  const n = Number(text.trim());
  if (!Number.isInteger(n) || !text.trim()) return null;
  return Math.min(last, Math.max(first, n));
}
