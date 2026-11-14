// The complete Agents list's long groups, drawn a part at a time (the sidebar and the phone's Agents page, #7440). With
// 400 Agents in one group the list drew every row at once: about 8,000 elements, 170–230 ms of a mid-range phone's main
// thread each time the page opened, laid out again on every page switch. A list of up to `windowHead + windowPart`
// rows is drawn whole. A longer one draws its first `windowHead` rows and its last row always, and the rest in parts
// of `windowPart` while they are within a screen of the list's view (an IntersectionObserver rooted at the scroller
// marked `data-row-scroller`), or hold the open row (or held it last) or the keyboard's focus, or are next to one that
// does. A part out of reach is one empty box of the height it had when last drawn (a ResizeObserver's report) or, never
// drawn, of the first rows' average, so the scroll bar and what is below keep their place.
//
// The rows stay in the list's flow, not placed by offsets, so sticky group headings, the folds above them and their
// animation work as they did; and every row is a keyed sibling of the others under one parent, whether its list is
// long or not and whichever part it is in, so a row keeps its element (its focus, a menu anchored on it, its badge's
// animation) when an Agent comes or goes above it or the list grows past the bound. A hidden mark (`data-rows-part`,
// no box, so no gap) starts each drawn part, keyed by its first row so it moves with the rows rather than the rows
// around it; the observers find a part's rows between two marks. The groups inside the rows (a to-do's spawned Agents,
// a tab's panes) take a "Collapse / Expand all" they missed while their part was not drawn (`FoldMissed`).
import { isValidElement, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactElement, type ReactNode } from "react";
import { foldAllSeq, FoldMissed, type FoldRegion } from "../lib/fold-all";
import { SESSION_LIST_METRICS } from "../ui/list-metrics";

const { windowHead: HEAD, windowPart: PART, estimate: ESTIMATE } = SESSION_LIST_METRICS;
/** The list's scroller (the sidebar's rows, the Agents page): the observer's root, its view and a screen above and
 *  below it being near. Without one, the window's. */
const ROW_SCROLLER = "[data-row-scroller]";

export type WindowedProps<T> = {
  items: readonly T[];
  /** One row, keyed (its key is its identity among the list's rows). */
  render: (item: T) => ReactElement;
  /** The open row: its part and the two beside it are always drawn. */
  holds?: (item: T) => boolean;
};

/** Whether a list of `n` rows is drawn a part at a time. */
export const windowed = (n: number) => n > HEAD + PART && typeof IntersectionObserver !== "undefined" && typeof ResizeObserver !== "undefined";
/** How many parts a long list of `n` rows has: the head, parts of `windowPart`, and its last row on its own (always
 *  drawn: the previous-row shortcut from the list's first row wraps round to it). */
export const partCount = (n: number) => 2 + Math.ceil((n - 1 - HEAD) / PART);
/** The part holding row `index` of a long list of `n` rows. */
export const partOf = (index: number, n: number) => (index < HEAD ? 0 : index >= n - 1 ? partCount(n) - 1 : 1 + Math.floor((index - HEAD) / PART));
/** Where part `i` of a long list of `n` rows starts and ends. */
export const partRows = (i: number, n: number): [number, number] =>
  i === 0 ? [0, HEAD] : i === partCount(n) - 1 ? [n - 1, n] : [HEAD + (i - 1) * PART, Math.min(n - 1, HEAD + i * PART)];

/** The parts of a long list of `items` that hold the rows drawn before (`had`, keyed elements), found by key. */
function partsHolding<T>(had: readonly ReactNode[], items: readonly T[], render: (item: T) => ReactElement): ReadonlySet<number> {
  const keys = new Set<string>();
  for (const el of had) if (isValidElement(el) && el.key !== null) keys.add(el.key);
  const out = new Set<number>();
  for (let i = 0, found = 0; i < items.length && found < keys.size; i++) {
    const key = render(items[i]!).key;
    if (key === null || !keys.has(key)) continue;
    found++;
    out.add(partOf(i, items.length));
  }
  return out;
}

/** A drawn part's rows in the page (between its mark and the next), or a part's empty box. */
type Laid = { rows: Element[]; gap?: Element };
/** Where an element is against the observer's view: in it (with the screen around it), above or below. */
type Side = -1 | 0 | 1;

export function WindowedRows<T>({ items, render, holds }: WindowedProps<T>) {
  const n = items.length;
  const long = windowed(n);
  const around = useContext(FoldMissed);
  const [region] = useState<FoldRegion>(() => ({ since: around.since }));
  const [near, setNear] = useState<ReadonlySet<number>>(() => new Set());
  const [wasLong, setWasLong] = useState(long);
  // the rows the list drew last while it was short (at most the bound), read when it grows past it
  const shortRows = useRef<readonly ReactNode[]>([]);
  if (wasLong !== long) {
    setWasLong(long);
    // a list growing past the bound keeps drawing the parts that hold the rows it had (so they keep their elements, a
    // focus among them), and the observer adds the parts near the view: from 10 rows to 400 in one update it draws
    // those and its first and last rows, not all 400 for a commit
    if (long) setNear(partsHolding(shortRows.current, items, render));
  }
  // the part holding the keyboard's focus, drawn with the two beside it: Tab from a part's last row finds the next
  // part's first, and a focused row scrolled away is not taken out from under the focus
  const [focused, setFocused] = useState(-1);
  const [rowHeight, setRowHeight] = useState<number>(ESTIMATE);
  // each part's height when last drawn, with how many rows it had then
  const heights = useRef(new Map<number, { height: number; rows: number }>());
  const end = useRef<HTMLSpanElement>(null);
  const open = long && holds ? items.findIndex(holds) : -1;
  const openPart = open < 0 ? -1 : partOf(open, n);
  // the part that held the open row last stays drawn after it does not (a herdr Agent's page moves on to its tab's
  // address at once, which no row here holds): the list's second try to scroll to it, once a group that opened for it
  // has grown, still finds the row
  const [held, setHeld] = useState(openPart);
  if (openPart >= 0 && openPart !== held) setHeld(openPart);

  const last = long ? partCount(n) - 1 : 0;
  const at = openPart >= 0 ? openPart : held;
  const beside = (i: number, p: number) => p >= 0 && Math.abs(i - p) <= 1;
  const drawn = (i: number) => i === 0 || i === last || near.has(i) || beside(i, at) || beside(i, focused);
  let undrawn = false;
  const out: ReactNode[] = [];
  if (!long) out.push(...items.map(render));
  else {
    for (let i = 0; i <= last; i++) {
      const [from, to] = partRows(i, n);
      if (drawn(i)) {
        const rows = items.slice(from, to).map(render);
        out.push(<span key={`mark:${rows[0]?.key ?? i}`} hidden data-rows-part={i} />, ...rows);
      } else {
        undrawn = true;
        const kept = heights.current.get(i);
        out.push(<div key={`gap:${i}`} aria-hidden data-rows-part={i} data-rows-gap="" style={{ height: kept && kept.rows === to - from ? kept.height : (to - from) * rowHeight }} />);
      }
    }
    out.push(<span key="mark:end" ref={end} hidden data-rows-end="" />);
  }

  // the groups in a part drawn now take the fold-alls since a part was first left undrawn; with every part drawn, none
  useEffect(() => {
    if (!undrawn) region.since = null;
    else region.since ??= foldAllSeq();
    shortRows.current = long ? [] : out;
  });

  // what the observers watch, read from the page after each commit: no layout, only the marks and their siblings
  const laid = useRef(new Map<number, Laid>());
  const partOfRow = useRef(new Map<Element, number>());
  const sides = useRef(new Map<Element, Side>());
  const watching = useRef<{ near: IntersectionObserver; sizes: ResizeObserver; seen: Set<Element>; sized: Set<Element> } | null>(null);
  const latest = useRef({ n, last });
  useLayoutEffect(() => {
    latest.current = { n, last };
  });
  // which parts are near: an empty box in view, or a drawn part with a row in view or running through it (its first row
  // above, its last below); a row not reported yet counts as near, so nothing is taken out before the observer speaks
  const nearNow = () => {
    const next = new Set<number>();
    for (const [i, part] of laid.current) {
      if (part.gap) {
        if (sides.current.get(part.gap) === 0) next.add(i);
        continue;
      }
      const first = part.rows[0] ? sides.current.get(part.rows[0]) : undefined;
      const final = part.rows.length ? sides.current.get(part.rows[part.rows.length - 1]!) : undefined;
      if (first === undefined || final === undefined || first === 0 || final === 0 || (first < 0 && final > 0)) next.add(i);
    }
    setNear((was) => (was.size === next.size && [...next].every((i) => was.has(i)) ? was : next));
  };
  useLayoutEffect(() => {
    if (!long) return;
    const root = end.current?.closest<HTMLElement>(ROW_SCROLLER) ?? null;
    const near = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!watching.current?.seen.has(e.target)) continue;
        const top = e.rootBounds?.top ?? 0;
        sides.current.set(e.target, e.isIntersecting ? 0 : e.boundingClientRect.bottom <= top ? -1 : 1);
      }
      nearNow();
    }, { root, rootMargin: "100% 0px" });
    // a drawn part's height, read after the browser's own layout when one of its rows changes size (the first rows'
    // average sizes the parts never drawn)
    const sizes = new ResizeObserver((entries) => {
      const parts = new Set<number>();
      for (const e of entries) {
        const i = partOfRow.current.get(e.target);
        if (i !== undefined) parts.add(i);
      }
      for (const i of parts) {
        const rows = laid.current.get(i)?.rows;
        if (!rows?.length) continue;
        const height = rows[rows.length - 1]!.getBoundingClientRect().bottom - rows[0]!.getBoundingClientRect().top;
        const [from, to] = partRows(i, latest.current.n);
        if (height > 0) heights.current.set(i, { height, rows: to - from });
        if (i === 0 && height > 0) setRowHeight((h) => (Math.abs(h - height / HEAD) < 0.5 ? h : height / HEAD));
      }
    });
    watching.current = { near, sizes, seen: new Set(), sized: new Set() };
    return () => {
      near.disconnect();
      sizes.disconnect();
      watching.current = null;
      sides.current.clear();
      laid.current.clear();
      partOfRow.current.clear();
    };
  }, [long]);
  useLayoutEffect(() => {
    const w = watching.current;
    const stop = end.current;
    if (!long || !w || !stop) return;
    // walk back from the end mark to the first part's: each mark or box starts a part, the elements after a mark are its rows
    const parts = new Map<number, Laid>();
    const rowParts = new Map<Element, number>();
    let rows: Element[] = [];
    for (let el = stop.previousElementSibling; el; el = el.previousElementSibling) {
      const mark = (el as HTMLElement).dataset?.rowsPart;
      if (mark === undefined) {
        rows.unshift(el);
        continue;
      }
      const i = Number(mark);
      parts.set(i, el.hasAttribute("data-rows-gap") ? { rows: [], gap: el } : { rows });
      for (const row of rows) rowParts.set(row, i);
      rows = [];
      if (i === 0) break;
    }
    laid.current = parts;
    partOfRow.current = rowParts;
    // the observer watches each empty box and each drawn part's first and last rows (not the first part's or the last,
    // always drawn); the size observer every drawn row but the last part's
    const seen = new Set<Element>();
    const sized = new Set<Element>();
    for (const [i, part] of parts) {
      if (part.gap) seen.add(part.gap);
      else if (i !== 0 && i !== last && part.rows.length) seen.add(part.rows[0]!).add(part.rows[part.rows.length - 1]!);
      if (i !== last) for (const row of part.rows) sized.add(row);
    }
    for (const el of w.seen) if (!seen.has(el)) { w.near.unobserve(el); sides.current.delete(el); }
    for (const el of seen) if (!w.seen.has(el)) w.near.observe(el);
    for (const el of w.sized) if (!sized.has(el)) w.sizes.unobserve(el);
    for (const el of sized) if (!w.sized.has(el)) w.sizes.observe(el);
    w.seen = seen;
    w.sized = sized;
    // the focused row may have moved to another part (an Agent came or went above it)
    const active = document.activeElement;
    const box = stop.parentElement;
    if (active && box?.contains(active)) {
      let row: Element | null = active;
      while (row && row.parentElement !== box) row = row.parentElement;
      const i = row ? rowParts.get(row) : undefined;
      if (i !== undefined && i !== focused) setFocused(i);
    }
  });
  // Tab, a click: the part with the keyboard's focus; gone elsewhere in the page, none
  useEffect(() => {
    const box = long ? end.current?.parentElement : null;
    if (!box) return;
    const holding = (node: EventTarget | null) => {
      let row = node instanceof Element ? node : null;
      while (row && row.parentElement !== box) row = row.parentElement;
      return row ? partOfRow.current.get(row) : undefined;
    };
    const into = (e: FocusEvent) => {
      const i = holding(e.target);
      if (i !== undefined) setFocused(i);
    };
    // the window losing focus says nothing (no relatedTarget): the row keeps it for when the window comes back
    const away = (e: FocusEvent) => {
      if (e.relatedTarget instanceof Node && holding(e.relatedTarget) === undefined) setFocused(-1);
    };
    box.addEventListener("focusin", into);
    box.addEventListener("focusout", away);
    return () => {
      box.removeEventListener("focusin", into);
      box.removeEventListener("focusout", away);
    };
  }, [long]);

  return <FoldMissed.Provider value={region}>{out}</FoldMissed.Provider>;
}
