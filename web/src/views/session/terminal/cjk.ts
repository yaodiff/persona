// Chinese in the terminal without gaps (docs/lessons/web.md "xterm.js"). xterm gives a wide character two cells and
// pads what its glyph leaves over with letter-spacing: a system Chinese font is 1em wide, two cells of a monospaced font
// are about 1.2em, so every character had a gap after it. As iOS (which enlarges such a glyph to fill its cells), the
// Chinese fallback here is a local system font under its own name, scaled with `size-adjust` so its characters are
// exactly two cells of the terminal's font. Its range-limited face goes first so named fallback fonts cannot shadow it.
// Where no such font is found the terminal keeps its font stack as it was (a browser without `size-adjust` loads the
// face unscaled: the gaps stay, nothing else changes).

import { loadFontFaces } from "../../../ui/font-faces";
import { fontLoadQuery, settleWithin, withFamily } from "../../../lib/terminal";

const FAMILY = "PersonaTermCJK";
// local() matches a font's full or PostScript name: the system Chinese fonts of macOS, Windows and Linux desktops.
const LOCAL = [
  "PingFang SC Regular", "PingFangSC-Regular", "Hiragino Sans GB W3", "HiraginoSansGB-W3", "Microsoft YaHei", "MicrosoftYaHei",
  "Noto Sans CJK SC Regular", "NotoSansCJKsc-Regular", "Noto Sans SC Regular", "NotoSansSC-Regular", "Source Han Sans SC Regular",
  "SourceHanSansSC-Regular", "WenQuanYi Micro Hei",
];
// Chinese, Japanese and Korean (and their full-width forms): the characters herdr gives two cells.
const RANGES =
  "U+1100-115F, U+2E80-303E, U+3041-33FF, U+3400-4DBF, U+4E00-9FFF, U+A000-A4CF, U+AC00-D7A3, U+F900-FAFF, U+FE30-FE4F, U+FF00-FF60, U+FFE0-FFE6, U+20000-2FFFD, U+30000-3FFFD";

const cache = new Map<string, Promise<string | null>>();

/** How wide `text` is in `family` at 100px, measured as the page lays it out. */
function measure(family: string, text: string): number {
  const span = document.createElement("span");
  span.textContent = text;
  span.style.cssText = `position:absolute;visibility:hidden;white-space:pre;font-size:100px;font-family:${family};left:-9999px;top:0`;
  document.body.appendChild(span);
  const w = span.getBoundingClientRect().width;
  span.remove();
  return w;
}

/**
 * The terminal's font stack with Chinese scaled to two cells of `stack`'s monospaced font, or null where it cannot be
 * done. Loads (once per stack) a size-adjusted face of the first system Chinese font found.
 */
export function cjkFontStack(stack: string): Promise<string | null> {
  let p = cache.get(stack);
  if (!p) {
    p = load(stack).catch(() => null);
    cache.set(stack, p);
  }
  return p;
}

async function load(stack: string): Promise<string | null> {
  if (typeof FontFace === "undefined" || !document.fonts) return null;
  const src = LOCAL.map((n) => `local("${n}")`).join(", ");
  const probeName = `${FAMILY}Probe`;
  const probe = new FontFace(probeName, src, { unicodeRange: RANGES });
  await probe.load(); // rejects when none of them is installed
  document.fonts.add(probe);
  const cell = measure(stack, "W".repeat(20)) / 20;
  // i18n-ignore: a CJK glyph to measure, not text
  const glyph = measure(`"${probeName}"`, "中".repeat(10)) / 10;
  document.fonts.delete(probe);
  if (!(cell > 0 && glyph > 0)) return null;
  const scale = (2 * cell) / glyph;
  if (scale < 1.02) return null; // already fills its two cells
  const face = new FontFace(FAMILY, src, { unicodeRange: RANGES, sizeAdjust: `${(scale * 100).toFixed(2)}%` } as FontFaceDescriptors);
  await face.load();
  document.fonts.add(face);
  return withFamily(stack, FAMILY);
}

/** How long the terminal waits for its font before it measures cells with whatever the browser has. */
const FONT_WAIT_MS = 1500;

/**
 * Resolves once the terminal's font is loaded (or after a moment, or where the browser cannot say): xterm measures a
 * cell when it opens and fits the grid from it, and a font that arrives later leaves cells the wrong width.
 */
export function terminalFontReady(size: number, stack: string): Promise<void> {
  if (typeof document === "undefined" || !document.fonts?.load) return Promise.resolve();
  const q = fontLoadQuery(size, stack);
  return settleWithin(loadFontFaces().then(() => document.fonts.load(q.font, q.sample)), FONT_WAIT_MS);
}
