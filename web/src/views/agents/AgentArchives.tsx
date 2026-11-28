// "Archived agents" (`/agents/archived`, docs/web/web.md "Archived agents"; Android's AgentArchivesScreen): every agent
// archived from the Agents list, newest activity first. Search and filter locally, then reveal matches in batches.
// A row opens its transcript. Restore returns it to Agents, as does new activity on its computer.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { appArchiveDeps, AGENT_ARCHIVES_FEATURE, listArchived, noteArchivedList, restoreAgent } from "../../lib/agent-archives";
import { agentId, agentName, sessionAgent, sessionAgentName } from "../../lib/agent-kind";
import { errorText } from "../../lib/api";
import { ago, baseName, sessionTitle } from "../../lib/format";
import { machineWithHub } from "../../lib/mesh";
import { href, navigate, onLinkClick, type Route } from "../../lib/router";
import { useStore } from "../../lib/store";
import type { Machine, Session } from "../../lib/types";
import { SessionMark } from "../../ui/AgentMark";
import { Button, IconButton, Loading, useNow } from "../../ui/atoms";
import { SearchField } from "../../ui/SearchField";
import { Empty } from "../assistant/parts";
import { t } from "../../i18n";

const BATCH_SIZE = 40;
const selectClass = "min-h-11 min-w-0 grow basis-36 rounded-control bg-inset px-2 py-1 text-xs text-ink shadow-hairline focus-visible:outline-accent";

function runtimeId(s: Session): string {
  return agentId(sessionAgent(s).agent);
}

/** Search within the exact computer and runtime, preserving the archive list's order. */
export function filterArchived(rows: Session[], query: string, machines: Machine[], machine = "", runtime = ""): Session[] {
  const needle = query.trim().toLowerCase();
  if (!needle && !machine && !runtime) return rows;
  return rows.filter((s) => {
    if (machine && s.machine_id !== machine) return false;
    if (runtime && runtimeId(s) !== runtime) return false;
    return !needle || [sessionTitle(s), s.cwd, machineWithHub(s.machine_id, machines), sessionAgentName(s), s.last_text ?? ""]
      .some((x) => x.toLowerCase().includes(needle));
  });
}

export function AgentArchives({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const supported = useStore((s) => s.features.includes(AGENT_ARCHIVES_FEATURE));
  const featuresKnown = useStore((s) => s.features.length > 0);
  const machines = useStore((s) => s.machines);
  const [rows, setRows] = useState<Session[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState({ query: "", machine: "", runtime: "" });
  const { query, machine, runtime } = filters;
  const [limit, setLimit] = useState(BATCH_SIZE);
  const list = useRef<HTMLDivElement>(null);
  const [restoring, setRestoring] = useState<Set<string>>(() => new Set());
  const nowMs = useNow(60_000);

  const changeFilter = (key: keyof typeof filters, value: string) => {
    setFilters((previous) => ({ ...previous, [key]: value }));
    setLimit(BATCH_SIZE);
  };

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    try {
      const list = await listArchived(signal);
      if (signal?.aborted) return;
      setRows(list);
      noteArchivedList(list);
      setError("");
    } catch (e) {
      if (!signal?.aborted) setError(errorText(e));
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, []);
  useEffect(() => {
    if (!supported) return;
    const c = new AbortController();
    void load(c.signal);
    return () => c.abort();
  }, [supported, load]);

  const { machineIds, runtimes } = useMemo(() => {
    const machineIds = new Set<string>();
    const runtimes = new Map<string, string>();
    for (const s of rows ?? []) {
      machineIds.add(s.machine_id);
      const id = runtimeId(s);
      if (!runtimes.has(id)) runtimes.set(id, sessionAgentName(s));
    }
    // A refresh or restore can remove the last matching row. Keep the selected option clearable.
    if (machine) machineIds.add(machine);
    if (runtime && !runtimes.has(runtime)) runtimes.set(runtime, agentName(runtime));
    return { machineIds, runtimes };
  }, [rows, machine, runtime]);
  const hits = useMemo(() => filterArchived(rows ?? [], query, machines, machine, runtime), [rows, query, machines, machine, runtime]);
  const visible = hits.slice(0, limit);
  const showMore = () => {
    flushSync(() => setLimit((count) => count + BATCH_SIZE));
    // Continue keyboard navigation from the first new row, including when the last batch removes the button.
    list.current?.children.item(visible.length)?.querySelector("a")?.focus();
  };
  const keyOf = (s: Session) => `${s.machine_id}/${s.session_id}`;

  const restore = async (s: Session) => {
    setRestoring((r) => new Set(r).add(keyOf(s)));
    // gone from here at once; a refusal reloads the list, which puts it back
    const ok = await restoreAgent(s, { ...appArchiveDeps, refresh: () => Promise.all([appArchiveDeps.refresh(), load()]) });
    if (ok) setRows((list) => (list ?? []).filter((x) => keyOf(x) !== keyOf(s)));
    setRestoring((r) => {
      const next = new Set(r);
      next.delete(keyOf(s));
      return next;
    });
  };

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("assistant-history.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        {/* a page under the Agents list: back to it, as a request's page and the settings' pages go back to theirs; the
            list keeps its grouping and filter (stored in this browser) */}
        <IconButton icon="chevronLeft" label={t("agents-archives.back")} onClick={() => navigate({ name: "agent-list" })} />
        <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">
          {t("agents-archives.title")}
          {rows && rows.length > 0 && <span className="ml-2 text-[12px] font-normal text-ink-3 tabular-nums">{rows.length}</span>}
        </h1>
        {supported && <IconButton icon="retry" label={t("agents-archives.refresh")} onClick={() => void load()} disabled={loading} />}
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[760px] px-4 py-5 sm:px-8 sm:py-6">
          {!supported && featuresKnown ? (
            <Empty icon="archive" title={t("agents-archives.unsupported_title")}>
              {t("agents-archives.unsupported_body")}
            </Empty>
          ) : (
            <>
              <SearchField value={query} onChange={(value) => changeFilter("query", value)} size="md" className="mb-2" placeholder={t("agents-archives.search_placeholder")} />
              {rows && (rows.length > 0 || machine || runtime) && (
                <div className="mb-2 flex flex-wrap gap-2">
                  <select aria-label={t("agents-archives.computer")} className={selectClass} value={machine} onChange={(e) => changeFilter("machine", e.target.value)}>
                    <option value="">{t("agents-archives.all_computers")}</option>
                    {[...machineIds].map((id) => <option key={id} value={id}>{machineWithHub(id, machines)}</option>)}
                  </select>
                  <select aria-label={t("agents-archives.runtime")} className={selectClass} value={runtime} onChange={(e) => changeFilter("runtime", e.target.value)}>
                    <option value="">{t("agents-archives.all_runtimes")}</option>
                    {[...runtimes].map(([id, label]) => <option key={id} value={id}>{label}</option>)}
                  </select>
                </div>
              )}
              <p className="mb-5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("agents-archives.description")}</p>

              {rows === null && !error && (
                <div className="flex justify-center py-16">
                  <Loading />
                </div>
              )}
              {error && (
                <Empty icon="info" title={t("agents-archives.load_failed")} action={<Button tone="secondary" icon="retry" busy={loading} onClick={() => void load()}>{t("agents-archives.retry")}</Button>}>
                  {error}
                </Empty>
              )}
              {rows !== null && !error && rows.length === 0 && (
                <Empty icon="archive" title={t("agents-archives.empty_title")}>
                  {t("agents-archives.empty_body")}
                </Empty>
              )}
              {rows !== null && rows.length > 0 && hits.length === 0 && <Empty icon="search" title={query.trim() ? t("agents-archives.no_results", { query: query.trim() }) : t("agents-archives.no_filter_results")} />}
              {hits.length > 0 && (hits.length > BATCH_SIZE || machine || runtime || query.trim()) && (
                <p role="status" className="mb-2 px-1 text-xs text-ink-3 tabular-nums">
                  {t("agents-archives.showing", { shown: visible.length, matches: hits.length })}
                </p>
              )}
              {hits.length > 0 && (
                <div ref={list} data-agent-archives className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
                  {visible.map((s) => (
                    <ArchivedRow key={keyOf(s)} s={s} now={nowMs} machine={machineWithHub(s.machine_id, machines)} busy={restoring.has(keyOf(s))} onRestore={() => void restore(s)} />
                  ))}
                </div>
              )}
              {visible.length < hits.length && (
                <div className="mt-3 flex justify-center">
                  <Button className="min-h-11" onClick={showMore}>{t("agents-archives.show_more")}</Button>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function ArchivedRow({ s, now, machine, busy, onRestore }: { s: Session; now: number; machine: string; busy: boolean; onRestore: () => void }) {
  const route: Route = { name: "session", mid: s.machine_id, sid: s.session_id };
  const title = sessionTitle(s);
  const where = [machine, baseName(s.cwd), sessionAgentName(s)].filter(Boolean).join(" · ");
  return (
    <div className="group relative flex items-center gap-2 pr-3 transition-colors hover:bg-hover">
      <a href={href(route)} onClick={(e) => onLinkClick(e, route)} title={[title, s.cwd].filter(Boolean).join("\n")} className="flex min-w-0 flex-1 items-center gap-3 py-2.5 pl-4 focus-visible:-outline-offset-2 focus-visible:outline-accent">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-field">
          <SessionMark s={s} size={16} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-baseline gap-2">
            <span className="min-w-0 flex-1 truncate text-[13.5px] font-medium text-ink">{title}</span>
            <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{ago(s.updated_at, now)}</span>
          </span>
          <span className="mt-0.5 block truncate text-[12px] text-ink-3">{where}</span>
          {s.last_text && <span className="mt-0.5 block truncate text-[12px] text-ink-2">{s.last_text}</span>}
        </span>
      </a>
      <Button tone="secondary" icon="undo" busy={busy} onClick={onRestore} title={t("agents-archives.restore_label", { title })}>
        {t("agents-archives.restore")}
      </Button>
    </div>
  );
}
