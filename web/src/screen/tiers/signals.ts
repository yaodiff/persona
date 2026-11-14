// What the browser measures of the video stream for the quality tiers (#6558): PersonaScreenKit's ReceiverSignals ported
// line for line (spec/fixtures/screen-tiers/signals is replayed against it). The queueing delay is a frame's one-way delay
// above the lowest of the last 10 s, so the helper's and the browser's clocks needn't agree; the delivered rate is what
// the link carried while busy, else what came over the last second; large frames over several reads say how fast the
// link moved them. Times are µs on the page's monotonic clock; the arithmetic is in the Swift side's order, so the two
// give the same numbers.

/** Frames this big or bigger, spread over several reads, say how fast the link moved them. */
export const BURST_BYTES = 32_000;
const BURST_MIN_SPAN_MICROS = 5_000;
const BASELINE_MICROS = 10_000_000;
const BUSY_MS = 50;

/** The browser's view of the stream for one tier decision, as `TierSignals`; null where nothing was measured. */
export type TierSignals = {
  frames: number;
  queueP50Ms: number | null;
  queueP95Ms: number | null;
  queueRising: boolean;
  queueFalling: boolean;
  deliveredKbps: number | null;
  burstKbps: number[];
  lateShare: number | null;
  decodeP95Ms: number | null;
  replacedShare: number | null;
};

export function percentile(values: number[], p: number): number | null {
  if (!values.length) return null;
  const s = [...values].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.round((s.length - 1) * p))];
}

export function mean(values: number[]): number | null {
  return values.length ? values.reduce((a, b) => a + b, 0) / values.length : null;
}

const within = (at: number, now: number, window: number) => at + window >= now;

export class ReceiverSignals {
  private baseline: { at: number; delay: number }[] = [];
  private queue: { at: number; ms: number; late: boolean; bytes: number }[] = [];
  private arrivals: { at: number; bytes: number }[] = [];
  private bursts: number[] = [];
  private decodes: { at: number; ms: number }[] = [];
  private showns: { at: number; replaced: boolean }[] = [];

  /** A frame arrived whole at `arrival`, its first bytes at `firstByte` (null: one read). `fps` is the frame rate asked
   *  for, which says when a frame is late (over two frame intervals queued). */
  frame(capture: number, arrival: number, firstByte: number | null, bytes: number, refinement: boolean, fps: number): void {
    if (bytes >= BURST_BYTES && firstByte !== null && arrival > firstByte && arrival - firstByte >= BURST_MIN_SPAN_MICROS) {
      this.bursts.push((bytes * 8 * 1000) / (arrival - firstByte));
    }
    if (refinement) return;
    const delay = arrival - capture;
    while (this.baseline.length && this.baseline[this.baseline.length - 1].delay >= delay) this.baseline.pop();
    this.baseline.push({ at: arrival, delay });
    while (this.baseline.length > 1 && arrival - this.baseline[0].at > BASELINE_MICROS) this.baseline.shift();
    const ms = (delay - this.baseline[0].delay) / 1000;
    const interval = 1000 / Math.max(fps, 1);
    this.queue.push({ at: arrival, ms, late: ms > 2 * interval, bytes });
    this.arrivals.push({ at: arrival, bytes });
    this.prune(arrival);
  }

  /** A picture took `ms` to decode. */
  decoded(ms: number, at: number): void {
    this.decodes.push({ at, ms });
    this.prune(at);
  }

  /** A decoded picture reached the screen, or was `replaced` by a newer one before it did. */
  shown(replaced: boolean, at: number): void {
    this.showns.push({ at, replaced });
    this.prune(at);
  }

  /** The stream moved to another route: its one-way delay is its own, so the floor starts over. */
  pathChanged(): void {
    this.baseline = [];
    this.queue = [];
    this.arrivals = [];
  }

  private prune(now: number) {
    this.queue = this.queue.filter((q) => !(q.at + 2_000_000 < now));
    this.arrivals = this.arrivals.filter((a) => !(a.at + 1_000_000 < now));
    this.decodes = this.decodes.filter((d) => !(d.at + 2_000_000 < now));
    this.showns = this.showns.filter((s) => !(s.at + 2_000_000 < now));
  }

  /** The signals at `now`; the bursts are handed over once. */
  take(now: number): TierSignals {
    this.prune(now);
    const recent = this.queue.filter((q) => within(q.at, now, 500_000)).map((q) => q.ms);
    const s: TierSignals = {
      frames: recent.length,
      queueP50Ms: percentile(recent, 0.5),
      queueP95Ms: percentile(recent, 0.95),
      queueRising: false,
      queueFalling: false,
      deliveredKbps: null,
      burstKbps: this.bursts,
      lateShare: null,
      decodeP95Ms: percentile(this.decodes.map((d) => d.ms), 0.95),
      replacedShare: null,
    };
    this.bursts = [];
    const early = mean(this.queue.filter((q) => within(q.at, now, 1_000_000) && !within(q.at, now, 750_000)).map((q) => q.ms));
    const late = mean(this.queue.filter((q) => within(q.at, now, 250_000)).map((q) => q.ms));
    if (early !== null && late !== null) {
      s.queueRising = late - early > 10;
      s.queueFalling = late - early < -10;
    }
    let busyBytes = 0, busyMicros = 0, busyFrames = 0;
    for (let i = 0; i + 1 < this.queue.length; i++) {
      const a = this.queue[i], b = this.queue[i + 1];
      if (a.ms > BUSY_MS && b.ms > BUSY_MS && b.at > a.at) {
        busyBytes += b.bytes;
        busyMicros += b.at - a.at;
        busyFrames += 1;
      }
    }
    const first = this.arrivals[0], last = this.arrivals[this.arrivals.length - 1];
    if (busyFrames >= 3 && busyMicros >= 50_000) s.deliveredKbps = (busyBytes * 8 * 1000) / busyMicros;
    else if (this.arrivals.length >= 4 && last.at - first.at >= 200_000) {
      const bytes = this.arrivals.slice(1).reduce((sum, a) => sum + a.bytes, 0);
      s.deliveredKbps = (bytes * 8 * 1000) / (last.at - first.at);
    }
    const window = this.queue.filter((q) => within(q.at, now, 500_000));
    s.lateShare = window.length ? window.filter((q) => q.late).length / window.length : null;
    s.replacedShare = this.showns.length ? this.showns.filter((x) => x.replaced).length / this.showns.length : null;
    return s;
  }
}
