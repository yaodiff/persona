// @vitest-environment jsdom
// "iPhone control" on a computer's page (#8217): Pair shows at once, before the hub answers (principle 7), and asks for
// that iPhone; a Linux computer says it can't yet; the consent is pointed to on the iPhone; Forget asks first.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// api() reads the token from localStorage, which this jsdom has none of
vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as unknown as { localStorage: Pick<Storage, "getItem" | "setItem" | "removeItem"> };
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});
import type { IPhoneDevice, MachineIPhone } from "../../lib/iphone";
import { IPhoneSection } from "./IPhoneSection";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Every request waits until the test answers it.
type Pending = { method: string; path: string; body: unknown; answer: (v: MachineIPhone) => void };
let pending: Pending[] = [];
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  return new Promise((resolve) => {
    pending.push({ method: init?.method ?? "GET", path: String(input).split("?")[0]!, body: init?.body ? JSON.parse(String(init.body)) : undefined, answer: (v) => resolve(json(v)) });
  });
}

const cable: IPhoneDevice = { id: "dev-1", name: "我的 iPhone", ios: "27.2", paired: false, usb: true, connected: false, locked: false, ready: false, code: "usb_unpaired" };
const paired: IPhoneDevice = { ...cable, paired: true, connected: true, ready: true, code: undefined, transport: "network", allowed: false };
const status = (more: Partial<MachineIPhone>): MachineIPhone => ({ enabled: true, installed: true, ddi_present: true, devices: [], ...more });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); await Promise.resolve(); });
};
const take = (method: string) => {
  const i = pending.findIndex((p) => p.method === method);
  expect(i).toBeGreaterThanOrEqual(0);
  return pending.splice(i, 1)[0]!;
};
const text = () => host.textContent ?? "";
const render = async (iphone: MachineIPhone, mac = true) => {
  act(() => root.render(<IPhoneSection mid="mb" iphone={iphone} online mac={mac} />));
  await flush();
};
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  act(() => (el as HTMLElement).click());
  await flush();
};

beforeEach(() => {
  pending = [];
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
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
  vi.useRealTimers();
});

describe("iPhone control", () => {
  it("shows Pair's progress at once, then what the iPhone waits for", async () => {
    await render(status({ devices: [cable] }));
    take("GET").answer(status({ devices: [cable] }));
    await flush();
    await click(host.querySelector('[data-testid="iphone-pair-dev-1"]'));
    expect(text()).toContain("正在与此 iPhone 配对…");
    const post = take("POST");
    expect(post.path).toMatch(/\/machines\/mb\/iphone\/pair$/);
    expect(post.body).toEqual({ device: "dev-1" });
    post.answer(status({ devices: [cable], job: { kind: "pair", state: "running", code: "trust_pending", device: "dev-1" } }));
    await flush();
    expect(text()).toContain("请在这台 iPhone 上轻点「信任」，然后输入锁屏密码");
    expect(host.querySelector('[data-testid="iphone-cancel-dev-1"]')).toBeTruthy();
  });

  it("says a Linux computer can't operate iPhones yet, with nothing to install", async () => {
    await render(status({ installed: false, ddi_present: false, code: "bridge_missing" }), false);
    expect(text()).toContain("该电脑暂不支持操控 iPhone");
    expect(host.querySelector('[data-testid="iphone-install"]')).toBeNull();
  });

  it("points to the iPhone for its consent", async () => {
    await render(status({ devices: [paired] }));
    expect(text()).toContain("等待此 iPhone 授权");
    expect(text()).toContain("设置 › 连接器 › 这台 iPhone");
  });

  it("forgets a paired iPhone from its menu, after asking", async () => {
    await render(status({ devices: [paired] }));
    take("GET").answer(status({ devices: [paired] }));
    await flush();
    await click(host.querySelector('[data-testid="iphone-device-dev-1"] button[aria-label]'));
    const row = () => [...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.includes("忘记"));
    await click(row());
    expect(pending.some((p) => p.method === "POST"), "the first choice only asks").toBe(false);
    await click(row());
    const post = take("POST");
    expect(post.path).toMatch(/\/machines\/mb\/iphone\/forget$/);
    expect(post.body).toEqual({ device: "dev-1" });
  });
});
