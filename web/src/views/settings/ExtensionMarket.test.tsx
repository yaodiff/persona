// @vitest-environment jsdom
// The catalog's empty line (#4480): with several markets and none picked, one unreadable and the others empty, it does
// not say "This marketplace has no extensions yet" under the unreadable banner. A GitHub market without a token that failed to read offers
// the token dialog here, where the user sees the error.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Catalog, CatalogItem, Market, MarketPreview } from "../../lib/extension-market";

const catalog = vi.hoisted(() => ({
  value: null as unknown,
  impl: null as null | ((q: string, market: string, refresh?: boolean, manager?: string) => Promise<unknown>),
  preview: vi.fn<(market: string, extension: string, manager?: string) => Promise<MarketPreview>>(),
}));
vi.mock("../../lib/extension-market", async (original) => ({
  ...(await original<typeof import("../../lib/extension-market")>()),
  getCatalog: (q: string, m: string, refresh?: boolean, manager?: string) =>
    (catalog.impl ? catalog.impl(q, m, refresh, manager) : Promise.resolve(catalog.value)),
  previewFromMarket: (market: string, extension: string, manager?: string) => catalog.preview(market, extension, manager),
}));
import { MarketCatalog, MarketItem } from "./ExtensionMarket";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const market = (id: string, extra: Partial<Market> = {}): Market => ({ id, spec: `https://example.com/${id}`, name: id, has_token: false, extensions: 0, ...extra });
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.includes(label));

beforeEach(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("MarketCatalog empty line", () => {
  it("says the readable markets are empty when another is unreadable", async () => {
    catalog.value = { markets: [market("official", { error: "timeout" }), market("team")], extensions: [] } satisfies Catalog;
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();
    expect(host.textContent).toContain("无法读取市场「official」");
    expect(host.textContent).toContain("已读取的市场中暂无扩展");
    expect(host.textContent).not.toContain("此市场暂无扩展");
  });

  it("keeps 此市场暂无扩展 for a single readable market", async () => {
    catalog.value = { markets: [market("official")], extensions: [] } satisfies Catalog;
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();
    expect(host.textContent).toContain("此市场暂无扩展");
  });
});

describe("MarketCatalog rate-limited GitHub market", () => {
  it("offers a token under the error of a GitHub market without one", async () => {
    catalog.value = { markets: [market("team", { spec: "github:example/extensions", error: "HTTP 403" })], extensions: [] } satisfies Catalog;
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();
    expect(host.textContent).toContain("未配置 token 时，GitHub 每小时仅允许读取 60 次");
    expect(button("添加 token")).toBeTruthy();
  });

  it("does not offer a token when the market has one or is not GitHub", async () => {
    catalog.value = {
      markets: [market("with-token", { spec: "github:example/extensions", has_token: true, error: "HTTP 403" }), market("site", { error: "HTTP 500" })],
      extensions: [],
    } satisfies Catalog;
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();
    expect(host.textContent).not.toContain("未配置 token 时，GitHub 每小时仅允许读取 60 次");
    expect(button("添加 token")).toBeUndefined();
  });

  it("asks no token of a newcomer for the official market: grey, and a quiet way to the markets page", async () => {
    catalog.value = {
      markets: [market("official", { spec: "github:example/official", official: true, error: "HTTP 403" })],
      extensions: [],
    } satisfies Catalog;
    const onMarkets = vi.fn();
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={onMarkets} />));
    await flush();
    expect(host.textContent).toContain("暂时无法访问官方扩展");
    expect(host.textContent).not.toContain("未配置 token 时，GitHub 每小时仅允许读取 60 次");
    expect(button("添加 token")).toBeUndefined();
    const link = button("前往「管理市场」添加 GitHub token");
    expect(link).toBeTruthy();
    act(() => link!.click());
    expect(onMarkets).toHaveBeenCalledOnce();
  });

  it("opens the token dialog for that market", async () => {
    catalog.value = { markets: [market("team", { spec: "github:example/extensions", error: "HTTP 403" })], extensions: [] } satisfies Catalog;
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();
    const fix = button("添加 token");
    expect(fix).toBeTruthy();
    act(() => fix!.click());
    await flush();
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("「team」的 token");
    // and how to get one: GitHub's token page, filled in
    const create = [...document.querySelectorAll('[role="dialog"] a')].find((a) => a.textContent?.includes("前往 GitHub 创建")) as HTMLAnchorElement | undefined;
    expect(create?.href).toContain("https://github.com/settings/personal-access-tokens/new?");
    expect(create?.target).toBe("_blank");
  });
});

describe("MarketCatalog updating state (#6468)", () => {
  type Ask = { q: string; market: string; resolve: (c: Catalog) => void };
  const asks: Ask[] = [];
  const item = (id: string) => ({ id, name: id, version: "1.0.0", market: "official", market_name: "official" }) as unknown as Catalog["extensions"][number];
  const reply = (ids: string[]): Catalog => ({ markets: [market("official"), market("team")], extensions: ids.map(item) });
  const type = (value: string) => {
    const input = host.querySelector("input") as HTMLInputElement;
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    act(() => { set.call(input, value); input.dispatchEvent(new Event("input", { bubbles: true })); });
  };
  const wait = (ms: number) => act(async () => { await new Promise((r) => setTimeout(r, ms)); });

  beforeEach(() => {
    asks.length = 0;
    catalog.impl = (q, m) => new Promise<Catalog>((resolve) => asks.push({ q, market: m, resolve }));
  });
  afterEach(() => { catalog.impl = null; });

  it("shows the updating state while a newly picked market loads", async () => {
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await wait(10);
    await act(async () => asks[0].resolve(reply(["a", "b"])));
    expect(host.textContent).toContain("2 个扩展");
    act(() => (button("team") as HTMLElement).click());
    await flush();
    expect(asks.at(-1)?.market).toBe("team");
    expect(host.querySelector("[role=status]")?.textContent).toContain("正在更新");
    expect(host.textContent).not.toContain("2 个扩展");
    await act(async () => asks.at(-1)!.resolve(reply(["c"])));
    expect(host.querySelector("[role=status]")).toBeNull();
    expect(host.textContent).toContain("1 个扩展");
  });

  it("does not present an older query's answer as current, and keeps the newest results out of order", async () => {
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await wait(10);
    await act(async () => asks[0].resolve(reply(["a", "b", "c"])));
    type("zz");
    await wait(300);
    const first = asks.at(-1)!;
    expect(first.q).toBe("zz");
    type("zzz"); // the typing pause for the second query is still running
    await act(async () => first.resolve(reply([])));
    expect(host.querySelector("[role=status]")).not.toBeNull(); // the answer is for "zz", not "zzz"
    expect(host.textContent).not.toContain("未找到");
    await wait(300);
    const second = asks.at(-1)!;
    expect(second.q).toBe("zzz");
    await act(async () => second.resolve(reply(["d"])));
    await act(async () => first.resolve(reply(["old"]))); // late, out of order
    expect(host.textContent).toContain("1 个扩展");
    expect(host.querySelector("[role=status]")).toBeNull();
  });
});

describe("MarketCatalog header Manage markets entry (#14958)", () => {
  it("requests installed status for the assistant selected for marketplace installs", async () => {
    const request = vi.fn(async () => ({ markets: [market("official")], extensions: [] }));
    catalog.impl = request;
    try {
      await act(async () => root.render(<MarketCatalog manager="work" onOpen={() => {}} />));
      await flush();
      expect(request).toHaveBeenCalledWith("", "", false, "work");
    } finally {
      catalog.impl = null;
    }
  });

  it("supports onManageMarkets and custom backLabel for Integrations while keeping legacy onMarkets compatible", async () => {
    catalog.value = { markets: [market("official")], extensions: [] } satisfies Catalog;
    const onManageMarkets = vi.fn();
    const onBack = vi.fn();
    act(() =>
      root.render(
        <MarketCatalog backLabel="集成与扩展" onBack={onBack} onOpen={() => {}} onManageMarkets={onManageMarkets} />,
      ),
    );
    await flush();
    const backBtn = button("集成与扩展");
    expect(backBtn).toBeTruthy();
    act(() => backBtn!.click());
    expect(onBack).toHaveBeenCalledOnce();

    const manageBtn = host.querySelector<HTMLButtonElement>('[data-testid="market-manage-markets"]');
    expect(manageBtn).not.toBeNull();
    expect(manageBtn!.textContent).toContain("管理市场");
    act(() => manageBtn!.click());
    expect(onManageMarkets).toHaveBeenCalledOnce();

    // Phone layout (onBack omitted) still shows the top-right Manage markets button.
    act(() => root.render(<MarketCatalog onOpen={() => {}} onManageMarkets={onManageMarkets} />));
    await flush();
    const phoneManageBtn = host.querySelector<HTMLButtonElement>('[data-testid="market-manage-markets"]');
    expect(phoneManageBtn).not.toBeNull();
    act(() => phoneManageBtn!.click());
    expect(onManageMarkets).toHaveBeenCalledTimes(2);

    // Legacy Extensions.tsx call signature (onMarkets without backLabel) keeps default "扩展" back label and Manage markets button.
    const onMarkets = vi.fn();
    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={onMarkets} />));
    await flush();
    expect(button("扩展")).toBeTruthy();
    const legacyManageBtn = host.querySelector<HTMLButtonElement>('[data-testid="market-manage-markets"]');
    expect(legacyManageBtn).not.toBeNull();
    act(() => legacyManageBtn!.click());
    expect(onMarkets).toHaveBeenCalledOnce();
  });
});

describe("ExtensionMarket latest_incompatible_version hints (#17698)", () => {
  const reason = "这个扩展需要更新版本的 hub，请先更新 hub（需要 >=300，现在是 100）";
  const entry = (extra: Partial<CatalogItem> = {}): CatalogItem => ({
    market: "official",
    market_name: "官方扩展",
    id: "example-cal",
    name: "Example 日历",
    version: "1.5.0",
    description: "同步日程与提醒",
    ...extra,
  });

  beforeEach(() => {
    catalog.preview.mockReset();
    catalog.preview.mockResolvedValue({
      preview_id: "pv-150",
      manifest: { id: "example-cal", name: "Example 日历", version: "1.5.0" },
      permissions: [],
      permissions_digest: "digest-150",
      expires_at: new Date(Date.now() + 60_000).toISOString(),
    });
  });

  it("shows a quiet hint for an installed compatible release while keeping compatible upgrades unblocked", async () => {
    const installedLatest = entry({
      installed: "1.5.0",
      installed_source: "installed",
      from_this_market: true,
      latest_incompatible_version: "3.0.0",
      latest_incompatible_reason: reason,
    });
    const upgradableOlder = entry({
      id: "example-notes",
      name: "Example 笔记",
      installed: "1.0.0",
      installed_source: "installed",
      from_this_market: true,
      upgradable: true,
      latest_incompatible_version: "3.0.0",
      latest_incompatible_reason: reason,
    });
    catalog.value = { markets: [market("official")], extensions: [installedLatest, upgradableOlder] } satisfies Catalog;

    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();

    const hints = [...host.querySelectorAll("[data-incompatible-hint]")];
    expect(hints).toHaveLength(2);
    expect(hints[0].textContent).toContain("3.0.0");
    expect(hints[0].textContent).toContain(reason);
    expect(host.textContent).toContain("已安装 1.5.0");
    expect(host.textContent).toContain("可升级到 1.5.0");

    // Detail view for already-installed compatible version shows the full notice.
    act(() => root.render(<MarketItem market="official" id="example-cal" onBack={() => {}} onInstalled={() => {}} />));
    await flush();
    const notice = host.querySelector("[data-incompatible-notice]");
    expect(notice).not.toBeNull();
    expect(notice!.textContent).toContain("有更新版本 3.0.0，当前 Persona 暂不兼容");
    expect(notice!.textContent).toContain(reason);

    // Detail view for upgradable older version shows the notice AND allows upgrading to the compatible release.
    act(() => root.render(<MarketItem market="official" id="example-notes" onBack={() => {}} onInstalled={() => {}} />));
    await flush();
    expect(host.querySelector("[data-incompatible-notice]")?.textContent).toContain("3.0.0");
    const upgradeBtn = button("升级到 1.5.0");
    expect(upgradeBtn).toBeTruthy();
    expect(upgradeBtn!.disabled).toBe(false);
    act(() => upgradeBtn!.click());
    await flush();
    expect(catalog.preview).toHaveBeenCalledWith("official", "example-notes", undefined);
  });

  it("shows the hint for an uninstalled compatible release without blocking installation", async () => {
    const installable = entry({
      latest_incompatible_version: "3.0.0",
      latest_incompatible_reason: reason,
    });
    catalog.value = { markets: [market("official")], extensions: [installable] } satisfies Catalog;

    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();

    expect(host.textContent).toContain("未安装");
    const hint = host.querySelector("[data-incompatible-hint]");
    expect(hint).not.toBeNull();
    expect(hint!.textContent).toContain("3.0.0");
    expect(hint!.textContent).toContain(reason);

    act(() => root.render(<MarketItem market="official" id="example-cal" onBack={() => {}} onInstalled={() => {}} />));
    await flush();

    const notice = host.querySelector("[data-incompatible-notice]");
    expect(notice).not.toBeNull();
    expect(notice!.textContent).toContain("3.0.0");
    expect(notice!.textContent).toContain(reason);

    const installBtn = button("安装");
    expect(installBtn).toBeTruthy();
    expect(installBtn!.disabled).toBe(false);
    act(() => installBtn!.click());
    await flush();
    expect(catalog.preview).toHaveBeenCalledWith("official", "example-cal", undefined);
  });

  it("omits the incompatible-version notice when item.problem already blocks the extension", async () => {
    const blocked = entry({
      version: "3.0.0",
      problem: reason,
      latest_incompatible_version: "3.0.0",
      latest_incompatible_reason: reason,
    });
    catalog.value = { markets: [market("official")], extensions: [blocked] } satisfies Catalog;

    act(() => root.render(<MarketCatalog onBack={() => {}} onOpen={() => {}} onMarkets={() => {}} />));
    await flush();

    expect(host.textContent).toContain("无法安装");
    expect(host.textContent).toContain(reason);
    expect(host.querySelector("[data-incompatible-hint]")).toBeNull();

    act(() => root.render(<MarketItem market="official" id="example-cal" onBack={() => {}} onInstalled={() => {}} />));
    await flush();

    expect(host.querySelector("[data-incompatible-notice]")).toBeNull();
    expect(host.textContent?.split(reason).length).toBe(2); // shown exactly once via item.problem
    expect(button("安装")).toBeUndefined();
  });
});
