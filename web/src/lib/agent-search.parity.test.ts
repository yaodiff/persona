import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { agentSearchHit } from "./agent-search";
import { stateOf } from "./format";
import { runtimeOrder } from "./runtime-list";
import type { AgentPins } from "./pins";
import type { Interaction, Machine, Session } from "./types";

// The Go index and the existing local matcher consume the same independent query corpus.
const fixture = JSON.parse(readFileSync(new URL("../../../spec/fixtures/session-search/cases.json", import.meta.url), "utf8")) as {
  now: string; machines: Machine[]; pins: AgentPins; interactions: Interaction[]; sessions: Session[];
  cases: { name: string; query: string; input: { query: string }; source: string; description: string; expected: { machine_id: string; session_id: string; score: number }[] }[];
};
const context = { machines: fixture.machines, pins: fixture.pins, interactions: fixture.interactions, now: Date.parse(fixture.now) };
describe("runtime search shared local/server query contract", () => {
  it("contains independently named, sourced queries rather than an empty replay", () => {
    expect(fixture.cases.length).toBeGreaterThan(0);
    expect(new Set(fixture.cases.map((item) => item.name)).size).toBe(fixture.cases.length);
    for (const item of fixture.cases) {
      expect(item.name).toMatch(/^[a-z0-9_]+$/);
      expect(item.source).not.toBe(""); expect(item.description).not.toBe("");
      expect(item.input.query).toBe(item.query);
    }
  });
  for (const test of fixture.cases) it(test.name, () => {
    const hits = fixture.sessions.map((session) => ({ session, hit: agentSearchHit(session, test.query, context) }))
      .filter(({ hit }) => hit.score > 0)
      .map(({ session, hit }) => ({ session, score: hit.score + (stateOf(session) === "offline" ? 0 : 5) }))
      .sort((a, b) => b.score - a.score || runtimeOrder(a.session, b.session))
      .map(({ session, score }) => ({ machine_id: session.machine_id, session_id: session.session_id, score }));
    expect(hits).toEqual(test.expected);
  });
});
