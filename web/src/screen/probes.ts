// What the live screen measures for its connection-stats overlay (#6558, docs/extensions/computer-use.md "Connection stats
// on the web"), each one a hook that the overlay installs while it is shown and takes off when it goes, so a page without
// the overlay measures nothing and sends nothing extra. ./meter turns what they hear into the overlay's reading.
//
// - Bytes: the page opens the RFB WebSocket itself and hands it to noVNC, so it can listen beside noVNC on the socket.
// - Frames: noVNC has no public frame event. Its display's flip() puts each finished framebuffer update on the screen, at
//   once or, while images still decode, from its render queue; the hook counts the flips that draw. A noVNC whose display
//   does not look like 1.7's is not hooked, and the frame rate is left out rather than guessed.
// - Round trip to the hub: the screen page holds no hub connection of its own (main.tsx loads only ScreenView here), and
//   another /api/v1/ws would count as one more connection of this device on the hub. So the page times GET
//   /api/v1/health, which needs no sign-in and reads only the hub's memory: from the request going out to the first
//   byte back, by its own Resource Timing entry. That is the browser's HTTP round trip to the hub, a little of it the
//   hub answering; not the hub's way on to the Mac.
import { BASE } from "../lib/base";

/** Calls `on` with the size of every message the socket receives; the returned function stops. */
export function countBytes(socket: WebSocket, on: (n: number) => void): () => void {
  const heard = (e: MessageEvent) => {
    const d: unknown = e.data;
    if (d instanceof ArrayBuffer) on(d.byteLength);
    else if (ArrayBuffer.isView(d)) on(d.byteLength);
    else if (typeof Blob !== "undefined" && d instanceof Blob) on(d.size);
  };
  socket.addEventListener("message", heard);
  return () => socket.removeEventListener("message", heard);
}

/** The part of noVNC 1.7's Display (core/display.js) the frame hook relies on. */
type Display = { flip: (fromQueue?: boolean) => unknown; _renderQ: unknown[] };

function knownDisplay(d: unknown): d is Display {
  return !!d && typeof (d as Display).flip === "function" && Array.isArray((d as Display)._renderQ);
}

/** Calls `on` for each frame noVNC draws to the screen; the returned function stops. Null when this noVNC's display is
 *  not the one the hook knows: then nothing is hooked and no frame rate is shown. */
export function countFrames(rfb: object, on: () => void): (() => void) | null {
  const display = (rfb as { _display?: unknown })._display;
  if (!knownDisplay(display)) return null;
  const own = Object.prototype.hasOwnProperty.call(display, "flip");
  const flip = display.flip;
  const counted = function (this: Display, fromQueue?: boolean) {
    // flip() with images still decoding only queues the flip; the queue calls flip(true) when it draws.
    const draws = fromQueue === true || this._renderQ.length === 0;
    const r = flip.call(this, fromQueue);
    if (draws) on();
    return r;
  };
  display.flip = counted;
  return () => {
    if (display.flip !== counted) return; // someone else replaced it since: theirs stays
    if (own) display.flip = flip;
    else delete (display as Partial<Display>).flip;
  };
}

/** How often the hub is probed while the overlay is shown and the page is in front. */
export const PROBE_EVERY_MS = 2000;
/** A probe that has not finished by then is given up (and not counted). */
const PROBE_TIMEOUT_MS = 5000;

/** Times GET /api/v1/health every PROBE_EVERY_MS while the page is visible, calling `on` with each round trip in ms; the
 *  returned function stops. A probe counts only when the hub answered it (200, `{"ok":true}`) and its own Resource
 *  Timing entry has both requestStart and responseStart; anything else is skipped, never estimated. A browser without
 *  resource entries for observers probes nothing. */
export function probeHub(on: (ms: number) => void, origin: string = window.location.origin): () => void {
  if (typeof PerformanceObserver === "undefined" || !PerformanceObserver.supportedEntryTypes?.includes("resource")) return () => {};
  type Pending = { at: number; ok?: boolean; ms?: number | null };
  const pending = new Map<string, Pending>();
  let seq = 0;
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let inFlight = false;

  const settle = (url: string) => {
    const p = pending.get(url);
    if (!p || p.ok === undefined || p.ms === undefined) return; // both halves are needed
    pending.delete(url);
    if (p.ok && p.ms !== null) on(p.ms);
  };
  const seen = new PerformanceObserver((list) => {
    for (const e of list.getEntries()) {
      const p = pending.get(e.name);
      if (!p) continue;
      const t = e as PerformanceResourceTiming;
      p.ms = t.requestStart > 0 && t.responseStart >= t.requestStart ? t.responseStart - t.requestStart : null;
      settle(e.name);
    }
  });
  seen.observe({ type: "resource" });

  const probe = async () => {
    if (stopped || inFlight) return; // one at a time

    inFlight = true;
    const url = `${origin}${BASE}/api/v1/health?probe=${++seq}`;
    const p: Pending = { at: performance.now() };
    pending.set(url, p);
    const ask = new AbortController();
    const late = setTimeout(() => ask.abort(), PROBE_TIMEOUT_MS);
    try {
      // no-store: an answer from a cache would time nothing; manual: a sign-in layer's redirect is not the hub.
      const res = await fetch(url, { cache: "no-store", redirect: "manual", signal: ask.signal });
      const body = (await res.json().catch(() => null)) as { ok?: unknown } | null;
      p.ok = res.ok && body?.ok === true;
    } catch {
      p.ok = false;
    } finally {
      clearTimeout(late);
      inFlight = false;
    }
    settle(url);
    // An entry that never came (a full buffer does not stop observers, but a browser may skip it): given up later.
    const now = performance.now();
    for (const [u, q] of pending) if (now - q.at > PROBE_TIMEOUT_MS * 2) pending.delete(u);
  };
  // Probing runs only while the page is in front: hidden, the next probe is not scheduled; back, one goes at once.
  const tick = () => {
    timer = undefined;
    if (stopped || document.visibilityState !== "visible") return;
    void probe();
    timer = setTimeout(tick, PROBE_EVERY_MS);
  };
  const shown = () => {
    if (document.visibilityState !== "visible") {
      clearTimeout(timer);
      timer = undefined;
    } else if (timer === undefined) tick();
  };
  document.addEventListener("visibilitychange", shown);
  tick();
  return () => {
    stopped = true;
    clearTimeout(timer);
    seen.disconnect();
    document.removeEventListener("visibilitychange", shown);
    pending.clear();
  };
}
