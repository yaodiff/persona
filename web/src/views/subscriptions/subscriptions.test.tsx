// @vitest-environment jsdom
// Source subscriptions on the web (#2041): the pages are the manager's declarative UI at /api/v1/subscriptions/-/ui,
// drawn again when a subscription changes; entry rows only once the manager reports source_subscriptions, and only for
// connectors that can be subscribed.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({
  calls: [] as { path: string; body?: Record<string, unknown> }[],
  summary: [] as unknown[],
  features: [] as string[],
  listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(),
}));

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: async (path: string, init: { json?: Record<string, unknown> } = {}) => {
    hub.calls.push({ path, body: init.json });
    if (path === "/subscriptions/summary") return { connectors: hub.summary };
    return { view: { title: "Subscriptions", sections: [{ items: [{ type: "text", text: `drawn ${hub.calls.length}` }] }] } };
  },
}));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
  ...real,
  useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), features: hub.features }),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
  };
});

import { parse, href } from "../../lib/router";
import { countsOf, subscriptionsHref, SUBSCRIPTIONS_FEATURE, SUBSCRIPTIONS_UI } from "../../lib/subscriptions";
import { LegacyExtensionUI } from "../settings/ExtensionView";
import { countsText, SubscriptionsEntry } from "./SubscriptionsEntry";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  hub.calls.length = 0;
  hub.summary = [];
  hub.listeners.clear();
  hub.features = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("the pages", () => {
  it("are drawn by the manager's endpoint, with the first state, and again when a subscription changes", async () => {
    act(() => root.render(<LegacyExtensionUI extension={SUBSCRIPTIONS_UI} ui="pages" state='{"ext":"apps"}' />));
    await settle();
    expect(hub.calls[0]).toMatchObject({ path: "/subscriptions/-/ui", body: { state: '{"ext":"apps"}' } });
    expect(host.textContent).toContain("drawn 1");
    await act(async () => hub.listeners.get("subscription")?.forEach((l) => l({ type: "subscription", id: "sub_1", state: "ok" })));
    await settle();
    expect(hub.calls.filter((c) => c.path === "/subscriptions/-/ui").length).toBe(2);
  });

  it("an extension's page does not redraw on subscription events", async () => {
    act(() => root.render(<LegacyExtensionUI extension="demo" ui="home" />));
    await settle();
    expect(hub.listeners.get("subscription")?.size ?? 0).toBe(0);
    expect(hub.calls[0].path).toMatch(/^\/(extensions|plugins)\/demo\/-\/ui\/home$/);
  });

  it("open at /subscriptions, with a scope in ?state=", () => {
    expect(parse("/subscriptions")).toEqual({ name: "subscriptions" });
    expect(href({ name: "subscriptions" })).toMatch(/\/subscriptions$/);
    expect(subscriptionsHref()).toBe("/subscriptions");
    expect(decodeURIComponent(subscriptionsHref({ ext: "apps" }))).toBe('/subscriptions?state={"ext":"apps"}');
  });
});

describe("entry rows", () => {
  const line = (l: Partial<{ connector: string; extension: string; subscribable: boolean; on: number; failing: number }>) =>
    ({ connector: "apps", extension: "apps", on: 0, failing: 0, ...l });

  it("count what is on and failing, and hide when nothing can be subscribed", () => {
    expect(countsOf([line({ subscribable: true, on: 2, failing: 1 }), line({ connector: "x", extension: "other", subscribable: true, on: 5 })],
      (l) => l.extension === "apps")).toEqual({ on: 2, failing: 1 });
    expect(countsOf([line({ subscribable: false })], () => true)).toBeNull();
    expect(countsOf([line({})], () => true)).toBeNull(); // not known yet
    expect(countsOf([line({ subscribable: false, on: 1 })], () => true)).toEqual({ on: 1, failing: 0 }); // tools gone, subscriptions kept
    expect(countsText({ on: 0, failing: 0 })).toBeTruthy();
  });

  it("appear only once the manager reports source_subscriptions", async () => {
    hub.summary = [line({ subscribable: true, on: 2, failing: 1 })];
    act(() => root.render(<SubscriptionsEntry pick={(l) => l.extension === "apps"} scope={{ ext: "apps" }} />));
    await settle();
    expect(host.querySelector("button")).toBeNull();
    expect(hub.calls.some((c) => c.path === "/subscriptions/summary")).toBe(false);
    act(() => root.unmount());
    hub.features = [SUBSCRIPTIONS_FEATURE];
    root = createRoot(host);
    act(() => root.render(<SubscriptionsEntry pick={(l) => l.extension === "apps"} scope={{ ext: "apps" }} />));
    await settle();
    const row = host.querySelector("button");
    expect(row?.textContent).toContain(countsText({ on: 2, failing: 1 }));
  });

  it("can explain subscriptions where the row appears without the subscriptions page", async () => {
    hub.features = [SUBSCRIPTIONS_FEATURE];
    hub.summary = [line({ subscribable: true, on: 1, failing: 0 })];
    act(() => root.render(<SubscriptionsEntry pick={(l) => l.extension === "apps"} scope={{ ext: "apps" }} description="Subscriptions bring new items into your Inbox." />));
    await settle();
    expect(host.textContent).toContain("Subscriptions bring new items into your Inbox.");
  });
});
