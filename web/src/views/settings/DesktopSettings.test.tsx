// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { DesktopSettings } from "./DesktopSettings";

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
  vi.unstubAllGlobals();
});

describe("Desktop options", () => {
  it("stays absent in a browser and in an older desktop without the additive capability", () => {
    vi.stubGlobal("personaDesktop", { version: 1 });
    act(() => root.render(<DesktopSettings />));
    expect(host.textContent).toBe("");
    expect(host.querySelector("button")).toBeNull();
  });

  it("opens only the shell's local options, not a hub-page Link command", () => {
    const openLocalSettings = vi.fn();
    vi.stubGlobal("personaDesktop", { version: 1, openLocalSettings });
    act(() => root.render(<DesktopSettings />));
    const button = host.querySelector<HTMLButtonElement>("button")!;
    expect(button.textContent).toContain(t("settings-desktop.open"));
    expect(host.textContent).toContain(t("settings-desktop.hint"));
    act(() => button.click());
    expect(openLocalSettings).toHaveBeenCalledOnce();
    expect(window.personaDesktop).not.toHaveProperty("linkCommand");
    expect(window.personaDesktop).not.toHaveProperty("saveHub");
  });
});
