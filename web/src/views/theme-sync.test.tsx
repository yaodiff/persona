// @vitest-environment jsdom
// Two tabs of the same browser: the theme picked in one shows in the other. The other tab stayed light until it was
// reloaded, and its Settings said 深色 over a light page (they read the stored choice). The language: lib/language-sync.test.ts.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { mediaListeners } = vi.hoisted(() => {
  const mediaListeners = new Set<() => void>();
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener(_type: string, cb: () => void) { mediaListeners.add(cb); },
    removeEventListener(_type: string, cb: () => void) { mediaListeners.delete(cb); },
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
  return { mediaListeners };
});
import { setTheme, useTheme } from "./theme";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  document.documentElement.classList.remove("dark");
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
  return <span data-theme={useTheme()} />;
}

describe("a theme picked in another tab", () => {
  it("shows here at once, and Settings here say what the page shows", () => {
    act(() => root.render(<Picked />));
    expect(host.querySelector("span")!.dataset.theme).toBe("system");
    elsewhere("persona.theme", "dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(host.querySelector("span")!.dataset.theme).toBe("dark");
    elsewhere("persona.theme", "light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(host.querySelector("span")!.dataset.theme).toBe("light");
  });

  it("ignores other keys, and follows a cleared storage back to the system's (light here)", () => {
    act(() => root.render(<Picked />));
    elsewhere("persona.theme", "dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    // another key changing elsewhere leaves the page as it is, though the stored theme says light now
    localStorage.setItem("persona.theme", "light");
    elsewhere("persona.sidebarWidth", "300");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    // another tab cleared the storage: the event has no key
    localStorage.clear();
    elsewhere(null);
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(host.querySelector("span")!.dataset.theme).toBe("system");
  });

  it("syncs theme changes and cross-tab storage updates to desktop setThemeSource without echoing media events", () => {
    const setThemeSource = vi.fn();
    Object.assign(window, { personaDesktop: { setThemeSource } });
    act(() => root.render(<Picked />));

    act(() => setTheme("dark"));
    expect(setThemeSource).toHaveBeenLastCalledWith("dark");

    act(() => setTheme("light"));
    expect(setThemeSource).toHaveBeenLastCalledWith("light");

    act(() => setTheme("system"));
    expect(setThemeSource).toHaveBeenLastCalledWith("system");

    elsewhere("persona.theme", "dark");
    expect(setThemeSource).toHaveBeenLastCalledWith("dark");

    const callsBeforeMedia = setThemeSource.mock.calls.length;
    mediaListeners.forEach((cb) => cb());
    expect(setThemeSource).toHaveBeenCalledTimes(callsBeforeMedia);

    delete (window as { personaDesktop?: unknown }).personaDesktop;
    expect(() => act(() => setTheme("dark"))).not.toThrow();
  });
});
