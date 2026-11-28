import { afterEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import { balanceAccounts, balanceEmpty, balanceText } from "./usage-balance";
import { accountPace, pace } from "./usage-pace";
import { formatPlanLabel, groupByProvider, planNote, providerTitle, remainingText, sortedWindows, sortForCard, timelinePosition } from "./usage-popover";
import { isAccountPaused, isWindowPaused, providerTimeline, stripProviderPrefix, unifiedTimeline, windowHeadroom } from "./usage-timeline";
import { countdown, fullest, isBalanceAccount, isPinnableUsageAccount, liveAccounts, parsePinnedUsageAccounts, parseUsageWindowPinId, PINNED_USAGE_ACCOUNTS_KEY, protectReserve, remainingPercent, remainingTone, resetLabel, resetPinnedUsageAccounts, sampleAge, selectSidebarUsageAccounts, selectSidebarUsageItems, setPinnedUsageAccounts, sidebarWindowTitle, sourceNames, tightestRemaining, togglePinnedUsageAccount, topAccounts, usageWindowPinId, windowKey, windowMinutes, type UsageAccount, type UsageWindow } from "./usage-windows";

const NOW = new Date(2026, 10, 5, 10, 0, 0).getTime(); // local time, so same-day checks do not depend on the zone
const at = (ms: number) => new Date(NOW + ms).toISOString();
const H = 3600_000;
const win = (label: string, minutes: number, used: number, resetsIn?: number): UsageWindow => ({ label, window_minutes: minutes, used_percent: used, resets_at: resetsIn === undefined ? undefined : at(resetsIn) });

describe("window length", () => {
  it("reads the label", () => {
    expect(windowMinutes({ label: "5h", window_minutes: 0 })).toBe(300);
    expect(windowMinutes({ label: "7d", window_minutes: 0 })).toBe(10080);
    expect(windowMinutes({ label: "45m", window_minutes: 0 })).toBe(45);
    expect(windowMinutes({ label: "1w", window_minutes: 0 })).toBe(10080);
  });
  it("falls back to the given minutes", () => {
    expect(windowMinutes({ label: "月", window_minutes: 600 })).toBe(600);
    expect(windowMinutes({ label: "月", window_minutes: 0 })).toBe(0);
  });
});

describe("pace", () => {
  it("is over when use is more than 5 points above an even spread", () => {
    // 5h window, 2h elapsed = 40% expected
    expect(pace(win("5h", 300, 50, 3 * H), NOW)).toEqual({ expected: 40, state: "over" });
  });
  it("is under when more than 5 below", () => {
    expect(pace(win("5h", 300, 30, 3 * H), NOW)?.state).toBe("under");
  });
  it("is on pace within 5 points, edges included", () => {
    expect(pace(win("5h", 300, 45, 3 * H), NOW)?.state).toBe("on");
    expect(pace(win("5h", 300, 35, 3 * H), NOW)?.state).toBe("on");
    expect(pace(win("5h", 300, 45.1, 3 * H), NOW)?.state).toBe("over");
  });
  it("says nothing in the first 5% of the window", () => {
    // 7d window: 5% is 8.4h
    expect(pace(win("7d", 10080, 90, 7 * 24 * H - 8 * H), NOW)).toBeNull();
    expect(pace(win("7d", 10080, 90, 7 * 24 * H - 9 * H), NOW)?.state).toBe("over");
  });
  it("needs a reset time and a length, and a time inside the window", () => {
    expect(pace(win("5h", 300, 50), NOW)).toBeNull();
    expect(pace(win("?", 0, 50, H), NOW)).toBeNull();
    expect(pace(win("5h", 300, 50, 6 * H), NOW)).toBeNull(); // not started: the reset is further than the length
    expect(pace(win("5h", 300, 50, -H), NOW)).toBeNull(); // already reset
  });
});

describe("accountPace", () => {
  it("is the window's pace for a fresh reading", () => {
    expect(accountPace({}, win("5h", 300, 50, 3 * H), NOW)).toEqual({ expected: 40, state: "over" });
    expect(accountPace({ stale: false }, win("5h", 300, 30, 3 * H), NOW)?.state).toBe("under");
  });
  it("says nothing for an out-of-date reading (#12411)", () => {
    expect(accountPace({ stale: true }, win("5h", 300, 50, 3 * H), NOW)).toBeNull();
    expect(accountPace({ stale: true }, win("7d", 10080, 100, 4 * 24 * H), NOW)).toBeNull();
  });
});

describe("countdown", () => {
  it("is minutes within the hour", () => {
    expect(countdown(at(45 * 60_000), NOW)).toBe("45 分钟");
    expect(countdown(at(30_000), NOW)).toBe("1 分钟");
  });
  it("is the clock time later the same day", () => {
    expect(countdown(at(3 * H + 5 * 60_000), NOW)).toBe("13:05");
  });
  it("is days and hours beyond today, in the language's own units", () => {
    expect(countdown(at(2 * 24 * H + 3 * H), NOW)).toBe("2 天 3 小时");
    expect(countdown(at(15 * H), NOW)).toBe("15 小时"); // tomorrow, under a day
  });
  it("leaves out a zero hour", () => {
    expect(countdown(at(5 * 24 * H), NOW)).toBe("5 天");
    expect(countdown(at(5 * 24 * H + 30 * 60_000), NOW)).toBe("5 天"); // the hours are whole: 30 minutes over is still 5 days
  });
  it("handles a due or missing time", () => {
    expect(countdown(at(-1000), NOW)).toBe("即将重置");
    expect(countdown(undefined, NOW)).toBe("");
    expect(countdown("soon", NOW)).toBe("");
  });
});

describe("reset phrase", () => {
  it("is relative for minutes and days, a clock time for later today", () => {
    expect(resetLabel(at(45 * 60_000), NOW)).toBe("45 分钟后重置");
    expect(resetLabel(at(3 * H + 5 * 60_000), NOW)).toBe("13:05 重置");
    expect(resetLabel(at(2 * 24 * H + 3 * H), NOW)).toBe("2 天 3 小时后重置");
    expect(resetLabel(at(5 * 24 * H), NOW)).toBe("5 天后重置");
    expect(resetLabel(at(-1), NOW)).toBe("即将重置");
    expect(resetLabel(undefined, NOW)).toBe("");
  });
  // each language its own units: no Latin unit letters in Chinese ("55m", "2d 3h") and no zero hour ("5d 0h")
  it.each([
    ["en", ["Resets in 45m", "Resets in 2d 3h", "Resets in 5d", "Resets in 15h"]],
    ["ja", ["45分後にリセット", "2日3時間後にリセット", "5日後にリセット", "15時間後にリセット"]],
    ["zh-Hans", ["45 分钟后重置", "2 天 3 小时后重置", "5 天后重置", "15 小时后重置"]],
  ])("reads naturally in %s", async (lang, want) => {
    await initI18n(lang);
    try {
      expect([45 * 60_000, 2 * 24 * H + 3 * H, 5 * 24 * H, 15 * H].map((ms) => resetLabel(at(ms), NOW))).toEqual(want);
    } finally {
      await initI18n("zh-Hans");
    }
  });
});

describe("sample age", () => {
  it("counts seconds first", () => {
    expect(sampleAge(at(-12_000), NOW)).toBe("12秒钟前");
    expect(sampleAge(at(5_000), NOW)).toBe("0秒钟前"); // a clock a little ahead
    expect(sampleAge(at(-5 * 60_000), NOW)).toBe("5分钟前");
    expect(sampleAge(at(-2 * H), NOW)).toBe("2小时前");
    expect(sampleAge(at(-50 * H), NOW)).toBe("2天前");
    expect(sampleAge("x", NOW)).toBe("");
  });
});

const account = (id: string, used: number, o: Partial<UsageAccount> = {}): UsageAccount => ({ id, provider: "codex", name: id, windows: [win("7d", 10080, used, 24 * H)], sampled_at: at(-60_000), ...o });

describe("which accounts show", () => {
  it("drops samples older than three days and windows that reset", () => {
    const got = liveAccounts(
      [account("old", 10, { sampled_at: at(-73 * H) }), account("ok", 10), { ...account("mixed", 10), windows: [win("5h", 300, 99, -H), win("7d", 10080, 3, 24 * H)] }, { ...account("none", 1), windows: [win("5h", 300, 99, -H)] }],
      NOW,
    );
    expect(got.map((a) => a.id)).toEqual(["ok", "mixed"]);
    expect(got[1].windows.map((w) => w.label)).toEqual(["7d"]);
  });
  it("keeps notice-only accounts in liveAccounts while excluding them from topAccounts bars and pinning", () => {
    const noticeOnly: UsageAccount = { id: "cool", provider: "claude", name: "Cooldown seat", windows: [], sampled_at: at(-60_000), notice: "Cooldown · resumes in 20m" };
    const blankNotice: UsageAccount = { id: "blank", provider: "claude", name: "Blank", windows: [], sampled_at: at(-60_000), notice: "   " };
    const ok = account("ok", 40);
    const got = liveAccounts([noticeOnly, blankNotice, ok], NOW);
    expect(got.map((a) => a.id)).toEqual(["cool", "ok"]);
    expect(isPinnableUsageAccount(noticeOnly)).toBe(false);
    expect(isPinnableUsageAccount(ok)).toBe(true);
    expect(topAccounts(got).map((a) => a.id)).toEqual(["ok"]);
    expect(selectSidebarUsageAccounts(got, ["cool"])).toEqual({ bars: [expect.objectContaining({ id: "ok" })], balances: [], custom: false });
  });
  it("ages kept numbers by stale_at, whatever the hub said when it answered (#1905)", () => {
    const kept = account("kept", 10, { stale_at: at(-H) }); // fresh when the hub answered, an hour past its time now
    const fresh = account("fresh", 10, { stale_at: at(H) });
    const older = account("older", 10); // an older hub sends no stale_at: its own mark stands
    const got = liveAccounts([kept, fresh, older], NOW);
    expect(got.map((a) => Boolean(a.stale))).toEqual([true, false, false]);
    expect(topAccounts(got).map((a) => a.id)).toEqual(["fresh", "older"]); // stale last
    expect(liveAccounts([account("flagged", 10, { stale: true, stale_at: at(H) })], NOW)[0].stale).toBe(true);
  });
  it("ranks by lowest headroom h = r - R (taking protection reserve into account), stale last", () => {
    const list = [account("a", 20), account("b", 90, { stale: true }), account("c", 60), account("d", 40)];
    expect(topAccounts(list).map((a) => a.id)).toEqual(["c", "d"]);
    expect(topAccounts([account("a", 20), account("b", 90, { stale: true })]).map((a) => a.id)).toEqual(["a", "b"]);
    expect(fullest({ ...account("x", 1), windows: [win("5h", 300, 30), win("7d", 10080, 70)] })).toBe(70);

    // Protected account with 30% remaining and 20% reserve (h = 10) ranks ahead of unprotected 15% remaining (h = 15)
    const protectedTight: UsageAccount = {
      id: "prot",
      provider: "claude",
      name: "Protected",
      sampled_at: at(-60_000),
      windows: [{ ...win("7d", 10080, 70, 24 * H), protect_percent: 80 }],
    };
    const unprotectedHigh = account("raw", 85); // r = 15, R = 0, h = 15
    const comfortable = account("ok", 30); // r = 70, R = 0, h = 70
    expect(topAccounts([comfortable, unprotectedHigh, protectedTight]).map((a) => a.id)).toEqual(["prot", "raw"]);
    expect(tightestRemaining(protectedTight)).toEqual({ remaining: 30, reserve: 20, headroom: 10, paused: false });
  });
  it("keeps tied sidebar percentages in provider and ID order across polls", () => {
    const first = [
      account("third", 38.1),
      account("second", 38.4, { provider: "agy" }),
      account("first", 38, { provider: "antigravity" }),
    ];
    const next = [
      account("first", 38.4, { provider: "agy" }),
      account("third", 38.4),
      account("second", 38, { provider: "antigravity" }),
    ];
    for (const poll of [first, next]) {
      expect(topAccounts(poll).map((a) => a.id)).toEqual(["first", "second"]);
    }
    expect(topAccounts([account("low", 38), account("high", 39)]).map((a) => a.id)).toEqual(["high", "low"]);
  });
});

describe("accounts with several sources and per-model windows (#2595)", () => {
  const account = (o: Partial<UsageAccount> = {}): UsageAccount => ({ id: "a", provider: "claude", name: "seat", sampled_at: at(-1000), windows: [win("5h", 300, 30, 2 * H), win("7d", 10080, 40, 24 * H)], ...o });
  it("sorts by the plan's own fullest window, not a model's that ran out", () => {
    const scoped = { ...win("7d", 10080, 100, 24 * H), model: "Fable" };
    const a = account({ windows: [win("7d", 10080, 40, 24 * H), scoped] });
    expect(fullest(a)).toBe(40);
    expect(fullest(account({ windows: [scoped] }))).toBe(100); // nothing else to go by
    expect(topAccounts([account({ id: "x", windows: [scoped, win("7d", 10080, 10, 24 * H)] }), account({ id: "y" })], 1)[0].id).toBe("y");
  });
  it("keeps healthy sibling model pools from reading 0% when one model pool is exhausted", () => {
    const multiPool: UsageAccount = account({
      id: "acct-alpha",
      provider: "antigravity",
      name: "Antigravity",
      windows: [
        { ...win("5h", 300, 100, 2 * H), model: "model-a" },
        { ...win("7d", 10080, 100, 48 * H), model: "model-a" },
        { ...win("5h", 300, 20, 3 * H), model: "model-b" },
        { ...win("7d", 10080, 38, 72 * H), model: "model-b" },
      ],
    });
    expect(tightestRemaining(multiPool)).toEqual({ remaining: 62, reserve: 0, headroom: 62, paused: false });
    expect(fullest(multiPool)).toBe(38);

    // Exhausted model-a does not hijack topAccounts ahead of genuinely tighter accounts.
    const tighterA = account({ id: "acct-beta", provider: "codex", name: "Codex", windows: [win("7d", 10080, 75, 24 * H)] }); // 25% left
    const tighterB = account({ id: "acct-gamma", provider: "claude", name: "Claude", windows: [win("7d", 10080, 60, 24 * H)] }); // 40% left
    expect(topAccounts([multiPool, tighterA, tighterB]).map((a) => a.id)).toEqual(["acct-beta", "acct-gamma"]);

    // Within one model pool, 5h exhaustion still constrains that pool (case/whitespace normalized) without leaking its 7d remaining.
    const partialDepleted: UsageAccount = account({
      id: "acct-delta",
      provider: "antigravity",
      name: "Antigravity",
      windows: [
        { ...win("5h", 300, 100, 2 * H), model: "Model-A" },
        { ...win("7d", 10080, 10, 48 * H), model: " model-a " }, // same pool as 5h -> pool tightest is 0%
        { ...win("5h", 300, 30, 3 * H), model: "model-b" },
        { ...win("7d", 10080, 15, 72 * H), model: "model-b" }, // pool tightest is 70%
      ],
    });
    expect(tightestRemaining(partialDepleted)).toEqual({ remaining: 70, reserve: 0, headroom: 70, paused: false });
    expect(fullest(partialDepleted)).toBe(30);

    // When a single model pool or every model pool is exhausted, the account reads 0%.
    const singlePool = account({
      id: "acct-single",
      windows: [
        { ...win("5h", 300, 100, 2 * H), model: "model-a" },
        { ...win("7d", 10080, 20, 48 * H), model: "model-a" },
      ],
    });
    expect(tightestRemaining(singlePool)).toEqual({ remaining: 0, reserve: 0, headroom: 0, paused: false });
    const allDepleted = account({
      id: "acct-empty",
      windows: [
        { ...win("5h", 300, 100, 2 * H), model: "model-a" },
        { ...win("5h", 300, 100, 3 * H), model: "model-b" },
        { ...win("7d", 10080, 20, 72 * H), model: "model-b" },
      ],
    });
    expect(tightestRemaining(allDepleted)).toEqual({ remaining: 0, reserve: 0, headroom: 0, paused: false });
    expect(fullest(allDepleted)).toBe(100);

    // Protected model pools: one paused pool does not mark the account paused while another pool has headroom.
    const onePaused = account({
      id: "acct-prot",
      windows: [
        { ...win("7d", 10080, 96, 48 * H), model: "model-a", protect_percent: 95 }, // r=4, R=5, h=-1, paused
        { ...win("7d", 10080, 20, 48 * H), model: "model-b", protect_percent: 95 }, // r=80, R=5, h=75
      ],
    });
    expect(tightestRemaining(onePaused)).toEqual({ remaining: 80, reserve: 5, headroom: 75, paused: false });

    const allPaused = account({
      id: "acct-all-paused",
      windows: [
        { ...win("7d", 10080, 98, 48 * H), model: "model-a", protect_percent: 95 }, // r=2, R=5, h=-3, paused
        { ...win("7d", 10080, 96, 48 * H), model: "model-b", protect_percent: 95 }, // r=4, R=5, h=-1, paused
      ],
    });
    expect(tightestRemaining(allPaused)).toEqual({ remaining: 4, reserve: 5, headroom: -1, paused: true });
  });
  it("names where an account was seen", () => {
    expect(sourceNames(account({ local: true, machines: ["MacBook", "Studio"], sources: ["Relay"] }))).toEqual(["MacBook", "Studio", "Relay"]);
    expect(sourceNames(account({ local: true, sources: ["Relay"] }))).toEqual(["电脑", "Relay"]); // an older hub: no names, never "this computer" (#1907)
    expect(sourceNames(account({ sources: ["Relay", "Team pool"] }))).toEqual(["Relay", "Team pool"]);
    expect(sourceNames(account())).toEqual([]); // an older hub says nothing about it
  });
  it("shows the normalized plan unless the name has it", () => {
    expect(planNote(account({ name: "Codex Pro", plan: "pro" }))).toBe("");
    expect(planNote(account({ name: "work seat", plan: "Pro" }))).toBe("Pro");
    expect(planNote(account({ name: "work seat", plan: "max" }))).toBe("Max");
    expect(planNote(account({ name: "work seat", plan: "claude_max_20x" }))).toBe("Claude Max 20x");
    expect(planNote(account({ name: "work seat", plan: "api" }))).toBe("API");
    expect(planNote(account({ name: "work seat", plan: "Pro+" }))).toBe("Pro+");
    expect(formatPlanLabel("enterprise")).toBe("Enterprise");
    expect(formatPlanLabel("xAI")).toBe("xAI");
    expect(planNote(account({ plan: " " }))).toBe("");
  });
  it("tells windows of one length apart by model, group, and title", () => {
    expect(windowKey(win("7d", 10080, 1))).not.toBe(windowKey({ ...win("7d", 10080, 1), model: "Fable" }));
    expect(windowKey({ ...win("7d", 10080, 1), group: "Pool A" })).not.toBe(windowKey({ ...win("7d", 10080, 1), group: "Pool B" }));
    expect(windowKey({ ...win("5h", 300, 1), group: "Pool A", title: "Burst" })).not.toBe(windowKey({ ...win("5h", 300, 1), group: "Pool A", title: "Steady" }));
  });
});

describe("prepaid balances (#11825)", () => {
  const bal = (id: string, amount: string, o: Partial<UsageAccount> = {}): UsageAccount => ({ id, provider: "deepseek", name: id, windows: [], sampled_at: at(-60_000), balance: { amount, currency: "CNY" }, ...o });
  it("keeps a balance account without windows, and it takes no bar place from a window account", () => {
    const got = liveAccounts([bal("ds", "12.34"), account("ok", 10)], NOW);
    expect(got.map((a) => a.id)).toEqual(["ds", "ok"]);
    expect(isBalanceAccount(got[0])).toBe(true);
    expect(balanceAccounts(got).map((a) => a.id)).toEqual(["ds"]);
    expect(topAccounts(got).map((a) => a.id)).toEqual(["ok"]);
    expect(balanceText(got[0].balance!)).toBe("CNY 12.34");
  });
  it("leaves a DeepSeek account that reports windows as an ordinary bar account", () => {
    const gateway: UsageAccount = { id: "ds-window", provider: "deepseek", name: "DeepSeek gateway", windows: [win("7d", 10080, 10, 24 * H)], sampled_at: at(-60_000) };
    expect(isBalanceAccount(gateway)).toBe(false);
    expect(balanceAccounts([gateway])).toEqual([]);
    expect(topAccounts([gateway]).map((a) => a.id)).toEqual(["ds-window"]);
  });
  it("keeps a genuine zero apart from a failed read", () => {
    const zero = bal("ds", "0.00");
    const failed: UsageAccount = { ...bal("ds", "0.00"), balance: undefined, problem: "unreachable" };
    expect(balanceEmpty(zero.balance!)).toBe(true);
    expect(liveAccounts([zero], NOW)[0].balance?.amount).toBe("0.00");
    const kept = liveAccounts([failed], NOW);
    expect(kept).toHaveLength(1);
    expect(kept[0].balance).toBeUndefined();
    expect(kept[0].problem).toBe("unreachable");
    expect(isBalanceAccount(kept[0])).toBe(true); // the provider still names a balance row, so the popover can say why
  });
});

describe("pinned sidebar usage accounts (#12418)", () => {
  const bal = (id: string, amount: string): UsageAccount => ({ id, provider: "deepseek", name: "DeepSeek", windows: [], sampled_at: at(-60_000), balance: { amount, currency: "CNY" } });
  const live = (): UsageAccount[] => [
    account("codex", 85, { provider: "codex", name: "Codex Pro" }),
    account("claude", 60, { provider: "claude", name: "Claude Max" }),
    account("agy", 15, { provider: "agy", name: "Antigravity" }),
    bal("ds", "12.34"),
  ];

  it("parses stored IDs with deduplication and tolerates malformed JSON", () => {
    expect(parsePinnedUsageAccounts(null)).toEqual([]);
    expect(parsePinnedUsageAccounts("")).toEqual([]);
    expect(parsePinnedUsageAccounts("{not-json")).toEqual([]);
    expect(parsePinnedUsageAccounts('{"a":1}')).toEqual([]);
    expect(parsePinnedUsageAccounts('["agy", "codex", "agy", "", "  ", 42, " ds "]')).toEqual(["agy", "codex", "ds"]);
  });

  it("keeps automatic top-2 + balance behavior when nothing is pinned", () => {
    const picked = selectSidebarUsageAccounts(live(), []);
    expect(picked.custom).toBe(false);
    expect(picked.bars.map((a) => a.id)).toEqual(["codex", "claude"]);
    expect(picked.balances.map((a) => a.id)).toEqual(["ds"]);
  });

  it("selects pinned window and balance accounts in pinned order", () => {
    const onlyAgy = selectSidebarUsageAccounts(live(), ["agy"]);
    expect(onlyAgy.custom).toBe(true);
    expect(onlyAgy.bars.map((a) => a.id)).toEqual(["agy"]);
    expect(onlyAgy.balances).toEqual([]);

    const mixed = selectSidebarUsageAccounts(live(), ["agy", "ds", "codex"]);
    expect(mixed.custom).toBe(true);
    expect(mixed.bars.map((a) => a.id)).toEqual(["agy", "codex"]);
    expect(mixed.balances.map((a) => a.id)).toEqual(["ds"]);

    const onlyBalance = selectSidebarUsageAccounts(live(), ["ds"]);
    expect(onlyBalance.custom).toBe(true);
    expect(onlyBalance.bars).toEqual([]);
    expect(onlyBalance.balances.map((a) => a.id)).toEqual(["ds"]);
  });

  it("shows surviving pinned accounts on partial loss and falls back cleanly when all pinned accounts are offline", () => {
    const partial = selectSidebarUsageAccounts(live(), ["gone", "agy"]);
    expect(partial.custom).toBe(true);
    expect(partial.bars.map((a) => a.id)).toEqual(["agy"]);
    expect(partial.balances).toEqual([]);

    const fallback = selectSidebarUsageAccounts(live(), ["gone-1", "gone-2"]);
    expect(fallback.custom).toBe(false);
    expect(fallback.bars.map((a) => a.id)).toEqual(["codex", "claude"]);
    expect(fallback.balances).toEqual([live()[3]]);
  });
});

describe("pinned individual usage windows (#13870)", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    resetPinnedUsageAccounts();
  });

  const bal = (id: string, amount: string): UsageAccount => ({ id, provider: "deepseek", name: "DeepSeek", windows: [], sampled_at: at(-60_000), balance: { amount, currency: "CNY" } });
  const agyGemini5h: UsageWindow = { ...win("5h", 300, 82, 3 * H), model: "Gemini Models" };
  const agyGemini7d: UsageWindow = { ...win("7d", 10080, 25, 48 * H), model: "Gemini Models" };
  const agyClaude7d: UsageWindow = { ...win("7d", 10080, 10, 72 * H), model: "Claude and GPT models" };
  const codex5h: UsageWindow = win("5h", 300, 85, 2 * H);
  const codex7d: UsageWindow = win("7d", 10080, 30, 50 * H);

  const live = (): UsageAccount[] => [
    { id: "codex", provider: "codex", name: "Codex Pro", windows: [codex5h, codex7d], sampled_at: at(-60_000) },
    { id: "agy", provider: "agy", name: "Antigravity", windows: [agyGemini5h, agyGemini7d, agyClaude7d], sampled_at: at(-60_000) },
    bal("ds", "12.34"),
  ];

  it("encodes and parses window pin IDs cleanly, including special characters and malformed inputs", () => {
    const id = usageWindowPinId("agy", agyGemini7d);
    expect(id).toBe("window:agy:7d:10080:Gemini Models");
    expect(parseUsageWindowPinId(id)).toEqual({ accountId: "agy", windowKey: windowKey(agyGemini7d) });

    const planId = usageWindowPinId("codex", codex7d);
    expect(planId).toBe("window:codex:7d:10080:");
    expect(parseUsageWindowPinId(planId)).toEqual({ accountId: "codex", windowKey: windowKey(codex7d) });

    const withColons = usageWindowPinId("acct:1/2", { ...win("7d", 10080, 10), model: "google:gemini-2.5" });
    expect(parseUsageWindowPinId(withColons)).toEqual({ accountId: "acct:1/2", windowKey: "7d:10080:google:gemini-2.5" });

    expect(parseUsageWindowPinId("agy")).toBeNull();
    expect(parseUsageWindowPinId("window::7d:10080:")).toBeNull();
    expect(parseUsageWindowPinId("window:agy:")).toBeNull();
    expect(parseUsageWindowPinId("window:%E0%A4%A:7d:10080:")).toBeNull();
  });

  it("formats concise sidebar window titles without duplicating identical account and model names", () => {
    expect(sidebarWindowTitle({ name: "Antigravity" }, agyGemini7d)).toBe("Antigravity · Gemini Models");
    expect(sidebarWindowTitle({ name: "Codex Pro" }, codex7d)).toBe("Codex Pro");
    expect(sidebarWindowTitle({ name: "Gemini" }, { model: "gemini" })).toBe("Gemini");
  });

  it("selects a single pinned window such as agy gemini 7d instead of the account's fullest window", () => {
    const pinId = usageWindowPinId("agy", agyGemini7d);
    const picked = selectSidebarUsageItems(live(), [pinId]);
    expect(picked.custom).toBe(true);
    expect(picked.items).toHaveLength(1);
    expect(picked.items[0]).toMatchObject({ kind: "window", key: pinId, account: { id: "agy" }, window: agyGemini7d });
  });

  it("preserves mixed pin order across multiple windows, legacy account pins, and prepaid balances", () => {
    const agyGem7d = usageWindowPinId("agy", agyGemini7d);
    const codex7dPin = usageWindowPinId("codex", codex7d);
    const agyClaudePin = usageWindowPinId("agy", agyClaude7d);
    const picked = selectSidebarUsageItems(live(), [agyGem7d, "ds", codex7dPin, "codex", agyClaudePin]);
    expect(picked.custom).toBe(true);
    expect(picked.items.map((item) => [item.kind, item.key])).toEqual([
      ["window", agyGem7d],
      ["balance", "ds"],
      ["window", codex7dPin],
      ["account", "codex"],
      ["window", agyClaudePin],
    ]);
  });

  it("keeps surviving pins when a pinned window disappears and falls back to automatic when all pinned windows vanish", () => {
    const missingWindow = usageWindowPinId("agy", { ...win("7d", 10080, 0), model: "Retired Model" });
    const liveWindow = usageWindowPinId("codex", codex7d);

    const partial = selectSidebarUsageItems(live(), [missingWindow, liveWindow]);
    expect(partial.custom).toBe(true);
    expect(partial.items.map((i) => i.key)).toEqual([liveWindow]);

    const fallback = selectSidebarUsageItems(live(), [missingWindow]);
    expect(fallback.custom).toBe(false);
    expect(fallback.items.map((i) => [i.kind, i.key])).toEqual([
      ["account", "codex"],
      ["account", "agy"],
      ["balance", "ds"],
    ]);
  });

  it("keeps in-memory pin state intact without reverting to stale storage when localStorage writes throw", () => {
    let stored: string | null = JSON.stringify(["codex"]);
    vi.stubGlobal("localStorage", {
      getItem: (key: string) => (key === PINNED_USAGE_ACCOUNTS_KEY ? stored : null),
      setItem: () => { throw new Error("QuotaExceededError"); },
      removeItem: () => { throw new Error("SecurityError"); },
    });

    const agyGem7d = usageWindowPinId("agy", agyGemini7d);
    expect(togglePinnedUsageAccount(agyGem7d)).toEqual(["codex", agyGem7d]);
    // Subsequent toggle reads in-memory state rather than reverting to stale stored '["codex"]'
    expect(togglePinnedUsageAccount("ds")).toEqual(["codex", agyGem7d, "ds"]);
    expect(togglePinnedUsageAccount("codex")).toEqual([agyGem7d, "ds"]);
    // Even if storage changes behind the scenes while volatile, in-memory state stays authoritative for the tab
    stored = JSON.stringify(["codex"]);
    expect(togglePinnedUsageAccount(agyGem7d)).toEqual(["ds"]);
    expect(resetPinnedUsageAccounts()).toEqual([]);
    expect(setPinnedUsageAccounts([agyGem7d])).toEqual([agyGem7d]);
  });

  it("works in memory when localStorage is undefined or getItem throws", () => {
    const agyGem7d = usageWindowPinId("agy", agyGemini7d);
    expect(togglePinnedUsageAccount(agyGem7d)).toEqual([agyGem7d]);
    expect(togglePinnedUsageAccount("codex")).toEqual([agyGem7d, "codex"]);
    expect(resetPinnedUsageAccounts()).toEqual([]);

    vi.stubGlobal("localStorage", {
      getItem: () => { throw new Error("SecurityError"); },
      setItem: () => { throw new Error("SecurityError"); },
      removeItem: () => { throw new Error("SecurityError"); },
    });
    expect(togglePinnedUsageAccount(agyGem7d)).toEqual([agyGem7d]);
    expect(togglePinnedUsageAccount(agyGem7d)).toEqual([]);
  });

  it("falls back to in-memory state without throwing when the global localStorage getter throws SecurityError", () => {
    const originalDescriptor = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
    const agyGem7d = usageWindowPinId("agy", agyGemini7d);
    let getterCalls = 0;
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      get() {
        getterCalls += 1;
        throw new DOMException("Storage access is blocked", "SecurityError");
      },
    });
    try {
      // First toggle exercises both readPinnedRaw() and setPinnedUsageAccounts() against the throwing getter
      expect(togglePinnedUsageAccount(agyGem7d)).toEqual([agyGem7d]);
      expect(getterCalls).toBeGreaterThanOrEqual(2);
      expect(togglePinnedUsageAccount("codex")).toEqual([agyGem7d, "codex"]);
      expect(togglePinnedUsageAccount(agyGem7d)).toEqual(["codex"]);
      expect(resetPinnedUsageAccounts()).toEqual([]);
      expect(setPinnedUsageAccounts([agyGem7d])).toEqual([agyGem7d]);
      expect(togglePinnedUsageAccount("ds")).toEqual([agyGem7d, "ds"]);
      expect(resetPinnedUsageAccounts()).toEqual([]);
    } finally {
      if (originalDescriptor) Object.defineProperty(globalThis, "localStorage", originalDescriptor);
      else Reflect.deleteProperty(globalThis, "localStorage");
    }
  });
});

describe("provider cards and the reset timeline (#13173)", () => {
  const acct = (id: string, name: string, provider: string, windows: UsageWindow[], sampled = at(-60_000)): UsageAccount => ({ id, name, provider, windows, sampled_at: sampled });
  it("lists the 5h window, then 7d, then the rest, grouping extension groups and per-model pools without interleaving", () => {
    const a = acct("a", "A", "claude", [{ ...win("7d", 10080, 1, 3 * H), model: "Fable" }, win("1d", 1440, 1, H), win("7d", 10080, 1, 5 * H), win("5h", 300, 1, H)]);
    expect(sortedWindows(a).map((w) => `${w.label}${w.model ? "*" : ""}`)).toEqual(["5h", "7d", "1d", "7d*"]);

    const agy = acct("agy", "Antigravity", "antigravity", [
      { ...win("5h", 300, 10, 2 * H), model: "Claude and GPT models" },
      { ...win("5h", 300, 20, 3 * H), model: "Gemini Models" },
      { ...win("7d", 10080, 30, 48 * H), model: "Claude and GPT models" },
      { ...win("7d", 10080, 40, 72 * H), model: "Gemini Models" },
    ]);
    expect(sortedWindows(agy).map((w) => `${w.model}:${w.label}`)).toEqual([
      "Claude and GPT models:5h",
      "Claude and GPT models:7d",
      "Gemini Models:5h",
      "Gemini Models:7d",
    ]);

    const ext = acct("ext", "Gateway", "claude", [
      { ...win("7d", 10080, 40, 48 * H), group: "Coding Pool", title: "Weekly" },
      { ...win("5h", 300, 20, 2 * H), group: "Reasoning Pool", title: "Burst" },
      { ...win("5h", 300, 10, 1 * H), group: "Coding Pool", title: "Short" },
    ]);
    // Preserves extension group order and relative window order inside each group
    expect(sortedWindows(ext).map((w) => `${w.group}:${w.title}`)).toEqual([
      "Coding Pool:Weekly",
      "Coding Pool:Short",
      "Reasoning Pool:Burst",
    ]);
  });
  it("sorts a card's accounts by the 7d reset, soonest first, those without one last, then case-insensitive name and id", () => {
    const a1 = acct("1", "Zed", "claude", [win("5h", 300, 1, H), win("7d", 10080, 1, 48 * H)]);
    const a2 = acct("2", "Amy", "claude", [win("7d", 10080, 1, 5 * H)]);
    const a3 = acct("3", "bob", "claude", [win("5h", 300, 1, H)]);
    const a4 = acct("4", "Abe", "claude", [win("7d", 10080, 1)]); // no reset time
    const a5 = acct("5", "Abe", "claude", [win("7d", 10080, 1)]); // same name, tie-broken by id
    expect(sortForCard([a1, a3, a5, a4, a2]).map((a) => `${a.name}:${a.id}`)).toEqual(["Amy:2", "Zed:1", "Abe:4", "Abe:5", "bob:3"]);
  });
  it("groups by provider in the hub's order, with the newest sample of each", () => {
    const g = groupByProvider([acct("1", "A", "claude", [], at(-5 * H)), acct("2", "B", "codex", [], at(-H)), acct("3", "C", "claude", [], at(-2 * H))]);
    expect(g.map((x) => [x.provider, x.accounts.length, x.sampledAt])).toEqual([["claude", 2, at(-2 * H)], ["codex", 1, at(-H)]]);
    expect(providerTitle("xai")).toBe("xAI");
    expect(providerTitle("codex")).toBe("Codex");
  });
  it("puts 24 hours at 45% of the shared axis and 7 days at the end, capped", () => {
    expect(timelinePosition(0)).toBe(0);
    expect(timelinePosition(6 * H)).toBeCloseTo(0.1125, 6);
    expect(timelinePosition(24 * H)).toBeCloseTo(0.45, 6);
    expect(timelinePosition(72 * H)).toBeCloseTo(0.45 + (48 / 144) * 0.55, 6);
    expect(timelinePosition(7 * 24 * H)).toBeCloseTo(1, 6);
    expect(timelinePosition(30 * 24 * H)).toBe(1);
    expect(timelinePosition(-H)).toBe(0);
  });
  it("makes a row per account in the given order, a lane per plan window from now to its reset", () => {
    const a = acct("a", "A", "claude", [win("7d", 10080, 1, 48 * H), win("5h", 300, 1, 2 * H)]);
    const b = acct("b", "B", "claude", [win("7d", 10080, 1, 20 * 24 * H)]);
    const rows = providerTimeline([b, a], NOW);
    expect(rows.map((r) => r.account.id)).toEqual(["b", "a"]);
    expect(rows[1].lanes.map((l) => [l.label, l.long, Number(l.end.toFixed(4)), l.remainingMs])).toEqual([["5h", false, 0.0375, 2 * H], ["7d", true, 0.45 + (24 / 144) * 0.55 > 0 ? Number((0.45 + (24 / 144) * 0.55).toFixed(4)) : 0, 48 * H]]);
    expect(rows[0].lanes[0].end).toBe(1); // beyond 7 days runs to the end of the axis
  });
  it("moves a rollout past its stale deadline out of account timelines and pinned sidebar items", () => {
    const windows = [win("5h", 300, 30, H)];
    const live = acct("seat", "Seat", "codex", windows);
    const file = { ...acct("file", "Codex", "codex", windows, at(-H)), reading_source: "rollout" as const, stale_at: at(-H / 2) };
    const readings = liveAccounts([live, file], NOW);
    expect(providerTimeline(readings, NOW).map((r) => r.account.id)).toEqual(["seat"]);
    const { items, custom } = selectSidebarUsageItems(readings, ["file", usageWindowPinId("file", windows[0])]);
    expect(custom).toBe(false);
    expect(items.map((item) => item.account.id)).toEqual(["seat"]);
  });
  it("merges multi-model windows of the same kind into one lane taking the earliest reset, while dropping missing/ended resets", () => {
    const empty = acct("e", "Empty", "claude", [win("5h", 300, 1), win("7d", 10080, 1, -H)]);
    expect(providerTimeline([empty], NOW)).toEqual([]);

    const agy = acct("ag", "Antigravity · Main", "antigravity", [
      { ...win("5h", 300, 30, 4 * H), model: "Claude" },
      { ...win("5h", 300, 45, 2 * H), model: "Gemini Pro" },
      { ...win("7d", 10080, 10, 48 * H), model: "Partner" },
    ]);
    const rows = providerTimeline([agy], NOW);
    expect(rows).toHaveLength(1);
    expect(rows[0].lanes.map((l) => [l.label, l.long, l.remainingMs, l.models.map((m) => m.model)])).toEqual([
      ["5h", false, 2 * H, ["Gemini Pro", "Claude"]],
      ["7d", true, 48 * H, ["Partner"]],
    ]);
  });
  it("builds a single unified timeline in provider-card and account order with stripped prefixes and paused flags", () => {
    const codex1 = acct("c1", "Codex #alpha", "codex", [
      { ...win("5h", 300, 96, 2 * H), protect_percent: 95 }, // r=4 <= R=5 -> paused
      win("7d", 10080, 40, 30 * H),
    ]);
    const codex2 = acct("c2", "Codex #beta", "codex", [win("7d", 10080, 20, 72 * H)]);
    const claude1 = acct("cl1", "Claude #gamma", "claude", [win("7d", 10080, 15, 24 * H)]);
    const noReset = acct("nr", "Codex #none", "codex", [win("7d", 10080, 10)]);
    const groups = groupByProvider([codex2, codex1, noReset, claude1]);
    const rows = unifiedTimeline(groups, NOW);
    expect(rows.map((r) => [r.provider, r.account.id, r.displayName, r.paused])).toEqual([
      ["codex", "c1", "#alpha", true],
      ["codex", "c2", "#beta", false],
      ["claude", "cl1", "#gamma", false],
    ]);
    // Paused is judged per lane: c1's 5h reached its reserve, its healthy 7d lane did not
    expect(rows[0].lanes.map((l) => [l.label, l.paused])).toEqual([["5h", true], ["7d", false]]);
    expect(rows[1].lanes.map((l) => l.paused)).toEqual([false]);
  });
  it("strips redundant leading provider prefixes cleanly without emptying exact matches or partial words", () => {
    expect(stripProviderPrefix("Codex #alpha", "codex")).toBe("#alpha");
    expect(stripProviderPrefix("Codex#alpha", "codex")).toBe("#alpha");
    expect(stripProviderPrefix("Claude #gamma", "claude")).toBe("#gamma");
    expect(stripProviderPrefix("Antigravity · Gemini", "antigravity")).toBe("Gemini");
    expect(stripProviderPrefix("Antigravity · Claude/GPT", "agy")).toBe("Claude/GPT");
    expect(stripProviderPrefix("Codex - Team", "codex")).toBe("Team");
    expect(stripProviderPrefix("Codex Pro", "codex")).toBe("Pro");
    expect(stripProviderPrefix("Codex", "codex")).toBe("Codex");
    expect(stripProviderPrefix("claudette", "claude")).toBe("claudette");
    expect(stripProviderPrefix("work-claude", "claude")).toBe("work-claude");
    expect(stripProviderPrefix("Codex Pro team", "claude")).toBe("Codex Pro team");
  });
  it("writes the time left short, in each language's units", async () => {
    await initI18n("zh-Hans");
    expect(remainingText(40 * 60_000)).toBe("40 分钟");
    expect(remainingText(1.5 * H)).toBe("1.5 小时");
    expect(remainingText(50 * H)).toBe("2 天 2 小时");
    expect(remainingText(48 * H)).toBe("2 天");
  });
});

describe("remaining-quota math, tone thresholds, and pause detection", () => {
  it("computes remainingPercent and protectReserve cleanly with clamping", () => {
    expect(remainingPercent(0)).toBe(100);
    expect(remainingPercent(35)).toBe(65);
    expect(remainingPercent(100)).toBe(0);
    expect(remainingPercent(-10)).toBe(100);
    expect(remainingPercent(120)).toBe(0);
    expect(protectReserve(undefined)).toBeNull();
    expect(protectReserve(95)).toBe(5);
    expect(protectReserve(80)).toBe(20);
    expect(protectReserve(100)).toBe(0);
    expect(protectReserve(-1)).toBeNull();
    expect(protectReserve(101)).toBeNull();
  });
  it("classifies remainingTone(r, R) by headroom h = r - R at 20 and 5 boundaries", () => {
    // Unprotected (R = 0)
    expect(remainingTone(100, 0)).toBe("green");
    expect(remainingTone(20, 0)).toBe("green");
    expect(remainingTone(19.9, 0)).toBe("orange");
    expect(remainingTone(5, 0)).toBe("orange");
    expect(remainingTone(4.9, 0)).toBe("red");
    expect(remainingTone(0, 0)).toBe("red");
    // Protected (R = 5, i.e. protect_percent = 95)
    expect(remainingTone(25, 5)).toBe("green"); // h = 20
    expect(remainingTone(24.9, 5)).toBe("orange"); // h = 19.9
    expect(remainingTone(10, 5)).toBe("orange"); // h = 5
    expect(remainingTone(9.9, 5)).toBe("red"); // h = 4.9
    expect(remainingTone(5, 5)).toBe("red"); // h = 0 (paused)
    expect(remainingTone(2, 5)).toBe("red"); // h = -3 (paused inside zone)
  });
  it("detects paused scheduling only when a protection reserve is configured and r <= R", () => {
    const unprotectedZero = win("7d", 10080, 100, 24 * H);
    expect(isWindowPaused(unprotectedZero)).toBe(false);
    expect(windowHeadroom(unprotectedZero)).toBe(0);

    const protectedAbove = { ...win("7d", 10080, 90, 24 * H), protect_percent: 95 }; // r=10, R=5 -> h=5
    expect(isWindowPaused(protectedAbove)).toBe(false);
    expect(windowHeadroom(protectedAbove)).toBe(5);

    const protectedExact = { ...win("7d", 10080, 95, 24 * H), protect_percent: 95 }; // r=5, R=5 -> paused
    expect(isWindowPaused(protectedExact)).toBe(true);

    const protectedInside = { ...win("7d", 10080, 98, 24 * H), protect_percent: 95 }; // r=2, R=5 -> paused
    expect(isWindowPaused(protectedInside)).toBe(true);

    expect(isAccountPaused({ windows: [protectedAbove, protectedExact] })).toBe(true);
    expect(isAccountPaused({ windows: [protectedAbove, { ...protectedInside, model: "Sonnet" }] })).toBe(false);
    expect(isAccountPaused({ windows: [{ ...protectedInside, model: "Gemini" }] })).toBe(true);
  });
});
