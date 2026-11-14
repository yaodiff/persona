// @vitest-environment jsdom
// The live screen's connection stats (#6558): a toggle in the bar of any live Mac (as the iOS app's ⋯ › Connection
// Stats), off by default and kept per Mac, showing a panel with what was measured on this connection and the round
// trip to the hub on its own line, only once a probe answered. The page hands noVNC the WebSocket it opened, so the
// bytes can be counted. A page an app embeds has no bar, so no toggle.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";

type FakeRFB = EventTarget & { canvas: HTMLCanvasElement; channel: unknown };
const rfbs = vi.hoisted(() => [] as FakeRFB[]);
const probes = vi.hoisted(() => ({
  frames: null as null | (() => void),
  hookable: true,
  bytes: null as null | ((n: number) => void),
  hub: null as null | ((ms: number) => void),
  stopped: [] as string[],
}));
vi.mock("@novnc/novnc", () => ({
  default: class extends EventTarget {
    viewOnly = true;
    canvas = document.createElement("canvas");
    channel: unknown;
    constructor(target: HTMLElement, channel: unknown) {
      super();
      this.channel = channel;
      target.append(this.canvas);
      rfbs.push(this as unknown as FakeRFB);
    }
    sendCredentials() {}
    disconnect() {
      this.canvas.remove();
    }
  },
}));
vi.mock("./probes", () => ({
  countFrames: (_r: object, on: () => void) => {
    if (!probes.hookable) return null;
    probes.frames = on;
    return () => probes.stopped.push("frames");
  },
  countBytes: (_s: WebSocket, on: (n: number) => void) => {
    probes.bytes = on;
    return () => probes.stopped.push("bytes");
  },
  probeHub: (on: (ms: number) => void) => {
    probes.hub = on;
    return () => probes.stopped.push("hub");
  },
}));
vi.mock("../views/theme", () => ({}));
vi.mock("./aes", () => ({ ensureSubtleAES: () => {} }));
vi.mock("../lib/api", async (actual) => ({
  ...(await actual<typeof import("../lib/api")>()),
  api: async (path: string) =>
    path === "/machines" ? { machines: [{ id: "macbook", name: "MacBook", online: true }] } : { enabled: true, screen_sharing: true, displays: [] },
}));
import { ScreenView } from "./ScreenView";
import { statsShown } from "./video";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket extends EventTarget {
  closed = false;
  constructor(public url: string) {
    super();
  }
  close() {
    this.closed = true;
  }
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const open = async (embedded = false) => {
  act(() => root.render(<ScreenView target={{ mid: "macbook", token: "tok", embedded, name: "MacBook", keep: "", unlock: false }} />));
  await flush();
};
const connect = async () => {
  act(() => void rfbs.at(-1)!.dispatchEvent(new CustomEvent("connect", { detail: {} })));
  await flush();
};
const toggle = () => host.querySelector<HTMLButtonElement>('[data-testid="screen-stats-toggle"]');
const panel = () => host.querySelector<HTMLElement>('[data-testid="screen-stats"]');
const toggleAppears = () => vi.waitFor(async () => {
  await act(async () => {});
  expect(toggle()).not.toBeNull();
}, { timeout: 3000 });
const second = () => act(() => void vi.advanceTimersByTime(1000));

beforeAll(async () => {
  await import("./StatsOverlay");
});

beforeEach(() => {
  rfbs.length = 0;
  Object.assign(probes, { frames: null, hookable: true, bytes: null, hub: null, stopped: [] });
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "performance"] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("connection stats on the live screen", () => {
  it("hands noVNC the RFB WebSocket the page opened", async () => {
    await open();
    const channel = rfbs.at(-1)!.channel as FakeSocket;
    expect(channel).toBeInstanceOf(FakeSocket);
    expect(channel.url).toMatch(/\/api\/v1\/machines\/macbook\/ports\/5900\/connect\?token=tok$/);
  });

  it("offers the toggle once live, off by default; on, the panel shows and the choice is kept per Mac", async () => {
    await open();
    expect(toggle()).toBeNull(); // not live yet
    await connect();
    await toggleAppears();
    expect(toggle()!.getAttribute("aria-pressed")).toBe("false");
    expect(toggle()!.getAttribute("aria-label")).toBe(t("screen-video.stats"));
    expect(panel()).toBeNull();
    act(() => toggle()!.click());
    expect(toggle()!.getAttribute("aria-pressed")).toBe("true");
    expect(panel()?.textContent).toContain(t("screen-video.stats_vnc"));
    expect(panel()?.textContent).toContain(t("screen-video.stats_via_hub"));
    expect(statsShown("macbook")).toBe(true);
    // the next page for this Mac opens with it on
    act(() => root.unmount());
    root = createRoot(host);
    await open();
    await connect();
    await toggleAppears();
    expect(panel()).not.toBeNull();
  });

  it("draws what was measured each second, the hub's round trip on its own line only once a probe answered", async () => {
    await open();
    await connect();
    await toggleAppears();
    act(() => toggle()!.click());
    const lines = () => [...panel()!.querySelectorAll("p")].map((p) => p.textContent);
    expect(lines()).toEqual([]); // nothing measured yet: mode and path only
    act(() => {
      probes.bytes!(250_000);
      for (let i = 0; i < 24; i++) probes.frames!();
    });
    second();
    expect(lines()).toEqual([`${t("screen-video.stats_mbps", { mbps: "2.0" })}·${t("screen-video.stats_fps", { fps: 24 })}`]);
    expect(host.querySelector('[data-testid="screen-stats-hub"]')).toBeNull();
    act(() => probes.hub!(23.4));
    second();
    expect(host.querySelector('[data-testid="screen-stats-hub"]')?.textContent).toBe(t("screen-video.stats_hub_round_trip", { ms: 23 }));
    // the round trip never joins the first line, where the picture's own latency would go
    expect(panel()!.firstElementChild!.textContent).not.toContain("23");
  });

  it("leaves the frame rate out when this noVNC could not be hooked", async () => {
    probes.hookable = false;
    await open();
    await connect();
    await toggleAppears();
    act(() => toggle()!.click());
    act(() => probes.bytes!(125_000));
    second();
    expect(panel()!.querySelector("p")?.textContent).toBe(t("screen-video.stats_mbps", { mbps: "1.0" }));
  });

  it("measures only while shown: closing the panel stops every probe and turns it off for this Mac", async () => {
    await open();
    await connect();
    await toggleAppears();
    act(() => toggle()!.click());
    expect(probes.stopped).toEqual([]);
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="screen-stats-close"]')!.click());
    expect(panel()).toBeNull();
    expect(probes.stopped.sort()).toEqual(["bytes", "frames", "hub"]);
    expect(statsShown("macbook")).toBe(false);
    expect(toggle()!.getAttribute("aria-pressed")).toBe("false");
  });

  it("has no toggle in a page an app embeds (no bar)", async () => {
    await open(true);
    await connect();
    await flush();
    expect(toggle()).toBeNull();
    expect(panel()).toBeNull();
  });
});
