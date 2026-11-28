import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Interaction, Session } from "./types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});
const fake = vi.hoisted(() => ({ state: {} as Record<string, unknown> }));
vi.mock("./store", async (importOriginal) => {
  const real = await importOriginal<typeof import("./store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), ...fake.state } as ReturnType<typeof real.getState>) };
});

const { useCounts } = await import("./sessions");
const { useScopedInteractions } = await import("./current-assistant");
const { waitingAgents: managerWaiting } = await import("./manager-attention");
const { waitingAgents: inboxWaiting } = await import("./inbox");
const { detectNewCards } = await import("./attention");

const at = "2026-11-20T08:00:00Z";
const original: Interaction = { id: "ix-original", machine_id: "m", session_id: "side", session_title: "Side chat", kind: "permission", created_at: at };
const mirror: Interaction = { ...original, id: "ix-main-copy", session_id: "main", mirror_of: original.id };
const sessions: Session[] = [
  { machine_id: "m", session_id: "side", title: "Side chat", cwd: "/assistant", status: "blocked", state: "needs_you", assistant: true, updated_at: at },
  { machine_id: "m", session_id: "main", title: "Main chat", cwd: "/assistant", status: "blocked", state: "needs_you", assistant: true, updated_at: at },
];
const mutes = { spaces: [], sessions: [] };

describe("approval display copies in aggregates", () => {
  it("counts and lists only the authoritative request, while conversation cards retain both", () => {
    const cards = [original, mirror];
    fake.state = { sessions: { m: sessions }, sessionSummaries: [], features: [], interactions: cards, loaded: true };
    let out: { count?: number; ids?: string[] } = {};
    function Probe() {
      out = { count: useCounts().interactions, ids: useScopedInteractions().map((ix) => ix.id) };
      return null;
    }
    renderToStaticMarkup(<Probe />);
    expect(out).toEqual({ count: 1, ids: [original.id] });
    const roster = { managers: [{ id: "a", name: "A" }], default: "a" } as unknown as Parameters<typeof managerWaiting>[0];
    expect(managerWaiting(roster, sessions, cards)).toEqual({ a: 1 });
    expect(inboxWaiting(cards, { m: sessions }, mutes, { source: "", kind: "", query: "" }).map((ix) => ix.id)).toEqual([original.id]);
  });

  it("does not announce the same approval again for its main copy", () => {
    const states = new Map([["m/side", "needs_you" as const], ["m/main", "needs_you" as const]]);
    const got = detectNewCards(new Map(), [original, mirror], states, states, sessions, new Set(), 1);
    expect(got.events.map((event) => [event.key, event.card.id])).toEqual([["m/side", original.id]]);
  });
});
