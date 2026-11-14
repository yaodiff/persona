import { useEffect, useState } from "react";
import { api } from "../../lib/api";
import { ownerHeaders, useChosenManager, useShownManager } from "../../lib/manager-choice";
import { mainRuntimeSummary, type MainExecution } from "../../lib/main-runtime";
import { useStore } from "../../lib/store";

function useMainExecution(main: string | undefined, machine: string | undefined): MainExecution | undefined {
  const manager = useShownManager();
  const chosen = useChosenManager();
  const conn = useStore((s) => s.conn);
  const [data, setData] = useState<{ manager: string; report: MainExecution }>();
  useEffect(() => {
    if (!main) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let pending = false;
    const load = async () => {
      if (document.visibilityState === "hidden" || pending) return;
      if (timer) clearTimeout(timer);
      pending = true;
      try {
        const report = await api<MainExecution>("/assistant?view=main-runtime", {
          headers: ownerHeaders(chosen ?? undefined), signal: controller.signal,
        });
        if (!controller.signal.aborted) {
          setData(report.session_id === main && report.state ? { manager, report } : undefined);
        }
      } catch {
        if (!controller.signal.aborted) setData(undefined);
      } finally {
        pending = false;
        if (!controller.signal.aborted) timer = setTimeout(load, 15_000);
      }
    };
    const visible = () => { if (document.visibilityState === "visible") void load(); };
    void load();
    document.addEventListener("visibilitychange", visible);
    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [main, machine, manager, chosen, conn]);
  if (data?.manager !== manager || data.report.session_id !== main ||
      (machine && data.report.machine_id && data.report.machine_id !== machine)) return undefined;
  return data.report;
}

/** A separate, compact header line so the main badge never competes with composer controls. */
export function MainRuntimeBadge({ sid, starting }: { sid?: string; starting?: boolean }) {
  const assistant = useStore((s) => s.assistant);
  const main = assistant?.main;
  const machine = assistant?.main_machine_id;
  const show = !sid || sid === main;
  const report = useMainExecution(show ? main : undefined, machine);
  const [expanded, setExpanded] = useState(false);
  if (!show || (!assistant && !starting)) return null;
  const summary = mainRuntimeSummary(main, report, starting);
  // nothing known about where the main chat runs: no line at all (not "not reported")
  if (!summary) return null;
  return (
    <div className="flex shrink-0 justify-end px-3 pb-1 sm:px-4">
      <button type="button" data-main-runtime className="group relative min-w-0 max-w-full rounded-control px-2 py-1 text-xs text-ink-2 hover:bg-hover focus-visible:outline focus-visible:outline-accent"
        aria-label={summary} aria-expanded={expanded}
        onClick={() => setExpanded((open) => !open)} onBlur={() => setExpanded(false)}
        onKeyDown={(e) => { if (e.key === "Escape") { e.preventDefault(); setExpanded(false); } }}>
        <span className="block truncate">{summary}</span>
        <span aria-hidden className={`pointer-events-none absolute end-0 top-full z-20 max-w-full w-max rounded-control bg-tooltip-bg px-2 py-1 text-start text-tooltip-fg shadow-overlay [overflow-wrap:anywhere] ${expanded ? "block" : "hidden group-hover:block group-focus-visible:block"}`}>
          {summary}
        </span>
      </button>
    </div>
  );
}
