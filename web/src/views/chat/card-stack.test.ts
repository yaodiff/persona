import { describe, expect, it } from "vitest";
import type { AnsweredCard } from "../../lib/store";
import type { Interaction } from "../../lib/types";
import { ANSWERED_SHOWN_MS, answeredHideAt, answeredShown, answeredTally, keptAnswers, latestAnswer, nextRun, runPosition, tallyDetail } from "./card-stack";

const ix = (id: string, kind: Interaction["kind"] = "permission") => ({ id, machine_id: "m", session_id: "s", kind }) as Interaction;
const done = (id: string, kind: Interaction["kind"], resolution: AnsweredCard["resolution"], at?: number): AnsweredCard => ({ ix: ix(id, kind), resolution, at });
const perm = (id: string, allow: boolean, at?: number) => done(id, "permission", { allow }, at);

describe("nextRun", () => {
  it("grows with new cards, in the order they came", () => {
    expect(nextRun([], ["a", "b"])).toEqual(["a", "b"]);
    expect(nextRun(["a", "b"], ["a", "b", "c"])).toEqual(["a", "b", "c"]);
    expect(nextRun(["a", "b"], ["b", "d"])).toEqual(["a", "b", "d"]);
  });
  it("keeps a card that left counted", () => {
    expect(nextRun(["a", "b", "c"], ["b", "c"])).toEqual(["a", "b", "c"]);
  });
  it("resets once nothing waits", () => {
    expect(nextRun(["a", "b"], [])).toEqual([]);
  });
  it("returns the same array when unchanged", () => {
    const seen = ["a", "b"];
    expect(nextRun(seen, ["a", "b"])).toBe(seen);
    expect(nextRun(seen, ["b"])).toBe(seen);
    const empty: string[] = [];
    expect(nextRun(empty, [])).toBe(empty);
  });
});

describe("runPosition", () => {
  const ids = ["a", "b", "c", "d", "e", "f"];
  it("is 1 / 6 at the start and 2 / 6 after one is answered", () => {
    expect(runPosition(ids, 6)).toEqual({ position: 1, total: 6 });
    expect(runPosition(ids, 5)).toEqual({ position: 2, total: 6 });
    expect(runPosition(ids, 1)).toEqual({ position: 6, total: 6 });
  });
  it("grows its total when a card arrives mid-run", () => {
    const grown = nextRun(ids, ["c", "d", "e", "f", "g"]);
    expect(runPosition(grown, 5)).toEqual({ position: 3, total: 7 });
  });
  it("never counts fewer than what waits", () => {
    expect(runPosition([], 3)).toEqual({ position: 1, total: 3 });
  });
});

describe("answeredTally and tallyDetail", () => {
  it("says all allowed", () => {
    const tally = answeredTally([perm("a", true), perm("b", true)]);
    expect(tally).toEqual({ count: 2, allowed: 2, denied: 0, handedBack: 0 });
    expect(tallyDetail(tally)).toBe("全部允许");
  });
  it("splits allowed and denied", () => {
    const tally = answeredTally([perm("a", true), perm("b", true), perm("c", false)]);
    expect(tally).toEqual({ count: 3, allowed: 2, denied: 1, handedBack: 0 });
    expect(tallyDetail(tally)).toBe("2 项允许 · 1 项拒绝");
  });
  it("counts a hand back, whatever else the resolution says", () => {
    const tally = answeredTally([perm("a", true), done("b", "permission", { dismiss: true })]);
    expect(tally).toEqual({ count: 2, allowed: 1, denied: 0, handedBack: 1 });
    expect(tallyDetail(tally)).toBe("1 项允许 · 1 项交回电脑");
  });
  it("reads a plan's approve as allow / deny", () => {
    const tally = answeredTally([done("a", "plan", { approve: true }), done("b", "plan", { approve: false })]);
    expect(tally).toEqual({ count: 2, allowed: 1, denied: 1, handedBack: 0 });
  });
  it("counts a question only in the total, with no detail", () => {
    const tally = answeredTally([done("a", "question", { answers: { q: "A" } }), done("b", "question", { answers: { q: "B" } })]);
    expect(tally).toEqual({ count: 2, allowed: 0, denied: 0, handedBack: 0 });
    expect(tallyDetail(tally)).toBe("");
  });
});

describe("the answered line's timing", () => {
  const latest = 10_000;
  it("takes the newest answer's time", () => {
    expect(latestAnswer([perm("a", true, 5), perm("b", true, 9), perm("c", true)])).toBe(9);
    expect(latestAnswer([])).toBe(0);
  });
  it("shows before ANSWERED_SHOWN_MS, hides at it", () => {
    expect(answeredHideAt(latest, 0)).toBe(latest + ANSWERED_SHOWN_MS);
    expect(ANSWERED_SHOWN_MS).toBe(3000);
    expect(answeredShown({ now: latest + 2999, latest, released: 0, held: false })).toBe(true);
    expect(answeredShown({ now: latest + 3000, latest, released: 0, held: false })).toBe(false);
  });
  it("stays while held", () => {
    expect(answeredShown({ now: latest + 60_000, latest, released: 0, held: true })).toBe(true);
  });
  it("restarts a fresh 3 s from the release", () => {
    const released = latest + 20_000;
    expect(answeredHideAt(latest, released)).toBe(released + 3000);
    expect(answeredShown({ now: released + 2999, latest, released, held: false })).toBe(true);
    expect(answeredShown({ now: released + 3000, latest, released, held: false })).toBe(false);
  });
  it("a newer answer after the release wins", () => {
    expect(answeredHideAt(30_000, 20_000)).toBe(33_000);
  });
});

describe("keptAnswers", () => {
  it("keeps what the store let go of while open, oldest first", () => {
    const a = perm("a", true, 1);
    const b = perm("b", true, 2);
    const c = perm("c", true, 3);
    expect(keptAnswers([a, b], [c]).map((x) => x.ix.id)).toEqual(["a", "b", "c"]);
    expect(keptAnswers([c], [a, b]).map((x) => x.ix.id)).toEqual(["a", "b", "c"]);
  });
  it("lists none twice, preferring the store's copy", () => {
    const old = perm("a", true, 1);
    const fresh = perm("a", false, 1);
    const out = keptAnswers([old, perm("b", true, 2)], [fresh]);
    expect(out.map((x) => x.ix.id)).toEqual(["a", "b"]);
    expect(out[0]).toBe(fresh);
  });
  it("is the store's list with nothing kept", () => {
    const a = perm("a", true, 1);
    expect(keptAnswers([], [a])).toEqual([a]);
    expect(keptAnswers([], [])).toEqual([]);
  });
});
