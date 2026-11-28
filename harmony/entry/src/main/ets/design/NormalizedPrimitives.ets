import { Colors } from './Colors';
import { ButtonStyles } from './Controls';
import { NavigationMark, PhoneIconButton } from './PhoneStyle';
import { tr } from './Strings';
import { Tokens } from './Tokens';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

export type PersonaStatusTone = 'needs' | 'working' | 'unread' | 'danger' | 'ai' | 'teal' | 'neutral';

/**
 * Unified sheet navigation header aligned with iOS `SheetClose` (#9653) and ArkUI HIG:
 * leading >= 44vp close/back control, centered 17vp SemiBold title, optional subtitle,
 * optional trailing action or shortcut badge, and optional hairline divider.
 */
@ComponentV2
export struct PersonaSheetHeader {
  @Require @Param title: string;
  @Param subtitle: string = '';
  @Param closeIdentity: string = '';
  @Param closeLabel: string = '';
  @Param leadingMark: NavigationMark = 'navigationClose';
  @Param trailingLabel: string = '';
  @Param trailingIdentity: string = '';
  @Param trailingShortcut: string = '';
  @Param showDivider: boolean = false;
  @Event onClose: () => void = () => {};
  @Event onTrailing: () => void = () => {};

  build() {
    Column({ space: 0 }) {
      Row({ space: Tokens.spaceS }) {
        PhoneIconButton({
          symbol: this.leadingMark,
          label: this.closeLabel || tr('close'),
          identity: this.closeIdentity,
          controlDiameter: Tokens.controlBar,
          action: () => this.onClose()
        })

        Column({ space: Tokens.spaceXxs }) {
          Text(this.title)
            .fontSize(Tokens.typeHeading)
            .maxFontScale(Tokens.interfaceFontMaxScale)
            .fontWeight(600)
            .fontColor(Colors.ink)
            .textAlign(TextAlign.Center)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
          if (this.subtitle) {
            Text(this.subtitle)
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.secondary)
              .textAlign(TextAlign.Center)
              .maxLines(1)
              .textOverflow({ overflow: TextOverflow.Ellipsis })
          }
        }
        .layoutWeight(1)
        .alignItems(HorizontalAlign.Center)

        if (this.trailingLabel) {
          Button(this.trailingLabel, { type: ButtonType.Normal })
            .buttonStyle(ButtonStyleMode.TEXTUAL)
            .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
            .padding({ left: Tokens.spaceS, right: Tokens.spaceS })
            .fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.link)
            .backgroundColor(Color.Transparent)
            .responseRegion(ButtonStyles.touchRegion(Tokens.controlBar))
            .onClick(() => {
              Haptics.tap();
              this.onTrailing();
            })
            .id(this.trailingIdentity)
        } else if (this.trailingShortcut) {
          Row() {
            Text(this.trailingShortcut)
              .fontSize(Tokens.typeLabel)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
              .fontFamily('monospace')
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.secondary)
              .padding({ left: Tokens.spaceS, right: Tokens.spaceS, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
              .backgroundColor(Colors.control)
              .borderRadius(Tokens.radiusCapsule)
              .maxLines(1)
          }
          .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
          .justifyContent(FlexAlign.End)
          .alignItems(VerticalAlign.Center)
        } else {
          Blank().width(Tokens.controlBar).accessibilityLevel('no')
        }
      }
      .width('100%')
      .constraintSize({ minHeight: Tokens.controlBar })
      .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceSm })
      .alignItems(VerticalAlign.Center)

      if (this.showDivider) {
        Divider().strokeWidth(Tokens.divider).color(Colors.hairlineGlass)
      }
    }
    .width('100%')
  }
}

/**
 * Semantic status pill with paired foreground and wash tokens.
 */
@ComponentV2
export struct PersonaStatusPill {
  @Require @Param tone: PersonaStatusTone;
  @Require @Param label: string;
  @Param symbol?: Resource = undefined;
  @Param showDot: boolean = false;
  @Param identity: string = '';
  @Local private env: NativeEnvironment = nativeEnvironment;

  private foreground(): ResourceColor {
    if (this.tone === 'needs') return Colors.needs;
    if (this.tone === 'working') return Colors.working;
    if (this.tone === 'unread') return Colors.unread;
    if (this.tone === 'danger') return Colors.danger;
    if (this.tone === 'ai') return Colors.ai;
    if (this.tone === 'teal') return Colors.teal;
    return Colors.secondary;
  }

  private wash(): ResourceColor {
    if (this.tone === 'needs') return Colors.needsWash;
    if (this.tone === 'working') return Colors.workingWash;
    if (this.tone === 'unread') return Colors.unreadWash;
    if (this.tone === 'danger') return Colors.dangerWash;
    if (this.tone === 'ai') return Colors.aiWash;
    if (this.tone === 'teal') return Colors.tealWash;
    return Colors.control;
  }

  build() {
    Row({ space: Tokens.spaceXs }) {
      if (this.symbol) {
        SymbolGlyph(this.symbol)
          .fontSize(Tokens.interfaceFontVp(Tokens.typeLabel, this.env.fontScale))
          .fontColor([this.foreground()])
      } else if (this.showDot) {
        Circle()
          .width(Tokens.spaceSm)
          .height(Tokens.spaceSm)
          .fill(this.foreground())
      }
      Text(this.label)
        .fontSize(Tokens.typeLabel)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
        .fontWeight(FontWeight.Medium)
        .fontColor(this.foreground())
        .maxLines(1)
        .textOverflow({ overflow: TextOverflow.Ellipsis })
    }
    .padding({ left: Tokens.spaceS, right: Tokens.spaceS, top: Tokens.spaceXxs + 1, bottom: Tokens.spaceXxs + 1 })
    .backgroundColor(this.wash())
    .borderRadius(Tokens.radiusCapsule)
    .alignItems(VerticalAlign.Center)
    .flexShrink(0)
    .id(this.identity)
  }
}

/**
 * Normalized section header matching iOS `Theme.Font.sectionHeader` (13vp SemiBold).
 */
@ComponentV2
export struct PersonaSectionLabel {
  @Require @Param text: string;
  @Param trailing: string = '';

  build() {
    Flex({ wrap: FlexWrap.Wrap, alignItems: ItemAlign.Center }) {
      Text(this.text)
        .fontSize(Tokens.typeSectionHeader)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSectionHeader))
        .fontWeight(600)
        .fontColor(Colors.secondary)
        .flexGrow(1)
        .margin({ right: Tokens.spaceSm })
      if (this.trailing) {
        Text(this.trailing)
          .fontSize(Tokens.typeLabel)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
          .fontColor(Colors.secondary)
          .maxLines(1)
      }
    }
    .width('100%')
    .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs, top: Tokens.spaceS, bottom: Tokens.spaceXs })
  }
}
