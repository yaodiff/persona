// Underlines saved words without changing the text (#10977). The CSS Custom Highlight API paints ranges over
// existing text nodes. A browser without it gets nothing: no style element, no observer.
import { savedWordRanges } from "./word";

const NAME = "persona-saved-words";
const NODES_PER_TURN = 80;
const DEBOUNCE_MS = 150;

type HighlightSet = Set<AbstractRange>;

interface HighlightHost {
  highlights: { set(name: string, value: HighlightSet): void; delete(name: string): void };
}

export function highlightSupported(scope: typeof globalThis = globalThis): boolean {
  const css = (scope as { CSS?: HighlightHost }).CSS;
  const Ctor = (scope as { Highlight?: new () => HighlightSet }).Highlight;
  return typeof Ctor === "function" && !!css && typeof css.highlights?.set === "function";
}

export interface SavedHighlights {
  update(words: readonly string[]): void;
  stop(): void;
}

function shown(node: Text): boolean {
  const parent = node.parentElement;
  if (!parent) return false;
  const check = (parent as Element & { checkVisibility?: (opts?: object) => boolean }).checkVisibility;
  if (typeof check !== "function") return true;
  try {
    return check.call(parent, { checkOpacity: false, checkVisibilityCSS: true });
  } catch {
    return true;
  }
}

/** Paints `words` over the text of `doc` and keeps up with edits, a few dozen nodes a frame. */
export function attachSavedHighlights(doc: Document = document): SavedHighlights {
  const scope = doc.defaultView ?? globalThis;
  if (!highlightSupported(scope)) return { update() {}, stop() {} };
  const Ctor = (scope as unknown as { Highlight: new () => HighlightSet }).Highlight;
  const css = (scope as unknown as { CSS: HighlightHost }).CSS;
  const paint = new Ctor();
  css.highlights.set(NAME, paint);
  const style = doc.createElement("style");
  style.dataset.textLookup = "underline";
  style.textContent = `::highlight(${NAME}){text-decoration:underline;text-decoration-thickness:1px;text-underline-offset:0.15em;text-decoration-color:var(--accent);}`;
  doc.head.appendChild(style);

  let words: string[] = [];
  let generation = 0;
  const byNode = new Map<Text, AbstractRange[]>();
  let pending: Text[] = [];
  let walker: TreeWalker | null = null;
  let timer: ReturnType<typeof scope.setTimeout> | 0 = 0;
  let frame = 0;
  let observing = false;

  const forget = (node: Text) => {
    const old = byNode.get(node);
    if (!old) return;
    for (const range of old) paint.delete(range);
    byNode.delete(node);
  };

  const paintNode = (node: Text) => {
    forget(node);
    if (!node.isConnected || !shown(node)) return;
    const ranges = savedWordRanges(node, words, { left: 1 });
    if (!ranges.length) return;
    byNode.set(node, ranges);
    for (const range of ranges) paint.add(range);
  };

  const pump = () => {
    frame = 0;
    const mine = generation;
    let left = NODES_PER_TURN;
    while (pending.length && left > 0) {
      paintNode(pending.shift()!);
      left--;
    }
    if (!walker && doc.body && words.length) walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT);
    while (walker && left > 0) {
      const next = walker.nextNode();
      if (!next) {
        walker = null;
        break;
      }
      paintNode(next as Text);
      left--;
    }
    if (mine !== generation) return;
    if (pending.length || walker) frame = scope.requestAnimationFrame(pump);
  };

  const kick = () => {
    if (timer) scope.clearTimeout(timer);
    timer = scope.setTimeout(() => {
      timer = 0;
      if (!frame) frame = scope.requestAnimationFrame(pump);
    }, DEBOUNCE_MS);
  };

  const textsIn = (node: Node): Text[] => {
    if (node.nodeType === Node.TEXT_NODE) return [node as Text];
    if (node.nodeType !== Node.ELEMENT_NODE) return [];
    const out: Text[] = [];
    const walk = doc.createTreeWalker(node, NodeFilter.SHOW_TEXT);
    let current: Node | null;
    while ((current = walk.nextNode())) out.push(current as Text);
    return out;
  };

  const observer = new MutationObserver((records) => {
    const extra: Text[] = [];
    for (const record of records) {
      if (record.type === "characterData" && record.target.nodeType === Node.TEXT_NODE) extra.push(record.target as Text);
      record.addedNodes.forEach((node) => extra.push(...textsIn(node)));
    }
    for (const node of byNode.keys()) if (!node.isConnected) forget(node);
    if (!extra.length) return;
    pending.push(...extra);
    kick();
  });

  const watch = () => {
    if (observing || !doc.body) return;
    observer.observe(doc.body, { subtree: true, childList: true, characterData: true });
    observing = true;
  };

  return {
    update(next) {
      generation++;
      words = [...next];
      for (const node of byNode.keys()) forget(node);
      pending = [];
      walker = null;
      if (!words.length || !doc.body) return;
      watch();
      kick();
    },
    stop() {
      generation++;
      if (timer) scope.clearTimeout(timer);
      if (frame) scope.cancelAnimationFrame(frame);
      observer.disconnect();
      observing = false;
      for (const node of [...byNode.keys()]) forget(node);
      css.highlights.delete(NAME);
      style.remove();
    },
  };
}
