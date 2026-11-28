// @vitest-environment jsdom
// The sidebar hub line keeps the newest status: a GET /hub/update that left before a hub_update push does not put the
// older status back, and a resumed hello (the hub replays the missed pushes) reads nothing again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(), held: [] as ((s: unknown) => void)[], reads: 0 }));
vi.mock("../../lib/hub-update", async (original) => ({
  ...(await original<typeof import("../../lib/hub-update")>()),
  setHubUpdateSettings: vi.fn(async () => ({ state: "idle" })),
  startHubUpdate: vi.fn(async () => ({ state: "building" })),
  hubUpdate: () => { hub.reads++; return new Promise((r) => hub.held.push(r)); },
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});
import { dismissedHubNotices, setHubUpdateSettings, startHubUpdate } from "../../lib/hub-update";
import { HubNoticeLine } from "./HubNoticeRow";

const base = { version: "1.0.0", build: 1, channel: "release", can_update: true, busy: [], auto: false, auto_notice: false, confirmed: true };
const latest = { version: "1.1.0", build: 2, newer: true, bad: false };
const emit = (type: string, m: Record<string, unknown>) => act(async () => hub.listeners.get(type)?.forEach((l) => l({ type, ...m })));
const updateButton = () => host.querySelector('[data-testid="hub-notice-update"]');

let host: HTMLDivElement;
let root: Root;
beforeEach(async () => {
  localStorage.clear();
  vi.clearAllMocks();
  hub.listeners.clear();
  hub.held.length = 0;
  hub.reads = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<HubNoticeLine />));
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

it("a read that left before a hub_update push does not put the older status back", async () => {
  // the update started while the mount's read was on its way: the push says so
  await emit("hub_update", { update: { ...base, state: "downloading", latest } });
  await act(() => vi.dynamicImportSettled());
  expect(host.querySelector('[data-kind="updating"]')).not.toBeNull();
  const during = host.textContent;
  await act(async () => hub.held[0]({ ...base, state: "idle", latest }));
  expect(updateButton()).toBeNull();
  expect(host.textContent).toBe(during);
  // a resumed hello replays the missed pushes: nothing to read again
  await emit("hello", { resumed: true });
  expect(hub.reads).toBe(1);
});

it("a fresh hello still reads, and only its newest read lands", async () => {
  await act(async () => hub.held[0]({ ...base, state: "idle", latest }));
  await act(() => vi.dynamicImportSettled());
  expect(updateButton()).not.toBeNull();
  // a hub that restarted: read again; an earlier read that answers last is older and stays dropped
  await emit("hello", {});
  await emit("hello", {});
  expect(hub.reads).toBe(3);
  await act(async () => hub.held[2]({ ...base, version: "1.1.0", build: 2, state: "idle", latest: { ...latest, newer: false } }));
  expect(updateButton()).toBeNull();
  await act(async () => hub.held[1]({ ...base, state: "idle", latest }));
  expect(updateButton()).toBeNull();
});

it("a read that left after the push lands", async () => {
  await emit("hub_update", { update: { ...base, state: "downloading", latest } });
  await emit("hello", {});
  await act(async () => hub.held[1]({ ...base, state: "idle", latest }));
  await act(() => vi.dynamicImportSettled());
  expect(updateButton()).not.toBeNull();
});

it.each([
  ["hub-notice-auto-off", { auto: false }],
  ["hub-notice-seen", { auto_notice_seen: true }],
] as const)("applies %s without opening Settings", async (id, settings) => {
  const path = location.href;
  await emit("hub_update", { update: { ...base, auto_notice: true } });
  await act(() => vi.dynamicImportSettled());
  await act(async () => host.querySelector<HTMLButtonElement>(`[data-testid="${id}"]`)!.click());
  expect(setHubUpdateSettings).toHaveBeenCalledWith(settings);
  expect(location.href).toBe(path);
});

it("starts the offered update or remembers its dismissal", async () => {
  const path = location.href;
  const available = { ...base, state: "idle", latest };
  await emit("hub_update", { update: available });
  await act(() => vi.dynamicImportSettled());
  await act(async () => (updateButton() as HTMLButtonElement).click());
  expect(startHubUpdate).toHaveBeenCalledWith(false);
  expect(host.querySelector('[data-kind="updating"]')).not.toBeNull();
  await emit("hub_update", { update: available });
  act(() => host.querySelector<HTMLButtonElement>('[data-testid="hub-notice-dismiss"]')!.click());
  expect(updateButton()).toBeNull();
  expect(dismissedHubNotices()).toContain("build:2");
  expect(location.href).toBe(path);
});
