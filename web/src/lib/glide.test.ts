import { describe, expect, it } from "vitest";
import { clipsOverflow, glideBox, glideStyle, sameBox } from "./glide";

const list = { top: 100, bottom: 700 };

describe("glideBox", () => {
  it("puts the box over the row, in the list's scrolled coordinates", () => {
    expect(glideBox({ top: 160, bottom: 192 }, [], list, 0)).toEqual({ top: 60, height: 32 });
    // scrolled down 40: the same row on screen is 40 further into the list
    expect(glideBox({ top: 160, bottom: 192 }, [], list, 40)).toEqual({ top: 100, height: 32 });
  });

  it("has nothing for no row or a row with no height", () => {
    expect(glideBox(null, [], list, 0)).toBeNull();
    expect(glideBox(undefined, [], list, 0)).toBeNull();
    expect(glideBox({ top: 160, bottom: 160 }, [], list, 0)).toBeNull();
  });

  it("goes away with a row folded into a closed group (#2476)", () => {
    // the Collapse closed to 0 height right above where the row would be
    expect(glideBox({ top: 300, bottom: 332 }, [{ top: 290, bottom: 290 }], list, 0)).toBeNull();
    // a sliver left at the end of the fold doesn't count
    expect(glideBox({ top: 300, bottom: 332 }, [{ top: 200, bottom: 301 }], list, 0)).toBeNull();
  });

  it("follows the clip while the group folds or opens", () => {
    // half the row is still inside the closing group
    expect(glideBox({ top: 300, bottom: 332 }, [{ top: 200, bottom: 316 }], list, 0)).toEqual({ top: 200, height: 16 });
    // nested groups: the tighter of each edge
    expect(glideBox({ top: 300, bottom: 332 }, [{ top: 280, bottom: 340 }, { top: 305, bottom: 400 }], list, 10)).toEqual({ top: 215, height: 27 });
  });

  it("leaves a row scrolled out of the list's view alone: the box scrolls along with it", () => {
    expect(glideBox({ top: 900, bottom: 932 }, [], list, 0)).toEqual({ top: 800, height: 32 });
  });

  it("tracks horizontal left/width when horizontal bounds are supplied (#17136)", () => {
    expect(glideBox({ top: 160, bottom: 192, left: 32.5, right: 242 }, [], { top: 100, bottom: 700, left: 10 }, 20)).toEqual({
      top: 80,
      height: 32,
      left: 22.5,
      width: 209.5,
    });
    expect(glideBox({ top: 160, bottom: 192, left: 32, right: 33 }, [], { top: 100, bottom: 700, left: 10 }, 0)).toBeNull();
  });
});

describe("sameBox", () => {
  it("compares placements", () => {
    expect(sameBox(null, null)).toBe(true);
    expect(sameBox({ top: 1, height: 2 }, { top: 1, height: 2 })).toBe(true);
    expect(sameBox({ top: 1, height: 2 }, { top: 1, height: 3 })).toBe(false);
    expect(sameBox({ top: 1, height: 2, left: 10, width: 200 }, { top: 1, height: 2, left: 10, width: 200 })).toBe(true);
    expect(sameBox({ top: 1, height: 2, left: 10, width: 200 }, { top: 1, height: 2, left: 12, width: 200 })).toBe(false);
    expect(sameBox({ top: 1, height: 2, left: 10, width: 200 }, { top: 1, height: 2, left: 10, width: 180 })).toBe(false);
    expect(sameBox({ top: 1, height: 2 }, null)).toBe(false);
  });
});

describe("glideStyle", () => {
  it("moves by transform and never transitions a layout property", () => {
    const s = glideStyle({ top: 120, height: 30 }, true, "ease");
    expect(s).toMatchObject({ top: 0, height: 30, transform: "translateY(120px)", opacity: 1 });
    // top / height in the transition lay the list out every frame of the glide
    expect(String(s.transition)).not.toMatch(/\b(top|height|left|width)\b/);
    expect(String(s.transition)).toContain("transform");
  });

  it("applies exact left/width and suppresses transform transition on instant reveal", () => {
    const s = glideStyle({ top: 120, height: 30, left: 22, width: 190 }, true, "ease", true);
    expect(s).toMatchObject({ top: 0, left: 22, width: 190, height: 30, transform: "translateY(120px)", opacity: 1 });
    expect(String(s.transition)).toBe("opacity 150ms ease");
  });

  it("fades out where it last was", () => {
    expect(glideStyle({ top: 40, height: 46 }, false, "ease")).toMatchObject({ transform: "translateY(40px)", height: 46, opacity: 0 });
    expect(glideStyle(null, false, "ease")).toMatchObject({ transform: "translateY(0px)", height: 0, opacity: 0 });
  });
});

describe("clipsOverflow", () => {
  it("knows which overflow clips", () => {
    expect(clipsOverflow("hidden")).toBe(true);
    expect(clipsOverflow("clip")).toBe(true);
    expect(clipsOverflow("visible")).toBe(false);
    expect(clipsOverflow("auto")).toBe(false);
  });
});
