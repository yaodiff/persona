// @vitest-environment jsdom
// The composer grows to six lines; native scrolling and the existing phone-only expanded editor stay intact.
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { onOverlayEntry } from "../../lib/back-close";
import { loadPages } from "../../lib/lazy-page";
import { Composer } from "./Composer";
import { COMPOSER_LINES, lineCount, offersExpand } from "./composer-grow";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("composer line cap", () => {
  it("uses one six-line token, independent of the visible viewport", () => {
    expect(COMPOSER_LINES).toBe(6);
  });
  it("counts wrapped lines and offers Expand only past the limit", () => {
    expect(lineCount(72, 24)).toBe(3);
    expect(lineCount(0, 24)).toBe(1);
    expect(offersExpand(3, 3)).toBe(false);
    expect(offersExpand(4, 3)).toBe(true);
  });
});

// jsdom lays nothing out: a line per "\n", 24px each (the computed line height falls back to 24 there too)
const scroll = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
let phone = true;
// the narrow layout, where Back closes overlays first (off unless a test turns it on)
let narrow = false;
let host: HTMLDivElement;
let root: Root;

beforeAll(async () => {
  await loadPages();
});

beforeEach(() => {
  phone = true;
  narrow = false;
  localStorage.clear();
  Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLTextAreaElement) { return this.value.split("\n").length * 24; },
  });
  vi.stubGlobal("matchMedia", (q: string) => ({
    media: q,
    get matches() { return q.includes("max-width: 639px") ? phone : q === "(max-width: 1023px)" && narrow; },
    addEventListener() {},
    removeEventListener() {},
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  if (scroll) Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", scroll);
  else delete (HTMLTextAreaElement.prototype as { scrollHeight?: number }).scrollHeight;
  vi.unstubAllGlobals();
});

const lines = (n: number) => Array.from({ length: n }, (_, i) => `step ${i + 1}`).join("\n");
const box = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
const type = (value: string) =>
  act(() => {
    const el = box();
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const expandButton = () => host.querySelector<HTMLButtonElement>('[data-testid="composer-expand"]');
const editor = () => document.querySelector<HTMLElement>('[data-testid="composer-expanded"]');

describe("the phone composer", () => {
  it("grows without a button, offers Expand past the limit, and keeps the draft and caret across the full editor", () => {
    act(() => root.render(<Composer draftKey="k1" placeholder="" onSend={async () => true} />));
    // jsdom's window is 768px tall: the shared six-line cap.
    type(lines(4));
    expect(box().style.height).toBe(`${4 * 24}px`);
    expect(expandButton()).toBeNull();
    expect(box().className).not.toContain("pr-8");
    expect(box().style.maxHeight).toBe(`min(${6 * 24}px, min(240px, calc(var(--kb-h) * 0.35)))`);
    type(lines(6));
    expect(box().style.height).toBe(`${6 * 24}px`);
    expect(expandButton()).toBeNull();

    type(lines(7));
    expect(expandButton()).not.toBeNull();
    type(lines(9));
    expect(expandButton()).not.toBeNull();
    expect(box().className).toContain("pr-8");

    box().setSelectionRange(5, 9);
    act(() => expandButton()!.click());
    expect(editor()!.parentElement).toBe(document.body);
    expect(host.contains(editor())).toBe(false);
    const big = editor()!.querySelector("textarea")!;
    expect(big.value).toBe(lines(9));
    expect([big.selectionStart, big.selectionEnd]).toEqual([5, 9]);
    expect(document.activeElement).toBe(big);

    big.setSelectionRange(12, 12);
    act(() => document.querySelector<HTMLButtonElement>('[data-testid="composer-collapse"]')!.click());
    expect(editor()).toBeNull();
    expect(box().value).toBe(lines(9));
    expect([box().selectionStart, box().selectionEnd]).toEqual([12, 12]);
    expect(document.activeElement).toBe(box());
    expect(expandButton()).not.toBeNull();

    type(lines(2));
    expect(expandButton()).toBeNull();
  });

  it("sends from the full editor and closes it", async () => {
    const sent: string[] = [];
    act(() => root.render(<Composer draftKey="k2" placeholder="" onSend={async (m) => (sent.push(m.text), true)} />));
    type(lines(9));
    act(() => expandButton()!.click());
    await act(async () => editor()!.querySelector<HTMLButtonElement>('[data-testid="composer-expanded-send"]')!.click());
    expect(sent).toEqual([lines(9)]);
    expect(editor()).toBeNull();
  });

  it("closes on Back as its collapse button does, staying on the page; collapsed, it takes its entry back off (#11442)", async () => {
    narrow = true;
    const at = location.href;
    act(() => root.render(<Composer draftKey="k-back" placeholder="" onSend={async () => true} />));
    type(lines(9));
    act(() => expandButton()!.click());
    expect(onOverlayEntry()).toBe(true);
    editor()!.querySelector("textarea")!.setSelectionRange(12, 12);
    await act(async () => {
      const popped = new Promise((r) => addEventListener("popstate", r, { once: true }));
      history.back();
      await popped;
    });
    expect(editor()).toBeNull();
    expect(location.href).toBe(at);
    expect(onOverlayEntry()).toBe(false);
    expect(box().value).toBe(lines(9));
    expect([box().selectionStart, box().selectionEnd]).toEqual([12, 12]);

    act(() => expandButton()!.click());
    expect(onOverlayEntry()).toBe(true);
    act(() => document.querySelector<HTMLButtonElement>('[data-testid="composer-collapse"]')!.click());
    expect(editor()).toBeNull();
    // back-close's timer and the Back it causes can finish after a fixed sleep under load.
    await act(async () => vi.waitFor(() => expect(onOverlayEntry()).toBe(false)));
    expect(onOverlayEntry()).toBe(false);
    expect(location.href).toBe(at);
  });

  it("uses the same six-line cap on desktop without offering the phone editor", () => {
    phone = false;
    act(() => root.render(<Composer draftKey="k3" placeholder="" onSend={async () => true} />));
    type(lines(12));
    expect(expandButton()).toBeNull();
    expect(box().style.maxHeight).toBe(`min(${6 * 24}px, min(240px, calc(var(--kb-h) * 0.35)))`);
  });
});

describe("the expanded composer keyboard", () => {
  const open = (onSend: (m: { text: string; images: { mime: string; data: string }[] }) => Promise<boolean>, blocked?: ComponentProps<typeof Composer>["blocked"]) => {
    act(() => root.render(<Composer draftKey="expanded-keyboard" placeholder="" onSend={onSend} blocked={blocked} />));
    type(lines(9));
    act(() => expandButton()!.click());
    const field = editor()!.querySelector<HTMLTextAreaElement>("textarea")!;
    field.setSelectionRange(field.value.length, field.value.length);
    return field;
  };
  const press = async (field: HTMLTextAreaElement, extra: KeyboardEventInit = {}, nativeNewline = false) => {
    const event = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...extra });
    await act(async () => {
      field.dispatchEvent(event);
      await Promise.resolve();
    });
    if (!event.defaultPrevented && nativeNewline) {
      // jsdom has no native textarea default action; carry out the newline the browser would insert.
      const start = field.selectionStart, end = field.selectionEnd;
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, `${field.value.slice(0, start)}\n${field.value.slice(end)}`);
        field.setSelectionRange(start + 1, start + 1);
        field.dispatchEvent(new Event("input", { bubbles: true }));
      });
    }
    return event;
  };

  it.each([
    { name: "Ctrl", mods: { ctrlKey: true } },
    { name: "Cmd", mods: { metaKey: true } },
  ])("sends on $name+Enter, keeps plain Enter as a newline, and leaves Mod+Alt/Shift+Enter alone", async ({ mods }) => {
    const sent: string[] = [];
    const field = open(async (m) => (sent.push(m.text), true));
    const at = field.value.indexOf("step 5");
    field.setSelectionRange(at, at);
    const withNewline = lines(9).replace("\nstep 5", "\n\nstep 5");
    expect((await press(field, {}, true)).defaultPrevented).toBe(false);
    expect(field.value).toBe(withNewline);
    expect(sent).toEqual([]);
    for (const extra of [{ altKey: true }, { shiftKey: true }]) {
      expect((await press(field, { ...mods, ...extra })).defaultPrevented).toBe(false);
      expect(sent).toEqual([]);
      expect(editor()).not.toBeNull();
    }
    expect((await press(field, mods)).defaultPrevented).toBe(true);
    expect(sent).toEqual([withNewline]);
    expect(editor()).toBeNull();
  });

  it("sends nothing during IME composition, including keyCode 229", async () => {
    const onSend = vi.fn(async () => true);
    const field = open(onSend);
    for (const extra of [{ ctrlKey: true, isComposing: true }, { metaKey: true, keyCode: 229 }, { isComposing: true }, { keyCode: 229 }]) {
      expect((await press(field, extra)).defaultPrevented).toBe(false);
    }
    expect(field.value).toBe(lines(9));
    expect(onSend).not.toHaveBeenCalled();
    expect(editor()).not.toBeNull();
  });

  it("returns a blocked Mod+Enter to the existing nudge without losing the draft", async () => {
    const onNudge = vi.fn(), onSend = vi.fn(async () => true);
    const field = open(onSend, { label: "Finish setup", onNudge });
    expect((await press(field, { ctrlKey: true })).defaultPrevented).toBe(true);
    expect(editor()).toBeNull();
    expect(box().value).toBe(lines(9));
    expect(host.querySelector('[data-testid="composer-nudge"]')).not.toBeNull();
    expect(onNudge).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("keeps an empty, unblocked draft open with its caret on Mod+Enter", async () => {
    const onSend = vi.fn(async () => true);
    const field = open(onSend);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, "");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    field.setSelectionRange(0, 0);
    expect((await press(field, { ctrlKey: true })).defaultPrevented).toBe(true);
    expect(editor()).not.toBeNull();
    expect(onSend).not.toHaveBeenCalled();
    expect(field.value).toBe("");
    expect([field.selectionStart, field.selectionEnd]).toEqual([0, 0]);
  });
});

describe("the expanded composer chunk", () => {
  it("opens on the first tap after a late chunk, keeping the draft and caret", async () => {
    const chunk = { calls: 0, open: () => {} };
    vi.resetModules();
    vi.doMock("./ComposerOverlays", async () => {
      chunk.calls++;
      await new Promise<void>((resolve) => { chunk.open = resolve; });
      return vi.importActual<typeof import("./ComposerOverlays")>("./ComposerOverlays");
    });
    try {
      const { initI18n } = await import("../../i18n");
      await initI18n("zh-Hans");
      const { Composer: DeferredComposer } = await import("./Composer");
      await act(async () => {
        root.render(<DeferredComposer draftKey="deferred-expand" placeholder="" onSend={async () => true} />);
      });
      type(lines(9));
      box().setSelectionRange(5, 9);
      const control = expandButton()!;
      act(() => control.dispatchEvent(new PointerEvent("pointerdown", { pointerType: "mouse", bubbles: true })));
      await vi.waitFor(() => expect(chunk.calls).toBe(1));
      expect(editor()).toBeNull();

      await act(async () => { control.click(); });
      expect(editor()).toBeNull();
      await act(async () => { chunk.open(); });
      await vi.waitFor(async () => {
        await act(async () => { await Promise.resolve(); });
        expect(editor()).not.toBeNull();
      }, { timeout: 10_000 });
      const field = editor()!.querySelector<HTMLTextAreaElement>("textarea")!;
      expect(field.value).toBe(lines(9));
      expect([field.selectionStart, field.selectionEnd]).toEqual([5, 9]);
    } finally {
      vi.doUnmock("./ComposerOverlays");
    }
  }, 20_000);
});
