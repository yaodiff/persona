// @vitest-environment jsdom
// Inline image annotation and cropping inside the expanded ComposerImageStage (#17173, #4991):
// opens in clean preview mode, activates Box / Pen / Crop in place above the exact same live Composer DOM node,
// commits non-destructively with latest-wins async encoding and flush on switch / collapse / send, isolates
// keyboard undo/redo and arrows when typing, enforces Esc precedence and touch safety, and preserves per-image
// source, edits, and undo/redo history (including edits carried in from ImageViewer).
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { COMPOSER_INPUT } from "../../lib/dock-focus";
import { setComposerImages } from "../../lib/composer-images";
import { sendDraft } from "../../lib/drafts";
import { noEdits, noHistory, record, type EditState, type ImageEdits } from "../../lib/image-edit";
import { loadPages } from "../../lib/lazy-page";
import { Composer, type Outgoing } from "./Composer";
import type { RenderEditedResult } from "./image-editor-shared";
import { t } from "../../i18n";

const encoder = vi.hoisted(() => ({
  impl: null as null | ((edits: ImageEdits, src: string, name?: string) => Promise<RenderEditedResult>),
  calls: [] as { src: string; edits: ImageEdits; name?: string }[],
}));

vi.mock("./image-editor-shared", async () => {
  const actual = await vi.importActual<typeof import("./image-editor-shared")>("./image-editor-shared");
  return {
    ...actual,
    renderEditedImage: async (
      _img: HTMLImageElement,
      src: string,
      edits: ImageEdits,
      _natural: { width: number; height: number },
      originalName?: string,
    ): Promise<RenderEditedResult> => {
      encoder.calls.push({ src, edits, name: originalName });
      if (encoder.impl) return encoder.impl(edits, src, originalName);
      const summary = `turns:${edits.turns}|marks:${edits.marks.length}|shape:${edits.shape}|crop:${edits.crop.width.toFixed(2)}`;
      return {
        ok: true,
        image: { name: originalName || "image.png", mime: "image/png", data: btoa(summary) },
      };
    },
  };
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let urls = 0;
const revoke = vi.fn();
let host: HTMLDivElement;
let root: Root;

const stubCanvasContext = () =>
  ({
    setTransform: vi.fn(),
    clearRect: vi.fn(),
    save: vi.fn(),
    restore: vi.fn(),
    beginPath: vi.fn(),
    rect: vi.fn(),
    clip: vi.fn(),
    translate: vi.fn(),
    scale: vi.fn(),
    transform: vi.fn(),
    drawImage: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    stroke: vi.fn(),
    fill: vi.fn(),
    strokeRect: vi.fn(),
    fillRect: vi.fn(),
    lineCap: "round",
    lineJoin: "round",
    strokeStyle: "",
    fillStyle: "",
    lineWidth: 1,
  }) as unknown as CanvasRenderingContext2D;

beforeAll(async () => {
  Element.prototype.scrollIntoView ??= () => {};
  HTMLImageElement.prototype.decode ??= () => Promise.resolve();
  await loadPages();
});

beforeEach(() => {
  urls = 0;
  revoke.mockClear();
  encoder.impl = null;
  encoder.calls = [];
  URL.createObjectURL = vi.fn(() => `blob:edit/${++urls}`);
  URL.revokeObjectURL = revoke;
  localStorage.clear();
  vi.stubGlobal("matchMedia", (query: string) => ({
    media: query,
    matches: query.includes("fine"),
    addEventListener() {},
    removeEventListener() {},
  }));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  vi.spyOn(HTMLImageElement.prototype, "decode").mockImplementation(function (this: HTMLImageElement) {
    Object.defineProperty(this, "naturalWidth", { configurable: true, value: 400 });
    Object.defineProperty(this, "naturalHeight", { configurable: true, value: 300 });
    return Promise.resolve();
  });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => stubCanvasContext());
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(300);
  vi.spyOn(HTMLCanvasElement.prototype, "getBoundingClientRect").mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 400,
    bottom: 300,
    width: 400,
    height: 300,
    toJSON() {},
  } as DOMRect);
  Element.prototype.setPointerCapture ??= () => {};
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  for (const key of ["inline-edit", "hub/first", "hub/second"]) setComposerImages(key, []);
  host.remove();
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const flush = async (n = 4) => {
  for (let i = 0; i < n; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const mount = (props: Partial<ComponentProps<typeof Composer>> = {}) =>
  act(() => root.render(<Composer draftKey="inline-edit" placeholder="Ask anything" onSend={async () => true} {...props} />));

const inputEl = () => host.querySelector<HTMLTextAreaElement>(COMPOSER_INPUT)!;
const valueSetter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
const typeText = (text: string, caret = text.length) =>
  act(() => {
    const el = inputEl();
    valueSetter.call(el, text);
    el.setSelectionRange(caret, caret);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

const attachDraft = (name: string, payload = name, edited?: { source: string } & EditState, draftKey = "inline-edit") =>
  act(() => {
    sendDraft(draftKey, { type: "image", image: { name, mime: "image/png", data: btoa(payload) }, edited });
  });

const tile = (name: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-attachmenttray.edit", { name })}"]`)!;
const removeBtn = (name: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-attachmenttray.remove", { name })}"]`)!;
const stage = () => host.querySelector<HTMLElement>("[data-composer-image-stage]");
const previewImg = () => host.querySelector<HTMLImageElement>('[data-testid="composer-image-preview"]');
const canvasEl = () => host.querySelector<HTMLCanvasElement>('[data-testid="composer-image-canvas"]');
const btn = (testId: string) => host.querySelector<HTMLButtonElement>(`[data-testid="${testId}"]`)!;

const expand = async (name: string) => {
  act(() => tile(name).click());
  await flush();
  expect(stage()).not.toBeNull();
};

const pointer = (el: HTMLElement, type: string, x: number, y: number, extra: PointerEventInit = {}) => {
  const EventCtor = typeof PointerEvent === "function" ? PointerEvent : MouseEvent;
  act(() => {
    el.dispatchEvent(
      new EventCtor(type, {
        clientX: x,
        clientY: y,
        button: 0,
        pointerId: 1,
        pointerType: "mouse",
        bubbles: true,
        cancelable: true,
        ...extra,
      }),
    );
  });
};

const drawStroke = (x0: number, y0: number, x1: number, y1: number) => {
  const c = canvasEl()!;
  pointer(c, "pointerdown", x0, y0);
  pointer(c, "pointermove", x1, y1);
  pointer(c, "pointerup", x1, y1);
};

const turned = (turns: number, before: EditState = { edits: noEdits(), history: noHistory() }): EditState => ({
  edits: { ...noEdits(), turns },
  history: record(before.history, before.edits),
});

describe("inline image editing inside ComposerImageStage", () => {
  it("restores the same preview and edit history on the first render after navigation", async () => {
    mount({ draftKey: "hub/first" });
    attachDraft("image.png", "original", undefined, "hub/first");
    await expand("image.png");
    act(() => btn("composer-image-tool-crop").click());
    await flush();
    act(() => btn("composer-image-rotate").click());
    await flush();
    const preview = tile("image.png").querySelector("img")!.getAttribute("src");
    expect(preview).toBe("blob:edit/2");

    act(() => root.render(null));
    mount({ draftKey: "hub/second" });
    expect(tile("image.png")).toBeNull();
    attachDraft("other.png", "other", undefined, "hub/second");
    mount({ draftKey: "hub/first" });

    expect(tile("image.png")?.querySelector("img")?.getAttribute("src")).toBe(preview);
    expect(tile("other.png")).toBeNull();
    expect(revoke).not.toHaveBeenCalled();

    await expand("image.png");
    await flush();
    expect(btn("composer-image-undo").disabled).toBe(false);
    act(() => btn("composer-image-undo").click());
    await flush();
    expect(encoder.calls.at(-1)?.src).toBe("blob:edit/1");
    expect(encoder.calls.at(-1)?.edits.turns).toBe(0);
    mount({ draftKey: "hub/second" });
    expect(tile("other.png")).not.toBeNull();
  });

  it("opens in preview mode, switches inline to Box / Pen / Crop without a modal dialog, and keeps the live Composer interactive", async () => {
    mount();
    attachDraft("shot.png", "SRC");
    const liveInput = inputEl();
    act(() => liveInput.focus());
    typeText("Before markup");

    await expand("shot.png");
    // Preview mode: no modal dialog, no canvas yet, no sub-bar yet
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(stage()?.dataset.composerImageTool).toBe("preview");
    expect(previewImg()?.getAttribute("src")).toBe("blob:edit/1");
    expect(canvasEl()).toBeNull();
    expect(host.querySelector('[data-testid="composer-image-subbar"]')).toBeNull();
    expect(btn("composer-image-undo").disabled).toBe(true);
    expect(btn("composer-image-redo").disabled).toBe(true);

    // Activate Box tool in place
    act(() => btn("composer-image-tool-box").click());
    await flush();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(stage()?.dataset.composerImageTool).toBe("box");
    expect(canvasEl()).not.toBeNull();
    expect(canvasEl()!.style.touchAction).toBe("none");
    expect(previewImg()).toBeNull();
    expect(host.querySelector('[data-testid="composer-image-subbar"]')).not.toBeNull();
    expect(host.querySelectorAll('[data-testid^="image-ink-"]')).toHaveLength(6);
    expect(btn("composer-image-width")).not.toBeNull();
    expect(btn("composer-image-clear").disabled).toBe(true);

    // Live Composer stays mounted, same DOM instance, and typable while editing
    expect(inputEl()).toBe(liveInput);
    expect(document.activeElement).toBe(liveInput);
    typeText("Before markup + live typing");
    expect(liveInput.value).toBe("Before markup + live typing");

    // Draw a Box stroke -> commits non-destructively, enables Undo & Clear, updates thumbnail while preserving source
    drawStroke(40, 40, 200, 160);
    await flush();
    expect(encoder.calls).toHaveLength(1);
    expect(encoder.calls[0].src).toBe("blob:edit/1");
    expect(encoder.calls[0].edits.marks).toHaveLength(1);
    expect(encoder.calls[0].edits.marks[0].tool).toBe("box");
    expect(encoder.calls[0].edits.marks[0].ink).toBe("red");
    expect(tile("shot.png").querySelector("img")!.getAttribute("src")).toBe("blob:edit/2");
    expect(revoke).not.toHaveBeenCalled(); // original source is never revoked
    expect(btn("composer-image-undo").disabled).toBe(false);
    expect(btn("composer-image-clear").disabled).toBe(false);

    // Switch to Pen, pick blue ink + thick width via menu, draw a second mark
    act(() => btn("composer-image-tool-pen").click());
    act(() => btn("image-ink-blue").click());
    act(() => btn("composer-image-width").click());
    const thickItem = [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')].find((el) => el.textContent?.includes(t("chat-imageeditor.width_thick")))!;
    expect(thickItem).not.toBeNull();
    act(() => thickItem.click());
    drawStroke(60, 60, 180, 140);
    await flush();
    expect(encoder.calls).toHaveLength(2);
    expect(encoder.calls[1].edits.marks).toHaveLength(2);
    expect(encoder.calls[1].edits.marks[1].tool).toBe("pen");
    expect(encoder.calls[1].edits.marks[1].ink).toBe("blue");
    expect(revoke.mock.calls.map(([u]) => u)).toEqual(["blob:edit/2"]); // replaced result revoked, source kept

    // Clear marks -> undo -> redo
    act(() => btn("composer-image-clear").click());
    await flush();
    expect(encoder.calls[2].edits.marks).toHaveLength(0);
    expect(btn("composer-image-clear").disabled).toBe(true);
    act(() => btn("composer-image-undo").click());
    await flush();
    expect(encoder.calls[3].edits.marks).toHaveLength(2);
    act(() => btn("composer-image-redo").click());
    await flush();
    expect(encoder.calls[4].edits.marks).toHaveLength(0);

    // Toggle active Pen button off -> returns cleanly to preview mode with stage still open
    act(() => btn("composer-image-tool-pen").click());
    await flush();
    expect(stage()?.dataset.composerImageTool).toBe("preview");
    expect(previewImg()?.getAttribute("src")).toBe("blob:edit/6");
    expect(canvasEl()).toBeNull();
    expect(inputEl()).toBe(liveInput);
  });

  it("supports inline Crop (rotate, aspect ratio menu, drag handle, reset) and cleans up URLs on remove", async () => {
    mount();
    attachDraft("photo.png", "ORIG");
    await expand("photo.png");

    act(() => btn("composer-image-tool-crop").click());
    await flush();
    expect(stage()?.dataset.composerImageTool).toBe("crop");
    expect(btn("composer-image-reset").disabled).toBe(true);

    // Rotate 90° left
    act(() => btn("composer-image-rotate").click());
    await flush();
    expect(encoder.calls[0].edits.turns).toBe(3);
    expect(btn("composer-image-reset").disabled).toBe(false);

    // Open aspect ratio menu and select Square
    act(() => btn("composer-image-ratio").click());
    const squareItem = [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')].find((el) => el.textContent?.includes(t("chat-imageeditor.shape_square")))!;
    expect(squareItem).not.toBeNull();
    act(() => squareItem.click());
    await flush();
    expect(encoder.calls[1].edits.shape).toBe("square");

    // Drag inside crop frame to move it -> commits one step on pointerup
    const c = canvasEl()!;
    pointer(c, "pointerdown", 200, 150);
    pointer(c, "pointermove", 220, 160);
    pointer(c, "pointerup", 220, 160);
    await flush();
    const callsAfterDrag = encoder.calls.length;
    expect(callsAfterDrag).toBeGreaterThanOrEqual(2);

    // Pointer cancel during crop drag restores without committing
    pointer(c, "pointerdown", 200, 150);
    pointer(c, "pointermove", 240, 180);
    pointer(c, "pointercancel", 240, 180);
    await flush();
    expect(encoder.calls).toHaveLength(callsAfterDrag);

    // Reset frame restores turns=0, shape="free", whole crop
    act(() => btn("composer-image-reset").click());
    await flush();
    const resetCall = encoder.calls.at(-1)!;
    expect(resetCall.edits.turns).toBe(0);
    expect(resetCall.edits.shape).toBe("free");
    expect(resetCall.edits.crop).toEqual({ x: 0, y: 0, width: 1, height: 1 });
    expect(btn("composer-image-reset").disabled).toBe(true);

    // Remove frees both rendered URL and original blob source URL
    revoke.mockClear();
    const currentUrl = tile("photo.png").querySelector("img")!.getAttribute("src")!;
    act(() => removeBtn("photo.png").click());
    expect(revoke.mock.calls.map(([u]) => u).sort()).toEqual(["blob:edit/1", currentUrl].sort());
    expect(stage()).toBeNull();
  });

  it("retains independent source, edits, and undo/redo history across multi-image switching and ImageViewer handoff", async () => {
    mount();
    const fromChat = "data:image/png;base64,Y2hhdA==";
    const viewerState = turned(1);
    attachDraft("from-viewer.png", "VIEWER_RENDER", { source: fromChat, ...viewerState });
    attachDraft("second.png", "SECOND_SRC");

    // Expand image from ImageViewer -> its prior history is immediately undoable and source is preserved
    await expand("from-viewer.png");
    await flush();
    expect(btn("composer-image-undo").disabled).toBe(false);
    act(() => btn("composer-image-tool-box").click());
    await flush();
    drawStroke(30, 30, 120, 100);
    await flush();
    expect(encoder.calls[0].src).toBe(fromChat);
    expect(encoder.calls[0].edits.turns).toBe(1);
    expect(encoder.calls[0].edits.marks).toHaveLength(1);

    // Switch to second image -> clean history, edit second image independently
    act(() => tile("second.png").click());
    await flush();
    expect(btn("composer-image-undo").disabled).toBe(true);
    act(() => btn("composer-image-tool-crop").click());
    await flush();
    act(() => btn("composer-image-rotate").click());
    await flush();
    expect(encoder.calls[1].src).toBe("blob:edit/2");
    expect(encoder.calls[1].edits.turns).toBe(3);
    expect(btn("composer-image-undo").disabled).toBe(false);

    // Switch back to first image -> exact source, edits, and 2-step undo stack restored
    act(() => tile("from-viewer.png").click());
    await flush();
    expect(btn("composer-image-undo").disabled).toBe(false);
    act(() => btn("composer-image-undo").click()); // undoes the box, back to turns=1
    await flush();
    expect(encoder.calls[2].src).toBe(fromChat);
    expect(encoder.calls[2].edits.turns).toBe(1);
    expect(encoder.calls[2].edits.marks).toHaveLength(0);
    act(() => btn("composer-image-undo").click()); // undoes viewer's turn=1, back to turns=0
    await flush();
    expect(encoder.calls[3].edits.turns).toBe(0);
    expect(btn("composer-image-undo").disabled).toBe(true);

    // Removing the ImageViewer attachment revokes only its rendered blob URL, leaving the chat's data: source alone
    revoke.mockClear();
    const viewerBlobUrl = tile("from-viewer.png").querySelector("img")!.getAttribute("src")!;
    act(() => removeBtn("from-viewer.png").click());
    expect(revoke.mock.calls.map(([u]) => u)).toEqual([viewerBlobUrl]);
  });

  it("uses latest-wins monotonic request protection and flushes in-flight encodes before send, switch, and collapse", async () => {
    const sent: Outgoing[] = [];
    mount({
      onSend: async (m) => {
        sent.push(m);
        return true;
      },
    });
    attachDraft("a.png", "A_ORIG");
    attachDraft("b.png", "B_ORIG");
    await expand("a.png");
    act(() => btn("composer-image-tool-crop").click());
    await flush();

    // 1. Monotonic latest-wins race protection: Edit #2 resolves before stale Edit #1
    const deferred: Array<{ resolve: (r: RenderEditedResult) => void; tag: string }> = [];
    encoder.impl = (edits, _src, name) =>
      new Promise<RenderEditedResult>((resolve) => {
        deferred.push({ resolve, tag: `turns:${edits.turns}:${name}` });
      });

    act(() => btn("composer-image-rotate").click()); // Edit #1 (turns=3)
    act(() => btn("composer-image-rotate").click()); // Edit #2 (turns=2)
    expect(deferred).toHaveLength(2);

    // Resolve newer Edit #2 first
    await act(async () => {
      deferred[1].resolve({ ok: true, image: { name: "a.png", mime: "image/png", data: btoa("EDIT_2_NEW") } });
      await Promise.resolve();
    });
    const urlAfterEdit2 = tile("a.png").querySelector("img")!.getAttribute("src");
    expect(urlAfterEdit2).toBe("blob:edit/3");

    // Resolve stale Edit #1 afterwards -> MUST be ignored
    await act(async () => {
      deferred[0].resolve({ ok: true, image: { name: "a.png", mime: "image/png", data: btoa("EDIT_1_STALE") } });
      await Promise.resolve();
    });
    expect(tile("a.png").querySelector("img")!.getAttribute("src")).toBe("blob:edit/3");

    // 2. Switch awaits in-flight encode before changing active image
    act(() => btn("composer-image-rotate").click()); // Edit #3 (turns=1), in-flight
    expect(deferred).toHaveLength(3);
    act(() => tile("b.png").click());
    expect(host.querySelector('[data-testid="composer-image-name"]')?.textContent).toBe("a.png"); // still waiting on flush
    await act(async () => {
      deferred[2].resolve({ ok: true, image: { name: "a.png", mime: "image/png", data: btoa("EDIT_3_FLUSHED") } });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(host.querySelector('[data-testid="composer-image-name"]')?.textContent).toBe("b.png");

    // 3. Submit awaits in-flight encode and sends freshly encoded pixels (deduplicating repeated clicks)
    act(() => btn("composer-image-rotate").click()); // Edit #4 on b.png, in-flight
    expect(deferred).toHaveLength(4);
    act(() => {
      host.querySelector<HTMLButtonElement>('[data-testid="composer-send"]')!.click();
      host.querySelector<HTMLButtonElement>('[data-testid="composer-send"]')!.click();
    });
    expect(sent).toHaveLength(0); // waiting for Edit #4 encode to flush
    await act(async () => {
      deferred[3].resolve({ ok: true, image: { name: "b.png", mime: "image/png", data: btoa("B_EDITED_FINAL") } });
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(sent).toHaveLength(1);
    expect(sent[0].images).toEqual([
      { mime: "image/png", data: btoa("EDIT_3_FLUSHED") },
      { mime: "image/png", data: btoa("B_EDITED_FINAL") },
    ]);
    expect(stage()).toBeNull();
  });

  it("flushes in-flight encode before Enter submit and before collapsing the stage", async () => {
    const sent: Outgoing[] = [];
    mount({
      onSend: async (m) => {
        sent.push(m);
        return true;
      },
    });
    attachDraft("c.png", "C_ORIG");
    await expand("c.png");
    act(() => btn("composer-image-tool-crop").click());
    await flush();

    let finishEncode!: (r: RenderEditedResult) => void;
    encoder.impl = () => new Promise<RenderEditedResult>((resolve) => { finishEncode = resolve; });

    // Collapse awaits in-flight encode
    act(() => btn("composer-image-rotate").click());
    act(() => btn("composer-image-close").click());
    expect(stage()).not.toBeNull();

    await act(async () => {
      finishEncode({ ok: true, image: { name: "c.png", mime: "image/png", data: btoa("C_FLUSHED_CLOSE") } });
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(stage()).toBeNull();
    expect(tile("c.png").querySelector("img")!.getAttribute("src")).toBe("blob:edit/2");

    // Re-expand, edit, then press Enter in live composer -> awaits in-flight encode before sending
    await expand("c.png");
    act(() => btn("composer-image-tool-crop").click());
    await flush();
    act(() => btn("composer-image-rotate").click());
    const input = inputEl();
    act(() => input.focus());
    typeText("Ship this");
    act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(sent).toHaveLength(0);

    await act(async () => {
      finishEncode({ ok: true, image: { name: "c.png", mime: "image/png", data: btoa("C_FLUSHED_ENTER") } });
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(sent).toHaveLength(1);
    expect(sent[0].text).toBe("Ship this");
    expect(sent[0].images).toEqual([{ mime: "image/png", data: btoa("C_FLUSHED_ENTER") }]);
    expect(stage()).toBeNull();
  });

  it("isolates keyboard undo/redo and arrows when live Composer input is focused, and blocks accidental touch swipe or drag-release backdrop collapse", async () => {
    mount();
    attachDraft("first.png", "1");
    attachDraft("second.png", "2");
    await expand("first.png");

    // Touch swipe switches image in preview mode
    const stageArea = host.querySelector<HTMLElement>('[data-testid="composer-image-stage-area"]')!;
    pointer(stageArea, "pointerdown", 240, 150, { pointerType: "touch" });
    pointer(stageArea, "pointerup", 120, 155, { pointerType: "touch" });
    expect(host.querySelector('[data-testid="composer-image-name"]')?.textContent).toBe("second.png");
    // Switch back to first
    act(() => tile("first.png").click());
    await flush();

    // Enter Box mode and draw a stroke
    act(() => btn("composer-image-tool-box").click());
    await flush();
    drawStroke(30, 30, 140, 120);
    await flush();
    expect(btn("composer-image-undo").disabled).toBe(false);

    // While edit tool is active, touch swipe on stage area MUST NOT switch images
    pointer(stageArea, "pointerdown", 240, 150, { pointerType: "touch" });
    pointer(stageArea, "pointerup", 100, 155, { pointerType: "touch" });
    expect(host.querySelector('[data-testid="composer-image-name"]')?.textContent).toBe("first.png");

    // Starting a stroke drag on canvas and releasing click on backdrop MUST NOT collapse stage
    const c = canvasEl()!;
    pointer(c, "pointerdown", 50, 50);
    pointer(c, "pointermove", 110, 90);
    pointer(c, "pointerup", 110, 90);
    const backdrop = host.querySelector<HTMLElement>('[data-testid="composer-image-backdrop"]')!;
    act(() => backdrop.click());
    expect(stage()).not.toBeNull();

    // Focus live Composer textarea -> Cmd/Ctrl+Z, Shift+Z, Ctrl+Y, and Arrow keys belong to text editing
    const input = inputEl();
    act(() => input.focus());
    const callsBefore = encoder.calls.length;
    for (const init of [
      { key: "z", metaKey: true },
      { key: "z", ctrlKey: true },
      { key: "z", metaKey: true, shiftKey: true },
      { key: "y", ctrlKey: true },
      { key: "ArrowRight" },
      { key: "ArrowLeft" },
    ]) {
      const ev = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
      act(() => {
        input.dispatchEvent(ev);
      });
      expect(ev.defaultPrevented, JSON.stringify(init)).toBe(false);
    }
    expect(encoder.calls).toHaveLength(callsBefore);
    expect(host.querySelector('[data-testid="composer-image-name"]')?.textContent).toBe("first.png");

    // Toolbar Undo button still works on click while textarea keeps focus
    const undoBtn = btn("composer-image-undo");
    const down = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    act(() => {
      undoBtn.dispatchEvent(down);
    });
    expect(down.defaultPrevented).toBe(true);
    act(() => undoBtn.click());
    await flush();
    expect(encoder.calls).toHaveLength(callsBefore + 1);
    expect(document.activeElement).toBe(input);

    // Outside editable inputs -> Cmd+Z / Cmd+Shift+Z / Ctrl+Y trigger image undo/redo and ArrowRight switches image
    act(() => input.blur());
    const redoEv = new KeyboardEvent("keydown", { key: "z", metaKey: true, shiftKey: true, bubbles: true, cancelable: true });
    act(() => {
      document.body.dispatchEvent(redoEv);
    });
    await flush();
    expect(redoEv.defaultPrevented).toBe(true);
    expect(encoder.calls).toHaveLength(callsBefore + 2);

    const undoEv = new KeyboardEvent("keydown", { key: "z", ctrlKey: true, bubbles: true, cancelable: true });
    act(() => {
      document.body.dispatchEvent(undoEv);
    });
    await flush();
    expect(undoEv.defaultPrevented).toBe(true);
    expect(encoder.calls).toHaveLength(callsBefore + 3);

    const ctrlYEv = new KeyboardEvent("keydown", { key: "y", ctrlKey: true, bubbles: true, cancelable: true });
    act(() => {
      document.body.dispatchEvent(ctrlYEv);
    });
    await flush();
    expect(ctrlYEv.defaultPrevented).toBe(true);
    expect(encoder.calls).toHaveLength(callsBefore + 4);

    const arrowEv = new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true });
    act(() => {
      document.body.dispatchEvent(arrowEv);
    });
    expect(arrowEv.defaultPrevented).toBe(true);
    expect(host.querySelector('[data-testid="composer-image-name"]')?.textContent).toBe("second.png");
  });
});
