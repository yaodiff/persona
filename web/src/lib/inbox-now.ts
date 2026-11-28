// The manager's inbox state key in the app's words (docs/assistant/inbox.md "Following up"). The key, its
// translations and the shared examples live in spec/fixtures/inbox/now.json; an older hub's missing key, or a newer
// hub's unknown one, leaves each existing state line on its own words.
import { formatDate, t } from "../i18n";
import { fromLine, sourceMailbox, type InboxItem } from "./inbox";
import { hm } from "./routines";
import { stateName } from "./state-labels";

// State keys only: the catalogs also hold composition fragments (next, look_again…) and calendar words.
const NOW_KEYS = new Set([
  "confirm_reply", "confirm_agent", "confirm_handoff", "decide", "queued", "looking", "agent_working", "waiting", "follow_up_ask", "check_due", "failed",
  "needs_you", "fyi", "folded", "done_replied", "done_manager", "done_user", "done_read", "done_dismissed", "done_muted", "done_superseded",
]);

/** Where the item came from, then who said what. A direct chat names the sender once. */
export function rowMeta(it: InboxItem, translatedPreview?: string): string {
  const place = it.where || sourceMailbox(it);
  if (!place) return fromLine(it, translatedPreview);
  const preview = translatedPreview || it.preview;
  const latest = it.sender && preview ? t("lib-inbox.sender_line", { sender: it.sender, text: preview }) : it.sender || preview;
  return it.sender === place ? latest : [place, latest].filter(Boolean).join(" · ");
}

/** A follow-up moment in the local zone: "09:00" today, "tomorrow 09:00", then a date and the 24-hour clock. */
function followTime(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1970) return "";
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((day(d) - day(now)) / 86_400_000);
  if (days === 0) return hm(d);
  if (days === 1) return `${t("lib-inbox-now.tomorrow")} ${hm(d)}`;
  const opts: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" };
  if (d.getFullYear() !== now.getFullYear()) opts.year = "numeric";
  return `${formatDate(d, opts)} ${hm(d)}`;
}

/** Whitespace alone is an absent optional part; keep the manager's original words otherwise. */
function present(value?: string): string | undefined {
  return value?.trim() ? value : undefined;
}

/** The item's current state in words, or undefined when this app does not know the manager's key. */
export function nowText(item: InboxItem): string | undefined {
  const key = item.now;
  if (!key || !NOW_KEYS.has(key)) return undefined;
  if (key === "needs_you") return stateName("needs_you");
  const follow = item.follow;
  const who = present(follow?.who);
  const next = present(follow?.next);
  if (key === "waiting") {
    const waiting = who ? t("lib-inbox-now.waiting", { who }) : t("lib-inbox-now.waiting_anyone");
    const time = follow?.check_at ? followTime(follow.check_at) : "";
    const again = time ? t("lib-inbox-now.look_again", { time }) : "";
    return `${waiting}${again}${next ? t("lib-inbox-now.next", { next }) : ""}`;
  }
  if (key === "check_due") {
    return `${t("lib-inbox-now.check_due")}${next ? t("lib-inbox-now.next", { next }) : ""}`;
  }
  if (key === "failed") {
    const reason = present(item.handling?.error);
    return reason ? t("lib-inbox-now.failed", { reason }) : t("lib-inbox-now.failed_plain");
  }
  return t(`lib-inbox-now.${key}`);
}
