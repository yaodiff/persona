// @vitest-environment jsdom
// Inbox events on the page (docs/web/web.md "Inbox"): the "In progress" section and the rows' marks, the detail's summary → suggestion
// card → recap → handling chain, "Edit" / "Say something", "Original"; and an older hub's items (none of the fields) exactly as before.
// Driven by the fake hub's fixture (scripts/mock/inbox_events.json).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  // a Feishu item's chat draws under its event (LarkThread): what jsdom lacks
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

const calls = vi.hoisted(() => ({
  list: [] as { path: string; method?: string; json?: unknown; headers?: unknown }[],
  items: [] as unknown[],
  suggestion: undefined as (() => Promise<unknown>) | undefined,
}));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown; headers?: unknown }) => {
    calls.list.push({ path, method: init?.method, json: init?.json, headers: init?.headers });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [] });
    if (path.startsWith("/mail/messages/")) {
      const uid = Number(path.split("/").pop()?.split("?")[0]);
      return Promise.resolve({ message: { uid, text: `mail body ${uid}`, seen: false } });
    }
    if (path.endsWith("/suggestion")) return calls.suggestion ? calls.suggestion() : Promise.reject(new Error("offline"));
    return Promise.resolve({});
  },
}));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
// the store's first snapshot is in (the list waits for it), with no agents waiting
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesChat: () => false, translatesMail: () => false, languageName: () => "" }));

import type { InboxItem } from "../../lib/inbox";
import { resetInboxForTests, shortTime, sourceLine } from "../../lib/inbox";
import { rowMeta } from "../../lib/inbox-now";
import { chainRows, type ChainEntry } from "../../lib/inbox-events";
import { ApiError } from "../../lib/api";
import enInboxEvent from "../../i18n/locales/en/inbox-inboxevent.json";
import { resetMailForTests } from "../../lib/mail";
import { getState } from "../../lib/store";
import type { AgentState, Session } from "../../lib/types";
import { stateName } from "../../lib/state-labels";
import { toast } from "../../ui/atoms";
import { InboxDetail } from "./InboxDetail";
import { chainIconMeaning, EventBody } from "./InboxEvent";
import { InboxList } from "./InboxList";
import { settleRosterDefault } from "../../lib/manager-choice";

// the roster's default is known, as once the app has read it: an item's page draws at once
settleRosterDefault();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const fixture = JSON.parse(readFileSync(join(__dirname, "../../../../scripts/mock/inbox_events.json"), "utf8")) as { items: InboxItem[] };
const showcase = JSON.parse(readFileSync(join(__dirname, "../../../../spec/fixtures/inbox/showcase.json"), "utf8")) as { items: InboxItem[]; sessions: Session[] };
const timeline = JSON.parse(readFileSync(join(__dirname, "../../../../spec/fixtures/inbox/timeline.json"), "utf8")) as {
  cases: {
    name: string;
    input: {
      item: { id?: string; chain: ChainEntry[] };
      sessions?: { machine_id: string; session_id: string; title?: string; state: AgentState }[];
    };
    expected: {
      folded: string[];
      expanded: string[];
      steps: { id: string; icon: string; reported: boolean }[];
      live_state?: { id: string; session_state: AgentState }[];
    };
  }[];
};
const byId = (id: string) => fixture.items.find((x) => x.id === id)!;
const reply = byId("lark:oc_demo1:100");
const urgent = byId("lark:oc_demo2:200");
const waiting = byId("lark:oc_demo3:300");
const agentSuggestion = byId("mail:a1:contract:88");
const failed = byId("lark:oc_demo6:600");
const conclusion = byId("lark:oc_demo7:700");
const managerDone = byId("mail:a1:notice:110");
const legacy = (it: InboxItem): InboxItem => {
  const { status: _s, urgent: _u, recap: _r, handling: _h, suggestion: _g, chain: _c, related: _rel, now: _n, ...rest } = it;
  // an older hub never files as the manager
  return rest.done_by === "manager" ? { ...rest, done_by: "user" } : rest;
};
/** An item with no source page of its own (no chat, no mail): its body is the preview, so "Original" needs no reads. */
const plain = (it: InboxItem): InboxItem => ({ ...it, lark: undefined, mail: undefined });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent?.trim() ?? "");
const press = async (text: string) => {
  const b = [...host.querySelectorAll("button")].find((x) => x.textContent?.trim() === text);
  if (!b) throw new Error(`no button ${text}: ${buttons().join(" | ")}`);
  await act(async () => b.click());
};

beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  Object.assign(getState(), { sessions: {} });
  calls.list = [];
  calls.items = [];
  calls.suggestion = undefined;
  vi.mocked(toast).mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("收件箱事件：详情", () => {
  it("calls the section Timeline in English", () => {
    expect(enInboxEvent.chain).toBe("Timeline");
  });
  it("matches every shared timeline case, including via and reported icons", () => {
    for (const c of timeline.cases) {
      const chain = c.input.item.chain;
      expect(chainRows(chain, false).map((e) => e.id), c.name).toEqual(c.expected.folded);
      expect(chainRows(chain, true).map((e) => e.id), c.name).toEqual(c.expected.expanded);
      for (const step of c.expected.steps) {
        const entry = chain.find((e) => e.id === step.id)!;
        expect(chainIconMeaning(entry), `${c.name}/${step.id}`).toBe(step.icon);
        expect(entry.kind === "reported", `${c.name}/${step.id}`).toBe(step.reported);
      }
    }
    expect(chainIconMeaning({ kind: "agent" })).toBe("agents");
    expect(chainIconMeaning({ kind: "agent_stopped" })).toBe("agents");
  });
  it("matches every shared live_state expectation, with no state on older Agent steps or handling", async () => {
    for (const c of timeline.cases.filter((x) => x.expected.live_state)) {
      const sessions: Record<string, Session[]> = {};
      for (const s of c.input.sessions ?? []) {
        (sessions[s.machine_id] ??= []).push({ ...s, title: s.title ?? s.session_id, cwd: "/work", status: s.state } as Session);
      }
      Object.assign(getState(), { sessions });
      const item = { ...plain(reply), id: c.input.item.id ?? c.name, handling: undefined, suggestion: undefined, now: undefined, chain: c.input.item.chain };
      act(() => root.render(<EventBody item={item} onSource={() => {}} />));
      if (c.input.item.chain.length > 3) await press(`展开全部（${c.input.item.chain.length}）`);
      for (const entry of c.input.item.chain) {
        const expected = c.expected.live_state?.find((x) => x.id === entry.id);
        const state = host.querySelector(`[data-chain-id="${entry.id}"] [data-chain-agent-state]`);
        expect(state?.textContent, `${c.name}/${entry.id}`).toBe(expected ? stateName(expected.session_state) : undefined);
      }
    }
  });
  it("draws 摘要, then the reply suggestion and 总结, then all three chain entries without a toggle", async () => {
    act(() => root.render(<EventBody item={reply} onSource={() => {}} />));
    const text = host.textContent ?? "";
    expect(text.indexOf(reply.summary!)).toBeLessThan(text.indexOf("建议回复"));
    expect(text.indexOf("建议回复")).toBeLessThan(text.indexOf(reply.recap!));
    expect(text.indexOf(reply.recap!)).toBeLessThan(text.indexOf("处理链条"));
    expect(host.querySelector("[data-suggestion-text]")?.textContent).toBe(reply.suggestion!.text);
    expect(buttons()).toEqual(expect.arrayContaining(["驳回", "修改", "发送"]));
    expect(buttons()).not.toContain("说一句");
    expect([...host.querySelectorAll("[data-chain-entry]")].map((e) => e.getAttribute("data-chain-entry"))).toEqual(["received", "triaged", "handling"]);
    expect(host.querySelector("[data-chain-toggle]")).toBeNull();
  });
  it("puts nowText and its conversation link before the summary, suggestion and background", () => {
    const item = { ...reply, now: "confirm_reply" } as InboxItem;
    act(() => root.render(<EventBody item={item} onSource={() => {}} />));
    const status = host.querySelector("[data-event-now]")!;
    expect(status.getAttribute("data-event-now")).toBe("confirm_reply");
    expect(status.textContent).toContain("等你确认回复");
    expect(status.textContent).toContain("处理对话");
    const text = host.textContent ?? "";
    expect(text.indexOf("等你确认回复")).toBeLessThan(text.indexOf(item.summary!));
    expect(text.indexOf(item.summary!)).toBeLessThan(text.indexOf("建议回复"));
    expect(text.indexOf("建议回复")).toBeLessThan(text.indexOf(item.recap!));
    expect(text.indexOf(item.recap!)).toBeLessThan(text.indexOf("处理链条"));
  });
  it("orders a locally constructed open item as status, summary, suggestion, recap and timeline", () => {
    const item: InboxItem = {
      id: "local:suggestion-order",
      source: "mail",
      kind: "mail",
      account: "local",
      account_name: "Local",
      title: "Review the proposal",
      preview: "Please review the proposal",
      count: 1,
      now: "confirm_reply",
      handling: { owner: "manager", state: "looking", machine_id: "local-machine", session_id: "local-session" },
      summary: "The proposal is ready for review.",
      suggestion: { version: 1, kind: "reply", text: "Thanks, I will review it." },
      recap: "The sender asked for feedback yesterday.",
      chain: [{ id: "local-triaged", kind: "triaged", text: "Sorted for review" }],
    };
    act(() => root.render(<EventBody item={item} onSource={() => {}} />));
    const selectors = ["[data-event-now]", "[data-event-summary]", "[data-suggestion]", "[data-event-recap]", "[data-event-chain]"];
    const ordered = selectors.map((selector) => {
      const element = host.querySelector(selector);
      expect(element, selector).not.toBeNull();
      return element!;
    });
    for (let i = 1; i < ordered.length; i++) {
      expect(ordered[i - 1].compareDocumentPosition(ordered[i]) & Node.DOCUMENT_POSITION_FOLLOWING, `${selectors[i - 1]} before ${selectors[i]}`).not.toBe(0);
    }
    expect(ordered[0].getAttribute("data-event-now")).toBe("confirm_reply");
    expect(ordered[0].textContent).toContain("等你确认回复");
    expect(buttons()).toContain("处理对话");
    expect(buttons()).not.toContain("说一句");
  });
  it("uses rowMeta without a time and moves Original into the small line; an older item keeps sourceLine and its time", () => {
    const embedded = { ...showcase.items[0], lark: reply.lark };
    act(() => root.render(<InboxDetail item={embedded} loaded />));
    expect(host.querySelector("[data-detail-meta] span")?.textContent).toBe("订单群 · 张三：好的，我看一下新的");
    expect(host.querySelector("[data-detail-meta] button")?.textContent).toBe("看原文");
    expect(host.querySelector("[data-detail-meta] button")?.getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector('[data-event-body="embedded"]')).toBeNull();
    expect(host.querySelector("[data-event-now]")?.textContent).toContain("等张三确认新报价");
    expect(host.querySelector("[data-event-now]")?.textContent).toContain("问更正后的报价行不行");

    const withWhere = { ...plain(reply), id: "where-source", where: "订单群" } as InboxItem;
    act(() => root.render(<InboxDetail item={withWhere} loaded />));
    const current = host.querySelector("[data-detail-meta]")!;
    expect(current.querySelector("span")?.textContent).toBe(rowMeta(withWhere));
    expect(current.textContent).not.toContain(shortTime(withWhere.updated_at));
    expect(current.querySelector("button")?.textContent).toBe("看原文");
    expect([...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "看原文")).toHaveLength(1);

    const old = { ...plain(reply), id: "old-source" };
    act(() => root.render(<InboxDetail item={old} loaded />));
    const legacy = host.querySelector("[data-detail-meta]")!;
    expect(legacy.querySelector("span")?.textContent).toBe(sourceLine(old));
    const time = shortTime(old.updated_at);
    expect(legacy.textContent).toBe(`${sourceLine(old)}${sourceLine(old) && time ? ` · ${time}` : time}看原文`);
    expect(legacy.querySelector("button")?.textContent).toBe("看原文");
  });
  it("renders showcase sc-1 folded and expanded, with reported apart and the Agent's live state", async () => {
    Object.assign(getState(), { sessions: { m1: showcase.sessions } });
    act(() => root.render(<EventBody item={showcase.items[0]} onSource={() => {}} />));
    const ids = () => [...host.querySelectorAll("[data-chain-id]")].map((e) => e.getAttribute("data-chain-id"));
    expect(ids()).toEqual(["c5", "c6", "c7"]);
    const reported = host.querySelector('[data-chain-id="c5"]')!;
    expect(reported.getAttribute("data-chain-reported")).toBe("");
    expect(reported.textContent).toContain("助手说");
    expect(reported.querySelector('[data-chain-icon="reported"]')).not.toBeNull();
    expect(reported.querySelector('[data-chain-glyph="chat"]')).not.toBeNull();
    await press("展开全部（7）");
    expect(ids()).toEqual(["c1", "c2", "c3", "c4", "c5", "c6", "c7"]);
    expect(host.querySelector('[data-chain-id="c1"] [data-chain-glyph="link"]')).not.toBeNull();
    expect(host.querySelector('[data-chain-id="c3"] [data-chain-glyph="code"]')).not.toBeNull();
    expect(host.querySelector('[data-chain-id="c4"] [data-chain-glyph="code"]')).not.toBeNull();
    expect(host.querySelector('[data-chain-id="c2"] [data-chain-agent-state]')).toBeNull();
    expect(host.querySelector('[data-chain-id="c3"] [data-chain-agent-state]')).toBeNull();
    expect(host.querySelector('[data-chain-id="c4"] [data-chain-agent-state]')?.textContent).toBe("完成未看");
    await press("收起");
    expect(ids()).toEqual(["c5", "c6", "c7"]);
  });
  it("uses trimmed reported detail with full expansion, falls back to legacy text, and keeps other details expandable", async () => {
    const item: InboxItem = {
      ...plain(reply),
      chain: [
        { id: "reported-detail", kind: "reported", text: "Assistant says: duplicated legacy words", detail: "  First line\nSecond line  " },
        { id: "reported-legacy", kind: "reported", text: "Assistant says: legacy words" },
        { id: "other-detail", kind: "triaged", text: "Sorted", detail: "The basis" },
      ],
    };
    act(() => root.render(<EventBody item={item} onSource={() => {}} />));
    const row = (id: string) => host.querySelector(`[data-chain-id="${id}"]`)!;
    expect(row("reported-detail").querySelector("button span.truncate")?.textContent).toBe("First line\nSecond line");
    expect(row("reported-detail").textContent).not.toContain("Assistant says: duplicated legacy words");
    expect(row("reported-detail").querySelector("button")!.disabled).toBe(false);
    expect(row("reported-detail").querySelector("button")!.getAttribute("aria-expanded")).toBe("false");
    await act(async () => row("reported-detail").querySelector("button")!.click());
    expect(row("reported-detail").querySelector("[data-chain-detail]")?.textContent).toBe("  First line\nSecond line  ");
    expect(row("reported-legacy").querySelector("button span.truncate")?.textContent).toBe("Assistant says: legacy words");
    expect(row("reported-legacy").querySelector("button")!.disabled).toBe(true);
    const other = row("other-detail");
    expect(other.querySelector("button")!.disabled).toBe(false);
    await act(async () => other.querySelector("button")!.click());
    expect(other.querySelector("[data-chain-detail]")?.textContent).toBe("The basis");
    act(() => root.render(<EventBody item={{ ...item, chain: [{ id: "reported-legacy", kind: "reported", text: "Assistant says: legacy words", detail: " \n " }] }} onSource={() => {}} />));
    expect(row("reported-legacy").querySelector("button span.truncate")?.textContent).toBe("Assistant says: legacy words");
  });
  it("labels each kind's buttons", () => {
    act(() => root.render(<EventBody item={agentSuggestion} onSource={() => {}} />));
    expect(buttons()).toEqual(expect.arrayContaining(["驳回", "修改", "开始"]));
    act(() => root.render(<EventBody item={conclusion} onSource={() => {}} />));
    expect(buttons()).toEqual(expect.arrayContaining(["驳回", "知道了"]));
    expect(buttons()).not.toContain("修改");
    act(() => root.render(<EventBody item={{ ...reply, suggestion: { ...reply.suggestion!, handback: true } }} onSource={() => {}} />));
    expect(buttons()).toEqual(expect.arrayContaining(["驳回", "修改", "确认"]));
    expect(host.textContent).toContain("需要你判断");
  });
  it("says 「AI 正在准备建议」 for 急事, why it failed, and what a working one waits for", () => {
    act(() => root.render(<EventBody item={urgent} onSource={() => {}} />));
    expect(host.querySelector("[data-event-preparing]")?.textContent).toContain("AI 正在准备建议");
    expect(host.querySelector("[data-suggestion]")).toBeNull();
    act(() => root.render(<EventBody item={failed} onSource={() => {}} />));
    expect(host.querySelector("[data-event-failed]")?.textContent).toContain("处理超时了");
    expect(buttons()).toContain("再试一次");
    act(() => root.render(<EventBody item={waiting} onSource={() => {}} />));
    expect(host.querySelector('[data-event-handling="waiting"]')?.textContent).toContain("等对方回复");
    expect(host.querySelector("[data-event-related]")?.textContent).toContain("评审材料");
  });
  it("说一句 without a suggestion sends version zero, the words and the item's manager", async () => {
    const item: InboxItem = { ...failed, manager: "m-work", handling: undefined };
    calls.suggestion = () => Promise.resolve({ ...item, handling: { owner: "manager", state: "looking" } });
    act(() => root.render(<EventBody item={item} onSource={() => {}} />));
    expect(buttons()).not.toContain("再试一次");
    await press("说一句");
    const input = host.querySelector<HTMLInputElement>("[data-event-say-input]")!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, "接着看一下");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await press("说一句");
    await flush();
    const sent = calls.list.find((c) => c.path.endsWith("/suggestion"));
    expect(sent?.path).toBe("/inbox/lark:oc_demo6:600/suggestion");
    expect(sent?.json).toMatchObject({ version: 0, action: "instruct", text: "接着看一下" });
    expect(sent?.headers).toEqual({ "X-Persona-Manager": "m-work" });
    expect(host.querySelector("[data-event-say]")).toBeNull();
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["已交给 AI，等它重新出建议", "ok"]);
  });
  it("再试一次 sends the localized default instruction at once when now says failed", async () => {
    const item = { ...failed, handling: undefined, now: "failed" } as InboxItem;
    calls.suggestion = () => Promise.resolve({ ...item, handling: { owner: "manager", state: "looking" } });
    act(() => root.render(<EventBody item={item} onSource={() => {}} />));
    expect(buttons()).toContain("再试一次");
    expect(buttons()).not.toContain("说一句");
    await press("再试一次");
    await flush();
    const sent = calls.list.find((c) => c.path.endsWith("/suggestion"));
    expect(sent?.json).toMatchObject({ version: 0, action: "instruct", text: "请再试一次。" });
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["已交给 AI，等它重新出建议", "ok"]);
  });
  it("a stale instruction refreshes the item and says why", async () => {
    calls.suggestion = () => Promise.reject(new ApiError(409, "建议已更新", { code: "suggestion_stale" }));
    act(() => root.render(<EventBody item={failed} onSource={() => {}} />));
    await press("再试一次");
    await flush();
    expect(calls.list.some((c) => c.path === "/inbox?sources=all")).toBe(true);
    expect(vi.mocked(toast).mock.calls.at(-1)).toEqual(["建议已更新", "info"]);
  });
  it("修改: edits the text and confirms it, or 说一句话 goes back as an instruction", async () => {
    act(() => root.render(<EventBody item={reply} onSource={() => {}} />));
    await press("修改");
    const area = host.querySelector<HTMLTextAreaElement>("[data-suggestion-editor]")!;
    expect(area.value).toBe(reply.suggestion!.text);
    const say = host.querySelector<HTMLInputElement>("[data-suggestion-say]")!;
    const sayButton = () => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "说一句话")!;
    expect(sayButton().disabled).toBe(true);
    // a React-controlled input: set the value through the native setter, then fire input
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(say, "别这么回，先问时间");
      say.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(sayButton().disabled).toBe(false);
    await act(async () => sayButton().click());
    await flush();
    const sent = calls.list.find((c) => c.path.endsWith("/suggestion"));
    expect(sent?.path).toBe("/inbox/lark:oc_demo1:100/suggestion");
    expect(sent?.json).toMatchObject({ version: 2, action: "instruct", text: "别这么回，先问时间" });
    // it did not go through (offline here): the editor stays with the words
    expect(host.querySelector("[data-suggestion-editor]")).not.toBeNull();
  });
  it("a chain entry goes to its source at its position, or opens its messages in place", async () => {
    const onSource = vi.fn();
    act(() => root.render(<EventBody item={reply} onSource={onSource} />));
    const received = host.querySelector('[data-chain-entry="received"]')!;
    await act(async () => received.querySelector("button")!.click());
    expect(onSource).toHaveBeenCalledWith({ position: "100", uid: undefined });
    await act(async () => received.querySelector<HTMLButtonElement>("button[aria-label]")!.click());
    expect(received.querySelector("[data-chain-detail]")?.textContent).toContain("客户催报价了");
    const triaged = host.querySelector('[data-chain-entry="triaged"]')!;
    await act(async () => triaged.querySelector("button")!.click());
    expect(triaged.querySelector("[data-chain-detail]")?.textContent).toBe("需要你 · 私聊直接提问");
  });
  it("opens on the event, 看原文 shows the item's own page, 看处理 goes back", async () => {
    act(() => root.render(<InboxDetail item={plain(reply)} loaded />));
    expect(host.querySelector("[data-event-body]")).not.toBeNull();
    expect(host.textContent).not.toContain(reply.preview);
    await press("看原文");
    expect(host.querySelector("[data-event-body]")).toBeNull();
    expect(host.textContent).toContain(reply.preview);
    await press("看处理");
    expect(host.querySelector("[data-event-body]")).not.toBeNull();
  });
  it("a Feishu item starts collapsed; Original mounts its chat and a chain entry focuses its message (#3262)", async () => {
    const item: InboxItem = { ...reply, lark: { ...reply.lark!, start_position: "102" } };
    const around = () => calls.list.map((c) => c.path).filter((p) => p.includes("around="));
    const reports = () => calls.list.filter((c) => c.method === "POST" && /\/lark\/conversations\/[^/]+\/read(?:\?|$)/.test(c.path));
    act(() => root.render(<InboxDetail item={item} loaded />));
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).toBeNull();
    expect(buttons()).toContain("看原文");
    expect(around()).toEqual([]); // the collapsed chat has not read its conversation
    expect(reports()).toEqual([]);
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]')!.click());
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).not.toBeNull();
    expect(around().at(-1)).toContain("around=102"); // the chat, at the first new message
    const received = host.querySelector('[data-chain-entry="received"]')!;
    await act(async () => received.querySelector("button")!.click());
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).not.toBeNull(); // the page stays expanded
    expect(around().at(-1)).toContain("around=100");
  });
  it("a mail item starts collapsed; Original reads and marks its body, then a chain entry opens that UID (#5471)", async () => {
    const item: InboxItem = { ...agentSuggestion, mail: { ...agentSuggestion.mail!, uids: [88, 89] } };
    const reads = () => calls.list.map((c) => c.path).filter((p) => p.startsWith("/mail/messages/") && !p.endsWith("/flags"));
    const flags = () => calls.list.map((c) => c.path).filter((p) => p.endsWith("/flags"));
    act(() => root.render(<InboxDetail item={item} loaded />));
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).toBeNull();
    expect(buttons()).toContain("看原文");
    expect(reads()).toEqual([]);
    expect(flags()).toEqual([]);
    expect(host.textContent).not.toContain("mail body 89");
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]')!.click());
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).not.toBeNull();
    expect(reads()).toEqual(["/mail/messages/a1/89?mailbox=INBOX"]);
    expect(flags()).toEqual(["/mail/messages/a1/89/flags"]); // an unseen body is really opened, not a seen fixture
    expect(host.textContent).toContain("mail body 89");
    const received = host.querySelector('[data-chain-entry="received"]')!;
    await act(async () => received.querySelector("button")!.click());
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).not.toBeNull();
    expect(reads().at(-1)).toBe("/mail/messages/a1/88?mailbox=INBOX");
    expect(flags().at(-1)).toBe("/mail/messages/a1/88/flags");
    expect(host.textContent).toContain("mail body 88");
    await press("查看最新邮件");
    await flush();
    expect(reads().at(-1)).toBe("/mail/messages/a1/89?mailbox=INBOX");
    expect(host.textContent).toContain("mail body 89");
    expect(buttons()).not.toContain("查看最新邮件");
  });
  it("an older hub's item: no event body, no 看原文, the AI 分身's draft as before", () => {
    const old = renderToStaticMarkup(<InboxDetail item={plain(legacy(reply))} loaded />);
    expect(old).not.toContain("data-event-body");
    expect(old).not.toContain("看原文");
    expect(old).toContain("查看草稿");
    expect(old).toContain(reply.preview);
    // with a suggestion the draft is the suggestion: not offered twice
    expect(renderToStaticMarkup(<InboxDetail item={plain(reply)} loaded />)).not.toContain("查看草稿");
  });
});

describe("收件箱事件：已处理", () => {
  it("uses nowText and the processing-conversation link for a done item, without open-item actions", () => {
    const item = { ...managerDone, now: "done_manager" } as InboxItem;
    act(() => root.render(<EventBody item={item} onSource={() => {}} />));
    const status = host.querySelector("[data-event-now]")!;
    expect(status.textContent).toContain("助手看完，不用管");
    expect(status.textContent).toContain("处理对话");
    expect(host.querySelector("[data-event-manager-done]")).toBeNull();
    expect(buttons()).not.toContain("说一句");
    expect(buttons()).not.toContain("再试一次");
  });
  it("keeps the manager's legacy words and puts its processing conversation on the same line", async () => {
    act(() => root.render(<EventBody item={managerDone} onSource={() => {}} />));
    const status = host.querySelector('[data-event-handling="done"]')!;
    expect(status.textContent).toContain("助手处理了");
    expect(status.textContent).toContain("处理对话");
    expect(host.querySelector("[data-event-manager-done]")).toBeNull();
    expect(host.querySelector("[data-suggestion]")).toBeNull();
    expect(buttons()).not.toContain("说一句");
    expect(buttons()).not.toContain("再试一次");
    const done = host.querySelector('[data-chain-entry="done"]')!;
    expect(done.textContent).toContain("管理者看完，不用管");
    expect(done.querySelector("button")!.disabled).toBe(true); // no link, nothing to expand
    expect(done.querySelector("svg path")?.getAttribute("d")).toBeTruthy();
  });
  it("gives an older done item with a conversation that link alone, and no open-item actions", () => {
    const item = { ...managerDone, id: "old-user-done", done_by: "user" } as InboxItem;
    act(() => root.render(<EventBody item={item} onSource={() => {}} />));
    expect(buttons()).toContain("处理对话");
    expect(host.querySelector("[data-event-handling]")).toBeNull();
    expect(host.querySelector("[data-event-manager-done]")).toBeNull();
    expect(buttons()).not.toContain("说一句");
    expect(buttons()).not.toContain("再试一次");
  });
  it("leaves an older done item without a conversation on its legacy presentation", () => {
    const manager = { ...managerDone, handling: undefined };
    act(() => root.render(<EventBody item={manager} onSource={() => {}} />));
    expect(host.querySelector("[data-event-manager-done]")?.textContent).toBe("助手处理了");
    expect(host.textContent).not.toContain("处理对话");
    const user = { ...manager, id: "old-user-no-conversation", done_by: "user" } as InboxItem;
    act(() => root.render(<EventBody item={user} onSource={() => {}} />));
    expect(host.querySelector("[data-event-manager-done]")).toBeNull();
    expect(host.querySelector("[data-event-handling]")).toBeNull();
    expect(host.textContent).not.toContain("处理对话");
    expect(buttons()).not.toContain("说一句");
    expect(buttons()).not.toContain("再试一次");
  });
  it("a conclusion in 知悉即可 stays listed with its card until 知道了", () => {
    act(() => root.render(<EventBody item={conclusion} onSource={() => {}} />));
    expect(host.querySelector('[data-suggestion="conclusion"]')).not.toBeNull();
  });
});

describe("收件箱事件：列表", () => {
  it("has 进行中 between 需要你 and 知悉即可, marks the rows, and draws a suggestion instead of the draft", async () => {
    calls.items = fixture.items;
    act(() => root.render(<InboxList />));
    await flush();
    const titles = [...host.querySelectorAll("[data-section-title]")].map((s) => s.textContent);
    expect(titles).toEqual(["需要你", "进行中", "知悉即可", "已折叠", "已处理"]);
    const row = (id: string) => host.querySelector(`[data-item="${id}"]`)!;
    expect(row(reply.id).querySelector('[data-event-mark="suggestion"]')?.textContent).toContain("建议回复");
    expect(row(reply.id).textContent).not.toContain("AI 分身草拟的回复"); // the mirrored draft is not drawn too
    expect(row(urgent.id).querySelector('[data-event-mark="preparing"]')?.textContent).toContain("AI 正在准备建议");
    expect(row(waiting.id).querySelector('[data-event-mark="working"]')?.textContent).toBe("等对方回复");
    expect(row(failed.id).querySelector('[data-event-mark="failed"]')).not.toBeNull();
    expect(row(managerDone.id).querySelector('[data-event-mark="manager-done"]')?.textContent).toBe("助手处理了");
    expect(row(conclusion.id).closest("[data-section]")?.getAttribute("data-section")).toBe("fyi");
    // In progress never folds: no toggle on its title, its rows always shown
    const working = host.querySelector('[data-section="working"]')!;
    expect(working.querySelector("[aria-expanded]")).toBeNull();
    expect(working.querySelector(`[data-item="${waiting.id}"]`)).not.toBeNull();
    expect(host.querySelector('[data-section="action"] [aria-expanded]')).not.toBeNull();
  });
  it("draws the five shared showcase rows with their state words and icons", async () => {
    calls.items = showcase.items;
    act(() => root.render(<InboxList />));
    await flush();
    const mark = (id: string) => host.querySelector(`[data-item="${id}"] [data-event-mark="now"]`)!;
    expect(mark("sc-1").getAttribute("data-event-icon")).toBe("clock");
    expect(mark("sc-1").textContent).toContain("等张三确认新报价");
    expect(mark("sc-2").getAttribute("data-event-icon")).toBe("needs-you");
    expect(mark("sc-2").textContent).toBe("等你确认回复");
    expect(host.querySelector('[data-item="sc-2"]')?.textContent).not.toContain("约，中午见。");
    expect(mark("sc-3").getAttribute("data-event-icon")).toBe("none");
    expect(mark("sc-3").textContent).toBe("知悉即可");
    expect(mark("sc-4").getAttribute("data-event-icon")).toBe("needs-you");
    expect(mark("sc-4").textContent).toBe("看过 3 次还没动静，还要跟吗？");
    expect(mark("sc-5").getAttribute("data-event-icon")).toBe("check");
    expect(mark("sc-5").textContent).toBe("你回过了");
  });
  it("an older hub's list: the four sections and the draft card as before", async () => {
    calls.items = fixture.items.map(legacy);
    act(() => root.render(<InboxList />));
    await flush();
    const titles = [...host.querySelectorAll("[data-section-title]")].map((s) => s.textContent);
    expect(titles).toEqual(["需要你", "知悉即可", "已折叠", "已处理"]);
    expect(host.querySelector("[data-event-mark]")).toBeNull();
    expect(host.querySelector(`[data-item="${reply.id}"]`)?.textContent).toContain("AI 分身草拟的回复");
  });
});
