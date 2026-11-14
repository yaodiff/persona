// The live screen's quality tiers in the browser (#6558), PersonaScreenKit's TierLadder ported line for line: a ladder of
// sizes and frame rates, best first, each with a bitrate band from pixels × frames a second. The same input gives the
// same ladder on both sides: spec/fixtures/screen-tiers/ladder, written by the Swift side, is replayed here.
import { capFor, fpsFor, MIN_KBPS, rounded, scaledSize } from "./capture-plan";

export type Bias = "auto" | "sharper" | "smoother";

export type Tier = { scale: number; fps: number; width: number; height: number; minKbps: number; maxKbps: number };

/** Bits per pixel per frame at a band's floor and at its ceiling. */
export const FLOOR_BITS_PER_PIXEL = 0.02;
export const CEILING_BITS_PER_PIXEL = 0.08;

/** How many more bits per pixel a codec needs than HEVC (TierLadder.bitsFactor): 1 for both until the encoding bench
 *  measures H.264. */
export function bitsFactor(codec: string): number {
  return codec.toLowerCase() === "h264" ? 1.0 : 1.0;
}

/** The ladder's steps for a bias, best first. */
export function steps(bias: Bias): [number, number][] {
  switch (bias) {
    case "sharper":
      return [[2, 60], [2, 30], [2, 15], [1.5, 15], [1, 15]];
    case "smoother":
      return [[2, 60], [1.5, 60], [1, 60], [1, 30], [1, 15]];
    default:
      return [[2, 60], [1.5, 60], [1.5, 30], [1, 30], [1, 15]];
  }
}

export type LadderInput = {
  points: [number, number];
  pixels: [number, number];
  maxSize: number;
  bias: Bias;
  capKbps: number;
  maxFps: number;
  codec?: string;
};

/** The ladder for a display under the client's long-edge cap, bitrate cap and frame-rate cap, for the stream's codec.
 *  Never empty: the last tier stays even when its floor is above the cap (its floor is then the helper's). */
export function makeLadder({ points, pixels, maxSize, bias, capKbps, maxFps, codec = "hevc" }: LadderInput): Tier[] {
  const cap = capFor(capKbps);
  const factor = bitsFactor(codec);
  const tiers: Tier[] = [];
  for (const [scale, stepFps] of steps(bias)) {
    const size = scaledSize(points, pixels, scale, maxSize);
    const fps = fpsFor(Math.min(stepFps, maxFps));
    if (tiers.some((t) => t.width === size.width && t.height === size.height && t.fps === fps)) continue;
    const perSecond = size.width * size.height * fps;
    const floor = Math.max(MIN_KBPS, rounded((perSecond * FLOOR_BITS_PER_PIXEL * factor) / 1000));
    const ceiling = Math.min(cap, Math.max(floor, rounded((perSecond * CEILING_BITS_PER_PIXEL * factor) / 1000)));
    tiers.push({ scale, fps, width: size.width, height: size.height, minKbps: floor, maxKbps: ceiling });
  }
  // A tier the cap can't carry at its floor is left out; the lowest stays, its floor the helper's own.
  const last = tiers.pop();
  if (last) {
    const kept = tiers.filter((t) => t.minKbps <= cap);
    last.minKbps = MIN_KBPS;
    last.maxKbps = Math.max(last.maxKbps, last.minKbps);
    return [...kept, last];
  }
  return tiers;
}
