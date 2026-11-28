import { describe, expect, it } from "vitest";
import { anchoredScroll, collapseMounted, followsAfterUnfold, followsAgain, FOLLOW_MARGIN, FOLLOW_STEP, isScrollbarPress, jumpLabel, leavesOnKey, leavesOnScrollbarDrag, leavesOnTouch, leavesOnWheel } from "./disclosure";

// A 1000 px tall list in an 800 px view at the top of the window.
const view = { clientHeight: 800, viewTop: 0 };

describe("anchoredScroll", () => {
  it("moves the offset by however far the held edge moved", () => {
    // a row above grew by 120 px while it opened: the clicked row went down, the offset follows it
    const r = anchoredScroll({ edge: "top", at: 300, follow: false }, { ...view, scrollTop: 500, scrollHeight: 3000, edgeAt: 420, elTop: 420 });
    expect(r).toEqual({ scrollTop: 620, follow: false });
    // nothing above it changed (it unfolds below the header): no scrolling at all
    expect(anchoredScroll({ edge: "top", at: 300, follow: false }, { ...view, scrollTop: 500, scrollHeight: 3000, edgeAt: 300, elTop: 300 }).scrollTop).toBe(500);
  });

  it("holds a work row's end when it folds from its 收起 button: the header comes down", () => {
    // 2000 px of trace above the end went away this frame: the end moved up 2000, the offset goes back with it
    const r = anchoredScroll({ edge: "bottom", at: 600, follow: false }, { ...view, scrollTop: 4000, scrollHeight: 9000, edgeAt: -1400, elTop: -1440 });
    expect(r.scrollTop).toBe(2000);
  });

  it("folding from inside a long trace brings the header down no further than its limit (#1486)", () => {
    const a = { edge: "bottom" as const, at: 1500, follow: false, topLimit: 400 };
    // early on the header is still far above: the end holds, the header comes down
    expect(anchoredScroll(a, { ...view, scrollTop: 4000, scrollHeight: 9000, edgeAt: 1450, elTop: -900 }).scrollTop).toBe(3950);
    // holding the end would take the header to 400 + 30: it stops at 400, and the end goes up instead
    expect(anchoredScroll(a, { ...view, scrollTop: 4000, scrollHeight: 9000, edgeAt: 1100, elTop: 30 }).scrollTop).toBe(3630);
    // a header held on screen does not move while the trace below it folds
    const held = { edge: "bottom" as const, at: 700, follow: false, topLimit: 200 };
    expect(anchoredScroll(held, { ...view, scrollTop: 4000, scrollHeight: 9000, edgeAt: 680, elTop: 200 }).scrollTop).toBe(4000);
  });

  it("cannot scroll past the ends: near the end a fold rests on the end", () => {
    const r = anchoredScroll({ edge: "top", at: 300, follow: false }, { ...view, scrollTop: 1200, scrollHeight: 1900, edgeAt: 250, elTop: 250 });
    expect(r.scrollTop).toBe(1100); // max = 1900 - 800
    expect(anchoredScroll({ edge: "top", at: 300, follow: false }, { ...view, scrollTop: 20, scrollHeight: 1900, edgeAt: 200, elTop: 200 }).scrollTop).toBe(0);
  });

  it("keeps following the end while the row stays in view", () => {
    // the end is 40 px further down this frame (an unfolding row moves ~25 px a frame)
    const r = anchoredScroll({ edge: "top", at: 500, follow: true }, { ...view, scrollTop: 1000, scrollHeight: 1840, edgeAt: 500, elTop: 500 });
    expect(r).toEqual({ scrollTop: 1040, follow: true });
  });

  it("stops following where the row would leave the top, and holds it there", () => {
    // the header near the top: pinning the end (40 px this frame) would take it to 30 - 40 = -10
    const r = anchoredScroll({ edge: "top", at: 30, follow: true }, { ...view, scrollTop: 1000, scrollHeight: 1840, edgeAt: 30, elTop: 30 });
    expect(r.follow).toBe(false);
    expect(r.scrollTop).toBe(1000 + 30 - FOLLOW_MARGIN); // the header at the margin
  });

  it("stops following when a lot lands at once: the row stays put", () => {
    // a folded trace's 650 steps arrived: the end is 2000 px further down this frame
    const r = anchoredScroll({ edge: "top", at: 250, follow: true }, { ...view, scrollTop: 1000, scrollHeight: 3800, edgeAt: 250, elTop: 250 });
    expect(r).toEqual({ scrollTop: 1000, follow: false });
    // just under the limit still follows
    expect(anchoredScroll({ edge: "top", at: 250, follow: true }, { ...view, scrollTop: 1000, scrollHeight: 1800 + FOLLOW_STEP, edgeAt: 250, elTop: 250 }).follow).toBe(true);
  });
});

describe("collapseMounted", () => {
  it("keeps a lazy collapse's content while it closes, drops it once closed", () => {
    expect(collapseMounted(true, false, false)).toBe(false);
    expect(collapseMounted(true, true, false)).toBe(true);
    expect(collapseMounted(true, false, true)).toBe(true);
    expect(collapseMounted(false, false, false)).toBe(true);
  });
});

// cindy's autoFollowIntent.test.ts, the cases that apply to a scroll view of our own
describe("following the end", () => {
  const view = { scrollHeight: 3000, clientHeight: 800 };
  const fits = { scrollHeight: 800, clientHeight: 800 };

  it("picks up only on downward movement near the end, even after passive layout shrinkage", () => {
    expect(followsAgain({ gap: 0, delta: 0 })).toBe(false);
    expect(followsAgain({ gap: 8, delta: -20 })).toBe(false);
    expect(followsAgain({ gap: 1, delta: 1 })).toBe(true);
    expect(followsAgain({ gap: 9, delta: -20 })).toBe(false);
    expect(followsAgain({ gap: 99, delta: 5 })).toBe(true);
    expect(followsAgain({ gap: 99, delta: 0 })).toBe(false);
    expect(followsAgain({ gap: 100, delta: 50 })).toBe(false);
  });

  it("the wheel going up leaves it; going down or sideways does not", () => {
    expect(leavesOnWheel({ deltaX: 0, deltaY: -1, ...view })).toBe(true);
    expect(leavesOnWheel({ deltaX: 0, deltaY: 40, ...view })).toBe(false);
    expect(leavesOnWheel({ deltaX: -30, deltaY: -10, ...view })).toBe(false);
    expect(leavesOnWheel({ deltaX: 0, deltaY: -40, ...fits })).toBe(false);
  });

  it("a finger pulling down more than 8 px leaves it", () => {
    expect(leavesOnTouch({ startY: 100, y: 108, ...view })).toBe(false);
    expect(leavesOnTouch({ startY: 100, y: 109, ...view })).toBe(true);
    expect(leavesOnTouch({ startY: 100, y: 60, ...view })).toBe(false);
  });

  it("PageUp, ↑ and Home leave it; other keys do not", () => {
    for (const key of ["PageUp", "ArrowUp", "Home"]) expect(leavesOnKey({ key, ...view })).toBe(true);
    for (const key of ["PageDown", "ArrowDown", "End", " ", "a"]) expect(leavesOnKey({ key, ...view })).toBe(false);
    expect(leavesOnKey({ key: "PageUp", ...fits })).toBe(false);
  });

  it("dragging the scroll bar up leaves it", () => {
    expect(isScrollbarPress({ targetIsView: true })).toBe(true);
    expect(isScrollbarPress({ targetIsView: false })).toBe(false);
    expect(leavesOnScrollbarDrag({ dragging: true, delta: -3 })).toBe(true);
    expect(leavesOnScrollbarDrag({ dragging: true, delta: -2 })).toBe(false);
    expect(leavesOnScrollbarDrag({ dragging: false, delta: -300 })).toBe(false);
  });

  it("the pill says 有新消息 when something came in, else 回到底部", () => {
    expect(jumpLabel(true)).toBe("有新消息");
    expect(jumpLabel(false)).toBe("回到底部");
  });
});

describe("followsAfterUnfold", () => {
  it("picks following up only when the view followed, the reader stayed put and the end is near", () => {
    expect(followsAfterUnfold({ wasAtEnd: true, readerMoved: false, gap: 40 })).toBe(true);
    // the reader wheeled up during the pause: it stays where they put it
    expect(followsAfterUnfold({ wasAtEnd: true, readerMoved: true, gap: 40 })).toBe(false);
    expect(followsAfterUnfold({ wasAtEnd: false, readerMoved: false, gap: 0 })).toBe(false);
    expect(followsAfterUnfold({ wasAtEnd: true, readerMoved: false, gap: 400 })).toBe(false);
  });
});
