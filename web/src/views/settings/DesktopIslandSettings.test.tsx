// @vitest-environment jsdom
// Settings › Notifications › "Show at the MacBook notch" (#7669): only where the desktop app can draw the island, off until
// turned on, and the desktop's answer is what the switch shows and what the island's rows go by.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DesktopBridge, DesktopIsland } from "../../lib/desktop-types";

const toast = vi.fn();
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: (...a: unknown[]) => toast(...a) }));
const setIslandOn = vi.fn();
vi.mock("../../lib/desktop-island", () => ({ setIslandOn: (on: boolean) => setIslandOn(on) }));

const { DesktopIslandSettings } = await import("./DesktopIslandSettings");

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  toast.mockClear();
  setIslandOn.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  delete (window as { personaDesktop?: unknown }).personaDesktop;
});

function desktop(island: DesktopIsland, set: (on: boolean) => Promise<DesktopIsland>) {
  const bridge = { getIsland: vi.fn(async () => island), setIslandEnabled: vi.fn(set) } as Partial<DesktopBridge>;
  Object.defineProperty(window, "personaDesktop", { value: bridge, configurable: true });
  return bridge;
}

const render = () => act(async () => root.render(<DesktopIslandSettings />));
const toggle = () => host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="在 MacBook 刘海区域显示"]');

describe("设置 › 通知 › 在 MacBook 刘海区域显示", () => {
  it("不在桌面 App 里、桌面 App 太旧或不是 Mac 时不显示", async () => {
    await render();
    expect(host.textContent).toBe("");
    Object.defineProperty(window, "personaDesktop", { value: { setBadge() {} }, configurable: true });
    await render();
    expect(host.textContent).toBe("");
    desktop({ supported: false, enabled: false }, async () => ({ supported: false, enabled: false }));
    await render();
    expect(toggle()).toBeNull();
  });

  it("默认关着，打开后以桌面 App 的回答为准", async () => {
    const bridge = desktop({ supported: true, enabled: false }, async (on) => ({ supported: true, enabled: on }));
    await render();
    expect(toggle()?.getAttribute("aria-checked")).toBe("false");
    expect(setIslandOn).toHaveBeenLastCalledWith(false);
    expect(host.textContent).toContain("仅在此 Mac 上的 Persona 桌面应用中生效");
    await act(async () => toggle()!.click());
    expect(bridge.setIslandEnabled).toHaveBeenCalledWith(true);
    expect(toggle()?.getAttribute("aria-checked")).toBe("true");
    expect(setIslandOn).toHaveBeenLastCalledWith(true);
  });

  it("没改成时提示原因，开关不动", async () => {
    desktop({ supported: true, enabled: false }, async () => { throw new Error("示例原因"); });
    await render();
    await act(async () => toggle()!.click());
    expect(toast).toHaveBeenCalledWith("未能保存更改：示例原因", "error");
    expect(toggle()?.getAttribute("aria-checked")).toBe("false");
    expect(setIslandOn).not.toHaveBeenCalledWith(true);
  });
});
