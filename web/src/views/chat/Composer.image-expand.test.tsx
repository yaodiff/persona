// @vitest-environment jsdom
// Expanding attached composer images above the single live Composer (#17095):
// preserves the mounted input DOM instance, draft text, focus, and caret; supports adding/switching/removing
// images while expanded; keeps /, @, + popovers and Escape precedence intact; and collapses cleanly on send/close.
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { COMPOSER_INPUT } from "../../lib/dock-focus";
import { setComposerImages } from "../../lib/composer-images";
import { sendDraft } from "../../lib/drafts";
import { COMPOSITION_TAIL_MS } from "../../lib/ime";
import { loadPages } from "../../lib/lazy-page";
import { Composer, type Outgoing } from "./Composer";
import { t } from "../../i18n";

vi.mock("../../lib/image-scale", () => ({
  fitPicture: async (file: File) => ({ mime: file.type || "image/png", data: btoa(file.name || "img") }),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let urls = 0;
const revoke = vi.fn();
let host: HTMLDivElement;
let root: Root;

beforeAll(async () => {
  Element.prototype.scrollIntoView ??= () => {};
  HTMLImageElement.prototype.decode ??= () => Promise.resolve();
  await loadPages();
});

beforeEach(() => {
  urls = 0;
  revoke.mockClear();
  URL.createObjectURL = vi.fn(() => `blob:expand/${++urls}`);
  URL.revokeObjectURL = revoke;
  localStorage.clear();
  vi.stubGlobal("matchMedia", (query: string) => ({
    media: query,
    matches: query.includes("fine"),
    addEventListener() {},
    removeEventListener() {},
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  setComposerImages("expand-test", []);
  host.remove();
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

const mount = (props: Partial<ComponentProps<typeof Composer>> = {}) =>
  act(() => root.render(<Composer draftKey="expand-test" placeholder="Ask anything" onSend={async () => true} {...props} />));

const inputEl = () => host.querySelector<HTMLTextAreaElement>(COMPOSER_INPUT)!;
const valueSetter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;

const typeText = (text: string, caret = text.length) =>
  act(() => {
    const el = inputEl();
    valueSetter.call(el, text);
    el.setSelectionRange(caret, caret);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

const attachDraft = (name: string, payload = name, draftKey = "expand-test") =>
  act(() => {
    sendDraft(draftKey, { type: "image", image: { name, mime: "image/png", data: btoa(payload) } });
  });

const tileButton = (name: string) =>
  host.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-attachmenttray.edit", { name })}"]`)!;
const removeButton = (name: string) =>
  host.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-attachmenttray.remove", { name })}"]`)!;
const stage = () => host.querySelector<HTMLElement>("[data-composer-image-stage]");
const previewImg = () => host.querySelector<HTMLImageElement>('[data-testid="composer-image-preview"]');
const counter = () => host.querySelector<HTMLElement>('[data-testid="composer-image-counter"]');

const waitUntil = (check: () => void) =>
  vi.waitFor(async () => {
    await act(async () => {
      await Promise.resolve();
    });
    check();
  });

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

describe("Composer expanded image stage", () => {
  it("reuses the exact same live Composer input instance and preserves draft, focus, and caret", async () => {
    mount();
    attachDraft("shot-1.png");
    const before = inputEl();
    act(() => before.focus());
    typeText("Check the header alignment", 9);
    expect(document.activeElement).toBe(before);
    expect([before.selectionStart, before.selectionEnd]).toEqual([9, 9]);

    // Pointer-clicking the thumbnail expands without blurring or duplicating the live input
    const tile = tileButton("shot-1.png");
    const down = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    act(() => { tile.dispatchEvent(down); });
    expect(down.defaultPrevented).toBe(true);
    act(() => tile.click());
    await flush();

    expect(stage()?.dataset.composerImageStage).toBe("open");
    expect(document.documentElement.dataset.composerStage).toBe("1");
    expect(host.querySelector("[data-composer-placeholder]")).not.toBeNull();
    expect(host.querySelector("[data-composer-image-expanded]")).not.toBeNull();
    expect(host.querySelectorAll(COMPOSER_INPUT)).toHaveLength(1);
    expect(inputEl()).toBe(before);
    expect(before.value).toBe("Check the header alignment");
    expect(document.activeElement).toBe(before);
    expect([before.selectionStart, before.selectionEnd]).toEqual([9, 9]);
    expect(previewImg()?.getAttribute("src")).toBe("blob:expand/1");
    expect(tileButton("shot-1.png").getAttribute("aria-pressed")).toBe("true");

    // Continue typing while expanded
    typeText("Check the top header alignment", 13);
    expect(inputEl()).toBe(before);

    // Clicking the enlarged image itself does not close the stage
    act(() => previewImg()!.click());
    expect(stage()).not.toBeNull();

    // Collapsing via Close button keeps the same live input, text, focus, and caret
    const closeBtn = host.querySelector<HTMLButtonElement>('[data-testid="composer-image-close"]')!;
    const closeDown = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    act(() => { closeBtn.dispatchEvent(closeDown); });
    expect(closeDown.defaultPrevented).toBe(true);
    act(() => closeBtn.click());
    await flush();

    expect(stage()).toBeNull();
    expect(host.querySelector("[data-composer-placeholder]")).toBeNull();
    expect(inputEl()).toBe(before);
    expect(before.value).toBe("Check the top header alignment");
    expect(document.activeElement).toBe(before);
    expect([before.selectionStart, before.selectionEnd]).toEqual([13, 13]);
  });

  it("supports pasting, dropping, picking, switching, and removing images while expanded", async () => {
    mount();
    attachDraft("first.png");
    act(() => tileButton("first.png").click());
    await flush();
    expect(previewImg()?.getAttribute("alt")).toBe("first.png");
    expect(counter()).toBeNull();

    // 1. Paste a second image into the live composer while expanded
    const pastedFile = new File(["pasted"], "pasted.png", { type: "image/png" });
    await act(async () => {
      const paste = new Event("paste", { bubbles: true, cancelable: true }) as ClipboardEvent;
      Object.defineProperty(paste, "clipboardData", { value: { files: [pastedFile], getData: () => "" } });
      inputEl().dispatchEvent(paste);
      await new Promise((ok) => setTimeout(ok, 0));
    });
    await waitUntil(() => expect(previewImg()?.getAttribute("alt")).toBe("pasted.png"));
    expect(stage()).not.toBeNull();
    expect(counter()?.textContent).toBe("2 / 2");
    expect(tileButton("pasted.png").getAttribute("aria-pressed")).toBe("true");

    // 2. Drag-and-drop a third image directly onto the expanded stage
    const droppedFile = new File(["dropped"], "dropped.png", { type: "image/png" });
    await act(async () => {
      const drop = new Event("drop", { bubbles: true, cancelable: true }) as DragEvent;
      Object.defineProperty(drop, "dataTransfer", {
        value: { types: ["Files"], files: [droppedFile], getData: () => "" },
      });
      stage()!.dispatchEvent(drop);
      await new Promise((ok) => setTimeout(ok, 0));
    });
    await waitUntil(() => expect(previewImg()?.getAttribute("alt")).toBe("dropped.png"));
    expect(stage()).not.toBeNull();
    expect(counter()?.textContent).toBe("3 / 3");

    // 3. Pick a fourth image through the hidden file input
    const pickedFile = new File(["picked"], "picked.png", { type: "image/png" });
    await act(async () => {
      const fileInput = host.querySelector<HTMLInputElement>('input[type="file"]')!;
      Object.defineProperty(fileInput, "files", { value: [pickedFile], configurable: true });
      fileInput.dispatchEvent(new Event("change", { bubbles: true }));
      await new Promise((ok) => setTimeout(ok, 0));
    });
    await waitUntil(() => expect(previewImg()?.getAttribute("alt")).toBe("picked.png"));
    expect(counter()?.textContent).toBe("4 / 4");

    // 4. Switch via Prev / Next controls and thumbnail click
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="composer-image-prev"]')!.click());
    expect(previewImg()?.getAttribute("alt")).toBe("dropped.png");
    expect(counter()?.textContent).toBe("3 / 4");
    act(() => tileButton("first.png").click());
    expect(previewImg()?.getAttribute("alt")).toBe("first.png");
    expect(counter()?.textContent).toBe("1 / 4");
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="composer-image-next"]')!.click());
    expect(previewImg()?.getAttribute("alt")).toBe("pasted.png");
    expect(counter()?.textContent).toBe("2 / 4");

    // 5. ArrowLeft/ArrowRight inside the textarea does not hijack image navigation; outside it does
    act(() => inputEl().focus());
    act(() => {
      inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }));
    });
    expect(previewImg()?.getAttribute("alt")).toBe("pasted.png");
    act(() => inputEl().blur());
    act(() => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }));
    });
    expect(previewImg()?.getAttribute("alt")).toBe("dropped.png");

    // 6. Removing a non-active image keeps the active image open
    act(() => removeButton("first.png").click());
    expect(previewImg()?.getAttribute("alt")).toBe("dropped.png");
    expect(counter()?.textContent).toBe("2 / 3");

    // 7. Removing the active middle image switches to the adjacent image (picked.png)
    act(() => removeButton("dropped.png").click());
    expect(previewImg()?.getAttribute("alt")).toBe("picked.png");
    expect(counter()?.textContent).toBe("2 / 2");

    // 8. Removing the active last image switches to the remaining previous image (pasted.png)
    act(() => removeButton("picked.png").click());
    expect(previewImg()?.getAttribute("alt")).toBe("pasted.png");
    expect(counter()).toBeNull();

    // 9. Removing the final image smoothly collapses back to docked composer mode
    act(() => removeButton("pasted.png").click());
    await flush();
    expect(stage()).toBeNull();
    expect(host.querySelector("[data-composer-placeholder]")).toBeNull();
  });

  it("respects Escape and backdrop precedence for /, @, +, IME, and external dialogs/menus", async () => {
    mount({
      commands: [{ word: "/compact", desc: "Compact", text: "/compact" }],
      mention: { files: () => ["/repo/src/index.ts"] },
    });
    attachDraft("ui.png");
    act(() => tileButton("ui.png").click());
    await flush();
    expect(stage()).not.toBeNull();

    // 1. Slash palette: first Esc closes only the palette, stage remains open
    act(() => inputEl().focus());
    typeText("/co");
    await waitUntil(() => expect(host.querySelector("[data-slash-palette]")).not.toBeNull());
    act(() => {
      inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(host.querySelector("[data-slash-palette]")).toBeNull();
    expect(stage()).not.toBeNull();

    // 2. Inline @ panel: first Esc closes only @ panel, stage remains open
    typeText("check @in");
    await waitUntil(() => expect(host.querySelector('[data-composer-panel="at"]')).not.toBeNull());
    act(() => {
      inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(host.querySelector('[data-composer-panel="at"]')).toBeNull();
    expect(stage()).not.toBeNull();

    // 3. IME composition (and Safari's post-compositionend tail): Esc does not close the stage
    let now = 1_000;
    const clock = vi.spyOn(performance, "now").mockImplementation(() => now);
    try {
      act(() => {
        inputEl().dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "测" }));
        inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", isComposing: true, bubbles: true, cancelable: true }));
      });
      expect(stage()).not.toBeNull();
      act(() => {
        inputEl().dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "测" }));
      });
      now += 10;
      act(() => {
        inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(stage()).not.toBeNull();
      now += COMPOSITION_TAIL_MS + 10;

      // 4. External dialog/menu: Esc yields to the outer dialog/menu and keeps stage open
      const extDialog = document.createElement("div");
      extDialog.setAttribute("role", "dialog");
      document.body.append(extDialog);
      act(() => {
        inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      });
      expect(stage()).not.toBeNull();
      extDialog.remove();

      // 5. + panel open: backdrop click closes only + panel, second backdrop click collapses stage
      act(() => host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.click());
      await waitUntil(() => expect(host.querySelector('[data-composer-panel="plus"]')).not.toBeNull());
      const backdrop = host.querySelector<HTMLElement>('[data-testid="composer-image-backdrop"]')!;
      act(() => {
        backdrop.dispatchEvent(new Event("pointerdown", { bubbles: true }));
        backdrop.click();
      });
      expect(host.querySelector('[data-composer-panel="plus"]')).toBeNull();
      expect(stage()).not.toBeNull();
      act(() => {
        backdrop.dispatchEvent(new Event("pointerdown", { bubbles: true }));
        backdrop.click();
      });
      await flush();
      expect(stage()).toBeNull();

      // 6. Clean Esc from input or stage button collapses stage before a pre-registered window listener (ChatView interrupt)
      const leaked: boolean[] = [];
      const onWinKey = (e: KeyboardEvent) => {
        if (e.key === "Escape" && !e.defaultPrevented) leaked.push(true);
      };
      window.addEventListener("keydown", onWinKey);
      try {
        act(() => tileButton("ui.png").click());
        await flush();
        expect(stage()).not.toBeNull();
        act(() => inputEl().focus());
        act(() => {
          inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        });
        await flush();
        expect(stage()).toBeNull();
        expect(leaked).toEqual([]);

        // Also test Esc when focus is on an inline stage toolbar button (outside textarea)
        act(() => tileButton("ui.png").click());
        await flush();
        const boxBtn = host.querySelector<HTMLButtonElement>('[data-testid="composer-image-tool-box"]')!;
        act(() => boxBtn.focus());
        act(() => {
          boxBtn.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        });
        await flush();
        expect(stage()).toBeNull();
        expect(leaked).toEqual([]);

        // Full Esc precedence with active inline tool & open tool sub-menu:
        // sub-menu -> composer inner overlay -> deactivate active edit tool -> collapse stage
        act(() => tileButton("ui.png").click());
        await flush();
        act(() => host.querySelector<HTMLButtonElement>('[data-testid="composer-image-tool-box"]')!.click());
        expect(stage()?.dataset.composerImageTool).toBe("box");

        // Open Line width menu -> 1st Esc closes only the menu (tool stays active, stage stays open)
        await waitUntil(() => expect(host.querySelector<HTMLButtonElement>('[data-testid="composer-image-width"]')!.disabled).toBe(false));
        const widthBtn = host.querySelector<HTMLButtonElement>('[data-testid="composer-image-width"]')!;
        act(() => widthBtn.click());
        expect(document.querySelector('[role="menu"]')).not.toBeNull();
        act(() => {
          document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        });
        expect(document.querySelector('[role="menu"]')).toBeNull();
        expect(stage()?.dataset.composerImageTool).toBe("box");
        expect(leaked).toEqual([]);

        // Composer slash palette while Box tool is active -> Esc closes only slash palette
        act(() => inputEl().focus());
        typeText("/co");
        await waitUntil(() => expect(host.querySelector("[data-slash-palette]")).not.toBeNull());
        act(() => {
          inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        });
        expect(host.querySelector("[data-slash-palette]")).toBeNull();
        expect(stage()?.dataset.composerImageTool).toBe("box");

        // Next Esc from live input deactivates Box tool (returns to preview) while keeping stage open
        act(() => {
          inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        });
        expect(stage()?.dataset.composerImageTool).toBe("preview");
        expect(stage()).not.toBeNull();
        expect(leaked).toEqual([]);

        // Final Esc collapses the expanded stage without leaking to ChatView's window listener
        act(() => {
          inputEl().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
        });
        await flush();
        expect(stage()).toBeNull();
        expect(leaked).toEqual([]);
      } finally {
        window.removeEventListener("keydown", onWinKey);
      }
    } finally {
      clock.mockRestore();
    }
  });

  it("submits text and attached images from expanded mode and collapses smoothly", async () => {
    const sent: Outgoing[] = [];
    mount({
      onSend: async (m) => {
        sent.push(m);
        return true;
      },
    });
    attachDraft("one.png", "ONE");
    attachDraft("two.png", "TWO");
    const liveInput = inputEl();
    act(() => tileButton("one.png").click());
    await flush();
    typeText("Compare both screenshots");

    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="composer-send"]')!.click();
    });
    expect(sent).toHaveLength(1);
    expect(sent[0].text).toBe("Compare both screenshots");
    expect(sent[0].images).toEqual([
      { mime: "image/png", data: btoa("ONE") },
      { mime: "image/png", data: btoa("TWO") },
    ]);
    expect(stage()).toBeNull();
    expect(document.documentElement.dataset.composerStage).toBeUndefined();
    expect(inputEl()).toBe(liveInput);
    expect(liveInput.value).toBe("");
  });
});
