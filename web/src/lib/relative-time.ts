// When a reply was written, in the words of the bar under it (#5314), ported with changes from cindy's useRelativeTime.ts:
// "just now" under a minute, then whole minutes, hours and days (rounded down), and from a week on the date and time
// as the chat writes them elsewhere (clock() in lib/format.ts), with the year only when it is not this one. The exact
// time, to the second, is its tooltip.
import { formatDate, t } from "../i18n";
import { timeOrNaN } from "./times";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const WEEK = 7 * DAY;

const pad = (n: number) => String(n).padStart(2, "0");

/** "5 minutes ago", "3 days ago", "Nov 5, 14:30", "Nov 5, 2025, 14:30" (in the page's language); "" for a missing or
 *  unreadable time. A time ahead of `now` (another computer's clock) is "just now". Keep whole days when the exact
 *  date is displayed alongside the relative time. */
export function relativeTime(iso: string | undefined, now = Date.now(), keepRelative = false): string {
  const at = timeOrNaN(iso);
  if (!Number.isFinite(at)) return "";
  const delta = now - at;
  if (delta < MINUTE) return t("lib-relative-time.just_now");
  if (delta < HOUR) return t("lib-relative-time.minutes_ago", { count: Math.floor(delta / MINUTE) });
  if (delta < DAY) return t("lib-relative-time.hours_ago", { count: Math.floor(delta / HOUR) });
  if (keepRelative || delta < WEEK) return t("lib-relative-time.days_ago", { count: Math.floor(delta / DAY) });
  const d = new Date(at);
  const year = d.getFullYear() === new Date(now).getFullYear() ? undefined : "numeric";
  const date = formatDate(d, { year, month: "short", day: "numeric" });
  return t("lib-format.date_time", { date, time: `${pad(d.getHours())}:${pad(d.getMinutes())}` });
}

/** "2026-11-12 14:30:05", local time; "" for a missing or unreadable time. */
export function exactTime(iso: string | undefined): string {
  const at = timeOrNaN(iso);
  if (!Number.isFinite(at)) return "";
  const d = new Date(at);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
