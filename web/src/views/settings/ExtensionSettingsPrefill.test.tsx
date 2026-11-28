// @vitest-environment jsdom
// A settings link's proposed values fill the existing extension form without saving; Save sends the form the owner sees.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../../lib/api";
import type { HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ detail: null as unknown, saved: [] as unknown[] }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: vi.fn(() => Promise.resolve(hub.detail)),
  setExtensionConfig: vi.fn((_id: string, config: unknown) => {
    hub.saved.push(config);
    return Promise.resolve({ ...(hub.detail as object), config });
  }),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { setExtensionConfig } from "../../lib/extensions";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let login = 0;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const input = (path: string) => host.querySelector<HTMLInputElement>(`[data-field="${path}"] input`)!;
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);

const detail = (): HubExtensionDetail => ({
  id: "example-ats",
  name: "Example ATS",
  kind: "process",
  status: "running",
  source: "installed",
  enabled: true,
  form: { fields: [
    { name: "machine", type: "string", title: "Machine" },
    { name: "repos", type: "array", title: "Repositories", items: { name: "items", type: "string" } },
    { name: "limits", type: "object", title: "Limits", fields: [
      { name: "max", type: "integer", title: "Max" },
      { name: "label", type: "string", title: "Label" },
    ] },
    { name: "accounts", type: "array", title: "Accounts", secret: { name: "entry_key", title: "Entry key" },
      items: { name: "items", type: "object", fields: [{ name: "name", type: "string", title: "Name" }] } },
  ] },
  config: { machine: "old-machine", limits: { max: 1, label: "Saved" }, accounts: [{ _id: "entry_one", name: "One" }] },
});

beforeEach(() => {
  setToken(`token-${++login}`);
  hub.detail = detail();
  hub.saved = [];
  vi.mocked(setExtensionConfig).mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("a proposed extension settings change", () => {
  it("shows ordinary proposed values, leaves unknown fields and entry lists alone, and saves only on Save", async () => {
    const prefill = {
      machine: "studio",
      repos: ["~/code"],
      limits: { max: 5, unknown: "ignored" },
      accounts: [{ name: "Other" }],
      api_key: "must not appear",
      unknown: "ignored",
    };
    act(() => root.render(<Extensions open="example-ats" onOpen={() => {}} prefill={prefill} />));
    await flush();

    expect(input("machine").value).toBe("studio");
    expect(input("limits.max").value).toBe("5");
    expect(input("limits.label").value).toBe("Saved");
    expect(host.textContent).toContain("~/code");
    expect(host.textContent).toContain("One");
    expect(host.textContent).not.toContain("must not appear");
    expect(setExtensionConfig).not.toHaveBeenCalled();

    await act(async () => button("保存")!.click());
    await flush();
    expect(setExtensionConfig).toHaveBeenCalledTimes(1);
    expect(setExtensionConfig).toHaveBeenCalledWith("example-ats", {
      machine: "studio",
      repos: ["~/code"],
      limits: { max: 5, label: "Saved" },
      accounts: [{ _id: "entry_one", name: "One" }],
    }, expect.objectContaining({ managerId: "assistant" }));
  });

  it("keeps the saved configuration as the base and applies once, so Revert removes the proposal", async () => {
    act(() => root.render(<Extensions open="example-ats" onOpen={() => {}} prefill={{ machine: "studio" }} />));
    await flush();
    expect(input("machine").value).toBe("studio");

    await act(async () => button("还原")!.click());
    await flush();
    expect(input("machine").value).toBe("old-machine");
    expect(button("保存")).toBeUndefined();
    expect(setExtensionConfig).not.toHaveBeenCalled();
  });
});
