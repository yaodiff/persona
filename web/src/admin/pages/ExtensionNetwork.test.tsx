// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
  Element.prototype.scrollIntoView ??= function () {};
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { toast } from "../../ui/atoms";
import { resetExtensionRoot, setCsrf, setTransport } from "../api";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
let puts: { path: string; body: unknown }[];
let sent: { method: string; path: string; body?: unknown }[];
let sandbox: Record<string, unknown>;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const ext = () => ({ id: "rsshub", name: "RSSHub", source: "installed", status: "running", enabled: true, permissions: [], sandbox });
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const sandboxSwitch = () => host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="在沙箱中运行 RSSHub"]')!;
const render = async (search = "?extension=rsshub") => {
  history.replaceState(null, "", "/admin/extensions" + search);
  await act(async () => root.render(<Extensions />));
  await flush();
};

beforeEach(() => {
  resetExtensionRoot();
  setCsrf("tok");
  puts = [];
  sent = [];
  vi.mocked(toast).mockClear();
  const at = new Date(Date.now() - 180_000).toISOString();
  sandbox = { enabled: true, default: true, chosen: false, active: true, owner_allowed: [{ host: "feeds.example.net", port: "8443", at }],
    egress: { allowed: 12, denied: 3, last_denied: "rss.example.org", recent_denied: [{ target: "rss.example.org", count: 2, at }, { target: "img.example.net", count: 1, at }] } };
  setTransport(async (path, init) => {
    const reply = (body: unknown) => new Response(JSON.stringify(body), { status: 200 });
    if (init?.method === "POST" && path.includes("/egress/allow")) {
      const body = JSON.parse(String(init.body));
      sent.push({ method: "POST", path, body });
      if (String(body.host).includes("*")) return new Response(JSON.stringify({ error: "只能放行具体的地址，不能用 *。", code: "invalid_host" }), { status: 400 });
      sandbox = { ...sandbox, owner_allowed: [...(sandbox.owner_allowed as object[]), { host: body.host, at }] };
      return reply(ext());
    }
    if (init?.method === "DELETE" && path.includes("/egress/allow")) {
      sent.push({ method: "DELETE", path });
      const host = new URL(path, "http://x").searchParams.get("host");
      sandbox = { ...sandbox, owner_allowed: (sandbox.owner_allowed as { host: string; port?: string }[]).filter((a) => (a.port ? `${a.host}:${a.port}` : a.host) !== host) };
      return reply(ext());
    }
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body));
      puts.push({ path, body });
      sandbox = { ...sandbox, enabled: body.enabled ?? sandbox.default, chosen: body.enabled !== null };
      return reply(ext());
    }
    if (path === "/api/v1/admin/extensions") return reply({ can_install: false, extensions: [ext()] });
    if (path.includes("metrics")) return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
    return reply({ extensions: [], items: [] });
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  history.replaceState(null, "", "/");
  setTransport((path, init) => fetch(path, init));
});

describe("网络与隔离 (#5696)", () => {
  it("the settings page's link opens the row at its network block, with the counts", async () => {
    await render();
    const block = host.querySelector('[data-extension-network="rsshub"]');
    expect(block).toBeTruthy();
    expect(block!.textContent).toContain("网络与隔离");
    expect(block!.textContent).toContain("默认开启（非官方市场来源）");
    expect(block!.textContent).toContain("已放行 12");
    expect(block!.textContent).toContain("已拦截 3");
  });

  it("asks before turning the sandbox off, and turning it on again goes back to the default", async () => {
    await render();
    await act(async () => sandboxSwitch().click());
    await flush();
    expect(dialog()?.textContent).toContain("关闭 RSSHub 的沙箱？");
    expect(puts).toHaveLength(0);
    const off = [...dialog()!.querySelectorAll("button")].find((b) => b.textContent?.trim() === "关闭沙箱")!;
    await act(async () => off.click());
    await flush();
    expect(puts).toEqual([{ path: "/api/v1/admin/extensions/rsshub/sandbox", body: { enabled: false } }]);
    expect(host.querySelector('[data-extension-network="rsshub"]')!.textContent).toContain("沙箱已关闭：RSSHub 可以访问任意网络地址");
    expect(host.querySelector('[data-extension-network="rsshub"]')!.textContent).toContain("已手动关闭 · 默认开启");

    await act(async () => sandboxSwitch().click());
    await flush();
    expect(dialog()).toBeNull();
    expect(puts[1]).toEqual({ path: "/api/v1/admin/extensions/rsshub/sandbox", body: { enabled: null } });
  });

  it("is closed without the link, and absent for an extension the hub does not run from a package", async () => {
    await render("");
    expect(host.querySelector('[data-extension-network="rsshub"]')).toBeNull();
  });

  it("allows a blocked address at once, with Undo, and revokes one (#5697)", async () => {
    await render();
    const block = () => host.querySelector('[data-extension-network="rsshub"]')!;
    expect(block().querySelector("[data-hosts-blocked]")!.textContent).toContain("rss.example.org");
    expect(block().querySelector("[data-hosts-allowed]")!.textContent).toContain("feeds.example.net:8443");
    expect(block().querySelector("[data-hosts-allowed]")!.textContent).toContain("仅此端口");
    const allowButton = [...block().querySelectorAll("[data-hosts-blocked] button")][0] as HTMLButtonElement;
    await act(async () => allowButton.click());
    await flush();
    expect(dialog()).toBeNull();
    expect(sent[0]).toEqual({ method: "POST", path: "/api/v1/admin/extensions/rsshub/egress/allow", body: { host: "rss.example.org" } });
    expect(block().querySelector("[data-hosts-blocked]")!.textContent).not.toContain("rss.example.org");
    expect(block().querySelector("[data-hosts-allowed]")!.textContent).toContain("任意端口");
    const [, , action] = vi.mocked(toast).mock.calls.at(-1)!;
    expect(action?.label).toBe("撤销");
    await act(async () => action!.run());
    await flush();
    expect(sent[1]).toEqual({ method: "DELETE", path: "/api/v1/admin/extensions/rsshub/egress/allow?host=rss.example.org" });

    const revoke = [...block().querySelectorAll("[data-hosts-allowed] button")].find((b) => b.textContent?.trim() === "撤销") as HTMLButtonElement;
    await act(async () => revoke.click());
    await flush();
    expect(sent[2]).toEqual({ method: "DELETE", path: "/api/v1/admin/extensions/rsshub/egress/allow?host=feeds.example.net%3A8443" });
  });

  it("says what is wrong with a typed address under the field", async () => {
    await render();
    const field = host.querySelector<HTMLInputElement>('input[aria-label="允许 RSSHub 访问的地址"]')!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(field, "*.example.org");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => field.form!.requestSubmit());
    await flush();
    expect(field.getAttribute("aria-invalid")).toBe("true");
    expect(field.form!.textContent).toContain("只能放行具体的地址，不能用 *。");
  });
});
