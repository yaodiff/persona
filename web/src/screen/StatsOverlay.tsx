// The live screen's connection stats on the web (#6558, docs/extensions/computer-use.md "Connection stats on the web"):
// the bar's toggle and the small panel it shows over the picture, as the iOS app's ⋯ › Connection Stats and
// ScreenStatsOverlay do. Off by default and kept per Mac (video.ts statsShown). ScreenView loads this chunk once the
// screen is live, so its words (screen-video) stay out of the first screen's catalog, which every page of the app sends.
//
// The panel measures only while it is shown (./probes, ./meter) and draws a reading once a second: the mode and path on
// its first line, then bitrate and frame rate, then the round trip to the hub on a line of its own and named so. That
// number is the browser's way to the hub, not the picture's latency, so it never sits on the first line the way the
// iOS app puts the video's end-to-end total there. A number that was not measured is not drawn.
//
// Its classes are ones other pages already use: Tailwind writes every class it finds in the source, comments included,
// into the one stylesheet the first screen sends, so a new one costs every page (startup-preload FIRST_SCREEN_BUDGET:
// #7368's two new ones, a translucent surface and a minimum width, took the ja first screen 29 B over it).
import { Fragment, useEffect, useRef, useState } from "react";
import { cx } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { t } from "../i18n";
import { mbpsText, ScreenMeter } from "./meter";
import { countBytes, countFrames, probeHub } from "./probes";
import type { Change, TierReason } from "./tiers/policy";
import type { ScreenQuality, ScreenStats } from "./video";
import type { Reading } from "./video-link";

/** How often the panel draws a new reading. */
export const READ_EVERY_MS = 1000;

/** The bar's Connection stats toggle. */
export function StatsToggle({ on, onToggle }: { on: boolean; onToggle: (on: boolean) => void }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={() => onToggle(!on)}
      aria-label={t("screen-video.stats")}
      title={t("screen-video.stats")}
      data-testid="screen-stats-toggle"
      className={cx(
        "touch-hit flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-control px-2 text-label font-medium transition-colors hover:bg-hover hover:text-ink",
        on ? "bg-hover text-ink" : "text-ink-2",
      )}
    >
      <Icon name="chart" size={14} />
      {/* a narrow bar keeps the icon only; the button's name says what it is */}
      <span className="hidden md:inline">{t("screen-video.stats")}</span>
    </button>
  );
}

/** The panel over the picture: measures `rfb` (noVNC) and `socket` (the RFB WebSocket it was handed) while shown. */
/** The video channel's numbers while its picture shows (#6558): ./video-link's reading, the codec and the size; with
 *  quality tiers, the tier, when it last changed (ms on performance.now()) and the Quality chosen. */
export type VideoStats = {
  reading: Reading;
  codec: string | null;
  size: { width: number; height: number } | null;
  tier?: { change: Change; at: number; quality: ScreenQuality } | null;
};

const QUALITY: Record<ScreenQuality, () => string> = {
  auto: () => t("screen-video.quality_auto"),
  sharper: () => t("screen-video.quality_sharper"),
  smoother: () => t("screen-video.quality_smoother"),
};

const REASON: Record<TierReason, () => string> = {
  start: () => t("screen-video.tier_start"),
  link_slowed: () => t("screen-video.tier_link_slowed"),
  link_steady: () => t("screen-video.tier_link_steady"),
  network_changed: () => t("screen-video.tier_network_changed"),
  phone_warm: () => t("screen-video.tier_device_warm"),
  phone_behind: () => t("screen-video.tier_device_behind"),
  choice: () => t("screen-video.tier_choice"),
};

/** The tier as the panel shows it, as the iPhone's ScreenTierText: the Quality chosen, the scale and the frame rate
 *  (Auto · 1.5× · 60 fps); and why it last changed, in plain words, with how long ago. */
export function tierParts(quality: ScreenQuality, c: Change): string[] {
  const scale = Number.isInteger(c.tier.scale) ? String(c.tier.scale) : c.tier.scale.toFixed(1);
  return [QUALITY[quality](), `${scale}×`, t("screen-video.stats_fps", { fps: c.tier.fps })];
}
export function tierReasonParts(reason: TierReason, at: number, now: number): string[] {
  const seconds = Math.max(0, Math.floor((now - at) / 1000));
  const ago = seconds < 60 ? t("screen-video.tier_seconds_ago", { n: seconds }) : t("screen-video.tier_minutes_ago", { n: Math.floor(seconds / 60) });
  return [REASON[reason](), ago];
}
/** Why the computer's video isn't playing on this page, said only when the computer offers it. */
export type NoVideoReason = "insecure" | "unsupported" | "failed";

const NO_VIDEO: Record<NoVideoReason, () => string> = {
  insecure: () => t("screen-video.stats_no_video_insecure"),
  unsupported: () => t("screen-video.stats_no_video_unsupported"),
  failed: () => t("screen-video.stats_no_video_failed"),
};

export function StatsOverlay({ rfb, socket, onClose, video = null, noVideo = null }: {
  rfb: object;
  socket: WebSocket;
  onClose: () => void;
  /** Set while the video channel is the picture: the panel shows its numbers, as the iPhone's does. */
  video?: VideoStats | null;
  noVideo?: NoVideoReason | null;
}) {
  const [stats, setStats] = useState<ScreenStats>({ mode: "vnc", path: "relay" });
  useEffect(() => {
    // The meter counts frames only when this noVNC could be hooked (frames come later, never during this call).
    let m: ScreenMeter;
    const undoFrames = countFrames(rfb, () => m.drew());
    m = new ScreenMeter(performance.now(), undoFrames !== null);
    const undoBytes = countBytes(socket, (n) => m.received(n));
    const stopProbe = probeHub((ms) => m.roundTrip(ms, performance.now()));
    const read = setInterval(() => setStats(m.read(performance.now())), READ_EVERY_MS);
    return () => {
      clearInterval(read);
      stopProbe();
      undoBytes();
      undoFrames?.();
    };
  }, [rfb, socket]);

  // Dragged anywhere inside the page's picture area, from where it starts at the top right.
  const [at, setAt] = useState({ x: 0, y: 0 });
  const drag = useRef<{ id: number; x: number; y: number; from: { x: number; y: number }; min: { x: number; y: number }; max: { x: number; y: number } } | null>(null);
  const start = (e: React.PointerEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).closest("button")) return;
    const el = e.currentTarget;
    const room = el.parentElement?.getBoundingClientRect();
    if (!room) return;
    const box = el.getBoundingClientRect();
    el.setPointerCapture?.(e.pointerId);
    drag.current = {
      id: e.pointerId,
      x: e.clientX,
      y: e.clientY,
      from: at,
      min: { x: at.x - (box.left - room.left), y: at.y - (box.top - room.top) },
      max: { x: at.x + (room.right - box.right), y: at.y + (room.bottom - box.bottom) },
    };
  };
  const move = (e: React.PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || d.id !== e.pointerId) return;
    const clamp = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), Math.max(lo, hi));
    setAt({ x: clamp(d.from.x + e.clientX - d.x, d.min.x, d.max.x), y: clamp(d.from.y + e.clientY - d.y, d.min.y, d.max.y) });
  };
  const end = (e: React.PointerEvent<HTMLDivElement>) => {
    if (drag.current?.id === e.pointerId) drag.current = null;
  };

  const r = video?.reading;
  const bitrate = video ? r?.mbps : stats.bitrateMbps;
  const fps = video ? r?.fps : stats.fps;
  const rates = [
    bitrate !== undefined ? t("screen-video.stats_mbps", { mbps: mbpsText(bitrate) }) : null,
    fps !== undefined ? t("screen-video.stats_fps", { fps: Math.round(fps) }) : null,
    video?.codec ?? null,
    video?.size ? `${video.size.width}×${video.size.height}` : null,
  ].filter((x): x is string => x !== null);
  // The latency broken down (video): capture · encode · network · decode, a segment not measured left out; their
  // total leads the first line, as on the iPhone.
  const segments = video
    ? [
        r?.captureMs !== undefined ? t("screen-video.stats_capture", { ms: Math.round(r.captureMs) }) : null,
        r?.encodeMs !== undefined ? t("screen-video.stats_encode", { ms: Math.round(r.encodeMs) }) : null,
        r?.networkMs !== undefined ? t("screen-video.stats_network", { ms: Math.round(r.networkMs) }) : null,
        r?.decodeMs !== undefined ? t("screen-video.stats_decode", { ms: Math.round(r.decodeMs) }) : null,
      ].filter((x): x is string => x !== null)
    : [];
  const dotted = (parts: string[]) =>
    parts.map((part, i) => (
      <Fragment key={i}>
        {i > 0 && <span className="text-ink-3">·</span>}
        <span>{part}</span>
      </Fragment>
    ));
  return (
    <div
      role="group"
      aria-label={t("screen-video.stats")}
      data-testid="screen-stats"
      onPointerDown={start}
      onPointerMove={move}
      onPointerUp={end}
      onPointerCancel={end}
      style={{ transform: `translate(${at.x}px, ${at.y}px)` }}
      className="absolute top-3 right-3 z-10 flex cursor-grab touch-none flex-col gap-1 rounded-card bg-surface/90 px-2.5 py-2 text-label text-ink shadow-overlay backdrop-blur-md select-none tabular-nums active:cursor-grabbing"
    >
      <div className="flex items-center gap-1.5 whitespace-nowrap">
        <span className="font-semibold">{video ? t("screen-video.stats_video") : t("screen-video.stats_vnc")}</span>
        {stats.path === "relay" && (
          <>
            <span className="text-ink-3">·</span>
            <span className="text-ink-2">{t("screen-video.stats_via_hub")}</span>
          </>
        )}
        {r?.totalMs !== undefined && (
          <>
            <span className="text-ink-3">·</span>
            <span data-testid="screen-stats-total">{t("screen-video.stats_ms", { ms: Math.round(r.totalMs) })}</span>
          </>
        )}
        <span className="flex-1" />
        <button
          type="button"
          onClick={onClose}
          aria-label={t("screen-video.stats_hide")}
          title={t("screen-video.stats_hide")}
          data-testid="screen-stats-close"
          className="touch-hit -mr-1 ml-3 flex size-5 items-center justify-center rounded-chip text-ink-2 hover:bg-hover hover:text-ink"
        >
          <Icon name="x" size={12} />
        </button>
      </div>
      {/* the tier and why it last changed, under the header as on the iPhone; the reason on its own line, since the
          reasons are words and would widen the panel */}
      {video?.tier && (
        <>
          <p className="flex items-center gap-1.5 whitespace-nowrap" data-testid="screen-stats-tier">
            <span className="text-ink-2">{t("screen-video.stats_tier")}</span>
            {dotted(tierParts(video.tier.quality, video.tier.change))}
          </p>
          <p className="flex items-center gap-1.5 whitespace-nowrap text-ink-3" data-testid="screen-stats-tier-reason">
            {dotted(tierReasonParts(video.tier.change.reason, video.tier.at, performance.now()))}
          </p>
        </>
      )}
      {rates.length > 0 && <p className="flex items-center gap-1.5 whitespace-nowrap text-ink-2">{dotted(rates)}</p>}
      {segments.length > 0 && (
        <p className="flex items-center gap-1.5 whitespace-nowrap text-ink-2" data-testid="screen-stats-segments">
          {dotted(segments)}
        </p>
      )}
      {stats.hubRoundTripMs !== undefined && (
        <p className="whitespace-nowrap text-ink-2" data-testid="screen-stats-hub">
          {t("screen-video.stats_hub_round_trip", { ms: Math.round(stats.hubRoundTripMs) })}
        </p>
      )}
      {!video && noVideo && (
        <p className="max-w-[240px] text-ink-3" data-testid="screen-stats-no-video">
          {NO_VIDEO[noVideo]()}
        </p>
      )}
    </div>
  );
}
