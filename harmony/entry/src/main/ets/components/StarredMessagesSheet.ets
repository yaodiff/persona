import { pasteboard } from '@kit.BasicServicesKit';
import {
  filterStarredSnapshots,
  isVerifiedStarSource,
  starSpeakerLabel,
  StarredSnapshot
} from '../core/StarredMessages';
import { Colors } from '../design/Colors';
import { IconActions } from '../design/IconActions';
import { PersonaSheetHeader, PersonaStatusPill } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { ClientModel } from '../models/ClientModel';
import { starredStore, StarredStore } from '../models/StarredStore';
import { toastCenter } from '../models/ToastCenter';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { MarkdownText } from './MarkdownText';

/**
 * Secondary sheet showing all starred message snapshots across Assistant & Agent chats,
 * matching iOS `StarredMessagesPage.swift`.
 */
@ComponentV2
export struct StarredMessagesSheet {
  @Require @Param model: ClientModel;
  @Event onOpenOriginal: (star: StarredSnapshot) => void = () => {};
  @Event onClose: () => void = () => {};

  @Local private store: StarredStore = starredStore;
  @Local private query: string = '';
  @Local private expandedIds: string[] = [];
  @Local private checkingId: string = '';
  @Local private unavailableIds: string[] = [];
  @Local private failedCheckIds: string[] = [];
  @Local private env: NativeEnvironment = nativeEnvironment;

  aboutToAppear(): void {
    this.store.startReading(this.model);
  }

  aboutToDisappear(): void {
    this.store.stopReading();
  }

  @Computed get matches(): StarredSnapshot[] {
    return filterStarredSnapshots(
      this.store.snapshots,
      this.query,
      tr('chat_lift_me'),
      tr('assistant')
    );
  }

  private isLongText(text: string): boolean {
    return text.length > 420 || text.split('\n').length > 6;
  }

  private displayText(star: StarredSnapshot): string {
    if (!this.isLongText(star.text) || this.expandedIds.includes(star.id)) {
      return star.text;
    }
    const lines = star.text.split('\n').slice(0, 6).join('\n');
    return lines.slice(0, 420) + '…';
  }

  private toggleExpanded(id: string): void {
    Haptics.tap();
    if (this.expandedIds.includes(id)) {
      this.expandedIds = this.expandedIds.filter((entry: string) => entry !== id);
    } else {
      this.expandedIds = this.expandedIds.concat([id]);
    }
  }

  private formatTime(star: StarredSnapshot): string {
    const raw = star.written_at || star.starred_at || '';
    if (!raw) return '';
    const ms = Date.parse(raw);
    if (!Number.isFinite(ms)) return raw;
    try {
      return new Date(ms).toLocaleString();
    } catch (_) {
      return raw;
    }
  }

  private copyStar(star: StarredSnapshot): void {
    try {
      pasteboard.getSystemPasteboard().setDataSync(
        pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, star.text)
      );
      Haptics.selection();
      toastCenter.show(tr('copied'));
    } catch (_) {
      Haptics.error();
      toastCenter.error(tr('failed'));
    }
  }

  private async openOriginal(star: StarredSnapshot): Promise<void> {
    if (this.checkingId === star.id) return;
    this.checkingId = star.id;
    this.failedCheckIds = this.failedCheckIds.filter((id: string) => id !== star.id);
    try {
      const result = await this.store.verifyOriginal(this.model, star);
      if (result === 'verified') {
        Haptics.tap();
        this.onOpenOriginal(star);
      } else if (result === 'unavailable') {
        if (!this.unavailableIds.includes(star.id)) {
          this.unavailableIds = this.unavailableIds.concat([star.id]);
        }
      } else {
        if (!this.failedCheckIds.includes(star.id)) {
          this.failedCheckIds = this.failedCheckIds.concat([star.id]);
        }
      }
    } finally {
      if (this.checkingId === star.id) this.checkingId = '';
    }
  }

  @Builder searchBar() {
    Row({ space: Tokens.spaceS }) {
      SymbolGlyph(Symbols.search)
        .fontSize('15vp')
        .fontColor([Colors.secondary])
      TextInput({ text: this.query, placeholder: tr('starred_search_placeholder') })
        .layoutWeight(1)
        .height(Tokens.controlCompact)
        .fontSize(Tokens.typeSecondary)
        .fontColor(Colors.ink)
        .placeholderColor(Colors.secondary)
        .backgroundColor(Color.Transparent)
        .padding({ left: 0, right: 0 })
        .onChange((value: string) => {
          this.query = value;
        })
        .id('starred.search')
      if (this.query.length > 0) {
        Button({ type: ButtonType.Circle }) {
          SymbolGlyph(IconActions.symbol('close'))
            .fontSize('13vp')
            .fontColor([Colors.secondary])
        }
        .width(28)
        .height(28)
        .padding(0)
        .backgroundColor(Color.Transparent)
        .responseRegion({ x: -8, y: -8, width: Tokens.controlBar, height: Tokens.controlBar })
        .accessibilityText(tr('clear'))
        .onClick(() => {
          this.query = '';
        })
        .id('starred.search.clear')
      }
    }
    .width('100%')
    .padding({ left: Tokens.spaceM, right: Tokens.spaceS, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
    .backgroundColor(Colors.control)
    .borderRadius(Tokens.radiusCard)
    .alignItems(VerticalAlign.Center)
  }

  @Builder emptyState(icon: Resource, title: string, subtitle: string, showRetry: boolean = false) {
    Column({ space: Tokens.spaceS }) {
      Stack({ alignContent: Alignment.Center }) {
        SymbolGlyph(icon)
          .fontSize('26vp')
          .fontColor([showRetry ? Colors.danger : Colors.working])
      }
      .width(56)
      .height(56)
      .borderRadius(Tokens.radiusPanel)
      .backgroundColor(showRetry ? Colors.dangerWash : Colors.workingWash)

      Text(title)
        .fontSize(Tokens.typeHeading)
        .maxFontScale(Tokens.interfaceFontMaxScale)
        .fontWeight(600)
        .fontColor(Colors.ink)
        .textAlign(TextAlign.Center)

      if (subtitle) {
        Text(subtitle)
          .fontSize(Tokens.typeSecondary)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
          .fontColor(Colors.secondary)
          .textAlign(TextAlign.Center)
          .constraintSize({ maxWidth: 320 })
      }

      if (showRetry) {
        Button(tr('retry'), { type: ButtonType.Capsule })
          .height(Tokens.controlBar)
          .padding({ left: Tokens.spaceL, right: Tokens.spaceL })
          .fontSize(Tokens.typeSecondary)
          .fontWeight(FontWeight.Medium)
          .fontColor(Colors.onAction)
          .backgroundColor(Colors.action)
          .margin({ top: Tokens.spaceXs })
          .onClick(() => {
            Haptics.tap();
            void this.store.loadPage(this.model);
          })
          .id('starred.retry')
      }
    }
    .width('100%')
    .layoutWeight(1)
    .padding(Tokens.spaceXl)
    .justifyContent(FlexAlign.Center)
    .alignItems(HorizontalAlign.Center)
  }

  @Builder starCard(star: StarredSnapshot) {
    Column({ space: Tokens.spaceS }) {
      Row({ space: Tokens.spaceS }) {
        Text(star.title || tr('conversation'))
          .fontSize(Tokens.typeBody)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
          .fontWeight(600)
          .fontColor(Colors.ink)
          .maxLines(1)
          .textOverflow({ overflow: TextOverflow.Ellipsis })
          .layoutWeight(1)
        PersonaStatusPill({
          tone: star.conversation === 'agent' ? 'ai' : 'neutral',
          label: starSpeakerLabel(star, tr('chat_lift_me'), tr('assistant')),
          symbol: $r('sys.symbol.star_fill')
        })
      }
      .width('100%')
      .alignItems(VerticalAlign.Center)

      if (this.formatTime(star)) {
        Text(this.formatTime(star))
          .fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .fontColor(Colors.secondary)
          .maxLines(1)
      }

      Column() {
        MarkdownText({
          text: this.displayText(star),
          chatReply: false
        })
      }
      .width('100%')
      .alignItems(HorizontalAlign.Start)
      .id('starred.snapshot.text.' + star.id)

      if (this.isLongText(star.text)) {
        Button(tr(this.expandedIds.includes(star.id) ? 'bubble_show_less' : 'bubble_show_more'), { type: ButtonType.Normal })
          .buttonStyle(ButtonStyleMode.TEXTUAL)
          .fontSize(Tokens.typeCaption)
          .fontWeight(FontWeight.Medium)
          .fontColor(Colors.link)
          .backgroundColor(Color.Transparent)
          .padding(0)
          .constraintSize({ minHeight: Tokens.controlBar })
          .onClick(() => this.toggleExpanded(star.id))
      }

      if (star.omitted_chars && star.omitted_chars > 0) {
        Text(tr('starred_omitted_chars').replace('{count}', String(star.omitted_chars)))
          .fontSize(Tokens.typeCaption)
          .fontColor(Colors.secondary)
      }

      if (this.unavailableIds.includes(star.id) || !isVerifiedStarSource(star)) {
        Text(tr('starred_original_unavailable'))
          .fontSize(Tokens.typeCaption)
          .fontColor(Colors.secondary)
      } else if (this.failedCheckIds.includes(star.id)) {
        Text(tr('starred_original_check_failed'))
          .fontSize(Tokens.typeCaption)
          .fontColor(Colors.secondary)
      }

      Flex({ wrap: FlexWrap.Wrap, alignItems: ItemAlign.Center }) {
        if (!this.unavailableIds.includes(star.id) && isVerifiedStarSource(star)) {
          Button({ type: ButtonType.Capsule }) {
            Row({ space: Tokens.spaceXs }) {
              if (this.checkingId === star.id) {
                LoadingProgress().width(14).height(14).color(Colors.link)
              } else {
                SymbolGlyph($r('sys.symbol.arrow_up_right'))
                  .fontSize('12vp')
                  .fontColor([Colors.link])
              }
              Text(tr('starred_open_original'))
                .fontSize(Tokens.typeCaption)
                .fontWeight(FontWeight.Medium)
                .fontColor(Colors.link)
                .maxLines(1)
            }
          }
          .height(Tokens.controlCompact)
          .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
          .responseRegion({ x: 0, y: -4, width: '100%', height: Tokens.controlBar })
          .backgroundColor(Colors.needsWash)
          .margin({ right: Tokens.spaceS, top: Tokens.spaceXs })
          .enabled(this.checkingId !== star.id)
          .onClick(() => { void this.openOriginal(star); })
          .id('starred.open.' + star.id)
        }

        Button({ type: ButtonType.Capsule }) {
          Row({ space: Tokens.spaceXs }) {
            SymbolGlyph(IconActions.symbol('copy'))
              .fontSize('12vp')
              .fontColor([Colors.ink])
            Text(tr('copy'))
              .fontSize(Tokens.typeCaption)
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.ink)
              .maxLines(1)
          }
        }
        .height(Tokens.controlCompact)
        .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
        .responseRegion({ x: 0, y: -4, width: '100%', height: Tokens.controlBar })
        .backgroundColor(Colors.control)
        .margin({ right: Tokens.spaceS, top: Tokens.spaceXs })
        .onClick(() => this.copyStar(star))
        .id('starred.copy.' + star.id)

        if (this.store.canWrite) {
          Button({ type: ButtonType.Capsule }) {
            Row({ space: Tokens.spaceXs }) {
              SymbolGlyph($r('sys.symbol.star_slash'))
                .fontSize('12vp')
                .fontColor([Colors.danger])
              Text(tr('unstar_message'))
                .fontSize(Tokens.typeCaption)
                .fontWeight(FontWeight.Medium)
                .fontColor(Colors.danger)
                .maxLines(1)
            }
          }
          .height(Tokens.controlCompact)
          .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
          .responseRegion({ x: 0, y: -4, width: '100%', height: Tokens.controlBar })
          .backgroundColor(Colors.dangerWash)
          .margin({ top: Tokens.spaceXs })
          .enabled(!this.store.isPending(this.model, star))
          .onClick(() => { void this.store.remove(this.model, star, star.id, true); })
          .id('starred.unstar.' + star.id)
        }
      }
      .width('100%')
    }
    .width('100%')
    .padding(Tokens.spaceL)
    .backgroundColor(Colors.sheetCard)
    .borderRadius(Tokens.radiusCard)
    .border({
      width: PhoneStyle.surfaceEdgeWidth(0.5),
      color: Colors.hairlineGlass,
      radius: Tokens.radiusCard
    })
    .alignItems(HorizontalAlign.Start)
    .id('starred.card.' + star.id)
  }

  build() {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: tr('starred_messages'),
        subtitle: this.store.snapshots.length > 0 ? String(this.store.snapshots.length) : '',
        closeIdentity: 'starred.close',
        showDivider: true,
        onClose: () => this.onClose()
      })

      if (this.store.state === 'loading' && this.store.snapshots.length === 0) {
        Column({ space: Tokens.spaceS }) {
          LoadingProgress().width(36).height(36).color(Colors.link)
          Text(tr('loading'))
            .fontSize(Tokens.typeSecondary)
            .fontColor(Colors.secondary)
        }
        .width('100%')
        .layoutWeight(1)
        .justifyContent(FlexAlign.Center)
        .alignItems(HorizontalAlign.Center)
      } else if (this.store.state === 'unsupported') {
        this.emptyState(
          $r('sys.symbol.star'),
          tr('starred_unavailable_title'),
          tr('starred_unavailable_message')
        )
      } else if (this.store.state === 'failed') {
        this.emptyState(
          $r('sys.symbol.exclamationmark_triangle'),
          tr('starred_load_failed'),
          '',
          true
        )
      } else if (this.store.snapshots.length === 0) {
        this.emptyState(
          $r('sys.symbol.star'),
          tr('starred_empty_title'),
          tr('starred_empty_message')
        )
      } else {
        Column({ space: Tokens.spaceM }) {
          this.searchBar()

          if (this.matches.length === 0) {
            this.emptyState(
              Symbols.search,
              tr('starred_search_empty'),
              ''
            )
          } else {
            List({ space: Tokens.spaceM }) {
              ForEach(this.matches, (star: StarredSnapshot) => {
                ListItem() {
                  this.starCard(star)
                }
              }, (star: StarredSnapshot) => star.id + ':' + star.text.length)
            }
            .width('100%')
            .layoutWeight(1)
            .scrollBar(BarState.Auto)
          }
        }
        .width('100%')
        .layoutWeight(1)
        .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceL })
      }
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.sheetBackground)
    .id('starred.sheet')
  }
}
