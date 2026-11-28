// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FontRows } from "./LookSettings";
import { applyFonts, FONT_KEY, fontPreference, setFontChoice } from "../../ui/font-preference";
import { t } from "../../i18n";
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Example" }) }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
beforeEach(() => { localStorage.clear(); applyFonts(); host = document.createElement("div"); document.body.append(host); root = createRoot(host); act(() => root.render(<FontRows />)); });
afterEach(() => { act(() => root.unmount()); host.remove(); localStorage.clear(); applyFonts(); vi.restoreAllMocks(); });
const selects = () => [...host.querySelectorAll("select")];
const choose = (index: number, value: string) => act(() => { const el = selects()[index]; el.value = value; el.dispatchEvent(new Event("change", { bubbles: true })); });
const type = (value: string) => act(() => { const el = host.querySelector("input")!; Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value); el.dispatchEvent(new Event("input", { bubbles: true })); });
// A custom family is a draft until its form is submitted (Apply); the save is async (ui/font-sync.ts saveFontChoice).
const apply = () => act(async () => { host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
describe("Appearance font controls", () => {
  it("shows two default selectors and reveals only the chosen custom field", async () => {
    expect(selects().map((el) => el.value)).toEqual(["default", "default"]);
    expect(host.querySelector("input")).toBeNull();
    choose(0, "custom"); expect(host.querySelectorAll("input")).toHaveLength(1);
    expect(localStorage.getItem(FONT_KEY)).toBeNull();
    type("Georgia"); expect(fontPreference().sans.preset).toBe("default");
    await apply(); expect(fontPreference().sans).toEqual({ preset: "custom", family: "Georgia" });
    await act(async () => choose(1, "menlo")); expect(fontPreference().sans.family).toBe("Georgia");
    await act(async () => choose(0, "default")); expect(host.querySelector("input")).toBeNull();
    expect(fontPreference().mono.preset).toBe("menlo");
    await act(async () => choose(1, "default"));
    expect([fontPreference().sans, fontPreference().mono]).toEqual([{ preset: "default" }, { preset: "default" }]);
  });
  it("keeps valid choices when font checks are unavailable, false or throw, and rejects invalid drafts", async () => {
    Object.defineProperty(document, "fonts", { configurable: true, value: { check: vi.fn(() => false) } });
    choose(0, "custom"); type("Missing Example Font"); await apply();
    expect(fontPreference().sans.family).toBe("Missing Example Font");
    // Availability is a canvas width probe (ui/font-availability.ts); jsdom has no 2D context, so it says nothing.
    expect(host.textContent).not.toContain(t("ui-look.font_unavailable", { kind: t("ui-look.font_sans") }));
    type("Bad, list"); expect(host.querySelector("input")!.getAttribute("aria-invalid")).toBe("true");
    await apply(); expect(fontPreference().sans.family).toBe("Missing Example Font");
    Object.defineProperty(document, "fonts", { configurable: true, value: { check: () => { throw Error("unsupported"); } } });
    type("Other Font"); await apply(); expect(fontPreference().sans.family).toBe("Other Font");
  });
  it("updates the mounted controls on another tab's save and reset", () => {
    act(() => setFontChoice("sans", { preset: "georgia" }));
    act(() => { localStorage.setItem(FONT_KEY, JSON.stringify({ mono: { preset: "custom", family: "Example Mono" } })); dispatchEvent(new StorageEvent("storage", { key: FONT_KEY })); });
    expect(selects().map((el) => el.value)).toEqual(["default", "custom"]);
    expect(host.querySelector("input")!.value).toBe("Example Mono");
    act(() => { localStorage.clear(); dispatchEvent(new StorageEvent("storage", { key: null })); });
    expect(selects().map((el) => el.value)).toEqual(["default", "default"]);
    expect(host.querySelector("input")).toBeNull();
  });
});
