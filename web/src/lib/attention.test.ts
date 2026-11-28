import { describe, expect, it } from "vitest";
import { answeredTags, badgeText, BATCH_MAX_MS, BATCH_QUIET_MS, batchDue, cardArrivalKey, finishedBatch, detectNewCards, FORGET_CARD_MS, detectTransitions, heldOnlyNeeds, isHeldLevel, MIN_GAP_MS, pickEvent, rateLimiter, SETTLE_MS, suppressed, tabTitle, trackTurns } from "./attention";
import { stateCounts } from "./sessions";
import { agentKey } from "./pins";
import type { AgentMutes } from "./agent-groups";
import type { AgentState, Interaction, Session } from "./types";

const session = (id: string, state: AgentState, patch: Partial<Session> = {}): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/tmp/p", status: state, state, updated_at: "2026-01-01T10:00:00Z", ...patch,
});
const noMutes: AgentMutes = { spaces: [], sessions: [], allowed: [] };
const prevOf = (...pairs: [string, AgentState][]) => new Map(pairs.map(([id, st]) => [`m/${id}`, st] as const));

describe("标签页标题", () => {
  it("需要你的数量放在前面，页面名保留在后面", () => {
    expect(tabTitle("设置", 3)).toBe("(3) 设置 · Persona");
    expect(tabTitle("Persona", 2)).toBe("(2) Persona");
  });
  it("0 个时不加前缀，和原来的标题一样", () => {
    expect(tabTitle("设置", 0)).toBe("设置 · Persona");
    expect(tabTitle("Persona", 0)).toBe("Persona");
    expect(tabTitle("", 0)).toBe("Persona");
  });
});

describe("需要你的数量", () => {
  it("用的是侧边栏角标的同一个数：安静任务的不计", () => {
    const all = [session("a", "needs_you"), session("b", "needs_you"), session("q", "needs_you", { quiet: true }), session("w", "working"), session("u", "unread")];
    expect(stateCounts(all).badge).toBe(2);
  });
});

describe("图标角标文字", () => {
  it("0 不画，1-9 写数字，更多写 9+", () => {
    expect(badgeText(0)).toBe("");
    expect(badgeText(1)).toBe("1");
    expect(badgeText(9)).toBe("9");
    expect(badgeText(10)).toBe("9+");
    expect(badgeText(120)).toBe("9+");
  });
});

describe("状态变化检测", () => {
  it("变成需要你时响，不管之前是什么状态", () => {
    for (const was of ["working", "idle", "unread"] as const) {
      const { events } = detectTransitions(prevOf(["a", was]), [session("a", "needs_you")], noMutes);
      expect(events).toEqual([{ kind: "needs", key: "m/a" }]);
    }
  });
  it("一直需要你不重复响", () => {
    expect(detectTransitions(prevOf(["a", "needs_you"]), [session("a", "needs_you")], noMutes).events).toEqual([]);
  });
  it("工作中 → 完成未看算完成", () => {
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "unread")], noMutes).events).toEqual([{ kind: "finished", key: "m/a" }]);
  });
  it("工作中 → 已看 不算（hub 也不推：看过了、或 hub 分诊为做完了）", () => {
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "idle")], noMutes).events).toEqual([]);
  });
  it("工作中 → 未运行 / 未知 不算；别的状态到完成未看也不算", () => {
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "offline")], noMutes).events).toEqual([]);
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "unknown")], noMutes).events).toEqual([]);
    expect(detectTransitions(prevOf(["a", "idle"]), [session("a", "unread")], noMutes).events).toEqual([]);
  });
  it("第一次看到的会话（刚打开页面、新会话）不响", () => {
    expect(detectTransitions(new Map(), [session("a", "needs_you"), session("b", "unread")], noMutes).events).toEqual([]);
  });
  it("被屏蔽通知的会话完成不响，但需要你照响（和筛选一致）", () => {
    const muted: AgentMutes = { spaces: [], sessions: ["m/a"], allowed: [] };
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "unread")], muted).events).toEqual([]);
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "needs_you")], muted).events).toEqual([{ kind: "needs", key: "m/a" }]);
  });
  it("安静任务的会话都不响（侧边栏也不计）", () => {
    expect(detectTransitions(prevOf(["a", "working"]), [session("a", "unread", { quiet: true })], noMutes).events).toEqual([]);
    expect(detectTransitions(prevOf(["a", "idle"]), [session("a", "needs_you", { quiet: true })], noMutes).events).toEqual([]);
  });
  it("返回新的状态表，下一次拿它比较", () => {
    const first = detectTransitions(prevOf(["a", "working"]), [session("a", "unread"), session("b", "working")], noMutes);
    expect(first.next.get("m/b")).toBe("working");
    expect(detectTransitions(first.next, [session("a", "unread"), session("b", "working")], noMutes).events).toEqual([]);
  });
  it("一批里需要你优先于完成", () => {
    const { events } = detectTransitions(prevOf(["a", "working"], ["b", "idle"]), [session("a", "unread"), session("b", "needs_you")], noMutes);
    expect(events).toHaveLength(2);
    expect(pickEvent(events)).toEqual({ kind: "needs", key: "m/b" });
    expect(pickEvent([])).toBeUndefined();
  });
});

describe("正在看的会话不响", () => {
  const a = agentKey({ machine_id: "m", session_id: "a" });
  it("窗口在前台且就是当前页的会话：不响", () => {
    expect(suppressed(a, { key: a, focused: true })).toBe(true);
  });
  it("窗口不在前台（切到别的标签页或应用）：响", () => {
    expect(suppressed(a, { key: a, focused: false })).toBe(false);
  });
  it("别的会话、或不在会话页：响", () => {
    expect(suppressed(a, { key: "m/b", focused: true })).toBe(false);
    expect(suppressed(a, { focused: true })).toBe(false);
  });
});

describe("限频", () => {
  it("3 秒内最多一声，过了又可以", () => {
    const allow = rateLimiter();
    expect(allow(1000)).toBe(true);
    expect(allow(1000 + MIN_GAP_MS - 1)).toBe(false);
    expect(allow(1000 + MIN_GAP_MS)).toBe(true);
  });
  it("被拦下的不重新计时", () => {
    const allow = rateLimiter(3000);
    allow(0);
    expect(allow(2000)).toBe(false);
    expect(allow(3000)).toBe(true);
  });
});

describe("轮次时长（只推长任务）", () => {
  const T0 = Date.parse("2026-01-01T10:00:00Z");
  it("从 hub 的 state_since 算起，做完时记下结束时间", () => {
    let turns = trackTurns(new Map(), [session("a", "working", { state_since: "2026-01-01T09:58:00Z" })], T0);
    expect(turns.get("m/a")).toEqual({ start: T0 - 120_000 });
    turns = trackTurns(turns, [session("a", "unread")], T0 + 5000);
    expect(turns.get("m/a")).toEqual({ start: T0 - 120_000, endedAt: T0 + 5000 });
  });
  it("刚停又接着干的是同一轮；停久了再干是新的一轮", () => {
    let turns = trackTurns(new Map(), [session("a", "working")], T0);
    turns = trackTurns(turns, [session("a", "unread")], T0 + 1000);
    turns = trackTurns(turns, [session("a", "working")], T0 + 1000 + SETTLE_MS - 1);
    expect(turns.get("m/a")).toEqual({ start: T0 });
    turns = trackTurns(turns, [session("a", "unread")], T0 + 10_000);
    turns = trackTurns(turns, [session("a", "working", { state_since: undefined })], T0 + 60_000);
    expect(turns.get("m/a")).toEqual({ start: T0 + 60_000 });
  });
  it("没在干活、也没见过它干活的会话没有轮次", () => {
    expect(trackTurns(new Map(), [session("a", "unread")], T0).has("m/a")).toBe(false);
  });
});

describe("处理过的通知收回（#3458）", () => {
  const snap = (...pairs: [string, AgentState][]) => new Map(pairs);
  it("离开需要你、离开完成未看、从列表里没了的都收回", async () => {
    const { settledTags } = await import("./attention");
    const prev = snap(["m/a", "needs_you"], ["m/b", "unread"], ["m/c", "needs_you"], ["m/d", "working"]);
    const next = snap(["m/a", "working"], ["m/b", "idle"], ["m/d", "unread"]);
    expect(settledTags(prev, next)).toEqual(["m/a#needs", "m/b#finished", "m/c#needs"]);
  });
  it("没变的不收回", async () => {
    const { settledTags } = await import("./attention");
    const s = snap(["m/a", "needs_you"], ["m/b", "unread"]);
    expect(settledTags(s, s)).toEqual([]);
  });
});

describe("已在需要你时又来一张卡（#4058）", () => {
  const card = (id: string, sid: string, at = "2026-01-01T10:00:00Z"): Interaction => ({ id, machine_id: "m", session_id: sid, kind: "permission", created_at: at });
  const waiting = prevOf(["a", "needs_you"], ["b", "needs_you"]);
  const sessions = [session("a", "needs_you"), session("b", "needs_you")];
  const T = 1_000_000;
  const none = new Set<string>();
  const seen = (ids: string[]) => new Map(ids.map((id) => [cardArrivalKey(card(id, "a")), T - 1000] as const));

  it("第一次拿到的卡片只作基线，不算新卡", () => {
    const { ids, events } = detectNewCards(undefined, [card("1", "a")], waiting, waiting, sessions, none, T);
    expect(events).toEqual([]);
    expect([...ids.keys()]).toEqual([cardArrivalKey(card("1", "a"))]);
  });
  it("会话一直在需要你，又来一张没见过的卡：算一次", () => {
    const { events } = detectNewCards(seen(["1"]), [card("1", "a"), card("2", "a")], waiting, waiting, sessions, none, T);
    expect(events).toEqual([{ kind: "needs", key: "m/a", card: card("2", "a") }]);
  });
  it("同一张卡再来一次（别的字段变了、快照重拉）不算", () => {
    expect(detectNewCards(seen(["1", "2"]), [card("2", "a"), card("1", "a")], waiting, waiting, sessions, none, T).events).toEqual([]);
  });
  it("卡片被答掉（变少）不算", () => {
    expect(detectNewCards(seen(["1", "2"]), [card("2", "a")], waiting, waiting, sessions, none, T).events).toEqual([]);
  });
  it("会话刚变成需要你时不算：那一次由状态变化发，不响两遍", () => {
    const was = prevOf(["a", "working"]);
    expect(detectNewCards(seen([]), [card("1", "a")], was, prevOf(["a", "needs_you"]), [session("a", "needs_you")], none, T).events).toEqual([]);
  });
  it("卡来了但会话已经不在需要你，或还没在需要你：不算", () => {
    expect(detectNewCards(seen([]), [card("1", "a")], waiting, prevOf(["a", "working"]), [session("a", "working")], none, T).events).toEqual([]);
    expect(detectNewCards(seen([]), [card("1", "a")], prevOf(["a", "working"]), prevOf(["a", "working"]), [session("a", "working")], none, T).events).toEqual([]);
  });
  it("安静任务的会话不算（侧边栏也不计）", () => {
    expect(detectNewCards(seen([]), [card("1", "q")], prevOf(["q", "needs_you"]), prevOf(["q", "needs_you"]), [session("q", "needs_you", { quiet: true })], none, T).events).toEqual([]);
  });
  it("一个会话一次来几张只算一次，说最早的那张；不同会话各算各的", () => {
    const { events } = detectNewCards(seen([]), [card("3", "a", "2026-01-01T10:00:05Z"), card("2", "a", "2026-01-01T10:00:01Z"), card("4", "b")], waiting, waiting, sessions, none, T);
    expect(events.map((e) => [e.key, e.card.id])).toEqual([["m/a", "2"], ["m/b", "4"]]);
  });
  it("通知说的那张卡没了就收回，会话还在等别的卡也一样", () => {
    const announced = new Map([["m/a", "1"], ["m/b", "4"]]);
    expect(answeredTags(announced, [card("2", "a"), card("4", "b")])).toEqual(["m/a#needs"]);
    expect(answeredTags(announced, [card("1", "a"), card("4", "b")])).toEqual([]);
  });
  it("需要你先到、卡后到：这张卡算同一次等你（follows），不另响一声", () => {
    const { events } = detectNewCards(seen([]), [card("1", "a")], waiting, waiting, sessions, new Set(["m/a"]), T);
    expect(events).toEqual([{ kind: "needs", key: "m/a", card: card("1", "a"), follows: true }]);
  });
  it("同 ID 的新 occurrence 是新卡，不被十分钟的旧卡记忆压住", () => {
    const first = card("1", "a");
    const { ids } = detectNewCards(undefined, [first], waiting, waiting, sessions, none, T);
    const replacement = card("1", "a", "2026-01-01T10:00:00.000000001Z");
    expect(detectNewCards(ids, [replacement], waiting, waiting, sessions, none, T + 1000).events)
      .toEqual([{ kind: "needs", key: "m/a", card: replacement }]);
  });
  it("卡短暂消失又回来（重连、重读屏幕）不算新卡；很久以前见过的才忘掉", () => {
    const { ids } = detectNewCards(seen(["1"]), [], waiting, waiting, sessions, none, T);
    expect(detectNewCards(ids, [card("1", "a")], waiting, waiting, sessions, none, T + 5000).events).toEqual([]);
    const later = T + FORGET_CARD_MS;
    const { ids: old } = detectNewCards(seen(["1"]), [], waiting, waiting, sessions, none, later);
    expect(detectNewCards(old, [card("1", "a")], waiting, waiting, sessions, none, later).events).toHaveLength(1);
  });
});

describe("助手还在看的问题先不响（#544）", () => {
  const ask = (id: string, sid: string, patch: Partial<Interaction> = {}): Interaction => ({ id, machine_id: "m", session_id: sid, kind: "question", created_at: "2026-01-01T10:00:00Z", ...patch });
  const perm = (id: string, sid: string): Interaction => ({ id, machine_id: "m", session_id: sid, kind: "permission", created_at: "2026-01-01T09:00:00Z" });
  const looking = (prev: Map<string, AgentState>) => detectTransitions(prev, [session("a", "needs_you")], noMutes, [ask("1", "a", { delegating: true })]);

  it("会话只因助手在看的问题而需要你：先不算，助手交给你时再算一次", () => {
    const first = looking(prevOf(["a", "working"]));
    expect(first.events).toEqual([]);
    expect(first.next.get("m/a")).toBe("working");
    expect(detectTransitions(first.next, [session("a", "needs_you")], noMutes, [ask("1", "a")]).events).toEqual([{ kind: "needs", key: "m/a" }]);
  });
  it("助手代答了：从头到尾不响", () => {
    const first = looking(prevOf(["a", "working"]));
    expect(detectTransitions(first.next, [session("a", "working")], noMutes, []).events).toEqual([]);
  });
  it("打开页面时助手正在看，之后交给你：也算", () => {
    const first = looking(new Map());
    expect(first.events).toEqual([]);
    expect(detectTransitions(first.next, [session("a", "needs_you")], noMutes, [ask("1", "a")]).events).toEqual([{ kind: "needs", key: "m/a" }]);
  });
  it("会话还有别的卡在等你：照常算", () => {
    const { events } = detectTransitions(prevOf(["a", "working"]), [session("a", "needs_you")], noMutes, [perm("p", "a"), ask("1", "a", { delegating: true })]);
    expect(events).toEqual([{ kind: "needs", key: "m/a" }]);
  });
  it("已在需要你时来一张助手在看的卡：先不算；同一张卡交给你时算新卡", () => {
    const waiting = prevOf(["a", "needs_you"]);
    const sessions = [session("a", "needs_you")];
    const T = 1_000_000;
    const first = detectNewCards(new Map([[cardArrivalKey(perm("p", "a")), T - 1000]]), [perm("p", "a"), ask("1", "a", { delegating: true })], waiting, waiting, sessions, new Set(), T);
    expect(first.events).toEqual([]);
    const back = detectNewCards(first.ids, [perm("p", "a"), ask("1", "a")], waiting, waiting, sessions, new Set(), T + 1000);
    expect(back.events).toEqual([{ kind: "needs", key: "m/a", card: ask("1", "a") }]);
  });
});

describe("一批做完的怎么显示（#4945）", () => {
  const unread = new Map([["m/a", "A"], ["m/b", "B"], ["m/c", "C"], ["m/d", "D"]]);
  it("一个都不剩：不发", () => {
    expect(finishedBatch([], unread)).toEqual({ kind: "none" });
    expect(finishedBatch(["m/x", "m/y"], unread)).toEqual({ kind: "none" });
  });
  it("只剩一个：走原来的单条", () => {
    expect(finishedBatch(["m/a"], unread)).toEqual({ kind: "one", key: "m/a" });
    expect(finishedBatch(["m/x", "m/b", "m/b"], unread)).toEqual({ kind: "one", key: "m/b" });
  });
  it("多个：合成一条，按做完的顺序列名字，重复的只算一次", () => {
    expect(finishedBatch(["m/c", "m/a", "m/c"], unread)).toEqual({ kind: "many", keys: ["m/c", "m/a"], names: ["C", "A"], more: false });
  });
  it("名字最多列三个，多的记一个「等」", () => {
    expect(finishedBatch(["m/a", "m/b", "m/c"], unread)).toMatchObject({ names: ["A", "B", "C"], more: false });
    expect(finishedBatch(["m/a", "m/b", "m/c", "m/d"], unread)).toMatchObject({ keys: ["m/a", "m/b", "m/c", "m/d"], names: ["A", "B", "C"], more: true });
  });
  it("安静一小段就发，最多等一小段", () => {
    expect(batchDue(0, 0)).toBe(BATCH_QUIET_MS);
    expect(batchDue(0, 1000)).toBe(1000 + BATCH_QUIET_MS);
    expect(batchDue(0, BATCH_MAX_MS)).toBe(BATCH_MAX_MS);
  });
});

it("does not notify about a directory-hidden question or completion", () => {
  const r = detectTransitions(prevOf(["a", "working"], ["b", "working"]), [session("a", "needs_you", { hidden_by_directory: true }), session("b", "unread", { hidden_by_directory: true })], noMutes);
  expect(r.events).toEqual([]);
  expect(r.next.size).toBe(0);
});

describe("push grading levels (#11771)", () => {
  const p0: Interaction = { id: "p0", machine_id: "m", session_id: "a", kind: "question" };
  const p1: Interaction = { id: "p1", machine_id: "m", session_id: "a", kind: "permission", push_level: "P1" };
  const p2: Interaction = { id: "p2", machine_id: "m", session_id: "a", kind: "question", push_level: "P2" };

  it("classifies P1/P2-only needs_you sessions as held and treats P0/mixed/cardless as immediate", () => {
    expect(isHeldLevel("P1")).toBe(true);
    expect(isHeldLevel("P2")).toBe(true);
    expect(isHeldLevel("P0")).toBe(false);
    expect(isHeldLevel(undefined)).toBe(false);

    const a = session("a", "needs_you");
    expect(heldOnlyNeeds(a, [])).toBe(false);
    expect(heldOnlyNeeds(a, [p1])).toBe(true);
    expect(heldOnlyNeeds(a, [p1, p2])).toBe(true);
    expect(heldOnlyNeeds(a, [p1, p0])).toBe(false);
    expect(heldOnlyNeeds(a, [p1, { ...p0, delegating: true }])).toBe(true);
  });

  it("suppresses P1 needs and finished (P2) transitions when grading is on, preserving off-mode behavior", () => {
    const base = prevOf(["a", "working"], ["b", "working"], ["c", "working"]);
    const next = [session("a", "needs_you"), session("b", "needs_you"), session("c", "unread")];
    const cards: Interaction[] = [p1, { id: "b0", machine_id: "m", session_id: "b", kind: "question" }];

    expect(detectTransitions(base, next, noMutes, cards, false).events).toEqual([
      { kind: "needs", key: "m/a" },
      { kind: "needs", key: "m/b" },
      { kind: "finished", key: "m/c" },
    ]);
    expect(detectTransitions(base, next, noMutes, cards, true).events).toEqual([
      { kind: "needs", key: "m/b" },
    ]);
  });

  it("ignores new P1/P2 cards when grading is on while still alerting on new P0 cards", () => {
    const waiting = prevOf(["a", "needs_you"]);
    const known = new Map([["old", 100]]);
    const rOn = detectNewCards(known, [{ id: "old", machine_id: "m", session_id: "a", kind: "question", push_level: "P1" }, p1, p2, p0], waiting, waiting, [session("a", "needs_you")], new Set(), 200, true);
    expect(rOn.events).toEqual([{ kind: "needs", key: "m/a", card: p0 }]);

    const rOff = detectNewCards(known, [{ id: "old", machine_id: "m", session_id: "a", kind: "question" }, p1], waiting, waiting, [session("a", "needs_you")], new Set(), 200, false);
    expect(rOff.events).toEqual([{ kind: "needs", key: "m/a", card: p1 }]);
  });
});
