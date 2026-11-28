// @vitest-environment jsdom
// An extension's page in Settings › Extensions (Android ExtensionModelParts): "Isolation" in its three states with the egress counts,
// and, for an extension that contributes a channel, the link to that channel's page in Settings › Channels.
import { act } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Channel } from "../../lib/channels";
import type { HubExtensionDetail } from "../../lib/extensions";

const detail = vi.hoisted(() => ({ value: null as unknown }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: () => Promise.resolve(detail.value),
}));
vi.mock("../../lib/channels", async (original) => ({
  ...(await original<typeof import("../../lib/channels")>()),
  useChannels: (): Channel[] => [{ id: "wechat", name: "微信", kind: "im", source: "plugin", installed: true, enabled: true, state: "ready" } as unknown as Channel],
}));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), features: ["channels"] }) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { adminNetworkURL, Extensions, SandboxGroup } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("运行隔离", () => {
  const blocked = { enabled: true, active: true, egress: { allowed: 3, denied: 1, last_denied: "rss.example.org:443", denied_at: new Date(Date.now() - 180_000).toISOString() } };
  it("says what was blocked in words, when, and links to the admin page's network block (#5696)", () => {
    const html = renderToStaticMarkup(<SandboxGroup s={blocked} id="rsshub" name="RSSHub" />);
    expect(html).toContain("运行隔离");
    expect(html).toContain("沙箱已生效");
    expect(html).toContain("已放行 3");
    expect(html).toContain("已拦截 1");
    expect(html).toContain("RSSHub 尝试访问");
    expect(html).toContain("rss.example.org:443");
    expect(html).toContain("不在其申请的允许范围内");
    expect(html).toContain("前往管理界面放行");
    expect(html).not.toContain("最近拒绝");
    expect(adminNetworkURL("rsshub")).toBe("/admin/extensions?extension=rsshub#network");
  });
  it("opens the admin page in a new tab", () => {
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    act(() => root.render(<SandboxGroup s={blocked} id="rss hub" name="RSSHub" />));
    act(() => host.querySelector<HTMLButtonElement>("[data-sandbox-blocked] button")!.click());
    expect(open).toHaveBeenCalledWith("/admin/extensions?extension=rss%20hub#network", "_blank", "noopener");
    open.mockRestore();
  });
  it("has no notice when nothing was blocked", () => {
    const html = renderToStaticMarkup(<SandboxGroup s={{ enabled: true, active: true, egress: { allowed: 12, denied: 0 } }} id="rsshub" name="RSSHub" />);
    expect(html).toContain("已拦截 0");
    expect(html).not.toContain("data-sandbox-blocked");
  });
  it("says why a sandbox asked for is not in effect", () => {
    const html = renderToStaticMarkup(<SandboxGroup s={{ enabled: true, active: false, why: "这台机器没有 bubblewrap" }} id="x" name="X" />);
    expect(html).toContain("已请求启用沙箱，但当前未生效");
    expect(html).toContain("这台机器没有 bubblewrap");
    expect(html).not.toContain("data-sandbox-egress");
  });
  it("says it is off", () => {
    expect(renderToStaticMarkup(<SandboxGroup s={{ enabled: false, active: false }} id="x" name="X" />)).toContain("未启用沙箱");
  });
});

describe("扩展详情里的渠道", () => {
  const ext = (over: Partial<HubExtensionDetail>): HubExtensionDetail => ({ id: "example-wechat", name: "示例微信", kind: "process", status: "running", source: "installed", enabled: true, ...over });

  it("links an extension that contributes a channel to its page, and shows its sandbox", async () => {
    detail.value = ext({ contributes: { channel: ["wechat"] }, sandbox: { enabled: true, active: false } });
    const onChannel = vi.fn();
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} onChannel={onChannel} />));
    await flush();
    expect(host.textContent).toContain("已请求启用沙箱，但当前未生效");
    const link = [...host.querySelectorAll<HTMLElement>("button, a, [role=button]")].find((b) => b.textContent?.includes("在「设置 › 渠道」中设置「微信」"));
    expect(link).toBeTruthy();
    act(() => link!.click());
    expect(onChannel).toHaveBeenCalledWith("wechat");
  });

  it("has no channel link or sandbox for an extension without them", async () => {
    detail.value = ext({});
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} onChannel={() => {}} />));
    await flush();
    expect(host.textContent).toContain("示例微信");
    expect(host.textContent).not.toContain("渠道」中设置");
    expect(host.textContent).not.toContain("运行隔离");
  });
});
