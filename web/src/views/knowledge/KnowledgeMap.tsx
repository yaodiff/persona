// Knowledge map (/kb/map): the knowledge base as a two-sided mind map (lib/kb-map.ts) in two views — the curated topics
// (topics → subtopics → knowledge points, docs/assistant/knowledge.md "Knowledge points") and the projects (what the
// main chat keeps on each, with the curated summary as sections) — and a side panel on what is selected. Pinch to zoom
// (trackpad, Safari gestures, two fingers); a tap selects, a double tap or the round toggle opens and closes.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { t } from "../../i18n";
import { api, enc, errorText, isAbort } from "../../lib/api";
import { BASE } from "../../lib/base";
import { type KBDetail, type KBDetailResponse, type KBEntry, type KBList } from "../../lib/kb";
import { pointDay, pointSources, pointWarns, POINT_KINDS, pointKindLabel } from "../../lib/kb-points";
import { buildProjectMap, buildTopicMap, layout, pointsUnder, walk, type Box, type MapNode, type Placed, type SummaryItem, type TopicData } from "../../lib/kb-map";
import { navigate } from "../../lib/router";
import { onHubEvent } from "../../lib/store";
import { Button, cx, IconButton, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { settingsHref } from "../settings/nav";
import { ErrorBox } from "./KBList";
import { Card, SectionTitle, TonePill } from "./KBParts";

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
function wrap(s: string, px: number, max: number, lines: number): string[] {
  const out: string[] = [];
  let l = "";
  for (const ch of s) {
    if (tw(l + ch, px) > max && l) {
      out.push(l);
      l = ch;
      if (out.length === lines) break;
    } else l += ch;
  }
  if (out.length < lines) out.push(l);
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
type View = "topics" | "projects";
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
  const [state, setState] = useState<Record<View, Load>>({ topics: null, projects: null });
  const [refreshes, setRefreshes] = useState(0);
  const started = useRef("");
  useEffect(() => {
    const ac = new AbortController();
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

// ── pinch to zoom ───────────────────────────────────────
// A trackpad pinch arrives as ctrl+wheel in Chrome / Firefox and as gesture events in Safari; on a touch screen it is two
// pointers. Every route zooms around the fingers: the point under them stays put once the new size is drawn.
const ZMIN = 0.3, ZMAX = 2.5;
// x / y: the fingers in the view; sx / sy: the scroll then; px / py: where the map starts inside the scrolled content
// (the room around it does not scale).
type Anchor = { x: number; y: number; sx: number; sy: number; px: number; py: number; from: number };

function usePinchZoom(host: RefObject<HTMLDivElement | null>, zoom: number, setZoom: (z: number) => void, enabled: boolean) {
  const current = useRef(zoom);
  current.current = zoom;
  const anchor = useRef<Anchor | null>(null);
  const zoomAt = useCallback((next: number, clientX?: number, clientY?: number) => {
    const el = host.current;
    if (!el) return;
    const z = Math.min(ZMAX, Math.max(ZMIN, next));
    if (Math.abs(z - current.current) < 0.001) return;
    const r = el.getBoundingClientRect();
    const x = (clientX ?? r.left + r.width / 2) - r.left, y = (clientY ?? r.top + r.height / 2) - r.top;
    if (!anchor.current) {
      const m = el.querySelector("svg[data-map]")?.getBoundingClientRect() ?? r;
      anchor.current = { x, y, sx: el.scrollLeft, sy: el.scrollTop, px: m.left - r.left + el.scrollLeft, py: m.top - r.top + el.scrollTop, from: current.current };
    }
    current.current = z;
    setZoom(z);
  }, [host, setZoom]);
  useLayoutEffect(() => {
    const el = host.current, a = anchor.current;
    if (!el || !a) return;
    anchor.current = null;
    const k = zoom / a.from;
    el.scrollLeft = a.px + (a.sx + a.x - a.px) * k - a.x;
    el.scrollTop = a.py + (a.sy + a.y - a.py) * k - a.y;
  }, [host, zoom]);
  useEffect(() => {
    const el = host.current;
    if (!el || !enabled) return;
    const wheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      zoomAt(current.current * Math.exp(-e.deltaY * 0.01), e.clientX, e.clientY);
    };
    type Gesture = Event & { scale: number; clientX: number; clientY: number };
    let base = 1;
    const gStart = (e: Event) => { e.preventDefault(); base = current.current; };
    const gChange = (e: Event) => { e.preventDefault(); const g = e as Gesture; zoomAt(base * g.scale, g.clientX, g.clientY); };
    const touches = new Map<number, { x: number; y: number }>();
    let pinch: { d: number; z: number } | null = null;
    const spread = () => { const [a, b] = [...touches.values()]; return { d: Math.hypot(a.x - b.x, a.y - b.y), mx: (a.x + b.x) / 2, my: (a.y + b.y) / 2 }; };
    const down = (e: PointerEvent) => {
      if (e.pointerType !== "touch") return;
      touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (touches.size === 2) pinch = { d: spread().d || 1, z: current.current };
    };
    const move = (e: PointerEvent) => {
      if (!touches.has(e.pointerId)) return;
      touches.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (pinch && touches.size === 2) { const s = spread(); zoomAt(pinch.z * (s.d / pinch.d), s.mx, s.my); }
    };
    const up = (e: PointerEvent) => { touches.delete(e.pointerId); if (touches.size < 2) pinch = null; };
    el.addEventListener("wheel", wheel, { passive: false });
    el.addEventListener("gesturestart", gStart);
    el.addEventListener("gesturechange", gChange);
    el.addEventListener("pointerdown", down);
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
    return () => {
      el.removeEventListener("wheel", wheel);
      el.removeEventListener("gesturestart", gStart);
      el.removeEventListener("gesturechange", gChange);
      el.removeEventListener("pointerdown", down);
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
    };
  }, [host, enabled, zoomAt]);
  return zoomAt;
}

// ── page ────────────────────────────────────────────────
const VIEWS: View[] = ["topics", "projects"];

export function KnowledgeMap({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const [view, setView] = useState<View>("topics");
  const [reload, setReload] = useState(0);
  const [load, refresh] = useMapData(view, reload);
  // A map that could not be read while the hub was away, or only in part (some entries' details failed), is read again
  // once it is back (its hello), quietly: what is drawn stays until the new read lands. A map read whole is not, since
  // reading it again means every entry's detail.
  const failed = Boolean(load && ("error" in load || load.failed > 0));
  useEffect(() => (failed ? onHubEvent("hello", () => refresh()) : undefined), [failed, refresh]);
  const [sel, setSel] = useState("");
  const [, bump] = useState(0);
  const [zoom, setZoom] = useState(1);
  const host = useRef<HTMLDivElement>(null);
  const root = load && "root" in load && !load.empty ? load.root : null;
  const zoomAt = usePinchZoom(host, zoom, setZoom, Boolean(root));
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
    setZoom(1);
    setView(v);
  };
  const toggle = (n: MapNode) => {
    n.collapsed = !n.collapsed;
    bump((x) => x + 1);
  };
  const reveal = (n: MapNode) => {
    for (let p = parents.get(n.id); p; p = parents.get(p.id)) p.collapsed = false;
    setSel(n.id);
    bump((x) => x + 1);
  };
  const selected = (sel && byId.get(sel)) || root;

  return (
    <div className="flex h-full min-w-0 flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("knowledge-knowledgemap.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <IconButton icon="chevronLeft" label={t("knowledge-knowledgemap.back")} onClick={() => navigate({ name: "kb", kind: "topics" })} />
        <h1 className="truncate text-[14px] font-semibold text-ink">{t("knowledge-knowledgemap.title")}</h1>
        <span className="hidden min-w-0 truncate text-[12px] text-ink-3 md:inline">{t(view === "topics" ? "knowledge-knowledgemap.subtitle_topics" : "knowledge-knowledgemap.subtitle_projects")}</span>
        <div className="flex-1" />
        <div role="tablist" aria-label={t("knowledge-knowledgemap.views")} className="inline-flex shrink-0 rounded-control bg-inset p-0.5 shadow-[inset_0_0_0_1px_var(--line)]">
          {VIEWS.map((v) => (
            <button key={v} type="button" role="tab" aria-selected={view === v} onClick={() => pick(v)}
              className={cx("h-7 whitespace-nowrap rounded-[7px] px-3 text-[12.5px]", view === v ? "bg-surface font-semibold text-ink shadow-btn" : "text-ink-2 hover:text-ink")}>
              {t(v === "topics" ? "knowledge-knowledgemap.view_topics" : "knowledge-knowledgemap.view_projects")}
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
            <div ref={host} className="absolute inset-0 overflow-auto bg-canvas"
              style={{ backgroundImage: "radial-gradient(var(--line-strong) 1px, transparent 1px)", backgroundSize: "22px 22px", touchAction: "pan-x pan-y", overflowAnchor: "none" }}>
              <MapCanvas key={view} root={load.root} sel={selected?.id ?? ""} zoom={zoom} onFit={setZoom} onSelect={(n) => setSel(n.id)} onToggle={toggle} />
            </div>
            <div className="absolute bottom-3 left-3 z-10 inline-flex items-center gap-0.5 rounded-control bg-surface p-1 text-[12px] shadow-overlay">
              <IconButton icon="minus" label={t("knowledge-knowledgemap.zoom_out")} size={26} iconSize={12} onClick={() => zoomAt(zoom - 0.1)} />
              <span className="min-w-10 text-center tabular-nums text-ink-3" aria-live="polite" aria-label={t("knowledge-knowledgemap.zoom_level", { percent: Math.round(zoom * 100) })}>{Math.round(zoom * 100)}%</span>
              <IconButton icon="plus" label={t("knowledge-knowledgemap.zoom_in")} size={26} iconSize={12} onClick={() => zoomAt(zoom + 0.1)} />
            </div>
          </div>
          <aside aria-label={t("knowledge-knowledgemap.details")} className="hidden w-[380px] shrink-0 overflow-y-auto border-l border-line bg-surface lg:block">
            {selected && <Panel node={selected} root={load.root} parents={parents} failed={load.failed} onOpen={reveal} />}
          </aside>
        </div>
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
      <p className="text-[13.5px] font-medium text-ink">{t(view === "projects" ? "knowledge-knowledgemap.empty_projects" : off ? "knowledge-knowledgemap.empty_off" : "knowledge-knowledgemap.empty_topics")}</p>
      <p className="mt-1 max-w-[380px] text-[12.5px] leading-relaxed text-ink-3">
        {t(view === "projects" ? "knowledge-knowledgemap.empty_projects_body" : off ? "knowledge-knowledgemap.empty_off_body" : "knowledge-knowledgemap.empty_topics_body")}
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

function MapCanvas({ root, sel, zoom, onFit, onSelect, onToggle }: { root: MapNode; sel: string; zoom: number; onFit: (z: number) => void; onSelect: (n: MapNode) => void; onToggle: (n: MapNode) => void }) {
  const L = layout(root, measure);
  const at = new Map<MapNode, Placed>(L.placed.map((p) => [p.node, p]));
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
  const key = (n: MapNode) => (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onSelect(n);
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
    nodes.push(
      <g key={n.id} role="button" tabIndex={0} aria-label={spoken(n)} aria-pressed={n.id === sel} aria-expanded={n.kids.length && p.depth > 0 ? !n.collapsed : undefined}
        className="outline-none [&:focus-visible>g]:[filter:drop-shadow(0_0_0.5px_var(--accent))_drop-shadow(0_0_2px_var(--accent))]"
        style={{ ...MOVE, transform: `translate(${p.x}px,${p.y - p.box.h / 2}px)`, cursor: "pointer" }}
        onClick={() => onSelect(n)} onDoubleClick={() => n.kids.length && onToggle(n)} onKeyDown={key(n)}>
        <NodeShape n={n} p={p} selected={n.id === sel} />
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
    // Half a screen of room on every side, so a pinch can keep any point under the fingers even when the map is smaller than the view.
    <div className="w-max" style={{ padding: "50vh 50vw" }}>
      <svg ref={box} data-map="" role="group" aria-label={t("knowledge-knowledgemap.map_label", { name: root.title })}
        width={L.width * zoom} height={L.height * zoom} viewBox={`0 0 ${L.width} ${L.height}`} className="block">
        {edges}
        {nodes}
      </svg>
    </div>
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
          <g transform={`translate(${w / 2 - 11},${h / 2 - 36})`} color="var(--surface)"><Icon name={n.view === "topics" ? "light" : "layers"} size={22} stroke={2} /></g>
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
          <g transform={`translate(13,${h / 2 - 9})`} color="var(--surface)"><Icon name={n.isTopic ? "light" : n.tone === "green" ? "check" : n.tone === "orange" ? "pause" : "folder"} size={18} stroke={2.2} /></g>
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
function Panel({ node, root, parents, failed, onOpen }: { node: MapNode; root: MapNode; parents: Map<string, MapNode | null>; failed: number; onOpen: (n: MapNode) => void }) {
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
}

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
