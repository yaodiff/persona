// @vitest-environment jsdom
// Holding noVNC 1.7's update requests while video shows (#6558): on the real RFB, a held connection's requests are
// dropped and the last one goes out when the hold ends; other connections are untouched.
import RFB from "@novnc/novnc";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { holdUpdates } from "./rfb-hold";

const ctx = { drawImage() {}, fillRect() {}, putImageData() {}, getImageData: () => ({ data: new Uint8ClampedArray(4) }), imageSmoothingEnabled: false };
type Sock = { flush: () => void; sQpush8: (v: number) => void };
const request = (rfb: object, incremental: boolean) =>
  (RFB as unknown as { messages: { fbUpdateRequest: (s: unknown, i: boolean, x: number, y: number, w: number, h: number) => void } }).messages.fbUpdateRequest(
    (rfb as { _sock: unknown })._sock, incremental, 0, 0, 480, 300);

beforeEach(() => {
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx as unknown as CanvasRenderingContext2D);
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("isSecureContext", true);
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const realRFB = () => {
  const channel = { send() {}, close() {}, binaryType: "", onerror: null, onmessage: null, onopen: null, protocol: "", readyState: WebSocket.CONNECTING };
  const div = document.createElement("div");
  document.body.append(div);
  return new RFB(div, channel as unknown as WebSocket);
};

describe("holdUpdates", () => {
  it("drops a held connection's requests and sends the last one when released", () => {
    const a = realRFB(), b = realRFB();
    const pushed: number[] = [];
    const sa = (a as unknown as { _sock: Sock })._sock;
    vi.spyOn(sa, "sQpush8").mockImplementation((v) => void pushed.push(v));
    vi.spyOn(sa, "flush").mockImplementation(() => {});
    const flushB = vi.spyOn((b as unknown as { _sock: Sock })._sock, "flush").mockImplementation(() => {});

    expect(holdUpdates(a, true)).toBe(true);
    request(a, true);
    request(a, false);
    expect(pushed).toEqual([]); // nothing went out while held
    request(b, true);
    expect(flushB).toHaveBeenCalledTimes(1); // another connection is untouched

    expect(holdUpdates(a, false)).toBe(true);
    expect(pushed).toEqual([3, 0]); // the last request (non-incremental) went out once: message type 3, incremental 0
    request(a, true);
    expect(pushed).toEqual([3, 0, 3, 1]); // and requests flow again
    a.disconnect();
    b.disconnect();
  });

  it("can't hold something that isn't noVNC's RFB, and changes nothing", () => {
    expect(holdUpdates({}, true)).toBe(false);
  });
});
