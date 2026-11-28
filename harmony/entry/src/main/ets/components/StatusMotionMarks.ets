import { FrameCallback } from '@kit.ArkUI';
import { MotionTokens, shouldAnimateLoop } from '../core/MotionTokens';
import { Colors } from '../design/Colors';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

class StatusMotionFrame extends FrameCallback {
  private action: () => void;
  constructor(action: () => void) { super(); this.action = action; }
  onFrame(_time: number): void { this.action(); }
}

/**
 * Breathing status dot matching iOS `BreathingRing` (apple/Persona/DesignSystem/BreathingRing.swift).
 * Pauses off-screen, in background, and under Reduce Motion (P9).
 */
@ComponentV2
export struct NeedsYouPulseDot {
  @Param dotSize: number = 10;
  @Param tint: ResourceColor = Colors.needs;
  @Param surface: ResourceColor = Colors.surface;
  @Param bordered: boolean = true;
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private visible: boolean = false;
  @Local private pulsing: boolean = false;
  private mounted: boolean = false;
  private generation: number = 0;

  aboutToAppear(): void { this.mounted = true; this.syncLoop(); }
  aboutToDisappear(): void { this.mounted = false; this.generation++; this.pulsing = false; }

  private eligible(): boolean {
    return this.mounted && shouldAnimateLoop(this.visible, this.env.foreground, this.env.reduceMotion);
  }

  @Monitor('visible', 'env.foreground') syncLoop(): void {
    const gen = ++this.generation;
    this.pulsing = false;
    if (!this.eligible()) return;
    this.getUIContext().postFrameCallback(new StatusMotionFrame(() => {
      if (gen === this.generation && this.eligible()) this.pulsing = true;
    }));
  }

  build() {
    Stack() {
      if (this.eligible()) {
        Circle()
          .width(this.dotSize + 4)
          .height(this.dotSize + 4)
          .fill(Color.Transparent)
          .stroke(this.tint)
          .strokeWidth(1.5)
          .scale({ x: this.pulsing ? MotionTokens.breathScale + 0.35 : 0.9, y: this.pulsing ? MotionTokens.breathScale + 0.35 : 0.9 })
          .opacity(this.pulsing ? 0 : MotionTokens.breathRingOpacity)
          .animation(this.pulsing ? {
            duration: MotionTokens.breathDurationMs,
            curve: Curve.EaseOut,
            iterations: -1,
            playMode: PlayMode.Normal
          } : undefined)
      }
      Circle()
        .width(this.dotSize)
        .height(this.dotSize)
        .fill(this.tint)
        .stroke(this.bordered ? this.surface : Color.Transparent)
        .strokeWidth(this.bordered ? 1.8 : 0)
    }
    .width(this.dotSize + 6)
    .height(this.dotSize + 6)
    .hitTestBehavior(HitTestMode.None)
    .accessibilityLevel('no-hide-descendants')
    .onVisibleAreaChange([0], (shown: boolean, ratio: number) => {
      const next = shown && ratio > 0;
      if (next !== this.visible) this.visible = next;
    })
  }
}

/**
 * Turning amber working arc matching iOS `WorkingArc` (apple/Persona/DesignSystem/WorkingArc.swift).
 * Freezes cleanly off-screen, in background, and under Reduce Motion (P9).
 */
@ComponentV2
export struct WorkingArcSpinner {
  @Param diameter: number = 16;
  @Param tint: ResourceColor = Colors.working;
  @Param surface: ResourceColor = Colors.surface;
  @Param showCoreDot: boolean = true;
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private visible: boolean = false;
  @Local private turning: boolean = false;
  private mounted: boolean = false;
  private generation: number = 0;

  aboutToAppear(): void { this.mounted = true; this.syncLoop(); }
  aboutToDisappear(): void { this.mounted = false; this.generation++; this.turning = false; }

  private eligible(): boolean {
    return this.mounted && shouldAnimateLoop(this.visible, this.env.foreground, this.env.reduceMotion);
  }

  @Monitor('visible', 'env.foreground') syncLoop(): void {
    const gen = ++this.generation;
    this.turning = false;
    if (!this.eligible()) return;
    this.getUIContext().postFrameCallback(new StatusMotionFrame(() => {
      if (gen === this.generation && this.eligible()) this.turning = true;
    }));
  }

  build() {
    Stack() {
      Circle()
        .width(this.diameter)
        .height(this.diameter)
        .fill(Color.Transparent)
        .stroke(this.tint)
        .strokeWidth(2)
        .opacity(0.24)
      Shape() {
        Path()
          .commands('M8 1.5 A6.5 6.5 0 1 1 2.1 10.6')
          .fill(Color.Transparent)
          .stroke(this.tint)
          .strokeWidth('2px')
          .strokeLineCap(LineCapStyle.Round)
      }
      .viewPort({ x: 0, y: 0, width: '16px', height: '16px' })
      .width(this.diameter)
      .height(this.diameter)
      .rotate({ angle: this.turning ? 360 : 0 })
      .animation(this.turning ? {
        duration: MotionTokens.workingArcTurnMs,
        curve: Curve.Linear,
        iterations: -1,
        playMode: PlayMode.Normal
      } : undefined)
      if (this.showCoreDot) {
        Circle()
          .width(Math.max(6, this.diameter * 0.45))
          .height(Math.max(6, this.diameter * 0.45))
          .fill(this.tint)
      }
    }
    .width(this.diameter + 2)
    .height(this.diameter + 2)
    .backgroundColor(this.surface)
    .borderRadius((this.diameter + 2) / 2)
    .hitTestBehavior(HitTestMode.None)
    .accessibilityLevel('no-hide-descendants')
    .onVisibleAreaChange([0], (shown: boolean, ratio: number) => {
      const next = shown && ratio > 0;
      if (next !== this.visible) this.visible = next;
    })
  }
}

/**
 * Streaming reply tail indicator matching iOS `StreamTail` (apple/Persona/Views/Chat/MessageViews.swift).
 * Soft pulsing pill at the end of a live reply; static under Reduce Motion, paused off-screen.
 */
@ComponentV2
export struct StreamTailCaret {
  @Param accent: ResourceColor = Colors.interfaceAccent;
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private visible: boolean = false;
  @Local private lit: boolean = false;
  private mounted: boolean = false;
  private generation: number = 0;

  aboutToAppear(): void { this.mounted = true; this.syncLoop(); }
  aboutToDisappear(): void { this.mounted = false; this.generation++; this.lit = false; }

  private eligible(): boolean {
    return this.mounted && shouldAnimateLoop(this.visible, this.env.foreground, this.env.reduceMotion);
  }

  @Monitor('visible', 'env.foreground') syncLoop(): void {
    const gen = ++this.generation;
    this.lit = false;
    if (!this.eligible()) return;
    this.getUIContext().postFrameCallback(new StatusMotionFrame(() => {
      if (gen === this.generation && this.eligible()) this.lit = true;
    }));
  }

  build() {
    Row({ space: 6 }) {
      Rect()
        .width(8)
        .height(14)
        .radius(4)
        .fill(this.accent)
        .opacity(this.eligible() ? (this.lit ? 0.95 : 0.25) : 0.65)
        .animation(this.eligible() && this.lit ? {
          duration: MotionTokens.caretPulseMs,
          curve: Curve.EaseInOut,
          iterations: -1,
          playMode: PlayMode.Alternate
        } : undefined)
    }
    .padding({ top: 2 })
    .hitTestBehavior(HitTestMode.None)
    .accessibilityLevel('no-hide-descendants')
    .id('chat.stream.caret')
    .onVisibleAreaChange([0], (shown: boolean, ratio: number) => {
      const next = shown && ratio > 0;
      if (next !== this.visible) this.visible = next;
    })
  }
}
