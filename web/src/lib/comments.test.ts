import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clampComment,
  codePreview,
  codeQuote,
  commentBlock,
  diffLabel,
  diffPick,
  fenceFor,
  lineRange,
  MAX_CODE_LINES,
  MAX_COMMENT_CHARS,
  openSpot,
  parseCommentBlock,
  rangeLabel,
  relativeTo,
  textQuote,
  truncateLines,
} from "./comments";
import { addQuote, cancelNote, getQuotes, parseQuotes, removeQuote, setNote, setQuotes, splitQuotes, storedQuotes, withQuoteBlock, type DraftQuote } from "./drafts";
import { markedLines } from "./comment-target";
import { parseDiff } from "./files";

const panelCalls = vi.hoisted(() => [] as unknown[]);
vi.mock("./panel", () => ({ openPanel: (p: unknown) => panelCalls.push(p) }));

const lines = (n: number, from = 1) => Array.from({ length: n }, (_, i) => `line ${from + i}`);

describe("the caps", () => {
  it("cuts a note at 4000 characters, never inside a surrogate pair", () => {
    expect(clampComment("a".repeat(MAX_COMMENT_CHARS))).toHaveLength(MAX_COMMENT_CHARS);
    expect(clampComment("a".repeat(MAX_COMMENT_CHARS + 50))).toHaveLength(MAX_COMMENT_CHARS);
    const edge = "a".repeat(MAX_COMMENT_CHARS - 1) + "😀";
    const cut = clampComment(edge);
    expect(cut).toHaveLength(MAX_COMMENT_CHARS - 1);
    expect(cut.endsWith("a")).toBe(true);
  });

  it("carries at most 60 lines of code and counts the rest", () => {
    expect(truncateLines(lines(60))).toEqual({ lines: lines(60), omitted: 0 });
    const cut = truncateLines(lines(61));
    expect(cut.lines).toHaveLength(MAX_CODE_LINES);
    expect(cut.omitted).toBe(1);
    expect(truncateLines(lines(500)).omitted).toBe(440);
    const q = codeQuote({ file: "a.ts", path: "/r/a.ts", from: 1, to: 100, lines: lines(100) });
    expect(q.text.split("\n")).toHaveLength(60);
    expect(q.comment?.spot?.omitted).toBe(40);
  });
});

describe("line ranges", () => {
  it("puts two clicked lines in order and writes one or a span", () => {
    expect(lineRange(14, 12)).toEqual({ from: 12, to: 14 });
    expect(lineRange(3, 3)).toEqual({ from: 3, to: 3 });
    expect(rangeLabel(7, 7)).toBe("7");
    expect(rangeLabel(12, 14)).toBe("12-14");
  });

  it("names a diff and a path from the session's folder", () => {
    expect(diffLabel({ kind: "staged" })).toBe("staged");
    expect(diffLabel({ kind: "unstaged" })).toBe("unstaged");
    expect(diffLabel({ kind: "commit", sha: "0123456789abcdef" })).toBe("commit 01234567");
    expect(diffLabel({ kind: "review", scope: "turn", base: "", emptyBase: true, live: true })).toBe("turn ..live");
    expect(relativeTo("/work/app/", "/work/app/src/a.ts")).toBe("src/a.ts");
    expect(relativeTo("/work/app", "/etc/hosts")).toBe("/etc/hosts");
    expect(relativeTo(undefined, "/etc/hosts")).toBe("/etc/hosts");
  });

  it("opens a review comment at its exact base and target", () => {
    panelCalls.length = 0;
    const spot = (diff: NonNullable<Parameters<typeof openSpot>[0]["diff"]>) => openSpot({
      file: "src/a.ts", path: "src/a.ts", from: 4, to: 4, omitted: 0, mid: "m", sid: "s", cwd: "/w", diff,
    });
    expect(spot({ kind: "review", scope: "commit", base: "1234567", baseHash: "1234567890abcdef" })).toBe(true);
    expect(panelCalls.at(-1)).toMatchObject({ kind: "review", scope: "commit", base: "1234567890abcdef" });
    expect(spot({ kind: "review", scope: "branch", base: "1234567", ref: "origin/main" })).toBe(true);
    expect(panelCalls.at(-1)).toMatchObject({ kind: "review", scope: "branch", base: "origin/main" });
    expect(spot({ kind: "review", scope: "turn", base: "1234567", baseHash: "1234567890abcdef", target: "76543210fedcba" })).toBe(true);
    expect(panelCalls.at(-1)).toMatchObject({ kind: "review", scope: "turn", base: "1234567890abcdef", target: "76543210fedcba" });
    expect(spot({ kind: "review", scope: "turn", base: "1234567", baseHash: "1234567890abcdef", live: true })).toBe(true);
    expect(panelCalls.at(-1)).toMatchObject({ kind: "review", scope: "turn", base: "1234567890abcdef", live: true });
    expect(spot({ kind: "review", scope: "turn", base: "1234567", emptyBase: true, target: "76543210fedcba" })).toBe(true);
    expect(panelCalls.at(-1)).toMatchObject({ kind: "review", scope: "turn", target: "76543210fedcba" });
  });

  it("picks diff rows by position: numbers from the new file, removed lines on their own count as old", () => {
    const rows = parseDiff(["@@ -10,3 +10,3 @@", " keep", "-gone", "+came", " tail"].join("\n"));
    // rows: 0 hunk, 1 keep (10/10), 2 gone (old 11), 3 came (new 11), 4 tail (12/12)
    expect(diffPick(rows, 1, 4)).toEqual({ from: 10, to: 12, old: false, lines: [" keep", "-gone", "+came", " tail"] });
    expect(diffPick(rows, 4, 3)).toEqual({ from: 11, to: 12, old: false, lines: ["+came", " tail"] });
    expect(diffPick(rows, 2, 2)).toEqual({ from: 11, to: 11, old: true, lines: ["-gone"] });
    expect(diffPick(rows, 0, 0)).toBeNull(); // a hunk header alone
  });
});

describe("the block the agent reads", () => {
  it("writes a code comment with its location, the code in a fence and the note", () => {
    const q = codeQuote({ file: "src/app.ts", path: "/w/src/app.ts", from: 12, to: 13, lines: ["const a = 1;", "const b = 2;"], lang: "ts" });
    q.comment!.body = "Why not use a constant?";
    expect(commentBlock(q, 2)).toBe(["Code comment 2", "File: src/app.ts:12-13", "```ts", "const a = 1;", "const b = 2;", "```", "", "Why not use a constant?"].join("\n"));
  });

  it("says which diff, whether the numbers are the old file's, and that code was left out", () => {
    const q = codeQuote({ file: "src/app.ts", path: "src/app.ts", from: 5, to: 80, lines: lines(76), old: true, diff: { kind: "commit", sha: "0123456789abcdef" }, lang: "diff" });
    q.comment!.body = "note";
    const block = commentBlock(q, 1).split("\n");
    expect(block.slice(0, 5)).toEqual(["Code comment 1", "File: src/app.ts:5-80", "Side: old", "Diff: commit 01234567", "Truncated: the first 60 of 76 lines"]);
    expect(block[5]).toBe("```diff");
    expect(block.at(-1)).toBe("note");
  });

  it("uses a fence the code cannot close, and leaves the note out when there is none", () => {
    expect(fenceFor("a ``` b")).toBe("````");
    expect(fenceFor("plain")).toBe("```");
    const q = codeQuote({ file: "a.md", path: "a.md", from: 1, to: 1, lines: ["```js"] });
    expect(commentBlock(q, 1)).toBe(["Code comment 1", "File: a.md:1", "````", "```js", "````"].join("\n"));
  });

  it("writes picked words as a nested quote and the note after a blank line", () => {
    const q = textQuote("first\nsecond", undefined);
    q.comment!.body = "needs a source";
    expect(commentBlock(q, 1)).toBe(["Comment 1", "Selected text:", "> first", "> second", "", "needs a source"].join("\n"));
  });

  it("cuts the note at 4000 characters in the block too", () => {
    const q = textQuote("x", undefined);
    q.comment!.body = "n".repeat(MAX_COMMENT_CHARS + 10);
    expect(commentBlock(q, 1).split("\n").at(-1)).toHaveLength(MAX_COMMENT_CHARS);
  });

  it("reads both kinds back", () => {
    const code = codeQuote({ file: "src/app.ts", path: "src/app.ts", from: 5, to: 80, lines: lines(76), old: true, diff: { kind: "staged" }, lang: "diff" });
    code.comment!.body = "line one\nline two";
    const back = parseCommentBlock(commentBlock(code, 3))!;
    expect(back.text).toBe(code.text);
    expect(back.comment?.body).toBe("line one\nline two");
    expect(back.comment?.spot).toMatchObject({ file: "src/app.ts", from: 5, to: 80, old: true, diff: { kind: "staged" }, lang: "diff", omitted: 16 });
    const words = textQuote("first\nsecond", undefined);
    words.comment!.body = "> not a quote\nreally";
    const again = parseCommentBlock(commentBlock(words, 1))!;
    expect(again.text).toBe("first\nsecond");
    expect(again.comment?.body).toBe("> not a quote\nreally");
    expect(parseCommentBlock("just a quote")).toBeNull();
  });
});

describe("the message with its quotes", () => {
  const code = (body: string): DraftQuote => ({ ...codeQuote({ file: "a.ts", path: "a.ts", from: 1, to: 1, lines: ["x"], lang: "ts" }), comment: { ...codeQuote({ file: "a.ts", path: "a.ts", from: 1, to: 1, lines: ["x"], lang: "ts" }).comment!, body } });

  it("keeps the old shape for a lone quote", () => {
    expect(withQuoteBlock({ text: "a\nb" }, "hello")).toBe("> a\n> b\n\nhello");
    expect(withQuoteBlock({ text: "a" }, "")).toBe("> a");
    expect(withQuoteBlock(null, "hello")).toBe("hello");
    expect(withQuoteBlock([], "hello")).toBe("hello");
  });

  it("puts the message quote first and numbers the comments in order", () => {
    const text = withQuoteBlock([code("one"), { text: "quoted" }, code("two")], "go");
    expect(text).toBe(
      ["> quoted", "", "> Code comment 1", "> File: a.ts:1", "> ```ts", "> x", "> ```", "> ", "> one", "", "> Code comment 2", "> File: a.ts:1", "> ```ts", "> x", "> ```", "> ", "> two", "", "go"].join("\n"),
    );
  });

  it("splits the leading blocks again and reads the comments back", () => {
    const text = withQuoteBlock([{ text: "quoted" }, code("one"), code("two")], "go on");
    const split = splitQuotes(text)!;
    expect(split.quotes).toHaveLength(3);
    expect(split.rest).toBe("go on");
    const back = parseQuotes(split.quotes);
    expect(back[0].text).toBe("quoted");
    expect(back[1].comment?.body).toBe("one");
    expect(back[2].comment?.spot?.file).toBe("a.ts");
  });

  it("takes a second block only when it is a comment", () => {
    expect(splitQuotes("> a\n\n> b\n\ntext")).toEqual({ quotes: ["a"], rest: "> b\n\ntext" });
    expect(splitQuotes("no quote")).toBeNull();
  });
});

describe("what a card shows of the code", () => {
  it("takes the shared indentation off the first lines and skips blank ones", () => {
    expect(codePreview("    if x {\n\n        run()\n    }")).toEqual(["if x {", "    run()"]);
    expect(codePreview("a\nb\nc", 3)).toEqual(["a", "b", "c"]);
    expect(codePreview("")).toEqual([]);
  });

  it("marks the lines that have a card, by side, with the card's number", () => {
    const a = codeQuote({ file: "a.ts", path: "a.ts", from: 3, to: 5, lines: ["x"] });
    const b = codeQuote({ file: "a.ts", path: "a.ts", from: 4, to: 4, lines: ["y"], old: true, diff: { kind: "staged" } });
    const c = codeQuote({ file: "b.ts", path: "b.ts", from: 1, to: 1, lines: ["z"] });
    const list = [{ text: "just a quote" }, a, b, c];
    expect([...markedLines(list, (s) => s.path === "a.ts")]).toEqual([[3, 1], [4, 1], [5, 1]]);
    expect([...markedLines(list, (s) => s.path === "a.ts", true)]).toEqual([[4, 2]]);
    expect([...markedLines(list, (s) => s.path === "b.ts")]).toEqual([[1, 3]]);
  });
});

describe("the quote list", () => {
  const key = "k/list";
  const store = new Map<string, string>();
  beforeEach(() => {
    store.clear();
    vi.stubGlobal("localStorage", { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => store.set(k, v), removeItem: (k: string) => store.delete(k) });
    setQuotes(key, []);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("replaces the message quote and piles the comments up after it", () => {
    addQuote(key, { text: "first" }, false);
    addQuote(key, textQuote("picked", undefined));
    addQuote(key, { text: "second" }, false);
    addQuote(key, textQuote("again", undefined));
    const list = getQuotes(key).list;
    expect(list.map((q) => q.text)).toEqual(["second", "picked", "again"]);
    expect(new Set(list.map((q) => q.id)).size).toBe(3);
    expect(storedQuotes(key).map((q) => q.text)).toEqual(["second", "picked", "again"]);
  });

  it("opens a new comment for writing, and Esc takes a fresh one back but only reverts an older one", () => {
    const id = addQuote(key, textQuote("picked", undefined));
    expect(getQuotes(key).editing).toBe(id);
    setNote(key, id, "draft");
    cancelNote(key, id, "");
    expect(getQuotes(key).list).toHaveLength(0);
    const kept = addQuote(key, textQuote("picked", undefined));
    setNote(key, kept, "first note", true);
    expect(getQuotes(key).editing).toBeNull();
    setNote(key, kept, "changed");
    cancelNote(key, kept, "first note");
    expect(getQuotes(key).list[0].comment?.body).toBe("first note");
  });

  it("clamps a note, removes a card, and reads what an older build stored", () => {
    const id = addQuote(key, textQuote("picked", undefined), false);
    setNote(key, id, "z".repeat(MAX_COMMENT_CHARS + 5));
    expect(getQuotes(key).list[0].comment?.body).toHaveLength(MAX_COMMENT_CHARS);
    removeQuote(key, id);
    expect(getQuotes(key).list).toHaveLength(0);
    expect(store.has(`quote:${key}`)).toBe(false);
    store.set("quote:old", JSON.stringify({ text: "legacy", from: "Persona" }));
    expect(storedQuotes("old")).toMatchObject([{ text: "legacy", from: "Persona" }]);
  });
});
