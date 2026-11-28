// @vitest-environment jsdom
// A hand-off conversation's write-back as a card in the chat (#5932), and the "Wrap up" chips over its composer.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FeedItem } from "../../lib/feed";
import { uncertainReplyKey, type InboxItem } from "../../lib/inbox";
import type { InboxSuggestionAttempt } from "../../lib/inbox-events";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const box = vi.hoisted(() => ({ items: [] as unknown[], acted: [] as unknown[][], handed: [] as unknown[][], uncertain: {} as Record<string, InboxSuggestionAttempt> }));
vi.mock("../../lib/inbox", async (load) => {
  const real = await load<typeof import("../../lib/inbox")>();
  const state = () => ({ items: box.items as InboxItem[], history: [], loaded: true, routing: [], sending: [], uncertainReplies: box.uncertain });
  return {
    ...real,
    useInbox: <T,>(select: (s: ReturnType<typeof state>) => T) => select(state()),
    loadRoutedItem: () => undefined,
    suggestionAct: (...a: unknown[]) => {
      box.acted.push(a);
      return Promise.resolve(true);
    },
    handOffToMain: (...a: unknown[]) => {
      box.handed.push(a);
      return Promise.resolve({ machine_id: "server", session_id: "m1" });
    },
  };
});

import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";

import { HandoffCard, WrapUpChips } from "./HandoffCard";

const feed: FeedItem = {
  id: "f1", conversation: "m1", at: "2026-11-13T09:00:00Z", kind: "inbox_suggestion", ref: "lark:f1:100",
  body: { version: 2, kind: "reply", text: "周五上午给你", recap: "示例客户要报价，已定周五", title: "示例客户", source: "lark", account: "飞书" },
};
const open = (patch: Record<string, unknown> = {}) => ({
  id: "lark:f1:100", source: "lark", kind: "dm", title: "示例客户", preview: "", count: 1, tier: "action",
  suggestion: { version: 2, kind: "reply", text: "周五上午给你", conversation: { machine_id: "server", session_id: "m1" } },
  handoffs: [{ machine_id: "server", session_id: "m1", at: "2026-11-13T09:00:00Z" }],
  ...patch,
});

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  resetManagerChoice();
  box.items = [];
  box.acted = [];
  box.handed = [];
  box.uncertain = {};
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const draw = (node: React.ReactNode) => act(() => root.render(node));

describe("the hand-off card", () => {
  it("shows the summary and an editable reply while the item waits for it, and Send confirms the item's suggestion with the edit", async () => {
    box.items = [open()];
    draw(<HandoffCard item={feed} />);
    expect(host.textContent).toContain("示例客户要报价，已定周五");
    expect(host.textContent).toContain("以你的名义回复「示例客户」");
    const area = host.querySelector("textarea")!;
    expect(area.value).toBe("周五上午给你");
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    act(() => {
      set.call(area, "周五上午发你报价");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const send = host.querySelector<HTMLButtonElement>('[data-testid="handoff-card-confirm"]')!;
    expect(send.textContent).toContain("发送");
    await act(async () => send.click());
    expect(box.acted).toHaveLength(1);
    expect(box.acted[0][1]).toBe("confirm");
    expect(box.acted[0][2]).toBe("周五上午发你报价");
  });

  it("says what went out, without Send, once the reply was sent", () => {
    box.items = [open({ done_at: "2026-11-13T10:00:00Z", done_by: "replied", suggestion: undefined, chain: [{ id: "c1", kind: "confirmed", text: "", detail: "周五上午给你" }] })];
    draw(<HandoffCard item={feed} />);
    expect(host.querySelector('[data-testid="handoff-card"]')?.getAttribute("data-state")).toBe("sent");
    expect(host.textContent).toContain("已以你的名义回复");
    expect(host.querySelector("textarea")).toBeNull();
    expect(host.querySelector('[data-testid="handoff-card-confirm"]')).toBeNull();
  });

  it("offers nothing to send once a newer suggestion replaced it", () => {
    box.items = [open({ suggestion: { version: 3, kind: "reply", text: "新的", conversation: { machine_id: "server", session_id: "m1" } } })];
    draw(<HandoffCard item={feed} />);
    expect(host.textContent).toContain("不再是当前建议");
    expect(host.textContent).not.toContain("没有发出");
    expect(host.querySelector('[data-testid="handoff-card"]')?.getAttribute("data-state")).toBe("replaced");
    expect(host.querySelector('[data-testid="handoff-card-confirm"]')).toBeNull();
  });

  it("does not claim a consumed failed reply was not sent when no delivery receipt survives", () => {
    box.items = [open({ suggestion: undefined, handling: { state: "failed" },
      chain: [{ id: "failed-1", kind: "failed", at: "2026-11-13T09:05:00Z", text: "发送失败" }],
    })];
    draw(<HandoffCard item={feed} />);
    expect(host.textContent).not.toContain("没有发出");
    expect(host.querySelector('[data-testid="handoff-card-confirm"]')).toBeNull();
  });

  const attempted = (status: "failed" | "unknown" | "sending" | "sent", extra = {}) => ({ version: 2, status, retryable: status === "failed", text: "实际编辑后确认的回复", at: "2026-11-18T13:00:00Z", conversation: { machine_id: "server", session_id: "m1" }, ...extra });
  it("reloads a failed edited reply as immutable actual words and Retry confirms its existing version", async () => {
    box.items = [open({ suggestion_attempts: [attempted("failed", { retry_after: 4 })] })];
    draw(<HandoffCard item={feed} />);
    expect(host.textContent).toContain("实际编辑后确认的回复");
    expect(host.textContent).toContain("4 秒后重试");
    expect(host.querySelector("textarea")).toBeNull();
    expect(host.querySelector('[data-testid="handoff-card-confirm"]')).toBeNull();
    expect(host.querySelector("time")?.dateTime).toBe("2026-11-18T13:00:00Z");
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="handoff-card-retry"]')!.click());
    expect(box.acted[0].slice(1)).toEqual(["confirm", undefined, undefined, 2]);
  });
  it.each(["unknown", "sending"] as const)("shows the %s receipt without Send, Edit, or Retry", status => {
    box.items = [open({ suggestion_attempts: [attempted(status)] })];
    draw(<HandoffCard item={feed} />);
    expect(host.textContent).toContain("实际编辑后确认的回复");
    expect(host.querySelector("textarea")).toBeNull();
    expect(host.querySelector('[data-testid="handoff-card-confirm"]')).toBeNull();
    expect(host.querySelector('[data-testid="handoff-card-retry"]')).toBeNull();
    expect(host.textContent).toContain(status === "unknown" ? "请先到原会话确认" : "请等待结果");
  });
  it("shows a lost retry as unknown even when its earlier failed receipt remains listed", () => {
    const item = open({ suggestion_attempts: [attempted("failed")] }) as InboxItem;
    box.items = [item];
    box.uncertain[uncertainReplyKey(item, 2)] = attempted("unknown");
    draw(<HandoffCard item={feed} />);
    expect(host.querySelector('[data-testid="handoff-card"]')?.getAttribute("data-state")).toBe("uncertain");
    expect(host.querySelector('[data-testid="handoff-card-retry"]')).toBeNull();
    expect(host.textContent).toContain("请先到原会话确认");
  });
  it("keeps an old failed card's status and time, links to the sole current sendable card", async () => {
    box.items = [open({ suggestion: { version: 3, kind: "reply", text: "合法新回复", conversation: { machine_id: "server", session_id: "m1" } }, suggestion_attempts: [attempted("failed")] })];
    const fresh = { ...feed, id: "new-card", body: { ...feed.body, version: 3, text: "合法新回复" } };
    draw(<><HandoffCard item={feed} /><HandoffCard item={fresh} /></>);
    const cards = host.querySelectorAll<HTMLElement>('[data-testid="handoff-card"]');
    expect(cards[0].dataset.state).toBe("failed");
    expect(cards[0].querySelector("time")).not.toBeNull();
    expect(cards[0].querySelector('[data-testid="handoff-card-retry"]')).toBeNull();
    expect(cards[1].dataset.state).toBe("live");
    expect(host.querySelectorAll('[data-testid="handoff-card-confirm"]')).toHaveLength(1);
    cards[1].scrollIntoView = vi.fn();
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="handoff-card-current"]')!.click());
    expect(document.activeElement).toBe(cards[1]);
    expect(cards[1].scrollIntoView).toHaveBeenCalled();
  });
  it("does not offer Retry for a non-retryable confirmed failure", () => {
    box.items = [open({ suggestion_attempts: [attempted("failed", { retryable: false })] })];
    draw(<HandoffCard item={feed} />);
    expect(host.querySelector('[data-testid="handoff-card-retry"]')).toBeNull();
    expect(host.querySelector('[data-testid="handoff-card-confirm"]')).toBeNull();
  });
  it("uses the actual sent receipt rather than the original proposal", () => {
    box.items = [open({ suggestion: undefined, done_at: "2026-11-18T13:00:00Z", done_by: "replied", suggestion_attempts: [attempted("sent")] })];
    draw(<HandoffCard item={feed} />);
    expect(host.textContent).toContain("实际编辑后确认的回复");
    expect(host.textContent).not.toContain("周五上午给你");
    expect(host.querySelector('[data-testid="handoff-card"]')?.getAttribute("data-state")).toBe("sent");
  });
  it("looks up and focuses only the selected manager's card for an identical item id", async () => {
    chooseManager("other"); // before the roster arrives, the persisted pick already scopes the inbox
    box.items = [open({ manager: "assistant", suggestion_attempts: [attempted("sent", { text: "别的助手" })] }), open({ manager: "other" })];
    draw(<><HandoffCard item={feed} /><WrapUpChips conversation="m1" /></>);
    const card = host.querySelector<HTMLElement>('[data-testid="handoff-card"]')!;
    expect(card.dataset.manager).toBe("other");
    expect(card.dataset.state).toBe("live");
    expect(host.textContent).not.toContain("别的助手");
    card.scrollIntoView = vi.fn();
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="wrap-up-chip"]')!.click());
    expect(document.activeElement).toBe(card);
    expect(box.handed).toHaveLength(0);
  });
  it("offers Got it for a conclusion worth knowing", () => {
    box.items = [open({ suggestion: { version: 2, kind: "conclusion", text: "只是通知", conversation: { machine_id: "server", session_id: "m1" } } })];
    draw(<HandoffCard item={{ ...feed, body: { ...feed.body, kind: "conclusion", outcome: "fyi", text: "只是通知" } }} />);
    expect(host.querySelector("textarea")).toBeNull();
    expect(host.querySelector('[data-testid="handoff-card-confirm"]')?.textContent).toBe("知道了");
  });

  it("shows bound @ mentions in live state and prunes them live when deleted from the draft", () => {
    const mentions = [{ id: "101", name: "Alice" }, { id: "202", name: "Bob" }];
    box.items = [open({ suggestion: { version: 2, kind: "reply", text: "@Alice @Bob please sync", mentions, conversation: { machine_id: "server", session_id: "m1" } } })];
    draw(<HandoffCard item={{ ...feed, body: { ...feed.body, text: "@Alice @Bob please sync", mentions } }} />);
    const bar = () => host.querySelector('[data-testid="bound-mentions"]');
    expect(bar()?.textContent).toContain("将 @：");
    expect(bar()?.textContent).toContain("@Alice");
    expect(bar()?.textContent).toContain("@Bob");

    const area = host.querySelector("textarea")!;
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    act(() => {
      set.call(area, "@Alice please sync, and @Charlie");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(bar()?.textContent).toContain("@Alice");
    expect(bar()?.textContent).not.toContain("@Bob");
    expect(bar()?.textContent).not.toContain("@Charlie");

    // Prefix collision (@AliceSmith) or @Alice only inside inline code must not keep @Alice bound.
    act(() => {
      set.call(area, "@AliceSmith and `@Alice` please sync");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(bar()).toBeNull();
  });

  it("renders Markdown and bound @ mentions once the reply is sent", () => {
    const mentions = [{ id: "101", name: "Alice" }];
    const mdText = "@Alice **Root cause confirmed**: call `fetch_report` and check [docs](https://example.com/doc)";
    box.items = [open({
      suggestion: undefined, done_at: "2026-11-18T13:00:00Z", done_by: "replied",
      suggestion_attempts: [attempted("sent", { text: mdText, mentions })],
    })];
    draw(<HandoffCard item={{ ...feed, body: { ...feed.body, text: mdText, mentions } }} />);
    expect(host.querySelector("strong")?.textContent).toBe("Root cause confirmed");
    expect(host.querySelector("code")?.textContent).toBe("fetch_report");
    const link = host.querySelector<HTMLAnchorElement>("a");
    expect(link?.textContent).toBe("docs");
    expect(link?.getAttribute("href")).toBe("https://example.com/doc");
    const bar = host.querySelector('[data-testid="bound-mentions"]');
    expect(bar?.textContent).toContain("已 @：");
    expect(bar?.textContent).toContain("@Alice");
  });
});

describe("Wrap up chips", () => {
  it("ask the conversation for the final write-back through the item's hand-off", async () => {
    box.items = [open({ suggestion: undefined })];
    draw(<WrapUpChips conversation="m1" />);
    const chip = host.querySelector<HTMLButtonElement>('[data-testid="wrap-up-chip"]')!;
    expect(chip.textContent).toBe("收尾：示例客户");
    await act(async () => chip.click());
    expect(box.handed).toHaveLength(1);
    expect((box.handed[0][0] as InboxItem).id).toBe("lark:f1:100");
    expect(box.handed[0][1]).toBe("请进行收尾：整理最终总结，并将回复写回这条收件箱事项。");
  });

  it("reveals the current pending reply instead of starting another hand-off", async () => {
    box.items = [open()];
    draw(<><HandoffCard item={feed} /><WrapUpChips conversation="m1" /></>);
    const card = host.querySelector<HTMLElement>('[data-testid="handoff-card"]')!;
    card.scrollIntoView = vi.fn();
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="wrap-up-chip"]')!.click());
    expect(box.handed).toHaveLength(0);
    expect(document.activeElement).toBe(card);
    expect(card.scrollIntoView).toHaveBeenCalled();
  });

  it("say a reply waits to be sent, another suggestion waits to be confirmed, and nothing while it is worked on", () => {
    const wait = () => host.querySelector('[data-testid="wrap-up-wait"]')?.textContent ?? null;
    const redraw = () => {
      act(() => root.unmount()); // the chips are memoized on the conversation: start over for each item state
      root = createRoot(host);
      draw(<WrapUpChips conversation="m1" />);
    };
    box.items = [open({ handling: { state: "done" } })];
    redraw();
    expect(wait()).toBe("待发送");
    box.items = [open({ suggestion: { version: 3, kind: "conclusion", text: "已处理" } })];
    redraw();
    expect(wait()).toBe("待确认");
    box.items = [open({ suggestion: undefined, handling: { state: "looking" } })];
    redraw();
    expect(host.querySelector('[data-testid="wrap-up-chip"]')?.textContent).toBe("收尾：示例客户");
    expect(wait()).toBeNull();
  });

  it("does not label an attempted unknown reply as waiting to send", () => {
    box.items = [open({ suggestion_attempts: [{ version: 2, status: "unknown", retryable: false, text: "实际回复", at: "2026-11-18T13:00:00Z" }] })];
    draw(<WrapUpChips conversation="m1" />);
    expect(host.querySelector('[data-testid="wrap-up-wait"]')).toBeNull();
    expect(host.querySelector('[data-testid="wrap-up-chip"]')).not.toBeNull();
  });
  it("are gone once the item is handled with nothing waiting for the owner", () => {
    box.items = [open({ suggestion: undefined, handling: { state: "done" } })];
    draw(<WrapUpChips conversation="m1" />);
    expect(host.querySelector('[data-testid="wrap-up-chip"]')).toBeNull();
  });

  it("are not drawn in a conversation nothing was handed to", () => {
    box.items = [open()];
    draw(<WrapUpChips conversation="m2" />);
    expect(host.querySelector('[data-testid="wrap-up-chip"]')).toBeNull();
  });
});
