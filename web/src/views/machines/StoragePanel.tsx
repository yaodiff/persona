// Lazy-loaded Storage & Cleanup panel (#12349): per-mount capacity and inode utilization (fast read-only statfs),
// on-demand bounded hotspot storage analysis (never automatic background du), and preview + confirmation-gated
// selective cleanup of allowlisted targets.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText, isAbort, pastHub } from "../../lib/api";
import { bytesBin, percent } from "../../lib/machine-detail";
import type { Machine } from "../../lib/types";
import { MachineTool, type MachineToolProps } from "./MachinePicker";
import { Button, cx, Dialog, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { UsageMap } from "./UsageMap";

export interface MountStat {
  mount: string;
  device?: string;
  fs_type?: string;
  used_bytes: number;
  avail_bytes: number;
  total_bytes: number;
  used_percent: number;
  inodes_total?: number;
  inodes_used?: number;
  inodes_free?: number;
  inodes_percent?: number;
  read_only?: boolean;
}

export interface MachineStorageReport {
  machine_id?: string;
  at: number;
  os: string;
  supported: boolean;
  mounts: MountStat[];
  collected_ms: number;
}

export interface StorageHotspotItem {
  label: string;
  path?: string;
  bytes: number;
  reclaimable_bytes?: number;
  stale?: boolean;
  detail?: string;
}

export interface StorageHotspotCategory {
  id: string;
  path?: string;
  bytes: number;
  reclaimable_bytes?: number;
  status: "ok" | "partial" | "empty" | "unavailable" | string;
  cleanable: boolean;
  cleanup_target_id?: string;
  in_use?: boolean;
  item_count?: number;
  items?: StorageHotspotItem[];
  note?: string;
}

export interface MachineStorageHotspotsReport {
  machine_id?: string;
  at: number;
  os: string;
  partial: boolean;
  duration_ms: number;
  categories: StorageHotspotCategory[];
}

export interface StorageCleanupItem {
  target_id: string;
  path?: string;
  action?: string;
  bytes: number;
  status: "preview" | "cleaned" | "in_use" | "missing" | "skipped" | "failed" | string;
  error?: string;
}

export interface StorageCleanupResult {
  machine_id?: string;
  dry_run: boolean;
  estimated_bytes: number;
  freed_bytes: number;
  available_before?: number;
  available_after?: number;
  items: StorageCleanupItem[];
}

type StorageErrorKind = "node_outdated" | "hub_outdated" | "forbidden" | "offline" | "deadline" | "error";

interface StorageError {
  kind: StorageErrorKind;
  text: string;
}

function classifyStorageError(e: unknown): StorageError {
  if (e instanceof ApiError) {
    const code = typeof e.body?.code === "string" ? e.body.code : "";
    if (e.status === 409 && code === "node_outdated") return { kind: "node_outdated", text: t("machines-storage.err_node_outdated") };
    if (e.status === 404 && (!code || !e.isJson)) return { kind: "hub_outdated", text: t("machines-storage.err_hub_outdated") };
    if (e.status === 403) return { kind: "forbidden", text: t("machines-storage.err_forbidden") };
    if (e.status === 503 || code === "machine_offline") return { kind: "offline", text: t("machines-storage.err_offline") };
    if (e.status === 504 || code === "deadline") return { kind: "deadline", text: t("machines-storage.err_deadline") };
  }
  return { kind: "error", text: errorText(e) };
}

function categoryTitle(id: string): string {
  switch (id) {
    case "git-worktrees":
      return t("machines-storage.cat_git_worktrees");
    case "docker":
      return t("machines-storage.cat_docker");
    case "user-cache":
      return t("machines-storage.cat_user_cache");
    case "go-build":
      return t("machines-storage.cat_go_build");
    case "go-mod":
      return t("machines-storage.cat_go_mod");
    case "npm-cache":
      return t("machines-storage.cat_npm_cache");
    case "pip-cache":
      return t("machines-storage.cat_pip_cache");
    case "persona-logs":
      return t("machines-storage.cat_persona_logs");
    case "persona-backups":
      return t("machines-storage.cat_persona_backups");
    default:
      return id;
  }
}

function targetTitle(id: string): string {
  switch (id) {
    case "go-build":
      return t("machines-storage.cat_go_build");
    case "go-mod":
      return t("machines-storage.cat_go_mod");
    case "npm-cache":
      return t("machines-storage.cat_npm_cache");
    case "pip-cache":
      return t("machines-storage.cat_pip_cache");
    case "persona-logs":
      return t("machines-storage.cat_persona_logs");
    case "persona-backups":
      return t("machines-storage.cat_persona_backups");
    case "docker-build-cache":
      return t("machines-storage.target_docker_build_cache");
    case "git-worktrees-prune":
      return t("machines-storage.target_git_worktrees_prune");
    default:
      return id;
  }
}

function cleanupStatusText(status: string, error?: string): string {
  switch (status) {
    case "preview":
      return t("machines-storage.status_preview");
    case "cleaned":
      return t("machines-storage.status_cleaned");
    case "in_use":
      return t("machines-storage.status_in_use");
    case "missing":
      return t("machines-storage.status_missing");
    case "skipped":
      return error ? t("machines-storage.status_skipped_why", { error }) : t("machines-storage.status_skipped");
    default:
      return error ? t("machines-storage.status_failed_why", { error }) : t("machines-storage.status_failed");
  }
}

function formatInodeCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function barTone(pct: number): "critical" | "warning" | "normal" {
  if (pct >= 90) return "critical";
  if (pct >= 80) return "warning";
  return "normal";
}

function UsageBar({ pct, label }: { pct: number; label: string }) {
  const clamped = Math.max(0, Math.min(100, pct));
  const tone = barTone(clamped);
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-inset" role="progressbar" aria-valuenow={Math.round(clamped)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div
        className={cx("h-full rounded-full transition-[width] duration-200", tone === "critical" ? "bg-red" : tone === "warning" ? "bg-orange" : "bg-accent")}
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
}

/** A request to open the disk map (#14576): on `mid`, in reclaim mode; `seq` changes on every request. */
export interface StorageFocus {
  mid: string;
  mode: "reclaim";
  seq: number;
}

export function StoragePanel({ focus, ...props }: MachineToolProps & { focus?: StorageFocus }) {
  return (
    <MachineTool {...props} key={focus?.seq} initialMachineId={focus?.mid ?? props.initialMachineId}>
      {(machine) => <Storage machine={machine} focus={focus} />}
    </MachineTool>
  );
}

function Storage({ machine, focus }: { machine?: Machine; focus?: StorageFocus }) {
  const selectedId = machine?.id ?? "";

  // Mount stats state (fast statfs read-only)
  const [mountsLoading, setMountsLoading] = useState(false);
  const [mountsReport, setMountsReport] = useState<MachineStorageReport | null>(null);
  const [mountsError, setMountsError] = useState<StorageError | null>(null);
  const [mountRefreshTick, setMountRefreshTick] = useState(0);

  // Hotspot analysis state (strictly on-demand — NEVER auto-triggered on mount)
  const [hotspotsLoading, setHotspotsLoading] = useState(false);
  const [hotspotsReport, setHotspotsReport] = useState<MachineStorageHotspotsReport | null>(null);
  const [hotspotsError, setHotspotsError] = useState<StorageError | null>(null);

  // Selective cleanup state (preview -> confirmation dialog -> execute)
  const [selectedTargets, setSelectedTargets] = useState<Set<string>>(new Set());
  const [previewBusy, setPreviewBusy] = useState(false);
  // Frozen snapshot of the dry-run preview and exact target IDs being confirmed so rescans
  // or selection edits cannot cause confirmCleanup to delete a different target set.
  const [confirmState, setConfirmState] = useState<{ targets: string[]; preview: StorageCleanupResult } | null>(null);
  const [cleanupBusy, setCleanupBusy] = useState(false);
  const [cleanupResult, setCleanupResult] = useState<StorageCleanupResult | null>(null);
  const [cleanupError, setCleanupError] = useState<StorageError | null>(null);

  // Track active machine ID and in-flight controllers so stale responses from a previous machine are aborted/ignored.
  const selectedIdRef = useRef(selectedId);
  selectedIdRef.current = selectedId;
  const hotspotsCtrlRef = useRef<AbortController | null>(null);
  const previewCtrlRef = useRef<AbortController | null>(null);
  const cleanupCtrlRef = useRef<AbortController | null>(null);

  // Abort in-flight per-machine requests and reset mount, hotspot & cleanup state when switching machines or unmounting.
  useEffect(() => {
    hotspotsCtrlRef.current?.abort();
    hotspotsCtrlRef.current = null;
    previewCtrlRef.current?.abort();
    previewCtrlRef.current = null;
    cleanupCtrlRef.current?.abort();
    cleanupCtrlRef.current = null;
    setMountsReport(null);
    setMountsError(null);
    setHotspotsLoading(false);
    setHotspotsReport(null);
    setHotspotsError(null);
    setSelectedTargets(new Set());
    setPreviewBusy(false);
    setConfirmState(null);
    setCleanupBusy(false);
    setCleanupResult(null);
    setCleanupError(null);
    return () => {
      hotspotsCtrlRef.current?.abort();
      hotspotsCtrlRef.current = null;
      previewCtrlRef.current?.abort();
      previewCtrlRef.current = null;
      cleanupCtrlRef.current?.abort();
      cleanupCtrlRef.current = null;
    };
  }, [selectedId, machine?.online]);

  // Fetch fast per-mount statfs report when machine changes or refresh is clicked.
  useEffect(() => {
    if (!machine) {
      setMountsReport(null);
      setMountsError(null);
      setMountsLoading(false);
      return;
    }
    if (!machine.online) {
      setMountsReport(null);
      setMountsError({ kind: "offline", text: t("machines-storage.err_offline") });
      setMountsLoading(false);
      setConfirmState(null);
      return;
    }
    const mid = machine.id;
    const ctrl = new AbortController();
    setMountsReport((prev) => (prev && prev.machine_id === mid ? prev : null));
    setMountsLoading(true);
    setMountsError(null);
    api<MachineStorageReport>(`/machines/${enc(mid)}/storage`, { signal: ctrl.signal, timeout: pastHub(15_000) })
      .then((res) => {
        if (ctrl.signal.aborted || mid !== selectedIdRef.current) return;
        setMountsReport({ ...res, machine_id: res.machine_id || mid, mounts: res.mounts ?? [] });
        setMountsLoading(false);
      })
      .catch((e) => {
        if (ctrl.signal.aborted || isAbort(e) || mid !== selectedIdRef.current) return;
        setMountsError(classifyStorageError(e));
        setMountsLoading(false);
      });
    return () => ctrl.abort();
  }, [machine?.id, machine?.online, mountRefreshTick]);

  const activeMountsReport = useMemo(
    () => (machine?.online && mountsReport && mountsReport.machine_id === selectedId ? mountsReport : null),
    [machine?.online, mountsReport, selectedId],
  );

  const scanHotspots = useCallback(async (preserveCleanupResult = false) => {
    if (!machine || !machine.online) return;
    const mid = machine.id;
    hotspotsCtrlRef.current?.abort();
    previewCtrlRef.current?.abort();
    previewCtrlRef.current = null;
    setPreviewBusy(false);
    setConfirmState(null);
    const ctrl = new AbortController();
    hotspotsCtrlRef.current = ctrl;
    setHotspotsLoading(true);
    setHotspotsError(null);
    if (!preserveCleanupResult) {
      setCleanupResult(null);
      setCleanupError(null);
    }
    try {
      const res = await api<MachineStorageHotspotsReport>(`/machines/${enc(mid)}/storage/hotspots`, {
        signal: ctrl.signal,
        timeout: pastHub(45_000),
      });
      if (ctrl.signal.aborted || mid !== selectedIdRef.current) return;
      const normalized = { ...res, categories: res.categories ?? [] };
      setHotspotsReport(normalized);
      const nextPicked = new Set<string>();
      for (const c of normalized.categories) {
        if (c.cleanable && c.cleanup_target_id && !c.in_use && (c.reclaimable_bytes ?? c.bytes) > 0) {
          nextPicked.add(c.cleanup_target_id);
        }
      }
      setSelectedTargets(nextPicked);
      setConfirmState(null);
    } catch (e) {
      if (ctrl.signal.aborted || isAbort(e) || mid !== selectedIdRef.current) return;
      setHotspotsError(classifyStorageError(e));
    } finally {
      if (hotspotsCtrlRef.current === ctrl) {
        hotspotsCtrlRef.current = null;
        setHotspotsLoading(false);
      }
    }
  }, [machine]);

  const cleanableCategories = useMemo(
    () => (hotspotsReport?.categories ?? []).filter((c) => c.cleanable && !!c.cleanup_target_id && !c.in_use),
    [hotspotsReport],
  );

  const chosenTargetIds = useMemo(
    () => cleanableCategories.map((c) => c.cleanup_target_id!).filter((id) => selectedTargets.has(id)),
    [cleanableCategories, selectedTargets],
  );

  const estimatedSelectedBytes = useMemo(
    () => cleanableCategories.filter((c) => selectedTargets.has(c.cleanup_target_id!)).reduce((sum, c) => sum + (c.reclaimable_bytes ?? c.bytes), 0),
    [cleanableCategories, selectedTargets],
  );

  const toggleTarget = (targetId: string) => {
    previewCtrlRef.current?.abort();
    previewCtrlRef.current = null;
    setPreviewBusy(false);
    setConfirmState(null);
    setSelectedTargets((prev) => {
      const next = new Set(prev);
      if (next.has(targetId)) next.delete(targetId);
      else next.add(targetId);
      return next;
    });
  };

  const requestPreview = async () => {
    if (!machine?.online || chosenTargetIds.length === 0) return;
    const mid = machine.id;
    const targets = [...chosenTargetIds];
    previewCtrlRef.current?.abort();
    const ctrl = new AbortController();
    previewCtrlRef.current = ctrl;
    setPreviewBusy(true);
    setCleanupError(null);
    try {
      const res = await api<StorageCleanupResult>(`/machines/${enc(mid)}/storage/cleanup`, {
        method: "POST",
        json: { targets, dry_run: true },
        signal: ctrl.signal,
        timeout: pastHub(30_000),
      });
      if (ctrl.signal.aborted || mid !== selectedIdRef.current) return;
      setConfirmState({ targets, preview: { ...res, items: res.items ?? [] } });
    } catch (e) {
      if (ctrl.signal.aborted || isAbort(e) || mid !== selectedIdRef.current) return;
      setCleanupError(classifyStorageError(e));
    } finally {
      if (previewCtrlRef.current === ctrl) {
        previewCtrlRef.current = null;
        setPreviewBusy(false);
      }
    }
  };

  const confirmCleanup = async () => {
    if (!machine?.online || !confirmState || confirmState.targets.length === 0) return;
    const mid = machine.id;
    const targets = [...confirmState.targets];
    cleanupCtrlRef.current?.abort();
    const ctrl = new AbortController();
    cleanupCtrlRef.current = ctrl;
    setCleanupBusy(true);
    setCleanupError(null);
    try {
      const res = await api<StorageCleanupResult>(`/machines/${enc(mid)}/storage/cleanup`, {
        method: "POST",
        json: { targets, dry_run: false, confirm: true },
        signal: ctrl.signal,
        timeout: pastHub(120_000),
      });
      if (ctrl.signal.aborted || mid !== selectedIdRef.current) return;
      setCleanupResult({ ...res, items: res.items ?? [] });
      setConfirmState(null);
      setMountRefreshTick((n) => n + 1);
      await scanHotspots(true);
    } catch (e) {
      if (ctrl.signal.aborted || isAbort(e) || mid !== selectedIdRef.current) return;
      setCleanupError(classifyStorageError(e));
      setConfirmState(null);
    } finally {
      if (cleanupCtrlRef.current === ctrl) {
        cleanupCtrlRef.current = null;
        setCleanupBusy(false);
      }
    }
  };

  if (!machine) {
    return null;
  }

  return (
    <div className="space-y-4" data-testid="storage-panel">
      {machine && <UsageMap key={String(machine.online)} machine={machine} focus={focus && focus.mid === machine.id ? { mode: focus.mode, seq: focus.seq } : undefined} />}

      {/* Per-mount capacity & inode utilization */}
      <section className="rounded-card bg-surface px-4 py-3.5 shadow-card" data-testid="storage-mounts-section">
        <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-medium text-ink">{t("machines-storage.mounts_title")}</h3>
          {machine.online && <Button tone="secondary" icon="retry" busy={mountsLoading} onClick={() => setMountRefreshTick(n => n + 1)} testId="storage-refresh-mounts">
            {t("machines-storage.refresh_mounts")}
          </Button>}
          {activeMountsReport && activeMountsReport.supported && (
            <span className="text-submeta text-ink-3 tabular-nums">
              {t("machines-storage.collected_ms", { ms: activeMountsReport.collected_ms })}
            </span>
          )}
        </div>

        {mountsLoading && !activeMountsReport && (
          <div className="py-4">
            <Loading />
          </div>
        )}

        {mountsError && (
          <p role="alert" className="flex items-start gap-2 rounded-control bg-red-tint px-3 py-2 text-meta text-red" data-testid="storage-mounts-error">
            <Icon name="warning" size={14} className="mt-0.5 shrink-0" />
            <span>{mountsError.text}</span>
          </p>
        )}

        {activeMountsReport && !activeMountsReport.supported && (
          <p className="text-meta text-ink-3" data-testid="storage-mounts-unsupported">
            {t("machines-storage.mounts_unsupported", { os: activeMountsReport.os })}
          </p>
        )}

        {activeMountsReport && activeMountsReport.supported && activeMountsReport.mounts.length === 0 && (
          <p className="text-meta text-ink-3">{t("machines-storage.mounts_empty")}</p>
        )}

        {activeMountsReport && activeMountsReport.supported && activeMountsReport.mounts.length > 0 && (
          <ul className="divide-y divide-line" data-testid="storage-mounts-list">
            {activeMountsReport.mounts.map((mt) => {
              const inodePct = mt.inodes_percent;
              const inodeWarn = inodePct !== undefined && inodePct >= 80;
              const inodeCritical = inodePct !== undefined && inodePct >= 90;
              return (
                <li key={mt.mount} className="py-3 first:pt-0 last:pb-0" data-mount={mt.mount}>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-meta font-medium text-ink">{mt.mount}</span>
                    {mt.fs_type && <span className="rounded-full bg-inset px-2 py-0.5 font-mono text-submeta text-ink-3">{mt.fs_type}</span>}
                    {mt.read_only && <span className="rounded-full bg-field px-2 py-0.5 text-submeta text-ink-3">{t("machines-storage.read_only")}</span>}
                    {inodeWarn && (
                      <span
                        data-testid="inode-warning"
                        className={cx(
                          "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-submeta font-medium",
                          inodeCritical ? "bg-red-tint text-red" : "bg-orange-tint text-orange",
                        )}
                      >
                        <Icon name="warning" size={12} />
                        {inodeCritical ? t("machines-storage.inode_critical", { percent: percent(inodePct) }) : t("machines-storage.inode_warning", { percent: percent(inodePct) })}
                      </span>
                    )}
                    {mt.device && <span className="ml-auto truncate font-mono text-submeta text-ink-3">{mt.device}</span>}
                  </div>

                  <div className="mt-2 grid grid-cols-1 gap-3 md:grid-cols-2">
                    <div>
                      <div className="mb-1 flex items-baseline justify-between gap-2 text-submeta">
                        <span className="text-ink-3">{t("machines-storage.capacity_label")}</span>
                        <span className="text-ink-2 tabular-nums">
                          {t("machines-storage.capacity_detail", {
                            used: bytesBin(mt.used_bytes),
                            total: bytesBin(mt.total_bytes),
                            avail: bytesBin(mt.avail_bytes),
                            percent: percent(mt.used_percent),
                          })}
                        </span>
                      </div>
                      <UsageBar pct={mt.used_percent} label={t("machines-storage.capacity_label")} />
                    </div>
                    <div>
                      <div className="mb-1 flex items-baseline justify-between gap-2 text-submeta">
                        <span className="text-ink-3">{t("machines-storage.inodes_label")}</span>
                        <span className={cx("tabular-nums", inodeCritical ? "font-medium text-red" : inodeWarn ? "font-medium text-orange" : "text-ink-2")}>
                          {inodePct !== undefined && mt.inodes_total
                            ? t("machines-storage.inodes_detail", {
                                used: formatInodeCount(mt.inodes_used ?? 0),
                                total: formatInodeCount(mt.inodes_total),
                                percent: percent(inodePct),
                              })
                            : t("machines-storage.inodes_na")}
                        </span>
                      </div>
                      {inodePct !== undefined ? <UsageBar pct={inodePct} label={t("machines-storage.inodes_label")} /> : <div className="h-1.5 w-full rounded-full bg-inset opacity-50" />}
                    </div>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      {/* On-demand hotspot analysis & selective safe cleanup */}
      <details className="rounded-card bg-surface px-4 py-3.5 shadow-card" data-testid="storage-hotspots-section">
        <summary className="cursor-pointer py-2 text-label font-medium text-ink">{t("machines-storage.hotspots_title")}</summary>
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <p className="text-submeta text-ink-3">{t("machines-storage.hotspots_hint")}</p>
          </div>
          <Button tone="primary" icon="search" disabled={!machine?.online || hotspotsLoading || cleanupBusy} busy={hotspotsLoading} onClick={() => void scanHotspots()} testId="storage-scan-hotspots">
            {hotspotsReport ? t("machines-storage.rescan_hotspots") : t("machines-storage.scan_hotspots")}
          </Button>
        </div>

        {hotspotsError && (
          <p role="alert" className="mt-3 flex items-start gap-2 rounded-control bg-red-tint px-3 py-2 text-meta text-red" data-testid="storage-hotspots-error">
            <Icon name="warning" size={14} className="mt-0.5 shrink-0" />
            <span>{hotspotsError.text}</span>
          </p>
        )}

        {!hotspotsReport && !hotspotsLoading && !hotspotsError && (
          <p className="mt-3 text-meta text-ink-3" data-testid="storage-hotspots-idle">
            {t("machines-storage.hotspots_idle")}
          </p>
        )}

        {hotspotsReport && (
          <div className="mt-3 space-y-3" data-testid="storage-hotspots-results">
            <div className="flex flex-wrap items-center justify-between gap-2 text-submeta text-ink-3">
              <span>{t("machines-storage.scan_duration", { ms: hotspotsReport.duration_ms })}</span>
              {hotspotsReport.partial && (
                <span className="inline-flex items-center gap-1 text-orange" data-testid="storage-hotspots-partial">
                  <Icon name="info" size={12} />
                  {t("machines-storage.scan_partial")}
                </span>
              )}
            </div>

            <ul className="divide-y divide-line" data-testid="storage-hotspots-list">
              {hotspotsReport.categories.map((cat) => {
                const targetId = cat.cleanup_target_id;
                const canSelect = Boolean(cat.cleanable && targetId && !cat.in_use);
                const checked = Boolean(targetId && selectedTargets.has(targetId) && canSelect);
                return (
                  <li key={cat.id} className="py-2.5 first:pt-0 last:pb-0" data-category={cat.id}>
                    <div className="flex items-start gap-2.5">
                      {targetId && cat.cleanable ? (
                        <input
                          type="checkbox"
                          className="mt-1 accent-accent"
                          aria-label={targetTitle(targetId)}
                          checked={checked}
                          disabled={!canSelect || previewBusy || cleanupBusy}
                          onChange={() => toggleTarget(targetId)}
                          data-target-checkbox={targetId}
                        />
                      ) : (
                        <span className="mt-1 inline-block size-3.5 shrink-0" aria-hidden />
                      )}
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-baseline justify-between gap-2">
                          <span className="text-meta font-medium text-ink">{categoryTitle(cat.id)}</span>
                          <span className="font-mono text-meta text-ink-2 tabular-nums">
                            {cat.status === "unavailable" ? t("machines-storage.status_unavailable") : bytesBin(cat.bytes)}
                          </span>
                        </div>
                        <div className="mt-0.5 flex flex-wrap items-center gap-2 text-submeta text-ink-3">
                          {cat.path && <span className="truncate font-mono">{cat.path}</span>}
                          {cat.in_use && <span className="text-orange">{t("machines-storage.cache_in_use")}</span>}
                          {!cat.cleanable && cat.status !== "unavailable" && cat.bytes > 0 && <span>{t("machines-storage.advisory_only")}</span>}
                          {cat.cleanable && cat.reclaimable_bytes !== undefined && cat.reclaimable_bytes > 0 && cat.reclaimable_bytes !== cat.bytes && (
                            <span className="tabular-nums">{t("machines-storage.reclaimable", { size: bytesBin(cat.reclaimable_bytes) })}</span>
                          )}
                        </div>
                        {cat.items && cat.items.length > 0 && (
                          <ul className="mt-1.5 space-y-0.5 rounded-control bg-inset px-2.5 py-1.5">
                            {cat.items.slice(0, 6).map((item) => (
                              <li key={`${item.label}:${item.path ?? ""}`} className="flex items-baseline justify-between gap-2 text-submeta">
                                <span className="min-w-0 truncate font-mono text-ink-2">
                                  {item.label}
                                  {item.stale && <span className="ml-1.5 rounded bg-orange-tint px-1 text-orange">{t("machines-storage.item_stale")}</span>}
                                  {item.detail && !item.stale && <span className="ml-1.5 text-ink-3">({item.detail})</span>}
                                </span>
                                <span className="shrink-0 font-mono text-ink-3 tabular-nums">{bytesBin(item.bytes)}</span>
                              </li>
                            ))}
                          </ul>
                        )}
                      </div>
                    </div>
                  </li>
                );
              })}
            </ul>

            <div className="flex flex-wrap items-center gap-2 border-t border-line pt-3">
              <Button
                tone="secondary"
                icon="trash"
                disabled={chosenTargetIds.length === 0 || previewBusy || cleanupBusy}
                busy={previewBusy}
                onClick={() => void requestPreview()}
                testId="storage-preview-cleanup"
              >
                {t("machines-storage.preview_cleanup")}
              </Button>
              {chosenTargetIds.length > 0 && (
                <span className="text-submeta text-ink-3 tabular-nums">
                  {t("machines-storage.selected_summary", { count: chosenTargetIds.length, size: bytesBin(estimatedSelectedBytes) })}
                </span>
              )}
            </div>
            <p className="text-submeta text-ink-3">{t("machines-storage.cleanup_safety_note")}</p>
          </div>
        )}

        {cleanupError && (
          <p role="alert" className="mt-3 flex items-start gap-2 rounded-control bg-red-tint px-3 py-2 text-meta text-red" data-testid="storage-cleanup-error">
            <Icon name="warning" size={14} className="mt-0.5 shrink-0" />
            <span>{cleanupError.text}</span>
          </p>
        )}

        {cleanupResult && (
          <div className="mt-3 rounded-control bg-green-tint px-3 py-2.5 text-meta" role="status" data-testid="storage-cleanup-result">
            <p className="flex items-center gap-1.5 font-medium text-ink">
              <Icon name="check" size={14} className="text-green" />
              {t("machines-storage.cleanup_freed", { size: bytesBin(cleanupResult.freed_bytes) })}
            </p>
            <ul className="mt-1.5 space-y-0.5">
              {cleanupResult.items.map((it) => (
                <li key={it.target_id} className="flex items-baseline justify-between gap-2 text-submeta" data-cleanup-item={it.target_id} data-status={it.status}>
                  <span className="truncate text-ink-2">{targetTitle(it.target_id)}</span>
                  <span className={cx("shrink-0", it.status === "cleaned" ? "text-ink-3" : it.status === "failed" ? "text-red" : "text-orange")}>
                    {cleanupStatusText(it.status, it.error)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </details>

      {/* Confirmation dialog populated from frozen server dry-run preview */}
      <Dialog
        open={confirmState !== null}
        onClose={() => !cleanupBusy && setConfirmState(null)}
        title={t("machines-storage.confirm_title")}
        footer={
          <>
            <Button tone="quiet" disabled={cleanupBusy} onClick={() => setConfirmState(null)}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={cleanupBusy} onClick={() => void confirmCleanup()} testId="storage-confirm-cleanup">
              {t("machines-storage.confirm_action", { size: bytesBin(confirmState?.preview.estimated_bytes ?? estimatedSelectedBytes) })}
            </Button>
          </>
        }
      >
        <p className="text-meta leading-relaxed text-ink-2">
          {t("machines-storage.confirm_body", {
            machine: machine?.name ?? "",
            size: bytesBin(confirmState?.preview.estimated_bytes ?? estimatedSelectedBytes),
          })}
        </p>
        {confirmState && (
          <ul className="mt-2.5 space-y-1.5" data-testid="storage-preview-list">
            {confirmState.preview.items.map((it) => (
              <li key={it.target_id} className="rounded-control bg-inset px-2.5 py-1.5 text-meta">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="font-medium text-ink">{targetTitle(it.target_id)}</span>
                  <span className="font-mono text-ink-2 tabular-nums">{bytesBin(it.bytes)}</span>
                </div>
                <div className="flex items-baseline justify-between gap-2 text-submeta text-ink-3">
                  <span className="truncate font-mono">{it.path || it.action || it.target_id}</span>
                  <span>{cleanupStatusText(it.status, it.error)}</span>
                </div>
              </li>
            ))}
          </ul>
        )}
        <p className="mt-2.5 text-submeta text-ink-3">{t("machines-storage.confirm_safe_note")}</p>
      </Dialog>
    </div>
  );
}
