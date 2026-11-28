// @vitest-environment jsdom
// Saving an extension's settings when the hub cleared its secrets and then refused the configuration
// (PUT …/config answers 500 secret_not_cleared or 409 read_only with secrets_cleared): the Secrets group is read again
// and says the key is gone, and the draft being edited stays.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, setToken } from "../../lib/api";
import type { HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ details: [] as unknown[], save: null as null | (() => Promise<unknown>) }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: vi.fn(() => Promise.resolve(hub.details.length > 1 ? hub.details.shift() : hub.details[0])),
  setExtensionConfig: vi.fn(() => hub.save!()),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { extensionDetail, setExtensionConfig } from "../../lib/extensions";
import { toast } from "../../ui/atoms";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let login = 0;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

const voice = (keySet: boolean): HubExtensionDetail => ({
  id: "example-voice",
  name: "Example voice",
  kind: "process",
  status: "running",
  source: "installed",
  enabled: true,
  form: { fields: [{ name: "host", type: "string", title: "Host" }] },
  config: { host: "a.example.com" },
  secrets: [{ name: "api_key", title: "API key", set: keySet }],
});

const typeInto = (input: HTMLInputElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
};
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);

beforeEach(() => {
  setToken(`token-${++login}`); // unsaved edits outlive the page (#2800): a new credential drops the case before's
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  vi.mocked(extensionDetail).mockClear();
  vi.mocked(setExtensionConfig).mockClear();
  vi.mocked(toast).mockClear();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const openAndEditHost = async () => {
  act(() => root.render(<Extensions open="example-voice" onOpen={() => {}} />));
  await flush();
  expect(host.textContent).toContain("API key");
  expect(host.textContent).toContain("已设置");
  const input = host.querySelector<HTMLInputElement>('input[type="text"], input:not([type])')!;
  expect(input.value).toBe("a.example.com");
  act(() => typeInto(input, "b.example.com"));
  return input;
};

describe("a settings save that cleared secrets but failed", () => {
  it("reads the page again so the cleared key shows as not set, and keeps the draft", async () => {
    hub.details = [voice(true), voice(true), voice(false)];
    hub.save = () => Promise.reject(new ApiError(500, "secret not cleared", { code: "secret_not_cleared", secrets_cleared: ["api_key"] }));
    const input = await openAndEditHost();
    await act(async () => button("保存")!.click());
    await flush();

    expect(setExtensionConfig).toHaveBeenCalledTimes(1);
    expect(extensionDetail).toHaveBeenCalledTimes(3); // opening, preflight, and the post-refusal read
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("secret not cleared"), "error");
    expect(host.textContent).not.toContain("已设置");
    expect(host.textContent).toContain("未设置");
    expect(input.value).toBe("b.example.com");
    expect(button("保存")).toBeTruthy();
  });

  it("refreshes the extension's places too: the hub restarts it without those secrets", async () => {
    hub.details = [voice(true), voice(true), voice(false)];
    hub.save = () => Promise.reject(new ApiError(409, "read only", { code: "read_only", secrets_cleared: ["api_key"] }));
    await openAndEditHost();
    const changes = vi.fn();
    window.addEventListener("persona:extensions-changed", changes);
    try {
      await act(async () => button("保存")!.click());
      await flush();
    } finally {
      window.removeEventListener("persona:extensions-changed", changes);
    }
    expect(changes).toHaveBeenCalledTimes(1);
  });

  it("does not read the page again for a failure that cleared nothing", async () => {
    hub.details = [voice(true), voice(true)];
    hub.save = () => Promise.reject(new ApiError(409, "read only", { code: "read_only" }));
    const input = await openAndEditHost();
    const changes = vi.fn();
    window.addEventListener("persona:extensions-changed", changes);
    await act(async () => button("保存")!.click());
    await flush();
    window.removeEventListener("persona:extensions-changed", changes);

    expect(extensionDetail).toHaveBeenCalledTimes(2); // opening and preflight only
    expect(changes).not.toHaveBeenCalled();
    expect(host.textContent).toContain("已设置");
    expect(input.value).toBe("b.example.com");
  });
});
