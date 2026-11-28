// @vitest-environment jsdom
// The terminal's colors under its Appearance are the page's own tokens of that scheme (styles.css's blocks, a theme
// color's, an extension theme's), not a palette of their own: a dark terminal on a light page is drawn exactly as
// "Match page" draws it on a dark page.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { terminalPalette } from "../../../lib/terminal";
import { scopedThemeCSS } from "../../../ui/extension-theme";
import { applyLook } from "../../../ui/look";
import { findDecorations, terminalColors } from "./colors";

const css = readFileSync(join(__dirname, "../../../styles.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
/** styles.css's token blocks: the rules whose selectors include a bare `:root` or `.dark`. */
const tokenRules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
  .map((m) => ({ selectors: m[1].split(",").map((s) => s.trim()), body: m[2] }))
  .filter((r) => r.selectors.includes(":root") || r.selectors.includes(".dark"));

/** Every token the terminal is drawn with: its palette, its screen's padding and find marks, xterm's IME box. */
const read = new Set<string>();
terminalPalette((n) => (read.add(n), ""), true);
terminalPalette((n) => (read.add(n), ""), false);
const TOKENS = [...read, "--orange-tint", "--surface", "--line"];

const root = document.documentElement;
const styles: HTMLStyleElement[] = [];
function addStyle(text: string) {
  const el = document.createElement("style");
  el.textContent = text;
  document.head.appendChild(el);
  styles.push(el);
}
function pageScheme(dark: boolean) {
  root.classList.toggle("dark", dark);
}

beforeEach(() => addStyle(tokenRules.map((r) => `${r.selectors.join(", ")} {${r.body}}`).join("\n")));

afterEach(() => {
  styles.splice(0).forEach((el) => el.remove());
  document.getElementById("persona-accent")?.remove();
  root.className = "";
  root.removeAttribute("style");
  delete root.dataset.accent;
  delete root.dataset.extensionTheme;
  localStorage.clear();
});

describe("the terminal's colors", () => {
  it("has each scheme's block of every token it reads scoped for the terminal too", () => {
    for (const name of TOKENS) {
      const defines = new RegExp(`(^|[\\s;])${name}\\s*:`);
      for (const [page, scope] of [[":root", '[data-terminal-scheme="light"]'], [".dark", '[data-terminal-scheme="dark"]']]) {
        const blocks = tokenRules.filter((r) => r.selectors.includes(page) && defines.test(r.body));
        expect(blocks.length, `${page} ${name}`).toBeGreaterThan(0);
        for (const b of blocks) expect(b.selectors, `${page} ${name}`).toContain(scope);
      }
    }
  });

  it("reads the page's tokens under Match page, following its scheme", () => {
    const light = terminalColors("page");
    pageScheme(true);
    const dark = terminalColors("page");
    expect(light.background).toBe("#f7f7f9");
    expect(dark.background).toBe("#111113");
    expect(dark.black).not.toBe(dark.foreground); // a visible black on dark
  });

  it("draws a dark terminal on a light page with the page's dark tokens, and a light one on a dark page with its light ones", () => {
    pageScheme(true);
    const dark = terminalColors("page");
    const lightOnDark = terminalColors("light");
    pageScheme(false);
    const light = terminalColors("page");
    expect(terminalColors("dark")).toEqual(dark);
    expect(lightOnDark).toEqual(light);
    expect(terminalColors("light")).toEqual(light);
  });

  it("keeps a theme color in the terminal's scheme", () => {
    localStorage.setItem("persona.accent", "teal");
    applyLook();
    pageScheme(true);
    const dark = terminalColors("page");
    pageScheme(false);
    expect(terminalColors("page").blue).not.toBe(dark.blue);
    expect(terminalColors("dark")).toEqual(dark);
  });

  it("keeps an extension theme in the terminal's scheme, over a theme color", () => {
    localStorage.setItem("persona.accent", "teal");
    applyLook();
    const theme = { light: { "--accent": "#0b6e4f", "--green": "#1d6b2f" }, dark: { "--accent": "#5fd4a8", "--green": "#7fdc8f" } };
    root.dataset.extensionTheme = "example/forest";
    addStyle(scopedThemeCSS(theme));
    // what ExtensionThemePicker puts on <html>: the page's scheme's tokens, inline
    const inline = (dark: boolean) => {
      root.removeAttribute("style");
      for (const [k, v] of Object.entries(theme[dark ? "dark" : "light"])) root.style.setProperty(k, v);
      pageScheme(dark);
    };
    inline(true);
    const dark = terminalColors("page");
    expect(dark.blue).toBe("#5fd4a8");
    inline(false);
    expect(terminalColors("page").blue).toBe("#0b6e4f");
    expect(terminalColors("dark")).toEqual(dark);
  });

  it("marks find matches in the terminal's scheme", () => {
    pageScheme(true);
    const dark = findDecorations();
    pageScheme(false);
    expect(dark).toBeDefined();
    expect(findDecorations()).not.toEqual(dark);
    localStorage.setItem("persona.terminal.appearance", "dark");
    expect(findDecorations()).toEqual(dark);
  });
});
