import { Colors } from './Colors';
import { Motion } from './Motion';
import { MotionTokens } from '../core/MotionTokens';
import { PhoneOutlineIcon } from './PhoneOutlineIcon';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';
import { Tokens } from './Tokens';

// Compact iPhone's inset lists share their margins, hierarchy and grouped surfaces with the phone shell.
export class PhoneStyle {
  static surfaceShadow(fallback: ShadowOptions): ShadowOptions {
    if (Colors.styleID === 'clay') return {radius:12,color:Colors.styleShadow,offsetX:5,offsetY:8};
    if (Colors.styleID === 'island') return {radius:20,color:Colors.styleShadow,offsetY:10};
    if (Colors.styleID === 'lark') return {radius:0,color:Color.Transparent};
    return fallback;
  }
  static cardElevation(_dark: boolean): ShadowOptions {
    return PhoneStyle.surfaceShadow({ radius: 0, color: Color.Transparent });
  }
  static floatingElevation(dark: boolean): ShadowOptions {
    return PhoneStyle.surfaceShadow({
      radius: dark ? 14 : 12,
      color: $r('app.color.persona_floating_shadow'),
      offsetY: dark ? 4 : 3
    });
  }
  static heroElevation(dark: boolean): ShadowOptions {
    return PhoneStyle.surfaceShadow({
      radius: dark ? 18 : 16,
      color: $r('app.color.persona_hero_shadow'),
      offsetY: dark ? 6 : 4
    });
  }
  static cardBorder(_dark: boolean): ResourceColor {
    return Colors.styleEdge || Color.Transparent;
  }
  static floatingBorder(dark: boolean): ResourceColor {
    return Colors.styleEdge || (dark ? Colors.glassRim : Colors.hairlineGlass);
  }
  static surfaceEdgeWidth(fallback: number): number {
    return Colors.styleID === 'clay' ? 1 : Colors.styleID === 'island' ? 0 : fallback;
  }
  static readonly page: number = 16;
  static readonly cardRadius: number = 15;
  static readonly inset: number = 14;
  static readonly detailListTop: number = 36;
  static readonly rowHeight: number = 48;
  static readonly bar: number = 44;
  static readonly title: number = 34;
  static readonly recapSheetWidth: Dimension = '96%';
  static readonly recapSheetHeight: string = '55%';
  static readonly recapSheetSurfaceHeight: string = 'calc(100% - 8vp)';
  static readonly recapSheetRadius: number = 44;
  static readonly recapBarHeight: number = 58;
  static readonly recapTitleMaxScale: number = 1.2;
  static recapCloseGlyph(scale: number): string { return String(PhoneStyle.formSize(24.5, 30.5, scale)) + 'vp'; }

  // Grouped form controls follow the capped interface text, including their vertical padding.
  static formSize(regular: number, largest: number, scale: number): number {
    const progress = Math.max(0, Math.min(1, (scale - 1) / (Tokens.interfaceFontMaxScale - 1)));
    return regular + (largest - regular) * progress;
  }
  // The example form uses the native body and footnote line boxes while preserving the chosen text size.
  static settingsExampleLineHeight(regular: number, largest: number, scale: number): string {
    return String(PhoneStyle.formSize(regular,largest,scale)*Math.min(scale,1))+'vp';
  }
  // The compact sidebar uses the capped body metric for both symbols and touch targets.
  static sidebarMetric(points: number, scale: number): number { return points * Math.min(scale,57.85/44); }
  static groupedRowHeight(scale: number): number { return PhoneStyle.formSize(52, 59, scale); }
  static readonly settingsGroupRadius: number = 28;
  static get settingsLabel(): ResourceColor {
    return Colors.systemControls && Colors.styleID === 'glass' ? $r('app.color.persona_switcher_ink') : Colors.ink;
  }
  static get settingsSecondary(): ResourceColor {
    return Colors.systemControls && Colors.styleID === 'glass' ? $r('app.color.project_card_secondary') : Colors.secondary;
  }
  static get settingsSeparator(): ResourceColor {
    return Colors.systemControls && Colors.styleID === 'glass' ? $r('app.color.phone_settings_separator') : Colors.divider;
  }

}

export type NavigationMark = 'navigationBack' | 'navigationClose';

@ComponentV2
export struct PhoneNavigationIcon {
  @Require @Param mark: NavigationMark;
  @Param tint: ResourceColor = Colors.ink;
  @Local private environment: NativeEnvironment = nativeEnvironment;

  build() {
    PhoneOutlineIcon({name:this.mark,color:this.tint,iconScale:PhoneStyle.formSize(1,1.25,this.environment.fontScale)})
  }
}

@ComponentV2
export struct PhoneIconButton {
  @Require @Param symbol: Resource | NavigationMark;
  @Require @Param label: string;
  @Param identity: string = '';
  @Param controlDiameter: number = PhoneStyle.bar;
  @Param tint: ResourceColor = Colors.ink;
  @Param active: boolean = true;
  @Event action: () => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private pressed: boolean = false;
  build() {
    Button({ type: ButtonType.Circle }) {
      if (typeof this.symbol === 'string') {
        PhoneNavigationIcon({mark:this.symbol,tint:this.tint})
      } else {
        SymbolGlyph(this.symbol).fontSize(String(19 * this.controlDiameter / PhoneStyle.bar) + 'vp').fontColor([this.tint])
      }
    }.width(this.controlDiameter).height(this.controlDiameter).padding(0)
      .backgroundColor(Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface)
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .border({ width: PhoneStyle.surfaceEdgeWidth(.5), color: PhoneStyle.floatingBorder(this.env.dark), radius: this.controlDiameter / 2 })
      .shadow(PhoneStyle.floatingElevation(this.env.dark))
      .scale({ x: this.pressed ? MotionTokens.pressScale : 1, y: this.pressed ? MotionTokens.pressScale : 1 })
      .animation(Motion.release(this.env.reduceMotion))
      .onTouch((event: TouchEvent) => {
        if (event.type === TouchType.Down) this.pressed = true;
        else if (event.type === TouchType.Up || event.type === TouchType.Cancel) this.pressed = false;
      })
      .accessibilityText(this.label).bindTips(this.label).enabled(this.active).onClick(() => { Haptics.tap(); this.action(); }).id(this.identity)
  }
}

@ComponentV2
export struct PhoneSectionTitle {
  @Require @Param text: string;
  @Param trailing: string = '';
  build() {
    Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center}) {
      Text(this.text).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium)
        .fontColor(Colors.secondary).flexGrow(1).margin({right:6})
      if (this.trailing) Text(this.trailing).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
    }.width('100%').padding({ left: 6, right: 6, top: 14, bottom: 7 })
  }
}

@ComponentV2
export struct PhoneSettingsRow {
  @Require @Param label: string;
  @Param elevated: boolean = false;
  @Param insetGrouped: boolean = false;
  @Require @Param symbol: Resource;
  @Param artwork:Resource|undefined=undefined;
  @Require @Param tint: ResourceColor;
  @Param identity: string = '';
  @Param detail: string = '';
  @Param last: boolean = false;
  @Event action: () => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private pressed: boolean = false;
  @Computed get symbolScale():number { return Math.min(this.env.fontScale,Tokens.interfaceFontMaxScale); }
  build() {
    Column() {
      Row({ space: PhoneStyle.formSize(8,5,this.env.fontScale) }) {
        if (this.artwork) {
          Image(this.artwork).supportSvg2(true).colorFilter(this.tint)
            .width(40*this.symbolScale).height(28*this.symbolScale).objectFit(ImageFit.Contain).accessibilityLevel('no')
        } else {
          SymbolGlyph(this.symbol).fontSize(String(24*this.symbolScale)+'vp').fontColor([this.tint])
            .width(40*this.symbolScale).height(28*this.symbolScale).accessibilityLevel('no')
        }
        Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center}) {
          Text(this.label).fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(this.insetGrouped ? PhoneStyle.settingsLabel : Colors.ink)
            .flexGrow(1).margin({right:4})
          if (this.detail) Text(this.detail).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
        }.layoutWeight(1)
        if (this.insetGrouped) {
          PhoneOutlineIcon({name:'forward',color:Colors.tertiary})
            .scale({x:this.symbolScale,y:this.symbolScale}).accessibilityLevel('no')
        } else SymbolGlyph($r('sys.symbol.chevron_right')).fontSize('14vp').fontColor([Colors.secondary])
      }.width('100%').constraintSize({ minHeight: PhoneStyle.groupedRowHeight(this.env.fontScale) })
        .padding({ left: PhoneStyle.formSize(12,9,this.env.fontScale), right: this.insetGrouped ? 22 : 14, top: 5, bottom: 5 })
      if (!this.last) Divider().color(this.insetGrouped ? PhoneStyle.settingsSeparator : Colors.divider).strokeWidth(0.5)
        .margin({ left: PhoneStyle.formSize(60,68,this.env.fontScale),right:this.insetGrouped ? 20 : 0 })
    }.width('100%')
      .backgroundColor(this.pressed ? Colors.pressOverlay : Colors.card(this.elevated))
      .animation(Motion.press(this.env.reduceMotion))
      .onTouch((event: TouchEvent) => {
        if (event.type === TouchType.Down) this.pressed = true;
        else if (event.type === TouchType.Up || event.type === TouchType.Cancel) this.pressed = false;
      })
      .accessibilityGroup(true)
      .accessibilityText(this.label + (this.detail ? ' · ' + this.detail : ''))
      .onClick(() => { Haptics.tap(); this.action(); }).id(this.identity)
  }
}
