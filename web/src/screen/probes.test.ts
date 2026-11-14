// @vitest-environment jsdom
// What the live screen measures for its connection stats (#6558): the bytes on the RFB socket, the frames noVNC draws
// (hooked on the real noVNC 1.7 display, so an upgrade that changes it fails here), and the round trip to the hub, timed
// by each probe's own Resource Timing entry and skipped whenever that timing is missing.
import RFB from "@novnc/novnc";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { countBytes, countFrames, PROBE_EVERY_MS, probeHub } from "./probes";

describe("countBytes", () => {
  it("counts every message the socket receives, beside its other listeners, until stopped", () => {
    const socket = new EventTarget() as WebSocket;
    const got: number[] = [];
    const stop = countBytes(socket, (n) => got.push(n));
    socket.dispatchEvent(new MessageEvent("message", { data: new ArrayBuffer(12) }));
    socket.dispatchEvent(new MessageEvent("message", { data: new Uint8Array(5) }));
    stop();
    socket.dispatchEvent(new MessageEvent("message", { data: new ArrayBuffer(99) }));
    expect(got).toEqual([12, 5]);
  });
});

describe("countFrames on noVNC 1.7", () => {
  const ctx = { drawImage() {}, fillRect() {}, putImageData() {}, getImageData: () => ({ data: new Uint8ClampedArray(4) }), imageSmoothingEnabled: false };
  beforeEach(() => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx as unknown as CanvasRenderingContext2D);
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("isSecureContext", true); // the page's https; noVNC only warns without it
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  /** A real RFB on a channel that never opens: its display is the one the page's noVNC has. */
  const realRFB = () => {
    const channel = { send() {}, close() {}, binaryType: "", onerror: null, onmessage: null, onopen: null, protocol: "", readyState: WebSocket.CONNECTING };
    const div = document.createElement("div");
    document.body.append(div);
    return new RFB(div, channel as unknown as WebSocket);
  };

  it("counts a frame for each flip that draws: at once, or when its render queue reaches it", () => {
    const r = realRFB();
    const display = (r as unknown as { _display: { flip: (q?: boolean) => void; _renderQ: unknown[] } })._display;
    let frames = 0;
    const stop = countFrames(r, () => frames++);
    expect(stop).not.toBeNull();
    display.flip(); // nothing queued: drawn now
    expect(frames).toBe(1);
    display._renderQ.push({ type: "img" }); // an image still decoding: the flip is queued behind it
    display.flip();
    expect(frames).toBe(1);
    display._renderQ.length = 0;
    display.flip(true); // the queue draws it
    expect(frames).toBe(2);
    stop!();
    display.flip();
    expect(frames).toBe(2);
    expect(Object.prototype.hasOwnProperty.call(display, "flip")).toBe(false); // noVNC's own method again
    r.disconnect();
  });

  it("hooks nothing on a display it does not know, so no frame rate is shown", () => {
    expect(countFrames({}, () => {})).toBeNull();
    expect(countFrames({ _display: { flip() {} } }, () => {})).toBeNull(); // no render queue
    expect(countFrames({ _display: { _renderQ: [] } }, () => {})).toBeNull();
  });
});

describe("probeHub", () => {
  type Entry = { name: string; requestStart: number; responseStart: number };
  let observers: { cb: (l: { getEntries: () => Entry[] }) => void; gone: boolean }[];
  let fetches: { url: string; init: RequestInit }[];
  let answer: () => Promise<{ ok: boolean; json: () => Promise<unknown> }>;
  let visible: DocumentVisibilityState;
  const emit = (e: Entry) => observers.filter((o) => !o.gone).forEach((o) => o.cb({ getEntries: () => [e] }));
  const settle = async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  };

  beforeEach(() => {
    vi.useFakeTimers();
    observers = [];
    fetches = [];
    visible = "visible";
    answer = async () => ({ ok: true, json: async () => ({ ok: true }) });
    vi.stubGlobal(
      "PerformanceObserver",
      class {
        static supportedEntryTypes = ["resource"];
        o: { cb: (l: { getEntries: () => Entry[] }) => void; gone: boolean };
        constructor(cb: (l: { getEntries: () => Entry[] }) => void) {
          this.o = { cb, gone: false };
          observers.push(this.o);
        }
        observe() {}
        disconnect() {
          this.o.gone = true;
        }
      },
    );
    vi.stubGlobal("fetch", (url: string, init: RequestInit) => {
      fetches.push({ url, init });
      return answer();
    });
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visible });
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    delete (document as { visibilityState?: unknown }).visibilityState;
  });

  it("times GET /api/v1/health by its own entry, uncached, every few seconds, and stops", async () => {
    const got: number[] = [];
    const stop = probeHub((ms) => got.push(ms), "http://hub");
    expect(fetches.map((f) => f.url)).toEqual(["http://hub/api/v1/health?probe=1"]);
    expect(fetches[0].init.cache).toBe("no-store");
    expect(fetches[0].init.redirect).toBe("manual");
    emit({ name: "http://hub/api/v1/other", requestStart: 1, responseStart: 900 }); // not ours: ignored
    emit({ name: "http://hub/api/v1/health?probe=1", requestStart: 100, responseStart: 124 });
    await settle();
    expect(got).toEqual([24]);
    // the answer may come before or after its entry
    await vi.advanceTimersByTimeAsync(PROBE_EVERY_MS);
    await settle();
    emit({ name: "http://hub/api/v1/health?probe=2", requestStart: 2000, responseStart: 2031 });
    expect(got).toEqual([24, 31]);
    stop();
    expect(observers.every((o) => o.gone)).toBe(true);
    await vi.advanceTimersByTimeAsync(PROBE_EVERY_MS * 3);
    expect(fetches).toHaveLength(2);
  });

  it("skips a probe whose timing is missing, and one the hub did not answer", async () => {
    const got: number[] = [];
    const stop = probeHub((ms) => got.push(ms), "http://hub");
    await settle();
    emit({ name: "http://hub/api/v1/health?probe=1", requestStart: 0, responseStart: 0 }); // no timing (cross-origin, say)
    answer = async () => ({ ok: false, json: async () => ({}) }); // a sign-in layer, a 502
    await vi.advanceTimersByTimeAsync(PROBE_EVERY_MS);
    await settle();
    emit({ name: "http://hub/api/v1/health?probe=2", requestStart: 10, responseStart: 30 });
    answer = async () => ({ ok: true, json: async () => ({ hello: 1 }) }); // 200, but not the hub's health
    await vi.advanceTimersByTimeAsync(PROBE_EVERY_MS);
    await settle();
    emit({ name: "http://hub/api/v1/health?probe=3", requestStart: 10, responseStart: 30 });
    expect(got).toEqual([]);
    stop();
  });

  it("probes nothing while the page is in the background, and once at once when it is back", async () => {
    const stop = probeHub(() => {}, "http://hub");
    await settle();
    expect(fetches).toHaveLength(1);
    visible = "hidden";
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(PROBE_EVERY_MS * 5);
    expect(fetches).toHaveLength(1);
    visible = "visible";
    document.dispatchEvent(new Event("visibilitychange"));
    expect(fetches).toHaveLength(2);
    stop();
  });

  it("probes nothing in a browser whose observers get no resource entries", () => {
    vi.stubGlobal("PerformanceObserver", class { static supportedEntryTypes = ["mark"]; observe() {} disconnect() {} });
    const stop = probeHub(() => {}, "http://hub");
    expect(fetches).toHaveLength(0);
    stop();
  });
});
