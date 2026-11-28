import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { assertDelivered, deliverySummary, isKept, secondsLeft, shownUndelivered } from "./undelivered";

describe("undelivered messages (#10194)", () => {
  it("reads 202 delivered later as kept, never as delivered", () => {
    expect(isKept({ delivered: "later" })).toBe(true);
    expect(isKept({})).toBe(false);
    expect(isKept(undefined)).toBe(false);
  });
  it("turns a bare operation receipt into an in-doubt failure", () => {
    for (const state of ["unknown", "pending"]) {
      let err: unknown;
      try { assertDelivered({ state }); } catch (e) { err = e; }
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).body.delivered).toBe("unknown");
    }
    expect(() => assertDelivered({ delivered: "later" })).not.toThrow();
    expect(() => assertDelivered(undefined)).not.toThrow();
  });
  it("hides a kept message the transcript already has", () => {
    const row = { undelivered: [
      { id: "a", text: "one", at: "2026-11-17T10:00:00Z", state: "waiting" as const },
      { id: "b", text: "two", at: "2026-11-17T10:01:00Z", state: "failed" as const },
    ] };
    expect(shownUndelivered(row, [{ client_msg_id: "a" }, {}]).map((m) => m.id)).toEqual(["b"]);
    expect(shownUndelivered(undefined, [])).toEqual([]);
    expect(shownUndelivered(row, [], ["b"]).map((m) => m.id)).toEqual(["a"]); // still drawn as sending here
  });
});

describe("a logical conversation's delivery queue (#17431)", () => {
  const at = "2026-11-27T12:00:00Z";
  it("finds the input awaiting its receipt, counts the waiting and lists the expired", () => {
    const s = deliverySummary([
      { id: "kept", text: "", at, state: "failed" }, // #10194: no reason, not counted here
      { id: "old", text: "", at, state: "failed", reason: "receipt_expired" },
      { id: "head", text: "", at, state: "waiting", reason: "awaiting_receipt", deadline: "2026-11-27T12:02:30Z" },
      { id: "q", text: "", at, state: "waiting", reason: "behind_unconfirmed", blocked_by: "head" },
    ]);
    expect(s.head?.id).toBe("head");
    expect(s.waiting).toBe(2);
    expect(s.expired.map((m) => m.id)).toEqual(["old"]);
    expect(deliverySummary(undefined)).toEqual({ waiting: 0, expired: [] });
  });
  it("counts the seconds left, never below zero", () => {
    const now = Date.parse("2026-11-27T12:00:00Z");
    expect(secondsLeft("2026-11-27T12:01:29.200Z", now)).toBe(90);
    expect(secondsLeft("2026-11-27T11:59:00Z", now)).toBe(0);
    expect(secondsLeft(undefined, now)).toBeUndefined();
    expect(secondsLeft("not a time", now)).toBeUndefined();
  });
});
