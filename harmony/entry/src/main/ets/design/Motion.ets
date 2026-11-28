import { curves, UIContext } from '@kit.ArkUI';
import { MotionTokens, clampDrawerVelocity, resolveMotionDuration } from '../core/MotionTokens';
import { nativeEnvironment } from '../platform/NativeEnvironment';

/**
 * ArkUI motion helpers aligned to iOS Theme.Motion (apple/Shared/Design/Theme.swift).
 * Every helper honors `nativeEnvironment.reduceMotion` so users with Reduce Motion enabled
 * get instant, jump-free updates.
 */
export class Motion {
  /** Snappy 250ms transition for state changes, chip selection, and fold toggles. */
  static state(reduced: boolean = nativeEnvironment.reduceMotion): AnimateParam | undefined {
    const duration = resolveMotionDuration(MotionTokens.stateMs, reduced);
    if (duration === 0) return undefined;
    return { duration: duration, curve: curves.cubicBezierCurve(0.2, 0.0, 0.0, 1.0) };
  }

  /** Smooth 300ms transition for layout reflows, search bar reveal, and card height changes. */
  static layout(reduced: boolean = nativeEnvironment.reduceMotion): AnimateParam | undefined {
    const duration = resolveMotionDuration(MotionTokens.layoutMs, reduced);
    if (duration === 0) return undefined;
    return { duration: duration, curve: curves.cubicBezierCurve(0.25, 0.1, 0.25, 1.0) };
  }

  /** Reveal / conceal timings for floating buttons, overlays, and lifted message menus. */
  static reveal(reduced: boolean = nativeEnvironment.reduceMotion): AnimateParam | undefined {
    const duration = resolveMotionDuration(MotionTokens.revealMs, reduced);
    if (duration === 0) return undefined;
    return { duration: duration, curve: Curve.EaseOut };
  }

  static conceal(reduced: boolean = nativeEnvironment.reduceMotion): AnimateParam | undefined {
    const duration = resolveMotionDuration(MotionTokens.concealMs, reduced);
    if (duration === 0) return undefined;
    return { duration: duration, curve: Curve.EaseIn };
  }

  /** Subtle spring feedback when pressing or releasing interactive pills and cards. */
  static press(reduced: boolean = nativeEnvironment.reduceMotion): AnimateParam | undefined {
    const duration = resolveMotionDuration(MotionTokens.pressMs, reduced);
    if (duration === 0) return undefined;
    return { duration: duration, curve: Curve.EaseOut };
  }

  static release(reduced: boolean = nativeEnvironment.reduceMotion): AnimateParam | undefined {
    const duration = resolveMotionDuration(MotionTokens.releaseMs, reduced);
    if (duration === 0) return undefined;
    return { duration: duration, curve: curves.responsiveSpringMotion(0.22, 0.88) };
  }

  /** Critically-damped drawer spring inheriting finger release velocity (Theme.Motion.drawer). */
  static drawer(reduced: boolean = nativeEnvironment.reduceMotion, velocityPxPerSec: number = 0,
    spanPx: number = 320, onFinish?: () => void): AnimateParam {
    const duration = resolveMotionDuration(MotionTokens.drawerMs, reduced);
    if (duration === 0) return { duration: 0, onFinish: onFinish };
    const normalized = clampDrawerVelocity(velocityPxPerSec, spanPx);
    return {
      duration: duration,
      curve: curves.interpolatingSpring(normalized, 1, 385, 39.2),
      onFinish: onFinish
    };
  }

  /** Runs `update` inside `uiContext.animateTo` unless Reduce Motion is active. */
  static run(uiContext: UIContext | undefined, param: AnimateParam | undefined, update: () => void): void {
    if (!uiContext || !param || nativeEnvironment.reduceMotion) {
      update();
      return;
    }
    try {
      uiContext.animateTo(param, update);
    } catch (_) {
      update();
    }
  }

  /** Scale + fade transition for floating controls (Jump to Latest, Search bar, Lift menu). */
  static fadeScale(reduced: boolean = nativeEnvironment.reduceMotion, scaleFrom: number = 0.92): TransitionEffect {
    if (reduced) return TransitionEffect.IDENTITY;
    return TransitionEffect.OPACITY.combine(TransitionEffect.scale({ x: scaleFrom, y: scaleFrom }))
      .animation({ duration: MotionTokens.stateMs, curve: Curve.EaseOut });
  }

  /** Vertical reveal transition for collapsible search bars and inline disclosures. */
  static slideFade(reduced: boolean = nativeEnvironment.reduceMotion, offsetY: number = -10): TransitionEffect {
    if (reduced) return TransitionEffect.IDENTITY;
    return TransitionEffect.OPACITY.combine(TransitionEffect.translate({ y: offsetY }))
      .animation({ duration: MotionTokens.stateMs, curve: curves.cubicBezierCurve(0.2, 0.0, 0.0, 1.0) });
  }
}
