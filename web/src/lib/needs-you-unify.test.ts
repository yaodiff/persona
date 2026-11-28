import { describe, expect, it } from "vitest";
import { assistantEntries } from "./assistant-entries";
import { directUserWaiting, directWaitingCount } from "./direct-waiting";
import { countsLabel, directionHint, totalCounts, treeLeaders, userCheckLabel } from "./direction-names";
import { normalizeTree } from "./direction-tree";
import { ownerBadge } from "./todo-triage";
import { makeSessionLookup, type Todo } from "./todos";
import type { Interaction, Session } from "./types";

const NOW = Date.parse("2026-11-25T13:20:00Z");
const recent = "2026-11-25T13:05:00Z";

const session = (sid: string, patch: Partial<Session> = {}): Session =>
  ({ machine_id: "m", session_id: sid, title: sid, cwd: "/code/app", state: "idle", status: "idle", updated_at: recent, state_since: recent, ...patch }) as Session;

const agent = (sid: string) => ({ machine_id: "m", session_id: sid, linked_at: recent });

describe("unify Needs you across main chat, To-dos badge, and sidebar directions", () => {
  it("keeps 6 user_check tasks and 1 unescalated supervised 503 out of red Needs you while preserving verification and urgent A-class items", () => {
    const sessions: Session[] = [
      session("s-done"),
      session("s-503-sup", { turn_error: "API Error: 503 Service Unavailable", turn_error_kind: "transient" }),
      session("s-503-own", { turn_error: "API Error: 503 Service Unavailable", turn_error_kind: "transient" }),
      session("s-owner-ask", { state: "needs_you", status: "blocked", pending_interactions: 1 }),
      session("s-held", { state: "needs_you", status: "blocked", pending_interactions: 1 }),
    ];
    const interactions: Interaction[] = [
      { id: "ix_owner_q", machine_id: "m", session_id: "s-owner-ask", kind: "question", created_at: recent },
      { id: "ix_sup_held", machine_id: "m", session_id: "s-held", kind: "permission", delegating: true, created_at: recent },
    ];

    const userChecks: Todo[] = Array.from({ length: 6 }, (_, i) => ({
      id: `uc_${i + 1}`,
      title: `Read-only check ${i + 1}`,
      origin: "main",
      created_at: recent,
      updated_at: recent,
      agents: [agent("s-done")],
      supervision: { supervisor: "lead-a", state: "user_check", reviews: [{ at: recent, verdict: "accepted" }] },
      ask: { outcome: "done", verdict: "Verification ready", asked_at: recent },
    }));

    const sup503: Todo = {
      id: "sup_503",
      title: "Supervised 503 task",
      origin: "main",
      created_at: recent,
      updated_at: recent,
      agents: [agent("s-503-sup")],
      supervision: { supervisor: "lead-a", state: "", reviews: [] },
    };

    const heldCard: Todo = {
      id: "sup_held",
      title: "Supervisor held card task",
      origin: "main",
      created_at: recent,
      updated_at: recent,
      agents: [agent("s-held")],
      supervision: { supervisor: "lead-a", state: "", reviews: [] },
    };

    const genuineDecision: Todo = {
      id: "owner_decide",
      title: "Genuine owner decision",
      origin: "main",
      created_at: recent,
      updated_at: recent,
      agents: [agent("s-done")],
      supervision: { supervisor: "lead-a", state: "needs_user", reviews: [{ at: recent, verdict: "needs_user", reason: "Choose path" }] },
      ask: { outcome: "decide", verdict: "Choose path", asked_at: recent },
    };

    const directCard: Todo = {
      id: "owner_card",
      title: "Direct owner question",
      origin: "main",
      created_at: recent,
      updated_at: recent,
      agents: [agent("s-owner-ask")],
      supervision: { supervisor: "lead-a", state: "", reviews: [] },
    };

    const unsup503: Todo = {
      id: "unsup_503",
      title: "Personal terminal 503",
      origin: "main",
      created_at: recent,
      updated_at: recent,
      agents: [agent("s-503-own")],
    };

    const todos = [...userChecks, sup503, heldCard, genuineDecision, directCard, unsup503];
    const lookup = makeSessionLookup(sessions);

    // 1. Main chat assistantEntries + directUserWaiting (legacy/no-view fallback path)
    const entries = assistantEntries({ conversation: "main", main: "main", todos, sessions, interactions, plans: {}, feed: [], lookup, now: NOW });
    expect(entries.verify.map((i) => i.key)).toEqual(userChecks.map((u) => `task:${u.id}`));
    expect(entries.needsYou.map((i) => i.key).sort()).toEqual(["task:owner_card", "task:owner_decide", "task:unsup_503"]);

    const waiting = directUserWaiting(entries.needsYou);
    expect(directWaitingCount(waiting, [])).toBe(2); // owner_decide + owner_card (legacy directUserWaiting keeps direct cards & decisions, excludes generic interrupted unless view says needs_you)

    // With authoritative hub views: main chat, To-dos badge, and direction tree all match on exact A-class items (3).
    const withViews: Todo[] = [
      ...userChecks.map((u) => ({ ...u, view: { phase: "ready", section: "just_finished", reason: "finished_unseen", rank: 0 } as const })),
      { ...sup503, view: { phase: "working", section: "working", reason: "persona_working", rank: 0 } as const },
      { ...heldCard, view: { phase: "working", section: "working", reason: "persona_working", rank: 0 } as const },
      { ...genuineDecision, view: { phase: "needs_you", section: "needs_you", reason: "owner_decides", rank: 0 } as const },
      { ...directCard, view: { phase: "needs_you", section: "needs_you", reason: "agent_asks", rank: 0 } as const },
      { ...unsup503, view: { phase: "needs_you", section: "needs_you", reason: "interrupted", rank: 0 } as const },
    ];
    const entriesWithViews = assistantEntries({ conversation: "main", main: "main", todos: withViews, sessions, interactions, plans: {}, feed: [], lookup, now: NOW });
    expect(entriesWithViews.verify.map((i) => i.key)).toEqual(userChecks.map((u) => `task:${u.id}`));
    expect(entriesWithViews.needsYou.map((i) => i.key).sort()).toEqual(["task:owner_card", "task:owner_decide", "task:unsup_503"]);
    const waitingWithViews = directUserWaiting(entriesWithViews.needsYou);
    expect(directWaitingCount(waitingWithViews, [])).toBe(3);

    // 2. To-dos sidebar badge: with authoritative views excludes user_check, supervised 503, and supervisor-held cards;
    //    legacy fallback without views/interactions still excludes user_check and supervised 503.
    expect([...ownerBadge(withViews, lookup, NOW)].sort()).toEqual(["owner_card", "owner_decide", "unsup_503"]);
    expect([...ownerBadge(todos, lookup, NOW)].sort()).toEqual(["owner_card", "owner_decide", "sup_held", "unsup_503"]);
  });

  it("layers direction user_check counts calmly apart from red Needs you", () => {
    const tree = normalizeTree({
      directions: [
        { id: "lead-a", name: "Platform", counts: { working: 1, needs_you: 0, stuck: 0, review: 6, user_check: 6, planned: 0 }, running: false, tasks: [] },
        { id: "lead-b", name: "Search", counts: { working: 0, needs_you: 2, stuck: 0, review: 1, user_check: 1, planned: 0 }, running: true, tasks: [] },
      ],
    });
    const leaders = treeLeaders(tree);
    expect(leaders[0].counts).toEqual({ running: false, needs_you: 0, user_check: 6 });
    expect(leaders[1].counts).toEqual({ running: true, needs_you: 2, user_check: 1 });

    const tt = (key: string, o: { count: number }) =>
      key === "sidebar.leader_needs_you" ? `${o.count} need you` : key === "sidebar.leader_user_check" ? `${o.count} to verify` : `${key} ${o.count}`;

    // Direction with only user_check has empty red needs_you label and calm verification label.
    expect(countsLabel(leaders[0].counts, tt)).toBe("");
    expect(userCheckLabel(leaders[0].counts, tt)).toBe("6 to verify");
    expect(directionHint(leaders[0].counts, tt)).toBe("6 to verify");

    // Direction with both shows primary Needs you and secondary verification.
    expect(countsLabel(leaders[1].counts, tt)).toBe("2 need you");
    expect(userCheckLabel(leaders[1].counts, tt)).toBe("1 to verify");
    expect(directionHint(leaders[1].counts, tt)).toBe("2 need you · 1 to verify");

    const map = new Map(leaders.map((l) => [l.id, l.counts!]));
    expect(totalCounts(map, ["lead-a", "lead-b"])).toEqual({ running: true, needs_you: 2, user_check: 7 });
  });
});
