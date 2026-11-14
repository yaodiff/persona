// The browser's quality-tier choice (#6558): PersonaScreenKit's TierPolicy ported line for line, and checked against the
// timelines it writes (spec/fixtures/screen-tiers/policy). Every 250 ms it reads the stream's signals (./signals), the
// helper's `timing` and the route, and moves to another tier only when the band no longer fits: down fast, to the best
// tier whose floor fits 80% of what arrived; up slowly, one tier after a calm window and only with proof of headroom.
// Times are seconds on any monotonic clock; Swift's `Int(x)` truncates, so does this (`Math.trunc`).
import { MAX_KBPS } from "./capture-plan";
import { makeLadder, type Bias, type Tier } from "./ladder";
import type { TierSignals } from "./signals";

export type TierReason = "start" | "link_slowed" | "link_steady" | "network_changed" | "phone_warm" | "phone_behind" | "choice";
export type Change = { tier: Tier; index: number; reason: TierReason; targetKbps: number };
export type Display = { points: [number, number]; pixels: [number, number]; maxSize: number };
/** The parts of a route's LinkStats the policy reads. */
export type Link = { kind: "lan" | "tailscale" | "ipv6" | "relay"; via: "wifi" | "cellular" | "wired" | "other"; minRttMs?: number | null; srttMs?: number | null };
/** The helper's `timing`, as the policy reads it. */
export type HelperTiming = { targetKbps?: number | null; kbps: number; skipped?: number | null };

const DOWN_QUEUE_MS = 150;
const EMERGENCY_QUEUE_MS = 400;
const EMERGENCY_HOLD = 0.25;
const UP_QUEUE_MS = 50;
const EXHAUSTED = 1.15;
const DOWN_SPACING = 1;
const DECODE_HOLD = 2;
const STABLE_WINDOW = 8;
const EARLY_WINDOW = 3;
const EARLY = 10;
const UNDO_WITHIN = 15;
const LONGEST_WINDOW = 60;
const WINDOW_RESET = 120;
const BURST_PROOFS = 3;

const pixelRate = (t: Tier) => t.width * t.height * t.fps;

export type PolicyInit = {
  display: Display;
  bias: Bias;
  capKbps: number;
  maxFps: number;
  /** The most pixels per point allowed (Data Saver on mobile data: 1); null, no limit. */
  maxScale?: number | null;
  /** The route; null before one is measured, read as the relay over Wi-Fi. */
  link: Link | null;
  /** Where this computer last settled over the same route, an index into the same ladder. */
  remembered?: number | null;
  now: number;
};

export class TierPolicy {
  ladder: Tier[];
  index: number;
  bias: Bias;
  capKbps: number;
  maxFps: number;
  maxScale: number | null;
  last: Change;
  readonly display: Display;

  private shortUntil: number;
  private lastDownAt: number | null = null;
  private queueAtDown = 0;
  private emergencySince: number | null = null;
  private decodeBadSince: number | null = null;
  private calmSince: number | null = null;
  private motionKbps: number | null = null;
  private bursts = 0;
  private burstKbps: number | null = null;
  private windows = new Map<number, number>();
  private lastUp: { at: number; into: number } | null = null;
  private tierSince: number;
  private helperTarget: number | null = null;
  private helperSent: number | null = null;
  private helperSkipped = 0;

  constructor({ display, bias, capKbps, maxFps, maxScale = null, link, remembered = null, now }: PolicyInit) {
    this.display = display;
    this.bias = bias;
    this.capKbps = capKbps;
    this.maxFps = maxFps;
    this.maxScale = maxScale;
    const tiers = TierPolicy.tiers(display, bias, capKbps, maxFps, maxScale);
    this.ladder = tiers;
    const start = remembered !== null
      ? Math.min(Math.max(remembered, 0), tiers.length - 1)
      : TierPolicy.indexFor(TierPolicy.rung(link), tiers, display, bias, maxFps, maxScale);
    this.index = start;
    this.shortUntil = now + EARLY;
    this.tierSince = now;
    this.last = { tier: tiers[start], index: start, reason: "start", targetKbps: tiers[start].minKbps };
  }

  get tier(): Tier {
    return this.ladder[this.index];
  }

  /** The helper's `timing`: its target, what went out and the frames it skipped that second. */
  helper(t: HelperTiming): void {
    this.helperTarget = t.targetKbps ?? null;
    this.helperSent = t.kbps;
    this.helperSkipped = t.skipped ?? 0;
  }

  /** One decision, every 250 ms; a change is returned for the page to send as an `update`. */
  tick(s: TierSignals, now: number): Change | null {
    const skipping = this.helperSkipped > 0;
    const queue = s.queueP50Ms ?? 0;

    // Down, fast: a long queue or skipped frames for a quarter second.
    if (queue > EMERGENCY_QUEUE_MS || skipping) {
      const since = this.emergencySince ?? now;
      this.emergencySince = since;
      if (now - since >= EMERGENCY_HOLD && this.outgrown(s.deliveredKbps) && this.canDown(now, s) && this.index < this.ladder.length - 1) {
        return this.down(this.fitting(s.deliveredKbps), s.deliveredKbps, queue, "link_slowed", now);
      }
    } else {
      this.emergencySince = null;
    }

    // Down: the bitrate is at the band's floor and the queue still won't drain.
    const target = this.helperTarget;
    if (queue > DOWN_QUEUE_MS && target !== null && target <= EXHAUSTED * this.tier.minKbps && this.outgrown(s.deliveredKbps) &&
        this.canDown(now, s) && this.index < this.ladder.length - 1) {
      return this.down(this.fitting(s.deliveredKbps), s.deliveredKbps, queue, "link_slowed", now);
    }

    // The page can't keep up: a lower frame rate.
    const interval = 1000 / Math.max(this.tier.fps, 1);
    const behind = (s.decodeP95Ms !== null && s.decodeP95Ms > 1.5 * interval) || (s.replacedShare !== null && s.replacedShare > 1 / 3);
    if (behind) {
      const since = this.decodeBadSince ?? now;
      this.decodeBadSince = since;
      if (now - since >= DECODE_HOLD) {
        const slower = this.ladder.findIndex((t, i) => i > this.index && t.fps < this.tier.fps);
        if (slower >= 0) {
          this.decodeBadSince = null;
          return this.down(slower, s.deliveredKbps, queue, "phone_behind", now);
        }
      }
    } else {
      this.decodeBadSince = null;
    }

    // Up, slow: a calm window with proof of headroom. A still screen (no frames) neither proves nor breaks it.
    if (s.frames > 0) {
      const calm = (s.queueP95Ms ?? 0) < UP_QUEUE_MS && !skipping && !behind;
      if (!calm) {
        this.forgetProof();
        return null;
      }
    }
    if (this.calmSince === null) this.calmSince = now;
    if (this.index > 0) {
      const next = this.ladder[this.index - 1];
      const sent = this.helperSent;
      if (target !== null && sent !== null && sent >= 0.8 * target && sent >= 1.25 * next.minKbps) {
        this.motionKbps = Math.max(this.motionKbps ?? 0, sent);
      }
      for (const b of s.burstKbps) {
        if (b >= 1.5 * next.minKbps) {
          this.bursts += 1;
          this.burstKbps = Math.min(this.burstKbps ?? Number.MAX_SAFE_INTEGER, Math.trunc(b / 1.5));
        }
      }
    }
    if (this.lastUp && this.lastUp.into === this.index && now - this.tierSince >= WINDOW_RESET) this.windows.delete(this.index);
    if (this.index <= 0 || now - this.calmSince < this.window(this.index - 1, now)) return null;
    const proven = Math.max(this.motionKbps ?? 0, this.bursts >= BURST_PROOFS ? (this.burstKbps ?? 0) : 0);
    if (proven <= 0) return null;
    return this.up(proven, now);
  }

  /** The stream moved to another route: a start tier below the current one is taken at once; otherwise the tier stays
   *  and the next step up waits only the short window. */
  path(to: Link, now: number): Change | null {
    this.forgetProof();
    this.shortUntil = now + EARLY;
    const start = TierPolicy.indexFor(TierPolicy.rung(to), this.ladder, this.display, this.bias, this.maxFps, this.maxScale);
    if (start <= this.index) return null;
    return this.move(start, this.ladder[start].minKbps, "network_changed", now);
  }

  /** Quality chose another bias: the new ladder's tier that the current rate fits. */
  setBias(b: Bias, now: number): Change | null {
    if (b === this.bias) return null;
    this.bias = b;
    return this.rebuild("choice", now);
  }

  /** The caps changed (mobile data, Data Saver, a warm device): a new ladder, the tier the rate fits. */
  setBudget(capKbps: number, maxFps: number, maxScale: number | null, now: number): Change | null {
    if (capKbps === this.capKbps && maxFps === this.maxFps && maxScale === this.maxScale) return null;
    const reason: TierReason = maxFps < this.maxFps ? "phone_warm" : "network_changed";
    this.capKbps = capKbps;
    this.maxFps = maxFps;
    this.maxScale = maxScale;
    return this.rebuild(reason, now);
  }

  private forgetProof() {
    this.calmSince = null;
    this.motionKbps = null;
    this.bursts = 0;
    this.burstKbps = null;
  }

  private canDown(now: number, s: TierSignals): boolean {
    if (this.lastDownAt === null) return true;
    if (now - this.lastDownAt >= DOWN_SPACING && !s.queueFalling) return true;
    return s.queueRising && (s.queueP50Ms ?? 0) > 2 * this.queueAtDown + 100;
  }

  /** The tier's floor doesn't fit 80% of the rate that arrived (or that rate is unknown). */
  private outgrown(delivered: number | null): boolean {
    return delivered === null ? true : this.tier.minKbps > 0.8 * delivered;
  }

  /** The best tier below this one whose floor fits 80% of the rate that arrived; the next one down when it's unknown. */
  private fitting(delivered: number | null): number {
    if (delivered === null) return this.index + 1;
    const i = this.ladder.findIndex((t, k) => k > this.index && t.minKbps <= 0.8 * delivered);
    return i >= 0 ? i : this.ladder.length - 1;
  }

  private window(into: number, now: number): number {
    const base = now < this.shortUntil ? EARLY_WINDOW : STABLE_WINDOW;
    return Math.max(base, this.windows.get(into) ?? 0);
  }

  private down(i: number, delivered: number | null, queue: number, reason: TierReason, now: number): Change {
    // A step up undone soon: that tier waits twice as long next time.
    if (this.lastUp && this.lastUp.into === this.index && now - this.lastUp.at < UNDO_WITHIN) {
      this.windows.set(this.index, Math.min(LONGEST_WINDOW, Math.max(STABLE_WINDOW, this.windows.get(this.index) ?? 0) * 2));
    }
    this.lastDownAt = now;
    this.queueAtDown = queue;
    this.emergencySince = null;
    const t = this.ladder[i];
    const target = delivered === null ? t.minKbps : Math.min(t.maxKbps, Math.max(t.minKbps, Math.trunc(0.85 * delivered)));
    return this.move(i, target, reason, now);
  }

  private up(proven: number, now: number): Change {
    const i = this.index - 1;
    this.lastUp = { at: now, into: i };
    const t = this.ladder[i];
    return this.move(i, Math.min(t.maxKbps, Math.max(t.minKbps, proven)), "link_steady", now);
  }

  private move(i: number, target: number, reason: TierReason, now: number): Change {
    this.index = i;
    this.tierSince = now;
    this.forgetProof();
    this.decodeBadSince = null;
    this.last = { tier: this.ladder[i], index: i, reason, targetKbps: target };
    return this.last;
  }

  private rebuild(reason: TierReason, now: number): Change {
    const rate = this.helperTarget ?? this.last.targetKbps;
    const current = this.tier;
    this.ladder = TierPolicy.tiers(this.display, this.bias, this.capKbps, this.maxFps, this.maxScale);
    this.windows.clear();
    this.lastUp = null;
    // The best tier the current rate fits, no richer in pixels a second than the one before.
    let i = this.ladder.findIndex((t) => t.minKbps <= rate && pixelRate(t) <= pixelRate(current));
    if (i < 0) i = this.ladder.length - 1;
    const t = this.ladder[i];
    return this.move(i, Math.min(t.maxKbps, Math.max(t.minKbps, rate)), reason, now);
  }

  /** Which rung of the full ladder a route starts on: the same network 0; Tailscale (or direct IPv6) 0 under 30 ms,
   *  else 1; the relay 1 over Wi-Fi, 2 over cellular. Nothing measured yet: the relay over Wi-Fi. */
  static rung(link: Link | null): number {
    if (!link) return 1;
    switch (link.kind) {
      case "lan":
        return 0;
      case "tailscale":
      case "ipv6":
        return (link.minRttMs ?? link.srttMs ?? Infinity) < 30 ? 0 : 1;
      default:
        return link.via === "cellular" ? 2 : 1;
    }
  }

  /** The ladder under the caps, without the tiers above `maxScale` (never empty: every ladder ends on 1× tiers). */
  static tiers(display: Display, bias: Bias, capKbps: number, maxFps: number, maxScale: number | null): Tier[] {
    const all = makeLadder({ points: display.points, pixels: display.pixels, maxSize: display.maxSize, bias, capKbps, maxFps });
    if (maxScale === null) return all;
    const kept = all.filter((t) => t.scale <= maxScale);
    return kept.length ? kept : all.slice(-1);
  }

  /** The tier of `ladder` for a rung of the uncapped ladder: the best one no richer in pixels a second. */
  static indexFor(rung: number, ladder: Tier[], display: Display, bias: Bias, maxFps: number, maxScale: number | null = null): number {
    const full = TierPolicy.tiers(display, bias, MAX_KBPS, maxFps, maxScale);
    const ref = pixelRate(full[Math.min(rung, full.length - 1)]);
    const i = ladder.findIndex((t) => pixelRate(t) <= ref);
    return i >= 0 ? i : ladder.length - 1;
  }
}
