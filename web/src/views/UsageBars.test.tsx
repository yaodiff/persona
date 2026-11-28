// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { initI18n } from "../i18n";
import { chooseManager } from "../lib/manager-choice";
import { onDraftEvent, type DraftEvent } from "../lib/drafts";
import { timelinePosition } from "../lib/usage-popover";
import { createUsageAliasStore, resetUsageTimelineOpen, resetUsageViewMode, USAGE_TIMELINE_OPEN_KEY, USAGE_VIEW_MODE_KEY } from "../lib/usage-popover-model";
import { PINNED_USAGE_ACCOUNTS_KEY, remainingTone, resetPinnedUsageAccounts, usageWindowPinId, type UsageAccount } from "../lib/usage-windows";
import { UsageBars } from "./UsageBars";
import { compactResetText, computeUsagePopoverLayout, packedColumnHeight, POPOVER_HYSTERESIS_PX, providerHue, UsagePopover, visibleTimelineTickHours } from "./UsagePopover";
import { unconnectedUsageProviders } from "./UsageUnconnected";

const NOW = new Date(2026, 10, 5, 10, 0, 0).getTime();
const H = 3600_000;
const iso = (ms: number) => new Date(NOW + ms).toISOString();
const balanceAccount = (id: string, amount: string, o: Partial<UsageAccount> = {}): UsageAccount => ({
  id,
  provider: "deepseek",
  name: "DeepSeek",
  sampled_at: iso(-12_000),
  windows: [],
  balance: { amount, currency: "CNY" },
  ...o,
});
const account = (id: string, used5: number, used7: number, o: Partial<UsageAccount> = {}): UsageAccount => ({
  id,
  provider: "codex",
  name: `账号 ${id}`,
  sampled_at: iso(-12_000),
  windows: [
    { label: "5h", window_minutes: 300, used_percent: used5, resets_at: iso(3 * H) },
    { label: "7d", window_minutes: 10080, used_percent: used7, resets_at: iso(2 * 24 * H + 3 * H) },
  ],
  ...o,
});

describe("the sidebar's usage strip", () => {
  it("is not drawn without data", () => {
    expect(renderToStaticMarkup(<UsageBars accounts={[]} now={NOW} />)).toBe("");
    // all samples older than three days
    expect(renderToStaticMarkup(<UsageBars accounts={[account("a", 10, 10, { sampled_at: iso(-80 * H) })]} now={NOW} />)).toBe("");
  });
  it("shows the two lowest-headroom accounts with remaining width, remainingTone color, and explicit remaining prefix", () => {
    // a: r=80 (green), b: r=15 (orange), c: r=40 (green), d: r=98 used -> r=2 (red)
    const html = renderToStaticMarkup(<UsageBars accounts={[account("a", 10, 20), account("b", 85, 30), account("c", 50, 60), account("d", 98, 10)]} now={NOW} />);
    expect(html.match(/role="progressbar"/g)).toHaveLength(2);
    expect(html).toContain("账号 d");
    expect(html).toContain("账号 b");
    expect(html).not.toContain("账号 a");
    expect(html).not.toContain("账号 c");
    expect(html).toContain("剩 2%");
    expect(html).toContain("剩 15%");
    expect(html).toMatch(/bg-red[^>]*style="width:2%/);
    expect(html).toMatch(/bg-orange[^>]*style="width:15%/);
    expect(html).toContain('aria-expanded="false"');
  });
  it("ranks protected accounts by headroom h = r - R and localizes collapsed remaining prefixes across zh-Hans, en, ja", async () => {
    const prot = account("prot", 70, 20, { name: "Protected seat" });
    prot.windows[0].protect_percent = 80; // r=30, R=20, h=10 -> orange
    const low = account("low", 85, 10, { name: "Raw low" }); // r=15, R=0, h=15 -> orange
    const comfy = account("ok", 10, 20, { name: "Comfy" }); // r=80, R=0, h=80 -> green
    for (const [lang, wantProt, wantLow] of [
      ["zh-Hans", "剩 30%", "剩 15%"],
      ["en", "30% left", "15% left"],
      ["ja", "残り 30%", "残り 15%"],
    ] as const) {
      await initI18n(lang);
      try {
        const html = renderToStaticMarkup(<UsageBars accounts={[comfy, low, prot]} now={NOW} />);
        expect(html).toContain("Protected seat");
        expect(html).toContain("Raw low");
        expect(html).not.toContain("Comfy");
        expect(html.indexOf("Protected seat")).toBeLessThan(html.indexOf("Raw low"));
        expect(html).toContain(wantProt);
        expect(html).toContain(wantLow);
        expect(html).toMatch(/bg-orange[^>]*style="width:30%/);
      } finally {
        await initI18n("zh-Hans");
      }
    }
  });
  it("shows Antigravity and Codex by default when the hub has reconciled the quota windows", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={[
      account("gemini", 3, 38, { provider: "antigravity", name: "Antigravity · Gemini", machines: ["Computer"], sources: ["External"] }),
      account("claude", 0, 0, { provider: "antigravity", name: "Antigravity · Claude/GPT" }),
      account("pro", 20, 23, { name: "Codex Pro" }),
    ]} now={NOW} />);
    expect(html.match(/role="progressbar"/g)).toHaveLength(2);
    expect(html).toContain("Antigravity · Gemini");
    expect(html).toContain("Codex Pro");
    expect(html).not.toContain("Antigravity · Claude/GPT");
  });
  it("shows a healthy sibling model pool's remaining quota instead of 0% when one model pool is depleted", () => {
    const multiPool: UsageAccount = {
      id: "acct-alpha",
      provider: "antigravity",
      name: "Antigravity",
      sampled_at: iso(-12_000),
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 100, resets_at: iso(2 * H), model: "model-a" },
        { label: "7d", window_minutes: 10080, used_percent: 100, resets_at: iso(48 * H), model: "model-a" },
        { label: "5h", window_minutes: 300, used_percent: 20, resets_at: iso(3 * H), model: "model-b" },
        { label: "7d", window_minutes: 10080, used_percent: 38, resets_at: iso(72 * H), model: "model-b" },
      ],
    };
    const html = renderToStaticMarkup(<UsageBars accounts={[multiPool]} now={NOW} />);
    expect(html).toContain("Antigravity");
    expect(html).toContain("剩 62%");
    expect(html).not.toContain("剩 0%");
    expect(html).toMatch(/bg-green[^>]*style="width:62%/);

    const allDepleted: UsageAccount = {
      ...multiPool,
      id: "acct-empty",
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 100, resets_at: iso(2 * H), model: "model-a" },
        { label: "5h", window_minutes: 300, used_percent: 100, resets_at: iso(3 * H), model: "model-b" },
      ],
    };
    const depletedHtml = renderToStaticMarkup(<UsageBars accounts={[allDepleted]} now={NOW} />);
    expect(depletedHtml).toContain("剩 0%");
    expect(depletedHtml).toMatch(/bg-red[^>]*style="width:0%/);
  });
  it("opens a portaled popover on intent, keeps it open on inside clicks, and plays the close transition before unmounting", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsageBars accounts={[account("a", 10, 20, { problem: "unreachable" })]} now={NOW} />));
      expect(document.querySelector("[role=dialog]")).toBeNull();
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!.click());
      await vi.waitFor(() => expect(document.querySelector("[role=dialog]")?.textContent).toContain("5h"));
      const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
      expect(dialog.parentElement).toBe(document.body);
      expect(dialog.getAttribute("data-state")).toBe("open");
      expect(dialog.textContent).toContain("暂时无法连接网关，当前显示的是上次获取的数据。");

      // Clicking inside the portaled dialog must not dismiss it
      await act(async () => {
        dialog.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
      });
      expect(document.querySelector<HTMLElement>("[role=dialog]")?.getAttribute("data-state")).toBe("open");

      // Toggling the trigger starts the 160ms closing animation and unmounts once animation finishes
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!.click());
      const closing = document.querySelector<HTMLElement>("[data-usage-popover]");
      expect(closing?.getAttribute("data-state")).toBe("closing");
      await vi.waitFor(() => expect(document.querySelector("[role=dialog]")).toBeNull(), { timeout: 500 });
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
  it("marks a stale account", () => {
    expect(renderToStaticMarkup(<UsageBars accounts={[account("a", 10, 20, { stale: true })]} now={NOW} />)).toContain("数据已过期");
  });
  it("marks a kept account stale once its stale_at has passed, though the hub said it was fresh (#1905)", () => {
    const kept = account("a", 10, 20, { stale_at: iso(-60_000) });
    expect(renderToStaticMarkup(<UsageBars accounts={[kept]} now={NOW} />)).toContain("数据已过期");
    expect(renderToStaticMarkup(<UsageBars accounts={[{ ...kept, stale_at: iso(60_000) }]} now={NOW} />)).not.toContain("数据已过期");
  });
});

describe("a prepaid balance in the sidebar (#11825)", () => {
  it("shows the amount and currency beside the two lowest-headroom window accounts", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={[balanceAccount("ds", "12.34"), account("a", 10, 20), account("b", 85, 30), account("c", 50, 60)]} now={NOW} />);
    expect(html).toContain("CNY 12.34");
    expect(html.match(/role="progressbar"/g)).toHaveLength(2); // the balance takes no bar
    expect(html).toContain("账号 b");
    expect(html).toContain("账号 c");
    expect(html).not.toContain("账号 a");
  });
  it("shows a genuine zero as an amount, not as no data", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={[balanceAccount("ds", "0.00")]} now={NOW} />);
    expect(html).toContain("CNY 0.00");
    expect(html).toMatch(/text-orange[^>]*>CNY 0\.00/);
    expect(html).not.toContain("无法读取余额");
  });
  it("shows a failed read as no amount and keeps healthy window accounts", () => {
    const failed = balanceAccount("ds", "12.34", { balance: undefined, problem: "unreachable", stale: true });
    const html = renderToStaticMarkup(<UsageBars accounts={[failed, account("a", 10, 20)]} now={NOW} />);
    expect(html).toContain(">—<");
    expect(html).toContain("账号 a");
    expect(html.match(/role="progressbar"/g)).toHaveLength(1);
    expect(html).toContain("opacity-60");
    expect(renderToStaticMarkup(<UsagePopover accounts={[failed]} now={NOW} />)).toContain("无法读取余额");
  });
  it("shows the amount, the sample age and both kinds of account in the popover (remaining percentages)", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={[balanceAccount("ds", "12.34"), account("a", 70, 10)]} now={NOW} />);
    expect(html).toContain("预付余额");
    expect(html).toContain("CNY 12.34");
    expect(html).toContain("5h");
    expect(html).toContain("30%"); // 100 - 70 = 30% remaining
    expect(html).toContain("90%"); // 100 - 10 = 90% remaining
    expect(html.match(/role="progressbar"/g)).toHaveLength(2); // only the window account's bars
  });
  it("says in the popover why a balance is unavailable or is the last one", () => {
    const never = balanceAccount("ds", "12.34", { balance: undefined, problem: "unreachable" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[never]} now={NOW} />)).toContain("暂时无法连接 DeepSeek");
    const kept = balanceAccount("ds", "12.34", { problem: "unreachable" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[kept]} now={NOW} />)).toContain("上次获取的余额");
    const refused = balanceAccount("ds", "12.34", { balance: undefined, problem: "rejected" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[refused]} now={NOW} />)).toContain("Key");
    expect(renderToStaticMarkup(<UsagePopover accounts={[refused]} now={NOW} />)).not.toContain("上次获取的余额");
    const refusedKept = balanceAccount("ds", "12.34", { problem: "rejected" });
    expect(renderToStaticMarkup(<UsagePopover accounts={[refusedKept]} now={NOW} />)).toContain("上次获取的余额");
  });
});

describe("the popover", () => {
  it("puts local and external Antigravity aliases in one provider card", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={[
      account("local", 3, 38, { provider: "agy", name: "Antigravity" }),
      account("external", 0, 0, { provider: "antigravity", name: "Antigravity · Claude/GPT" }),
    ]} now={NOW} />);
    expect(html.match(/data-usage-provider=/g)).toHaveLength(1);
    expect(html).toContain('data-usage-provider="antigravity" aria-label="Antigravity"');
    expect(html).toContain("Antigravity · Claude/GPT");
  });
  it("lists every window on the remaining-quota scale with remainingTone colors and mirrored expected-remaining pace ticks", () => {
    // 5h: used 85% -> remaining 15% (orange: 5<=h<20), 2h elapsed -> expectedUsed=40% -> expectedRemaining=60%
    // 7d: used 10% -> remaining 90% (green: h>=20), 5d elapsed -> expectedUsed~71% -> expectedRemaining~29%
    // 30d: used 97% -> remaining 3% (red: h<5)
    const a = account("a", 85, 10);
    a.windows.push({ label: "30d", window_minutes: 43200, used_percent: 97, resets_at: iso(10 * 24 * H) });
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    const html = host.innerHTML;
    expect(html).toContain("12秒钟前");
    expect(html).toContain("5h");
    expect(html).toContain("7d");
    expect(html).toContain("15%");
    expect(html).toContain("90%");
    expect(html).toContain("3%");
    expect(html).toContain("13:00 重置");
    expect(html).toContain("2 天 3 小时后重置");

    const row5 = host.querySelector('[data-window-row="5h"]')!;
    const row7 = host.querySelector('[data-window-row="7d"]')!;
    const row30 = host.querySelector('[data-window-row="30d"]')!;
    expect(row5.classList.contains("grid")).toBe(true);
    expect(row5.querySelector("[data-usage-percent]")?.textContent).toBe("15%");
    expect(row5.querySelector("[data-usage-percent]")?.classList.contains("text-orange")).toBe(true);
    expect(row5.querySelector<HTMLElement>('[role="progressbar"] > div')?.classList.contains("bg-orange")).toBe(true);
    expect(row5.querySelector<HTMLElement>('[role="progressbar"] > div')?.style.width).toBe("15%");
    expect(row7.querySelector("[data-usage-percent]")?.textContent).toBe("90%");
    expect(row7.querySelector("[data-usage-percent]")?.classList.contains("text-green")).toBe(true);
    expect(row7.querySelector<HTMLElement>('[role="progressbar"] > div')?.classList.contains("bg-green")).toBe(true);
    expect(row7.querySelector<HTMLElement>('[role="progressbar"] > div')?.style.width).toBe("90%");
    expect(row30.querySelector("[data-usage-percent]")?.textContent).toBe("3%");
    expect(row30.querySelector("[data-usage-percent]")?.classList.contains("text-red")).toBe(true);
    expect(row30.querySelector<HTMLElement>('[role="progressbar"] > div')?.classList.contains("bg-red")).toBe(true);
    expect(row30.querySelector<HTMLElement>('[role="progressbar"] > div')?.style.width).toBe("3%");

    // Expected-pace tick is mirrored to x = 100 - expectedUsed (5h has 40% elapsed -> tick at 60%)
    const tick5 = row5.querySelector<HTMLElement>("[data-pace-tick]")!;
    expect(tick5.style.left).toBe("60%");
    expect(tick5.classList.contains("w-px")).toBe(true);
    expect(tick5.classList.contains("bg-ink-3")).toBe(true);

    // Bar shorter than tick (5h: 15% < 60%) -> running fast ("消耗偏快"); bar longer than tick (7d: 90% > ~29%) -> no prompt
    expect(row5.getAttribute("aria-label")).toContain("消耗偏快");
    expect(row7.getAttribute("aria-label")).not.toContain("消耗偏快");
    expect(row7.getAttribute("aria-label")).not.toContain("用得偏慢");

    // Shared remainingTone thresholds match 20 and 5
    expect([100, 20, 19.9, 5, 4.9, 0].map((r) => remainingTone(r, 0))).toEqual(["green", "green", "orange", "orange", "red", "red"]);
  });
  it("draws no pace hint in the first 5% of a window, keeps focus across pace transitions, and discloses details via Collapse", async () => {
    const a = account("a", 90, 90);
    a.windows[0].resets_at = iso(4.9 * H + 0.05 * H); // 5h window just started
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsagePopover accounts={[a]} now={NOW} />));
      expect(host.querySelectorAll("[data-pace-tick]")).toHaveLength(1);
      const row5 = host.querySelector<HTMLButtonElement>('[data-window-row="5h"]')!;
      const row7 = host.querySelector<HTMLButtonElement>('[data-window-row="7d"]')!;
      expect(row5.tagName).toBe("BUTTON");
      expect(row5.getAttribute("title")).not.toContain("消耗偏快");
      expect(row7.tagName).toBe("BUTTON");
      expect(row7.className).toContain("[@media(pointer:coarse)]:min-h-11");
      expect(row7.querySelector("[data-window-chevron]")).not.toBeNull();
      expect(row7.getAttribute("aria-expanded")).toBe("false");
      expect(row7.getAttribute("aria-label")).toContain("消耗偏快");
      expect(host.querySelector("[data-usage-pace-detail]")).toBeNull();

      row7.focus();
      expect(document.activeElement).toBe(row7);
      const onPace = account("a", 90, 71);
      onPace.windows[0].resets_at = iso(4.9 * H + 0.05 * H);
      await act(async () => root.render(<UsagePopover accounts={[onPace]} now={NOW} />));
      expect(document.activeElement).toBe(row7);

      await act(async () => root.render(<UsagePopover accounts={[a]} now={NOW} />));
      await act(async () => row7.click());
      expect(row7.getAttribute("aria-expanded")).toBe("true");
      const detail = host.querySelector("[data-usage-pace-detail]")!;
      expect(detail.classList.contains("flex-wrap")).toBe(true);
      expect(detail.textContent).toContain("消耗偏快");
      expect(detail.textContent).toContain("2 天 3 小时后重置");

      await act(async () => row7.click());
      expect(row7.getAttribute("aria-expanded")).toBe("false");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
  it("keeps an out-of-date account's remaining numbers and reset with readable neutral percentage, no pace, and no tick (#12411)", () => {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[account("a", 70, 10, { stale: true })]} now={NOW} />);
    const html = host.innerHTML;
    expect(html).toContain("已过期");
    expect(html).toContain("30%");
    expect(html).toContain("2 天 3 小时后重置");
    expect(html).not.toContain("消耗偏快");
    expect(html).not.toContain("data-pace-tick");
    const staleSection = host.querySelector('[data-usage-stale="true"]')!;
    expect(staleSection.classList.contains("opacity-70")).toBe(false);
    const pct = host.querySelector('[data-window-row="5h"] [data-usage-percent]')!;
    expect(pct.classList.contains("text-ink-2")).toBe(true);
    expect(pct.classList.contains("text-green")).toBe(false);
    expect(pct.closest(".opacity-70")).toBeNull();
  });
  it("is a labelled dialog", () => {
    expect(renderToStaticMarkup(<UsagePopover accounts={[account("a", 1, 1)]} now={NOW} />)).toContain('role="dialog" aria-label="订阅额度"');
  });
});

describe("where the numbers came from (#2595)", () => {
  const mixed = (): UsageAccount[] => [
    account("a", 20, 30, { name: "work-claude", plan: "Max", local: true, machines: ["MacBook"], sources: ["Relay"] }),
    account("b", 5, 10, { name: "team-seat", plan: "Plus", sources: ["Relay", "Team pool"] }),
    account("c", 40, 50, { name: "Codex Pro", plan: "pro", local: true }),
    account("d", 1, 2, { name: "team-seat-3", sources: ["Relay"], stale: true, problem: "unreachable" }),
  ];
  it("names the sources of each account in the popover, the computers first", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={mixed()} now={NOW} />);
    expect(html).toContain("MacBook · Relay");
    expect(html).toContain(">电脑<");
    expect(html).toContain("Relay · Team pool");
    expect(html.match(/data-usage-source/g)).toHaveLength(4);
  });
  it("writes the plan beside the name unless the name already says it", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={mixed()} now={NOW} />);
    expect(html).toContain(">Max<");
    expect(html).toContain(">Plus<");
    expect(html).not.toContain(">pro<");
  });
  it("says when the gateway could not be read and these are its last numbers", () => {
    const html = renderToStaticMarkup(<UsagePopover accounts={mixed()} now={NOW} />);
    expect(html).toContain("暂时无法连接网关，当前显示的是上次获取的数据。");
    expect(html.match(/暂时无法连接网关/g)).toHaveLength(1);
  });
  it("says why a gateway's numbers are old: a refused key is not a network problem (#1902)", () => {
    const one = (problem: string) => renderToStaticMarkup(<UsagePopover accounts={[account("r", 1, 2, { name: "seat", sources: ["Relay"], stale: true, problem })]} now={NOW} />);
    expect(one("rejected")).toContain("网关拒绝了当前 Key，请前往「管理界面 → 用量来源」更新");
    expect(one("rejected")).not.toContain("暂时无法连接网关");
    expect(one("failed")).toContain("读取网关数据失败，当前显示的是上次获取的数据。");
    expect(one("error")).toContain("网关刚才未能读取此账号的数据");
    expect(one("something-newer")).not.toContain("text-orange");
    expect(one("something-newer")).toContain("opacity-70");
  });
  it("labels a per-model window by its model with remaining aria-label", () => {
    const a = account("m", 10, 20, { name: "claude-seat", sources: ["Relay"] });
    a.windows.push({ label: "7d", window_minutes: 10080, used_percent: 100, resets_at: iso(2 * 24 * H + 3 * H), model: "Fable" });
    const html = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    expect(html).toContain("data-usage-model");
    expect(html).toContain(">Fable<");
    expect(html).toContain("claude-seat 7d（Fable）：剩余 0%");
    expect(html.match(/role="progressbar"/g)).toHaveLength(3);
  });
  it("puts the sources in the tooltip of a sidebar bar", () => {
    const html = renderToStaticMarkup(<UsageBars accounts={mixed().slice(0, 1)} now={NOW} />);
    expect(html).toContain("来源：MacBook · Relay");
  });
  it.each([
    ["en", "No quota data connected"],
    ["zh-Hans", "未接入额度数据"],
    ["ja", "利用量データ未連携"],
  ])("keeps the launcher row and names its missing quota data in %s", async (lang, label) => {
    await initI18n(lang);
    try {
      const html = renderToStaticMarkup(<UsageBars accounts={[]} unconnected={[{ id: "claude", title: "Claude Code", agent: "claude" }]} now={NOW} />);
      expect(html).toContain("Claude Code");
      expect(html).toContain(label);
    } finally {
      await initI18n("zh-Hans");
    }
  });
  it("shows agy quota windows instead of its missing-data row", () => {
    const live = [account("agy", 25, 75, { provider: "agy", name: "Antigravity", local: true })];
    const unconnected = unconnectedUsageProviders([{ name: "agy", agent: "agy" }], live);
    expect(unconnected).toEqual([]);
    const strip = renderToStaticMarkup(<UsageBars accounts={live} unconnected={unconnected} now={NOW} />);
    expect(strip).toContain("Antigravity");
    expect(strip).toContain("剩 25%");
    expect(strip).not.toContain("未接入额度数据");
  });
  it.each(["agy", "antigravity"])("matches %s usage and visibility for either Antigravity launcher alias", (provider) => {
    const launchers = [
      { name: "native-agy", agent: "agy" },
      { name: "native-antigravity", display_name: "Native quota", agent_type: "antigravity" },
      { name: "relay", agent: "agy", base_url: "https://relay.example.com" },
    ];
    const live = [account("gemini", 3, 38, { provider, name: "Antigravity · Gemini" })];
    expect(unconnectedUsageProviders(launchers, live).map((p) => p.id)).toEqual(["relay"]);
    expect(unconnectedUsageProviders(launchers, [], [provider]).map((p) => p.id)).toEqual(["relay"]);
  });
  it("says in plain words whether a provider is not read yet or can only be read through its relay (#12411)", () => {
    const html = renderToStaticMarkup(
      <UsagePopover
        accounts={[]}
        unconnected={[
          { id: "claude", title: "Claude Code", agent: "claude" },
          { id: "agy", title: "Antigravity", agent: "agy" },
          { id: "deepseek", title: "DeepSeek", agent: "claude" },
        ]}
        now={NOW}
      />,
    );
    expect(html).toContain("尚未读取到 Claude Code 的额度数据");
    expect(html).toContain("尚未读取到 Antigravity 的额度数据");
    expect(html.match(/尚未读取到 Claude Code/g)).toHaveLength(1);
    expect(html).toContain("无法直接读取此服务商的额度");
    expect(html).not.toMatch(/statusLine|网关/);
  });
  it("leaves out a provider the owner hid from the bars, as a row too (#13179)", () => {
    const launchers = [{ name: "claude" }, { name: "codex", agent: "codex" }, { name: "agy", agent: "agy" }, { name: "deepseek", display_name: "DeepSeek", agent: "claude", base_url: "https://relay.example.com" }];
    expect(unconnectedUsageProviders(launchers, [], []).map((p) => p.id)).toEqual(["claude", "codex", "agy", "deepseek"]);
    expect(unconnectedUsageProviders(launchers, [], ["claude"]).map((p) => p.id)).toEqual(["codex", "agy", "deepseek"]);
    expect(unconnectedUsageProviders(launchers, [], ["codex"]).map((p) => p.id)).toEqual(["claude", "agy", "deepseek"]);
    expect(unconnectedUsageProviders(launchers, [], ["deepseek", "agy"]).map((p) => p.id)).toEqual(["claude", "codex"]);
    expect(unconnectedUsageProviders([{ name: "ag", agent: "antigravity" }], [], ["agy"])).toEqual([]);
  });
  it("lists active providers without usage data and hands setup off to the assistant on click", () => {
    const live = [account("c", 40, 75, { provider: "codex", name: "Codex Pro", local: true })];
    const unconnected = unconnectedUsageProviders(
      [
        { name: "claude" },
        { name: "codex", agent: "codex" },
        { name: "agy", agent: "agy" },
      ],
      live,
    );
    expect(unconnected).toEqual([
      { id: "claude", title: "Claude Code", agent: "claude" },
      { id: "agy", title: "Antigravity", agent: "agy" },
    ]);

    const strip = renderToStaticMarkup(<UsageBars accounts={live} unconnected={unconnected} now={NOW} />);
    expect(strip).toContain("Codex Pro");
    expect(strip).toContain("Claude Code");
    expect(strip).toContain("Antigravity");
    expect(strip).toContain("未接入额度数据");

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    const events: DraftEvent[] = [];
    const off = onDraftEvent("persona-new", (e) => events.push(e));
    let closed = false;
    try {
      act(() => root.render(<UsagePopover accounts={live} unconnected={unconnected} now={NOW} onClose={() => { closed = true; }} />));
      expect(host.textContent).toContain("让助手接入");
      act(() => host.querySelector<HTMLButtonElement>("[data-usage-connect=claude]")!.click());
      expect(closed).toBe(true);
      expect(events).toHaveLength(1);
      expect(events[0]).toMatchObject({ type: "replace" });
      expect((events[0] as { text: string }).text).toContain("Claude Code");
    } finally {
      off();
      act(() => root.unmount());
      host.remove();
    }
  });
});

describe("user-selected pinned usage accounts (#12418)", () => {
  afterEach(() => {
    resetPinnedUsageAccounts();
    localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
  });

  const sampleAccounts = (): UsageAccount[] => [
    account("codex", 85, 30, { provider: "codex", name: "Codex Pro" }),
    account("claude", 60, 40, { provider: "claude", name: "Claude Max" }),
    account("agy", 15, 20, { provider: "agy", name: "Antigravity" }),
    balanceAccount("ds", "12.34"),
  ];
  const sampleUnconnected = [{ id: "kimi", title: "Kimi", agent: "claude" }];

  it("pins and unpins accounts from the popover, persists to localStorage, and resets to automatic", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsageBars accounts={sampleAccounts()} unconnected={sampleUnconnected} now={NOW} />));
      const stripBtn = () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!;

      // Default: lowest-headroom 2 window accounts (Codex Pro, Claude Max) + DeepSeek balance + unconnected row
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("Claude Max");
      expect(stripBtn().textContent).not.toContain("Antigravity");
      expect(stripBtn().textContent).toContain("CNY 12.34");
      expect(stripBtn().textContent).toContain("Kimi");

      // Open portaled popover: all 4 accounts listed, none pinned, no reset bar
      await act(async () => stripBtn().click());
      await vi.waitFor(() => expect(document.querySelector("[data-usage-pin=agy]")).not.toBeNull());
      expect(document.querySelector("[data-usage-reset-pins]")).toBeNull();
      expect(document.querySelectorAll("[data-usage-pin]")).toHaveLength(4);
      expect(document.querySelector("[data-usage-pin=agy]")?.getAttribute("aria-pressed")).toBe("false");

      // Pin Antigravity only
      await act(async () => document.querySelector<HTMLButtonElement>("[data-usage-pin=agy]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe('["agy"]');
      expect(document.querySelector("[data-usage-pin=agy]")?.getAttribute("aria-pressed")).toBe("true");
      expect(document.querySelector("[data-usage-reset-pins]")).not.toBeNull();
      expect(stripBtn().textContent).toContain("Antigravity");
      expect(stripBtn().textContent).not.toContain("Codex Pro");
      expect(stripBtn().textContent).not.toContain("Claude Max");
      expect(stripBtn().textContent).toContain("Kimi");
      expect(document.querySelectorAll("[data-usage-pin]")).toHaveLength(4);
      expect(document.querySelectorAll("[data-usage-connect]")).toHaveLength(1);

      // Also pin Codex Pro and DeepSeek balance
      await act(async () => document.querySelector<HTMLButtonElement>("[data-usage-pin=codex]")!.click());
      await act(async () => document.querySelector<HTMLButtonElement>("[data-usage-pin=ds]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe('["agy","codex","ds"]');
      expect(stripBtn().textContent).toContain("Antigravity");
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("CNY 12.34");
      expect(stripBtn().textContent).not.toContain("Claude Max");

      // Unpin Antigravity
      await act(async () => document.querySelector<HTMLButtonElement>("[data-usage-pin=agy]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe('["codex","ds"]');
      expect(stripBtn().textContent).not.toContain("Antigravity");
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("CNY 12.34");

      // Reset to automatic
      await act(async () => document.querySelector<HTMLButtonElement>("[data-usage-reset-pins]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBeNull();
      expect(document.querySelector("[data-usage-reset-pins]")).toBeNull();
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("Claude Max");
      expect(stripBtn().textContent).toContain("CNY 12.34");
      expect(stripBtn().textContent).not.toContain("Antigravity");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("tolerates corrupt localStorage and syncs pinned accounts across tabs via storage events", async () => {
    localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, "{bad-json");
    const corruptHtml = renderToStaticMarkup(<UsageBars accounts={sampleAccounts()} now={NOW} />);
    expect(corruptHtml).toContain("Codex Pro");
    expect(corruptHtml).toContain("Claude Max");
    expect(corruptHtml).not.toContain("Antigravity");

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["agy"]');
      await act(async () => root.render(<UsageBars accounts={sampleAccounts()} now={NOW} />));
      const stripBtn = () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!;
      expect(stripBtn().textContent).toContain("Antigravity");
      expect(stripBtn().textContent).not.toContain("Codex Pro");

      await act(async () => {
        localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["claude"]');
        window.dispatchEvent(new StorageEvent("storage", { key: PINNED_USAGE_ACCOUNTS_KEY, newValue: '["claude"]' }));
      });
      expect(stripBtn().textContent).toContain("Claude Max");
      expect(stripBtn().textContent).not.toContain("Antigravity");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("falls back to automatic ranking when all pinned accounts are offline or expired, and lets the user reset", () => {
    localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["offline-agy"]');
    const strip = renderToStaticMarkup(<UsageBars accounts={sampleAccounts()} now={NOW} />);
    expect(strip).toContain("Codex Pro");
    expect(strip).toContain("Claude Max");
    expect(strip).toContain("CNY 12.34");
    expect(strip).not.toContain("Antigravity");

    const popover = renderToStaticMarkup(<UsagePopover accounts={sampleAccounts()} now={NOW} />);
    expect(popover).toContain("固定项暂不可用");
    expect(popover).toContain("恢复默认");
  });

  it("pins individual usage windows such as agy gemini 7d, preserves multi-window order, falls back when a window disappears, and resets", async () => {
    const agyAccount = (): UsageAccount => ({
      id: "agy",
      provider: "agy",
      name: "Antigravity",
      sampled_at: iso(-12_000),
      local: true,
      machines: ["MacBook"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 88, resets_at: iso(3 * H), model: "Gemini Models" },
        { label: "7d", window_minutes: 10080, used_percent: 25, resets_at: iso(2 * 24 * H), model: "Gemini Models" },
        { label: "7d", window_minutes: 10080, used_percent: 12, resets_at: iso(3 * 24 * H), model: "Claude and GPT models" },
      ],
    });
    const accountsWithAgy = (): UsageAccount[] => [
      account("codex", 85, 30, { provider: "codex", name: "Codex Pro" }),
      agyAccount(),
      balanceAccount("ds", "12.34"),
    ];
    const agy = agyAccount();
    const gemini7dPin = usageWindowPinId("agy", agy.windows[1]);
    const codex7dPin = usageWindowPinId("codex", account("codex", 85, 30).windows[1]);

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsageBars accounts={accountsWithAgy()} now={NOW} />));
      const stripBtn = () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!;

      // Open popover: every UsageWindow row has a pin button alongside account pin buttons
      await act(async () => stripBtn().click());
      await vi.waitFor(() => expect(document.querySelector(`[data-usage-pin-window="${gemini7dPin}"]`)).not.toBeNull());
      expect(document.querySelectorAll("[data-usage-pin]")).toHaveLength(3);
      expect(document.querySelectorAll("[data-usage-pin-window]")).toHaveLength(5);
      expect(document.querySelector(`[data-usage-pin-window="${gemini7dPin}"]`)?.getAttribute("aria-pressed")).toBe("false");

      // Pin Antigravity Gemini Models 7d (25% used -> 75% remaining): collapsed strip renders 75% remaining bar with account+model+7d, NOT 12% tightest
      await act(async () => document.querySelector<HTMLButtonElement>(`[data-usage-pin-window="${gemini7dPin}"]`)!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe(JSON.stringify([gemini7dPin]));
      expect(document.querySelector(`[data-usage-pin-window="${gemini7dPin}"]`)?.getAttribute("aria-pressed")).toBe("true");
      expect(document.querySelector("[data-usage-pinned-bar]")?.textContent).toContain("侧栏已固定 1 项");
      expect(stripBtn().querySelectorAll('[role="progressbar"]')).toHaveLength(1);
      expect(stripBtn().textContent).toContain("Antigravity · Gemini Models");
      expect(stripBtn().textContent).toContain("7d");
      expect(stripBtn().textContent).toContain("剩 75%");
      expect(stripBtn().textContent).not.toContain("12%");
      expect(stripBtn().querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("75");
      expect(stripBtn().querySelector('[role="progressbar"]')?.getAttribute("aria-label")).toContain("Antigravity · Gemini Models 7d");
      expect(stripBtn().querySelector('[role="progressbar"]')?.getAttribute("aria-label")).toContain("剩余 75%");
      expect(stripBtn().querySelector(`[data-usage-window="${gemini7dPin}"]`)?.getAttribute("title")).toContain("MacBook");

      // Also pin DeepSeek balance and Codex Pro 7d (30% used -> 70% remaining): exact pinned order is preserved
      await act(async () => document.querySelector<HTMLButtonElement>('[data-usage-pin="ds"]')!.click());
      await act(async () => document.querySelector<HTMLButtonElement>(`[data-usage-pin-window="${codex7dPin}"]`)!.click());
      expect(document.querySelector("[data-usage-pinned-bar]")?.textContent).toContain("侧栏已固定 3 项");
      const stripText = stripBtn().textContent ?? "";
      expect(stripText.indexOf("Antigravity · Gemini Models")).toBeLessThan(stripText.indexOf("CNY 12.34"));
      expect(stripText.indexOf("CNY 12.34")).toBeLessThan(stripText.indexOf("Codex Pro"));
      expect(stripBtn().querySelectorAll('[role="progressbar"]')).toHaveLength(2);
      expect(stripBtn().textContent).toContain("剩 70%");
      expect(stripBtn().textContent).not.toContain("15%");

      // If Gemini Models 7d disappears from live data, surviving pins stay rendered; popover counts 2 live items
      const withoutGemini7d: UsageAccount[] = [
        account("codex", 85, 30, { provider: "codex", name: "Codex Pro" }),
        { ...agyAccount(), windows: [agy.windows[0], agy.windows[2]] },
        balanceAccount("ds", "12.34"),
      ];
      await act(async () => root.render(<UsageBars accounts={withoutGemini7d} now={NOW} />));
      expect(stripBtn().textContent).not.toContain("Gemini Models");
      expect(stripBtn().textContent).toContain("CNY 12.34");
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(document.querySelector("[data-usage-pinned-bar]")?.textContent).toContain("侧栏已固定 2 项");

      // When only a missing window pin remains, strip falls back to automatic ranking and popover shows fallback notice.
      // Automatic ranking reads each account as a whole: Antigravity has only model pools, so it shows the pool with the
      // most headroom (Claude and GPT models, 88% left), not Gemini's exhausted 5h window (#15669).
      await act(async () => document.querySelector<HTMLButtonElement>('[data-usage-pin="ds"]')!.click());
      await act(async () => document.querySelector<HTMLButtonElement>(`[data-usage-pin-window="${codex7dPin}"]`)!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe(JSON.stringify([gemini7dPin]));
      expect(document.querySelector("[data-usage-pinned-bar]")?.textContent).toContain("固定项暂不可用");
      expect(stripBtn().textContent).toContain("Codex Pro剩 15%");
      expect(stripBtn().textContent).toContain("Antigravity剩 88%");
      expect(stripBtn().textContent).not.toContain("Gemini Models");

      // Reset to automatic clears stored pins
      await act(async () => document.querySelector<HTMLButtonElement>("[data-usage-reset-pins]")!.click());
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBeNull();
      expect(document.querySelector("[data-usage-pinned-bar]")).toBeNull();
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("survives localStorage write failures using tab-local in-memory pin state", async () => {
    localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["codex"]');
    const setSpy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    const removeSpy = vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      const live = sampleAccounts();
      const agy7dPin = usageWindowPinId("agy", live[2].windows[1]);
      await act(async () => root.render(<UsageBars accounts={live} now={NOW} />));
      const stripBtn = () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!;
      expect(stripBtn().textContent).toContain("Codex Pro");

      await act(async () => stripBtn().click());
      await vi.waitFor(() => expect(document.querySelector(`[data-usage-pin-window="${agy7dPin}"]`)).not.toBeNull());

      // Pin agy 7d (20% used -> 80% remaining) despite setItem throwing: UI updates and stays updated across further toggles
      await act(async () => document.querySelector<HTMLButtonElement>(`[data-usage-pin-window="${agy7dPin}"]`)!.click());
      expect(stripBtn().textContent).toContain("Antigravity");
      expect(stripBtn().textContent).toContain("剩 80%");

      // Unpin codex: only agy 7d remains instead of reverting to stored '["codex"]'
      await act(async () => document.querySelector<HTMLButtonElement>('[data-usage-pin="codex"]')!.click());
      expect(stripBtn().textContent).not.toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("Antigravity");

      // Reset works in memory even when removeItem throws
      await act(async () => document.querySelector<HTMLButtonElement>("[data-usage-reset-pins]")!.click());
      expect(document.querySelector("[data-usage-pinned-bar]")).toBeNull();
      expect(stripBtn().textContent).toContain("Codex Pro");
      expect(stripBtn().textContent).toContain("Claude Max");
    } finally {
      setSpy.mockRestore();
      removeSpy.mockRestore();
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("does not append extra unconnected rows once four or more bars are pinned", () => {
    const live = [
      account("codex", 85, 30, { provider: "codex", name: "Codex Pro" }),
      account("claude", 60, 40, { provider: "claude", name: "Claude Max" }),
    ];
    localStorage.setItem(
      PINNED_USAGE_ACCOUNTS_KEY,
      JSON.stringify([
        usageWindowPinId("codex", live[0].windows[0]),
        usageWindowPinId("codex", live[0].windows[1]),
        usageWindowPinId("claude", live[1].windows[0]),
        usageWindowPinId("claude", live[1].windows[1]),
      ]),
    );
    const strip = renderToStaticMarkup(<UsageBars accounts={live} unconnected={sampleUnconnected} now={NOW} />);
    expect(strip.match(/role="progressbar"/g)).toHaveLength(4);
    expect(strip).not.toContain("data-usage-unconnected");
  });

  it.each([
    [
      "en",
      "Pin Antigravity to sidebar",
      "Unpin Antigravity from sidebar",
      "Pin Antigravity 7d to sidebar",
      "Unpin Antigravity 7d from sidebar",
      "Pin Antigravity 7d (Gemini Models) to sidebar",
      "Unpin Antigravity 7d (Gemini Models) from sidebar",
      "Pinned in sidebar: 2",
      "Reset to automatic",
    ],
    [
      "zh-Hans",
      "将 Antigravity 固定到侧栏",
      "取消将 Antigravity 固定到侧栏",
      "将 Antigravity 7d 固定到侧栏",
      "取消将 Antigravity 7d 固定到侧栏",
      "将 Antigravity 7d（Gemini Models）固定到侧栏",
      "取消将 Antigravity 7d（Gemini Models）固定到侧栏",
      "侧栏已固定 2 项",
      "恢复默认",
    ],
    [
      "ja",
      "Antigravity をサイドバーに固定",
      "Antigravity のサイドバー固定を解除",
      "Antigravity 7d をサイドバーに固定",
      "Antigravity 7d のサイドバー固定を解除",
      "Antigravity 7d（Gemini Models）をサイドバーに固定",
      "Antigravity 7d（Gemini Models）のサイドバー固定を解除",
      "サイドバーに 2 件を固定中",
      "自動表示に戻す",
    ],
  ])("localizes account and window pin controls and reset action in %s", async (lang, pinLabel, unpinLabel, pinWinLabel, unpinWinLabel, pinModelLabel, unpinModelLabel, summaryText, resetLabelText) => {
    await initI18n(lang);
    try {
      const live: UsageAccount[] = [{
        ...account("agy", 20, 30, { provider: "agy", name: "Antigravity" }),
        windows: [
          { label: "7d", window_minutes: 10080, used_percent: 30, resets_at: iso(2 * 24 * H) },
          { label: "7d", window_minutes: 10080, used_percent: 25, resets_at: iso(2 * 24 * H), model: "Gemini Models" },
        ],
      }];
      const unpinned = renderToStaticMarkup(<UsagePopover accounts={live} now={NOW} />);
      expect(unpinned).toContain(`aria-label="${pinLabel}"`);
      expect(unpinned).toContain(`aria-label="${pinWinLabel}"`);
      expect(unpinned).toContain(`aria-label="${pinModelLabel}"`);
      expect(unpinned).not.toContain(resetLabelText);

      const planPin = usageWindowPinId("agy", live[0].windows[0]);
      const modelPin = usageWindowPinId("agy", live[0].windows[1]);
      localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, JSON.stringify(["agy", modelPin]));
      const pinned = renderToStaticMarkup(<UsagePopover accounts={live} now={NOW} />);
      expect(pinned).toContain(`aria-label="${unpinLabel}"`);
      expect(pinned).toContain(`aria-label="${pinWinLabel}"`);
      expect(pinned).toContain(`aria-label="${unpinModelLabel}"`);
      expect(pinned).toContain(summaryText);
      expect(pinned).toContain(resetLabelText);

      localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, JSON.stringify([planPin]));
      const planPinned = renderToStaticMarkup(<UsagePopover accounts={live} now={NOW} />);
      expect(planPinned).toContain(`aria-label="${unpinWinLabel}"`);
    } finally {
      localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
      await initI18n("zh-Hans");
    }
  });
});

describe("provider cards and single unified reset timeline (V2)", () => {
  it("renders a single unified timeline at the bottom across all provider cards with provider dividers, stripped prefixes, and multi-model merging", () => {
    const claude = (id: string, name: string, h7: number, paused = false): UsageAccount => ({
      ...account(id, 10, paused ? 96 : 20, { provider: "claude", name, plan: "Max" }),
      windows: [
        { label: "7d", window_minutes: 10080, used_percent: paused ? 96 : 20, resets_at: iso(h7 * H), ...(paused ? { protect_percent: 95 } : {}) },
        { label: "5h", window_minutes: 300, used_percent: 10, resets_at: iso(3 * H) },
      ],
    });
    const agy: UsageAccount = {
      id: "ag1",
      provider: "antigravity",
      name: "Antigravity · Main",
      plan: "Ultra",
      sampled_at: iso(-12_000),
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 25, resets_at: iso(4 * H), model: "Claude" },
        { label: "5h", window_minutes: 300, used_percent: 40, resets_at: iso(2 * H), model: "Gemini Pro" },
      ],
    };
    const codex = account("c1", 10, 20, { provider: "codex", name: "Codex #alpha", plan: "Plus" });

    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[claude("1", "Claude #later", 100), claude("2", "Claude #sooner", 30, true), agy, codex]} now={NOW} defaultTimelineOpen />);

    // 3 provider cards, NONE containing a timeline or repeated sort subtitle; 1 popover-level sort subtitle + 1 unified timeline section
    const cards = [...host.querySelectorAll("[data-usage-provider]")];
    expect(cards).toHaveLength(3);
    for (const card of cards) {
      expect(card.querySelector("[data-usage-timeline]")).toBeNull();
      expect(card.querySelector("[data-usage-sort-subtitle]")).toBeNull();
      expect(card.classList.contains("usage-popover-card")).toBe(true);
    }
    expect(host.querySelectorAll("[data-usage-sort-subtitle]")).toHaveLength(1);
    const section = host.querySelector("[data-usage-timeline-section]")!;
    expect(section.classList.contains("usage-popover-span")).toBe(true);
    const timelines = [...host.querySelectorAll("[data-usage-timeline]")];
    expect(timelines).toHaveLength(1);

    // Row order: claude(2 sooner), claude(1 later), agy(ag1), codex(c1)
    const rows = [...timelines[0].querySelectorAll("[data-timeline-row]")];
    expect(rows.map((r) => r.getAttribute("data-timeline-row"))).toEqual(["2", "1", "ag1", "c1"]);

    // Hairline provider dividers ONLY when provider changes (indices 2 and 3, not 0 or 1)
    expect(rows[0].querySelector("[data-timeline-provider-divider]")).toBeNull();
    expect(rows[1].querySelector("[data-timeline-provider-divider]")).toBeNull();
    expect(rows[2].querySelector("[data-timeline-provider-divider]")).not.toBeNull();
    expect(rows[3].querySelector("[data-timeline-provider-divider]")).not.toBeNull();

    // Name column: 6px provider dot + stripped prefix + plan subtitle
    expect(rows[0].querySelector("[data-timeline-account-name]")?.textContent).toBe("#sooner");
    expect(rows[1].querySelector("[data-timeline-account-name]")?.textContent).toBe("#later");
    expect(rows[2].querySelector("[data-timeline-account-name]")?.textContent).toBe("Main");
    expect(rows[3].querySelector("[data-timeline-account-name]")?.textContent).toBe("#alpha");
    expect(rows[3].querySelector<HTMLElement>('[data-timeline-provider-dot="codex"]')?.classList.contains("size-1.5")).toBe(true);
    expect(rows[3].querySelector<HTMLElement>('[data-timeline-provider-dot="codex"]')?.style.background).toBe("var(--hue-green)");

    // Multi-model Antigravity row: merged into 1 5h lane taking earliest reset (2h) and listing both models in tooltip
    const agyLanes = [...rows[2].querySelectorAll("[data-timeline-lane]")];
    expect(agyLanes).toHaveLength(1);
    expect(agyLanes[0].getAttribute("data-timeline-lane")).toBe("5h");
    expect(agyLanes[0].querySelector("[data-timeline-remaining]")?.textContent).toBe("2 小时");
    expect(rows[2].getAttribute("title")).toContain("Gemini Pro");
    expect(rows[2].getAttribute("title")).toContain("Claude");

    // Paused end dot is per lane: account "2" has its 7d at the protection line, its 5h is healthy (teal)
    const lanes0 = [...rows[0].querySelectorAll<HTMLElement>("[data-timeline-lane]")];
    expect(lanes0.map((l) => l.getAttribute("data-timeline-lane"))).toEqual(["5h", "7d"]);
    const dot5 = lanes0[0].querySelector<HTMLElement>("[data-timeline-dot]")!;
    const dot7 = lanes0[1].querySelector<HTMLElement>("[data-timeline-dot]")!;
    expect(dot5.getAttribute("data-timeline-dot")).toBe("active");
    expect(dot5.style.borderColor).toBe("var(--hue-teal)");
    expect(dot7.getAttribute("data-timeline-dot")).toBe("paused");
    expect(dot7.style.borderColor).toBe("var(--orange)");
    expect(rows[0].getAttribute("aria-label")).toContain("7d · 已暂停");
    expect(rows[0].getAttribute("aria-label")).not.toContain("5h · 已暂停");
    expect(rows[1].querySelector('[data-timeline-dot="paused"]')).toBeNull();
  });

  it.each([
    ["zh-Hans", "剩余额度（按 7d 重置时间由近到远）", ["现在", "6h", "12h", "1d", "3d", "5d", "7d"], ["4.4 小时", "4 天 16 小时", "6 天 24 小时"], ["5h 窗口恢复", "7d 窗口恢复", "30d 窗口恢复", "现在", "保护线"], "达到此阈值时将自动暂停调度"],
    ["en", "Remaining (by 7d reset, soonest first)", ["Now", "6h", "12h", "1d", "3d", "5d", "7d"], ["4.4h", "4d 16h", "6d 24h"], ["5h window resets", "7d window resets", "30d window resets", "Now", "Protection line"], "Scheduling pauses automatically at this limit"],
    ["ja", "残り（7d リセットが早い順）", ["今", "6h", "12h", "1d", "3d", "5d", "7d"], ["4.4時間", "4日16時間", "6日24時間"], ["5h 枠リセット", "7d 枠リセット", "30d 枠リセット", "今", "保護ライン"], "この上限に達すると自動で一時停止します"],
  ])("renders card header dot, remaining sort subtitle, short-unit ticks and dynamic legend in %s", async (lang, subtitle, ticks, countdowns, legendLabels, protectTitle) => {
    await initI18n(lang);
    try {
      const a = account("a", 10, 20, { provider: "claude", name: "Team alpha account", plan: "Team" });
      a.windows[0].resets_at = iso(4.4 * H);
      a.windows[1].resets_at = iso(112 * H);
      a.windows.push({ label: "30d", window_minutes: 43200, used_percent: 10, resets_at: iso(167.75 * H) });
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} defaultTimelineOpen />);

      const card = host.querySelector('[data-usage-provider="claude"]')!;
      expect(card.classList.contains("@container")).toBe(true);
      const dot = card.querySelector<HTMLElement>('[data-provider-dot="claude"]')!;
      expect(dot.style.background).toBe("var(--hue-orange)");
      expect(providerHue("claude")).toBe("var(--hue-orange)");
      expect(providerHue("codex")).toBe("var(--hue-green)");
      expect(providerHue("agy")).toBe("var(--hue-blue)");
      expect(providerHue("unknown-provider")).toBe("var(--hue-gray)");
      expect(card.querySelector("[data-usage-sort-subtitle]")).toBeNull();
      expect(host.querySelector("[data-usage-header] [data-usage-sort-subtitle]")?.textContent).toBe(subtitle);

      const axis = host.querySelector("[data-timeline-axis]")!;
      const tickEls = [...axis.querySelectorAll("[data-timeline-tick]")];
      expect(tickEls.map((tick) => tick.textContent)).toEqual(ticks);
      expect(axis.classList.contains("whitespace-nowrap")).toBe(true);
      for (const tick of tickEls) expect(tick.classList.contains("whitespace-nowrap")).toBe(true);

      const row = host.querySelector("[data-timeline-row]")!;
      const name = row.querySelector("[data-timeline-account-name]")!;
      expect(name.textContent).toBe(a.name);
      expect(name.classList.contains("truncate")).toBe(true);
      expect(row.getAttribute("aria-label")).toContain(a.name);

      const lanes = [...row.querySelectorAll("[data-timeline-lane]")];
      expect(lanes.map((lane) => lane.getAttribute("data-timeline-lane"))).toEqual(["5h", "7d", "30d"]);
      const labels = lanes.map((lane) => lane.querySelector<HTMLElement>("[data-timeline-remaining]")!);
      expect(labels.map((label) => label.textContent)).toEqual(countdowns);
      const positions = labels.map((label) => parseFloat(label.parentElement!.style.gridTemplateColumns));
      expect(positions).toEqual([4.4, 112, 167.75].map((hours) => timelinePosition(hours * H)));

      const legend = host.querySelector("[data-timeline-legend]")!;
      expect(legend.classList.contains("flex-wrap")).toBe(true);
      expect(legend.classList.contains("gap-x-3")).toBe(true);
      expect(legend.classList.contains("gap-y-1")).toBe(true);
      const items = [...legend.querySelectorAll<HTMLElement>("[data-legend-item]")];
      expect(items.map((el) => el.textContent)).toEqual(legendLabels);
      for (const item of items) expect(item.classList.contains("whitespace-nowrap")).toBe(true);
      const protectItem = legend.querySelector<HTMLElement>('[data-legend-item="protect"]')!;
      expect(protectItem.getAttribute("title")).toBe(protectTitle);
      expect(protectItem.textContent).not.toContain(protectTitle);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("progressively hides colliding interior ticks (6h, 5d, 12h, 3d) and rebinds ResizeObserver when the timeline mounts later", async () => {
    expect(visibleTimelineTickHours(null)).toEqual([0, 6, 12, 24, 72, 120, 168]);
    expect(visibleTimelineTickHours(360)).toEqual([0, 6, 12, 24, 72, 120, 168]);
    expect(visibleTimelineTickHours(200)).toEqual([0, 12, 24, 72, 120, 168]);
    expect(visibleTimelineTickHours(146)).toEqual([0, 24, 72, 168]);
    expect(visibleTimelineTickHours(102)).toEqual([0, 24, 168]);

    let axisCb: ((entries: { contentRect: { width: number } }[]) => void) | undefined;
    let axisObserved = 0;
    const origRO = globalThis.ResizeObserver;
    vi.stubGlobal(
      "ResizeObserver",
      class {
        private cb: (entries: { contentRect: { width: number } }[]) => void;
        constructor(cb: (entries: { contentRect: { width: number } }[]) => void) {
          this.cb = cb;
        }
        observe(el: Element) {
          if (el.hasAttribute("data-timeline-axis")) {
            axisObserved++;
            axisCb = this.cb;
          }
        }
        disconnect() {}
      },
    );
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      const noReset = account("a", 10, 20);
      noReset.windows.forEach((w) => delete w.resets_at);
      await act(async () => root.render(<UsagePopover accounts={[noReset]} now={NOW} defaultTimelineOpen />));
      expect(host.querySelector("[data-timeline-axis]")).toBeNull();
      expect(axisObserved).toBe(0);

      await act(async () => root.render(<UsagePopover accounts={[account("a", 10, 20)]} now={NOW} defaultTimelineOpen />));
      expect(axisObserved).toBe(1);
      const ticks = () => [...host.querySelectorAll("[data-timeline-tick]")].map((el) => el.textContent);
      expect(ticks()).toEqual(["现在", "6h", "12h", "1d", "3d", "5d", "7d"]);

      await act(async () => axisCb?.([{ contentRect: { width: 200 } }]));
      expect(ticks()).toEqual(["现在", "12h", "1d", "3d", "5d", "7d"]);

      await act(async () => axisCb?.([{ contentRect: { width: 146 } }]));
      expect(ticks()).toEqual(["现在", "1d", "3d", "7d"]);

      await act(async () => axisCb?.([{ contentRect: { width: 102 } }]));
      expect(ticks()).toEqual(["现在", "1d", "7d"]);

      await act(async () => axisCb?.([{ contentRect: { width: 360 } }]));
      expect(ticks()).toEqual(["现在", "6h", "12h", "1d", "3d", "5d", "7d"]);
    } finally {
      await act(async () => root.unmount());
      host.remove();
      if (origRO) vi.stubGlobal("ResizeObserver", origRO);
      else Reflect.deleteProperty(globalThis, "ResizeObserver");
    }
  });

  it("matches timeline legend labels to the actual window lengths present", () => {
    const dayOnly: UsageAccount = {
      id: "d1",
      provider: "claude",
      name: "Daily seat",
      sampled_at: iso(-12_000),
      windows: [{ label: "1d", window_minutes: 1440, used_percent: 30, resets_at: iso(12 * H) }],
    };
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[dayOnly]} now={NOW} defaultTimelineOpen />);
    const items = [...host.querySelectorAll<HTMLElement>("[data-legend-item]")].map((el) => el.textContent);
    expect(items).toEqual(["1d 窗口恢复", "现在", "保护线"]);
  });

  it("adapts reset copy for narrow (<300px) container cards while letting the meter shrink before short countdowns", () => {
    expect(compactResetText(iso(45 * 60_000), NOW)).toBe("45m");
    expect(compactResetText(iso(1.5 * H), NOW)).toBe("1.5h");
    expect(compactResetText(iso(50 * H), NOW)).toBe("2d 2h");
    expect(compactResetText(iso(48 * H), NOW)).toBe("2d");
    expect(compactResetText(iso(47.8 * H), NOW)).toBe("2d");
    expect(compactResetText(iso(-1000), NOW)).toBe("即将重置");

    const a = account("a", 12, 72);
    a.windows[0].resets_at = iso(1.5 * H);
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);
    const row = host.querySelector('[data-window-row="5h"]')!;
    expect(row.className).toContain("grid-cols-[1.75rem_minmax(2.5rem,1fr)_2.25rem_3.75rem_0.75rem]");
    expect(row.className).toContain("@[300px]:grid-cols-[1.75rem_minmax(2.5rem,1fr)_2.25rem_6.75rem_0.75rem]");
    const full = row.querySelector("[data-reset-full]")!;
    const short = row.querySelector("[data-reset-short]")!;
    expect(full.className).toContain("hidden");
    expect(full.className).toContain("@[300px]:inline");
    expect(short.className).toContain("@[300px]:hidden");
    expect(short.textContent).toBe("1.5h");
  });

  it("draws no unified timeline when no account has any reset time", () => {
    const a = account("a", 1, 1);
    a.windows.forEach((w) => delete w.resets_at);
    expect(renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />)).not.toContain("data-usage-timeline");
  });

  it.each([
    ["zh-Hans", "保护 5%", "剩余 5% 时暂停调度", "已暂停"],
    ["en", "Keep 5%", "pauses scheduling at 5% remaining", "Paused"],
    ["ja", "確保 5%", "残り 5% で自動一時停止", "一時停止"],
  ])("draws left-side [0, R] protection zone, reserve flag, aria-label, and paused state in %s", async (lang, flagText, ariaProtect, pausedText) => {
    await initI18n(lang);
    try {
      // 5h: unprotected (r=90), 7d: protected R=5 (used 96% -> r=4 <= 5 -> paused & red)
      const a = account("a", 10, 96);
      a.windows[1].protect_percent = 95;
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[a]} now={NOW} />);

      const row5 = host.querySelector('[data-window-row="5h"]')!;
      const row7 = host.querySelector('[data-window-row="7d"]')!;
      expect(row5.classList.contains("mt-3.5")).toBe(false);
      expect(row7.classList.contains("mt-3.5")).toBe(true);
      expect(row7.getAttribute("data-window-paused")).toBe("true");

      // Zone at left [0, 5%], vertical line at 5%, flag shows R=5%
      const zone = row7.querySelector<HTMLElement>("[data-protect-zone]")!;
      const line = row7.querySelector<HTMLElement>("[data-protect-line]")!;
      const flag = row7.querySelector<HTMLElement>("[data-protect-flag]")!;
      expect(zone.classList.contains("left-0")).toBe(true);
      expect(zone.style.width).toBe("5%");
      expect(line.style.left).toBe("5%");
      expect(flag.textContent).toBe(flagText);
      expect(flag.classList.contains("left-0")).toBe(true); // left-aligned at small R so it never overlaps "7d"

      // Bar is red at width 4% (inside [0, 5%]), reset text starts with Paused ·
      const bar = row7.querySelector<HTMLElement>('[role="progressbar"] > div')!;
      expect(bar.classList.contains("bg-red")).toBe(true);
      expect(bar.style.width).toBe("4%");
      const progress = row7.querySelector('[role="progressbar"]')!;
      expect(progress.getAttribute("aria-valuenow")).toBe("4");
      expect(progress.getAttribute("aria-label")).toContain(ariaProtect);
      const resetEl = row7.querySelector("[data-usage-reset]")!;
      expect(resetEl.getAttribute("data-usage-paused")).toBe("true");
      expect(resetEl.textContent).toContain(pausedText);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("renders usage bars and popover typography with named text tokens instead of hard-coded pixel classes (#13211)", () => {
    const a = account("a", 40, 65, {
      provider: "claude",
      name: "Work Claude",
      plan: "Max",
      sources: ["Relay"],
      problem: "unreachable",
    });
    a.windows[1].protect_percent = 90;
    a.windows.push({ label: "7d", window_minutes: 10080, used_percent: 50, resets_at: iso(30 * H), model: "Sonnet" });
    const stripHtml = renderToStaticMarkup(<UsageBars accounts={[a, balanceAccount("ds", "12.34")]} now={NOW} />);
    const popoverHtml = renderToStaticMarkup(<UsagePopover accounts={[a, balanceAccount("ds", "12.34")]} now={NOW} />);
    const arbitraryPxFont = /text-\[(?:length:)?\d+(?:\.\d+)?px\]/;
    expect(stripHtml).not.toMatch(arbitraryPxFont);
    expect(popoverHtml).not.toMatch(arbitraryPxFont);
    expect(stripHtml).toContain("text-submeta");
    expect(popoverHtml).toContain("text-meta");
    expect(popoverHtml).toContain("text-label");
    expect(popoverHtml).toContain("text-submeta");
  });

  it("keeps and renders extension accounts that carry notice or headline with or without windows", () => {
    const noticeOnly: UsageAccount = {
      id: "cool-1",
      provider: "claude",
      name: "Cooldown seat",
      plan: "Max",
      sampled_at: iso(-12_000),
      windows: [],
      notice: "Cooldown · resumes in 30m",
    };
    const stripHtml = renderToStaticMarkup(<UsageBars accounts={[noticeOnly]} now={NOW} />);
    expect(stripHtml).toContain('data-usage-notice-strip="cool-1"');
    expect(stripHtml).toContain("Cooldown seat");
    expect(stripHtml).toContain("Cooldown · resumes in 30m");
    expect(stripHtml).not.toContain('role="progressbar"');

    const popOnly = renderToStaticMarkup(<UsagePopover accounts={[noticeOnly]} now={NOW} />);
    expect(popOnly).toContain("data-usage-notice");
    expect(popOnly).toContain("Cooldown · resumes in 30m");
    expect(popOnly).not.toContain("data-usage-sort-subtitle");
    expect(popOnly).not.toContain("data-usage-timeline");
    expect(popOnly).not.toContain("data-usage-pin=");

    localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, '["cool-1"]');
    try {
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[noticeOnly]} now={NOW} />);
      expect(host.textContent).toContain("固定项暂不可用");
      const unpinBtn = host.querySelector('[data-usage-pin="cool-1"]');
      expect(unpinBtn).not.toBeNull();
      expect(unpinBtn?.getAttribute("aria-pressed")).toBe("true");
    } finally {
      resetPinnedUsageAccounts();
      localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
    }

    const withWindows = account("cool-2", 62, 84, {
      provider: "claude",
      name: "Restricted seat",
      headline: "Available 4/5",
      notice: "Rate limited · resumes in 15m",
    });
    const popBoth = renderToStaticMarkup(<UsagePopover accounts={[withWindows]} now={NOW} />);
    expect(popBoth).toContain("data-usage-headline");
    expect(popBoth).toContain("Available 4/5");
    expect(popBoth).toContain("data-usage-notice");
    expect(popBoth).toContain("Rate limited · resumes in 15m");
    expect(popBoth).toContain("data-usage-sort-subtitle");
    expect(popBoth).toContain('data-window-row="5h"');
  });
});

describe("height-first adaptive multi-column layout, hysteresis, scroll fallback, and reduced motion", () => {
  it("stays single-column when compact, and expands to 2 columns on wide desktop once multi-provider height passes comfort threshold", () => {
    const compact = computeUsagePopoverLayout({
      naturalHeight: 380,
      viewportWidth: 1440,
      viewportHeight: 900,
      anchorRect: { left: 10, top: 800, bottom: 880 },
      prevCols: null,
      providerCount: 3,
    });
    expect(compact.cols).toBe(1);
    expect(compact.width).toBe(384); // 360px single-column comfort width + 24px pad
    expect(compact.left).toBe(10);
    expect(compact.bottom).toBe(108); // 900 - 800 + 8
    expect(compact.availH).toBe(776); // min(900 - 32, 800 - 24)
    expect(compact.availW).toBe(1414); // 1440 - 16 - 10

    // With >=2 provider cards and single-col height > 440px on desktop, expands to 2 balanced columns instead of a skinny tower
    const desktopMulti = computeUsagePopoverLayout({
      naturalHeight: 520,
      viewportWidth: 1440,
      viewportHeight: 900,
      anchorRect: { left: 10, top: 800, bottom: 880 },
      prevCols: null,
      providerCount: 3,
    });
    expect(desktopMulti.cols).toBe(2);
    expect(desktopMulti.width).toBe(716); // 2 * 340 + 12 + 24
    expect(desktopMulti.colWidth).toBe(340);

    // Single provider never creates an empty second column even when tall
    const singleProvider = computeUsagePopoverLayout({
      naturalHeight: 560,
      viewportWidth: 1440,
      viewportHeight: 900,
      anchorRect: { left: 10, top: 800 },
      prevCols: null,
      providerCount: 1,
    });
    expect(singleProvider.cols).toBe(1);
    expect(singleProvider.width).toBe(384);
  });

  it("expands to 2 or 3 columns on wide viewports when height overflows availH, capped at 3 and providerCount", () => {
    const twoCols = computeUsagePopoverLayout({
      naturalHeight: 1100,
      viewportWidth: 1440,
      viewportHeight: 900,
      anchorRect: { left: 10, top: 800 },
      prevCols: null,
      providerCount: 3,
    });
    expect(twoCols.cols).toBe(2);
    expect(twoCols.width).toBe(716);
    expect(twoCols.colWidth).toBe(340);

    const threeCols = computeUsagePopoverLayout({
      naturalHeight: 1700,
      viewportWidth: 1440,
      viewportHeight: 900,
      anchorRect: { left: 10, top: 800 },
      prevCols: null,
      providerCount: 3,
    });
    expect(threeCols.cols).toBe(3);
    expect(threeCols.width).toBe(1068);

    // Even if height is huge, capped at 3 columns max
    const cappedAt3 = computeUsagePopoverLayout({
      naturalHeight: 4000,
      viewportWidth: 2560,
      viewportHeight: 900,
      anchorRect: { left: 10, top: 800 },
      prevCols: null,
      providerCount: 5,
    });
    expect(cappedAt3.cols).toBe(3);

    // Never creates empty columns beyond providerCount
    const cappedByProviders = computeUsagePopoverLayout({
      naturalHeight: 1800,
      viewportWidth: 1600,
      viewportHeight: 700,
      anchorRect: { left: 10, top: 620 },
      prevCols: null,
      providerCount: 2,
    });
    expect(cappedByProviders.cols).toBe(2);
  });

  it("applies 24px hysteresis in both height and width to avoid column-count flicker", () => {
    expect(POPOVER_HYSTERESIS_PX).toBe(24);
    // Currently 2 cols; single-col comfort limit = 440, H = 430 (only 10px under 440, < 24px deadband) -> stays at 2 cols
    const stayTwo = computeUsagePopoverLayout({
      naturalHeight: 430,
      viewportWidth: 1440,
      viewportHeight: 700,
      anchorRect: { left: 10, top: 624 }, // availH = 600
      prevCols: 2,
      providerCount: 3,
    });
    expect(stayTwo.cols).toBe(2);

    // Once H <= 440 - 24 (416), collapses cleanly to 1 col
    const dropToOne = computeUsagePopoverLayout({
      naturalHeight: 416,
      viewportWidth: 1440,
      viewportHeight: 700,
      anchorRect: { left: 10, top: 624 },
      prevCols: 2,
      providerCount: 3,
    });
    expect(dropToOne.cols).toBe(1);

    // Width hysteresis: col step = 352, 2*352 = 704; at prevCols = 1, availW = 714 (past 704 by only 10px, < 24px) -> stays at 1 col
    const stayOneNarrow = computeUsagePopoverLayout({
      naturalHeight: 900,
      viewportWidth: 740, // availW = 740 - 16 - 10 = 714
      viewportHeight: 700,
      anchorRect: { left: 10, top: 624 },
      prevCols: 1,
      providerCount: 3,
    });
    expect(stayOneNarrow.cols).toBe(1);

    // Once availW >= 2*352 + 24 = 728, expands cleanly to 2 cols
    const expandToTwo = computeUsagePopoverLayout({
      naturalHeight: 900,
      viewportWidth: 754, // availW = 754 - 16 - 10 = 728
      viewportHeight: 700,
      anchorRect: { left: 10, top: 624 },
      prevCols: 1,
      providerCount: 3,
    });
    expect(expandToTwo.cols).toBe(2);

    // If viewport narrows below 2*352 = 704 while at 2 cols, shrinks immediately so it never clips horizontally
    const immediateShrink = computeUsagePopoverLayout({
      naturalHeight: 900,
      viewportWidth: 710, // availW = 684 < 704
      viewportHeight: 700,
      anchorRect: { left: 10, top: 624 },
      prevCols: 2,
      providerCount: 3,
    });
    expect(immediateShrink.cols).toBe(1);
  });

  it("falls back to single-column scrollable layout with fade masks on narrow & short viewports", async () => {
    const narrowLayout = computeUsagePopoverLayout({
      naturalHeight: 900,
      viewportWidth: 420,
      viewportHeight: 460,
      anchorRect: { left: 10, top: 380 },
      prevCols: null,
      providerCount: 3,
    });
    expect(narrowLayout.cols).toBe(1);
    expect(narrowLayout.availH).toBe(356);
    expect(narrowLayout.width).toBe(384);
    expect(narrowLayout.left + narrowLayout.width).toBeLessThanOrEqual(420 - 16);

    // Verify scroll-edge fade attributes in the DOM when scrollHeight exceeds availH
    const origIH = window.innerHeight;
    const origIW = window.innerWidth;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 420 });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 460 });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsagePopover accounts={[account("a", 20, 40), account("b", 50, 60, { provider: "claude" })]} now={NOW} />));
      const scrollEl = host.querySelector<HTMLElement>("[data-usage-scroll]")!;
      Object.defineProperty(scrollEl, "scrollHeight", { configurable: true, value: 900 });
      Object.defineProperty(scrollEl, "clientHeight", { configurable: true, value: 428 });
      Object.defineProperty(scrollEl, "scrollTop", { configurable: true, writable: true, value: 0 });
      await act(async () => {
        window.dispatchEvent(new Event("resize"));
      });
      expect(scrollEl.getAttribute("data-scrollable")).toBe("true");
      expect(scrollEl.getAttribute("data-fade-top")).toBe("false");
      expect(scrollEl.getAttribute("data-fade-bottom")).toBe("true");

      scrollEl.scrollTop = 120;
      await act(async () => {
        scrollEl.dispatchEvent(new Event("scroll"));
      });
      expect(scrollEl.getAttribute("data-fade-top")).toBe("true");
      expect(scrollEl.getAttribute("data-fade-bottom")).toBe("true");

      scrollEl.scrollTop = 472;
      await act(async () => {
        scrollEl.dispatchEvent(new Event("scroll"));
      });
      expect(scrollEl.getAttribute("data-fade-top")).toBe("true");
      expect(scrollEl.getAttribute("data-fade-bottom")).toBe("false");
    } finally {
      await act(async () => root.unmount());
      host.remove();
      Object.defineProperty(window, "innerWidth", { configurable: true, value: origIW });
      Object.defineProperty(window, "innerHeight", { configurable: true, value: origIH });
    }
  });

  it("counts full-width blocks and unsplittable cards when choosing columns, picking 3 where whole-height math says 2", () => {
    expect(packedColumnHeight([400, 380, 360], 1)).toBe(1164);
    expect(packedColumnHeight([400, 380, 360], 2)).toBe(752);
    expect(packedColumnHeight([400, 380, 360], 3)).toBe(400);
    expect(packedColumnHeight([400, 380], 3)).toBe(400); // never more columns than cards
    expect(packedColumnHeight([], 2)).toBe(0);

    // 1440x990, trigger at y=900: availH = 876. Cards 400/380/360 plus a 400px full-width timeline.
    const base = { viewportWidth: 1440, viewportHeight: 990, anchorRect: { left: 10, top: 900 }, prevCols: null };
    const natural = 24 + 400 + 1164; // 1588: ceil(1588 / 876) = 2, but 2 columns are 24 + 400 + 752 = 1176 > 876
    expect(computeUsagePopoverLayout({ ...base, naturalHeight: natural, providerCount: 3 }).cols).toBe(2);
    const packed = computeUsagePopoverLayout({ ...base, naturalHeight: natural, cardHeights: [400, 380, 360], spanHeight: 400 });
    expect(packed.availH).toBe(876);
    expect(packed.cols).toBe(3); // 24 + 400 + 400 = 824 fits without a scrollbar

    // Compact cards fitting under comfort height stay one column; taller 2-card stack expands to 2 columns
    expect(computeUsagePopoverLayout({ ...base, cardHeights: [120, 120], spanHeight: 120 }).cols).toBe(1);
    expect(computeUsagePopoverLayout({ ...base, cardHeights: [200, 180], spanHeight: 120 }).cols).toBe(2);
    // Nothing fits even in 3 columns: widest the viewport allows, scroll fallback
    expect(computeUsagePopoverLayout({ ...base, cardHeights: [900, 900, 900], spanHeight: 100 }).cols).toBe(3);
    // Hysteresis with packing: at 3 columns, 2 would fit by only 10px (< 24px): stay at 3
    const twoFitsBarely = { ...base, cardHeights: [500, 300, 180], spanHeight: 342 }; // 2 cols: 24 + 342 + 500 = 866 <= 876
    expect(computeUsagePopoverLayout({ ...twoFitsBarely, prevCols: 3 }).cols).toBe(3);
    expect(computeUsagePopoverLayout({ ...twoFitsBarely, prevCols: null }).cols).toBe(2);
  });

  it("keeps pure CSS transitions in the lazy stylesheet and honors prefers-reduced-motion with 120ms opacity-only fade", () => {
    const css = readFileSync(join(__dirname, "usage-popover.css"), "utf8");
    expect(css).toContain("translateY(6px) scale(0.98)");
    expect(css).toContain("240ms");
    expect(css).toContain("translateY(4px)");
    expect(css).toContain("160ms");
    expect(css).toContain("width 300ms");
    expect(css).toContain("150ms");
    expect(css).toContain("column-span: all");
    expect(css).toContain("break-inside: avoid");
    expect(css).toContain("prefers-reduced-motion: reduce");
    expect(css).toContain("120ms !important");
    expect(css).not.toMatch(/font-size\s*:\s*\d+(?:\.\d+)?px/);
  });
});

describe("popover keyboard focus, Back on narrow screens, and a fresh clock on reopen", () => {
  it("moves keyboard focus into the popover on open and back to the trigger on Escape", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsageBars accounts={[account("a", 10, 20)]} now={NOW} />));
      const trigger = host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!;
      trigger.focus();
      await act(async () => trigger.click());
      await vi.waitFor(() => expect(document.querySelector("[role=dialog]")).not.toBeNull());
      const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
      expect(dialog.getAttribute("tabindex")).toBe("-1");
      expect(dialog.contains(document.activeElement)).toBe(true);
      await act(async () => {
        window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
      });
      expect(document.querySelector("[data-usage-popover]")?.getAttribute("data-state")).toBe("closing");
      expect(document.activeElement).toBe(trigger);
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("closes the popover on Back before anything under it on a narrow screen", async () => {
    const narrow = { matches: true, media: "(max-width: 1023px)", addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} };
    vi.stubGlobal("matchMedia", (q: string) => (q === narrow.media ? narrow : { ...narrow, matches: false, media: q }));
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsageBars accounts={[account("a", 10, 20)]} now={NOW} />));
      await act(async () => host.querySelector<HTMLButtonElement>("[data-usage-bars] > button")!.click());
      await vi.waitFor(() => expect(document.querySelector("[role=dialog]")).not.toBeNull());
      expect((history.state as Record<string, unknown> | null)?.personaOverlay).toEqual(expect.any(String));
      await act(async () => {
        history.back();
      });
      await vi.waitFor(() => expect(document.querySelector("[data-usage-popover]")?.getAttribute("data-state") ?? "closed").not.toBe("open"));
      expect(host.querySelector("[data-usage-bars] > button")?.getAttribute("aria-expanded")).toBe("false");
    } finally {
      narrow.matches = false;
      await act(async () => root.unmount());
      host.remove();
      vi.unstubAllGlobals();
    }
  });

  it("draws countdowns and sample ages from the moment it reopens, not from when it last closed", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    const accounts = [account("a", 10, 20)];
    try {
      await act(async () => root.render(<UsagePopover open accounts={accounts} onClose={() => {}} />));
      expect(host.textContent).toContain("12秒钟前");
      await act(async () => root.render(<UsagePopover open={false} accounts={accounts} onClose={() => {}} />));
      vi.setSystemTime(NOW + 60_000);
      await act(async () => root.render(<UsagePopover open accounts={accounts} onClose={() => {}} />));
      expect(host.textContent).toContain("1分钟前");
      expect(host.textContent).not.toContain("12秒钟前");
    } finally {
      await act(async () => root.unmount());
      host.remove();
      vi.useRealTimers();
    }
  });
});

describe("grouped windows, extension metadata, compact views, and collapsible timeline (#15848)", () => {
  afterEach(() => {
    resetUsageViewMode();
    resetUsageTimelineOpen();
    resetPinnedUsageAccounts();
    localStorage.removeItem(USAGE_VIEW_MODE_KEY);
    localStorage.removeItem(USAGE_TIMELINE_OPEN_KEY);
    localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
  });

  const multiAccounts = (): UsageAccount[] => [
    account("codex-a", 15, 38, {
      provider: "codex",
      name: "Codex #alpha",
      plan: "plus",
      local: true,
      machines: ["Workstation"],
    }),
    {
      id: "claude-work",
      provider: "claude",
      name: "Claude #work",
      plan: "max",
      sampled_at: iso(-25_000),
      sources: ["Gateway"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 32, resets_at: iso(2 * H) },
        { label: "7d", window_minutes: 10080, used_percent: 58, resets_at: iso(30 * H), protect_percent: 90 },
        { label: "7d", window_minutes: 10080, used_percent: 82, resets_at: iso(30 * H), model: "Fable" },
      ],
    },
    {
      id: "agy-main",
      provider: "antigravity",
      name: "Antigravity · Main",
      plan: "ultra",
      sampled_at: iso(-15_000),
      local: true,
      machines: ["Workstation"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 20, resets_at: iso(1.5 * H), model: "Claude and GPT models" },
        { label: "5h", window_minutes: 300, used_percent: 35, resets_at: iso(2.5 * H), model: "Gemini Models" },
        { label: "7d", window_minutes: 10080, used_percent: 45, resets_at: iso(60 * H), model: "Claude and GPT models" },
        { label: "7d", window_minutes: 10080, used_percent: 60, resets_at: iso(72 * H), model: "Gemini Models" },
      ],
    },
  ];

  it("aggregates Antigravity multi-model windows so each model group heading renders exactly once", () => {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={multiAccounts()} now={NOW} />);

    const agyCard = host.querySelector('[data-usage-provider="antigravity"]')!;
    const groupTitles = [...agyCard.querySelectorAll("[data-usage-group-title]")].map((el) => el.textContent);
    expect(groupTitles).toEqual(["Claude and GPT models", "Gemini Models"]);
    expect(agyCard.querySelectorAll("[data-usage-model]")).toHaveLength(2);

    const groups = [...agyCard.querySelectorAll("[data-usage-window-group]")];
    expect(groups).toHaveLength(2);
    expect([...groups[0].querySelectorAll("[data-window-row]")].map((el) => el.getAttribute("data-window-row"))).toEqual(["5h", "7d"]);
    expect([...groups[1].querySelectorAll("[data-window-row]")].map((el) => el.getAttribute("data-window-row"))).toEqual(["5h", "7d"]);
  });

  it("renders single-model sub-windows (Fable) inline without a stacked heading, normalizes plans, and shows one popover-level sort subtitle", () => {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={multiAccounts()} now={NOW} />);

    // Sort subtitle appears once in popover header, never repeated inside cards
    expect(host.querySelectorAll("[data-usage-sort-subtitle]")).toHaveLength(1);
    for (const card of host.querySelectorAll("[data-usage-provider]")) {
      expect(card.querySelector("[data-usage-sort-subtitle]")).toBeNull();
    }

    // Plan labels normalized: "plus" -> "Plus", "max" -> "Max", "ultra" -> "Ultra"
    const plans = [...host.querySelectorAll("[data-usage-plan]")].map((el) => el.textContent);
    expect(plans).toEqual(["Plus", "Max", "Ultra"]);

    // Claude Fable renders inline inside its single row with zero group headings
    const claudeCard = host.querySelector('[data-usage-provider="claude"]')!;
    expect(claudeCard.querySelectorAll("[data-usage-group-title]")).toHaveLength(0);
    const inlineRow = claudeCard.querySelector('[data-window-inline-model="true"]')!;
    expect(inlineRow).not.toBeNull();
    expect(inlineRow.getAttribute("data-window-row")).toBe("7d");
    expect(inlineRow.querySelector("[data-usage-model]")?.textContent).toBe("Fable");
  });

  it("consumes and renders extension group, title, and note fields in both Cards and Table views", () => {
    const extAccount: UsageAccount = {
      id: "ext-pool",
      provider: "claude",
      name: "Shared Pool",
      plan: "claude_max_20x",
      sampled_at: iso(-20_000),
      sources: ["Pool Extension"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 25, resets_at: iso(2 * H), group: "Coding Pool", title: "Burst", note: "Shared across IDE and CLI" },
        { label: "7d", window_minutes: 10080, used_percent: 40, resets_at: iso(48 * H), group: "Coding Pool", title: "Weekly" },
        { label: "7d", window_minutes: 10080, used_percent: 85, resets_at: iso(72 * H), group: "Reasoning Pool", note: "Reserves 10% for interactive turns", protect_percent: 90 },
      ],
    };

    const cardsHost = document.createElement("div");
    cardsHost.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[extAccount]} now={NOW} />);
    expect([...cardsHost.querySelectorAll("[data-usage-group-title]")].map((el) => el.textContent)).toEqual(["Coding Pool", "Reasoning Pool"]);
    expect([...cardsHost.querySelectorAll("[data-usage-window-title]")].map((el) => el.textContent)).toEqual(["Burst", "Weekly"]);
    expect([...cardsHost.querySelectorAll("[data-usage-window-note]")].map((el) => el.textContent)).toEqual([
      "Shared across IDE and CLI",
      "Reserves 10% for interactive turns",
    ]);
    expect(cardsHost.querySelector("[data-usage-plan]")?.textContent).toBe("Claude Max 20x");

    const tableHost = document.createElement("div");
    tableHost.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[extAccount]} now={NOW} defaultViewMode="table" />);
    expect(tableHost.querySelector("[data-usage-table]")).not.toBeNull();
    expect(tableHost.textContent).toContain("Burst");
    expect(tableHost.textContent).toContain("Weekly");
    expect(tableHost.textContent).toContain("Shared across IDE and CLI");
    expect(tableHost.textContent).toContain("Reserves 10% for interactive turns");
  });

  it("shows a non-destructive overlap hint when a local-only row and an external row share reset times", () => {
    const localRow: UsageAccount = {
      id: "cl-local",
      provider: "claude",
      name: "Claude",
      sampled_at: iso(-10_000),
      local: true,
      machines: ["Workstation"],
      windows: [{ label: "7d", window_minutes: 10080, used_percent: 40, resets_at: iso(48 * H) }],
    };
    const extRow: UsageAccount = {
      id: "cl-ext",
      provider: "claude",
      name: "Claude #work",
      plan: "max",
      sampled_at: iso(-12_000),
      sources: ["Gateway"],
      windows: [{ label: "7d", window_minutes: 10080, used_percent: 41, resets_at: iso(48 * H + 10_000) }],
    };
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[localRow, extRow]} now={NOW} />);
    expect(host.querySelectorAll('[data-usage-provider="claude"] section[aria-label]')).toHaveLength(2);
    const hint = host.querySelector("[data-usage-overlap-hint]")!;
    expect(hint).not.toBeNull();
    expect(hint.textContent).toContain("Claude #work");
  });

  it("defaults the reset timeline to collapsed with a next-reset summary and expands/persists on toggle", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsagePopover accounts={multiAccounts()} now={NOW} />));
      const toggle = host.querySelector<HTMLButtonElement>("[data-usage-timeline-toggle]")!;
      expect(toggle).not.toBeNull();
      expect(toggle.getAttribute("aria-expanded")).toBe("false");
      expect(host.querySelector("[data-usage-timeline]")).toBeNull();
      const summary = host.querySelector("[data-usage-timeline-summary]")!;
      expect(summary.querySelector("[data-usage-timeline-next-name]")?.textContent).toBe("Main");
      expect(summary.querySelector("[data-usage-timeline-next-name]")?.className).toContain("truncate");
      const timeSegment = summary.querySelector("[data-usage-timeline-next-time]")!;
      expect(timeSegment.textContent).toBe("· 5h · 1.5h");
      expect(timeSegment.className).toContain("shrink-0");
      expect(timeSegment.className).toContain("whitespace-nowrap");
      expect(summary.getAttribute("title")).toContain("1.5 小时");

      // Expand timeline: grid layout with adaptive name column mounts and persists
      await act(async () => toggle.click());
      expect(toggle.getAttribute("aria-expanded")).toBe("true");
      expect(localStorage.getItem(USAGE_TIMELINE_OPEN_KEY)).toBe("1");
      const timeline = host.querySelector("[data-usage-timeline]")!;
      expect(timeline).not.toBeNull();
      expect(timeline.querySelector(".usage-timeline-grid")).not.toBeNull();
      expect(timeline.querySelectorAll("[data-timeline-row]")).toHaveLength(3);
      // Normalized plan in timeline row subtitle
      expect(timeline.textContent).toContain("Max");
      expect(timeline.textContent).not.toContain(">max<");

      // Collapse again
      await act(async () => toggle.click());
      expect(toggle.getAttribute("aria-expanded")).toBe("false");
      expect(localStorage.getItem(USAGE_TIMELINE_OPEN_KEY)).toBeNull();
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });

  it("switches between Cards and Magpie-style Table view with column sorting, pinning, narrow clamping, and token typography", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    const accounts = [...multiAccounts(), balanceAccount("ds-1", "18.50")];
    try {
      await act(async () => root.render(<UsagePopover accounts={accounts} now={NOW} />));
      expect(host.querySelector("[data-usage-popover]")?.getAttribute("data-view")).toBe("cards");
      expect(host.querySelector("[data-usage-table]")).toBeNull();

      // Switch to Table view
      const tableBtn = host.querySelector<HTMLButtonElement>('[data-usage-view="table"]')!;
      await act(async () => tableBtn.click());
      expect(host.querySelector("[data-usage-popover]")?.getAttribute("data-view")).toBe("table");
      expect(localStorage.getItem(USAGE_VIEW_MODE_KEY)).toBe("table");
      const table = host.querySelector("[data-usage-table]")!;
      expect(table).not.toBeNull();
      // Named typography tokens only (no arbitrary pixel classes in Table view either)
      expect(table.innerHTML).not.toMatch(/text-\[(?:length:)?\d+(?:\.\d+)?px\]/);

      // Columns: Account, 5h, 7d
      expect([...table.querySelectorAll("thead th")].map((th) => th.textContent?.trim())).toEqual(["账号", "5h", "7d"]);
      expect([...table.querySelectorAll("[data-usage-table-row]")].map((r) => r.getAttribute("data-usage-table-row"))).toEqual([
        "codex-a",
        "claude-work",
        "agy-main",
        "ds-1",
      ]);

      // Sort by 7d (ascending tightest headroom first -> descending -> default)
      const sort7d = table.querySelector<HTMLButtonElement>('[data-table-sort="m:10080"]')!;
      await act(async () => sort7d.click());
      expect(sort7d.closest("th")?.getAttribute("aria-sort")).toBe("ascending");
      expect([...table.querySelectorAll("[data-usage-table-row]")].map((r) => r.getAttribute("data-usage-table-row"))).toEqual([
        "claude-work",
        "agy-main",
        "codex-a",
        "ds-1",
      ]);
      await act(async () => sort7d.click());
      expect(sort7d.closest("th")?.getAttribute("aria-sort")).toBe("descending");
      expect([...table.querySelectorAll("[data-usage-table-row]")].map((r) => r.getAttribute("data-usage-table-row"))).toEqual([
        "codex-a",
        "agy-main",
        "claude-work",
        "ds-1",
      ]);
      await act(async () => sort7d.click());
      expect(sort7d.closest("th")?.getAttribute("aria-sort")).toBe("none");

      // Account-level and window-level pin buttons both work inside Table view and sync back to Cards view
      const pinAgy = table.querySelector<HTMLButtonElement>('[data-usage-pin="agy-main"]')!;
      await act(async () => pinAgy.click());
      expect(pinAgy.getAttribute("aria-pressed")).toBe("true");

      const agyGemini7dPin = usageWindowPinId("agy-main", multiAccounts()[2].windows[3]);
      const pinAgyWin = table.querySelector<HTMLButtonElement>(`[data-usage-pin-window="${agyGemini7dPin}"]`)!;
      expect(pinAgyWin).not.toBeNull();
      await act(async () => pinAgyWin.click());
      expect(pinAgyWin.getAttribute("aria-pressed")).toBe("true");

      // Switch back to Cards view: the pinned window stays pressed inside its grouped pool
      const cardsBtn = host.querySelector<HTMLButtonElement>('[data-usage-view="cards"]')!;
      await act(async () => cardsBtn.click());
      expect(host.querySelector("[data-usage-table]")).toBeNull();
      expect(localStorage.getItem(USAGE_VIEW_MODE_KEY)).toBeNull();
      expect(host.querySelector(`[data-usage-pin-window="${agyGemini7dPin}"]`)?.getAttribute("aria-pressed")).toBe("true");
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }

    // Table layout geometry: wide single-column width on desktop, clamped safely on narrow mobile viewports
    const desktopTable = computeUsagePopoverLayout({
      viewMode: "table",
      tableColCount: 2,
      viewportWidth: 1440,
      viewportHeight: 900,
      anchorRect: { left: 12, top: 800 },
    });
    expect(desktopTable.cols).toBe(1);
    expect(desktopTable.width).toBeGreaterThanOrEqual(600);
    expect(desktopTable.width).toBeLessThanOrEqual(820);

    const narrowTable = computeUsagePopoverLayout({
      viewMode: "table",
      tableColCount: 3,
      viewportWidth: 375,
      viewportHeight: 667,
      anchorRect: { left: 10, top: 600 },
    });
    expect(narrowTable.cols).toBe(1);
    expect(narrowTable.width).toBeLessThanOrEqual(375 - 16 - 10);
  });

  it.each([
    ["en", "Cards", "Table", "Account", "Next:"],
    ["zh-Hans", "卡片", "表格", "账号", "最近："],
    ["ja", "カード", "テーブル", "アカウント", "次回："],
  ])("localizes view switcher, table headings, and collapsed timeline summary in %s", async (lang, cardsLabel, tableLabel, acctHead, nextPrefix) => {
    await initI18n(lang);
    try {
      const cardsHtml = renderToStaticMarkup(<UsagePopover accounts={multiAccounts()} now={NOW} />);
      expect(cardsHtml).toContain(cardsLabel);
      expect(cardsHtml).toContain(tableLabel);
      expect(cardsHtml).toContain(nextPrefix);

      const tableHtml = renderToStaticMarkup(<UsagePopover accounts={multiAccounts()} now={NOW} defaultViewMode="table" />);
      expect(tableHtml).toContain(acctHead);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("never renders 'Fable · Fable' or drops '7d' when an extension sends matching title and model, and aligns all bars across mixed rows", () => {
    const claudeWithDupTitle: UsageAccount = {
      id: "cl-fable",
      provider: "claude",
      name: "Claude #team",
      plan: "max",
      sampled_at: iso(-240_000),
      sources: ["Pool"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 2, resets_at: iso(4.4 * H) },
        { label: "7d", window_minutes: 10080, used_percent: 0 }, // 100% left, NO resets_at
        { label: "7d", window_minutes: 10080, used_percent: 18, resets_at: iso(48 * H), model: "Fable", title: "Fable" },
      ],
    };
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[claudeWithDupTitle]} now={NOW} />);

    const card = host.querySelector('[data-usage-provider="claude"]')!;
    expect(card.textContent).not.toContain("Fable · Fable");

    const rows = [...card.querySelectorAll<HTMLButtonElement>("[data-window-row]")];
    expect(rows).toHaveLength(3);
    // Every row keeps fixed column 1 span ("5h", "7d", "7d") and identical ruler-aligned grid columns
    expect(rows.map((r) => r.querySelector("[data-window-span]")?.textContent)).toEqual(["5h", "7d", "7d"]);
    const expectedGrid = "grid-cols-[1.75rem_minmax(2.5rem,1fr)_2.25rem_3.75rem_0.75rem]";
    for (const row of rows) {
      expect(row.className).toContain(expectedGrid);
      expect(row.querySelector("[data-usage-percent]")?.className).toContain("w-9");
    }
    // The 100% row without reset time still reserves fixed reset & chevron placeholders so its bar aligns with siblings
    expect(rows[1].querySelector("[data-window-reset-placeholder]")).not.toBeNull();
    expect(rows[1].querySelector("[data-window-chevron-placeholder]")).not.toBeNull();

    // The Fable row places its deduplicated caption in col-start-2 directly above the bar, preserving "7d" in col-start-1
    const fableRow = rows[2];
    expect(fableRow.getAttribute("data-window-inline-model")).toBe("true");
    const modelCaption = fableRow.querySelector("[data-usage-model]")!;
    expect(modelCaption.textContent).toBe("Fable");
    expect(modelCaption.className).toContain("col-start-2");
    expect(fableRow.querySelector('[role="progressbar"]')?.getAttribute("aria-label")).toBe("Claude #team 7d（Fable）：剩余 82%");

    // Unpinned desktop window pins use low-interference overlay mode without eating meter width
    const winPins = [...card.querySelectorAll("[data-usage-pin-window]")];
    expect(winPins).toHaveLength(3);
    for (const pin of winPins) {
      expect(pin.className).toContain("usage-win-pin");
      expect(pin.getAttribute("data-pin-pinned")).toBe("false");
      expect(pin.getAttribute("data-pin-overlay")).toBe("true");
    }
  });

  it("merges redundant single-account provider headers and suppresses duplicate per-account sample ages", () => {
    const agySingle: UsageAccount = {
      id: "agy-single",
      provider: "antigravity",
      name: "Antigravity",
      plan: "ultra",
      sampled_at: iso(-240_000),
      local: true,
      machines: ["devbox"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 15, resets_at: iso(3 * H), model: "Gemini Models" },
        { label: "7d", window_minutes: 10080, used_percent: 30, resets_at: iso(48 * H), model: "Gemini Models" },
      ],
    };
    const claudeA: UsageAccount = {
      id: "cl-a",
      provider: "claude",
      name: "Claude #one",
      sampled_at: iso(-240_000), // 4分钟前 (same as card header)
      sources: ["Gateway"],
      windows: [{ label: "5h", window_minutes: 300, used_percent: 20, resets_at: iso(2 * H) }],
    };
    const claudeB: UsageAccount = {
      id: "cl-b",
      provider: "claude",
      name: "Claude #two",
      sampled_at: iso(-240_000), // 4分钟前 (same as card header)
      sources: ["Gateway"],
      windows: [{ label: "5h", window_minutes: 300, used_percent: 40, resets_at: iso(3 * H) }],
    };
    const claudeOld: UsageAccount = {
      id: "cl-c",
      provider: "claude",
      name: "Claude #older",
      sampled_at: iso(-15 * 60_000), // 15分钟前 (differs from card header -> MUST show)
      sources: ["Gateway"],
      windows: [{ label: "5h", window_minutes: 300, used_percent: 10, resets_at: iso(4 * H) }],
    };

    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={[agySingle, claudeA, claudeB, claudeOld]} now={NOW} />);

    // Single Antigravity card merges title + plan + pin into card header without repeating <h3>Antigravity</h3> or "1 个账号"
    const agyCard = host.querySelector('[data-usage-provider="antigravity"]')!;
    expect(agyCard.querySelectorAll("h3")).toHaveLength(1);
    expect(agyCard.querySelector("h3")?.textContent).toBe("Antigravity");
    expect(agyCard.textContent).not.toContain("1 个账号");
    expect(agyCard.querySelector("[data-usage-merged-header='true']")).not.toBeNull();
    expect(agyCard.querySelector("header [data-usage-plan]")?.textContent).toBe("Ultra");
    expect(agyCard.querySelector('header [data-usage-pin="agy-single"]')).not.toBeNull();
    expect(agyCard.querySelector("[data-usage-source]")?.textContent).toBe("devbox");

    // Claude card shows "4分钟前" once in card header, hides matching ages on #one/#two, and shows distinct "15分钟前" on #older
    const claudeCard = host.querySelector('[data-usage-provider="claude"]')!;
    expect(claudeCard.querySelector("[data-usage-card-age]")?.textContent).toBe("4分钟前");
    expect(claudeCard.textContent?.match(/4分钟前/g)).toHaveLength(1);
    const acctAges = [...claudeCard.querySelectorAll("[data-usage-account-age]")].map((el) => el.textContent);
    expect(acctAges).toEqual(["15分钟前"]);
  });

  it("collapses stale overlapping local accounts by default in Cards and Table views and expands on click", async () => {
    const staleLocal: UsageAccount = {
      id: "codex-local-stale",
      provider: "codex",
      name: "Codex Promax",
      plan: "pro",
      sampled_at: iso(-2 * H),
      stale: true,
      local: true,
      machines: ["devbox"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 25, resets_at: iso(2 * H) },
        { label: "7d", window_minutes: 10080, used_percent: 45, resets_at: iso(48 * H) },
      ],
    };
    const liveExternal: UsageAccount = {
      id: "codex-ext-live",
      provider: "codex",
      name: "Magpie c13f4ab515d6",
      plan: "pro",
      sampled_at: iso(-120_000),
      sources: ["Magpie"],
      windows: [
        { label: "5h", window_minutes: 300, used_percent: 18, resets_at: iso(2 * H + 15_000) },
        { label: "7d", window_minutes: 10080, used_percent: 42, resets_at: iso(48 * H + 10_000) },
      ],
    };

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<UsagePopover accounts={[staleLocal, liveExternal]} now={NOW} />));
      const collapsed = host.querySelector('[data-usage-stale-overlap="collapsed"]')!;
      expect(collapsed).not.toBeNull();
      // Collapsed by default: stale local windows are not taking card space until expanded
      expect(collapsed.querySelectorAll("[data-window-row]")).toHaveLength(0);
      expect(collapsed.textContent).toContain("Codex Promax");
      expect(collapsed.textContent).toContain("Magpie c13f4ab515d6");

      // Expand stale overlapping account on click -> reveals original stale windows
      const toggle = collapsed.querySelector<HTMLButtonElement>("[data-usage-stale-overlap-toggle]")!;
      await act(async () => toggle.click());
      const expanded = host.querySelector('[data-usage-stale-overlap="expanded"]')!;
      expect(expanded).not.toBeNull();
      expect(expanded.querySelectorAll("[data-window-row]")).toHaveLength(2);

      // Collapsed timeline summary keeps window and countdown non-truncating even for long account names
      const summary = host.querySelector("[data-usage-timeline-summary]")!;
      expect(summary.querySelector("[data-usage-timeline-next-name]")?.className).toContain("truncate");
      expect(summary.querySelector("[data-usage-timeline-next-time]")?.textContent).toBe("· 5h · 2h");
      expect(summary.querySelector("[data-usage-timeline-next-time]")?.className).toContain("shrink-0");

      // Table view also collapses stale overlapping local account by default and expands on click
      await act(async () => host.querySelector<HTMLButtonElement>('[data-usage-view="table"]')!.click());
      const tableCollapsed = host.querySelector('tr[data-usage-stale-overlap="collapsed"]')!;
      expect(tableCollapsed).not.toBeNull();
      expect(tableCollapsed.querySelectorAll("[data-window-row]")).toHaveLength(0);
      await act(async () => tableCollapsed.querySelector<HTMLButtonElement>("[data-usage-stale-overlap-toggle]")!.click());
      const tableExpanded = host.querySelector('tr[data-usage-stale-overlap="expanded"]')!;
      expect(tableExpanded).not.toBeNull();
      expect(tableExpanded.querySelectorAll("[data-window-row]")).toHaveLength(2);
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
});

describe("Codex historical rollout readings", () => {
  const live = account("verified", 20, 30, { local: true, machines: ["Machine A"] });
  const old = { ...account("rollout", 68, 44, { local: true, machines: ["Machine B"], sampled_at: iso(-H), stale: true }), reading_source: "rollout" } as const;

  function panel(accounts: UsageAccount[], mode: "cards" | "table" = "cards") {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<UsagePopover accounts={accounts} defaultViewMode={mode} now={NOW} defaultTimelineOpen />);
    return host;
  }

  it("counts one account and draws one reset row while retaining the old sample", () => {
    const host = panel([live, old]);
    expect({
      count: host.querySelector('[data-usage-provider="codex"] > header')?.textContent?.match(/\d+ 个账号/)?.[0],
      resets: [...host.querySelectorAll("[data-timeline-row]")].map(row => row.getAttribute("data-timeline-row")),
    }).toEqual({ count: "1 个账号", resets: ["verified"] });
    for (const mode of ["cards", "table"] as const) {
      const view = panel([live, old], mode);
      const source = view.querySelector("details")!;
      expect(source.open).toBe(false);
      source.querySelector("summary")!.click();
      expect(source.open).toBe(true);
      expect(source.textContent).toContain("Machine B");
      expect(source.querySelector("time")?.dateTime).toBe(iso(-H));
      expect(source.textContent).toContain("32%");
      expect(source.querySelector("dd time")?.getAttribute("datetime")).toBe(old.windows[0].resets_at);
      if (mode === "table") expect(view.querySelectorAll("[data-usage-table-row]")).toHaveLength(1);
    }
  });

  it("keeps a source-only panel reachable without adding an account or a reset", () => {
    const sidebar = document.createElement("div");
    sidebar.innerHTML = renderToStaticMarkup(<UsageBars accounts={[old]} now={NOW} />);
    expect(sidebar.querySelector('button[aria-haspopup="dialog"]')).not.toBeNull();
    expect(sidebar.querySelector('[role="progressbar"]')).toBeNull();
    for (const mode of ["cards", "table"] as const) {
      const host = panel([old], mode);
      expect(host.querySelector("details")?.textContent).toContain("Machine B");
      expect(host.querySelector("[data-timeline-row], [data-usage-table-row]")).toBeNull();
    }
  });

  it("keeps both accounts at a shared reset, including old-hub and keyless team readings", () => {
    const team = account("team", 55, 11, { name: "Team", local: true, machines: ["Machine C"] });
    for (const other of [team, { ...team, stale: true }, { ...team, reading_source: "rollout" as const }]) {
      const host = panel([live, other]);
      expect(host.querySelector('[data-usage-provider="codex"] > header')?.textContent).toContain("2 个账号");
      expect(host.querySelectorAll("[data-timeline-row]")).toHaveLength(2);
      expect(host.querySelector("details")).toBeNull();
    }
  });
});

describe("browser-local Codex account aliases", () => {
  it("keeps the second same-plan seat distinct in cards, table and timeline after reload, reordering and reset", () => {
    const plan = { name: "Codex Plus", plan: "plus" };
    const first = account("seat-a", 14, 20, plan);
    const second = account("seat-b", 50, 70, plan);
    const historical = account("old", 65, 72, { ...plan, reading_source: "rollout", stale: true });
    const host = document.createElement("div");
    const root = createRoot(host);
    const prompt = vi.spyOn(window, "prompt").mockReturnValue(" 工作 ");
    const pin = () => host.querySelector<HTMLButtonElement>('[data-usage-pin="seat-b"]')!;
    const edit = () => host.querySelector<HTMLButtonElement>('[data-usage-alias="seat-b"] button');
    const headings = () => Array.from(host.querySelectorAll("section[aria-label] > header h3[title]"), (n) => n.textContent);
    const view = (accounts: UsageAccount[], key: number, mode: "cards" | "table") =>
      act(() => root.render(<UsagePopover key={key} accounts={accounts} now={NOW} defaultViewMode={mode} defaultTimelineOpen />));
    try {
      view([first, second, historical], 0, "cards");
      expect(headings()).toEqual(["Codex Plus", "Codex Plus"]);
      act(() => pin().click());
      const pins = localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY);
      expect(edit(), "the selected Codex account has a rename action").not.toBeNull();
      const resetTimes = () => Array.from(host.querySelectorAll("[data-timeline-row]"), (row) => row.getAttribute("title"));
      const beforeResetTimes = resetTimes();
      act(() => edit()!.click());
      expect(resetTimes()).toEqual(beforeResetTimes);
      expect(headings()).toEqual(["Codex Plus", "工作"]);
      expect(pin().getAttribute("aria-label")).toContain("工作");
      expect(host.querySelector('[data-timeline-row="seat-b"]')?.getAttribute("aria-label")).toContain("工作");
      expect(host.querySelector('[data-timeline-row="seat-a"] [data-timeline-account-name]')?.textContent).toBe("Plus");
      expect(host.querySelector('[data-usage-alias="old"]')).toBeNull();
      expect(host.querySelector('[data-timeline-row="old"]')).toBeNull();
      expect(second.name).toBe("Codex Plus");
      act(() => chooseManager("other-manager"));
      expect(headings()).toEqual(["Codex Plus", "Codex Plus"]);
      act(() => chooseManager(null));
      expect(headings()).toEqual(["Codex Plus", "工作"]);
      const refreshed = { ...second, windows: second.windows.map((w) => ({ ...w, resets_at: iso(5 * H), used_percent: 0 })) };
      view([refreshed, historical, first], 1, "table");
      expect(host.querySelectorAll("[data-usage-table-row]")).toHaveLength(2);
      const row = host.querySelector('[data-usage-table-row="seat-b"]')!;
      expect(row.querySelector('[title="工作"]')?.textContent).toBe("工作");
      expect(row.querySelector("[data-usage-plan]")?.textContent).toBe("Plus");
      expect(host.querySelector('[data-timeline-row="seat-b"] [data-timeline-account-name]')?.textContent).toBe("工作");
      expect(localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY)).toBe(pins);
      prompt.mockReturnValue("  ");
      act(() => edit()!.click());
      expect(row.querySelector('[title="Codex Plus"]')?.textContent).toBe("Codex Plus");
      expect(host.querySelector('[data-timeline-row="seat-b"] [data-timeline-account-name]')?.textContent).toBe("Plus");
    } finally {
      prompt.mockRestore();
      act(() => root.unmount());
      chooseManager(null);
      resetPinnedUsageAccounts();
    }
  });

  it("partitions persisted aliases by hub and manager even when their public row IDs match", () => {
    const id = "same-seat";
    const read = (hub: string, manager: string) => {
      const store = createUsageAliasStore(hub, manager);
      function Label() { return store.useValue().get(id) ?? ""; }
      return renderToStaticMarkup(<Label />);
    };
    const store = createUsageAliasStore("https://hub.example/a", "primary");
    store.setAlias(id, "Work");
    try {
      expect(read("https://hub.example/a", "primary")).toBe("Work");
      expect(read("https://hub.example/b", "primary")).toBe("");
      expect(read("https://hub.example/a", "other")).toBe("");
    } finally {
      store.setAlias(id, "");
    }
  });

  it("edits a single merged header, preserves a literal provider prefix, and rejects an overlong alias", () => {
    const host = document.createElement("div");
    const root = createRoot(host);
    const a = account("single", 10, 20, { name: "Codex", plan: "plus" });
    const prompt = vi.spyOn(window, "prompt");
    const edit = (value: string | null) => {
      prompt.mockReturnValue(value);
      act(() => host.querySelector<HTMLButtonElement>('[data-usage-alias="single"] button')!.click());
    };
    try {
      act(() => root.render(<UsagePopover accounts={[a]} now={NOW} defaultTimelineOpen />));
      const label = () => host.querySelector("[data-timeline-account-name]")?.textContent;
      expect(label()).toBe("Codex");
      edit("Codex Studio");
      expect(label()).toBe("Codex Studio");
      expect(host.querySelector("[data-usage-plan]")?.textContent).toBe("Plus");
      edit("a".repeat(33));
      edit(null);
      expect(label()).toBe("Codex Studio");
      edit("");
      expect(label()).toBe("Codex");
    } finally {
      prompt.mockRestore();
      act(() => root.unmount());
    }
  });
});
