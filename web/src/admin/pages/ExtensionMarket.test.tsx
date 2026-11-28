// @vitest-environment jsdom
// The admin page's markets (#4481): the official market out of reach is grey, not a red "fill in a token", and the
// catalog does not say "no extensions" when it could not be read; another market's error stays red.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { resetExtensionRoot, setCsrf, setTransport } from "../api";
import type { Market } from "../../lib/extension-market";
import { ExtensionMarkets } from "./ExtensionMarket";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const LIMITED = "读不到 github:nwverse/persona-extensions：被限流了（HTTP 403）：约 45 分钟后再试；常用这个市场就填一个 token，额度更高";
let host: HTMLDivElement;
let root: Root;
let markets: Market[];
let calls: { path: string; method: string; body: unknown }[] = [];

const flush = async () => {
  // the catalog loads after a timer (its search debounce)
  for (let i = 0; i < 8; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const text = () => host.textContent ?? "";

beforeEach(() => {
  resetExtensionRoot();
  setCsrf("tok");
  calls = [];
  setTransport(async (url, init) => {
    const path = url.replace("/api/v1/admin", "").replace(/\?.*$/, "");
    const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status });
    const method = init?.method ?? "GET";
    if (path.startsWith("/extensions/-/markets/") && path.includes("/keys")) {
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ path, method, body });
      const m = markets[0];
      const keys = { ...(m.trusted_keys ?? {}) };
      if (method === "POST") keys["2222222222222222"] = body.key;
      else delete keys[path.split("/").pop()!];
      markets = [{ ...m, trusted_keys: keys }];
      return reply(markets[0]);
    }
    if (path === "/extensions") return reply({ extensions: [] });
    if (path === "/extensions/-/markets") return reply({ markets });
    if (path === "/extensions/-/catalog") return reply({ extensions: [] });
    return reply({}, 404);
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  setTransport((url, init) => fetch(url, init));
  setCsrf("");
  resetExtensionRoot();
});

const open = async () => {
  act(() => root.render(<ExtensionMarkets onChanged={() => {}} />));
  await flush();
};

const official = (error?: string): Market => ({ id: "official", name: "github:nwverse/persona-extensions", spec: "github:nwverse/persona-extensions", official: true, extensions: 0, error } as Market);
const github = (id: string, error?: string): Market => ({ id, name: id, spec: "github:example/extensions", extensions: 0, error } as Market);
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.includes(label));

describe("the admin page's markets", () => {
  it("shows the official market out of reach in grey, with the reason on hover", async () => {
    markets = [official(LIMITED)];
    await open();
    expect(text()).toContain("暂时无法获取官方扩展，请稍后重试。");
    expect(text()).not.toContain("填一个 token");
    const line = [...host.querySelectorAll("p")].find((p) => p.textContent === "暂时无法获取官方扩展，请稍后重试。")!;
    expect(line.className).toContain("text-ink-3");
    expect(line.getAttribute("title")).toBe(LIMITED);
    expect(host.querySelector(".bg-red-tint")).toBeNull();
    expect(host.querySelector(".text-red")).toBeNull();
    expect(text()).toContain("暂时无法读取该市场，恢复后其扩展会显示在此处。");
    expect(text()).not.toContain("暂无可用扩展");
    // the official market is the GitHub one most likely to hit the shared rate limit: its error still offers a token
    expect(text()).toContain("未配置 token 时，GitHub 每小时仅允许读取 60 次");
    expect(button("添加 token")).toBeTruthy();
  });

  it("keeps another market's error red", async () => {
    markets = [official(), { id: "m2", name: "team", spec: "https://market.example.com/persona-extensions.json", extensions: 0, error: "HTTP 404" } as Market];
    await open();
    expect(text()).toContain("读取失败：HTTP 404");
    expect(host.querySelector(".text-red")).not.toBeNull();
    expect(text()).not.toContain("未配置 token 时，GitHub 每小时仅允许读取 60 次");
  });

  it("offers a token under a GitHub market's error, and opens its dialog", async () => {
    markets = [github("team", "HTTP 403")];
    await open();
    expect(text()).toContain("未配置 token 时，GitHub 每小时仅允许读取 60 次");
    const fix = button("添加 token");
    expect(fix).toBeTruthy();
    act(() => fix!.click());
    await flush();
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("「team」的 token");
  });

  it("does not offer a token when the GitHub market has one", async () => {
    markets = [{ ...github("team", "HTTP 403"), has_token: true }];
    await open();
    expect(text()).not.toContain("未配置 token 时，GitHub 每小时仅允许读取 60 次");
  });

  it("shows a private market's signing keys and edits them", async () => {
    const key = { key: "ed25519:" + "A".repeat(43) + "=", key_id: "1111111111111111" };
    markets = [{ ...github("private"), signature: { state: "signed", key_id: key.key_id }, trusted_keys: { [key.key_id]: key.key } }];
    await open();
    expect(text()).toContain("1 把公钥");
    act(() => button("签名公钥")!.click());
    await flush();
    const dialog = () => document.querySelector('[role="dialog"]')?.textContent ?? "";
    expect(dialog()).toContain("签名公钥 · private");
    expect(dialog()).toContain(key.key_id);
    expect(dialog()).toContain("用于签署当前索引");
    expect(dialog()).toContain("仅官方市场的公钥可豁免沙箱隔离");
    const input = document.querySelector<HTMLInputElement>('[role="dialog"] input')!;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    act(() => {
      setter.call(input, "ed25519:" + "B".repeat(43) + "=");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const add = [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === "添加") as HTMLButtonElement;
    act(() => add.click());
    await flush();
    expect(calls).toEqual([{ path: "/extensions/-/markets/private/keys", method: "POST", body: { key: "ed25519:" + "B".repeat(43) + "=" } }]);
    expect(dialog()).toContain("2222222222222222");
  });

  it("offers no signing keys for the official market", async () => {
    markets = [official()];
    await open();
    expect(button("签名公钥")).toBeUndefined();
  });

  it("says there are no extensions when every market was read", async () => {
    markets = [official()];
    await open();
    expect(text()).toContain("暂无可用扩展");
    expect(text()).not.toContain("暂时无法");
  });
});
