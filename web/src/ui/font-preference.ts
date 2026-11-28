// The before-paint cache holds the effective font choices for this origin.
import { useSyncExternalStore } from "react";
export const FONT_KEY = "persona.font";
export const FONT_STYLE_ID = "persona-font";
export const FONT_PRESETS = {
  sans: { inter: "Inter", system: "system-ui", serif: "Georgia", georgia: "Georgia", sourceHan: "Source Han Serif SC" },
  mono: { jetbrains: "JetBrains Mono", system: "ui-monospace", sfMono: "SF Mono", fira: "Fira Code", cascadia: "Cascadia Code", menlo: "Menlo", consolas: "Consolas" },
} as const;
export type FontKind = keyof typeof FONT_PRESETS;
export interface FontChoice { preset: string; family?: string }
export interface FontPreference { sans: FontChoice; mono: FontChoice }
export const FONT_SERIF = 'Georgia, "Songti SC", "Hiragino Mincho ProN", "Noto Serif CJK SC", "Source Han Serif SC", SimSun, serif';
const DEFAULT: FontChoice = { preset: "default" };
// index.html's before-paint copy is checked against this implementation by font-preference.test.ts.
export const FONT_FALLBACKS = {
  sans: 'Inter, -apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", ui-sans-serif, system-ui, sans-serif',
  mono: '"JetBrains Mono", "SF Mono", "DejaVu Sans Mono", "Liberation Mono", ui-monospace, Menlo, Consolas, monospace',
};
/** One installed family, not CSS, a list, a URL, or a control sequence. */
export function validFontFamily(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0 && value.trim().length <= 100 && !/[\u0000-\u001f\u007f,;{}<>]/u.test(value);
}
export function normalizeFonts(raw: unknown): FontPreference {
  const obj = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
  const one = (kind: FontKind): FontChoice => {
    const value = obj[kind];
    if (!value || typeof value !== "object") return DEFAULT;
    const { preset, family } = value as FontChoice;
    if (preset === "custom" && validFontFamily(family)) return { preset, family: family.trim() };
    if (typeof preset === "string" && Object.hasOwn(FONT_PRESETS[kind], preset)) return { preset };
    return DEFAULT;
  };
  return { sans: one("sans"), mono: one("mono") };
}
interface FontSettings {
  fonts: FontPreference;
  browser: boolean;
  shared: FontPreference;
}
const stored = () => {
  try { return localStorage.getItem(FONT_KEY); }
  catch { return null; }
};
function readSettings(cached: string | null): FontSettings {
  let raw;
  try { raw = JSON.parse(cached ?? "null"); } catch { raw = null; }
  const fonts = normalizeFonts(raw);
  // Treat choices made before Hub sync as a browser override. Never upload them implicitly.
  const browser = raw?.browser === true || (raw?.browser === undefined && Object.values(fonts).some((choice) => choice.preset !== "default"));
  return { fonts, browser, shared: browser ? normalizeFonts(raw?.shared) : fonts };
}
export const fontSettings = () => readSettings(stored());
export const fontPreference = () => fontSettings().fonts;
export function fontFamily(kind: FontKind, choice: FontChoice): string | null {
  if (choice.preset === "custom") return validFontFamily(choice.family) ? choice.family.trim() : null;
  return (FONT_PRESETS[kind] as Record<string, string>)[choice.preset] ?? null;
}
export const quoteFont = (family: string) => `"${family.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
export function fontCSS(value: FontPreference, inline: CSSStyleDeclaration): string {
  const declarations = (["sans", "mono"] as const).flatMap((kind) => {
    if (kind === "sans" && value.sans.preset === "serif") return [];
    const family = fontFamily(kind, value[kind]);
    if (!family) return [];
    const first = value[kind].preset === "system" ? family : quoteFont(family);
    // Explicit choices win over inline theme defaults. Reset removes only the chosen family's declaration.
    return [`--font-${kind}: ${first}, ${inline.getPropertyValue(`--font-${kind}`) || FONT_FALLBACKS[kind]} !important;`, ...(kind === "mono" ? ["--font-inline-code: var(--font-mono);"] : [])];
  });
  const rule = declarations.length ? `:root { ${declarations.join(" ")} }` : "";
  return rule + (value.sans.preset === "serif" ? `.prose-j { font-family: ${FONT_SERIF}; }` : "");
}
export function applyFonts(doc: Document = document) {
  const css = fontCSS(fontPreference(), doc.documentElement.style);
  let style = doc.getElementById(FONT_STYLE_ID);
  if (!css) { style?.remove(); return; }
  if (!style) { style = doc.createElement("style"); style.id = FONT_STYLE_ID; doc.head.append(style); }
  if (style.textContent !== css) style.textContent = css;
}
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
export function useFontSettings(): FontSettings {
  return readSettings(useSyncExternalStore(subscribe, stored));
}
function storeFonts({ fonts, browser, shared }: FontSettings) {
  try {
    localStorage.setItem(FONT_KEY, JSON.stringify({ ...fonts, browser, ...(browser ? { shared } : {}) }));
  } catch { return false; }
  applyFonts();
  listeners.forEach((l) => l());
  return true;
}
export function cacheSharedFonts(shared: FontPreference) {
  const current = fontSettings();
  return storeFonts({ fonts: current.browser ? current.fonts : shared, browser: current.browser, shared });
}
export function setBrowserFonts(browser: boolean) {
  const current = fontSettings();
  return storeFonts({ ...current, browser, fonts: browser ? current.fonts : current.shared });
}
export function setFontChoice(kind: FontKind, choice: FontChoice) {
  const current = fontSettings();
  return storeFonts({ ...current, browser: true, fonts: normalizeFonts({ ...current.fonts, [kind]: choice }) });
}
if (typeof window !== "undefined") {
  addEventListener("storage", (event) => {
    if (event.key !== null && event.key !== FONT_KEY) return;
    listeners.forEach((l) => l());
  });
}
