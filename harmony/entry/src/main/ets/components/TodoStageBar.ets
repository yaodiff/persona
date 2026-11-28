import { Colors } from '../design/Colors';
import { PersonaStatusPill, PersonaStatusTone } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import { buildTodoStageProjection, resolveTodoStage, TodoStage, TodoStageProjection, TodoStageStep } from '../core/TodoDecisions';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

export function todoStageLabelKey(stage: TodoStage | 'done'): string {
  return 'todo_stage_' + stage;
}

export function todoStagePillTone(stage: TodoStage, done: boolean): PersonaStatusTone {
  if (done || stage === 'verify') return 'unread';
  if (stage === 'review' || stage === 'merge') return 'needs';
  if (stage === 'develop' || stage === 'deploy') return 'working';
  return 'neutral';
}

/**
 * Compact stage pill for Todo list rows; draws nothing when `stage` is absent or `stageKind === 'general'`.
 */
@ComponentV2
export struct TodoStagePill {
  @Param stage: string = '';
  @Param stageKind: string = '';
  @Param done: boolean = false;
  @Param identity: string = '';

  @Computed private get resolved(): TodoStage | null {
    return resolveTodoStage(this.stage, this.stageKind);
  }

  build() {
    if (this.resolved) {
      PersonaStatusPill({
        tone: todoStagePillTone(this.resolved, this.done),
        label: tr(todoStageLabelKey(this.done ? 'done' : this.resolved)),
        identity: this.identity
      })
    }
  }
}

/**
 * Full 6-step Todo Stage progress bar (`Plan -> Develop -> Review -> Merge -> Deploy -> Verified`,
 * appending `Done` when closed) aligned with iOS `TodoStageBar.swift`.
 * Drops step labels cleanly to `Step N of 6 · <Stage>` when horizontal room is narrow.
 */
@ComponentV2
export struct TodoStageBar {
  @Param stage: string = '';
  @Param stageKind: string = '';
  @Param done: boolean = false;
  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private barWidth: number = 0;

  @Computed private get projection(): TodoStageProjection | null {
    const resolved = resolveTodoStage(this.stage, this.stageKind);
    if (!resolved) return null;
    return buildTodoStageProjection(resolved, this.done);
  }

  @Computed private get showStepNames(): boolean {
    if (this.barWidth > 0 && this.barWidth < 330) return false;
    if (this.env.fontScale > 1.25 && this.barWidth < 420) return false;
    return true;
  }

  private dotLabel(step: TodoStageStep, proj: TodoStageProjection): string {
    const label = tr(todoStageLabelKey(step.stage));
    const statusKey = step.state === 'passed'
      ? 'todo_stage_state_done'
      : step.state === 'current'
        ? 'todo_stage_state_current'
        : 'todo_stage_state_ahead';
    return label + ', ' + tr('todo_stage_step_of')
      .replace('{current}', String(step.stepNumber))
      .replace('{total}', String(proj.totalSteps)) + ', ' + tr(statusKey);
  }

  private summaryText(proj: TodoStageProjection): string {
    const label = tr(todoStageLabelKey(proj.done ? 'done' : proj.stage));
    return tr('todo_stage_step_summary')
      .replace('{current}', String(proj.currentNumber))
      .replace('{total}', String(proj.totalSteps))
      .replace('{stage}', label);
  }

  @Builder stepDot(step: TodoStageStep) {
    if (step.state === 'passed') {
      Stack({ alignContent: Alignment.Center }) {
        Circle()
          .width(16)
          .height(16)
          .fill(Colors.positive)
        SymbolGlyph(Symbols.check)
          .fontSize('10vp')
          .fontWeight(FontWeight.Bold)
          .fontColor([Colors.onAction])
      }
      .width(18)
      .height(18)
    } else if (step.state === 'current') {
      Stack({ alignContent: Alignment.Center }) {
        Circle()
          .width(20)
          .height(20)
          .fill(Colors.accentWell)
        Circle()
          .width(12)
          .height(12)
          .fill(Colors.link)
      }
      .width(20)
      .height(20)
    } else {
      Circle()
        .width(14)
        .height(14)
        .fill(Color.Transparent)
        .stroke(Colors.divider)
        .strokeWidth(1.5)
    }
  }

  @Builder connector(reached: boolean, visible: boolean) {
    Row()
      .layoutWeight(1)
      .height(2)
      .backgroundColor(reached ? Colors.positive : Colors.control)
      .opacity(visible ? (reached ? 0.55 : 1) : 0)
  }

  @Builder dotWithLines(step: TodoStageStep, index: number, total: number, currentIndex: number) {
    Row({ space: 0 }) {
      this.connector(index <= currentIndex, index > 0)
      this.stepDot(step)
      this.connector(index < currentIndex, index < total - 1)
    }
    .width('100%')
    .height(22)
    .alignItems(VerticalAlign.Center)
  }

  build() {
    if (this.projection) {
      Column({ space: Tokens.spaceXs }) {
        Row({ space: 0 }) {
          ForEach(this.projection!.steps, (step: TodoStageStep, index: number) => {
            Column({ space: Tokens.spaceXs }) {
              this.dotWithLines(step, index, this.projection!.steps.length, this.projection!.currentIndex)
              if (this.showStepNames) {
                Text(tr(todoStageLabelKey(step.stage)))
                  .fontSize(Tokens.typeLabel)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
                  .fontWeight(step.state === 'current' ? FontWeight.Bold : FontWeight.Medium)
                  .fontColor(
                    step.state === 'current'
                      ? Colors.link
                      : step.state === 'passed'
                        ? Colors.ink
                        : Colors.secondary
                  )
                  .maxLines(1)
                  .textOverflow({ overflow: TextOverflow.Ellipsis })
                  .textAlign(TextAlign.Center)
              }
            }
            .layoutWeight(1)
            .alignItems(HorizontalAlign.Center)
            .accessibilityGroup(true)
            .accessibilityText(this.dotLabel(step, this.projection!))
            .id('todo.stage.' + step.id)
          }, (step: TodoStageStep) => step.id + ':' + step.state)
        }
        .width('100%')

        if (!this.showStepNames) {
          Text(this.summaryText(this.projection!))
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.link)
            .width('100%')
            .id('todo.stage.summary')
        }
      }
      .width('100%')
      .padding(Tokens.spaceM)
      .backgroundColor(Colors.surface)
      .borderRadius(Tokens.radiusCard)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusCard })
      .shadow(PhoneStyle.cardElevation(this.env.dark))
      .onAreaChange((_old: Area, area: Area) => {
        const w = Number(area.width);
        if (Number.isFinite(w) && w > 0 && Math.abs(w - this.barWidth) > 1) {
          this.barWidth = w;
        }
      })
      .id('todo.stage')
    }
  }
}
