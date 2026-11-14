// The live screen's connection numbers on the web (#6558, docs/extensions/computer-use.md "Connection stats on the web"):
// what the page measured on its RFB connection and on its way to the hub, turned into the reading the overlay draws
// once a second (ScreenStats, ./video). Pure: the page feeds it bytes, frames and round trips (./probes) with their times.
//
// What is shown equals what was measured. A number with nothing behind it stays out of the reading, never guessed: no
// frame counter (a noVNC this page does not know) means no frame rate, and no probe answered lately means no round
// trip. A measured zero is a number: a screen that does not change sends nothing, and the overlay says 0 fps.
import type { ScreenStats } from "./video";

/** How many recent round trips the shown one is the median of, so one slow answer does not make the number jump. */
export const ROUND_TRIPS_KEPT = 5;
/** A round trip older than this is not shown (the page was in the background, or the hub stopped answering). */
export const ROUND_TRIP_FRESH_MS = 20_000;

export class ScreenMeter {
  private bytes = 0;
  private frames = 0;
  private since: number;
  private trips: { ms: number; at: number }[] = [];

  /** `now`: where the first reading's window starts. `countsFrames`: whether frames are counted at all. */
  constructor(now: number, private readonly countsFrames: boolean) {
    this.since = now;
  }

  /** Bytes the RFB connection received. */
  received(n: number): void {
    if (n > 0) this.bytes += n;
  }

  /** A frame noVNC drew to the screen. */
  drew(): void {
    this.frames++;
  }

  /** One round trip to the hub, `ms` long, measured at `at`. */
  roundTrip(ms: number, at: number): void {
    if (!Number.isFinite(ms) || ms < 0) return;
    this.trips.push({ ms, at });
    if (this.trips.length > ROUND_TRIPS_KEPT) this.trips.shift();
  }

  /** The reading at `now`: bitrate and frame rate over the time since the last reading (which starts the next one),
   *  and the median of the recent round trips. The picture is always VNC through the hub's port relay here. */
  read(now: number): ScreenStats {
    const out: ScreenStats = { mode: "vnc", path: "relay" };
    const secs = (now - this.since) / 1000;
    if (secs > 0) {
      out.bitrateMbps = (this.bytes * 8) / 1e6 / secs;
      if (this.countsFrames) out.fps = this.frames / secs;
      this.bytes = 0;
      this.frames = 0;
      this.since = now;
    }
    const fresh = this.trips.filter((t) => now - t.at <= ROUND_TRIP_FRESH_MS).map((t) => t.ms);
    if (fresh.length > 0) out.hubRoundTripMs = median(fresh);
    return out;
  }
}

/** A bitrate as the overlay writes it, in Mbps: one decimal, two below 1 Mbps, so a trickle that was measured (a small
 *  change on a still screen) does not read as 0.0. */
export function mbpsText(mbps: number): string {
  return mbps < 1 ? mbps.toFixed(2) : mbps.toFixed(1);
}

export function median(xs: readonly number[]): number {
  const s = [...xs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}
