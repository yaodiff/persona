// The DOM side of the attention signals (#2332, logic in lib/attention.ts): this first-screen shell handles the tab
// title, favicon badge, desktop Dock badge, synchronous user-gesture audio unlock, and early `ws_notify` / baseline
// continuity immediately, while loading the heavy background notification, desktop presence, Web Push, and Service
// Worker reply runtime asynchronously (lib/attention-signals-runtime.ts). `<AttentionSignals>` is what App renders.
import { useEffect, useRef } from "react";
import { attentionSoundEnabled, badgeText, cardArrivalKey, onAttentionSoundChange, tabTitle } from "./attention";
import { homeKey } from "./folded-rows";
import { activityOf, stateOf } from "./format";
import { agentKey } from "./pins";
import { useAgentSessions, useCounts } from "./sessions";
import { onHello, onHubEvent, sayHello, setHelloExtras, useStore } from "./store";
import type { AttentionRuntime, AttentionSnapshot } from "./attention-signals-runtime";
import type { Interaction, Session } from "./types";

// ── favicon ──────────────────────────────────────────────

const ICONS = 'link[rel="icon"]';
const SIZE = 64; // the icon files are 64 px; the browser scales the tab's down
const decoded = new Map<string, Promise<HTMLImageElement>>();
const drawn = new Map<string, string>();

function load(src: string): Promise<HTMLImageElement> {
  let p = decoded.get(src);
  if (!p) {
    p = new Promise((resolve, reject) => {
      const img = new Image();
      img.onload = () => resolve(img);
      img.onerror = () => reject(new Error("icon"));
      img.src = src;
    });
    decoded.set(src, p);
    p.catch(() => decoded.delete(src));
  }
  return p;
}

/** The icon with the "Needs you" badge at its top right: a red disc (the sidebar's --status-needs) with the count. */
async function badged(src: string, text: string): Promise<string> {
  const key = `${src}|${text}`;
  const hit = drawn.get(key);
  if (hit) return hit;
  const img = await load(src);
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = SIZE;
  const g = canvas.getContext("2d");
  if (!g) throw new Error("canvas");
  g.drawImage(img, 0, 0, SIZE, SIZE);
  const css = getComputedStyle(document.documentElement);
  const r = SIZE * 0.34;
  const cx = SIZE - r;
  const cy = r;
  g.beginPath();
  g.arc(cx, cy, r, 0, Math.PI * 2);
  g.fillStyle = css.getPropertyValue("--status-needs").trim() || css.getPropertyValue("--red").trim() || "red";
  g.fill();
  g.lineWidth = SIZE * 0.05;
  g.strokeStyle = css.getPropertyValue("--surface").trim() || "white";
  g.stroke();
  g.fillStyle = "white";
  g.font = `700 ${text.length > 1 ? r * 1.05 : r * 1.35}px system-ui, sans-serif`;
  g.textAlign = "center";
  g.textBaseline = "middle";
  g.fillText(text, cx, cy + r * 0.06);
  const url = canvas.toDataURL("image/png");
  drawn.set(key, url);
  return url;
}

let faviconSeq = 0;
let faviconNeeds = 0;

/** The look's icon (lib/look-icon.ts, #3200) under the badge: each link's original becomes the look's picture for its
 *  scheme (index.html's light and dark links), or the files index.html names again with null. */
export function setFaviconBase(icon: { light: string; dark: string } | null): void {
  for (const link of document.querySelectorAll<HTMLLinkElement>(ICONS)) {
    link.dataset.file ??= link.dataset.original ?? link.getAttribute("href") ?? "";
    const dark = (link.getAttribute("media") ?? "").includes("dark");
    link.dataset.original = icon ? (dark ? icon.dark : icon.light) : link.dataset.file;
  }
  setFaviconBadge(faviconNeeds);
}

/** Swaps the page's icon links (index.html has a light and a dark one, chosen by the browser's media query) for the
 *  badged copy of each, or back to the originals at 0. The files themselves come through the service worker like any
 *  other icon (lib/sw-routes.ts "static": the hub's, else the cached one), so this also works offline; the drawn
 *  copies are data: URLs and never touch the network or the cache. */
export function setFaviconBadge(needs: number): void {
  faviconNeeds = needs;
  const links = [...document.querySelectorAll<HTMLLinkElement>(ICONS)];
  const text = badgeText(needs);
  const seq = ++faviconSeq;
  for (const link of links) {
    link.dataset.original ??= link.getAttribute("href") ?? "";
    const original = link.dataset.original;
    if (!text) {
      link.setAttribute("href", original);
      continue;
    }
    badged(original, text).then(
      (url) => seq === faviconSeq && link.setAttribute("href", url),
      () => seq === faviconSeq && link.setAttribute("href", original), // the icon can't be read: leave it as it is
    );
  }
}

// ── synchronous audio unlock ─────────────────────────────

let audioCtx: AudioContext | undefined;
let audioGestured = false;

const GESTURES = ["pointerdown", "keydown", "touchend"] as const;

/** Listens for the gesture that starts audio, or stops listening (adding the same listener twice adds it once). */
function listenAudio(on: boolean) {
  for (const ev of GESTURES) window[on ? "addEventListener" : "removeEventListener"](ev, unlockAudio, { capture: true });
}

function unlockAudio() {
  audioGestured = true;
  listenAudio(false);
  try {
    audioCtx ??= new AudioContext();
    void audioCtx.resume();
  } catch {
    // no WebAudio here: silent
  }
}

/** Returns the unlocked, running AudioContext shared with the lazy runtime, or undefined while silent/locked. */
function readyAudioContext(): AudioContext | undefined {
  return audioGestured && audioCtx && audioCtx.state === "running" ? audioCtx : undefined;
}

/**
 * Browsers only start audio after a user gesture: until one has happened nothing plays (and nothing is queued). Making
 * the AudioContext takes 80–130 ms of the gesture it runs in, so it is made only for the alert sound, which is off by
 * default: on the first gesture once it is on, or at once when it is switched on by a click or a key (Settings ›
 * Notifications), which is a gesture itself. With it off, the page's first key or click pays nothing. Returns the
 * cleanup. Stays in the first-screen shell so a user's sole gesture during cold load is never missed.
 */
export function armAudio(): () => void {
  if (typeof window === "undefined") return () => {};
  const follow = () => {
    const want = attentionSoundEnabled() && audioCtx?.state !== "running";
    if (want && navigator.userActivation?.isActive) unlockAudio();
    else listenAudio(want);
  };
  follow();
  const off = onAttentionSoundChange(follow);
  return () => {
    off();
    listenAudio(false);
  };
}

// ── lazy background runtime ──────────────────────────────

type AttentionRuntimeModule = typeof import("./attention-signals-runtime");

let loadedRuntime: AttentionRuntimeModule | null = null;
let runtimePromise: Promise<AttentionRuntimeModule> | null = null;

function loadAttentionRuntime(): Promise<AttentionRuntimeModule> {
  if (loadedRuntime) return Promise.resolve(loadedRuntime);
  return (runtimePromise ??= import("./attention-signals-runtime").then(
    (mod) => {
      loadedRuntime = mod;
      return mod;
    },
    (err) => {
      runtimePromise = null;
      throw err;
    },
  ));
}

/** Loads the background attention-signals runtime chunk if not already loaded. */
export function preloadAttentionSignals(): Promise<void> {
  return loadAttentionRuntime().then(() => undefined);
}

/** Plays the alert chime or soft completion note if audio is armed and running. */
export function play(kind: "needs" | "finished"): void {
  const ctx = readyAudioContext();
  if (!ctx) return;
  if (loadedRuntime) {
    loadedRuntime.playOnContext(ctx, kind);
    return;
  }
  void loadAttentionRuntime().then((mod) => mod.playOnContext(readyAudioContext(), kind), () => {});
}

// ── cold-window continuity helpers ───────────────────────

/** Mirrors lib/notify.ts systemNotifyActive without importing notify.ts on the first screen, so the initial
 *  client_hello declares `ws_notify` before the lazy runtime arrives (preventing hub/notifyring.go from dropping
 *  the device's ring during cold load). */
function systemNotifyReady(): boolean {
  const desktop = typeof window !== "undefined" ? window.personaDesktop : undefined;
  if (desktop?.desktopPresence?.().notifies === false) return false;
  try {
    if (localStorage.getItem("persona.systemNotify") !== "1") return false;
  } catch {
    return false;
  }
  const perm = desktop ? desktop.notificationPermission() : typeof Notification === "undefined" ? "unsupported" : Notification.permission;
  return perm === "granted";
}

/** Startup silence window for hub replay bursts right after a `ws_notify` hello (matches runtime HUB_REPLAY_MS). */
const HUB_REPLAY_MS = 3_000;
const MAX_EARLY_NOTICES = 64;
const MAX_BASELINE_PRUNE = 256;

interface ColdBuffer {
  count: number;
  firstPane: { mid: string; sid: string } | undefined;
  firstPushGrading: boolean;
  /** Per-agentKey baseline session row so a new session's working/non-waiting baseline is never lost under high churn. */
  baselineSessions: Map<string, Session>;
  /** Cards present on initial open (or when a brand-new session first appeared already waiting). */
  baselineInteractions: Interaction[];
  baselineInteractionIds: Set<string>;
  latest: AttentionSnapshot;
}

const stampWorking = (s: Session, now: number): Session =>
  stateOf(s) === "working" && !s.state_since ? { ...s, state_since: new Date(now).toISOString() } : s;

function recordColdSnapshot(buf: ColdBuffer | null, snap: AttentionSnapshot, now: number): ColdBuffer {
  if (!buf) {
    const baselineSessions = new Map<string, Session>();
    for (const s of snap.sessions) {
      if (s.hidden_by_directory) continue;
      baselineSessions.set(agentKey(s), stampWorking(s, now));
    }
    return {
      count: 1,
      firstPane: snap.pane,
      firstPushGrading: snap.pushGrading,
      baselineSessions,
      baselineInteractions: [...snap.interactions],
      baselineInteractionIds: new Set(snap.interactions.map(cardArrivalKey)),
      latest: snap,
    };
  }
  buf.count++;
  buf.latest = snap;
  const liveKeys = new Set<string>();
  for (const s of snap.sessions) {
    if (s.hidden_by_directory) continue;
    const k = agentKey(s);
    liveKeys.add(k);
    const st = activityOf(s);
    const prev = buf.baselineSessions.get(k);
    if (!prev) {
      buf.baselineSessions.set(k, stampWorking(s, now));
      // A brand-new session first appearing already waiting should not treat its initial cards as newly added cards.
      if (st === "needs_you") {
        for (const ix of snap.interactions) {
          const arrivalKey = cardArrivalKey(ix);
          if (ix.session_id !== "" && homeKey(ix) === k && !buf.baselineInteractionIds.has(arrivalKey)) {
            buf.baselineInteractionIds.add(arrivalKey);
            buf.baselineInteractions.push(ix);
          }
        }
      }
    } else {
      const prevSt = activityOf(prev);
      // Preserve intermediate working/non-waiting baselines so idle -> working -> unread and
      // needs_you -> working -> needs_you transitions survive arbitrary snapshot churn before runtime loads.
      if (st === "working" && prevSt !== "working") {
        buf.baselineSessions.set(k, stampWorking(s, now));
      } else if (prevSt === "needs_you" && st !== "needs_you") {
        buf.baselineSessions.set(k, s);
      }
    }
  }
  if (buf.baselineSessions.size > MAX_BASELINE_PRUNE) {
    for (const k of buf.baselineSessions.keys()) {
      if (!liveKeys.has(k)) buf.baselineSessions.delete(k);
    }
  }
  return buf;
}

// ── the hook ─────────────────────────────────────────────

/**
 * The tab title (`label` is the page's label, as App works it out), the favicon badge, the sounds and the system
 * notifications. `pane` is the agent conversation on the page, if any: it is not announced while the window is focused.
 * Sounds and notifications follow this browser's Settings › Notifications (lib/notify.ts): "Needs you" by its switch,
 * "Done" by its three choices, a turn's length as the page saw it (trackTurns).
 */
export function useAttentionSignals(label: string, pane: { mid: string; sid: string } | undefined): void {
  const needs = useCounts().badge;
  useEffect(() => {
    document.title = tabTitle(label, needs);
  }, [label, needs]);
  useEffect(() => {
    setFaviconBadge(needs);
    window.personaDesktop?.setBadge(needs);
  }, [needs]);

  // Arm gesture-driven AudioContext unlock synchronously so a first-paint click or key is never missed.
  useEffect(armAudio, []);

  const sessions = useAgentSessions();
  const loaded = useStore((s) => s.loaded);
  const interactions = useStore((s) => s.interactions);
  const pushGrading = Boolean(useStore((s) => s.pushGrading));

  const latest = useRef<AttentionSnapshot>({ pane, sessions, interactions, loaded, pushGrading });
  latest.current = { pane, sessions, interactions, loaded, pushGrading };
  const runtime = useRef<AttentionRuntime | null>(null);
  const coldBuffer = useRef<ColdBuffer | null>(null);
  const startRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    let alive = true;
    let loading = false;
    let instance: AttentionRuntime | null = null;
    let sawHello = false;
    let earlyQuietUntil = 0;
    let earlyAsking: boolean | undefined;
    const earlyNotices: { msg: Record<string, unknown>; at: number }[] = [];
    let offEarlyHello: (() => void) | undefined;
    let offEarlyNotify: (() => void) | undefined;

    const clearEarly = () => {
      offEarlyHello?.();
      offEarlyHello = undefined;
      offEarlyNotify?.();
      offEarlyNotify = undefined;
    };

    const mount = (mod: AttentionRuntimeModule, carryHubNotify: boolean) => {
      clearEarly();
      const buf = coldBuffer.current;
      coldBuffer.current = null;
      const hubNotify = carryHubNotify ? { asking: earlyAsking, quietUntil: earlyQuietUntil } : undefined;
      if (buf && buf.count > 1) {
        const baseline: AttentionSnapshot = {
          pane: buf.firstPane,
          sessions: [...buf.baselineSessions.values()],
          interactions: buf.baselineInteractions,
          loaded: true,
          pushGrading: buf.firstPushGrading,
        };
        instance = mod.createAttentionRuntime(baseline, { sawHello, getAudioContext: readyAudioContext, hubNotify });
        instance.setPane(buf.latest.pane);
        instance.syncLoaded(buf.latest.loaded);
        instance.update(buf.latest.sessions, buf.latest.interactions, buf.latest.loaded, buf.latest.pushGrading);
        instance.setPane(latest.current.pane);
      } else {
        instance = mod.createAttentionRuntime(buf ? buf.latest : latest.current, { sawHello, getAudioContext: readyAudioContext, hubNotify });
      }
      runtime.current = instance;
      if (carryHubNotify && earlyNotices.length) {
        const queued = earlyNotices.splice(0);
        for (const entry of queued) instance.replayHubNotify(entry.msg, entry.at);
      }
    };

    const start = () => {
      if (!alive || instance || loading) return;
      if (loadedRuntime) {
        mount(loadedRuntime, offEarlyNotify !== undefined);
        return;
      }
      if (!offEarlyHello) {
        offEarlyHello = onHello(() => {
          sawHello = true;
          if (!instance && !loading) start();
        });
      }
      if (!offEarlyNotify) {
        const earlyAble = () => attentionSoundEnabled() || systemNotifyReady();
        setHelloExtras(() => {
          const on = earlyAble();
          if (on && !earlyAsking) earlyQuietUntil = Date.now() + HUB_REPLAY_MS;
          earlyAsking = on;
          return on ? ["ws_notify"] : [];
        });
        if (earlyAble() !== Boolean(earlyAsking)) sayHello();
        offEarlyNotify = onHubEvent("notify", (msg) => {
          const at = Date.now();
          if (earlyQuietUntil > 0 && at < earlyQuietUntil) return;
          if (earlyNotices.length >= MAX_EARLY_NOTICES) earlyNotices.shift();
          earlyNotices.push({ msg, at });
        });
      }
      loading = true;
      void loadAttentionRuntime().then(
        (mod) => {
          loading = false;
          if (!alive) return;
          mount(mod, true);
        },
        () => {
          loading = false;
          coldBuffer.current = null;
        },
      );
    };
    startRef.current = start;
    start();
    return () => {
      alive = false;
      startRef.current = null;
      coldBuffer.current = null;
      earlyNotices.length = 0;
      const hadEarly = offEarlyNotify !== undefined;
      clearEarly();
      if (instance) {
        if (runtime.current === instance) runtime.current = null;
        instance.dispose();
        instance = null;
      } else if (hadEarly) {
        setHelloExtras(() => []);
      }
    };
  }, []);

  useEffect(() => {
    runtime.current?.setPane(pane);
  }, [pane]);

  useEffect(() => {
    if (loaded && !runtime.current) startRef.current?.();
    runtime.current?.syncLoaded(loaded);
  }, [loaded]);

  useEffect(() => {
    if (!runtime.current) {
      if (!loaded) {
        coldBuffer.current = null;
        return;
      }
      const snap: AttentionSnapshot = { pane: latest.current.pane, sessions, interactions, loaded, pushGrading };
      coldBuffer.current = recordColdSnapshot(coldBuffer.current, snap, Date.now());
      return;
    }
    runtime.current.update(sessions, interactions, loaded, pushGrading);
  }, [sessions, interactions, loaded, pushGrading]);
}

/**
 * `useAttentionSignals` as a component that draws nothing. It reads every session push (the "Needs you" count, the
 * transitions): rendered as a leaf, a push re-renders only it, not the shell with the sidebar and the page around it.
 */
export function AttentionSignals({ label, pane }: { label: string; pane: { mid: string; sid: string } | undefined }): null {
  useAttentionSignals(label, pane);
  return null;
}
