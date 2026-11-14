// The live screen's hardware video channel (#3954, docs/extensions/computer-use.md "Live screen video"): the picture
// channel a Mac uses and the connection numbers an overlay can show, beside the VNC/RFB path ScreenView draws today. This
// file is the interface the design leaves now — pure data, no i18n and no UI, so it can be read on the screen page's
// first paint without pulling its words into the first screen's catalog. The switcher, quality bias and overlay (their
// words in screen-video) are a lazily-loaded chunk, as the Displays menu is (DisplayMenu, ./displays). Per-Mac choices
// are kept per Mac in this browser, as the display choice is (./displays), and as the iOS app keeps them on the device.
//
// What the overlay shows comes from the same instrumentation that would measure the stream, so what is shown equals what
// was measured; a number that is not measured is left out (undefined), never guessed. Today the web picture is always
// RFB: the overlay (StatsOverlay, #6558) shows its bitrate and frame rate and the round trip to the hub (./meter,
// ./probes); the video path's segments, `path` (#6398) and `tier` (#6396) are the interfaces the follow-ups fill.

/** Which picture channel a Mac's live screen uses: the hardware **video** channel (smoother, far less bandwidth) or
 *  **VNC/RFB** (always available). Null until the user picks one, so the page can default to video when the computer
 *  advertises `screen_video` and it is granted, else VNC. */
export type ScreenPicture = "video" | "vnc";
/** The quality bias for the video channel (the interface #6396 fills): **auto** is adaptive tiering; the two biases pin
 *  tier selection toward resolution (**sharper**) or frame rate (**smoother**). */
export type ScreenQuality = "auto" | "sharper" | "smoother";
/** Which network path the stream is on (#6398). */
export type ScreenPath = "direct-lan" | "direct-tailscale" | "relay";

/** One reading for the connection-stats overlay, as the iOS `ScreenStats` carries it. VNC mode fills what the client can
 *  measure (round-trip, frame rate, bitrate); video mode adds the per-segment latency and the codec / resolution. A
 *  field that is not measured is left out. */
export interface ScreenStats {
  mode: ScreenPicture;
  path?: ScreenPath;
  tier?: string; // #6396, e.g. "1080p · 60"
  tierReason?: string; // #6396, a short reason shown on a tier change
  bitrateMbps?: number;
  fps?: number;
  codec?: string; // video only, e.g. "HEVC"
  resolution?: string; // video only, e.g. "1920×1080"
  // Per-segment latency (ms), video mode. Capture ≈ 0 on the Mac (gate ①), so it may be left out / folded into total.
  captureMs?: number;
  encodeMs?: number;
  networkMs?: number;
  decodeMs?: number;
  totalMs?: number;
  // VNC mode: what the client can measure.
  roundTripMs?: number;
  /** The browser's HTTP round trip to the hub (#6558, ./probes): not the hub's way on to the Mac, so the overlay names it
   *  and never shows it as the picture's latency. */
  hubRoundTripMs?: number;
}

/** The latency segments present, in pipeline order, for the overlay's "capture · encode · network · decode" line. `key`
 *  names the segment; the overlay turns it into words (screen-video). A segment that is not measured drops out. */
export function latencySegments(s: ScreenStats): { key: "capture" | "encode" | "network" | "decode"; ms: number }[] {
  const order = [
    { key: "capture", ms: s.captureMs },
    { key: "encode", ms: s.encodeMs },
    { key: "network", ms: s.networkMs },
    { key: "decode", ms: s.decodeMs },
  ] as const;
  return order.flatMap((p) => (typeof p.ms === "number" ? [{ key: p.key, ms: p.ms }] : []));
}

// Per-Mac choices, kept per Mac in this browser as the display choice is (./displays). A key per choice; a Mac with no
// choice yet reads null / the default. localStorage can throw (private mode, disabled): the page still works, the choice
// just does not carry to the next open.
const pictureKey = (mid: string) => `persona.screen.picture.${mid}`;
const qualityKey = (mid: string) => `persona.screen.quality.${mid}`;
const statsKey = (mid: string) => `persona.screen.stats.${mid}`;

const read = (key: string): string | null => {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
};
const write = (key: string, value: string | null): void => {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    // not kept: this page still uses it, the next one starts from the default
  }
};

/** Whether this page plays the video channel (#6558). Off for users until the web's video path has been checked on a
 *  real Mac in each browser (#6558's last PR): the page keeps RFB and offers no Picture switcher. A development server
 *  started with VITE_SCREEN_VIDEO=1 turns it on (web/e2e/screen-video.mjs); a production build never does. An object so
 *  the switcher's tests can turn it on. */
export const webVideo = { plays: import.meta.env.DEV && import.meta.env.VITE_SCREEN_VIDEO === "1" };

/** Whether this page's side of the quality tiers drives the stream (#6396, #6558): the page picks the tier
 *  (./video-tiers), and Quality, the mobile-data card and its choice (./data-use) are offered, only when the helper's
 *  `hello` lists `tiers` too, as the iPhone's ScreenTiers. On: the page drives them. An object so tests can turn it off
 *  (a helper with tiers meeting a page without them). Like everything of the video, users see it once webVideo.plays. */
export const webTiers = { supported: true };

/** Whether the page offers the picture switcher: this browser plays video and the Mac advertises it. */
export function videoOffered(screenVideo: unknown): boolean {
  return webVideo.plays && videoAdvertised(screenVideo);
}

/** Whether a computer / browser status advertises the hardware video channel (#3954): `screen_video` present with a
 *  protocol version > 0. The Picture / Quality switcher appears only then, so no Mac shows a control it can't use — the
 *  same test the iOS app makes (AppModel.advertisesVideo). An unreadable value is "not advertised", never a guess. */
export function videoAdvertised(screenVideo: unknown): boolean {
  if (!screenVideo || typeof screenVideo !== "object") return false;
  const v = (screenVideo as { version?: unknown }).version;
  return typeof v === "number" && v > 0;
}

/** The chosen picture channel for a Mac, or null until one is picked (so the page picks the default). */
export function pictureChoice(mid: string): ScreenPicture | null {
  const v = read(pictureKey(mid));
  return v === "video" || v === "vnc" ? v : null;
}
export function setPictureChoice(mid: string, p: ScreenPicture): void {
  write(pictureKey(mid), p);
}

/** The quality bias for a Mac; auto until another is picked. */
export function qualityChoice(mid: string): ScreenQuality {
  const v = read(qualityKey(mid));
  return v === "sharper" || v === "smoother" || v === "auto" ? v : "auto";
}
export function setQualityChoice(mid: string, q: ScreenQuality): void {
  write(qualityKey(mid), q);
}

/** Whether the connection-stats overlay is shown for a Mac; off until turned on. */
export function statsShown(mid: string): boolean {
  return read(statsKey(mid)) === "1";
}
export function setStatsShown(mid: string, on: boolean): void {
  write(statsKey(mid), on ? "1" : null);
}
