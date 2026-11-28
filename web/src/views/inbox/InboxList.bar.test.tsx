// @vitest-environment jsdom
// The Inbox's section bar (docs/assistant/inbox.md "Section bar", #9053): it opens on 需要你, each choice shows its
// section alone with its title as a heading, the counts are in their tones and spoken with the names, the arrow keys
// move along it, a search shows 全部 and clearing it goes back, a choice with nothing in it says so in its own words,
// and the grouping is its trailing menu. The rules themselves are replayed from spec/fixtures/inbox (lib/inbox-bar.test.ts).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const hub = vi.hoisted(() => ({ items: [] as unknown[], waiting: true }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "a1", email: "me@example.com" }] });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: hub.items });
    return Promise.resolve({});
  },
}));
// an agent waiting on you, counted in 需要你
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  const interactions = [{ id: "ix1", machine_id: "m", session_id: "s1", session_title: "改登录页", kind: "question" }];
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true, interactions: (hub.waiting ? interactions : []) as never })) };
});

import { InboxList } from "./InboxList";
import { barFade } from "./InboxBar";
import { reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { edgeMask } from "../chat/edge-fade";
import { renderToStaticMarkup } from "react-dom/server";
import { Icon } from "../../ui/icons";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, patch: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "lark", kind: "group", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "text", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier: "action", version: `v-${id}`, lark: { feed_id: id, is_group: true }, ...patch,
});
// 需要你: two items (and the agent); 进行中: one; 知悉即可: two; 已折叠: one; 已处理: one
const everySection = () => [
  item("a1", { summary: "合同条款确认" }), item("a2"), item("w1", { status: "working" }), item("f1", { tier: "fyi" }),
  item("f2", { tier: "fyi", summary: "合同的附件" }), item("n1", { tier: "noise" }), item("d1", { done_at: "2026-11-09T05:00:00Z", done_by: "user" }),
];

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const choices = () => [...host.querySelectorAll<HTMLButtonElement>("[data-bar-choice]")];
const choice = (id: string) => host.querySelector<HTMLButtonElement>(`[data-bar-choice="${id}"]`)!;
const chosen = () => choices().find((b) => b.getAttribute("aria-checked") === "true")?.dataset.barChoice;
const sections = () => [...host.querySelectorAll<HTMLElement>("[data-section]")].map((s) => s.dataset.section);
const pick = (id: string) => act(() => choice(id).click());
const key = (el: HTMLElement, k: string) => act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })); });
const typeSearch = async (text: string) => {
  const input = host.querySelector<HTMLInputElement>('input[aria-label="搜索收件箱"]')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await flush();
};
const show = async () => {
  act(() => root.render(<InboxList />));
  await flush();
};

beforeEach(() => {
  hub.items = everySection();
  hub.waiting = true;
  localStorage.clear();
  resetInboxForTests();
  resetMailForTests();
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("the Inbox's section bar (#9053)", () => {
  it("opens on 需要你, with each count in its tone and spoken with its choice's name", async () => {
    await show();
    const bar = host.querySelector('[role="radiogroup"]')!;
    expect(bar.getAttribute("aria-label")).toBe("分段");
    // over the list's scroller, so it stays in sight
    expect(bar.closest("[data-inbox-list-scroll]")).toBeNull();
    // 已折叠 has no choice of its own
    expect(choices().map((b) => b.dataset.barChoice)).toEqual(["action", "working", "fyi", "done", "all"]);
    expect(choices().map((b) => b.textContent)).toEqual(["需要你3", "进行中1", "知悉即可2", "已处理", "全部"]);
    expect(chosen()).toBe("action");
    expect(choices().map((b) => b.tabIndex)).toEqual([0, -1, -1, -1, -1]);
    expect(choices().map((b) => b.getAttribute("aria-label"))).toEqual(["需要你，3", "进行中，1", "知悉即可，2", null, null]);
    // the blue of 需要你 (the agent waiting counts), the neutral tone for the rest; the number is in the name, not read twice
    const badge = (id: string) => choice(id).querySelector<HTMLElement>("[data-count-badge]");
    expect([badge("action")?.dataset.countBadge, badge("working")?.dataset.countBadge, badge("fyi")?.dataset.countBadge]).toEqual(["needs", "neutral", "neutral"]);
    expect(badge("action")?.getAttribute("aria-hidden")).toBe("true");
    expect(badge("done")).toBeNull();
    // only 需要你 is drawn, its title a heading that doesn't fold
    expect(sections()).toEqual(["action"]);
    expect(host.querySelector('[data-section="action"] h3')?.textContent).toContain("需要你");
    expect(host.querySelector('[data-section="action"] > div > button[aria-expanded]')).toBeNull();
  });

  it("shows a choice's section alone with its title's actions, and 全部 stacks them all as before", async () => {
    await show();
    pick("fyi");
    expect(chosen()).toBe("fyi");
    expect(sections()).toEqual(["fyi"]);
    const fyi = host.querySelector('[data-section="fyi"]')!;
    expect(fyi.querySelector("h3")?.textContent).toContain("知悉即可");
    expect([...fyi.querySelectorAll("button")].some((b) => b.textContent === "全部设为已看")).toBe(true);
    expect([...fyi.querySelectorAll("[data-item]")].map((r) => r.getAttribute("data-item"))).toEqual(["f1", "f2"]);

    pick("done");
    expect(sections()).toEqual(["done"]);
    expect(host.querySelector('[data-section="done"] h3')?.textContent).toContain("已处理");
    expect(host.querySelector('[data-section="done"] [data-item="d1"]')?.closest("[inert]")).toBeNull();

    pick("all");
    expect(sections()).toEqual(["action", "working", "fyi", "noise", "done"]);
    const folds = (k: string) => host.querySelector(`[data-section="${k}"] > div > button[aria-expanded]`)?.getAttribute("aria-expanded");
    expect(["action", "working", "fyi", "noise", "done"].map(folds)).toEqual(["true", undefined, "true", "false", "false"]);
  });

  it("moves along the bar with the arrow keys, Home and End, choosing as it goes", async () => {
    await show();
    act(() => choice("action").focus());
    await key(choice("action"), "ArrowRight");
    expect(chosen()).toBe("working");
    expect(document.activeElement).toBe(choice("working"));
    expect(choice("working").tabIndex).toBe(0);
    await key(choice("working"), "End");
    expect(chosen()).toBe("all");
    expect(document.activeElement).toBe(choice("all"));
    await key(choice("all"), "Home");
    expect(chosen()).toBe("action");
    await key(choice("action"), "ArrowLeft");
    expect(chosen()).toBe("all");
    expect(sections()).toEqual(["action", "working", "fyi", "noise", "done"]);
  });

  it("searches the whole Inbox: its text shows 全部 with the matches' counts, a choice narrows, clearing goes back", async () => {
    await show();
    pick("fyi");
    await typeSearch("合同");
    expect(chosen()).toBe("all");
    expect(choices().map((b) => b.textContent)).toEqual(["需要你1", "知悉即可1", "全部"]);
    expect(sections()).toEqual(["action", "fyi"]);
    pick("action");
    expect(sections()).toEqual(["action"]);
    await typeSearch("");
    expect(chosen()).toBe("fyi");
    expect(sections()).toEqual(["fyi"]);
  });

  it("says a choice with nothing in it is empty in its own words, not 都处理完了", async () => {
    hub.items = [item("f1", { tier: "fyi" })];
    hub.waiting = false;
    await show();
    expect(chosen()).toBe("action");
    const empty = host.querySelector<HTMLElement>("[data-inbox-section-empty]")!;
    expect(empty.dataset.inboxSectionEmpty).toBe("action");
    // its own words, and under them where the rest are, as the other empty states draw their line
    expect(empty.querySelector("p")?.textContent).toBe("暂无需要你处理的事项");
    expect(empty.querySelector("[data-inbox-empty-hint]")?.textContent).toBe("其余内容可在上方的其他分区中查看。");
    expect(empty.querySelector("[data-inbox-empty-hint]")?.className).toContain("text-ink-3");
    expect(host.textContent).not.toContain("已全部处理完毕");
    expect(host.querySelector(".bg-green-tint")).toBeNull();
    // no count at zero, and the rows elsewhere are counted right above
    expect(choices().map((b) => b.textContent)).toEqual(["需要你", "知悉即可1", "全部"]);
  });

  it("keeps a chosen choice while it empties, and says so", async () => {
    await show();
    pick("working");
    hub.items = everySection().map((it) => (it.id === "w1" ? { ...it, done_at: "2026-11-09T06:00:00Z", done_by: "manager" } : it));
    await act(async () => { await reloadInbox(); });
    await flush();
    expect(chosen()).toBe("working");
    expect(choice("working").textContent).toBe("进行中");
    expect(host.querySelector("[data-inbox-section-empty] p")?.textContent).toBe("暂无进行中的事项");
    expect(host.querySelector("[data-inbox-section-empty] [data-inbox-empty-hint]")?.textContent).toBe("其余内容可在上方的其他分区中查看。");
    // left, it goes: no row and not chosen
    pick("action");
    expect(choice("working")).toBeNull();
  });

  it("says what comes to 已处理 under its empty words, not where the rest are", async () => {
    await show();
    pick("done");
    // its one item moved back: nothing done, with the rest counted above
    hub.items = everySection().map((it) => (it.id === "d1" ? { ...it, done_at: undefined, done_by: undefined } : it));
    await act(async () => { await reloadInbox(); });
    await flush();
    expect(chosen()).toBe("done");
    const empty = host.querySelector<HTMLElement>("[data-inbox-section-empty]")!;
    expect(empty.dataset.inboxSectionEmpty).toBe("done");
    expect(empty.querySelector("p")?.textContent).toBe("暂无已处理的事项");
    expect(empty.querySelector("[data-inbox-empty-hint]")?.textContent).toBe("标为已处理的事项会显示在这里。");
  });

  it("says 都处理完了 only when nothing is open anywhere, with 已处理 still a choice", async () => {
    hub.items = [item("d1", { done_at: "2026-11-09T05:00:00Z", done_by: "user" })];
    hub.waiting = false;
    await show();
    expect(host.textContent).toContain("已全部处理完毕");
    expect(host.querySelector("[data-inbox-section-empty]")).toBeNull();
    expect(choices().map((b) => b.dataset.barChoice)).toEqual(["action", "done", "all"]);
    // 全部: the same, with 已处理 under it
    pick("all");
    expect(host.textContent).toContain("已全部处理完毕");
    expect(sections()).toEqual(["done"]);
  });

  // hidden, not greyed (docs/assistant/inbox.md "Section bar"): neither In progress nor Done is grouped by source or time;
  // its place stays, so All doesn't move under the pointer
  it("has the grouping in its trailing menu, hidden (its place kept) where the choice is not grouped", async () => {
    await show();
    const grouping = () => host.querySelector<HTMLButtonElement>('[data-inbox-bar] button[aria-label="分组方式"]')!;
    const hidden = () => {
      const box = grouping().closest("[data-bar-grouping]")!;
      const out = box.hasAttribute("inert");
      expect(box.classList.contains("invisible")).toBe(out);
      return out;
    };
    expect(hidden()).toBe(false);
    expect(grouping().disabled).toBe(false);
    // a menu, said so; its own mark, not the filters'
    expect(grouping().getAttribute("aria-haspopup")).toBe("menu");
    expect(grouping().getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector('[role="radiogroup"][aria-label="分组方式"]')).toBeNull(); // no row of its own
    // the grouping's own mark, not the filters' (DESIGN_SYSTEM 3.4 "List controls")
    const filterMark = renderToStaticMarkup(<Icon name="filter" />).match(/ d="([^"]+)"/)?.[1];
    expect(grouping().querySelector("path")?.getAttribute("d")).toMatch(/^M21 12h-8/);
    expect(grouping().querySelector("path")?.getAttribute("d")).not.toBe(filterMark);
    act(() => grouping().click());
    expect(grouping().getAttribute("aria-expanded")).toBe("true");
    act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    pick("done");
    expect(hidden()).toBe(true);
    pick("working");
    expect(hidden()).toBe(true);
    pick("fyi");
    expect(hidden()).toBe(false);
    pick("all");
    expect(hidden()).toBe(false);
  });

  // blank text in the field searches nothing (the rows don't narrow), so the bar doesn't take it as a search either
  it("takes blank search text as no search", async () => {
    hub.items = [];
    hub.waiting = false;
    await show();
    await typeSearch("   ");
    expect(chosen()).toBe("action");
    expect(host.querySelector("[data-inbox-filtered-empty]")).toBeNull();
    expect(host.textContent).toContain("已全部处理完毕");
  });

  // a row a few px too long keeps what is in sight legible; a pill half out still reads as going on
  it("fades the sections' row over as much as is past each edge, up to the usual fade", () => {
    expect(barFade(0, 0)).toBeUndefined();
    expect(barFade(0, 3)).toBe("linear-gradient(90deg,transparent,#000 0px,#000 calc(100% - 3px),transparent)");
    expect(barFade(0, 300)).toContain("calc(100% - 44px)");
    expect(barFade(120, 0)).toContain("#000 28px,");
    // at full width, the fade every other sideways row draws (its own copy of the widths)
    expect(barFade(300, 300)).toBe(edgeMask({ left: true, right: true }));
  });

  it("keeps All beside the grouping, out of the row of sections that scrolls sideways", async () => {
    await show();
    const strip = host.querySelector<HTMLElement>("[data-bar-strip]")!;
    expect([...strip.querySelectorAll<HTMLElement>("[data-bar-choice]")].map((b) => b.dataset.barChoice)).toEqual(["action", "working", "fyi", "done"]);
    expect(strip.contains(choice("all"))).toBe(false);
    expect(choice("all").closest('[role="radiogroup"]')).toBe(strip.closest('[role="radiogroup"]'));
    expect(choice("all").textContent).toBe("全部");
    // the arrows go on from the row to All, and End reaches it
    choice("action").focus();
    key(choice("action"), "End");
    expect(chosen()).toBe("all");
    expect(document.activeElement).toBe(choice("all"));
    key(choice("all"), "ArrowRight");
    expect(chosen()).toBe("action");
  });

  it("caps a count at 99+ and still says the exact number", async () => {
    hub.items = Array.from({ length: 120 }, (_, i) => item(`a${i}`));
    await show();
    expect(choice("action").querySelector("[data-count-badge]")?.textContent).toBe("99+");
    expect(choice("action").getAttribute("aria-label")).toBe("需要你，121");
  });
});

describe("the Inbox's expandable filter strip (#16990)", () => {
  const scrollWidthDesc = Object.getOwnPropertyDescriptor(Element.prototype, "scrollWidth");
  const clientWidthDesc = Object.getOwnPropertyDescriptor(Element.prototype, "clientWidth");
  let metrics = { scrollWidth: 0, clientWidth: 0 };
  let resizeCallbacks: Array<{ target: Element; cb: ResizeObserverCallback }> = [];

  const filtersToolbar = () => host.querySelector<HTMLDivElement>('[role="toolbar"][aria-label="筛选"]')!;
  const expandButton = () => host.querySelector<HTMLButtonElement>("[data-expand-filters]");
  const triggerResize = async () => {
    const toolbar = filtersToolbar();
    await act(async () => {
      for (const entry of resizeCallbacks) {
        if (entry.target === toolbar) entry.cb([] as ResizeObserverEntry[], {} as ResizeObserver);
      }
    });
    await flush();
  };

  beforeEach(() => {
    metrics = { scrollWidth: 0, clientWidth: 0 };
    resizeCallbacks = [];
    Object.defineProperty(Element.prototype, "scrollWidth", {
      configurable: true,
      get() {
        if (this.getAttribute("role") === "toolbar" && this.getAttribute("aria-label") === "筛选") return metrics.scrollWidth;
        return scrollWidthDesc?.get ? scrollWidthDesc.get.call(this) : 0;
      },
    });
    Object.defineProperty(Element.prototype, "clientWidth", {
      configurable: true,
      get() {
        if (this.getAttribute("role") === "toolbar" && this.getAttribute("aria-label") === "筛选") return metrics.clientWidth;
        return clientWidthDesc?.get ? clientWidthDesc.get.call(this) : 0;
      },
    });
    class FakeResizeObserver {
      private cb: ResizeObserverCallback;
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb;
      }
      observe(target: Element) {
        resizeCallbacks.push({ target, cb: this.cb });
      }
      unobserve(target: Element) {
        resizeCallbacks = resizeCallbacks.filter((entry) => !(entry.cb === this.cb && entry.target === target));
      }
      disconnect() {
        resizeCallbacks = resizeCallbacks.filter((entry) => entry.cb !== this.cb);
      }
    }
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  });

  afterEach(() => {
    if (scrollWidthDesc) Object.defineProperty(Element.prototype, "scrollWidth", scrollWidthDesc);
    if (clientWidthDesc) Object.defineProperty(Element.prototype, "clientWidth", clientWidthDesc);
    vi.restoreAllMocks();
  });

  it("hides the expand toggle when collapsed and all chips fit without overflow", async () => {
    metrics = { scrollWidth: 280, clientWidth: 360 };
    await show();
    const toolbar = filtersToolbar();
    expect(expandButton()).toBeNull();
    expect(toolbar.className).toContain("flex-nowrap");
    expect(toolbar.className).toContain("overflow-x-auto");
    expect(toolbar.className).not.toContain("flex-wrap");
    expect(toolbar.parentElement?.className).toContain("items-center");
    expect(toolbar.querySelector(".bg-line-strong")).not.toBeNull();
  });

  it("shows the expand toggle when overflowing on mount and tracks ResizeObserver changes", async () => {
    metrics = { scrollWidth: 280, clientWidth: 360 };
    await show();
    expect(expandButton()).toBeNull();

    // Narrowing triggers overflow via ResizeObserver.
    metrics = { scrollWidth: 520, clientWidth: 300 };
    await triggerResize();
    const toggle = expandButton()!;
    const toolbar = filtersToolbar();
    const cleanAll = host.querySelector<HTMLButtonElement>("[data-clean-all]")!;
    expect(toggle).not.toBeNull();
    expect(toolbar.contains(toggle)).toBe(false);
    expect(toggle.nextElementSibling).toBe(cleanAll);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-controls")).toBe(toolbar.id);
    expect(toggle.getAttribute("aria-label")).toBe("展开筛选");
    expect(toggle.getAttribute("title")).toBe("展开筛选");
    expect(toolbar.style.maskImage || toolbar.getAttribute("style") || "").toContain("linear-gradient");

    // Widening back removes overflow and hides the toggle while collapsed.
    metrics = { scrollWidth: 280, clientWidth: 420 };
    await triggerResize();
    expect(expandButton()).toBeNull();
  });

  it("expands to a wrapped layout without horizontal fade on click and collapses back on second click", async () => {
    metrics = { scrollWidth: 540, clientWidth: 300 };
    await show();
    const toolbar = filtersToolbar();
    const toggle = expandButton()!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toolbar.getAttribute("style") ?? "").toContain("linear-gradient");

    // Expand
    await act(async () => toggle.click());
    await flush();
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("true");
    expect(expandButton()?.getAttribute("aria-label")).toBe("收起筛选");
    expect(expandButton()?.getAttribute("title")).toBe("收起筛选");
    expect(toolbar.className).toContain("flex-wrap");
    expect(toolbar.className).not.toContain("flex-nowrap");
    expect(toolbar.className).not.toContain("overflow-x-auto");
    expect(toolbar.style.maskImage).toBe("");
    expect(toolbar.getAttribute("style") ?? "").toBe("");
    expect(toolbar.parentElement?.className).toContain("items-start");
    expect(toolbar.querySelector(".bg-line-strong")).toBeNull();
    expect(localStorage.getItem("inbox.filters.expanded")).toBe("1");

    // Collapse back while still overflowing
    await act(async () => expandButton()!.click());
    await flush();
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("false");
    expect(expandButton()?.getAttribute("aria-label")).toBe("展开筛选");
    expect(toolbar.className).toContain("flex-nowrap");
    expect(toolbar.className).toContain("overflow-x-auto");
    expect(toolbar.className).not.toContain("flex-wrap");
    expect(toolbar.parentElement?.className).toContain("items-center");
    expect(toolbar.querySelector(".bg-line-strong")).not.toBeNull();
    expect(toolbar.getAttribute("style") ?? "").toContain("linear-gradient");
    expect(localStorage.getItem("inbox.filters.expanded")).toBeNull();

    // Expanding, widening viewport, then collapsing hides the toggle once everything fits
    await act(async () => expandButton()!.click());
    await flush();
    metrics = { scrollWidth: 260, clientWidth: 420 };
    await act(async () => expandButton()!.click());
    await flush();
    expect(expandButton()).toBeNull();
  });

  it("restores the persisted expanded preference on remount and tolerates unavailable localStorage", async () => {
    localStorage.setItem("inbox.filters.expanded", "1");
    metrics = { scrollWidth: 280, clientWidth: 360 };
    await show();
    const toolbar = filtersToolbar();
    expect(toolbar.className).toContain("flex-wrap");
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("true");
    expect(expandButton()?.getAttribute("aria-label")).toBe("收起筛选");

    // Unmount and remount still reads the saved preference.
    act(() => root.unmount());
    root = createRoot(host);
    await show();
    expect(filtersToolbar().className).toContain("flex-wrap");
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("true");

    // Unavailable storage falls back cleanly to in-memory toggle state without throwing.
    act(() => root.unmount());
    root = createRoot(host);
    metrics = { scrollWidth: 520, clientWidth: 280 };
    const origGet = Storage.prototype.getItem;
    const origSet = Storage.prototype.setItem;
    const origRemove = Storage.prototype.removeItem;
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(function (this: Storage, k: string) {
      if (k === "inbox.filters.expanded") throw new Error("storage disabled");
      return origGet.call(this, k);
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, k: string, v: string) {
      if (k === "inbox.filters.expanded") throw new Error("storage disabled");
      return origSet.call(this, k, v);
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(function (this: Storage, k: string) {
      if (k === "inbox.filters.expanded") throw new Error("storage disabled");
      return origRemove.call(this, k);
    });
    await show();
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("false");
    await act(async () => expandButton()!.click());
    await flush();
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("true");
    expect(filtersToolbar().className).toContain("flex-wrap");
  });

  it("selects and clears filters cleanly while expanded", async () => {
    hub.items = [
      item("l1"),
      item("m1", { source: "mail", kind: "mail", account: "a1", account_name: "me@example.com", tier: "action", lark: undefined, mail: { account_id: "a1", uids: [1] } as InboxItem["mail"] }),
    ];
    hub.waiting = false;
    metrics = { scrollWidth: 520, clientWidth: 280 };
    await show();
    await act(async () => expandButton()!.click());
    await flush();

    const toolbar = filtersToolbar();
    expect(toolbar.className).toContain("flex-wrap");
    const chip = (label: string) => [...toolbar.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")].find((b) => b.textContent?.trim() === label)!;

    await act(async () => chip("飞书").click());
    await flush();
    expect(chip("飞书").getAttribute("aria-pressed")).toBe("true");
    expect([...host.querySelectorAll("[data-item]")].map((r) => r.getAttribute("data-item"))).toEqual(["l1"]);
    const clearBtn = host.querySelector<HTMLButtonElement>("[data-clear-filters]")!;
    expect(clearBtn).not.toBeNull();
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("true");

    await act(async () => clearBtn.click());
    await flush();
    expect(host.querySelector("[data-clear-filters]")).toBeNull();
    expect(chip("飞书").getAttribute("aria-pressed")).toBe("false");
    expect(document.activeElement).toBe(chip("飞书"));
    expect([...host.querySelectorAll("[data-item]")].map((r) => r.getAttribute("data-item"))).toEqual(["l1", "m1"]);
    expect(expandButton()?.getAttribute("aria-expanded")).toBe("true");
    expect(toolbar.className).toContain("flex-wrap");
  });
});
