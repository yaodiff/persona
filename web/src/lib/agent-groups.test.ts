import { describe, expect, it } from "vitest";
import { agentGrouping, type AgentMutes, awaitsReply, buildAgentGroups, hostsOf, isQuiet, justFinished, replyAsk, sessionHost, splitAgentSessions } from "./agent-groups";
import { byAttention } from "./format";
import type { AgentState, Machine, Session } from "./types";

const machines: Machine[] = [
  { id: "machine-b", name: "电脑 B", online: true },
  { id: "machine-a", name: "电脑 A", online: true },
  { id: "machine-off", name: "离线电脑", online: false },
];
const date = (hour: number) => `2026-01-01T${String(hour).padStart(2, "0")}:00:00Z`;
const session = (id: string, state: AgentState, patch: Partial<Session> = {}): Session => ({
  machine_id: "machine-a", session_id: id, title: id, cwd: "/tmp/project", status: state, state, updated_at: date(1), ...patch,
});
const ids = (sessions: Session[]) => sessions.map((s) => s.session_id);
const mutes: AgentMutes = { spaces: ["machine-a/项目"], sessions: ["machine-a/muted-working", "machine-a/muted-needs", "machine-a/muted-idle", "machine-a/muted-waiting"], allowed: ["machine-a/allowed"] };

describe("Agent 分组偏好", () => {
  it("没选过的默认按待办，选过的（包括按机器）保持原样，并兼容未知的偏好", () => {
    expect(agentGrouping(null)).toBe("todo");
    expect(agentGrouping("invalid")).toBe("todo");
    for (const value of ["repo", "space", "state", "machine", "todo"]) expect(agentGrouping(value)).toBe(value);
  });
});

describe("By status", () => {
  it("keeps waiting and muted rows under their phase and folds Idle", () => {
    const all = [session("idle", "idle"), session("working", "working"), session("unread", "unread"), session("needs", "needs_you"),
      session("waiting", "unknown", { loop: { kind: "cron" } }), session("unknown", "unknown"), session("muted-working", "working")];
    const groups = buildAgentGroups(all, machines, "state", mutes);
    expect(groups.map((g) => g.state)).toEqual(["needs_you", "working", "done", "idle"]);
    expect(ids(groups[3].sessions)).toEqual(["idle", "unknown", "waiting"]);
    expect(groups[3].folded).toBe(true);
    expect(groups.slice(0, 3).every((g) => !g.folded)).toBe(true);
  });

  it("按时间降序，同时间按 ID，工作中使用启动时间且可回退更新时间", () => {
    const all = [session("a", "working", { state_since: date(2), updated_at: date(8) }), session("b", "working", { state_since: date(3), updated_at: date(4) }),
      session("c", "working", { updated_at: date(3) }), session("old", "working", { updated_at: undefined })];
    expect(ids(buildAgentGroups(all, machines, "state")[0].sessions)).toEqual(["b", "c", "a", "old"]);
    expect(ids(buildAgentGroups(all.map((s) => ({ ...s, updated_at: s.state_since ? date(20) : s.updated_at })), machines, "state")[0].sessions)).toEqual(["b", "c", "a", "old"]);
    expect(ids(all.slice().sort(byAttention))).toEqual(["b", "c", "a", "old"]);
  });

  it("只显示非空状态组", () => {
    expect(buildAgentGroups([], machines, "state")).toEqual([]);
    expect(buildAgentGroups([session("working", "working")], machines, "state").map((g) => g.name)).toEqual(["工作中"]);
  });
});

describe("所有分组共用 live 与最近的边界", () => {
  it("助手不混入 Agents，离线或未知机器的会话只进入最近", () => {
    const all = [session("live", "unknown"), session("assistant", "working", { assistant: true }), session("offline", "offline"),
      session("machine-offline", "working", { machine_id: "machine-off" }), session("unknown-machine", "working", { machine_id: "unknown" })];
    const lists = splitAgentSessions(all, machines);
    expect(ids(lists.live)).toEqual(["live"]);
    expect(ids(lists.recent)).toEqual(["offline", "machine-offline", "unknown-machine"]);
    expect(ids(buildAgentGroups(all, machines, "state").flatMap((g) => g.sessions))).toEqual(["live"]);
  });

  it("最近按更新时间倒序并限制 40 条，不修改输入数据", () => {
    const all = Array.from({ length: 50 }, (_, i) => session(String(i).padStart(2, "0"), "offline", { updated_at: date(i % 24) }));
    const before = ids(all);
    const { recent } = splitAgentSessions(all, machines);
    expect(recent).toHaveLength(40);
    expect(recent[0].updated_at).toBe(date(23));
    expect(ids(all)).toEqual(before);
  });
});

describe("sessionHost", () => {
  it("names the host from the pane ref; a bare id is herdr's", () => {
    const s = (pane_id?: string) => ({ machine_id: "m", session_id: "s", status: "idle", pane_id }) as never;
    expect(sessionHost(s("w1:p2"))).toBe("herdr");
    expect(sessionHost(s("orca:t1:l2"))).toBe("orca");
    expect(sessionHost(s("p2"))).toBe("herdr");
    expect(sessionHost(s("botmux:abc"))).toBe("botmux");
    expect(sessionHost(s("tern:42"))).toBe("tern");
    expect(sessionHost(s("rex:block:b1"))).toBe("rex");
    expect(sessionHost(s())).toBe("");
    expect([...hostsOf([s("p1"), s("botmux:a"), s("botmux:b"), s()])].sort()).toEqual(["botmux", "herdr"]);
    expect([...hostsOf([s("p1"), s("tern:7"), s("tern:8")])].sort()).toEqual(["herdr", "tern"]);
    expect([...hostsOf([s("p1"), s("rex:block:b1")])].sort()).toEqual(["herdr", "rex"]);
  });
});

describe("待你回复（#5096）", () => {
  const asking = (id: string, patch: Partial<Session> = {}) => session(id, "idle", { awaits_reply: true, asks: "要不要推到 beta？", ...patch });

  it("已看 + awaits_reply、没屏蔽才算；完成未看、屏蔽的、没标的都不算", () => {
    expect(awaitsReply(asking("a"))).toBe(true);
    expect(awaitsReply(asking("a"), mutes)).toBe(true);
    expect(awaitsReply(session("u", "unread", { awaits_reply: true }))).toBe(true);
    expect(awaitsReply(asking("muted-idle"), mutes)).toBe(false);
    expect(awaitsReply(asking("in-space", { space_name: "项目" }), mutes)).toBe(false);
    expect(awaitsReply(session("plain", "idle"))).toBe(false);
  });

  it("不算安静（不收进空闲的空间），屏蔽的照旧算安静", () => {
    expect(isQuiet(asking("a"))).toBe(false);
    expect(isQuiet(session("plain", "idle"))).toBe(true);
    expect(isQuiet(asking("muted-idle"), mutes)).toBe(true);
  });

  it("groups a finished question in Done and sorts it after unseen replies", () => {
    const all = [session("idle", "idle"), asking("reply"), session("working", "working"), session("unread", "unread"), session("needs", "needs_you")];
    const groups = buildAgentGroups(all, machines, "state");
    expect(ids(groups.find((g) => g.state === "done")!.sessions)).toEqual(["unread", "reply"]);
    expect(ids(all.sort(byAttention))).toEqual(["needs", "working", "unread", "reply", "idle"]);
  });

  it("问的那句：分诊说在等你（blocked·user）时用它的 line，否则用 asks", () => {
    expect(replyAsk(asking("a"))).toBe("要不要推到 beta？");
    expect(replyAsk(asking("a", { work: { phase: "blocked", on: "user", line: "等你定 A 还是 B" } }))).toBe("等你定 A 还是 B");
    expect(replyAsk(asking("a", { work: { phase: "blocked", on: "external", line: "在等 CI" } }))).toBe("要不要推到 beta？");
    expect(replyAsk(session("plain", "idle"))).toBe("");
  });
});

describe("Just finished (#9751)", () => {
  const finished = (id: string, patch: Partial<Session> = {}) => session(id, "idle", { work: { phase: "done", line: "Merged", just_finished: true }, ...patch });

  it("is a seen agent the hub marks just finished, unless muted", () => {
    expect(justFinished(finished("a"))).toBe(true);
    expect(justFinished(finished("a"), mutes)).toBe(true);
    expect(justFinished(session("u", "unread", { work: { phase: "done", just_finished: true } }))).toBe(false);
    expect(justFinished(finished("muted-idle"), mutes)).toBe(false);
    expect(justFinished(finished("in-space", { space_name: "项目" }), mutes)).toBe(false);
    expect(justFinished(session("done", "idle", { work: { phase: "done", line: "Merged" } }))).toBe(false);
  });

  it("is not quiet, so it never folds into Idle spaces; a muted one still is", () => {
    expect(isQuiet(finished("a"))).toBe(false);
    expect(isQuiet(finished("muted-idle"), mutes)).toBe(true);
  });

  it("places a just-finished row under Done", () => {
    const groups = buildAgentGroups([finished("loop", { loop: { kind: "cron" } })], machines, "state");
    expect(groups.map((g) => g.key)).toEqual(["state:done"]);
  });

  it("ranks after Working and before the seen ones (byAttention)", () => {
    const all = [session("idle", "idle", { updated_at: date(9) }), finished("done"), session("working", "working"), session("unread", "unread")];
    expect(ids(all.sort(byAttention))).toEqual(["working", "unread", "done", "idle"]);
  });
});

describe("waiting in the background (#6045)", () => {
  it("keeps background waits under Working after active turns", () => {
    const all = [session("busy", "working"), session("sleeping", "working", { background_waiting: true }), session("woken", "idle", { loop: { prompt: "check" } as Session["loop"] })];
    const groups = buildAgentGroups(all, machines, "state", { spaces: [], sessions: [] });
    expect(groups.map((g) => [g.key, ids(g.sessions)])).toEqual([["state:working", ["busy", "sleeping"]], ["state:idle", ["woken"]]]);

  });
});
