// The shared side-fold fixtures (spec/fixtures/side-fold, #7944): the rows the hub sends (the input with the manager's
// fold applied, or a paged list's rows as they come) go through the web's list choke point (foldRows); the rows left and
// each one's folded state, and each card's home and chat, must be the fixture's. The server reads the same cases
// (server/internal/assistant/fleet_side_fold_test.go).
import { readFileSync } from "node:fs";
import { afterEach, describe, expect, it } from "vitest";
import { requestHome } from "./child-sessions";
import { foldRows } from "./folded-rows";
import { activityOf } from "./format";
import { cardChat, setSideFold } from "./side-fold";
import type { Interaction, Session } from "./types";

type Fold = Pick<Session, "folded_under" | "folded_as" | "folded_side" | "children">;
interface Case {
  name: string;
  input: { sessions: Session[]; interactions: Interaction[]; paged?: boolean };
  expected: { fold: Record<string, Fold>; rows: { id: string; state: string }[]; cards: { id: string; home: string; chat: string }[] };
}
const fixture = JSON.parse(readFileSync(new URL("../../../spec/fixtures/side-fold/cases.json", import.meta.url), "utf8")) as { version: number; cases: Case[] };
const key = (s: { machine_id: string; session_id: string }) => `${s.machine_id}/${s.session_id}`;

afterEach(() => setSideFold(false));

describe("spec/fixtures/side-fold", () => {
  it("is version 1 with cases", () => {
    expect(fixture.version).toBe(1);
    expect(fixture.cases.length).toBeGreaterThan(0);
  });

  it.each(fixture.cases.map((c) => [c.name, c] as const))("%s", (_, c) => {
    setSideFold(true);
    // what the hub sends: the manager's fold on the node's rows; a paged list's rows are folded already
    const sent = c.input.paged ? c.input.sessions : c.input.sessions.map((s) => ({ ...s, ...c.expected.fold[key(s)] }) as Session);
    const listed = foldRows(sent).filter((s) => !s.archived);
    expect(listed.map((s) => ({ id: key(s), state: activityOf(s) }))).toEqual(c.expected.rows);
    // a card carries its session's fold (the hub copies it, Interaction.folded_*)
    const bySession = new Map(sent.map((s) => [key(s), s]));
    const cards = c.input.interactions.map((ix) => {
      const s = bySession.get(key(ix));
      const card = { ...ix, folded_under: s?.folded_under, folded_as: s?.folded_as, folded_side: s?.folded_side };
      return { id: ix.id, home: `${ix.machine_id}/${requestHome(card)}`, chat: `${ix.machine_id}/${cardChat(card)}` };
    });
    expect(cards).toEqual(c.expected.cards);
  });
});
