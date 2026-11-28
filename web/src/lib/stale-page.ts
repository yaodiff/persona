// The safety net under a chat's live stream (#3139): the page follows a session by WebSocket pushes, and when one is lost
// (a socket that looks open but is not, a queue the hub had to drop) it used to stay behind until a reload. The session
// list keeps coming over the same socket and says the transcript grew: its `rev` moved since the page's newest read was
// sent, or its `updated_at` is newer than the newest item the page holds. Then, once nothing has come for a while, the
// page asks for what it is missing once (`items?after_seq=`), applies it silently, and only says "Not live" when that
// keeps being needed. iOS / Mac follow the same rule and numbers (`AgentSyncPacing.catchUpAt`).
//
// The rev catches the last line of a burst whose push was lost (#1179): its `updated_at` is within `STALE_SLACK_MS` of
// the line before, which the page holds. A push never settles a moved rev, as nothing says which lines the rev was
// for: the list comes from the node's poll and the push from its transcript watch, so a list can come before the push
// for an earlier line, and the lost line behind it. Only a read sent after the move does. The node reports revs on its
// next tick, mostly after the items they belong to, so this costs one empty read each time an open chat goes quiet.

/** No items for this long before a page that looks behind asks. */
export const STALE_QUIET_MS = 4000;
/** At most one ask this often per session. */
export const STALE_PULL_EVERY_MS = 10000;
/** The list's `updated_at` is the file's mtime and an item's `ts` the line's own clock: closer than this is the same write. */
export const STALE_SLACK_MS = 1000;
/** Asks in a row that found what the stream should have brought (or failed) before the page says it is not live. */
export const STALE_HINT_AFTER = 2;
/** Two isolated recoveries farther apart than this are treated as separate blips rather than a broken stream. */
export const STALE_RECOVERY_WINDOW_MS = 120_000;

export interface StaleView {
  /** The session's `updated_at` as the list has it now. */
  updatedAt?: string;
  /** The newest item the page holds, in ms (0: none, or none with a time). */
  lastItemAt: number;
  /** The session's rev moved since the page's newest read that came back was sent (`RevWatch.unread`). */
  revUnread?: boolean;
  /** When the rev last moved, in ms (0: not since the page opened): the lines it was for may still be on their way. */
  revMovedAt?: number;
  /** When items last came (a push, a page, an ask), in ms. */
  lastArrival: number;
  /** When the tab most recently became visible, in ms (0: visible since open): give a waking socket time to push. */
  visibleSince?: number;
  /** When this session was last asked for, in ms (0: never). */
  lastPull: number;
  /** The `updated_at` the last ask was for: a write that added no items (a title, a mode) is not asked about twice. */
  checkedFor?: string;
  now: number;
  visible: boolean;
  /** An ask is running. */
  pulling: boolean;
}

/** Whether to ask for what the page may be missing now. */
export function shouldPull(v: StaleView): boolean {
  if (!v.visible || v.pulling || (!v.revUnread && !newerThanItems(v))) return false;
  const quietSince = Math.max(v.lastArrival, v.revMovedAt ?? 0, v.visibleSince ?? 0);
  return v.now - quietSince >= STALE_QUIET_MS && v.now - v.lastPull >= STALE_PULL_EVERY_MS;
}

/** The list's `updated_at` is past the page's newest item, and no ask was for it yet. */
function newerThanItems(v: StaleView): boolean {
  if (!v.updatedAt || v.updatedAt === v.checkedFor) return false;
  const updated = Date.parse(v.updatedAt);
  return Number.isFinite(updated) && updated > v.lastItemAt + STALE_SLACK_MS;
}

/** A session's `rev` (its transcript's size) as an open page hears it, from the list and from the hub's `revs` event,
 *  and whether a read was sent since it last moved. */
export class RevWatch {
  private rev?: number;
  private moves = 0;
  /** `moves` when the newest read that came back was sent. */
  private read = 0;
  /** When the rev last moved, in ms (0: not since the page opened). */
  movedAt = 0;

  /** `rev`: what the page has when its first read is sent, which that read covers. Without one, the first rev heard is
   *  a move: the page cannot tell whether its first read covered it. */
  constructor(rev: number | undefined) {
    this.rev = rev;
  }

  heard(rev: number | undefined, now: number): void {
    if (rev === undefined || rev === this.rev) return;
    this.rev = rev;
    this.moves++;
    this.movedAt = now;
  }

  /** A read is being sent; give what this returns to `answered` once it came back. */
  sending(): number {
    return this.moves;
  }

  /** A read came back: it holds every line written before it was sent, which the moves heard by then were for. */
  answered(sent: number): void {
    this.read = Math.max(this.read, sent);
  }

  /** The rev moved since the newest read that came back was sent. */
  get unread(): boolean {
    return this.moves > this.read;
  }
}

/** What the asks so far say about the stream. */
export interface StaleTally {
  /** Asks in a row that brought items the push should have. */
  recovered: number;
  /** Asks in a row that failed. */
  failed: number;
  /** When the previous recovery landed, in ms, so isolated blips minutes apart do not accumulate. */
  lastRecoveredAt?: number;
}

export const FRESH_TALLY: StaleTally = { recovered: 0, failed: 0 };

export type PullOutcome = "found" | "empty" | "failed";

export function afterPull(t: StaleTally, outcome: PullOutcome, now?: number): StaleTally {
  if (outcome === "failed") {
    return t.lastRecoveredAt !== undefined
      ? { recovered: t.recovered, failed: t.failed + 1, lastRecoveredAt: t.lastRecoveredAt }
      : { recovered: t.recovered, failed: t.failed + 1 };
  }
  if (outcome === "found") {
    const expired = now !== undefined && t.recovered > 0 && t.lastRecoveredAt !== undefined && now - t.lastRecoveredAt > STALE_RECOVERY_WINDOW_MS;
    const recovered = expired ? 1 : t.recovered + 1;
    return now !== undefined ? { recovered, failed: 0, lastRecoveredAt: now } : { recovered, failed: 0 };
  }
  return t.lastRecoveredAt !== undefined
    ? { recovered: t.recovered, failed: 0, lastRecoveredAt: t.lastRecoveredAt }
    : { recovered: t.recovered, failed: 0 };
}

/** The stream brought items itself: it works. */
export function afterPush(t: StaleTally): StaleTally {
  return t.recovered || t.failed ? FRESH_TALLY : t;
}

/** Whether the page should say it is not live. */
export function notLive(t: StaleTally): boolean {
  return t.recovered >= STALE_HINT_AFTER || t.failed >= STALE_HINT_AFTER;
}

/** The time of the newest item with one, in ms (0 when none has). */
export function newestItemAt(items: readonly { ts?: string }[]): number {
  for (let i = items.length - 1; i >= 0; i--) {
    const at = Date.parse(items[i].ts ?? "");
    if (Number.isFinite(at)) return at;
  }
  return 0;
}
