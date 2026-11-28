// The seen outbox (lib/seen-outbox.ts): a session this page marked seen stays seen until the hub's own rows say it was
// recorded, a new turn ended, or the agent works or needs you again.
import { afterEach, describe, expect, it } from "vitest";
import { dueMarks, heldCounts, heldRows, heldSeen, liveMarks, markOver, newMark, nextDue, SEEN_CONFIRM_MS, SEEN_KEEP_MS, SEEN_SETTLE_MS, settleMarks, unansweredMarks, unseen, type SeenMark } from "./seen-outbox";
import { activityOf } from "./format";
import type { ListCounts } from "./session-list";
import { runtimeIdentity } from "./runtime-list";
import { setSideFold, unreadSides } from "./side-fold";
import type { Session } from "./types";

const T0 = Date.parse("2026-01-01T00:00:00Z");
const row = (patch: Partial<Session> = {}): Session => ({
  machine_id: "m", session_id: "s", title: "s", cwd: "/", status: "done", state: "unread",
  updated_at: "2026-01-01T00:00:00Z", seen_at: "0001-01-01T00:00:00Z", ...patch,
});

describe("seen outbox", () => {
  it.each([{ kind: "unseen" }, { kind: "just_finished" }, { kind: "asks", unseen: true }] as const)("acknowledges $kind locally while preserving the manager's view", (reason) => {
    const session = row({ state: "working", view: { phase: "done", reason, rank: 30 } });
    const mark = newMark(session, T0);
    expect(mark).toBeDefined();
    const shown = heldSeen(session, mark, T0 + 1000);
    expect(shown.view).toBe(session.view);
    expect(unseen(shown)).toBe(false);
    expect(activityOf(shown)).toBe("idle");
    expect(unseen(session)).toBe(true);
  });

  it("only waits for unread agents", () => {
    expect(newMark(row(), T0)).toBeDefined();
    expect(newMark(row({ state: "idle" }), T0)).toBeUndefined();
    expect(newMark(row({ state: "working", status: "working" }), T0)).toBeUndefined();
  });

  it("waits for Just finished agents too, and shows them seen meanwhile (#9751)", () => {
    const finished = row({ state: "idle", work: { phase: "done", line: "Merged", just_finished: true } });
    const mark = newMark(finished, T0)!;
    expect(mark).toBeDefined();
    const shown = heldSeen(finished, mark, T0 + 1000);
    expect(shown.state).toBe("idle");
    expect(shown.work).toEqual({ phase: "done", line: "Merged", just_finished: false });
    expect(markOver({ ...finished, seen_at: "2026-01-01T00:00:03Z", work: { phase: "done", line: "Merged" } }, mark, T0 + 3000)).toBe(true);
    expect(newMark(row({ state: "idle", work: { phase: "done", line: "Merged" } }), T0)).toBeUndefined();
  });

  it("holds an unread row seen until the hub records it", () => {
    const mark = newMark(row(), T0)!;
    // the hub's next list still says unread (the seen was lost or not taken yet): shown seen
    expect(heldSeen(row(), mark, T0 + 1000).state).toBe("idle");
    expect(markOver(row(), mark, T0 + 1000)).toBe(false);
    // the hub recorded it: the mark is over
    expect(markOver(row({ state: "idle", seen_at: "2026-01-01T00:00:03Z" }), mark, T0 + 3000)).toBe(true);
  });

  it("does not take the page's own seen_at for the hub's record", () => {
    const local = "2026-01-01T00:00:01.000Z";
    const mark = newMark(row(), T0, local)!;
    expect(markOver(row({ state: "idle", seen_at: local }), mark, T0 + 1000)).toBe(false);
  });

  it("lets a new turn, work or a question show again", () => {
    const mark = newMark(row(), T0)!;
    // the transcript touched without a new turn (inside the hub's 2 s): still the same turn
    expect(markOver(row({ updated_at: "2026-01-01T00:00:01Z" }), mark, T0)).toBe(false);
    // another turn ended after the look: unread again
    const next = row({ updated_at: "2026-01-01T00:01:00Z" });
    expect(markOver(next, mark, T0)).toBe(true);
    expect(heldSeen(next, mark, T0).state).toBe("unread");
    expect(markOver(row({ state: "working", status: "working" }), mark, T0)).toBe(true);
    expect(markOver(row({ state: "needs_you", status: "blocked" }), mark, T0)).toBe(true);
  });

  it("gives up after a while", () => {
    const mark = newMark(row(), T0)!;
    expect(markOver(row(), mark, T0 + SEEN_KEEP_MS + 1)).toBe(true);
    expect(liveMarks({ a: mark }, T0 + SEEN_KEEP_MS + 1)).toEqual({});
  });

  it("keeps the same array when nothing is held", () => {
    const rows = [row({ session_id: "a" }), row({ session_id: "b" })];
    expect(heldRows(rows, {}, T0)).toBe(rows);
    const held = heldRows(rows, { [runtimeIdentity(rows[1])]: newMark(rows[1], T0)! }, T0);
    expect(held[0]).toBe(rows[0]);
    expect(held[1].state).toBe("idle");
  });

  it("retries a socket mark over HTTP after SEEN_CONFIRM_MS, and resends what was not answered", () => {
    const socket: SeenMark = { ...newMark(row(), T0)!, route: "socket" };
    const http: SeenMark = { ...socket, route: "http" };
    const answered: SeenMark = { ...http, answered: true };
    const marks = { a: socket, b: http, c: answered };
    expect(dueMarks(marks, T0 + SEEN_CONFIRM_MS - 1)).toEqual([]);
    expect(dueMarks(marks, T0 + SEEN_CONFIRM_MS)).toEqual(["a"]);
    expect(nextDue(marks, T0)).toBe(T0 + SEEN_CONFIRM_MS);
    expect(unansweredMarks(marks, T0)).toEqual(["a", "b"]);
  });
});

describe("seen outbox: settling and counts", () => {
  const counts = (unread: number, idle: number, justFinished?: number): ListCounts =>
    ({ total: unread + idle, revision: "r", state: { unread, idle }, machine: {}, project: {}, group: {}, ...(justFinished === undefined ? {} : { just_finished: justFinished }) });

  it("holds a row read before the hub recorded the mark while it settles, and lets go after", () => {
    const stale = row();
    const id = runtimeIdentity(stale);
    const marks = { [id]: newMark(stale, T0)! };
    // one list (the chat's, the store's) shows it recorded
    const settled = settleMarks([row({ state: "idle", seen_at: "2026-01-01T00:00:01Z" })], marks, T0 + 1000);
    expect(settled[id].recordedAt).toBe(T0 + 1000);
    // the sidebar's list has not read since: its row is still the one before the look, and is drawn seen
    expect(heldSeen(stale, settled[id], T0 + 2000).state).toBe("idle");
    // nothing more is sent for it
    expect(dueMarks({ ...settled, [id]: { ...settled[id], sentAt: T0 } }, T0 + SEEN_CONFIRM_MS)).toEqual([]);
    expect(unansweredMarks(settled, T0 + 2000)).toEqual([]);
    expect(nextDue(settled, T0 + 2000)).toBeUndefined();
    // a later row saying recorded again keeps the first time
    expect(settleMarks([row({ state: "idle", seen_at: "2026-01-01T00:00:01Z" })], settled, T0 + 5000)).toBe(settled);
    // settled long enough: gone
    expect(liveMarks(settled, T0 + 1000 + SEEN_SETTLE_MS + 1)).toEqual({});
  });

  it("drops a settling mark at once when the agent moves on", () => {
    const id = runtimeIdentity(row());
    const settled = settleMarks([row({ state: "idle", seen_at: "2026-01-01T00:00:01Z" })], { [id]: newMark(row(), T0)! }, T0 + 1000);
    const next = row({ updated_at: "2026-01-01T00:01:00Z", seen_at: "2026-01-01T00:00:01Z" });
    expect(heldSeen(next, settled[id], T0 + 2000).state).toBe("unread");
    expect(settleMarks([next], settled, T0 + 2000)).toEqual({});
    expect(settleMarks([row({ state: "working", status: "working" })], settled, T0 + 2000)).toEqual({});
  });

  it("keeps the same marks when no row changes them", () => {
    const id = runtimeIdentity(row());
    const marks = { [id]: newMark(row(), T0)! };
    expect(settleMarks([row(), row({ session_id: "other" })], marks, T0 + 1000)).toBe(marks);
  });

  it("leaves held agents out of the hub's Unread and Just finished counts read with their rows", () => {
    const a = row({ session_id: "a" });
    const b = row({ session_id: "b" });
    const done = row({ session_id: "c", state: "idle", work: { phase: "done", line: "Merged", just_finished: true } });
    const marks = { [runtimeIdentity(a)]: newMark(a, T0)!, [runtimeIdentity(done)]: newMark(done, T0)! };
    const c = counts(2, 3, 1);
    expect(heldCounts(c, [a, b, done], marks, T0 + 1000)).toEqual({ ...c, state: { unread: 1, idle: 4 }, just_finished: 0 });
    // rows read after the hub recorded it: the counts read with them have it already
    const recorded = row({ session_id: "a", state: "idle", seen_at: "2026-01-01T00:00:01Z" });
    expect(heldCounts(counts(1, 4), [recorded, b], marks, T0 + 1000)).toEqual(counts(1, 4));
    // nothing held: the same object
    expect(heldCounts(c, [b], marks, T0 + 1000)).toBe(c);
    // never below zero
    expect(heldCounts(counts(0, 5), [a], marks, T0 + 1000).state.unread).toBe(0);
  });
});

describe("a parent kept Unread by its side sessions (#13299)", () => {
  afterEach(() => setSideFold(false));
  const counts = (unread: number, idle: number): ListCounts => ({ total: unread + idle, revision: "r", state: { unread, idle }, machine: {}, project: {}, group: {} });
  // the hub counts the parent by its folded state: its own reply was seen, one side's was not
  const parent = row({ session_id: "p", state: "idle", children: { total: 2, sides: 2, sides_unread: 1 } });
  const side = (session_id: string, patch: Partial<Session> = {}) => row({ session_id, folded_under: "p", folded_as: "side", ...patch });
  const unread = side("a", { updated_at: "2026-01-01T00:00:05Z" });
  const later = side("b", { updated_at: "2026-01-01T00:00:09Z" });
  const sidesOf = (held: Session[]) => (s: Session) => (s.session_id === "p" ? held : undefined);

  it("orders the unread sides the newest first and leaves out the rest", () => {
    setSideFold(true);
    expect(unreadSides([unread, side("c", { state: "idle" }), later, row({ session_id: "own" })]).map((s) => s.session_id)).toEqual(["b", "a"]);
    // without side folding no row is a side
    setSideFold(false);
    expect(unreadSides([unread, later])).toEqual([]);
  });

  it("takes the parent out of Unread once the held sides were all seen here, not before", () => {
    setSideFold(true);
    const c = counts(1, 4);
    // the side rows held for it, still unread: counted as the hub did
    expect(heldCounts(c, [parent], {}, T0 + 1000, sidesOf([unread]))).toBe(c);
    // seen here (the held row is the seen copy): Seen at once, before the hub's next read
    expect(heldCounts(c, [parent], {}, T0 + 1000, sidesOf([{ ...unread, state: "idle" }])).state).toEqual({ unread: 0, idle: 5 });
    // no side rows held for it (no open chat, nothing read yet): the hub's rollup stands
    expect(heldCounts(c, [parent], {}, T0 + 1000, () => undefined)).toBe(c);
  });

  it("keeps a parent seen here Unread while a side of it is", () => {
    setSideFold(true);
    const own = row({ session_id: "p", children: { total: 1, sides: 1, sides_unread: 1 } });
    const marks = { [runtimeIdentity(own)]: newMark(own, T0)! };
    expect(heldCounts(counts(1, 4), [own], marks, T0 + 1000).state).toEqual({ unread: 1, idle: 4 });
    expect(heldCounts(counts(1, 4), [own], marks, T0 + 1000, sidesOf([unread])).state).toEqual({ unread: 1, idle: 4 });
    // its side seen too: Seen
    expect(heldCounts(counts(1, 4), [own], marks, T0 + 1000, sidesOf([{ ...unread, state: "idle" }])).state).toEqual({ unread: 0, idle: 5 });
  });
});
