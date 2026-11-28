// The token calendar on the assistant's new-chat page (#9549, the iPhone's #9506; docs/web/web.md): every
// computer's tokens a day (lib/usage-history.ts), one square a day, a column a week, this week last, today outlined,
// the theme color in four steps by rank. Its line says today's and the last 30 days' tokens; a clicked day (or the
// arrow keys on the grid) shows that day's tokens and four kinds. Read when the page opens and when the tab comes back
// to the front, never on a timer. Not on the first screen: a lazy import, its strings the namespace `tokencalendar`.
// The line opens Token usage (#10042, TokenUsagePage.tsx) in a dialog (a sheet on a phone), in this chunk (a chunk of
// its own would name itself on the first screen); there the calendar is drawn again with only a picked day's line
// (`page`). A project picked under By project (#16530, TokenProjects.tsx) shows in the same dialog, its title going back.
import { useCallback, useEffect, useMemo, useState } from "react";
import { formatDate, formatNumber, t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { calendarLayout, dayTotal, fetchUsageDays, lastDays, weeksThatFit, type UsageDay, type UsageDays } from "../../lib/usage-history";
import type { UsageProject } from "../../lib/usage-projects";
import { Dialog } from "../../ui/atoms";
import { ProjectTitle } from "./TokenProjects";
import TokenUsage from "./TokenUsagePage";

// The squares' size and spacing (the iPhone's Theme.Heat, a little smaller for a page's 13 px text), and the theme
// color's strength at each step above none: inline, so no class is added to the first screen's styles.
const CELL = 12;
const GAP = 3;
const RADIUS = 2.5;
const STEPS = [28, 50, 75, 100];
/** What it reads: as many days as the new-chat page's 640 px column can show. */
const READ_DAYS = 40 * 7;

function fill(level: number): string {
  if (level <= 0) return "var(--field)";
  return level >= STEPS.length ? "var(--accent)" : `color-mix(in srgb, var(--accent) ${STEPS[level - 1]}%, transparent)`;
}

const short = (n: number) => formatNumber(n, { notation: "compact", maximumSignificantDigits: 3 });
const utc = (date: string) => new Date(`${date}T00:00:00Z`);
const dayName = (date: string) => formatDate(utc(date), { month: "short", day: "numeric", weekday: "short", timeZone: "UTC" });

/** The days, read on mount and when the tab comes back to the front. */
export function useUsageDays(enabled = true): UsageDays | null {
  const [data, setData] = useState<UsageDays | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    const read = () => fetchUsageDays(READ_DAYS).then((u) => live && setData(u), () => {}); // an older hub: no card
    read();
    const onVisible = () => document.visibilityState === "visible" && read();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      live = false;
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [enabled]);
  return data;
}

export default function TokenCalendar({ days, page }: { days?: UsageDays | null; page?: boolean } = {}) {
  const own = useUsageDays(days === undefined); // the page reads them for its totals and hands them in
  const data = days !== undefined ? days : own;
  const [width, setWidth] = useState(0);
  const [picked, setPicked] = useState<string | null>(null);
  const [opened, setOpened] = useState(false);
  // The project Token usage shows (By project, #16530): the dialog's title goes back from it.
  const [project, setProject] = useState<UsageProject | null>(null);
  // The squares' room, measured where they are drawn: a narrower window shows fewer weeks.
  const measure = useCallback((el: HTMLDivElement | null) => {
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const weeks = weeksThatFit(width, CELL, GAP);
  const layout = useMemo(() => calendarLayout(data?.days ?? [], weeks), [data, weeks]);
  const byDate = useMemo(() => new Map((data?.days ?? []).map((d) => [d.date, d])), [data]);
  const shown = useMemo(() => layout.columns.flat().flatMap((c) => (c.date ? [c.date] : [])), [layout]);
  const weekdays = useMemo(
    () => [0, 2, 4].map((r) => ({ r, name: formatDate(Date.UTC(2024, 0, 1 + r), { weekday: "narrow", timeZone: "UTC" }) })),
    [],
  );
  if (!data || !data.days.some((d) => dayTotal(d) > 0)) return null; // an older hub, or no usage yet
  const day: UsageDay | undefined = picked ? byDate.get(picked) : undefined;
  const step = CELL + GAP;
  const move = (by: number) => {
    const at = picked ? shown.indexOf(picked) : shown.length - 1;
    setPicked(shown[Math.min(shown.length - 1, Math.max(0, at + by))] ?? null);
  };

  return (
    <div className={page ? "text-left" : "mt-4 rounded-[10px] px-3 py-3 text-left shadow-hairline"} data-token-calendar>
      <div className="text-meta text-ink-2" aria-live="polite">
        {day && picked ? (
          <>
            <div>{tRich("tokencalendar.day_line", { day: <b className="font-semibold text-ink">{dayName(picked)}</b>, tokens: <b className="font-semibold text-ink">{short(dayTotal(day))}</b> })}</div>
            <div className="mt-0.5 text-submeta">
              {t("tokencalendar.kinds", { input: short(day.in ?? 0), output: short(day.out ?? 0), write: short(day.cache_write ?? 0), read: short(day.cache_read ?? 0) })}
            </div>
          </>
        ) : page ? null : (
          <button type="button" onClick={() => setOpened(true)} title={t("tokencalendar.open_page")} aria-haspopup="dialog" className="text-left hover:underline" data-token-calendar-line>
            {tRich("tokencalendar.line", {
              today: <b className="font-semibold text-ink">{short(lastDays(data.days, 1))}</b>,
              month: <b className="font-semibold text-ink">{short(lastDays(data.days, 30))}</b>,
            })}
          </button>
        )}
      </div>
      <div className="mt-2 flex gap-1 text-submeta text-ink-3">
        <div aria-hidden style={{ display: "grid", gridTemplateRows: `${CELL + 13}px repeat(7, ${CELL}px)`, rowGap: GAP }}>
          <span />
          {Array.from({ length: 7 }, (_, r) => (
            <span key={r} style={{ lineHeight: `${CELL}px` }}>{weekdays.find((w) => w.r === r)?.name ?? ""}</span>
          ))}
        </div>
        <div ref={measure} className="min-w-0 flex-1">
          <div aria-hidden style={{ position: "relative", height: CELL + 13 }}>
            {layout.months.map((m) => (
              <span key={m.column} style={{ position: "absolute", left: m.column * step, whiteSpace: "nowrap" }}>
                {formatDate(utc(m.first), { month: "short", timeZone: "UTC" })}
              </span>
            ))}
          </div>
          <div
            role="img"
            tabIndex={0}
            aria-label={picked && day ? t("tokencalendar.read_day", { day: dayName(picked), tokens: short(dayTotal(day)) }) : t("tokencalendar.grid")}
            onKeyDown={(e) => {
              const by = { ArrowUp: -1, ArrowDown: 1, ArrowLeft: -7, ArrowRight: 7 }[e.key];
              if (by) {
                e.preventDefault();
                move(by);
              } else if (e.key === "Escape") setPicked(null);
            }}
            style={{ display: "grid", gridAutoFlow: "column", gridTemplateRows: `repeat(7, ${CELL}px)`, gridAutoColumns: `${CELL}px`, gap: GAP, outline: "none" }}
          >
            {layout.columns.map((column, c) =>
              column.map((cell, r) =>
                cell.date === null ? (
                  <span key={`${c}-${r}`} />
                ) : (
                  <span
                    key={cell.date}
                    title={`${dayName(cell.date)} · ${short(dayTotal(byDate.get(cell.date) ?? { date: cell.date }))}`}
                    onClick={() => setPicked(picked === cell.date ? null : cell.date)}
                    style={{
                      background: fill(cell.level),
                      borderRadius: RADIUS,
                      cursor: "pointer",
                      boxShadow: cell.date === picked
                        ? "0 0 0 2px var(--ink)"
                        : layout.today && layout.today[0] === c && layout.today[1] === r ? "inset 0 0 0 1.5px var(--ink)" : undefined,
                    }}
                  />
                ),
              ),
            )}
          </div>
        </div>
      </div>
      {!page && (
        <Dialog
          open={opened}
          onClose={() => {
            setOpened(false);
            setProject(null);
          }}
          title={project ? <ProjectTitle project={project} onBack={() => setProject(null)} /> : t("tokencalendar.title")}
          width={720}
        >
          {opened && <TokenUsage project={project} onProject={setProject} />}
        </Dialog>
      )}
    </div>
  );
}
