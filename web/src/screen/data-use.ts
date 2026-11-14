// How much data the live screen's video may use in the browser (#6558), as the iPhone's ScreenVideoBudget and
// ScreenMobileData (#6396). The cap goes to the helper in `start.max_kbps` and, when the network changes, in an `update`;
// the helper's own adaptation works under it.
//
// Without quality tiers (a helper without them, or a page that does not drive them: video.ts webTiers), the caps
// are the iPhone's: 1.5 Mbit/s with Data Saver on (`navigator.connection.saveData`, the browser's Low Data Mode), 4 on
// mobile data, 12 otherwise. With tiers, mobile data follows the user's choice, as on the iPhone: Clearer keeps
// 4 Mbit/s; Data Saver (the default until they choose) is 1.5 and at most one pixel per point (`maxScale`, which the
// tier policy applies). The browser's Data Saver is always Data Saver.
//
// A browser can't always tell mobile data apart: `navigator.connection` exists in Chrome and Edge but not in Safari or
// Firefox. `type: "cellular"` counts as mobile data; with no answer, a phone's browser (a coarse pointer and a narrow
// screen) gets the mobile data cap and nothing is asked, and any other browser gets the Wi-Fi cap.
//
// The choice is kept in this browser for every computer, as the iPhone keeps it on the device; the question (the card,
// VideoPicture's MobileDataCard) is asked at most twice while nothing has been chosen.

export const WIFI_KBPS = 12_000;
export const MOBILE_KBPS = 4_000;
export const SAVER_KBPS = 1_500;
export const FPS = 60;

/** What the browser says about the network it is on. `cellular` null: it doesn't say. */
export type Network = { saveData: boolean; cellular: boolean | null; phone: boolean };
/** The user's choice for mobile data. */
export type MobileData = "clearer" | "saver";
/** The cap on the video channel: bitrate, frame rate, and (Data Saver with tiers) the most pixels per point. */
export type Budget = { maxKbps: number; fps: number; maxScale?: number };

type Connection = EventTarget & { type?: string; saveData?: boolean };
type Env = { connection?: Connection | null; coarse: boolean; shortSide: number };

function defaultEnv(): Env {
  const nav = typeof navigator === "undefined" ? undefined : (navigator as Navigator & { connection?: Connection });
  const coarse = typeof matchMedia === "function" && matchMedia("(pointer: coarse)").matches;
  const s = typeof screen === "undefined" ? undefined : screen;
  return { connection: nav?.connection ?? null, coarse, shortSide: s ? Math.min(s.width, s.height) : Infinity };
}

/** The network as this browser tells it. A phone: a coarse pointer and a screen no wider than 600 CSS pixels on its
 *  short side (every phone; no tablet). */
export function readNetwork(env: Env = defaultEnv()): Network {
  const c = env.connection;
  const type = typeof c?.type === "string" ? c.type : null;
  return {
    saveData: c?.saveData === true,
    // "unknown" is no answer; any other type is an answer (wifi, ethernet, …: not mobile data)
    cellular: type === null || type === "unknown" ? null : type === "cellular",
    phone: env.coarse && env.shortSide <= 600,
  };
}

/** Calls `onChange` when the browser says the network changed; the returned function stops. Nothing where the browser
 *  says nothing. */
export function watchNetwork(onChange: () => void, env: Env = defaultEnv()): () => void {
  const c = env.connection;
  if (!c || typeof c.addEventListener !== "function") return () => {};
  c.addEventListener("change", onChange);
  return () => c.removeEventListener("change", onChange);
}

/** On mobile data as far as the browser says, with its Data Saver off: where the question and the choice apply. */
export function onMobileData(net: Network): boolean {
  return net.cellular === true && !net.saveData;
}

/** The cap for `net`. `mobileData` is the choice with tiers (`currentMobileData()`), null without them. */
export function budgetOf(net: Network, mobileData: MobileData | null): Budget {
  const mobile = net.cellular === true || (net.cellular === null && net.phone);
  if (mobileData === null) return { maxKbps: net.saveData ? SAVER_KBPS : mobile ? MOBILE_KBPS : WIFI_KBPS, fps: FPS };
  if (net.saveData || (net.cellular === true && mobileData === "saver")) return { maxKbps: SAVER_KBPS, fps: FPS, maxScale: 1 };
  return { maxKbps: mobile ? MOBILE_KBPS : WIFI_KBPS, fps: FPS };
}

export const sameBudget = (a: Budget, b: Budget) => a.maxKbps === b.maxKbps && a.fps === b.fps && a.maxScale === b.maxScale;

// The choice, kept in this browser for every computer. localStorage can throw (private mode, disabled): the page still
// works, starting from Data Saver and asking again next time.
const KEY = "persona.screen.mobileData";
const ASKED_KEY = "persona.screen.mobileData.asked";
/** Times the question goes unanswered before it stops being asked. */
export const ASKS = 2;

const read = (key: string): string | null => {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
};
const write = (key: string, value: string): void => {
  try {
    localStorage.setItem(key, value);
  } catch {
    // not kept: this page still uses it
  }
};

/** What the user chose; null until they do. */
export function chosenMobileData(): MobileData | null {
  const v = read(KEY);
  return v === "clearer" || v === "saver" ? v : null;
}
/** What the picture uses on mobile data: the choice, else Data Saver. */
export function currentMobileData(): MobileData {
  return chosenMobileData() ?? "saver";
}
export function chooseMobileData(m: MobileData): void {
  write(KEY, m);
}
/** Ask on this page: nothing chosen yet, and asked fewer than `ASKS` times. */
export function shouldAskMobileData(): boolean {
  return chosenMobileData() === null && (Number(read(ASKED_KEY)) || 0) < ASKS;
}
/** The question was shown (an answer counts as one too; choosing makes it moot). */
export function markMobileDataAsked(): void {
  write(ASKED_KEY, String((Number(read(ASKED_KEY)) || 0) + 1));
}
