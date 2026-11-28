// @vitest-environment jsdom
// The island at the MacBook notch (#7669): what the web sends the desktop app. The counts and the rows are of the same
// agents, the Agents list's: the ones that need you, then the working ones, then the ones that finished unseen, each
// state's newest first, each with its Agents-list line.
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";
import { DesktopIslandSignals, ISLAND_ROWS, islandState, setIslandOn } from "./desktop-island";
import type { DesktopIslandState } from "./desktop-types";
import type { Interaction, Session } from "./types";

// a paged hub's Agents list: the first page it pushes, and the complete list, read only while asked for
const lists = vi.hoisted(() => ({ pushed: [] as Session[], complete: [] as Session[], completeOn: false }));
vi.mock("./sessions", () => ({
  useAgentSessions: () => lists.pushed,
  useFullAgentSessions: (on: boolean) => {
    lists.completeOn = on;
    return { sessions: on ? lists.complete : [], loaded: on, error: "" };
  },
}));

const s = (id: string, o: Partial<Session>): Session => ({ machine_id: "mac", session_id: id, title: `示例 ${id}`, cwd: "/code", status: "idle", ...o });
const at = (minute: number) => `2030-01-01T09:${String(minute).padStart(2, "0")}:00Z`;

describe("islandState", () => {
  it("lists who needs you, then who works, newest first, each with what it asks or does", () => {
    const sessions = [
      s("w1", { state: "working", updated_at: at(1), activity: "在跑测试" }),
      s("n1", { state: "needs_you", updated_at: at(2) }),
      s("w2", { state: "working", updated_at: at(5) }),
      s("n2", { state: "needs_you", updated_at: at(3), attention_reason: "等你确认发布" }),
      s("n3", { state: "needs_you", updated_at: at(4) }),
      s("u1", { state: "unread", updated_at: at(9) }),
      s("i1", { state: "idle", updated_at: at(9) }),
      s("a1", { state: "needs_you", updated_at: at(9), assistant: true }),
    ];
    const card: Interaction = { id: "ix1", machine_id: "mac", session_id: "n1", kind: "question", questions: [{ question: "用哪个框架？" } as NonNullable<Interaction["questions"]>[number]] };
    // a child session's question: the Agents list folds it under n3 and shows it on n3's row
    const childCard: Interaction = { id: "ix2", machine_id: "mac", session_id: "c3", folded_under: "n3", kind: "question", questions: [{ question: "先合哪个分支？" } as NonNullable<Interaction["questions"]>[number]] };
    // frozen as the hooks' shared lists are treated: sorting it in place would throw
    const state = islandState(Object.freeze(sessions), [card, childCard]);
    expect(state.rows.map((r) => [r.title, r.state, r.label, r.line])).toEqual([
      ["示例 n3", "needs_you", "需要你", "向你提问：先合哪个分支？"],
      ["示例 n2", "needs_you", "需要你", "等你确认发布"],
      ["示例 n1", "needs_you", "需要你", "向你提问：用哪个框架？"],
      ["示例 w2", "working", "工作中", ""],
      ["示例 w1", "working", "工作中", "在跑测试"],
    ]);
    expect(state.rows.every((r) => r.route.startsWith("/") && !r.route.startsWith("//"))).toBe(true);
    expect(state.rows[0].route).toContain("n3");
    // the assistant's conversation that needs you is neither a row nor a count: the Agents list leaves it out
    expect([state.needsYou, state.working, state.unread]).toEqual([3, 2, 1]);
    expect(state.summary).toBe("3 个需要你，2 个工作中，1 个完成未看");
  });

  it("counts only the agents it can list: the assistant's conversation at work is neither", () => {
    const state = islandState([
      s("w1", { state: "working", updated_at: at(1), activity: "在跑测试" }),
      s("a1", { state: "working", updated_at: at(2), assistant: true }),
    ], []);
    expect(state.rows.map((r) => r.title)).toEqual(["示例 w1"]);
    expect([state.needsYou, state.working, state.unread]).toEqual([0, 1, 0]);
  });

  it("lists the ones that finished unseen after the working ones, with what they did", () => {
    const state = islandState([
      s("u1", { state: "unread", updated_at: at(9), last_text: "已合并到 main" }),
      s("w1", { state: "working", updated_at: at(1) }),
    ], []);
    expect(state.rows.map((r) => [r.title, r.state, r.label, r.line])).toEqual([
      ["示例 w1", "working", "工作中", ""],
      ["示例 u1", "unread", "完成未看", "已合并到 main"],
    ]);
    expect([state.needsYou, state.working, state.unread]).toEqual([0, 1, 1]);
  });

  it("keeps a working agent's place while it works: its updates do not move it", () => {
    const rows = (sessions: Session[]) => islandState(sessions, []).rows.map((r) => r.title);
    const w1 = s("w1", { state: "working", state_since: at(1), updated_at: at(8) });
    const w2 = s("w2", { state: "working", state_since: at(2), updated_at: at(3) });
    expect(rows([w1, w2])).toEqual(["示例 w2", "示例 w1"]);
    expect(rows([{ ...w1, updated_at: at(4) }, { ...w2, updated_at: at(9) }])).toEqual(["示例 w2", "示例 w1"]);
  });

  it("lists at most five and says nothing when nothing is going on", () => {
    const many = Array.from({ length: 8 }, (_, i) => s(`w${i}`, { state: "working", updated_at: at(i) }));
    const state = islandState(many, []);
    expect(state.rows).toHaveLength(ISLAND_ROWS);
    expect(state.working).toBe(8);
    expect(islandState([], [])).toEqual({ needsYou: 0, working: 0, unread: 0, rows: [], summary: "" });
  });

  it("lists every agent the counts count while the island is on, not only a paged hub's first page", async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const first = s("n1", { state: "needs_you", updated_at: at(1) });
    lists.pushed = [first];
    lists.complete = [first, s("n2", { state: "needs_you", updated_at: at(2) })];
    const setIslandState = vi.fn();
    Object.defineProperty(window, "personaDesktop", { value: { getIsland: async () => ({ supported: true, enabled: true }), setIslandState }, configurable: true });
    const last = () => setIslandState.mock.lastCall?.[0] as DesktopIslandState;
    const sent = () => last().rows.map((r) => r.title);
    const root = createRoot(document.createElement("div"));
    // on since the last run: the desktop says so when the web starts
    await act(async () => root.render(createElement(DesktopIslandSignals)));
    expect(lists.completeOn).toBe(true);
    expect(sent()).toEqual(["示例 n2", "示例 n1"]);
    expect(last().needsYou).toBe(2);
    // turned off in Settings: the complete list is not read, and the desktop keeps the pushed page's rows
    act(() => setIslandOn(false));
    expect(lists.completeOn).toBe(false);
    expect(sent()).toEqual(["示例 n1"]);
    expect(last().needsYou).toBe(1);
    // the shell gone (the credential stopped working): nothing of this account stays at the notch
    act(() => root.unmount());
    expect(setIslandState).toHaveBeenLastCalledWith({ needsYou: 0, working: 0, unread: 0, rows: [], summary: "" });
    delete (window as { personaDesktop?: unknown }).personaDesktop;
  });
});
