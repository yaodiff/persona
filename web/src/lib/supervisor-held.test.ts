// Cards a task's supervisor holds (lib/supervisor-held.ts): `delegating` on any card but a question. They and the agents
// waiting only on them are left out of Needs you's number and badge, its sounds and notifications, and its one-tap
// answers; a delegating question (the assistant's own look, lib/delegate.ts) and an unmarked card stay as they were.
import { describe, expect, it } from "vitest";
import { cardArrivalKey, detectNewCards, detectTransitions, heldOnlyNeeds } from "./attention";
import { waitingCard } from "./notify";
import { quickAnswers } from "./quick-answers";
import { boundedCounts, stateCounts } from "./sessions";
import { heldAsWorking, rowCards, supervisorHeld, supervisorHeldOnly, userCards } from "./supervisor-held";
import type { AgentMutes } from "./agent-groups";
import type { ListCounts } from "./session-list";
import type { AgentState, Interaction, Session } from "./types";

const session = (id: string, state: AgentState, patch: Partial<Session> = {}): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/tmp/p", status: state, state, updated_at: "2026-01-01T10:00:00Z", ...patch,
});
const screen = (id: string, sid: string, patch: Partial<Interaction> = {}): Interaction => ({
  id, machine_id: "m", session_id: sid, kind: "screen", created_at: "2026-01-01T10:00:00Z",
  dialog: { question: "Do you want to proceed?", detail: "rmdir build", options: [{ key: "1", label: "Yes" }, { key: "2", label: "No" }] }, ...patch,
});
const card = (id: string, sid: string, kind: Interaction["kind"], patch: Partial<Interaction> = {}): Interaction => ({ id, machine_id: "m", session_id: sid, kind, created_at: "2026-01-01T09:00:00Z", ...patch });
const noMutes: AgentMutes = { spaces: [], sessions: [], allowed: [] };
const prevOf = (...pairs: [string, AgentState][]) => new Map(pairs.map(([id, st]) => [`m/${id}`, st] as const));

describe("supervisorHeld", () => {
  it("is a delegating card of any kind but a question", () => {
    for (const kind of ["screen", "permission", "plan", "elicitation"] as const) expect(supervisorHeld({ kind, delegating: true }), kind).toBe(true);
    expect(supervisorHeld({ kind: "question", delegating: true })).toBe(false);
    expect(supervisorHeld({ kind: "screen" })).toBe(false);
    expect(supervisorHeld({ kind: "screen", delegating: false })).toBe(false);
  });

  it("userCards drops only the held cards, and gives the same array when none is", () => {
    const mine = [screen("a", "s"), card("q", "s", "question", { delegating: true })];
    expect(userCards(mine)).toBe(mine);
    expect(userCards([...mine, screen("h", "s", { delegating: true })]).map((ix) => ix.id)).toEqual(["a", "q"]);
  });

  it("supervisorHeldOnly names the sessions whose every card, at least one, is held; a child's under its parent", () => {
    const held = supervisorHeldOnly([
      screen("h1", "only", { delegating: true }),
      screen("h1-main-copy", "only", { mirror_of: "h1" }),
      screen("h2", "mixed", { delegating: true }),
      card("p", "mixed", "permission"),
      card("q", "asking", "question", { delegating: true }),
      screen("c", "child", { delegating: true, folded_under: "parent" }),
    ]);
    expect([...held].sort()).toEqual(["m/only", "m/parent"]);
  });

  it("rowCards summarizes own/child user cards, own held count, and held-only state for one row", () => {
    const cards = [
      screen("h1", "only", { delegating: true }),
      screen("h2", "mixed", { delegating: true }),
      card("p", "mixed", "permission"),
      card("q", "asking", "question", { delegating: true }),
      screen("c", "child", { delegating: true, folded_under: "parent" }),
      screen("h3", "parent-mixed", { delegating: true, folded_under: "parent-mixed-root" }),
      card("p2", "parent-mixed-root", "permission"),
    ];
    expect(rowCards(cards, "m", "only")).toEqual({ ix: undefined, kidCount: 0, ownHeld: 1, heldOnly: true });
    expect(rowCards(cards, "m", "parent")).toEqual({ ix: undefined, kidCount: 0, ownHeld: 0, heldOnly: true });
    expect(rowCards(cards, "m", "mixed")).toEqual({ ix: cards[2], kidCount: 0, ownHeld: 1, heldOnly: false });
    expect(rowCards([...cards].reverse(), "m", "mixed")).toEqual({ ix: cards[2], kidCount: 0, ownHeld: 1, heldOnly: false });
    expect(rowCards(cards, "m", "parent-mixed-root")).toEqual({ ix: cards[6], kidCount: 0, ownHeld: 0, heldOnly: false });
    expect(rowCards(cards, "m", "asking")).toEqual({ ix: cards[3], kidCount: 0, ownHeld: 0, heldOnly: false });
    expect(rowCards(cards, "m", "bare")).toEqual({ ix: undefined, kidCount: 0, ownHeld: 0, heldOnly: false });
    expect(rowCards([], "m", "only")).toEqual({ ix: undefined, kidCount: 0, ownHeld: 0, heldOnly: false });
  });

  it("heldAsWorking lists an agent waiting only on held cards as Working, and leaves the rest as they are", () => {
    const sessions = [session("only", "needs_you"), session("mixed", "needs_you"), session("bare", "needs_you")];
    const cards = [screen("h1", "only", { delegating: true }), screen("h2", "mixed", { delegating: true }), card("p", "mixed", "permission")];
    expect(heldAsWorking(sessions, cards).map((s) => s.state)).toEqual(["working", "needs_you", "needs_you"]);
    expect(heldAsWorking(sessions, [card("p", "mixed", "permission")])).toBe(sessions);
  });
});

describe("stateCounts with held cards", () => {
  const all = [session("held", "needs_you"), session("mine", "needs_you"), session("mixed", "needs_you"), session("bare", "needs_you"), session("w", "working")];
  const cards = [screen("h", "held", { delegating: true }), screen("s", "mine"), screen("hm", "mixed", { delegating: true }), card("p", "mixed", "permission")];

  it("counts an agent waiting only on held cards in neither Needs you nor the badge (Working instead)", () => {
    expect(stateCounts(all, cards)).toEqual({ needs: 3, working: 2, waiting: 0, unread: 0, badge: 3 });
  });

  it("keeps an unheld screen card's agent, one with a card of its own beside a held one, and one with no card", () => {
    expect(stateCounts(all, cards.map((ix) => ({ ...ix, delegating: undefined })))).toEqual({ needs: 4, working: 1, waiting: 0, unread: 0, badge: 4 });
    expect(stateCounts(all)).toEqual({ needs: 4, working: 1, waiting: 0, unread: 0, badge: 4 });
  });

  it("keeps an agent waiting on a question the assistant is still looking at counted, as before", () => {
    expect(stateCounts([session("a", "needs_you")], [card("q", "a", "question", { delegating: true })]).badge).toBe(1);
  });
});

describe("boundedCounts (a paging hub's counts)", () => {
  const counts = (patch: Partial<Pick<ListCounts, "held_needs" | "supervisor_held">> = {}) => ({ state: { needs_you: 5, working: 3, unread: 1 }, ...patch });

  it("takes supervisor_held out of Needs you and the badge, graded or not, into Working", () => {
    expect(boundedCounts(counts({ supervisor_held: 2 }), false)).toEqual({ needs: 3, working: 5, waiting: 0, unread: 1, badge: 3 });
    expect(boundedCounts(counts({ supervisor_held: 2, held_needs: 1 }), true)).toEqual({ needs: 3, working: 5, waiting: 0, unread: 1, badge: 2 });
  });

  it("is as before without the field (an older hub), and never goes below zero", () => {
    expect(boundedCounts(counts({ held_needs: 1 }), false)).toEqual({ needs: 5, working: 3, waiting: 0, unread: 1, badge: 5 });
    expect(boundedCounts(counts({ held_needs: 1 }), true).badge).toBe(4);
    expect(boundedCounts(counts({ supervisor_held: 9, held_needs: 9 }), true)).toMatchObject({ needs: 0, badge: 0, working: 8 });
  });
});

describe("attention for held cards", () => {
  it("does not chime or notify for an agent waiting only on held cards, and does once the card is passed up", () => {
    const held = detectTransitions(prevOf(["a", "working"]), [session("a", "needs_you")], noMutes, [screen("h", "a", { delegating: true })]);
    expect(held.events).toEqual([]);
    expect(held.next.get("m/a")).toBe("working");
    expect(detectTransitions(held.next, [session("a", "needs_you")], noMutes, [screen("h", "a")]).events).toEqual([{ kind: "needs", key: "m/a" }]);
  });

  it("still chimes for an unheld screen card, and for an agent with a card of its own beside a held one", () => {
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "needs_you")], noMutes, [screen("s", "a")]).events).toEqual([{ kind: "needs", key: "m/a" }]);
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "needs_you")], noMutes, [screen("h", "a", { delegating: true }), card("p", "a", "permission")]).events).toEqual([{ kind: "needs", key: "m/a" }]);
  });

  it("neither announces nor remembers a held card; announces it once it is passed up", () => {
    const waiting = prevOf(["a", "needs_you"]);
    const sessions = [session("a", "needs_you")];
    const T = 1_000_000;
    const first = detectNewCards(new Map([[cardArrivalKey(card("p", "a", "permission")), T - 1000]]), [card("p", "a", "permission"), screen("h", "a", { delegating: true })], waiting, waiting, sessions, new Set(), T);
    expect(first.events).toEqual([]);
    expect(first.ids.has("h")).toBe(false);
    const back = detectNewCards(first.ids, [card("p", "a", "permission"), screen("h", "a")], waiting, waiting, sessions, new Set(), T + 1000);
    expect(back.events).toEqual([{ kind: "needs", key: "m/a", card: screen("h", "a") }]);
  });

  it("passes a held card over when grading and when picking the card a notification is about", () => {
    const p1 = card("p1", "a", "permission", { push_level: "P1" });
    expect(heldOnlyNeeds(session("a", "needs_you"), [p1, screen("h", "a", { delegating: true })])).toBe(true);
    expect(waitingCard([screen("h", "a", { delegating: true, created_at: "2026-01-01T08:00:00Z" }), p1], session("a", "needs_you"))?.id).toBe("p1");
  });

  it("offers no one-tap answer for a held card, and the dialog's options for an unheld one", () => {
    expect(quickAnswers(screen("h", "a", { delegating: true }))).toBeNull();
    expect(quickAnswers(card("p", "a", "permission", { delegating: true }))).toBeNull();
    expect(quickAnswers(screen("s", "a"))?.map((c) => c.label)).toEqual(["Yes", "No"]);
  });
});
