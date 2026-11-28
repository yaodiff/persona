import { SymbolGlyphModifier } from '@kit.ArkUI';
import { pasteboard } from '@kit.BasicServicesKit';
import { canMutateQueueIndex, ComposerQueueState, QueuedComposerItem } from '../core/ComposerQueue';
import { Colors } from '../design/Colors';
import { IconActions } from '../design/IconActions';
import { Motion } from '../design/Motion';
import { PhoneStyle } from '../design/PhoneStyle';
import { tr } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { toastCenter } from '../models/ToastCenter';
import { Haptics } from '../platform/Haptics';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

/**
 * Unified composer-top queue dock strip matching iOS `ComposerTopSlot.swift` (#16247).
 * Docks inside the top edge of the composer surface above a hairline divider.
 */
@ComponentV2
export struct ComposerTopStrip {
  @Require @Param queue: ComposerQueueState;
  @Param busy: boolean = false;
  @Event onSendNow: () => void = () => {};
  @Event onEditQueued: (index: number) => void = () => {};
  @Event onDeleteQueued: (index: number) => void = () => {};
  @Event onMoveQueued: (index: number, delta: -1 | 1) => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private expanded: boolean = false;
  @Local private copiedItemId: string = '';
  private copyResetTimer: number = -1;

  aboutToDisappear(): void {
    if (this.copyResetTimer !== -1) {
      clearTimeout(this.copyResetTimer);
      this.copyResetTimer = -1;
    }
  }

  private toggleExpanded(): void {
    Haptics.tap();
    Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
      this.expanded = !this.expanded;
    });
  }

  private copyItem(item: QueuedComposerItem): void {
    try {
      pasteboard.getSystemPasteboard().setDataSync(
        pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, item.text)
      );
      Haptics.selection();
      this.copiedItemId = item.id;
      if (this.copyResetTimer !== -1) clearTimeout(this.copyResetTimer);
      this.copyResetTimer = setTimeout(() => {
        if (this.copiedItemId === item.id) this.copiedItemId = '';
        this.copyResetTimer = -1;
      }, 1500);
      toastCenter.show(tr('copied'));
    } catch (_) {
      Haptics.error();
      toastCenter.error(tr('failed'));
    }
  }

  private countBadgeLabel(): string {
    return tr('composer_queued_count').replace('{count}', String(this.queue.items.length));
  }

  @Builder ownItemMenu(item: QueuedComposerItem, index: number) {
    Menu() {
      MenuItem({
        content: tr('composer_queue_edit'),
        symbolStartIcon: new SymbolGlyphModifier($r('sys.symbol.square_and_pencil'))
      })
        .enabled(!this.busy)
        .onClick(() => {
          Haptics.tap();
          this.onEditQueued(index);
        })
        .id('composer.queue.edit.' + item.id)

      if (this.queue.steersQueue && this.queue.ownCount > 1) {
        MenuItem({
          content: tr('composer_queue_move_up'),
          symbolStartIcon: new SymbolGlyphModifier(Symbols.up)
        })
          .enabled(index > 0 && !this.busy)
          .onClick(() => {
            Haptics.tap();
            this.onMoveQueued(index, -1);
          })
          .id('composer.queue.move_up.' + item.id)

        MenuItem({
          content: tr('composer_queue_move_down'),
          symbolStartIcon: new SymbolGlyphModifier(Symbols.down)
        })
          .enabled(index < this.queue.ownCount - 1 && !this.busy)
          .onClick(() => {
            Haptics.tap();
            this.onMoveQueued(index, 1);
          })
          .id('composer.queue.move_down.' + item.id)
      }

      MenuItem({
        content: tr('copy'),
        symbolStartIcon: new SymbolGlyphModifier(IconActions.symbol('copy'))
      })
        .onClick(() => this.copyItem(item))
        .id('composer.queue.copy.' + item.id)

      MenuItem({
        content: tr('delete'),
        symbolStartIcon: new SymbolGlyphModifier(IconActions.symbol('delete'))
      })
        .contentFontColor(Colors.danger)
        .enabled(!this.busy)
        .onClick(() => {
          Haptics.tap();
          this.onDeleteQueued(index);
        })
        .id('composer.queue.delete.' + item.id)
    }
  }

  build() {
    Column({ space: 0 }) {
      Row({ space: Tokens.spaceS }) {
        Button({ type: ButtonType.Normal }) {
          Row({ space: Tokens.spaceSm }) {
            SymbolGlyph($r('sys.symbol.clock'))
              .fontSize('12.5vp')
              .fontColor([Colors.secondary])
            Text(this.countBadgeLabel())
              .fontSize(Tokens.typeLabel)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.needs)
              .maxLines(1)
              .padding({ left: 7, right: 7, top: 2, bottom: 2 })
              .backgroundColor(Colors.needsWash)
              .borderRadius(Tokens.radiusCapsule)
            if (!this.expanded && this.queue.preview) {
              Text(this.queue.preview)
                .fontSize(Tokens.typeCaption)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                .fontColor(Colors.secondary)
                .maxLines(1)
                .textOverflow({ overflow: TextOverflow.Ellipsis })
                .layoutWeight(1)
            } else {
              Blank().layoutWeight(1)
            }
            SymbolGlyph(Symbols.down)
              .fontSize('11vp')
              .fontColor([Colors.tertiary])
              .rotate({ angle: this.expanded ? 180 : 0 })
              .animation(Motion.state(this.env.reduceMotion))
          }
          .width('100%')
          .alignItems(VerticalAlign.Center)
        }
        .layoutWeight(1)
        .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs, top: Tokens.spaceXs, bottom: Tokens.spaceXs })
        .backgroundColor(Color.Transparent)
        .accessibilityText(this.countBadgeLabel())
        .onClick(() => this.toggleExpanded())
        .id('composer.queue.toggle')

        if (this.queue.steersQueue && this.queue.ownCount > 0) {
          Button({ type: ButtonType.Capsule }) {
            Row({ space: Tokens.spaceXs }) {
              if (this.busy) {
                LoadingProgress()
                  .width(12)
                  .height(12)
                  .color(Colors.link)
              } else {
                SymbolGlyph($r('sys.symbol.bolt'))
                  .fontSize('11.5vp')
                  .fontColor([Colors.link])
              }
              Text(tr('composer_queue_send_now'))
                .fontSize(Tokens.typeLabel)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                .fontWeight(FontWeight.Medium)
                .fontColor(Colors.link)
                .maxLines(1)
            }
          }
          .height(26)
          .padding({ left: 10, right: 10 })
          .responseRegion({ x: 0, y: -9, width: '100%', height: Tokens.controlBar })
          .backgroundColor(Colors.needsWash)
          .border({
            width: PhoneStyle.surfaceEdgeWidth(0.5),
            color: Colors.hairlineGlass,
            radius: Tokens.radiusCapsule
          })
          .enabled(!this.busy)
          .onClick(() => {
            Haptics.tap();
            this.onSendNow();
          })
          .id('composer.queue.send_now')
        }
      }
      .width('100%')
      .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs, top: Tokens.spaceXxs, bottom: Tokens.spaceXs })
      .alignItems(VerticalAlign.Center)
      .id('composer.queue.strip')

      if (this.expanded) {
        Scroll() {
          Column({ space: Tokens.spaceSm }) {
            ForEach(this.queue.items, (item: QueuedComposerItem, index: number) => {
              Row({ space: Tokens.spaceS }) {
                Text(String(index + 1))
                  .fontSize(Tokens.typeLabel)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                  .fontWeight(FontWeight.Bold)
                  .fontColor(Colors.needs)
                  .textAlign(TextAlign.Center)
                  .width(20)
                  .height(20)
                  .borderRadius(Tokens.radiusControl)
                  .backgroundColor(Colors.needsWash)

                Column({ space: Tokens.spaceXxs }) {
                  Text(item.text)
                    .fontSize(Tokens.typeCaption)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                    .fontColor(Colors.ink)
                    .lineHeight(Tokens.lineHeightCaption)
                    .maxLines(3)
                    .textOverflow({ overflow: TextOverflow.Ellipsis })
                    .width('100%')
                  if (item.relayed || item.sender) {
                    Row({ space: Tokens.spaceSm }) {
                      if (item.relayed) {
                        Text(tr('composer_queue_relayed'))
                          .fontSize(Tokens.typeLabel)
                          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                          .fontWeight(FontWeight.Medium)
                          .fontColor(Colors.secondary)
                          .padding({ left: Tokens.spaceSm, right: Tokens.spaceSm, top: 1, bottom: 1 })
                          .backgroundColor(Colors.control)
                          .borderRadius(Tokens.radiusBadge)
                      }
                      if (item.sender) {
                        Text(item.sender)
                          .fontSize(Tokens.typeLabel)
                          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                          .fontColor(Colors.secondary)
                          .maxLines(1)
                          .textOverflow({ overflow: TextOverflow.Ellipsis })
                      }
                    }
                  }
                  if (item.relayed || index >= this.queue.ownCount) {
                    Text(tr(item.noWake ? 'composer_queue_arrives_next' : 'composer_queue_arrives_turn_end'))
                      .fontSize(Tokens.typeLabel)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                      .fontColor(Colors.secondary)
                      .maxLines(1)
                      .textOverflow({ overflow: TextOverflow.Ellipsis })
                  }
                }
                .layoutWeight(1)
                .alignItems(HorizontalAlign.Start)

                if (canMutateQueueIndex(this.queue.ownCount, index) && !item.relayed) {
                  Button({ type: ButtonType.Circle }) {
                    Row({ space: 3 }) {
                      ForEach([0, 1, 2], (_dot: number) => {
                        Circle().width(3.5).height(3.5).fill(Colors.secondary)
                      }, (dot: number) => String(dot))
                    }
                  }
                  .width(28)
                  .height(28)
                  .padding(0)
                  .backgroundColor(Color.Transparent)
                  .responseRegion({ x: -8, y: -8, width: Tokens.controlBar, height: Tokens.controlBar })
                  .accessibilityText(tr('more'))
                  .bindTips(tr('more'))
                  .enabled(!this.busy)
                  .bindMenu(this.ownItemMenu(item, index))
                  .id('composer.queue.more.' + item.id)
                } else {
                  Button({ type: ButtonType.Circle }) {
                    SymbolGlyph(this.copiedItemId === item.id ? Symbols.check : IconActions.symbol('copy'))
                      .fontSize('13vp')
                      .fontColor([this.copiedItemId === item.id ? Colors.positive : Colors.secondary])
                  }
                  .width(28)
                  .height(28)
                  .padding(0)
                  .backgroundColor(Color.Transparent)
                  .responseRegion({ x: -8, y: -8, width: Tokens.controlBar, height: Tokens.controlBar })
                  .accessibilityText(tr('copy'))
                  .bindTips(tr('copy'))
                  .onClick(() => this.copyItem(item))
                  .id('composer.queue.copy.' + item.id)
                }
              }
              .width('100%')
              .padding({ left: 10, right: Tokens.spaceS, top: 7, bottom: 7 })
              .alignItems(VerticalAlign.Top)
              .backgroundColor(Colors.pressOverlay)
              .borderRadius(Tokens.radiusCard)
              .border({
                width: PhoneStyle.surfaceEdgeWidth(0.5),
                color: Colors.hairlineGlass,
                radius: Tokens.radiusCard
              })
            }, (item: QueuedComposerItem, index: number) => item.id + ':' + String(index) + ':' + item.text)
          }
          .width('100%')
          .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs, bottom: Tokens.spaceSm })
        }
        .scrollable(ScrollDirection.Vertical)
        .scrollBar(BarState.Auto)
        .constraintSize({ maxHeight: 168 })
        .width('100%')
        .transition(Motion.slideFade(this.env.reduceMotion, -4))
        .animation(Motion.state(this.env.reduceMotion))
        .id('composer.queue.list')
      }

      Divider()
        .strokeWidth(Tokens.divider)
        .color(Colors.divider)
        .width('100%')
        .margin({ top: Tokens.spaceXxs, bottom: Tokens.spaceXxs })
    }
    .width('100%')
    .alignItems(HorizontalAlign.Start)
    .animation(Motion.state(this.env.reduceMotion))
  }
}
