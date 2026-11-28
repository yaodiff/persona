// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { clickRejected, contextFor, isLookupWord, resolveWord, savedWordRanges, sentenceAround, wordAt } from "./word";

afterEach(() => {
  document.body.replaceChildren();
  delete (document as unknown as Record<string, unknown>).caretPositionFromPoint;
  delete (document as unknown as Record<string, unknown>).caretRangeFromPoint;
});

describe("Latin words", () => {
  it("accepts letters and an inner apostrophe or hyphen, and nothing else", () => {
    expect(isLookupWord("hello")).toBe(true);
    expect(isLookupWord("don't")).toBe(true);
    expect(isLookupWord("well-known")).toBe(true);
    expect(isLookupWord("a")).toBe(false);
    expect(isLookupWord("")).toBe(false);
    expect(isLookupWord("-no")).toBe(false);
    expect(isLookupWord("end-")).toBe(false);
    expect(isLookupWord("a".repeat(41))).toBe(false);
    expect(isLookupWord("a".repeat(40))).toBe(true);
    expect(isLookupWord("hello2")).toBe(false);
    expect(isLookupWord("你好")).toBe(false);
  });

  it("reads the word under a caret, including the position just after it", () => {
    expect(wordAt("Say hello there", 4)).toEqual({ start: 4, end: 9, word: "hello" });
    expect(wordAt("Say hello there", 9)).toEqual({ start: 4, end: 9, word: "hello" });
    // a caret on the space sits just after the previous word; the click's rect decides if it landed there
    expect(wordAt("Say hello there", 3)).toEqual({ start: 0, end: 3, word: "Say" });
    expect(wordAt("a well-known path", 3)).toMatchObject({ word: "well-known" });
    expect(wordAt("I don't know", 3)).toMatchObject({ word: "don't" });
    expect(wordAt("x", 0)).toBeNull();
  });

  it("keeps the surrounding sentence and cuts a long one at 500 characters", () => {
    expect(sentenceAround("First. Say hello there. Next", 11, 16)).toBe("Say hello there.");
    const long = `${"word ".repeat(200)}hello ${"tail ".repeat(200)}`;
    const at = long.indexOf("hello");
    const sentence = sentenceAround(long, at, at + 5);
    expect(sentence.length).toBeLessThanOrEqual(500);
    expect(sentence).toContain("hello");
  });
});

describe("clicks that must not look a word up", () => {
  const plain = { button: 0, detail: 1, alt: false, ctrl: false, meta: false, shift: false, downX: 10, downY: 10, upX: 12, upY: 11, selectionCollapsed: true };

  it("rejects anything but a still, unmodified primary click with a collapsed selection", () => {
    expect(clickRejected(plain)).toBeNull();
    expect(clickRejected({ ...plain, button: 2 })).toBe("button");
    expect(clickRejected({ ...plain, detail: 2 })).toBe("multi");
    expect(clickRejected({ ...plain, detail: 3 })).toBe("multi");
    expect(clickRejected({ ...plain, detail: 0 })).toBe("multi");
    expect(clickRejected({ ...plain, shift: true })).toBe("modifier");
    expect(clickRejected({ ...plain, meta: true })).toBe("modifier");
    expect(clickRejected({ ...plain, upX: 20, upY: 20 })).toBe("move");
    expect(clickRejected({ ...plain, selectionCollapsed: false })).toBe("selection");
  });
});

function layOut(word: string): Text {
  const p = document.createElement("p");
  p.textContent = `Say ${word} there. Next sentence.`;
  document.body.append(p);
  const text = p.firstChild as Text;
  const rect = new DOMRect(0, 0, 200, 20);
  Range.prototype.getClientRects = () => ({ length: 1, 0: rect, item: () => rect }) as unknown as DOMRectList;
  (document as unknown as Record<string, unknown>).caretPositionFromPoint = () => ({
    offsetNode: text,
    offset: text.data.indexOf(word),
  });
  return text;
}

describe("where a click resolves", () => {
  it("reads the word and its sentence when the click lands in the word", () => {
    layOut("hello");
    expect(resolveWord(document, 20, 10)).toEqual({ word: "hello", context: "Say hello there.", range: expect.any(Range) });
  });

  it("uses caretRangeFromPoint where caretPositionFromPoint is missing", () => {
    const text = layOut("hello");
    delete (document as unknown as Record<string, unknown>).caretPositionFromPoint;
    (document as unknown as Record<string, unknown>).caretRangeFromPoint = () => {
      const range = document.createRange();
      range.setStart(text, text.data.indexOf("hello"));
      range.collapse(true);
      return range;
    };
    expect(resolveWord(document, 20, 10)?.word).toBe("hello");
  });

  it("does nothing when the click misses the word's rect", () => {
    layOut("hello");
    const rect = new DOMRect(100, 100, 20, 10);
    Range.prototype.getClientRects = () => ({ length: 1, 0: rect, item: () => rect }) as unknown as DOMRectList;
    expect(resolveWord(document, 20, 10)).toBeNull();
  });

  it.each([
    ["a link", (host: HTMLElement) => { const a = document.createElement("a"); a.href = "/next"; host.append(a); return a; }],
    ["a button", (host: HTMLElement) => { const b = document.createElement("button"); host.append(b); return b; }],
    ["a role=button row", (host: HTMLElement) => { host.setAttribute("role", "button"); return host; }],
    ["an input", (host: HTMLElement) => { const input = document.createElement("input"); host.append(input); return input; }],
    ["a textarea", (host: HTMLElement) => { const area = document.createElement("textarea"); host.append(area); return area; }],
    ["code", (host: HTMLElement) => { const code = document.createElement("code"); host.append(code); return code; }],
    ["pre", (host: HTMLElement) => { const pre = document.createElement("pre"); host.append(pre); return pre; }],
    ["contenteditable", (host: HTMLElement) => { host.setAttribute("contenteditable", "true"); return host; }],
    ["data-no-lookup", (host: HTMLElement) => { host.setAttribute("data-no-lookup", ""); return host; }],
  ])("does nothing inside %s", (_name, wrap) => {
    document.body.replaceChildren();
    const host = document.createElement("div");
    const box = wrap(host);
    box.append("Say hello there.");
    document.body.append(host);
    const text = box.lastChild?.nodeType === Node.TEXT_NODE ? box.lastChild : box.querySelector("*")?.lastChild;
    const node = (text?.nodeType === Node.TEXT_NODE ? text : box.firstChild) as Text;
    // an input has no text node of its own: point the caret at the element
    const target: Node = node?.nodeType === Node.TEXT_NODE ? node : box;
    (document as unknown as Record<string, unknown>).caretPositionFromPoint = () => ({
      offsetNode: target,
      offset: target.nodeType === Node.TEXT_NODE ? (target.textContent ?? "").indexOf("hello") : 0,
    });
    const rect = new DOMRect(0, 0, 200, 20);
    Range.prototype.getClientRects = () => ({ length: 1, 0: rect, item: () => rect }) as unknown as DOMRectList;
    expect(resolveWord(document, 20, 10)).toBeNull();
  });

  it("joins the sentence across inline marks and skips a code span", () => {
    const p = document.createElement("p");
    p.innerHTML = "Say <em>hello</em> there. <code>hello</code>";
    document.body.append(p);
    const em = p.querySelector("em")!.firstChild as Text;
    expect(contextFor(p, em, 0, 5)).toBe("Say hello there.");
  });

  it("does not underline a saved word inside code, and does when the switch is on", () => {
    const p = document.createElement("p");
    p.innerHTML = "A hello here. <code>hello</code>";
    document.body.append(p);
    const on = savedWordRanges(document.body, ["Hello"], { left: 20 });
    expect(on).toHaveLength(1);
    expect(on[0]!.toString()).toBe("hello");
    expect(savedWordRanges(document.body, [], { left: 20 })).toHaveLength(0);
    const budget = { left: 0 };
    expect(savedWordRanges(document.body, ["hello"], budget)).toHaveLength(0);
  });

  it("scans a text node handed in as the walk's root", () => {
    const p = document.createElement("p");
    p.textContent = "Hello again, hello.";
    document.body.append(p);
    const text = p.firstChild as Text;
    const budget = { left: 5 };
    const ranges = savedWordRanges(text, ["hello"], budget);
    expect(ranges.map((r) => [r.startContainer, r.startOffset, r.toString()])).toEqual([[text, 0, "Hello"], [text, 13, "hello"]]);
    expect(budget.left).toBe(4);
    // a text node inside code is still skipped, root or not
    const code = document.createElement("code");
    code.textContent = "hello";
    document.body.append(code);
    expect(savedWordRanges(code.firstChild!, ["hello"], { left: 5 })).toEqual([]);
    expect(savedWordRanges(text, ["hello"], { left: 0 })).toEqual([]);
  });
});

// The same Latin letters as iOS (TextLookupWords.swift) and the manager (text_lookup.go): accented words are words.
describe("accented Latin words", () => {
  it("accepts é, ï, Å and ß, and still refuses other scripts, digits, × and ÷", () => {
    for (const word of ["café", "résumé", "naïve", "Ångström", "straße", "łódź", "über-cool", "l’été"]) {
      expect(isLookupWord(word), word).toBe(true);
    }
    for (const word of ["слово", "单词", "café2", "a×b", "a÷b", "é", "-café", "café-"]) {
      expect(isLookupWord(word), word).toBe(false);
    }
  });

  it("reads the whole accented word under the caret, not its ASCII part", () => {
    expect(wordAt("un café noir", 4)).toEqual({ start: 3, end: 7, word: "café" });
    expect(wordAt("un café noir", 7)).toEqual({ start: 3, end: 7, word: "café" });
    expect(wordAt("send a résumé.", 9)).toEqual({ start: 7, end: 13, word: "résumé" });
    expect(wordAt("a naïve view", 3)).toMatchObject({ word: "naïve" });
    // a typographic apostrophe is looked up as the plain one, as iOS sends it
    expect(wordAt("I don’t know", 3)).toMatchObject({ word: "don't" });
  });

  it.each(["café", "résumé", "naïve"])("resolves a click on %s", (word) => {
    layOut(word);
    expect(resolveWord(document, 20, 10)).toEqual({ word, context: `Say ${word} there.`, range: expect.any(Range) });
  });

  it("underlines saved accented words as whole words only", () => {
    const p = document.createElement("p");
    p.textContent = "Café, résumé and a naïve cafés idea; résumés too.";
    document.body.append(p);
    const ranges = savedWordRanges(document.body, ["café", "résumé", "naïve"], { left: 5 });
    expect(ranges.map((r) => r.toString())).toEqual(["Café", "résumé", "naïve"]);
  });
});
