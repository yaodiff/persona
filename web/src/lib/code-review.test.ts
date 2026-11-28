// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { hunkRows, type Hunk } from "./code-view";
import {
  changeId, findIn, foldedAtFirst, lineTyped, gapRows, gaps, ignoreWhitespace, isGenerated, keepLoaded, LineCache, lineLink, nextOf, onlyWhitespace, overlay, patchView, pickRange, reveal,
  reviewCommand, setViewedHere, splitSuggestions, suggestionBlock, viewedHere, wordDiff, words, wordSpans,
} from "./code-review";

const cut = (s: string, spans: { start: number; end: number }[]) => spans.map((p) => s.slice(p.start, p.end));

describe("words that changed", () => {
  it("splits a line into words, spaces and symbols", () => {
    expect(words("return fmt.Errorf(x)")).toEqual(["return", " ", "fmt", ".", "Errorf", "(", "x", ")"]);
    expect(words("名字 = 1")).toEqual(["名字", " ", "=", " ", "1"]);
  });

  it("marks only the words that changed", () => {
    const a = "\t\treturn err";
    const b = "\t\treturn fmt.Errorf(\"upload: %w\", err)";
    const d = wordDiff(a, b)!;
    expect(cut(a, d.old)).toEqual([]);
    expect(cut(b, d.new)).toEqual(["fmt.Errorf(\"upload: %w\", ", ")"]);
  });

  it("joins changed words with the space between them", () => {
    const d = wordDiff("let a = old value;", "let a = new thing;")!;
    expect(cut("let a = new thing;", d.new)).toEqual(["new thing"]);
    expect(cut("let a = old value;", d.old)).toEqual(["old value"]);
  });

  it("gives up on lines too unlike or too long", () => {
    expect(wordDiff("completely different text here", "nothing in common at all, really")).toBeNull();
    expect(wordDiff("x".repeat(2000), "y".repeat(2000))).toBeNull();
    expect(wordDiff("same", "same")).toEqual({ old: [], new: [] });
  });

  it("pairs a run of deletions with the additions after it, line by line", () => {
    const rows = hunkRows({ old_start: 1, new_start: 1, lines: [" a", "-const x = 1", "-const y = 2", "+const x = 10", "+const y = 2 // same", " b"] });
    const spans = wordSpans(rows);
    expect(cut(rows[1].text, spans.get(1)!)).toEqual(["1"]);
    expect(cut(rows[3].text, spans.get(3)!)).toEqual(["10"]);
    expect(cut(rows[4].text, spans.get(4)!).join("")).toBe(" // same");
    expect(spans.has(0)).toBe(false);
  });

  it("cuts highlighted tokens where the changed words begin and end", () => {
    const pieces = overlay([{ text: "return", cls: "kw" }, { text: " err", cls: "" }], "return err", [{ start: 3, end: 8 }]);
    expect(pieces).toEqual([
      { text: "ret", cls: "kw", changed: false },
      { text: "urn", cls: "kw", changed: true },
      { text: " e", cls: "", changed: true },
      { text: "rr", cls: "", changed: false },
    ]);
    expect(overlay(null, "plain", undefined)).toEqual([{ text: "plain", cls: "", changed: false }]);
  });
});

describe("whitespace", () => {
  it("draws a change of indentation only as unchanged", () => {
    const rows = hunkRows({ old_start: 10, new_start: 10, lines: ["-if x {", "-  y()", "+if x {", "+    y()", "+    z()", " }"] });
    const out = ignoreWhitespace(rows);
    expect(out.map((r) => r.kind)).toEqual(["context", "context", "add", "context"]);
    expect(out[1]).toMatchObject({ old: 11, new: 11, text: "    y()" });
    expect(out[2]).toMatchObject({ kind: "add", new: 12, text: "    z()" });
  });

  it("says when a diff is all whitespace", () => {
    expect(onlyWhitespace([{ old_start: 1, new_start: 1, lines: ["-a  b", "+a b"] }])).toBe(true);
    expect(onlyWhitespace([{ old_start: 1, new_start: 1, lines: ["-a", "+b"] }])).toBe(false);
  });
});

describe("generated files", () => {
  it("knows lockfiles, generated and vendored files by their paths", () => {
    for (const p of ["go.sum", "web/package-lock.json", "a/yarn.lock", "api/x.pb.go", "gen/y_gen.go", "dist/app.min.js", "vendor/x/y.go", "Cargo.lock"]) expect(isGenerated(p), p).toBe(true);
    for (const p of ["main.go", "src/lock.ts", "docs/vendor.md"]) expect(isGenerated(p), p).toBe(false);
    expect(isGenerated("main.go", true)).toBe(true);
  });

  it("folds generated, big and viewed files at first", () => {
    expect(foldedAtFirst({ path: "go.sum" })).toBe("generated");
    expect(foldedAtFirst({ path: "a.go", additions: 300, deletions: 200 })).toBe("big");
    expect(foldedAtFirst({ path: "a.go", viewed: true })).toBe("viewed");
    expect(foldedAtFirst({ path: "a.go", additions: 3 })).toBeNull();
  });
});

describe("hidden lines", () => {
  const hunks: Hunk[] = [
    { old_start: 5, new_start: 5, lines: [" a", "-b", "+b2", "+b3", " c"] }, // new 5..8, old 5..7
    { old_start: 30, new_start: 31, lines: [" x", " y"] }, // new 31..32
  ];

  it("finds the gaps before, between and after the hunks", () => {
    expect(gaps(hunks)).toEqual([
      { at: 0, from: 1, to: 4, delta: 0 },
      { at: 1, from: 9, to: 30, delta: -1 },
      { at: 2, from: 33, to: undefined, delta: -1 },
    ]);
    expect(gaps(hunks, 40)[2].to).toBe(40);
    expect(gaps([{ old_start: 1, new_start: 0, lines: ["-gone"] }])).toEqual([]);
  });

  it("shows a gap 20 lines at a time from either end, then all of it", () => {
    const g = gaps(hunks)[1]; // 9..30: 22 lines
    let r = reveal(g, { top: 0, bottom: 0 }, "up");
    expect(r).toEqual({ shown: { top: 0, bottom: 20 }, need: { from: 11, to: 30 } });
    r = reveal(g, r.shown, "down");
    expect(r).toEqual({ shown: { top: 2, bottom: 20 }, need: { from: 9, to: 10 } });
    expect(reveal(g, { top: 0, bottom: 0 }, "all").shown).toEqual({ top: 22, bottom: 0 });
    // after the last hunk the end is not known: down only
    const last = gaps(hunks)[2];
    expect(reveal(last, { top: 0, bottom: 0 }, "up").need).toEqual({ from: 33, to: 52 });
  });

  it("draws the lines shown from the cache, with both numbers", () => {
    const cache = new LineCache();
    cache.put(1, Array.from({ length: 40 }, (_, i) => `line ${i + 1}`), 40);
    const g = gaps(hunks, cache.total)[1];
    const { top, bottom, hidden } = gapRows(g, { top: 2, bottom: 3 }, cache);
    expect(top.map((r) => [r.old, r.new, r.text])).toEqual([[8, 9, "line 9"], [9, 10, "line 10"]]);
    expect(bottom.map((r) => r.new)).toEqual([28, 29, 30]);
    expect(hidden).toBe(17);
    expect(gapRows(g, { top: 22, bottom: 0 }, cache).hidden).toBe(0);
  });

  it("keeps what came of a file, and learns its end", () => {
    const c = new LineCache();
    c.put(10, ["a", "b"]);
    expect(c.has(10, 11)).toBe(true);
    expect(c.has(9, 11)).toBe(false);
    expect(c.has(10)).toBe(false); // the end is not known
    c.put(12, []); // nothing past 11
    expect(c.total).toBe(11);
    expect(c.has(10)).toBe(true);
  });
});

describe("viewed here", () => {
  beforeEach(() => localStorage.clear());

  it("keeps marks by path and revision, or by the lines when there is none", () => {
    const h: Hunk[] = [{ old_start: 1, new_start: 1, lines: ["+a"] }];
    expect(changeId("a.go", "r1")).toBe("a.go@r1");
    expect(changeId("a.go", undefined, h)).not.toBe(changeId("a.go", undefined, [{ old_start: 1, new_start: 1, lines: ["+b"] }]));
    setViewedHere("a.go@r1", true);
    expect(viewedHere().has("a.go@r1")).toBe(true);
    expect(viewedHere().has("a.go@r2")).toBe(false); // the file changed since
    setViewedHere("a.go@r1", false);
    expect(viewedHere().size).toBe(0);
  });
});

describe("suggestions", () => {
  it("cuts a comment at its suggestion blocks", () => {
    const body = "Maybe:\n\n```suggestion\nreturn nil\n```\nthanks";
    expect(splitSuggestions(body)).toEqual([
      { kind: "text", text: "Maybe:\n\n" },
      { kind: "suggestion", lines: ["return nil"] },
      { kind: "text", text: "\nthanks" },
    ]);
    expect(splitSuggestions("```suggestion\n```")).toEqual([{ kind: "suggestion", lines: [] }]);
    expect(splitSuggestions("no block")).toEqual([{ kind: "text", text: "no block" }]);
  });

  it("ignores outer code fences, supports 4-backtick suggestion fences, and strips CRLF", () => {
    const quoted = "Example:\n```md\n```suggestion\nnoop\n```\n```\nDone.";
    expect(splitSuggestions(quoted)).toEqual([{ kind: "text", text: quoted }]);

    const tildeQuoted = "~~~\n```suggestion\nnoop\n```\n~~~";
    expect(splitSuggestions(tildeQuoted)).toEqual([{ kind: "text", text: tildeQuoted }]);

    const nested = "Wrap in fence:\n````suggestion\n```ts\nconst x = 1;\n```\n````\nAfter";
    expect(splitSuggestions(nested)).toEqual([
      { kind: "text", text: "Wrap in fence:\n" },
      { kind: "suggestion", lines: ["```ts", "const x = 1;", "```"] },
      { kind: "text", text: "\nAfter" },
    ]);

    expect(splitSuggestions("```suggestion\r\na\r\nb\r\n```\r\n")).toEqual([
      { kind: "suggestion", lines: ["a", "b"] },
    ]);
  });

  it("writes the block of the lines picked", () => {
    expect(suggestionBlock(["a", "b"])).toBe("```suggestion\na\nb\n```\n");
  });
});

describe("picking lines and links", () => {
  it("makes a range on one side", () => {
    expect(pickRange({ side: "new", line: 9 }, { side: "new", line: 5 })).toEqual({ side: "new", start_line: 5, line: 9 });
    expect(pickRange({ side: "old", line: 9 }, { side: "new", line: 5 })).toEqual({ side: "new", line: 5 });
    expect(pickRange(null, { side: "new", line: 5 })).toEqual({ side: "new", line: 5 });
  });

  it("fills a line link, https only", () => {
    expect(lineLink("https://example.com/a.go#L{start}-L{end}", 3, 7)).toBe("https://example.com/a.go#L3-L7");
    for (const bad of [
      "javascript:alert({start})",
      "http://example.com/a.go#L{start}",
      "https:/admin#L{start}",
      "https:admin#L{start}",
      "https://user:pw@evil.example/a.go#L{start}",
      "https://@evil.example/a.go#L{start}",
      "https://good.example\\@evil.example/a.go#L{start}",
      "https://example.com/a.go\t#L{start}",
    ]) {
      expect(lineLink(bad, 1), bad).toBeUndefined();
    }
    expect(lineLink(undefined, 1)).toBeUndefined();
  });
});

describe("keys", () => {
  it("maps keys to commands, never while typing or with a modifier", () => {
    expect(reviewCommand({ key: "n" })).toBe("next-file");
    expect(reviewCommand({ key: "v" })).toBe("viewed");
    expect(reviewCommand({ key: "n", metaKey: true })).toBeNull();
    const input = document.createElement("textarea");
    expect(reviewCommand({ key: "n", target: input })).toBeNull();
    expect(reviewCommand({ key: "q" })).toBeNull();
  });

  it("takes a key typed with AltGr (Ctrl+Alt on Windows) as the character it types", () => {
    // a German layout on Windows: AltGr+9 types "]", AltGr+8 "["
    const altGr = (k: string) => k === "AltGraph";
    expect(reviewCommand({ key: "]", ctrlKey: true, altKey: true, getModifierState: altGr })).toBe("next-thread");
    expect(reviewCommand({ key: "[", ctrlKey: true, altKey: true, getModifierState: altGr })).toBe("prev-thread");
    // Ctrl+Alt without AltGr is still a chord, and AltGr does not make ⌘ a typed key
    expect(reviewCommand({ key: "]", ctrlKey: true, altKey: true, getModifierState: () => false })).toBeNull();
    expect(reviewCommand({ key: "]", ctrlKey: true, altKey: true, metaKey: true, getModifierState: altGr })).toBeNull();
    const input = document.createElement("input");
    expect(reviewCommand({ key: "]", ctrlKey: true, altKey: true, getModifierState: altGr, target: input })).toBeNull();
  });

  it("finds the next and the previous one from the top of the screen", () => {
    const tops = [0, 500, 1200];
    expect(nextOf(tops, 0, true)).toBe(1);
    expect(nextOf(tops, 600, true)).toBe(2);
    expect(nextOf(tops, 1200, true)).toBe(-1);
    expect(nextOf(tops, 600, false)).toBe(1);
    expect(nextOf(tops, 500, false)).toBe(0);
  });
});

describe("components in place", () => {
  type D = { type: string; key: string; path: string; hunks?: Hunk[]; load?: { id: string } };
  const diff = (key: string, lines?: string[]): D => ({ type: "diff", key, path: key, ...(lines ? { hunks: [{ old_start: 1, new_start: 1, lines }] } : { load: { id: "load" } }) });
  it("puts an answer's components where those of the same key were", () => {
    const view = { sections: [{ items: [diff("a"), diff("b")] }] };
    const out = patchView(view, [diff("b", ["+x"])]);
    expect(out.sections[0].items[1].hunks?.[0].lines).toEqual(["+x"]);
    expect(out.sections[0].items[0]).toBe(view.sections[0].items[0]);
    expect(patchView(view, [diff("zzz", ["+x"])])).toBe(view);
  });

  it("keeps the diffs loaded when the page is drawn again without them", () => {
    const before = { sections: [{ items: [diff("a", ["+1"]), diff("b")] }] };
    const next = { sections: [{ items: [diff("a"), diff("b")] }] };
    const out = keepLoaded(before, next);
    expect(out.sections[0].items[0].hunks?.[0].lines).toEqual(["+1"]);
    expect(out.sections[0].items[1].hunks).toBeUndefined();
    // the extension's own lines win
    const fresh = { sections: [{ items: [diff("a", ["+2"])] }] };
    expect(keepLoaded(before, fresh).sections[0].items[0].hunks?.[0].lines).toEqual(["+2"]);
  });
});

describe("reading a file", () => {
  it("finds a word in every line, any case", () => {
    expect(findIn(["Err := f()", "no", "if err != nil { return ERR }"], "err")).toEqual([
      { line: 0, spans: [{ start: 0, end: 3 }] },
      { line: 2, spans: [{ start: 3, end: 6 }, { start: 23, end: 26 }] },
    ]);
    expect(findIn(["a"], "")).toEqual([]);
  });

  it("reads a line number typed, held to the file", () => {
    expect(lineTyped("12", 1, 40)).toBe(12);
    expect(lineTyped("400", 1, 40)).toBe(40);
    expect(lineTyped("x", 1, 40)).toBeNull();
    expect(lineTyped(" ", 1, 40)).toBeNull();
  });
});
