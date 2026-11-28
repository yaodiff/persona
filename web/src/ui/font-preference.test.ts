// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { applyFonts, cacheSharedFonts, fontSettings, setBrowserFonts, FONT_FALLBACKS, FONT_KEY, FONT_PRESETS, fontPreference, normalizeFonts, quoteFont, setFontChoice, validFontFamily } from "./font-preference";

const bootstrap = [...readFileSync("index.html", "utf8").matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]).find((s) => s.includes('"persona.font"'))!;
new Function(bootstrap)();
const style = () => document.getElementById("persona-font");
beforeEach(() => { localStorage.clear(); document.documentElement.removeAttribute("style"); applyFonts(); });
afterEach(() => { localStorage.clear(); document.documentElement.removeAttribute("style"); applyFonts(); });

describe("browser-local font preference", () => {
  it("adds nothing by default, changes families independently, and removes the stylesheet on reset", () => {
    expect(style()).toBeNull();
    setFontChoice("sans", { preset: "georgia" });
    expect(style()!.textContent).toContain('"Georgia", ' + FONT_FALLBACKS.sans);
    expect(style()!.textContent).not.toContain("--font-mono:");
    setFontChoice("mono", { preset: "menlo" });
    setFontChoice("sans", { preset: "default" });
    expect(style()!.textContent).toContain('"Menlo"');
    expect(style()!.textContent).not.toContain("--font-sans:");
    setFontChoice("mono", { preset: "default" });
    expect(style()).toBeNull();
    expect(fontSettings().browser).toBe(true);
  });
  it("keeps the theme default behind an explicit choice, notices late theme changes, and restores it without touching the inline token", async () => {
    document.documentElement.style.setProperty("--font-sans", "Arial, sans-serif");
    setFontChoice("sans", { preset: "georgia" });
    expect(style()!.textContent).toContain('"Georgia", Arial, sans-serif !important');
    document.documentElement.style.setProperty("--font-sans", "Verdana, sans-serif");
    await Promise.resolve();
    expect(style()!.textContent).toContain('"Georgia", Verdana, sans-serif !important');
    setFontChoice("sans", { preset: "default" });
    expect(style()).toBeNull();
    expect(document.documentElement.style.getPropertyValue("--font-sans")).toBe("Verdana, sans-serif");
  });
  it("follows another tab's changes and storage resets", () => {
    localStorage.setItem(FONT_KEY, JSON.stringify({ mono: { preset: "consolas" } }));
    dispatchEvent(new StorageEvent("storage", { key: FONT_KEY }));
    expect(style()!.textContent).toContain('"Consolas"');
    localStorage.clear(); dispatchEvent(new StorageEvent("storage", { key: null }));
    expect(style()).toBeNull();
  });
  it("rejects malformed values and CSS injection while quoting installed family names", () => {
    for (const family of ["", " ", "A,B", "A; color:red", "A{X}", "</style>", "A\nB", "X".repeat(101), null]) expect(validFontFamily(family)).toBe(false);
    const family = 'A "B" \\ C';
    setFontChoice("sans", { preset: "custom", family });
    expect(fontPreference().sans.family).toBe(family);
    expect(style()!.textContent).toContain(quoteFont(family));
    expect(normalizeFonts({ mono: { preset: "__proto__" } }).mono.preset).toBe("default");
    localStorage.setItem(FONT_KEY, "{"); applyFonts(); expect(style()).toBeNull();
  });
  it("renders the exact runtime CSS before paint for every preset, custom escaping and invalid stored shape", () => {
    const cases: unknown[] = [null, [], 1, "bad", {}, { sans: null, mono: [] }, { sans: { preset: "__proto__" } }];
    for (const kind of ["sans", "mono"] as const) for (const preset of Object.keys(FONT_PRESETS[kind])) cases.push({ [kind]: { preset } });
    for (const family of ['A "B" \\ C', " 本地字体 ", "A; color:red", "A\nB", "X".repeat(101)]) cases.push({ sans: { preset: "custom", family }, mono: { preset: "menlo" } }); // i18n-ignore
    for (const value of cases) {
      localStorage.setItem(FONT_KEY, JSON.stringify(value)); applyFonts();
      const expected = style()?.textContent ?? null;
      style()?.remove();
      dispatchEvent(new StorageEvent("storage", { key: FONT_KEY }));
      expect(style()?.textContent ?? null, JSON.stringify(value)).toBe(expected);
      expect(document.querySelectorAll("#persona-font").length).toBeLessThanOrEqual(1);
    }
  });

  it("keeps a legacy choice local and caches Hub defaults behind it until the override is disabled", () => {
    localStorage.setItem(FONT_KEY, JSON.stringify({ sans: { preset: "georgia" }, mono: { preset: "consolas" } }));
    const shared = normalizeFonts({ sans: { preset: "inter" }, mono: { preset: "jetbrains" } });
    cacheSharedFonts(shared);
    expect(fontSettings()).toEqual({
      fonts: normalizeFonts({ sans: { preset: "georgia" }, mono: { preset: "consolas" } }),
      shared,
      browser: true,
    });
    setBrowserFonts(false);
    expect(fontSettings()).toEqual({ fonts: shared, shared, browser: false });
    const css = style()?.textContent;
    style()?.remove();
    dispatchEvent(new StorageEvent("storage", { key: FONT_KEY }));
    expect(style()?.textContent).toBe(css);
  });
  it("retains CJK and symbol fallbacks and makes no font download declaration", () => {
    setFontChoice("sans", { preset: "custom", family: "Arial" });
    setFontChoice("mono", { preset: "system" });
    expect(style()!.textContent).toContain('"PingFang SC"');
    expect(style()!.textContent).toContain("ui-monospace");
    expect(style()!.textContent).not.toMatch(/@font-face|url\(/);
  });
});
