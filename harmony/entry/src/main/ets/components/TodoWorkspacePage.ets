import { phoneDueDate, phoneTodoPlanDate, relativeActivityDate } from '../core/PhoneDate';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { i18n } from '@kit.LocalizationKit';
import settings from '@ohos.settings';
import { IconActions } from '../design/IconActions';
import { Symbols } from '../design/Symbols';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { ButtonStyles, SelectStyles } from '../design/Controls';
import { Haptics } from '../platform/Haptics';
import { NeedsYouPulseDot, WorkingArcSpinner } from './StatusMotionMarks';
import { workspaceGridColumns, workspaceGridGutter, workspaceHorizontalPadding } from '../core/WorkspaceGrid';
import { ComposeListItemV2, ContentItemV2, LengthMetrics } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { TodoWorkspace, TodoWorkspaceAgent, TodoWorkspaceGroup, TodoWorkspaceItem, TodoWorkspaceSection,
  TodoWorkspaceSpace, TodoWorkspaceSpaceChoice, TodoCalendarBand, TodoCalendarDrop, todoCalendarDrop, todoDropDue, todoCalendarDays, todoDayKey, todoAgentStateLabel, todoMatchesDue, TODO_DUE_FILTERS, TODO_LIST_ORDERS } from '../models/TodoWorkspace';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { TODO_HUB_SECTIONS, todoHubLabel } from '../models/TodoHubView';
import { ResourceRow, Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr, trError } from '../design/Strings';
import { MarkdownText } from './MarkdownText';
import { PhoneTodoDetailForm } from './PhoneTodoDetailForm';
import { PhoneTodoDetailEditor } from '../models/PhoneTodoDetailEditor';
import { markdownVisibleText, parseMarkdown } from '../core/Markdown';
import { composerModelName } from '../models/SessionComposerModes';
import { launchModelName } from '../models/AgentLaunchChoices';
import { PersonaStatusPill } from '../design/NormalizedPrimitives';
import { detailLines, rowSummary, TodoWaitDetailModel, TodoWaitInput, TodoWaitLabels, TodoWaitRowSummary } from '../core/TodoWaitText';
import { TodoStageBar, TodoStagePill } from './TodoStageBar';
import { TodoDecisionsSection } from './TodoDecisionsSection';
import { RuleSuggestionBar } from './RuleSuggestionBar';

interface TodoWindowFrame { left:number; top:number; width:number; height:number; }

@ObservedV2
class BoundTodoSection {
  readonly identity: string;
  readonly key: string;
  @Trace value: TodoWorkspaceSection;
  @Trace rows: BoundRow<TodoWorkspaceGroup>[] = [];

  constructor(identity: string, key: string, value: TodoWorkspaceSection) {
    this.identity = identity;
    this.key = key;
    this.value = value;
  }
}

@ComponentV2
export struct TodoWorkspacePage {
  @Require @Param model: ClientModel;
  @Param row: ResourceRow | undefined = undefined;
  @Param list: boolean = true;
  @Param phoneList: boolean = false;
  @Param phoneDetail: boolean = false;
  @Param createRevision: number = 0;
  @Param visible: boolean = true;
  @Event onOpen: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onOpenResource: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onLink: (address: string) => void = (_address: string) => {};
  @Event onCreateAgent: (todo: ResourceRow) => void = (_todo: ResourceRow) => {};
  @Event onTitle: (title: string) => void = (_title: string) => {};
  @Local workspace: TodoWorkspace = new TodoWorkspace();
  @Local listWidth: number = 0;
  @Local detailWidth:number=402;
  @Local detailEditor: PhoneTodoDetailEditor = new PhoneTodoDetailEditor();
  private detailPlanKey: string = '';
  private seenNoticeTimer: number = -1;
  @Local sectionBindings: BoundTodoSection[] = [];
  @Local expandedGroups:string[] = [];
  @Local showDone:boolean = false;
  @Local showShelved:boolean = false;
  @Local liftedId:string = '';
  @Local landing:TodoCalendarDrop | undefined = undefined;
  private dragged:TodoWorkspaceItem | undefined;
  private bands:TodoCalendarBand[] = [];
  private calendarScroll:Scroller = new Scroller();
  private dragTimer:number = -1;
  private edge:number = 0;
  private dragX:number = 0;
  private dragY:number = 0;
  private ignoreClickUntil:number = 0;
  @Local clock:number = Date.now();
  @Local listClockStyle:string = 'locale';
  private clockTimer:number = -1;
  private weekStart:number = 1;
  @Local lineageRowBindings: BoundRow<ResourceRow>[] = [];
  @Local relatedTodoBindings: BoundRow<TodoWorkspaceItem>[] = [];
  @Local spaceBindings: BoundRow<TodoWorkspaceSpace>[] = [];
  @Local activeAgentBindings: BoundRow<TodoWorkspaceAgent>[] = [];
  @Local previousAgentBindings: BoundRow<TodoWorkspaceAgent>[] = [];
  @Local previousAgentsOpen: boolean = false;
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  private sectionBindingSequence: number = 0;
  private pageAvailable: boolean = false;
  private lastVisible: boolean = true;
  private workspaceOpening: boolean = false;
  private workspaceInitialized: boolean = false;
  private queuedRefresh: boolean = false;
  private openSerial: number = 0;

  aboutToAppear(): void {
    this.pageAvailable = true;
    this.readListClockStyle();
    try { this.weekStart = i18n.System.getFirstDayOfWeek() % 7; } catch (_) {}
    this.clock=Date.now(); this.clockTimer=setInterval(() => { if (this.visible) this.clock=Date.now(); },60000);
    this.lastVisible = this.visible;
    this.previousAgentsOpen = false;
    this.syncRowBindings();
    this.openWorkspace();
  }
  aboutToDisappear(): void {
    clearTimeout(this.seenNoticeTimer);
    this.pageAvailable = false; this.clearDrag();
    clearInterval(this.clockTimer); this.clockTimer=-1;
    this.openSerial++;
    this.workspaceOpening = false;
    this.workspaceInitialized = false;
    this.queuedRefresh = false;
    if (this.list) this.workspace.close();
    else {
      const serial=this.openSerial;
      this.detailEditor.finish(this.workspace).finally(() => { if (!this.pageAvailable && this.openSerial===serial) this.workspace.close(); });
    }
  }

  @Monitor('row', 'list') routeChanged(): void { this.syncRowBindings(); if (this.pageAvailable) this.openWorkspace(); }
  @Monitor('createRevision') createRequested(): void {
    if (!this.phoneList || !this.list || !this.visible) return;
    if (this.workspace.calendarOpen) this.calendarCreate();
    else this.workspace.startCreate();
  }

  @Monitor('model.profile') profileChanged(): void {
    this.detailEditor.reset(); this.detailPlanKey='';
    this.expandedGroups=[]; this.showDone=false; this.showShelved=false; this.clearDrag();
    this.previousAgentsOpen = false;
    this.syncRowBindings();
    if (this.pageAvailable) this.openWorkspace();
  }
  @Monitor('model.connected') connectionChanged(): void {
    if (!this.pageAvailable) return;
    if (this.model.connected) this.refreshWhenReady(true);
    else this.workspace.error = 'offline';
  }
  @Monitor('visible') visibilityChanged(): void {
    const becameVisible = !this.lastVisible && this.visible;
    this.lastVisible = this.visible;
    if (!this.visible) this.clearDrag();
    if (becameVisible && this.pageAvailable) {
      this.publishTitle();
      this.refreshWhenReady();
      this.readListClockStyle();
    }
  }
  @Monitor('model.sessions') sessionsChanged(): void { this.workspace.update(this.model); }
  @Monitor('model.machines') machinesChanged(): void { this.workspace.update(this.model); }
  @Monitor('model.knowledge') projectsChanged(): void { this.workspace.update(this.model); }
  @Monitor('workspace.selected') selectedChanged(): void {
    this.syncRowBindings(); this.publishTitle();
    if (!this.list) {
      this.detailEditor.sync(this.workspace);
      const row=this.workspace.selected, key=JSON.stringify([this.model.scope(),row.manager,row.id]);
      if (row.id && key!==this.detailPlanKey) {
        this.detailPlanKey=key; this.workspace.loadDetailProjects().catch(() => {});
        if (this.phoneDetail && this.visible) this.workspace.markSeen(row,true).catch(() => {});
      }
    }
  }
  @Monitor('workspace.items', 'workspace.search', 'workspace.statusFilter', 'workspace.groupBy','workspace.dueFilter','workspace.order','clock',
    'workspace.hubCounts', 'workspace.hubChoice','workspace.seenPreview',
    'workspace.projects', 'workspace.sessions', 'workspace.machines') rowsChanged(): void { this.syncRowBindings(); }

  private openWorkspace(): void {
    const serial = ++this.openSerial;
    this.workspaceOpening = true;
    this.workspaceInitialized = false;
    this.queuedRefresh = false;
    this.workspace.open(this.model, this.row, this.list).catch(() => undefined).finally(() => {
      if (serial !== this.openSerial) return;
      this.workspaceOpening = false;
      this.workspaceInitialized = true;
      if (this.queuedRefresh && this.pageAvailable && this.visible) {
        this.queuedRefresh = false;
        this.workspace.refresh().catch(() => undefined);
      }
    });
  }

  @Monitor('workspace.seenUndo','workspace.saving') seenNoticeChanged():void {
    clearTimeout(this.seenNoticeTimer);
    const notice=this.workspace.seenUndo;
    if (notice && !this.workspace.saving) this.seenNoticeTimer=setTimeout(()=>{
      if (this.workspace.seenUndo===notice) this.workspace.seenUndo=undefined;
    }, 6000);
  }

  private refreshWhenReady(retryOpening: boolean = false): void {
    if (!this.pageAvailable || !this.visible) return;
    if (this.workspaceOpening) {
      // The initial open loads the workspace and detail target. A connection retry
      // still needs a fresh read once that open has rebound its owner.
      if (this.workspaceInitialized || retryOpening) this.queuedRefresh = true;
      return;
    }
    if (this.workspaceInitialized) this.workspace.refresh().catch(() => undefined);
  }

  private publishTitle(): void {
    const row = this.row, selected = this.workspace.selected;
    if (this.list || !row || !selected.id || this.workspace.targetGone || row.id !== selected.id) return;
    const manager = row.manager || this.model.profile.manager;
    if (selected.manager && selected.manager !== manager) return;
    this.onTitle(selected.title);
  }

  private syncRowBindings(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager,
      this.row?.key() || 'list', this.list ? 'list' : 'detail']);
    const sameOwnerScope = this.rowBindingOwner === this.model && this.rowBindingScope === scope;
    const previous = sameOwnerScope ? this.sectionBindings : [];
    const available = new Map<string, BoundTodoSection[]>();
    previous.forEach((binding: BoundTodoSection) => {
      const bindings = available.get(binding.identity) || [];
      bindings.push(binding);
      available.set(binding.identity, bindings);
    });
    const occurrences = new Map<string, number>(), next: BoundTodoSection[] = [];
    this.workspace.sections(this.clock,this.weekStart).forEach((section: TodoWorkspaceSection) => {
      const occurrence = occurrences.get(section.key) || 0;
      occurrences.set(section.key, occurrence + 1);
      const identity = JSON.stringify([section.key, occurrence]);
      const matches = available.get(identity);
      const existing = matches && matches.length ? matches.shift() : undefined;
      let binding: BoundTodoSection;
      if (existing) {
        binding = existing;
        binding.value = section;
        binding.rows = reconcileBoundRows(binding.rows, section.rows, (group: TodoWorkspaceGroup) => group.key,
          this.model, this.model, scope, scope);
      } else {
        this.sectionBindingSequence++;
        binding = new BoundTodoSection(identity, 'todo-section-binding.' + String(this.sectionBindingSequence), section);
        binding.rows = reconcileBoundRows([], section.rows, (group: TodoWorkspaceGroup) => group.key,
          undefined, this.model, '', scope);
      }
      next.push(binding);
    });
    this.sectionBindings = next;
    const current = this.workspace.selected;
    this.lineageRowBindings = reconcileBoundRows(this.lineageRowBindings, current.id ? this.workspace.lineageResources(current) : [],
      (resource: ResourceRow) => resource.key(), this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.relatedTodoBindings = reconcileBoundRows(this.relatedTodoBindings, current.id ? this.workspace.relatedTodos(current) : [],
      (todo: TodoWorkspaceItem) => JSON.stringify([todo.manager, todo.id]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.spaceBindings = reconcileBoundRows(this.spaceBindings, current.id ? current.spaces : [],
      (space: TodoWorkspaceSpace) => JSON.stringify([space.machineId, space.space]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.activeAgentBindings = reconcileBoundRows(this.activeAgentBindings, current.id ? current.activeAgents() : [],
      (agent: TodoWorkspaceAgent) => JSON.stringify([agent.machineId, agent.sessionId]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.previousAgentBindings = reconcileBoundRows(this.previousAgentBindings, current.id ? current.previousAgents() : [],
      (agent: TodoWorkspaceAgent) => JSON.stringify([agent.machineId, agent.sessionId]),
      this.rowBindingOwner, this.model, this.rowBindingScope, scope);
    this.rowBindingOwner = this.model;
    this.rowBindingScope = scope;
  }

  private statusOptions(): SelectOption[] {
    const options: SelectOption[] = [{ value: tr('all') }];
    ['needs_you', 'checking', 'working', 'followup', 'not_started', 'done'].forEach((state: string) => options.push({ value: tr('todo_state_' + state) }));
    return options;
  }
  private statusIndex(): number {
    const states: string[] = ['all', 'needs_you', 'checking', 'working', 'followup', 'not_started', 'done'];
    const index = states.indexOf(this.workspace.statusFilter);
    return index < 0 ? 0 : index;
  }
  private hubMode(): boolean { return this.workspace.effectiveGroup() === 'hub'; }
  private groupKeys(): string[] {
    return this.workspace.hubCounts ? ['hub', 'status', 'project', 'thread'] : ['status', 'project', 'thread'];
  }
  private groupOptions(): SelectOption[] {
    return this.groupKeys().map((key: string): SelectOption => ({ value: tr(key === 'hub' ? 'flow_todo_hub' : 'todo_group_' + key) }));
  }
  private groupIndex(): number { return this.groupKeys().indexOf(this.workspace.effectiveGroup()); }
  private sessionKey(row: ResourceRow): string { return JSON.stringify([row.manager, row.machine, row.session || row.id]); }
  private choiceKey(choice: TodoWorkspaceSpaceChoice): string { return JSON.stringify([choice.machineId, choice.space]); }
  private selectedSession(): ResourceRow | undefined {
    return this.workspace.candidateSessions().find((row: ResourceRow) => this.sessionKey(row) === this.workspace.selectedAgentKey);
  }
  private selectedSpace(): TodoWorkspaceSpaceChoice | undefined {
    return this.workspace.spaceChoices().find((choice: TodoWorkspaceSpaceChoice) => this.choiceKey(choice) === this.workspace.selectedSpaceKey);
  }
  private selectSession(index: number): void {
    const row = this.workspace.candidateSessions()[index];
    if (row) this.workspace.selectedAgentKey = this.sessionKey(row);
  }
  private selectSpace(index: number): void {
    const choice = this.workspace.spaceChoices()[index];
    if (choice) this.workspace.selectedSpaceKey = this.choiceKey(choice);
  }
  private sessionOptions(): SelectOption[] {
    return this.workspace.candidateSessions().map((row: ResourceRow): SelectOption => ({ value: (row.title || row.id) + ' · ' + this.workspace.machineName(row.machine) }));
  }
  private spaceOptions(): SelectOption[] {
    return this.workspace.spaceChoices().map((choice: TodoWorkspaceSpaceChoice): SelectOption => ({ value: choice.label }));
  }
  private targetCurrent(todo: TodoWorkspaceItem, generation: number, scope: string): boolean {
    return this.model.owns(generation, scope) && this.workspace.isSelected(todo);
  }
  private todoSubtitle(group: TodoWorkspaceGroup, includeState: boolean = false): string {
    const relatedCount = group.related().length;
    const state = tr('todo_state_' + group.state);
    const visibleState = includeState || this.workspace.groupBy !== 'status' ? state : '';
    const related = relatedCount > 0 ? tr('todo_related') + ' ' + String(relatedCount) : '';
    return [visibleState, related].filter((value: string) => !!value).join(' · ');
  }
  private sectionTitle(section: TodoWorkspaceSection): string {
    if (this.workspace.effectiveGroup() === 'status' || this.hubMode() ||
      ['todo_unsorted', 'todo_no_thread', 'phone_todo_section_done'].includes(section.title))
      return tr(section.title);
    return section.title;
  }

  private todoWaitLabels(): TodoWaitLabels {
    return {
      kindPersonReply: tr('todo_wait_kind_person_reply'),
      kindMergeReview: tr('todo_wait_kind_merge_review'),
      kindPrMerge: tr('todo_wait_kind_pr_merge'),
      kindExternalEvent: tr('todo_wait_kind_external_event'),
      kindDate: tr('todo_wait_kind_date'),
      kindTodoHandoff: tr('todo_wait_kind_todo_handoff'),
      kindAgent: tr('todo_wait_kind_agent'),
      kindUsageLimit: tr('todo_wait_kind_usage_limit'),
      kindComputerOffline: tr('todo_wait_kind_computer_offline'),
      kindScheduledReport: tr('todo_wait_kind_scheduled_report'),
      kindParked: tr('todo_wait_kind_parked'),
      kindUnknown: tr('waiting'),
      blockerReviewRequired: tr('todo_wait_blocker_review_required'),
      blockerChangesRequested: tr('todo_wait_blocker_changes_requested'),
      blockerChecksPending: tr('todo_wait_blocker_checks_pending'),
      blockerChecksFailing: tr('todo_wait_blocker_checks_failing'),
      blockerConflicts: tr('todo_wait_blocker_conflicts'),
      usageLimitTarget: tr('todo_wait_usage_limit_target'),
      waitingToMergeTarget: tr('todo_wait_waiting_to_merge_target'),
      waitingOnTarget: tr('todo_wait_waiting_on_target'),
      backAt: tr('todo_wait_back_at'),
      wakesAt: tr('todo_wait_wakes_at'),
      checkInDue: tr('todo_wait_check_in_due'),
      untilAt: tr('todo_wait_until_at'),
      sinceAt: tr('todo_wait_since_at')
    };
  }

  private waitInput(todo: TodoWorkspaceItem): TodoWaitInput | undefined {
    const w = todo.wait;
    if (!w) return undefined;
    return {
      kind: w.kind,
      waiting_on: w.waiting_on,
      reason: w.reason,
      wake_condition: w.wake_condition,
      wake_at: w.wake_at,
      until: w.until,
      since: w.since,
      url: w.url,
      handoff_todo_id: w.handoff_todo_id,
      contact: w.contact,
      pr_blockers: w.pr_blockers,
      step: w.step,
      unknownKind: todo.waitUnknownKind || undefined
    };
  }

  private waitRowSummary(todo: TodoWorkspaceItem): TodoWaitRowSummary | undefined {
    const input = this.waitInput(todo);
    return input ? rowSummary(input, this.clock, this.todoWaitLabels()) : undefined;
  }

  private waitDetailModel(todo: TodoWorkspaceItem): TodoWaitDetailModel | undefined {
    const input = this.waitInput(todo);
    return input ? detailLines(input, this.clock, this.todoWaitLabels()) : undefined;
  }

  @Builder todoWaitLine(todo: TodoWorkspaceItem) {
    if (this.waitRowSummary(todo)) {
      Row({ space: Tokens.spaceXs }) {
        SymbolGlyph($r('sys.symbol.clock'))
          .fontSize(Tokens.interfaceFontVp(12, nativeEnvironment.fontScale))
          .fontColor([this.waitRowSummary(todo)!.isDue ? Colors.working : Colors.secondary])
          .accessibilityLevel('no')
        Column({ space: Tokens.spaceXxs }) {
          Text(this.waitRowSummary(todo)!.lead)
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontColor(Colors.ink)
            .maxLines(2)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
          if (this.waitRowSummary(todo)!.timing) {
            Text(this.waitRowSummary(todo)!.timing!)
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(this.waitRowSummary(todo)!.isDue ? Colors.working : Colors.secondary)
              .maxLines(2)
              .textOverflow({ overflow: TextOverflow.Ellipsis })
          }
        }
        .alignItems(HorizontalAlign.Start)
        .layoutWeight(1)
      }
      .width('100%')
      .alignItems(VerticalAlign.Top)
      .id('todo.wait.line.' + todo.id)
    }
  }

  @Builder waitFactRow(label: string, value: string, overdue: boolean = false) {
    Column({ space: Tokens.spaceXxs }) {
      Text(label)
        .fontSize(Tokens.typeLabel)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
        .fontColor(Colors.secondary)
      Text(value)
        .fontSize(Tokens.typeSecondary)
        .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
        .fontColor(overdue ? Colors.working : Colors.ink)
        .copyOption(CopyOptions.LocalDevice)
    }
    .width('100%')
    .alignItems(HorizontalAlign.Start)
  }

  @Builder todoWaitSection(todo: TodoWorkspaceItem) {
    if (this.waitDetailModel(todo)) {
      Column({ space: Tokens.spaceS }) {
        Row({ space: Tokens.spaceS }) {
          Row({ space: Tokens.spaceXs }) {
            SymbolGlyph($r('sys.symbol.clock'))
              .fontSize(Tokens.interfaceFontVp(13, nativeEnvironment.fontScale))
              .fontColor([Colors.secondary])
              .accessibilityLevel('no')
            Text(tr('todo_wait_heading'))
              .fontSize(Tokens.typeCaption)
              .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontWeight(FontWeight.Medium)
              .fontColor(Colors.secondary)
          }
          .layoutWeight(1)
          PersonaStatusPill({
            tone: this.waitDetailModel(todo)!.untilIsDue ? 'working' : 'neutral',
            label: this.waitDetailModel(todo)!.kindTitle
          })
        }
        .width('100%')
        .alignItems(VerticalAlign.Center)

        if (this.waitDetailModel(todo)!.step) {
          Text(tr('todo_wait_from_step').replace('{step}', this.waitDetailModel(todo)!.step!))
            .fontSize(Tokens.typeCaption)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
            .fontColor(Colors.secondary)
        }

        if (this.waitDetailModel(todo)!.waitingOn) {
          if (this.waitDetailModel(todo)!.handoffTodoID) {
            Column({ space: Tokens.spaceXxs }) {
              Text(tr('todo_wait_waiting_on'))
                .fontSize(Tokens.typeLabel)
                .fontColor(Colors.secondary)
              Button(this.waitDetailModel(todo)!.waitingOn!, { type: ButtonType.Normal })
                .buttonStyle(ButtonStyleMode.TEXTUAL)
                .fontSize(Tokens.typeSecondary)
                .fontColor(Colors.link)
                .constraintSize({ minWidth: Tokens.controlBar, minHeight: Tokens.controlBar })
                .padding(0)
                .onClick(() => {
                  const target = new ResourceRow(this.waitDetailModel(todo)!.handoffTodoID!, this.waitDetailModel(todo)!.waitingOn!, 'todo');
                  target.manager = todo.manager;
                  this.onOpenResource(target);
                })
                .id('todo.wait.handoff.' + this.waitDetailModel(todo)!.handoffTodoID!)
            }
            .width('100%')
            .alignItems(HorizontalAlign.Start)
          } else {
            this.waitFactRow(tr('todo_wait_waiting_on'), this.waitDetailModel(todo)!.waitingOn!)
          }
        }

        if (this.waitDetailModel(todo)!.blockers.length > 0) {
          Column({ space: Tokens.spaceXs }) {
            Text(tr('todo_wait_blocked_by'))
              .fontSize(Tokens.typeLabel)
              .fontColor(Colors.secondary)
            Flex({ wrap: FlexWrap.Wrap, space: { main: LengthMetrics.vp(Tokens.spaceSm), cross: LengthMetrics.vp(Tokens.spaceSm) } }) {
              ForEach(this.waitDetailModel(todo)!.blockers, (blocker: string) => {
                PersonaStatusPill({ tone: 'working', label: blocker })
              }, (blocker: string) => blocker)
            }
            .width('100%')
          }
          .width('100%')
          .alignItems(HorizontalAlign.Start)
        }

        if (this.waitDetailModel(todo)!.reason) {
          this.waitFactRow(tr('todo_wait_details'), this.waitDetailModel(todo)!.reason!)
        }

        if (this.waitDetailModel(todo)!.wakeCondition) {
          this.waitFactRow(tr('todo_wait_wakes_when'), this.waitDetailModel(todo)!.wakeCondition!)
        }

        if (this.waitDetailModel(todo)!.wakeAtText) {
          this.waitFactRow(tr('todo_wait_scheduled_wake'), this.waitDetailModel(todo)!.wakeAtText!)
        }

        if (this.waitDetailModel(todo)!.untilText) {
          this.waitFactRow(
            tr(this.waitDetailModel(todo)!.untilIsDue ? 'todo_wait_check_in_due_label' : 'todo_wait_until_label'),
            this.waitDetailModel(todo)!.untilText!,
            this.waitDetailModel(todo)!.untilIsDue
          )
        }

        if (this.waitDetailModel(todo)!.contact) {
          Column({ space: Tokens.spaceXs }) {
            Text(
              [
                tr('todo_wait_sent_via').replace('{channel}', this.waitDetailModel(todo)!.contact!.channel),
                this.waitDetailModel(todo)!.contact!.sender || '',
                this.waitDetailModel(todo)!.contact!.sentAtText || ''
              ].filter((part: string) => !!part).join(' · ')
            )
              .fontSize(Tokens.typeLabel)
              .fontColor(Colors.secondary)
            if (this.waitDetailModel(todo)!.contact!.request) {
              Text(this.waitDetailModel(todo)!.contact!.request!)
                .fontSize(Tokens.typeCaption)
                .fontColor(Colors.ink)
                .copyOption(CopyOptions.LocalDevice)
            }
            if (this.waitDetailModel(todo)!.contact!.url) {
              Button(tr('todo_wait_open_message'), { type: ButtonType.Capsule })
                .height(32)
                .constraintSize({ minWidth: Tokens.controlBar })
                .responseRegion({ x: 0, y: -6, width: '100%', height: Tokens.controlBar })
                .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
                .backgroundColor(Colors.surface)
                .fontColor(Colors.link)
                .fontSize(Tokens.typeLabel)
                .onClick(() => this.onLink(this.waitDetailModel(todo)!.contact!.url!))
                .id('todo.wait.contact')
            }
          }
          .width('100%')
          .alignItems(HorizontalAlign.Start)
          .padding(Tokens.spaceS)
          .backgroundColor(Colors.control)
          .borderRadius(Tokens.radiusControl)
        }

        if (this.waitDetailModel(todo)!.sourceURL) {
          Button(tr(this.waitDetailModel(todo)!.isPR ? 'todo_wait_view_pr' : 'todo_wait_open_source'), { type: ButtonType.Capsule })
            .height(32)
            .constraintSize({ minWidth: Tokens.controlBar })
            .responseRegion({ x: 0, y: -6, width: '100%', height: Tokens.controlBar })
            .padding({ left: Tokens.spaceM, right: Tokens.spaceM })
            .backgroundColor(Colors.control)
            .fontColor(Colors.link)
            .fontSize(Tokens.typeCaption)
            .onClick(() => this.onLink(this.waitDetailModel(todo)!.sourceURL!))
            .id('todo.wait.source')
        }

        if (this.waitDetailModel(todo)!.sinceText) {
          Text(tr('todo_wait_waiting_since').replace('{time}', this.waitDetailModel(todo)!.sinceText!))
            .fontSize(Tokens.typeLabel)
            .fontColor(Colors.secondary)
        }
      }
      .alignItems(HorizontalAlign.Start)
      .width('100%')
      .padding(Tokens.spaceL)
      .backgroundColor(Colors.surface)
      .borderRadius(PhoneStyle.cardRadius)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      .id('todo.wait.section')
    }
  }
  private completionLabel(): string {
    return tr(this.workspace.selected.isDone() && !this.workspace.selected.startError ? 'reopen' : 'todo_complete');
  }
  private detailEmptyKey(): string {
    return this.workspace.loading ? 'loading' : this.workspace.error || 'todo_gone';
  }
  private changeCompletion(): void {
    const todo = this.workspace.selected;
    if (!this.workspace.isSelected(todo)) return;
    if (todo.isDone() && !todo.startError) { Haptics.tap(); this.workspace.setDone(todo, false).catch(() => undefined); }
    else this.confirmDone(todo);
  }
  private openProject(): void {
    const resource = this.workspace.projectResource(this.workspace.selected);
    if (resource) this.onOpenResource(resource);
  }
  private openThread(): void {
    const resource = this.workspace.threadResource(this.workspace.selected);
    if (resource) this.onOpenResource(resource);
  }
  private openSource(): void {
    const resource = this.workspace.sourceResource(this.workspace.selected);
    if (resource) this.onOpenResource(resource);
  }

  private confirmDone(todo: TodoWorkspaceItem): void {
    const agents = todo.activeAgents().map((agent: TodoWorkspaceAgent) => (agent.title || tr('agent')) + ' · ' + this.workspace.machineName(agent.machineId));
    const message = todo.title + (agents.length ? '\n' + agents.join('\n') : '');
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), target = todo.id;
    this.getUIContext().showAlertDialog({ title: tr('todo_complete'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_complete'), action: () => {
        if (!this.model.owns(generation, scope) || !this.workspace.isSelected(todo) || this.workspace.selected.id !== target) return;
        Haptics.success();
        this.workspace.setDone(todo, true).catch(() => undefined);
      } } });
  }

  private confirmDelete(todo: TodoWorkspaceItem): void {
    const target = todo.id, generation = this.model.connectionGeneration(), scope = this.model.scope();
    const agents = todo.activeAgents().map((agent: TodoWorkspaceAgent) => (agent.title || tr('agent')) + ' · ' + this.workspace.machineName(agent.machineId));
    const message = todo.title + (agents.length ? '\n' + agents.join('\n') : '');
    this.getUIContext().showAlertDialog({ title: tr('delete'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (!this.model.owns(generation, scope) || !this.workspace.isSelected(todo) || this.workspace.selected.id !== target) return;
        this.workspace.remove(todo).then((removed: boolean) => { if (removed) this.workspace.selected = new TodoWorkspaceItem(); }).catch(() => undefined);
      } } });
  }

  private confirmLinkAgent(todo: TodoWorkspaceItem): void {
    const session = this.selectedSession();
    if (!session) return;
    const machine = this.workspace.machineName(session.machine);
    const current = Wire.object(session.data(), 'todo');
    const previous = Wire.text(current, 'title');
    const message = todo.title + '\n' + (session.title || tr('agent')) + ' · ' + machine +
      (previous ? '\n' + tr('todo_move_from') + ': ' + previous : '');
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), key = this.sessionKey(session);
    this.getUIContext().showAlertDialog({ title: tr('todo_link_agent'), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_link_agent'), action: () => {
        if (!this.targetCurrent(todo, generation, scope) || this.sessionKey(session) !== key ||
          !this.workspace.candidateSessions(todo).some((row: ResourceRow) => this.sessionKey(row) === key)) return;
        this.workspace.linkAgent(todo, session).catch(() => undefined);
      } } });
  }

  private confirmUnlinkAgent(todo: TodoWorkspaceItem, agent: TodoWorkspaceAgent): void {
    const target = todo.id, identity = JSON.stringify([agent.machineId, agent.sessionId]);
    const currentAgent = todo.agents.find((item: TodoWorkspaceAgent) => JSON.stringify([item.machineId, item.sessionId]) === identity);
    if (!currentAgent || !currentAgent.isActive()) return;
    const machine = this.workspace.machineName(currentAgent.machineId);
    const generation = this.model.connectionGeneration(), scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('todo_unlink_agent'),
      message: todo.title + '\n' + (currentAgent.title || tr('agent')) + ' · ' + machine,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_unlink_agent'), action: () => {
        const selected = this.workspace.selected;
        const latest = selected.agents.find((item: TodoWorkspaceAgent) => JSON.stringify([item.machineId, item.sessionId]) === identity);
        if (this.targetCurrent(todo, generation, scope) && selected.id === target && latest?.isActive())
          this.workspace.unlinkAgent(selected, latest).catch(() => undefined);
      } } });
  }

  private confirmLinkSpace(todo: TodoWorkspaceItem): void {
    const choice = this.selectedSpace();
    if (!choice) return;
    const target = todo.id, key = this.choiceKey(choice), generation = this.model.connectionGeneration(), scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('todo_link_project'), message: todo.title + '\n' + choice.label,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_link_project'), action: () => {
        if (!this.targetCurrent(todo, generation, scope) || this.workspace.selected.id !== target || this.choiceKey(choice) !== key ||
          !this.workspace.spaceChoices(todo).some((item: TodoWorkspaceSpaceChoice) => this.choiceKey(item) === key)) return;
        this.workspace.linkSpace(todo, choice).catch(() => undefined);
      } } });
  }

  private confirmUnlinkSpace(todo: TodoWorkspaceItem, space: TodoWorkspaceSpace): void {
    const identity = JSON.stringify([space.machineId, space.space]);
    const currentSpace = todo.spaces.find((item: TodoWorkspaceSpace) => JSON.stringify([item.machineId, item.space]) === identity);
    if (!currentSpace) return;
    const target = todo.id, generation = this.model.connectionGeneration(), scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('todo_remove_project'), message: todo.title + '\n' +
      currentSpace.space + ' · ' + this.workspace.machineName(currentSpace.machineId),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('todo_remove_project'), action: () => {
        const selected = this.workspace.selected;
        const latest = selected.spaces.find((item: TodoWorkspaceSpace) => JSON.stringify([item.machineId, item.space]) === identity);
        if (this.targetCurrent(todo, generation, scope) && selected.id === target && latest)
          this.workspace.unlinkSpace(selected, latest).catch(() => undefined);
      } } });
  }

  @Builder advancedMenu() {
    Menu() {
      MenuItem({ content: tr('new_agent') }).enabled(!this.workspace.saving && !this.workspace.targetGone)
        .onClick(() => this.onCreateAgent(this.workspace.todoResource(this.workspace.selected)))
    }
  }
  @Builder todoMenu(todo: TodoWorkspaceItem) {
    Menu() {
      MenuItem({ content: tr('delete') }).enabled(!this.workspace.targetGone).onClick(() => this.confirmDelete(todo))
    }
  }

  private dueTitle(todo:TodoWorkspaceItem):string {
    if (!Number.isFinite(todo.due)) return '';
    const day = phoneDueDate(todo.due, this.clock, this.dateLocale(), !!todo.dueAt && Number.isFinite(Date.parse(todo.dueAt)));
    return (!todo.isDone() && todo.dueBy < this.clock ? tr('flow_todo_overdue') + ' · ' : '') + day;
  }
  private todoColumns(): number {
    return workspaceGridColumns(this.listWidth, nativeEnvironment.fontScale, 2);
  }
  private todoSidePadding(): number {
    return workspaceHorizontalPadding(this.listWidth);
  }
  private setRowDone(todo:TodoWorkspaceItem):void {
    const completing = !todo.isDone() || !!todo.startError;
    if (completing) Haptics.success(); else Haptics.tap();
    Motion.run(this.getUIContext(), Motion.layout(nativeEnvironment.reduceMotion), () => {
      this.workspace.setDone(todo,completing,true).catch(() => undefined);
    });
  }
  private dueFilterLabel(key:string):string { return tr(key === 'all' ? 'all' : 'flow_todo_'+key); }
  private toggleGroup(key:string):void {
    this.expandedGroups=this.expandedGroups.includes(key) ? this.expandedGroups.filter((k:string) => k !== key) : this.expandedGroups.concat([key]);
  }
  private completionShown(todo:TodoWorkspaceItem):boolean { return todo.isDone() && !todo.startError; }
  private rowClosed(todo: TodoWorkspaceItem): boolean {
    return this.hubMode() && todo.hubView ? todo.hubView.phase === 'done' : this.completionShown(todo);
  }
  private rowLine(group: TodoWorkspaceGroup): string {
    if (!this.hubMode()) {
      const context = group.phone;
      if (context.summaryKind === 'failure') return tr('phone_todo_start_failure').replace('{reason}', context.summary);
      if (context.summaryKind === 'empty') return tr('phone_todo_not_handed');
      return context.summaryKind === 'session' ? markdownVisibleText(parseMarkdown(context.summary)) : context.summary;
    }
    const line = group.lead.hubView?.line || '';
    if (line) return line;
    return group.state === 'todo' || group.state === 'someday' ? group.lead.notes : tr(todoHubLabel(group.state));
  }
  private rowStatus(group: TodoWorkspaceGroup): string {
    if (group.lead.startError) return tr('phone_todo_did_not_start');
    return tr(['needs_you', 'working'].includes(group.state) ? group.state : 'todo_state_' + group.state);
  }
  private leadMetadata(group: TodoWorkspaceGroup): string {
    const context = group.phone, lead = context.lead;
    if (!lead) return context.activeAgents ? '· ' + tr('project_agent_count').replace('{count}', String(context.activeAgents)) : '';
    const data = lead.data(), model = Wire.text(data, 'model').trim().replace(/\[[^\]]*\]$/, '');
    const bare = model.split('/').pop() || model;
    const effort = Wire.text(data, 'effort');
    const effortName = effort === 'xhigh' ? tr('phone_session_extra_high') :
      ['auto', 'low', 'medium', 'high', 'max'].includes(effort) ? tr(effort) : effort;
    return '· ' + [this.workspace.machineName(lead.machine, group.lead.manager),
      launchModelName(bare, composerModelName(bare)), effortName].filter((value: string) => !!value).join(' · ');
  }
  private rowLineColor(group: TodoWorkspaceGroup): ResourceColor {
    if (this.hubMode()) return group.state === 'needs_you' ? Colors.link : Colors.secondary;
    if (group.phone.summaryKind === 'failure') return Colors.danger;
    return group.phone.summaryKind === 'session' && group.phone.urgentState === 'needs_you' ? Colors.needs : Colors.secondary;
  }
  @Builder rowMenu(todo:TodoWorkspaceItem) {
    Menu() {
      if (this.hubMode()) MenuItem({ content: tr('open') }).onClick(() => this.onOpenResource(this.workspace.todoResource(todo)))
      else {
        MenuItem({content:tr(this.completionShown(todo) ? 'reopen' : 'todo_complete')}).enabled(this.model.connected && !this.workspace.saving)
          .onClick(() => this.setRowDone(todo))
        MenuItem({content:tr('edit')}).onClick(() => this.workspace.beginEdit(todo))
      }
    }
  }
  @Builder metadata(todo:TodoWorkspaceItem) {
    if (Number.isFinite(todo.due) || !!todo.priority) Row({space:6}) {
      if (Number.isFinite(todo.due)) {
        SymbolGlyph(Symbols.calendar).fontSize('12vp').fontColor([!todo.isDone() && todo.dueBy < this.clock ? Colors.danger : Colors.secondary])
        Text(this.dueTitle(todo)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontColor(!todo.isDone() && todo.dueBy < this.clock ? Colors.danger : Colors.secondary)
      }
      if (todo.priority) Text(tr(['low','medium','high'].includes(todo.priority) ? todo.priority : 'flow_todo_priority') +
        (!['low','medium','high'].includes(todo.priority) ? ' · ' + todo.priority : '')).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
        .fontColor(todo.priority === 'high' ? Colors.needs : Colors.secondary).padding({left:5,right:5,top:1,bottom:1}).backgroundColor(Colors.control).borderRadius(Tokens.radiusBadge)
    }.width('100%')
  }
  private readListClockStyle():void {
    if (!this.phoneList) return;
    const context=this.getUIContext().getHostContext();
    if (!context) return;
    try {
      const preference=settings.getValueSync(context,settings.date.TIME_FORMAT,'default');
      this.listClockStyle=preference==='12' || preference==='24' ? preference : 'locale';
    } catch (_) { this.listClockStyle='locale'; }
  }
  private since(todo:TodoWorkspaceItem):string {
    return relativeActivityDate(Date.parse(todo.updatedAt || todo.createdAt), this.clock, this.dateLocale(),
      tr('agent_time_just_now'), tr('kb_minutes_ago'), tr('kb_yesterday'), this.phoneList && !this.hubMode() ? this.listClockStyle : '');
  }
  private phoneMeasure(regular:number, largest:number):number {
    return PhoneStyle.formSize(regular,largest,nativeEnvironment.fontScale);
  }
  private phonePlanDue(todo:TodoWorkspaceItem):string {
    const date=phoneTodoPlanDate(todo.due,this.clock,this.dateLocale(),Number.isFinite(Date.parse(todo.dueAt)));
    return tr(!todo.isDone() && todo.dueBy<this.clock ? 'phone_todo_was_due' : 'phone_todo_due_on').replace('{date}',date);
  }
  private activeReminder(todo:TodoWorkspaceItem):number {
    const record=todo.data();
    return !todo.isDone() && !Wire.text(record,'reminded_at') ? Date.parse(Wire.text(record,'remind_at')) : Number.NaN;
  }
  @Builder phonePlan(binding:BoundRow<TodoWorkspaceGroup>) {
    if (Number.isFinite(binding.value.lead.due) || (!binding.value.lead.isDone() && binding.value.lead.priority==='high') || Number.isFinite(this.activeReminder(binding.value.lead))) {
      Row({space:6}) {
        if (!binding.value.lead.isDone() && binding.value.lead.priority==='high') SymbolGlyph($r('sys.symbol.flag_fill'))
          .fontSize(Tokens.interfaceFontVp(13,nativeEnvironment.fontScale)).fontColor([Colors.needs]).accessibilityText(tr('phone_todo_high_priority'))
        if (Number.isFinite(binding.value.lead.due)) Row({space:4}) {
          SymbolGlyph(Symbols.calendar).fontSize(Tokens.interfaceFontVp(13,nativeEnvironment.fontScale)).width(this.phoneMeasure(16,23.3))
            .fontColor([!binding.value.lead.isDone() && binding.value.lead.dueBy<this.clock ? Colors.danger : Colors.secondary]).accessibilityLevel('no')
          Text(this.phonePlanDue(binding.value.lead)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).lineHeight(this.phoneMeasure(15.7,23.4)+'vp')
            .letterSpacing(this.phoneMeasure(0,-0.29)+'vp')
            .fontColor(!binding.value.lead.isDone() && binding.value.lead.dueBy<this.clock ? Colors.danger : Colors.secondary).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
        }.alignItems(VerticalAlign.Top).layoutWeight(1)
        if (Number.isFinite(this.activeReminder(binding.value.lead))) SymbolGlyph($r('sys.symbol.bell')).fontSize(Tokens.interfaceFontVp(13,nativeEnvironment.fontScale))
          .fontColor([Colors.secondary]).accessibilityText(tr('phone_todo_remind_at').replace('{date}',phoneTodoPlanDate(this.activeReminder(binding.value.lead),this.clock,this.dateLocale(),true)))
      }.width('100%').alignItems(VerticalAlign.Top)
    }
  }
  private phoneStateInk(state:string):ResourceColor {
    if (state==='needs_you') return Colors.needs;
    if (state==='working' || state==='waiting') return Colors.working;
    return state==='unread' ? Colors.unread : $r('app.color.project_card_secondary');
  }
  private phoneDotInk(state:string):ResourceColor {
    if (state==='needs_you') return Colors.needsMark;
    if (state==='working') return Colors.workingMark;
    return state==='unread' ? Colors.unreadMark : $r('app.color.project_card_secondary');
  }
  private phoneReviewInk(tone:string):ResourceColor {
    if (tone==='caution') return $r('app.color.persona_dock_warning');
    if (tone==='danger') return Colors.danger;
    return tone==='positive' ? Colors.positive : $r('app.color.project_card_secondary');
  }
  private phoneReviewCaption(group:TodoWorkspaceGroup):string {
    const review=group.phone, label=tr(review.reviewKey).replace('{round}',String(review.reviewRound));
    return review.reviewCount ? label+' · '+review.reviewCount : label;
  }
  private openLeadAt(binding:BoundRow<TodoWorkspaceGroup>, event:ClickEvent):void {
    if (this.phoneList && !this.hubMode() && binding.value.members.length>1) {
      const disclosure=this.windowFrame('todos.related.'+binding.value.lead.id);
      if (disclosure && event.windowX>=disclosure.left && event.windowX<disclosure.left+disclosure.width &&
        event.windowY>=disclosure.top && event.windowY<disclosure.top+disclosure.height) return;
    }
    this.openTodoAt(binding.value.lead,binding.value.phone.canFileSeen,event);
  }
  private openTodoAt(todo:TodoWorkspaceItem,hasSeen:boolean,event:ClickEvent):void {
    if (hasSeen && this.phoneList && !this.hubMode() && this.workspace.supportsSeen()) {
      const chip=this.windowFrame('todos.seen.'+todo.id);
      // The status line clips a child's hit test at its own height. Its surrounding row receives the vertical outset.
      if (chip && event.windowX>=chip.left && event.windowX<=chip.left+chip.width &&
        event.windowY>=chip.top-16 && event.windowY<=chip.top+chip.height+16) {
        this.workspace.markSeen(todo);
        return;
      }
    }
    this.onOpenResource(this.workspace.todoResource(todo));
  }
  @Builder phoneStatusWords(binding:BoundRow<TodoWorkspaceGroup>) {
    Flex({ wrap: FlexWrap.Wrap, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(6), cross: LengthMetrics.vp(4) } }) {
        if (binding.value.phone.urgentState==='working') {
          WorkingArcSpinner({diameter:12,tint:Colors.workingMark,surface:Color.Transparent,showCoreDot:true})
            .accessibilityText(tr('agent_task_state_working'))
        } else if (binding.value.phone.urgentState==='needs_you') {
          NeedsYouPulseDot({dotSize:7,tint:Colors.needsMark,surface:Colors.surface,bordered:false})
            .accessibilityText(tr('agent_task_state_needs_you'))
        } else {
          Stack() {
            Circle().width(7).height(7).fill(this.phoneDotInk(binding.value.phone.urgentState))
              .opacity(binding.value.phone.urgentState==='offline' ? 0.3 : ['needs_you','working','unread'].includes(binding.value.phone.urgentState) ? 1 : 0.55)
          }.width(7).height(7).clip(false)
            .accessibilityText(tr('agent_task_state_'+(binding.value.phone.urgentState || (binding.value.phone.taskState==='done' ? 'idle' : 'unknown'))))
        }
        Text(tr(binding.value.phone.statusKey)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontWeight(600).maxLines(1)
          .lineHeight(this.phoneMeasure(14.3,21.7)+'vp').fontColor(this.phoneStateInk(binding.value.phone.shownState)).id('todos.status.'+binding.value.lead.id)
        if (binding.value.phone.showAgentMetadata && binding.value.phone.activeAgents) Text(this.leadMetadata(binding.value)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .lineHeight(this.phoneMeasure(15.7,23.4)+'vp').fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1)
        if (binding.value.phone.showAgentMetadata && binding.value.phone.lead && binding.value.phone.activeAgents>1) Text('+'+String(binding.value.phone.activeAgents-1)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
          .lineHeight(this.phoneMeasure(15.7,23.4)+'vp').fontColor(Colors.secondary).maxLines(1)
        if (binding.value.phone.reviewKey) Text(this.phoneReviewCaption(binding.value)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
          .lineHeight(this.phoneMeasure(14.3,21.7)+'vp').maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
          .fontColor(this.phoneReviewInk(binding.value.phone.reviewTone)).padding({left:6,right:6,top:1,bottom:1})
          .backgroundColor(binding.value.phone.reviewTone==='caution' ? $r('app.color.todo_review_caution_fill') :
            binding.value.phone.reviewTone==='positive' ? $r('app.color.todo_review_positive_fill') :
              binding.value.phone.reviewTone==='danger' ? $r('app.color.todo_review_danger_fill') : $r('app.color.todo_review_neutral_fill'))
          .borderRadius(Tokens.radiusCapsule).id('todos.review.'+binding.value.lead.id)
        TodoStagePill({
          stage: binding.value.lead.stage,
          stageKind: binding.value.lead.stageKind,
          done: this.rowClosed(binding.value.lead),
          identity: 'todos.stage.' + binding.value.lead.id
        })
      }.constraintSize({maxWidth:'100%'})
  }
  @Builder seenChip(current:()=>TodoWorkspaceItem) {
    Stack() {
      Button({type:ButtonType.Normal}) {
        Row({space:4}) {
          SymbolGlyph($r('sys.symbol.checkmark_circle')).fontSize(this.phoneMeasure(13,19.5)+'vp')
            .width(this.phoneMeasure(14,21.3)).height(this.phoneMeasure(14.3,21.7)).fontColor([Colors.interfaceAccent]).accessibilityLevel('no')
          Text(tr('phone_todo_got_it')).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel))
            .lineHeight(this.phoneMeasure(14.3,21.7)+'vp').fontColor(Colors.interfaceAccent).maxLines(1)
        }
      }.height(this.phoneMeasure(14.3,21.7)).padding({left:6,right:6}).clip(false).backgroundColor(Color.Transparent)
        .responseRegion({x:0,y:-16,width:'100%',height:this.phoneMeasure(14.3,21.7)+32})
        .accessibilityText(tr(current().kind==='concern' && !current().isDone() ? 'phone_todo_seen_open_a11y' : 'phone_todo_seen_done_a11y').replace('{title}',current().title))
        .enabled(!this.workspace.saving).onClick(()=>{ Haptics.success(); this.workspace.markSeen(current()); }).id('todos.seen.'+current().id)
    }.height(this.phoneMeasure(16.3,23.7)).backgroundColor(Colors.accentWell).borderRadius(Tokens.radiusCapsule).margin({top:-1,bottom:-1})
  }
  @Builder phoneRelatedLabel(count:number, parts:string[]) {
    Text() {
      Span(parts[0])
      if (parts.length>1) {
        // The system UI face uses tabular digits. The installed Noto Sans face
        // supplies proportional advances for the count without changing the words.
        Span(String(count)).fontFamily('Noto Sans')
        Span(parts.slice(1).join('{count}'))
      }
    }.fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium)
      .fontFeature('pnum 1').letterSpacing(/[\u3400-\u9fff]/.test(parts.join('')) ? this.phoneMeasure(0,-0.2)+'vp' : 0)
      .fontColor(Colors.secondary).maxLines(1)
  }
  @Builder phoneRowWords(binding:BoundRow<TodoWorkspaceGroup>) {
    Column({space:4}) {
      Row({space:5}) {
        Text(binding.value.lead.title).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(600)
          .letterSpacing(/[\u3400-\u9fff]/.test(binding.value.lead.title) ? this.phoneMeasure(-0.35,-0.96)+'vp' : 0)
          .lineHeight(this.phoneMeasure(20.3,28.3)+'vp').fontColor(this.rowClosed(binding.value.lead) ? Colors.secondary : Colors.ink)
          .decoration({type:this.rowClosed(binding.value.lead) ? TextDecorationType.LineThrough : TextDecorationType.None})
          .layoutWeight(1).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis})
        if (this.since(binding.value.lead)) Text(this.since(binding.value.lead)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
          .letterSpacing(this.phoneMeasure(0,-0.45)+'vp').lineHeight(this.phoneMeasure(14.3,21.7)+'vp').fontColor(Colors.secondary).margin({top:5}).maxLines(1)
      }.width('100%').alignItems(VerticalAlign.Top)
      if (binding.value.phone.canFileSeen && this.workspace.supportsSeen()) {
        Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center,space:{cross:LengthMetrics.vp(4)}}) {
          this.phoneStatusWords(binding)
          Row() { this.seenChip(():TodoWorkspaceItem=>binding.value.lead) }.justifyContent(FlexAlign.End).padding({left:4}).flexGrow(1)
        }.width('100%')
      } else {
        Row() { this.phoneStatusWords(binding) }.width('100%')
      }
      this.phonePlan(binding)
      this.todoWaitLine(binding.value.lead)
      if (this.rowLine(binding.value)) Text(this.rowLine(binding.value)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
        .lineHeight(this.phoneMeasure(15.7,23.4)+'vp').fontColor(this.rowLineColor(binding.value))
        .maxLines(['notes','empty'].includes(binding.value.phone.summaryKind) ? 1 : 2).textOverflow({overflow:TextOverflow.Ellipsis})
      if (binding.value.members.length>1) Button({type:ButtonType.Normal}) {
        Row({space:4}) {
          SymbolGlyph(this.expandedGroups.includes(binding.value.key) ? Symbols.down : Symbols.expand)
            .fontSize(this.phoneMeasure(12,18)+'vp').width(this.phoneMeasure(9,13)).fontColor([Colors.secondary])
            .accessibilityLevel('no')
          this.phoneRelatedLabel(binding.value.members.length-1,tr('phone_todo_related_count').split('{count}'))
        }
      }.height(this.phoneMeasure(44,57.7)).padding(0).backgroundColor(Color.Transparent)
        .accessibilityText(tr('phone_todo_related_count').replace('{count}',String(binding.value.members.length-1)))
        .accessibilityDescription(tr(this.expandedGroups.includes(binding.value.key) ? 'control_expanded' : 'control_collapsed'))
        .onClick(() => this.toggleGroup(binding.value.key)).id('todos.related.'+binding.value.lead.id)
    }.alignItems(HorizontalAlign.Start).padding({top:4,bottom:4}).layoutWeight(1)
  }
  @Builder todoRow(binding:BoundRow<TodoWorkspaceGroup>, section:BoundTodoSection) {
    ListItem() {
      Column() {
        Row({space:this.phoneList && !this.hubMode() ? 12 : 7}) {
          if (this.hubMode()) {
            Column() {
              if (this.rowClosed(binding.value.lead)) SymbolGlyph(Symbols.check).fontSize('17vp').fontColor([Colors.positive])
              else if (binding.value.state === 'working') WorkingArcSpinner({diameter:14,tint:Colors.workingMark,surface:Color.Transparent,showCoreDot:true})
              else if (binding.value.state === 'needs_you') NeedsYouPulseDot({dotSize:9,tint:Colors.needsMark,surface:Colors.surface,bordered:false})
              else Circle().width(9).height(9).fill(binding.value.state === 'just_finished' ? Colors.positive : Colors.secondary)
            }.width(38).height(44).justifyContent(FlexAlign.Center)
          } else Button({type:ButtonType.Normal}) {
            Stack() { if (this.completionShown(binding.value.lead)) SymbolGlyph(Symbols.check).fontSize('13vp').fontColor([Color.White]) }
              .width(this.phoneList ? this.phoneMeasure(22.7,28.7) : 21).aspectRatio(1).borderRadius(Tokens.radiusCapsule)
              .border({width:1.4,color:this.completionShown(binding.value.lead) ? Colors.positive : Colors.secondary,radius:Tokens.radiusCapsule})
              .backgroundColor(this.completionShown(binding.value.lead) ? Colors.positive : Color.Transparent)
              .animation(Motion.state(nativeEnvironment.reduceMotion))
          }.width(this.phoneList ? this.phoneMeasure(44,57.7) : 44).aspectRatio(1).padding(0).backgroundColor(Color.Transparent).enabled(this.model.connected && !this.workspace.saving)
            .accessibilityText(tr(this.completionShown(binding.value.lead) ? 'reopen' : 'todo_complete') + ' · ' + binding.value.lead.title)
            .onClick(() => this.setRowDone(binding.value.lead)).id('todos.toggle.'+binding.value.lead.id)
          Row({space:this.phoneList && !this.hubMode() ? this.phoneMeasure(10,12) : 7}) {
            if (this.phoneList && !this.hubMode()) this.phoneRowWords(binding)
            else Column({space:4}) {
              Row({space:5}) {
                Text(binding.value.lead.title).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Medium).layoutWeight(1)
                  .fontColor(this.rowClosed(binding.value.lead) ? Colors.secondary : Colors.ink)
                  .decoration({type:this.rowClosed(binding.value.lead) ? TextDecorationType.LineThrough : TextDecorationType.None}).maxLines(2)
                if (Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17))<1.5 && this.since(binding.value.lead))
                  Text(this.since(binding.value.lead)).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontColor(Colors.secondary)
              }.width('100%').alignItems(VerticalAlign.Top)
              if (!this.hubMode()) Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center,space:{main:LengthMetrics.vp(4),cross:LengthMetrics.vp(2)}}) {
                if (binding.value.phone.urgentState === 'working') {
                  WorkingArcSpinner({diameter:11,tint:Colors.workingMark,surface:Color.Transparent,showCoreDot:true})
                    .accessibilityText(tr('working'))
                } else if (binding.value.phone.urgentState === 'needs_you') {
                  NeedsYouPulseDot({dotSize:6,tint:Colors.needsMark,surface:Colors.surface,bordered:false})
                    .accessibilityText(tr('needs_you'))
                } else {
                  Circle().width(6).height(6).fill(Colors.secondary)
                    .accessibilityText(tr(binding.value.phone.urgentState || (binding.value.state === 'done' ? 'idle' : 'unknown')))
                }
                Text(this.rowStatus(binding.value)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
                  .fontColor(binding.value.lead.startError ? Colors.secondary : binding.value.state==='needs_you' ? Colors.needs : Colors.secondary)
                if (binding.value.phone.activeAgents) Text(this.leadMetadata(binding.value))
                  .fontSize(Tokens.caption).maxFontScale(Tokens.interfaceScaleFor(Tokens.caption)).fontColor(Colors.secondary)
                  .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1)
                if (binding.value.phone.lead && binding.value.phone.activeAgents > 1) Text('+' + String(binding.value.phone.activeAgents-1))
                  .fontSize(Tokens.caption).maxFontScale(Tokens.interfaceScaleFor(Tokens.caption)).fontColor(Colors.secondary)
              }.width('100%')
              this.metadata(binding.value.lead)
              this.todoWaitLine(binding.value.lead)
              if (this.rowLine(binding.value)) Text(this.rowLine(binding.value)).fontSize(this.hubMode() ? 14 : Tokens.caption)
                .maxFontScale(Tokens.interfaceScaleFor(this.hubMode() ? 14 : Tokens.caption))
                .fontColor(this.rowLineColor(binding.value)).maxLines(!this.hubMode() && ['notes','empty'].includes(binding.value.phone.summaryKind) ? 1 : 2)
                .textOverflow({overflow:TextOverflow.Ellipsis})
            }.alignItems(HorizontalAlign.Start).layoutWeight(1)
            SymbolGlyph(Symbols.expand).fontSize((this.phoneList && !this.hubMode() ? this.phoneMeasure(12,16) : 12)+'vp').fontColor([Colors.secondary])
              .width(this.phoneList && !this.hubMode() ? this.phoneMeasure(12,16) : undefined)
          }.layoutWeight(1).constraintSize({minHeight:44}).accessibilityGroup(!(this.phoneList && !this.hubMode() &&
            (binding.value.members.length>1 || binding.value.phone.canFileSeen && this.workspace.supportsSeen())))
            .onClick((event:ClickEvent) => this.openLeadAt(binding,event))
            .bindContextMenu(this.rowMenu(binding.value.lead),ResponseType.LongPress).id('todos.open.'+binding.value.lead.id)
        }.width('100%').alignItems(this.phoneList && !this.hubMode() ? VerticalAlign.Top : VerticalAlign.Center)
          .padding(this.phoneList && !this.hubMode() ? {left:20,right:20,top:15,bottom:15} : {left:8,right:14,top:10,bottom:10})
        if (binding.value.members.length > 1) {
          if (!this.phoneList || this.hubMode()) {
            Divider().strokeWidth(0.5).color(Colors.divider).margin({left:54})
            Row({space:6}) {
              SymbolGlyph(this.expandedGroups.includes(binding.value.key) ? Symbols.down : Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
              Text(tr('todo_related') + ' · ' + String(binding.value.members.length-1)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.link)
            }.height(42).width('100%').padding({left:57}).accessibilityGroup(true).onClick(() => this.toggleGroup(binding.value.key))
              .id('todos.related.'+binding.value.lead.id)
          }
          if (this.expandedGroups.includes(binding.value.key)) ForEach(binding.value.related(),(todo:TodoWorkspaceItem) => {
            Row({space:8}) {
              Text(todo.title).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink).layoutWeight(1).maxLines(2)
              if (this.phoneList && !this.hubMode() && this.workspace.showsSeenOnMember(todo)) this.seenChip(():TodoWorkspaceItem=>this.workspace.items.find((item:TodoWorkspaceItem):boolean=>item.id===todo.id && item.manager===todo.manager) || todo)
              SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
            }.constraintSize({minHeight:44}).width('100%').padding({left:57,right:14})
              .accessibilityGroup(!(this.phoneList && !this.hubMode() && this.workspace.showsSeenOnMember(todo)))
              .onClick((event:ClickEvent) => this.openTodoAt(todo,this.workspace.showsSeenOnMember(todo),event)).id('todos.member.'+todo.id)
          },(todo:TodoWorkspaceItem) => JSON.stringify([todo.id,todo.title]))
        }
        if (this.todoColumns() === 1 && this.phoneList && !this.hubMode() && section.rows[section.rows.length-1]!==binding) Divider().strokeWidth(0.5).color(Colors.divider).margin({left:20})
      }.width('100%').backgroundColor(Colors.surface).borderRadius(this.todoColumns() === 1 && this.phoneList && !this.hubMode() ?
        {topLeft:section.rows[0]===binding ? 26 : 0,topRight:section.rows[0]===binding ? 26 : 0,
          bottomLeft:section.rows[section.rows.length-1]===binding ? 26 : 0,bottomRight:section.rows[section.rows.length-1]===binding ? 26 : 0} : PhoneStyle.cardRadius)
        .border(this.todoColumns() === 1 && this.phoneList && !this.hubMode() ?
          {width:{left:PhoneStyle.surfaceEdgeWidth(0.5),right:PhoneStyle.surfaceEdgeWidth(0.5),
            top:section.rows[0]===binding ? PhoneStyle.surfaceEdgeWidth(0.5) : 0,
            bottom:section.rows[section.rows.length-1]===binding ? PhoneStyle.surfaceEdgeWidth(0.5) : 0},
            color:PhoneStyle.cardBorder(nativeEnvironment.dark)} :
          {width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
    }.id('todo.'+binding.value.lead.manager+'.'+binding.value.lead.id)
  }
  private sectionOpen(section:TodoWorkspaceSection):boolean {
    if (this.hubMode()) return !this.foldable(section) || !!this.workspace.search.trim() || this.workspace.hubUnfolded.includes(section.key);
    if (!this.foldable(section) || this.workspace.search.trim()) return true;
    return section.key==='shelved' ? this.showShelved : this.showDone || this.workspace.statusFilter==='done';
  }
  private foldable(section: TodoWorkspaceSection): boolean {
    if (!this.hubMode()) return this.workspace.effectiveGroup()==='status' && (section.key==='done' || section.key==='shelved');
    return section.key === 'someday' || section.key === TODO_HUB_SECTIONS[1] || (section.key === 'done' && this.workspace.hubChoice !== 'done');
  }
  private sectionCount(binding: BoundTodoSection): number {
    if (this.hubMode() && binding.value.key === 'needs_you') return this.workspace.hubCounts?.needsYou || 0;
    return binding.rows.reduce((n: number, r: BoundRow<TodoWorkspaceGroup>) => n + r.value.members.length, 0);
  }
  private sectionTint(key:string):ResourceColor {
    if (key==='needs_you') return Colors.needs;
    if (key==='working') return Colors.working;
    if (key==='just_done' || key==='just_finished') return Colors.unread;
    return key==='pr' ? Colors.link : Colors.secondary;
  }
  @Builder sectionHeader(binding:BoundTodoSection) {
    if (!this.hubMode() || !this.workspace.hubCalm()) Row({space:6}) {
      Text(this.sectionTitle(binding.value)).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontWeight(FontWeight.Medium).fontColor(this.phoneList && !this.hubMode() ? Colors.secondary : Colors.ink)
      Text(String(this.sectionCount(binding))).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
        .fontColor(this.sectionTint(binding.value.key)).padding({left:this.phoneList && !this.hubMode() ? 5 : 0,right:this.phoneList && !this.hubMode() ? 5 : 0})
        .backgroundColor(this.phoneList && !this.hubMode() ? Colors.control : Color.Transparent).borderRadius(Tokens.radiusBadge)
      Blank().layoutWeight(1)
      if (this.foldable(binding.value)) SymbolGlyph(this.sectionOpen(binding.value) ? Symbols.down : Symbols.expand)
        .fontSize(12).fontColor([Colors.secondary])
    }.width('100%').height(this.phoneList && !this.hubMode() ? this.phoneMeasure(38.3,45.7) : 40).padding({left:this.phoneList && !this.hubMode() ? 20 : 5,right:8}).accessibilityGroup(true)
      .onClick(() => {
        if (!this.foldable(binding.value)) return;
        Haptics.tap();
        Motion.run(this.getUIContext(), Motion.layout(nativeEnvironment.reduceMotion), () => {
          if (this.hubMode()) this.workspace.toggleHubFold(binding.value.key);
          else if (binding.value.key==='shelved') this.showShelved=!this.showShelved;
          else this.showDone = !this.showDone;
        });
      })
      .id('todos.section.'+binding.value.key)
  }
  @Builder hubChoiceBar() {
    Scroll() {
      Row({ space: 5 }) {
        ForEach(this.workspace.hubCounts?.choices(this.workspace.hubChoice) || [], (key: string) => {
          Button({ type: ButtonType.Capsule }) {
            Row({ space: 5 }) {
              Text(tr(todoHubLabel(key))).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontWeight(FontWeight.Medium)
                .fontColor(this.workspace.hubChoice === key ? Colors.onAction : Colors.ink)
              if (['needs_you','in_progress','todo'].includes(key) && this.workspace.hubCounts?.choiceCount(key))
                Text(String(this.workspace.hubCounts?.choiceCount(key) || 0)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(FontWeight.Bold)
                  .fontColor(this.workspace.hubChoice === key ? Colors.onAction : key === 'needs_you' ? Colors.needs : Colors.secondary)
            }
          }.height(34).padding({ left: 10, right: 10 }).backgroundColor(this.workspace.hubChoice === key ? Colors.action : Colors.surface)
            .animation(Motion.state(nativeEnvironment.reduceMotion))
            .accessibilitySelected(this.workspace.hubChoice === key).onClick(() => {
              Haptics.selection();
              Motion.run(this.getUIContext(), Motion.layout(nativeEnvironment.reduceMotion), () => { this.workspace.setHubChoice(key); });
            }).id('todos.hub.choice.' + key)
        }, (key: string) => key + '/' + String(this.workspace.hubCounts?.choiceCount(key) || 0))
      }.padding({ left: this.todoSidePadding(), right: this.todoSidePadding() })
    }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).align(Alignment.Start).width('100%').id('todos.hub.choices')
  }
  @Builder listFilters() {
    Menu() {
      MenuItemGroup({ header: tr('todo_group_by') }) {
        ForEach(this.groupKeys(), (key: string) => {
          MenuItem({ content: tr(key === 'hub' ? 'flow_todo_hub' : 'todo_group_' + key) }).selected(this.workspace.effectiveGroup() === key).selectIcon(true)
            .onClick(() => { Haptics.selection(); this.workspace.setGroupBy(key); }).id('todos.group.' + key)
        }, (key: string) => key)
      }
      MenuItemGroup({header:tr('flow_todo_due')}) {
        ForEach(TODO_DUE_FILTERS,(key:string) => {
          MenuItem({content:this.dueFilterLabel(key)}).selected(this.workspace.dueFilter === key).selectIcon(true)
            .onClick(() => { Haptics.selection(); this.workspace.setDueFilter(key); }).id('todos.due.'+key)
        },(key:string) => key)
      }
      MenuItemGroup({header:tr('flow_todo_order')}) {
        ForEach(TODO_LIST_ORDERS,(key:string) => {
          MenuItem({content:tr(this.hubMode() && key === 'recent' ? 'flow_todo_suggested' : 'flow_todo_'+key)}).selected(this.workspace.order === key).selectIcon(true)
            .onClick(() => { Haptics.selection(); this.workspace.setOrder(key); }).id('todos.sort.'+key)
        },(key:string) => key)
      }
      if (!this.hubMode()) MenuItemGroup({ header: tr('todo_filter_status') }) {
        ForEach(this.statusOptions(), (option: SelectOption, index: number) => {
          MenuItem({ content: option.value }).selected(this.statusIndex() === index).selectIcon(true)
            .onClick(() => { Haptics.selection(); this.workspace.setStatusFilter(['all', 'needs_you', 'checking', 'working', 'followup', 'not_started', 'done'][index]); })
        }, (option: SelectOption) => JSON.stringify(option.value))
      }
    }.width(Tokens.popupWidth)
  }

  @Computed get hiddenWaitingCount(): number { return this.workspace.hiddenWaiting(this.clock,this.weekStart); }

  @Builder modePicker() {
    Row() {
      ForEach(['list','calendar'],(mode:string) => {
        Button({type:ButtonType.Capsule}) {
          Text(tr(mode==='list' ? 'phone_todo_list' : 'calendar')).fontSize(14).maxFontScale(Tokens.navigationTextMaxScale)
            .fontWeight(this.workspace.calendarOpen===(mode==='calendar') ? FontWeight.Bold : FontWeight.Normal).fontColor(Colors.ink)
        }.constraintSize({minHeight:32}).padding({left:9,right:9,top:3,bottom:3})
          .backgroundColor(this.workspace.calendarOpen===(mode==='calendar') ? Colors.surface : Color.Transparent)
          .animation(Motion.state(nativeEnvironment.reduceMotion))
          .accessibilitySelected(this.workspace.calendarOpen===(mode==='calendar')).enabled(this.workspace.calendarReady)
          .onClick(() => { Haptics.selection(); this.workspace.setCalendar(mode==='calendar'); }).id('todos.mode.'+mode)
      },(mode:string) => mode)
    }.borderRadius(Tokens.radiusCapsule).backgroundColor(Colors.control).padding(2)
  }
  private dateLocale():string {
    return this.model.language && this.model.language!=='default' ? this.model.language : i18n.getSystemLanguage();
  }
  private agendaItems(day:number):TodoWorkspaceItem[] {
    const key=todoDayKey(new Date(day));
    return this.workspace.calendarRows().filter((item:TodoWorkspaceItem) => Number.isFinite(item.due) && todoDayKey(new Date(item.due))===key)
      .sort((a:TodoWorkspaceItem,b:TodoWorkspaceItem) => Number(!!a.dueAt)-Number(!!b.dueAt) || a.due-b.due || Number(a.isDone())-Number(b.isDone()) || a.title.localeCompare(b.title));
  }
  private moveCalendar(delta:number):void {
    const date=new Date(this.workspace.calendarAnchor);
    if (this.workspace.calendarMode==='month') { date.setDate(1); date.setMonth(date.getMonth()+delta); }
    else date.setDate(date.getDate()+delta*(this.workspace.calendarMode==='week' ? 7 : 1));
    this.workspace.setCalendar(true,this.workspace.calendarMode,date.getTime());
  }
  private calendarCreate(hour:number = -1,day:number=this.workspace.calendarAnchor):void {
    if (Date.now()<this.ignoreClickUntil) return;
    const date=new Date(day);
    if (hour<0) this.workspace.startCreate(todoDayKey(date));
    else { date.setHours(hour,0,0,0); this.workspace.startCreate('',date.toISOString()); }
  }
  private windowFrame(id:string):TodoWindowFrame|undefined {
    try {
      const context=this.getUIContext(),r=context.getComponentUtils().getRectangleById(id);
      if (r.size.width<=0 || r.size.height<=0) return undefined;
      return {left:context.px2vp(r.windowOffset.x),top:context.px2vp(r.windowOffset.y),
        width:context.px2vp(r.size.width),height:context.px2vp(r.size.height)};
    } catch (_) { return undefined; }
  }
  private bounds(id:string,days:number[],hour:number):TodoCalendarBand|undefined {
    const frame=this.windowFrame(id);
    return frame ? {left:frame.left,top:frame.top,width:frame.width,height:frame.height,days:days,hour:hour} : undefined;
  }

  private measureDropBands():void {
    const mode=this.workspace.calendarMode,days=todoCalendarDays(mode,this.workspace.calendarAnchor,this.weekStart);
    const result:TodoCalendarBand[]=[];
    if (mode==='month') {
      days.forEach((day:number) => {
        const r=this.bounds('todos.calendar.day.'+todoDayKey(new Date(day)),[day],-2);
        if (r) result.push(r);
      });
    } else {
      const header=this.bounds('todos.calendar.allday.slots',days,-1);
      if (header) result.push(header);
      for (let hour=0;hour<24;hour++) {
        const r=this.bounds('todos.calendar.slots.'+String(hour),days,hour);
        if (r) result.push(r);
      }
    }
    this.bands=result;
  }

  private clearDrag():void {
    if (this.dragged) this.ignoreClickUntil=Date.now()+350;
    this.dragged=undefined; this.liftedId=''; this.landing=undefined; this.bands=[];
    this.edge=0; clearInterval(this.dragTimer); this.dragTimer=-1;
  }

  private lift(item:TodoWorkspaceItem):void {
    this.clearDrag();
    if (!this.visible || !this.workspace.canMove(item)) return;
    this.dragged=item; this.liftedId=item.id; this.measureDropBands();
  }

  private moveFinger(event:GestureEvent):void {
    if (!this.dragged || !event.fingerList.length) return;
    const finger=event.fingerList[0];
    this.dragX=finger.globalX; this.dragY=finger.globalY;
    this.updateLanding();
  }

  private updateLanding():void {
    const rect=this.bounds('todos.calendar.scroll',[],-1),x=this.dragX,y=this.dragY;
    const inside=!!rect && x>=rect.left && x<rect.left+rect.width && y>=rect.top && y<rect.top+rect.height;
    this.landing=inside ? todoCalendarDrop(this.bands,x,y) : undefined;
    this.edge=!rect || !inside ? 0 : y<rect.top+26 ? -1 : y>rect.top+rect.height-26 ? 1 : 0;
    if (!this.edge || this.dragTimer!==-1) return;
    this.dragTimer=setInterval(()=>{
      if (!this.dragged || !this.edge) { clearInterval(this.dragTimer); this.dragTimer=-1; return; }
      this.calendarScroll.scrollBy(0,7*this.edge); this.measureDropBands();
      this.landing=todoCalendarDrop(this.bands,this.dragX,this.dragY);
    },30);
  }

  private release(event:GestureEvent):void {
    if (!this.dragged) return;
    this.moveFinger(event);
    const item=this.dragged,drop=this.landing;
    this.clearDrag();
    if (drop && this.visible && this.pageAvailable) this.workspace.reschedule(item,todoDropDue(item,drop));
  }

  private selectedDrop(day:number,hour:number):boolean {
    const drop=this.landing;
    return !!drop && drop.day===day && (hour===-2 || hour===-1 && drop.minute===-1 || hour>=0 && Math.floor(drop.minute/60)===hour);
  }

  private openDay(day:number):void {
    if (Date.now()>=this.ignoreClickUntil) this.workspace.setCalendar(true,'day',day);
  }

  private moveNotice():string {
    const item=this.workspace.movedItem();
    if (!item || !Number.isFinite(item.due)) return '';
    const date=new Date(item.due);
    return tr('phone_calendar_moved').replace('{due}',item.dueAt ? date.toLocaleString(this.dateLocale()) : date.toLocaleDateString(this.dateLocale()));
  }

  @Builder calendarItem(item:TodoWorkspaceItem,condensed:boolean=false) {
    Button({type:ButtonType.Normal}) {
      Row({space:condensed ? 2 : 6}) {
        if (!condensed) Circle().width(7).height(7).fill(item.isDone() ? Colors.secondary : Colors.link)
        Text(item.title).fontSize(condensed ? 11 : 15).maxFontScale(Tokens.interfaceScaleFor(condensed ? 11 : 15))
          .fontColor(Colors.ink).layoutWeight(1).maxLines(condensed ? 1 : 2).textOverflow({overflow:TextOverflow.Ellipsis})
      }.width('100%')
    }.width('100%').constraintSize({minHeight:condensed ? 23 : 38}).padding({left:condensed ? 2 : 8,right:condensed ? 2 : 8,top:2,bottom:2})
      .backgroundColor(Colors.selection).opacity(this.liftedId===item.id ? 0.5 : 1)
      .accessibilityText(item.title).onClick(() => { if (!this.dragged && Date.now()>=this.ignoreClickUntil) this.onOpenResource(this.workspace.todoResource(item)); })
      .onTouch((event:TouchEvent)=>{
        if (event.type===TouchType.Cancel) this.clearDrag();
        else if (event.type===TouchType.Up && this.dragged) {
          const item=this.dragged;
          setTimeout(()=>{ if (this.dragged===item) this.clearDrag(); },30);
        }
      })
      .priorityGesture(GestureGroup(GestureMode.Sequence,
        LongPressGesture({repeat:false,duration:400}).onAction(()=>this.lift(item)),
        PanGesture({direction:PanDirection.All,distance:4}).onActionUpdate((ev:GestureEvent)=>this.moveFinger(ev))
          .onActionEnd((ev:GestureEvent)=>this.release(ev)).onActionCancel(()=>this.clearDrag())
      ).onCancel(()=>this.clearDrag())).id('todos.calendar.item.'+item.id)
  }

  @Computed get calendarWeekDays():number[] {
    return todoCalendarDays('week',this.workspace.calendarAnchor,this.weekStart);
  }
  @Computed get calendarPageDays():number[] {
    return todoCalendarDays(this.workspace.calendarMode,this.workspace.calendarAnchor,this.weekStart);
  }
  @Builder calendarStrip(month:boolean=false) {
    Flex({wrap:FlexWrap.Wrap}) {
      ForEach(month ? this.calendarPageDays : this.calendarWeekDays,(day:number) => {
        Column({space:2}) {
          if (!month) Text(new Date(day).toLocaleDateString(this.dateLocale(),{weekday:'short'})).fontSize(11)
            .maxFontScale(Tokens.interfaceScaleFor(11)).fontColor(Colors.secondary)
          Text(String(new Date(day).getDate())).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink)
          if (month) {
            ForEach(this.agendaItems(day).slice(0,3),(item:TodoWorkspaceItem)=>{ this.calendarItem(item,true) },(item:TodoWorkspaceItem)=>item.id+item.raw)
            if (this.agendaItems(day).length>3) Text('+'+String(this.agendaItems(day).length-3)).fontSize(11)
              .maxFontScale(Tokens.interfaceScaleFor(11)).fontColor(Colors.link)
          } else if (this.agendaItems(day).length) Circle().width(4).height(4).fill(Colors.link)
        }.width('14.28%').constraintSize({minHeight:month ? 70 : 46}).padding(2).borderRadius(Tokens.radiusBadge)
          .backgroundColor(this.selectedDrop(day,-2) || todoDayKey(new Date(day))===todoDayKey(new Date(this.workspace.calendarAnchor)) ? Colors.selection : Color.Transparent)
          .onClick(()=>this.openDay(day)).accessibilityText(new Date(day).toLocaleDateString(this.dateLocale()))
          .id('todos.calendar.day.'+todoDayKey(new Date(day)))
      },(day:number)=>String(day))
    }.width('100%').backgroundColor(Colors.surface).borderRadius(Tokens.radiusControl)
  }

  @Builder calendarTimeline() {
    ForEach(Array.from({length:25},(_:Object,i:number)=>i-1),(hour:number) => {
      Row({space:3}) {
        Text(hour<0 ? tr('phone_calendar_all_day') : String(hour).padStart(2,'0')+':00').width(43)
          .fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontColor(Colors.secondary)
        Row() {
          ForEach(this.calendarPageDays,(day:number)=>{
            Column({space:2}) {
              ForEach(this.agendaItems(day).filter((item:TodoWorkspaceItem)=>hour<0 ? !item.dueAt : !!item.dueAt && new Date(item.due).getHours()===hour),
                (item:TodoWorkspaceItem)=>{ this.calendarItem(item,this.calendarPageDays.length>1) },(item:TodoWorkspaceItem)=>item.id+item.raw)
            }.layoutWeight(1).constraintSize({minHeight:hour<0 ? 30 : 48}).backgroundColor(this.selectedDrop(day,hour) ? Colors.selection : Color.Transparent)
              .onClick(()=>this.calendarCreate(hour,day))
              .id('todos.calendar.slot.'+todoDayKey(new Date(day))+'.'+String(hour))
          },(day:number)=>String(day))
        }.layoutWeight(1).alignItems(VerticalAlign.Top).id(hour<0 ? 'todos.calendar.allday.slots' : 'todos.calendar.slots.'+String(hour))
      }.width('100%').alignItems(VerticalAlign.Top).id(hour<0 ? 'todos.calendar.allday' : 'todos.calendar.hour.'+String(hour))
      Divider().strokeWidth(0.5).color(Colors.divider)
    },(hour:number)=>String(hour))
  }
  @Builder calendarContent() {
    Column({space:7}) {
      Row({space:5}) {
        this.modePicker()
        Blank().layoutWeight(1)
        ForEach(['day','week','month'],(mode:string) => {
          Button(tr('phone_calendar_'+mode),{type:ButtonType.Capsule}).constraintSize({minHeight:32}).fontSize(13)
            .maxFontScale(Tokens.navigationTextMaxScale).fontColor(this.workspace.calendarMode===mode ? Colors.link : Colors.secondary)
            .padding({left:6,right:6}).backgroundColor(this.workspace.calendarMode===mode ? Colors.selection : Color.Transparent)
            .accessibilitySelected(this.workspace.calendarMode===mode).onClick(() => this.workspace.setCalendar(true,mode)).id('todos.calendar.mode.'+mode)
        },(mode:string) => mode)
      }.width('100%')
      Row({space:5}) {
        Button({type:ButtonType.Circle}) { SymbolGlyph(Symbols.collapse).fontSize('16vp').fontColor([Colors.link]) }
          .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('back')).onClick(() => this.moveCalendar(-1)).id('todos.calendar.prev')
        Button(new Date(this.workspace.calendarAnchor).toLocaleDateString(this.dateLocale()),{buttonStyle:ButtonStyleMode.TEXTUAL}).layoutWeight(1)
          .fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).accessibilityText(tr('flow_todo_today')).onClick(() => this.workspace.setCalendar(true,this.workspace.calendarMode,Date.now())).id('todos.calendar.today')
        Button({type:ButtonType.Circle}) { SymbolGlyph(Symbols.expand).fontSize('16vp').fontColor([Colors.link]) }
          .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('phone_calendar_'+this.workspace.calendarMode)+' +1').onClick(() => this.moveCalendar(1)).id('todos.calendar.next')
        if (!this.phoneList) Button({type:ButtonType.Circle}) { SymbolGlyph(Symbols.plus).fontSize('18vp').fontColor([Colors.link]) }
          .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('add')).onClick(() => this.calendarCreate()).id('todos.calendar.add')
      }.width('100%')
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.danger).id('todos.calendar.error')
      if (this.workspace.undoTodo) Row({space:8}) {
        Text(this.moveNotice()).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.ink).layoutWeight(1)
        Button(tr('ks_undo'),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontColor(Colors.link)
          .maxFontScale(Tokens.interfaceFontMaxScale).onClick(()=>this.workspace.undoCalendarMove()).id('todos.calendar.undo')
      }.width('100%')
      if (this.workspace.calendarMode!=='month') this.calendarStrip()
      Scroll(this.calendarScroll) { Column() {
        if (this.workspace.calendarMode==='month') this.calendarStrip(true)
        else this.calendarTimeline()
        Button(tr('flow_todo_no_due')+' '+String(this.workspace.items.filter((i:TodoWorkspaceItem)=>!Number.isFinite(i.due)).length),{buttonStyle:ButtonStyleMode.TEXTUAL}).maxFontScale(Tokens.interfaceFontMaxScale)
          .fontColor(Colors.link).onClick(() => this.workspace.setCalendar(false))
      }.width('100%').alignItems(HorizontalAlign.Start) }.layoutWeight(1).width('100%').enableScrollInteraction(!this.liftedId).id('todos.calendar.scroll')
    }.width('100%').height('100%').padding({left:16,right:16,top:7,bottom:8}).backgroundColor(Colors.grouped).id('todos.calendar')
  }
  @Builder groupMenu() {
    Menu() {
      ForEach(this.groupKeys(),(key:string) => {
        MenuItem({content:tr(key==='hub' ? 'flow_todo_hub' : 'todo_group_'+key)}).selected(this.workspace.effectiveGroup()===key).selectIcon(true)
          .onClick(() => { Haptics.selection(); this.workspace.setGroupBy(key); }).id('todos.group.'+key)
      },(key:string) => key)
    }
  }
  private filterCountLabel(key:string):string {
    const count=this.workspace.items.filter((item:TodoWorkspaceItem) => this.workspace.searchMatches(item,this.workspace.search) &&
      todoMatchesDue(item,key,this.clock,this.weekStart)).length;
    return tr('phone_todo_filter_count').replace('{count}',String(count));
  }
  @Builder phoneFilters() {
    Menu() {
      MenuItemGroup({header:tr('phone_todo_show')}) {
        ForEach(TODO_DUE_FILTERS,(key:string) => {
          MenuItem({content:this.dueFilterLabel(key),labelInfo:this.filterCountLabel(key)}).selected(this.workspace.dueFilter===key).selectIcon(true)
            .onClick(() => { Haptics.selection(); this.workspace.setDueFilter(key); }).id('todos.due.'+key)
        },(key:string) => key)
      }
      MenuItemGroup({header:tr('phone_todo_sort_by')}) {
        ForEach(TODO_LIST_ORDERS,(key:string) => {
          MenuItem({content:tr('flow_todo_'+key)}).selected(this.workspace.order===key).selectIcon(true)
            .onClick(() => { Haptics.selection(); this.workspace.setOrder(key); }).id('todos.sort.'+key)
        },(key:string) => key)
      }
      if (this.workspace.statusFilter!=='all') MenuItem({content:tr('all')+' · '+tr('todo_filter_status')})
        .onClick(()=>{ Haptics.selection(); this.workspace.setStatusFilter('all'); })
    }.width(Tokens.popupWidth)
  }
  @Builder filterMark() {
    Stack() {
      Circle().width(this.phoneMeasure(23,29)).height(this.phoneMeasure(23,29))
        .fill(this.workspace.dueFilter==='all' && this.workspace.order==='recent' ? Color.Transparent :
          Colors.systemControls ? $r('app.color.phone_filter_mark_fill') : Colors.action)
        .stroke(this.workspace.dueFilter==='all' && this.workspace.order==='recent' ? Colors.link :
          Colors.systemControls ? $r('app.color.phone_filter_mark_fill') : Colors.action).strokeWidth(this.phoneMeasure(1.8,2.3))
      Column({space:this.phoneMeasure(2.1,2.5)}) {
        ForEach([12,8,4],(length:number) => {
          Row().width(this.phoneMeasure(length,length*1.3)).height(this.phoneMeasure(1.6,2.1)).borderRadius(Tokens.radiusCapsule)
            .backgroundColor(this.workspace.dueFilter==='all' && this.workspace.order==='recent' ? Colors.link :
              Colors.systemControls ? $r('app.color.phone_filter_mark_ink') : Colors.onAction)
        },(length:number) => String(length))
      }
    }.accessibilityLevel('no')
  }
  @Builder phoneListControls() {
    Row({space:12}) {
      Row() {
        ForEach(['list','calendar'],(mode:string) => {
          Button({type:ButtonType.Capsule}) {
            Text(tr(mode==='list' ? 'phone_todo_list' : 'calendar')).fontSize(Tokens.typeCaption)
              .fontColor(mode==='list' && Colors.systemControls ? $r('app.color.phone_filter_segment_ink') : Colors.ink)
              .fontWeight(mode==='list' ? 600 : FontWeight.Normal).maxLines(1)
          }.height(27).padding({left:8,right:8}).backgroundColor(mode==='list' ?
            Colors.systemControls ? $r('app.color.phone_filter_segment_fill') : Colors.surface : Color.Transparent)
            .animation(Motion.state(nativeEnvironment.reduceMotion))
            .accessibilitySelected(mode==='list').enabled(this.workspace.calendarReady).onClick(() => { Haptics.selection(); this.workspace.setCalendar(mode==='calendar'); }).id('todos.mode.'+mode)
        },(mode:string) => mode)
      }.height(31).padding(2).borderRadius(Tokens.radiusCapsule).backgroundColor(Colors.control)
      Blank().layoutWeight(1)
      Button({type:ButtonType.Normal}) {
        Row({space:Tokens.spaceXs}) {
          if (this.workspace.effectiveGroup()==='status') SymbolGlyph($r('sys.symbol.list_checkmark'))
            .fontSize(Tokens.interfaceFontVp(Tokens.typeHeading,nativeEnvironment.fontScale)).fontColor([Colors.link]).accessibilityLevel('no')
          else SymbolGlyph(this.workspace.effectiveGroup()==='direction' ? $r('sys.symbol.pin') : Symbols.folder)
            .fontSize(Tokens.interfaceFontVp(Tokens.typeHeading,nativeEnvironment.fontScale)).fontColor([Colors.link]).accessibilityLevel('no')
          Text(this.groupOptions()[Math.max(0,this.groupIndex())].value).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeHeading))
            .fontColor(Colors.link).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        }
      }.padding({left:Tokens.spaceSm,right:Tokens.spaceSm}).constraintSize({minWidth:Tokens.controlBar,minHeight:Tokens.controlBar}).borderRadius(Tokens.radiusCapsule)
        .backgroundColor(Color.Transparent).flexShrink(1).accessibilityText(tr('todo_group_by')+' · '+this.groupOptions()[Math.max(0,this.groupIndex())].value)
        .bindMenu(this.groupMenu).id('todos.group')
      if (this.workspace.dueFilter!=='all') Button({type:ButtonType.Capsule}) {
        Row({space:4}) {
          Text('×').fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.onAction).accessibilityLevel('no')
          Text(this.dueFilterLabel(this.workspace.dueFilter)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontWeight(FontWeight.Medium)
            .fontColor(Colors.onAction).maxLines(1)
        }
      }.padding({left:9,right:9}).height(this.phoneMeasure(30,37.3)).constraintSize({minWidth:this.phoneMeasure(70,66.7)})
        .backgroundColor(Colors.action).accessibilitySelected(true).accessibilityText(tr('phone_todo_clear_filter').replace('{filter}',this.dueFilterLabel(this.workspace.dueFilter)))
        .onClick(()=>this.workspace.setDueFilter('all')).id('todos.due.clear')
      Button({type:ButtonType.Circle}) { this.filterMark() }
        .width(this.phoneMeasure(44,57.7)).aspectRatio(1).padding(0).backgroundColor(Color.Transparent)
        .accessibilityText(tr('phone_todo_filter_sort')+' · '+this.dueFilterLabel(this.workspace.dueFilter)+' · '+tr('flow_todo_'+this.workspace.order))
        .bindMenu(this.phoneFilters).id('todos.filters')
    }.width('100%').padding({left:16,right:16,top:8,bottom:4})
    Column() {
      Row({space:8}) {
        SymbolGlyph($r('sys.symbol.magnifyingglass')).fontSize(Tokens.interfaceFontVp(16,nativeEnvironment.fontScale))
          .width(this.phoneMeasure(18,25)).fontColor([Colors.secondary]).accessibilityLevel('no')
        TextInput({text:this.workspace.search,placeholder:tr('todo_search')}).fontSize(Tokens.typeBody).maxFontScale(Tokens.interfaceFontMaxScale)
          .fontColor(Colors.ink).placeholderColor(Colors.tertiary).placeholderFont({size:Tokens.interfaceFontVp(Tokens.typeBody,nativeEnvironment.fontScale)})
          .backgroundColor(Color.Transparent).padding(0).height(this.phoneMeasure(22,28)).layoutWeight(1).enterKeyType(EnterKeyType.Search)
          .onChange((value:string) => this.workspace.setSearch(value)).id('todos.search')
        if (this.workspace.search) Button({type:ButtonType.Circle}) {
          SymbolGlyph($r('sys.symbol.xmark_circle_fill')).fontSize(Tokens.interfaceFontVp(16,nativeEnvironment.fontScale)).fontColor([Colors.secondary])
        }.width(this.phoneMeasure(24,30)).aspectRatio(1).padding(0).backgroundColor(Color.Transparent).accessibilityText(tr('clear'))
          .onClick(()=>this.workspace.setSearch('')).id('todos.search.clear')
      }.width('100%').height(this.phoneMeasure(40,52.7)).padding({left:12,right:12}).backgroundColor(Colors.control).borderRadius(Tokens.radiusCapsule)
        .onClick(()=>this.getUIContext().getFocusController().requestFocus('todos.search'))
      if (this.hiddenWaitingCount) Row({space:8}) {
        SymbolGlyph($r('sys.symbol.exclamationmark_message')).fontSize(Tokens.interfaceFontVp(16,nativeEnvironment.fontScale))
          .fontColor([$r('app.color.todo_notice_mark')]).accessibilityLevel('no')
        Column({space:4}) {
          Text(tr('phone_todo_list_hidden').replace('{count}',String(this.hiddenWaitingCount)).replace('{filter}',this.dueFilterLabel(this.workspace.dueFilter)))
            .fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).lineHeight(this.phoneMeasure(18,25.3)+'vp').fontColor(Colors.secondary).width('100%')
          Button({type:ButtonType.Normal}) {
            Text(tr('phone_todo_show_all')).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).lineHeight(this.phoneMeasure(18,25.3)+'vp').fontColor(Colors.link)
          }.padding(0).backgroundColor(Color.Transparent).alignSelf(ItemAlign.Start).onClick(()=>this.workspace.setDueFilter('all')).id('todos.due.show_all')
        }.alignItems(HorizontalAlign.Start).layoutWeight(1)
      }.width('100%').padding({left:12,right:12,top:8,bottom:8}).alignItems(VerticalAlign.Top).margin({top:this.phoneMeasure(10.3,8)})
        .backgroundColor($r('app.color.todo_notice_fill'))
        .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:Tokens.radiusCard})
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).id('todos.due.hidden')
    }.width('100%').padding({left:32,right:32,top:this.phoneMeasure(10,8),bottom:this.hiddenWaitingCount ? 8 : this.phoneMeasure(10.3,8)})
  }
  @Builder listContent() {
    Column() {
      if (this.phoneList && !this.hubMode()) this.phoneListControls()
      else Column() {
      Row({ space: Tokens.gap }) {
        this.modePicker()
        Flex({wrap:FlexWrap.Wrap,space:{main:LengthMetrics.vp(5),cross:LengthMetrics.vp(5)}}) {
          Button({type:ButtonType.Capsule}) {
            Row({space:5}) {
              Text(this.groupOptions()[Math.max(0,this.groupIndex())].value).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.ink)
              SymbolGlyph(Symbols.down).fontSize('10vp').fontColor([Colors.secondary])
            }
          }.padding({left:12,right:12}).height(Math.max(38,Math.ceil(21*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17)))+10)).backgroundColor(Colors.surface).bindMenu(this.listFilters).id('todos.group')
          if (this.workspace.dueFilter !== 'all') Button(this.dueFilterLabel(this.workspace.dueFilter) + ' ×', {type:ButtonType.Capsule})
            .fontColor(Colors.onAction).backgroundColor(Colors.action).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).height(Math.max(38,Math.ceil(21*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17)))+10)).padding({left:12,right:12})
            .onClick(() => this.workspace.setDueFilter('all')).id('todos.due.clear')
        }.layoutWeight(1)
        Badge({ count: (!this.hubMode() && this.workspace.statusFilter !== 'all' ? 1 : 0) + (this.workspace.effectiveGroup() !== 'status' ? 1 : 0) + (this.workspace.dueFilter !== 'all' ? 1 : 0) + (this.workspace.order !== 'recent' ? 1 : 0),
          style: { color: Colors.onAction, badgeColor: Colors.action, borderWidth: 0, borderColor: Color.Transparent, badgeSize: 17, fontSize: '11vp' } }) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(Symbols.filter).fontSize(Tokens.symbolFont).fontColor([Colors.link]) }
          .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('todo_filter_status') + ' · ' + this.statusOptions()[this.statusIndex()].value + ' · ' + this.groupOptions()[Math.max(0, this.groupIndex())].value)
          .bindMenu(this.listFilters).id('todos.filters')
        }
        if (!this.phoneList) Button({ type: ButtonType.Circle }) { SymbolGlyph(Symbols.plus).fontSize(Tokens.symbolFont).fontColor([Colors.link]) }.attributeModifier(ButtonStyles.chrome).accessibilityText(tr('add')).onClick(() => this.workspace.startCreate()).id('todos.add')
      }.padding({ left: Tokens.inset, right: Tokens.inset, top: Tokens.inset, bottom: Tokens.gap })
      Search({value:this.workspace.search,placeholder:tr('todo_search')}).maxFontScale(Tokens.interfaceFontMaxScale).width('100%').height(Math.max(40,Math.ceil(24*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17)))+8))
        .padding({left:Tokens.page,right:Tokens.page}).onChange((value:string) => this.workspace.setSearch(value)).id('todos.search')
      if (this.hiddenWaitingCount) Column({space:4}) {
        Row({space:8}) {
          SymbolGlyph(Symbols.inbox).fontSize('16vp').fontColor([Colors.needs])
          Text(tr('phone_todo_waiting_hidden').replace('{count}',String(this.hiddenWaitingCount))
            .replace('{filter}',this.dueFilterLabel(this.workspace.dueFilter))).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink).layoutWeight(1)
        }.width('100%')
        Button(tr('project_show_all'),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontSize(Tokens.typeSecondary).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(Colors.link)
          .alignSelf(ItemAlign.Start).constraintSize({minHeight:Math.max(36,Math.ceil(22*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(17)))+8)}).padding({left:6,right:6}).onClick(() => this.workspace.setDueFilter('all')).id('todos.due.show_all')
      }.width('100%').padding({left:13,right:10,top:6,bottom:6}).constraintSize({minHeight:44})
        .backgroundColor(Colors.surface).borderRadius(Tokens.radiusCard).margin({top:6}).id('todos.due.hidden')
      }.width('100%')
      if (this.hubMode() && !this.workspace.hubCalm() && !this.workspace.search.trim()) this.hubChoiceBar()
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
        .fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      List({space:this.todoColumns() > 1 ? 10 : (this.phoneList && !this.hubMode() ? 6 : 8)}) {
        if (this.workspace.loading && !this.workspace.items.length) ListItemGroup() { ListItem() { LoadingProgress().height(Tokens.touch).width(Tokens.touch).padding(Tokens.page) } }
        ForEach(this.sectionBindings, (section: BoundTodoSection) => {
          ListItemGroup({ header: () => this.sectionHeader(section), space: this.todoColumns() > 1 ? 10 : 0 }) {
            if (this.sectionOpen(section.value)) ForEach(section.rows, (binding: BoundRow<TodoWorkspaceGroup>) => this.todoRow(binding,section),
              (binding: BoundRow<TodoWorkspaceGroup>) => binding.key)
          }.id(section.key)
        }, (section: BoundTodoSection) => section.key)
        if (!this.sectionBindings.length && !this.workspace.loading) ListItemGroup() {
          ListItem() {
            Text(tr(this.workspace.search ? 'no_results' : 'empty')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).padding(Tokens.page).width('100%')
          }
        }
      }.lanes(this.todoColumns(), workspaceGridGutter(this.todoColumns()))
        .width('100%').layoutWeight(1).padding({ left: this.todoSidePadding(), right: this.todoSidePadding(), bottom: Tokens.inset })
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
      .onAreaChange((_:Area,area:Area)=>{ const w=Number(area.width); if (Number.isFinite(w) && w>0 && Math.abs(w-this.listWidth)>0.5) this.listWidth=w; })
  }

  @Builder agentRow(binding: BoundRow<TodoWorkspaceAgent>) {
    if (binding.value.isActive()) {
      ListItem() { this.agentContent(binding) }.onClick(() => this.onOpen(this.workspace.agentSession(binding.value)))
        .bindContextMenu(this.agentMenu(binding), ResponseType.LongPress)
        .id('todo.agent.' + binding.value.machineId + '.' + binding.value.sessionId)
    } else {
      ListItem() { this.agentContent(binding) }.onClick(() => this.onOpen(this.workspace.agentSession(binding.value)))
        .id('todo.agent.' + binding.value.machineId + '.' + binding.value.sessionId)
    }
  }

  @Builder agentContent(binding: BoundRow<TodoWorkspaceAgent>) {
    ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title || tr('agent'),
      secondaryText: this.workspace.machineName(binding.value.machineId) + ' · ' +
        tr(todoAgentStateLabel(this.workspace.agentState(binding.value))) }) })
  }

  @Builder agentMenu(binding: BoundRow<TodoWorkspaceAgent>) {
    Menu() { MenuItem({ content: tr('todo_unlink_agent') }).enabled(!this.workspace.targetGone)
      .onClick(() => this.confirmUnlinkAgent(this.workspace.selected, binding.value)) }
  }

  @Builder spaceRow(binding: BoundRow<TodoWorkspaceSpace>) {
    ListItem() {
      ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.space,
        secondaryText: this.workspace.machineName(binding.value.machineId) }) })
    }.bindContextMenu(this.spaceMenu(binding), ResponseType.LongPress)
      .id('todo.space.' + binding.value.machineId + '.' + binding.value.space)
  }

  @Builder spaceMenu(binding: BoundRow<TodoWorkspaceSpace>) {
    Menu() { MenuItem({ content: tr('todo_remove_project') }).enabled(!this.workspace.targetGone)
      .onClick(() => this.confirmUnlinkSpace(this.workspace.selected, binding.value)) }
  }

  private detailState():string {
    const todo=this.workspace.selected;
    if (this.hubMode()) return tr(todoHubLabel(this.workspace.hubSection(todo,this.clock)));
    if (todo.startError) return tr('phone_todo_did_not_start');
    const state=this.workspace.state(todo);
    return tr(['needs_you','working'].includes(state) ? state : 'todo_state_'+state);
  }
  private detailStatusLine():string {
    const todo=this.workspace.selected,line=this.hubMode() ? todo.hubView?.line || '' : '';
    return line && ![todo.notes,todo.startError,todo.doneReason].includes(line) ? line : '';
  }

  private detailFooterHeight():number { return PhoneStyle.formSize(46,60.3,nativeEnvironment.fontScale); }
  private footerWords(handoff:boolean):string {
    if (handoff) return tr('todo_detail_hand_to_agent');
    return tr(this.workspace.selected.isDone() && !this.workspace.selected.startError ? 'reopen' : 'todo_detail_done');
  }
  private stackFooter():boolean {
    if (this.workspace.selected.isDone()) return false;
    const ui=this.getUIContext(), available=(Math.min(680,this.detailWidth)-44)/2;
    return [this.footerWords(true),this.footerWords(false)].some((text:string) =>
      ui.px2vp(ui.getMeasureUtils().measureText({textContent:text,fontSize:Tokens.interfaceFontVp(17,nativeEnvironment.fontScale),fontWeight:600}))+52>available);
  }

  @Builder detailFooterAction(handoff:boolean) {
    Button({type:ButtonType.Capsule}) {
      Row({space:5}) {
        SymbolGlyph(handoff ? $r('sys.symbol.paperplane') : this.workspace.selected.isDone() ? $r('sys.symbol.arrow_counterclockwise') : Symbols.check)
          .fontSize(Tokens.interfaceFontVp(17,nativeEnvironment.fontScale)).fontColor([handoff ? Colors.onAction : Colors.ink])
        Text(this.footerWords(handoff)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(600)
          .fontColor(handoff ? Colors.onAction : Colors.ink).maxLines(1)
      }.justifyContent(FlexAlign.Center).width('100%')
    }.width('100%').height(this.detailFooterHeight()).padding({left:8,right:8}).backgroundColor(handoff ? Colors.action : Colors.surface)
      .border({width:handoff ? 0 : PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:this.detailFooterHeight()/2})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark)).enabled(this.workspace.detailEditable() && !this.workspace.saving)
      .onClick(() => { if (handoff) { Haptics.tap(); this.onCreateAgent(this.workspace.todoResource(this.workspace.selected)); } else this.changeCompletion(); })
      .id(handoff ? 'todo.handToAgent' : 'todo.toggleDone')
  }

  @Builder detailFooter() {
    if (this.stackFooter()) Column({space:8}) {
      this.detailFooterAction(true)
      this.detailFooterAction(false)
    }.width('100%').constraintSize({maxWidth:680}).padding({left:16,right:16,bottom:14})
    else Row({space:12}) {
      if (!this.workspace.selected.isDone()) Row() { this.detailFooterAction(true) }.layoutWeight(1)
      Row() { this.detailFooterAction(false) }.layoutWeight(1)
    }.width('100%').constraintSize({maxWidth:680}).padding({left:16,right:16,bottom:14})
  }

  @Builder detailContent() {
    Stack({alignContent:Alignment.Bottom}) {
      Scroll() {
        if (this.workspace.selected.id) Column({ space: Tokens.page }) {
          PhoneTodoDetailForm({workspace:this.workspace, editor:this.detailEditor,locale:this.dateLocale(),now:this.clock,
            status:this.detailState(), statusLine:this.detailStatusLine(),statusMenu:():void=>this.todoMenu(this.workspace.selected),tone:Colors.link})
          TodoStageBar({
            stage: this.workspace.selected.stage,
            stageKind: this.workspace.selected.stageKind,
            done: this.workspace.selected.isDone()
          })
          this.todoWaitSection(this.workspace.selected)
          if (Object.keys(Wire.object(this.workspace.selected.ask, 'suggestion')).length > 0) {
            RuleSuggestionBar({
              model: this.model,
              rawSuggestion: Wire.object(this.workspace.selected.ask, 'suggestion'),
              cardKey: 'todo|' + this.workspace.selected.id + '|' + Wire.text(this.workspace.selected.ask, 'asked_at'),
              manager: this.workspace.selected.manager,
              onSettled: () => { this.workspace.refresh().catch(() => undefined); }
            })
          }
          Column({space:6}) {
            Row() {
              Text(tr('todo_detail_notes')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).layoutWeight(1)
              Button({type:ButtonType.Circle}) { SymbolGlyph(Symbols.edit).fontSize('16vp').fontColor([Colors.link]) }
                .padding(0).width(Tokens.controlBar).height(Tokens.controlBar).backgroundColor(Color.Transparent).accessibilityText(tr('edit')).id('todo.notes.edit')
                .enabled(this.workspace.detailEditable() && !this.workspace.saving).onClick(() => this.workspace.beginEdit(this.workspace.selected))
            }.width('100%')
            if (this.workspace.selected.notes) MarkdownText({text:this.workspace.selected.notes,onLink:(address:string) => this.onLink(address)})
            else Text(tr('todo_detail_empty_notes')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
          }.alignItems(HorizontalAlign.Start).width('100%').padding(16).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
            .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
            .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
          TodoDecisionsSection({
            model: this.model,
            todo: this.workspace.selected,
            editable: this.workspace.detailEditable() && !this.workspace.saving,
            onChanged: () => { this.workspace.refresh().catch(() => undefined); }
          })
          if (this.workspace.selected.project) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: this.workspace.projectTitle(this.workspace.selected) }) }) }
              .onClick(() => this.openProject())
          }
          if (this.workspace.threadTitle(this.workspace.selected)) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: this.workspace.threadTitle(this.workspace.selected) }) }) }
              .onClick(() => this.openThread())
          }
          if (this.workspace.sourceResource(this.workspace.selected)) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('todo_source_conversation') }) }) }
              .onClick(() => this.openSource())
          }
          ForEach(this.lineageRowBindings, (binding: BoundRow<ResourceRow>) => {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title || tr('todo_related') }) }) }
              .onClick(() => this.onOpenResource(binding.value))
          }, (binding: BoundRow<ResourceRow>) => binding.key)
          ForEach(this.relatedTodoBindings, (binding: BoundRow<TodoWorkspaceItem>) => {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title }) }) }
              .onClick(() => this.onOpenResource(this.workspace.todoResource(binding.value)))
          }, (binding: BoundRow<TodoWorkspaceItem>) => binding.key)

          if (this.spaceBindings.length) {
            Text(tr('todo_projects')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).width('100%')
            List() { ForEach(this.spaceBindings, (binding: BoundRow<TodoWorkspaceSpace>) => this.spaceRow(binding),
              (binding: BoundRow<TodoWorkspaceSpace>) => binding.key) }.height(Math.min(300, Tokens.touch * this.spaceBindings.length))
          }

          if (this.activeAgentBindings.length || this.previousAgentBindings.length || !this.workspace.selected.isDone() && this.workspace.candidateSessions().length) Text(tr('todo_agents')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).width('100%')
          ForEach(this.activeAgentBindings, (binding: BoundRow<TodoWorkspaceAgent>) => this.agentRow(binding),
            (binding: BoundRow<TodoWorkspaceAgent>) => binding.key)
          if (this.previousAgentBindings.length) {
            Row() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('todo_previously_involved') }) }) }
              .onClick(() => { this.previousAgentsOpen = !this.previousAgentsOpen; })
            if (this.previousAgentsOpen) {
              ForEach(this.previousAgentBindings, (binding: BoundRow<TodoWorkspaceAgent>) => this.agentRow(binding),
                (binding: BoundRow<TodoWorkspaceAgent>) => binding.key)
            }
          }

          if (!this.workspace.selected.isDone()) {
            if (this.workspace.candidateSessions().length) {
              Select(this.sessionOptions()).attributeModifier(SelectStyles.inline).selected(Math.max(0, this.workspace.candidateSessions().findIndex((row: ResourceRow) => this.sessionKey(row) === this.workspace.selectedAgentKey)))
                .value(this.selectedSession()?.title || tr('todo_choose_agent'))
                .onSelect((index: number) => this.selectSession(index))
                .accessibilityText(tr('todo_choose_agent')).width('100%')
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('todo_link_agent')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('todo_link_agent', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('todo_link_agent')).bindTips(tr('todo_link_agent')).enabled(!!this.selectedSession() && !this.workspace.saving && !this.workspace.targetGone)
                .onClick(() => this.confirmLinkAgent(this.workspace.selected))
            }
            Button({ type: ButtonType.Circle }) { Image($r('sys.media.ohos_ic_public_more')).width(Tokens.symbol).height(Tokens.symbol).fillColor(Colors.link) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('more')).bindTips(tr('more')).bindMenu(this.advancedMenu)
              .enabled(!this.workspace.saving && !this.workspace.targetGone)
            if (this.workspace.spaceChoices().length) {
              Select(this.spaceOptions()).attributeModifier(SelectStyles.inline).selected(Math.max(0, this.workspace.spaceChoices().findIndex((item: TodoWorkspaceSpaceChoice) => this.choiceKey(item) === this.workspace.selectedSpaceKey)))
                .value(this.selectedSpace()?.label || tr('todo_choose_project'))
                .onSelect((index: number) => this.selectSpace(index))
                .accessibilityText(tr('todo_choose_project')).width('100%')
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('todo_link_project')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('todo_link_project', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('todo_link_project')).bindTips(tr('todo_link_project')).enabled(!!this.selectedSpace() && !this.workspace.saving && !this.workspace.targetGone)
                .onClick(() => this.confirmLinkSpace(this.workspace.selected))
            }
          }
          if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
            .fontColor($r('app.color.persona_danger')).width('100%')
        }.alignItems(HorizontalAlign.Start).padding({left:16,right:16,top:16,bottom:this.stackFooter() ? this.detailFooterHeight()*2+48 : this.detailFooterHeight()+38}).width('100%')
        else Text(tr(this.detailEmptyKey())).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).padding(Tokens.page).width('100%')
      }.align(Alignment.TopStart).height('100%').width('100%')
      if (this.workspace.selected.id) this.detailFooter()
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
      .onAreaChange((_:Area,area:Area)=>{this.detailWidth=Number(area.width);})
  }

  @Builder editor() {
    Column({ space: Tokens.page }) {
      if (!this.workspace.selected.id && Number.isFinite(this.workspace.selected.due))
        Text(tr('flow_todo_due')+' · '+this.dueTitle(this.workspace.selected)).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).width('100%').id('todos.calendar.create_due')
      TextInput({ text: this.workspace.titleDraft, placeholder: tr('title') }).onChange((value: string) => this.workspace.setTitleDraft(value))
        .id('todos.title')
      TextArea({ text: this.workspace.notesDraft, placeholder: tr('todo_notes') }).onChange((value: string) => this.workspace.setNotesDraft(value))
        .height(Tokens.inputExpanded).id('todos.notes')
      if (this.workspace.error) Text(tr(this.workspace.error)).maxFontScale(Tokens.interfaceFontMaxScale).fontColor($r('app.color.persona_danger')).width('100%')
      Row({ space: Tokens.gap }) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save'))
          .enabled(!this.workspace.saving && !this.workspace.targetGone && !!this.workspace.titleDraft.trim())
          .onClick(() => this.workspace.save().catch(() => undefined)).id('todos.save')
      }.width('100%').justifyContent(FlexAlign.End)
    }.padding(Tokens.page).width('100%')
  }

  build() {
    Column() {
      if (this.list && this.workspace.calendarOpen) this.calendarContent()
      else if (this.list && this.phoneList) Stack({alignContent:Alignment.Bottom}) {
        this.listContent()
        if (this.workspace.seenUndo) Row({space:12}) {
          SymbolGlyph(Symbols.check).fontSize(Tokens.typeSecondary).fontColor([Colors.positive]).accessibilityLevel('no')
          Text(tr(this.workspace.seenUndo.otherOpen ? 'phone_todo_seen_open' : 'phone_todo_seen_done')).fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary)).fontColor(Colors.ink).flexShrink(1)
          Button(tr('ks_undo'),{buttonStyle:ButtonStyleMode.TEXTUAL}).padding({left:8,right:8}).fontSize(Tokens.typeSecondary)
            .fontColor(Colors.link).onClick(()=>this.workspace.undoSeen()).id('todos.seen.undo')
        }.padding({left:16,right:8}).constraintSize({minHeight:Tokens.controlBar,maxWidth:'94%'}).margin({bottom:12})
          .backgroundColor(Colors.surface).borderRadius(Tokens.radiusSheetCard).shadow(ShadowStyle.OUTER_DEFAULT_SM).id('todos.seen.notice')
      }.width('100%').height('100%')
      else if (this.list) this.listContent()
      else this.detailContent()
    }.width('100%').height('100%')
      .bindSheet($$this.workspace.editorOpen, this.editor(), { title: { title: tr(this.workspace.selected.id ? 'edit' : 'create') }, height: SheetSize.LARGE, showClose: true })
  }
}
