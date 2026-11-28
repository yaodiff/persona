import { pasteboard } from '@kit.BasicServicesKit';
import { Colors } from '../design/Colors';
import { Motion } from '../design/Motion';
import { PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import {
  TURN_CHANGES_INITIAL_VISIBLE,
  ToolIconRole,
  TurnChangeSet,
  TurnChangedFile,
  WorkStepView
} from '../core/WorkPresentation';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { Haptics } from '../platform/Haptics';
import { toastCenter } from '../models/ToastCenter';
import { WorkingArcSpinner } from './StatusMotionMarks';

function iconForToolRole(role: ToolIconRole): Resource {
  if (role === 'read') return Symbols.document;
  if (role === 'edit' || role === 'write') return Symbols.edit;
  if (role === 'search' || role === 'find') return $r('sys.symbol.magnifyingglass');
  if (role === 'subagent') return Symbols.agents;
  if (role === 'todo') return Symbols.check;
  if (role === 'git') return $r('sys.symbol.arrow_triangle_divide');
  if (role === 'plan') return $r('sys.symbol.list_bullet');
  if (role === 'question') return $r('sys.symbol.questionmark_circle');
  if (role === 'thought') return $r('sys.symbol.info_circle');
  if (role === 'fold') return Symbols.folder;
  if (role === 'terminal' || role === 'code' || role === 'skill') return $r('sys.symbol.bolt');
  return Symbols.gear;
}

/**
 * Structured tool-step timeline matching iOS `ToolViews.swift`.
 * Renders human verbs, semantic icons, live running spinners, diff badges,
 * and tap-to-inspect input/output drawers.
 */
@ComponentV2
export struct WorkStepTimeline {
  @Require @Param steps: WorkStepView[];
  @Param fallbackText: string = '';
  @Event onOpenFile: (path: string) => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private openedStepIds: string[] = [];

  private isOpen(id: string): boolean {
    return this.openedStepIds.includes(id);
  }

  private toggleStep(id: string): void {
    Haptics.tap();
    Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
      this.openedStepIds = this.isOpen(id)
        ? this.openedStepIds.filter((entry: string) => entry !== id)
        : this.openedStepIds.concat([id]);
    });
  }

  private copySnippet(text: string): void {
    try {
      pasteboard.getSystemPasteboard().setDataSync(pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, text));
      Haptics.selection();
      toastCenter.show(tr('copied'));
    } catch (_) {
      Haptics.error();
      toastCenter.error(tr('failed'));
    }
  }

  @Builder stepStatusMark(step: WorkStepView) {
    if (step.running) {
      WorkingArcSpinner({ diameter: 14, tint: Colors.working, surface: Color.Transparent, showCoreDot: false })
    } else if (step.failed) {
      SymbolGlyph($r('sys.symbol.exclamationmark_triangle')).fontSize('14vp').fontColor([Colors.danger])
    } else {
      SymbolGlyph(iconForToolRole(step.iconRole)).fontSize('14vp').fontColor([step.kind === 'thought' ? Colors.secondary : Colors.link])
    }
  }

  @Builder diffCounts(add: number, del: number) {
    if (add > 0 || del > 0) {
      Row({ space: 4 }) {
        if (add > 0) {
          Text('+' + String(add))
            .fontSize(Tokens.typeLabel)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
            .fontWeight(FontWeight.Medium)
            .fontFamily('monospace')
            .fontColor(Colors.diffAdded)
            .padding({ left: 5, right: 5, top: 2, bottom: 2 })
            .backgroundColor(Colors.codeAddedFill)
            .borderRadius(Tokens.radiusBadge)
        }
        if (del > 0) {
          Text('−' + String(del))
            .fontSize(Tokens.typeLabel)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
            .fontWeight(FontWeight.Medium)
            .fontFamily('monospace')
            .fontColor(Colors.diffRemoved)
            .padding({ left: 5, right: 5, top: 2, bottom: 2 })
            .backgroundColor(Colors.codeRemovedFill)
            .borderRadius(Tokens.radiusBadge)
        }
      }
    }
  }

  build() {
    Column({ space: 6 }) {
      if (this.steps.length === 0) {
        Text(this.fallbackText)
          .fontFamily('monospace')
          .fontSize(13)
          .fontColor(Colors.secondary)
          .lineHeight(20)
          .copyOption(CopyOptions.LocalDevice)
          .width('100%')
      } else {
        Row({ space: Tokens.spaceSm }) {
          SymbolGlyph(Symbols.spaces)
            .fontSize('12.5vp')
            .fontColor([Colors.secondary])
          Text(tr('work_summary'))
            .fontSize(Tokens.typeLabel)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.secondary)
            .maxLines(1)
          Blank().layoutWeight(1)
          Text(String(this.steps.length))
            .fontSize(Tokens.typeLabel)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
            .fontWeight(FontWeight.Bold)
            .fontFamily('monospace')
            .fontColor(Colors.link)
            .padding({ left: 7, right: 7, top: 1.5, bottom: 1.5 })
            .backgroundColor(Colors.accentWell)
            .borderRadius(Tokens.radiusCapsule)
            .id('chat.work.timeline.count')
        }
        .width('100%')
        .padding({ left: Tokens.spaceXs, right: Tokens.spaceXs, top: Tokens.spaceXxs, bottom: Tokens.spaceXxs })
        .alignItems(VerticalAlign.Center)

        ForEach(this.steps, (step: WorkStepView, index: number) => {
          Column({ space: Tokens.spaceSm }) {
            Button({ type: ButtonType.Normal }) {
              Row({ space: Tokens.spaceS }) {
                Stack() { this.stepStatusMark(step) }.width(18).height(18)
                Column({ space: 1 }) {
                  Row({ space: Tokens.spaceSm }) {
                    Text(step.verb)
                      .fontSize(Tokens.typeCaption)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                      .fontWeight(FontWeight.Medium)
                      .fontColor(step.failed ? Colors.danger : Colors.ink)
                      .maxLines(1)
                    if (step.subject) {
                      Text(step.subject)
                        .fontSize(Tokens.typeCaption)
                        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                        .fontColor(Colors.secondary)
                        .maxLines(1)
                        .textOverflow({ overflow: TextOverflow.Ellipsis })
                        .ellipsisMode(step.filePath ? EllipsisMode.CENTER : EllipsisMode.END)
                        .layoutWeight(1)
                    }
                  }.width('100%')
                }.layoutWeight(1).alignItems(HorizontalAlign.Start)
                this.diffCounts(step.additions, step.deletions)
                if (step.inputPreview || step.outputPreview) {
                  SymbolGlyph(Symbols.down)
                    .fontSize('11vp')
                    .fontColor([Colors.tertiary])
                    .rotate({ angle: this.isOpen(step.id) ? 0 : -90 })
                    .animation(Motion.state(this.env.reduceMotion))
                }
              }.width('100%').padding({ left: 10, right: 10, top: 7, bottom: 7 }).constraintSize({ minHeight: Tokens.controlCompact })
            }
            .width('100%')
            .padding(0)
            .backgroundColor(Colors.surface)
            .borderRadius(Tokens.radiusControl)
            .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: step.failed ? Colors.danger : PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusControl })
            .onClick(() => {
              if (step.inputPreview || step.outputPreview) this.toggleStep(step.id);
              else if (step.filePath) { Haptics.tap(); this.onOpenFile(step.filePath); }
            })
            .id('chat.work.step.' + step.id)

            if (this.isOpen(step.id) && (step.inputPreview || step.outputPreview)) {
              Column({ space: Tokens.spaceSm }) {
                if (step.inputPreview) {
                  Text(step.inputPreview)
                    .fontFamily('monospace')
                    .fontSize(Tokens.typeLabel)
                    .lineHeight(Tokens.lineHeightCaption)
                    .fontColor(Colors.ink)
                    .copyOption(CopyOptions.LocalDevice)
                    .width('100%')
                }
                if (step.outputPreview) {
                  if (step.inputPreview) Divider().strokeWidth(Tokens.divider).color(Colors.divider)
                  Text(step.outputPreview)
                    .fontFamily('monospace')
                    .fontSize(Tokens.typeLabel)
                    .lineHeight(Tokens.lineHeightCaption)
                    .fontColor(step.failed ? Colors.danger : Colors.secondary)
                    .copyOption(CopyOptions.LocalDevice)
                    .width('100%')
                }
                Row({ space: Tokens.spaceS }) {
                  if (step.filePath) {
                    Button(tr('open'), { type: ButtonType.Capsule })
                      .height(28)
                      .padding({ left: 10, right: 10 })
                      .fontSize(Tokens.typeLabel)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                      .fontColor(Colors.link)
                      .backgroundColor(Colors.selection)
                      .onClick(() => { Haptics.tap(); this.onOpenFile(step.filePath); })
                  }
                  Blank().layoutWeight(1)
                  Button(tr('copy'), { type: ButtonType.Capsule })
                    .height(28)
                    .padding({ left: 10, right: 10 })
                    .fontSize(Tokens.typeLabel)
                    .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                    .fontColor(Colors.secondary)
                    .backgroundColor(Colors.control)
                    .onClick(() => this.copySnippet([step.inputPreview, step.outputPreview].filter((v: string) => !!v).join('\n\n')))
                }.width('100%')
              }
              .width('100%')
              .padding(10)
              .backgroundColor(Colors.codeCanvas)
              .borderRadius(Tokens.radiusControl)
              .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusControl })
              .transition(Motion.slideFade(this.env.reduceMotion, -4))
            }
          }.width('100%')
        }, (step: WorkStepView, index: number) => step.id + ':' + String(step.running) + ':' + String(index))
      }
    }
    .width('100%')
    .padding(Tokens.spaceS)
    .backgroundColor(Colors.control)
    .borderRadius(Tokens.radiusCard)
    .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusCard })
    .animation(Motion.state(this.env.reduceMotion))
    .id('chat.work.timeline')
  }
}

/**
 * Completed turn Changed Files card matching iOS `TurnChangesCard.swift` (#12016).
 */
@ComponentV2
export struct TurnChangesCard {
  @Require @Param changes: TurnChangeSet;
  @Event onOpenFile: (path: string) => void = () => {};
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private expanded: boolean = false;

  private visibleFiles(): TurnChangedFile[] {
    if (this.expanded || this.changes.files.length <= TURN_CHANGES_INITIAL_VISIBLE) {
      return this.changes.files.slice();
    }
    return this.changes.files.slice(0, TURN_CHANGES_INITIAL_VISIBLE);
  }

  private headerLabel(): string {
    const count = this.changes.files.length;
    return count === 1
      ? tr('turn_changes_one')
      : tr('turn_changes_many').replace('{count}', String(count));
  }

  build() {
    Column({ space: 0 }) {
      Row({ space: 10 }) {
        Stack() {
          SymbolGlyph(Symbols.edit).fontSize('15vp').fontColor([Colors.link])
        }
        .width(30)
        .height(30)
        .borderRadius(9)
        .backgroundColor(Colors.selection)
        Column({ space: Tokens.spaceXxs }) {
          Text(this.headerLabel())
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(1)
          if (this.changes.exact) {
            Row({ space: Tokens.spaceSm }) {
              if (this.changes.totalAdditions > 0) {
                Text('+' + String(this.changes.totalAdditions))
                  .fontSize(Tokens.typeLabel)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                  .fontFamily('monospace')
                  .fontWeight(FontWeight.Medium)
                  .fontColor(Colors.diffAdded)
              }
              if (this.changes.totalDeletions > 0) {
                Text('-' + String(this.changes.totalDeletions))
                  .fontSize(Tokens.typeLabel)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                  .fontFamily('monospace')
                  .fontWeight(FontWeight.Medium)
                  .fontColor(Colors.diffRemoved)
              }
            }
          }
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        if (this.changes.files.length > 0) {
          Button(tr('turn_changes_review'), { type: ButtonType.Capsule })
            .height(30)
            .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.link)
            .backgroundColor(Colors.selection)
            .onClick(() => {
              Haptics.tap();
              this.onOpenFile(this.changes.files[0].path);
            })
            .id('turn_changes.review')
        }
      }.width('100%').padding({ left: 14, right: 14, top: 10, bottom: 10 })

      Divider().strokeWidth(Tokens.divider).color(Colors.divider)

      Column({ space: Tokens.spaceXxs }) {
        ForEach(this.visibleFiles(), (file: TurnChangedFile) => {
          Button({ type: ButtonType.Normal }) {
            Row({ space: Tokens.spaceS }) {
              SymbolGlyph(Symbols.document).fontSize('14vp').fontColor([Colors.secondary])
              Text(file.shown)
                .fontSize(Tokens.typeCaption)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                .fontFamily('monospace')
                .fontColor(Colors.ink)
                .maxLines(1)
                .textOverflow({ overflow: TextOverflow.Ellipsis })
                .ellipsisMode(EllipsisMode.CENTER)
                .layoutWeight(1)
              if (file.exact && (file.additions > 0 || file.deletions > 0)) {
                Row({ space: Tokens.spaceXs }) {
                  if (file.additions > 0) {
                    Text('+' + String(file.additions))
                      .fontSize(Tokens.typeLabel)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                      .fontFamily('monospace')
                      .fontColor(Colors.diffAdded)
                  }
                  if (file.deletions > 0) {
                    Text('-' + String(file.deletions))
                      .fontSize(Tokens.typeLabel)
                      .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                      .fontFamily('monospace')
                      .fontColor(Colors.diffRemoved)
                  }
                }
              }
            }.width('100%').padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: 7, bottom: 7 }).constraintSize({ minHeight: 38 })
          }
          .width('100%')
          .padding(0)
          .backgroundColor(Color.Transparent)
          .onClick(() => {
            Haptics.tap();
            this.onOpenFile(file.path);
          })
          .id('turn_changes.file.' + file.shown)
        }, (file: TurnChangedFile) => file.path + ':' + String(file.additions) + ':' + String(file.deletions))

        if (this.changes.files.length > TURN_CHANGES_INITIAL_VISIBLE) {
          Button({ type: ButtonType.Normal }) {
            Row({ space: Tokens.spaceSm }) {
              Text(this.expanded
                ? tr('bubble_show_less')
                : tr('turn_changes_more').replace('{count}', String(this.changes.files.length - TURN_CHANGES_INITIAL_VISIBLE)))
                .fontSize(Tokens.typeCaption)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
                .fontColor(Colors.link)
              SymbolGlyph(Symbols.down)
                .fontSize('11vp')
                .fontColor([Colors.link])
                .rotate({ angle: this.expanded ? 180 : 0 })
                .animation(Motion.state(this.env.reduceMotion))
            }.padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: Tokens.spaceSm, bottom: Tokens.spaceS })
          }
          .width('100%')
          .padding(0)
          .backgroundColor(Color.Transparent)
          .onClick(() => {
            Haptics.tap();
            Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
              this.expanded = !this.expanded;
            });
          })
          .id('turn_changes.toggle')
        }
      }.width('100%').padding({ top: Tokens.spaceXs, bottom: Tokens.spaceXs })
    }
    .width('100%')
    .backgroundColor(Colors.styleCard || Colors.codeSurface)
    .borderRadius(Tokens.radiusCard)
    .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusCard })
    .shadow(PhoneStyle.cardElevation(this.env.dark))
    .transition(Motion.fadeScale(this.env.reduceMotion, 0.96))
    .id('turn_changes.card')
  }
}
