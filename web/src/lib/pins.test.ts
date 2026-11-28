import { describe, expect, it } from "vitest";
import { agentHandoffText } from "./agent-handoff";
import { buildSpaces } from "./agent-groups";
import { isPinned, isSpacePinned, pinnedSessions, pinsOf, togglePin, toggleSpacePin } from "./pins";
import type { Session } from "./types";

const session = (id: string, patch: Partial<Session> = {}): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/home/me/code/app", status: "idle", state: "idle", ...patch,
});

describe("置顶（/pins，iOS PinsStore）", () => {
  it("旧 hub 没有字段时当作没有置顶", () => {
    expect(pinsOf(undefined)).toEqual({ spaces: [], sessions: [] });
    expect(pinsOf({ spaces: ["m/a"] })).toEqual({ spaces: ["m/a"], sessions: [] });
  });

  it("置顶放到最前，再点一次取消；空间按名字", () => {
    let p = pinsOf({});
    p = togglePin(session("a"), p);
    p = togglePin(session("b"), p);
    expect(p.sessions).toEqual(["m/b", "m/a"]);
    expect(isPinned(session("a"), p)).toBe(true);
    p = togglePin(session("a"), p);
    expect(p.sessions).toEqual(["m/b"]);
    p = toggleSpacePin("m", "天气", p);
    expect(isSpacePinned("m", "天气", p)).toBe(true);
    expect(isSpacePinned("m", undefined, p)).toBe(false);
    expect(toggleSpacePin("m", "天气", p).spaces).toEqual([]);
  });

  it("置顶的 Agent 按置顶顺序（最新的在前）", () => {
    const list = [session("a"), session("b"), session("c")];
    expect(pinnedSessions(list, { spaces: [], sessions: ["m/c", "m/x", "m/a"] }).map((s) => s.session_id)).toEqual(["c", "a"]);
    expect(pinnedSessions(list, { spaces: [], sessions: [] })).toEqual([]);
  });
});

describe("按空间分组（buildSpaces）", () => {
  it("herdr 的顺序：机器、空间、标签；不在空间里的另放", () => {
    const machines = [{ id: "m2", name: "B", online: true }, { id: "m", name: "A", online: true }];
    const live = [
      session("t2", { space_id: "w1", space_name: "app", space_index: 1, slot_index: 2 }),
      session("t1", { space_id: "w1", space_name: "app", space_index: 1, slot_index: 1 }),
      session("o", { machine_id: "m2", space_id: "w9", space_name: "other", space_index: 9 }),
      session("loose"),
    ];
    const { spaces, loose } = buildSpaces(live, machines);
    expect(spaces.map((sp) => [sp.key, sp.name, sp.sessions.map((s) => s.session_id)])).toEqual([
      ["m2/w9", "other", ["o"]],
      ["m/w1", "app", ["t1", "t2"]],
    ]);
    expect(loose.map((s) => s.session_id)).toEqual(["loose"]);
    expect("index" in spaces[0]).toBe(false);
  });
});

describe("交给 Persona（iOS AgentHandoff）", () => {
  it("Agent 的位置和最后说的作为上下文卡片，再加上要求", () => {
    const t = agentHandoffText(session("abc", { title: "", last_text: "做完了", state: "unread" }), "我的电脑");
    expect(t.startsWith('<persona-context kind="context" title="Agent · app" url="">\n机器：我的电脑（m）\n会话：abc\n目录：/home/me/code/app\n状态：unread\n最后一条消息：做完了\n</persona-context>\n\n')).toBe(true);
    expect(t).toContain("agent_read");
    expect(agentHandoffText(session("x", { last_text: "字".repeat(900) })).match(/字/g)).toHaveLength(800);
  });
});
