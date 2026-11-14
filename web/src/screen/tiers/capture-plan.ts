// The parts of PersonaScreenKit's CapturePlan the quality tiers use (#6396 / #6558), as Persona Screen applies them:
// the bitrate limits, the encoded size for a scale, the frame rate and the cap. Integer arithmetic where Swift's is, so
// both sides give the same numbers (spec/fixtures/screen-tiers).

/** The lowest and highest bitrate the helper encodes at, in kbit/s. */
export const MIN_KBPS = 200;
export const MAX_KBPS = 50_000;
/** The scales (pixels per point) the helper takes; a request outside is clamped. */
export const SCALES = { min: 0.25, max: 4 } as const;

const even = (v: number) => Math.max(2, v & ~1);
/** Swift's `rounded()` for the positive values here: half away from zero, which is `Math.round` above 0. */
export const rounded = (v: number) => Math.round(v);

/** The encoded size for a display `width` × `height`: the long edge at most `maxSize` (0 or less: no cap), the aspect
 *  ratio kept, never larger than the display, both sides even and at least 2. */
export function sizeFor(width: number, height: number, maxSize: number): { width: number; height: number } {
  if (width <= 0 || height <= 0) return { width: 2, height: 2 };
  const long = Math.max(width, height), short = Math.min(width, height);
  if (maxSize <= 0 || long <= maxSize) return { width: even(width), height: even(height) };
  const scaledShort = Math.floor((short * maxSize + Math.floor(long / 2)) / long); // integer division, as Swift's
  return width >= height ? { width: even(maxSize), height: even(scaledShort) } : { width: even(scaledShort), height: even(maxSize) };
}

/** The encoded size for a display `points` and `pixels` in size at `scale` pixels per point: never more than its
 *  pixels, then capped by `maxSize`. */
export function scaledSize(points: [number, number], pixels: [number, number], scale: number, maxSize: number): { width: number; height: number } {
  if (!Number.isFinite(scale) || points[0] <= 0 || points[1] <= 0) return sizeFor(pixels[0], pixels[1], maxSize);
  const s = Math.min(Math.max(scale, SCALES.min), SCALES.max);
  const width = Math.min(rounded(points[0] * s), Math.max(pixels[0], 1));
  const height = Math.min(rounded(points[1] * s), Math.max(pixels[1], 1));
  return sizeFor(width, height, maxSize);
}

/** The frame rate to capture at: kept within 1–60. */
export const fpsFor = (requested: number) => Math.min(Math.max(requested, 1), 60);

/** A bitrate cap kept within MIN_KBPS–MAX_KBPS; 0 or less means no cap. */
export const capFor = (requested: number) => (requested <= 0 ? MAX_KBPS : Math.min(Math.max(requested, MIN_KBPS), MAX_KBPS));
