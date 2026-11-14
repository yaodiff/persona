// @vitest-environment jsdom
// Esc out of a menu row's rename: the input goes, the menu stays open with its Rename row highlighted, and the focus goes
// back to what opened the menu (its button, a sidebar row), as a rename that commits gives it back. It was left on body,
// so the next key reached nothing and a screen reader lost its place; and with no row highlighted, Enter went to the
// sidebar row under the open menu and opened its conversation.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Menu } from "./atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
let row: HTMLButtonElement;

beforeEach(() => {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  const css = ((globalThis as { CSS?: Partial<typeof CSS> }).CSS ??= {});
  css.escape ??= (s: string) => s.replace(/[^\w-]/g, (c) => `\\${c}`);
  // the sidebar row the menu key opened the menu from
  row = document.createElement("button");
  row.textContent = "周末去杭州的行程";
  host = document.createElement("div");
  document.body.append(row, host);
  root = createRoot(host);
  row.focus();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  row.remove();
});

function Open({ commit = async () => {}, pin = () => {} }: { commit?: (name: string) => Promise<void>; pin?: () => void }) {
  const [open, setOpen] = useState(true);
  return open ? (
    <Menu
      anchor={row}
      onClose={() => setOpen(false)}
      items={[
        { key: "pin", label: "设为常驻", accel: "P", onSelect: pin },
        { key: "rename", label: "重命名…", accel: "R", onSelect: () => {}, rename: { value: "周末去杭州的行程", commit } },
      ]}
    />
  ) : null;
}

const key = (k: string) => {
  const e = new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true });
  act(() => void (document.activeElement ?? document.body).dispatchEvent(e));
};
const menu = () => document.querySelector("[role=menu]");
/** The row the highlight sits under: jsdom lays nothing out, so it is told by the highlight showing at all and by the
 *  keys that act on it. */
const highlighted = () => (menu()?.querySelector<HTMLElement>("span[aria-hidden]")?.style.opacity ?? "0") === "1";

describe("Esc out of a menu's rename", () => {
  it("keeps the menu open and gives the focus back to what opened it, not body", () => {
    act(() => root.render(<Open />));
    key("r");
    expect(document.activeElement?.tagName).toBe("INPUT");
    key("Escape");
    expect(document.querySelector("input")).toBeNull();
    expect(menu()).not.toBeNull();
    expect(document.activeElement).toBe(row);
    // the Rename row is back, and the next Esc is the menu's: it closes, the focus stays on the row
    expect([...menu()!.querySelectorAll("[role=menuitem]")].map((b) => b.textContent)).toContain("重命名…R");
    key("Escape");
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(row);
  });

  it("highlights the Rename row, so Enter renames again and the arrows go on from it, not the row underneath", () => {
    const pin = vi.fn();
    act(() => root.render(<Open pin={pin} />));
    expect(highlighted()).toBe(false);
    key("r");
    key("Escape");
    expect(highlighted()).toBe(true);
    // Enter is the menu's: the Rename row's field again, not the sidebar row the focus is on
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    act(() => void row.dispatchEvent(enter));
    expect(enter.defaultPrevented).toBe(true);
    expect(document.activeElement?.tagName).toBe("INPUT");
    key("Escape");
    // ↑ from the Rename row is the row above it
    key("ArrowUp");
    key("Enter");
    expect(pin).toHaveBeenCalledTimes(1);
  });

  it("does the same for Enter on the unchanged name, which renames nothing", () => {
    const commit = vi.fn(async () => {});
    act(() => root.render(<Open commit={commit} />));
    key("r");
    key("Enter");
    expect(commit).not.toHaveBeenCalled();
    expect(menu()).not.toBeNull();
    expect(document.activeElement).toBe(row);
  });
});
