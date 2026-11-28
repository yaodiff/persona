import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { PhoneStyle } from '../design/PhoneStyle';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { ToastCenter, toastCenter } from '../models/ToastCenter';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';
import { WorkingArcSpinner } from './StatusMotionMarks';

/**
 * Floating frosted-glass toast & connection status overlay matching iOS `Toasts.swift` / `Notice.swift`.
 * Never shifts underlying layout and lets touches pass through everywhere outside the capsule.
 */
@ComponentV2
export struct FloatingToastOverlay {
  @Param reconnecting: boolean = false;
  @Param topOffset: number = 12;
  @Local private toasts: ToastCenter = toastCenter;
  @Local private env: NativeEnvironment = nativeEnvironment;

  private iconFor(kind: string): Resource {
    if (kind === 'error') return $r('sys.symbol.exclamationmark_triangle');
    if (kind === 'success') return $r('sys.symbol.checkmark_circle');
    return $r('sys.symbol.info_circle');
  }

  private tintFor(kind: string): ResourceColor {
    if (kind === 'error') return Colors.danger;
    if (kind === 'success') return Colors.positive;
    return Colors.link;
  }

  private iconWellFor(kind: string): ResourceColor {
    if (kind === 'error') return Colors.dangerWash;
    if (kind === 'success') return Colors.unreadWash;
    return Colors.needsWash;
  }

  build() {
    Column({ space: Tokens.spaceS }) {
      if (this.toasts.current) {
        Row({ space: 10 }) {
          Stack() {
            SymbolGlyph(this.iconFor(this.toasts.current.kind))
              .fontSize(Tokens.interfaceFontVp(16, this.env.fontScale))
              .fontColor([this.tintFor(this.toasts.current.kind)])
          }
          .width(28)
          .height(28)
          .borderRadius(Tokens.radiusCard)
          .backgroundColor(this.iconWellFor(this.toasts.current.kind))
          Text(this.toasts.current.message)
            .fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(3)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
            .flexShrink(1)
          if (this.toasts.current.actionTitle && this.toasts.current.onAction) {
            Button(this.toasts.current.actionTitle, { type: ButtonType.Capsule })
              .height(30)
              .padding({ left: 11, right: 11 })
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontWeight(FontWeight.Bold)
              .fontColor(Colors.link)
              .backgroundColor(Colors.selection)
              .onClick(() => {
                Haptics.tap();
                const cb = this.toasts.current?.onAction;
                this.toasts.dismiss();
                if (cb) cb();
              })
              .id('toast.action')
          }
        }
        .padding({ left: Tokens.spaceM, right: Tokens.spaceL, top: Tokens.spaceS, bottom: Tokens.spaceS })
        .constraintSize({ maxWidth: 420 })
        .backgroundColor(Colors.toastSurface)
        .backgroundBlurStyle(BlurStyle.COMPONENT_THICK)
        .borderRadius(Tokens.radiusCapsule)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.75), color: PhoneStyle.floatingBorder(this.env.dark), radius: Tokens.radiusCapsule })
        .shadow(PhoneStyle.heroElevation(this.env.dark))
        .onClick(() => this.toasts.dismiss())
        .transition(Motion.slideFade(this.env.reduceMotion, -16))
        .id('floating.toast')
      } else if (this.reconnecting) {
        Row({ space: Tokens.spaceS }) {
          WorkingArcSpinner({ diameter: 14, tint: Colors.working, surface: Color.Transparent, showCoreDot: false })
          Text(tr('connecting'))
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(1)
        }
        .padding({ left: 14, right: 14, top: Tokens.spaceS, bottom: Tokens.spaceS })
        .backgroundColor(Colors.toastSurface)
        .backgroundBlurStyle(BlurStyle.COMPONENT_THICK)
        .borderRadius(Tokens.radiusCapsule)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.75), color: PhoneStyle.floatingBorder(this.env.dark), radius: Tokens.radiusCapsule })
        .shadow(PhoneStyle.heroElevation(this.env.dark))
        .hitTestBehavior(HitTestMode.None)
        .transition(Motion.slideFade(this.env.reduceMotion, -14))
        .id('floating.connection')
      }
    }
    .width('100%')
    .padding({ left: 20, right: 20, top: this.topOffset })
    .alignItems(HorizontalAlign.Center)
    .hitTestBehavior(HitTestMode.Transparent)
  }
}
