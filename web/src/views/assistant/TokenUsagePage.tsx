// Token usage (#10042, the iPhone's TokenUsagePage; docs/web/web.md): what the token calendar's line opens, in a
// dialog (a sheet on a phone; TokenCalendar.tsx). Today's, the last 7 and the last 30 days' tokens; the calendar again
// (only a picked day's line over it); the last 7 days by the hour; by computer, by agent and by kind over those 7
// days; the line that says what the count is: input, output, cache write and cache read added up, the same count as
// today's tokens on the Machines page; under the totals, by project (#16530, TokenProjects.tsx), whose picked project
// replaces the page. Read when it opens and when the tab comes back to the front, never on a timer.
// In the calendar's lazy chunk (no route and no chunk of its own: the first screen has no room for either); its strings
// are the calendar's namespace.
import { useEffect, useMemo, useRef, useState } from "react";
import { formatDate, formatNumber, t } from "../../i18n";
import { agentName } from "../../lib/agent-kind";
import { USAGE_COLOR, type UsageKind } from "../../lib/tokens";
import {
  dayTotal,
  fetchUsageDays,
  fetchUsageHours,
  hourLayout,
  lastDays,
  type UsageAgentShare,
  type UsageDays,
  type UsageHours,
} from "../../lib/usage-history";
import { fetchUsageProjects, type UsageProject, type UsageProjects } from "../../lib/usage-projects";
import { AgentMark } from "../../ui/AgentMark";
import { Loading } from "../../ui/atoms";
import TokenCalendar from "./TokenCalendar";
import { ProjectDetail, ProjectsSection } from "./TokenProjects";

/** What the page reads: a year of days (as many weeks as the page shows), and the last 7 days by the hour. */
const YEAR = 53 * 7;
// The squares of the hours (the calendar's look), their steps' strength over the theme color, inline as the calendar's.
const CELL = 12;
const GAP = 3;
const RADIUS = 2.5;
const STEPS = [28, 50, 75, 100];
const KINDS: UsageKind[] = ["cache_read", "cache_write", "out", "in"];

function fill(level: number): string {
  if (level < 0) return "transparent";
  if (level === 0) return "var(--field)";
  return level >= STEPS.length ? "var(--accent)" : `color-mix(in srgb, var(--accent) ${STEPS[level - 1]}%, transparent)`;
}

const short = (n: number) => formatNumber(n, { notation: "compact", maximumSignificantDigits: 3 });
const utc = (date: string) => new Date(`${date}T00:00:00Z`);
const hourName = (h: number) => formatDate(Date.UTC(2024, 0, 1, h), { hour: "numeric", timeZone: "UTC" });

/** The days, the hours and the projects (null from a hub without them), read when the page opens and when the tab
 *  comes back to the front. */
function useUsage(): { days: UsageDays | null; hours: UsageHours | null; projects: UsageProjects | null; failed: boolean } {
  const [days, setDays] = useState<UsageDays | null>(null);
  const [hours, setHours] = useState<UsageHours | null>(null);
  const [projects, setProjects] = useState<UsageProjects | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    const read = () => {
      fetchUsageDays(YEAR).then(
        (d) => {
          if (!live) return;
          setDays(d);
          setFailed(false);
        },
        () => live && setFailed(true), // keeps what was there
      );
      fetchUsageHours(7).then((h) => live && setHours(h), () => {});
      fetchUsageProjects().then((p) => live && setProjects(p), () => {});
    };
    read();
    const onVisible = () => document.visibilityState === "visible" && read();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      live = false;
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);
  return { days, hours, projects, failed };
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mt-5">
      <h2 className="mb-1.5 px-1 text-label font-medium text-ink-3">{title}</h2>
      <div className="rounded-card bg-surface px-4 py-3 shadow-card">{children}</div>
    </section>
  );
}

/** Rows of names with a bar against the largest and the number, the bars lined up whatever the names' widths. */
function Shares({ rows }: { rows: { key: string; name: React.ReactNode; total: number }[] }) {
  const top = Math.max(1, ...rows.map((r) => r.total));
  return (
    <div className="grid items-center gap-x-3 gap-y-2" style={{ gridTemplateColumns: "auto minmax(48px, 1fr) auto" }}>
      {rows.map((r) => (
        <div key={r.key} className="contents">
          <span className="flex min-w-0 items-center gap-2 text-meta text-ink">{r.name}</span>
          <div className="h-1 rounded-full bg-inset">
            <div className="h-1 rounded-full bg-accent" style={{ width: `${(100 * r.total) / top}%` }} />
          </div>
          <span className="text-right text-label text-ink-3 tabular-nums">{short(r.total)}</span>
        </div>
      ))}
    </div>
  );
}

/** An agent's row name: its mark and its name as Agents shows them (lib/agent-kind); usage kept before the hub counted
 *  agents apart has no agent ("", which agentName would read as Claude Code) and no mark. */
function agentRowName(a: UsageAgentShare): React.ReactNode {
  if (!a.agent) return <span className="truncate text-ink-3">{t("tokencalendar.not_split")}</span>;
  return (
    <>
      <AgentMark agent={a.agent} size={14} decorative />
      <span className="truncate">{agentName(a.agent)}</span>
    </>
  );
}

function HourGrid({ hours }: { hours: UsageHours }) {
  const layout = useMemo(() => hourLayout(hours.hours), [hours]);
  const [picked, setPicked] = useState<[number, number] | null>(null);
  const today = layout.dates.length - 1;
  const dayName = (r: number) =>
    r === today ? t("tokencalendar.today") : formatDate(utc(layout.dates[r]), { month: "short", day: "numeric", weekday: "short", timeZone: "UTC" });
  const move = (dr: number, dc: number) => {
    const [r, c] = picked ?? [today, 0];
    const nr = Math.min(today, Math.max(0, r + dr));
    const nc = Math.min(23, Math.max(0, c + dc));
    if (layout.levels[nr]?.[nc] !== undefined && layout.levels[nr][nc] >= 0) setPicked([nr, nc]);
  };
  return (
    <div>
      <div className="mb-2 text-meta text-ink-2" aria-live="polite" style={{ minHeight: "1.25em" }}>
        {picked &&
          t("tokencalendar.hour_line", { day: dayName(picked[0]), hour: hourName(picked[1]), tokens: short(layout.tokens[picked[0]][picked[1]]) })}
      </div>
      <div className="flex gap-2 text-submeta text-ink-3">
        <div aria-hidden className="grid shrink-0 text-right" style={{ gridTemplateRows: `repeat(${layout.dates.length}, ${CELL}px)`, rowGap: GAP }}>
          {layout.dates.map((d, r) => (
            <span key={d} className="whitespace-nowrap" style={{ lineHeight: `${CELL}px` }}>
              {dayName(r)}
            </span>
          ))}
        </div>
        <div className="min-w-0 flex-1">
          <div
            role="img"
            tabIndex={0}
            aria-label={t("tokencalendar.hour_grid")}
            onKeyDown={(e) => {
              const by = ({ ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1] } as Record<string, [number, number]>)[e.key];
              if (by) {
                e.preventDefault();
                move(by[0], by[1]);
              } else if (e.key === "Escape") setPicked(null);
            }}
            style={{ display: "grid", gridTemplateColumns: "repeat(24, minmax(0, 1fr))", gridAutoRows: `${CELL}px`, gap: GAP, outline: "none" }}
          >
            {layout.levels.map((row, r) =>
              row.map((level, c) => (
                <span
                  key={`${r}-${c}`}
                  title={level < 0 ? undefined : `${dayName(r)} ${hourName(c)} · ${short(layout.tokens[r][c])}`}
                  onClick={() => level >= 0 && setPicked(picked && picked[0] === r && picked[1] === c ? null : [r, c])}
                  style={{
                    background: fill(level),
                    borderRadius: RADIUS,
                    cursor: level < 0 ? undefined : "pointer",
                    boxShadow: picked && picked[0] === r && picked[1] === c ? "0 0 0 2px var(--ink)" : undefined,
                  }}
                />
              )),
            )}
          </div>
          <div aria-hidden className="relative mt-1" style={{ height: 14 }}>
            {[0, 6, 12, 18].map((h) => (
              <span key={h} className="absolute whitespace-nowrap" style={{ left: `${(100 * h) / 24}%` }}>
                {hourName(h)}
              </span>
            ))}
          </div>
        </div>
      </div>
      <Legend />
      <p className="mt-2 text-label text-ink-3">{t("tokencalendar.hour_note")}</p>
    </div>
  );
}

function Legend() {
  return (
    <div aria-hidden className="mt-2 flex items-center justify-end gap-1 text-submeta text-ink-3">
      <span className="mr-1">{t("tokencalendar.less")}</span>
      {[0, 1, 2, 3, 4].map((l) => (
        <span key={l} style={{ width: CELL, height: CELL, borderRadius: RADIUS, background: fill(l) }} />
      ))}
      <span className="ml-1">{t("tokencalendar.more")}</span>
    </div>
  );
}

function Kinds({ total }: { total: UsageHours["total"] }) {
  const all = Math.max(1, dayTotal({ ...total, date: "" }));
  const value = (k: UsageKind) => total[k] ?? 0;
  return (
    <div>
      <div className="flex h-1.5 overflow-hidden rounded-full bg-inset">
        {KINDS.map((k) => (
          <span key={k} style={{ width: `${(100 * value(k)) / all}%`, background: USAGE_COLOR[k] }} />
        ))}
      </div>
      <div className="mt-2 grid items-center gap-x-3 gap-y-1.5" style={{ gridTemplateColumns: "1fr auto auto" }}>
        {KINDS.map((k) => (
          <div key={k} className="contents text-meta">
            <span className="flex items-center gap-2 text-ink">
              <span aria-hidden className="inline-block h-2 w-2 shrink-0 rounded-full" style={{ background: USAGE_COLOR[k] }} />
              {t(`machines-usage.kind_${k}`)}
            </span>
            <span className="text-right text-label text-ink-3 tabular-nums">{short(value(k))}</span>
            <span className="text-right text-label text-ink-3 tabular-nums">
              {formatNumber(value(k) / all, { style: "percent", maximumSignificantDigits: 3 })}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

/** project: the project to show instead of the page (By project's breakdown, the dialog's title going back), picked
 *  with onProject. Its range (today, 3 or 7 days) is the list's: the one picked stays. */
export default function TokenUsage({ project, onProject }: { project?: UsageProject | null; onProject?: (p: UsageProject | null) => void } = {}) {
  const { days, hours, projects, failed } = useUsage();
  const [range, setRange] = useState(2); // the last 7 days, as the page's other sections
  const top = useRef<HTMLDivElement>(null);
  const shown = useRef(project?.key);
  useEffect(() => { // into a project or back: from its top (the dialog keeps its scroll otherwise)
    if (shown.current === project?.key) return;
    shown.current = project?.key;
    top.current?.scrollIntoView?.({ block: "start" });
  }, [project?.key]);
  const machines = (hours?.machines ?? []).filter((m) => dayTotal({ ...m, date: "" }) > 0);
  const agents = (hours?.agents ?? []).filter((a) => dayTotal({ ...a, date: "" }) > 0);
  if (project) {
    return (
      <div ref={top} data-token-usage-page>
        <ProjectDetail project={project} range={range} onRange={setRange} />
      </div>
    );
  }
  return (
    <div ref={top} data-token-usage-page>
          {days && (
            <>
              <div className="grid grid-cols-3 gap-3 rounded-card bg-surface px-4 py-3 shadow-card">
                {(
                  [
                    ["today", 1],
                    ["last_7", 7],
                    ["last_30", 30],
                  ] as const
                ).map(([key, n]) => (
                  <div key={key} className="min-w-0">
                    <div className="truncate text-label text-ink-3">{t(`tokencalendar.${key}`)}</div>
                    <div className="truncate text-card-title font-semibold text-ink tabular-nums">{short(lastDays(days.days, n))}</div>
                  </div>
                ))}
              </div>
              {projects && onProject && <ProjectsSection data={projects} range={range} onRange={setRange} onOpen={onProject} />}
              <Section title={t("tokencalendar.each_day")}>
                <TokenCalendar days={days} page />
                <Legend />
              </Section>
            </>
          )}
          {hours && (
            <>
              <Section title={t("tokencalendar.by_hour")}>
                <HourGrid hours={hours} />
              </Section>
              {machines.length > 0 && (
                <Section title={t("tokencalendar.by_computer")}>
                  <Shares
                    rows={machines.map((m) => ({
                      key: m.id,
                      name: <span className="truncate">{m.name || t("tokencalendar.removed_computer")}</span>,
                      total: dayTotal({ ...m, date: "" }),
                    }))}
                  />
                </Section>
              )}
              {agents.length > 0 && (
                <Section title={t("tokencalendar.by_agent")}>
                  <Shares rows={agents.map((a) => ({ key: a.agent || "-", name: agentRowName(a), total: dayTotal({ ...a, date: "" }) }))} />
                </Section>
              )}
              <Section title={t("tokencalendar.by_kind")}>
                <Kinds total={hours.total} />
              </Section>
              <p className="mt-3 px-1 text-label text-ink-3">{t("tokencalendar.count_note")}</p>
            </>
          )}
          {!days && !hours && (failed ? <p className="py-10 text-center text-meta text-ink-3">{t("tokencalendar.failed")}</p> : <Loading />)}
    </div>
  );
}
