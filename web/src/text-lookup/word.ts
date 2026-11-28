// Click-to-look-up for a Latin word (#10977). Pure resolution: what counts as a word, which clicks must not open
// anything, and the sentence around the word. The listener in listen.ts is the only caller that touches the page.

export const WORD_MIN = 2;
export const WORD_MAX = 40;
/** A press that travels farther than this is a drag or a scroll, not a lookup. */
export const MOVE_PX = 5;
export const CONTEXT_MAX = 500;
/** Long enough that the second press of a double-click arrives first and the word stays selected. */
export const DOUBLE_CLICK_MS = 400;

/** Latin letters, the same set as iOS (TextLookupWords.swift) and the manager (text_lookup.go): ASCII, Latin-1 and
 *  Latin Extended-A/B (é, ß, ł…), not × or ÷. JavaScript's `\b` and `\w` only know ASCII, so word edges are found
 *  with this class, never with them. */
const LETTERS = "A-Za-z\u00C0-\u00D6\u00D8-\u00F6\u00F8-\u024F";
const LETTER = new RegExp(`[${LETTERS}]`, "u");
/** An apostrophe (straight or typographic) or a hyphen, only between two letters. */
const WORD = new RegExp(`^[${LETTERS}]+(?:['\u2019-][${LETTERS}]+)*$`, "u");
const WORD_CHAR = new RegExp(`[${LETTERS}'\u2019-]`, "u");
const WORDS_IN_TEXT = new RegExp(`[${LETTERS}]+(?:['\u2019-][${LETTERS}]+)*`, "gu");
const SENTENCE_EDGE = /[.!?\n。！？]/;

export function isLookupWord(word: string): boolean {
  return word.length >= WORD_MIN && word.length <= WORD_MAX && WORD.test(word);
}

function isWordChar(ch: string | undefined): boolean {
  return !!ch && WORD_CHAR.test(ch);
}

/** The word as the manager takes it: a typographic apostrophe becomes the plain one, as on iOS. */
function plain(word: string): string {
  return word.replace(/\u2019/g, "'");
}

/** The Latin word covering `offset` (a caret between characters), or null. `offset === end` is the caret just after it. */
export function wordAt(text: string, offset: number): { start: number; end: number; word: string } | null {
  if (!text || offset < 0 || offset > text.length) return null;
  let i = -1;
  if (offset < text.length && isWordChar(text[offset])) i = offset;
  else if (isWordChar(text[offset - 1])) i = offset - 1;
  else return null;
  let start = i;
  let end = i + 1;
  while (isWordChar(text[start - 1])) start--;
  while (isWordChar(text[end])) end++;
  while (start < end && !LETTER.test(text[start]!)) start++;
  while (end > start && !LETTER.test(text[end - 1]!)) end--;
  if (start >= end || offset < start || offset > end) return null;
  const word = text.slice(start, end);
  return isLookupWord(word) ? { start, end, word: plain(word) } : null;
}

/** The sentence containing [start, end), at most `limit` characters, whitespace collapsed. */
export function sentenceAround(text: string, start: number, end: number, limit = CONTEXT_MAX): string {
  const safeStart = Math.max(0, Math.min(start, text.length));
  const safeEnd = Math.max(safeStart, Math.min(end, text.length));
  let from = safeStart;
  while (from > 0 && !SENTENCE_EDGE.test(text[from - 1]!)) from--;
  let to = safeEnd;
  while (to < text.length && !SENTENCE_EDGE.test(text[to]!)) to++;
  if (to < text.length && /[.!?。！？]/.test(text[to]!)) to++;
  const sentence = text.slice(from, to);
  const clipped = sentence.length <= limit ? sentence : clipAround(sentence, safeStart - from, safeEnd - safeStart, limit);
  return clipped.replace(/\s+/g, " ").trim();
}

function clipAround(sentence: string, focus: number, span: number, limit: number): string {
  let from = Math.max(0, focus - Math.floor((limit - span) / 2));
  let to = from + limit;
  if (to > sentence.length) {
    to = sentence.length;
    from = Math.max(0, to - limit);
  }
  return sentence.slice(from, to);
}

export interface PointerClick {
  button: number;
  /** Click count. 0 is a keyboard or scripted click; 2 and 3 are a double- or triple-click. */
  detail: number;
  alt: boolean;
  ctrl: boolean;
  meta: boolean;
  shift: boolean;
  downX: number;
  downY: number;
  upX: number;
  upY: number;
  selectionCollapsed: boolean;
}

/** Why this press must not look a word up, or null when it may. */
export function clickRejected(c: PointerClick): "button" | "modifier" | "multi" | "move" | "selection" | null {
  if (c.button !== 0) return "button";
  if (c.detail !== 1) return "multi";
  if (c.alt || c.ctrl || c.meta || c.shift) return "modifier";
  const dx = c.upX - c.downX;
  const dy = c.upY - c.downY;
  if (dx * dx + dy * dy > MOVE_PX * MOVE_PX) return "move";
  if (!c.selectionCollapsed) return "selection";
  return null;
}

/** Links, controls, code and the escape hatch. Checked on the target and every ancestor, so a row that is a link
 *  still wins when its text sits in a block inside the link. */
const SKIP = [
  "a", "button", "input", "textarea", "select", "option", "summary", "label",
  "code", "pre", "kbd", "samp", "script", "style", "noscript", "svg", "canvas",
  "[role='button']", "[role='link']", "[role='menuitem']", "[role='menuitemcheckbox']", "[role='menuitemradio']",
  "[role='tab']", "[role='checkbox']", "[role='radio']", "[role='switch']", "[role='option']", "[role='textbox']",
  "[contenteditable]:not([contenteditable='false'])",
  "[data-no-lookup]", "[data-terminal-scheme]", ".xterm",
].join(",");

/** True when the click belongs to a control, a link, code, a terminal or `data-no-lookup`. */
export function lookupBlocked(target: Node): boolean {
  const doc = target.ownerDocument;
  if (!doc) return true;
  let el: Element | null = target.nodeType === Node.ELEMENT_NODE ? (target as Element) : target.parentElement;
  while (el && el !== doc.documentElement) {
    if (el.matches(SKIP)) return true;
    el = el.parentElement;
  }
  return false;
}

const BLOCKS = new Set("DIV P LI ARTICLE SECTION TD TH H1 H2 H3 H4 H5 H6 BLOCKQUOTE PRE MAIN HEADER FOOTER UL OL TABLE FIGCAPTION DD DT FIGURE ASIDE NAV FORM".split(" "));

/** The block whose sentence is the word's context. Inline marks (em, span, a) do not end it. */
export function textContainer(node: Node): Element | null {
  const doc = node.ownerDocument;
  let el: Element | null = node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
  while (el && el !== doc?.documentElement) {
    const display = doc?.defaultView?.getComputedStyle(el).display ?? "";
    if (BLOCKS.has(el.tagName) || display === "block" || display === "flex" || display === "grid" || display === "list-item") return el;
    el = el.parentElement;
  }
  return node.parentElement;
}

export function pointInRects(rects: ArrayLike<{ left: number; right: number; top: number; bottom: number }>, x: number, y: number, slop = 1): boolean {
  for (let i = 0; i < rects.length; i++) {
    const r = rects[i]!;
    if (x >= r.left - slop && x <= r.right + slop && y >= r.top - slop && y <= r.bottom + slop) return true;
  }
  return false;
}

export interface ResolvedWord {
  word: string;
  context: string;
  range: Range;
}

/** The caret APIs, read loosely: a browser may have either, neither, or an older shape. */
interface CaretDoc {
  caretPositionFromPoint?: (x: number, y: number) => { offsetNode: Node; offset: number } | null;
  caretRangeFromPoint?: (x: number, y: number) => Range | null;
}

/** The word under (x, y), when the caret APIs can see a text node, the click lands in that word's rect, and nothing
 *  interactive owns it. Null is "do nothing", including where the caret APIs are missing. */
export function resolveWord(doc: Document, x: number, y: number): ResolvedWord | null {
  const caret = caretFromPoint(doc, x, y);
  if (!caret || caret.node.nodeType !== Node.TEXT_NODE) return null;
  if (lookupBlocked(caret.node)) return null;
  const text = caret.node.textContent ?? "";
  const span = wordAt(text, caret.offset);
  if (!span) return null;
  const range = doc.createRange();
  range.setStart(caret.node, span.start);
  range.setEnd(caret.node, span.end);
  const rects = range.getClientRects();
  if (!rects.length || !pointInRects(rects, x, y)) return null;
  const container = textContainer(caret.node);
  const context = container ? contextFor(container, caret.node as Text, span.start, span.end) : sentenceAround(text, span.start, span.end);
  return { word: span.word, context, range };
}

function caretFromPoint(doc: Document, x: number, y: number): { node: Node; offset: number } | null {
  const any = doc as unknown as CaretDoc;
  if (typeof any.caretPositionFromPoint === "function") {
    const pos = any.caretPositionFromPoint(x, y);
    if (!pos?.offsetNode) return null;
    return { node: pos.offsetNode, offset: pos.offset };
  }
  if (typeof any.caretRangeFromPoint === "function") {
    const range = any.caretRangeFromPoint(x, y);
    if (!range) return null;
    return { node: range.startContainer, offset: range.startOffset };
  }
  return null;
}

/** Sentence context from the block, skipping code and anything `data-no-lookup` marks. */
export function contextFor(container: Element, node: Text, start: number, end: number): string {
  const doc = container.ownerDocument;
  const walker = doc.createTreeWalker(container, NodeFilter.SHOW_TEXT, {
    acceptNode(current) {
      return lookupBlocked(current) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT;
    },
  });
  let found = -1;
  let cursor = 0;
  const parts: string[] = [];
  let current: Node | null;
  while ((current = walker.nextNode())) {
    const piece = current.textContent ?? "";
    if (current === node) found = cursor + start;
    parts.push(piece);
    cursor += piece.length;
  }
  if (found < 0) return sentenceAround(node.textContent ?? "", start, end);
  return sentenceAround(parts.join(""), found, found + (end - start));
}

/** Ranges of saved words in `root`. `budget.left` counts text nodes visited and stops the walk when it runs out. */
export function savedWordRanges(root: Node, words: readonly string[], budget: { left: number }): Range[] {
  const wanted = new Set(words.map((w) => plain(w).toLowerCase()));
  if (!wanted.size || budget.left <= 0) return [];
  const doc = root.ownerDocument ?? (root as Document);
  const owner = root.nodeType === Node.DOCUMENT_NODE ? (root as Document) : doc;
  const walker = owner.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(current) {
      return lookupBlocked(current) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT;
    },
  });
  const ranges: Range[] = [];
  const pattern = new RegExp(WORDS_IN_TEXT);
  const scan = (current: Text) => {
    if (budget.left <= 0) return;
    budget.left--;
    const text = current.textContent ?? "";
    pattern.lastIndex = 0;
    let match: RegExpExecArray | null;
    while ((match = pattern.exec(text))) {
      const word = match[0];
      if (!wanted.has(plain(word).toLowerCase()) || !isLookupWord(word)) continue;
      const range = owner.createRange();
      range.setStart(current, match.index);
      range.setEnd(current, match.index + word.length);
      ranges.push(range);
    }
  };
  // A walk starts after its root, so a text node handed in on its own is scanned here.
  if (root.nodeType === Node.TEXT_NODE) {
    if (!lookupBlocked(root)) scan(root as Text);
    return ranges;
  }
  let current: Node | null;
  while ((current = walker.nextNode())) {
    if (budget.left <= 0) break;
    scan(current as Text);
  }
  return ranges;
}
