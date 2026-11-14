// The live screen's video channel on the page (#3954, #6558; docs/extensions/computer-use.md "Live screen video
// (#3954)"): a chunk of its own that ScreenView loads only for a Mac whose node offers video, so neither its code nor
// its words (screen-video) are in the first screen. It probes what this browser decodes, runs the channel
// (./video-link) over the hub's relay, draws each picture on a canvas over noVNC's at the display's place, and says why
// when the page falls back to VNC. Input always stays on RFB: noVNC's canvas stays underneath at opacity 0.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { cx, Dialog } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { t } from "../i18n";
import type { Budget, MobileData } from "./data-use";
import type { Change } from "./tiers/policy";
import type { ScreenQuality } from "./video";
import { holdUpdates } from "./rfb-hold";
import { ScreenVideoLink, type Fallback, type Reading, type Want } from "./video-link";
import { relayTransport, videoSocket, type DecoderMaker, type Picture } from "./video-stream";
import type { VideoCodec } from "./video-probe";

import { probeCodecs } from "./video-probe";

/** What this browser decodes at the size the page will ask for (`budget`). */
export function probeHere() {
  const size = maxSize();
  return probeCodecs(size, Math.round((size * 10) / 16));
}

/** What the page shows of the channel: the picture, why VNC shows instead, and the overlay's numbers. */
export type VideoState = {
  showing: boolean;
  fallback: Fallback | null;
  reading: Reading;
  codec: string | null;
  size: { width: number; height: number } | null;
  tiers: boolean;
  decoderFailed: boolean;
  /** The quality tier and when it last changed (ms on performance.now()); null without tiers. */
  tier: Change | null;
  tierAt: number | null;
};

/** The picture's long edge at most: this screen's, at least 1920 and at most 4K, as the iPhone asks for its own. */
function maxSize() {
  const long = Math.max(window.screen?.width ?? 1920, window.screen?.height ?? 1080) * (window.devicePixelRatio || 1);
  return Math.min(3840, Math.max(1920, Math.round(long)));
}

/** The video over noVNC's canvas. `screen` is the element noVNC draws in; `pair` the signed-in connection's, null while
 *  RFB isn't; `want` what to show (null: VNC); `budget` the cap on its data (./data-use), sent again when it changes.
 *  With `tiers` (this page drives them, video.ts webTiers) the tiers take `tierBudget` (with the mobile-data choice) and
 *  `quality` once the helper lists them too. */
export function VideoChannel({
  rfb,
  screen,
  pair,
  want,
  codecs,
  budget,
  tierBudget = budget,
  quality = "auto",
  tiers = false,
  mid,
  token,
  onState,
  makeDecoder,
}: {
  rfb: object;
  screen: HTMLElement | null;
  pair: string | null;
  want: Want | null;
  codecs: VideoCodec[];
  budget: Budget;
  tierBudget?: Budget;
  quality?: ScreenQuality;
  tiers?: boolean;
  mid: string;
  token: string;
  onState: (s: VideoState) => void;
  makeDecoder?: DecoderMaker;
}) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [link, setLink] = useState<ScreenVideoLink | null>(null);
  const stateCb = useRef(onState);
  stateCb.current = onState;
  const budgetNow = useRef({ budget, tierBudget, quality });
  budgetNow.current = { budget, tierBudget, quality };

  // One link a page; its inputs follow the props.
  useEffect(() => {
    const now = budgetNow.current;
    const link = new ScreenVideoLink({
      open: (r) => relayTransport(videoSocket(window.location, mid, token, r.pair)),
      codecs,
      maxSize: maxSize(),
      maxKbps: now.budget.maxKbps,
      fps: now.budget.fps,
      tiers: tiers ? { key: mid, bias: now.quality, budget: now.tierBudget } : null,
      makeDecoder,
    });
    setLink(link);
    const tell = () =>
      stateCb.current({
        showing: link.showing,
        fallback: link.fallback,
        reading: link.reading,
        codec: link.codec,
        size: link.size,
        tiers: link.tiers,
        decoderFailed: link.decoderFailed,
        tier: link.tier,
        tierAt: link.tierAt,
      });
    link.onChange = tell;
    link.onReading = tell;
    const hidden = () => link.hold("background", document.visibilityState === "hidden");
    document.addEventListener("visibilitychange", hidden);
    hidden();
    return () => {
      document.removeEventListener("visibilitychange", hidden);
      link.close();
      setLink(null);
    };
  }, [mid, token, codecs, makeDecoder, tiers]);

  useEffect(() => link?.want(want), [link, want]);
  useEffect(
    () => link?.setBudget({ maxKbps: budget.maxKbps, fps: budget.fps, maxScale: budget.maxScale }, { maxKbps: tierBudget.maxKbps, fps: tierBudget.fps, maxScale: tierBudget.maxScale }),
    [link, budget.maxKbps, budget.fps, budget.maxScale, tierBudget.maxKbps, tierBudget.fps, tierBudget.maxScale],
  );
  useEffect(() => link?.setQuality(quality), [link, quality]);
  useEffect(() => link?.rfb(pair ? { pair } : null), [link, pair]);

  // Showing: noVNC asks for no updates and its canvas goes clear; the newest picture is drawn at the next frame.
  useEffect(() => {
    if (!link || !screen) return;
    let latest: Picture | null = null;
    let frame = 0;
    const vnc = () => [...screen.querySelectorAll("canvas")].find((c) => c !== canvas.current) ?? null;
    const place = () => {
      const c = canvas.current, v = vnc();
      if (!c || !v || !v.width) return;
      // relative to what the video canvas is placed in (the page's picture area), so it lies on noVNC's picture
      const box = v.getBoundingClientRect(), host = ((c.offsetParent as HTMLElement | null) ?? screen).getBoundingClientRect();
      const scale = box.width / v.width;
      const [x, y, w, h] = link.rect ?? [0, 0, v.width, v.height];
      Object.assign(c.style, { left: `${box.left - host.left + x * scale}px`, top: `${box.top - host.top + y * scale}px`, width: `${w * scale}px`, height: `${h * scale}px` });
    };
    const draw = () => {
      frame = 0;
      const c = canvas.current, p = latest;
      latest = null;
      if (!c || !p) return p?.close();
      const width = p.displayWidth ?? c.width, height = p.displayHeight ?? c.height;
      if (c.width !== width || c.height !== height) Object.assign(c, { width, height });
      c.getContext("2d")?.drawImage(p as unknown as CanvasImageSource, 0, 0, width, height);
      p.close();
      place();
    };
    link.onPicture = (p) => {
      // A picture still waiting for the next frame is replaced, never drawn: the tiers count those.
      link.shown(latest !== null);
      latest?.close();
      latest = p;
      if (!frame) frame = requestAnimationFrame(draw);
    };
    link.onShowing = (on) => {
      holdUpdates(rfb, on);
      const v = vnc();
      if (v) v.style.opacity = on ? "0" : "";
      if (canvas.current) canvas.current.style.display = on ? "" : "none";
      if (on) place();
    };
    const watch = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(place);
    watch?.observe(screen);
    if (canvas.current?.parentElement) watch?.observe(canvas.current.parentElement);
    return () => {
      watch?.disconnect();
      if (frame) cancelAnimationFrame(frame);
      latest?.close();
      link.onPicture = (p) => p.close();
      link.onShowing = () => {};
      holdUpdates(rfb, false);
      const v = vnc();
      if (v) v.style.opacity = "";
    };
  }, [link, rfb, screen]);

  return <canvas ref={canvas} data-testid="screen-video-picture" aria-hidden="true" className="pointer-events-none absolute" style={{ display: "none" }} />;
}

const LINES: Record<Fallback, () => string> = {
  lost: () => t("screen-video.fallback_lost"),
  locked: () => t("screen-video.fallback_locked"),
  notAllowed: () => t("screen-video.fallback_not_allowed"),
};

/** The line that says why VNC shows while video was chosen; for `notAllowed`, with "How to turn it on": one sentence,
 *  a window to answer when macOS hasn't asked yet (`asked` false), else where in System Settings. */
export function FallbackLine({ fallback, asked }: { fallback: Fallback; asked: boolean }) {
  const [how, setHow] = useState(false);
  const words = LINES[fallback]();
  return (
    <>
      {fallback === "notAllowed" ? (
        <button type="button" onClick={() => setHow(true)} data-testid="screen-fallback-help" className="rounded-control bg-surface/90 px-3 py-1.5 text-label text-ink-2 shadow-overlay backdrop-blur-md">
          {words} <span className="text-accent">{t("screen-video.how_to_turn_on")}</span>
        </button>
      ) : (
        <p role="status" data-testid="screen-fallback" className="rounded-control bg-surface/90 px-3 py-1.5 text-label text-ink-2 shadow-overlay backdrop-blur-md">
          {words}
        </p>
      )}
      <Dialog open={how} onClose={() => setHow(false)} title={t("screen-video.turn_on_title")}>
        <p className="text-meta text-ink-2">{asked ? t("screen-video.how_settings") : t("screen-video.how_window")}</p>
      </Dialog>
    </>
  );
}

const MOBILE_DATA: { m: MobileData; title: () => string; usage: () => string }[] = [
  { m: "clearer", title: () => t("screen-video.mobile_data_clearer"), usage: () => t("screen-video.mobile_data_clearer_usage") },
  { m: "saver", title: () => t("screen-video.mobile_data_saver"), usage: () => t("screen-video.mobile_data_saver_usage") },
];

/** The choices for mobile data in words, with about how much data an hour of typical use takes (the iPhone's), for the
 *  card and the ⋯ menu. */
export const mobileDataChoices = () => MOBILE_DATA.map(({ m, title, usage }) => ({ m, title: title(), usage: usage() }));

/** The question the live screen asks on mobile data (#6558, the iPhone's ScreenMobileDataCard), under the bar and over
 *  none of the controls: the picture already runs on Data Saver; Clearer or Data Saver takes effect at once and is kept
 *  in this browser for every computer, and the card goes. Closed unanswered, the picture stays on Data Saver and the
 *  question comes back once next time (./data-use). */
export function MobileDataCard({ onChoose, onClose }: { onChoose: (m: MobileData) => void; onClose: () => void }) {
  // Side by side where both choices fit, one over the other where they don't (a phone in English or Japanese), as the
  // iPhone's ViewThatFits: their words are never cut short or wrapped.
  const row = useRef<HTMLDivElement>(null);
  const [stack, setStack] = useState(false);
  useLayoutEffect(() => {
    const el = row.current;
    if (!el) return;
    const measure = () => {
      const was = el.style.flexDirection;
      el.style.flexDirection = "row"; // measured side by side, never drawn so
      const cut = [...el.querySelectorAll("span")].some((s) => s.scrollWidth > s.clientWidth);
      el.style.flexDirection = was;
      setStack(cut);
    };
    measure();
    void document.fonts?.ready.then(measure);
    const watch = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    watch?.observe(el);
    return () => watch?.disconnect();
  }, []);
  return (
    <section
      data-testid="screen-mobile-data"
      aria-label={t("screen-video.mobile_data_title")}
      className="pointer-events-auto flex w-full max-w-[420px] flex-col gap-2 rounded-card bg-surface/90 p-3 text-label text-ink shadow-overlay backdrop-blur-md"
    >
      <div className="flex items-start gap-1.5">
        <Icon name="phone" size={12} className="mt-0.5 shrink-0 text-ink-3" />
        <div className="min-w-0 flex-1">
          <p className="font-semibold">{t("screen-video.mobile_data_title")}</p>
          <p className="text-ink-2">{t("screen-video.mobile_data_line")}</p>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label={t("screen-video.mobile_data_close")}
          title={t("screen-video.mobile_data_close")}
          data-testid="screen-mobile-data-close"
          className="touch-hit -mr-1 flex size-5 shrink-0 items-center justify-center rounded-chip text-ink-2 hover:bg-hover hover:text-ink"
        >
          <Icon name="x" size={12} />
        </button>
      </div>
      <div ref={row} className={cx("flex gap-2", stack && "flex-col")}>
        {mobileDataChoices().map(({ m, title, usage }) => (
          <button
            key={m}
            type="button"
            onClick={() => onChoose(m)}
            data-testid={`screen-mobile-data-${m}`}
            className={cx(
              "touch-hit flex min-w-0 flex-1 flex-col items-center rounded-control px-3 py-1.5 whitespace-nowrap",
              m === "clearer" ? "bg-accent-ink text-surface hover:brightness-105" : "bg-field text-ink hover:bg-hover",
            )}
          >
            <span className="max-w-full truncate font-medium">{title}</span>
            <span className={cx("max-w-full truncate", m === "saver" && "text-ink-2")}>{usage}</span>
          </button>
        ))}
      </div>
    </section>
  );
}
