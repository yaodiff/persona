import { describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";

/** The width xterm gives the character at the start of `text` once it is written to a fresh terminal. */
async function widthOf(term: Terminal, text: string): Promise<number> {
  await new Promise<void>((done) => term.write(`\x1b[2J\x1b[H${text}`, done));
  return term.buffer.active.getLine(0)!.getCell(0)!.getWidth();
}

async function loadUnicodeGraphemesAddon() {
  // The addon's Node branch decodes through a pool-backed Buffer and builds a DataView over the whole ArrayBuffer,
  // ignoring the Buffer's byteOffset. Earlier full-suite tests can leave non-zero bytes in that pool, making this
  // suite fail nondeterministically. Force the same zero-offset atob branch browsers use (#6874).
  vi.stubGlobal("Buffer", undefined);
  vi.resetModules();
  try {
    return await vi.importActual<typeof import("@xterm/addon-unicode-graphemes")>("@xterm/addon-unicode-graphemes");
  } finally {
    vi.unstubAllGlobals();
  }
}

describe("matchHerdrWidths", () => {
  it("gives emoji two cells like herdr, where xterm's own table gives one", async () => {
    Buffer.allocUnsafe(8).fill(0xff);
    const { UnicodeGraphemesAddon } = await loadUnicodeGraphemesAddon();
    const plain = new Terminal({ allowProposedApi: true, cols: 20, rows: 2 });
    expect(await widthOf(plain, "\u{1F600}")).toBe(1);
    expect(await widthOf(plain, "✅")).toBe(1);
    const term = new Terminal({ allowProposedApi: true, cols: 20, rows: 2 });
    term.loadAddon(new UnicodeGraphemesAddon());
    expect(term.unicode.activeVersion).toBe("15-graphemes");
    for (const emoji of ["\u{1F600}", "\u{1F680}", "✅", "\u{1F468}‍\u{1F469}‍\u{1F467}", "\u{1F1E8}\u{1F1F3}", "❤️"]) {
      expect(await widthOf(term, emoji), emoji).toBe(2);
    }
  });

  it("keeps Chinese and full-width punctuation at two cells and Latin text and box drawing at one", async () => {
    const { UnicodeGraphemesAddon } = await loadUnicodeGraphemesAddon();
    const term = new Terminal({ allowProposedApi: true, cols: 20, rows: 2 });
    term.loadAddon(new UnicodeGraphemesAddon());
    for (const wide of ["中", "「", "」", "（", "，", "：", "！", "あ", "한"]) expect(await widthOf(term, wide), wide).toBe(2);
    for (const narrow of ["a", "│", "─", "⌘", "✓", "★", "→"]) expect(await widthOf(term, narrow), narrow).toBe(1);
  });
});
