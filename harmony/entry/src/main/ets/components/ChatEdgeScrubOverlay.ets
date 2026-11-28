import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { PhoneStyle } from '../design/PhoneStyle';
import { Tokens } from '../design/Tokens';
import {
  EDGE_SCRUB_BAND_VP,
  EDGE_SCRUB_SLOP_VP,
  EdgeScrubColumn,
  EdgeScrubMark,
  edgeScrubIndexAt,
  edgeScrubYOf,
  fitEdgeScrubColumn
} from '../core/EdgeScrub';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';

/**
 * Right-edge conversation scrubber overlay matching iOS `ChatEdgeScrub.swift` (#1666).
 * Long-press along the right edge band to scrub across user turns with per-tick haptics;
 * release to jump directly to the selected turn.
 */
@ComponentV2
export struct ChatEdgeScrubOverlay {
  @Require @Param marks: EdgeScrubMark[];
  @Param viewportHeight: number = 0;
  @Event onJump: (mark: EdgeScrubMark) => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private active: boolean = false;
  @Local private pickedIndex: number = -1;
  @Local private column: EdgeScrubColumn | undefined = undefined;
  private holdTimer: number = -1;
  private startY: number = 0;
  private startX: number = 0;
  private startIndex: number = -1;
  private movedEnough: boolean = false;

  aboutToDisappear(): void {
    this.cancelHold();
  }

  private cancelHold(): void {
    if (this.holdTimer !== -1) {
      clearTimeout(this.holdTimer);
      this.holdTimer = -1;
    }
  }

  private beginScrub(y: number): void {
    const col = fitEdgeScrubColumn(this.marks.length, this.viewportHeight, 24, 40);
    if (!col) return;
    const idx = edgeScrubIndexAt(col, y);
    this.column = col;
    this.pickedIndex = idx;
    this.startIndex = idx;
    this.movedEnough = false;
    Haptics.tap();
    Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
      this.active = true;
    });
  }

  private updateScrub(y: number): void {
    if (!this.active || !this.column) return;
    const idx = edgeScrubIndexAt(this.column, y);
    if (idx !== this.startIndex && Math.abs(y - this.startY) > EDGE_SCRUB_SLOP_VP) {
      this.movedEnough = true;
    }
    if (idx !== this.pickedIndex) {
      this.pickedIndex = idx;
      Haptics.selection();
    }
  }

  private endScrub(commit: boolean): void {
    this.cancelHold();
    const shouldJump = commit && this.active && this.movedEnough && this.pickedIndex >= 0 && this.pickedIndex < this.marks.length;
    const chosen = shouldJump ? this.marks[this.pickedIndex] : undefined;
    Motion.run(this.getUIContext(), Motion.conceal(this.env.reduceMotion), () => {
      this.active = false;
    });
    this.column = undefined;
    this.pickedIndex = -1;
    this.movedEnough = false;
    if (chosen) this.onJump(chosen);
  }

  private activeIndices(): number[] {
    if (!this.column) return [];
    const list: number[] = [];
    for (let i = 0; i < this.column.count; i++) list.push(this.column.first + i);
    return list;
  }

  build() {
    if (this.marks.length >= 2 && this.viewportHeight > 120) {
      Stack({ alignContent: Alignment.TopEnd }) {
        // Narrow invisible right-edge hold strip (never steals normal scrolls or horizontal swipes)
        Row()
          .width(EDGE_SCRUB_BAND_VP)
          .height('100%')
          .backgroundColor(Color.Transparent)
          .onTouch((event: TouchEvent) => {
            const touch = event.touches[0];
            if (!touch) return;
            if (event.type === TouchType.Down) {
              this.cancelHold();
              this.startX = touch.x;
              this.startY = touch.y;
              const y = touch.y;
              this.holdTimer = setTimeout(() => {
                this.holdTimer = -1;
                this.beginScrub(y);
              }, 260);
            } else if (event.type === TouchType.Move) {
              if (!this.active) {
                if (Math.hypot(touch.x - this.startX, touch.y - this.startY) > EDGE_SCRUB_SLOP_VP) {
                  this.cancelHold();
                }
              } else {
                this.updateScrub(touch.y);
              }
            } else if (event.type === TouchType.Up) {
              this.endScrub(true);
            } else if (event.type === TouchType.Cancel) {
              this.endScrub(false);
            }
          })
          .id('chat.edgeScrub.band')

        if (this.active && this.column) {
          Stack({ alignContent: Alignment.TopEnd }) {
            ForEach(this.activeIndices(), (idx: number) => {
              Row()
                .width(idx === this.pickedIndex ? 18 : 9)
                .height(3)
                .borderRadius(2)
                .backgroundColor(idx === this.pickedIndex ? Colors.link : Colors.tertiary)
                .position({ x: '100%', y: edgeScrubYOf(this.column!, idx) })
                .translate({ x: idx === this.pickedIndex ? -24 : -15 })
                .animation(Motion.state(this.env.reduceMotion))
            }, (idx: number) => String(idx))

            if (this.pickedIndex >= 0 && this.pickedIndex < this.marks.length) {
              Text(this.marks[this.pickedIndex].preview)
                .fontSize(Tokens.interfaceFontVp(13, this.env.fontScale))
                .fontWeight(FontWeight.Medium)
                .fontColor(Colors.ink)
                .maxLines(2)
                .textOverflow({ overflow: TextOverflow.Ellipsis })
                .constraintSize({ maxWidth: 230 })
                .padding({ left: 12, right: 12, top: 8, bottom: 8 })
                .backgroundColor(Colors.surface)
                .borderRadius(14)
                .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.divider, radius: 14 })
                .shadow(PhoneStyle.cardElevation(this.env.dark))
                .position({
                  x: '100%',
                  y: Math.max(12, Math.min(this.viewportHeight - 64, edgeScrubYOf(this.column!, this.pickedIndex) - 18))
                })
                .translate({ x: -264 })
                .id('chat.edgeScrub.preview')
            }
          }
          .width('100%')
          .height('100%')
          .hitTestBehavior(HitTestMode.None)
          .transition(Motion.fadeScale(this.env.reduceMotion, 0.96))
          .id('chat.edgeScrub')
        }
      }
      .width('100%')
      .height('100%')
      .hitTestBehavior(HitTestMode.Transparent)
    }
  }
}
