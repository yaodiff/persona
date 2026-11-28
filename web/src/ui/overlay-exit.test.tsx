// @vitest-environment jsdom
// Overlays leave instead of vanishing (useExit, ui/atoms.tsx): a Dialog stays mounted, inert and showing what it
// showed, while its exit plays, and goes when that ends (or after a fallback); reduced motion and a browser without
// the Web Animations API close it at once. Toasts: a timed-out one plays its exit before it goes, the one a new toast
// pushes up glides there by transform, and the timer holds while the pointer or the focus is on a toast.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Dialog, EXIT_MS, toast, Toasts } from "./atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Fake = { el: Element; keyframes: Keyframe[]; finish: () => void; cancelled: boolean };
let made: Fake[] = [];
let reduce = false;

/** jsdom has no Element.animate: one that records each call and finishes when the test says so. */
function installAnimate() {
  (Element.prototype as { animate?: unknown }).animate = function (this: Element, keyframes: Keyframe[]) {
    let finish!: () => void;
    let fail!: (e: unknown) => void;
    const finished = new Promise<void>((res, rej) => {
      finish = res;
      fail = rej;
    });
    const fake: Fake = { el: this, keyframes, finish, cancelled: false };
    made.push(fake);
    return {
      finished,
      cancel: () => {
        fake.cancelled = true;
        fail(new DOMException("cancelled", "AbortError"));
      },
    } as unknown as Animation;
  };
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.useFakeTimers();
  made = [];
  reduce = false;
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduced-motion") ? reduce : false, addEventListener() {}, removeEventListener() {} }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => void vi.advanceTimersByTime(10_000));
  act(() => root.unmount());
  host.remove();
  delete (Element.prototype as { animate?: unknown }).animate;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const panel = () => document.body.querySelector<HTMLElement>("[role=dialog]");

function Host({ open, item }: { open: boolean; item: string | null }) {
  return (
    <Dialog open={open} onClose={() => {}} title="标题" footer={<button type="button">启动</button>}>
      {/* a caller that clears what the body reads once it is closed */}
      <p>{item ?? "空"}</p>
    </Dialog>
  );
}

describe("Dialog's exit", () => {
  it("stays, inert and as it was, while the exit plays, and goes when it ends", async () => {
    installAnimate();
    act(() => root.render(<Host open item="甲" />));
    act(() => root.render(<Host open={false} item={null} />));
    const back = panel()!.parentElement!;
    expect(back.dataset.closing).toBe("true");
    expect(back.hasAttribute("inert")).toBe(true);
    expect(panel()!.textContent).toContain("甲");
    expect(panel()!.getAttribute("aria-modal")).toBeNull(); // shortcuts work again as soon as it closes
    // the backdrop fades, the card goes back down the way it came
    expect(made.map((m) => m.el)).toEqual([back, panel()]);
    expect(made[1].keyframes.at(-1)).toMatchObject({ opacity: 0 });
    await act(async () => made.forEach((m) => m.finish()));
    expect(panel()).toBeNull();
  });

  it("goes after a fallback when the exit never ends (a hidden tab)", () => {
    installAnimate();
    act(() => root.render(<Host open item="甲" />));
    act(() => root.render(<Host open={false} item={null} />));
    act(() => void vi.advanceTimersByTime(EXIT_MS.card - 1));
    expect(panel()).not.toBeNull();
    act(() => void vi.advanceTimersByTime(200));
    expect(panel()).toBeNull();
  });

  it("opened again on its way out, stops the exit and is simply open", () => {
    installAnimate();
    act(() => root.render(<Host open item="甲" />));
    act(() => root.render(<Host open={false} item={null} />));
    act(() => root.render(<Host open item="乙" />));
    expect(made.every((m) => m.cancelled)).toBe(true);
    const back = panel()!.parentElement!;
    expect(back.dataset.closing).toBeUndefined();
    expect(back.hasAttribute("inert")).toBe(false);
    expect(panel()!.textContent).toContain("乙");
    act(() => void vi.advanceTimersByTime(1000));
    expect(panel()).not.toBeNull();
  });

  it("closes at once under reduced motion", () => {
    installAnimate();
    reduce = true;
    act(() => root.render(<Host open item="甲" />));
    act(() => root.render(<Host open={false} item={null} />));
    expect(panel()).toBeNull();
    expect(made).toHaveLength(0);
  });

  it("closes at once where there is no Web Animations API", () => {
    act(() => root.render(<Host open item="甲" />));
    act(() => root.render(<Host open={false} item={null} />));
    expect(panel()).toBeNull();
  });

  it("gives the focus back when it closes, not when the exit ends", () => {
    installAnimate();
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();
    act(() => root.render(<Host open item="甲" />));
    expect(panel()!.contains(document.activeElement)).toBe(true);
    act(() => root.render(<Host open={false} item={null} />));
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });
});

const rows = () => [...document.body.querySelectorAll<HTMLElement>("[aria-live] > div")];

describe("toasts' motion and hold", () => {
  beforeEach(() => {
    // the stack grows up from the bottom: a row's top is 46px higher for every row under it
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      let below = 0;
      for (let n = this.nextElementSibling; n; n = n.nextElementSibling) below++;
      const top = this.parentElement?.hasAttribute("aria-live") ? 500 - 46 * below : 0;
      return { top, bottom: top + 38, left: 0, right: 0, width: 0, height: 38, x: 0, y: top, toJSON() {} } as DOMRect;
    });
    act(() => root.render(<Toasts />));
  });

  it("plays its exit when it times out, and goes when that ends", async () => {
    installAnimate();
    act(() => void toast("提示一", "info"));
    act(() => void vi.advanceTimersByTime(2800));
    expect(rows()).toHaveLength(1);
    expect(rows()[0].dataset.closing).toBe("true");
    expect(rows()[0].hasAttribute("inert")).toBe(true);
    const exit = made.find((m) => m.el === rows()[0] && m.keyframes.at(-1)?.opacity === 0)!;
    expect(exit).toBeTruthy();
    await act(async () => exit.finish());
    expect(rows()).toHaveLength(0);
  });

  it("slides the one a new toast pushes up from where it was, by transform", () => {
    installAnimate();
    act(() => void toast("第一条", "info"));
    made = [];
    act(() => void toast("第二条", "info"));
    const first = rows()[0];
    const glide = made.find((m) => m.el === first);
    expect(glide?.keyframes).toEqual([{ transform: "translateY(46px)" }, { transform: "none" }]);
  });

  it("holds its time while the pointer is on it, and runs out after", () => {
    act(() => void toast("没发出去", "error"));
    act(() => void vi.advanceTimersByTime(1000));
    act(() => void rows()[0].dispatchEvent(new PointerEvent("pointerover", { bubbles: true, pointerType: "mouse" })));
    act(() => void vi.advanceTimersByTime(6000));
    expect(rows()).toHaveLength(1);
    act(() => void rows()[0].dispatchEvent(new PointerEvent("pointerout", { bubbles: true, pointerType: "mouse", relatedTarget: document.body })));
    act(() => void vi.advanceTimersByTime(4199));
    expect(rows()).toHaveLength(1);
    act(() => void vi.advanceTimersByTime(1));
    expect(rows()).toHaveLength(0);
  });

  it("is not held by a finger: a tap's compatibility mouseover has no mouseout to follow, so its time runs on", () => {
    act(() => void toast("触摸一下", "error"));
    act(() => void rows()[0].dispatchEvent(new PointerEvent("pointerover", { bubbles: true, pointerType: "touch" })));
    act(() => void rows()[0].dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    act(() => void vi.advanceTimersByTime(5199));
    expect(rows()).toHaveLength(1);
    act(() => void vi.advanceTimersByTime(1));
    expect(rows()).toHaveLength(0);
  });

  it("holds its time while the focus is in it, and a closed one lets the others run", () => {
    act(() => void toast("一", "info"));
    act(() => void toast("二", "error"));
    act(() => rows()[0].querySelector<HTMLButtonElement>("button")!.focus());
    act(() => void vi.advanceTimersByTime(8000));
    expect(rows()).toHaveLength(2);
    // closing the focused one takes its hold with it
    act(() => rows()[0].querySelector<HTMLButtonElement>("button")!.click());
    expect(rows().map((r) => r.textContent)).toEqual(["二"]);
    act(() => void vi.advanceTimersByTime(5200));
    expect(rows()).toHaveLength(0);
  });
});
