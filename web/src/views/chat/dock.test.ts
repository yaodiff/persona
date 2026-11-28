import { describe, expect, it } from "vitest";
import type { Interaction } from "../../lib/types";
import { AWAIT_MS, awaitingTurn, cardFold, cardLine, DOCK_MAX, INPUT_MAX, nextWaiting, pickupStateForWait, PLAN_MAX, turnStart, visibleShare, showsAnswered } from "./dock";

describe("visibleShare", () => {
  it("is a share of the visible height, capped when asked", () => {
    expect(visibleShare(0.55)).toBe("calc(var(--kb-h) * 0.55)");
    expect(visibleShare(0.35, 240)).toBe("min(240px, calc(var(--kb-h) * 0.35))");
  });

  it("keeps the dock to about half, the text box to 240px or a third of what is visible", () => {
    expect(DOCK_MAX).toBe("calc(var(--kb-h) * 0.55)");
    expect(INPUT_MAX).toBe("min(240px, calc(var(--kb-h) * 0.35))");
    expect(PLAN_MAX).toBe("calc(var(--kb-h) * 0.4)");
  });
});

describe("cardFold", () => {
  const wide = { narrow: false, keyboard: false, open: false };
  const narrow = { narrow: true, keyboard: false, open: false };

  it("shows only the first card on a wide screen, the keyboard or not", () => {
    expect(cardFold(3, wide)).toEqual({ full: 1, summary: null });
    expect(cardFold(3, { ...wide, keyboard: true })).toEqual({ full: 1, summary: null });
    expect(cardFold(3, { ...wide, open: true })).toEqual({ full: 3, summary: null });
  });

  it("shows only the first in full, with no summary line, on a narrow one", () => {
    expect(cardFold(3, narrow)).toEqual({ full: 1, summary: null });
    expect(cardFold(1, narrow)).toEqual({ full: 1, summary: null });
    expect(cardFold(0, narrow)).toEqual({ full: 0, summary: null });
  });

  it("shows them all once the position chip is tapped", () => {
    expect(cardFold(3, { ...narrow, open: true })).toEqual({ full: 3, summary: null });
  });

  it("folds them all into one line on a phone on its side, until tapped", () => {
    expect(cardFold(3, { ...narrow, short: true })).toEqual({ full: 0, summary: { kind: "short", count: 3 } });
    expect(cardFold(1, { ...narrow, short: true })).toEqual({ full: 0, summary: { kind: "short", count: 1 } });
    expect(cardFold(3, { ...narrow, short: true, open: true })).toEqual({ full: 3, summary: null });
    expect(cardFold(3, { ...narrow, short: true, keyboard: true })).toEqual({ full: 0, summary: { kind: "typing", count: 3 } });
  });

  it("folds them into one line while the keyboard is up, even opened", () => {
    expect(cardFold(3, { ...narrow, keyboard: true })).toEqual({ full: 0, summary: { kind: "typing", count: 3 } });
    expect(cardFold(1, { ...narrow, keyboard: true, open: true })).toEqual({ full: 0, summary: { kind: "typing", count: 1 } });
    expect(cardFold(0, { ...narrow, keyboard: true })).toEqual({ full: 0, summary: null });
  });
});

describe("cardLine", () => {
  const ix = (x: Partial<Interaction>) => ({ id: "i", machine_id: "m", session_id: "s", kind: "question", ...x }) as Interaction;
  it("says what each card asks, else what it waits for", () => {
    expect(cardLine(ix({ questions: [{ question: "用哪个框架？", options: [] }] as Interaction["questions"] }))).toBe("用哪个框架？");
    expect(cardLine(ix({ kind: "permission", tool_name: "Bash", tool_input: { description: "跑测试" } }))).toBe("跑测试");
    expect(cardLine(ix({ kind: "plan" }))).toBe("计划等待你审批");
    expect(cardLine(ix({ kind: "screen" }))).toBe("终端等待你处理");
  });
});

describe("nextWaiting (#4091)", () => {
  const ix = (id: string, mid: string, sid: string) => ({ id, machine_id: mid, session_id: sid, kind: "question" }) as Interaction;
  it("is the first card of another chat in the list's order, counting every card outside this chat", () => {
    const all = [ix("a", "m", "s"), ix("b", "m", "t"), ix("c", "m", "s"), ix("d", "n", "s")];
    expect(nextWaiting(all, "m", "s")).toEqual({ ix: all[1], count: 2 });
  });
  it("tells the same session id on another computer apart", () => {
    expect(nextWaiting([ix("a", "n", "s")], "m", "s")?.ix.id).toBe("a");
  });
  it("counts and opens only the authoritative request when a display copy exists", () => {
    const original = ix("original", "m", "side");
    const mirror = { ...ix("display", "m", "main"), mirror_of: original.id };
    expect(nextWaiting([original, mirror], "m", "third")).toEqual({ ix: original, count: 1 });
    expect(nextWaiting([original, mirror], "m", "side")).toBeNull();
  });
  it("is nothing when only this chat has cards, or none does", () => {
    expect(nextWaiting([ix("a", "m", "s")], "m", "s")).toBeNull();
    expect(nextWaiting([], "m", "s")).toBeNull();
  });
});

describe("turnStart", () => {
  it("is the turn's message, else its first item", () => {
    expect(turnStart({ user: { ts: "2026-11-05T01:00:00Z" } as never, start: "2026-11-05T01:00:02Z" })).toBe("2026-11-05T01:00:00Z");
    expect(turnStart({ start: "2026-11-05T01:00:02Z" })).toBe("2026-11-05T01:00:02Z");
    expect(turnStart(undefined)).toBeUndefined();
  });

  it("answered cards fold away with the rest while typing or on a short screen (#1996)", () => {
    expect(showsAnswered({ narrow: true, keyboard: true, open: false })).toBe(false);
    expect(showsAnswered({ narrow: true, keyboard: false, open: false, short: true })).toBe(false);
    expect(showsAnswered({ narrow: true, keyboard: false, open: true, short: true })).toBe(true);
    expect(showsAnswered({ narrow: true, keyboard: false, open: false })).toBe(true);
    expect(showsAnswered({ narrow: false, keyboard: true, open: false })).toBe(true);
  });
});

describe("awaitingTurn (#3654)", () => {
  const at = Date.parse("2026-11-05T10:00:00Z");
  const user = { id: "u1", text: "hi", images: [], contexts: [], ts: "2026-11-05T10:00:00Z" };
  const seg = (o: Record<string, unknown> = {}) => ({ user, steps: [], ...o }) as never;

  it("is on while your message is the newest thing and the hub has not said working", () => {
    expect(awaitingTurn(seg(), "idle", at + 2_000)).toBe(true);
    expect(awaitingTurn(seg(), "unread", at + 2_000)).toBe(true);
  });
  it("stops after a minute without anything", () => {
    expect(awaitingTurn(seg(), "idle", at + AWAIT_MS)).toBe(false);
  });
  it("is off once something answered, or without a message of yours last", () => {
    expect(awaitingTurn(seg({ steps: [{ kind: "text" }] }), "idle", at)).toBe(false);
    expect(awaitingTurn(seg({ answer: { id: "a" } }), "idle", at)).toBe(false);
    expect(awaitingTurn({ steps: [] } as never, "idle", at)).toBe(false);
    expect(awaitingTurn(undefined, "idle", at)).toBe(false);
  });
  it("holds working across early thought/tool pushes until the hub catches up or the same turn finishes", () => {
    for (const step of [{ kind: "thinking" }, { kind: "tool" }]) {
      const turn = seg({ steps: [step] });
      expect(awaitingTurn(turn, "idle", at + 500)).toBe(true);
      expect(awaitingTurn(turn, "idle", at + 500, undefined, true)).toBe(false);
      expect(awaitingTurn(turn, "offline", at + 500)).toBe(false);
    }
  });
  it("is off when the turn failed after the message, before writing anything (#3935)", () => {
    expect(awaitingTurn(seg(), "idle", at + 2_000, { since: "2026-11-05T10:00:01Z" })).toBe(false);
    expect(awaitingTurn(seg(), "idle", at + 2_000, { since: "2026-11-05T10:00:00Z" })).toBe(false);
    expect(awaitingTurn(seg(), "idle", at + 2_000, {})).toBe(false);
  });
  it("stays on past a failure from before the message", () => {
    expect(awaitingTurn(seg(), "idle", at + 2_000, { since: "2026-11-05T09:59:00Z" })).toBe(true);
  });
  it("leaves the hub's own states alone", () => {
    for (const st of ["working", "needs_you", "offline"] as const) expect(awaitingTurn(seg(), st, at)).toBe(false);
  });
});

describe("pickupStateForWait (#14941 review P1)", () => {
  it("demotes working to idle only while the work is background-only and the foreground has not started", () => {
    expect(pickupStateForWait("working", true, false)).toBe("idle");
  });

  it("keeps working once the echoed turn already shows foreground activity", () => {
    for (const foregroundActive of [true]) {
      expect(pickupStateForWait("working", true, foregroundActive)).toBe("working");
    }
  });

  it("never invents a background demotion without the explicit flag", () => {
    expect(pickupStateForWait("working", false, false)).toBe("working");
    expect(pickupStateForWait("working", undefined, false)).toBe("working");
  });

  it("passes non-working states through unchanged", () => {
    expect(pickupStateForWait("idle", true, false)).toBe("idle");
    expect(pickupStateForWait("unread", true, true)).toBe("unread");
  });
});
