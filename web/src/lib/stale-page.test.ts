import { describe, expect, it } from "vitest";
import {
  afterPull,
  afterPush,
  FRESH_TALLY,
  newestItemAt,
  notLive,
  RevWatch,
  shouldPull,
  STALE_HINT_AFTER,
  STALE_PULL_EVERY_MS,
  STALE_RECOVERY_WINDOW_MS,
  STALE_QUIET_MS,
  type StaleView,
} from "./stale-page";

const T0 = Date.parse("2026-11-05T10:00:00Z");
const behind = (over: Partial<StaleView> = {}): StaleView => ({
  updatedAt: new Date(T0 + 60_000).toISOString(),
  lastItemAt: T0 + 20_000,
  lastArrival: T0,
  lastPull: 0,
  now: T0 + STALE_QUIET_MS + 1,
  visible: true,
  pulling: false,
  ...over,
});

describe("shouldPull", () => {
  it("asks when the list is newer than the newest item and nothing has come for a while", () => {
    expect(shouldPull(behind())).toBe(true);
  });

  it("does not while the page is caught up, or the write is the item's own", () => {
    expect(shouldPull(behind({ lastItemAt: T0 + 60_000 }))).toBe(false);
    expect(shouldPull(behind({ lastItemAt: T0 + 59_500 }))).toBe(false); // mtime a moment after the line's clock
    expect(shouldPull(behind({ updatedAt: undefined }))).toBe(false);
    expect(shouldPull(behind({ updatedAt: "not a time" }))).toBe(false);
  });

  it("waits for the stream: items came a moment ago", () => {
    expect(shouldPull(behind({ now: T0 + STALE_QUIET_MS - 1 }))).toBe(false);
    expect(shouldPull(behind({ lastArrival: T0 + 3000, now: T0 + 3000 + STALE_QUIET_MS - 1 }))).toBe(false);
  });

  it("asks at most once per interval, and not while one runs", () => {
    const now = T0 + 60_000;
    expect(shouldPull(behind({ now, lastPull: now - STALE_PULL_EVERY_MS + 1 }))).toBe(false);
    expect(shouldPull(behind({ now, lastPull: now - STALE_PULL_EVERY_MS }))).toBe(true);
    expect(shouldPull(behind({ pulling: true }))).toBe(false);
  });

  it("only while the page is visible", () => {
    expect(shouldPull(behind({ visible: false }))).toBe(false);
  });

  it("does not ask twice about a write that added no items", () => {
    const v = behind();
    expect(shouldPull({ ...v, checkedFor: v.updatedAt })).toBe(false);
    expect(shouldPull({ ...v, checkedFor: new Date(T0 + 30_000).toISOString() })).toBe(true); // it moved on since
  });

  it("a page with no timed item asks once per write", () => {
    expect(shouldPull(behind({ lastItemAt: 0 }))).toBe(true);
  });
});

describe("shouldPull by rev (#1179)", () => {
  // The last line of a burst, written 0.3 s after the line before, whose push was lost: the list's updated_at is within
  // the slack of the newest line the page holds, but the session's rev moved since the page last read.
  const burst = (over: Partial<StaleView> = {}): StaleView =>
    behind({ updatedAt: new Date(T0 + 20_300).toISOString(), revUnread: true, revMovedAt: T0, ...over });

  it("asks when the rev moved since the page last read, though updated_at is within the slack", () => {
    expect(shouldPull(burst({ revUnread: false }))).toBe(false); // by time alone it is the newest line's own write
    expect(shouldPull(burst())).toBe(true);
  });

  it("waits until nothing came and the rev did not move for a while", () => {
    const moved = T0 + 1000;
    expect(shouldPull(burst({ lastArrival: T0 - 10_000, revMovedAt: moved, now: moved + STALE_QUIET_MS - 1 }))).toBe(false);
    expect(shouldPull(burst({ lastArrival: T0 - 10_000, revMovedAt: moved, now: moved + STALE_QUIET_MS }))).toBe(true);
    expect(shouldPull(burst({ lastArrival: moved, revMovedAt: T0, now: moved + STALE_QUIET_MS - 1 }))).toBe(false);
  });

  it("is not settled by an ask about the same updated_at: a `revs` event moves only the rev", () => {
    const v = burst();
    expect(shouldPull({ ...v, checkedFor: v.updatedAt })).toBe(true);
    expect(shouldPull({ ...v, updatedAt: undefined })).toBe(true);
  });

  it("keeps the pacing, and asks only while visible and not already asking", () => {
    const now = T0 + 60_000;
    expect(shouldPull(burst({ now, lastPull: now - STALE_PULL_EVERY_MS + 1 }))).toBe(false);
    expect(shouldPull(burst({ now, lastPull: now - STALE_PULL_EVERY_MS }))).toBe(true);
    expect(shouldPull(burst({ visible: false }))).toBe(false);
    expect(shouldPull(burst({ pulling: true }))).toBe(false);
  });

  it("waits STALE_QUIET_MS after a hidden tab becomes visible before pulling", () => {
    const woke = T0 + 20_000;
    expect(shouldPull(burst({ lastArrival: T0, revMovedAt: T0, visibleSince: woke, now: woke + STALE_QUIET_MS - 1 }))).toBe(false);
    expect(shouldPull(burst({ lastArrival: T0, revMovedAt: T0, visibleSince: woke, now: woke + STALE_QUIET_MS }))).toBe(true);
  });
});

describe("RevWatch", () => {
  it("takes the rev the page had when it first read as no move", () => {
    const w = new RevWatch(100);
    w.heard(100, T0);
    w.heard(undefined, T0);
    expect(w.unread).toBe(false);
    expect(w.movedAt).toBe(0);
    w.heard(110, T0 + 5);
    expect(w.unread).toBe(true);
    expect(w.movedAt).toBe(T0 + 5);
  });

  it("without a rev then, counts the first one heard as a move", () => {
    const w = new RevWatch(undefined);
    expect(w.unread).toBe(false);
    w.heard(100, T0);
    expect(w.unread).toBe(true);
  });

  it("is settled by a read sent after the move", () => {
    const w = new RevWatch(100);
    w.heard(110, T0);
    w.answered(w.sending());
    expect(w.unread).toBe(false);
    w.heard(110, T0 + 1); // the list catching up with a `revs` event it already heard
    expect(w.unread).toBe(false);
  });

  it("is not settled by a read sent before the move, however late that read comes back", () => {
    const w = new RevWatch(100);
    const read = w.sending();
    w.heard(110, T0);
    w.answered(read);
    expect(w.unread).toBe(true);
  });

  it("a read that comes back after a later one settles no less", () => {
    const w = new RevWatch(100);
    const early = w.sending();
    w.heard(110, T0);
    const late = w.sending();
    w.answered(late);
    w.answered(early);
    expect(w.unread).toBe(false);
  });
});

describe("the tally", () => {
  it("says not live after repeated recoveries, and a push clears it", () => {
    let t = FRESH_TALLY;
    for (let i = 1; i < STALE_HINT_AFTER; i++) {
      t = afterPull(t, "found");
      expect(notLive(t)).toBe(false);
    }
    t = afterPull(t, "found");
    expect(notLive(t)).toBe(true);
    expect(notLive(afterPush(t))).toBe(false);
  });

  it("an ask that finds nothing is not a miss, and does not clear the misses", () => {
    let t = afterPull(FRESH_TALLY, "found");
    t = afterPull(t, "empty");
    expect(t.recovered).toBe(1);
    expect(notLive(afterPull(t, "found"))).toBe(true);
  });

  it("failed asks count on their own and a good one clears them", () => {
    let t = FRESH_TALLY;
    for (let i = 0; i < STALE_HINT_AFTER; i++) t = afterPull(t, "failed");
    expect(notLive(t)).toBe(true);
    expect(afterPull(t, "empty").failed).toBe(0);
  });

  it("a push on a clean tally changes nothing", () => {
    expect(afterPush(FRESH_TALLY)).toBe(FRESH_TALLY);
  });

  it("treats isolated recoveries separated by more than STALE_RECOVERY_WINDOW_MS as separate blips", () => {
    let t = afterPull(FRESH_TALLY, "found", T0);
    expect(notLive(t)).toBe(false);
    // Past the window: starts a fresh streak instead of flipping notLive.
    t = afterPull(t, "found", T0 + STALE_RECOVERY_WINDOW_MS + 1);
    expect(t.recovered).toBe(1);
    expect(notLive(t)).toBe(false);
    // A second recovery within the window still trips notLive.
    t = afterPull(t, "found", T0 + STALE_RECOVERY_WINDOW_MS + 15_000);
    expect(t.recovered).toBe(2);
    expect(notLive(t)).toBe(true);
  });
});

describe("newestItemAt", () => {
  it("is the last item that has a time", () => {
    expect(newestItemAt([{ ts: "2026-11-05T10:00:00Z" }, { ts: "2026-11-05T10:00:05Z" }, {}])).toBe(Date.parse("2026-11-05T10:00:05Z"));
    expect(newestItemAt([{}, {}])).toBe(0);
    expect(newestItemAt([])).toBe(0);
  });
});
