import { PhoneGroupingGlyph } from './PhoneGroupingGlyph';
import { AgentTaskGrouping } from '../models/AgentTaskGrouping';
import { LengthMetrics } from '@kit.ArkUI';
import { pasteboard } from '@kit.BasicServicesKit';
import { Colors } from '../design/Colors';
import { Symbols } from '../design/Symbols';
import { Tokens } from '../design/Tokens';
import { PhoneNavigationIcon, PhoneSectionTitle, PhoneStyle } from '../design/PhoneStyle';
import { tr } from '../design/Strings';
import { AgentDecisionQueue, AgentDecisionSnapshot } from '../models/AgentDecisionQueue';
import { ClientModel } from '../models/ClientModel';
import { AgentWorkspace, agentGroupingLabels, coreRowId } from '../models/AgentWorkspace';
import { ProjectSearchResult, projectQuickFilters, projectQuickLabels, projectSearchResult } from '../models/ProjectSearch';
import { machineAgents } from '../models/MachineDashboard';
import { ProjectCatalog, PersonaProject, ProjectAgent, ProjectWorktree, ProjectBrowserState } from '../models/ProjectCatalog';
import { ResourceRow, Wire } from '../models/Wire';
import { ProjectLine, ProjectHost, projectOutline, projectHosts, projectSearchAgents, worktreeSpaceCards } from '../models/ProjectOutline';
import { WorktreeActions, WorktreeChange } from '../models/WorktreeActions';
import { WorktreeSheet } from './WorktreeSheet';
import { AgentDirectoryRowContent } from './AgentDirectoryRowContent';
import { deviceLook } from '../models/DeviceLook';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { compactTreeInset, deduplicateAgentPlace } from '../core/FoldPosture';
import { Motion } from '../design/Motion';
import { Haptics } from '../platform/Haptics';

@ComponentV2
export struct ProjectsPage {
  @Require @Param model: ClientModel;
  @Require @Param catalog: ProjectCatalog;
  @Param agentRepositories: PersonaProject[] | undefined = undefined;
  @Param agentFilterActive: boolean = false;
  @Param searchWorkspace: AgentWorkspace | undefined = undefined;
  @BuilderParam groupedSearchRows?:(workspace:AgentWorkspace,tasks:AgentTaskGrouping,decisions:AgentDecisionQueue)=>void;
  @Local private searchGroups:AgentWorkspace = new AgentWorkspace(undefined,'repo',false);
  @Local private searchTasks:AgentTaskGrouping = new AgentTaskGrouping();
  private groupLoad:number = 0;
  private groupOpening:Promise<void> | undefined;
  private get outlineBrowser(): ProjectBrowserState {
    return this.agentRepositories ? this.catalog.agentBrowser : this.catalog.browser;
  }
  @Computed get outlineProjects(): PersonaProject[] { return this.agentRepositories || this.catalog.projects; }
  @Param compact: boolean = false;
  @Param adjacentItem: string = '';
  @Param topContentInset: number = 0;
  @Event onSearchMode: (active:boolean) => void = () => {};
  @Event isMuted: (row:ResourceRow) => boolean = () => false;
  @BuilderParam agentActions?:(row:ResourceRow,spaceName:string)=>void;
  @Local searchMode: boolean = false;
  @Local quick: string = '';
  private searchField: SearchController = new SearchController();
  @Event onBack: () => void = () => {};
  @Event onMachine: (tree: ProjectWorktree) => void = () => {};
  @Param page: string = 'projects';
  @Param target: string = '';
  private scroller: Scroller = new Scroller();
  private restoredRoute: string = '';
  @Local refreshing: boolean = false;
  @Local editor: WorktreeActions = new WorktreeActions();
  @Local editorShown: boolean = false;
  private editedTree: ProjectWorktree | undefined;
  private createdChange: WorktreeChange | undefined;
  @Local private spaceDecisions: AgentDecisionQueue = new AgentDecisionQueue();
  private stopSpaceEvents: (() => void) | undefined;
  private spaceReadTimer: number = -1;
  @Local private searchCards:AgentDecisionSnapshot[] = [];
  private searchOwners:ClientModel[] = [];
  private stopSearchEvents:(() => void)[] = [];
  private retireSearchEvents():void {
    this.stopSearchEvents.forEach((stop:() => void) => stop());
    this.stopSearchEvents = []; this.searchOwners = []; this.searchCards = [];
  }
  @Monitor('model.directoryMachines') watchSearchOwners():void {
    if (this.page !== 'projects' || !this.searchMode) return;
    const owners = this.model.directoryOwners();
    if (owners.length === this.searchOwners.length && owners.every((owner:ClientModel) => this.searchOwners.includes(owner))) return;
    this.retireSearchEvents();
    this.searchOwners = owners.slice();
    this.stopSearchEvents = owners.map((owner:ClientModel) => owner.observeEvents((_raw:string,type:string) => {
      if (['hello','interaction','interactions','interaction_resolved'].includes(type)) this.refreshSpaceDecisions();
      if (['todo','assistant_feed'].includes(type) && ['todo','direction'].includes(this.searchGroups.groupingMode)) this.prepareSearchGroups();
    }));
    this.refreshSpaceDecisions();
  }
  private refreshSpaceDecisions(): void {
    clearTimeout(this.spaceReadTimer);
    if (this.page === 'projects' && this.searchMode) {
      this.spaceReadTimer = setTimeout(() => this.spaceDecisions.reload(this.model,undefined,
        (pending:AgentDecisionSnapshot[]) => {
          if (!this.searchMode) return;
          this.searchCards = pending;
          this.searchGroups.updateSupervisorCards(this.model,this.spaceDecisions);
        }).catch(() => {}),50);
      return;
    }
    const agent = this.detailTree?.agents[0];
    const owner = agent ? this.model.directoryOwnerFor(agent.row) : undefined;
    if (owner) this.spaceReadTimer = setTimeout(() => this.spaceDecisions.reload(this.model,owner).catch(() => {}),50);
  }
  private pendingDescription(agent:ProjectAgent): string {
    const count = Math.max(agent.pending,this.spaceDecisions.forSession(this.model,agent.row).length);
    return count ? tr('project_pending_count').replace('{count}',String(count)) : '';
  }
  @Monitor('catalog.loading') finishedRefresh(): void { if (!this.catalog.loading) { this.refreshing = false; this.refreshSpaceDecisions(); } }
  @Monitor('model.profile') ownerChanged(): void { this.releaseSearchGroups(); this.retireSearchEvents(); clearTimeout(this.spaceReadTimer); this.spaceDecisions.clear(); this.editor.close(); this.editorShown = false; this.restoredRoute = ''; this.quick = ''; this.searchMode = false; this.onSearchMode(false); }
  aboutToAppear(): void {
    if (this.page === 'project_tree') {
      this.stopSpaceEvents = this.model.observeEvents((_payload:string, type:string) => {
        if (['interaction','interactions','interaction_resolved'].includes(type)) this.refreshSpaceDecisions();
      });
      this.refreshSpaceDecisions();
    }
    if (this.page === 'projects' && this.compact && (this.catalog.browser.searchActive || this.catalog.browser.query)) {
      this.searchMode = true; this.quick=this.catalog.browser.searchQuick; this.onSearchMode(true); this.watchSearchOwners(); this.prepareSearchGroups();
    }
  }
  aboutToDisappear(): void {
    this.releaseSearchGroups(); this.retireSearchEvents(); clearTimeout(this.spaceReadTimer); this.stopSpaceEvents?.(); this.spaceDecisions.clear();
    this.createdChange = undefined; this.editor.close(); if (this.searchMode) this.onSearchMode(false);
  }
  @Event onProject: (key: string) => void = () => {};
  @Event onTree: (key: string) => void = () => {};
  @Event onSession: (row: ResourceRow) => void = () => {};
  @Event onStart: (tree: ProjectWorktree) => void = () => {};
  @Event onChanged: (tree: ProjectWorktree, change: WorktreeChange) => void = () => {};
  @Event onRefresh: () => void = () => {};

  private readonly activityKinds: string[] = ['needs_you', 'working', 'agent_group_unread', 'agent_group_background', 'idle'];
  private count(n: number, word: string): string { return String(n) + ' ' + tr(word); }
  private project(): PersonaProject | undefined { return this.catalog.projects.find((row: PersonaProject) => row.key === this.target); }
  @Computed get hosts(): ProjectHost[] { return projectHosts(this.outlineProjects); }
  @Computed get hostActivity(): Map<string, Map<string, number>> {
    const byHost = new Map<string, Map<string, number>>();
    for (const host of this.hosts) {
      const counts = new Map<string, number>();
      const members = new Map<string,ResourceRow>();
      for (const project of host.projects) {
        for (const agent of projectSearchAgents(project,this.page === 'projects' ? this.catalog.browser.query : '')) {
          if (!agent.archived) members.set(agent.row.key(),agent.row);
        }
      }
      for (const agent of machineAgents(Array.from(members.values()))) {
        let kind = agent.counted;
        if (agent.state === 'working' && kind === 'idle') kind = 'agent_group_background';
        else if (kind === 'unread') kind = 'agent_group_unread';
        else if (kind === 'unknown') kind = 'idle';
        counts.set(kind,(counts.get(kind) || 0) + 1);
      }
      byHost.set(host.key, counts);
    }
    return byHost;
  }
  private hostKinds(key: string): string[] {
    return this.activityKinds.filter((kind: string) => (this.hostActivity.get(key)?.get(kind) || 0) > 0);
  }
  private hostSummary(key: string): string {
    return this.hostKinds(key).map((kind: string) => this.count(this.hostActivity.get(key)!.get(kind)!, kind)).join(' · ');
  }
  private activityTint(kind: string): ResourceColor {
    if (kind === 'needs_you') return Colors.needs;
    if (kind === 'working') return Colors.working;
    if (kind === 'agent_group_unread') return Colors.unread;
    if (kind === 'agent_group_background') return Colors.working;
    return kind === 'agent_group_waiting' ? Colors.ai : Colors.secondary;
  }
  private filteringText(): boolean { return this.agentRepositories ? this.agentFilterActive : this.page === 'projects' && (!!this.catalog.browser.query.trim() || !!this.quick); }
  private narrowOutline(): boolean {
    const width = nativeEnvironment.windowWidthPx;
    return width > 0 && this.getUIContext().px2vp(width) < 320;
  }
  private spaceCardScale(): number {
    if (!nativeEnvironment.windowWidthPx || this.narrowOutline()) return 1;
    const ui = this.getUIContext();
    const columnPx = Math.floor((nativeEnvironment.windowWidthPx-ui.vp2px(PhoneStyle.page*2+12))/2);
    // Both columns follow the thicker selection stroke. The independently rasterized inset can round outward by one pixel.
    const edgePx = Math.ceil(ui.vp2px(1.5))+1;
    const available = Math.floor(ui.px2vp(columnPx-edgePx*2));
    return Math.max(0.88,Math.min(1,available/(150+2*12)));
  }
  // UIFontMetrics scales the iPhone’s 17-point reference to 22⅓ at the interface maximum.
  // This is the geometry scale of its grouped list, distinct from the 23-point body font.
  private outlineControlScale(): number {
    return Math.max(1,Math.min(nativeEnvironment.fontScale,67/51));
  }
  private treeInset(level: number): number {
    // In compact phones AND narrow foldable split master panes (< 420vp), keep indentation tight
    // so full agent titles stay legible without early ellipsis.
    const widthVp = this.agentRepositories ? 340 : (nativeEnvironment.windowWidthPx > 0 ? this.getUIContext().px2vp(nativeEnvironment.windowWidthPx) : 0);
    return compactTreeInset(level, widthVp, this.outlineControlScale());
  }
  private agentCountText(count: number): string {
    return count === 1 ? tr('project_agent_count_one') : tr('project_agent_count').replace('{count}', String(count));
  }
  @Computed get detailTree(): ProjectWorktree | undefined {
    return this.page === 'project_tree' ? this.catalog.selectedWorktree(this.target) : undefined;
  }
  @Computed get detailSessions(): ProjectLine[] {
    const tree = this.detailTree;
    if (!tree) return [];
    return worktreeSpaceCards(this.catalog.projects, tree);
  }
  @Computed get lines(): ProjectLine[] {
    return projectOutline(this.outlineProjects,this.outlineBrowser,this.page === 'project' ? this.target : '',
      this.page === 'machine_projects' ? this.target : '',this.page === 'projects' ? this.quick : '',(agent:ProjectAgent) => this.isMuted(agent.row),
      !!this.agentRepositories,this.agentFilterActive,
      this.page === 'projects' && this.searchMode && this.filteringText() ? this.queryResult.rows : undefined);
  }
  @Computed get tabRows(): Map<string, ProjectLine> {
    const result = new Map<string, ProjectLine>();
    const rows = this.page === 'project_tree' ? [] : this.lines;
    for (const row of rows) if (row.kind === 'tab') result.set(row.key, row);
    return result;
  }
  private route(): string { return this.page + '/' + this.target; }
  private get outlineOffset(): number { return this.page === 'project' || this.compact && this.page === 'projects' && !this.searchMode ? 1 : 0; }
  private restorePosition(): void {
    if (!this.lines.length || this.restoredRoute === this.route()) return;
    const route = this.route(), key = this.catalog.browser.anchor(route);
    this.restoredRoute = route;
    if (!key) return;
    const index = this.lines.findIndex((l:ProjectLine) => l.key === key);
    if (index >= 0) this.scroller.scrollToIndex(index + this.outlineOffset, false, ScrollAlign.START);
  }
  private rememberPosition(start: number): void {
    if (start < 0 || this.restoredRoute !== this.route()) return;
    const index = start - this.outlineOffset;
    if (index < 0) this.catalog.browser.remember(this.route(), '');
    else if (index < this.lines.length) this.catalog.browser.remember(this.route(), this.lines[index].key);
  }
  private search(text:string):void {
    if (this.catalog.browser.query !== text) this.catalog.browser.resetSearchFolds();
    this.catalog.browser.query = text; this.catalog.browser.remember(this.route(),'');
    this.syncSearchGroups();
    if (!this.showGroupedResults && this.lines.length) this.scroller.scrollToIndex(0,false,ScrollAlign.START);
  }
  private selectMachine(key:string):void {
    this.catalog.browser.machine = key; this.catalog.browser.remember(this.route(),'');
    this.syncSearchGroups();
    if (!this.showGroupedResults && this.lines.length) this.scroller.scrollToIndex(0,false,ScrollAlign.START);
  }
  private toggle(line:ProjectLine):void {
    const current = this.lines.find((entry:ProjectLine) => entry.key === line.key);
    if (!current) return;
    if (this.agentRepositories) this.outlineBrowser.toggle(current.key,current.open);
    else this.catalog.toggleBrowserFold(this.model,current.key,current.open);
  }
  private tabOpen(key: string): boolean {
    return this.filteringText() || this.outlineBrowser.open(key, true);
  }
  private toggleTab(key: string): void {
    if (this.filteringText() || !this.tabRows.has(key)) return;
    if (this.agentRepositories) this.outlineBrowser.toggle(key,this.outlineBrowser.open(key,true));
    else this.catalog.toggleBrowserFold(this.model, key, this.outlineBrowser.open(key, true));
  }
  private tabDescription(key: string): string {
    const row = this.tabRows.get(key);
    if (!row) return '';
    return [row.tabTitle, row.tabLead?.row.title || '',
      row.count === 1 ? tr('project_agent_count_one') : tr('project_agent_count').replace('{count}',String(row.count)),
      this.filteringText() ? '' : tr(this.tabOpen(key) ? 'agent_collapse' : 'agent_expand')].filter((v:string) => !!v).join(' · ');
  }
  private tabTint(key: string): ResourceColor {
    const lead = this.tabRows.get(key)?.tabLead;
    return lead ? this.tint(lead) : Colors.secondary;
  }
  private openHeader(line: ProjectLine): void {
    if (this.filteringText()) this.outlineBrowser.toggleSearch(line.key);
    else this.toggle(line);
  }
  private openTree(tree: ProjectWorktree): void {
    const entry = this.catalog.tree(tree.key);
    if (!entry) return;
    const agents = this.agentRepositories ? tree.agents : entry.agents;
    if (agents.length) this.chooseAgent(agents[0]); else this.chooseTree(entry.key);
  }
  private chooseAgent(agent:ProjectAgent):void {
    this.outlineBrowser.selectedTree = ''; this.outlineBrowser.selectedSession = agent.row.key();
    this.onSession(agent.row);
  }
  private chooseTree(key:string):void {
    if (!this.catalog.tree(key)) return;
    this.outlineBrowser.selectedSession = ''; this.outlineBrowser.selectedTree = key;
    this.onTree(key);
  }
  private age(agent:ProjectAgent):string {
    if (!agent.updated) return '';
    const minutes = Math.max(0,Math.floor((Date.now() - agent.updated)/60000));
    if (!minutes) return tr('agent_time_just_now');
    if (minutes < 60) return String(minutes) + tr('agent_time_minute_suffix');
    if (minutes < 1440) return String(Math.floor(minutes/60)) + tr('agent_time_hour_suffix');
    return String(Math.floor(minutes/1440)) + tr('agent_time_day_suffix');
  }
  private tint(agent:ProjectAgent):ResourceColor {
    if (agent.state === 'needs_you' || agent.rank === 2) return Colors.needs;
    if (agent.state === 'unread' || agent.rank === 5) return Colors.unread;
    return agent.state === 'working' ? Colors.working : Colors.secondary;
  }
  private treeDetail(tree:ProjectWorktree):string {
    const parent = tree.parentKey ? this.catalog.tree(tree.parentKey) : undefined;
    if (parent && tree.projectTitle && parent.projectTitle && tree.projectTitle !== parent.projectTitle) return tree.projectTitle;
    if (tree.branch) return tree.branch === tree.title ? '' : tree.branch;
    if (tree.workspaceKind === 'git') return tr('project_detached') + (tree.head ? ' @ ' + tree.head.slice(0,7) : '');
    return '';
  }
  private async copyWorktree(tree:ProjectWorktree, path:boolean):Promise<void> {
    const current = this.menuTarget(tree.key), text = path ? tree.cwd : tree.title;
    if (!current || !text || (path ? current.cwd : current.title) !== text) return;
    const epoch = this.model.connectionGeneration(), scope = this.model.scope();
    try { await pasteboard.getSystemPasteboard().setData(pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN,text)); }
    catch (_) { if (this.model.owns(epoch,scope)) this.catalog.notice = tr('failed'); }
  }
  private menuTarget(key: string): ProjectWorktree | undefined {
    const known = this.catalog.tree(key);
    if (this.page === 'project_tree' && (this.target !== key || this.catalog.selectedWorktree(key) !== known)) return undefined;
    return known;
  }
  private menuEntries(key: string): ProjectWorktree[] {
    const current = this.menuTarget(key);
    return current ? [current] : [];
  }
  private projectMenuRoots(key: string): ProjectWorktree[] {
    const project = this.catalog.projects.find((item: PersonaProject) => item.key === key);
    return project && project.roots.length === 1 ? project.roots : [];
  }
  private headerIdentity(tree: ProjectWorktree): string {
    return JSON.stringify([tree.key, tree.cwd, tree.title, tree.branch, tree.machineTitle, tree.parentKey, tree.breadcrumb,
      tree.online, tree.canStart, tree.permits('create'), tree.permits('rename'), tree.permits('clean')]);
  }
  private startFromTree(key: string): void {
    const current = this.menuTarget(key);
    if (current?.canStart) this.onStart(current);
  }
  private chooseParent(key: string): void {
    const current = this.menuTarget(key);
    if (current?.parentKey) this.chooseTree(current.parentKey);
  }
  private openEditor(tree: ProjectWorktree, action: string): void {
    if (this.editor.busy) return;
    const current = this.menuTarget(tree.key);
    if (!current || current.cwd !== tree.cwd || !current.permits(action)) return;
    this.createdChange = undefined; this.editedTree = current; this.editorShown = true; this.editor.open(this.model, current, action);
  }
  private async complete(check: boolean = false, replay: boolean = false): Promise<void> {
    const tree = this.editedTree;
    if (!tree) return;
    const change = check ? await this.editor.reconcile(replay) : await this.editor.submit();
    if (!change || this.editedTree !== tree || !this.editorShown) return;
    if (change.created) this.createdChange = change;
    this.editorShown = false;
    if (!change.created) this.onChanged(tree, change);
  }
  private editorDismissed(): void {
    const tree = this.editedTree, change = this.createdChange;
    this.createdChange = undefined; this.editedTree = undefined; this.editor.close();
    if (tree && change?.created?.receiptCurrent?.()) this.onChanged(tree,change);
  }
  private currentEditorTree(): ProjectWorktree | undefined {
    const previous = this.editedTree;
    if (!previous) return undefined;
    const current = this.menuTarget(previous.key);
    return current?.cwd === previous.cwd ? current : undefined;
  }
  @Monitor('catalog.projects') editorLocationChanged(): void {
    if (!this.editorShown) return;
    const current = this.currentEditorTree();
    if (current) this.editor.locationTitle = current.title;
  }
  private retryEditor(): void {
    if (!this.editorShown || this.editor.busy || this.editor.loading || this.editor.ready || this.editor.unknown) return;
    const current = this.currentEditorTree();
    if (!current) { this.editor.error = 'request_updated'; return; }
    this.editedTree = current;
    this.editor.open(this.model,current,this.editor.action);
  }
  @Builder editorSheet() {
    WorktreeSheet({ form: this.editor, onCancel: () => { if (!this.editor.busy) this.editorShown = false; },
      onSubmit: () => this.complete(), onReconcile: (replay: boolean) => this.complete(true,replay),
      onRetry: () => this.retryEditor() })
  }
  @Builder actionMenu(key: string) {
    Menu() {
      ForEach(this.menuEntries(key), (tree: ProjectWorktree) => {
        MenuItemGroup() {
          if (this.page !== 'project_tree') MenuItem({ content: tr('project_view_worktree') })
            .onClick(() => this.chooseTree(tree.key)).id('tree.action.open')
          if (tree.canStart) MenuItem({content:tr('project_start_here')}).onClick(() => this.startFromTree(tree.key)).id('tree.action.start')
          if (tree.cwd) MenuItem({content:tr('project_copy_path')}).onClick(() => this.copyWorktree(tree,true)).id('tree.action.copy')
          MenuItem({content:tr('project_copy_name')}).onClick(() => this.copyWorktree(tree,false)).id('tree.action.copy_name')
          if (tree.parentKey) MenuItem({content:tr('project_open_parent')}).onClick(() => this.chooseParent(tree.key)).id('tree.action.parent')
          if (tree.permits('create')) MenuItem({ content: tr('tree_create') }).onClick(() => this.openEditor(tree, 'create')).id('tree.action.create')
          if (tree.permits('rename')) MenuItem({ content: tr('tree_rename') }).onClick(() => this.openEditor(tree, 'rename')).id('tree.action.rename')
          if (tree.permits('clean')) MenuItem({ content: tr('tree_cleanup') }).contentFontColor(Colors.danger).onClick(() => this.openEditor(tree, 'clean')).id('tree.action.clean')
        }
      }, (tree: ProjectWorktree) => this.headerIdentity(tree))
    }
  }
  @Builder more(tree: ProjectWorktree) {
    if (tree.cwd || tree.canStart || tree.permits('create') || tree.permits('rename') || tree.permits('clean')) {
      Button({ type: ButtonType.Circle }) {
        Row({space:2}) {
          ForEach([0,1,2], (_dot:number) => { Circle().width(4).height(4).fill(Colors.link) }, (dot:number) => String(dot))
        }
      }.width(42).height(42).padding(0).backgroundColor(Colors.surface).border({width:0.5,color:Colors.divider,radius:21}).accessibilityText(tr('tree_actions'))
        .responseRegion({ x: -3, y: -3, width: 44, height: 44 })
        .bindMenu(this.actionMenu(tree.key)).id('tree.actions')
    }
  }
  @Builder icon(symbol: Resource, tint: ResourceColor) {
    Stack() { SymbolGlyph(symbol).fontSize('18vp').fontColor([tint]) }
      .width(32).height(32).borderRadius(Tokens.radiusControl).backgroundColor(Colors.control)
  }
  @Builder detailRow(title: string, detail: string, symbol: Resource, color: ResourceColor, identity: string, action: () => void) {
    Row({ space: 11 }) {
      this.icon(symbol, color)
      Column({ space: 2 }) {
        Text(title).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(2).textOverflow({ overflow: TextOverflow.Ellipsis })
        if (detail) Text(detail).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).maxLines(2).textOverflow({ overflow: TextOverflow.Ellipsis })
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      SymbolGlyph(Symbols.expand).fontSize('13vp').fontColor([Colors.secondary])
    }.width('100%').padding({ left: 14, right: 14, top: 10, bottom: 10 }).constraintSize({ minHeight: 52 })
      .accessibilityGroup(true).accessibilityText(title + (detail ? ' · ' + detail : '')).onClick(action).id(identity)
  }
  @Builder message(value: string) {
    Text(value).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).padding(16).width('100%').textAlign(TextAlign.Start)
  }
  @Builder machineMenu() {
    Menu() {
      MenuItem({content:tr('project_all_machines')}).selected(!this.catalog.browser.machine).selectIcon(true).onClick(() => this.selectMachine('')).id('projects.machine.all')
      ForEach(this.hosts,(host:ProjectHost) => {
        MenuItem({content:host.name}).selected(this.catalog.browser.machine === host.key).selectIcon(true).onClick(() => this.selectMachine(host.key)).id('projects.machine.'+host.key)
      },(host:ProjectHost) => host.key)
    }
  }
  private setSearchMode(active:boolean): void {
    if (!this.compact || this.searchMode === active) return;
    this.searchMode = active; this.catalog.browser.searchActive=active; this.onSearchMode(active);
    if (active) { this.watchSearchOwners(); this.prepareSearchGroups(); }
    else {
      this.retireSearchEvents(); clearTimeout(this.spaceReadTimer); this.spaceDecisions.clear();
      this.releaseSearchGroups(); this.searchField.stopEditing(); this.quick = ''; this.catalog.browser.searchQuick=''; this.search('');
    }
  }
  @Computed get directorySearchRows():ResourceRow[] {
    if (!this.searchMode || this.page !== 'projects' || this.agentRepositories) return [];
    const source = new Map<string,ResourceRow>();
    // Keep cached workspace rows from known owners and let the live directory override the same session.
    for (const project of this.catalog.projects) for (const tree of project.trees) for (const agent of tree.agents) {
      if (this.model.directoryOwnerFor(agent.row)) source.set(agent.row.key(),agent.row);
    }
    for (const row of this.model.directorySessions) if (row.kind === 'session') source.set(row.key(),row);
    return Array.from(source.values());
  }
  @Computed get queryResult(): ProjectSearchResult {
    if (this.page !== 'projects' || !this.searchMode || !this.searchWorkspace) return new ProjectSearchResult();
    const rows=this.directorySearchRows;
    const visible = rows.filter((row:ResourceRow) => {
      const data = row.data();
      return !Wire.bool(data,'archived') && !Wire.bool(data,'hidden_by_directory') && !Wire.bool(data,'assistant');
    });
    const quick = this.quick || 'all', machine = this.catalog.browser.machine;
    const projection = this.searchWorkspace.repositoryQuery(this.model,visible,this.catalog.browser.query,quick,machine);
    return projectSearchResult(this.model,rows,projection,this.searchCards,machine,quick,coreRowId);
  }
  @Computed get showGroupedResults():boolean {
    return !!this.groupedSearchRows && this.compact && this.page==='projects' && this.searchMode && this.searchGroups.groupingMode!=='repo';
  }
  @Monitor('directorySearchRows') projectSearchRowsChanged():void { this.syncSearchGroups(); }
  private releaseSearchGroups():void {
    this.groupLoad++; this.groupOpening=undefined; this.searchTasks.clear(); this.searchGroups.hide();
  }
  private prepareSearchGroups():void {
    if (!this.groupedSearchRows || !this.searchMode || this.page!=='projects') return;
    const model=this.model, scope=model.scope(), generation=model.connectionGeneration(), revision=++this.groupLoad;
    if (!this.groupOpening && !this.searchGroups.isBoundTo(model)) {
      const opening=this.searchGroups.open(model); this.groupOpening=opening;
      opening.finally(()=>{if (this.groupOpening===opening) this.groupOpening=undefined;}).catch(()=>{});
    }
    Promise.all([this.groupOpening || Promise.resolve(),this.searchTasks.refresh(model)]).then(() => {
      if (this.groupLoad!==revision || !this.searchMode || this.model!==model || !model.owns(generation,scope)) return;
      this.searchGroups.updateGroupingSupport(this.searchTasks.supportsTodos,this.searchTasks.supportsDirections);
      const shared=this.searchWorkspace;
      if (!this.catalog.browser.searchGrouping) this.catalog.browser.searchGrouping=shared && shared.isBoundTo(model) ? shared.groupingMode : 'repo';
      this.syncSearchGroups();
    }).catch(() => {});
  }
  private syncSearchGroups():void {
    if (!this.groupedSearchRows || !this.searchMode || !this.searchGroups.isBoundTo(this.model)) return;
    const rows=this.directorySearchRows.filter((row:ResourceRow) => !Wire.bool(row.data(),'hidden_by_directory'));
    this.searchGroups.setProjectSearch(this.model,rows,this.catalog.browser.query,this.quick || 'all',
      this.catalog.browser.machine,this.catalog.browser.searchGrouping || 'repo',this.spaceDecisions);
  }
  private selectSearchGrouping(key:string):void {
    if (!this.searchGroups.groupingOptions().includes(key)) return;
    this.catalog.browser.searchGrouping=key;
    if (this.searchWorkspace?.isBoundTo(this.model)) {
      this.searchWorkspace.updateGroupingSupport(this.searchTasks.supportsTodos,this.searchTasks.supportsDirections);
      this.searchWorkspace.setGroupingMode(key);
    }
    this.syncSearchGroups();
  }
  @Builder searchGroupingMenu() {
    Menu() {
      ForEach(this.searchGroups.groupingOptions(),(key:string) => {
        MenuItem({content:tr(agentGroupingLabels[key])}).selected(this.searchGroups.groupingMode===key).selectIcon(true)
          .onClick(()=>this.selectSearchGrouping(key)).id('projects.grouping.'+key)
      },(key:string) => key)
      if (this.hosts.length>1 || this.catalog.browser.machine)
        MenuItem({content:tr('agent_filter_machine'),builder:()=>{this.machineMenu();}}).id('projects.machine')
    }
  }
  @Builder searchGrouping() {
    Button({type:ButtonType.Circle}) { PhoneGroupingGlyph({controlHeight:this.quickHeight()}) }
      .width(this.quickHeight()).height(this.quickHeight()).padding(0).backgroundColor(Color.Transparent)
      .accessibilityText(tr('agent_grouping_current').replace('{group}',tr(agentGroupingLabels[this.searchGroups.groupingMode])))
      .bindMenu(this.searchGroupingMenu).id('projects.grouping')
  }
  private quickHeight():number { return PhoneStyle.formSize(44,57.7,nativeEnvironment.fontScale); }
  private selectQuick(key:string):void {
    if ((this.quick || 'all') === key) return;
    this.quick = key === 'all' ? '' : key; this.catalog.browser.searchQuick=this.quick; this.syncSearchGroups();
    this.catalog.browser.resetSearchFolds(); this.catalog.browser.remember(this.route(),'');
    if (!this.showGroupedResults && this.lines.length) this.scroller.scrollToIndex(0,false,ScrollAlign.START);
  }
  @Builder machineFilter() {
    Button({type:ButtonType.Circle}) {
      SymbolGlyph(Symbols.filter).fontSize('17vp').fontColor([this.catalog.browser.machine ? Colors.link : Colors.ink])
    }.width(this.searchMode ? this.quickHeight() : 36).height(this.searchMode ? this.quickHeight() : 36).padding(0).backgroundColor(Color.Transparent)
      .responseRegion(this.searchMode ? {x:0,y:0,width:'100%',height:'100%'} : {x:-4,y:-4,width:44,height:44}).accessibilityText(tr('agent_filter_machine'))
      .accessibilityDescription(this.hosts.find((host:ProjectHost) => host.key === this.catalog.browser.machine)?.name || tr('project_all_machines'))
      .bindMenu(this.machineMenu).id('projects.machine')
  }
  @Builder searchEntry() {
    Row({space:8}) {
      Button({type:ButtonType.Capsule}) {
        Row({space:7}) {
          SymbolGlyph($r('sys.symbol.magnifyingglass')).fontSize('15vp').fontColor([Colors.secondary])
          Text(tr('project_search')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).maxLines(1)
        }.width('100%')
      }.height(40).layoutWeight(1).padding({left:12,right:12}).backgroundColor(Colors.surface)
        .accessibilityText(tr('project_search')).onClick(() => {
          this.setSearchMode(true);
          setTimeout(() => { if (this.searchMode) this.getUIContext().getFocusController().requestFocus('projects.search'); },65);
        }).id('projects.search')
      if (this.hosts.length > 1 || this.catalog.browser.machine) this.machineFilter()
    }.width('100%').padding({bottom:8})
  }
  private compactSearchIcon(): string {
    return String(PhoneStyle.formSize(20, 27.33, nativeEnvironment.fontScale)) + 'vp';
  }

  private searchTextSize(): string {
    return this.compact ? String(PhoneStyle.formSize(17, 22.2, nativeEnvironment.fontScale) * Math.min(1, nativeEnvironment.fontScale)) + 'vp' : '16fp';
  }

  @Builder searchAndFilter() {
    Column({space:this.searchMode ? 20 : 6}) {
      Row({space:this.searchMode ? 11 : 8}) {
        Search({value:this.catalog.browser.query,placeholder:tr(this.compact ? 'agents_search_prompt' : 'project_search'),controller:this.searchField})
          .height(this.compact ? 44 : Math.max(40,21*nativeEnvironment.fontScale+12)).margin(this.compact ? 0 : {top:8,bottom:8}).layoutWeight(1)
          .textFont({size:this.searchTextSize()}).placeholderFont({size:this.searchTextSize()})
          .searchIcon(this.compact ? {size:this.compactSearchIcon(),color:$r('app.color.persona_switcher_ink')} : undefined)
          .cancelButton(this.compact ? {style:CancelButtonStyle.INPUT,icon:{
            size:PhoneStyle.formSize(24,32,nativeEnvironment.fontScale),color:$r('app.color.persona_switcher_ink'),src:$r('app.media.phone_search_clear')
          }} : undefined)
          .searchButton('').borderRadius(Tokens.radiusPanel).fontColor(Colors.ink)
          .backgroundColor(this.compact && Colors.styleID === 'glass' ? $r('app.color.phone_project_search_field') : Colors.surface)
          .onFocus(() => this.setSearchMode(true)).onClick(() => this.setSearchMode(true)).onChange((value:string) => this.search(value))
          .onSubmit(() => this.searchField.stopEditing()).id('projects.search')
        if (this.searchMode) {
          Button({type:ButtonType.Circle}) { SymbolGlyph($r('sys.symbol.xmark')).fontSize(String(PhoneStyle.formSize(25.5,32,nativeEnvironment.fontScale))+'vp').fontColor([Colors.ink]) }
            .width(44).height(44).padding(0).backgroundColor(Colors.control)
            .accessibilityText(tr('cancel')).onClick(() => this.setSearchMode(false)).id('projects.search_cancel')
        } else if (this.hosts.length > 1 || this.catalog.browser.machine) this.machineFilter()
      }.width('100%')
      if (this.searchMode) Row() {
        Scroll() {
          Row({space:4}) {
            ForEach(projectQuickFilters.filter((key:string) => key === 'all' || key === this.quick ||
              Wire.number(this.queryResult.counts,key) > 0),(key:string) => {
              Button({type:ButtonType.Capsule}) {
                Row({space:4}) {
                  if (key !== 'all' && key !== 'feishu' && key !== 'terminal') Circle().width(5).height(5).fill(this.activityTint(projectQuickLabels[key]))
                  Text(tr(projectQuickLabels[key])).fontSize(Tokens.interfaceFontVp(15,nativeEnvironment.fontScale))
                    .fontWeight((this.quick || 'all') === key ? FontWeight.Bold : FontWeight.Normal)
                    .fontColor((this.quick || 'all') === key ? Colors.link : Colors.ink).maxLines(1)
                  if (key !== 'all') Text(String(Wire.number(this.queryResult.counts,key)))
                    .fontSize(Tokens.interfaceFontVp(15,nativeEnvironment.fontScale)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary)
                }.padding({left:8,right:8,top:4,bottom:4}).borderRadius(Tokens.radiusCapsule)
                  .backgroundColor((this.quick || 'all') === key ? Colors.selection : Color.Transparent)
              }.height(this.quickHeight()).constraintSize({minWidth:this.quickHeight()}).padding(0).backgroundColor(Color.Transparent)
                .accessibilitySelected((this.quick || 'all') === key).accessibilityText(tr(projectQuickLabels[key]) +
                  (key === 'all' ? '' : ' ' + String(Wire.number(this.queryResult.counts,key))))
                .onClick(() => this.selectQuick(key)).id('projects.quick.'+projectQuickLabels[key])
            },(key:string) => key)
          }.padding({left:4,right:4})
        }.layoutWeight(1).height(this.quickHeight()).align(Alignment.Start).scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).id('projects.quick')
        Divider().vertical(true).strokeWidth(0.5).height(22*this.quickHeight()/44).color(Colors.divider)
        this.searchGrouping()
      }.width('100%').height(this.quickHeight()).backgroundColor(Colors.grouped).borderRadius(Tokens.radiusCapsule).clip(true)
        .outlineWidth(0.5).outlineColor(Colors.divider).outlineRadius(Tokens.radiusCapsule)
    }.width('100%').padding({bottom:8})
  }
  @Builder projectMenu(key: string) {
    Menu() {
      MenuItem({content:tr('project_open')}).onClick(() => this.onProject(key)).id('projects.open_project')
      ForEach(this.projectMenuRoots(key), (tree: ProjectWorktree) => {
        MenuItemGroup() {
          MenuItem({content:tr('project_view_worktree')}).onClick(() => this.chooseTree(tree.key)).id('tree.action.open')
          if (tree.canStart) MenuItem({content:tr('project_start_here')}).onClick(() => this.startFromTree(tree.key)).id('tree.action.start')
          if (tree.permits('create')) MenuItem({content:tr('tree_create')}).onClick(() => this.openEditor(tree,'create')).id('tree.action.create')
        }
      }, (tree: ProjectWorktree) => this.headerIdentity(tree))
    }
  }
  private agentContext(agent: ProjectAgent, paneName: string = '', starter: boolean = false): string {
    const special = [2, 4, 5, 6].includes(agent.rank) || agent.state === 'offline';
    return [special ? tr(agent.stateLabel) : '', paneName || agent.tab, starter ? tr('session_space_starter') : '', agent.model || agent.runtime]
      .filter((value: string) => !!value).join(' · ');
  }
  @Computed get floatingAgentCards():boolean {
    return !!this.agentRepositories && deviceLook.agentDisplay==='cards' && this.model.style!=='beautiful';
  }
  @Builder cellActions(agent:ProjectAgent,treeKey:string) {
    if(this.agentRepositories && this.agentActions){this.agentActions(agent.row,agent.spaceName)}
    else {this.actionMenu(treeKey)}
  }
  private useHerdrTabFor(agent: ProjectAgent): boolean {
    return this.model.directoryOwnerFor(agent.row)?.useHerdrTabName ?? this.model.useHerdrTabName;
  }
  private displayAgentTitle(agent: ProjectAgent): string {
    return (this.useHerdrTabFor(agent) && agent.herdrTabName ? agent.herdrTabName : agent.row.title) || tr('agent_untitled');
  }
  @Builder agentCell(agent:ProjectAgent, treeKey:string, level:number, paneName:string = '', starter:boolean = false) {
    Row() {
      AgentDirectoryRowContent({agent:agent,display:this.agentRepositories?
        (deviceLook.agentDisplay==='list'?'list':this.model.style==='beautiful'?'beautiful':deviceLook.agentDisplay):'avatar',
        activityList:!!this.agentRepositories,muted:this.agentRepositories?this.isMuted(agent.row):false, age:this.age(agent),
        place:deduplicateAgentPlace(this.displayAgentTitle(agent), paneName || agent.tab), showsSource:!!(agent.tab || agent.runtime),
        starter:starter,narrow:this.narrowOutline(),
        leftInset:this.agentRepositories && deviceLook.agentDisplay==='list' ? PhoneStyle.inset+6+18*this.outlineControlScale()*Math.min(level,3) : this.treeInset(level)+6,
        controlScale:this.outlineControlScale(),
        fontScale:nativeEnvironment.fontScale,tint:this.tint(agent),useHerdrTabName:this.useHerdrTabFor(agent)})
    }.width('100%')
      .accessibilityGroup(true).accessibilityText([this.displayAgentTitle(agent),agent.preview,tr(agent.stateLabel),this.agentContext(agent,paneName,starter),agent.todoTitle,this.age(agent)].filter((v:string) => !!v).join(' · '))
      .accessibilitySelected(this.adjacentItem === 'session:' + agent.row.key())
      .onClick(() => this.chooseAgent(agent)).bindContextMenu(this.cellActions(agent,treeKey),ResponseType.LongPress).id(this.agentRepositories ? 'agents.repo.session' : 'projects.inline_session')
  }
  @Builder foldChevron(open:boolean) {
    SymbolGlyph(Symbols.down).fontSize('12vp').fontColor([Colors.secondary])
      .rotate({ angle: open ? 0 : -90 })
      .animation(Motion.state(nativeEnvironment.reduceMotion))
  }
  // A virtual row can outlive the projection object that created it. Keep only its scoped key in this builder.
  @Builder tabLine(key: string) {
    Row({ space: 8 }) {
      Stack() { Circle().width(7).height(7).fill(this.tabTint(key)) }.width(20)
      if (this.narrowOutline()) {
        Column({space:2}) {
          Text(this.tabRows.get(key)?.tabTitle || '').fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
          if (this.tabRows.get(key)?.tabLead?.row.title) Text(this.tabRows.get(key)?.tabLead?.row.title || '').fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).maxLines(3)
          Text(tr('project_agent_count').replace('{count}', String(this.tabRows.get(key)?.count || 0))).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
      } else {
        Text(this.tabRows.get(key)?.tabTitle || '').fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
          .constraintSize({ maxWidth: '40%' }).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
        Text(this.tabRows.get(key)?.tabLead?.row.title || '').fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).layoutWeight(1)
          .maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
        Text(tr('project_agent_count').replace('{count}', String(this.tabRows.get(key)?.count || 0)))
          .fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary).maxLines(1)
      }
      SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary]).rotate({angle:this.tabOpen(key) ? 90 : 0})
        .visibility(this.filteringText() ? Visibility.None : Visibility.Visible)
    }.width('100%').constraintSize({minHeight:Math.max(46,44*this.outlineControlScale())}).padding({left:this.treeInset(this.tabRows.get(key)?.level || 0)+6,right:PhoneStyle.inset})
      .accessibilityGroup(true).accessibilityText(this.tabDescription(key)).onClick(() => this.toggleTab(key)).id('projects.tab')
  }
  @Builder countItems(key: string) {
    ForEach(this.hostKinds(key), (kind: string) => {
      Row({ space: 4 }) {
        Circle().width(6).height(6).fill(this.activityTint(kind))
        Text(String(this.hostActivity.get(key)?.get(kind) || 0)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary)
          .id('projects.host_count.'+kind)
      }
    }, (kind: string) => kind)
  }
  @Builder counts(key: string) {
    if (this.narrowOutline()) {
      Flex({wrap:FlexWrap.Wrap, space:{main:LengthMetrics.vp(8),cross:LengthMetrics.vp(4)}}) { this.countItems(key) }.width('100%')
    } else {
      Row({space:8}) { this.countItems(key) }
    }
  }
  @Builder headerLine(line:ProjectLine) {
    Row({space:8}) {
      if (line.kind === 'host') {
        Stack() { SymbolGlyph(Symbols.device).fontSize('17vp').fontColor([Colors.ink]) }.width(29).height(29).backgroundColor(Colors.surface).borderRadius(Tokens.radiusControl)
        Column({ space: 3 }) {
          Text(line.project.machine).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
            .maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).id('projects.host_name')
          if (this.narrowOutline()) this.counts(line.project.hostKey)
        }.layoutWeight(1).alignItems(HorizontalAlign.Start)
        if (!this.narrowOutline()) this.counts(line.project.hostKey)
      } else {
        SymbolGlyph(Symbols.folder).fontSize('17vp').fontColor([Colors.secondary]).width(18)
        Text(line.project.title).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Bold)
          .fontColor(Colors.ink).layoutWeight(1).maxLines(this.narrowOutline() ? 3 : 1).textOverflow({overflow:TextOverflow.Ellipsis})
      }
      this.foldChevron(line.open)
    }.width('100%').constraintSize({minHeight:44*this.outlineControlScale()+(line.kind === 'host' ? 4 : 0)})
      .padding({left:PhoneStyle.inset,right:PhoneStyle.inset,top:line.kind === 'host' ? 14 : 4,bottom:4})
      .accessibilityGroup(true).accessibilityText([line.kind === 'host' ? line.project.machine : line.project.title,
        line.kind === 'host' ? this.hostSummary(line.project.hostKey) : this.count(line.count,'project_sessions'),
        tr(line.open ? 'agent_collapse' : 'agent_expand')]
        .filter((part: string) => !!part).join(' · '))
      .onClick(() => { Haptics.tap(); this.openHeader(line); }).id(this.agentRepositories ? (line.kind === 'host' ? 'agents.repo.machine' : 'agents.project') :
        line.kind === 'host' ? 'projects.host' : 'projects.project')
  }
  private treeSummary(line:ProjectLine):string {
    return [line.tree!.main ? tr('project_checkout_primary') : '', this.treeDetail(line.tree!),
      line.count ? this.agentCountText(line.count) : ''].filter((part:string) => !!part).join(' · ');
  }
  // A worktree inferred from sessions has no host workspace to open. Its whole row folds the group.
  private treeRowFolds(line:ProjectLine):boolean {
    return !this.filteringText() && !line.tree!.source && (line.count > 0 || line.tree!.children.length > 0);
  }
  @Builder treeLine(line:ProjectLine) {
    Row({space:0}) {
      Row({space:6}) {
        Stack() {
          if (line.tree!.main) SymbolGlyph($r('sys.symbol.house')).fontSize('14vp').fontColor([Colors.secondary])
        }.width(18)
        if (this.narrowOutline()) {
          Column({space:3}) {
            Row({space:5}) {
              Text(line.tree!.title).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(line.tree!.online && line.tree!.activeCount ? Colors.ink : Colors.secondary).layoutWeight(1)
              if (line.tree!.pinnedAt > 0) SymbolGlyph($r('sys.symbol.pin')).fontSize('12vp').fontColor([Colors.link])
            }.width('100%')
            if (this.treeSummary(line)) Text(this.treeSummary(line)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(4)
          }.alignItems(HorizontalAlign.Start).layoutWeight(1)
        } else {
          Text(line.tree!.title).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(line.tree!.online && line.tree!.activeCount ? Colors.ink : Colors.secondary)
            .constraintSize({maxWidth:this.treeDetail(line.tree!) ? '56%' : '74%'}).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
          if (line.tree!.pinnedAt > 0) SymbolGlyph($r('sys.symbol.pin')).fontSize('12vp').fontColor([Colors.link])
          if (line.tree!.main) Text(tr('project_checkout_primary')).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11))
            .fontWeight(FontWeight.Medium).fontColor(Colors.link)
            .backgroundColor(Colors.selection).borderRadius(Tokens.radiusBadge).padding({left:6,right:6,top:2,bottom:2}).maxLines(1)
          if (this.treeDetail(line.tree!)) {
            Row() {
              Text(this.treeDetail(line.tree!)).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11))
                .fontFamily(line.tree!.workspaceKind === 'git' ? 'monospace' : 'sans-serif')
                .fontWeight(FontWeight.Medium)
                .fontColor(line.tree!.workspaceKind === 'git' && !line.tree!.branch ? Colors.working : Colors.secondary)
                .padding({left:6,right:6,top:1.5,bottom:1.5})
                .backgroundColor(Colors.control)
                .border({width:0.5,color:Colors.divider,radius:7})
                .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
            }.layoutWeight(1).justifyContent(FlexAlign.Start).clip(true)
          } else Blank().layoutWeight(1)
          if (line.count) Text(!line.tree!.activeCount ? tr('project_worktree_idle') : this.agentCountText(line.count))
            .fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary).maxLines(1)
        }
        if (this.treeRowFolds(line)) this.foldChevron(line.open)
      }.layoutWeight(1).constraintSize({minHeight:44*this.outlineControlScale()})
        .padding({left:this.treeInset(line.level),right:this.treeRowFolds(line) || this.filteringText() ? PhoneStyle.inset : 3})
        .accessibilityGroup(true)
        .accessibilityText([line.tree!.title,this.treeDetail(line.tree!),line.tree!.cwd,this.count(line.count,'project_sessions'),
          this.treeRowFolds(line) ? tr(line.open ? 'agent_collapse' : 'agent_expand') : ''].filter((v:string) => !!v).join(' · '))
        .onClick(() => { Haptics.tap(); if (this.treeRowFolds(line)) this.toggle(line); else this.openTree(line.tree!); })
        .bindContextMenu(this.actionMenu(line.tree!.key),ResponseType.LongPress).id(this.agentRepositories ? 'agents.worktree' : 'projects.worktree')
      if (line.tree!.source && !this.filteringText() && (line.count || line.tree!.children.length)) Button({type:ButtonType.Normal}) { this.foldChevron(line.open) }
        .width(44*this.outlineControlScale()).height(44*this.outlineControlScale()).padding(0).backgroundColor(Color.Transparent).accessibilityText(tr(line.open ? 'agent_collapse' : 'agent_expand'))
        .onClick(() => { Haptics.tap(); this.toggle(line); }).id('projects.fold')
    }.width('100%')
      .id('projects.tree_owner.' + line.tree!.machine + '.' + line.tree!.source + '.' + line.tree!.sourceId)
  }
  @Builder subsidiaryLine(line:ProjectLine) {
    Row({space:8}) {
      SymbolGlyph(line.kind === 'children' ? Symbols.folder : line.kind === 'more' ? Symbols.agents : Symbols.check).fontSize('14vp').fontColor([Colors.secondary]).width(18)
      Text(line.kind === 'more' ? tr('project_show_all') + ' ' + this.count(line.count,'project_sessions') :
        line.kind === 'children' ? this.count(line.count,'project_children') : tr('project_idle_named').replace('{count}',String(line.count)) + (line.preview ? ' · '+line.preview : ''))
        .fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(line.kind === 'more' ? Colors.link : Colors.secondary).layoutWeight(1)
      if (!this.filteringText() || line.kind === 'more') this.foldChevron(line.kind !== 'more' && line.open)
    }.width('100%').constraintSize({minHeight:44*this.outlineControlScale()}).padding({left:this.treeInset(line.level),right:PhoneStyle.inset})
      .accessibilityGroup(true).onClick(() => {
        if (line.kind === 'more') this.outlineBrowser.showAll(line.tree!.key);
        else if (!this.filteringText()) this.toggle(line);
      })
      .id(line.kind === 'children' ? 'projects.children' : line.kind === 'more' ? 'projects.show_all' : 'projects.idle_agents')
  }
  @Builder lineContent(line:ProjectLine) {
    if (line.kind === 'host') this.headerLine(line)
    else if (line.kind === 'idle-projects') Text(tr('project_idle_repositories')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary)
      .padding({left:6,right:8,top:10,bottom:5})
    else if (line.kind === 'project') Row() { this.headerLine(line) }.width('100%').bindContextMenu(this.projectMenu(line.project.key),ResponseType.LongPress)
    else if (line.kind === 'tree') this.treeLine(line)
    else if (line.kind === 'tab') this.tabLine(line.key)
    else if (line.kind === 'session') this.agentCell(line.agent!,line.tree!.key,line.level,line.paneName,line.starter)
    else this.subsidiaryLine(line)
  }
  private outlineItemNeedsDivider(entry: RepeatItem<ProjectLine>): boolean {
    if (entry.item.first || this.floatingAgentCards) return false;
    if (entry.item.kind === 'host' || entry.item.kind === 'idle-projects' || entry.item.kind === 'project') return false;
    if (entry.item.kind === 'tree') return true;
    if (entry.item.kind === 'session') {
      return entry.index > 0 && this.lines[entry.index - 1]?.kind === 'session';
    }
    return true;
  }
  private outlineDividerLeftInset(line: ProjectLine): number {
    if (line.kind === 'tree') return PhoneStyle.inset;
    if (line.kind === 'session') return this.treeInset(line.level) + 54;
    return this.treeInset(line.level);
  }
  @Builder outlineItems() {
    Repeat(this.lines).each((entry:RepeatItem<ProjectLine>) => {
      ListItem() {
        Column() {
          if (this.outlineItemNeedsDivider(entry)) {
            Divider().strokeWidth(0.5).color(Colors.divider)
              .margin({ left: this.outlineDividerLeftInset(entry.item), right: PhoneStyle.inset })
          }
          this.lineContent(entry.item)
        }.width('100%').padding({left:this.floatingAgentCards && entry.item.kind!=='session'?16:0,
          right:this.floatingAgentCards && entry.item.kind!=='session'?16:0}).alignItems(HorizontalAlign.Start)
          .backgroundColor(entry.item.kind === 'host' || entry.item.kind === 'idle-projects' ? Color.Transparent :
            entry.item.agent && this.adjacentItem === 'session:' + entry.item.agent!.row.key() ||
            entry.item.kind === 'tree' && this.adjacentItem === 'tree:' + entry.item.tree!.key ? Colors.selection : Colors.surface)
          .border(entry.item.kind === 'host' || entry.item.kind === 'idle-projects' || this.floatingAgentCards ? { width: 0 } : {
            width: {
              top: entry.item.first ? PhoneStyle.surfaceEdgeWidth(0.5) : 0,
              bottom: entry.item.last ? PhoneStyle.surfaceEdgeWidth(0.5) : 0,
              left: PhoneStyle.surfaceEdgeWidth(0.5),
              right: PhoneStyle.surfaceEdgeWidth(0.5)
            },
            color: PhoneStyle.cardBorder(nativeEnvironment.dark),
            radius: {
              topLeft: entry.item.first ? PhoneStyle.cardRadius : 0,
              topRight: entry.item.first ? PhoneStyle.cardRadius : 0,
              bottomLeft: entry.item.last ? PhoneStyle.cardRadius : 0,
              bottomRight: entry.item.last ? PhoneStyle.cardRadius : 0
            }
          })
          .borderRadius({topLeft:entry.item.first ? PhoneStyle.cardRadius : 0,topRight:entry.item.first ? PhoneStyle.cardRadius : 0,
            bottomLeft:entry.item.last ? PhoneStyle.cardRadius : 0,bottomRight:entry.item.last ? PhoneStyle.cardRadius : 0})
          .shadow(entry.item.last && entry.item.kind !== 'host' && entry.item.kind !== 'idle-projects' && !this.floatingAgentCards ?
            PhoneStyle.cardElevation(nativeEnvironment.dark) : { radius: 0, color: Color.Transparent })
          .margin({bottom:entry.item.last && this.lines[entry.index+1]?.kind !== 'host' ? 12 : 0})
      }
    }).key((line:ProjectLine) => line.key).virtualScroll({totalCount:this.lines.length,reusable:false})
  }
  @Builder repositoryList() {
    Column() {
      if (this.page === 'projects' && (!this.compact || this.searchMode)) {
        Column() { this.searchAndFilter() }.width('100%').padding({left:PhoneStyle.page,right:PhoneStyle.page})
      }
      if (this.showGroupedResults && this.groupedSearchRows) {
        Column() { this.groupedSearchRows(this.searchGroups,this.searchTasks,this.spaceDecisions) }.layoutWeight(1).width('100%')
      } else Column() {
        List({scroller:this.scroller}) {
          if (this.compact && this.page === 'projects' && !this.searchMode) ListItem() { this.searchEntry() }
          if (this.page === 'project' && this.project()) ListItem() {
            PhoneSectionTitle({ text: this.project()!.machine, trailing: this.count(this.project()!.trees.length, 'project_worktrees') })
          }.id('projects.repository_summary')
          this.outlineItems()
          if (!this.lines.length && !this.catalog.loading) ListItem() {
            this.message(tr(this.catalog.browser.query || this.catalog.browser.machine ? 'no_results' :
              this.catalog.partial ? 'project_catalog_unavailable' : 'project_empty'))
          }
        }.layoutWeight(1).width('100%').scrollBar(BarState.Auto).maintainVisibleContentPosition(true)
          .onAreaChange(() => this.restorePosition())
          .contentStartOffset(this.compact && this.page === 'projects' && !this.searchMode ? this.topContentInset : 0)
          .onScrollIndex((start:number) => this.rememberPosition(start)).id(this.page === 'projects' ? 'projects.list' : 'projects.worktrees')
      }.layoutWeight(1).width('100%').padding({left:PhoneStyle.page,right:PhoneStyle.page})
    }.width('100%').height('100%')
  }
  private pathLabel(tree: ProjectWorktree): string {
    const machine = this.model.directoryMachines.find((row: ResourceRow) => row.group === tree.group && row.manager === tree.manager && row.id === tree.machine);
    const home = machine ? Wire.text(machine.data(),'home').replace(/\/+$/,'') : '';
    return home && (tree.cwd === home || tree.cwd.startsWith(home+'/')) ? '~'+tree.cwd.slice(home.length) : tree.cwd;
  }
  private spaceCaptionScale(points:number): number {
    return Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(points));
  }
  @Builder detailHeader(tree: ProjectWorktree) {
    ListItem() {
      Column({space:9}) {
        Flex({wrap:FlexWrap.Wrap,alignItems:ItemAlign.Center,space:{main:LengthMetrics.vp(8),cross:LengthMetrics.vp(5)}}) {
          Button({type:ButtonType.Capsule}) {
            Row({space:6}) {
              SymbolGlyph(Symbols.device).fontSize(String(19*this.spaceCaptionScale(13))+'vp').fontColor([Colors.link])
                .width(22.5*this.spaceCaptionScale(13)).height(15.5*this.spaceCaptionScale(13))
              Text(tree.machineTitle).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(600).fontColor(Colors.link).maxLines(2)
              SymbolGlyph(Symbols.expand).fontSize(String(11*this.spaceCaptionScale(12))+'vp').fontColor([Colors.link])
                .width(9.5*this.spaceCaptionScale(12)).height(12*this.spaceCaptionScale(12))
            }
          }.padding({left:12,right:12,top:6,bottom:6}).backgroundColor(Colors.selection)
            .responseRegion({x:0,y:-6,width:'100%',height:'150%'}).accessibilityGroup(true).accessibilityText(tree.machineTitle)
            .onClick(() => this.onMachine(tree)).id('projects.machine_spaces')
          if (tree.cwd) Text(this.pathLabel(tree)).fontColor(Colors.secondary).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
            .maxLines(this.narrowOutline() ? 4 : 1).wordBreak(WordBreak.BREAK_ALL)
            .textOverflow({overflow:TextOverflow.Ellipsis}).copyOption(CopyOptions.LocalDevice).accessibilityText(tree.cwd)
            .id('projects.detail_owner.' + tree.machine + '.' + tree.source + '.' + tree.sourceId)
          if (!this.compact) this.more(tree)
        }.width('100%')
        if (tree.parentKey) Button({type:ButtonType.Normal}) {
          Row({space:5}) {
            SymbolGlyph(Symbols.collapse).fontSize('12vp').fontColor([Colors.link])
            Text(tree.breadcrumb).fontColor(Colors.link).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).maxLines(2)
          }
        }.constraintSize({minHeight:44}).padding(0).backgroundColor(Color.Transparent).accessibilityText(tr('project_open_parent') + ' · ' + tree.breadcrumb)
          .onClick(() => this.chooseParent(tree.key)).id('projects.parent')
        if (!tree.online) Text(tr('offline')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
      }.alignItems(HorizontalAlign.Start).padding({top:8,bottom:16}).width('100%')
    }
  }
  private openSpaceCard(treeKey:string,sessionKey:string):void {
    if (this.page !== 'project_tree' || this.target !== treeKey) return;
    const line = this.detailSessions.find((entry:ProjectLine) => entry.agent?.row.key() === sessionKey);
    if (line?.agent) this.chooseAgent(line.agent);
  }
  @Builder spaceStatus(agent:ProjectAgent) {
    Stack() {
      if (agent.rank === 4) {
        SymbolGlyph($r('sys.symbol.clock')).fontSize('15vp').fontColor([this.tint(agent)])
      } else {
        if (agent.state === 'working' || agent.state === 'needs_you' && !nativeEnvironment.reduceMotion) {
          Circle().width(15).height(15).fill(Color.Transparent).stroke(this.tint(agent)).strokeWidth(1.5).opacity(0.45)
        }
        Circle().width(9).height(9).fill(this.tint(agent))
      }
    }.width(16).height(16).flexShrink(0).accessibilityLevel('no').id('projects.space_status.'+agent.row.id)
  }
  private spaceSlotTitle(agent:ProjectAgent,paneName:string): string {
    return [agent.tabTitle || agent.tab || agent.runtime,paneName].filter((s:string) => !!s).join(' · ') || '—';
  }
  private fitsRuntimeChip(agent:ProjectAgent,paneName:string): boolean {
    if (paneName || !agent.runtime || this.narrowOutline() || !nativeEnvironment.windowWidthPx) return false;
    const ui=this.getUIContext(), scale=this.spaceCardScale();
    const column=(ui.px2vp(nativeEnvironment.windowWidthPx)-2*PhoneStyle.page-12)/2;
    const inside=column-2*(12*scale+(agent.state==='needs_you' ? 1.5 : 0.5));
    const font=String(12*scale*Math.min(nativeEnvironment.fontScale,Tokens.interfaceScaleFor(12)))+'vp';
    const metrics=ui.getMeasureUtils();
    const tab=metrics.measureText({textContent:this.spaceSlotTitle(agent,paneName),fontSize:font,fontWeight:FontWeight.Medium});
    const runtime=metrics.measureText({textContent:agent.runtime,fontSize:font});
    // Two chip insets, an outline, two gaps and the dedicated status slot must fit together.
    return ui.px2vp(Math.ceil(tab)+Math.ceil(runtime))+14+13+2*5+16<=inside;
  }
  @Builder spaceSlot(agent:ProjectAgent,paneName:string) {
    Row({space:5}) {
      Row({space:5}) {
        Text(this.spaceSlotTitle(agent,paneName))
          .fontSize(12*this.spaceCardScale()).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(FontWeight.Medium).fontColor(agent.active ? this.tint(agent) : $r('app.color.project_card_secondary'))
          .padding({left:7,right:7,top:2,bottom:2}).backgroundColor(Colors.subtle).borderRadius(Tokens.radiusCapsule)
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1).constraintSize({maxWidth:'100%'})
        if (this.fitsRuntimeChip(agent,paneName)) Text(agent.runtime)
          .fontSize(12*this.spaceCardScale()).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor($r('app.color.project_card_secondary')).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
          .padding({left:6,right:6,top:1,bottom:1}).border({width:0.5,color:Colors.divider,radius:Tokens.radiusCapsule}).flexShrink(1)
      }.layoutWeight(1).clip(true)
      this.spaceStatus(agent)
    }.width('100%').id('projects.space_slot.'+agent.row.id)
  }
  private spaceModelTracking(model:string): string {
    if (!model || !nativeEnvironment.windowWidthPx || this.narrowOutline()) return '0vp';
    const ui=this.getUIContext(), scale=this.spaceCardScale();
    const column=(ui.px2vp(nativeEnvironment.windowWidthPx)-2*PhoneStyle.page-12)/2;
    const available=Math.floor(ui.vp2px(column-24*scale))-1;
    const points=12*scale*this.spaceCaptionScale(12), font=String(points)+'vp';
    const metrics=ui.getMeasureUtils(), natural=metrics.measureText({textContent:model,fontSize:font});
    if (natural<=available) return '0vp';
    // A small tracking adjustment accommodates differences in native font advances at the same reading size.
    // Keep genuinely long identifiers at their ordinary spacing and truncate the tail.
    const limit=points*0.02;
    const compact=metrics.measureText({textContent:model,fontSize:font,letterSpacing:String(-limit)+'vp'});
    if (compact>available || compact>=natural) return '0vp';
    return String(-limit*Math.min(1,(natural-available+1)/(natural-compact)))+'vp';
  }
  @Builder spaceModel(agent:ProjectAgent,measureOnly:boolean) {
    Text(agent.model).fontSize(12*this.spaceCardScale()).maxFontScale(Tokens.interfaceScaleFor(12))
      .fontColor($r('app.color.project_card_tertiary')).maxLines(this.narrowOutline() ? 3 : 1)
      .letterSpacing(this.spaceModelTracking(agent.model)).wordBreak(WordBreak.BREAK_ALL)
      .textOverflow({overflow:TextOverflow.Ellipsis}).visibility(measureOnly ? Visibility.Hidden : Visibility.Visible)
      .accessibilityLevel('no').id(measureOnly ? '' : 'projects.space_model.'+agent.row.id)
  }
  @Builder spaceFooter(agent:ProjectAgent) {
    if (agent.model) {
      Row() { this.spaceModel(agent,false) }.width('100%')
        // GridCol measures this overlay against its inner width, while positioning it at the outer origin.
        // Recover the two stroke widths so the model has the same visual 12-point inset on both sides.
        .padding({left:12*this.spaceCardScale(),right:12*this.spaceCardScale()-(agent.state==='needs_you' ? 3 : 1),bottom:12*this.spaceCardScale()})
        .hitTestBehavior(HitTestMode.None)
    }
  }
  @Builder spaceCard(agent:ProjectAgent,paneName:string,starter:boolean,treeKey:string) {
    GridCol({span:1}) {
      Column({space:8*this.spaceCardScale()}) {
        this.spaceSlot(agent,paneName)
        Text(this.displayAgentTitle(agent)).fontSize(15*this.spaceCardScale()).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(600)
          .fontColor(Colors.ink).maxLines(this.narrowOutline() ? 4 : 2).textOverflow({overflow:TextOverflow.Ellipsis}).id('projects.space_title.'+agent.row.id)
        Text([tr(agent.stateLabel),starter ? tr('session_space_starter') : '',this.pendingDescription(agent)]
          .filter((part:string) => !!part).join(' · ')).fontSize(12*this.spaceCardScale()).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(600).fontColor(agent.active ? this.tint(agent) : $r('app.color.project_card_secondary'))
        if (agent.preview) Text(agent.preview).fontSize(12*this.spaceCardScale()).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor($r('app.color.project_card_secondary')).maxLines(this.narrowOutline() ? 5 : 3).textOverflow({overflow:TextOverflow.Ellipsis})
        Blank().layoutWeight(1)
        // The intrinsic footer reserves room during measurement. Its visible copy follows the stretched grid row below.
        if (agent.model) this.spaceModel(agent,true)
      }.width('100%').constraintSize({minHeight:174*this.spaceCardScale()}).padding(12*this.spaceCardScale()).alignItems(HorizontalAlign.Start)
    }.backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
      .border({width:agent.state === 'needs_you' ? 1.5 : PhoneStyle.surfaceEdgeWidth(0.5),color:agent.state === 'needs_you' ? $r('app.color.project_card_needs_border') : PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      .overlay(this.spaceFooter(agent),{align:Alignment.BottomStart})
      .accessibilityGroup(true).accessibilityDescription(agent.model).onClick(() => this.openSpaceCard(treeKey,agent.row.key())).id('projects.session')
  }
  @Builder sessions() {
    List() {
      ForEach(this.detailTree ? [this.detailTree!] : [], (tree: ProjectWorktree) => {
        this.detailHeader(tree)
      }, (tree: ProjectWorktree) => this.headerIdentity(tree))
      ListItem() {
        GridRow({columns:this.narrowOutline() ? 1 : 2,gutter:{x:12,y:12}}) {
          ForEach(this.detailSessions,(line:ProjectLine) => {
            this.spaceCard(line.agent!,line.paneName,line.starter,this.target)
          },(line:ProjectLine) => JSON.stringify([line.key,line.agent?.row.raw,line.agent?.state,line.paneName,line.starter]))
          if (this.catalog.tree(this.target)?.canStart) GridCol({span:1}) {
            Column({space:8}) {
              SymbolGlyph(Symbols.plus).fontSize('22vp').fontColor([Colors.link])
              Text(tr('new_agent')).fontSize(15*this.spaceCardScale()).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(600).fontColor(Colors.link)
            }.width('100%').constraintSize({minHeight:174*this.spaceCardScale()}).justifyContent(FlexAlign.Center)
          }.align(Alignment.Center).backgroundColor(Colors.selection).border({width:1,style:BorderStyle.Dashed,color:Colors.link,radius:PhoneStyle.cardRadius})
            .accessibilityGroup(true).onClick(() => this.startFromTree(this.target)).id('projects.start')
        }.width('100%').alignItems(ItemAlign.Stretch).id('projects.session_grid')
      }
      if (!this.detailSessions.length) ListItem() { this.message(tr('project_no_sessions')) }
      if (this.detailTree && this.detailTree!.children.length) {
        ListItem() { PhoneSectionTitle({ text: tr('project_children') }) }
        ListItemGroup() {
          ForEach(this.detailTree!.children, (child: ProjectWorktree) => {
            ListItem() { this.detailRow(child.title, child.branch || child.cwd, Symbols.folder, Colors.link, 'projects.worktree', () => this.chooseTree(child.key)) }
          }, (child: ProjectWorktree) => JSON.stringify([child.key,child.title,child.branch,child.cwd]))
        }.divider({strokeWidth:0.5,color:Colors.divider,startMargin:56})
          .backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius).clip(true)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
          .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      }
    }.width('100%').layoutWeight(1).maintainVisibleContentPosition(true)
      .padding({left:PhoneStyle.page,right:PhoneStyle.page,top:8,bottom:12}).id('projects.sessions')
  }
  @Builder spaceBar() {
    Row() {
      Button({type:ButtonType.Circle}) {
        PhoneNavigationIcon({mark:'navigationBack'})
      }.width(PhoneStyle.bar).height(PhoneStyle.bar).padding(0).backgroundColor(Colors.surface)
        .border({width:0.5,color:Colors.divider,radius:PhoneStyle.bar/2})
        .accessibilityText(tr('back')).onClick(() => this.onBack()).id('shell.back')
      Blank().layoutWeight(1)
      ForEach(this.menuEntries(this.target), (current:ProjectWorktree) => {
        this.more(current)
      }, (current:ProjectWorktree):string => this.headerIdentity(current))
    }.width('100%').padding({left:PhoneStyle.page,right:PhoneStyle.page,bottom:4})
    Text(this.detailTree?.title || '').fontSize(PhoneStyle.title).fontWeight(FontWeight.Bold).maxFontScale(Tokens.interfaceScaleFor(34))
      .fontColor(Colors.ink).maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).width('100%').textAlign(TextAlign.Start)
      .padding({left:PhoneStyle.page,right:PhoneStyle.page,top:15,bottom:6}).id('shell.page_title')
  }
  build() {
    if (this.agentRepositories) {
      ListItemGroup() {
        this.outlineItems()
        if (!this.lines.length) ListItem() {
          if (this.catalog.loading) LoadingProgress().width(28).height(28).margin(12)
          else this.message(tr(this.agentFilterActive ? 'no_results' : this.catalog.partial ? 'project_catalog_unavailable' : 'project_empty'))
        }
      }.width('100%').padding({left:this.floatingAgentCards?0:4,right:this.floatingAgentCards?0:4}).id('agents.repositories')
        .bindSheet($$this.editorShown,this.editorSheet(),{height:SheetSize.LARGE,showClose:false,dragBar:false,
          onWillDismiss:(dismiss:DismissSheetAction) => { if (!this.editor.busy) dismiss.dismiss(); },
          onDisappear:() => this.editorDismissed()})
    } else Column() {
      if (this.compact && this.detailTree) this.spaceBar()
      if (this.catalog.loading && !this.catalog.projects.length) LoadingProgress().width(29).height(29).margin(14)
      if (this.catalog.startError) this.message(tr('project_start_unavailable'))
      if (this.catalog.notice) Text(this.catalog.notice).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.link).width('100%').padding({ left: 18, right: 18, top: 4, bottom: 7 }).id('tree.result')
      if (this.catalog.partial) {
        Row({ space: 6 }) {
          Text(tr('project_catalog_partial')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.secondary).layoutWeight(1)
          Button(tr('refresh'), { buttonStyle: ButtonStyleMode.TEXTUAL }).enabled(!this.catalog.loading).onClick(() => this.onRefresh()).id('projects.retry')
        }.padding({ left: 16, right: 16 }).width('100%')
      }
      Refresh({ refreshing: $$this.refreshing }) {
        Column() {
          if (this.page === 'projects' || this.page === 'machine_projects' || this.page === 'project' && this.project()) this.repositoryList()
          else if (this.detailTree) {
            this.sessions()
          } else if (!this.catalog.loading) this.message(tr('project_gone'))
        }.width('100%').height('100%')
      }.width('100%').layoutWeight(1).onRefreshing(() => this.onRefresh())
    }.width('100%').height('100%').alignItems(HorizontalAlign.Start).backgroundColor(Colors.grouped)
      .bindSheet($$this.editorShown, this.editorSheet(), { height: SheetSize.LARGE, showClose: false, dragBar: false,
        onWillDismiss: (dismiss: DismissSheetAction) => { if (!this.editor.busy) dismiss.dismiss(); },
        onDisappear: () => this.editorDismissed() })
  }
}
