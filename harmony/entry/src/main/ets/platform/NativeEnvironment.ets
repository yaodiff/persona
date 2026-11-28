import { ConfigurationConstant } from '@kit.AbilityKit';
import type { Configuration } from '@kit.AbilityKit';
import { display, window } from '@kit.ArkUI';
import { accessibility } from '@kit.AccessibilityKit';
import { classifyFoldPosture, widthClassFromBreakpoint } from '../core/FoldPosture';
import type { FoldPostureKind } from '../core/FoldPosture';

/** Native application configuration, shared by workspace and detached views. */
@ObservedV2
export class NativeEnvironment {
  @Trace foreground: boolean = true;
  @Trace dark: boolean = false;
  @Trace fontScale: number = 1;
  @Trace safeBottomPx: number = 0;
  @Trace safeTopPx: number = 0;
  @Trace keyboardHeightPx: number = 0;
  @Trace windowWidthPx: number = 0;
  @Trace windowHeightPx: number = 0;
  @Trace isFullScreen: boolean = false;
  @Trace isFoldable: boolean = false;
  @Trace foldStatus: number = 0;
  @Trace foldDisplayMode: number = 0;
  @Trace foldPosture: FoldPostureKind = 'straight';
  @Trace isTriFold: boolean = false;
  private lastWidthVp: number = 0;
  private lastHeightVp: number = 0;
  private lastWidthBreakpoint: number = -1;
  private windowRef: window.Window | undefined;
  private keyboardChanged: ((height: number) => void) | undefined;
  private sizeChanged: (size: window.Size) => void = (_size: window.Size): void => this.refreshWindow();
  private rectChanged: (event: window.RectChangeOptions) => void = (_event: window.RectChangeOptions): void => this.refreshWindow();
  private avoidChanged: (event: window.AvoidAreaOptions) => void = (_event: window.AvoidAreaOptions): void => this.refreshWindow();
  private foldStatusListener: ((status: display.FoldStatus) => void) | undefined;
  private foldModeListener: ((mode: display.FoldDisplayMode) => void) | undefined;

  get reduceMotion(): boolean {
    try { return accessibility.isAnimationReduceEnabledSync(); } catch (_) { return false; }
  }

  bindWindow(value: window.Window): void {
    this.unbindWindow(); this.windowRef = value;
    value.on('windowSizeChange', this.sizeChanged);
    value.on('windowRectChange', this.rectChanged);
    value.on('avoidAreaChange', this.avoidChanged);
    const changed = (height: number): void => { if (this.windowRef === value && this.keyboardChanged === changed) this.keyboardHeightPx = Number.isFinite(height) ? Math.max(0, height) : 0; };
    this.keyboardChanged = changed; value.on('keyboardHeightChange', changed);
    this.bindFoldListeners();
    this.refreshWindow();
    this.refreshFoldCapability();
  }
  unbindWindow(): void {
    const value = this.windowRef, keyboard = this.keyboardChanged; this.windowRef = undefined; this.keyboardChanged = undefined;
    this.keyboardHeightPx = 0; this.isFullScreen = false; this.windowHeightPx = 0;
    this.unbindFoldListeners();
    if (!value) return;
    try { value.off('windowSizeChange', this.sizeChanged); value.off('windowRectChange', this.rectChanged); value.off('avoidAreaChange', this.avoidChanged); if (keyboard) value.off('keyboardHeightChange', keyboard); } catch (_) {}
  }
  private bindFoldListeners(): void {
    try {
      if (!display.isFoldable()) return;
      const statusCb = (status: display.FoldStatus): void => {
        this.foldStatus = Number(status) || 0;
        this.refreshFoldCapability();
      };
      const modeCb = (mode: display.FoldDisplayMode): void => {
        this.foldDisplayMode = Number(mode) || 0;
        this.refreshFoldCapability();
      };
      this.foldStatusListener = statusCb;
      this.foldModeListener = modeCb;
      display.on('foldStatusChange', statusCb);
      display.on('foldDisplayModeChange', modeCb);
    } catch (_) {
      this.foldStatusListener = undefined;
      this.foldModeListener = undefined;
    }
  }
  private unbindFoldListeners(): void {
    const statusCb = this.foldStatusListener, modeCb = this.foldModeListener;
    this.foldStatusListener = undefined;
    this.foldModeListener = undefined;
    try { if (statusCb) display.off('foldStatusChange', statusCb); } catch (_) {}
    try { if (modeCb) display.off('foldDisplayModeChange', modeCb); } catch (_) {}
  }
  private refreshWindow(): void {
    if (!this.windowRef) return;
    try {
      const rect = this.windowRef.getWindowProperties().windowRect;
      const bottom = this.windowRef.getWindowAvoidArea(window.AvoidAreaType.TYPE_NAVIGATION_INDICATOR).bottomRect.height;
      this.safeBottomPx = rect.top + rect.height - bottom;
      this.keyboardHeightPx = this.windowRef.getWindowAvoidArea(window.AvoidAreaType.TYPE_KEYBOARD).bottomRect?.height || 0;
      const nextWidth = Number.isFinite(rect.width) ? rect.width : 0;
      const nextHeight = Number.isFinite(rect.height) ? rect.height : 0;
      if (nextWidth !== this.windowWidthPx || nextHeight !== this.windowHeightPx) {
        this.lastWidthVp = 0;
        this.lastHeightVp = 0;
        this.lastWidthBreakpoint = -1;
      }
      this.windowWidthPx = nextWidth;
      this.windowHeightPx = nextHeight;
      this.safeTopPx = this.windowRef.getWindowAvoidArea(window.AvoidAreaType.TYPE_SYSTEM).topRect?.height || 0;
      this.isFullScreen = this.windowRef.getWindowStatus() === window.WindowStatusType.FULL_SCREEN;
      this.syncPosture();
    } catch (_) {}
  }

  @Trace nativeHoverAvailable: boolean = false;
  @Trace creaseTopPx: number = 0;
  @Trace creaseHeightPx: number = 0;
  private refreshFoldCapability(): void {
    // Some emulator images report foldable but omit the crease region. The native
    // FoldSplitContainer dereferences that region when entering hover mode.
    try {
      const foldable = display.isFoldable();
      this.isFoldable = foldable;
      if (foldable) {
        try { this.foldStatus = Number(display.getFoldStatus()) || 0; } catch (_) {}
        try { this.foldDisplayMode = Number(display.getFoldDisplayMode()) || 0; } catch (_) {}
      } else {
        this.foldStatus = 0;
        this.foldDisplayMode = 0;
      }
      const region = foldable ? display.getCurrentFoldCreaseRegion() : undefined;
      const valid = !!region && region.creaseRects.length > 0 && region.creaseRects.every((rect: display.Rect): boolean =>
        Number.isFinite(rect.left) && Number.isFinite(rect.top) && Number.isFinite(rect.width) && Number.isFinite(rect.height) && rect.width > 0 && rect.height > 0);
      this.nativeHoverAvailable = valid;
      if (valid && region) {
        const horizontal = region.creaseRects.find((rect: display.Rect): boolean => rect.width >= rect.height) || region.creaseRects[0];
        this.creaseTopPx = horizontal.top;
        this.creaseHeightPx = horizontal.height;
      } else {
        this.creaseTopPx = 0;
        this.creaseHeightPx = 0;
      }
    } catch (_) {
      this.isFoldable = false;
      this.foldStatus = 0;
      this.foldDisplayMode = 0;
      this.nativeHoverAvailable = false;
      this.creaseTopPx = 0;
      this.creaseHeightPx = 0;
    }
    this.syncPosture();
  }
  private displayDensity(): number {
    try {
      const density = display.getDefaultDisplaySync().densityPixels;
      if (Number.isFinite(density) && density > 0) return density;
    } catch (_) {}
    return 3.2;
  }
  syncPosture(widthVp: number = 0, heightVp: number = 0, widthBreakpoint: number = -1): void {
    if (widthVp > 0) this.lastWidthVp = widthVp;
    if (heightVp > 0) this.lastHeightVp = heightVp;
    if (widthBreakpoint >= 0) this.lastWidthBreakpoint = widthBreakpoint;
    const density = this.displayDensity();
    const fallbackWidth = widthVp > 0 ? widthVp : (this.lastWidthVp > 0 ? this.lastWidthVp : (this.windowWidthPx > 0 ? this.windowWidthPx / density : 0));
    const fallbackHeight = heightVp > 0 ? heightVp : (this.lastHeightVp > 0 ? this.lastHeightVp : (this.windowHeightPx > 0 ? this.windowHeightPx / density : 0));
    const inferredBreakpoint = fallbackWidth >= 1440 ? 4 : fallbackWidth >= 840 ? 3 : fallbackWidth >= 600 ? 2 : fallbackWidth >= 320 ? 1 : 0;
    const resolvedBreakpoint = widthBreakpoint >= 0 ? widthBreakpoint : (this.lastWidthBreakpoint >= 0 ? this.lastWidthBreakpoint : (fallbackWidth > 0 ? inferredBreakpoint : 1));
    this.foldPosture = classifyFoldPosture({
      widthClass: widthClassFromBreakpoint(resolvedBreakpoint),
      windowWidthVp: fallbackWidth,
      windowHeightVp: fallbackHeight,
      isFoldable: this.isFoldable,
      foldStatus: this.foldStatus,
      foldDisplayMode: this.foldDisplayMode,
      nativeHoverAvailable: this.nativeHoverAvailable
    });
    this.isTriFold = this.foldPosture === 'trifold_unfolded' ||
      this.foldPosture === 'trifold_angled';
  }
  update(configuration: Configuration): void {
    if (configuration.colorMode === ConfigurationConstant.ColorMode.COLOR_MODE_DARK) this.dark = true;
    else if (configuration.colorMode === ConfigurationConstant.ColorMode.COLOR_MODE_LIGHT) this.dark = false;
    this.refreshFoldCapability();
    const scale = configuration.fontSizeScale;
    if (typeof scale === 'number' && Number.isFinite(scale) && scale > 0) this.fontScale = scale;
  }
}
export const nativeEnvironment: NativeEnvironment = new NativeEnvironment();
