/**
 * Shared motion tokens mirroring iOS Theme.Motion (apple/Shared/Design/Theme.swift)
 * and performance rule P9 (docs/apple/performance.md): continuous motion pauses off screen,
 * in background, or under Reduce Motion.
 */
export class MotionTokens {
  /** Opening/closing controls, chips, folds, and state changes (Theme.Motion.state / standard). */
  static readonly stateMs: number = 250;
  /** Layout changes, search field reveal, card expansion (Theme.Motion.layout). */
  static readonly layoutMs: number = 300;
  /** Multi-row block unfolding in place (Theme.Motion.expand). */
  static readonly expandMs: number = 400;
  /** Floating affordances appearing / concealing (Theme.Motion.reveal / conceal). */
  static readonly revealMs: number = 280;
  static readonly concealMs: number = 200;
  /** Drawer settle duration without bounce (Theme.Motion.drawer). */
  static readonly drawerMs: number = 320;
  /** Spring-back duration after gesture release (Theme.Motion.springBack). */
  static readonly springBackMs: number = 300;
  /** Subtle tactile press feedback on decision pills & interactive cards. */
  static readonly pressMs: number = 140;
  static readonly releaseMs: number = 220;
  static readonly pressScale: number = 0.96;
  static readonly composerPressScale: number = 0.75;
  /** Needs You breathing ring (Theme.Motion.needsYouBreath*). */
  static readonly breathDurationMs: number = 1000;
  static readonly breathScale: number = 1.2;
  static readonly breathRingOpacity: number = 0.6;
  /** Working status arc rotation (Theme.Motion.workingArcTurn). */
  static readonly workingArcTurnMs: number = 1000;
  /** Streaming tail caret pulse. */
  static readonly caretPulseMs: number = 650;
}

export function resolveMotionDuration(durationMs: number, reduceMotion: boolean): number {
  if (reduceMotion || !Number.isFinite(durationMs) || durationMs <= 0) return 0;
  return Math.round(durationMs);
}

/** P9 guard: repeating status animations run only while visible, foregrounded, and not reduced. */
export function shouldAnimateLoop(visible: boolean, foreground: boolean, reduceMotion: boolean): boolean {
  return visible && foreground && !reduceMotion;
}

/** Clamps normalized gesture release velocity for spring-driven drawers and cards. */
export function clampDrawerVelocity(velocityPxPerSec: number, spanPx: number): number {
  if (!Number.isFinite(velocityPxPerSec) || !Number.isFinite(spanPx) || spanPx <= 0) return 0;
  const normalized = velocityPxPerSec / spanPx;
  return Math.max(-12, Math.min(12, normalized));
}
