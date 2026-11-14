// "Archived agents" (`/agents/archived`, docs/web/web.md "Archived agents"; Android's AgentArchivesScreen): every agent
// archived from the Agents list, newest activity first, searchable on its title, folder, computer, agent and last
// words. A row opens its chat (read from its transcript); "Restore" puts it back in the Agents list. What the hub
// lists is what is archived: a session with new activity comes back by itself and leaves this list.
import { useCallback, useEffect, useMemo, useState } from "react";
import { appArchiveDeps, AGENT_ARCHIVES_FEATURE, listArchived, noteArchivedList, restoreAgent } from "../../lib/agent-archives";
import { agentName, sessionAgent } from "../../lib/agent-kind";
import { errorText } from "../../lib/api";
import { ago, baseName, sessionTitle } from "../../lib/format";
import { machineWithHub } from "../../lib/mesh";
import { href, navigate, onLinkClick, type Route } from "../../lib/router";
import { useStore } from "../../lib/store";
import type { Machine, Session } from "../../lib/types";
import { SessionMark } from "../../ui/AgentMark";
import { Button, IconButton, Loading, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Empty } from "../assistant/parts";
import { t } from "../../i18n";

/** The rows whose title, folder, computer, agent or last words hold `query` (case-insensitive). */
export function filterArchived(rows: Session[], query: string, machines: Machine[]): Session[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return rows;
  return rows.filter((s) =>
    [sessionTitle(s), s.cwd, machineWithHub(s.machine_id, machines), agentName(sessionAgent(s).agent, s.agent_label), s.last_text ?? ""]
      .some((x) => x.toLowerCase().includes(needle)),
  );
}

export function AgentArchives({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const supported = useStore((s) => s.features.includes(AGENT_ARCHIVES_FEATURE));
  const featuresKnown = useStore((s) => s.features.length > 0);
  const machines = useStore((s) => s.machines);
  const [rows, setRows] = useState<Session[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("");
  const [restoring, setRestoring] = useState<Set<string>>(() => new Set());
  const nowMs = useNow(60_000);

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

  const hits = useMemo(() => filterArchived(rows ?? [], query, machines), [rows, query, machines]);
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
              <label className="mb-2 flex h-9 items-center gap-2 rounded-control bg-surface px-3 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
                <Icon name="search" size={13} stroke={2} className="text-ink-3" />
                <input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={(e) => e.key === "Escape" && setQuery("")}
                  placeholder={t("agents-archives.search_placeholder")}
                  aria-label={t("agents-archives.search_placeholder")}
                  className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
                />
                {query && <IconButton icon="x" label={t("agents-archives.clear_search")} size={22} iconSize={12} onClick={() => setQuery("")} />}
              </label>
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
              {rows !== null && rows.length > 0 && hits.length === 0 && <Empty icon="search" title={t("agents-archives.no_results", { query: query.trim() })} />}
              {hits.length > 0 && (
                <div data-agent-archives className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
                  {hits.map((s) => (
                    <ArchivedRow key={keyOf(s)} s={s} now={nowMs} machine={machineWithHub(s.machine_id, machines)} busy={restoring.has(keyOf(s))} onRestore={() => void restore(s)} />
                  ))}
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
  const where = [machine, baseName(s.cwd), agentName(sessionAgent(s).agent, s.agent_label)].filter(Boolean).join(" · ");
  return (
    <div className="group relative flex items-center gap-2 pr-3 transition-colors hover:bg-hover">
      <a href={href(route)} onClick={(e) => onLinkClick(e, route)} title={[title, s.cwd].filter(Boolean).join("\n")} className="flex min-w-0 flex-1 items-center gap-3 py-2.5 pl-4 focus-visible:outline-none">
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
