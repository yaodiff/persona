// @vitest-environment jsdom
// "Browser sign-ins" on a Mac's page (#5821): three sites or fewer are listed in place; more fold behind one summary row
// that opens the whole list here, with a search, an order and checkboxes, and every clear (one site or several) asks first
// and goes to the hub as one DELETE naming its sites.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// api() reads the token from localStorage, which this jsdom has none of
vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as unknown as { localStorage: Pick<Storage, "getItem" | "setItem" | "removeItem"> };
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const toasts = vi.hoisted(() => [] as { text: string; tone?: string }[]);
vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/atoms")>()),
  toast: (text: string, tone?: string) => void toasts.push({ text, tone }),
}));

import type { CookieSiteNow } from "../../lib/cookies";
import { BrowserLogins } from "./BrowserLogins";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const DAY = 86_400_000;
const iso = (ms: number) => new Date(ms).toISOString();
let sites: CookieSiteNow[] = [];
let deletes: string[] = [];

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), "http://hub");
  if (url.pathname !== "/api/v1/machines/mac/browser/cookies") return Promise.resolve(json({ error: "not found" }, 404));
  if (init?.method === "DELETE") {
    deletes.push(url.search);
    const gone = url.searchParams.getAll("site");
    const removed = sites.filter((s) => gone.includes(s.domain)).reduce((n, s) => n + s.count, 0);
    sites = sites.filter((s) => !gone.includes(s.domain));
    return Promise.resolve(json({ sites, removed }));
  }
  return Promise.resolve(json({ sites }));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const settle = async () => {
  await act(async () => { await new Promise((r) => setTimeout(r, 200)); }); // the search's settle time
  await flush();
};
const listed = () => [...host.querySelectorAll("[data-browser-logins-site]")].map((e) => e.textContent);
const summary = () => host.querySelector<HTMLButtonElement>("[data-browser-logins-summary]");
const button = (label: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const typeIn = (input: HTMLInputElement, value: string) => {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  set.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
};
const render = () => act(() => root.render(<BrowserLogins mid="mac" online />));

beforeEach(() => {
  deletes = [];
  toasts.length = 0;
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("BrowserLogins", () => {
  it("lists three sites in place, without a summary row", async () => {
    sites = [
      { domain: "b.example", count: 2, expires: iso(Date.now() + 30 * DAY) },
      { domain: "a.example", count: 1 },
      { domain: "c.example", count: 4, expires: iso(Date.now() + 9 * DAY) },
    ];
    render();
    await flush();
    expect(summary()).toBeNull();
    expect(listed()).toEqual(["a.example", "b.example", "c.example"]);
    expect(host.querySelector("input[type=checkbox]")).toBeNull();
  });

  it("folds four or more behind one row that counts them, says the first expiry and the expired, and opens the list", async () => {
    const now = Date.now();
    sites = [
      { domain: "d.example", count: 1, expires: iso(now + 40 * DAY) },
      { domain: "a.example", count: 2, expires: iso(now - 2 * DAY) },
      { domain: "c.example", count: 3, expires: iso(now + 5 * DAY) },
      { domain: "b.example", count: 1 },
    ];
    render();
    await flush();
    const row = summary()!;
    expect(row.getAttribute("aria-expanded")).toBe("false");
    expect(row.textContent).toContain("4 个网站");
    expect(row.textContent).toContain("1 个已过期");
    expect(row.textContent).toContain("最早");
    expect(listed()).toEqual([]);
    act(() => row.click());
    await flush();
    expect(row.getAttribute("aria-expanded")).toBe("true");
    expect(listed()).toEqual(["a.example", "b.example", "c.example", "d.example"]);
    expect(host.textContent).toContain("已过期");
  });

  it("filters by domain once the typing settles", async () => {
    sites = ["alpha", "beta", "gamma", "delta", "alder"].map((w) => ({ domain: `${w}.example`, count: 1 }));
    render();
    await flush();
    act(() => summary()!.click());
    await flush();
    const search = host.querySelector<HTMLInputElement>("[data-browser-logins] input:not([type])")!;
    act(() => typeIn(search, "AL"));
    expect(listed()).toHaveLength(5); // not yet: the search settles first
    await settle();
    expect(listed()).toEqual(["alder.example", "alpha.example"]);
    act(() => typeIn(search, "zzz"));
    await settle();
    expect(listed()).toEqual([]);
    expect(host.textContent).toContain("未找到匹配「zzz」的网站");
  });

  it("clears the checked sites in one DELETE after asking, and keeps the rest", async () => {
    sites = ["a", "b", "c", "d", "e"].map((w) => ({ domain: `${w}.example`, count: 2 }));
    render();
    await flush();
    act(() => summary()!.click());
    await flush();
    const boxes = [...host.querySelectorAll<HTMLInputElement>("li input[type=checkbox]")];
    act(() => boxes[1]!.click());
    act(() => boxes[3]!.click());
    await flush();
    act(() => button("清除 2 个")!.click());
    await flush();
    expect(document.body.textContent).toContain("清除 2 个网站的登录状态？");
    expect(deletes).toEqual([]); // nothing until confirmed
    act(() => button("清除")!.click());
    await flush();
    expect(deletes).toEqual(["?site=b.example&site=d.example"]);
    expect(listed()).toEqual(["a.example", "c.example", "e.example"]);
    expect(toasts.at(-1)).toEqual({ text: "已清除 4 条", tone: "ok" });
    expect(button("清除 2 个")).toBeUndefined();
  });

  it("clears one site from its row's menu, after asking", async () => {
    sites = [{ domain: "a.example", count: 3 }, { domain: "b.example", count: 1 }];
    render();
    await flush();
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="a.example 的更多操作"]')!.click());
    await flush();
    const item = [...document.body.querySelectorAll<HTMLElement>("[role=menuitem]")].find((e) => e.textContent?.includes("清除登录"));
    act(() => item!.click());
    await flush();
    expect(document.body.textContent).toContain("清除 a.example 的登录状态？");
    act(() => button("清除")!.click());
    await flush();
    expect(deletes).toEqual(["?site=a.example"]);
    expect(listed()).toEqual(["b.example"]);
  });
});
