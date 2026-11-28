// @vitest-environment jsdom
// One Escape closes one overlay, the topmost: a zoomed diagram inside a Dialog, or a Dialog over another Dialog.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// jsdom has no matchMedia (useMedia reads it); the diagram is drawn already, so Mermaid itself never loads
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/mermaid", () => ({
  cachedMermaid: () => '<svg data-drawn=""></svg>',
  renderMermaid: () => Promise.resolve('<svg data-drawn=""></svg>'),
  mermaidProblem: () => ({ summary: "", detail: "" }),
}));
import { t } from "../i18n";
import { Dialog } from "./atoms";
import { MermaidBlock, MermaidViewer } from "./Mermaid";

// The zoomed view is its own chunk (Mermaid.tsx): in before the clicks, so each one draws it at once, as after a hover.
beforeAll(async () => {
  await MermaidViewer.preload();
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const escape = () => act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
const zoomed = () => document.querySelector(`[role="dialog"][aria-label="${t("ui-mermaid.diagram")}"]`);
const dialogs = () => document.querySelectorAll('[role="dialog"][aria-modal="true"]').length;

/** A Dialog the test can see closing: onClose is a spy, and it also sets open to false as a caller would. */
function Closable({ name, onClose, children }: { name: string; onClose: (name: string) => void; children?: React.ReactNode }) {
  const [open, setOpen] = useState(true);
  return (
    <Dialog open={open} onClose={() => (onClose(name), setOpen(false))} title={name}>
      {children}
    </Dialog>
  );
}

describe("Escape closes only the topmost overlay", () => {
  it("closes a zoomed diagram first and leaves the Dialog it is in open", () => {
    const closed = vi.fn();
    act(() => root.render(<Closable name="outer" onClose={closed}><MermaidBlock code="graph TD; A-->B" /></Closable>));
    act(() => document.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-mermaid.zoom")}"]`)!.click());
    expect(zoomed()).not.toBeNull();

    escape();
    expect(zoomed()).toBeNull();
    expect(closed).not.toHaveBeenCalled();

    escape();
    expect(closed).toHaveBeenCalledTimes(1);
    expect(closed).toHaveBeenCalledWith("outer");
  });

  it("closes the inner of two nested Dialogs open from the start, then the outer", () => {
    const closed = vi.fn();
    act(() => root.render(<Closable name="outer" onClose={closed}><Closable name="inner" onClose={closed} /></Closable>));
    expect(dialogs()).toBe(2);

    escape();
    expect(closed.mock.calls).toEqual([["inner"]]);

    escape();
    expect(closed.mock.calls).toEqual([["inner"], ["outer"]]);
  });

  it("closes a Dialog opened later over one already open first", () => {
    const closed = vi.fn();
    function Two() {
      const [second, setSecond] = useState(false);
      return (
        <>
          <Closable name="first" onClose={closed}>
            <button type="button" data-open-second onClick={() => setSecond(true)}>more</button>
          </Closable>
          {second && <Closable name="second" onClose={closed} />}
        </>
      );
    }
    act(() => root.render(<Two />));
    act(() => document.querySelector<HTMLButtonElement>("[data-open-second]")!.click());
    expect(dialogs()).toBe(2);

    escape();
    expect(closed.mock.calls).toEqual([["second"]]);
    escape();
    expect(closed.mock.calls).toEqual([["second"], ["first"]]);
  });

  it("uses the latest onClose a Dialog was rendered with", () => {
    const first = vi.fn();
    const latest = vi.fn();
    act(() => root.render(<Dialog open onClose={first}>a</Dialog>));
    act(() => root.render(<Dialog open onClose={latest}>a</Dialog>));
    escape();
    expect(first).not.toHaveBeenCalled();
    expect(latest).toHaveBeenCalledTimes(1);
  });

  it("does nothing once every overlay is closed", () => {
    const closed = vi.fn();
    act(() => root.render(<Dialog open={false} onClose={closed}>a</Dialog>));
    escape();
    expect(closed).not.toHaveBeenCalled();
  });

  it("ignores Escape while an IME composition is active (isComposing or keyCode 229)", () => {
    const closed = vi.fn();
    act(() => root.render(<Closable name="form" onClose={closed}><input /></Closable>));
    const input = document.querySelector("input")!;

    act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", isComposing: true, bubbles: true, cancelable: true }));
    });
    expect(closed).not.toHaveBeenCalled();

    const safari = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    Object.defineProperty(safari, "keyCode", { value: 229 });
    act(() => {
      input.dispatchEvent(safari);
    });
    expect(closed).not.toHaveBeenCalled();

    escape();
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it("respects defaultPrevented when a child inside the Dialog already consumed Escape", () => {
    const closed = vi.fn();
    const consumed = vi.fn();
    act(() =>
      root.render(
        <Closable name="form" onClose={closed}>
          <input
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                consumed();
              }
            }}
          />
        </Closable>,
      ),
    );
    const input = document.querySelector("input")!;
    act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(consumed).toHaveBeenCalledTimes(1);
    expect(closed).not.toHaveBeenCalled();
  });
});
