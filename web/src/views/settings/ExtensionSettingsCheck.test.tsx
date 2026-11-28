// @vitest-environment jsdom
// A change the extension checked and turned down (PUT …/secrets/{name} or …/config answers 400 check_failed in the
// extension's own words, 429 check_rate_limited, 409 settings_moved): nothing was saved, so the secret's dialog stays
// open with what was typed and says why under the field, and the settings form says it at its save bar.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/api";
import type { HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ detail: null as unknown, secret: null as null | (() => Promise<unknown>), save: null as null | (() => Promise<unknown>) }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: vi.fn(() => Promise.resolve(hub.detail)),
  setExtensionSecret: vi.fn(() => hub.secret!()),
  setExtensionConfig: vi.fn(() => hub.save!()),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { setExtensionConfig, setExtensionSecret } from "../../lib/extensions";
import { toast } from "../../ui/atoms";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

const REFUSED = "网关不认这把 Key（401）：invalid api key";

const gateway = (key: { set: boolean; hint?: string }): HubExtensionDetail => ({
  id: "example-gateway",
  name: "Example gateway",
  kind: "process",
  status: "running",
  source: "installed",
  enabled: true,
  form: { fields: [{ name: "gateway_url", type: "string", title: "网关地址" }] },
  config: { gateway_url: "https://a.example.com" },
  secrets: [{ name: "gateway_key", title: "Key", ...key }],
});

const typeInto = (input: HTMLInputElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
};
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const secretInput = () => document.querySelector<HTMLInputElement>('input[aria-label="Key"]');

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  vi.mocked(setExtensionSecret).mockClear();
  vi.mocked(setExtensionConfig).mockClear();
  vi.mocked(toast).mockClear();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const open = async () => {
  act(() => root.render(<Extensions open="example-gateway" onOpen={() => {}} />));
  await flush();
};
const openKeyDialog = async () => {
  await open();
  const row = [...host.querySelectorAll<HTMLElement>("button, [role=button]")].find((b) => b.textContent?.includes("Key"));
  await act(async () => row!.click());
  const input = secretInput()!;
  expect(input).toBeTruthy();
  return input;
};

describe("a secret the extension turned down", () => {
  it("stays open with what was typed, says why under the field, and keeps the focus there", async () => {
    hub.detail = gateway({ set: false });
    hub.secret = () => Promise.reject(new ApiError(400, REFUSED, { code: "check_failed", error: REFUSED }));
    const input = await openKeyDialog();
    act(() => typeInto(input, "ag-bad-key"));
    await act(async () => button("保存")!.click());
    await flush();

    expect(setExtensionSecret).toHaveBeenCalledWith("example-gateway", "gateway_key", "ag-bad-key", expect.objectContaining({ managerId: "assistant" }));
    const alert = document.querySelector<HTMLElement>('[role="alert"]');
    expect(alert?.textContent).toContain(REFUSED);
    // under the field, tied to it
    expect(input.compareDocumentPosition(alert!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(input.getAttribute("aria-describedby")).toBe(alert!.id);
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(secretInput()).toBe(input);
    expect(input.value).toBe("ag-bad-key");
    expect(input.disabled).toBe(false);
    expect(document.activeElement).toBe(input);
    // said in the dialog, not as a toast, and the value is in no message
    expect(toast).not.toHaveBeenCalled();
    expect(alert!.textContent).not.toContain("ag-bad-key");
  });

  it("says a check asked for too often the same way", async () => {
    hub.detail = gateway({ set: true, hint: "7f3a" });
    hub.secret = () => Promise.reject(new ApiError(429, "检查太频繁了，稍后再试", { code: "check_rate_limited" }));
    const input = await openKeyDialog();
    expect(document.body.textContent).toContain("已设置 · 末 4 位 7f3a");
    act(() => typeInto(input, "ag-demo-key-0000-9b1c"));
    await act(async () => button("保存")!.click());
    await flush();
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("检查太频繁了，稍后再试");
    expect(secretInput()?.value).toBe("ag-demo-key-0000-9b1c");
  });

  it("closes once saved, and the value leaves the page", async () => {
    hub.detail = gateway({ set: false });
    hub.secret = () => Promise.resolve(gateway({ set: true, hint: "7f3a" }));
    const input = await openKeyDialog();
    act(() => typeInto(input, "ag-demo-key-0000-7f3a"));
    await act(async () => button("保存")!.click());
    await flush();
    expect(secretInput()).toBeNull();
    expect(document.body.innerHTML).not.toContain("ag-demo-key-0000-7f3a");
    expect(host.textContent).toContain("已设置 · 末 4 位 7f3a");
  });
});

describe("settings the extension turned down", () => {
  it("says why at the save bar and keeps the draft; changing it again takes the word away", async () => {
    hub.detail = gateway({ set: true, hint: "7f3a" });
    hub.save = () => Promise.reject(new ApiError(400, "网关地址连不上：connection refused", { code: "check_failed" }));
    await open();
    const input = host.querySelector<HTMLInputElement>('input[type="text"], input:not([type])')!;
    act(() => typeInto(input, "https://wrong.example.com"));
    await act(async () => button("保存")!.click());
    await flush();

    expect(setExtensionConfig).toHaveBeenCalledTimes(1);
    const alert = host.querySelector<HTMLElement>('[role="alert"]');
    expect(alert?.textContent).toContain("网关地址连不上：connection refused");
    // in the save bar, by the button that saves again
    expect(alert!.parentElement?.contains(button("保存")!)).toBe(true);
    expect(toast).not.toHaveBeenCalled();
    expect(input.value).toBe("https://wrong.example.com");

    act(() => typeInto(input, "https://b.example.com"));
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });
});
