import { directUserRequest, interactionIsDelegated, interactionPermissionSubject } from '../models/InteractionDecision';
import { IconActions } from '../design/IconActions';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { Symbols } from '../design/Symbols';
import { AgentDecisionQueue, AgentDecisionCard } from '../models/AgentDecisionQueue';
import { InteractionDecisionForm } from './InteractionDecisionForm';
import { RuleSuggestionBar } from './RuleSuggestionBar';
import { util } from '@kit.ArkTS';
import { NativeEnvironment, nativeEnvironment } from '../platform/NativeEnvironment';
import { nativeSheetInset } from '../platform/NativeInsets';
import { TaskTrayStartActions } from './TaskTrayStartActions';
import { TaskStartFailureSheet } from './TaskStartFailureSheet';
import { TaskStartRecovery } from '../models/TaskStartRecovery';
import { AssistantViewCard } from './AssistantViewCard';
import { AssistantBriefCard } from './AssistantBriefCard';
import { assistantCardResource } from '../models/AssistantCardLinks';
import { ComposeListItemV2, ContentItemV2, OperateItemV2, OperateCheckV2, LengthMetrics } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { deviceFeatures } from '../models/DeviceFeatures';
import { AssistantFeedModel, PlanEditTask } from '../models/AssistantFeed';
import { NavigationContext } from '../models/NavigationContext';
import { DecisionPager, orderedDecisions } from '../models/DecisionPager';
import { Decision, ResourceRow, Wire } from '../models/Wire';
import { MarkdownText } from './MarkdownText';
import { Tokens } from '../design/Tokens';
import { Colors } from '../design/Colors';
import { PhoneStyle } from '../design/PhoneStyle';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { Haptics } from '../platform/Haptics';
import { assistantBoardRows, assistantTrayRows, TaskTrayRow, TrayRequest } from '../models/AssistantTrays';
import { ButtonStyles } from '../design/Controls';
import { tr } from '../design/Strings';
import { projectAssistantReceipt, AssistantReceiptProjection, AssistantReceiptCriterion } from '../models/AssistantReceipt';

// The Hub accepts bypassPermissions for writable task plans; read-only tasks
// are locked to plan mode and cannot be upgraded from this editor.
const PLAN_PERMISSIONS: string[] = ['bypassPermissions'];

class TaskQuestionRow {
  readonly todoId:string;
  readonly title:string;
  readonly agent:ResourceRow;
  readonly card:AgentDecisionCard;
  readonly at:number;
  constructor(todoId:string,title:string,agent:ResourceRow,card:AgentDecisionCard,at:number) {
    this.todoId=todoId; this.title=title; this.agent=agent; this.card=card; this.at=at;
  }
}

@ComponentV2
export struct AssistantCards {
  @Require @Param model: ClientModel;
  @Require @Param feed: AssistantFeedModel;
  @Param item: Record<string, Object> | undefined = undefined;
  @Param dockOnly: boolean = false;
  @Param decisionPager: DecisionPager | undefined = undefined;
  @Param dockRoom: number = 400;
  @Param composerHeight: number = 0;
  @Param mainDockSpace:number = -1;
  @Event onDockVisibility:(shown:boolean)=>void = (_shown:boolean)=>{};
  @Local private actionBarHeight: number = 28;
  @Local private measuredDockHeight: number = 0;
  @Local private pagerHeight: number = 28;
  @Local private dragHeight: number = -1;
  private dragStart: number = 0;
  @Local private dragCard: string = '';
  @Param detailOnly: boolean = false;
  @Param active: boolean = false;
  @Event onOpenResource: (row: ResourceRow) => void = () => {};
  @Event onOpenSession: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onOpenTodo: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onOpenTriage: () => void = () => {};
  @Event onOpenInbox: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onOpenCard: (itemId: string, kind: string) => void = (_itemId: string, _kind: string) => {};
  @Event onLink: (address: string) => void = (_address: string) => {};

  @Local environment: NativeEnvironment = nativeEnvironment;
  @Local editorInset: number = 0;
  @Local trayOpen: boolean = false;
  @Local boardOpen: boolean = false;
  @Local private failureOpen:boolean=false;
  @Local private recovery:TaskStartRecovery=new TaskStartRecovery();
  private showFailureAfterBoard:boolean=false;
  private failureRow:ResourceRow|undefined;
  @Local trayContentHeight: number = 0;
  @Local trayBucket: string = '';
  @Local private pressedTrayChip: string = '';
  private trayOwner: string = '';
  private readonly trayScroller: Scroller = new Scroller();
  private planScrollKey: string = '';
  @Local private planActionsHeight: number = 0;
  @Local private requests: AgentDecisionQueue = new AgentDecisionQueue();
  private offRequests: (() => void) | undefined;
  private subscribedOwner: string = '';
  private requestModel: ClientModel | undefined;
  private syncRequests():void {
    const key=this.dockOnly && this.active && this.model.connected && this.feed.conversationId && this.model.selected.kind==='conversation' ? this.trayScope() : '';
    if (key) this.decisionPager?.bind(key);
    if (key===this.subscribedOwner && (!key || this.requestModel===this.model)) return;
    this.offRequests?.(); this.offRequests=undefined; this.requests.clear(); this.subscribedOwner=key; this.requestModel=this.model;
    if (!key) return;
    this.requests.reload(this.model,this.model);
    this.offRequests=this.model.observeEvents((event:string) => {
      if (this.subscribedOwner!==key || !this.active || event.length>2*1024*1024) return;
      const frame=Wire.parse(event), type=Wire.text(frame,'type');
      if (['interaction','interactions','interaction_resolved'].includes(type)) this.requests.reload(this.model,this.model);
    });
  }

  @Computed get taskQuestions():TaskQuestionRow[] {
    const questions:TaskQuestionRow[]=[];
    if (!this.dockOnly || !this.active || !this.feed.todosReady || this.feed.owner!==this.model.profile.manager) return questions;
    const tasks=this.feed.todos.filter((todo:Record<string,Object>) =>
      Wire.text(todo,'origin')===this.feed.conversationId && Wire.text(todo,'manager',this.feed.owner)===this.feed.owner);
    tasks.sort((a:Record<string,Object>,b:Record<string,Object>) =>
      (Date.parse(Wire.text(b,'created_at')) || 0) - (Date.parse(Wire.text(a,'created_at')) || 0));
    for (const todo of tasks) {
      const todoId=Wire.text(todo,'id');
      if (!todoId) continue;
      // Each supervising task gets its own turn in the queue. Repeated links inside one task do not.
      const seen=new Set<string>();
      for (const agent of Wire.rows(todo,'agents')) {
        if (Wire.text(agent,'released_at') || Wire.text(agent,'dismissed_at')) continue;
        const machine=Wire.text(agent,'machine_id'), session=Wire.text(agent,'session_id');
        const row=this.model.sessions.find((candidate:ResourceRow) => candidate.manager===this.feed.owner &&
          (!candidate.group || candidate.group===this.model.profile.id) && candidate.machine===machine && (candidate.session || candidate.id)===session);
        if (!row) continue;
        for (const card of this.requests.forSession(this.model,row)) {
          if (seen.has(card.key) || card.owner!==this.model) continue;
          seen.add(card.key);
          questions.push(new TaskQuestionRow(todoId,Wire.text(todo,'title'),row,card,card.since));
        }
      }
    }
    return questions.sort((a:TaskQuestionRow,b:TaskQuestionRow) => a.at-b.at);
  }

  @Computed get decisionPages(): Decision[] {
    if (!this.dockOnly || !this.active || !this.decisionPager) return [];
    const own = this.model.pendingDecisions.length ? this.model.pendingDecisions : this.model.decision ? [this.model.decision] : [];
    return orderedDecisions(own, this.taskQuestions.map((row:TaskQuestionRow) => row.card.request));
  }
  @Computed get decisionIds(): string[] { return this.decisionPages.map((request:Decision) => request.id); }
  @Computed get visibleDock(): boolean { return this.shownDecisions.length>0; }
  @Monitor('visibleDock') dockVisibilityChanged():void { if (this.dockOnly) this.onDockVisibility(this.visibleDock); }
  @Computed get shownDecisions(): Decision[] {
    const pager = this.decisionPager;
    if (!pager || this.trayOpen || pager.collapsed(this.decisionIds)) return [];
    const current = this.decisionPages[pager.position(this.decisionIds)];
    return current ? [current] : [];
  }
  @Monitor('decisionIds', 'requests.snapshotComplete', 'feed.todosReady') decisionListChanged(): void {
    if (this.dockOnly && this.active && this.subscribedOwner === this.trayScope() && this.feed.todosReady && this.requests.snapshotComplete) {
      this.decisionPager?.reconcile(this.decisionIds);
    }
  }
  private supervised(request: Decision): TaskQuestionRow | undefined {
    // A conversation's own request uses the conversation's admission and answer path.
    if (this.model.pendingDecisions.some((own:Decision) => own.id === request.id) || this.model.decision?.id === request.id) return undefined;
    return this.taskQuestions.find((row:TaskQuestionRow) => row.card.request.id === request.id && row.card.request.raw === request.raw);
  }
  private decisionVisible(request: Decision, scope: string): boolean {
    return this.active && this.model.connected && this.subscribedOwner === scope && this.trayScope() === scope &&
      this.shownDecisions.some((shown:Decision) => shown.id === request.id && shown.raw === request.raw && shown.machine === request.machine && shown.session === request.session);
  }
  private decisionAdmission(request: Decision): () => boolean {
    const scope = this.trayScope();
    return ():boolean => this.decisionVisible(request, scope);
  }
  private openQuestionSource(row: TaskQuestionRow, scope: string): void {
    if (!this.decisionVisible(row.card.request, scope)) return;
    const copy = NavigationContext.rowFrom(JSON.stringify(row.agent));
    const envelope = Wire.parse(row.card.request.raw), inner = Wire.object(envelope, 'interaction');
    const data = copy.data();
    data['navigation_decision_tool'] = Wire.text(inner,'tool_use_id',Wire.text(envelope,'tool_use_id'));
    copy.raw = JSON.stringify(data);
    this.onOpenSession(copy);
  }
  private toggleDecisions(): void {
    const pager = this.decisionPager;
    if (!pager || !this.decisionIds.length) return;
    if (this.trayOpen || pager.collapsed(this.decisionIds)) { this.trayOpen = false; pager.show(); }
    else pager.fold(this.decisionIds);
  }
  private moveDecision(offset: number): void {
    const pager = this.decisionPager;
    if (pager && this.shownDecisions.length) pager.select(this.decisionIds, pager.position(this.decisionIds) + offset);
  }
  private trayScope(): string { return JSON.stringify([this.model.scope(), this.model.connectionGeneration(), this.model.target()]); }
  @Monitor('active', 'model.selected', 'model.profile', 'model.connected', 'feed.conversationId')
  trayOwnerChanged(): void { if (!this.active || this.trayOwner !== this.trayScope()) { this.trayOpen = false; this.boardOpen=false; this.hideFailure(); } this.syncRequests(); }
  private openTray(bucket: string): void {
    if (!this.active) return;
    this.planScrollKey='';
    if (this.trayOpen && this.trayBucket===bucket) { this.trayOpen=false; return; }
    this.trayContentHeight=0; this.trayBucket=bucket; this.trayOwner=this.trayScope(); this.expandedEntry=''; this.trayOpen=true;
  }
  private trayRequests(manager:string):TrayRequest[] {
    return this.requests.cards.filter((c:AgentDecisionCard) => c.owner===this.model && c.manager===manager)
      .map((c:AgentDecisionCard) => new TrayRequest(c.machine,c.session,c.request.id,
        c.request.kind==='permission' ? interactionPermissionSubject(c.request) : c.preview,c.since,directUserRequest(c.request),interactionIsDelegated(c.request)));
  }
  @Computed get trayRows(): TaskTrayRow[] {
    if (!this.dockOnly || !this.active || !this.feed.todosReady || this.feed.owner!==this.model.profile.manager) return [];
    const manager=this.feed.owner, session=this.feed.conversationId;
    const main=!this.model.mainMachineId || this.model.mainMachineId===this.model.selected.machine ? this.model.mainId : '';
    const requests=this.trayRequests(manager);
    const feed=this.feed.items.filter((i:Record<string,Object>) => Wire.text(i,'kind')!=='message' || this.feed.messageState(i)==='open');
    const rows=assistantTrayRows(this.feed.directoryTodos,this.model.sessions,feed,this.feed.plans,requests,manager,this.model.profile.id,session,main,Date.now());
    for (const req of requests) {
      if (req.machine!==this.model.selected.machine || req.session!==session || session===main && !req.direct) continue;
      rows.unshift(new TaskTrayRow('question',req.id,'needs','needs_you',req.title,'',{}));
    }
    return rows;
  }
  @Computed get boardRows(): TaskTrayRow[] {
    if (!this.boardOpen || !this.active || this.trayScope()!==this.trayOwner) return [];
    const main=!this.model.mainMachineId || this.model.mainMachineId===this.model.selected.machine ? this.model.mainId : '';
    const manager=this.model.profile.manager;
    return assistantBoardRows(this.feed.directoryTodos,this.model.sessions,manager,this.model.profile.id,this.feed.conversationId,main,{
      requests:this.trayRequests(manager), plans:this.feed.plans, machineName:(id:string):string=>this.model.machineName(id),
      orphanedLine:(reason:string):string=>[tr('tray_orphaned'),['error','offline','closed'].includes(reason) ? tr('tray_orphaned_'+reason) : '']
        .filter((part:string)=>!!part).join(' · ') });
  }
  private openBoardRow(entry: TaskTrayRow): void {
    if (!this.boardOpen || this.trayScope()!==this.trayOwner) return;
    const actual=this.boardRows.find((value:TaskTrayRow) => value.key()===entry.key()), row=actual?.resource;
    if (!row || !actual) return;
    if (actual.kind==='todo' && actual.status==='failed' && this.openFailedTask(row,true)) return;
    this.boardOpen=false;
    if (row.kind==='session') this.onOpenSession(row); else this.onOpenTodo(row);
  }
  private openFailedTask(row:ResourceRow, board:boolean):boolean {
    const scope=this.trayScope(), id=row.id;
    this.failureRow=row; this.showFailureAfterBoard=board;
    if (!board) this.failureOpen=true;
    this.recovery.open(this.model,this.feed,row,()=>this.active && this.trayScope()===scope && this.failureRow?.id===id &&
      (this.showFailureAfterBoard || this.failureOpen));
    if (!this.recovery.id) { this.hideFailure(); return false; }
    if (board) this.boardOpen=false; else this.trayOpen=false;
    return true;
  }
  @Monitor('feed.todos','feed.plans','model.sessions','recovery.state','recovery.busy') failureStarted():void {
    if (!this.failureOpen || this.recovery.state!=='starting' || !this.failureRow || !this.active || this.trayScope()!==this.trayOwner) return;
    const todo=this.feed.todo(this.failureRow.id);
    if (Wire.text(todo,'origin')!==this.feed.conversationId || Wire.text(todo,'manager',this.feed.owner)!==this.feed.owner) return;
    const agents=Wire.rows(todo,'agents').filter((a:Record<string,Object>)=>!Wire.text(a,'released_at') && !Wire.text(a,'dismissed_at'));
    for (const agent of agents) {
      const session=this.model.sessions.find((row:ResourceRow)=>row.manager===this.feed.owner && (!row.group || row.group===this.model.profile.id) &&
        row.machine===Wire.text(agent,'machine_id') && (row.session||row.id)===Wire.text(agent,'session_id'));
      if (session) { this.hideFailure(); this.onOpenSession(session); return; }
    }
    this.recovery.observeNextAttempt();
  }
  private hideFailure():void {
    this.showFailureAfterBoard=false;this.failureOpen=false;this.failureRow=undefined;this.recovery.close();
  }
  private boardClosed():void {
    this.boardOpen=false;
    if (this.showFailureAfterBoard && this.active && this.trayScope()===this.trayOwner) {
      this.failureOpen=true; this.showFailureAfterBoard=false;
    }
  }
  @Builder failureDetail() {
    TaskStartFailureSheet({recovery:this.recovery,onClose:()=>this.hideFailure(),onEdit:()=>{
      const row=this.failureRow;
      if(row && this.active && this.trayScope()===this.trayOwner) { this.hideFailure();this.onOpenTodo(row); }
    }})
  }
  private boardSectionTitle(bucket: string): string {
    return tr(bucket==='needs' ? 'needs_you' : bucket==='working' ? 'working' : bucket==='failed' ? 'tray_did_not_start' :
      bucket==='done' ? 'done' : bucket==='unread' ? 'agent_task_state_unread' : 'todo_state_followup');
  }
  private rowNote(entry: TaskTrayRow): string { return entry.waitingForOwner ? tr('tray_waiting_answer') : entry.note; }
  private trayDetail(entry: TaskTrayRow): string {
    const state=entry.kind==='message' ? tr('tray_draft_reply') : entry.status==='needs_you' ? tr('needs_you') : this.rowState(entry);
    return state+(entry.agentCount>1 ? ' · '+tr('project_agent_count').replace('{count}',String(entry.agentCount)) : '');
  }
  private trayHeight(): number {
    const cap = this.expandedEntry ? 380 : 280;
    const actions = this.pinnedTrayPlan ? this.planActionsHeight || 56 : 0;
    const available = Math.max(actions ? 0 : 48,this.dockRoom-this.composerHeight-this.actionBarHeight-70-actions);
    return Math.min(cap,available,this.trayContentHeight || 54);
  }
  @Local private expandedEntry:string='';
  @Computed get pinnedTrayPlan(): TaskTrayRow | undefined {
    if (!this.active || !this.trayOpen || this.trayBucket!=='needs' ||
      this.trayOwner!==this.trayScope() || !this.expandedEntry.startsWith('plan:')) return undefined;
    return this.trayRows.find((row: TaskTrayRow) => row.kind==='plan' && row.bucket==='needs' &&
      row.key()===this.expandedEntry && this.feed.planState(row.id)==='open');
  }
  private alignOpenedTrayPlan(entry:TaskTrayRow, area:Area): void {
    if (!this.planScrollKey || this.planScrollKey!==entry.key() || this.pinnedTrayPlan?.id!==entry.id) return;
    const top=Number(area.position.y);
    if (!Number.isFinite(top) || Number(area.height)<=0) return;
    // The row is a child of the scrolling content. Its layout position already includes prior rows and group headings.
    this.planScrollKey='';
    this.trayScroller.scrollTo({xOffset:0,yOffset:Math.max(0,top)});
  }
  private trayPlanAction(entry:TaskTrayRow, owner:string, action:string): void {
    const visible=this.pinnedTrayPlan;
    if (!visible || owner!==this.trayOwner || visible.id!==entry.id ||
      Wire.text(visible.data,'conversation')!==Wire.text(entry.data,'conversation')) return;
    if (action==='edit' && this.feed.planCanEdit(entry.id)) this.feed.editPlan(entry.id);
    if (action==='start' && this.feed.planCanStart(entry.id) && Wire.rows(entry.data,'tasks').length) {
      this.feed.startPlan(entry.id,this.feed.rememberPlan(entry.id),undefined,JSON.stringify(entry.data));
    }
    if (action==='dismiss' && this.feed.planCanDismiss(entry.id)) this.feed.dismissPlan(entry.id,JSON.stringify(entry.data));
  }
  private rowState(entry:TaskTrayRow):string {
    if (entry.kind==='plan' || entry.kind==='message') return tr('assistant_feed_'+entry.kind);
    if (entry.status==='failed') return tr('failed');
    if (entry.status==='interrupted' || entry.status==='usage_limit') return this.stateLabel(entry.status);
    if (entry.status==='unread') return tr('todo_state_checking');
    if (entry.status==='waiting') return tr('todo_state_working');
    return tr('todo_state_'+entry.status);
  }
  private trayGroupTitle(id:string):string {
    return this.model.conversations.find((r:ResourceRow) => r.id===id && r.manager===this.feed.owner && (!r.group || r.group===this.model.profile.id))?.title || tr('chat_tray_other');
  }
  private activateTray(entry:TaskTrayRow):void {
    if (!this.active || !this.trayOpen || this.trayScope()!==this.trayOwner) return;
    const actual=this.trayRows.find((r:TaskTrayRow) => r.key()===entry.key());
    if (!actual) return;
    if (actual.kind==='message' || actual.kind==='plan') {
      this.expandedEntry=this.expandedEntry===actual.key() ? '' : actual.key();
      this.planScrollKey=this.expandedEntry && actual.kind==='plan' ? actual.key() : '';
      if (this.expandedEntry && actual.kind==='plan') {
        const id=this.planId(actual.data);
        if (!this.feed.planIsFresh(id) && !this.feed.planLoading(id)) this.feed.refreshPlan(id);
      }
      return;
    }
    if (actual.kind==='todo' && actual.status==='failed' && actual.resource && this.openFailedTask(actual.resource,false)) return;
    this.trayOpen=false;
    if (actual.kind === 'question') {
      const at = this.decisionIds.indexOf(actual.id);
      if (at >= 0) { this.decisionPager?.show(); this.decisionPager?.select(this.decisionIds, at); }
    }
    if (actual.resource && actual.kind==='todo') this.onOpenTodo(actual.resource);
    if (actual.resource && actual.kind==='agent') this.onOpenSession(actual.resource);
  }
  private trayLabel(bucket: string): string { return tr(bucket === 'needs' ? 'chat_tray_needs' : bucket === 'done' ? 'done' : 'chat_tray_progress'); }
  private trayColor(bucket: string): ResourceColor { return bucket === 'needs' ? Colors.needs : bucket === 'done' ? Colors.positive : Colors.working; }

  private editorReady: boolean = false;
  private editorNodeId: string = 'assistant.editor.' + util.generateRandomUUID();
  @Monitor('environment.safeBottomPx') editorBoundaryChanged(): void { this.measureEditor(); }
  private measureEditor(): void {
    if (!this.editorReady) return;
    const value = nativeSheetInset(this.getUIContext(), this.editorNodeId, this.environment.safeBottomPx);
    if (Math.abs(value - this.editorInset) > 0.1) this.editorInset = value;
  }
  @Local receiptView: AssistantReceiptProjection = projectAssistantReceipt({}, {}, false);
  @Local receiptViewStamp: string = '';
  aboutToAppear(): void { this.updateReceiptView(); this.syncRequests(); }
  aboutToDisappear():void { this.hideFailure(); if (this.dockOnly) this.onDockVisibility(false); this.offRequests?.(); this.offRequests=undefined; this.requests.clear(); this.subscribedOwner=''; this.requestModel=undefined; }
  @Monitor('item', 'feed.items', 'feed.todos', 'feed.todosReady', 'feed.todoError', 'feed.busyKeys', 'feed.uncertainKeys', 'model.sessions', 'model.machines', 'model.unresolvedWrites', 'model.selected', 'model.profile', 'model.pendingDecisions', 'model.decision', 'model.connected')
  receiptFactsChanged(): void { this.updateReceiptView(); }
  private updateReceiptView(): void {
    if (!this.item || Wire.text(this.item, 'kind') !== 'receipt') return;
    const projection = this.receiptProjection(this.item), todo = Wire.text(this.item, 'ref');
    this.receiptView = projection;
    const task = this.receiptTodo(this.item), agents = Wire.rows(task, 'agents');
    const sessions = this.model.sessions.filter((row: ResourceRow) => agents.some((agent: Record<string, Object>) => Wire.text(agent, 'machine_id') === row.machine && Wire.text(agent, 'session_id') === (row.session || row.id)))
      .map((row: ResourceRow) => [row.key(), row.title, row.machine, this.model.machineName(row.machine)]);
    this.receiptViewStamp = JSON.stringify([Wire.text(this.item, 'id'), todo, projection, this.feed.todosReady, Wire.text(task, 'id'), agents, sessions, this.feed.busyKeys.includes('todo:' + todo)]);
  }


  private stateLabel(state: string): string {
    const known: string[] = ['not_started', 'working', 'waiting', 'needs_you', 'interrupted', 'usage_limit', 'unread', 'followup', 'done', 'failed', 'cancelled', 'accepted'];
    return tr(known.includes(state) ? 'assistant_feed_state_' + state : 'assistant_feed_state_unknown');
  }

  private machineLabel(id: string): string { return id ? this.model.machineName(id) || id : ''; }

  private planId(item: Record<string, Object>): string {
    return Wire.text(item,'ref') || (Array.isArray(item['tasks']) && Wire.text(item,'state') ? Wire.text(item,'id') : '');
  }
  private plan(item: Record<string, Object>): Record<string, Object> {
    const id = this.planId(item), current = Wire.record(this.feed.plans[id]);
    if (Object.keys(current).length > 0) return current;
    const snapshot = Wire.object(this.itemBody(item), 'plan');
    return Wire.text(snapshot, 'id') === id ? snapshot : Wire.payload([], []);
  }
  private planTasks(item: Record<string, Object>): Record<string, Object>[] { return Wire.rows(this.plan(item), 'tasks'); }
  private planState(item: Record<string, Object>): string { return Wire.text(this.plan(item), 'state', 'unknown'); }
  private planSummaryTitle(item: Record<string, Object>): string {
    const tasks = this.planTasks(item);
    return tasks.length > 0 ? Wire.text(tasks[0], 'title', tr('assistant_feed_plan_unavailable')) : tr('assistant_feed_plan_unavailable');
  }
  private planSummaryMore(item: Record<string, Object>): string {
    const count = this.planTasks(item).length;
    return count > 1 ? tr('assistant_detail_more_tasks') + ' ' + String(count - 1) : '';
  }
  private taskBrief(task: Record<string, Object>): Record<string, Object> { return Wire.object(task, 'brief'); }
  private taskGoal(task: Record<string, Object>): string {
    return Wire.text(this.taskBrief(task), 'goal') || Wire.text(task, 'goal');
  }
  private taskOutOfBounds(task: Record<string, Object>): string[] {
    const values = Wire.strings(this.taskBrief(task), 'out_of_bounds');
    return values.length ? values : Wire.strings(task, 'out_of_bounds');
  }
  private taskDoneWhen(task: Record<string, Object>): string[] {
    const brief = this.taskBrief(task);
    if (Wire.has(brief, 'criteria')) return Wire.strings(brief, 'criteria');
    const values = Wire.strings(brief, 'done_when');
    return values.length ? values : Wire.strings(task, 'done_when');
  }
  private taskBriefSource(task: Record<string, Object>): string[] {
    const brief = this.taskBrief(task), request = Wire.object(brief, 'request');
    return [Wire.text(request, 'text'), Wire.text(brief, 'handover'), Wire.text(brief, 'reason')]
      .filter((value: string) => !!value.trim());
  }
  private itemBody(item: Record<string, Object>): Record<string, Object> { return Wire.object(item, 'body'); }
  private reportText(item: Record<string, Object>): string {
    const body = this.itemBody(item);
    return Wire.text(body, 'detail', Wire.text(body, 'text', Wire.text(body, 'summary')));
  }
  private todoId(card: Record<string, Object>): string { return Wire.text(card, 'todo_id'); }
  private taskTodo(card: Record<string, Object>): Record<string, Object> { return this.feed.todo(this.todoId(card)); }
  private activeAgents(card: Record<string, Object>): Record<string, Object>[] {
    return Wire.rows(this.taskTodo(card), 'agents').filter((agent: Record<string, Object>) =>
      !Wire.text(agent, 'released_at') && !Wire.text(agent, 'dismissed_at'));
  }
  private todoAgents(todoId: string): Record<string, Object>[] {
    return this.activeAgents(Wire.payload(['todo_id'], [todoId]));
  }
  private openTodo(id: string): void {
    const row = this.feed.openTodo(id);
    if (row) { this.trayOpen = false; this.onOpenTodo(row); }
  }
  private openAgentSession(todoId: string, agent: Record<string, Object>): void {
    const row = this.feed.openAgent(todoId, agent);
    if (row) { this.trayOpen = false; this.onOpenSession(row); }
  }
  private agentTitle(todoId: string, agent: Record<string, Object>): string {
    const row = this.feed.openAgent(todoId, agent);
    return row ? row.title || row.session : Wire.text(agent, 'agent');
  }
  private agentMachine(todoId: string, agent: Record<string, Object>): string {
    const row = this.feed.openAgent(todoId, agent);
    return row ? this.machineLabel(row.machine) : '';
  }
  private messageId(item: Record<string, Object>): string { return Wire.text(item, 'id'); }
  private messageState(item: Record<string, Object>): string { return this.feed.messageState(item); }
  private messageTodoId(item: Record<string, Object>): string { return Wire.text(this.itemBody(item), 'todo_id'); }
  private messageTodo(item: Record<string, Object>): Record<string, Object> { return this.feed.todo(this.messageTodoId(item)); }
  private openMessageTodo(item: Record<string, Object>): void { this.openTodo(this.messageTodoId(item)); }

  private openDetail(item: Record<string, Object>): void {
    const id = Wire.text(item, 'id'), kind = Wire.text(item, 'kind');
    if (this.active && id && ['plan', 'receipt'].includes(kind)) this.onOpenCard(id, kind);
  }

  @Builder moreDotsGlyph(tint: ResourceColor) {
    Row({ space: 3.5 }) {
      ForEach([0, 1, 2], (_dot: number) => {
        Circle().width(4).height(4).fill(tint)
      }, (dot: number) => String(dot))
    }.accessibilityLevel('no')
  }

  @Builder planSummary(item: Record<string, Object>) {
    Button({ type: ButtonType.Normal }) {
      Row({ space: Tokens.inset }) {
        Stack({ alignContent: Alignment.Center }) {
          SymbolGlyph(Symbols.document).fontSize('18vp').fontColor([Colors.ai])
        }
          .width(34).height(34).borderRadius(Tokens.radiusControl)
          .backgroundColor(Colors.aiWash)
        Column({ space: Tokens.fine }) {
          Text(tr('assistant_feed_plan')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontWeight(FontWeight.Medium).fontColor(Colors.ai).width('100%')
          Text(this.planSummaryTitle(item)).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).width('100%')
          if (this.planTasks(item).length > 1) Text(this.planSummaryMore(item)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(Colors.secondary).width('100%')
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        SymbolGlyph(Symbols.expand).fontSize(Tokens.meta).fontColor([Colors.secondary])
      }.width('100%').alignItems(VerticalAlign.Center)
    }.buttonStyle(ButtonStyleMode.NORMAL).backgroundColor(Colors.styleCard || Colors.sheetBackground).borderRadius(Tokens.card)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.card })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .padding(Tokens.page).width('100%').enabled(this.active).onClick(() => { Haptics.tap(); this.openDetail(item); })
      .id('assistant.detail.plan.' + Wire.text(item, 'id')).accessibilityText(tr('assistant_detail_view_plan') + ' ' + this.planSummaryTitle(item) + (this.planTasks(item).length > 1 ? ' ' + this.planSummaryMore(item) : ''))
  }

  @Builder planMenu(item: Record<string, Object>) {
    Menu() {
      MenuItem({ content: tr('assistant_detail_dismiss_plan') })
        .enabled(this.feed.planCanDismiss(this.planId(item)))
        .onClick(() => this.feed.dismissPlan(this.planId(item), JSON.stringify(this.plan(item))))
    }
  }

  @Builder planCard(item: Record<string, Object>, actionsPinned:boolean = false) {
    Column({ space: Tokens.page }) {
      if (!Object.keys(this.plan(item)).length) {
        Text(tr('assistant_feed_plan_unavailable')).fontSize(Tokens.body).fontColor($r('sys.color.font_secondary'))
      } else {
        if (!this.feed.planIsFresh(this.planId(item))) {
          if (this.feed.planLoading(this.planId(item))) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
          else Row({ space: Tokens.gap }) {
            Text(tr(this.feed.planActionError(this.planId(item)) || this.feed.error || 'assistant_detail_plan_refresh'))
              .fontSize(Tokens.meta).fontColor(Colors.secondary).layoutWeight(1)
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.feed.refreshPlan(this.planId(item)))
          }.width('100%')
        }
        Row({ space: Tokens.gap }) {
          if (!this.detailOnly) Text(tr('assistant_feed_plan')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).layoutWeight(1)
          else Blank().layoutWeight(1)
          Text(tr('assistant_feed_plan_' + (['open', 'started', 'dismissed'].includes(this.planState(item)) ? this.planState(item) : 'unknown')))
            .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        }.width('100%')
        ForEach(this.planTasks(item), (task: Record<string, Object>, index: number) => {
          this.plannedTask(task, index)
        }, (task: Record<string, Object>, index: number) => String(index) + ':' + JSON.stringify(task))
        if (this.planState(item) === 'open') {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('assistant_feed_remember') }),
            operateItemV2: new OperateItemV2({ checkbox: new OperateCheckV2({ isCheck: this.feed.rememberPlan(this.planId(item)),
              accessibilityText: tr('assistant_feed_remember'), onChange: (_checked: boolean) => this.feed.toggleRememberPlan(this.planId(item)) }) }) })
          if (!actionsPinned) Row({ space: Tokens.inset }) {
            Button({ type: ButtonType.Circle }) { SymbolGlyph(Symbols.edit).fontSize(Tokens.symbolFont).fontColor([Colors.link]) }
              .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('edit')).enabled(this.feed.planCanEdit(this.planId(item)))
              .onClick(() => this.feed.editPlan(this.planId(item))).id('assistant.plan.edit')
            Button({ type: ButtonType.Circle }) { this.moreDotsGlyph(Colors.link) }
              .attributeModifier(ButtonStyles.chrome).accessibilityText(tr('more')).enabled(this.feed.planCanDismiss(this.planId(item)))
              .bindMenu(this.planMenu(item)).id('assistant.plan.more')
            Blank().layoutWeight(1)
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('assistant_feed_start')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('assistant_feed_start', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('assistant_feed_start')).bindTips(tr('assistant_feed_start')).id('assistant.plan.start')
              .enabled(this.feed.planCanStart(this.planId(item)) && this.planTasks(item).length > 0)
              .onClick(() => this.feed.startPlan(this.planId(item), this.feed.rememberPlan(this.planId(item)), undefined,
                JSON.stringify(this.plan(item))))
          }.width('100%')
          if (Object.keys(Wire.object(this.plan(item), 'suggestion')).length > 0) {
            RuleSuggestionBar({
              model: this.model,
              rawSuggestion: Wire.object(this.plan(item), 'suggestion'),
              cardKey: 'plan|' + this.planId(item),
              manager: this.feed.owner,
              onSettled: () => { this.feed.refreshPlan(this.planId(item)); }
            })
          }
        }
        if (this.feed.planActionError(this.planId(item)) && this.feed.planIsFresh(this.planId(item))) Row({ space: Tokens.gap }) {
          Text(tr(this.feed.planActionError(this.planId(item)))).fontSize(Tokens.meta).fontColor(Colors.needs).layoutWeight(1)
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).enabled(!this.feed.planLoading(this.planId(item)))
            .onClick(() => this.feed.refreshPlan(this.planId(item))).accessibilityText(tr('refresh'))
        }.width('100%')
      }
    }.width('100%').padding(Tokens.page).alignItems(HorizontalAlign.Start)
      .backgroundColor(Colors.canvas)
  }

  @Builder plannedTask(task: Record<string, Object>, index: number) {
    Column({ space: Tokens.page }) {
      Text((index + 1).toString() + '. ' + Wire.text(task, 'title', tr('assistant_feed_task')))
        .fontSize(Tokens.title).fontWeight(FontWeight.Medium).fontColor(Colors.ink).width('100%')
      Flex({ wrap: FlexWrap.Wrap, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
        if (Wire.text(task, 'machine')) Text(this.machineLabel(Wire.text(task, 'machine')))
          .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).constraintSize({ maxWidth: '100%' })
        if (Wire.text(task, 'cwd')) Text(Wire.text(task, 'cwd')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).constraintSize({ maxWidth: '100%' })
        if (Wire.bool(task, 'readonly')) Text(tr('read_only')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).constraintSize({ maxWidth: '100%' })
        else if (Wire.text(task, 'permission')) Text(tr('assistant_feed_permission_' + Wire.text(task, 'permission')))
          .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).constraintSize({ maxWidth: '100%' })
      }.width('100%')
      if (Wire.text(task, 'why')) Text(Wire.text(task, 'why')).fontSize(Tokens.meta)
        .fontColor($r('sys.color.font_secondary')).width('100%')
      if (this.taskGoal(task) || this.taskOutOfBounds(task).length || this.taskDoneWhen(task).length || this.taskBriefSource(task).length)
        Divider().strokeWidth(Tokens.hairline).color(Colors.divider)
      if (this.taskGoal(task)) Column({ space: Tokens.fine }) {
        Text(tr('assistant_detail_goal')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).fontColor(Colors.ai).width('100%')
        Text(this.taskGoal(task)).fontSize(Tokens.body).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start)
      if (this.taskOutOfBounds(task).length > 0) Column({ space: Tokens.fine }) {
        Text(tr('assistant_detail_out_of_bounds')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).fontColor(Colors.ink).width('100%')
        ForEach(this.taskOutOfBounds(task), (value: string, valueIndex: number) =>
          Text(value).fontSize(Tokens.body).width('100%'), (_value: string, valueIndex: number) => String(valueIndex))
      }.width('100%').alignItems(HorizontalAlign.Start)
      if (this.taskDoneWhen(task).length > 0) Column({ space: Tokens.fine }) {
        Text(tr('assistant_detail_done_when')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).fontColor(Colors.ink).width('100%')
        ForEach(this.taskDoneWhen(task), (value: string, valueIndex: number) =>
          Text(value).fontSize(Tokens.body).width('100%'), (_value: string, valueIndex: number) => String(valueIndex))
      }.width('100%').alignItems(HorizontalAlign.Start)
      if (this.taskBriefSource(task).length > 0) Column({ space: Tokens.fine }) {
        Text(tr('assistant_detail_brief')).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
        ForEach(this.taskBriefSource(task), (value: string, valueIndex: number) =>
          Text(value).fontSize(Tokens.body).width('100%'), (_value: string, valueIndex: number) => String(valueIndex))
      }.width('100%').alignItems(HorizontalAlign.Start)
      if (Wire.text(task, 'prompt') && (this.taskGoal(task) || this.taskOutOfBounds(task).length || this.taskDoneWhen(task).length || this.taskBriefSource(task).length))
        Divider().strokeWidth(Tokens.hairline).color(Colors.divider)
      if (Wire.text(task, 'prompt')) MarkdownText({ text: Wire.text(task, 'prompt'), onLink: (address: string) => this.onLink(address) })
      if (Wire.text(task, 'error')) Text(Wire.text(task, 'error')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger'))
    }.width('100%').alignItems(HorizontalAlign.Start).padding({ top: Tokens.gap, bottom: Tokens.gap })
  }

  private sessionProgress(todoId: string, agent: Record<string, Object>): string {
    const row = this.feed.openAgent(todoId, agent);
    if (!row) return '';
    const detail: string[] = [];
    const place = [this.machineLabel(row.machine), row.path].filter((value: string) => !!value).join(' · ');
    if (place) detail.push(place);
    if (row.detail) detail.push(row.detail);
    const plan = Wire.object(row.data(), 'plan');
    const total = Wire.number(plan, 'total'), done = Wire.number(plan, 'done');
    if (total > 0 && done >= 0) detail.push(tr('assistant_feed_progress') + ' ' + String(done) + '/' + String(total));
    return detail.join(' · ');
  }

  @Builder agentRow(todoId: string, agent: Record<string, Object>) {
    if (this.feed.openAgent(todoId, agent)) {
      ComposeListItemV2({ contentItemV2: new ContentItemV2({
        primaryText: this.agentTitle(todoId, agent),
        secondaryText: this.sessionProgress(todoId, agent) || Wire.text(agent, 'agent') || this.agentMachine(todoId, agent)
      }) }).onClick(() => this.openAgentSession(todoId, agent)).accessibilityText(tr('assistant_feed_open_agent'))
    }
  }

  @Builder taskMark(entry: TaskTrayRow) {
    Stack({alignContent:Alignment.Center}) {
      if (entry.kind==='message') {
        SymbolGlyph(Wire.text(Wire.object(entry.data,'body'),'source')==='mail' ? $r('sys.symbol.envelope') : Symbols.persona)
          .fontSize(14).fontColor([Colors.link])
      } else {
        Circle().width(9).height(9).fill(this.trayColor(entry.bucket))
      }
    }.width(14).accessibilityLevel('no')
  }

  @Builder taskRow(entry: TaskTrayRow) {
    Column() {
      Button({type:ButtonType.Normal}) {
        Row({space:8}) {
          this.taskMark(entry)
          Column({space:2}) {
            Row({space:8}) {
              Text(entry.title || this.rowState(entry)).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeHeading)).fontColor(Colors.ink)
                .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
              Text(this.trayDetail(entry)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(this.trayColor(entry.bucket)).maxLines(1)
            }.width('100%')
            if (this.rowNote(entry)) Text(this.rowNote(entry)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontColor(Colors.secondary).textAlign(TextAlign.Start).width('100%').maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
          }.alignItems(HorizontalAlign.Start).layoutWeight(1)
          SymbolGlyph(this.expandedEntry===entry.key() ? Symbols.down : Symbols.expand).fontSize(11).fontColor([Colors.tertiary])
        }.width('100%').padding({left:12,right:12,top:7,bottom:7}).constraintSize({minHeight:this.rowNote(entry) ? 54 : 40})
      }.width('100%').padding(0).backgroundColor(Color.Transparent)
        .id('chat.tasks.'+entry.kind+'.'+entry.id).accessibilityText([entry.title,this.trayDetail(entry),this.rowNote(entry)].filter((v:string) => !!v).join('、'))
        .onClick(() => { Haptics.tap(); this.activateTray(entry); })
      if (entry.kind==='todo' && entry.status==='failed' && entry.resource) TaskTrayStartActions({model:this.model,feed:this.feed,row:entry.resource!,
        visible:():boolean=>this.trayOpen && this.active && this.trayScope()===this.trayOwner})
      if (this.expandedEntry===entry.key() && entry.kind==='message') this.message(entry.data)
      if (this.expandedEntry===entry.key() && entry.kind==='plan') this.planCard(entry.data,this.pinnedTrayPlan?.id===entry.id)
    }.width('100%').alignItems(HorizontalAlign.Start)
      .onAreaChange((_before:Area,area:Area) => this.alignOpenedTrayPlan(entry,area))
  }

  @Builder boardMark(row:TaskTrayRow) {
    Stack({alignContent:Alignment.Center}) {
      if (row.bucket==='failed' || row.bucket==='done' || row.bucket==='unread') {
        SymbolGlyph(row.bucket==='failed' ? $r('sys.symbol.exclamationmark_circle_fill') : $r('sys.symbol.checkmark_circle_fill'))
          .fontSize(13).fontColor([row.bucket==='failed' ? Colors.danger : row.bucket==='done' ? Colors.secondary : Colors.positive])
      } else {
        Circle().width(9).height(9).fill(row.bucket==='needs' ? Colors.needsMark : row.bucket==='working' ? Colors.workingMark : Colors.tertiary)
      }
    }.width(16).accessibilityLevel('no')
  }

  @Builder taskBoard() {
    Column() {
      Row().width(36).height(5).borderRadius(Tokens.radiusCapsule).backgroundColor(Colors.tertiary).margin({top:5,bottom:6}).accessibilityLevel('no')
      Stack({alignContent:Alignment.Center}) {
        Text(tr('task')).fontSize(Tokens.typeHeading).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeHeading)).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
        Row() {
          Button({type:ButtonType.Circle}) { SymbolGlyph($r('sys.symbol.xmark')).fontSize(16).fontColor([Colors.ink]) }
            .width(36).height(36).backgroundColor(Colors.sheetControl).accessibilityText(tr('close')).onClick(() => this.boardOpen=false).id('tasks-board-close')
          Blank()
        }.width('100%')
      }.width('100%').height(56).padding({left:20,right:20})
      if (!this.boardRows.length) {
        Blank()
        Text(tr('tray_empty_tasks')).fontSize(19).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
        Text(tr('tray_empty_tasks_detail')).fontSize(Tokens.typeSecondary).fontColor(Colors.secondary).textAlign(TextAlign.Center).padding(12)
        Blank()
      } else {
        Scroll() {
          Column({space:18}) {
            ForEach(['needs','working','unread','followup','done','failed'],(bucket:string) => {
              if (this.boardRows.some((row:TaskTrayRow) => row.bucket===bucket)) {
                Column() {
                  Text(this.boardSectionTitle(bucket)+' · '+this.boardRows.filter((row:TaskTrayRow) => row.bucket===bucket).length)
                    .fontSize(Tokens.typeSectionHeader).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSectionHeader)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary)
                    .padding({left:20,top:12,bottom:10}).constraintSize({minHeight:40}).id('tasks-board.section.'+bucket)
                  Column() {
                    ForEach(this.boardRows.filter((row:TaskTrayRow) => row.bucket===bucket),(row:TaskTrayRow,index:number) => {
                      if (index>0) Divider().strokeWidth(0.5).color(Colors.divider).margin({left:48})
                      Button({type:ButtonType.Normal}) {
                        Row({space:12}) {
                          this.boardMark(row)
                          Column({space:2}) {
                            Text(row.title).fontColor(Colors.ink).fontSize(Tokens.typeBody).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeBody)).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
                            Row({space:5}) {
                              if (row.kind==='agent') SymbolGlyph(Symbols.agents).fontSize(13).fontColor([Colors.secondary])
                              Text(row.note).fontColor(Colors.secondary).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
                            }.constraintSize({minHeight:14})
                          }.alignItems(HorizontalAlign.Start).layoutWeight(1)
                          SymbolGlyph(Symbols.expand).fontSize(11).fontColor([Colors.tertiary])
                        }.width('100%').padding({left:20,right:20,top:14,bottom:14}).constraintSize({minHeight:row.note ? 67.5 : 66})
                      }.width('100%').padding(0).backgroundColor(Color.Transparent).onClick(() => this.openBoardRow(row)).id('tasks-board.'+row.kind+'.'+row.id)
                    },(row:TaskTrayRow,index:number) => JSON.stringify([index,row.key(),row.title,row.note,row.status]))
                  }.width('100%').backgroundColor(Colors.sheetCard).borderRadius(Tokens.radiusCard).clip(true)
                }.width('100%').alignItems(HorizontalAlign.Start)
              }
            },(bucket:string) => bucket)
          }.width('100%').padding({left:16,right:16,bottom:24})
        }.layoutWeight(1).width('100%').align(Alignment.Top)
      }
    }.width('100%').height('100%').backgroundColor(Colors.sheetBackground).id('tasks-board')
  }

  @Builder pinnedPlanActions(entry:TaskTrayRow, owner:string) {
    Column() {
      Divider().strokeWidth(Tokens.hairline).color(Colors.divider).padding({left:12,right:12})
      Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center,space:{main:LengthMetrics.vp(8),cross:LengthMetrics.vp(8)}}) {
        Button(Wire.rows(entry.data,'tasks').length>1 ? tr('tray_plan_start_many').replace('{count}',String(Wire.rows(entry.data,'tasks').length)) : tr('tray_plan_start'))
          .attributeModifier(ButtonStyles.primary).flexGrow(1).flexShrink(0).id('assistant.plan.start')
          .enabled(this.feed.planCanStart(entry.id) && Wire.rows(entry.data,'tasks').length>0)
          .onClick(() => this.trayPlanAction(entry,owner,'start'))
        Row({space:8}) {
          Button(tr('tray_plan_edit')).attributeModifier(ButtonStyles.secondary).id('assistant.plan.edit')
            .enabled(this.feed.planCanEdit(entry.id)).onClick(() => this.trayPlanAction(entry,owner,'edit'))
          Button(tr('tray_plan_dismiss')).attributeModifier(ButtonStyles.secondary).id('assistant.plan.dismiss')
            .enabled(this.feed.planCanDismiss(entry.id)).onClick(() => this.trayPlanAction(entry,owner,'dismiss'))
        }.flexShrink(0)
      }.width('100%').padding({left:16,right:16,top:8,bottom:8})
    }.width('100%').id('action-tray.plan-actions').onAreaChange((_before:Area,area:Area) => {
      const measured=Number(area.height);
      if (measured>0 && Math.abs(measured-this.planActionsHeight)>.5) this.planActionsHeight=measured;
    })
  }
  @Builder trayList() {
    Column() {
      Row({space:8}) {
        Text(this.trayLabel(this.trayBucket)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontWeight(FontWeight.Medium)
          .fontColor(Colors.secondary).layoutWeight(1)
        Button({type:ButtonType.Normal}) {
          Text(tr('all')).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontColor(Colors.secondary)
            .padding({left:12,right:12,top:3,bottom:3}).backgroundColor(Colors.control).borderRadius(Tokens.radiusCard)
        }.constraintSize({minWidth:50}).height(44).padding(0).backgroundColor(Color.Transparent)
          .onClick(() => { this.trayOpen=false; this.boardOpen=true; }).id('action-tray.board')
        Button({type:ButtonType.Circle}) { SymbolGlyph($r('sys.symbol.xmark')).fontSize(15).fontColor([Colors.secondary]) }
          .width(44).height(44).padding(0).backgroundColor(Color.Transparent).accessibilityText(tr('close')).onClick(() => this.trayOpen=false).id('action-tray.close')
      }.width('100%').padding({left:12}).constraintSize({minHeight:44})
      Scroll(this.trayScroller) {
        Column() {
          ForEach(this.trayRows.filter((row:TaskTrayRow) => row.bucket===this.trayBucket),(row:TaskTrayRow) => {
            if (row.groupHeading) Text(this.trayGroupTitle(row.groupHeading)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption))
              .fontWeight(FontWeight.Medium).fontColor(Colors.secondary).textAlign(TextAlign.Start)
              .padding({left:12,top:6,bottom:2}).width('100%').constraintSize({minHeight:24}).id('chat.tasks.group.'+row.groupHeading)
            this.taskRow(row)
          },(row:TaskTrayRow) => JSON.stringify([row.key(),row.title,row.status,row.note,row.groupHeading,row.agentCount,row.waitingForOwner]))
          if (!this.trayRows.some((row:TaskTrayRow) => row.bucket===this.trayBucket)) Text(tr('empty')).fontSize(Tokens.typeCaption).fontColor(Colors.secondary).padding(16)
          if (this.feed.todoError) Text(tr(this.feed.todoError)).fontSize(Tokens.typeCaption).fontColor(Colors.danger)
        }.width('100%').onAreaChange((_old:Area,area:Area) => {this.trayContentHeight=Number(area.height);})
      }.width('100%').height(this.trayHeight()).align(Alignment.TopStart).scrollBar(BarState.Auto).id('action-tray.rows')
      if (this.pinnedTrayPlan) this.pinnedPlanActions(this.pinnedTrayPlan!,this.trayOwner)
    }.width('100%').alignItems(HorizontalAlign.Start).padding({bottom:4}).margin({bottom:16})
      .backgroundColor(Colors.styleID === 'glass' ? Colors.glassSurface : Colors.surface)
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.environment.dark),radius:Tokens.radiusSheetCard})
      .borderRadius(Tokens.radiusSheetCard)
      .shadow(PhoneStyle.floatingElevation(this.environment.dark))
      .transition(Motion.slideFade(this.environment.reduceMotion, 8))
      .id('chat.tasks.tray')
  }

  private metric(points:number):number {
    return points*Math.min(67/51,Math.max(1,this.environment.fontScale));
  }
  private measuredMainPlanRoom():number {
    if (this.mainDockSpace<0 || this.shownDecisions[0]?.kind!=='plan') return 0;
    // The action strip sits below the card. Using its rendered bottom here made
    // its line limit follow the preceding layout when the outer scroll viewport resized.
    const below = this.actionBarHeight + Tokens.page + 2 * Tokens.fine;
    const free = Math.max(44,this.mainDockSpace-below-Tokens.gap-(Tokens.gap+1));
    return Math.min(free,Math.max(this.dockRoom/2,free-72));
  }
  private dockHeightLimit():number {
    // The navigation bar sits outside this pane. Leave the other footer controls, list floor,
    // and a strip of conversation visible as the keyboard and the real window change size.
    const rest=Math.max(0,this.composerHeight)+this.actionBarHeight+20+Tokens.gap*3;
    const free=Math.max(44,this.dockRoom-rest-Tokens.gap-1);
    const fallback=this.environment.keyboardHeightPx>0 ? free : Math.min(free,Math.max(this.dockRoom/2,free-72));
    if (this.environment.keyboardHeightPx>0) return fallback;
    if (deviceFeatures.boundTo(this.model) && deviceFeatures.voiceActive) return Math.min(free,Math.max(44,this.dockRoom/2));
    const tallest=this.tallestDock(), shortest=Math.min(150,tallest);
    if (this.dragCard===this.shownDecisions[0]?.id && this.dragHeight>=0) return Math.max(shortest,Math.min(tallest,this.dragHeight));
    const share=this.model.questionCardHeightShare;
    return share<0 ? this.measuredMainPlanRoom() || fallback : shortest+share*(tallest-shortest);
  }
  private tallestDock():number {
    const rest=Math.max(0,this.composerHeight)+this.actionBarHeight+20+Tokens.gap*3;
    return Math.max(44,this.dockRoom-rest-Tokens.gap-1-24);
  }
  private resizeCard(request:Decision,progress:string,delta:number):void {
    if (this.shownDecisions[0]?.id!==request.id || this.environment.keyboardHeightPx>0) return;
    if (progress==='toggle') {
      this.model.setQuestionCardHeight(this.model.questionCardHeightShare===1 ? -1 : 1).catch(() => {});
      return;
    }
    const top=this.tallestDock(),low=Math.min(150,top),span=top-low;
    if (progress==='up' || progress==='down') {
      const choice=this.model.questionCardHeightShare>=0 ? this.model.questionCardHeightShare :
        span>0 ? (Math.max(low,Math.min(top,this.measuredDockHeight))-low)/span : 1;
      this.model.setQuestionCardHeight(Math.max(0,Math.min(1,choice+(progress==='up' ? .25 : -.25)))).catch(() => {});
      return;
    }
    if (progress==='begin') { this.dragStart=this.measuredDockHeight;this.dragCard=request.id; }
    if (this.dragCard!==request.id) return;
    if (progress==='cancel') { this.dragCard='';this.dragHeight=-1;return; }
    this.dragHeight=Math.max(low,Math.min(top,this.dragStart-delta));
    if (progress==='end') {
      const chosen=span>0 ? (this.dragHeight-low)/span : 1;
      this.model.setQuestionCardHeight(chosen).catch(() => {});this.dragCard='';this.dragHeight=-1;
    }
  }
  @Computed get dockCardLimit():number {
    return Math.max(44,this.dockHeightLimit()-(this.decisionIds.length>1 ? this.pagerHeight+4 : 0));
  }
  private canMoveDecision(next:boolean):boolean {
    return next ? this.decisionPager!.position(this.decisionIds)<this.decisionIds.length-1 : this.decisionPager!.position(this.decisionIds)>0;
  }
  @Builder decisionArrow(next:boolean) {
    Button({type:ButtonType.Normal}) {
      Stack() {
        PhoneOutlineIcon({name:'decisionChevron',color:Colors.systemControls && Colors.styleID==='glass' && !this.canMoveDecision(next) ?
          $r('app.color.decision_pager_disabled') : Colors.ink})
          .rotate({angle:next ? -90 : 90}).scale({x:Math.min(this.environment.fontScale,Tokens.interfaceScaleFor(13)),
            y:Math.min(this.environment.fontScale,Tokens.interfaceScaleFor(13))})
      }.width(this.metric(28)).height(this.metric(28))
        .backgroundColor(Colors.systemControls && Colors.styleID==='glass' ? $r('app.color.decision_header_chip') : Colors.control)
        .borderRadius(this.metric(28)/2)
    }.width(this.metric(44)).height(this.metric(44)-16).padding(0).backgroundColor(Color.Transparent)
      .responseRegion({x:0,y:-8,width:this.metric(44),height:this.metric(44)}).accessibilityText(tr(next ? 'decision_next' : 'decision_previous'))
      .enabled(this.canMoveDecision(next))
      .onClick(() => this.moveDecision(next ? 1 : -1)).id(next ? 'chat.decisions.next' : 'chat.decisions.previous')
  }
  @Builder decisionNavigation() {
    Row({space:8}) {
      Row({space:4}) {
        ForEach(this.decisionPager!.dots(this.decisionIds),(at:number) => {
          Blank().width(this.metric(at===this.decisionPager!.position(this.decisionIds) ? 18 : 9)).height(this.metric(9)).borderRadius(this.metric(9)/2)
            .backgroundColor(at===this.decisionPager!.position(this.decisionIds) ? Colors.link : Colors.control).accessibilityLevel('no')
        },(at:number) => String(at))
      }
      Text(String(this.decisionPager!.position(this.decisionIds)+1)+' / '+String(this.decisionIds.length))
        .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).id('chat.decisions.position')
      Blank().layoutWeight(1)
      this.decisionArrow(false)
      this.decisionArrow(true)
    }.width('100%').padding({left:4,right:4}).onAreaChange((_before:Area,area:Area):void => {
      const h=Number(area.height);if (h>0 && Math.abs(h-this.pagerHeight)>0.5) this.pagerHeight=h;
    })
  }
  @Builder questionCard(request:Decision, row:TaskQuestionRow | undefined, scope:string) {
    Column() {
      InteractionDecisionForm({model:this.model,request:request,taskAgent:row?.agent,taskCard:row?.card,taskQueue:row ? this.requests : undefined,
        docked:true, dockSource:row?.title || '', draft:this.decisionPager?.draft(request), draftSession:this.trayScope(),
        resizeDock:this.environment.keyboardHeightPx===0 && this.dockRoom>0 && !(deviceFeatures.boundTo(this.model) && deviceFeatures.voiceActive), onDockResize:(phase:string,delta:number):void => this.resizeCard(request,phase,delta),
        heightLimit:this.dockCardLimit,admission:this.decisionAdmission(request),
        onDockFold:() => this.decisionPager?.fold(this.decisionIds), onLater:() => this.decisionPager?.fold(this.decisionIds),
        onViewTask:() => { if (row) this.openQuestionSource(row,scope); },
        onLink:(address:string) => { if (this.decisionVisible(request,scope)) this.onLink(address); }})
    }.width('100%').id('chat.tasks.question.'+request.id)
      .onAreaChange((_before:Area,area:Area):void => {
        const h=Number(area.height)+(this.decisionIds.length>1 ? this.pagerHeight+4 : 0);
        if (h>0 && Math.abs(this.measuredDockHeight-h)>0.5) this.measuredDockHeight=h;
      }).parallelGesture(PanGesture({fingers:1,direction:PanDirection.Horizontal,distance:24}).onActionEnd((event:GestureEvent) => {
        if (Math.abs(event.offsetX)>66 && Math.abs(event.offsetX)>2*Math.abs(event.offsetY)) this.moveDecision(event.offsetX<0 ? 1 : -1);
      }))
  }
  private trayDotColor(bucket: string): ResourceColor {
    if (bucket === 'needs') return Colors.needsMark;
    if (bucket === 'done') return Colors.positive;
    return Colors.workingMark;
  }
  private trayCount(bucket: string): number {
    return this.trayRows.filter((r: TaskTrayRow) => r.bucket === bucket).length;
  }
  @Builder decisionEntry() {
    Button({type:ButtonType.Capsule}) {
      Row({space:6}) {
        Circle().width(7.5).height(7.5).fill(this.shownDecisions.length ? Colors.onAction : Colors.needsMark).accessibilityLevel('no')
        Text(tr('decision_to_decide')).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontWeight(FontWeight.Medium)
          .fontColor(this.shownDecisions.length ? Colors.onAction : Colors.ink)
        Text(String(this.decisionIds.length)).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontWeight(FontWeight.Bold)
          .fontColor(this.shownDecisions.length ? Colors.onAction : Colors.needs)
          .padding({left:6,right:6,top:1,bottom:1}).borderRadius(Tokens.radiusCapsule)
          .backgroundColor(this.shownDecisions.length ? Colors.onActionWash : Colors.accentWell)
        SymbolGlyph(this.shownDecisions.length ? Symbols.down : Symbols.up).fontSize('11vp').fontColor([this.shownDecisions.length ? Colors.onAction : Colors.needs])
        if (this.decisionPager!.arrived(this.decisionIds)>0) Text('+'+String(this.decisionPager!.arrived(this.decisionIds))).fontSize(Tokens.typeLabel).fontWeight(FontWeight.Medium).fontColor(this.shownDecisions.length ? Colors.onAction : Colors.needs)
      }
    }.constraintSize({minHeight:32}).padding({left:11,right:11,top:5,bottom:5})
      .fontColor(this.shownDecisions.length ? Colors.onAction : Colors.ink)
      .backgroundColor(this.shownDecisions.length ? Colors.action : (Colors.styleCard || Colors.sheetBackground))
      .border({
        width: PhoneStyle.surfaceEdgeWidth(0.5),
        color: PhoneStyle.cardBorder(this.environment.dark),
        radius: Tokens.radiusCapsule
      })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .scale({
        x: this.pressedTrayChip === 'decisions' ? MotionTokens.pressScale : 1,
        y: this.pressedTrayChip === 'decisions' ? MotionTokens.pressScale : 1
      })
      .animation(Motion.release(this.environment.reduceMotion))
      .onTouch((event: TouchEvent) => {
        if (event.type === TouchType.Down) this.pressedTrayChip = 'decisions';
        else if (event.type === TouchType.Up || event.type === TouchType.Cancel) {
          if (this.pressedTrayChip === 'decisions') this.pressedTrayChip = '';
        }
      })
      .responseRegion({x:0,y:-6,width:'100%',height:44}).accessibilitySelected(this.shownDecisions.length>0)
      .accessibilityText(tr('decision_to_decide')+' '+String(this.decisionIds.length))
      .onClick(() => { Haptics.tap(); this.toggleDecisions(); }).id('chat.decisions.toggle')
  }
  @Builder taskDock() {
    Column({space:Tokens.fine}) {
      if (this.trayOpen) this.trayList()
      if (this.shownDecisions.length && this.decisionIds.length > 1) this.decisionNavigation()
      ForEach(this.shownDecisions,(request:Decision) => {
        this.questionCard(request,this.supervised(request),this.trayScope())
      },(request:Decision) => JSON.stringify([this.trayScope(),request.id,request.raw]))
      Scroll() {
        Row({space:8}) {
          if (this.decisionPager && this.decisionIds.length) this.decisionEntry()
          ForEach(['needs','progress','done'],(bucket:string) => {
            if (bucket!=='done' || this.trayBucket==='done' && this.trayOpen || this.trayRows.some((r:TaskTrayRow) => r.bucket===bucket)) Button({type:ButtonType.Capsule}) {
              Row({space:6}) {
                Circle()
                  .width(7.5)
                  .height(7.5)
                  .fill(this.trayOpen && this.trayBucket===bucket ? Colors.onAction : this.trayDotColor(bucket))
                  .opacity(this.trayOpen && this.trayBucket===bucket || this.trayCount(bucket) > 0 ? 1 : 0.45)
                  .accessibilityLevel('no')
                Text(this.trayLabel(bucket)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontWeight(FontWeight.Medium)
                  .fontColor(this.trayOpen && this.trayBucket===bucket ? Colors.onAction : this.trayCount(bucket) > 0 ? Colors.ink : Colors.secondary)
                Text(String(this.trayCount(bucket))).fontSize(Tokens.typeLabel).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeLabel)).fontWeight(FontWeight.Bold)
                  .fontColor(this.trayOpen && this.trayBucket===bucket ? Colors.onAction : this.trayCount(bucket) > 0 ? this.trayColor(bucket) : Colors.secondary)
                  .padding({left:6,right:6,top:1,bottom:1}).borderRadius(Tokens.radiusCapsule)
                  .backgroundColor(this.trayOpen && this.trayBucket===bucket
                    ? Colors.onActionWash
                    : this.trayCount(bucket) > 0 && bucket === 'needs'
                      ? Colors.accentWell
                      : Colors.pressOverlay)
              }
            }.constraintSize({minHeight:32}).padding({left:11,right:9,top:5,bottom:5})
              .backgroundColor(this.trayOpen && this.trayBucket===bucket ? Colors.action : (Colors.styleCard || Colors.sheetBackground))
              .border({
                width: PhoneStyle.surfaceEdgeWidth(0.5),
                color: PhoneStyle.cardBorder(this.environment.dark),
                radius: Tokens.radiusCapsule
              })
              .shadow(PhoneStyle.cardElevation(this.environment.dark))
              .scale({
                x: this.pressedTrayChip === bucket ? MotionTokens.pressScale : 1,
                y: this.pressedTrayChip === bucket ? MotionTokens.pressScale : 1
              })
              .animation(Motion.release(this.environment.reduceMotion))
              .onTouch((event: TouchEvent) => {
                if (event.type === TouchType.Down) this.pressedTrayChip = bucket;
                else if (event.type === TouchType.Up || event.type === TouchType.Cancel) {
                  if (this.pressedTrayChip === bucket) this.pressedTrayChip = '';
                }
              })
              .responseRegion({x:0,y:-6,width:'100%',height:44}).accessibilitySelected(this.trayOpen && this.trayBucket===bucket)
              .accessibilityText(this.trayLabel(bucket)+'、'+String(this.trayCount(bucket)))
              .onClick(() => { Haptics.tap(); this.openTray(bucket); }).id('chat.tasks.'+bucket)
          },(bucket:string) => bucket)
        }.padding({ top: 2, bottom: 3 })
      }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).width('100%').align(Alignment.Start)
        .padding({left:4,right:4}).margin({top:this.shownDecisions.length ? Tokens.page : 0})
        .onAreaChange((_before:Area,area:Area):void => {
          const h=Number(area.height);if (h>0 && Math.abs(h-this.actionBarHeight)>0.5) this.actionBarHeight=h;
        })
    }.width('100%').alignItems(HorizontalAlign.Start).padding({top:3,bottom:Tokens.fine})
  }

  private receiptTodo(item: Record<string, Object>): Record<string, Object> { return this.feed.todo(Wire.text(item, 'ref')); }

  private receiptProjection(item: Record<string, Object>): AssistantReceiptProjection {
    const todoId = Wire.text(item, 'ref'), todo = this.receiptTodo(item);
    return projectAssistantReceipt(item, todo, this.feed.todosReady, this.feed.receiptLeadAgent(todoId),
      this.feed.canRetryReceipt(Wire.text(item, 'id'), todoId), this.feed.canContinueReceipt(Wire.text(item, 'id'), todoId), this.model.canApprove());
  }

  @Builder receiptSummary(item: Record<string, Object>) {
    ForEach([this.receiptViewStamp], (_stamp: string) => { this.receiptSummaryContent(item, this.receiptView) }, (stamp: string) => stamp)
  }

  private receiptTone(projection: AssistantReceiptProjection): ResourceColor {
    if (projection.outcome === 'done') return Colors.positive;
    if (projection.outcome === 'failed') return Colors.danger;
    return Colors.secondary;
  }

  @Builder receiptSummaryContent(item: Record<string, Object>, projection: AssistantReceiptProjection) {
    Button({ type: ButtonType.Normal }) {
      Row({ space: Tokens.inset }) {
        SymbolGlyph(projection.outcome === 'done' ? Symbols.check : Symbols.document).fontSize(Tokens.symbolFont)
          .fontColor([this.receiptTone(projection)])
        Column({ space: Tokens.fine }) {
          Row({ space: Tokens.gap }) {
            Text(projection.title || tr('assistant_feed_receipt')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).layoutWeight(1)
            Text(tr('assistant_detail_receipt_' + projection.outcome)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(this.receiptTone(projection))
          }.width('100%')
          if (projection.summary) Text(projection.summary).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontColor(Colors.secondary).width('100%').maxLines(3)
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        SymbolGlyph(Symbols.expand).fontSize(Tokens.meta).fontColor([Colors.secondary])
      }.width('100%')
    }.buttonStyle(ButtonStyleMode.NORMAL).backgroundColor(Colors.styleCard || Colors.sheetBackground).borderRadius(Tokens.card)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.card })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .padding(Tokens.page).width('100%').enabled(this.active).onClick(() => { Haptics.tap(); this.openDetail(item); })
      .id('assistant.detail.receipt.' + Wire.text(item, 'id')).accessibilityText(tr('assistant_detail_view_receipt') + ' ' + projection.title + ' ' + tr('assistant_detail_receipt_' + projection.outcome) + ' ' + projection.summary)
  }

  @Builder receiptCriteriaRow(criterion: AssistantReceiptCriterion) {
    Column({ space: Tokens.fine }) {
      Row({ space: Tokens.gap }) {
        Text(criterion.criterion).fontSize(Tokens.body).layoutWeight(1)
        Text(tr('assistant_detail_criterion_' + criterion.state)).fontSize(Tokens.meta)
          .fontColor(criterion.state === 'met' ? Colors.positive : Colors.secondary)
      }.width('100%')
      if (criterion.evidence) Text(criterion.evidence).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      if (criterion.change) Text(criterion.change).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start).padding({ top: Tokens.gap, bottom: Tokens.gap })
  }

  @Builder receipt(item: Record<string, Object>) {
    ForEach([this.receiptViewStamp], (_stamp: string) => { this.receiptContent(item, this.receiptView, Wire.text(item, 'ref'), this.receiptTodo(item)) }, (stamp: string) => stamp)
  }

  @Builder receiptContent(item: Record<string, Object>, projection: AssistantReceiptProjection, todoId: string,
    todo: Record<string, Object>) {
    Column({ space: Tokens.gap }) {
      Text(projection.title || tr('assistant_feed_receipt')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      Text(tr('assistant_detail_receipt_' + projection.outcome)).fontSize(Tokens.meta)
        .fontColor(projection.outcome === 'done' ? Colors.positive : Colors.secondary)
      if (projection.continued) Text(tr('assistant_detail_continued')).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      if (projection.modelServiceInterruption) Text(tr('assistant_detail_model_service_interruption'))
        .fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      if (projection.summary) MarkdownText({ text: projection.summary, onLink: (address: string) => this.onLink(address) })
      if (projection.error && projection.error !== projection.summary) Text(projection.error).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      if (projection.pullRequest) MarkdownText({ text: projection.pullRequest, onLink: (address: string) => this.onLink(address) })
      if (projection.criteria.length > 0) {
        Text(tr('assistant_detail_criteria')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
        Text(tr('assistant_detail_criteria_met') + ' ' + String(projection.metCount) + ' / ' + String(projection.criteria.length))
          .fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
        ForEach(projection.criteria, (criterion: AssistantReceiptCriterion) => this.receiptCriteriaRow(criterion),
          (criterion: AssistantReceiptCriterion) => JSON.stringify(criterion))
      }
      if (projection.criteriaMore > 0) Text(tr('assistant_detail_criteria_more') + ' ' + String(projection.criteriaMore))
        .fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
      if (this.feed.todoError) {
        Text(tr(this.feed.todoError)).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%')
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.feed.refresh())
      }
      if (this.feed.error) Row({ space: Tokens.gap }) {
        Text(tr(this.feed.error)).fontSize(Tokens.meta).fontColor(Colors.needs).layoutWeight(1)
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).onClick(() =>
          this.feed.locateItem(Wire.text(item, 'id'), 'receipt'))
      }.width('100%')
      if (projection.recovery) {
        Button({ type: ButtonType.Circle }) { if (IconActions.busy('assistant_detail_' + projection.recovery)) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.onAction); else SymbolGlyph(IconActions.symbol('assistant_detail_' + projection.recovery)).fontSize(Tokens.symbolFont).fontColor([IconActions.color('assistant_detail_' + projection.recovery, true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('assistant_detail_' + projection.recovery)).bindTips(tr('assistant_detail_' + projection.recovery))
          .enabled(projection.recovery === 'retry' ? this.feed.canRetryReceipt(Wire.text(item, 'id'), todoId) :
            this.feed.canContinueReceipt(Wire.text(item, 'id'), todoId))
          .onClick(() => this.feed.retryReceipt(Wire.text(item, 'id'), todoId, projection.recovery))
          .id('assistant.detail.recovery.' + Wire.text(item, 'id'))
      }
      if (todoId && Object.keys(todo).length > 0) {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('assistant_detail_open_task') }) })
          .onClick(() => this.openTodo(todoId)).accessibilityText(tr('assistant_detail_open_task'))
        ForEach(this.todoAgents(todoId), (agent: Record<string, Object>) => this.agentRow(todoId, agent),
          (agent: Record<string, Object>) => Wire.text(agent, 'machine_id') + '/' + Wire.text(agent, 'session_id'))
      }
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.page)
      .backgroundColor(Colors.grouped)
  }

  @Builder answered(item: Record<string, Object>) {
    Column({ space: Tokens.gap }) {
      Text(tr('assistant_feed_answered')).fontSize(Tokens.body).fontWeight(FontWeight.Medium)
      if (Wire.text(this.itemBody(item), 'summary')) Text(Wire.text(this.itemBody(item), 'summary')).fontSize(Tokens.body)
      if (Wire.rows(this.itemBody(item), 'items').length === 0) {
        if (Wire.text(this.itemBody(item), 'question')) Text(Wire.text(this.itemBody(item), 'question')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        if (Wire.text(this.itemBody(item), 'answer')) Text(Wire.text(this.itemBody(item), 'answer')).fontSize(Tokens.body)
      }
      ForEach(Wire.rows(this.itemBody(item), 'items'), (entry: Record<string, Object>, index: number) => {
        Column({ space: Tokens.fine }) {
          if (Wire.text(entry, 'question')) Text(Wire.text(entry, 'question')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
          if (Wire.text(entry, 'answer')) Text(Wire.text(entry, 'answer')).fontSize(Tokens.body)
        }.width('100%').alignItems(HorizontalAlign.Start)
      }, (_entry: Record<string, Object>, index: number) => String(index))
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.page)
      .backgroundColor($r('sys.color.comp_background_tertiary'))
  }

  @Builder review(item: Record<string, Object>) {
    Column({ space: Tokens.gap }) {
      Text(Wire.text(this.itemBody(item), 'title', tr('assistant_feed_review'))).fontSize(Tokens.body).fontWeight(FontWeight.Medium)
      Text(tr('assistant_feed_review_' + Wire.text(this.itemBody(item), 'verdict', 'unknown'))).fontSize(Tokens.meta)
        .fontColor($r('sys.color.font_secondary'))
      if (Wire.text(this.itemBody(item), 'note')) MarkdownText({ text: Wire.text(this.itemBody(item), 'note'), onLink: (address: string) => this.onLink(address) })
      ForEach(Wire.rows(this.itemBody(item), 'checks'), (check: Record<string, Object>, index: number) => {
        Row({ space: Tokens.gap }) {
          Text(Wire.text(check, 'criterion')).layoutWeight(1)
          Text(tr('assistant_feed_check_' + (Wire.bool(check, 'met') ? 'met' : 'not_met'))).fontColor($r('sys.color.font_secondary'))
        }.width('100%')
      }, (_check: Record<string, Object>, index: number) => String(index))
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.page)
      .backgroundColor($r('sys.color.comp_background_tertiary'))
  }

  @Builder routed(item: Record<string, Object>) {
    Column({ space: Tokens.gap }) {
      Text(Wire.text(this.itemBody(item), 'from') === 'assistant' ? tr('assistant_feed_persona_message') : tr('assistant_feed_task_message'))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (Wire.text(this.itemBody(item), 'text')) MarkdownText({ text: Wire.text(this.itemBody(item), 'text'), onLink: (address: string) => this.onLink(address) })
      if (Wire.bool(this.itemBody(item), 'interrupted')) Text(tr('assistant_feed_interrupted')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.page)
      .backgroundColor($r('sys.color.comp_background_tertiary'))
  }

  @Builder report(item: Record<string, Object>) {
    Column({ space: Tokens.gap }) {
      Text(tr('assistant_feed_report')).fontSize(Tokens.body).fontWeight(FontWeight.Medium)
      if (this.reportText(item)) MarkdownText({ text: this.reportText(item), onLink: (address: string) => this.onLink(address) })
      else Text(tr('assistant_feed_report_unavailable')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.page)
      .backgroundColor($r('sys.color.comp_background_tertiary'))
  }

  @Builder message(item: Record<string, Object>) {
    Column({ space: Tokens.gap }) {
      Text([Wire.text(this.itemBody(item), 'sender'), Wire.text(this.itemBody(item), 'chat')].filter((value: string) => !!value).join(' · ') || tr('assistant_feed_message'))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (Wire.text(this.itemBody(item), 'text')) MarkdownText({ text: Wire.text(this.itemBody(item), 'text'), onLink: (address: string) => this.onLink(address) })
      if (this.messageTodoId(item) && Object.keys(this.messageTodo(item)).length > 0) {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(this.messageTodo(item), 'title'),
          secondaryText: this.stateLabel(this.todoState(this.messageTodoId(item))) }) }).onClick(() => this.openMessageTodo(item))
      }
      Text(tr('assistant_feed_message_' + this.messageState(item))).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (this.messageState(item) === 'sent') {
        Text(Wire.text(this.itemBody(item), 'sent', Wire.text(this.itemBody(item), 'draft'))).fontSize(Tokens.body).width('100%')
      } else {
        TextArea({ text: this.feed.messageDraft(item), placeholder: tr('assistant_feed_draft') })
          .height(Tokens.inputExpanded).enabled(this.messageState(item) === 'open' && !this.feed.busyKeys.includes('message:' + this.messageId(item)))
          .onChange((value: string) => this.feed.setMessageDraft(this.messageId(item), value)).id('assistant-feed.draft.' + this.messageId(item))
      }
      if (this.messageState(item) === 'open') {
        Row({ space: Tokens.gap }) {
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('assistant_feed_send')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('assistant_feed_send', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('assistant_feed_send')).bindTips(tr('assistant_feed_send')).enabled(this.feed.messageCanSend(item)).onClick(() => this.confirmSend(item))
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('assistant_feed_dismiss')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('assistant_feed_dismiss', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('assistant_feed_dismiss')).bindTips(tr('assistant_feed_dismiss')).enabled(this.feed.messageCanDismiss(item))
            .onClick(() => this.feed.dismissMessage(this.messageId(item)))
        }.width('100%')
      } else if (this.messageState(item) === 'pending') {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).onClick(() => this.feed.refresh())
      } else if (this.messageState(item) === 'dismissed' && this.feed.messageCanUndo(item)) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('assistant_feed_undo_dismiss')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('assistant_feed_undo_dismiss', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('assistant_feed_undo_dismiss')).bindTips(tr('assistant_feed_undo_dismiss')).onClick(() => this.feed.undoDismissMessage(this.messageId(item)))
      }
      if (Wire.text(this.itemBody(item), 'filed_at')) Text(tr('assistant_feed_filed_elsewhere')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      if (Wire.text(item, 'ref')) Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(tr('assistant_feed_open_inbox')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).width('100%').padding({ left: Tokens.page, right: Tokens.page }).onClick(() => this.openInbox(item))
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.page)
      .backgroundColor($r('sys.color.comp_background_tertiary'))
  }

  private todoState(todoId: string): string {
    const card = this.feed.taskCardsForConversation().find((value: Record<string, Object>) => Wire.text(value, 'todo_id') === todoId);
    return card ? Wire.text(card, 'state', 'unknown') : 'unknown';
  }

  private confirmSend(item: Record<string, Object>): void {
    const id = Wire.text(item, 'id'), body = Wire.object(item, 'body'), draft = this.feed.messageDraft(item);
    const destination = [Wire.text(body, 'source'), Wire.text(body, 'sender'), Wire.text(body, 'chat')].filter((value: string) => !!value).join(' · ');
    const details = [destination, Wire.text(body, 'text'), draft].filter((value: string) => !!value).join('\n\n');
    this.getUIContext().showAlertDialog({ title: tr('assistant_feed_confirm_send'), message: details,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('assistant_feed_send'), action: () => this.feed.sendMessage(id, draft) } });
  }

  private openInbox(item: Record<string, Object>): void {
    const body = Wire.object(item, 'body'), id = Wire.text(item, 'ref');
    if (!id || !this.feed.owner) return;
    const row = new ResourceRow(id, Wire.text(body, 'sender', Wire.text(body, 'chat', id)), 'inbox');
    row.manager = this.feed.owner; row.raw = JSON.stringify(body);
    this.onOpenInbox(row);
  }

  private openCardResource(address: string): void {
    if (!this.active) return;
    const row = assistantCardResource(address, this.model.profile.id, this.feed.owner, this.model.sessions.concat(this.model.conversations));
    if (!row) { this.model.error = 'assistant_view_invalid_target'; return; }
    if (!row.title) row.title = tr(row.kind === 'todo' ? 'todos' : row.kind === 'knowledge' ? 'knowledge' : row.kind === 'conversation' ? 'main_chat' : 'agents');
    this.onOpenResource(row);
  }
  private cardKey(item: Record<string, Object>): string { return Wire.text(item, 'ref', Wire.text(Wire.object(this.itemBody(item), 'view'), 'id')); }

  @Builder unsupported(item: Record<string, Object>) {
    Column({ space: Tokens.gap }) {
      Text(tr('assistant_feed_unsupported_activity')).fontSize(Tokens.body)
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.page)
      .backgroundColor($r('sys.color.comp_background_tertiary'))
  }

  @Builder activity(item: Record<string, Object>) {
    if (Wire.text(item, 'kind') === 'view') AssistantViewCard({ model: this.model, feed: this.feed, item: item, cardKey: this.cardKey(item), active: this.active, onLink: (address: string) => this.onLink(address), onOpenResource: (address: string) => this.openCardResource(address) })
    else if (Wire.text(item, 'kind') === 'plan') { if (this.detailOnly) this.planCard(item); else this.planSummary(item) }
    else if (Wire.text(item, 'kind') === 'receipt') { if (this.detailOnly) this.receipt(item); else this.receiptSummary(item) }
    else if (Wire.text(item,'kind')==='brief') AssistantBriefCard({model:this.model,feed:this.feed,item:item,active:this.active,onOpenTriage:() => this.onOpenTriage(),onOpen:(row:ResourceRow) => this.onOpenResource(row)})
    else if (Wire.text(item, 'kind') === 'answered') this.answered(item)
    else if (Wire.text(item, 'kind') === 'review') this.review(item)
    else if (Wire.text(item, 'kind') === 'routed') this.routed(item)
    else if (Wire.text(item, 'kind') === 'report') this.report(item)
    else if (Wire.text(item, 'kind') === 'message') this.message(item)
    else this.unsupported(item)
  }

  private planEditMachineIds(): string[] {
    const ids = this.model.machines.filter((row: ResourceRow) => Wire.text(row.data(), 'trust') !== 'pending' &&
      !Wire.bool(row.data(), 'pending') && !Wire.bool(row.data(), 'pending_confirm')).map((row: ResourceRow) => row.id).filter((id: string) => !!id);
    return ids;
  }

  @Builder editTask(task: PlanEditTask, index: number, ids: string[]) {
    Column({ space: Tokens.gap }) {
      if (this.feed.editTasks.length > 1) Text(tr('assistant_feed_task') + ' ' + String(index + 1))
        .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      TextInput({ text: task.title, placeholder: tr('title') }).onChange((value: string) => this.feed.updateEditTask(index, 'title', value))
      if (ids.length > 0) {
        if (!ids.includes(task.machine)) Text(this.machineLabel(task.machine)).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
        Select(ids.map((id: string): SelectOption => ({ value: this.machineLabel(id) })))
          .value(ids.includes(task.machine) ? this.machineLabel(task.machine) : '').onSelect((choice: number) => {
            if (choice >= 0 && choice < ids.length) this.feed.updateEditTask(index, 'machine', ids[choice]);
          })
      } else {
        TextInput({ text: task.machine, placeholder: tr('assistant_feed_machine') })
          .onChange((value: string) => this.feed.updateEditTask(index, 'machine', value))
      }
      TextInput({ text: task.cwd, placeholder: tr('assistant_feed_folder') }).onChange((value: string) => this.feed.updateEditTask(index, 'cwd', value))
      if (task.readonly) Text(tr('read_only')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary'))
      else Select(PLAN_PERMISSIONS.map((mode: string): SelectOption => ({ value: tr('assistant_feed_permission_' + mode) })))
        .value(tr('assistant_feed_permission_' + task.permission)).onSelect((choice: number) => {
          if (choice >= 0 && choice < PLAN_PERMISSIONS.length) this.feed.updateEditTask(index, 'permission', PLAN_PERMISSIONS[choice]);
        })
      TextArea({ text: task.prompt, placeholder: tr('assistant_feed_prompt') }).height(Tokens.inputExpanded)
        .onChange((value: string) => this.feed.updateEditTask(index, 'prompt', value))
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder planEditor() {
    Column({ space: Tokens.page }) {
      Scroll() {
        Column({ space: Tokens.section }) {
          ForEach(this.feed.editTasks, (task: PlanEditTask, index: number) => this.editTask(task, index, this.planEditMachineIds()),
            (_task: PlanEditTask, index: number) => String(index))
        }.width('100%').alignItems(HorizontalAlign.Start)
      }.layoutWeight(1).width('100%').align(Alignment.TopStart)
      if (this.feed.error) Text(tr(this.feed.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('app.color.persona_danger'))
      Row({ space: Tokens.gap }) {
        Blank().layoutWeight(1)
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('assistant_feed_start')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('assistant_feed_start', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('assistant_feed_start')).bindTips(tr('assistant_feed_start')).id('assistant.plan.editor.start')
          .enabled(this.feed.planCanStart(this.feed.editingPlanId) && this.feed.editTasks.every((task: PlanEditTask) =>
            task.title.trim().length > 0 && task.prompt.trim().length > 0 && task.machine.trim().length > 0 && task.cwd.trim().length > 0 &&
            (task.readonly || task.permission === 'bypassPermissions')))
          .onClick(() => this.feed.startEditedPlan())
      }.width('100%')
    }.width('100%').height('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.page, bottom: this.editorInset + Tokens.page }).alignItems(HorizontalAlign.Start)
      .id(this.editorNodeId).onAreaChange(() => this.measureEditor())
  }

  build() {
    if (this.dockOnly) {
      Column() {
        this.taskDock()
        Column().width(0).height(0).bindSheet($$this.boardOpen,this.taskBoard(),{height:'97.4%',preferType:SheetType.BOTTOM,
          dragBar:false,showClose:false,onDisappear:()=>this.boardClosed()})
        Column().width(0).height(0).bindSheet($$this.failureOpen,this.failureDetail(),{height:'97.4%',detents:['60%','97.4%'],detentSelection:'97.4%',
          preferType:SheetType.BOTTOM,dragBar:false,showClose:false,onDisappear:()=>this.hideFailure()})
      }.width('100%')
        .bindSheet($$this.feed.editing, this.planEditor(), {
          title: { title: tr('assistant_feed_edit_plan') }, height: SheetSize.LARGE, showClose: true,
          onAppear: () => { this.editorReady = true; this.measureEditor(); }, onDisappear: () => this.editorReady = false,
          onWillDismiss: () => this.feed.cancelPlanEdit()
        })
    } else if (this.item) {
      if (this.detailOnly) {
        Column() { this.activity(this.item) }.width('100%')
          .bindSheet($$this.feed.editing, this.planEditor(), {
            title: { title: tr('assistant_feed_edit_plan') }, height: SheetSize.LARGE, showClose: true,
            onAppear: () => { this.editorReady = true; this.measureEditor(); }, onDisappear: () => this.editorReady = false,
          onWillDismiss: () => this.feed.cancelPlanEdit()
          })
      } else this.activity(this.item)
    }
  }
}
