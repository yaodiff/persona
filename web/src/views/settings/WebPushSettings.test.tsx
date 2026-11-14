// @vitest-environment jsdom
// Settings › Notifications › When Persona is closed (#7459): why a browser can't, the switch turning Web Push on and off,
// how it stands (the push service, the last delivery, a failure), a browser that blocks notifications, an older hub,
// and Send Test.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WebPushState, WebPushSupport } from "../../lib/web-push";

let support: WebPushSupport = "ok";
let state: WebPushState = { loaded: true, working: false, status: { subscribed: false } };
const turnOn = vi.fn();
const turnOff = vi.fn();
const sendTest = vi.fn();
const load = vi.fn();
vi.mock("../../lib/web-push", async (orig) => ({
  ...(await orig<typeof import("../../lib/web-push")>()),
  webPushSupport: () => support,
  useWebPush: () => state,
  loadWebPush: () => load(),
  turnOnWebPush: () => turnOn(),
  turnOffWebPush: () => turnOff(),
  sendTestPush: () => sendTest(),
}));
const toast = vi.fn();
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: (...a: unknown[]) => toast(...a) }));

const { WebPushSettings } = await import("./WebPushSettings");
const { WebPushError } = await import("../../lib/web-push");

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  support = "ok";
  state = { loaded: true, working: false, status: { subscribed: false } };
  for (const f of [turnOn, turnOff, sendTest, load, toast]) f.mockReset();
  vi.stubGlobal("Notification", { permission: "granted" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const render = () => act(async () => root.render(<WebPushSettings />));
const toggle = () => host.querySelector<HTMLButtonElement>('[role="switch"]')!;
const text = () => host.textContent ?? "";
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === label);

describe("Persona 关闭时 (#7459)", () => {
  it("关着：说能收到什么，点开就订阅", async () => {
    await render();
    expect(load).toHaveBeenCalled();
    expect(text()).toContain("通知这个浏览器");
    expect(text()).toContain("即使没有打开任何 Persona 页面");
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    expect(toggle().disabled).toBe(false);
    expect(button("发送测试")).toBeUndefined();
    turnOn.mockResolvedValue({ subscribed: true });
    await act(async () => toggle().click());
    expect(turnOn).toHaveBeenCalled();
  });

  it("开着：说经哪个推送服务、上次送达，能发测试和关掉", async () => {
    state = { loaded: true, working: false, status: { subscribed: true, service: "FCM", current: true, last_ok_at: "2026-11-14T06:00:00Z" } };
    await render();
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(text()).toContain("已打开，经 FCM。上次送达：");
    sendTest.mockResolvedValue(undefined);
    await act(async () => button("发送测试")!.click());
    expect(sendTest).toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith("测试已发送，稍后就会出现。");
    turnOff.mockResolvedValue(undefined);
    await act(async () => toggle().click());
    expect(turnOff).toHaveBeenCalled();
  });

  it("上一条没推出去时说原因，测试失败也说", async () => {
    state = { loaded: true, working: false, status: { subscribed: true, service: "Apple", current: true, failed_at: "2026-11-14T06:00:00Z", status: 403, error: "403 BadJwtToken" } };
    await render();
    expect(text()).toContain("上一条通知没有发出去：403 BadJwtToken");
    expect(text()).toContain("未送达");
    sendTest.mockRejectedValue(new Error("推送服务没有接收这条通知：410"));
    await act(async () => button("发送测试")!.click());
    expect(toast).toHaveBeenCalledWith("测试没有发出去：推送服务没有接收这条通知：410", "error");
  });

  it("浏览器拒绝了通知：说去网站设置里允许，开关不能打开", async () => {
    vi.stubGlobal("Notification", { permission: "denied" });
    await render();
    expect(text()).toContain("这个浏览器禁止了本站的通知");
    expect(toggle().disabled).toBe(true);
  });

  it("打开时浏览器拒绝：提示一句，开关不动", async () => {
    turnOn.mockRejectedValue(new WebPushError("denied"));
    await render();
    await act(async () => toggle().click());
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("这个浏览器禁止了本站的通知"), "error");
  });

  it("用不了时说为什么，不读 hub", async () => {
    const cases: [WebPushSupport, string][] = [
      ["home-screen", "先把 Persona 添加到主屏幕"],
      ["insecure", "需要 HTTPS"],
      ["subpath", "子路径"],
      ["desktop-app", "桌面应用会自己发通知"],
      ["unsupported", "这个浏览器无法在 Persona 关闭时接收通知"],
    ];
    for (const [why, words] of cases) {
      support = why;
      load.mockClear();
      act(() => root.unmount());
      root = createRoot(host);
      await render();
      expect(text()).toContain(words);
      expect(toggle().disabled).toBe(true);
      expect(load).not.toHaveBeenCalled();
    }
  });

  it("hub 太旧时说去更新", async () => {
    state = { loaded: true, working: false, hubMissing: true };
    await render();
    expect(text()).toContain("hub 版本太旧");
    expect(toggle().disabled).toBe(true);
  });
});
