// The disk usage map (#14576, docs/server/machine-usage-map.md "The map", "Low-disk guidance", "Clients"): one
// computer's disk as a nested, drill-down treemap on the left and what is already offloaded on the right, on one scale.
// This component owns the data (the overview, the folders opened, polling while the node scans, the providers and
// their inventories, the selection) and the flows (Move to Trash → Undo / Delete Permanently, Offload with its install
// step, Restore, Clean cache through the existing cleanup). The pure rules are in lib/usage-map.ts; the boxes in
// UsageTreemap, the ribbons in UsageMapChannel, the actions under the map in UsageMapTray.
//
// The scan is the node's work (up to two minutes for the overview), so like the hotspot scan below it starts when the
// user asks; focus=reclaim (a disk alert's "See what's using space") asks at once.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { cleanup } from "../../lib/machine-diagnosis";
import { bytesBin, percent } from "../../lib/machine-detail";
import { isAbort, errorText } from "../../lib/api";
import type { Machine } from "../../lib/types";
import {
  actionable,
  ageStep,
  cancelJob,
  cloudTree,
  fetchInventory,
  fetchJob,
  fetchProviders,
  findNode,
  layoutBoxes,
  linkIndex,
  outermost,
  parentPath,
  pickForGoal,
  pollUsage,
  purgeTrash,
  reclaimCandidates,
  removePaths,
  runOffload,
  scaleChip,
  spliceNode,
  splitScale,
  suggestReclaim,
  trailTo,
  trashPaths,
  undoTrash,
  usageFailure,
  whatIf,
  withFree,
  zoomable,
  type MapBox,
  type OffloadInventory,
  type OffloadItem,
  type OffloadJob,
  type OffloadProvider,
  type OffloadTool,
  type ReclaimCandidate,
  type TrashResult,
  type UsageFailure,
  type UsageNode,
  type UsageReport,
  type UsageVolume,
} from "../../lib/usage-map";
import { Button, copyText, cx, Menu, type MenuItem } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Segmented } from "../assistant/parts";
import { CleanDialog, InstallDialog, OffloadDialog, PurgeDialog, RestoreDialog, sumBytes, TrashDialog } from "./UsageMapDialogs";
import { cacheTitle, cleanupStatusText, LegacyCacheRecovery } from "./LegacyCacheRecovery";
import { UsageMapChannel, type Ribbon } from "./UsageMapChannel";
import { UsageMapCloud, type CloudState } from "./UsageMapCloud";
import { cloudMenu, localMenu, refusalText } from "./UsageMapMenu";
import { UsageMapTray, type TrayJob, type TrayNotice } from "./UsageMapTray";
import { UsageTreemap, type Tint } from "./UsageTreemap";
import "./usage-map.css";

export interface UsageFocus {
  mode: "reclaim";
  /** Changes on every request, so asking again re-applies it. */
  seq: number;
}

const CHANNEL = 24;
const MIN_CLOUD = 120;
const CLOUD_EMPTY_W = 168;
const CLOUD_KINDS = new Set(["file", "dir", "app", "app_data", "bundle", "session", "other"]);
const providerKey = (p: Pick<OffloadProvider, "extension" | "id">) => `${p.extension}/${p.id}`;
const SEP = "\u0001";

type Dialog =
  | { kind: "trash"; nodes: UsageNode[] }
  | { kind: "purge" }
  | { kind: "offload"; nodes: UsageNode[]; provider: OffloadProvider; refused: { path?: string; reason: string }[] }
  | { kind: "install"; action: "offload" | "restore"; provider: OffloadProvider; tool: OffloadTool; paths?: string[]; items?: string[]; nodes: UsageNode[] }
  | { kind: "restore"; nodes: UsageNode[]; provider: OffloadProvider }
  | { kind: "clean"; nodes: UsageNode[] };

interface Job {
  provider: OffloadProvider;
  id: string;
  action: "offload" | "restore";
  paths: string[];
  state: OffloadJob | null;
  error?: string;
}

/** A fixed box's name, by its key; a folder's own name otherwise. */
export function nodeName(n: UsageNode): string {
  if (n.id.startsWith("cloud-group:")) {
    if (n.id === "cloud-group:more") return t("machines-usagemap.cloud_more", { count: n.items ?? 0 });
    return CLOUD_KINDS.has(n.name) ? t(`machines-usagemap.cloud_kind_${n.name}`) : n.name;
  }
  if (n.kind === "other" && n.items) return t("machines-usagemap.kind_other", { count: n.items });
  if (n.key && !n.name) {
    switch (n.key) {
      case "apps":
      case "home":
      case "appdata":
      case "system":
      case "free":
      case "unmeasured":
      case "bundle":
      case "cache":
      case "data":
        return t(`machines-usagemap.kind_${n.key}`);
      default:
        return t("machines-usagemap.cat_other");
    }
  }
  if (n.kind === "root") return t("machines-usagemap.crumb_disk");
  if (n.kind === "unmeasured") return t("machines-usagemap.kind_unmeasured");
  if (n.kind === "free") return t("machines-usagemap.kind_free");
  return n.name || n.display || n.path || t("machines-usagemap.cat_other");
}

/** An app's part ("Cache") is said with its app ("Example · Cache"). */
function fullName(n: UsageNode, parent: UsageNode | null): string {
  const own = nodeName(n);
  return (n.key === "bundle" || n.key === "cache" || n.key === "data") && parent?.kind === "app" ? `${nodeName(parent)} · ${own}` : own;
}

function categoryText(n: UsageNode): string {
  if (n.kind === "unmeasured") return t("machines-usagemap.cat_unmeasured");
  switch (n.category) {
    case "apps":
    case "personal":
    case "appdata":
    case "cache":
    case "system":
    case "free":
      return t(`machines-usagemap.cat_${n.category}`);
    default:
      return t("machines-usagemap.cat_other");
  }
}

function ageText(usedAt: number | undefined, now: number): string {
  const step = ageStep(usedAt, now);
  return step ? t(`machines-usagemap.age_${step}`) : "";
}

const share = (n: UsageNode, of: UsageNode | null) => (of && of.bytes > 0 ? percent((100 * n.bytes) / of.bytes) : "");

function useWidth(ref: React.RefObject<HTMLElement | null>): number {
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const read = () => setW(Math.floor(el.getBoundingClientRect().width));
    read();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(read);
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return w || 640;
}

export function UsageMap({ machine, focus, pollMs = 1000 }: { machine: Machine; focus?: UsageFocus; pollMs?: number }) {
  const mid = machine.id;
  const [started, setStarted] = useState(false);
  const [tree, setTree] = useState<UsageNode | null>(null);
  const [volume, setVolume] = useState<UsageVolume | undefined>();
  const [home, setHome] = useState("");
  const [scanning, setScanning] = useState(false);
  const [partial, setPartial] = useState(false);
  const [failure, setFailure] = useState<UsageFailure | null>(null);
  const [trail, setTrail] = useState<string[]>([]);
  const [loading, setLoading] = useState<Set<string>>(new Set());
  const [providers, setProviders] = useState<OffloadProvider[] | null>(null);
  const [inventories, setInventories] = useState<Map<string, CloudState>>(new Map());
  const [cloudTrail, setCloudTrail] = useState<string[]>([]);
  const [cloudFilter, setCloudFilter] = useState<{ path: string; name: string } | null>(null);
  const [selection, setSelection] = useState<string[]>([]);
  const [cloudSelection, setCloudSelection] = useState<string[]>([]);
  const [hover, setHover] = useState<{ side: "local" | "cloud"; box: MapBox } | null>(null);
  const [tint, setTint] = useState<Tint>("kind");
  const [goal, setGoal] = useState(0);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialogError, setDialogError] = useState("");
  const [trashed, setTrashed] = useState<{ result: TrashResult; ids: string[]; bytes: number } | null>(null);
  const [notice, setNotice] = useState<{ text: string; detail?: string; items?: TrayNotice["items"] } | null>(null);
  const [job, setJob] = useState<Job | null>(null);
  const [menu, setMenu] = useState<{ items: MenuItem[]; point: { x: number; y: number }; header: string } | null>(null);
  const [dragging, setDragging] = useState<{ side: "local" | "cloud"; node: UsageNode } | null>(null);
  const [zoomKey, setZoomKey] = useState(0);
  const [focusTick, setFocusTick] = useState(0);
  const [cloudFocusTick, setCloudFocusTick] = useState(0);
  const [reclaim, setReclaim] = useState(false);
  const [status, setStatus] = useState("");
  const [now] = useState(() => Date.now());

  const wrap = useRef<HTMLDivElement>(null);
  const width = useWidth(wrap);
  const ctrl = useRef<AbortController | null>(null);
  const folders = useRef(new Map<string, UsageNode>());
  const midRef = useRef(mid);
  midRef.current = mid;

  // another computer: everything starts over
  useEffect(() => {
    ctrl.current?.abort();
    ctrl.current = new AbortController();
    folders.current = new Map();
    setStarted(false);
    setTree(null);
    setVolume(undefined);
    setHome("");
    setScanning(false);
    setPartial(false);
    setFailure(null);
    setTrail([]);
    setLoading(new Set());
    setProviders(null);
    setInventories(new Map());
    setCloudTrail([]);
    setCloudFilter(null);
    setSelection([]);
    setCloudSelection([]);
    setDialog(null);
    setTrashed(null);
    setNotice(null);
    setJob(null);
    setReclaim(false);
    return () => ctrl.current?.abort();
  }, [mid]);

  const signal = () => ctrl.current?.signal;

  /** The overview again (each answer replaces it; the folders opened so far are put back in). */
  const mergeOverview = useCallback((r: UsageReport) => {
    setFailure(null);
    setVolume(r.volume);
    if (r.home) setHome(r.home);
    setPartial(r.partial);
    let next = withFree(r.node, r.volume);
    for (const f of [...folders.current.values()].sort((a, b) => (a.path ?? "").length - (b.path ?? "").length)) if (findNode(next, f.id)) next = spliceNode(next, f);
    setTree(next);
  }, []);

  const loadOverview = useCallback(async () => {
    setScanning(true);
    setFailure(null);
    try {
      await pollUsage(mid, "", { signal: signal(), intervalMs: pollMs, onReport: mergeOverview });
    } catch (e) {
      if (isAbort(e) || midRef.current !== mid) return;
      setFailure(usageFailure(e));
    } finally {
      if (midRef.current === mid) setScanning(false);
    }
  }, [mid, mergeOverview, pollMs]);

  /** One folder (opened, or asked again after a change), spliced into the map as its answers come. */
  const loadFolder = useCallback(
    async (id: string, path: string, rescan = false) => {
      setLoading((s) => new Set(s).add(id));
      try {
        await pollUsage(mid, path, {
          rescan,
          signal: signal(),
          intervalMs: pollMs,
          onReport: (r) => {
            if (r.volume) setVolume(r.volume);
            const fresh = { ...r.node, id };
            folders.current.set(id, fresh);
            setTree((t) => (t ? withFree(spliceNode(t, fresh), r.volume) : t));
          },
        });
      } catch (e) {
        if (isAbort(e)) return;
        setStatus(usageFailure(e).text);
      } finally {
        setLoading((s) => {
          const n = new Set(s);
          n.delete(id);
          return n;
        });
      }
    },
    [mid, pollMs],
  );

  const loadInventories = useCallback(
    async (list: readonly OffloadProvider[], refresh = false) => {
      for (const p of list) {
        const key = providerKey(p);
        setInventories((m) => new Map(m).set(key, { state: m.get(key)?.inv ? "ok" : "loading", inv: m.get(key)?.inv }));
        fetchInventory(p, mid, { refresh, signal: signal() })
          .then((inv) => {
            if (midRef.current !== mid) return;
            setInventories((m) => new Map(m).set(key, { state: "ok", inv: { ...inv, items: inv.items ?? [] } }));
          })
          .catch((e) => {
            if (isAbort(e) || midRef.current !== mid) return;
            setInventories((m) => new Map(m).set(key, { state: "failed", text: usageFailure(e).text, inv: m.get(key)?.inv }));
          });
      }
    },
    [mid],
  );

  const start = useCallback(() => {
    if (!machine.online) return;
    setStarted(true);
    void loadOverview();
    fetchProviders(mid, signal())
      .then((list) => {
        if (midRef.current !== mid) return;
        setProviders(list);
        void loadInventories(list);
      })
      .catch((e) => {
        if (isAbort(e) || midRef.current !== mid) return;
        setProviders([]);
      });
  }, [machine.online, mid, loadOverview, loadInventories]);

  // focus=reclaim: start at once and mark what to free
  useEffect(() => {
    if (!focus || focus.mode !== "reclaim" || !machine.online) return;
    setReclaim(true);
    if (!started) start();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus?.seq, machine.online]);

  // ── what is on screen ──

  const trailNodes = useMemo(() => {
    if (!tree) return [];
    const last = [...trail].reverse().find((id) => findNode(tree, id));
    return (last && trailTo(tree, last)) || [tree];
  }, [tree, trail]);
  const focusNode = trailNodes[trailNodes.length - 1] ?? null;

  const provList = useMemo(() => providers ?? [], [providers]);
  const itemProvider = useMemo(() => {
    const m = new Map<string, { provider: OffloadProvider; item: OffloadItem }>();
    for (const p of provList) for (const it of inventories.get(providerKey(p))?.inv?.items ?? []) m.set(`${providerKey(p)}${SEP}${it.id}`, { provider: p, item: it });
    return m;
  }, [provList, inventories]);
  const allItems = useMemo(() => [...itemProvider.entries()].map(([id, v]) => ({ ...v.item, id })), [itemProvider]);
  const links = useMemo(() => linkIndex(allItems), [allItems]);
  const cloudTotal = useMemo(() => {
    let s = 0;
    for (const p of provList) {
      const inv = inventories.get(providerKey(p))?.inv;
      if (inv) s += inv.total_bytes || inv.items.reduce((a, i) => a + i.bytes, 0) + (inv.more?.bytes ?? 0);
    }
    return s;
  }, [provList, inventories]);

  const selectedNodes = useMemo(() => (tree ? selection.map((id) => findNode(tree, id)).filter((n): n is UsageNode => !!n) : []), [tree, selection]);
  const preview = useMemo(() => {
    const list = [...selectedNodes];
    if (dragging?.side === "local" && !list.some((n) => n.id === dragging.node.id)) list.push(dragging.node);
    return list;
  }, [selectedNodes, dragging]);
  const what = useMemo(() => (preview.length ? whatIf(volume, preview) : null), [volume, preview]);
  // the "what if" preview: the free strip grows by what the selection would return, the picked boxes turn to ghosts
  const shownFocus = useMemo(() => {
    const freeAt = focusNode?.children?.findIndex((c) => c.key === "free") ?? -1;
    if (!focusNode || !what || what.freed <= 0 || freeAt < 0) return focusNode;
    const children = focusNode.children!.slice();
    children[freeAt] = { ...children[freeAt], bytes: children[freeAt].bytes + what.freed };
    return { ...focusNode, children, bytes: focusNode.bytes + what.freed };
  }, [focusNode, what]);
  const pendingTwins = useMemo(() => (provList.length ? outermost(preview.filter((n) => n.can_offload)) : []), [preview, provList]);

  const cloudRoot = useMemo(() => {
    const filtered = cloudFilter ? links.under(cloudFilter.path) : allItems;
    const merged: OffloadInventory = { state: "ready", total_bytes: 0, items: filtered };
    let more = { count: 0, bytes: 0 };
    if (!cloudFilter) for (const p of provList) {
      const m = inventories.get(providerKey(p))?.inv?.more;
      if (m) more = { count: more.count + m.count, bytes: more.bytes + m.bytes };
    }
    if (more.bytes) merged.more = more;
    return cloudTree(merged, pendingTwins);
  }, [allItems, links, cloudFilter, provList, inventories, pendingTwins]);
  const cloudTrailNodes = useMemo(() => {
    const last = [...cloudTrail].reverse().find((id) => findNode(cloudRoot, id));
    return (last && trailTo(cloudRoot, last)) || [cloudRoot];
  }, [cloudRoot, cloudTrail]);
  const cloudFocus = cloudTrailNodes[cloudTrailNodes.length - 1];
  const cloudSelectedNodes = useMemo(() => cloudSelection.map((id) => findNode(cloudRoot, id)).filter((n): n is UsageNode => !!n && !!n.cloud), [cloudRoot, cloudSelection]);

  // ── geometry ──

  const stacked = width < 640;
  const hasCloudSide = provList.length > 0 || providers !== null;
  const geo = useMemo(() => {
    const total = volume?.total_bytes ?? tree?.bytes ?? 0;
    const cloud = cloudTotal + sumBytes(pendingTwins);
    if (stacked) {
      const localH = Math.round(Math.min(440, Math.max(300, width * 1.05)));
      if (!cloud) return { local: { w: width, h: localH }, cloud: { x: 0, y: localH + CHANNEL, w: width, h: 0 }, ratio: 1, rowH: localH };
      const s = splitScale(total, cloud, localH, width, 80, 0.45);
      const lh = Math.round(s.local.w);
      return { local: { w: width, h: lh }, cloud: { x: 0, y: lh + CHANNEL, w: Math.round(s.cloud.h), h: Math.round(s.cloud.w) }, ratio: s.ratio, rowH: lh };
    }
    const h = Math.round(Math.min(520, Math.max(260, (width * 9) / 16)));
    if (!hasCloudSide) return { local: { w: width, h }, cloud: { x: width, y: 0, w: 0, h: 0 }, ratio: 1, rowH: h };
    const avail = width - CHANNEL;
    if (!cloud) return { local: { w: avail - CLOUD_EMPTY_W, h }, cloud: { x: avail - CLOUD_EMPTY_W + CHANNEL, y: 0, w: CLOUD_EMPTY_W, h: 0 }, ratio: 1, rowH: h };
    const s = splitScale(total, cloud, avail, h, MIN_CLOUD, 0.45);
    const lw = Math.round(s.local.w);
    return { local: { w: lw, h }, cloud: { x: lw + CHANNEL, y: 0, w: Math.round(s.cloud.w), h: Math.round(s.cloud.h) }, ratio: s.ratio, rowH: h };
  }, [volume, tree, cloudTotal, pendingTwins, stacked, width, hasCloudSide]);

  const maxDepth = stacked ? 2 : 3;
  const sizeOf = useCallback((n: UsageNode) => bytesBin(n.bytes), []);
  const localBoxes = useMemo(
    () => (shownFocus ? layoutBoxes(shownFocus, geo.local.w, geo.local.h, { maxDepth, narrow: stacked, top: true, label: (n, p) => fullName(n, p), size: sizeOf }) : []),
    [shownFocus, geo.local.w, geo.local.h, maxDepth, stacked, sizeOf],
  );
  const cloudBoxes = useMemo(
    () => (cloudFocus && geo.cloud.w > 0 && geo.cloud.h > 0 ? layoutBoxes(cloudFocus, geo.cloud.w, geo.cloud.h, { maxDepth, narrow: stacked, label: (n) => nodeName(n), size: sizeOf }) : []),
    [cloudFocus, geo.cloud.w, geo.cloud.h, maxDepth, stacked, sizeOf],
  );

  // badges: how many offloaded items each visible box held
  const pathOf = useCallback((n: UsageNode) => n.path ?? (n.id === "group:home" ? home : undefined), [home]);
  const badges = useMemo(() => {
    const m = new Map<string, number>();
    if (!allItems.length) return m;
    for (const b of localBoxes) {
      const p = pathOf(b.node);
      if (!p) continue;
      const c = links.under(p).length;
      if (c) m.set(b.node.id, c);
    }
    return m;
  }, [localBoxes, links, allItems.length, pathOf]);

  // ribbons and the partners they light
  const { ribbons, lit } = useMemo(() => {
    const out: Ribbon[] = [];
    const litIds = new Set<string>();
    if (!cloudBoxes.length) return { ribbons: out, lit: litIds };
    const anchors = localBoxes.map((b) => ({ id: b.node.id, path: pathOf(b.node) }));
    const cloudBoxFor = (itemId: string) => cloudBoxes.find((b) => b.node.id === `cloud:${itemId}`) ?? cloudBoxes.find((b) => b.node.children?.some((c) => c.id === `cloud:${itemId}`));
    const pair = (local: MapBox, cloud: MapBox) => {
      const key = `${local.node.id}->${cloud.node.id}`;
      if (out.some((r) => r.key === key)) return;
      litIds.add(local.node.id).add(cloud.node.id);
      out.push({ key, from: { x: local.x, y: local.y, w: local.w, h: local.h }, to: { x: cloud.x + geo.cloud.x, y: cloud.y + geo.cloud.y, w: cloud.w, h: cloud.h } });
    };
    const fromCloud = (cb: MapBox) => {
      const it = cb.node.cloud;
      if (!it) return;
      const a = links.anchor(it, anchors);
      const lb = a && localBoxes.find((b) => b.node.id === a);
      if (lb) pair(lb, cb);
    };
    if (hover?.side === "local" && badges.get(hover.box.node.id)) {
      const p = pathOf(hover.box.node)!;
      for (const it of links.under(p)) {
        const cb = cloudBoxFor(it.id);
        if (cb) pair(hover.box, cb);
      }
    }
    if (hover?.side === "cloud") fromCloud(hover.box);
    for (const id of cloudSelection) {
      const cb = cloudBoxes.find((b) => b.node.id === id);
      if (cb) fromCloud(cb);
    }
    return { ribbons: out, lit: litIds };
  }, [cloudBoxes, localBoxes, hover, badges, links, cloudSelection, geo.cloud.x, geo.cloud.y, pathOf]);

  // ── suggestions and the goal ──

  const suggestions = useMemo<ReclaimCandidate[]>(() => (reclaim && tree ? suggestReclaim(tree, provList, now) : []), [reclaim, tree, provList, now]);
  const suggested = useMemo(() => new Set(suggestions.map((s) => s.node.id)), [suggestions]);
  const candidates = useMemo(() => (tree ? reclaimCandidates(tree, provList, now) : []), [tree, provList, now]);
  const goalMax = useMemo(() => pickForGoal(candidates, Infinity).reduce((s, c) => s + c.bytes, 0), [candidates]);
  const goalStep = goalMax > 20 * 1024 ** 3 ? 1024 ** 3 : 100 * 1024 ** 2;

  // ── actions ──

  const selectedSet = useMemo(() => new Set(selection), [selection]);
  const cloudSelectedSet = useMemo(() => new Set(cloudSelection), [cloudSelection]);
  const ghosts = useMemo(() => new Set(preview.map((n) => n.id)), [preview]);

  const toggleLocal = (n: UsageNode) => {
    if (!actionable(n)) {
      setStatus(n.path ? refusalText(n.refusal) : t("machines-usagemap.menu_not_allowed"));
      return;
    }
    setSelection((s) => (s.includes(n.id) ? s.filter((x) => x !== n.id) : [...s, n.id]));
  };
  const toggleCloud = (n: UsageNode) => {
    const p = n.cloud && itemProvider.get(n.cloud.id)?.provider;
    if (!n.cloud?.restorable || !p?.restore) {
      setStatus(t("machines-usagemap.menu_not_restorable"));
      return;
    }
    setCloudSelection((s) => (s.includes(n.id) ? s.filter((x) => x !== n.id) : [...s, n.id]));
  };

  const zoomLocal = (b: MapBox, byKeyboard: boolean) => {
    if (!tree) return;
    const t2 = trailTo(tree, b.node.id);
    if (!t2) return;
    setTrail(t2.map((n) => n.id));
    setZoomKey((k) => k + 1);
    if (byKeyboard) setFocusTick((k) => k + 1);
    const n = b.node;
    if (n.path && n.has_children && !folders.current.has(n.id) && !loading.has(n.id)) void loadFolder(n.id, n.path);
  };
  const upLocal = () => {
    if (trailNodes.length <= 1) return false;
    setTrail(trailNodes.slice(0, -1).map((n) => n.id));
    setZoomKey((k) => k + 1);
    setFocusTick((k) => k + 1);
    return true;
  };
  const zoomCloud = (b: MapBox, byKeyboard: boolean) => {
    const t2 = trailTo(cloudRoot, b.node.id);
    if (!t2 || !b.node.children?.length) return;
    setCloudTrail(t2.map((n) => n.id));
    if (byKeyboard) setCloudFocusTick((k) => k + 1);
  };
  const upCloud = () => {
    if (cloudTrailNodes.length > 1) {
      setCloudTrail(cloudTrailNodes.slice(0, -1).map((n) => n.id));
      setCloudFocusTick((k) => k + 1);
      return true;
    }
    if (cloudFilter) {
      setCloudFilter(null);
      return true;
    }
    return false;
  };

  const openDialog = (d: Dialog) => {
    setMenu(null);
    setDialogError("");
    setDialog(d);
  };
  const forAction = (n: UsageNode, pick: (x: UsageNode) => boolean) => (selectedSet.has(n.id) ? outermost(selectedNodes.filter(pick)) : [n]);
  const askTrash = (nodes: UsageNode[]) => {
    const ok = outermost(nodes.filter((n) => n.can_trash && !n.cache_id));
    if (!ok.length) {
      const n = nodes[0];
      setStatus(n?.cache_id ? t("machines-usagemap.menu_cache_cleaned") : refusalText(n?.refusal));
      return;
    }
    openDialog({ kind: "trash", nodes: ok });
  };
  const askOffload = (nodes: UsageNode[], provider?: OffloadProvider) => {
    const p = provider ?? provList[0];
    const ok = outermost(nodes.filter((n) => n.can_offload && !n.cache_id));
    if (!p || !ok.length) return;
    openDialog({ kind: "offload", nodes: ok, provider: p, refused: [] });
  };
  const askRestore = (nodes: UsageNode[]) => {
    const ok = nodes.filter((n) => n.cloud?.restorable);
    const p = ok[0]?.cloud && itemProvider.get(ok[0].cloud.id)?.provider;
    if (!p || !ok.length) return;
    openDialog({ kind: "restore", nodes: ok.filter((n) => itemProvider.get(n.cloud!.id)?.provider === p), provider: p });
  };
  const askClean = (nodes: UsageNode[]) => {
    const ok = nodes.filter((n) => n.cache_id && !n.cache_in_use);
    if (ok.length) openDialog({ kind: "clean", nodes: ok });
  };

  /** After a change: the affected folders are asked again (rescan on the parent), the zoom and selection stay. */
  const refreshAfter = useCallback(
    async (paths: readonly string[], removed: boolean) => {
      if (removed) {
        for (const [id] of folders.current) if (paths.some((p) => id === p || id.startsWith(p + "/"))) folders.current.delete(id);
        setTree((t) => (t ? removePaths(t, paths) : t));
        setSelection((s) => s.filter((id) => !paths.includes(id)));
      }
      const parents = [...new Set(paths.map(parentPath).filter(Boolean))];
      let overview = false;
      for (const p of parents) {
        const known = tree ? findNode(tree, p) : null;
        if (known) void loadFolder(p, p, true);
        else overview = true;
      }
      if (overview) {
        try {
          await pollUsage(mid, "", { signal: signal(), intervalMs: pollMs, onReport: mergeOverview });
        } catch {
          // the map keeps what it shows
        }
      }
    },
    [tree, loadFolder, mid, pollMs, mergeOverview],
  );

  const machineName = machine.name;

  const confirmTrash = async (nodes: UsageNode[]) => {
    setBusy(true);
    setDialogError("");
    try {
      const r = await trashPaths(mid, nodes.map((n) => n.path!), { confirm: true });
      const items = r.items ?? [];
      const done = items.filter((i) => i.status === "trashed");
      setDialog(null);
      setTrashed({ result: { ...r, items }, ids: done.map((i) => i.trash_id!).filter(Boolean), bytes: done.reduce((s, i) => s + i.bytes, 0) || r.pending_bytes });
      setNotice(null);
      if (done.length) void refreshAfter(done.map((i) => i.path), true);
    } catch (e) {
      setDialogError(usageFailure(e).text);
    } finally {
      setBusy(false);
    }
  };

  const undo = async () => {
    if (!trashed) return;
    setBusy(true);
    try {
      const r = await undoTrash(mid, trashed.ids);
      const back = (r.restored ?? []).map((x) => x.path);
      setTrashed(null);
      setNotice({ text: t("machines-usagemap.trash_undone", { count: back.length }) });
      void refreshAfter(back.length ? back : trashed.result.items.map((i) => i.path), false);
    } catch (e) {
      setNotice({ text: usageFailure(e).text });
    } finally {
      setBusy(false);
    }
  };

  const purge = async () => {
    if (!trashed) return;
    setBusy(true);
    setDialogError("");
    try {
      const r = await purgeTrash(mid, trashed.ids);
      const unsupported = (r.items ?? []).some((i) => i.status === "unsupported");
      setDialog(null);
      setTrashed(null);
      setNotice({ text: unsupported ? t("machines-usagemap.trash_purge_unsupported") : t("machines-usagemap.trash_purged", { size: bytesBin(r.freed_bytes) }) });
      void refreshAfter([], false);
      if (volume && r.available_after !== undefined) setVolume({ ...volume, free_bytes: r.available_after, used_bytes: Math.max(0, volume.total_bytes - r.available_after) });
    } catch (e) {
      setDialogError(usageFailure(e).text);
    } finally {
      setBusy(false);
    }
  };

  const startRun = async (provider: OffloadProvider, action: "offload" | "restore", nodes: UsageNode[], install = false) => {
    setBusy(true);
    setDialogError("");
    const paths = action === "offload" ? nodes.map((n) => n.path!) : undefined;
    const items = action === "restore" ? nodes.map((n) => itemProvider.get(n.cloud!.id)?.item.id ?? n.cloud!.id) : undefined;
    try {
      const r = await runOffload(provider, { machine: mid, action, ...(paths ? { paths } : {}), ...(items ? { items } : {}), ...(install ? { install: true } : {}) });
      if (r.kind === "needs_install") {
        setDialog({ kind: "install", action, provider, tool: r.tool, paths, items, nodes });
        return;
      }
      if (r.kind === "refused") {
        const refused = r.items.map((i) => ({ path: i.path ?? i.id, reason: i.reason }));
        if (action === "offload") setDialog({ kind: "offload", nodes, provider, refused });
        else {
          setDialog(null);
          setNotice({ text: t("machines-usagemap.offload_restore_refused", { provider: provider.name }), items: refused.map((x, i) => ({ key: `${x.path ?? i}`, tone: "warn" as const, text: x.reason })) });
        }
        return;
      }
      setDialog(null);
      if (action === "offload") setSelection((s) => s.filter((id) => !nodes.some((n) => n.id === id)));
      else setCloudSelection((s) => s.filter((id) => !nodes.some((n) => n.id === id)));
      setJob({ provider, id: r.job, action, paths: paths ?? nodes.map((n) => n.cloud?.origin_path ?? "").filter(Boolean), state: null });
    } catch (e) {
      setDialogError(usageFailure(e).text);
    } finally {
      setBusy(false);
    }
  };

  // the job's progress, once a second until it ends; then the inventory and the folders are read again
  useEffect(() => {
    if (!job || (job.state && job.state.state !== "running") || job.error) return;
    let dead = false;
    const timer = setTimeout(async () => {
      try {
        const s = await fetchJob(job.provider, mid, job.id, signal());
        if (dead) return;
        setJob((j) => (j && j.id === job.id ? { ...j, state: { ...s, items: s.items ?? [] } } : j));
        if (s.state !== "running") {
          void loadInventories(provList, true);
          if (job.paths.length) void refreshAfter(job.paths, job.action === "offload" && s.state === "done");
        }
      } catch (e) {
        if (dead || isAbort(e)) return;
        // lost track (a redeploy): ask the inventory again
        setJob((j) => (j && j.id === job.id ? { ...j, error: t("machines-usagemap.offload_job_lost") } : j));
        void loadInventories(provList, true);
      }
    }, job.state ? pollMs : 0);
    return () => {
      dead = true;
      clearTimeout(timer);
    };
  }, [job, mid, pollMs, provList, loadInventories, refreshAfter]);

  const confirmClean = async (nodes: UsageNode[]) => {
    setBusy(true);
    setDialogError("");
    try {
      const r = await cleanup(mid, nodes.map((n) => n.cache_id!));
      setDialog(null);
      setSelection((s) => s.filter((id) => !nodes.some((n) => n.id === id)));
      setNotice({
        text: t("machines-alerts.freed", { size: bytesBin(r.freed_bytes) }),
        items: r.items.map(item => ({ key: item.id, tone: item.status === "cleaned" ? "ok" : "warn",
          text: `${cacheTitle(item.id)} · ${cleanupStatusText(item.status, item.error)}` })),
      });
      void refreshAfter(nodes.map((n) => n.path!), false);
    } catch (e) {
      setDialogError(usageFailure(e).text);
    } finally {
      setBusy(false);
    }
  };

  const openLocalMenu = (b: MapBox, point: { x: number; y: number }) => {
    const n = b.node;
    const offloaded = pathOf(n) ? links.under(pathOf(n)!).length : 0;
    setMenu({
      point,
      header: fullName(n, b.parent),
      items: localMenu(n, { selected: selectedSet.has(n.id), providers: provList, offloadedCount: offloaded }, {
        zoom: () => zoomLocal(b, true),
        toggle: () => toggleLocal(n),
        trash: () => askTrash(forAction(n, (x) => !!x.can_trash && !x.cache_id)),
        offload: (p) => askOffload(forAction(n, (x) => !!x.can_offload), p),
        clean: () => askClean(forAction(n, (x) => !!x.cache_id)),
        copy: () => void copyText(n.path ?? "", { done: t("machines-usagemap.menu_copied") }),
        restore: () => {},
        showOffloaded: () => showOffloaded(n),
      }),
    });
  };
  const openCloudMenu = (b: MapBox, point: { x: number; y: number }) => {
    const n = b.node;
    const p = n.cloud ? itemProvider.get(n.cloud.id)?.provider : undefined;
    setMenu({
      point,
      header: nodeName(n),
      items: cloudMenu(n, { selected: cloudSelectedSet.has(n.id), provider: p }, {
        zoom: () => zoomCloud(b, true),
        toggle: () => toggleCloud(n),
        trash: () => {},
        offload: () => {},
        clean: () => {},
        copy: () => void copyText(n.cloud?.origin_path ?? "", { done: t("machines-usagemap.menu_copied") }),
        restore: () => askRestore(cloudSelectedSet.has(n.id) ? cloudSelectedNodes : [n]),
        showOffloaded: () => {},
      }),
    });
  };
  const showOffloaded = (n: UsageNode) => {
    const p = pathOf(n);
    if (!p) return;
    setCloudFilter({ path: p, name: nodeName(n) });
    setCloudTrail([]);
    setCloudFocusTick((k) => k + 1);
  };

  const chooseSuggestion = (c: ReclaimCandidate) => {
    if (!tree) return;
    const t2 = trailTo(tree, c.node.id);
    if (!t2) return;
    setTrail(t2.slice(0, -1).map((n) => n.id));
    setZoomKey((k) => k + 1);
    setSelection((s) => (s.includes(c.node.id) ? s : [...s, c.node.id]));
  };

  const commitGoal = (v: number) => {
    setGoal(v);
    setSelection(pickForGoal(candidates, v).map((c) => c.node.id));
  };

  // ── words ──

  const a11yOf = (b: MapBox, side: "local" | "cloud") => {
    const n = b.node;
    const parent = side === "local" ? focusNode : cloudFocus;
    const parts = [fullName(n, b.parent), bytesBin(n.bytes)];
    const sh = share(n, parent);
    if (sh) parts.push(t("machines-usagemap.a11y_share", { share: sh }));
    parts.push(categoryText(n));
    const age = ageText(n.used_at, now);
    if (age) parts.push(age);
    const badge = badges.get(n.id);
    if (side === "local" && badge) parts.push(t("machines-usagemap.cloud_badge", { count: badge }));
    if ((side === "local" ? selectedSet : cloudSelectedSet).has(n.id)) parts.push(t("machines-usagemap.a11y_selected"));
    if (suggested.has(n.id)) parts.push(t("machines-usagemap.a11y_suggested"));
    return parts.join(", ");
  };

  const detail = (() => {
    if (status) return status;
    if (!hover) return dragging ? "" : t("machines-usagemap.scan_hint");
    const n = hover.box.node;
    const parent = hover.side === "local" ? focusNode : cloudFocus;
    const parts = [fullName(n, hover.box.parent), bytesBin(n.bytes)];
    const sh = share(n, parent);
    if (sh) parts.push(t("machines-usagemap.a11y_share", { share: sh }));
    const age = ageText(n.used_at, now);
    if (age) parts.push(age);
    const where = n.display ?? n.path ?? n.cloud?.origin_path;
    if (where) parts.push(where);
    if (hover.side === "local" && n.path && !n.can_trash && !n.cache_id && n.refusal) parts.push(refusalText(n.refusal));
    if (n.cloud?.stored_bytes !== undefined) parts.push(t("machines-usagemap.cloud_stored", { size: bytesBin(n.cloud.stored_bytes) }));
    return parts.join(" · ");
  })();
  useEffect(() => {
    if (!status) return;
    const id = setTimeout(() => setStatus(""), 4000);
    return () => clearTimeout(id);
  }, [status]);

  // ── what the tray shows ──

  const trashable = outermost(selectedNodes.filter((n) => n.can_trash && !n.cache_id)).length;
  const offloadable = outermost(selectedNodes.filter((n) => n.can_offload && !n.cache_id)).length;
  const cleanable = selectedNodes.filter((n) => n.cache_id && !n.cache_in_use).length;
  const restorable = cloudSelectedNodes.filter((n) => n.cloud?.restorable).length;

  const trayJob: TrayJob | null = job
    ? (() => {
        const s = job.state;
        const title = job.error
          ? job.error
          : !s || s.state === "running"
            ? t(`machines-usagemap.offload_phase_${s?.phase && ["install", "check", "upload", "verify", "cleanup", "download"].includes(s.phase) ? s.phase : job.action === "restore" ? "download" : "upload"}`)
            : s.state === "done"
              ? job.action === "restore"
                ? t("machines-usagemap.offload_job_restored")
                : t("machines-usagemap.offload_job_done", { size: bytesBin(s.freed_bytes ?? s.done_bytes) })
              : s.state === "canceled"
                ? t("machines-usagemap.offload_job_canceled")
                : t("machines-usagemap.offload_job_failed", { error: s.error || errorText(s.error) });
        const running = !job.error && (!s || s.state === "running");
        return {
          title,
          detail: s && running ? t("machines-usagemap.offload_job_progress", { done: bytesBin(s.done_bytes), total: bytesBin(s.total_bytes) }) : "",
          done: s?.done_bytes ?? 0,
          total: s?.total_bytes ?? 0,
          state: job.error ? "failed" : (s?.state ?? "running"),
          onCancel: running ? () => void cancelJob(job.provider, mid, job.id).then(() => setJob((j) => (j ? { ...j, state: { ...(j.state ?? { done_bytes: 0, total_bytes: 0, items: [] }), state: "canceled" } } : j))).catch(() => {}) : undefined,
          onDismiss: running ? undefined : () => setJob(null),
        };
      })()
    : null;

  const trayNotice: TrayNotice | null = trashed
    ? {
        text: t("machines-usagemap.trash_done", { count: trashed.ids.length, machine: machineName }),
        detail: trashed.ids.length ? t("machines-usagemap.trash_pending", { size: bytesBin(trashed.bytes) }) : undefined,
        items: trashed.result.items
          .filter((i) => i.status !== "trashed")
          .map((i) => ({
            key: i.path,
            tone: "warn" as const,
            text: `${i.display ?? i.path}: ${i.status === "refused" ? refusalText(i.refusal) : i.status === "missing" ? t("machines-usagemap.trash_status_missing") : t("machines-usagemap.trash_status_failed", { error: i.error ?? "" })}`,
          })),
        onUndo: trashed.ids.length ? () => void undo() : undefined,
        onPurge: trashed.ids.length ? () => openDialog({ kind: "purge" }) : undefined,
        onDismiss: () => setTrashed(null),
        busy,
      }
    : notice
      ? { ...notice, onDismiss: () => setNotice(null) }
      : null;

  // ── render ──

  if (!machine.online) {
    return (
      <section className="rounded-card bg-surface px-4 py-3.5 shadow-card" data-testid="usage-map">
        <h3 className="text-sm font-medium text-ink">{t("machines-usagemap.title")}</h3>
        <p className="mt-1 text-meta text-ink-3">{t("machines-usagemap.scan_offline")}</p>
      </section>
    );
  }

  const free = volume?.free_bytes ?? 0;
  const total = volume?.total_bytes ?? 0;
  const headline = reclaim && volume
    ? suggestions.length
      ? t("machines-usagemap.reclaim_headline", { free: bytesBin(free), total: bytesBin(total), count: suggestions.length, size: bytesBin(suggestions.reduce((s, c) => s + c.bytes, 0)) })
      : scanning
        ? t("machines-usagemap.reclaim_looking", { free: bytesBin(free), total: bytesBin(total) })
        : t("machines-usagemap.reclaim_none", { free: bytesBin(free), total: bytesBin(total) })
    : "";
  const chip = scaleChip(geo.ratio);
  const cloudStates = provList.map((p) => ({ provider: p, state: inventories.get(providerKey(p)) ?? { state: "loading" as const } }));

  return (
    <section className="rounded-card bg-surface px-4 py-3.5 shadow-card" data-testid="usage-map" data-reclaim={reclaim || undefined}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-medium text-ink">{t("machines-usagemap.title")}</h3>
          <p className="text-submeta text-ink-3">{t("machines-usagemap.subtitle")}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {tree && <Segmented compact value={tint} onChange={setTint} label={t("machines-usagemap.tint_label")} options={[{ v: "kind", label: t("machines-usagemap.tint_kind") }, { v: "age", label: t("machines-usagemap.tint_age") }]} />}
          {!started ? (
            <Button tone="primary" icon="chart" onClick={start} testId="usage-start">
              {t("machines-usagemap.scan_start")}
            </Button>
          ) : (
            <Button
              tone="secondary"
              icon="retry"
              busy={scanning}
              onClick={() => {
                const f = focusNode;
                if (f?.path) void loadFolder(f.id, f.path, true);
                else {
                  folders.current = new Map();
                  setScanning(true);
                  void pollUsage(mid, "", { rescan: true, signal: signal(), intervalMs: pollMs, onReport: mergeOverview })
                    .catch((e) => !isAbort(e) && setFailure(usageFailure(e)))
                    .finally(() => setScanning(false));
                }
                if (provList.length) void loadInventories(provList, true);
              }}
              testId="usage-rescan"
            >
              {t("machines-usagemap.scan_rescan")}
            </Button>
          )}
        </div>
      </div>

      {failure && (
        <p role="alert" className="mt-3 flex items-start gap-2 rounded-control bg-red-tint px-3 py-2 text-meta text-red" data-testid="usage-error" data-kind={failure.kind}>
          <Icon name="warning" size={14} className="mt-0.5 shrink-0" />
          <span>{failure.text}</span>
        </p>
      )}

      {!tree && (failure?.kind === "node_outdated" || failure?.kind === "hub_outdated") && <LegacyCacheRecovery machine={machine} />}

      {started && !tree && !failure && <p className="mt-3 text-meta text-ink-3" data-testid="usage-scanning">{t("machines-usagemap.scan_scanning")}</p>}

      {headline && (
        <div className="mt-3" data-testid="usage-reclaim">
          <p className="text-meta font-medium text-ink">{headline}</p>
          {suggestions.length > 0 && (
            <div className="mt-1.5 flex flex-wrap gap-1.5" role="group" aria-label={t("machines-usagemap.reclaim_label")}>
              {suggestions.map((s) => (
                <button
                  key={s.node.id}
                  type="button"
                  data-suggestion={s.node.path}
                  onClick={() => chooseSuggestion(s)}
                  className={cx("inline-flex max-w-full items-center gap-1.5 rounded-full px-2.5 py-1 text-submeta transition-colors", selectedSet.has(s.node.id) ? "bg-accent-tint text-accent-ink shadow-hairline" : "bg-orange-tint text-ink hover:bg-hover")}
                >
                  <Icon name={s.action === "clean" ? "sparkles" : s.action === "trash" ? "trash" : "upload"} size={12} className="shrink-0" />
                  <span className="truncate">{nodeName(s.node)}</span>
                  <span className="shrink-0 tabular-nums text-ink-2">{bytesBin(s.bytes)}</span>
                  <span className="shrink-0 text-ink-3">{t(`machines-usagemap.reclaim_action_${s.action}`)}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      <div ref={wrap} className="w-full">
        {tree && focusNode && (
          <>
            <div className={cx("mt-3 flex gap-2", stacked ? "flex-col" : "items-end")}>
              <nav aria-label={t("machines-usagemap.a11y_crumbs_local")} className="flex min-w-0 flex-1 flex-wrap items-center gap-1 text-meta" data-testid="usage-crumbs">
                {trailNodes.map((n, i) => {
                  const last = i === trailNodes.length - 1;
                  return (
                    <span key={n.id} className="inline-flex items-center gap-1">
                      {i > 0 && <Icon name="chevron" size={11} className="-rotate-90 text-ink-3" />}
                      {last ? (
                        <span className="font-medium text-ink" aria-current="location">{nodeName(n)}</span>
                      ) : (
                        <button
                          type="button"
                          className="rounded-[6px] px-1 text-accent-ink hover:bg-accent-tint"
                          onClick={(e) => {
                            setTrail(trailNodes.slice(0, i + 1).map((x) => x.id));
                            setZoomKey((k) => k + 1);
                            if (e.detail === 0) setFocusTick((k) => k + 1);
                          }}
                        >
                          {nodeName(n)}
                        </button>
                      )}
                    </span>
                  );
                })}
                {(scanning || loading.size > 0) && <span className="ml-1 text-submeta text-ink-3">{t("machines-usagemap.scan_measuring")}</span>}
              </nav>
              {hasCloudSide && !stacked && (
                <div style={{ width: geo.cloud.w }} className="shrink-0">
                  <CloudCrumbs root={cloudRoot} trail={cloudTrailNodes} filter={cloudFilter} onTrail={(ids) => setCloudTrail(ids)} onClearFilter={() => setCloudFilter(null)} chip={chip} />
                </div>
              )}
            </div>

            <div className="relative mt-2" style={stacked && hasCloudSide ? { paddingTop: geo.cloud.y } : { height: geo.rowH }} onKeyDown={(e) => e.key === "Escape" && menu && setMenu(null)}>
              <div className="absolute top-0 left-0">
                <UsageTreemap
                  side="local"
                  boxes={localBoxes}
                  width={geo.local.w}
                  height={geo.local.h}
                  label={t("machines-usagemap.a11y_map_local", { machine: machineName })}
                  zoomKey={`${focusNode.id}:${zoomKey}`}
                  focusTick={focusTick}
                  tint={tint}
                  now={now}
                  selected={selectedSet}
                  suggested={suggested}
                  lit={lit}
                  ghosts={ghosts}
                  busy={loading}
                  badges={badges}
                  freeGain={!!what && what.freed > 0}
                  nameOf={(b) => fullName(b.node, b.parent)}
                  sizeOf={(b) => (b.node.kind === "free" && what && what.freed > 0 ? `${bytesBin(b.node.bytes - what.freed)} +${bytesBin(what.freed)}` : bytesBin(b.node.bytes))}
                  a11yOf={(b) => a11yOf(b, "local")}
                  badgeLabel={(count) => t("machines-usagemap.cloud_badge", { count })}
                  canZoom={(b) => zoomable(b.node)}
                  onZoom={zoomLocal}
                  onToggle={(b) => toggleLocal(b.node)}
                  onMenu={openLocalMenu}
                  onTrash={(b) => askTrash(forAction(b.node, (x) => !!x.can_trash && !x.cache_id))}
                  onUp={upLocal}
                  onHover={(b) => setHover(b ? { side: "local", box: b } : null)}
                  onBadge={(b) => showOffloaded(b.node)}
                  canDrag={(b) => provList.length > 0 && !!b.node.can_offload && !!b.node.path && !b.node.cache_id}
                  onDragStart={(b) => setDragging({ side: "local", node: b.node })}
                  onDragEnd={() => setDragging(null)}
                  accepts={dragging?.side === "cloud"}
                  onDrop={() => {
                    const n = dragging?.node;
                    setDragging(null);
                    if (n) askRestore(cloudSelectedSet.has(n.id) ? cloudSelectedNodes : [n]);
                  }}
                  dropHint={dragging?.side === "cloud" ? t("machines-usagemap.drop_restore") : undefined}
                />
              </div>
              {hasCloudSide && (
                <div className={stacked ? undefined : "absolute"} style={{ left: geo.cloud.x, top: geo.cloud.y, width: geo.cloud.w }}>
                  {stacked && <CloudCrumbs root={cloudRoot} trail={cloudTrailNodes} filter={cloudFilter} onTrail={(ids) => setCloudTrail(ids)} onClearFilter={() => setCloudFilter(null)} chip={chip} />}
                  <UsageMapCloud
                    states={cloudStates}
                    providers={providers}
                    boxesEmpty={!cloudBoxes.length}
                    accepts={dragging?.side === "local"}
                    onDrop={() => {
                      const n = dragging?.node;
                      setDragging(null);
                      if (n) askOffload(selectedSet.has(n.id) ? selectedNodes : [n]);
                    }}
                    dropHint={dragging?.side === "local" && provList[0] ? t("machines-usagemap.drop_offload", { provider: provList[0].name }) : undefined}
                  >
                    {cloudBoxes.length > 0 && (
                      <UsageTreemap
                        side="cloud"
                        boxes={cloudBoxes}
                        width={geo.cloud.w}
                        height={geo.cloud.h}
                        label={t("machines-usagemap.a11y_map_cloud")}
                        zoomKey={`${cloudFocus.id}`}
                        focusTick={cloudFocusTick}
                        tint={tint}
                        now={now}
                        selected={cloudSelectedSet}
                        suggested={EMPTY}
                        lit={lit}
                        ghosts={EMPTY}
                        busy={EMPTY}
                        badges={EMPTY_MAP}
                        freeGain={false}
                        nameOf={(b) => nodeName(b.node)}
                        sizeOf={(b) => bytesBin(b.node.bytes)}
                        a11yOf={(b) => a11yOf(b, "cloud")}
                        badgeLabel={() => ""}
                        canZoom={(b) => !!b.node.children?.length}
                        onZoom={zoomCloud}
                        onToggle={(b) => toggleCloud(b.node)}
                        onMenu={openCloudMenu}
                        onTrash={() => {}}
                        onUp={upCloud}
                        onHover={(b) => setHover(b ? { side: "cloud", box: b } : null)}
                        canDrag={(b) => !!b.node.cloud?.restorable && !!itemProvider.get(b.node.cloud.id)?.provider.restore}
                        onDragStart={(b) => setDragging({ side: "cloud", node: b.node })}
                        onDragEnd={() => setDragging(null)}
                        accepts={dragging?.side === "local"}
                        onDrop={() => {
                          const n = dragging?.node;
                          setDragging(null);
                          if (n) askOffload(selectedSet.has(n.id) ? selectedNodes : [n]);
                        }}
                      />
                    )}
                  </UsageMapCloud>
                </div>
              )}
              <UsageMapChannel ribbons={ribbons} width={stacked ? width : width} height={stacked ? geo.cloud.y + geo.cloud.h : geo.rowH} stacked={stacked} />
            </div>

            <p aria-live="polite" className="mt-1.5 min-h-5 truncate text-submeta text-ink-2" data-testid="usage-detail">
              {detail}
            </p>
            {partial && !scanning && <p className="text-submeta text-ink-3" data-testid="usage-partial">{t("machines-usagemap.scan_partial")}</p>}
            <Legend tint={tint} />
          </>
        )}
      </div>

      <UsageMapTray
        what={what}
        local={selectedNodes}
        cloud={cloudSelectedNodes}
        nameOf={(n) => nodeName(n)}
        trashable={trashable}
        offloadable={offloadable}
        cleanable={cleanable}
        restorable={restorable}
        hasProvider={provList.length > 0}
        onTrash={() => askTrash(selectedNodes)}
        onOffload={() => askOffload(selectedNodes)}
        onClean={() => askClean(selectedNodes)}
        onRestore={() => askRestore(cloudSelectedNodes)}
        onClear={() => {
          setSelection([]);
          setCloudSelection([]);
          setGoal(0);
        }}
        onRemove={(n) => (n.cloud ? setCloudSelection((s) => s.filter((x) => x !== n.id)) : setSelection((s) => s.filter((x) => x !== n.id)))}
        goal={tree && !scanning ? { max: goalMax, value: Math.min(goal, goalMax), step: goalStep, onInput: setGoal, onCommit: commitGoal } : null}
        job={trayJob}
        notice={trayNotice}
      />

      {menu && <Menu items={menu.items} anchor={null} point={menu.point} onClose={() => setMenu(null)} header={menu.header} headerAsWritten width={248} />}

      {dialog?.kind === "trash" && (
        <TrashDialog nodes={dialog.nodes} machine={machineName} busy={busy} error={dialogError} name={nodeName} onCancel={() => setDialog(null)} onConfirm={() => void confirmTrash(dialog.nodes)} />
      )}
      {dialog?.kind === "purge" && trashed && (
        <PurgeDialog count={trashed.ids.length} bytes={trashed.bytes} machine={machineName} busy={busy} error={dialogError} onCancel={() => setDialog(null)} onConfirm={() => void purge()} />
      )}
      {dialog?.kind === "offload" && (
        <OffloadDialog
          nodes={dialog.nodes}
          providers={provList}
          provider={dialog.provider}
          onProvider={(p) => setDialog({ ...dialog, provider: p, refused: [] })}
          refused={dialog.refused}
          machine={machineName}
          busy={busy}
          error={dialogError}
          name={nodeName}
          onCancel={() => setDialog(null)}
          onConfirm={() => void startRun(dialog.provider, "offload", dialog.nodes)}
        />
      )}
      {dialog?.kind === "install" && (
        <InstallDialog
          tool={dialog.tool}
          provider={dialog.provider}
          action={dialog.action}
          machine={machineName}
          busy={busy}
          error={dialogError}
          onCancel={() => setDialog(null)}
          onConfirm={() => void startRun(dialog.provider, dialog.action, dialog.nodes, true)}
        />
      )}
      {dialog?.kind === "restore" && (
        <RestoreDialog nodes={dialog.nodes} provider={dialog.provider} machine={machineName} busy={busy} error={dialogError} name={nodeName} onCancel={() => setDialog(null)} onConfirm={() => void startRun(dialog.provider, "restore", dialog.nodes)} />
      )}
      {dialog?.kind === "clean" && <CleanDialog nodes={dialog.nodes} machine={machineName} busy={busy} error={dialogError} onCancel={() => setDialog(null)} onConfirm={() => void confirmClean(dialog.nodes)} />}
    </section>
  );
}

const EMPTY: ReadonlySet<string> = new Set();
const EMPTY_MAP: ReadonlyMap<string, number> = new Map();

function CloudCrumbs({ root, trail, filter, onTrail, onClearFilter, chip }: { root: UsageNode; trail: UsageNode[]; filter: { name: string } | null; onTrail: (ids: string[]) => void; onClearFilter: () => void; chip?: number }) {
  return (
    <nav aria-label={t("machines-usagemap.a11y_crumbs_cloud")} className="flex flex-wrap items-center gap-1 text-meta" data-testid="usage-cloud-crumbs">
      <Icon name="cloud" size={13} className="text-ink-3" />
      {trail.map((n, i) => {
        const last = i === trail.length - 1;
        const label = n === root ? t("machines-usagemap.crumb_cloud") : nodeName(n);
        return (
          <span key={n.id} className="inline-flex items-center gap-1">
            {i > 0 && <Icon name="chevron" size={11} className="-rotate-90 text-ink-3" />}
            {last ? <span className="font-medium text-ink">{label}</span> : <button type="button" className="rounded-[6px] px-1 text-accent-ink hover:bg-accent-tint" onClick={() => onTrail(trail.slice(0, i + 1).map((x) => x.id))}>{label}</button>}
          </span>
        );
      })}
      {filter && (
        <button type="button" className="inline-flex items-center gap-1 rounded-full bg-accent-tint px-2 text-submeta text-accent-ink" onClick={onClearFilter} data-testid="usage-cloud-filter">
          {t("machines-usagemap.cloud_from", { name: filter.name })}
          <Icon name="x" size={10} />
        </button>
      )}
      {chip && (
        <span className="ml-auto rounded-full bg-field px-1.5 text-submeta text-ink-2 tabular-nums" title={t("machines-usagemap.cloud_scale_hint", { n: chip })} data-testid="usage-scale-chip">
          {t("machines-usagemap.cloud_scale_chip", { n: chip })}
        </span>
      )}
    </nav>
  );
}

function Legend({ tint }: { tint: Tint }) {
  const items =
    tint === "kind"
      ? (["apps", "personal", "appdata", "cache", "system", "other", "free"] as const).map((c) => ({ key: c, label: t(`machines-usagemap.cat_${c}`), color: `var(--umap-${c})` }))
      : (["recent", "months", "half_year", "year"] as const).map((a) => ({ key: a, label: t(`machines-usagemap.age_${a}`), color: `var(--umap-age-${a})` }));
  return (
    <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-submeta text-ink-2" aria-label={t("machines-usagemap.a11y_legend")} data-testid="usage-legend">
      {items.map((i) => (
        <li key={i.key} className="inline-flex items-center gap-1.5">
          <span aria-hidden className={cx("inline-block size-3 rounded-[3px]", i.key === "free" && "border border-dashed border-line-strong")} style={{ background: i.key === "free" ? undefined : i.color }} />
          {i.label}
        </li>
      ))}
    </ul>
  );
}
