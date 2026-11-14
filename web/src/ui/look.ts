// Settings › General › Appearance's "Theme color" and "Text size" (the app's Settings › Appearance › Theme color and Interface style › Text size, apple/Shared/Design/ColorSpec.swift and
// TextSize.swift; docs/web/web.md "Theme color and text size"). Both live in this browser only. The defaults add nothing to the page: no attribute,
// no style element, so the page is what it was before these existed. An extension theme (ExtensionThemePicker) still
// lays its colors over whichever theme color is chosen: its tokens are inline on <html>, above any stylesheet.
import { useSyncExternalStore } from "react";
import { SCHEME_ATTR } from "../lib/terminal-appearance";
import { meetContrast, MIN_ROLE_CONTRAST, MIN_TEXT_CONTRAST } from "./extension-theme";

type Scheme = "light" | "dark";
export interface Pair { light: string; dark: string }

/** The fixed colors, in the app's order; each is the accent of the style it came from (ColorSpec.blue, .coral …). */
export const ACCENT_PRESETS = ["blue", "coral", "lavender", "teal", "lark"] as const;
export type AccentPreset = (typeof ACCENT_PRESETS)[number];
/** "Default" is the page's own blue (the web has one style, so the app's "Follow interface style" is this); "Match avatar" the persona's accent. */
export type AccentChoice = "default" | "persona" | AccentPreset;

/**
 * The app's values. Only the light-text bubbles keep their own ("System blue"'s is the page's default bubble, "Lark blue"'s Lark's):
 * the page's own bubble has dark words, so the others get a pale bubble of their accent instead of the app's colored one.
 */
export const PRESET_COLORS: Record<AccentPreset, { accent: Pair; bubble?: Pair }> = {
  blue: { accent: { light: "#007aff", dark: "#0a84ff" }, bubble: { light: "#dceaff", dark: "#1d3557" } },
  coral: { accent: { light: "#d4683f", dark: "#cd6e45" } },
  lavender: { accent: { light: "#7a5af8", dark: "#8979eb" } },
  teal: { accent: { light: "#27747f", dark: "#2a9798" } },
  lark: { accent: { light: "#3370ff", dark: "#4c88ff" }, bubble: { light: "#d1e3ff", dark: "#133063" } },
};

const TINT = { light: 10, dark: 16 };
const BUBBLE = { light: 14, dark: 24 };

/**
 * The page tokens an accent sets: the accent itself (moved until it reads as a control on every background, as the
 * app's PersonaPalette.meeting does), the darker or lighter one for words, the tint of selected things, and my bubble.
 */
export function accentTokens(accent: Pair, bubble?: Pair): Record<Scheme, Record<string, string>> {
  const one = (scheme: Scheme) => {
    const a = meetContrast(accent[scheme], scheme, MIN_ROLE_CONTRAST);
    return {
      "--accent": a,
      "--accent-ink": meetContrast(a, scheme, MIN_TEXT_CONTRAST),
      "--accent-tint": `color-mix(in srgb, ${a} ${TINT[scheme]}%, var(--surface))`,
      "--user-bubble": bubble?.[scheme] ?? `color-mix(in srgb, ${a} ${BUBBLE[scheme]}%, var(--surface))`,
    };
  };
  return { light: one("light"), dark: one("dark") };
}

const HEX = /^#[0-9a-f]{6}$/i;

/** A persona package's accent (manifest.colors, docs/apple/persona.md "Color scheme"), a scheme it leaves out taken from the other; null without one. */
export function personaAccent(colors: { light?: Record<string, string>; dark?: Record<string, string> } | undefined): Pair | null {
  const light = colors?.light?.accent, dark = colors?.dark?.accent;
  const l = light && HEX.test(light) ? light : undefined, d = dark && HEX.test(dark) ? dark : undefined;
  if (!l && !d) return null;
  return { light: (l ?? d)!.toLowerCase(), dark: (d ?? l)!.toLowerCase() };
}

/**
 * The stylesheet for a choice; empty for "Default", and for "Match avatar" while the persona brings no colors (then it is "Default").
 * Under an extension theme (`extension`) my bubble is left out: the extension owns the accent, so the bubble must not
 * carry the theme color's tint (on the app the extension theme is a theme color of its own, ColorSpec(extensionTheme:over:)).
 * Each scheme's tokens also go on its terminal scope (SCHEME_ATTR), which a terminal of the other scheme reads.
 */
export function accentCSS(choice: AccentChoice, persona: Pair | null, extension = false): string {
  const pair = choice === "persona" ? persona : choice === "default" ? null : PRESET_COLORS[choice]?.accent;
  if (!pair) return "";
  const t = accentTokens(pair, choice === "persona" || choice === "default" ? undefined : PRESET_COLORS[choice].bubble);
  if (extension) for (const scheme of ["light", "dark"] as const) delete t[scheme]["--user-bubble"];
  const block = (sel: string, tokens: Record<string, string>) => `${sel} { ${Object.entries(tokens).map(([k, v]) => `${k}: ${v};`).join(" ")} }`;
  const scoped = (scheme: Scheme) => `[data-accent] [${SCHEME_ATTR}="${scheme}"]`;
  return [block(":root[data-accent]", t.light), block(":root.dark[data-accent]", t.dark), block(scoped("light"), t.light), block(scoped("dark"), t.dark)].join("\n");
}

// ── Text size ─────────────────────────────────────────────

/** "Small" / "Standard" / "Large": every font size in the page times --text-scale (styles.css, vite.config.ts's textScale). */
export const TEXT_SIZES = ["small", "standard", "large"] as const;
export type TextSize = (typeof TEXT_SIZES)[number];

// ── the saved choices ─────────────────────────────────────

const ACCENT_KEY = "persona.accent";
const PERSONA_ACCENT_KEY = "persona.accent.persona";
/** index.html reads this one before the first paint. */
export const TEXT_SIZE_KEY = "persona.text-size";

const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function write(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    // not kept: the next page starts with the default
  }
}

export function accentChoice(): AccentChoice {
  const v = read(ACCENT_KEY);
  return v === "persona" || (ACCENT_PRESETS as readonly string[]).includes(v ?? "") ? (v as AccentChoice) : "default";
}

/** The chosen persona's accent as last seen, so "Match avatar" paints before the hub's list comes. */
export function savedPersonaAccent(): Pair | null {
  try {
    const v = JSON.parse(read(PERSONA_ACCENT_KEY) ?? "null") as Pair | null;
    return v && HEX.test(v.light) && HEX.test(v.dark) ? v : null;
  } catch {
    return null;
  }
}

export function textSize(): TextSize {
  const v = read(TEXT_SIZE_KEY);
  return (TEXT_SIZES as readonly string[]).includes(v ?? "") ? (v as TextSize) : "standard";
}

const STYLE_ID = "persona-accent";
let extensionActive = false;

/** ExtensionThemeProvider says when an extension theme paints over the page (then the theme color leaves my bubble alone). */
export function setExtensionThemeActive(on: boolean) {
  if (extensionActive === on) return;
  extensionActive = on;
  applyLook();
}

/** Puts the saved choices on the page (main.tsx, before the first render; again after each change). */
export function applyLook(doc: Document = document) {
  const root = doc.documentElement;
  const css = accentCSS(accentChoice(), savedPersonaAccent(), extensionActive);
  let el = doc.getElementById(STYLE_ID);
  if (css) {
    if (!el) {
      el = doc.createElement("style");
      el.id = STYLE_ID;
      doc.head.appendChild(el);
    }
    el.textContent = css;
    root.dataset.accent = accentChoice();
  } else {
    el?.remove();
    delete root.dataset.accent;
  }
  const size = textSize();
  if (size === "standard") delete root.dataset.textSize;
  else root.dataset.textSize = size;
}

// Another tab of this browser changed one (or its storage was cleared): this one follows at once, as with the appearance
// (views/theme.ts), and its Settings show the choice it shows.
if (typeof window !== "undefined")
  addEventListener("storage", (e) => {
    if (e.key !== null && e.key !== ACCENT_KEY && e.key !== PERSONA_ACCENT_KEY && e.key !== TEXT_SIZE_KEY) return;
    applyLook();
    notify();
  });

export function setAccentChoice(c: AccentChoice) {
  write(ACCENT_KEY, c === "default" ? null : c);
  applyLook();
  notify();
}

/** The chosen persona's accent (Persona.tsx, each time the hub's list or the choice changes); null: it brings none. */
export function setPersonaAccent(p: Pair | null) {
  const before = read(PERSONA_ACCENT_KEY);
  const next = p ? JSON.stringify(p) : null;
  if (before === next) return;
  write(PERSONA_ACCENT_KEY, next);
  applyLook();
  notify();
}

export function setTextSize(s: TextSize) {
  write(TEXT_SIZE_KEY, s === "standard" ? null : s);
  applyLook();
  notify();
}

export const useAccentChoice = () => useSyncExternalStore(subscribe, accentChoice);
export const useTextSize = () => useSyncExternalStore(subscribe, textSize);
/** Serialized so useSyncExternalStore sees the same value until it changes. */
const personaAccentRaw = () => read(PERSONA_ACCENT_KEY);
export function usePersonaAccent(): Pair | null {
  useSyncExternalStore(subscribe, personaAccentRaw);
  return savedPersonaAccent();
}
