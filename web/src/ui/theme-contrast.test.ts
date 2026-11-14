/// <reference types="node" />
import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";
vi.mock("../styles.css?raw", () => ({ default: "" }));
import { contrast, meetContrast, MIN_TEXT_CONTRAST } from "./extension-theme";
import { ACCENT_PRESETS, accentTokens, PRESET_COLORS } from "./look";

// The default palette's text tokens must stay readable as small text (WCAG AA 4.5:1) on every background token
// (#2804); the same backgrounds extension themes are checked against (extension-theme.ts).
const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
// The minutes' summary card's own tokens load with the Knowledge page (#6563).
const minutesCss = readFileSync(new URL("../views/knowledge/minutes-overview.css", import.meta.url), "utf8");
const BACKGROUNDS = ["page", "canvas", "surface", "inset", "hover", "hover-2", "field"];
const TEXT = ["ink", "ink-2", "ink-3", "accent-ink", "attention", "green", "orange", "red", "ai", "source-terminal", "source-desktop"];

// A color laid over another (#rrggbbaa over #rrggbb), and `color-mix(in srgb, a (1 - p), b p)`, as the browser paints them.
const channels = (hex: string) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
const toHex = (c: number[]) => `#${c.map((v) => Math.round(v).toString(16).padStart(2, "0")).join("")}`;
function over(color: string, bg: string): string {
  if (color.length !== 9) return color;
  const a = parseInt(color.slice(7, 9), 16) / 255, b = channels(bg);
  return toHex(channels(color).map((v, i) => v * a + b[i] * (1 - a)));
}
function mix(a: string, b: string, p: number): string {
  const y = channels(b);
  return toHex(channels(a).map((v, i) => v * (1 - p) + y[i] * p));
}
// A sweep of the RGB cube, for theme colors nobody listed: a persona's own accent, an extension theme's.
const STEPS = [0, 34, 68, 102, 136, 170, 204, 238, 255];
const SWEEP = STEPS.flatMap((r) => STEPS.flatMap((g) => STEPS.map((b) => toHex([r, g, b]))));

function tokens(scheme: "light" | "dark"): Record<string, string> {
  const block = (scheme === "light" ? /:root\s*\{([^}]+)\}/ : /\.dark\s*\{([^}]+)\}/).exec(css)?.[1] ?? "";
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-f]{3,8})\s*;/gi)].map((m) => [m[1], m[2]]));
}

describe("default palette contrast", () => {
  for (const scheme of ["light", "dark"] as const) {
    const t = tokens(scheme);
    it(`${scheme}: text tokens reach 4.5:1 on every background`, () => {
      const short = TEXT.flatMap((fg) => BACKGROUNDS.map((bg) => {
        expect(t[fg], `--${fg}`).toMatch(/^#[0-9a-f]{6}$/i);
        expect(t[bg], `--${bg}`).toMatch(/^#[0-9a-f]{3,6}$/i);
        const ratio = contrast(t[fg], t[bg]);
        return ratio < 4.5 ? `--${fg} on --${bg}: ${ratio.toFixed(2)}` : "";
      })).filter(Boolean);
      expect(short).toEqual([]);
    });
    it(`${scheme}: semantic tints are pale while error and selection fills stay neutral`, () => {
      const expected = scheme === "light"
        ? { green: "#e8f5ed", orange: "#fdf1e5", ai: "#efedfb" }
        : { green: "#3dbb7224", orange: "#f68f3c24", ai: "#9c94ff24" };
      for (const [role, value] of Object.entries(expected)) expect(t[`${role}-tint`]).toBe(value);
      expect(t["red-tint"]).toBe(t.inset);
      expect(t["accent-tint"]).toBe(t["hover-2"]);
      expect(contrast(t["danger-fill"], "#ffffff")).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t["accent-ink"], t.surface)).toBeGreaterThanOrEqual(4.5);
    });
    it(`${scheme}: neutral and user bubbles keep body, quote, attachment and focus contrast at 4.5:1`, () => {
      for (const token of ["neutral-bubble", "user-bubble"]) {
        const bubble = [...css.matchAll(new RegExp(`--${token}:\\s*(#[0-9a-f]{6})`, "gi"))][scheme === "light" ? 0 : 1]?.[1] ?? "";
        expect(bubble).toMatch(/^#[0-9a-f]{6}$/i);
        if (token === "user-bubble") expect(bubble).toBe(scheme === "light" ? "#dceaff" : "#1d3557");
        for (const role of ["ink", "ink-2", "ink-3"]) {
          // The dark blue bubble locally strengthens muted labels without changing page text.
          const fg = token === "user-bubble" && scheme === "dark" && role === "ink-3" ? t["ink-2"] : t[role];
          expect(contrast(fg, bubble), `${role} on ${token}`).toBeGreaterThanOrEqual(4.5);
        }
        if (token === "user-bubble") expect(contrast(t.ink, bubble)).toBeGreaterThanOrEqual(4.5);
      }
      expect(css).toContain(".dark .bg-user-bubble { --ink-3: var(--ink-2); }");
      expect(css).toContain(".bg-user-bubble :is(button, a, summary):focus-visible { outline-color: var(--ink); --tw-ring-color: var(--ink); }");
    });
    it(`${scheme}: --danger-wash (red at 12%) keeps --ink readable on the canvas and the tray's surface (words on it are --ink; --red only colours icons)`, () => {
      expect(css).toContain("--danger-wash: color-mix(in srgb, var(--red) 12%, transparent)");
      const over = (full: string) => {
        const bg = full.length === 4 ? `#${[...full.slice(1)].map((c) => c + c).join("")}` : full;
        const [r, g, b] = [1, 3, 5].map((i) => Math.round(parseInt(t.red.slice(i, i + 2), 16) * 0.12 + parseInt(bg.slice(i, i + 2), 16) * 0.88));
        return `#${[r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("")}`;
      };
      for (const bg of ["canvas", "surface"]) expect(contrast(t.ink, over(t[bg])), `ink on the wash over ${bg}`).toBeGreaterThanOrEqual(4.5);
    });
    // #6563: the minutes' summary card. Its topic cards lie on --topic-N-tint, its core conclusions on --ai-tint.
    it(`${scheme}: the minutes' topic tints keep the words, the bold places and the times at 4.5:1, their icons at 3:1`, () => {
      const nth = (sheet: string, name: string) => [...sheet.matchAll(new RegExp(`--${name}:\\s*(#[0-9a-f]{6})\\b`, "gi"))][scheme === "light" ? 0 : 1]?.[1] ?? "";
      const tints = [1, 2, 3, 4].map((n) => nth(minutesCss, `topic-${n}-tint`));
      expect(new Set(tints).size).toBe(4);
      for (const [i, tint] of tints.entries()) {
        expect(tint, `--topic-${i + 1}-tint`).toMatch(/^#[0-9a-f]{6}$/i);
        for (const role of ["ink", "ink-2", "ink-3", "accent-ink"]) {
          expect(contrast(t[role], tint), `--${role} on --topic-${i + 1}-tint`).toBeGreaterThanOrEqual(4.5);
        }
        // Never a status color's tint, nor the AI's.
        for (const role of ["green", "orange", "red", "ai", "accent"]) expect(tint).not.toBe(t[`${role}-tint`]);
        // The topic's icon: its hue on a surface tile.
        const hue = /--topic-\d: var\(--hue-([a-z]+)\)/g;
        const names = [...minutesCss.matchAll(hue)].map((m) => m[1]);
        expect(names).toEqual(["blue", "teal", "pink", "gray"]);
        expect(contrast(nth(css, `hue-${names[i]}`), t.surface), `--hue-${names[i]} on --surface`).toBeGreaterThanOrEqual(3);
      }
    });
    // In dark the conclusions draw a time's --accent-ink 15% of the way to --ink (minutes-overview.css).
    const onAi = (accentInk: string) => scheme === "dark" ? mix(accentInk, t.ink, 0.15) : accentInk;
    it(`${scheme}: the minutes' core conclusions keep --ai, the words and the times readable on --ai-tint`, () => {
      // --ai-tint is a solid color in light and the AI color at 0x24 alpha in dark: laid over the card's surface.
      const tint = over(t["ai-tint"], t.surface);
      expect(contrast(t.ai, tint), "--ai on --ai-tint").toBeGreaterThanOrEqual(4.5);
      // A time that cannot jump is --ink-3 there, which dark mode raises to --ink-2 (minutes-overview.css).
      const muted = scheme === "dark" ? t["ink-2"] : t["ink-3"];
      for (const [role, fg] of [["ink", t.ink], ["ink-2", t["ink-2"]], ["ink-3 as drawn", muted], ["accent-ink as drawn", onAi(t["accent-ink"])]]) {
        expect(contrast(fg, tint), `${role} on --ai-tint`).toBeGreaterThanOrEqual(4.5);
      }
      expect(minutesCss).toMatch(/\.dark \[data-minutes-conclusions\] \{\s*--ink-3: var\(--ink-2\);\s*--minutes-ai-accent-ink: color-mix\(in srgb, var\(--accent-ink\) 85%, var\(--ink\)\);\s*\}/);
      expect(minutesCss).toContain(".dark [data-minutes-conclusions] > ul { --accent-ink: var(--minutes-ai-accent-ink); }");
    });
    it(`${scheme}: every theme color's times keep 4.5:1 on the minutes' topic tints and in the core conclusions`, () => {
      const tints = [1, 2, 3, 4].map((n) => [...minutesCss.matchAll(new RegExp(`--topic-${n}-tint:\\s*(#[0-9a-f]{6})\\b`, "gi"))][scheme === "light" ? 0 : 1][1]);
      const ai = over(t["ai-tint"], t.surface);
      // The page's own, each built-in theme color, then any accent as a persona's (accentTokens) or an extension
      // theme's (meetContrast alone) is made readable on the page's backgrounds.
      const accents: [string, string][] = [
        ["default", t["accent-ink"]],
        ...ACCENT_PRESETS.map((p): [string, string] => [p, accentTokens(PRESET_COLORS[p].accent)[scheme]["--accent-ink"]]),
        ...SWEEP.map((c): [string, string] => [`persona ${c}`, accentTokens({ light: c, dark: c })[scheme]["--accent-ink"]]),
        ...SWEEP.map((c): [string, string] => [`extension ${c}`, meetContrast(c, scheme, MIN_TEXT_CONTRAST)]),
      ];
      const short = accents.flatMap(([name, ink]) => [
        ...tints.map((tint, i) => contrast(ink, tint) < 4.5 ? `${name} on --topic-${i + 1}-tint` : ""),
        contrast(onAi(ink), ai) < 4.5 ? `${name} on --ai-tint: ${contrast(onAi(ink), ai).toFixed(2)}` : "",
      ]).filter(Boolean);
      expect(short).toEqual([]);
      // Unadjusted, the built-in theme colors fall short on the dark AI tint: the rule above is what keeps them.
      if (scheme === "dark") expect(contrast(accentTokens(PRESET_COLORS.blue.accent).dark["--accent-ink"], ai)).toBeLessThan(4.5);
    });
    it(`${scheme}: --ink-3 stays a step quieter than --ink-2`, () => {
      expect(contrast(t["ink-3"], t.surface)).toBeLessThan(contrast(t["ink-2"], t.surface));
    });
  }
});
