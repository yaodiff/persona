// @vitest-environment jsdom
// Native <select> overlay and PNG/high-DPI frame handling in PreviewPanel (docs/web/preview.md).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const pointer = vi.hoisted(() => ({ fine: true }));
vi.mock("../../lib/viewport", async (original) => ({ ...(await original<typeof import("../../lib/viewport")>()), hasFinePointer: () => pointer.fine }));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (path.includes("/preview/servers")) return { servers: [{ name: "web", port: 5173, listening: true }] };
      if (path.includes("/ports/5173/open")) return { port: 5173, listening: true };
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  const state = {
    ...real.getState(),
    sessions: { m1: [{ machine_id: "m1", session_id: "s1", workspace_id: "w1", cwd: "/project" }] },
    machines: [{ id: "m1", name: "box", online: true }],
  };
  return { ...real, useStore: <T,>(select: (s: typeof state) => T): T => select(state) };
});

import { t } from "../../i18n";
import { PreviewPanel } from "./PreviewPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket | null = null;
  binaryType = "";
  readyState = 0;
  sent: Record<string, unknown>[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() {
    FakeSocket.last = this;
  }
  send(data: string) {
    this.sent.push(JSON.parse(data));
  }
  close() {}
  receiveJson(msg: Record<string, unknown>) {
    act(() => this.onmessage?.({ data: JSON.stringify(msg) }));
  }
  receiveBinary(buf: ArrayBuffer) {
    act(() => this.onmessage?.({ data: buf }));
  }
}

let resizeCb: ResizeObserverCallback | null = null;
let host: HTMLDivElement;
let root: Root;
let bitmapBlobs: Blob[] = [];
let bitmapSize = { width: 1200, height: 1600 };
let bitmapFail = false;

function framePacket(seq: number, w: number, h: number, bytes: number[]): ArrayBuffer {
  const out = new Uint8Array(9 + bytes.length);
  const v = new DataView(out.buffer);
  v.setUint8(0, 0x66); // 'f'
  v.setUint32(1, seq);
  v.setUint16(5, w);
  v.setUint16(7, h);
  out.set(bytes, 9);
  return out.buffer;
}

const PNG_BYTES = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00];
const JPEG_BYTES = [0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10];

beforeEach(() => {
  pointer.fine = true;
  FakeSocket.last = null;
  resizeCb = null;
  bitmapBlobs = [];
  bitmapSize = { width: 1200, height: 1600 };
  bitmapFail = false;
  sessionStorage.clear();
  vi.stubGlobal("devicePixelRatio", 2);
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("fine"), media: query, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("ResizeObserver", class {
    constructor(cb: ResizeObserverCallback) {
      resizeCb = cb;
    }
    observe() {
      resizeCb?.([{ contentRect: { width: 600, height: 800 } } as ResizeObserverEntry], this as unknown as ResizeObserver);
    }
    unobserve() {}
    disconnect() {}
  });
  vi.stubGlobal("createImageBitmap", vi.fn(async (blob: Blob) => {
    bitmapBlobs.push(blob);
    if (bitmapFail) throw new Error("decode failed");
    return { width: bitmapSize.width, height: bitmapSize.height, close: vi.fn() };
  }));
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ drawImage: vi.fn() } as unknown as CanvasRenderingContext2D);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => await Promise.resolve());
};

async function live(size = { width: 600, height: 800 }) {
  act(() => root.render(<PreviewPanel mid="m1" sid="s1" cwd="/project" port={5173} onClose={() => {}} />));
  await flush();
  const sock = FakeSocket.last!;
  expect(sock).not.toBeNull();
  act(() => {
    sock.readyState = FakeSocket.OPEN;
    sock.onopen?.();
  });
  const canvas = host.querySelector<HTMLCanvasElement>("[data-preview-canvas]")!;
  const box = canvas.parentElement!;
  canvas.setPointerCapture = () => {};
  vi.spyOn(box, "getBoundingClientRect").mockReturnValue({
    left: 0, top: 0, right: size.width, bottom: size.height, width: size.width, height: size.height, x: 0, y: 0, toJSON() {},
  });
  vi.spyOn(canvas, "getBoundingClientRect").mockReturnValue({
    left: 0, top: 0, right: size.width, bottom: size.height, width: size.width, height: size.height, x: 0, y: 0, toJSON() {},
  });
  const keys = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${t("preview.keyboard_label")}"]`)!;
  return { sock, canvas, box, keys };
}

const SAMPLE_SELECT = {
  t: "select_open",
  id: 7,
  rect: { x: 40, y: 120, w: 220, h: 36 },
  selected: 2,
  options: [
    { index: 0, label: "<b>Editor</b>", group: "Workspace" },
    { index: 1, label: "Owner (managed)", disabled: true, group: "Workspace" },
    { index: 2, label: "Viewer", group: "Workspace" },
    { index: 4, label: "Reviewer", group: "Automation" },
    { index: 5, label: "Archived", disabled: true, group: "Automation" },
  ],
};

function listbox() {
  return document.querySelector<HTMLElement>('[role="listbox"]');
}

function options() {
  return [...document.querySelectorAll<HTMLElement>('[role="option"]')];
}

function keydown(key: string, target: EventTarget = document.activeElement || window) {
  act(() => void target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));
}

describe("PreviewPanel ready features and high-DPI frame decoding", () => {
  it("sends features {select: true, png: true} every time ready arrives", async () => {
    const { sock } = await live();
    sock.receiveJson({ t: "ready", browser: "Chrome", w: 600, h: 800 });
    expect(sock.sent).toContainEqual({ t: "features", select: true, png: true });

    sock.sent.length = 0;
    sock.receiveJson({ t: "ready", browser: "Chrome", w: 600, h: 800 });
    expect(sock.sent).toContainEqual({ t: "features", select: true, png: true });
  });

  it("decodes PNG and JPEG f frames by magic bytes, sizes backing store to image pixels, and acks every f frame", async () => {
    const { sock, canvas } = await live();
    bitmapSize = { width: 1200, height: 1600 };
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();
    expect(bitmapBlobs[0]?.type).toBe("image/png");
    expect(canvas.width).toBe(1200);
    expect(canvas.height).toBe(1600);
    expect(canvas.style.width).toBe("600px");
    expect(canvas.style.height).toBe("800px");
    expect(sock.sent).toContainEqual({ t: "ack", seq: 1 });

    sock.receiveBinary(framePacket(2, 600, 800, JPEG_BYTES));
    await flush();
    expect(bitmapBlobs[1]?.type).toBe("image/jpeg");
    expect(sock.sent).toContainEqual({ t: "ack", seq: 2 });

    // Unknown/failed frames must still be acknowledged so node in-flight accounting never stalls.
    bitmapFail = true;
    sock.receiveBinary(framePacket(3, 600, 800, PNG_BYTES));
    await flush();
    expect(sock.sent).toContainEqual({ t: "ack", seq: 3 });

    sock.receiveBinary(framePacket(4, 600, 800, [0x01, 0x02, 0x03, 0x04]));
    await flush();
    expect(sock.sent).toContainEqual({ t: "ack", seq: 4 });
  });
});

describe("PreviewPanel native <select> overlay", () => {
  it("renders accessible listbox, optgroups, disabled items, plain-text labels, and positions below the select", async () => {
    const { sock } = await live();
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();
    sock.receiveJson(SAMPLE_SELECT);

    const list = listbox();
    expect(list).not.toBeNull();
    const opts = options();
    expect(opts).toHaveLength(5);
    // Labels are rendered strictly as plain text, never HTML.
    expect(opts[0].textContent).toContain("<b>Editor</b>");
    expect(opts[0].querySelector("b")).toBeNull();
    expect(opts[1].getAttribute("aria-disabled")).toBe("true");
    expect(opts[2].getAttribute("aria-selected")).toBe("true");
    expect(opts[0].getAttribute("aria-selected")).toBe("false");
    // Optgroup headings shown once per group.
    const groups = [...list!.querySelectorAll("[data-preview-select-group]")].map((el) => el.textContent);
    expect(groups).toEqual(["Workspace", "Automation"]);

    // Positioned below y + h = 156, width at least select width (220px).
    const top = parseFloat(list!.style.top);
    const left = parseFloat(list!.style.left);
    const minW = parseFloat(list!.style.minWidth || list!.style.width);
    expect(top).toBeGreaterThanOrEqual(156);
    expect(left).toBe(40);
    expect(minW).toBeGreaterThanOrEqual(220);
  });

  it("flips above the select when space below is insufficient", async () => {
    const { sock } = await live();
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();
    sock.receiveJson({
      ...SAMPLE_SELECT,
      id: 8,
      rect: { x: 40, y: 740, w: 200, h: 36 },
    });

    const list = listbox()!;
    expect(list).not.toBeNull();
    expect(list.dataset.placement).toBe("top");
    expect(parseFloat(list.style.top)).toBeLessThan(740);
  });

  it("selects on mouse click, ignores disabled items, sends select_choose with option.index (including 0), and returns focus to keys", async () => {
    const { sock, keys } = await live();
    act(() => keys.focus());
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();
    sock.receiveJson(SAMPLE_SELECT);

    // Clicking disabled option does nothing.
    act(() => options()[1].click());
    expect(listbox()).not.toBeNull();
    expect(sock.sent.filter((m) => m.t === "select_choose")).toHaveLength(0);

    // Clicking option with index 0 sends index: 0 and closes.
    act(() => options()[0].click());
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_choose", id: 7, index: 0 });
    expect(document.activeElement).toBe(keys);
  });

  it("supports keyboard navigation skipping disabled options, Home/End, Enter/Space selection, and blocks forwarding keys to the page", async () => {
    const { sock, keys } = await live();
    act(() => keys.focus());
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();
    sock.receiveJson(SAMPLE_SELECT);
    sock.sent.length = 0;

    // Initial active is selected option (Viewer, index 2). ArrowUp skips disabled index 1 -> index 0.
    keydown("ArrowUp");
    expect(sock.sent.filter((m) => m.t === "key")).toHaveLength(0);
    // End jumps to last enabled option (Reviewer, index 4, skipping disabled index 5).
    keydown("End");
    keydown("Enter");
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_choose", id: 7, index: 4 });
    expect(sock.sent.filter((m) => m.t === "key")).toHaveLength(0);
    expect(document.activeElement).toBe(keys);

    // Reopen and choose with Home + Space.
    sock.sent.length = 0;
    sock.receiveJson({ ...SAMPLE_SELECT, id: 9, selected: 4 });
    keydown("Home");
    keydown(" ");
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_choose", id: 9, index: 0 });
    expect(sock.sent.filter((m) => m.t === "key")).toHaveLength(0);
    expect(document.activeElement).toBe(keys);

    // Matching keyup right after Enter/Space selection must not leak to the page.
    act(() => void keys.dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true, cancelable: true })));
    expect(sock.sent.filter((m) => m.t === "key")).toHaveLength(0);

    // ArrowDown from index 0 skips disabled index 1 and lands on index 2.
    sock.sent.length = 0;
    sock.receiveJson({ ...SAMPLE_SELECT, id: 100, selected: 0 });
    keydown("ArrowDown");
    keydown("Enter");
    expect(sock.sent).toContainEqual({ t: "select_choose", id: 100, index: 2 });
  });

  it("scrolls a long list to the currently selected option and supports touch selection", async () => {
    pointer.fine = false;
    const { sock } = await live();
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();
    const many = Array.from({ length: 40 }, (_, i) => ({ index: i, label: `Option ${i}` }));
    sock.receiveJson({ t: "select_open", id: 30, rect: { x: 20, y: 100, w: 200, h: 36 }, selected: 25, options: many });

    const list = listbox()!;
    expect(list).not.toBeNull();
    expect(parseFloat(list.style.maxHeight)).toBeLessThanOrEqual(280);
    // Simulate scrollable geometry and trigger a fresh select_open so scrollTop centers the selected row.
    Object.defineProperty(list, "scrollHeight", { value: 1280, configurable: true });
    Object.defineProperty(list, "clientHeight", { value: 280, configurable: true });
    const opts = options();
    opts.forEach((el, i) => {
      Object.defineProperty(el, "offsetTop", { value: i * 32, configurable: true });
      Object.defineProperty(el, "offsetHeight", { value: 32, configurable: true });
    });
    sock.receiveJson({ t: "select_open", id: 31, rect: { x: 20, y: 100, w: 200, h: 36 }, selected: 25, options: many });
    expect(listbox()!.scrollTop).toBeGreaterThan(0);

    // Touch tap selects option cleanly.
    const target = options()[3];
    act(() => {
      target.dispatchEvent(new PointerEvent("pointerdown", { pointerType: "touch", pointerId: 1, bubbles: true, cancelable: true }));
      target.dispatchEvent(new PointerEvent("pointerup", { pointerType: "touch", pointerId: 1, bubbles: true, cancelable: true }));
      target.click();
    });
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_choose", id: 31, index: 3 });
  });

  it("closes and sends select_close on Esc, outside click (without forwarding click to page), wheel, resize, Desktop/Phone toggle, and address navigation", async () => {
    const { sock, canvas, keys } = await live();
    act(() => keys.focus());
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();

    // 1. Escape
    sock.receiveJson({ ...SAMPLE_SELECT, id: 10 });
    sock.sent.length = 0;
    keydown("Escape");
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_close", id: 10 });
    expect(sock.sent.filter((m) => m.t === "key")).toHaveLength(0);
    expect(document.activeElement).toBe(keys);

    // 2. Outside pointerdown/pointerup on canvas must close and NOT forward mouse event to page
    sock.receiveJson({ ...SAMPLE_SELECT, id: 11 });
    sock.sent.length = 0;
    act(() => {
      canvas.dispatchEvent(new PointerEvent("pointerdown", { clientX: 500, clientY: 500, pointerId: 1, button: 0, pointerType: "mouse", bubbles: true, cancelable: true }));
      canvas.dispatchEvent(new PointerEvent("pointerup", { clientX: 500, clientY: 500, pointerId: 1, button: 0, pointerType: "mouse", bubbles: true, cancelable: true }));
    });
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_close", id: 11 });
    expect(sock.sent.filter((m) => m.t === "mouse")).toHaveLength(0);

    // 3. Wheel on canvas closes; wheel inside listbox does not
    sock.receiveJson({ ...SAMPLE_SELECT, id: 12 });
    sock.sent.length = 0;
    act(() => void listbox()!.dispatchEvent(new WheelEvent("wheel", { deltaY: 40, bubbles: true, cancelable: true })));
    expect(listbox()).not.toBeNull();
    act(() => void canvas.dispatchEvent(new WheelEvent("wheel", { deltaY: 40, bubbles: true, cancelable: true })));
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_close", id: 12 });

    // 4. Panel resize
    sock.receiveJson({ ...SAMPLE_SELECT, id: 13 });
    sock.sent.length = 0;
    act(() => resizeCb?.([{ contentRect: { width: 520, height: 700 } } as ResizeObserverEntry], {} as ResizeObserver));
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_close", id: 13 });

    // 5. Desktop/Phone switch
    sock.receiveJson({ ...SAMPLE_SELECT, id: 14 });
    sock.sent.length = 0;
    const phoneBtn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.size_desktop")}"]`)!;
    act(() => phoneBtn.click());
    expect(listbox()).toBeNull();
    expect(sock.sent.filter((m) => m.t === "select_close" && m.id === 14)).toHaveLength(1);

    // 6. Address bar navigation
    sock.receiveJson({ ...SAMPLE_SELECT, id: 15 });
    sock.sent.length = 0;
    const addr = host.querySelector<HTMLInputElement>("[data-preview-address]")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(addr, "/notes");
      addr.dispatchEvent(new Event("input", { bubbles: true }));
      addr.form!.requestSubmit();
    });
    expect(listbox()).toBeNull();
    expect(sock.sent).toContainEqual({ t: "select_close", id: 15 });
    expect(sock.sent).toContainEqual({ t: "nav", url: "/notes" });
  });

  it("closes without echoing select_close on matching node select_close, replacement select_open, and disconnect", async () => {
    const { sock, keys } = await live();
    act(() => keys.focus());
    sock.receiveBinary(framePacket(1, 600, 800, PNG_BYTES));
    await flush();

    // Non-matching select_close is ignored
    sock.receiveJson({ ...SAMPLE_SELECT, id: 20 });
    sock.sent.length = 0;
    sock.receiveJson({ t: "select_close", id: 19 });
    expect(listbox()).not.toBeNull();

    // Matching select_close closes without echoing select_close
    sock.receiveJson({ t: "select_close", id: 20 });
    expect(listbox()).toBeNull();
    expect(sock.sent.filter((m) => m.t === "select_close")).toHaveLength(0);
    expect(document.activeElement).toBe(keys);

    // New select_open replaces open list without sending select_close
    sock.receiveJson({ ...SAMPLE_SELECT, id: 21 });
    sock.sent.length = 0;
    sock.receiveJson({
      t: "select_open",
      id: 22,
      rect: { x: 20, y: 80, w: 180, h: 32 },
      selected: 0,
      options: [{ index: 0, label: "Replaced option" }],
    });
    expect(options()).toHaveLength(1);
    expect(options()[0].textContent).toContain("Replaced option");
    expect(sock.sent.filter((m) => m.t === "select_close")).toHaveLength(0);

    // Disconnect closes list without sending select_close
    act(() => sock.onclose?.());
    expect(listbox()).toBeNull();
    expect(sock.sent.filter((m) => m.t === "select_close")).toHaveLength(0);
  });
});
