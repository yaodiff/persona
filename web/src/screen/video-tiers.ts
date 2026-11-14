// One video stream's quality tiers in the browser (#6558, #6396; docs/extensions/computer-use.md "Quality tiers"), as
// the iPhone's ScreenVideoTiers: what the frames say (./tiers/signals) and the tier chosen from it (./tiers/policy). The
// stream feeds it as frames arrive, decode and reach the screen, and ticks it every 250 ms; the link hands it Quality,
// the data cap and the route. A change is sent to the helper as an `update` (`updateOf`).
//
// It starts when the helper's `hello` lists "tiers" with the display's points and pixels; before that, and with a helper
// from before tiers, it changes nothing and the stream runs on its cap as it always has.
import type { Budget } from "./data-use";
import { TierPolicy, type Change, type Link } from "./tiers/policy";
import type { Bias } from "./tiers/ladder";
import { ReceiverSignals } from "./tiers/signals";
import type { Hello, Timing, Update } from "./video-wire";

/** What the link knows when the stream opens. */
export type TiersContext = {
  bias: Bias;
  budget: Budget;
  /** The picture's long edge at most this many pixels (the stream's `max_size`). */
  maxSize: number;
  /** The route; null before one is measured (the relay over Wi-Fi). */
  link: Link | null;
  /** The tier this computer last settled on over the same kind of route, on this page. */
  remembered: number | null;
};

export class ScreenVideoTiers {
  private signals = new ReceiverSignals();
  private policy: TierPolicy | null = null;

  constructor(private context: TiersContext) {}

  /** The tiers run: a helper with them said hello. */
  get active(): boolean {
    return this.policy !== null;
  }

  /** The stream's `hello`. A helper with tiers starts the policy; its first tier is returned to send. */
  hello(h: Hello, now: number): Change | null {
    const points = h.points, pixels = h.pixels;
    if (!h.features?.includes("tiers") || points?.length !== 2 || pixels?.length !== 2) return null;
    const c = this.context;
    this.policy = new TierPolicy({
      display: { points: [points[0], points[1]], pixels: [pixels[0], pixels[1]], maxSize: c.maxSize },
      bias: c.bias,
      capKbps: c.budget.maxKbps,
      maxFps: c.budget.fps,
      maxScale: c.budget.maxScale ?? null,
      link: c.link,
      remembered: c.remembered,
      now,
    });
    this.signals = new ReceiverSignals();
    return this.policy.last;
  }

  /** A frame arrived whole at `arrival` (µs), its first bytes at `firstByte` when it came over several reads. */
  frame(captureMicros: number, arrival: number, firstByte: number | null, bytes: number, refinement: boolean): void {
    if (!this.policy) return;
    this.signals.frame(captureMicros, arrival, firstByte, bytes, refinement, this.policy.tier.fps);
  }

  decoded(ms: number, at: number): void {
    if (this.policy) this.signals.decoded(ms, at);
  }

  shown(replaced: boolean, at: number): void {
    if (this.policy) this.signals.shown(replaced, at);
  }

  helper(t: Timing): void {
    this.policy?.helper({ targetKbps: t.target_kbps ?? null, kbps: t.kbps, skipped: t.skipped ?? null });
  }

  /** The 250 ms decision. */
  tick(micros: number): Change | null {
    if (!this.policy) return null;
    return this.policy.tick(this.signals.take(micros), micros / 1_000_000);
  }

  setBias(b: Bias, micros: number): Change | null {
    this.context = { ...this.context, bias: b };
    return this.policy?.setBias(b, micros / 1_000_000) ?? null;
  }

  setBudget(b: Budget, micros: number): Change | null {
    this.context = { ...this.context, budget: b };
    return this.policy?.setBudget(b.maxKbps, b.fps, b.maxScale ?? null, micros / 1_000_000) ?? null;
  }
}

/** A tier as the `update` that sets it on the helper. */
export function updateOf(c: Change): Omit<Update, "type"> {
  return { fps: c.tier.fps, max_kbps: c.tier.maxKbps, scale: c.tier.scale, min_kbps: c.tier.minKbps, target_kbps: c.targetKbps };
}
