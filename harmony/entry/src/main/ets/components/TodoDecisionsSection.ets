import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { Motion } from '../design/Motion';
import { PersonaSectionLabel, PersonaSheetHeader } from '../design/NormalizedPrimitives';
import { PhoneStyle } from '../design/PhoneStyle';
import { Symbols } from '../design/Symbols';
import { tr } from '../design/Strings';
import { Tokens } from '../design/Tokens';
import {
  buildAddTodoDecisionPayload,
  currentTodoDecisions,
  formatDecisionTimestamp,
  isDecisionCurrent,
  listedTodoDecisions,
  parseTodoDecisionsResponse,
  shownTodoDecisions,
  supersedeCandidatesFor,
  TODO_DECISIONS_FIRST_COUNT,
  TodoDecision,
  todoDecisionPatchPath,
  TodoDecisionsDisplay,
  todoDecisionsListPath,
  todoDecisionsPostPath
} from '../core/TodoDecisions';
import { ClientModel } from '../models/ClientModel';
import { TodoWorkspaceItem } from '../models/TodoWorkspace';
import { toastCenter } from '../models/ToastCenter';
import { Haptics } from '../platform/Haptics';
import { HttpFailure } from '../platform/HubTransport';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';

@ComponentV2
export struct TodoDecisionsSection {
  @Require @Param model: ClientModel;
  @Require @Param todo: TodoWorkspaceItem;
  @Param editable: boolean = true;
  @Event onChanged: () => void = () => {};

  @Local private env: NativeEnvironment = nativeEnvironment;
  @Local private all: TodoDecision[] | undefined = undefined;
  @Local private loading: boolean = false;
  @Local private unsupported: boolean = false;
  @Local private loadError: string = '';
  @Local private showAll: boolean = false;
  @Local private display: TodoDecisionsDisplay = 'first';
  @Local private sheetOpen: boolean = false;
  @Local private revisingTarget: TodoDecision | undefined = undefined;
  @Local private questionDraft: string = '';
  @Local private choiceDraft: string = '';
  @Local private reasonDraft: string = '';
  @Local private saving: boolean = false;
  @Local private saveError: string = '';

  private activeTodoKey: string = '';
  private loadSerial: number = 0;

  aboutToAppear(): void {
    this.syncTodo(true);
  }

  @Monitor('todo.id', 'todo.manager', 'todo.updatedAt', 'model.connected')
  todoChanged(): void {
    this.syncTodo(false);
  }

  private todoKey(): string {
    return JSON.stringify([this.model.scope(), this.todo.manager || this.model.profile.manager, this.todo.id]);
  }

  private syncTodo(force: boolean): void {
    const key = this.todoKey();
    if (!this.todo.id) {
      this.activeTodoKey = '';
      this.all = undefined;
      this.unsupported = false;
      this.loadError = '';
      return;
    }
    if (key !== this.activeTodoKey) {
      this.activeTodoKey = key;
      this.loadSerial++;
      this.all = undefined;
      this.unsupported = false;
      this.loadError = '';
      this.showAll = false;
      this.display = 'first';
      this.sheetOpen = false;
      this.revisingTarget = undefined;
      this.load().catch(() => undefined);
      return;
    }
    if (force || this.all === undefined) {
      this.load().catch(() => undefined);
    } else {
      this.load().catch(() => undefined);
    }
  }

  private is404(err: Object): boolean {
    if (err instanceof HttpFailure) return err.status === 404;
    const rec = err as Record<string, Object>;
    return typeof rec['status'] === 'number' && rec['status'] === 404;
  }

  private managerQuery(): string | undefined {
    const m = this.todo.manager || '';
    if (m && m !== this.model.profile.manager) return m;
    return undefined;
  }

  private async load(): Promise<void> {
    const owner = this.model;
    const todoId = this.todo.id;
    if (!todoId || (!owner.connected && !owner.demo)) return;
    const key = this.todoKey();
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    const serial = ++this.loadSerial;
    if (this.all === undefined) this.loading = true;
    this.loadError = '';
    try {
      const raw = await owner.repo().call(todoDecisionsListPath(todoId, this.managerQuery()));
      if (serial !== this.loadSerial || key !== this.todoKey() || !owner.owns(generation, scope)) return;
      this.all = parseTodoDecisionsResponse(raw);
      this.unsupported = false;
      this.loadError = '';
    } catch (err) {
      if (serial !== this.loadSerial || key !== this.todoKey() || !owner.owns(generation, scope)) return;
      if (this.is404(err as Object)) {
        this.unsupported = true;
        this.loadError = '';
      } else if (this.all === undefined) {
        this.loadError = 'todo_decisions_load_failed';
      }
    } finally {
      if (serial === this.loadSerial) this.loading = false;
    }
  }

  @Computed private get currentList(): TodoDecision[] {
    return currentTodoDecisions(this.all ?? []);
  }

  @Computed private get revisedCount(): number {
    return Math.max(0, (this.all?.length ?? 0) - this.currentList.length);
  }

  @Computed private get listed(): TodoDecision[] {
    return listedTodoDecisions(this.all ?? [], this.showAll);
  }

  @Computed private get shown(): TodoDecision[] {
    return shownTodoDecisions(this.listed, this.display);
  }

  @Computed private get folded(): boolean {
    return this.display === 'folded' && this.listed.length > 0;
  }

  @Computed private get hasMore(): boolean {
    return !this.folded && this.listed.length > TODO_DECISIONS_FIRST_COUNT;
  }

  private decisionById(id: string): TodoDecision | undefined {
    return (this.all ?? []).find((item: TodoDecision) => item.id === id);
  }

  private sourceLabel(d: TodoDecision): string {
    if (d.source === 'outcome') return tr('todo_decision_source_outcome');
    if (d.source === 'agent') return tr('todo_decision_source_agent');
    if (d.source === 'thread') return tr('todo_decision_source_thread');
    if (d.source === 'card') return tr('todo_decision_source_card');
    return tr('todo_decision_source_manual');
  }

  private decisionMeta(d: TodoDecision): string {
    const parts: string[] = [];
    const when = formatDecisionTimestamp(d.createdAt);
    if (when) parts.push(when);
    parts.push(this.sourceLabel(d));
    if (!isDecisionCurrent(d)) {
      parts.push(tr('todo_decision_revised_badge'));
    } else if (d.supersedesId) {
      const replaced = this.decisionById(d.supersedesId);
      if (replaced) {
        parts.push(tr('todo_decision_revises').replace('{choice}', replaced.choice));
      }
    }
    return parts.join(' · ');
  }

  private openAddSheet(supersedes?: TodoDecision): void {
    Haptics.tap();
    this.revisingTarget = supersedes;
    this.questionDraft = supersedes ? supersedes.question : '';
    this.choiceDraft = '';
    this.reasonDraft = '';
    this.saveError = '';
    this.sheetOpen = true;
  }

  private async submitDecision(): Promise<void> {
    if (this.saving) return;
    const payload = buildAddTodoDecisionPayload(
      this.questionDraft,
      this.choiceDraft,
      this.reasonDraft,
      this.revisingTarget?.id || ''
    );
    if (!payload) return;
    const owner = this.model;
    const todoId = this.todo.id;
    const key = this.todoKey();
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    if (!todoId || (!owner.connected && !owner.demo)) return;
    this.saving = true;
    this.saveError = '';
    try {
      await owner.repo().call(todoDecisionsPostPath(todoId, this.managerQuery()), 'POST', payload);
      if (key !== this.todoKey() || !owner.owns(generation, scope)) return;
      this.display = 'all';
      this.sheetOpen = false;
      Haptics.success();
      toastCenter.success(tr(this.revisingTarget ? 'todo_decision_revised_toast' : 'todo_decision_recorded_toast'));
      await this.load();
      this.onChanged();
    } catch (_) {
      if (key !== this.todoKey() || !owner.owns(generation, scope)) return;
      Haptics.error();
      this.saveError = tr('failed');
    } finally {
      this.saving = false;
    }
  }

  private async markSupersedes(d: TodoDecision, oldId: string): Promise<void> {
    if (this.saving) return;
    const owner = this.model;
    const todoId = this.todo.id;
    const key = this.todoKey();
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    if (!todoId || (!owner.connected && !owner.demo)) return;
    this.saving = true;
    try {
      const body: Record<string, Object> = {};
      body['supersedes_id'] = oldId;
      await owner.repo().call(todoDecisionPatchPath(todoId, d.id, this.managerQuery()), 'PATCH', body);
      if (key !== this.todoKey() || !owner.owns(generation, scope)) return;
      Haptics.success();
      toastCenter.success(tr(oldId ? 'todo_decision_marked_revised' : 'todo_decision_revision_undone'));
      await this.load();
      this.onChanged();
    } catch (_) {
      if (key !== this.todoKey() || !owner.owns(generation, scope)) return;
      Haptics.error();
      toastCenter.error(tr('failed'));
    } finally {
      this.saving = false;
    }
  }

  @Builder decisionMenu(d: TodoDecision) {
    Menu() {
      ForEach(supersedeCandidatesFor(d, this.all ?? []), (cand: TodoDecision) => {
        MenuItem({ content: tr('todo_decision_revises_item').replace('{question}', cand.question).replace('{choice}', cand.choice) })
          .onClick(() => {
            this.markSupersedes(d, cand.id).catch(() => undefined);
          })
      }, (cand: TodoDecision) => cand.id)
      if (d.supersedesId) {
        MenuItem({ content: tr('todo_decision_undo_revision') })
          .onClick(() => {
            this.markSupersedes(d, '').catch(() => undefined);
          })
      }
    }
  }

  @Builder decisionRow(d: TodoDecision) {
    Column({ space: Tokens.spaceXs }) {
      Row({ space: Tokens.spaceS }) {
        Text(d.question)
          .fontSize(Tokens.typeBody)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody))
          .fontWeight(FontWeight.Medium)
          .fontColor(isDecisionCurrent(d) ? Colors.ink : Colors.tertiary)
          .decoration({ type: isDecisionCurrent(d) ? TextDecorationType.None : TextDecorationType.LineThrough })
          .layoutWeight(1)

        if (isDecisionCurrent(d) && this.editable) {
          Row({ space: Tokens.spaceXs }) {
            Button(tr('todo_decision_revise'), { type: ButtonType.Normal })
              .buttonStyle(ButtonStyleMode.TEXTUAL)
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.link)
              .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
              .padding({ left: Tokens.spaceS, right: Tokens.spaceS })
              .backgroundColor(Color.Transparent)
              .onClick(() => this.openAddSheet(d))
              .id('todo.decision.revise.' + d.id)

            if (supersedeCandidatesFor(d, this.all ?? []).length > 0 || !!d.supersedesId) {
              Button({ type: ButtonType.Normal }) {
                Row({ space: 3 }) {
                  Circle().width(3.5).height(3.5).fill(Colors.secondary)
                  Circle().width(3.5).height(3.5).fill(Colors.secondary)
                  Circle().width(3.5).height(3.5).fill(Colors.secondary)
                }
              }
              .buttonStyle(ButtonStyleMode.TEXTUAL)
              .width(Tokens.controlBar)
              .height(Tokens.controlBar)
              .padding(0)
              .backgroundColor(Color.Transparent)
              .accessibilityText(tr('more'))
              .bindMenu(this.decisionMenu(d))
              .id('todo.decision.more.' + d.id)
            }
          }
          .alignItems(VerticalAlign.Center)
        }
      }
      .width('100%')
      .alignItems(VerticalAlign.Top)

      Text(tr('todo_decision_chose') + ' ' + d.choice)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontColor(isDecisionCurrent(d) ? Colors.ink : Colors.tertiary)
        .decoration({ type: isDecisionCurrent(d) ? TextDecorationType.None : TextDecorationType.LineThrough })

      if (d.reason) {
        Text(tr('todo_decision_reason_prefix') + ' ' + d.reason)
          .fontSize(Tokens.typeCaption)
          .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .fontColor(isDecisionCurrent(d) ? Colors.secondary : Colors.tertiary)
          .decoration({ type: isDecisionCurrent(d) ? TextDecorationType.None : TextDecorationType.LineThrough })
      }

      Text(this.decisionMeta(d))
        .fontSize(Tokens.typeLabel)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
        .fontColor(Colors.secondary)
        .maxLines(2)
    }
    .width('100%')
    .alignItems(HorizontalAlign.Start)
    .padding({ top: Tokens.spaceS, bottom: Tokens.spaceS })
    .id('todo.decision.' + d.id)
  }

  @Builder addDecisionSheet() {
    Column({ space: 0 }) {
      PersonaSheetHeader({
        title: tr(this.revisingTarget ? 'todo_decision_revise' : 'todo_decision_record_title'),
        closeIdentity: 'todo.decision.cancel',
        trailingLabel: this.saving ? tr('saving') : tr(this.revisingTarget ? 'todo_decision_revise' : 'todo_decision_save'),
        trailingIdentity: 'todo.decision.save',
        showDivider: true,
        onClose: () => {
          if (!this.saving) this.sheetOpen = false;
        },
        onTrailing: () => {
          this.submitDecision().catch(() => undefined);
        }
      })

      Scroll() {
        Column({ space: Tokens.spaceL }) {
          if (this.revisingTarget) {
            Text(tr('todo_decision_revise_note').replace('{choice}', this.revisingTarget!.choice))
              .fontSize(Tokens.typeSecondary)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
              .fontColor(Colors.secondary)
              .width('100%')
              .padding(Tokens.spaceM)
              .backgroundColor(Colors.control)
              .borderRadius(Tokens.radiusControl)
          }

          Column({ space: Tokens.spaceXs }) {
            PersonaSectionLabel({ text: tr('todo_decision_question_label') })
            TextInput({ text: this.questionDraft, placeholder: tr('todo_decision_question_placeholder') })
              .fontSize(Tokens.typeBody)
              .fontColor(Colors.ink)
              .backgroundColor(Colors.surface)
              .borderRadius(Tokens.radiusControl)
              .constraintSize({ minHeight: Tokens.controlBar })
              .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
              .enabled(!this.saving)
              .onChange((val: string) => { this.questionDraft = val; })
              .id('todo.decision.question')
          }
          .width('100%')

          Column({ space: Tokens.spaceXs }) {
            PersonaSectionLabel({ text: tr('todo_decision_choice_label') })
            TextInput({ text: this.choiceDraft, placeholder: tr('todo_decision_choice_placeholder') })
              .fontSize(Tokens.typeBody)
              .fontColor(Colors.ink)
              .backgroundColor(Colors.surface)
              .borderRadius(Tokens.radiusControl)
              .constraintSize({ minHeight: Tokens.controlBar })
              .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
              .enabled(!this.saving)
              .onChange((val: string) => { this.choiceDraft = val; })
              .id('todo.decision.choice')
          }
          .width('100%')

          Column({ space: Tokens.spaceXs }) {
            PersonaSectionLabel({ text: tr('todo_decision_reason_label') })
            TextArea({ text: this.reasonDraft, placeholder: tr('todo_decision_reason_placeholder') })
              .fontSize(Tokens.typeBody)
              .fontColor(Colors.ink)
              .backgroundColor(Colors.surface)
              .borderRadius(Tokens.radiusControl)
              .height(108)
              .padding(Tokens.spaceM)
              .enabled(!this.saving)
              .onChange((val: string) => { this.reasonDraft = val; })
              .id('todo.decision.reason')
          }
          .width('100%')

          if (this.saveError) {
            Text(this.saveError)
              .fontSize(Tokens.typeSecondary)
              .fontColor(Colors.danger)
              .width('100%')
          }

          Button(tr(this.revisingTarget ? 'todo_decision_revise' : 'todo_decision_save'), { type: ButtonType.Capsule })
            .width('100%')
            .constraintSize({ minHeight: Tokens.controlBar })
            .backgroundColor(Colors.action)
            .fontColor(Colors.onAction)
            .fontSize(Tokens.typeBody)
            .fontWeight(FontWeight.Medium)
            .enabled(!this.saving && !!this.questionDraft.trim() && !!this.choiceDraft.trim())
            .onClick(() => {
              this.submitDecision().catch(() => undefined);
            })
            .id('todo.decision.submit')
        }
        .width('100%')
        .padding({ left: Tokens.spaceL, right: Tokens.spaceL, top: Tokens.spaceM, bottom: Tokens.spaceXl })
        .alignItems(HorizontalAlign.Start)
      }
      .layoutWeight(1)
      .width('100%')
    }
    .width('100%')
    .height('100%')
    .backgroundColor(Colors.sheetBackground)
    .id('todo.decision.sheet')
  }

  build() {
    if (!this.unsupported) {
      Column({ space: Tokens.spaceS }) {
        Row({ space: Tokens.spaceXs }) {
          Text(tr('todo_decisions_title') + ' · ' + String(this.currentList.length))
            .fontSize(Tokens.typeSectionHeader)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSectionHeader))
            .fontWeight(600)
            .fontColor(Colors.secondary)
            .layoutWeight(1)

          if (this.editable) {
            Button(tr('add'), { type: ButtonType.Normal })
              .buttonStyle(ButtonStyleMode.TEXTUAL)
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.link)
              .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
              .padding({ left: Tokens.spaceS, right: Tokens.spaceS })
              .backgroundColor(Color.Transparent)
              .onClick(() => this.openAddSheet(undefined))
              .id('todo.decisions.add')
          }

          if (this.all !== undefined && this.listed.length > 0) {
            Button({ type: ButtonType.Normal }) {
              SymbolGlyph(this.folded ? Symbols.down : Symbols.up)
                .fontSize('14vp')
                .fontColor([Colors.link])
            }
            .buttonStyle(ButtonStyleMode.TEXTUAL)
            .width(Tokens.controlBar)
            .height(Tokens.controlBar)
            .padding(0)
            .backgroundColor(Color.Transparent)
            .accessibilityText(tr(this.folded ? 'expand_all' : 'collapse_all'))
            .onClick(() => {
              Haptics.tap();
              Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
                this.display = this.folded ? 'all' : 'folded';
              });
            })
            .id('todo.decisions.fold')
          }
        }
        .width('100%')
        .alignItems(VerticalAlign.Center)

        if (this.loadError && this.all === undefined) {
          Row({ space: Tokens.spaceM }) {
            Text(tr(this.loadError))
              .fontSize(Tokens.typeSecondary)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
              .fontColor(Colors.secondary)
              .layoutWeight(1)
            Button(tr('retry'), { type: ButtonType.Normal })
              .buttonStyle(ButtonStyleMode.TEXTUAL)
              .fontSize(Tokens.typeCaption)
              .fontColor(Colors.link)
              .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
              .onClick(() => { this.load().catch(() => undefined); })
              .id('todo.decisions.retry')
          }
          .width('100%')
          .padding(Tokens.spaceM)
          .backgroundColor(Colors.surface)
          .borderRadius(Tokens.radiusCard)
        } else if (this.all === undefined || this.loading && this.all === undefined) {
          Row({ space: Tokens.spaceS }) {
            LoadingProgress().width(20).height(20)
            Text(tr('todo_decisions_loading'))
              .fontSize(Tokens.typeSecondary)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
              .fontColor(Colors.secondary)
          }
          .width('100%')
          .padding(Tokens.spaceM)
          .backgroundColor(Colors.surface)
          .borderRadius(Tokens.radiusCard)
        } else if (!this.folded) {
          Column({ space: Tokens.spaceXs }) {
            if (this.shown.length === 0) {
              Text(tr('todo_decisions_empty'))
                .fontSize(Tokens.typeSecondary)
                .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                .fontColor(Colors.secondary)
                .width('100%')
                .id('todo.decisions.empty')
            } else {
              ForEach(this.shown, (d: TodoDecision, idx: number) => {
                this.decisionRow(d)
                if (idx < this.shown.length - 1) {
                  Divider().strokeWidth(Tokens.divider).color(Colors.divider)
                }
              }, (d: TodoDecision) => d.id + ':' + d.supersededBy + ':' + d.supersedesId)

              if (this.hasMore) {
                Button(
                  this.display === 'first'
                    ? tr('todo_decisions_show_all').replace('{count}', String(this.listed.length))
                    : tr('todo_decisions_show_first').replace('{count}', String(TODO_DECISIONS_FIRST_COUNT)),
                  { type: ButtonType.Normal }
                )
                  .buttonStyle(ButtonStyleMode.TEXTUAL)
                  .fontSize(Tokens.typeSecondary)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                  .fontWeight(FontWeight.Medium)
                  .fontColor(Colors.link)
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .padding({ left: 0, right: Tokens.spaceM })
                  .backgroundColor(Color.Transparent)
                  .onClick(() => {
                    Haptics.tap();
                    Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
                      this.display = this.display === 'first' ? 'all' : 'first';
                    });
                  })
                  .id('todo.decisions.more')
              }

              if (this.revisedCount > 0) {
                Button(
                  this.showAll
                    ? tr('todo_decisions_current_only')
                    : tr('todo_decisions_full_history').replace('{count}', String(this.revisedCount)),
                  { type: ButtonType.Normal }
                )
                  .buttonStyle(ButtonStyleMode.TEXTUAL)
                  .fontSize(Tokens.typeSecondary)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
                  .fontWeight(FontWeight.Medium)
                  .fontColor(Colors.link)
                  .constraintSize({ minHeight: Tokens.controlBar })
                  .padding({ left: 0, right: Tokens.spaceM })
                  .backgroundColor(Color.Transparent)
                  .onClick(() => {
                    Haptics.tap();
                    Motion.run(this.getUIContext(), Motion.state(this.env.reduceMotion), () => {
                      this.showAll = !this.showAll;
                    });
                  })
                  .id('todo.decisions.history')
              }
            }
          }
          .width('100%')
          .alignItems(HorizontalAlign.Start)
          .padding(Tokens.spaceL)
          .backgroundColor(Colors.surface)
          .borderRadius(Tokens.radiusCard)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.env.dark), radius: Tokens.radiusCard })
          .shadow(PhoneStyle.cardElevation(this.env.dark))
        }
      }
      .width('100%')
      .alignItems(HorizontalAlign.Start)
      .bindSheet($$this.sheetOpen, this.addDecisionSheet(), {
        height: SheetSize.MEDIUM,
        showClose: false,
        dragBar: true,
        backgroundColor: Colors.sheetBackground
      })
      .id('todo.decisions.section')
    }
  }
}
