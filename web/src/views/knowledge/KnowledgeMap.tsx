// Knowledge map (/kb/map): the knowledge base as a two-sided mind map (lib/kb-map.ts) in two views — the curated topics
// (topics → subtopics → knowledge points, docs/assistant/knowledge.md "Knowledge points") and the projects (what the
// main chat keeps on each, with the curated summary as sections) — and a side panel on what is selected. Pinch to zoom
// (trackpad, Safari gestures, two fingers), drag or scroll to pan; a tap selects, a double tap or the round toggle opens
// and closes, and the node opened or closed stays where it is on screen. A zoom only resizes the drawing: the layout and
// the nodes are kept until the map or its folding changes.
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { t } from "../../i18n";
import { api, enc, errorText, isAbort } from "../../lib/api";
import { BASE } from "../../lib/base";
import { type KBDetail, type KBDetailResponse, type KBEntry, type KBList } from "../../lib/kb";
import { pointDay, pointSources, pointWarns, POINT_KINDS, pointKindLabel } from "../../lib/kb-points";
import { buildProjectMap, buildTopicMap, layout, pointsUnder, walk, type Box, type Layout, type MapNode, type Placed, type SummaryItem, type TopicData } from "../../lib/kb-map";
import { buildTreeMap, canMove, nodeId, type Verdict } from "../../lib/kb-tree";
import { navigate } from "../../lib/router";
import { onHubEvent } from "../../lib/store";
import { Button, cx, IconButton, Loading, Menu } from "../../ui/atoms";
import { type MenuAt } from "../../ui/menu-model";
import { Icon } from "../../ui/icons";
import { scrollBehavior } from "../../ui/stream/motion";
import { settingsHref } from "../settings/nav";
import { ErrorBox } from "./KBList";
import { Card, SectionTitle, TonePill } from "./KBParts";
import { moveProject, nodeMenu, ProjectHome, TreeRootPanel, useTreeData } from "./ProjectTree";

const TONE = { accent: "var(--accent)", green: "var(--green)", orange: "var(--orange)" } as const;
const tint = (c: string, p: number) => `color-mix(in srgb, ${c} ${p}%, var(--surface))`;
const IMP_SHADE: Record<string, number> = { P0: 100, P1: 72, P2: 46, P3: 28 };
const EASE = "cubic-bezier(.2,.7,.2,1)";
const MOVE: CSSProperties = { transition: `transform .32s ${EASE}` };

// ── text measuring ──────────────────────────────────────
const ctx = typeof document !== "undefined" ? document.createElement("canvas").getContext("2d") : null;
const widths = new Map<string, number>();
let family = "";
function tw(s: string, px: number, weight = 400): number {
  if (!family && typeof document !== "undefined") family = getComputedStyle(document.body).fontFamily || "sans-serif";
  const k = `${px}|${weight}|${s}`;
  let w = widths.get(k);
  if (w === undefined) {
    if (ctx) ctx.font = `${weight} ${px}px ${family}`;
    w = ctx ? ctx.measureText(s).width : s.length * px;
    widths.set(k, w);
  }
  return w;
}
function clip(s: string, px: number, weight: number, max: number): string {
  if (tw(s, px, weight) <= max) return s;
  let out = s;
  while (out && tw(out + "…", px, weight) > max) out = out.slice(0, -1);
  return out + "…";
}
// What a line may break between (SVG text has no CSS line breaking): a run of spaces, one CJK character (Chinese and
// Japanese break between any two, as they always did here), or a word of anything else.
const CJK = "\\p{Script=Han}\\p{Script=Hiragana}\\p{Script=Katakana}\\p{Script=Hangul}\\u3000-\\u303f\\uff00-\\uffef";
const TOKENS = new RegExp(`\\s+|[${CJK}]|[^\\s${CJK}]+`, "gu");
/** `s` in at most `lines` lines of `max` px, the last clipped with "…" when it does not fit. Words stay whole; a word
 *  longer than a line breaks inside it; spaces never start a line. */
function wrap(s: string, px: number, max: number, lines: number): string[] {
  const out: string[] = [];
  let l = "";
  const full = () => out.length === lines;
  const put = (piece: string) => {
    if (l && tw(l + piece, px) > max) {
      out.push(l.trimEnd());
      l = piece;
    } else l += piece;
  };
  for (const tok of s.match(TOKENS) ?? []) {
    if (full()) break;
    if (/^\s/.test(tok)) {
      if (l) l += tok;
    } else if (tw(tok, px) <= max) put(tok);
    else for (const ch of tok) {
      put(ch);
      if (full()) break;
    }
  }
  if (!full()) out.push(l.trimEnd());
  else out[lines - 1] = clip(out[lines - 1] + "…", px, 400, max);
  return out;
}

function measure(n: MapNode): Box {
  switch (n.kind) {
    case "root": return { w: 108, h: 108, lines: [] };
    case "branch": return { w: 58 + tw(clip(n.title, 15, 650, 260), 15, 650) + 12 + tw(n.meta ?? "", 12, 600) + 16, h: 44, lines: [clip(n.title, 15, 650, 260)] };
    case "group": return { w: 34 + tw(n.title, 13, 650) + 26, h: 30, lines: [] };
    case "project": return { w: 280, h: 58, lines: [clip(n.title, 13.5, 600, 180)] };
    case "point": {
      const tag = tw(pointKindLabel(n.point!.kind), 11, 600) + 14, ls = wrap(n.title, 12, 300, 4);
      return { w: 10 + tag + 8 + Math.max(...ls.map((l) => tw(l, 12))) + 12, h: Math.max(28, 10 + ls.length * 17), lines: ls };
    }
    case "section": case "day": case "topic": {
      const s = clip(n.title, 12.5, 600, 220);
      return { w: 30 + tw(s, 12.5, 600) + (n.meta ? tw(n.meta, 11) + 12 : 0) + 14, h: 28, lines: [s] };
    }
    case "pnode": {
      const s = clip(n.title, 12.5, 600, 220);
      const w1 = 28 + tw(s, 12.5, 600) + (n.flag ? tw(n.flag.text, 11, 600) + 30 : 0) + (n.meta ? tw(n.meta, 11) + 12 : 0) + 12;
      const w2 = n.where ? 28 + tw(clip(n.where, 11, 400, 260), 11) + 12 : 0;
      return { w: Math.max(w1, w2), h: n.where ? 44 : 30, lines: [s] };
    }
    case "link": {
      const s = clip(n.title, 12.5, 400, 240);
      return { w: 32 + tw(s, 12.5) + 12, h: 28, lines: [s] };
    }
    default: {
      const ls = wrap(n.title, 12, 300, n.wrap ? 5 : 2);
      return { w: Math.max(...ls.map((l) => tw(l, 12))) + 20, h: 10 + ls.length * 17, lines: ls };
    }
  }
}

// ── data ────────────────────────────────────────────────
type View = "topics" | "projects" | "tree";
type Load = { root: MapNode; failed: number; empty: boolean; off: boolean } | { error: string } | null;

/** Every entry of a kind with its detail (a failed detail still gives the entry; `failed` counts them). */
async function entries(kind: string, signal: AbortSignal): Promise<{ list: KBEntry[]; details: Map<string, KBDetail>; failed: number }> {
  const list = (await api<KBList>(`/kb/${kind}`, { signal })).items ?? [];
  const got = await Promise.allSettled(list.map((e) => api<KBDetailResponse>(`/kb/${kind}/${enc(e.id)}`, { signal })));
  const details = new Map<string, KBDetail>();
  let failed = 0;
  got.forEach((r, i) => (r.status === "fulfilled" ? details.set(list[i].id, r.value.detail) : failed++));
  return { list, details, failed };
}

/** A view's map: read from scratch when the view or `reload` changes (Loading, then the map or the error), and quietly on
 *  `refresh()`: what is drawn stays while it reads, and a drawn map stays when that read fails. */
function useMapData(view: View, reload: number): [Load, () => void] {
  const [state, setState] = useState<Record<View, Load>>({ topics: null, projects: null, tree: null });
  const [refreshes, setRefreshes] = useState(0);
  const started = useRef("");
  useEffect(() => {
    const ac = new AbortController();
    if (view === "tree") return; // the project tree reads its own list (ProjectTree.tsx useTreeData)
    const quiet = started.current === `${view}:${reload}`;
    started.current = `${view}:${reload}`;
    if (!quiet) setState((s) => ({ ...s, [view]: null }));
    (async () => {
      try {
        if (view === "topics") {
          const [{ list, details, failed }, setting] = await Promise.all([
            entries("topics", ac.signal),
            // Only to say why the map is empty: a failed read is not the map's error.
            api<{ kb_topics?: boolean }>("/assistant", { signal: ac.signal }).catch((e): { kb_topics?: boolean } => {
              if (isAbort(e)) throw e;
              return {};
            }),
          ]);
          const data: TopicData[] = list.map((entry) => ({ entry, detail: details.get(entry.id) }));
          setState((s) => ({ ...s, topics: { root: buildTopicMap(data), failed, empty: list.length === 0, off: setting.kb_topics === false } }));
        } else {
          const { list, details, failed } = await entries("projects", ac.signal);
          setState((s) => ({ ...s, projects: { root: buildProjectMap(list, details), failed, empty: list.length === 0, off: false } }));
        }
      } catch (e) {
        if (isAbort(e)) return;
        const error = errorText(e);
        setState((s) => (quiet && s[view] && "root" in s[view] ? s : { ...s, [view]: { error } }));
      }
    })();
    return () => ac.abort();
  }, [view, reload, refreshes]);
  const refresh = useCallback(() => setRefreshes((x) => x + 1), []);
  return [state[view], refresh];
}

// ── pinch to zoom, drag to pan ──────────────────────────
// A trackpad pinch arrives as ctrl+wheel in Chrome / Firefox and as gesture events in Safari; on a touch screen it is two
// pointers. Every route zooms around the fingers: the point under them stays put once the new size is drawn. Zooming out
// goes on until the whole map fits (`floor`, never below ZFLOOR), so a large map can still be seen whole.
const ZMIN = 0.3, ZMAX = 2.5, ZFLOOR = 0.05;
/** A zoom button's step; the most one wheel event zooms (a mouse notch is 100 px of delta, a pinch sends small ones). */
const STEP = 1.25, WHEEL_MAX = 25;
/** How far a mouse press moves before it pans instead of clicking. */
const DRAG = 4;
// Once the new size is drawn, map point (mx, my) (in the map's own units) is put at (x, y) in the view. px / py: where the
// map starts inside the scrolled content (the room around it does not scale).
type Anchor = { x: number; y: number; mx: number; my: number; px: number; py: number };
/** How far the drawing is moved inside the map's box (its viewBox starts there), in the map's units. */
type Shift = { x: number; y: number };

/** The layout as drawn: every node moved by `s`. */
function moved(L: Layout, s: Shift): Layout {
  if (!s.x && !s.y) return L;
  return { ...L, cx: L.cx + s.x, cy: L.cy + s.y, placed: L.placed.map((p) => ({ ...p, x: p.x + s.x, y: p.y + s.y })) };
}

function place(el: HTMLElement, a: Anchor, zoom: number) {
  el.scrollLeft = a.px + a.mx * zoom - a.x;
  el.scrollTop = a.py + a.my * zoom - a.y;
}

/** Where the map starts inside the host's scrolled content. */
function mapOrigin(el: HTMLElement): { px: number; py: number } {
  const r = el.getBoundingClientRect(), m = el.querySelector("svg[data-map]")?.getBoundingClientRect() ?? r;
  return { px: m.left - r.left + el.scrollLeft, py: m.top - r.top + el.scrollTop };
}

function usePinchZoom(host: RefObject<HTMLDivElement | null>, zoom: number, setZoom: (z: number) => void, enabled: boolean, floor: () => number) {
  const current = useRef(zoom);
  current.current = zoom;
  const lowest = useRef(floor);
  lowest.current = floor;
  const anchor = useRef<Anchor | null>(null);
  /** Zooms to `next` keeping the map point at (clientX, clientY) (the view's middle by default) where it is; with `at`,
   *  map point `at` goes to the view's middle. */
  const zoomAt = useCallback((next: number, clientX?: number, clientY?: number, at?: { mx: number; my: number }) => {
    const el = host.current;
    if (!el) return;
    // Out no further than the floor, unless the zoom is below it already (the map grew smaller since): out never zooms in.
    const z = Math.min(ZMAX, Math.max(Math.min(lowest.current(), current.current), next));
    const r = el.getBoundingClientRect(), o = mapOrigin(el);
    if (at) {
      const a = { x: el.clientWidth / 2, y: el.clientHeight / 2, mx: at.mx, my: at.my, ...o };
      if (Math.abs(z - current.current) < 0.001) return place(el, a, current.current);
      anchor.current = a;
    } else {
      if (Math.abs(z - current.current) < 0.001) return;
      if (!anchor.current) {
        const x = (clientX ?? r.left + el.clientWidth / 2) - r.left, y = (clientY ?? r.top + el.clientHeight / 2) - r.top;
        anchor.current = { x, y, mx: (el.scrollLeft + x - o.px) / current.current, my: (el.scrollTop + y - o.py) / current.current, ...o };
      }
    }
    current.current = z;
    setZoom(z);
  }, [host, setZoom]);
  useLayoutEffect(() => {
    const el = host.current, a = anchor.current;
    if (!el || !a) return;
    anchor.current = null;
    place(el, a, zoom);
  }, [host, zoom]);
  useEffect(() => {
    const el = host.current;
    if (!el || !enabled) return;
    const wheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      const dy = e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1);
      zoomAt(current.current * Math.exp(-Math.max(-WHEEL_MAX, Math.min(WHEEL_MAX, dy)) * 0.01), e.clientX, e.clientY);
    };
    type Gesture = Event & { scale: number; clientX: number; clientY: number };
    let base = 1;
    const gStart = (e: Event) => { e.preventDefault(); base = current.current; };
    const gChange = (e: Event) => { e.preventDefault(); const g = e as Gesture; zoomAt(base * g.scale, g.clientX, g.clientY); };
    const touches = new Map<number, { x: number; y: number }>();
    let pinch: { d: number; z: number } | null = null;
    const spread = () => { const [a, b] = [...touches.values()]; return { d: Math.hypot(a.x - b.x, a.y - b.y), mx: (a.x + b.x) / 2, my: (a.y + b.y) / 2 }; };
    // A mouse press that moves past DRAG pans the map; the click it ends in is the drag's, not a tap on a node. (Touch
    // pans by itself: touch-action.)
    let drag: { id: number; x: number; y: number; sx: number; sy: number; moved: boolean } | null = null;
    let swallow = false;
    const down = (e: PointerEvent) => {
      if (e.pointerType === "mouse") {
        const r = el.getBoundingClientRect();
        // not on the scroll bars
        if (e.button === 0 && e.clientX - r.left < el.clientWidth && e.clientY - r.top < el.clientHeight) drag = { id: e.pointerId, x: e.clientX, y: e.clientY, sx: el.scrollLeft, sy: el.scrollTop, moved: false };
        return;
      }
      if (e.pointerType !== "touch") return;
      touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (touches.size === 2) pinch = { d: spread().d || 1, z: current.current };
    };
    const move = (e: PointerEvent) => {
      if (drag && e.pointerId === drag.id) {
        const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
        if (!drag.moved && Math.hypot(dx, dy) < DRAG) return;
        if (!drag.moved) {
          drag.moved = true;
          el.setPointerCapture(e.pointerId);
        }
        el.scrollLeft = drag.sx - dx;
        el.scrollTop = drag.sy - dy;
        return;
      }
      if (!touches.has(e.pointerId)) return;
      touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (pinch && touches.size === 2) { const s = spread(); zoomAt(pinch.z * (s.d / pinch.d), s.mx, s.my); }
    };
    const up = (e: PointerEvent) => {
      if (drag && e.pointerId === drag.id) {
        // the click comes right after pointerup, in the same task: swallow it, then nothing more
        if (drag.moved && e.type === "pointerup") (swallow = true), setTimeout(() => (swallow = false), 0);
        drag = null;
        return;
      }
      touches.delete(e.pointerId);
      if (touches.size < 2) pinch = null;
    };
    const click = (e: MouseEvent) => {
      if (!swallow) return;
      swallow = false;
      e.stopPropagation();
      e.preventDefault();
    };
    el.addEventListener("wheel", wheel, { passive: false });
    el.addEventListener("gesturestart", gStart);
    el.addEventListener("gesturechange", gChange);
    el.addEventListener("pointerdown", down);
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
    el.addEventListener("click", click, true);
    return () => {
      el.removeEventListener("wheel", wheel);
      el.removeEventListener("gesturestart", gStart);
      el.removeEventListener("gesturechange", gChange);
      el.removeEventListener("pointerdown", down);
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
      el.removeEventListener("click", click, true);
    };
  }, [host, enabled, zoomAt]);
  return zoomAt;
}

// ── page ────────────────────────────────────────────────
const VIEWS: View[] = ["topics", "projects", "tree"];
/** A segment of the header's tab bars (the views, By hierarchy | By tag). */
const TAB = "h-7 whitespace-nowrap rounded-[7px] text-[12.5px]";
const VIEW_LABEL: Record<View, string> = { topics: "knowledge-knowledgemap.view_topics", projects: "knowledge-knowledgemap.view_projects", tree: "knowledge-knowledgemap.view_tree" };
const SUBTITLE: Record<View, string> = { topics: "knowledge-knowledgemap.subtitle_topics", projects: "knowledge-knowledgemap.subtitle_projects", tree: "knowledge-knowledgemap.subtitle_tree" };

export function KnowledgeMap({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const [view, setView] = useState<View>("topics");
  const [reload, setReload] = useState(0);
  const [mapLoad, refresh] = useMapData(view, reload);
  // The project tree: its own list, drawn by hierarchy or by tag; what the user opened or closed survives a redraw.
  const tree = useTreeData(view === "tree", reload);
  const [mode, setMode] = useState<"tree" | "tag">("tree");
  const closed = useRef(new Map<string, boolean>());
  const treeRoot = useMemo(() => (view === "tree" && tree.list ? buildTreeMap(tree.list, tree.parents, mode, closed.current) : null), [view, tree.list, tree.parents, mode]);
  const load: Load = view === "tree" ? (tree.error && !tree.list ? { error: tree.error } : treeRoot ? { root: treeRoot, failed: 0, empty: !tree.list?.length, off: false } : null) : mapLoad;
  const [menu, setMenu] = useState<{ node: MapNode; at: MenuAt } | null>(null);
  const [milestoneAsk, setMilestoneAsk] = useState(0);
  const [sheet, setSheet] = useState(false);
  // A map that could not be read while the hub was away, or only in part (some entries' details failed), is read again
  // once it is back (its hello), quietly: what is drawn stays until the new read lands. A map read whole is not, since
  // reading it again means every entry's detail.
  const failed = Boolean(load && ("error" in load || load.failed > 0));
  useEffect(() => (failed ? onHubEvent("hello", () => refresh()) : undefined), [failed, refresh]);
  const [sel, setSel] = useState("");
  // The folding lives on the nodes (MapNode.collapsed); `folds` counts its changes, so the layout is redone only then.
  const [folds, bump] = useState(0);
  const [zoom, setZoom] = useState(1);
  const host = useRef<HTMLDivElement>(null);
  const root = load && "root" in load && !load.empty ? load.root : null;
  // eslint-disable-next-line react-hooks/exhaustive-deps -- `folds` stands for the folding the layout reads off the nodes
  const L = useMemo(() => (root ? layout(root, measure) : null), [root, folds]);
  // Opening or closing a branch keeps that node where it is on screen. The new layout is drawn moved by `shift`, so the node
  // keeps its place in the drawing while the rest slides around it, and the view scrolls by as much as the node moved in the
  // map's box, in the same frame: nothing jumps and slides back. Under reduced motion the slide is gone (styles.css) and
  // the node still stays put. `kept`: the node's place in the drawing when it was toggled.
  const shown = useRef<{ L: Layout | null; shift: Shift }>({ L: null, shift: { x: 0, y: 0 } });
  const kept = useRef<{ id: string; x: number; y: number } | null>(null);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- read once per new layout: `kept` is set just before it
  const shift = useMemo((): Shift => {
    const k = kept.current, p = k && L?.placed.find((x) => x.node.id === k.id);
    return k && p ? { x: k.x - p.x, y: k.y - p.y } : shown.current.shift;
  }, [L]);
  useLayoutEffect(() => {
    const el = host.current, before = shown.current.shift;
    shown.current = { L, shift };
    kept.current = null;
    const dx = (shift.x - before.x) * zoom, dy = (shift.y - before.y) * zoom;
    if (!el || (!dx && !dy)) return;
    el.scrollLeft -= dx;
    el.scrollTop -= dy;
  }, [L, shift, zoom]);
  /** The zoom that shows the whole map (with a little room). */
  const fits = useCallback(() => {
    const el = host.current;
    return el && L ? Math.min((el.clientWidth - 24) / L.width, (el.clientHeight - 24) / L.height) : 1;
  }, [L]);
  const zoomAt = usePinchZoom(host, zoom, setZoom, Boolean(root), () => Math.max(ZFLOOR, Math.min(ZMIN, fits())));
  const byId = useMemo(() => {
    const m = new Map<string, MapNode>();
    if (root) walk(root, (n) => m.set(n.id, n));
    return m;
  }, [root]);
  const parents = useMemo(() => {
    const m = new Map<string, MapNode | null>();
    if (root) walk(root, (n, p) => m.set(n.id, p));
    return m;
  }, [root]);
  const pick = (v: View) => {
    if (v === view) return;
    setSel("");
    setSheet(false);
    setZoom(1);
    setView(v);
  };
  const toggle = useCallback((n: MapNode) => {
    const { L, shift } = shown.current, p = L?.placed.find((x) => x.node === n);
    kept.current = p ? { id: n.id, x: p.x + shift.x, y: p.y + shift.y } : null;
    n.collapsed = !n.collapsed;
    if (view === "tree") closed.current.set(n.id, n.collapsed);
    bump((x) => x + 1);
  }, []);
  // Opened from the side panel: its branch opens, and the map brings it to the middle once it is laid out.
  const centerOn = useRef("");
  const reveal = useCallback((n: MapNode) => {
    for (let p = parents.get(n.id); p; p = parents.get(p.id)) p.collapsed = false;
    centerOn.current = n.id;
    setSel(n.id);
    bump((x) => x + 1);
  }, [parents]);
  useEffect(() => {
    const el = host.current, p = centerOn.current && L?.placed.find((x) => x.node.id === centerOn.current);
    if (!el || !p) return;
    centerOn.current = "";
    const o = mapOrigin(el);
    el.scrollTo({ left: o.px + (p.x + p.box.w / 2) * zoom - el.clientWidth / 2, top: o.py + p.y * zoom - el.clientHeight / 2, behavior: scrollBehavior() });
  }, [L, zoom]);
  const fitAll = () => L && zoomAt(Math.min(1, fits()), undefined, undefined, { mx: L.width / 2, my: L.height / 2 });
  // Zoom keys, as in the other zoomable views (the image viewer, the extension graph): with a node focused and no ⌘, Ctrl or
  // Alt held, + or = zooms in one step, - or _ out, 0 fits the whole map. A step zooms around the focused node (the
  // nearest point of it in view), so it stays put. ⌘/Ctrl + =, - and 0 are left to the browser's and the desktop app's page
  // zoom (lib/shortcuts.ts keeps them reserved).
  const zoomKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const el = host.current, focused = e.target instanceof Element ? e.target.closest("svg[data-map] [tabindex]") : null;
    if (!el || !focused || e.metaKey || e.ctrlKey || e.altKey) return;
    const step = e.key === "+" || e.key === "=" ? STEP : e.key === "-" || e.key === "_" ? 1 / STEP : e.key === "0" ? 0 : null;
    if (step === null) return;
    e.preventDefault();
    if (!step) return fitAll();
    const r = focused.getBoundingClientRect(), v = el.getBoundingClientRect();
    const x = Math.min(Math.max(r.left + r.width / 2, v.left), v.left + el.clientWidth), y = Math.min(Math.max(r.top + r.height / 2, v.top), v.top + el.clientHeight);
    zoomAt(zoom * step, x, y);
  };
  const selected = (sel && byId.get(sel)) || root;
  const openProject = (id: string) => {
    const n = byId.get(nodeId(id)) ?? [...byId.values()].find((x) => x.project?.id === id);
    if (n) reveal(n);
  };
  // Dragging and the node menu: only by hierarchy (in the tag view a project is drawn once per tag).
  const treeLive = useRef(tree);
  treeLive.current = tree;
  const canDrop = useCallback((a: MapNode, b: MapNode) => (a.project ? canMove(treeLive.current.parents, a.project.id, b.kind === "root" ? null : b.project?.id ?? "") : { ok: false }), []);
  const drop = useCallback((a: MapNode, b: MapNode) => void moveProject(treeLive.current, a.project!.id, b.kind === "root" ? null : b.project!.id), []);
  const drag = useMemo<DragApi>(() => ({ canDrop, onDrop: drop }), [canDrop, drop]);
  const dragEnabled = view === "tree" && mode === "tree" && tree.list ? drag : undefined;
  const onMenu = view === "tree" && tree.list ? (n: MapNode, at: MenuAt) => { if (n.project) setMenu({ node: n, at }); } : undefined;
  const choose = (n: MapNode) => {
    setSel(n.id);
    if (view === "tree" && n.project) setSheet(true);
  };
  const panel = (n: MapNode) => view === "tree"
    ? n.project
      ? <ProjectHome node={n} data={tree} onOpen={openProject} milestoneAsk={milestoneAsk} onMenu={(el) => setMenu({ node: n, at: { anchor: el } })} />
      : <TreeRootPanel data={tree} />
    : <Panel node={n} root={load && "root" in load ? load.root : n} parents={parents} failed={load && "root" in load ? load.failed : 0} onOpen={reveal} />;

  // By hierarchy | By tag: in the header, and on a narrow screen over the map's corner, where the header has no room.
  const arrange = (className: string) => (
    <div role="tablist" aria-label={t("knowledge-knowledgemap.arrange")} className={cx("shrink-0 rounded-control bg-inset p-0.5 shadow-[inset_0_0_0_1px_var(--line)]", className)}>
      {(["tree", "tag"] as const).map((m) => (
        <button key={m} type="button" role="tab" aria-selected={mode === m} onClick={() => { setMode(m); setSel(""); }}
          className={cx(TAB, "px-2.5", mode === m ? "bg-surface font-semibold text-ink shadow-btn" : "text-ink-2 hover:text-ink")}>
          {t(m === "tree" ? "knowledge-knowledgemap.by_hierarchy" : "knowledge-knowledgemap.by_tag")}
        </button>
      ))}
    </div>
  );
  return (
    <div className="flex h-full min-w-0 flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("knowledge-knowledgemap.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <IconButton icon="chevronLeft" label={t("knowledge-knowledgemap.back")} onClick={() => navigate({ name: "kb", kind: "topics" })} />
        <h1 className="truncate text-[14px] font-semibold text-ink">{t("knowledge-knowledgemap.title")}</h1>
        <span className="hidden min-w-0 truncate text-[12px] text-ink-3 md:inline">{t(SUBTITLE[view])}</span>
        <div className="flex-1" />
        {view === "tree" && arrange("hidden md:inline-flex")}
        <div role="tablist" aria-label={t("knowledge-knowledgemap.views")} className="inline-flex shrink-0 rounded-control bg-inset p-0.5 shadow-[inset_0_0_0_1px_var(--line)]">
          {VIEWS.map((v) => (
            <button key={v} type="button" role="tab" aria-selected={view === v} onClick={() => pick(v)}
              className={cx(TAB, "px-3", view === v ? "bg-surface font-semibold text-ink shadow-btn" : "text-ink-2 hover:text-ink")}>
              {t(VIEW_LABEL[v])}
            </button>
          ))}
        </div>
      </header>
      {!load ? (
        <div className="py-16"><Loading /></div>
      ) : "error" in load ? (
        <div className="mx-auto w-full max-w-[560px] p-6"><ErrorBox text={load.error} onRetry={() => setReload((x) => x + 1)} /></div>
      ) : load.empty ? (
        <Empty view={view} off={load.off} />
      ) : (
        <div className="flex min-h-0 flex-1">
          <div className="relative min-w-0 flex-1">
            <div ref={host} onKeyDown={zoomKey} className="absolute inset-0 cursor-grab select-none overflow-auto bg-canvas active:cursor-grabbing"
              style={{ backgroundImage: "radial-gradient(var(--line-strong) 1px, transparent 1px)", backgroundSize: "22px 22px", touchAction: "pan-x pan-y", overflowAnchor: "none" }}>
              {L && <MapCanvas key={view === "tree" ? `tree:${mode}` : view} L={L} shift={shift} title={load.root.title} sel={selected?.id ?? ""} zoom={zoom} onFit={setZoom} onSelect={choose} onToggle={toggle} drag={dragEnabled} onMenu={onMenu} />}
            </div>
            {view === "tree" && arrange("absolute top-3 left-3 z-10 inline-flex bg-surface md:hidden")}
            <div className="absolute bottom-3 left-3 z-10 inline-flex items-center gap-0.5 rounded-control bg-surface p-1 text-[12px] shadow-overlay">
              <IconButton icon="minus" label={t("knowledge-knowledgemap.zoom_out")} size={26} iconSize={12} onClick={() => zoomAt(zoom / STEP)} />
              <span className="min-w-10 text-center tabular-nums text-ink-3" aria-live="polite" aria-label={t("knowledge-knowledgemap.zoom_level", { percent: Math.round(zoom * 100) })}>{Math.round(zoom * 100)}%</span>
              <IconButton icon="plus" label={t("knowledge-knowledgemap.zoom_in")} size={26} iconSize={12} onClick={() => zoomAt(zoom * STEP)} />
              <IconButton icon="fit" label={t("ui-mermaid.fit")} size={26} iconSize={12} onClick={fitAll} />
            </div>
          </div>
          <aside aria-label={t("knowledge-knowledgemap.details")} className="hidden w-[380px] shrink-0 overflow-y-auto border-l border-line bg-surface lg:block">
            {selected && panel(selected)}
          </aside>
          {view === "tree" && sheet && selected?.project && (
            // phone and narrow windows: the same panel as a sheet from the bottom
            <div className="fixed inset-x-0 bottom-0 z-30 max-h-[62vh] overflow-y-auto rounded-t-[16px] bg-surface shadow-overlay lg:hidden" data-tree-sheet="">
              <div className="sticky top-0 z-[1] flex justify-end bg-surface px-3 pt-2"><IconButton icon="x" label={t("knowledge-knowledgemap.close")} size={28} onClick={() => setSheet(false)} /></div>
              {panel(selected)}
            </div>
          )}
        </div>
      )}
      {menu && menu.node.project && (
        <Menu items={nodeMenu(tree, menu.node.project, () => { setSel(menu.node.id); setMilestoneAsk((x) => x + 1); setSheet(true); })}
          anchor={menu.at.anchor ?? null} point={menu.at.point} header={menu.node.title} headerAsWritten width={260} onClose={() => setMenu(null)} />
      )}
    </div>
  );
}

function Empty({ view, off }: { view: View; off: boolean }) {
  return (
    <div className="flex flex-col items-center px-6 py-16 text-center">
      <span className="mb-3 flex size-10 items-center justify-center rounded-[11px] bg-field text-ink-3">
        <Icon name={view === "topics" ? "light" : "layers"} size={18} />
      </span>
      <p className="text-[13.5px] font-medium text-ink">{t(view !== "topics" ? "knowledge-knowledgemap.empty_projects" : off ? "knowledge-knowledgemap.empty_off" : "knowledge-knowledgemap.empty_topics")}</p>
      <p className="mt-1 max-w-[380px] text-[12.5px] leading-relaxed text-ink-3">
        {t(view !== "topics" ? "knowledge-knowledgemap.empty_projects_body" : off ? "knowledge-knowledgemap.empty_off_body" : "knowledge-knowledgemap.empty_topics_body")}
      </p>
      {view === "topics" && off && (
        <div className="mt-4">
          <Button tone="secondary" icon="gear" onClick={() => { location.href = BASE + settingsHref("knowledge", null, false); }}>{t("knowledge-knowledgemap.open_settings")}</Button>
        </div>
      )}
    </div>
  );
}

// ── map ─────────────────────────────────────────────────
function ribbon(x1: number, y1: number, x2: number, y2: number, w1: number, w2: number): string {
  const m = (x1 + x2) / 2;
  return `M${x1},${y1 - w1 / 2} C${m},${y1 - w1 / 2} ${m},${y2 - w2 / 2} ${x2},${y2 - w2 / 2} L${x2},${y2 + w2 / 2} C${m},${y2 + w2 / 2} ${m},${y1 + w1 / 2} ${x1},${y1 + w1 / 2} Z`;
}

/** What a node says to a screen reader: its title, what it holds and whether it is open. */
function spoken(n: MapNode): string {
  if (n.point) return `${pointKindLabel(n.point.kind)}: ${n.title}`;
  return n.meta ? `${n.title} · ${n.meta}` : n.title;
}

/** Dragging a project node onto another (the project tree by hierarchy): whether a node takes the dragged one, and the drop. */
type DragApi = { canDrop: (a: MapNode, b: MapNode) => Verdict; onDrop: (a: MapNode, b: MapNode) => void };
/** How far a press moves before it is a drag, and how long a finger holds for the node menu. */
const DRAG_PX = 6, HOLD_MS = 450;

function MapCanvas({ L, shift, title, sel, zoom, onFit, onSelect, onToggle, drag, onMenu }: {
  L: Layout; shift: Shift; title: string; sel: string; zoom: number; onFit: (z: number) => void; onSelect: (n: MapNode) => void; onToggle: (n: MapNode) => void;
  drag?: DragApi;
  /** a project node's menu (right click, Shift+F10, a long press) */
  onMenu?: (n: MapNode, at: MenuAt) => void;
}) {
  // A mouse or pen press that moves past DRAG_PX drags the project: the others say whether they take it (the ones that
  // can't are dimmed with the reason), the one under the pointer lights up, and letting go there moves it. Esc or
  // letting go elsewhere cancels. A finger held still opens the node menu (Move to… is the touch way to move).
  const [dragging, setDragging] = useState<{ src: MapNode; x: number; y: number; over: MapNode | null } | null>(null);
  const press = useRef<{ n: MapNode; x: number; y: number; id: number; timer?: number; moved: boolean } | null>(null);
  const suppress = useRef(false);
  const live = useRef(dragging);
  live.current = dragging;
  const toMap = (e: { clientX: number; clientY: number }) => {
    const r = box.current!.getBoundingClientRect();
    return { x: (e.clientX - r.left) / zoom + shift.x, y: (e.clientY - r.top) / zoom + shift.y };
  };
  const hit = (x: number, y: number, src: MapNode) => {
    const p = L.placed.find((q) => q.node !== src && x >= q.x && x <= q.x + q.box.w && y >= q.y - q.box.h / 2 && y <= q.y + q.box.h / 2);
    if (p) return p.node;
    return Math.hypot(x - L.cx, y - L.cy) < 62 ? L.placed.find((q) => q.node.kind === "root")?.node ?? null : null;
  };
  const verdicts = useMemo(() => {
    const m = new Map<MapNode, Verdict>();
    if (dragging && drag) {
      for (const p of L.placed) if (p.node !== dragging.src) m.set(p.node, drag.canDrop(dragging.src, p.node));
    }
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dragging?.src, L, drag]);
  const down = (n: MapNode) => (e: React.PointerEvent) => {
    if (!n.project || e.button !== 0 || (!drag && !onMenu)) return;
    if (e.pointerType !== "touch" && drag) e.preventDefault(); // no text selection (and no scroll with it) while dragging
    const st = { n, x: e.clientX, y: e.clientY, id: e.pointerId, moved: false, timer: undefined as number | undefined };
    if (e.pointerType === "touch" && onMenu) {
      const point = { x: e.clientX, y: e.clientY };
      st.timer = window.setTimeout(() => { suppress.current = true; press.current = null; onMenu(n, { point }); }, HOLD_MS);
    }
    press.current = st;
  };
  useEffect(() => {
    if (!drag && !onMenu) return;
    const move = (e: PointerEvent) => {
      const st = press.current;
      if (!st || e.pointerId !== st.id || Math.hypot(e.clientX - st.x, e.clientY - st.y) <= DRAG_PX) return;
      if (st.timer) { clearTimeout(st.timer); st.timer = undefined; }
      if (e.pointerType === "touch" || !drag) { press.current = null; return; } // a finger pans the map
      st.moved = true;
      const m = toMap(e);
      setDragging({ src: st.n, x: m.x, y: m.y, over: hit(m.x, m.y, st.n) });
    };
    const up = (e: PointerEvent) => {
      const st = press.current;
      press.current = null;
      if (!st) return;
      if (st.timer) clearTimeout(st.timer);
      if (!st.moved || e.type === "pointercancel") { setDragging(null); return; }
      suppress.current = true;
      const d = live.current;
      setDragging(null);
      if (d?.over && drag?.canDrop(d.src, d.over).ok) drag.onDrop(d.src, d.over);
    };
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key !== "Escape" || (!press.current && !live.current)) return;
      if (press.current?.timer) clearTimeout(press.current.timer);
      suppress.current = true;
      press.current = null;
      setDragging(null);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", up);
    window.addEventListener("keydown", esc);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", up);
      window.removeEventListener("keydown", esc);
    };
  });
  // First paint: shrink to fit (not below 65%), then center on the root. `target` is the zoom asked for: centering waits
  // until it is drawn, or a second run of this effect (React's dev StrictMode) would center at the old zoom.
  const fitted = useRef<"no" | "zoomed" | "done">("no");
  const target = useRef(zoom);
  const box = useRef<SVGSVGElement>(null);
  useEffect(() => {
    const host = box.current?.parentElement?.parentElement;
    if (!host || fitted.current === "done") return;
    if (fitted.current === "no") {
      fitted.current = "zoomed";
      const z = Math.max(0.65, Math.min(1, (host.clientWidth - 24) / L.width, (host.clientHeight - 24) / L.height));
      target.current = z;
      if (Math.abs(z - zoom) > 0.01) return onFit(z);
    }
    if (Math.abs(target.current - zoom) > 0.01) return;
    fitted.current = "done";
    const s = box.current!.getBoundingClientRect(), h = host.getBoundingClientRect();
    const left = s.left - h.left + host.scrollLeft, top = s.top - h.top + host.scrollTop; // where the map sits inside the padding
    host.scrollTo({ left: left + L.cx * zoom - host.clientWidth / 2, top: top + L.cy * zoom - host.clientHeight / 2 });
  });
  // The edges and nodes, kept while only the zoom changes: a zoom resizes the drawing, nothing in it.
  const drawing = useMemo(() => draw(moved(L, shift), sel, onSelect, onToggle, { at: new Map(L.placed.map((p) => [p.node, p])), dragging, verdicts, drag, down, onMenu, suppress }), [L, shift, sel, onSelect, onToggle, dragging, verdicts, drag, onMenu]);
  return (
    // Half a screen of room on every side, so a pinch can keep any point under the fingers even when the map is smaller than the view.
    // Overflow visible: while a closed branch's neighbours slide in, they are not cut at the map's new, smaller edge.
    <div className="w-max" style={{ padding: "50vh 50vw" }}>
      <svg ref={box} data-map="" role="group" aria-label={t("knowledge-knowledgemap.map_label", { name: title })}
        width={L.width * zoom} height={L.height * zoom} viewBox={`${shift.x} ${shift.y} ${L.width} ${L.height}`} overflow="visible" className="block">
        {drawing}
        {dragging && (
          <g style={{ pointerEvents: "none" }} transform={`translate(${dragging.x - shift.x + 12},${dragging.y - shift.y - 14})`} data-drag-ghost="">
            <rect width={tw(dragging.src.title, 12.5, 600) + 24} height={28} rx={14} fill="var(--surface)" stroke="var(--accent)" strokeWidth={1.5} style={{ filter: "drop-shadow(0 4px 10px rgb(0 0 0 / 30%))" }} />
            <text x={12} y={18.5} fontSize={12.5} fontWeight={600} fill="var(--ink)">{dragging.src.title}</text>
          </g>
        )}
        {dragging && (
          <text x={L.cx} y={L.cy + 82} textAnchor="middle" fontSize={11.5} fontWeight={600} fill="var(--accent)" paintOrder="stroke" stroke="var(--canvas)" strokeWidth={3} style={{ pointerEvents: "none" }}>
            {t("knowledge-knowledgemap.drop_top")}
          </text>
        )}
      </svg>
    </div>
  );
}

/** The draw's project-tree interaction state; none for the topics and projects maps. */
type DrawInteractions = {
  at: Map<MapNode, Placed>;
  dragging: { src: MapNode; x: number; y: number; over: MapNode | null } | null;
  verdicts: Map<MapNode, Verdict>;
  drag?: DragApi;
  down: (n: MapNode) => (e: React.PointerEvent) => void;
  onMenu?: (n: MapNode, at: MenuAt) => void;
  suppress: RefObject<boolean>;
};

function draw(L: Layout, sel: string, onSelect: (n: MapNode) => void, onToggle: (n: MapNode) => void, interactions: DrawInteractions): ReactNode {
  const { at, dragging, verdicts, drag, down, onMenu, suppress } = interactions;
  const key = (n: MapNode) => (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onSelect(n);
    } else if (onMenu && n.project && (e.key === "ContextMenu" || (e.shiftKey && e.key === "F10"))) {
      e.preventDefault();
      onMenu(n, { anchor: e.currentTarget as unknown as HTMLElement });
    } else if (n.kids.length && ((e.key === "ArrowRight" && n.collapsed) || (e.key === "ArrowLeft" && !n.collapsed))) {
      e.preventDefault();
      onToggle(n);
    }
  };
  const edges: ReactNode[] = [];
  const nodes: ReactNode[] = [];
  for (const p of L.placed) {
    const n = p.node;
    if (p.parent) {
      const a = at.get(p.parent)!, R = p.side === "R", d = p.depth;
      const x1 = p.parent.kind === "root" ? L.cx + (R ? 42 : -42) : R ? a.x + a.box.w + 20 : a.x - 20;
      const x2 = R ? p.x : p.x + p.box.w;
      const w1 = d === 1 ? 12 : d === 2 ? 4.5 : 2.2, w2 = d === 1 ? 5 : d === 2 ? 2 : 1.1;
      const path = ribbon(x1, a.y, x2, p.y, w1, w2);
      edges.push(<path key={n.id} d={path} fill={TONE[n.tone]} fillOpacity={d === 1 ? 0.85 : d === 2 ? 0.5 : 0.35}
        style={{ d: `path("${path}")`, transition: `d .32s ${EASE}` } as CSSProperties} />);
    }
    const v = dragging ? verdicts.get(n) : undefined, isSrc = dragging?.src === n, over = dragging?.over === n && v?.ok;
    const L2 = p.side === "L" && p.depth > 0;
    nodes.push(
      <g key={n.id} role="button" tabIndex={0} aria-label={spoken(n)} aria-pressed={n.id === sel} aria-expanded={n.kids.length && p.depth > 0 ? !n.collapsed : undefined}
        data-node={n.project?.id ?? (n.kind === "root" ? "root" : undefined)} data-drop={v ? (v.ok ? "ok" : "no") : undefined}
        className="outline-none [&:focus-visible>g]:[filter:drop-shadow(0_0_0.5px_var(--accent))_drop-shadow(0_0_2px_var(--accent))]"
        style={{ ...MOVE, transform: `translate(${p.x}px,${p.y - p.box.h / 2}px)`, cursor: drag && n.project ? "grab" : "pointer" }}
        onPointerDown={down(n)}
        onContextMenu={onMenu && n.project ? (e) => { e.preventDefault(); onMenu(n, { point: { x: e.clientX, y: e.clientY } }); } : undefined}
        onClick={() => { if (suppress.current) { suppress.current = false; return; } onSelect(n); }} onDoubleClick={() => n.kids.length && onToggle(n)} onKeyDown={key(n)}>
        {over && <rect x={-5} y={-5} width={p.box.w + 10} height={p.box.h + 10} rx={p.box.h / 2 + 5} fill="none" stroke="var(--accent)" strokeWidth={2.5} />}
        <g style={{ opacity: isSrc ? 0.35 : v && !v.ok ? 0.3 : 1, transition: "opacity .15s" }}><NodeShape n={n} p={p} selected={n.id === sel} /></g>
        {v && (!v.ok ? v.why : v.warn) && (over || !v.ok) && (
          <text x={L2 ? -8 : p.box.w + 26} y={p.box.h / 2 + 4} textAnchor={L2 ? "end" : "start"} fontSize={11} fontWeight={600}
            fill={v.ok ? "var(--attention)" : "var(--ink-3)"} paintOrder="stroke" stroke="var(--canvas)" strokeWidth={3}>{v.ok ? v.warn : v.why}</text>
        )}
      </g>,
    );
    if (n.kids.length && p.depth > 0) {
      const R = p.side === "R", lab = n.collapsed ? `+${n.kids.length}` : "−";
      const r = n.collapsed ? Math.max(9, tw(lab, 10, 650) / 2 + 5) : 8;
      const tx = R ? p.x + p.box.w + 11 : p.x - 11;
      nodes.push(
        <g key={`${n.id}:t`} role="button" aria-label={t(n.collapsed ? "knowledge-knowledgemap.expand" : "knowledge-knowledgemap.collapse", { name: n.title })}
          style={{ ...MOVE, transform: `translate(${tx}px,${p.y}px)`, cursor: "pointer" }} onClick={() => onToggle(n)}>
          <rect x={-r - 6} y={-15} width={2 * r + 12} height={30} fill="transparent" />
          <rect x={-r} y={-9} width={2 * r} height={18} rx={9} fill={n.collapsed ? TONE[n.tone] : "var(--surface)"} stroke={TONE[n.tone]} strokeWidth={1.3} />
          <text y={3.6} textAnchor="middle" fontSize={10} fontWeight={650} fill={n.collapsed ? "var(--surface)" : TONE[n.tone]}>{lab}</text>
        </g>,
      );
    }
  }
  return (
    <>
      {edges}
      {nodes}
    </>
  );
}

function NodeShape({ n, p, selected }: { n: MapNode; p: Placed; selected: boolean }) {
  const { w, h } = p.box, c = TONE[n.tone];
  const ring = selected ? <rect x={-3} y={-3} width={w + 6} height={h + 6} rx={n.kind === "project" ? 15 : h / 2 + 3} fill="none" stroke="var(--ink)" strokeWidth={2.2} /> : null;
  switch (n.kind) {
    case "root":
      return (
        <g>
          <circle cx={w / 2} cy={h / 2} r={62} fill="none" stroke="var(--ink)" strokeOpacity={0.1} strokeWidth={8} />
          <circle cx={w / 2} cy={h / 2} r={54} fill="var(--ink)" />
          <g transform={`translate(${w / 2 - 11},${h / 2 - 36})`} color="var(--surface)"><Icon name={n.view === "topics" ? "light" : n.view === "tree" ? "branch" : "layers"} size={22} stroke={2} /></g>
          <text x={w / 2} y={h / 2 + 7} textAnchor="middle" fontSize={17} fontWeight={650} fill="var(--surface)">{clip(n.title, 17, 650, 96)}</text>
          <text x={w / 2} y={h / 2 + 25} textAnchor="middle" fontSize={11} fill="var(--surface)" fillOpacity={0.7}>{n.meta}</text>
          {selected && <circle cx={w / 2} cy={h / 2} r={58} fill="none" stroke="var(--accent)" strokeWidth={2.2} />}
        </g>
      );
    case "branch":
      return (
        <g>
          <rect width={w} height={h} rx={22} fill={c} />
          <circle cx={22} cy={h / 2} r={14} fill="var(--surface)" fillOpacity={0.22} />
          <g transform={`translate(13,${h / 2 - 9})`} color="var(--surface)"><Icon name={n.view === "tree" ? "tag" : n.isTopic ? "light" : n.tone === "green" ? "check" : n.tone === "orange" ? "pause" : "folder"} size={18} stroke={2.2} /></g>
          <text x={44} y={h / 2 + 5.5} fontSize={15} fontWeight={650} fill="var(--surface)">{p.box.lines[0]}</text>
          <text x={w - 16} y={h / 2 + 4.5} textAnchor="end" fontSize={12} fontWeight={600} fill="var(--surface)" fillOpacity={0.85}>{n.meta}</text>
          {ring}
        </g>
      );
    case "group":
      return (
        <g>
          <rect width={w} height={h} rx={15} fill={tint(c, 14)} />
          <rect x={8} y={8} width={14} height={14} rx={4} fill={tint(c, IMP_SHADE[n.title] ?? 18)} />
          <text x={30} y={h / 2 + 4.5} fontSize={13} fontWeight={650} fill="var(--ink)">{n.title}</text>
          <text x={w - 10} y={h / 2 + 4} textAnchor="end" fontSize={11} fill="var(--ink-3)">{n.meta}</text>
          {ring}
        </g>
      );
    case "project": {
      const done = n.status === "done", lines = n.lines ?? [];
      const days = new Map<string, number>();
      for (const l of lines) days.set(l.when.slice(0, 10), (days.get(l.when.slice(0, 10)) ?? 0) + 1);
      const series = [...days].sort(([a], [b]) => a.localeCompare(b)).slice(-8);
      const mx = Math.max(1, ...series.map(([, v]) => v)), bw = 6, gap = 2, bx = w - 12 - series.length * (bw + gap);
      const sub = [n.meta, n.summary?.length ? t("knowledge-knowledgemap.has_summary") : "", lines.length ? t("knowledge-knowledgemap.notes", { count: lines.length }) : ""].filter(Boolean).join(" · ");
      return (
        <g>
          <rect width={w} height={h} rx={12} fill="var(--surface)" style={{ filter: "drop-shadow(0 1px 2px rgb(16 24 40 / 6%)) drop-shadow(0 3px 8px rgb(16 24 40 / 7%))" }} />
          <rect x={0} y={10} width={4} height={h - 20} rx={2} fill={n.importance && IMP_SHADE[n.importance] ? tint("var(--accent)", IMP_SHADE[n.importance]) : "var(--line-strong)"} />
          <text x={16} y={h / 2 - 5} fontSize={13.5} fontWeight={600} fill={done ? "var(--ink-3)" : "var(--ink)"}>{p.box.lines[0]}</text>
          <text x={16} y={h / 2 + 14} fontSize={11} fill="var(--ink-3)">{clip(sub, 11, 400, bx - 24)}</text>
          {series.map(([d, v], i) => {
            const bh = Math.max(2, (v / mx) * 24);
            return <rect key={d} x={bx + i * (bw + gap)} y={h / 2 + 14 - bh} width={bw} height={bh} rx={1.5} fill={done ? "var(--ink-3)" : TONE[n.tone]} fillOpacity={i === series.length - 1 ? 1 : 0.5} />;
          })}
          {ring}
        </g>
      );
    }
    case "section": case "day": case "topic":
      return (
        <g>
          <rect width={w} height={h} rx={14} fill={tint(c, 13)} />
          <g transform={`translate(9,${h / 2 - 7})`} color={c}><Icon name={n.icon ?? (n.isTopic ? "book" : "chat")} size={14} stroke={2} /></g>
          <text x={28} y={h / 2 + 4.3} fontSize={12.5} fontWeight={600} fill="var(--ink)">{p.box.lines[0]}</text>
          {n.meta && <text x={w - 12} y={h / 2 + 4} textAnchor="end" fontSize={11} fill="var(--ink-3)">{n.meta}</text>}
          {ring}
        </g>
      );
    case "point": {
      const label = pointKindLabel(n.point!.kind), tag = tw(label, 11, 600) + 14, warn = pointWarns(n.point!.kind);
      return (
        <g>
          <rect width={w} height={h} rx={8} fill="var(--surface)" stroke="var(--line)" />
          <rect x={8} y={6} width={tag} height={16} rx={8} fill={warn ? "var(--orange-tint)" : tint(c, 14)} />
          <text x={8 + tag / 2} y={17.6} textAnchor="middle" fontSize={11} fontWeight={600} fill={warn ? "var(--orange)" : "var(--ink-2)"}>{label}</text>
          {p.box.lines.map((l, i) => <text key={i} x={8 + tag + 8} y={18.5 + i * 17} fontSize={12} fill="var(--ink)">{l}</text>)}
          {ring}
        </g>
      );
    }
    case "pnode": {
      const f = n.flag, fc = f?.state === "accepted" ? "var(--green)" : f?.state === "not_accepted" ? "var(--orange)" : "var(--accent)";
      const mh = n.where ? 30 : h, tx = 28 + tw(p.box.lines[0], 12.5, 600) + 8;
      const done = n.status === "done";
      return (
        <g>
          <rect width={w} height={h} rx={14} fill={tint(c, n.depth === 1 ? 20 : 12)} />
          <g transform={`translate(9,${mh / 2 - 7})`} color={c}><Icon name="folder" size={14} stroke={2} /></g>
          <text x={28} y={mh / 2 + 4.3} fontSize={12.5} fontWeight={600} fill={done ? "var(--ink-3)" : "var(--ink)"}>{p.box.lines[0]}</text>
          {f && (
            <g transform={`translate(${tx},${mh / 2 - 9})`} data-flag={f.state}>
              <rect width={tw(f.text, 11, 600) + 26} height={18} rx={9} fill={tint(fc, 20)} />
              <g transform="translate(6,3)" color={fc}><Icon name="flag" size={12} stroke={2.2} /></g>
              <text x={21} y={12.6} fontSize={11} fontWeight={600} fill={fc}>{f.text}</text>
            </g>
          )}
          {n.meta && <text x={w - 12} y={mh / 2 + 4} textAnchor="end" fontSize={11} fill="var(--ink-3)">{n.meta}</text>}
          {n.where && <text x={28} y={36} fontSize={11} fill="var(--ink-3)">{clip(n.where, 11, 400, 260)}</text>}
          {ring}
        </g>
      );
    }
    case "link":
      return (
        <g>
          <rect width={w} height={h} rx={8} fill="var(--surface)" stroke="var(--line)" />
          <g transform={`translate(9,${h / 2 - 7.5})`} color={c}><Icon name="link" size={15} stroke={2} /></g>
          <text x={30} y={h / 2 + 4.3} fontSize={12.5} fill="var(--ink)">{p.box.lines[0]}</text>
          {ring}
        </g>
      );
    default:
      return (
        <g>
          <rect width={w} height={h} rx={8} fill="var(--surface)" fillOpacity={0.8} />
          {p.box.lines.map((l, i) => <text key={i} x={10} y={18 + i * 17} fontSize={12} fill="var(--ink-2)">{l}</text>)}
          {ring}
        </g>
      );
  }
}

// ── side panel ──────────────────────────────────────────
// Memo: a zoom or a fold redraws the page, not the panel (the topics' panel counts every point and source).
const Panel = memo(function Panel({ node, root, parents, failed, onOpen }: { node: MapNode; root: MapNode; parents: Map<string, MapNode | null>; failed: number; onOpen: (n: MapNode) => void }) {
  const crumbs: string[] = [];
  for (let p = parents.get(node.id); p && p.kind !== "root"; p = parents.get(p.id)) crumbs.unshift(p.title);
  if (node.kind === "root") return node.view === "topics" ? <TopicsRootPanel root={root} failed={failed} onOpen={onOpen} /> : <ProjectsRootPanel root={root} failed={failed} />;
  if (node.point) return <PointPanel node={node} crumbs={crumbs} />;
  if (node.isTopic) return <TopicPanel node={node} crumbs={crumbs} onOpen={onOpen} />;
  if (node.kind === "project") return <ProjectPanel node={node} crumbs={crumbs} />;
  return (
    <div className="p-5">
      <h2 className="text-[16px] font-semibold leading-snug text-ink">{node.title}</h2>
      <div className="mt-0.5 mb-3 text-[12px] text-ink-3">{crumbs.join(" › ")}</div>
      {node.body && <div className="rounded-[12px] bg-inset px-3.5 py-3 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink">{node.body}</div>}
      {node.open && <OpenButton open={node.open} />}
      {node.kids.length > 0 && <KidList kids={node.kids} onOpen={onOpen} />}
    </div>
  );
});

function KidList({ kids, onOpen }: { kids: MapNode[]; onOpen: (n: MapNode) => void }) {
  return (
    <div className="mt-3">
      {kids.slice(0, 12).map((k) => (
        <button key={k.id} type="button" onClick={() => onOpen(k)} className="flex w-full items-center gap-2 border-t border-line py-2 text-left text-[12.5px] text-ink hover:text-accent-ink">
          <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" /><span className="truncate">{k.title}</span>
          {k.meta && <span className="ml-auto shrink-0 text-[11px] text-ink-3">{k.meta}</span>}
        </button>
      ))}
    </div>
  );
}

function OpenButton({ open }: { open: NonNullable<MapNode["open"]> }) {
  return (
    <div className="mt-4">
      <Button tone="secondary" icon="external" onClick={() => navigate({ name: "kb", kind: open.kind, id: open.id })}>{t("knowledge-knowledgemap.open_entry")}</Button>
    </div>
  );
}

function KindCounts({ points }: { points: MapNode[] }) {
  const by = new Map<string, number>();
  for (const p of points) by.set(p.point!.kind, (by.get(p.point!.kind) ?? 0) + 1);
  const known = POINT_KINDS.filter((k) => by.get(k));
  const other = [...by].filter(([k]) => !(POINT_KINDS as readonly string[]).includes(k)).reduce((n, [, v]) => n + v, 0);
  return (
    <div className="my-3 flex flex-wrap gap-1.5">
      {known.map((k) => <TonePill key={k} tone={pointWarns(k) ? "orange" : "neutral"}>{t("knowledge-knowledgemap.kind_count", { kind: pointKindLabel(k), count: by.get(k)! })}</TonePill>)}
      {other > 0 && <TonePill>{t("knowledge-knowledgemap.kind_count", { kind: pointKindLabel(""), count: other })}</TonePill>}
    </div>
  );
}

function TopicsRootPanel({ root, failed, onOpen }: { root: MapNode; failed: number; onOpen: (n: MapNode) => void }) {
  const points = pointsUnder(root);
  const sources = new Set(points.flatMap((p) => (p.point!.sources ?? []).map((s) => `${s.kind}/${s.id}`)));
  return (
    <div className="p-5">
      <h2 className="text-[18px] font-semibold text-ink">{t("knowledge-knowledgemap.topics_title")}</h2>
      <Kpis items={[[root.kids.length, t("knowledge-knowledgemap.kpi_topics")], [points.length, t("knowledge-knowledgemap.kpi_points")], [sources.size, t("knowledge-knowledgemap.kpi_sources")]]} />
      <Callout title={t("knowledge-knowledgemap.topics_about_title")}>{t("knowledge-knowledgemap.topics_about")}</Callout>
      {failed > 0 && <p className="mb-3 text-[12px] text-orange">{t("knowledge-knowledgemap.failed", { count: failed })}</p>}
      <KindCounts points={points} />
      {root.kids.map((k) => (
        <button key={k.id} type="button" onClick={() => onOpen(k)} className="flex w-full items-center gap-2 border-t border-line py-2 text-left text-[12.5px] text-ink hover:text-accent-ink">
          <Icon name="light" size={13} stroke={2} className="shrink-0 text-ink-3" /><span className="truncate">{k.title}</span>
          <span className="ml-auto shrink-0 text-[11px] text-ink-3">{t("knowledge-knowledgemap.points", { count: Number(k.meta) || 0 })}</span>
        </button>
      ))}
    </div>
  );
}

function TopicPanel({ node, crumbs, onOpen }: { node: MapNode; crumbs: string[]; onOpen: (n: MapNode) => void }) {
  const points = pointsUnder(node);
  return (
    <div className="p-5">
      <h2 className="text-[18px] font-semibold leading-snug text-ink">{node.title}</h2>
      <div className="mt-0.5 text-[12px] text-ink-3">{crumbs.join(" › ") || t("knowledge-knowledgemap.topic")}</div>
      {node.body && <div className="mt-3"><Callout title={t("knowledge-knowledgemap.topic_about")}>{node.body}</Callout></div>}
      <KindCounts points={points} />
      {node.open && <OpenButton open={node.open} />}
      {node.kids.filter((k) => k.isTopic).map((k) => (
        <button key={k.id} type="button" onClick={() => onOpen(k)} className="mt-2 flex w-full items-center gap-2 border-t border-line py-2 text-left text-[12.5px] text-ink hover:text-accent-ink">
          <Icon name="book" size={13} stroke={2} className="shrink-0 text-ink-3" /><span className="truncate">{k.title}</span>
          <span className="ml-auto shrink-0 text-[11px] text-ink-3">{t("knowledge-knowledgemap.points", { count: Number(k.meta) || 0 })}</span>
        </button>
      ))}
      <ul className="mt-3 space-y-2.5">
        {node.kids.filter((k) => k.point).map((k) => (
          <li key={k.id}>
            <button type="button" onClick={() => onOpen(k)} className="w-full text-left text-[12.5px] leading-relaxed text-ink hover:text-accent-ink">
              <span className="mr-1.5 inline-flex align-[1px]"><TonePill tone={pointWarns(k.point!.kind) ? "orange" : "neutral"}>{pointKindLabel(k.point!.kind)}</TonePill></span>
              {k.title}
            </button>
            {k.point!.why && <div className="mt-0.5 pl-1 text-[12px] leading-relaxed text-ink-3">{k.point!.why}</div>}
          </li>
        ))}
      </ul>
    </div>
  );
}

function PointPanel({ node, crumbs }: { node: MapNode; crumbs: string[] }) {
  const p = node.point!;
  const sources = pointSources(p, node.links);
  return (
    <div className="p-5">
      <div className="mb-2 flex flex-wrap gap-1.5">
        <TonePill tone={pointWarns(p.kind) ? "orange" : "neutral"} strong>{pointKindLabel(p.kind)}</TonePill>
        {p.when && <TonePill>{t("knowledge-knowledgemap.confirmed", { day: pointDay(p.when) })}</TonePill>}
      </div>
      <h2 className="text-[16px] font-semibold leading-snug text-ink">{p.text}</h2>
      <div className="mt-0.5 mb-3 text-[12px] text-ink-3">{crumbs.join(" › ")}</div>
      {p.why && <Callout title={t(p.kind === "pitfall" ? "knowledge-knowledgemap.cause" : "knowledge-knowledgemap.reason")}>{p.why}</Callout>}
      {sources.length > 0 && (
        <section>
          <SectionTitle title={t("knowledge-knowledgemap.sources")} count={sources.length} />
          <Card>
            {sources.map((s) => (
              <button key={`${s.kind}/${s.id}`} type="button" onClick={() => navigate({ name: "kb", kind: s.kind, id: s.id })}
                className="flex w-full items-center gap-2 px-4 py-2.5 text-left text-[12.5px] text-ink hover:bg-hover">
                <Icon name={s.kind === "chats" ? "chat" : "layers"} size={13} stroke={2} className="shrink-0 text-ink-3" />
                <span className="min-w-0 flex-1 truncate">{s.name}</span>
                <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
              </button>
            ))}
          </Card>
        </section>
      )}
    </div>
  );
}

function ProjectsRootPanel({ root, failed }: { root: MapNode; failed: number }) {
  const projects: MapNode[] = [];
  walk(root, (n) => { if (n.kind === "project") projects.push(n); });
  const notes = projects.reduce((s, p) => s + (p.lines?.length ?? 0), 0);
  return (
    <div className="p-5">
      <h2 className="text-[18px] font-semibold text-ink">{t("knowledge-knowledgemap.projects_title")}</h2>
      <Kpis items={[[projects.filter((p) => p.status !== "done" && p.status !== "paused").length, t("knowledge-knowledgemap.kpi_active")], [projects.filter((p) => p.summary?.length).length, t("knowledge-knowledgemap.kpi_summaries")], [notes, t("knowledge-knowledgemap.kpi_notes")]]} />
      <Callout title={t("knowledge-knowledgemap.projects_about_title")}>{t("knowledge-knowledgemap.projects_about")}</Callout>
      {failed > 0 && <p className="text-[12px] text-orange">{t("knowledge-knowledgemap.failed", { count: failed })}</p>}
    </div>
  );
}

function ProjectPanel({ node, crumbs }: { node: MapNode; crumbs: string[] }) {
  const lines = node.lines ?? [];
  const goalNode = node.kids.find((k) => k.goal);
  const goal = goalNode?.kids.map((k) => k.title).join(" ");
  const sections = (node.summary ?? []).filter((x) => x.title !== goalNode?.title);
  return (
    <div className="p-5">
      <h2 className="text-[18px] font-semibold leading-snug text-ink">{node.title}</h2>
      <div className="mt-0.5 text-[12px] text-ink-3">{crumbs.join(" › ")}</div>
      <div className="mt-2 mb-3 flex flex-wrap gap-1.5">
        {(node.meta ?? "").split(" · ").filter(Boolean).map((x) => <TonePill key={x}>{x}</TonePill>)}
        {node.noGoal && <TonePill tone="orange">{t("knowledge-knowledgemap.no_goal")}</TonePill>}
      </div>
      {goal && <Callout title={t("knowledge-knowledgemap.goal")}>{goal}</Callout>}
      {sections.map((sec) => (
        <section key={sec.title} className="mt-4">
          <SectionTitle title={sec.title} />
          <Items items={sec.items} />
        </section>
      ))}
      {!node.summary?.length && <p className="mt-3 text-[12px] text-ink-3">{t("knowledge-knowledgemap.no_summary")}</p>}
      <section className="mt-4">
        <SectionTitle title={t("knowledge-knowledgemap.main_chat_notes")} count={lines.length} />
        {lines.length ? (
          <ol className="mt-1.5 ml-1.5 border-l-2 border-line pl-4">
            {lines.slice(0, 10).map((l, i) => (
              <li key={i} className="relative pb-3 text-[12.5px] leading-relaxed text-ink">
                <span className="absolute top-1.5 -left-[23px] size-2.5 rounded-full bg-accent shadow-[0_0_0_3px_var(--surface)]" />
                <div className="text-[11px] text-ink-3 tabular-nums">{l.when.slice(5)}</div>
                {l.text}
              </li>
            ))}
          </ol>
        ) : <p className="text-[12px] text-ink-3">{t("knowledge-knowledgemap.no_notes")}</p>}
      </section>
      {node.open && <OpenButton open={node.open} />}
    </div>
  );
}

function Items({ items }: { items: SummaryItem[] }) {
  return (
    <ul className="ml-4 list-disc space-y-1 text-[12.5px] leading-relaxed text-ink marker:text-ink-3">
      {items.map((i, k) => (
        <li key={k}>{i.text}{i.kids.length > 0 && <div className="mt-1"><Items items={i.kids} /></div>}</li>
      ))}
    </ul>
  );
}

function Callout({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="mb-4 rounded-[12px] px-3.5 py-3 text-[13px] leading-relaxed text-ink" style={{ background: tint("var(--accent)", 10) }}>
      <div className="mb-0.5 text-[11px] font-semibold tracking-wide text-accent-ink">{title}</div>
      {children}
    </div>
  );
}

function Kpis({ items }: { items: [number, string][] }) {
  return (
    <div className="my-4 grid grid-cols-3 gap-2">
      {items.map(([v, l]) => (
        <div key={l} className="rounded-[12px] bg-inset px-3 py-2.5">
          <div className="text-[20px] leading-tight font-semibold tabular-nums text-ink">{v}</div>
          <div className="mt-0.5 text-[11.5px] text-ink-3">{l}</div>
        </div>
      ))}
    </div>
  );
}
