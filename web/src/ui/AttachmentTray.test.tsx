// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AttachmentTray } from "./AttachmentTray";

// An attachment's ✕ on a phone (#11442): its tap area grows out of the tile's corner (touch-hit-corner) instead of
// being centred on it, so a tap on a picture's middle opens the picture rather than removing it. A browser measures the
// real areas: scripts/test/web_mobile_input_e2e.py (tile_hits).

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

const button = (label: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;

/** touch-hit-corner's reach from styles.css, in px: out of the corner (up, right) and into the tile (left, down), with
 *  --hit-in-x / --hit-in-y as a tile sets them or else their defaults there. */
function cornerReach(css: string, inX?: number, inY?: number) {
  const rule = /@utility touch-hit-corner \{[^]*?&::before \{([^}]*)\}/.exec(css)?.[1] ?? "";
  const px = (side: string) => {
    const v = new RegExp(`\\b${side}: ([^;]+);`).exec(rule)?.[1] ?? "";
    const fallback = /var\(--hit-in-([xy]), (\d+)px\)/.exec(v);
    if (fallback) return (fallback[1] === "x" ? inX : inY) ?? Number(fallback[2]);
    return -Number(/^(-?\d+)px$/.exec(v)?.[1] ?? NaN);
  };
  return { up: px("top"), right: px("right"), left: px("left"), down: px("bottom") };
}

describe("AttachmentTray", () => {
  it("opens a picture from its middle and removes it only from its ✕", () => {
    const onOpen = vi.fn();
    const onRemove = vi.fn();
    act(() => root.render(<AttachmentTray attachments={[{ id: "a", name: "a.png", url: "blob:a" }]} onOpen={onOpen} onRemove={onRemove} />));
    act(() => button("编辑 a.png").click());
    expect(onOpen).toHaveBeenCalledWith("a");
    expect(onRemove).not.toHaveBeenCalled();
    act(() => button("移除 a.png").click());
    expect(onRemove).toHaveBeenCalledWith("a");
  });

  it("keeps a read-only picture opener's label and omits remove", () => {
    const onOpen = vi.fn();
    act(() => root.render(<AttachmentTray attachments={[{ id: "a", name: "a.png", url: "blob:a" }]} onOpen={onOpen} openLabel={(name) => `View ${name}`} />));
    act(() => button("View a.png").click());
    expect(onOpen).toHaveBeenCalledWith("a");
    expect(button("移除 a.png")).toBeNull();
  });

  it("gives the ✕ the corner's tap area, reaching further into a file's tile only", () => {
    act(() => root.render(<AttachmentTray attachments={[{ id: "a", name: "a.png", url: "blob:a" }, { id: "b", name: "b.txt" }]} onRemove={() => {}} />));
    const pic = button("移除 a.png").classList;
    expect(pic.contains("touch-hit-corner")).toBe(true);
    expect(pic.contains("touch-hit")).toBe(false);
    expect([...pic].some((c) => c.startsWith("[--hit-in"))).toBe(false);
    const file = button("移除 b.txt").classList;
    expect(file.contains("touch-hit-corner")).toBe(true);
    expect([...file]).toEqual(expect.arrayContaining(["[--hit-in-x:8px]", "[--hit-in-y:24px]"]));
  });

  it("keeps the corner's area out of a picture's middle and off the row above", () => {
    const css = readFileSync(join(__dirname, "../styles.css"), "utf8");
    const r = cornerReach(css);
    // The 52 px photo has an 18 px control at top-0.5 right-px: x 33 to 51, y 2 to 20.
    expect(33 - r.left).toBeGreaterThan(26);
    expect(20 + r.down).toBeLessThanOrEqual(26);
    // The standalone tray's 6 px row gap still contains the outward reach.
    expect(2 - r.up).toBe(-6);
    // and right as far as touch-hit's 44 px square around the ✕ does, no further (it must not make a page scroll)
    expect(r.right).toBe((44 - 18) / 2);
    // a file's ✕ (in its pr-7 column, 28 px) takes the column and reaches 44 px down from the tile's top
    const f = cornerReach(css, 8, 24);
    expect(18 + 2 + f.left).toBe(28);
    expect(20 + f.down).toBe(44);
  });

  it("highlights the active image tile and optionally preserves input focus on pointer down", () => {
    const onOpen = vi.fn();
    const onRemove = vi.fn();
    act(() =>
      root.render(
        <AttachmentTray
          attachments={[
            { id: "a", name: "a.png", url: "blob:a" },
            { id: "b", name: "b.png", url: "blob:b" },
          ]}
          activeId="b"
          keepFocus
          onOpen={onOpen}
          onRemove={onRemove}
        />,
      ),
    );
    const tileA = button("编辑 a.png");
    const tileB = button("编辑 b.png");
    expect(tileA.getAttribute("aria-pressed")).toBe("false");
    expect(tileB.getAttribute("aria-pressed")).toBe("true");
    expect(tileB.closest("[data-attachment-id]")?.classList.contains("ring-2")).toBe(true);
    expect(tileA.closest("[data-attachment-id]")?.classList.contains("ring-2")).toBe(false);

    const down = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    act(() => { tileB.dispatchEvent(down); });
    expect(down.defaultPrevented).toBe(true);
    act(() => tileB.click());
    expect(onOpen).toHaveBeenCalledWith("b");
  });
});
