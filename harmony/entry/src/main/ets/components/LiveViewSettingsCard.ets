import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import {
  FleetItem,
  FleetLiveState,
  fleetItemProgressPercent,
  sampleFleetState
} from '../core/FleetLiveState';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { NativeLiveView, nativeLiveView } from '../platform/NativeLiveView';
import { Haptics } from '../platform/Haptics';
import { toastCenter } from '../models/ToastCenter';

const PREVIEW_MODES: string[] = ['needsYou', 'working', 'background', 'done'];

/**
 * Interactive Fleet Live View preview & settings section aligned with iOS
 * `IslandSettingsView` + `FleetIslandViews` (`docs/apple/live-activity.md`).
 */
@ComponentV2
export struct LiveViewSettingsCard {
  @Local private liveView: NativeLiveView = nativeLiveView;
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private previewMode: string = 'needsYou';
  @Local private pressedId: string = '';

  @Computed get previewState(): FleetLiveState {
    return sampleFleetState(this.previewMode, this.liveView.lockDetails);
  }

  private phaseCount(state: FleetLiveState): string {
    if (state.phase === 'needsYou') return String(state.needsYou);
    if (state.phase === 'working') return String(state.working);
    if (state.phase === 'done') return String(state.unread);
    return '0';
  }

  private phaseTint(phase: string): ResourceColor {
    if (phase === 'needsYou') return Colors.needs;
    if (phase === 'working') return Colors.working;
    if (phase === 'done') return Colors.unread;
    return Colors.link;
  }

  private headline(state: FleetLiveState): string {
    if (state.phase === 'needsYou') return tr('live_view_needs_count').replace('{count}', String(state.needsYou));
    if (state.phase === 'working') return tr('live_view_working_count').replace('{count}', String(state.working));
    if (state.phase === 'done') return tr('live_view_unread_count').replace('{count}', String(state.unread));
    return tr('live_view_all_caught_up');
  }

  private capsuleTrailing(state: FleetLiveState): string {
    const first = state.items[0];
    if (state.phase === 'working' && first?.background && fleetItemProgressPercent(first) >= 0) {
      return String(fleetItemProgressPercent(first)) + '%';
    }
    if (state.phase === 'needsYou') return tr('needs_you');
    if (state.phase === 'working') return tr('live_view_capsule_metric_working');
    if (state.phase === 'done') return tr('agent_group_unread');
    return tr('live_view_capsule_done');
  }

  private itemLine(item: FleetItem, details: boolean): string {
    if (!details) {
      if (item.kind === 'permission') return tr('live_view_anon_permission');
      if (item.kind === 'plan') return tr('live_view_anon_plan');
      if (item.kind === 'question') return tr('live_view_anon_question');
      if (item.kind === 'working') return tr('live_view_anon_working');
      return tr('live_view_anon_unread');
    }
    return item.detail || item.place || item.title;
  }

  @Builder previewCapsule() {
    // Authentic pure-black hardware Live View capsule mirroring iOS Dynamic Island compact presentation.
    Row({ space: 8 }) {
      Row({ space: 6 }) {
        Image($r('app.media.phone_persona_face')).width(16).height(16).objectFit(ImageFit.Contain).accessibilityLevel('no')
        Text(this.phaseCount(this.previewState))
          .fontSize(Tokens.interfaceFontVp(13, this.env.fontScale))
          .fontWeight(FontWeight.Bold)
          .fontFeature('tnum 1')
          .fontColor(this.phaseTint(this.previewState.phase))
          .maxLines(1)
      }
      // Simulated camera cutout gap
      Blank().width(24)
      Row({ space: 5 }) {
        Circle().width(6).height(6).fill(this.phaseTint(this.previewState.phase))
        Text(this.capsuleTrailing(this.previewState))
          .fontSize(Tokens.typeLabel)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
          .fontWeight(FontWeight.Medium)
          .fontColor(Color.White)
          .maxLines(1)
      }
    }
    .height(34)
    .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
    .backgroundColor(Color.Black)
    .borderRadius(17)
    .border({ width: this.env.dark ? 0.5 : 0, color: Colors.hairlineGlass, radius: 17 })
    .shadow(PhoneStyle.floatingElevation(this.env.dark))
    .animation(Motion.state(this.env.reduceMotion))
    .id('live_view.preview.capsule')
  }

  @Builder previewCard() {
    Column({ space: 10 }) {
      Row({ space: 8 }) {
        Stack() {
          Image($r('app.media.phone_persona_face')).width(20).height(20).objectFit(ImageFit.Contain)
        }
        .width(28)
        .height(28)
        .borderRadius(14)
        .backgroundColor(Colors.control)
        Column({ space: 1 }) {
          Text(this.headline(this.previewState))
            .fontSize(Tokens.interfaceFontVp(15, this.env.fontScale))
            .fontWeight(FontWeight.Bold)
            .fontColor(Colors.ink)
            .maxLines(1)
          Text(tr('persona'))
            .fontSize(Tokens.interfaceFontVp(12, this.env.fontScale))
            .fontColor(Colors.secondary)
            .maxLines(1)
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        Row({ space: 5 }) {
          if (this.previewState.needsYou > 0) this.countChip(String(this.previewState.needsYou), Colors.needs)
          if (this.previewState.working > 0) this.countChip(String(this.previewState.working), Colors.working)
          if (this.previewState.unread > 0) this.countChip(String(this.previewState.unread), Colors.unread)
        }
      }.width('100%')

      ForEach(this.previewState.items.slice(0, 2), (item: FleetItem) => {
        Column({ space: 4 }) {
          Row({ space: 6 }) {
            Circle().width(6).height(6).fill(item.kind === 'working' ? Colors.working : item.kind === 'unread' ? Colors.unread : Colors.needs)
            Text(this.liveView.lockDetails ? item.title : this.itemLine(item, false))
              .fontSize(Tokens.interfaceFontVp(14, this.env.fontScale))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.ink)
              .maxLines(1)
              .textOverflow({ overflow: TextOverflow.Ellipsis })
              .layoutWeight(1)
            if (this.liveView.lockDetails && item.background) {
              Text(tr('live_view_background_tag'))
                .fontSize(Tokens.interfaceFontVp(11, this.env.fontScale))
                .fontColor(Colors.working)
                .padding({ left: 6, right: 6, top: 2, bottom: 2 })
                .backgroundColor(Colors.control)
                .borderRadius(6)
            }
          }.width('100%')
          if (this.liveView.lockDetails) {
            Text(this.itemLine(item, true))
              .fontSize(Tokens.interfaceFontVp(13, this.env.fontScale))
              .fontColor(Colors.secondary)
              .maxLines(1)
              .textOverflow({ overflow: TextOverflow.Ellipsis })
              .width('100%')
              .padding({ left: 12 })
          }
          if (item.background && fleetItemProgressPercent(item) >= 0) {
            Row() {
              Row()
                .width(String(fleetItemProgressPercent(item)) + '%')
                .height(4)
                .backgroundColor(Colors.working)
                .borderRadius(2)
            }
            .width('100%')
            .height(4)
            .backgroundColor(Colors.divider)
            .borderRadius(2)
            .margin({ top: 2 })
          }
        }.width('100%').alignItems(HorizontalAlign.Start)
      }, (item: FleetItem) => this.previewMode + ':' + item.id + ':' + String(this.liveView.lockDetails) + ':' + item.detail)

      if (this.liveView.lockDetails && this.previewState.phase === 'needsYou') {
        Row({ space: 8 }) {
          Text(tr('live_view_preview_approve'))
            .fontSize(Tokens.interfaceFontVp(12.5, this.env.fontScale))
            .fontWeight(FontWeight.Medium)
            .fontColor(Color.White)
            .textAlign(TextAlign.Center)
            .layoutWeight(1)
            .padding({ top: 7, bottom: 7 })
            .backgroundColor(Colors.positive)
            .borderRadius(Tokens.capsule)
          Text(tr('live_view_preview_deny'))
            .fontSize(Tokens.interfaceFontVp(12.5, this.env.fontScale))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .textAlign(TextAlign.Center)
            .layoutWeight(1)
            .padding({ top: 7, bottom: 7 })
            .backgroundColor(Colors.control)
            .borderRadius(Tokens.capsule)
        }.width('100%').padding({ top: 2 })
      }
    }
    .width('100%')
    .padding(14)
    .backgroundColor(Colors.elevatedGlass)
    .backgroundBlurStyle(BlurStyle.COMPONENT_THICK)
    .borderRadius(Tokens.radiusCard)
    .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.floatingBorder(this.env.dark), radius: Tokens.radiusCard })
    .shadow(PhoneStyle.floatingElevation(this.env.dark))
    .animation(Motion.state(this.env.reduceMotion))
    .id('live_view.preview.card')
  }

  @Builder countChip(value: string, tint: ResourceColor) {
    Row({ space: 4 }) {
      Circle().width(6).height(6).fill(tint)
      Text(value)
        .fontSize(Tokens.interfaceFontVp(12, this.env.fontScale))
        .fontWeight(FontWeight.Bold)
        .fontFeature('tnum 1')
        .fontColor(Colors.ink)
    }
    .padding({ left: 7, right: 7, top: 3, bottom: 3 })
    .backgroundColor(Colors.control)
    .borderRadius(10)
  }

  private modeTitle(mode: string): string {
    if (mode === 'needsYou') return tr('needs_you');
    if (mode === 'working') return tr('working');
    if (mode === 'background') return tr('live_view_background_tag');
    return tr('agent_group_unread');
  }

  build() {
    Column({ space: Tokens.inset }) {
      Text(tr('live_view_section_title'))
        .fontSize(Tokens.typeSectionHeader)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSectionHeader))
        .fontWeight(600)
        .fontColor(Colors.secondary)
        .width('100%')
        .padding({ left: Tokens.spaceXs })

      // Hardware Live Capsule + Lock-Screen banner preview inside an iOS inset-grouped stage
      Column({ space: 14 }) {
        Row() {
          this.previewCapsule()
        }.width('100%').justifyContent(FlexAlign.Center)
        this.previewCard()
        Row({ space: 2 }) {
          ForEach(PREVIEW_MODES, (mode: string) => {
            Button({ type: ButtonType.Capsule }) {
              Text(this.modeTitle(mode))
                .fontSize(Tokens.interfaceFontVp(11.5, this.env.fontScale))
                .minFontSize('9vp')
                .maxFontSize(Tokens.interfaceFontVp(11.5, this.env.fontScale))
                .fontWeight(this.previewMode === mode ? FontWeight.Bold : FontWeight.Medium)
                .fontColor(this.previewMode === mode ? Colors.ink : Colors.secondary)
                .maxLines(1)
                .textOverflow({ overflow: TextOverflow.Ellipsis })
            }
            .layoutWeight(1)
            .height(30)
            .padding({ left: 3, right: 3 })
            .backgroundColor(this.previewMode === mode ? Colors.surface : Color.Transparent)
            .scale({ x: this.pressedId === mode ? MotionTokens.pressScale : 1, y: this.pressedId === mode ? MotionTokens.pressScale : 1 })
            .animation(Motion.state(this.env.reduceMotion))
            .onTouch((event: TouchEvent) => {
              if (event.type === TouchType.Down) this.pressedId = mode;
              else if (event.type === TouchType.Up || event.type === TouchType.Cancel) this.pressedId = '';
            })
            .onClick(() => {
              Haptics.selection();
              Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
                this.previewMode = mode;
              });
            })
            .id('live_view.preview.mode.' + mode)
          }, (mode: string) => mode)
        }
        .width('100%')
        .padding(3)
        .backgroundColor(Colors.control)
        .borderRadius(Tokens.capsule)
      }
      .width('100%')
      .padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: 14, bottom: Tokens.spaceM })
      .backgroundColor(Colors.surface)
      .borderRadius(Tokens.radiusSheetCard)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusSheetCard })
      .shadow(PhoneStyle.cardElevation(this.env.dark))

      // Live View toggles
      Column() {
        Row({ space: Tokens.inset }) {
          Column({ space: 2 }) {
            Text(tr('live_view_enable'))
              .fontSize(Tokens.body)
              .maxFontScale(Tokens.interfaceFontMaxScale)
              .fontColor(Colors.ink)
            Text(tr('live_view_enable_detail'))
              .fontSize(Tokens.interfaceFontVp(13, this.env.fontScale))
              .fontColor(Colors.secondary)
          }.layoutWeight(1).alignItems(HorizontalAlign.Start)
          Toggle({ type: ToggleType.Switch, isOn: this.liveView.enabled })
            .selectedColor(Colors.positive)
            .enabled(!this.liveView.busy)
            .accessibilityText(tr('live_view_enable'))
            .onChange((value: boolean) => {
              Haptics.tap();
              this.liveView.setEnabled(value).catch(() => {});
            })
            .id('live_view.toggle.enabled')
        }.width('100%').constraintSize({ minHeight: 54 }).padding({ left: Tokens.page, right: Tokens.page, top: 8, bottom: 8 })

        Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })

        Row({ space: Tokens.inset }) {
          Column({ space: 2 }) {
            Text(tr('live_view_lock_details'))
              .fontSize(Tokens.body)
              .maxFontScale(Tokens.interfaceFontMaxScale)
              .fontColor(Colors.ink)
            Text(tr('live_view_lock_details_detail'))
              .fontSize(Tokens.interfaceFontVp(13, this.env.fontScale))
              .fontColor(Colors.secondary)
          }.layoutWeight(1).alignItems(HorizontalAlign.Start)
          Toggle({ type: ToggleType.Switch, isOn: this.liveView.lockDetails })
            .selectedColor(Colors.positive)
            .enabled(!this.liveView.busy && this.liveView.enabled)
            .accessibilityText(tr('live_view_lock_details'))
            .onChange((value: boolean) => {
              Haptics.tap();
              this.liveView.setLockDetails(value).catch(() => {});
            })
            .id('live_view.toggle.details')
        }.width('100%').constraintSize({ minHeight: 54 }).padding({ left: Tokens.page, right: Tokens.page, top: 8, bottom: 8 })

        Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })

        Row({ space: 10 }) {
          Button(tr('live_view_test_publish'), { type: ButtonType.Capsule })
            .layoutWeight(1)
            .height(42)
            .fontSize(Tokens.interfaceFontVp(14, this.env.fontScale))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.onAction)
            .backgroundColor(Colors.action)
            .scale({ x: this.pressedId === 'publish' ? MotionTokens.pressScale : 1, y: this.pressedId === 'publish' ? MotionTokens.pressScale : 1 })
            .animation(Motion.release(this.env.reduceMotion))
            .onTouch((event: TouchEvent) => {
              if (event.type === TouchType.Down) this.pressedId = 'publish';
              else if (event.type === TouchType.Up || event.type === TouchType.Cancel) this.pressedId = '';
            })
            .enabled(this.liveView.enabled && !this.liveView.busy)
            .onClick(() => {
              Haptics.tap();
              this.liveView.publishSample(this.previewMode).then((ok: boolean) => {
                if (ok) { Haptics.success(); toastCenter.success(tr('live_view_published_toast')); }
                else Haptics.error();
              });
            })
            .id('live_view.test.publish')
          if (this.liveView.active) {
            Button(tr('live_view_test_stop'), { type: ButtonType.Capsule })
              .height(42)
              .padding({ left: 16, right: 16 })
              .fontSize(Tokens.interfaceFontVp(14, this.env.fontScale))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.link)
              .backgroundColor(Colors.control)
              .scale({ x: this.pressedId === 'stop' ? MotionTokens.pressScale : 1, y: this.pressedId === 'stop' ? MotionTokens.pressScale : 1 })
              .animation(Motion.release(this.env.reduceMotion))
              .onTouch((event: TouchEvent) => {
                if (event.type === TouchType.Down) this.pressedId = 'stop';
                else if (event.type === TouchType.Up || event.type === TouchType.Cancel) this.pressedId = '';
              })
              .onClick(() => {
                Haptics.tap();
                this.liveView.stopNow().catch(() => {});
              })
              .id('live_view.test.stop')
          }
        }.width('100%').padding({ left: Tokens.page, right: Tokens.page, top: 10, bottom: 12 })
      }
      .width('100%')
      .backgroundColor(Colors.surface)
      .borderRadius(PhoneStyle.cardRadius)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: PhoneStyle.cardRadius })
      .shadow(PhoneStyle.cardElevation(this.env.dark))

      if (this.liveView.statusCode) {
        Row({ space: 6 }) {
          SymbolGlyph($r('sys.symbol.info_circle')).fontSize(Tokens.interfaceFontVp(14, this.env.fontScale)).fontColor([Colors.secondary])
          Text(tr(this.liveView.statusCode))
            .fontSize(Tokens.interfaceFontVp(13, this.env.fontScale))
            .fontColor(Colors.secondary)
            .layoutWeight(1)
        }.width('100%').padding({ left: 4, right: 4 }).id('live_view.status')
      }
    }.width('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.page })
  }
}
