// Cache recovery on computers that predate the disk usage map (#8037): each volume, the largest folders under home (the node says when its bounded
// scan was cut short), and the rebuildable caches it found, each with a checkbox: the ones nothing uses are picked, one
// a running tool uses can't be. Clean selected → a confirmation naming every folder and the total → POST …/cleanup →
// what the volume really gained (freed_bytes, measured by the node) and each item's outcome; then the diagnosis again.
import { useEffect, useMemo, useState } from "react";
import { t } from "../../i18n";
import { bytesBin, percent } from "../../lib/machine-detail";
import { cleanup, diagnosisFailure, type CleanupResult, type DiagnosisCache, type DiagnosisFailure } from "../../lib/machine-diagnosis";
import type { Machine } from "../../lib/types";
import { Button, cx, Dialog } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { DiagnosisLoading, DiagnosisNote, SheetPart, ShareBar, useDiagnosis } from "./AlertParts";

const SECTIONS = ["disk"] as const;

/** A cache's name for people, by its catalog id; the id itself for one this page does not know. */
export function cacheTitle(id: string): string {
  switch (id) {
    case "go-build":
      return t("machines-alerts.cache_go_build");
    case "go-mod":
      return t("machines-alerts.cache_go_mod");
    case "npm-cache":
      return t("machines-alerts.cache_npm");
    case "pip-cache":
      return t("machines-alerts.cache_pip");
    case "uv-cache":
      return t("machines-alerts.cache_uv");
    case "yarn-cache":
      return t("machines-alerts.cache_yarn");
    case "homebrew-cache":
      return t("machines-alerts.cache_homebrew");
    case "xcode-derived-data":
      return t("machines-alerts.cache_xcode");
    case "gradle-cache":
      return t("machines-alerts.cache_gradle");
    case "cargo-registry":
      return t("machines-alerts.cache_cargo");
    default:
      return id;
  }
}

export function cleanupStatusText(status: string, error?: string): string {
  switch (status) {
    case "cleaned":
      return t("machines-alerts.status_cleaned");
    case "in_use":
      return t("machines-alerts.status_in_use");
    case "missing":
      return t("machines-alerts.status_missing");
    default:
      return error ? t("machines-alerts.status_failed_why", { error }) : t("machines-alerts.status_failed");
  }
}

export function LegacyCacheRecovery({ machine }: { machine: Machine }) {
  const { read, reload } = useDiagnosis(machine.id, SECTIONS, machine.online);
  const data = read.state === "ok" ? read.data.disk : undefined;
  const caches = useMemo(() => data?.caches ?? [], [data]);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<CleanupResult | null>(null);
  const [failure, setFailure] = useState<DiagnosisFailure | null>(null);
  // each new read: the caches nothing uses are picked, in-use ones never
  useEffect(() => setPicked(new Set(caches.filter((c) => !c.in_use).map((c) => c.id))), [caches]);
  const chosen = caches.filter((c) => picked.has(c.id) && !c.in_use);
  const total = chosen.reduce((n, c) => n + c.bytes, 0);

  const run = async () => {
    setBusy(true);
    setFailure(null);
    try {
      setResult(await cleanup(machine.id, chosen.map((c) => c.id)));
      setAsking(false);
      reload();
    } catch (e) {
      setFailure(diagnosisFailure(e));
      setAsking(false);
    } finally {
      setBusy(false);
    }
  };
  const toggle = (c: DiagnosisCache) => {
    const next = new Set(picked);
    if (next.has(c.id)) next.delete(c.id);
    else next.add(c.id);
    setPicked(next);
  };

  return (
    <>
      <SheetPart part="what">
        {data?.volumes.length ? (
          <ul className="space-y-2" data-testid="volumes">
            {data.volumes.map((v) => {
              const pct = v.total ? (100 * v.used) / v.total : 0;
              return (
                <li key={v.mount}>
                  <div className="flex items-baseline gap-2 text-meta">
                    <span className="min-w-0 flex-1 truncate font-mono text-ink">{v.mount}</span>
                    <span className="shrink-0 text-ink-2 tabular-nums">{t("machines-alerts.volume_used", { used: bytesBin(v.used), total: bytesBin(v.total), percent: percent(pct) })}</span>
                  </div>
                  <div className="mt-1"><ShareBar share={pct / 100} tone={pct >= 90 ? "critical" : pct >= 80 ? "warning" : undefined} label={percent(pct)} /></div>
                </li>
              );
            })}
          </ul>
        ) : null}
      </SheetPart>
      <SheetPart part="why">
        {read.state === "loading" && <DiagnosisLoading label={t("machines-alerts.disk_loading")} />}
        {read.state === "failed" && <DiagnosisNote failure={read.failure} onRetry={reload} />}
        {data && (
          <>
            <h4 className="mb-1 text-meta font-medium text-ink">{t("machines-alerts.folders_title")}</h4>
            {data.folders.length ? (
              <ul className="divide-y divide-line" data-testid="folders">
                {data.folders.map((f) => (
                  <li key={f.path} className="flex items-baseline gap-2 py-1 text-meta">
                    <span className="min-w-0 flex-1 truncate font-mono text-ink">{f.path}</span>
                    <span className="shrink-0 text-ink-2 tabular-nums">{bytesBin(f.bytes)}</span>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-meta text-ink-3">{t("machines-alerts.folders_none")}</p>
            )}
            {data.partial && <p className="mt-1 text-submeta text-ink-3" data-testid="folders-partial">{t("machines-alerts.folders_partial")}</p>}
          </>
        )}
      </SheetPart>
      <SheetPart part="do">
        {read.state === "failed" && <p className="text-meta text-ink-3">{t("machines-alerts.caches_unavailable")}</p>}
        {data && (
          <>
            <h4 className="mb-1 text-meta font-medium text-ink">{t("machines-alerts.caches_title")}</h4>
            {caches.length ? (
              <ul className="space-y-0.5" data-testid="caches">
                {caches.map((c) => (
                  <li key={c.id}>
                    <label className={cx("flex items-start gap-2 rounded-control px-1 py-1.5", c.in_use ? "opacity-60" : "cursor-pointer hover:bg-hover")} data-cache={c.id}>
                      <input type="checkbox" className="mt-0.5 accent-accent" checked={picked.has(c.id) && !c.in_use} disabled={c.in_use || busy} onChange={() => toggle(c)} />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-baseline gap-2 text-meta">
                          <span className="min-w-0 flex-1 truncate text-ink">{cacheTitle(c.id)}</span>
                          <span className="shrink-0 text-ink-2 tabular-nums">{bytesBin(c.bytes)}</span>
                        </span>
                        <span className="block truncate font-mono text-submeta text-ink-3">{c.path}</span>
                        {c.in_use && <span className="block text-submeta text-orange">{t("machines-alerts.cache_in_use")}</span>}
                      </span>
                    </label>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-meta text-ink-3">{t("machines-alerts.caches_none")}</p>
            )}
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Button tone="secondary" icon="trash" disabled={!chosen.length || busy} onClick={() => setAsking(true)} testId="clean-selected">
                {t("machines-alerts.clean_selected")}
              </Button>
              {chosen.length > 0 && <span className="text-submeta text-ink-3 tabular-nums">{t("machines-alerts.clean_total", { count: chosen.length, size: bytesBin(total) })}</span>}
            </div>
            <p className="mt-1.5 text-submeta text-ink-3">{t("machines-alerts.clean_note")}</p>
          </>
        )}
        {failure && <div className="mt-2"><DiagnosisNote failure={failure} onRetry={() => setAsking(true)} /></div>}
        {result && <CleanupOutcome result={result} />}
      </SheetPart>
      <Dialog
        open={asking}
        onClose={() => !busy && setAsking(false)}
        title={t("machines-alerts.clean_confirm_title")}
        footer={
          <>
            <Button tone="quiet" disabled={busy} onClick={() => setAsking(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={busy} onClick={() => void run()} testId="clean-confirm">
              {t("machines-alerts.clean_confirm", { size: bytesBin(total) })}
            </Button>
          </>
        }
      >
        <p className="text-meta leading-relaxed text-ink-2">{t("machines-alerts.clean_confirm_body", { machine: machine.name, size: bytesBin(total) })}</p>
        <ul className="mt-2 space-y-1" data-testid="clean-list">
          {chosen.map((c) => (
            <li key={c.id} className="text-meta">
              <div className="flex items-baseline gap-2">
                <span className="min-w-0 flex-1 truncate text-ink">{cacheTitle(c.id)}</span>
                <span className="shrink-0 text-ink-2 tabular-nums">{bytesBin(c.bytes)}</span>
              </div>
              <div className="truncate font-mono text-submeta text-ink-3">{c.path}</div>
            </li>
          ))}
        </ul>
        <p className="mt-2 text-submeta text-ink-3">{t("machines-alerts.clean_confirm_safe")}</p>
      </Dialog>
    </>
  );
}

function CleanupOutcome({ result }: { result: CleanupResult }) {
  const failed = result.items.filter((i) => i.status !== "cleaned");
  return (
    <div className={cx("mt-3 rounded-control px-3 py-2 text-meta", failed.length ? "bg-field" : "bg-green-tint")} role="status" data-testid="cleanup-result">
      <p className="flex items-center gap-1.5 font-medium text-ink">
        <Icon name={failed.length ? "info" : "check"} size={14} className={failed.length ? "text-ink-3" : "text-green"} />
        {t("machines-alerts.freed", { size: bytesBin(result.freed_bytes) })}
      </p>
      <ul className="mt-1 space-y-0.5">
        {result.items.map((i) => (
          <li key={i.id} className="flex items-baseline gap-2 text-submeta" data-item={i.id} data-status={i.status}>
            <span className="min-w-0 flex-1 truncate text-ink-2">{cacheTitle(i.id)}</span>
            <span className={cx("shrink-0", i.status === "cleaned" ? "text-ink-3" : i.status === "failed" ? "text-red" : "text-orange")}>{cleanupStatusText(i.status, i.error)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
