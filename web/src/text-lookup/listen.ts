// One listener for the whole page (#10977). It stays off until the hub says text lookup is available, and the
// popover and the underline load only then.
import {
  bindLookupBridge,
  EXTENSIONS_CHANGED,
  FOCUS_REFRESH_MS,
  lookupSnapshot,
  replaceLookupSnapshot,
  TEXT_LOOKUP_CHANGED,
  TEXT_LOOKUP_FEATURE,
  withWordSaved,
  type LookupApi,
  type LookupState,
} from "./client";
import { DOUBLE_CLICK_MS, lookupBlocked, resolveWord } from "./word";
import type { SavedHighlights } from "./highlight";

export interface LookupOpen {
  word: string;
  context: string;
  rect: { left: number; right: number; top: number; bottom: number };
}

const EMPTY: LookupState = { available: false, underline_saved: false, words: [] };

export function startTextLookup(opts: {
  features: () => readonly string[];
  onHub: (type: string, listener: (msg: Record<string, unknown>) => void) => () => void;
  api: LookupApi;
  open: (hit: LookupOpen) => void;
  doc?: Document;
  /** Tests shorten the double-click wait. */
  delay?: number;
  /** How long a second click still counts as a double-click and cancels the first. */
  doubleClick?: number;
  now?: () => number;
  onWindow?: (type: string, listener: () => void) => () => void;
}): () => void {
  const doc = opts.doc ?? document;
  const delay = opts.delay ?? DOUBLE_CLICK_MS;
  const doubleClick = opts.doubleClick ?? DOUBLE_CLICK_MS;
  const now = opts.now ?? (() => Date.now());
  const view = doc.defaultView ?? window;
  let generation = 0;
  let stopped = false;
  let lastFetch = 0;
  let highlight: SavedHighlights | null = null;
  let hlGen = 0;
  let pointer: AbortController | null = null;
  const offs: (() => void)[] = [];

  const stopPointer = () => {
    pointer?.abort();
    pointer = null;
  };

  const syncHighlight = async (state: LookupState) => {
    const mine = ++hlGen;
    if (!state.available || !state.underline_saved || state.words.length === 0) {
      highlight?.stop();
      highlight = null;
      return;
    }
    const mod = await import("./highlight");
    if (stopped || mine !== hlGen) return;
    if (!mod.highlightSupported(view)) return;
    highlight ??= mod.attachSavedHighlights(doc);
    highlight.update(state.words);
  };

  const apply = (state: LookupState) => {
    replaceLookupSnapshot(state);
    if (!state.available) stopPointer();
    else ensurePointer();
    void syncHighlight(state);
  };

  const refresh = async () => {
    if (stopped) return;
    if (!opts.features().includes(TEXT_LOOKUP_FEATURE)) {
      apply(EMPTY);
      return;
    }
    const mine = ++generation;
    lastFetch = now();
    try {
      const state = await opts.api.state();
      if (stopped || mine !== generation) return;
      apply(state);
    } catch {
      if (stopped || mine !== generation) return;
      if (!lookupSnapshot()?.available) apply(EMPTY);
    }
  };

  const ensurePointer = () => {
    if (pointer) return;
    const ac = new AbortController();
    pointer = ac;
    const signal = ac.signal;
    let down: { x: number; y: number; id: number; moved: boolean } | null = null;
    let pending: ReturnType<typeof setTimeout> | undefined;
    let lastClick = 0;
    const clearPending = () => {
      if (pending) clearTimeout(pending);
      pending = undefined;
    };
    doc.addEventListener("pointerdown", (event) => {
      const e = event as PointerEvent;
      if (e.detail > 1 || now() - lastClick < doubleClick) clearPending();
      if (e.button !== 0 || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.detail > 1) {
        down = null;
        return;
      }
      down = { x: e.clientX, y: e.clientY, id: e.pointerId, moved: false };
    }, { capture: true, signal });
    doc.addEventListener("pointermove", (event) => {
      const e = event as PointerEvent;
      if (!down || e.pointerId !== down.id) return;
      const dx = e.clientX - down.x;
      const dy = e.clientY - down.y;
      if (dx * dx + dy * dy > 25) down.moved = true;
    }, { capture: true, signal });
    doc.addEventListener("pointerup", (event) => {
      const e = event as PointerEvent;
      if (!down || e.pointerId !== down.id) return;
      const dx = e.clientX - down.x;
      const dy = e.clientY - down.y;
      if (dx * dx + dy * dy > 25) down.moved = true;
    }, { capture: true, signal });
    doc.addEventListener("pointercancel", () => {
      down = null;
      clearPending();
    }, { capture: true, signal });
    doc.addEventListener("selectionchange", () => {
      const sel = doc.getSelection();
      if (sel && !sel.isCollapsed) clearPending();
    }, { signal });
    doc.addEventListener("click", (event) => {
      const e = event as MouseEvent;
      const gesture = down;
      down = null;
      lastClick = now();
      if (!gesture || gesture.moved || e.detail !== 1 || e.button !== 0) return;
      if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.target instanceof Node && lookupBlocked(e.target)) return;
      const sel = doc.getSelection();
      if (sel && !sel.isCollapsed) return;
      const x = gesture.x;
      const y = gesture.y;
      clearPending();
      pending = setTimeout(() => {
        pending = undefined;
        if (stopped || !lookupSnapshot()?.available) return;
        const again = doc.getSelection();
        if (again && !again.isCollapsed) return;
        const resolved = resolveWord(doc, x, y);
        if (!resolved) return;
        const rect = resolved.range.getBoundingClientRect();
        opts.open({
          word: resolved.word,
          context: resolved.context,
          rect: rect.width || rect.height
            ? { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom }
            : { left: x, right: x + 1, top: y, bottom: y + 1 },
        });
      }, delay);
    }, { capture: true, signal });
  };

  const onFocus = () => {
    if (doc.visibilityState === "hidden") return;
    if (now() - lastFetch < FOCUS_REFRESH_MS) return;
    void refresh();
  };
  offs.push(opts.onHub(TEXT_LOOKUP_CHANGED, () => void refresh()));
  offs.push(opts.onHub("hello", () => void refresh()));
  const onWindow = opts.onWindow ?? ((type: string, listener: () => void) => {
    view.addEventListener(type, listener);
    return () => view.removeEventListener(type, listener);
  });
  offs.push(onWindow(EXTENSIONS_CHANGED, () => void refresh()));
  offs.push(onWindow("focus", onFocus));
  doc.addEventListener("visibilitychange", onFocus);
  offs.push(() => doc.removeEventListener("visibilitychange", onFocus));

  bindLookupBridge({
    setWordSaved(word, saved) {
      const current = lookupSnapshot();
      if (!current?.available) return;
      const next = withWordSaved(current, word, saved);
      replaceLookupSnapshot(next);
      void syncHighlight(next);
    },
    refresh() {
      void refresh();
    },
  });

  void refresh();
  return () => {
    stopped = true;
    generation++;
    hlGen++;
    stopPointer();
    highlight?.stop();
    highlight = null;
    bindLookupBridge(null);
    replaceLookupSnapshot(null);
    for (const off of offs) off();
  };
}
