import { describe, expect, it } from "vitest";
import { applyFilter, arrangeSpaces, isBotmux, withoutBotmux, filterCounts, matchesFilter, matchingKeys, shownFilters, agentFilter, unreadOf, type SpaceLike } from "./agent-filter";
import { isQuiet, type AgentMutes } from "./agent-groups";
import { NO_PINS } from "./pins";
import type { AgentState, Session } from "./types";

const date = (min: number) => `2026-01-01T10:${String(min).padStart(2, "0")}:00Z`;
const session = (id: string, state: AgentState, patch: Partial<Session> = {}): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/tmp/p", status: state, state, updated_at: date(1), ...patch,
});
const ids = (list: Session[]) => list.map((s) => s.session_id);
const none: AgentMutes = { spaces: [], sessions: [], allowed: [] };
const muted: AgentMutes = { spaces: ["m/安静"], sessions: ["m/muted-unread", "m/muted-needs", "m/muted-working"], allowed: [] };

describe("快速筛选（iOS AgentFilter）", () => {
  it("默认全部，未知的值也是全部", () => {
    expect(agentFilter(null)).toBe("all");
    expect(agentFilter("x")).toBe("all");
    expect(agentFilter("unread")).toBe("unread");
  });

  it("屏蔽的需要你仍算，屏蔽的未读 / 工作中不算", () => {
    const all = [session("needs", "needs_you"), session("muted-needs", "needs_you"), session("unread", "unread"), session("muted-unread", "unread"),
      session("working", "working"), session("muted-working", "working"), session("space-muted", "working", { space_name: "安静" }), session("idle", "idle")];
    expect(filterCounts(all, muted)).toEqual({ needs: 2, unread: 1, working: 1, bg_waiting: 0, feishu: 0, terminal: 0 });
    expect(all.filter((s) => matchesFilter(s, "all", muted))).toHaveLength(all.length);
    expect(ids(all.filter((s) => matchesFilter(s, "working", muted)))).toEqual(["working"]);
  });

  it("只露出有 Agent 的状态，选中的空了也留着", () => {
    expect(shownFilters("all", { needs: 0, unread: 2, working: 0, bg_waiting: 0, feishu: 0, terminal: 0 })).toEqual(["all", "unread"]);
    expect(shownFilters("needs", { needs: 0, unread: 0, working: 1, bg_waiting: 0, feishu: 0, terminal: 0 })).toEqual(["all", "needs", "working"]);
    expect(shownFilters("all", { needs: 0, unread: 0, working: 0, bg_waiting: 0, feishu: 0, terminal: 0 })).toEqual(["all"]);
  });

  it("waiting in the background has its own chip and leaves Working (#6045)", () => {
    const all = [session("busy", "working"), session("sleeping", "working", { background_waiting: true }), session("muted-sleeping", "working", { background_waiting: true }),
      session("idle-flag", "idle", { background_waiting: true })];
    const mutes: AgentMutes = { ...muted, sessions: [...muted.sessions, "m/muted-sleeping"] };
    expect(filterCounts(all, mutes)).toEqual({ needs: 0, unread: 0, working: 1, bg_waiting: 1, feishu: 0, terminal: 0 });
    expect(ids(all.filter((s) => matchesFilter(s, "working", mutes)))).toEqual(["busy"]);
    expect(ids(all.filter((s) => matchesFilter(s, "bg_waiting", mutes)))).toEqual(["sleeping"]);
    expect(shownFilters("all", filterCounts(all, mutes))).toEqual(["all", "working", "bg_waiting"]);
  });

  it("选中时显示的留到再选一次：点开的未读变成已看，位置不变", () => {
    const before = [session("a", "unread"), session("b", "unread"), session("c", "working")];
    const kept = matchingKeys(before, "unread", none);
    expect([...kept]).toEqual(["m/a", "m/b"]);
    const after = [session("a", "idle"), session("b", "unread"), session("c", "working"), session("d", "unread")];
    expect(ids(applyFilter(after, "unread", none, kept))).toEqual(["a", "b", "d"]);
    expect(ids(applyFilter(after, "unread", none, new Set()))).toEqual(["b", "d"]);
    expect(applyFilter(after, "all", none, new Set())).toBe(after);
    expect(matchingKeys(after, "all", none).size).toBe(0);
  });

  it("counts exactly the Agents listed by In a tab when tab associations change", () => {
    const herdr = session("herdr", "idle", { pane_id: "w1:p1" });
    const orca = session("orca", "working", { pane_id: "orca:w2:p1" });
    const desktop = session("desktop", "idle", { space_kind: "codex_app", pane_id: "w1:p2" });
    const lark = session("lark", "idle", { pane_id: "botmux:group/topic" });
    const unbound = session("unbound", "working");
    const before = [herdr, orca, desktop, lark, unbound];
    const kept = matchingKeys(before, "terminal", none);
    expect(ids(applyFilter(before, "terminal", none, kept))).toEqual(["herdr", "orca"]);
    expect(filterCounts(before, none).terminal).toBe(2);

    const after = [{ ...herdr, pane_id: undefined }, { ...orca, state: "offline" as const }, desktop, lark, unbound,
      session("new", "needs_you", { pane_id: "w1:p3" })];
    const shown = applyFilter(after, "terminal", none, kept);
    expect(ids(shown)).toEqual(["new"]);
    expect(filterCounts(after, none).terminal).toBe(shown.length);
  });

  it("全部标为已读只标完成未看的", () => {
    expect(ids(unreadOf([session("a", "unread"), session("b", "idle"), session("c", "needs_you"), session("d", "unread")]))).toEqual(["a", "d"]);
  });

  it("Mark all as seen marks the Just finished ones too (#9751)", () => {
    const finished = { ...session("e", "idle"), work: { phase: "done" as const, just_finished: true } };
    const done = { ...session("f", "idle"), work: { phase: "done" as const } };
    expect(ids(unreadOf([session("a", "unread"), finished, done]))).toEqual(["a", "e"]);
  });
});

describe("待你回复的空间不收进「空闲的空间」（#5096）", () => {
  const sp = (name: string, sessions: Session[]): SpaceLike => ({ key: `m/${name}`, machineId: "m", name, sessions });
  it("只有一个已看但在等你回复的 Agent 的空间留在上面，屏蔽的照旧折起来", () => {
    const asking = sp("问你", [session("q1", "idle", { awaits_reply: true, asks: "要不要推到 beta？" })]);
    const quiet = sp("安静", [session("i1", "idle")]);
    const mutedAsk = sp("屏蔽", [session("m1", "idle", { awaits_reply: true, space_name: "屏蔽" })]);
    const mutes: AgentMutes = { spaces: ["m/屏蔽"], sessions: [] };
    const busy = (x: SpaceLike) => x.sessions.some((s) => !isQuiet(s, mutes));
    const r = arrangeSpaces([quiet, asking, mutedAsk], { pins: NO_PINS, mutes, filtered: false, busy });
    expect(r.active.map((x) => x.name)).toEqual(["问你"]);
    expect(r.idle.map((x) => x.name)).toEqual(["安静", "屏蔽"]);
  });
});

describe("空间的顺序", () => {
  const sp = (name: string, sessions: Session[]): SpaceLike => ({ key: `m/${name}`, machineId: "m", name, sessions });
  const busy = (x: SpaceLike) => x.sessions.some((s) => s.state !== "idle");
  const a = sp("a", [session("a1", "idle", { updated_at: date(5) })]);
  const b = sp("b", [session("b1", "working", { state_since: date(2), updated_at: date(9) })]);
  const c = sp("c", [session("c1", "unread", { updated_at: date(7) })]);
  const d = sp("安静", [session("d1", "working", { updated_at: date(8) })]);

  it("置顶的空间在最前（屏蔽的不算），然后是忙的、屏蔽的忙的，安静的折起来", () => {
    const r = arrangeSpaces([a, b, d, c], { pins: { spaces: ["m/a", "m/安静"], sessions: [] }, mutes: muted, filtered: false, busy });
    expect(r.active.map((x) => x.name)).toEqual(["a", "b", "c", "安静"]);
    expect(r.idle).toEqual([]);
    const r2 = arrangeSpaces([a, b, d, c], { pins: NO_PINS, mutes: none, filtered: false, busy });
    expect(r2.active.map((x) => x.name)).toEqual(["b", "安静", "c"]);
    expect(r2.idle.map((x) => x.name)).toEqual(["a"]);
  });

  it("有筛选时全部列出、保持原有顺序，不折叠", () => {
    const r = arrangeSpaces([a, b, d, c], { pins: NO_PINS, mutes: none, filtered: true, busy });
    // Activity still updates each row, but no longer moves its workspace (#11801).
    expect(r.active.map((x) => x.name)).toEqual(["a", "b", "安静", "c"]);
    expect(r.idle).toEqual([]);
  });

  it("retains a quieted active space but brings new attention out of Idle (#11801)", () => {
    const currentBusy = (x: SpaceLike) => x.sessions.some((s) => !isQuiet(s, none));
    const busyA = sp("a", [session("a1", "working")]);
    const quietB = sp("b", [session("b1", "idle")]);
    const quietC = sp("c", [session("c1", "idle")]);
    const first = arrangeSpaces([busyA, quietB, quietC], { pins: NO_PINS, mutes: none, filtered: false, busy: currentBusy });
    expect(first.active.map((x) => x.name)).toEqual(["a"]);
    expect(first.idle.map((x) => x.name)).toEqual(["b", "c"]);

    const quietA = sp("a", [session("a1", "idle", { updated_at: date(10) })]);
    const busyB = sp("b", [session("b1", "needs_you", { updated_at: date(11) })]);
    const finishedC = sp("c", [session("c1", "idle", { work: { phase: "done", just_finished: true } })]);
    const after = arrangeSpaces([quietA, busyB, finishedC], { pins: NO_PINS, mutes: none, filtered: false, busy: currentBusy, retainedActive: new Set(first.active.map((x) => x.key)) });
    expect(after.active.map((x) => x.name)).toEqual(["a", "b", "c"]);
    expect(after.idle).toEqual([]);
    expect(after.active[0]!.sessions[0]!.state).toBe("idle");
    expect(after.active[1]!.sessions[0]!.state).toBe("needs_you");
    expect(after.active[2]!.sessions[0]!.work?.just_finished).toBe(true);
  });
});

describe("筛选栏的「botmux」显示 / 隐藏（#2786）", () => {
  it("隐藏时去掉 botmux 会话，herdr、Orca 和没有窗格的留着；显示时原样", () => {
    const all = [session("b", "working", { pane_id: "botmux:oc_1/t1" }), session("h", "working", { pane_id: "w1:p1" }), session("o", "idle", { pane_id: "orca:x" }), session("r", "idle")];
    expect(all.filter(isBotmux).map((s) => s.session_id)).toEqual(["b"]);
    expect(ids(withoutBotmux(all, true))).toEqual(["h", "o", "r"]);
    expect(withoutBotmux(all, false)).toBe(all);
  });

  it("does not offer an empty Lark group filter even when previously selected", () => {
    expect(shownFilters("feishu", { needs: 0, unread: 0, working: 0, bg_waiting: 0, feishu: 0, terminal: 0 })).toEqual(["all"]);
  });
});
