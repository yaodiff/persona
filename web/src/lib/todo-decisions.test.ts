import { decisionSourceLabel } from "./todo-decision-label";
import { describe, expect, it } from "vitest";
import type { Session } from "./types";
import { makeSessionLookup, supersedeCandidates, type Todo, type TodoDecision } from "./todos";

const dec = (id: string, patch: Partial<TodoDecision> = {}): TodoDecision => ({
  id, todo_id: "todo_1", question: `q-${id}`, choice: `c-${id}`, reason: "", source: "manual", source_machine: "",
  source_session: "", source_message: "", supersedes_id: "", superseded_by: "", created_at: "2026-11-04T10:00:00Z", ...patch,
});

const todo: Todo = {
  id: "todo_1", title: "发版", created_at: "2026-11-04T09:00:00Z", updated_at: "2026-11-04T09:00:00Z",
  agents: [{ machine_id: "mac", session_id: "gone", linked_at: "2026-11-04T09:00:00Z", title: "旧 Agent", cwd: "/code", agent: "claude", source: "manual" }],
};

describe("decisionSourceLabel", () => {
  const sessions = [{ machine_id: "mac", session_id: "s1", title: "发版 Agent" }] as Session[];
  const lookup = makeSessionLookup(sessions);
  it("says a decision written by hand is one", () => {
    expect(decisionSourceLabel(dec("a"), todo, lookup)).toBe("手动记录");
  });
  it("says a decision recorded when the task was finished came from the agent's outcome", () => {
    expect(decisionSourceLabel(dec("a", { source: "outcome", source_machine: "mac", source_session: "s1" }), todo, lookup)).toBe("记录于 Agent 完成时");
  });
  it("says a decision an agent noted while it worked is the agent's, not the user's", () => {
    expect(decisionSourceLabel(dec("a", { source: "agent", source_machine: "mac", source_session: "s1" }), todo, lookup)).toBe("由 Agent 执行期间记录");
  });
  it("says a decision the user gave in the todo's chat thread came from there", () => {
    expect(decisionSourceLabel(dec("a", { source: "thread", source_message: "feishu-om_1" }), todo, lookup)).toBe("来自飞书话题回复");
  });
  it("names the agent whose card it was answered in, from the session list or the todo's snapshot", () => {
    expect(decisionSourceLabel(dec("a", { source: "card", source_machine: "mac", source_session: "s1" }), todo, lookup)).toBe("回复自「发版 Agent」的提问");
    expect(decisionSourceLabel(dec("a", { source: "card", source_machine: "mac", source_session: "gone" }), todo, lookup)).toBe("回复自「旧 Agent」的提问");
    expect(decisionSourceLabel(dec("a", { source: "card", source_machine: "mac", source_session: "nobody" }), todo, lookup)).toBe("回复自 Agent 的提问");
    const long = makeSessionLookup([{ machine_id: "mac", session_id: "s2", title: "请立刻用 AskUserQuestion 工具问我一个问题（只问一个）" }] as Session[]);
    expect(decisionSourceLabel(dec("a", { source: "card", source_machine: "mac", source_session: "s2" }), todo, long)).toBe("回复自「请立刻用 AskUserQuestion 工具问…」的提问");
  });
});

describe("supersedeCandidates", () => {
  it("offers the earlier decisions in effect, not the later ones, itself or one it already replaces", () => {
    const all = [dec("a", { superseded_by: "c" }), dec("b"), dec("c", { supersedes_id: "a" }), dec("d")];
    expect(supersedeCandidates(all[2], all).map((d) => d.id)).toEqual(["b"]);
    expect(supersedeCandidates(all[3], all).map((d) => d.id)).toEqual(["b", "c"]);
    expect(supersedeCandidates(all[1], all)).toEqual([]);
    expect(supersedeCandidates(dec("x"), all)).toEqual([]);
  });
});
