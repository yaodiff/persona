// @vitest-environment jsdom
// The page's keys field on a touch screen (#11511): it is a real editable, so focusing it raises a phone's keyboard,
// which shrinks the panel and the page. A finger's scroll, a pick and Go leave it alone; a tap focuses it once the finger
// lifts; a mouse focuses it on the press, as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const pointer = vi.hoisted(() => ({ fine: false }));
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

/** The preview's socket: the test opens it and reads what the panel sent. */
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
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  pointer.fine = false;
  FakeSocket.last = null;
  sessionStorage.clear();
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("ResizeObserver", class {
    constructor(private cb: ResizeObserverCallback) {}
    observe() {
      this.cb([{ contentRect: { width: 390, height: 600 } } as ResizeObserverEntry], this as unknown as ResizeObserver);
    }
    unobserve() {}
    disconnect() {}
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
};

/** Opens the panel on the running server, with its socket open. */
async function live() {
  act(() => root.render(<PreviewPanel mid="m1" sid="s1" cwd="/project" port={5173} onClose={() => {}} />));
  await flush();
  const sock = FakeSocket.last!;
  expect(sock).not.toBeNull();
  act(() => {
    sock.readyState = FakeSocket.OPEN;
    sock.onopen?.();
  });
  const canvas = host.querySelector<HTMLCanvasElement>("[data-preview-canvas]")!;
  canvas.setPointerCapture = () => {};
  const keys = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${t("preview.keyboard_label")}"]`)!;
  return { sock, canvas, keys };
}

function press(target: Element, type: string, y: number, pointerType: string) {
  act(() => void target.dispatchEvent(new PointerEvent(type, { clientX: 100, clientY: y, pointerId: 1, button: 0, pointerType, bubbles: true, cancelable: true })));
}
const mouseDowns = (sock: FakeSocket) => sock.sent.filter((m) => m.t === "mouse" && m.type === "down");
const pickButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("preview.pick")}"]`)!;

/** Types an address and submits it, the way Go does. */
function go(url: string) {
  const field = host.querySelector<HTMLInputElement>("[data-preview-address]")!;
  act(() => field.focus());
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, url);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
  act(() => void field.form!.requestSubmit());
  return field;
}

describe("PreviewPanel's keys field and a touch screen", () => {
  it("leaves it alone when a finger scrolls the page", async () => {
    const { sock, canvas, keys } = await live();
    press(canvas, "pointerdown", 300, "touch");
    expect(document.activeElement).not.toBe(keys);
    press(canvas, "pointermove", 280, "touch");
    press(canvas, "pointerup", 280, "touch");

    expect(document.activeElement).not.toBe(keys);
    expect(sock.sent.some((m) => m.t === "wheel")).toBe(true);
    expect(mouseDowns(sock)).toHaveLength(0);
  });

  it("focuses it once a tap lifts, and clicks the page", async () => {
    const { sock, canvas, keys } = await live();
    press(canvas, "pointerdown", 300, "touch");
    expect(document.activeElement).not.toBe(keys);
    press(canvas, "pointerup", 300, "touch");

    expect(document.activeElement).toBe(keys);
    expect(mouseDowns(sock)).toHaveLength(1);
  });

  it("leaves it alone for a tap that picks an element", async () => {
    const { sock, canvas, keys } = await live();
    act(() => pickButton().click());
    expect(sock.sent).toContainEqual({ t: "pick", on: true });
    expect(document.activeElement).not.toBe(keys);

    press(canvas, "pointerdown", 300, "touch");
    press(canvas, "pointerup", 300, "touch");
    expect(mouseDowns(sock)).toHaveLength(1);
    expect(document.activeElement).not.toBe(keys);
  });

  it("puts the address field's keyboard away after Go, without focusing it", async () => {
    const { sock, keys } = await live();
    const field = go("/settings");

    expect(sock.sent).toContainEqual({ t: "nav", url: "/settings" });
    expect(document.activeElement).not.toBe(keys);
    expect(document.activeElement).not.toBe(field);
  });
});

describe("PreviewPanel's keys field with a mouse", () => {
  it("focuses it on the press", async () => {
    pointer.fine = true;
    const { sock, canvas, keys } = await live();
    press(canvas, "pointerdown", 300, "mouse");

    expect(document.activeElement).toBe(keys);
    expect(mouseDowns(sock)).toHaveLength(1);
  });

  it("focuses it for Pick (Esc cancels there) and after Go (typing goes to the page)", async () => {
    pointer.fine = true;
    const { keys } = await live();
    act(() => pickButton().click());
    expect(document.activeElement).toBe(keys);
    act(() => keys.blur());

    go("/settings");
    expect(document.activeElement).toBe(keys);
  });

  it("guards prompt Enter/Escape and address-bar Escape against IME and cancels prompt on real Escape", async () => {
    const { sock } = await live();
    const outerKeyDown = vi.fn();
    window.addEventListener("keydown", outerKeyDown);
    try {
      // Address bar Safari IME Escape (keyCode 229) keeps focus on address input.
      const addr = host.querySelector<HTMLInputElement>("[data-preview-address]")!;
      act(() => { addr.focus(); });
      const imeAddrEsc = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
      Object.defineProperty(imeAddrEsc, "keyCode", { value: 229 });
      act(() => { addr.dispatchEvent(imeAddrEsc); });
      expect(document.activeElement).toBe(addr);

      // Show a window.prompt() dialog bar from the preview socket.
      act(() => sock.onmessage?.({ data: JSON.stringify({ t: "dialog", kind: "prompt", message: "Your name?" }) }));
      const input = host.querySelector<HTMLInputElement>('[data-preview-bar="dialog"] input')!;
      expect(input).toBeTruthy();

      // IME Enter (isComposing / Safari keyCode 229) must NOT submit the prompt.
      act(() => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "draft");
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      const imeEnter1 = new KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true, cancelable: true });
      const imeEnter2 = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
      Object.defineProperty(imeEnter2, "keyCode", { value: 229 });
      act(() => { input.dispatchEvent(imeEnter1); input.dispatchEvent(imeEnter2); });
      expect(sock.sent.filter((m) => m.t === "dialog")).toEqual([]);

      // Real Escape cancels the prompt and stops propagation so enclosing SidePanel stays open.
      outerKeyDown.mockClear();
      const esc = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
      act(() => { input.dispatchEvent(esc); });
      expect(esc.defaultPrevented).toBe(true);
      expect(sock.sent.filter((m) => m.t === "dialog")).toEqual([{ t: "dialog", accept: false, text: "" }]);
      expect(outerKeyDown).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", outerKeyDown);
    }
  });
});
