// @vitest-environment jsdom
// "Add mail account" › Outlook (iOS OutlookSignInSection): GET the hub's Microsoft config, then the form; a malformed client ID
// is caught before any PUT, a good one is sent trimmed and the card turns to "already set"; "Change" / "Keep current" go back and forth.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { OutlookClient } from "./MailSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ID = "00000000-1111-2222-3333-444444444444";
const ok = (body: unknown) => ({ ok: true, status: 200, text: async () => JSON.stringify(body) });
const fail = (status: number, error: string) => ({ ok: false, status, text: async () => JSON.stringify({ error }) });
const redirect = "https://example.com/callback";

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const button = (text: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(text));
const type = (value: string) => {
  const input = host.querySelector<HTMLInputElement>("input")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const submit = async () => {
  act(() => host.querySelector("form")!.requestSubmit());
  await flush();
};
const puts = () => fetcher.mock.calls.filter(([, init]) => init?.method === "PUT");

beforeEach(() => {
  fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("Outlook 的 client ID", () => {
  it("asks for one, refuses a malformed one without a PUT, saves a good one trimmed and says it is set", async () => {
    fetcher.mockResolvedValueOnce(ok({ configured: false, redirect_uri: redirect }));
    act(() => root.render(<OutlookClient />));
    await flush();
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/mail/oauth/microsoft/config");
    expect(host.textContent).toContain(redirect);

    type("not-an-id");
    await submit();
    expect(host.textContent).toContain("客户端 ID 格式不正确");
    expect(puts()).toHaveLength(0);

    fetcher.mockResolvedValueOnce(ok({ configured: true, client_id: ID, source: "app", redirect_uri: redirect }));
    type(`  ${ID}  `);
    await submit();
    expect(puts()).toHaveLength(1);
    expect(JSON.parse(puts()[0][1].body)).toEqual({ client_id: ID });
    expect(host.textContent).toContain("当前 hub 已配置用于 Outlook 登录的 client ID");
    expect(host.textContent).toContain(ID);
    expect(host.textContent).toContain("在 Persona 中填写");
    expect(host.querySelector("form")).toBeNull();
  });

  it("describes the client ID field by its error so a screen reader hears why", async () => {
    fetcher.mockResolvedValueOnce(ok({ configured: false, redirect_uri: redirect }));
    act(() => root.render(<OutlookClient />));
    await flush();
    const input = host.querySelector<HTMLInputElement>("input")!;
    expect(input.hasAttribute("aria-describedby")).toBe(false);

    type("not-an-id");
    await submit();
    expect(input.getAttribute("aria-invalid")).toBe("true");
    const described = input.getAttribute("aria-describedby");
    expect(described).toBeTruthy();
    expect(document.getElementById(described!)?.textContent).toContain("客户端 ID 格式不正确");
  });

  it("goes to the form with 更换 (the current ID filled in) and back with 不换了", async () => {
    fetcher.mockResolvedValueOnce(ok({ configured: true, client_id: ID, source: "config", redirect_uri: redirect }));
    act(() => root.render(<OutlookClient />));
    await flush();
    expect(host.textContent).toContain("来自 manager.json");
    act(() => button("更换 client ID")!.click());
    expect(host.querySelector<HTMLInputElement>("input")!.value).toBe(ID);
    act(() => button("保持当前配置")!.click());
    expect(host.querySelector("form")).toBeNull();
    expect(host.textContent).toContain("当前 hub 已配置用于 Outlook 登录的 client ID");
    expect(puts()).toHaveLength(0);
  });

  it("says why when the config can't be read", async () => {
    fetcher.mockResolvedValueOnce(fail(500, "boom"));
    act(() => root.render(<OutlookClient />));
    await flush();
    expect(host.textContent).toContain("无法读取 Outlook 登录设置");
    expect(host.textContent).toContain("boom");
    expect(host.querySelector("form")).toBeNull();
  });
});
