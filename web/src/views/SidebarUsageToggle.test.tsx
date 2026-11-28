// @vitest-environment jsdom
// The sidebar's usage card can be turned off (lib/sidebar-usage.ts, Settings › General › Appearance): shown by default,
// gone when off, and the setting's switch keeps nothing when it is back on.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import { SIDEBAR_USAGE_KEY, sidebarUsageShown } from "../lib/sidebar-usage";
import type { UsageAccount } from "../lib/usage-windows";
import { SidebarUsage } from "./UsageBars";
import { SidebarUsageRow } from "./settings/LookSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const accounts: UsageAccount[] = [
  {
    id: "a",
    provider: "codex",
    name: "账号 a",
    sampled_at: new Date().toISOString(),
    windows: [{ label: "5h", window_minutes: 300, used_percent: 50, resets_at: new Date(Date.now() + 3600_000).toISOString() }],
  },
];

vi.mock("../lib/usage-windows", async (orig) => ({ ...(await orig<typeof import("../lib/usage-windows")>()), useUsageWindows: () => accounts }));

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  await act(async () => { await vi.dynamicImportSettled(); });
  host.remove();
  await initI18n("zh-Hans");
});

describe("sidebar usage card setting", () => {
  it("is shown by default", async () => {
    await act(async () => root.render(<SidebarUsage />));
    expect(sidebarUsageShown()).toBe(true);
    expect(host.querySelector("[data-usage-bars]")).not.toBeNull();
    expect(host.textContent).toContain("账号 a");
  });

  it("is gone once the setting is off, and back when it is turned on", async () => {
    await act(async () => root.render(<><SidebarUsageRow /><SidebarUsage /></>));
    const sw = () => host.querySelector<HTMLElement>("[role=switch]")!;
    expect(sw().getAttribute("aria-checked")).toBe("true");
    await act(async () => sw().click());
    expect(localStorage.getItem(SIDEBAR_USAGE_KEY)).toBe("0");
    expect(host.querySelector("[data-usage-bars]")).toBeNull();
    expect(host.textContent).not.toContain("账号 a");
    await act(async () => sw().click());
    expect(localStorage.getItem(SIDEBAR_USAGE_KEY)).toBeNull();
    expect(host.querySelector("[data-usage-bars]")).not.toBeNull();
  });
});
