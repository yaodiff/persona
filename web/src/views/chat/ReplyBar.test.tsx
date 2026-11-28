// @vitest-environment jsdom
// The latest finished reply keeps its full bar. Earlier bars appear on hover or keyboard focus, linger on exit and
// stay with an open ⋯. Copy message turns into ✓ until the next hover.
// SegmentView gives it to a finished turn's last reply only: never narration in a work summary, nor words still coming.
// After the time, what the exchange cost or its tokens (#5426), with cindy's usage card or tooltip.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import type { Segment } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { usageKey } from "../../lib/turn-usage";
import type { ModelTokens } from "../../lib/turn-cost";
import { REPLY_BAR_LINGER_MS } from "../../ui/motion";
import { ChatActionsContext, type ChatActions } from "./chat-actions";
import { MessageActions } from "./MessageActions";
import { LatestReplyId } from "./latest-message-time";
import { forgetPrices } from "../../lib/turn-cost";
import { Answer, SegmentView } from "./SegmentView";
import { StepView, type WorkProps } from "./WorkGroup";

const copied = vi.hoisted(() => vi.fn(async (..._: unknown[]) => true));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), copyText: copied }));
// the hub's catalog: Opus and Sonnet have prices, Haiku none (server/internal/assistant/models_catalog.json)
const PRICES: Record<string, object> = {
  "claude-opus-5-5": { input: 4, output: 20, cache_read: 0.2, cache_write: 5, cache_write_1h: 8 },
  "claude-sonnet-5-5": { input: 2, output: 10, cache_read: 0.1, cache_write: 2.5 },
};
const catalog = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (original) => ({ ...(await original<typeof import("../../lib/api")>()), api: catalog }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const chat: ChatActions = { draftKey: "m/s", canCompose: true, speaker: "Agent", title: "Fix login", forward: () => {}, fork: () => {}, images: () => [] };
const replyTime = "2026-11-12T09:59:01Z";
let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  await initI18n("zh-Hans");
  copied.mockClear();
  forgetPrices();
  catalog.mockReset();
  catalog.mockImplementation(async (_path: string, init: { json: { ids: string[] } }) => ({ models: init.json.ids.flatMap((id) => (PRICES[id] ? [{ id, price: PRICES[id] }] : [{ id }])) }));
  vi.useFakeTimers({ now: new Date("2026-11-12T10:00:00Z") });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

/** The reply with the bar, once its code is in (it loads after the first screen). */
async function draw({ actions = chat, itemId = "r1#0" as string | null, pinned = false, usage = undefined as string | undefined, usageMs = undefined as number | undefined, ttftMs = undefined as number | undefined } = {}) {
  await act(async () => root.render(
    <ChatActionsContext.Provider value={actions}>
      <LatestReplyId.Provider value={pinned ? itemId ?? undefined : undefined}>
        <MessageActions text="All done." ts={replyTime} itemId={itemId ?? undefined} bar usage={usage} usageMs={usageMs} ttftMs={ttftMs}>
          <p data-message>All done.</p>
        </MessageActions>
      </LatestReplyId.Provider>
    </ChatActionsContext.Provider>,
  ));
  // React tries the lazy bar again on a timer, which the fake clock holds: let it run
  await act(async () => {
    await vi.dynamicImportSettled();
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(host.querySelector('[data-reply-bar="loading"]')).toBeNull();
}

const bar = () => host.querySelector<HTMLElement>("[data-reply-bar]")!;
const shown = () => bar().classList.contains("opacity-100");
const button = (label: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
const labels = () => [...host.querySelectorAll("[data-reply-bar] button")].map((b) => b.getAttribute("aria-label"));
// the bar watches the reply it is in (MessageActions' element)
const enter = () => act(() => void bar().parentElement!.dispatchEvent(new MouseEvent("mouseenter")));
const leave = () => act(() => void bar().parentElement!.dispatchEvent(new MouseEvent("mouseleave")));
const wait = (ms: number) => act(() => void vi.advanceTimersByTime(ms));

describe("the reply bar", () => {
  it("shows Copy, Start a new session, Translate, More and the time at rest on the latest reply", async () => {
    await draw({ pinned: true });
    expect(labels()).toEqual(["复制消息", "开启新会话", "翻译", "更多消息操作"]);
    expect(bar().lastElementChild?.tagName).toBe("TIME");
    expect(bar().querySelector("time")?.textContent).toBe("刚刚");
    for (const control of bar().querySelectorAll("button, time")) {
      expect(control.closest(".opacity-0, .pointer-events-none")).toBeNull();
    }
  });

  it("says chat, not session, in a Persona conversation", async () => {
    await draw({ actions: { ...chat, assistant: true } });
    expect(labels()).toEqual(["复制消息", "开启新对话", "翻译", "更多消息操作"]);
  });

  it("has no Start a new session where the session cannot branch, or for words that are not a line of their own", async () => {
    await draw({ actions: { ...chat, fork: undefined } });
    expect(labels()).toEqual(["复制消息", "翻译", "更多消息操作"]);
    await draw({ itemId: "line12#0" });
    expect(labels()).toEqual(["复制消息", "翻译", "更多消息操作"]);
  });

  it("has no ⋯ with nothing to put in it (a chat that cannot send, words not in the transcript)", async () => {
    await draw({ actions: { ...chat, canCompose: false }, itemId: null });
    expect(labels()).toEqual(["复制消息", "翻译"]);
  });

  it("hides earlier replies' actions and time until hover or keyboard focus, then lingers on pointer exit", async () => {
    await draw();
    expect(shown()).toBe(false);
    expect(bar().querySelector("time")?.closest(".opacity-0")).toBe(bar());
    expect(bar().className).toContain("has-[:focus-visible]:opacity-100");
    enter();
    expect(shown()).toBe(true);
    leave();
    wait(REPLY_BAR_LINGER_MS - 50);
    expect(shown()).toBe(true);
    enter();
    wait(REPLY_BAR_LINGER_MS);
    expect(shown()).toBe(true);
    leave();
    wait(REPLY_BAR_LINGER_MS);
    expect(shown()).toBe(false);
    expect(bar().className).toContain("pointer-events-none");
  });

  it("stays while its ⋯ menu is open, which holds Quote, Forward and Use as context", async () => {
    await draw();
    enter();
    act(() => button("更多消息操作").click());
    expect([...document.querySelectorAll('[role="menuitem"]')].map((item) => item.textContent?.trim())).toEqual(["引用", "转发", "作为上下文"]);
    leave();
    wait(REPLY_BAR_LINGER_MS * 2);
    expect(shown()).toBe(true);
    act(() => button("更多消息操作").click());
    expect(shown()).toBe(false);
  });

  it("copies without a toast and shows ✓ until the pointer comes back", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    try {
      await draw();
      enter();
      await act(async () => button("复制消息").click());
      expect(writeText).toHaveBeenCalledExactlyOnceWith("All done.");
      expect(copied).not.toHaveBeenCalled(); // copyText, which says "Copied"
      const check = () => button("复制消息").innerHTML.includes("M20 6L9 17l-5-5");
      expect(check()).toBe(true);
      leave();
      wait(REPLY_BAR_LINGER_MS * 2);
      expect(check()).toBe(true); // it fades out as it was
      enter();
      expect(check()).toBe(false);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("copies the old way, which says so, without the async clipboard (plain http)", async () => {
    await draw();
    enter();
    await act(async () => button("复制消息").click());
    expect(copied).toHaveBeenCalledExactlyOnceWith("All done.");
    expect(button("复制消息").innerHTML).toContain("M20 6L9 17l-5-5");
  });

  it("says when, the exact time as its tooltip, and keeps no timer while the pointer is elsewhere", async () => {
    await draw();
    const time = () => host.querySelector("time")!;
    expect(time().textContent).toBe("刚刚");
    expect(time().title).toMatch(/^2026-11-12 \d\d:59:01$/);
    wait(REPLY_BAR_LINGER_MS);
    expect(vi.getTimerCount()).toBe(0);
    wait(5 * 60_000);
    expect(time().textContent).toBe("刚刚");
    enter();
    expect(time().textContent).toBe("5 分钟前");
    wait(60_000);
    expect(time().textContent).toBe("6 分钟前");
    leave();
    wait(REPLY_BAR_LINGER_MS);
    expect(vi.getTimerCount()).toBe(0);
  });
});

const tokens = (m: Partial<ModelTokens>): ModelTokens => ({ model: "claude-opus-5-5", in: 0, out: 0, cache_read: 0, cache_write: 0, cache_write_1h: 0, ...m });
// $0.35 of Opus: 2 input, 3,400 output, 1.2M read from the cache, 9,000 written to it
const OPUS_TURN = usageKey({ entries: [tokens({ in: 2, out: 3400, cache_read: 1_200_000, cache_write: 9000 })] });
const pointer = (type: string, el: Element, pointerType = "mouse") => act(() => {
  const e = new MouseEvent(type, { bubbles: true, relatedTarget: document.body });
  Object.defineProperty(e, "pointerType", { value: pointerType });
  el.dispatchEvent(e);
});
const usageText = () => host.querySelector<HTMLButtonElement>("[data-reply-bar] [data-turn-usage]");
const card = () => document.querySelector<HTMLElement>("[data-usage-card]");
const cardLines = () => card()?.textContent?.split("\n");

describe("what the exchange cost, after the time", () => {
  it("says what it is worth, after the time, and opens cindy's card 250 ms into a hover", async () => {
    await draw({ usage: OPUS_TURN, usageMs: 65_000 });
    expect(catalog).toHaveBeenCalledExactlyOnceWith("/launchers/catalog", expect.objectContaining({ method: "POST", json: { ids: ["claude-opus-5-5"] } }));
    expect(bar().lastElementChild).toBe(usageText());
    expect(usageText()?.previousElementSibling?.tagName).toBe("TIME");
    expect(usageText()?.textContent).toBe("价值 $0.35");
    expect(usageText()?.className).toContain("ml-1.5");
    enter();
    pointer("pointerover", usageText()!);
    wait(240);
    expect(card()).toBeNull();
    wait(10);
    expect(card()?.dataset.usageCard).toBe("hover");
    expect(card()?.getAttribute("role")).toBe("dialog");
    expect(cardLines()).toEqual(["本轮明细", "本轮 token 价值：$0.35", "Tokens：共 1.2M · 输入 2 · 输出 3.4k", "缓存拆分：读取 1.2M · 写入 9.0k · 命中率 99.3%", "耗时 1m 5s", "模型：Opus 5.5"]);
    // the pointer crosses to the card and stays: open; it leaves the card: gone 220 ms later
    pointer("pointerout", usageText()!);
    pointer("pointerover", card()!);
    wait(500);
    expect(card()).not.toBeNull();
    pointer("pointerout", card()!);
    wait(210);
    expect(card()).not.toBeNull();
    wait(20);
    expect(card()).toBeNull();
  });

  it("shows the latest reply's usage in the row after the time, including after the pointer leaves", async () => {
    await draw({ pinned: true, usage: OPUS_TURN });
    expect(usageText()?.textContent).toBe("价值 $0.35");
    expect(usageText()?.closest(".opacity-0")).toBeNull();
    expect(usageText()?.previousElementSibling?.tagName).toBe("TIME");
    enter();
    leave();
    wait(REPLY_BAR_LINGER_MS);
    expect(usageText()?.closest(".opacity-0")).toBeNull();
    expect(card()).toBeNull();
  });

  it("keeps the bar while its card is open, pins it with a click, and Esc or a click elsewhere closes it", async () => {
    await draw({ usage: OPUS_TURN });
    enter();
    act(() => usageText()!.click());
    expect(card()?.dataset.usageCard).toBe("pinned");
    expect(usageText()?.getAttribute("aria-expanded")).toBe("true");
    leave();
    pointer("pointerout", usageText()!);
    wait(REPLY_BAR_LINGER_MS * 4);
    expect(card()).not.toBeNull();
    expect(shown()).toBe(true);
    act(() => void document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(card()).toBeNull();
    act(() => usageText()!.click());
    act(() => void document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true })));
    expect(card()).toBeNull();
  });

  it("opens at keyboard focus", async () => {
    await draw({ usage: OPUS_TURN });
    act(() => usageText()!.focus());
    expect(card()?.dataset.usageCard).toBe("hover");
    act(() => usageText()!.blur());
    wait(230);
    expect(card()).toBeNull();
  });

  it("shows each model's cost when the exchange used two, the subagent's included", async () => {
    await draw({ usage: usageKey({ entries: [tokens({ out: 5000 }), tokens({ model: "claude-sonnet-5-5", out: 2000 })] }) });
    expect(usageText()?.textContent).toBe("价值 $0.12");
    act(() => usageText()!.click());
    expect(cardLines()?.slice(0, 5)).toEqual(["本轮明细", "本轮 token 价值：$0.12", "按模型拆分：", "· Opus 5.5 $0.10", "· Sonnet 5.5 $0.02"]);
    expect(cardLines()?.some((l) => l.startsWith("模型："))).toBe(false);
  });

  it("without a price for every model, says the tokens, their lines in a tooltip that cannot be pinned", async () => {
    await draw({ usage: usageKey({ entries: [tokens({ in: 100, out: 23_300 }), tokens({ model: "claude-haiku-5-5", out: 10 })] }) });
    expect(usageText()?.textContent).toBe("23.4k tokens");
    expect(usageText()?.dataset.turnUsage).toBe("tokens");
    enter();
    pointer("pointerover", usageText()!);
    wait(490);
    expect(card()).toBeNull();
    wait(10);
    expect(card()?.getAttribute("role")).toBe("tooltip");
    expect(usageText()?.getAttribute("aria-describedby")).toBe(card()?.id);
    expect(cardLines()).toEqual(["Tokens：共 23.4k · 输入 100 · 输出 23.3k", "缓存拆分：读取 0 · 写入 0 · 命中率 0%", "模型：2 个模型", "本轮暂无法计算费用，仅显示用量。"]);
    pointer("pointerout", usageText()!);
    expect(card()).toBeNull();
    // a click closes a tooltip rather than pinning it
    pointer("pointerover", usageText()!);
    wait(500);
    act(() => usageText()!.click());
    expect(card()).toBeNull();
  });

  it("says the tokens when the catalog cannot be asked, and nothing at all without usage", async () => {
    catalog.mockRejectedValue(new Error("offline"));
    await draw({ usage: OPUS_TURN });
    expect(usageText()?.textContent).toBe("1.2M tokens");
    await draw();
    expect(usageText()).toBeNull();
  });

  it("in a Persona conversation: only the cost in plain words, nothing about tokens, and nothing without a cost", async () => {
    await draw({ actions: { ...chat, assistant: true }, usage: OPUS_TURN, usageMs: 65_000 });
    expect(usageText()?.textContent).toBe("约 $0.35");
    act(() => usageText()!.click());
    expect(card()?.getAttribute("aria-label")).toBe("本条回复的估算费用");
    expect(cardLines()).toEqual(["本条回复费用约 $0.35（按公开价格估算）"]);
    await draw({ actions: { ...chat, assistant: true }, usage: usageKey({ entries: [tokens({ model: "claude-haiku-5-5", out: 10 })] }) });
    expect(usageText()).toBeNull();
  });

  it("includes TTFT in the Agent turn usage card when observed, while keeping Persona cards plain", async () => {
    await draw({ usage: OPUS_TURN, usageMs: 65_000, ttftMs: 420 });
    act(() => usageText()!.click());
    expect(cardLines()).toContain("首字延迟 420 ms");
    act(() => void document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));

    await draw({ actions: { ...chat, assistant: true }, usage: OPUS_TURN, usageMs: 65_000, ttftMs: 420 });
    act(() => usageText()!.click());
    expect(cardLines()).toEqual(["本条回复费用约 $0.35（按公开价格估算）"]);
  });
});

describe("on a touch screen", () => {
  let touchScreen = false;
  beforeEach(() => {
    touchScreen = true;
    vi.stubGlobal("matchMedia", (q: string) => ({ get matches() { return q === "(hover: none)" && touchScreen; }, addEventListener() {}, removeEventListener() {} }));
  });
  afterEach(() => {
    touchScreen = false;
    vi.unstubAllGlobals();
  });

  it("ends the copy + ⋯ row, and a tap opens the card pinned", async () => {
    await draw({ usage: OPUS_TURN });
    await act(async () => {
      await vi.dynamicImportSettled();
      await vi.advanceTimersByTimeAsync(0);
    });
    const row = [...host.querySelectorAll<HTMLElement>("[data-turn-usage]")].find((el) => !el.closest("[data-reply-bar]"));
    expect(row?.textContent).toBe("价值 $0.35");
    expect(row?.parentElement?.lastElementChild).toBe(row);
    expect(row?.className).toContain("touch-hit");
    pointer("pointerover", row!, "touch");
    wait(1000);
    expect(card()).toBeNull();
    act(() => row!.click());
    expect(card()?.dataset.usageCard).toBe("pinned");
    // a second tap on the text lets it go (cindy's popover trigger), as a press anywhere else does
    act(() => row!.click());
    expect(card()).toBeNull();
    act(() => row!.click());
    act(() => void document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true })));
    expect(card()).toBeNull();
  });

  it("holds the tokens' lines on a tap, and lets them go on the next", async () => {
    await draw({ usage: usageKey({ entries: [tokens({ model: "claude-haiku-5-5", out: 1200 })] }) });
    await act(async () => {
      await vi.dynamicImportSettled();
      await vi.advanceTimersByTimeAsync(0);
    });
    const row = [...host.querySelectorAll<HTMLElement>("[data-turn-usage]")].find((el) => !el.closest("[data-reply-bar]"));
    expect(row?.textContent).toBe("1.2k tokens");
    act(() => row!.click());
    expect(card()?.getAttribute("role")).toBe("tooltip");
    act(() => row!.click());
    expect(card()).toBeNull();
  });
});

const T = "2026-11-12T09:00:00Z";
const text = (id: string, seq: number, words: string): Item => ({ id, seq, ts: T, role: "assistant", kind: "text", text: words });
const exchange = (extra: Partial<Segment> = {}): Segment => ({
  id: "u1#0",
  user: { id: "u1#0", text: "Look at it", images: [], contexts: [], ts: T },
  steps: [{ kind: "text", id: "a2#0", item: text("a2#0", 2, "Let me look.") }, { kind: "tool", id: "t3#0", use: { id: "t3#0", seq: 3, role: "assistant", kind: "tool_use", tool_use_id: "c3", tool_name: "Read", tool_input: { file_path: "/a.ts" }, ts: T }, images: [] }],
  answer: text("a4#0", 4, "It is fine."),
  tools: 1,
  thoughts: 0,
  replies: 1,
  folds: [],
  start: T,
  ...extra,
});
const segment = (seg: Segment, live = false, streaming?: string, busy?: boolean) =>
  renderToStaticMarkup(
    <ChatActionsContext.Provider value={chat}>
      <SegmentView seg={seg} live={live} streaming={streaming} busy={busy} mid="m" sid="s" expandFold={async () => {}} />
    </ChatActionsContext.Provider>,
  );
const bars = (html: string) => html.split("data-reply-bar").length - 1;

describe("which replies have the bar", () => {
  it("a finished turn's last reply, under its words", () => {
    const html = segment(exchange());
    expect(bars(html)).toBe(1);
    expect(html.indexOf("It is fine.")).toBeLessThan(html.indexOf("data-reply-bar"));
  });

  it("not narration inside an opened work summary", () => {
    const props: WorkProps = { mid: "m", sid: "s", scope: "t:", expandFold: async () => {}, agentStatus: () => "completed" };
    const html = renderToStaticMarkup(
      <ChatActionsContext.Provider value={chat}>
        <StepView step={{ kind: "text", id: "a2#0", item: text("a2#0", 2, "Let me look.") }} p={props} />
      </ChatActionsContext.Provider>,
    );
    expect(html).toContain("Let me look.");
    expect(html).toContain("group/msg"); // its right-click menu is still there
    expect(bars(html)).toBe(0);
  });

  it("not while the turn runs, nor words still streaming", () => {
    expect(bars(segment(exchange(), true))).toBe(0);
    // drawn finished while a message of yours is on its way, the chat still at work: no bar to come and go
    expect(bars(segment(exchange(), false, undefined, true))).toBe(0);
    expect(bars(segment(exchange({ answer: undefined }), true, "Still writ"))).toBe(0);
    expect(bars(renderToStaticMarkup(<Answer streaming="Still writ" mid="m" sid="s" />))).toBe(0);
  });

  it("a reply cut short says so above its words, once the turn is over", () => {
    const html = segment(exchange({ cut: true }));
    expect(html).toContain(">此回复未完成，内容仅供参考。<");
    expect(html.indexOf("此回复未完成")).toBeLessThan(html.indexOf("It is fine."));
    expect(segment(exchange())).not.toContain("此回复未完成");
    expect(segment(exchange({ cut: true }), true)).not.toContain("此回复未完成");
  });
});
