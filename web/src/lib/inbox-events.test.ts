// Inbox events (docs/assistant/inbox.md "Events and handling chain"), driven by the fake hub's fixture (scripts/mock/inbox_events.json):
// one item per state. An item without this phase's fields stays where the tier put it.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./store", () => ({ onHubEvent: () => () => {} }));
vi.mock("../ui/atoms", () => ({ toast: vi.fn() }));

import { toast } from "../ui/atoms";
import { badgeCount, getInbox, reloadInbox, resetInboxForTests, sections, suggestionAct, TIERS, type InboxItem } from "./inbox";
import {
  actOnSuggestion, canExpand, chainRows, chainTap, chainTime, firstLine, handlingWord, hasEvent, itemAtUid, preparing, statusOf, suggestionButtons, suggestionTitle,
} from "./inbox-events";

const fixture = JSON.parse(readFileSync(join(__dirname, "../../../scripts/mock/inbox_events.json"), "utf8")) as { items: InboxItem[] };
const byId = (id: string) => fixture.items.find((x) => x.id === id)!;
const reply = byId("lark:oc_demo1:100");
const urgent = byId("lark:oc_demo2:200");
const waiting = byId("lark:oc_demo3:300");
const agentWorking = byId("mail:a1:ci:77");
const agentSuggestion = byId("mail:a1:contract:88");
const failed = byId("lark:oc_demo6:600");
const conclusion = byId("lark:oc_demo7:700");
const noise = byId("mail:a1:news:99");
/** The same item as an older hub lists it: none of this phase's fields. */
const legacy = (it: InboxItem): InboxItem => {
  const { status: _s, urgent: _u, recap: _r, handling: _h, suggestion: _g, chain: _c, related: _rel, ...rest } = it;
  return rest;
};
const all = { source: "", kind: "", query: "" };
const ids = (l: { id: string }[]) => l.map((x) => x.id);

describe("收件箱事件：分区", () => {
  it("puts 进行中 between 需要你 and 知悉即可", () => {
    expect(TIERS.map((x) => x.key)).toEqual(["action", "working", "fyi", "noise"]);
    expect(TIERS[1].title).toBe("进行中");
  });
  it("sections by the hub's status", () => {
    const s = sections(fixture.items, all);
    expect(ids(s.tiers.action).sort()).toEqual([reply.id, urgent.id, agentSuggestion.id, failed.id].sort());
    expect(ids(s.tiers.working).sort()).toEqual([waiting.id, agentWorking.id].sort());
    expect(ids(s.tiers.fyi)).toEqual([conclusion.id]);
    expect(ids(s.tiers.noise)).toEqual([noise.id]);
    expect(s.done).toHaveLength(2);
    // The navigation badge follows unread Needs you items, not every open section.
    expect(badgeCount(fixture.items)).toBe(s.tiers.action.filter((it) => !it.read).length);
    expect(badgeCount([urgent])).toBe(1);
    expect(badgeCount([waiting, agentWorking])).toBe(0);
  });
  it("falls back to the tier for an older hub (a working item stays under 需要你, empty tier = 需要你)", () => {
    const old = fixture.items.map(legacy);
    const s = sections(old, all);
    expect(s.tiers.working).toEqual([]);
    expect(ids(s.tiers.action)).toContain(waiting.id);
    expect(statusOf({ ...legacy(reply), tier: "" })).toBe("action");
    expect(statusOf({ ...legacy(reply), tier: "fyi" })).toBe("fyi");
    expect(old.some(hasEvent)).toBe(false);
    expect(fixture.items.every(hasEvent)).toBe(true);
  });
});

describe("收件箱事件：行上的标记", () => {
  it("says what is going on", () => {
    expect(handlingWord(urgent)).toBe("AI 正在看");
    expect(handlingWord(agentWorking)).toBe("Agent 在做");
    expect(handlingWord(waiting)).toBe("等对方回复");
    expect(handlingWord(reply)).toBeUndefined();
    expect(handlingWord(legacy(waiting))).toBeUndefined();
  });
  it("「AI 正在准备建议」 only for 急事 being looked at without a suggestion", () => {
    expect(preparing(urgent)).toBe(true);
    expect(preparing({ ...urgent, urgent: false })).toBe(false);
    expect(preparing({ ...urgent, suggestion: reply.suggestion })).toBe(false);
    expect(preparing(waiting)).toBe(false);
  });
  it("names the suggestion's kind and its first line", () => {
    expect(suggestionTitle(reply.suggestion!)).toBe("建议回复");
    expect(suggestionTitle(agentSuggestion.suggestion!)).toBe("建议派 Agent");
    expect(suggestionTitle(conclusion.suggestion!)).toBe("结论");
    expect(firstLine("\n  第一行\n第二行")).toBe("第一行");
  });
});

describe("收件箱事件：建议的按钮", () => {
  it("per kind, and 确认 for one that needs judgment", () => {
    expect(suggestionButtons(reply.suggestion!)).toEqual({ confirm: "发送", edit: true });
    expect(suggestionButtons(agentSuggestion.suggestion!)).toEqual({ confirm: "开始", edit: true });
    expect(suggestionButtons(conclusion.suggestion!)).toEqual({ confirm: "知道了", edit: false });
    expect(suggestionButtons({ ...reply.suggestion!, handback: true })).toEqual({ confirm: "确认", edit: true });
  });
});

describe("收件箱事件：处理链条", () => {
  it("folds to the three newest entries", () => {
    expect(ids(chainRows(waiting.chain, false))).toEqual(["c4", "c5", "c6"]);
    expect(ids(chainRows(waiting.chain, true))).toEqual(["c1", "c2", "c3", "c4", "c5", "c6"]);
    expect(chainRows(undefined, false)).toEqual([]);
  });
  it("goes where each link says, or expands in place", () => {
    const conv = (mid: string, sid: string) => ({ name: "session", mid, sid }) as const;
    const [received, triaged, handling, confirmed, sent] = waiting.chain!;
    expect(chainTap(received, conv)).toEqual({ kind: "source", position: "300", uid: undefined, url: undefined });
    expect(chainTap(triaged, conv)).toEqual({ kind: "expand" });
    expect(chainTap(handling, conv)).toEqual({ kind: "route", route: { name: "session", mid: "m1", sid: "s-demo3" } });
    expect(chainTap(confirmed, conv)).toEqual({ kind: "expand" });
    expect(chainTap(sent, conv)).toMatchObject({ kind: "source", position: "305" });
    expect(chainTap(agentWorking.chain![0], conv)).toMatchObject({ kind: "source", uid: 77 });
    expect(chainTap({ id: "x", kind: "sent", text: "发了评论", link: { type: "source", url: "https://example.com/pr/1" } }, conv)).toMatchObject({ kind: "source", url: "https://example.com/pr/1" });
    expect(chainTap({ id: "x", kind: "related", text: "有关", link: { type: "item", id: "mail:a1:review:55" } }, conv)).toEqual({ kind: "route", route: { name: "inbox", id: "mail:a1:review:55" } });
    // an unknown link and nothing to expand: nothing happens
    expect(chainTap({ id: "x", kind: "future", text: "以后的", link: { type: "elsewhere" } }, conv)).toEqual({ kind: "none" });
  });
  it("keeps detail and messages expandable for reported and other kinds", () => {
    const conv = (mid: string, sid: string) => ({ name: "session", mid, sid }) as const;
    const reported = { id: "r", kind: "reported", text: "Assistant says: sent", detail: "Sent" };
    expect(canExpand(reported)).toBe(true);
    expect(chainTap(reported, conv)).toEqual({ kind: "expand" });
    const withMessages = { ...reported, messages: [{ text: "Original message" }] };
    expect(canExpand(withMessages)).toBe(true);
    expect(chainTap(withMessages, conv)).toEqual({ kind: "expand" });
    const other = { id: "t", kind: "triaged", text: "Sorted", detail: "The basis" };
    expect(canExpand(other)).toBe(true);
    expect(chainTap(other, conv)).toEqual({ kind: "expand" });
  });
  it("writes the time, with the day when it is not today", () => {
    const now = new Date(2026, 10, 5, 12, 0);
    expect(chainTime(new Date(2026, 10, 5, 9, 3).toISOString(), now)).toBe("09:03");
    expect(chainTime(new Date(2026, 10, 4, 18, 30).toISOString(), now)).toBe("11月4日 18:30");
    expect(chainTime(undefined, now)).toBe("");
  });
  it("opens a mail of the thread at its uid", () => {
    const it2 = { ...agentWorking, mail: { ...agentWorking.mail!, uids: [70, 77, 80] } };
    expect(itemAtUid(it2, 77).mail!.uids).toEqual([70, 77]);
    expect(itemAtUid(it2, 5).mail!.uids).toEqual([70, 77, 80]);
    expect(itemAtUid(it2).mail!.uids).toEqual([70, 77, 80]);
  });
});

describe("收件箱事件：确认 / 驳回 / 说一句话", () => {
  const res = (status: number, body: unknown) => ({ ok: status < 300, status, text: async () => JSON.stringify(body), headers: new Headers() });
  let fetcher: ReturnType<typeof vi.fn>;
  beforeEach(async () => {
    resetInboxForTests();
    vi.mocked(toast).mockClear();
    vi.stubGlobal("localStorage", { getItem: () => "jd_test" });
    fetcher = vi.fn().mockResolvedValueOnce(res(200, { items: fixture.items }));
    vi.stubGlobal("fetch", fetcher);
    await reloadInbox();
  });
  afterEach(() => vi.unstubAllGlobals());

  it("sends the version shown, the action, the edited text and a client id", async () => {
    const after = { ...reply, suggestion: undefined, status: "working", handling: { ...reply.handling!, state: "waiting" } };
    fetcher.mockResolvedValueOnce(res(200, after));
    expect(await suggestionAct(reply, "confirm", "好的，周四给你")).toBe(true);
    const [url, init] = fetcher.mock.calls[1];
    expect(url).toBe("/api/v1/inbox/lark:oc_demo1:100/suggestion");
    const body = JSON.parse(init.body);
    expect(body).toMatchObject({ version: 2, action: "confirm", text: "好的，周四给你" });
    expect(body.client_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(getInbox().items.find((x) => x.id === reply.id)?.status).toBe("working");
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["已发送，等对方回复", "ok"]);
  });
  it("「建议已更新」 and the item as the hub has it now when the version moved", async () => {
    const fresh = { ...reply, suggestion: { ...reply.suggestion!, version: 3, text: "新的建议" } };
    fetcher.mockResolvedValueOnce(res(409, { code: "suggestion_stale", error: "建议已更新", item: fresh }));
    expect(await suggestionAct(reply, "confirm")).toBe(false);
    expect(getInbox().items.find((x) => x.id === reply.id)?.suggestion?.text).toBe("新的建议");
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["建议已更新", "info"]);
  });
  it("says why another failure did not go through, and leaves the item", async () => {
    fetcher.mockResolvedValueOnce(res(502, { code: "assistant_in_doubt", error: "不确定发出去没有" }));
    expect(await suggestionAct(agentSuggestion, "reject")).toBe(false);
    expect(getInbox().items.find((x) => x.id === agentSuggestion.id)?.suggestion?.version).toBe(1);
    expect(vi.mocked(toast).mock.calls.at(-1)?.[1]).toBe("error");
  });
  it("instruct carries the words; no suggestion: nothing is sent", async () => {
    fetcher.mockResolvedValueOnce(res(200, reply));
    await actOnSuggestion("/inbox/x", 4, "instruct", "别这么回，先问时间", "cid-1");
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ version: 4, action: "instruct", text: "别这么回，先问时间", client_id: "cid-1" });
    expect(await suggestionAct(failed, "confirm")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
