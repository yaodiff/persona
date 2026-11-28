// Inbox events (docs/assistant/inbox.md "Events and handling chain"), driven by the fake hub's fixture (scripts/mock/inbox_events.json):
// one item per state. An item without this phase's fields stays where the tier put it.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./store", () => ({ onHubEvent: () => () => {} }));
vi.mock("../ui/atoms", () => ({ toast: vi.fn() }));

import { initI18n } from "../i18n";
import { toast } from "../ui/atoms";
import { badgeCount, getInbox, handOffToMain, reloadInbox, replaceItem, resetInboxForTests, sections, suggestionAct, uncertainReplyKey, TIERS, type InboxItem } from "./inbox";
import {
  actedText, actOnSuggestion, canExpand, chainRows, chainTap, chainTime, firstLine, handlingWord, hasEvent, itemAtUid, preparing, statusOf, suggestionButtons, suggestionTitle,
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
    expect(handlingWord(urgent)).toBe("AI 正在查看");
    expect(handlingWord(agentWorking)).toBe("Agent 处理中");
    expect(handlingWord(waiting)).toBe("等待对方回复");
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
    expect(suggestionTitle(agentSuggestion.suggestion!)).toBe("建议启动 Agent");
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

describe("handoff suggestion copy", () => {
  const handoff = { kind: "handoff", version: 7, text: "Review the dates", target_title: "Launch", session_id: "s-target" };

  afterEach(async () => { await initI18n("zh-Hans"); });
  it.each([
    ["en", "Suggested handoff to Launch", "Hand off", "Handed off"],
    ["zh-Hans", "建议转交给「Launch」", "转交", "已转交"],
    ["ja", "「Launch」への引き継ぎ", "引き継ぐ", "引き継ぎました"],
  ])("names the destination, action and receipt in %s", async (lang, title, button, receipt) => {
    await initI18n(lang);
    expect(suggestionTitle(handoff)).toBe(title);
    expect(suggestionButtons(handoff)).toEqual({ confirm: button, edit: true });
    expect(actedText(handoff, "confirm")).toBe(receipt);
  });

  it("keeps unknown kinds generic even when they carry a target", () => {
    const unknown = { ...handoff, kind: "future" };
    expect(suggestionTitle(unknown)).toBe("建议");
    expect(suggestionButtons(unknown)).toEqual({ confirm: "确认", edit: true });
    expect(actedText(unknown, "confirm")).toBe("已交由 AI 处理");
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
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["已发送，等待对方回复", "ok"]);
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
  const receipt = (status: "failed" | "sending" | "unknown" | "sent", extra = {}) => ({ version: 2, status, retryable: status === "failed", text: "实际确认的修改稿", at: "2026-11-18T13:00:00Z", conversation: reply.suggestion!.conversation, ...extra });
  it("preserves delivery metadata and authoritative failure readback", async () => {
    const item = { ...reply, suggestion_attempts: [receipt("failed", { retry_after: 3 })] };
    fetcher.mockResolvedValueOnce(res(502, { code: "rate_limited", error: "wait", delivered: "none", retryable: true, retry_after: 3, item }));
    const r = await actOnSuggestion("/inbox/x", 2, "confirm");
    expect(r).toMatchObject({ ok: false, stale: false, delivered: "none", retryable: true, retry_after: 3, item: JSON.parse(JSON.stringify(item)) });
  });
  it("reads a retained failed attempt and retries only its frozen version, without an edited replacement", async () => {
    const item = { ...reply, suggestion_attempts: [receipt("failed")] };
    fetcher.mockResolvedValueOnce(res(502, { code: "temporarily_unavailable", error: "offline", delivered: "none", retryable: true, item }));
    expect(await suggestionAct(reply, "confirm", "实际确认的修改稿")).toBe(false);
    expect(getInbox().items.find(x => x.id === reply.id)?.suggestion_attempts).toEqual(item.suggestion_attempts);
    expect(await suggestionAct(item, "confirm", "不同的新修改")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(2);
    fetcher.mockResolvedValueOnce(res(200, { ...reply, suggestion: undefined, suggestion_attempts: [receipt("sent")] }));
    expect(await suggestionAct(item, "confirm")).toBe(true);
    expect(JSON.parse(fetcher.mock.calls[2][1].body)).toMatchObject({ version: 2, action: "confirm" });
    expect(JSON.parse(fetcher.mock.calls[2][1].body).text).toBeUndefined();
  });
  it.each(["unknown", "sending"] as const)("never retries a durable %s attempt", async status => {
    const item = { ...reply, suggestion_attempts: [receipt(status)] };
    replaceItem(item);
    expect(await suggestionAct(item, "confirm")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
  it("uses GET after a lost response, and its durable receipt supersedes the local uncertainty", async () => {
    const item = { ...reply, suggestion_attempts: [receipt("failed")] };
    fetcher.mockRejectedValueOnce(new TypeError("lost response")).mockResolvedValueOnce(res(200, item));
    expect(await suggestionAct(reply, "confirm", "实际确认的修改稿")).toBe(false);
    expect(fetcher.mock.calls[2][0]).toBe("/api/v1/inbox/lark:oc_demo1:100");
    expect(getInbox().uncertainReplies[uncertainReplyKey(reply, 2)]).toBeUndefined();
    expect(getInbox().items.find(x => x.id === reply.id)?.suggestion_attempts?.[0].status).toBe("failed");
  });
  it("keeps a conservative no-resend hint when both POST and readback are lost, without blocking a different version", async () => {
    fetcher.mockRejectedValueOnce(new TypeError("lost response")).mockRejectedValueOnce(new TypeError("GET offline"));
    expect(await suggestionAct(reply, "confirm", "实际确认的修改稿")).toBe(false);
    expect(getInbox().uncertainReplies[uncertainReplyKey(reply, 2)]).toMatchObject({ status: "unknown", text: "实际确认的修改稿", retryable: false });
    expect(await suggestionAct(reply, "confirm")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(3);
    const fresh = { ...reply, suggestion: { ...reply.suggestion!, version: 3, text: "合法新回复" } };
    replaceItem(fresh);
    fetcher.mockResolvedValueOnce(res(200, { ...fresh, suggestion: undefined }));
    expect(await suggestionAct(fresh, "confirm")).toBe(true);
    expect(JSON.parse(fetcher.mock.calls[3][1].body).version).toBe(3);
  });
  it("does not let an older failed receipt authorize another send after a Retry's POST and GET are lost", async () => {
    const item = { ...reply, suggestion_attempts: [receipt("failed")] };
    replaceItem(item);
    fetcher.mockRejectedValueOnce(new TypeError("retry response lost")).mockRejectedValueOnce(new TypeError("GET offline"));
    expect(await suggestionAct(item, "confirm")).toBe(false);
    expect(getInbox().uncertainReplies[uncertainReplyKey(item, 2)]?.status).toBe("unknown");
    expect(await suggestionAct(item, "confirm")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(3);
  });
  it("accepts a later fresh list's durable sent receipt after a lost retry", async () => {
    const item = { ...reply, suggestion_attempts: [receipt("failed")] };
    replaceItem(item);
    fetcher.mockRejectedValueOnce(new TypeError("retry response lost")).mockRejectedValueOnce(new TypeError("GET offline"));
    await suggestionAct(item, "confirm");
    fetcher.mockResolvedValueOnce(res(200, { items: [{ ...reply, suggestion: undefined, suggestion_attempts: [receipt("sent")] }] }));
    await reloadInbox();
    expect(getInbox().uncertainReplies[uncertainReplyKey(item, 2)]).toBeUndefined();
    expect(getInbox().items.find(x => x.id === reply.id)?.suggestion_attempts?.[0].status).toBe("sent");
  });
  it("does not let a list begun before the lost retry clear its new uncertainty", async () => {
    const item = { ...reply, suggestion_attempts: [receipt("failed")] };
    replaceItem(item);
    let respond!: (r: unknown) => void;
    fetcher.mockImplementationOnce(() => new Promise(resolve => { respond = resolve; }))
      .mockRejectedValueOnce(new TypeError("retry response lost")).mockRejectedValueOnce(new TypeError("GET offline"));
    const listing = reloadInbox();
    await suggestionAct(item, "confirm");
    respond(res(200, { items: [item] }));
    await listing;
    expect(getInbox().uncertainReplies[uncertainReplyKey(item, 2)]?.status).toBe("unknown");
    expect(await suggestionAct(item, "confirm")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(4);
  });
  it("does not treat a held delta row as a newly returned receipt", async () => {
    const item = { ...reply, suggestion_attempts: [receipt("failed")] };
    fetcher.mockResolvedValueOnce(res(200, { items: [item], cursor: "c1" }));
    await reloadInbox();
    fetcher.mockRejectedValueOnce(new TypeError("retry response lost")).mockRejectedValueOnce(new TypeError("GET offline"));
    await suggestionAct(item, "confirm");
    fetcher.mockResolvedValueOnce(res(200, { items: [], cursor: "c2", delta: true }));
    await reloadInbox();
    expect(getInbox().items.find(x => x.id === reply.id)?.suggestion_attempts?.[0].status).toBe("failed");
    expect(getInbox().uncertainReplies[uncertainReplyKey(item, 2)]?.status).toBe("unknown");
    expect(await suggestionAct(item, "confirm")).toBe(false);
  });
  it("merges a late outcome without overwriting a newer pushed suggestion", async () => {
    let respond!: (r: unknown) => void;
    fetcher.mockImplementationOnce(() => new Promise(resolve => { respond = resolve; }));
    const pending = suggestionAct(reply, "confirm", "实际确认的修改稿");
    const fresh = { ...reply, suggestion: { ...reply.suggestion!, version: 3, text: "新建议" } };
    replaceItem(fresh);
    respond(res(502, { code: "offline", error: "offline", item: { ...reply, suggestion_attempts: [receipt("failed")] } }));
    await pending;
    const now = getInbox().items.find(x => x.id === reply.id)!;
    expect(now.suggestion?.version).toBe(3);
    expect(now.suggestion_attempts?.[0].text).toBe("实际确认的修改稿");
    expect(await suggestionAct(reply, "confirm")).toBe(false);
  });
  it.each(["source", "filed"] as const)("keeps an arrived same-version %s push when an old failed response finishes", async change => {
    let respond!: (r: unknown) => void;
    fetcher.mockImplementationOnce(() => new Promise(resolve => { respond = resolve; }));
    const pending = suggestionAct(reply, "confirm", "实际确认的修改稿");
    const newer: InboxItem = { ...reply, suggestion: undefined, ...(change === "source"
      ? { version: "new-source", updated_at: "2026-11-18T14:00:00Z", count: 2 }
      : { done_at: "2026-11-18T14:00:00Z", done_by: "user" }) };
    replaceItem(newer);
    respond(res(502, { code: "offline", error: "offline", item: { ...reply, suggestion_attempts: [receipt("failed")] } }));
    await pending;
    const now = getInbox().items.find(x => x.id === reply.id)!;
    expect(now.suggestion).toBeUndefined();
    expect(now.version).toBe(newer.version);
    expect(now.done_at).toBe(newer.done_at);
    expect(now.suggestion_attempts?.[0].status).toBe("failed");
    expect(await suggestionAct(now, "confirm")).toBe(false);
  });
  it.each(["source", "filed"] as const)("keeps an arrived same-version %s push while retaining a late successful receipt", async change => {
    let respond!: (r: unknown) => void;
    fetcher.mockImplementationOnce(() => new Promise(resolve => { respond = resolve; }));
    const pending = suggestionAct(reply, "confirm", "实际确认的修改稿");
    const newer: InboxItem = { ...reply, suggestion: undefined, ...(change === "source"
      ? { version: "new-source", updated_at: "2026-11-18T14:00:00Z", count: 2 }
      : { done_at: "2026-11-18T14:00:00Z", done_by: "user" }) };
    replaceItem(newer);
    respond(res(200, { ...reply, suggestion: undefined, done_at: "2026-11-18T13:00:00Z", done_by: "replied", suggestion_attempts: [receipt("sent")] }));
    await pending;
    const now = getInbox().items.find(x => x.id === reply.id)!;
    expect(now.version).toBe(newer.version);
    expect(now.done_at).toBe(newer.done_at);
    expect(now.done_by).toBe(newer.done_by);
    expect(now.suggestion_attempts?.[0].status).toBe("sent");
  });
  it("coalesces quick clicks before the first reply response", async () => {
    let respond!: (r: unknown) => void;
    fetcher.mockImplementationOnce(() => new Promise(resolve => { respond = resolve; }));
    const one = suggestionAct(reply, "confirm");
    expect(await suggestionAct(reply, "confirm")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(2);
    respond(res(200, { ...reply, suggestion: undefined }));
    await one;
  });
  it("coalesces concurrent Wrap up entries, uses authoritative readback, and releases the local request on completion", async () => {
    let respond!: (r: unknown) => void;
    fetcher.mockImplementationOnce(() => new Promise(resolve => { respond = resolve; }));
    const item = { ...reply, suggestion: undefined };
    const one = handOffToMain(item, "收尾", "first", true);
    const two = handOffToMain(item, "收尾", "second", true);
    expect(one).toBe(two);
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ text: "收尾", client_id: "first", reuse_pending: true });
    respond(res(200, { item: reply, session: { machine_id: "server", session_id: "main" }, reused: true }));
    await one;
    expect(getInbox().items.find(x => x.id === reply.id)?.suggestion?.version).toBe(2);
    fetcher.mockResolvedValueOnce(res(200, { session: { machine_id: "server", session_id: "main" } }));
    await handOffToMain(item, "收尾", "third", true);
    expect(fetcher).toHaveBeenCalledTimes(3); // the server, rather than a permanent client cache, owns the pending gap
  });
  it("captures a handoff's current store item rather than a stale component copy", async () => {
    const current = { ...reply, suggestion: undefined, title: "current before request" };
    replaceItem(current);
    const fresh = { ...current, suggestion: { ...reply.suggestion!, version: 3, text: "handoff result" } };
    fetcher.mockResolvedValueOnce(res(200, { item: fresh, session: { machine_id: "server", session_id: "main" } }));
    await handOffToMain({ ...reply }, "收尾", "one", true);
    expect(getInbox().items.find(x => x.id === reply.id)?.suggestion?.version).toBe(3);
  });
  it("preserves a source push that arrives during handoff instead of reopening the old suggestion", async () => {
    let respond!: (r: unknown) => void;
    fetcher.mockImplementationOnce(() => new Promise(resolve => { respond = resolve; }));
    const pending = handOffToMain(reply, "收尾", "one", true);
    replaceItem({ ...reply, suggestion: undefined, version: "new-source", count: 2 });
    respond(res(200, { item: reply, session: { machine_id: "server", session_id: "main" } }));
    await pending;
    const now = getInbox().items.find(x => x.id === reply.id)!;
    expect(now.suggestion).toBeUndefined();
    expect(now.version).toBe("new-source");
  });
  it("captures a fresh GET snapshot after a POST-era push, then accepts its uncontested readback", async () => {
    let post!: (r: unknown) => void;
    let read!: (r: unknown) => void;
    let markReadStarted!: () => void;
    const readStarted = new Promise<void>(resolve => { markReadStarted = resolve; });
    fetcher.mockImplementationOnce(() => new Promise(resolve => { post = resolve; }))
      .mockImplementationOnce(() => new Promise(resolve => { read = resolve; markReadStarted(); }));
    const pending = suggestionAct(reply, "confirm");
    replaceItem({ ...reply, suggestion: undefined, version: "new-source" });
    post(res(502, { code: "offline", error: "response lost" }));
    await readStarted;
    expect(fetcher).toHaveBeenCalledTimes(3);
    const fresh = { ...reply, version: "new-source", suggestion: { ...reply.suggestion!, version: 3, text: "authoritative current" }, suggestion_attempts: [receipt("unknown")] };
    read(res(200, fresh));
    await pending;
    expect(getInbox().items.find(x => x.id === reply.id)?.suggestion?.version).toBe(3);
  });
  it("keeps a same-version filing that arrives during fallback GET, and merges only its receipt", async () => {
    let read!: (r: unknown) => void;
    let markReadStarted!: () => void;
    const readStarted = new Promise<void>(resolve => { markReadStarted = resolve; });
    fetcher.mockResolvedValueOnce(res(502, { code: "offline", error: "response lost" }))
      .mockImplementationOnce(() => new Promise(resolve => { read = resolve; markReadStarted(); }));
    const pending = suggestionAct(reply, "confirm");
    await readStarted;
    expect(fetcher).toHaveBeenCalledTimes(3);
    replaceItem({ ...reply, suggestion: undefined, done_at: "2026-11-18T14:00:00Z", done_by: "user" });
    read(res(200, { ...reply, suggestion_attempts: [receipt("unknown")] }));
    await pending;
    const now = getInbox().items.find(x => x.id === reply.id)!;
    expect(now.suggestion).toBeUndefined();
    expect(now.done_by).toBe("user");
    expect(now.suggestion_attempts?.[0].status).toBe("unknown");
  });
  it("keeps explicit typed handoffs independent, including equal words with distinct identities", async () => {
    fetcher.mockResolvedValue(res(200, { session: { machine_id: "server", session_id: "main" } }));
    await Promise.all([handOffToMain(reply, "新要求", "one"), handOffToMain(reply, "新要求", "two"), handOffToMain(reply, "另一要求", "three")]);
    expect(fetcher).toHaveBeenCalledTimes(4);
    for (const [, init] of fetcher.mock.calls.slice(1)) expect(JSON.parse(init.body).reuse_pending).toBeUndefined();
  });
  it("does not reconcile an error item from a different manager, and routes readback to the original manager", async () => {
    const target = { ...reply, manager: "other" };
    fetcher.mockResolvedValueOnce(res(200, { items: [target, reply] }));
    await reloadInbox();
    fetcher.mockResolvedValueOnce(res(502, { code: "offline", error: "offline", item: { ...reply, manager: "foreign", suggestion_attempts: [receipt("failed")] } }))
      .mockResolvedValueOnce(res(200, { ...target, suggestion_attempts: [receipt("unknown")] }));
    expect(await suggestionAct(target, "confirm")).toBe(false);
    expect(new Headers(fetcher.mock.calls[3][1].headers).get("X-Persona-Manager")).toBe("other");
    expect(getInbox().items.find(x => x.manager === "other")?.suggestion_attempts?.[0].status).toBe("unknown");
    expect(getInbox().items.find(x => !x.manager)?.suggestion_attempts).toBeUndefined();
  });
  it("keeps a handback reply's instruction behavior outside actual reply delivery tracking", async () => {
    const item = { ...reply, suggestion: { ...reply.suggestion!, handback: true } };
    replaceItem(item);
    fetcher.mockResolvedValueOnce(res(502, { code: "offline", error: "offline" }));
    expect(await suggestionAct(item, "confirm", "需要判断的修改稿")).toBe(false);
    expect(getInbox().uncertainReplies).toEqual({});
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(JSON.parse(fetcher.mock.calls[1][1].body).text).toBe("需要判断的修改稿");
  });
  it("instruct carries the words; no suggestion: nothing is sent", async () => {
    fetcher.mockResolvedValueOnce(res(200, reply));
    await actOnSuggestion("/inbox/x", 4, "instruct", "别这么回，先问时间", "cid-1");
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ version: 4, action: "instruct", text: "别这么回，先问时间", client_id: "cid-1" });
    expect(await suggestionAct(failed, "confirm")).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
