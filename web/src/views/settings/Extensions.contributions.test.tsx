// @vitest-environment jsdom
// A contribution switch's label is the manifest's display title, else its id; the id stays in the hint and the
// request that saves the switch (#16522).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ detail: null as unknown, set: vi.fn() }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: () => Promise.resolve(hub.detail),
  setContribution: (...args: unknown[]) => {
    hub.set(...args);
    return Promise.resolve(hub.detail);
  },
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

const detail = (): HubExtensionDetail => ({
  id: "search-as-code",
  name: "Search as code",
  kind: "process",
  status: "running",
  source: "installed",
  enabled: true,
  contributions: [
    { point: "skills", id: "search-as-code", title: "Search code", enabled: true, can_disable: true },
    { point: "mcp_servers", id: "search-as-code", enabled: true, can_disable: true },
  ],
});

beforeEach(() => {
  hub.set.mockClear();
  hub.detail = detail();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("contribution switches", () => {
  it("shows the title, keeps the id in the hint, and saves by point and id", async () => {
    act(() => root.render(<Extensions open="search-as-code" onOpen={() => {}} />));
    await flush();

    const titled = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="Search code"]')!;
    const fallback = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="search-as-code"]')!;
    expect(titled).toBeTruthy();
    expect(fallback).toBeTruthy();
    expect(host.textContent).toContain("skills · search-as-code");

    act(() => titled.click());
    await flush();
    expect(hub.set).toHaveBeenCalledWith("search-as-code", "skills", "search-as-code", false, expect.objectContaining({ managerId: "assistant" }));
  });
});
