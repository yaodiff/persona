// @vitest-environment jsdom
// Two tabs of one browser: a theme color or a text size picked in one shows in the other at once, as the appearance does
// (views/theme-sync.test.tsx). The other tab kept the old ones: dark with the new appearance, but in the old color.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { applyLook, useAccentChoice, useTextSize } from "./look";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  applyLook();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
});

/** What another tab's choice looks like here: the stored value changed, then the storage event. */
const elsewhere = (key: string | null, value?: string) =>
  act(() => {
    if (key && value !== undefined) localStorage.setItem(key, value);
    window.dispatchEvent(new StorageEvent("storage", { key }));
  });

function Picked() {
  return <span data-accent={useAccentChoice()} data-size={useTextSize()} />;
}
const shown = () => ({ ...host.querySelector("span")!.dataset });
const page = () => ({ accent: document.documentElement.dataset.accent, size: document.documentElement.dataset.textSize });

describe("a theme color or text size picked in another tab", () => {
  it("shows here at once, and Settings here say what the page shows", () => {
    act(() => root.render(<Picked />));
    elsewhere("persona.accent", "coral");
    expect(page().accent).toBe("coral");
    expect(document.getElementById("persona-accent")!.textContent).toContain("--accent: #d2673e;");
    elsewhere("persona.text-size", "large");
    expect(page()).toEqual({ accent: "coral", size: "large" });
    expect(shown()).toEqual({ accent: "coral", size: "large" });
  });

  it("ignores other keys, and follows a cleared storage back to the defaults", () => {
    act(() => root.render(<Picked />));
    elsewhere("persona.accent", "teal");
    localStorage.setItem("persona.accent", "lavender");
    elsewhere("persona.sidebarWidth", "300");
    expect(page().accent).toBe("teal");
    localStorage.clear();
    elsewhere(null);
    expect(page()).toEqual({ accent: undefined, size: undefined });
    expect(document.getElementById("persona-accent")).toBeNull();
    expect(shown()).toEqual({ accent: "default", size: "standard" });
  });
});
