// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { contrast, pageToken } from "./extension-theme";
import {
  ACCENT_PRESETS, accentChoice, accentCSS, accentTokens, applyLook, DEFAULT_GLASS_TRANSLUCENCY, glassTranslucency, personaAccent,
  PRESET_COLORS, savedPersonaAccent, setAccentChoice, setExtensionThemeActive, setGlassTranslucency, setPersonaAccent,
  setSurfaceStyle, setTextSize, surfaceStyle, textSize,
} from "./look";

const BACKGROUNDS = ["page", "canvas", "surface", "inset", "hover", "hover-2", "field"];

beforeEach(() => {
  localStorage.clear();
  applyLook();
});

describe("主题色", () => {
  it("adds nothing to the page by default", () => {
    expect(accentChoice()).toBe("default");
    expect(accentCSS("default", null)).toBe("");
    expect(document.documentElement.dataset.accent).toBeUndefined();
    expect(document.getElementById("persona-accent")).toBeNull();
  });

  it("gives every fixed color an accent that reads as a control, and words that read as text, on every background", () => {
    for (const p of ACCENT_PRESETS) {
      for (const scheme of ["light", "dark"] as const) {
        const tk = accentTokens(PRESET_COLORS[p].accent)[scheme];
        for (const bg of BACKGROUNDS.map((b) => pageToken(b, scheme))) {
          expect(bg).toMatch(/^#/);
          expect(contrast(tk["--accent"], bg)).toBeGreaterThanOrEqual(3);
          expect(contrast(tk["--accent-ink"], bg)).toBeGreaterThanOrEqual(4.5);
        }
      }
    }
  });

  it("keeps the app's accents as they are where they already meet the floor", () => {
    expect(accentTokens(PRESET_COLORS.lavender.accent).light["--accent"]).toBe("#7a5af8");
    expect(accentTokens(PRESET_COLORS.teal.accent).dark["--accent"]).toBe("#2a9798");
    // "Coral" is a hair short of 3:1 on the hover background in light: a few steps darker
    expect(accentTokens(PRESET_COLORS.coral.accent).light["--accent"]).toBe("#c9633c");
  });

  it("puts the choice on the page and takes it off again", () => {
    setAccentChoice("coral");
    expect(localStorage.getItem("persona.accent")).toBe("coral");
    expect(document.documentElement.dataset.accent).toBe("coral");
    const css = document.getElementById("persona-accent")!.textContent!;
    expect(css).toContain(":root[data-accent] { --accent: #c9633c;");
    expect(css).toContain(":root.dark[data-accent] { --accent: #cd6e45;");
    setAccentChoice("default");
    expect(localStorage.getItem("persona.accent")).toBeNull();
    expect(document.documentElement.dataset.accent).toBeUndefined();
    expect(document.getElementById("persona-accent")).toBeNull();
  });

  it("leaves my bubble to an extension theme laid over a fixed color", () => {
    expect(accentCSS("coral", null)).toContain("--user-bubble");
    expect(accentCSS("coral", null, true)).not.toContain("--user-bubble");
    expect(accentCSS("coral", null, true)).toContain("--accent: #c9633c;");
    setAccentChoice("coral");
    setExtensionThemeActive(true);
    expect(document.getElementById("persona-accent")!.textContent).not.toContain("--user-bubble");
    setExtensionThemeActive(false);
    expect(document.getElementById("persona-accent")!.textContent).toContain("--user-bubble");
  });

  it("follows the persona's accent, and is the default while the persona brings none", () => {
    setAccentChoice("persona");
    expect(document.documentElement.dataset.accent).toBeUndefined();
    setPersonaAccent(personaAccent({ light: { accent: "#94600A" }, dark: { accent: "#B47A0C" } }));
    expect(savedPersonaAccent()).toEqual({ light: "#94600a", dark: "#b47a0c" });
    expect(document.documentElement.dataset.accent).toBe("persona");
    expect(document.getElementById("persona-accent")!.textContent).toContain("--accent: #94600a;");
    setPersonaAccent(null);
    expect(document.documentElement.dataset.accent).toBeUndefined();
  });

  it("raises a persona's accent that would not read to the floor", () => {
    const tk = accentTokens({ light: "#f2c230", dark: "#1a1a1a" });
    expect(contrast(tk.light["--accent"], pageToken("page", "light"))).toBeGreaterThanOrEqual(3);
    expect(contrast(tk.dark["--accent"], pageToken("surface", "dark"))).toBeGreaterThanOrEqual(3);
  });

  it("reads a persona's colors: one scheme stands in for the other, anything but #RRGGBB is none", () => {
    expect(personaAccent({ light: { accent: "#112233" } })).toEqual({ light: "#112233", dark: "#112233" });
    expect(personaAccent({ light: { primary: "#112233" } })).toBeNull();
    expect(personaAccent({ light: { accent: "red" } })).toBeNull();
    expect(personaAccent(undefined)).toBeNull();
  });
});

describe("字号", () => {
  it("is 标准 with nothing set, and marks the page for 小 and 大", () => {
    expect(textSize()).toBe("standard");
    expect(document.documentElement.dataset.textSize).toBeUndefined();
    setTextSize("large");
    expect(localStorage.getItem("persona.text-size")).toBe("large");
    expect(document.documentElement.dataset.textSize).toBe("large");
    setTextSize("standard");
    expect(localStorage.getItem("persona.text-size")).toBeNull();
    expect(document.documentElement.dataset.textSize).toBeUndefined();
  });
});

describe("窗口外观风格", () => {
  it("defaults to opaque default style and toggles opt-in liquid-glass cleanly", () => {
    const setSurface = vi.fn();
    Object.assign(window, { personaDesktop: { platform: "darwin", setSurfaceStyle: setSurface } });
    expect(surfaceStyle()).toBe("default");
    expect(glassTranslucency()).toBe(DEFAULT_GLASS_TRANSLUCENCY);
    expect(document.documentElement.dataset.surfaceStyle).toBeUndefined();
    expect(document.documentElement.dataset.desktopGlass).toBeUndefined();

    setSurfaceStyle("liquid-glass");
    expect(localStorage.getItem("persona.surface-style")).toBe("liquid-glass");
    expect(document.documentElement.dataset.surfaceStyle).toBe("liquid-glass");
    expect(document.documentElement.dataset.desktopGlass).toBe("1");
    expect(document.documentElement.style.getPropertyValue("--glass-t")).toBe("0.75");
    expect(setSurface).toHaveBeenLastCalledWith("liquid-glass");

    setGlassTranslucency(0);
    expect(glassTranslucency()).toBe(0);
    expect(localStorage.getItem("persona.glass-translucency")).toBe("0");
    expect(document.documentElement.style.getPropertyValue("--glass-t")).toBe("0.00");

    setGlassTranslucency(100);
    expect(glassTranslucency()).toBe(100);
    expect(localStorage.getItem("persona.glass-translucency")).toBe("100");
    expect(document.documentElement.style.getPropertyValue("--glass-t")).toBe("1.00");

    setGlassTranslucency(84);
    expect(glassTranslucency()).toBe(84);
    expect(localStorage.getItem("persona.glass-translucency")).toBe("84");
    expect(document.documentElement.style.getPropertyValue("--glass-t")).toBe("0.84");

    setGlassTranslucency(DEFAULT_GLASS_TRANSLUCENCY);
    expect(localStorage.getItem("persona.glass-translucency")).toBeNull();
    expect(document.documentElement.style.getPropertyValue("--glass-t")).toBe("0.75");

    setSurfaceStyle("default");
    expect(localStorage.getItem("persona.surface-style")).toBeNull();
    expect(document.documentElement.dataset.surfaceStyle).toBeUndefined();
    expect(document.documentElement.dataset.desktopGlass).toBeUndefined();
    expect(document.documentElement.style.getPropertyValue("--glass-t")).toBe("");
    expect(setSurface).toHaveBeenLastCalledWith("default");
    delete (window as { personaDesktop?: unknown }).personaDesktop;
  });

  it("syncs web theme source before surface style and omits gracefully on older shells", () => {
    const order: string[] = [];
    const setThemeSource = vi.fn((theme: string) => order.push(`theme:${theme}`));
    const setSurface = vi.fn((style: string) => order.push(`surface:${style}`));
    Object.assign(window, { personaDesktop: { platform: "darwin", setThemeSource, setSurfaceStyle: setSurface } });

    localStorage.setItem("persona.theme", "dark");
    applyLook();
    expect(order).toEqual(["theme:dark", "surface:default"]);

    localStorage.setItem("persona.theme", "light");
    applyLook();
    expect(setThemeSource).toHaveBeenLastCalledWith("light");

    localStorage.setItem("persona.theme", "sepia");
    applyLook();
    expect(setThemeSource).toHaveBeenLastCalledWith("system");

    localStorage.removeItem("persona.theme");
    applyLook();
    expect(setThemeSource).toHaveBeenLastCalledWith("system");

    Object.assign(window, { personaDesktop: { platform: "darwin", setSurfaceStyle: setSurface } });
    expect(() => applyLook()).not.toThrow();
    delete (window as { personaDesktop?: unknown }).personaDesktop;
  });

  it("seeds data-desktop-glass synchronously in the index.html pre-paint script before React hydration", () => {
    const html = readFileSync(resolve(import.meta.dirname, "../../index.html"), "utf8");
    const prePaintScript = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)]
      .map((m) => m[1]!)
      .find((s) => s.includes("persona.surface-style"));
    expect(prePaintScript).toBeDefined();
    const runPrePaint = new Function(prePaintScript!);

    try {
      vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }));
      localStorage.setItem("persona.surface-style", "liquid-glass");
      Object.assign(window, { personaDesktop: { platform: "darwin", setSurfaceStyle: vi.fn() } });
      runPrePaint();
      expect(document.documentElement.dataset.surfaceStyle).toBe("liquid-glass");
      expect(document.documentElement.dataset.desktopGlass).toBe("1");

      delete document.documentElement.dataset.desktopGlass;
      delete document.documentElement.dataset.surfaceStyle;
      delete (window as { personaDesktop?: unknown }).personaDesktop;
      runPrePaint();
      expect(document.documentElement.dataset.surfaceStyle).toBe("liquid-glass");
      expect(document.documentElement.dataset.desktopGlass).toBeUndefined();

      delete document.documentElement.dataset.surfaceStyle;
      Object.assign(window, { personaDesktop: { platform: "win32", setSurfaceStyle: vi.fn() } });
      runPrePaint();
      expect(document.documentElement.dataset.desktopGlass).toBeUndefined();
    } finally {
      vi.unstubAllGlobals();
      delete (window as { personaDesktop?: unknown }).personaDesktop;
      document.documentElement.classList.remove("dark");
      delete document.documentElement.dataset.surfaceStyle;
      delete document.documentElement.dataset.desktopGlass;
      document.documentElement.style.removeProperty("--glass-t");
    }
  });
});
