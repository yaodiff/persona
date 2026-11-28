// @vitest-environment jsdom
// The unified model panel (#5032) as a person uses it: the list's groups and the row that runs now, picks that wait for
// the chat (Recent records only applied ones), rows that can't be picked and why, the setup flyout (applied at once on
// the model that runs now, kept as an override on another), favorites, the keys (Tab / arrows / Enter / ← / Esc in
// order), typed IDs, a list being read or that failed, and "Configure models".
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PanelProvider, PanelState } from "../../lib/model-panel";
import { t } from "../../i18n";
import type { LauncherQuotaAnswer } from "../../lib/quota";
import { effortTitle } from "../../lib/session-actions";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // a precise pointer: the search takes the focus when the panel opens
  g.matchMedia = (q: string) => ({
    matches: q === "(any-pointer: fine)",
    media: q,
    addEventListener() {},
    removeEventListener() {},
  });
  Element.prototype.scrollIntoView ??= () => {};
});

const navigated = vi.hoisted(() => [] as string[]);
vi.mock("../../lib/router", async (orig) => ({
  ...(await orig<typeof import("../../lib/router")>()),
  navigate: (to: string) => navigated.push(to),
}));

const { ModelPanel } = await import("./ModelPanel");
const { panelStoreKey } = await import("./store");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });
const EFFORTS = ["low", "medium", "high", "xhigh", "max"];

const claude = (over: Partial<PanelProvider> = {}): PanelProvider => ({
  id: "claude",
  name: "Claude",
  engine: "claude",
  typable: true,
  models: [
    {
      id: "opus",
      name: "Opus 5.5",
      resolved: "claude-opus-5-5",
      description: "Most capable",
      efforts: EFFORTS,
      defaultEffort: "high",
      context: 1_000_000,
    },
    {
      id: "sonnet",
      name: "Sonnet 5",
      description: "Everyday",
      efforts: EFFORTS,
      defaultEffort: "medium",
    },
    { id: "vendor/model-x", name: "vendor/model-x", efforts: [], custom: true },
  ],
  ...over,
});
const codex = (over: Partial<PanelProvider> = {}): PanelProvider => ({
  id: "codex",
  name: "Codex",
  engine: "codex",
  models: [
    {
      id: "gpt-5.5",
      name: "GPT 5.5",
      efforts: ["low", "medium", "high"],
      speeds: [{ id: "standard" }, { id: "fast" }],
    },
  ],
  unavailable: "需要新开对话",
  ...over,
});
const baseState: PanelState = {
  current: { provider: "claude", model: "claude-opus-5-5", effort: "xhigh" },
  recommended: ["claude"],
};

let host: HTMLDivElement;
let root: Root;
let anchor: HTMLButtonElement;

beforeEach(() => {
  localStorage.clear();
  navigated.length = 0;
  anchor = document.createElement("button");
  document.body.append(anchor);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
});

interface Opts {
  providers?: PanelProvider[];
  state?: PanelState;
  onPick?: (c: unknown) => Promise<boolean>;
  onApplyCurrent?: (p: { effort?: string; speed?: string }) => Promise<boolean>;
  onReload?: (p: string) => void;
  onForget?: (p: string, m: string) => void;
  quotas?: Readonly<Record<string, LauncherQuotaAnswer>>;
  header?: React.ReactNode;
  held?: boolean;
}

async function open(o: Opts = {}) {
  const onClose = vi.fn();
  const onPick = vi.fn(o.onPick ?? (async () => true));
  const onApplyCurrent = o.onApplyCurrent && vi.fn(o.onApplyCurrent);
  const render = (extra: Partial<Opts> = {}) =>
    root.render(
      <ModelPanel
        anchor={anchor}
        onClose={onClose}
        providers={extra.providers ?? o.providers ?? [claude(), codex()]}
        state={extra.state ?? o.state ?? baseState}
        scope="agent"
        onPick={onPick}
        onApplyCurrent={onApplyCurrent}
        onReload={o.onReload}
        onForget={o.onForget}
        quotas={o.quotas}
        header={o.header}
        held={extra.held ?? o.held}
      />,
    );
  act(() => render());
  await flush();
  return { onClose, onPick, onApplyCurrent, render };
}

const rows = () => [...document.body.querySelectorAll<HTMLElement>("[data-panel-row]")];
const launcher = (id: string) => document.body.querySelector<HTMLButtonElement>(`[data-panel-launcher="${id}"]`)!;
const row = (text: string) => rows().find((r) => r.textContent?.includes(text))!;
const groups = () =>
  [...document.body.querySelectorAll<HTMLElement>("[data-panel-section]")].map((g) => `${g.getAttribute("aria-label")}: ${[...g.querySelectorAll("[data-panel-row]")].map((r) => r.querySelector(".truncate")?.textContent).join(", ")}`);
const button = (label: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.getAttribute("aria-label") === label || b.textContent?.trim() === label)!;
const key = (el: Element, k: string, init: KeyboardEventInit = {}) =>
  act(() => {
    el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, ...init }));
  });
const flyout = () => document.body.querySelector<HTMLElement>("[data-model-flyout]");
const search = () => document.body.querySelector<HTMLInputElement>("[data-model-panel] input")!;
function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const stored = (kind: "favorites" | "recent" | "overrides") => JSON.parse(localStorage.getItem(panelStoreKey("agent", kind)) ?? "null");

describe("the model panel", () => {
  it("opens only the current launcher and leaves every launcher summary visible", async () => {
    await open();
    expect(groups()).toEqual(["Claude: Opus 5.5, Sonnet 5, vendor/model-x", "Codex: "]);
    expect(row("Opus").getAttribute("aria-pressed")).toBe("true");
    expect(row("Sonnet").getAttribute("aria-pressed")).toBe("false");
    expect(document.activeElement).toBe(search());
    expect(document.body.querySelector("[data-panel-launcher]")).not.toBeNull();
    expect(document.body.querySelector("[data-rail-cell]")).toBeNull();
    expect(launcher("claude").getAttribute("aria-expanded")).toBe("true");
    expect(launcher("codex").getAttribute("aria-expanded")).toBe("false");
    // a folded launcher that can't take a pick says why in its tooltip, not in a line of the list (#12315)
    expect(launcher("codex").getAttribute("aria-description")).toBe("需要新开对话");
    expect(document.body.textContent).not.toContain("需要新开对话");
  });

  it.each(["search", "expanded"] as const)("distinguishes identical model names by their launcher in %s", async (view) => {
    const shared = [{ id: "fable", name: "Fable", efforts: [] }];
    await open({ providers: [claude({ models: shared }), codex({ models: shared, unavailable: undefined })], state: { current: null, recommended: [], starting: true } });
    if (view === "search") type(search(), "Fable");
    else {
      act(() => launcher("claude").click());
      act(() => launcher("codex").click());
    }
    const duplicates = rows().filter((r) => r.textContent?.includes("Fable"));
    expect(duplicates).toHaveLength(2);
    expect(duplicates[0].textContent).toContain("Claude");
    expect(duplicates[1].textContent).toContain("Codex");
  });

  it("draws each provider's Agent mark once, first: on its launcher summary and at the start of its rows, never a letter (#5497)", async () => {
    await open({ providers: [claude(), codex(), { ...codex(), id: "dsh", name: "dsh", engine: "dsh" }] });
    const mark = (el: Element) => el.querySelector("[data-agent-mark]")?.getAttribute("data-agent-mark");
    expect(["claude", "codex", "dsh"].map((p) => mark(launcher(p)))).toEqual(["claude", "codex", "dsh"]);
    expect(row("Opus").querySelectorAll("[data-agent-mark]")).toHaveLength(1);
    expect(mark(row("Opus").firstElementChild!)).toBe("claude");
    act(() => launcher("codex").click());
    expect(mark(row("GPT").firstElementChild!)).toBe("codex");
    expect(row("GPT").querySelector("[data-row-setup] [data-agent-mark]")).toBeNull();
    expect(document.body.querySelector('[data-agent-mark="letter"]')).toBeNull();
  });

  it("draws a provider's weekly quota bar and reset tooltip, and no bar when it is unknown", async () => {
    await open({
      quotas: {
        claude: {
          quota: {
            gateway: "g",
            budget: [{ label: "每周预算", used_ratio: 0.62 }],
            pools: [
              {
                name: "b",
                windows: [
                  {
                    label: "每周",
                    used_ratio: 0.77,
                    resets_at: "2099-01-01T00:00:00Z",
                  },
                ],
              },
            ],
          },
        },
      },
    });
    const cell = document.body.querySelector<HTMLButtonElement>('[data-panel-launcher="claude"]')!;
    expect(cell.querySelector("[data-weekly-remaining]")?.getAttribute("data-weekly-remaining")).toBe("23");
    expect(cell.querySelector("[data-weekly-remaining]")?.className).toContain("bg-orange");
    expect(cell.textContent + cell.querySelector("[aria-label]")?.getAttribute("aria-label")).toContain("每周");
    expect(cell.textContent + cell.querySelector("[aria-label]")?.getAttribute("aria-label")).toContain("重置");
    expect(document.body.querySelector('[data-panel-launcher="codex"] [data-weekly-remaining]')).toBeNull();
  });

  it("uses the accent tone for a healthy weekly quota", async () => {
    await open({
      quotas: {
        claude: {
          quota: {
            gateway: "g",
            budget: [{ label: "weekly", used_ratio: 0.5 }],
          },
        },
      },
    });
    expect(document.body.querySelector('[data-panel-launcher="claude"] [data-weekly-remaining]')?.className).toContain("bg-accent");
  });

  it("shows a known model's price tier and per-million flyout details, with nothing when price is unknown", async () => {
    const priced = claude({
      models: [
        {
          id: "opus",
          name: "Opus 5.5",
          efforts: EFFORTS,
          price: { input: 4, output: 20, cache_read: 0.2 },
        },
        { id: "unknown", name: "Unknown", efforts: [] },
      ],
    });
    await open({
      providers: [priced],
      state: { current: null, recommended: ["claude"] },
    });
    act(() => launcher("claude").click());
    expect(row("Opus").querySelector("[data-price-tier]")?.getAttribute("data-price-tier")).toBe("3");
    expect(row("Unknown").querySelector("[data-price-tier]")).toBeNull();
    act(() => row("Opus").querySelector<HTMLButtonElement>("[data-row-customize]")!.click());
    await flush();
    expect(flyout()!.textContent).toContain("每 100 万 token");
    expect(flyout()!.textContent).toContain("输入 $4");
    expect(flyout()!.textContent).toContain("输出 $20");
    expect(flyout()!.textContent).toContain("缓存读取 $0.2");
  });

  it("a pick waits for the chat: applied, it goes into Recent and the panel closes", async () => {
    const { onPick, onClose } = await open();
    key(row("Sonnet"), "Enter");
    await flush();
    expect(onPick).toHaveBeenCalledWith({
      provider: "claude",
      model: "sonnet",
    });
    expect(onClose).toHaveBeenCalled();
    expect(stored("recent")).toEqual([{ provider: "claude", model: "sonnet" }]);
  });

  it("a refused pick keeps the panel open and records nothing", async () => {
    const { onPick, onClose } = await open({ onPick: async () => false });
    act(() => row("Sonnet").click());
    await flush();
    expect(onPick).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
    expect(stored("recent")).toBeNull();
  });

  it("rows that can't be picked are off and say why; the reason shows once under the provider's header", async () => {
    const { onPick } = await open();
    act(() => launcher("codex").click());
    const gpt = row("GPT");
    expect(gpt.getAttribute("aria-disabled")).toBe("true");
    expect(gpt.title).toBe("需要新开对话");
    expect(document.body.querySelectorAll('[data-panel-section="provider"]')[1]?.textContent).toContain("需要新开对话");
    act(() => gpt.click());
    key(gpt, "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("while the chat is busy every row is off and the reason is the note on top", async () => {
    const { onPick } = await open({
      state: { ...baseState, busy: "Agent 正在工作" },
    });
    expect(document.body.querySelector('[role="note"]')?.textContent).toContain("Agent 正在工作");
    expect(rows().every((r) => r.getAttribute("aria-disabled") === "true")).toBe(true);
    key(row("Sonnet"), "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("while busy the running model's row stays reachable for its flyout, but Enter picks nothing", async () => {
    const { onPick } = await open({
      state: { ...baseState, busy: "Agent 正在工作" },
      onApplyCurrent: async () => true,
    });
    const opus = row("Opus");
    expect(opus.getAttribute("aria-disabled")).toBeNull();
    expect(
      rows()
        .filter((r) => r !== opus)
        .every((r) => r.getAttribute("aria-disabled") === "true"),
    ).toBe(true);
    key(opus, "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("the flyout of the model that runs now applies an effort at once and stays open", async () => {
    const { onApplyCurrent } = await open({ onApplyCurrent: async () => true });
    key(row("Opus"), "ArrowLeft");
    await flush();
    const f = flyout()!;
    expect(f.getAttribute("aria-label")).toContain("Opus 5.5");
    expect(f.textContent).toContain("Claude · 1M 上下文");
    expect(f.textContent).toContain("推荐配置");
    const slider = f.querySelector<HTMLElement>('[role="slider"]')!;
    expect(document.activeElement).toBe(slider);
    expect(slider.getAttribute("aria-valuenow")).toBe("3");
    key(slider, "ArrowRight");
    await flush();
    expect(onApplyCurrent).toHaveBeenCalledWith({ effort: "max" });
    expect(flyout()).not.toBeNull();
    expect(stored("overrides")).toEqual({ "claude\nopus": { effort: "max" } });
  });

  it("the flyout of another model keeps its setup as an override, shown as customized until reset", async () => {
    const { onApplyCurrent } = await open({ onApplyCurrent: async () => true });
    act(() => row("Sonnet").querySelector<HTMLButtonElement>("[data-row-customize]")!.click());
    const slider = flyout()!.querySelector<HTMLElement>('[role="slider"]')!;
    key(slider, "Home");
    await flush();
    expect(onApplyCurrent).not.toHaveBeenCalled();
    expect(stored("overrides")).toEqual({
      "claude\nsonnet": { effort: "low" },
    });
    expect(flyout()!.textContent).toContain("已自定义");
    act(() => button("恢复推荐").click());
    expect(stored("overrides")).toEqual({});
    expect(flyout()!.textContent).toContain("推荐配置");
  });

  it("Codex's current model takes effort and Fast in its flyout though its rows can't be picked", async () => {
    const state: PanelState = {
      current: { provider: "codex", model: "gpt-5.5", effort: "low" },
      recommended: [],
    };
    const { onApplyCurrent, onPick } = await open({
      providers: [codex({ unavailable: "Codex 不能换模型" })],
      state,
      onApplyCurrent: async () => true,
    });
    const gpt = row("GPT");
    // reachable for its flyout, so not announced as disabled; its pick is still off
    expect(gpt.getAttribute("aria-disabled")).toBeNull();
    key(gpt, "Enter");
    await flush();
    expect(onPick).not.toHaveBeenCalled();
    expect(flyout()).not.toBeNull();
    const fast = flyout()!.querySelector<HTMLButtonElement>('[data-panel-speed="fast"]')!;
    expect(fast.getAttribute("aria-pressed")).toBe("false");
    act(() => fast.click());
    await flush();
    expect(onApplyCurrent).toHaveBeenCalledWith({ speed: "fast" });
  });

  it("the current effort-only row is not greyed out and a tap opens its working slider", async () => {
    const state: PanelState = {
      current: { provider: "codex", model: "gpt-5.5", effort: "high" },
      recommended: [],
    };
    const { onApplyCurrent, onPick } = await open({
      providers: [codex()],
      state,
      onApplyCurrent: async () => true,
    });
    const gpt = row("GPT");
    expect(gpt.className).not.toContain("opacity-50");
    act(() => gpt.click());
    const slider = flyout()!.querySelector<HTMLElement>('[role="slider"]')!;
    key(slider, "Home");
    await flush();
    expect(onApplyCurrent).toHaveBeenCalledWith({ effort: "low" });
    expect(onPick).not.toHaveBeenCalled();
  });

  it("keeps actual effort and a visible reason when the session cannot change it", async () => {
    const reason = "This session has no shared control connection.";
    await open({
      providers: [
        codex({
          models: [
            {
              id: "gpt-5.5",
              name: "GPT 5.5",
              efforts: [],
              effortUnavailable: reason,
            },
          ],
        }),
      ],
      state: {
        current: { provider: "codex", model: "gpt-5.5", effort: "max" },
        recommended: [],
      },
      onApplyCurrent: async () => true,
    });
    const gpt = row("GPT");
    expect(gpt.querySelector("[data-row-setup]")!.textContent).toContain(effortTitle("max"));
    expect(gpt.textContent).toContain(reason);
    act(() => gpt.click());
    expect(flyout()!.textContent).toContain(reason);
    expect(flyout()!.querySelector('[role="slider"]')).toBeNull();
  });

  it("held by a dialog about the pick (#5054): stays open under its backdrop, leaving it the clicks, focus and Escape", async () => {
    const { onClose, render } = await open({ held: true });
    const shell = document.body.querySelector<HTMLElement>("[data-model-panel]")!;
    expect(shell.className).toContain("z-30");
    expect(shell.getAttribute("data-held")).toBe("true");
    const outside = document.createElement("button");
    document.body.append(outside);
    act(() => outside.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    act(() => outside.focus());
    key(outside, "Escape");
    expect(onClose).not.toHaveBeenCalled();
    // answered: back above the page, and outside closes it again
    act(() => render({ held: false }));
    expect(shell.className).toContain("z-50");
    act(() => outside.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect(onClose).toHaveBeenCalledTimes(1);
    outside.remove();
  });

  it("while a switch waits, the running model's row is picked back even where its provider's rows are off", async () => {
    const fixed = codex({ unavailable: "Codex 运行中不能换模型" });
    const { onPick } = await open({
      providers: [claude(), fixed],
      state: {
        current: { provider: "codex", model: "gpt-5.5" },
        pending: { provider: "claude", model: "opus" },
        recommended: [],
      },
    });
    const gpt = row("GPT");
    expect(gpt.getAttribute("aria-disabled")).toBeNull();
    key(gpt, "Enter");
    await flush();
    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ provider: "codex", model: "gpt-5.5" }));
  });

  it("Esc closes the flyout first (the focus back on its row), then the panel", async () => {
    const { onClose } = await open();
    const sonnet = row("Sonnet");
    sonnet.focus();
    key(sonnet, "ArrowLeft");
    expect(flyout()).not.toBeNull();
    key(document.activeElement ?? document.body, "Escape");
    expect(flyout()).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(row("Sonnet"));
    key(document.activeElement!, "Escape");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("the star saves a copy of the row's setup once; Favorites lists it, and its star removes it", async () => {
    await open();
    const star = () => document.body.querySelector('[data-panel-section="provider"]')!.querySelectorAll<HTMLButtonElement>('button[aria-label="加入收藏"]')[1];
    act(() => star().click());
    // the row's star is one way: a second press saves nothing new
    act(() => star().click());
    const saved = stored("favorites");
    expect(saved).toHaveLength(1);
    expect(saved[0]).toMatchObject({
      provider: "claude",
      model: "sonnet",
    });
    expect(document.body.querySelector("[data-panel-quick]")?.textContent).toContain("Sonnet 5");
    act(() => document.body.querySelector<HTMLButtonElement>('[data-panel-quick] button[aria-label="取消收藏"]')!.click());
    expect(stored("favorites")).toEqual([]);
    expect(document.body.querySelector("[data-panel-quick]")).toBeNull();
  });

  it("Recent lists the setups applied, most recent first", async () => {
    localStorage.setItem(
      panelStoreKey("agent", "recent"),
      JSON.stringify([
        { provider: "claude", model: "sonnet", effort: "low" },
        { provider: "claude", model: "opus", effort: "xhigh" },
      ]),
    );
    await open();
    expect([...document.body.querySelectorAll("[data-panel-quick] [data-panel-row]")].map((r) => r.querySelector(".truncate")?.textContent)).toEqual(["Claude · Sonnet 5", "Claude · Opus 5.5"]);
    expect(row("Opus").getAttribute("aria-pressed")).toBe("true");
    // a recent row has no flyout
    expect(row("Sonnet").querySelector("[data-row-customize]")).toBeNull();
  });

  it("keys: Tab or ↓ from the search goes to the rows, ↑↓ move, ↑ on the first goes back", async () => {
    await open();
    key(search(), "ArrowDown");
    expect(document.activeElement).toBe(row("Opus"));
    key(row("Opus"), "ArrowDown");
    expect(document.activeElement).toBe(row("Sonnet"));
    key(row("Sonnet"), "ArrowUp");
    key(row("Opus"), "ArrowUp");
    expect(document.activeElement).toBe(launcher("claude"));
    key(launcher("claude"), "ArrowUp");
    expect(document.activeElement).toBe(search());
    key(search(), "Tab");
    expect(document.activeElement).toBe(row("Opus"));
  });

  it("a search lists every provider's matches and offers a typed ID; picking it sends the ID", async () => {
    const { onPick } = await open();
    act(() => button("Codex").click());
    type(search(), "vendor/new-1");
    expect(rows().map((r) => r.textContent)).toEqual([expect.stringContaining("使用「vendor/new-1」")]);
    expect(row("使用「vendor/new-1」").textContent).toContain("Claude · 未在列表中，将直接按输入的 ID 使用");
    key(search(), "Enter");
    await flush();
    expect(onPick).toHaveBeenCalledWith({
      provider: "claude",
      model: "vendor/new-1",
    });
    type(search(), "");
    expect(launcher("codex").getAttribute("aria-expanded")).toBe("true");
  });

  it("an ID typed before is in monospace with a second line and can be forgotten", async () => {
    const onForget = vi.fn();
    await open({ onForget });
    const typed = row("vendor/model-x");
    expect(typed.textContent).toContain("Claude · 历史输入");
    expect(typed.querySelector(".font-mono")).not.toBeNull();
    act(() => typed.querySelector<HTMLButtonElement>('button[aria-label="移除「vendor/model-x」"]')!.click());
    expect(onForget).toHaveBeenCalledWith("claude", "vendor/model-x");
  });

  it("a provider still being read shows a spinner; one that failed says why and offers Retry", async () => {
    const onReload = vi.fn();
    await open({
      providers: [
        claude(),
        codex({ unavailable: undefined, models: null, loading: true }),
        {
          ...codex(),
          id: "cx2",
          name: "Codex 2",
          unavailable: undefined,
          models: null,
          error: "连不上",
        },
      ],
      onReload,
    });
    const sections = [...document.body.querySelectorAll('[data-panel-section="provider"]')];
    act(() => launcher("codex").click());
    expect(sections[1].querySelector('[role="status"]')?.textContent).toContain("正在加载模型");
    expect(sections[2].textContent).toContain("连不上");
    act(() => button("重试").click());
    expect(onReload).toHaveBeenCalledWith("cx2");
  });

  it("Configure models opens Settings › Model providers and closes the panel", async () => {
    const { onClose } = await open();
    act(() => button("配置模型").click());
    expect(onClose).toHaveBeenCalled();
    expect(navigated).toEqual(["/settings?tab=models#settings-providers"]);
  });

  it("a press outside closes it; one on the pill (which toggles it itself) does not", async () => {
    const { onClose } = await open({
      header: <div data-testid="strip">跟随默认</div>,
    });
    expect(document.body.querySelector('[data-testid="strip"]')).not.toBeNull();
    act(() => {
      anchor.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    });
    expect(onClose).not.toHaveBeenCalled();
    act(() => {
      search().dispatchEvent(new Event("pointerdown", { bubbles: true }));
    });
    expect(onClose).not.toHaveBeenCalled();
    act(() => {
      document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps an Agent's records apart from a Persona chat's, under this hub's address", () => {
    expect(panelStoreKey("agent", "favorites")).toBe("persona.modelPanel..agent.favorites");
    expect(panelStoreKey("assistant", "recent", "/hub")).toBe("persona.modelPanel./hub.assistant.recent");
  });
});

describe("launcher-first acceptance #10249", () => {
  const catalog = (n = 7): PanelProvider[] => Array.from({ length: n }, (_, i) => claude({ id: `account-${i}`, name: `Account ${i}`, defaultModel: "Opus 5.5", unavailable: i === 6 ? "Signed out" : undefined }));

  it("keeps seven summaries visible, expands only current, and picks another launcher in two actions", async () => {
    const { onPick } = await open({ providers: catalog(), state: { current: { provider: "account-2", model: "opus" }, recommended: [] } });
    expect(document.body.querySelectorAll("[data-panel-launcher]")).toHaveLength(7);
    expect([...document.body.querySelectorAll('[data-panel-launcher][aria-expanded="true"]')].map((el) => el.getAttribute("data-panel-launcher"))).toEqual(["account-2"]);
    expect(launcher("account-6").getAttribute("aria-description")).toBe("Signed out");
    for (const header of document.body.querySelectorAll<HTMLElement>("[data-panel-launcher]")) {
      // the launcher the chat runs on says "Current" and not its model again (#12315)
      if (header.dataset.panelLauncher === "account-2") {
        expect(header.textContent).toContain("当前");
        expect(header.textContent).not.toContain("Opus 5.5");
      } else expect(header.textContent).toContain("Claude Code · Opus 5.5 · 3 个模型");
    }
    act(() => launcher("account-4").click());
    const wanted = document.body.querySelector<HTMLElement>('[data-panel-row="model:account-4\\a sonnet"]')!;
    act(() => wanted.click());
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "account-4", model: "sonnet" });
  });

  it("without a current launcher starts all twelve collapsed, never hiding a launcher in another bucket", async () => {
    await open({ providers: catalog(12), state: { current: null, recommended: [] } });
    expect(document.body.querySelectorAll("[data-panel-launcher]")).toHaveLength(12);
    expect(document.body.querySelector('[data-panel-launcher][aria-expanded="true"]')).toBeNull();
    expect(rows()).toHaveLength(0);
    act(() => launcher("account-11").click());
    expect(rows()).toHaveLength(3);
  });

  it("search is flat, labels repeated model names with the launcher, and restores folding after clearing", async () => {
    await open({ providers: catalog(), state: { current: null, recommended: [] } });
    type(search(), "Sonnet");
    expect(rows()).toHaveLength(7);
    expect(document.body.querySelector("[data-panel-family]")).toBeNull();
    expect(document.body.querySelector("[data-panel-section]")).toBeNull();
    for (const [i, item] of rows().entries()) expect(item.textContent).toContain(`Account ${i}`);
    type(search(), "Account 4");
    expect(document.body.querySelectorAll("[data-panel-launcher]")).toHaveLength(1);
    expect(launcher("account-4")).not.toBeNull();
    act(() => launcher("account-4").click());
    expect(search().value).toBe("");
    expect(launcher("account-4").getAttribute("aria-expanded")).toBe("true");
  });

  it("search waits for unread launcher lists before claiming there are no matches", async () => {
    const { render } = await open({ providers: [claude(), codex({ models: null, loading: true, unavailable: undefined })] });
    type(search(), "unlisted model");
    expect(document.body.querySelector('[role="status"]')?.textContent).toContain("正在加载模型");
    expect(document.body.textContent).not.toContain("没有匹配的模型");
    act(() => render({ providers: [claude(), codex({ models: [], unavailable: undefined })] }));
    expect(document.body.querySelector('[role="status"]')).toBeNull();
    expect(document.body.textContent).toContain("没有匹配的模型");
  });

  it("search keeps the running model's recorded effort even when its catalog cannot change it", async () => {
    await open({ providers: [codex({ models: [{ id: "gpt-5.5", name: "GPT 5.5", efforts: [] }] })], state: { current: { provider: "codex", model: "gpt-5.5", effort: "max" }, recommended: [] }, onApplyCurrent: async () => true });
    type(search(), "GPT");
    expect(row("GPT").querySelector("[data-row-setup]")!.textContent).toContain(effortTitle("max"));
  });

  it("typed IDs name their launcher and send that exact launcher", async () => {
    const { onPick } = await open({ providers: catalog(2) });
    type(search(), "vendor/new-model");
    const typed = rows().filter((r) => r.dataset.panelRow?.startsWith("typed:"));
    expect(typed).toHaveLength(2);
    expect(typed[1].textContent).toContain("Account 1");
    act(() => typed[1].click());
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "account-1", model: "vendor/new-model" });
  });

  it("opens a favorite saved by the binary speed picker as Fast", async () => {
    localStorage.setItem(panelStoreKey("agent", "favorites"), JSON.stringify([
      { uid: "fast", provider: "codex", model: "gpt-5.5", fast: true },
      { uid: "ultra", provider: "codex", model: "gpt-5.5", speed: "ultrafast", fast: false },
    ]));
    const current = codex({ unavailable: undefined });
    current.models = current.models?.map((model) => ({
      ...model,
      speeds: [{ id: "standard" }, { id: "fast" }, { id: "ultrafast" }],
    })) ?? null;
    const { onPick } = await open({ providers: [current], state: { current: null, recommended: ["codex"] } });
    const choices = [...document.querySelectorAll<HTMLElement>("[data-panel-quick] [data-panel-row]")];
    expect(choices.map((choice) => choice.querySelector("[data-row-speed]")?.getAttribute("aria-label")))
      .toEqual([t("ui-modelpanel.fast_short"), t("ui-modelpanel.speed_ultrafast")]);
    act(() => choices[0].click());
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "codex", model: "gpt-5.5", speed: "fast" });
  });

  it("combines favorites and recent into at most three distinct one-action saved setups", async () => {
    localStorage.setItem(panelStoreKey("agent", "favorites"), JSON.stringify([{ uid: "saved", provider: "claude", model: "sonnet", effort: "low" }]));
    localStorage.setItem(
      panelStoreKey("agent", "recent"),
      JSON.stringify([
        { provider: "claude", model: "sonnet", effort: "low" },
        { provider: "claude", model: "opus" },
        { provider: "codex", model: "gpt-5.5" },
        { provider: "claude", model: "vendor/model-x" },
      ]),
    );
    const { onPick } = await open();
    const quick = [...document.body.querySelectorAll<HTMLElement>("[data-panel-quick] [data-panel-row]")];
    expect(quick).toHaveLength(3);
    expect(quick[0].textContent).toContain("Claude");
    act(() => quick[0].click());
    await flush();
    expect(onPick).toHaveBeenCalledWith({ provider: "claude", model: "sonnet", effort: "low" });
  });
});

describe("a long list", () => {
  const many = (n: number): PanelProvider => ({
    id: "gw",
    name: "Gateway",
    engine: "claude",
    models: Array.from({ length: n }, (_, i) => ({
      id: `gw-model-${i}`,
      name: `Gateway ${i}`,
      efforts: [],
    })),
  });

  it("an expanded launcher keeps its complete list, without a second view or bucket", async () => {
    await open({ providers: [claude(), many(20)] });
    expect(rows().filter((r) => r.textContent?.includes("Gateway "))).toHaveLength(0);
    act(() => launcher("gw").click());
    expect(rows().filter((r) => r.textContent?.includes("Gateway "))).toHaveLength(20);
    expect(document.body.querySelector("[data-panel-more]")).toBeNull();
  });

  it.each(["", "A detailed model"])("a name cut short shows the launcher and whole model ID; others add them only on a touch screen (description: %s)", async (description) => {
    const sw = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollWidth");
    const cw = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
    // a name longer than 20 characters does not fit
    Object.defineProperty(HTMLElement.prototype, "scrollWidth", {
      configurable: true,
      get(this: HTMLElement) {
        return (this.textContent?.length ?? 0) * 10;
      },
    });
    Object.defineProperty(HTMLElement.prototype, "clientWidth", {
      configurable: true,
      get() {
        return 200;
      },
    });
    try {
      const long = "vendor/very-long-model-name-2026-11-01-preview";
      await open({
        providers: [
          claude({
            models: [
              { id: long, name: long, description, efforts: [] },
              { id: "sonnet", name: "Sonnet 5", efforts: [] },
            ],
          }),
          codex(),
        ],
        state: { current: null, recommended: [] },
      });
      act(() => launcher("claude").click());
      const cut = row(long).querySelector<HTMLElement>("[data-row-id]")!;
      expect(cut.dataset.rowId).toBe("always");
      expect(cut.textContent).toBe(`Claude · ${long}`);
      expect(cut.firstElementChild!.className).toContain("line-clamp-2");
      type(search(), "sonnet");
      const short = row("Sonnet 5").querySelector<HTMLElement>("[data-row-id]")!;
      expect(short.dataset.rowId).toBe("touch");
      expect(short.className).toContain("hidden");
      expect(short.textContent).toBe("Claude · sonnet");
    } finally {
      if (sw) Object.defineProperty(HTMLElement.prototype, "scrollWidth", sw);
      if (cw) Object.defineProperty(HTMLElement.prototype, "clientWidth", cw);
    }
  });
});

describe("model families", () => {
  const max = (): PanelProvider => ({
    id: "claude-max",
    name: "claude-max",
    engine: "claude",
    models: [
      {
        id: "default",
        name: "Default (recommended)",
        resolved: "claude-opus-5-5",
        efforts: [],
      },
      {
        id: "opus",
        name: "Opus 5.5",
        resolved: "claude-opus-5-5",
        efforts: [],
      },
      {
        id: "fable",
        name: "Fable 5.2",
        resolved: "claude-fable-5-2",
        efforts: [],
      },
      { id: "claude-fable-5", name: "Fable 5", efforts: [] },
      { id: "claude-opus-4-8", name: "Opus 4.8", efforts: [] },
      { id: "claude-opus-4-7", name: "Opus 4.7", efforts: [] },
    ],
  });
  const names = () => rows().map((r) => r.querySelector(".truncate")?.textContent);
  const fold = (series = "claude") => document.body.querySelector<HTMLButtonElement>(`[data-panel-fold$="${series}"]`)!;

  it("each series under its name, the newest marked Latest; Earlier versions unfolds and folds; a search finds the folded", async () => {
    await open({
      providers: [max(), codex()],
      state: { current: null, recommended: [] },
    });
    act(() => launcher("claude-max").click());
    expect([...document.body.querySelectorAll("[data-panel-family]")].map((f) => f.getAttribute("aria-label"))).toEqual(["Claude"]);
    expect(names()).toEqual(["Default (recommended)", "Fable 5.2", "Opus 5.5"]);
    expect(row("Opus 5.5").querySelector("[data-row-latest]")?.textContent).toBe("最新");
    expect(row("Default (recommended)").querySelector("[data-row-latest]")).toBeNull();

    const claudeFold = fold("claude");
    expect(claudeFold.textContent).toBe("更早版本 · 3");
    expect(claudeFold.getAttribute("aria-label")).toBe("Claude：显示 3 个更早版本");
    expect(claudeFold.getAttribute("aria-expanded")).toBe("false");
    act(() => claudeFold.click());
    expect(names()).toEqual(["Default (recommended)", "Fable 5.2", "Fable 5", "Opus 5.5", "Opus 4.8", "Opus 4.7"]);
    expect(fold("claude").getAttribute("aria-expanded")).toBe("true");
    expect(fold("claude").textContent).toBe("收起更早版本");
    act(() => fold("claude").click());
    expect(names()).not.toContain("Opus 4.8");

    type(search(), "opus 4.7");
    expect(names()).toEqual(["Opus 4.7"]);
    expect(document.body.querySelector("[data-panel-fold]")).toBeNull();
  });

  it("the older model that runs now stays in its series, and ↑↓ walk the rows shown", async () => {
    await open({
      providers: [max(), codex()],
      state: {
        current: { provider: "claude-max", model: "claude-opus-4-7" },
        recommended: [],
        starting: true,
      },
    });
    expect(names()).toEqual(["Default (recommended)", "Fable 5.2", "Opus 5.5", "Opus 4.7"]);
    expect(fold("claude").textContent).toBe("更早版本 · 2");
    const now = row("Opus 4.7");
    expect(now.hasAttribute("data-current")).toBe(true);
    act(() => now.focus());
    await key(now, "ArrowUp");
    expect(document.activeElement).toBe(row("Opus 5.5"));
  });

  it("groups models by series (Orchestrator under Gemini, GPT-OSS under GPT) and puts unrecognized/custom models under Other while keeping Default headerless", async () => {
    const mixed: PanelProvider = {
      id: "agy-mix",
      name: "agy-mix",
      engine: "agy",
      models: [
        { id: "default", name: "Default (recommended)", providerDefault: true, efforts: [] },
        { id: "claude-opus-5-5", name: "Opus 5.5", efforts: [] },
        { id: "claude-opus-4-8", name: "Opus 4.8", efforts: [] },
        { id: "claude-sonnet-5-5", name: "Sonnet 5.5", efforts: [] },
        { id: "gemini-pro-orchestrator-neon", name: "Orchestrator (Neon)", efforts: [] },
        { id: "gpt-oss-120b-medium", name: "GPT-OSS 120B (Medium)", efforts: [] },
        { id: "glm-5.3", name: "GLM 5.3", efforts: [] },
        { id: "acme-custom-1", name: "Acme Custom 1", efforts: [] },
        { id: "vendor/model-x", name: "vendor/model-x", efforts: [], custom: true },
      ],
    };
    await open({
      providers: [mixed, codex()],
      state: { current: null, recommended: [] },
    });
    act(() => launcher("agy-mix").click());

    const familyEls = [...document.body.querySelectorAll<HTMLElement>("[data-panel-family]")];
    expect(familyEls.map((el) => [el.dataset.panelFamily, el.getAttribute("aria-label")])).toEqual([
      ["claude", "Claude"],
      ["gemini", "Gemini"],
      ["gpt", "GPT"],
      ["glm", "GLM"],
      ["other", "其他"],
    ]);

    const otherGroup = familyEls.find((el) => el.dataset.panelFamily === "other")!;
    expect(otherGroup.getAttribute("role")).toBe("group");
    expect(otherGroup.querySelector(".text-submeta")?.textContent).toBe("其他");
    expect(otherGroup.querySelector("[data-panel-fold]")).toBeNull();
    expect([...otherGroup.querySelectorAll("[data-panel-row]")].map((r) => r.querySelector(".truncate")?.textContent)).toEqual([
      "Acme Custom 1",
      "vendor/model-x",
    ]);
    expect(row("Default (recommended)").closest("[data-panel-family]")).toBeNull();

    const glmRow = row("GLM 5.3");
    act(() => glmRow.focus());
    await key(glmRow, "ArrowDown");
    expect(document.activeElement).toBe(row("Acme Custom 1"));
    await key(row("Acme Custom 1"), "ArrowUp");
    expect(document.activeElement).toBe(glmRow);
  });
});
