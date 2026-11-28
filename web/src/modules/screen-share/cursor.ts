// Keeps the web live screen's mouse cursor visible and releases noVNC's capture overlay (#17828).
//
// noVNC 1.7 initializes its canvas to `style.cursor = "none"` and only replaces it on a remote PseudoCursor (-239) or
// when `showDotCursor` is true (rendering a 3x3 dot on desktop, and still leaving `style.cursor = "none"` when touch
// `useFallback` is active). macOS Screen Sharing never sends PseudoCursor, so without a guard the local pointer stays
// hidden or shrinks to an almost invisible 3x3 dot. noVNC's `events.js` also mounts a full-viewport
// `#noVNC_mouse_capture_elem` on `mousedown` and never calls `releaseCapture()` on disconnect or window blur.

const CAPTURE_PROXY_ID = "noVNC_mouse_capture_elem";
const MIN_REMOTE_CURSOR_PX = 4;

// 11x11 high-contrast ringed dot PNG (white outer ring, black inner ring, white center; hotspot at 5,5) so the local
// pointer reads clearly on both dark terminals and light windows without obscuring text under the tip.
const VISIBLE_DOT_PNG =
  "iVBORw0KGgoAAAANSUhEUgAAAAsAAAALCAYAAACprHcmAAAAMklEQVR42mNgQAP/kQADLvAfD8BnGhxj1YBNIboGrAqx2QLn004xSW4mOTRICmdiYxAA2GwPAH/+4/gAAAAASUVORK5CYII=";

export const VISIBLE_DOT_CURSOR = `url("data:image/png;base64,${VISIBLE_DOT_PNG}") 5 5, default`;

/** Reads IHDR width/height from a CSS `url("data:image/png;base64,...")` cursor value, or null if not a valid PNG URL. */
export function pngCursorSize(cursor: string): { w: number; h: number } | null {
  const m = /data:image\/png;base64,([A-Za-z0-9+/=]+)/.exec(cursor);
  if (!m) return null;
  try {
    // Bytes 16..23 hold IHDR width & height (big-endian uint32); 32 base64 chars decode to 24 bytes.
    const bin = atob(m[1].slice(0, 32));
    if (bin.length < 24) return null;
    if (bin.charCodeAt(0) !== 0x89 || bin.charCodeAt(1) !== 0x50 || bin.charCodeAt(2) !== 0x4e || bin.charCodeAt(3) !== 0x47) return null;
    const u32 = (i: number) =>
      ((bin.charCodeAt(i) << 24) | (bin.charCodeAt(i + 1) << 16) | (bin.charCodeAt(i + 2) << 8) | bin.charCodeAt(i + 3)) >>> 0;
    return { w: u32(16), h: u32(20) };
  } catch {
    return null;
  }
}

/** True when a CSS cursor value hides the pointer, is unset, uses an unusable data URI, or is noVNC's tiny 3x3 dot. */
export function needsVisibleCursor(cursor: string): boolean {
  const c = cursor.trim();
  if (!c || c === "none") return true;
  if (c.startsWith("url(")) {
    const size = pngCursorSize(c);
    if (!size) return /data:,/.test(c);
    return Math.max(size.w, size.h) <= MIN_REMOTE_CURSOR_PX;
  }
  return false;
}

/** Replaces hidden or tiny-dot cursor values on `el` with a clearly visible fallback, falling back to `"default"` when
 *  the host CSS engine rejects data-URI hotspot cursors. */
export function ensureVisibleCursor(el: HTMLElement): void {
  const inline = el.style.cursor;
  const computed = typeof getComputedStyle === "function" ? getComputedStyle(el).cursor : "";
  if (!needsVisibleCursor(inline) && computed !== "none") return;
  if (inline !== VISIBLE_DOT_CURSOR) el.style.cursor = VISIBLE_DOT_CURSOR;
  // Engines that do not support `url(...) x y, default` (e.g. jsdom or restricted WebViews) ignore the assignment and
  // keep the previous value ("none" or ""). Fall back to the standard keyword so computed cursor is never "none".
  const afterInline = el.style.cursor;
  const afterComputed = typeof getComputedStyle === "function" ? getComputedStyle(el).cursor : "";
  if (needsVisibleCursor(afterInline) || afterComputed === "none") {
    if (el.style.cursor !== "default") el.style.cursor = "default";
  }
}

/** Watches noVNC's canvas (and its `#noVNC_mouse_capture_elem` drag proxy when mounted) so neither ever stays at
 *  `cursor: none` or a tiny 3x3 dot, while preserving real remote PseudoCursor bitmaps. Returns a cleanup function. */
export function guardScreenCursor(canvas: HTMLCanvasElement): () => void {
  let proxyObserved: HTMLElement | null = null;
  const proxyObserver =
    typeof MutationObserver === "undefined"
      ? null
      : new MutationObserver(() => {
          if (proxyObserved && proxyObserved.style.display !== "none") ensureVisibleCursor(proxyObserved);
        });

  const guardProxy = () => {
    const proxy = document.getElementById(CAPTURE_PROXY_ID);
    if (!proxy) return;
    if (proxy !== proxyObserved) {
      proxyObserver?.disconnect();
      proxyObserved = proxy;
      proxyObserver?.observe(proxy, { attributes: true, attributeFilter: ["style"] });
    }
    if (proxy.style.display !== "none") ensureVisibleCursor(proxy);
  };

  const sync = () => {
    ensureVisibleCursor(canvas);
    guardProxy();
  };

  sync();
  const canvasObserver = typeof MutationObserver === "undefined" ? null : new MutationObserver(sync);
  canvasObserver?.observe(canvas, { attributes: true, attributeFilter: ["style"] });
  // Runs right after noVNC's own mousedown handler calls `setCapture(this._canvas)` on the same canvas element.
  canvas.addEventListener("mousedown", guardProxy);

  return () => {
    canvas.removeEventListener("mousedown", guardProxy);
    canvasObserver?.disconnect();
    proxyObserver?.disconnect();
    proxyObserved = null;
  };
}

/** Releases noVNC's `#noVNC_mouse_capture_elem` full-viewport overlay through its own `mouseup` release path so a
 *  disconnect, blur, hidden tab, or switch to View only never leaves an invisible z-index:10000 layer over the page. */
export function releaseNoVNCCapture(): void {
  const doc = document as Document & { releaseCapture?: () => void };
  if (typeof doc.releaseCapture === "function") {
    try {
      doc.releaseCapture();
    } catch {
      // ignore if no element currently holds native capture
    }
  }
  const proxy = document.getElementById(CAPTURE_PROXY_ID);
  if (proxy && proxy.style.display !== "none") {
    proxy.dispatchEvent(new MouseEvent("mouseup", { bubbles: false, cancelable: true, buttons: 0 }));
    if (proxy.style.display !== "none") proxy.style.display = "none";
  }
}
