import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { PhoneStyle } from '../design/PhoneStyle';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';

@ComponentV2
export struct PhoneFirstRun {
  @Param error: string = '';
  @Param pendingCleanup: boolean = false;
  @Event onScan: () => void = () => {};
  @Event onManual: () => void = () => {};
  @Event onDemo: () => void = () => {};
  @Event onCleanup: () => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private viewportWidth: number = 402;
  @Local private pressedId: string = '';

  private metric(standard: number, largest: number): number {
    return PhoneStyle.formSize(standard, largest, this.env.fontScale);
  }

  private ink(): ResourceColor {
    return this.env.dark ? Color.White : Color.Black;
  }

  private help(): void {
    this.getUIContext().showAlertDialog({
      title: tr('welcome_action_help'),
      message: tr('welcome_action_hint'),
      alignment: DialogAlignment.Center,
      width: Math.min(340, Math.max(0, this.viewportWidth - 32)),
      cornerRadius: 28,
      primaryButton: { value: tr('welcome_action_done'), action: () => {} }
    });
  }

  @Builder
  shortcut(kind: string, image: Resource, action: () => void) {
    Button({ type: ButtonType.Normal }) {
      Column({ space: 8 }) {
        Stack() {
          SymbolGlyph(image).fontSize(this.metric(20, 26) + 'vp')
            .fontColor([kind === 'demo' ? Colors.link : this.ink()]).accessibilityLevel('no')
        }
        .width(this.metric(50, 60))
        .aspectRatio(1)
        .borderRadius(30)
        .backgroundColor(Colors.control)

        Text(tr('welcome_action_' + kind))
          .fontSize(this.metric(13, 18) + 'vp')
          .lineHeight(this.metric(17, 23) + 'vp')
          .fontWeight(FontWeight.Medium)
          .fontColor(this.ink())
          .maxLines(1)
      }
    }
    .padding({ left: 4, right: 4, top: 2, bottom: 2 })
    .backgroundColor(Color.Transparent)
    .accessibilityText(tr('welcome_action_' + kind))
    .scale({ x: this.pressedId === kind ? MotionTokens.pressScale : 1, y: this.pressedId === kind ? MotionTokens.pressScale : 1 })
    .animation(Motion.release(this.env.reduceMotion))
    .onTouch((event: TouchEvent) => {
      if (event.type === TouchType.Down) this.pressedId = kind;
      else if (event.type === TouchType.Up || event.type === TouchType.Cancel) this.pressedId = '';
    })
    .onClick(() => { Haptics.tap(); action(); })
    .id('welcome.' + kind)
  }

  @Builder
  actions() {
    this.shortcut('other', $r('sys.symbol.keyboard'), () => this.onManual())
    this.shortcut('help', $r('sys.symbol.questionmark_circle'), () => this.help())
    this.shortcut('demo', $r('sys.symbol.play_circle_fill'), () => this.onDemo())
  }

  @Builder
  heroContent() {
    Column({ space: 28 }) {
      Column({ space: 14 }) {
        Image(this.env.dark ? $r('app.media.welcome_app_icon_dark') : $r('app.media.welcome_app_icon'))
          .width(80)
          .height(80)
          .borderRadius(18)
          .objectFit(ImageFit.Cover)
          .accessibilityLevel('no')

        Column({ space: 6 }) {
          Text(tr('persona'))
            .fontSize(this.metric(28, 34) + 'vp')
            .lineHeight(this.metric(34, 40) + 'vp')
            .fontWeight(FontWeight.Bold)
            .fontColor(this.ink())
          Text(tr('phone_welcome_intro'))
            .fontSize(this.metric(15, 20) + 'vp')
            .lineHeight(this.metric(21, 27) + 'vp')
            .fontColor(Colors.secondary)
            .textAlign(TextAlign.Center)
            .constraintSize({ maxWidth: 300 })
        }.alignItems(HorizontalAlign.Center)
      }.alignItems(HorizontalAlign.Center)

      Button({ type: ButtonType.Capsule }) {
        Row({ space: 9 }) {
          SymbolGlyph($r('sys.symbol.qrcode')).fontSize(this.metric(18, 24) + 'vp')
            .fontColor([Color.White]).accessibilityLevel('no')
          Text(tr('welcome_action_scan')).fontSize(this.metric(17, 22) + 'vp').fontWeight(600)
            .fontColor(Color.White).maxLines(1)
        }
      }
      .width('100%')
      .height(this.metric(54, 64))
      .padding({ left: 16, right: 16 })
      .backgroundColor(this.env.dark ? '#0A84FF' : '#007AFF')
      .scale({ x: this.pressedId === 'connect' ? MotionTokens.pressScale : 1, y: this.pressedId === 'connect' ? MotionTokens.pressScale : 1 })
      .animation(Motion.release(this.env.reduceMotion))
      .onTouch((event: TouchEvent) => {
        if (event.type === TouchType.Down) this.pressedId = 'connect';
        else if (event.type === TouchType.Up || event.type === TouchType.Cancel) this.pressedId = '';
      })
      .accessibilityText(tr('welcome_action_scan'))
      .onClick(() => { Haptics.tap(); this.onScan(); })
      .id('welcome.connect')

      if (this.viewportWidth >= this.metric(260, 330)) {
        Row({ space: 28 }) { this.actions() }.justifyContent(FlexAlign.Center)
      } else {
        Column({ space: 10 }) { this.actions() }
      }

      if (this.error) {
        Row({ space: 8 }) {
          SymbolGlyph($r('sys.symbol.exclamationmark_triangle')).fontSize('16vp').fontColor([Colors.danger])
          Text(this.error).fontSize(14).fontColor(Colors.danger).layoutWeight(1)
        }
        .width('100%')
        .padding({ left: 14, right: 14, top: 10, bottom: 10 })
        .backgroundColor(this.env.dark ? '#28FF453A' : '#14BA1A1A')
        .borderRadius(14)
      }
      if (this.pendingCleanup) {
        Button(tr('retry_cleanup'), { buttonStyle: ButtonStyleMode.TEXTUAL }).onClick(() => { Haptics.tap(); this.onCleanup(); })
      }
    }
    .width('100%')
    .constraintSize({ maxWidth: 360 })
    .padding({ left: 24, right: 24, top: 40, bottom: 28 })
  }

  build() {
    Scroll() {
      Column() {
        this.heroContent()
      }
      .width('100%')
      .constraintSize({ minHeight: '100%' })
      .justifyContent(FlexAlign.Center)
      .alignItems(HorizontalAlign.Center)
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.canvas)
    .scrollBar(BarState.Off)
    .onAreaChange((_: Area, area: Area) => { this.viewportWidth = Number(area.width); })
  }
}
