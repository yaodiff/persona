// @vitest-environment jsdom
// Live screen cursor visibility and capture cleanup (#17828): runs against the real @novnc/novnc RFB (no RFB mock),
// driving an authentic RFB 3.8 handshake over an in-memory WebSocket so Cursor.attach/change/clear and events.js
// setCapture/#noVNC_mouse_capture_elem execute for real.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ensureVisibleCursor, needsVisibleCursor, pngCursorSize, VISIBLE_DOT_CURSOR } from "./cursor";
import { ScreenView } from "./ScreenView";

vi.mock("../../views/theme", () => ({}));
vi.mock("./aes", () => ({ ensureSubtleAES: () => {} }));
vi.mock("../../lib/api", async (actual) => ({
  ...(await actual<typeof import("../../lib/api")>()),
  api: async (path: string) =>
    path === "/machines" ? { machines: [{ id: "macbook", name: "MacBook", online: true }] } : { enabled: true, screen_sharing: true, displays: [] },
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** Minimal valid PNG header carrying exact IHDR width/height so noVNC's Cursor.change() produces realistic data URLs. */
function pngDataUrl(width: number, height: number): string {
  const bytes = new Uint8Array(24);
  bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52], 0);
  const view = new DataView(bytes.buffer);
  view.setUint32(16, width);
  view.setUint32(20, height);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return `data:image/png;base64,${btoa(bin)}`;
}

/** In-memory RFB 3.8 server over a fake WebSocket: completes security-type-None handshake and lets tests push updates. */
class FakeRFBSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances: FakeRFBSocket[] = [];

  readyState = FakeRFBSocket.CONNECTING;
  binaryType = "arraybuffer";
  protocol = "";
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private stage: "ver" | "sec" | "init" | "ready" = "ver";
  private listeners = new Map<string, Set<EventListener>>();

  constructor(public url: string) {
    FakeRFBSocket.instances.push(this);
    queueMicrotask(() => {
      if (this.readyState !== FakeRFBSocket.CONNECTING) return;
      this.readyState = FakeRFBSocket.OPEN;
      this.emit("open", new Event("open"));
      this.deliver(new TextEncoder().encode("RFB 003.008\n"));
    });
  }

  addEventListener(type: string, fn: EventListener) {
    let set = this.listeners.get(type);
    if (!set) this.listeners.set(type, (set = new Set()));
    set.add(fn);
  }
  removeEventListener(type: string, fn: EventListener) {
    this.listeners.get(type)?.delete(fn);
  }

  send(data: ArrayBuffer | Uint8Array) {
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    if (this.stage === "ver" && bytes.length >= 12) {
      this.stage = "sec";
      this.deliver(new Uint8Array([1, 1])); // 1 security type: None (1)
    } else if (this.stage === "sec" && bytes.length >= 1) {
      this.stage = "init";
      this.deliver(new Uint8Array([0, 0, 0, 0])); // SecurityResult: OK
    } else if (this.stage === "init" && bytes.length >= 1) {
      this.stage = "ready";
      const name = new TextEncoder().encode("Mock Mac");
      const init = new Uint8Array(24 + name.length);
      const dv = new DataView(init.buffer);
      dv.setUint16(0, 480); // fb width
      dv.setUint16(2, 300); // fb height
      init.set([32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0], 4);
      dv.setUint32(20, name.length);
      init.set(name, 24);
      this.deliver(init);
    }
  }

  /** Sends an RFB FramebufferUpdate containing a single PseudoCursor (-239) rectangle. */
  sendPseudoCursor(w: number, h: number, hotX: number, hotY: number, opaque = true) {
    const pixels = new Uint8Array(w * h * 4);
    const maskBytes = Math.ceil(w / 8) * h;
    const mask = new Uint8Array(maskBytes);
    if (opaque && w > 0 && h > 0) {
      pixels.fill(255);
      mask.fill(0xff);
    }
    const msg = new Uint8Array(4 + 12 + pixels.length + mask.length);
    const dv = new DataView(msg.buffer);
    msg[0] = 0; // FramebufferUpdate
    dv.setUint16(2, 1); // 1 rect
    dv.setUint16(4, hotX);
    dv.setUint16(6, hotY);
    dv.setUint16(8, w);
    dv.setUint16(10, h);
    dv.setInt32(12, -239); // pseudoEncodingCursor
    msg.set(pixels, 16);
    msg.set(mask, 16 + pixels.length);
    this.deliver(msg);
  }

  serverClose(wasClean = false) {
    if (this.readyState === FakeRFBSocket.CLOSED) return;
    this.readyState = FakeRFBSocket.CLOSED;
    const ev = Object.assign(new Event("close"), { wasClean, code: wasClean ? 1000 : 1006, reason: "" });
    this.emit("close", ev);
  }

  close() {
    this.serverClose(true);
  }

  private deliver(bytes: Uint8Array) {
    const copy = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
    queueMicrotask(() => {
      if (this.readyState !== FakeRFBSocket.OPEN) return;
      this.emit("message", new MessageEvent("message", { data: copy }));
    });
  }

  private emit(type: string, ev: Event) {
    const handler = (this as unknown as Record<string, ((e: Event) => void) | null>)[`on${type}`];
    handler?.(ev);
    for (const fn of this.listeners.get(type) ?? []) fn(ev);
  }
}

const ctx2d = {
  drawImage() {},
  fillRect() {},
  clearRect() {},
  putImageData() {},
  getImageData: () => ({ data: new Uint8ClampedArray(4) }),
  imageSmoothingEnabled: false,
};

let host: HTMLDivElement;
let root: Root;
let origElementFromPoint: typeof document.elementFromPoint | undefined;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};

beforeEach(() => {
  FakeRFBSocket.instances.length = 0;
  origElementFromPoint = document.elementFromPoint;
  document.elementFromPoint = () => null;
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx2d as unknown as CanvasRenderingContext2D);
  vi.spyOn(HTMLCanvasElement.prototype, "toDataURL").mockImplementation(function (this: HTMLCanvasElement) {
    return pngDataUrl(this.width, this.height);
  });
  vi.stubGlobal("ImageData", class {
    constructor(public data: Uint8ClampedArray, public width: number, public height: number) {}
  });
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("WebSocket", FakeRFBSocket);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.getElementById("noVNC_mouse_capture_elem")?.remove();
  if (origElementFromPoint) document.elementFromPoint = origElementFromPoint;
  else delete (document as Partial<Document>).elementFromPoint;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function openLive() {
  act(() => root.render(<ScreenView target={{ mid: "macbook", token: "tok", embedded: false, name: "MacBook", keep: "", unlock: false }} />));
  await flush();
  await vi.waitFor(() => {
    expect(host.querySelector('[data-testid="screen"] canvas')).not.toBeNull();
    expect(host.querySelectorAll('header [role="group"] button')).toHaveLength(2);
  });
  const screen = host.querySelector<HTMLDivElement>('[data-testid="screen"]')!;
  const canvas = screen.querySelector<HTMLCanvasElement>("canvas")!;
  const [viewOnlyBtn, takeControlBtn] = [...host.querySelectorAll<HTMLButtonElement>('header [role="group"] button')];
  return { screen, canvas, viewOnlyBtn, takeControlBtn, sock: FakeRFBSocket.instances.at(-1)! };
}

function captureProxy(): HTMLElement | null {
  return document.getElementById("noVNC_mouse_capture_elem");
}

function assertVisibleCursor(el: HTMLElement) {
  const cur = getComputedStyle(el).cursor || el.style.cursor;
  expect(cur).not.toBe("");
  expect(cur).not.toBe("none");
  const size = pngCursorSize(cur);
  if (size) {
    // Must not be noVNC's tiny 3x3 dot cursor or a 0x0 empty PNG.
    expect(Math.max(size.w, size.h)).toBeGreaterThan(4);
  }
}

describe("ScreenView live cursor and capture cleanup (real noVNC)", () => {
  it("disables pointer hits on the screen target in View only mode so the browser keeps its normal cursor", async () => {
    const { screen, canvas } = await openLive();
    expect(screen.classList.contains("pointer-events-none")).toBe(true);
    // Even if inspected directly, the RFB canvas itself is never stuck at cursor: none.
    assertVisibleCursor(canvas);
  });

  it("enables pointer interaction and a clear non-none cursor in Take control mode when the server sends no cursor", async () => {
    const { screen, canvas, takeControlBtn } = await openLive();
    act(() => takeControlBtn.click());
    await flush();

    expect(screen.classList.contains("pointer-events-none")).toBe(false);
    assertVisibleCursor(canvas);
  });

  it("preserves a real remote PseudoCursor (-239) and falls back to a visible cursor on a 0x0 or transparent cursor", async () => {
    const { canvas, takeControlBtn, sock } = await openLive();
    act(() => takeControlBtn.click());
    await flush();

    // Real RFB PseudoCursor (-239) flows into noVNC's Cursor; under jsdom (supportsCursorURIs=false -> useFallback=true),
    // noVNC updates its body-mounted fallback canvas while our guard keeps the RFB canvas off cursor:none.
    act(() => sock.sendPseudoCursor(12, 16, 1, 2, true));
    await flush();
    const fallbackCursorCanvas = [...document.body.children].find((el): el is HTMLCanvasElement => el.tagName === "CANVAS");
    if (fallbackCursorCanvas) {
      expect(fallbackCursorCanvas.width).toBe(12);
      expect(fallbackCursorCanvas.height).toBe(16);
    } else {
      expect(pngCursorSize(canvas.style.cursor)).toEqual({ w: 12, h: 16 });
    }
    assertVisibleCursor(canvas);

    // Server sends an empty (0x0) cursor -> must not fall back to cursor: none or tiny 3x3 dot.
    act(() => sock.sendPseudoCursor(0, 0, 0, 0, false));
    await flush();
    assertVisibleCursor(canvas);

    // Server sends a fully transparent cursor -> must still stay clearly visible.
    act(() => sock.sendPseudoCursor(8, 8, 0, 0, false));
    await flush();
    assertVisibleCursor(canvas);
  });

  it("upgrades none and tiny 3x3 dot cursors to the 11x11 visible dot while keeping real remote PNG cursors", () => {
    const tinyDot = `url("${pngDataUrl(3, 3)}") 1 1, default`;
    const remoteArrow = `url("${pngDataUrl(12, 16)}") 1 2, default`;
    expect(pngCursorSize(VISIBLE_DOT_CURSOR)).toEqual({ w: 11, h: 11 });
    expect(needsVisibleCursor("")).toBe(true);
    expect(needsVisibleCursor("none")).toBe(true);
    expect(needsVisibleCursor("url(data:,) 1 1, default")).toBe(true);
    expect(needsVisibleCursor(tinyDot)).toBe(true);
    expect(needsVisibleCursor(VISIBLE_DOT_CURSOR)).toBe(false);
    expect(needsVisibleCursor(remoteArrow)).toBe(false);

    // Browser-like element that accepts data-URI hotspot cursors: tiny 3x3 dot upgrades to 11x11, 12x16 stays untouched.
    const fakeEl = { style: { cursor: tinyDot } } as unknown as HTMLElement;
    const origGCS = globalThis.getComputedStyle;
    vi.stubGlobal("getComputedStyle", (el: HTMLElement) => ({ cursor: el.style.cursor }));
    try {
      ensureVisibleCursor(fakeEl);
      expect(fakeEl.style.cursor).toBe(VISIBLE_DOT_CURSOR);

      fakeEl.style.cursor = "none";
      ensureVisibleCursor(fakeEl);
      expect(fakeEl.style.cursor).toBe(VISIBLE_DOT_CURSOR);

      fakeEl.style.cursor = remoteArrow;
      ensureVisibleCursor(fakeEl);
      expect(fakeEl.style.cursor).toBe(remoteArrow);
    } finally {
      vi.stubGlobal("getComputedStyle", origGCS);
    }
  });

  it("recovers if noVNC (e.g. touch useFallback) forces canvas.style.cursor back to none", async () => {
    const { canvas, takeControlBtn } = await openLive();
    act(() => takeControlBtn.click());
    await flush();

    // Simulate noVNC's touch useFallback leaving canvas.style.cursor = "none" while a mouse connects.
    act(() => {
      canvas.style.cursor = "none";
    });
    await flush();
    assertVisibleCursor(canvas);

    // Pressing mouse down opens #noVNC_mouse_capture_elem; its cursor must also never be none.
    act(() => {
      canvas.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, buttons: 1 }));
    });
    await flush();
    const proxy = captureProxy();
    expect(proxy).not.toBeNull();
    expect(proxy!.style.display).not.toBe("none");
    assertVisibleCursor(proxy!);
  });

  it("releases #noVNC_mouse_capture_elem on disconnect, window blur, document hidden, and switching back to View only", async () => {
    const { canvas, viewOnlyBtn, takeControlBtn, sock } = await openLive();
    act(() => takeControlBtn.click());
    await flush();

    const startDrag = async () => {
      act(() => {
        canvas.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, buttons: 1 }));
      });
      await flush();
      expect(captureProxy()?.style.display).not.toBe("none");
    };

    // 1. Window blur
    await startDrag();
    act(() => {
      window.dispatchEvent(new Event("blur"));
    });
    await flush();
    expect(captureProxy()?.style.display).toBe("none");

    // 2. Document becomes hidden
    await startDrag();
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await flush();
    expect(captureProxy()?.style.display).toBe("none");
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });

    // 3. Switching from Take control back to View only
    await startDrag();
    act(() => viewOnlyBtn.click());
    await flush();
    expect(captureProxy()?.style.display).toBe("none");

    // 4. Disconnect while mouse button is held
    act(() => takeControlBtn.click());
    await flush();
    await startDrag();
    act(() => sock.serverClose(false));
    await flush();
    expect(captureProxy()?.style.display).toBe("none");
  });
});
