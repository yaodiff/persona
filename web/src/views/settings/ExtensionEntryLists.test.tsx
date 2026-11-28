// @vitest-environment jsdom
// Entry lists keep host-owned IDs out of text fields, browser-only keys stable across immutable edits, and each secret
// in the existing write-only dialog. Adding and removing entries changes only the configuration draft until Save.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../../lib/api";
import type { HubExtensionDetail, JsonObject } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ detail: null as unknown }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: vi.fn(() => Promise.resolve(hub.detail)),
  setExtensionConfig: vi.fn((_id: string, config: unknown) => Promise.resolve({ ...(hub.detail as object), config })),
  setExtensionSecret: vi.fn(() => Promise.resolve(hub.detail)),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { extensionDetail, setExtensionConfig, setExtensionSecret } from "../../lib/extensions";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let login = 0;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const detail = (): HubExtensionDetail => ({
  id: "example-entries",
  name: "Example entries",
  kind: "process",
  status: "running",
  source: "installed",
  enabled: true,
  form: {
    fields: [{
      name: "accounts",
      type: "array",
      title: "Accounts",
      max_items: 2,
      secret: { name: "account_key", title: "Account key", hint: "last4" },
      items: {
        name: "",
        type: "object",
        fields: [
          { name: "name", type: "string", title: "Name", required: true },
          { name: "address", type: "string", title: "Address", format: "uri", required: true, clears_secret: true },
        ],
      },
    }],
  },
  config: { accounts: [{ _id: "entry_saved", name: "One", address: "https://one.example.com" }] },
  secrets: [{ name: "entry_saved", title: "Account key", set: true, hint: "1234", list: "accounts", entry_title: "One" }],
});
const buttons = (label: string, within: ParentNode = host) => [...within.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === label);
const button = (label: string, within: ParentNode = host) => buttons(label, within)[0];
const entries = () => [...host.querySelectorAll<HTMLElement>("[data-entry-key]")];
const typeInto = (input: HTMLInputElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
};
const open = async () => {
  act(() => root.render(<Extensions open="example-entries" onOpen={() => {}} />));
  await flush();
};

beforeEach(() => {
  setToken(`token-${++login}`);
  hub.detail = detail();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  vi.mocked(extensionDetail).mockClear();
  vi.mocked(setExtensionConfig).mockClear();
  vi.mocked(setExtensionSecret).mockClear();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("extension entry lists", () => {
  it("adds and edits an entry with a stable browser key, without secret or local metadata in the saved config", async () => {
    await open();
    expect(entries()).toHaveLength(1);
    expect(buttons("密钥")).toHaveLength(1); // no duplicate in the fixed-secrets group
    expect(host.textContent).toContain("保存更改后的地址会清除该条目的密钥");

    act(() => button("添加条目").click());
    expect(entries()).toHaveLength(2);
    expect(button("添加条目").disabled).toBe(true);
    expect(button("密钥", entries()[1]).disabled).toBe(true);
    expect(entries()[1].textContent).toContain("保存此条目后才能设置密钥");

    const key = entries()[1].dataset.entryKey;
    const inputs = entries()[1].querySelectorAll<HTMLInputElement>("input");
    act(() => typeInto(inputs[0], "Two"));
    const address = entries()[1].querySelectorAll<HTMLInputElement>("input")[1];
    address.focus();
    act(() => typeInto(address, "https://two.example.com"));
    expect(entries()[1].dataset.entryKey).toBe(key);
    expect(entries()[1].querySelectorAll<HTMLInputElement>("input")[1]).toBe(address);
    expect(document.activeElement).toBe(address);

    await act(async () => button("保存").click());
    await flush();
    const sent = JSON.parse(JSON.stringify(vi.mocked(setExtensionConfig).mock.calls[0][1])) as JsonObject;
    expect(sent).toEqual({
      accounts: [
        { _id: "entry_saved", name: "One", address: "https://one.example.com" },
        { name: "Two", address: "https://two.example.com" },
      ],
    });
    expect(JSON.stringify(sent)).not.toContain("new-entry-");
    expect(JSON.stringify(sent)).not.toContain("account_key");
  });

  it("uses the existing write-only dialog for the saved entry's own secret", async () => {
    await open();
    act(() => button("密钥", entries()[0]).click());
    const input = document.body.querySelector<HTMLInputElement>('input[aria-label="One · Account key"]')!;
    expect(input.type).toBe("password");
    act(() => typeInto(input, "placeholder-secret-5678"));
    await act(async () => button("保存", document.body).click());
    await flush();
    expect(setExtensionSecret).toHaveBeenCalledWith("example-entries", "entry_saved", "placeholder-secret-5678", expect.objectContaining({ managerId: "assistant" }));
    expect(setExtensionConfig).not.toHaveBeenCalled();
    expect(host.textContent).not.toContain("placeholder-secret-5678");
  });

  it("confirms removal and omits an emptied optional list only when the settings are saved", async () => {
    await open();
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="移除 One"]')!.click());
    expect(document.body.textContent).toContain("保存时将删除此条目及其已保存的密钥");
    expect(entries()).toHaveLength(1);
    act(() => button("移除条目", document.body).click());
    expect(entries()).toHaveLength(0);
    expect(setExtensionConfig).not.toHaveBeenCalled();
    await act(async () => button("保存").click());
    await flush();
    expect(vi.mocked(setExtensionConfig).mock.calls[0][1]).toEqual({});
  });

  it("saves an explicit empty array for a required list that permits zero entries", async () => {
    const next = detail();
    next.form!.fields![0].required = true;
    next.form!.fields![0].min_items = 0;
    hub.detail = next;
    await open();
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="移除 One"]')!.click());
    act(() => button("移除条目", document.body).click());
    await act(async () => button("保存").click());
    await flush();
    expect(vi.mocked(setExtensionConfig).mock.calls[0][1]).toEqual({ accounts: [] });
  });

  it("lets an optional list with a positive minimum return to its valid unset state", async () => {
    const next = detail();
    next.form!.fields![0].min_items = 1;
    hub.detail = next;
    await open();
    const remove = host.querySelector<HTMLButtonElement>('button[aria-label="移除 One"]')!;
    expect(remove.disabled).toBe(false);
    act(() => remove.click());
    act(() => button("移除条目", document.body).click());
    expect(entries()).toHaveLength(0);
    await act(async () => button("保存").click());
    await flush();
    expect(vi.mocked(setExtensionConfig).mock.calls[0][1]).toEqual({});
  });
});
