import { describe, expect, it } from "vitest";
import { placeMenu, placeSubmenu, trapTab } from "./overlay";

describe("Tab inside a dialog", () => {
  it("wraps from the last control to the first and back", () => {
    expect(trapTab(3, 2, false)).toBe(0);
    expect(trapTab(3, 0, true)).toBe(2);
  });
  it("leaves the browser's order alone between two controls", () => {
    expect(trapTab(3, 0, false)).toBeNull();
    expect(trapTab(3, 1, true)).toBeNull();
  });
  it("brings a focus that is on the dialog itself (or got out) to its first or last control", () => {
    expect(trapTab(3, -1, false)).toBe(0);
    expect(trapTab(3, -1, true)).toBe(2);
  });
  it("keeps it on the dialog when there is nothing to land on", () => {
    expect(trapTab(0, -1, false)).toBe(-1);
  });
  // Chrome lets the keyboard focus a scrolling body with no control inside (the shortcut help's list): Tab went from it
  // back to the first control, so the footer's button after it was never reached going forward
  it("goes on in the browser's order from something inside that is no control, wrapping only past either end", () => {
    // Close, the body, Edit in Settings: the body sits before control 1
    expect(trapTab(2, -1, false, 1)).toBeNull(); // on to Edit in Settings
    expect(trapTab(2, -1, true, 1)).toBeNull(); // back to Close
    // a body after every control: Tab wraps to the first, Shift+Tab goes back as usual
    expect(trapTab(2, -1, false, 2)).toBe(0);
    expect(trapTab(2, -1, true, 2)).toBeNull();
    // one before every control: Shift+Tab wraps to the last
    expect(trapTab(2, -1, true, 0)).toBe(1);
  });
});

const wide = { width: 208, align: "left" as const, side: "bottom" as const, screenW: 1280, screenH: 800 };

describe("menu placement", () => {
  it("opens under the anchor on a wide screen, as before", () => {
    expect(placeMenu({ left: 100, right: 130, top: 50, bottom: 80 }, 200, wide)).toEqual({ left: 100, top: 86, width: 208 });
  });
  it("flips above when there is no room below, and never above the window's top", () => {
    expect(placeMenu({ left: 100, right: 130, top: 700, bottom: 730 }, 200, wide).top).toBe(494);
    expect(placeMenu({ left: 100, right: 130, top: 140, bottom: 160 }, 290, { ...wide, screenH: 300 }).top).toBe(8);
  });
  it("shows a menu the window can hold whole, over its button when neither side has room for it", () => {
    const beside = { ...wide, beside: true };
    // a 700px menu from a button low in an 800px window (784px to use): moved up to fit, every row shows
    expect(placeMenu({ left: 100, right: 130, top: 600, bottom: 630 }, 700, beside)).toEqual({ left: 100, top: 8, width: 208 });
    // a right-click menu of 500px at y = 450 in a 1280 × 820 window: whole from the top margin, not cut to the 442px above
    expect(placeMenu({ left: 300, right: 300, top: 450, bottom: 450 }, 500, { ...beside, screenH: 820, gap: 0 })).toEqual({ left: 300, top: 8, width: 208 });
    // the session's ⋯ (about 610px) from the middle of a 1440 × 900 window: whole
    expect(placeMenu({ left: 700, right: 726, top: 430, bottom: 456 }, 610, { ...beside, screenW: 1440, screenH: 900 })).toEqual({ left: 700, top: 8, width: 208 });
  });
  it("opens a menu taller than the window on the roomier side of its button, as tall as that room, not over it", () => {
    const beside = { ...wide, beside: true };
    // the session header's ⋯ in a short window: under it, scrolling inside
    expect(placeMenu({ left: 470, right: 496, top: 14, bottom: 40 }, 610, { ...beside, screenH: 560 })).toEqual({ left: 470, top: 46, width: 208, maxHeight: 506 });
    // a button low on the page: above it, from the window's top margin
    expect(placeMenu({ left: 100, right: 130, top: 400, bottom: 430 }, 700, { ...beside, screenH: 600 })).toEqual({ left: 100, top: 8, width: 208, maxHeight: 386 });
    // with little room on both sides it opens over the button, as tall as the window allows
    expect(placeMenu({ left: 100, right: 130, top: 150, bottom: 170 }, 330, { ...beside, screenH: 320 })).toEqual({ left: 100, top: 8, width: 208 });
    // a card that does not scroll (the turn's cost, an info tip), or a menu on a phone, is placed as before: over the button
    expect(placeMenu({ left: 470, right: 496, top: 14, bottom: 40 }, 610, { ...wide, screenH: 560 })).toEqual({ left: 470, top: 8, width: 208 });
  });
  it("keeps to the screen's width and the right edge for a right-aligned one", () => {
    expect(placeMenu({ left: 300, right: 380, top: 0, bottom: 30 }, 100, { ...wide, align: "right", screenW: 390 })).toMatchObject({ left: 172, width: 208 });
    expect(placeMenu({ left: 0, right: 30, top: 0, bottom: 30 }, 100, { ...wide, screenW: 200 }).width).toBe(184);
  });
  it("on a phone keeps above the keyboard: inside the visible viewport, capped to its height", () => {
    // the keyboard leaves the top 400px; the anchor is near its bottom
    const p = placeMenu({ left: 10, right: 40, top: 360, bottom: 390 }, 300, { ...wide, screenW: 390, screenH: 844, view: { top: 0, height: 400 } });
    expect(p.maxHeight).toBe(384);
    expect(p.top).toBe(54); // above the anchor: 360 - 300 - 6
    const tall = placeMenu({ left: 10, right: 40, top: 200, bottom: 230 }, 600, { ...wide, screenW: 390, screenH: 844, view: { top: 0, height: 400 } });
    expect(tall.top).toBe(8);
    expect(tall.top + Math.min(600, tall.maxHeight!)).toBeLessThanOrEqual(392);
  });
  it("follows the visible viewport when iOS scrolled it down (offsetTop)", () => {
    const p = placeMenu({ left: 10, right: 40, top: 310, bottom: 340 }, 100, { ...wide, screenW: 390, screenH: 844, view: { top: 300, height: 380 } });
    expect(p.top).toBe(346);
    const up = placeMenu({ left: 10, right: 40, top: 600, bottom: 630 }, 100, { ...wide, side: "top", screenW: 390, screenH: 844, view: { top: 300, height: 300 } });
    expect(up.top).toBeGreaterThanOrEqual(308);
    expect(up.top + 100).toBeLessThanOrEqual(592);
  });
});

describe("menu at the cursor", () => {
  const point = { left: 300, right: 300, top: 200, bottom: 200 };
  it("opens at the pointer, with no gap", () => {
    expect(placeMenu(point, 160, { ...wide, gap: 0 })).toEqual({ left: 300, top: 200, width: 208 });
  });
  it("flips above the pointer near the bottom and stays inside on the right", () => {
    const low = { left: 1200, right: 1200, top: 700, bottom: 700 };
    expect(placeMenu(low, 160, { ...wide, gap: 0 })).toEqual({ left: 1064, top: 540, width: 208 });
  });
});

describe("submenu placement", () => {
  const row = { left: 100, right: 308, top: 120, bottom: 152 };
  const o = { width: 208, screenW: 1280, screenH: 800 };
  it("opens beside the row, lined up with it and overlapping the parent a little", () => {
    expect(placeSubmenu(row, 120, o)).toEqual({ left: 304, top: 116, width: 208 });
  });
  it("opens on the left when the right has no room", () => {
    const edge = { left: 1000, right: 1208, top: 120, bottom: 152 };
    expect(placeSubmenu(edge, 120, o).left).toBe(796);
  });
  it("slides up to stay on screen and never above the top", () => {
    expect(placeSubmenu({ ...row, top: 700, bottom: 732 }, 200, o).top).toBe(592);
    expect(placeSubmenu({ ...row, top: 0, bottom: 32 }, 200, o).top).toBe(8);
  });
  it("keeps to what the keyboard leaves on a phone", () => {
    const view = { top: 0, height: 400 };
    expect(placeSubmenu({ ...row, top: 300, bottom: 332 }, 300, { ...o, view })).toEqual({ left: 304, top: 92, width: 208, maxHeight: 384 });
  });
});
