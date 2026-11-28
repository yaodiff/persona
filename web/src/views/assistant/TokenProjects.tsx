// Token usage by project (#16530, docs/web/web.md "Token usage"): on the Token usage page, today's, the last 3 and
// the last 7 days' tokens by project across computers, development (git repositories) and non-development (folders,
// the home folder, temporary folders, the assistant) apart, largest first; a project opens its breakdown in the same
// dialog (by day or hour, computer, Agent, model, launcher, and its worktrees and folders). What a project is, its
// share and its computers are the hub's (lib/usage-projects.ts); this draws them. In the calendar's chunk with the
// page; its strings are the calendar's namespace. Colors: development the accent, non-development --project-other.
import { useEffect, useMemo, useState } from "react";
import { formatDate, formatNumber, t } from "../../i18n";
import { agentName } from "../../lib/agent-kind";
import { USAGE_COLOR, type UsageKind } from "../../lib/tokens";
import {
  fetchUsageProjectDetail,
  PROJECT_RANGES,
  projectGroups,
  projectName,
  projectTotal,
  sharePercent,
  type ProjectTokens,
  type UsagePart,
  type UsageProject,
  type UsageProjectDetail,
  type UsageProjectRange,
  type UsageProjects,
} from "../../lib/usage-projects";
import { loadLaunchers } from "../../lib/launcher-list";
import { modelLabel } from "../../lib/session-models";
import { useMedia } from "../../lib/viewport";
import { AgentMark } from "../../ui/AgentMark";
import { Loading } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { SHEET_QUERY } from "../../ui/overlay";
import { Segmented } from "./parts";

const short = (n: number) => formatNumber(n, { notation: "compact", maximumSignificantDigits: 3 });
const KINDS: UsageKind[] = ["cache_read", "cache_write", "out", "in"];
/** How many of a group's projects show before "Show N more". */
const FIRST = 6;
/** A row of the list on a wide screen: mark, name, bar, tokens, share, chevron; fixed number columns, so every row's
 *  edges line up. */
const COLUMNS = "16px minmax(0, 1.25fr) minmax(48px, 1fr) 56px 40px 14px";

const KIND_ICON: Record<UsageProject["kind"], IconName> = {
  repo: "git",
  local: "commit",
  folder: "folder",
  home: "home",
  temp: "clock",
  assistant: "sparkles",
  unknown: "folder",
};

/** A group's color: development the accent, non-development its own, what nothing resolved (and not split) grey. */
function groupColor(group: UsageProject["group"] | "not_split"): string {
  if (group === "development") return "var(--accent)";
  if (group === "other") return "var(--project-other)";
  return "color-mix(in srgb, var(--ink-3) 40%, transparent)";
}

/** The range picker: Today, 3 Days, 7 Days. */
export function RangePicker({ value, onChange }: { value: number; onChange: (i: number) => void }) {
  const names = [t("tokencalendar.today"), t("tokencalendar.days_3"), t("tokencalendar.days_7")];
  return (
    <Segmented
      compact
      label={t("tokencalendar.project_range")}
      value={String(value)}
      options={PROJECT_RANGES.map((_, i) => ({ v: String(i), label: names[i] }))}
      onChange={(v) => onChange(Number(v))}
    />
  );
}

function Bar({ share, color }: { share: number; color: string }) {
  return (
    <div className="h-1 rounded-full bg-inset">
      <div className="h-1 rounded-full" style={{ width: `${Math.max(0, Math.min(1, share)) * 100}%`, background: color }} />
    </div>
  );
}

/** A project's name with what tells it apart: its host when it is not GitHub's, a computer count when it ran on
 *  several, a mark when its repository has no remote. */
function ProjectLabel({ p }: { p: UsageProject }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span className="truncate text-ink">{projectName(p)}</span>
      {p.host && p.host !== "github.com" && <span className="shrink-0 text-submeta text-ink-3">{p.host}</span>}
      {p.machines > 1 && (
        <span className="flex shrink-0 items-center gap-0.5 text-submeta text-ink-3 tabular-nums" title={t("tokencalendar.project_computers", { count: p.machines })}>
          <Icon name="machine" size={12} />
          {p.machines}
        </span>
      )}
    </span>
  );
}

/** The list of one range: the development / non-development split, then each group's projects, then what is not
 *  split (usage stored before the hub kept folders). */
function ProjectList({ range, onOpen, phone }: { range: UsageProjectRange; onOpen: (p: UsageProject) => void; phone: boolean }) {
  const groups = useMemo(() => projectGroups(range), [range]);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const all = Math.max(1, projectTotal(range.total));
  const top = Math.max(1, ...range.projects.map((p) => projectTotal(p.tokens)), projectTotal(range.not_split));
  const notSplit = projectTotal(range.not_split);
  if (!range.projects.length && !notSplit) {
    return (
      <div className="flex flex-col items-center gap-2 py-4 text-ink-3">
        <Icon name="folder" size={28} />
        <span className="text-meta text-ink">{t(range.days === 1 ? "tokencalendar.projects_none_today" : "tokencalendar.projects_none")}</span>
      </div>
    );
  }
  const row = (p: UsageProject) => {
    const total = projectTotal(p.tokens);
    const color = groupColor(p.group);
    const label = `${projectName(p)}, ${short(total)}, ${sharePercent(p.share)}`;
    if (phone) {
      return (
        <button key={p.key} type="button" onClick={() => onOpen(p)} aria-label={label} className="block w-full py-1.5 text-left">
          <span className="flex items-center gap-2 text-meta">
            <span className="shrink-0" style={{ color }}><Icon name={KIND_ICON[p.kind]} size={14} /></span>
            <span className="min-w-0 flex-1"><ProjectLabel p={p} /></span>
            <span className="shrink-0 text-label text-ink-2 tabular-nums">{short(total)}</span>
            <span className="shrink-0 text-right text-label text-ink-3 tabular-nums" style={{ minWidth: 34 }}>{sharePercent(p.share)}</span>
            <span className="shrink-0 text-ink-3"><Icon name="chevronRight" size={13} /></span>
          </span>
          <span className="mt-1 block" style={{ marginLeft: 22 }}><Bar share={total / top} color={color} /></span>
        </button>
      );
    }
    return (
      <button key={p.key} type="button" onClick={() => onOpen(p)} aria-label={label} className="grid w-full items-center gap-x-3 py-1.5 text-left" style={{ gridTemplateColumns: COLUMNS }}>
        <span className="flex justify-center" style={{ color }}><Icon name={KIND_ICON[p.kind]} size={15} /></span>
        <span className="min-w-0 text-meta"><ProjectLabel p={p} /></span>
        <Bar share={total / top} color={color} />
        <span className="text-right text-label text-ink-2 tabular-nums">{short(total)}</span>
        <span className="text-right text-label text-ink-3 tabular-nums">{sharePercent(p.share)}</span>
        <span className="flex text-ink-3"><Icon name="chevronRight" size={14} /></span>
      </button>
    );
  };
  const groupHead = (key: "development" | "other", sum: number) => {
    const name = t(key === "development" ? "tokencalendar.group_development" : "tokencalendar.group_other");
    const dot = <span aria-hidden className="inline-block h-2 w-2 shrink-0 rounded-full" style={{ background: groupColor(key) }} />;
    if (phone) {
      return (
        <div key={`h-${key}`} className="flex items-center gap-2 pt-3 pb-1 text-label font-medium text-ink-3">
          {dot}
          <span className="flex-1">{name}</span>
          <span className="tabular-nums">{short(sum)} · {sharePercent(sum / all)}</span>
        </div>
      );
    }
    return (
      <div key={`h-${key}`} className="grid items-center gap-x-3 pt-3 pb-1 text-label font-medium text-ink-3" style={{ gridTemplateColumns: COLUMNS }}>
        <span className="flex justify-center">{dot}</span>
        <span>{name}</span>
        <span />
        <span className="text-right tabular-nums">{short(sum)}</span>
        <span className="text-right tabular-nums">{sharePercent(sum / all)}</span>
        <span />
      </div>
    );
  };
  const group = (key: "development" | "other") => {
    const list = groups[key];
    if (!list.length) return null;
    const shown = open[key] ? list : list.slice(0, FIRST);
    const more = list.length - shown.length;
    return [
      groupHead(key, groups.sums[key]),
      ...shown.map(row),
      more > 0 && (
        <button
          key={`more-${key}`}
          type="button"
          onClick={() => setOpen((o) => ({ ...o, [key]: true }))}
          className="block py-1 text-left text-label text-accent-ink"
          style={{ paddingLeft: phone ? 22 : 28 }}
        >
          {t("tokencalendar.projects_more", { count: more })}
        </button>
      ),
    ];
  };
  const rest = [...groups.unknown];
  const tail = (
    <>
      {rest.map(row)}
      {notSplit > 0 &&
        (phone ? (
          <div key="not-split" className="py-1.5">
            <span className="flex items-center gap-2 text-meta text-ink-3">
              <span className="min-w-0 flex-1 truncate">{t("tokencalendar.not_split")}</span>
              <span className="shrink-0 text-label tabular-nums">{short(notSplit)}</span>
              <span className="shrink-0 text-right text-label tabular-nums" style={{ minWidth: 34 }}>{sharePercent(notSplit / all)}</span>
              <span style={{ width: 13 }} />
            </span>
            <span className="mt-1 block"><Bar share={notSplit / top} color={groupColor("not_split")} /></span>
          </div>
        ) : (
          <div key="not-split" className="grid items-center gap-x-3 py-1.5 text-ink-3" style={{ gridTemplateColumns: COLUMNS }}>
            <span />
            <span className="truncate text-meta">{t("tokencalendar.not_split")}</span>
            <Bar share={notSplit / top} color={groupColor("not_split")} />
            <span className="text-right text-label tabular-nums">{short(notSplit)}</span>
            <span className="text-right text-label tabular-nums">{sharePercent(notSplit / all)}</span>
            <span />
          </div>
        ))}
    </>
  );
  const split = (
    <div aria-hidden className="mb-1 flex h-1.5 overflow-hidden rounded-full bg-inset" style={{ gap: 2 }}>
      {(["development", "other"] as const).map((g) => groups.sums[g] > 0 && <span key={g} style={{ flex: groups.sums[g] / all, background: groupColor(g) }} />)}
      {groups.sums.unknown + notSplit > 0 && <span style={{ flex: (groups.sums.unknown + notSplit) / all, background: groupColor("not_split") }} />}
    </div>
  );
  if (phone) {
    return (
      <div>
        {split}
        {group("development")}
        {group("other")}
        {tail}
      </div>
    );
  }
  return (
    <div>
      {split}
      {group("development")}
      {group("other")}
      {tail}
    </div>
  );
}

/** The page's By project section: the range picker and the list; null from an older hub. */
export function ProjectsSection({ data, range, onRange, onOpen }: { data: UsageProjects; range: number; onRange: (i: number) => void; onOpen: (p: UsageProject) => void }) {
  const phone = useMedia(SHEET_QUERY);
  const r = data.ranges[range] ?? data.ranges[data.ranges.length - 1];
  return (
    <section className="mt-5" data-token-projects>
      <div className="mb-1.5 flex items-center justify-between gap-3 px-1">
        <h2 className="text-label font-medium text-ink-3">{t("tokencalendar.by_project")}</h2>
        {!phone && <RangePicker value={range} onChange={onRange} />}
      </div>
      <div className="rounded-card bg-surface px-4 py-3 shadow-card">
        {phone && (
          <div className="mb-2">
            <RangePicker value={range} onChange={onRange} />
          </div>
        )}
        <ProjectList range={r} onOpen={onOpen} phone={phone} />
      </div>
    </section>
  );
}

// ---- one project ----

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mt-5 min-w-0">
      <h2 className="mb-1.5 px-1 text-label font-medium text-ink-3">{title}</h2>
      <div className="rounded-card bg-surface px-4 py-3 shadow-card">{children}</div>
    </section>
  );
}

/** Rows of a name, a bar against the largest and the number, the three edges lined up. */
function Parts({ rows }: { rows: { key: string; name: React.ReactNode; tokens: ProjectTokens }[] }) {
  const top = Math.max(1, ...rows.map((r) => projectTotal(r.tokens)));
  return (
    <div className="grid items-center gap-x-3 gap-y-2" style={{ gridTemplateColumns: "auto minmax(48px, 1fr) auto" }}>
      {rows.map((r) => (
        <div key={r.key} className="contents">
          <span className="flex min-w-0 items-center gap-2 text-meta text-ink">{r.name}</span>
          <Bar share={projectTotal(r.tokens) / top} color="var(--accent)" />
          <span className="text-right text-label text-ink-3 tabular-nums">{short(projectTotal(r.tokens))}</span>
        </div>
      ))}
    </div>
  );
}

const utc = (date: string) => new Date(`${date}T00:00:00Z`);
const hourName = (h: number) => formatDate(Date.UTC(2024, 0, 1, h), { hour: "numeric", timeZone: "UTC" });

/** By day (by the hour for today): columns against the largest, and a line over them with one's day (or hour) and
 *  tokens: the largest's at first, then the one hovered or tapped (kept until another is). */
function Columns({ detail, days }: { detail: UsageProjectDetail; days: number }) {
  const cols =
    days === 1
      ? detail.hours.map((h) => ({ key: String(h.t), day: t("tokencalendar.today"), hour: hourName(h.hour), axis: h.hour % 6 === 0 ? hourName(h.hour) : "", value: projectTotal(h.tokens) }))
      : detail.days.slice(-days).map((d, i, all) => {
          const today = i === all.length - 1;
          const day = today ? t("tokencalendar.today") : formatDate(utc(d.date), { month: "short", day: "numeric", weekday: "short", timeZone: "UTC" });
          const axis = today ? t("tokencalendar.today") : formatDate(utc(d.date), { month: "numeric", day: "numeric", timeZone: "UTC" });
          return { key: d.date, day, hour: "", axis, value: projectTotal(d.tokens) };
        });
  const [picked, setPicked] = useState<string | null>(null);
  const [hovered, setHovered] = useState<string | null>(null);
  const top = Math.max(1, ...cols.map((c) => c.value));
  const largest = cols.reduce<(typeof cols)[number] | undefined>((a, c) => (!a || c.value > a.value ? c : a), undefined);
  const shown = cols.find((c) => c.key === hovered) ?? cols.find((c) => c.key === picked) ?? largest;
  const line = (c: (typeof cols)[number]) =>
    c.hour ? t("tokencalendar.hour_line", { day: c.day, hour: c.hour, tokens: short(c.value) }) : t("tokencalendar.day_line", { day: c.day, tokens: short(c.value) });
  return (
    <div>
      <div className="mb-2 text-meta text-ink-2 tabular-nums" aria-live="polite" style={{ minHeight: "1.25em" }} data-token-project-line>
        {shown && shown.value > 0 && line(shown)}
      </div>
      <div className="flex items-end" style={{ height: 110, gap: days === 1 ? 3 : 10 }} onMouseLeave={() => setHovered(null)}>
        {cols.map((c) => (
          <button
            key={c.key}
            type="button"
            aria-label={line(c)}
            aria-pressed={shown?.key === c.key}
            onClick={() => setPicked(c.key)}
            onMouseEnter={() => setHovered(c.key)}
            onFocus={() => setHovered(c.key)}
            onBlur={() => setHovered(null)}
            className="flex h-full min-w-0 flex-1 flex-col justify-end"
          >
            <span
              className="block w-full"
              style={{
                height: `${Math.max(c.value > 0 ? 2 : 0, (100 * c.value) / top)}%`,
                borderRadius: "4px 4px 2px 2px",
                background: shown?.key === c.key ? "var(--accent)" : "color-mix(in srgb, var(--accent) 55%, transparent)",
              }}
            />
          </button>
        ))}
      </div>
      <div aria-hidden className="mt-1 flex text-submeta text-ink-3" style={{ gap: days === 1 ? 3 : 10 }}>
        {cols.map((c) => (
          <span key={c.key} className="min-w-0 flex-1 truncate text-center" style={{ overflow: "visible" }}>
            {c.axis}
          </span>
        ))}
      </div>
    </div>
  );
}

function Stat({ label, value, good }: { label: string; value: string; good?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="truncate text-label text-ink-3">{label}</div>
      <div className={good ? "text-card-title font-semibold text-green tabular-nums" : "text-card-title font-semibold text-ink tabular-nums"}>{value}</div>
    </div>
  );
}

/** One project's breakdown: what it is, its totals for the range, by day, by computer, Agent, model and launcher,
 *  and its worktrees and folders. */
/** The launchers' display names (Settings › Models, GET /launchers), read once a page: a launcher without one, or
 *  before the list comes (or from a hub that will not give it), shows its name. */
function useLauncherNames(): Record<string, string> {
  const [names, setNames] = useState<Record<string, string>>({});
  useEffect(() => {
    let live = true;
    loadLaunchers<{ name: string; display_name?: string }>().then(
      (r) => live && setNames(Object.fromEntries(r.launchers.filter((l) => l.display_name?.trim()).map((l) => [l.name, l.display_name!.trim()]))),
      () => {},
    );
    return () => {
      live = false;
    };
  }, []);
  return names;
}

export function ProjectDetail({ project, range, onRange }: { project: UsageProject; range: number; onRange: (i: number) => void }) {
  const [detail, setDetail] = useState<UsageProjectDetail | null>(null);
  const [failed, setFailed] = useState(false);
  const phone = useMedia(SHEET_QUERY);
  const launcherNames = useLauncherNames();
  useEffect(() => {
    let live = true;
    const read = () =>
      fetchUsageProjectDetail(project.key).then(
        (d) => {
          if (!live) return;
          setDetail(d);
          setFailed(false);
        },
        () => live && setFailed(true),
      );
    read();
    const onVisible = () => document.visibilityState === "visible" && read();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      live = false;
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [project.key]);
  if (!detail) return failed ? <p className="py-10 text-center text-meta text-ink-3">{t("tokencalendar.failed")}</p> : <Loading />;
  const b = detail.ranges[range] ?? detail.ranges[detail.ranges.length - 1];
  const p = detail.project;
  const all = Math.max(1, projectTotal(b.total));
  const value = (k: UsageKind) => (b.total as Record<string, number | undefined>)[k] ?? 0;
  const breakdown = [
    b.machines.length > 0 && (
      <Card key="m" title={t("tokencalendar.by_computer_plain")}>
        <Parts rows={b.machines.map((m) => ({ key: m.key, name: <span className="truncate">{m.name || t("tokencalendar.removed_computer")}</span>, tokens: m.tokens }))} />
      </Card>
    ),
    b.agents.length > 0 && (
      <Card key="a" title={t("tokencalendar.by_agent_plain")}>
        <Parts
          rows={b.agents.map((a: UsagePart) => ({
            key: a.key || "-",
            name: a.key ? (
              <>
                <AgentMark agent={a.key} size={14} decorative />
                <span className="truncate">{agentName(a.key)}</span>
              </>
            ) : (
              <span className="truncate text-ink-3">{t("tokencalendar.not_split")}</span>
            ),
            tokens: a.tokens,
          }))}
        />
      </Card>
    ),
    b.models.length > 0 && (
      <Card key="mo" title={t("tokencalendar.by_model")}>
        <Parts rows={b.models.map((m) => ({ key: m.key, name: <span className="truncate" title={m.key}>{m.key ? modelLabel(m.key) : t("tokencalendar.project_unknown_model")}</span>, tokens: m.tokens }))} />
      </Card>
    ),
    b.launchers.length > 0 && (
      <Card key="l" title={t("tokencalendar.by_launcher")}>
        <Parts
          rows={b.launchers.map((l) => ({
            key: l.key || "-",
            name: (
              <span className="truncate" title={l.key && launcherNames[l.key] ? l.key : undefined}>
                {l.key ? launcherNames[l.key] ?? l.key : t("tokencalendar.launcher_terminal")}
              </span>
            ),
            tokens: l.tokens,
          }))}
        />
      </Card>
    ),
  ].filter(Boolean);
  const placeTop = Math.max(1, ...b.places.map((pl) => projectTotal(pl.tokens)));
  return (
    <div data-token-project={p.key}>
      <div className="rounded-card bg-surface px-4 py-3 shadow-card">
        <div className="flex items-center gap-2">
          <span className="shrink-0" style={{ color: groupColor(p.group) }}>
            <Icon name={KIND_ICON[p.kind]} size={17} />
          </span>
          {p.host && <span className="text-label text-ink-3">{p.host}</span>}
          {p.machines > 1 && (
            <span className="flex items-center gap-0.5 text-label text-ink-3 tabular-nums">
              <Icon name="machine" size={12} />
              {t("tokencalendar.project_computers", { count: p.machines })}
            </span>
          )}
          <span className="flex-1" />
          {!phone && <RangePicker value={range} onChange={onRange} />}
        </div>
        {phone && (
          <div className="mt-2">
            <RangePicker value={range} onChange={onRange} />
          </div>
        )}
        <div className="mt-2 flex flex-wrap items-end gap-y-2" style={{ columnGap: 28 }}>
          <div className="text-2xl font-semibold text-ink tabular-nums">
            {short(projectTotal(b.total))}
          </div>
          <Stat label={t("tokencalendar.project_share")} value={sharePercent(b.share)} />
          <Stat label={t("tokencalendar.project_requests")} value={formatNumber(b.total.req ?? 0)} />
          <Stat label={t("tokencalendar.project_cache_hit")} value={formatNumber(b.cache_hit, { style: "percent", maximumFractionDigits: 0 })} good={b.cache_hit >= 0.8} />
        </div>
        <div className="mt-3 flex h-1.5 overflow-hidden rounded-full bg-inset">
          {KINDS.map((k) => (
            <span key={k} style={{ width: `${(100 * value(k)) / all}%`, background: USAGE_COLOR[k] }} />
          ))}
        </div>
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-label text-ink-2">
          {KINDS.map((k) => (
            <span key={k} className="flex items-center gap-1.5">
              <span aria-hidden className="inline-block h-2 w-2 shrink-0 rounded-full" style={{ background: USAGE_COLOR[k] }} />
              {t(`machines-usage.kind_${k}`)} {formatNumber(value(k) / all, { style: "percent", maximumSignificantDigits: 2 })}
            </span>
          ))}
        </div>
      </div>
      <Card title={t(range === 0 ? "tokencalendar.by_hour_plain" : "tokencalendar.each_day")}>
        <Columns detail={detail} days={PROJECT_RANGES[range] ?? 7} />
      </Card>
      {b.places.length > 0 && phone && (
        <Card title={t("tokencalendar.project_places")}>
          {b.places.map((pl) => (
            <div key={`${pl.machine}\u0000${pl.place}`} className="py-1.5">
              <span className="flex items-center gap-2 text-meta">
                <span className="flex shrink-0" style={{ color: "var(--accent)" }} title={pl.worktree ? t("tokencalendar.project_worktree") : undefined}>
                  <Icon name={pl.worktree ? "branch" : "folder"} size={14} />
                </span>
                <span className="min-w-0 flex-1 break-all text-ink">{pl.place}</span>
                <span className="shrink-0 text-label text-ink-3 tabular-nums">{short(projectTotal(pl.tokens))}</span>
              </span>
              <span className="block text-submeta text-ink-3" style={{ marginLeft: 22 }}>{pl.machine_name || t("tokencalendar.removed_computer")}</span>
              <span className="mt-1 block" style={{ marginLeft: 22 }}><Bar share={projectTotal(pl.tokens) / placeTop} color="var(--accent)" /></span>
            </div>
          ))}
        </Card>
      )}
      {b.places.length > 0 && !phone && (
        <Card title={t("tokencalendar.project_places")}>
          <div className="grid items-center gap-x-3 gap-y-2" style={{ gridTemplateColumns: "auto minmax(0, 1.6fr) minmax(48px, 1fr) auto" }}>
            {b.places.map((pl) => (
              <div key={`${pl.machine}\u0000${pl.place}`} className="contents">
                <span className="flex" style={{ color: "var(--accent)" }} title={pl.worktree ? t("tokencalendar.project_worktree") : undefined}>
                  <Icon name={pl.worktree ? "branch" : "folder"} size={14} />
                </span>
                <span className="flex min-w-0 items-center gap-1.5 text-meta">
                  <span className="truncate text-ink">{pl.place}</span>
                  <span className="shrink-0 text-submeta text-ink-3">{pl.machine_name || t("tokencalendar.removed_computer")}</span>
                </span>
                <Bar share={projectTotal(pl.tokens) / placeTop} color="var(--accent)" />
                <span className="text-right text-label text-ink-3 tabular-nums">{short(projectTotal(pl.tokens))}</span>
              </div>
            ))}
          </div>
        </Card>
      )}
      <div className={phone ? "" : "grid grid-cols-2 gap-x-3"}>{breakdown}</div>
    </div>
  );
}

/** The dialog's title while a project is open: back to the list, and the project's name. */
export function ProjectTitle({ project, onBack }: { project: UsageProject; onBack: () => void }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <button type="button" onClick={onBack} aria-label={t("tokencalendar.project_back")} title={t("tokencalendar.project_back")} className="flex shrink-0 text-ink-3">
        <Icon name="chevronLeft" size={16} />
      </button>
      <span className="truncate">{projectName(project)}</span>
    </span>
  );
}
