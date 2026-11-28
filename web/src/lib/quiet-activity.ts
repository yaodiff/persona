import type { FeedItem } from "./feed";
import type { ChatLine } from "./turns";
import { timeOrNaN as time } from "./times";

export type QuietRound = Extract<ChatLine, { kind: "supervision" }> & { completedSilent: true };
export interface BackgroundActivityRound {
  line: QuietRound;
  feed: FeedItem[];
}

const ROUND_FEED: ReadonlySet<FeedItem["kind"]> = new Set(["task", "routed", "answered"]);

/** Feed written during a completed silent round: [its first input, the next real input). Other cards never move. */
function inRound(item: FeedItem, line: QuietRound): boolean {
  if (!ROUND_FEED.has(item.kind)) return false;
  const at = time(item.at), start = time(line.start), until = time(line.until);
  if (!Number.isFinite(at) || !Number.isFinite(start) || at < start) return false;
  return !Number.isFinite(until) || at < until;
}

/** Split before disclosure or day dividers: hidden rounds and their cards never create an empty main-timeline row/day. */
export function splitBackgroundActivity(lines: ChatLine[], feed: FeedItem[], from?: number): {
  timeline: ChatLine[];
  timelineFeed: FeedItem[];
  activity: BackgroundActivityRound[];
} {
  const activity: BackgroundActivityRound[] = [];
  const hidden = new Set<string>();
  for (const line of lines) {
    if (line.kind !== "supervision" || line.completedSilent !== true) continue;
    const quiet = line as QuietRound;
    const cards = feed.filter((item) => (from === undefined || time(item.at) >= from) && inRound(item, quiet));
    for (const item of cards) hidden.add(item.id);
    activity.push({ line: quiet, feed: cards });
  }
  return {
    timeline: lines.filter((line) => line.kind !== "supervision" || !line.completedSilent),
    timelineFeed: hidden.size ? feed.filter((item) => !hidden.has(item.id)) : feed,
    activity,
  };
}
