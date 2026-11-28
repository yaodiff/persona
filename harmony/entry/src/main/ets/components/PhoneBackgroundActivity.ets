import type { BackgroundRound } from '../core/BackgroundActivity';
import { activityFeedRound, splitBackgroundRounds } from '../core/BackgroundActivity';
import type { TranscriptItem } from '../core/Domain';
import { i18n } from '@kit.LocalizationKit';
import { relativeActivityDate } from '../core/PhoneDate';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { PhoneStyle } from '../design/PhoneStyle';
import { TodoWorkspacePage } from './TodoWorkspacePage';
import { Colors } from '../design/Colors';
import { tr, trError } from '../design/Strings';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { conversationFeedPath } from '../models/AssistantFeed';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { ClientModel } from '../models/ClientModel';
import { TodoWorkspaceAgent, TodoWorkspaceItem, todoWorkspaceStatus } from '../models/TodoWorkspace';
import { TranscriptModel } from '../models/TranscriptModel';
import { ResourceRow, Wire } from '../models/Wire';
import { Motion } from '../design/Motion';
import { Haptics } from '../platform/Haptics';
import { WorkingArcSpinner } from './StatusMotionMarks';

@ComponentV2
export struct PhoneBackgroundActivity {
  @Require @Param model: ClientModel;
  @Require @Param row: ResourceRow;
  @Event onClose: () => void = () => {};

  @Event onOpen: (row: ResourceRow) => void = (_row: ResourceRow) => {};

  @Event onLink: (address: string) => void = (_address: string) => {};
  @Event onCreateAgent: (todo: ResourceRow) => void = (_todo: ResourceRow) => {};
  @Local taskSheet: boolean = false;
  @Local detailTask: ResourceRow | undefined = undefined;
  @Local detailTitle: string = '';

  @Local taskRows: ResourceRow[] = [];
  @Local viewport: number = 0;
  @Local rounds: BoundRow<BackgroundRound>[] = [];
  @Local feed: Record<string, Object>[] = [];
  @Local opened: string[] = [];
  @Local more: boolean = false;
  @Local cursor: string = '';
  @Local busy: boolean = true;
  @Local error: string = '';

  private transcript: TranscriptModel | undefined = undefined;
  private ticket: number = 0;
  private generation: number = -1;
  private scope: string = '';
  private target: string = '';

  private current(ticket: number): boolean {
    const main = this.model.mainActivityRow();
    return this.ticket === ticket && this.model.owns(this.generation, this.scope) &&
      main !== undefined && this.model.target(main) === this.target;
  }

  @Monitor('model.profile', 'model.connected', 'model.mainId', 'model.mainMachineId')
  accountChanged(): void { if (!this.current(this.ticket)) this.onClose(); }

  aboutToAppear(): void { this.load(); }
  aboutToDisappear(): void { this.ticket++; this.transcript?.close(); this.transcript = undefined; }

  private async load(): Promise<void> {
    const ticket = ++this.ticket;
    this.transcript?.close(); this.transcript = undefined;
    this.generation = this.model.connectionGeneration(); this.scope = this.model.scope(); this.target = this.model.target(this.row);
    this.taskRows = this.model.todos;
    this.busy = true; this.error = ''; this.more = false; this.cursor = ''; this.feed = []; this.rounds = []; this.opened = [];
    try {
      this.transcript = this.model.readActivityTranscript(this.row, (items: readonly TranscriptItem[], more: boolean): void => {
        if (!this.current(ticket)) return;
        const actual = this.model.mainActivityRow();
        const running = actual?.state === 'working' || actual?.state === 'running' || Wire.bool(actual?.data() || {}, 'working');
        const next = Array.from(splitBackgroundRounds(items, running).rounds);
        this.rounds = reconcileBoundRows(this.rounds, next, (round: BackgroundRound) => round.id,
          this.model, this.model, this.scope, this.scope);
        this.more = more;
      }, (error: Error): void => { if (this.current(ticket)) this.error = error.message; });
      await Promise.all([this.transcript?.open(), this.readFeed(ticket, ''), this.readTasks(ticket)]);
    } catch (error) {
      if (this.current(ticket)) this.error = (error as Error).message || 'failed';
    } finally { if (this.current(ticket)) this.busy = false; }
  }

  private async readTasks(ticket: number): Promise<void> {
    if (!this.model.connected || this.model.demo) return;
    const rows = await this.model.repo().todos();
    if (this.current(ticket)) this.taskRows = rows;
  }

  private async readFeed(ticket: number, cursor: string): Promise<void> {
    if (!this.model.connected || this.model.demo || !this.current(ticket)) return;
    const path = conversationFeedPath(this.row.id, this.row.manager || this.model.profile.manager, cursor);
    const page = await this.model.repo().call(path);
    if (!this.current(ticket)) return;
    const items = new Map<string, Record<string, Object>>();
    for (const item of this.feed.concat(Wire.rows(page, 'items'))) {
      const id = Wire.text(item, 'id');
      if (id) items.set(id, item);
    }
    this.feed = Array.from(items.values());
    const next = Wire.text(page, 'next_cursor');
    this.cursor = next !== cursor ? next : '';
  }

  private async older(): Promise<void> {
    if (this.busy) return;
    this.busy = true; this.error = '';
    const ticket = this.ticket;
    try {
      if (this.more) await this.transcript?.older();
      if (this.current(ticket) && this.cursor) await this.readFeed(ticket, this.cursor);
    } catch (error) { if (this.current(ticket)) this.error = (error as Error).message || 'failed'; }
    finally { if (this.current(ticket)) this.busy = false; }
  }

  private toggle(key: string): void {
    Haptics.tap();
    const opening = !this.opened.includes(key);
    Motion.run(this.getUIContext(), Motion.layout(nativeEnvironment.reduceMotion), () => {
      this.opened = opening ? this.opened.concat([key]) : this.opened.filter((id: string) => id !== key);
    });
    if (opening && key.startsWith('work:')) {
      const round = this.rounds.find((binding: BoundRow<BackgroundRound>) => 'work:' + binding.value.id === key);
      if (round) this.readFoldedWork(round.value, key, this.ticket);
    }
  }

  private async readFoldedWork(round: BackgroundRound, key: string, ticket: number): Promise<void> {
    try {
      for (const item of round.items) {
        if (!this.current(ticket) || !this.opened.includes(key)) return;
        if (item.kind === 'fold') await this.transcript?.unfold(item.id);
      }
    } catch (error) { if (this.current(ticket)) this.error = (error as Error).message; }
  }

  private roundFeed(round: BackgroundRound): Record<string, Object>[] {
    return this.feed.filter((item: Record<string, Object>): boolean =>
      activityFeedRound([round], Wire.text(item, 'kind'), Date.parse(Wire.text(item, 'at'))) === round.id)
      .sort((left: Record<string, Object>, right: Record<string, Object>): number =>
        Date.parse(Wire.text(left, 'at')) - Date.parse(Wire.text(right, 'at')));
  }

  private transcriptLabel(item: TranscriptItem): string {
    if (item.kind !== 'tool_use') return item.text || '';
    const input = Wire.object(Wire.record(item as Object), 'tool_input');
    return Wire.text(input, 'description') || Wire.text(input, 'text') || String(item.tool_name || '');
  }

  private changeFold(item: TranscriptItem): void {
    const ticket = this.ticket;
    this.transcript?.unfold(item.id).catch((error: Error): void => { if (this.current(ticket)) this.error = error.message; });
  }

  private task(item: Record<string, Object>): ResourceRow | undefined {
    const id = Wire.text(item, 'ref');
    const owner = this.row.manager || this.model.profile.manager;
    return this.taskRows.find((row: ResourceRow) => row.id === id && (!row.manager || row.manager === owner) &&
      (!row.group || row.group === this.row.group || row.group === this.model.profile.id));
  }

  private taskTitle(item: Record<string, Object>): string {
    return this.task(item)?.title || Wire.text(Wire.object(item, 'body'), 'title') || tr('activity_task');
  }

  private agents(item: Record<string, Object>): TodoWorkspaceAgent[] {
    const raw = this.task(item)?.data();
    if (!raw) return [];
    const agents = Wire.rows(raw, 'agents').map((agent: Record<string, Object>) => new TodoWorkspaceAgent(agent))
      .filter((agent: TodoWorkspaceAgent) => agent.machineId && agent.sessionId && !agent.startError);
    if (!agents.length) return [];
    // The hub can later attach another Agent. Only the Agents linked together at this handoff belong on its card.
    const times = agents.map((agent: TodoWorkspaceAgent) => Date.parse(agent.linkedAt));
    if (times.some((at: number) => !Number.isFinite(at))) return agents.filter((_agent: TodoWorkspaceAgent, i: number) => !Number.isFinite(times[i]));
    const first = Math.min(...times);
    return agents.filter((_agent: TodoWorkspaceAgent, i: number) => times[i] - first <= 120000);
  }

  private listed(agent: TodoWorkspaceAgent): ResourceRow | undefined {
    const owner = this.row.manager || this.model.profile.manager;
    return this.model.sessions.find((session: ResourceRow) => session.manager === owner &&
      session.machine === agent.machineId && (session.session || session.id) === agent.sessionId);
  }

  private follow(item: Record<string, Object>, agent?: TodoWorkspaceAgent): void {
    const task = this.task(item);
    if (!this.current(this.ticket) || !task) return;
    if (!agent) {
      this.detailTask = task; this.detailTitle = task.title; this.taskSheet = true;
      return;
    }
    const current = this.agents(item).find((row: TodoWorkspaceAgent) =>
      row.machineId === agent.machineId && row.sessionId === agent.sessionId);
    if (!current) return;
    const existing = this.listed(current);
    if (existing) { this.onOpen(existing); return; }
    const row = new ResourceRow(current.sessionId, current.title || task.title, 'session');
    row.machine = current.machineId; row.session = current.sessionId; row.manager = task.manager || this.model.profile.manager;
    row.group = task.group || this.row.group; row.path = current.cwd; row.raw = current.raw;
    this.onOpen(row);
  }

  private titleFor(item: Record<string, Object>, agent?: TodoWorkspaceAgent): string {
    return (agent ? this.listed(agent)?.title.trim() || agent.title.trim() : '') || this.taskTitle(item);
  }

  private badge(item: Record<string, Object>): string {
    const wake = Wire.text(Wire.object(item, 'body'), 'wake');
    return tr('activity_handoff_' + (['active', 'resumed', 'continuation'].includes(wake) ? wake : 'new'));
  }

  private status(item: Record<string, Object>, agent?: TodoWorkspaceAgent): string {
    const task = this.task(item);
    if (!task) return tr('activity_task_gone');
    const todo = new TodoWorkspaceItem(task.data(), task.manager);
    if (todo.startError) return tr('activity_start_failed');
    if (!agent) return tr(todo.doneAt ? 'activity_closed' : 'activity_starting_agent');
    const stage = todoWorkspaceStatus(todo, this.model.sessions);
    const session = this.listed(agent);
    if (!session || Wire.bool(session.data(), 'starting')) return tr('activity_starting');
    return tr(['needs_you', 'working'].includes(stage) ? stage : 'todo_state_' + stage);
  }

  private activityTime(agent: TodoWorkspaceAgent): string {
    const row = this.listed(agent);
    const data = row?.data() || {};
    const at = Date.parse(Wire.text(data, 'activity_at') || Wire.text(data, 'updated_at'));
    const locale = this.model.language && this.model.language !== 'default' ? this.model.language : i18n.getSystemLanguage();
    return relativeActivityDate(at, Date.now(), locale, tr('agent_time_just_now'), tr('kb_minutes_ago'), tr('kb_yesterday')) || tr('activity_no_motion');
  }

  @Builder disclosure(label: string, key: string, identity: string) {
    Button({ type: ButtonType.Normal }) {
      Row({ space: 8 }) {
        SymbolGlyph(Symbols.down).fontSize(11).fontColor([Colors.secondary])
          .rotate({ angle: this.opened.includes(key) ? 0 : -90 })
          .animation(Motion.state(nativeEnvironment.reduceMotion))
        Text(label).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).textAlign(TextAlign.Start)
      }.width('100%').padding({ top: 5, bottom: 5 }).constraintSize({ minHeight: 28 })
    }.width('100%').padding(0).backgroundColor(Color.Transparent)
      .accessibilityText(label).accessibilitySelected(this.opened.includes(key)).onClick(() => this.toggle(key)).id(identity)
  }

  @Builder dispatch(item: Record<string, Object>, agent?: TodoWorkspaceAgent, disambiguate: boolean = false) {
    Button({ type: ButtonType.Normal }) {
      Row({ space: 12 }) {
        Stack({ alignContent: Alignment.Center }) {
          PhoneOutlineIcon({name:'dispatchToAgent',color:Colors.ink})
        }.width(32).height(32).backgroundColor($r('app.color.activity_icon_well')).borderRadius(16).accessibilityLevel('no')
        Column({ space: 4 }) {
          Row({ space: 6 }) {
            Text(this.titleFor(item, agent)).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Medium)
              .fontColor(Colors.ink).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis }).flexShrink(1)
            Text(this.badge(item)).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontWeight(FontWeight.Bold)
              .fontColor(Wire.text(Wire.object(item, 'body'), 'wake') === 'active' ? Colors.ink : Colors.surface)
              .backgroundColor(Wire.text(Wire.object(item, 'body'), 'wake') === 'active' ? Color.Transparent : Colors.ink)
              .padding({ left: 6, right: 6, top: 1, bottom: 1 }).border({ width: 0.5, color: Colors.ink, radius: 10 }).maxLines(1).id('dispatch.badge')
          }.width('100%')
          Row({ space: 4 }) {
            if (agent) {
              Text(this.activityTime(agent) + ' ·').fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.tertiary).maxLines(1)
              WorkingArcSpinner({ diameter: 12, tint: Colors.workingMark, surface: Color.Transparent, showCoreDot: false })
            }
            Text(this.status(item, agent)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).maxLines(2)
          }
        }.alignItems(HorizontalAlign.Start).layoutWeight(1)
        if (this.task(item)) { SymbolGlyph($r('sys.symbol.arrow_up_right')).fontSize(13).fontColor([Colors.secondary]).accessibilityLevel('no') }
      }.width('100%').padding(12).constraintSize({ minHeight: 64 })
    }.width('100%').padding(0).backgroundColor(Colors.styleCard || $r('app.color.activity_task_card')).border({ width: 0.5, color: $r('app.color.activity_card_edge'), radius: 15 })
      .enabled(!!this.task(item)).onClick(() => this.follow(item, agent))
      .id('dispatch.task.' + Wire.text(item, 'ref') + (disambiguate && agent ? '.' + agent.sessionId : ''))
  }

  @Builder answered(item: Record<string, Object>) {
    Column({ space: 6 }) {
      Button({ type: ButtonType.Capsule }) {
        Row({ space: 6 }) {
          SymbolGlyph(Symbols.check).fontSize(12).fontColor([Colors.secondary])
          Text(this.taskTitle(item) + ' · ' + (Wire.text(Wire.object(item, 'body'), 'answer') || tr('activity_answered')))
            .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
            .maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis }).flexShrink(1)
          SymbolGlyph(this.opened.includes(Wire.text(item,'id')) ? Symbols.down : Symbols.expand).fontSize(10).fontColor([Colors.secondary])
        }.padding({ left: 11, right: 11, top: 5, bottom: 5 }).constraintSize({ minHeight: 28 })
      }.padding(0).backgroundColor($r('app.color.activity_answer_chip')).constraintSize({ maxWidth: '100%' })
        .accessibilitySelected(this.opened.includes(Wire.text(item,'id'))).onClick(() => this.toggle(Wire.text(item, 'id'))).id('answered-line')
      if (this.opened.includes(Wire.text(item, 'id'))) {
        Column({ space: 6 }) {
          Text(Wire.text(Wire.object(item, 'body'), 'question')).fontColor(Colors.ink).fontSize(16).fontWeight(FontWeight.Medium)
          Text(Wire.text(Wire.object(item, 'body'), 'answer')).fontColor(Colors.secondary).fontSize(15)
          if (Wire.text(Wire.object(item, 'body'), 'reason')) {
            Text(Wire.text(Wire.object(item, 'body'), 'reason')).fontColor(Colors.secondary).fontSize(13)
          }
        }.width('100%').padding(12).alignItems(HorizontalAlign.Start).backgroundColor($r('app.color.activity_answer_chip')).borderRadius(14).id('answered-detail')
      }
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder routed(item: Record<string, Object>) {
    Column({ space: 4 }) {
      Button({ type: ButtonType.Normal }) {
        Row({ space: 4 }) {
          Text(Wire.text(Wire.object(item, 'body'), 'from') === 'assistant' ? 'Persona →' : tr('activity_me') + ' →')
            .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
          Text('@' + this.taskTitle(item)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium)
            .fontColor(Colors.link).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
        }
      }.padding(0).backgroundColor(Color.Transparent).enabled(!!this.task(item)).onClick(() => this.follow(item)).id('activity.routed.task')
      Text(Wire.text(Wire.object(item, 'body'), 'text')).width('100%').textAlign(TextAlign.Start)
        .fontSize(17).fontColor(Colors.ink).padding({ left: 16, right: 16, top: 12, bottom: 12 }).backgroundColor($r('app.color.activity_exchange_fill')).borderRadius(17)
        .id('exchange.text')
    }.width('100%').padding({ right: 32 }).alignItems(HorizontalAlign.Start).id('exchange.routed')
  }

  @Builder feedRow(item: Record<string, Object>) {
    if (Wire.text(item, 'kind') === 'task') {
      if (this.agents(item).length) {
        Column({ space: 8 }) {
          ForEach(this.agents(item), (agent: TodoWorkspaceAgent) => { this.dispatch(item, agent, this.agents(item).length > 1) },
            (agent: TodoWorkspaceAgent) => JSON.stringify([agent.machineId, agent.sessionId]))
        }.width('100%')
      } else { this.dispatch(item) }
    } else if (Wire.text(item, 'kind') === 'routed') { this.routed(item) }
    else if (Wire.text(item, 'kind') === 'answered') { this.answered(item) }
  }

  @Builder activity(binding: BoundRow<BackgroundRound>) {
    Column({ space: 16 }) {
      this.disclosure(tr('activity_record'), binding.value.id, 'supervision-record')
      if (this.opened.includes(binding.value.id)) {
        Column({ space: 6 }) {
          Text(tr('activity_input')).fontSize(13).fontColor(Colors.secondary)
          Text(binding.value.items[0].text || '').width('100%').fontSize(16).fontColor(Colors.ink)
        }.width('100%').padding(12).alignItems(HorizontalAlign.Start).backgroundColor($r('app.color.activity_answer_chip')).borderRadius(14).id('supervision-input')
        this.disclosure(tr('work_summary'), 'work:' + binding.value.id, 'work-head')
        if (this.opened.includes('work:' + binding.value.id)) {
          ForEach(binding.value.items.slice(1), (item: TranscriptItem) => {
            if (item.kind === 'fold') {
              Button(tr('activity_expand')).onClick(() => this.changeFold(item)).id('activity.fold.' + item.id)
            } else if (this.transcriptLabel(item)) {
              Text(this.transcriptLabel(item)).width('100%').fontSize(15).fontColor(Colors.ink).textAlign(TextAlign.Start)
            }
          }, (item: TranscriptItem) => item.id)
        }
      }
      ForEach(this.roundFeed(binding.value), (item: Record<string, Object>) => { this.feedRow(item) }, (item: Record<string, Object>) => Wire.text(item, 'id'))
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder taskDetail() {
    if (this.detailTask) {
      Column() {
        Stack({ alignContent: Alignment.Center }) {
          Text(this.detailTitle).fontSize(17).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis }).padding({ left: 58, right: 58 })
          Row() {
            Button({ type: ButtonType.Circle }) { SymbolGlyph($r('sys.symbol.xmark')).fontSize(21).fontColor([Colors.ink]) }
              .width(44).height(44).backgroundColor(Colors.sheetControl).onClick(() => { this.taskSheet = false; })
              .accessibilityText(tr('close')).id('activity.task.close')
            Blank()
          }.width('100%')
        }.width('100%').height(54).padding({ left: 16, right: 16 })
        TodoWorkspacePage({ model: this.model, row: this.detailTask, list: false, visible: this.taskSheet,
          onTitle: (title: string) => { this.detailTitle = title; },
          onOpen: (row: ResourceRow) => { this.taskSheet = false; this.onOpen(row); },
          onOpenResource: (row: ResourceRow) => { this.taskSheet = false; this.onOpen(row); },
          onLink: (address: string) => this.onLink(address),
          onCreateAgent: (row: ResourceRow) => { this.taskSheet = false; this.onCreateAgent(row); } })
      }.width('100%').height('100%').backgroundColor(Colors.sheetBackground).id('activity.task.page')
    }
  }

  build() {
    Column() {
      Row().width(60).height(4).borderRadius(2).backgroundColor($r('app.color.activity_grabber')).margin({ top: 5, bottom: 4 }).accessibilityLevel('no')
      Stack({ alignContent: Alignment.Center }) {
        Text(tr('background_activity')).fontSize(17).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
        Row() {
          Button({ type: ButtonType.Circle }) {
            SymbolGlyph($r('sys.symbol.xmark')).fontSize(PhoneStyle.recapCloseGlyph(nativeEnvironment.fontScale)).fontColor([Colors.ink])
          }.width(44).height(44).backgroundColor($r('app.color.activity_close_fill')).border({ width: 0.5, color: $r('app.color.activity_card_edge'), radius: 22 }).accessibilityText(tr('close')).onClick(() => this.onClose()).id('activity.close')
          Blank()
        }.width('100%')
      }.width('100%').height(54).padding({ left: 16, right: 16 })
      if (this.error) {
        Text(trError(this.error)).fontColor(Colors.secondary).padding(12)
        Button(tr('retry')).onClick(() => this.load()).id('activity.retry')
      }
      if (!this.rounds.length && this.busy) {
        Blank()
        LoadingProgress().width(32).height(32)
        Blank()
      } else if (!this.rounds.length && !this.more && !this.cursor && !this.error) {
        Blank()
        SymbolGlyph($r('sys.symbol.clock')).fontSize(38).fontColor([Colors.secondary])
        Text(tr('activity_empty')).fontSize(19).fontWeight(FontWeight.Medium).fontColor(Colors.ink).margin({ top: 10 })
        Text(tr('activity_empty_detail')).fontSize(15).fontColor(Colors.secondary).margin({ top: 6 }).id('background-activity-empty')
        Blank()
      } else {
        Scroll() {
          Column({ space: 16 }) {
            if (this.more || this.cursor) { Button(tr('load_more')).enabled(!this.busy).onClick(() => this.older()).id('activity.older') }
            ForEach(this.rounds, (binding: BoundRow<BackgroundRound>) => { this.activity(binding) },
              (binding: BoundRow<BackgroundRound>) => binding.key)
          }.width('100%').constraintSize({ minHeight: this.viewport }).justifyContent(FlexAlign.End)
            .padding({ left: 16, right: 16, top: 12, bottom: 82 })
        }.width('100%').layoutWeight(1).align(Alignment.Top).scrollBar(BarState.Off)
          .onAreaChange((_old: Area, area: Area) => { this.viewport = Number(area.height); }).id('activity.scroll')
      }
    }.width('100%').height('100%').backgroundColor(Colors.stylePage || $r('app.color.activity_sheet_fill')).id('background-activity-page')
      .bindSheet($$this.taskSheet, this.taskDetail(), { height: SheetSize.LARGE, dragBar: true, showClose: false,
        onDisappear: () => { this.taskSheet = false; this.detailTask = undefined; } })
  }
}
